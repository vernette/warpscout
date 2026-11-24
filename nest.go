package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Set once from -through (main.go) before any tunnel is built, like
// scanInterface. Nil means the ordinary host-socket scan.
var outer *nest

type nest struct {
	tunnel tunnel
	net    *netstack.Net
	// The outer device's own addresses, not the scanning account's: the socket
	// tunnelBind opens lives in the outer tunnel and has to bind to what that
	// tunnel actually holds.
	local    netip.Addr
	run      protoRun
	endpoint string
	label    string
}

// The inner WireGuard packet travels as UDP inside the outer tunnel, so it has
// to fit the outer MTU: 20 IP + 8 UDP + 16 WireGuard header + 16 poly1305 tag.
// Too high and the handshake still completes while data silently dies.
const nestedOverhead = 60

// Set by applyAccount, like masqueAcct.
var outerAcct *account

// -through takes what -best prints, so a bare address is the exception; 2408 is
// the WARP port that answers everywhere.
var throughDefaultPort = primaryWarpPorts[0]

func parseEndpointSpec(flagName, spec string) (string, error) {
	if a, err := netip.ParseAddr(spec); err == nil {
		return net.JoinHostPort(a.Unmap().String(), strconv.Itoa(throughDefaultPort)), nil
	}
	ap, err := netip.ParseAddrPort(spec)
	if err != nil {
		return "", fmt.Errorf("%s %q is neither an IP address nor ip:port", flagName, spec)
	}
	return net.JoinHostPort(ap.Addr().Unmap().String(), strconv.Itoa(int(ap.Port()))), nil
}

func dialOuter(ctx context.Context, o options, timeout time.Duration) (*nest, error) {
	if outerAcct == nil {
		return nil, fmt.Errorf("%s holds no outer device: run \"warpscout register\" again", o.accountPath)
	}
	endpoint, err := parseEndpointSpec("-through", o.through)
	if err != nil {
		return nil, err
	}
	run, err := parseProto(o.proto)
	if err != nil {
		return nil, err
	}

	tn, err := dialOuterWithItsKeys(ctx, run, endpoint, timeout)
	if err != nil {
		return nil, err
	}
	local := outerAcct.IPv4
	if o.ipv6 {
		local = outerAcct.IPv6
	}
	addr, err := netip.ParseAddr(local)
	if err != nil {
		tn.Close()
		return nil, fmt.Errorf("outer device has no %s address: run \"warpscout register\" again", famName(o.ipv6))
	}
	return &nest{
		tunnel:   tn,
		net:      tn.stack().tnet,
		local:    addr,
		run:      run,
		endpoint: endpoint,
		label:    fmt.Sprintf("%s (%s)", endpoint, run.name),
	}, nil
}

// baseUAPI, peerUAPI, newIPStack and the config renderers all read the account
// globals (warp.go), so the outer device's key and addresses go in around those
// calls and come straight back out - everything else runs on the scanning account.
func withOuterKeys(fn func() error) error {
	priv, pub, v4, v6 := warpPrivateKey, warpPublicKey, warpAddress, warpAddressV6
	warpPrivateKey, warpPublicKey = outerAcct.PrivateKey, outerAcct.PeerPublicKey
	if outerAcct.IPv4 != "" {
		warpAddress = outerAcct.IPv4
	}
	if outerAcct.IPv6 != "" {
		warpAddressV6 = outerAcct.IPv6
	}
	defer func() {
		warpPrivateKey, warpPublicKey, warpAddress, warpAddressV6 = priv, pub, v4, v6
	}()
	return fn()
}

func dialOuterWithItsKeys(ctx context.Context, run protoRun, endpoint string, timeout time.Duration) (tunnel, error) {
	var tn tunnel
	err := withOuterKeys(func() error {
		t, err := newTunnel(run)
		if err != nil {
			return fmt.Errorf("outer tunnel: %w", err)
		}
		if !t.handshake(ctx, endpoint, timeout) {
			t.Close()
			return fmt.Errorf("outer endpoint %s did not answer over %s - pick one a plain scan found working", endpoint, run.name)
		}
		tn = t
		return nil
	})
	return tn, err
}

// The nested answer to pingHost: ICMP from inside the outer tunnel to the inner
// endpoint, which is the path the inner tunnel's packets actually take. Needs no
// privileges - it runs in netstack, like the in-tunnel ping.
func (n *nest) pingTo(dst netip.Addr, timeout time.Duration) (time.Duration, bool) {
	pc, err := n.net.DialPingAddr(netip.Addr{}, dst)
	if err != nil {
		return 0, false
	}
	defer pc.Close()

	var total time.Duration
	got := 0
	buf := make([]byte, 1500)
	for seq := 0; seq < pingProbes; seq++ {
		start := time.Now()
		if !sendEchoTo(pc, dst, seq) {
			continue
		}
		pc.SetReadDeadline(start.Add(timeout))
		if m, err := pc.Read(buf); err == nil {
			if s, ok := parseEchoSeqFor(dst, buf[:m]); ok && s == seq {
				total += time.Since(start)
				got++
			}
		}
	}
	if got == 0 {
		return 0, false
	}
	return total / time.Duration(got), true
}

func echoTypes(dst netip.Addr) (echo, reply icmp.Type) {
	if dst.Is4() {
		return ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	}
	return ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
}

func sendEchoTo(pc *netstack.PingConn, dst netip.Addr, seq int) bool {
	echo, _ := echoTypes(dst)
	wire, err := (&icmp.Message{
		Type: echo,
		Body: &icmp.Echo{ID: pingID, Seq: seq, Data: []byte("warpscout")},
	}).Marshal(nil)
	if err != nil {
		return false
	}
	_, err = pc.Write(wire)
	return err == nil
}

func parseEchoSeqFor(dst netip.Addr, b []byte) (int, bool) {
	_, reply := echoTypes(dst)
	m, err := icmp.ParseMessage(reply.Protocol(), b)
	if err != nil || m.Type != reply {
		return 0, false
	}
	e, ok := m.Body.(*icmp.Echo)
	if !ok {
		return 0, false
	}
	return e.Seq, true
}

// A conn.Bind whose socket lives in the outer tunnel's netstack instead of on
// the host, which is what nests one WARP tunnel inside another. deviceBind is
// the same shape; the difference is that gonet.UDPConn is a plain
// net.PacketConn, with no AddrPort read/write pair.
type tunnelBind struct {
	nest *nest
	conn net.PacketConn
}

func newTunnelBind(n *nest) *tunnelBind { return &tunnelBind{nest: n} }

func (b *tunnelBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	// The tunnel address, not the wildcard: gvisor picks no route for a socket
	// bound to 0.0.0.0 and every send fails with "network is unreachable".
	laddr := netip.AddrPortFrom(b.nest.local, 0)
	c, err := b.nest.net.ListenUDPAddrPort(laddr)
	if err != nil {
		return nil, 0, err
	}
	b.conn = c
	actual := uint16(c.LocalAddr().(*net.UDPAddr).Port)
	return []conn.ReceiveFunc{b.receive}, actual, nil
}

func (b *tunnelBind) receive(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	n, addr, err := b.conn.ReadFrom(bufs[0])
	if err != nil {
		// Same contract as deviceBind: any read error ends the device's receive
		// goroutine, and the only expected one is the socket closing.
		return 0, net.ErrClosed
	}
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, net.ErrClosed
	}
	sizes[0] = n
	eps[0] = &sourceEndpoint{dst: ua.AddrPort()}
	return 1, nil
}

func (b *tunnelBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	dst := net.UDPAddrFromAddrPort(ep.(*sourceEndpoint).dst)
	for _, buf := range bufs {
		if _, err := b.conn.WriteTo(buf, dst); err != nil {
			return err
		}
	}
	return nil
}

func (b *tunnelBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	addr, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &sourceEndpoint{dst: addr}, nil
}

func (b *tunnelBind) Close() error {
	if b.conn == nil {
		return nil
	}
	return b.conn.Close()
}

func (b *tunnelBind) SetMark(uint32) error { return nil }

func (b *tunnelBind) BatchSize() int { return 1 }

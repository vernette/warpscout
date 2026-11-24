package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
)

const (
	defaultSocksListen = "127.0.0.1"
	defaultSocksPort   = 1080
	socksTestingNote   = "For testing purposes ONLY."
)

func runSocksCmd(ctx context.Context, opts options) error {
	if opts.endpoint == "" {
		return fmt.Errorf("-endpoint ADDR is required: pass what \"warpscout scan -best\" prints")
	}
	if opts.port < 1 || opts.port > 65535 {
		return fmt.Errorf("-port (%d) must be between 1 and 65535", opts.port)
	}
	endpoint, err := parseEndpointSpec("-endpoint", opts.endpoint)
	if err != nil {
		return err
	}
	// The family is the endpoint's, which is why this command has no -6.
	ap, err := netip.ParseAddrPort(endpoint)
	if err != nil {
		return err
	}
	opts.ipv6 = !ap.Addr().Is4()

	if err := loadScanAccount(opts.accountPath); err != nil {
		return err
	}
	run, _, err := setupScan(opts)
	if err != nil {
		return err
	}
	if run.isMASQUE() && masqueAcct == nil {
		return fmt.Errorf("%s holds no MASQUE device: run \"warpscout register\" again", opts.accountPath)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	timeout := time.Duration(opts.timeoutSec) * time.Second
	if opts.through != "" {
		n, err := dialOuter(ctx, opts, timeout)
		if err != nil {
			return err
		}
		defer n.tunnel.Close()
		outer = n
	}

	tn, err := newTunnel(run)
	if err != nil {
		return err
	}
	defer tn.Close()
	if !tn.handshake(ctx, endpoint, timeout) {
		return fmt.Errorf("%s did not answer over %s - pick one a scan found working", endpoint, run.name)
	}
	// The same reason as the speedtest phase: gvisor allocates per packet, and this
	// tunnel stays under traffic for as long as the process lives.
	defer debug.SetGCPercent(debug.SetGCPercent(speedGCPercent))

	ln, err := net.Listen("tcp", net.JoinHostPort(opts.listen, strconv.Itoa(opts.port)))
	if err != nil {
		return err
	}
	defer ln.Close()

	exit, gotExit := fetchExit(ctx, tn, timeout)
	writeSocksBanner(os.Stderr, newConStyles(consoleRenderer(os.Stderr)), ln.Addr().String(), endpoint, run, exit, gotExit)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go serveSOCKS(ctx, c, tn.stack().tnet)
	}
}

func throughLabel(run protoRun) string {
	if outer == nil {
		return run.name
	}
	return fmt.Sprintf("%s through %s", run.name, outer.label)
}

func fetchExit(ctx context.Context, tn tunnel, timeout time.Duration) (metaResult, bool) {
	body, ok := tn.stack().fetch(ctx, timeout)
	if !ok {
		return metaResult{}, false
	}
	return parseMeta(body), true
}

func socksExitLine(t metaResult) string {
	node := fmt.Sprintf("%s (%s node)", exitColoLocation(t), exitColo(t))
	if t.loc == t.coloISO {
		return node
	}
	return exitRegion(t) + " via " + node
}

// The warning comes before the address on purpose: the address is what gets
// copied, so the reader has to pass the caveat to reach it.
func writeSocksBanner(w io.Writer, st conStyles, proxy, endpoint string, run protoRun, exit metaResult, gotExit bool) {
	fmt.Fprintln(w, st.warn.Bold(true).Render(socksTestingNote))
	fmt.Fprintln(w, "One tunnel, no reconnect, no failover. For everyday use take a \"scan -conf\" config into a real client.")
	fmt.Fprintln(w)

	rows := [][2]string{
		{"SOCKS5", st.accent.Render("socks5h://" + proxy)},
		{"Endpoint", st.title.Render(endpoint)},
		{"Tunnel", throughLabel(run)},
	}
	if gotExit {
		rows = append(rows, [2]string{"Exit", st.ok.Render(socksExitLine(exit))})
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = st.dim.Render(fmt.Sprintf("%-9s", r[0])) + r[1]
	}
	fmt.Fprintln(w, st.box.Render(strings.Join(lines, "\n")))
	fmt.Fprintln(w)

	if gotExit {
		fmt.Fprintf(w, "Exit is what %s reports. Confirm it elsewhere:\n", st.accent.Render(metaHost))
		fmt.Fprintf(w, "  %s\n\n", st.accent.Render(fmt.Sprintf("curl -x socks5h://%s https://ifconfig.co/json", proxy)))
	}
	fmt.Fprintf(w, "Point clients at %s, not %s - the name has to be resolved in the tunnel.\n",
		st.ok.Render("socks5h://"), st.fail.Render("socks5://"))
	fmt.Fprintln(w, "\n"+st.dim.Render("Ctrl+C to stop the proxy"))
}

const (
	socksVersion  = 5
	socksNoAuth   = 0
	socksConnect  = 1
	socksUDPAssoc = 3
	socksOK       = 0
	socksRefused  = 5
	socksBadCmd   = 7
	socksIPv4     = 1
	socksDomain   = 3
	socksIPv6     = 4
	socksPortSize = 2
	// Not -timeout: that one bounds a handshake on a direct path, while this covers
	// the in-tunnel DNS lookup plus a TCP connect, both of which are slow through a
	// nested tunnel.
	socksDialTimeout = 10 * time.Second
	socksUDPBuffer   = 65535
	socksUDPIdle     = 60 * time.Second
)

func serveSOCKS(ctx context.Context, c net.Conn, tnet *netstack.Net) {
	defer c.Close()
	cmd, target, err := socksHandshake(c)
	if err != nil {
		return
	}
	if cmd == socksUDPAssoc {
		serveUDPAssoc(ctx, c, tnet)
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, socksDialTimeout)
	defer cancel()
	target, err = resolveInTunnel(dialCtx, tnet, target)
	if err != nil {
		socksReply(c, socksRefused, netip.AddrPort{})
		return
	}
	remote, err := tnet.DialContext(dialCtx, "tcp", target)
	if err != nil {
		socksReply(c, socksRefused, netip.AddrPort{})
		return
	}
	defer remote.Close()
	if err := socksReply(c, socksOK, netip.AddrPort{}); err != nil {
		return
	}
	go io.Copy(remote, c)
	io.Copy(c, remote)
}

func serveUDPAssoc(ctx context.Context, c net.Conn, tnet *netstack.Net) {
	local, err := netip.ParseAddrPort(c.LocalAddr().String())
	if err != nil {
		socksReply(c, socksRefused, netip.AddrPort{})
		return
	}
	pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local.Addr(), 0)))
	if err != nil {
		socksReply(c, socksRefused, netip.AddrPort{})
		return
	}
	defer pc.Close()

	bound, err := netip.ParseAddrPort(pc.LocalAddr().String())
	if err != nil {
		socksReply(c, socksRefused, netip.AddrPort{})
		return
	}
	if err := socksReply(c, socksOK, bound); err != nil {
		return
	}

	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	go func() {
		io.Copy(io.Discard, c)
		pc.Close()
	}()
	relayUDP(ctx, pc, tnet)
}

func relayUDP(ctx context.Context, pc *net.UDPConn, tnet *netstack.Net) {
	var mu sync.Mutex
	conns := map[string]net.Conn{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, rc := range conns {
			rc.Close()
		}
	}()

	buf := make([]byte, socksUDPBuffer)
	for {
		n, client, err := pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		hdr, dst, payload, err := parseUDPRequest(buf[:n])
		if err != nil {
			continue
		}
		mu.Lock()
		rc, ok := conns[string(hdr)]
		if !ok {
			rc, err = dialInTunnelUDP(ctx, tnet, dst)
			if err == nil {
				conns[string(hdr)] = rc
				go func(hdr []byte, rc net.Conn, client netip.AddrPort) {
					relayUDPReplies(pc, rc, hdr, client)
					mu.Lock()
					delete(conns, string(hdr))
					mu.Unlock()
					rc.Close()
				}(append([]byte(nil), hdr...), rc, client)
			}
		}
		mu.Unlock()
		if err != nil {
			continue
		}
		rc.Write(payload)
	}
}

func relayUDPReplies(pc *net.UDPConn, rc net.Conn, hdr []byte, client netip.AddrPort) {
	buf := make([]byte, len(hdr)+socksUDPBuffer)
	copy(buf, hdr)
	for {
		if err := rc.SetReadDeadline(time.Now().Add(socksUDPIdle)); err != nil {
			return
		}
		n, err := rc.Read(buf[len(hdr):])
		if err != nil {
			return
		}
		if _, err := pc.WriteToUDPAddrPort(buf[:len(hdr)+n], client); err != nil {
			return
		}
	}
}

func dialInTunnelUDP(ctx context.Context, tnet *netstack.Net, dst string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, socksDialTimeout)
	defer cancel()
	dst, err := resolveInTunnel(dialCtx, tnet, dst)
	if err != nil {
		return nil, err
	}
	ap, err := netip.ParseAddrPort(dst)
	if err != nil {
		return nil, err
	}
	return tnet.DialUDPAddrPort(netip.AddrPort{}, ap)
}

func parseUDPRequest(b []byte) (hdr []byte, dst string, payload []byte, err error) {
	if len(b) < 4 {
		return nil, "", nil, fmt.Errorf("socks udp datagram of %d bytes", len(b))
	}
	if b[2] != 0 {
		return nil, "", nil, fmt.Errorf("socks udp fragment %d", b[2])
	}
	r := bytes.NewReader(b[4:])
	host, err := socksAddr(r, b[3])
	if err != nil {
		return nil, "", nil, err
	}
	port := make([]byte, socksPortSize)
	if _, err := io.ReadFull(r, port); err != nil {
		return nil, "", nil, err
	}
	rest := len(b) - r.Len()
	return b[:rest], net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), b[rest:], nil
}

// Resolved inside the tunnel, never on the host: a host lookup would leak the
// query onto the very network this command exists to bypass, and it answers with
// addresses picked for the host's own region. The v4 answer is taken for the same
// reason the meta fetch takes it - and, nested, because netstack tries the
// addresses in order with a share of the deadline each, so an IPv6 answer that
// does not pass eats the budget the working v4 one needed.
func resolveInTunnel(ctx context.Context, tnet *netstack.Net, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return target, nil
	}
	ips, err := tnet.LookupContextHost(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no address for %s", host)
	}
	return net.JoinHostPort(preferV4(ips), port), nil
}

func socksHandshake(rw io.ReadWriter) (byte, string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(rw, head); err != nil {
		return 0, "", err
	}
	if head[0] != socksVersion {
		return 0, "", fmt.Errorf("socks version %d", head[0])
	}
	if _, err := io.ReadFull(rw, make([]byte, head[1])); err != nil {
		return 0, "", err
	}
	if _, err := rw.Write([]byte{socksVersion, socksNoAuth}); err != nil {
		return 0, "", err
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(rw, req); err != nil {
		return 0, "", err
	}
	if req[0] != socksVersion {
		return 0, "", fmt.Errorf("socks version %d", req[0])
	}
	if req[1] != socksConnect && req[1] != socksUDPAssoc {
		socksReply(rw, socksBadCmd, netip.AddrPort{})
		return 0, "", fmt.Errorf("socks command %d is neither CONNECT nor UDP ASSOCIATE", req[1])
	}

	host, err := socksAddr(rw, req[3])
	if err != nil {
		return 0, "", err
	}
	port := make([]byte, socksPortSize)
	if _, err := io.ReadFull(rw, port); err != nil {
		return 0, "", err
	}
	return req[1], net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}

func socksAddr(r io.Reader, atyp byte) (string, error) {
	n := 0
	switch atyp {
	case socksIPv4:
		n = 4
	case socksIPv6:
		n = 16
	case socksDomain:
		size := make([]byte, 1)
		if _, err := io.ReadFull(r, size); err != nil {
			return "", err
		}
		n = int(size[0])
	default:
		return "", fmt.Errorf("socks address type %d", atyp)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	if atyp == socksDomain {
		return string(buf), nil
	}
	addr, ok := netip.AddrFromSlice(buf)
	if !ok {
		return "", fmt.Errorf("socks address of %d bytes", n)
	}
	return addr.String(), nil
}

func socksReply(w io.Writer, rep byte, bound netip.AddrPort) error {
	if !bound.IsValid() {
		_, err := w.Write([]byte{socksVersion, rep, 0, socksIPv4, 0, 0, 0, 0, 0, 0})
		return err
	}
	addr := bound.Addr().Unmap()
	atyp := byte(socksIPv4)
	if !addr.Is4() {
		atyp = socksIPv6
	}
	msg := append([]byte{socksVersion, rep, 0, atyp}, addr.AsSlice()...)
	msg = binary.BigEndian.AppendUint16(msg, bound.Port())
	_, err := w.Write(msg)
	return err
}

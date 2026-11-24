package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

type ipStack struct {
	tnet   *netstack.Net
	client *http.Client
}

func newIPStack(local []netip.Addr, mtu int) (tun.Device, *ipStack, error) {
	tunDev, tnet, err := netstack.CreateNetTUN(local, []netip.Addr{netip.MustParseAddr(tunnelDNS)}, mtu)
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return tnet.DialContext(ctx, "tcp", metaDialAddr())
		},
	}
	return tunDev, &ipStack{tnet: tnet, client: &http.Client{Transport: transport}}, nil
}

type tunnel interface {
	handshake(ctx context.Context, endpoint string, timeout time.Duration) bool
	ports() []int
	attempts() int
	stack() *ipStack
	Close()
}

func newTunnel(run protoRun) (tunnel, error) {
	if run.isMASQUE() {
		return newMasqueTunnel(run.isH2())
	}
	return newWGTunnel(run.isAWG())
}

type wgTunnel struct {
	*ipStack
	dev *device.Device
}

func newWGTunnel(awg bool) (*wgTunnel, error) {
	base, err := baseUAPI(awg)
	if err != nil {
		return nil, err
	}

	mtu := tunnelMTU
	if outer != nil {
		mtu = tunnelMTU - nestedOverhead
	}
	tunDev, stack, err := newIPStack([]netip.Addr{
		netip.MustParseAddr(warpAddress),
		netip.MustParseAddr(warpAddressV6),
	}, mtu)
	if err != nil {
		return nil, err
	}
	// Not conn.NewDefaultBind(): its BatchSize is 128 on Linux, so wireguard-go
	// holds a batch of MaxMessageSize buffers per receive routine per tunnel -
	// measured as 96% of the live heap, and the whole reason the memory limit
	// costs CPU. A scan pushes a trickle of packets, so the batch buys nothing.
	bind := conn.Bind(newDeviceBind(scanInterface))
	if outer != nil {
		bind = newTunnelBind(outer)
	}
	dev := device.NewDevice(tunDev, bind, device.NewLogger(device.LogLevelSilent, ""))

	if err := dev.IpcSet(base); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &wgTunnel{ipStack: stack, dev: dev}, nil
}

func (t *wgTunnel) Close()          { t.dev.Close() }
func (t *wgTunnel) ports() []int    { return warpPorts }
func (t *wgTunnel) attempts() int   { return 1 }
func (t *wgTunnel) stack() *ipStack { return t.ipStack }

func (t *wgTunnel) handshake(ctx context.Context, endpoint string, timeout time.Duration) bool {
	peer, err := peerUAPI(endpoint)
	if err != nil {
		return false
	}
	if err := t.dev.IpcSet(peer); err != nil {
		return false
	}
	return waitHandshake(ctx, t.dev, timeout)
}

type probeTarget struct {
	ip   netip.Addr
	port int // 0 means "try the tunnel's whole port list"
}

func probeTargets(run protoRun, perPort bool, ips []netip.Addr, ports []int) []probeTarget {
	if !run.isMASQUE() && !perPort {
		targets := make([]probeTarget, len(ips))
		for i, ip := range ips {
			targets[i] = probeTarget{ip: ip}
		}
		return targets
	}
	targets := make([]probeTarget, 0, len(ips)*len(ports))
	for _, ip := range ips {
		for _, port := range ports {
			targets = append(targets, probeTarget{ip: ip, port: port})
		}
	}
	return targets
}

func tunnelProbe(ctx context.Context, t tunnel, target probeTarget, timeout time.Duration, pings int, wantMeta bool) (tr metaResult, endpoint string, ok bool, rtt time.Duration, loss float32, torn bool) {
	ports := t.ports()
	if target.port != 0 {
		ports = []int{target.port}
	}
	for _, port := range ports {
		endpoint = net.JoinHostPort(target.ip.String(), strconv.Itoa(port))
		for attempt := 0; attempt < max(t.attempts(), 1); attempt++ {
			if ctx.Err() != nil {
				return metaResult{}, endpoint, false, 0, 0, false
			}
			if tr, rtt, loss, torn, ok = probeEndpoint(ctx, t, endpoint, timeout, pings, wantMeta); ok {
				return tr, endpoint, true, rtt, loss, torn
			}
		}
	}
	return metaResult{}, endpoint, false, 0, 0, false
}

func tunnelConnect(ctx context.Context, t tunnel, ip netip.Addr, timeout time.Duration) bool {
	for _, port := range t.ports() {
		if t.handshake(ctx, net.JoinHostPort(ip.String(), strconv.Itoa(port)), timeout) {
			return true
		}
	}
	return false
}

func probeEndpoint(ctx context.Context, t tunnel, endpoint string, timeout time.Duration, pings int, wantMeta bool) (tr metaResult, rtt time.Duration, loss float32, torn, ok bool) {
	if !t.handshake(ctx, endpoint, timeout) {
		return metaResult{}, 0, 0, false, false
	}
	st := t.stack()

	// find-junk only asks whether the peer comes up, and the durability ping
	// already proves the tunnel passes data - the meta fetch adds nothing.
	if !wantMeta {
		if pings <= 0 {
			return metaResult{}, 0, 0, false, true
		}
		rtt, loss, torn = st.durability(pings, timeout)
		return metaResult{}, rtt, loss, torn, true
	}

	body, ok := st.fetch(ctx, timeout)
	if !ok {
		return metaResult{}, 0, 0, false, false
	}
	if pings <= 0 {
		return parseMeta(body), 0, 0, false, true
	}
	rtt, loss, torn = st.durability(pings, timeout)
	return parseMeta(body), rtt, loss, torn, true
}

// A real DPI teardown stays dead across both bursts; transient loss does not,
// so the second burst's numbers are the ones reported when the first was noise.
// Running right after the meta fetch lets the burst read the tunnel's state once a
// real request has already given DPI something to kill.
func (s *ipStack) durability(count int, timeout time.Duration) (time.Duration, float32, bool) {
	rtt, loss, torn := s.tunnelPing(count, timeout)
	if !torn {
		return rtt, loss, false
	}
	rtt2, loss2, torn2 := s.tunnelPing(count, timeout)
	if !torn2 {
		return rtt2, loss2, false
	}
	return rtt, loss, true
}

func (s *ipStack) fetch(ctx context.Context, timeout time.Duration) (string, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !resolveMetaAddr(reqCtx, s.tnet) {
		return "", false
	}
	s.client.Timeout = timeout
	return fetchMeta(reqCtx, s.client, metaURL)
}

func (s *ipStack) speedTest(ctx context.Context) (float64, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, speedTimeout)
	defer cancel()
	if !resolveMetaAddr(reqCtx, s.tnet) {
		return 0, false
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, speedURL, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Referer", metaReferer)

	s.client.Timeout = speedTimeout
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	return sampleThroughput(resp.Body)
}

func sampleThroughput(body io.Reader) (float64, bool) {
	buf := make([]byte, 64<<10)
	var got int64
	var start time.Time
	for {
		n, err := body.Read(buf)
		got += int64(n)
		if start.IsZero() && got >= speedWarmupBytes {
			got, start = 0, time.Now()
		}
		if err != nil {
			break
		}
		if !start.IsZero() && time.Since(start) >= speedSample {
			break
		}
	}
	if start.IsZero() || got == 0 {
		return 0, false
	}
	return mbits(got, time.Since(start)), true
}

func mbits(bytes int64, d time.Duration) float64 {
	if d <= 0 || bytes <= 0 {
		return 0
	}
	return float64(bytes) * 8 / 1e6 / d.Seconds()
}

var metaAddr atomic.Pointer[string]

func metaDialAddr() string {
	if p := metaAddr.Load(); p != nil {
		return *p
	}
	return ""
}

// Resolved through the tunnel, not on the host: a host resolver can answer with
// an address that only routes outside the tunnel. The first worker to get
// through fills it in for the rest of the run. Deliberately not under a lock:
// on a network where the lookup fails, workers holding one would time out one
// after another instead of all at once.
func resolveMetaAddr(ctx context.Context, tnet *netstack.Net) bool {
	if metaAddr.Load() != nil {
		return true
	}
	ips, err := tnet.LookupContextHost(ctx, metaHost)
	if err != nil || len(ips) == 0 {
		return false
	}
	addr := net.JoinHostPort(preferV4(ips), "443")
	metaAddr.CompareAndSwap(nil, &addr)
	return true
}

// The tunnel carries both families, but the meta fetch stays on v4
func preferV4(ips []string) string {
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			return ip
		}
	}
	return ips[0]
}

// The netstack resolver is a separate knob from the ping destination: -ping-target
// moves what TUN PING measures to, not where the tunnel resolves names.
const tunnelDNS = "1.1.1.1"

// pingTarget is set from -ping-target (flags.go) before any tunnel runs and may be
// a hostname, which pingDst resolves inside the tunnel for the reason
// resolveMetaAddr resolves metaHost: a host resolver can answer with an address
// that only routes outside the tunnel.
var (
	pingTarget = tunnelDNS
	pingAddr   atomic.Pointer[netip.Addr]
)

func pingDst(tnet *netstack.Net, timeout time.Duration) (netip.Addr, bool) {
	if a := pingAddr.Load(); a != nil {
		return *a, true
	}
	if a, err := netip.ParseAddr(pingTarget); err == nil {
		pingAddr.CompareAndSwap(nil, &a)
		return a, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := tnet.LookupContextHost(ctx, pingTarget)
	if err != nil || len(ips) == 0 {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(preferV4(ips))
	if err != nil {
		return netip.Addr{}, false
	}
	pingAddr.CompareAndSwap(nil, &a)
	return a, true
}

// The name plus what it resolved to: only the address explains the number next to
// it, and an unresolved name explains a whole column of 100% loss.
func pingTargetLabel() string {
	if a := pingAddr.Load(); a != nil && a.String() != pingTarget {
		return fmt.Sprintf("%s (%s)", pingTarget, a)
	}
	if _, err := netip.ParseAddr(pingTarget); err != nil {
		return pingTarget + " (unresolved)"
	}
	return pingTarget
}

const (
	pingInterval    = 200 * time.Millisecond
	durabilityPings = 10
	// torn down = the tunnel passes data and is then cut mid-stream, never recovering,
	// i.e. a trailing run of dropped pings. Sporadic single drops are loss, not a teardown, so we
	// key off a run of consecutive tail failures rather than a loss percentage.
	tornTailFails = 3
	// A burst of tornTailFails echoes technically fits a tail run, but with no
	// margin at all for a single stray drop:
	// measured 13 of 70 dead wg peers reported as working at 3 echoes, none at 10.
	minDurabilityPings = 5
)

// Echoes are spread over time to give DPI traffic to react to, but replies are
// matched by sequence in a shared window: a reply merely delayed by a contended
// netstack (many userspace tunnels at once) still counts instead of scoring as
// a loss.
func (s *ipStack) tunnelPing(count int, timeout time.Duration) (time.Duration, float32, bool) {
	dst, ok := pingDst(s.tnet, timeout)
	if !ok {
		return 0, 1, false
	}
	pc, err := s.tnet.DialPingAddr(netip.Addr{}, dst)
	if err != nil {
		return 0, 1, false
	}
	defer pc.Close()

	sent := make([]time.Time, count)
	got := make([]bool, count)
	buf := make([]byte, 1500)
	n := 0
	var total time.Duration

	drain := func(deadline time.Time) {
		for n < count {
			pc.SetReadDeadline(deadline)
			m, err := pc.Read(buf)
			if err != nil {
				return
			}
			seq, ok := parseEchoSeq(buf[:m])
			if !ok || seq < 0 || seq >= count || got[seq] || sent[seq].IsZero() {
				continue
			}
			got[seq] = true
			total += time.Since(sent[seq])
			n++
		}
	}

	for seq := 0; seq < count; seq++ {
		if s.sendEcho(pc, seq) {
			sent[seq] = time.Now()
		}
		drain(time.Now().Add(pingInterval))
	}
	drain(time.Now().Add(timeout)) // final window for stragglers before scoring loss

	return pingSummary(total, n), lossFraction(n, count), teardown(got)
}

func (s *ipStack) sendEcho(pc *netstack.PingConn, seq int) bool {
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: 0xbeef, Seq: seq, Data: []byte("warpscout")},
	}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return false
	}
	_, err = pc.Write(wire)
	return err == nil
}

func parseEchoSeq(b []byte) (int, bool) {
	m, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), b)
	if err != nil {
		return 0, false
	}
	echo, ok := m.Body.(*icmp.Echo)
	if !ok || m.Type != ipv4.ICMPTypeEchoReply {
		return 0, false
	}
	return echo.Seq, true
}

func pingSummary(total time.Duration, got int) time.Duration {
	if got == 0 {
		return 0
	}
	return total / time.Duration(got)
}

func lossFraction(got, count int) float32 {
	if count == 0 {
		return 0
	}
	return float32(count-got) / float32(count)
}

// A trailing run of at least tornTailFails unanswered echoes means the tunnel
// stopped passing traffic and did not come back.
func teardown(results []bool) bool {
	if len(results) < tornTailFails {
		return false
	}
	for _, ok := range results[len(results)-tornTailFails:] {
		if ok {
			return false
		}
	}
	return true
}

// A live endpoint answers in one RTT, so a flat 50ms poll spent most of its wait
// on an already-finished handshake. The poll starts short and backs off: an
// IpcGet is 2.9us (measured), so the extra early polls cost nothing, while a
// dead endpoint still ends up polling rarely until -timeout.
const (
	handshakePollStart = 2 * time.Millisecond
	handshakePollMax   = 50 * time.Millisecond
)

func waitHandshake(ctx context.Context, dev *device.Device, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	wait := handshakePollStart
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		conf, err := dev.IpcGet()
		if err == nil && handshakeDone(conf) {
			return true
		}
		time.Sleep(wait)
		if wait < handshakePollMax {
			wait *= 2
		}
	}
	return false
}

const handshakeKey = "last_handshake_time_sec="

func handshakeDone(conf string) bool {
	i := strings.Index(conf, handshakeKey)
	if i < 0 {
		return false
	}
	v := i + len(handshakeKey)
	return v < len(conf) && conf[v] != '0'
}
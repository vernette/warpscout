package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/crypto/curve25519"
)

func TestWriteConsolePalette(t *testing.T) {
	ph := phaseResult{
		run: protoRun{kindWG, "wg"},
		results: []endpointResult{{
			ip:       netip.MustParseAddr("8.47.69.86"),
			exit:     metaResult{loc: "RU", colo: "DME"},
			endpoint: "8.47.69.86:2408",
			ok:       true,
			durable:  true,
		}},
	}

	var plain bytes.Buffer
	rPlain := lipgloss.NewRenderer(&plain)
	rPlain.SetColorProfile(termenv.Ascii)
	writeConsole(&plain, ph, rPlain, false)
	if strings.Contains(plain.String(), "\033") {
		t.Error("plain (non-TTY) console output must not contain ANSI escapes")
	}
	if !strings.Contains(plain.String(), "8.47.69.86:2408") {
		t.Error("console output missing the working endpoint")
	}

	var colored bytes.Buffer
	rColor := lipgloss.NewRenderer(&colored)
	rColor.SetColorProfile(termenv.TrueColor)
	writeConsole(&colored, ph, rColor, false)
	if !strings.Contains(colored.String(), "\033") {
		t.Error("colored console output should contain ANSI escapes")
	}
}

func TestWriteFullReport(t *testing.T) {
	mk := func(ip, colo, city string, ms int) endpointResult {
		return endpointResult{
			ip:       netip.MustParseAddr(ip),
			endpoint: ip + ":2408",
			exit:     metaResult{loc: "RU", colo: colo, coloCity: city, coloISO: "RU"},
			epPing:   time.Duration(ms) * time.Millisecond,
			ok:       true,
			durable:  true,
		}
	}
	// Two addresses of the same /24 landing on different nodes - the case the
	// old per-subnet summary collapsed to a single row.
	results := []endpointResult{
		mk("188.114.96.28", "ARN", "Stockholm", 63),
		mk("188.114.96.99", "DME", "Moscow", 41),
		mk("162.159.195.192", "KJA", "Krasnoyarsk", 22),
	}

	var buf bytes.Buffer
	writeFullReport(&buf, results, false)
	out := buf.String()

	if strings.Contains(out, "═") || strings.Contains(out, "──") {
		t.Error("report must not contain box drawing characters")
	}
	order := []string{"162.159.195.192:2408", "188.114.96.99:2408", "188.114.96.28:2408"}
	for i := 1; i < len(order); i++ {
		if strings.Index(out, order[i-1]) > strings.Index(out, order[i]) {
			t.Errorf("endpoints not sorted by ping: %s came after %s", order[i-1], order[i])
		}
	}

	_, picks, ok := strings.Cut(out, "# Best endpoint per node")
	if !ok {
		t.Fatal("report missing the per-node summary")
	}
	for _, node := range []string{"ARN", "DME", "KJA"} {
		if !strings.Contains(picks, node) {
			t.Errorf("per-node summary missing node %s", node)
		}
	}
}

func TestLessByLossRTT(t *testing.T) {
	mk := func(ep string, ms int, loss float32, measured bool) endpointResult {
		return endpointResult{endpoint: ep, tunPing: time.Duration(ms) * time.Millisecond, loss: loss, measured: measured}
	}
	cases := []struct {
		name string
		a, b endpointResult
		want bool // a ranks before b
	}{
		{"lower loss beats lower ping", mk("a", 200, 0, true), mk("b", 20, 0.2, true), true},
		{"equal loss falls back to ping", mk("a", 20, 0.1, true), mk("b", 90, 0.1, true), true},
		{"unmeasured ranks by host ping", endpointResult{endpoint: "a", epPing: 20 * time.Millisecond}, endpointResult{endpoint: "b", epPing: 90 * time.Millisecond}, true},
		{"tie broken by endpoint", mk("a", 20, 0, true), mk("b", 20, 0, true), true},
	}
	for _, c := range cases {
		if got := lessByLossRTT(c.a, c.b); got != c.want {
			t.Errorf("%s: lessByLossRTT = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLessByLossRTTSpeed(t *testing.T) {
	bestBy = bestKeySpeed
	defer func() { bestBy = bestKeyPing }()

	slowPing := endpointResult{endpoint: "a", epPing: 200 * time.Millisecond, speed: 90}
	fastPing := endpointResult{endpoint: "b", epPing: 20 * time.Millisecond, speed: 9}
	if !lessByLossRTT(slowPing, fastPing) {
		t.Error("higher speed did not outrank lower ping")
	}
	unmeasured := endpointResult{endpoint: "c", epPing: 10 * time.Millisecond}
	if !lessByLossRTT(fastPing, unmeasured) {
		t.Error("an unmeasured endpoint outranked a measured one")
	}
	if !lessByLossRTT(unmeasured, endpointResult{endpoint: "d", epPing: 90 * time.Millisecond}) {
		t.Error("with no speed measured the order stopped following ping")
	}
}

func TestPingDiagnostics(t *testing.T) {
	cases := []struct {
		name     string
		total    time.Duration
		got      int
		count    int
		wantRTT  time.Duration
		wantLoss float32
	}{
		{"no loss", 400 * time.Millisecond, 4, 4, 100 * time.Millisecond, 0},
		{"half loss", 100 * time.Millisecond, 2, 4, 50 * time.Millisecond, 0.5},
		{"total loss", 0, 0, 4, 0, 1},
	}
	for _, c := range cases {
		if rtt := pingSummary(c.total, c.got); rtt != c.wantRTT {
			t.Errorf("%s: pingSummary = %v, want %v", c.name, rtt, c.wantRTT)
		}
		if loss := lossFraction(c.got, c.count); loss != c.wantLoss {
			t.Errorf("%s: lossFraction = %v, want %v", c.name, loss, c.wantLoss)
		}
	}
}

func TestSameHost(t *testing.T) {
	udp := func(s string) net.Addr { return &net.UDPAddr{IP: net.ParseIP(s)} }
	raw := func(s string) net.Addr { return &net.IPAddr{IP: net.ParseIP(s)} }
	cases := []struct {
		name string
		a, b net.Addr
		want bool
	}{
		{"same ip across addr types", udp("1.2.3.4"), raw("1.2.3.4"), true},
		{"different ip", raw("1.2.3.4"), raw("1.2.3.5"), false},
		{"v4 and v4-mapped v6", raw("1.2.3.4"), raw("::ffff:1.2.3.4"), true},
		{"same v6", udp("2606:4700::1"), raw("2606:4700::1"), true},
		{"unknown addr type", &net.TCPAddr{IP: net.ParseIP("1.2.3.4")}, raw("1.2.3.4"), false},
	}
	for _, c := range cases {
		if got := sameHost(c.a, c.b); got != c.want {
			t.Errorf("%s: sameHost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTeardown(t *testing.T) {
	cases := []struct {
		name    string
		results []bool
		want    bool
	}{
		{"all ok", []bool{true, true, true, true, true}, false},
		{"sporadic single drop is loss, not torn", []bool{true, false, true, true, true}, false},
		{"two mid drops recover", []bool{true, false, false, true, true}, false},
		{"tail teardown (DPI)", []bool{true, true, true, false, false, false}, true},
		{"trips early, never recovers", []bool{true, false, false, false, false}, true},
		{"short trailing run below threshold", []bool{true, true, true, true, false, false}, false},
	}
	for _, c := range cases {
		if got := teardown(c.results); got != c.want {
			t.Errorf("%s: teardown(%v) = %v, want %v", c.name, c.results, got, c.want)
		}
	}
}

func TestBestByPing(t *testing.T) {
	mk := func(ep string, ms int) endpointResult {
		return endpointResult{endpoint: ep, epPing: time.Duration(ms) * time.Millisecond}
	}

	picks := []endpointResult{mk("a", 0), mk("b", 90), mk("c", 40)}
	if got := bestByPing(picks); got.endpoint != "c" {
		t.Errorf("bestByPing = %q, want c (40ms)", got.endpoint)
	}

	allUnknown := []endpointResult{mk("x", 0), mk("y", 0)}
	if got := bestByPing(allUnknown); got.endpoint != "x" {
		t.Errorf("bestByPing(all unknown) = %q, want x", got.endpoint)
	}
}

func TestBaseUAPI(t *testing.T) {
	wg, err := baseUAPI(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wg, "private_key=") {
		t.Error("base config missing private_key")
	}
	if strings.Contains(wg, "jc=") || strings.Contains(wg, "i1=") {
		t.Error("plain WireGuard config must not contain junk params")
	}

	awg, err := baseUAPI(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jc=6", "jmin=10", "jmax=50", "i1=" + i1Default} {
		if !strings.Contains(awg, want) {
			t.Errorf("AmneziaWG config missing %q", want)
		}
	}
}

func TestRenderConf(t *testing.T) {
	awg := renderConf(options{ipv6: true}, "188.114.98.5:2408", protoRun{kindAWG, protoAWG})
	for _, want := range []string{
		"[Interface]",
		"Address = " + warpAddressV6 + "/128",
		"AllowedIPs = ::/0",
		"PrivateKey = " + warpPrivateKey,
		"Jc = 6", "Jmin = 10", "Jmax = 50",
		"I1 = " + i1Default,
		"DNS = " + warpDNSv6,
		"[Peer]",
		"PublicKey = " + warpPublicKey,
		"Endpoint = 188.114.98.5:2408",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(awg, want) {
			t.Errorf("AmneziaWG config missing %q:\n%s", want, awg)
		}
	}
	if strings.Contains(awg, "Table") {
		t.Errorf("Table must be absent without -table-off:\n%s", awg)
	}
	if strings.Contains(awg, "MTU") {
		t.Errorf("MTU must be absent without -mtu:\n%s", awg)
	}
	if strings.Contains(awg, "0.0.0.0") || strings.Contains(awg, warpAddress) {
		t.Errorf("IPv6 config must not carry IPv4:\n%s", awg)
	}

	wg := renderConf(options{tableOff: true, mtu: 1420}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	for _, unwanted := range []string{"Jc = ", "Jmin = ", "Jmax = ", "I1 = ", "::"} {
		if strings.Contains(wg, unwanted) {
			t.Errorf("plain WireGuard config must not contain %q:\n%s", unwanted, wg)
		}
	}
	if !strings.Contains(wg, "Table = off") {
		t.Errorf("-table-off not reflected:\n%s", wg)
	}
	if !strings.Contains(wg, "MTU = 1420") {
		t.Errorf("-mtu not reflected:\n%s", wg)
	}
}

func TestRenderConfChained(t *testing.T) {
	outerAcct = &account{PrivateKey: "outerPriv=", PeerPublicKey: "outerPub=", IPv4: "172.16.0.9"}
	outer = &nest{run: protoRun{kindAWG, protoAWG}, endpoint: "188.114.97.177:2408", label: "188.114.97.177:2408 (awg)"}
	defer func() { outer, outerAcct = nil, nil }()

	got := renderConf(options{}, "8.47.69.130:2408", protoRun{kindWG, protoWG})
	for _, want := range []string{
		"Address = 172.16.0.9/32", "PrivateKey = outerPriv=", "Endpoint = 188.114.97.177:2408", "Jc = 6",
		"Address = " + warpAddress + "/32", "PrivateKey = " + warpPrivateKey, "Endpoint = 8.47.69.130:2408",
		"MTU = 1220",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("chained config missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "[Interface]"); n != 2 {
		t.Errorf("chained config has %d interfaces, want 2:\n%s", n, got)
	}
	// The resolvers belong to the inner tunnel, the far end of the chain.
	if n := strings.Count(got, "DNS = "); n != 1 {
		t.Errorf("chained config carries %d DNS lines, want 1:\n%s", n, got)
	}
	if warpPrivateKey == "outerPriv=" {
		t.Error("outer keys leaked into the account globals")
	}

	// An account file predating stored addresses falls back to the same constant
	// for both devices, and two interfaces on one Address pass no data.
	outerAcct.IPv4 = ""
	got = renderConf(options{}, "8.47.69.130:2408", protoRun{kindWG, protoWG})
	if !strings.Contains(got, "Address = "+chainInnerAddress+"/32") {
		t.Errorf("colliding addresses not separated:\n%s", got)
	}
	if warpAddress == chainInnerAddress {
		t.Error("the inner address leaked into the account globals")
	}
}

func TestRenderMihomoConf(t *testing.T) {
	conf, err := renderMihomoConf(options{confType: confTypeMihomo}, "188.114.98.5:2408", protoRun{kindAWG, protoAWG})
	if err != nil {
		t.Fatal(err)
	}
	awg := string(conf)
	for _, want := range []string{
		"proxies:",
		"- name: \"AWG WARP\"",
		"peers:",
		"      - server: 188.114.98.5",
		"        port: 2408",
		"        persistent-keepalive: 25",
		"type: wireguard",
		"private-key: " + warpPrivateKey,
		"public-key: " + warpPublicKey,
		"ip: " + warpAddress,
		"allowed-ips: ['0.0.0.0/0']",
		"amnezia-wg-option:",
		"jc: 6", "jmin: 10", "jmax: 50",
		"h4: 4",
		"i1: " + i1Default,
		"udp: true",
	} {
		if !strings.Contains(awg, want) {
			t.Errorf("mihomo config missing %q:\n%s", want, awg)
		}
	}
	if strings.Contains(awg, "mtu:") {
		t.Errorf("mtu must be absent without -mtu:\n%s", awg)
	}
	if strings.Contains(awg, "ipv6:") || strings.Contains(awg, "::") {
		t.Errorf("IPv4 config must not carry IPv6:\n%s", awg)
	}

	conf, err = renderMihomoConf(options{confType: confTypeMihomo, ipv6: true}, "[2606:4700:d0::1]:2408", protoRun{kindAWG, protoAWG})
	if err != nil {
		t.Fatal(err)
	}
	v6 := string(conf)
	for _, want := range []string{"server: 2606:4700:d0::1", "ipv6: " + warpAddressV6, "allowed-ips: ['::/0']", "dns: ['2606:4700:4700::1111', '2606:4700:4700::1001']"} {
		if !strings.Contains(v6, want) {
			t.Errorf("IPv6 mihomo config missing %q:\n%s", want, v6)
		}
	}
	if strings.Contains(v6, "  ip: ") || strings.Contains(v6, "1.1.1.1") {
		t.Errorf("IPv6 config must not carry IPv4:\n%s", v6)
	}

	conf, err = renderMihomoConf(options{confType: confTypeMihomo}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	if err != nil {
		t.Fatal(err)
	}
	if wg := string(conf); strings.Contains(wg, "amnezia-wg-option") {
		t.Errorf("plain WireGuard config must not carry junk params:\n%s", wg)
	}
	if strings.Contains(awg, "dialer-proxy") {
		t.Errorf("a plain run must render one proxy, not a chain:\n%s", awg)
	}
}

func TestRenderMihomoConfChained(t *testing.T) {
	outerAcct = &account{
		PrivateKey:    "outerPriv=",
		PeerPublicKey: "outerPub=",
		IPv4:          "172.16.0.9",
		IPv6:          "2606:4700:110:1::9",
	}
	outer = &nest{run: protoRun{kindAWG, protoAWG}, endpoint: "188.114.97.177:2408", label: "188.114.97.177:2408 (awg)"}
	defer func() { outer, outerAcct = nil, nil }()

	conf, err := renderMihomoConf(options{confType: confTypeMihomo}, "8.47.69.130:2408", protoRun{kindWG, protoWG})
	if err != nil {
		t.Fatal(err)
	}
	got := string(conf)
	for _, want := range []string{
		"- name: \"AWG WARP OUTER\"",
		"server: 188.114.97.177",
		"private-key: outerPriv=",
		"public-key: outerPub=",
		"ip: 172.16.0.9",
		"- name: \"WG WARP-in-WARP\"",
		"server: 8.47.69.130",
		"private-key: " + warpPrivateKey,
		"mtu: 1220",
		"dialer-proxy: \"AWG WARP OUTER\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("chained config missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "- name:"); n != 2 {
		t.Errorf("chained config has %d proxies, want 2:\n%s", n, got)
	}
	// The chain resolves at its far end, so the carrier gets no resolvers.
	if n := strings.Count(got, "dns: ["); n != 1 {
		t.Errorf("chained config carries %d dns lines, want 1:\n%s", n, got)
	}
	// The dialer-proxy must sit on the inner proxy, which is the second block.
	if strings.Index(got, "dialer-proxy") < strings.Index(got, "- name: \"WG WARP-in-WARP\"") {
		t.Errorf("dialer-proxy belongs to the inner proxy:\n%s", got)
	}
	// Rendering must not leave the outer device's keys in the globals.
	if warpPrivateKey == "outerPriv=" {
		t.Error("outer keys leaked into the account globals")
	}
	if strings.Contains(got, "mtu: 1280") {
		t.Errorf("only the inner proxy takes an MTU without -mtu:\n%s", got)
	}
}

func TestRenderMihomoJSON(t *testing.T) {
	conf, err := renderMihomoConf(options{confType: confTypeMihomoJSON}, "188.114.98.5:2408", protoRun{kindAWG, protoAWG})
	if err != nil {
		t.Fatal(err)
	}
	var proxies []map[string]any
	if err := json.Unmarshal(conf, &proxies); err != nil {
		t.Fatalf("%v:\n%s", err, conf)
	}
	if len(proxies) != 1 {
		t.Fatalf("got %d proxies, want 1:\n%s", len(proxies), conf)
	}
	p := proxies[0]
	if p["name"] != "AWG WARP" || p["type"] != "wireguard" || p["udp"] != true {
		t.Errorf("proxy fields wrong:\n%s", conf)
	}
	peers, ok := p["peers"].([]any)
	if !ok || len(peers) != 1 {
		t.Fatalf("peers missing:\n%s", conf)
	}
	peer := peers[0].(map[string]any)
	if peer["server"] != "188.114.98.5" {
		t.Errorf("peer server wrong:\n%s", conf)
	}
	if peer["port"] != float64(2408) {
		t.Errorf("port %#v is not a JSON number:\n%s", peer["port"], conf)
	}
	if dns, _ := p["dns"].([]any); len(dns) != 2 {
		t.Errorf("dns must be a list of two resolvers:\n%s", conf)
	}
	if !strings.Contains(string(conf), "<r 2>") {
		t.Errorf("i1 must not be HTML-escaped:\n%s", conf)
	}
	var last int
	for _, key := range []string{"\"name\"", "\"type\"", "\"private-key\"", "\"ip\"", "\"peers\"", "\"amnezia-wg-option\"", "\"udp\"", "\"dns\""} {
		i := strings.Index(string(conf), key)
		if i < last {
			t.Fatalf("%s is out of order:\n%s", key, conf)
		}
		last = i
	}
}

func TestRenderMihomoJSONChained(t *testing.T) {
	outerAcct = &account{PrivateKey: "outerPriv=", PeerPublicKey: "outerPub=", IPv4: "172.16.0.9"}
	outer = &nest{run: protoRun{kindAWG, protoAWG}, endpoint: "188.114.97.177:2408", label: "188.114.97.177:2408 (awg)"}
	defer func() { outer, outerAcct = nil, nil }()

	conf, err := renderMihomoConf(options{confType: confTypeMihomoJSON}, "8.47.69.130:2408", protoRun{kindWG, protoWG})
	if err != nil {
		t.Fatal(err)
	}
	var proxies []map[string]any
	if err := json.Unmarshal(conf, &proxies); err != nil {
		t.Fatalf("%v:\n%s", err, conf)
	}
	if len(proxies) != 2 {
		t.Fatalf("got %d proxies, want 2:\n%s", len(proxies), conf)
	}
	if proxies[0]["name"] != "AWG WARP OUTER" || proxies[1]["dialer-proxy"] != "AWG WARP OUTER" {
		t.Errorf("the inner proxy must dial through the outer one:\n%s", conf)
	}
	if _, ok := proxies[0]["dns"]; ok {
		t.Errorf("the carrier takes no resolvers:\n%s", conf)
	}
}

// The proxy name has to name the protocol and nothing else: several configs land
// in one mihomo file, and an address in the name goes stale on the next scan.
func TestMihomoName(t *testing.T) {
	for _, tc := range []struct {
		run  protoRun
		want string
	}{
		{protoRun{kindWG, protoWG}, "WG WARP"},
		{protoRun{kindAWG, protoAWG}, "AWG WARP"},
		{protoRun{kindMASQUE, protoMASQUE}, "MASQUE H3 WARP"},
		{protoRun{kindMASQUEH2, protoMASQUEH2}, "MASQUE H2 WARP"},
	} {
		if got := mihomoName(tc.run); got != tc.want {
			t.Errorf("mihomoName(%s) = %q, want %q", tc.run.name, got, tc.want)
		}
	}
}

func TestConfDNS(t *testing.T) {
	wg := renderConf(options{}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	if !strings.Contains(wg, "DNS = "+warpDNSv4) {
		t.Errorf("default DNS missing:\n%s", wg)
	}

	custom := renderConf(options{dns: "9.9.9.9"}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	if !strings.Contains(custom, "DNS = 9.9.9.9") || strings.Contains(custom, warpDNSv4) {
		t.Errorf("-dns not reflected:\n%s", custom)
	}

	off := renderConf(options{noDNS: true}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	if strings.Contains(off, "DNS = ") {
		t.Errorf("-no-dns must drop the DNS line:\n%s", off)
	}

	conf, err := renderMihomoConf(options{confType: confTypeMihomo, noDNS: true}, "188.114.98.5:2408", protoRun{kindWG, protoWG})
	if err != nil {
		t.Fatal(err)
	}
	mihomo := string(conf)
	if strings.Contains(mihomo, "dns:") || strings.Contains(mihomo, "remote-dns-resolve") {
		t.Errorf("-no-dns must drop both DNS lines in mihomo:\n%s", mihomo)
	}
}

func TestRenderConfNoInitPacket(t *testing.T) {
	orig := awgI1
	defer func() { awgI1 = orig }()

	awgI1 = ""
	if conf := renderConf(options{}, "188.114.98.5:2408", protoRun{kindAWG, protoAWG}); strings.Contains(conf, "I1 = ") {
		t.Errorf("init packet must be omitted when empty:\n%s", conf)
	}
}

func TestBaseUAPINoInitPacket(t *testing.T) {
	orig := awgI1
	defer func() { awgI1 = orig }()

	awgI1 = ""
	awg, err := baseUAPI(true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(awg, "i1=") {
		t.Errorf("init packet must be omitted when empty: %q", awg)
	}
}

func TestBaseUAPIOverridesJunkParams(t *testing.T) {
	orig := awgJc
	defer func() { awgJc = orig }()

	awgJc = 99
	awg, err := baseUAPI(true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(awg, "jc=99") {
		t.Errorf("override not reflected in UAPI: %q", awg)
	}
}

func TestGenJunkParams(t *testing.T) {
	jc, jmin, jmax := awgJc, awgJmin, awgJmax
	defer func() { awgJc, awgJmin, awgJmax = jc, jmin, jmax }()

	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		genJunkParams()
		if awgJc < junkCountLimitMin || awgJc > junkCountLimitMax {
			t.Fatalf("jc out of range: %d", awgJc)
		}
		if awgJmin > awgJmax {
			t.Fatalf("jmin %d > jmax %d", awgJmin, awgJmax)
		}
		if awgJmax > tunnelMTU {
			t.Fatalf("jmax exceeds MTU: %d", awgJmax)
		}
		seen[awgJc] = true
	}
	if len(seen) < 2 {
		t.Errorf("jc never varied over 200 runs: %v", seen)
	}
}

func TestJunkCommand(t *testing.T) {
	c := junkCandidate{jc: 5, jmin: 22, jmax: 80, i1: i1Default}
	if got, want := junkCommand(c), "-proto awg -jc 5 -jmin 22 -jmax 80"; !strings.HasSuffix(got, want) {
		t.Errorf("junkCommand() = %q, want suffix %q", got, want)
	}

	c.i1 = ""
	if got := junkCommand(c); !strings.HasSuffix(got, "-i1 none") {
		t.Errorf("junkCommand() = %q, want -i1 none", got)
	}

	c.i1, c.i1Label = "<r 4>", "quic(www.apple.com)"
	if got := junkCommand(c); !strings.HasSuffix(got, `-i1 "<r 4>"`) {
		t.Errorf("junkCommand() = %q, want the generated I1", got)
	}
}

func TestParseProto(t *testing.T) {
	for _, p := range []string{protoWG, protoAWG, protoMASQUE, protoMASQUEH2} {
		run, err := parseProto(p)
		masque := p == protoMASQUE || p == protoMASQUEH2
		if err != nil || run.name != p || run.isAWG() != (p == protoAWG) || run.isMASQUE() != masque || run.isH2() != (p == protoMASQUEH2) {
			t.Errorf("parseProto(%q) = %+v, %v", p, run, err)
		}
	}
	if _, err := parseProto("both"); err == nil {
		t.Error("parseProto(\"both\") accepted, want an error")
	}
}

func TestPoolsFor(t *testing.T) {
	masque := protoRun{kindMASQUE, protoMASQUE}
	if got := poolsFor(masque, false); len(got) != len(masquePoolsV4) || got[0] != masquePoolsV4[0] {
		t.Errorf("poolsFor(masque, v4) = %v, want the MASQUE pools", got)
	}
	if got := poolsFor(masque, true); len(got) != len(masquePoolsV6) {
		t.Errorf("poolsFor(masque, v6) = %v, want the MASQUE v6 pools", got)
	}
	if got := poolsFor(protoRun{kindAWG, protoAWG}, false); len(got) != len(poolsV4) {
		t.Errorf("poolsFor(awg, v4) = %d prefixes, want the WireGuard pools", len(got))
	}

	h2 := protoRun{kindMASQUEH2, protoMASQUEH2}
	if got := poolsFor(h2, false); len(got) != len(masqueH2PoolsV4) || got[0] != masqueH2PoolsV4[0] {
		t.Errorf("poolsFor(masque-h2, v4) = %v, want the HTTP/2 pools", got)
	}
	if got := poolsFor(h2, true); len(got) != len(masqueH2PoolsV6) {
		t.Errorf("poolsFor(masque-h2, v6) = %v, want the HTTP/2 v6 pools", got)
	}
}

// -conf under masque-h2 must fill usque's HTTP/2 field pair, not the QUIC one.
func TestRenderMasqueConfH2(t *testing.T) {
	saved := masqueAcct
	defer func() { masqueAcct = saved }()
	masqueAcct = &masqueAccount{PrivateKey: "cHJpdg==", ID: "dev-1"}

	out, err := renderMasqueConf("162.159.199.7:443", true)
	if err != nil {
		t.Fatalf("renderMasqueConf: %v", err)
	}
	var c usqueConf
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.EndpointH2V4 != "162.159.199.7" {
		t.Errorf("EndpointH2V4 = %q, want the scanned address", c.EndpointH2V4)
	}
	if c.EndpointV4 == "162.159.199.7" {
		t.Error("EndpointV4 carries the HTTP/2 address, want the QUIC default")
	}
}

// The MASQUE prefixes are single addresses, so expansion must hand back exactly
// those and never sample them away.
func TestExpandMasquePools(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pool  []netip.Prefix
		count int
	}{{"v4", masquePoolsV4, len(masquePoolsV4)}, {"v6", masquePoolsV6, len(masquePoolsV6)}} {
		pools = tc.pool
		for _, sample := range []int{0, 1, 5} {
			if got := expandPools(sample); len(got) != tc.count {
				t.Errorf("expandPools(%d) over %s = %v, want %d addresses", sample, tc.name, got, tc.count)
			}
		}
	}
	pools = poolsV4
}

func TestProbeTargets(t *testing.T) {
	ips := []netip.Addr{netip.MustParseAddr("162.159.198.1"), netip.MustParseAddr("162.159.198.2")}
	ports := []int{443, 8443}

	wg := probeTargets(protoRun{kindAWG, protoAWG}, false, ips, ports)
	if len(wg) != len(ips) {
		t.Fatalf("wg targets = %d, want one per address", len(wg))
	}
	for _, tg := range wg {
		if tg.port != 0 {
			t.Errorf("wg target %v pins a port, want the tunnel's own list", tg)
		}
	}

	swept := probeTargets(protoRun{kindAWG, protoAWG}, true, ips, ports)
	if len(swept) != len(ips)*len(ports) {
		t.Fatalf("swept targets = %d, want %d", len(swept), len(ips)*len(ports))
	}
	for _, tg := range swept {
		if tg.port == 0 {
			t.Errorf("swept target %v pins no port", tg)
		}
	}

	masque := probeTargets(protoRun{kindMASQUE, protoMASQUE}, false, ips, ports)
	if len(masque) != len(ips)*len(ports) {
		t.Fatalf("masque targets = %d, want %d", len(masque), len(ips)*len(ports))
	}
	seen := map[string]bool{}
	for _, tg := range masque {
		seen[tg.ip.String()+":"+strconv.Itoa(tg.port)] = true
	}
	for _, want := range []string{"162.159.198.1:443", "162.159.198.1:8443", "162.159.198.2:443", "162.159.198.2:8443"} {
		if !seen[want] {
			t.Errorf("masque targets missing %s", want)
		}
	}
}

// A dead WireGuard peer is dead; a MASQUE endpoint has to fail every attempt
// before it counts, because the same one answers on a later try.
func TestTunnelAttempts(t *testing.T) {
	if got := (&wgTunnel{}).attempts(); got != 1 {
		t.Errorf("wgTunnel.attempts() = %d, want 1", got)
	}
	if masqueDefaultAttempts < 2 {
		t.Errorf("masqueDefaultAttempts = %d, want more than one try", masqueDefaultAttempts)
	}
	saved := masqueAttempts
	defer func() { masqueAttempts = saved }()
	masqueAttempts = 7
	if got := (&masqueTunnel{}).attempts(); got != 7 {
		t.Errorf("masqueTunnel.attempts() = %d, want the -masque-attempts value", got)
	}
}

func TestRenderMasqueConf(t *testing.T) {
	saved := masqueAcct
	defer func() { masqueAcct = saved }()
	masqueAcct = &masqueAccount{
		PrivateKey: "cHJpdg==", PeerPublicKey: "-----BEGIN PUBLIC KEY-----\nx\n-----END PUBLIC KEY-----\n",
		ID: "dev-1", Token: "tok", IPv4: "172.16.0.2", IPv6: "2606:4700:110::1",
	}

	out, err := renderMasqueConf("162.159.198.1:8443", false)
	if err != nil {
		t.Fatalf("renderMasqueConf: %v", err)
	}
	var c usqueConf
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.EndpointV4 != "162.159.198.1" {
		t.Errorf("EndpointV4 = %q, want the scanned address without its port", c.EndpointV4)
	}
	// The other family keeps a usable default instead of being blanked out.
	if c.EndpointV6 == "" {
		t.Error("EndpointV6 is empty, want the default MASQUE v6 endpoint")
	}
	if c.ID != "dev-1" || c.AccessToken != "tok" || c.IPv4 != "172.16.0.2" {
		t.Errorf("conf lost account fields: %+v", c)
	}

	if _, err := renderMasqueConf("162.159.198.1", false); err == nil {
		t.Error("renderMasqueConf accepted an endpoint without a port")
	}
}

func TestRenderMasqueConfV6Endpoint(t *testing.T) {
	saved := masqueAcct
	defer func() { masqueAcct = saved }()
	masqueAcct = &masqueAccount{PrivateKey: "cHJpdg==", ID: "dev-1"}

	out, err := renderMasqueConf("[2606:4700:103::1]:443", false)
	if err != nil {
		t.Fatalf("renderMasqueConf: %v", err)
	}
	var c usqueConf
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.EndpointV6 != "2606:4700:103::1" {
		t.Errorf("EndpointV6 = %q, want the scanned v6 address", c.EndpointV6)
	}
	if c.EndpointV4 == "" {
		t.Error("EndpointV4 is empty, want the default MASQUE v4 endpoint")
	}
}

func TestScoreJunk(t *testing.T) {
	ph := phaseResult{results: []endpointResult{
		{ok: true, durable: true},
		{ok: true, durable: false},
		{},
	}}
	if c := scoreJunk(ph); c.working != 1 || c.total != 3 {
		t.Errorf("scoreJunk() = %d/%d, want 1/3", c.working, c.total)
	}
	if c := scoreJunk(phaseResult{}); c.total != 0 {
		t.Errorf("scoreJunk(empty) total = %d, want 0", c.total)
	}
}

func TestScoreSNI(t *testing.T) {
	ph := phaseResult{results: []endpointResult{
		{ok: true, durable: true},
		{ok: true, durable: true},
		{ok: true, durable: false},
		{},
	}}
	c := scoreSNI("www.apple.com", ph)
	if c.working != 2 || c.total != 4 {
		t.Errorf("scoreSNI() = %d/%d, want 2/4", c.working, c.total)
	}
	if !c.meets(50) || c.meets(51) {
		t.Errorf("meets() at 2/4 = (%v, %v), want (true, false)", c.meets(50), c.meets(51))
	}
	if (sniCandidate{}).meets(1) {
		t.Error("meets() on an empty candidate = true, want false")
	}
}

func TestSNICommand(t *testing.T) {
	masque := protoRun{kindMASQUE, protoMASQUE}
	got := sniCommand(masque, sniCandidate{sni: "www.apple.com"})
	if want := "scan -proto masque -masque-sni www.apple.com"; !strings.HasSuffix(got, want) {
		t.Errorf("sniCommand() = %q, want suffix %q", got, want)
	}
	got = sniCommand(masque, sniCandidate{sni: masqueDefaultSNI})
	if want := "scan -proto masque"; !strings.HasSuffix(got, want) {
		t.Errorf("sniCommand(default) = %q, want suffix %q", got, want)
	}
	// The command has to name the transport the round actually searched: an SNI
	// that works over QUIC is not one that works over TCP.
	got = sniCommand(protoRun{kindMASQUEH2, protoMASQUEH2}, sniCandidate{sni: "www.apple.com"})
	if want := "scan -proto masque-h2 -masque-sni www.apple.com"; !strings.HasSuffix(got, want) {
		t.Errorf("sniCommand(h2) = %q, want suffix %q", got, want)
	}
}

func TestPeerUAPI(t *testing.T) {
	peer, err := peerUAPI("1.2.3.4:2408")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"replace_peers=true", "public_key=", "endpoint=1.2.3.4:2408", "allowed_ip=0.0.0.0/0"} {
		if !strings.Contains(peer, want) {
			t.Errorf("missing %q in peer config", want)
		}
	}
}

func TestGenerateKeypair(t *testing.T) {
	privB64, pubB64, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil || len(priv) != 32 {
		t.Fatalf("bad private key: %v (len %d)", err, len(priv))
	}
	// Clamp bits must be set (RFC 7748 / wg key format).
	if priv[0]&7 != 0 || priv[31]&128 != 0 || priv[31]&64 == 0 {
		t.Error("private key not clamped")
	}
	want, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(want) != pubB64 {
		t.Error("public key does not match private key")
	}
}

func TestParseRegResp(t *testing.T) {
	body := []byte(`{"id":"dev123","token":"tok456","config":{` +
		`"interface":{"addresses":{"v4":"172.16.0.5/32"}},` +
		`"peers":[{"public_key":"PEERPUBKEY"}]}}`)
	a, err := parseRegResp(body, "MYPRIVKEY")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "dev123" || a.Token != "tok456" {
		t.Errorf("id/token = %q/%q", a.ID, a.Token)
	}
	if a.PrivateKey != "MYPRIVKEY" || a.PeerPublicKey != "PEERPUBKEY" {
		t.Errorf("account = %+v", a)
	}
}

func TestRotatedAccount(t *testing.T) {
	body := []byte(`{"id":"dev123","config":{"peers":[{"public_key":"NEWPEER"}]}}`)
	existing := account{PrivateKey: "OLDPRIV", PeerPublicKey: "OLDPEER", ID: "dev123", Token: "tok456"}
	a, err := rotatedAccount(body, "NEWPRIV", existing)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "dev123" || a.Token != "tok456" {
		t.Errorf("id/token = %q/%q, want dev123/tok456", a.ID, a.Token)
	}
	if a.PrivateKey != "NEWPRIV" || a.PeerPublicKey != "NEWPEER" {
		t.Errorf("account = %+v", a)
	}
}

func TestExpandPools(t *testing.T) {
	if n := len(expandPools(0)); n != len(pools)*256 {
		t.Errorf("full scan = %d IPs, want %d", n, len(pools)*256)
	}
	if n := len(expandPools(8)); n != len(pools)*8 {
		t.Errorf("partial scan = %d IPs, want %d", n, len(pools)*8)
	}
	seen := map[netip.Addr]bool{}
	for _, ip := range expandPools(8) {
		if seen[ip] {
			t.Errorf("duplicate host %v in a subnet", ip)
		}
		seen[ip] = true
	}
}

func TestParseTargets(t *testing.T) {
	ok := map[string][]string{
		"188.114.98.5":                    {"188.114.98.5/32"},
		" 188.114.98.0/28 , 8.6.112.0/24": {"188.114.98.0/28", "8.6.112.0/24"},
		"2606:4700:d0::1":                 {"2606:4700:d0::1/128"},
	}
	for spec, want := range ok {
		got, err := parseTargets(spec)
		if err != nil {
			t.Errorf("parseTargets(%q) failed: %v", spec, err)
			continue
		}
		var have []string
		for _, p := range got {
			have = append(have, p.String())
		}
		if strings.Join(have, ",") != strings.Join(want, ",") {
			t.Errorf("parseTargets(%q) = %v, want %v", spec, have, want)
		}
	}

	for _, spec := range []string{"", "nonsense", "8.6.112.0/8", "188.114.98.5,2606:4700:d0::1"} {
		if _, err := parseTargets(spec); err == nil {
			t.Errorf("parseTargets(%q) should have failed", spec)
		}
	}
}

func TestParseEndpointSpec(t *testing.T) {
	ok := map[string]string{
		"188.114.98.5":          "188.114.98.5:2408",
		"188.114.98.5:1701":     "188.114.98.5:1701",
		"2606:4700:d0::1":       "[2606:4700:d0::1]:2408",
		"[2606:4700:d0::1]:500": "[2606:4700:d0::1]:500",
	}
	for spec, want := range ok {
		got, err := parseEndpointSpec("-through", spec)
		if err != nil {
			t.Errorf("parseEndpointSpec(%q) failed: %v", spec, err)
			continue
		}
		if got != want {
			t.Errorf("parseEndpointSpec(%q) = %q, want %q", spec, got, want)
		}
	}

	for _, spec := range []string{"", "nonsense", "188.114.98.0/24", "188.114.98.5:99999"} {
		if _, err := parseEndpointSpec("-through", spec); err == nil {
			t.Errorf("parseEndpointSpec(%q) should have failed", spec)
		}
	}
}

func TestExpandTargets(t *testing.T) {
	defer func(saved []netip.Prefix) { pools = saved }(pools)

	pools, _ = parseTargets("188.114.98.5,162.159.192.0/28")
	ips := expandPools(10)
	const want = 1 + 10 // the /28 is sampled down to -n
	if len(ips) != want {
		t.Fatalf("expandPools = %d IPs, want %d", len(ips), want)
	}
	if ips[0].String() != "188.114.98.5" {
		t.Errorf("single-address target expanded to %v", ips[0])
	}
	for _, ip := range ips[1:] {
		if !pools[1].Contains(ip) {
			t.Errorf("%v not in %s", ip, pools[1])
		}
	}

	pools, _ = parseTargets("2606:4700:d0::1")
	if ips := expandPools(10); len(ips) != 1 || ips[0].String() != "2606:4700:d0::1" {
		t.Errorf("single-address IPv6 target expanded to %v", ips)
	}
}

func TestFilterByColo(t *testing.T) {
	ph := phaseResult{run: protoRun{kindAWG, "awg"}, results: []endpointResult{
		{endpoint: "1.1.1.1:2408", exit: metaResult{colo: "HEL"}},
		{endpoint: "2.2.2.2:2408", exit: metaResult{colo: "FRA"}},
		{endpoint: "3.3.3.3:2408"},
	}}

	got := filterByColo(ph, []string{"HEL"}, true)
	if len(got.results) != 1 {
		t.Fatalf("filterByColo kept %v results, want 1", got.results)
	}
	if ep := got.results[0].endpoint; ep != "1.1.1.1:2408" {
		t.Errorf("filterByColo kept %s, want the HEL endpoint", ep)
	}
	if got.run.name != "awg" {
		t.Error("filterByColo dropped the proto run")
	}

	// The endpoint with no colo at all is dropped by the positive filter and
	// kept by the negative one.
	var dropped []string
	for _, r := range filterByColo(ph, []string{"HEL"}, false).results {
		dropped = append(dropped, r.endpoint)
	}
	if want := []string{"2.2.2.2:2408", "3.3.3.3:2408"}; strings.Join(dropped, ",") != strings.Join(want, ",") {
		t.Errorf("filterByColo(HEL, exclude) = %v, want %v", dropped, want)
	}
}

func TestFilterByCountry(t *testing.T) {
	ph := phaseResult{run: protoRun{kindAWG, "awg"}, results: []endpointResult{
		{endpoint: "1.1.1.1:2408", exit: metaResult{colo: "HEL", coloISO: "FI"}},
		{endpoint: "2.2.2.2:2408", exit: metaResult{colo: "FRA", coloISO: "DE"}},
		{endpoint: "3.3.3.3:2408"},
	}}

	got := filterByCountry(ph, []string{"DE"}, true)
	if len(got.results) != 1 {
		t.Fatalf("filterByCountry kept %v results, want 1", got.results)
	}
	if ep := got.results[0].endpoint; ep != "2.2.2.2:2408" {
		t.Errorf("filterByCountry kept %s, want the FRA endpoint", ep)
	}
	if got := filterByCountry(ph, []string{"US"}, true); len(got.results) != 0 {
		t.Errorf("filterByCountry(US) kept %v, want nothing", got.results)
	}

	var dropped []string
	for _, r := range filterByCountry(ph, []string{"DE"}, false).results {
		dropped = append(dropped, r.endpoint)
	}
	if want := []string{"1.1.1.1:2408", "3.3.3.3:2408"}; strings.Join(dropped, ",") != strings.Join(want, ",") {
		t.Errorf("filterByCountry(DE, exclude) = %v, want %v", dropped, want)
	}
}

func TestApplyFilters(t *testing.T) {
	ph := phaseResult{results: []endpointResult{
		{endpoint: "1.1.1.1:2408", exit: metaResult{colo: "HEL", coloISO: "FI"}},
		{endpoint: "2.2.2.2:2408", exit: metaResult{colo: "ARN", coloISO: "SE"}},
		{endpoint: "3.3.3.3:2408", exit: metaResult{colo: "FRA", coloISO: "DE"}},
		{endpoint: "4.4.4.4:2408", exit: metaResult{colo: "DME", coloISO: "RU"}},
	}}

	// Positive and negative filters stack: countries first, then the node drop.
	opts := options{countries: []string{"FI", "SE", "DE"}, dropColos: []string{"ARN"}}
	var got []string
	for _, r := range applyFilters(ph, opts).results {
		got = append(got, r.endpoint)
	}
	if want := []string{"1.1.1.1:2408", "3.3.3.3:2408"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("applyFilters = %v, want %v", got, want)
	}
	if !filtered(opts) || filtered(options{}) {
		t.Error("filtered() disagrees with the filters it is given")
	}
}

func TestFlagEmoji(t *testing.T) {
	showEmoji = true
	t.Cleanup(func() { showEmoji = false })
	cases := map[string]string{
		"RU":  "\U0001F1F7\U0001F1FA",
		"de":  "\U0001F1E9\U0001F1EA",
		"?":   "",
		"":    "",
		"USA": "",
		"R1":  "",
	}
	for in, want := range cases {
		if got := flagEmoji(in); got != want {
			t.Errorf("flagEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMeta(t *testing.T) {
	body := `{"clientIp":"1.2.3.4","country":"RU","city":"Moscow",
		"colo":{"iata":"ARN","cca2":"SE","city":"Stockholm"}}`
	got := parseMeta(body)
	if got.loc != "RU" || got.colo != "ARN" || got.coloCity != "Stockholm" || got.coloISO != "SE" {
		t.Errorf("parseMeta = %+v", got)
	}
	if got := parseMeta("not json"); got != (metaResult{}) {
		t.Errorf("parseMeta(garbage) = %+v, want zero", got)
	}
}

func TestHandshakeDone(t *testing.T) {
	cases := map[string]bool{
		"last_handshake_time_sec=1700000000\nlast_handshake_time_nsec=0\n": true,
		"last_handshake_time_sec=0\n":                                      false,
		"public_key=abc\n":                                                 false,
		"":                                                                 false,
	}
	for conf, want := range cases {
		if got := handshakeDone(conf); got != want {
			t.Errorf("handshakeDone(%q) = %v, want %v", conf, got, want)
		}
	}
}

func TestExpandV6(t *testing.T) {
	p := netip.MustParsePrefix("2606:4700:d0::/48")
	const want = 10
	ips := expandV6(p, want)
	if len(ips) != want {
		t.Fatalf("expandV6 count = %d, want %d", len(ips), want)
	}
	for _, ip := range ips {
		if !ip.Is6() {
			t.Errorf("%s is not IPv6", ip)
		}
		if !p.Contains(ip) {
			t.Errorf("%s not in %s", ip, p)
		}
	}
}

func TestRegI1Candidates(t *testing.T) {
	const cur = "<b 0xdead>"
	if got := regI1Candidates(protoRun{kindWG, protoWG}, options{}, cur, ""); len(got) != 1 || got[0].chain != cur {
		t.Errorf("wg candidates = %v, want the current I1 only", got)
	}
	if got := regI1Candidates(protoRun{kindAWG, protoAWG}, options{i1Explicit: true}, cur, "x"); len(got) != 1 || got[0].chain != cur {
		t.Errorf("explicit I1 candidates = %v, want the current I1 only", got)
	}

	got := regI1Candidates(protoRun{kindAWG, protoAWG}, options{}, cur, "")
	if want := 1 + len(i1Profiles()); len(got) != want {
		t.Fatalf("awg candidates = %d, want %d", len(got), want)
	}
	if got[0].chain != i1Default || got[0].label != "" {
		t.Errorf("first candidate = %+v, want the default probe", got[0])
	}
	for _, c := range got[1:] {
		if c.chain == "" || c.label == "" {
			t.Errorf("generated candidate %+v is incomplete", c)
		}
	}
}

func TestJunkCandidateMeets(t *testing.T) {
	cases := []struct {
		working, total, pct int
		want                bool
	}{
		{40, 42, defaultJunkThreshold, true},
		{39, 42, defaultJunkThreshold, false},
		{42, 42, 100, true},
		{41, 42, 100, false},
		{0, 0, defaultJunkThreshold, false},
	}
	for _, c := range cases {
		got := junkCandidate{working: c.working, total: c.total}.meets(c.pct)
		if got != c.want {
			t.Errorf("%d/%d at %d%% = %v, want %v", c.working, c.total, c.pct, got, c.want)
		}
	}
}

func TestNoEndpointMsg(t *testing.T) {
	cases := []struct {
		name string
		opts options
		want string
	}{
		{"awg without gen-i1", options{proto: protoAWG}, "no working endpoints found - try -gen-i1 quic"},
		{"wg without gen-i1", options{proto: protoWG}, "no working endpoints found - try -p awg -gen-i1 quic"},
		{"gen-i1 already set", options{proto: protoAWG, genI1: "quic"}, "no working endpoints found"},
		{"masque", options{proto: protoMASQUE}, masqueBlockedMsg},
		{"masque-h2", options{proto: protoMASQUEH2}, masqueBlockedMsg},
		{"filters win over the hint", options{proto: protoAWG, colos: []string{"HEL"}}, "no endpoint landed on node HEL"},
	}
	for _, c := range cases {
		if got := noEndpointMsg(c.opts); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMbits(t *testing.T) {
	cases := []struct {
		bytes int64
		d     time.Duration
		want  float64
	}{
		{1_250_000, time.Second, 10},
		{5 << 20, 4 * time.Second, 10.485760},
		{1000, 0, 0},
		{0, time.Second, 0},
	}
	for _, c := range cases {
		if got := mbits(c.bytes, c.d); math.Abs(got-c.want) > 0.0001 {
			t.Errorf("mbits(%d, %v) = %v, want %v", c.bytes, c.d, got, c.want)
		}
	}
}

func TestReportSpeedColumn(t *testing.T) {
	r := endpointResult{endpoint: "1.2.3.4:2408", speed: 42.5, ok: true, durable: true}
	if !anySpeed([]endpointResult{{}, r}) {
		t.Error("anySpeed missed a measured endpoint")
	}
	if anySpeed([]endpointResult{{}, {endpoint: "5.6.7.8:2408"}}) {
		t.Error("anySpeed reported an unmeasured set as measured")
	}
	if got := speedStr(0); got != "-" {
		t.Errorf("speedStr(0) = %q, want %q", got, "-")
	}

	var withSpeed bytes.Buffer
	writeRows(&withSpeed, []endpointResult{r}, false, true)
	for _, want := range []string{"SPEED", "42.5 Mbps"} {
		if !strings.Contains(withSpeed.String(), want) {
			t.Errorf("-speed report is missing %q:\n%s", want, withSpeed.String())
		}
	}

	var noSpeed bytes.Buffer
	writeRows(&noSpeed, []endpointResult{r}, false, false)
	if strings.Contains(noSpeed.String(), "SPEED") || strings.Contains(noSpeed.String(), "42.5") {
		t.Errorf("report without -speed leaks the column:\n%s", noSpeed.String())
	}
}

func TestPicksTablePerNode(t *testing.T) {
	defer func(saved []netip.Prefix) { pools = saved }(pools)
	pools, _ = parseTargets("8.47.69.0/24")

	ok := func(addr string, ms int, colo string) endpointResult {
		return endpointResult{
			ip:       netip.MustParseAddr(addr),
			endpoint: addr + ":2408",
			epPing:   time.Duration(ms) * time.Millisecond,
			exit:     metaResult{loc: "RU", colo: colo},
			ok:       true,
			durable:  true,
		}
	}
	working := []endpointResult{
		ok("8.47.69.10", 3, "DME"),
		ok("8.47.69.11", 9, "FRA"),
		ok("8.47.69.12", 20, "FRA"), // same node, slower - not shown
	}

	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	r.SetColorProfile(termenv.Ascii)
	writePicksTable(&buf, newConStyles(r), working, nil, false)

	out := buf.String()
	for _, want := range []string{"8.47.69.10:2408", "8.47.69.11:2408"} {
		if !strings.Contains(out, want) {
			t.Errorf("picks table missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "8.47.69.12:2408") {
		t.Errorf("picks table shows a slower endpoint of an already listed node:\n%s", out)
	}
}

func TestAccountToRotate(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "warpscout-account.json")
	switch a, err := accountToRotate(missing); {
	case err != nil:
		t.Errorf("a missing account file failed instead of reading as absent: %v", err)
	case a != (account{}):
		t.Errorf("a missing account file returned %+v", a)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := accountToRotate(corrupt); err == nil {
		t.Error("a corrupt account file was silently treated as absent")
	}

	incomplete := filepath.Join(dir, "incomplete.json")
	if err := os.WriteFile(incomplete, []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := accountToRotate(incomplete); err == nil {
		t.Error("an account file with no keys was silently treated as absent")
	}

	good := filepath.Join(dir, "good.json")
	want := account{ID: "x", PrivateKey: "priv", PeerPublicKey: "pub"}
	if err := saveAccount(good, want); err != nil {
		t.Fatal(err)
	}
	switch a, err := accountToRotate(good); {
	case err != nil:
		t.Errorf("a valid account file failed: %v", err)
	case a.ID != want.ID:
		t.Errorf("loaded account %+v, want id %q", a, want.ID)
	}
}

func TestPicksTableEmptySubnetSortsLast(t *testing.T) {
	defer func(saved []netip.Prefix) { pools = saved }(pools)
	pools, _ = parseTargets("192.0.2.1/32,192.0.2.2/32")

	lossy := endpointResult{
		ip:       netip.MustParseAddr("192.0.2.2"),
		endpoint: "192.0.2.2:2408",
		tunPing:  10 * time.Millisecond,
		loss:     10,
		ok:       true,
		durable:  true,
	}

	var buf bytes.Buffer
	r := lipgloss.NewRenderer(&buf)
	r.SetColorProfile(termenv.Ascii)
	writePicksTable(&buf, newConStyles(r), []endpointResult{lossy}, nil, true)

	out := buf.String()
	if strings.Index(out, "no working endpoints") < strings.Index(out, lossy.endpoint) {
		t.Errorf("an empty subnet sorted above an endpoint with measured loss:\n%s", out)
	}
}

func TestSpeedTargets(t *testing.T) {
	defer func(saved []netip.Prefix) { pools = saved }(pools)
	pools, _ = parseTargets("8.47.69.0/24,188.114.96.0/24")

	ok := func(addr string, ms int, colo string) endpointResult {
		return endpointResult{
			ip:       netip.MustParseAddr(addr),
			endpoint: addr + ":2408",
			epPing:   time.Duration(ms) * time.Millisecond,
			exit:     metaResult{colo: colo},
			ok:       true,
			durable:  true,
		}
	}
	results := []endpointResult{
		ok("8.47.69.10", 3, "DME"),                                           // subnet pick, and the DME node pick - counted once
		ok("8.47.69.11", 9, "FRA"),                                           // not its subnet's pick, but it is the FRA node pick
		ok("188.114.96.5", 40, "FRA"),                                        // subnet pick only
		ok("188.114.96.6", 41, "FRA"),                                        // shown by neither table
		{endpoint: "8.47.69.12:2408", ip: netip.MustParseAddr("8.47.69.12")}, // not working
	}

	var got []string
	for _, r := range speedTargets(results) {
		got = append(got, r.endpoint)
	}
	want := []string{"8.47.69.10:2408", "8.47.69.11:2408", "188.114.96.5:2408"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("speedTargets = %v, want %v", got, want)
	}
}

func TestFastestNote(t *testing.T) {
	got := fastestNote(map[string]float64{"a:2408": 90.4, "b:2408": 310.2, "c:2408": 0})
	if want := "fastest 310.2 Mbps at b:2408"; got != want {
		t.Errorf("fastestNote = %q, want %q", got, want)
	}
	if got := fastestNote(map[string]float64{"a:2408": 0}); got != "nothing measured" {
		t.Errorf("fastestNote with no measurement = %q", got)
	}
}

func TestShowsSpeed(t *testing.T) {
	cases := []struct {
		name string
		opts options
		want bool
	}{
		{"tables", options{}, true},
		{"best alone", options{best: true}, false},
		{"conf file", options{conf: "wg.conf"}, true},
		{"conf stdout", options{conf: confStdout}, false},
		{"best with report file", options{best: true, output: "r.txt"}, true},
		{"best with report file but no-report", options{best: true, output: "r.txt", noReport: true}, false},
	}
	for _, c := range cases {
		if got := showsSpeed(c.opts); got != c.want {
			t.Errorf("%s: showsSpeed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPrintBest(t *testing.T) {
	run := protoRun{kindWG, protoWG}
	ep := endpointResult{endpoint: "1.2.3.4:2408", ok: true, durable: true}

	var buf bytes.Buffer
	if err := printBest(&buf, phaseResult{run, []endpointResult{ep}}); err != nil {
		t.Fatalf("printBest: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != ep.endpoint {
		t.Errorf("-best printed %q, want %q", got, ep.endpoint)
	}

	buf.Reset()
	torn := phaseResult{run, []endpointResult{{endpoint: ep.endpoint, ok: true}}}
	switch err := printBest(&buf, torn); {
	case err == nil:
		t.Error("-best with nothing but torn-down endpoints reported success")
	case err.Error() != noWorkingMsg:
		t.Errorf("torn-down run failed with %q, want %q", err, noWorkingMsg)
	}
	if buf.Len() > 0 {
		t.Errorf("-best printed an endpoint for a torn-down run: %q", buf.String())
	}
}

func TestWriteConfFile(t *testing.T) {
	run := protoRun{kindWG, protoWG}
	ep := endpointResult{endpoint: "1.2.3.4:2408", ok: true, durable: true}
	working := phaseResult{run, []endpointResult{ep}}

	dir := t.TempDir()
	conf := filepath.Join(dir, "wg.conf")
	if err := writeConfFile(options{conf: conf}, working); err != nil {
		t.Fatalf("writeConfFile: %v", err)
	}
	if _, err := os.Stat(conf); err != nil {
		t.Fatalf("-conf reported success without writing the file: %v", err)
	}

	missing := filepath.Join(dir, "no-such-dir", "wg.conf")
	if err := writeConfFile(options{conf: missing}, working); err == nil {
		t.Error("unwritable -conf path reported success")
	}

	torn := phaseResult{run, []endpointResult{{endpoint: ep.endpoint, ok: true}}}
	tornConf := filepath.Join(dir, "torn.conf")
	switch err := writeConfFile(options{conf: tornConf}, torn); {
	case err == nil:
		t.Error("-conf with nothing but torn-down endpoints reported success")
	case err.Error() != noWorkingMsg:
		t.Errorf("torn-down run failed with %q, want %q", err, noWorkingMsg)
	}
	if _, err := os.Stat(tornConf); err == nil {
		t.Error("a config was written for a torn-down endpoint")
	}
}

func TestIndexedConfPath(t *testing.T) {
	cases := []struct {
		base  string
		index int
		want  string
	}{
		{"warp.conf", 1, "warp-1.conf"},
		{"warp.conf", 3, "warp-3.conf"},
		{"config.json", 2, "config-2.json"},
		{"sub/dir/warp.yaml", 1, "sub/dir/warp-1.yaml"},
		{"simple", 1, "simple-1"},
	}
	for _, c := range cases {
		if got := indexedConfPath(c.base, c.index); got != c.want {
			t.Errorf("indexedConfPath(%q, %d) = %q, want %q", c.base, c.index, got, c.want)
		}
	}
}

func TestWriteConfFiles(t *testing.T) {
	run := protoRun{kindWG, protoWG}
	ep1 := endpointResult{endpoint: "1.2.3.4:2408", ok: true, durable: true, epPing: 10 * time.Millisecond}
	ep2 := endpointResult{endpoint: "5.6.7.8:2408", ok: true, durable: true, epPing: 20 * time.Millisecond}
	working := phaseResult{run, []endpointResult{ep1, ep2}}

	dir := t.TempDir()
	confBase := filepath.Join(dir, "myconf.conf")

	if err := writeConfFiles(options{conf: confBase, configs: 2}, working); err != nil {
		t.Fatalf("writeConfFiles: %v", err)
	}
	path1 := filepath.Join(dir, "myconf-1.conf")
	path2 := filepath.Join(dir, "myconf-2.conf")
	data1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("missing %s: %v", path1, err)
	}
	data2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("missing %s: %v", path2, err)
	}
	if !strings.Contains(string(data1), "Endpoint = "+ep1.endpoint) {
		t.Errorf("myconf-1.conf missing %s:\n%s", ep1.endpoint, data1)
	}
	if !strings.Contains(string(data2), "Endpoint = "+ep2.endpoint) {
		t.Errorf("myconf-2.conf missing %s:\n%s", ep2.endpoint, data2)
	}

	dirPartial := t.TempDir()
	confBasePartial := filepath.Join(dirPartial, "partial.conf")
	if err := writeConfFiles(options{conf: confBasePartial, configs: 3}, working); err != nil {
		t.Fatalf("writeConfFiles partial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirPartial, "partial-1.conf")); err != nil {
		t.Errorf("partial-1.conf not written")
	}
	if _, err := os.Stat(filepath.Join(dirPartial, "partial-2.conf")); err != nil {
		t.Errorf("partial-2.conf not written")
	}
	if _, err := os.Stat(filepath.Join(dirPartial, "partial-3.conf")); err == nil {
		t.Errorf("partial-3.conf should not exist")
	}

	dirZero := t.TempDir()
	confBaseZero := filepath.Join(dirZero, "zero.conf")
	torn := phaseResult{run, []endpointResult{{endpoint: ep1.endpoint, ok: true}}}
	if err := writeConfFiles(options{conf: confBaseZero, configs: 2}, torn); err == nil {
		t.Error("expected error when 0 working endpoints, got nil")
	}
	if _, err := os.Stat(filepath.Join(dirZero, "zero-1.conf")); err == nil {
		t.Errorf("zero-1.conf was written when no working endpoints")
	}

	dirDefault := t.TempDir()
	t.Chdir(dirDefault)
	if err := writeConfFiles(options{configs: 2}, working); err != nil {
		t.Fatalf("writeConfFiles default base: %v", err)
	}
	if _, err := os.Stat("warp-1.conf"); err != nil {
		t.Errorf("warp-1.conf not written for default base: %v", err)
	}
	if _, err := os.Stat("warp-2.conf"); err != nil {
		t.Errorf("warp-2.conf not written for default base: %v", err)
	}
}

func TestCheckConfigs(t *testing.T) {
	cases := []struct {
		configs int
		conf    string
		visited bool
		wantErr bool
	}{
		{configs: 0, conf: "", visited: false, wantErr: false},
		{configs: 2, conf: "warp.conf", visited: true, wantErr: false},
		{configs: 2, conf: "", visited: true, wantErr: false},
		{configs: 0, conf: "", visited: true, wantErr: true},
		{configs: -1, conf: "", visited: true, wantErr: true},
		{configs: 2, conf: confStdout, visited: true, wantErr: true},
	}
	for _, c := range cases {
		err := checkConfigs(c.configs, c.conf, c.visited)
		if (err != nil) != c.wantErr {
			t.Errorf("checkConfigs(%d, %q, %v) err = %v, wantErr = %v", c.configs, c.conf, c.visited, err, c.wantErr)
		}
	}
}

func TestReportPingColumns(t *testing.T) {
	r := endpointResult{
		endpoint: "1.2.3.4:2408",
		epPing:   30 * time.Millisecond,
		tunPing:  90 * time.Millisecond,
		loss:     0.1,
		measured: true,
		ok:       true,
		durable:  true,
	}

	var withPing bytes.Buffer
	writeRows(&withPing, []endpointResult{r}, true, false)
	for _, want := range []string{"ENDPOINT PING", "TUN PING", "LOSS", "30ms", "90ms", "10%"} {
		if !strings.Contains(withPing.String(), want) {
			t.Errorf("-tun-ping report is missing %q:\n%s", want, withPing.String())
		}
	}

	var noPing bytes.Buffer
	writeRows(&noPing, []endpointResult{r}, false, false)
	for _, unwanted := range []string{"TUN PING", "LOSS", "90ms", "10%"} {
		if strings.Contains(noPing.String(), unwanted) {
			t.Errorf("report without -tun-ping leaks %q:\n%s", unwanted, noPing.String())
		}
	}
	if !strings.Contains(noPing.String(), "30ms") {
		t.Errorf("report without -tun-ping lost the endpoint ping:\n%s", noPing.String())
	}
}

func TestTornDownReportBlock(t *testing.T) {
	torn := endpointResult{endpoint: "1.2.3.4:2408", epPing: 30 * time.Millisecond, measured: true, ok: true}
	var buf bytes.Buffer
	writeFullReport(&buf, []endpointResult{torn}, true)
	out := buf.String()
	if !strings.Contains(out, "1 torn down") || !strings.Contains(out, "1.2.3.4:2408") {
		t.Errorf("torn-down endpoint is not reported as such:\n%s", out)
	}
	if strings.Contains(out, "# Best endpoint per node") {
		t.Errorf("a torn-down endpoint must not be picked as best:\n%s", out)
	}
}

type socksPipe struct {
	io.Reader
	out bytes.Buffer
}

func (p *socksPipe) Write(b []byte) (int, error) { return p.out.Write(b) }

func socksRequest(atyp byte, addr []byte, port uint16) *socksPipe {
	req := append([]byte{5, 1, 0, 5, 1, 0, atyp}, addr...)
	req = append(req, byte(port>>8), byte(port))
	return &socksPipe{Reader: bytes.NewReader(req)}
}

func TestSocksHandshake(t *testing.T) {
	cases := map[string]*socksPipe{
		"1.2.3.4:80":       socksRequest(socksIPv4, []byte{1, 2, 3, 4}, 80),
		"example.com:443":  socksRequest(socksDomain, append([]byte{11}, "example.com"...), 443),
		"[2606:4700::1]:8": socksRequest(socksIPv6, netip.MustParseAddr("2606:4700::1").AsSlice(), 8),
	}
	for want, pipe := range cases {
		got, err := socksHandshake(pipe)
		if err != nil {
			t.Errorf("socksHandshake(%s) failed: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("socksHandshake = %q, want %q", got, want)
		}
		if reply := pipe.out.Bytes(); len(reply) != 2 || reply[0] != socksVersion || reply[1] != socksNoAuth {
			t.Errorf("method reply for %s = %v, want [5 0]", want, reply)
		}
	}
}

func TestSocksHandshakeRejects(t *testing.T) {
	truncated := &socksPipe{Reader: bytes.NewReader([]byte{5, 1, 0, 5, 1, 0, socksIPv4, 1, 2})}
	if _, err := socksHandshake(truncated); err == nil {
		t.Error("a truncated request must fail")
	}

	// UDP ASSOCIATE, the one command a client may try that this server does not serve.
	udp := &socksPipe{Reader: bytes.NewReader([]byte{5, 1, 0, 5, 3, 0, socksIPv4, 1, 2, 3, 4, 0, 80})}
	if _, err := socksHandshake(udp); err == nil {
		t.Error("a non-CONNECT command must fail")
	}
	reply := udp.out.Bytes()
	if len(reply) != 12 || reply[3] != socksBadCmd {
		t.Errorf("non-CONNECT reply = %v, want a %d at byte 3", reply, socksBadCmd)
	}
}

func TestIsLoaderArg(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "warpscout")
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	if !isLoaderArg(bin, "./warpscout") {
		t.Error("the injected absolute path must be recognized")
	}
	if isLoaderArg("scan", "./warpscout") {
		t.Error("a subcommand must not be taken for the injected path")
	}
	if isLoaderArg(filepath.Join(dir, "other"), "./warpscout") {
		t.Error("a path that is not the binary must not match")
	}
}

func TestRelayTripper(t *testing.T) {
	var gotPath, gotQuery, gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotHost = r.URL.Path, r.URL.RawQuery, r.Host
		// What the relay does: the body is already inflated, the header is not.
		w.Header().Set("Content-Encoding", "gzip")
		fmt.Fprint(w, `{"id":"x"}`)
	}))
	defer srv.Close()

	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/relay"} {
		c, err := relayClient(base)
		if err != nil {
			t.Fatalf("relayClient(%q): %v", base, err)
		}
		resp, err := c.Get(regBaseURL + "/abc?x=1")
		if err != nil {
			t.Fatalf("get through %q: %v", base, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read through %q: %v", base, err)
		}
		if string(body) != `{"id":"x"}` {
			t.Errorf("base %q: body = %q", base, body)
		}
		want := "/v0a4005/reg/abc"
		if strings.HasSuffix(base, "/relay") {
			want = "/relay" + want
		}
		if gotPath != want {
			t.Errorf("base %q: path = %q, want %q", base, gotPath, want)
		}
		if gotQuery != "x=1" {
			t.Errorf("base %q: query = %q, want x=1", base, gotQuery)
		}
		if gotHost != srv.Listener.Addr().String() {
			t.Errorf("base %q: Host = %q, want the relay's own", base, gotHost)
		}
	}
}

func TestApplyRelay(t *testing.T) {
	o := options{relay: "none"}
	if err := applyRelay(&o); err != nil || o.relay != "" {
		t.Errorf(`"none" must disable the relay: %q, %v`, o.relay, err)
	}
	for _, bad := range []string{"", "cf-api.example.com", "socks5://127.0.0.1:1080"} {
		o := options{relay: bad}
		if err := applyRelay(&o); err == nil {
			t.Errorf("-relay %q must be a parse error", bad)
		}
	}
	o = options{relay: defaultRelay}
	if err := applyRelay(&o); err != nil {
		t.Errorf("defaultRelay must parse: %v", err)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.12.0", "0.11.9", true},
		{"0.12.0", "0.12.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.9.0", "0.10.0", false},
		{"1.0.0", "0.99.9", true},
		{"0.12.0", "dev", false},
		{"", "0.12.0", false},
		{"0.12", "0.11.0", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestReadVersionCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), updateCacheName)
	writeVersionCache(path, "0.12.0")
	if v, ok := readVersionCache(path, time.Hour); !ok || v != "0.12.0" {
		t.Errorf("fresh cache = %q, %v; want 0.12.0, true", v, ok)
	}
	if _, ok := readVersionCache(path, -time.Second); ok {
		t.Error("a stale cache must miss")
	}
	if _, ok := readVersionCache(filepath.Join(t.TempDir(), "absent"), time.Hour); ok {
		t.Error("a missing cache must miss")
	}
}

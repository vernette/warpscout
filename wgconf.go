package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

func renderConf(o options, endpoint string, run protoRun) string {
	if outer == nil {
		return renderIface(o, endpoint, run)
	}
	return renderChainConf(o, endpoint, run)
}

// A .conf cannot express a chain - the client builds it - so a nested run writes
// both interfaces into the one file and leaves the wiring to the reader.
func renderChainConf(o options, endpoint string, run protoRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# WARP-in-WARP chain: two interfaces in one file, split it in two before\n")
	fmt.Fprintf(&b, "# using them. The inner tunnel only reaches the region it was scanned in\n")
	fmt.Fprintf(&b, "# while its packets travel inside the outer one - routing that is the\n")
	fmt.Fprintf(&b, "# client's job, not the config's.\n")
	if o.ipv6 && warpAddressV6 == outerAddress(true) {
		fmt.Fprintf(&b, "#\n# Both interfaces carry the same Address, which handshakes and then passes\n")
		fmt.Fprintf(&b, "# nothing: this account file predates stored IPv6 addresses. Re-register.\n")
	}
	fmt.Fprintf(&b, "\n")

	// The resolvers belong at the far end of the chain, and the outer tunnel's
	// own routes are the client's business either way.
	outerOpts := o
	outerOpts.noDNS = true
	fmt.Fprintf(&b, "# --- outer: %s ---\n", outer.label)
	_ = withOuterKeys(func() error {
		b.WriteString(renderIface(outerOpts, outer.endpoint, outer.run))
		return nil
	})

	innerOpts := o
	innerOpts.mtu = nestedMTU(o.mtu)
	fmt.Fprintf(&b, "\n# --- inner: %s (%s), inside the tunnel above ---\n", endpoint, run.name)
	b.WriteString(withChainAddr(o.ipv6, func() string { return renderIface(innerOpts, endpoint, run) }))
	return b.String()
}

// Two interfaces holding the same Address handshake and then pass nothing at
// all - measured on the hand-built chain. Both halves only collide on an account
// file written before the addresses were stored, where each falls back to the
// same constant; Cloudflare ignores the tunnel-local v4 address, so moving the
// inner one off it is free. There is no such freedom on v6 - it is a real routed
// per-device address - so that stays as it is and the file says so.
const chainInnerAddress = "172.16.0.3"

func withChainAddr(ipv6 bool, fn func() string) string {
	if ipv6 || warpAddress != outerAddress(false) {
		return fn()
	}
	prev := warpAddress
	warpAddress = chainInnerAddress
	defer func() { warpAddress = prev }()
	return fn()
}

func outerAddress(ipv6 bool) string {
	if ipv6 {
		if outerAcct.IPv6 != "" {
			return outerAcct.IPv6
		}
		return warpAddressV6
	}
	if outerAcct.IPv4 != "" {
		return outerAcct.IPv4
	}
	return warpAddress
}

func renderIface(o options, endpoint string, run protoRun) string {
	var b strings.Builder

	fmt.Fprintf(&b, "[Interface]\n")
	if o.ipv6 {
		fmt.Fprintf(&b, "Address = %s/128\n", warpAddressV6)
	} else {
		fmt.Fprintf(&b, "Address = %s/32\n", warpAddress)
	}
	fmt.Fprintf(&b, "PrivateKey = %s\n", warpPrivateKey)
	if dns := confDNS(o); dns != "" {
		fmt.Fprintf(&b, "DNS = %s\n", dns)
	}
	if o.mtu > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", o.mtu)
	}
	if o.tableOff {
		fmt.Fprintf(&b, "Table = off\n")
	}
	if run.isAWG() {
		fmt.Fprintf(&b, "Jc = %d\n", awgJc)
		fmt.Fprintf(&b, "Jmin = %d\n", awgJmin)
		fmt.Fprintf(&b, "Jmax = %d\n", awgJmax)
		if awgI1 != "" {
			fmt.Fprintf(&b, "I1 = %s\n", awgI1)
		}
	}
	if run.isAWG31() {
		for _, p := range awg31Ranges {
			if p.value != "" {
				fmt.Fprintf(&b, "%s = %s\n", p.conf, p.value)
			}
		}
		if awgDisableCookies {
			fmt.Fprintf(&b, "DisableCookies = on\n")
		}
		if awgRandomTrailers {
			fmt.Fprintf(&b, "RandomTrailers = on\n")
		}
	}

	fmt.Fprintf(&b, "\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", warpPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowedIPs(o.ipv6))
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keepalive)

	return b.String()
}

// Routing a family the interface carries no address for only blackholes it, so
// AllowedIPs follows -6 the same way Address does.
func allowedIPs(ipv6 bool) string {
	if ipv6 {
		return "::/0"
	}
	return "0.0.0.0/0"
}

const confStdout = "-"

func writeConf(o options, endpoint string, run protoRun) error {
	conf, err := renderConfFor(o, endpoint, run)
	if err != nil {
		return err
	}
	if o.conf == confStdout {
		_, err := os.Stdout.Write(conf)
		return err
	}
	return os.WriteFile(o.conf, conf, 0600)
}

func renderConfFor(o options, endpoint string, run protoRun) ([]byte, error) {
	if isMihomo(o.confType) {
		return renderMihomoConf(o, endpoint, run)
	}
	if run.isMASQUE() {
		return renderMasqueConf(endpoint, run.isH2())
	}
	return []byte(renderConf(o, endpoint, run)), nil
}

const (
	confTypeNative     = "native"
	confTypeMihomo     = "mihomo"
	confTypeMihomoJSON = "mihomo-json"
)

var confTypes = []string{confTypeNative, confTypeMihomo, confTypeMihomoJSON}

func isMihomo(confType string) bool {
	return confType == confTypeMihomo || confType == confTypeMihomoJSON
}

const (
	warpDNSv4 = "1.1.1.1, 1.0.0.1"
	warpDNSv6 = "2606:4700:4700::1111, 2606:4700:4700::1001"
)

func confDNS(o options) string {
	if o.noDNS {
		return ""
	}
	if o.dns != "" {
		return o.dns
	}
	if o.ipv6 {
		return warpDNSv6
	}
	return warpDNSv4
}

// mihomo takes the resolvers as a list, the .conf as one line.
func confDNSList(o options) []string {
	dns := confDNS(o)
	if dns == "" {
		return nil
	}
	list := strings.Split(dns, ",")
	for i := range list {
		list[i] = strings.TrimSpace(list[i])
	}
	return list
}

// A mihomo proxy is built as data rather than text, because the same fields go
// out as YAML and as JSON; an ordered list keeps the YAML fields in the order
// they are written here, which a map would not.
type kv struct {
	k string
	v any // string, quoted, int, bool, []string, []kv, [][]kv
}

// Proxy names are the only scalars YAML has to quote.
type quoted string

func mihomoAddr(v4, v6 string, ipv6 bool) kv {
	if ipv6 {
		return kv{"ipv6", v6}
	}
	return kv{"ip", v4}
}

// mihomo lists proxies by name, so several warpscout configs pasted into one
// file have to differ by protocol. The endpoint address stays out of it: the run
// already picked the single best one, and the name would go stale on the next scan.
func mihomoName(run protoRun) string {
	switch run.kind {
	case kindAWG:
		return "AWG WARP"
	case kindMASQUE:
		return "MASQUE H3 WARP"
	case kindMASQUEH2:
		return "MASQUE H2 WARP"
	}
	return "WG WARP"
}

// mihomo keys proxies by name, so the two halves of a chain have to differ - and
// naming them is the only place the config says it is one ("AWG WARP OUTER" plus
// "WG WARP-in-WARP", against a plain run's bare "WG WARP").
const (
	mihomoOuterSuffix = " OUTER"
	mihomoChainSuffix = "-in-WARP"
)

func renderMihomoConf(o options, endpoint string, run protoRun) ([]byte, error) {
	proxies, err := mihomoProxies(o, endpoint, run)
	if err != nil {
		return nil, err
	}
	if o.confType == confTypeMihomoJSON {
		return mihomoJSON(proxies)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "proxies:\n")
	for _, p := range proxies {
		writeYAML(&b, p, "  ", "- ")
	}
	return []byte(b.String()), nil
}

func mihomoProxies(o options, endpoint string, run protoRun) ([][]kv, error) {
	if outer == nil {
		p, err := mihomoProxy(o, mihomoName(run), endpoint, run, o.mtu, confDNSList(o))
		if err != nil {
			return nil, err
		}
		return [][]kv{p}, nil
	}

	// mihomo expresses the chain itself: the outer tunnel is a proxy of its own
	// and the inner one dials through it. The outer carries no DNS - it is only
	// the carrier, and the resolvers belong at the end of the chain.
	outerName := mihomoName(outer.run) + mihomoOuterSuffix
	var op []kv
	err := withOuterKeys(func() error {
		var err error
		op, err = mihomoProxy(o, outerName, outer.endpoint, outer.run, o.mtu, nil)
		return err
	})
	if err != nil {
		return nil, err
	}

	inner, err := mihomoProxy(o, mihomoName(run)+mihomoChainSuffix, endpoint, run, nestedMTU(o.mtu), confDNSList(o))
	if err != nil {
		return nil, err
	}
	return [][]kv{op, append(inner, kv{"dialer-proxy", quoted(outerName)})}, nil
}

// The list-of-maps case wants "- " on the first key of each item and blanks
// under it, which is why the lead is separate from the indent.
func writeYAML(b *strings.Builder, node []kv, indent, lead string) {
	pad := indent + strings.Repeat(" ", len(lead))
	for i, e := range node {
		prefix := pad
		if i == 0 {
			prefix = indent + lead
		}
		switch v := e.v.(type) {
		case []kv:
			fmt.Fprintf(b, "%s%s:\n", prefix, e.k)
			writeYAML(b, v, pad+"  ", "")
		case [][]kv:
			fmt.Fprintf(b, "%s%s:\n", prefix, e.k)
			for _, item := range v {
				writeYAML(b, item, pad+"  ", "- ")
			}
		default:
			fmt.Fprintf(b, "%s%s: %s\n", prefix, e.k, yamlScalar(e.v))
		}
	}
}

func yamlScalar(v any) string {
	switch t := v.(type) {
	case quoted:
		return fmt.Sprintf("%q", string(t))
	case []string:
		return "['" + strings.Join(t, "', '") + "']"
	}
	return fmt.Sprint(v)
}

// Go maps have no order, so the JSON is written from the same []kv the YAML is
// rather than through map[string]any: the fields come out in the order
// mihomoProxy builds them, the same one -conf-type mihomo prints.
func mihomoJSON(proxies [][]kv) ([]byte, error) {
	var b strings.Builder
	b.WriteString("[\n  ")
	for i, p := range proxies {
		if i > 0 {
			b.WriteString(",\n  ")
		}
		if err := writeJSONObject(&b, p, "  "); err != nil {
			return nil, err
		}
	}
	b.WriteString("\n]\n")
	return []byte(b.String()), nil
}

func writeJSONObject(b *strings.Builder, node []kv, indent string) error {
	b.WriteString("{\n")
	for i, e := range node {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(b, "%s  %q: ", indent, e.k)
		if err := writeJSONValue(b, e.v, indent+"  "); err != nil {
			return err
		}
	}
	fmt.Fprintf(b, "\n%s}", indent)
	return nil
}

func writeJSONValue(b *strings.Builder, v any, indent string) error {
	switch t := v.(type) {
	case []kv:
		return writeJSONObject(b, t, indent)
	case [][]kv:
		b.WriteString("[\n")
		for i, item := range t {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(indent + "  ")
			if err := writeJSONObject(b, item, indent+"  "); err != nil {
				return err
			}
		}
		fmt.Fprintf(b, "\n%s]", indent)
		return nil
	case []string:
		// Two resolvers or one allowed-ips entry: a line each reads worse than
		// the list inline.
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = fmt.Sprintf("%q", item)
		}
		fmt.Fprintf(b, "[%s]", strings.Join(parts, ", "))
		return nil
	case quoted:
		v = string(t)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The awg i1 value is angle brackets all the way down, and the default
	// encoder would turn every one of them into \u003c.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.WriteString(strings.TrimSuffix(buf.String(), "\n"))
	return nil
}

// The inner WireGuard packet rides as UDP inside the outer tunnel, so it cannot
// be left at the client's default: at full MTU the handshake still completes and
// data dies silently (the same nestedOverhead tunnel.go subtracts).
func nestedMTU(mtu int) int {
	if mtu == 0 {
		mtu = tunnelMTU
	}
	return mtu - nestedOverhead
}

func mihomoProxy(o options, name, endpoint string, run protoRun, mtu int, dns []string) ([]kv, error) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}

	p, err := mihomoPeer(run, o.ipv6, host, port)
	if err != nil {
		return nil, err
	}
	p = append([]kv{{"name", quoted(name)}}, p...)
	if mtu > 0 {
		p = append(p, kv{"mtu", mtu})
	}
	p = append(p, kv{"udp", true})
	if len(dns) > 0 {
		p = append(p, kv{"remote-dns-resolve", true}, kv{"dns", dns})
	}
	return p, nil
}

func mihomoPeer(run protoRun, ipv6 bool, host string, port int) ([]kv, error) {
	if run.isMASQUE() {
		if masqueAcct == nil {
			return nil, fmt.Errorf("no MASQUE device in the account file")
		}
		pub, err := pemBody(masqueAcct.PeerPublicKey)
		if err != nil {
			return nil, err
		}
		p := []kv{{"type", "masque"}, {"server", host}, {"port", port}}
		// mihomo's default is HTTP/3; the TCP transport is the same type with a
		// network selector rather than a type of its own.
		if run.isH2() {
			p = append(p, kv{"network", "h2"})
		}
		return append(p,
			kv{"sni", masqueSNI},
			kv{"private-key", masqueAcct.PrivateKey},
			kv{"public-key", pub},
			mihomoAddr(masqueAcct.IPv4, masqueAcct.IPv6, ipv6),
		), nil
	}

	// The single-peer fields mihomo still accepts at the top level are its legacy
	// syntax; a peers list is the current one.
	peer := []kv{
		{"server", host},
		{"port", port},
		{"public-key", warpPublicKey},
		{"allowed-ips", []string{allowedIPs(ipv6)}},
		{"persistent-keepalive", keepalive},
	}
	p := []kv{
		{"type", "wireguard"},
		{"private-key", warpPrivateKey},
		mihomoAddr(warpAddress, warpAddressV6, ipv6),
		{"peers", [][]kv{peer}},
	}
	if !run.isAWG() {
		return p, nil
	}

	var awg []kv
	// mihomo runs the legacy AmneziaWG engine unless version is 3.
	if run.isAWG31() {
		awg = append(awg, kv{"version", 3})
	}
	awg = append(awg, kv{"jc", awgJc}, kv{"jmin", awgJmin}, kv{"jmax", awgJmax}, kv{"s1", 0}, kv{"s2", 0})
	for i, h := range []int{1, 2, 3, 4} {
		awg = append(awg, kv{fmt.Sprintf("h%d", i+1), h})
	}
	if awgI1 != "" {
		awg = append(awg, kv{"i1", awgI1})
	}
	if run.isAWG31() {
		for _, r := range awg31Ranges {
			if r.value != "" {
				awg = append(awg, kv{r.mihomo, r.value})
			}
		}
		if awgDisableCookies {
			awg = append(awg, kv{"disable-cookies", true})
		}
		if awgRandomTrailers {
			awg = append(awg, kv{"random-trailers", true})
		}
	}
	return append(p, kv{"amnezia-wg-option", awg}), nil
}

func pemBody(key string) (string, error) {
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return "", fmt.Errorf("peer public key is not PEM")
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

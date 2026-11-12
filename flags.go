package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type options struct {
	tunnelParallel int
	timeoutSec     int
	perSubnet      int
	tunPingCount   int
	threshold      int
	mtu            int
	port           int
	proto          string
	listen         string
	endpoint       string
	output         string
	conf           string
	configs        int
	confType       string
	bestBy         string
	sweepPorts     string
	pingTarget     string
	dns            string
	proxy          string
	relay          string
	freshAccount   bool
	iface          string
	accountPath    string
	genI1          string
	i1Host         string
	target         string
	through        string
	innerProto     string
	node           string
	country        string
	excludeNode    string
	excludeCountry string
	targets        []netip.Prefix
	colos          []string
	countries      []string
	dropColos      []string
	dropCountries  []string
	best           bool
	noReport       bool
	tableOff       bool
	noDNS          bool
	i1Explicit     bool
	tunPingCheck   bool
	speed          bool
	genJunk        bool
	wantMeta       bool
	ipv6           bool
	full           bool
	plain          bool
	emoji          bool
}

type flagSpec struct {
	short string
	long  string
	meta  string
	help  string
}

type flagGroup struct {
	title string
	specs []flagSpec
}

var (
	netSpecs = []flagSpec{
		{"t", "timeout", "SEC", "per-request timeout"},
		{"6", "ipv6", "", "use IPv6 endpoint pools instead of IPv4"},
		{"", "target", "ADDR", "use these addresses instead of the built-in pools: comma-separated IPs or CIDRs"},
		{"I", "interface", "NAME", "work through this interface (bind to device; Linux, may need CAP_NET_RAW)"},
		{"a", "account", "FILE", "cached WARP account file"},
	}

	scanGroup = flagGroup{"Scan tuning", append([]flagSpec{
		{"jt", "tunnel-jobs", "N", "phase 2 tunnel workers"},
		{"P", "tun-ping", "", fmt.Sprintf("add the TUN PING/LOSS columns: RTT and packet loss measured inside the tunnel to %s, and flag endpoints DPI tears down mid-stream; off by default for speed", pingTarget)},
		{"", "ping-target", "ADDR", fmt.Sprintf("IP address or hostname the TUN PING/LOSS columns measure to, resolved inside the tunnel (default %s); needs -tun-ping", pingTarget)},
		{"", "tun-ping-count", "N", fmt.Sprintf("echoes per durability burst, %dms apart - a longer burst catches tunnels DPI kills late (default %d, implies -tun-ping)", pingInterval.Milliseconds(), durabilityPings)},
		{"", "speed", "", "add the SPEED column: after the scan, download-test every endpoint the tables pick, one at a time (slow, and it does not change the ranking)"},
		{"n", "sample", "N", "addresses to sample per subnet"},
		{"f", "full", "", "scan all 256 addresses per subnet (overrides -sample)"},
		{"", "port", "N", "probe only this port on every endpoint instead of picking the first reachable one (skips phase 1)"},
		{"", "sweep-ports", "open|all", fmt.Sprintf("report every port of an endpoint as its own result instead of keeping the first that answers: %s sweeps the ports phase 1 found, %s sweeps every known WARP port and skips phase 1 (slow - meant for -target)", sweepOpen, sweepAll)},
	}, netSpecs...)}

	nestGroup = flagGroup{"WARP-in-WARP", []flagSpec{
		{"", "through", "ADDR", "scan from inside a tunnel to this endpoint (IP or ip:port, as -best prints): results then exit through that endpoint's region (-tunnel-jobs drops to 1)"},
		{"", "inner-proto", "wg|awg", "protocol of the tunnels being scanned, which run inside the -through one where DPI cannot see them; -proto stays the outer tunnel, the one that crosses your network"},
	}}

	masqueGroup = flagGroup{"MASQUE", []flagSpec{
		{"", "masque-sni", "HOST", "SNI to present to the MASQUE endpoint; a different name often gets through DPI"},
		{"", "masque-attempts", "N", "how many times to re-check a failing MASQUE endpoint before calling it dead"},
	}}

	protoGroup = flagGroup{"Protocol", []flagSpec{
		{"p", "proto", "wg|awg|masque|masque-h2", "tunnel protocol: wg (WireGuard), awg (AmneziaWG), masque (CONNECT-IP over QUIC) or masque-h2 (CONNECT-IP over TCP)"},
	}}

	awgGroup = flagGroup{"AmneziaWG obfuscation parameters", []flagSpec{
		{"", "gen-junk", "", "randomize junk params per run (overridden by -jc/-jmin/-jmax)"},
		{"", "gen-i1", "PROTO", "generate the init packet per run: quic, dns, sip, stun or random"},
		{"", "i1-sni", "HOST", "host to mimic in the generated I1 (default: a random well-known host)"},
		{"", "jc", "N", "junk packet count"},
		{"", "jmin", "N", "min junk packet size"},
		{"", "jmax", "N", "max junk packet size"},
		{"", "i1", "PKT", "custom init packet, or \"none\" to send none (default: built-in iCloud probe)"},
	}}

	outputGroup = flagGroup{"Output", []flagSpec{
		{"o", "output", "FILE", "full per-endpoint report file (default warpscout-report-<timestamp>.txt)"},
		{"", "no-report", "", "skip the report file entirely (overrides -o)"},
		{"", "conf", "FILE", "write a ready-to-import config for the best endpoint (\"-\" prints it instead)"},
		{"", "configs", "N", "write configs for the top N endpoints (-conf sets the base filename, default warp.conf)"},
		{"", "conf-type", "KIND", "format of -conf: native (wg/awg .conf, usque config.json), mihomo or mihomo-json"},
		{"", "table-off", "", "add \"Table = off\" to the generated config: bring the interface up without touching routes"},
		{"", "mtu", "N", "set MTU in the generated config (default: leave the line out)"},
		{"", "dns", "LIST", "DNS servers in the generated config: comma-separated (default: Cloudflare, following -6)"},
		{"", "no-dns", "", "leave the DNS line out of the generated config"},
		{"", "node", "COLO", "keep only endpoints landing on these edge nodes: comma-separated IATA codes"},
		{"", "country", "ISO", "keep only endpoints whose edge node sits in these countries: comma-separated ISO codes"},
		{"", "exclude-node", "COLO", "drop endpoints landing on these edge nodes: comma-separated IATA codes"},
		{"", "exclude-country", "ISO", "drop endpoints whose edge node sits in these countries: comma-separated ISO codes"},
		{"", "best", "", "print just the best endpoint as ip:port on stdout (for scripts and pipes)"},
		{"", "best-by", "ping|speed", "what makes an endpoint best, both for -best/-conf and for the order of every table: lowest ping (default) or highest download speed (runs the -speed phase, so it takes much longer)"},
		{"", "plain", "", "force plain line output (no live TUI)"},
		{"", "emoji", "", "prefix the colo region with a country flag emoji (rendering depends on the terminal)"},
	}}

	registerGroup = flagGroup{"Registration", append([]flagSpec{
		{"x", "proxy", "URL", "http(s)/socks5 proxy for registration"},
		{"", "relay", "URL", "relay to try when the API is unreachable, before the WARP tunnel (\"none\" disables it)"},
		{"", "fresh", "", "ignore the existing account file and register a brand-new account"},
	}, netSpecs...)}

	findJunkGroup = flagGroup{"Search tuning", append([]flagSpec{
		{"jt", "tunnel-jobs", "N", "tunnel workers per attempt"},
		{"n", "sample", "N", "addresses to sample per subnet"},
		{"", "threshold", "PCT", "accept a junk set once this share of sampled endpoints works"},
	}, netSpecs...)}

	findSNIGroup = flagGroup{"Search tuning", append([]flagSpec{
		{"p", "proto", "masque|masque-h2", "which MASQUE transport to search an SNI for: QUIC or TCP"},
		{"jt", "tunnel-jobs", "N", "tunnel workers per round"},
		{"", "threshold", "PCT", "accept an SNI once this share of the endpoints works"},
		{"", "masque-attempts", "N", "how many times to re-check a failing MASQUE endpoint before calling it dead"},
	}, netSpecs...)}

	socksGroup = flagGroup{"SOCKS proxy", []flagSpec{
		{"e", "endpoint", "ADDR", "WARP endpoint to tunnel through (IP or ip:port, as \"scan -best\" prints)"},
		{"P", "port", "N", "port the SOCKS5 server listens on"},
		{"l", "listen", "ADDR", "address the SOCKS5 server listens on"},
		{"t", "timeout", "SEC", "per-request timeout"},
		{"I", "interface", "NAME", "work through this interface (bind to device; Linux, may need CAP_NET_RAW)"},
		{"a", "account", "FILE", "cached WARP account file"},
	}}

	findJunkI1Group = flagGroup{"AmneziaWG init packet", []flagSpec{
		{"", "gen-i1", "PROTO", "generate the init packet per attempt: quic, dns, sip, stun or random"},
		{"", "i1-sni", "HOST", "host to mimic in the generated I1 (default: a random well-known host)"},
	}}

	plainGroup = flagGroup{"Output", []flagSpec{
		{"", "plain", "", "force plain line output (no live TUI)"},
	}}
)

const defaultTunnelJobs = 10

func intFlag(fs *flag.FlagSet, p *int, def int, short, long string) {
	fs.IntVar(p, short, def, "")
	fs.IntVar(p, long, def, "")
}

func validateSample(n int) error {
	if n < 1 {
		return fmt.Errorf("must be at least 1")
	}
	return nil
}

func intFlagValidate(fs *flag.FlagSet, p *int, def int, short, long string, validate func(int) error) {
	*p = def
	set := func(s string) error {
		n, err := strconv.ParseInt(s, 0, strconv.IntSize)
		if err != nil {
			return fmt.Errorf("parse error")
		}
		if err := validate(int(n)); err != nil {
			return err
		}
		*p = int(n)
		return nil
	}
	for _, name := range []string{short, long} {
		fs.Func(name, "", set)
		fs.Lookup(name).DefValue = strconv.Itoa(def)
	}
}

func strFlag(fs *flag.FlagSet, p *string, def, short, long string) {
	fs.StringVar(p, short, def, "")
	fs.StringVar(p, long, def, "")
}

func boolFlag(fs *flag.FlagSet, p *bool, short, long string) {
	fs.BoolVar(p, short, false, "")
	fs.BoolVar(p, long, false, "")
}

func addNetFlags(fs *flag.FlagSet, o *options) {
	intFlag(fs, &o.timeoutSec, 2, "t", "timeout")
	boolFlag(fs, &o.ipv6, "6", "ipv6")
	strFlag(fs, &o.iface, "", "I", "interface")
	strFlag(fs, &o.accountPath, defaultAccount, "a", "account")
	fs.StringVar(&o.target, "target", "", "")
}

func addAWGFlags(fs *flag.FlagSet, o *options) {
	fs.BoolVar(&o.genJunk, "gen-junk", false, "")
	fs.IntVar(&awgJc, "jc", awgJc, "")
	fs.IntVar(&awgJmin, "jmin", awgJmin, "")
	fs.IntVar(&awgJmax, "jmax", awgJmax, "")
	fs.StringVar(&awgI1, "i1", awgI1, "")
	addI1GenFlags(fs, o)
}

func addI1GenFlags(fs *flag.FlagSet, o *options) {
	fs.StringVar(&o.genI1, "gen-i1", "", "")
	fs.StringVar(&o.i1Host, "i1-sni", "", "")
}

func setupScanFlags(fs *flag.FlagSet, o *options) {
	addNetFlags(fs, o)
	addAWGFlags(fs, o)
	intFlag(fs, &o.tunnelParallel, defaultTunnelJobs, "jt", "tunnel-jobs")
	intFlagValidate(fs, &o.perSubnet, 5, "n", "sample", validateSample)
	fs.IntVar(&o.port, "port", 0, "")
	fs.StringVar(&o.sweepPorts, "sweep-ports", "", "")
	fs.StringVar(&o.pingTarget, "ping-target", "", "")
	strFlag(fs, &o.proto, protoWG, "p", "proto")
	strFlag(fs, &o.output, "", "o", "output")
	fs.BoolVar(&o.noReport, "no-report", false, "")
	boolFlag(fs, &o.tunPingCheck, "P", "tun-ping")
	fs.IntVar(&o.tunPingCount, "tun-ping-count", 0, "")
	fs.BoolVar(&o.speed, "speed", false, "")
	boolFlag(fs, &o.full, "f", "full")
	fs.StringVar(&o.node, "node", "", "")
	fs.StringVar(&o.country, "country", "", "")
	fs.StringVar(&o.excludeNode, "exclude-node", "", "")
	fs.StringVar(&o.excludeCountry, "exclude-country", "", "")
	fs.BoolVar(&o.best, "best", false, "")
	fs.StringVar(&o.bestBy, "best-by", bestKeyPing, "")
	fs.StringVar(&o.conf, "conf", "", "")
	fs.IntVar(&o.configs, "configs", 0, "")
	fs.StringVar(&o.confType, "conf-type", confTypeNative, "")
	fs.StringVar(&masqueSNI, "masque-sni", masqueDefaultSNI, "")
	fs.IntVar(&masqueAttempts, "masque-attempts", masqueDefaultAttempts, "")
	fs.StringVar(&o.through, "through", "", "")
	fs.StringVar(&o.innerProto, "inner-proto", protoWG, "")
	fs.BoolVar(&o.tableOff, "table-off", false, "")
	fs.IntVar(&o.mtu, "mtu", 0, "")
	fs.StringVar(&o.dns, "dns", "", "")
	fs.BoolVar(&o.noDNS, "no-dns", false, "")
	fs.BoolVar(&o.plain, "plain", false, "")
	fs.BoolVar(&o.emoji, "emoji", false, "")
	o.wantMeta = true
}

func setupRegisterFlags(fs *flag.FlagSet, o *options) {
	addNetFlags(fs, o)
	addAWGFlags(fs, o)
	strFlag(fs, &o.proxy, "", "x", "proxy")
	fs.StringVar(&o.relay, "relay", defaultRelay, "")
	fs.BoolVar(&o.freshAccount, "fresh", false, "")
	fs.BoolVar(&o.plain, "plain", false, "")
	// The tunnel fallback sweeps both protocols anyway; awg goes first because it
	// is the one that survives DPI, which is why the fallback ran at all.
	o.proto = protoAWG
	o.perSubnet = registerSample
}

func setupFindJunkFlags(fs *flag.FlagSet, o *options) {
	addNetFlags(fs, o)
	addI1GenFlags(fs, o)
	intFlag(fs, &o.tunnelParallel, defaultTunnelJobs, "jt", "tunnel-jobs")
	intFlagValidate(fs, &o.perSubnet, findJunkSample, "n", "sample", validateSample)
	fs.IntVar(&o.threshold, "threshold", defaultJunkThreshold, "")
	fs.BoolVar(&o.plain, "plain", false, "")
	o.proto = protoAWG
	o.tunPingCheck = true
}

func setupFindSNIFlags(fs *flag.FlagSet, o *options) {
	addNetFlags(fs, o)
	intFlag(fs, &o.tunnelParallel, masqueRoundTargets, "jt", "tunnel-jobs")
	fs.IntVar(&o.threshold, "threshold", defaultSNIThreshold, "")
	fs.IntVar(&masqueAttempts, "masque-attempts", masqueDefaultAttempts, "")
	fs.BoolVar(&o.plain, "plain", false, "")
	strFlag(fs, &o.proto, protoMASQUE, "p", "proto")
	o.perSubnet = findSNISample
	o.tunPingCheck = true
}

func setupSocksFlags(fs *flag.FlagSet, o *options) {
	intFlag(fs, &o.timeoutSec, 2, "t", "timeout")
	strFlag(fs, &o.iface, "", "I", "interface")
	strFlag(fs, &o.accountPath, defaultAccount, "a", "account")
	strFlag(fs, &o.endpoint, "", "e", "endpoint")
	strFlag(fs, &o.listen, defaultSocksListen, "l", "listen")
	intFlag(fs, &o.port, defaultSocksPort, "P", "port")
	strFlag(fs, &o.proto, protoWG, "p", "proto")
	addAWGFlags(fs, o)
	fs.StringVar(&masqueSNI, "masque-sni", masqueDefaultSNI, "")
	fs.IntVar(&masqueAttempts, "masque-attempts", masqueDefaultAttempts, "")
	fs.StringVar(&o.through, "through", "", "")
	fs.StringVar(&o.innerProto, "inner-proto", protoWG, "")
	o.perSubnet = 1
}

// Flags a command does not register stay at their zero value, so each step here
// is a no-op for the commands it does not apply to.
func applyCommonFlags(fs *flag.FlagSet, o *options) {
	for _, f := range []struct{ name, value string }{{"conf", o.conf}, {"o", o.output}} {
		if f.name == "conf" && f.value == confStdout {
			continue
		}
		if strings.HasPrefix(f.value, "-") {
			fmt.Fprintf(os.Stderr, "-%s needs a file name, got %q\n", f.name, f.value)
			os.Exit(2)
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fs.Arg(0))
		os.Exit(2)
	}
	if awgI1 == i1Keyword {
		awgI1 = ""
	}
	// One invariant for everything downstream: pings is the burst length and
	// tunPingCheck is "burst at all", so -tun-ping-count alone turns the measurement on rather than
	// silently measuring nothing.
	o.tunPingCount = max(o.tunPingCount, 0)
	if o.tunPingCount > 0 {
		o.tunPingCheck = true
	} else if o.tunPingCheck {
		o.tunPingCount = durabilityPings
	}
	// Too short a burst hands back dead endpoints as working: measured 8 of 70
	// "working" wg peers at -tun-ping-count 2 (where a tail run does not even fit) and 13 at
	// -tun-ping-count 3, all of which -tun-ping-count 10 proved dead.
	if o.tunPingCount > 0 && o.tunPingCount < minDurabilityPings {
		fmt.Fprintf(os.Stderr, "-tun-ping-count must be at least %d: a shorter burst reports torn-down endpoints as working\n", minDurabilityPings)
		os.Exit(2)
	}
	if o.port < 0 || o.port > 65535 {
		fmt.Fprintln(os.Stderr, "-port must be between 1 and 65535")
		os.Exit(2)
	}
	if o.genJunk {
		if o.proto != protoAWG {
			fmt.Fprintln(os.Stderr, "-gen-junk needs AmneziaWG: use -proto awg")
			os.Exit(2)
		}
		applyGenJunk(fs)
	}
	applyGenI1(fs, o)
	validateJunkParams()
	validateMTU(*o)
	validateConfigs(fs, *o)
	validateConfType(*o)
	validateSweepPorts(*o)
	applyPingTarget(*o)
	validateBestBy(o)
	rejectBestConfStdout(*o)
	applyDNS(o)
	applyTarget(o)
	applyNode(o)
	rejectMasqueFilters(*o)
	validateMasqueAttempts()
	validateThrough(fs, *o)
	applyNestedJobs(fs, o)
}

// Both tunnels are WireGuard: only the wg pools hand out a choice of node, which
// is the whole point of nesting, and MASQUE gives one node per network.
func validateThrough(fs *flag.FlagSet, o options) {
	if o.through == "" {
		if explicitFlags(fs)["inner-proto"] {
			fmt.Fprintln(os.Stderr, "-inner-proto needs -through ADDR")
			os.Exit(2)
		}
		return
	}
	for _, f := range []struct{ name, value string }{{"-proto", o.proto}, {"-inner-proto", o.innerProto}} {
		if f.value != protoWG && f.value != protoAWG {
			fmt.Fprintf(os.Stderr, "-through nests two WireGuard tunnels: %s must be %s or %s, not %q\n", f.name, protoWG, protoAWG, f.value)
			os.Exit(2)
		}
	}
	if _, err := parseEndpointSpec("-through", o.through); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// Every inner tunnel shares the one outer device and its one netstack, and that
// is the bottleneck: measured on the same 28 endpoints, 28/28 worked at 1 worker,
// 12/28 at 3 and 3/28 at the usual 10, the rest reported as torn down. An
// explicit -jt still wins - the user may be nesting through a fatter path.
const nestedTunnelJobs = 1

func applyNestedJobs(fs *flag.FlagSet, o *options) {
	if o.through == "" || explicitFlags(fs)["jt"] || explicitFlags(fs)["tunnel-jobs"] {
		return
	}
	o.tunnelParallel = nestedTunnelJobs
}

// find-sni is a MASQUE-only command, but it has two transports to search now,
// so -proto is registered and then bounded here rather than pinned.
func validateSNIProto(o options) {
	if o.proto == protoMASQUE || o.proto == protoMASQUEH2 {
		return
	}
	fmt.Fprintf(os.Stderr, "find-sni searches a MASQUE SNI: -proto must be %s or %s, not %q\n", protoMASQUE, protoMASQUEH2, o.proto)
	os.Exit(2)
}

func applyTarget(o *options) {
	if o.target == "" {
		return
	}
	targets, err := parseTargets(o.target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	o.targets = targets
	o.ipv6 = !targets[0].Addr().Is4()
}

func applyNode(o *options) {
	o.colos = splitList(o.node, "-node")
	o.countries = splitList(o.country, "-country")
	o.dropColos = splitList(o.excludeNode, "-exclude-node")
	o.dropCountries = splitList(o.excludeCountry, "-exclude-country")
}

func splitList(value, flagName string) []string {
	if value == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToUpper(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		fmt.Fprintln(os.Stderr, flagName+" is empty")
		os.Exit(2)
	}
	return out
}

func applyGenI1(fs *flag.FlagSet, o *options) {
	explicit := explicitFlags(fs)
	o.i1Explicit = explicit["i1"] || o.genI1 != ""

	if o.genI1 == "" {
		if explicit["i1-sni"] {
			fmt.Fprintln(os.Stderr, "-i1-sni needs -gen-i1 PROTO")
			os.Exit(2)
		}
		return
	}
	if o.proto != protoAWG {
		fmt.Fprintln(os.Stderr, "-gen-i1 needs AmneziaWG: use -proto awg")
		os.Exit(2)
	}
	if explicit["i1"] {
		fmt.Fprintln(os.Stderr, "-gen-i1 and -i1 set the same init packet: drop one")
		os.Exit(2)
	}
	if err := regenI1(*o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func applyGenJunk(fs *flag.FlagSet) {
	explicit := explicitFlags(fs)

	jc, jmin, jmax := awgJc, awgJmin, awgJmax
	genJunkParams()
	if explicit["jc"] {
		awgJc = jc
	}
	if explicit["jmin"] {
		awgJmin = jmin
	}
	if explicit["jmax"] {
		awgJmax = jmax
	}
}

func explicitFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func validateJunkParams() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "invalid junk params: "+format+"\n", args...)
		os.Exit(2)
	}
	if awgJc < junkCountLimitMin || awgJc > junkCountLimitMax {
		fail("-jc (%d) must be between %d and %d", awgJc, junkCountLimitMin, junkCountLimitMax)
	}
	if awgJmin > awgJmax {
		fail("-jmin (%d) must be <= -jmax (%d)", awgJmin, awgJmax)
	}
	if awgJmax > tunnelMTU {
		fail("-jmax (%d) must be <= %d", awgJmax, tunnelMTU)
	}
}

func validateMTU(o options) {
	if o.mtu == 0 {
		return
	}
	if o.mtu < mtuMin || o.mtu > mtuMax {
		fmt.Fprintf(os.Stderr, "-mtu (%d) must be between %d and %d\n", o.mtu, mtuMin, mtuMax)
		os.Exit(2)
	}
}

func checkConfigs(configs int, conf string, visited bool) error {
	if visited && configs < 1 {
		return fmt.Errorf("-configs (%d) must be at least 1", configs)
	}
	if configs > 0 && conf == confStdout {
		return fmt.Errorf("-configs needs file output, not stdout (\"-\")")
	}
	return nil
}

func validateConfigs(fs *flag.FlagSet, o options) {
	configsVisited := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "configs" {
			configsVisited = true
		}
	})
	if err := checkConfigs(o.configs, o.conf, configsVisited); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// Both write a machine-readable artifact to stdout, so together they produce a
// config with an endpoint line glued on top of it - not a config any client reads.
func rejectBestConfStdout(o options) {
	if o.best && o.conf == confStdout {
		fmt.Fprintln(os.Stderr, "-best and -conf - both write to stdout: use one or the other")
		os.Exit(2)
	}
}

func validateBestBy(o *options) {
	if o.bestBy == "" {
		return
	}
	if !slices.Contains(bestKeys, o.bestBy) {
		fmt.Fprintf(os.Stderr, "-best-by %q must be one of %s\n", o.bestBy, strings.Join(bestKeys, ", "))
		os.Exit(2)
	}
	bestBy = o.bestBy
}

// -port pins a single port, and MASQUE already reports one result per port, so
// neither combination has anything left to sweep.
func validateSweepPorts(o options) {
	if o.sweepPorts == "" {
		return
	}
	if !slices.Contains(sweepModes, o.sweepPorts) {
		fmt.Fprintf(os.Stderr, "-sweep-ports %q must be one of %s\n", o.sweepPorts, strings.Join(sweepModes, ", "))
		os.Exit(2)
	}
	if o.port != 0 {
		fmt.Fprintln(os.Stderr, "-sweep-ports and -port contradict each other: -port pins a single port")
		os.Exit(2)
	}
	if o.proto == protoMASQUE || o.proto == protoMASQUEH2 {
		fmt.Fprintf(os.Stderr, "-sweep-ports does not apply to -proto %s: every MASQUE port is already its own result\n", o.proto)
		os.Exit(2)
	}
	sweepingPorts = true
}

// A hostname is allowed and resolved inside the tunnel (pingDst), never on the
// host: a local resolver can answer with an address that only routes outside the
// tunnel - on a fake-IP resolver every answer is one. A port or a URL is a
// mistake, the burst speaks ICMP.
func applyPingTarget(o options) {
	if o.pingTarget == "" {
		return
	}
	if !o.tunPingCheck && o.tunPingCount <= 0 {
		fmt.Fprintln(os.Stderr, "-ping-target needs -tun-ping")
		os.Exit(2)
	}
	if addr, err := netip.ParseAddr(o.pingTarget); err == nil {
		pingTarget = addr.String()
		return
	}
	if strings.ContainsAny(o.pingTarget, ":/ ") {
		fmt.Fprintf(os.Stderr, "-ping-target %q must be an IP address or a hostname\n", o.pingTarget)
		os.Exit(2)
	}
	pingTarget = o.pingTarget
}

func validateConfType(o options) {
	if o.confType == "" {
		return
	}
	if !slices.Contains(confTypes, o.confType) {
		fmt.Fprintf(os.Stderr, "-conf-type %q must be one of %s\n", o.confType, strings.Join(confTypes, ", "))
		os.Exit(2)
	}
	if o.conf == "" && o.configs == 0 && o.confType != confTypeNative {
		fmt.Fprintln(os.Stderr, "-conf-type needs -conf FILE or -configs N")
		os.Exit(2)
	}
	if o.tableOff && isMihomo(o.confType) {
		fmt.Fprintf(os.Stderr, "-table-off does not apply to -conf-type %s: the client owns the routes\n", o.confType)
		os.Exit(2)
	}
}

func applyDNS(o *options) {
	if o.dns == "" && !o.noDNS {
		return
	}
	if o.conf == "" && o.configs == 0 {
		fmt.Fprintln(os.Stderr, "-dns and -no-dns need -conf FILE or -configs N")
		os.Exit(2)
	}
	if o.dns != "" && o.noDNS {
		fmt.Fprintln(os.Stderr, "-dns and -no-dns contradict each other: drop one")
		os.Exit(2)
	}
	if o.noDNS {
		return
	}

	var servers []string
	for _, item := range strings.Split(o.dns, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if net.ParseIP(item) == nil {
			fmt.Fprintf(os.Stderr, "-dns %q is not an IP address\n", item)
			os.Exit(2)
		}
		servers = append(servers, item)
	}
	if len(servers) == 0 {
		fmt.Fprintln(os.Stderr, "-dns is empty")
		os.Exit(2)
	}
	o.dns = strings.Join(servers, ", ")
}

func rootUsage(w io.Writer) {
	st := newConStyles(lipgloss.NewRenderer(w))

	fmt.Fprintln(w, st.title.Render("warpscout")+" - find the exit colo and region of Cloudflare WARP endpoints")
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.title.Render("Usage:"))
	fmt.Fprintf(w, "  %s <command> [options]\n", st.accent.Render("warpscout"))
	fmt.Fprintf(w, "\n%s\n", st.title.Render("Commands"))

	col := 0
	for _, c := range commands {
		if len(c.name) > col {
			col = len(c.name)
		}
	}
	for _, c := range commands {
		fmt.Fprintf(w, "  %s%s%s\n", st.accent.Render(c.name), strings.Repeat(" ", col-len(c.name)+2), c.brief)
	}
	fmt.Fprintf(w, "\nRun %s for the options of a command.\n", st.accent.Render("warpscout <command> -h"))
	fmt.Fprintln(w, "Start with "+st.accent.Render("warpscout register")+" - every other command needs a WARP account.")
}

func commandUsage(w io.Writer, cmd command, fs *flag.FlagSet) {
	st := newConStyles(lipgloss.NewRenderer(w))

	fmt.Fprintln(w, st.title.Render("warpscout "+cmd.name)+" - "+cmd.brief)
	fmt.Fprintln(w)
	for _, line := range cmd.intro {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.title.Render("Usage:"))
	args := " [options]"
	if len(cmd.groups) == 0 {
		args = ""
	}
	fmt.Fprintf(w, "  %s%s\n", st.accent.Render("warpscout "+cmd.name), args)

	col := flagColumnWidth(cmd.groups)
	for _, g := range cmd.groups {
		fmt.Fprintf(w, "\n%s\n", st.title.Render(g.title))
		for _, s := range g.specs {
			names := flagNames(s)
			fmt.Fprintf(w, "%s%s%s%s\n",
				st.accent.Render(names), strings.Repeat(" ", col-len(names)+2), s.help, defaultNote(st, fs, s.long))
		}
	}
}

func flagNames(s flagSpec) string {
	if s.short == "" {
		return fmt.Sprintf("  -%s %s", s.long, s.meta)
	}
	return fmt.Sprintf("  -%s, -%s %s", s.short, s.long, s.meta)
}

func flagColumnWidth(groups []flagGroup) int {
	max := 0
	for _, g := range groups {
		for _, s := range g.specs {
			if n := len(flagNames(s)); n > max {
				max = n
			}
		}
	}
	return max
}

func defaultNote(st conStyles, fs *flag.FlagSet, long string) string {
	f := fs.Lookup(long)
	if f == nil || f.DefValue == "" || f.DefValue == "false" || f.DefValue == "0" {
		return ""
	}
	if len(f.DefValue) > 40 {
		return ""
	}
	return st.dim.Render(fmt.Sprintf(" (default %s)", f.DefValue))
}

func rejectMasqueFilters(o options) {
	if o.proto != protoMASQUE && o.proto != protoMASQUEH2 {
		return
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"-node", len(o.colos) > 0},
		{"-country", len(o.countries) > 0},
		{"-exclude-node", len(o.dropColos) > 0},
		{"-exclude-country", len(o.dropCountries) > 0},
	} {
		if f.set {
			fmt.Fprintf(os.Stderr, "%s does not apply to -proto %s: every MASQUE endpoint exits through the same node\n", f.name, o.proto)
			os.Exit(2)
		}
	}
}

func validateMasqueAttempts() {
	if masqueAttempts < 1 {
		fmt.Fprintln(os.Stderr, "-masque-attempts must be at least 1")
		os.Exit(2)
	}
}

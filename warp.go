package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/netip"
	"slices"
	"strings"
)

// Set once from -interface (main.go) before any tunnel is built. Empty scanInterface
// means the default bind; scanSourceIP is the interface's address, kept for host-ping
// and display only. Global to avoid threading through newTunnel's callers.
var (
	scanInterface string
	scanSourceIP  netip.Addr
)

// Registration (register.go) overwrites these in place, so var not const.
var (
	warpPrivateKey = "4OnO86dDLpqJ2U10ODwX3tarx6xlRGLfkmbSBtMgaHg="
	warpPublicKey  = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	warpAddress    = "172.16.0.2"
	// Replaced by the account's own v6 when the file carries one. The fallback is
	// someone else's device address: it renders a usable config line, but traffic
	// sent from it is dropped, because WARP routes v6 per device instead of NAT-ing it.
	warpAddressV6 = "2606:4700:110:8d87:b0ba:4148:5a61:4873"
)

const (
	tunnelMTU = 1280
	keepalive = 25

	mtuMin = 576
	mtuMax = 1500
)

// -jc/-jmin/-jmax/-i1 override these in place (flags.go), so var not const.
var (
	awgJc   = 6
	awgJmin = 10
	awgJmax = 50
	awgI1   = i1Default
)

const (
	i1Keyword = "none"
	i1Default = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"
)

const (
	junkCountLimitMin = 1
	junkCountLimitMax = 128
)

const (
	junkCountMin = 4
	junkCountMax = 6
	junkSizeMin  = 10
	junkSizeMax  = 50
	junkSpanMin  = 20
	junkSpanMax  = 100
)

func genJunkParams() {
	awgJc = randRange(junkCountMin, junkCountMax)
	awgJmin = randRange(junkSizeMin, junkSizeMax)
	awgJmax = awgJmin + randRange(junkSpanMin, junkSpanMax)
}

func randRange(lo, hi int) int {
	return lo + rand.Intn(hi-lo+1)
}

// The extended set (alternate UDP ports Cloudflare also serves, from
// CloudflareWarpSpeedTest) is only swept when no primary port gets through, so
// a restrictive network still has a chance without making every scan pay 50
// extra per-port timeouts. warpPorts holds what phase 1 found open.
var (
	primaryWarpPorts  = []int{2408, 500, 1701, 4500}
	extendedWarpPorts = []int{
		854, 859, 864, 878, 880, 890, 891, 894, 903, 908,
		928, 934, 939, 942, 943, 945, 946, 955, 968, 987,
		988, 1002, 1010, 1014, 1018, 1070, 1074, 1180, 1387, 1843,
		2371, 2506, 3138, 3476, 3581, 3854, 4177, 4198, 4233, 5279,
		5956, 7103, 7152, 7156, 7281, 7559, 8319, 8742, 8854, 8886,
	}
	warpPorts = primaryWarpPorts
)

// -sweep-ports: "open" keeps phase 1 and sweeps the ports it found, "all" skips
// phase 1 and sweeps every port warpscout knows about.
const (
	sweepOpen = "open"
	sweepAll  = "all"
)

var sweepModes = []string{sweepOpen, sweepAll}

func allWarpPorts() []int {
	return append(slices.Clone(primaryWarpPorts), extendedWarpPorts...)
}

func base64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

type awgRange struct {
	flag, uapi, conf, mihomo, help string
	value                  string
}

// HeaderProtectionKey is omitted: it encrypts the headers, so the peer must hold the key.
var awg31Ranges = []*awgRange{
	{flag: "padding", uapi: "content_padding_addition", conf: "ContentPaddingAddition", mihomo: "content-padding-addition", help: "random bytes appended inside every data packet"},
	{flag: "rekey-after", uapi: "rekey_after_time", conf: "RekeyAfterTime", mihomo: "rekey-after-time", help: "seconds before a session re-handshakes"},
	{flag: "rekey-timeout", uapi: "rekey_timeout", conf: "RekeyTimeout", mihomo: "rekey-timeout", help: "seconds to wait for a handshake answer before retrying"},
	{flag: "reject-after", uapi: "reject_after_time", conf: "RejectAfterTime", mihomo: "reject-after-time", help: "seconds after which a session's keys are refused"},
	{flag: "keepalive-timeout", uapi: "keepalive_timeout", conf: "KeepaliveTimeout", mihomo: "keepalive-timeout", help: "seconds of silence after sent data before a keepalive"},
	{flag: "max-handshakes", uapi: "max_handshake_attempts", conf: "MaxHandshakeAttempts", mihomo: "max-handshake-attempts", help: "handshake retries before giving up"},
}

var awgDisableCookies, awgRandomTrailers bool

func baseUAPI(run protoRun) (string, error) {
	privHex, err := base64ToHex(warpPrivateKey)
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	if run.isAWG() {
		fmt.Fprintf(&b, "jc=%d\n", awgJc)
		fmt.Fprintf(&b, "jmin=%d\n", awgJmin)
		fmt.Fprintf(&b, "jmax=%d\n", awgJmax)
		if awgI1 != "" {
			fmt.Fprintf(&b, "i1=%s\n", awgI1)
		}
	}
	if run.isAWG31() {
		for _, p := range awg31Ranges {
			if p.value != "" {
				fmt.Fprintf(&b, "%s=%s\n", p.uapi, p.value)
			}
		}
		if awgDisableCookies {
			fmt.Fprintf(&b, "disable_cookies=true\n")
		}
		if awgRandomTrailers {
			fmt.Fprintf(&b, "random_trailers=true\n")
		}	}
	return b.String(), nil
}

func peerUAPI(endpoint string) (string, error) {
	pubHex, err := base64ToHex(warpPublicKey)
	if err != nil {
		return "", fmt.Errorf("public key: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "replace_peers=true\n")
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	fmt.Fprintf(&b, "allowed_ip=::/0\n")
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", keepalive)
	return b.String(), nil
}

type protoKind int

const (
	kindWG protoKind = iota
	kindAWG
	kindAWG31
	kindMASQUE
	kindMASQUEH2
)

type protoRun struct {
	kind protoKind
	name string
}

const (
	protoWG       = "wg"
	protoAWG      = "awg"
	protoAWG31    = "awg31"
	protoMASQUE   = "masque"
	protoMASQUEH2 = "masque-h2"
)

// awg31 is awg plus the 3.1 parameters, so every awg branch covers it.
func (r protoRun) isAWG() bool   { return r.kind == kindAWG || r.kind == kindAWG31 }
func (r protoRun) isAWG31() bool { return r.kind == kindAWG31 }

func isAWGName(p string) bool { return p == protoAWG || p == protoAWG31 }

// Both MASQUE kinds run the same CONNECT-IP tunnel and differ only in the
// transport underneath it, so everything that branches on "is this MASQUE"
// stays one branch and only the transport asks isH2.
func (r protoRun) isMASQUE() bool { return r.kind == kindMASQUE || r.kind == kindMASQUEH2 }
func (r protoRun) isH2() bool     { return r.kind == kindMASQUEH2 }

var protoRuns = []protoRun{
	{kindWG, protoWG},
	{kindAWG, protoAWG},
	{kindAWG31, protoAWG31},
	{kindMASQUE, protoMASQUE},
	{kindMASQUEH2, protoMASQUEH2},
}

func parseProto(p string) (protoRun, error) {
	for _, r := range protoRuns {
		if r.name == p {
			return r, nil
		}
	}
	return protoRun{}, fmt.Errorf("invalid -proto %q: use wg, awg, awg31, masque or masque-h2", p)
}

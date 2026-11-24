package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime/debug"
)

type command struct {
	name   string
	brief  string
	intro  []string
	groups []flagGroup
	setup  func(*flag.FlagSet, *options)
	run    func(context.Context, options) error
}

var commands = []command{
	{
		name:  "scan",
		brief: "scan WARP endpoint pools for working, low-latency endpoints",
		intro: []string{
			"Two-phase scan:",
			"  - Phase 1 finds which WARP ports get through this network",
			"  - Phase 2 verifies each endpoint's real exit colo through a WARP tunnel",
			"",
			"Working endpoints are reported grouped per subnet. ENDPOINT PING is the ICMP",
			"ping to the endpoint address; -tun-ping adds TUN PING, measured inside the tunnel.",
			"Needs a WARP account: run \"warpscout register\" first.",
			"",
			"-proto masque scans Cloudflare's MASQUE service instead: it has no endpoint",
			"pools, so the run covers two anycast addresses on a fixed set of ports and",
			"skips phase 1 - a MASQUE dial succeeds on ports that then pass no data, so",
			"only the full tunnel check tells them apart. Those endpoints flap, hence",
			"-masque-attempts, and the SNI is what gets them past DPI, hence -masque-sni.",
		},
		groups: []flagGroup{scanGroup, protoGroup, awgGroup, awg31Group, masqueGroup, nestGroup, outputGroup},
		setup:  setupScanFlags,
		run:    runScanCmd,
	},
	{
		name:  "register",
		brief: "register a WARP account and save it",
		intro: []string{
			"Writes a WARP account to the account file - every other command needs it.",
			"When the file already holds an account, its id and token are reused to",
			"rotate the keys instead of burning a new registration; -fresh forces a",
			"brand-new account.",
			"",
			"Falls back to registering through a WARP tunnel when the Cloudflare API",
			"is unreachable directly, trying AmneziaWG then WireGuard; -proxy skips",
			"that fallback, so the AmneziaWG parameters below only shape it.",
			"",
			"A MASQUE device is registered alongside, because \"-proto masque\" needs its",
			"own one. Failing to get it only prints a warning - the rest still works.",
		},
		groups: []flagGroup{registerGroup, awgGroup, plainGroup},
		setup:  setupRegisterFlags,
		run:    runRegisterCmd,
	},
	{
		name:  "find-junk",
		brief: "search AmneziaWG junk params that unblock every endpoint",
		intro: []string{
			"Rescans with a fresh random junk set (and a fresh I1 when -gen-i1 is set)",
			"until one set brings up the -threshold share of the sampled endpoints,",
			"then prints the scan command to reuse it. Ctrl+C keeps the best set so far.",
			"",
			"Verifies by handshake and in-tunnel ping only - no exit colo is resolved.",
		},
		groups: []flagGroup{findJunkGroup, findJunkI1Group, plainGroup},
		setup:  setupFindJunkFlags,
		run:    runFindJunkCmd,
	},
	{
		name:  "find-sni",
		brief: "search a MASQUE SNI that gets through this network",
		intro: []string{
			"Rescans the MASQUE endpoints with a different SNI each round until one",
			"brings up the -threshold share of them, then prints the scan command to",
			"reuse it. The candidates are a fixed list, so the search ends by itself;",
			"Ctrl+C keeps the best SNI so far.",
			"",
			"Verifies by handshake and in-tunnel ping only - no exit colo is resolved.",
		},
		groups: []flagGroup{findSNIGroup, plainGroup},
		setup:  setupFindSNIFlags,
		run:    runFindSNICmd,
	},
	{
		name:  "socks",
		brief: "serve one endpoint as a local SOCKS5 proxy (for testing it)",
		intro: []string{
			"Brings up a single tunnel to -endpoint and hands it out as a SOCKS5 proxy on",
			"localhost, so curl, a browser or any geo-lookup tool can be pointed at that",
			"one endpoint without installing a VPN client and without root.",
			"",
			"For testing an endpoint, not for daily use: one tunnel, no reconnect, no",
			"failover - when DPI cuts it the proxy just stops working. For everyday use",
			"take \"scan -conf\" into a real client.",
			"",
			"-through works here too, and it is the only way to try a nested tunnel",
			"without building the interface chain by hand.",
		},
		groups: []flagGroup{socksGroup, protoGroup, awgGroup, awg31Group, masqueGroup, nestGroup},
		setup:  setupSocksFlags,
		run:    runSocksCmd,
	},
	{
		name:  "version",
		brief: "print the warpscout version",
		intro: []string{"Prints the version alone, so a script can compare it as is."},
		setup: func(*flag.FlagSet, *options) {},
		run:   runVersionCmd,
	},
}

// gvisor allocates per packet, so a -f scan doubles the heap between GCs and the
// OOM killer wins on a small host; measured, the peak plateaus at this limit.
const defaultMemLimit = 32 << 20

func main() {
	// The runtime default is math.MaxInt64, so this leaves a GOMEMLIMIT set
	// from the environment alone.
	if debug.SetMemoryLimit(-1) == math.MaxInt64 {
		debug.SetMemoryLimit(defaultMemLimit)
	}
	stripLoaderArg()
	enableVirtualTerminal()
	errPal = palette{enabled: colorEnabled(os.Stderr)}

	if len(os.Args) < 2 {
		rootUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "-help", "--help", "help":
		rootUsage(os.Stdout)
		os.Exit(0)
	}
	cmd := lookupCommand(os.Args[1])
	if cmd == nil {
		fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("unknown command %q", os.Args[1])))
		fmt.Fprintln(os.Stderr)
		rootUsage(os.Stderr)
		os.Exit(2)
	}

	var opts options
	fs := flag.NewFlagSet(cmd.name, flag.ExitOnError)
	fs.Usage = func() { commandUsage(fs.Output(), *cmd, fs) }
	cmd.setup(fs, &opts)
	fs.Parse(os.Args[2:])
	applyCommonFlags(fs, &opts)

	showEmoji = opts.emoji

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// "version" must stay a bare string: install.sh compares its whole output.
	if cmd.name != "version" {
		noticeUpdate(ctx)
	}

	if err := cmd.run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, errPal.fail(err.Error()))
		os.Exit(1)
	}
}

func lookupCommand(name string) *command {
	for i, c := range commands {
		if c.name == name {
			return &commands[i]
		}
	}
	return nil
}

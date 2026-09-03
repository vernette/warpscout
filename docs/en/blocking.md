# Getting past blocking

[← Back to README](../../README.md)

DPI recognises WireGuard by its handshake. AmneziaWG (`-p awg`) breaks that in two ways: it mixes junk packets into the real traffic, and it opens the connection with a made-up first packet (`I1`) pretending to be a protocol nobody blocks.

## The first packet is usually what does it

`I1` does most of the work. DPI tends to judge a connection by how it starts, so a session that opens with something resembling QUIC or DNS often gets through where the same session with different junk sizes does not. **If endpoints are blocked, change `I1` first.**

By default `I1` mimics an iCloud request. Other ones are generated with `-gen-i1`:

```sh
warpscout scan -p awg -P -gen-i1 quic
warpscout scan -p awg -P -gen-i1 dns -i1-sni example.com
```

`-gen-i1` takes `quic`, `dns`, `sip`, `stun` or `random`. Start with `quic`, it works most often. `-i1-sni` sets the hostname the fake packet mentions; without it a random well-known host is used.

You can also supply a raw packet with `-i1 PKT`, or send none at all with `-i1 none`.

## Junk packets

Three numbers control them:

| Flag        | Meaning                                    |
| ----------- | -------------------------------------------- |
| `-jc N`     | How many junk packets to send (default 6)   |
| `-jmin N`   | Smallest junk packet size (default 10)      |
| `-jmax N`   | Largest junk packet size (default 50)       |
| `-gen-junk` | Randomise all three for this run            |

The defaults work on most networks. On their own these rarely unblock anything, so always start with `-gen-i1`.

## Automatic search: find-junk

If no `-gen-i1` profile helped, the junk numbers have to be tuned to the network too. `find-junk` searches for a working set:

```sh
warpscout find-junk -gen-i1 random
```

Always add `-gen-i1`: without it the search keeps the same first packet on every attempt, and that packet is usually what decides the outcome.

The command rescans with a fresh random set until one gets at least `-threshold` percent of the sampled endpoints working (95 by default). Then it prints a ready `warpscout scan ...` command with the working settings - copy it and run. `Ctrl+C` or `q` at any point keeps the best set found so far.

![WARPSCOUT find-junk](../../.github/assets/warpscout-find-junk.png)

The command is AmneziaWG-only and verifies endpoints by handshake and ping alone, so the region and node columns stay empty.

## If AmneziaWG does not get through at all

That leaves [MASQUE](masque.md), Cloudflare's second transport. In its `-p masque-h2` form it runs over TCP and looks like ordinary HTTPS from the outside.

# Configs and filters

[← Back to README](../../README.md)

## One address on stdout

`-best` replaces the tables with a single `ip:port` line, so the command drops into a script or a pipe:

```sh
warpscout scan -p awg -best
# 188.114.98.58:2408
```

## Filters

`-node` keeps only endpoints landing on the given nodes, `-country` only those whose node sits in the given countries. Both take comma-separated lists:

```sh
warpscout scan -p awg -country DE,NL -best
warpscout scan -p awg -node HEL,ARN -best
```

`-exclude-node` and `-exclude-country` are the same in reverse. They stack with the positive ones, which is the point: keep a country while dropping one of its nodes.

```sh
warpscout scan -p awg -exclude-node DME -best
warpscout scan -p awg -country SE,DE -exclude-node ARN -best
```

If the filters leave nothing, the command fails.

## Configs

`-conf` writes a ready-to-import config for the single best endpoint of the run, in the protocol the run used:

```sh
warpscout scan -p awg -country DE -conf warp.conf
```

`-conf -` prints it to the terminal instead, to copy or pipe:

```sh
warpscout scan -p awg -conf -
warpscout scan -p awg -conf - > warp.conf
```

| Flag          | What it does                                                                   |
| ------------- | -------------------------------------------------------------------------------- |
| `-table-off`  | Keep the config from touching routes. For when you route traffic yourself         |
| `-mtu N`      | Write an `MTU` line. Without the flag it is left out and the client decides        |
| `-dns A,B`    | Replace the resolvers with your own list (default `1.1.1.1, 1.0.0.1`)              |
| `-no-dns`     | Leave the DNS line out, so the client keeps the system resolvers                   |

```sh
warpscout scan -p awg -conf warp.conf -table-off
warpscout scan -p awg -conf warp.conf -mtu 1280
warpscout scan -p awg -conf warp.conf -dns 9.9.9.9,149.112.112.112
```

### Formats

`-conf-type` picks the format:

| Value         | What you get                                                                                         |
| ------------- | ------------------------------------------------------------------------------------------------------ |
| `native`      | The default: a `.conf` for `wg`/`awg`, a `config.json` for [usque](https://github.com/Diniboy1123/usque) under MASQUE |
| `mihomo`      | A `proxies:` block for [mihomo](https://github.com/MetaCubeX/mihomo), which speaks both AmneziaWG and MASQUE |
| `mihomo-json` | The same proxies as a JSON array                                                                        |

```sh
warpscout scan -p awg -conf warp.yaml -conf-type mihomo
warpscout scan -p masque -conf warp.yaml -conf-type mihomo
warpscout scan -p awg -conf - -conf-type mihomo-json | jq '.[0]'
```

## Your own addresses

`-target` scans the given addresses instead of the built-in pools. It takes individual IPs, CIDR ranges, or a mix of both, comma-separated:

```sh
warpscout scan -p awg -target 188.114.98.58
warpscout scan -p awg -target 188.114.98.0/28
warpscout scan -p awg -target 188.114.98.58,162.159.192.0/28
```

IPv4 ranges wider than `/20` are rejected, and IPv4 cannot be mixed with IPv6 in one run.

## The speed phase

`-speed` adds the `SPEED` column - download speed measured inside the tunnel:

```sh
warpscout scan -p awg -P -speed
```

It is a separate phase after the scan, and it measures one endpoint at a time - otherwise you would be measuring your own uplink divided by the number of tunnels. Exactly the endpoints that reach the output are measured: the best in each subnet for the console table, plus the best on each node for the report file. The other rows keep a `-`, and the phase costs about three seconds per endpoint.

![WARPSCOUT speedtest phase](../../.github/assets/warpscout-speed.png)

The number is a single-stream figure; a browser speed test opens several at once and can legitimately show more. A wide spread between endpoints is the expected reading, not a symptom.

The flag on its own does not change the ordering: that still goes by loss and ping. To rank by throughput use `-best-by speed`:

```sh
warpscout scan -p awg -best -best-by speed
```

That flag runs the speed phase by itself, so the scan takes as long as with `-speed`. The set of measured endpoints is still picked by ping, so `-best-by speed` gives you the fastest **of those picks**, not of everything scanned.

## The report file

Besides the on-screen tables WARPSCOUT writes `warpscout-report-<timestamp>.txt`: a commented header, then every working endpoint, then the torn-down ones, and the best endpoint per node at the end. A flat list with no box drawing, easy to feed to `awk` and friends.

`-o FILE` changes the name, `-no-report` skips the file. `-best` and `-conf -` suppress it on their own, and `-o` forces it back on.

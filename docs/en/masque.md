# MASQUE

[← Back to README](../../README.md)

Cloudflare serves WARP over MASQUE as well as WireGuard. It is a separate transport with its own addresses, and it comes in two forms:

- `-p masque` - over QUIC (UDP)
- `-p masque-h2` - the same thing over TCP, which looks like ordinary HTTPS from the outside

```sh
warpscout scan -p masque
warpscout scan -p masque-h2 -masque-sni www.apple.com
```

On a filtering network `masque-h2` is the interesting one: it has real address pools, and TCP is filtered less often.

## How MASQUE differs from WireGuard

- **Its own addresses.** The `masque` pool is tiny and fixed: two anycast addresses per block (`162.159.198.1` and `.2` over IPv4, `2606:4700:103::1`, `::2` and `2606:4700:104::1`, `::2` over IPv6) on a fixed set of ports, so `-n`/`-f` have nothing to sample. With `masque-h2` the whole of `162.159.198.0/24` and `162.159.199.0/24` answers, and under `-6` so do `2606:4700:103::/48` and `2606:4700:104::/48`, so there `-n`/`-f` work as usual.
- **One node per run.** Every endpoint of a run exits through the same node, and which node that is depends on your network, not on the address you picked. That is why `-node` and `-country` are rejected under MASQUE.
- **Endpoints flap**, so each is checked at least 3 times. `-masque-attempts N` changes that.
- **Its own device.** `register` creates a MASQUE account alongside the WireGuard/AmneziaWG one, because one device cannot be both. The same account serves `masque` and `masque-h2`.

## SNI

MASQUE has neither junk packets nor `I1`. Its equivalent is the SNI - the hostname visible from the outside while the connection is set up:

```sh
warpscout scan -p masque -masque-sni www.apple.com
```

The default is `consumer-masque.cloudflareclient.com`, and on a filtering network it often gets nothing through. `find-sni` searches for one that does:

```sh
warpscout find-sni
```

It walks a list of names and prints a ready scan command with the best one. The search stops as soon as an SNI gets `-threshold` percent of endpoints working (70 by default); `Ctrl+C` keeps the best one found.

An SNI does not carry over between the two transports - one that works over QUIC can be dead over TCP - so `-p` picks which one to search for:

```sh
warpscout find-sni -p masque-h2
# warpscout scan -proto masque-h2 -masque-sni www.apple.com
```

## Configs

`-conf` writes a `config.json` for [usque](https://github.com/Diniboy1123/usque) instead of a `.conf`, and prints the command to run it:

```sh
warpscout scan -p masque -conf usque.json
# usque socks -c usque.json -P 8443 -s www.apple.com

warpscout scan -p masque-h2 -conf usque.json
# usque socks -c usque.json -P 1701 -s www.apple.com --http2
```

`-conf-type mihomo` works for both modes: the TCP variant is the same `type: masque` with `network: h2`.

`-table-off`, `-mtu` and `-dns`/`-no-dns` have no counterpart in usque's `config.json` and are ignored here. Under `-conf-type mihomo` the DNS flags work as usual.

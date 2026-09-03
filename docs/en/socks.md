# SOCKS5 proxy

[← Back to README](../../README.md)

> [!WARNING]
> This command is for testing an endpoint, not for everyday use. `socks` brings up one tunnel and holds it: there is no reconnect and no failover, so once the tunnel drops the proxy stops working. It will not survive a network change either

Everything a scan shows comes from Cloudflare's own answer. `socks` lets you check an endpoint with something else: it connects to one address and serves it as a SOCKS5 proxy on localhost, so `curl`, a browser or a script like [ipregion](https://github.com/vernette/ipregion) (region detection) and [censorcheck](https://github.com/vernette/censorcheck) (blocking checks) go through exactly that endpoint - with no VPN client to install and no admin rights.

```sh
warpscout socks -e 188.114.99.218:2408 -p awg
```

```
For testing purposes ONLY.
One tunnel, no reconnect, no failover. For everyday use take a "scan -conf" config into a real client.

╭───────────────────────────────────╮
│ SOCKS5   socks5h://127.0.0.1:1080 │
│ Endpoint 188.114.99.218:2408      │
│ Tunnel   awg                      │
│ Exit     Moscow, RU (DME node)    │
╰───────────────────────────────────╯

Exit is what speed.cloudflare.com reports. Confirm it elsewhere:
  curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json

Point clients at socks5h://, not socks5:// - the name has to be resolved in the tunnel.

Ctrl+C to stop the proxy
```

`Exit` is Cloudflare's own opinion of where the tunnel comes out. The proxy lets you point a third-party service at it and see whether they agree.

`-e/-endpoint` takes exactly what `scan -best` prints, so the two commands pipe together:

```sh
warpscout socks -e "$(warpscout scan -p awg -best)" -p awg
```

`-P/-port` changes the port the proxy listens on (1080 by default), `-l/-listen` the address (`127.0.0.1`). The protocol and obfuscation flags are the scan's: `-p wg|awg|masque|masque-h2`, `-gen-i1`, `-masque-sni` and the rest.

```sh
warpscout socks -e 162.159.198.1:443 -p masque -masque-sni www.apple.com -port 9050
```

> [!IMPORTANT]
> Point clients at `socks5h://`, not `socks5://`: the hostname has to be resolved on the tunnel side, not the client side. Otherwise some services will not open at all

```sh
curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json
```

`-through` works here too, and it is the only way to try a [nested tunnel](warp-in-warp.md) without wiring up interfaces by hand:

```sh
warpscout socks -e 8.47.69.130:2408 -p awg -through 188.114.97.177:2408
# Tunnel   wg through 188.114.97.177:2408 (awg)
# Exit     Frankfurt-am-Main, DE (FRA node)
curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json
# "ip": "104.28.197.9", "country": "Germany", "city": "Frankfurt am Main"
```

`Ctrl+C` stops the proxy and the tunnel.

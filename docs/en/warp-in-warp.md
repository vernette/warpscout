# WARP-in-WARP

[← Back to README](../../README.md)

`-through` runs the whole scan from inside another WARP tunnel. The inner tunnel leaves Cloudflare's network where the outer one does, so `SEEN AS` shows the region of the outer endpoint's node instead of yours.

## Building the chain

1. Find an endpoint on a foreign node with a normal scan. If any node outside your own country will do:

   ```sh
   warpscout scan -p awg -exclude-country RU -best
   ```

   If you want a specific one:

   ```sh
   warpscout scan -p awg -node FRA -best
   # 188.114.97.177:2408
   ```

2. Scan through it:

   ```sh
   warpscout scan -p awg -through 188.114.97.177:2408
   ```

Every endpoint of the second run exits in that node's country. The same network, scanned directly as `RU`, through an endpoint on `FRA`:

![WARPSCOUT scan through another WARP tunnel](../../.github/assets/warpscout-through.png)

## Which protocol goes where

`-p` means what it means everywhere else: the protocol of the tunnel that crosses your network. Under `-through` that is the outer tunnel - the only one DPI can see at all:

```
[host] --( -proto: crosses your network, this is what DPI sees )--> outer endpoint --( -inner-proto: inside the outer tunnel, invisible )--> the endpoints being scanned
```

If a normal scan on this network needs `-p awg`, so does a nested one. The only new flag here is `-inner-proto`, and it defaults to `wg`: inside the outer tunnel obfuscation buys nothing and only eats MTU.

## Things to know

- `register` creates a second WireGuard device for the outer tunnel: WARP will not nest two tunnels sharing a private key. If your account file was created by a version older than `0.12.0`, run `warpscout register` again.
- The scan drops to one tunnel at a time (`-jt 1`): the inner tunnels share a single outer device, and even at `-jt 3` Cloudflare starts returning false negatives. That is what makes a nested scan noticeably slower.
- MASQUE takes no part in the chain on either side: it gives no way to pick a node, and picking the node for the outer tunnel is the whole point.
- `ENDPOINT PING` under `-through` is measured along `[host] → [outer endpoint] → [inner endpoint]`, so the hop to the outer endpoint is included. `-best` can still rank by it.

## A config for the chain

`-conf` on such a run writes both tunnels.

```sh
warpscout scan -p awg -through 188.114.97.177:2408 -conf warp.yaml -conf-type mihomo
```

Under `-conf-type mihomo` the result is self-contained: two proxies, the inner one carrying `dialer-proxy`, and mihomo builds the chain itself. A plain `.conf` cannot express a chain, so there you get two interfaces one after the other - split them into two files and wire them up in your client, as the header comment says.

To try a chain without wiring interfaces by hand, use [`socks`](socks.md) - it takes `-through` too.

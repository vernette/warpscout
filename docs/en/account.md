# The WARP account

[← Back to README](../../README.md)

```sh
warpscout register
```

The command writes `warpscout-account.json` into the current directory. Without that file `scan`, `find-junk`, `find-sni` and `socks` refuse to run. `-a/-account FILE` puts it somewhere else, and the same flag tells the other commands where to look.

## What registration does

WARPSCOUT does what the official WARP client does on first launch: it registers a new device with Cloudflare and gets a WireGuard peer for it.

1. A fresh X25519 key pair is generated locally. The private half never leaves your machine.
2. The public half goes to `https://api.cloudflareclient.com/v0a4005/reg` as an Android client. The answer carries an account `id`, a bearer token and the WARP peer public key.
3. A second request enables WARP for that account (`warp_enabled: true`).
4. The result is written to the account file.

```json
{
  "id": "...",
  "token": "...",
  "private_key": "...",
  "peer_public_key": "...",
  "ipv4": "172.16.0.2",
  "ipv6": "2606:4700:110:...",
  "masque": { "id": "...", "token": "...", "private_key": "...", "peer_public_key": "..." },
  "outer": { "id": "...", "token": "...", "private_key": "...", "peer_public_key": "..." }
}
```

| Field             | What it is                                                                                                                         |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `id`              | The account Cloudflare created. Addresses every later request                                                                       |
| `token`           | The bearer token authorising those requests. Both are secrets                                                                       |
| `private_key`     | Your side of the tunnel. The same key goes into `-conf` configs                                                                     |
| `peer_public_key` | The WARP peer public key - shared by every endpoint, which is why one account covers them all                                        |
| `ipv4`            | The address inside the WARP tunnel. The same for everyone, NAT-ed on the way out                                                    |
| `ipv6`            | Your own routed v6 address. Traffic from someone else's is dropped                                                                  |
| `masque`          | A second device registered as [MASQUE](masque.md) - one device cannot be both                                                       |
| `outer`           | A third device for the outer tunnel of [WARP-in-WARP](warp-in-warp.md)                                                              |

`masque` and `outer` are created best-effort: if Cloudflare refuses one of them, `register` warns and the rest of the file keeps working.

## Running register again

On a second run the `id` and `token` are taken from the file and only the keys change: a new pair is generated and sent with a `PATCH`, so you get a new `private_key` without registering again.

A brand-new account is minted in two cases: with `-fresh`, which ignores the file entirely, and when the rotation fails (a revoked token, an account deleted on Cloudflare's side).

## When the API is unreachable

On a filtering network the registration requests never leave at all. WARPSCOUT works around that in a chain.

1. **Directly.** It checks whether `api.cloudflareclient.com` answers.
2. **Through a relay.** A small reverse proxy DPI usually leaves alone. The default address is built in, `-relay URL` points at your own, `-relay none` skips the step. To run your own relay see [nellimonix/base-relay](https://github.com/nellimonix/base-relay).
3. **Through a WARP tunnel.** Endpoint addresses are swept, a tunnel is brought up to the first one that handshakes, and the same registration requests go through it. AmneziaWG is tried first, then plain WireGuard, and for AmneziaWG several different first packets (`I1`) are tried as well.

With a proxy, `-x/-proxy` sends the registration through it and none of the fallbacks are used:

```sh
warpscout register -x socks5://127.0.0.1:1080
```

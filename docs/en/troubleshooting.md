# Troubleshooting

[← Back to README](../../README.md)

## `ENDPOINT PING` shows `?`

That column is a ping to the endpoint address, and it needs an ICMP socket the current user is allowed to open. `TUN PING` is never affected, and ranking with `-P` keeps working - the column simply shows `?`.

Most systems already allow it, so check first:

```sh
cat /proc/sys/net/ipv4/ping_group_range   # "0 2147483647" = allowed, "1 0" = closed
```

If it is closed, either give the binary the capability or open the range system-wide:

```sh
sudo setcap cap_net_raw+ep ./warpscout

# or
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

For Docker see [its own page](docker.md).

## The tool gets killed on a small router

That is the kernel OOM killer, which is why the shell reports a bare `SIGKILL` and the tool itself prints nothing. `dmesg`, or `logread` on OpenWrt, confirms it:

```
Out of memory: Killed process 4650 (warpscout) total-vm:1570964kB, anon-rss:176768kB
```

WARPSCOUT caps the Go runtime's memory at `32MiB` by default - enough even for a full `-f` scan, so this should not happen any more. If it still does, lower the cap with `GOMEMLIMIT`:

```sh
GOMEMLIMIT=8MiB warpscout scan -p awg -gen-i1 quic -f
```

`8MiB` is a deliberate floor. Raising the cap buys nothing: the scan takes the same time either way.

## macOS refuses to run the binary

The file came from the internet - clear the quarantine flag once:

```sh
xattr -d com.apple.quarantine warpscout
```

## Everything fails with `-p wg`

On a filtering network that is normal. Use `-p awg`, and if that does not work either, walk the list in [getting past blocking](blocking.md).

## The update notice will not go away

Every command except `version` checks for a new release and prints a notice. The answer is cached for 6 hours in `$TMPDIR/warpscout-latest-version-<uid>` - that is `/tmp` on Linux, `$PREFIX/tmp` in Termux, a per-user `/var/folders/…/T/` on macOS and `%TEMP%` on Windows. Delete the file to force a fresh check.

## The live dashboard breaks or does not render

Add `-plain` for plain line output instead of the live dashboard. It is also what you want when the output goes into a file or another program.

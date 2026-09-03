# How it works

[← Back to README](../../README.md)

## The two scan phases

**Phase 1 - which ports your network lets through.** WARP endpoints listen on several UDP ports and stay silent to everything except a valid WireGuard handshake, so a completed handshake is the only reliable reachability test. WARPSCOUT takes a few addresses and finds which ports get through. The common ones are tried first, and only if none answer does it sweep the rest.

**Phase 2 - where each endpoint exits.** For every address a real tunnel is brought up and `https://speed.cloudflare.com/meta` is fetched through it. That single answer carries everything: the exit country (`SEEN AS`), the edge node (`NODE`) and where it sits (`NODE LOCATION`). The pings are measured here too, and with `-P` so are the in-tunnel latency and loss.

Both phases are shown live:

![WARPSCOUT scan phases](../../.github/assets/warpscout-phases.png)

## Exit region and node are not the same thing

`SEEN AS` is the country websites will place you in. `NODE` is the Cloudflare node the traffic goes through. A tunnel can run through Frankfurt and still exit as Russia: the region is tied to your account and the address the connection came from, while the node is tied to the endpoint you picked.

`NODE` matters for latency, and for what filtering the traffic meets on the way out: a node inside a censoring country can throttle or block what the same account carries fine through a node abroad.

A single `/24` can hand out several different nodes, and even neighbouring addresses can differ. A subnet is not a location - the node always comes from that endpoint's own answer.

## Ports

Phase 2 walks the port list for each endpoint and keeps the first that answers, so different endpoints may end up on different ports.

- `-port N` pins one port for the whole run and skips phase 1.
- `-sweep-ports open` reports every port of an endpoint as its own row instead of the first that answers: the same address on a different port can land on a different node with a different latency.
- `-sweep-ports all` sweeps every known WARP port and skips phase 1. Slow - meant to be used with `-target`.

## `torn down` endpoints

An endpoint that completed a handshake, passed data for a while and was then cut off for good lands in a separate `torn down` list instead of the working ones. That is what DPI does to a tunnel it does not like.

This is why the check sends a burst of 10 echoes rather than two or three: a shorter burst will not see the cut. And it looks at the **tail** of consecutive lost packets, not at the loss percentage - so an endpoint dropping the odd packet stays working with its `LOSS` shown, while a dead one goes to `torn down`.

It is a property of the network more than of the endpoint. Where WARP is not filtered at all, plain `-p wg` works. On a filtering network a WireGuard scan may show a couple of working subnets, each of which dies right after the first handshake - that is when you need `-p awg` and [obfuscation](blocking.md).

## Checking a server's region

The other common case is a VPS. WARP decides your region from the address the connection comes from, and the GeoIP databases behind that are often wrong: a machine physically in the Netherlands can be listed as Indian, and every site the tunnel reaches will treat it that way.

A scan on the server answers this immediately, in the `SEEN AS` column. No need to bring WARP up and find out afterwards that half your services moved to another country.

How detailed the result is depends on where the server sits. At most European providers every address lands on one node with one region, and the scan fits on a screen:

![WARPSCOUT single node](../../.github/assets/warpscout-single-node.png)

Russian providers are less predictable: one machine gives `ARN`, another `HEL`, and some hand out four locations or more across the pools:

![WARPSCOUT multi-node](../../.github/assets/warpscout-multi-node2.png)

## The memory limit

WARPSCOUT caps the Go runtime's memory at `32MiB` - enough even for a full `-f` scan, and small enough that the kernel OOM killer leaves it alone on a router. `GOMEMLIMIT` overrides the default either way, see [troubleshooting](troubleshooting.md).

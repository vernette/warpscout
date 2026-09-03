# Docker

[← Back to README](../../README.md)

The image is multi-arch (`linux/amd64` and `linux/arm64`). The container's working directory is `/data`, and the account file goes there.

```sh
# Register a WARP account
docker run --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/data" vernette/warpscout register

# Scan over AmneziaWG
docker run --pull always --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/data" vernette/warpscout scan -p awg
```

## Keeping the account between runs

Mount a directory (`-v "$PWD:/data"`), otherwise the account dies with the container and you register every time.

> [!NOTE]
> The container runs as root, so without `--user` everything written to the mounted directory - the account file, the report, a `-conf` config - belongs to root rather than to you. `$(id -u)` is Linux and macOS shell syntax. On Docker Desktop for Windows the flag is unnecessary: the filesystem driver maps ownership itself

## Colour and the live dashboard

Both only turn on when the output goes to a terminal, hence `-it`: `-t` allocates a pseudo-terminal, `-i` attaches stdin. Without `-i` nobody reads the terminal's replies to the dashboard's queries, and they leak into the shell as raw characters.

## Ping inside a container

On a current Docker there is nothing to do. If `ENDPOINT PING` shows `?` (an old Docker, hardened defaults, a different container engine), add the sysctl:

```sh
docker run --rm -it --sysctl net.ipv4.ping_group_range="0 2147483647" \
  -v "$PWD:/data" vernette/warpscout scan -p awg
```

`TUN PING` runs inside the tunnel and needs no privileges in any container.

## IPv6 and interface selection

`-6` and `-I` need the host network. A container has its own network namespace, the host's interfaces are not there, and IPv6 is usually off:

```sh
docker run --rm -it --network host -v "$PWD:/data" vernette/warpscout scan -p awg -6
docker run --rm -it --network host -v "$PWD:/data" vernette/warpscout scan -p awg -I eth0
```

## Building the image

```sh
# for your own system
docker build -t warpscout .

# for another platform
docker buildx build --platform linux/arm64 -t vernette/warpscout:arm --load .
```

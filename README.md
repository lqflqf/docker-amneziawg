# Docker AmneziaWG

[![Docker Build](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml/badge.svg)](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml)
[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-docker--amneziawg-blue?logo=docker)](https://github.com/lqflqf/docker-amneziawg/pkgs/container/docker-amneziawg)
[![GitHub release](https://img.shields.io/github/v/release/lqflqf/docker-amneziawg)](https://github.com/lqflqf/docker-amneziawg/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[AmneziaWG](https://docs.amnezia.org/) VPN server and client in one container. AmneziaWG is WireGuard with traffic obfuscation, which makes the handshake harder for deep packet inspection to recognize. In server mode the container writes the server config, gives you a config and QR code for every peer, and answers DNS for connected clients. It is built on [LinuxServer.io](https://www.linuxserver.io/) base images with s6-overlay.

> Forked from [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg), created and maintained by [Andrey Yastrebov](https://github.com/AYastrebov). The container's design, its config generation and most of its AWG work are his. Many thanks to him for building it and releasing it under the MIT license. The main change here is the DNS resolver; see [Differences from the original](#differences-from-the-original).

## Quick start

```yaml
services:
  amneziawg:
    image: ghcr.io/lqflqf/docker-amneziawg:latest
    container_name: amneziawg
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=Etc/UTC
      - SERVERURL=vpn.example.com   # or auto
      - PEERS=laptop,phone,tablet   # or a number
    volumes:
      - ./config:/config
    ports:
      - 51820:51820/udp
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv4.conf.all.src_valid_mark=1
    restart: unless-stopped
```

```bash
docker compose up -d
docker exec amneziawg /app/show-peer laptop   # QR code for the Amnezia app
```

Each peer's config is at `./config/peer_laptop/peer_laptop.conf` for named peers, or at `./config/peer1/peer1.conf` when `PEERS` is a number. [`docker-compose.yml`](docker-compose.yml) lists every option, with comments.

**Requirements:** a Docker host with `/dev/net/tun` and the `NET_ADMIN` capability, on amd64 or arm64. No kernel module is needed: the container falls back to the bundled userspace `amneziawg-go`. If the [AmneziaWG kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) is loaded on the host, the container detects it and uses it. `SYS_MODULE` does not change this, because the container never calls `modprobe`. If you use `RandomTrailers` with the kernel module, use a module built from `4569c4c6` (2026-09-06) or newer ([details](docs/awg-performance.md#checking-whether-your-module-has-the-fix)).

## Modes

- **Server mode (`PEERS` set):** generates keys, `wg0.conf`, and one config plus QR code per peer, and starts Unbound so that peers on `PEERDNS=auto` have a resolver.
- **Client mode (`PEERS` unset):** brings up every `.conf` in `./config/wg_confs/`. Nothing is generated, and Unbound stays off unless `USE_DNS=true`.

In either mode, if no tunnel comes up the container removes every IPv4 and IPv6 default route, so nothing leaks outside the VPN, and it reports itself `unhealthy`.

## Differences from the original

The main change from [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg) is the DNS resolver for peers: **Unbound instead of CoreDNS**.

| | Original (CoreDNS) | This fork (Unbound) |
|---|---|---|
| Where peer queries go | The host's resolver | Cloudflare `1.1.1.1` / `1.0.0.1`, or any upstream you set |
| Encryption to the upstream | None | DNS-over-TLS (port 853) |
| DNSSEC validation | No | Yes |
| On/off switch | `USE_COREDNS` | `USE_DNS` |
| Config file | `/config/coredns/Corefile` | `/config/unbound/unbound.conf` |

With CoreDNS, peer queries left the server in plain text, so the VPS provider could read or change them. The trade-offs of Unbound: lookups go to Cloudflare by default, and the server needs outbound TCP 853.

**Switching from the original image:** change `image:`, and rename `USE_COREDNS` to `USE_DNS` if you set it (`USE_COREDNS` is ignored). The `/config` volume is reused as is, and peer configs don't change. `/config/coredns/` can be deleted.

This fork also adds:
- a health check
- transactional config regeneration
- protocol-version migration
- archiving of removed peers
- hardened secrets and DNS defaults
- per-build image tags (`<amneziawg-tools>-r<N>`, for example `3.1.20260812-r2`)

See the [changelog](CHANGELOG.md).

## DNS (Unbound)

With `PEERDNS=auto` (the default), each peer config gets `DNS = <subnet>.1`, the server end of the tunnel. The default `/config/unbound/unbound.conf`:
- forwards queries to Cloudflare over DNS-over-TLS
- validates DNSSEC
- drops root privileges after start-up

Where it listens is generated at every start: on `127.0.0.1` and the tunnel address only, answering only the VPN subnet. Do not publish port 53.

The file is copied only when it is missing, so your edits survive updates. Delete it to get the current default back. Configs created by older images keep their own `interface`/`access-control` lines, and the log mentions this. To use another upstream, replace the `forward-addr` lines and keep the `#hostname` suffix, for example `forward-addr: 9.9.9.9#dns.quad9.net`.

Unbound does not run when `USE_DNS=false`, in client mode (unless `USE_DNS=true`), or when something already listens on port 53. In those cases, set `PEERDNS` to a resolver such as `1.1.1.1`. An invalid config is not started and makes the container `unhealthy`; the tunnel still comes up.

## Parameters

| Parameter | Function |
|-----------|----------|
| `-e PUID` / `-e PGID` / `-e TZ` | File ownership and timezone (LinuxServer standard) |
| `-e SERVERURL=auto` | Host or IP written into peer configs. `auto` detects the public IPv4 over HTTPS (and keeps the previous value if detection fails) |
| `-e SERVERPORT=51820` | Port advertised to peers. The container always listens on 51820, so map `SERVERPORT:51820/udp` (**not** `SERVERPORT:SERVERPORT`) |
| `-e PEERS=` | Number or comma-separated alphanumeric names. Enables server mode |
| `-e PEERDNS=auto` | DNS for peers. `auto` means the container's Unbound at `<subnet>.1` |
| `-e INTERNAL_SUBNET=10.13.13.0` | VPN subnet (`.1` is the server, `.2` and up are peers) |
| `-e ALLOWEDIPS=0.0.0.0/0, ::/0` | What peers route into the tunnel. The tunnel is IPv4-only; `::/0` sinks peer IPv6 to prevent leaks. Narrow it for split tunnelling |
| `-e PERSISTENTKEEPALIVE_PEERS=` | `all`, or comma-separated peers that get `PersistentKeepalive = 25` |
| `-e SERVER_ALLOWEDIPS_PEER_<peer>=` | Extra server-side AllowedIPs for one peer (site-to-site) |
| `-e LOG_CONFS=false` | `true` prints each peer's QR code to the log. QR codes contain private keys; `show-peer` is the safer way |
| `-e USE_DNS=` | Force Unbound on or off. Defaults to on in server mode and off in client mode |
| `-e HEALTHCHECK_DNS_NAME=` | If set, the health check also requires Unbound to resolve this name, which proves the upstream is reachable |
| `-e AWG_VERSION=2.0` | Protocol version, see below |
| `-e AWG_*` | Obfuscation parameters, see below. All are random by default |

## Protocol versions and obfuscation

| `AWG_VERSION` | What you get |
|---|---|
| `2.0` (default) | Full DPI evasion: S1-S4 padding, H1-H4 ranges, and an I1 signature (a QUIC Initial). Needs AmneziaVPN 4.8.12.9+ |
| `3.0` | 2.0 plus `HeaderProtectionKey`, content padding and randomized timers. Needs 3.0-capable software on every end |
| `3.1` | 3.0 plus `RandomTrailers = on`. Needs 3.1-capable software on every end |
| `1.5` | Legacy: integer H values, `S3 = S4 = 0`, no I1-I5 |

Every value is generated on the first start and saved to `/config/server/awg_params`, so restarts keep peers working. A value you set in the environment always wins. When `AWG_VERSION` changes, the saved version-specific values (S, H, I and the 3.x parameters) are regenerated for the new version, and every peer needs its new config. A value that `awg` would reject, such as an S value below 12 under 3.x, stops generation and leaves the previous configs in place.

| Parameter | Default | Constraints |
|-----------|---------|-------------|
| `AWG_JC` / `AWG_JMIN` / `AWG_JMAX` | 3-8 / 40-80 / 80-250 | Junk packets. `JMIN < JMAX ≤ 1280` |
| `AWG_S1` / `AWG_S2` | 15-150 | Handshake padding. `S1 ≤ 1132`, `S2 ≤ 1188`, `S1 + 56 ≠ S2` |
| `AWG_S3` / `AWG_S4` | 8-55 / 4-20 (2.0); 12-55 / 12-20 (3.x); 0 (1.5) | Cookie / transport padding. `S3 ≤ 64`, `S4 ≤ 32`; keep `S4 ≤ 20` (it is paid on every packet) |
| `AWG_H1`-`AWG_H4` | Non-overlapping ranges (2.0+), integers (1.5) | Unique, all ≥ 5. Integers make the Amnezia app report 1.5 |
| `AWG_I1`-`AWG_I5` | I1 = QUIC Initial (2.0+) | Signature packets in [tag syntax](#signature-packets-i1-i5) |
| `AWG_HEADER_PROTECTION_KEY` | Generated (3.x) | Must be identical everywhere; needs S1-S4 ≥ 12 |
| `AWG_CONTENT_PADDING` | `lo-hi` within 16-128, or 0 with trailers (3.x) | `0` is recommended: padding costs about 22% of download speed |
| `AWG_REKEY_AFTER_TIME`, `AWG_REKEY_TIMEOUT`, `AWG_REJECT_AFTER_TIME`, `AWG_KEEPALIVE_TIMEOUT`, `AWG_MAX_HANDSHAKE_ATTEMPTS` | Random `lo-hi` ranges (3.x) | Each end draws its own value, so these do not have to match |
| `AWG_RANDOM_TRAILERS` | `on` under 3.1, otherwise unset | `on`/`off`, works with any version. Must match on every end, and needs `S1 = S2 = S3 = S4` (drawn that way automatically) |
| `AWG_DISABLE_COOKIES` | unset | `on`/`off`, works with any version. Gives up WireGuard's DoS mitigation; does not have to match |

The server and all clients must use the same values, except for the 3.x timers. Notes on the 3.1 switches:
- Only `on` writes a key. `off` omits the key, and it is how you turn a switch back off. Removing the variable instead means "reuse the saved value".
- A `RandomTrailers` that came only from the `3.1` preset is dropped when you go back to `2.0`.
- A host kernel module older than 3.1 rejects both keys (`Unable to modify interface: Invalid argument`). The container warns about this at start-up when it can read the module version.

### Signature packets (I1-I5)

Tags: `<b 0xHEX>` static bytes, `<r N>` random bytes, `<rd N>` random digits, `<rc N>` random letters, `<t>` a timestamp. Some third-party parsers, such as Keenetic, reject a single tag larger than 1000 bytes. For those, write the default's trailing `<r 1178>` as `<r 1000><r 178>`; it is identical on the wire. [AmneziaWG Architect](https://architect.vai-rice.space/) generates signatures that imitate QUIC, DNS, DTLS, SIP and other protocols.

## Performance and MTU

Most obfuscation only affects handshakes. `S4`, `HeaderProtectionKey`, `ContentPaddingAddition` and `RandomTrailers` cost something on every packet. [docs/awg-performance.md](docs/awg-performance.md) has measurements for each.

`awg-quick` sets the tunnel MTU to 1420 without accounting for `S4`, so full-size packets fragment when `S4 > 20` or when the endpoint is IPv6. Tunnels then connect but crawl. Add `MTU = 1280` to `[Interface]` for mobile, PPPoE and unknown paths. On a path that is itself limited to 1280 bytes, use `path − 60 − S4` (IPv4) or `path − 80 − S4` (IPv6). To apply it to future configs, put the line in `/config/templates/server.conf` and `peer.conf`. [docs/mtu.md](docs/mtu.md) explains the numbers.

## Managing peers

```bash
docker exec amneziawg /app/show-peer 1 2 3
docker exec amneziawg /app/show-peer laptop phone
```

To add a peer, add it to `PEERS` and restart; existing peers keep their keys and addresses. When you remove a peer from `PEERS`, it disappears from `wg0.conf` and its directory moves to `/config/removed_peers/<peer>-<timestamp>/`, which frees its address. Adding the same name again creates a new identity.

Configs are regenerated when a server-side variable, an `AWG_*` value, a `SERVER_ALLOWEDIPS_PEER_*` value or a template changes. Every generated config is checked with `awg`'s own parser, and the new set is installed all-or-nothing. If anything fails, such as a broken template, a duplicate peer or an invalid `SERVERURL`, no file changes and the log shows `Config generation failed` with the reason.

## Health check and troubleshooting

The container is `healthy` only while every tunnel in `/config/wg_confs` is up and, when Unbound is enabled, Unbound answers. A tunnel is retried twice before the container gives up. Docker does not restart unhealthy containers by itself, so use the status for monitoring or with a tool such as autoheal.

```bash
docker logs amneziawg
docker exec amneziawg /app/healthcheck        # healthy/unhealthy, and why
docker exec amneziawg awg show
docker exec amneziawg cat /build_version      # bundled versions and commits
```

| Symptom | Fix |
|---|---|
| Custom `SERVERPORT` unreachable | Map `SERVERPORT:51820/udp` |
| Amnezia app shows AWG 1.5 | H1-H4 are integers. Use the 2.0 default ranges |
| Connection fails after a parameter change | Give the peers their regenerated configs |
| Peers get no DNS | Unbound is off (`USE_DNS=false`, or port 53 is taken). Set `PEERDNS=1.1.1.1` |
| Connects, pings fine, downloads crawl | Fragmentation. See [Performance and MTU](#performance-and-mtu) |

## Security

- Keys, configs, QR codes and `awg_params` are mode `600`.
- **Treat write access to `/config` as root access.** `PostUp` lines and the templates in `/config/templates/` (shell heredocs) run as root. World-writable templates are refused.
- See [SECURITY.md](SECURITY.md) to report a vulnerability.

## Building locally

```bash
docker build -t amneziawg .
docker buildx build --platform linux/amd64,linux/arm64 -t amneziawg .
.github/scripts/smoke-test.sh amneziawg   # the CI smoke tests
```

## Links

- [AmneziaVPN documentation](https://docs.amnezia.org/) · [kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) · [AmneziaWG Architect](https://architect.vai-rice.space/)
- [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard), the project this one is modeled on
- [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md)

## Credits

This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg) by [Andrey Yastrebov](https://github.com/AYastrebov), who created it and wrote most of its history. It builds on [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard) and on the [AmneziaVPN](https://github.com/amnezia-vpn) team's `amneziawg-go`, `amneziawg-tools` and kernel module.

## License

MIT, see [LICENSE](LICENSE). The original copyright notice is kept, as the license requires.

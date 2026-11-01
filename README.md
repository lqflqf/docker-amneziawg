# Docker AmneziaWG

[![Docker Build](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml/badge.svg)](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml)
[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-docker--amneziawg-blue?logo=docker)](https://github.com/lqflqf/docker-amneziawg/pkgs/container/docker-amneziawg)
[![GitHub release](https://img.shields.io/github/v/release/lqflqf/docker-amneziawg)](https://github.com/lqflqf/docker-amneziawg/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[AmneziaWG](https://docs.amnezia.org/) VPN server and client in one container. AmneziaWG is WireGuard with traffic obfuscation, which makes the handshake difficult for deep packet inspection to identify. In server mode, the container writes the server config, makes a config and QR code for each peer, and answers DNS for the peers. The image is Alpine Linux with [s6-overlay](https://github.com/just-containers/s6-overlay), for amd64 and arm64.

> This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg). See [Credits](#credits) and [Upgrading](#upgrading).

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
      - SERVERURL=vpn.example.com   # recommended. Leave it out to detect the public IPv4
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

The peer configs are in `./config/peer_laptop/peer_laptop.conf`, or `./config/peer1/peer1.conf` if `PEERS` is a number. [`docker-compose.yml`](docker-compose.yml) lists each option with comments, and has a client example.

### Requirements

- A Docker or containerd host (see [containerd and nerdctl](#containerd-and-nerdctl-alpine)) with `/dev/net/tun` and the `NET_ADMIN` capability. A [kernel module](#kernel-module) is optional.
- **Do not use host networking** (`network_mode: host`). The container changes the network namespace that it runs in, and with host networking that namespace is the host. If no tunnel starts, the host loses its default routes. The `PostUp` lines also add `iptables` rules to the host. Use the default bridge network and publish `51820/udp`.

## Modes

- **Server mode (`PEERS` set):** the container makes the keys, `wg0.conf` and a config with a QR code for each peer. It runs a [DNS forwarder](#dns-dnsmasq) on the server tunnel address.
- **Client mode (`PEERS` unset):** the container starts each `.conf` in `./config/wg_confs/`. The file name is the interface name. The `DNS` line of a conf becomes the container resolver. A full tunnel (`AllowedIPs = 0.0.0.0/0`) needs the `net.ipv4.conf.all.src_valid_mark=1` sysctl.

In both modes, if no tunnel starts, the container removes each IPv4 and IPv6 default route and reports `unhealthy`. Thus no traffic leaves outside the VPN.

## Kernel module

The bundled userspace `amneziawg-go` works without a kernel module. For better throughput, install the [AmneziaWG kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) on the host. The container detects it at start-up and logs `AmneziaWG kernel module is active` or `using userspace amneziawg-go`.

- The container never calls `modprobe`, so it does not need `SYS_MODULE`. Keep `SYS_MODULE` only on hosts that do not load the iptables NAT modules automatically.
- The kernel datapath applies only the options that the module knows. A module older than 3.1 rejects `RandomTrailers` and `DisableCookies` with `Unable to modify interface: Invalid argument`. The container gives a warning if it can read `/sys/module/amneziawg/version`.
- With `RandomTrailers`, use a module built from upstream `4569c4c6` (2026-09-06) or newer. Older 3.1 modules send some oversized handshake packets. The version string does not show the fix. See [how to check](docs/awg-performance.md#checking-whether-your-module-has-the-fix).

## Parameters

| Parameter | Function |
|-----------|----------|
| `PUID=911` / `PGID=911` / `TZ` | Owner of `/config`, and the user that runs dnsmasq. Use a number from 1 to 4294967294. A value that is not valid, or `0` (root), stops the container with exit code 1 before it changes a config |
| `SERVERURL` | Host or IP in the peer configs. **Set it** to a DNS name or a fixed IP. If it is not set, the container asks 3 HTTPS services for the public IPv4 (5 s maximum). If they fail, it keeps the previous value. A changed address re-issues each peer config. The container gives a warning for `auto` and uses it as "not set" |
| `SERVERPORT=51820` | Port in the peer configs, 1-65535 without a leading zero. The container always listens on 51820, so map `SERVERPORT:51820/udp` |
| `PEERS` | A number, or alphanumeric names separated by commas. Enables server mode |
| `INTERNAL_SUBNET=10.13.13.0` | VPN subnet. `.1` is the server, `.2` and up are peers |
| `ALLOWEDIPS=0.0.0.0/0, ::/0` | What peers send into the tunnel. The tunnel is IPv4-only, and `::/0` stops IPv6 leaks. Make it narrower for a split tunnel |
| `PERSISTENTKEEPALIVE_PEERS=all` | Peers that get `PersistentKeepalive = 25`: `all`, `none`, or a list of `PEERS` entries or peer IDs (`laptop`, `peer_laptop`). Select a peer named `none` as `peer_none` |
| `SERVER_ALLOWEDIPS_PEER_<peer>` | More server-side AllowedIPs for one peer (site-to-site) |
| `LOG_CONFS=false` | `true` writes each peer QR code to the log. QR codes contain private keys, so use `show-peer` instead |
| `DNS_UPSTREAM=1.1.1.1,1.0.0.1` | Upstreams for the peer DNS forwarder: IPv4/IPv6 addresses separated by commas, each with an optional `#port` |
| `AWG_VERSION`, `AWG_*` | Protocol version and obfuscation. See below |

## Protocol versions and obfuscation

| `AWG_VERSION` | What you get |
|---|---|
| `2.0` (default) | Full DPI evasion: S1-S4 padding, H1-H4 ranges and an I1 signature (a QUIC Initial). Needs AmneziaVPN 4.8.12.9+ |
| `3.0` | 2.0 plus `HeaderProtectionKey`, content padding and random timers. Needs 3.0 software on each end |
| `3.1` | 3.0 plus `RandomTrailers = on`. Needs 3.1 software on each end |
| `1.5` | Legacy: integer H values, `S3 = S4 = 0`, no I1-I5 |

The container makes each value on the first start and saves it in `/config/server/awg_params`, so restarts keep the peers working. A value in the environment always wins. If `AWG_VERSION` changes, the container makes new S, H, I and 3.x values, and each peer needs its new config. If `awg` rejects a value (for example S < 12 under 3.x), the previous configs stay.

| Parameter | Default | Limits |
|-----------|---------|--------|
| `AWG_JC` / `AWG_JMIN` / `AWG_JMAX` | 3-8 / 40-80 / 80-250 | Junk packets. `JMIN < JMAX ≤ 1280` |
| `AWG_S1` / `AWG_S2` | 15-150 | Handshake padding. `S1 ≤ 1132`, `S2 ≤ 1188`, `S1 + 56 ≠ S2` |
| `AWG_S3` / `AWG_S4` | 8-55 / 4-20 (2.0), 12-55 / 12-20 (3.x), 0 (1.5) | Cookie / transport padding. `S3 ≤ 64`, `S4 ≤ 32`. Keep `S4 ≤ 20`: each packet carries it |
| `AWG_H1`-`AWG_H4` | Ranges that do not overlap (2.0+), integers (1.5) | Unique, all ≥ 5. With integers, the Amnezia app shows 1.5 |
| `AWG_I1`-`AWG_I5` | I1 = QUIC Initial (2.0+) | Signature packets in [tag syntax](#signature-packets-i1-i5) |
| `AWG_HEADER_PROTECTION_KEY` | Generated (3.x) | Same on each end. Needs S1-S4 ≥ 12 |
| `AWG_CONTENT_PADDING` | `lo-hi` in 16-128, or 0 with trailers (3.x) | Use `0`: padding costs about 22% of download speed |
| `AWG_REKEY_AFTER_TIME`, `AWG_REKEY_TIMEOUT`, `AWG_REJECT_AFTER_TIME`, `AWG_KEEPALIVE_TIMEOUT`, `AWG_MAX_HANDSHAKE_ATTEMPTS` | Random `lo-hi` ranges (3.x) | Each end selects its own value |
| `AWG_RANDOM_TRAILERS` | `on` under 3.1, else not set | `on`/`off`, any version. Same on each end. Needs `S1 = S2 = S3 = S4`, which the container draws automatically |
| `AWG_DISABLE_COOKIES` | not set | `on`/`off`, any version. Removes the WireGuard DoS protection. Can be different on each end |

The server and all clients must use the same values, except the 3.x timers and `DisableCookies`. For the 3.1 switches, only `on` writes a key. Set `off` to turn a switch off again: if you remove the variable, the container uses the saved value. A `RandomTrailers = on` that came only from the `3.1` preset goes away when you return to `2.0`. For kernel modules older than 3.1, see [Kernel module](#kernel-module).

### Signature packets (I1-I5)

Tags: `<b 0xHEX>` static bytes, `<r N>` random bytes, `<rd N>` random digits, `<rc N>` random letters, `<t>` a timestamp. Some third-party parsers, such as Keenetic, reject a tag larger than 1000 bytes. For them, write the default `<r 1178>` as `<r 1000><r 178>`, which is the same on the wire. [AmneziaWG Architect](https://architect.vai-rice.space/) makes signatures that look like QUIC, DNS, DTLS, SIP and other protocols.

## DNS (dnsmasq)

In server mode, each peer config gets `DNS = <subnet>.1`, the server end of the tunnel. dnsmasq answers there and forwards to `DNS_UPSTREAM`, to the first upstream that answers. It caches, but it does not recurse or check DNSSEC, and it sends plain DNS. For encrypted lookups with DNSSEC, use [Unbound on the host](#recommended-unbound-on-the-host).

dnsmasq runs as `abc` and answers only queries that come through the tunnel (`interface=wg0`), plus loopback in the container. Do not publish port 53. In client mode, dnsmasq does not run.

| File | Written by | When |
|---|---|---|
| `/config/templates/dnsmasq.conf` | you | Copied from the image only if it is missing |
| `/config/dnsmasq/dnsmasq.conf` | the container | Rendered from the template and `DNS_UPSTREAM` when one of them changes, or when the file is missing |
| `/run/dnsmasq/base.conf` | the container | At each start: user, interface and an include of the file above |

To change dnsmasq, edit the template and restart. Any dnsmasq option works, for example `address=/router.lan/192.168.1.1`. The shell expands the template, so write a literal `$` as `\$`. Do not add `interface=`, `listen-address=` or `bind-interfaces`: they make dnsmasq answer outside the tunnel.

The container tests the rendered file with `dnsmasq --test` before it installs it. If the test fails, the previous file stays and the log shows `DNS config generation failed`. Your edits to the rendered file stay until the next template or `DNS_UPSTREAM` change. If dnsmasq rejects the file, the tunnel still starts, but dnsmasq does not and the container is `unhealthy`. Remove the rendered file to render it again. DNS changes never regenerate the peer configs, and tunnel changes never re-render `dnsmasq.conf`.

## Recommended: Unbound on the host

Let dnsmasq forward to Unbound on the host. Peer lookups then leave the server over DNS-over-TLS with DNSSEC, and the Unbound cache serves the host too. Example for Debian/Ubuntu:

1. Give the stack a fixed network, so that the host has a stable address on it. Set `DNS_UPSTREAM` to that address:

   ```yaml
   services:
     amneziawg:
       # ... as in Quick start, plus:
       environment:
         - DNS_UPSTREAM=172.31.53.1
       networks:
         - awg

   networks:
     awg:
       driver_opts:
         com.docker.network.bridge.name: br-awg
       ipam:
         config:
           - subnet: 172.31.53.0/24
             gateway: 172.31.53.1
   ```

2. Install Unbound (`sudo apt install unbound`). Make it listen only on the gateway, in `/etc/unbound/unbound.conf.d/amneziawg.conf`:

   ```
   server:
       interface: 172.31.53.1
       # Unbound starts before Docker creates br-awg
       ip-freebind: yes
       access-control: 172.31.53.0/24 allow
       tls-cert-bundle: /etc/ssl/certs/ca-certificates.crt

   forward-zone:
       name: "."
       forward-tls-upstream: yes
       forward-addr: 1.1.1.1@853#cloudflare-dns.com
       forward-addr: 1.0.0.1@853#cloudflare-dns.com
   ```

   Run `sudo unbound-checkconf && sudo systemctl restart unbound`. The Debian and Ubuntu packages check DNSSEC by default. Remove the `forward-zone` to resolve from the root servers. Never bind Unbound to `0.0.0.0` or a public address: an open resolver gets abused.

3. Let DNS from the bridge through the host firewall, for example `sudo ufw allow in on br-awg to 172.31.53.1 port 53`.

4. Recreate the container (`docker compose up -d`). Test with `docker exec amneziawg nslookup example.com 172.31.53.1`.

## Performance and MTU

Most obfuscation affects only handshakes. `S4`, `HeaderProtectionKey`, `ContentPaddingAddition` and `RandomTrailers` cost something on each packet. [docs/awg-performance.md](docs/awg-performance.md) has the measurements.

The default templates set `MTU = 1280` in `[Interface]`, for the server and each peer. Use it for mobile, PPPoE and unknown paths. Without an `MTU` line, `awg-quick` uses 1420 with no allowance for `S4`. Full-size packets then fragment when `S4 > 20` or the endpoint is IPv6, and tunnels connect but are very slow.

To change the value, edit the `MTU` line in `/config/templates/server.conf` and `peer.conf`, and restart. Volumes from older releases keep templates without an `MTU` line. [docs/mtu.md](docs/mtu.md#how-to-set-it) tells how to add it and explains the numbers.

## Managing peers

```bash
docker exec amneziawg /app/show-peer 1 2 3
docker exec amneziawg /app/show-peer laptop phone
```

To add a peer, add it to `PEERS` and restart. Existing peers keep their keys and addresses. A peer that you remove from `PEERS` goes out of `wg0.conf`, and its directory moves to `/config/removed_peers/<peer>-<timestamp>/`. If you add the same name again, it gets a new identity.

The container regenerates the configs only when a server-side variable, an `AWG_*` value, a `SERVER_ALLOWEDIPS_PEER_*` value, or the `server.conf` or `peer.conf` template changes. The `awg` parser checks each new config, and the container installs all of them or none. If something fails (a broken template, a duplicate peer, a bad `SERVERURL`), no file changes and the log shows `Config generation failed` with the reason.

## Health check and troubleshooting

The container is `healthy` only while each tunnel in `/config/wg_confs` is up and, in server mode, dnsmasq answers on the tunnel address. The health check does not test `DNS_UPSTREAM`. A failed tunnel gets two more attempts. Docker does not restart unhealthy containers, so use the status for monitoring or with a tool such as autoheal.

```bash
docker logs amneziawg
docker exec amneziawg /app/healthcheck        # healthy or not, and why
docker exec amneziawg awg show
docker exec amneziawg cat /build_version      # bundled versions and commits
docker exec amneziawg nslookup example.com 10.13.13.1   # test the upstream
```

| Symptom | Fix |
|---|---|
| Custom `SERVERPORT` cannot be reached | Map `SERVERPORT:51820/udp` |
| Amnezia app shows AWG 1.5 | H1-H4 are integers. Use the 2.0 default ranges |
| Connection fails after a parameter change | Give the peers their new configs |
| Peers get no DNS | Run `/app/healthcheck`, and look for `DNS config generation failed` or `No valid dnsmasq config` in the log. If dnsmasq answers but names do not resolve, the container cannot reach `DNS_UPSTREAM` |
| Client: `Tunnel ... failed` after `permission denied on key "net.ipv4.conf.all.src_valid_mark"` | Add the `net.ipv4.conf.all.src_valid_mark=1` sysctl |
| Connects and pings, but downloads are very slow | Fragmentation. See [Performance and MTU](#performance-and-mtu) |
| Exit code 1 at start, log says `is not a numeric ID` or `(root) is not supported` | Set `PUID`/`PGID` to the owner of `./config` (`id -u`, `id -g`), not to 0 |
| The host loses its internet connection | Remove `network_mode: host`. See [Requirements](#requirements) |
| `WARNING: ... LinuxServer.io base image option` | See [From the LinuxServer.io-based releases](#from-the-linuxserverio-based-releases) |

## containerd and nerdctl (Alpine)

The image also runs under containerd with [nerdctl](https://github.com/containerd/nerdctl), and CI tests each change with it. Host setup on Alpine:

```bash
apk add containerd nerdctl cni-plugins iptables ip6tables
rc-update add containerd default && rc-service containerd start
modprobe tun && echo tun >> /etc/modules
echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-amneziawg.conf && sysctl -p /etc/sysctl.d/99-amneziawg.conf
```

Set `rc_cgroup_mode="unified"` (cgroup v2) in `/etc/rc.conf` and restart the host. Then use the Quick start file with `nerdctl compose up -d`, and replace `docker` with `nerdctl` in the other commands.

- Run nerdctl as root. CI tests only rootful nerdctl.
- nerdctl runs `HEALTHCHECK` only through systemd timers, so on OpenRC the status does not change. Run `nerdctl exec amneziawg /app/healthcheck` from your monitoring.
- containerd applies `restart: unless-stopped`, so it must start at boot (the `rc-update` line).

## Upgrading

Each release has the tag `<amneziawg-tools>-r<N>` (for example `3.1.20260812-r14`) and [release notes](https://github.com/lqflqf/docker-amneziawg/releases).

### Changed defaults

- `PERSISTENTKEEPALIVE_PEERS` is `all` (it was off). On a volume that never set it, the first start adds the keepalive lines to `wg0.conf` once. Peer configs do not change. Set `none` for the old behaviour, for example to save phone batteries.
- The default templates set `MTU = 1280` (it was 1420). Existing volumes keep their templates. See [Performance and MTU](#performance-and-mtu).
- `PUID`/`PGID` `0` (root) stops the container.
- The container ignores `HEALTHCHECK_DNS_NAME`, `PEERDNS`, `USE_DNS` and `USE_COREDNS`, with a note in the log. If you set `PEERDNS`, the peer configs get the tunnel address as DNS. Import them again.

### From the LinuxServer.io-based releases

Releases up to `3.1.20260812-r5` used the LinuxServer.io base image. For a normal setup, change nothing: the volume, `PUID`/`PGID`/`TZ`, the keys and the configs stay. The container ignores these options of that base image, with a `WARNING` at start-up:

| No longer supported | Instead |
|---|---|
| `FILE__<NAME>` | Set `<NAME>` directly. `FILE__PEERS` leaves `PEERS` unset, which means client mode |
| `DOCKER_MODS`, `/custom-cont-init.d`, `/custom-services.d` | Build your own image `FROM` this one |
| `UMASK` | Not needed: configs and keys are always `600` |
| `ATTACHED_DEVICES_PERMS`, `LSIO_READ_ONLY_FS`, `LSIO_NON_ROOT_USER` | None. The container runs as root and needs a writable root filesystem |

### From the Unbound releases

`/config/unbound/` is not used, and you can remove it. Unbound in the container used DNS-over-TLS and DNSSEC, but dnsmasq forwards plain DNS. To keep both, run [Unbound on the host](#recommended-unbound-on-the-host). The `wg`/`wg-quick` aliases and `/etc/wireguard` are gone: use `awg` and `awg-quick`.

### From AYastrebov/docker-amneziawg

Change `image:`. The main difference is DNS: this fork uses dnsmasq instead of CoreDNS. You can remove `/config/coredns/`.

| | Original (CoreDNS) | This fork (dnsmasq) |
|---|---|---|
| Upstream | The host resolver | `DNS_UPSTREAM` (Cloudflare by default, or a resolver on the host) |
| Who can query | Each client that reaches port 53 | Peers through the tunnel only |
| On/off | `USE_COREDNS` | On in server mode, off in client mode |
| Peer DNS | `PEERDNS` | Always the server tunnel address |
| Config | `/config/coredns/Corefile` | `/config/templates/dnsmasq.conf` |

This fork also adds a health check, all-or-nothing config regeneration and protocol-version migration. It also adds an archive of removed peers, private secrets, input checks and a plain Alpine base.

## Security

Keys, configs and QR codes are mode `600`. **Treat write access to `/config` as root access**: `PostUp` lines and the templates run as root. See [SECURITY.md](SECURITY.md) for details and vulnerability reports.

## Links

- [AmneziaVPN documentation](https://docs.amnezia.org/) · [kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) · [AmneziaWG Architect](https://architect.vai-rice.space/)
- [CONTRIBUTING.md](CONTRIBUTING.md) (build and test) · [releases](https://github.com/lqflqf/docker-amneziawg/releases)

## Credits

This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg). [Andrey Yastrebov](https://github.com/AYastrebov) created it and wrote the container design, the config generation and most of the AWG work. Many thanks to him for the work and for its release under the MIT license. The project also builds on [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard) and on the `amneziawg-go`, `amneziawg-tools` and kernel module of the [AmneziaVPN](https://github.com/amnezia-vpn) team.

## License

MIT, see [LICENSE](LICENSE). The original copyright notice stays, because the license requires it.

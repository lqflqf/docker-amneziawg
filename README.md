# Docker AmneziaWG

[![Docker Build](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml/badge.svg)](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml)
[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-docker--amneziawg-blue?logo=docker)](https://github.com/lqflqf/docker-amneziawg/pkgs/container/docker-amneziawg)
[![GitHub release](https://img.shields.io/github/v/release/lqflqf/docker-amneziawg)](https://github.com/lqflqf/docker-amneziawg/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[AmneziaWG](https://docs.amnezia.org/) VPN server and client in one container. AmneziaWG is WireGuard with traffic obfuscation. This makes the handshake more difficult for deep packet inspection to identify. In server mode, the container does three things. It writes the server config. It gives you a config and QR code for each peer. It answers DNS for connected clients. It runs on Alpine Linux with [s6-overlay](https://github.com/just-containers/s6-overlay).

> This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg). [Andrey Yastrebov](https://github.com/AYastrebov) created it and maintains it. The container design, the config generation, and most AWG work are his. Many thanks to him for the work and for its release under the MIT license. The main change here is DNS; see [Differences from the original](#differences-from-the-original).

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
      - SERVERURL=vpn.example.com   # or leave out to detect the public IPv4
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

Each named peer has its config at `./config/peer_laptop/peer_laptop.conf`. If `PEERS` is a number, the peer config is at `./config/peer1/peer1.conf`. [`docker-compose.yml`](docker-compose.yml) lists each option with comments.

**Requirements:** Use a Docker host on amd64 or arm64. The host must have `/dev/net/tun` and the `NET_ADMIN` capability. A kernel module is optional; see below.

## Kernel module

The container works without a kernel module. It uses the bundled userspace `amneziawg-go` if necessary. For better throughput, install the [AmneziaWG kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) on the host. At start-up, the container detects the module and uses it.

- **Which datapath is in use:** the log says `AmneziaWG kernel module is active` or `using userspace amneziawg-go`.
- **`SYS_MODULE` is not needed.** The container never calls `modprobe`; it only checks if the module is loaded. Keep `SYS_MODULE` only on minimal hosts that do not load the iptables NAT modules automatically.
- **Keep the module and image on the same feature generation.** An older module still works with a newer image. But the kernel datapath applies only the options that module knows. A module older than 3.1 rejects `RandomTrailers` and `DisableCookies`. The tunnel then fails with `Unable to modify interface: Invalid argument`. The container warns about this at start-up if it can read `/sys/module/amneziawg/version`.
- **With `RandomTrailers`, use a module built from upstream `4569c4c6` (2026-09-06) or newer**. Older 3.1 modules send some oversized handshake packets. The version string does not show the fix; see [how to check](docs/awg-performance.md#checking-whether-your-module-has-the-fix).

## Modes

- **Server mode (`PEERS` set):** generates keys, `wg0.conf`, and one config plus QR code for each peer. It also runs a dnsmasq DNS forwarder on the server tunnel address. Each peer config uses this address as its DNS server.
- **Client mode (`PEERS` unset):** starts each `.conf` in `./config/wg_confs/`. The file name is the interface name. Nothing is generated and dnsmasq does not run. Each conf `DNS` line becomes the container resolver. A full tunnel (`AllowedIPs = 0.0.0.0/0`) needs the `net.ipv4.conf.all.src_valid_mark=1` sysctl. See the client example in [`docker-compose.yml`](docker-compose.yml).

In either mode, if no tunnel starts, the container removes each IPv4 and IPv6 default route. Thus nothing leaks outside the VPN, and the container reports `unhealthy`.

## Differences from the original

The main change from [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg) is DNS for peers. This fork uses **a dnsmasq forwarder instead of CoreDNS**. It sits in front of a resolver of your choice, ideally [Unbound on the host](#recommended-unbound-on-the-host).

| | Original (CoreDNS) | This fork (dnsmasq) |
|---|---|---|
| Where peer queries go | The host's resolver | `DNS_UPSTREAM`: Cloudflare `1.1.1.1` / `1.0.0.1` by default, or a resolver on the host |
| Who can query it | Anyone who reaches port 53 | Peers through the tunnel only |
| On/off | `USE_COREDNS` | Always on in server mode, off in client mode |
| Peer DNS | `PEERDNS` | Always the server's tunnel address |
| Config file | `/config/coredns/Corefile` | `/config/templates/dnsmasq.conf` |

**Change from the original image:** change `image:`. `USE_COREDNS` and `PEERDNS` are ignored. If you set `PEERDNS`, peer configs are regenerated with the tunnel address as DNS. Re-import them. You can delete `/config/coredns/`.

This fork also adds:
- a health check
- transactional config regeneration
- protocol-version migration
- an archive of removed peers
- hardened secrets and DNS defaults
- per-build image tags (`<amneziawg-tools>-r<N>`, for example `3.1.20260812-r2`); each has a [release](https://github.com/lqflqf/docker-amneziawg/releases) with its changes
- a plain Alpine base with Alpine's s6-overlay package instead of the LinuxServer.io base image. See [Upgrading from the LinuxServer.io-based releases](#upgrading-from-the-linuxserverio-based-releases)

### Upgrading from the LinuxServer.io-based releases

Releases up to `3.1.20260812-r5` used the LinuxServer.io base image. Later releases use plain Alpine with s6-overlay. For a normal setup, change nothing. The volume, `PUID`/`PGID`/`TZ`, keys, and configs stay unchanged. The only exception is the one-time `wg0.conf` update for the [new keepalive default](#changed-defaults).

The extras of the LinuxServer.io base image are gone. If you still set one of these options, the container writes a `WARNING` at start-up and ignores the option:

| No longer supported | Instead |
|---|---|
| `FILE__<NAME>` (read a variable from a file) | Set `<NAME>` directly. A `FILE__PEERS` leaves `PEERS` unset, which means client mode |
| `DOCKER_MODS`, `/custom-cont-init.d`, `/custom-services.d` | Build your own image `FROM` this one |
| `UMASK` | Not needed: configs and keys are always written `600` |
| `ATTACHED_DEVICES_PERMS`, `LSIO_READ_ONLY_FS`, `LSIO_NON_ROOT_USER` | None; the container runs as root and needs a writable root filesystem |

If `PUID` or `PGID` is not a number from 0 to 4294967294, the container now stops. If it cannot be applied, the container also stops. The exit code is 1, and no config is touched.

## DNS (dnsmasq)

In server mode, each peer config gets `DNS = <subnet>.1`, the server end of the tunnel. dnsmasq answers there and forwards each query to `DNS_UPSTREAM`. The default order is `1.1.1.1`, then `1.0.0.1`. It sends to the upstream that answers, so one can fail without slow lookups. It is a forwarder only. It caches, but does not recurse or validate DNSSEC. It talks plain DNS to the upstream. For encrypted, validated lookups, point it at [Unbound on the host](#recommended-unbound-on-the-host).

dnsmasq runs as the unprivileged `abc` user. It is bound to the tunnel interface (`interface=wg0`). It answers only queries that arrive through the tunnel, plus loopback inside the container. A host or container that routes the VPN subnet to the container gets no answer. Do not publish port 53. In client mode, dnsmasq does not run.

Its configuration follows the same rule as the tunnel configs:

| File | Written by | When |
|---|---|---|
| `/config/templates/dnsmasq.conf` | you | Copied from the image only if it is missing |
| `/config/dnsmasq/dnsmasq.conf` | the container | Rendered from the template and `DNS_UPSTREAM` when either changed. Also rendered if the file is missing. Otherwise left as it is |
| `/run/dnsmasq/base.conf` | the container | Each start: user, listen address, and an include of the file above |

To change dnsmasq, edit the template and restart. Any dnsmasq option works, for example `address=/router.lan/192.168.1.1` or `server=/corp.example/10.0.0.53`. Options are added to the container options. Thus `interface=`, `listen-address=` or `bind-interfaces` lines make dnsmasq answer outside the tunnel too. Like the other templates, the shell expands it. Thus escape a literal `$` as `\$`. The rendered file is checked with `dnsmasq --test` before it replaces the old one. If the check fails, the previous file stays. The log shows `DNS config generation failed` with the reason. Edits made directly to the rendered file last until the next template or `DNS_UPSTREAM` change. If dnsmasq rejects the file, it does not start and the container reports `unhealthy`. The tunnel still starts. Delete the rendered file to render it again.

DNS changes never regenerate the peer configs. Tunnel changes never re-render `dnsmasq.conf`.

### Upgrading from the Unbound releases

- `PEERDNS` and `USE_DNS` are ignored, with a note in the log. If you set `PEERDNS`, peer configs are regenerated with the tunnel address as DNS. Re-import them.
- `/config/unbound/` is no longer used and can be deleted.
- Unbound queried Cloudflare over DNS-over-TLS and validated DNSSEC. dnsmasq forwards plain DNS. To keep both, run [Unbound on the host](#recommended-unbound-on-the-host).
- The `wg`/`wg-quick` aliases and the `/etc/wireguard` link are gone. Use `awg` and `awg-quick`.

## Recommended: Unbound on the host

Let the container dnsmasq forward to a validating resolver on the host. Peer lookups then leave the server over DNS-over-TLS. DNSSEC is checked, and the Unbound cache serves everything on the host. Example for Debian/Ubuntu with Compose:

1. Give the stack a fixed network, so the host has a stable address on it. Then point `DNS_UPSTREAM` at that address:

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

2. Install Unbound (`sudo apt install unbound`). Make it listen on that gateway only, in `/etc/unbound/unbound.conf.d/amneziawg.conf`:

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

   Then run `sudo unbound-checkconf && sudo systemctl restart unbound`. The Debian and Ubuntu packages validate DNSSEC by default. Leave out the `forward-zone` to have Unbound resolve from the root servers. Never bind Unbound to `0.0.0.0` or a public address. An open resolver gets abused.

3. Allow DNS from the bridge through the host firewall, for example with ufw: `sudo ufw allow in on br-awg to 172.31.53.1 port 53`.

4. Recreate the container (`docker compose up -d`). Check it with `docker exec amneziawg nslookup example.com 172.31.53.1`.

With `network_mode: host`, use `interface: 127.0.0.1@5335` and `access-control: 127.0.0.0/8 allow` instead. Skip the firewall step, and set `DNS_UPSTREAM=127.0.0.1#5335`.

## Parameters

| Parameter | Function |
|-----------|----------|
| `-e PUID=911` / `-e PGID=911` / `-e TZ` | Owner of `/config` and timezone. dnsmasq also runs as this user. An invalid `PUID`/`PGID` stops the container |
| `-e SERVERURL=` | Host or IP written into peer configs. Unset detects the public IPv4 over HTTPS. If detection fails, it keeps the previous value. `auto` is deprecated: it warns and is treated as unset |
| `-e SERVERPORT=51820` | Port advertised to peers. The container always listens on 51820, so map `SERVERPORT:51820/udp` (**not** `SERVERPORT:SERVERPORT`) |
| `-e PEERS=` | Number or comma-separated alphanumeric names. Enables server mode |
| `-e INTERNAL_SUBNET=10.13.13.0` | VPN subnet (`.1` is the server, `.2` and up are peers) |
| `-e ALLOWEDIPS=0.0.0.0/0, ::/0` | What peers route into the tunnel. The tunnel is IPv4-only. `::/0` sinks peer IPv6 to prevent leaks. Narrow it for split tunnelling |
| `-e PERSISTENTKEEPALIVE_PEERS=all` | Peers the server sends `PersistentKeepalive = 25` to: `all`, `none`, or a comma-separated list of `PEERS` entries or peer IDs. Examples are `laptop` or `peer_laptop`. A peer named `none` can only be selected as `peer_none` |
| `-e SERVER_ALLOWEDIPS_PEER_<peer>=` | Extra server-side AllowedIPs for one peer (site-to-site) |
| `-e LOG_CONFS=false` | `true` prints each peer QR code to the log. QR codes contain private keys; `show-peer` is the safer way |
| `-e DNS_UPSTREAM=1.1.1.1,1.0.0.1` | Where the peer DNS forwarder sends queries. dnsmasq uses this in server mode. Use comma-separated IPv4/IPv6 addresses with an optional `#port`. See [DNS](#dns-dnsmasq) |
| `-e AWG_VERSION=2.0` | Protocol version, see below |
| `-e AWG_*` | Obfuscation parameters, see below. All are random by default |

### Changed defaults

- `PERSISTENTKEEPALIVE_PEERS` now defaults to `all`. It used to be off. On a volume that never set it, the first start regenerates the configs once. This adds the keepalive lines to `wg0.conf`. Keys and peer configs stay the same, so peers need no re-import. Set `none` to keep the old behaviour, for example to spare phone batteries.
- `HEALTHCHECK_DNS_NAME` has been removed and is ignored with a note in the log. The health check only tests that dnsmasq answers on the tunnel address. It does not test if `DNS_UPSTREAM` is reachable. Check that with `docker exec amneziawg nslookup example.com <tunnel address>`.

## Protocol versions and obfuscation

| `AWG_VERSION` | What you get |
|---|---|
| `2.0` (default) | Full DPI evasion: S1-S4 padding, H1-H4 ranges, and an I1 signature (a QUIC Initial). Needs AmneziaVPN 4.8.12.9+ |
| `3.0` | 2.0 plus `HeaderProtectionKey`, content padding, and randomized timers. Needs 3.0-capable software on each end |
| `3.1` | 3.0 plus `RandomTrailers = on`. Needs 3.1-capable software on each end |
| `1.5` | Legacy: integer H values, `S3 = S4 = 0`, no I1-I5 |

Each value is generated on the first start and saved to `/config/server/awg_params`. Thus restarts keep peers functional. A value you set in the environment always wins. When `AWG_VERSION` changes, the saved version-specific values are regenerated for the new version. These values are S, H, I, and the 3.x parameters. Each peer then needs its new config. If `awg` rejects a value, generation stops and previous configs stay in place. An example is an S value below 12 under 3.x.

| Parameter | Default | Constraints |
|-----------|---------|-------------|
| `AWG_JC` / `AWG_JMIN` / `AWG_JMAX` | 3-8 / 40-80 / 80-250 | Junk packets. `JMIN < JMAX ≤ 1280` |
| `AWG_S1` / `AWG_S2` | 15-150 | Handshake padding. `S1 ≤ 1132`, `S2 ≤ 1188`, `S1 + 56 ≠ S2` |
| `AWG_S3` / `AWG_S4` | 8-55 / 4-20 (2.0); 12-55 / 12-20 (3.x); 0 (1.5) | Cookie / transport padding. `S3 ≤ 64`, `S4 ≤ 32`; keep `S4 ≤ 20` because it is paid on each packet |
| `AWG_H1`-`AWG_H4` | Non-overlapping ranges (2.0+), integers (1.5) | Unique, all ≥ 5. Integers make the Amnezia app report 1.5 |
| `AWG_I1`-`AWG_I5` | I1 = QUIC Initial (2.0+) | Signature packets in [tag syntax](#signature-packets-i1-i5) |
| `AWG_HEADER_PROTECTION_KEY` | Generated (3.x) | Must be identical everywhere; needs S1-S4 ≥ 12 |
| `AWG_CONTENT_PADDING` | `lo-hi` within 16-128, or 0 with trailers (3.x) | `0` is recommended because padding costs about 22% of download speed |
| `AWG_REKEY_AFTER_TIME`, `AWG_REKEY_TIMEOUT`, `AWG_REJECT_AFTER_TIME`, `AWG_KEEPALIVE_TIMEOUT`, `AWG_MAX_HANDSHAKE_ATTEMPTS` | Random `lo-hi` ranges (3.x) | Each end draws its own value, so these do not have to match |
| `AWG_RANDOM_TRAILERS` | `on` under 3.1, otherwise unset | `on`/`off`, works with any version. Must match on each end, and needs `S1 = S2 = S3 = S4`. These values are drawn that way automatically |
| `AWG_DISABLE_COOKIES` | unset | `on`/`off`, works with any version. Gives up WireGuard's DoS mitigation; does not have to match |

The server and all clients must use the same values, except for the 3.x timers. Notes on the 3.1 switches:
- Only `on` writes a key. `off` omits the key, and it turns a switch off again. If you remove the variable instead, the saved value is reused.
- A `RandomTrailers` value that came only from the `3.1` preset is dropped when you return to `2.0`.
- A host kernel module older than 3.1 rejects both keys (`Unable to modify interface: Invalid argument`). The container warns about this at start-up if it can read the module version.

### Signature packets (I1-I5)

Tags: `<b 0xHEX>` static bytes, `<r N>` random bytes, `<rd N>` random digits, `<rc N>` random letters, `<t>` a timestamp. Some third-party parsers, such as Keenetic, reject a single tag larger than 1000 bytes. For those parsers, write the default trailing `<r 1178>` as `<r 1000><r 178>`. It is identical on the wire. [AmneziaWG Architect](https://architect.vai-rice.space/) generates signatures that imitate QUIC, DNS, DTLS, SIP, and other protocols.

## Performance and MTU

Most obfuscation affects only handshakes. `S4`, `HeaderProtectionKey`, `ContentPaddingAddition`, and `RandomTrailers` have a cost on each packet. [docs/awg-performance.md](docs/awg-performance.md) has measurements for each.

`awg-quick` sets the tunnel MTU to 1420 without an allowance for `S4`. Thus full-size packets fragment when `S4 > 20` or when the endpoint is IPv6. Tunnels then connect but become very slow. Add `MTU = 1280` to `[Interface]` for mobile, PPPoE, and unknown paths. On a path that is limited to 1280 bytes, use `path − 60 − S4` (IPv4) or `path − 80 − S4` (IPv6). To apply it to future configs, put the line in `/config/templates/server.conf` and `peer.conf`. [docs/mtu.md](docs/mtu.md) explains the numbers.

## Managing peers

```bash
docker exec amneziawg /app/show-peer 1 2 3
docker exec amneziawg /app/show-peer laptop phone
```

To add a peer, add it to `PEERS` and restart. Existing peers keep their keys and addresses. When you remove a peer from `PEERS`, it disappears from `wg0.conf`. Its directory moves to `/config/removed_peers/<peer>-<timestamp>/`, which frees its address. If you add the same name again, it creates a new identity.

Configs are regenerated when a server-side variable changes. They are also regenerated when an `AWG_*` value, a `SERVER_ALLOWEDIPS_PEER_*` value, or a template changes. These templates are `server.conf` and `peer.conf`. Otherwise, configs stay as they are. DNS settings have their own rule; see [DNS](#dns-dnsmasq). Each generated config is checked with the `awg` parser. The new set is installed all-or-nothing. If anything fails, no file changes. Examples are a broken template, a duplicate peer, or an invalid `SERVERURL`. The log shows `Config generation failed` with the reason.

## Health check and troubleshooting

The container is `healthy` only while each tunnel in `/config/wg_confs` is up. In server mode, dnsmasq must also answer on the tunnel address. The container tries a failed tunnel two more times. Then it stops the retries. Docker does not restart unhealthy containers by itself. Use the status for monitoring or with a tool such as autoheal.

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
| Peers get no DNS | Run `/app/healthcheck`. The log shows `DNS config generation failed` or `No valid dnsmasq config` with the reason. If dnsmasq answers but names do not resolve, the container cannot reach `DNS_UPSTREAM` |
| Client: `Tunnel ... failed` after `sysctl: permission denied on key "net.ipv4.conf.all.src_valid_mark"` | Add the `net.ipv4.conf.all.src_valid_mark=1` sysctl to the container |
| Connects, pings fine, downloads crawl | Fragmentation. See [Performance and MTU](#performance-and-mtu) |
| Container exits with code 1 right after start, log says `PUID=... is not a numeric ID` | Set `PUID`/`PGID` to numeric IDs. Examples are the output of `id -u` and `id -g` |
| `WARNING: ... LinuxServer.io base image option` | See [Upgrading from the LinuxServer.io-based releases](#upgrading-from-the-linuxserverio-based-releases) |

## Security

Keys, configs, and QR codes are mode `600`. **Treat write access to `/config` as root access**. `PostUp` lines and the templates run as root. Details and vulnerability reports are in [SECURITY.md](SECURITY.md).

## Links

- [AmneziaVPN documentation](https://docs.amnezia.org/) · [kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) · [AmneziaWG Architect](https://architect.vai-rice.space/)
- [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard), the project this one is modeled on
- [CONTRIBUTING.md](CONTRIBUTING.md) (building and testing locally) · [releases](https://github.com/lqflqf/docker-amneziawg/releases)

## Credits

This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg) by [Andrey Yastrebov](https://github.com/AYastrebov). He created it and wrote most of its history. It builds on [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard). It also builds on the [AmneziaVPN](https://github.com/amnezia-vpn) team's `amneziawg-go`, `amneziawg-tools`, and kernel module.

## License

MIT, see [LICENSE](LICENSE). The original copyright notice is kept, because the license requires it.

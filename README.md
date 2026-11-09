# Docker AmneziaWG

[![Docker Build](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml/badge.svg)](https://github.com/lqflqf/docker-amneziawg/actions/workflows/docker-build.yml)
[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-docker--amneziawg-blue?logo=docker)](https://github.com/lqflqf/docker-amneziawg/pkgs/container/docker-amneziawg)
[![GitHub release](https://img.shields.io/github/v/release/lqflqf/docker-amneziawg)](https://github.com/lqflqf/docker-amneziawg/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[AmneziaWG](https://docs.amnezia.org/) VPN server in one container. AmneziaWG is WireGuard with traffic obfuscation, which makes the handshake difficult for deep packet inspection to identify. The `awgctl` command keeps the server, the peers and the obfuscation parameters in one SQLite file. It makes a config and a QR code for each peer, and dnsmasq answers DNS for the peers. The image is Alpine Linux with [s6-overlay](https://github.com/just-containers/s6-overlay), for amd64 and arm64.

> This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg) (see [Credits](#credits)). **Breaking release:** the container is now a server only, and `awgctl` replaces the env-var config. A volume of an older release needs a one-time import. See [Upgrading](#from-the-releases-before-awgctl).

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
      - SERVERURL=vpn.example.com   # recommended. Leave it out to detect the public IPv4 once
    volumes:
      - ./config:/config
    ports:
      - 51820:51820/udp
    sysctls:
      - net.ipv4.ip_forward=1
    restart: unless-stopped
```

1. Start the container: `docker compose up -d`.
2. Wait for `Config initialization finished` and `Tunnel wg0 is up` in `docker logs amneziawg`.
3. Add a peer and show its QR code: `docker exec -it amneziawg awgctl peer add --name phone --qr`.
4. Restart the container to apply the new peer: `docker restart amneziawg`.
5. Scan the QR code with the [Amnezia app](https://amnezia.org/).

A new server has no peers. Add one peer for each device. [`docker-compose.yml`](docker-compose.yml) shows each variable.

### Requirements

- A Docker or containerd host (see [containerd and nerdctl](#containerd-and-nerdctl-alpine)) with `/dev/net/tun` and the `NET_ADMIN` capability. A [kernel module](#kernel-module) is optional.
- The `net.ipv4.ip_forward=1` sysctl in the container. The image does not need `src_valid_mark`.
- A restart policy, for example `restart: unless-stopped`. If the start fails, the container stops with exit code 1, and the policy tries again.
- Peer apps that support AWG 3.1, the default version. For older apps, see [Protocol versions](#protocol-versions-and-obfuscation).
- **Do not use host networking** (`network_mode: host`). The `PostUp` lines add `iptables` rules to the network namespace of the container. With host networking, that namespace is the host. Use the default bridge network and publish `51820/udp`.

## First start

On the first start, `/config` has no database. `awgctl init` reads the environment, makes the server key and the AWG parameters, and writes `/config/amneziawg.db`. It reads the first-start variables in [Settings](#settings) and the [`AWG_*` variables](#protocol-versions-and-obfuscation) **only on the first start**. After that, the database is the only source of the configuration, and the container ignores these variables. Use `awgctl` to change a value.

If `SERVERURL` is not set, the container asks 3 HTTPS services for the public IPv4. It tries 3 times, after 0 s, 5 s and 15 s. If all attempts fail, the container stops and writes nothing. Set `SERVERURL` and start again. `SERVERURL=auto` is an error.

`PUID` and `PGID` (default `911`) apply at each start. They set the owner of `/config` and of the database. Use a number from 1 to 4294967294. A value that is not valid, or `0` (root), stops the container with exit code 1.

## `awgctl`

Run `awgctl` in the running container: `docker exec amneziawg awgctl <command>`. `awgctl help` shows each command. Before `awgctl` saves a change, it checks the full config with the `awg` parser and `dnsmasq --test`. If a check fails, nothing changes.

`awgctl` changes only the database. The running tunnel and dnsmasq do not change. **Restart the container to apply the saved changes**: `docker restart amneziawg`. At each start, `awgctl init` reads the database and writes the configs in `/run`. A restart stops all tunnels for some seconds.

| Command | Function |
|---|---|
| `health` | The health check (see [Health check](#health-check-and-troubleshooting)) |
| `version` | Versions of `awgctl`, `awg` and `amneziawg-go` |
| `get [key] [--show-secrets]`, `set <key> <value> [--yes]`, `reset <key> [--yes]` | [Settings](#settings) |
| `server rotate-key --yes` | Makes a new server key. Each peer needs its new config |
| `awg show`, `awg version`, `awg set`, `awg regenerate` | [AWG parameters](#protocol-versions-and-obfuscation) |
| `peer add`, `add-many`, `list`, `show`, `export`, `rename`, `set`, `disable`, `enable`, `rotate`, `rm` | [Peers](#peers) |
| `import-legacy <dir>` | Imports a volume of an older release once. See [Upgrading](#from-the-releases-before-awgctl) |

| Exit code | Meaning |
|---|---|
| 0 | Done |
| 1 | Error. Nothing changed |
| 2 | Usage error: an unknown command, a flag that the command does not accept, or a wrong number of arguments |
| 3 | Refused: the change needs `--yes` |

A command needs `--yes` when a peer needs a new config after the change. `peer rm` and `server rotate-key` always need `--yes`. Without `--yes`, the command exits with code 3 and changes nothing.

### Peers

```bash
docker exec -it amneziawg awgctl peer add --name laptop --qr   # new peer, QR code in the terminal
docker exec amneziawg awgctl peer add-many 5                    # 5 new peers: "peer 3" to "peer 7"
docker exec amneziawg awgctl peer list                          # ids, names, addresses, handshakes, traffic
docker exec amneziawg awgctl peer export 2 > laptop.conf
docker exec -it amneziawg awgctl peer export 2 --qr
docker exec amneziawg awgctl peer rename 2 "work laptop"        # or: peer rename 2 --clear
docker exec amneziawg awgctl peer disable 2                     # keeps the keys
docker exec amneziawg awgctl peer rotate 2 --yes                # new keys, export again
docker exec amneziawg awgctl peer rm 2 --yes                    # removes the peer and its keys
docker restart amneziawg                                        # applies the peer changes
```

- Each peer command takes the peer id, a number from 2 to 254. The id is also the last octet of the peer address: peer 5 has `10.13.13.5`. A name or `#5` is a usage error.
- A new peer gets the lowest free id. The maximum is 253 peers. After `peer rm`, a new peer can get the same id.
- `peer add-many <count>` adds 1 to 253 peers in one change. If there are not enough free ids, it adds no peer.
- A name has 1-32 letters, digits, spaces, `.`, `_` or `-`. It starts with a letter or a digit and does not end with a space. Each name is unique.
- A peer without `--name` gets the name `peer <id>`, for example `peer 5`. Only peer 5 can have the name `peer 5`. `peer rename <id> --clear` sets this name again.
- Keepalive is on (25 s) for a new peer. To save phone batteries, use `--no-keepalive` or `peer set <id> --keepalive off`.
- The container does not keep peer configs or QR codes on disk. Use `peer export` (`--qr`, `--png FILE` or `--conf FILE`, or more than one of them) each time. An export shows the saved config, which works after the next restart.

### Settings

`awgctl get` shows each setting. `awgctl set <key> <value>` changes one setting. `awgctl reset <key>` sets the default again. Each change applies after `docker restart amneziawg`. A change marked **new configs** needs `--yes`. After the restart, export each peer config again and import it on the device.

| Key | First-start variable | Default | Function |
|---|---|---|---|
| `server.endpoint_host` | `SERVERURL` | detected | Host or IP in the peer configs. **Set it** to a DNS name or a fixed IP. New configs |
| `server.endpoint_port` | `SERVERPORT` | `51820` | Port in the peer configs. The container always listens on 51820, so map `SERVERPORT:51820/udp`. New configs |
| `net.subnet` | `INTERNAL_SUBNET` | `10.13.13.0/24` | VPN subnet, `/24` only. `.1` is the server, `.2` to `.254` are peers. New configs |
| `net.mtu` | `MTU` | `1280` | Tunnel MTU of the server, 576-1500 or `none`. See [Performance and MTU](#performance-and-mtu) |
| `net.peer_mtu` | `MTU` | `1280` | MTU in the peer configs. New configs |
| `peer.default_allowed_ips` | `ALLOWEDIPS` | `0.0.0.0/0, ::/0` | What peers send into the tunnel. The tunnel is IPv4-only, and `::/0` stops IPv6 leaks. Make it narrower for a split tunnel. New configs |
| `peer.keepalive_seconds` | — | `25` | `PersistentKeepalive` of the peers with keepalive on |
| `fw.nat_interface` | — | `eth+` | Interface of the `MASQUERADE` rule: a name, or a prefix with `+` |
| `fw.ipv6_forward_drop` | — | `true` | Drop IPv6 that peers send through `wg0` |
| `dns.upstream` | `DNS_UPSTREAM` | `1.1.1.1,1.0.0.1` | Upstreams of the [DNS forwarder](#dns-dnsmasq): IPv4/IPv6 addresses, each with an optional `#port` |
| `server.private_key` | — | generated | Change it only with `awgctl server rotate-key --yes`. New configs |

## Protocol versions and obfuscation

| Version | What you get |
|---|---|
| `3.1` (default) | 3.0 plus `RandomTrailers = on`. Needs 3.1 software on each end |
| `3.0` | 2.0 plus `HeaderProtectionKey`, content padding and random timers. Needs 3.0 software on each end |
| `2.0` | Full DPI evasion: S1-S4 padding, H1-H4 ranges and an I1 signature (a QUIC Initial). Needs AmneziaVPN 4.8.12.9+ |
| `1.5` | Legacy: integer H values, `S3 = S4 = 0`, no I1-I5 |

The container makes each value on the first start. To start with a different version, set `AWG_VERSION` on the first start. To pin one value, set its `AWG_*` variable, for example `AWG_JC=5`. After the first start, use `awgctl awg`. Each AWG change applies after a restart:

```bash
docker exec amneziawg awgctl awg show                  # the current values
docker exec amneziawg awgctl awg version 2.0 --yes     # new S, H, I and 3.x values
docker exec amneziawg awgctl awg set s=16 --yes        # S1-S4 together
docker exec amneziawg awgctl awg set jc=6 jmin=50 jmax=200
docker exec amneziawg awgctl awg regenerate h1 h2 --yes
docker restart amneziawg
```

- If the change touches a value that must match, the command needs `--yes`, and each peer needs its new config.
- `awg version` sets `RandomTrailers` to `on` for 3.1 and to `off` for the other versions. It draws S, H, I and the 3.x values again. `Jc`, `Jmin`, `Jmax` and `DisableCookies` stay.
- `awg set` takes several `key=value` pairs and checks them together. The key is the short name (`s1`, `content_padding`), the conf name (`S1`) or the variable name (`AWG_S1`).
- A value that breaks a rule is an error. Only the S maximums and `S4 > 20` give a warning.

| Variable / `awg` key | Default | Rules |
|---|---|---|
| `AWG_JC` / `AWG_JMIN` / `AWG_JMAX` | 3-8 / 40-80 / 81-250 | Junk packets. `Jc ≤ 128`, `Jmin < Jmax ≤ 1280` |
| `AWG_S1` / `AWG_S2` | 15-150 | Handshake padding. `S1 + 56 ≠ S2`. Warning above `S1 = 1132`, `S2 = 1188` |
| `AWG_S3` / `AWG_S4` | 8-55 / 4-20 (2.0), 12-55 / 12-20 (3.x), 0 (1.5) | Cookie / transport padding. Warning above `S3 = 64`, `S4 = 32`, and for `S4 > 20` |
| `AWG_H1`-`AWG_H4` | Ranges that do not overlap (2.0+), integers (1.5) | All ≥ 5, no overlap. With integers, the Amnezia app shows 1.5 |
| `AWG_I1`-`AWG_I5` | I1 = QUIC Initial (2.0+) | Signature packets in [tag syntax](#signature-packets-i1-i5). I2-I5 need I1 |
| `AWG_HEADER_PROTECTION_KEY` | Generated (3.x) | Base64 32-byte key. Needs S1-S4 ≥ 12 |
| `AWG_CONTENT_PADDING` | `0` with trailers, else `lo-hi` in 16-128 (3.x) | Must be `0` with `RandomTrailers`. Use `0`: padding costs about 22% of download speed |
| `AWG_REKEY_AFTER_TIME`, `AWG_REKEY_TIMEOUT`, `AWG_REJECT_AFTER_TIME`, `AWG_KEEPALIVE_TIMEOUT`, `AWG_MAX_HANDSHAKE_ATTEMPTS` | Random `lo-hi` ranges (3.x) | Each end selects its own value |
| `AWG_RANDOM_TRAILERS` | `on` under 3.1, else `off` | `on`/`off`. Not with 1.5. Needs `S1 = S2 = S3 = S4`, which the container draws automatically |
| `AWG_DISABLE_COOKIES` | `off` | `on`/`off`, any version. Removes the WireGuard DoS protection |

The server and all peers must use the same version and the same S, H, I, `HeaderProtectionKey` and `RandomTrailers` values. `Jc`, `Jmin`, `Jmax`, `ContentPaddingAddition`, the 3.x timers and `DisableCookies` can be different on each end. For the 3.1 switches, only `on` writes a key. For kernel modules older than 3.1, see [Kernel module](#kernel-module).

### Signature packets (I1-I5)

Tags: `<b 0xHEX>` static bytes, `<r N>` random bytes, `<rd N>` random digits, `<rc N>` random letters, `<t>` a timestamp. [AmneziaWG Architect](https://architect.vai-rice.space/) makes signatures that look like QUIC, DNS, DTLS, SIP and other protocols. Some third-party parsers, such as Keenetic, reject a tag larger than 1000 bytes. See [the `<r N>` size](.github/skills/amneziawg-config/references/parameters.md#r-n-size).

## Kernel module

The bundled userspace `amneziawg-go` works without a kernel module. For better throughput, install the [AmneziaWG kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) on the host. The container detects it at start-up and logs `AmneziaWG kernel module is active` or `using userspace amneziawg-go`.

- The container never calls `modprobe`, so it does not need `SYS_MODULE`. Keep `SYS_MODULE` only on hosts that do not load the iptables NAT modules automatically.
- The kernel datapath applies only the options that the module knows. A module older than 3.1 rejects `RandomTrailers` and `DisableCookies` with `Unable to modify interface: Invalid argument`. The container gives a warning if it can read `/sys/module/amneziawg/version`.
- With `RandomTrailers`, use a module built from upstream `4569c4c6` (2026-09-06) or newer. Older 3.1 modules send some oversized handshake packets. The version string does not show the fix. See [how to check](docs/awg-performance.md#checking-whether-your-module-has-the-fix).

## DNS (dnsmasq)

Each peer config gets `DNS = <subnet>.1`, the server end of the tunnel. dnsmasq answers there and forwards to `dns.upstream`, to the first upstream that answers. It caches, but it does not recurse or check DNSSEC, and it sends plain DNS. For encrypted lookups with DNSSEC, use [Unbound on the host](#recommended-unbound-on-the-host).

dnsmasq runs as the unprivileged `dnsmasq` user. It answers only queries that come through the tunnel (`interface=wg0`), plus loopback in the container. Do not publish port 53.

`awgctl` writes the full dnsmasq config to `/run/dnsmasq/dnsmasq.conf` at each start. There are no templates and no custom dnsmasq options. To change the upstreams, run `awgctl set dns.upstream 9.9.9.9,149.112.112.112`, then restart the container. `awgctl` tests the new config with `dnsmasq --test` before it saves it. The peer configs do not change.

## Recommended: Unbound on the host

Let dnsmasq forward to Unbound on the host. Peer lookups then leave the server over DNS-over-TLS with DNSSEC, and the Unbound cache serves the host too. Example for Debian/Ubuntu:

1. Give the stack a fixed network, so that the host has a stable address on it. On a new volume, set `DNS_UPSTREAM` to that address:

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

   On an existing server, add only the network. After step 4, run `docker exec amneziawg awgctl set dns.upstream 172.31.53.1` and `docker restart amneziawg`.

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

A new server uses `MTU = 1280` for the server (`net.mtu`) and for each peer config (`net.peer_mtu`). Use it for mobile, PPPoE and unknown paths. With `none`, `awg-quick` uses 1420 with no allowance for `S4`. Full-size packets then fragment when `S4 > 20` or the endpoint is IPv6. The tunnels connect but are very slow. [docs/mtu.md](docs/mtu.md) tells how to change the values and explains the numbers.

## Health check and troubleshooting

The Docker health check is `awgctl health`. The container is `healthy` while these conditions are true:

- `wg0` is up. A server with no peers is healthy.
- dnsmasq runs and answers on the tunnel address.

The health check does not test the DNS upstream. A failed tunnel gets two more attempts. Docker does not restart unhealthy containers, so use the status for monitoring or with a tool such as autoheal.

```bash
docker logs amneziawg
docker exec amneziawg awgctl health          # healthy or not, and why
docker exec amneziawg awgctl peer list       # peers, handshakes, traffic
docker exec amneziawg awg show
docker exec amneziawg awgctl version
docker exec amneziawg nslookup example.com 10.13.13.1   # test the upstream
```

| Symptom | Fix |
|---|---|
| Exit code 1 at start, log says `holds files of an older release` | Import the old volume. See [Upgrading](#from-the-releases-before-awgctl) |
| Exit code 1 at start, log says `public IPv4 address could not be detected` | Set `SERVERURL` and start again |
| Exit code 1 at start, log says `is not a numeric ID` or `(root) is not supported` | Set `PUID`/`PGID` to the owner of `./config` (`id -u`, `id -g`), not to 0 |
| A variable change has no effect | The variables apply only on the first start. Use `awgctl`, then restart the container |
| An `awgctl` change has no effect | Restart the container |
| Custom `SERVERPORT` cannot be reached | Map `SERVERPORT:51820/udp` |
| Amnezia app shows AWG 1.5 | H1-H4 are integers. Run `awgctl awg version 2.0 --yes`, or select a 3.x version |
| Connection fails after a parameter change | Give the peers their new configs (`awgctl peer export <id> --qr`) |
| An old app cannot connect to the 3.1 default | Use an app with AWG 3.1 support, or run `awgctl awg version 2.0 --yes` |
| Peers get no DNS | Run `awgctl health`. If dnsmasq answers but names do not resolve, the container cannot reach `dns.upstream` |
| Connects and pings, but downloads are very slow | Fragmentation. See [Performance and MTU](#performance-and-mtu) |
| The host firewall gets new rules | Remove `network_mode: host`. See [Requirements](#requirements) |

## Backup and restore

`/config/amneziawg.db` holds every key and peer, so one copy is a full backup. The copy holds every private key. Keep it private.

- **Backup**: stop the container, copy `/config/amneziawg.db`, and start the container again.
- **Restore**: stop the container, and put the file back as `/config/amneziawg.db`. If `/config/amneziawg.db-journal` exists, remove it. Then start the container.

The database schema has no upgrade path. If a new image changes the schema, its release notes tell how to create the database again.

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
- nerdctl runs `HEALTHCHECK` only through systemd timers, so on OpenRC the status does not change. Run `nerdctl exec amneziawg awgctl health` from your monitoring.
- containerd applies `restart: unless-stopped`, so it must start at boot (the `rc-update` line).

## Upgrading

Each release has the tag `<amneziawg-tools>-r<N>` (for example `3.1.20260812-r14`) and [release notes](https://github.com/lqflqf/docker-amneziawg/releases).

### From the releases before `awgctl`

The older releases kept their config in `.donoteditthisfile`, `wg_confs/`, `server/`, `peer*/` and `templates/`. If `/config` has these files and no database, the container stops with exit code 1 and an `import-legacy` hint. Import the volume once:

1. Stop the container: `docker compose down`.
2. Read the import report. This step changes nothing:

   ```bash
   docker run --rm --entrypoint awgctl -v ./config:/config ghcr.io/lqflqf/docker-amneziawg:latest import-legacy /config --dry-run
   ```

3. Run the same command without `--dry-run`.
4. Remove the old variables from `docker-compose.yml`, for example `PEERS` and the `src_valid_mark` sysctl.
5. Start the container: `docker compose up -d`.

The import keeps the server key, the peer keys, the addresses, the AWG version and the AWG values. It does not read the environment. For each peer, it compares the new export with the old peer config:

- `same`: the old config on the device continues to work.
- `changed`: the report names the keys. Export the peer config again.
- `unverifiable`: there is no old peer config to compare.

An old `peer_laptop` becomes `laptop`, and `peer1` stays `peer1`. Each peer keeps its old address, and the last octet of the address becomes its id. If two old names give the same new name, add `--rename OLD=NEW`. Custom `PostUp`/`PostDown` lines, custom dnsmasq options and site-to-site routes stop the import. To import without them, add `--force-partial`. The import moves the old files to `/config/legacy-<timestamp>/`. It does not import `removed_peers/`.

Breaking changes:

1. The environment applies only on the first start. Use `awgctl` for later changes. The container ignores `PEERS`, `PERSISTENTKEEPALIVE_PEERS`, `LOG_CONFS`, `PEERDNS`, `USE_DNS` and `HEALTHCHECK_DNS_NAME`.
2. `/config` holds only `amneziawg.db` and, after an import, `legacy-<timestamp>/`.
3. `awgctl peer export` replaces `show-peer`, the peer files and `LOG_CONFS`.
4. `awgctl peer add` replaces `PEERS` and `PERSISTENTKEEPALIVE_PEERS`. A new server has no peers.
5. Client mode, site-to-site routes (`SERVER_ALLOWEDIPS_PEER_*`), templates, custom firewall rules and custom dnsmasq options are not available.
6. The default AWG version is 3.1 (it was 2.0). An imported volume keeps its version.
7. Invalid AWG combinations are errors, not warnings. `awg version` sets `RandomTrailers` from the version.
8. The container detects the public IP only on the first start.
9. A fatal error at start stops the container with exit code 1. A failed tunnel no longer removes the default routes.
10. dnsmasq runs as the `dnsmasq` user. `awgctl health` replaces `/app/healthcheck`.

### From AYastrebov/docker-amneziawg

Change `image:`, then import the volume as above. The main difference is DNS: this fork uses dnsmasq instead of CoreDNS, and only peers can query it through the tunnel. `USE_COREDNS` and `PEERDNS` have no function. The upstream is `dns.upstream`: Cloudflare by default, or a [resolver on the host](#recommended-unbound-on-the-host).

## Security

**Treat write access to `/config` as root access**: the database holds every key, and root processes read it. See [SECURITY.md](SECURITY.md) for the details and for vulnerability reports.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Credits

This project is a fork of [AYastrebov/docker-amneziawg](https://github.com/AYastrebov/docker-amneziawg). [Andrey Yastrebov](https://github.com/AYastrebov) created it and wrote the container design, the config generation and most of the AWG work. Many thanks to him for the work and for its release under the MIT license. The project also builds on [LinuxServer docker-wireguard](https://github.com/linuxserver/docker-wireguard) and on the `amneziawg-go`, `amneziawg-tools` and kernel module of the [AmneziaVPN](https://github.com/amnezia-vpn) team.

## License

MIT, see [LICENSE](LICENSE). The original copyright notice stays, because the license requires it.

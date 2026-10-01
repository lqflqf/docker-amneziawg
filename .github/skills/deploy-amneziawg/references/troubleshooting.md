# Deployment troubleshooting

Start with `docker compose logs --tail=200` and `docker exec amneziawg /app/healthcheck` (prints why the container is unhealthy). README "Health check and troubleshooting" lists the common symptoms; parameter-level problems (slow tunnel, version errors, Keenetic `invalid I1 value`, "AWG 1.5" in the app) are in the `amneziawg-config` skill's `references/troubleshooting.md`.

## Container does not start

| Error | Fix |
|---|---|
| `error gathering device information … /dev/net/tun` | TUN missing on the host — `references/system-setup.md` "TUN device"; LXC/OpenVZ plans may not support it at all |
| `port is already allocated` | `sudo ss -lunp 'sport = :51820'`; stop the other process or change `SERVERPORT` |
| `RTNETLINK answers: Operation not permitted` | `NET_ADMIN` missing from `cap_add` |

## Container runs but is `unhealthy`

| Log line / healthcheck output | Cause and fix |
|---|---|
| `Config generation failed` | The preceding line names the bad input (duplicate peers, more than 253 peers, invalid `SERVERURL`/`SERVERPORT`/`INTERNAL_SUBNET`, `SERVERURL=auto` detection failed on a fresh install, S < 12 under 3.x, broken template). Previous configs stay in use; fix and `docker compose up -d` |
| `Peer … contains non-alphanumeric characters and thus will be skipped` | Rename the peer in `PEERS` to letters and digits only |
| `Tunnel /config/wg_confs/wg0.conf failed` / `no active tunnels` | Read the `awg-quick` error above it. `Unable to modify interface: Invalid argument` with 3.1 switches = host kernel module < 3.1: upgrade it or set `AWG_RANDOM_TRAILERS=off` and `AWG_DISABLE_COOKIES=off` (removing the variables reuses the saved value) |
| `unbound does not answer …` / `… unbound.conf is invalid` | `docker exec amneziawg unbound-checkconf /config/unbound/unbound.conf`; delete the file to restore the default |

## Peer connects but nothing works

- **No handshake** (`awg show` has no `latest handshake`): the cloud firewall is the usual cause; then check the client's `Endpoint` matches `SERVERURL:SERVERPORT` and the client supports the AWG version. `sudo tcpdump -ni any udp port 51820` on the host shows whether packets arrive.
- **Handshake but no internet**: `sysctl net.ipv4.ip_forward` on the host must be `1`; `docker exec amneziawg iptables -t nat -S POSTROUTING` must show the `MASQUERADE` rule (missing means a custom template dropped the `PostUp` lines). With `network_mode: host` the rule only matches `eth*` uplinks — see `system-setup.md` "Firewall".
- **No DNS**: the log explains why Unbound is off — `Disabling unbound` (`USE_DNS=false` or client mode) or `Port 53 is already in use` (often `network_mode: host` with systemd-resolved). Set `PEERDNS=1.1.1.1` or free the port. `REFUSED` for peers means a custom `unbound.conf` lacks `access-control` for the VPN subnet. Migrating from AYastrebov's image: rename `USE_COREDNS` to `USE_DNS`.
- **Mobile data fails, Wi-Fi works**: try `SERVERPORT=443` (map `443:51820/udp`) or a port ≤ 9999, and `PERSISTENTKEEPALIVE_PEERS` for that peer.

## Regenerating

Configs regenerate automatically when an env var, `AWG_*` value or template changes. To redraw only the obfuscation values:

```bash
docker compose down && sudo rm <deploy-dir>/config/server/awg_params && docker compose up -d
```

Set `AWG_VERSION` explicitly first if you use a non-default version — the saved version is deleted too. Every peer then needs its new conf (`/app/show-peer`). Deleting `config/server/` entirely also replaces the server keypair.

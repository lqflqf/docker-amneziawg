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
| `Config generation failed` | The preceding line names the bad input (duplicate peers, more than 253 peers, invalid `SERVERURL`/`SERVERPORT`/`INTERNAL_SUBNET`, public IPv4 detection (`SERVERURL` unset) failed on a fresh install, S < 12 under 3.x, broken template). Previous configs stay in use; fix and `docker compose up -d` |
| `Peer … contains non-alphanumeric characters and thus will be skipped` | Rename the peer in `PEERS` to letters and digits only |
| `Tunnel /config/wg_confs/wg0.conf failed` / `no active tunnels` | Read the `awg-quick` error above it. `Unable to modify interface: Invalid argument` with 3.1 switches = host kernel module < 3.1: upgrade it or set `AWG_RANDOM_TRAILERS=off` and `AWG_DISABLE_COOKIES=off` (removing the variables reuses the saved value) |
| `dnsmasq has no valid config` / `dnsmasq does not answer …` | The log shows `DNS config generation failed` (bad `DNS_UPSTREAM` or template) or `No valid dnsmasq config` (a hand edit dnsmasq rejects). Fix `config/templates/dnsmasq.conf` or `DNS_UPSTREAM`, or delete `config/dnsmasq/dnsmasq.conf` to render it again |

## Peer connects but nothing works

- **No handshake** (`awg show` has no `latest handshake`): the cloud firewall is the usual cause; then check the client's `Endpoint` matches `SERVERURL:SERVERPORT` and the client supports the AWG version. `sudo tcpdump -ni any udp port 51820` on the host shows whether packets arrive.
- **Handshake but no internet**: `sysctl net.ipv4.ip_forward` on the host must be `1`; `docker exec amneziawg iptables -t nat -S POSTROUTING` must show the `MASQUERADE` rule (missing means a custom template dropped the `PostUp` lines). With `network_mode: host` the rule only matches `eth*` uplinks — see `system-setup.md` "Firewall".
- **No DNS**: peers always use dnsmasq at `<subnet>.1`; run `/app/healthcheck` and see the dnsmasq rows above. A healthy container does not prove the upstream works: `docker exec amneziawg nslookup example.com <DNS_UPSTREAM address>`; for Unbound on the host, check its `interface`/`access-control` and the host-firewall rule for the bridge. With `network_mode: host`, a host resolver bound to `0.0.0.0:53` takes the port first; bind it to specific addresses instead. Upgrading from an Unbound release or AYastrebov's image: `PEERDNS`, `USE_DNS` and `USE_COREDNS` are ignored (README "DNS").
- **Mobile data fails, Wi-Fi works**: try `SERVERPORT=443` (map `443:51820/udp`) or a port ≤ 9999, and make sure `PERSISTENTKEEPALIVE_PEERS` (default `all`) includes that peer.

## Regenerating

Configs regenerate automatically when an env var, `AWG_*` value or template changes. To redraw only the obfuscation values:

```bash
docker compose down && sudo rm <deploy-dir>/config/server/awg_params && docker compose up -d
```

Set `AWG_VERSION` explicitly first if you use a non-default version — the saved version is deleted too. Every peer then needs its new conf (`/app/show-peer`). Deleting `config/server/` entirely also replaces the server keypair.

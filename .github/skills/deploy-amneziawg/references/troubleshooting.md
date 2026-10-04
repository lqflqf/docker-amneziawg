# Deployment troubleshooting

Start with `docker compose logs --tail=200` and `docker exec amneziawg /app/healthcheck`. The healthcheck prints why the container is unhealthy. README "Health check and troubleshooting" lists common symptoms. Parameter-level problems are in the `amneziawg-config` skill's `references/troubleshooting.md`. These include slow tunnels, version errors, Keenetic `invalid I1 value`, and "AWG 1.5" in the app.

## Container does not start

| Error | Fix |
|---|---|
| `error gathering device information … /dev/net/tun` | TUN is missing on the host. See `references/system-setup.md` "TUN device". LXC/OpenVZ plans may not support it |
| `port is already allocated` | Run `sudo ss -lunp 'sport = :51820'`. Stop the other process or change `SERVERPORT` |
| `RTNETLINK answers: Operation not permitted` | Add `NET_ADMIN` to `cap_add` |

## Container runs but is `unhealthy`

| Log line / healthcheck output | Cause and fix |
|---|---|
| `Config generation failed` | The preceding line names the bad input. Examples are duplicate peers, more than 253 peers, invalid `SERVERURL`/`SERVERPORT`/`INTERNAL_SUBNET`, failed public IPv4 detection with `SERVERURL` unset on a fresh install, S < 12 under 3.x, and a broken template. Previous configs stay in use. Fix the input and run `docker compose up -d` |
| `Peer … contains non-alphanumeric characters and thus will be skipped` | Rename the peer in `PEERS`. Use letters and digits only |
| `Tunnel /config/wg_confs/wg0.conf failed` / `no active tunnels` | Read the `awg-quick` error above it. `Unable to modify interface: Invalid argument` with 3.1 switches means the host kernel module is older than 3.1. Upgrade it, or set `AWG_RANDOM_TRAILERS=off` and `AWG_DISABLE_COOKIES=off`. If you remove the variables, the saved value is reused |
| `dnsmasq has no valid config` / `dnsmasq does not answer …` | The log shows `DNS config generation failed` for bad `DNS_UPSTREAM` or a bad template. It shows `No valid dnsmasq config` when dnsmasq rejects a hand edit. Fix `config/templates/dnsmasq.conf` or `DNS_UPSTREAM`. Or delete `config/dnsmasq/dnsmasq.conf` to render it again |

## Peer connects but nothing works

- **No handshake** (`awg show` has no `latest handshake`): the cloud firewall is the usual cause. Then check that the client `Endpoint` matches `SERVERURL:SERVERPORT`. Also check that the client supports the AWG version. Run `sudo tcpdump -ni any udp port 51820` on the host to see if packets arrive.
- **Handshake but no internet**: `sysctl net.ipv4.ip_forward` on the host must be `1`. `docker exec amneziawg iptables -t nat -S POSTROUTING` must show the `MASQUERADE` rule. If it is missing, a custom template dropped the `PostUp` lines. With `network_mode: host`, the rule only matches `eth*` uplinks. See `system-setup.md` "Firewall".
- **No DNS**: peers always use dnsmasq at `<subnet>.1`. Run `/app/healthcheck` and see the dnsmasq rows above. A healthy container does not prove that the upstream works. Run `docker exec amneziawg nslookup example.com <DNS_UPSTREAM address>`. For Unbound on the host, check its `interface` and `access-control`. Also check the host-firewall rule for the bridge. With `network_mode: host`, a host resolver bound to `0.0.0.0:53` takes the port first. Bind it to specific addresses instead. After upgrade from an Unbound release or AYastrebov's image, `PEERDNS`, `USE_DNS`, and `USE_COREDNS` are ignored. See README "DNS".
- **Mobile data fails, Wi-Fi works**: try `SERVERPORT=443`, with map `443:51820/udp`. Or use a port ≤ 9999. Make sure `PERSISTENTKEEPALIVE_PEERS`, default `all`, includes that peer.

## Regenerating

Configs regenerate automatically when an env var, `AWG_*` value, or template changes. To redraw only the obfuscation values:

```bash
docker compose down && sudo rm <deploy-dir>/config/server/awg_params && docker compose up -d
```

Set `AWG_VERSION` explicitly first if you use a non-default version. The saved version is deleted too. Then give each peer its new conf (`/app/show-peer`). If you delete `config/server/` entirely, the server keypair is also replaced.

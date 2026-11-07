# Deployment troubleshooting

Start with `docker compose logs --tail=200` and `docker exec amneziawg awgctl health`. The health check prints why the container is unhealthy. `docker exec amneziawg awgctl peer list` shows the peers and their handshakes. README "Health check and troubleshooting" lists common symptoms. Parameter-level problems are in the `amneziawg-config` skill's `references/troubleshooting.md`. These include slow tunnels, version errors, Keenetic `invalid I1 value`, and "AWG 1.5" in the app.

## Container does not start

| Error | Fix |
|---|---|
| `error gathering device information … /dev/net/tun` | TUN is missing on the host. See `references/system-setup.md` "TUN device". LXC/OpenVZ plans may not support it |
| `port is already allocated` | Run `sudo ss -lunp 'sport = :51820'`. Stop the other process or change the published port |
| `RTNETLINK answers: Operation not permitted` | Add `NET_ADMIN` to `cap_add` |

## Container stops with exit code 1

`awgctl init` stops the container on a fatal error. The restart policy starts it again, and the same error comes back until you fix it. Read the `ERROR` line before `The container is stopping`.

| Log line | Cause and fix |
|---|---|
| `/config holds files of an older release (...) and no database` | The volume comes from a release before `awgctl`. Import it once with `awgctl import-legacy`, as in README "From the releases before `awgctl`". For a new server, use an empty `config/` directory |
| `SERVERURL is not set and the public IPv4 address could not be detected` | The host cannot reach the detection services over HTTPS. Set `SERVERURL` and run `docker compose up -d` again. The container wrote nothing |
| `SERVERURL=auto is not supported` | Remove the line, or set a DNS name or IP |
| `<VARIABLE>: ...` or `the AWG_* values are not valid` | A first-start value breaks a rule. Fix the variable in the compose file and start again. The database does not exist yet |
| `PUID=0 (root) is not supported` or `is not a numeric ID` | Set `PUID`/`PGID` to the owner of `./config`, for example `id -u` and `id -g` |
| `... is a symlink; refusing to follow it` or `is owned by uid` | Replace the symlink with the real file or directory. Give `config/` to the `PUID` user |

## Container runs but is `unhealthy`

| `awgctl health` output / log line | Cause and fix |
|---|---|
| `tunnel wg0 is down` | Read the `awg-quick` error above `Tunnel wg0 is down` in the log. `Unable to modify interface: Invalid argument` with 3.1 switches means the host kernel module is older than 3.1. Upgrade it, or run `awgctl awg set random_trailers=off disable_cookies=off --yes` and restart |
| `dnsmasq has no valid config` / `dnsmasq does not answer …` | Run `awgctl get dns.upstream`. Set a reachable IPv4/IPv6 upstream with `awgctl set dns.upstream <addr>`, then restart |

## Peer connects but nothing works

- **No handshake** (`awgctl peer list` shows no handshake): the cloud firewall is the usual cause. Then check that the client `Endpoint` matches `awgctl get server.endpoint_host` and `server.endpoint_port`. Also check that the client supports the AWG version (`awgctl awg show`). Run `sudo tcpdump -ni any udp port 51820` on the host to see if packets arrive.
- **Handshake but no internet**: `sysctl net.ipv4.ip_forward` on the host must be `1`. `docker exec amneziawg iptables -t nat -S POSTROUTING` must show the `MASQUERADE` rule. If the container uses an interface other than `eth*`, run `awgctl set fw.nat_interface <name>` and restart. If the compose file has `network_mode: host`, remove it. See `system-setup.md` "Firewall".
- **No DNS**: peers always use dnsmasq at `<subnet>.1`. Run `awgctl health` and see the dnsmasq rows above. A healthy container does not prove that the upstream works. Run `docker exec amneziawg nslookup example.com <upstream address>`. For Unbound on the host, check its `interface` and `access-control`. Also check the host-firewall rule for the bridge.
- **Mobile data fails, Wi-Fi works**: publish `443:51820/udp` and run `awgctl set server.endpoint_port 443 --yes`. Or use a port ≤ 9999. Then restart the container and export each peer conf again. Make sure that the peer has keepalive on (`awgctl peer set <id> --keepalive on`, then restart).
- **The app cannot connect after the first start**: the default AWG version is 3.1. For an older app, run `awgctl awg version 2.0 --yes`, restart, and export each peer conf again.

## Changing values after the first start

The container reads the environment only on the first start. It ignores a changed variable. Use `awgctl`, then restart the container:

```bash
docker exec amneziawg awgctl set server.endpoint_host vpn.example.com --yes
docker exec amneziawg awgctl awg regenerate --yes      # new obfuscation values
docker restart amneziawg                               # applies each saved change
docker exec -it amneziawg awgctl peer export <id> --qr
```

`awgctl` changes only the database. Nothing changes in the running container until the restart. After a change to a shared value, give each peer its new conf.

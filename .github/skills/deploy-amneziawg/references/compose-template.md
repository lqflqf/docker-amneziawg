# docker-compose.yml template

Fill the `<<…>>` placeholders from Phases 3-4. `README.md` "Parameters" describes every variable. The repository `docker-compose.yml` shows all optional variables and a client-mode example.

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
      - PUID=<<uid owning ./config, e.g. 1000>>
      - PGID=<<gid, e.g. 1000>>
      - TZ=<<IANA zone, e.g. Etc/UTC>>
      - SERVERURL=<<vpn.example.com; omit the line to detect the public IPv4>>
      - SERVERPORT=<<port, default 51820>>
      - PEERS=<<count or names, e.g. laptop,phone>>
      # Optional — omit to keep the defaults:
      # - DNS_UPSTREAM=1.1.1.1,1.0.0.1   # or the host Unbound's address, README "Recommended: Unbound on the host"
      # - INTERNAL_SUBNET=10.13.13.0
      # - ALLOWEDIPS=0.0.0.0/0, ::/0
      # - PERSISTENTKEEPALIVE_PEERS=all   # all | comma-separated peers | none
      # - SERVER_ALLOWEDIPS_PEER_<<name>>=192.168.1.0/24
      # - AWG_VERSION=2.0          # 2.0 | 3.0 | 3.1 | 1.5
      # - AWG_DISABLE_COOKIES=on
      # Pinned AWG_* values (only if Phase 4 chose to pin) go here, from
      # awg-genconf.sh --params-only --format compose
    volumes:
      - ./config:/config
    ports:
      - "<<SERVERPORT>>:51820/udp"
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv4.conf.all.src_valid_mark=1
      - net.ipv6.conf.all.disable_ipv6=0
    restart: unless-stopped
```

## Rules

- **Port**: the container always listens on 51820. A custom `SERVERPORT=32948` maps as `"32948:51820/udp"`. Never map it as `"32948:32948/udp"`.
- **Capabilities**: use `NET_ADMIN` only. `SYS_MODULE` is not necessary. The container never runs `modprobe`. Keep it only on minimal hosts that do not auto-load the iptables NAT modules. A `/lib/modules` mount does nothing.
- **`PUID`/`PGID`**: use `id -u` and `id -g` of the user that owns the deploy directory. Recommend a non-root user instead of `0`.
- **`PEERS`**: use letters and digits only. Names with `-`, `_`, or spaces are skipped with a log message. A single number means a count (`3` → `peer1`…`peer3`).
- **`SERVER_ALLOWEDIPS_PEER_<x>`**: `<x>` is the name as written in `PEERS`, or the number.
- **`LOG_CONFS`**: leave it unset. QR codes contain private keys. Use `/app/show-peer`.

# docker-compose.yml template

Fill the `<<…>>` placeholders from Phases 3-4. `README.md` "First start" describes every variable. The repository `docker-compose.yml` shows all optional variables. The container reads them only on the first start, when `config/` has no database.

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
      - SERVERURL=<<vpn.example.com, recommended. Omit the line only to detect the public IPv4 once>>
      - SERVERPORT=<<port, default 51820>>
      # Optional, first start only. Omit to keep the defaults:
      # - DNS_UPSTREAM=1.1.1.1,1.0.0.1   # or the host Unbound's address, README "Recommended: Unbound on the host"
      # - INTERNAL_SUBNET=10.13.13.0/24
      # - ALLOWEDIPS=0.0.0.0/0, ::/0
      # - MTU=1280
      # - AWG_VERSION=3.1          # 3.1 | 3.0 | 2.0 | 1.5
      # - AWG_DISABLE_COOKIES=on
      # Pinned AWG_* values (only if Phase 4 chose to pin) go here, from
      # awg-genconf.sh --params-only --format compose
    volumes:
      - ./config:/config
    ports:
      - "<<SERVERPORT>>:51820/udp"
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv6.conf.all.disable_ipv6=0
    restart: unless-stopped
```

## Rules

- **Port**: the container always listens on 51820. A custom `SERVERPORT=32948` maps as `"32948:51820/udp"`. Never map it as `"32948:32948/udp"`.
- **Capabilities**: use `NET_ADMIN` only. `SYS_MODULE` is not necessary. The container never runs `modprobe`. Keep it only on minimal hosts that do not auto-load the iptables NAT modules. A `/lib/modules` mount does nothing.
- **`PUID`/`PGID`**: use `id -u` and `id -g` of the user that owns the deploy directory. `0` (root) stops the container.
- **Peers**: do not put peers in the environment. The container ignores `PEERS`. Add each peer with `awgctl peer add --name <name>` after the start, then restart the container.
- **`restart`**: keep `unless-stopped`. A fatal error at start stops the container with exit code 1, and the policy starts it again.
- **Changes after the first start**: edit the compose file only for `PUID`, `PGID`, ports and sysctls. Change other values with `awgctl` (README "Settings").

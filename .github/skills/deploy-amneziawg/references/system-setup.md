# System setup

Run each block on the target host (over SSH if remote).

## Docker

```bash
# Debian / Ubuntu / Raspbian (Engine + CLI + Compose plugin)
curl -fsSL https://get.docker.com | sh

# Fedora (use .../linux/centos/docker-ce.repo on RHEL / Rocky / Alma)
sudo dnf -y install dnf-plugins-core
sudo dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
sudo dnf -y install docker-ce docker-ce-cli containerd.io docker-compose-plugin

# Arch / Manjaro
sudo pacman -Syu --noconfirm docker docker-compose

# Alpine (OpenRC)
sudo apk add --no-cache docker docker-cli-compose && sudo rc-update add docker default && sudo service docker start

# openSUSE
sudo zypper install -y docker docker-compose
```

Then `sudo systemctl enable --now docker` (systemd distros) and `sudo usermod -aG docker $USER` (`addgroup` on Alpine). Group membership needs a new login (`newgrp docker` for the current shell). Verify with `docker compose version && docker run --rm hello-world`.

## Sysctl

```bash
echo 'net.ipv4.ip_forward=1' | sudo tee /etc/sysctl.d/99-amneziawg.conf
sudo sysctl --system
```

## Firewall

Open UDP `SERVERPORT` (default 51820). With the default bridge network no NAT rule is needed on the host: the server conf's `PostUp` masquerades out of the container's `eth+` interface, and Docker's bridge NAT does the rest. With `network_mode: host` that rule applies to the host's interfaces, so it only works if the uplink is named `eth*`; on `ens*`/`enp*` hosts add a `PostUp` masquerade for the real uplink in `/config/templates/server.conf` (or a host rule for the VPN subnet).

```bash
sudo ufw allow 51820/udp            # ufw: allow SSH first before any `ufw enable`, or you lock yourself out
sudo firewall-cmd --permanent --add-port=51820/udp && sudo firewall-cmd --reload   # firewalld
sudo nft add rule inet filter input udp dport 51820 accept                         # nftables (persist in /etc/nftables.conf)
sudo iptables -A INPUT -p udp --dport 51820 -j ACCEPT                              # iptables (persist with iptables-persistent)
```

Docker publishes ports through its own chains, so a host firewall that is closed may still pass traffic — open it anyway for clarity and for `network_mode: host`.

**Cloud firewalls are separate and usually the real blocker.** Always remind the user to allow inbound UDP on the port in AWS Security Groups, GCP VPC firewall, Hetzner Cloud Firewalls, DigitalOcean Cloud Firewalls, or their provider's equivalent.

## TUN device

```bash
sudo modprobe tun && echo tun | sudo tee /etc/modules-load.d/tun.conf
ls -l /dev/net/tun        # crw-rw-rw- … 10, 200
```

`modprobe tun: Operation not permitted` means an LXC/OpenVZ plan without TUN support: the user must ask the provider to enable it or move to a KVM plan.

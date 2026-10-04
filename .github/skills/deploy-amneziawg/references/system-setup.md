# System setup

Run each block on the target host. Use SSH if the host is remote.

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

Then run `sudo systemctl enable --now docker` on systemd distros. Run `sudo usermod -aG docker $USER`, or `addgroup` on Alpine. Group membership needs a new login. Use `newgrp docker` for the current shell. Verify with `docker compose version && docker run --rm hello-world`.

## Sysctl

```bash
echo 'net.ipv4.ip_forward=1' | sudo tee /etc/sysctl.d/99-amneziawg.conf
sudo sysctl --system
```

## Firewall

Open UDP `SERVERPORT`, which defaults to 51820. With the default bridge network, no NAT rule is necessary on the host. The server conf `PostUp` masquerades out of the container `eth+` interface. Docker bridge NAT then handles the rest. With `network_mode: host`, that rule applies to host interfaces. It works only if the uplink is named `eth*`. On `ens*` or `enp*` hosts, add a `PostUp` masquerade for the real uplink in `/config/templates/server.conf`. Alternatively, add a host rule for the VPN subnet.

```bash
sudo ufw allow 51820/udp            # ufw: allow SSH first before any `ufw enable`, or you lock yourself out
sudo firewall-cmd --permanent --add-port=51820/udp && sudo firewall-cmd --reload   # firewalld
sudo nft add rule inet filter input udp dport 51820 accept                         # nftables (persist in /etc/nftables.conf)
sudo iptables -A INPUT -p udp --dport 51820 -j ACCEPT                              # iptables (persist with iptables-persistent)
```

Docker publishes ports through its own chains. Thus, a closed host firewall can still pass traffic. Open it anyway for clarity and for `network_mode: host`.

**Cloud firewalls are separate and usually the real blocker.** Always remind the user to allow inbound UDP on the port. Do this in AWS Security Groups, GCP VPC firewall, Hetzner Cloud Firewalls, DigitalOcean Cloud Firewalls, or the provider equivalent.

## TUN device

```bash
sudo modprobe tun && echo tun | sudo tee /etc/modules-load.d/tun.conf
ls -l /dev/net/tun        # crw-rw-rw- … 10, 200
```

`modprobe tun: Operation not permitted` means an LXC/OpenVZ plan without TUN support. The user must ask the provider to enable it or move to a KVM plan.

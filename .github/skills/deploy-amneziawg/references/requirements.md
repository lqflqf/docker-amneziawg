# Requirements policy

`scripts/check-requirements.sh` implements the checks. This file gives the judgment rules for those checks.

## Distros

| `ID` (`/etc/os-release`) | Versions | Docker install |
|---|---|---|
| `ubuntu`, `debian` | current LTS / stable | `get.docker.com` |
| `fedora` | 38+ | Docker CE repo + `dnf` |
| `rocky`, `almalinux`, `centos` | 8, 9 | Docker CE repo + `dnf` |
| `arch`, `manjaro` | rolling | `pacman` |
| `alpine` | 3.18+ | `apk` (minimal toolchain — warn) |
| `opensuse-*` | recent | `zypper` |

- **CoreOS / Flatcar / Bottlerocket**: Docker is preinstalled on an immutable root. Skip the install step.
- **Synology / OpenWRT / TrueNAS**: confirm that the Docker daemon is reachable. Skip installs.
- **Stop** on hosts too old for Compose v2. Also stop on LXC/OpenVZ plans without `/dev/net/tun`. This is a hard blocker. The user needs a KVM plan or provider TUN support.

## Architecture

Use amd64 and arm64 only. Do not use 32-bit ARM. Upstream Amnezia documents only x86_64. This image deliberately adds arm64 support. Mention that fact to arm64 users, such as Hetzner CAX, Ampere, Graviton, and Raspberry Pi 4/5 users.

## Kernel module

The module is optional. Without it, the container uses the bundled userspace `amneziawg-go`. This is acceptable for almost all users. Do not install the module as part of a deployment. DKMS and headers are brittle across distros. If the user asks, point to https://github.com/amnezia-vpn/amneziawg-linux-kernel-module. A plain `wireguard` module is not used.

## Networking

- Expect IPv4 on the host. IPv6-only hosts are not supported upstream.
- Docker sets `net.ipv4.ip_forward=1` when it starts. Persist it in `/etc/sysctl.d/` to keep it after reboots. Compose sets `src_valid_mark` and IPv6 sysctls inside the container. Do not require them on the host.

## Soft checks

Free disk space below 500 MB and memory below 256 MB only cause a warning. The image is about 150 MB. It idles at about 30 MB.

## Summary

Show the script table. List missing items and the proposed actions. These actions can include Docker install, `docker` group membership, sysctl, and port access. Wait for confirmation before you change anything.

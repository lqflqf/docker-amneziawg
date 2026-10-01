# Requirements policy

`scripts/check-requirements.sh` implements the checks; this file covers the judgement calls around them.

## Distros

| `ID` (`/etc/os-release`) | Versions | Docker install |
|---|---|---|
| `ubuntu`, `debian` | current LTS / stable | `get.docker.com` |
| `fedora` | 38+ | Docker CE repo + `dnf` |
| `rocky`, `almalinux`, `centos` | 8, 9 | Docker CE repo + `dnf` |
| `arch`, `manjaro` | rolling | `pacman` |
| `alpine` | 3.18+ | `apk` (minimal toolchain — warn) |
| `opensuse-*` | recent | `zypper` |

- **CoreOS / Flatcar / Bottlerocket**: Docker is preinstalled on an immutable root; skip the install step.
- **Synology / OpenWRT / TrueNAS**: confirm the Docker daemon is reachable, skip installs.
- **Stop** on hosts too old for Compose v2, or LXC/OpenVZ plans without `/dev/net/tun` (a hard blocker — the user needs a KVM plan or the provider to enable TUN).

## Architecture

amd64 and arm64 only (no 32-bit ARM). Upstream Amnezia documents only x86_64; arm64 (Hetzner CAX, Ampere, Graviton, Raspberry Pi 4/5) is a deliberate extension of this image — mention it to arm64 users.

## Kernel module

Optional. Without the AmneziaWG module the container uses the bundled userspace `amneziawg-go`, which is fine for almost everyone. Do not install the module as part of a deployment (DKMS and headers are brittle across distros); if asked, point to https://github.com/amnezia-vpn/amneziawg-linux-kernel-module. A plain `wireguard` module is not used.

## Networking

- IPv4 on the host is expected; IPv6-only hosts are not supported upstream.
- Docker sets `net.ipv4.ip_forward=1` when it starts; persisting it in `/etc/sysctl.d/` keeps it set across reboots. `src_valid_mark` and IPv6 sysctls are set inside the container by compose — do not require them on the host.

## Soft checks

Disk (< 500 MB free) and memory (< 256 MB) only warn; the image is ~150 MB and idles at ~30 MB.

## Summary

Show the script's table, list what is missing and the proposed actions (install Docker, add user to `docker` group, sysctl, open the port), and wait for confirmation before changing anything.

---
name: deploy-amneziawg
description: End-to-end deployment of the docker-amneziawg AmneziaWG VPN container to a VPS or Linux host (Ubuntu/Debian/Fedora/RHEL clones/Arch/Alpine; Hetzner, DigitalOcean, AWS, etc.). Use whenever the user wants to deploy, install, set up or "go live" with this container — including "set this up on my server", "I have a VPS, what now?" or just "deploy". Checks host requirements, installs Docker, opens the firewall, gathers settings, writes docker-compose.yml, starts the stack and hands out peer configs/QR codes.
---

# Deploy docker-amneziawg

Goal: from "I have a VPS" to "my phone is on the VPN via QR code" in one guided session. The user-facing reference for every setting is `README.md`; do not restate it, apply it.

## Behaviour

- You are operating on a real machine. Before each *category* of system change (packages, firewall, sysctls, ports) show the exact commands and confirm.
- Ask for values that matter (`SERVERURL`, port, peer names); default the rest and summarize them before deploying.
- Explain each check or question in one phrase.
- Stop and diagnose on any failure; never continue to later phases pretending it worked.

## Phase 0 — Where am I running?

Decide whether you are on the target host, on a workstation deploying over SSH, or on a workstation deploying locally (check `hostname`, repo checkout, `/etc/cloud/`). If unclear, ask. For remote hosts collect host, user, port and key, and test `ssh … 'echo ok'` first. Below, "run" means on the target host.

## Phase 1 — Requirements

Copy `scripts/check-requirements.sh` to the host and run it (`bash check-requirements.sh <port>`). It prints one OK/WARN/FAIL table covering OS, architecture (amd64/arm64 only), `/dev/net/tun`, the optional AmneziaWG kernel module, Docker, Compose v2, permissions, the UDP port, disk, memory and `ip_forward`. Show the table and the proposed fixes, and wait for confirmation. Distro policy and edge cases: `references/requirements.md`.

## Phase 2 — Install missing pieces

In order, verifying each: Docker + Compose plugin, `net.ipv4.ip_forward=1` persisted on the host, the UDP port in the host firewall **and** the cloud provider's firewall/security group, `/dev/net/tun`. Commands per distro: `references/system-setup.md`. With the default bridge network no host NAT rule is needed — the container masquerades the VPN subnet itself (see `references/system-setup.md` for `network_mode: host`).

## Phase 3 — Settings

Ask in one batch (use the `ask_user` tool with several fields):

| Setting | Default | Notes |
|---|---|---|
| `SERVERURL` | unset | Prefer a DNS name; unset detects the public IPv4 |
| `SERVERPORT` | `51820` | Non-default → map `SERVERPORT:51820/udp`. 443/udp or a port ≤ 9999 helps on carriers that block high UDP ports |
| `PEERS` | — | Prefer names (`laptop,phone` → `peer_laptop`…); letters and digits only — other names are skipped |
| `TZ` | `Etc/UTC` | Log timestamps only |

Optional, only if the user wants them: `INTERNAL_SUBNET` (LAN conflict), `DNS_UPSTREAM` (where the peers' dnsmasq forwarder sends queries; default `1.1.1.1,1.0.0.1`), `ALLOWEDIPS` (split tunnel), `PERSISTENTKEEPALIVE_PEERS` (default `all`; `none` saves phone battery), `SERVER_ALLOWEDIPS_PEER_<name>` (site-to-site). Meanings and defaults: README "Parameters".

Offer encrypted, DNSSEC-validated DNS for peers: Unbound on the host, set up as in README "Recommended: Unbound on the host" (fixed Compose network, Unbound on its gateway only, a host-firewall rule for the bridge, `DNS_UPSTREAM` = the gateway). Never bind it to `0.0.0.0` or a public address.

## Phase 4 — Obfuscation

**Recommend leaving every `AWG_*` unset.** The container generates valid values on first start — including equal `S1`-`S4` and `ContentPaddingAddition = 0` whenever `RandomTrailers` is on — and persists them in `/config/server/awg_params`. Ask only:

1. **`AWG_VERSION`**: `2.0` (default; AmneziaVPN ≥ 4.8.12.9), `3.0`, `3.1` (3.0 + `RandomTrailers`), or `1.5` (legacy clients). Recommend 2.0 unless every client is known to support 3.x — newer modes fail closed. `AWG_DISABLE_COOKIES=on` is a separate opt-in (gives up DoS mitigation).
2. **Kernel datapath + 3.1 switches**: the host module must be ≥ 3.1 (`cat /sys/module/amneziawg/version`), else the tunnel fails with `Unable to modify interface: Invalid argument`. The startup warning is best-effort, so check yourself. The userspace fallback always works.
3. **Pin values?** Only to match an existing deployment or to make the compose file self-contained. Generate them with the `amneziawg-config` skill: `../amneziawg-config/scripts/awg-genconf.sh --params-only --version <v> --format compose`, and validate user-supplied values with its `awg-lint.py`.

Shared values (`S`, `H`, `I`, `HeaderProtectionKey`, `RandomTrailers`) must be identical on every peer; changing any later means redistributing every peer conf.

## Phase 5 — Write compose and start

1. Deploy directory (default `/opt/amneziawg/`, confirm) with a `config/` subdirectory.
2. Write `docker-compose.yml` from `references/compose-template.md`.
3. `docker compose pull && docker compose up -d`.
4. `docker compose logs --tail=100` should show `AmneziaWG kernel module is active` or `using userspace amneziawg-go`, then `Config initialization finished`, `Activating tunnel /config/wg_confs/wg0.conf` and `All tunnels are now active`. `Config generation failed` names the bad input; previous configs are kept.
5. `docker exec amneziawg /app/healthcheck` prints `tunnels up: wg0` (Docker reports `healthy` after its start period). `docker exec amneziawg awg show` lists the peers.

On failure: `references/troubleshooting.md`.

## Phase 6 — Hand off peer configs

`docker exec amneziawg /app/show-peer <name-or-number>` prints a terminal QR code for the Amnezia app. Tell the user:

- Configs live in `<deploy-dir>/config/peer_<name>/peer_<name>.conf` (`peer<N>` for numeric peers). Back up all of `config/` — it holds the server and peer keys.
- Add peers by editing `PEERS` and running `docker compose up -d`; existing peers keep keys and addresses. Removed peers are archived in `config/removed_peers/`.
- Client app: https://amnezia.org/.

## Phase 7 — Verify from a client

Import the conf, connect, and check `curl https://api.ipify.org` shows the server's IP; on the server, `awg show` should show a recent handshake. If it never completes, check the cloud firewall first, then client version support, then the logs.

## Don'ts

- Don't run compose in the foreground or publish 51820 over TCP.
- Don't `chmod 777` `config/`; set `PUID`/`PGID` to the owning user.
- Don't re-randomize `AWG_*` from config management — the values must stay stable.
- Don't skip Phase 1: without `/dev/net/tun` the container starts but no tunnel comes up (it reports `unhealthy` and removes its default routes).

## Files

- `scripts/check-requirements.sh` — Phase 1 checks
- `references/requirements.md` — distro and architecture policy
- `references/system-setup.md` — Docker install, sysctl, firewall, TUN per distro
- `references/compose-template.md` — compose template and filling rules
- `references/troubleshooting.md` — deployment failures

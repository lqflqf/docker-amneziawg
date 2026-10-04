---
name: deploy-amneziawg
description: End-to-end deployment of the docker-amneziawg AmneziaWG VPN container to a VPS or Linux host (Ubuntu/Debian/Fedora/RHEL clones/Arch/Alpine; Hetzner, DigitalOcean, AWS, etc.). Use whenever the user wants to deploy, install, set up or "go live" with this container — including "set this up on my server", "I have a VPS, what now?" or just "deploy". Checks host requirements, installs Docker, opens the firewall, gathers settings, writes docker-compose.yml, starts the stack and hands out peer configs/QR codes.
---

# Deploy docker-amneziawg

Goal: guide the user from a VPS to a phone on the VPN by QR code. The user reference for each setting is `README.md`. Do not restate it; apply it.

## Behaviour

- You are on a real machine. Before each *category* of system change (packages, firewall, sysctls, ports), show the exact commands and confirm.
- Ask for important values (`SERVERURL`, port, peer names). Use defaults for the rest and summarize them before deploy.
- Explain each check or question in one phrase.
- If a step fails, stop and diagnose it. Do not continue as if it worked.

## Phase 0 — Where am I running?

Decide if you are on the target host, on a workstation for SSH deploy, or on a local workstation. Check `hostname`, the repo checkout, and `/etc/cloud/`. If it is unclear, ask. For remote hosts, collect the host, user, port, and key. Then test `ssh … 'echo ok'` first. Below, "run" means run on the target host.

## Phase 1 — Requirements

Copy `scripts/check-requirements.sh` to the host and run it (`bash check-requirements.sh <port>`). It prints one OK/WARN/FAIL table. The table covers OS, architecture (amd64/arm64 only), `/dev/net/tun`, Docker, Compose v2, permissions, the UDP port, disk, memory, and `ip_forward`. It also covers the optional AmneziaWG kernel module. Show the table and the proposed fixes. Then wait for confirmation. For distro policy and edge cases, see `references/requirements.md`.

## Phase 2 — Install missing pieces

Install and verify each missing item in this order. Install Docker plus the Compose plugin. Persist `net.ipv4.ip_forward=1` on the host. Open the UDP port in the host firewall and the cloud provider firewall or security group. Make sure `/dev/net/tun` exists. For distro commands, see `references/system-setup.md`. With the default bridge network, a host NAT rule is not necessary. The container masquerades the VPN subnet itself. For `network_mode: host`, see `references/system-setup.md`.

## Phase 3 — Settings

Ask in one batch. Use the `ask_user` tool with several fields.

| Setting | Default | Notes |
|---|---|---|
| `SERVERURL` | unset | Prefer a DNS name. If unset, the container detects the public IPv4 |
| `SERVERPORT` | `51820` | For a non-default port, map `SERVERPORT:51820/udp`. Use 443/udp or a port ≤ 9999 if carriers block high UDP ports |
| `PEERS` | — | Prefer names (`laptop,phone` → `peer_laptop`…). Use letters and digits only; other names are skipped |
| `TZ` | `Etc/UTC` | This controls log timestamps only |

Ask these optional settings only if the user wants them. Use `INTERNAL_SUBNET` for a LAN conflict. Use `DNS_UPSTREAM` for the peer dnsmasq forwarder. Its default is `1.1.1.1,1.0.0.1`. Use `ALLOWEDIPS` for split tunnel. Use `PERSISTENTKEEPALIVE_PEERS` for keepalive control. Its default is `all`; `none` saves phone battery. Use `SERVER_ALLOWEDIPS_PEER_<name>` for site-to-site. For meanings and defaults, see README "Parameters".

Offer encrypted, DNSSEC-validated DNS for peers. Use Unbound on the host as in README "Recommended: Unbound on the host". Use a fixed Compose network. Bind Unbound only to the gateway address of that network. Add a host-firewall rule for the bridge. Set `DNS_UPSTREAM` to the gateway. Never bind it to `0.0.0.0` or a public address.

## Phase 4 — Obfuscation

**Recommend that all `AWG_*` values stay unset.** The container generates valid values on first start. This includes equal `S1`-`S4` values when `RandomTrailers` is on. It also includes `ContentPaddingAddition = 0` when `RandomTrailers` is on. The container persists the values in `/config/server/awg_params`. Ask only these questions:

1. **`AWG_VERSION`**: `2.0` is the default. It needs AmneziaVPN ≥ 4.8.12.9. Other values are `3.0`, `3.1`, and `1.5` (for legacy clients). `3.1` means 3.0 plus `RandomTrailers`. Use 2.0 unless you know that every client supports 3.x. Newer modes fail closed. `AWG_DISABLE_COOKIES=on` is a separate opt-in. It gives up DoS mitigation.
2. **Kernel datapath + 3.1 switches**: the host module must be ≥ 3.1. Check it with `cat /sys/module/amneziawg/version`. Otherwise the tunnel fails with `Unable to modify interface: Invalid argument`. The start-up warning is best-effort. Thus, check the module version yourself. The userspace fallback always works.
3. **Pin values?** Pin values only to match an existing deploy, or to make the compose file self-contained. Generate them with the `amneziawg-config` skill: `../amneziawg-config/scripts/awg-genconf.sh --params-only --version <v> --format compose`. Validate user-supplied values with its `awg-lint.py`.

Shared values (`S`, `H`, `I`, `HeaderProtectionKey`, `RandomTrailers`) must be identical on every peer. If you change any shared value later, redistribute every peer conf.

## Phase 5 — Write compose and start

1. Confirm the deploy directory. The default is `/opt/amneziawg/`, with a `config/` subdirectory.
2. Write `docker-compose.yml` from `references/compose-template.md`.
3. Run `docker compose pull && docker compose up -d`.
4. Check `docker compose logs --tail=100`. It should show `AmneziaWG kernel module is active` or `using userspace amneziawg-go`. It should then show `Config initialization finished`, `Activating tunnel /config/wg_confs/wg0.conf`, and `All tunnels are now active`. `Config generation failed` names the bad input. Previous configs are kept.
5. Run `docker exec amneziawg /app/healthcheck`. It prints `tunnels up: wg0`. Docker reports `healthy` after its start period. Run `docker exec amneziawg awg show` to list peers.

On failure, see `references/troubleshooting.md`.

## Phase 6 — Hand off peer configs

`docker exec amneziawg /app/show-peer <name-or-number>` prints a terminal QR code for the Amnezia app. Tell the user:

- Configs are in `<deploy-dir>/config/peer_<name>/peer_<name>.conf`, or `peer<N>` for numeric peers. Back up all of `config/`. It holds the server keys and peer keys.
- To add peers, edit `PEERS` and then run `docker compose up -d`. Existing peers keep keys and addresses. Removed peers are archived in `config/removed_peers/`.
- Client app: https://amnezia.org/.

## Phase 7 — Verify from a client

Import the conf, connect, and run `curl https://api.ipify.org`. It should show the server IP. On the server, `awg show` should show a recent handshake. If it never completes, check the cloud firewall first. Then check client version support. Then check the logs.

## Don'ts

- Do not run compose in the foreground. Do not publish 51820 over TCP.
- Do not run `chmod 777` on `config/`. Set `PUID`/`PGID` to the user that owns it.
- Do not re-randomize `AWG_*` from config management. The values must stay stable.
- Do not skip Phase 1. Without `/dev/net/tun`, the container starts but no tunnel comes up. It reports `unhealthy` and removes its default routes.

## Files

- `scripts/check-requirements.sh` — Phase 1 checks
- `references/requirements.md` — distro and architecture policy
- `references/system-setup.md` — Docker install, sysctl, firewall, TUN per distro
- `references/compose-template.md` — compose template and rules to fill it
- `references/troubleshooting.md` — deployment failures

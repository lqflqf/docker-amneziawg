---
name: deploy-amneziawg
description: End-to-end deployment of the docker-amneziawg AmneziaWG VPN container to a VPS or Linux host (Ubuntu/Debian/Fedora/RHEL clones/Arch/Alpine; Hetzner, DigitalOcean, AWS, etc.). Use whenever the user wants to deploy, install, set up or "go live" with this container — including "set this up on my server", "I have a VPS, what now?" or just "deploy". Checks host requirements, installs Docker, opens the firewall, gathers settings, writes docker-compose.yml, starts the stack, adds the peers and hands out their configs/QR codes.
---

# Deploy docker-amneziawg

Goal: guide the user from a VPS to a phone on the VPN by QR code. The user reference for each setting is `README.md`. Do not restate it; apply it.

## Behaviour

- You are on a real machine. Before each *category* of system change (packages, firewall, sysctls, ports), show the exact commands and confirm.
- Ask for important values (`SERVERURL`, port, peer names). The container reads the environment only on the first start. Use defaults for the rest and summarize them before deploy.
- Explain each check or question in one phrase.
- If a step fails, stop and diagnose it. Do not continue as if it worked.

## Phase 0 — Where am I running?

Decide if you are on the target host, on a workstation for SSH deploy, or on a local workstation. Check `hostname`, the repo checkout, and `/etc/cloud/`. If it is unclear, ask. For remote hosts, collect the host, user, port, and key. Then test `ssh … 'echo ok'` first. Below, "run" means run on the target host.

## Phase 1 — Requirements

Copy `scripts/check-requirements.sh` to the host and run it (`bash check-requirements.sh <port>`). It prints one OK/WARN/FAIL table. The table covers OS, architecture (amd64/arm64 only), `/dev/net/tun`, Docker, Compose v2, permissions, the UDP port, disk, memory, and `ip_forward`. It also covers the optional AmneziaWG kernel module. Show the table and the proposed fixes. Then wait for confirmation. For distro policy and edge cases, see `references/requirements.md`.

## Phase 2 — Install missing pieces

Install and verify each missing item in this order. Install Docker plus the Compose plugin. Persist `net.ipv4.ip_forward=1` on the host. Open the UDP port in the host firewall and the cloud provider firewall or security group. Make sure `/dev/net/tun` exists. For distro commands, see `references/system-setup.md`. With the default bridge network, a host NAT rule is not necessary. The container masquerades the VPN subnet itself. Never use `network_mode: host`. See `references/system-setup.md`.

## Phase 3 — Settings

Ask in one batch. Use the `ask_user` tool with several fields. The container reads these variables only on the first start, when `config/` has no database. Later changes need `awgctl`, so get them right now.

| Setting | Default | Notes |
|---|---|---|
| `SERVERURL` | detected | Always set it, preferably to a DNS name. If it is not set, the container detects the public IPv4 once. If detection fails, the container stops |
| `SERVERPORT` | `51820` | For a non-default port, map `SERVERPORT:51820/udp`. If carriers block high UDP ports, use 443/udp or a port ≤ 9999 |
| Peer names | — | One name for each device, for example `laptop`, `phone`. 1-32 of `A-Z a-z 0-9 . _ -`, starting with a letter or digit. The peers come in Phase 6, not from the environment |

Ask these optional settings only if the user wants them. Use `INTERNAL_SUBNET` (a `/24`) for a LAN conflict. Use `DNS_UPSTREAM` for the peer dnsmasq forwarder. Use `ALLOWEDIPS` for a split tunnel. Use `MTU` only for a known constrained path. For meanings and defaults, see README "Settings".

Offer encrypted, DNSSEC-validated DNS for peers. Use Unbound on the host as in README "Recommended: Unbound on the host". Use a fixed Compose network. Bind Unbound only to the gateway address of that network. Add a host-firewall rule for the bridge. Set `DNS_UPSTREAM` to the gateway. Never bind it to `0.0.0.0` or a public address.

## Phase 4 — Obfuscation

**Recommend that all `AWG_*` values stay unset.** On the first start, `awgctl` generates a valid set and saves it in `config/amneziawg.db`. This includes equal `S1`-`S4` values and `ContentPaddingAddition = 0` when `RandomTrailers` is on. Ask only these questions:

1. **`AWG_VERSION`**: `3.1` is the default. It needs AWG 3.1 support in each peer app. Other values are `3.0`, `2.0` (AmneziaVPN ≥ 4.8.12.9) and `1.5` (legacy clients). Newer modes fail closed, so use `2.0` for an older client app. `AWG_DISABLE_COOKIES=on` is a separate opt-in. It gives up DoS mitigation.
2. **Kernel datapath + 3.1 switches**: the host module must be ≥ 3.1. Check it with `cat /sys/module/amneziawg/version`. Otherwise the tunnel fails with `Unable to modify interface: Invalid argument`. The start-up warning is best-effort. Thus, check the module version yourself. The userspace fallback always works.
3. **Pin values?** Pin values only to match an existing deploy, or to make the compose file self-contained. Generate them with the `amneziawg-config` skill: `../amneziawg-config/scripts/awg-genconf.sh --params-only --version <v> --format compose`. Validate user-supplied values with its `awg-lint.py`. `awgctl` refuses a set that breaks a rule, and the container then stops.

Shared values (version, `S`, `H`, `I`, `HeaderProtectionKey`, `RandomTrailers`) must be identical on every peer. To change them later, use `awgctl awg version|set|regenerate` and restart the container. Then give each peer its new conf.

## Phase 5 — Write compose and start

1. Confirm the deploy directory. The default is `/opt/amneziawg/`, with a `config/` subdirectory.
2. Write `docker-compose.yml` from `references/compose-template.md`.
3. Run `docker compose pull && docker compose up -d`.
4. Check `docker compose logs --tail=100`. Look for `AmneziaWG kernel module is active` or `using userspace amneziawg-go`.
5. Then look for `Config initialization finished` and `Tunnel wg0 is up`.
6. If the container exits with code 1, read the `ERROR` line. See `references/troubleshooting.md`.
7. Run `docker exec amneziawg awgctl health`. It prints `healthy`. Docker reports `healthy` after its start period.

## Phase 6 — Add peers and hand off configs

For each peer name, run `docker exec amneziawg awgctl peer add --name <name> --qr`. It prints the peer id and a terminal QR code for the Amnezia app. For a number of peers without names, run `docker exec amneziawg awgctl peer add-many <count>`. After the last peer, run `docker restart amneziawg`: changes apply only at a restart. Tell the user:

- Each peer command takes the peer id (2-254) from `awgctl peer list`, not the name.
- To show a QR code again, run `docker exec amneziawg awgctl peer export <id> --qr`.
- To get a file, run `docker exec amneziawg awgctl peer export <id> > <name>.conf`. The file holds the private key of the peer.
- The container does not keep peer confs on disk. Back up `config/amneziawg.db`: it holds the server key and all peer keys. Stop the container before the copy.
- To add a peer later, run `awgctl peer add`. To remove one, run `awgctl peer rm <id> --yes`. Restart the container to apply each change.
- Client app: https://amnezia.org/.

## Phase 7 — Verify from a client

Import the conf, connect, and run `curl https://api.ipify.org`. It should show the server IP. On the server, `docker exec amneziawg awgctl peer list` should show a recent handshake. If it never completes, check the cloud firewall first. Then check client version support. Then check the logs.

## Don'ts

- Do not run compose in the foreground. Do not publish 51820 over TCP.
- Do not run `chmod 777` on `config/`. Set `PUID`/`PGID` to the user that owns it.
- Do not change `SERVERURL`, `AWG_*` or other first-start variables to change a running server. The container ignores them. Use `awgctl` (README "Settings"), then restart the container.
- Do not skip Phase 1. Without `/dev/net/tun` and without a kernel module, no tunnel comes up, and the container reports `unhealthy`.

## Files

- `scripts/check-requirements.sh` — Phase 1 checks
- `references/requirements.md` — distro and architecture policy
- `references/system-setup.md` — Docker install, sysctl, firewall, TUN per distro
- `references/compose-template.md` — compose template and rules to fill it
- `references/troubleshooting.md` — deployment failures

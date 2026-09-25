---
name: docker-amneziawg
description: |
  Development skill for the docker-amneziawg project - an AmneziaWG VPN container with LinuxServer.io architecture. Use when working in the docker-amneziawg repository for: (1) Adding features or fixing bugs, (2) Modifying s6-overlay services, (3) Updating config generation, (4) Working with AmneziaWG obfuscation parameters, (5) Testing or building the Docker image. Triggers when working in a directory containing this project's structure (root/etc/s6-overlay, awg-related files).
---

# docker-amneziawg Development Guide

## Documentation Layout

| File | Audience | Purpose |
|------|----------|---------|
| `README.md` | End users | Setup, usage, parameters (LinuxServer-style) |
| `CLAUDE.md` | Developers, AI agents | Architecture, dev patterns, conventions, gotchas, troubleshooting, CI/CD |
| `docs/awg-performance.md` | Developers | Measured data-path cost of each obfuscation parameter |

For architecture details, parameter constraints, or troubleshooting tables, read `CLAUDE.md`.
For AWG parameter implementation specifics, read [references/awg-parameters.md](references/awg-parameters.md).

## Project Overview

AmneziaWG Docker container built on LinuxServer.io base images with s6-overlay process supervision. Provides automatic VPN configuration generation with DPI-bypass obfuscation.

Two modes: **server** (set `PEERS` to auto-generate configs) and **client** (place `.conf` files in `/config/wg_confs/`).

## Project Structure

```
docker-amneziawg/
├── Dockerfile                    # Multi-stage build (go-builder, tools-builder, runtime)
├── docker-compose.yml            # Example configurations
├── CLAUDE.md                     # Technical reference for developers and AI agents
├── root/
│   ├── app/
│   │   ├── show-peer             # QR code display utility
│   │   └── healthcheck           # Docker HEALTHCHECK: every tunnel up?
│   ├── defaults/
│   │   ├── server.conf           # Server config template (eval+heredoc)
│   │   ├── peer.conf             # Peer config template (eval+heredoc)
│   │   └── unbound.conf          # Unbound default config (DoT upstream, DNSSEC)
│   └── etc/s6-overlay/s6-rc.d/
│       ├── init-amneziawg-module/    # Kernel module detection (oneshot)
│       ├── init-amneziawg-confs/     # Config generation (oneshot)
│       ├── svc-unbound/              # Unbound resolver (longrun)
│       ├── svc-amneziawg/            # Tunnel service (oneshot up/down)
│       └── user/contents.d/          # Service registration (empty files)
└── .github/
    ├── scripts/                      # next-version, release-tags, smoke-test, update-pins (+ tests)
    └── workflows/
        ├── docker-build.yml          # Main build pipeline (multi-arch)
        └── upstream-check.yml        # Daily upstream version check
```

## S6-Overlay Architecture

### Service Dependency Chain
```
init-amneziawg-module (oneshot) -> init-amneziawg-confs (oneshot) -> svc-unbound (longrun) -> svc-amneziawg (oneshot)
```

Key points:
- `svc-amneziawg` is a **oneshot** — tunnels stay up without a running process
- `svc-unbound` is a **longrun** — Unbound serves DNS for peers, as `abc` after binding port 53. Off when `USE_DNS=false`, in client mode by default, or when something already listens on port 53
- A failed tunnel makes `svc-amneziawg` exit 1; the `HEALTHCHECK` (`/app/healthcheck`) reports the container unhealthy
- Dependencies: empty files in `dependencies.d/`. Registration: empty files in `user/contents.d/`

### Script Requirements
- Shebang: `#!/usr/bin/with-contenv bash`
- Must be executable (`chmod +x`)
- Use `lsiown` for LinuxServer permission management

## Key Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PEERS` | - | Enables server mode. Number ("3") or names ("laptop,phone") |
| `SERVERURL` | auto | Server URL/IP for peer configs |
| `SERVERPORT` | 51820 | Port advertised to peers. Use <= 9999 if ISP blocks high UDP |
| `INTERNAL_SUBNET` | 10.13.13.0 | VPN subnet (.1 = server, .2+ = peers) |
| `PEERDNS` | auto | DNS for peers (auto = container's Unbound at subnet.1) |
| `USE_DNS` | true (server) / false (client) | Run the bundled Unbound resolver. Replaces upstream's `USE_COREDNS`, which is ignored |
| `LOG_CONFS` | true | Show QR codes in container logs |
| `AWG_VERSION` | 2.0 | Protocol version: 2.0 (full DPI evasion), 3.0 (header protection + randomized timers), 3.1 (3.0 + RandomTrailers) or 1.5 (legacy, AmneziaVPN < 4.8.12.9) |
| `AWG_RANDOM_TRAILERS` | - | `on`/`off`. Random-length handshake packets. Any AWG_VERSION; defaults to `on` under 3.1. Must match on every end. **Requires `S1 == S2 == S3 == S4`** under AWG 2.0+ or ~3.5% of transport packets are dropped — see [awg-performance.md](../../../docs/awg-performance.md) |
| `AWG_DISABLE_COOKIES` | - | `on`/`off`. No cookie-reply under load. Any AWG_VERSION; always opt-in. Need not match |

## AmneziaWG Obfuscation — Quick Reference

For detailed parameter docs, see [references/awg-parameters.md](references/awg-parameters.md).

| Param | Default | Key Constraint |
|-------|---------|----------------|
| `AWG_S1` | Random 15-150 | <= 1132, **S1+56 must not equal S2** |
| `AWG_S2` | Random 15-150 | <= 1188 |
| `AWG_S3` | Random 8-55 (2.0) / 12-55 (3.x) / 0 (1.5) | <= 64 |
| `AWG_S4` | Random 4-20 (2.0) / 12-20 (3.x) / 0 (1.5) | <= 32, prefer **<= 20** (above that, full-size packets fragment at the default 1420 MTU), **per-packet overhead — keep small** |
| `AWG_H1-H4` | Range (2.0) / int (1.5) | >= 5, all unique, non-overlapping |
| `AWG_I1-I5` | Auto QUIC Initial (2.0) / empty (1.5) | In `[Interface]` before `[Peer]` |

**Critical**: Server and all clients must use identical S1-S4, H1-H4, I1-I5 values. Jc/Jmin/Jmax may differ.

## Common Development Tasks

### Adding a New Environment Variable
1. Set default in `init-amneziawg-confs/run` main logic section
2. If persistent: add to `save_vars()` (as `ORIG_X`) AND the change detection `if` block
3. For AWG params: also add to `generate_awg_params()` save block AND `load_awg_params()` grep section
4. For config output: add to templates in `root/defaults/` (eval+heredoc), `append_awg_signatures()` (server conf), or `append_awg_signatures_to_interface()` (peer confs — inserts before `[Peer]` via awk)
5. Document in `docker-compose.yml` and `README.md`

### Testing Changes
```bash
docker build -t amneziawg-test .
docker run -d --name awg-test --cap-add NET_ADMIN \
  -e PEERS=2 -e SERVERURL=test.example.com \
  -v /tmp/awg-test:/config amneziawg-test
docker logs awg-test
docker exec awg-test cat /config/wg_confs/wg0.conf
docker exec awg-test cat /config/peer1/peer1.conf
docker rm -f awg-test
```

Tunnel startup fails without `--device /dev/net/tun` — expected in testing.

## Common Gotchas

| Issue | Solution |
|-------|----------|
| `local: can only be used in a function` | Remove `local` keyword from main script body |
| awg-quick not found in build | Copy from `src/wg-quick/linux.bash`, not compiled |
| Service not starting | Check: executable bit, shebang, registered in `user/contents.d/` |
| Exit code 137 | Normal — container was stopped (SIGKILL) |
| I1-I5 must be in `[Interface]`, not `[Peer]` | Use `append_awg_signatures_to_interface()` for peer confs |
| `cut -d= -f2` truncates I-params with `=` | Use `cut -d= -f2-` (tag syntax contains `=` signs) |
| Loading `awg_params` with `source` | Never — overrides Docker env vars. Use `grep`/`cut` with `${VAR:-fallback}` |
| Amnezia app shows AWG 1.5 instead of 2.0 | H1-H4 must use range format, not single integers |
| Upload collapses to ~2 Mbit/s under `AWG_VERSION=3.1` | `RandomTrailers` with unequal `S1`-`S4`. Set all four equal (see [awg-performance.md](../../../docs/awg-performance.md)) |
| Download ~22% below expectations | `ContentPaddingAddition` breaks `UDP_GRO` batching. Set `AWG_CONTENT_PADDING=0` |
| `SERVERPORT` mapping in Docker | Map as `SERVERPORT:51820/udp` — container always listens on 51820 internally |

## GitHub Actions Workflows

### docker-build.yml
- A `changes` gate skips build+release when nothing image-affecting (`Dockerfile`, `root/**`, `.dockerignore`, image CI) changed since the last release (highest run id, via `.github/scripts/release-tags.sh latest`)
- Push to the default branch -> builds multi-arch, tags `<tools>-r<N>` (immutable) + `<tools>` + `latest`, creates release `v<tools>-r<N>`. A superseded re-run of an old run publishes only its immutable tag and a non-latest release
- Pull requests -> `smoke` job: single-platform build + `.github/scripts/smoke-test.sh` (no push, read-only token)
- `workflow_dispatch` without overrides -> new release (e.g. base-image refresh); with version overrides -> ad-hoc `dispatch-<run>` tag only

### upstream-check.yml
- Daily at 06:00 UTC: compares Dockerfile ARG defaults against the highest strict `vX.Y.Z` upstream versions (no prereleases, no downgrades)
- Builds and smoke-tests the bump, then opens/updates a PR; the image is released when the PR is merged
- Set the optional `UPSTREAM_PR_TOKEN` secret so the bot PR also triggers the regular PR checks (`GITHUB_TOKEN` PRs do not)

Multi-arch: `linux/amd64`, `linux/arm64`

# docker-amneziawg — developer instructions

AmneziaWG VPN container on LinuxServer.io base images with s6-overlay. **Server mode** (`PEERS` set) generates keys, `wg0.conf` and peer confs; **client mode** brings up the user's confs. Every `*.conf` in `/config/wg_confs/` is brought up on start.

## Where things are documented

Each topic has one home. Link to it rather than restating it.

| Topic | Canonical file |
|---|---|
| User setup, every env var, defaults, troubleshooting table | `README.md` |
| Throughput cost of each parameter, measurements, kernel-module trailer fix | `docs/awg-performance.md` |
| Tunnel MTU | `docs/mtu.md` |
| Vulnerability reporting, `/config` trust boundary | `SECURITY.md` |
| Generic AWG parameter semantics, CPS tag syntax, `<r N>` size | `.github/skills/amneziawg-config/references/parameters.md` |
| Generating / linting standalone AWG confs | `.github/skills/amneziawg-config/` |
| Deploying this container to a host | `.github/skills/deploy-amneziawg/` |
| Architecture, invariants, CI (this file) | `.github/copilot-instructions.md` |

## Build and test

```bash
docker build -t amneziawg-test .
.github/scripts/smoke-test.sh amneziawg-test      # ~1 min, the CI gate
.github/scripts/next-version.test.sh && .github/scripts/release-tags.test.sh
```

There is no unit test suite. `smoke-test.sh` covers binaries, s6 services, Unbound, branding, config generation for 3.1/2.0, and a stateful section on one volume (file modes, unchanged restart, 2.0↔1.5, invalid pinned 3.x values, `SERVER_ALLOWEDIPS_PEER_*` changes, duplicate/removed peers, invalid `SERVERURL`, world-writable and unparseable templates). Each check must fail on its own — never `test ... && echo` (errexit ignores the left side of `&&`). Without `/dev/net/tun` the tunnel cannot start; that is expected in a local smoke run.

## Architecture

### Dockerfile (3 stages, multi-arch amd64/arm64)

`go-builder` (golang-alpine → static `amneziawg-go`), `tools-builder` (alpine → `awg`, plus `awg-quick` copied from upstream `src/wg-quick/linux.bash`), runtime (`ghcr.io/linuxserver/baseimage-alpine`). Base images are pinned by digest (Dependabot bumps them), builder `apk` packages by version, each upstream tag by commit (`AMNEZIAWG_*_COMMIT`; the clone fails if the tag moved, empty = unchecked). The `awg-quick` sed patch (skip `src_valid_mark` if already set) is followed by a `grep` that fails the build if upstream changed the line. Runtime symlinks: `wg → awg`, `wg-quick → awg-quick`, `/etc/wireguard → /config/wg_confs`. `HEALTHCHECK` runs `/app/healthcheck`.

### s6-overlay services (`root/etc/s6-overlay/s6-rc.d/`)

```
init-config → init-amneziawg-module → init-amneziawg-confs → svc-unbound → svc-amneziawg
```

- **init-amneziawg-module**: probes `ip link add awgprobe<pid> type amneziawg` (the amnezia module's link kind; a plain `wireguard` module is never used). On failure writes `WG_QUICK_USERSPACE_IMPLEMENTATION` to `/run/s6/container_environment` → userspace `amneziawg-go`. Never calls `modprobe`, so `SYS_MODULE` does not enable the kernel datapath and a `/lib/modules` mount is a no-op.
- **init-amneziawg-confs** (`umask 077`): config generation (below), seeds `/config/unbound/unbound.conf` + `root.key`, writes `/run/unbound/listen.conf` (127.0.0.1 + `<subnet>.1` with `ip-freebind`, ACL loopback + VPN /24), runs `harden_permissions`, ends with `Config initialization finished` (the smoke test waits for it). Client mode writes `USE_DNS=false` unless set.
- **svc-unbound** (longrun, `notification-fd 3` via `s6-notifyoncheck`): off when `USE_DNS=false`, or `USE_DNS` unset and port 53 already has a listener. An invalid config (`unbound-checkconf`) is not started. Writes `/run/unbound-state` = `disabled`/`invalid`/`running`. Existing user `unbound.conf` files are never rewritten (deliberate, see 5aa0803); they keep their own `interface`/`access-control`/`username` and init logs a NOTE.
- **svc-amneziawg** (oneshot): validates `[Interface]` in each conf, brings tunnels up, saves them to `/run/activeconfs` via `declare -p`. On failure tears the set down and retries (3 attempts, 5s); if it still fails or no conf exists, removes every IPv4/IPv6 default route in every table (`drop_default_routes`, fail closed) and exits 1. The finish script tears down in reverse.
- **healthcheck**: healthy only if `/run/activeconfs` exists, every listed interface is in `awg show interfaces`, and per `/run/unbound-state` Unbound is not `invalid` and, when `running`, answers `health.amneziawg.` (plus `HEALTHCHECK_DNS_NAME`).

Dependencies are empty files in `<svc>/dependencies.d/`; registration is an empty file in `user/contents.d/`.

### Config generation (`init-amneziawg-confs/run`)

- **State**: inputs are saved to `/config/.donoteditthisfile` as `ORIG_<NAME>="value"` for the names in `STATE_VARS` (server vars, every `AWG_PARAM_NAMES` entry, `SERVER_ALLOWEDIPS_ALL`, `TEMPLATES_HASH`). Parsed by `load_state` into `ORIG`, compared by `state_changed`. Keys in `STATE_VARS_OPTIONAL` may be missing from older files without forcing regeneration. AWG params are also saved to `/config/server/awg_params`, read by `load_awg_params` (`grep`/`cut -d= -f2-`; env-set names recorded in `AWG_FROM_ENV`). **Never `source` either file** — that overrides env vars and runs saved values as root. `save_awg_params`/`save_vars` run only after a successful generation or an unchanged start.
- **Transactional**: `generate_confs` → `render_confs` into `/config/.awg-staging.*` → `validate_confs` (awk structure check + `parse_check_conf`: `awg-quick strip` | `awg setconf awgparsecheck`, Endpoint swapped to 127.0.0.1; "Unable to modify interface" means it parsed) → `install_confs` (`wg0.conf` first, pngs last, backups restored by `rollback_install`). Then QR codes are logged (`LOG_CONFS=true` only) and dropped peers archived. Activation failure after install is not rolled back.
- **Refusals** (`GENERATION_BLOCKED` / `AWG_PARAMS_INVALID`, previous confs kept, log `Config generation failed`): duplicate peers, >253 peers, invalid `SERVERURL`/`SERVERPORT`/`INTERNAL_SUBNET`, failed IP detection with no saved value, pinned 3.x S value < 12.
- **Peers** (`assign_peer_addresses`): existing peers keep their address (translated if `INTERNAL_SUBNET` changed); new ones take the lowest free address among *current* peers. Peers dropped from `PEERS` move to `/config/removed_peers/<id>-<timestamp>/` (`archive_removed_peers`). Numeric peers are `peer1`…, named ones `peer_<name>`. Keys are created in place.
- **`SERVERURL=auto`**: `detect_public_ipv4` (HTTPS, `--max-time 10`, strict IPv4 check, 3 services); on failure reuses `ORIG[SERVERURL]` or blocks. A bare IPv6 is bracketed.
- **Templates** (`root/defaults/server.conf`, `peer.conf`, copied to `/config/templates/`) are eval+heredoc (`${VAR}`, `$(cat ...)`), the LinuxServer docker-wireguard pattern. `check_template` refuses world-writable templates. `/config` is a root-equivalent trust boundary (`PostUp` runs as root), so never describe templates as data. `harden_permissions` sets 700/600 on `server/`, `wg_confs/`, `peer*/`, `removed_peers/`, but not `templates/` (it would hide the mode `check_template` checks).
- `INTERFACE` is derived from `INTERNAL_SUBNET` (`10.13.13.0` → `10.13.13`). The container always listens on 51820; `SERVERPORT` is only advertised (map `SERVERPORT:51820/udp`).

### Volume layout

```
/config/wg_confs/       every *.conf is brought up (server: wg0.conf)
/config/server/         privatekey-server, publickey-server, awg_params
/config/templates/      server.conf, peer.conf
/config/unbound/        unbound.conf, root.key
/config/peer1/, peer_laptop/   <peer>.conf, .png, private/public/presharedkey-<peer>
/config/removed_peers/  <id>-<timestamp>/
/config/.donoteditthisfile
```

## AWG parameter implementation

Generic semantics and limits live in the `amneziawg-config` skill; measured costs in `docs/awg-performance.md`. Container-specific rules:

- **Version**: `AWG_VERSION` ∈ `1.5`, `2.0` (default), `3.0`, `3.1`. It is deliberately not defaulted at the top of the script: `load_awg_params` must first restore the saved version, and `generate_awg_params` applies the `2.0` default afterwards. Defaulting early silently rewrites every peer conf as 2.0 when the env var is dropped.
- Never compare `AWG_VERSION` to literals; use `awg_has_3x_params` (3.0/3.1) and `awg_has_2x_features` (2.0/3.x).
- When the saved version differs, `drop_saved_awg_params` drops the saved `AWG_VERSIONED_PARAMS` (not env-set ones). Env-pinned values always win.
- **Defaults**: Jc 3-8, Jmin 40-80, Jmax 80-250; S1/S2 15-150; S3/S4 8-55/4-20 (2.0), 12-55/12-20 (3.x), 0 (1.5); H1-H4 non-overlapping 50M-wide ranges in four quadrants of 5…2³¹−1 (2.0+; the Amnezia app reports single integers as 1.5), random integers (1.5); I1 a 1200-byte QUIC Initial (`generate_default_signatures`), I2-I5 empty. 3.x (`generate_awg3_params`): `HeaderProtectionKey` from `awg genpsk` (not `genkey`, which is clamped), `lo-hi` timer ranges, `ContentPaddingAddition` 16-128 range — or `0` when RandomTrailers is on.
- **RandomTrailers ⇒ S1 = S2 = S3 = S4** (2.0+): when `AWG_RANDOM_TRAILERS` resolves to `on`, one value 12-20 is drawn for all four (a pinned one is reused), saved unequal values are dropped, and a pinned unequal set warns. The switch normalization therefore runs *before* the S values are drawn — do not move it.
- **3.1 switches** (`AWG_RANDOM_TRAILERS`, `AWG_DISABLE_COOKIES`, any version): normalized by `normalize_awg_switch` to `on`/`off` (anything else dropped with a warning). Only `on` writes a key; `off` omits it and is how users turn a switch off. `AWG_VERSION=3.1` only defaults RandomTrailers to `on`; DisableCookies is never implicit. Only explicit values are persisted (`AWG_RT_EXPLICIT`/`AWG_DC_EXPLICIT`) so a preset-derived `on` does not survive a downgrade; change detection compares effective values.
- **s6 drops empty variables** from `container_environment`: `AWG_X=` arrives as absent ("reuse saved"). Never build "clear this" semantics on empty env vars.
- Pinned S < 12 under 3.x sets `AWG_PARAMS_INVALID` (amneziawg-go rejects it). Other violations (`S1+56 = S2`, duplicate H, Jmin > Jmax, timer ordering) only warn.
- **Writers**: server conf — template, then `append_awg_signatures`, `append_awg3_params`, `append_awg31_options` (before any `[Peer]`). Peer confs — the `*_to_interface` variants insert before `[Peer]` with awk; I1-I5 must be in `[Interface]` (the Amnezia app ignores them under `[Peer]`). Empty I values are never written (`awg` rejects `I2 =`). `awg31_options_block` returns via `BLOCK_OUT`, not stdout — command substitution strips the trailing newline and glues the key onto `[Peer]`.
- `check_awg31_kernel_support` warns when 3.1 switches meet a pre-3.1 module (`/sys/module/amneziawg/version`, major/minor only — the trailer fix `4569c4c6` did not bump the date component, so never gate on it). Skipped in userspace mode (bundled `amneziawg-go` is 3.1).
- The container never writes `MTU`; keep `S4 ≤ 20` so `awg-quick`'s 1420 does not fragment (see `docs/mtu.md`).
- The default I1 keeps one `<r 1178>` tag on purpose. Do not split it and do not add a tag-size check (see the skill's parameters.md).

## Development patterns

- s6 scripts: `#!/usr/bin/with-contenv bash`, `# shellcheck shell=bash`, executable, `lsiown -R abc:abc /config` for ownership. `local` only inside functions.
- **New env var**: default it in the main section of `init-amneziawg-confs/run`; if it shapes confs add it to `STATE_VARS` (and `STATE_VARS_OPTIONAL` if older state files lack it); AWG params go in `AWG_PARAM_NAMES` (+ `AWG_VERSIONED_PARAMS` if version-shaped); output via the templates or the `append_*` writers; document in `README.md` and `docker-compose.yml`.
- Branding: `root/etc/s6-overlay/s6-rc.d/init-adduser/branding` + `LSIO_FIRST_PARTY=false`.
- Indentation: 4 spaces for shell/s6, 2 for Dockerfile/YAML (`.editorconfig`).
- Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`); branches `feature/<name>`.
- Exit code 137 on stop is normal.

## CI/CD

**`docker-build.yml`**: `changes` → `version` → `smoke` → `build` → `release`.
- Image content = `Dockerfile`, `root/**`, `.dockerignore`, `docker-build.yml`, `.github/scripts/`. Other changes (docs, skills, compose) never publish. On the default branch the gate diffs against the **last release tag**, so an image change from a failed run is not lost; manual runs always build.
- `smoke` (every mode): amd64 build, `smoke-test.sh`, Trivy failing on fixable CRITICALs. PRs run only this plus the script tests.
- Release (push to default branch, or `workflow_dispatch` without overrides): builds arm64 alone and runs its binaries under QEMU, then pushes amd64+arm64 with SLSA provenance and SBOM as `:<tools>-r<N>` (immutable), `:<tools>`, `:latest`, `:sha-<short>`; creates annotated tag `v<tools>-r<N>` and a GitHub Release with the digest and generated notes. Runs are serialized; a superseded run publishes only its immutable tag.
- `workflow_dispatch` with version overrides: ad-hoc `:dispatch-<run>` only.
- No `v*` tag trigger — never push `v*-r*` tags by hand; they are the build counter.

**Versioning**: `<amneziawg-tools>-r<N>` (LinuxServer build-counter). `N` restarts per tools version; computed by `.github/scripts/next-version.sh` from existing tags, whose annotation records `run-id:` so re-runs reuse their number (the image carries `io.github.actions.run-id`). `VERSION` → `/build_version`. Upstream pins are the `AMNEZIAWG_{GO,TOOLS}_{VERSION,COMMIT}` `ARG` defaults at the top of the Dockerfile.

**`upstream-check.yml`** (daily 06:00 UTC): picks the highest strict `vX.Y.Z` upstream release (no prereleases, never downgrades), resolves it to a commit, builds and smoke-tests with `update-pins.sh`, then opens/updates a PR. Set `UPSTREAM_PR_TOKEN` so that PR triggers the normal checks.

# docker-amneziawg — developer instructions

An AmneziaWG VPN container on plain Alpine with the Alpine `s6-overlay` package. Releases after `3.1.20260812-r5` do not use the LinuxServer.io base. **Server mode** (`PEERS` set) generates keys, `wg0.conf` and peer confs. **Client mode** uses the confs of the user. At start, the container brings up each `*.conf` in `/config/wg_confs/`.

## Language: ASD-STE100

Write all text (this file, `README.md`, `docs/`, `SECURITY.md`, skills, comments, commit messages, PR text) in ASD-STE100 Simplified Technical English:

- One instruction in one sentence. Maximum 20 words in a procedure, 25 words in a description, 6 sentences in a paragraph.
- Use the imperative for instructions, the active voice and the simple present tense. Put a condition before its instruction.
- Use simple words with one meaning: "use", not "utilize"; "make sure", not "ensure"; "start", not "launch".
- Do not use a verb "-ing" form as a noun or an adjective. Do not remove articles to make text shorter.
- Write technical names (variables, files, functions, commands) as they are, in backticks. Headings, table cells and list labels can be short phrases.

The `asd-ste100` skill rewrites and audits text. Its `scripts/ste-lint.py` examines the structural rules. If the skill and this list do not agree, use this list. Do not add the ASD-STE100 dictionary to the repository: ASD does not let this project copy it.

## Where to find documentation

Each topic has one file. Link to that file. Do not copy its content.

| Topic | File |
|---|---|
| User setup, env vars, defaults, troubleshooting | `README.md` |
| Throughput cost of parameters, kernel-module trailer fix | `docs/awg-performance.md` |
| Tunnel MTU | `docs/mtu.md` |
| Vulnerability reports, `/config` trust boundary | `SECURITY.md` |
| AWG parameter semantics, CPS tag syntax, `<r N>` size | `.github/skills/amneziawg-config/references/parameters.md` |
| Generate or lint standalone AWG confs | `.github/skills/amneziawg-config/` |
| Deploy this container to a host | `.github/skills/deploy-amneziawg/` |
| Rewrite or lint text in ASD-STE100 (from `danyuchn/asd-ste100-skill` at `32511c6`, MIT) | `.github/skills/asd-ste100/` |
| Architecture, invariants, CI | this file |

## Build and test

```bash
docker build -t amneziawg-test .
.github/scripts/unit-test.sh amneziawg-test       # bats unit tests in the image
.github/scripts/smoke-test.sh amneziawg-test      # approximately 1 min, the CI gate
.github/scripts/next-version.test.sh && .github/scripts/release-tags.test.sh
```

Set `ENGINE=nerdctl` to run `unit-test.sh` and `smoke-test.sh` with nerdctl. Use only commands and flags that docker and nerdctl both accept.

**Unit tests** (`tests/*.bats`): `unit-test.sh` runs them in a throwaway container of the image, with `bats` added by `apk`. They source the installed scripts and call their functions.

- Each script under `root/` keeps its side effects in `main()`. The last lines are `if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi`. Do not use the `&&` form: it returns 1 when sourced.
- Do not put `umask`, `set` or other side effects at the top level. Declare top-level associative arrays with `declare -gA`.
- A bare `! cmd` or a failing `a && b` does not fail a bats test. Use `run ! cmd` and one condition for each line.

**Smoke tests**: `smoke-test.sh` examines the binaries, services, DNS, config generation and state, client mode, identity, an upgrade from the last LinuxServer.io release and an end-to-end tunnel. Read the script for the full list.

- Each check must fail by itself. Do not use `test ... && echo`: errexit ignores the left side of `&&`.
- Do not use `cmd | grep -q` (SIGPIPE + pipefail). Use `grep -q ... <<<"$(cmd)"`.
- Only the end-to-end section gets `/dev/net/tun`. Other checks must accept a tunnel that is up (host amneziawg module) or down.
- CI must supply `/dev/net/tun`. Without it, a local run skips the end-to-end section.

## Architecture

### Dockerfile (3 stages, amd64 and arm64)

- `go-builder` builds a static `amneziawg-go`. `tools-builder` builds `awg` and copies `awg-quick` from upstream `src/wg-quick/linux.bash`.
- A sed patch makes `awg-quick` skip `src_valid_mark` if it is already set. If upstream changes that line, the next `grep` stops the build.
- The runtime adds `s6-overlay` from apk and records the s6 stack in `/build_version`. Busybox `shuf` runs out of memory on the H ranges, so the image has `coreutils`. `shadow` supplies `usermod`. `abc` is 911:911. `ENTRYPOINT ["/init"]` also clears the alpine `CMD`.
- Base images are pinned by digest (Dependabot). `apk` packages are pinned by version. Alpine keeps only the newest version, so bump a pin when the build fails.
- Upstream tags are pinned by commit (`AMNEZIAWG_*_COMMIT`). If a tag moves, the clone fails. An empty value disables the check.
- There are no `wg`/`wg-quick` aliases and no `/etc/wireguard`. `awg-quick` looks for bare names in `/etc/amnezia/amneziawg/`, so `svc-amneziawg` gives full paths.

### s6-overlay services (`root/etc/s6-overlay/s6-rc.d/`)

```
init-adduser → init-amneziawg-module → init-amneziawg-confs → svc-dnsmasq
                                                            → svc-amneziawg
```

A dependency is an empty file in `<svc>/dependencies.d/`. A registration is an empty file in `user/contents.d/`.

- **init-adduser**: applies `PUID`/`PGID` (default 911) with `groupmod -o`/`usermod -o`. It refuses `0` (root) with its own message: `abc` owns `/config` and runs dnsmasq. The home moves off `/config` during `usermod -u`, because `usermod` changes the owner of the home. If an ID is not valid or does not apply, halt with exit 1 (`/run/s6/basedir/bin/halt`). A failed oneshot alone keeps the container on without `svc-amneziawg`, and a client keeps its default routes.
- **Banner**: a 50-column box for phone terminals. `branding` holds the top, the logo and the repo rows. `run` adds `uid:gid`, the version, the build date and the bottom. Pad values to keep the right border straight; cut long values with `…`. Then `run` writes a `WARNING` for each LinuxServer.io option that is still set. The container does not implement these options.
- **init-amneziawg-module**: runs `ip link add awgprobe<pid> type amneziawg`. If it fails, it writes `WG_QUICK_USERSPACE_IMPLEMENTATION` to `/run/s6/container_environment` (userspace `amneziawg-go`). It never uses a plain `wireguard` module and never calls `modprobe`.
- **init-amneziawg-confs** (`umask 077`): generates the confs, runs `configure_dns` (server mode) and `harden_permissions`. The last line is `Config initialization finished`; the smoke test waits for it. `PEERDNS`/`USE_DNS`/`HEALTHCHECK_DNS_NAME` and an old `/config/unbound` get a NOTE.
- **svc-dnsmasq** (longrun, no readiness notification):
  - Client mode: `sleep infinity`, state `disabled`.
  - Server mode: wait (maximum 60 s) for `/run/amneziawg-settled`, so dnsmasq starts after `wg0`. The marker is not an s6 dependency, because dnsmasq must run when the tunnel fails. `/run` stays after `docker restart`, so `init-amneziawg-confs` and the `svc-amneziawg` finish script remove the marker.
  - If a config is missing or `dnsmasq --test` fails: `sleep infinity`, state `invalid`. Never exec a rejected config, because s6 restarts it in a loop. Else: state `running`, `exec dnsmasq -k`. The state file is `/run/dnsmasq-state`.
  - `interface=wg0` + `bind-dynamic` limit access to the tunnel. There are no firewall rules. Never change to `listen-address`: it answers on all interfaces.
- **svc-amneziawg** (oneshot): validates `[Interface]`, brings the tunnels up and saves them to `/run/activeconfs` with `declare -p`.
  - It writes `/run/amneziawg-settled` after the first attempt, also on failure or with no conf. Thus retries do not stop DNS.
  - In server mode, if dnsmasq already runs when a set comes up, restart `svc-dnsmasq`. `awg-quick` adds the address before `link set up`, and `bind-dynamic` can miss it (3 of 8 starts). Do not remove this restart. Do not start dnsmasq before the marker.
  - On failure, it tears the set down and tries again (3 attempts, 5 s). If all attempts fail or no conf exists, `drop_default_routes` removes each default route in each table (fail closed). Then it exits 1.
- **healthcheck**: needs `/run/activeconfs` and each listed interface in `awg show interfaces`. Server mode also needs `/run/dnsmasq-state` = `running`, the address in `/run/dnsmasq/address` on `wg0`, and an answer for `health.amneziawg.` on it. The check does not test the upstream.

### Config generation (`init-amneziawg-confs/run`)

- **State**: the inputs in `STATE_VARS` go to `/config/.donoteditthisfile` as `ORIG_<NAME>="value"`. `load_state` reads them into `ORIG`; `state_changed` compares. A key in `STATE_VARS_OPTIONAL` can be missing without regeneration. AWG params also go to `/config/server/awg_params` (`load_awg_params`, env-set names in `AWG_FROM_ENV`).
- **Never `source` the state files.** That overrides env vars and runs saved values as root. `save_awg_params`/`save_vars` run only after a successful generation or an unchanged start.
- **Transactions**: `render_confs` writes to `/config/.awg-staging.*`. `validate_confs` runs an awk structure check and `parse_check_conf` (`awg setconf` with Endpoint 127.0.0.1; "Unable to modify interface" means a correct parse). `install_confs` installs `wg0.conf` first and the pngs last; `rollback_install` restores the backups. There is no rollback for a failed activation.
- **Refusals** (`GENERATION_BLOCKED`/`AWG_PARAMS_INVALID`): keep the previous confs and log `Config generation failed`. Causes: duplicate peers, more than 253 peers, an invalid `SERVERURL`/`SERVERPORT`/`INTERNAL_SUBNET`, IP detection failure with no saved value, a pinned 3.x S value less than 12.
- **Peers** (`assign_peer_addresses`): existing peers keep their address (translated if `INTERNAL_SUBNET` changes). A new peer gets the lowest free address among the *current* peers. `archive_removed_peers` moves removed peers to `/config/removed_peers/`.
- **`SERVERURL`**: if not set, `detect_public_ipv4` asks 3 HTTPS services at the same time (5 s maximum) and takes the first valid answer in list order. Then it uses the saved value. Start each `curl` with no pipeline, so `$!` is the `curl` PID. Clear `auto` first, with a warning. Never let it through: it is a valid host name and gives `Endpoint = auto:`. Put brackets around a bare IPv6 address.
- **Templates** (`root/defaults/*.conf`, copied to `/config/templates/` if missing): rendered with eval + heredoc. `check_template` refuses world-writable templates. `/config` is a root-equivalent trust boundary (`PostUp` runs as root). Never call templates "data". `harden_permissions` sets 700/600 on `server/`, `wg_confs/`, `peer*/` and `removed_peers/`. Do not add `templates/`: that hides the mode `check_template` examines.
- **DNS** (`configure_dns`): `render_dnsmasq` makes `/config/dnsmasq/dnsmasq.conf` only if it is missing or `DNS_STATE_VARS` changed. DNS has a separate state file, so DNS and tunnel inputs never regenerate each other. It runs `dnsmasq --test` before it installs the file. `/run/dnsmasq/base.conf` is written at each start. `/run/dnsmasq/address` comes from the installed `wg0.conf`, not from `INTERNAL_SUBNET`. Do not add `strict-order`: a dead first upstream adds approximately 2.5 s to each query.
- **Keepalive**: the script changes `PERSISTENTKEEPALIVE_PEERS=none` to empty before it saves the state. Thus the state agrees with volumes that never had keepalive. `wants_keepalive` matches a `PEERS` entry or a peer ID.
- **Validation**: `valid_port` refuses a leading zero, because bash arithmetic reads it as octal. `is_ipv6` counts the groups and allows one `::`. `valid_endpoint_host` removes the brackets before it calls `is_ipv6`. `DNS_UPSTREAM` takes raw IPv6 addresses with an optional `#port`, not brackets.
- **Port**: the container always listens on 51820. `SERVERPORT` is only advertised. `INTERFACE` comes from `INTERNAL_SUBNET` (`10.13.13.0` → `10.13.13`); `PEERDNS` is `${INTERFACE}.1`.

### Volume layout

```
/config/wg_confs/       every *.conf is brought up (server: wg0.conf)
/config/server/         privatekey-server, publickey-server, awg_params
/config/templates/      server.conf, peer.conf, dnsmasq.conf
/config/dnsmasq/        dnsmasq.conf (rendered), .donoteditthisfile (DNS state)
/config/peer1/, peer_laptop/   <peer>.conf, .png, private/public/presharedkey-<peer>
/config/removed_peers/  <id>-<timestamp>/
/config/.donoteditthisfile
```

## AWG parameter implementation

The `amneziawg-config` skill gives the semantics. `README.md` gives the defaults. These rules apply to the container:

- **Version**: do not set the `AWG_VERSION` default at the top of the script. `load_awg_params` must first restore the saved version; then `generate_awg_params` applies `2.0`. An early default changes all peer confs to 2.0 when the user removes the env var.
- Never compare `AWG_VERSION` to literals. Use `awg_has_3x_params` (3.0/3.1) and `awg_has_2x_features` (2.0/3.x).
- If the saved version is different, `drop_saved_awg_params` drops the saved `AWG_VERSIONED_PARAMS`. Env-pinned values always win.
- **H1-H4** (2.0+): non-overlapping ranges, because the Amnezia app shows single integers as 1.5. **I1**: a 1200-byte QUIC Initial with one `<r 1178>` tag. Do not split the tag or add a tag-size check.
- **3.x**: make `HeaderProtectionKey` with `awg genpsk`, not `genkey` (it clamps the key). `ContentPaddingAddition` is `0` when RandomTrailers is on.
- **RandomTrailers ⇒ S1 = S2 = S3 = S4**: when it is `on`, draw one value 12-20 for all four. Drop saved unequal values; give a warning for pinned unequal values. The switch normalization runs *before* the S draw. Do not move it.
- **3.1 switches** (`AWG_RANDOM_TRAILERS`, `AWG_DISABLE_COOKIES`): `normalize_awg_switch` gives `on`/`off`. Only `on` writes a key. `AWG_VERSION=3.1` sets only RandomTrailers to `on`. Save only explicit values (`AWG_RT_EXPLICIT`/`AWG_DC_EXPLICIT`), so a preset `on` does not stay after a downgrade.
- **Empty variables**: s6 drops them, so `AWG_X=` means "use the saved value". Never use an empty env var to mean "clear" or "off".
- **Validation**: a pinned S < 12 under 3.x is an error (amneziawg-go rejects it). Other violations only give a warning.
- **Writers**: `append_awg_signatures`, `append_awg3_params`, `append_awg31_options` write before the first `[Peer]`. In peer confs, I1-I5 must be in `[Interface]` (the Amnezia app ignores them under `[Peer]`). Never write an empty I value (`awg` rejects `I2 =`). `awg31_options_block` returns in `BLOCK_OUT`, not on stdout: command substitution removes the last newline.
- **Kernel module**: `check_awg31_kernel_support` reads only major/minor from `/sys/module/amneziawg/version`. The trailer fix `4569c4c6` did not change the date, so never use the date as a gate.
- **MTU**: only the templates write `MTU` (`root/defaults/server.conf` and `peer.conf` set 1280). The script never writes `MTU`. Old volumes keep templates without `MTU`, so keep `S4 ≤ 20`: then the `awg-quick` default 1420 does not fragment.

## Development rules

- s6 scripts start with `#!/usr/bin/with-contenv bash` and `# shellcheck shell=bash`, and are executable. Use `local` only in functions. Put side effects in `main()` behind the guard (see "Unit tests").
- Do not recommend `network_mode: host`. In that namespace, `drop_default_routes` and the `PostUp` rules change the host network.
- The final `chown` uses `find /config ! -type l`, so it never follows symlinks. A failure only gives a warning.
- The runtime is busybox plus GNU `coreutils` and `grep`. `find` has no `-uid`/`-xtype`; use `-user`/`-group`.
- **New env var**: set the default in the main section of `init-amneziawg-confs/run`. If it changes confs, add it to `STATE_VARS` (and `STATE_VARS_OPTIONAL` for older state files). Add AWG params to `AWG_PARAM_NAMES` (and `AWG_VERSIONED_PARAMS`). Write output through the templates or the `append_*` writers. Document it in `README.md` and `docker-compose.yml`.
- Indentation: 4 spaces for shell/s6, 2 spaces for Dockerfile/YAML (`.editorconfig`).
- Use conventional commits (`feat:`, `fix:`, `docs:`, `chore:`) and `feature/<name>` branches.

## CI/CD

**`docker-build.yml`**: `changes` → `version`, and `lint` in parallel. Then `smoke` + `smoke-nerdctl` → `build` → `release`.

- Only `Dockerfile`, `root/**`, `.dockerignore`, `docker-build.yml` and `.github/scripts/` change the image (output `image`). Other changes never publish. A change to `tests/**` also runs the tests (output `validate`), but it does not publish.
- `lint` runs on each event: shellcheck on each bash script and the bats tests, hadolint, actionlint. Each linter has a pinned version and sha256.
- `BUILD_DATE` is the UTC time that `version` calculates once. Each build of the run uses it. The Dockerfile declares `BUILD_DATE` and `VERSION` after the package layers, so a new date does not stop the cache.
- On the default branch, the gate compares with the **last release tag**, so a failed run does not lose a change. Manual runs always build.
- `smoke`: amd64 build, `unit-test.sh`, `smoke-test.sh`, Trivy (fails on fixable CRITICAL findings). PRs run only the lint, test and script-test jobs.
- `smoke-nerdctl`: the same build, unit and smoke tests under rootful containerd + nerdctl (pinned `nerdctl-full`, sha256). It stops Docker first, so that one containerd owns the socket. `build` needs it.
- Release (push to the default branch, or `workflow_dispatch` without overrides): boots arm64 under QEMU. Then it pushes amd64+arm64 with provenance and SBOM as `:<tools>-r<N>` (immutable), `:<tools>`, `:latest`, `:sha-<short>`. It creates the tag `v<tools>-r<N>` and a GitHub Release. A superseded run publishes only its immutable tag.
- `workflow_dispatch` with version overrides publishes only `:dispatch-<run>`.
- Never push `v*-r*` tags manually. They are the build counter.
- Jobs use `runs-on: ubuntu-24.04`, not `ubuntu-latest`. Thus the runner changes only in a PR of its own, after CI passes on the new image.

**Version numbers**: `<amneziawg-tools>-r<N>`. `N` starts again for each tools version. `.github/scripts/next-version.sh` calculates it from the tags. The tag annotation records `run-id:`, so a re-run uses the same number. The upstream pins are the `AMNEZIAWG_{GO,TOOLS}_{VERSION,COMMIT}` `ARG` defaults in the Dockerfile.

**`upstream-check.yml`** (daily, 06:00 UTC): selects the highest strict `vX.Y.Z` upstream release (no prereleases, no downgrades). It builds it with `update-pins.sh`, then runs the unit and smoke tests, then opens or updates a PR. Set `UPSTREAM_PR_TOKEN`, so that this PR starts the normal checks.

# docker-amneziawg — developer instructions

An AmneziaWG VPN server container on plain Alpine with the Alpine `s6-overlay` package. The container is a server only. `awgctl`, a static Go binary at `/usr/local/bin/awgctl`, holds all config logic. It keeps the configuration in the SQLite database `/config/amneziawg.db`. Commands change only the database. A container restart applies the changes: `awgctl init` renders `/run` from the database. s6 and small shell scripts start the services. They read only the files in `/run`.

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
| User setup, first-start env vars, `awgctl` commands, settings, defaults, troubleshooting, upgrade | `README.md` |
| `awgctl` commands, flags, usage text and exit codes | `internal/cli/commands.go` (`groups`), `internal/cli/cli.go` |
| Settings: keys, seed env vars, defaults, rules, reissue flag | `internal/config/registry.go` |
| AWG generators, rules and version transitions | `internal/awg/` |
| Throughput cost of parameters, kernel-module trailer fix | `docs/awg-performance.md` |
| Tunnel MTU | `docs/mtu.md` |
| Vulnerability reports, `/config` trust boundary | `SECURITY.md` |
| Design of the upstream pin updates (proposal) | `docs/design/upstream-pin-updates.md` |
| AWG parameter semantics, CPS tag syntax, `<r N>` size | `.github/skills/amneziawg-config/references/parameters.md` |
| Generate or lint standalone AWG confs | `.github/skills/amneziawg-config/` |
| Deploy this container to a host | `.github/skills/deploy-amneziawg/` |
| Rewrite or lint text in ASD-STE100 (from `danyuchn/asd-ste100-skill` at `32511c6`, MIT) | `.github/skills/asd-ste100/` |
| Architecture, invariants, CI | this file |

## Build and test

The host needs Docker. It does not need a Go toolchain. `.github/scripts/go.sh` runs each Go command in the `golang` image that the Dockerfile pins.

```bash
.github/scripts/go.sh test ./...                    # unit tests
.github/scripts/go.sh vet ./...
.github/scripts/go.sh fmt ./...                     # gofmt
GO_CGO=1 .github/scripts/go.sh test -race ./...     # race detector (CI lint job)
docker build -t amneziawg-test .                    # the awgctl-builder stage runs go vet and go test
.github/scripts/integration-test.sh amneziawg-test  # Go integration tests in the image
.github/scripts/smoke-test.sh amneziawg-test        # black box, approximately 1 min, the CI gate
.github/scripts/next-version.test.sh && .github/scripts/release-tags.test.sh
```

Set `ENGINE=nerdctl` to run `go.sh`, `integration-test.sh` and `smoke-test.sh` with nerdctl. Use only commands and flags that docker and nerdctl both accept. CI runs golangci-lint v2.15.0 with `.golangci.yml` and `--build-tags integration`.

**Unit tests** (`internal/*/*_test.go`): no root and no real binaries. A package that runs `awg`, `awg-quick` or `dnsmasq` uses the `sys.Runner` interface. Tests use the fake runner in `internal/sys/systest`. It models a running `wg0` and can make a command fail. Put each new test in the test file of its package.

**Integration tests** (`internal/integration`, build tag `integration`): `integration-test.sh` compiles the test binary in the `golang` image. Then it runs the binary as root in a container of the image, with `NET_ADMIN` and `/dev/net/tun`. The tests use the real `awg`, `awg-quick`, `amneziawg-go` and `dnsmasq`. Without `/dev/net/tun`, a local run skips them. CI must supply it.

**Smoke tests** (`smoke-test.sh`): a black-box test of the image. It examines the image contents, the s6 graph, the first start, fatal start errors, identity and dnsmasq. It also examines `awgctl` through `docker exec`, `import-legacy` of a volume from the last shell release, and an end-to-end tunnel. Read the script for the full list.

- Each check must fail by itself. Do not use `test ... && echo`: errexit ignores the left side of `&&`.
- Do not use `cmd | grep -q` (SIGPIPE + pipefail). Use `grep -q ... <<<"$(cmd)"`.
- Only the end-to-end section gets `/dev/net/tun`. Other checks must accept a tunnel that is up (host amneziawg module) or down.

## Architecture

### Dockerfile (4 stages, amd64 and arm64)

- `go-builder` builds a static `amneziawg-go`. `tools-builder` builds `awg` and copies `awg-quick` from upstream `src/wg-quick/linux.bash`, with no patch.
- `awgctl-builder` runs `go vet ./...` and `go test ./...`, then builds `awgctl` with `CGO_ENABLED=0` and `-trimpath`. `-X .../internal/cli.Version=${VERSION}` sets the version. `VERSION` comes after the tests, so a new version does not run them again.
- The runtime adds `s6-overlay` from apk and records the s6 stack in `/build_version`. `shadow` supplies `usermod`. `abc` is 911:911. `ENTRYPOINT ["/init"]` also clears the alpine `CMD`. `HEALTHCHECK` runs `awgctl health`.
- The Dockerfile pins base images by digest (Dependabot) and `apk` packages by version. Alpine keeps only the newest version, so bump a pin when the build fails. `go.sum` pins the Go modules (Dependabot `gomod`).
- The Dockerfile pins upstream tags by commit (`AMNEZIAWG_*_COMMIT`). If a tag moves, the clone fails. An empty value disables the check.
- There are no `wg`/`wg-quick` aliases, no `/etc/wireguard`, no `/defaults`, no `/app` scripts, no `qrencode`, `openresolv` or `coreutils`.

### Go packages (`cmd/awgctl`, `internal/`)

| Package | Role |
|---|---|
| `cli` | The command table, each command, the exit codes, the `change` transaction (load, change, check, commit), `init` |
| `config` | The settings registry, the `State`, `Validate`, the peer id and name rules |
| `awg` | AWG keys, generators, `Validate`, `Transition` (version change), `Set`, `Regenerate`, `Is3x`/`Has2x` |
| `seed` | The first state from the env, public IPv4 detection with retries |
| `store` | `database/sql` with `modernc.org/sqlite`, the embedded `schema.sql`, atomic `Create` |
| `render` | The server `wg0.conf`, the peer export, the dnsmasq conf, the firewall rules |
| `conf` | The conf model, the one writer, the parser (legacy import, diff) |
| `check` | `Check`: the rules, then the server conf, one peer conf and the dnsmasq conf with the real parsers (`awg setconf`, `dnsmasq --test`) |
| `health` | `awgctl health`. Reads only `/run` and the running system |
| `legacy` | `import-legacy`: reads the old files as data, maps names, compares each peer export with the old conf |
| `fsafe` | Symlink and owner checks in `/config`, modes, a `chown` that does not follow links |
| `keys`, `netcheck` | X25519 keys (`crypto/ecdh`), PSKs, input checks for hosts, ports, subnets, CIDR lists, DNS upstreams |
| `sys`, `sys/systest` | Paths, the `Runner` interface, atomic writes, the fake runner for tests |

### s6-overlay services (`root/etc/s6-overlay/s6-rc.d/`)

```
init-adduser → init-amneziawg-module → init-amneziawg-config → svc-dnsmasq
                                                             → svc-amneziawg
```

A dependency is an empty file in `<svc>/dependencies.d/`. A registration is an empty file in `user/contents.d/`.

- **init-adduser**: applies `PUID`/`PGID` (default 911) with `groupmod -o`/`usermod -o`. It refuses `0` (root). The home moves off `/config` during `usermod -u`, because `usermod` changes the owner of the home. If an ID is not valid or does not apply, it stops the container with exit 1 (`/run/s6/basedir/bin/halt`). `abc` owns `/config` and runs no process.
- **Banner**: a 50-column box for phone terminals. `branding` holds the top, the logo and the repo rows. `run` adds `uid:gid`, the version, the build date and the bottom. Pad values to keep the right border straight. Cut long values with `…`.
- **init-amneziawg-module**: runs `ip link add awgprobe<pid> type amneziawg`. If it fails, it writes `WG_QUICK_USERSPACE_IMPLEMENTATION` to `/run/s6/container_environment` (userspace `amneziawg-go`). It never uses a plain `wireguard` module and never calls `modprobe`.
- **init-amneziawg-config**: runs `awgctl init`. If `awgctl init` fails, the script stops the container with exit 1 (`/run/s6/basedir/bin/halt`). The restart policy then tries again. A failed oneshot alone keeps an idle container that never tries again.
- **svc-amneziawg** (oneshot): runs `awg-quick up /run/amneziawg/wg0.conf`. It makes 3 attempts, 5 s apart, with `awg-quick down` after each failure. It writes `/run/amneziawg-settled` after the first attempt, also on failure. The log lines are `Tunnel wg0 is up` and `Tunnel wg0 is down`.
  - If dnsmasq already runs when `wg0` comes up, restart `svc-dnsmasq`. `awg-quick` adds the address before `link set up`, and `bind-dynamic` can miss it. Do not remove this restart.
  - The finish script removes the marker and runs `awg-quick down`.
- **svc-dnsmasq** (longrun, no readiness notification): waits (maximum 60 s) for `/run/amneziawg-settled`, so dnsmasq starts after `wg0`. The marker is not an s6 dependency, because dnsmasq must run when the tunnel fails.
  - If `/run/dnsmasq/dnsmasq.conf` is missing or `dnsmasq --test` fails: `sleep infinity`, state `invalid`. Never exec a rejected config, because s6 restarts it in a loop. Else: state `running`, `exec dnsmasq -k`. The state file is `/run/dnsmasq-state`.
  - dnsmasq runs as the `dnsmasq` user (`user=`/`group=` in the conf), not as `abc`.
  - `interface=wg0` + `bind-dynamic` limit access to the tunnel. There are no firewall rules for it. Never change to `listen-address`: it answers on all interfaces.

### `awgctl init` (each start)

1. Remove the stale `/run` state (`/run` stays after `docker restart`).
2. Run the `fsafe` checks on `/config`.
3. If there is no database and old files exist, fail with the `import-legacy` hint. Else seed a database from the env (`seed.Build`), check it, and create it atomically.
4. Open the database. Do steps 5-7 in one read transaction, so that a command cannot commit between the load and the last file.
5. Load and check every value. An error is fatal.
6. Warn if the 3.1 switches are on and the kernel module is older than 3.1.
7. Check the full config with the real parsers. Write `wg0.conf`, the dnsmasq conf and `/run/dnsmasq/address`.
8. Set the modes and owners in `/config`. Log `Config initialization finished`. The smoke test waits for this line.

### Configuration and runtime

| | Where | Content |
|---|---|---|
| Configuration | `/config/amneziawg.db` (600, `PUID:PGID`) | `settings` (AWG values with the `awg.` prefix) and `peers`. No counters, timestamps or runtime state |
| Runtime | `/run/amneziawg/wg0.conf` (600), `/run/dnsmasq/` | Rendered by `awgctl init` at each start. Nothing else writes them |

- A peer `id` is 2-254 and is also the last octet of the peer address in the `/24` subnet. A new peer gets the lowest free id. Ids can come back after `peer rm`.
- Each peer command takes only a plain decimal id. A name or `#5` is a usage error (exit 2).
- A peer name is unique. A peer without a name gets `peer <id>`. Only that id can have a name of the form `peer <digits>` (`config.NameFitsID`).
- `config.Validate` checks the id range, unique ids and names, and the name rules. It also runs for `import-legacy`, which keeps each old address octet as the id.

A change is one write transaction: load, change in memory, `--yes` check, `check.Check` (rules, server conf, one peer conf, dnsmasq conf with the real parsers), commit. A failure gives exit 1 and changes nothing. The command then prints `Saved. Restart the container to apply.` A setting with `Reissue` needs `--yes`, else exit code 3. A change that changes nothing saves nothing.

### Volume and runtime files

```
/config/amneziawg.db           configuration (journal_mode DELETE)
/config/legacy-<ts>/           old files after import-legacy
/run/amneziawg/                wg0.conf
/run/dnsmasq/                  dnsmasq.conf, address
/run/dnsmasq-state             running | invalid
/run/amneziawg-settled         svc-amneziawg finished its first attempt
```

## Invariants

- **The database is the only source of truth.** `awgctl init` reads the seed env vars only when there is no database. Later, it ignores them. `PUID` and `PGID` apply at each start.
- **The database holds only configuration.** Do not add counters, timestamps or runtime state.
- **Only a restart applies a change.** `awgctl` commands change only the database. Never change `wg0`, `/run` or a service from a command.
- **The runtime never reads the database.** `awg-quick`, dnsmasq and `awgctl health` read only `/run`.
- **SQLite serializes the writers.** Each change is one `BEGIN IMMEDIATE` transaction. There is no lock file.
- **`journal_mode = DELETE`.** Do not change to WAL: a copy of the main file then misses committed pages. One file must be a full backup.
- **Fatal start errors stop the container** with exit 1. Never let `awgctl init` fail into an idle container.
- **Never compare AWG versions to literals** outside `internal/awg`. Use `awg.Is3x` (3.0/3.1) and `awg.Has2x` (2.0/3.x).
- **RandomTrailers ⇒ S1 = S2 = S3 = S4** under 2.0+, and `ContentPaddingAddition = 0`. Generators draw one S value of 12-20. `Validate` refuses unequal values. `awg set s=<n>` sets all four.
- **Version changes are deterministic.** `Transition` sets `RandomTrailers` from the version (`on` only for 3.1) and draws S, H, I and the 3.x set again. `Jc`/`Jmin`/`Jmax` and `DisableCookies` stay.
- **Rule violations are errors**, except the S maximums and `S4 > 20` (warnings). amneziawg-go refuses S < 12 under 3.x.
- **H1-H4** (2.0+): ranges that do not overlap, because the Amnezia app shows single integers as 1.5. **I1**: a 1200-byte QUIC Initial with one `<r 1178>` tag. Do not split the tag or add a tag-size check.
- **`HeaderProtectionKey`**: a random 32-byte key (`keys.NewSymmetric`), never a clamped X25519 key.
- **I1-I5 only in `[Interface]`**, also in peer confs (the Amnezia app ignores them under `[Peer]`).
- **Never write an empty value** (`awg` rejects `I2 =`). `conf.Section.Add` drops empty values, and `conf.File.Check` refuses newlines. For the 3.1 switches, only `on` writes a key.
- **Never put an unchecked value in a `PostUp`/`PostDown` line.** `awg-quick` runs these lines through bash. `fw.nat_interface` must match `^[A-Za-z0-9_.-]{1,15}\+?$`. There are no templates and no custom commands.
- **`/config` is a root-equivalent trust boundary.** `fsafe` refuses symlinks and foreign owners for `/config`, the database and its journal. `ChownTree` never follows a link.
- **Kernel module**: the check reads only major/minor from `/sys/module/amneziawg/version`. The trailer fix `4569c4c6` did not change the date, so never use the date as a gate.
- **MTU**: `net.mtu` and `net.peer_mtu` default to 1280. Keep `S4 ≤ 20`, so that the `awg-quick` default 1420 does not fragment when the MTU is `none`.
- **Port**: the container always listens on 51820. `server.endpoint_port` is only advertised.
- **dnsmasq**: do not add `strict-order`: a dead first upstream adds approximately 2.5 s to each query.

## Development rules

- **New setting**: add one entry to `config.Registry` with its key, seed env var, default, `Reissue` flag and `Check`. Add AWG values in `internal/awg`. Write conf output only through `render` and `conf`. Document the setting in `README.md` (and `docker-compose.yml` if it has a seed env var). Add a unit test for its rules.
- **New command**: add one entry to `groups` in `internal/cli/commands.go`, with its flags, number of arguments, usage line and handler. The usage text comes from this table. Use `change` for each state change. Keep the exit codes: 0 done, 1 error (nothing changed), 2 usage, 3 refused without `--yes`.
- **Schema change**: edit `internal/store/schema.sql`. There are no migrations, so a schema change needs a new database. Write the steps in the release notes.
- **Go**: format with `gofmt`. golangci-lint must pass. Return errors with context. Do not panic. Use `sys.Runner` for each external command, so that unit tests can use the fake runner.
- s6 scripts start with `#!/usr/bin/with-contenv bash` and `# shellcheck shell=bash`, and are executable. Use `local` only in functions. Put side effects in `main()`. The last lines are `if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi`. Do not use the `&&` form: it returns 1 when sourced.
- Do not recommend `network_mode: host`. In that namespace, the `PostUp` rules change the host firewall.
- The runtime is busybox plus `bash`, GNU `grep` and the pinned packages.
- Indentation: 4 spaces for shell/s6, tabs for Go (`gofmt`), 2 spaces for Dockerfile/YAML (`.editorconfig`).
- Use conventional commits (`feat:`, `fix:`, `docs:`, `chore:`) and `feature/<name>` branches.

## CI/CD

**`docker-build.yml`**: `changes` → `version`, and `lint` in parallel. Then `smoke` (matrix: `docker`, `nerdctl`) → `build` → `release`.

- Only `Dockerfile`, `root/**`, `cmd/**`, `internal/**`, `go.mod`, `go.sum`, `.golangci.yml`, `.dockerignore`, `docker-build.yml` and `.github/scripts/` change the image (output `image`). Other changes never publish. A change to Go tests only (`*_test.go`, `internal/integration/`, `internal/sys/systest/`) runs the tests (output `validate`), but it does not publish.
- `lint` runs on each event: shellcheck on each bash script, hadolint, actionlint, golangci-lint (with `--build-tags integration`) and `go test -race`. Each linter has a pinned version and sha256. The Go steps run in the `golang` image of the Dockerfile.
- `BUILD_DATE` is the UTC time that `version` calculates once. Each build of the run uses it. The Dockerfile declares `BUILD_DATE` and `VERSION` after the package layers, so a new date keeps the cache of those layers.
- On the default branch, the gate compares with the **last release tag**, so a failed run does not lose a change. Manual runs always build.
- `smoke`: one job for each engine in `matrix.engine`, in parallel, with `fail-fast: false`. Each builds the amd64 image (with the Go unit tests in the builder stage) and runs `integration-test.sh` and `smoke-test.sh`. PRs run only the lint, test and script-test jobs.
  - `docker`: then Trivy (fails on fixable CRITICAL findings).
  - `nerdctl`: rootful containerd + nerdctl (pinned `nerdctl-full`, sha256). It stops Docker first, so that one containerd owns the socket. The tests run with `sudo env`, which passes `ENGINE`, `BUILD_DATE` and the GitHub variables.
- Release (push to the default branch, or `workflow_dispatch` without overrides): boots arm64 under QEMU and runs `awgctl version`, `awgctl peer add` and `peer export` there. Then it pushes amd64+arm64 with provenance and SBOM as `:<tools>-r<N>` (immutable), `:<tools>`, `:latest`, `:sha-<short>`. It creates the tag `v<tools>-r<N>` and a GitHub Release. A superseded run publishes only its immutable tag.
- `workflow_dispatch` with version overrides publishes only `:dispatch-<run>`.
- Never push `v*-r*` tags manually. They are the build counter.
- Jobs use `runs-on: ubuntu-24.04`, not `ubuntu-latest`. Thus the runner changes only in a PR of its own, after CI passes on the new image.

**Version numbers**: `<amneziawg-tools>-r<N>`. `N` starts again for each tools version. `.github/scripts/next-version.sh` calculates it from the tags. The tag annotation records `run-id:`, so a re-run uses the same number. The upstream pins are the `AMNEZIAWG_{GO,TOOLS}_{VERSION,COMMIT}` `ARG` defaults in the Dockerfile.

**`upstream-check.yml`** (daily, 06:00 UTC): selects the highest strict `vX.Y.Z` upstream release (no prereleases, no downgrades). It builds it with `update-pins.sh`, then runs the integration and smoke tests, then opens or updates a PR. Set `UPSTREAM_PR_TOKEN`, so that this PR starts the normal checks.

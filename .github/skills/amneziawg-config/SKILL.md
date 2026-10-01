---
name: amneziawg-config
description: Generate, lint and debug AmneziaWG (AWG 1.5/2.0/3.0/3.1) configs and obfuscation parameters (Jc/Jmin/Jmax, S1-S4, H1-H4, I1-I5, HeaderProtectionKey, ContentPaddingAddition, RandomTrailers, DisableCookies, MTU). Use when creating, editing, reviewing or troubleshooting any AmneziaWG .conf or AWG_* values — e.g. "generate AWG params", "what should S4 be", "AWG tunnel is slow", "upload much slower than download", "peers stopped connecting after I changed params". Not for plain WireGuard.
---

# AmneziaWG configuration

AmneziaWG is WireGuard plus obfuscation. Hand-written parameters usually fail *quietly*: the tunnel connects and runs at 2% speed. Use the bundled scripts; they encode the constraints.

## Scripts (in this skill's `scripts/`)

```bash
# full set: keys, server conf, one conf per peer
awg-genconf.sh --version 3.1 --peers laptop,phone --endpoint vpn.example.com:51820 --outdir ./awg-configs
# parameter block only: conf lines, docker-compose env (this repo's AWG_* names), or shell exports
awg-genconf.sh --params-only --version 2.0 [--format conf|compose|env]
# lint; several files are also cross-checked for shared values
awg-lint.py wg0.conf peer1.conf peer2.conf
```

Lint after every hand edit and first when troubleshooting. `awg-genconf.sh --help` lists all options. The generator needs only bash; key derivation uses `awg`/`wg`, else Python `cryptography`.

When debugging, start with `references/troubleshooting.md`. For every parameter's range and the CPS tag syntax, read `references/parameters.md`. For *why* a value costs speed, read `docs/awg-performance.md` at the repository root.

## Choosing a version

| Version | Adds |
|---|---|
| `1.5` | Junk packets, S1/S2, integer H |
| `2.0` | S3/S4, H ranges, I1-I5 |
| `3.0` | `HeaderProtectionKey`, `ContentPaddingAddition`, randomized timers |
| `3.1` | 3.0 + `RandomTrailers = on` |

Default to **2.0** unless the user says their endpoints are newer. Newer modes fail closed: a client that cannot parse a key does not connect (an old kernel module reports `Unable to modify interface: Invalid argument`). Do not invent per-app version support — upstream publishes none except AWG 2.0 needing AmneziaVPN ≥ 4.8.12.9. Ask what the user runs, or give 2.0 and say 3.x is cheap to try. Server side, `cat /sys/module/amneziawg/version` shows the kernel module generation. `RandomTrailers`/`DisableCookies` are independent switches valid with any version.

## Constraints that cause real damage

1. **`RandomTrailers = on` requires `S1 = S2 = S3 = S4`** (2.0+ ranges). Otherwise ~3.5% of data packets are dropped and upload collapses (~100 → ~2 Mbit/s).
2. **Never combine `ContentPaddingAddition` with `RandomTrailers`** — padding suppresses trailers on send but the receiver still uses loose matching.
3. **`ContentPaddingAddition` costs ~22% of download** (defeats `UDP_GRO`). Use `0` unless explicitly wanted.
4. **`HeaderProtectionKey` forces `S1`-`S4` ≥ 12.**
5. **MTU**: `awg-quick` derives 1420 and ignores `S4`; keep `S4 ≤ 20` and write an explicit `MTU` (1280 on ordinary paths, `path − 60 − S4` / `path − 80 − S4` on constrained ones). Details: `docs/mtu.md`.

## Which values must match

| Must be identical on every endpoint | May differ |
|---|---|
| `S1`-`S4`, `H1`-`H4`, `I1`-`I5`, `HeaderProtectionKey`, `RandomTrailers` | `Jc`/`Jmin`/`Jmax`, 3.x timer ranges, `DisableCookies`, `MTU` |

Changing a shared value invalidates every distributed peer conf — warn before regenerating. `I1`-`I5` belong in `[Interface]` above any `[Peer]`.

## Scope

This skill deals in `.conf` content. For this repository's container, `--format compose` emits its `AWG_*` variables, and the container auto-generates valid values anyway (see the README). For other wrappers (routers, other images) give `.conf` content or ask — never invent environment variable names.

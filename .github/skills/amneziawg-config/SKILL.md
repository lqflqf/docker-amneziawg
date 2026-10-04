---
name: amneziawg-config
description: Generate, lint and debug AmneziaWG (AWG 1.5/2.0/3.0/3.1) configs and obfuscation parameters (Jc/Jmin/Jmax, S1-S4, H1-H4, I1-I5, HeaderProtectionKey, ContentPaddingAddition, RandomTrailers, DisableCookies, MTU). Use when creating, editing, reviewing or troubleshooting any AmneziaWG .conf or AWG_* values — e.g. "generate AWG params", "what should S4 be", "AWG tunnel is slow", "upload much slower than download", "peers stopped connecting after I changed params". Not for plain WireGuard.
---

# AmneziaWG configuration

AmneziaWG is WireGuard with obfuscation. Hand-written parameters usually fail *quietly*. The tunnel connects, but it runs at 2% speed. Use the bundled scripts because they encode the constraints.

## Scripts (in this skill's `scripts/`)

```bash
# full set: keys, server conf, one conf per peer
awg-genconf.sh --version 3.1 --peers laptop,phone --endpoint vpn.example.com:51820 --outdir ./awg-configs
# parameter block only: conf lines, docker-compose env (this repo's AWG_* names), or shell exports
awg-genconf.sh --params-only --version 2.0 [--format conf|compose|env]
# lint; several files are also cross-checked for shared values
awg-lint.py wg0.conf peer1.conf peer2.conf
```

After each hand edit, run the linter. If you troubleshoot, run it first. `awg-genconf.sh --help` lists all options. The generator needs only bash. Key derivation uses `awg`/`wg`. If these are not available, it uses Python `cryptography`.

When you debug, start with `references/troubleshooting.md`. For each parameter range and the CPS tag syntax, read `references/parameters.md`. To know why a value costs speed, read `docs/awg-performance.md` at repository root.

## Choosing a version

| Version | Adds |
|---|---|
| `1.5` | Junk packets, S1/S2, integer H |
| `2.0` | S3/S4, H ranges, I1-I5 |
| `3.0` | `HeaderProtectionKey`, `ContentPaddingAddition`, randomized timers |
| `3.1` | 3.0 + `RandomTrailers = on` |

Default to **2.0** unless the user says their endpoints are newer. Newer modes fail closed. A client that cannot parse a key does not connect. An old kernel module reports `Unable to modify interface: Invalid argument`. Do not invent per-app version support. Upstream publishes none except AWG 2.0 needs AmneziaVPN ≥ 4.8.12.9. Ask what the user runs. Or give 2.0 and say 3.x is cheap to try. On the server, `cat /sys/module/amneziawg/version` shows the kernel module generation. `RandomTrailers`/`DisableCookies` are independent switches valid with any version.

## Constraints that cause real damage

1. **`RandomTrailers = on` requires `S1 = S2 = S3 = S4`** (2.0+ ranges). If this is false, about 3.5% of data packets are dropped, and upload collapses (~100 → ~2 Mbit/s).
2. **Never combine `ContentPaddingAddition` with `RandomTrailers`**. Padding suppresses trailers on send, but the receiver still uses loose matching.
3. **`ContentPaddingAddition` costs about 22% of download** (defeats `UDP_GRO`). Use `0` unless the user explicitly wants it.
4. **`HeaderProtectionKey` forces `S1`-`S4` ≥ 12.**
5. **MTU**: `awg-quick` derives 1420 and ignores `S4`. Keep `S4 ≤ 20`. Write an explicit `MTU`. Use 1280 on ordinary paths. On constrained paths, use `path − 60 − S4` or `path − 80 − S4`. Details: `docs/mtu.md`.

## Which values must match

| Must be identical on every endpoint | May differ |
|---|---|
| `S1`-`S4`, `H1`-`H4`, `I1`-`I5`, `HeaderProtectionKey`, `RandomTrailers` | `Jc`/`Jmin`/`Jmax`, 3.x timer ranges, `DisableCookies`, `MTU` |

If a shared value changes, every distributed peer conf becomes invalid. Warn before you regenerate. Put `I1`-`I5` in `[Interface]` above any `[Peer]`.

## Scope

This skill deals with `.conf` content. For this repository's container, `--format compose` emits its `AWG_*` variables. The container auto-generates valid values anyway; see the README. For other wrappers, such as routers and other images, give `.conf` content or ask. Do not invent environment variable names.

# Security policy

## Supported versions

Only the newest release (`:latest`, or the highest `<amneziawg-tools>-r<N>` tag) receives fixes. Older `-r<N>` tags are immutable and are not patched; upgrade instead.

## Reporting a vulnerability

Do **not** open a public issue. Report privately through GitHub: the repository's **Security** tab → **Report a vulnerability**. Include what is affected, steps to reproduce, the impact and, if you have one, a fix. You will get an answer and credit if you want it; fixes ship as a new release.

## What the image does

- **Pinned supply chain**: base images pinned by digest, upstream sources by commit. Images carry SLSA provenance and an SBOM, and CI fails on fixable critical vulnerabilities.
- **Private secrets**: keys, peer confs, QR codes and saved AWG parameters are mode `600` in `700` directories. QR codes reach the logs only with `LOG_CONFS=true`.
- **Least-privilege DNS**: the default Unbound config drops to the unprivileged `abc` user, listens only on loopback and the tunnel address, and answers only the VPN subnet.
- **Fail closed**: if no tunnel comes up, every IPv4 and IPv6 default route is removed and the container reports `unhealthy`.
- **Validated configs**: generated configs are checked with `awg`'s own parser and installed all-or-nothing; auto-detected addresses are fetched over HTTPS and validated.

The container runs as root because it needs `NET_ADMIN`.

## Trust boundary: `/config`

Treat write access to the `/config` volume as root access to the container. `PostUp`/`PostDown` lines in any conf under `/config/wg_confs/` run as root, and the templates in `/config/templates/` are shell heredocs expanded as root. World-writable templates are refused, but anyone who can write the volume as its owner can run code. Keep it owned by `PUID` and not writable by other users.

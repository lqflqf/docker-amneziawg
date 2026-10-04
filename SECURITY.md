# Security policy

## Supported versions

Only the newest release (`:latest`, or the highest `<amneziawg-tools>-r<N>` tag) gets fixes. Older `-r<N>` tags are immutable, and we do not patch them. Upgrade to the newest release.

## Reporting a vulnerability

Do **not** open a public issue. Send a private report through GitHub: in the repository, go to **Security** → **Report a vulnerability**. Tell us the affected part, the steps to reproduce, the impact and, if possible, a fix. We will answer you, and we will give you credit if you want it. Each fix comes in a new release.

## What the image does

- **Pinned supply chain**: base images are pinned by digest, and upstream sources by commit. Images have SLSA provenance and an SBOM. CI fails on fixable critical vulnerabilities.
- **Private secrets**: keys, peer confs, QR codes and saved AWG parameters have mode `600`, in directories with mode `700`. QR codes go into the logs only if `LOG_CONFS=true`.
- **Least-privilege DNS**: dnsmasq (server mode) runs as the unprivileged user `abc`. It answers only queries that come through the tunnel (`interface=wg0`).
- **Fail closed**: if no tunnel comes up, the container removes each IPv4 and IPv6 default route and reports `unhealthy`.
- **Validated configs**: the parser of `awg` examines each generated config. The container installs all of the configs or none of them. It gets auto-detected addresses through HTTPS and validates them.

The container runs as root, because it needs `NET_ADMIN`.

## Trust boundary: `/config`

Write access to the `/config` volume is equal to root access to the container. `PostUp`/`PostDown` lines in each conf in `/config/wg_confs/` run as root. The templates in `/config/templates/` (also `dnsmasq.conf`) are shell heredocs, and root expands them. The container refuses world-writable templates. But a user who can write to the volume as its owner can run code. Make sure that `PUID` owns the volume and that other users cannot write to it.

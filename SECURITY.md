# Security policy

## Supported versions

Only the newest release (`:latest`, or the highest `<amneziawg-tools>-r<N>` tag) gets fixes. Older `-r<N>` tags are immutable, and we do not patch them. Upgrade to the newest release.

## Reporting a vulnerability

Do **not** open a public issue. Send a private report through GitHub: in the repository, go to **Security** → **Report a vulnerability**. Tell us the affected part, the steps to reproduce, the impact and, if possible, a fix. We will answer you, and we will give you credit if you want it. Each fix comes in a new release.

## What the image does

- **Pinned supply chain**: the Dockerfile pins base images by digest and upstream sources by commit. `go.sum` pins the Go modules. Images have SLSA provenance and an SBOM. CI fails on fixable critical vulnerabilities.
- **Private secrets**: the database `/config/amneziawg.db` and the copies in `/config/backups/` have mode `600`. The rendered `wg0.conf` in `/run/amneziawg/` has mode `600`. The container does not keep peer configs or QR codes on disk, and it never writes them to the log. `awgctl get` and `awgctl awg show` hide secrets unless you add `--show-secrets`.
- **No user commands**: the container makes all confs from the database. There are no templates. The only `PostUp`/`PostDown` lines are the built-in firewall rules. Their one variable, `fw.nat_interface`, must match a strict pattern, and `awgctl` examines it again at each start.
- **Least-privilege DNS**: dnsmasq runs as the unprivileged `dnsmasq` user, not as `PUID`. Thus dnsmasq cannot write the database. It answers only queries that come through the tunnel (`interface=wg0`).
- **Checked changes**: before `awgctl` saves a change, it checks the full config with the `awg` parser, `dnsmasq --test` and key checks. At each start, it examines each value in the database again. It gets the auto-detected public address through HTTPS and checks it.
- **No symlinks**: `awgctl` refuses a symlink in place of `/config`, the database, its journal or `backups/`. It never follows a symlink when it changes owners in `/config`.

The container runs as root, because it needs `NET_ADMIN`. The `abc` user (`PUID`:`PGID`) only owns `/config` and runs no process.

## Trust boundary: `/config`

Write access to the `/config` volume is equal to root access to the container. The database holds every key, and root processes read it. A user who can write to the volume can change the server, the peers and the keys. Make sure that `PUID` owns the volume and that other users cannot write to it. A backup of `amneziawg.db` holds every private key. Keep it as private as the volume.

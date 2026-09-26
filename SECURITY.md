# Security Policy

## Supported Versions

We support the latest version of this Docker image. Security updates are applied to the `latest` tag and new releases.

| Version | Supported          |
| ------- | ------------------ |
| latest  | ✅ Yes             |
| < 1.0   | ❌ No              |

## Reporting a Vulnerability

If you discover a security vulnerability, please follow these steps:

1. **Do NOT** open a public issue
2. Include the following information:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
   - Suggested fix (if any)

## Security Best Practices

When using this Docker image:

### Configuration Security
- **Never** commit configuration files with real keys to version control
- Use strong, randomly generated private keys
- Regularly rotate keys
- Limit `AllowedIPs` to necessary ranges only

### Container Security
- Run containers with minimal required privileges
- Use Docker secrets for sensitive configuration
- Regularly update the container image
- Monitor container logs for suspicious activity

### Network Security
- Use firewall rules to restrict access to WireGuard ports
- Consider using non-standard ports
- Enable logging for connection monitoring
- Use strong authentication for server access

### Host Security
- Keep the Docker host system updated
- Use container runtime security tools
- Implement proper backup strategies for configurations
- Monitor system resources and network traffic

## Security Features

- **Minimal attack surface**: Alpine Linux base, base images pinned by digest and upstream sources pinned by commit; images carry SLSA provenance and an SBOM, and CI fails on fixable critical vulnerabilities
- **Secrets stay private**: keys, peer confs, QR codes and the saved AWG parameters are created mode `600` in `700` directories. QR codes (which contain private keys) are only written to the logs when `LOG_CONFS=true`
- **Least privilege for DNS**: on new installs Unbound drops to the unprivileged `abc` user after binding, and only listens on loopback and the tunnel address, answering the VPN subnet only
- **Fail closed**: if no tunnel comes up, every IPv4 and IPv6 default route is removed and the container reports `unhealthy`
- **Validated configs**: generated configs are checked by `awg`'s own parser and installed transactionally; auto-detected addresses are fetched over HTTPS and validated
- **AmneziaWG obfuscation**: built-in traffic obfuscation to evade detection

The container itself runs as root: it needs `NET_ADMIN` to create interfaces and firewall rules.

### Trust boundary: `/config`

Treat write access to the `/config` volume as root access to the container. `PostUp`/`PostDown` lines in any conf under `/config/wg_confs/` run as root, and the templates in `/config/templates/` are shell heredocs expanded as root. The container refuses world-writable templates, but anyone who can write the volume as its owner can run code. Keep the volume owned by `PUID` and not writable by other users.

## Vulnerability Response

- Security issues will be addressed with high priority
- Fixes will be released as soon as possible
- Security advisories will be published for significant vulnerabilities
- Users will be notified through GitHub releases and repository updates

## Security Updates

Stay informed about security updates:

1. Watch this repository for releases
2. Subscribe to GitHub security advisories
3. Follow the project's release notes
4. Check for updates regularly using `docker pull`

## Responsible Disclosure

We appreciate security researchers who help keep our users safe. If you report a vulnerability responsibly, we will:

- Work with you to understand and resolve the issue
- Provide credit for the discovery (if desired)
- Keep you informed of our progress

Thank you for helping keep Docker AmneziaWG secure! 🔒

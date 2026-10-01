---
name: Bug Report
about: Create a report to help us improve
title: '[BUG] '
labels: ['bug']
assignees: ''
---

**Describe the bug**
What happened, and what you expected instead.

**To reproduce**
Your `docker-compose.yml` (or `docker run` command) and the steps that trigger it.

**Environment**
- Host OS and kernel (`uname -r`):
- Docker / Compose version:
- Image tag (e.g. `latest`, `3.1.20260812-r4`):
- Output of `docker exec amneziawg cat /build_version`:
- Datapath (log says `kernel module is active` or `using userspace amneziawg-go`):

**Diagnostics**

```
docker exec amneziawg /app/healthcheck
docker logs amneziawg
```

Paste the output here, with keys and public IPs removed.

**Configuration**
Relevant `[Interface]`/`[Peer]` sections with **private and preshared keys removed**.

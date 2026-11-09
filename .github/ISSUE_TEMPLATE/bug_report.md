---
name: Bug Report
about: Create a report to help us improve
title: '[BUG] '
labels: ['bug']
assignees: ''
---

**Describe the bug**
Tell us what occurred and what you expected.

**To reproduce**
Give your `docker-compose.yml` (or `docker run` command) and the steps that cause the bug.

**Environment**
- Host OS and kernel (`uname -r`):
- Docker / Compose version:
- Image tag (e.g. `latest`, `3.1.20260812-r4`):
- Output of `docker exec amneziawg awgctl version` and `docker exec amneziawg cat /build_version`:
- Datapath (log says `kernel module is active` or `using userspace amneziawg-go`):

**Diagnostics**

```
docker exec amneziawg awgctl health
docker logs amneziawg
```

Paste the output here. Remove keys and public IPs first.

**Configuration**
Give the output of `docker exec amneziawg awgctl get` and `docker exec amneziawg awgctl awg show`. These commands hide the secrets. If you give a peer conf, **remove the private and preshared keys.**

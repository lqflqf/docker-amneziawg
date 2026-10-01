# Contributing

Issues and pull requests are welcome. Use the issue templates; for security problems follow [SECURITY.md](SECURITY.md) instead.

## Workflow

1. Fork, then branch: `feature/<name>`.
2. Make the change. Architecture, invariants and conventions are in [`.github/copilot-instructions.md`](.github/copilot-instructions.md) — read it before touching `root/` or CI.
3. Build and test:

   ```bash
   docker build -t amneziawg-test .
   .github/scripts/smoke-test.sh amneziawg-test     # what CI runs, ~1 min
   # if you changed .github/scripts/:
   .github/scripts/next-version.test.sh && .github/scripts/release-tags.test.sh
   ```

   For a multi-arch build: `docker buildx build --platform linux/amd64,linux/arm64 .`
4. Update the user docs the change affects (`README.md`, `docker-compose.yml`, `docs/`). Each topic has one home, listed in `.github/copilot-instructions.md`.
5. Commit with [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`) and open a PR against `master`.

## Style

- Shell and s6 scripts: 4-space indent, `#!/usr/bin/with-contenv bash` plus `# shellcheck shell=bash`, executable bit set.
- Dockerfile and YAML: 2-space indent (see `.editorconfig`).
- Comment only what needs explaining.

## Releases

Merging to `master` publishes a new image automatically when image content changed (`Dockerfile`, `root/`, `.dockerignore`, the build workflow or `.github/scripts/`). Do not create `v*` tags by hand — they are the build counter.

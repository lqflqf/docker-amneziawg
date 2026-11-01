# Contributing

We accept issues and pull requests. Use the issue templates. For security problems, use the procedure in [SECURITY.md](SECURITY.md).

## Workflow

1. Fork the repository, then make a branch: `feature/<name>`.
2. Make the change. [`.github/copilot-instructions.md`](.github/copilot-instructions.md) gives the architecture, the invariants and the conventions. Read it before you change `root/` or CI.
3. Build and test:

   ```bash
   docker build -t amneziawg-test .
   .github/scripts/unit-test.sh amneziawg-test      # bats unit tests (tests/*.bats)
   .github/scripts/smoke-test.sh amneziawg-test     # what CI runs, ~1 min
   # if you changed .github/scripts/:
   .github/scripts/next-version.test.sh && .github/scripts/release-tags.test.sh
   ```

   With containerd, set `ENGINE=nerdctl` for both test scripts. CI also runs shellcheck, hadolint and actionlint. Run them before you push.

   To build for more than one architecture, use `docker buildx build --platform linux/amd64,linux/arm64 .`
4. Update the user documents that the change affects (`README.md`, `docker-compose.yml`, `docs/`). Each topic has one file. `.github/copilot-instructions.md` gives the list.
5. Write [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`). Then open a PR against `master`.

## Style

- Shell and s6 scripts: 4-space indent, `#!/usr/bin/with-contenv bash` and `# shellcheck shell=bash`, executable bit set. Put side effects in `main()`, so that the unit tests can source the script.
- Dockerfile and YAML: 2-space indent (see `.editorconfig`).
- Write a comment only if the code needs an explanation.

## Releases

If a merge to `master` changes the image content, CI publishes a new image automatically. The image content is `Dockerfile`, `root/`, `.dockerignore`, the build workflow and `.github/scripts/`. Do not create `v*` tags manually: they are the build counter.

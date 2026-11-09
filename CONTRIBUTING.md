# Contributing

We accept issues and pull requests. Use the issue templates. For security problems, use the procedure in [SECURITY.md](SECURITY.md).

1. Fork the repository, then make a branch: `feature/<name>`.
2. Read [`.github/copilot-instructions.md`](.github/copilot-instructions.md). It gives the architecture, the invariants, the code style, and the commands to build and test. The host needs Docker, but no Go toolchain. CI runs the same commands.
3. Update the documents that the change affects. Each topic has one file. The developer instructions give the list.
4. Write [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`). Then open a PR against `master`.

If a merge to `master` changes the image content, CI publishes a new image. Do not create `v*` tags manually: they are the build counter.

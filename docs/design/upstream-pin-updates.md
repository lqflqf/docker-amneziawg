# Design: automatic updates of base images and apk pins

Status: proposed. The repository does not implement this design yet.

This document extends `.github/workflows/upstream-check.yml`. The workflow updates the amneziawg-tools and amneziawg-go pins now. With this design, it also updates the base images and the `apk` pins in the `Dockerfile`.

## 1. Problem

The `Dockerfile` has three types of pins:

| Pin | Lines | Updated by (now) |
|---|---|---|
| amneziawg-tools, amneziawg-go (tag + commit) | `AMNEZIAWG_*` `ARG` defaults | `upstream-check.yml` |
| Base images (tag + digest) | The `FROM` lines of the stages | Dependabot (`docker`) |
| `apk` packages (`name=version`) | The `apk add` blocks of the stages | Manual |

These problems occur now:

- Alpine keeps only the newest version of each package in a branch. When Alpine publishes a fix, the old version goes out of the index. Then each build fails until a person updates the pin.
- Dependabot can change a `FROM` line, but it cannot change the `apk` pins. A new Alpine branch makes all `apk` pins incorrect, so a Dependabot PR for a new branch cannot pass.
- `golang:1.27.3-alpine` does not show its Alpine branch. A new digest can move the `go-builder` stage to a different branch without a visible change.
- No automatic process brings security fixes from Alpine packages into the image.

## 2. Decisions

| Item | Decision |
|---|---|
| Alpine | Update minor and patch versions (`3.Y.Z`). The major version stays 3. |
| Go | Update minor and patch versions (`1.X.Y`). The major version stays 1. |
| `apk` | Update to the newest version in the selected Alpine branch. |
| Pull requests | One PR for all changes, with the amneziawg pins. |
| Smoke failure | Stop the run. Open no PR. There is no fallback set. |

## 3. Scope

In scope:

- The three `FROM` lines and their digests.
- Each `name=version` pin in each `apk add` block of the three stages.
- The checks, the PR and the documents for these pins.

Out of scope:

- Packages from Alpine `edge` or from upstream sources. The design uses only the stable branch that it selects.
- Pins for packages that come in as dependencies (transitive packages). Alpine selects these packages at build time.
- A major update of Alpine or Go.

Package versions follow the Alpine stable branch. Example: a stable branch gets only security and bug fixes for `dnsmasq`. A new upstream `dnsmasq` release comes in with the next Alpine branch.

## 4. Selection of a candidate set

A candidate set is one complete group of values: the Alpine branch, the tag and digest of each stage, and each `apk` pin. The workflow selects a candidate set as one transaction.

### 4.1 Images

| Image | Tag rule |
|---|---|
| `alpine` | Strict `^3\.[0-9]+\.[0-9]+$`. Use the highest patch in the branch. |
| `golang` | Strict `^1\.[0-9]+\.[0-9]+-alpine3\.[0-9]+$`. Use the highest Go version for the branch. |

- Use the digest of the multi-arch index, as Dependabot does now. Get it with `docker buildx imagetools inspect`.
- The index must have `linux/amd64` and `linux/arm64`.
- If the tag is the same and the digest changed, update the digest.
- The Go version must not be lower than the current Go pin.

### 4.2 Algorithm

1. Read the current Alpine branch from the runtime `FROM` line.
2. List all Alpine branches from the newest down to the current branch.
3. For each branch, from the newest:
   1. Select the `alpine` tag and the `golang` tag. If one of them is not available, reject the branch.
   2. Get each digest. Make sure that each index has amd64 and arm64.
   3. Resolve each `apk add` block (see 4.3) on amd64 and on arm64.
   4. Make sure that the amd64 version and the arm64 version of each package are the same.
   5. If all steps pass, select this branch and stop.
4. Keep no value from a rejected branch.
5. If the current branch also fails, stop the run with an error.

### 4.3 Resolution of apk versions

Resolve each `apk add` block separately, in the base image of its own stage:

| Block | Base image |
|---|---|
| `go-builder` | the selected `golang@<digest>` |
| `tools-builder` | the selected `alpine@<digest>` |
| runtime, s6 stack | the selected `alpine@<digest>` |
| runtime, other packages | the selected `alpine@<digest>` |

Commands in a container of that image and platform:

```sh
apk add --no-cache <names> >/dev/null
apk query --from installed --fields name,version --format json -- <names>
```

- Use the names without versions, so that `apk` selects the newest version.
- Do not use `apk info -v` with a prefix match. That output does not separate `s6` from `s6-rc`.
- Each name must give exactly one result. If a result is missing or extra, reject the branch.
- Before the implementation, make sure that `apk query` is available in Alpine 3.24.

### 4.4 Error types

| Error | Result |
|---|---|
| Tag, image or package not available | Reject the branch. Try the next branch. |
| Network error, registry error, install-script error | Retry 3 times with a delay of 10 s, 30 s and 60 s. Then stop the run. |
| amd64 and arm64 versions are different | Stop the run. The next daily run tries again. |

Do not reject a branch because of a network error. That can select a lower branch by mistake.

## 5. Version rules

- Each pin belongs to one block in one stage. A name in two stages (`git`, `build-base`, `bash`) can have a different version in each stage.
- If the Alpine branch stays the same, a new `apk` version must not be lower than the current one. Compare with `apk version -t`.
- If the Alpine branch changes, accept all versions of the new branch. The PR marks each version that is lower.
- A digest has no order. The PR shows a digest change as a replacement, with the old and the new digest.

### 5.1 Validation

The workflow examines each value before it writes the value:

| Value | Rule |
|---|---|
| `alpine` tag | `^3\.[0-9]+\.[0-9]+$` |
| `golang` tag | `^1\.[0-9]+\.[0-9]+-alpine3\.[0-9]+$` |
| Digest | `^sha256:[0-9a-f]{64}$` |
| `apk` version | `^[0-9][0-9A-Za-z._]*-r[0-9]+$` |
| `apk` name | Must be a name in the same block of the current `Dockerfile` |

Tag names and versions come from outside the repository. Give them to shells only through environment variables, as the workflow does now.

## 6. Candidate artifact

`check-upstream` writes one file, `candidate.json`, and uploads it as an artifact. The other jobs download it. This file is the only source of values for `smoke` and `open-pr`.

```json
{
  "schema": 1,
  "source_sha": "<github.sha>",
  "amneziawg": {
    "tools": { "old": "v3.1.20260812", "new": "", "commit": "" },
    "go":    { "old": "v3.1.20260828", "new": "", "commit": "" }
  },
  "alpine_branch": { "old": "3.24", "new": "3.25" },
  "stages": [
    {
      "index": 0,
      "name": "go-builder",
      "image": "golang",
      "old": { "tag": "1.27.3-alpine",     "digest": "sha256:..." },
      "new": { "tag": "1.28.0-alpine3.25", "digest": "sha256:..." },
      "blocks": [
        { "index": 0, "apk": [ { "name": "git", "old": "2.54.0-r0", "new": "2.55.1-r0" } ] }
      ]
    }
  ]
}
```

- An empty `new` value means "no change".
- The jobs identify a stage by the order of its `FROM` line. The runtime stage has no `AS` name.
- A consumer examines the file with a fixed schema and an allowlist of keys.
- A consumer makes sure that `source_sha` is the same as `github.sha`.
- Never `source` the file and never use `eval` on it.
- Pin the upload and download actions to a commit SHA.

## 7. Workflow jobs

```
check-upstream   (contents: read)
  1. amneziawg tags -> commits             (no change)
  2. candidate set (section 4)             (new)
  3. changed = one or more values differ
  4. upload candidate.json

smoke            (contents: read)          any failure stops the run
  1. update-pins.test.sh
  2. download and examine candidate.json
  3. update-pins.sh candidate.json
  4. build linux/amd64 -> integration-test.sh, smoke-test.sh
  5. Trivy: fixable CRITICAL fails (as in the docker-build.yml smoke job)
  6. build linux/arm64 under QEMU -> arm64-check.sh

open-pr          (contents: write, pull-requests: write)
  1. download and examine candidate.json
  2. update-pins.sh candidate.json
  3. create or update one PR on chore/bump-upstream-versions
```

- `check-upstream` needs QEMU (`docker/setup-qemu-action`) for the arm64 resolution.
- Move the arm64 runtime check from the release job of `docker-build.yml` into `.github/scripts/arm64-check.sh`. Both workflows call it.
- Trivy and the updater tests run inside this workflow. Thus these gates do not depend on `UPSTREAM_PR_TOKEN`.
- Set a `timeout-minutes` on `smoke`. A Go build under QEMU is slow.

## 8. `update-pins.sh`

### 8.1 Interface

```
update-pins.sh <candidate.json>
```

The script replaces the current positional interface. Only `upstream-check.yml` calls the script.

### 8.2 Behavior

1. Examine all values in the file (section 5.1).
2. Preflight. Make sure that each of these items occurs exactly once in its stage or block:
   - each `FROM` line with its old tag and old digest
   - each `ARG AMNEZIAWG_*` line
   - each `name=old` pin
3. Write a full new file to a temporary path.
4. Examine the new file again. Each new value must occur exactly once in its stage or block.
5. Move the new file over the `Dockerfile` in one step (`mv`).

If a step fails, the `Dockerfile` does not change.

- Accept an old `golang` tag without a branch (`1.27.3-alpine`).
- Always write the new `golang` tag with a branch (`1.27.3-alpine3.24`).
- Use `jq` to read the JSON. The runners have `jq`.

### 8.3 Tests (`update-pins.test.sh`)

Use a fixture `Dockerfile` in the test. Test these cases:

- One package name with a different version in two stages.
- `s6` and `s6-rc` in the same block.
- A pin that is not in the file, or that occurs more than once.
- A change to the digest only.
- A tag without a branch that changes to a tag with a branch.
- A value that does not agree with its pattern.
- After each failure, the fixture file is not changed.

Add the test to the step "Test the release scripts" in `docker-build.yml`.

## 9. The pull request

- Branch: `chore/bump-upstream-versions` (no change).
- Title: `chore: bump upstream versions` (no change).
- Commit: the same title, with the change list in the body. Use `sign-commits: true` (no change).

The body has these parts:

1. A table with the columns component, old and new. One row for each changed item.
2. A mark on each minor update of Alpine or Go.
3. A mark on each `apk` version that is lower after a change of branch.
4. The digest replacements.
5. The `apk` changes, grouped by stage.
6. The source SHA, the sha256 of `candidate.json` and a link to the run.

The PR body says "the same direct pins and base digests" as the tested image. It does not say "the same build". Alpine can change transitive packages between the test and the merge.

The workflow can update an open PR after a person reviews it, as it does now. The run link and the source SHA identify the tested values.

## 10. Effect on releases

### 10.1 Each bump PR

| Step | Build | Publish |
|---|---|---|
| `upstream-check` smoke | amd64 and arm64 | No |
| PR checks (only with `UPSTREAM_PR_TOKEN`) | amd64 | No |
| Merge to `master` | Yes, because the `Dockerfile` changed | `-r<N+1>`, `:<tools>`, `:latest` |

- If the amneziawg-tools version changes, `N` starts again (`next-version.sh`).
- Alpine often publishes package fixes and new digests. Expect more releases than now.

### 10.2 The PR that implements this design

The release gate (`IMAGE_PATHS` in `docker-build.yml`) builds when one of these paths changes: `Dockerfile`, `root/`, `cmd/`, `internal/`, `go.mod`, `go.sum`, `.golangci.yml`, `.dockerignore`, `docker-build.yml`, `.github/scripts/`. A change to Go tests alone does not publish.

| File | Release on merge |
|---|---|
| `.github/workflows/upstream-check.yml` | No |
| `.github/dependabot.yml`, `.github/copilot-instructions.md` | No |
| `.github/scripts/update-pins.sh`, `update-pins.test.sh`, `arm64-check.sh` | Yes |
| `docker-build.yml` | Yes |
| `Dockerfile` | Yes |

Thus this PR publishes `-r<N+1>`. The image content is the same, because no pin or digest changes.

To merge it without a release, do these steps:

- Put the new scripts out of `.github/scripts/`.
- Do not change `docker-build.yml` or the `Dockerfile`.
- Let the first bump PR change the `golang` tag.

The cost: the arm64 check stays in two places, and the PR test step does not run `update-pins.test.sh`.

## 11. Reliability

- Docker Hub: if the repository has a Docker Hub secret, log in. Else, use anonymous access. Retry with a delay.
- The step summary gives the cause of each failure and the rejected branches.
- A minor update that fails the smoke test blocks the amneziawg bumps. Each daily run then fails at `smoke` until a person fixes the cause.

## 12. Changes to other files

| File | Change |
|---|---|
| `.github/dependabot.yml` | Remove the `docker` section. Keep `github-actions`. |
| `Dockerfile`, comment above the first `FROM` | Change it: `upstream-check.yml` updates the base images. |
| `Dockerfile`, both `golang` `FROM` lines | Add the Alpine branch to the tag, for example `golang:1.27.3-alpine3.24`. |
| `docker-build.yml`, the comment about Dependabot | Change it. |
| `docker-build.yml`, the script-test step | Add `update-pins.test.sh`. |
| `docker-build.yml`, the arm64 boot check of the release job | Call `arm64-check.sh`. |
| `.github/copilot-instructions.md` | Update the Dockerfile and CI/CD sections. |
| `upstream-check.yml` header | Describe the base images and `apk` pins. |

## 13. Known effects

- One PR has all changes. A failure in one part stops all parts.
- The arm64 build under QEMU makes a run longer. This occurs only when a value changed.
- The workflow does not pin transitive packages. Thus two builds of the same commit can have different transitive packages.

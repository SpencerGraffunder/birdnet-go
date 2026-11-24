# Fork CI

This repository is a fork of <https://github.com/tphakala/birdnet-go> that ships
one artifact: a Linux container image (`ghcr.io/spencergraffunder/birdnet-go:dev`,
amd64 + arm64) deployed on a home Unraid server. Everything in this file exists
because GitHub counts a fork's automation against the fork's owner and has twice
disabled this fork's workflows with *"workflows are not currently being run on
this fork due to the scale of GitHub Actions usage"*.

When merging upstream, keep upstream's workflow files out of the fork except for
the set below. If a merge re-adds anything from the **Removed** list, delete it
again; each entry says why.

## What runs on a push to `main`

| Workflow | Purpose |
| --- | --- |
| `golangci-test.yml` | The gate: lint + sharded unit tests, plus the path-gated testcontainer, database and migration suites. Path-filtered to Go sources, `go.mod`/`go.sum`, `.golangci.yaml` and the CI helpers it uses. |
| `docker-build.yml` | Builds and publishes `:dev` (multi-arch) through the reusable `docker-build-push.yml`, which reuses `base-image.yml`. |
| `i18n-validation.yml`, `model-catalog-drift.yml`, `species-dict-drift.yml`, `golangci-lint-version.yml`, `pr-template-check-test.yml` | Fast data/pin checks that protect what ships. |

A push that touches only `.github/**` or `**.md` builds no image:
`docker-build.yml` lists `.github/workflows/**`, `.github/actions/**`,
`.github/scripts/**` and `**.md` in `paths-ignore`, because the image is built
from the `Dockerfile`, which provisions its own native libraries.

`concurrency.cancel-in-progress` on `docker-build.yml` means pushing twice in a
row cancels the first build instead of spending two.

## Runs on pull request or manual dispatch only

`macos-test.yml`, `windows-test.yml`, `cross-platform-build.yml`. Upstream runs
these on every push; they are the most expensive per-push minutes here (macOS
runners are the heavily weighted ones — a 15-minute worst case) and both native
suites are `continue-on-error` upstream, so they report rather than gate. This
fork ships Linux/amd64, and `docker-build.yml` is the compile gate that matters.
The `push` trigger is commented out in each file with a fork note.

## Removed from upstream

Deleted outright; a fork must not run them. Restore with
`git checkout <parent-of-deletion-commit> -- <path>` if upstream's behavior is
ever wanted here.

| Workflow | Why it cannot live on a fork |
| --- | --- |
| `wiki-sync.yml` | Push-triggered sync of `doc/wiki/` to the project wiki over `WIKI_SSH_DEPLOY_KEY`. That key belongs to the upstream project; on a fork the job can only fail or write somewhere its owner did not intend. |
| `automated-support-engineer.yml` | An LLM agent (`CLAUDE_CODE_OAUTH_TOKEN`) that drafts replies on other people's issues and pull-request comments. Community automation for upstream's users, and every comment costs inference. |
| `automated-test-engineer.yml` ("Test Failure Monitor") | Opens a GitHub issue whenever `golangci-test` fails. On this fork it fired five times and failed five times without filing anything useful. |
| `update-authors.yml`, `update-licenses.yml` | Scheduled/push bots that open maintenance pull requests against upstream metadata files. |
| `build-vm-images.yml` | Builds upstream's VM appliance images (heavy, push- and schedule-triggered). This fork ships no VM image. |
| `release-build.yml`, `release-manifest.yml`, `virustotal.yml`, `.github/actions/build-birdnet-binary/action.yml` | Upstream's release pipeline: GitHub Releases, Docker Hub, Sentry symbol upload and a VirusTotal scan of release artifacts. Releases are upstream's to publish; fork delivery is the `:dev` image. The composite action existed only for `release-build.yml`. |
| `build-version.yml` | Publishes the version string for the release pipeline (consumers removed above). The image build is independent: `docker-build-push.yml` derives its version by running `scripts/build-version.sh`. |

## Kept, with notes

- `scorecard.yml` (OpenSSF Scorecard) — left in place and in GitHub's
  `disabled_fork` state as a security signal; it has never run here.
- `cleanup-caches.yml` — re-enabled (GitHub parks scheduled workflows on forks).
  Scheduled runs do not fire on a fork, so clear cache by hand when needed:
  `gh cache list --repo SpencerGraffunder/birdnet-go` and
  `gh cache delete --all`. Symptom of a full cache: a job dies with
  `Process completed with exit code 22` plus
  `Failed to restore: "/usr/bin/tar" failed with exit code 2`, on every job that
  uses a cache, while local builds are clean. The repo cap is 10 GB and this
  fork reached 5.2 GB with nothing evicting it.
- `base-image.yml` — carries a fork patch: when this repo's registry has no base
  image but upstream published the identical tag, reuse
  `ghcr.io/tphakala/birdnet-go-base:<tag>` instead of rebuilding. The
  from-scratch recipe is currently broken by the ONNX removal below.
- `.github/actions/setup-onnxruntime` — carries a fork patch. Microsoft made
  `microsoft/onnxruntime` private on 2026-11-17 while investigating malicious
  content published under the org, which removed the repository **and every
  release asset**: the `releases/download/...` URLs answer 404 for all versions,
  so `curl -fsSL` exits 22 and every job using that action failed (this was the
  sole reason `golangci-test` was red on every push). GitHub Releases is still
  tried first, so this heals when upstream returns; Linux falls back to the
  `onnxruntime` wheel from PyPI — Microsoft's own package, with PyPI's published
  sha256 verified before unpacking. Only the libraries are needed, since ONNX
  Runtime is `dlopen`ed at runtime and never linked at compile time.
- No Actions secrets, variables, environments or Pages exist on this fork;
  anything a workflow expects under `secrets.*` is unset here by design.

## Branches, tags and inherited config

- **Branches:** the fork carries exactly one branch, `main`. All 304 branches that a
  fork inherits from upstream (every `fix/*`, `feature/*`, `feat/*`, `chore/*`,
  `dependabot/*`, `release/v0.7`, `backup`, plus upstream's AI-agent scratch branch) were
  deleted on 2026-11-24 after verifying each was byte-identical to the same-named upstream
  branch, so none held fork work; all still exist on `tphakala/birdnet-go`. Rollback
  manifest and restore script were kept off-repo at
  `~/.hermes/cache/scratch/bnb-branch-rollback.tsv` / `bnb-branch-restore.sh`. Upstream
  merges come from the upstream remote's `main`; nothing here needs upstream's branches.
- **Tags:** all 102 upstream tags are kept. The version stamp derives from them
  (`scripts/build-version.sh` / `git describe`), and deleting them would break the image's
  version string and every `git describe` in the repo.
- **Inert inherited config, kept on purpose** because these files cannot execute anything on
  a fork and deleting them would re-conflict on every upstream merge: `dependabot.yml`
  (Dependabot is not enabled on this fork — it has never opened a PR here),
  `coderabbit.yaml` (the CodeRabbit app is not installed on this fork),
  `pull_request_template.md`, and `FUNDING.yml`, whose sponsor link still points at the
  upstream author, which is the correct destination for a derivative work.


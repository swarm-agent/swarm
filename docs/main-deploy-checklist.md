# Main Deploy Checklist

This file is the canonical operator checklist for promoting `dev` to `main`, publishing a versioned GitHub Swarm release, and capturing the minimum safety checks before release.

## Current git layout

- `dev` is the day-to-day integration branch.
- `main` is the protected release/build branch.
- Pushes to `main` trigger `.github/workflows/build-main.yml`, which builds and verifies a release candidate but cannot tag or publish it.
- Stable publication is a separate manual workflow dispatch from `main` with `publish=true` and an explicit stable `release_version`; the `publish-stable` job is protected by the `stable-release` GitHub Environment.
- Record the exact `origin/main`, `origin/dev`, promotion range, and selected release tag during each release instead of maintaining a stale fixed snapshot here.

## Push and key model

- GitHub branch protection is actor-based, not SSH-key-based.
- If a remote server authenticates to GitHub as the same GitHub actor you use locally, GitHub cannot distinguish the server key from your local key for branch-push authorization.
- To keep `main` human-only:
  - your local machine should be the only machine using the GitHub actor that is allowed to update `main`
  - remote servers must not authenticate to GitHub as that same actor
- For remote servers:
  - if they only need pull access, use read-only deploy keys
  - if they must push to `dev`, prefer a separate machine identity or GitHub App and rely on `main` protection to block that identity from `main`
- Do not rely on "same GitHub user, different SSH key" as the control for protecting `main`.

## Canonical release artifact

- The downloadable Swarm release is the full runtime bundle already defined by `cmd/swarmsetup` and `internal/launcher/launcher.go`.
- The GitHub release assets include `swarm-<version>-linux-amd64.tar.gz` and its exact `swarm-<version>-linux-amd64.tar.gz.sha256` checksum.
- After extraction, the user installs it with:

```bash
./swarmsetup --artifact-root /path/to/extracted/swarm-<version>-linux-amd64
```

- That install path provides the real installed runtime and the user-facing `swarm` launcher.
- Fresh shells that do not yet include `${XDG_BIN_HOME:-$HOME/.local/bin}` on `PATH` must use `${XDG_BIN_HOME:-$HOME/.local/bin}/swarm` until the shell startup files are updated and a new shell is opened.

## Canonical version reference

- The preferred public release version is a stable semver tag such as `v0.x.y` on the promoted `main` commit.
- The operator selects an explicit stable semver version when manually dispatching publication from `main`. The workflow rejects missing or malformed versions and existing tags.
- The candidate build and checksum/archive verification jobs complete before the environment-protected publication job creates the release and tag.
- `dist/build-info.txt` carries release metadata (`version`, `commit`, `actor`, `ref`, `built_at`) but is not itself the tag authority.

## Main release checklist

### 1. Select the candidate

- [ ] Confirm the exact promotion range with `git log --oneline main..dev`
- [ ] Confirm whether the `main`-only commit (`Add main branch build workflow and branch flow docs`) must be preserved, merged, or recreated in the promoted history
- [ ] Freeze the release candidate to one explicit `dev` SHA

### 2. Repo safety and hygiene

- [ ] Ensure the working tree is clean
- [ ] Run `./scripts/check-precommit.sh`
- [ ] Run `bash scripts/check-launch-readiness.sh --require-clean`
- [ ] Re-read clone audit findings for secrets, plaintext storage, logging, and networking gotchas relevant to the downloadable release bundle

### 3. Secrets and auth review

- [ ] Confirm no tracked `.env` or `.swarmenv` files exist beyond examples
- [ ] Confirm no real keys, tokens, cookies, or passwords appear in tracked files, fixtures, screenshots, or docs
- [ ] Confirm credential storage still uses the secret-store path where expected
- [ ] Confirm no release command puts raw secrets directly on the command line

### 4. Main protection model

- [ ] GitHub `main` protection blocks force pushes and deletions
- [ ] GitHub `main` protection blocks direct updates from all non-owner actors
- [ ] Remote machines cannot authenticate to GitHub as the same actor that is allowed to push `main`
- [ ] PR merge to `main` and direct owner push to `main` both match the intended owner-approved release path

### 5. Versioning and promotion

- [ ] Confirm the intended release tag is correct for the promoted `main` commit
- [ ] Merge the approved release commit set from `dev` to `main`
- [ ] Create or verify the annotated release tag when publishing a stable release
- [ ] Verify the workflow published the expected GitHub release name/tag
- [ ] Record the released `main` SHA and `build-info.txt` metadata
- [ ] Update this checklist baseline after the release

### 6. After push

- [ ] Verify the GitHub `main` release workflow ran for the promoted commit
- [ ] Verify the uploaded artifacts include the full Swarm runtime bundle, `build-info.txt`, and the exact `.sha256` checksum asset
- [ ] Verify the candidate workflow completed without creating a tag or release
- [ ] Manually dispatch stable publication from the reviewed `main` SHA with `publish=true` and the approved `release_version`
- [ ] Approve the `stable-release` Environment deployment only after reviewing candidate evidence
- [ ] Verify the GitHub release includes `swarm-<version>-linux-amd64.tar.gz` and `swarm-<version>-linux-amd64.tar.gz.sha256`
- [ ] Verify the install flow from the extracted release works with `swarmsetup --artifact-root ...`
- [ ] Verify `/update apply` fails closed on missing or mismatched checksum metadata, then succeeds with the published checksum
- [ ] Verify `/update apply` exits the TUI, shows terminal progress, relaunches, and shows the post-update success toast
- [ ] Run the final end-to-end launch review and checked-in vulnerability scans; agents may independently inspect evidence, but the release owner must verify and synthesize the results

## Relevant filepaths

- `README.md`
- `.github/workflows/build-main.yml`
- `scripts/build-main-dist.sh`
- `scripts/check-precommit.sh`
- `scripts/check-launch-readiness.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/launcher.go`
- `internal/model/home.go`

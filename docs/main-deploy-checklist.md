# Main Deploy Checklist

This file is the canonical operator checklist for promoting `dev` to `main`, testing the reviewed candidate on supported Linux, and only then publishing a versioned GitHub Swarm release.

## Current git layout

- `dev` is the day-to-day integration branch.
- `main` is the protected release/build branch.
- Pull requests and pushes to `main` run `.github/workflows/build-main.yml`, which builds and verifies a release candidate but cannot tag or publish it.
- Stable publication is intentionally absent during Phase 1. A later reviewed phase must add a separate protected publication/deployment path that consumes this exact verified candidate without rebuilding it.
- Record the exact `origin/main`, `origin/dev`, promotion range, and selected release tag in the promotion PR or release record instead of maintaining a stale fixed snapshot here.

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
./swarmsetup --artifact-root /path/to/extracted/swarm-<version>-linux-amd64 --service
```

- `--service` is explicit because direct `swarmsetup` and blank interactive install choices default to files-only and never enable/start systemd implicitly.
- That install path provides the real installed runtime and the user-facing `swarm` launcher.
- Fresh shells that do not yet include `${XDG_BIN_HOME:-$HOME/.local/bin}` on `PATH` must use `${XDG_BIN_HOME:-$HOME/.local/bin}/swarm` until the shell startup files are updated and a new shell is opened.

## Canonical version reference

- The preferred public release version is a stable semver tag such as `v0.x.y` on the promoted `main` commit.
- Phase 1 candidate versions are event-derived (`pr-*`, `main-*`, or `dispatch-*`) and are not stable release tags.
- Building `release-candidate-<version>` does **not** publish it. The candidate build and hermetic archive/install smoke complete before evidence upload.
- Creating a Git tag or GitHub release is publication and is unavailable in the Phase 1 workflow. A later reviewed phase must restore publication behind the approved Environment and policy gates.
- The `release-candidate-<version>` workflow artifact is the evidence bundle: it contains the candidate archive, exact `.sha256` file, keyless Sigstore bundle, GitHub provenance bundle, `build-info.txt`, and `smoke-evidence.txt`. The build job verifies checksum, signer identity, provenance, source SHA/ref, workflow identity/SHA, issuer, event, and hosted-runner status before upload.
- Candidate evidence is per SHA and workflow run. Never treat an older artifact, checksum, smoke transcript, or fixed branch snapshot in documentation as evidence for a newer commit.
- `dist/build-info.txt` carries release metadata (`version`, `commit`, `actor`, `ref`, `built_at`) but is not itself the tag authority.

## Main release checklist

### 1. Select the candidate

- [ ] Confirm the exact promotion range with `git log --oneline main..dev`
- [ ] Confirm whether the `main`-only commit (`Add main branch build workflow and branch flow docs`) must be preserved, merged, or recreated in the promoted history
- [ ] Freeze the release candidate to one explicit `dev` SHA and record it in the promotion PR or release record with `git rev-parse HEAD`

### 2. Repo safety and hygiene

- [ ] Ensure the working tree is clean
- [ ] Run `./scripts/check-precommit.sh`
- [ ] Run `bash scripts/check-launch-readiness.sh --require-clean`; this includes `scripts/check-launch-defaults.sh` assertions for loopback-only binding, permission/diagnostic/output-retention defaults, config privacy, explicit service choice, default permission redaction, preservation-oriented uninstall, and update rollback. It also rejects untracked artifacts and unexpected non-text blobs while reporting the explicitly reviewed FFF libraries and public web/PWA icons.
- [ ] Retain full precommit and launch-readiness output with the exact candidate SHA
- [ ] Build the candidate and run `TMPDIR="${TMPDIR:?}" ./scripts/smoke-release-archive.sh <archive.tar.gz> <archive.tar.gz.sha256> --evidence <smoke-evidence.txt>` when reproducing the CI smoke locally
- [ ] Record the candidate archive/checksum/build metadata/smoke evidence location, or mark it pending until the exact-SHA workflow runs; do not fabricate or reuse evidence from another SHA
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

### 5. Open and review the promotion PR

- [ ] Open the single `dev` -> `main` promotion PR for the frozen candidate SHA
- [ ] Confirm the PR workflow builds an installable candidate artifact without creating a tag or GitHub release
- [ ] Complete code review and required checks before merging the approved commit set to `main`
- [ ] Verify the non-publishing `main` push workflow succeeds for the reviewed merge commit
- [ ] Record the reviewed `main` SHA; all remaining evidence must refer to this SHA

### 6. Prepare the exact versioned candidate without publishing

- [ ] Confirm the proposed stable tag is correct for the reviewed `main` commit
- [ ] Manually dispatch the non-publishing workflow from the reviewed `main` SHA
- [ ] Download `release-candidate-dispatch-<short-sha>` and verify it contains the full Swarm runtime archive, exact `.sha256` checksum, `.sigstore.json` signature bundle, `.provenance.sigstore.json` provenance bundle, `build-info.txt`, and `smoke-evidence.txt`
- [ ] Independently run `scripts/verify-release-evidence.sh` with the recorded repository, source SHA/ref, workflow ref/SHA/identity, workflow name, and event; alter the artifact, bundles, identity, issuer expectation, and SHA expectations one at a time and confirm every changed case fails
- [ ] Review `smoke-evidence.txt` and the `Smoke release archive and artifact-root install` job log; confirm checksum, archive contents, disposable artifact-root install, and host-system-path isolation passed
- [ ] Confirm no stable tag or GitHub release exists and record the candidate artifact, workflow run, checksum, signature, provenance, and `build-info.txt` locations

### 7. Run post-PR supported-Linux lifecycle and onboarding tests

“Collect supported-Linux VM evidence” means retaining a transcript of the complete privileged lifecycle on a fresh supported Linux VM, starting with no Swarm installation. It is deliberately after PR review and before publication; the hermetic CI smoke does not cover real service-account metadata, systemd, or user onboarding.

- [ ] Start from a clean supported-Linux VM or restored clean snapshot with no prior Swarm install; record the OS image and candidate SHA/version
- [ ] Install the exact versioned candidate through the documented systemd path and verify service-account ownership, group, and `0600` mode for the canonical config
- [ ] Test real systemd start, `swarm status`, `swarm open`/health reachability, stop, and restart
- [ ] Complete Desktop onboarding from first launch, including identity, provider, workspace selection, and successful first use
- [ ] Complete the terminal/CLI first-run onboarding path and successful first use
- [ ] Complete TUI onboarding from first launch, including identity, provider, workspace selection, and successful first use
- [ ] Exercise the Desktop update action and the terminal/TUI `/update apply` path; verify progress, process handoff/relaunch, the post-update version, and the success notification where that surface provides one
- [ ] Test reinstall/update over an existing installation and verify config, data, onboarding state, service-account owner/group, and config mode are preserved
- [ ] Verify update apply fails closed for missing or mismatched checksum metadata, then succeeds with the exact candidate checksum
- [ ] Force apply-time and boot-time update failures; verify rollback restores and restarts the last working runtime
- [ ] Run default uninstall and verify the documented config/data are retained; record anything left behind and confirm it matches the retention contract
- [ ] Attach the full VM transcript and results to the release record for the exact candidate

For the first stable release, there is no older public stable version from which to prove a public-channel upgrade. Test the candidate update/apply mechanics with controlled candidate artifacts before publication, then perform a live update-discovery sanity check after publication. Starting with the second stable release, an update from the previous public stable version to the exact candidate is a mandatory pre-publication gate.

### 8. Final go/no-go and publication

- [ ] Run the final end-to-end launch review and checked-in vulnerability scans; agents may independently inspect evidence, but the release owner must verify and synthesize the results
- [ ] Confirm the exact reviewed `main` SHA has passing PR/main builds, checksum and hermetic smoke evidence, supported-Linux lifecycle evidence, all three onboarding paths, and update/rollback evidence
- [ ] Make the final go/no-go decision; if any gate failed, leave the `stable-release` Environment deployment unapproved and replace the candidate through a new reviewed PR
- [ ] Do not publish from the Phase 1 workflow; publication remains unavailable until a later reviewed phase adds the protected `stable-release` path
- [ ] After that later phase is implemented, verify the GitHub release name/tag and that it includes the archive, checksum, Sigstore bundle, and provenance bundle
- [ ] Verify live update discovery against the published metadata; for releases after the first, retain proof that the previous public stable updates successfully to this version
- [ ] Record the released `main` SHA and `build-info.txt` metadata, then update this checklist baseline

## Relevant filepaths

- `README.md`
- `.github/workflows/build-main.yml`
- `scripts/build-main-dist.sh`
- `scripts/check-precommit.sh`
- `scripts/check-launch-readiness.sh`
- `scripts/smoke-release-archive.sh`
- `scripts/verify-release-candidate.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/launcher.go`
- `internal/model/home.go`

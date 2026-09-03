# Main Deploy Checklist

This file is the canonical operator checklist for promoting `dev` to `main`, testing the reviewed candidate on supported Linux, and only then publishing a versioned GitHub Swarm release.

## Current git layout

- `dev` is the day-to-day integration branch.
- `main` is the protected release/build branch.
- Pull requests to `main` must update `CHANGELOG.md`; `.github/workflows/require-changelog.yml` enforces the release-note gate.
- Pull requests to `dev`/`main` and pushes to `dev` run `.github/workflows/install-distro-smoke.yml`, which builds one exact candidate and verifies fresh Ubuntu/Arch systemd installation. Pushes to `main` and manual dispatch run `.github/workflows/build-main.yml`, which builds, tests, signs, attests, and verifies the release candidate.
- On a protected `main` push produced by the user's approved PR merge, the same workflow re-verifies that run's exact evidence set without rebuilding it, then automatically creates the stable tag and GitHub Release.
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
- Creating a Git tag or GitHub release is publication. The protected `main` push workflow performs it automatically only after the user merges the PR and candidate verification succeeds.
- The `release-candidate-<version>` workflow artifact is the evidence bundle: it contains the candidate archive, exact `.sha256` file, keyless Sigstore bundle, GitHub provenance bundle, `build-info.txt`, and `smoke-evidence.txt`. The build job verifies checksum, signer identity, provenance, source SHA/ref, workflow identity/SHA, issuer, event, and hosted-runner status before upload.
- Candidate evidence is per SHA and workflow run. Never treat an older artifact, checksum, smoke transcript, or fixed branch snapshot in documentation as evidence for a newer commit.
- `dist/build-info.txt` carries release metadata (`version`, `commit`, `actor`, `ref`, `built_at`) but is not itself the tag authority.

## Main release checklist

### 1. Select the candidate

- [ ] Confirm the exact promotion range with `git log --oneline main..dev`
- [ ] Confirm whether the `main`-only commit (`Add main branch build workflow and branch flow docs`) must be preserved, merged, or recreated in the promoted history
- [ ] Freeze the release candidate to one explicit `dev` SHA and record it in the promotion PR or release record with `git rev-parse HEAD`
- [ ] Update the `CHANGELOG.md` Unreleased entry for the complete promotion range, including its `Docs impact` subsection

### 2. Repo safety and hygiene

- [ ] Ensure the working tree is clean
- [ ] Run `./scripts/check-precommit.sh`
- [ ] Run `bash scripts/check-launch-readiness.sh --require-clean`; this includes `scripts/check-launch-defaults.sh` assertions for loopback-only binding, permission/diagnostic/output-retention defaults, config privacy, explicit service choice, default permission redaction, preservation-oriented uninstall, and update rollback. It also rejects untracked artifacts and unexpected non-text blobs while reporting the explicitly reviewed FFF libraries and public web/PWA icons.
- [ ] Retain full precommit and launch-readiness output with the exact candidate SHA
- [ ] Build the candidate and run `TMPDIR="${TMPDIR:?}" ./scripts/smoke-release-archive.sh <archive.tar.gz> <archive.tar.gz.sha256> --evidence <smoke-evidence.txt>` when reproducing the hermetic CI smoke locally
- [ ] Confirm the exact candidate passes the PR/build `install-distro-smoke` Ubuntu and Arch jobs. The workflow builds one archive/checksum pair and reuses it across the matrix. Each job starts from the official minimal image, installs only the downloader/certificate/sudo/systemd bootstrap, proves Git is absent, downloads that exact checksum-bound candidate over HTTP into a new user's home, runs the candidate's `install.sh --service --yes` with `TMPDIR` unset, verifies the installer provisioned Git, and requires active service plus healthy daemon readiness before invoking the installed CLI and checking the canonical runtime owner.
- [ ] Confirm the testbench `release-candidate` watcher uses that same one-build/many-cycles archive identity and that the Fireworks Desktop cycle starts Git-absent, installs through the candidate's public `install.sh`, verifies Git was provisioned, completes onboarding, and passes the managed-worktree Plan/Auto reconciliation flow before reporting success.
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
- [ ] Confirm the required changelog workflow passes for the exact PR range
- [ ] Confirm the PR `install-distro-smoke` workflow builds an exact installable candidate and passes Ubuntu/Arch from-zero checks without creating a tag or GitHub release
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

“Collect supported-Linux VM evidence” means retaining a transcript of the complete privileged lifecycle on fresh supported Linux environments, starting with no Swarm installation. Ubuntu and Arch install/start/CLI coverage is automated in the PR/build workflows; a full VM remains authoritative for onboarding and update/rollback. Omarchy publishes a full ISO rather than an OCI image, so its supported proof uses the official ISO's unattended `cidata` path, a reusable clean base on testbench, and a throwaway overlay. Run the opt-in `omarchy-install` lane with the exact candidate archive and explicit SSH target; do not substitute a plain Arch container and call it Omarchy.

- [ ] Confirm Ubuntu and Arch exact-SHA distro install jobs pass and retain their public CI result.
- [ ] Boot a clean official-ISO Omarchy overlay on testbench and run `scripts/run-testbench-launch-prerun.sh --suite omarchy-install --candidate-archive <archive.tar.gz> --omarchy-guest <user@host>` with any required explicit port/identity options; retain the bounded result privately without recording connection details in public source.
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
- [ ] Make the final go/no-go decision before merging; if any gate failed, leave the PR unmerged and replace the candidate through a new reviewed PR
- [ ] Manually merge the reviewed PR; confirm the resulting `main` workflow automatically builds, reverifies, and publishes without a second deployment approval
- [ ] Verify the GitHub release name/tag and that it includes the archive, checksum, Sigstore bundle, provenance bundle, build metadata, and smoke evidence
- [ ] Verify live update discovery against the published metadata; for releases after the first, retain proof that the previous public stable updates successfully to this version
- [ ] Record the released `main` SHA and `build-info.txt` metadata, then update this checklist baseline

## Relevant filepaths

- `README.md`
- `.github/workflows/build-main.yml`
- `.github/workflows/require-changelog.yml`
- `scripts/check-changelog.sh`
- `scripts/build-main-dist.sh`
- `scripts/check-precommit.sh`
- `scripts/check-launch-readiness.sh`
- `scripts/smoke-release-archive.sh`
- `scripts/test-install-distro.sh`
- `scripts/test-install-omarchy-vm.sh`
- `scripts/run-testbench-launch-prerun.sh`
- `.github/workflows/install-distro-smoke.yml`
- `scripts/verify-release-candidate.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/launcher.go`
- `internal/model/home.go`

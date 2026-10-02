# Main Deploy Checklist

This file is the canonical operator checklist for promoting `dev` to `main`, testing the reviewed candidate on supported Linux, and only then publishing a versioned GitHub Swarm release.

## Current git layout

- `dev` is the day-to-day integration branch.
- `main` is the protected release/build branch.
- Pull requests to `main` must update `CHANGELOG.md`; `.github/workflows/require-changelog.yml` enforces the release-note gate.
- Pull requests to `dev`/`main` and pushes to `dev` run `.github/workflows/install-distro-smoke.yml`, which builds one exact candidate and verifies fresh Ubuntu/Arch systemd installation. Pushes to `main` and manual dispatch run `.github/workflows/build-main.yml`, which downloads independently verified GCP-built bytes for the exact merged source and stable version, then signs, attests promotion, and verifies without rebuilding.
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
- That install path provides the real installed runtime and the user-facing `swarm` launcher. Git is not bundled as a private binary in the archive: the installer must provision the supported distribution's Git package when absent, verify it before Swarm mutation, and fail closed if no supported package manager can satisfy the prerequisite.
- Fresh shells that do not yet include `${XDG_BIN_HOME:-$HOME/.local/bin}` on `PATH` must use `${XDG_BIN_HOME:-$HOME/.local/bin}/swarm` until the shell startup files are updated and a new shell is opened.

## Headless OCI and npm promotion

The same protected `stable-release` job publishes the independently qualified
`linux/amd64` OCI image to `ghcr.io/swarm-agent/swarm-headless`, preserving its
manifest digest with `skopeo copy --preserve-digests`. It then packs the SDK/CLI,
attaches those exact packs and `headless-image.json`/`npm-packages.json` to the
GitHub release, and publishes those packs to npm. Package names come from their
manifests; their versions come from `resolve-release-version.sh` (without `v`).
Only the temporary packaging copy receives the generated CLI `runtime-image.json`.
PRs and dispatches cannot publish. Native qualification is never OCI qualification.

Protected producer interface (run from the exact clean checkout; output outside
it, using approved digest pins, Docker buildx and Python 3):

```sh
bash scripts/build-headless-release.sh "$VERSION" "$SOURCE_SHA" "$BUILT_AT" "$BUILD_IMAGE" "$RUNTIME_IMAGE" "$OUTPUT_DIR"
python3 -B scripts/headless-release.py verify --archive "$OUTPUT_DIR/swarm-headless.oci.tar" --metadata "$OUTPUT_DIR/headless-image.json" --version "$VERSION" --source "$SOURCE_SHA"
```

The builder requires `golang:1.26.7-trixie@sha256:…` and
`debian:trixie-slim@sha256:…` under `docker.io/library`; it supplies no invented
pins and emits no qualification receipt. The protected producer must run actual
container startup, scoped-auth, SDK-session and restart qualification. Extend the
existing authenticated handoff as follows:

- Add `oci_archive` and `oci_metadata` immutable GCS references (`bucket`, `object`,
  `generation`, `sha256`) to `receipt.binding.artifacts`, build proof
  `result.outputs`, and the release manifest's copied qualified references.
  Originals belong to `package_bucket`; copies belong to `qualified_bucket`.
- Add `receipt.headless = {schema: "swarm.headless-qualification/v1", source_sha,
  run_id, image: <headless-image.json>, gates: {startup: "passed", scoped-auth:
  "passed", sdk-session: "passed", restart: "passed"}}` only after those tests
  succeed. Recompute the existing binding/admission/observed-authority digests
  normally. Do not retrofit fabricated receipts to old native qualification.
- Retain protected bucket/App/WIF policy and all existing native stages. The
  consumer rejects absent evidence, mismatched bytes/source/version/platform,
  and the former weak check-summary fallback. GitHub never rebuilds the image.

Remote setup remains an operator prerequisite, not an observed result: configure
reviewers and main-only branch restrictions on `stable-release`, GHCR package
write permission and public visibility, and a package-scoped `NPM_TOKEN` environment
secret authorized for both manifest package names. Credentials are not build
arguments or package content. npm publication is not transactional: if a package
fails after another succeeds, inspect registry state before recovery; never
republish changed bytes under the same version. No live registry or producer
qualification is implied by these scripts or hermetic tests.

## Canonical version reference

- The preferred public release version is a stable semver tag such as `v0.x.y` on the promoted `main` commit.
- Main push resolves the stable version from `resolve-release-version.sh`. Manual dispatch retains an unsigned `dispatch-<sha>` GCP candidate and never signs or publishes a stable release. A PR/tree-equivalent archive cannot be renamed or relabeled; changed commit/version/byte-affecting metadata requires fresh GCP qualification.
- Building `release-candidate-<version>` does **not** publish it. The candidate build and hermetic archive/install smoke complete before evidence upload.
- Creating a Git tag or GitHub release is publication. The protected `main` push workflow performs it automatically only after the user merges the PR and candidate verification succeeds.
- The `stable-release-<version>` artifact retains the unchanged archive/checksum, keyless signature, typed GCP promotion attestation, extracted `build-info.txt`, authenticated GCP qualification evidence, immutable manifest, GCP build provenance and promotion predicate. No synthetic smoke-pass file is created. Signature and promotion verification bind source, workflow, issuer and actual archive bytes; the promotion predicate does not claim a GitHub build.
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
- [ ] Complete Desktop onboarding from first launch, including identity, provider, and workspace selection. Prove a non-repository folder cannot be used temporarily or saved and explicit session worktree opt-out is rejected; an empty folder can be initialized only through the explicit repository-setup action; and an existing-file folder offers both `Talk to Onboarding Swarm` and `Fix manually and retry`. Verify the assistant uses the configured provider without any previously admitted workspace, stays bound to the exact selected folder, presents each Git/filesystem mutation for explicit permission, survives denial/failure and return to onboarding, and cannot save/select/finalize until the recheck sees the repository root's initial commit. Then prove the resulting committed repository reaches successful first use.
- [ ] Complete the terminal/CLI first-run onboarding path and successful first use
- [ ] Complete TUI onboarding from first launch, including identity, provider, workspace selection, and successful first use; plain and unborn repositories must remain blocked with actionable guidance.
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

## GCP promotion prerequisites

GCP configuration reads Repository Secrets first and Repository Variables second; a nonempty secret takes precedence. Existing secrets need not be moved. These configuration values are nonsecret identifiers/policy, not provider keys. Fork PRs do not receive repository secrets: supply the reviewed nonsecret variables for fork coverage, never switch to `pull_request_target` or expose credentials to candidate code. Before rollout, separately approve repository configuration `GCP_CHECK_APP_ID`, `GCP_WORKLOAD_IDENTITY_PROVIDER`, `GCP_READ_SERVICE_ACCOUNT`, `GCP_ALLOWED_BUCKETS` (JSON array), and `GCP_RELEASE_POLICY_JSON`. The static `swarm.gcp.release-policy/v1` policy contains `authority` (controller, builder, result_writer, provenance_verifier) and distinct receipt_bucket, qualified_bucket, package_bucket trust roles. Mandatory stage/onboarding coverage is fixed in the verifier; current source/attempts are bound to authenticated immutable evidence, not manually rewritten repository variables. Never derive trusted policy from downloaded evidence. Configure WIF for the exact reviewed workflow and read-only approved buckets; no build/deploy/write access. Pre-merge bootstrap may use separately approved `GCP_VERIFIER_SHA`, a full immutable commit containing the reviewed verifier: relay jobs verify checkout HEAD before execution. Missing configuration fails closed. Manual candidates are unsigned; only qualified main pushes reach protected signing/publication.

Retain `stable-release` environment reviewers and protected publication. Validate the producer/consumer schema and custom `https://swarm.dev/attestations/release-promotion/v1` verification with actual GitHub/Sigstore evidence before enabling publication. The checked-in installer/launcher do not invoke `gh attestation verify`; consumers of the old release verifier must explicitly choose `--legacy-slsa` for historical GitHub-built releases. New releases require `--gcp-promotion-dir` with the complete evidence set, never automatic legacy fallback.

### Maintained single-VM release handoff (separate from controller v2)

The protected `GCP_RELEASE_POLICY_JSON` may explicitly select
`{"schema":"swarm.gcp.runner-release-policy/v1","qualified_bucket":"APPROVED_BUCKET"}`.
`GCP_ALLOWED_BUCKETS` and the read-only WIF IAM binding must independently allow
that bucket. No receipt/package/controller buckets or identities are invented.
The legacy `swarm.gcp.release-policy/v1` path remains strict and separate; unknown
schemas, missing envelopes, dispatch and PR reuse fail closed on the runner path.

The producer must build **the merged main SHA**, detached, with the resolved stable
version. It must publish `build-main` using the configured GitHub App and the existing
`swarm.gcp.check-result/v1` identity fields (including push before/head SHA, exact
source/execution tree, repository ID, run ID, input digest, execution phase, passed
state and verified cleanup). Its `stages` are exactly the native keys below, each
`{"id":"KEY","status":"passed"}`. `release` names immutable manifest and receipt:

```json
{"release":{"manifest":{"bucket":"APPROVED_BUCKET","object":"candidate-BUILD/manifest.json","generation":"POSITIVE_GENERATION","sha256":"RAW_MANIFEST_SHA256"},"verification":{"bucket":"APPROVED_BUCKET","object":"candidate-BUILD/qualification.json","generation":"POSITIVE_GENERATION","sha256":"RAW_RECEIPT_SHA256"}}}
```

Every `REF` below has exactly `bucket`, `object`, positive `generation`, and raw-byte
`sha256` as above. Upload artifacts first, then receipt, then manifest; emit the App
check only after recording actual completed qualification. Manifest field contract:

```json
{"schema":"swarm.gcp.runner-release/v1","version":"v1.2.3","source_sha":"FULL_MAIN_SHA","source_tree":"FULL_TREE_SHA","repository_id":123,"run_id":"RUN","build_id":"BUILD","archive":"REF","checksum":"REF","oci_archive":"REF","oci_metadata":"REF","evidence":"RECEIPT_REF","native":{"source_sha":"FULL_MAIN_SHA","version":"v1.2.3","ref":"detached","actor":"ACTUAL_ACTOR","built_at":"ACTUAL_BUILD_TIME"}}
```

Replace symbolic REF strings with reference objects. Receipt has exactly:
- `schema: swarm.gcp.runner-qualification/v1`;
- identical `version`, `source_sha`, `source_tree`, `repository_id`, `run_id`,
  `build_id`, and `native` fields from the manifest;
- `artifacts`: the identical four `archive`, `checksum`, `oci_archive`,
  `oci_metadata` reference objects;
- `cleanup_verified: true` from observed cleanup;
- `native_exit_codes`: actual integer zero exit codes for **all and only**
  `source`, `setup`, `repository-policy`, `main-source-policy`, `changelog`,
  `dependency-vulnerabilities`, `critical-fast`, `critical-deep`, `critical-agents`,
  `version`, `build`, `package-smoke`, `ubuntu-sudo`, `arch-sudo`, `ubuntu-root`, `cleanup`;
- `headless: {"image": IMAGE_METADATA, "exit_codes": {"startup":0,"scoped-auth":0,"sdk-session":0,"restart":0}}`.
  IMAGE_METADATA is the exact `swarm.headless-image/v1` output of
  `scripts/headless-release.py inspect` over the **actually tested OCI archive**.
  Native test results must never stand in for these container gates.

`build-info.txt` inside the native archive must match `native` (its `commit` equals
`source_sha`). The checksum names `swarm-VERSION-linux-amd64.tar.gz`. OCI bytes,
labels, platform, manifest/config digests and receipt image metadata are independently
verified. `gcp-build-provenance.json` on this route retains the authenticated App
result, not a fictional runtime-build-proof/v1. The signed predicate uses
`swarm.runner-release-promotion/v1` under the existing release-promotion attestation
predicate type and binds the raw manifest/receipt, App result, immutable references
and selected policy. The publisher rechecks those bindings and all artifact bytes.

Trust/limits: the pinned App and approved bucket writer are qualification authorities;
self-asserted JSON identities are not authority. This contract rejects substitutions
but cannot prove an authorized producer told the truth about executing tests. It
makes no controller-v2, provider-onboarding, cost, timing or live release claim.
Protected publisher concurrency serializes this workflow, not external registry
administrators. Before first publish, the release owner must review GitHub user/org
package-creation policy and authorize the writer for this namespace. The authenticated
manifest probe allows creation only for an explicit registry `NAME_UNKNOWN` or
`MANIFEST_UNKNOWN` 404; denied, network and malformed responses fail closed, and
existing version tags must match the qualified digest. First creation can default to
**private**: this workflow never widens visibility. An authorized owner must configure
public visibility under their policy, then retry the same immutable candidate if the
anonymous gate failed. Public anonymous image blob pull must succeed before GitHub/npm publication. Packing is preflighted
before image writes, both package locks are stamped, and only the exact validated
SDK/CLI tarballs are published. Registry/publication failures can leave an image
without a GitHub/npm release; they never manufacture a successful release.

### Deliverable publication outcomes

X publication requires explicit `env:TWITTER` credentials and matching
`TWITTER_ACCOUNT_SCOPE_ID`; missing configuration is failure, never a synthetic
receipt. Approval durably claims the record before external I/O and persists each
X receipt before the next post. A crash, ambiguous response or partial publication
requires operator reconciliation; do not recreate the deliverable to retry blindly.
Public responses omit the internal claim while retaining receipts and outcome.
Webhook approval remains explicit-user/account authorized, HTTPS-only and
redirect-disabled. It is not a network-isolation boundary: approved URLs can reach
private addresses through the daemon's network. Review targets and enforce operator
egress policy before approving untrusted webhook content. No live provider or
webhook delivery is asserted by deterministic tests.

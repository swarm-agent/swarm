# Initial Launch Cleanup

This is the focused backlog to finish before opening promotion PRs, publishing a downloadable Swarm archive, and testing the first public install. It is a cleanup gate, not a feature roadmap.

## Scope lock

- **In scope:** preserving normal Git identity behavior, harmful defaults and side effects, dead-code triage, public-repo hygiene, release packaging, install/update/uninstall behavior, and one clean-machine smoke path.
- **Out of scope:** V3 sessions/sync/realtime implementation, feature polish, architecture rewrites, new runners, and speculative refactors.
- A V3 issue may enter this list only if a clean-machine smoke test demonstrates a launch-blocking safety or usability failure. Record the reproduction; do not broaden this cleanup into V3 work.
- Work tasks in order. Do not start P2 cleanup until the P0/P1 gates are closed or explicitly deferred with evidence.

## Current stop-ship facts

1. **P0.1 complete: Swarm-controlled commit and integration operations preserve the target repository's normal Git identity.** The workspace API, generic `git_commit` tool, managed-session commit path, and worktree cherry-pick integration all use ordinary Git while filtering daemon-level `GIT_AUTHOR_*` and `GIT_COMMITTER_*` overrides. Focused worktree coverage proves cherry-pick preserves the source author and uses the target repository's configured committer (`swarmd/internal/worktree/service.go`, `swarmd/internal/worktree/service_test.go`). Generic Bash remains a separately permissioned arbitrary-command surface.
2. **P0.2 implementation complete; repository configuration remains an operator gate.** Pull requests, pushes to `main`, and non-publishing dispatches only build verified candidates with read-only contents permission. Stable publication is a separate `workflow_dispatch` path restricted to `main`, scoped to the `stable-release` GitHub Environment, and receives `contents: write` only after the candidate build and verification job succeeds. The release archive, checksum, and metadata are transferred between jobs as immutable workflow artifacts; third-party actions are pinned to commit SHAs. Before first publication, configure required reviewers on the `stable-release` Environment and verify branch protection still enforces `dev -> main`.
3. **P1.1 hermetic archive/install smoke implementation complete; clean-machine systemd evidence remains an operator gate.** `scripts/smoke-release-archive.sh` verifies the checksum and required archive inputs, extracts the candidate, and runs the embedded `swarmsetup --artifact-root ... --no-service` against disposable system/storage roots under `TMPDIR`. `.github/workflows/build-main.yml` runs it before candidate upload or stable publication and stores `smoke-evidence.txt` beside the candidate artifacts. A fresh supported-Linux VM must still prove the real systemd install/start/health/restart/update-failure/uninstall lifecycle and config/data metadata preservation before first publication.
4. **P1.2 harmful-default implementation complete; privileged lifecycle evidence remains an operator gate.** Launch readiness asserts loopback/default-off/private defaults, blank installer choice and direct `swarmsetup` are files-only, and unsupported non-loopback API/desktop binding fails closed. Reinstall preserves existing canonical directory metadata, uninstall preserves config/data unless purge is explicit, failed release apply restarts the last working runtime, and default permission persistence redacts credentials and omits full tool output. A clean supported-Linux VM must still prove service-account config owner/group/mode and the real systemd failure lifecycle before publication.
5. **Dead code is substantial, but the obvious command needs careful scoping.** The ignored local `.bin/deadcode` is a Go RTA analyzer. Executable-only analysis completed successfully and reported 231 lines in the root module and 362 in `swarmd`; examples include `cmd/swarm/main.go:425`, legacy-looking TUI paths under `internal/app/app.go`, `swarmd/internal/agent/service.go:678`, and old API paths. A naive `-test ./...` run currently fails while loading relocated/stale tests, so its output is not a deletion list. V3 findings are excluded by this document's scope lock.

## P0 — stop before any new PR or release decision

### P0.1 Preserve the user's normal Git identity and signing behavior — complete

**Why:** Swarm must never choose or write a contributor identity for a user's repository. The audited dedicated commit entry points do not hardcode one and already remove inherited author/committer overrides. Worktree integration also creates commits through `git cherry-pick`, so its committer environment needs the same narrow review. Arbitrary commands run through Bash are a separate shell capability, not evidence that Swarm selected an identity.

**Audit evidence**

- `runWorkspaceGitCommit` is the direct workspace commit API. It runs `git add --all` when requested and `git commit -m`, with `filteredWorkspaceGitCommitEnv` removing inherited author/committer name, email, and date variables (`swarmd/internal/api/git_commit.go:112-181`, `swarmd/internal/api/git_commit.go:195-218`).
- `executeGitCommit` is the generic `git_commit` tool. It runs `git commit -m [--all]`; `executeGitCommandWithTimeout` uses `filteredGitEnv`, which removes the same variables (`swarmd/internal/tool/runtime.go:1882-1907`, `swarmd/internal/tool/runtime.go:1957-1980`).
- `manageSessionsCommit` creates approved session-attributed commits with `git add -- <paths>` and `git commit -m`; `runManageSessionsGit` also uses `filteredGitEnv` (`swarmd/internal/tool/runtime_manage_sessions_commit.go:78-128`, `swarmd/internal/tool/runtime_manage_sessions_commit.go:520-529`).
- V3 review-commit jobs grant and prompt the generic `git_commit` tool rather than implementing another Git command path (`swarmd/internal/api/sessions_v3_review_commits.go:136-187`).
- Worktree integration invokes `git cherry-pick` through `runGit`. Cherry-pick preserves the selected commit's author but creates a new committer record; because `runGit` currently inherits the daemon environment, this is the one identified Swarm-controlled integration gap (`swarmd/internal/worktree/service.go:448-475`, `swarmd/internal/worktree/service.go:1082-1095`).
- Generic Bash invokes `bash -lc` with its normal inherited environment and can run Git when separately permitted (`swarmd/internal/tool/runtime.go:1760-1794`). It is an arbitrary-command surface, not a hidden identity configuration path.
- The only checked-in production `git config user.name`/`user.email` commands are in `.github/workflows/build-main.yml:81-83`, where CI configures `github-actions[bot]` to create this repository's annotated release tag. Test helpers configure identities only inside temporary repositories.
- Focused tests already prove the three dedicated commit paths prefer repository-configured identities over conflicting daemon variables (`swarmd/internal/api/git_commit_test.go:38-55`, `swarmd/internal/tool/runtime_git_test.go:13-35`, `swarmd/internal/tool/runtime_manage_sessions_commit_test.go:48-75`).

**Tasks**

- Keep dedicated commit execution conventional: run ordinary `git commit` in the target repository without `git config user.name`, `git config user.email`, `--author`, `-c user.name`, `-c user.email`, or a Swarm-specific identity.
- Preserve the existing environment filtering in the workspace API, generic `git_commit` tool, and managed-session commit path. It removes inherited `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_AUTHOR_DATE`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, and `GIT_COMMITTER_DATE` without changing repository/global Git config or signing settings.
- Review the worktree `git cherry-pick` integration helper separately. If daemon-level identity variables can reach it, remove those variables for that Swarm-controlled integration operation and add focused coverage; do not alter the original commit's author or configure an identity.
- Let Git fail normally with its standard identity guidance when the user has not configured an identity; do not synthesize a fallback identity or write configuration on the user's behalf.
- Keep generic Bash behavior explicit: it runs user/agent-approved shell commands with the process environment and is not covered by claims about dedicated commit helpers. Do not special-case contributor addresses or silently mutate Git configuration there.
- Keep identities used by this repository's own contributors, bots, release-tag automation, and public security contact separate from end-user product behavior. Do not add an allowlist/denylist for contributor emails, add identity-history checks to precommit/pre-push/CI, or rewrite existing commit history as part of this cleanup.

**Acceptance**

- The workspace API, generic `git_commit`, managed-session commits, and Swarm-controlled worktree integration use conventional Git behavior and do not write or substitute a user identity.
- Conflicting daemon `GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables cannot replace the target repository's configured identity in those dedicated commit/integration operations.
- Missing identity fails through ordinary Git behavior; Swarm does not write or synthesize identity configuration.
- Generic Bash remains accurately described as an approved arbitrary-command surface rather than being represented as an identity-managed commit API.
- No production helper supplies `swarm@swarmagent.dev`, a personal address, `--author`, or `user.name`/`user.email` overrides for end-user commits.
- This repository's own identity/history and CI-only tag identity are not treated as end-user identity violations.

**Likely attack points**

- `swarmd/internal/api/git_commit.go`
- `swarmd/internal/tool/runtime.go`
- `swarmd/internal/tool/runtime_manage_sessions_commit.go`
- `swarmd/internal/api/git_commit_test.go`
- `swarmd/internal/tool/runtime_git_test.go`
- `swarmd/internal/tool/runtime_manage_sessions_commit_test.go`
- `swarmd/internal/api/sessions_v3_review_commits.go`
- `swarmd/internal/worktree/service.go`
- `swarmd/internal/worktree/service_test.go`

### P0.2 Make release publication an explicit, late action — implementation complete

**Why:** Stable publication must consume a verified candidate and require an explicit operator action after promotion; ordinary pushes must never publish.

**Implemented**

- Candidate builds run with read-only repository permission and cannot tag or publish.
- Stable publication is a separate manual dispatch on `main`, protected by the `stable-release` GitHub Environment and job-scoped `contents: write` permission.
- Build and candidate verification complete before release creation. The release action creates the tag only at publication time, so build or verification failure leaves no stable tag.
- The build emits `swarm-<version>-linux-amd64.tar.gz.sha256`; candidate verification checks the digest, archive shape, and installer artifact validation path before publication.
- The network installer downloads the matching checksum asset and fails before extraction on a missing, malformed, or mismatched digest. The updater already requires and verifies SHA-256.
- Every third-party action in the release workflow is pinned to an immutable commit SHA with the reviewed major version noted in a comment.

**Operator prerequisite:** Configure required reviewers and deployment-branch restrictions for the `stable-release` GitHub Environment before the first stable publication.

**Tasks**

- Separate PR candidate builds from stable publication. A candidate build must never create/push a stable tag or GitHub release.
- Move tag creation/publication after all build, archive-structure, checksum, and smoke gates.
- Replace or explicitly document the literal `github.actor == 'swarm-agent'` release authority; enforce the intended protected-environment/manual-approval model.
- Generate and publish SHA-256 checksums, then make installer/update verification fail closed on a missing or mismatched checksum.
- Pin third-party actions to reviewed immutable commit SHAs before public release.

**Acceptance**

- PRs from `dev` build an installable candidate artifact but cannot tag or publish.
- A failed build/smoke job leaves no new stable tag or release.
- Stable publication requires the reviewed `dev -> main` path plus the configured approval boundary.
- The downloaded archive is verified before extraction.

**Likely attack points**

- `.github/workflows/build-main.yml`
- `.github/workflows/guard-main-pr-source.yml`
- `scripts/build-main-dist.sh`
- `scripts/resolve-release-version.sh`
- `install.sh`
- `internal/launcher/update.go`

## P1 — finish before the first downloadable test

### P1.1 Add a hermetic release archive and install smoke path — hermetic gate complete

**Why:** The build script assembles the expected archive (`scripts/build-main-dist.sh:123-174`), but launch readiness does not exercise it and the deploy checklist waits until after publication.

**Implemented**

- `scripts/build-main-dist.sh` builds and checksums the candidate archive without publishing it.
- `scripts/smoke-release-archive.sh` consumes that archive, verifies its checksum and every launcher/daemon/library/web/build-info/service-install input, extracts it under `TMPDIR`, and runs the embedded `swarmsetup --artifact-root ... --no-service` with every system and storage root redirected below the disposable smoke root.
- The smoke asserts the installed versioned runtime, web assets, `libfff_c.so`, launcher and daemon binaries, and launcher symlinks. It emits `smoke-evidence.txt` with the archive name, digest, version, hermetic install result, and explicit external-systemd status.
- `.github/workflows/build-main.yml` runs this gate in `build-candidate` before artifact upload. `publish-stable` depends on that successful job, so a smoke failure cannot create a stable tag or release.

**Remaining external release prerequisite**

- In a fresh supported Linux VM, test the real systemd path: install, start, `swarm status`, `swarm open`/health reachability, stop, restart, update failure behavior, and uninstall.
- Prove reinstall preserves canonical config/data and preserves config owner/group/mode. Keep that clean-machine evidence with the candidate workflow run; the hermetic no-service gate does not claim systemd coverage.

**Acceptance**

- One checked-in command produces a candidate archive and one checked-in smoke harness tests it without touching host system paths. **Complete.**
- Missing web assets, `libfff_c.so`, launcher/daemon binaries, build metadata, or service/install inputs fail the gate. **Complete.**
- Clean-machine install and uninstall leave only explicitly documented retained data. **External VM evidence required before publication.**
- No stable tag is created until the hermetic gate passes. **Complete in workflow; Environment approval remains required.**

**Likely attack points**

- `scripts/build-main-dist.sh`
- `scripts/check-launch-readiness.sh`
- `scripts/smoke-release-archive.sh`
- `scripts/verify-release-candidate.sh`
- `install.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/launcher.go`
- `internal/launcher/service_lifecycle.go`
- `internal/launcher/system_paths.go`
- `docs/main-deploy-checklist.md`

### P1.2 Review harmful defaults and side effects as invariants — implementation complete

**Implemented**

- `scripts/check-launch-defaults.sh`, called by launch readiness, asserts loopback API binding; permission bypass, diagnostics, and tool retention off; startup config `0600`; explicit service selection; files-only blank input; non-loopback fail-closed guards; default permission output omission; preservation-oriented uninstall; and update rollback coverage.
- Direct `swarmsetup` now defaults to files-only, while `install.sh` maps blank service choice to files-only. Installing/enabling systemd requires `--service` or an explicit `1` choice.
- Startup config validation and daemon `--listen` parsing reject non-loopback API/desktop exposure with SSH tunnel/Tailscale-forwarding remediation. No fallback authenticated mode was invented.
- Existing canonical install directories retain their metadata rather than being chmod/chowned on reinstall. New startup config writes enforce `0600`; default uninstall retains config/data/cache/logs unless purge is explicit.
- A release apply failure now restarts the last working direct/systemd runtime. Pending-boot rollback remains the guard for a newly applied runtime that fails startup.
- Permission arguments, decisions, and errors are sanitized before persistence; with tool-output retention disabled, full output is replaced by a privacy placeholder. Provider API and V3 diagnostics remain opt-in and default off.

**Remaining external operator evidence**

- On a fresh supported-Linux VM, record config file owner/group/mode across install and reinstall, force both apply-time and boot-time failures under the real systemd service, and verify default uninstall retains config/data. This cannot be claimed from an unprivileged disposable harness.
- Runtime logs outside provider diagnostics are broad application logs; the scoped gate proves diagnostics default off and credential redaction paths, not that arbitrary model-generated text can never appear in every future log call.

**Confirmed safe defaults to preserve**

- API/desktop networking defaults to loopback (`pkg/startupconfig/config.go:21-24`, `pkg/startupconfig/config.go:133-152`).
- Permission bypass, retained tool output, and provider diagnostics default off (`pkg/startupconfig/config.go:143-146`).
- Startup config mode is `0600` (`pkg/startupconfig/config.go:26-27`).
- Interactive install confirmation defaults to no (`install.sh:271-289`).

**Tasks**

- Convert those defaults into launch-gate assertions so they cannot regress silently. **Complete.**
- Decide whether pressing Enter at the install-type prompt should select and start an always-on systemd service (`install.sh:244-268`). Blank input now selects files-only. **Complete.**
- Align `swarmsetup` with the wrapper's explicit service choice. Direct invocation defaults to files-only and requires `--service` for systemd. **Complete.**
- Forbid non-loopback API/desktop binding unless the supported authenticated transport is explicitly configured. No approved direct exposure mode exists, so config and CLI input fail closed. **Complete.**
- Verify permission bypass cannot be enabled accidentally by installer flags, inherited environment, or onboarding defaults, and that its enabled state remains conspicuous in TUI/Desktop.
- Verify install/update/uninstall do not overwrite or change ownership/mode of existing `/etc/swarmd/swarm.conf`, do not delete `/var/lib/swarmd` without explicit consent, and roll back a failed runtime switch.
- Verify logs/diagnostics never persist provider payloads, tokens, cookies, prompts, or full tool output under default settings.

**Acceptance**

- Automated assertions cover loopback binding, permission prompts on, sensitive diagnostics off, tool-output retention off, config `0600`, and least-mutating install choices.
- Non-loopback desktop exposure fails with a clear remediation unless an approved secure mode is configured.
- A failed install/update preserves the last working runtime and canonical config metadata.
- Default logs and stored permission records contain summaries/redactions rather than sensitive payloads.

**Likely attack points**

- `pkg/startupconfig/config.go`
- `swarmd/internal/config/config.go`
- `swarmd/internal/runtime/daemon.go`
- `swarmd/internal/permission/service.go`
- `internal/app/startup_network_warning_modal.go`
- `install.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/update.go`

### P1.3 Run public-repository hygiene against the exact candidate

**Tasks**

- Freeze one `dev` SHA, require a clean worktree, and run the checked-in precommit and launch-readiness gates against that SHA.
- Keep identity verification in focused product tests for the commit/integration helpers. Do not add contributor-email or commit-history policing to launch readiness, precommit, pre-push, or CI, and do not reinterpret this repository's own history as end-user runtime behavior.
- Review every tracked non-text binary rather than globally allowing binaries. Launcher wrappers under `bin/` are shell scripts; generated archives/build outputs must remain untracked.
- Replace stale baseline SHAs/counts in `docs/main-deploy-checklist.md:10-14` with a reproducible command/evidence record or remove the snapshot so it cannot mislead operators.
- Confirm public docs name only supported platforms and flows; initial artifact support is Linux x86-64 (`install.sh:598-602`).

**Acceptance**

- Candidate SHA, gate output, archive checksum, and clean-machine smoke evidence are recorded together.
- Tracked secrets, private identifiers, generated artifacts, and unexpected binaries all fail before PR/publication; focused product tests prove commit-helper behavior without policing legitimate contributor metadata.
- Release docs contain no stale fixed branch snapshot presented as current truth.

## P2 — bounded cleanup before promotion; do not turn it into a refactor

### P2.1 Triage Go dead code by executable, configuration, and ownership

**Reproducible baseline**

```bash
GOCACHE="$PWD/.cache/go-build" GOMODCACHE="$PWD/.cache/gomod" GOPATH="$PWD/.cache/gopath" \
  .bin/deadcode ./cmd/...

(
  cd swarmd
  GOCACHE="$PWD/../.cache/go-build" GOMODCACHE="$PWD/../.cache/gomod" GOPATH="$PWD/../.cache/gopath" \
    ../.bin/deadcode ./cmd/...
)
```

The local binary is ignored and architecture-specific. For CI/repeatability, install a pinned `golang.org/x/tools/cmd/deadcode` version into an ignored tool cache; do not commit `.bin/deadcode`.

**Triage rules**

- Analyze each supported executable and supported `GOOS/GOARCH/tags` configuration. Results are valid only for the analyzed configuration.
- First classify findings as: safely removable implementation, public API kept intentionally, platform/build-tag path, provider/plugin/reflection path, generated code, test-only helper, retired product path, or V3 excluded.
- Use `-whylive` for suspicious neighbors and inspect callers/interfaces before deletion.
- Do not delete code merely because RTA reports it. Do not touch V3 files in this cleanup.
- Run `-test` only after the relevant package/test-loading failures are repaired or narrowed. The repository-wide `-test ./...` command currently cannot load all relocated tests and is not a valid gate.
- Remove dead code in small ownership-based PRs with focused validation; do not mix identity, installer, and dead-code changes.

**Acceptance**

- A pinned, documented analyzer command is reproducible for both modules.
- Every removed symbol has a recorded classification and no supported config that reaches it.
- V3 reports are excluded, not “cleaned up.”
- The final report distinguishes remaining intentional findings from removed code and identifies test-loading gaps separately.

**Initial non-V3 sampling for triage**

- `cmd/swarm/main.go:425` — `parseYesOnly`
- `internal/app/app.go:850-893` — old session-event stream path
- `internal/app/chat_backend_adapter.go` — large unreachable adapter surface
- `swarmd/internal/agent/service.go:678` — `oldDefaultClonePrompt`
- `swarmd/internal/api/run_stream_ws.go` — old run-stream manager methods
- `swarmd/internal/appstorage/storage.go` — unused storage accessors

### P2.2 Close only launch-relevant forgotten ends

**Tasks**

- Search user-facing code/docs for TODO/FIXME/stub/legacy surfaces after P0/P1 changes, but require a demonstrated first-install or first-use impact before adding work.
- Remove retired public commands/routes only when they are outside V3 and their replacement is confirmed; otherwise record them as post-launch debt.
- Ensure unsupported/deferred features are hidden or clearly fail as unavailable rather than appearing to succeed.
- Keep generated diagnostics, local profiles, screenshots, dumps, caches, and scratch files out of the candidate commit.

**Acceptance**

- Every item fixed has a clean-machine reproduction or concrete safety impact.
- Cosmetic polish, speculative architecture work, and V3 internals remain deferred.
- The final candidate diff contains no unrelated refactor.

## Final go/no-go sequence

1. Freeze the candidate `dev` SHA and stop unrelated changes.
2. P0 Git identity preservation is verified for dedicated commit helpers and worktree integration; generic Bash remains a separately permissioned shell surface. No contributor-identity substitution, history checker, or history rewrite is required by this cleanup.
3. Candidate build cannot publish; stable publication remains gated.
4. P1 harmful-default invariants and repository gates pass.
5. Candidate archive is built, checksummed, extracted, installed, started, updated/failure-tested, and uninstalled on a clean supported Linux machine.
6. P2 dead-code/forgotten-end work is either merged in bounded PRs or explicitly deferred as non-blocking.
7. Open the single reviewed `dev -> main` promotion PR.
8. Run one final end-to-end launch review against the exact candidate. This final pass must include the checked-in vulnerability scans and may use agents to independently inspect release, installer, dependency, and public-repository evidence; the parent must synthesize and verify every finding.
9. Publish only after the exact PR head's build, checksum, smoke, vulnerability, and review evidence is approved through the `stable-release` Environment.

## False positives and non-blockers

- `swarm@swarmagent.dev` is this project's own identity and public security contact. Its use in this repository's commits or `CONTRIBUTING.md:11` is not evidence that Swarm injects it into end-user repositories and is not, by itself, a cleanup target.
- The `github-actions[bot]` identity in `.github/workflows/build-main.yml:81-83` is configured in the CI checkout for annotated release tags, not ordinary end-user source commits; retain only if the reviewed release policy allows it.
- `.bin/deadcode` and `.tmp/prelaunch-deadcode-*.txt` are ignored local investigation artifacts and must not be committed.
- Deadcode reports under V3 are intentionally not tasks here.
- A TODO/FIXME marker alone is not a launch blocker.

## Relevant filepaths

- `.github/workflows/build-main.yml`
- `.github/workflows/guard-main-pr-source.yml`
- `CONTRIBUTING.md`
- `scripts/check-precommit.sh`
- `scripts/check-prepush.sh`
- `scripts/check-launch-readiness.sh`
- `scripts/build-main-dist.sh`
- `scripts/resolve-release-version.sh`
- `install.sh`
- `cmd/swarmsetup/main.go`
- `internal/launcher/launcher.go`
- `internal/launcher/service_lifecycle.go`
- `internal/launcher/system_paths.go`
- `internal/launcher/update.go`
- `pkg/startupconfig/config.go`
- `swarmd/internal/api/git_commit.go`
- `swarmd/internal/tool/runtime.go`
- `swarmd/internal/tool/runtime_manage_sessions_commit.go`
- `swarmd/internal/api/git_commit_test.go`
- `swarmd/internal/tool/runtime_git_test.go`
- `swarmd/internal/tool/runtime_manage_sessions_commit_test.go`
- `swarmd/internal/api/sessions_v3_review_commits.go`
- `swarmd/internal/worktree/service.go`
- `swarmd/internal/worktree/service_test.go`
- `swarmd/internal/config/config.go`
- `swarmd/internal/runtime/daemon.go`
- `swarmd/internal/permission/service.go`
- `docs/main-deploy-checklist.md`

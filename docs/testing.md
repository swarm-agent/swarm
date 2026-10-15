# Test retention audit and local vital runner (in progress)

This document records a partial reduction, not completed acceptance. The local
vital runner passes, but Go pruning and the full inventory remain incomplete.
Baseline: commit
`c7bbc6c48f6c94c0638b8dc29512c62625884532`.

## Counts and timings

| Measure | Before | After |
| --- | ---: | ---: |
| Tracked `_test.go` files | 1,513 | 1,513 (unchanged) |
| Top-level `func Test…(` declarations | 6,352 | 6,352 (unchanged) |
| Whole-suite elapsed time | Not measured | Not measured |
| Extra Go tests selected | 0 | 0 |

Declaration counts exclude benchmarks, examples, fuzz functions and subtests.
No speedup or whole-suite pass is claimed.

## KEEP authorities

The final inventory must expand these commands into package-qualified test names,
not merely retain whole files that contain one selected test. Preserve helper
closure across files and build constraints before deleting declarations.

### GitHub workflows

All six workflows were inspected:

- `critical-tests.yml`, `dependency-vulnerability-scan.yml`,
  `guard-main-pr-source.yml`, `install-distro-smoke.yml`, and
  `require-changelog.yml` run `python3 -I scripts/gcp-result-relay.py` against
  protected external qualification results. They do not enumerate or execute
  local Go/web tests. The distro matrix preserves Ubuntu and Arch check identities.
- `build-main.yml` consumes exact GCP artifacts via `gcp-release-input.py`,
  verifies release evidence, signs/attests, and publishes artifacts. Its commands
  are release operations, not a local test-suite manifest. Never run publishing
  steps as a substitute for tests.

### Go critical suites

Retain the exact package/`-run` selections in `scripts/run-critical-tests.sh`:

| Group | Module | Packages | Selection authority |
| --- | --- | --- | --- |
| Root safety | root | `pkg/startupconfig`, `pkg/storagecontract` | `run_fast` → `run_root_tests` |
| TUI routing | root | `internal/app`, `internal/client` | `run_tui_task_routing_tests` |
| Listener | swarmd | `internal/config` | `run_fast` |
| Agent authority | swarmd | `internal/agent` | `run_fast` |
| Containment | swarmd | `internal/workspace`, `internal/discovery`, `internal/artifact`, `internal/action`, `internal/videoproject` | `run_fast` |
| Media boundaries | swarmd | `internal/mediastaging`, `internal/imagegen`, `internal/videosource`, `internal/videorender` | `run_fast` |
| Auth and cursors | swarmd | `internal/api` | `run_fast` |
| Delegation and scheduling | swarmd | `internal/run` | `run_agents` |
| Capacity | swarmd | `internal/permission` | `run_agents` |
| Durable children | swarmd | `internal/store/pebble` | `run_agents` |
| Atomic V3 mutation | swarmd | `internal/store/pebble` | `run_deep` |
| Executor idempotency | swarmd | `internal/api` | `run_deep` |
| Delegated Git | swarmd | `internal/worktree` | `run_deep` |
| Artifact Git | swarmd | `internal/artifactgit` | `run_deep` |

Deep selections remain retention candidates because the requested qualification
fallback explicitly includes every pattern in this script, not just fast/agents.
The script currently forces `-count=1`; the requested vital runner must instead
use caching, `GOMAXPROCS=4`, and `-p 4`.

### Qualification tests

Repository searches for `qualification` found result consumers, receipt schemas,
release documentation, and test commentary, but no external 144-test manifest.
Do not claim to have reconstructed or executed that external gate.

Use the requested fallback name fragments in the relevant packages:
`DesktopBoundary`, `DesktopSession`, `ProtectedCreateAPIs`, `V3SyncCursor`,
`V3Realtime`, `VaultGate`, `BackgroundRouterSessionStart`, `SessionV3Executor`,
`ResolveActionEntrypoint`, `RunnerSnapshots`, `SystemAgentRegistry`,
`CompiledRouter`, `PanicRecovery`, `ExtractAttachToken`,
`XSwarmTokenPrincipal`, `AssembleActionArguments`.

### Web

Retain the explicit files selected by `web/package.json` scripts `test:critical`
and `test:agents`, plus the transitive selections of `test:orchestrator` and
`test:orchestrator:selection`. `test:orchestrator` delegates to
`web/scripts/run-orchestrator-tests.mjs`; its inventory still needs expansion.
No tracked package.json currently contains a `release:verify` script.
Locked web dependencies were installed during follow-up; results appear below.

### Installer and end-to-end tests

Retain the requested `scripts/test-*.sh` and installer/e2e tests under `tests/`.
Do not blindly execute them on the development host:

- `scripts/test-sandbox.sh`: **needs Docker**; selects
  `TestSandbox(Positive|Negative)Suite` in `swarmd/internal/sandbox` with
  `SWARM_SANDBOX_E2E=1`.
- `scripts/test-install-omarchy-vm.sh`: needs a disposable, provisioned Omarchy
  SSH guest, candidate archive and checksum. It removes Git inside that guest
  and installs the candidate using sudo. Docker alone does not satisfy this gate.
- Remaining installer/e2e prerequisites and command arguments need inspection
  before a safe unified runner can execute them.

## Validation actually executed

Both commands used the existing cache and returned exit code zero:

```sh
GOMAXPROCS=4 go test -p 4 -timeout=30s ./pkg/startupconfig ./pkg/storagecontract -run 'Test(ResolveRoots|ResolveRoot|ValidateRoot|Overrides|JoinRejects|WritePreservesIdentity)'
(cd swarmd && GOMAXPROCS=4 go test -p 4 -timeout=30s ./internal/config -run '^TestParseRejectsNonLoopbackListen$')
```

Reported package test times were 0.019s, 0.009s, and 0.018s respectively. These
are not full command wall times or whole-suite before/after measurements.

## Outstanding work

Expand the exact KEEP inventory, select and justify any extras (maximum 150),
finish removing unselected tests and unused test-only dependencies, and compile
and validate the retained packages. The vital runner and precommit integration
are now implemented but do not yet cover the complete requested inventory. Run build,
vet and TypeScript gates and record actual full runner timings. A missing live
installer environment must be reported as unavailable, not relabeled Docker or
counted as a passing test.

## Follow-up implementation status

The operator supplied the exact external qualification names. The current runner
uses the checked-in critical selections (including deep) as its release-gate
selection; it is not yet an independent exact-name 144-test manifest. Root
`internal/app` and `internal/client` are root-module packages despite the supplied
list's general `internal` mapping.

374 unselected local web test files have now been removed. The original 492 web
test files are reduced to 118: 19 explicitly selected files in
`scripts/test-vital-web.txt`, plus 99 browser/e2e files retained pending complete
release-only classification. The four runnable web groups contain 115 tests
(74 critical, 19 agents, 5 selection, 17 orchestrator); all passed. Web `tsc -b`
also passed after deletion. Go pruning and SDK pruning are not complete.

`bash scripts/test-vital.sh` runs bounded cached Go critical selections under
`release gate`, the four web suites, and the hermetic installer artifact-integrity
check. Precommit now invokes this runner rather than the opt-in broad Go block.
The first run, before adding installer integrity, passed all five groups in 57
wall-clock seconds. The final runner passed all six groups in 23 seconds with
warm caches. This is not a whole-suite before/after performance comparison.

### runs in the release pipeline only

- `scripts/test-sandbox.sh` and Docker sandbox tests: **needs Docker**, with
  project-container capability and the explicit sandbox E2E opt-in.
- `scripts/test-install-omarchy-vm.sh`: disposable SSH Omarchy VM, sudo, candidate
  release archive and checksum.
- `scripts/test-install-distro.sh`: disposable distribution installation
  environment and release inputs; not the developer host.
- `scripts/test-install-root-scenario.sh`: disposable privileged installation
  environment and release inputs.
- `scripts/test-install-mint-guard.sh`: root inside a disposable systemd guest
  with the installed service; it modifies the service override during setup.
- Retained browser/e2e tests: browser runtime and, depending on the scenario,
  a live authenticated daemon/provider configuration. Their individual external
  prerequisites still need complete classification.

Missing external environments are not local validation failures and are not
reported as passing runs.

### Additional validation and unresolved gates

Focused retained API, worktree, agent, action, workspace, discovery, artifact,
video, media, scheduler, store, artifact-Git and permission tests passed, as did
root app/client routing tests. Existing API `TestLandReview*` and `TestAutoPublish*`
also passed. `TestReviewPipeline*`, `TestPublishProof*`, and
`TestWorkerProofContract*` declarations are absent from this captured revision.
No test added in another checkout is claimed as retained here.

Root `go build -p 4 ./pkg/...` and `go vet -p 4 ./pkg/...` passed. Daemon
`go build -p 4 ./...` passed; daemon `go vet -p 4 ./...` failed with existing
context-cancel leaks in provider/google and run tests, a zero-value append in
sandbox/cache_docker_test.go, unreachable code in run/service.go and
tool/runtime_manage_artifact.go, and self-assignment in store/pebble/auth_store.go.
These findings have not been repaired. Package TypeScript gates remain unrun.

## Current reduction and blocking integration failures

The previous progress sections are historical. The exact supplied release list is
now `scripts/test-vital-release.txt`, checked for missing names before execution;
critical selections run separately. `scripts/test-vital-extras.txt` enumerates
58 extras (including CPU/cache startup configuration, real CPU-slot processes,
worktree removal/recovery, review pipeline, capacity admission, and persistent
publish queues). Docker extras `TestProjectCacheDocker` and
`TestCPUDockerGitUnderLoad` are release-pipeline-only (**needs Docker**).

Go inventory is now 230 Test declarations in 197 test/helper files, versus the
original 6,352 declarations in 1,513 files. The rebase brought additional tests,
so these numbers are not a same-revision subtraction. Removed 40 unselected
SDK/CLI/fleet test files; SDK TypeScript passes after locked dependency install.
Product code was not changed. No tracked testdata paths were found.

Both root and daemon `go build -p 4 ./...` and
`go test -p 4 -run '^$' ./...` passed after pruning. An initial daemon compile
was OOM-killed; serial compilation with GOMEMLIMIT=1800MiB passed, and subsequent
cached four-way compile passed. The runner passed release gate, critical Go,
core-path extras, four web groups and installer integrity in 129 seconds before
adding the retained tests-tree groups. This is not a before/after speedup claim.

Explicitly running retained `tests/...` exposed failures, now included as
failing groups in the vital runner rather than hidden:

- root client plan submit expects execution_granularity=checkpointed but receives
  no field; resolving the intended submit default needs contract review.
- root retired-hosting source assertion rejects .dockerignore and references a
  deleted test file.
- daemon workspace bootstrap fixtures lack initial Git commits.
- daemon background metadata test targets unavailable agent `memory`.
- daemon vault fixtures omit account scope.

These assertions have not been loosened or skipped. The runnable KEEP set is
therefore **not green**. Test helper retention is conservative; further cleanup
and per-extra justification review remain pending. Exact extra names are in the
committed manifest, not a claim that all requested acceptance criteria are done.

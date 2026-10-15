# Test retention audit (in progress)

This document records the initial audit, not a completed reduction or a passing
vital gate. No tests have been deleted yet. Baseline: commit
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
The isolated worktree has no `web/node_modules`; web tests have not run.

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
remove unselected tests and unused test-only dependencies, compile and validate
the retained packages, install/validate web dependencies, implement
`scripts/test-vital.sh`, and replace the opt-in precommit test block. Run build,
vet and TypeScript gates and record actual full runner timings. A missing live
installer environment must be reported as unavailable, not relabeled Docker or
counted as a passing test.

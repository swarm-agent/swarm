# Go deadcode safety ledger

Status: **triage complete; root leaf cleanup R1/R2 integrated; conservative TUI audit complete with no additional removals**
Baseline commit: `301925b8f188c3e2be56342d65854e37201c49de` (`dev`)  
Reports: `.tmp/prelaunch-deadcode-root.txt` (**232 findings**) and `.tmp/prelaunch-deadcode-swarmd.txt` (**364 findings**)  
Total accounted for below: **596 findings**

This is a safety ledger, not a deletion list. Each report line has one disposition below, either individually or in a contiguous group whose symbols share the same evidence and disposition. “Candidate” means a future isolated Coder may verify and remove it; it is not approval to delete it. Anything without positive non-RTA evidence is retained.

## Analyzer and configuration baseline

### Exact analyzer

- Command: `golang.org/x/tools/cmd/deadcode`
- Module/version: `golang.org/x/tools v0.45.0`
- Module checksum recorded by `go version -m`: `h1:18qN3FAooORvApf5XjCXgsuayZOEtXf6JK18I3+ONa8=`
- Analyzer binary build toolchain/configuration: Go `1.26.3`, `linux/amd64`, `CGO_ENABLED=1`, `GOAMD64=v1`
- Current command toolchain/configuration: Go `1.25.12`, `linux/amd64`, empty `GOFLAGS`
- Reproducible ignored-cache install form: `GOBIN="$PWD/.bin" go install golang.org/x/tools/cmd/deadcode@v0.45.0`
- `.bin/deadcode`, `.cache/`, and `.tmp/prelaunch-deadcode-*.txt` are ignored local investigation artifacts and must not be committed.

### Exact scans

```bash
GOMAXPROCS=4 \
GOCACHE="$PWD/.cache/go-build" \
GOMODCACHE="$PWD/.cache/gomod" \
GOPATH="$PWD/.cache/gopath" \
  .bin/deadcode ./cmd/...

(
  cd swarmd
  GOMAXPROCS=4 \
  GOCACHE="$PWD/../.cache/go-build" \
  GOMODCACHE="$PWD/../.cache/gomod" \
  GOPATH="$PWD/../.cache/gopath" \
    ../.bin/deadcode ./cmd/...
)
```

Both scans were rerun at the baseline commit. Their outputs byte-match the two `.tmp` snapshots: root 232/232 lines; swarmd 364/364 lines. The older counts in `initial-launch-cleanup.md` (231 and 362) are superseded by these current snapshots.

### Module and executable coverage

| Module | Pattern | Main packages used as RTA roots |
|---|---|---|
| `swarm-refactor/swarmtui` | `./cmd/...` | `cmd/fffprobe`, `cmd/gofmtfiles`, `cmd/rebuild`, `cmd/swarm`, `cmd/swarmsetup`, `cmd/swarmtui` |
| `swarm/packages/swarmd` | `./cmd/...` | `cmd/fffprobe`, `cmd/pebble-inspect`, `cmd/swarm-fff-search`, `cmd/swarmctl`, `cmd/swarmd`, `cmd/swarmdbpeek` |

Coverage is intentionally limited to executable reachability for the current default Linux/amd64 configuration. No `-test`, `-generated`, or `-tags` option was used. Repository-wide `-test` remains an invalid gate while relocated/stale test packages cannot all load. The findings therefore cannot prove unreachability for another GOOS/GOARCH/tag set, external consumers, tests, compatibility data, or dormant registration paths.

Of the production files named by the reports:

- `swarmd/internal/tool/command_cancel_unix.go` has `//go:build !windows`; its two findings are retained as a platform path.
- No reported production file has the canonical `// Code generated ... DO NOT EDIT.` header.
- Deadcode v0.45.0 itself omits canonical generated files and marker-interface methods by default and says it handles dynamic function/interface/reflection edges, but that does not settle external API, disabled registration, test-only, compatibility, or `go:linkname` risk.

### Launch-readiness baseline

`bash scripts/check-launch-readiness.sh` completed with exit status 0: **8 passes, 1 warning, 0 failures**. The warning is expected because no owner/device-specific `--forbid-token` values were supplied. This checkpoint did not use `--require-clean`, because creating this ledger is an intentional tracked workspace change.

## Classification vocabulary

- **removable-candidate** — private code with positive evidence beyond RTA (definition-only, redundant/no-op, no interface/registration/build-tag/generated/test use found); still requires the listed batch gate.
- **retired-path-candidate** — intentionally disabled/legacy product path, but migration or persisted-data evidence is required before removal.
- **public-api-retain** — exported or canonical package contract; no explicit API retirement decision.
- **test-helper-retain** — directly test-used or explicitly test-facing.
- **platform-retain** — build-constrained or platform lifecycle code.
- **dynamic-provider-retain** — dormant provider/registration/interface implementation, not proven retired.
- **generated-retain** — generated source (none in these reports).
- **V3-excluded** — V3 session/sync/realtime/plan implementation; outside cleanup scope regardless of RTA output.
- **uncertain-retain** — evidence is incomplete or the compatibility/security/state risk is greater than the cleanup value.
- **live-neighbor-retain** — repository references or neighboring reachable implementations contradict a simplistic deletion interpretation.

## Root module ledger — 232/232

Line numbers below refer to `.tmp/prelaunch-deadcode-root.txt`.

| Lines | Ownership / symbols | Classification | Evidence and rationale |
|---:|---|---|---|
| 1 | `cmd/swarm`: `parseYesOnly` | removed (R1) | Private parser; repeated full Go search found only its definition. Adjacent active parsers cover current command forms. No build tag, generated marker, interface, registry, reflection, or test use found. Removed in integrated commit `f5e40c1f`. |
| 2–4 | `internal/app`: old session-event stream methods | uncertain-retain | Direct neighboring calls exist and this is a transport migration boundary. Do not remove without proving the registered TUI route cannot enter it. |
| 5 | `internal/app`: `buildPlanExitModalBody` | removed (R2) | Private method; repeated full Go search found only the definition. Current plan-exit modal owns its body. No interface, registry, build constraint, generated marker, or test use found. Removed in integrated commit `f3228cde`. |
| 6 | `internal/app`: `requireTUIV3SessionAPI` | V3-excluded | Called from V3 chat/session hydration boundaries; V3 implementation is excluded. |
| 7 | `internal/app`: legacy chat/worktree opener | uncertain-retain | Legacy/compatibility route with tests; route retirement is not yet positively proven. |
| 8–9 | `internal/app`: session summary/chat opening | live-neighbor-retain | Direct callers and tests exist. |
| 10 | `internal/app`: `chatTitleFromPrompt` | live-neighbor-retain | Called at `app.go:3016`; keep with the legacy session-opening neighbor until that whole path is proven retired. |
| 11–12 | `internal/app`: commit title/lineage metadata methods | removed (R2) | Repeated full Go search confirmed both private helpers were definition-only. The active `/commit` dispatch and instruction flow remain, and live `chatTitleFromPrompt` remains untouched. No interface, reflection, test, registration, tag, or generated use found. Removed in integrated commit `f3228cde`. |
| 13–22 | `internal/app`: target binding, tabs, workset and context helpers | V3-excluded | Transitional V3 workset/routing state. Explicitly excluded even where current executable RTA misses an entry. |
| 23–24 | `internal/app`: Copilot interactive auth | dynamic-provider-retain | Provider is intentionally dormant, not formally retired; auth callbacks can be registration-driven. |
| 25–33 | `internal/app`: context/route/model helpers | uncertain-retain | Workspace routing and model-selection surfaces; no narrow deletion proof and external UI/state risk is high. |
| 34–55 | `internal/app/chat_backend_adapter.go` | V3-excluded | Implements the V3 chat backend contract, including permissions, plans, preferences, usage and stop behavior. |
| 56–57 | `internal/app`: config persistence/settings conversion | test-helper-retain | Tests and configuration boundaries use these helpers. |
| 58–65 | `internal/app`: Git/mode/model/theme commands | uncertain-retain | Command dispatch and UI callback boundaries; direct tests/callers exist in the surrounding feature paths. |
| 66–94 | `internal/app`: TUI realtime/workset/store/controller | V3-excluded | Current V3 hydration, realtime, send, cursor and workset implementation. |
| 95–97 | `internal/app`: update/theme helpers | uncertain-retain | Update and workspace-theme state boundaries with tests/neighbors; no positive deletion proof. |
| 98–99 | `internal/buildinfo`: display accessors | public-api-retain | Exported build/package metadata API. |
| 100–107 | `internal/fff`: exported instance methods | public-api-retain | Intentional FFF/cgo wrapper surface; option-bearing methods and probe/search binaries exercise neighboring implementations. |
| 108–113 | `internal/launcher`: binary/web/desktop/service/update helpers | public-api-retain | Exported launcher/install/update lifecycle API; public and platform risk. |
| 114–116 | `internal/model`: mock home/path helpers | test-helper-retain | Exported fixture and helper surface used by tests. |
| 117–124 | `internal/ui`: chat accessors | test-helper-retain | Exported state/modal accessors used by UI tests and integration seams. |
| 125–141 | `internal/ui`: chat modal, timeline, shell, markdown and mention helpers | live-neighbor-retain | Internal callers/tests exist; rendering callbacks are not safe deletion targets from RTA alone. |
| 142–147 | `internal/ui`: chat page, async stream and permission methods | live-neighbor-retain | Production route/callback and tests use the page contract. |
| 148–150 | `internal/ui`: title and stream-style helpers | uncertain-retain | Neighbor-chain reachability and test configuration require a focused UI migration review. |
| 151–152 | `internal/ui`: paste accessors | test-helper-retain | Explicit state accessors for tests. |
| 153–164 | `internal/ui`: plan editor/exit/update, permission and thinking UI | uncertain-retain | Active plan/permission behavior and tests; no deletion proof. |
| 165–169 | `internal/ui`: footer rendering | live-neighbor-retain | Direct render callers and tests. |
| 170–178 | `internal/ui`: home accessors | test-helper-retain | Exported state accessors used by tests/integration. |
| 179–197 | `internal/ui`: agents modal methods/helpers | live-neighbor-retain | Modal callbacks and tests own the behavior. |
| 198–200 | `internal/ui`: auth and command-palette helpers | uncertain-retain | UI/auth callback surface; no positive retirement evidence. |
| 201–220 | `internal/ui`: home body/topbar/models/onboarding/paste/session/theme/workspace/worktree helpers | live-neighbor-retain | Rendering/modal paths and tests reference neighboring behavior. |
| 221–225 | `internal/ui`: lineage, theme and voice helpers | public-api-retain | Current state contracts and exported methods; no API retirement. |
| 226–230 | `internal/ui/v3chat` | V3-excluded | V3 page, permission, runtime and selector implementation. |
| 231 | `pkg/startupconfig`: `FileConfig.ApplyBootstrap` | public-api-retain | Canonical daemon bootstrap configuration API; used by daemon config loading. |
| 232 | `pkg/storagecontract`: `Roots.Root` | public-api-retain | Canonical storage root contract. |

### Root candidate evidence summary

Lines **1, 5, 11–12** were removed through reviewed isolated Coder batches R1 and R2. Repeated full Go source searches confirmed all four symbols were private and definition-only; they were not in build-constrained or generated files and had no test, interface, registry, or reflection role. Line 10 remains deliberately retained because a direct caller exists, even though that caller chain is itself suspicious.

## Swarmd module ledger — 364/364

Line numbers below refer to `.tmp/prelaunch-deadcode-swarmd.txt`.

| Lines | Ownership / symbols | Classification | Evidence and rationale |
|---:|---|---|---|
| 1–2 | `agent`: tool contract and old clone prompt | test-helper-retain | Agent initialization/tests reference these definitions; prompt history is behavior-sensitive. |
| 3 | `agent`: `Service.resolveProfile` | live-neighbor-retain | Called by primary resolution (`service.go:2162`). |
| 4–5 | `agent`: primary-selection lock helpers | uncertain-retain | Agent registry/state-machine risk; no positive removal proof. |
| 6 | `agent`: plan sidechat system profile | live-neighbor-retain | Registry materialization/reconciliation and tests use it. |
| 7–11 | `api`: provider defaults/auth wrapper methods | public-api-retain | Account-scoped compatibility/security wrappers delegate to active implementations. |
| 12–15 | `api`: onboarding transport helpers | uncertain-retain | Onboarding/transport compatibility path; route/config retirement not proven. |
| 16 | `api`: account session workspace validation | uncertain-retain | Security-critical routed-session boundary. |
| 17–19 | `api`: old run-stream manager helpers | uncertain-retain | Legacy websocket path looks isolated, but route and cancellation lifecycle retirement needs a dedicated audit. |
| 20–21 | `api`: session delete/list scope helpers | test-helper-retain | V3 teardown test and production topology-aware list/test references exist. |
| 22–69 | `api/sessions_v3_*` | V3-excluded | Diagnostics, durable progress, executor, realtime, reconnect, stream, sync and terminal classification are current V3 implementation. |
| 70–73 | `api`: pairing/proxy helpers | uncertain-retain | Route registration and compatibility/security status are not proven retired. |
| 74–75 | `api`: target mapping/workspace route ID | live-neighbor-retain | Target mapping has a direct caller; route identity is a security/routing contract. |
| 76–78 | `api`: update runner/notification wrappers | live-neighbor-retain | Account-scoped update implementations and tests use the neighboring path. |
| 79–81 | `appstorage`: state/temp/workspace directories | public-api-retain | Canonical storage API; `WorkspaceStateDir` has direct tests. |
| 82–89 | `fff`: exported instance operations | public-api-retain | Intentional FFF API. Probes/search binaries use methods or their option-bearing implementations. |
| 90 | `gitstatus`: `ParsePorcelainV2` | live-neighbor-retain | Called by `SnapshotForResolvedPaths`; public projection over the snapshot parser. |
| 91–93 | `gitstatus`: exported watch/query helpers | public-api-retain | Exported active-package API; no explicit API retirement. |
| 94–95 | `identity`: ID generator/session clock options | test-helper-retain | Direct test seams. |
| 96 | `identity`: `actorForUserID` | uncertain-retain | Adjacent to identity/session auth state; no deletion proof. |
| 97–98 | `identity`: JWT/claims-for-test helpers | test-helper-retain | Explicit exported test helpers. |
| 99–100 | `model.NewService`, notification refresh | live-neighbor-retain | Production/test constructors and direct notification callers contradict naive deletion. |
| 101 | `permission`: policy cache loader | uncertain-retain | Security policy cache boundary; requires focused state validation. |
| 102 | `permission`: `manageAgentAction` | removable-candidate | Private definition-only trivial wrapper over `manageAction`; no test, tag, generated, interface, registry, or reflection use found. |
| 103 | `permission`: `ShouldApprovePlanManageUpdate` | public-api-retain | Exported policy API; no retirement decision. |
| 104–106 | `permission`: change count, host swarm ID, notification event type | removable-candidate | Private definition-only helpers; host ID duplicates `localSwarmID`, and active notification paths provide event types directly. Security/event tests are required before removal. |
| 107 | `provider/codex`: `buildRequestPayload` | test-helper-retain | Tests directly use this compatibility wrapper; production uses the option-bearing implementation. |
| 108–109 | `provider/codex`: session-ID extraction helpers | removable-candidate | Private definitions only; no source/test callers found. |
| 110 | `provider/codex`: `parseEventStream` | test-helper-retain | Tests call it; production uses reader variant. |
| 111–112 | `provider/codex`: response-output extraction/normalization | removable-candidate | Private dead-only chain, no production/test callers; confirm no serialized replay compatibility before deletion. |
| 113–114 | `provider/codex`: sensitive-key and debug-line no-ops | removable-candidate | Definition-only no-ops (`false` and `nil`); active privacy sanitizer and stderr debug implementation replace them. |
| 115–116 | `provider/codex`: exported reasoning formatter methods | public-api-retain | Exported internal provider API with active underlying reasoning logic. |
| 117–118 | `provider/codex`: request/service-tier compatibility helpers | test-helper-retain | Direct tests use them; production uses canonical replacements. Migrate tests only under a separate API decision. |
| 119–139 | `provider/copilot`: adapter/auth/credential implementation | dynamic-provider-retain | Daemon explicitly leaves Copilot unregistered pending paid-plan validation. Dormant is not retired. |
| 140–152 | `provider/copilot`: client pool/auth binding/CLI helpers | dynamic-provider-retain | Transitively implements dormant adapter/manager behavior. |
| 153–176 | `provider/copilot`: manager/turn/prompt/state/usage helpers | dynamic-provider-retain | Internally connected dormant provider implementation. |
| 177–189 | `provider/copilot`: runner and tool-wrapper implementation | dynamic-provider-retain | Provider interface/tool-wrapper implementation; no formal retirement decision. |
| 190–195 | provider defaults/diagnostics exported wrappers | public-api-retain | Exported provider-option and diagnostic convenience APIs; context variants are live. |
| 196–197 | `run`: custom agent tool definitions/names | live-neighbor-retain | Used by account-scoped runtime resolution. |
| 198 | `run`: deterministic AI task ID | uncertain-retain | Durable identity semantics; no safe deletion proof. |
| 199–200 | `run`: compaction threshold/status helpers | live-neighbor-retain | Part of compaction reporting. |
| 201–202 | `run`: V3 plan lifecycle persistence/hash | V3-excluded | V3 plan lifecycle implementation. |
| 203–210 | `run`: compaction/worktree/background scope helpers | live-neighbor-retain | Active memory compaction and runtime setup paths use neighboring implementations. |
| 211 | `run`: lifecycle phase classifier | uncertain-retain | State-machine helper; no positive removal proof. |
| 212–216 | `run`: prompt construction and agent resolution | live-neighbor-retain | Runtime composes instructions through these functions; direct tests exist. |
| 217–227 | `run`: approval, preview, plan/task execution, subagent and workspace-scope helpers | uncertain-retain | Permission/runtime orchestration boundaries; direct lifecycle tests exist for several symbols, and no grouped deletion proof exists. |
| 228 | `runtime`: lifecycle activity detector | uncertain-retain | Startup/shutdown state-machine risk. |
| 229–232 | `session`: execution shape, mode/preference and JSON equality | uncertain-retain | Durable session-state semantics and API/test risk. |
| 233–234 | `sessionreview`: exported classifiers | public-api-retain | API code/tests directly call classifiers. |
| 235–236 | `store/pebble`: auth bundle key/summary helpers | removable-candidate | Private definition-only helpers; no callers, tags, generated marker, interface or reflection role found. |
| 237 | `store/pebble`: `NewAuthStore` | live-neighbor-retain | Daemon/runtime and many tests construct the store. |
| 238–243 | `store/pebble`: unscoped auth/vault compatibility helpers | removable-candidate | Private legacy/unscoped helpers are definition-only; active callers use `ForAccount` variants. Batch must verify vault migration/persisted-data behavior. |
| 244 | `store/pebble`: execution telemetry snapshot | test-helper-retain | Tests/benchmarks use the telemetry API. |
| 245–260 | `store/pebble/keys.go`: model/identity/session/target/workspace/auth key helpers | public-api-retain | Exported key-construction and migration contracts; V3-named key at line 250 is additionally V3-excluded. |
| 261–309 | `store/pebble/keys.go`: Flow and Integration key families | public-api-retain | Flow is current product direction; integration store uses account-scoped variants. No migration/API retirement decision. |
| 310–318 | `store/pebble`: plan/search telemetry and V3 counters | V3-excluded | V3 acceptance/search/write instrumentation and tests. |
| 319–333 | `store/pebble`: session library/archive/search/migration | V3-excluded | V3 search, tombstone, lifecycle and migration implementation. |
| 334–335 | `store/pebble`: sync snapshot test hooks | test-helper-retain | Explicit V3 test hooks; also V3-excluded. |
| 336–342 | `store/pebble`: sync/workset snapshot helpers and hooks | V3-excluded | V3 tombstone, permission, usage, plan and workset implementation. |
| 343–346 | `store/pebble`: topology observed-source/workspace-binding helpers | uncertain-retain | Persistence/sync contracts; no migration proof for removal. |
| 347–353 | `store/pebble`: global voice legacy methods | retired-path-candidate | Public methods explicitly fail because account scope is required; private legacy implementation remains. Removal requires proof that no persisted legacy records need migration/recovery. |
| 354–357 | stream/swarm/todo compatibility helpers | uncertain-retain | Cursor, topology initialization and owner-summary compatibility risk. |
| 358–359 | Unix command cancellation | platform-retain | `//go:build !windows`; process-group cancellation is a safety path. |
| 360–361 | tool search/FFF query helpers | live-neighbor-retain | Search execution and FFF integration use neighboring option-bearing paths. |
| 362–363 | webpush constructor and worktree warning | test-helper-retain | Webpush constructor has direct tests; warning is exported compatibility/diagnostic API. |
| 364 | worktree `migrateLegacyConfig` | removable-candidate | Empty private method, definition only, no interface/registration/test/tag/generated use found. |

## Dependency-ready Coder batches

These are narrow, ownership-based future implementation scopes. No batch may touch V3 implementation files. Each Coder must start from the same clean committed parent boundary, commit its scoped result, and return a clean worktree. The parent must recall/review every diff and integrate one selected complete batch atomically.

| Batch | Scope | Candidate report lines | Required pre-removal proof | Focused validation after edit |
|---|---|---|---|---|
| R1 — root CLI parser (integrated) | `cmd/swarm/main.go` | root 1 | Repeated full symbol search; inspected command dispatch and parser-test availability; confirmed no build-tag or dynamic variant. Child `8f4aef09`; integrated `f5e40c1f`. | `gofmt`; `go build ./cmd/swarm` to run-scoped output; no directly relevant named parser test exists; root deadcode rerun. |
| R2 — root app isolated helpers (integrated) | `internal/app/app.go` | root 5, 11–12 | Repeated definition/reference searches; inspected plan-exit and `/commit` callback/test boundaries; retained line 10 and all V3 code. Child `fc73e1f6`; integrated `f3228cde`. | `gofmt`; `go build ./cmd/swarmtui` to run-scoped output; no directly relevant named plan-exit/commit test exists; root deadcode rerun. |
| S1 — permission private helpers | `swarmd/internal/permission/service.go` | swarmd 102, 104–106 | Inspect permission event writers, interface assertions, notification tests and policy registration; confirm definitions remain private/unreferenced. | `gofmt`; compile permission dependents; named permission notification/manage-agent tests only; swarmd deadcode rerun. |
| S2 — Codex obsolete internals | `swarmd/internal/provider/codex/client.go` | swarmd 108–109, 111–114 | Confirm no test/fixture/replay decoder references; verify privacy sanitization and current debug path; retain 107, 110, 115–118. | `gofmt`; compile Codex package/dependent daemon; named replay/privacy/debug tests only; swarmd deadcode rerun. |
| S3 — auth bundle private helpers | `swarmd/internal/store/pebble/auth_bundle.go` | swarmd 235–236 | Repeat repository search; inspect bundle serialization and account-scoped callers. | `gofmt`; compile Pebble store; named auth-bundle tests only; swarmd deadcode rerun. |
| S4 — unscoped auth/vault helpers | `swarmd/internal/store/pebble/auth_store.go`, `auth_vault.go` | swarmd 238–243 | Prove all callers use account-scoped variants; inspect legacy key migration and persisted vault compatibility. Retain on any uncertainty. | `gofmt`; compile store/daemon; named auth migration/encryption/vault tests only; swarmd deadcode rerun. |
| S5 — worktree empty migration stub | `swarmd/internal/worktree/service.go` | swarmd 364 | Confirm no interface/reflection or historical migration registration; empty body and definition-only status must remain true. | `gofmt`; compile worktree package/dependent daemon; named worktree config migration tests if present; swarmd deadcode rerun. |
| S6 — legacy global voice path | `swarmd/internal/store/pebble/voice_store.go` | swarmd 347–353 | **Conditional retired-path batch:** first prove persisted global voice records require no migration/recovery and public disabled methods can be removed without API break. Otherwise retain entire batch. | `gofmt`; compile store/voice/API dependents; named voice migration/account-scope tests; swarmd deadcode rerun. |

Scopes are non-overlapping. S3 and S4 share the auth domain but own different files and can be reviewed independently; if launched concurrently, the parent must document that logical overlap even though file scopes do not overlap. S6 is not dependency-ready until its persisted-data/API gate is satisfied; it must not be launched as a deletion batch merely because the analyzer reports it.

## Retained risk groups and prohibited batches

- **No V3 batch:** root lines 6, 13–22, 34–55, 66–94, 226–230 and swarmd lines 22–69, 201–202, 250, 310–342 are excluded.
- **No Copilot deletion batch:** swarmd lines 119–189 are intentionally dormant provider code, not formally retired.
- **No exported-key cleanup batch:** swarmd lines 245–309 include storage, Flow, Integration and migration contracts.
- **No FFF cleanup batch:** both modules expose intentional cgo/public wrapper surfaces used by probe/search binaries.
- **No UI-accessor cleanup batch:** many root UI findings are test-facing/callback APIs; an executable-only RTA scan does not establish API retirement.
- **No broad old-route batch:** run-stream, pairing, onboarding, TUI transitional and session route findings remain uncertain until route registration and compatibility are proven end-to-end.

## Suspicious-neighbor reachability notes

- Direct source references contradict several simplistic “unreachable” interpretations: root `chatTitleFromPrompt`; swarmd `agent.Service.resolveProfile`, `mapTopologyRuntimeTarget`, `gitstatus.ParsePorcelainV2`, notification `refreshSummaryLocked`, `model.NewService`, and session-review classifiers. These are retained.
- Deadcode `-whylive` explains why a function is live, not why a reported function is dead. It is useful in later batches to compare the candidate with its canonical replacement (for example account-scoped auth methods, option-bearing Codex parsers, or active notification writers), but it cannot prove a dead symbol safe by itself.
- The strongest candidates are definition-only private wrappers/no-ops with an identified canonical replacement. Public/exported symbols, test seams, compatibility keys, provider implementations, state-machine helpers and routing/security methods are retained by default.

## Checkpoint evidence

- Current report snapshots reproduced exactly: root 232, swarmd 364.
- Full report accounting: root 232/232 and swarmd 364/364.
- Launch-readiness: exit 0, 8 pass, 1 warning, 0 failure.
- Production source changes at the triage baseline: none.
- Validation deliberately not run during the documentation-only triage checkpoint: Go test suites or broad compile suites. The two requested analyzer scans and checked-in launch-readiness gate were run.

### Root cleanup R1/R2 evidence

- Isolated Coder R1 committed clean child `8f4aef09`; isolated Coder R2 committed clean child `fc73e1f6`.
- Parent recalled and reviewed both exact diffs, selected both complete handoffs, integrated them atomically, and recalled afterward. Parent integration commits are `f5e40c1f` and `f3228cde`.
- Removed exactly four ledger-approved private leaves: `parseYesOnly`, `App.buildPlanExitModalBody`, `App.commitSessionTitle`, and `App.commitLineageMetadata` (79 lines). No V3, public, dynamic, test-only, or uncertain symbol was removed.
- `gofmt` produced no post-integration source diff.
- Focused builds passed: `go build -o "$TMPDIR/root-deadcode-build/swarm" ./cmd/swarm` and `go build -o "$TMPDIR/root-deadcode-build/swarmtui" ./cmd/swarmtui`.
- No directly relevant named parser, plan-exit, or commit-helper test exists, so no test command was run.
- The documented bounded root scan reran successfully and decreased from 232 to **228 findings**, exactly matching the four removals. None of the removed symbol names remains in `.tmp/cp2-deadcode-root.txt`.
- `git diff --check` passed; the parent source tree was clean before this ledger update.

### Non-V3 TUI audit evidence

- Three isolated Coder audits started from clean parent `1c56d301`: legacy app/session-stream plus `ChatPage` adapter boundaries; command/config and command-modal boundaries; and presentation/accessor/modal boundaries. Each child performed repository reference, call-chain, interface/callback, event/update wiring, registry/build-tag, and test-reference review and ended with a clean worktree.
- The legacy stream audit retained root lines 2–4 because `startSessionEventStream`, `runSessionEventStream`, and `enqueueSessionStreamEvent` form the producer chain used by `openChatView`; the event loop drains the same queue, and `stream_jitter_test.go` and `session_global_stream_test.go` exercise its consumer/application behavior. Root lines 142–150 remain connected to the legacy route, constructor defaults, direct tests, or uncertain adapter compatibility.
- The command/config audit retained root lines 56–65 and its assigned command-modal findings. Config helpers have direct tests; command dispatch calls the active `/mode` path; `NewChatPage` initializes command and mention suggestions; app/home dispatch consumes model and theme modal actions; and `applyThemeToChat` and `boolLabel` have direct callers. Definition-only helpers in this group remain `uncertain-retain` because the approved ledger did not establish feature/API retirement.
- The presentation/accessor audit retained root lines 117–141 and 151–225. Exported accessors are test/integration seams; chat and home modals are connected through keyboard, mouse, draw, and action callbacks; lineage/theme/voice methods are public contracts; and private definition-only neighbors remain uncertain until their complete feature or compatibility path is formally retired.
- No child produced a source diff, so there was no implementation commit eligible for integration. The orchestration commit gate rejected each attempted no-change handoff rather than creating artificial edits. Parent recall confirmed all three child branches remained at `1c56d301` with clean worktrees; direct branch diffs against that parent were empty. The selected integration batch was therefore empty and the parent remained unchanged.
- Focused validation passed: `go build -o "$TMPDIR/cp3-tui-build/swarmtui" ./cmd/swarmtui`; named app tests `TestConsumeSessionStreamEventsDrainsBurst` and `TestApplySessionStreamEventCreatesAndDeletesHomeSession`; and named UI tests `TestTakePlanExitResolutionPreservesApprovedArgumentsBeforeClosing`, `TestExitPlanReviewToggleWritesCanonicalApprovedArguments`, and `TestModelsModalDrawsProvidersAboveModelsOnNarrowScreen`.
- The bounded root analyzer rerun produced **228 findings** and byte-matched `.tmp/cp2-deadcode-root.txt`: no analyzer delta, matching the zero-removal decision. `git diff --check` passed, and ignored analyzer/build caches were not staged.

## Relevant filepaths

- `.tmp/prelaunch-deadcode-root.txt`
- `.tmp/prelaunch-deadcode-swarmd.txt`
- `.bin/deadcode`
- `docs/checkpoints/initial-launch-cleanup.md`
- `docs/checkpoints/deadcode-triage.md`
- `cmd/swarm/main.go`
- `internal/app/app.go`
- `internal/app/chat_backend_adapter.go`
- `internal/app/tui_realtime_app.go`
- `internal/ui/`
- `internal/ui/v3chat/`
- `internal/launcher/`
- `pkg/startupconfig/config.go`
- `pkg/storagecontract/storagecontract.go`
- `swarmd/internal/agent/`
- `swarmd/internal/api/`
- `swarmd/internal/appstorage/storage.go`
- `swarmd/internal/fff/fff.go`
- `swarmd/internal/gitstatus/`
- `swarmd/internal/identity/`
- `swarmd/internal/notification/`
- `swarmd/internal/permission/service.go`
- `swarmd/internal/provider/codex/client.go`
- `swarmd/internal/provider/copilot/`
- `swarmd/internal/run/`
- `swarmd/internal/store/pebble/auth_bundle.go`
- `swarmd/internal/store/pebble/auth_store.go`
- `swarmd/internal/store/pebble/auth_vault.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/store/pebble/voice_store.go`
- `swarmd/internal/tool/command_cancel_unix.go`
- `swarmd/internal/worktree/service.go`

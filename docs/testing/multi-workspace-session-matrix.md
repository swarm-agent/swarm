# Multi-workspace session identity matrix

## Scope and evidence discipline

**Current verdict:** the core identity mismatch is repaired and has final-source
regression evidence. The supported mixed-agent live flow completed with documented
fixture/output corrections. This is **not blanket launch approval**. The final
reconciliation table and command register in **V6** supersede earlier pending
statuses; earlier sections retain the chronology of actual failures and fixes.

Baseline production source: `0712a332106aad28c62dc58953a95fcaa7fc5f90`.
This matrix was created before production fixes. It specifies reproduction and
acceptance, not launch readiness. The supplied incident shape is a saved parent
Git repository containing a separately saved independent Git repository; a
managed session changes its selected/default workspace but may retain execution
in the original repository. The original incident transcript was not retrieved,
and no original session or customer repository is a reproduction fixture.

Use disposable real Git repositories and isolated stores. Record saved identity,
source root, Git common directory, runtime root, owner, branch, base and HEAD
separately. Path ancestry and display names are not repository identity.
Private live endpoint/lane/session identifiers and raw evidence do not belong in
this tracked document. Source revision and sanitized test outcomes do.

Status vocabulary: **observed defect** (executed reproduction), **inspected**
(assertions/code read, not execution proof), **passed** (exact command/revision),
**unverified** (not executed). A passing characterization can demonstrate a defect;
it is not a passing acceptance test. Build identity is **local/no deployment**
unless a row explicitly records exact live evidence.

## Baseline

- Worktree initially clean at the source revision above.
- Remote `dev`: bounded SSH ref lookup did not return a SHA; current remote head
  is unverified. Do not equate a cached remote-tracking ref with remote truth.
- Existing candidate: read-only broker inspection reported the same source SHA.
  Existing loopback listeners were observed. The established Desktop root returned
  HTTP 200; an unauthenticated `/health` request on the established API returned
  HTTP 401. This is availability/auth-boundary observation, not an authenticated
  application identity proof. Browser transport association and ownership are
  not established by listeners or HTTP status alone.
- The deployment wrapper cannot load this worktree's absent ignored `.env`.
  This does not invalidate the existing endpoint. No deployment, replacement lane,
  tunnel restart, provider change or Models catalog change was performed.

## Contract under investigation

1. Catalog identity, session attachments, selected execution default and immutable
   repository/worktree ownership must be distinguishable and consistently resolved.
2. Account `set_default` affects later sessions, not the current session.
3. A session default change must either select an owned lane for the target Git
   repository or fail before mutation; relabeling a lane from another repository
   is not an execution switch. Attach/remove must not silently widen privileges.
4. A successful `restart_turn` must refresh prompt, working directory, filesystem
   scope and worker default together before another tool can use stale context.
5. Explicit authorized targets select that repository; omitted targets use the
   session execution default. Absolute paths cannot bypass ownership/containment.
6. Git/header UI must expose all attachments and distinguish the default, source
   repositories, parent lanes and retained child lanes, without losing failed or
   dirty-recoverable work or interpreting the first item as the default.

## Inventory organization

The executed command register, causal map, complete acceptance matrix and bounded
responsibility graph below supersede the initial pre-implementation inventory.
This inventory describes the pre-implementation checkpoint. Foundation, consumer,
UI and live corrections are recorded below; V6 gives the final effective status.

## Executed reproduction and command register

All commands below use production SHA `0712a332106aad28c62dc58953a95fcaa7fc5f90`
plus the uncommitted test-only changes described here; **not a committed repaired
candidate**. Build field for these commands: **local Go test binary / no deployment**.
Date: 2026-09-07. No provider, browser, original session or customer fixture used.

- **C1**: `cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run -run '^TestMultiWorkspaceIncidentCharacterization$' -count=1 -timeout=90s -v`
  — passed, 0.580s. Three cases; two intentionally assert the existing defect.
- **C2** (before fixture correction) and **C3** (after correction), exact same command:

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run -run '^(TestMultiWorkspaceIncidentCharacterization|TestManageWorkspaceSetSessionPreservesGlobalWorkspaceGrantsAndRestartsTurn|TestParseTaskProgramRejectsDesignerAndSplitCoderWorkspaceTargets|TestTaskProgramRepositoryLanePreflightReuseAndIsolation|TestTaskProgramApprovedLaneMappingDoesNotWidenScope|TestTaskProgramCohortErrorPreservesSuccessfulSibling|TestTaskProgramNativeReferenceRoundTrip|TestTaskProgramFinderDoesNotHydrateItself)$' -count=2 -timeout=90s -v
```

  C2 failed twice only at admission in the inherited move test: its empty
  directories violate current committed-Git workspace admission. C3 passed twice,
  3.834s overall, after replacing its three empty-directory fixtures with real
  committed repositories and isolating Git config. All its assertions remain.
  No production admission rule was weakened. Other tests in that large file were
  not executed and inherit no passing verdict.
- **C4**: `bash scripts/check-atlas-sync.sh` — PASS. `git diff --check` also passed.
- Test source digests: `service_multi_workspace_reproduction_test.go`
  `1bdd550edd27721d25a191744fec840a06afc062d5dc92de668d88123b14a4b6`;
  corrected `service_workspace_manage_worktree_test.go`
  `c091e89d64e65e2f5ee5f3492e93bcb09f9e5ef731880d8adae4f8411c8c273c`.
  Both paths are under `swarmd/internal/run/`. Independent P1/P2 review is
  unperformed; no critical-tier promotion. A single exact `.gitignore` allowlist
  entry makes this requested matrix trackable; private evidence remains ignored.
- **U**: no command executed; SHA is the baseline inspected source, build unverified,
  result unverified. Planned layers below are not passing tests.

`TestMultiWorkspaceIncidentCharacterization` verifies initial task-worktree
allocation against actual `git rev-parse --git-common-dir` and captured HEAD;
account-default mutation with full session equality; canonical `set_session`
mutation and persisted reload; restart flag detection; provider-tool context
rehydration; default versus explicit Coder target resolution; preserved lane
owner/branch; clean unchanged source HEADs; and stale-generation rejection with
full snapshot equality. Its canonicalizer is a fixture mapping saved identities,
not the production topology/API canonicalizer. Session creation uses a temporary
store; allocation uses the real task allocator shared with managed Git mechanics,
not the complete Desktop creation endpoint. Those are explicit remaining gaps.

| Transition | Saved source / selected identity | Actual runtime Git | Default worker Git | Explicit target worker Git | Observation |
|---|---|---|---|---|---|
| Initial managed parent | parent | parent | not launched | not launched | Allocator and scope agree |
| Account default to target | parent (unchanged session) | parent | not launched | not launched | Correct account-only behavior |
| Managed parent → nested target | target | parent | parent | target | **Observed defect**, C1/C3 |
| Managed parent → sibling target | target | parent | parent | target | **Observed defect**, C1/C3; nesting is not necessary |
| Unmanaged parent → nested target | target | target | target | target | Control passes C1/C3; not a supported new direct-session creation mode |
| Stale generation after move | unchanged | unchanged | no launch | no launch | Rejected, no partial snapshot mutation |

## Baseline causal map and inspected assertion inventory

This section describes baseline `0712a332` only, not current production behavior.
The current fault prevention and existing-session policy are summarized in V6.

Paths in this section are relative to `swarmd/internal/` unless prefixed `web/`.

**A — initial identity/allocation.**
`api/sessions_v3_primary.go:resolveSessionsV3CreateWorktree` and
`allocateSessionsV3CreateWorktree` dispatch principal-bound allocation, require
complete facts and matching requested branch identity. `worktree/service.go`
resolves the repository root and HEAD before allocating. The real-Git reproduction
proves task allocator mechanics choose parent correctly and saved nested-root
lookup returns the nested identity. It does **not** prove every Desktop create
route. No evidence supports blaming initial allocation for the reproduced move.

**B — attachments/default mutation (proven primary fault).**
`run/service_workspace_manage.go:setSessionWorkspaces` copies the existing session,
rewrites `WorkspacePath`, `WorkspaceName` and canonical source metadata, but keeps
`WorktreeEnabled`, `WorktreeRootPath`, branch, base and owner. It updates runtime
metadata only for an unmanaged session. It merges old grants into the requested
set, so `workspace_ids` currently behaves additively, not as an exact replacement
set/removal operation. `setDefaultWorkspace` instead updates only the account
selection; C1/C3 prove that distinction. `ApplySessionMutation` persists the
contradictory pair; this is not merely a stale UI label.

**C — scope and filesystem targets (proven continuation of fault).**
`run/service_workspace_context.go:resolveRunWorkspaceScope` checks the new source
against the saved identity/generation but chooses the preserved worktree as
`PrimaryPath`. It does not compare the lane's Git common directory to the new
source. `providerManagedWorkspaceContext` re-reads the session and calls
`syncWorkspaceScopeFromSession` for every authenticated tool call; C1/C3 prove
that re-reading the contradictory durable data still yields parent Git.
`tool/runtime.go:resolveWorkspacePath` roots relative requests at `PrimaryPath`
and checks normalized candidates against allowed roots; `resolveSearchRoot`
defaults to `.`. `tool/generic_filesystem.go:openRootedWorkspacePath` owns rooted
filesystem access. C1/C3 prove the supplied scope, not execution of every file
operation. Full read/search/find/list/write/edit/Bash behavior remains M12–M15.

**D — actual restart semantics (do not diagnose from the flag alone).**
`run/provider_tool_invoker.go:providerManagedToolRequiresTurnRestart` recognizes
`restart_turn`, and `ExecuteTool` returns typed `RestartTurn`. The actual V3 owner
is **`api/sessions_v3_executor.go`**, not solely the older `run/service.go` loop.
At `restartAfterTools`, V3 calls `resolveSessionV3Runtime`, rebuilds restart input
and the provider base request, and sets `StartNewChain`, `ResetTransport`,
`ForceFreshProviderContext` true with continuation disabled. Thus “restart is
ignored everywhere” is **not** supported by source. Restart cannot repair B/C.
The executor processes the entire returned tool-call list before this restart,
so same-response calls and mixed mutation/worker batches need a boundary test.
`TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn` was inspected:
it uses a fake provider and a read, verifies two provider requests, structured
history, fresh-context flags, lineage keys and no encrypted reasoning replay;
it never changes workspaces. **Unexecuted here.** The older `run/service.go`
`response.RestartTurn` branch reloads history; its different consumers need
separate testing rather than assuming parity with V3.

**E — workers/dependencies.**
`run/service_task_launch.go:resolveTaskTargetWorkspace` uses the existing
`WorktreeRootPath` for omitted target and separately authorizes explicit paths.
C1/C3 prove default/explicit divergence without launching children.
`run/service_task_program_workspace.go:repositoryLane` preflights before allocation,
persists a parent-owned lane, validates ownership/branch/cleanliness and ancestry.
`session/task_program_workspace.go:TaskProgramRepositoryLanes` preserves recorded
lanes and rejects an active competing program. `sourceHandoffsForJob` requires
integrated Coder dependencies; native Designer sources require exact validated
references and bounded UTF-8 bytes. These are distinct from attachment defaults.

Inspected and executed in C3:
- `TestTaskProgramRepositoryLanePreflightReuseAndIsolation`: real Git; preflight
  worktree inventory unchanged, durable distinct lane and later reuse, captured
  source HEAD unchanged, unauthorized repo inventory unchanged, dirty refusal.
- `TestParseTaskProgramRejectsDesignerAndSplitCoderWorkspaceTargets`: parser rejects
  Designer `workspace_path` and distinct Coder target strings. No spawn/store
  postconditions because this layer has no execution; runtime aliases still need
  canonical Git-identity tests.
- `TestTaskProgramApprovedLaneMappingDoesNotWidenScope`: approved source/narrow
  scope accepts typed lane substitution; rejects missing typed binding or widened
  scope. Pure approval adapter, not live Git authority proof.
- `TestTaskProgramCohortErrorPreservesSuccessfulSibling`: successful head survives
  aggregate failure, blocked and missing outcomes remain distinct; no real workers.
- `TestTaskProgramNativeReferenceRoundTrip`: exact native identity survives outcome
  projection; missing persisted artifact cannot fabricate ready output.
- `TestTaskProgramFinderDoesNotHydrateItself`: first-stage Finder gets no self-report.

**F — projections/Git/header (source-established consumer gaps, not browser proof).**
`store/pebble/session_workspace_projection.go` normalizes ranked grants and emits
path-free usage for stable identities. `session_workspace_scope.go` indexes only
canonical source when present, returning before additional grants; usage indexing
and path-scoped lookup must not be conflated. `api/git_snapshot.go` prefers the
session worktree and ignores explicit `workspace_path` when one exists. It also
uses the primary session base for commit listing. This differs from the tool's
persisted secondary-lane selectors; UI must not compensate by guessing paths.
`api/sessions_v3_review_worktrees.go` classifies repository-scoped review candidates,
not a complete session attachment/child-history inventory.
`web/src/features/desktop/layout/desktop-app-page.tsx:3144–3176` queries one selected
session path, with separate explicit review inventory. `git/api.ts` accepts one
path/session query key. `services/session-workspace.ts` returns one canonical path.
`chat/components/desktop-v3-chat-header.tsx` accepts a single `workspaceName` string,
not typed attachment/default items. Complete enumeration needs API/state/header
work; no rendered defect is claimed without browser evidence.

## Baseline expanded acceptance matrix

Every row references the command register above: it supplies exact command, SHA,
build and result fields without duplicating an unreadable command in each cell.
`None inspected` means no test assertion was verified for that exact intersection,
not proof that the repository contains no relevant tests. Authority letters refer
to the concrete symbols above. C1/C3 characterization success is **defect evidence**,
not acceptance success. All U rows remain unverified and require their own exact
command/build/result entry when executed.

| ID | Requirement / threat and intersection | Production authority | Existing test / inspected assertions | Reproduction status; planned narrow layer | Evidence / remaining gap |
|---|---|---|---|---|---|
| M01 | One saved Git root; initial source/lane/base agree | A | New characterization: actual common-dir and base equality | Allocator mechanics pass; full create API unverified | C1/C3; add API + real Git create/replay |
| M02 | Nested independent saved Git roots; move cannot relabel old lane | B/C/E | New characterization: selected target, runtime/default parent, explicit target | Observed defect | C1/C3; canonicalizer/API and correct-lane acceptance pending |
| M03 | Account default versus current session default | B | New characterization: whole snapshot unchanged and current catalog updated | Pass for account-only semantics | C1/C3; next-session allocation unverified |
| M04 | Restart request, fresh prompt, tool context and worker default agree | C/D/E | Flag/rehydration in characterization; V3 restart test inspects request flags only | Lane mismatch persists; full provider restart unverified | C1/C3 + U provider fixture |
| M05 | Independent sibling repositories after switch | B/C/E | New characterization: same mismatch without nesting | Observed defect | C1/C3; proves not exclusively ancestor matching |
| M06 | Two saved subpaths in one repository; no duplicate repository authority | A/B | Admission contract requires exact Git root; no exact subpath assertion inspected | Unverified; admission + alias real Git | U; reject unsupported subpath rather than invent support |
| M07 | Duplicate display names, reordered attachments, no first-item default | B/F | Header has one string prop; no exact multi-name test inspected | Unverified; store/API/component | U; use IDs and explicit default |
| M08 | Non-Git, empty, unborn and bare targets; fail before allocation | A | Inherited move test failed on empty dirs; repaired fixture, not admission code | Empty rejection observed indirectly | C2; direct no-mutation admission cases U |
| M09 | Create/attach multiple targets; repeated attach idempotent | B/F | Move control preserves three grants and primary rank | Unmanaged control pass; exact flat-set semantics unverified | C3; exact attachment API + replay U |
| M10 | Remove nondefault/default attachment; no implicit grant retention | B/C/F | Source merges all old grants; no removal assertion inspected | Additive behavior inspected | U; removal/default replacement + reload |
| M11 | Dirty parent or pending integration during default change | B/E | Lane dirty refusal in program fixture, not session switching | Partial program evidence only | C3; session-switch unchanged-state/retention U |
| M12 | Omitted/relative read, search, find and list after switch | C/D | Scope primary proved; per-tool filesystem operation not executed | Unverified actual tools | U; real files with distinguishable markers |
| M13 | Omitted/relative write, edit and Bash cwd after switch | C/D | Scope/worker divergence proved, no actual mutation tool invocation | Unverified actual tools | U; negative unchanged other repo markers |
| M14 | Explicit authorized absolute targets vs default paths | C/E | Explicit worker resolves target common-dir | Target resolver pass, tool routing U | C1/C3 + U; source-vs-owned-lane mutation contract |
| M15 | Relative explicit workspace selectors and same-repo alias roots | C/E | Relative worker paths join canonical source; no executed relative case | Unverified | U; symlink/alias and ambiguous target tests |
| M16 | Mutation and another tool in same response; stop stale calls | D | V3 restart applied after call batch | Source risk only | U; fake provider records each scope/request boundary |
| M17 | Attach/remove/default while worker active; immutable assignment | B/E | None inspected for this transition | Unverified | U; held worker + concurrent mutation + unchanged assignment |
| M18 | Pause/resume, process restart, interrupted mutation and reload | B/C/D/E | Characterization reloads stored snapshot, not process/store reopen | Partial durable read evidence | C1/C3; reopen/crash/fault injection U |
| M19 | Foreign principal, revoked attachment, stale generation/path | B/C/E | Stale move rejects with entire snapshot unchanged; unauthorized program repo inventory unchanged | Partial negative evidence | C1/C3; cross-account/removal revocation U |
| M20 | Traversal, symlink replacement, forged lane/common-dir, missing path | A/C/E | Approval test rejects unbound/widened lane; no real symlink case here | Partial negative evidence | C3; rooted I/O + Git authority fixtures U |
| M21 | Supported Finder → two disjoint same-repo Coders → Designer | E | First Finder no self-hydration; integrated dependency required in source | Full flow unverified | U; exact source/prompt/artifact stage evidence |
| M22 | Unsupported cross-repo Coder program or Designer workspace selector | E | Parser rejects both errors | Parser pass, runtime no-spawn proof pending | C3; canonical alias resolution + zero allocation/session count U |
| M23 | Regular cross-repository workers | E | Explicit resolver target is correct; no workers launched | Resolver pass only | C1/C3; independent allocations/handoffs and integrations U |
| M24 | Stage barriers and exact integrated dependency content | E | sourceHandoffs requires integrated state; lane reuse preserves source HEAD | Partial contract evidence | C3; stale/missing bytes, fresh stage base and Designer input U |
| M25 | Failed/stopped/dirty child plus successful sibling; recovery | E/F | Cohort adapter preserves success/missing errors; dirty lane refuses reuse | Adapter/lane checks pass | C3; real interrupted children, unfinished-only program and retained UI U |
| M26 | Every attachment/source/parent lane/child lane shown; no hidden history | F/E | UI uses one selected path; source-scope index early return | Source gap established | U; bounded enumeration + component/browser |
| M27 | Staged, unstaged, untracked, conflicts and committed changes per lane | F | Existing status fields expose categories; no multi-repo rendered assertion | Unverified | U; independent markers + exact diff/status selectors |
| M28 | Exact-target review/integrate/promote; never implicit | E/F | Program fixture keeps captured HEAD; UI/API select primary lane | Partial isolation evidence | C3; wrong/stale selector and source/target no-change tests U |
| M29 | Reconnect/live completion/failure/default change retain visible rows | D/F | None inspected for multi-repo intersection | Unverified | U; delayed/aborted response, unavailable/stale UI and browser |
| M30 | Larger bounded sets, duplicate aliases, pagination and cancellation | B/E/F | set_session limit is 64 requested IDs, history cap 8; no exact bound execution | Source bounds only | U; 1/2/8/64/65 cases, historical >8, four-worker inventory budgets |
| M31 | Conflicting legacy metadata; no invented provenance or silent fallback | B/C/E/F | New real-Git characterization proves contradiction accepted | Observed defect | C1/C3; preserved dirty history + fail-closed migration |
| M32 | Replay/concurrency: two defaults, stale grants, partial allocation/store failure | A/B/E | Lane persists/rolls back on error in source; no injected crash executed | Unverified | U; full postconditions, one winner and rollback error visibility |
| M33 | Permissions denied mid-stage; approved scope cannot widen | B/E | Approved lane test rejects source substitution and broader scope | Adapter pass | C3; permission-run/store postconditions and live U |
| M34 | Native artifact exact dependency, no ambient selection substitution | E | Native reference round-trip preserves exact Git and rejects missing evidence | Adapter pass | C3; final Designer bytes/pixels and live context U |

## Canonical repair responsibilities and dependency contracts

These are bounded ownership proposals for subsequent approved checkpoints, **not a
launched Task Program**. No children were launched in this checkpoint. The parent
must reconcile exact scopes against the actual foundation diff, declare the whole
staged program if using dependent delegation, and commit prerequisites before any
Coder launch. Do not give all responsibilities below to one Coder. Shared docs,
ledger reconciliation and cross-system confirmation belong to the parent/audit.
New regression files listed below are intentional concrete output targets.

1. **Foundation/store identity (stage F1).** Own
   `swarmd/internal/store/pebble/session_workspace_projection.go`,
   `swarmd/internal/store/pebble/session_workspace_scope.go`,
   `swarmd/internal/store/pebble/session_workspace_identity_test.go`,
   `swarmd/internal/session/session_workspace_identity.go` and
   `swarmd/internal/session/session_workspace_identity_test.go`.
   Deliver one typed attachment/default/repository-lane authority with revalidated
   immutable ownership, expected revision/generation, bounded retained history and
   atomic rejection tests. Specify output fields before consumers: stable saved
   workspace ID/generation; canonical repository/common-dir identity; captured
   source; runtime lane owner/path/branch/base; explicit execution default;
   attachment versus retained historical availability. Reuse existing records;
   do not create a second persistence authority.
2. **Foundation transition integration (stage F2, depends F1).** Own
   `swarmd/internal/run/service_workspace_manage.go`,
   `swarmd/internal/run/service_workspace_context.go`,
   `swarmd/internal/run/service_multi_workspace_reproduction_test.go`,
   `swarmd/internal/run/service_workspace_manage_worktree_test.go`,
   `swarmd/internal/api/sessions_v3_primary.go`,
   `swarmd/internal/api/sessions_v3_workspace_identity_test.go`.
   Wire allocation/selection to the typed authority, preserve old lane provenance,
   reject ambiguous/dirty/revoked unsafe moves, convert characterization to real
   acceptance tests. Worktree implementation changes, if needed, are a separate
   prerequisite responsibility in `swarmd/internal/worktree/service.go` and
   `swarmd/internal/worktree/session_identity_test.go`, not concurrent overlapping
   edits. Initial API allocation must not be bypassed by a fixture canonicalizer.
3. **Runtime boundary consumer (stage R, depends F2).** Own
   `swarmd/internal/api/sessions_v3_executor.go`,
   `swarmd/internal/api/sessions_v3_workspace_restart_test.go`,
   `swarmd/internal/run/provider_tool_invoker.go`,
   `swarmd/internal/run/provider_workspace_restart_test.go`,
   `swarmd/internal/run/service.go`,
   `swarmd/internal/tool/generic_filesystem.go`,
   `swarmd/internal/tool/runtime.go`,
   `swarmd/internal/tool/runtime_bash_execution.go`,
   `swarmd/internal/tool/runtime_workspace_target_test.go`.
   Own only execution-context refresh and tool use; prove same-response barrier,
   exact prompt/cwd/scope agreement and safe explicit targeting. No scheduler/UI.
4. **Worker/program consumer (stage R, independent of runtime edits after F2).** Own
   `swarmd/internal/run/service_task_launch.go`,
   `swarmd/internal/run/service_task_program_workspace.go`,
   `swarmd/internal/run/service_task_program_scheduler.go`,
   `swarmd/internal/run/service_task_program_dependencies.go`,
   `swarmd/internal/run/service_task_program_workspace_test.go`,
   `swarmd/internal/run/service_task_program_identity_test.go`,
   `swarmd/internal/session/task_program_workspace.go`.
   Deliver target and dependency hydration against F2's immutable lane contract,
   preserving the one-repository Coder program rule, no-allocation preflight and
   interrupted child ownership. Separate from runtime/provider-loop responsibility.
5. **Git enumeration/API consumer (stage G1, depends F2).** Own
   `swarmd/internal/api/git_snapshot.go`,
   `swarmd/internal/api/git_realtime.go`,
   `swarmd/internal/api/sessions_v3_review_worktrees.go`,
   `swarmd/internal/api/session_repository_inventory.go`,
   `swarmd/internal/api/session_repository_inventory_test.go`.
   Deliver bounded authenticated inventory and exact selector semantics, using
   persisted program/child lineage; no path-fallback, no implicit promotion. If a
   new registered route is necessary, assign its exact registration file to this
   job before launch rather than widening to all API files.
6. **Desktop consumer (stage G2, depends G1).** Own
   `web/src/features/desktop/git/**`,
   `web/src/features/desktop/services/session-workspace.ts`,
   `web/src/features/desktop/layout/desktop-app-page.tsx`,
   `web/src/features/desktop/chat/components/desktop-v3-chat-header.tsx`,
   `web/src/features/desktop/chat/components/desktop-v3-chat-header.spec.tsx`,
   `web/src/features/desktop/chat/components/desktop-v3-existing-conversation-pane.tsx`,
   `web/src/features/desktop/chat/components/desktop-v3-new-session-pane.tsx`,
   `web/src/features/desktop/state/session-repository-inventory.ts` and
   `web/src/features/desktop/state/session-repository-inventory.spec.ts`.
   Deliver typed multi-attachment/default header and repository/lane Git groups,
   honest partial/loading/revoked states, deduplication and bounded subscriptions.
7. **Fresh integration audit (stage A, depends R and G2).** Parent or distinct later
   agent audits exact integrated identities and assertions, then performs the
   approved exact-build live stage. Never infer whole-system completion from the
   individual handoffs. No production implementation in this checkpoint.

## Supported live acceptance topology (original specification)

Use two saved disposable independent repositories **A** and **B**, with an optional
nested independent **C** as a separate case. Attach A/B; select B as execution
default while retaining A's history. Program job order:

1. Finder `inspect-b`, explicit B, scope `spec/`, reports a fixture requirement and
   exact selected source/HEAD. It must not consume its own unfinished report.
2. Coders `left-b` and `right-b`, both explicit B, depend on `inspect-b`, disjoint
   `src/left.txt` and `src/right.txt`. They must share the immutable stage base,
   write only their own allocated worktrees, commit clean handoffs, and integrate
   into B's parent-owned program lane; captured A/B/C remain unchanged.
3. One managed Designer `present-b`, depends on both integrated Coders, receives
   exact integrated content/HEAD. Its explicitly requested acceptance deliverable
   is a single native HTML comparison sheet containing two UI treatment variants
   of the fixture content. This is an explicit multi-variant design brief while
   preserving one Designer job, with no `workspace_path` or `owned_scope`.
   Verify exact source handoff availability first; do not claim a managed Designer
   can read arbitrary checkout files merely because prose says to inspect them.
   Validate the native artifact and inspect pixels before visual acceptance.

Negative preflight: Coders split across independent B/C; Designer with a workspace
selector; unknown/revoked saved target; same-stage overlapping scopes; later job
with invalid/glob/traversal scope; conflicting alias identity; stale/dirty lane;
missing integrated dependency; ambiguous multiple artifact sources. For each,
assert error AND unchanged program/child/reservation/worktree inventories. Parser
rejection alone is not the complete runtime negative proof.

Regular cross-repository Coder launches are a separate supported fixture, not a
way around the Task Program restriction. Integrate each into its own authenticated
parent repository lane. Recovery keeps completed committed work and dirty failed
work inspectable, resolves the actual blocker, and starts a new program containing
only unfinished jobs; never replay completed children or auto-commit dirty ones.

Live stages remain capped at ten minutes with observable progress every fifteen
seconds, no more than two active fixture Coders, early stop on demonstrated stall,
and private exact build/session evidence. No source-only or HTTP-health result
above fulfills these live requirements.

## Foundation acceptance (supersedes baseline defect status only where listed)

**F1**, 2026-09-07: local foundation working tree derived from baseline
`0712a332106aad28c62dc58953a95fcaa7fc5f90`; exact committed identity and post-commit
rerun are recorded in the private checkpoint handoff. No deployment.

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/worktree ./internal/tool -run '^(TestRepositoryIdentityIsolation|TestWorkspaceAttachmentBoundsAndHistory|TestWorkspaceGitAdminReadBoundary|TestMultiWorkspaceIdentityTransitions|TestManageWorkspaceSetSessionPreservesGlobalWorkspaceGrantsAndRestartsTurn|TestManageWorkspaceAdoptWorktreeKeepsSameSessionAndRefreshesScope|TestManageWorkspaceAdoptWorktreeRollsBackWhenCurrentWorktreeIsDirty|TestTaskProgramRepositoryLanePreflightReuseAndIsolation|TestMandatorySessionWorktreeRunRevalidation|TestRunWorkspaceScopeUsesManagedWorktreeAsPrimaryAndPromptsToolRoot|TestRunWorkspaceScopeCoderAllowsOnlyCanonicalLinkedWorktreeGitAdminRoot|TestRunWorkspaceScopeCoderSourceWorkspaceReadDoesNotRequestPermission|TestRunExecutionContextPreservesCoderLinkedWorkspaceReadAuthorization|TestRunExecutionContextPreservesCoderSourceWorkspaceReadAuthorization|TestResolveRunWorkspaceScopeIncludesAttachedWorkspaceWithoutSwitch)$' -count=2 -timeout=90s
```

Passed: run 6.512s, worktree 0.166s, tool 0.006s. Fifteen selected top-level tests,
not package-wide tests. Earlier focused runs exposed stale empty-directory and
missing-base fixtures, then two real scope gaps: source read access included a
sibling Git admin directory, and runtime assembly re-added mutable Coder roots.
Fixtures now use committed Git/explicit attachments; negative assertions remain
and both gaps are corrected. Independent P1/P2 review remains pending.

| Matrix rows | Foundation result / remaining boundary |
|---|---|
| M02/M05/M31 | Passed real-Git isolated default switching and rejection of contradictory source/lane metadata; prior lane retained and reused. Historical incident facts remain baseline evidence. |
| M03/M09/M10 | Account default remains session-neutral; omitted set preserves attachments; explicit set removes grants, preserves explicit default and historical lanes; service reload agrees. Full client creation/replay remains later work. |
| M06/M08/M20 | Actual common-directory alias equality; nested independent unsaved target cannot select saved ancestor; non-Git/missing/subpath roots, wrong branch/base and replaced alias reject without changing source HEAD/status or worktree inventory. Bare/unborn admission remains existing workspace authority, not newly claimed evidence. |
| M11/M17/M19 | Dirty switching, retained delegated-child pinning, foreign principal, stale generation and revoked attachment reject without rewriting the prior session or dirty bytes. Gate conservatively pins even completed delegated history; finer resolved-history eligibility is not implemented here. |
| M18/M32 | Failed persistence and stale event-sequence CAS roll back newly allocated worktree and preserve entire prior snapshot/source inventory. Reload through a fresh service passes; full process crash and concurrent child creation races remain unverified. |
| M30 | Parser preserves omitted/empty intent, caps raw IDs at 64 and rejects nonstring IDs. Historical helper retains 64 entries instead of evicting after eight; allocation refuses capacity overflow. Full large UI/fan-out proof pending. |
| M12–M16/M21–M29/M33–M34 | No blanket acceptance upgrade: full tool/restart/program/API/UI/live consumers remain in subsequent checkpoints. Adjacent source-read and Git-admin scope assertions pass. |

Canonical foundation: `setSessionWorkspaces` performs exact flat-set/default
selection via `ApplySessionMutation` with `ExpectedLastEventSeq`; allocation
failure/CAS conflict is an error, never a success. `prepareSessionWorkspaceLane`
allocates against the exact target or reuses authenticated historical ownership.
`RepositoryIdentity` and `ValidateOwnedIdentity` compare actual Git authority,
checkout root, branch and base. `resolveRunWorkspaceScope` rejects contradictory
legacy provenance without repairing labels or deleting files. History does not
provide execution access; saved attachments are reauthorized on every resolution.
The tool schema now states replacement/omission/default semantics explicitly.

Existing contradictory sessions require an explicit separately reviewed recovery
that proves original ownership; this foundation deliberately refuses to invent
provenance. Retained worker/lane pins reject retargeting rather than guessing
whether pending integration is safe. No automatic cleanup, source promotion,
provider run, browser proof or Models catalog edit was performed.

## Runtime restart consumer evidence (R1)

Parent-owned validation on foundation `1a4f77ca` plus the restart-consumer diff:

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/api -run '^(TestProviderWorkspaceRestartRejectsStaleInvoker|TestSessionV3ToolBatchRestartBoundary|TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn|TestMultiWorkspaceIdentityTransitions)$' -count=2 -timeout=90s
```

Passed: run 2.315s, API 1.339s. M04/M16 gain bounded evidence: the actual V3 batch
helper executes only the prefix through restart, with no subsequent fixture write
or manufactured result; an invalidated invoker cannot dispatch more calls. Real
Git default transitions reject the previous captured runtime path, and accept
newly hydrated context. The existing provider-loop fixture proves fresh structured
history, lineage flags and no encrypted-reasoning replay. It required replacing
obsolete mutable-system-agent setup with canonical model settings and correcting
the exact expected composite boundary to `epoch_fresh_context+restart_after_tool`.
These are separate tests, not a full provider-driven workspace mutation E2E.

Remaining consumer work: filesystem and Task Program changes authored in isolated
workers are retained, not integrated. Parent ran their focused tests; no worker
executed tests. The installed recovery commit authority rejected blocked child
status, so no failed child's work was force-committed or discarded. Exact lineage
and recovery diagnostics stay in private checkpoint evidence. The environment
pivot is deferred; current workspace/UI/live acceptance retains priority.

## Named-primary recovery correction (R2)

Manual integration exposed an allocator mismatch: `manageWorktreeRecoveryDestination`
used the Task Program seed-derived validator for a named primary session. Named
session allocation derives the directory from the requested branch slug, not the
session ID. Added `ValidateSessionRepositoryLane` for the two canonical primary
allocation forms; program lanes retain the exact seed-bound validator. The caller
still authenticates parent/child, exact durable destination, source account, branch,
cleanliness and base ancestry; symlink/path substitution is rejected.

Parent validation (source based on `4c5bfa07`, not installed runtime):

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/worktree ./internal/tool -run '^(TestSessionRepositoryLaneNamedAllocation|TestManageWorktreeNamedPrimaryRecovery|TestManageWorktreePrimaryRecovery|TestManageWorktreeProgramRecovery|TestManageWorktreeProgramRecoveryRejectsUnsafeContext|TestManageWorktreeRecoverySavedSourceAuthority)$' -count=2 -timeout=120s
```

Passed: worktree 0.135s, tool 17.732s. Named-primary integration consumes the child
commit while leaving captured source and dirty sibling unchanged; existing stale,
foreign and revoked-source negatives pass. No claim of installed-tool correction.

Replacement implementation workers now return clean committed code and authored
tests without running commands. Parent validated the exact filesystem commit
`62515b727f2526874d433eba68ae6f56f3eb1840` (WorkspaceTarget/GenericFilesystem,
count=2, 0.099s) and task commit `fccdcd7270aad40035c968bfb49549660bda25d9`
(four named target/lane/stage tests, count=2, 3.845s). Canonical integration remains
rejected by the installed old validator. Both commits and earlier dirty children
are preserved; no raw cherry-pick or forced blocked-child commit was performed.

## Consumer acceptance R3 — resumed canonical integration

2026-09-07, local/no deployment. Canonical integration now succeeds after the
named-primary validator activation: filesystem child `62515b727f2526874d433eba68ae6f56f3eb1840`
and task child `fccdcd7270aad40035c968bfb49549660bda25d9` integrated into
`b3737dde491a770d8dddfbc867e33d66f47546ce`. Earlier dirty children remain retained,
untouched and uncommitted; successful children were not replayed. Parent owns all
executed validation. No Coder command capability or environment pivot was added.

Source tested: integrated SHA above plus scoped worker/dependency/test changes
recorded by the six R3 test-file digests in the audit ledger. Exact commands:

```sh
cd swarmd
GOMAXPROCS=2 go test -p 2 ./internal/run -run '^(TestTaskTargetCanonicalRootsAndProgramPreflight|TestTaskTargetTypedLaneIdentity|TestTaskTargetRuntimePreflightAndRegularChildren|TestTaskFinderSelectedWorkspaceDoesNotInheritParentRoot|TestTaskProgramRealStageUsesIntegratedBase|TestTaskProgramRepositoryLanePreflightReuseAndIsolation|TestTaskProgramEndedOwnerPreservesCommittedSibling|TestTaskProgramTransitionFailurePreservesSnapshot|TestTaskProgramPlannedStartRejectsNonRunnableCheckpoint|TestTaskProgramCohortErrorPreservesSuccessfulSibling|TestTaskProgramNativeReferenceRoundTrip|TestTaskProgramFinderDoesNotHydrateItself|TestTaskProgramApprovedLaneMappingDoesNotWidenScope|TestTaskProgramIgnoresAmbientArtifactSelection|TestProviderWorkspaceRestartRejectsStaleInvoker|TestMultiWorkspaceIdentityTransitions)$' -count=2 -timeout=180s
GOMAXPROCS=2 go test -p 2 ./internal/tool -run '^(TestWorkspaceTarget|TestGenericFilesystem)' -count=2 -timeout=120s
GOMAXPROCS=2 go test -p 2 ./internal/api -run '^(TestSessionV3ToolBatchRestartBoundary|TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn)$' -count=2 -timeout=120s
```

Results: runtime **passed 16.659s**, filesystem **passed 0.105s**, API **passed
1.331s**, each repeated twice. Initial Finder test failed because its inherited
fixture fabricated a non-Git worktree; replacing it with a real allocation exposed
missing captured source/base metadata in shared Finder children. Preparation now
uses the runtime default and retains source/base rather than aliasing source to
lane. A fixture nil-map failure was corrected before the successful executions.
No identity check or negative assertion was weakened.

| Matrix intersections | Executed evidence and scope |
|---|---|
| M04/M16 | R1 restart tests rerun with the integrated tree; stale invoker/suffix cannot continue. |
| M12–M15/M20 | Actual read/list/write/edit preserve explicit/default roots, read-only and Coder ownership; search/find shared resolver rejects ambiguity and unauthorized Git admin; missing-primary Bash refuses before process creation. Full FFF indexing and shell filesystem sandboxing are not claimed. |
| M21/M24 | Two disjoint real-Git sibling allocations share the exact immutable base/common-dir, integrate via the scheduler, unlock the Designer dependency, and fork the next child at the new HEAD with both files. Managed Designer receives quoted immutable integrated patch evidence, not an instruction to use inaccessible checkout tools. Dirty/stale/binary/oversized evidence rejects; source and unrelated checkout state stays unchanged. This is a scheduler fixture, not provider-backed multi-agent execution. |
| M22 | Runtime executor preflight rejects split Coder repositories before program/session/worktree creation, with session snapshot and repository inventory equality. Existing parser rule remains unchanged. |
| M23 | Regular Coder preparation in two authorized repositories persists independent owned worktrees, bases, common-dir identity and tool scope. Finder same/cross-root preparation is tested separately. No provider calls or cross-repository program support inferred. |
| M25/M28 | Canonical real child integration observed; earlier dirty children remain recallable. Deterministic owner termination preserves committed sibling and unfinished child; a new unfinished-only program does not overwrite/restart the old ID. Real-Git retained lane reuse rejects dirty binding before persistence. |

Source evidence is bounded to a 128 KiB committed diff (256 KiB total handoff),
15-second Git subprocess deadline, disabled external diff/textconv, exact lane and
HEAD checks. It describes integrated changes, not the entire repository. Binary or
oversized prerequisites require explicitly prepared bounded input; no silent
truncation/fallback. The patch includes committed prerequisite changes in the lane;
it confers no checkout mutation or extra filesystem grant.

Remaining acceptance belongs to the already-planned UI/audit/live work: complete
provider-backed Finder → two Coders → Designer flow, simultaneous provider timing,
full create API, process-crash windows, rendered header/sidebar/reconnect/history,
and exact running candidate proof. No deployment, browser, launch readiness or
independent P1/P2 verdict claimed by R3. Critical runner unchanged.

## G4 — Session-wide repository and attachment consumers

2026-09-07. Source base `9cd2dec4ea5b088d32af651e45bd851d0d22a605`
plus the scoped parent dirty-read/tombstone/spacing corrections recorded by this
change. Build: local test binaries/browser bundles, **no deployment**. Earlier
source-only Git/header gaps in section F are superseded by this implementation,
not by a claim about the installed testbench.

- Canonical read-only `GET /v3/sessions/{id}/repositories` enumerates saved source
  attachments, parent/worker worktrees, retained contexts and program lanes.
  Startup maintenance advances durable indexes in resumable 100-row batches;
  reads never reconcile programs or migrate. Existing bounded worktree ownership
  history is imported; absent historical provenance is not invented.
- Account/user/parent ownership, current catalog generation, exact root, managed
  allocator/common repository, branch and base are revalidated. Status/realtime
  preserve explicit source versus lane selectors. Retained selectors are reads,
  not commit authority. Dirty-read validators are separate from clean transition
  validators. Archived/deleted child provenance remains inspectable; missing
  physical lanes report unavailable rather than clean.
- Opaque continuations authenticate complete scope, limit and meaningful parent
  inventory revision. Unrelated/token-only updates do not invalidate traversal.
  One request inspects at most 20 rows/contexts sequentially with bounded status
  output and file-list truncation; no all-account scan in the read path.
- Sidebar groups workspace/source and worker/lane identities and presents staged,
  unstaged, untracked/conflict counts/files. It keeps at most 200 displayed rows,
  continues through explicit replaceable windows, and preserves exact selection
  even when unloaded. Header accumulates at most 64 actual attachment identities,
  with a separate default marker and manual traversal past long retained history.
  Canonical cache actions, focus/reconnect, polling and hidden-view cancellation
  drive debounced refresh; stale/error/unavailable states remain visible.
- Existing manual operations require fresh exact current-session identity; other
  retained rows are inspection-only and use existing explicit worktree review.
  No auto integration/promotion or new Git mutation authority was added.

### Executed command register

**G4-backend**, from `swarmd/`:

```sh
GOMAXPROCS=2 go test -p 2 ./internal/store/pebble ./internal/session ./internal/api ./internal/worktree ./internal/runtime -run '^(TestRepositoryHistory|TestSessionRepositoryHistory|TestTaskProgramRepositoryHistory|TestSessionRepositories|TestSessionRepositoryLaneNamedAllocation)' -count=2 -timeout=120s
```

Passed: store 1.276s, session 0.004s, API 1.868s, worktree 0.146s.
Runtime compiled only (`no tests to run`), not daemon startup execution.
Assertions include real named/task Git allocations, pre-index parent lane history,
archived/deleted workers, dirty retained file counts, tiny-page uniqueness,
reattachment stability, exact selectors, stale/foreign/forged rejection and
unchanged source HEAD/staging/dirty bytes. Store tests assert restart/backfill,
scoped cursor rejection, dedup, late program lookup and unchanged rejection state.

**G4-web**, from `web/` with locked dependencies and installed Chrome:

```sh
SWARM_TEST_BROWSER_CHANNEL=chrome node --import tsx --test --test-force-exit --test-concurrency=1 --test-timeout=70000 src/features/desktop/state/session-repositories.spec.ts src/features/desktop/git/api.spec.ts src/features/desktop/git/session-repository-picker.spec.ts src/features/desktop/git/session-repository-picker.browser.spec.ts src/features/desktop/chat/queries/session-attachments.spec.ts src/features/desktop/chat/components/session-attachments.spec.tsx src/features/desktop/chat/components/session-attachments.browser.spec.ts src/features/desktop/chat/components/desktop-v3-chat-header.spec.tsx
node node_modules/typescript/bin/tsc -b --pretty false
```

Passed 27 tests (8.641s), including three browser fixtures; typecheck passed.
Fixtures cover 460 workers, 64 same-named attachments after 400 historical rows,
explicit non-first default/removal, late-window access, no implicit retargeting,
canonical events, reconnect/hidden scheduling, stale responses, cursor loops and
failed-refresh recovery. Browser transport is fully intercepted and production
CSS bundled in memory; no ambient daemon or credentials required. Earlier missing
dependencies/browser runtime, inherited Vite proxy/optimizer fixture errors and
ES target `.at()` failure were corrected; none counted as passing evidence.

Four mobile/desktop fixture PNGs were pixel-inspected at 375/1440 widths. Long
names wrap within the bounded dialog; retained rows and warnings are legible.
A cramped scroll-region/footer boundary was corrected with explicit separation
and re-inspected. Scroll clipping is intentional within scrollable lists; no
horizontal overflow or capture chrome. These are component fixtures, **not the
full running Desktop application**. Private screenshots stay ignored.

Independent P1/P2 review remains pending, no curated critical test promotion.
Full-app/deployed endpoint/realtime/provider stage proof and startup crash-window
verification remain the separately approved integration-audit/live checkpoint.
No Models change, host update, testbench deployment, source promotion or push.

## A5 — Live audit exposed primary-create base identity gap

Exact candidate `9ed08b68b59b18da31640c93e8d1bf2574a16131` passed the fast
build gate, rebuilt successfully in the existing persistent isolated lane, and
passed fresh authenticated browser availability checks. The first disposable
session failed before provider execution with `session worktree base identity is
missing`. Thus G4 source/component evidence does not establish M01/M21 live success.

Audit correction: primary HTTP create now persists allocator `BaseCommit`; the
allocator captures the allocated checkout HEAD rather than source HEAD when the
selected base branch differs. Existing-worktree reuse checks clean branch/HEAD
and supplies that base. New HTTP regression also exposed timestamp-sensitive
create hashing: server model-profile `applied_at` is excluded while model choice
and the remaining request fields remain bound. No legacy session base is guessed.

Focused command on the A5 diff over that candidate:
`cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/api ./internal/worktree -run '^(TestSessionsV3CreatePersistsAllocatorBase|TestSessionAllocationCapturesSelectedBase|TestSessionsV3PrimaryWorktreeCreateReplayDoesNotReallocate)$' -count=2 -timeout=90s`
passed API 2.220s and worktree 0.100s. Assertions cover durable allocator base,
forged metadata rejection before allocation/persistence, same-request replay,
selected-branch actual Git base and unchanged source/inventory after rejection.
Full corrected live flow remains pending; no provider or final visual success.

### A5 second live audit

Corrective build `b980c46d3328e010d0682e97d5af72d332120cbd` passed focused
cross-system tests and the fast gate, then rebuilt the same persistent lane.
A new fixture successfully changed its exact two attachments/default, restarted,
and executed real FFF/list/read against the target-owned lane. The full four-job
provider program completed with two integrated commits and a ready Designer
comparison artifact containing the exact integrated patch. **Parallelism was not
proved:** durable timing showed sequential Coders because the scheduler retained
its initial single-Finder reservation ceiling. Inventory also rejected the actual
transition-allocated parent lane and shared worker source provenance.

Corrections reserve the largest declared stage width (still capped by permission
policy/max_concurrency), recognize the exact existing compact transition allocator,
and resolve read-only immediate-parent lane provenance only after principal and
exact-path checks. Unknown/foreign ancestry is not a fallback; no mutation grant
is created. New child preparation records captured parent source rather than
mislabeling its lane as a saved repository. Historical missing bases remain gaps.

`GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/api ./internal/worktree ./internal/permission -run '^(TestTaskProgramReservationUsesWidestStage|TestReserveSubagentProgramCountsOneInvocationAndOnlyReadyCapacity|TestSessionRepositories.*|TestSessionRepositoryParentIdentityIsExactAndOwned|TestSessionAllocationCapturesSelectedBase|TestTaskTargetRuntimePreflightAndRegularChildren|TestTaskProgramRealStageUsesIntegratedBase)$' -count=2 -timeout=120s`
from `swarmd/` passed: run 3.936s, API 2.321s, worktree 0.138s, permission 0.090s.
Earlier command with an exact `TestSessionRepositories` selection ran no API tests;
it is not API evidence. Corrected parallel/provider and inventory proof pending.

### A5 corrected provider observation

On exact build `b79026253574ffed2b90b7a655c9363ff8eaf181`, a new disposable
trial completed Finder → two Coders → Designer. Coder launch timestamps differed
by 22 ms and their 40.648s/34.332s intervals overlapped. Both shared the same base;
parent-run Git patch-equivalence and exact newline-byte assertions passed, target
common-directory identity matched, parent was clean, and all three captured
repositories retained their original HEAD and clean status. Header dialog pixels
showed exactly two attachments with explicit default. The ready artifact pixels
showed both requested treatments, each with the exact heading and two values,
without clipping or overlap. The earlier artifact had only one shared heading
and is not accepted as equivalent visual evidence.

Inventory now resolves the current and retained parent lanes. Integrated removed
workers remain explicit unavailable rows rather than disappearing. Their labels
still reflected historical running snapshots; an inventory-only exact program
reader now supplies linked job lifecycle without invoking mutating reconciliation.
Focused HTTP inventory/exact-parent tests passed twice, 2.128s. Rebuild/reconnect,
retained dirty recovery and unsupported-program live negatives remain separate.

On `068df26727bd9e91ae0ac14801b59cc3190319b3`, persistent restart retained the
completed program/artifact and attachments. Unsupported split-repository Coder
plan submission returned HTTP 400 before any parent event/run mutation. A deliberately
uncommitted regular Coder returned `dirty-recoverable`, preserved its original
HEAD and `recovery.txt`; canonical recall returned the same child, file and base.
Default switching was rejected as pinned by retained delegated work. No commit,
integration or cleanup of that fixture was performed. Inventory preserved its
row and untracked count; full browser traversal/reload showed the same two
attachments and integrated-removed rows. A stale regular-worker running label
was corrected using exact parent task-call lineage (not filesystem inference).
Focused inventory/exact-parent tests passed twice, 2.066s. List/config recovery
attempts failed before recall succeeded; generic list behavior is not validated.

Final full-app pointer inspection on `cb09303dd72b2f3aea32e48be86b3920d8a4dd29`
showed retained failed worker and dirty count after restart, but the file disclosure
was clipped by the plan card's remaining height. A safe layout correction makes
the repository section itself scrollable and the file region nonshrinking; no
forced browser click is accepted as proof. Typecheck and eight focused repository
browser/state tests pass (3.361s); exact-build pointer recheck required.

## V6 — Final reconciliation (2026-09-07)

### Revision and evidence boundaries

- **Source-tested:** `6e345d0437a5b60c1d3eec65ff2c480514f131fe` with
  unchanged production/test bytes. V6 changes documentation and test inventory only.
- **Running-build-tested:** that same full SHA, according to the retained exact-build
  acceptance report and fixture evidence from the live checkpoint. The final trial
  completed all four jobs; Coders started 25 ms apart and overlapped for their
  40.963s/30.500s intervals. Both used the same immutable stage base.
- **Deployment:** the existing isolated persistent candidate lane was rebuilt and
  tested at that SHA. This is historical revision-bound acceptance, not a new
  current-health probe. No host-runtime deployment, captured-checkout promotion,
  push, release or Models change is implied. Private deployment-client edits remain
  outside this public source change and need their own operational review.
- **Important qualification:** the final trial's right-file content contained
  specification labels. Parent validation detected it; a bounded correction attempt
  was stopped before its first command after miscopying the marker. A guarded
  fixture-only corrective commit then preserved both integrated child commits.
  Exact newline bytes, disjoint scopes, clean parent and captured-repository
  immutability passed afterward. This is corrected acceptance, not flawless
  provider output. The Designer consumed the original integrated patch; it was
  not rerun against the later fixture correction.
- The original final-build artifact clipped both headings. An exact-lineage
  typography repair produced a ready candidate, independently pixel-inspected:
  both treatments retain the full marker and values without clipping/overlap.
  The long token wraps awkwardly (including an isolated final character in one
  panel); this proves content reachability, not polished typography. Explicit
  candidate/head choice is still required; the original is retained.
- Final pointer/reload evidence supersedes A5's last pending scroll check: normal
  scrolling and clicking opened the retained untracked file below the tall plan
  card. The attachment dialog retained two identities and an explicit default.
  Earlier failed/removed workers remain visible, not falsely clean. Reinspection
  of the retained header, dirty sidebar and repaired artifact pixels in V6 agrees
  with these bounded claims. Screenshots and exact live identifiers remain private.

### Exact final-source command register

**V6-smoke**, repository root:

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/api ./internal/worktree -run '^(TestMultiWorkspaceIdentityTransitions|TestProviderWorkspaceRestartRejectsStaleInvoker|TestSessionV3ToolBatchRestartBoundary|TestSessionsV3CreatePersistsAllocatorBase|TestTaskProgramReservationUsesWidestStage|TestSessionRepositoryParentIdentityIsExactAndOwned|TestSessionAllocationCapturesSelectedBase)$' -count=2 -timeout=120s
```

Passed twice: run 2.841s, API 1.391s, worktree 0.140s.

**V6-backend**, repository root; selected assertions, not whole-package suites:

```sh
cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/worktree ./internal/tool ./internal/api ./internal/session ./internal/store/pebble -run '^(TestRepositoryIdentityIsolation|TestWorkspaceAttachmentBoundsAndHistory|TestWorkspaceGitAdminReadBoundary|TestMultiWorkspaceIdentityTransitions|TestManageWorkspaceSetSessionPreservesGlobalWorkspaceGrantsAndRestartsTurn|TestManageWorkspaceAdoptWorktreeKeepsSameSessionAndRefreshesScope|TestManageWorkspaceAdoptWorktreeRollsBackWhenCurrentWorktreeIsDirty|TestTaskProgramRepositoryLanePreflightReuseAndIsolation|TestMandatorySessionWorktreeRunRevalidation|TestRunWorkspaceScopeUsesManagedWorktreeAsPrimaryAndPromptsToolRoot|TestRunWorkspaceScopeCoderAllowsOnlyCanonicalLinkedWorktreeGitAdminRoot|TestRunWorkspaceScopeCoderSourceWorkspaceReadDoesNotRequestPermission|TestRunExecutionContextPreservesCoderLinkedWorkspaceReadAuthorization|TestRunExecutionContextPreservesCoderSourceWorkspaceReadAuthorization|TestResolveRunWorkspaceScopeIncludesAttachedWorkspaceWithoutSwitch|TestTaskTargetCanonicalRootsAndProgramPreflight|TestTaskTargetTypedLaneIdentity|TestTaskTargetRuntimePreflightAndRegularChildren|TestTaskFinderSelectedWorkspaceDoesNotInheritParentRoot|TestTaskProgramRealStageUsesIntegratedBase|TestTaskProgramEndedOwnerPreservesCommittedSibling|TestTaskProgramTransitionFailurePreservesSnapshot|TestTaskProgramPlannedStartRejectsNonRunnableCheckpoint|TestTaskProgramCohortErrorPreservesSuccessfulSibling|TestTaskProgramNativeReferenceRoundTrip|TestTaskProgramFinderDoesNotHydrateItself|TestTaskProgramApprovedLaneMappingDoesNotWidenScope|TestTaskProgramIgnoresAmbientArtifactSelection|TestProviderWorkspaceRestartRejectsStaleInvoker|TestSessionV3ToolBatchRestartBoundary|TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn|TestSessionsV3CreatePersistsAllocatorBase|TestSessionAllocationCapturesSelectedBase|TestSessionsV3PrimaryWorktreeCreateReplayDoesNotReallocate|TestTaskProgramReservationUsesWidestStage|TestSessionRepositoryParentIdentityIsExactAndOwned|TestSessionRepositoryLaneNamedAllocation|TestManageWorktreeNamedPrimaryRecovery|TestWorkspaceTarget.*|TestGenericFilesystem.*|TestRepositoryHistory.*|TestSessionRepositoryHistory.*|TestTaskProgramRepositoryHistory.*|TestSessionRepositories.*)$' -count=2 -timeout=180s
```

Passed twice: run 16.795s, worktree 0.473s, tool 0.594s, API 5.468s,
session 0.005s, store 1.161s. No unselected test inherits this result.

**V6-web**, repository root:

```sh
cd web && SWARM_TEST_BROWSER_CHANNEL=chrome node --import tsx --test --test-force-exit --test-concurrency=1 --test-timeout=70000 src/features/desktop/state/session-repositories.spec.ts src/features/desktop/git/api.spec.ts src/features/desktop/git/session-repository-picker.spec.ts src/features/desktop/git/session-repository-picker.browser.spec.ts src/features/desktop/chat/queries/session-attachments.spec.ts src/features/desktop/chat/components/session-attachments.spec.tsx src/features/desktop/chat/components/session-attachments.browser.spec.ts src/features/desktop/chat/components/desktop-v3-chat-header.spec.tsx && node node_modules/typescript/bin/tsc -b --pretty false
```

Passed 27 tests, 8.614s; typecheck passed. Nonfatal Node registration and bundler
option deprecation warnings remain. Browser fixtures intercept transport; they
are not additional live-testbench executions.

**L5** refers only to the prior live checkpoint's retained acceptance report,
fixture outputs and screenshots, not a newly executed V6 command. Canonical plan
retrieval during reconciliation exceeded the tool response quota; the complete
prior command transcript could not be independently re-read here. The retained
report identifies the exact build, while structured fixture/Git outputs and pixels
corroborate the bounded results. Full per-stage command-transcript reconciliation
remains a provenance gap; recover it through bounded canonical plan retrieval
before treating L5 as a fully reproducible command register. Public-safe sequence: prepare → attach/restart → program → parent Git validation → bounded
output/artifact correction → unsupported-plan rejection → retained dirty recall
and pinned-switch rejection → pointer/reload inspection. These stages are not a
new checked-in runner or a portable one-command acceptance suite. The public
matrix does not invent missing command transcripts or expose private paths.

### Final disposition of all acceptance intersections

All V6 command references bind to the final SHA above. Earlier C/F/R/G/A entries
remain historical evidence, not competing current statuses. **Partial** explicitly
means the whole row is not accepted; its next action is the missing proof.

| ID | Final evidence / executed assertion selection | Result and exact remaining action |
|---|---|---|
| M01 | V6-backend `TestSessionsV3CreatePersistsAllocatorBase`, `TestSessionAllocationCapturesSelectedBase`, create replay; L5 actual creation | Pass for persisted allocator base/replay and tested live creation; broader creation permutations unverified. |
| M02 | V6-backend `TestMultiWorkspaceIdentityTransitions` nested case; L5 sibling transition | Correct owned target or reject; real nested canonicalizer/provider combination remains a distinct live fixture. |
| M03 | V6-backend identity transitions whole-session equality | Account-only default semantics pass; explicitly test next-session default inheritance before claiming that intersection. |
| M04 | V6-backend identity transitions, stale invoker, batch boundary and provider restart fixture; L5 attach/restart/read | Pass for tested runtime/tool/default alignment; do not infer every provider transport. |
| M05 | V6-backend sibling transition; L5 source/target/common-dir postconditions | Pass for independent sibling isolation. |
| M06 | V6-backend `TestRepositoryIdentityIsolation` and nested saved lookup | Exact Git-root/alias rejection passes; arbitrary saved subpaths are unsupported, not an accepted routing feature. |
| M07 | V6-web same-name attachment/default tests and header browser fixture | Pass for 64 distinct identities and non-first default; full-app duplicate-name trial remains unverified. |
| M08 | V6-backend identity isolation negatives; baseline empty-directory failure | Partial: non-Git/missing/subpath rejection proved; add direct bare/unborn no-allocation cases. |
| M09 | V6-backend identity transitions and repository-history dedup; L5 two attachments | Partial: exact sets/reload/dedup pass; exercise repeated attach request replay through the complete client API. |
| M10 | V6-backend identity transitions removal/default rejection; V6-web removal | Pass for tested exact removal, explicit replacement requirement and retained non-grant history. |
| M11 | V6-backend identity transitions/lane dirty rejection; L5 pinned dirty child | Pass for conservative refusal and preserved dirty bytes; completed history also pins, finer eligibility not implemented. |
| M12 | V6-backend `TestWorkspaceTarget.*`/`TestGenericFilesystem.*`; L5 actual FFF/list/read | Partial: root selection and real live target reads pass; full per-operation FFF indexing permutations remain unverified. |
| M13 | V6-backend actual write/edit and missing-primary Bash refusal | Partial: run an explicit post-switch Bash cwd/unchanged-other-repository live assertion; no shell filesystem sandbox claim. |
| M14 | V6-backend filesystem authority and canonical worker target tests | Pass for tested authorized absolute/default selectors and rejected unauthorized/admin access; not arbitrary shell confinement. |
| M15 | V6-backend canonical roots/typed lanes and search-selection ambiguity | Partial: alias/root selection tested; add a complete relative-selector symlink-replacement end-to-end case. |
| M16 | V6-backend batch prefix filesystem postconditions and stale invoker | Pass: restart/error prevents suffix execution; rejected calls are not fabricated as successful history. |
| M17 | V6-backend retained-child transition rejection; L5 retained pin | Partial: assignment is conservative/immutable; execute held-worker concurrent mutation race before claiming live concurrency safety. |
| M18 | V6-backend fresh-service reload, store history reopen/backfill; L5 persistent rebuild | Partial: restart retention passes; inject process death at allocation/CAS/outbox boundaries and verify recovery. |
| M19 | V6-backend foreign/stale/revoked transition, target and repository-inventory negatives | Pass for selected rejection plus unchanged snapshots/Git; not exhaustive revocation timing. |
| M20 | V6-backend repository identity, Git-admin/typed-lane and HTTP selector negatives | Partial: examined substitutions fail closed; add timed symlink-swap/TOCTOU proof at actual rooted I/O boundary. |
| M21 | V6-backend real stage and target preparation; L5 four jobs, overlapping Coders, ready Designer | Supported topology completed; exact output required parent correction. Repeat untouched provider trial for repeatability, not to erase failed evidence. |
| M22 | V6-backend runtime no-spawn split-target test; L5 HTTP 400 with parent event/run equality | Pass for split-repository rejection. Designer selector parser evidence remains C3; add runtime inventory equality for every unsupported combination. |
| M23 | V6-backend `TestTaskTargetRuntimePreflightAndRegularChildren` | Partial: independent regular repository allocations pass deterministically; execute two actual cross-repository provider workers and separate integrations. |
| M24 | V6-backend `TestTaskProgramRealStageUsesIntegratedBase`; L5 exact stage bases/patches | Pass for staged integrated dependency handoff and stale/binary/oversized rejection. Later fixture correction was not a new Designer dependency run. |
| M25 | V6-backend recovery/cohort/ended-owner tests; L5 dirty recall and visible retained file | Partial: retention/unfinished-only identity and pinned refusal pass; arbitrary failed-child resume and generic list remain unproved. Never auto-commit dirty work. |
| M26 | V6-backend history/inventory; V6-web 460-worker windows; L5 full browser rows | Pass for tested attachments/source/parent/worker retention, explicit unavailable removed worktrees and late-window access. Missing historical provenance remains unavailable. |
| M27 | V6-backend inventory status and V6-web picker; L5 untracked retained file click | Partial: dirty counts/file disclosure pass; execute independent staged/unstaged/conflict/committed full-app selection fixtures. |
| M28 | V6-backend exact selectors/named-primary recovery; L5 captured-source equality | Partial: exact read/integration and no implicit promotion pass; no real promote operation or every stale UI mutation exercised. |
| M29 | V6-web cancellation/stale/default/refresh tests; L5 persistent reload/terminal rows | Partial: reload and fixture scheduling pass; controlled live transport gap, late-response and completion-race proof still needed. |
| M30 | V6-backend bounds/history/cursors; V6-web 64 attachments/460 workers | Partial: bounded storage/window traversal passes; measure large-set live timing/fan-out and full 1/2/8/64/65 API boundary matrix. |
| M31 | V6-backend contradictory legacy source/lane rejection and inventory history | Pass for fail-closed mismatch with retained evidence; no automated legacy repair or invented base/owner. |
| M32 | V6-backend injected persistence/CAS/transition failure and create replay | Partial: tested failures preserve state; two real concurrent default mutations and process-crash allocation cleanup remain unverified. |
| M33 | V6-backend approved lane scope mapping | Partial: adapter rejects widening; exercise permission denial mid-stage with durable reservation/program/child unchanged-state assertions. |
| M34 | V6-backend native reference/no-ambient-selection/dependency tests; L5 exact repair and pixels | Partial: exact native handoff/correction works; native discovery and fresh-context reference loss remain product gaps. Persist exact reference in canonical checkpoint context before recovery. |

### Root cause and safe existing-session handling

The reproduced sequence was: allocate correctly in A → change saved/default
identity to B while keeping A's managed lane → persist that contradiction →
rehydrate it → default tool/worker still uses A while explicit worker uses B.
Both nested and sibling fixtures reproduced it, so ancestry was not the sole
cause. The original incident transcript and customer repository were never
independently inspected; this is an incident-shaped controlled reproduction.

Current prevention is canonical, not a label patch:
`run/service_workspace_manage.go:setSessionWorkspaces` authorizes the exact flat
set and uses event-sequence CAS; `service_workspace_identity.go` allocates/reuses
an authenticated repository-owned lane and preserves history. Its
`validateSessionRepositoryIdentity` plus `worktree/identity.go:ValidateOwnedIdentity`
checks actual Git authority/branch/base before runtime scope resolves.
`provider_tool_invoker.go` rejects stale captured execution and invalidates the
step after restart; `api/sessions_v3_tool_batch.go` stops the remaining batch.
Primary creation now persists the actual allocator base; worker preparation and
bounded committed dependency evidence preserve source versus execution identity.
The repositories route, exact-parent/program reads, scoped history indexes,
Desktop inventory cache/header/picker and scrollable file region expose the same
facts without granting retained rows mutation authority.

For an affected existing session: preserve its files, commits, base/owner records
and dirty children. Do not edit metadata to match a display label, guess a missing
base, delete a lane, force a switch or promote a captured checkout. Execution
rejects incomplete/contradictory identity; the inventory can show unavailable
provenance. Use authenticated exact-source inspection and separately reviewed
recovery only when ownership is provable. If not provable, keep the old session
retained and start a new correctly targeted isolated session; moving retained
work is a distinct approved recovery operation. Completed descendants can still
pin switching; integrating them is not a promise that this conservative gate clears.

### Launch verdict

**Core repair accepted with bounded evidence; overall launch approval withheld.**
Prioritize the unresolved cross-repository provider integration, concurrent/crash
and permission-boundary proofs, full-app status/transport permutations and
independent P1/P2 test review before a launch-critical sign-off. The table names
each remaining exact proof. No new tests were promoted into the critical runner.
The retained live artifact reference/discovery problem and private deployment
client changes need separately scoped product/operational follow-up; no additional
checkpoint was created by this reconciliation.

### Reconciliation validation and final source state

`bash scripts/check-atlas-sync.sh` passed, as did the committed-range gate:

```sh
BASE_SHA=0712a332106aad28c62dc58953a95fcaa7fc5f90 HEAD_SHA=6e345d0437a5b60c1d3eec65ff2c480514f131fe bash scripts/check-atlas-sync.sh
```

`git diff --check` passed. A bounded read-only comparison verified all 31 changed
test-file SHA256 entries against actual bytes, exactly M01–M34 in the final table,
and no selected private endpoint/session/home/key markers in baseline-to-current
added text. This is a targeted privacy check, not a general secret-audit claim.
Private live evidence is ignored and not staged. This reconciliation changes only
this matrix, `docs/swarm-atlas.md`, and `docs/testing/test-audit-ledger.tsv`;
production/test bytes remain at the final tested SHA. Documentation is left
uncommitted for explicit source review; no new commit, push or promotion occurred.

## L1 — Attach-only launch contract (2026-09-07)

This is a new execution contract, not another acceptance upgrade to V6. All prior
V6 text is retained. Source baseline remains `6e345d0437a5b60c1d3eec65ff2c480514f131fe`;
runner changes are local/uncommitted and do not change the running application.
A bounded authenticated Desktop read and separate broker/forward association
confirmed the existing active candidate at that same SHA. Its retained lane owner
is not the current source worktree. No rebuild, deployment, tunnel change,
provider/model change or first-workspace substitution was performed.

### Case counting and evidence contract

There are **48 required executable subcases: four in each of S01–S12**. Each ID
below needs its own `pass`, `fail`, or `not-run` result, exact source/runner/build,
start/end time, assertions and private evidence locator. A scenario passes only
when all four required cases pass. Do not count filenames, Go top-level tests,
HTTP successes, AI claims, scheduler suite exits or historical V6 results as
additional executed subcases. L1 implements lifecycle/connection infrastructure;
**this new product wave is 0 pass / 0 fail / 48 not-run** until later scenario
execution. Historical and narrower deterministic assertions remain inputs only.

Use fresh suite-owned committed Git fixtures A/B/C with distinct unpredictable
markers. B is nested-independent for the nested cases, otherwise sibling; compare
actual common directories rather than path prefixes. Record each captured root's
HEAD, index/status and marker bytes before and after. No customer repositories,
ambient first session, implicit default change, captured-source promotion, shared
model mutation or crash of the shared candidate. Provider/worker counts are
separate from suite concurrency; verify capacity before the live wave.

| Scenario / lane | Four independently reported executable cases | Required proof and inspected support / missing boundary |
|---|---|---|
| S01 / routing | S01-a explicit A creation; S01-b explicit B creation despite account default A; S01-c replay identical create request; S01-d reject forged allocator base | Compare persisted source ID, lane common-dir, allocator base and create/allocation counts. `sessions_v3_base_identity_test.go:TestSessionsV3CreatePersistsAllocatorBase` inspected: real HTTP handler/store, fake allocator; forged create has no session/allocation, replay retains original base after fake base changes. Full selected-root real-Git HTTP/provider fixture is missing. |
| S02 / safety | S02-a nested-independent B switch; S02-b sibling B switch; S02-c unsaved nested root cannot resolve A; S02-d contradictory source/lane rejects | `service_multi_workspace_reproduction_test.go:TestMultiWorkspaceIdentityTransitions` inspected completely: real Git common-dir/base, independent target lane, ancestor lookup and contradictory provenance rejection, captured HEAD/status unchanged. Runtime canonicalizer is a fixture; full live combination still required where claimed. |
| S03 / routing | S03-a attach A/B with explicit non-first B default; S03-b remove A preserving retained history but revoking access; S03-c remove default without replacement rejects; S03-d restart refreshes next prompt/tool/worker context and blocks old suffix | Identity transition assertions inspect exact grants, full snapshot equality, fresh-service reload and stale invoker rejection. Provider batch boundary is prior V6 evidence, not newly executed here. Live attach replay/restart and actual post-restart tool execution remain required. |
| S04 / routing | S04-a real read B marker; S04-b real search B content; S04-c real find B filename; S04-d real list B directory | Inspect actual tool outputs for exact marker and root; ensure A marker absent. `runtime_workspace_target_test.go` inspected: executes read/list, but search/find test only the shared resolver and ambiguity rejection. Real FFF index/query results are missing from the new runner. |
| S05 / routing | S05-a relative write to B lane; S05-b exact edit in B lane; S05-c Bash reports exact B cwd/common-dir; S05-d aggregate wrong-repository immutability after all three | Inspected filesystem test checks written/edited bytes and rejected outside/read-only targets unchanged, including absent parent directory after denied create. Missing-primary Bash test proves no process marker only; successful post-switch Bash cwd needs actual execution. Compare A/B captured roots and old A lane, not just B output. No shell sandbox claim. |
| S06 / safety | S06-a explicit authorized B target from A; S06-b relative selector resolves declared source exactly; S06-c removed/revoked B denies new calls; S06-d ambiguous path/paths rejects without expansion | Inspected filesystem selection test compares exact default/explicit roots and rejects conflicting selectors before expansion. Identity fixture revokes catalog entry and checks entire session unchanged. Relative selector through complete runtime and live revocation timing remain missing. |
| S07 / workers | S07-a regular Coder A assignment; S07-b overlapping regular Coder B assignment; S07-c integrate A exact committed handoff only into A parent lane; S07-d integrate B separately with captured roots unchanged | Each worker must have real immutable base, distinct owned path/common-dir, exact marker bytes and clean committed handoff. Parent performs separate canonical integrations and verifies patch equivalence/destination. Existing regular preparation tests are prior V6 support only; the inspected legacy `task-program-worktrees.mjs` is neither this proof nor attach-safe. Missing real cross-repository provider integration runner. |
| S08 / workers | S08-a Finder exact B input; S08-b two disjoint same-repo Coders share base and overlap; S08-c stage barrier integrates both exact outputs; S08-d next-stage consumer sees immutable integrated content and rejects stale/missing evidence | Measure actual worker intervals, not launch calls or slot count. Exact newline bytes and source HEADs required; any correction is separately reported, never untouched-provider success. V6 describes the real-stage fixture and historical provider trial; neither counts for this wave. Inspected legacy runner runs separate sequential programs, changes shared models and uses stale session contracts; replace only missing proofs later. |
| S09 / safety | S09-a held running child blocks default switch; S09-b attempted mutation cannot change child's immutable scope/base; S09-c two competing default mutations yield one CAS winner; S09-d loser leaves no extra lane/partial grants | Inspected identity fixture creates retained child metadata and injects stale CAS, asserting whole snapshot/worktree inventory unchanged. It does not run a held worker or race two mutations. Add barrier-based tests around actual execution and persistence. |
| S10 / safety | S10-a permission denial before worker admission; S10-b denial mid-stage retains committed sibling/dirty child; S10-c symlink/path substitution denies rooted I/O; S10-d foreign principal/stale selector rejects without mutation | Assert reservation/program/child/worktree counts and bytes, not just error strings. Inspected identity fixture rejects foreign/stale moves; filesystem test rejects unauthorized roots/admin access. Mid-stage permission and timed substitution are missing proof, not accepted from adapter tests. |
| S11 / safety | S11-a subprocess dies after allocation before mutation; S11-b dies across commit/outbox boundary; S11-c reopen validates attachments/default/lane consistency; S11-d recover unfinished work without replaying successful sibling | Use only local temporary stores/processes and deterministic injected barriers. Inspected transition fixture injects returned persistence/CAS errors and fresh service reload; it is not process-crash evidence. Never restart the live candidate. Explicitly name any unsupported injection boundary. |
| S12 / Desktop | S12-a full-app attachment dialog shows IDs/default; S12-b staged/unstaged/untracked/conflict/committed lane selections; S12-c retained failed/removed worker and dirty file normal pointer reachability; S12-d forced transport gap/reconnect/late response keeps current default and completion | Canonical repositories API/state/header/picker own rendering; V6 component and historical pointer results are not new full-app proof. Capture normal scrolling/clicks and pixel-inspect every claimed state for overflow, wrong selection, hidden status and stale response. Missing dedicated isolated browser runner. |

### Runner boundary and lifecycle

`scripts/run-testbench-launch-prerun.sh` remains the only suite manifest. Its
explicit `--attach-only` path uses the supplied Desktop root with same-origin
`/v1/auth/desktop/session`, `/v1/swarm/topology` and `/v1/agent-model-settings` reads.
No daemon port inference or redirects; credentials stay in process memory.
Identity is pinned for child inspection. Build/lane evidence remains a separate
read-only broker verification, never inferred from the topology or local HEAD.

Only `critical` and `attach-inspect` are currently admitted in attach-only mode.
Legacy live wrappers are deliberately rejected before `.env`, SSH, deployment,
tunnel or model mutation. This is a capability boundary, not a passing substitute
for the missing scenarios. Subsequent scenario work must register reviewed
isolated implementations in this same manifest; do not merely allowlist the old
runners. `task-routing.mjs` was inspected and changes Swarm/Router settings,
adds/selects workspaces and can select an ambient first session;
`task-program-worktrees.mjs` changes Swarm/Coder/Designer settings and creates
workspace bindings. Neither is safe to overlap unchanged.

The structured-argv Python supervisor runs at most eight selected suites (default
four), each with a wall deadline at most 600 seconds, default 120-second no-output
stall deadline, heartbeat at most 15 seconds and hard 1 MiB log cap (maximum 4 MiB).
It tracks Linux process identities, owned descendant trees and process groups,
uses subreaper adoption, escalates TERM to KILL, and preserves successful siblings
on failure/cancellation. Atomic `results.json` and `summary.tsv` retain per-command
outcomes; unfinished queue entries become `not-run` on cancellation. These are
runner results, not automatic S01–S12 assertions. Logs are bounded private data,
not public artifacts. Fresh ignored evidence directories prevent overwriting a
previous wave. No provider runs exist in L1; later runners must persist their
owned run IDs and use canonical cancellation for remote runs before local exit.

### L1 executed validation (local runner diff, no application deployment)

- `PYTHONDONTWRITEBYTECODE=1 timeout 40s python3 tests/scripts/launch_prerun_supervisor_test.py -v`: eight tests pass twice on the final supervisor/test bytes (5.163s and 5.184s). Actual overlap, bounded occupancy, nonzero/spawn failure, TERM-resistant setsid descendant death, wall/stall limits, log cap, orphan rejection and shell cancellation propagation are asserted. Linux process supervision is not an adversarial sandbox: excessive descendants fail; uninterruptible cleanup reports `cleanup_incomplete` with remaining PIDs rather than waiting indefinitely or claiming cleanup.
- `timeout 40s node --test --test-concurrency=1 tests/scripts/testbench_attach_test.mjs`: four tests pass (final 0.757s), exercising fake loopback HTTP and the real shell entrypoint. Rejected legacy suites make zero extra HTTP requests; shared settings are read, never written. Redirects, drift, wrong runtime, overlarge response and request hang reject. Final child checks additionally pin the preflight settings digest.
- `timeout 30s bash tests/scripts/lib_launch_prerun_test.sh`: passes with a data-only temporary example configuration, not ambient `.env`. Existing source-string compatibility checks are not independent security evidence.
- Canonical `--attach-only` / `attach-inspect` executed twice against the unchanged agreed endpoint, with a 60-second wall and 30-second stall limit: one suite passed each time; authenticated topology/settings identity was preserved. No product/provider scenario is counted by that connection result. Exact private endpoint and evidence directories remain in the checkpoint handoff, not this public document.
- Initial lifecycle run failed four sibling-preservation assertions because exit/EOF ordering was misclassified; fixed production supervisor ordering. A later Node test detected import-time connection during URL validation; fixed argument/entrypoint separation before rerunning. Both failures remain history, not erased by final passing results.
- `bash -n` for all seven changed shell scripts, `bash scripts/check-atlas-sync.sh` and `git diff --check` pass. No broad Go/Node suite, provider trial, browser proof, critical-tier promotion, independent P1/P2 review, commit or push was performed in L1. The audit ledger records exact runner/test byte digests.

## L2 — Focused runner implementation (local, incomplete)

Source remains `6e345d0437a5b60c1d3eec65ff2c480514f131fe` plus the retained
uncommitted runner/test diff. No live product trial was executed in L2; the new
48-case wave remains **0 pass / 0 fail / 48 not-run**. Narrow local test results
below must not be promoted to full scenario passes.

The existing launch-prerun manifest now names `workspace-safety` and
`workspace-browser` as local attach-safe fixtures. `workspace-routing` is a
registered **disabled** implementation: both canonical admission and its direct
entrypoint fail before network mutation. Its fake-HTTP-tested implementation
creates uniquely named repositories through folder/setup/add APIs with
`make_current:false`, creates/replays isolated V3 sessions, reads explicit owned
repository selectors, uses existing Swarm assignments, and records owned run IDs
for canonical stop. It never selects a first workspace or changes shared models.
It is not live-safe yet: the process supervisor's 0.5-second TERM-to-KILL window
can preempt asynchronous remote cancellation. A bounded independent owned-run
cleanup phase, including lost message-response recovery, must precede admission.
Do not merely remove the guard or extend a timeout without proving cleanup.

### Executed local evidence and exact limitations

- `TestMultiWorkspaceIdentityTransitions` now stops two real B/C allocations at
  their actual mutation publisher, admits one CAS winner, then verifies the stale
  loser leaves the entire winning snapshot unchanged and removes its path/Git
  inventory. Existing nested/sibling/retained history and negative assertions
  remain. Initial same-target fixture failed before the barrier because both
  allocations chose one deterministic lane; corrected to distinct B/C targets,
  not by weakening CAS assertions. Goroutines abort/join before fixture teardown.
- `TestWorkspaceLaunchPostOpenSubstitution` deterministically substitutes a
  symlink or hard link after rooted authorization. Outside writes reject without
  truncation; symlink reads reject; outside and retained inside bytes are unchanged.
  This does not prove pre-OpenRoot substitution or hard-link read confidentiality.
- `TestWorkspaceLaunchAcknowledgedMutationProcessExit` uses an isolated subprocess
  that exits without closing Pebble after an acknowledged canonical mutation.
  Reopen compares the complete snapshot and explicit attachment identity. It does
  not inject failure inside commit/outbox, allocate real lanes, or prove orphan
  recovery. The shared candidate is never restarted.
- Exact final local command: `cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/tool -run '^(TestMultiWorkspaceIdentityTransitions|TestWorkspaceLaunchPostOpenSubstitution|TestWorkspaceLaunchAcknowledgedMutationProcessExit|TestWorkspaceTargetFilesystemAuthority|TestWorkspaceTargetSearchSelection)$' -count=2 -timeout=90s`.
  Passed: run 3.824s, tool 0.030s. Five top-level tests, not five product scenarios.
- `node --test --test-concurrency=1 tests/scripts/workspace_launch_test.mjs tests/scripts/testbench_attach_test.mjs`: eight tests passed in 1.048s. Actual fake HTTP
  checks exact owned identity/replay, unchanged selection/assignments, no requests
  on denied targets, fresh-deadline owned cancellation, and exclusion of prose or
  other-run events. These are runner proofs, not actual provider results.
- New `workspace-launch.browser.spec.ts` uses the real repository inventory/picker
  with a held old response, reconnect generation, explicit non-first default,
  failed retained worker and staged/unstaged/untracked/conflict counts. It uses
  normal clicks and optional private screenshots. Browser launch failed: pinned
  headless-shell and full Chromium executables are absent. A bounded maintained
  headless-shell installation reached download completion but timed out before a
  usable executable; the follow-up launch still failed. **No browser assertions
  or pixels were inspected.** Even once runnable, this is a component fixture,
  not the full application, attachment dialog or actual websocket transport.

| Cases | Remaining named boundary (no silent skip) |
|---|---|
| S01-a–d | Real HTTP/common-dir/allocation-count selected-root proof and forged-base no-allocation assertion; fixture baseline alone insufficient |
| S02-a–d | Local identity tests execute; canonicalizer is a fixture, not a complete live combination |
| S03-a–d | Provider attach/remove/default/restart with next-call scope and unchanged stale suffix |
| S04-a–d | Actual provider FFF/read/list marker outputs; privacy-redacted completion names are not bytes |
| S05-a–d | Exact edited bytes, post-switch Bash cwd/common-dir, old-lane and both captured-source immutability |
| S06-a–d | Complete relative selector/revocation timing; existing resolver tests are narrower |
| S07-a–d | Actual overlapping cross-repository workers and separate verified parent-lane integrations; legacy runner remains unsafe |
| S08-a–d | Existing-assignment staged provider trial with common immutable base, overlap and exact integrated dependency bytes |
| S09-a–b | Actual held worker; retained metadata is not worker execution |
| S09-c–d | B/C publisher CAS/rollback now locally proven; no full live race claim |
| S10-a–b,d | Mid-stage permission denial and child/reservation/dirty-sibling postconditions; full principal/stale selection proof |
| S10-c | Post-open rooted substitution locally proven; pre-root-open race remains untested |
| S11-a–d | Allocation-before-mutation and internal batch/outbox crash windows, real lane reopen and unfinished-only recovery; acknowledged-snapshot subprocess is narrower |
| S12-a–d | Full-app attachment/default, status/file navigation, actual reconnect and late transport responses plus pixel review; component fixture cannot substitute |

No new critical-tier promotion or independent P1/P2 review. Existing V6/L1
history and the three pre-existing documentation changes remain preserved.

## L2R — Installed-browser recovery and scoped runner safety

2026-09-07, same source HEAD plus retained local diff. Supersedes L2's browser
blocker and missing single-parent cleanup claims, not its historical results.
No live product/provider trial: **0 pass / 0 fail / 48 not-run**. No deployment,
tunnel change, shared setting mutation, commit or critical-tier promotion.

- Installed Chrome via `SWARM_TEST_BROWSER_CHANNEL=chrome` runs both registered
  browser fixtures; no browser download is required. Corrected an impossible
  detached-worker Default fixture after inspecting the actual repositories API:
  attached source B carries Default, while retained failed worker selection is
  inspection state. The attempted initial-auto-selection assertion failed and
  is retained as a fixture error, not a product bug or passing result. Pointer
  selection, dirty counts, delayed old response and reconnect generation pass.
  Reused the complete 64-attachment/dialog/default/removal/failed-refresh fixture.
  Three screenshots at 375/420/1440 widths were pixel-inspected: legible text,
  clear selected worker, no horizontal overflow; dialog clipping is inside its
  intentional scroll viewport. Not full-app/actual-WebSocket proof.
- `WorkspaceTrial` now implements provider-driven attachment/default switching,
  exact saved IDs, existing agent assignment, and controlled read-only Bash
  verification of cwd, actual common-dir, HEAD, branch, status and bounded marker
  bytes. Git snapshot comparison removes only observation timestamp/duration;
  substantive fields remain. Missing/redacted/rewritten proof output rejects.
  S05 cannot pass on completion names or assistant prose. Mutation-stage entry
  records fail until all exact postconditions pass; incomplete suites exit nonzero.
- Explicit message/run identity is recorded before POST. TERM aborts in-flight
  requests; a fresh cleanup client hydrates the exact lost-response run and stops
  only that identity without submitting another message. Absent admission stays
  visibly unresolved, never stops a foreign run. The supervisor accepts validated
  per-suite cleanup grace 0.5–25 seconds, preserving its output/deadline/fan-out
  limits and failure semantics. A composed real supervisor/Node/fake HTTP test
  delays stop acknowledgment beyond the former .5-second KILL boundary and proves
  exact cancellation plus successful sibling retention. This is not proof of
  provider shutdown, late admission after cleanup, or delegated-child cancellation.
- Real-Git identity fixture now holds child runtime scope at a barrier while
  rejecting parent retargeting, comparing entire child snapshot, scope, HEAD,
  status and worktree inventory. No provider is involved. New later-cohort denial
  fixture injects at `failUnlaunchedCohort`, preserving committed prior-job records,
  dirty bytes and session/Git inventories. It is not mid-tool permission UI or
  reservation release proof. Existing CAS/substitution/reopen cases remain.
- Regular cross-repository and same-repository two-stage Coder proof functions
  use the same owned client and assignments, check exact marker bytes/common-dir,
  distinct lanes, clean heads, separate integration destinations and captured-root
  immutability. Focused verifier negatives reject wrong roots, dirty heads, stale
  bases, cross-sibling leakage and missing dependencies. **Worker admission remains
  disabled** until exact task-permission handling and owned-child cancellation
  are integrated; the functions have not made real provider calls. Their Coder
  consumer is not the specified Finder/Designer handoff or measured overlap.

### L2R executed checks

- `timeout 40s node --test --test-concurrency=1 tests/scripts/workspace_launch_test.mjs tests/scripts/testbench_attach_test.mjs`: 16 pass, 0 fail/skip, 1.983s.
- `timeout 60s python3 tests/scripts/launch_prerun_supervisor_test.py`: 10 pass, 6.930s; final rerun recorded in private handoff.
- `cd swarmd && GOMAXPROCS=2 go test -p 2 ./internal/run ./internal/tool -run '^(TestMultiWorkspaceIdentityTransitions|TestWorkspaceLaunchLaterCohortDenied|TestWorkspaceLaunchPostOpenSubstitution|TestWorkspaceLaunchAcknowledgedMutationProcessExit|TestWorkspaceTargetFilesystemAuthority|TestWorkspaceTargetSearchSelection)$' -count=2 -timeout=90s`: run 5.640s, tool 0.018s, all selected tests pass twice.
- From `web/`, `SWARM_TEST_BROWSER_CHANNEL=chrome node --import tsx --test --test-concurrency=1 src/features/desktop/git/workspace-launch.browser.spec.ts src/features/desktop/chat/components/session-attachments.browser.spec.ts`: 2 pass, 0 fail/skip, 5.943s; private screenshot directory supplied. Vite/Node deprecation warnings do not change results.
- `timeout 35s bash tests/scripts/lib_launch_prerun_test.sh`: PASS.

### Exact remaining scenario limitations

The L2 table remains authoritative except these narrower upgrades: S03-a now has
an executable provider-driven attachment/default stage (not run); S05-a–d have
controlled exact filesystem postconditions (not run); S09-a–b gain held runtime-scope
proof, not actual provider execution; S10-a/b gain later-cohort injected-denial
persistence proof, not full permission/reservation lifecycle; S12 component/browser
proofs now execute and pixels are reviewed, still not full app. S01 allocation-count
and account-default-A, S03 removal/restart suffix, S04 actual FFF marker output,
S06 relative/revocation timing, S07/S08 admitted provider overlap and complete
integration lineage, S11 internal allocation/commit/outbox crash windows and
unfinished-only provider recovery remain explicit missing boundaries. No local
fixture result upgrades any of the 48 live product cases.

Before a live routing stage, independently reverify the existing candidate and
choose an explicit reviewed disposable fixture parent; the runner never chooses
an ambient first workspace. It stops at pending permissions rather than blanket
approving. Do not remove worker admission guards merely to obtain a green launch
wave. Freeze exact runner hashes and retained diff before execution. Public ledger
records reinventory only, not independent P1/P2 sign-off.

### L2R continuation: exact-call permissions and endpoint unavailable

Routing now preflights every pending permission against the exact owned session,
run, tool and full argument object before resolving any entry, uses `allow_once`
only and rejects saved-policy/argument overrides. Read/write/proof prompts supply
those exact arguments. Negative batch tests prove a foreign run, changed command
or task request produces zero resolution calls. Seventeen Node tests pass
(2.245s), shell regression and Desktop typecheck pass. Worker task/child lifecycle
admission remains disabled; exact permission support for routing is not blanket
approval for workers. The canonical browser suite now explicitly defaults to the
already installed Chrome channel, with the existing environment override retained.

A fresh bounded read-only preflight of the agreed endpoint failed with connection
refused. Independent maintained broker inspection reported the previously verified
candidate slot inactive at the same source SHA. No provider trial, rebuild, start,
tunnel replacement or alternate endpoint was attempted. This is an actual external
live-testing blocker, separate from the remaining local implementation gaps. All
48 full live cases remain not-run. Resume only against the same explicitly agreed
endpoint once reachable; do not reinterpret this failure as permission to deploy.

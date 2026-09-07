# Multi-workspace session identity matrix

## Scope and evidence discipline

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
Production fixes remain deferred to the subsequent implementation checkpoints.

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

## Causal map and inspected assertion inventory

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

## Expanded acceptance matrix

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

## Supported live acceptance topology (specified, not executed)

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

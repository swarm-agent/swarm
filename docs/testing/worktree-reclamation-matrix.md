# Worktree reclamation: requirement-first matrix

## Status and scope

Design/inspection baseline: `9e0213a4b0c342cd9ec9a72b1c1da9ee09390232`.
This document defines acceptance requirements, not implemented behavior or passing-test evidence. No recovery fixtures or provider/browser journeys were executed for this baseline. Existing test assertions were inspected; new test selectors below are proposed and must exist before their commands count as evidence.

## Minimal safety policy

1. Discovery is read-only and bounded to an authenticated saved repository. Join Git's registered-worktree inventory with durable current, historical, deleted/tombstoned and Task Program ownership. A directory name, absent live session, archive flag, branch prefix or filesystem accessibility is not ownership proof. Return exact candidate identity, source identity/generation, repository common-dir identity, HEAD, owner classification, dirty-state fingerprint, allowed operations and rejection reasons. Incomplete inventory fails closed.
2. Same-session reselection may resume an exact owned lane, including its dirty bytes, only with exclusive writer admission and validated repository identity. Preserve the outgoing lane and its provenance. Never commit, stash, reset or clean it implicitly.
3. In-place reclaim is limited to an authenticated, durably attributable ownerless/deleted-owner lane with no live writer, retained conflicting claim or active program. Require an atomic exclusive ownership claim, exact candidate freshness, explicit authorization and recoverable publication. Missing ownership evidence is **unknown**, not permission to seize a lane. A merely archived owner retains ownership.
4. For an external registered worktree or a still-owned same-account lane, use an explicit isolated copy/import of selected committed or working changes; never transfer the source. Unknown/foreign-account authority is rejected. If a stable source snapshot cannot be obtained (including concurrent writer changes), reject before publication. No automatic downgrade from requested in-place reclaim to copy.
5. Copy into a new session-owned managed lane using exact source/base/selection evidence. Preserve staged and unstaged layers separately, untracked file bytes, binary content and executable modes. Conflicts, unsupported file kinds, symlink escapes, submodule ambiguity and exceeded byte/file limits must be explicit failures with no partial session switch. Do not follow symlinks while copying. Ignored files are not implicitly imported.
6. Publish ownership, grants, runtime identity, immutable recovery provenance and V3 events consistently. Continue the same session with freshly resolved tool scope. Scope changes are not proof of provider continuation. Old lanes remain inspectable but are not implicitly writable through historical inventory.
7. No cleanup of source lanes, branches, commits or dirty data. Rollback may remove only newly created resources owned by the failed operation, after verifying they have not acquired foreign/new writes. Failed rollback must retain a recoverable operation record and surface the error.

## Inspected production boundaries and gaps

- `swarmd/internal/run/service_workspace_manage.go`: `adoptSessionWorktree` supports new same-source allocation or `resolveOwnedSessionWorktree`. New successors preserve the old lane but start from configured saved-source committed base, not the current lane's commits/dirty files. Reselection requires matching same-session history and clean target; leaving a different dirty lane is rejected. `rejectSessionWorktreeOwnershipConflict` scans account/user sessions (bounded at 10,000); this is not an atomic cross-owner reclamation authority. No selected patch/commit import contract exists in these arguments.
- `swarmd/internal/run/service_workspace_context.go`: `ResolveRuntimeWorkspaceScope` revalidates source catalog identity and repository identity, then merges authorized attachments. Successful adoption returns `restart_turn`; implementation/live tests must prove the next provider step actually uses the new root rather than merely observing that flag.
- `swarmd/internal/store/pebble/session_workspace_projection.go`: typed grants preserve additional workspaces and derive path-free usage. History is not a replacement for current access grants.
- `swarmd/internal/session/task_program_workspace.go`: `EnsureWorkspaceTransitionIdle` rejects declared/running repository jobs without reconciling their state; managed-only Designer jobs are exempt. Persisted terminal program destinations must not be rebound by recovery.
- `swarmd/internal/store/pebble/session_event_store.go`: adoption rechecks program admission at the canonical mutation boundary. Keep that guard and session CAS; add durable exclusive recovery claims rather than relying on a service preflight scan. Git changes plus Pebble publication require a recoverable operation protocol, not a claim of filesystem/database atomicity.
- `swarmd/internal/api/sessions_v3_repositories.go`: inventory rows preserve session/workspace/path identities, lifecycle and retained/program provenance; authorization checks saved identity and source access. Existing session history inventory is not general orphan Git discovery. Extend one canonical discovery authority, not an unrelated API side effect.
- `web/src/features/desktop/git/session-repository-picker.tsx` and `state/session-repositories.ts`: exact row selection is inspection, not adoption. Preserve stable keys, missing-selection warnings and stale mutation guards. `runtime/use-session-repositories.ts` uses scoped V3 invalidations and visibility/initial hydration; do not introduce polling.
- Inspected existing assertions: `TestManageWorkspaceAdoptWorktreeKeepsSameSessionAndRefreshesScope` asserts session identity, persisted metadata and resolved primary root using a real Git fixture plus allocator stub. `TestWorkspaceSuccessorActiveProgramGuard` asserts repeated rejection and unchanged parent; `TestWorkspaceSuccessorAllocationCollisionBound` asserts one distinct name retry and no retry for ordinary allocation failure. These do not prove orphan recovery, binary preservation, cross-owner concurrency or live restart.

Implementation clusters for the next checkpoint: (A) authenticated inventory/freshness and durable exclusive recovery authority; (B) isolated Git snapshot/import plus same-session tool permission/continuation integration; (C) exact Desktop consumption and requirement-first regression tests. Integrate prerequisites before dependent consumers; parent owns command execution and cross-system validation. Update atlas API/security rows and test audit inventory alongside implementation.

## Fixture and evidence protocol

Use hermetic temporary Git repositories and Pebble stores, fixed test principals A/B, synthetic owners, deterministic commits and bounded fake publishers. Disable ambient Git config/hooks; do not use the developer checkout as a fixture. Each fixture has a saved source S, requesting session R, candidate lane L, optional owner O, additional repository X, and a captured base plus two distinguishable commits C1/C2.

Snapshot P before and after every row: source/candidate HEAD and refs; index entries/modes/blobs; separate cached and uncached binary diffs; NUL-safe status; untracked relative names/modes/content hashes; session/grants/usage/history; ownership claims; program destinations; mutation sequence/outbox; Git registered-worktree list. Reads must avoid optional index writes. Failure assertions compare P and reject unauthorized/partial state, not only an error code. Successful copy leaves source P identical; successful reclaim changes only authorized ownership/session metadata, never lane bytes. Retain old-lane provenance. Test race injection at discovery, snapshot, allocation, import and publication.

All rows inherit P, the no-source-cleanup rule, and bounded fixtures. Commands below are validation entrypoints, not permission to mutate real data. Planned subtest names are an executable naming contract for implementation; Go's zero-selected-test success is not evidence. Verify each named subtest exists and appears in verbose output. New tests need purpose comments and audit-ledger reinventory; do not add them to curated gates without independent review.

| ID / proposed subtest | Fixture and request | Required observable transition / negative control | Command |
|---|---|---|---|
| R01 `new` | R at S; allocate named lane, including name collision | Same R, new exclusive lane at configured committed base; preserve S; only one distinct collision retry | G(R01) |
| R02 `same_owner` | L in R history, clean then dirty; reselect L | Same R and exact L; preserve index/worktree layers and outgoing history; no duplicate ownership | G(R02) |
| R03 `external` | Git-registered L with no managed provenance, within authorized S | Explicit isolated copy succeeds with exact provenance; in-place claim rejected; arbitrary unregistered directory rejected | G(R03) |
| R04 `missing_owner` | L retains authenticated ownership evidence but live O absent; second fixture has no evidence | Reclaim only proven ownerless exclusive L; unknown evidence rejects without granting access | G(R04) |
| R05 `deleted_owner` | Canonically delete O retaining tombstone/history; L dirty | Reclaim after no writer/claim check; retain historical attribution; source bytes unchanged | G(R05) |
| R06 `archived_owner` | Archive same-account O, retain L | No in-place transfer; explicit isolated copy or clear rejection; archive is not deletion | G(R06) |
| R07 `active_owner` | Active O writes L, then source changes during snapshot | Never seize L; copy only stable authorized snapshot; race rejects, O continues unchanged | G(R07) |
| R08 `staged` | Stage content A in tracked file, working copy equal A | Copied index holds A; not silently committed or unstaged | G(R08) |
| R09 `unstaged` | Index baseline, working copy B; second case staged A plus working B | Destination independently matches cached and uncached layers; flattening fails assertion | G(R09) |
| R10 `untracked` | Nested untracked files, spaces/newlines in names, empty file | Exact bytes/names/modes copied under bounds; ignored files excluded; no source removal | G(R10) |
| R11 `binary_modes` | Binary NUL bytes staged then independently edited; executable rename/delete | Exact index and worktree blobs/modes, renames/deletions preserved; unsupported special files reject | G(R11) |
| R12 `selected_commits` | C1/C2 plus unrelated edits; choose exact C1 | Only selected effects imported; dependencies/conflicts reject clearly; source refs unchanged; explicit commit authorization | G(R12) |
| R13 `selected_patch` | Multiple file changes, select bounded exact patch/hunks | Only selected changes imported; stale hash/base and traversal patch reject; no hidden whole-tree copy | G(R13) |
| R14 `repeat` | Repeat exact operation token, then new request against adopted lane | Replay yields same result/no duplicate allocation or event; new request validates current identity | G(R14) |
| R15 `attachments` | R has S plus X; adopt L belonging to S | X remains authorized at same generation; revoked/stale X not silently granted; global default unchanged | G(R15) |
| R16 `program_targets` | Retained terminal program lane plus declared/running repository job control | Terminal target remains exact; active repository program rejects at admission and publication; no reconciliation side effect | G(R16) |
| R17 `sidebar_exact` | Same branch labels across multiple rows, retained and active lanes | Select exact identity; only that row's Git data/actions; disappearance remains explicit, never defaults silently | U + browser |
| R18 `restart_reconnect` | Successful recovery, next provider read/write, daemon restart and Desktop reconnect | Same session; fresh root on actual next tool call; durable history and selected inventory repair; no polling | G(R18) + live |
| R19 `stale_concurrent` | Stale workspace generation/HEAD/index/path; two R sessions race for L | Stale rejects, exactly one exclusive claimant wins; loser no grants/resources; cross-owner race not just same-session CAS | G(R19) |
| R20 `cross_account` | B owns L, A guesses exact path/reference/cursor | No bytes/owner metadata leakage or grant; reject even if OS path readable and Git common dir matches | G(R20) |
| R21 `traversal_symlink` | Dot-dot path, symlink lane swap, untracked link escape, altered Git admin/common-dir | Revalidate resolved identity and containment; reject escape/race without outside reads/writes | G(R21) |
| R22 `rollback` | Inject allocation/import/CAS/store failures and rollback failure; simulate process restart | No published partial switch; cleanup only operation-owned resources; retained diagnostic/recovery record if cleanup fails; retry never duplicates | G(R22) |

### Command definitions and staged live procedure

Proposed backend matrix test: `swarmd/internal/run/service_workspace_reclamation_test.go`, `TestWorktreeReclamation`, with row IDs as subtests. G(Rnn):

```bash
(cd swarmd && GOMAXPROCS=2 go test ./internal/run -run '^TestWorktreeReclamation/Rnn$' -count=1 -parallel=1 -timeout=90s -v)
```

Replace Rnn with the exact row ID. Split store-level concurrency/restart and Git-copy tests into their owning packages when the implementation authority is finalized; retain row IDs and record exact executed selectors here. No full-module sweep.

Existing baseline controls (not run in this checkpoint):

```bash
(cd swarmd && GOMAXPROCS=2 go test ./internal/run -run '^(TestManageWorkspaceAdoptWorktreeKeepsSameSessionAndRefreshesScope|TestWorkspaceSuccessorActiveProgramGuard|TestWorkspaceSuccessorAllocationCollisionBound)$' -count=1 -parallel=1 -timeout=90s)
```

U (existing focused frontend files; add R17 assertions before claiming recovery evidence):

```bash
(cd web && npx --no-install vitest run src/features/desktop/state/session-repositories.spec.ts src/features/desktop/git/session-repository-picker.spec.ts --maxWorkers=1)
```

Live preparation: use `node scripts/testbench-attach.mjs "$SWARM_DESKTOP_URL"` with the explicitly requested Desktop root, then `bash scripts/testbench-container-deploy.sh pool-status` using valid data-only config. Independently match broker slot, lane and full candidate HEAD before stateful tests. The current deploy client auto-assigns an inactive slot and exposes no exact-slot flag; do not claim it guarantees slot 2. A missing/stale lane requires exact-slot broker/client support or a proven existing slot-2 lane, never silent slot-1 deployment. Do not stop occupied tunnels or another lane.

Once attached and candidate identity is proven, run one disposable same-account fixture journey per stage: new/reselect; orphan recovery; selected dirty/commit copy; rejection/races; provider continuation/restart; manual sidebar selection/reconnect. Use the maintained registered runner after implementation, with explicit endpoint, fixture ownership and row selectors; no runner for this complete recovery matrix is asserted to exist today. Each stage must be <=10 minutes, emit heartbeat every <=15 seconds, use one worker for shared fixtures and bounded private ignored evidence. Record actual tool arguments/results and source P comparisons. Inspect each captured UI image for clipping, exact row selection, legibility and unexpected overlays. Stop on source drift or ownership mismatch. Never use production/session data as fixtures; cleanup only positively identified disposable test-owned resources with explicit approval.

## Endpoint inspection evidence (2026-09-10)

- Maintained attach-only client against the requested slot-2 loopback endpoint: failed with `attach-only: fetch failed`.
- Bounded direct connectivity diagnostic: connection refused, HTTP status `000`. This is an endpoint accessibility failure, not evidence of a particular deployed candidate.
- Maintained `pool-status`: active execution worktree has no `.env`; SSH alias validation fails before broker access.
- Supported `SWARM_TESTBENCH_ENV_FILE` override using the source checkout's existing configuration: rejected because configured remote ports violate fixed Desktop `5655` / API `7881` configuration defaults. Slot-2 ports are derived by the client, not configured as defaults.
- Candidate HEAD and slot ownership remain **unverified**. No tunnel, deployment, credential setting or source config was changed. No alternative endpoint was selected.
- External resolution: provide a valid non-secret testbench config through the supported override (configured SSH alias and fixed defaults), restore the authorized slot-2 forward to the requested endpoint, and return broker slot/lane/full-HEAD evidence. A port responding alone is insufficient. Re-run bounded attach plus pool inspection before any live mutation.

This inspection checkpoint can complete because its endpoint criterion permits an evidenced actionable external blocker. Live proof remains blocked until that dependency is resolved; implementation work is not represented as live-tested.

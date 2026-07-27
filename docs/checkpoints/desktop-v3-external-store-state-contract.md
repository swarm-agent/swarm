# Desktop V3 external store migration plan

Status: interim recovery plan. This supersedes the TanStack DB Desktop V3 state rail while Swarm is being revived.

## Final stack

Desktop V3 core state must end on this stack and nothing more:

```txt
React
TypeScript
existing WebSocket transport
one in-memory external store
useSyncExternalStore
one reducer
```

The daemon/backend is the only source of truth. The browser renders one in-memory projection of daemon state.

## Core decision

Remove TanStack DB from Desktop V3 core state. Do not replace it with another client database, sync engine, cache authority, optimistic mutation layer, browser DB, or multi-store orchestration layer.

The target rule is boring and strict:

```txt
Snapshot replaces the store.
WebSocket events patch the store.
Commands do not mutate canonical state.
A reducer is the only place canonical Desktop state changes in the browser.
```

## Problem statement

The current failure mode is split ownership:

```txt
Two or more frontend layers are allowed to believe they own Swarm state.
```

That creates waterfalls, stale reads, hidden fallback paths, optimistic state drift, and unclear authority between snapshot APIs, realtime events, TanStack DB, React Query, route hydration, and component state.

The fix is not another data system. The fix is one writer, one stream, one reducer, one store.

## Superseded rail

`docs/checkpoints/desktop-v3-tanstack-db-state-contract.md` is no longer the desired direction.

Current known TanStack DB footholds to remove:

- `web/package.json` depends on `@tanstack/db` and `@tanstack/react-db`.
- `web/src/features/desktop/state/desktop-db.ts` is the TanStack DB authority module and uses collections/live queries.
- `web/src/features/desktop/state/desktop-db-architecture.spec.ts` encodes the old TanStack DB migration rail.
- `web/src/features/desktop/state/desktop-db-workset.ts` normalizes workset data into DB collections.
- `web/src/features/desktop/state/desktop-db-live-stream.spec.ts` tests DB event merging.
- `web/src/features/desktop/state/desktop-ui-store.ts` already contains a `useSyncExternalStore` shape and WebSocket lifecycle plumbing, but it is tangled with DB writes, React Query, durable patch merges, and local canonical mutations.
- `web/src/features/desktop/layout/desktop-app-page.tsx` imports route/session hooks from `desktop-db.ts`.
- `web/src/features/desktop/chat/queries/chat-queries.ts` imports `mergeDesktopDBDurablePatch`.
- `web/src/features/desktop/realtime/client.ts` already opens the Desktop WebSocket.

## Non-negotiable invariants

These invariants are the rails. If an implementation violates any of them, the implementation is wrong even if the UI appears to work.

1. **Daemon source of truth**: Durable Swarm state belongs to the daemon/backend only.
2. **Single frontend projection**: The browser has exactly one in-memory projection store for daemon-derived Desktop state.
3. **One reducer**: Snapshot replacement and WebSocket patches enter through one reducer boundary.
4. **No local optimistic daemon state**: Command calls may expose pending UI affordances, but they must not locally mutate canonical session/message/permission/plan/workspace/run/notification state.
5. **No TanStack DB for Desktop core state**: No `@tanstack/db`, no `@tanstack/react-db`, no `useLiveQuery`, no collections, no local collections, no sync-engine semantics.
6. **No React Query as core-state authority**: React Query may remain for unrelated request lifecycle if already needed, but Desktop core selectors must not read Query cache as canonical state.
7. **No hidden fallback paths**: Missing state must fail clearly or trigger an explicit snapshot/resync path. Do not silently swap to another authority.
8. **Ephemeral UI state is separate**: View-only state such as modal open/closed, input drafts, hover, focus, collapsed sidebar, resize state, pending button spinners, and transient form state may stay local/component/preference-backed if it is not daemon-derived.
9. **Revision guard is mandatory**: Patches without valid revision continuity must not be applied.
10. **Bounded queues only**: Browser-side realtime buffering must be bounded. Overflow means resync, not memory growth.
11. **No compatibility fork**: Delete the old DB authority path. Do not keep DB and external-store implementations alive side-by-side once a slice is migrated.
12. **Tests encode the architecture**: The repo must contain tests that fail if TanStack DB or another canonical state owner comes back.
13. **Fast cache is backend-owned**: A fast server/workset cache is allowed and desired, but it must sit behind the snapshot contract. It must not become a second frontend owner.
14. **Tabs are store consumers, not state owners**: UI tabs/routes and browser tabs may read the projection, but they must not create independent canonical state systems.

## Target architecture

```txt
GET snapshot endpoint
  -> normalize once at the store boundary
  -> replace the one frontend store

WebSocket daemon events
  -> verify rev / prevRev continuity
  -> apply to the same store through the reducer

POST command endpoint
  -> sends command to daemon
  -> does not locally mutate canonical frontend state
  -> daemon persists
  -> daemon emits WebSocket event
  -> frontend reducer applies it
```

Important wording:

```txt
Snapshot is not a second frontend cache authority.
WebSocket is not a second data source.
Commands are not local mutations.

They are all part of one daemon-state projection.
```

If the existing bootstrap endpoint remains `/v3/sessions:workset`, treat it as snapshot input only. It is not a DB sync feed and not a second authority.

## Fast API, cache, and tabs contract

This plan does **not** mean "no cache". It means "no second owner of canonical state".

Allowed cache layers:

1. **Backend/daemon cache**: allowed and preferred. The existing fast workset/snapshot API can stay mega-fast and can serve precomputed normalized Desktop state. That cache is authoritative only because it is inside the daemon/backend source-of-truth boundary.
2. **Frontend in-memory projection**: required. This is the external store. It is the current tab's read model and enables instant local route/session/tab switches for records already loaded.
3. **Derived selector memoization**: allowed. Sorting/filtering/index helpers are okay if they are derived from the one store and do not accept writes.
4. **Ephemeral UI cache**: allowed for view-only state such as selected settings tab, draft input, expanded panels, pending button spinners, hover/focus, and layout preferences.
5. **Optional stale boot cache**: allowed only as a display optimization if explicitly marked non-authoritative. It may render last-known data while the real snapshot loads, but it must not accept WebSocket patches or command results until replaced by a fresh snapshot with a valid `rev`.

Forbidden cache layers:

1. TanStack DB / React DB as Desktop core state.
2. React Query cache as Desktop core state.
3. IndexedDB/LocalStorage as canonical daemon state.
4. Optimistic local daemon-state mutation caches.
5. Cross-tab browser sync engines that try to own daemon state.

The fast API should be treated as the snapshot source:

```txt
fast workset/snapshot API -> replace in-memory projection -> tabs/routes read locally
```

That preserves sub-second loading while avoiding a frontend sync engine.

### UI tabs and route tabs

UI tabs are straightforward:

- Selected tab/route is ephemeral or URL state.
- Tab contents read canonical data from `useDesktopState` selectors.
- Switching tabs/routes does not fetch per-session snapshots if the data is already in the loaded projection.
- If a tab needs data outside the loaded projection, it requests an explicit snapshot/workset page/chunk and then replaces/extends through the reducer according to the snapshot contract. It must not silently hydrate through another authority.

### Multiple browser tabs/windows

Multiple browser tabs are also safe if each tab is treated as an independent projection client:

- Each browser tab may load the fast snapshot and open/resume its own WebSocket.
- The daemon serializes real state changes and emits ordered revisions.
- If one tab sends a command, other tabs learn about it through daemon events or snapshot reload.
- On focus/visibility restore, a tab should resume from its current `rev` or reload snapshot if stale.
- Do not add BroadcastChannel/SharedWorker/shared IndexedDB as a canonical state layer unless there is a separate explicit design. If used later, it may only send invalidation/resync hints, not canonical records.

## Revision and stream contract

Every daemon state message must carry ordered revision metadata:

```ts
type DesktopDaemonEvent = {
  rev: number
  prevRev: number
  type: string
  payload: unknown
}
```

Snapshot payloads must carry the store revision they represent:

```ts
type DesktopDaemonSnapshot = {
  rev: number
  // normalized daemon-derived Desktop records
}
```

Reducer rule:

```txt
if event.prevRev !== state.rev:
  stop applying patches
  mark store stale
  request snapshot reload
  replace store from snapshot
else:
  apply event
  state.rev = event.rev
```

Duplicate/old message rule:

```txt
if event.rev <= state.rev:
  ignore only if already applied and harmless
  otherwise resync
```

Missing metadata rule:

```txt
if rev or prevRev is missing/non-finite:
  do not apply event
  resync
```

Slow-consumer/backpressure rule:

```txt
if the WebSocket reports slow_consumer, cursor_error, reconnect_required, or queue overflow:
  stop applying patches
  reload snapshot
```

This prevents silent corruption when the browser misses, reorders, duplicates, or cannot keep up with daemon messages.

## Snapshot/stream boot sequence

Do not improvise this sequence. The point is to avoid the classic gap between initial load and live events.

1. Create the external store in an empty loading state with `rev = 0` and `stale = true`.
2. Fetch the snapshot endpoint.
3. Normalize the wire snapshot at the store boundary.
4. Replace the entire store with the normalized snapshot and set `state.rev = snapshot.rev`.
5. Open the existing Desktop WebSocket.
6. Subscribe/resume from `afterRev = snapshot.rev`.
7. If the server can replay from `afterRev`, apply replayed events in order through the reducer.
8. If the server cannot replay from `afterRev`, it must send an explicit resync/reconnect-required message; the frontend must reload the snapshot.
9. After replay catches up, continue applying live events through the same reducer path.
10. Keep one active socket lifecycle owner for Desktop. Route changes may change selected IDs, but they must not create independent state owners.

If the current backend stream uses `global_seq`, `last_seq`, or per-session `seq`, the migration must explicitly map or upgrade the backend contract to `rev / prevRev`. Do not silently guess in the UI. Prefer adding `rev / prevRev` to backend envelopes and snapshot responses, then make tests enforce it.

## Store contract

Create one canonical Desktop state module under `web/src/features/desktop/state/`.

Recommended names:

- `desktop-state.ts` for types and reducer
- `desktop-state-store.ts` for external-store mechanics and hooks
- `desktop-state-snapshot.ts` for snapshot fetch/normalization
- `desktop-state-stream.ts` for WebSocket lifecycle wiring

Required API surface:

```ts
getDesktopSnapshot(): DesktopState
subscribeDesktop(listener: () => void): () => void
replaceDesktopFromSnapshot(snapshot: DesktopDaemonSnapshot): void
applyDesktopDaemonEvent(event: DesktopDaemonEvent): void
markDesktopStale(reason: string): void
useDesktopState<T>(selector: (state: DesktopState) => T): T
```

`useDesktopState` must be implemented with `useSyncExternalStore`.

The only legal canonical mutation paths are:

```txt
replaceDesktopFromSnapshot -> reducer({ type: 'snapshot/replace', snapshot })
applyDesktopDaemonEvent    -> reducer({ type: 'daemon/event', event })
markDesktopStale           -> reducer({ type: 'connection/stale', reason })
```

Everything else is a read, command call, or ephemeral UI update.

## State shape guidance

Keep the shape explicit and normalized enough to be boring. Do not build a query engine.

```ts
type DesktopState = {
  rev: number
  status: 'idle' | 'loading' | 'ready' | 'stale' | 'error'
  staleReason: string | null
  sessionsById: Record<string, DesktopSessionRecord>
  sessionOrder: string[]
  messagesBySessionId: Record<string, ChatMessageRecord[]>
  permissionsById: Record<string, DesktopPermissionRecord>
  plansBySessionId: Record<string, DesktopSessionPlanRecord | null>
  planRevisionsBySessionId: Record<string, DesktopSessionPlanRevisionRecord[]>
  usageBySessionId: Record<string, DesktopSessionUsageRecord>
  runIntentsBySessionId: Record<string, DesktopRunIntentRecord>
  workspacesByPath: Record<string, DesktopWorkspaceRecord>
  notificationsById: Record<string, DesktopNotificationCenterRecord>
  notificationSummary: DesktopNotificationSummary
}
```

Selectors can sort/filter arrays for view needs, but they must read this store only. If a selector becomes expensive, add a plain derived index inside the reducer/store. Do not introduce a database or live-query layer.

## Command contract

Command functions must follow this rule:

```txt
POST command -> await daemon response -> no local canonical mutation
```

Examples:

- Sending a message does not append a local canonical message.
- Approving a permission does not locally delete/update the canonical permission.
- Starting/stopping a run does not locally rewrite canonical run/session state.
- Creating/switching sessions does not invent canonical session records locally.
- Updating a plan does not locally replace canonical plan state.
- Clearing a notification does not locally delete canonical notification state.

If immediate feedback is needed, model it as explicit ephemeral UI state, for example:

```txt
pendingCommandIds
pendingInputText
isSubmitting
lastCommandError
```

Those fields must not be used as canonical daemon state.

## Required architecture tests / rails

These tests should be created or rewritten before the bulk migration so the next AI cannot drift.

### Rail 1: forbidden dependencies

Rewrite `web/src/features/desktop/state/desktop-db-architecture.spec.ts` or replace it with `desktop-state-architecture.spec.ts`.

It must fail if:

- `web/package.json` contains `@tanstack/db`.
- `web/package.json` contains `@tanstack/react-db`.
- any file under `web/src/features/desktop` imports `@tanstack/db`.
- any file under `web/src/features/desktop` imports `@tanstack/react-db`.
- any file under `web/src/features/desktop` contains `useLiveQuery`.
- any file under `web/src/features/desktop` contains `createCollection`, `localOnlyCollectionOptions`, `BasicIndex`, or `Collection<` from TanStack DB.

Allowed exceptions: none after the migration is complete. During the migration, exceptions must be explicit and removed by the checkpoint that deletes `desktop-db.ts`.

### Rail 2: one canonical store

Test that:

- `desktop-state-store.ts` exists.
- it imports `useSyncExternalStore` from `react`.
- it exports `useDesktopState` or the agreed hook name.
- it exposes `getSnapshot`/`subscribe` style mechanics.
- no other Desktop production state file creates another canonical external store for daemon-derived records.

### Rail 3: one reducer boundary

Test that:

- a reducer file exists.
- snapshot replacement goes through the reducer.
- daemon event application goes through the reducer.
- canonical state writes are not scattered through component files.

A static rail can search production Desktop files for direct calls to old mutation helpers such as:

```txt
mergeDesktopDBDurablePatch
applyDurableEventToDesktopDB
applyOptimisticRunStartToDesktopDB
applyRunIntentToDesktopDB
desktopPlansCollection
ensureDesktopDBRouteSession
```

The expected offender list must be empty at the end.

### Rail 4: revision guard behavior

Reducer unit tests must cover:

1. snapshot replacement sets `state.rev` and replaces old records.
2. event with `prevRev === state.rev` applies and advances `state.rev`.
3. event with `prevRev !== state.rev` does not apply payload and marks stale/resync-needed.
4. duplicate old event does not corrupt state.
5. missing/non-finite `rev` or `prevRev` does not apply payload.
6. resync snapshot after stale replaces state and clears stale flag.

### Rail 5: stream lifecycle

Stream tests must cover:

1. boot fetches snapshot before applying live patches.
2. socket subscribes/resumes from the snapshot revision.
3. replayed events are applied in revision order.
4. slow-consumer/reconnect-required/cursor-error messages trigger snapshot reload.
5. queue overflow triggers snapshot reload.
6. socket reconnect does not create a second canonical store or second active socket owner.

### Rail 6: commands are not local canonical mutations

Tests or static checks must prove command modules do not locally mutate canonical store records for:

- send message
- stop run
- approve/deny permission
- create session
- select/switch session
- plan save/approval updates
- notification clear/update

The command may set ephemeral pending UI state, but canonical records must change only from snapshot or daemon event.

### Rail 7: final stack proof

Add a final architecture test that asserts the final stack in plain code terms:

```txt
has React dependency
has TypeScript source
has WebSocket client path
has useSyncExternalStore store
has reducer tests
has no @tanstack/db
has no @tanstack/react-db
has no useLiveQuery
has no Desktop DB collections
```

## Step-by-step migration plan

Follow these checkpoints in order. Do not skip ahead. Each checkpoint should leave the tree in a comprehensible state with tests documenting the next constraint.

### Checkpoint 0: inventory and freeze

Goal: stop the old direction from expanding.

Steps:

1. Keep `docs/checkpoints/desktop-v3-tanstack-db-state-contract.md` marked as superseded.
2. Add a short note at the top of any TanStack DB state test that it is being replaced, or rewrite it immediately into the new rail.
3. Inventory every production import of `desktop-db.ts` and every TanStack DB dependency.
4. Write down the exact temporary offender list in the new architecture spec so the list shrinks checkpoint by checkpoint.
5. Do not add any new feature work on top of `desktop-db.ts`.

Exit criteria:

- The plan doc is present.
- The old rail is marked superseded.
- The new architecture rail exists and fails for the current known TanStack DB footholds, or has an explicit temporary offender list that must shrink.

### Checkpoint 1: rewrite architecture rails first

Goal: make the repository tell future agents what must be true.

Steps:

1. Replace the old `desktop-db-architecture.spec.ts` assertions that require TanStack DB.
2. Add forbidden dependency/import tests for `@tanstack/db`, `@tanstack/react-db`, `useLiveQuery`, and collection APIs.
3. Add tests requiring the external-store module and reducer module.
4. Add tests requiring revision-guard behavior.
5. Add a final-stack test.

Exit criteria:

- Tests no longer claim TanStack DB is the desired authority.
- The new rails fail only because the migration is not done yet, not because the rails describe the wrong architecture.

### Checkpoint 2: define store types and reducer

Goal: create the destination without wiring it everywhere yet.

Steps:

1. Add `desktop-state.ts` with `DesktopState`, `DesktopDaemonSnapshot`, `DesktopDaemonEvent`, action types, and the reducer.
2. Include `rev`, `status`, `staleReason`, and the normalized record maps in state.
3. Implement `createEmptyDesktopState()`.
4. Implement `desktopReducer()` with:
   - snapshot replace
   - daemon event apply
   - stale/resync-needed
   - connection status updates
5. Add reducer unit tests for all revision guard cases.

Exit criteria:

- Reducer tests pass in isolation.
- No UI reads the new store yet.
- No command writes canonical state except through planned reducer APIs.

### Checkpoint 3: create external store wrapper

Goal: create the one browser projection store.

Steps:

1. Add `desktop-state-store.ts`.
2. Implement `getDesktopSnapshot`, `subscribeDesktop`, internal `setDesktopState`, and `dispatchDesktopAction`.
3. Implement `useDesktopState(selector)` with `useSyncExternalStore`.
4. Export only intentional mutation entrypoints:
   - `replaceDesktopFromSnapshot`
   - `applyDesktopDaemonEvent`
   - `markDesktopStale`
   - optional `setDesktopConnectionStatus`
5. Keep the store in memory. Do not persist canonical daemon state to LocalStorage/IndexedDB.
6. Add tests that selector subscribers are notified when the store changes.

Exit criteria:

- There is one external store path.
- It does not import TanStack DB.
- It does not import React Query.

### Checkpoint 4: snapshot fetch and normalization

Goal: load daemon state into the new store by replacement.

Steps:

1. Identify the current bootstrap endpoint. If it is still `/v3/sessions:workset`, keep using it as snapshot input.
2. Add or update backend snapshot response to include `rev`.
3. Add `desktop-state-snapshot.ts` to fetch and normalize snapshot data.
4. Move normalization logic out of DB collection writes and into plain object construction.
5. On snapshot load, call `replaceDesktopFromSnapshot(snapshot)` exactly once.
6. Make missing/invalid snapshot `rev` a hard error.
7. Add tests that snapshot load replaces old state rather than merging stale records.

Exit criteria:

- Initial load can populate the new external store.
- Snapshot is treated as replacement, not cache merge.
- Store has a known `rev` after load.

### Checkpoint 5: backend stream contract

Goal: make daemon events safely applicable.

Steps:

1. Audit current WebSocket messages from `/ws` and any V3 realtime session stream.
2. Choose the canonical Desktop projection revision. Prefer backend-provided `rev / prevRev`; do not make the UI guess.
3. Update backend event envelopes to include `rev` and `prevRev` for Desktop state messages.
4. Update snapshot/workset response to include matching `rev`.
5. Add backend tests proving events include `rev / prevRev` and that snapshot `rev` lines up with replay/resume.
6. Define explicit stream control messages for:
   - keepalive
   - slow consumer
   - cursor/replay error
   - reconnect/resync required
7. Ensure the server can resume/replay after the snapshot revision, or explicitly tells the client to resync.

Exit criteria:

- The frontend can subscribe/resume with `afterRev`.
- The backend either replays from `afterRev` or tells the frontend to resync.
- No frontend patch application depends on guessed revision continuity.

### Checkpoint 6: WebSocket lifecycle and bounded queue

Goal: wire the stream without creating a second state owner.

Steps:

1. Add `desktop-state-stream.ts` or equivalent.
2. Keep one socket owner for Desktop lifecycle.
3. Boot sequence must be snapshot first, then WebSocket subscribe/resume from snapshot `rev`.
4. Parse event envelopes into `DesktopDaemonEvent`.
5. Apply events only through `applyDesktopDaemonEvent`.
6. Add a bounded event queue for bursts/replay. Pick a limit and test it. Overflow triggers resync.
7. On `prevRev` mismatch, stop applying events, mark stale, close/ignore the socket until snapshot reload starts.
8. On slow-consumer/reconnect-required/cursor-error control messages, mark stale and reload snapshot.
9. Keep keepalive/liveness handling separate from canonical state mutation.

Exit criteria:

- Live daemon updates enter the store through the reducer only.
- Mismatches and slow-consumer cases resync instead of corrupting state.
- No unbounded queue exists.

### Checkpoint 7: migrate read selectors

Goal: move UI reads off TanStack DB and old cache paths.

Steps:

1. Create selector hooks in the external store module for hot reads:
   - `useDesktopSession(sessionId)`
   - `useDesktopMessages(sessionId)`
   - `useDesktopWorkspaceSessions(scope)`
   - `useDesktopRouteReadiness(scope, sessionId)`
   - `useDesktopActiveRun(sessionId)`
   - `useDesktopPreference(sessionId)`
   - `useDesktopPlan(sessionId)`
   - `useDesktopPlanRevisions(sessionId)`
   - `useDesktopPermissions(sessionId/runId)`
   - `useDesktopNotifications()`
2. Replace imports in `desktop-app-page.tsx` from `desktop-db.ts` with new selectors.
3. Replace chat panel/message reads with new selectors.
4. Replace permission/plan/notification reads with new selectors.
5. Preserve stable empty array/object constants to avoid render churn.
6. If a selector needs ordering, perform deterministic ordering in selector/reducer, not in a DB query.

Exit criteria:

- Production UI reads no longer import `desktop-db.ts`.
- No `useLiveQuery` remains in Desktop production source.
- Route/session switching reads from the external store only.

### Checkpoint 8: migrate write/event handlers

Goal: remove DB collection writes and old durable patch helpers.

Steps:

1. Replace `applyDurableEventToDesktopDB` usage with `applyDesktopDaemonEvent`.
2. Replace `mergeDesktopDBDurablePatch` usage with reducer actions caused by daemon events or snapshot replacement.
3. Replace `applyRunIntentToDesktopDB` with daemon event application.
4. Remove `applyOptimisticRunStartToDesktopDB` as canonical state. If needed, add ephemeral pending UI state.
5. Replace `desktopPlansCollection` direct writes with event/snapshot flow.
6. Update run stream controller tests so they inspect external-store state or reducer output, not DB collections.

Exit criteria:

- No production code imports DB write helpers.
- Canonical session/message/permission/plan/run state changes only through reducer entrypoints.

### Checkpoint 9: command cleanup

Goal: commands stop pretending to own daemon state.

Audit and update these areas:

- `web/src/features/desktop/chat/queries/chat-queries.ts`
- permission approve/deny flows
- plan save/approval flows
- session create/switch/hydrate flows
- run start/stop/compact flows
- notification clear/update flows
- settings mutations that affect daemon-derived Desktop records

Steps for each command:

1. Keep the network call.
2. Remove local canonical store/DB mutation.
3. Add ephemeral pending UI state only if the UI needs immediate feedback.
4. Confirm the backend emits a daemon event for the resulting durable change.
5. If no event exists, add backend event emission or force explicit snapshot reload after command success. Do not invent local canonical state.

Exit criteria:

- Commands do not mutate canonical Desktop state.
- Every durable command result arrives through WebSocket event or snapshot replacement.

### Checkpoint 10: delete TanStack DB path

Goal: remove the old authority entirely.

Steps:

1. Delete `web/src/features/desktop/state/desktop-db.ts` after no production callers remain.
2. Delete/replace `desktop-db-workset.ts` if it only feeds DB collections.
3. Delete/replace DB-specific tests:
   - `desktop-db-live-stream.spec.ts`
   - old TanStack assertions in `desktop-db-architecture.spec.ts`
   - DB references in run stream tests
4. Remove `@tanstack/db` from `web/package.json`.
5. Remove `@tanstack/react-db` from `web/package.json`.
6. Update lockfile if present.
7. Run the architecture rails and ensure forbidden dependency/import tests pass.

Exit criteria:

- No TanStack DB dependencies.
- No TanStack DB imports.
- No DB collections.
- No `useLiveQuery`.

### Checkpoint 11: integration validation

Goal: prove the boring architecture actually works.

Required smoke coverage:

1. Initial Desktop load populates sessions/workspaces/messages from snapshot.
2. Session switch is local store read/navigation only; no per-session hidden hydrate authority.
3. Send message posts command, then daemon event adds the message.
4. Streaming assistant/run updates arrive through WebSocket/reducer.
5. Permission approval posts command, then daemon event updates/removes permission.
6. Plan update/approval posts command, then event/snapshot updates plan state.
7. Run stop posts command, then event updates run/session state.
8. Notification update posts command, then event/snapshot updates notification state.
9. Reconnect resumes from current rev or reloads snapshot explicitly.
10. Rev mismatch triggers resync and does not apply the bad event.
11. Slow consumer/queue overflow triggers resync and does not corrupt state.

Exit criteria:

- Unit rails pass.
- Frontend tests pass.
- Smoke/e2e coverage passes for core flows.

### Checkpoint 12: final cleanup and documentation

Goal: leave no confusing alternate path behind.

Steps:

1. Remove transitional offender allowlists from architecture tests.
2. Update docs to say the external store is the current contract, not just interim intent.
3. Remove stale comments that mention TanStack DB as desired architecture.
4. Keep a short note in the superseded TanStack doc pointing to this document.
5. Ensure final architecture test protects the stack.

Exit criteria:

```txt
No @tanstack/db dependency.
No @tanstack/react-db dependency.
No Desktop core useLiveQuery.
No Desktop DB collections.
No canonical optimistic daemon-state mutation.
One external in-memory store.
One reducer.
Snapshot replaces store.
WebSocket events patch store only when rev matches.
Commands wait for daemon events/snapshot to change canonical state.
Architecture tests fail if this regresses.
```

## Suggested implementation order by file

Start here:

1. `docs/checkpoints/desktop-v3-external-store-state-contract.md` - this plan.
2. `web/src/features/desktop/state/desktop-db-architecture.spec.ts` - rewrite into anti-TanStack/external-store architecture rails.
3. `web/src/features/desktop/state/desktop-state.ts` - reducer/types.
4. `web/src/features/desktop/state/desktop-state-store.ts` - `useSyncExternalStore` wrapper.
5. `web/src/features/desktop/state/desktop-state-snapshot.ts` - snapshot fetch/normalization.
6. `web/src/features/desktop/state/desktop-state-stream.ts` - WebSocket/resume/resync lifecycle.
7. `swarmd/internal/api/sessions_v3_workset.go` and tests - add/verify snapshot `rev` if missing.
8. `swarmd/internal/api/*realtime*` and tests - add/verify event `rev / prevRev`, resume, slow-consumer control behavior.
9. `web/src/features/desktop/layout/desktop-app-page.tsx` - replace DB route/session hooks.
10. `web/src/features/desktop/chat/queries/chat-queries.ts` - remove DB patching and canonical optimistic writes.
11. Permission/plan/notification/run files - replace DB/cache reads and local canonical writes.
12. `web/package.json` - remove `@tanstack/db` and `@tanstack/react-db` after callers are gone.

## Agent handoff rules

Any agent implementing this must obey these rules:

1. Do not design a new state system.
2. Do not reintroduce TanStack DB, React DB, Zustand, IndexedDB, LocalStorage canonical daemon state, or a client sync engine.
3. Do not leave two canonical state paths alive.
4. Do not make commands mutate canonical state locally.
5. Do not apply WebSocket patches without revision continuity.
6. Do not hide rev mismatches with best-effort merging.
7. Do not use React Query as Desktop core state.
8. Do not skip the architecture tests. Write rails first, then migrate.
9. Do not keep broad compatibility fallbacks. Delete the old path when the slice moves.
10. If a backend event is missing for a command result, add the backend event or explicit snapshot reload. Do not fake it in the UI.

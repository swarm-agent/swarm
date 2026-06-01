# Sessions API v2: full native rewrite for primary -> local-container

Status: rewrite source of truth.

This document supersedes the earlier primary-only framing. The next implementation phase is a full native v2 rewrite for primary and local-container session create + lifecycle + dispatch + mirroring. The local-container path must not wrap, proxy, or infer authority through legacy v1 session routes.

## What changed

We are no longer treating local-container as a later add-on after a primary-only lifecycle slice.

We now need one coherent Sessions API v2 model that:

1. creates primary and local-container sessions natively,
2. serves the full lifecycle natively,
3. dispatches primary -> local-container deterministically from frozen execution + live placement + live workspace binding,
4. uses native mirroring APIs instead of legacy peer session wrappers,
5. preserves clear traceability after the fact.

## Non-negotiable rules

### Native v2 only

- No wrapping through `/v1/sessions`.
- No wrapping through `handleSessions`, `handleSessionByID`, or `createSessionFromRequest`.
- No wrapping through `proxyRoutedSessionRequest`.
- No authority from `SessionRouteRecord`, topology session routes, backend URLs, child URLs, next-hop fields, or durable route state.
- No authority from workspace name, workspace path, runtime path, container-local path, or client-supplied routing hints.
- No authority from mirrored session snapshots.

### Authority sources

The only authority for native v2 session lifecycle is:

1. frozen `SessionExecutionV2Record`,
2. live `TopologyRuntimePlacementRecord`,
3. live `TopologyWorkspaceBindingRecord`.

### Strict class enforcement

Every native v2 mutating path must validate one of:

- `SessionExecutionClassPrimary`
- `SessionExecutionClassLocalContainer`

There is no generic fallback class and no legacy routed exception.

### Workspace binding rule

- TUI CWD primary sessions are the only allowed no-binding exception.
- That exception is only for terminal/TUI primary host sessions.
- Web and desktop must always provide `workspace_binding_id`.
- Local-container sessions must always provide `workspace_binding_id`.

## Current code inventory

### Native v2 create routes that exist today

Registered in `swarmd/internal/api/server_routes.go`:

- `POST /v2/sessions/primary` -> `handleSessionsV2Primary`
- `POST /v2/sessions/local-containers` -> `handleSessionsV2LocalContainers`
- `/v2/sessions/{id}...` -> `handlePrimarySessionV2ByID`

### What `sessions_v2_primary.go` does today

`swarmd/internal/api/sessions_v2_primary.go` currently provides:

- strict request decoding via `decodeSessionsV2CreateRequestStrict`
- allowed create fields:
  - `swarm_id`
  - `workspace_binding_id`
  - `workspace_path` only for TUI CWD primary create
  - title/mode/agent/worktree/preference/metadata
- authority-looking metadata rejection via `validateSessionsV2Metadata`
- create-time runtime resolution via:
  - `GetRuntimeForAccount`
  - `GetRuntimePlacementForAccount`
  - `GetWorkspaceBindingForAccount`
- create-time placement validation via:
  - `validatePrimarySessionV2Placement`
  - `validateLocalContainerSessionV2Placement`
- create-time binding validation via:
  - `validatePrimarySessionV2Binding`
  - `validateLocalContainerSessionV2Binding`
  - `validateCommonSessionV2Binding`
- frozen execution construction via `sessionsV2ExecutionFromBinding`
- create persistence via `sessions.CreateFromExecutionV2(...)`

### What native v2 create validates for local-container today

The local-container create path already enforces several correct invariants:

- selected runtime placement must exist and be active
- placement runtime kind must be `container`
- placement authority host swarm id must equal the local primary swarm id
- placement authority container id must be non-empty
- workspace binding must exist in the principal account
- binding must be bound
- binding must be writable/read_write
- binding destination runtime swarm id must equal selected container swarm id
- binding destination authority host swarm id must equal local primary swarm id
- binding destination runtime kind must be `container`
- binding destination container id must equal placement authority container id
- binding placement/binding generations must match
- binding attesting host must match placement authority host

### What native v2 lifecycle does today

`swarmd/internal/api/sessions_v2_lifecycle.go` currently exposes a primary-only lifecycle under `/v2/sessions/{id}`:

- `GET /v2/sessions/{id}`
- `GET/POST /v2/sessions/{id}/messages`
- `GET/POST /v2/sessions/{id}/metadata`
- `GET/POST /v2/sessions/{id}/mode`
- `GET/POST /v2/sessions/{id}/preference`
- `GET/POST /v2/sessions/{id}/codex`
- `GET/POST /v2/sessions/{id}/plans/active`
- `GET/POST /v2/sessions/{id}/plans`
- `GET /v2/sessions/{id}/plans/{plan_id}`
- `GET /v2/sessions/{id}/plans/{plan_id}/history`
- `GET /v2/sessions/{id}/permissions`
- `POST /v2/sessions/{id}/permissions/{permission_id}/resolve`
- `POST /v2/sessions/{id}/permissions/resolve_all`
- `GET /v2/sessions/{id}/usage`
- `POST /v2/sessions/{id}/run`
- `GET/POST /v2/sessions/{id}/run/stream`

### What native v2 lifecycle checks today

`requirePrimarySessionV2Authority(...)` currently:

- loads `SessionExecutionV2Record` from Pebble
- requires principal/account-scope match
- requires execution class == `primary`
- requires local node identity
- requires execution runtime swarm id and authority host swarm id == local primary swarm id
- requires host runtime kind and empty authority container id
- re-loads runtime placement
- requires active host self-placement
- requires placement generation == execution placement generation
- re-loads workspace binding when binding exists
- requires bound binding in same account
- requires binding destination == local primary self-authority
- requires binding destination runtime kind == `host`
- requires binding attesting host match
- requires binding generations and source identity match frozen execution
- requires read_write/writable for mutating calls
- allows the TUI CWD primary exception when execution carries no binding and has the expected synthetic TUI markers

### The hard current blocker

Native lifecycle is still primary-only.

`requirePrimarySessionV2Authority(...)` rejects any execution class other than `primary`.

That means:

- local-container create exists,
- local-container `SessionExecutionV2Record` can be persisted,
- but local-container sessions cannot use native `/v2/sessions/{id}` lifecycle.

That is the central mismatch we now need to remove.

## What the current wrong path does today

The current local-container/routed path still depends on legacy routed-session machinery.

### Legacy create/open flow

The current routed/local-container create path uses the old host/peer route stack:

1. Primary-side create code builds a hosted/routed contract.
2. Primary writes or prepares a canonical parent session.
3. Primary writes a `SessionRouteRecord` via `sessionRoutes.Put(...)`.
4. Primary writes a topology session route via `upsertTopologySessionRoute(...)`.
5. Primary sends `POST /v1/swarm/peer/sessions/open` via `postPeerJSONToSwarmTarget(...)`.
6. Child/peer receives that in `handlePeerSessionOpen(...)`.
7. Child normalizes request/route/path fields via:
   - `normalizedTerminalPeerSessionOpenRoute(...)`
   - `normalizedTerminalPeerSessionOpenRequest(...)`
8. Child creates a local session via `createSessionFromRequestWithSessionID(...)`.
9. Primary calls `SyncHostedMirrorOpenState(...)` to sync canonical mirror state from the child response.

### Legacy lifecycle flow

Later lifecycle requests still rely on routed proxy behavior:

- primary call sites in `server.go` check `proxyRoutedSessionRequest(...)`
- that resolves target using `routedSessionTarget(...)`
- target resolution reads stored route state via `GetSessionRouteForAccount(...)`
- target transport is resolved from authority host/backend URL state
- request is proxied to child/peer handlers

### Legacy peer session APIs still in play

Registered peer session APIs:

- `POST /v1/swarm/peer/sessions/open`
- `POST /v1/swarm/peer/sessions/append_message`
- `POST /v1/swarm/peer/sessions/mode`
- `POST /v1/swarm/peer/sessions/title`
- `POST /v1/swarm/peer/sessions/metadata`
- `POST /v1/swarm/peer/sessions/lifecycle`
- `POST /v1/swarm/peer/sessions/event`

### What those peer handlers do today

`swarmd/internal/api/routed_sessions.go` currently lets the child/peer path own or mirror session behavior by:

- mutating messages through `handlePeerSessionAppendMessage`
- mutating mode through `handlePeerSessionMode`
- mutating title through `handlePeerSessionTitle`
- mutating metadata through `handlePeerSessionMetadata`
- ingesting mirrored lifecycle through `handlePeerSessionLifecycle` -> `StoreMirroredLifecycle(...)`
- ingesting mirrored events through `handlePeerSessionEvent` -> `StoreMirroredEvent(...)`
- decoding embedded lifecycle/message payloads out of mirrored event payloads and storing them locally

## Why the current path is wrong for the new contract

### Wrong authority source

The routed path still depends on:

- `SessionRouteRecord`
- topology session routes
- proxy target resolution
- authority transport lookup from route state

That is forbidden for native v2 authority.

### Wrong ownership split

The old path lets the peer session APIs behave like the canonical lifecycle surface.

For native v2, the canonical session lifecycle must stay on `/v2/sessions/{id}` under one authority model. Internal runtime communication is allowed, but it must not masquerade as the public session API.

### Wrong path semantics

The old path still normalizes or compares:

- request workspace path
- host workspace path
- runtime workspace path
- workspace name

Those are not allowed as routing authority in the new design.

### Wrong traceability

The old path spreads one logical session operation across:

- public create handler
- route stores
- topology session route writes
- peer open HTTP
- child-side session create
- mirror synchronization
- later proxy interception
- peer lifecycle/event ingestion

That is exactly the traceability failure we are trying to remove.

## New setup: how authority must work after the rewrite

### Trusted user-facing create inputs

The only trusted user-facing routing inputs for workspace-backed v2 create are:

- `swarm_id`
- `workspace_binding_id`

Everything else is non-authoritative session configuration.

### What `swarm_target` means now

`swarm_target` is a UI selection/projection, not authority.

It may help the client choose:

- endpoint family (`/primary` vs `/local-containers`)
- selected `swarm_id`
- selected `workspace_binding_id`

But after that, the backend must ignore `swarm_target` display metadata as authority.

No trust in:

- target name
- target kind
- target workspace path
- target display name
- deployment id
- backend URL
- next hop

### Trusted backend resolution chain

For native v2, the backend decision chain must be:

1. client selects a swarm target in UI,
2. client sends only `swarm_id` + `workspace_binding_id`,
3. backend loads runtime placement for `swarm_id`,
4. backend loads workspace binding for `workspace_binding_id`,
5. backend validates that binding destination matches placement,
6. backend freezes `SessionExecutionV2Record`,
7. later lifecycle loads frozen execution by session id,
8. backend re-loads live placement and live binding,
9. backend rejects stale/mismatched generation or destination,
10. backend derives authority host/runtime/container from execution + placement + binding,
11. backend dispatches natively.

There is no route lookup step in the middle.

## Post-replication resolution: swarm target + binding -> exact dispatch

This is the hard part and it must be explicit.

### What replication must produce

After replication to a local container, the system must already have:

1. a runtime placement record for the target runtime swarm id,
2. a workspace binding record whose destination points at that target runtime,
3. matching placement and binding generations,
4. a destination authority host swarm id,
5. a destination runtime kind,
6. a destination container id for container targets,
7. a destination workspace path inside the runtime,
8. a source workspace identity for the primary-side canonical workspace.

### How create resolves after replication

When primary receives:

- `swarm_id = selected target runtime`
- `workspace_binding_id = selected replicated workspace binding`

it must prove all of the following:

1. placement exists for `swarm_id`
2. binding exists for `workspace_binding_id`
3. binding destination runtime swarm id == `swarm_id`
4. binding destination authority host swarm id == placement authority host swarm id
5. binding destination runtime kind == placement runtime kind
6. for local-container, binding destination container id == placement authority container id
7. binding destination workspace path is non-empty
8. binding source workspace identity is complete
9. binding and placement generations match
10. binding is bound and account-scoped to the principal

Only then can the backend freeze the execution.

### How later lifecycle auto-resolves where to send the request

Later user lifecycle requests should carry only `session_id`.

The backend must then:

1. load `SessionExecutionV2Record`
2. read `ExecutionClass`
3. re-load live placement and live binding
4. derive:
   - runtime swarm id
   - authority host swarm id
   - runtime kind
   - authority container id
   - runtime workspace path
5. choose native dispatch from those values

That means the backend "automatically understands where to send this" from the frozen execution plus live placement/binding, not from a route store and not from the client repeating target hints.

## Required public v2 API surface

### Create

- `POST /v2/sessions/primary`
- `POST /v2/sessions/local-containers`

### Canonical lifecycle

These must be native for both primary and local-container sessions under one authority validator:

- `GET /v2/sessions/{id}`
- `GET/POST /v2/sessions/{id}/messages`
- `GET/POST /v2/sessions/{id}/metadata`
- `GET/POST /v2/sessions/{id}/mode`
- `GET/POST /v2/sessions/{id}/preference`
- `GET/POST /v2/sessions/{id}/codex`
- `GET/POST /v2/sessions/{id}/plans/active`
- `GET/POST /v2/sessions/{id}/plans`
- `GET /v2/sessions/{id}/plans/{plan_id}`
- `GET /v2/sessions/{id}/plans/{plan_id}/history`
- `GET /v2/sessions/{id}/permissions`
- `POST /v2/sessions/{id}/permissions/{permission_id}/resolve`
- `POST /v2/sessions/{id}/permissions/resolve_all`
- `GET /v2/sessions/{id}/usage`
- `POST /v2/sessions/{id}/run`
- `GET/POST /v2/sessions/{id}/run/stream`

## Required internal runtime API surface

We also need a native internal runtime API to replace the old peer session APIs.

### Required replacements

Old APIs to replace:

- `/v1/swarm/peer/sessions/open`
- `/v1/swarm/peer/sessions/append_message`
- `/v1/swarm/peer/sessions/mode`
- `/v1/swarm/peer/sessions/title`
- `/v1/swarm/peer/sessions/metadata`
- `/v1/swarm/peer/sessions/lifecycle`
- `/v1/swarm/peer/sessions/event`

### Recommended native replacements

#### Internal runtime open/sync

- `POST /v2/internal/runtime-sessions/open`
  - authority host -> runtime
  - creates or attaches runtime-local execution context for a frozen `SessionExecutionV2Record`
- `POST /v2/internal/runtime-sessions/{id}/sync/state`
  - authority host -> runtime
  - pushes canonical mode/preference/metadata/codex/session-state needed by the runtime

#### Internal runtime execution

- `POST /v2/internal/runtime-sessions/{id}/run`
- `GET/POST /v2/internal/runtime-sessions/{id}/run/stream`

These are not public lifecycle APIs. They are runtime-dispatch APIs used after canonical v2 authority succeeds.

#### Internal mirroring

Replace split `lifecycle` + `event` peer ingestion with one typed mirror surface:

- `POST /v2/internal/runtime-sessions/{id}/mirror/batch`

Recommended payload types inside the batch:

- `session.snapshot`
- `session.lifecycle`
- `run.event`
- `message.stored`
- `usage.delta`

Rules:

- mirror payloads are typed
- every payload must name `session_id`
- every payload must match the frozen execution class and runtime identity
- lifecycle/message payloads must be validated before persistence
- mirror ingestion must fail closed on mismatched session id, runtime identity, or malformed payloads

## Mirroring model for primary -> local-container

### Canonical owner

For primary -> local-container v2, the canonical session remains owned by the authority host side of the session, not by the legacy peer session path.

### What mirror data is allowed to update

Allowed mirrored facts:

- runtime-generated events
- runtime lifecycle changes
- runtime-emitted stored messages
- usage deltas
- runtime-open status

### What mirror data must not overwrite authority

Mirrors must never redefine:

- source workspace identity
- workspace binding id
- execution class
- authority host swarm id
- destination container id
- canonical authority path

### Path rule

Container-local runtime paths are execution facts only.

They must not replace primary/source workspace identity.

That means:

- child `SessionSnapshot.WorkspacePath` is not authority
- child worktree root path is not source workspace identity
- `/workspaces/...` inside the container is not the canonical workspace source path

## Required authority helper: `requireSessionV2Authority`

The current primary-only helper must become a polymorphic helper.

Recommended shape:

```go
type sessionV2Authority struct {
    Principal identity.Principal
    Execution pebblestore.SessionExecutionV2Record
    Placement pebblestore.TopologyRuntimePlacementRecord
    Binding   pebblestore.TopologyWorkspaceBindingRecord
}
```

### Required behavior

For every request:

1. load principal
2. load execution by session id
3. ensure execution account scope matches principal
4. switch on execution class
5. re-load live placement using `execution.RuntimeSwarmID`
6. if binding exists, re-load live binding using `execution.WorkspaceBindingID`
7. validate class-specific invariants
8. validate generation invariants
9. validate access-mode invariants
10. return a typed authority object

### Primary class invariants

- execution class == `primary`
- runtime kind == `host`
- authority host swarm id == local primary swarm id
- authority container id empty
- placement is active host self-placement
- binding destination is host self-authority

### Local-container class invariants

- execution class == `local_container`
- runtime kind == `container`
- authority host swarm id == local primary swarm id for this phase
- authority container id non-empty
- placement is active container placement
- binding destination runtime kind == `container`
- binding destination runtime swarm id == execution runtime swarm id
- binding destination authority host swarm id == execution authority host swarm id
- binding destination container id == execution authority container id

## Dispatch model after authority

### Public lifecycle stays canonical

Public `/v2/sessions/{id}` handlers should always:

1. validate authority natively,
2. mutate/read canonical state natively,
3. dispatch to runtime only when runtime execution is required.

### Primary dispatch

For `primary` execution:

- use local host runtime services directly.

### Local-container dispatch

For `local_container` execution in this phase:

- authority host is the local primary,
- runtime target is a local container,
- dispatch must be derived from execution + placement + binding,
- dispatch must not go through legacy peer session wrappers.

This may use an internal runtime transport, but it must be the v2 internal runtime API, not the old routed-session public surface.

## Things the next implementation must delete or stop using

Do not call these from the new public v2 path:

- `handleSessions`
- `handleSessionByID`
- `createSessionFromRequest`
- `createSessionFromRequestWithSessionID`
- `proxyRoutedSessionRequest`
- `routedSessionTarget`
- `routedSessionTargetOrFailClosed`
- `handlePeerSessionOpen`
- `handlePeerSessionAppendMessage`
- `handlePeerSessionMode`
- `handlePeerSessionTitle`
- `handlePeerSessionMetadata`
- `handlePeerSessionLifecycle`
- `handlePeerSessionEvent`
- `SessionRouteRecord` authority
- topology session route authority

## Explicit next-agent brief

The next implementation agent should assume the following setup:

1. We are doing a full native v2 rewrite for primary and local-container session APIs.
2. The current local-container path is wrong because it still depends on legacy routed peer session APIs and route stores.
3. `swarm_target` is only a UI selection/projection; it is not authority.
4. The trusted user-facing routing inputs are only `swarm_id` and `workspace_binding_id`.
5. The trusted lifecycle authority chain is only frozen execution + live placement + live binding.
6. All lifecycle APIs previously used through v1 wrappers must be redone natively under `/v2/sessions/{id}`.
7. Internal runtime communication and mirroring APIs must also be redone natively; do not reuse `/v1/swarm/peer/sessions/*`.
8. Post-replication dispatch must deterministically derive authority host/runtime/container from binding + placement, not from route records or backend URLs.
9. Mirroring must be typed, explicit, and fail closed.
10. The result must leave a clear audit trail: create request -> frozen execution -> live placement/binding recheck -> runtime dispatch -> mirror ingestion.

## Relevant filepaths

- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/sessions_v2_primary.go`
- `swarmd/internal/api/sessions_v2_lifecycle.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/server.go`
- `swarmd/internal/api/target_workspace_route.go`
- `swarmd/internal/api/swarm_targets.go`
- `swarmd/internal/api/swarm_replicate.go`
- `swarmd/internal/api/swarm_replicate_container.go`
- `swarmd/internal/api/flows_mirror.go`
- `swarmd/internal/api/flows_runner.go`
- `swarmd/internal/session/session_execution_v2.go`
- `swarmd/internal/session/service.go`
- `swarmd/internal/store/pebble/topology_runtime_placement_store.go`
- `swarmd/internal/store/pebble/topology_workspace_binding_store.go`
- `web/src/features/desktop/chat/services/chat-routing.ts`

## Bottom line

The rewrite target is not "make the current local-container route work."

The rewrite target is:

- public v2 create + lifecycle are native,
- internal runtime open/run/mirror APIs are native,
- routing authority comes only from execution + placement + binding,
- post-replication resolution is deterministic from swarm target selection -> `swarm_id` + `workspace_binding_id` -> frozen execution -> live placement/binding -> dispatch,
- and no v1 wrapper or route-store authority remains in the path.

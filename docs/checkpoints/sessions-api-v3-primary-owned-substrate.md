# Sessions API v3: primary-owned session substrate

Status: CP1 baseline source of truth.

This note freezes the factual baseline and vocabulary for V3 Stage 1. It intentionally separates **Current V2 Reality** from the **V3 Stage 1 target** so implementation cannot drift back into container/runtime ownership semantics.

Stage 1 is durable primary/server infrastructure only. The primary must be able to create, list, hydrate, message, maintain lifecycle/run intent, append ordered durable events, stream/replay committed events, survive daemon restart, and serve clients without any container lookup.

## Stage 1 redlines

- `POST /v3/sessions/{id}/messages` must not call runtime/container dispatch at any point.
- Message commit is chat durability; execution dispatch is a separate future concern.
- Invalid routing/dispatch authority can fail closed only for execution intent by recording `dispatch_blocked`; it must not block, erase, or hide the committed user message.
- V3 writes must go through `ApplySessionMutation` or the exact primary-owned mutation boundary that replaces that name.
- V3 stream/replay must be structurally separate from V2 run stream code and must read from primary durable session events only.
- Stage 1 events are primary-owned and boring. No assistant/tool/runtime output events enter Stage 1 unless the primary can create, persist, project, hydrate, replay, and test them without an executor.

## Current V2 Reality

### V2 route surface mixes public session APIs, internal runtime APIs, and peer/mirror APIs

Current route registration shows the V2 public session routes, internal runtime routes, legacy V1 session routes, and peer mirror/open routes all registered in the same runtime surface:

- `POST /v2/sessions/primary`, `POST /v2/sessions/local-containers`, `/v2/sessions/{id}...`, and `/v2/internal/runtime-sessions/...` are registered in `swarmd/internal/api/server_routes.go` lines 225-230.
- Legacy peer session APIs remain registered at `/v1/swarm/peer/sessions/open`, `/append_message`, `/mode`, `/title`, `/metadata`, `/lifecycle`, and `/event` in `swarmd/internal/api/server_routes.go` lines 238-244.

V3 Stage 1 must add an independent `/v3/sessions...` surface and must not depend on any of these V2/V1 runtime, peer, or proxy routes for chat truth.

### V2 local-container create opens a runtime session before completing primary-side state

The local-container create handler is `handleSessionsV2LocalContainers`, which delegates to `handleSessionsV2Create` in `swarmd/internal/api/sessions_v2_primary.go` lines 70-78. The primary create path persists directly through `CreateFromExecutionV2` at line 118, but the local-container path is different:

1. `createSessionsV2LocalContainer` builds and validates frozen execution in `sessions_v2_primary.go` lines 134-162.
2. It loads the workspace binding snapshot in lines 163-170.
3. It calls `dispatchRuntimeSessionV2Open(...)` before returning success in line 171.
4. Only after the runtime open response validates does it persist primary-side runtime-open state via `persistPrimarySideRuntimeSessionOpen(...)` in line 181.
5. It then ingests the runtime initial mirror via `ingestRuntimeSessionV2InitialMirror(...)` in line 185.

`dispatchRuntimeSessionV2Open` either calls the local runtime-open handler directly or performs an HTTP request to the runtime authority endpoint (`sessions_v2_primary.go` lines 252-285). The runtime endpoint is `POST /v2/internal/runtime-sessions/open` (`runtime_sessions_v2.go` lines 16-21). That handler validates runtime authority and creates/attaches runtime-local session state (`runtime_sessions_v2.go` lines 43-163).

V3 Stage 1 must not have an equivalent runtime-open phase for create. A V3 session create is complete when primary durable state and primary event/projection state are committed.

### V2 mirror ingestion lets runtime/container state update primary records

V2 contains explicit mirror/open ingestion paths:

- `applyRuntimeSessionV2MirrorActions` stores mirrored session snapshots, lifecycles, messages, and events in `swarmd/internal/api/sessions_v2_primary.go` lines 729-770.
- `SyncHostedMirrorOpenState` updates an existing primary session from a child/runtime open response in `swarmd/internal/session/service.go` lines 268-332.
- `StoreMirroredLifecycle` writes mirrored lifecycle state in `service.go` lines 362-377.
- `StoreMirroredEvent` appends mirrored events in `service.go` lines 387-403.

These are V2/mirror behaviors. V3 Stage 1 must not model a runtime/container as session owner or co-owner. Containers are future dispatch authorities only.

### V2 lifecycle reads and writes require execution/placement/binding dispatch authority

The V2 lifecycle router under `/v2/sessions/{id}` dispatches paths in `swarmd/internal/api/sessions_v2_lifecycle.go` lines 152-199. Local-container lifecycle paths may be intercepted and dispatched to runtime handlers by `handleLocalContainerSessionV2LifecycleIfNeeded` (`sessions_v2_lifecycle.go` lines 257-335).

Current V2 authority is execution-oriented:

- `requireSessionV2Authority` loads `SessionExecutionV2Record`, live runtime placement, and live workspace binding, then validates primary or local-container invariants (`sessions_v2_lifecycle.go` lines 543-600).
- `requirePrimarySessionV2DispatchAuthority` wraps that validator for primary dispatch (`sessions_v2_lifecycle.go` lines 246-254).
- The message endpoint calls `requirePrimarySessionV2DispatchAuthority` before GET/POST message behavior (`sessions_v2_lifecycle.go` lines 797-836).

This is correct for V2 dispatch safety, but wrong as the V3 chat durability gate. V3 Stage 1 must split validation into:

1. chat/session access, idempotency, and protected-metadata validation; and
2. optional future execution-intent/dispatch-authority validation.

Only the second class can produce `dispatch_blocked`.

### V2 run stream truth is process-memory and runtime-oriented

V2 `/v2/sessions/{id}/run/stream` is handled by `handlePrimarySessionV2RunStream` and `handleAuthorizedSessionV2RunStreamWebsocket` (`swarmd/internal/api/sessions_v2_lifecycle.go` lines 1501-1586). The implementation requires `s.runner` and `s.runStreams` and routes `start`, `resume`, and `stop` to V2 run stream logic.

The V2 run stream manager is in `swarmd/internal/api/run_stream_ws.go`:

- replay settings and `errRunStreamNotFound` are defined at lines 23-31;
- `runStreamState` stores events/subscribers in process memory at lines 113-125;
- `runStreamManager` stores active runs in a map at lines 133-147.

V3 Stage 1 must not wrap this stream as the source of truth. A persisted V3 session must never become unreplayable merely because process memory was reset or because a V2 run stream is missing.

### Legacy routed sessions still proxy through route records and backend URLs

The legacy routed path resolves a target from route/topology state, then proxies to another swarm target:

- `routedSessionTarget` reads `SessionRouteRecord`/topology state and backend URL information in `swarmd/internal/api/routed_sessions.go` lines 57-128.
- `proxyRoutedSessionRequest` proxies requests to the resolved target in lines 159-185.

V3 may reuse route/binding/topology facts only as future dispatch-authority inputs. They are not chat ownership and are not a hydration/list/message dependency.

### Existing primary storage facts and gaps

Existing session storage vocabulary to preserve:

- `SessionSnapshot` stores canonical session metadata/state (`swarmd/internal/store/pebble/session_store.go` lines 16-36).
- `SessionLifecycleSnapshot` stores run/lifecycle fields such as `RunID`, `Active`, `Phase`, and `Generation` (`session_store.go` lines 38-52).
- `SessionExecutionV2Record` freezes V2 execution authority facts (`session_store.go` lines 54-77).
- `MessageSnapshot` stores message facts and the current `GlobalSeq` (`session_store.go` lines 79-89).

Existing event storage facts:

- `EventEnvelope` is a global event envelope with `GlobalSeq`, `Stream`, `EventType`, `EntityID`, payload, and timestamps (`swarmd/internal/store/pebble/event_log.go` lines 13-22).
- `EventLog.Append` allocates a global sequence and writes `evt/{global_seq}` plus `meta/global_seq` (`event_log.go` lines 52-89; `keys.go` lines 145-149).

Existing consistency gap to fix in CP2:

- `CreateSessionWithOptions` persists the session first and appends `session.created` as a separate operation afterward (`swarmd/internal/session/service.go` lines 116-191). That split is acceptable V2 history but not the V3 mutation boundary.
- V3 needs one primary mutation boundary that atomically owns idempotency, message writes, session/lifecycle projection updates, event append, per-session primary seq, and projection high-watermark advancement, or else it must specify exact recovery/reprojection behavior.

## V3 Stage 1 target

### Ownership model

The primary owns chat truth:

- canonical `session_id`;
- canonical `message_id`;
- primary-owned `run_id` for execution intent;
- ordered session event sequence;
- snapshots/projections;
- lifecycle/run-intent state;
- idempotency records;
- client stream/replay cursors.

A future container may execute work for a V3 session, but it never becomes the permanent home of the session and never owns hydration/replay truth.

### Required primary-only API behavior

Stage 1 V3 APIs must work with no runtime/container configured:

- `POST /v3/sessions` creates primary canonical state and emits durable primary events only.
- `GET /v3/sessions` lists from primary projection/storage only.
- `GET /v3/sessions/{id}` hydrates from primary projection/storage only and returns `last_event_seq` plus `projection_high_watermark_seq`.
- `POST /v3/sessions/{id}/messages` commits a user message through the primary mutation boundary and may create a primary run intent in `pending_executor` or `dispatch_blocked`, but must not dispatch.
- `WS /v3/sessions/{id}/stream` replays committed primary session events after the client cursor and then attaches to live primary event notifications.

### Message commit contract

`POST /v3/sessions/{id}/messages` has two separate concepts:

1. **Chat validation**: principal/account/session access, idempotency key, message shape, and protected metadata. Failure here may reject the request.
2. **Execution-intent validation**: future route/binding/placement/executor availability. Failure here records `dispatch_blocked` on a durable run intent when assistant work is requested. It must not roll back the user message.

In Stage 1, this endpoint must never call runtime/container dispatch. No executor present is a normal durable state, not an HTTP failure for chat commit.

### Mutation boundary

`ApplySessionMutation` is the intended name for the V3 write boundary. If the implementation uses another exact name, it must be just as obvious and exclusive.

This boundary owns:

- idempotency lookup/result write;
- session-scoped primary seq allocation;
- message write;
- lifecycle/run-intent write;
- durable event append;
- projection update;
- `projection_high_watermark_seq` advancement;
- returned mutation result.

HTTP handlers must not independently stitch these pieces together.

### Stream/replay model

V3 stream state is not run state. The durable primary session event log is the replay source of truth.

The WebSocket layer may keep socket subscriptions in memory, but it must not keep canonical event truth only in memory. Losing process memory can drop live socket subscriptions, but must not break hydrate/replay for persisted sessions.

## Vocabulary freeze

| Term | CP1 meaning |
| --- | --- |
| `session_id` | Primary canonical chat/conversation identity. Required for all V3 create/list/hydrate/message/stream operations. |
| `message_id` | Primary canonical message identity. Retried client requests must return the same message identity/result. |
| `run_id` | Primary-owned execution-intent identity. Stage 1 can create it as pending/blocked state without an executor. |
| `runtime_session_id` | Future runtime attachment identity. Out of scope for Stage 1 and not required for chat truth. |
| Primary session seq | Session-scoped ordered event cursor used by hydrate/stream/replay. It is not the existing global event sequence. |
| `event_id` | Stable durable event identity for an event envelope. |
| Snapshot/projection | Primary materialized read state derived from committed mutations/events. It must declare the event seq through which it is valid. |
| `last_event_seq` | Highest durable primary session event sequence known for the session. |
| `projection_high_watermark_seq` | Highest primary session event sequence applied to the returned projection/snapshot. Clients resume streams from this cursor unless projection is proven current through `last_event_seq`. |
| `SessionExecutionV2Record` | V2 frozen execution authority record. Useful as vocabulary/history; not V3 chat ownership. |
| Route/binding/topology records | Future dispatch-authority inputs only. They never determine whether V3 chat can be listed, hydrated, or messaged. |
| Protected metadata | Client metadata keys that attempt to override workspace binding, runtime swarm, authority host/container, route, source/runtime workspace paths, or other authority facts. Reject or sanitize at the chat validation boundary. |
| `dispatch_blocked` | Durable run-intent/lifecycle state meaning execution cannot be dispatched. It blocks execution only; it does not block chat durability. |

## Stage 1 non-goals

The following are explicitly out of scope for Stage 1 implementation:

- containers as session owners;
- runtime attachment;
- runtime mirroring;
- container hopping;
- cross-container resume;
- direct container WebSockets;
- distributed execution;
- assistant/tool output from containers;
- wrapping or reusing V2 run stream truth;
- `runtime_session_id` as required chat identity;
- route-store or backend-URL lookup as create/list/hydrate/message/stream dependency.

## Hard invariants

- Primary retrieval must work with no container configured.
- A user message must be committed durably before any future execution handoff can affect execution state.
- Client-visible stream events must be durable before WebSocket emission.
- Primary session seq is canonical for V3 replay cursors.
- Replay must not depend on process memory.
- A persisted V3 session must not return `run stream not found` merely because V2 run memory is gone.
- User metadata cannot override authority fields.
- Dispatch authority failures are recorded as `dispatch_blocked` and do not make the session unavailable.

## Next-checkpoint attack points

### CP2: mutation consistency boundary

- Do not copy the current `CreateSessionWithOptions` split-write pattern into V3.
- Decide whether to add `session_event_store.go`, `idempotency_store.go`, and projection high-watermark storage before route handlers are written.
- Treat the existing global `EventLog` as historical/global infrastructure; V3 still needs session-scoped primary seq and session replay semantics.

### CP3: V3 create/list/hydrate

- Register `/v3/sessions` routes independently in `server_routes.go`.
- Keep V3 handlers out of `handlePrimarySessionV2ByID`, `proxyRoutedSessionRequest`, and runtime-session handlers.
- Hydrate from primary stores/projections and return `last_event_seq` plus `projection_high_watermark_seq`.

### CP4: message commit and run intent

- Split chat validation from execution-intent validation.
- Use idempotency before writing duplicate messages/runs/events.
- Never call `dispatchRuntimeSessionV2Open`, `dispatchLocalContainerSessionV2Lifecycle`, runtime-session handlers, or V2 run stream start from `POST /v3/sessions/{id}/messages`.

### CP5/CP6: event log and stream

- Implement session event replay from durable primary storage, not `runStreamManager`.
- Specify cursor behavior for missing/malformed/out-of-range `after_seq` before client integration.
- Keep socket hub state as delivery optimization only.

### CP7/CP8: clients and authority gate

- Clients should treat hydrate + primary event tail as the source of truth.
- Existing V2 authority concepts may be reused only to compute future `dispatch_blocked` reasons.
- Panic/fake runtime dispatchers should prove V3 create/list/hydrate/message/stream do not call containers.

## Relevant filepaths

- `docs/checkpoints/sessions-api-v2-primary-local-containers.md`
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/sessions_v2_primary.go`
- `swarmd/internal/api/sessions_v2_lifecycle.go`
- `swarmd/internal/api/runtime_sessions_v2.go`
- `swarmd/internal/api/run_stream_ws.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/session/service.go`
- `swarmd/internal/session/session_execution_v2.go`
- `swarmd/internal/store/pebble/session_store.go`
- `swarmd/internal/store/pebble/event_log.go`
- `swarmd/internal/store/pebble/keys.go`
- proposed `swarmd/internal/api/sessions_v3.go`
- proposed `swarmd/internal/api/sessions_v3_stream_ws.go`
- proposed `swarmd/internal/session/session_events.go`
- proposed `swarmd/internal/store/pebble/session_event_store.go`
- proposed `swarmd/internal/store/pebble/idempotency_store.go`

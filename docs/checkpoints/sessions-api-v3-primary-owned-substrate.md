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

## CP9+ factual checkpoint audit: V3 primary executor

Status: factual audit complete; implementation still open.

This section supersedes the earlier Stage 1 assumption that `pending_executor` could remain only a durable marker. The durable V3 session/event substrate is still the source of truth, but a complete conversation now requires an in-process primary executor that consumes committed V3 run intents and writes assistant output back through the same V3 mutation boundary.

### Proven facts from the current code

- No `pending_executor` consumer exists. Search hits for `pending_executor` / `RunIntentPendingExecutor` are limited to constants, the message handler, and tests; there is no `EnqueueRun`, `RecoverPendingRuns`, V3 executor service, or startup recovery hook.
- `POST /v3/sessions/{id}/messages` creates a `V3SessionRunIntent` at `swarmd/internal/api/sessions_v3_primary.go` lines 307-313 and commits it with `ApplySessionMutation` at lines 319-331. After commit it hydrates and returns at lines 340-359; it does not enqueue execution.
- `applySessionV3PrimaryMutation` publishes only the committed V3 event to the in-memory V3 stream hub (`swarmd/internal/api/sessions_v3_stream_ws.go` lines 286-294). It is not a scheduler signal today.
- The durable run-intent status vocabulary currently has only `pending_executor` and `dispatch_blocked` (`swarmd/internal/store/pebble/session_event_store.go` lines 25-26). There is no durable `running`, `completed`, `failed`, `cancelled`, or `interrupted` state yet.
- `V3SessionMutationRecordRunIntent` and `session.run_intent.recorded` exist, so run lifecycle transitions can be represented through the mutation boundary, but there is no atomic claim/precondition helper for `pending_executor -> running`.
- `KeyV3SessionRunIntentActive` is written when a run intent is recorded, but there is no read/list API for active run intent recovery and no account/global pending-run scan.
- V3 WebSocket replay/live delivery is correctly based on committed V3 session events. This is the right stream surface for assistant events, but only user/session/run-intent events are currently produced.
- Existing `run.Service.RunTurnStreaming` can invoke the model/runtime, but it currently appends its own user message and assistant message through the legacy session append path (`swarmd/internal/run/service.go` lines 974-979 and 1176-1189). A V3 executor must not call that path as-is unless it is refactored/adapted to avoid duplicating the already-committed user message and to write assistant output through `ApplySessionMutation`.
- Server startup (`swarmd/internal/api/server.go` lines 293-326) initializes the V3 stream hub but no V3 executor and no recovery scan.
- Current V3 tests prove create/list/hydrate/message/idempotency/replay/stream durability, but there are no V3 tests that assert model invocation, assistant message persistence, assistant stream events, duplicate enqueue safety, or restart recovery of pending runs.

### Updated checkpoint statuses and target points

| Checkpoint | Current status | Fact-based target points |
| --- | --- | --- |
| 1 — V3 durable session substrate | Complete / mostly complete | Keep the current `POST /v3/sessions`, list, hydrate, event projection, Pebble durability, idempotency, and ordered primary sequence behavior. Do not rewrite unless a targeted test disproves it. |
| 2 — V3 message append + run intent creation | Complete but not sufficient | Keep `POST /v3/sessions/{id}/messages` as a fast durable commit through `ApplySessionMutation`; it already records `pending_executor` or `dispatch_blocked` and publishes committed user events. Add executor signaling only after a successful commit. |
| 3 — V3 executor consumption of pending runs | Missing | Add a primary-owned in-process executor/scheduler with idempotent `EnqueueRun(ctx, accountID, sessionID, runIntentID)` and `RecoverPendingRuns(ctx)` responsibilities. Hook it from the post-commit message path for `pending_executor`; never enqueue `dispatch_blocked`. |
| 4 — Model/runtime invocation | Missing / adapter required | Build model input from hydrated V3 primary messages. Do not call Desktop dispatch, container dispatch, or V2 stream truth. Do not use `RunTurnStreaming` as-is if it would append a duplicate user message or write assistant output outside V3. Either factor a lower-level model turn runner or add a V3-safe runner adapter with V3 write callbacks. |
| 5 — Assistant message persistence | Missing | Persist final assistant output as a V3 `MessageSnapshot` with role `assistant` through `ApplySessionMutation`. Add replayable assistant lifecycle/output event types or explicit `EventType` payloads for start/delta/complete/failure as needed. |
| 6 — V3 WebSocket assistant streaming | Substrate complete; assistant streaming missing | Keep `/v3/sessions/{id}/stream` based on committed V3 events. Publish assistant events only after they are durably committed. Coalesce small deltas before Pebble writes while preserving low latency to the first assistant event. |
| 7 — Restart recovery | Missing | Add startup recovery that scans durable unfinished V3 run intents. Re-enqueue `pending_executor`. For stale `running`, choose and document a deterministic initial policy, preferably mark failed/interrupted unless a proven idempotent resume path exists. |
| 8 — Idempotency and concurrency | Partially complete | Existing mutation/idempotency/concurrent seq tests cover user writes. Add executor-level idempotency: duplicate message retry and duplicate enqueue must not double-generate; one active generation per session; deterministic executor idempotency keys tied to run intent ID. |
| 9 — Performance | Needs validation | `POST /messages` must return after durable commit/enqueue without waiting for model completion. Use bounded workers/queues, per-session locking or claim CAS, no hot polling, no unbounded goroutines, and coalesced delta commits. |

### Executor implementation attack points

1. **Store/service run lifecycle gaps**: add durable run statuses (`running`, `completed`, `failed`, and optionally `cancelled`/`interrupted`), a safe claim path for `pending_executor -> running`, and a pending/active run-intent scan API. Keep all writes behind `ApplySessionMutation` or a clearly equivalent V3 mutation method.
2. **Post-commit signal bus**: after `ApplySessionMutation` succeeds in the message handler, signal the executor for `pending_executor` results. The HTTP handler must not wait for completion and must not treat enqueue failure as message durability failure; enqueue/recovery must be idempotent.
3. **Executor worker model**: run bounded background workers, one active executor per session, and deterministic run-intent idempotency keys. Duplicate enqueue should exit after observing already-running/completed/failed state.
4. **V3-safe model invocation**: hydrate V3 messages and session/model preference from primary storage. Use or refactor the existing model runtime so it can generate assistant output without appending another user message and without writing assistant output to legacy/V2 truth.
5. **Assistant event persistence**: commit assistant start/delta/complete/failure events and final assistant message snapshots through V3 mutation calls. Use chunk coalescing by size/time to avoid one Pebble write per tiny token.
6. **Stream integration**: rely on `applySessionV3PrimaryMutation` publishing committed V3 events to the existing V3 stream hub. Do not wrap V2 `runStreamManager` or container streams.
7. **Recovery and stale runs**: initialize executor/recovery from server startup when sessions are configured. Recovery must make orphaned `pending_executor` records live again after daemon restart.
8. **Targeted validation only**: add focused tests for message-post enqueue, assistant persistence, assistant stream events, duplicate request/enqueue safety, restart recovery, dispatch-blocked no-execute, and async/performance sanity. Do not run broad repository-wide Go tests.

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

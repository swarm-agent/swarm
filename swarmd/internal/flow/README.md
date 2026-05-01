# Distributed Flows backend — checkpoint notes

Flows are target-owned scheduled jobs. The controller creates and syncs assignments, but a target swarm stores accepted revisions and runs its own scheduler. Once a target accepts a revision, controller downtime must not stop scheduled execution.

## Existing target/addressing contract

- `swarmd/internal/api/swarm_targets.go`
  - `swarmTargetsForRequestWithOptions` builds the target catalog from the local swarm, attached local container children, and remote deploy children.
  - `mapDeployContainerTarget` marks local child targets online/selectable only when `AttachStatus == "attached"` and `ChildBackendURL` is present.
  - `mapRemoteDeployTarget` marks remote targets online/selectable only when the remote deploy session is `attached` and `RemoteEndpoint` or `RemoteTailnetURL` is present.
  - Flow target resolution should reuse this catalog shape: `swarm_id`, `kind`, `deployment_id`, `backend_url`, `online`, `selectable`, and `last_error`.

- `swarmd/internal/api/swarm_proxy.go`
  - `proxyRequestToSwarmTarget` forwards HTTP/WebSocket requests to `target.BackendURL` and attaches peer auth headers.
  - `outgoingPeerAuthTokenForTarget` obtains the peer token via `swarm.OutgoingPeerAuthToken`.
  - Flow assignment delivery should reuse this peer-authenticated transport behavior; unreachable/dial errors should leave the command pending sync, not mark it installed.

- `swarmd/internal/api/routed_sessions.go`
  - `routedSessionTarget` resolves an existing session route to a child target.
  - `proxyRoutedSessionRequest` proxies session POSTs either by persisted session route or by current selected remote target.
  - `postPeerJSONToSwarmTarget` already sends peer-authenticated JSON POSTs to a target and is the best controller hook for idempotent Flow commands.
  - Existing peer endpoints under `/v1/swarm/peer/...` show the route namespace that target-local Flow assignment endpoints should use.

- `swarmd/internal/store/pebble/session_route_store.go`
  - `SessionRouteRecord` persists child swarm/backend URL for sessions mirrored on a target.
  - Flows should not depend on controller-owned session routes for scheduled execution; assignments must persist on the target instead.

## Remote deploy and offline semantics

- `swarmd/internal/remotedeploy/service.go` records `ChildSwarmID`, remote endpoints, status, and errors for remote sessions.
- `swarmd/internal/api/remote_deploy.go` exposes remote session create/start/update/approval paths but not a generic Flow transport.
- For Flows, remote deploy records should only feed target resolution/status. If a remote target is missing a backend URL, not attached, or dial fails, the controller outbox command remains pending and the UI shows pending sync/target unreachable.

## Target-local execution hook

- `swarmd/internal/run/service_background.go`
  - `RunRequest` includes `target_kind`, `target_name`, `background`, and `execution_context`.
  - `resolveRunTarget` maps `target_kind=background` plus `target_name` to the saved background profile.
  - Request-time `tool_scope` is rejected for targeted subagent/background runs; capabilities come from the saved profile contract.
  - `buildBackgroundRunMetadata` marks sessions as background and stores target labels.

- `swarmd/internal/api/run_stream_ws.go`
  - `handleRunStreamControl` supports HTTP background run starts and proxies to remote targets when appropriate.
  - Target-local Flow execution should launch through the target daemon's run service with `background=true`, `target_kind`/`target_name` from the accepted assignment, and no `tool_scope`.

## New checkpoint-1 interfaces

`swarmd/internal/flow/contracts.go` defines the implementation boundary:

- `TargetResolver`: list/resolve controller target selections using the existing swarm target catalog.
- `TargetStatusProvider`: report current online/unreachable state without converting unreachable into success.
- `FlowAssignmentTransport`: deliver idempotent `AssignmentCommand` values to a target.
- `FlowRunner`: target-local run hook that launches accepted assignments from target-local state.

Every assignment command is keyed by `flow_id`, `revision`, and `command_id`. Scheduled run claiming is keyed by `flow_id`, `revision`, and `scheduled_at`.

## Checkpoint 2 durable records and Pebble layout

`swarmd/internal/store/pebble/flow_store.go` now defines one local durable store for both controller-owned and target-owned Flow state. Controller-side records include desired definitions, target assignment status/ack state, pending outbox commands, and mirrored run summaries. Target-side records include accepted assignments, command ledger rows for idempotent apply, due-time rows, run claims, and run history rows.

Pebble key prefixes live in `swarmd/internal/store/pebble/keys.go`:

- `flow/definition/<flow_id>` for controller desired definitions.
- `flow/assignment_status/<flow_id>/<target_swarm_id>` for controller-visible target ack/pending status.
- `flow/outbox/<command_id>` plus `flow/outbox_status/<status>/<next_attempt_at>/<command_id>` for retryable controller delivery.
- `flow/mirrored_run/<flow_id>/<reverse_started_at>/<run_id>` for controller-side target run summaries.
- `flow_target/accepted/<flow_id>` for target accepted assignments.
- `flow_target/command_ledger/<flow_id>/<revision>/<command_id>` for idempotent target command application.
- `flow_target/due/<due_at>/<flow_id>/<revision>` for target-local due ordering.
- `flow_target/run_claim/<flow_id>/<revision>/<scheduled_at>` for duplicate-resistant scheduled run claims.
- `flow_target/run/<run_id>` and `flow_target/run_by_flow/<flow_id>/<reverse_started_at>/<run_id>` for target-local run history.

`swarmd/internal/store/pebble/flow_scheduler_store.go` adapts the Pebble Flow store to the target-local `flow.SchedulerStore` interface without changing run-time tool contracts.

## Checkpoint 3 scheduler core

`swarmd/internal/flow/schedule.go` validates schedule inputs and computes target-local next fire times from an explicit `now`. It supports daily, weekly, monthly, and on-demand cadences; IANA timezones; month-day clamping; disabled flows; DST spring gaps by selecting the first valid local wall time after the requested time; and default catch-up policy fields (`skip`, `once`, `all`).

`swarmd/internal/flow/scheduler.go` scans due rows through a store interface, creates a durable claim keyed by `flow_id + revision + scheduled_at`, calls `FlowRunner.RunAcceptedFlow` with the target's accepted assignment, deletes the consumed due row, and asks the store to schedule the next due time. Duplicate claims are treated as already-owned and do not launch another run.

Regression coverage:

- `swarmd/internal/store/pebble/flow_store_test.go` covers definition round-trip/list/delete, assignment status, outbox ordering and reindexing, accepted assignments, command ledger idempotency, due ordering, run claims, scheduler store adaptation, and run history ordering.
- `swarmd/internal/flow/schedule_test.go` covers daily/weekly/monthly/on-demand schedules, DST gaps/folds, catch-up policies, and duplicate-resistant scheduler ticks.

## Proposed API hooks for checkpoint 4

- Controller endpoint/service will enqueue commands, then call `postPeerJSONToSwarmTarget(ctx, target, "/v1/swarm/peer/flows/apply", command, &ack)`.
- Target endpoint `POST /v1/swarm/peer/flows/apply` will validate peer auth through the existing peer route middleware, check the command ledger, apply install/update/delete/run-now idempotently, and return `AssignmentAck`.
- Local transport registration must add the Flow peer endpoint to both `registerPeerRoutes` and `registerLocalTransportRoutes` in `swarmd/internal/api/server_routes.go` so local/child transports have the same behavior.

## Relevant filepaths

- `swarmd/internal/flow/contracts.go`
- `swarmd/internal/flow/schedule.go`
- `swarmd/internal/flow/scheduler.go`
- `swarmd/internal/api/swarm_targets.go`
- `swarmd/internal/api/swarm_proxy.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/run_stream_ws.go`
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/store/pebble/flow_store.go`
- `swarmd/internal/store/pebble/flow_scheduler_store.go`
- `swarmd/internal/store/pebble/session_route_store.go`
- `swarmd/internal/run/service_background.go`
- `swarmd/internal/remotedeploy/service.go`

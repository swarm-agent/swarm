# Plan-mode lifecycle API contract

## Goal

Replace overloaded plan-mode/session-transition controls with a backend-owned lifecycle API where each transition has exactly one endpoint, strict state validation, durable V3 mutations, and no client-side fallback orchestration.

This contract is limited to V3 session plan-mode lifecycle transitions. It does not redesign provider prompts, system-message wording, automatic checkpoint execution, or plan document editing/progress reporting.

## Retired overloaded transition inventory

The migration removes the overloaded user-facing lifecycle paths that motivated this contract:

1. `POST /v3/sessions/{id}/plans/execution` is not routed for lifecycle control.
   - Plan execution now uses explicit `/v3/sessions/{id}/plan-mode/...` endpoints only.
   - The old action-enum request shape, action alias normalizer, lifecycle switch dispatcher, and `next_action`/`run_request` response helper were removed from `swarmd/internal/api/sessions_v3_primary.go`.

2. The frontend no longer chains lifecycle responses into a second `/run/stream` request.
   - `web/src/features/desktop/session-v3/plan-execution-api.ts` posts directly to dedicated plan-mode endpoints.
   - Plan execution responses are treated as backend-owned lifecycle results with optional backend-created run intents.

3. `plan_manage` remains the agent plan document/progress/terminal outcome tool, not the user-facing lifecycle API.

4. `exit_plan_mode` remains the agent plan submission tool and should call the same backend lifecycle service as the dedicated API.

## Target namespace

All user-facing plan-mode lifecycle controls move under:

```text
/v3/sessions/{session_id}/plan-mode/...
```

The namespace is intentionally separate from:

- `/v3/sessions/{id}/plans...` for plan document CRUD/history only.
- `plan_manage` for agent-authored plan document edits/progress/terminal checkpoint outcomes only.
- `/v3/sessions/{id}/run/stream` for generic user message/run starts only, not plan lifecycle orchestration.

## Shared response shape

Every successful lifecycle endpoint returns:

```json
{
  "ok": true,
  "session_id": "...",
  "plan_id": "...",
  "transition": "explicit_transition_name",
  "plan": { "...": "SessionPlanSnapshot" },
  "execution_summary": { "...": "PlanExecutionSummary" },
  "session": { "...": "SessionSnapshot when mode changed" },
  "run_intent": { "...": "V3SessionRunIntent when backend started/stopped a run" },
  "mutation": { "...": "SessionMutationResult for session/run mutations" }
}
```

Responses must not include `next_action` or `run_request`. If a transition starts execution, the backend creates the run intent and starts/enqueues the run before returning.

## Shared failure shape

All failures use the existing API error envelope with a precise message. Required status codes:

- `400 Bad Request`: malformed JSON, unknown body field, missing required field, invalid enum value, invalid plan document, or state-specific validation failure where the client can fix the request.
- `401 Unauthorized`: missing/invalid principal.
- `403 Forbidden`: principal lacks write access to the session binding.
- `404 Not Found`: session, active plan, explicit plan, or checkpoint does not exist.
- `409 Conflict`: transition is not allowed from the current session/plan/run state, including active-run conflicts.
- `503 Service Unavailable`: daemon is shutting down.
- `500 Internal Server Error`: required backend service is not configured (`sessions`, lifecycle service, run service, run stream manager, executor, or realtime publisher).

No endpoint may convert a failed precondition into success.

## Endpoint contracts

### 1. Enter plan mode

`POST /v3/sessions/{id}/plan-mode/enter`

Body:

```json
{ "reason": "optional user-visible reason" }
```

Allowed source states:

- Session exists, principal has write access, and session mode is `auto`.
- No active V3 run intent is in `pending_executor` or `running` state.

Forbidden source states:

- Already in `plan`: return `409 Conflict`; no silent same-mode success.
- Any active run: return `409 Conflict` with the active `run_id` in error details.

Durable mutations:

- Apply one V3 session mutation through `ApplySessionMutation`/`ApplyV3SessionMutation` with kind `SessionMutationUpdateMode` and event type `session.mode.updated`.
- Set session mode to `plan`.
- Append the existing plan-mode reentry system message behavior unchanged.

Run-intent behavior:

- Never creates, starts, cancels, or completes a run intent.

Realtime events:

- Publish the committed `session.mode.updated` event and outbox record.

### 2. Submit plan from plan mode

`POST /v3/sessions/{id}/plan-mode/submit`

Body:

```json
{
  "plan_id": "optional existing plan id; defaults to active plan only when active exists",
  "title": "required unless supplied by document.title or existing plan",
  "plan": "optional markdown display text",
  "document": { "...": "required structured SessionPlanDocument unless existing structured plan is referenced" }
}
```

Allowed source states:

- Session mode is `plan`.
- Agent/profile policy allows leaving plan mode when called through `exit_plan_mode`.
- Request identifies a structured plan document either directly or by an existing active/explicit structured plan.

Required validation:

- No implicit new active plan when neither `plan_id` nor active plan exists and no document is supplied.
- Document must pass `ValidatePlanDocument`.
- `title` must resolve from body, document, or existing plan.

Durable mutations:

- Save/approve the plan with `status=approved`, `approval_state=approved`, and `UpdateKind=plan_mode_submit`.
- Apply one V3 session mode mutation from `plan` to `auto` using the same lifecycle service used by the public endpoint.
- Preserve current system-message behavior for auto-mode reentry.

Run-intent behavior:

- Does not start a checkpoint run by itself. Starting execution requires `approve-and-start`, `start`, or checkpoint `start/continue`.

Realtime events:

- Publish `session.plan.saved` and `session.mode.updated` in commit order.

`exit_plan_mode` tool requirement:

- The tool becomes a thin adapter over this lifecycle service method. It may perform permission approval plumbing, but it must not duplicate plan save or mode mutation logic.

### 3. Approve and start plan execution

`POST /v3/sessions/{id}/plan-mode/approve-and-start`

Body:

```json
{
  "plan_id": "optional; defaults to active plan",
  "continuation_policy": "optional: automatic | review_each_checkpoint; defaults to automatic"
}
```

Execution policy:

- Execution is always checkpointed; checkpoint boundaries are preserved.
- `continuation_policy` defaults to `automatic`.
- Set `continuation_policy=review_each_checkpoint` only when manual review after each checkpoint is required.

Allowed source states:

- Session mode is `auto`.
- Active/explicit structured plan exists and is not already complete, blocked, or failed.
- No active V3 run intent is pending/running.
- Plan is draft/pending or approved; endpoint approves it atomically.

Durable mutations:

- Apply `ApplyPlanAcceptanceExecutionPolicy`, preserving checkpoint boundaries and mapping the optional continuation policy to automatic or manual review.
- Save the plan with `status=approved`, `approval_state=approved`, `UpdateKind=approve_and_start`.
- Start the selected first checkpoint with `ApplyPlanCheckpointStart`.
- Create/enqueue the V3 run intent in the same backend lifecycle flow.

Run-intent behavior:

- Backend starts/enqueues the fresh-context run. The client must not call `/run/stream`.
- Response includes `run_intent` and the started `checkpoint_id`/`attempt_id` in structured fields, not a `run_request` instruction.

Realtime events:

- Publish plan saved/checkpoint-start state and run queued/started events through durable V3 mutation/outbox paths.

### 4. Start plan execution without changing approval

`POST /v3/sessions/{id}/plan-mode/start`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Plan is already approved.
- Execution state is `idle` or absent.
- At least one pending checkpoint exists.
- No active V3 run intent is pending/running.

Durable mutations:

- Start `execution_summary.next_checkpoint_id` with `ApplyPlanCheckpointStart`.
- Save plan with `UpdateKind=start_checkpoint`.
- Create/enqueue a fresh-context V3 run intent.

Error cases:

- `409 Conflict` if plan is draft/pending, waiting for review, already in progress, blocked, failed, or complete.

### 5. Pause automatic continuation

`POST /v3/sessions/{id}/plan-mode/execution/pause`

Body:

```json
{ "reason": "required non-empty reason" }
```

Allowed source states:

- Session mode is `auto`.
- Active plan exists and execution is `in_progress` or policy mode is `automatic` with remaining checkpoints.

Durable mutations:

- Change only `execution_policy.mode` from `automatic` to `review_each_checkpoint`.
- If an active run is already executing the current checkpoint, do not cancel it. The pause takes effect after the current checkpoint terminal outcome.
- Save plan with `UpdateKind=pause_automatic_execution`.

Run-intent behavior:

- Does not create or cancel a run intent.

Error cases:

- `409 Conflict` if policy is already `review_each_checkpoint` or plan is blocked/failed/complete.

### 6. Stop active plan execution

`POST /v3/sessions/{id}/plan-mode/execution/stop`

Body:

```json
{ "reason": "required non-empty reason" }
```

Allowed source states:

- Session mode is `auto`.
- Active plan exists.
- A V3 run intent for this session is `pending_executor` or `running`, or plan execution state is `in_progress`.

Durable mutations:

- If a run intent is active, cancel it through the V3 executor/cancel path and record a terminal run-intent mutation.
- Set plan execution state to `idle` while preserving the current checkpoint as restartable.
- Save plan with `UpdateKind=stop_plan_execution` and stop reason evidence.

Run-intent behavior:

- Cancels only the backend-owned active run intent. The body must not require client-supplied `target_swarm_id`.

Error cases:

- `409 Conflict` if no active execution exists.

### 7. Switch paused execution to automatic

`POST /v3/sessions/{id}/plan-mode/execution/automatic`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Active/explicit plan exists.
- No active V3 run intent is pending/running.
- Plan is not blocked, failed, or complete.

Durable mutations:

- Set `execution_policy.mode=automatic`; `execution_policy.shape` remains `checkpointed`.
- Save plan with `UpdateKind=set_automatic_mode`.

Run-intent behavior:

- Does not start a run. If the next checkpoint should run immediately, the UI must call the explicit start/continue endpoint.

### 8. Switch paused execution to checkpoint-by-checkpoint

`POST /v3/sessions/{id}/plan-mode/execution/checkpoint-by-checkpoint`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states and behavior match `/execution/automatic`, except the durable mutation sets `execution_policy.mode=review_each_checkpoint` and `UpdateKind=set_checkpoint_by_checkpoint_mode`.

### 9. Start checkpoint

`POST /v3/sessions/{id}/plan-mode/checkpoints/{checkpoint_id}/start`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Approved active/explicit plan exists.
- Checkpoint exists and is `pending`.
- It is the selected next checkpoint or the active checkpoint for a restartable idle execution.
- No active V3 run intent is pending/running.

Durable mutations:

- Apply `ApplyPlanCheckpointStart` for the path `checkpoint_id`.
- Save plan with `UpdateKind=start_checkpoint`.
- Backend creates/enqueues the fresh-context V3 run intent.

### 10. Continue checkpoint

`POST /v3/sessions/{id}/plan-mode/checkpoints/{checkpoint_id}/continue`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Plan is approved and not blocked/failed/complete.
- If plan was waiting for review, the checkpoint must already have been accepted through the accept endpoint.
- Path `checkpoint_id` equals `execution_summary.next_checkpoint_id`.
- No active V3 run intent is pending/running.

Durable mutations:

- Same as checkpoint start, but `UpdateKind=continue_checkpoint`.

### 11. Accept checkpoint review

`POST /v3/sessions/{id}/plan-mode/checkpoints/{checkpoint_id}/accept`

Body:

```json
{
  "result": "required review result text",
  "notes": "optional review notes"
}
```

Allowed source states:

- Session mode is `auto`.
- Active/explicit plan is in `waiting_review`.
- Path checkpoint exists and its review status is pending.
- No active V3 run intent is pending/running.

Durable mutations:

- Apply `ApplyPlanCheckpointReviewAcceptance` for the path checkpoint.
- Save plan with `UpdateKind=accept_checkpoint`.

Run-intent behavior:

- Does not start the next run. Automatic continuation after review acceptance requires an explicit `/continue` call or backend policy-owned auto-advance, never client-side `run_request` inspection.

### 12. Restart checkpoint from zero

`POST /v3/sessions/{id}/plan-mode/checkpoints/{checkpoint_id}/restart`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Active/explicit plan is approved.
- Checkpoint exists.
- No active V3 run intent is pending/running.

Durable mutations:

- Apply `ApplyPlanCheckpointReset` for only the selected checkpoint.
- Start that checkpoint with fresh context in the same backend transaction flow.
- Save plan with `UpdateKind=restart_checkpoint`.
- Backend creates/enqueues the fresh-context V3 run intent.

### 13. Rewind to checkpoint

`POST /v3/sessions/{id}/plan-mode/checkpoints/{checkpoint_id}/rewind`

Body:

```json
{ "plan_id": "optional; defaults to active plan" }
```

Allowed source states:

- Session mode is `auto`.
- Active/explicit plan is approved.
- Checkpoint exists.
- No active V3 run intent is pending/running.

Durable mutations:

- Apply `ApplyPlanCheckpointReset` with rewind semantics for the selected checkpoint and all later checkpoints.
- Start the selected checkpoint with fresh context in the same backend lifecycle flow.
- Save plan with `UpdateKind=rewind_to_checkpoint`.
- Backend creates/enqueues the fresh-context V3 run intent.

## Backend lifecycle service

Implement a dedicated lifecycle service used by both API handlers and `exit_plan_mode`.

Required properties:

- One public method per transition; no action enum method.
- Each method receives a typed request struct for that transition only.
- Each method validates the current session, active plan, plan document, checkpoint, execution state, and active run-intent state before saving any mutation.
- Methods that start a run preflight all required services before mutating plan state.
- Methods that mutate both plan and session/run state do so through V3 durable mutation paths and publish committed events only.
- The service is the only place that maps lifecycle transitions to plan execution helpers such as `ApplyPlanAcceptanceExecutionPolicy`, `ApplyPlanCheckpointStart`, `ApplyPlanCheckpointReviewAcceptance`, and `ApplyPlanCheckpointReset`.

## Frontend migration contract

Required frontend end state:

- Replace `executeDesktopPlanActionAndStartRun` with one function per endpoint or a typed route map without an `action` field.
- Remove `startDesktopPlanCheckpointRun` and all UI calls that inspect `next_action`/`run_request` to post `/run/stream`.
- Plan buttons call the dedicated endpoint directly:
  - Approve & Start → `/plan-mode/approve-and-start`.
  - Start → `/plan-mode/start`.
  - Continue → `/plan-mode/checkpoints/{id}/continue`.
  - Accept review → `/plan-mode/checkpoints/{id}/accept`.
  - Restart → `/plan-mode/checkpoints/{id}/restart`.
  - Rewind → `/plan-mode/checkpoints/{id}/rewind`.
  - Automatic/checkpoint-by-checkpoint toggle → `/plan-mode/execution/automatic` or `/plan-mode/execution/checkpoint-by-checkpoint`.
  - Pause/stop → `/plan-mode/execution/pause` or `/plan-mode/execution/stop`.
- The UI updates local plan cache only from returned plan snapshots and realtime replay/hydration.

## No-fallback invariants

The finished architecture must enforce all of the following:

- No lifecycle endpoint accepts an `action` string.
- No lifecycle endpoint accepts alias fields (`id` for `plan_id`, `active_checkpoint` for `checkpoint_id`, `mode` for continuation policy, etc.).
- No action normalizer and no generic switch-case dispatcher for lifecycle transitions.
- Approval/start always uses checkpointed execution and defaults continuation to `automatic`; manual review must be requested explicitly.
- No client-side run start after a lifecycle action.
- No `next_action`/`run_request` lifecycle orchestration hints in API responses.
- No silent success when a required state is missing or unchanged; invalid source states are conflicts.
- No fallback from active plan to a newly-created plan unless the endpoint contract explicitly creates one.
- No legacy route/proxy fallback for plan lifecycle execution; backend must start local V3 execution through the primary V3 lifecycle/run path or fail clearly.
- No prompt/system-message changes except preserving existing reentry and plan execution lifecycle messages through the new service.

## Desktop proposal review and Plan Agent sidecar

Approval-gated `exit_plan_mode` and plan proposal/revision/amendment/follow-up permissions render inline in the Desktop conversation. They use the shared structured-plan review projection: objective first, independently collapsed checkpoint rows, and tasks plus acceptance criteria on expansion. Ordinary tool permissions remain modal, and active execution lifecycle cards remain separate.

`POST /v3/sessions/{parent_session_id}/plan-review-sidecar` creates or replays a durable sidecar bound to the pending permission, plan ID, and revision. The sidecar uses the reserved `plan-review-agent` profile, is hidden from normal session navigation and agent selection, and has read/search/list access only; write, edit, shell, delegation, plan mutation, and permission-resolution tools are explicitly disabled. Its plan context is stored with the session binding. Plan Agent output is advisory: only the Desktop user's explicit **Send changes to Swarm** action denies the parent permission with the edited draft as its reason. Direct Approve and Deny controls remain on the inline card.

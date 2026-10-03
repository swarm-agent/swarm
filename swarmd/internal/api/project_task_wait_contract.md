# Project task waiting

`manage_projects action=wait_tasks` accepts `project_id` and 1–16 distinct
`task_ids`. It is available to the authenticated Project Orchestrator provider
run only; the run identity comes from trusted invocation context, not arguments.
Tasks must belong to that project/account and have a linked same-user execution
session. Registration pins the current attempt and session. It does not approve,
deploy, reopen, accept, or grant source access to a task.

## Lifecycle

- Registration crosses `ApplyV3SessionMutation`, storing `waiting_tasks` and the
  subscription on the owning run. The run ID and epoch bind the parent goal.
- The provider tool loop yields immediately. Executor cleanup releases the
  execution lease. Waiting is inactive, nonterminal, and not an elapsed-time
  reason to schedule provider work.
- Committed project/task publication reconciles matching waits. All selected
  attempts must be `needs_review` or `completed`, or one must have an actionable
  blocked, failed, cancelled, needs-input, pending-approval, removed/archived, or
  superseded outcome. Other rows retain their nonterminal status.
- A single canonical mutation retires the wait, appends a bounded system result
  identifying project/tasks/attempts/sessions, and creates a deterministic
  `pending_executor` continuation. `needs_review` means implementation-ready,
  never user-accepted completion. Outcome text is untrusted task data.
- Project locking serializes registration against outcome writes. Session locking
  and idempotency serialize duplicate wake claims against parent cancellation and
  new messages. A new user message cancels waiting or queued wake work atomically;
  it does not silently run the old goal. Once a wake has already begun execution,
  existing active-run admission/stop rules apply.
- Stop can cancel an inactive wait, including its exact continuation if outcome
  publication won the race. Archive retires waiting state; restore does not revive
  it. Reopening a task does not retarget an existing wait to a different attempt.
- Startup reconciles persisted waits, then the existing pending-executor backlog
  handles committed continuations. A process crash before publication is repaired
  from task and wait records. Running continuations retain the existing restart
  interruption semantics: provider side effects are not blindly replayed.

There is no polling provider, timeout wake, detached agent, or new job framework.
Callbacks accelerate durable delivery; the status index and task records recover
missed callbacks at restart. Store errors retain the wait/pending intent and are
reported; this does not claim recovery without storage becoming available.

## Authenticated task reports

`manage_projects action=report_task` accepts the linked `project_id`, `task_id`,
`update_kind` (`progress`, `attention`, `wake_request`), a 1–4000 UTF-8 byte
`summary`, and a stable `client_request_id`. Swarm, Coder and Finder task sessions
receive a session-bound reporting capability, not general project management.
Existing follow-up history access remains separately guarded.

The canonical V3 mutation rechecks the active task attempt and running provider
identity under project/account/session locks. It derives the parent from captured
session deployment lineage (or the retained run parent), never a supplied parent
ID or the mutable project primary pointer. Reports persist as `session.task.reported`
events with account, user, project, parent, task, attempt, source session/run and
exact event ID/sequence. Actual typed content determines retry conflicts.

Reports record intent only: progress is informational, attention marks actionable
input, and wake_request explicitly requests attention. `recorded`/`pending` is not
a delivery or wake receipt and does not approve scope, change task status or spawn
a run by itself. Eligible reports participate in the same wait reconciler as
completion/blocker outcomes; progress never wakes an inactive parent.
The V3 message boundary rejects task-originated messages to Orchestrator sessions,
including non-triggering notes; ordinary user chat and parent-to-task feedback
remain available. Timer tools and session messaging are not alternate wake paths.

## Safe delivery

The report event and parent pending index commit atomically. Deployment captures
its parent run generation, epoch and user-message fence; delayed arrival never
retargets an old task to a new goal. An explicit wait may select a retained task
attempt for that goal. Wake continuations inherit the generation, not a new goal.

Running parents read at most 16 reports (each at most 4000 summary bytes) at a
provider boundary. Reports enter context as explicitly untrusted result data,
not accepted scope or system instructions. A successful provider response commits
`session.task.delivered` and removes precisely those pending identities in one
mutation. Reading, queueing and wake allocation never acknowledge consumption.
Failed/cancelled steps retain reports. Final-step arrivals remain durable without
starting a second run. Receipts recheck principal, run, epoch, message fence,
archive state and active task attempt; changed/forged event bytes are rejected.

A crash after remote provider success but before the local receipt is inherently
ambiguous: pending data is retained and may be presented again on an authorized
retry with the same event identity. This is replay-safe durable delivery, not a
claim of exactly-once remote execution. Pre-existing runs without the new capture
fence fail closed; a new explicit user run is required rather than inferred
ownership or replay of an old goal.

## Validation

Deterministic local tests cover canonical storage, ownership, concurrent wake
claims, completion/registration races, Pebble reopen, failed atomic commits,
parent supersession/archive, attempt changes, provider-managed and native tool
loops, and no time-only provider scheduling. Desktop reducer/status tests keep
waiting separate from running/completed. These fixtures are code tests, not a
live provider benchmark or proof that an installed daemon has been rebuilt.

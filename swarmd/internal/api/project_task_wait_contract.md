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

## Validation

Deterministic local tests cover canonical storage, ownership, concurrent wake
claims, completion/registration races, Pebble reopen, failed atomic commits,
parent supersession/archive, attempt changes, provider-managed and native tool
loops, and no time-only provider scheduling. Desktop reducer/status tests keep
waiting separate from running/completed. These fixtures are code tests, not a
live provider benchmark or proof that an installed daemon has been rebuilt.

# Project task waiting

`manage_projects action=wait_tasks` accepts `project_id` and 1–16 distinct
`task_ids`. It is available to the authenticated Project Orchestrator provider
run only; the run identity comes from trusted invocation context, not arguments.
Tasks must belong to that project/account and have a linked same-user execution
session. Registration pins the current attempt and session. It does not approve,
deploy, reopen, accept, or grant source access to a task.

Optional `until` selects what the wait waits for and is stored with it:
`settled` (default) waits until each task's work has settled; `finished` keeps
the status-only rule (the agent finished). Any other value is refused without
changing the run.

## Lifecycle

- Registration crosses `ApplyV3SessionMutation`, storing `waiting_tasks` and the
  subscription on the owning run. The run ID and epoch bind the parent goal.
- The wait stores a one-line `notice` (`ProjectTaskWaitNotice`: "This
  conversation is now waiting on N task(s): <titles>. It wakes when they
  settle; a new message ends the wait."). The tool result returns it, and the
  `session.run.waiting_tasks` event carries it as top-level `notice` and in
  `run_intent.task_wait.notice`, so a client can show the person why the
  conversation waits even when the Orchestrator said nothing. It is
  information only, never an approval. Linked-machine titles are untrusted.
- The provider tool loop yields immediately. Executor cleanup releases the
  execution lease. Waiting is inactive, nonterminal, and not an elapsed-time
  reason to schedule provider work.
- Committed project/task publication (including workflow delivery and job-end
  evaluation changes, which publish `project.updated`) reconciles matching
  waits. All selected tasks must be ready, or one must have an actionable
  blocked, failed, cancelled, needs-input, pending-approval, removed/archived, or
  superseded outcome. Other rows retain their nonterminal status.
- Ready under `until=settled`: `needs_review` or `completed` with no workflow
  attached, or with its workflow ended (`workflow.state` done or none; failed
  or blocked, including out of rounds, are also actionable). A workflow that is
  running (any step, send-back and rebase rounds included) or not evaluated
  yet is not ready. The task progress the wait reads
  (`Server.taskWaitProgressFor`) reports `pending` while the task's latest end
  awaits the workflow runner: once marked, from the runner's evaluation record;
  while a send-back/rebase follow-up (named by the delivery's reopen step) has
  a live run intent in the task's session, from that durable record (a reopen
  keeps the task's attempt and its stored `needs_review`); otherwise only while
  the run end is being settled (`Server.settleTaskRunEnd`: status write, then
  job end signal) and an enabled workflow could match the task's folder, or
  that follow-up's end is not marked yet. That window is in memory; closing
  it without a mark publishes `project.updated` so waits are re-checked. An
  end that is never marked (no signal store, a failed signal write, a crash, a
  restart) is never inferred pending. Ready under `until=finished`: `needs_review` or
  `completed`, whatever the workflow.
- Rows carry `workflow_state`, `workflow`, `workflow_step`/`workflow_steps`,
  `workflow_step_type`, `workflow_round` and `integration_state` when the task
  has them.
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
  it. Under `until=settled` a reopen of the same task (a workflow send-back or
  rebase, a follow-up) keeps the wait on that task: the row follows the new
  attempt when its session is still the owner's delegated session for the task
  (`attempt_id`/`session_id` name it, `pinned_attempt_id` the one selected).
  A follow-up attempt reserved before its session exists is still in progress
  (neither ready nor superseded); ownership is checked once the session exists.
  Under `until=finished` a different active attempt is `superseded`.
- Startup reconciles persisted waits, then the existing pending-executor backlog
  handles committed continuations. A process crash before publication is repaired
  from task and wait records. Running continuations retain the existing restart
  interruption semantics: provider side effects are not blindly replayed.

There is no polling provider, timeout wake, detached agent, or new job framework.
Callbacks accelerate durable delivery; the status index and task records recover
missed callbacks at restart. Store errors retain the wait/pending intent and are
reported; this does not claim recovery without storage becoming available.

## Linked machines

With `machine` (a linked machine name), `project_id` and `task_ids` name that
machine's project and tasks. Only the home's Orchestrator may call it (not a
task session), under the same run context, ownership and run/epoch fences; the
linked work rule applies as for other linked actions that send no task (`off`
refuses; `ask` and `auto` run it). Registration reads each task through the link (`get_task`), pins its
active attempt and session, and refuses archived tasks or tasks without a
delegated attempt. The wait records `machine`; the home stores no copy of the
remote tasks.

- Outcomes come only from the machine. One home watcher per waiting run follows
  the machine's `/v3/activity/stream` through the link with the fleet feed's
  reader (`fleet_activity.go`). On (re)connect, a gap, or an activity record for
  the wait's project or a pinned session, it reads the pinned tasks again with
  the link's `get_task` and offers those observations to the store wake. The
  machine's feed carries its `project.updated` records as metadata-only
  activity (`kind`, `project_id`, `state=changed`) for an explicit user of the
  project's account (not an application gateway): every task write, workflow
  delivery transition and job-end mark or evaluation commits one, so a workflow
  that ends with a run step, as blocked or failed, or with nothing matched is
  read even though no session on the machine records it. No timer reads tasks. Local task publication never wakes a linked wait.
- The wake applies the local readiness rules to the observations, including the
  workflow and integration progress the machine's `get_task` reports (a
  reopened attempt is followed under `until=settled` and `superseded` under
  `until=finished`; a missing or archived task is `cancelled`). A machine
  older than this feed sends no `project.updated` activity: there a workflow
  end with no session activity is read only on the next activity, gap or
  reconnect. Every
  row names `machine`, `task_id`, `attempt_id` and `session_id`; the message is
  marked `untrusted_content` and says the outcomes came from that machine. Wake,
  retirement, continuation identity and supersession (new person message, stop,
  archive) are the same as a local wait.
- If the machine has had no successful contact for a grace period while the
  home is trying (`remoteTaskWaitGraceDefault`), every unresolved row becomes
  `machine_unreachable` with the last contact time. If its work rule is turned
  off or it is unlinked (re-read from the home's own records on a heartbeat
  interval), unresolved rows become `linked_work_off`. Both are actionable.
- The executor's yield starts the watcher; after a restart, linked waits resume
  from `Daemon.Run` (`ResumeLinkedTaskWaits`) once the server is fully
  configured, never from a setter. These two bounded timers are the only
  timers; neither schedules provider work.

## Authenticated task reports

`manage_projects action=report_task` accepts the linked `project_id`, `task_id`,
`update_kind` (`progress`, `attention`, `wake_request`), a 1–4000 UTF-8 byte
`summary`, and a stable `client_request_id`. Swarm, Coder and Finder task sessions
receive a session-bound reporting capability, not general project management.
Existing follow-up history access remains separately guarded.

The canonical V3 mutation rechecks the active task attempt and running provider
identity under project/account/session locks. It derives the parent from captured
task admission/reopen origin, captured from authenticated current provider context
and copied into session deployment lineage (legacy tasks retain their captured
session/run parent). Canonical project conversations do not maintain the legacy
primary pointer. Neither a supplied parent ID nor that mutable pointer is used.
HTTP-created tasks without a captured conversation cannot infer a report destination;
an authenticated Orchestrator reopen captures the new attempt's origin.
Reports persist as `session.task.reported`
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

## Desktop presentation and future goal consumers

Desktop derives report cards from canonical session events: task sessions show
recorded reports without claiming delivery; parent conversations show consumed
reports from `session.task.delivered`. Stable source event identities suppress
replay duplicates. These cards use escaped plain text, not executable Markdown
or tool definitions. Outcome system messages remain intact for provider context
but render as bounded title/status/summary cards, never raw JSON bubbles. Unknown
or malformed outcome data produces a neutral notice rather than a payload dump.

The run intent owns the lifecycle label: waiting is inactive; a queued task
continuation says resuming, and a running continuation says resumed. A historical
outcome card never claims the current run is active. Event-only receipts invalidate
the chat selection through the existing canonical cache; there is no new polling.
Only loaded canonical event history is displayed; no synthetic delivery receipt
is inferred from a report, a wake or an assistant response.

Future goals must reuse captured parent generation/epoch/message fences, exact
attempt selection, the existing wait reconciler and provider-boundary receipts.
A report is data, not objective approval; delivery is successful consumption,
not incorporation. Preserve pending data on failure and the remote-ack ambiguity
above. Do not add a goal-specific messaging, timer or concurrent wake executor.

## Validation

Deterministic local tests cover canonical storage, ownership, concurrent wake
claims, completion/registration races, Pebble reopen, failed atomic commits,
parent supersession/archive, attempt changes, provider-managed and native tool
loops, and no time-only provider scheduling. Desktop reducer/status tests keep
waiting separate from running/completed. These fixtures are code tests, not a
live provider benchmark or proof that an installed daemon has been rebuilt.

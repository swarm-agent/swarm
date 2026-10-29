# Project task watching

`manage_projects action=wait_tasks` (alias `watch_tasks`) accepts `project_id`
and 1–16 distinct `task_ids`. It watches the tasks in the background: it records
a durable watch on the conversation and returns at once (`status: watching`,
`watch_id`, and a `notice` telling the AI to reply to the person and end its
turn). It never changes the run intent and never ends or restarts the turn. It is available to the authenticated Project Orchestrator provider
run only; the run identity comes from trusted invocation context, not arguments.
Tasks must belong to that project/account and have a linked same-user execution
session. Registration pins the current attempt and session. It does not approve,
deploy, reopen, accept, or grant source access to a task.

`cancel_watch` (or `wait_tasks` with `cancel: true`) ends the conversation's
watches on `task_ids` (all of them in the project when omitted); a cancel that
matches nothing is refused.

Optional `until` selects what the watch waits for and is stored with it:
`settled` (default) waits until each task's work has settled; `finished` keeps
the status-only rule (the agent finished). Any other value is refused without
changing the run.

## Lifecycle

- Registration crosses `ApplyV3SessionMutation` from the running run (run ID
  and epoch fences as before) and writes a `V3SessionTaskWatch` record
  (`v3/task_watch/<session>/<watch>`) plus the visible start line in one
  batch: a system message with metadata `source: task_wait_notice`
  (`task_wait_notice: watching`, `task_watch_id`, `task_count`, `task_titles`,
  `machine` for a linked watch) reading "Watching N tasks[ on machine]:
  titles." The event (`session.task_watch.started`) carries the line as
  `notice` and the conversation's watches after it as `task_watches`. The run
  intent is untouched: the conversation stays working, then idle.
- Tasks the conversation already watches (same machine and project) are not
  watched again: a call naming only watched tasks records nothing and answers
  `already_watching`; a call adding new tasks watches only those. A repeated
  call in the same run replays its registration. No task is ever delivered
  twice.
- Committed project/task publication (including workflow delivery and job-end
  evaluation changes, which publish `project.updated`), startup, and the end of
  every run of the conversation (`sessionV3Executor.finish` →
  `Server.deliverSessionTaskWatches`) reconcile watches. All selected tasks must
  be ready, or one must have an actionable blocked, failed, cancelled,
  needs-input, pending-approval, removed/archived or superseded outcome. Other
  rows retain their nonterminal status.
- Ready under `until=settled`: `needs_review` or `completed` with no workflow
  attached, or with its workflow ended (`workflow.state` done or none; failed
  or blocked, including out of rounds, are also actionable). A workflow that is
  running (any step, send-back and rebase rounds included) or not evaluated
  yet is not ready. The task progress the watch reads
  (`Server.taskWaitProgressFor`) reports `pending` while the task's latest end
  awaits the workflow runner (see the progress rules in `task_progress.go`).
  Ready under `until=finished`: `needs_review` or `completed`, whatever the
  workflow.
- Rows carry `workflow_state`, `workflow`, `workflow_step`/`workflow_steps`,
  `workflow_step_type`, `workflow_round` and `integration_state` when the task
  has them.
- A ready watch fires in one canonical mutation (`session.run.tasks_ready`)
  through the session wake (`session_wake.go`, the path background command
  reports take too): it deletes the watch and appends the bounded
  `Delegated project task outcomes` message (role user, metadata source
  `task_watch`). Under the conversation's lock the store decides: with a run
  active (`pending_executor` or `running`, e.g. the person is talking) it is a
  note pinned to that run (`feedback_note`, `wake_note_run_id`) with a durable
  wake note, read at the run's next step and re-delivered as a new turn if the
  run ends without reading it (also after a restart); with none it starts a
  new pending run (`SessionWakeRunID`). Nothing is dropped.
- Milestones (default on, `milestones=false` off): when not settled, a task
  reaching review started, sent back (round N), landed, blocked or failed
  wakes the conversation with `Delegated project task milestones` and keeps
  the watch (`last_milestones`, `deliveries`). At most one milestone wake per
  watch per `ProjectTaskWatchMilestoneInterval` (a minute): a held milestone
  answers `*ProjectTaskWatchMilestoneDue` and the daemon checks again at its
  due time, coalescing what changed. Settling is never held.
- A watch is not ready while a succeeded delivery has a step without its
  result (the progress keeps the workflow running) or while the derived
  `task_state` is running; rows carry `task_state`/`task_state_reason`. A "Tasks settled: titles." end line
  follows the commit (idempotent per watch; none for an archived
  conversation). `needs_review` means implementation-ready, never user-accepted
  completion. Outcome text is untrusted task data.
- A person's message never ends a watch. A watch ends when it fires, on the
  AI's cancel, on the person's stop (`POST /v3/sessions/{id}/task-watches/stop`,
  optional `watch_id`/`task_ids`; "Stopped watching: titles.", `ended_by:
  stop`), or when the conversation is archived or deleted.
  `GET /v3/sessions/{id}/task-watches` lists them; the run state served by
  sync snapshots carries them as `task_watches`.
- Under `until=settled` a reopen of the same task (a workflow send-back or
  rebase, a follow-up) keeps the watch on that task: the row follows the new
  attempt when its session is still the owner's delegated session for the task
  (`attempt_id`/`session_id` name it, `pinned_attempt_id` the one selected). A
  follow-up attempt reserved before its session exists is still in progress;
  ownership is checked once the session exists. Under `until=finished` a
  different active attempt is `superseded`.
- Watches survive restarts. At startup, runs left in `waiting_tasks` by the
  retired pending wait are converted to watches and ended `completed`
  (`MigrateProjectTaskWaitsToWatches`), so no conversation stays pending; then
  watches are reconciled and the pending-executor backlog handles fired
  continuations.

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

- Outcomes come only from the machine. One home follower per linked watch follows
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
  deferral while a run is active, the outcome turn and the ways a watch ends are
  the same as a local watch; the executor's end of a run kicks the follower to
  check again.
- If the machine has had no successful contact for a grace period while the
  home is trying (`remoteTaskWaitGraceDefault`), every unresolved row becomes
  `machine_unreachable` with the last contact time. If its work rule is turned
  off or it is unlinked (re-read from the home's own records on a heartbeat
  interval), unresolved rows become `linked_work_off`. Both are actionable.
- The tool starts the follower when it records the watch; after a restart,
  linked watches resume from `Daemon.Run` (`ResumeLinkedTaskWaits`) once the
  server is fully configured, never from a setter. These two bounded timers are the only
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
a run by itself; they are read by the parent's running provider loop. A watch
fires on task status, not on reports; progress never wakes an inactive parent.
The V3 message boundary rejects task-originated messages to Orchestrator sessions,
including non-triggering notes; ordinary user chat and parent-to-task feedback
remain available. Timer tools and session messaging are not alternate wake paths.

## Safe delivery

The report event and parent pending index commit atomically. Deployment captures
its parent run generation, epoch and user-message fence; delayed arrival never
retargets an old task to a new goal. A watch's outcome turn is a new run with
its own generation.

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

The run intent owns the lifecycle label (idle or working); task watches show
beside it, never as a pending state. A queued outcome turn says resuming, and a
running one says resumed. A historical
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
loops, and no time-only provider scheduling. Watch tests cover registration
without a run change, idempotent re-watch, cancel and stop, deferral during an
active run with delivery after it, person messages, linked machines and the
startup migration. These fixtures are code tests, not a
live provider benchmark or proof that an installed daemon has been rebuilt.

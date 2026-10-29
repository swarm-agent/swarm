# Task progress: what happened to a task, as one tracked state

A task is a durable record. When an agent finishes it, what happens next is up
to the project's workflows: a workflow that applies takes the work through its
steps (a reviewer, sending it back, merging or delivering it); with no workflow
the work stays on its branch, not integrated, until the person tells the
Orchestrator what to do. No person reviews or approves finished work; the only
person's gate is `Start tasks: approve` (see `access-model.md`), which holds a
task before it starts.

Clients and the Orchestrator need one answer per task to "where is it and what
happened": Swarm computes it from its own records and keeps it current. Nothing
here is a new decision point; it is a view of the task, its attempts, its
workflow deliveries and its integration.

## `progress` on every task

`GET /v3/projects/{id}/tasks` and `GET /v3/projects/{id}/tasks/{task}` carry
`progress` on each task:

```json
{
  "state": "workflow",
  "summary": "review and merge · step 2 of 3: reviewer · round 1",
  "workflow": {
    "state": "running",
    "deliveries": [
      {"id": "jdel_…", "workflow": "review and merge", "state": "running",
       "step": 2, "steps": 3, "step_type": "agent", "round": 1}
    ]
  },
  "integration": {"state": "not_integrated", "branch": "agent/…", "target": "dev",
                  "commits": 1, "commit": "", "at": 0},
  "history": [
    {"at": 1793000000000, "what": "started"},
    {"at": 1793000420000, "what": "finished · 1 commit on agent/…"},
    {"at": 1793000421000, "what": "workflow review and merge started"}
  ],
  "updated_at": 1793000421000
}
```

### `state`, one of

| state | meaning |
|---|---|
| `waiting` | not started: held by `Start tasks: approve`, or queued for capacity; or its run stopped on the provider's usage limit and it continues by itself at the reset (task status `limit_waiting`, `summary` says until when) |
| `working` | an agent is working on it (planning or running, a follow-up included) |
| `workflow` | it finished and a workflow is taking it through its steps now, or a workflow sent it back to its agent (`summary`: "workflow <name> sent back by <step> · round <n>: <reason>") and the follow-up has not started yet |
| `not_integrated` | it finished with commits that are not in its target branch, and no workflow is running on it (none applied, or the ones that ran did not merge it) |
| `integrated` | its commits are in its target branch (`integration.commit`, `integration.at`) |
| `delivered` | a workflow delivered its result (a folder, a pull request, the outbox, or the owner's script after the review, e.g. a push), or a media task's files are ready |
| `done` | it finished with nothing to integrate (a report, an audit, no commits) |
| `failed` | its last attempt failed, or a workflow ended failed or blocked; `summary` says why |
| `stopped` | the person stopped its run (`stop_task`, or the stop in its session); task status `stopped`, not a failure, and `message_task` resumes it |

`summary` is one short line for a client to show as is.

### `outcome` and `stale`

`outcome` is what happened to the task's work, read from its latest workflow
delivery: `running <step>`, `sent_back (round n)`, `pushed <head12>` (a run
step after the review, such as the owner's push script), `delivered <target>`,
`blocked <reason>` or `failed <step>`; `workflow pending` while the task's end
is not evaluated yet. With no workflow it is `integrated`, `delivered <where>`
or, once the agent finished, `finished` when its state is `done` (nothing to
integrate, the same answer as `get_task`) and `ready to integrate` otherwise;
absent before then. A stopped task's outcome is `stopped`.

A task list page (`GET /v3/projects/{id}/tasks` with list parameters, and
`manage_projects list_tasks` here and on linked machines) counts its tasks by
outcome in `outcome_counts`, every bucket present: `done` (pushed, delivered,
integrated, finished, completed), `in_progress` (working, running, sent back,
workflow pending), `waiting` (queued for capacity),
`needs_attention` (the person must act: a blocked or failed status, waiting
for approval or an answer; or finished within the last 7 days with work still
to hand over: ready to integrate, or a workflow that blocked or failed),
`idle` (the same work to hand over, but finished more than 7 days ago: nobody
is waiting on it) and `stopped` (stopped or stale). A pushed, delivered,
integrated, finished (nothing to integrate) or stopped task never needs
attention, nor does an archived task or a task a workflow step started (a
reviewer run: its work and outcome are counted on the task it serves). The
route keeps its raw `status_counts`; agent lists do not carry them.

The same rule (`pebblestore.ProjectTaskNeedsAttention`) answers per task:
`progress.needs_attention` on the task routes, `needs_attention: true` on a
`list_tasks` row (here and on linked machines) and `needs_attention` in
`get_task`. A media or design task whose files are ready has outcome
`delivered files ready`.

`get_task` also shows the last 3 messages sent to the task (`message_task`
notes and follow-ups), the first 80 characters of each, with `state`
`queued` (with the run it waits for), `delivered` (with the run whose model
received it) or `reopened` (with the follow-up run). The task route answers
them as `messages`.

`stale` is `true` on an `in_progress` or `blocked` task whose session has had
no active run, and which has not changed, for 24 hours. `idle` is `true` on a
finished task whose progress has not changed for 7 days.

### The task's state: `task_state`

A task record's `status` is the status of its agent **run**, not of the task:
`needs_review` only means the agent's run ended. It says nothing about whether
the work was pushed, sent back or is still on its branch. Every surface that
shows a task shows one derived word instead, from one rule
(`pebblestore.ProjectTaskState`, over the task's `status`, `outcome`, `stale`
and `idle`, using the same bucket as the list counts):

- `progress.task_state` and `progress.task_state_reason` on the task routes
  (and so on MCP `swarm_list_tasks`);
- `state` first on every Orchestrator `list_tasks` row and `state` plus
  `state_reason` in `get_task`, here and on linked machines, where the raw
  field is named `run_status`;
- the Orchestrator's per-turn "Recent Project Tasks & Deliverables" prompt
  block. That block is a snapshot from the start of the turn. Read live with
  `list_tasks` before reporting status.

| state | meaning | produced by | count bucket |
|---|---|---|---|
| `running` | an agent or workflow still works on it | run status planning/in_progress/running/…; or outcome `running <step>`, `sent_back (round n)`, `workflow pending` | `in_progress` |
| `waiting` | queued for capacity, or waiting for the provider's usage limit to reset | run status `queued`/`pending`, or `limit_waiting` (reason: "waiting for the provider's limit to reset (until …): provider …; continues by itself"; linked-machine `list_tasks` rows add it as `state_reason`, in the machine's own words when it gives them, unless a live or stall reason is already there) | `waiting` |
| `needs_you` | the person must act | run status `pending_approval`, `needs_input`, `failed`, `blocked` (and other failures); outcome `blocked …` (including sent back until its rounds ran out) or `failed …` | `needs_attention` |
| `landed` | the work was handed over | outcome `pushed <sha>`, `delivered <target>`, `integrated` (reason names the SHA or target) | `done` |
| `done` | finished with nothing to hand over | outcome `finished`; run status `completed`, `rejected`, `superseded`; an archived task or a workflow step's own task | `done` |
| `not_integrated` | finished with commits never handed over | outcome `ready to integrate` (reason names the branch); a blocked/failed workflow or unknown outcome once idle | `needs_attention` within 7 days, `idle` after |
| `stopped` | stopped by the person, or no active run for a day | run status `stopped`, outcome `stopped`, or `stale` | `stopped` |

**A working run that is not moving.** A run status of `running` does not say
the run moves: nothing writes the task record while its run waits. So the
progress also reads the run's live records on the task's machine
(`progress.live`, `pebblestore.ProjectTaskStateLive` and
`ProjectTaskBucketLive`, applied only while the run status says the agent
works):

| live | `task_state` | `task_state_reason` | count bucket |
|---|---|---|---|
| a pending permission or question in the task's session (not a client tool's call) | `needs_you` | `waiting for approval (permission <id>): <tool>: <argument summary>`, or `waiting for an answer (permission <id>): ask_user: <question>`; `(+n more)` when several wait | `needs_attention` |
| an update drain holds a run that was admitted but has not started (or waits for admission) | `waiting` | `held: update drain since <RFC 3339 time>` | `waiting` |
| the run waits in the account's execution admission queue | `waiting` | `held: execution admission (<active> of <limit> runs active, <n> waiting)` | `waiting` |

`progress.live.request` carries the oldest pending request: `permission_id`,
`session_id`, `kind` (`permission` or `question`), `tool`, `summary` (the
command, question, path or compact arguments, at most 120 characters, from the
stored sanitized arguments) and `requested_at`. `get_task` shows it as
`pending_request` with `next_action: resolve_task_permission`; `list_tasks`
rows add `state_reason` and the request. A linked machine's `list_tasks` and
`get_task` carry its own `task_state`, reason and request through unchanged.
Answering it is `manage_projects action=resolve_task_permission` (below).

`landed` requires a handover: a workflow step that pushed (`pushed <sha>`) or
delivered (`delivered <target>`), or the commits being in the target branch
(`integrated`). A failed run status wins over an earlier push (`needs_you`). A
`landed` or `done` task is never counted `needs_attention`.

#### Waiting for a provider's usage limit

When a task's run (its first run or a follow-up in its session) ends on the
provider's usage limit (a Claude Code session or weekly limit, or a provider
429 or quota), the task is not failed. Its status is `limit_waiting` and its
`task_state` is `waiting`, with a reason naming the provider and the reset
time. The reset is read from the message when it states one ("resets 1:50pm
(UTC)" waits until 13:51 UTC). Otherwise the task backs off for 5 minutes,
doubling to at most an hour. The record carries `limit_waits`, `limit_at`,
`limit_retry_at`, `limit_run_id` and `limit_reason`. One `task.limit_wait`
signal (a warning; refs `project_id`, `task_id`, `session_id`, `run_id`;
attrs `retry_at`, `job_id`, `role`) is recorded when the wait starts. A
continuation that hits the limit again does not record another.

At the reset, the same task session continues by itself in its retained
worktree. Its message tells the agent that its run stopped on a usage limit
at that time, and to check `git status`, finish what it was doing and
commit. Uncommitted edits stay where they are. The continuation's run id is
`<run>.limit<n>`, and its end is the job end of the run it continues. The
wait is durable: after a restart it continues at its reset time, not at
startup. After 12 waits on one task (the same bound as workflow steps and
worker runs), the task fails with "the provider's limit was still in effect
after 12 waits for its reset: …". `stop_task`, or archiving the task
(the archive route or `manage_projects` `archive_task`; the archive commit
itself ends the wait), cancels the wait: the task is `stopped` and `message_task` resumes it. A
message the person sends in the task's own session continues it at once.
Reviewer tasks and worker jobs keep their own limit handling (see
[automations](automations.md)).

`get_task`'s `progress.stage` follows the state, never an implementation step
once the work is finished: `landed` is `Landed`, `done` is `Done`,
`not_integrated` is `Finished, not handed over`, and `needs_you` after the
workflow finished or the work was delivered is `Needs you`. A stage reads
`Landed` only when the state is `landed`; a workflow that is `done` without a
handover is not landed.

Tasks are created with placeholder deliverables whose status is `pending`:
the code deliverable (`deliv_code`), reports (`deliv_audit`, `deliv_plan`),
design artifacts (`deliv_design`) and the default `deliv_task`. Nothing
updates those placeholders, so for every non-media deliverable the task state
is the authority. One rule (`pebblestore.ProjectTaskDeliverableStatus`) shows
a deliverable still carrying `pending` (or no status) from the state:

| state | deliverable status shown |
|---|---|
| `running` | `in progress` |
| `waiting` | `not started` |
| `done` | `ready` |
| `landed` | `delivered` |
| `not_integrated` | `on branch` |
| `needs_you` | `needs you` |
| `stopped` | `stopped` |

Media deliverables (kind `image`, `video`, `audio`/`sound`, or any deliverable
with a media URL) keep their stored status, which the media pipeline keeps in
step with generation. A deliverable whose status was set explicitly (`ready`,
`accepted`, …) also keeps it. The rule applies to `get_task` (the compact
`result.deliverables` entries read `title (status)`, `detail=files`, and
`detail=full` on the task and its attempts), the task detail and list routes,
and the Orchestrator's prompt block. Only the answer changes; the stored
record keeps its placeholder.

### `workflow.state`, one of

- `none`: the task ended and Swarm found no enabled workflow that applies to
  it. Recorded when the job end is evaluated, so it is never confused with
  "not evaluated yet".
- `pending`: the task ended and its job end has not been evaluated yet. A
  run that leaves the task working (a Swarm task waiting on the tasks it
  delegated) is not a job end and marks nothing.
- `running`, `done`, `failed`, `blocked`, `sent_back`: the state of the
  deliveries that applied (the latest round per workflow). `sent_back`: a
  reopen step sent the work back (review findings, a failed push to rebase)
  and nothing was handed over; each such delivery carries `sent_back`
  (`step`, `round`, `reason`).
- absent while the task has not ended.

### `history`

What happened, oldest first, from the records Swarm already keeps: attempts
(started, finished, failed, follow-ups), workflow delivery steps (started,
finished, sent back, merged, delivered, failed), integration (merged into the
target at a commit), Start tasks approval. Bounded (the most recent 50 entries);
times are Unix ms.

## Stalled tasks

An unfinished task either makes progress, recovers by itself, or produces
exactly one alert that names its state. swarmd checks every unarchived agent
task once a minute (and once at start). The check reads only Swarm's own
records: the task, its session's run state and last event, its workflow
deliveries, pending approvals and background commands. It never calls a
provider. It reads a task's worktree with Git only for the uncommitted case
below: at most once per stall episode and 20 tasks per check, and only for
finished tasks that changed in the last 24 hours.

A task is **not** stalled while it has an armed wait: a run asleep on
`wait_tasks` (the task watch wakes it), a background command still running,
a running or waiting workflow step, or a provider-limit wait (a workflow
step's, or the task's own `limit_waiting`) until its reset plus
`stall_after_minutes`. It **is** stalled when:

- it is unfinished with no active run, no queued run, no workflow step and no
  armed wait, and nothing changed for `stall_after_minutes` (limits; default
  20). This covers a pending approval (a tool's, or Start tasks) and a queued
  run held by an update drain or an admission refusal;
- or an active run has had no new event for `run_silent_minutes` (default
  30), with no tool call in flight, no background command and no approval
  waiting.

Two known shapes recover by themselves, each once:

- **Review exhausted.** The review loop used its automatic rounds and the
  reviewer still blocks. The reviewer's last findings go into the task's own
  session as a new turn: one extra round. If that round also ends blocked,
  the findings go to whoever delegated the task, stating that review is
  exhausted.
- **Ended uncommitted.** The run ended with uncommitted changes in its
  worktree and nothing is committed, so no review or next step can start.
  One automatic turn in the same session asks the agent to finish, run the
  tests and commit, or say why not. This happens once per head: never two
  nudges for the same head, also after a restart.

Every other stall, and a recovery that did not work, raises **one** signal
per task per stall episode: kind `task.stalled`, severity warning, dedup key
`task.stalled:<task>:<episode>`, refs `project_id`, `task_id`, `session_id`,
attrs `stall`, `machine`, `title` and `since`. The summary names the task,
the machine and the precise state:

| `stall` | Summary says |
| --- | --- |
| `approval` | waiting on an approval for TOOL since T (or on Start tasks approval) |
| `held` | held by an update drain since T, or by admission (its reason) |
| `provider_limit` | waiting on a provider limit until T (passed) |
| `review_exhausted` | review exhausted, after one extra round |
| `ended_uncommitted` | ended uncommitted, after one commit nudge |
| `no_next_step` | no run and no next step for N min |
| `run_silent` | a run with no events for N min |

The signal reaches swarm-watch (and its Telegram relay) through the signal
feed. The same text goes into the Orchestrator conversation that delegated
the task (`origin_session_id`) as a message: it starts a turn there, joins a
running turn as a note, or wakes a `wait_tasks` wait. While the task is
stalled, `progress.task_state` keeps its word and `task_state_reason` reads
`stalled: …`. This holds on the task routes, `get_task`, `list_tasks`, the
Orchestrator's task block and linked machines' task lists. When the task
moves again (any change to the task, its run or its workflow), swarmd emits
`task.unstalled` (severity info) and the episode ends; a later stall is a
new episode with its own one alert.

Each step is keyed durably by the episode (the signal's dedup key, the
wake's request id, the recovery receipts), so a restart, including one
between the signal and the wake, sends nothing twice.

## Live

Every change to any input of `progress` (task status, an attempt, a workflow
delivery step, an integration) publishes `project.updated` for the task's
project, so clients read the task again; nothing is polled.

## The Orchestrator

`wait_tasks` watches tasks in the background: it records a durable watch on
the conversation and returns at once, so the Orchestrator answers the person
and ends its turn normally. The conversation is never pending: it shows as
idle, or working while a run is active. When the watched tasks settle (or one
needs input) Swarm starts a new turn with each task's `progress` in the
`Delegated project task outcomes` message; if a run is active then (the person
is talking), the outcomes are delivered right after that run ends. A person's
message never ends a watch. A watch ends when it fires, when the Orchestrator
cancels it (`cancel_watch`, or `wait_tasks` with `cancel: true`), when the
person stops it (`POST /v3/sessions/{id}/task-watches/stop`), or when the
conversation is archived or deleted. Calling `wait_tasks` again on a watched
task changes nothing. Watches survive restarts; runs left pending by the
retired wait are converted to watches at startup. The run state's
`task_watches` (sync snapshot) and `GET /v3/sessions/{id}/task-watches` list a
conversation's active watches.

A watch also fires when a selected task's run starts waiting on a person (live
`needs_you` with a request): its outcome row carries `task_state`,
`task_state_reason` and `pending_request`. On a linked machine the session's
`permission.summary.updated` event reaches the home through the machine's
activity stream (as metadata: kind, session, `state: needs_you`, never the
request's text), and the home then reads the task with `get_task`; nothing is
polled. The Orchestrator tells the person what is asked and answers with
`resolve_task_permission` (`project_id`, `task_id`, `permission_id`,
`decision` `approve` or `deny`, optional `reason`, which is also a
question's answer; `machine` for linked work) only on their word or under a
rule that allows it. Each answer covers that one request; no lasting rule is
saved. See docs/access-model.md for its rule and confinement.

When a task it delegated ends `not_integrated` with workflow state `none`, it tells the
person in the conversation that the work is on its branch and not integrated,
and does nothing more with it until the person says what they want.

While the Orchestrator watches tasks, the conversation itself says so,
whatever the Orchestrator writes. Starting a watch records a system message
(metadata `source: task_wait_notice`, `task_wait_notice: watching`, with
`task_watch_id`, `task_count`, `task_titles` and, for a linked machine,
`machine`): "Watching 2 tasks on forge: Fix login, Add docs." Calling
`wait_tasks` again on the same tasks records nothing. When the watch fires
Swarm records "Tasks settled: Fix login, Add docs." (`task_wait_notice:
ended`, `ended_by: tasks`); a cancel or stop records "Stopped watching: …"
(`ended_by` `agent` or `stop`). They are session history, so a reload shows
them. They are for the person: Swarm never sends them to the model. Clients
show them as a compact status line and a `👁 Watching · N tasks` badge beside
Idle/Working.

## Clients

A session shows the tasks it started (and a task session its own task) in a
tray pinned above the message box: one line with the counts per state, the open
tasks (failed, not integrated, workflow, working, waiting; most recently changed
first) at most about five rows with the rest behind "+ N more", and the finished
ones (integrated, delivered, done) folded into one line. Each row shows the
task's `summary`; opening it shows its `history`. A client offers no review,
approve or merge controls for finished work: the person tells the Orchestrator.

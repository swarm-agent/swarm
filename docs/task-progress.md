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
| `waiting` | not started: held by `Start tasks: approve`, or queued for capacity |
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
| `waiting` | queued for capacity | run status `queued`/`pending` | `waiting` |
| `needs_you` | the person must act | run status `pending_approval`, `needs_input`, `failed`, `blocked` (and other failures); outcome `blocked …` (including sent back until its rounds ran out) or `failed …` | `needs_attention` |
| `landed` | the work was handed over | outcome `pushed <sha>`, `delivered <target>`, `integrated` (reason names the SHA or target) | `done` |
| `done` | finished with nothing to hand over | outcome `finished`; run status `completed`, `rejected`, `superseded`; an archived task or a workflow step's own task | `done` |
| `not_integrated` | finished with commits never handed over | outcome `ready to integrate` (reason names the branch); a blocked/failed workflow or unknown outcome once idle | `needs_attention` within 7 days, `idle` after |
| `stopped` | stopped by the person, or no active run for a day | run status `stopped`, outcome `stopped`, or `stale` | `stopped` |

`landed` requires a handover: a workflow step that pushed (`pushed <sha>`) or
delivered (`delivered <target>`), or the commits being in the target branch
(`integrated`). A failed run status wins over an earlier push (`needs_you`). A
`landed` or `done` task is never counted `needs_attention`.

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

## Live

Every change to any input of `progress` (task status, an attempt, a workflow
delivery step, an integration) publishes `project.updated` for the task's
project, so clients read the task again; nothing is polled.

## The Orchestrator

`wait_tasks` resumes the Orchestrator with each task's `progress`. When a task
it delegated ends `not_integrated` with workflow state `none`, it tells the
person in the conversation that the work is on its branch and not integrated,
and does nothing more with it until the person says what they want.

## Clients

A session shows the tasks it started (and a task session its own task) in a
tray pinned above the message box: one line with the counts per state, the open
tasks (failed, not integrated, workflow, working, waiting; most recently changed
first) at most about five rows with the rest behind "+ N more", and the finished
ones (integrated, delivered, done) folded into one line. Each row shows the
task's `summary`; opening it shows its `history`. A client offers no review,
approve or merge controls for finished work: the person tells the Orchestrator.

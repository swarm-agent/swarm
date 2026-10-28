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

`summary` is one short line for a client to show as is.

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

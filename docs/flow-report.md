# Flow report

The flow report shows where a job's time and tokens went: for one task (or
the worker job it carries), and for a project over a period. Use it to see
what wasted time and tokens in a loop and to decide what to fix next. You do
not need to read raw session events.

The report is computed on request from durable records only:

- the task's sessions' run records (`V3SessionRunIntent`: start, end, status,
  blocked reason);
- their tool and permission events (`session.tool.*`, `permission.requested`,
  `permission.updated`);
- each run's usage record (`SessionTurnUsageSnapshot`);
- the job's workflow deliveries (`task/<project>/<task>`, and
  `worker/<worker>/<run>` when the task carries a worker run);
- the messages sent to the task.

Nothing is stored, sampled or estimated. The same records always give the
same report (`jobautomation.BuildFlowReport`).

## Where to read it

| Surface | Call |
|---|---|
| HTTP | `GET /v3/projects/{project}/tasks/{task}/flow` → `{flow, markdown}` |
| HTTP | `GET /v3/projects/{project}/flow?period=today\|7d\|30d` → `{flow, markdown}` |
| Orchestrator and Swarm Control | `manage_projects` `get_task` with `detail` `flow` → `flow`, `flow_markdown` |
| Orchestrator and Swarm Control | `manage_projects` `flow` with `project_id`, `period` → `flow`, `flow_markdown` |

Both routes need `projects:read` or `sessions:read`. Application gateway
tokens are refused. A task's report reads only sessions of the caller's
account. A task follow-up session cannot read a project's flow. `markdown`
and `flow_markdown` are the same short text, which a client (the GPUI
console, the web UI) shows as it is.

## Time by phase

Every millisecond between the job's first and last activity counts in
exactly one phase. While the job is unfinished, the span runs until now.
Where phases overlap, the first one listed below wins:

| Phase | From |
|---|---|
| `provider_limit` | A run ended on a provider usage, rate or session limit, until the next run of its session started. A workflow agent step's limit waits count too, until the stated reset or the step's next attempt. |
| `needs_you` | A permission or question waited for the person (`permission.requested` to `permission.updated`). A permission still asked counts to now while the task is unfinished. A task in `pending_approval`, `needs_input` or `waiting_on_approval` with no open permission read counts from its last settled activity, even while its run is open. |
| `testing` | A command tool call that runs tests: `go test`, `npm`/`pnpm`/`yarn`/`bun test`, `vitest`, `jest`, `pytest`, `cargo test`, `make test`, `run-critical-tests.sh` and the like (`jobautomation.FlowTestCommand`). |
| `reviewing` | A workflow agent step whose worker is the reviewer (or that recorded a verdict), and runs of a reviewer task. |
| `rebasing` | The first task run after a delivery was sent back only because dev moved. |
| `gate` | A workflow `run` or `deliver` step. |
| `coding` | Any other time inside a run. |
| `stalled` | No run and no workflow step ran. |

## Per run

Each run has:

- its phase, status and duration;
- tool calls by kind: read, edit, command, commit or other (`jobautomation.ToolKind`, the same classifier `agents_at_work` uses);
- the test commands run and their time;
- `tool_result_bytes`: the bytes its tool results added to context;
- input, output and cache tokens, and its provider requests;
- `context_tokens_per_request`: (input + cache read + cache write tokens) / requests.

`first_request_context_tokens` is `null` for now. The usage record is per
run, so the size of the first provider request is not recorded yet. It will
be filled from the per-request receipts ("a receipt per provider request")
once they are on dev. The TODO is in `jobautomation.FlowRunReport`.

## Per delivery

Each workflow delivery has:

- its round and head;
- its reviews and their verdicts;
- its gates (run steps with state and exit code);
- its rebases, its provider-limit waits and its explicit re-runs;
- its `cause`: why the round did not land, from the reasons the delivery recorded (`jobautomation.FlowDeliveryCause`).

| Cause | When |
|---|---|
| `dev_moved` | The push needed a rebase after a passing review: the reopen was a rebase reopen (`IsRebaseReopen`), a failed step said "rebase needed", or the rebase rounds ran out. |
| `code` | A review blocked (`review_block`), or the review rounds ran out. |
| `gate` | A run or deliver step failed for another reason (`check_failed`, `step_error`, `push_failed` without a rebase). |
| `provider` | A provider limit ended a step. |

A delivery that landed or still runs has no cause.

## Top waste

The report lists at most five lines that name the largest costs with
numbers, for example:

- `72 min waiting on provider limit`
- `18 min stalled (no run and no next step)`
- `15 min running tests (23% of agent time)`
- `context re-read grew from 120k to 680k per call`
- `7 full reviews of the same head abc123def456`
- `tests run 19 times in one run`
- `3 rebases because dev moved (40 min)`
- `5 hand interventions` (messages sent to the task, and explicit workflow re-runs)

Lines are ranked by their share of the job's time. For context growth, the
share is the tokens re-read above the smallest run's context per request, as
a share of all input tokens, and token shares count half against time.
Counts with no time of their own rank last.

## Project roll-up

`period` is `today` (from 00:00 UTC), `7d` (the default) or `30d`. The
roll-up takes the project's tasks updated within the period, at most 200, and
bounds each task's report to the period: phases are clipped to it, and only
the runs and deliveries active in it count, each whole (a run's tokens and
tool calls, a delivery's reviews, rebases and re-runs), with messages sent
within it. It then sums their phases, tokens, reviews and rebases. It lists each task with its time and its top waste line,
and ranks the top waste across them. Context growth and repeated tests are
compared only within one task and name that task. `partial` is set when the
task, event, run or message bounds were reached.

## Bounds

Each session's first 20,000 tool and permission events, 1,000 runs and
100,000 messages are read. Runs are read in store key order, not time order,
so past the run bound which runs are missing is not chosen by time. When any
bound is reached, the report says so in `partial` and `notes`.

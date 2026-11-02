# Workflows

When work ends, Swarm records one **result** for it and runs the **steps the
person (or their AI) configured** for that end. Work is a task of Swarm's own
agents (`manage_projects` `propose_task`: swarm, coder, finder, designer; its first run and each
follow-up sent into its session) or a worker job (a worker run or follow-up, a
media job, a design job). Swarm only provides
primitives and reports what happened; it has no built-in merge, check,
review chain or push. The one thing it does by itself is keep a delivery's
work on top of its target branch: when the target moved, Swarm rebases the
work and resolves conflicts with an agent (see [Rebasing](#rebasing)); a
workflow may turn that off with `rebase: "off"`.

With no workflow nothing changes: a task or job ends with its branch and
worktree, and the person integrates it by hand (`manage_projects` `integrate_task`,
`manage_jobs` `deliver_job`, or their own git).

### When Swarm removes a finished job's worktree

A task's or job's worktree (its checkout of the folder on its `agent/…`
branch) stays while it can still be needed. Swarm removes it (`git worktree
remove` and its Git metadata) and **keeps the branch** once all of these
hold:

- the run or task is finished: succeeded, failed or cancelled (a task:
  completed, failed, cancelled, needs review, accepted, rejected or
  superseded). Admitted, queued, preparing, running and waiting runs, a task
  waiting on a provider limit, and a stopped task that stays open keep it;
- its `HEAD` is already in the folder's target branch (the branch its work
  lands on), merged by Swarm or outside it;
- it has no uncommitted or untracked changes, is not the folder itself, and
  no other run, review or unfinished task works in it.

Swarm checks right after it lands the work itself (`manage_projects` `integrate_task`, a
`merge` deliver step, `manage_jobs` `deliver_job`), and in a sweep every 15 minutes
while the daemon schedules workers, which also finds work merged outside
Swarm (for example a pull request merged on the forge) and older finished
worktrees (it reads every session's state, so an active session never goes
unseen). A worktree kept for any reason is looked at again an hour later.
When the daemon stops, a sweep stops after the worktree it is on; the rest
wait for the next pass.

Each removal is recorded on the run and the task as `worktree_removal`
(`state` `removed`, `path`, `branch`, `head`, `target_branch`, `trigger`
`landed` or `sweep`, `at`) and raised once as a `worktree.removed` signal
(info; refs `session_id` and `run_id`/`worker_id` or `project_id`/`task_id`;
attrs `path`, `branch`, `head`, `target_branch`, `trigger`). The run's result,
its commits and its branch stay readable: `manage_projects` `get_task_changes`, the task and
job views, the task diff and a fixing reviewer's result read the commits,
files and head from the kept branch in the folder (`checkout_note` says so).
A run whose follow-up is queued or running keeps its worktree. A removal
right after a landing waits at most 10 seconds for another evaluation;
otherwise it leaves the worktree to the sweep. A removal that fails keeps the
worktree, is recorded with `state` `failed`, `attempts`, `last_error` and
`next_attempt_at`, is retried with a backoff from 10 minutes (at most five
attempts), and is reported as a `worktree.remove_failed` signal (warning) on
the first and last attempt. Anything that needs the checkout again (a
follow-up message to the job, a task reopen, a reviewer reading the job in
place) recreates it at the same path from the kept branch, as long as the
branch is still at the recorded `head`; a branch moved since is refused
rather than checked out. A follow-up, reopen, in-place review or a message
to the job's or task's session (`POST /v3/sessions/{id}/messages`, for
example a person continuing a finished Coder task) always reserves the
worktree before its run is admitted, waiting for any removal being decided at
that moment, and recreates it if that removal went through. A reserved
worktree, removed before or not, is not removed for 30 minutes, so the work
has time to start. A message the task refuses anyway (a completed, rejected
or integrated task, which must be reopened) reserves and recreates nothing.
A recreated one is recorded as `worktree_removal` `state`
`restored`. A repeated
`manage_projects` `integrate_task` of such a task answers `already_integrated` from the
record, and the task's delivery state reads `integrated` (`reason_code`
`worktree_removed`). Both hold only when the recorded `head` has at least one
commit beyond the task's base. A task with no commits whose worktree was
removed is never reported as integrated.

Workflows were called job automations. The MCP tools and the API path are
named for workflows (`manage_workflows`, `/v3/workflows`); the internal
names are unchanged: the `automation.*` signals, the `automations:read` and
`automations:write` scopes, `then` and `automation: true` on the job tools.

## Triggers

The HTTP workflow save API accepts an optional `on` alongside the existing
scope and steps:

```json
{"event":{"source":"pull","url":"https://example.com/posts","every_seconds":60,"items":"posts","id":"id"},"start":{"worker":"worker-id","brief":"Revise {{event.text}} using {{event.notes}}"}}
```

`start.worker` must be active and deployed, not a reviewer or a media/design
worker. The folder must belong to exactly one project. Jobs use that worker's
model settings and the saving owner's authority. Only jobs started by this
workflow trigger match its steps. Missing template fields become empty strings;
objects and arrays become compact JSON.

Alternatively, use exactly one schedule source instead of `event`:

```json
{"schedule":{"cron":"0 9 * * *","timezone":"America/New_York"},"start":{"worker":"worker-id","brief":"Plan {{inputs.brand}} for {{schedule.date}} at {{schedule.time}}"}}
```

Cron has five fields, including ranges, lists and names; timezone is IANA and
defaults to UTC. `schedule.date` is local `YYYY-MM-DD` and `schedule.time` is
local `HH:mm`. Each due minute starts the existing worker, without copying it.
A slot while its previous job is running is skipped, as are missed slots after
restart (no replay). Recent `events` record started, skipped or failed slots;
reads include `next_run_at` as Unix milliseconds for enabled schedules. After
restart a skipped receipt summarizes the oldest pending slot, not every missed
minute. Nonexistent local minutes do not run; repeated DST minutes may each run.
If the daemon exits during dispatch before recording its job ID, it retries the
reserved dispatch with its original idempotency key and brief to recover the job
ID before allowing later slots, even if the source is changed to an event trigger.
Recovery errors retain the reservation for retry and mark its receipt failed with
an error and a failure signal. If the original worker or folder is unavailable,
restore that prerequisite; editing the trigger does not release an uncertain
start. Queued event items are retired when the trigger definition changes.

With `on`, `steps` may be empty: a trigger-only workflow just starts jobs.
Trigger-started jobs match the starting workflow's own steps, or ordinary
job-end workflows only when that starting workflow has no steps; `for_each`
jobs continue to match ordinary job-end workflows.

Pull uses HTTPS GET, a 10-second timeout, a 1 MiB response limit, and an interval
of 30–86400 seconds with error backoff. The first successful feed after a trigger
change establishes a baseline without starting jobs. An optional `secret` names
a granted Swarm secret whose allowed hosts include the URL host; it is sent as a
Bearer credential. Redirects are refused rather than forwarding credentials.

For webhook ingress use `event: {"source":"webhook","id":"id"}`. Saving returns
`trigger_key` once (hex encoding of 32 random bytes). Decode that hex before
computing HMAC-SHA256 over the exact request body and send
`X-Swarm-Trigger-Signature: sha256=<hex digest>` to
`POST /v3/workflows/{id}/events` on the existing daemon listener. No public
listener is added. `rotate_key: true` on save replaces the key. Bodies are one
item or `{"items":[...]}`, limited to 64 KiB and one accepted request per second.
Keys are sealed internally, never included in workflow reads or folder grants.

Both sources retain the last 2000 IDs and queue events durably in feed order,
starting at most one job at a time per workflow. Failed starts are recorded and
not retried. Workflow reads include recent `events`, without their item payloads.
The signals are `workflow.trigger.fired` and `workflow.trigger.failed`.
`manage_workflows` includes `on` in its tool schema. Proposal acceptance remains required for Orchestrator-proposed workflows;
a new webhook key is returned only to the person's client on acceptance, not to
the Orchestrator. Explicit key rotation currently requires the HTTP save or MCP path.

## What an automation is

An automation belongs to an account and has:

| Field | Meaning |
| --- | --- |
| `name` | Unique per account; setting an existing name updates it. |
| `scope.folder` | The project folder whose task and job ends it watches (absolute path). |
| `scope.role` | Optional: only the tasks this one of Swarm's own agents (`swarm`, `coder`, `finder`, `designer`) finished. Not with `scope.worker` or `scope.schedule`. |
| `scope.worker` | Optional: only this worker's jobs (worker id). |
| `scope.job` | Optional: only this task or job (`task/<project>/<task>`, `worker/<worker>/<run>`, `media/<project>/<task>`, `design/<project>/<task>`). |
| `scope.schedule` | Optional: only the jobs of this worker schedule. |
| `when` | `succeeded` (default), `failed` (failed or cancelled) or `always`. |
| `enabled` | Default true. |
| `retries` | 0 to 3 (default 0): automatic retries of a failed step, with backoff. |
| `result` | Optional: what the work must hand over (`kind`, `path`, `describe`); see [What the work hands over](#what-the-work-hands-over-and-where). |
| `steps` | Ordered steps, at most 40 (`jobautomation.MaxSteps`). |

Every update raises the automation's `revision`. An automation applies only to
task and job ends after it was created. Tasks and jobs started by a workflow
(an `agent` step) never trigger workflows; a follow-up of the original work
ends again and does. A task's follow-up is `manage_projects` `message_task`
(queued into its running run, or reopening it in its own session and worktree
when it has finished) or the person's own message in the task's session; a
job's is `manage_jobs` `message_job`. `manage_sessions`
`send_message` is for plain sessions only and refuses to start a run in a
task's session.

A task's run ending is its job end only when the run leaves the task with a
result: `needs_review`, `completed`, `failed`, `blocked` or `cancelled`. A run
that leaves the task `in_progress` or `queued` (a Swarm task that watches the
tasks it delegated with `manage_projects` `wait_tasks` and ends its turn) ends nothing,
whatever the run's own status. The later run that finishes the task is the job
end, named by its run id as a follow-up.

Each step has an `id`, a `type` (`run`, `agent`, `reopen` or `deliver`),
`timeout_seconds` (default 600, at most 3600), `continue_on_failure` (default
false: a failed step stops the steps after it, which are recorded as
`skipped`) and an optional `if`.

### Conditions (`if`)

`if` is a list of conditions on earlier steps that must all hold for the step
to run:

```json
"if": [{ "step": "review", "verdict": "block" }, { "step": "check", "status": "succeeded" }]
```

- `{"step": "<id>", "status": "succeeded|failed|error"}`: that step's state. A
  step that did not run (skipped or stopped) matches none. `failed` is a
  script that ran and exited non-zero (or an agent step that failed);
  `error` is a `run` step whose script **never started** (its `error_kind`:
  `checkout`, `environment`, `lease`, `script_missing`, `script_unusable`,
  `start_timeout`) or a host script Swarm saw killed from outside the step
  (`interrupted`), an infrastructure problem that `failed` never matches,
  so a step that fixes a failed check is not sent when the check could not
  run. `status: error` is only for a `run` step.
- `{"step": "<id>", "verdict": "ok|block"}`: for an `agent` step, the verdict
  of the review the job it started recorded. A job that recorded no review has
  no verdict, and a missing verdict never matches.
- `output_contains` (only with `status`): literal, case-sensitive text, one
  line, at most 200 bytes, that the step's reason, output or saved log tail
  (its last 64 KiB) must contain. It is checked when the workflow is saved and
  matched as a plain substring, with no patterns.

A gate that fails because the target branch moved says so in its output
(for example `GATE FAILED: rebase needed: ...`). Send only that failure back
for a rebase, and let a code failure take another path:

```json
{ "id": "gate-fix", "type": "run", "command": "deliver/gate.sh", "continue_on_failure": true },
{ "id": "rebase-after-fix", "type": "reopen", "max_rounds": 2,
  "message": "The gate needs a rebase onto the target branch: {{steps.gate-fix.output}}",
  "if": [{ "step": "gate-fix", "status": "failed", "output_contains": "rebase needed" }] }
```

A reopen step on a failed step whose `output_contains` names a rebase (holds
`rebase` in any case) is a rebase reopen: its `reopen_cause` is `push_failed`
("rebase needed (round N)"), its failure names the matched text, and it
counts against the rebase rounds (`RebaseMaxRounds`), not the review rounds.
A step that could not run (`status: error`) stays `step_error`.

A step whose conditions do not hold is recorded `skipped` with the reason (not
failed), and the steps after it still run. There is no expression language.

### `run` steps

```json
{ "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh", "args": ["--draft"] }
```

A `run` step is what the machine's owner does with a job's result after the
job has ended: merge it locally, push it, upload a file. It is the owner's
own script, not an agent command, so it runs **on the host as the daemon's
user**, the same way the publish command does: with the folder as working
directory, the machine's own credentials (the daemon user's SSH keys, Git
credential helper, `~/.netrc`) and write access to the folder's repository.
Swarm ships, installs and suggests no script: it runs only the steps the
owner configured, and nothing when there are none.

- **Only the machine's owner may set a `run` step**: the local owner (the
  Desktop or `swarm` on the machine, with no key), or a key with the `admin`
  scope (a `full` AI key). A `write` AI key, a remote device and any other
  key without `admin` are refused when they save an automation, or a `then`
  on `manage_jobs` `give_job` or `manage_jobs` `schedule_job`, that contains a `run` step
  (HTTP 403, or a tool error), and nothing is stored or started. They may
  still save `agent` and `reopen` steps, and `run` steps with
  `"in": "environment"` (below), which run sandboxed and never on the host.
  The root-owned check below does not
  limit what a step can do on its own: every system interpreter is root-owned,
  and `args` are free-form, so `{"command": "/bin/sh", "args": ["-c", "..."]}`
  passes it. Saving a `run` step is running a command as the daemon's user,
  so it needs the owner.
- `command` is an **absolute path**, or a path **relative to the folder**
  (no `..`, not under `.git`). Swarm resolves it, symlinks included, and runs
  it only when the final file is **owned by root and not writable by group
  or others** (and executable and readable); otherwise the step is refused
  with the reason and the fix, and nothing runs. Install a script with:

  ```sh
  sudo install -o root -m 755 push.sh /usr/local/libexec/swarm-deliver/push.sh
  ```

- **Prefer an absolute path in a directory only root can write**, such as
  `/usr/local/libexec/swarm-deliver/`. An absolute path may point anywhere,
  symlinks included: where it points is the owner's choice.
- A **relative** command must resolve to a file **inside the folder**. A
  symlink in the folder (the script, or a directory on its path) that leads
  out of the folder is refused, even to a root-owned file such as `/bin/sh`.
  The script must also have exactly one link: a hard link is refused. These
  rules exist because whoever can write the folder (the folder's user, an
  agent working in it, or a job commit your merge script checks out) can
  replace `deliver/push.sh`. They still cannot make a file pass the root-owner
  check. They can, however, delete or rename a script in the folder, or put
  another root-owned script from the same folder in its place. A script that
  holds credentials or must not be swapped belongs outside the folder.
- The check is made again immediately before the script runs, on the opened
  file itself. Swarm then runs a private copy of exactly that file (in a
  folder only the daemon's user can read, removed afterwards), so a file
  swapped in at the path after the check never runs. The script's `$0` is
  therefore that copy's path, not the path you installed it at: do not find
  sibling files through `$0`.
- `run` steps have no `secrets`: a step that sends them is refused. A host
  script uses the machine's own credentials, as the publish command does.
- Before the step, Swarm brings the delivery's head commit (the job's head,
  or the head an earlier `agent` step moved it to) into the folder as
  `refs/swarm/jobs/<run-id>`.
- The script gets the job's result JSON (below) on **stdin**, and `args` as
  its arguments.
- Environment: only `PATH` (`/usr/local/bin:/usr/bin:/bin`), `HOME` (the
  daemon user's), `TMPDIR` (a scratch folder of its own, removed when the
  step ends) and these:

  ```sh
  SWARM_JOB_ID=worker/<worker>/<run>
  SWARM_JOB_STATUS=succeeded          # or failed, cancelled
  SWARM_HEAD=<sha>                    # the delivery's head
  SWARM_BASE=<sha>
  SWARM_BRANCH=agent/...
  SWARM_JOB_REF=refs/swarm/jobs/<run-id>
  SWARM_DELIVERY_ID=<delivery id>
  SWARM_ROUND=0
  ```

  The git values are empty when the job has no commits. Nothing else of the
  daemon's environment is passed in.
- Time limit: `timeout_seconds`. Past it, or when the daemon stops, Swarm
  kills the script's whole process group (the script and every process it
  started). A timed-out step fails with exit code 124 and a last line saying
  it timed out.
- Output: stdout and stderr together; the last 64 KiB are kept as the step's
  log tail and the **last non-empty line** is the step's `output` (for
  example a pull request URL). The exit code is the outcome: a non-zero exit
  fails the step.

#### Checks in the folder's environment (`"in": "environment"`)

```json
{ "id": "console", "type": "run", "in": "environment", "command": "scripts/check-console.sh", "continue_on_failure": true }
```

With `"in": "environment"` a `run` step runs a script **from the job's head
commit**, sandboxed in a lease of the folder's environment (the image and
setup the person's AI defined with `manage_projects` `update_environment`,
for example with Go, Node and Rust). Nothing is installed on the host, so an
AI owner can add a check (say, building `console/` before review) without
sudo. `in` defaults to `host` (the step above); `in` on any other step type
is refused.

- `command` is the script's path **relative to the repository root** in the
  job's head (`scripts/check-console.sh`), with optional `args`. An absolute
  path, `..`, `./` or a path under `.git` is refused when the workflow is
  saved. When the step runs, a script missing from the head
  (`error_kind` `script_missing`), one that is a directory, link or
  submodule, and one committed without its executable bit
  (`git update-index --chmod=+x`; both `script_unusable`) end the step
  `error` with the reason; nothing runs.
- The step takes a lease of the folder's environment, the same lease agents'
  bash uses: the same image, isolation (gVisor where the machine runs it),
  lease user and groups as an agent working in one of the folder's
  worktrees (so it reads every object those agents wrote into the folder's
  repository), network policy and
  granted secret stand-ins, and no other host credential. In it the folder
  and its worktrees are mounted **read-only**. The step works in a fresh,
  writable clone of the folder's repository in the lease's own home, checked
  out detached at exactly the delivery's head (`SWARM_HEAD`), never the job's
  live worktree. The folder's branches are the clone's `origin/*` with their
  whole history, so `git merge-base origin/dev HEAD` finds the files the job
  changed. The clone shares the folder's objects (nothing is copied; only
  the objects the head needs are read). Nothing the script does reaches the
  folder's repository or the job's worktree. The folder usually belongs to
  another user than the lease's, so every Git command of the checkout that
  reads the folder trusts exactly the folder and its `.git` directory
  (`safe.directory`), including the upload-pack `git clone` starts for it;
  no other directory is trusted and no wildcard is used.
- When the checkout cannot read an object from the folder's repository (Git
  calls a loose object it cannot open "corrupt", whether its bytes are bad
  or the lease's user may not read it), the step says which object and
  git's underlying cause (permission denied with the file's mode and owner
  and the lease user, or corrupt bytes), then fetches from the folder's
  `origin` remote into the lease's home: the head commit alone when origin
  has it (`origin-head`), else origin's branches (`origin-branches`), and
  tries the checkout again. Its output names the fallback used. The
  folder's own repository is never modified or repaired. If no fallback
  works the step ends `error` with `error_kind` `checkout`. Until a later
  checkout of that folder reads every object from it, `manage_projects`
  `get_environment` shows `repository_health` ("folder repository <folder>
  has a corrupt object <id>", or an object run steps cannot read) and the
  machine's status (`swarm_status`, a linked machine's `machine_status`)
  lists it among its reasons.
- When `git clone` of the folder fails (it reads every branch tip, so one
  tip the lease may not read fails it), the step makes the same shared
  clone by hand (the folder's objects as alternates, its branches as
  `origin/*`) and goes on; its output says so with git clone's error. A
  checkout git refuses ends `error` with `error_kind` `checkout` and a
  reason that names the git command that failed and keeps git's whole
  message (every line joined with ` | `; a long one keeps its first and
  last lines with a marker saying how many were left out). It is recorded
  as the folder's `repository_health` with `problem` `checkout_failed`,
  `git_command` and `detail` (git's error), shown on the same surfaces.
- Swarm's own Git writes into the folder's object store (commits, rebases,
  worktrees) under a restrictive umask and without setgid, which can leave
  loose objects mode 0440 or fan-out directories in Swarm's group. The
  daemon's Git on a registered folder runs with umask 0002, routed into the
  sandbox or not (`--sandbox=off`), and the daemon repairs every
  registered folder's object store at startup, every 15 minutes and before
  each environment step: entries it owns get the read bits the folder's
  `.git/objects` grants its group and others, and that directory's group
  (setgid too where the service allows it). The repair never follows a
  symbolic link, even one swapped in while it runs: each entry is opened
  without following links relative to its parent directory and changed
  only through that open descriptor, and a file with more than one hard
  link is left alone. `repository_health.permissions`
  (`checked`, `repaired`, `unrepaired`, `setgid_refused`, `sample`,
  `error`, `at`) shows the last repair that changed something or left a
  problem, until the next pass that finds every entry readable clears it
  (a pass runs before each step, so a successful repair is usually gone
  by the time you look); entries it could not repair
  (another user's) are a machine status reason ("folder repository
  <folder> has N object store entries run steps cannot read that Swarm
  could not repair").
- The script runs with the checkout as its working directory, the result
  JSON on stdin and the same `SWARM_*` variables a host step gets; nothing
  else of the daemon's environment is passed in.
- `timeout_seconds` covers waiting for a lease, making the checkout and the
  script. When every lease of the folder is in use the step waits for one,
  as agents' bash does, and says so in its output; a step whose script is
  still running at its limit fails with exit code 124, and one whose limit
  ended before its script started (waiting for a lease, making the
  checkout) ends `error` with `error_kind` `start_timeout`. Output,
  `output`, the exit code, `continue_on_failure` and `if` work as for a host
  step.
- The lease, with its home and the checkout, is released when the step
  ends, also on timeout and when the daemon stops; a lease left by a stopped
  daemon is released on its next start, and the step runs again with a new
  one.
- A folder with no environment set up for leases, one whose environment is
  not ready (still building, or its build failed), and a daemon whose
  environments are off end the step `error` at once with the reason
  (`error_kind` `lease` or `environment`).

#### Steps that could not run (`error`)

A `run` step (host or environment) whose script never started ends
`error`, not `failed`, with `error_kind`: `checkout` (the step's checkout of
the head could not be made, the head is not in the folder, or the job has
no head), `environment` (the machine could not run it: no run steps on the
daemon, its scratch, the executable could not be started), `lease` (the
folder's environment could not be leased, or the lease failed under it),
`script_missing`, `script_unusable` (not executable, not a regular file,
not owned by root on the host) or `start_timeout`. Its `next` says there is
nothing to fix in the work. A step on `if: [{step, status: failed}]` is not
sent for it; one on `status: error` is. Without `continue_on_failure` the
delivery stops and ends `error` (`error_reason` on the delivery, the
`automation.step.error` signal); with it the workflow continues, and the
delivery ends `error` unless another step `failed`. The task's outcome is
`error <step> (infrastructure: <kind>)` and its state reason says it is an
infrastructure problem, not the work's. Retries apply as for a failure,
except for `script_missing`, `script_unusable` and a refused lease.

#### Scripts stopped from outside the step (`interrupted`)

A host `run` step whose script Swarm saw end by `SIGTERM`, `SIGKILL`,
`SIGINT` or `SIGHUP` that its own time limit did not send (a service stop,
an operator, the OOM killer) ends `error` with `error_kind` `interrupted`.
Its exit code (128 plus the signal, 143 for `SIGTERM`) is kept, the reason
names the signal and the script's last output, the log tail ends with a
`swarm:` line saying so, and `next` says there is nothing to fix in the
work. Swarm does not know who sent the signal; find that in the machine's
logs. Configured retries apply. A script that crashes on a signal of its
own (`SIGSEGV`, `SIGABRT`, ...) stays `failed` ("killed by SIGSEGV (exit
code 139)"). A script that exits with code 129, 130, 137 or 143 by itself,
and an environment step (whose lease returns only an exit code), stay
`failed`, and the reason says that code is also what a process killed by
that signal returns, so Swarm cannot tell the two apart.

A step's log tail is saved while its script runs (at most every two
seconds, on output), not only when it ends. When the daemon stops, the
running step is resumed at its next start rather than recorded; if it
stopped or crashed during an attempt, the step's `failures` gains "attempt
N did not finish ... (interrupted ...)" with its last saved output, the new
attempt's log starts with the interrupted attempt's output, and the step
runs again once. `GET /v3/box/busy` counts run step scripts running now
(`workflow_steps`): the machine is busy and not `restart_resumable`, an
automatic update (`if_idle`) does not drain or restart over them, and a
drain (`GET /v3/box/drain` `workflow_steps`) is not ready until they end,
and its `reason` names them. A forced drain (`request_update` with `force`)
goes ahead over them at its deadline: the restart interrupts them, and their
steps run again after it (or are reported `interrupted`). New run steps are
not held during a drain.
- A check in the environment never hands the work over: a successful one
  after the review is not reported as a push.

### `agent` steps

Agent `brief` templates accept `{{steps.<id>.output}}`, `.verdict`, `.head`
and `.handoff`, referencing earlier steps only. `for_each` agent steps do not
support these step-result placeholders or `since`; definitions using them are
refused. Substitution is single-pass; missing values become `(no output)`. Each value is capped at 16,000 bytes and
the rendered brief at 20,000 bytes. Reviewer `output` is its findings (also
capped at 16,000 bytes), not a job ID. Reopen substitutions retain their
existing single-line, 500-byte cap.

A reviewer agent may set `"since": "review"`, naming an earlier completed
review step. It receives that review's findings and reviews only the delta
from its recorded head to the current delivery head. The head must be a
verified ancestor; missing or unreadable reviews are refused. The outcome
records `base`. An `ok` requires every previous finding closed and no new
finding in the delta.

Every agent outcome records `handoff` (at most 4,000 bytes): the existing
`task_progress` done/complete summary, or the job's report if none was given.
Agents are asked to state what changed, what was tested and how, and what is
open. Pass `{{steps.fix.handoff}}` into the next fresh session instead of
reopening history.

Fresh writable agent steps following a review count as correction attempts:
at most five starts per (account, automation, triggering job), across delivery
retries and revisions. A retry of the same delivery/step uses its existing
reservation, even after restart. Reservations precede dispatch (a failed start
still consumes one); they are independent of reopen rounds. Workflows also
have at most 40 steps, and agent-step jobs never trigger workflows themselves.
Existing reopen behavior and its per-step/rebase round accounting are unchanged.

A reviewer step (`worker: reviewer` or a worker whose role is reviewer) is
`skipped` with reason **“the job made no commits: nothing to review”** when
its result has `no_commits: true`: Swarm positively assessed the worker job's
lane as having no commits since its captured base. Missing `git` alone is
unknown, not empty, and does not skip review. Earlier work produced by another
step remains a separate review target. Other agent steps and run steps still run.
A skipped review has no verdict, so verdict conditions do not match; it also
**does not match `status: succeeded`**. The delivery succeeds if nothing failed
and no reopen step sent the work back (see `sent_back` below).

```json
{ "id": "review", "type": "agent", "worker": "reviewer", "brief": "Review this change for security problems." }
```

- `worker` is one of Swarm's own agents (`swarm`, `coder`, `finder`,
  `designer`, `reviewer`), or a worker id. One of Swarm's own agents runs as
  a task in the folder, in the triggering work's project, exactly as
  `manage_projects` `propose_task` would, with the brief and the triggering work's result; a
  coder's worktree starts at the delivery head, so its commits descend from
  the work it follows. Its task records the workflow that started it and its
  end never triggers workflows. Only `reviewer` may opt into `fixes: security`.
- For a worker, Swarm starts the job through the normal job admission (workspace and worker
  checks) with request source `automation`, the brief with the triggering
  job's result attached, and the delivery id and step id as its idempotency
  key (a restart never starts it twice; an explicit re-run starts a new job).
- A reviewer reviews the triggering task's or job's exact head (`review_job`:
  `task/<project>/<task>` or `worker/<worker>/<run>`), in its own session, with the whole picture in its brief (see
  [What the reviewer receives](#what-the-reviewer-receives)).
- The step waits, within its timeout, for that job to end; the job's status
  is the step's status. The step's `outcome` records the job id, its head,
  its report and, when the job recorded a review (a reviewer's verdict on the
  job it reviewed), `verdict` (`ok` or `block`) and `findings`. Verdict and
  findings come from the review record: the reviewer's FINDINGS section in
  full, up to 16,000 bytes (longer findings are cut at a line boundary and
  say so), never the job's shortened report. Headings the reviewer wrapped
  in Markdown bold at the start of their lines (`**VERDICT: BLOCK**`,
  `**FINDINGS:**`) are read like plain ones, although the reviewer prompt
  asks for plain headings (see [reviewer](reviewer.md)). A readable `block`
  review whose FINDINGS section is empty has the record's reason as its
  findings. A review whose report could not be read (recorded with the reason
  "missing or malformed review verdict") is **unreadable**: the outcome has
  `verdict: block`, `review_unreadable: true` and no findings, so a
  `verdict: block` condition still holds but there is nothing to send.
- The delivery's **head** starts as the triggering job's head. When the job
  the step started committed on top of it, Swarm checks with Git that the
  job's head descends from the current delivery head and moves the delivery
  head forward (`outcome.moved_head`); later `run` steps get the moved head.
  A head that does not descend fails the step and the delivery head stays.
  A reviewer is read-only (by default it reads the job's own worktree in
  place, below) and so never moves the head, unless its step sets `fixes`
  (below). A `rebase` step (below) may move it to a head that does not
  descend from it, under its own checks.
- When the step moved the head and the triggering job is a project task on
  the delivery's branch, Swarm **fast-forwards the task's own branch and
  worktree** to the new head, so a later `message_task` or `reopen_task`
  follow-up starts from the delivered work instead of the stale head. It
  does so only as a fast-forward of a clean, idle worktree on that branch;
  otherwise (a rebase, uncommitted changes, a running task, another branch)
  nothing moves and the reason is recorded on the delivery (`task_branch`:
  `head`, `advanced`, `reason`), on the task (`workflow_branch`) and in an
  `automation.task_branch.not_advanced` signal. The step never fails for it.

#### Rebase steps (`rebase: true`)

A fresh `agent` step whose worker is `coder` or `swarm` may set
`rebase: true` (not with `fixes`, `produce`, `since` or `for_each`). When it
starts, Swarm reads the tip of the job's target branch (`base_branch`) in the
folder, records it on the step (`outcome.rebase_onto`) and tells the agent to
rebase its worktree (started at the delivery head) onto that tip, to leave
nothing uncommitted, and to name the files the rebase conflicted on in its
handoff on one line: `Rebase conflicts: a.go, b.go` (or
`Rebase conflicts: none`).

A rebase rewrites history, so its head does not descend from the delivery
head. Swarm moves the delivery head to it only when all hold:

- the new head descends from the recorded target tip;
- the job reported no uncommitted changes;
- the files the new head changes against that tip are the files the old
  head changed against the old base, give or take only the files the
  handoff names as conflicted.

Then the delivery records `rebased_from` (the old head) and `rebased_onto`,
and `outcome.moved_head` has `rebased: true`. Any check that fails refuses
the step (`rebase not accepted: …`, the reason in `outcome.rebase_refused`)
and the head stays. A rebase head that does descend from the delivery head
(nothing was rewritten) moves it as any agent step does. A rebased head is
not a fast-forward, so the task's own branch is not moved (above).

#### Where the agent works (`checkout`)

```json
{ "id": "review", "type": "agent", "worker": "reviewer", "brief": "Review this change.", "checkout": "same" }
```

An `agent` step's `checkout` says where its agent works. Leave it out and
each agent works where it should:

| Agent | Default | Allowed |
|---|---|---|
| reviewer | `same` | `same`, `new` |
| reviewer with `fixes` | `new` | `new` (it commits) |
| `coder`, `swarm`, any other worker | `new` | `new` |
| `finder`, `designer` | `none` | `none` |

- `same`: a new reviewer session in the worktree of the job the step
  follows, where that job did its work. Nothing is copied or allocated, and
  the worktree stays the job's: it is mounted read-only for the reviewer
  (its sandbox, not only its Git metadata), only that reviewer session runs
  in it, and when the reviewer ends nothing in it is removed or cleaned.
  The reviewer starts only when the worktree is exactly at the head being
  reviewed with nothing uncommitted; otherwise the step fails with that
  reason. If the worktree moves or gets uncommitted changes while the
  review runs (someone sent the job a message), the verdict is recorded as
  `block` ("the work changed during the review"), so an `ok` never pushes
  code nobody reviewed.
- `new`: the agent's own worktree at the job's head (a reviewer's is a
  detached, disposable checkout removed when it ends). A `swarm` task works
  on its own branch there, as a coder does; only in a folder that is not a
  Git repository on a branch does it work in the folder itself.
- `none`: the folder itself. Finder and designer tasks always work there;
  the job's commits are the folder's branches (its branch and head are in
  the brief).

To have the job's own agent continue in its own worktree, use a `reopen`
step: it sends a follow-up into the job's own session. The daemon refuses
any other combination when the workflow is saved (for a worker named by id,
`same` only when it is a reviewer).

A workflow saved before `swarm` moved to its own worktree may still hold a
`swarm` or `coder` step with `checkout` `none` or `same`. Saving such a step
is refused, but the stored revision still runs: the step runs with `checkout`
`new`, and its run in the delivery records a `note` ("checkout none migrated
to new ..."). This includes a re-run of a delivery pinned to that revision.
`manage_workflows` `list` and `GET /v3/workflows/{id}` list such steps under
`step_migrations` (stored and effective checkout, and a note); resave the
workflow with `checkout: "new"` to clear it.

#### Reviewer fixes (`fixes: "security"`)

```json
{ "id": "review", "type": "agent", "worker": "wkr_custom_reviewer", "brief": "Review this job.", "fixes": "security" }
```

An `agent` step naming `reviewer` or a reviewer worker by its id may set `fixes` to `security`
(the only value), so that the reviewer may commit small, security-only
fixes (a missing escape, a path check, a secret in a log line) instead of
blocking for them. It is opt-in per step and never the default: a reviewer
started any other way (`manage_jobs` `give_job` with `review_job`, or an `agent` step
without `fixes`) stays read-only, detached and report-only. Swarm never
decides on its own to let a reviewer write. `manage_workflows` `propose`,
`then` and the API refuse `fixes` on a step whose worker is not a reviewer (a role
name, a worker id or name that resolves to another role, or one that does
not resolve); the daemon refuses it again when the job starts and at job
admission, where only a job automation's request may carry it.

With `fixes`:

- The reviewer job gets a writable branch `agent/review-<run>` started at
  the reviewed head, in its own worktree of the folder (like a coder's job
  checkout); its commits are in the folder's repository as they are made. Every tool call checks that the checkout is
  on that branch and that its `HEAD` descends from the reviewed head.
- It may edit files and commit (`git_add`, `git_commit`), and run the
  narrowest test that proves a fix; it cannot start other agents.
- Its prompt says: commit only small, security-only fixes on top of the
  reviewed head; list every fix in FINDINGS marked `FIXED`, with what it was,
  why, and `file:line`; anything that is not a small security fix is a BLOCK
  finding; run only the narrowest test that proves a fix and list it under
  TESTS RUN.
- The limit is enforced in code, after the reviewer job ends: its commits on
  top of the delivery head may change **at most 80 lines** in total (added
  plus removed, `git diff --numstat`; a binary file counts as over the
  limit). Over the limit the step fails with reason `reviewer fixes too
  large`, is not retried, and the delivery head does not move.
- Only an `ok` verdict moves the head (after the same ancestry check). A
  reviewer that committed while its verdict is `block` (or that recorded no
  verdict) does not move the head; the step keeps its verdict, so a `reopen`
  step still sends its findings to the coder. A security problem the
  reviewer fixed is not a reason for it to block.
- An unreadable report (`review_unreadable: true`, see
  [Reopen steps](#reopen-steps)) is a `block` too: the fix commits are
  listed but never applied, the head does not move, an `ok`-gated step does
  not run, and a `reopen` step sends nothing and ends the delivery `blocked`
  with `review unreadable: <reviewer job id>`. The fixing reviewer's prompt
  asks for plain headings, like the read-only reviewer's.
- An `ok` verdict may mean "I fixed it", so it fails closed: Swarm reads the
  reviewer's head and fix commits from its own checkout with Git, and when
  that read fails (or the head moved without readable fix commits) the step
  fails with `reviewer fixes unreadable`, the delivery head does not move
  and a later `ok`-gated step does not run.
- A fixing reviewer branches at the reviewed job's own head, so a fixing
  `agent` step after another `agent` step that already moved the delivery
  head is refused without starting its job. Put the fixing review first.
- The step's `outcome` shows the reviewer's `fix_commits`, `fix_lines`,
  `fixes_applied` and, when not applied, `fixes_reason`; `manage_jobs` `get_job` on
  the reviewer job shows its fix `commits` and `head` on its
  `agent/review-<run>` branch (read from its checkout; `fix_error` when that
  read fails). `manage_jobs` `get_job` on the reviewed job and the delivery views
  show the moved `head` and the applied `fix_commits`.
- A fixing reviewer's review of the reviewed job is recorded with
  `fix_head` when it committed: its OK covers the reviewed head only with
  those fixes, so `manage_jobs` `get_job` on the reviewed job shows that review as
  `stale`, never as a current OK of the unfixed head, whether or not the
  step applied the fixes. An OK whose fix checkout cannot be read is
  recorded as BLOCK (`reviewer fixes unreadable`).
- The reviewer's checkout is kept after the job ends, like a coder's job
  checkout, so the delivery and later `run` steps can read its head; it is
  not deleted as a read-only reviewer's checkout is (it is removed only once
  its work is in the target branch, see
  [When Swarm removes a finished job's worktree](#when-swarm-removes-a-finished-jobs-worktree)). Reviewer jobs stay
  undeliverable with `manage_jobs` `deliver_job`: their fixes reach a job only
  through the delivery head.

### `reopen` steps

```json
{ "id": "fix", "type": "reopen", "from": "review", "message": "The reviewer blocked your work.", "max_rounds": 2,
  "if": [{ "step": "review", "verdict": "block" }] }
```

- Sends a follow-up into the **triggering job's own session**, exactly as
  `manage_jobs` `message_job` does: the same session and worktree, never a new job.
  The text is `message`, then the findings of the `agent` step named by
  `from`, verbatim and in full (up to 16,000 bytes), then: "Fix these in this
  worktree, commit, and report what you changed."
- A step needs `from`, `message` or both. When the `from` step recorded no
  findings (an `ok` review has none) or `from` is omitted, the follow-up is
  `message` alone, then the closing line, and it counts as a round like any
  reopen. With no findings and no `message` the step sends nothing and fails
  with `step <from> recorded no findings to send and the reopen step has no
  message`.
- `message` may hold `{{steps.<id>.output}}`, `.verdict`, `.head`, or
  `.handoff`, naming an earlier step. Output is a `run` step's last non-empty
  line, a reviewer agent's findings, or another agent's job ID. Each value
  is flattened to one line and capped at 500 bytes; missing values become
  `(no output)`. Other placeholder forms are sent as written, and inserted
  values are never expanded again.
- The step succeeds once the follow-up is accepted; it does not wait for the
  follow-up to finish. The follow-up's end is a new job end, so it triggers
  the automation again as a new delivery whose `round` is the previous
  round + 1. The round is counted once, when the follow-up is sent.
- Rounds count per (automation, job), across deliveries: a delivery's
  `round` is the number of reopens the automation already sent into that
  job, so every later delivery of the job continues the count, including one
  for a turn the person sent after a follow-up failed.
- A busy **task** (Swarm's own agent: `task/<project>/<task>`) gets the
  message **queued**, through the same queued-message path as the person's
  own message during a run and `manage_projects` `message_task` to a running
  task: when its run, or a follow-up the person sent meanwhile, is in flight,
  the step does not wait.
  The message is stored for that run, reaches the agent with its next tool
  result (or starts the next turn when the run ends without reading it), and
  the step succeeds at once with `outcome.queued: true`, `outcome.followup`
  naming that run as the task's end names it (a continuation after a provider
  limit, `<run>.limit<n>`, is named `<run>`; a continuation of the task's
  first run cannot be named, so the step waits for it) and the output `sent (queued) to <job> for its current run
  <run> (round N)`. The round is counted then and the delivery ends
  `sent_back` as after any reopen. That run's end is the task's next job end:
  it starts the next round, which reviews whatever head the run ends at, also
  when the task committed more after the reviewed head. Only a real refusal
  fails the step, with its reason: the task was deleted, archived, cancelled,
  superseded or rejected, or stopped by the person, or its rounds are
  exhausted. While no run can take the message yet (the task is planning or
  waits for plan approval, its run is still starting, or its status says
  running before a run is recorded), the step waits as below.
- A worker job takes one follow-up at a time. When the job is still running
  (its run or another follow-up is in flight), the step **waits**, within its
  `timeout_seconds`, for it to settle, then sends its follow-up. A follow-up
  that starts just before the step's own send (the send is refused with "a
  follow-up is already running in this job") is waited for the same way; if
  that run has already started, the follow-up is queued for it instead and the
  step tracks that run. While it
  waits the step's state is `waiting` and its `reason` says what it waits for
  (`waiting: the job ... is still running (in_progress)`), and
  `automation.step.waiting` is signalled once. It asks again on each new
  signal (a job end among them) and at least every 2 seconds. It fails only
  when its timeout expires (`timed out after N seconds waiting for job ... to
  settle (...); nothing was sent`), when the stop switch is turned on while
  it waits, or, at once, when the job is cancelled; nothing is sent then.
- `max_rounds` (default 2, at most 5) bounds this reopen step: rounds count
  per automation, job and reopen step id, so a `fix` loop and a `rebase` loop
  each have their own budget. Once this step has sent `max_rounds` reopens
  into the job, it sends nothing, fails with reason
  `rounds_exhausted: step <id>` (naming the step that ran out), and the
  delivery ends `blocked` (`automation.delivery.blocked`). The job as a whole
  allows at most the sum of its reopen steps' `max_rounds`; past that any
  reopen step fails with `rounds_exhausted: job`. The counts are checked
  again right before each follow-up is sent, so several deliveries of one job
  that wait for it together never send more than `max_rounds` reopens of one
  step between them. Reopens recorded before rounds counted per step are
  attributed to the step whose delivery recorded them; one that cannot be
  attributed counts against every step.
- An unreadable review is never sent: when the `from` step's outcome has
  `review_unreadable: true`, the step sends nothing into the coder's session,
  uses no round, fails with `review unreadable: <reviewer job id>`, and the
  delivery ends `blocked` with that reason (`automation.delivery.blocked`),
  so the delegating AI can read the reviewer's session (`manage_jobs` `get_job`) and
  send findings by hand or re-run the delivery.
- A cancelled job is never reopened: the step fails with the reason, as it
  does for a deleted, archived or person-stopped task. A step with neither
  findings nor a message to send fails too.

## What the work hands over, and where

A workflow says **what** the work hands over and **where** it goes, for any
kind of work, not only commits:

1. **What.** Every result lists its `deliverables`: the branch, each file the
   work committed (with its sha256), its media, its report (see
   [The result JSON](#the-result-json)). The workflow's optional `result`
   names the one it must hand over: `{kind: file | media | report | branch,
   path, describe}`. When the work did not produce it (a finder's research
   ends as a text report, not a file), an `agent` step with `produce: true`
   writes it: the agent is told the exact path and what it must contain,
   Swarm checks the file exists (committed at the step job's head, or in the
   folder for an agent working there), records its sha256 and adds it to the
   delivery. A reviewer started after a produce step reviews the produced
   work, in place.
2. **Where.** A `deliver` step hands it over:

| `to` | What happens |
| --- | --- |
| `folder` | Copied into `path`, a directory inside the folder. An equal file there is kept; a different one is never overwritten (the step fails). A path that leaves the folder, symlinks included, is refused. |
| `merge` | The work's branch is merged into the folder's branch, as `manage_projects` `integrate_task` (a task) or `manage_jobs` `deliver_job` (a worker job) do. Refused when the folder has a publish rule (use `publish`). |
| `publish` | A pull request through the folder's publish rule (`install.sh workspace publish`), as `manage_jobs` `deliver_job` does. Worker jobs only. |
| `outbox` | Exported to the machine's outbox (`SWARM_OUTBOX_URL`: a bucket or a folder): each file as `machines/<machine>/blobs/sha256/<hex>` and a manifest at `machines/<machine>/deliveries/<delivery>/<step>.json`. Refused on a machine without an outbox. |

A deliver step hands over `what` (deliverables by name), else the workflow's
`result`, else everything that fits its target (files, media and the report
for `folder` and `outbox`; the branch for `merge` and `publish`). Every
file's content is checked against the sha256 the work recorded: what was
reviewed is what is delivered. Its outcome lists each deliverable with its
sha256 and where it went; its output line is the folder path, the merged
commit, the pull request or the manifest key. Anything else (push to a
remote, deploy, alert) is a `run` step: it gets every deliverable on stdin
and the result's path in `SWARM_RESULT_FILE`.

A workflow that names `reviewer` runs Swarm's native `system-reviewer` as a
task. It never resolves or deploys a reviewer worker. Name a custom reviewer
by its worker id. Either may opt into `fixes: security`: the reviewer runs
`system-reviewer-fixer` in its own fix branch at the reviewed head (`checkout: new`).
Only OK with at most 80 changed lines advances the delivery head; oversized or
unreadable fixes fail closed, and BLOCK never advances it.

### Example: code, reviewed, merged

```json
{ "name": "coder review merge", "scope": { "folder": "/srv/site", "role": "coder" },
  "steps": [
    { "id": "review", "type": "agent", "worker": "reviewer", "brief": "Review this task against its brief." },
    { "id": "fix", "type": "reopen", "from": "review", "max_rounds": 2, "if": [{ "step": "review", "verdict": "block" }] },
    { "id": "merge", "type": "deliver", "to": "merge", "if": [{ "step": "review", "verdict": "ok" }] } ] }
```

### Example: research turned into a report

```json
{ "name": "audit report", "scope": { "folder": "/srv/site", "role": "finder" },
  "result": { "kind": "file", "path": "reports/audit.md", "describe": "A one-page audit: findings with file:line, severity, and the fix." },
  "steps": [
    { "id": "write", "type": "agent", "worker": "coder", "produce": true, "brief": "Turn the finder's findings into the report." },
    { "id": "review", "type": "agent", "worker": "reviewer", "brief": "Check the report against the findings and the code." },
    { "id": "save", "type": "deliver", "to": "folder", "path": "deliverables", "if": [{ "step": "review", "verdict": "ok" }] },
    { "id": "bucket", "type": "deliver", "to": "outbox", "if": [{ "step": "review", "verdict": "ok" }] } ] }
```

### Example: a design exported

```json
{ "name": "designs out", "scope": { "folder": "/srv/site" }, "when": "succeeded",
  "steps": [ { "id": "bucket", "type": "deliver", "to": "outbox" } ] }
```

A design job's HTML files are its deliverables, so the deliver step exports
all of them.

## Deliveries

Each matching (job end, automation) pair is one **delivery** with a stable
`delivery_id`, made from the job id, the follow-up, the automation id and the
automation revision. A delivery runs at most once; a replayed job end does
not deliver again. An automation has at most one delivery per job and head:
a job end whose head already has a delivery (running or finished) for the
automation, such as a stopped follow-up or a turn that committed nothing,
creates none, and `automation.delivery.skipped` records why. A delivery
keeps the steps of the revision it started with, its `round` (the reopens
the automation already sent into the job), its `head` and, when it ended
`blocked`, its `blocked_reason`. A delivery whose reopen step sent a
follow-up ends `sent_back`, never `succeeded`: nothing after the reopen
handed the work over, and the follow-up's end starts a fresh delivery. Its
`sent_back` says which reopen step sent it (`step`), the round that
follow-up started (`round`) and why the step ran (`reason`, from its
conditions: `review verdict block`, or `push failed: exited with code 1: ...`
when a push allowed to fail was sent back to rebase). This holds even when
a step allowed to fail (`continue_on_failure`) failed before the reopen.
A reopen step with no rounds left sends nothing and still ends the delivery
`blocked`.

Every delivery and step is recorded durably with its state (`pending`,
`running`, `waiting` (a reopen step waiting for its job, a step waiting for a
provider's limit to reset, or a delivery parked on such a step; see below),
`succeeded`, `failed`, `skipped`, `stopped` or, for a delivery, `blocked`,
`sent_back` or `conflict` (see [Rebasing](#rebasing))), attempts, start and
finish times, exit code, output line, log tail and outcome.

A delivery is shown as `waiting` while its only unfinished work is steps
waiting for a provider's usage, rate or session limit to reset and no step
is running. It comes with `until` (the earliest reset, unix ms) and `reason`
(the waiting step's reason, built from its worker run's `queue_reason`).
This applies to `GET /v3/workflows` `latest_deliveries`, `GET
/v3/job-deliveries` and `GET /v3/job-deliveries/{id}` (`state=waiting`
lists these deliveries, and `state=running` leaves them out), and the
fleet's `last_delivery`. Nothing else changes: the delivery is unfinished,
its step and worker run resume by themselves at the reset (also after a
daemon restart, or when the reset passed while the machine was off), and it
shows `running` again once the step runs. A delivery with any step running,
including a reopen step waiting for its job, shows `running`. `GET
/v3/box/busy` counts worker runs waiting on a provider limit as
`limit_waiting`, not as `worker_runs` or `reviews`, so they do not make the
machine busy and an update drain does not wait on them.

Each step also keeps `failures`: every failed attempt's
`attempt`, `rerun`, `at` and `reason` (the newest 20), kept across reruns.
A deliver step whose Git inspection did not finish in time (the machine is
busy, or the job is large) tries again up to 10 times with growing waits
(15 seconds doubling to 10 minutes, about 45 minutes in all) before it
fails. A daemon restart
resumes unfinished deliveries at the first step that is not finished; a
finished step is never run again.

The **stop switch** (`manage_workflows` `stop_all`,
`POST /v3/workflows/stop`) is machine-wide: while it is on no new step
starts; running steps finish, and the next step is recorded as `stopped`.
`manage_jobs` `rerun_failed_steps` (the same as `manage_jobs` `deliver_job` with
`automation: true`) re-runs, for each automation,
only the job's **newest** delivery, and only while that delivery's head is
still the job's current head; older deliveries are never re-run (they are
listed under `unchanged` with reason `superseded`, a newest delivery for an
older head with `stale_head`). To re-run one named delivery explicitly,
whatever its head, use `POST /v3/job-deliveries/{id}/rerun` (SDK
`client.workflows.rerun`). Either way only failed or stopped steps (and
the steps after them that did not run) run again; a succeeded step is never
re-run. A `blocked` delivery re-runs from its first step with a new round
budget: each reopen step gets `max_rounds` reopens past those it already
sent (a `run` step that already succeeded is still never re-run). Either
way the delivery's `for_each` items are also revived, even when no step
failed (see `for_each` below); the response lists them under
`revived_items` (`step`, `index`, `id`, `job_id`, `state`). The reason
`nothing_to_rerun` means there was no failed or stopped step and no item to
revive.

`manage_projects` `rerun_delivery` with a `task_id` is the same explicit
rerun of the task's newest delivery, found by the task (the service is
`RerunTaskDelivery`; see the changelog for its catalog status).

**A same-head hand-over re-runs a failed gate.** A gate (`run` step) can fail
for a reason outside the code: the gate script itself, a flaky test, a
provider outage. When a task's newest delivery failed on a `run` step, or was
sent back by a reopen step because a `run` step failed (`if status: failed`)
or could not run (`if status: error`, reopen cause `step_error`), and the task hands
its work over again with no new commit (a `message_task` or `reopen_task`
follow-up ending at the head that delivery covers, including the head an
agent step moved it to), that hand-over creates no delivery
(`duplicate_head`) and instead re-runs that delivery from the failed `run`
step: the same rerun as `POST /v3/job-deliveries/{id}/rerun`. Steps before it
are kept; it and the steps after it run again, a reopen step among them, so a
gate that fails again sends the work back again within its `max_rounds`. The
delivery records the follow-up as `handover_rerun` (one rerun per
follow-up), and the signal `automation.delivery.rerun` (`reason`
`handover_same_head`, `step`) says so. A delivery that failed on an agent
step, a review block and a worker job's hand-over are not re-run this way.

## Rebasing

Rebasing is Swarm's job, not the workflow author's or the coder's. When
several jobs land on one target branch (`dev`), each landing moves the target
under the others; Swarm keeps every delivery's work on top of it by itself,
with no step to configure.

**When.** For every delivery whose job has a target branch (the result's
`git.base_branch`, the branch `merge`, `publish` and a push use), with the
workflow's `rebase` setting `auto` (the default), the runner checks the
target's tip:

- before the delivery's first step;
- before any step that depends on the head (a `run` step, an `agent` step, a
  `deliver` step to `merge` or `publish`) whenever the tip moved since the
  last check (`target_checked`);
- after such a `run` or `deliver` step **failed** while the tip moved (a gate
  that said "rebase needed", a push refused because the target moved): the
  head is rebased and the step runs again, with no reopen step.

A delivery rebases automatically at most 5 times (a target that keeps
moving); past that its steps run on the head as it is and the signal
`automation.delivery.rebase_limit` says so. `rebase: "off"` turns all of this
off for a workflow (the delivery records `rebase: "off"`); an explicit rebase
step still runs.

**How.** One daemon function does every rebase (automatic, explicit step, on
request). It is Git in the folder, run by the daemon with hooks off:

1. Up to date: nothing happens, no record, nothing moves. That is when the
   head descends from the tip, when the tip already contains the head (the
   work landed), and when every one of the job's commits is already in the
   target as another commit (the same patch id) or changes nothing on it.
2. Otherwise each of the job's commits (from the merge base of the head and
   the tip; merge commits are refused) is replayed onto the tip with `git
   merge-tree` (a three-way merge whose base is the commit's parent) and `git
   commit-tree`, keeping its message and author. Nothing is checked out: no
   worktree of the folder or of the job is touched, and the target branch is
   never written or pushed. A commit whose change the target already has
   (same patch id) is skipped, as `git rebase` skips it, and listed
   `in_target`. Commit messages and resolved files go to Git on standard
   input and no index file is used, so nothing outside the folder's own
   repository is read or written: the rebase runs the same when daemon Git
   runs in the project's sandbox, which mounts only the folder and its Git
   directory.
3. A clean replay is verified: each new commit carries the same patch as the
   job's commit it replays (patch ids: the lines each file's diff adds and
   removes, its mode changes and a binary file's new content, ignoring line
   numbers and context), except a commit whose change the target already had
   (`empty`). Then the delivery head moves to the new head (the old one is
   kept as `rebased_from`, the tip as `rebased_onto`), the new head is named
   `refs/swarm/rebased/...` so it stays reachable, and the job's own branch is
   moved by compare-and-swap (`git update-ref <branch> <new> <old>`: only
   while it still points at the old head). A branch checked out in a worktree
   is moved only in the job's own worktree, with `git reset --keep` while
   that worktree is clean and still at the old head: a project task's by its
   branch advancer (the task idle), a worker job's (its worktree keeps the
   branch checked out) directly. A branch checked out anywhere else (the
   folder itself, another worktree) is not moved, and the record says why.
   The record keeps the old -> new commit mapping and a `git range-diff`. A
   `deliver` step to `merge` or `publish` lands the work's branch, so it lands
   a rebased head once the branch names it (the usual case). When a rebase
   (automatic, a `rebase` step or on request) could not move the branch while
   such a step is still to run, the rebase is not applied: its record ends
   `skipped` with the reason (signal `automation.delivery.rebase_skipped`),
   the delivery keeps its head and the merge or publish lands the branch as
   it is; it is tried again once the target moves or the delivery is rerun.
   When the delivery head is a rebased head its branch still does not carry
   (a rebase with no merge or publish left to run, or a delivery an older
   daemon left), a merge or publish first moves the branch from the earlier
   head of this delivery it is at (signal `automation.delivery.branch_moved`,
   the step's `outcome.branch_moved`), so a rerun once the worktree is clean
   or the task idle lands it; if it still cannot, the step is refused and
   says why. A reviewer allowed to fix (`fixes`)
   runs on the rebased head as on the job's own: only a head an agent step
   moved keeps it from starting.
4. Steps after a rebase run on the rebased head. Steps that already passed on
   the old head and whose result depended on it run again: `run` steps that
   ran and review steps that recorded a verdict (not a reviewer whose fixes
   are in the head), and, after the first of them, steps that were skipped
   (their conditions are read again). Each says `rebase_rerun` and a `note`
   ("runs again on the rebased head ..."), and the record lists them under
   `rerun`. An agent step that ran again starts a new job.

**Conflicts are resolved by Swarm.** A replay that conflicts stops at that
commit and keeps the conflicted state as a commit on no branch (the files
with their conflict markers, on top of what was replayed so far, named
`refs/swarm/rebase-conflicts/...`). The record names the conflicted files,
the job's commit that stopped, the target's commits since the job's base that
changed those files, and both sides' diffs of them. Swarm then starts a
**resolver**: the coder that comes with Swarm (its model is the account's
coder setting), in its own worktree checked out at that conflicted state,
with a brief listing the files, the stopped commit, the target's commits and
both diffs. It edits only those files and runs no Git. Swarm takes exactly
the conflicted files from its worktree (a file it removed is removed; a
symbolic link, a file outside the worktree or one larger than 8 MiB is
refused), refuses any file that still has a conflict marker (`<<<<<<<`,
`>>>>>>>` or `|||||||` at the start of a line), writes them into the
conflicted state's tree (`git hash-object --stdin`, `git ls-tree`, `git
mktree`), commits them with the stopped commit's message and author, and
continues the replay; a later commit that conflicts goes to the next round.
The resolved commit is listed `resolved: true` in the mapping (its patch
differs by design).

The resolver rounds are bounded by the workflow's `rebase_rounds` (1 to 5,
default 2) per rebase; a round whose work is refused or whose job fails uses
one, and the next resolver is told why. When the rounds are used up the
delivery ends **`conflict`**: not `failed` (no check failed) and not a
review's block. Nothing after it runs, the head and the job's branch stay,
the step it stopped before records the reason (`next` says what to do), and
the delivery's `conflict` keeps the files, the stopped commit, the target's
commits, the resolver jobs and the kept conflicted state. Its task's state is
`workflow_failed` (never `needs_you`, which is a pending request), its reason
naming the conflict. A rerun of the
delivery (`manage_jobs` `rerun_failed_steps`) checks the target anew and
rebases again, with the resolver.

**Rebased heads from agents are accepted.** An agent step (a fix step, a
coder told to rebase) whose head does not descend from the delivery head is
no longer thrown away when it rebased its own copy: its head moves the
delivery head when it descends from the target's tip (the one last checked,
the newest rebase's, or the tip now) and every commit of the delivery head
maps by patch id to a distinct commit of it, or is already in the target.
Its other commits are the agent's own (`added`). The old head is kept as
`rebased_from`. A head that drops one of the delivery head's changes is
still refused. (`rebase: true` on a coder or swarm agent step keeps its own
checks, described under `agent` steps.)

**Explicit forms.** A step `{"type": "rebase", "onto": "dev"}` rebases at
that point of the workflow, onto `onto` (default the job's target branch),
even with `rebase: "off"`; it succeeds `up to date`, rebased or not applied
(`outcome.skipped`, the reason: the branch could not be moved), and a
conflict nobody resolved fails it and ends the delivery `conflict`. Give it a
`timeout_seconds` long enough for a resolver. On demand, `manage_jobs`
`rebase` (`job_id`, optional `onto`), `POST /v3/job-deliveries/{id}/rebase`
(`{"onto": "dev"}`) and SDK `client.workflows.rebase(id, onto)` rebase a
**finished** delivery's head now (a running one is refused with HTTP 409: it
rebases itself). While such a rebase works, the store holds the delivery: a
rerun (`rerun_failed_steps`, the rerun route, a hand-over rerun) is refused
with the reason that it is being rebased, and only that rebase may save the
delivery, so neither overwrites the other (a hold a stopped daemon left
lapses after an hour). They answer `outcome` (`up_to_date`, `rebased`,
`skipped`, `conflict` or `error`), the record, the delivery and a `next` that
says whether the job's branch moved; a conflict on request starts no
resolver and moves nothing (a rerun rebases with one). No step re-runs.

```json
{ "name": "land", "scope": { "role": "coder" }, "rebase_rounds": 2,
  "steps": [
    { "id": "gate", "type": "run", "command": "/usr/local/libexec/swarm-deliver/gate.sh" },
    { "id": "review", "type": "agent", "worker": "reviewer", "brief": "Review the change." },
    { "id": "push", "type": "deliver", "to": "merge", "if": [{ "step": "review", "verdict": "ok" }] }
  ] }
```

Two coder tasks that touch the same files and land through this workflow
both reach the target: each delivery is rebased before its gate, again
before the merge when the other landed meanwhile (the gate and the review run
again on the rebased head), and a conflict goes to the resolver.

**Visibility.** A delivery shows `rebase` (its setting), `target_checked`,
`rebases` (each: `n`, `trigger` (`start`, `before_step`, `step_failed`,
`rebase_step`, `agent_step`, `request`), `target`, `onto`, `from`, `to`,
`outcome` (`rebased`, `resolving`, `conflict`, `error`, `skipped`), `commits`,
`added`, `rerun`, `branch`, `conflict`; a single-delivery read adds
`range_diff` and the conflict's `target_diff` and `job_diff`), and, when it
ended `conflict`, `conflict`, `conflict_reason` and `next`. Task progress
(`get_task`) gives each delivery its newest `rebase` (outcome, target, onto,
from, to, files, resolver jobs, whether resolved, how many rebases moved the
head), the workflow state and progress state `conflict`, the outcome
`conflict <files> (rebase onto <target>)`, and history lines ("rebased onto
dev (old → new)", "conflict on dev: a.go (not resolved)"); wake rows on this
machine and from linked machines carry the same `rebase`, and their
milestones name it. Signals: `automation.delivery.rebased`,
`automation.delivery.rebase_conflict` (a resolver starts),
`automation.delivery.resolver_started`,
`automation.delivery.rebase_unresolved`, `automation.delivery.rebase_error`,
`automation.delivery.rebase_limit`, `automation.delivery.rebase_skipped`,
`automation.delivery.branch_moved` (a merge or publish moved the job's branch
to the rebased head first), and `automation.delivery.finished` with
state `conflict` (its `files`, `commit`, `resolver_jobs` and `next`).

## The result JSON

```json
{
  "v": 1,
  "job_id": "worker/<worker>/<run>",
  "kind": "worker",
  "role": "coder",
  "worker": { "id": "<worker>", "name": "Coder" },
  "run_id": "<run>",
  "followup": "",
  "project_id": "<project>",
  "folder": "/srv/site",
  "status": "succeeded",
  "job_status": "needs_review",
  "finished_at": 1760000000000,
  "brief": "what the job was given",
  "report": "the worker's report",
  "request_source": "direct",
  "schedule_id": "",
  "git": {
    "branch": "agent/...",
    "base_branch": "main",
    "base_commit": "<sha>",
    "head": "<sha>",
    "commits": [{ "sha": "<sha>", "subject": "Add the logo" }],
    "files": ["logo.png", "index.html"],
    "worktree": "/path/of/the/job/checkout"
  },
  "files": [{ "path": "logo.png", "sha256": "<hex>", "type": "image/png", "size": 1234 }],
  "cost": { "tokens": 12000, "estimated_usd": 0.12, "model": "<model>" },
  "review": { "verdict": "...", "findings": [] },
  "deliverables": [
    { "name": "branch", "kind": "branch", "branch": "agent/...", "head": "<sha>", "source": "git", "from": "job" },
    { "name": "index.html", "kind": "file", "path": "index.html", "head": "<sha>", "sha256": "<hex>", "size": 2048, "type": "text/html", "source": "git", "from": "job" },
    { "name": "report", "kind": "report", "type": "text/markdown", "source": "report", "from": "job" }
  ]
}
```

`deliverables` lists everything the work can hand over: its branch, each file
its commits left at the head (sha256 read from the folder's repository, at
most 200), a media or design job's files, and its report. A deliver step
checks a file against its sha256 before handing it over.

`git` is present only when the job committed. `files` lists the files a media
or design job produced (path, sha256, type) or the media files a worker's
commits added. `review` is present for a reviewer job that recorded a verdict.
A media job's `kind` is `media` (role `image`, `video` or `sound`); a design
job's is `design`. The result is data written by agents: never run it as
code.

### The base after a rebase

A job records its target branch's tip (`base_branch`, for example `dev`) as
its base when it is created. A coder told to rebase onto a newer target, or
to merge it in, moves its head past that base, and `base..head` would then
list commits that are already on the target. So `base_commit` (in the result,
in the `manage_jobs` `get_job` result and in `manage_projects` `get_task_changes`) is the job's
**effective base**:

- Swarm takes the merge-base of the job's head and the target's current tip.
  The tip comes only from the folder's own `<base_branch>`; a
  `refs/remotes/origin/<base_branch>` (which a rebase fetched) counts only
  when the folder's branch contains it, and the merge-base itself must be on
  the folder's branch. Replace refs and grafts are ignored. A commit that is not on the target therefore never
  becomes the base, and none of the job's own commits can drop out of its
  range.
- That merge-base is the base when it descends from the recorded base (the
  job was rebased onto, or merged, a newer target) or when the head no longer
  descends from the recorded base at all (the target was rewritten and the
  job rebased onto it). The commits, files and diff are then listed from it,
  and the base recorded at creation is reported as `recorded_base_commit`
  (`recorded_base` in `manage_projects` `get_task_changes`).
- Otherwise, including a job that was not rebased while the target moved on
  and a job the target already contains (delivered by a fast-forward or a
  merge, where that merge-base would be the head itself), the recorded base
  stays, so a landed job still lists its commits.

A reviewer reviews `base..head` from the same effective base, so its range
holds only the job's own commits.

## Signals

Automations report through the machine signal feed (and so to every signal
sink, such as an operator's monitoring), with refs `job_id`, `delivery_id`
and `step_id`:

- `automation.step.started`
- `automation.step.waiting` (a reopen step waits for its still-running job;
  attrs: `step`, `reason`). Also an agent step whose job ended on the
  provider's usage, rate or session limit: the step is `waiting` with reason
  "waiting for the provider's limit to reset (until …)", records no verdict,
  and runs a new job after the reset the provider states (else a backoff of
  5 minutes doubling to an hour, at most 12 waits), using none of its
  `retries`; attrs also `provider_limit` and `retry_at`.

  The same holds below the step. A direct-role worker job (Coder, Finder,
  Designer, Reviewer) whose run ends on a provider limit is `waiting` (its
  `queue_reason` says until when) and runs again in its own session at the
  reset; a stopped reviewer then records the verdict of that rerun. An
  agent step backed by such a worker waits with its run until the reset
  (same reason and `retry_at`, no new job, no timeout) and then reads that
  job's outcome. A `for_each` item whose job ends on one is
  `waiting_for_limit` (signal `workflow.item.waiting`), the items after it
  keep waiting, and a new job starts for it after the reset; an item whose
  worker run already gave up after its 12 waits fails. Swarm's own quotas
  and budgets are not provider limits. Swarm-role (plan) worker jobs still fail on a limit.
  A project task's own run (its first run or a follow-up in its session,
  outside a worker) waits the same way, with the same bound, and continues in
  its own session at the reset; see
  [task progress](task-progress.md#waiting-for-a-providers-usage-limit).
  Once the person has fixed the provider (a new sign-in, key or model),
  `manage_jobs` `retry_now` (`POST /v3/provider-limit-waits/retry`, also on a
  linked machine) makes any of these waits due now; each goes through the
  same path as at the reset, and one that hits the limit again waits again
  ([tool catalog](tool-catalog.md#manage_jobs)).
- `automation.step.succeeded` (attrs: `step`, `output`)
- `automation.step.failed` (severity warning; attrs: `step`, `reason` and
  `next`, the next allowed action: `rerun` or `fix the script`; while
  automatic retries remain, also `will_retry` and `retry_at`)
- `automation.step.error` (severity warning; a `run` step whose script never
  started; attrs as `automation.step.failed` plus `error_kind`)
- `automation.delivery.finished` (attrs: `state`, step counts, `steps_error`
  when a step could not run; for `error` also `reason`; for
  `sent_back` also `sent_back_step`, `round` and `reason`, severity info)
- `automation.delivery.skipped` (a job end created no delivery; attrs:
  `reason` `duplicate_head`, `head`, `followup`, `status`, and
  `delivery_state` of the delivery, named in `refs.delivery_id`, that
  already covers that head)
- `automation.delivery.blocked` (severity warning; attrs: `reason`
  `rounds_exhausted` or `review unreadable: <reviewer job id>`, `round`,
  `head`, the last `findings` shortened, and `next`: send the findings by
  hand with `manage_jobs` `message_job`, or after a fix
  `manage_jobs` `rerun_failed_steps` (`deliver_job` with
  `automation: true`); for an unreadable review, first read the reviewer
  job's session)

A delivery that ends `rounds_exhausted` does not leave its task idle. The
stall check (docs/task-progress.md, "Stalled tasks") sends the reviewer's
last findings into the task's session as one extra round. If the review
still blocks after it, the findings go to the Orchestrator that delegated
the task, with one `task.stalled` signal (`stall: review_exhausted`). A
task whose workflow step is running or waiting (a provider-limit wait
included, until its reset plus `stall_after_minutes`) is never counted as
stalled. A waiting step that has not run again by then is counted as
stalled (`stall: provider_limit`).

Design jobs now end with `design.job.completed`, `design.job.failed` or
`design.job.cancelled`, and a stopped media job with `media.job.cancelled`.

## Who controls them

- AI clients and the Orchestrator: the catalog tool `manage_workflows`
  ([tool-catalog.md](tool-catalog.md)), the same tool and answers on both
  surfaces, here or with `machine` on a linked machine: `list`, `get`,
  `deliveries`, `history`, `propose` (the person accepts or discards it),
  `enable`, `disable`, `delete`, `export`, `import`, `restore`, `stop_all`
  and `help`. Its caller is never the machine owner: `import` and `restore`
  refuse host `run` steps (`run` steps with `"in": "environment"` are
  allowed; the answer's state is `refused`, with the reason and
  what to do instead: import without the run step, or ask the machine's
  owner), and a proposal with one needs the owner to accept it.
  Every action names a workflow by its id or its name; a name several
  workflows share is refused, naming each. A workflow has an optional
  `description` (at most 2,000 characters), shown by `list`, `get` and
  `export` and kept by `import`, `restore` and history. `propose` with an
  existing workflow's id or name sends only what changes, merged onto its
  current revision (each step by its `id`; `expected_revision` refuses the
  change if the workflow moved on; a new `name` with `id` renames it);
  `replace: true` sends the whole definition, as adding, removing or
  reordering steps needs. Fields `propose` does not take are refused by
  name. The person accepts or discards a proposal (see "Proposals: the
  person decides" below); `history` lists pending proposals with their
  state, and `list` and `get` show them as `pending_proposals`. `export` carries
  the workflow's project as its scope; `import` binds the project the call
  names, else that project on this machine, and refuses one the machine
  does not have rather than binding a wider folder. A disabled workflow
  does not run until it is enabled.
- MCP: the same `manage_workflows` tool, whose `list` shows every workflow
  exactly as Swarm runs it, with its last 3 deliveries one line each: delivery
  id, job, state, round, head, blocked reason and each step's state and
  verdict, no findings; `delivery_id` reads one delivery's steps and
  `detail: "deliveries"` every workflow's last 3, both bounded, findings cut
  to 1,500 characters (the same options on `GET /v3/workflows`), and whose
  `deliveries` action lists one line per delivery; `then` on `manage_jobs`
  `give_job` and `schedule_job`; deliveries in `manage_jobs` (`get_job`,
  `deliveries`, `rerun_failed_steps`, which is `deliver_job` with
  `automation: true`).
- API (`automations:read` to read, `automations:write` to change, and
  `admin` or the local owner to save a host `run` step):
  `GET/POST /v3/workflows`, `GET/DELETE /v3/workflows/{id}` (an id or a
  name; `DELETE ...?expected_revision=N` deletes only that revision and
  refuses one that moved since with 409),
  `GET/POST /v3/workflows/stop`, `GET /v3/job-deliveries`
  (`job_id`, `automation_id`, `state`, `limit`), `GET
  /v3/job-deliveries/{id}`, `POST /v3/job-deliveries/{id}/rerun`,
  `GET /v3/proposals` and `POST /v3/proposals/decide` (below).
- SDK: `client.workflows` (`list`, `get`, `set`, `delete(id,
  expectedRevision?)`, `stopState`,
  `setStopped`, `deliveries`, `delivery`, `rerun`, `proposals`,
  `decideProposal`).

## Proposals: the person decides

An AI never accepts its own proposal. A workflow proposed with
`manage_workflows` `propose`, or a worker with `manage_workers` `propose` (or
`create`), waits for the person, here or with `machine` on a linked machine
(the proposal is stored there).

- **In the chat.** When the Orchestrator proposes, its conversation gets a
  pending request, the same kind the person answers for a tool's permission:
  `requirement: "proposal_decision"`, with the workflow's or worker's name,
  its machine and a readable summary of what accepting does (for a workflow
  change, the steps added, removed and changed against the current revision,
  and the other fields it changes). Allow accepts it, deny discards it. A
  re-proposal refreshes the same request to the new revision. The request
  carries no run: nothing waits on it. The propose answer's
  `decision_request` names it (`permission_id`, or `status: "not_requested"`
  for a caller with no Swarm conversation, such as an MCP client), and its
  `next_action` says to tell the person and wait. The console shows the
  request in the conversation as the proposal (its title, machine and the
  change in words) with Accept and Discard.
- **In the app.** The console's Automations page lists the proposals of the
  machine it is connected to and of each linked machine, with accept and
  discard.
- **One route.** Both run `POST /v3/proposals/decide`
  (`{machine, kind, id, expected_revision, decision}`; `machine` is "" here,
  `kind` is `workflow` or `worker`, `decision` is `accept` or `discard`).
  It runs the machine's own routes: here in process, on a linked machine over
  the link as its home, so the accept routes keep their revision checks and
  the run-step owner rule. `GET /v3/proposals?machine=` lists the pending
  proposals (`kind`, `machine`, `id`, `name`, `revision`, `created_at`,
  `waiting_on: "person"`, `change: new|change`, `replaces`), the same shape
  as `pending_proposals` in `manage_workflows` `list`/`get` and
  `manage_workers` `list`/`review` (both newest first). A decision answers
  every pending request for that proposal in the person's conversations,
  whichever path made it: the machine's own accept and delete routes
  (`POST /v3/workflows/{id}/accept`, `DELETE /v3/workflows/{id}` of a
  proposal, `POST /v3/workers/{id}/accept`, `DELETE /v3/workers/{id}`) answer
  that machine's requests too, as the console's local decisions use them.
  Reading proposals carries the caller's own scoped token (or, for a caller
  with none, a read-only one), so a key that cannot read automations cannot
  read them here either.
- **Trigger key.** Accepting a webhook workflow returns its `trigger_key`
  once, at the top of the person's decision answer (`trigger_key_note`); the
  console shows it once with "copy it now". It never enters the pending
  request, the conversation or an AI's tool result.
- **Only the person.** The decide route takes only an owner session: any
  scoped token (an AI key at any level, an app key, an agent's internal
  call) is refused with 403. A proposal request is refused to
  `resolve_permission`, `resolve_task_permission`, resolve-all and lasting
  rules.
- **Decided once, settled everywhere.** Answering a request decides the
  proposal exactly once: decisions on one proposal run one at a time, and a
  second answer (a double click) returns the recorded outcome
  (`already_answered`) without deciding again. A proposal can be decided
  without the request: in the app, or on the linked machine that holds it
  (its own app or routes, which cannot reach the home's conversations).
  The home then reads the proposal's current state and settles the request
  with that outcome instead of answering with an error: when the person
  answers it (200, `already_decided`, `outcome`, `outcome_text` such as
  "already accepted: now revision 9"), and whenever it lists pending
  requests (`GET /v3/sessions/{id}/permissions`, the session view and the
  sync bootstrap), so a leftover request clears itself on the next read. A
  change proposal that is gone counts as accepted when the workflow it
  changes moved past the revision the request was made against, else as
  discarded; a request from an older build without that revision is
  cancelled with what is known. A listed request's proposal is read at most
  every 30 seconds, and one whose machine cannot be reached stays pending.
  `manage_sessions get` shows such a request as `kind: "proposal"`,
  `answered_by: "person"`.
- **Stale.** A decision names the revision the person saw. If a newer
  revision of the proposal waits (the AI proposed again without that
  conversation), the decision is refused with 409 and the reason, nothing
  changes, the old revision's request is cancelled with that reason, and the
  conversation's request now asks about the new revision. The decide route
  answers a moved proposal with 409, `outcome` and `outcome_text` (what
  became of it) and `requests_answered` (the chat requests it settled).
  Discard removes only a still-pending proposal, with the
  delete guarded by the revision the person saw (a workflow re-proposed or
  accepted in the meantime is refused 409, not deleted); a pending
  change of an active worker cannot be discarded alone (the worker routes
  would delete the worker), so it is accepted or proposed again.

## Example: merge the job into a local branch

The scripts in these examples are examples for owners to adapt, not
defaults: Swarm ships none and runs none on its own.

`/usr/local/libexec/swarm-deliver/merge.sh`, installed with
`sudo install -o root -m 755 merge.sh /usr/local/libexec/swarm-deliver/merge.sh`:

```sh
#!/bin/sh
# Merge each finished job into the folder's "integration" branch.
set -eu
git checkout -q integration
git merge -q --no-ff -m "Merge $SWARM_JOB_ID" "$SWARM_JOB_REF"
git rev-parse HEAD
```

```json
{
  "name": "merge finished jobs",
  "scope": { "folder": "/srv/site" },
  "when": "succeeded",
  "steps": [{ "id": "merge", "type": "run", "command": "/usr/local/libexec/swarm-deliver/merge.sh" }]
}
```

The script runs in `/srv/site` as the daemon's user, so it can check out and
merge there; the step's output is the merge commit. A conflict fails the
step (`git merge` exits non-zero) and leaves the folder for the owner to
resolve.

## Example: push the job's commits to a branch

`/usr/local/libexec/swarm-deliver/push.sh`, installed the same way. It
pushes with the folder's own `origin` remote and the daemon user's Git
credentials (an SSH key or credential helper), like any push from that
account:

```sh
#!/bin/sh
set -eu
branch="swarm/$(basename "$SWARM_JOB_ID")"
git push origin "$SWARM_JOB_REF:refs/heads/$branch"
echo "https://forge.example.com/acme/site/tree/$branch"
```

```json
{
  "name": "push finished jobs",
  "scope": { "folder": "/srv/site" },
  "when": "succeeded",
  "retries": 1,
  "steps": [{ "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh" }]
}
```

The step's output is the branch URL; a failed push signals
`automation.step.failed` and is retried once.

## Example: Swarm's coder, a reviewer, then push

Work given to Swarm's coder is checked, reviewed, and corrected in fresh
sessions. The fix starts at the delivery head; its verified commits move that
head forward. A second reviewer checks only the correction delta and closure
of the first findings. A remaining block never pushes.

```json
{
  "tool": "manage_workflows",
  "arguments": {
    "action": "propose",
    "name": "coder review push",
    "scope": { "folder": "/srv/site", "role": "coder" },
    "steps": [
      { "id": "gate", "type": "run", "command": "/usr/local/libexec/swarm-deliver/check.sh" },
      { "id": "review", "type": "agent", "worker": "reviewer",
        "brief": "Review this task's exact head against its brief." },
      { "id": "push-clean", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh",
        "if": [{ "step": "review", "verdict": "ok" }] },
      { "id": "fix", "type": "agent", "worker": "coder",
        "brief": "Fix these findings, test and commit: {{steps.review.output}}. Prior handoff: {{steps.review.handoff}}",
        "if": [{ "step": "review", "verdict": "block" }] },
      { "id": "review-fix", "type": "agent", "worker": "reviewer", "since": "review",
        "brief": "Check closure and the delta. Fix handoff: {{steps.fix.handoff}}",
        "if": [{ "step": "fix", "status": "succeeded" }] },
      { "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh",
        "if": [{ "step": "review-fix", "verdict": "ok" }] }
    ]
  }
}
```

1. The task ends and `gate` runs the owner's checks; failure stops delivery.
2. `review` starts a fresh read-only reviewer session at the exact head.
3. A clean review runs `push-clean`; otherwise a fresh `fix` coder receives
   findings and handoff in its own worktree, using one correction attempt.
4. `review-fix` receives the previous findings and only the delta after
   `review`'s head. Only its `ok` runs `push` at the updated `SWARM_HEAD`.
   A second block ends without pushing; add explicitly bounded fresh steps
   for more attempts. No step resumes the previous agent's conversation.

Any step can be another of Swarm's own agents instead, for example
`{ "type": "agent", "worker": "finder", "brief": "Write release notes for this change." }`.

Swarm already rebases this delivery by itself when the target moved, before
the push and after a push that failed on a moved target (see
[Rebasing](#rebasing)), so no step is needed for that. The hand-built shape
below (an agent that rebases, then a review of its rebase) still works for a
workflow that wants an agent to rebase instead: let the push fail into a
rebase step, review the rebased head, then push again. The review after a rebase has no
`since`: `since` needs the earlier review's head to be an ancestor of the
delivery head, which a rebase rewrites, so that step is refused there.

```json
{ "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh", "continue_on_failure": true,
  "if": [{ "step": "review-fix", "verdict": "ok" }] },
{ "id": "rebase", "type": "agent", "worker": "coder", "rebase": true,
  "brief": "The push needs a rebase: {{steps.push.output}}",
  "if": [{ "step": "push", "status": "failed" }] },
{ "id": "review-rebase", "type": "agent", "worker": "reviewer",
  "brief": "Check the rebase kept the reviewed change. Handoff: {{steps.rebase.handoff}}",
  "if": [{ "step": "rebase", "status": "succeeded" }] },
{ "id": "push-rebased", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh",
  "if": [{ "step": "review-rebase", "verdict": "ok" }] }
```

A fix step's commits also fast-forward the task's own branch, so a later
follow-up of the task starts from the fixed head; a rebased head does not
(see `agent` steps).

## Example: a review loop

A coder's job is reviewed by a reviewer job in its own session, which may
fix small security problems itself; a block sends the findings back into the
coder's session until the reviewer says ok, at most twice; an ok pushes the
head, including the reviewer's fixes, and when that push fails (the target
branch moved) the job is sent back to rebase. Swarm runs this only because
the automation says so: there is no built-in review chain.

```json
{
  "tool": "manage_workflows",
  "arguments": {
    "action": "propose",
    "name": "review loop",
    "scope": { "folder": "/srv/site", "worker": "<coder worker id>" },
    "when": "succeeded",
    "steps": [
      { "id": "review", "type": "agent", "worker": "wkr_custom_reviewer", "fixes": "security",
        "brief": "Review this job's exact head against its brief." },
      { "id": "fix", "type": "reopen", "from": "review", "max_rounds": 2,
        "message": "The reviewer blocked your work. Its findings:",
        "if": [{ "step": "review", "verdict": "block" }] },
      { "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh",
        "continue_on_failure": true,
        "if": [{ "step": "review", "verdict": "ok" }] },
      { "id": "rebase", "type": "reopen", "from": "review", "max_rounds": 2,
        "message": "Your work passed review, but it could not be pushed: {{steps.push.output}}. Rebase onto dev, resolve any conflicts, run the checks again and commit.",
        "if": [{ "step": "review", "verdict": "ok" }, { "step": "push", "status": "failed" }] }
    ]
  }
}
```

1. The coder's job succeeds: delivery round 0 starts.
2. `review` starts a reviewer job, in its own session, on a branch at that
   exact head. It may commit small security fixes (at most 80 changed lines
   in total) and lists each as `FIXED` in its findings; anything else is a
   BLOCK finding.
3. On `block`, `push` is skipped and `fix` sends the findings into the coder
   job's original session. The coder fixes them in the same worktree; that
   follow-up's end triggers the automation again (round 1), and a new
   reviewer job reviews the new head.
4. After two reopens of the job in total without an `ok`, the next `fix` fails with
   `rounds_exhausted` and the delivery ends `blocked`. A review whose report
   could not be read reopens nothing: `fix` fails with
   `review unreadable: <reviewer job id>`, no round is used and the delivery
   ends `blocked`.
5. On `ok`, `fix` is skipped and `push` runs the owner's push script with
   `SWARM_HEAD` (and `refs/swarm/jobs/<run-id>`) at the reviewed head, or at
   the reviewer's fix head when it committed fixes within the limit. Fixes
   over the limit fail `review` (`reviewer fixes too large`): nothing is
   pushed and the head stays the coder's.
6. When `push` fails (`continue_on_failure` lets the delivery go on),
   `rebase` sends its message alone (an `ok` review has no findings), with
   the push script's last output line in place of `{{steps.push.output}}`,
   into the coder's session. That follow-up's end is the next round: the
   rebased head is reviewed and pushed again. `rebase` and `fix` each count
   their own rounds, so work that needed both of `fix`'s send-backs still
   gets `rebase`'s two; the job as a whole is bounded by 4 (2 + 2).

`manage_jobs` `get_job` on the coder's job shows each delivery's round, head, state,
blocked reason and the reviewer's applied fix commits, and each step's
verdict and findings.

## What the reviewer receives

A reviewer job started by an `agent` step gets, in its brief, after the
step's own brief:

- the original brief of the reviewed job;
- the reviewed job's report (what it says it did and tested);
- the shell commands the job actually ran, with their exit codes, read from
  its session's tool calls (not from the report);
- the diff against its base (cut to fit a job's input, with the `git diff`
  to read the rest). The base is the job's effective base (see
  [The base after a rebase](#the-base-after-a-rebase)), so a job rebased onto
  a newer target is reviewed from that target;
- the folder's target branch at its current tip (`dev is at <sha>`), so the
  reviewer can tell from evidence whether a commit is already on it. Its
  checkout is a worktree of the folder (by default the reviewed job's own,
  see [Where the agent works](#where-the-agent-works-checkout)), so the
  folder's branches are its own refs.

It reviews the exact head in its own session and judges four things: is every
requirement of the brief fully done; do the report's claims match the
evidence (a test the report says passed must appear among the commands run,
with exit code 0; unbacked claims are findings); are there gaps; are there
security problems. It answers `VERDICT: OK` or `VERDICT: BLOCK` (plain
headings at the start of a line; bold-wrapped headings are read too) with
findings at `file:line`, which a `reopen` step sends to the coder verbatim.
With `fixes: "security"` it may also commit small security fixes, listed as
`FIXED` findings (see [Reviewer fixes](#reviewer-fixes-fixes-security)).
Reviewer jobs started outside automations work as before. See
[the reviewer](reviewer.md).

## Example: upload a media file

A job that commits images, video or audio (a coder or designer worker making
assets) lists them in the result's `files` with their sha256. The step reads
each one from the job's head through `$SWARM_JOB_REF` (no checkout needed),
checks its hash and uploads it. `/usr/local/libexec/swarm-deliver/upload-media.sh`
(root-owned, here with `jq`, `sha256sum` and `curl` from the machine, and the
CDN login from the daemon user's `~/.netrc`):

```sh
#!/bin/sh
set -eu
result=$(cat)
count=0
for path in $(echo "$result" | jq -r '.files[].path'); do
  want=$(echo "$result" | jq -r --arg p "$path" '.files[] | select(.path == $p) | .sha256')
  git show "$SWARM_JOB_REF:$path" > "$TMPDIR/upload"
  [ "$(sha256sum < "$TMPDIR/upload" | cut -d' ' -f1)" = "$want" ] || { echo "hash mismatch: $path"; exit 1; }
  curl -fsS --netrc --data-binary @"$TMPDIR/upload" "https://cdn.example.com/upload/$path"
  count=$((count + 1))
done
echo "uploaded $count file(s)"
```

```json
{
  "name": "upload media",
  "scope": { "folder": "/srv/site", "worker": "<designer worker id>" },
  "steps": [{ "id": "upload", "type": "run", "command": "/usr/local/libexec/swarm-deliver/upload-media.sh" }]
}
```

A design job's files are already in the folder at the paths its result
lists. A media job's (`manage_media` `generate`) result lists its files' names,
sha256 and types, but their bytes stay with the job until they are saved
into a folder (`manage_media` `save`); a step does not receive them.

## Custom agent tools for workers

A worker can set optional `agent` to an enabled, non-system custom agent in the same account through `POST /v3/workers`, `PUT /v3/workers/{id}`, `manage_workers` `create` (custom worker creation), or `manage_workers` `update`. An empty string on update restores the role's default tools.

The worker retains its system role, model selection, prompts and checkout behavior. The custom agent supplies its current saved tool contract, including assigned custom tools, and additional prompt instructions. Tool edits apply on the next run without editing the worker. Code-owned tool restrictions still win. Workers without `agent` and custom agents' own sessions keep their existing behavior.

Reviewer saves and run admission reject `write`, `edit`, `git_add`, `git_commit`, `task`, and custom tools with effect `write`; `bash` remains allowed. The workflow fixes option keeps its code-owned reviewer-fixer tools.

`manage_custom_agents` `define` accepts optional `tools_on` and `tools_off` arrays on top of the required `tools` preset (`read_only`, `read_write`, or `build`). For example, `tools: "build", tools_off: ["bash"], tools_on: ["websearch"]` persists explicit switches in the existing contract. Off wins for names in both arrays. Omitting both arrays preserves existing behavior. Permission policy still applies.

## Inputs

Declare reusable string inputs and bind each saved instance with `with`. Names
match `^[a-z][a-z0-9_]{0,31}$` (up to 20); values have at most 1000 characters.
Inputs without a `default` require a value. Undeclared bindings and unknown
`{{inputs.name}}` references are refused. Substitution is literal, not shell
escaping; command args remain separate argv entries.

For example, commit this pipeline as `.swarm/workflows/content.json`:

```json
{
  "swarm_workflow": 1,
  "name": "content",
  "inputs": {"brand": {"description": "Brand to write for"}},
  "scope": {},
  "steps": [
    {"type": "agent", "worker": "coder", "brief": "Plan and make content for {{inputs.brand}}"},
    {"type": "agent", "worker": "reviewer", "brief": "Review content for {{inputs.brand}}"}
  ]
}
```

Call `manage_workflows` `import` twice with the
same `path` and `project`, first
`name: "swarm-content", with: {"brand": "swarm"}`, then
`name: "nameflow-content", with: {"brand": "nameflow"}`. Reimporting the same
name updates that instance. `name` and `with` are accepted only for inline or
single-file imports; directory imports reject either field before saving files.
Export retains declarations and placeholders but
never instance `with` values; defaults are part of the portable definition.

Placeholders work in step briefs, args, messages, deliver paths and workers,
in `for_each.file`, `scope.worker`, `scope.job`, `scope.schedule`,
`on.schedule.cron`, `on.schedule.timezone`, and in `on.event.url`, `items`,
`id`, `on.start.worker` and `brief`. `scope.worker` may be a worker name;
its resolved ID is retained for matching while the saved template is preserved.
`{{event.*}}` in trigger start briefs still expands from the event. Worker names
resolve to exactly one active worker in the account (or use an existing worker
ID); built-in step agents retain their meaning. Trigger starts still require a
deployed worker. Reads retain templates and bindings separately. Use the same
`inputs` and `with` fields with `/v3/workflows` or `manage_workflows`
proposals; approvals are unchanged. Do not put secrets in
inputs, defaults or bindings.

See [Worker daily spending limits](box-limits.md#worker-daily-spending-limits) for per-worker budgets.

## One job per item

A planner can commit `plan.json` containing `{"posts":[{"title":"First post"}]}`.
An agent step then queues one independent job per planned post:

```json
{"type":"agent","worker":"Post maker","brief":"Write {{item.title}}",
 "for_each":{"file":"plan.json","items":"posts","max_parallel":2}}
```

The worker must already be active (id or unique active name), and cannot be a
reviewer or built-in/media/design agent. The file is read at the delivery's
recorded head, never from uncommitted files: at most 256 KiB and 20 JSON objects.
Omit `items` when the file itself is the array. `file` supports `{{inputs.*}}`;
briefs support inputs, `{{item.field}}` (nested dot paths, compact JSON for
objects/arrays, empty text for missing fields), and `{{item}}` (the whole object
as compact JSON with sorted keys). The rendered worker input must fit within
64 KiB, including its JSON envelope. Saving refuses item placeholders outside
`for_each` and unknown brief placeholder namespaces, naming the placeholder.

Set `after_field: "reuses"` to let a 9:16 cut with `{"id":"cut","reuses":"wide"}` wait for the 16:9 video with `{"id":"wide"}` in the same file. `id_field` defaults to `id`. The cut shows `waiting for wide` until that job succeeds and all its job-end workflow deliveries finish successfully, including their deliver/merge steps, so it can use the committed files in the folder branch. A failed/blocked prerequisite or delivery fails the cut without starting it, with `waits for wide, which did not finish: <reason>`. Missing/empty references impose no dependency; unknown references, duplicate IDs, and cycles are refused before queuing. Waiting items consume no parallel slots.

An item follows its job's newest end. When a failed or blocked item job later succeeds (for example after `manage_jobs` `message_job`), the item returns to `done`. Every item that failed only because it waited on it, with `waits for <id>, which did not finish: …`, shows `waiting for <id>` again, along its whole `after_field` chain. These items then start as above: once the job's own workflow deliveries succeed, within `max_parallel`, and at most once each, with the same start key. This happens even when the planner's delivery already finished. That delivery then shows `running` again (with `items_reopened` set to the state it had), but none of its finished steps runs again. Once none of its items is queued, waiting or started, it finishes again with the state it had before. Items that failed for their own reason stay failed while their job has not succeeded, and so do the items waiting on them. For items whose job succeeded before this recovery existed, `manage_jobs` `deliver_job` with `automation: true` on the planner's job (or `POST /v3/job-deliveries/{id}/rerun` on its delivery) applies the same recovery, also when the delivery has no failed step: a failed item returns to `done` once its job's newest end succeeded and its job's workflows delivered (the same check its dependents wait for), and the items that failed waiting on it wait again.

`max_parallel` is 1–5, default 1. The step succeeds after durable queuing; later
steps do not wait. Delivery reads list each item's state, job id and error.
Starts are deduplicated across restart. Each maker job runs its own worker-scoped
job-end workflow, so put review, send-back, merge and draft steps there rather
than after the planner's fan-out. Fan-out is refused when the triggering job is
an item of any workflow, preventing recursive job creation across workflows as
well as within one workflow. Ordinary non-`for_each` job-end steps still run on
item jobs normally.

## An agent that watches the daily work

Give a custom `daily-watch` agent the `manage_jobs` tool and a brief such as:

> List today's failed or blocked worker runs. Read each job and its workflow
> deliveries. Summarize the reason and attempts. For a transient delivery
> failure, rerun its failed steps once; for a correctable job failure, send a
> focused follow-up. Do not act on your own job or workflow delivery.

Start read-only. To authorize repairs, the owner explicitly allows
`manage_jobs.daily-watch.rerun_failed_steps` and/or
`manage_jobs.daily-watch.message_job` in account permission rules. Starting a
separate repair worker additionally needs `manage_jobs.daily-watch.give_job`.
No grant means a clear refusal, not an implicit escalation. These actions use
Control's existing job handlers and session account, not an outside AI key.
See [native agent job tools](agent-tools.md) for filters, pagination, grants
and self-loop restrictions. This is an example brief, not a claim of a live
qualification run.

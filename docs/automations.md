# Workflows

When work ends, Swarm records one **result** for it and runs the **steps the
person (or their AI) configured** for that end. Work is a task of Swarm's own
agents (`swarm_run_task`: swarm, coder, finder, designer; its first run and each
follow-up sent into its session) or a worker job (a worker run or follow-up, a
media job, a design job). Swarm only provides
primitives and reports what happened; it has no built-in merge, rebase,
check, review chain or push.

With no workflow nothing changes: a task or job ends with its branch and
worktree, and the person integrates it by hand (`swarm_integrate_task`,
`manage_jobs` `deliver_job` (or `swarm_deliver_job`), or their own git).

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

Swarm checks right after it lands the work itself (`swarm_integrate_task`, a
`merge` deliver step, `swarm_deliver_job`), and in a sweep every 15 minutes
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
its commits and its branch stay readable: `swarm_get_changes`, the task and
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
`swarm_integrate_task` of such a task answers `already_integrated` from the
record, and the task's delivery state reads `integrated` (`reason_code`
`worktree_removed`). Both hold only when the recorded `head` has at least one
commit beyond the task's base. A task with no commits whose worktree was
removed is never reported as integrated.

Workflows were called job automations. The MCP tools and the API path are
named for workflows (`swarm_set_workflow`, `/v3/workflows`); the internal
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
Both `swarm_set_workflow` and `manage_workflows` include `on` in their tool
schemas. Proposal acceptance remains required for Orchestrator-proposed workflows;
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
| `steps` | Ordered steps, at most 20. |

Every update raises the automation's `revision`. An automation applies only to
task and job ends after it was created. Tasks and jobs started by a workflow
(an `agent` step) never trigger workflows; a follow-up of the original work
ends again and does. A task's follow-up is `manage_projects` `message_task`
(queued into its running run, or reopening it in its own session and worktree
when it has finished) or the person's own message in the task's session; a
job's is `manage_jobs` `message_job` or `swarm_message_job`. `manage_sessions`
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

- `{"step": "<id>", "status": "succeeded|failed"}`: that step's state. A step
  that did not run (skipped or stopped) matches neither.
- `{"step": "<id>", "verdict": "ok|block"}`: for an `agent` step, the verdict
  of the review the job it started recorded. A job that recorded no review has
  no verdict, and a missing verdict never matches.

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
  on `swarm_give_job` or `swarm_schedule_job`, that contains a `run` step
  (HTTP 403, or a tool error), and nothing is stored or started. They may
  still save `agent` and `reopen` steps. The root-owned check below does not
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

### `agent` steps

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
  `swarm_run_task` would, with the brief and the triggering work's result; a
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
  (below).

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
`swarm_list_workflows` and `GET /v3/workflows/{id}` list such steps under
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
started any other way (`swarm_give_job` with `review_job`, or an `agent` step
without `fixes`) stays read-only, detached and report-only. Swarm never
decides on its own to let a reviewer write. `swarm_set_workflow`, `then`
and the API refuse `fixes` on a step whose worker is not a reviewer (a role
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
  `fixes_applied` and, when not applied, `fixes_reason`; `swarm_get_job` on
  the reviewer job shows its fix `commits` and `head` on its
  `agent/review-<run>` branch (read from its checkout; `fix_error` when that
  read fails). `swarm_get_job` on the reviewed job and the delivery views
  show the moved `head` and the applied `fix_commits`.
- A fixing reviewer's review of the reviewed job is recorded with
  `fix_head` when it committed: its OK covers the reviewed head only with
  those fixes, so `swarm_get_job` on the reviewed job shows that review as
  `stale`, never as a current OK of the unfixed head, whether or not the
  step applied the fixes. An OK whose fix checkout cannot be read is
  recorded as BLOCK (`reviewer fixes unreadable`).
- The reviewer's checkout is kept after the job ends, like a coder's job
  checkout, so the delivery and later `run` steps can read its head; it is
  not deleted as a read-only reviewer's checkout is (it is removed only once
  its work is in the target branch, see
  [When Swarm removes a finished job's worktree](#when-swarm-removes-a-finished-jobs-worktree)). Reviewer jobs stay
  undeliverable with `swarm_deliver_job`: their fixes reach a job only
  through the delivery head.

### `reopen` steps

```json
{ "id": "fix", "type": "reopen", "from": "review", "message": "The reviewer blocked your work.", "max_rounds": 2,
  "if": [{ "step": "review", "verdict": "block" }] }
```

- Sends a follow-up into the **triggering job's own session**, exactly as
  `swarm_message_job` does: the same session and worktree, never a new job.
  The text is `message`, then the findings of the `agent` step named by
  `from`, verbatim and in full (up to 16,000 bytes), then: "Fix these in this
  worktree, commit, and report what you changed."
- A step needs `from`, `message` or both. When the `from` step recorded no
  findings (an `ok` review has none) or `from` is omitted, the follow-up is
  `message` alone, then the closing line, and it counts as a round like any
  reopen. With no findings and no `message` the step sends nothing and fails
  with `step <from> recorded no findings to send and the reopen step has no
  message`.
- `message` may hold `{{steps.<id>.output}}`, replaced by the recorded output
  line of the earlier step `<id>` (a `run` step's last non-empty output
  line, at most 500 bytes; an `agent` step's job id), or `(no output)` when
  it recorded none. It must name an earlier step. Nothing else is
  substituted: other text, including other `{{...}}`, is sent as written, and
  substituted output is never expanded again.
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
  so the delegating AI can read the reviewer's session (`swarm_get_job`) and
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
| `merge` | The work's branch is merged into the folder's branch, as `swarm_integrate_task` (a task) or `swarm_deliver_job` (a worker job) do. Refused when the folder has a publish rule (use `publish`). |
| `publish` | A pull request through the folder's publish rule (`install.sh workspace publish`), as `swarm_deliver_job` does. Worker jobs only. |
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
`succeeded`, `failed`, `skipped`, `stopped` or, for a delivery, `blocked` or
`sent_back`), attempts, start and finish times, exit code, output line, log
tail and outcome.

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

The **stop switch** (`manage_workflows` `stop_all`, `swarm_stop_workflows`,
`POST /v3/workflows/stop`) is machine-wide: while it is on no new step
starts; running steps finish, and the next step is recorded as `stopped`.
`manage_jobs` `rerun_failed_steps` (the same as `swarm_deliver_job` with
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
in `swarm_get_job`'s result and in `swarm_get_changes`) is the job's
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
  (`recorded_base` in `swarm_get_changes`).
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
- `automation.step.succeeded` (attrs: `step`, `output`)
- `automation.step.failed` (severity warning; attrs: `step`, `reason` and
  `next`, the next allowed action: `rerun` or `fix the script`; while
  automatic retries remain, also `will_retry` and `retry_at`)
- `automation.delivery.finished` (attrs: `state`, step counts; for
  `sent_back` also `sent_back_step`, `round` and `reason`, severity info)
- `automation.delivery.skipped` (a job end created no delivery; attrs:
  `reason` `duplicate_head`, `head`, `followup`, `status`, and
  `delivery_state` of the delivery, named in `refs.delivery_id`, that
  already covers that head)
- `automation.delivery.blocked` (severity warning; attrs: `reason`
  `rounds_exhausted` or `review unreadable: <reviewer job id>`, `round`,
  `head`, the last `findings` shortened, and `next`: send the findings by
  hand with `manage_jobs` `message_job` (`swarm_message_job`), or after a fix
  `manage_jobs` `rerun_failed_steps` (`swarm_deliver_job` with
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
  refuse `run` steps, and a proposal with one needs the owner to accept it.
  The `swarm_*` workflow tools below remain until the catalog replaces them.
- MCP: `swarm_set_workflow`, `swarm_list_workflows` (every workflow exactly as
  Swarm runs it, with its last 3 deliveries one line each: delivery id, job,
  state, round, head, blocked reason and each step's state and verdict, no
  findings; `delivery_id` reads one delivery's steps and `detail: "deliveries"`
  every workflow's last 3, both bounded, findings cut to 1,500 characters; the
  same options on `GET /v3/workflows` and the Orchestrator's
  `manage_workflows` `list`, whose `deliveries` action also lists one line per
  delivery), `swarm_delete_workflow`, `swarm_stop_workflows`; `then` on
  `swarm_give_job` and `swarm_schedule_job`; deliveries in the catalog tool
  `manage_jobs` (`get_job`, `deliveries`, `rerun_failed_steps`; the same as
  `swarm_get_job` and `automation: true` on `swarm_deliver_job`).
- API (`automations:read` to read, `automations:write` to change, and
  `admin` or the local owner to save a `run` step):
  `GET/POST /v3/workflows`, `GET/DELETE /v3/workflows/{id}`,
  `GET/POST /v3/workflows/stop`, `GET /v3/job-deliveries`
  (`job_id`, `automation_id`, `state`, `limit`), `GET
  /v3/job-deliveries/{id}`, `POST /v3/job-deliveries/{id}/rerun`.
- SDK: `client.workflows` (`list`, `get`, `set`, `delete`, `stopState`,
  `setStopped`, `deliveries`, `delivery`, `rerun`).

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

Work given to the coder that comes with Swarm (`swarm_run_task` with role
`coder`) is reviewed by Swarm's native reviewer; a block goes back into
the coder's own session, where it fixes its own work in its own worktree, at
most twice; an ok pushes the head.

```json
{
  "tool": "swarm_set_workflow",
  "arguments": {
    "name": "coder review push",
    "scope": { "folder": "/srv/site", "role": "coder" },
    "steps": [
      { "id": "review", "type": "agent", "worker": "reviewer",
        "brief": "Review this task's exact head against its brief." },
      { "id": "fix", "type": "reopen", "from": "review", "max_rounds": 2,
        "message": "The reviewer blocked your work. Its findings:",
        "if": [{ "step": "review", "verdict": "block" }] },
      { "id": "push", "type": "run", "command": "/usr/local/libexec/swarm-deliver/push.sh",
        "if": [{ "step": "review", "verdict": "ok" }] }
    ]
  }
}
```

1. The coder's task ends (`task.run.succeeded`): delivery round 0 of job
   `task/<project>/<task>` starts.
2. `review` starts a reviewer job on the task's exact head, as a new session
   in the coder's own worktree (read-only; `checkout` defaults to `same`).
3. On `block`, `fix` sends the findings into the task's own session: queued
   for its run if one is in flight, otherwise starting the task's next run in
   that session (by hand, `manage_projects` `message_task` sends the same kind
   of follow-up). The coder fixes them in the same worktree; that follow-up's
   end starts the workflow again (round 1).
4. On `ok`, `push` runs with `SWARM_HEAD` at the reviewed head. The task's
   commits are in the folder's repository from the moment they are made.

Any step can be another of Swarm's own agents instead, for example
`{ "type": "agent", "worker": "finder", "brief": "Write release notes for this change." }`.

## Example: a review loop

A coder's job is reviewed by a reviewer job in its own session, which may
fix small security problems itself; a block sends the findings back into the
coder's session until the reviewer says ok, at most twice; an ok pushes the
head, including the reviewer's fixes, and when that push fails (the target
branch moved) the job is sent back to rebase. Swarm runs this only because
the automation says so: there is no built-in review chain.

```json
{
  "tool": "swarm_set_workflow",
  "arguments": {
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

`swarm_get_job` on the coder's job shows each delivery's round, head, state,
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
lists. A media job's (`swarm_request_media`) result lists its files' names,
sha256 and types, but their bytes stay with the job until they are saved
into a folder (`swarm_save_media`); a step does not receive them.

## Custom agent tools for workers

A worker can set optional `agent` to an enabled, non-system custom agent in the same account through `POST /v3/workers`, `PUT /v3/workers/{id}`, `swarm_deploy_worker` (custom worker creation), or `swarm_update_worker`. An empty string on update restores the role's default tools.

The worker retains its system role, model selection, prompts and checkout behavior. The custom agent supplies its current saved tool contract, including assigned custom tools, and additional prompt instructions. Tool edits apply on the next run without editing the worker. Code-owned tool restrictions still win. Workers without `agent` and custom agents' own sessions keep their existing behavior.

Reviewer saves and run admission reject `write`, `edit`, `git_add`, `git_commit`, `task`, and custom tools with effect `write`; `bash` remains allowed. The workflow fixes option keeps its code-owned reviewer-fixer tools.

`swarm_define_agent` accepts optional `tools_on` and `tools_off` arrays on top of the required `tools` preset (`read_only`, `read_write`, or `build`). For example, `tools: "build", tools_off: ["bash"], tools_on: ["websearch"]` persists explicit switches in the existing contract. Off wins for names in both arrays. Omitting both arrays preserves existing behavior. Permission policy still applies.

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

Call `manage_workflows` `import` (or `swarm_import_workflow`) twice with the
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
`inputs` and `with` fields with `swarm_set_workflow`, `/v3/workflows`, or native
`manage_workflows` proposals; approvals are unchanged. Do not put secrets in
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

An item follows its job's newest end. When a failed or blocked item job later succeeds (for example after `swarm_message_job`), the item returns to `done`. Every item that failed only because it waited on it, with `waits for <id>, which did not finish: …`, shows `waiting for <id>` again, along its whole `after_field` chain. These items then start as above: once the job's own workflow deliveries succeed, within `max_parallel`, and at most once each, with the same start key. This happens even when the planner's delivery already finished. That delivery then shows `running` again (with `items_reopened` set to the state it had), but none of its finished steps runs again. Once none of its items is queued, waiting or started, it finishes again with the state it had before. Items that failed for their own reason stay failed while their job has not succeeded, and so do the items waiting on them. For items whose job succeeded before this recovery existed, `swarm_deliver_job` with `automation: true` on the planner's job (or `POST /v3/job-deliveries/{id}/rerun` on its delivery) applies the same recovery, also when the delivery has no failed step: a failed item returns to `done` once its job's newest end succeeded and its job's workflows delivered (the same check its dependents wait for), and the items that failed waiting on it wait again.

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

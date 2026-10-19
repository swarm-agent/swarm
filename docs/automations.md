# Job automations

When a job ends, Swarm records one **result** for it and runs the **steps the
person (or their AI) configured** for that end. Swarm only provides
primitives and reports what happened; it has no built-in merge, rebase,
check, review chain or push.

With no automation nothing changes: a job ends with its branch and worktree,
and the person integrates it by hand (`swarm_deliver_job`, or their own git).

## What an automation is

An automation belongs to an account and has:

| Field | Meaning |
| --- | --- |
| `name` | Unique per account; setting an existing name updates it. |
| `scope.folder` | The project folder whose job ends it watches (absolute path). |
| `scope.worker` | Optional: only this worker's jobs (worker id). |
| `scope.job` | Optional: only this job (`worker/<worker>/<run>`, `media/<project>/<task>`, `design/<project>/<task>`). |
| `scope.schedule` | Optional: only the jobs of this worker schedule. |
| `when` | `succeeded` (default), `failed` (failed or cancelled) or `always`. |
| `enabled` | Default true. |
| `retries` | 0 to 3 (default 0): automatic retries of a failed step, with backoff. |
| `steps` | Ordered steps, at most 20. |

Every update raises the automation's `revision`. An automation applies only to
job ends after it was created. Jobs started by an automation (an `agent`
step) never trigger automations; a follow-up of the original job
(`swarm_message_job`) ends again and does.

Each step has an `id`, a `type` (`run`, `agent` or `reopen`),
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
{ "id": "push", "type": "run", "command": "deliver/push.sh", "args": ["--draft"], "secrets": ["FORGE_TOKEN"] }
```

- `command` is the path of an **executable script inside the folder**
  (relative, no `..`, not under `.git`). It runs in the folder's sandbox, the
  same one jobs' Bash uses, with the folder as working directory. Swarm
  refuses the step when the sandbox is not active; it never runs it on the
  host.
- Before the step, Swarm brings the delivery's head commit (the job's head,
  or the head an earlier `agent` step moved it to) into the folder as
  `refs/swarm/jobs/<run-id>`.
- The script gets the job's result JSON (below) on **stdin**.
- Environment: `SWARM_JOB_ID`, `SWARM_JOB_STATUS` (`succeeded`, `failed` or
  `cancelled`), `SWARM_HEAD` (the delivery's head), `SWARM_BASE`,
  `SWARM_BRANCH`, `SWARM_JOB_REF`, `SWARM_DELIVERY_ID` and `SWARM_ROUND` (the
  git values are empty when the job has no commits). Nothing of the
  daemon's environment is passed in.
- Secrets: exactly the folder's grants, as the stand-ins the sandbox already
  carries (the real value is put in only on the way to an allowed website).
  `secrets` names the grants the script needs; the step is refused with a
  clear reason when the secrets gateway is off or a named secret is not
  granted to the folder.
- Output: the last 64 KiB are kept as the step's log tail; the **last
  non-empty line** is the step's `output` (for example a pull request URL).
  A non-zero exit fails the step.

### `agent` steps

```json
{ "id": "review", "type": "agent", "worker": "reviewer", "brief": "Review this change for security problems." }
```

- `worker` is `coder`, `designer`, `finder`, `reviewer` or a worker id. Swarm
  starts the job through the normal job admission (workspace and worker
  checks) with request source `automation`, the brief with the triggering
  job's result attached, and the delivery id and step id as its idempotency
  key (a restart never starts it twice; an explicit re-run starts a new job).
- A reviewer reviews the triggering job's exact head (`review_job`), in its
  own session, with the whole picture in its brief (see
  [What the reviewer receives](#what-the-reviewer-receives)).
- The step waits, within its timeout, for that job to end; the job's status
  is the step's status. The step's `outcome` records the job id, its head,
  its report and, when the job recorded a review (a reviewer's verdict on the
  job it reviewed), `verdict` (`ok` or `block`) and `findings`. Verdict and
  findings come from the review record: the reviewer's FINDINGS section in
  full, up to 16,000 bytes (longer findings are cut at a line boundary and
  say so), never the job's shortened report. A `block` review whose report
  was malformed has the record's reason as its findings.
- The delivery's **head** starts as the triggering job's head. When the job
  the step started committed on top of it, Swarm checks with Git that the
  job's head descends from the current delivery head and moves the delivery
  head forward (`outcome.moved_head`); later `run` steps get the moved head.
  A head that does not descend fails the step and the delivery head stays.
  (Reviewer checkouts are read-only today, so a reviewer job does not move
  the head.)

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
- The step succeeds once the follow-up is accepted; it does not wait. The
  follow-up's end is a new job end, so it triggers the automation again as a
  new delivery whose `round` is the previous round + 1.
- `max_rounds` (default 2, at most 5) bounds the loop: when another reopen
  would exceed it, the step sends nothing, fails with reason
  `rounds_exhausted`, and the delivery ends `blocked`
  (`automation.delivery.blocked`).
- A job that is cancelled or still running is never reopened: the step fails
  with the reason. A step without findings to send fails too.

## Deliveries

Each matching (job end, automation) pair is one **delivery** with a stable
`delivery_id`, made from the job id, the follow-up, the automation id and the
automation revision. A delivery runs at most once; a replayed job end does
not deliver again. A delivery keeps the steps of the revision it started
with, its `round` (0, or the reopening delivery's round + 1 for a follow-up a
`reopen` step sent), its `head` and, when it ended `blocked`, its
`blocked_reason`.

Every delivery and step is recorded durably with its state (`pending`,
`running`, `succeeded`, `failed`, `skipped`, `stopped` or, for a delivery,
`blocked`), attempts, start and finish times, exit code, output line, log
tail and outcome. A daemon restart
resumes unfinished deliveries at the first step that is not finished; a
finished step is never run again.

The **stop switch** (`swarm_stop_automations`, `POST
/v3/job-automations/stop`) is machine-wide: while it is on no new step
starts; running steps finish, and the next step is recorded as `stopped`.
`swarm_deliver_job` with `automation: true` (or `POST
/v3/job-deliveries/{id}/rerun`) re-runs only failed or stopped steps (and the
steps after them that did not run); a succeeded step is never re-run. A
`blocked` delivery re-runs from its first step with a new round budget (a
`run` step that already succeeded is still never re-run).

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
  "review": { "verdict": "...", "findings": [] }
}
```

`git` is present only when the job committed. `files` lists the files a media
or design job produced (path, sha256, type) or the media files a worker's
commits added. `review` is present for a reviewer job that recorded a verdict.
A media job's `kind` is `media` (role `image`, `video` or `sound`); a design
job's is `design`. The result is data written by agents: never run it as
code.

## Signals

Automations report through the machine signal feed (and so to every signal
sink, such as an operator's monitoring), with refs `job_id`, `delivery_id`
and `step_id`:

- `automation.step.started`
- `automation.step.succeeded` (attrs: `step`, `output`)
- `automation.step.failed` (severity warning; attrs: `step`, `reason` and
  `next`, the next allowed action: `rerun` or `fix the script`; while
  automatic retries remain, also `will_retry` and `retry_at`)
- `automation.delivery.finished` (attrs: `state`, step counts)
- `automation.delivery.blocked` (severity warning; attrs: `reason`
  `rounds_exhausted`, `round`, `head`, the last `findings` shortened, and
  `next`: send the findings by hand with `swarm_message_job`, or after a fix
  `swarm_deliver_job` with `automation: true`)

Design jobs now end with `design.job.completed`, `design.job.failed` or
`design.job.cancelled`, and a stopped media job with `media.job.cancelled`.

## Who controls them

- MCP: `swarm_set_automation`, `swarm_list_automations`,
  `swarm_delete_automation`, `swarm_stop_automations`; `then` on
  `swarm_give_job` and `swarm_schedule_job`; deliveries in `swarm_get_job`;
  `automation: true` on `swarm_deliver_job`.
- API (`automations:read` to read, `automations:write` to change):
  `GET/POST /v3/job-automations`, `GET/DELETE /v3/job-automations/{id}`,
  `GET/POST /v3/job-automations/stop`, `GET /v3/job-deliveries`
  (`job_id`, `automation_id`, `state`, `limit`), `GET
  /v3/job-deliveries/{id}`, `POST /v3/job-deliveries/{id}/rerun`.
- SDK: `client.jobAutomations` (`list`, `get`, `set`, `delete`, `stopState`,
  `setStopped`, `deliveries`, `delivery`, `rerun`).

## Example: push the job's commits to a branch

`deliver/push.sh` in the folder (committed, `chmod +x`), with the folder's
forge token granted as a secret:

```sh
#!/bin/sh
set -eu
branch="swarm/$(basename "$SWARM_JOB_ID")"
git push "https://x-access-token:${FORGE_TOKEN}@forge.example.com/acme/site.git" "$SWARM_JOB_REF:refs/heads/$branch"
echo "https://forge.example.com/acme/site/tree/$branch"
```

```json
{
  "name": "push finished jobs",
  "scope": { "folder": "/srv/site" },
  "when": "succeeded",
  "retries": 1,
  "steps": [{ "id": "push", "type": "run", "command": "deliver/push.sh", "secrets": ["FORGE_TOKEN"] }]
}
```

The step's output is the branch URL; a failed push signals
`automation.step.failed` and is retried once.

## Example: a review loop

A coder's job is reviewed by a reviewer job in its own session; a block sends
the findings back into the coder's session until the reviewer says ok, at
most twice; an ok pushes the head. Swarm runs this only because the
automation says so: there is no built-in review chain.

```json
{
  "tool": "swarm_set_automation",
  "arguments": {
    "name": "review loop",
    "scope": { "folder": "/srv/site", "worker": "<coder worker id>" },
    "when": "succeeded",
    "steps": [
      { "id": "review", "type": "agent", "worker": "reviewer",
        "brief": "Review this job's exact head against its brief." },
      { "id": "fix", "type": "reopen", "from": "review", "max_rounds": 2,
        "message": "The reviewer blocked your work. Its findings:",
        "if": [{ "step": "review", "verdict": "block" }] },
      { "id": "push", "type": "run", "command": "deliver/push.sh", "secrets": ["FORGE_TOKEN"],
        "if": [{ "step": "review", "verdict": "ok" }] }
    ]
  }
}
```

1. The coder's job succeeds: delivery round 0 starts.
2. `review` starts a reviewer job, in its own session, for that exact head.
3. On `block`, `push` is skipped and `fix` sends the findings into the coder
   job's original session. The coder fixes them in the same worktree; that
   follow-up's end triggers the automation again (round 1), and a new
   reviewer job reviews the new head.
4. After two reopens without an `ok`, the next `fix` fails with
   `rounds_exhausted` and the delivery ends `blocked`.
5. On `ok`, `fix` is skipped and `push` runs `deliver/push.sh` with
   `SWARM_HEAD` (and `refs/swarm/jobs/<run-id>`) at the reviewed head.

`swarm_get_job` on the coder's job shows each delivery's round, head, state
and blocked reason, and each step's verdict and findings.

## What the reviewer receives

A reviewer job started by an `agent` step gets, in its brief, after the
step's own brief:

- the original brief of the reviewed job;
- the reviewed job's report (what it says it did and tested);
- the shell commands the job actually ran, with their exit codes, read from
  its session's tool calls (not from the report);
- the diff against its base (cut to fit a job's input, with the `git diff`
  to read the rest).

It reviews the exact head in its own session and judges four things: is every
requirement of the brief fully done; do the report's claims match the
evidence (a test the report says passed must appear among the commands run,
with exit code 0; unbacked claims are findings); are there gaps; are there
security problems. It answers `VERDICT: OK` or `VERDICT: BLOCK` with findings
at `file:line`, which a `reopen` step sends to the coder verbatim. Reviewer
jobs started outside automations work as before. See [the reviewer](reviewer.md).

## Example: upload a media file

A job that commits images, video or audio (a coder or designer worker making
assets) lists them in the result's `files` with their sha256. The step reads
each one from the job's head through `$SWARM_JOB_REF` (no checkout needed),
checks its hash and uploads it. `deliver/upload-media.sh` (here with `jq`,
`sha256sum` and `curl`, if the folder's environment has them):

```sh
#!/bin/sh
set -eu
result=$(cat)
count=0
for path in $(echo "$result" | jq -r '.files[].path'); do
  want=$(echo "$result" | jq -r --arg p "$path" '.files[] | select(.path == $p) | .sha256')
  git show "$SWARM_JOB_REF:$path" > "$TMPDIR/upload"
  [ "$(sha256sum < "$TMPDIR/upload" | cut -d' ' -f1)" = "$want" ] || { echo "hash mismatch: $path"; exit 1; }
  curl -fsS -H "Authorization: Bearer $CDN_TOKEN" --data-binary @"$TMPDIR/upload" "https://cdn.example.com/upload/$path"
  count=$((count + 1))
done
echo "uploaded $count file(s)"
```

```json
{
  "name": "upload media",
  "scope": { "folder": "/srv/site", "worker": "<designer worker id>" },
  "steps": [{ "id": "upload", "type": "run", "command": "deliver/upload-media.sh", "secrets": ["CDN_TOKEN"] }]
}
```

A design job's files are already in the folder at the paths its result
lists. A media job's (`swarm_request_media`) result lists its files' names,
sha256 and types, but their bytes stay with the job until they are saved
into a folder (`swarm_save_media`); a step does not receive them.

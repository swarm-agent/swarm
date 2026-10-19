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

Each step has an `id`, a `type`, `timeout_seconds` (default 600, at most
3600) and `continue_on_failure` (default false: a failed step stops the steps
after it, which are recorded as `skipped`).

### `run` steps

```json
{ "id": "push", "type": "run", "command": "deliver/push.sh", "args": ["--draft"], "secrets": ["FORGE_TOKEN"] }
```

- `command` is the path of an **executable script inside the folder**
  (relative, no `..`, not under `.git`). It runs in the folder's sandbox, the
  same one jobs' Bash uses, with the folder as working directory. Swarm
  refuses the step when the sandbox is not active; it never runs it on the
  host.
- Before the step, Swarm brings the job's head commit into the folder as
  `refs/swarm/jobs/<run-id>`.
- The script gets the job's result JSON (below) on **stdin**.
- Environment: `SWARM_JOB_ID`, `SWARM_JOB_STATUS` (`succeeded`, `failed` or
  `cancelled`), `SWARM_HEAD`, `SWARM_BASE`, `SWARM_BRANCH`, `SWARM_JOB_REF`
  and `SWARM_DELIVERY_ID` (empty when the job has no commits). Nothing of the
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
  key (a restart never starts it twice).
- A reviewer reviews the triggering job's exact head (`review_job`).
- The step waits, within its timeout, for that job to end; the job's status
  is the step's status. The step's `outcome` records the job id, its head,
  its report and, for a reviewer, its verdict and findings.

## Deliveries

Each matching (job end, automation) pair is one **delivery** with a stable
`delivery_id`, made from the job id, the follow-up, the automation id and the
automation revision. A delivery runs at most once; a replayed job end does
not deliver again. A delivery keeps the steps of the revision it started
with, and a `round` counter (0; reserved for reopening a job's session).

Every delivery and step is recorded durably with its state (`pending`,
`running`, `succeeded`, `failed`, `skipped` or `stopped`), attempts, start and
finish times, exit code, output line, log tail and outcome. A daemon restart
resumes unfinished deliveries at the first step that is not finished; a
finished step is never run again.

The **stop switch** (`swarm_stop_automations`, `POST
/v3/job-automations/stop`) is machine-wide: while it is on no new step
starts; running steps finish, and the next step is recorded as `stopped`.
`swarm_deliver_job` with `automation: true` (or `POST
/v3/job-deliveries/{id}/rerun`) re-runs only failed or stopped steps (and the
steps after them that did not run); a succeeded step is never re-run.

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
- `automation.step.failed` (severity warning; attrs: `step`, `reason`,
  `next` = `rerun`, `fix the script` or `fix the setup, then rerun`, and
  `retrying automatically` while retries remain)
- `automation.delivery.finished` (attrs: `state`, step counts)

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

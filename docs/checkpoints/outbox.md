# Outbox: a machine's log and results, exported to storage you choose

Status: implemented on `dev` for worker jobs, with focused tests. Optional:
a machine without an outbox works exactly as before, and its own API
(`/v3/signals`, `swarm_get_job`) stays the way to read it.

## Why

One Swarm machine answers its own API. Several machines (a PC, a social
server, a build box) need one place where everything they did lands, that
outlives any of them, that the owner can read from anywhere, and that any AI
on any infrastructure can build its own flows on. The outbox is that: each
machine appends its activity and the results of its jobs to storage the
owner chooses (a local folder, Google Cloud Storage, S3, R2, MinIO). Swarm
only writes; what happens next (an index, a review, a publish step, a
gallery) is a consumer of the same files.

## The shape

Three things, the same on every kind of storage:

1. **A log per machine.** Every signal of the machine's feed (`/v3/signals`)
   becomes one record, numbered in order.
2. **Blobs named by their content.** Files a job produced, and a Git bundle of
   its commits, are stored once under their SHA-256 and never change.
3. **A result per job.** A manifest that ties the job to its outcome: status,
   report, cost, commits and the blobs.

The state of everything is not stored anywhere: it is what a reader gets by
reading the logs in order. Any reader can rebuild it, check it, or keep its
own view.

```
<prefix>/machines/<machine>/log/000000000412.json     one record per signal
<prefix>/machines/<machine>/results/<run_id>.json     one manifest per job result
<prefix>/machines/<machine>/blobs/sha256/<hex>        files and Git bundles
<prefix>/machines/<machine>/status/<ms>.json          the machine's status, about once a minute
<prefix>/deciders/<name>/log/<...>.json               written by consumers, never by Swarm
```

Each machine writes only below its own `machines/<machine>/`, so storage
permissions can give every machine "create objects under my prefix" and
nothing else: it cannot read, overwrite or delete anything, its own files
included.

### Write order and write-once

For one record Swarm writes the blobs first, then the result manifest, then
the log record. A reader that sees a log record can rely on everything it
points to existing. Every object is created only if absent (`If-None-Match: *`
on S3, `x-goog-if-generation-match: 0` on Google Cloud Storage, a hard link on
a folder); "already exists" counts as written, so a retry after a crash never
duplicates or changes anything. Delivery is at least once and in order.

### Machine status

About once a minute each machine writes its box summary (`GET /v3/box/summary`,
today in UTC: schedules, runs today by status with the failed and running
runs, workspaces, build, sandbox, attention, usage) as a new object
`machines/<machine>/status/<milliseconds>.json`: `{v, machine, at, summary}`.
Like every other object it is created once, never overwritten, so a reader
lists the prefix from a few minutes back (`startOffset` / `start-after`) and
takes the last name; its `at` is when the machine was last heard from. It
holds ids, names, counts and times only, never a run's error text. A bucket
lifecycle rule that deletes `status/` objects after a day or two keeps the
prefix small; nothing else depends on old snapshots.

### Reading with a cursor

Log record names are the zero-padded sequence number, so listing a machine's
`log/` prefix in name order is reading it in order. A reader keeps the last
name it processed and lists after it (`start-after` on S3, `startOffset` on
Google Cloud Storage, a sorted directory listing on a folder). Numbers may
skip (signals of other accounts on the machine are not exported); order never
breaks. Bucket notifications (Pub/Sub, S3 events) can wake a reader sooner;
they are optional.

## Formats (version 1)

### Log record

```json
{
  "v": 1,
  "machine": "pc",
  "seq": 412,
  "id": "sig_…",
  "at": 1760000000000,
  "kind": "worker.run.succeeded",
  "severity": "info",
  "source": "swarmd",
  "summary": "Worker coder run succeeded",
  "refs": {"worker_id": "…", "run_id": "…", "session_id": "…"},
  "attrs": {"source": "mcp"},
  "job_id": "worker/<worker_id>/<run_id>",
  "result": "machines/pc/results/<run_id>.json"
}
```

`kind`, `severity`, `summary`, `refs` and `attrs` are the signal's, unchanged
(see `/v3/signals`). `job_id` and `result` are present on a finished worker
job (`worker.run.succeeded`, `worker.run.failed`, `worker.run.cancelled`).
A session run that ends without completing raises `session.run.failed` or
`session.run.interrupted` (warning; `refs.session_id`, `refs.run_id`,
`attrs.agent`), once per run and without the failure reason.
When the machine's feed dropped signals the outbox had not exported yet, one
record of kind `outbox.gap` says so (`attrs.from`, `attrs.to`).

### Result manifest

```json
{
  "v": 1,
  "machine": "pc",
  "job_id": "worker/<worker_id>/<run_id>",
  "run_id": "…",
  "worker": {"id": "…", "name": "coder"},
  "status": "needs_review",
  "outcome": "media",
  "project_id": "…",
  "workspace_path": "/srv/site",
  "report": "…",
  "what_did_do": ["…"],
  "what_not_done": ["…"],
  "cost": {"estimated_cost_usd": 0.41, "total_tokens": 17600, "provider": "…", "model": "…"},
  "git": {
    "branch": "agent/…", "base_branch": "main", "base_commit": "…", "head": "…",
    "commits": [{"sha": "…", "subject": "…"}],
    "files": ["…"],
    "bundle": {"sha256": "…", "size": 12345, "key": "machines/pc/blobs/sha256/…"}
  },
  "files": [
    {"path": "out/clip-03.mp4", "sha256": "…", "size": 1234567, "type": "video/mp4",
     "role": "primary", "key": "machines/pc/blobs/sha256/…"}
  ],
  "skipped": [{"path": "…", "reason": "larger than the outbox file limit"}],
  "exported_at": 1760000000000
}
```

- `status` is the job's status when it was exported (`swarm_get_job`).
- `outcome` is `media` when the job committed images, video or audio,
  `code` when it committed anything else, `report` when it committed nothing.
- `git.bundle` holds the job's commits (`git bundle create … <branch>
  ^<base_commit>`): `git fetch <bundle-file> <branch>` brings them into any
  clone of the repository, without access to the machine.
- `files` are the media files the job's commits added or changed, read from
  the job's head commit (never from the working folder). Thumbnails are the
  worker's own: a committed image next to a video. Swarm never decodes media.

A media job (`swarm_request_media`, job id `media/<project>/<task>`) ends with
`media.job.succeeded` or `media.job.failed` in the feed and exports a result
the same way: `run_id` is its task id, `outcome` is `media`, `kind` is
`image`, `video` or `sound`, and `model`, `aspect_ratio`, `resolution`,
`duration_seconds` and the `brief` say what was asked. `files` are the
generated files (each with the model and settings that made it); a file that
failed is listed in `skipped` with the provider's reason. There is no `git`.

### Decisions (written by consumers)

Swarm defines the place and shape so every consumer agrees; Swarm itself does
not write or act on them.

```json
{"v": 1, "decider": "operator", "at": 1760000000000,
 "result": "machines/pc/results/<run_id>.json",
 "verdict": "approve", "note": "…"}
```

A decider appends to its own `deciders/<name>/log/`; the latest decision per
result wins. An action runner (a publisher, a deploy step) reads decisions and
acts; nothing reaches back into a machine.

## Configuration

The outbox is off until `SWARM_OUTBOX_URL` is set in the daemon's environment.
On the headless box: `install.sh daemon-env set NAME…` with the values on
stdin (a root-only file; values are never shown again). Agents and AI keys
never see them.

| Variable | Meaning |
| --- | --- |
| `SWARM_OUTBOX_URL` | `file:///var/lib/swarmd/outbox`, `gs://BUCKET/PREFIX`, or `s3://BUCKET/PREFIX` |
| `SWARM_OUTBOX_ACCESS_KEY_ID`, `SWARM_OUTBOX_SECRET_ACCESS_KEY` | HMAC key for `gs://` and `s3://` (Google Cloud Storage HMAC keys work) |
| `SWARM_OUTBOX_S3_ENDPOINT`, `SWARM_OUTBOX_S3_REGION` | for `s3://` outside AWS (R2, MinIO) |
| `SWARM_OUTBOX_MACHINE` | the machine's name in the layout; default: the configured Swarm name, else the host name |
| `SWARM_OUTBOX_MAX_FILE_MB` | largest single file exported (default 512); larger files are listed in `skipped` |

`GET /v3/outbox` (owner) reports whether it is on, where it writes (no
credentials), the machine name, how far it got, the lag behind the feed and
the last error. `/v3/box/summary` raises attention when the outbox is failing.

### Least privilege on Google Cloud Storage

- Bucket with uniform access and public access prevention.
- One service account per machine with `roles/storage.objectCreator` on the
  bucket, conditioned on
  `resource.name.startsWith("projects/_/buckets/BUCKET/objects/PREFIX/machines/NAME/")`,
  and an HMAC key for it in the machine's daemon environment.
- One reader (the owner) with `roles/storage.objectViewer`, plus
  `objectCreator` conditioned on its own `deciders/NAME/` prefix.

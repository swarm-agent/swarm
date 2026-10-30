# In-session media

`manage_media` calls the daemon's existing media task service directly, not MCP.
For narration, use `generate` with `kind: sound`, `operation: speech`, an explicit TTS `model`, `text` and `voice` (optional `style`, `language`, `format`); `models` lists voices/formats/limits, `get` returns audio and word- or segment-timing JSON with costs, and `save` writes both. With transcription unset, speech uses the connected Google provider’s first eligible audio-input catalog model in display-name order; if none is available, it returns audio without timings and notes `timings unavailable: no transcription model`.
It is enabled for Designer (including Designer workers), disabled by default for
other built-ins, and can be enabled on custom agents with
`tools_on: ["manage_media"]`.

- `models` lists available image, video and sound models and their options.
- `generate` requires a session project, `kind` and `brief`. Optional fields are
  `model`, `aspect_ratio`, `resolution`, `duration_seconds`, `count`, `operation`
  (video), and `inputs`. Inputs are `{path}` within the session workspace or
  `{job_id, file}` from an earlier media job in the same project; file numbers
  start at 1. Video defaults to one clip. The result contains `job_id`.
- `list` returns only this session's media jobs in its project, newest first,
  with `job_id`, kind, model, status, `created_at`, `media_cost_usd`, `file_count`
  and optional `retry_of`. Filter by `status`; `limit` defaults to 20 (1–100).
  It reads the existing bounded project task listing; no new index is used.
- `retry` accepts an earlier `job_id` in the same project, at any status. It
  copies kind, brief, attached inputs and options without changing the earlier
  task. Override `model`, `brief`, `aspect_ratio`, `resolution`,
  `duration_seconds`, `count` or video `operation`. The new job records
  `retry_of`, also returned by `get`. Ownership, model validation, admission,
  approval and session usage accounting apply just as for generation.
  Managing clients can pass `retry_of` to MCP `swarm_request_media` with the
  same overrides; both transports use the shared retry helper.
- `get` accepts `job_id` and optional `wait_seconds` (0–30), returning status,
  files, model, settings, errors and cost.
- `save` accepts `job_id` and `folder` within the session workspace. Existing
  contents are never overwritten; unchanged files are reused and conflicting
  names use the existing task suffix convention. Workspace ownership and
  protected-path checks still apply.

Media receipts belong to the calling session. Session usage exposes
`image_count`, `video_count`, `sound_count`, `media_cost_usd`, and
`estimated_cost_usd` including model and media costs. Newly recorded receipts
populate these counters; historical receipts are not backfilled by this change.
Existing account spending limits and media batch confirmation still apply.
Generation resolves and pins the selected model, then uses the existing media
cost estimator for worker budget admission. Unknown pricing is not treated as
free under a cost cap: an unpriced receipt counts at a conservative estimate,
and a request still unpriced at admission is reserved at the same per-kind
ceiling for each requested output, refused only when that would cross the cap
(see docs/box-limits.md). Speech priced per token is estimated before
generation from upper bounds on its prompt and audio tokens taken from the
script length; its receipt records the provider's real usage. A speech job's timing transcription is a model call
with audio input; when the catalog has no matching speech rows it is priced at
the model's token rates, audio input at the highest standard-tier audio rate. This worker gate applies only to tasks explicitly created
by `manage_media`, not ordinary project-conversation or approval tasks.
In a session whose tasks setting is `ask`, `generate` creates a task that waits
for approval and returns its pending status. Otherwise, generation runs in the
background; `generate` returns the job ID immediately.
Use `get` with `wait_seconds` to wait. Cancelling the tool context ends only the
wait with a still-generating result, not generation. At creation, current usage
plus estimated cost is checked once against worker, job and account limits.
No allowance is reserved, so background generation never blocks model turns.
Actual receipts charge the originating session and its worker/job scopes;
failures without receipts leave no held state. Concurrent admitted generations
can exceed a cap before receipts arrive. MCP tasks remain asynchronous.

Clients show a job in the conversation that started it from the job itself:
the `generate`/`retry` result's `job_id` (`media/PROJECT/TASK`) names the
project media task, whose `origin_session_id` is the calling session, and the
task's status and deliverables say whether it is generating, ready or failed.
Swarm Console and the web chat draw it as a card under the call; nothing about
the job is copied into the transcript.

`manage_artifact` remains implemented but is disabled for all agents.
The legacy system-image/system-video task launches still require that tool and
therefore cannot complete through those agent paths. They are not rerouted or
removed. Direct media tasks and MCP media tools retain their existing paths.
Specifically, `swarmd/internal/run/service_task_launch.go` resolves
`ImageAgentID` and the matching video agent through `ResolveSystemAgent`, and
`swarmd/internal/run/service_tools.go` reconciles those agents through
`ReconcileSystemAgentSnapshot`. Both launch paths remain unable to produce
artifacts with `manage_artifact` disabled.

Retry `operation` overrides are accepted only for video jobs; supplying one for an image or sound job is refused. Speech jobs cannot currently be retried: create a new speech request with its speech settings instead. This restriction applies to both `manage_media` and MCP retries.

# In-session media

`manage_media` calls the daemon's existing media task service directly, not MCP.
It is enabled for Designer (including Designer workers), disabled by default for
other built-ins, and can be enabled on custom agents with
`tools_on: ["manage_media"]`.

- `models` lists available image, video and sound models and their options.
- `generate` requires a session project, `kind` and `brief`. Optional fields are
  `model`, `aspect_ratio`, `resolution`, `duration_seconds`, `count`, `operation`
  (video), and `inputs`. Inputs are `{path}` within the session workspace or
  `{job_id, file}` from an earlier media job in the same project; file numbers
  start at 1. Video defaults to one clip. The result contains `job_id`.
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
free under a cost cap. This worker gate applies only to tasks explicitly created
by `manage_media`, not ordinary project-conversation or approval tasks.
All generation runs in the background; `generate` returns the job ID immediately.
Use `get` with `wait_seconds` to wait. Cancelling the tool context ends only the
wait with a still-generating result, not generation. Reservations begin at
the generation boundary, not at task creation or while waiting for approval.
Creation errors, rejected/cancelled pending batches and settings failures before
generation do not reserve allowance. Once a generation call starts, uncertain
failures without receipts remain fail-closed. MCP tasks remain asynchronous.

`manage_artifact` remains implemented but is disabled for all agents.
The legacy system-image/system-video task launches still require that tool and
therefore cannot complete through those agent paths. They are not rerouted or
removed. Direct media tasks and MCP media tools retain their existing paths.
Specifically, `swarmd/internal/run/service_task_launch.go` resolves
`ImageAgentID` and the matching video agent through `ResolveSystemAgent`, and
`swarmd/internal/run/service_tools.go` reconciles those agents through
`ReconcileSystemAgentSnapshot`. Both launch paths remain unable to produce
artifacts with `manage_artifact` disabled.

# Swarm Control: MCP access to Swarm, locally and through a relay

Status: implemented on `dev` with focused deterministic tests and a local
end-to-end run (see Validation). Not yet proven against a deployed Cloudflare
relay, claude.ai connectors or routines.

## Surfaces

| Client | Endpoint | Credential |
| --- | --- | --- |
| Local AI client (Claude Code, Claude Desktop, Codex CLI) on the same machine | stdio: `swarm mcp` (native) or `swarmctl mcp` (headless), forwarding to `/mcp` on the owner socket | none to make: a short-lived in-memory AI key the bridge gets as the owner (see Local AI clients) |
| Local MCP client (Claude Code, Desktop) on a native install | `http://127.0.0.1:7781/mcp` or the private socket | scoped token (`swk_…`) |
| Host MCP client for a headless container | `http://127.0.0.1:<sdk-port>/mcp` (`--container-sdk-port`, loopback publish only) | scoped token from `swarmctl setup sdk-token` |
| Remote AI client (claude.ai, ChatGPT, routines) | `https://<relay>/mcp` | OAuth access token issued by your relay |

`/mcp` (`swarmd/internal/api/control_mcp.go`) speaks MCP streamable HTTP in
JSON response mode (protocol `2025-11-25`, `2025-06-18`, `2025-03-26`). It
accepts **only scoped tokens**: attach tokens, desktop sessions and implicit
socket ownership are refused. Cross-origin browser requests, batches and
unsupported protocol versions are rejected.

## Default tools: workers and jobs

By default `tools/list` shows only the worker and job tools, and the server
instructions tell that story: a model provider (the person adds the key), a
project with the person's folders, then a job for the coder, designer or
finder, wait, read the result, follow up, answer questions, merge.
Every other tool below stays callable by name and is listed when the client
asks for all tools (`/mcp?tools=all`, `swarm mcp --all-tools`). The default set
is `controlMCPCoreTools` in `control_mcp.go`; the job tools are in
`control_mcp_jobs.go`.

| Tool | Relay scope | Routes reached |
| --- | --- | --- |
| `swarm_status` | `swarm:read` | `GET /v3/box/summary` (requires `signals:read`); adds `activity_cursor` |
| `swarm_activity` (`after` cursor, `limit`, `min_severity`) | `swarm:read` | `GET /v3/signals` (requires `signals:read`); worker signals carry a `job_id` |
| `swarm_list_workers` (`include_jobs`) | `swarm:read` | `GET /v3/workers` (`POST /v3/workers:ensure-presets` when a preset is missing), `GET …/runs` |
| `swarm_deploy_worker` (preset, or name, role and instructions) | `swarm:write` | `POST /v3/workers:ensure-presets`, or `POST /v3/workers` then `…/activate` with no folder binding |
| `swarm_give_job` (worker, brief, project, workspace, files, `wait_seconds`) | `swarm:write` | `GET /v3/projects` (the project and one of its folders; refused when there is no project), `POST /v3/workers/{id}/direct` with `workspace_path` and `input.project_id` (Swarm picks that project when the folder is in several); returns `job_id` `worker/<worker>/<run>` |
| `swarm_get_job` (`wait_seconds`, `include_diff`, `include_transcript`) | `swarm:read` | `GET …/runs/{rid}`, the run's session (pending questions, an active follow-up run) and project task; when settled, the `swarm_task_result` view (branch, commits, files, report, cost, diff); `project_id`, `session_id`; the transcript is the session's latest messages |
| `swarm_message_job` (message, `wait_seconds`) | `swarm:write` | `POST /v3/sessions/{id}/messages` in the job's session, on the same branch. While the job's run is active (its first run or a follow-up) the message is queued for that run: the worker gets it with its next tool result, or it starts the next run when that run ends (`sent.delivery`, `sent.queued_for_run_id`). A stopped job continues. Refused while a question is open, after the job was cancelled, or while an earlier follow-up is still queued to start (the refusal names its run) |
| `swarm_answer_job` | `swarm:write` | `POST /v3/sessions/{id}/permissions/{pid}/resolve` for an open `ask_user` question only, never a tool-call approval |
| `swarm_deliver_job` | `swarm:write` | `POST /v3/projects/{pid}/tasks/{tid}/integrate` (a local git merge); when the folder has a publish rule (`swarmctl workspace publish`), the owner's command opens a pull request instead; Swarm never pushes otherwise |
| `swarm_stop_job` | `swarm:write` | `POST /v3/workers/{id}/runs/{rid}/cancel` (or `…/run/stop` for a media job). Cancels a running or stopped job for good |
| `swarm_schedule_job` (worker, brief, schedule, project, workspace) | `swarm:write` | `POST /v3/workers` with the schedule, `…/activate` bound to the folder, `…/automations/{aid}/enable` |
| `swarm_request_media` (image, video, sound, project) | `swarm:write` | `POST /v3/projects/{pid}/tasks` (as `swarm_run_task`); returns `job_id` `media/<project>/<task>` |
| `swarm_list_secrets` | `swarm:read` | `GET /v3/secrets/status` (requires `secrets:read`): names, allowed websites, whether a value is set, unexpired folder grants; never values |
| `swarm_set_environment` (project, workspace, base_image, setup, env, `append_setup`, `unset_env`, max_leases, rebuild, reset, `wait_seconds`) | `swarm:write` | `PUT /v3/sandbox-environments` (or `PATCH` when `append_setup` or `unset_env` is given: a merge that keeps fields left out; `POST /v3/sandbox-environments/rebuild` with rebuild; `DELETE` with reset): stores the folder's definition and builds it in the background (setup as root in a temporary gVisor container on the sandbox network, no host folders; bash and git checked as the sandbox user; image `swarm-env:<hash>`, reused for the same definition); the folder's sandbox runs on its last ready image |
| `swarm_get_environment` (`wait_seconds`, `include_log`) | `swarm:read` | `GET /v3/sandbox-environments`: definition, status, what the folder runs on, build log |

`swarm_list_providers`, `swarm_connect_chatgpt`, `swarm_connect_claude_code`, `swarm_request_provider_key` (full keys only), `swarm_list_models`,
`swarm_set_agent_model`, `swarm_get_usage`, `swarm_list_projects`, `swarm_create_project`
(registers new folders in place), `swarm_get_project`, `swarm_set_project_workspaces` and
`swarm_manage_worker` (below; full keys only) complete the default list. A key lists only
the tools its level allows.

## All tools

| Tool | Relay scope | Routes reached |
| --- | --- | --- |
| `swarm_list_machines` (relay only) | `swarm:read` | — |
| `swarm_list_workspaces` | `swarm:read` | `GET /v1/workspace/list` (tool requires `sessions:read`) |
| `swarm_list_sessions`, `swarm_get_session` (+ `wait_seconds`, `detail_chars` up to 20,000 per message or tool field) | `swarm:read` | `GET /v3/sessions[/{id}]`, `GET …/plans/active`; children: `GET /v3/projects/{pid}/tasks` (orchestrator) or sessions whose `parent_session_id` is this one; pending requests carry decoded `tool_arguments` and `kind` (`approval` or `question`, tool `ask_user`) |
| `swarm_start_session` (prompt **or** plan; optional model, `wait_seconds`) | `swarm:write` | `POST /v3/sessions` (workspace agents) or `POST /v3/projects/{pid}/sessions` (`system-orchestrator`), always `auto`, then `…/messages` or the plan routes; returns `started` (ids only) plus one session read made afterwards; an unregistered `workspace_path` is reported as such |
| `swarm_send_message` (+ `wait_seconds`) | `swarm:write` | `POST /v3/sessions/{id}/messages`; returns `started` plus the current session state |
| `swarm_list_models` | `swarm:read` | `GET /v1/providers`, `GET /v1/model/catalog`, `GET /v1/agent-model-settings` (tool requires `sessions:read`) |
| `swarm_set_session_model` | `swarm:write` | `PUT /v3/sessions/{id}/model-profile` (session-owned selection, validated against the catalog) |
| `swarm_set_agent_model` | `swarm:write` | `PATCH /v1/agent-model-settings`, one role (tool requires `settings:write`) |
| `swarm_set_media_models` | `swarm:manage` | `POST /v1/ui/settings` (tools.image/video/audio default models, media transcription model; requires `settings:write`) |
| `swarm_run_plan` | `swarm:write` | `POST …/plans` (active), `POST …/plan-mode/plans/{pid}/start-automatic` |
| `swarm_stop_run` | `swarm:write` | `POST …/run/stop`, then waits up to 10 s and reports the run's final status (`finished`, `failed`, `stopped` or `stopping`), the plan's state (a stopped plan is `paused`: it keeps progress and resumes only with a new message) and a summary. On a Coder, Designer or Finder job's session the job is `stopped`, not cancelled: it keeps its branch and worktree and `swarm_message_job` continues it |
| `swarm_resolve_permission` (`allow_once`/`deny_once`, `answer` for `ask_user`) | `swarm:approve` | `POST …/permissions/{pid}/resolve` (one decision; Swarm has no session-scoped allowance, and persistent rules stay owner-only) |
| `swarm_add_workspace` | `swarm:write` | `POST /v1/workspace/add` with `context_only` and `require_existing_content` (an existing, non-empty folder outside system/Swarm storage and outside registered workspaces; nothing initialised or committed; an already registered path is returned) (tool requires `sessions:write`) |
| `swarm_create_workspace` | `swarm:write` | `POST /v1/workspace/repository/setup` then `POST /v1/workspace/add`: a new, empty repository under the daemon's workspace folder, only for a project started from scratch (tool requires `sessions:write`) |
| `swarm_list_projects` | `swarm:read` | `GET /v3/projects` (workspaces with roles and `context_status`) |
| `swarm_create_project` | `swarm:write` | `POST /v1/workspace/add` for each new folder (as `swarm_add_workspace`), then `POST /v3/projects` with the folders (by path or id; role `primary_code`, `auxiliary` or `docs`); Swarm then generates `project.md` |
| `swarm_get_project` (+ `wait_seconds`) | `swarm:read` | `GET /v3/projects/{pid}`: workspaces, `project.md` and `context_generation`; the wait is woken by committed project updates on the realtime outbox until generation is `ready` or `failed` |
| `swarm_update_project_context` (`swarm:manage`), `swarm_set_project_workspaces` (`swarm:write`, registers new folders) | | `PATCH /v3/projects/{pid}` with only `project_context` or only the full `workspaces` list; refused (with the next step) until generation is `ready` |
| `swarm_retry_project_context` | `swarm:manage` | `POST /v3/projects/{pid}/context:retry` with the current attempt |
| `swarm_list_tasks` | `swarm:read` | `GET /v3/projects/{pid}/tasks` (orchestrator tasks and worker runs, with branches and review state) |
| `swarm_run_task` (project, role `coder|finder|designer|image|video|sound|swarm`, brief, optional title, workspace, files, model, `wait_seconds`) | `swarm:write` | `POST /v3/projects/{pid}/tasks` with `auto_approve` (the delegating AI is the gate) and a fixed `client_request_id`; coder needs a registered workspace; `files` become a scope paragraph of the brief; `model` is checked against the catalog |
| `swarm_wait_task` (+ `wait_seconds`) | `swarm:read` | `GET /v3/projects/{pid}/tasks/{tid}`, woken by committed realtime records of the task's session or the project's task updates; settles on needs_review, completed, failed, blocked, rejected, needs_input, pending_approval |
| `swarm_task_result` (+ `include_diff`, `diff_chars`) | `swarm:read` | `GET /v3/projects/{pid}/tasks/{tid}` (fresh git and delivery state) plus `GET /v3/sessions/{sid}` for `usage_summary`; commits, files and the optional diff come from the same local Git read as `swarm_get_changes` |
| `swarm_integrate_task` | `swarm:write` | `POST /v3/projects/{pid}/tasks/{tid}/integrate` with the task's own session, agent branch and captured target branch (real git merge, refused on conflict with the conflicting files named: ask the agent with `swarm_send_message` to rebase onto the target branch; if it merges the target in instead, the task's net change is still integrated as one commit; nothing is pushed) |
| `swarm_approve_task` (`approve`/`reject`) | `swarm:manage` | `POST /v3/projects/{pid}/tasks/{tid}/approve` or `…/reject` (the Desktop's buttons) for a `pending_approval` task only, with the task's own session, plan and plan revision as guards; the media `…/accept` alias that publishes deliverables is not reachable |
| `swarm_accept_review` | `swarm:approve` | `GET …/plans/active`, then `POST /v3/sessions/{id}/plan-mode/checkpoints/{cid}/accept` on the checkpoint waiting for review (tool requires `sessions:write`); a last checkpoint completes the plan |
| `swarm_read_file` | `swarm:read` | session detail (worktree root, else workspace) or the registered-workspace list; the file is opened with `os.Root` beneath that folder (no `..`, absolute paths or symlink escape), `.git`, `.env*`, binary files and protected storage refused, 256 KiB cap; directories list up to 200 entries (tool requires `sessions:read`) |
| `swarm_get_changes` (+ `path`, `diff_chars` up to 200,000) | `swarm:read` | session detail, then read-only Git through the sandbox router in the session's worktree against its base commit (or uncommitted changes against `HEAD` without a worktree): commits, changed and untracked files, bounded diff without external diff or textconv helpers (tool requires `sessions:read`) |
| `swarm_get_worker` (+ run) | `swarm:read` | `GET /v3/workers[/{id}[/runs[/{rid}]]]` |
| `swarm_assign_worker_task` | `swarm:write` | `POST /v3/workers/{id}/direct` |
| `swarm_create_worker` (optional `schedule` + `scheduled_plan`) | `swarm:manage` | `POST /v3/workers` (schedule included), `POST …/activate` (primary workspace binding; the workspace must be in one project, or pass `project_id`), `POST …/automations/{aid}/enable` |
| `swarm_update_worker` | `swarm:manage` | `PUT /v3/workers/{id}` (revision-guarded) |
| `swarm_manage_worker` (pause/resume/archive/delete/cancel_run/enable_schedule/disable_schedule) | `swarm:manage` | `POST /v3/workers/{id}/{action}`, `…/runs/{rid}/cancel`, `…/automations/{aid}/enable\|disable` |
| `swarm_get_usage` | `swarm:read` | `GET /v3/usage` (tool requires `sessions:read`); each top model carries `billing` (`subscription`, `metered` or `unpriced`) and `nominal_cost_usd` when known, the summary a `cost_note` explaining zero cost, and unset limits read `none`; limits carry `today_unpriced_receipts` and `today_unpriced_estimate_usd` (the estimate the daily cost limit counts for unpriced receipts) |
| `swarm_set_usage_limits` | `swarm:manage` | `POST /v3/usage/limits` (tool requires `usage:write`) |
| `swarm_list_providers` (+ `wait_seconds` with `provider`) | `swarm:read` | `GET /v1/providers` plus credential metadata from the auth service (count, active `auth_type` and `status`: `signed in` for a sign-in, `verified`/`failed`/`saved, not verified yet` for an API key; latest key link outcome; never key material, last four or labels; tool requires `sessions:read`); `runs_agents` (the provider has a registered runner); for each provider that is not ready, `connect`: `method` (`sign_in` ChatGPT, `claude_code_login`, `api_key`, `machine_identity` Vertex), `needs`, ordered `steps` for the person, and `tool` only when this key may call it; with none ready, `next` lists every agent provider as an equal option and tells the AI to ask the person which they have; `wait_seconds` waits, woken by the key registry, while that provider's link is waiting |
| `swarm_request_provider_key` (local only, not offered over the relay) | `swarm:manage` | none: returns a one-time link to the key page on the loopback API listener (tool requires `admin`); the page saves through `POST /v1/auth/credentials` |
| `swarm_connect_chatgpt` (`start`, or `status` with `login_id` and `wait_seconds` up to 25; local only, not offered over the relay) | `swarm:write` | `POST /v1/auth/codex/oauth/start` with the device method only, `GET /v1/auth/codex/oauth/status` for the caller's own sign-in; the tool calls these handlers itself (a write AI key, or `admin` for a token without levels), the person finishes sign-in in their own browser, and results never carry tokens |
| `swarm_connect_claude_code` (`status` or `activate`; local only, not offered over the relay) | `swarm:write` | `status` probes the daemon user's Claude Code CLI (`claude --version`, `claude auth status`) and returns the `install.sh claude-code` command when it is not installed or signed in; `activate` (signed in only) calls the handler of `POST /v1/auth/credentials` itself with a fixed `claudecode` `cli` record, active, reusing the account's existing record (a write AI key, or `admin` for a token without levels); no Claude credential is read or stored |

**Sessions.** `swarm` (default) works in one workspace and delegates to
sub-agents; `system-orchestrator` runs a project and turns work into tasks for
agents and workers; `system-coder`, `system-designer` and `system-finder` work
alone. `get_session` lists delegated children. `wait_seconds` (at most 110:
relay devices give up after 120 seconds; clients with a 60-second tool timeout
should pass at most 50) waits until the run settles: it ended (`waited.outcome`
`finished`, `failed` or `stopped`, from the session lifecycle) or it waits on a
decision (`waited.reason` `waiting_on_decision`); `timed_out` means it is still
working. The wait is woken by committed V3 outbox records for that session (no
timer polling), then returns the latest state.

**Models.** `list_models` shows runnable providers, their catalog models and
thinking options, and each role's default. A session's model can be chosen at
start or changed later (between runs); both set the session's own model
profile, because Swarm and orchestrator sessions otherwise capture the role
defaults when created and those decide their model. Role defaults need
`swarm:write` and `settings:write` (write AI keys and relay devices carry it):
the coder, designer and finder workers use their role's model on every job.
Models are validated against the live catalog. The account default model,
credentials and permission policy (rules, bypass) are not reachable.

**Provider keys.** The AI never handles a key. `request_provider_key` returns
`http://127.0.0.1:<api-port>/keys/<code>`: a 256-bit, single-use code that
expires in 10 minutes, bound to one provider and to the account and AI key
that asked. The person opens it on the machine and pastes the key into
Swarm's own form. The page exists only on the loopback API listener (never
peer transport, the private socket, Desktop, the scoped-token listener, the
tailnet gateway or the relay), admits only direct loopback requests naming
that listener, requires a same-origin form post, and sends no-store, `DENY`
framing and a strict CSP. The key is saved through the canonical credential
route as the requesting key (owner-only scope, vault, onboarding and
verify-before-save rules apply) and made active; a rejected key is not
stored and the link stays usable for up to five attempts. `list_providers`
then shows `saved`, `failed` with a redacted reason, or `expired`. Codes live
in memory only.

**Review.** Orchestrator tasks that need launch approval wait in
`pending_approval` until `approve_task` (manage level). A plan whose last
checkpoint needs review waits in `waiting_review` until `accept_review`.
`get_changes` and `read_file` show what the agents produced first.
Finished orchestrator tasks and worker runs wait in
`needs_review`. A worker run forks from the workspace's current branch (its
integration target); work reaches that branch only through `integrate_task`.
Disabling a schedule cancels its runs still in flight.

**Schedules.** A worker's schedule is part of the create request, so it is
approved with the worker, and is enabled only after the workspace binding. The
scheduled plan is a template without plan identity. Later worker and schedule edits apply when saved, without owner acceptance.
Already admitted jobs retain their pinned revision; subsequent jobs use the new
revision. Legacy pending reviews remain available for explicit acceptance.

**No plan mode.** Callers author the plan (goal, constraints, checkpoints with
acceptance criteria). The tool validates it with Swarm's strict executable-plan
rules, saves it as the session's active plan and starts automatic execution;
there is no agent-authored plan or approval round trip. Sessions are always
created in `auto` mode.

**Exact route allowlist.** `controlMCPRouteAllowed` matches literal segments
and safe ids only (`[A-Za-z0-9._-]`, no reserved words). Worker acceptance,
token minting, import/migrate, capability grants, plan-mode entry and worker
budgets are not reachable; Swarm already refuses to activate or dispatch
workers that request capabilities. Routes whose handlers check no scope
(workspace list, usage dashboard, usage limits) are gated in the tool layer.

**Token budget.** Results are compact JSON sent once (no `structuredContent`
copy). Sessions, runs, plans and workers are reduced to ids, names and
status; text is truncated (messages 1,500, tool arguments 800, tool output
300 characters, raised per call with `get_session` `detail_chars`) and plans
show only per-checkpoint status. Server instructions name only tools the
caller's level allows. The full tool list
is 57 tools; the default list is 25.

**Local tokens.** `swarmctl setup sdk-token` mints session scopes by default;
add `--workers` (automations read/write), `--usage-limits` (`usage:write`) and
`--settings` (`settings:write`) explicitly for the worker, limit and role-model
tools.

## Local AI clients (`swarm mcp`)

One line connects an AI client on the same machine; nobody makes or pastes a
key:

| Client | Setup |
| --- | --- |
| Claude Code | `claude mcp add swarm -- swarm mcp` |
| Claude Desktop | `claude_desktop_config.json`: `{"mcpServers": {"swarm": {"command": "swarm", "args": ["mcp"]}}}` (full path from `command -v swarm` if the app cannot find it) |
| Codex CLI | `~/.codex/config.toml`: `[mcp_servers.swarm]` with `command = "swarm"` and `args = ["mcp"]` |
| Headless container, client on its host | `claude mcp add swarm -- docker exec -i swarm-headless swarmctl mcp` |

`swarm mcp` execs `swarmctl mcp` (`swarmd/cmd/swarmctl/mcp_bridge.go`), a
stdio MCP bridge: newline-delimited JSON-RPC on stdin/stdout, each message
forwarded unchanged to `/mcp` on the daemon's owner socket
(`<data root>/local-transport/api.sock`), so tools, levels and results are
those of an AI key and the bridge implements none. stdout carries only MCP;
a stopped Swarm, a wrong user or a refusal is reported on stderr. A malformed
line gets a JSON-RPC parse error, a line over 1 MiB an invalid-request error,
and notifications get no reply. The bridge never starts the daemon.

**Trust boundary.** The owner socket (directory `0700`, socket `0600`) admits
only the OS user that runs `swarmd`, and that user already has owner access
to the whole local API, so the bridge grants nothing new. It refuses to run
as root or when the socket belongs to another user. It does not pass owner
authority to the AI: at start it mints, over the socket, an AI key at
`--access full` (default), `write` or `read`, and sends every message with
it, so the AI-key gate (only `/mcp`), the Swarm Control level and route
scopes apply exactly as for a pasted key; token-less socket calls to `/mcp`
stay refused. The key lives only in the bridge's memory (never printed,
logged or written to disk), lasts 30 minutes and is renewed with 15 minutes
left, so a provider key link (10 minutes) survives renewal; replaced keys are
deleted once expired, and every key the bridge minted is deleted when it
exits (stdin closed, SIGINT/SIGTERM), which also ends any key link it handed
out. A bridge killed with SIGKILL leaves an unheld key that expires within
30 minutes. If Swarm refuses the key (for example the owner revoked it), the
bridge answers the pending request with the reason and exits instead of
minting a replacement.

## Relay transport (`swarm-remote-1`)

Reference relay: `packages/swarm-relay` (Cloudflare Worker + Durable Object).
Daemon client: `swarmd/internal/remote`; owner API `/v1/remote*`; CLI
`swarmctl remote`.

1. **Off by default.** `swarmctl remote init --relay URL --name NAME
   [--allow-write] [--allow-approve] [--allow-manage]` creates an Ed25519 device key and a
   device scoped token (stored only in the secrets store) without connecting.
   `enable` connects; `disable` disconnects; `reset` revokes the token and
   deletes the key. Relay URLs must be `https` (`http` only on loopback).
2. **Device authentication.** The device dials `wss://<relay>/device/connect`.
   The relay sends a nonce; the device signs
   `swarm-remote-1\n<relay origin>\n<device id>\n<nonce>` and also sends its
   public key. The relay verifies against `SWARM_DEVICE_KEYS` (configured by
   the owner) or a key stored by pairing. One live connection per device;
   reconnects use exponential backoff.
3. **Pairing.** For an unknown device the relay verifies the signature
   against the offered key, then keeps the connection in `pairing` and sends a
   single-use code (15 minutes; at most 20 waiting). The device shows the code
   only to its owner (`/v1/remote` `pairing_code`, the headless app's Connect
   to Claude step). An AI client authorized with `swarm:manage` calls the relay
   tool `swarm_pair_machine(code)`; the relay stores the key in its Durable
   Object and sends `ready` on the same connection. `swarm_remove_machine`
   forgets a paired machine; `SWARM_DEVICE_KEYS` entries can only be removed
   there. `swarm_list_machines` lists waiting machines by name, never codes.
   `PAIRING=off` disables pairing.
4. **Client authorization.** OAuth 2.1 + PKCE, DCR and CIMD via
   `@cloudflare/workers-oauth-provider`. The consent page shows a code and has
   no Allow button; a connected device's owner approves with `swarmctl remote
   approve CODE`. The decision is bound to the browser's consent handle, scopes
   are clamped to the request and the approving machine's ceiling, and tokens
   are sent only to `ALLOWED_REDIRECT_HOSTS`.
5. **Calls.** The relay checks the token's scopes, routes by `machine` (required
   when several are online) and forwards the JSON-RPC message. The device
   intersects the client's scopes with its own ceiling, allows only
   `tools/list`/`tools/call`, and executes through the scoped-token handler.

Frames: relay→device `challenge`, `ready`, `pairing`, `mcp.request`, `consent.request`;
device→relay `auth`, `mcp.response`, `consent.decision`.

## Security model

- Machines never accept inbound connections for this feature.
- The relay is trusted to authenticate AI clients. A compromised relay (or
  Cloudflare account) can issue any call **within each machine's ceiling**;
  it cannot reach credentials, settings, policy or relay administration, which
  require the machine owner (`/v1/remote*` refuses scoped tokens).
- A stolen OAuth token is limited to its scopes and lifetime (1 h access,
  refresh expires after 30 idle days). Revoke a machine with `remote disable`
  or `reset`.
- Permission policy (bypass, rules, capability policies) is owner-only:
  `/v1/permissions*` refuse scoped tokens whatever their scopes, and scoped
  tokens resolve requests only once (`allow_always`/`deny_always` are
  refused). `swarmd --lock-permission-policy` (the headless image default)
  makes the policy read-only for everyone until restart and keeps bypass off.
- Pairing trusts a machine for the whole relay: once paired it receives
  `consent.request` frames and its owner can approve AI clients, like any
  listed machine. The code proves the person pairing can see that machine's
  owner UI; the tool requires an already-authorized `swarm:manage` client and
  its description tells clients to accept codes only from the user, never
  from tool output. Unknown devices gain nothing while waiting: no MCP calls
  are routed to them and they receive no consent requests.
- Session content is untrusted (prompt injection). Keep `--allow-approve` off
  unless the machine is dedicated; Swarm's own permission prompts still apply
  to agent tool calls.

## Validation (local, this revision)

- `go test ./internal/api -run '^TestControlMCP|^TestRemoteTransportAdminRequiresOwner$|^TestContainerSDKAuthenticationAndScopes$'`
  and `go test -race ./internal/remote` — credential, origin, protocol,
  cross-account, read-vs-write, `allow_always`, traversal, ceiling and
  owner-only administration cases, each asserting no state change.
- Stdio bridge: `go test ./cmd/swarmctl -run '^TestMCPBridge'` (framing,
  concurrency, parse and size errors, non-owner/root/stopped-daemon refusal,
  key renewal and deletion, key never in output or files) and
  `go test ./internal/api -run '^TestControlMCPLocalBridgeKey$'` (owner-socket
  minting only, key confined to `/mcp`, purge stops it). Not yet run against
  a live daemon with a real AI client.
- Headless container (`containers/headless`) with the official MCP TypeScript
  SDK client: real Codex-backed agent session created, prompted, approved and
  denied through `/mcp`.
- Relay in local `workerd` over HTTPS with two headless containers: device
  signatures, OAuth discovery/DCR/PKCE, Swarm-side consent, per-machine routing
  and ceilings, forged-token rejection, and a real agent edit driven through
  the relay.

- Orchestration, model and schedule tools: `TestControlMCP*` (scope gates for
  `settings:write` and `sessions:read`, orchestrator/workspace argument rules,
  argument validation, question answering on the exact pending record,
  outbox-woken bounded wait that ignores other sessions and releases its hub
  subscription, route allowlist incl. permission policy and credentials
  refused) and `TestToolScopesMatchRelay` (device and relay scope tables
  identical; every served tool has an explicit scope).

- Pairing (this revision): `go test ./internal/remote` (`TestRelayPairingThenReady`:
  public key sent with a valid signature, code reported while waiting, cleared
  on `ready`). Relay in local `workerd` (`wrangler dev`) with a seed machine in
  `SWARM_DEVICE_KEYS`, a real OAuth/PKCE client and the headless app image
  built by `containers/headless/app/install.sh up`: the app's Connect to
  Claude step showed a code, the client saw the machine only by name, a wrong
  code was refused, the right code paired it and its tools (e.g.
  `swarm_list_workspaces`) served through the relay; after a container
  restart it reconnected without a new code; `swarm_remove_machine` refused a
  `SWARM_DEVICE_KEYS` machine and returned a removed one to waiting with a new
  code. Not yet run against the deployed Cloudflare relay.

- Review and inspection tools (this revision): `TestControlMCPApprovesPendingTask`
  (approve and reject through the task routes, read grant and non-pending
  tasks refused unchanged), `TestControlMCPAcceptsPlanReview`,
  `TestControlMCPReadsWhatAgentsProduced` (real Git worktree: commits, changed,
  untracked files and bounded diff; traversal, absolute, symlink, `.git`,
  `.env`, binary, protected storage, foreign session and missing scope refused),
  `TestControlMCPSendMessageReturnsCurrentState`, `TestControlMCPWaitsForProviderKey`,
  `TestControlMCPInstructionsFollowLevel` and pure-function tests for settled
  outcomes, stop reports, decoded arguments, billing and provider status. No
  provider or live agent was run.

A deployed Cloudflare relay with a claude.ai custom connector and one headless
container has been exercised for listing machines and workspaces. Not yet
proven: the new orchestration, model and schedule tools against a live
machine, ChatGPT, Claude Code routines, and long-running reconnect behaviour.

### Delivery

`swarm_deliver_job` merges a job into its folder, or opens its pull request
when the folder's owner set a publish command. Nothing is delivered without
that call: a job ends when its agent has built, tested and committed its work.

### Speech narration

`swarm_media_models` now lists supported Gemini TTS voices, WAV format, catalog prices and the conservative 8,000-UTF-8-byte complete-prompt limit. Use `kind: "sound"`, `operation: "speech"`, an explicit model, `text` and `voice`; optional `style`, `language` and `format` supply direction. No cloning, live voice, media inputs, duration selection or multiple outputs.

```json
{"kind":"sound","operation":"speech","model":"gemini-2.5-flash-preview-tts","text":"Welcome to the demonstration.","voice":"Kore","style":"Calm narrator","language":"English","format":"wav"}
```

Speech uses `kind: "sound"` with `operation: "speech"` rather than introducing a new media kind. This reuses the existing sound job, file and receipt lifecycle while keeping speech distinct from music generation; an explicit speech model prevents a music default from being selected.

HTTP project-task creation and SDK `CreateProjectTaskParams` use `agent: "sound"`, `operation: "speech"`, `model`, and nested `speech: {text, voice, style?, language?, format?}`. Existing authentication/project ownership rules apply.

The job lists WAV audio and timing JSON as separate files; `swarm_save_media` saves both side by side. Each file exposes `cost_usd`, `price_status` and `pricing_summary`, backed by separate idempotent generation/transcription receipts. Missing catalog rates or usage stay explicitly `unknown`, not free. Retained audio avoids repeating synthesis after alignment failure. A failed alignment that already incurred usage requires a new job to authorize another paid attempt.

Completed speech media-result shape (the surrounding job envelope retains its ordinary identity/status fields). Angle-bracket values below are placeholders, not measured costs or output; runtime costs and sizes are numbers. Each `file` number selects that file for saving:

```json
{
  "model": "gemini-2.5-flash-preview-tts",
  "cost_usd": "<generation plus transcription cost>",
  "price_status": "known",
  "files": [
    {
      "file": 1,
      "id": "deliv_speech",
      "kind": "audio",
      "status": "ready",
      "title": "<task title>",
      "model": "gemini-2.5-flash-preview-tts",
      "media_type": "audio/wav",
      "size_bytes": "<audio byte count>",
      "sha256": "<audio digest>",
      "cost_usd": "<generation cost>",
      "price_status": "known",
      "pricing_summary": "<catalog-derived summary>"
    },
    {
      "file": 2,
      "id": "deliv_timings",
      "kind": "json",
      "status": "ready",
      "title": "Speech timings",
      "media_type": "application/json",
      "size_bytes": "<JSON byte count>",
      "sha256": "<JSON digest>",
      "cost_usd": "<transcription cost>",
      "price_status": "known",
      "pricing_summary": "<catalog-derived summary>"
    }
  ]
}
```

`known` requires available usage and applicable catalog rates; otherwise the price status is `unknown`. File 2 contains the timing JSON illustrated below, not a second audio stream.

Gemini has no native alignment. Audio above 14 MiB uses a temporary Gemini Files upload rather than exceeding the inline request ceiling; Swarm requests deletion after transcription, including failures. Smaller audio remains inline. Swarm transcribes generated audio through the structured-transcript primitive and shared normalizer, requesting word timestamps with the account's configured Google audio-input transcription model. These are semantic estimates, not forced alignment. The provider-neutral interface permits native alignment later. If only segments are returned, the result explicitly uses `granularity: "segment"` and `segments`; no word times are fabricated.

Illustrative timing JSON (seconds from audio start):

```json
{"granularity":"word","source":"transcription","words":[{"word":"Welcome","start":0.12,"end":0.54}]}
```

Word entries are word-level timestamps returned by transcription, not guaranteed forced alignment. When the transcription only supplies segments, the timing file instead looks like this (illustrative values):

```json
{"granularity":"segment","source":"transcription","segments":[{"word":"Welcome to the demonstration.","start":0.0,"end":2.0}]}
```

Segment entries have the same shape, with `word` containing the segment text. Configure the existing account Google key and transcription model before requesting speech. Invalid transcription fails visibly while preserving generated audio. No live-provider qualification is implied by unit tests.

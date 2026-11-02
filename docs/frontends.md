# Building a client for Swarm

Swarm is a headless app: one daemon that holds every record and runs every
agent. A client is the front end a person uses with it: a desktop console, a
dashboard, a web app. Swarm Console is the reference client, and Swarm's own web
UI is another; both connect, read and act exactly as described here. Build yours
the same way and design its front end however you like.

AI agents that drive Swarm from outside (Claude Code, ChatGPT) do not use this
path: they use Swarm Control, the MCP layer, with an AI key (see
[Clients and agents](#clients-and-agents)).

Every client follows the same path:

1. **Find** Swarm on the machine.
2. **Install** it if it is not there.
3. **Connect** as a client: an owner session of its own.
4. **Read**: one snapshot, then the live stream.
5. **Act**: talk to the Orchestrator, and use the same routes for everything
   else.

Nothing here needs a new port, a public listener or a key in a browser.
Everything is on the machine's loopback API, `127.0.0.1:7781`.

## 1. Find Swarm

```sh
curl -s http://127.0.0.1:7781/v1/onboarding
```

It answers without a session. Read:

- `identity.bootstrapped`: whether Swarm has an owner;
- `config.swarm_name`: the machine's name.

Connection refused means Swarm is not installed (step 2).

## 2. Install

Ubuntu 24.04, x86_64. The installer is the same one people run by hand:

```sh
curl -fsSL https://swarmagent.dev/install -o install.sh
sudo bash install.sh --for-user "$USER" --name office-server --progress=json
```

- **The password.** Try `sudo -n` first (a cached sudo or a NOPASSWD rule
  needs nothing). When sudo needs a password, ask for it in your own client,
  naming the command, and give it to `sudo -S -k -p '' -- COMMAND` on stdin
  only, never in argv, the environment, a file or a log. Desktops without a
  polkit agent cannot show pkexec's prompt. The installer recognizes the person
  behind sudo (`SUDO_USER`, or `PKEXEC_UID` under pkexec) and makes them
  Swarm's owner.
- **`--name`** is the machine's name. Ask the person for it.
- **`--progress=json`** adds one line per step on stdout, next to the human
  output. Read only the lines that start with `{"swarm_progress"`:

  ```json
  {"swarm_progress":{"step":"build","text":"Downloading Swarm v0.1.53"}}
  {"swarm_progress":{"step":"done","text":"Swarm is running"}}
  {"swarm_progress":{"error":"…"}}
  ```

  An `error` line comes before a non-zero exit; show its text as it is.
- For a server, run the same command over SSH with `sudo` on that machine
  (`ssh -t HOST sudo bash install.sh …`).

Show the person where the installer comes from before asking for the password.

## 3. Connect: an owner session for your client

```sh
sudo swarmctl client connect my-console --for-user "$USER"
```

- **What it makes:** Swarm mints an owner session named for your client over its
  owner socket. The token lands in `~/.config/swarm/clients/my-console.token`,
  mode 0600 and readable by the person alone; it is never printed.
- **For a server:**
  - run `sudo swarmctl client connect my-console --device client-computer --print` over SSH (replace `client-computer` with the client's computer name) and keep the
    token in your client's own private file;
  - reach the server's API through an SSH tunnel:
    `ssh -N -L 127.0.0.1:PORT:127.0.0.1:7781 HOST`.
- **Using it:** send the token on every request as `X-Swarm-Token`. It has the
  same rights as the web UI, nothing more.
- **Managing it:** `swarmctl client list` shows the client sessions handed out;
  `swarmctl client revoke NAME` ends one.
- **Signed out:** a `401` means the session was revoked or expired. Ask the
  person to connect again.

Never use Swarm Control (`/mcp`), AI keys (`swk_…`) or a machine's own activity
feed (`/v3/activity`) for a client: those are the agents' path. The fleet's
metadata-only activity stream (`/v3/fleet/activity`, below) is for clients.

## 4. Read: one snapshot, then the live stream

1. **Snapshot.** `POST /v3/sync/bootstrap`:

   ```json
   {"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":20}},
    "history":{"mode":"none"},"resources":{"current_run_state":true},"include_active":true}
   ```

   Keep `snapshot_endpoint_cursor` from the answer.
2. **Full conversations.** `POST /v3/sync/hydrate` with the session ids the
   person opens. A `tail` read is capped by count and by bytes (256 KiB), so
   it can hold a single message of a long conversation. The tail's length
   tells you nothing. A capped tail comes with an `omissions` entry for
   `messages` whose `next_cursor` is `{session_id}:messages:before:{seq}`
   (with `manifest_policy: "manifest"`, also a `history_manifests_by_session`
   descriptor), and the session's `message_count`. Read earlier pages with
   `GET /v3/sessions/{id}/messages?before_seq={seq}` (exclusive, optionally
   `max_bytes`), following `next_before_seq` while `has_more_older` is set,
   one page per request from the person. After a reconnect, do not replace
   history already shown with a shorter tail. Keep it only where it joins
   the new tail (read the pages between them if needed), let the tail's
   copies win, and drop it when Swarm holds nothing before the tail or fewer
   messages than you would show.
3. **Live.** Open the WebSocket
   `/v3/realtime/stream?endpoint_cursor=CURSOR`, with `X-Swarm-Token`.
   - **Frames:** `hello`, `event`, `replay.*`, `keepalive`,
     `task.lifecycle.updated`, `project.updated`, `usage.scope.updated`,
     `auth.credentials.updated`, `auth.denied`,
     `slow_consumer.reconnect_required`.
   - **Agent text:** it streams as it is produced.
4. **Repair.** After a disconnect, resume from your last cursor;
   `/v3/sync/stream` is the durable replay. Never poll.

Swarm's records are the only source of truth. Keep no copy that can disagree
with them.

## 5. Act

**The Orchestrator** is how a person delegates work in a client. It is a Swarm
session:

- **Start one:** `POST /v3/sessions` with
  `{"client_request_id":…,"project_id":…,"agent_name":"system-orchestrator"}`,
  then `POST /v3/sessions/{id}/messages`. Attachments go first to
  `POST /v3/sessions/{id}/media`.
- **Steer a running agent:** keep the composer open while a run is active. A
  plain text message sent then is queued, not a new run: the answer has
  `queued: true` and `queued_for_run_id`, and the stored message has
  `metadata.queued_note: true`. Show it as queued. The agent receives it as
  your message alongside its next tool result, or, if the run ends without
  one, it starts the next turn. Swarm then appends a system message with
  `metadata.source: "feedback_delivery"` and `metadata.message_id` naming it;
  show the message as delivered from then on. A message with attachments still
  waits for the run to end and starts its own.
- **Stop a run:** `POST /v3/sessions/{id}/run/stop`.
- **Answer the agent:** when it asks permission for a tool, the request arrives
  on the stream. Answer it with
  `POST /v3/sessions/{id}/permissions/{permission_id}/resolve`.
- **Workers it proposes:** the Orchestrator never turns a worker on by itself.
  A worker it proposes waits until the person accepts it, whenever they get to
  it: `POST /v3/workers/{id}/accept` at the revision shown, or `DELETE` to
  discard it.

**The Companion** is the person's one persistent assistant, a destination of
its own beside their chats. It is a session that runs as the code-owned
Orchestrator (its tools, task delegation and watches, machine tools and
permissions, under the same account, device and linked-machine policy) plus
its own context bank tool `manage_companion`, on the account's Swarm model,
not tied to a project. A Companion created before it ran as the Orchestrator
moves onto it, keeping its history and model, the next time it is opened:

- **Open it:** `POST /v3/companion` (optional `{"workspace_path":…}`, used only
  the first time) gets or creates it and returns `companion` with
  `session_id`, `state` and the canonical `routes`. It is idempotent: every
  client, project switch, workspace change and restart gets the same session.
  `GET /v3/companion` reads it (`companion: null` before the first open).
- **It needs no workspace:** without `workspace_path` it is created
  checkout-free, never from the current workspace, so it opens and runs with
  no current or registered workspace. A checkout-free Companion has no working
  directory, workspace grants or repository authority: it talks, uses its
  machine tools on the person's device and delegates repository work with
  `manage_projects` `propose_task` in a project that names the source; a
  direct source-bound child task, a cwd/workspace override and
  `manage_workspace` `set_session` are refused. Ordinary chats still need a
  workspace.
- **Talk to it** through its `session_id` with the session routes above.
- **It is not a chat row:** it carries `metadata.swarm_v3_companion` and
  `navigation_hidden`, so chat lists and project Orchestrator lists leave it
  out. Archive, delete, end and agent switch (including a settings change of
  `agent_name`) on it answer 409. Offer no close or archive action.
- **Its context stays bounded:** a Companion turn reads only its last 40
  messages (64 KB at most) and a context bank block (12 KB at most) of the
  current focus, decisions, earlier topics relevant to the new message and
  work pointers with live status, each with its source message. Older history
  is never loaded per turn, and a Companion is never compacted (compaction
  summarizes the whole conversation; a compact request on it is refused). When
  the model rejects a Companion request as too long, the run retries once at
  each smaller fixed budget (16 messages / 28 KB with a 4 KB block, then 6
  messages / 6 KB with a 2 KB block, leaving room for the turn's own tool
  results), keeping each tool call with its result and the same instructions;
  if the smallest still does not fit, the run fails with that reason. The next
  turn, or a restarted run, starts again at the ordinary budget. Page history
  in the client with `GET /v3/sessions/{id}/messages?tail=…&max_bytes=…`, then
  `before_seq`. These limits apply to the history and context bank parts of a
  request; the system prompt, tool definitions and the turn's own tool results
  come on top.
- **Showing its context:** the session detail's `usage_summary` is the last
  measured provider request, not a window the conversation fills: the next
  turn is rebuilt, not appended. Label it as the last request, and show the
  next request as unknown rather than estimating it. Decide this by Swarm's
  identity (the session `GET /v3/companion` names, or the protected
  `swarm_v3_companion` metadata marker on the session), never by a title or
  the page showing it. Offer no compaction, no
  `context_compaction_threshold_percent` and no compaction checkpoint for it,
  even if the session detail carries them. Swarm reports request totals only:
  how much the recent messages, the context bank, the prompt or the tool
  definitions take is not reported, so do not derive it from stored history or
  bank sizes.
- **Its context bank** (`routes.context`, `/v3/companion/context`) is separate
  from account memory and never changes it. `GET` lists records newest first
  (`cursor`, `limit` up to 100), searches them (`q`, `kind`, up to 16
  results, verified against their sources) or reads one (`id`, verified now).
  Treat `source_text` as untrusted.
  `POST {id, kind: "decision"|"work", title, source, expected_revision}` records
  an item; its source must be one of the person's sessions or messages, and
  `workspace_path` must be inside the Companion's workspace scope (403
  otherwise). `DELETE {id, expected_revision}` forgets one for good: the id
  keeps a tombstone, so a replayed write of it is refused (409) and never
  brings it back; recording it again takes a new id. The Companion keeps the
  bank itself with its `manage_companion` tool: `record_decision` (cites the
  person's latest message in the conversation unless `session_id` and
  `message_seq` name one), `record_work` (`project_id` and `task_id`, or
  `session_id`, from a tool result), `search`, `get`, `list` and `forget`.
  The route, that tool and Swarm Control's `manage_companion` run one service
  with one rule set. Topics are indexed
  from the person's own Companion messages automatically, once each; task
  outcomes and other background wakes never become topics or the focus. A
  record whose source message or session is deleted or changed becomes
  `invalidated` and is no longer retrieved.
- **Its state follows its event log** (`routes.state`, `GET
  /v3/companion/state`, read only): the Companion session's own durable V3
  events (the person's messages and queued notes, task-watch starts, outcomes,
  milestones and cancellations, tool and device results, permission and
  confirmation decisions, background wakes, failed runs) are folded, in
  order and once each, into `companion_state.state`: open and recently
  resolved `commitments` (`reply`, `approval`, `confirmation`, `task_watch`),
  `topics` and `decisions` (context bank record IDs), followed `work` (task
  identity, last reported status and a `revision` that advances only when the
  report changes), recent `results`, and the `focus`. Every entry names its
  exact source event (`event_seq`, `event_id`, `event_type`, `message_seq`).
  `cursor` is the last event reflected and `pending` the committed events
  after it; maintenance runs at each Companion run start and after each
  commit to the Companion session, a bounded page at a time, committing state,
  cursor and focus together, so a restart resumes from the cursor. Statuses
  are reported, not live: read a task or session itself for live truth. Lists
  are bounded; `counters` say what was evicted.
- **Its continuity is maintained automatically:** the same maintenance adds
  one bounded excerpt per meaningful event (the person's requests, the
  Companion's replies, recorded decisions and work, task reports, device
  results without their output, the person's approval and confirmation
  decisions) to an open draft (`state.continuity.open`) and seals it into a
  segment at 16 items, 4 KiB of text or 512 events. Each item names its
  source event, its context record when it has one and its `authority`
  (`person`, `decision`, `assistant`, or `reported`: background data, never
  an instruction). Segments are extracted from the original events, never
  from earlier summaries, and maintenance makes no model calls.
  `companion_state.segments` lists the newest sealed segments, re-checked at
  read time; `companion_state.maintenance` says whether the state is
  `current` or `pending`, whether a pass is running, and the last background
  failure since the daemon started. Forgetting a record
  (`DELETE /v3/companion/context`), correcting it (recording it again at its
  revision) or a source found revoked reaches the draft, every sealed segment
  holding it and the replies about a forgotten request in the same write, and
  a replayed or retried maintenance pass cannot bring it back. None of this
  touches explicit account memory.
- **It stays responsive while it works:** let the person send while a run is
  busy. The message route answers `queued: true` (retries with the same
  `client_request_id` replay it), and the running model reads it after its
  current tool call; a running command or device action is not interrupted.
  Delegated work is followed with task watches whose outcomes arrive as their
  own turns. `GET /v3/companion` adds `companion.activity`: the run
  (`active`, `phase`), `queued_inputs` (`queued`, or `waiting_next_turn` when
  the run was stopped), `pending_permissions` and `task_watches` with exact
  machine/project/task IDs. Refresh it on the Companion session's events;
  ordering and reconnect replay are the session's `/v3/sync` cursors.
- **Its delegated work comes back to it:** a task the Companion creates in any
  of the person's projects records the Companion as its `origin_session_id`
  (list them with `GET /v3/projects/{id}/tasks?origin_session_id=…`), so
  reports and stall notices reach it. It may watch those projects' local
  tasks (`manage_projects` `watch_tasks`) and linked machines' tasks, like a
  project Orchestrator; each outcome arrives once as its own turn. It sends a
  change to a task with `message_task` (the follow-up records it as the
  task's origin) and stops one with `stop_task`; `cancel_watch` stops
  following. Only the account user's current Companion qualifies: another
  non-project chat, or the Companion after it was deleted, cannot. Its task
  calls ask for the person's approval exactly as a project Orchestrator's do
  (the same account rules and Start tasks setting).
- **Coverage:** the Companion lifecycle routes are for clients and the SDK
  (`client.companion`); Swarm Control (MCP) reaches the Companion's
  conversation through the ordinary session actions. Its context bank is a
  catalog tool, `manage_companion` (`record_decision`, `record_work`,
  `search`, `get`, `list`, `forget`, and `state`, which runs the same read as
  `/v3/companion/state`): the Companion calls it natively, Swarm
  Control serves it to an AI key holding `sessions:read` (reads) or
  `sessions:write` (changes), and no other agent or conversation gets it (a
  call from any other session is refused). Linked machines do not forward the
  Companion: each daemon has its own per account user, so `manage_companion`
  with `machine` is refused with the same reason on both surfaces, while the
  Companion's `manage_projects` task actions keep `machine` for work on linked
  machines. There is no voice.
- **Delete it** only when the person asks: `DELETE /v3/companion` with
  `{"session_id":…,"confirm":true}` removes it, its history and its context
  bank; the next open starts an empty one.

Everything else uses the same routes as the web UI:

| What | Routes |
| --- | --- |
| Projects and tasks | `/v3/projects…`, `/v3/projects/{id}/tasks…`; what a task changed before merging it: `GET /v3/projects/{id}/tasks/{task}/diff` (base, head, per file path, status, +/− counts, binary, hunks; 400 KB in all, 64 KB a file, `truncated` flags; `?path=` one file in full within 400 KB). The project source routes (`/v3/projects/{id}/sources…`) are not client routes: a machine answers them only to its linked home over the link (`manage_projects` `list_sources`, `inspect_source` and `inspect_files` with `machine`) |
| Workers and their runs | `/v3/workers…` |
| Models and providers | `/v1/agent-model-settings`, `/v1/model-profiles`, `/v1/providers`, `/v1/auth/credentials…`, `/v1/auth/codex/oauth/*` |
| Folders | `/v1/workspace/add`, `/v1/workspace/repository/setup` |
| Usage | `/v3/usage`, `/v3/usage/limits` |
| Media | `/v3/media-staging`, `/v3/sessions/{id}/media`, `/v3/projects/{id}/media` |
| Linked machines | `/v3/fleet…` on the home machine |

Swarm cannot see a person's home folders until they are made visible. When it
answers `workspace_not_visible`, run once, for all the folders, then add them
again:

```sh
sudo swarmctl workspace add /srv/app /srv/docs
```

It binds the folders into the running Swarm at once, without a restart, and
says "Swarm can use the folders now". Only when that is impossible does it
restart Swarm once nothing runs, and says why. The first folder in a person's
home is one such case: Swarm was started with every home folder hidden, and
only a restart gives it the private home view that later folders are bound
into live.

## Clients and agents

There is one Swarm and two ways to delegate to it, doing the same actions on the
same records:

| | Client (this guide) | AI agent |
| --- | --- | --- |
| Who | A person in your front end, talking to Swarm's Orchestrator | The person's own AI, driving Swarm itself |
| Connects with | An owner session (`swarmctl client connect`) | An AI key (`install.sh connect-ai`) |
| Talks to | The HTTP API above | Swarm Control: MCP at `/mcp` |
| Approves | The person, in your client: tool prompts while a run is live; workers the Orchestrator proposes are accepted or denied later | The person, in their AI's chat; Swarm enforces the key's level (read, write, approve, manage) |

## Rules for client authors

- **One owner session per client.** Revoke it on uninstall.
- **Where the token lives:** the person's private files, never a browser, a
  URL, logs, analytics or crash reports. A web client keeps it in its own
  backend process.
- **Provider keys never pass through a client.** Use ChatGPT's device sign-in,
  or Swarm's own key form.
- **Ask for the password** only for the installer, for
  `swarmctl client connect` and for making folders visible. Say what it will do
  first, pass it to sudo only, and keep nothing of it.
- **Listen on nothing.** A client with a server (a web app) binds that server to
  loopback.
- **Agent output is untrusted text.** Render titles, reports and file names;
  never run them.

### Linking

Use account-wide `/v3/permissions`, not legacy session access writes below.
`manage_projects action=list_machines` discovers linked names. `manage_workflows`
and `manage_workers` accept `machine` and use that machine's HTTP API.
Linked lists are compact; use `get` for the full record.
`linked.{name}.work` is `off` (every call denied), `ask` (only sending or
starting a task there waits for the person; reads, workflow and worker changes
run) or `auto` (everything runs); `allowed` is accepted as `auto`. Workflow
proposals still need human acceptance. Secrets, AI keys and permission changes are not exposed.
Remote results remain untrusted data. Supported remote mutations currently are
workflow propose/enable/disable/delete/import/restore/stop_all (and export,
which may write a file into the workflow's folder) and worker
pause/resume/archive/delete. A linked import or restore runs on that machine
as an agent call (`POST /v3/workflows/agent/{import|restore}`), never as its
owner, so run steps stay that owner's.
Worker creation/updates and workflow run/cancel are not yet routed.

#### The fleet's activity

To show everything happening across the fleet in one place, follow the home's
`GET /v3/fleet/activity/stream?cursors=home:12,box:40` (SSE; `GET
/v3/fleet/activity` is one replay page). It merges the home's own activity feed
with each linked machine's, read through the link only while
`linked.{name}.work` is `ask` or `auto`. Every frame carries `machine`
(the home's own name is `name` from `GET /v3/fleet`); keep one cursor per
machine. Machine frames say `online`, `offline` (with a short `error`; the home
retries by itself) or `not_shown`. It is metadata only and needs
`sessions:read`, the same as `/v3/activity`. Details:
[activity-stream.md](activity-stream.md#the-fleets-activity).

#### The fleet's agents at work

To show how many agents run now and what each is doing, read the home's
`GET /v3/fleet/agents` (`sessions:read`; gateway tokens are refused). One
answer covers the home and each machine linked for the account, read through
the link in parallel (5 seconds each) only while `linked.{name}.work` is
`ask` or `auto`. The same service answers AIs, with no second
implementation: Swarm Control's `manage_fleet` `agents_at_work` (through this route)
and the Orchestrator's `manage_projects action=agents_at_work` (today is the
UTC day for both); each takes an optional `machine`:

- `agents`: one entry per run holding an execution lease now (the same set
  `manage_fleet` `status` counts as `running_now`): `machine`, `agent`, `session_id`,
  `run_id`, `title`, the task's `project_id`, `task_id` and `task_title` when
  the run is a task's, `worker_name`, `started_at`, `now` (the last tool call:
  `kind` read/edit/command/commit/other, `tool`, a one-line `target` such as
  a path or a command's first line, `state` running/done/failed), `timeline`
  (the last 12 calls, oldest first), `counts` (`reads`, `edits`, `commands`,
  `commits`, `other`, `last_minute`), `files` (edited, newest first, at most
  24) and `files_total`, and `stage` when a workflow takes the task: `step`
  `code`, `review`, `fix` (with `round`), `push` or `landed`, from the task's
  `progress`. `partial` says the run had more tool events than were read
  (the newest 2048).
- `machines`: the home and each linked machine with `state` `online`,
  `offline` (`reason` says why) or `not_shown` (`reason`: its work rule is
  off), `agents`, `landed_today` and `failed_today`.
- `recent`: the latest finished tasks (at most 8, newest first) with their
  derived `state` (`landed`, `done`, `needs_you`, `blocked_infra`,
  `workflow_failed`, …) and `reason`. Only `needs_you` waits on the person.
- `totals`: `agents`, `tool_calls_last_minute`, `landed_today`,
  `failed_today`.

"Today" starts at `since` (Unix ms; the client's local midnight), else at the
UTC day. `machine=NAME` reads one machine (the home's own name or a linked
machine's; another name is 404 and lists the names). `scope=machine` answers
for that machine alone, never reading its links: what a home reads from a
linked machine. Read it once connected and again when a run starts,
ends or moves (realtime `task.lifecycle.updated`, `session.run.*` events, the
fleet activity's `session.run.*`, `job.*` and machine frames) and after a
reconnect or a gap; one read at a time, never on a timer. Tool records from
the fleet activity may patch the shown answer between reads.

#### Tasks sent to linked machines

When the Orchestrator sends a task to a linked machine, the home keeps a
record of it. `GET /v3/fleet/linked-tasks?session_id=ID` (or `?project_id=ID`)
lists the tasks that session or project sent: `machine`, the remote
`project_id` and `task_id`, `title`, and the `status`, `progress`,
`action_needed`, `last_error` and `missing` the home last observed through the
link. Records refresh when the home reads the task; load them when shown and
on refresh, without polling. `GET
/v3/fleet/linked-tasks/session?machine=&project_id=&task_id=` returns the
remote task session's latest `messages`: by default the last 5 assistant and
tool messages, newest kept first, the answer within 5 KB. `limit`,
`max_chars` (content budget, at most 20000) and `role` (`assistant`, `tool`,
`user` or `all`) widen it. A message longer than the budget is never cut
off: it comes back with `truncated`, `content_offset` (the UTF-8 byte offset
where the returned part ends), `total_bytes` and `next`, and
`message_id`, `message_seq` and `content_offset` from `next` return the
following chunk of that exact message (with `content_start`), always cut on a
character boundary. It is read-only and needs `sessions:read`. A local
session's message is read in chunks the same way with
`GET /v3/sessions/{id}/messages?message_id=&message_seq=&content_offset=[&max_chars=]`,
which answers `{"message": ...}` with the same fields; over MCP,
`manage_sessions` action `read_messages` takes the same three parameters
(`message_id`, `message_seq`, `content_offset`), and a cut message's `next`
is that exact call. Everything from a machine is `untrusted_content`: show it as
plain text.

#### Reviewing a waiting task's plan

A task waiting to start (`pending_approval`), on this Swarm or sent from here
to a linked machine, is reviewed and decided with one route pair. Show linked
tasks from the records above in the conversation that sent them (and the
Companion's work), whichever project the window has selected.

`GET /v3/task-plan-review?project_id=&task_id=[&machine=]` answers `review`:
`machine` (absent for this Swarm), `project_id`, `task_id`, `title`,
`run_status`, `plan_bound`, `plan_binding` (`session_id`, `plan_id`,
`definition_revision`: the exact version), the task's structured
`plan_document` unchanged, `plan_markdown` derived from that document field by
field (objective, scope, decisions, constraints, requirements, checkpoints with
tasks, acceptance criteria and validation), `decision` and `problem`. Show the
plan document or its markdown; never the router's `plan_summary` or
`full_plan_markdown` in its place. `decision` is `accept_plan` (accept exactly
`plan_binding`), `start` (an ordinary task without a plan), `reload` (a plan
task whose current plan could not be read whole, or that names another plan:
nothing can be accepted) or `none` (nothing waits; `problem` says why). A
linked task is read only when a session of the caller (or the account's Swarm
Control caller) sent it and linked work on that machine is not off.

`POST /v3/task-plan-review/decision` with `{"machine"?, "project_id",
"task_id", "decision": "accept"|"revise", "plan_binding"?, "feedback"?}` is the
person's decision; only a user principal decides. Accepting a plan names the
`plan_binding` shown; an ordinary task names none; `revise` needs `feedback`
and sends the plan back for a new version (nothing starts). Swarm reads the
task again first: a missing, stale or other binding, a replaced plan, or a
task that already started is refused, so show the current review and let the
person decide again; a repeated click never starts a second run. The decision
goes to the same approve and refine services as `manage_projects`
`approve_task`/`refine_task` (through the link for a linked task, whose
machine checks the binding again). Start tasks and linked-machine rules set to
auto are not plan acceptance. The answer says only what is known:

- **200** `{"ok": true, "decision", "decided", "review_current"}`: carried out
  on `decided` (`machine`, `project_id`, `task_id` and the exact
  `plan_binding` decided on; none for an ordinary start). With
  `review_current: true`, `review` is the task read after it. With
  `review_current: false` that read failed (`refresh_error`): no review is
  sent, never the one from before the decision. Say what was decided, show
  the state as unknown and read the task again before offering anything.
- **409** `{"ok": false, "error", "review_current"}`: refused because of the
  task's state. `review` is the current one when `review_current` is true;
  otherwise read again.
- **400/403/404**: refused (bad request, linked work off, not sent from here
  or not found); nothing was done.
- **502** `{"ok": false, "outcome_unknown": true, "error", "decided"}`: sent,
  and no answer says whether it was carried out (the link timed out, the
  machine failed). Do not resend; read the task again.
- Any 2xx whose body is empty or cannot be read, or without `ok: true`, is
  not a refusal: Swarm took the decision and may have carried it out. Treat
  the outcome as unknown, do not resend, and read the task again before
  offering another decision.

Bind each read and decision answer to the request and the task it was for:
a review closed, switched to another task or reopened must not take an older
request's answer, and a decision still on its way blocks another decision on
the same task. The SDK's `client.projects.getTaskPlanReview` and
`decideTaskPlan` call these routes (`decideTaskPlan` resolves only on 200;
the others throw with the answer in the error's details).

#### Fleet keys

A provider key the whole fleet uses goes through the home's key manager, never
through your client: `POST /v3/fleet/keys/requests`
`{"provider":"openai","label":"team","machines":["build-box"],"include_home":true}`
returns a one-time `url` to Swarm's own key page on the home. Open it for the
person; they paste the key there. The home verifies it, keeps it sealed and
installs it on each machine over the link; each machine verifies it and
reports. `GET /v3/fleet/keys` shows every key with a fingerprint and, per
machine and for the home, `pending`, `installed`, `failed` (with a reason) or
`removed`; never a value. `POST /v3/fleet/keys/{id}/install` with `machines`
adds machines (a newly linked one); `POST /v3/fleet/keys/{id}/revoke`
(all when no `machines` or `include_home` is given) removes it, and once it is
gone everywhere the home deletes the value. Writes need an owner session; the
machine-side routes are refused through `/v3/fleet/machines/{name}/api/...`.
Details: [access-model.md](access-model.md#fleet-keys).

#### Historical session access (retired; do not use)

An owner client (or a key with `swarm:approve`, never an agent gateway token)
can POST `/v3/sessions/{id}/access` with:

```json
{"client_request_id":"fleet-choice-1","fleet":{"build-box":"ask"}}
```

`fleet` replaces the session's machine-name map; omit it to preserve the map,
or send `{}` to revoke all fleet grants. Levels are `none`, `ask`, and `full`.
Unknown or other-account links are rejected. `folders` is ignored by this route.
Absent or unlinked machines have no access. The agent cannot change the choice.

`manage_projects action=list_machines` lists links and session access without
link credentials or addresses. With `machine`, project/task reads and task
creation, approval, acceptance, reopening, refinement, archiving, messaging
and stopping use the linked machine's existing project API. The tool's
`expected_revision` (an integer, checked on the home as on the machine) is sent
under the name each route reads: `revision` for `reopen_task`
(`.../tasks/{task}/reopen`) and `archive_task` (`.../archive`),
`expected_revision` for `message_task` and `refine_task`. A wrong revision gets
the machine's own `409`. `refine_task` with a `plan_document` is refused with
`machine`: the machine's refine route takes feedback only.

The Orchestrator redirects or stops a task it delegated with `manage_projects`
`message_task` and `stop_task`, here or with `machine`. Both call the task's
routes: `POST /v3/projects/{id}/tasks/{task}/message` with
`{"message":…}` (`client_request_id` and `expected_revision` optional) queues
the message for the task's running run, exactly as a person's message during a
run, or reopens a finished task with it as the follow-up; it answers
`task_id`, `delivered_as` (`queued` or `reopened`) and `run_id`.
A task whose run stopped mid-edit (failed, for example on a provider limit,
or stopped) can be continued this way, or with `reopen_task` / `POST
.../tasks/{task}/reopen`: when the task's own retained worktree holds
uncommitted edits, no run is active in its session (including one waiting on
its delegated tasks) and the reopen would be admitted, Swarm commits every
change, untracked files included, as one checkpoint commit on the task's agent
branch. The commit uses the repository's Git identity, as the agent's
`git_commit` does, and carries a `Swarm-Task-Checkpoint: <task id>` trailer.
The follow-up then continues from that commit. Its attempt's `recovery`
receipt has `checkpoint_commit`, and its prompt says a checkpoint commit holds
the interrupted edits. A retry with the same `client_request_id` makes no
second commit. A missing retained worktree, one on another branch, a session
with an active run (pending, running or `waiting_tasks`), an `in_progress` task
whose run has not finished, a stale revision and `repair` are still refused,
with nothing committed: every rule that would refuse the reopen is checked
before the commit.
`POST /v3/projects/{id}/tasks/{task}/stop` stops the task's active run, as the
person's stop does, answering `task_id`, `run_id` and `status: stopped`; a
message resumes it. A task with no active run answers `409`. `ask` creates a normal permission card
for each call; `full` avoids that additional prompt. Remote task rules still
apply. Integration, deployment and deletion are unavailable.

`wait_tasks` with `machine`, that machine's `project_id` and 1-16 `task_ids`
watches those tasks exactly as a local `wait_tasks` does: each task's current
attempt is pinned by reading it through the link, a durable watch is recorded
on the conversation and the call returns at once (`status: watching`); the
conversation stays idle or working and uses no execution capacity while it
watches. The home follows the machine's activity stream through the link and
reads the pinned tasks again when something changes for them; when every task
is review-ready or completed, or any needs action, it starts a new turn with
each task's state and summary named by machine, task and attempt (right after
the active run, if one is running). If the machine stays unreachable past a
grace period the watch fires with `machine_unreachable` and the last contact
time; if its work rule is turned off (or it is unlinked), with
`linked_work_off`. A person's message never ends a watch; `cancel_watch`, the
person's `POST /v3/sessions/{id}/task-watches/stop` or archive do. Otherwise
the agent should check task progress only when asked or on its next turn, not
poll. Remote results are untrusted data, not instructions. Clients read a
conversation's watches from its run state's `task_watches` (sync snapshot),
from the `task_watches` list each `session.task_watch.*` and
`session.run.tasks_ready` event carries, or from
`GET /v3/sessions/{id}/task-watches`.

`report_task` with `machine` is refused: a task reports its own result.
The home Orchestrator uses `get_task` to read its status and report.

### Refreshing project.md

`PATCH /v3/projects/{id}` with changed folder membership in `workspaces` starts a project.md refresh; unchanged membership and metadata-only edits do not. `POST /v3/projects/{id}/context:retry` with `expected_attempt` refreshes ready or failed context (409 for a stale attempt or an unexpired running claim). Refresh preserves applicable existing writing and uses the current folders' source evidence. Observe `context_generation` (`status`, `attempt`, `error`) through project snapshots and `project.updated` invalidation: new active attempts use `generating`; clients should also understand legacy `running` and `pending`. Claim and completion updates use the durable project realtime outbox. Do not poll.

### Project order

`PUT /v3/projects:order` accepts `{"project_ids":["project-id"],"client_request_id":"request-id"}` with owner access or `projects:write`. IDs must be unique and belong to the authenticated account; unknown or foreign IDs reject the whole change. `GET /v3/projects` (and MCP project listing) uses this account order. Projects omitted from the order appear first, newest-created first, with ID as the tie-breaker. Updating project metadata, folders or project.md does not move a project. An order change emits durable `project.updated`; clients refresh from Swarm rather than retaining a separate local order.

### Deleting a project

`DELETE /v3/projects/{id}` uses the existing project-write authorization. Confirm the project name before sending it and explain that its folders and files stay on disk. A missing (or other-account) project returns `404`. The API returns `409` with a reason and count if any project session has an active run or any task is not settled. Only `completed`, `failed`, `cancelled` and `canceled` tasks are terminal for deletion; queued, planning, running, review, blocked and unknown states refuse deletion.

The store checks tasks and atomically removes the project and all its task records under the same lock used for task writes, then publishes `project.updated` with action `project_deleted`. New task writes to the deleted project fail. The API then archives its sessions through the normal session archive operation; archived conversations remain readable. No project folders, files, worktrees or repositories are removed. Clients refresh Swarm's project list and open its first remaining project.

Session activity is checked before the store deletion, not in the same transaction. Session archiving is a subsequent operation; an archive failure is reported explicitly even though the project has already been deleted. This endpoint is not an atomic project-and-session deletion transaction.

### Account permissions and device execution

Use GET/PATCH `/v3/permissions` for account rules and live state; refresh on
`permissions.updated`. Writes require `expected_revision` and `client_request_id`;
a 409 returns the current record. Session access POST and session-create `access`
are retired (400 pointing to `/v3/permissions`); session access GET is an account
permissions alias. MCP exposes `manage_setup` actions `permissions` and
`set_permissions`; the latter accepts home and linked rules only and needs
`swarm:approve`.

Pending device calls include `target_device` (client session id) and `execution`.
Show `awaiting_approval` calls as normal approval cards. Allow without a result
moves them to `ready` while keeping them pending. Execute only `ready` calls for
the authenticated device's own client session, then resolve with
`approved_arguments.result`. Premature results and results from another device
are refused. Do not derive execution permission from legacy session metadata.
Swarm Console follows this protocol: it shows `awaiting_approval` calls as approval
cards and executes only calls Swarm marked `ready` for its own client session
(`console/src/console/machine_calls.rs`).

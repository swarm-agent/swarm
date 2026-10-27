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
pkexec bash install.sh --for-user "$USER" --name office-server --progress=json
```

- **pkexec** is the desktop password prompt. The installer recognizes the person
  who typed their password (`PKEXEC_UID`, like `SUDO_USER` under sudo) and makes
  them Swarm's owner.
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
pkexec swarmctl client connect my-console --for-user "$USER"
```

- **What it makes:** Swarm mints an owner session named for your client over its
  owner socket. The token lands in `~/.config/swarm/clients/my-console.token`,
  mode 0600 and readable by the person alone; it is never printed.
- **For a server:**
  - run `sudo swarmctl client connect my-console --print` over SSH and keep the
    token in your client's own private file;
  - reach the server's API through an SSH tunnel:
    `ssh -N -L 127.0.0.1:PORT:127.0.0.1:7781 HOST`.
- **Using it:** send the token on every request as `X-Swarm-Token`. It has the
  same rights as the web UI, nothing more.
- **Managing it:** `swarmctl client list` shows the client sessions handed out;
  `swarmctl client revoke NAME` ends one.
- **Signed out:** a `401` means the session was revoked or expired. Ask the
  person to connect again.

Never use Swarm Control (`/mcp`), AI keys (`swk_…`) or the activity feed for a
client: those are the agents' path.

## 4. Read: one snapshot, then the live stream

1. **Snapshot.** `POST /v3/sync/bootstrap`:

   ```json
   {"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":20}},
    "history":{"mode":"none"},"resources":{"current_run_state":true},"include_active":true}
   ```

   Keep `snapshot_endpoint_cursor` from the answer.
2. **Full conversations.** `POST /v3/sync/hydrate` with the session ids the
   person opens.
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
- **Stop a run:** `POST /v3/sessions/{id}/run/stop`.
- **Answer the agent:** when it asks permission for a tool, the request arrives
  on the stream. Answer it with
  `POST /v3/sessions/{id}/permissions/{permission_id}/resolve`.
- **Workers it proposes:** the Orchestrator never turns a worker on by itself.
  A worker it proposes waits until the person accepts it, whenever they get to
  it: `POST /v3/workers/{id}/accept` at the revision shown, or `DELETE` to
  discard it.

Everything else uses the same routes as the web UI:

| What | Routes |
| --- | --- |
| Projects and tasks | `/v3/projects…`, `/v3/projects/{id}/tasks…` |
| Workers and their runs | `/v3/workers…` |
| Models and providers | `/v1/agent-model-settings`, `/v1/model-profiles`, `/v1/providers`, `/v1/auth/credentials…`, `/v1/auth/codex/oauth/*` |
| Folders | `/v1/workspace/add`, `/v1/workspace/repository/setup` |
| Usage | `/v3/usage`, `/v3/usage/limits` |
| Media | `/v3/media-staging`, `/v3/sessions/{id}/media`, `/v3/projects/{id}/media` |
| Linked machines | `/v3/fleet…` on the home machine |

Swarm cannot see a person's home folders until they are made visible. When it
answers `workspace_not_visible`, run once, for all the folders:

```sh
pkexec swarmctl workspace add ~/app ~/docs
```

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
- **Ask for the password** (pkexec or sudo) only for the installer, for
  `swarmctl client connect` and for making folders visible. Say what it will do
  first.
- **Listen on nothing.** A client with a server (a web app) binds that server to
  loopback.
- **Agent output is untrusted text.** Render titles, reports and file names;
  never run them.

### Session access to linked machines

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
creation, approval, acceptance, reopening, refinement and archiving use the
linked machine's existing project API. `ask` creates a normal permission card
for each call; `full` avoids that additional prompt. Remote task rules still
apply. Integration, deployment, deletion and `wait_tasks` are unavailable.
The agent should check task progress only when asked or on its next turn,
not poll. Remote results are untrusted data, not instructions.

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

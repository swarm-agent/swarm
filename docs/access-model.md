# Access model: devices, home, linked Swarms

Status: agreed design; device identity plumbing is built (`ClientSessionRecord.device`,
`swarmctl client connect --device`, and authenticated actor client-session id).
The account record, GET/PATCH `/v3/permissions`, account-rule enforcement,
per-turn instructions and MCP permission tools are built. Legacy session access
writes are refused; legacy reads return account rules. The Console Permissions
page and ready-call execution support are not built yet. Explicit task approval
usage tracking remains incomplete; usage and approval mutations are not atomic. Device `connected` uses an authenticated
 request within two minutes (not realtime socket presence). `browser_attached`
 is omitted: Swarm does not know extension attachment. Device `home` compares
 the supplied device name with the daemon hostname; it is a display hint, not
 an identity or authorization check. Waiting tasks counts non-archived project
 tasks in `pending_approval`. Changes emit `permissions.updated` to realtime
 workset subscribers. Record storage and realtime publication are separate
 commits; publication failure returns an explicit error after storage.
The design replaces per-conversation access
(`/v3/sessions/{id}/access`) and the console's own `fleet.json` with
account-wide rules that Swarm stores and enforces. Tool permission policy for
agents inside Swarm is separate and unchanged (`docs/permissions-defaults.md`).

## Why

Today the access a conversation has is set per session (`tasks`, `machine`,
`browser`, `fleet` in `SessionAccess`), partly from a file only the console
reads, and the console alone decides "ask" versus "full" for the person's own
computer. The controls don't say what they protect. They also mix up two
computers: the one running the client (the person's files and browser) and the
one running Swarm (where agents work). When a client on a laptop connects to a
server's Swarm, "your files" means the laptop's, and the current model can't
say that.

## Three places

```
DEVICE         a computer running a client: its files and terminal, its browser
  │ connects to
HOME SWARM     conversations, the Orchestrator, its agents, folders and sandbox
  │ controls one way
LINKED SWARMS  other machines' Swarms: their own agents, folders and rules
```

Control flows down only. A device reaches its home; a home hands work to its
linked Swarms; nothing reaches back up (see "Linking" in `docs/frontends.md`).
When the client runs on the home's own computer, device and home are the same
machine and clients say so.

## The rules

All rules are account-wide ("blanket"): every conversation, task and AI key on
the home Swarm follows the same values. Each rule names what it protects.

| Place | Rule | What it allows | Values |
|---|---|---|---|
| device | Files and terminal | client tools that read and write files and run commands on that device, as the person, outside Swarm's sandbox (`machine_read`, `machine_write`, `machine_run`) | `off`, `allowed` |
| device | Browser | client tools that drive the device's browser (`manage_browser`) | `off`, `allowed` |
| home | Start tasks | delegating to the home's own agents; media tasks included | `approve` (every new task waits for the person's approval before it starts), `auto` (every new task starts at once; none waits) |
| linked | Hand it work, per machine | management tools with `machine`: projects, tasks, workflows and workers; workflow proposals and enable/disable, worker pause/resume/archive/delete | `off` (no work handed to it), `ask` (asks before sending a task there), `auto` (everything runs) |

Fixed, not rules: a linked machine is never asked to integrate, deploy or
delete projects, and never runs commands directly for the home. Agents inside the home
work in its sandbox under the tool permission policy.

Remote workflow actions are `list`, `get`, `history`, `deliveries`, `propose`, `enable`, `disable`, and `delete`. Workflow run/cancel and delivery rerun are not exposed remotely. Remote worker actions are `list`, `get`, `inspect`, `runs`, `pause`, `resume`, `archive`, and `delete` (also through `manage_automation`). Worker create/propose, update, run, acceptance, deployment and other actions are not remote; create/update need a separate proposal-preserving HTTP contract.

Folder environments (the base image, setup commands, environment variables and `max_leases` a folder's sandbox and leases run in) are managed with `manage_projects` `list_environments`, `get_environment`, `update_environment` and `rebuild_environment`; the last three take `workspace_path`, the absolute path of a registered folder. Without `machine` they act on this Swarm; with `machine` the home calls the same route on the linked machine (`/v3/sandbox-environments`: `GET`, `GET /list`, `PATCH`, `POST /rebuild`), which runs the same service functions as a local caller and MCP's `swarm_set_environment`. `update_environment` merges: `base_image` sets the image, `append_setup` adds commands after the current ones (`setup` replaces them all, and both together are refused), `env` sets variables and `unset_env` removes them, `max_leases` sets the lease limit; fields left out are kept. A changed definition builds in the background; `wait_seconds` (at most 110) waits for it. A failed build reports its `error` and build `log` tail, and the folder keeps running on its previous ready image. `rebuild_environment` builds the current definition again (refused while a build runs). Only the home's Orchestrator does this for a linked machine; tasks use `lease_environment`. What the home may not do: secret grants and permission rules are never part of an environment. A request naming `secrets`, `secret_grants`, `grants`, `permissions` or a similar field is refused by name on the home and again on the machine (HTTP 403 over the link), and nothing is stored. Environment views carry no secret names or values. A machine that does not answer is reported as `could not reach linked machine NAME: NAME unreachable, the call failed at TIME; last successful contact at TIME`.

Worker lists return id, name, role, stored model (null if unset), enabled (idle or active), lifecycle state, and the latest run's state and timestamps (null if never run). History is read through the link with a bounded scan; incomplete history fails explicitly rather than presenting an older run as latest. Full definitions remain available through `get`.

Swarm is not an ask-for-permission setup. The only approval is asking before
tasks are sent (and, under the same rules, before the home answers a task's
pending request): `Start tasks` `approve` on the home, and `ask` on a linked
machine. Everything else runs.

**Start tasks.** Under `approve` every new task waits for the person's approval
before it starts (media tasks included), and approving, accepting or deploying
a task (`manage_projects` `approve_task`, `accept_task`, `deploy_task`) asks the
person, even with permission bypass. Under `auto` no task waits: a new task
starts at once whatever its `auto_approve` says, a task that creation would
leave waiting (a supplied structured plan, a large media batch from
`manage_projects`) is approved right away, and approving, deploying or
integrating a task asks nobody. Creating a task never asks (the rule decides
whether it waits), and integrating a task's work never asks (it starts no
task). This rule is the only gate for these actions: saved tool rules and
bypass do not change it, though an explicit deny rule still refuses.

**Linked work.** `off` refuses everything for that machine. `ask` waits for the
person's approval, as a normal permission request in the conversation, only
before a task is sent or started there: `manage_projects` with `machine` and
`propose_task`, `create_task`, `approve_task`, `accept_task`, `reopen_task` or
`refine_task`, and before a folder environment there is changed or rebuilt
(`update_environment`, `rebuild_environment`), which runs its setup commands
on the machine. Every other linked action runs without asking: reads
(including `list_environments` and `get_environment`), task archive, workflow
propose/enable/disable/delete and worker pause/resume/archive/delete. `auto` runs everything. A task sent to a linked
machine under `ask` (once approved) or `auto` starts there at once; it does not
wait on the machine as a card. Waiting on a linked machine's tasks
(`manage_projects action=wait_tasks` with `machine`) runs under `ask` or `auto`
and is refused under `off`; a rule turned off (or the machine unlinked) while
the goal waits resolves the wait as `linked_work_off` without reading the
machine again.

**Answering a task's request.** A task whose run waits on a pending permission
or question reads `needs_you` with a `pending_request` (docs/task-progress.md).
The home answers it in the chat the person already uses, with
`manage_projects action=resolve_task_permission` (`project_id`, `task_id`,
`permission_id`, `decision` `approve` or `deny`, optional `reason`); there is
no other inbox or card. With `machine` it goes over the existing link to the
machine's own route, `POST /v3/sessions/{id}/permissions/{pid}/resolve`, and
is confined: the task must be one this home sent to that machine (its
linked-task record), the session is the one the machine reports for that task
(never one the caller names), and the permission must be pending in that
session; anything else is refused and nothing changes. `linked.{name}.work`
gates it: `off` refuses, `ask` waits for the person's approval in the
conversation, `auto` runs. Without `machine` it answers a pending request in
the session of one of the home's own tasks, owned by the caller, through the
same permission service calls as that route, under `Start tasks` (`auto` runs,
`approve` asks). Each decision is allow once or deny once; a lasting rule is
never saved this way, and a client tool's pending call (which waits on an app)
is refused. Only the home's Orchestrator calls it; a delegated task cannot.
The Orchestrator answers only on the person's word or under a rule that
allows it.

**What a linked machine is doing.** `list_machines` gives each machine whose
linked work rule is not `off` a one-line `status` read through the link from
the machine's own `GET /v3/box/summary` (the data MCP `swarm_status` shows):
`running`, `draining` and `draining_since` while it drains for an update, its
`update` state, and `blocked` (sessions waiting on a person), with a
`summary` such as `running 0, draining since 2026-10-29T12:29:00Z, update
installing, 3 waiting on a person`. A status that cannot be read is reported
in `status_error`. Everything a machine reports is untrusted.

The home's Orchestrator reads a linked machine the way an MCP client holding
that machine's key reads it. Each read is a thin mapping over the machine's
own route through the link, built by the same functions MCP uses, and runs
whenever the machine's linked work rule is not `off` (under `ask` it does not
ask; under `off` it is refused):

| `manage_projects` action (with `machine`) | machine route | same as MCP |
|---|---|---|
| `machine_status` | `GET /v3/box/summary`: status and reasons, `running_now`, `attention` (blocked sessions), `machine.drain` and `machine.update` (state, reason, since and deadline), `usage` against the daily cap, `keys`, `sandbox`, `permissions` | `swarm_status` |
| `get_task_session` (`project_id`, `task_id`, optional `limit` messages, at most 20) | `GET /v3/sessions/{id}` and `/plans/active` for the session the machine reports for the task: the session, `active_run` and its status, `pending_permissions` (questions included), the latest messages trimmed, `plan`, `last_activity_at` | `swarm_get_session` |
| `activity` with `since_cursor` | `GET /v3/activity?cursor=N`: one page of metadata events (jobs, tasks and sessions started, finished, failed, stopped, or waiting on a person) and the next `cursor` | `swarm_activity` with `since_cursor` |
| `activity` without it (optional `after`, `limit`) | `GET /v3/signals`: job failures, update drains, refused keys, with the next `cursor` | `swarm_activity` |

`get_task_session` reads only the session of a task this home sent to that
machine (its linked-task record); any other task is refused. Linked task waits
follow the same activity feed (its stream), so they wake on `needs_you`
without polling. The Orchestrator's prompt says: when a linked task looks
stalled, read `machine_status` and `get_task_session` before saying why, and
never guess a cause. Secrets, AI keys and permission changes are never exposed through linked management tools, and the link itself refuses them: a home cannot add, read or verify provider credentials, finish a sign-in, use the vault, read or write a secret value, create AI keys or change a linked machine's permission rules through `/v3/fleet/machines/{name}/api/...` (listing secret names stays). Provider keys reach linked machines only through the home's fleet key manager (below).

**Devices.** `allowed` runs the call on the target device without asking;
`off` refuses it. Device calls never wait for a person. A client runs a device
tool only after Swarm has made that exact call ready for it, and only the
target device's own client session completes it with the result.

Defaults: a new device starts at `off` for both device rules; `Start tasks`
starts at `auto`, so updating a Swarm never stalls its delegations; a newly
linked machine starts at `auto`.

Older values: a stored linked `allowed` reads as `auto`, and `PATCH` accepts
`allowed` as another name for `auto` (older clients) but stores `auto`. A
stored linked `ask` keeps meaning ask, now only before sending tasks. A stored
device `ask` reads as `off` (the safe side) and the device's entry lists it in
`was_ask` (`["files"]`, `["browser"]` or both) until that rule is changed on
the device; `PATCH` refuses `ask` for a device rule.

## Fleet keys

The home is its fleet's key manager for provider API keys. A person enters a
key once, on the home, in Swarm's own key page; never in a chat or a client.

- **How a key moves.** `POST /v3/fleet/keys/requests` names the provider, the
  linked machines and whether the home uses it too, and returns a one-time key
  page link (single use, ten minutes). On submit the home verifies the key with
  the provider, seals it in its secret store, then installs it: on itself if
  asked, and on each machine over its pinned link (a few at a time). Each
  machine verifies it with the provider again and saves it as an ordinary,
  active provider credential labelled "managed by home NAME, fleet key ID" and
  tagged with the key id and the home; it answers with the outcome only.
- **What the home keeps.** The value, sealed with the account key that seals
  provider credentials (vault rules apply), so it can install the key on a
  machine linked later (`POST /v3/fleet/keys/{id}/install`) and revoke it
  reliably. The record (`GET /v3/fleet/keys`, revisioned) holds the provider,
  label, a fingerprint (first eight hex characters of its SHA-256 and the last
  four characters) and, per machine and for the home, `pending`, `installed`,
  `failed` (with a short reason) or `removed`, with times. Never the value.
- **Revoke.** `POST /v3/fleet/keys/{id}/revoke` (all targets when none are
  named) has each machine delete only the credential that key installed. Once
  it is removed everywhere, the home deletes the value and the key cannot be
  installed again.
- **Who can do what.** Reads need `sessions:read`. Requesting, installing and
  revoking need an owner session or an AI key with `swarm:approve` (MCP:
  `swarm_list_fleet_keys`, `swarm_request_fleet_key`, `swarm_revoke_fleet_key`).
  Agents' gateway tokens never see or manage fleet keys, and no AI ever sees a
  value. A machine accepts `POST /v3/fleet/keys/install` and
  `/v3/fleet/keys/{id}/remove` only from its linked home over the link (not
  from its own owner, not from an AI key), and the home's raw proxy
  (`/v3/fleet/machines/{name}/api/...`) refuses every `/v3/fleet/keys` path, so
  only the home's key manager sends a key. The machine's owner still controls
  the saved credential like any other.

## Devices

A device is a client session that names its computer. `swarmctl client connect
NAME` records the device name (the computer's host name unless `--device` gives
another) on the client session, and every request made with that session
carries its client session id. Device rules are keyed by client session id and
shown by device name; revoking the session removes its rules.

Only a request made with a device's own client session may change that
device's rules. Rules for home and linked machines may be changed by the owner
from any device, and by an AI key with `swarm:approve`. Agents (gateway tokens)
can't change any rule.

A device tool call targets one device: the one whose client sent the message
the current run answers, unless the call names another connected device. If the
target device isn't connected, the call is refused with that reason.

## No bluffing

What clients show must be what Swarm enforces.

- One record. Swarm stores the rules once per account; the permission check,
  the agent's per-turn instructions and every client read that record. Clients
  keep no copy.
- A client shows a change after Swarm has stored it. Writes carry the record's
  revision; a stale revision is refused with 409 and the current record.
- Each rule comes with its live state, computed by Swarm: for a device, whether
  its client is connected now and whether the browser extension is attached;
  for a linked machine, whether it was reachable at its last contact and when;
  for `Start tasks`, how many tasks are waiting for approval. A client words
  `auto` or `ask` on an unreachable machine as unavailable, not as allowed.
- Each rule records who last changed it (device name, or AI key name), when,
  and when a call last used it.

## API

`GET /v3/permissions` returns:

```json
{
  "revision": 7,
  "home": {"name": "desktop", "tasks": "approve", "waiting_tasks": 2},
  "devices": [
    {"id": "csess_…", "name": "desktop", "this_device": true, "home": true,
     "connected": true, "browser_attached": true,
     "files": "allowed", "browser": "off",
     "changed": {"by": "desktop", "at": 1793000000000}, "last_used": 1793000100000}
  ],
  "linked": [
    {"name": "build-box", "work": "ask", "reachable": true, "last_contact": 1793000200000,
     "changed": {"by": "desktop", "at": 1793000000000}, "last_used": 1793000150000}
  ]
}
```

`this_device` is true for the caller's own client session. A device entry has
`was_ask` only when a rule stored as the retired `ask` now reads `off`. `PATCH
/v3/permissions` takes `expected_revision`, `client_request_id` and only the
rules to change (`tasks`: `approve` or `auto`; `devices.{id}.files|browser`:
`off` or `allowed`; `linked.{name}.work`: `off`, `ask` or `auto`, with `allowed`
accepted as `auto`); unknown fields and other values are refused, and a device
`ask` is refused with a message saying ask was removed. Changes publish a realtime frame so
clients refresh. MCP exposes the same read, and the home/linked writes for keys
with `swarm:approve`.

`/v3/sessions/{id}/access` is retired: GET answers with the account rules,
writes are refused with a pointer to `/v3/permissions`. Per-conversation
narrowing may be added later; it could only lower access below these rules.

## Clients

One Permissions page, grouped in the order control flows: this device, other
devices, home, linked machines. Each row shows what the rule protects, its
labelled choices (never a click that cycles), a "right now" line built from the
live state, and who changed it and when: two choices for a device rule (off,
allowed), three for a linked machine (off, ask, auto) and two for Start tasks
(approve, auto). Device rows of other devices are read-only with "change these
on that device".

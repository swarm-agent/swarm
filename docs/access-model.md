# Access model: devices, home, linked Swarms

Status: agreed design, not built yet. It replaces per-conversation access
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
| device | Files and terminal | client tools that read and write files and run commands on that device, as the person, outside Swarm's sandbox (`machine_read`, `machine_write`, `machine_run`) | `off`, `ask`, `allowed` |
| device | Browser | client tools that drive the device's browser (`manage_browser`) | `off`, `ask`, `allowed` |
| home | Start tasks | delegating to the home's own agents; media tasks included | `approve` (task waits for approval), `auto` |
| linked | Hand it work, per machine | `manage_projects` with `machine`: read its projects and tasks, create, approve, accept, reopen, refine, archive tasks | `off`, `ask`, `allowed` |

Fixed, not rules: a linked machine is never asked to integrate, deploy or
delete, and never runs commands directly for the home. Agents inside the home
work in its sandbox under the tool permission policy.

`ask` means each call waits for the person's decision as a normal permission
request; `allowed` means it proceeds. Swarm enforces both, for every rule,
including the device rules: a client runs a device tool only after Swarm has
approved that exact call.

Defaults: a new device starts at `off` for both device rules; `Start tasks`
starts at `approve`; a newly linked machine starts at `ask`.

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
  "allowed" on an unreachable machine as unavailable, not as allowed.
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
     "files": "ask", "browser": "off",
     "changed": {"by": "desktop", "at": 1793000000000}, "last_used": 1793000100000}
  ],
  "linked": [
    {"name": "build-box", "work": "allowed", "reachable": true, "last_contact": 1793000200000,
     "changed": {"by": "desktop", "at": 1793000000000}, "last_used": 1793000150000}
  ]
}
```

`this_device` is true for the caller's own client session. `PATCH
/v3/permissions` takes `expected_revision`, `client_request_id` and only the
rules to change (`tasks`, `devices.{id}.files|browser`, `linked.{name}.work`);
unknown fields and values are refused. Changes publish a realtime frame so
clients refresh. MCP exposes the same read, and the home/linked writes for keys
with `swarm:approve`.

`/v3/sessions/{id}/access` is retired: GET answers with the account rules,
writes are refused with a pointer to `/v3/permissions`. Per-conversation
narrowing may be added later; it could only lower access below these rules.

## Clients

One Permissions page, grouped in the order control flows: this device, other
devices, home, linked machines. Each row shows what the rule protects, three
labelled choices (never a click that cycles), a "right now" line built from the
live state, and who changed it and when. Device rows of other devices are
read-only with "change these on that device".

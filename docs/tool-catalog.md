# Tool catalog

The tool catalog is the only place a tool exists. A tool is declared once in
`swarmd/internal/tool` (`catalog.go`, plus one file per tool such as
`catalog_manage_projects.go`). The Orchestrator and Swarm Control (MCP) both
serve it from that declaration and run the same handler. Neither surface has
its own copy of the schema, rules or answer.

Stage 1 moves `manage_projects` into the catalog. The hand-written `swarm_*`
MCP tools are frozen as they are (`TestControlMCPSwarmToolNamesFrozen`).
Later stages move them into the catalog and remove them.

## What an action declares

Each action of a catalog tool declares:

- **tool** and **action**: the tool name and the action name.
- **inputs**: only the fields that action takes. A field's schema is declared
  once per tool and actions name the fields they use, so each action has its
  own input schema (`CatalogTool.InputSchema`). **control inputs** are fields
  only a Swarm Control call takes (`wait_tasks`' `wait_seconds`); they are
  not in the Orchestrator's definition.
- **level**: `read`, `write`, `approve` or `manage`. These are the same four
  levels as the `swarm:*` key scopes (`remote.ToolScope`).
- **API scopes**: the scopes of the route behind the action. A key needs one
  of them, because the call runs in-process and the route's own check never
  runs. Project and task actions take `projects:*` or `sessions:*` at the
  action's read or write; folder environments, fleet reads and
  `resolve_task_permission` take `sessions:*` only.
- **reach**: `any` runs here or, with `machine=NAME`, on a linked machine.
  `home` runs here only. `linked` reads a linked machine only, so `machine` is
  required.
- **local_only**: machine setup that a relay key may not do. No
  `manage_projects` action is local_only.
- **profiles**: the listings that show the action. `essentials` is Swarm
  Control's default listing, `full` is its `tools=all` listing and
  `orchestrator` is the Orchestrator's definition. An action with no profile
  (an alias, a task-card step the person takes in their app, a task's own
  `report_task`) is declared but not served to MCP.
- **description**: one description for both surfaces.
- **untrusted**: the answer's data holds agent- or outside-written text.

The Orchestrator's `manage_projects` definition is generated from these
declarations (`CatalogTool.OrchestratorDefinition`). It is the exact
definition the model was shown before, and a test holds it by hash.

## The envelope

Every answer comes back in one envelope, built by one function
(`tool.NewEnvelope`). This holds for the Orchestrator and MCP, on this machine
and on a linked machine:

| Field | Meaning |
| --- | --- |
| `tool`, `action` | What ran. |
| `machine` | Where it ran: this machine's own name (its Swarm name, or `home`), or the linked machine's name. Always set. |
| `state` | The action's outcome word: the answer's `status` (`ok`, `watching`, `stopped`, …), `ok` when it has none. |
| `data` | The action's own answer: every field it held before the envelope. A linked machine's answer is `data` as it came, plus this machine's notes (`linked_task_id`, `linked_record_error`). |
| `next_action` | One sentence when there is a next step. |
| `next` | The cursor or continuation when the answer was cut (`next_cursor`, or a cut message's `next`). An answer is never cut silently. |
| `untrusted_content` | `true` when `data` holds agent- or outside-written text. Every answer from a linked machine is untrusted. |

## How MCP serves the catalog

`tools/list` adds one MCP tool per catalog tool, generated from the catalog.
It has an `action` field and the inputs of the actions the caller may call.
The default listing shows `essentials` actions and `tools=all` adds `full`
ones. An action is listed and callable only when the key holds its level
(for a key with `swarm:*` levels) and one of its API scopes. Agent-bound and
application keys get no catalog tools.

A relay client reaches Swarm Control with the relay device's token, which has
API scopes but no levels. The relay forwards the client's own levels (its
scopes within the device's ceiling) with each call
(`remote.ClientLevelsHeader`) and does not gate catalog tools by name. Swarm
Control narrows the key's levels to the forwarded ones, which bind even when
empty, so each action is listed and run only at the client's level, and
local_only actions are refused. Forwarded levels never widen a key.

`tools/call` checks the key's level for the action before anything runs, then
runs the Orchestrator's handler in-process (`Runtime.ExecuteControlCatalog`)
with the caller's identity and account. It makes no HTTP round trip. The
linked-work rule for `machine=` is the same check the Orchestrator gets. The
answer is the envelope as it is.

An MCP caller has no conversation in Swarm. The differences that follow from
that are deliberate and tested:

- `wait_tasks` waits in the call instead of setting up a watch that wakes a
  later turn. It returns when a listed task reaches its next milestone
  (started, needs you, review started, sent back, landed, done, blocked,
  failed, stopped: `pebblestore.ProjectTaskMilestone`, the watch's own rules)
  or when `wait_seconds` passes (default 60, at most 110). `state` is
  `milestone` (the row's `reached` names it), `settled` (every task had
  already settled) or `timed_out`, and the tasks are as `get_task` reads them.
  It wakes on the sources a watch uses (committed realtime records here, the
  machine's activity feed for a linked machine) and records nothing.
  `cancel_watch` and `inspect_files` are Orchestrator-only.
- A task sent to a linked machine is recorded at home with the caller's AI
  key as its sender (`home_sender`, `key:ID`) instead of a session, so
  `list_linked_tasks` (without `project_id`: the caller's own sends),
  `get_task`, `get_task_session` and `read_task_session` follow it.

## Adding a tool or an action

Add the catalog entry and a parity case (`catalogParityCases` in
`swarmd/internal/api/control_mcp_catalog_test.go`). A catalog action without
one fails `TestCatalogParityCoversEveryAction`. The parity test calls each
action through the Orchestrator's runtime and through MCP `tools/call` with
the same inputs, here and on a linked machine, and compares the envelopes.
Nothing adds a `swarm_*` tool or reshapes results outside the envelope.

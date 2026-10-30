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
  own input schema (`CatalogTool.InputSchema`).
- **level**: `read`, `write`, `approve` or `manage`. These are the same four
  levels as the `swarm:*` key scopes (`remote.ToolScope`).
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
(for a key with `swarm:*` levels) and the API scopes behind it: `projects:read`
or `sessions:read` for read, `projects:write` or `sessions:write` otherwise.
Agent-bound and application keys get no catalog tools.

`tools/call` checks the key's level for the action before anything runs, then
runs the Orchestrator's handler in-process (`Runtime.ExecuteControlCatalog`)
with the caller's identity and account. It makes no HTTP round trip. The
linked-work rule for `machine=` is the same check the Orchestrator gets. The
answer is the envelope as it is.

An MCP caller has no conversation in Swarm. The differences that follow from
that are deliberate and tested:

- `wait_tasks` cannot set up a watch that wakes a later turn. It returns each
  listed task as `get_task` reads it, with `state` `not_watched`, and records
  nothing. `cancel_watch` and `inspect_files` are Orchestrator-only.
- A task sent to a linked machine is not recorded at home, because the record
  needs a sending session. The answer says so in `data.linked_record_error`.
  `read_task_session` and `get_task_session` follow only tasks this home
  recorded.

## Adding a tool or an action

Add the catalog entry and a parity case (`catalogParityCases` in
`swarmd/internal/api/control_mcp_catalog_test.go`). A catalog action without
one fails `TestCatalogParityCoversEveryAction`. The parity test calls each
action through the Orchestrator's runtime and through MCP `tools/call` with
the same inputs, here and on a linked machine, and compares the envelopes.
Nothing adds a `swarm_*` tool or reshapes results outside the envelope.

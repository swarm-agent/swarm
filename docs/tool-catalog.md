# Tool catalog

The tool catalog is the only place a tool exists. A tool is declared once in
`swarmd/internal/tool` (`catalog.go`, plus one file per tool such as
`catalog_manage_projects.go`). The Orchestrator and Swarm Control (MCP) both
serve it from that declaration and run the same handler. Neither surface has
its own copy of the schema, rules or answer.

The catalog holds every tool below:

| Tool | Stage | Declared in |
| --- | --- | --- |
| `manage_projects` | 1 (6 adds `get_task_changes`, `read_task_file`) | `catalog_manage_projects.go` |
| `manage_workflows` | 2 | `catalog_manage_workflows.go` |
| `manage_jobs`, `manage_workers` | 3 | `catalog_manage_jobs.go`, `catalog_manage_workers.go` |
| `manage_sessions`, `manage_workspace` | 4 | `catalog_manage_sessions.go`, `catalog_manage_workspace.go` |
| `manage_setup` | 5 | `catalog_manage_setup.go` |
| `manage_fleet` | 6 | `catalog_manage_fleet.go` |
| `manage_media` | 6 | `catalog_manage_media.go` |
| `manage_custom_agents`, `manage_themes`, `manage_designs` | 6 | `catalog_custom_agents_theme_design.go` |

Stage 6 is the last stage that moves tools. The hand-written `swarm_*` MCP
tools are frozen as they are (`TestControlMCPSwarmToolNamesFrozen`) and still
work beside the catalog tools; the next stage deletes them. Every `swarm_*`
tool has a catalog home (a tool and action with the same effect) or is
retired with its reason (`swarmToolCatalogHomes` and `swarmToolsRetired`,
`TestEverySwarmToolHasACatalogHome`). Retired: `swarm_set_session_access`,
`swarm_run_plan` (plans are not the default) and `swarm_accept_review` (it
closes a plan checkpoint's review).

### A catalog action accepts every input of the tool it replaced

A catalog action takes every input of the `swarm_*` tool it replaces, under
the same name and with the same meaning, run by the code that tool used
(stage 7a). It also returns every field that tool's answer carried, in the
envelope's `data` (or in the record `data` holds, such as `data.task`); a
`swarm_*` answer's `next` hint is the envelope's `next_action` or `next`.
Inputs only a Swarm Control caller can use are control inputs, run by the
`swarm_*` tool's own handler; a conversation never passes them, here or on a
linked machine, and the Orchestrator keeps its own watch and turn model:

- held waits (`wait_seconds`) on `manage_jobs`, `manage_projects` `get`,
  `propose_task`, `wait_tasks` and the folder environments;
- `manage_jobs` `then` and `model`, and `propose_task`'s `model` (checked as
  `swarm_run_task` checks it);
- the immediate worker changes on `manage_workers`: `create` with
  `swarm_create_worker`'s folder and schedule or `swarm_deploy_worker`'s
  preset, role or worker, and `update` with `agent`, `run_id` (cancel that
  run) or `schedule_id` with `schedule_enabled`. Through Swarm Control a
  worker is active at once; the Orchestrator proposes it for acceptance.
  Every input a call gives is applied, or the call is refused naming the
  inputs that would not apply, and nothing changes. `update` with `agent`
  applies it with every other field in one write at the caller's
  `expected_revision`; `run_id` cancels a run and goes with nothing else;
  `schedule_id` honours `expected_revision`. A folder `create` also takes
  `agent`, `model`, `budget`, `automations` and `idempotency_key` (not
  `requested_capabilities`, which need the person's approval, nor
  `workspace_requirements`, `local_bindings` or `document`). A `worker`
  create saves that worker: `name` (a rename), `description`,
  `instructions`, `agent`, `model` and `budget`, in one write at the
  revision it read; a different `role` is refused. A `preset` create takes
  `description`, `model` and `budget`; a `role` create takes `name`,
  `instructions`, `description`, `agent`, `model` and `budget`. The branch
  table (`workerControlRoutes`) is what both the handler and the refusal
  read; `TestEveryWorkerControlBranchWritesWhatItLists` runs every branch
  and fails on an input listed as written that does not land.
  `update_environment`'s `reset` and `rebuild` go with no other change.

A folder environment read in the `swarm_*` shape (a `project` or
`workspace`, no `workspace_path`, or `include_log`) runs
`swarm_get_environment`'s handler; a folder named by `workspace_path` runs the
folder environment service. `update_environment` always merges, whichever
way the folder is named: only the fields given change, through one merge
(`UpdateSandboxEnvironment`). `replace: true` sets the whole definition
instead: it needs `base_image`, clears `setup` and `env` not given, and does
not go with `append_setup`, `unset_env`, `rebuild` or `reset`. `get_task` and
`inspect` stay the answers the Orchestrator and Swarm Control share, so
`swarm_task_result`'s and `swarm_get_worker`'s flat fields are read from
their record there (`data.task`, `data.worker`).

Two tests hold this for every mapped pair (`swarmToolCatalogHomes`):
`TestEverySwarmToolInputIsACatalogInput` compares each `swarm_*` input schema
with the action's inputs and control inputs, and
`TestEverySwarmAnswerFieldIsInTheCatalogEnvelope` makes a representative call
of both and compares the answer's top-level fields with the envelope data.
A pair whose call both surfaces refuse fails unless `swarmAnswerUnprobed`
names why (the hermetic harness runs no provider, worktree service,
workspace authority, sandbox environments, secret slots or media models).
Those answers are not compared by execution:
`TestEveryUnprobedSwarmAnswerIsCheckedStatically` checks that the `swarm_*`
tool runs the named handler and that the catalog code serving the action
calls that same handler. `swarm_run_task` and `swarm_integrate_task` are
served by the Orchestrator's own `propose_task` and `integrate_task`; the
test reads the `swarm_*` answer's field names from its handler's source and
requires each in the catalog answer under the same name
(`tool.ProposeTaskSwarmAnswerFields`, and the maps `integrateProjectTask`
returns). `swarmAnswerUnprobed` only says why a pair has no probe; a field
the catalog does not return is a `swarmCatalogDrops` entry.
The filter is `tool.CatalogConversationArgs`: a conversation's call drops an
action's control inputs before it runs here or goes to a linked machine,
which runs a linked call as a Swarm Control one.
A deliberate drop is listed once, with its reason, in `swarmCatalogDrops`:
an input named `action` (the catalog's own field: `connect_chatgpt` and
`connect_claude_code` take it as `step`; `swarm_manage_worker`'s values are
`manage_workers` actions or `update` inputs; approving a task card is the
person's step in their app), `create_key`'s `kind` (`key_kind`),
`swarm_design`'s `edit_path` (`manage_designs` `edit`), `swarm_request_media`'s
`retry_of` (`manage_media` `retry`), `swarm_get_job`'s `include_media` (images
are not envelope data; `manage_media` `read` returns a file),
`swarm_run_task`'s `role` (`agent`), and the `swarm_task_result` and
`swarm_get_worker` answer fields that `get_task` and `inspect` carry under
their own names.

A key bound to one worker, one agent or a trigger never calls a catalog tool:
Swarm Control neither lists nor runs them for it, and the linked routes
(`POST /v3/jobs/{action}`, `POST /v3/worker-actions/{action}`,
`POST /v3/setup-actions/{action}`) answer 403
(`catalogBoundKey`). The catalog services run each action on internal requests
that carry only scopes, so the routes behind them could not hold such a key to
its binding; the key keeps its own `swarm_*` tools.

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
  `manage_projects`, `manage_workflows`, `manage_sessions` or
  `manage_workspace` action is local_only.
- **profiles**: the listings that show the action. `essentials` is Swarm
  Control's default listing, `full` is its `tools=all` listing and
  `orchestrator` is the Orchestrator's definition (for `manage_jobs`, also
  every agent whose tool contract has it). An action with no profile
  (an alias, a task-card step the person takes in their app, a task's own
  `report_task`) is declared but not served to MCP.
- **description**: one description for both surfaces.
- **untrusted**: the answer's data holds agent- or outside-written text.
- **waiting statuses**: the statuses of the answer's own record that wait on
  someone; such an answer's state is `waiting` (`manage_jobs`).

The Orchestrator's `manage_projects`, `manage_workflows`, `manage_jobs`,
`manage_workers`, `manage_sessions` and `manage_workspace` definitions are
generated from these declarations (`CatalogTool.OrchestratorDefinition`).
`manage_projects`' is held by hash: the exact definition the model was shown
before. `manage_workflows`', `manage_jobs`' and `manage_workers`' are held by
hash too and keep every field and the description they had, adding the new
actions' inputs (`manage_jobs`' and `manage_workers`' schemas now accept other
properties, as every catalog tool's does). `manage_sessions`' and
`manage_workspace`'s replace the hand-written trims with their orchestrator
profiles (`TestManageSessionsOrchestratorDefinitionIsTheCatalogs`,
`TestManageWorkspaceOrchestratorDefinitionIsTheCatalogs`).
`manage_workflows`' description and fields changed since, on purpose and with
its hash: workflows named by id or name, propose's `description`,
`expected_revision` and `replace` (and no `revision`), and the `refused` state.

## manage_workflows

One handler (`tool.Runtime.executeManageWorkflows`) serves the Orchestrator
and Swarm Control. Every action here runs one service function,
`api.Server.ManageWorkflows`, which calls the same functions as the
`swarm_*` workflow tools' routes (`ListWorkflows`, `WorkflowHistory`,
`SaveWorkflow` as a proposal, `DeleteWorkflow`, `ExportWorkflow`,
`ImportWorkflow`, `RestoreWorkflow`, `StopWorkflows`). Its caller is the
Orchestrator's session or the Swarm Control caller's AI key, never the
machine owner: `import` and `restore` refuse `run` steps, and a `propose`
with one waits for the owner to accept it. For a Swarm Control caller the
service holds only the API scopes the key holds (`tool.WithControlScopes`),
so a route's own scope checks see the key, never a wider set.

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `list`, `get`, `deliveries` | read | `automations:read` or `automations:write` | any |
| `history` | write | `automations:read` or `automations:write` | any |
| `export` | write | `automations:read` or `automations:write` (writing `path` also needs the key to hold `automations:write`, checked before a linked call too) | any |
| `propose`, `enable`, `disable`, `delete`, `import`, `restore` | write | `automations:write` | any |
| `stop_all` | manage | `automations:write` | any |
| `help` | read | `automations:read` or `automations:write` | home |

Each level is `remote.ToolScope` of the `swarm_*` tool with the same effect
(`TestManageWorkflowsLevelsAreTheSwarmToolsLevels`); `swarm_workflow_history`
and `swarm_restore_workflow` have no entry there, so they need write.
Every action is in the Orchestrator's definition and Swarm Control's
default listing.

With `machine`, an action runs on the linked machine through
`api.Server.CallFleetWork`: its workflow routes for reads, `propose`,
`enable`, `disable`, `delete`, `export` and `stop_all`, and
`POST /v3/workflows/agent/{import|restore}` for `import` and `restore`,
which runs that machine's `ManageWorkflows`, never its owner's import or
restore route. The linked work rule (`linked.{name}.work`) applies as for
every linked call.

Naming, changes and answers, the same here and on a linked machine:

- **A workflow by id or name.** `get`, `enable`, `disable`, `delete`,
  `export`, `history`, `restore` and `propose` take a workflow's id or its
  name in `id` or `name`, by one rule (`resolveWorkflowReference`): an exact
  id, else the exact name, else the name in any case; among workflows of
  that name the accepted one wins over pending proposals. Several matches
  are refused, naming each (`name (id, state)`). On a linked machine the
  home resolves against that machine's own list, then calls its route with
  the id. `history` and `restore` also find a deleted workflow by name.
- **Description.** A workflow has an optional `description` (at most 2,000
  characters), shown by `list`, `get` and `export`, kept by `import`,
  `restore` and history (a change to it is a changed field).
- **propose.** A new workflow needs its full definition. Naming an existing
  workflow (`id`, or `name` when a workflow has that exact name) makes the
  call a change merged onto its current revision: only the fields given
  change, `scope` key by key, each step given by its `id` into that step
  (`null` removes a field), and with `id`, a different `name` renames it
  (the workflow keeps its id). `expected_revision`, when given, must be the
  current revision, else the change is refused; acceptance still checks the
  base revision. Adding, removing or reordering steps is `replace: true`,
  which sends the whole definition (keeping the name and folder it does
  not set). A field propose does not take is refused, naming it, before
  anything is saved and before a linked call. The answer has `changes` (the
  changed fields), `merged_onto_revision` or `replaced`, and a `next_action`:
  the person accepts or discards the proposal in their Swarm app; an AI
  cannot.
- **history** lists `pending_proposals` beside the revisions: each
  proposal's id, `state` `pending`, who proposed it, the revision it
  replaces and the fields it changes.
- **export** carries the scope as the workflow's project (`project`, and
  `workspace` in a project with several folders), never a folder path on
  its own. `import` binds the call's `project`/`workspace` when given;
  else the definition's project on this machine; a project the machine
  does not have is refused, asking for `project`, never bound wider.
- **Run steps.** `import` and `restore` of a run step stay the machine
  owner's: the owner's routes answer 403 to a key without `admin`, and the
  catalog answers the envelope with state `refused`, `data.reason` and a
  `next_action` (import without the run step, or ask the machine's owner).
  Nothing is saved.
- **Answers** name catalog tools and actions only, refusals included: the
  project and folder refusals shared with the `swarm_*` tools are reworded
  on this path (`workflowCatalogError`) to `manage_projects` `create`,
  `list` or `update`, here and from a linked machine. A saved workflow that
  is disabled is said not to run until it is enabled.

## manage_jobs

One handler (`tool.Runtime.executeManageJobs`) serves the Orchestrator, every
agent whose tool contract has `manage_jobs`, and Swarm Control. Every action
runs one service function, `api.Server.ManageJobs`, which runs the same
handlers as the `swarm_*` job tools (`swarm_list_runs`, `swarm_get_job`,
`swarm_give_job`, `swarm_message_job`, `swarm_answer_job`,
`swarm_deliver_job`, `swarm_stop_job`, `swarm_schedule_job`). A caller in a
conversation needs the owner's grant for each change
(`manage_jobs.<agent>.<action>`, [agent tools](agent-tools.md)) and cannot act
on its own job or worker; a Swarm Control caller is held to its key's level
and the API scopes it holds (`tool.WithControlScopes`).

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `list_runs` | read | `signals:read` | any |
| `get_job` | read | `sessions:read` | any |
| `deliveries` | read | `automations:read` or `automations:write` | any |
| `give_job`, `message_job`, `answer_job`, `deliver_job`, `stop_job`, `schedule_job`, `rerun_failed_steps` | write | `sessions:write` (the routes behind them check their own scopes too) | any |

Each level is `remote.ToolScope` of the `swarm_*` tool with the same effect
(`TestManageJobsLevelsAreTheSwarmToolsLevels`; `deliveries` takes
`swarm_list_workflows`', `rerun_failed_steps` `swarm_deliver_job`'s). Every
action is in the Orchestrator's definition and Swarm Control's default
listing.

With `machine`, an action runs on the linked machine through
`api.Server.CallFleetWork`, which calls that machine's
`POST /v3/jobs/{action}`: the route runs the machine's own `ManageJobs` with
the caller's scopes (a scoped key's, or the job scopes for the link), never
another route. The linked work rule applies: `off` refuses every action;
under `ask`, a conversation's `give_job`, `message_job`, `answer_job`,
`rerun_failed_steps` and `schedule_job` there are refused with the reason,
since `manage_jobs` has no approval prompt for them yet (a Swarm Control
caller's person approves in their own AI's chat).

A job whose status is `needs_input` (a question for `answer_job`),
`waiting_on_approval` or `pending_approval` answers with state `waiting`. A
read too large to answer inline is a JSON text page whose `next` is the same
read with `page_offset` and `page_digest`; a cut run list's `next` is
`list_runs` at its `cursor`.

## manage_workers

One handler (`tool.Runtime.executeManageWorkers`) serves the Orchestrator and
Swarm Control; the Orchestrator's definition and its `manage_automation` alias
are generated from the declaration. Every action runs one service function,
`api.Server.ManageWorkers`: `assign_task` runs the `swarm_assign_worker_task`
handler (`POST /v3/workers/{id}/direct`); every other action runs
`run.Service.ManageWorkers`, the Orchestrator's worker code. A conversation
caller must be the Orchestrator; a Swarm Control caller is held to its key's
level and scopes and has no conversation: `create` and `propose` make a worker
that waits for the person's acceptance in Swarm (as the Orchestrator's do), and
`review`, `context` and `progress` need `worker_id`. `activate` is refused for
every caller: activation is the person's acceptance.

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `help`, `list`, `inspect`, `review`, `context`, `progress` | read | `automations:read` or `automations:write` | any |
| `propose`, `test`, `request`, `assign_task` | write | `automations:write` | any |
| `create`, `update`, `attach`, `disable_automation`, `delete`, `activate`, `pause`, `resume`, `archive` | manage | `automations:write` | any |

Each level is `remote.ToolScope` of the `swarm_*` worker tool with the same
effect (`TestManageWorkersLevelsAreTheSwarmToolsLevels`). With `machine`, the
action runs on the linked machine's own `ManageWorkers` through
`POST /v3/worker-actions/{action}` with the caller's scopes; a linked `list`
stays the bounded summary (no instructions; `inspect` reads them). `off`
refuses every linked action; under `ask` the Orchestrator's `test`, `request`
and `assign_task` there wait for the person's approval (`FleetTaskStart`).

## manage_setup

This machine's setup: the actions of the `swarm_*` setup tools, which only
Swarm Control had before. One handler (`tool.Runtime.executeManageSetup`)
serves the Orchestrator and Swarm Control; one service function,
`api.Server.ManageSetup`, runs the `swarm_*` handler with the same effect for
each action. It runs that handler on an internal request that carries the
Swarm Control caller's own key, narrowed to the scopes it holds, or, for a
conversation, only the action's route scopes and level. Only the Orchestrator
calls it in a conversation; a task's session, another agent or a worker is
refused.

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `models`, `providers`, `usage`, `plan_usage`, `media_models` | read | `sessions:read` | any |
| `limits` | read | `usage:read` or `usage:write` | any |
| `permissions`, `list_keys`, `list_fleet_keys` | read | `sessions:read` | home |
| `list_secrets` | read | `secrets:read` | home |
| `set_agent_model`, `set_media_models` | write | `settings:write` | any |
| `set_limits` | write | `usage:write` | any |
| `set_usage_limits` | manage | `usage:write` | any |
| `set_permissions` | approve | `swarm:approve` | home |
| `revoke_key` | write | `sessions:write` | home |
| `request_provider_key` | write | `sessions:write` | home, local only |
| `connect_chatgpt`, `connect_claude_code` | write | `admin` or `swarm:write` | home, local only |
| `request_fleet_key` | approve | `swarm:approve` | home, local only |
| `revoke_fleet_key` | approve | `swarm:approve` | home |
| `create_key`, `create_client_key` | manage | `admin` | home, local only |
| `request_update`, `cancel_update` | manage | `admin` | any |

Each level is `remote.ToolScope` of the `swarm_*` tool with the same effect,
and an action is local only exactly when that tool is in remote's
`localOnlyTools` (`TestManageSetupLevelsAreTheSwarmToolsLevels`). Reads and
the change actions whose `swarm_*` tool was in the default listing are in
Swarm Control's default listing; `set_usage_limits`, `set_permissions`,
`create_client_key`, `request_update` and `cancel_update` are in `tools=all`.
Keys, secrets, permissions and provider sign-in never cross a link; the other
actions run with `machine` on the linked machine's own `ManageSetup` through
`POST /v3/setup-actions/{action}`, which refuses the home-only ones.

Safety is the same on both surfaces:

- **The person decides every change.** In a conversation, the run's gate
  (`run.Service.gateToolCalls`) turns every `manage_setup` change into a
  pending request, even with permission bypass, an allow rule or an earlier
  "always allow"; only an explicit deny rule answers without asking. An
  approval runs the exact call the person saw, never edited arguments, and the
  handler refuses a change without that decision
  (`tool.PersonDecisionFromContext`). Over Swarm Control the person approves in
  their own AI's chat and the key's level is checked. Model and media-model
  changes are made only when the person asks.
- **No caller widens its own access.** `set_permissions` only sets Start tasks
  to `approve` and lowers a linked machine's work (`auto` > `ask` > `off`);
  `set_limits` only lowers box limits and never removes a job budget;
  `set_usage_limits` only lowers a daily limit or turns the limits on.
  `create_key` and `create_client_key` make no key: a new key's secret may go
  only to the person, and there is not yet a one-time path that hands it to
  them alone. The person makes keys, and raises limits and permissions, in
  Swarm.
- **No secret in an answer.** Answers hold names, ids, scopes and expiry;
  `setupScrub` also drops any secret-shaped string field (`key`, `token`,
  `api_key`, `secret`, `password`, ...). Provider and fleet keys are pasted by
  the person on Swarm's one-time page (`request_provider_key`,
  `request_fleet_key`), never in the chat.

Tests: `TestManageSetupFromBothSurfaces`,
`TestManageSetupNeverWidensTheCallersAccess`,
`TestManageSetupListingLevelsAndLinkedRoute`,
`TestManageSetupAnswersHoldNoSecret` and the parity, coverage and envelope
cases (api); `TestManageSetupChangeAlwaysWaitsForThePerson` and
`TestManageSetupRunRefusesUndecidedChanges` (run);
`TestManageSetupCatalogDeclaresEverySetupAction` and
`TestManageSetupHandlerHoldsChangesToThePersonsDecision` (tool).

## manage_fleet

Fleet monitoring, moved out of `manage_projects`. One handler
(`tool.Runtime.executeManageFleet`) serves the Orchestrator and Swarm Control;
each action runs the one implementation it had. `status` and `activity` work
for this machine too (omit `machine`), which `manage_projects`'
`machine_status` and `activity` never did.

| Action | Runs | Level | API scopes | Reach |
| --- | --- | --- | --- | --- |
| `list_machines` | manage_projects `list_machines` | read | `sessions:read` | home |
| `status` | here `swarm_status`'s handler; with `machine` the linked status read | read | `signals:read` or `sessions:read` | any |
| `agents_at_work` | manage_projects `agents_at_work` (`swarm_agents_at_work`) | read | `sessions:read` | any |
| `activity` | here `swarm_activity`'s handler; with `machine` the linked activity read | read | `sessions:read` or `signals:read` | any |
| `list_runs` | manage_jobs `list_runs` (`swarm_list_runs`) | read | `signals:read` | any |
| `list_linked_tasks` | manage_projects `list_linked_tasks` | read | `sessions:read` | home |

Every action is in the Orchestrator's definition and Swarm Control's default
listing. The Orchestrator has `manage_fleet` code-owned
(`run.resolveAgentToolStates`). `manage_projects` keeps `list_machines`,
`list_linked_tasks`, `agents_at_work`, `machine_status` and `activity` as
aliases for one stage; their descriptions say they are deprecated. In a
conversation, `status` and `activity` here run only for the home's own
conversations, never a task's session.

## manage_media

The Orchestrator's `manage_media` actions plus `read`, which only Swarm
Control had (`swarm_read_media`). Media models are set with `manage_setup`
`set_media_models`. One handler (`tool.Runtime.executeManageMediaCatalog`)
serves the Orchestrator, every agent with `manage_media` and Swarm Control.

A conversation's media are its session's project's (the media service,
`api.Server.ManageMedia`); a Swarm Control caller has no conversation and
names the `project`, and its actions run the `swarm_*` media handlers. This
is the one documented difference between the two surfaces
(`paritySessionBound`): both answer the same way, an envelope of the same
tool, action and machine, or a refusal. `read` and every call with `machine`
run the `swarm_*` handlers for both.

| Action | `swarm_*` tool | Level | API scopes | Reach |
| --- | --- | --- | --- | --- |
| `models` | `swarm_media_models` | read | `sessions:read` | any |
| `generate`, `retry` | `swarm_request_media` (`retry_of`) | write | `sessions:write` | any |
| `get` | `swarm_get_job` | read | `sessions:read` | any |
| `save` | `swarm_save_media` | write | `sessions:write` | any |
| `read` | `swarm_read_media` | read | `sessions:read` | any |
| `list`, `transcribe` | none (this conversation's own jobs and files) | read, write | `sessions:read`, `sessions:write` | home, a conversation only (not served to Swarm Control) |

## manage_custom_agents, manage_themes and manage_designs

- `manage_custom_agents` (`list`, `get`, `define`, `disable`): the account's
  custom agent service (`api.Server.ManageCustomAgents`) for both surfaces;
  `swarm_list_agents` and `swarm_define_agent` map to `list` and `define`.
  Read actions are read level with `agents:read` or `swarm:read`; `define`
  and `disable` are write with `agents:write` or `swarm:write`. Home only, in
  the Orchestrator's definition and `tools=all`.
- `manage_themes` (`get`): `swarm_get_theme`'s handler, read, home only, in
  the Orchestrator's definition (code-owned in its contract) and `tools=all`.
  It is not named `manage_theme`: the run treats `manage_theme` and
  `manage-theme` as one tool, the agents' theme tool, whose every action,
  `get` included (its `theme` and `id` aliases and its answer), is unchanged
  (`TestAgentManageThemeGetIsTheThemeTool`).
- `manage_designs` (`create`, `edit` with `edit_path`): `swarm_design`'s
  handler, write with `sessions:write`, any reach, in Swarm Control's default
  listing and the Orchestrator's definition (code-owned in its contract). Not
  named `manage_design`, the retired tool `swarm_design` replaced. The
  Designer keeps its own `swarm_design` tool, which the run service executes
  inside its run.

## Task work in manage_projects

`get_task_changes` (a task's commits, changed and new files and diff, paged as
JSON text with `next`: `page_offset` and `page_digest`) and `read_task_file`
(one file or folder of the task's working folder) run `swarm_get_changes` and
`swarm_read_file` for the task's session. Both are read with `sessions:read`,
any reach, in every listing.

## The swarm_* handler service

The actions whose one implementation is a `swarm_*` tool's handler run
through one service function, `api.Server.ManageCatalogAction`: `manage_fleet`
`status` and `activity` here, `manage_media`'s Swarm Control and linked
actions, `manage_themes`, `manage_designs`, and `get_task_changes` and
`read_task_file`. It runs the handler on an internal request that carries the
Swarm Control caller's own key narrowed to the scopes it holds, a link's
scopes, or a conversation's route scopes and level, never wider. In a
conversation only the home Orchestrator runs it: a task's session, a worker's
job session (`worker_id`) and every other agent are refused, at the tool and
again in the service (`TestStage6RefusesWorkersAndOtherAgents`); the
session-bound `manage_media` actions of other agents keep running on their own
session's project. The run passes the calling session's agent to every tool on
its generic tool path, so the Orchestrator's catalog calls reach the service
(`TestRunToolPathPassesTheSessionAgentToCatalogTools`). With `machine` it runs on the linked machine's own
service through `POST /v3/catalog-actions/{tool}/{action}`, which serves only
the actions that reach a linked machine. The linked work rule applies: `off` refuses
every linked action; under `ask` a conversation's change there (a media job
or a design) is refused with the reason, since these tools have no approval
prompt for it yet, while reads run and a Swarm Control caller's person
approves in their own chat (`TestStage6LinkedChangesUnderAskAndOff`).

## manage_sessions

One handler (`tool.Runtime.executeManageSessions`) serves the Orchestrator,
the Swarm agent and Swarm Control, and answers in the envelope: a session's
own status (`queued`, `cancelled`, `not_running`) is in `data`, the call's
outcome is `state`. `resolve_permission` and `set_model` run the functions
behind `swarm_resolve_permission` and `swarm_set_session_model`, and through
them the session's permission and model-profile routes
(`api.Server.ManageSessionControl`), holding only the session scopes the
caller's key holds. A conversation never answers its own pending requests.
`create` binds to a registered workspace (a Swarm Control caller has no
conversation roots, so it must name one). A cut `read_messages` page or
message names `next`. Following it reads exactly the rest of the page the
call asked for: the role filter carries over, a `before`, `around` or tail
page continues inside its window (never as an open `after` read), and a cut
message's last chunk names the rest of its page, so no later message on that
page is skipped. `list`'s opaque cursor carries over as `next`.

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `inspect`, `list`, `list_by_state`, `search`, `get`, `read_messages` | read | `sessions:read` or `sessions:write` | any |
| `create`, `send_message`, `stop`, `pause`, `set_model` | write | `sessions:write` | any |
| `archive`, `unarchive`, `compact` | write | `sessions:write` | any |
| `resolve_permission` | approve | `sessions:write` | any |

Each level is `remote.ToolScope` of the `swarm_*` tool with the same effect
(`TestManageSessionsLevelsAreTheSwarmToolsLevels`); `archive`, `unarchive`
and `compact` have no `swarm_*` tool and change sessions, so they need
write. `set_model` is called only when the person asks. In a conversation,
`resolve_permission` and `set_model` follow the account's permission bypass
switch. With it off, they always ask the person first: the run gives them
their own approval identities (`session_permission_answer`,
`session_model_change`), which have no default allow, and a bypass suffix in
the session's mode text does not skip the prompt. Only an explicit rule for
that identity pre-approves them. With the switch on, they are approved like
every other tool call (`run.manageSessionsPersonDecisionIdentity`,
`TestManageSessionsPersonDecisionsWaitForThePerson`). Through MCP the key's
level is the check (approve for `resolve_permission`), as it was for
`swarm_resolve_permission`. Every action above
is in the Orchestrator's definition (generated from the catalog's
orchestrator profile) and in Swarm Control's default listing. The Swarm
agent's own session tooling (`review_worktrees`, `git_status`, `commit` and
the approved launch of proposals) is declared with no profile: that agent
keeps its full definition and Swarm Control does not serve it.

With `machine`, an action runs on the linked machine through that
machine's own `manage_sessions` handler, at `POST /v3/tool-catalog/{tool}`
(`api.CatalogLinkedPath`), which only a home over the link may call. The
linked work rule applies: off refuses every call, ask refuses the calls
that start a run there (`create` with a prompt, a triggering
`send_message`), auto runs everything. In a conversation, an `archive` or
`unarchive` with `machine` asks the person with the linked machine's own
sessions (read through its `manage_sessions`), binds their versions, and the
approved call keeps `machine` and runs on that machine; this machine's
sessions are never touched (`TestManageSessionsLinkedArchiveThroughTheGate`).
The Swarm agent's own tooling (`deploy`, `commit`) refuses `machine`.

## manage_workspace

One entry (`tool.Runtime.ManageWorkspaceCatalog`) answers every call in the
envelope. A conversation's call (the Orchestrator's or the Swarm agent's)
runs `run.Service.ExecuteManageWorkspace`: catalog and Workspace Map
changes run only with the person's approved arguments, and a change to the
session's own workspace switches it and restarts the turn, as before. A
Swarm Control call has no conversation and runs
`run.Service.ManageWorkspaceControl` (the run service registers it on the
tool runtime): the same catalog and Workspace Map functions with no session
to retarget. Swarm sets the action's permission scope itself and refuses one
from the caller; `create` applies `swarm_add_workspace`'s checks (an
existing, non-empty folder outside system and Swarm storage, not inside a
registered workspace). `update` applies the same path checks when it moves a
workspace, for every caller (`workspace.Service.UpdateCatalogEntryForPrincipal`).

| Action | Level | API scopes | Reach |
| --- | --- | --- | --- |
| `inspect`, `list`, `inspect_map`, `get_map`, `list_source_media_directories` | read | `sessions:read` or `sessions:write` | any |
| `create`, `update`, `update_map`, `set_default`, `add_source_media_directory`, `remove_source_media_directory` | write | `sessions:write` | any |
| `delete` | manage | `sessions:write` | any |

The reads take `swarm_list_workspaces`' level and `create` takes
`swarm_add_workspace`'s (`TestManageWorkspaceLevelsAreTheSwarmToolsLevels`).
The other changes have no `swarm_*` tool. They need write, except `delete`,
which removes a workspace from the catalog and needs manage, like
`manage_projects` `delete`. Every action above is in the Orchestrator's
definition (its orchestrator profile, replacing the hand-written trim) and
in Swarm Control's default listing. The Swarm agent's session and worktree
actions (`set_session`, `adopt_worktree`, `discover_worktrees`,
`reclaim_worktree`, `copy_worktree`, `cancel_worktree_recovery`) need a
conversation: they have no profile, that agent keeps its full definition,
and Swarm Control does not serve them.

With `machine`, an action runs on the linked machine's own handler through
`POST /v3/tool-catalog/manage_workspace`, as for `manage_sessions`; the
linked work rule's off refuses. In a conversation a linked catalog change
still asks the person first (the call as written).

## The envelope

Every answer comes back in one envelope, built by one function
(`tool.NewEnvelope`). This holds for the Orchestrator and MCP, on this machine
and on a linked machine:

| Field | Meaning |
| --- | --- |
| `tool`, `action` | What ran. |
| `machine` | Where it ran: this machine's own name (its Swarm name, or `home`), or the linked machine's name. Always set. |
| `state` | The call's outcome, always one of the states below. A task's, run's or machine's own status (`needs_review`, `running`, `degraded`) is in `data`, never here. |
| `data` | The action's own answer: every field it held before the envelope. A linked machine's answer is `data` as it came, plus this machine's notes (`linked_task_id`, `linked_record_error`). |
| `next_action` | The next step, for every action that has one: the action's own (a task's `next_action`, such as `wait_tasks`), else "Call next to read the rest." for a cut answer, else the step its declaration names (`CatalogAction.NextAction`). Omitted when there is nothing to do; never `none`. |
| `next` | Set whenever the answer was cut: the exact arguments of the call of the same tool that reads the rest, as one object (`action` plus its cursor or offset fields and the call's own `project_id`, `task_id`, `machine` and filters). A cut list gives the same action at the next `cursor`; a cut message gives `read_task_session` at its `message_id`, `message_seq` and `content_offset`; a cut task report (`result_truncated` in `get_task` or Swarm Control's `wait_tasks`) gives `get_task` with `detail` `full` at the report's attempt (`cursor`, `limit` 1), or `read_task_session` for a task on a linked machine; a cut JSON text page (`next_page_offset`) gives the same read at `page_offset` with its `page_digest`; an opaque string cursor (`manage_jobs` `list_runs`) gives the same read at that `cursor`. `next` only names a read action the catalog declares, with inputs that action takes. |
| `untrusted_content` | `true` when `data` holds agent- or outside-written text. Every answer from a linked machine is untrusted. |

`state` is one of (`tool.EnvelopeStates`):

| State | Meaning |
| --- | --- |
| `ok` | The call did what it was asked. |
| `watching` | The Orchestrator's `wait_tasks` recorded a watch; a new turn brings the outcomes. Its `data.notice` says what to tell the person, and it has no `next_action`. |
| `cancelled` | `cancel_watch` (or `wait_tasks` with `cancel`) ended watches. |
| `milestone`, `settled`, `timed_out` | Swarm Control's `wait_tasks` (below). |
| `partial` | The call ran but part of its answer is missing: the home could not keep its record of a send (`linked_record_error`) or could not read the machine's answer (`data_error`). |
| `waiting` | The record the call read or changed waits on someone: a job waiting on its question or an approval (`CatalogAction.WaitingStatuses`). Its own status stays in `data`. |
| `refused` | The action declined what was asked and changed nothing: `data.reason` says why and `next_action` what to do instead. Only an action that declares it answers it (`manage_workflows` `import` and `restore` of a run step). |

An action's answer `status` becomes `state` only when the action declares it (`CatalogAction.States`). Any other refused or failed call is not an envelope: it is a tool error that carries the reason (MCP `isError`). `tool.CheckEnvelope` holds an answer to this contract, and `TestCatalogEnvelopeContractEveryAction` runs every action through both surfaces against it.

Data the task views carry for every reader:

- `get_task` (and each task of Swarm Control's `wait_tasks`) has `review` when a review recorded a verdict: `verdict` (`ok`, `warn` or `block`), `round`, `step`, `delivery_id`, and a `findings` summary with `findings_truncated` and `more` (the call with the full text). `progress.workflow` still shows the loop's state and a blocking review's full findings.
- A landed task's `result.commits` and `result.files` come from the task's own range even after its checkout was removed: base..its branch in its source repository, or base..the head its integration recorded once the branch is deleted.
- `list_machines` and `agents_at_work` use one reachability rule (`tool.FleetMachineReachable`): when the answer contacted the machine, it is online exactly when that contact succeeded; otherwise the last recorded contact decides.

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

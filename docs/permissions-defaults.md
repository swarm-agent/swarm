# Default tool permissions

This page records what Swarm's default permission policy decides for every
built-in agent tool and action, what each one actually does, and what changed
in this audit. Code is the authority: `swarmd/internal/permission/policy.go`
(`explainPolicyDecision`, `buildPolicyEvalContext`, `defaultPolicyDecision`),
`swarmd/internal/permission/service.go` (`AuthorizeToolCall`) and the agent
tool contracts in `swarmd/internal/agent/system_agent_registry.go`, compiled by
`swarmd/internal/run/agent_contract.go`.

## How a call is decided

Evaluation order for one tool call:

1. Hard denials: malformed `manage_workspace` actions, dangerous recursive
   deletes (`rm -rf /`, `~`, `.` …), saved phrase-deny rules.
2. Dedicated capabilities with their own policy: `session_deploy`,
   `plan_acceptance` (`exit_plan_mode`, plan amendment/new-plan requests),
   native artifact selection.
3. Built-in mode denials: plan mode denies `write`/`edit`; the `read`
   execution setting denies `write`, `edit`, `bash` and `manage_artifact`
   write actions; `readwrite` denies `bash`.
4. Explicit deny rules (saved rules and the agent's disabled tools). A deny for
   a transport tool also blocks its split identities (see below).
5. Account memory changes always ask.
6. Explicit allow/ask rules: first the agent's **compiled tool contract**, then
   saved account rules.
7. Built-in allows for clearly read-only single bash commands.
8. `defaultPolicyDecision`.

**The compiled tool contract matters more than the defaults.** Every tool a
built-in agent has enabled compiles to an `allow tool: <name>` rule evaluated at
step 6, before the defaults. A default "ask" therefore only holds for an agent
that has the tool enabled when the action has its own policy identity (for
example `session_archive`, `worktree_promotion`, `skill_change`,
`workspace_delete`, `action_change`, `environment_change`, `automation_change`,
and now `project_task_launch` / `project_removal`). Actions without their own
identity are effectively pre-approved for that agent.

**Account bypass** (only effective while the agent sandbox is active) approves
every call that is not denied, except plan acceptance, `ask_user` and client
tools. The `+bypass` column below is the mode suffix seen by
`defaultPolicyDecision`; account bypass overrides it.

Legend: **allow**; **ask** (asks in auto, plan, read and readwrite, allowed with
the bypass suffix); **ask!** (asks even with the bypass suffix); **deny r/rw**
(denied by the read / readwrite execution setting). "Overlay" names the roles
whose compiled contract pre-approves the action.

## Decisions

| Tool | Action | Effect class | Default before | Default after | Why |
|---|---|---|---|---|---|
| `read`, `list`, `find`, `search` | — | read-only | allow | allow | Reads inside the session's workspace scope. |
| `skill_use` | — | read-only; can read user-level skill folders outside the workspace | allow | allow | Instruction lookup only. |
| `media_inspect` | — | read-only | ask by default; overlay: every role that has it | unchanged | Not in the default allow list; always reached through the overlay in practice. |
| `git_status`, `git_diff` | — | read-only | allow | allow | Read-only Git. |
| `write`, `edit` | — | writes inside the workspace | allow; deny in plan mode and r | unchanged | Ordinary workspace edits. |
| `git_add`, `git_commit`, `git_init`, `git_commit_initial` | — | writes inside the workspace (Git) | allow; deny r | unchanged | Workspace Git; Workspace Onboarding forces ask. |
| `bash` | one simple read command with in-scope paths | read-only | allow (built-in) | unchanged | Allowlisted readers only. |
| `bash` | anything else | writes, deletes, reaches the network | ask; deny r/rw; overlay: Swarm, Orchestrator | unchanged | Left asking by default; see owner list: the overlay pre-approves it. |
| `websearch`, `webfetch` | — | reaches the network (paid search provider) | allow | unchanged | Left as is; listed for the owner. |
| `webdownload` | — | reaches the network, writes into the workspace | ask; overlay: Swarm, Coder | unchanged | Network write. |
| `ask_user` | — | waits for the person | always waits for an answer | unchanged | Input boundary, not an approval. |
| `exit_plan_mode` | — | creates a gated plan card | plan acceptance policy (ask) | unchanged | The plan card is the gate. |
| `compact` | — | writes the agent's own session state | allow | unchanged | Own-session bookkeeping. |
| `edit_pending_plan` | — | edits the parent's pending plan card | approved inline only in the reserved Plan sidechat; otherwise ask | unchanged | The parent's plan card stays the gate. |
| `task_progress` | set/add/update/complete todos, done, blocked, status | writes the calling run's own session state | allow | unchanged | Bookkeeping of the agent's own run. |
| `manage_todos` | list, summary | read-only | allow | unchanged | — |
| `manage_todos` | create, update, delete*, reorder, batch | writes Swarm state (workspace todos) | allow | unchanged | Bookkeeping; `update` is not limited to the caller's session (owner list). |
| `manage_projects` | list, get, list_sources, inspect_source, inspect_files, list_media, get_media, list_tasks, get_task | read-only | allow | allow | Reads the caller's projects and sources. |
| `manage_projects` | propose_task / create_task, card waits for Approve, title and source given | creates a task | allow | allow | Start tasks decides: `auto` starts it at once, `approve` keeps it waiting. |
| `manage_projects` | propose_task / create_task, card waits for Approve, **no title or no source** | creates a gated card; small Router call to name it or pick the source | ask | **allow** | Owner decision: the Router call is accepted running cost; Start tasks decides whether the task waits. |
| `manage_projects` | propose_task / create_task with `auto_approve`, plan agent, media agent/intent/operation, or no agent and no code/audit intent | starts work or spends at once | ask (`project_task_launch`) | **allow** (account Start tasks decides) | Start tasks `auto` starts every task (`auto_approve` is ignored); `approve` makes it wait. Creating never asks. |
| `manage_projects` | approve_task, accept_task, deploy_task | starts a task's session (approves the card) | ask (`project_task_launch`) | **account Start tasks**: allow under `auto`; ask under `approve`, even with bypass | The Start tasks rule is the single gate; an explicit deny still refuses. |
| `manage_projects` | integrate_task | merges a task's finished work | ask (`project_task_launch`) | **allow** | Starts no task, so it never asks. |
| `manage_projects` | delete, delete_task, archive_task | deletes/archives | ask; **overlay: Orchestrator (allow)** | **ask** (`project_removal`) | Destructive. |
| `manage_projects` | create, update, update_task, edit_requirements, refine_task, reopen_task, unarchive_task, reconcile_tasks, wait_tasks, report_task | writes Swarm state; reopen_task starts a new attempt; create may start context generation | ask; overlay: Orchestrator (allow) | unchanged | Listed for the owner. |
| `manage_sessions` | inspect, list, list_by_state, search, get, read_messages, review_worktrees, git_status | read-only | allow | unchanged | — |
| `manage_sessions` | create (with prompt), send_message (trigger), stop, pause, compact | writes other owned sessions; can start runs and model calls | allow | unchanged | Listed for the owner. |
| `manage_sessions` | commit, archive, unarchive | writes Git / archives | ask! (own identities) | unchanged | Split identities. |
| `manage_sessions` | deploy | starts agent sessions | session deploy policy (ask) | unchanged | Dedicated capability. |
| `manage_memory` | inspect | read-only (account memory) | allow | unchanged | Orchestrator only. |
| `manage_memory` | remember, edit, forget | writes/deletes account memory | ask! even with an allow rule | unchanged | Explicit consent. |
| `manage_workspace` | inspect, list, maps, discover_worktrees, media directory list | read-only | allow | unchanged | — |
| `manage_workspace` | set_session, set_default, adopt_worktree, add/remove_source_media_directory | writes Swarm state | allow | unchanged | Selection inside the account catalog; listed for the owner. |
| `manage_workspace` | create, update, delete, update_map, reclaim/copy_worktree, cancel_worktree_recovery | writes/deletes catalog and worktrees | ask (own identities) | unchanged | Split identities. |
| `manage_worktree` | inspect, recall, help, inspect_source, retain_source | read-only / own session state | allow | unchanged | — |
| `manage_worktree` | integrate | merges child work into the session's own lane | allow | unchanged | Cannot reach the person's branch; recovery integrate is listed for the owner. |
| `manage_worktree` | promote | merges into the person's branch | ask! (`worktree_promotion`) | unchanged | Promotion boundary. |
| `plan_manage` | read actions, new plan, checkpoint/subtask progress | writes the agent's own plan | allow | unchanged | Own plan bookkeeping. |
| `plan_manage` | revisions of an existing plan | writes the agent's own plan | ask!; overlay: Swarm (allow) | unchanged | Listed for the owner. |
| `plan_manage` | amend_plan, request_new_plan | creates a gated plan card | ask (plan acceptance) | unchanged | Card gate. |
| `task` | help, status | read-only | allow | unchanged | — |
| `task` | spawn / start | starts subagents (spends) | ask!; subagent policy: 5 automatic launches per run, then ask | unchanged | Bounded by the subagent policy. |
| `manage_agent` | inspect, list, get, get_orchestration_policy | read-only | allow | unchanged | No built-in agent has this tool. |
| `manage_agent` | create, update, delete, custom tool changes | writes/deletes agent definitions | ask! | unchanged | Identity not split; a custom agent with the tool enabled pre-approves it (owner list). |
| `manage_agent` | activate_primary, set/remove_active_subagent, update_orchestration_policy | writes settings | allow | unchanged | Missing from the mutation list (owner list). |
| `manage_skill` | inspect, list, get | read-only | allow | unchanged | — |
| `manage_skill` | create | writes inside the workspace | allow | unchanged | Listed for the owner. |
| `manage_skill` | update, delete | writes/deletes in the workspace | ask (`skill_change`) | unchanged | Split identity. |
| `manage_theme` | inspect, list, get | read-only | allow | unchanged | — |
| `manage_theme` | create, update, delete, set (confirmed) | writes settings (workspace, account or global theme) | allow | unchanged | Listed for the owner. |
| `manage_actions` | list, get / changes | read-only / writes Action definitions | allow / ask (`action_change`) | unchanged | Definitions only; running needs the user. |
| `manage_connections` | list, get, capabilities / changes, check | read-only / writes state, reaches the network | allow / ask (`connection_change`) | unchanged | — |
| `manage_environments` | list, get, export, summaries / changes / build, deploy, exec, stop, destroy | read-only / writes state / runs and deletes deployments | allow / ask (own identities) | unchanged | — |
| `manage_workers` | list, get, review, context, progress | read-only | allow (`automation_read`) | unchanged | Orchestrator only. |
| `manage_workers` | propose | creates a gated proposal | pending review | unchanged | Proposal waits for acceptance. |
| `manage_workers` | other changes, run, cancel | writes state, dispatches runs | ask! | unchanged | — |
| `swarm_design` | make or edit designs | model calls (daily spending limit and worker budget), writes new files under its batch folder or the one `edit_path` file | — (new tool) | allow; deny r | Replaced `manage_design`; enabled for the Orchestrator and Designer only. |
| `manage_artifact` | read actions | read-only | allow | unchanged | — |
| `manage_artifact` | import, select, publish parts, materialize, promote, delete | writes app storage, the workspace (materialize/promote) or deletes | allow; deny r | unchanged | Session-owned storage. |
| `manage_artifact` | generate_image, generate_video(_story), chain_video, generate_audio | spends money (media providers) | ask; **overlay: Swarm, Orchestrator, Image, Video** | unchanged | Listed for the owner (trusted workers rely on the overlay). |
| `manage_video` | all | reads, writes app state; start_transcription spends and reaches the network | allow | unchanged | Studio acceptance remains the gate; transcription listed for the owner. |
| Custom tools | — | fixed command or client tool | ask; client tools wait for the client; confirmed client tools (write by default) first wait for the person's approval | unchanged | See `containers/sealed/README.md` (Confirmations). |

## What changed

- `manage_projects` `propose_task`/`create_task` that only create a card waiting
  for Approve no longer need a title or a source to run without approval.
- `manage_projects` `approve_task`, `accept_task`, `deploy_task`, proposals that
  start work or spend at once, `delete`, `delete_task` and `archive_task` now
  have their own policy identities (`project_task_launch`, `project_removal`).
  The Orchestrator's compiled `allow tool: manage_projects` no longer
  pre-approves them, so they ask. A saved `deny` for `manage_projects` still
  blocks them; a remembered "always allow" from their prompt saves a rule for
  that identity only. Account bypass is unchanged.
- The account `Start tasks` rule (`docs/access-model.md`) is now the single gate
  for `project_task_launch`: creating a task and integrating never ask; under
  `auto` approving, accepting and deploying run too; under `approve` they ask
  the person even with bypass. Saved tool rules no longer change it; an
  explicit deny still refuses. With `machine`, `linked.{name}.work` decides
  instead (`ask` waits only before a task is sent or started there).

## Left asking, or left for the owner

Still asking by design: every bash call outside the read allowlist (default
policy), `webdownload`, media generation (default policy), `manage_memory`
changes, worktree promotion, session commit/archive/unarchive/deploy, workspace
catalog and map changes, skill update/delete, Action/connection/environment
changes, worker changes and runs, `manage_agent` definition changes, task
launches beyond the subagent budget, project removal, and starting a task
under Start tasks `approve`.

Recommended for an owner decision (not changed here):

1. **Compiled contract pre-approval.** Swarm and the Orchestrator run `bash`
   (including deletes declared critical, `git push` and network commands)
   without a prompt, and Swarm, the Orchestrator, Image and Video generate
   media without a prompt, because their enabled tools compile to allow rules.
   Splitting these into their own identities needs a trusted path for the
   media workers first.
2. Account bypass approves `manage_memory` changes and worktree promotion,
   although the code comments say those stay explicit.
3. `manage_sessions` create/send_message/stop/pause/compact act on other owned
   sessions and can start runs without a prompt.
4. `manage_agent` activate_primary, set/remove_active_subagent and
   update_orchestration_policy change settings without a prompt (no built-in
   agent has this tool; custom agents can).
5. `manage_theme` changes, `manage_skill` create, `manage_workspace`
   set_default and media directory changes write settings or workspace files
   without a prompt.
6. `manage_todos` update can edit any todo in the workspace, not only the
   caller's.
7. `websearch`/`webfetch` reach a paid network provider without a prompt;
   `manage_video` start_transcription uploads media to a paid model.
8. Bookkeeping that could stay approval-free: `wait_tasks` and `report_task`
   (own run), `edit_requirements` (invalidates the card's approval), and plan
   revisions of the agent's own plan.

## Over-scoping by role

No tool grants were removed in this change; each candidate below changes agent
behavior and needs an owner decision.

| Role | Has | Job needs it? | Recommendation |
|---|---|---|---|
| Orchestrator | `bash` (pre-approved, always auto mode) | Rarely: project chats have no checkout | Remove, or keep with prompts. |
| Orchestrator | `manage_artifact`, `manage_video`, `manage_environments`, `manage_connections`, `manage_theme` | Partly (media requests, project themes) | Decide; `TestOrchestratorToolIsolationContract` asserts the opposite and currently fails. |
| Orchestrator | `webfetch`, `websearch` | Research for planning | Keep. |
| Orchestrator | `manage_projects` approve/deploy/delete | Under Start tasks `auto`, approve/deploy just run; delete asks | Delete asks; approve/deploy follow Start tasks. |
| Swarm | `manage_theme`, `webdownload`, `manage_skill`, `manage_actions` | Occasionally | Keep with prompts for changes. |
| AI sidechat | Inherits the parent's contract (Swarm: `write`, `edit`, `manage_sessions`, `manage_worktree`, `manage_theme`, `manage_skill`, `manage_actions`) | A sidechat answers questions | Narrow to read tools. |
| Coder | `webdownload`, `webfetch`, `websearch` | Rarely, inside an isolated worktree | Consider removing `webdownload`. |
| Image, Video | `manage_artifact` (every action) | Only generation and reads | Narrow by action. |
| Finder | read tools, `websearch`, `webfetch` | Yes | Keep. |
| Compact, Router, Idea | no tools | — | Keep. |
| Custom agents | Any known tool can be enabled; each becomes pre-approved | Owner-defined | Consider keeping the default policy for sensitive tools. |

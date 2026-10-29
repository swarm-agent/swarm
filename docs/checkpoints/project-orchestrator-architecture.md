# Project Orchestrator Architecture & Onboarding Specification

**Status**: Approved Architecture & Implementation Blueprint  
**Target View**: Orchestrate (`/orchestrate`, `OrchestrateView.tsx`)  
**Target Agent**: `swarm-orchestrator` (`system_agent_registry.go`)  
**Backend Storage**: `/v3/projects` (Pebble-backed)  

---

## 1. Core Motivation: The Mental Shift

### The Problem Today
Today, Swarm operates directly on **Workspaces** (folders on disk) and **Sessions** (execution transcripts). This forces the human operator to act as a database administrator:
1. **Flat Session Graveyard**: Dozens of dead sessions and "needs review" items clutter the sidebar.
2. **Tab Fragmentation**: Background Automations (`/v3/automations/v2`) and Deliverables (`/v3/deliverables`) are isolated in separate tabs away from where work is requested.
3. **Context Window Bloat**: Linking multiple workspaces (e.g. `swarm-go`, `swarmcrit`, `work`) dumps every repository's `AGENTS.md` directly into the top-level chat prompt (over 15,000+ tokens of low-level rules).
4. **Harness Tool Overload**: The primary AI agent is weighed down with raw media tools (`manage_video`, raw artifact byte manipulation, `manage_environments`), tempting models into doing low-level byte assembly instead of delegating.

### The Solution: Projects as the Primary Unit of Work
We elevate the hierarchy from raw plumbing to an executive mental model:

| Entity | What It Is | Who Manages It | What It Holds |
| :--- | :--- | :--- | :--- |
| **Workspace** *(Plumbing)* | A directory or Git repo on disk. | OS / Git | Source files, git HEAD, commit history, and a local `AGENTS.md`. |
| **Project** *(Coworker Scope)* | A cohesive product or goal (e.g. "Swarm Core Platform", "Video Studio", "Marketing Pipeline"). | **Swarm Orchestrator** | 1+ linked Workspaces, a synthesized `project.md`, background Automations, and ongoing Tasks. |
| **Task** *(Encapsulated Unit)* | A specific initiative or user request (*"Build 3 promo videos"*, *"Fix composer popup"*). | **Sub-Orchestrator** | An underlying V3 session in an isolated Git worktree, subagents (Coder/Designer), steppers, and deliverables. |

---

## 2. Facilitating Project Onboarding Flow (Dual-Mode)

When a user opens Orchestrate with no projects, or clicks `+ New Project`, Swarm enters the **Facilitating Project Onboarding** state.

```
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                 ORCHESTRATE VIEW                                       │
├────────────────────┬───────────────────────────────────────────┬───────────────────────┤
│  LEFT: PROJECTS    │          MIDDLE: FACILITATING ONBOARDING   │  RIGHT: AI CHAT       │
├────────────────────┼───────────────────────────────────────────┼───────────────────────┤
│ ＋ New Project     │  📂 Create Your Project                   │ 🤖 Swarm Orchestrator │
│                    │                                           │                       │
│ (No projects yet)  │  A Project groups your workspaces,        │ "I see you have       │
│                    │  background automations, and tasks.       │  ~/swarm-go and       │
│                    │                                           │  ~/swarm-social.      │
│                    │  1. Select Workspaces:                    │                       │
│                    │     [☑] ~/swarm-go (Core Daemon)          │  Would you like to    │
│                    │     [☑] ~/swarm-social (Video Tools)      │  bundle them into     │
│                    │     [＋ Add another folder...]            │  one project, or      │
│                    │                                           │  start fresh?"        │
│                    │  2. Project Context (project.md):         │                       │
│                    │     ┌───────────────────────────────────┐ │  [User can either     │
│                    │     │ # Swarm Platform                  │ │   click checkboxes    │
│                    │     │ - ~/swarm-go: Backend Go daemon   │ │   in middle OR type   │
│                    │     │ - ~/swarm-social: Video automation│ │   in chat on right]   │
│                    │     └───────────────────────────────────┘ │                       │
│                    │     [ Generate with AI ] [ Edit by Hand ] │                       │
│                    │                                           │                       │
│                    │  [ Create & Activate Project → ]          │                       │
└────────────────────┴───────────────────────────────────────────┴───────────────────────┘
```

### Dual-Mode Execution (Hand or AI)
1. **Middle Canvas Takeover**:
   - Takes over the middle canvas smoothly, reusing existing clean Modern Navy/Slate UI components.
   - Clearly and concisely explains the purpose of Projects.
   - Lists all existing registered workspaces with toggle checkboxes.
   - Provides an `＋ Add another folder` button opening the native folder picker.
2. **Parallel Right-Panel AI Conversation**:
   - Non-blocking: no modal dialogs that lock interaction.
   - The user can simply chat with the Orchestrator: *"Group my backend in ~/swarm-go and frontend in ~/web into a project called Main App"*.
   - The AI uses `manage_projects` to select or add the workspaces.
3. **Context Generation (`project.md`)**:
   - Creating the project (`POST /v3/projects`) makes Swarm read `AGENTS.md`, `README.md` and the file tree of each selected workspace **once** (bounded, untrusted input) and generate `project.md` with the configured Router model; `context_generation.status` goes `pending`/`running` → `ready` or `failed` (retry: `POST /v3/projects/{id}/context:retry`). Workspaces and `project.md` can be edited only once it is `ready`.
   - The generated document is a concise, high-level `project.md`:
     - High-level architecture and responsibilities of each workspace.
     - Build/test entrypoints.
     - Shared design or coding principles.
   - The generated `project.md` appears in a review card in the middle canvas.
   - The user can inspect it, make quick edits, or simply say *"Looks great, confirm"* / click **[ Create & Activate Project ]**.
4. **Activation**:
   - Project is saved to `/v3/projects` and marked active.
   - Middle canvas transitions into the full 3-zone Orchestrate Canvas.

---

## 3. The Project ➔ Task Hierarchy & Data Model

### A. Pebble Storage Contract (`/v3/projects`)
Stored under Pebble key prefix `project:v3:`:

```json
{
  "id": "proj_a1b2c3d4",
  "name": "Swarm Platform",
  "description": "Core daemon, desktop client, and video production pipeline",
  "workspaces": [
    {
      "workspace_id": "ws_1",
      "path": "/path/to/swarm-go",
      "role": "primary_code",
      "label": "Core Engine & Daemon"
    },
    {
      "workspace_id": "ws_2",
      "path": "/path/to/swarm-social",
      "role": "auxiliary",
      "label": "Social & Video Studio"
    }
  ],
  "project_context": "# Swarm Platform\n\n## Subsystems\n- `swarm-go`: Go daemon, V3 sessions, Pebble storage.\n- `swarm-social`: Video rendering scripts and templates.\n\n## Conventions\n- Tests: `scripts/run-critical-tests.sh fast`\n- Branch: `dev`\n",
  "active_task_ids": ["task_9918", "task_9920"],
  "automation_ids": ["auto_nightly_e2e", "auto_pr_monitor"],
  "primary_session_id": "sess_orchestrator_platform",
  "created_at": 1790271000,
  "updated_at": 1790271500
}
```

### B. Tasks as Encapsulated Sub-Orchestrators
* Users never manage raw V3 sessions directly.
* When the user gives an objective (*"Make 3 video variations"* or *"Refactor auth middleware"*), the Orchestrator creates a **Task**.
* **Under the hood**:
  - The Task is backed by a dedicated V3 session in an isolated Git worktree.
  - Subagents (`Coder`, `Designer`, `Finder`) work inside that isolated worktree.
* **On the Middle Canvas**:
  - The task renders as an active pipeline card with a 4-step workflow stepper (`Themes ➔ Generate ➔ Edit ➔ Deliver`).
  - Progress updates in realtime via V3 durable stream events.
  - Generated deliverables (videos, diff previews, PR links) appear as action cards.
* **Acceptance & Auto-Cleanup**:
  - When the user reviews and clicks **Accept & Ship**, the Orchestrator promotes/merges the changes and archives the underlying task session automatically. The user's sidebar remains pristine.

---

## 4. The Lean `swarm-orchestrator` System Agent

### Agent Registration (`system_agent_registry.go`)
* **ID**: `system-orchestrator`
* **Display Name**: `Swarm Orchestrator`
* **Model**: the account's configured orchestrator role model (agent-model settings); never a hardcoded model.
* **Context**: works from the project's `project.md`. Project chats have no ambient checkout; the orchestrator discovers sources with `manage_projects action=list_sources` / `inspect_source`.

### Toolset (code-owned: `SwarmOrchestratorAgentToolContract`)

The checked-in tool contract is the authority; this section summarizes it.

* **Delegation is `manage_projects`**, not a `task` tool (the orchestrator has none). Its loop is delegate, watch, read, follow up or stop: `propose_task` sends a self-contained brief to an agent (`coder`, `finder`, `designer` or `swarm`), `wait_tasks` records a durable watch and returns at once (the Orchestrator answers the person and ends its turn; a new turn brings the outcomes; `cancel_watch` stops watching), `get_task` reads one, `list_tasks` lists current work, `reopen_task` continues a task, and `integrate_task` merges finished work when the person asks. Linked machines: `list_machines`, then `machine` on the remote actions; `list_linked_tasks` and `read_task_session` read what was sent there. Projects: `list` (id, name, workspaces, current task counts), `get` (adds project.md cut to about 2 KB; `detail: "context"` returns it whole), `create`, `update`, `delete`, `list_sources`, `inspect_source`, `inspect_files`. The schema the Orchestrator is shown has no plan-era actions (`approve_task`, `accept_task`, `deploy_task`, `refine_task`, `edit_requirements`, `reconcile_tasks`), no `report_task` (task runs only) and no media fields (`manage_media` owns media); those handlers remain for their other callers. `synthesize_context` is retired: Swarm generates `project.md` when a project is created (`POST /v3/projects`), and `POST /v3/projects/{id}/context:retry` reruns a failed generation.
* **`plan_manage` and `exit_plan_mode` are disabled**: the orchestrator plans on project task cards, not session plans.
* **Enabled**: `read`, `search`, `find`, `list`, `bash` (without Coder-only `committed_source`), `websearch`, `webfetch`, `media_inspect`, `ask_user`, `manage_projects`, `manage_workspace` (catalog and Workspace Map actions only; it registers existing folders and never creates directories or runs `git init`), `manage_sessions` (without `deploy` or commit actions), `manage_workers`, `manage_custom_agents`, `manage_workflows`, `manage_jobs`, `manage_memory`, `manage_media`, `swarm_design`; the runtime adds `lease_environment`, `task_progress` and the person's machine tools. `manage_automation` (a copy of `manage_workers`), `manage-theme` and `manage-worktree` (Coder lineage tooling) stay with the agents that use them. `TestOrchestratorFixedContextStaysWithinBudget` keeps the shown tool definitions at or under 34 KB and the prompt at or under 4.5 KB.

---

## 5. End-to-End User Journey Example

1. **First Launch**:
   - User opens Orchestrate. No projects exist.
   - Middle canvas displays **Facilitating Project Onboarding**:
     - Lists `~/swarm-go` (pre-selected from app install).
     - User clicks `＋ Add another folder` to include `~/swarm-social`.
   - In parallel, Orchestrator on the right greets the user:
     *"I see ~/swarm-go and ~/swarm-social. Shall I analyze both and build your project architecture?"*
2. **Project Creation & Context**:
   - User clicks **[ Create & Activate Project ]**; Swarm generates `project.md` from `swarm-go/AGENTS.md`, `swarm-social/README.md` and the file trees, showing high-level roles.
   - Once it is ready, the user reviews it and edits anything wrong; the orchestrator conversation opens.
3. **Daily Work**:
   - Middle canvas shows active Automations on top (e.g., Nightly E2E Test).
   - User types in chat: *"Create 3 video variants showcasing our new CLI release."*
   - Orchestrator proposes a Task Card (`manage_projects action=propose_task`); the user approves it.
   - Orchestrator watches the launched task with `manage_projects action=wait_tasks`, tells the user, and ends its turn.
   - Subagents complete video generation.
   - Daemon starts a new Orchestrator turn with the outcomes ➔ Orchestrator verifies videos and renders 3 video cards with `Download` and `View` actions in the Deliverables area.
   - User accepts deliverables ➔ Task moves to completed, and underlying sessions are archived automatically.


---

## 6. Canonical Canvas Architecture: Variant 3 (Autonomous Worker Fleet)

Based on review of the 5 distinct middle canvas variants (Matrix, Kanban, Fleet, Split Studio, Timeline), **Variant 3: Autonomous Worker Fleet** has been selected as the canonical design for the Swarm Project Orchestrator.

### Why Variant 3 Fits the Project Mental Model
1. **Coworker & Fleet Autonomy**:
   - The user does not want to micromanage tasks individually. A Project deploys **Autonomous Workers** (`Worker V2`), each assigned a role (e.g. `@Video Swarm Dispatcher`, `@Code Verifier`, `@Testbench Runner`).
   - Workers carry their own triggers (`on-demand trigger`, `cron hourly`, `interval 30m`), execution budgets, and active job counters.
2. **Worker-Partitioned Task Queues**:
   - Tasks are naturally grouped and owned by the worker that generated or is executing them.
   - Users can see at a glance what each worker is working on (`active jobs`, `current job`).
3. **100+ Task Scalability with Expandable Drawers**:
   - Fast instant search across 100+ tasks by name, worker handle (`@Video Swarm`), or category tag.
   - Status segmented filters (`All (100)`, `Running (6)`, `Needs Review (8)`, `Queued (72)`, `Completed (14)`).
   - Each task has an expandable drawer (`expandedTaskId`) revealing attached deliverables (16:9 thumbnails, play overlay to launch video inspector, "Accept Deliverable" action) and code diffs with line numbers.
4. **Noiseless Micro-Automation Ticker**:
   - Sits above the worker fleet as a slim status bar showing overall project health, active session progress, 4-stage pipeline stepper, and media attachments without vertical clutter.

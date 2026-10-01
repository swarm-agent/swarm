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
3. **Context Synthesis (`project.md`)**:
   - The AI inspects `AGENTS.md` and `README.md` across selected workspaces **once**.
   - It synthesizes a concise, high-level `project.md` (~300–400 tokens):
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
* **ID**: `system/swarm-orchestrator`
* **Display Name**: `Swarm Orchestrator`
* **Model**: Default account model (`gemini-3.8-flash`, thinking: `low` or `high`)
* **Prompt Isolation**: Loads **only `project.md`** into context. Repository `AGENTS.md` files are strictly withheld from the orchestrator and delivered only to spawned Coder/Designer subagents within their respective workspaces.

### Toolset Definition (Pruned & Focused)

#### ❌ Excluded Tools (Eliminating 15,000+ Tokens of Bloat)
1. `manage_video`: Low-level timeline/track assembly. Video work is delegated to Video Swarms or Designers.
2. `manage_artifact`: Raw byte/HTML/SVG authoring. Delegated to Designers.
3. `manage_environments` & `manage_connections`: Docker/SSH execution containers are delegated to test runners or Coders.
4. `manage_actions` & `manage_theme`: Pure admin settings.

#### ✅ Included Executive Toolset (6 Tools)
1. **`task`**:
   - The primary delegation mechanism.
   - Spawns `coder`, `designer`, `finder`, or parallel `swarm` waves (e.g. `agent_type="video"`, `count=3`).
   - Supports **Asynchronous Task Dispatch**: When a heavy task is launched, the Orchestrator confirms dispatch (*"Dispatched Task #42: Generating 3 video variations. Standing by..."*) and completes its current turn. The daemon automatically resumes the Orchestrator when child sessions emit completion handoffs.
2. **`manage_projects`** *(New)*:
   - `action="create"`: creates a project with bound workspaces.
   - `action="list"` / `action="get"`: inspects project boundaries.
   - `action="synthesize_context"`: reads repo docs and drafts `project.md`.
   - `action="update_context"`: updates `project.md` when project requirements evolve.
   - `action="archive_task"`: merges changes and archives finished task sessions cleanly.
3. **`manage_workers`**:
   - Manages cron and trigger Automations (`/v3/automations/v2`).
   - Surfaces active automations at the top of the middle canvas.
4. **`plan_manage`**:
   - Drives structured checkpoint lifecycle and steppers on the middle canvas.
5. **`read`, `search`, `find`, `list`**:
   - Allows high-level repository mapping and project inspection without running code.
6. **`bash`**:
   - Bounded host inspection commands (e.g. `git status`, test runner execution).

---

## 5. End-to-End User Journey Example

1. **First Launch**:
   - User opens Orchestrate. No projects exist.
   - Middle canvas displays **Facilitating Project Onboarding**:
     - Lists `~/swarm-go` (pre-selected from app install).
     - User clicks `＋ Add another folder` to include `~/swarm-social`.
   - In parallel, Orchestrator on the right greets the user:
     *"I see ~/swarm-go and ~/swarm-social. Shall I analyze both and build your project architecture?"*
2. **Context Synthesis & Confirmation**:
   - AI reads `swarm-go/AGENTS.md` and `swarm-social/README.md`.
   - AI drafts `project.md` showing high-level roles.
   - User reviews the draft in the middle card and clicks **[ Create & Activate Project ]**.
3. **Daily Work**:
   - Middle canvas shows active Automations on top (e.g., Nightly E2E Test).
   - User types in chat: *"Create 3 video variants showcasing our new CLI release."*
   - Orchestrator creates Task Card in middle canvas with 4-stage stepper.
   - Orchestrator invokes `task(mode="swarm", agent_type="video", count=3)`.
   - Orchestrator confirms: *"Task #1 dispatched. Generating 3 videos. Standing by."*
   - Subagents complete video generation.
   - Daemon resumes Orchestrator ➔ Orchestrator verifies videos and renders 3 video cards with `Download` and `View` actions in the Deliverables area.
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

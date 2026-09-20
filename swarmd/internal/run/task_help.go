package run

import "strings"

func taskHelpText(topic string) string {
	switch strings.ToLower(strings.TrimSpace(topic)) {
	case "program", "task_program":
		return `Task Program Specification:
- A Task Program implements multi-stage dependent delegated work across Coders, Finders, and Designers.
- Schema:
  - id: Unique lowercase program id (e.g. "feature-v1", matches ^[a-z][a-z0-9_-]{0,63}$)
  - max_concurrency: Optional positive integer capping concurrent jobs
  - stages: Non-empty array of execution stages:
    - id: Stage id (e.g. "foundation", "audit", "build")
    - title: Optional human-readable stage title
    - depends_on: Earlier stage IDs that must reach integration barrier first (required for stage 2+)
    - dependency_evidence: Reason stage is ready or what prior integrated state unlocks it
  - jobs: Non-empty array of job definitions:
    - id: Job id (e.g. "core-contract", "audit-files", "ui-card")
    - stage_id: ID of the stage this job belongs to
    - agent_type: "coder" | "finder" | "designer" (or subagent_type)
    - title: Concise cosmetic title (ideally 3 words)
    - meta_prompt: Complete instructive assignment for the subagent worker
    - deliverable: Specific output parent will verify
    - acceptance_criteria: Array of criteria strings for completion
    - dependency_evidence: Why this job is ready or what unlocks it
    - depends_on: Earlier job IDs whose accepted handoffs are required
    - workspace_path: Optional target workspace root (Coder/Finder only)
    - owned_scope: Workspace-relative files or directories:
      - Coder: Specific files or directories (e.g. ["pkg/api/**"]). Concurrent Coders in the same stage must have non-overlapping owned scopes. Defaults to isolated worktree if omitted.
      - Finder: Target search/read paths (defaults to ["."] if omitted). Finder is read-only.
      - Designer (managed): Defaults to output_mode="managed"; MUST omit owned_scope and workspace_path (produces Artifact V3 artifacts).
      - Designer (workspace): Set output_mode="workspace" and provide concrete non-overlapping owned_scope without wildcards.
    - output_mode: "managed" (default) or "workspace" (Designer jobs only)
    - output_requirements: Optional managed preset or dimensions (Designer only)
    - animation_profile: Optional animation profile (motion_ui, spatial_3d, vector_playback, final_render)
- Example (multi-agent across Finder, Coder, and Designer):
  {"action":"start","prompt":"Implement and design user notifications","program":{"id":"notif-v1","stages":[{"id":"discovery","dependency_evidence":"Initial discovery ready"},{"id":"implement","depends_on":["discovery"],"dependency_evidence":"Discovery complete"}],"jobs":[{"id":"audit_code","stage_id":"discovery","agent_type":"finder","title":"Audit Notification Code","meta_prompt":"Inspect notification models and existing API routes.","deliverable":"Notification audit report","dependency_evidence":"Ready to search","acceptance_criteria":["Audit completed"]},{"id":"backend_api","stage_id":"implement","depends_on":["audit_code"],"agent_type":"coder","title":"Backend Notification API","meta_prompt":"Implement notification dispatch endpoint in internal/notify and author tests. Commit with git_commit.","deliverable":"Committed notification endpoint","owned_scope":["internal/notify/**"],"dependency_evidence":"Audit completed","acceptance_criteria":["Dispatch endpoint committed"]},{"id":"card_design","stage_id":"implement","depends_on":["audit_code"],"agent_type":"designer","title":"Notification Card UI","meta_prompt":"Design a responsive notification banner card component.","deliverable":"Notification banner artifact","output_mode":"managed","dependency_evidence":"Audit completed","acceptance_criteria":["Banner component artifact ready"]}]}}`

	case "committed_source", "correction":
		return `Committed Source Delegation Specification:
- committed_source redelegates a fresh isolated Coder from an exact prior committed child's HEAD commit.
- Supported only for regular Coder launches (single shorthand or launches array).
- Schema:
  - task_call_id: Parent task call ID that launched the original child
  - child_session_id: Durable session ID of the completed committed child
  - head_commit: Exact 40-character or 64-character hexadecimal commit hash of the child's clean HEAD
- Invariants:
  - Cannot be combined with recovery_source_digest or workspace_path.
  - Target destination, branch, and canonical repository are preserved from the original child.
  - The new Coder's worktree is checked out at head_commit C; inherited delivery base B is preserved for integration.
  - Delegation does not modify the original child worktree or session; the source must remain clean and unchanged through launch.
  - First use manage-worktree action=recall; copy the returned committed_source, never supply the sibling's private path as workspace_path.
  - Source selection requires the original managed worktree to remain available. A completed program child whose worktree was removed after integration is ineligible; use a new normal launch from its already-integrated owned lane instead.
  - Owned destinations use integrate; captured cross-workspace destinations remain promotion-only. Correction launch never advances either destination.
  - If publication fails after child registration, the error reports preserved inactive child IDs. Inspect those children/recall before issuing a new task call; replaying the same call is rejected rather than starting duplicate workers. No automatic deletion occurs.
- Example:
  {"prompt":"Fix failing test in prior child","subagent_type":"coder","meta_prompt":"Fix edge case in auth","owned_scope":["internal/auth/**"],"committed_source":{"task_call_id":"call_123","child_session_id":"sess_abc","head_commit":"1111222233334444555566667777888899990000"}}`

	case "swarm", "iteration":
		return `Iteration Swarm & Rapid Alternatives Specification:
- Iteration Swarms generate rapid parallel alternatives from one prompt.
- Mode: mode="swarm" (count: 1-256)
- agent_type: "coder" | "designer" | "image" | "video" | "idea"
- Configuration:
  - themes: Array of per-variant themes (length must equal count)
  - groups: Group specializations with name, count, instructions
  - iteration_controls: Focused boundary with preserve, change, exclude
  - output_contract: Deliverable contract for workers
  - output_requirements: Managed preset ("x_header", "landscape_video", "portrait_video") or width/height
  - animation_profile: "motion_ui" | "spatial_3d" | "vector_playback" | "final_render"
- Examples:
  - Idea Swarm: {"mode":"swarm","agent_type":"idea","count":20,"prompt":"What is the clearest name for this feature?"}
  - Image Swarm: {"mode":"swarm","agent_type":"image","count":3,"themes":["minimal","editorial","product"],"prompt":"Campaign images","output_requirements":{"width":1536,"height":1024}}
  - Video Swarm: {"mode":"swarm","agent_type":"video","count":4,"themes":["cinematic","particles","minimal","cyber"],"prompt":"Transformation sequence"}`

	default:
		return `Task Tool Delegation & Swarm Contract:

1. Operational Invariants:
   - Every task call requires a top-level non-empty prompt.
   - Coders implement code, author tests, and commit with Git tools. Coders do NOT get Bash or shell execution.
   - The parent runs tests, linters, and verification using shell/build tools.
   - Coder assignments require declared owned_scope and clean target worktrees.
   - Concurrent Coders must have non-overlapping owned scopes.

2. Regular Delegation Mode:
   - launches: Array of subagent launches for one parallel wave.
   - Launches require: subagent_type ("coder"|"finder"|"designer"), title (3 words), meta_prompt, deliverable, concurrency_reason, owned_scope.
   - committed_source: Optional object {task_call_id, child_session_id, head_commit} for Coder launches to redelegate from an exact prior committed child's commit. Must not be combined with recovery_source_digest or workspace_path.
   - Example:
     {"mode":"regular","prompt":"Refactor API and UI","launches":[{"subagent_type":"coder","title":"Backend API Work","meta_prompt":"Refactor endpoint handlers","deliverable":"Committed API","concurrency_reason":"Independent scope","owned_scope":["internal/api/**"],"dependency_evidence":"Ready"},{"subagent_type":"coder","title":"Frontend UI Work","meta_prompt":"Update settings UI","deliverable":"Committed UI","concurrency_reason":"Uses API contract","owned_scope":["web/src/**"],"dependency_evidence":"Ready"}]}

3. Staged Task Programs:
   - Call task action="help" topic="program" for complete multi-stage Task Program schema and examples.

4. Iteration Swarms (mode="swarm"):
   - Call task action="help" topic="swarm" for Iteration Swarm schema (code, design, image, video, idea).`
	}
}

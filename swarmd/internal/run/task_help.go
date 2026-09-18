package run

import "strings"

func taskHelpText(topic string) string {
	switch strings.ToLower(strings.TrimSpace(topic)) {
	case "program", "task_program":
		return `Task Program Specification:
- A Task Program implements multi-stage dependent delegated work.
- Schema:
  - id: Unique program id (e.g. "image-feature-v1")
  - stages: Array of execution stages:
    - id: Stage id (e.g. "foundation", "audit")
    - depends_on: Earlier stage IDs that must reach integration barrier first
    - dependency_evidence: Reason stage is ready or what prior state unlocks it
  - jobs: Array of job definitions:
    - id: Job id (e.g. "core-contract")
    - stage_id: ID of the stage this job belongs to
    - agent_type: "coder" | "finder" | "designer"
    - meta_prompt: Complete instructive assignment for the subagent
    - title: Concise cosmetic title (ideally 3 words)
    - deliverable: Specific output parent will verify
    - acceptance_criteria: Criteria for completion
    - owned_scope: Workspace-relative files or directories (e.g. ["swarmd/internal/api/**"])
    - workspace_path: Optional target workspace root
    - depends_on: Earlier job IDs whose handoffs are required
- Example:
  {"action":"start","prompt":"Implement feature","program":{"id":"feat-v1","stages":[{"id":"foundation","dependency_evidence":"Contract clear"}],"jobs":[{"id":"core","stage_id":"foundation","agent_type":"coder","title":"Core Work","meta_prompt":"Implement API","deliverable":"Committed code","owned_scope":["pkg/**"],"dependency_evidence":"Ready","acceptance_criteria":["Tests pass"]}]}}`

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
   - Example:
     {"mode":"regular","prompt":"Refactor API and UI","launches":[{"subagent_type":"coder","title":"Backend API Work","meta_prompt":"Refactor endpoint handlers","deliverable":"Committed API","concurrency_reason":"Independent scope","owned_scope":["internal/api/**"],"dependency_evidence":"Ready"},{"subagent_type":"coder","title":"Frontend UI Work","meta_prompt":"Update settings UI","deliverable":"Committed UI","concurrency_reason":"Uses API contract","owned_scope":["web/src/**"],"dependency_evidence":"Ready"}]}

3. Staged Task Programs:
   - Call task action="help" topic="program" for complete multi-stage Task Program schema and examples.

4. Iteration Swarms (mode="swarm"):
   - Call task action="help" topic="swarm" for Iteration Swarm schema (code, design, image, video, idea).`
	}
}

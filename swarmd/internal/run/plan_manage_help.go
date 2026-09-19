package run

import "swarm/packages/swarmd/internal/tool"

func planManageHelpText() string {
	return `Plan Manage Lifecycle & Document Specification:

1. Canonical SessionPlanDocument Schema:
   - title: Plan title (string)
   - info:
     - goal: High-level plan goal (string, required)
     - scope: Project scope boundary (string)
     - context: Contextual narrative (string)
     - validation_strategy: High-level test/validation approach (string)
     - decisions: Architectural decisions made ([]string)
     - constraints: Hard technical constraints ([]string)
     - assumptions: Operational assumptions ([]string)
     - open_questions: Unresolved questions ([]string)
     - relevant_files: Key source file paths ([]string)
     - success_criteria: Verifiable criteria for total success ([]string)
   - checkpoints: Ordered array of checkpoints:
     - id: Stable checkpoint id, e.g. "cp-1" (string, required)
     - title: Checkpoint title (string, required)
     - order: 1-based sequence number (int, required)
     - status: "pending" | "in_progress" | "completed" | "needs_review" | "blocked" | "failed" (string, required)
     - objective: Checkpoint objective (string, required if tasks omitted)
     - tasks: Specific checklist items ([]string, required if objective omitted)
     - acceptance_criteria: Completion checks ([]string, required)
     - notes: Context, constraints, relevant files, validation expectations (string)
     - task_program: Optional staged Task Program (see task action="help")
     - artifacts: Optional workspace-relative artifacts

2. Feedback Intent Routing Table:
   | Feedback Intent | Action | Description |
   | :--- | :--- | :--- |
   | Inquiry or guidance only | (none) | Answer without mutating the plan when no deliverable change requested |
   | Localized additive patch | add_subtask | Add subtask to current checkpoint: {"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"title":"..."}} |
   | Checklist replacement | replace_subtasks | Replace subtasks with complete authoritative list, preserving attempt history |
   | Checkpoint redefinition | restart_checkpoint | Redefine checkpoint with replacement title, tasks, acceptance_criteria, notes |
   | Independent shippable work | transition_checkpoint_boundary | Trusted parent turn appends self-contained checkpoint and continues current run |
   | Whole-plan replacement | request_new_plan | Submit complete replacement document with title, info.goal, and checkpoints |

3. Worker V2 Schedule Specification:
` + tool.WorkerV2AuthoringInstructions + `

4. Canonical JSON Examples:
   - Start session checkpoint:
     {"action":"start_session_checkpoint","change_request":"<verbatim request>","checkpoint_title":"Title","tasks":["Task 1"],"acceptance_criteria":["Done"],"notes":"Context"}
   - Complete checkpoint:
     {"action":"complete_checkpoint","checkpoint_id":"cp-1","report":"Report","changed_files":["file.go"],"validation":["passed"],"result":"done","closing_state":"routine_clean","summary":"Clean completion","recommendation":{"decision":"ship","action":"review","reason":"Criteria met","action_state":"ready"}}
   - Request new plan (multi-checkpoint with task_program):
     {"action":"request_new_plan","title":"CI/CD Pipeline Overhaul","document":{"title":"CI/CD Pipeline Overhaul","info":{"goal":"Modernize and harden CI/CD pipeline"},"checkpoints":[{"id":"cp-1","title":"Audit workflows","status":"pending","order":1,"tasks":["Audit CI configs"],"acceptance_criteria":["Audit complete"],"task_program":{"id":"ci-audit","stages":[{"id":"audit","dependency_evidence":"Ready initially"}],"jobs":[{"id":"inspect","stage_id":"audit","agent_type":"finder","title":"Inspect CI","meta_prompt":"Audit workflows","deliverable":"Report","acceptance_criteria":["Audited"],"dependency_evidence":"Ready initially"}]}},{"id":"cp-2","title":"Implement pipeline","status":"pending","order":2,"tasks":["Write workflow definitions"],"acceptance_criteria":["CI passes"]}]}}
   - Amend plan:
     {"action":"amend_plan","base_revision":1,"update_summary":"What changed","replace_from_checkpoint_id":"cp-2","document":{"info":{"goal":"Updated"},"checkpoints":[{"id":"cp-1","title":"Done","status":"completed"},{"id":"cp-2","title":"Revised","status":"pending"}]}}
   - Add subtask:
     {"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"title":"Measure capacity"}}
   - Restart checkpoint:
     {"action":"restart_checkpoint","checkpoint_id":"cp-1","change_request":"<new request>","checkpoint_title":"New title","tasks":["New task"],"acceptance_criteria":["New criteria"],"notes":"New notes"}
   - Exit plan mode:
     {"title":"Plan Title","document":{"title":"Plan Title","info":{"goal":"Goal"},"checkpoints":[{"id":"cp-1","title":"Step 1","status":"pending","order":1,"tasks":["Task"],"acceptance_criteria":["Done"]}]}}`
}

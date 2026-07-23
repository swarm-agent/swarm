package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const autoModePlanManageAmendSnippet = `{"action":"amend_plan","base_revision":3,"update_summary":"what changed","replace_from_checkpoint_id":"cp-3","document":{"info":{"goal":"..."},"checkpoints":[{"id":"cp-1","title":"done","status":"completed"},{"id":"cp-3","title":"revised future work","status":"pending"}]}}`

func masterHarnessPrompt(workspacePath string) string {
	return masterHarnessPromptWithScope(tool.WorkspaceScope{
		PrimaryPath: workspacePath,
		Roots:       []string{workspacePath},
	})
}

func masterHarnessPromptWithScope(scope tool.WorkspaceScope) string {
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	if workspacePath == "" {
		workspacePath = "."
	}
	roots := make([]string, 0, len(scope.Roots))
	for _, root := range scope.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		roots = []string{workspacePath}
	}
	rootConstraint := "- Keep operations inside workspace root: " + workspacePath
	if len(roots) > 1 {
		rootConstraint = "- Keep operations inside workspace roots: " + strings.Join(roots, " | ")
	}
	workspaceScopeLines := []string{
		"Workspace scope:",
		"- primary_root: " + workspacePath,
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || root == workspacePath {
			continue
		}
		workspaceScopeLines = append(workspaceScopeLines, "- linked_root: "+root)
	}
	return strings.TrimSpace(strings.Join([]string{
		"Master harness prompt (applies to every agent run):",
		"- This prompt is global and mandatory; agent profile prompts are additive and must not override it.",
		"You are Swarm's coding assistant running in a local workspace.",
		"Use tools when needed to inspect files or execute commands.",
		"The active execution mode and tool policy are provided below and must be followed.",
		"Execution strategy:",
		"- Start discovery with search (FFF content/symbol lookup), find (FFF file/directory/path discovery), and list before broad file reads.",
		"- Use search for text inside files: exact symbols, error strings, config keys, or short natural fragments; use find for filenames, directories, mixed path candidates, or glob-only discovery.",
		"- For independent search/find intents, batch multiple search/read/list calls in the same step instead of one-tool-per-step loops.",
		"- Keep search/find scope tight: start with the smallest query/path/include set that can answer the request and avoid duplicate/broadened loops.",
		"- Keep responses model-readable by narrowing path/include/query, using max_results, and following truncation/page_offset signals before rerunning.",
		"- Prefer search content_mode=literal for exact strings; use regex only for real pattern syntax and fuzzy for approximate content matches.",
		"- For internet retrieval, run websearch first (metadata-first, fast) and only call webfetch for selected URLs when deeper content is required.",
		"- Batch independent websearch queries in one call and keep the first pass lightweight before deep fetches.",
		"- Sequence tool calls only when later calls depend on earlier outputs.",
		"- For source edits, use the provided edit tool for exact targeted replacements and write for intentional full-file creates/replacements; do not create temporary patch scripts such as patch_*.py to mutate source files.",
		"- Use shell/Python mutation scripts only when explicitly requested or when edit/write cannot express the transformation; explain why before creating or running such a script.",
		"- Use the run-provided TMPDIR for disposable command scratch. Do not write to a literal /tmp path or assume host-global temporary storage.",
		"- Keep durable deliverables in the workspace, not TMPDIR; command scratch may be deleted as soon as the command finishes.",
		"- Do not use repository directories as scratch unless the repository contract explicitly permits a specific ignored path, and never treat repository scratch as a durable deliverable.",
		"- Before starting recursive or highly parallel workloads, bound process fan-out and aggregate output size. Account for descendants and generated files, not only the top-level command.",
		"- Avoid commands that recursively emit unbounded stdout/stderr or files; narrow the scope, cap concurrency/output, and preserve only the requested workspace artifacts.",
		"- For multiple independent user-owned manage_todos operations, prefer a single atomic `batch` action with an `operations` array when they should succeed or fail together.",
		"- Use reorder only when relative list order matters; otherwise prefer independent create/update/delete/focus calls so parallel execution remains available.",
		"- For read, it is safe to request up to 2000 lines per call; read as many lines/chunks as needed to gain full context.",
		"- Before delegating, do a quick first pass with search/read/list to gather enough context to write strong subagent prompts.",
		"- Treat that first pass as preparation for delegation on larger tasks, not as a reason to keep all exploration local.",
		"- Use search hits to choose high-value read/list follow-up targets immediately.",
		"- Do not default to full-repository sweeps for routine tasks; start with user-provided paths/symbols/errors and nearby call paths.",
		"- Match effort to request scope: for narrow, explicit asks (for example a single-file change or a simple commit task), execute directly with minimal tooling.",
		"- Keep cohesive work direct. For example, fix one sidebar yourself unless inspection reveals genuinely independent owned scopes; delegation is available but is never a goal.",
		"- When a user asks to create, make, start, or open a new session without explicitly asking for subagents or naming an agent or agents, use manage-sessions deploy; do not use the task tool. Treat `session` as a durable user session by default.",
		"- For manage-sessions deploy proposals, suggest a short worktree_name and leave managed worktree isolation enabled by default. Do not ask the user to invent or type a branch name; the approval UI lets them disable worktree isolation. Set worktree=false only when the user explicitly requests the current workspace/no branch.",
		"- Use the task tool when the user explicitly asks to use subagents, asks for an Explorer or Coder, or names the agent or agents to run. Otherwise do not reinterpret a new-session request as delegation.",
		"- Use Explorer only for a distinct research question with a specific evidence-based deliverable. Use Coder for dependency-ready implementation scopes; declared owned scopes guide planning, review, and collision warnings rather than forbidding intentional overlap.",
		"- Coder launch requires a clean committed parent worktree. Each Coder starts from the parent's exact current Git HEAD on a unique child branch in a sibling worktree; children in one wave share that immutable base commit but never share a writable branch or worktree.",
		"- Do not delegate the parent's entire task to Coder or run dependent assignments concurrently. Intentional overlapping Coder scopes are allowed for split refactors, but the parent must record the overlap and resolve integration conflicts explicitly. The current backend orchestration policy defines delegation limits; available budget is never a target.",
		"- Each implementation Coder must commit its own completed changes on its allocated branch and finish with a clean worktree. Failed or stopped dirty children remain recallable as dirty-recoverable work but are not successful handoffs and must never be auto-committed.",
		"- The parent retains its own work, blocks while the current launch wave runs, and preserves every child handoff for recall, including child session, immutable base commit, parent branch, child branch, worktree, and head commit.",
		"- After children finish, recall and inspect every child diff/commit. Then call manage_worktree integrate exactly once with only the complete selected committed child session_ids. Integration is automatic: the tool derives lineage and parent HEAD, preflights the full ordered stack, applies atomically, and leaves the parent unchanged with an actionable conflict result if any commit conflicts. Do not pass or reconstruct integration manifests.",
		"- After integration, recall again to verify integrated child states and the resulting parent HEAD. Keep the parent clean before completing the checkpoint; any later Coder/checkpoint allocation resolves from that integrated parent HEAD boundary.",
		"- When one user request has multiple dependency-ready children, batch the exact current wave into one task call using `launches`; each launch must state its deliverable, concurrency reason, owned scope, and dependency evidence.",
		"- After delegated work, synthesize findings into one concrete update.",
		"- In that synthesis, include key findings, likely attack points, and a final Relevant filepaths list.",
		"- Stop discovery once you can name likely files/functions and the next concrete action.",
		"- For multi-step implementation work, keep durable task state current: use plan_manage complete_subtask at a genuine boundary for one task, or batch all tasks completed since the last update with subtask_ids. If the checkpoint is now fully done, the same call may set complete_checkpoint=true and carry the terminal outcome evidence.",
		"- Preserve manage_todos as the user-owned workspace todo surface. Do not use manage_todos for agent self-tracking or checkpoint lifecycle state.",
		"- Put final checkpoint notes, reports, changed files, and validation evidence on the terminal checkpoint action rather than making a separate routine progress update. Terminal report metadata is internal execution evidence; it never substitutes for the requested user-visible output. Include or link the actual requested artifact in the assistant response before the terminal lifecycle call.",
		"- Keep plan_manage as the single canonical checkpoint lifecycle surface. In checkpoints with multiple typed subtasks, record genuine mid-checkpoint progress with complete_subtask, batching subtask_ids when several tasks finished before the next update. When all work and acceptance criteria are done, finish directly with complete_checkpoint or combine the final task transitions and checkpoint completion via complete_subtask complete_checkpoint=true; do not waste a second tool call.",
		"- In auto mode with no active plan, if the user asks for a clear bounded implementation task and did not ask for a full plan, use plan_manage action=start_session_checkpoint instead of presenting a plan. Session mode=auto is not evidence that a plan exists: when active_plan_present=false, never call request_followup_checkpoint. start_session_checkpoint is the one atomic create-and-start operation for this state; do not call start_checkpoint afterward. The checkpoint must be a self-contained handoff for the current run: put the full verbatim original user request in change_request, set a concrete checkpoint_title, and include tasks, acceptance_criteria, and notes for scope, constraints, delegation hints, relevant files, and validation expectations. This creates and starts a durable one-checkpoint active plan and preserves normal completed/needs_review/blocked/failed terminal lifecycle states; an explicit user pause/stop is recorded separately as paused and remains restartable. If the original request explicitly requires a later AI, fresh context, a second checkpoint, or another independently executed stage, treat it as multi-checkpoint work even when each stage is small: submit exactly one approval-gated plan with plan_manage action=request_new_plan and include every known checkpoint up front. A checkpoint run cannot append another checkpoint to itself. Also use request_new_plan when the work is broad, uncertain, high-risk, or multi-phase; do not use plan_manage new/save to create an activated draft/pending plan shell.",
		"- When user feedback arrives for a stopped/paused active checkpoint, classify it before choosing a lifecycle action. If it corrects or changes requirements for the same deliverable, call restart_checkpoint with the full verbatim current request in change_request and provide a complete replacement checkpoint_title, tasks, acceptance_criteria, and notes; this atomically replaces the stale checkpoint definition before fresh-context restart. Never restart the unchanged checkpoint after requirement-changing feedback. If the user redirects to an independent deliverable or separate task, do not restart or carry forward the old objective; use request_followup_checkpoint so the current request becomes a new ordered checkpoint with its own self-contained objective. Ask only when the distinction is a real product ambiguity that cannot be resolved from the request.",
		"- If user feedback asks for another ordered auto-mode checkpoint on an active, approved, running, blocked, or final-review plan, use plan_manage action=request_followup_checkpoint with the full verbatim current user request in change_request. This action is valid only from the parent conversation, never from a provider-managed checkpoint run; a plan being in running state does not override that boundary. Inside a checkpoint run, do not call or retry request_followup_checkpoint: the backend rejects it. Continue and complete all resolvable work within the current checkpoint. If a genuinely independent later unit is still required, state clearly in the assistant response and terminal next-action evidence that it was not created and must be appended by a later parent-conversation turn; never use update_checkpoint or an artifact attachment as a substitute, and never claim the checkpoint was added after a failed tool result. On a blocked plan, call request_followup_checkpoint directly from the parent conversation; it atomically resolves the blocked checkpoint as superseded, inserts the new checkpoint after it, and continues according to the follow-up policy, so do not call resolve_blocked_checkpoint first. Failed checkpoints remain stopped and are not cleared this way. Treat this as adding a session checkpoint to the active session chain for ordering; it does not imply the new checkpoint is semantically a follow-up or part of one related thread. The checkpoint must be a self-contained handoff for a fresh run: preserve material context instead of compressing it, set a concrete checkpoint_title when useful, and include tasks, acceptance_criteria, and notes for scope, constraints, delegation hints, relevant files, and validation expectations. Use request_plan_revision for larger plan rewrites and request_new_plan for a separate replacement direction.",
		"- Terminal checkpoint actions only finish the current checkpoint; do not use complete_checkpoint to encode new user feedback or to re-complete a plan already waiting for final review.",
		"- In automatic execution, keep solving acceptance gaps that are resolvable with the available tools. Discovering more work, scope growth, a missing interface/API or implementation, uncertainty, or an incomplete/failed first approach is not by itself a reason to stop; adapt the implementation safely and continue.",
		"- Use mark_needs_review only when user or audit judgment is inherently required, mark_blocked only for a named external dependency/input/unavailable permission that cannot be obtained or worked around, and mark_failed only for a nonrecoverable execution error. Complete only when the checkpoint acceptance criteria are met.",
		"- Before completing a review, make any known safe correction. Then emit exactly one recommendation in the terminal plan_manage payload: decision ship/change/revert/defer, action, short reason, and action_state taken/ready/needs_approval. Do not present a menu of Git actions. Never run commits, cherry-picks, reverts, resets, or other risky Git actions without their separate permission; report denied or unavailable actions honestly.",
		"- Searching, reading, and codebase discovery do not count as completed task progress by themselves; call complete_subtask only after the corresponding task's concrete implementation or verification work is actually complete.",
		"- If the checkpoint is a single concrete task, skip intermediate progress churn and use the terminal checkpoint action when done.",
		"- If a branch of investigation is not required to complete the user request, stop and list it as optional follow-up instead of exploring it now.",
		"- If the user explicitly instructs you to change settings, make the settings change directly via the appropriate settings/config tool or file path instead of only suggesting it.",
		"- If the user is only making a suggestion or preference statement rather than an explicit change request, do not silently mutate settings; either note the suggestion as follow-up guidance or redirect them to the relevant settings surface.",
		"- When you provide long commands, config snippets, file contents, or any text the user is likely to copy, wrap that exact payload in <copy>...</copy> tags. Use an optional label attribute like <copy label=\"restart command\">...</copy> when it helps the UI preview.",
		"- Keep copy-tag payloads exact and free of explanatory prose; put context before or after the tagged block. Multiple <copy> blocks are allowed in one response.",
		"- Every Bash tool call must include explanation as an ordered list, category as exactly read, write, update, or delete, and critical as an explicit boolean pay-attention flag. For routine commands, explanation should normally contain one direct, human-scannable sentence that states the purpose and meaningful effect; do not narrate obvious shell mechanics, stdout/stderr capture, exit-status reporting, the working directory, lack of source edits, or generic build-artifact behavior unless one is materially relevant.",
		"- Expand Bash explanation into multiple concise items only when the command has several distinct material effects that cannot be understood clearly in one line. Name concrete files or directories changed, processes started, listeners and ports opened, public network exposure, privileges used, destructive actions, security-sensitive changes, and other consequential environmental effects whenever present; never hide these behind vague summaries.",
		"- Set Bash critical=true whenever the user should pay special attention before execution. Critical reads are exceptional: secrets or credentials, production databases, private customer data, protected system files, large or expensive queries, and reads coupled to outbound exfiltration. Public listeners, destructive or privileged operations, security-sensitive changes, and other unusually consequential effects are also critical. Bash category meanings: read only observes state; write creates new state, resources, or processes; update is a non-removal in-place mutation and never means removal; delete removes state and always requires critical=true. Routine source reads, listings, searches, status checks, and ordinary local logs are noncritical. For mixed commands, use the highest-impact category. Never omit or silently guess Bash metadata.",
		"- An executable plan without an explicit complete structured document is invalid and will be rejected. Every approval-bearing plan must include a title, info.goal, and at least one ordered pending checkpoint with id, title, objective or concrete tasks, and non-empty acceptance_criteria. Markdown/prose is display-only and never substitutes for checkpoints; the backend revalidates the exact approved document at every persistence, mode-switch, and execution boundary.",
		"- In plan mode, once the plan is actionable, submit it with exit_plan_mode for approval so the session can leave plan mode and continue execution; include the final complete structured document (info and checkpoints) directly in that exit_plan_mode call instead of doing a separate last-minute plan_manage save first.",
		fmt.Sprintf("- In auto mode, never call exit_plan_mode. To amend an active approved/running plan, use plan_manage amend_plan with base_revision and future-checkpoint scope, for example: %s", autoModePlanManageAmendSnippet),
		"Harness tool usage examples:",
		"- search (content/symbol lookup): {\"query\":\"modeCapabilityInstructions\",\"path\":\"swarmd/internal/run\",\"include\":\"*.go\",\"content_mode\":\"literal\"}",
		"- search (multi-query content lookup): {\"queries\":[\"modeCapabilityInstructions\",\"exit_plan_mode\"],\"path\":\"swarmd/internal/run\",\"include\":\"*.go\"}",
		"- search (regex with pagination/context): {\"query\":\"func .*Prompt\",\"path\":\"swarmd/internal/run\",\"include\":\"*.go\",\"content_mode\":\"regex\",\"before_context\":1,\"after_context\":2,\"file_offset\":0}",
		"- find (path discovery): {\"query\":\"service prompt\",\"mode\":\"files\",\"path\":\"swarmd/internal/run\",\"include\":\"*.go\"}",
		"- find (directory/mixed discovery): {\"query\":\"runtime\",\"mode\":\"mixed\",\"path\":\"swarmd/internal\",\"max_results\":20}",
		"- websearch (parallel Exa search): {\"queries\":[\"latest exa api pricing\",\"exa search endpoint\"],\"num_results\":5,\"search_type\":\"instant\"}",
		"- webfetch (Exa contents for selected URLs): {\"urls\":[\"https://docs.exa.ai/reference/search\"],\"text\":{\"max_characters\":1200},\"summary\":{\"query\":\"Key points\"}}",
		"- If search/find returns truncated=true, narrow path/include/query first; for search content pagination use next_file_offset as file_offset, and for find use page_index.",
		"- task (explorer delegation): {\"description\":\"Map plan mode state transition flow\",\"subagent_type\":\"explorer\",\"prompt\":\"Inspect run/plan flow. Return architecture summary, attack points, and relevant filepaths with evidence.\"}",
		"- task (batched dependency-ready scopes): {\"description\":\"Implement backend and frontend changes from one parent HEAD\",\"prompt\":\"Complete only the declared scopes and commit each child result.\",\"launches\":[{\"subagent_type\":\"coder\",\"meta_prompt\":\"Implement backend API\",\"deliverable\":\"Scoped committed API implementation\",\"concurrency_reason\":\"No dependency on UI work\",\"owned_scope\":[\"swarmd/internal/api/**\"],\"dependency_evidence\":\"API contract already finalized\"},{\"subagent_type\":\"coder\",\"meta_prompt\":\"Implement settings UI\",\"deliverable\":\"Scoped committed settings implementation\",\"concurrency_reason\":\"Uses finalized API contract\",\"owned_scope\":[\"web/src/features/desktop/settings/**\"],\"dependency_evidence\":\"No unfinished child output required\"}]}",
		"- manage_todos (user todo batch only): use {\"action\":\"batch\",\"owner_kind\":\"user\",\"operations\":[{...},{...},{...}]} when the user asks to mutate their workspace todo list atomically.",
		"- manage_todos (user todo reorder only): use {\"action\":\"reorder\",\"owner_kind\":\"user\",\"ordered_ids\":[\"todo_3\",\"todo_1\",\"todo_2\"]} only when the user asks to reorder their todo list.",
		"- Do not use manage_todos for agent execution checklists or checkpoint progress; use plan_manage complete_subtask for genuine typed-task boundaries, including batched subtask_ids and optional atomic checkpoint completion. Use update_checkpoint only for meaningful intermediate state, not routine task completion notes.",
		"- plan_manage terminal checkpoint example: {\"action\":\"complete_checkpoint\",\"checkpoint_id\":\"cp-1\",\"report\":\"Implemented requested change\",\"changed_files\":[\"path/to/file\"],\"validation\":[\"not run; not requested\"],\"result\":\"done\"}",
		"- plan_manage no-active-plan session checkpoint example: {\"action\":\"start_session_checkpoint\",\"change_request\":\"full verbatim original user request\",\"checkpoint_title\":\"Concrete handoff title\",\"tasks\":[\"Concrete task\"],\"acceptance_criteria\":[\"Completion check\"],\"notes\":\"Scope, constraints, relevant files, validation expectations\"}; use this as the only checkpoint-creation call in auto mode for straightforward bounded tasks when active_plan_present=false. It atomically creates and starts the checkpoint in the current run; never precede it with request_followup_checkpoint or follow it with start_checkpoint.",
		"- SessionPlanDocument info field types are strict in the canonical schema: goal, scope, context, and validation_strategy are strings; decisions, relevant_files, constraints, assumptions, open_questions, and success_criteria are arrays of strings. Never send scope as an array or decisions as a string.",
		"- plan_manage no-active-plan broad plan proposal example: {\"action\":\"request_new_plan\",\"title\":\"Plan: feature work\",\"document\":{\"title\":\"Plan: feature work\",\"info\":{\"goal\":\"Feature work\",\"scope\":\"Implement the requested feature\",\"decisions\":[\"Use the canonical path\"]},\"checkpoints\":[{\"id\":\"cp-1\",\"title\":\"First step\",\"status\":\"pending\",\"order\":1,\"tasks\":[\"Implement the feature\"],\"acceptance_criteria\":[\"The requested feature works\"]}]}}; use this as the single approval-request path for broad auto-mode work with no active plan. User approval applies the plan as approved and returns the fresh-context start path; do not create a draft with action=new/save first.",
		"- plan_manage requirement-changing restart example: {\"action\":\"restart_checkpoint\",\"checkpoint_id\":\"cp-1\",\"change_request\":\"full verbatim redirected request for the same deliverable\",\"checkpoint_title\":\"Replacement handoff title\",\"tasks\":[\"Complete replacement task\"],\"acceptance_criteria\":[\"Replacement requirement is satisfied\"],\"notes\":\"Complete replacement context and validation expectations\"}; use a plain restart without change_request only for a true retry with unchanged requirements. For an independent redirected task, use request_followup_checkpoint instead.",
		"- plan_manage active-plan session checkpoint example: {\"action\":\"request_followup_checkpoint\",\"change_request\":\"full verbatim current user request\",\"checkpoint_title\":\"Concrete handoff title\",\"tasks\":[\"Concrete task\"],\"acceptance_criteria\":[\"Completion check\"],\"notes\":\"Scope, constraints, relevant files, validation expectations\"}; despite the legacy action name, this appends one ordered checkpoint to the active session chain and directly recovers a blocked plan without a prior resolve_blocked_checkpoint call. Use request_plan_revision for broader approved-plan rewrites and request_new_plan for a separate new plan proposal.",
		fmt.Sprintf("- plan_manage active-plan amendment example: %s", autoModePlanManageAmendSnippet),
		"- plan_manage modular document patches: update_info and update_checkpoint merge only provided fields and preserve omitted fields; use replace_info/set_info or replace_checkpoint/set_checkpoint only when intentionally replacing a whole object.",
		"- exit_plan_mode (submit final structured plan document for approval and exit plan mode; include plan_id when reusing an active plan): {\"title\":\"Plan: tighten harness prompt\",\"plan_id\":\"plan_123\",\"document\":{\"title\":\"Plan: tighten harness prompt\",\"info\":{\"goal\":\"Tighten harness prompt\",\"relevant_files\":[\"swarmd/internal/run/service.go\"]},\"checkpoints\":[{\"id\":\"cp-1\",\"title\":\"Update prompt\",\"status\":\"pending\",\"order\":1,\"tasks\":[\"Update master prompt\"],\"acceptance_criteria\":[\"Prompt matches the executable-plan contract\"]}]}}",
		strings.Join(workspaceScopeLines, "\n"),
		"Tool constraints:",
		rootConstraint,
		"- If the user explicitly asks about a path outside the current workspace scope, call the relevant path-based tool on that exact path anyway. The backend can request workspace access approval; user approval grants temporary access for this chat session unless they explicitly choose the separate persistent add-dir option. Do not refuse solely because the path is outside the current scope.",
		"- For bash, avoid destructive commands unless explicitly requested.",
		"Respond with concrete, concise results.",
	}, "\n"))
}

func defaultInstructions(workspacePath string) string {
	return masterHarnessPrompt(workspacePath)
}

func applyAgentPreferenceOverrides(base pebblestore.ModelPreference, agentProfile pebblestore.AgentProfile) pebblestore.ModelPreference {
	return applyAgentPreferenceOverridesForMode(base, agentProfile, sessionruntime.ModeAuto)
}

func applyAgentPreferenceOverridesForMode(base pebblestore.ModelPreference, agentProfile pebblestore.AgentProfile, mode string) pebblestore.ModelPreference {
	providerOverride := strings.ToLower(strings.TrimSpace(agentProfile.Provider))
	modelOverride := strings.TrimSpace(agentProfile.Model)
	thinkingOverride := normalizeThinkingLevel(agentProfile.Thinking)

	if pebblestore.AgentModelMode(agentProfile) == "split" && pebblestore.AgentSupportsSplitModel(agentProfile) {
		mode = sessionruntime.NormalizeMode(mode)
		if mode == sessionruntime.ModePlan {
			providerOverride = strings.ToLower(strings.TrimSpace(agentProfile.PlanProvider))
			modelOverride = strings.TrimSpace(agentProfile.PlanModel)
			thinkingOverride = normalizeThinkingLevel(agentProfile.PlanThinking)
			if serviceTierOverride := strings.TrimSpace(agentProfile.PlanServiceTier); serviceTierOverride != "" {
				base.ServiceTier = serviceTierOverride
			}
		} else if mode == sessionruntime.ModeAuto {
			providerOverride = strings.ToLower(strings.TrimSpace(agentProfile.AutoProvider))
			modelOverride = strings.TrimSpace(agentProfile.AutoModel)
			thinkingOverride = normalizeThinkingLevel(agentProfile.AutoThinking)
			if serviceTierOverride := strings.TrimSpace(agentProfile.AutoServiceTier); serviceTierOverride != "" {
				base.ServiceTier = serviceTierOverride
			}
		}
	} else if serviceTierOverride := strings.TrimSpace(agentProfile.AutoServiceTier); serviceTierOverride != "" {
		base.ServiceTier = serviceTierOverride
	}

	switch {
	case providerOverride != "" && modelOverride != "":
		base.Provider = providerOverride
		base.Model = modelOverride
	case providerOverride == "" && modelOverride != "":
		base.Model = modelOverride
	}
	if thinkingOverride != "" {
		base.Thinking = thinkingOverride
	}
	base.Thinking = normalizeThinkingWithProvider(base.Provider, base.Thinking)
	base.ServiceTier = modelruntime.NormalizeServiceTierForProvider(base.Provider, base.ServiceTier)
	if !strings.EqualFold(strings.TrimSpace(base.Provider), "codex") || !strings.EqualFold(strings.TrimSpace(base.Model), "gpt-5.4") {
		base.ContextMode = ""
	}
	return base
}

func normalizeThinkingWithProvider(providerID, thinking string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if normalized := normalizeThinkingLevel(thinking); normalized != "" {
		if (providerID == "copilot" || providerID == "fireworks" || providerID == "openrouter") && normalized == "xhigh" {
			return "high"
		}
		return normalized
	}
	switch providerID {
	case "google":
		return "xhigh"
	case "copilot":
		return "high"
	case "fireworks":
		return "high"
	case "openrouter":
		return "high"
	default:
		return pebblestore.DefaultThinkingLevel
	}
}

func normalizeThinkingLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off":
		return "off"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	case "ultra":
		return "ultra"
	default:
		return ""
	}
}

func (s *Service) resolveAgentProfileForAccount(accountScopeID, name, targetKind string) (pebblestore.AgentProfile, error) {
	targetKind = normalizeRunTargetKind(targetKind)
	switch targetKind {
	case "", RunTargetKindAgent:
		return s.resolveAgentForAccount(accountScopeID, name)
	case RunTargetKindSubagent, RunTargetKindBackground:
		if s.agents == nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("targeted agent %q cannot resolve without agent service", strings.TrimSpace(name))
		}
		if strings.TrimSpace(accountScopeID) != "" {
			return s.agents.ResolveAgentForAccount(accountScopeID, name)
		}
		return s.agents.ResolveAgent(name)
	default:
		return pebblestore.AgentProfile{}, fmt.Errorf("unsupported target_kind %q", strings.TrimSpace(targetKind))
	}
}

func (s *Service) resolveAgent(name string) (pebblestore.AgentProfile, error) {
	return s.resolveAgentForAccount("", name)
}

func (s *Service) resolveAgentForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	if s.agents != nil {
		if strings.TrimSpace(accountScopeID) != "" {
			return s.agents.ResolveAgentForAccount(accountScopeID, name)
		}
		return s.agents.ResolveAgent(name)
	}
	profile, ok := agentruntime.DefaultProfileByName("swarm")
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("default agent %q not found", "swarm")
	}
	profile.Description = "fallback primary agent"
	return profile, nil
}

func (s *Service) composeInstructions(workspacePath string, agentProfile pebblestore.AgentProfile, userInstructions string) string {
	scope, err := s.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{WorkspacePath: workspacePath}, identity.Principal{})
	if err != nil {
		scope = tool.WorkspaceScope{
			PrimaryPath: workspacePath,
			Roots:       []string{workspacePath},
		}
	}
	return s.composeInstructionsForScope(scope, agentProfile, userInstructions)
}

func (s *Service) ComposeRuntimeInstructions(scope tool.WorkspaceScope, mode string, bypassPermissions bool, agentProfile pebblestore.AgentProfile, userInstructions string) string {
	base := s.composeInstructionsForScope(scope, agentProfile, userInstructions)
	base = appendHostRuntimeContext(base, scope.PrimaryPath, scope.Roots)
	return composeModeAwareInstructions(base, mode, bypassPermissions, agentProfile)
}

func (s *Service) composeInstructionsForScope(scope tool.WorkspaceScope, agentProfile pebblestore.AgentProfile, userInstructions string) string {
	return s.composeInstructionsForScopeWithDiscoveryRoots(scope, scope.Roots, agentProfile, userInstructions)
}

func normalizeInstructionDiscoveryRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) composeInstructionsForScopeWithDiscoveryRoots(scope tool.WorkspaceScope, discoveryRoots []string, agentProfile pebblestore.AgentProfile, userInstructions string) string {
	blocks := make([]string, 0, 6)
	blocks = append(blocks, masterHarnessPromptWithScope(scope))
	if s.permissions != nil {
		if policy, err := s.permissions.CurrentPolicyForAccount(scope.Principal.AccountScopeID); err == nil {
			subagents := policy.Subagents
			blocks = append(blocks, strings.TrimSpace(strings.Join([]string{
				"Current backend orchestration policy (account-scoped):",
				"- mode: " + string(subagents.Mode),
				fmt.Sprintf("- automatic_launches_per_parent_run: %d (cumulative approval-free launch budget for this parent run; completed children still count, preventing repeated automatic spawning loops)", subagents.AutomaticLaunchesPerParentRun),
				fmt.Sprintf("- active_child_limit: %d (concurrent active-child ceiling for this parent run; completed children release this capacity)", subagents.ActiveChildLimit),
				"- over_budget_action: " + string(subagents.OverBudgetAction),
				fmt.Sprintf("- absolute_wave_maximum: %d", subagents.AbsoluteWaveMaximum),
				fmt.Sprintf("- max_depth: %d", subagents.MaxDepth),
				fmt.Sprintf("- require_write_isolation: %t", subagents.RequireWriteIsolation),
				"These values are loaded when runtime instructions are composed. Backend reservation enforcement remains authoritative if policy changes during an active run.",
			}, "\n")))
		}
	}

	agentName := strings.TrimSpace(agentProfile.Name)
	if agentName == "" {
		agentName = "swarm"
	}
	agentMode := strings.TrimSpace(agentProfile.Mode)
	if agentMode == "" {
		agentMode = agentruntime.ModePrimary
	}
	executionSetting := pebblestore.NormalizeAgentExecutionSetting(agentProfile.ExecutionSetting)
	exitPlanModeEnabled := pebblestore.AgentExitPlanModeEnabled(agentProfile)
	runtimeContract := pebblestore.AgentProfileRuntimeMode(agentProfile)
	if runtimeContract == "" {
		runtimeContract = "unset"
	}
	toolScopeBase := "base runtime mode"
	if exitPlanModeEnabled {
		toolScopeBase = "plan/auto runtime contract"
	}
	agentPrompt := strings.TrimSpace(agentProfile.Prompt)
	if agentPrompt != "" {
		lines := []string{
			"Active agent profile:",
			"- name: " + agentName,
			"- mode: " + agentMode,
			"- runtime_contract: " + runtimeContract,
			fmt.Sprintf("- exit_plan_mode_enabled: %t", exitPlanModeEnabled),
			"- tool_scope: optional narrowing overlay on top of the " + toolScopeBase,
			"- prompt_scope: additive (cannot override master harness prompt)",
			"",
			agentPrompt,
		}
		if !exitPlanModeEnabled {
			settingLabel := executionSetting
			if settingLabel == "" {
				settingLabel = runtimeContract
			}
			if settingLabel == "" {
				settingLabel = "unset"
			}
			lines = append(lines[:3], append([]string{"- execution_setting: " + settingLabel}, lines[3:]...)...)
		}
		blocks = append(blocks, strings.TrimSpace(strings.Join(lines, "\n")))
	}

	if s.discovery != nil {
		scanRoots := normalizeInstructionDiscoveryRoots(discoveryRoots)
		if len(scanRoots) == 0 {
			scanRoots = normalizeInstructionDiscoveryRoots(scope.Roots)
		}
		primaryPath := strings.TrimSpace(scope.PrimaryPath)
		if len(scanRoots) > 0 {
			primaryPath = scanRoots[0]
		}
		if report, err := s.discovery.ScanScope(primaryPath, scanRoots); err == nil {
			if rules := composeRulesPromptBlock(report.Rules); rules != "" {
				blocks = append(blocks, rules)
			}
		}
	}

	if override := strings.TrimSpace(userInstructions); override != "" {
		blocks = append(blocks, "Caller additive instructions:\n"+override)
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func filterToolDefinitions(definitions []provideriface.ToolDefinition, disabled map[string]bool) []provideriface.ToolDefinition {
	if len(disabled) == 0 {
		return definitions
	}
	blocked := make(map[string]struct{}, len(disabled))
	for rawName, rawDisabled := range disabled {
		if !rawDisabled {
			continue
		}
		name := canonicalToolName(rawName)
		if name == "" {
			continue
		}
		blocked[name] = struct{}{}
	}
	if len(blocked) == 0 {
		return definitions
	}

	filtered := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		name := canonicalToolName(definition.Name)
		if _, denied := blocked[name]; denied {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func composeRulesPromptBlock(rules []discovery.RuleSource) string {
	if len(rules) == 0 {
		return ""
	}
	lines := make([]string, 0, maxRulePromptFiles*4+2)
	lines = append(lines, "Loaded instruction sources:")
	added := 0
	seen := make(map[string]struct{}, maxRulePromptFiles)
	for _, rule := range rules {
		if added >= maxRulePromptFiles {
			break
		}
		path := strings.TrimSpace(rule.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = filepath.Base(path)
		}
		lines = append(lines, "- "+name+": "+path)
		if snippet := readPromptSnippet(path); snippet != "" {
			lines = append(lines, snippet)
		}
		added++
	}
	if added == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func readPromptSnippet(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func composeModeAwareInstructions(baseInstructions, mode string, bypassPermissions bool, agentProfile pebblestore.AgentProfile) string {
	base := strings.TrimSpace(baseInstructions)
	modeDetails := modeCapabilityInstructions(mode, bypassPermissions, agentProfile)
	if base == "" {
		return modeDetails
	}
	return strings.TrimSpace(base + "\n\n" + modeDetails)
}

func modeCapabilityInstructions(mode string, bypassPermissions bool, agentProfile pebblestore.AgentProfile) string {
	setting, hasExecutionSetting := pebblestore.AgentExecutionSetting(agentProfile)
	executionSetting := setting
	exitPlanModeEnabled := pebblestore.AgentExitPlanModeEnabled(agentProfile)
	runtimeMode := pebblestore.AgentProfileRuntimeMode(agentProfile)
	if !exitPlanModeEnabled && runtimeMode != "" && runtimeMode != pebblestore.AgentRuntimeModePlanAuto {
		executionSetting = runtimeMode
		hasExecutionSetting = true
	}
	if executionSetting == "" {
		executionSetting = "unset"
	}

	currentMode := strings.ToLower(strings.TrimSpace(mode))
	if exitPlanModeEnabled {
		currentMode = sessionruntime.NormalizeMode(currentMode)
	} else if hasExecutionSetting {
		currentMode = executionSetting
	} else if currentMode == "" {
		currentMode = "unset"
	}

	lines := make([]string, 0, 24)
	if exitPlanModeEnabled {
		lines = append(lines,
			"Current session mode: "+currentMode+".",
			"The current session mode above is authoritative for this turn and supersedes any earlier transcript text, tool output, or UI guidance that described a different mode.",
			"Session mode can be changed between turns; do not treat an earlier auto/plan state as permanent.",
			"Current agent runtime contract: plan_auto (exit_plan_mode transitions an approved plan turn to auto; it does not make auto mode irreversible).",
		)
	} else {
		lines = append(lines,
			"Current execution mode: "+currentMode+".",
			"The current execution mode above is authoritative for this turn and supersedes any earlier transcript text, tool output, or UI guidance that described a different mode.",
			"Execution mode is controlled by the saved agent runtime_mode because plan mode is disabled for this agent.",
			"Current agent runtime contract: "+executionSetting+".",
		)
	}
	lines = append(lines,
		fmt.Sprintf("Current agent exit-plan-mode enabled: %t.", exitPlanModeEnabled),
		"The tool list attached to this run is the authoritative resolved contract for this agent.",
		"Use ask-user only for true product/decision forks; do not use ask-user to request tool permissions.",
		"Tool capability policy (enforced by backend):",
	)
	switch executionSetting {
	case "unset":
		if exitPlanModeEnabled {
			lines = append(lines,
				"- tool availability is determined by plan mode until exit_plan_mode switches the session to auto.",
				"- read/readwrite runtime capability requests are overridden while plan mode is enabled.",
			)
		} else {
			lines = append(lines,
				"- no static runtime mode is configured for this agent.",
				"- with plan mode disabled, runs will fail until runtime_mode is set to read or readwrite.",
			)
		}
	case pebblestore.AgentExecutionSettingRead:
		lines = append(lines,
			"- read runtime mode provides the baseline non-mutating contract when plan mode is disabled.",
			"- the saved agent profile may still explicitly enable or disable tools beyond that baseline.",
			"- do not assume bash, write, or edit access unless those tools are present in the resolved tool list.",
		)
	case pebblestore.AgentExecutionSettingReadWrite:
		lines = append(lines,
			"- readwrite runtime mode provides the baseline mutable contract when plan mode is disabled.",
			"- the saved agent profile may still explicitly disable tools or add scoped tools beyond that baseline.",
			"- do not assume bash access unless bash is present in the resolved tool list.",
		)
	}
	if exitPlanModeEnabled {
		lines = append(lines,
			fmt.Sprintf("- exit_plan_mode is available for this agent, but still requires explicit approval and only succeeds from session plan mode. Never call it from auto; to revise an active approved/running plan in auto, use plan_manage amend_plan with base_revision and future-checkpoint scope, for example: %s", autoModePlanManageAmendSnippet),
			"- plan_manage is available in both plan and auto to inspect or update saved plans; it does not change session mode.",
		)
	} else {
		lines = append(lines,
			"- exit_plan_mode is unavailable for this agent and will be rejected by backend policy.",
		)
	}
	if currentMode == sessionruntime.ModePlan {
		lines = append(lines,
			"Plan-mode expectation: run targeted discovery, then draft/refine a concrete execution plan quickly.",
			"Do not keep scanning for unrelated edge cases once the plan is actionable.",
			"Do not create or churn agent checklist todos during plan-only discovery. If progress tracking is needed, keep it in plan_manage on the active plan/checkpoint.",
		)
		if exitPlanModeEnabled {
			lines = append(lines,
				"Keep refining the plan with plan_manage as needed while staying in plan mode. For the final step, call exit_plan_mode once with the final structured document (info/checkpoints) and active plan_id when available; do not do a redundant plan_manage save immediately before exit_plan_mode just to submit the same plan. After approval, execution continues in auto on the same active plan/checklist, and plan_manage can still update it.",
				"Because the current session mode is plan, you may call exit_plan_mode when the plan is actionable even if earlier transcript text says the session already exited plan mode or that exit_plan_mode cannot be called from auto.",
			)
		}
	} else {
		lines = append(lines,
			"Execution expectation: continue implementation; ask-user only for true product/decision forks.",
			"When an active plan exists and the work is checkpointed, complete the checkpoint with the appropriate terminal plan_manage action; do not use manage_todos for agent self-tracking.",
		)
		if currentMode == sessionruntime.ModeAuto && exitPlanModeEnabled {
			lines = append(lines,
				fmt.Sprintf("Use the injected durable run state's active_plan_present field as the authoritative plan-existence signal; do not call plan_manage get-active merely to probe for a plan. When that state says an active plan exists, continue its scoped lifecycle and use get-active only if full plan details are materially needed beyond the injected state. Use amend_plan/request_followup_checkpoint/request_plan_revision/request_new_plan as appropriate to revise active work without switching modes. Do not call exit_plan_mode from auto; it only applies when leaving plan mode. For active whole-plan amendments, use plan_manage amend_plan with base_revision and future-checkpoint scope, for example: %s", autoModePlanManageAmendSnippet),
			)
		}
		if !exitPlanModeEnabled && hasExecutionSetting {
			lines = append(lines,
				"With plan mode disabled, the backend uses runtime_mode as the effective runtime contract.",
			)
		}
	}
	if bypassPermissions {
		lines = append(lines,
			"Permission bypass is active: normal tool approval prompts are skipped.",
			"task still requires explicit approval before launching subagents, even when permission bypass is active.",
		)
		if exitPlanModeEnabled {
			lines = append(lines, "exit_plan_mode still requires explicit approval even when permission bypass is active.")
		}
	}
	lines = append(lines, "When approval is required, invoke the tool directly and let the permission system resolve it; never use ask-user for tool approvals.")
	return strings.Join(lines, "\n")
}

func buildInput(messages []pebblestore.MessageSnapshot) []map[string]any {
	input := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if isManualCompactionAcknowledgement(message) {
			continue
		}

		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "assistant":
			if assistantInput, ok := buildAssistantOutputInput(content); ok {
				input = append(input, assistantInput)
			}
		case "reasoning":
			// Reasoning summaries are for UI/debug visibility and should not
			// influence subsequent model turns.
			continue
		case "system":
			if isToolDBDebugMessage(content) {
				continue
			}
			if attachedPlanText := strings.TrimSpace(mapString(message.Metadata, contextCompactionPlanTextMetadataKey)); attachedPlanText != "" {
				content = strings.TrimSpace(content + "\n\nActive session plan (still in effect after compaction):\n\n" + attachedPlanText)
			}
			input = append(input, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "[system] " + content},
				},
			})
		case "tool":
			if historyInput, ok := buildToolHistoryInput(content); ok {
				input = append(input, historyInput...)
			}
		default:
			if shouldDropSensitiveConversationMessage(message) {
				continue
			}
			input = append(input, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": content},
				},
			})
		}
	}
	return input
}

func buildAssistantOutputInput(content string) (map[string]any, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false
	}
	return map[string]any{
		"role": "assistant",
		"content": []map[string]any{
			{"type": "output_text", "text": content},
		},
	}, true
}

func isManualCompactionAcknowledgement(message pebblestore.MessageSnapshot) bool {
	if strings.ToLower(strings.TrimSpace(message.Role)) != "assistant" {
		return false
	}
	if source := strings.ToLower(strings.TrimSpace(mapString(message.Metadata, "source"))); source == "manual_context_compaction_ack" {
		return true
	}
	content := strings.TrimSpace(message.Content)
	if content == "" || !strings.HasPrefix(content, "Manual context compact complete (Compact #") {
		return false
	}
	return !strings.Contains(content, "Compacted recap:")
}

func shouldDropSensitiveConversationMessage(message pebblestore.MessageSnapshot) bool {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role != "user" {
		return false
	}
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return false
	}
	metadata := message.Metadata
	if metadata == nil {
		return false
	}
	if source := strings.ToLower(strings.TrimSpace(mapString(metadata, "source"))); source == "command" {
		if strings.HasPrefix(content, "/auth ") {
			return true
		}
	}
	return false
}

func convertToolDefinitions(definitions []tool.Definition) []provideriface.ToolDefinition {
	out := make([]provideriface.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, provideriface.ToolDefinition{
			Type:        definition.Type,
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  normalizeProviderToolParameters(definition.Parameters),
		})
	}
	return out
}

func normalizeProviderToolParameters(parameters map[string]any) map[string]any {
	if len(parameters) == 0 {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	out := cloneToolSchemaMap(parameters)
	if strings.TrimSpace(mapString(out, "type")) == "" {
		out["type"] = "object"
	}
	if strings.EqualFold(strings.TrimSpace(mapString(out, "type")), "object") {
		if _, ok := out["properties"].(map[string]any); !ok {
			out["properties"] = map[string]any{}
		}
	}
	return out
}

func cloneToolSchemaMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if value == nil {
			continue
		}
		out[key] = cloneToolSchemaValue(value)
	}
	return out
}

func cloneToolSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneToolSchemaMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				out = append(out, cloneToolSchemaValue(item))
			}
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

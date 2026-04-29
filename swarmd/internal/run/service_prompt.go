package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const autoModePlanManageSaveSnippet = `{"action":"save","plan":"# Plan\n1. ..."}`

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
		"- Start discovery with search (scoped workspace content/file search via FFF) and list before broad file reads.",
		"- For independent search intents, batch multiple search/read/list calls in the same step instead of one-tool-per-step loops.",
		"- Keep search scope tight: start with the smallest query/pattern set that can answer the request and avoid duplicate/broadened search loops.",
		"- Keep search responses model-readable by narrowing path/include/pattern and max_results before rerunning.",
		"- For internet retrieval, run websearch first (metadata-first, fast) and only call webfetch for selected URLs when deeper content is required.",
		"- Batch independent websearch queries in one call and keep the first pass lightweight before deep fetches.",
		"- Sequence tool calls only when later calls depend on earlier outputs.",
		"- For multiple independent manage_todos operations, prefer a single atomic `batch` action with an `operations` array when they should succeed or fail together.",
		"- Use reorder only when relative list order matters; otherwise prefer independent create/update/delete/focus calls so parallel execution remains available.",
		"- For read, it is safe to request up to 2000 lines per call; read as many lines/chunks as needed to gain full context.",
		"- Before delegating, do a quick first pass with search/read/list to gather enough context to write strong subagent prompts.",
		"- Treat that first pass as preparation for delegation on larger tasks, not as a reason to keep all exploration local.",
		"- Use search hits to choose high-value read/list follow-up targets immediately.",
		"- Do not default to full-repository sweeps for routine tasks; start with user-provided paths/symbols/errors and nearby call paths.",
		"- Match effort to request scope: for narrow, explicit asks (for example a single-file change or a simple commit task), execute directly with minimal tooling.",
		"- After the first pass, delegate when the scope is broad, cross-cutting, unfamiliar, or split across multiple plausible areas.",
		"- For unfamiliar codebases or broad investigations, use task with subagent_type=explorer to map areas of interest, likely attack points, and candidate filepaths.",
		"- Use task to delegate focused work to subagents (explorer, memory, parallel, clone) when delegation improves latency or quality.",
		"- When one user request needs multiple subagents, batch them into a single task call using `launches` so the user gets one approval modal for the whole delegation.",
		"- Each launch should carry only the child type plus its assigned role/meta instruction; the shared parent prompt stays at the task level.",
		"- For broad investigations, split independent scopes and run multiple explorer delegations in parallel when possible so different agents can go deep in different areas.",
		"- If one quick read/search/list confirms the needed change, continue directly; otherwise prefer delegation over doing all multi-branch exploration yourself.",
		"- After delegated or parallel work, synthesize findings into one concrete update.",
		"- In that synthesis, include key findings, likely attack points, and a final Relevant filepaths list.",
		"- Stop discovery once you can name likely files/functions and the next concrete action.",
		"- For multi-step implementation work, keep two levels of tracking distinct: `plan_manage` is the high-level plan, while `manage_todos` with `owner_kind=agent` is the execution checklist.",
		"- Preserve user todos as user-owned work. Do not mix user todos with the agent checklist unless the user explicitly asks for that.",
		"- Create or update agent checklist items when the task spans multiple concrete implementation steps, and keep the checklist current as execution advances.",
		"- Use `owner_kind=agent` for checklist entries and `group` only for phases or milestones, not for separating user vs agent work.",
		"- Keep at most one agent todo in progress at a time. When starting the next step, clear the previous in-progress agent item in the same update or batch.",
		"- Searching, reading, and codebase discovery do not count as checklist progress by themselves; only mark progress when a concrete implementation step starts or completes.",
		"- If the task is short and can be completed in one concrete step, skip checklist churn; otherwise keep the persistent agent checklist updated.",
		"- If a branch of investigation is not required to complete the user request, stop and list it as optional follow-up instead of exploring it now.",
		"- If the user explicitly instructs you to change settings, make the settings change directly via the appropriate settings/config tool or file path instead of only suggesting it.",
		"- If the user is only making a suggestion or preference statement rather than an explicit change request, do not silently mutate settings; either note the suggestion as follow-up guidance or redirect them to the relevant settings surface.",
		"- When you provide long commands, config snippets, file contents, or any text the user is likely to copy, wrap that exact payload in <copy>...</copy> tags. Use an optional label attribute like <copy label=\"restart command\">...</copy> when it helps the UI preview.",
		"- Keep copy-tag payloads exact and free of explanatory prose; put context before or after the tagged block. Multiple <copy> blocks are allowed in one response.",
		"- In plan mode, once the plan is actionable, submit it with exit_plan_mode for approval so the session can leave plan mode and continue execution; do not continue irrelevant exploration.",
		fmt.Sprintf("- In auto mode, never call exit_plan_mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
		"Harness tool usage examples:",
		"- search (single scope): {\"query\":\"modeCapabilityInstructions\",\"path\":\"swarmd/internal/run\",\"include\":\"*.go\"}",
		"- search (multi-query in one call): {\"queries\":[\"modeCapabilityInstructions\",\"exit_plan_mode\"],\"path\":\"swarmd/internal/run\",\"include\":\"*.go\"}",
		"- search (parallel-friendly batch within one scope): {\"queries\":[\"buildTaskDelegationPrompt\",\"modeCapabilityInstructions\",\"exit_plan_mode\"],\"path\":\"swarmd/internal/run\",\"include\":\"*.go\"}",
		"- websearch (parallel Exa search): {\"queries\":[\"latest exa api pricing\",\"exa search endpoint\"],\"num_results\":5,\"search_type\":\"instant\"}",
		"- webfetch (Exa contents for selected URLs): {\"urls\":[\"https://docs.exa.ai/reference/search\"],\"text\":{\"max_characters\":1200},\"summary\":{\"query\":\"Key points\"}}",
		"- If search returns truncated=true, narrow path/include/pattern and rerun instead of broadening search scope.",
		"- task (explorer delegation): {\"description\":\"Map plan mode state transition flow\",\"subagent_type\":\"explorer\",\"prompt\":\"Inspect run/plan flow. Return architecture summary, attack points, and relevant filepaths with evidence.\"}",
		"- task (batched subagents): {\"description\":\"Write poem variants\",\"prompt\":\"Write a poem about the sea.\",\"launches\":[{\"subagent_type\":\"parallel\",\"meta_prompt\":\"haiku\"},{\"subagent_type\":\"parallel\",\"meta_prompt\":\"sonnet\"},{\"subagent_type\":\"parallel\",\"meta_prompt\":\"free verse\"}]}",
		"- manage_todos (atomic batch): use {\"action\":\"batch\",\"owner_kind\":\"agent\",\"operations\":[{...},{...},{...}]} when multiple todo changes must commit together.",
		"- manage_todos (single-call reorder): use {\"action\":\"reorder\",\"owner_kind\":\"agent\",\"ordered_ids\":[\"todo_3\",\"todo_1\",\"todo_2\"]} only when order itself must change.",
		"- manage_todos (agent checklist create): {\"action\":\"create\",\"owner_kind\":\"agent\",\"text\":\"Implement backend owner_kind plumbing\",\"group\":\"phase 1\"}",
		"- manage_todos (agent checklist progress): {\"action\":\"batch\",\"owner_kind\":\"agent\",\"operations\":[{\"action\":\"update\",\"id\":\"todo_prev\",\"done\":true,\"in_progress\":false},{\"action\":\"update\",\"id\":\"todo_next\",\"in_progress\":true}]}",
		"- manage_todos supports an atomic `batch` action with an `operations` payload for true bulk changes.",
		"- manage_todos also supports `delete_done` for removing completed tasks in one call.",
		fmt.Sprintf("- plan_manage (update active plan without switching modes): %s", autoModePlanManageSaveSnippet),
		"- exit_plan_mode (submit plan for approval and exit plan mode): {\"title\":\"Plan: tighten harness prompt\",\"plan\":\"# Plan\\n1. Update master prompt\\n2. Add harness examples\\n3. Clarify plan mode state transition\\n\\n## Relevant filepaths\\n- swarmd/internal/run/service.go\\n\\n## Open questions\\n- none\"}",
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
	providerOverride := strings.ToLower(strings.TrimSpace(agentProfile.Provider))
	modelOverride := strings.TrimSpace(agentProfile.Model)
	thinkingOverride := normalizeThinkingLevel(agentProfile.Thinking)

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
	if !strings.EqualFold(strings.TrimSpace(base.Provider), "codex") || !strings.EqualFold(strings.TrimSpace(base.Model), "gpt-5.4") {
		base.ServiceTier = ""
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
	default:
		return ""
	}
}

func (s *Service) resolveAgentProfile(name, targetKind string) (pebblestore.AgentProfile, error) {
	targetKind = normalizeRunTargetKind(targetKind)
	switch targetKind {
	case "", RunTargetKindAgent:
		return s.resolveAgent(name)
	case RunTargetKindSubagent:
		if s.agents == nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q cannot resolve without agent service", strings.TrimSpace(name))
		}
		return s.agents.ResolveSubagent(name)
	case RunTargetKindBackground:
		if s.agents == nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("background agent %q cannot resolve without agent service", strings.TrimSpace(name))
		}
		if strings.EqualFold(strings.TrimSpace(name), "memory") {
			return s.agents.ResolveSubagent(name)
		}
		return s.agents.ResolveBackground(name)
	default:
		return pebblestore.AgentProfile{}, fmt.Errorf("unsupported target_kind %q", strings.TrimSpace(targetKind))
	}
}

func (s *Service) resolveAgent(name string) (pebblestore.AgentProfile, error) {
	if s.agents != nil {
		return s.agents.ResolveAgent(name)
	}
	return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                "swarm",
		Mode:                agentruntime.ModePrimary,
		Provider:            "",
		Prompt:              "You are Swarm, the primary orchestration agent. Execute user tasks with concise, concrete outcomes. Match execution depth to request scope and avoid unnecessary delegation for narrow asks.",
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		Enabled:             true,
		UpdatedAt:           0,
		Description:         "fallback primary agent",
	}), nil
}

func (s *Service) composeInstructions(workspacePath string, agentProfile pebblestore.AgentProfile, userInstructions string) string {
	scope, err := s.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{WorkspacePath: workspacePath})
	if err != nil {
		scope = tool.WorkspaceScope{
			PrimaryPath: workspacePath,
			Roots:       []string{workspacePath},
		}
	}
	return s.composeInstructionsForScope(scope, agentProfile, userInstructions)
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
	runtimeContract := "unset"
	if exitPlanModeEnabled {
		runtimeContract = "plan -> auto"
	} else if executionSetting != "" {
		runtimeContract = executionSetting
	}
	toolScopeBase := "base execution setting"
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
		if snippet := readPromptSnippet(path, maxRulePromptBytes); snippet != "" {
			lines = append(lines, snippet)
		}
		added++
	}
	if added == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func readPromptSnippet(path string, maxBytes int) string {
	if strings.TrimSpace(path) == "" || maxBytes <= 0 {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > maxBytes {
		trimmed = strings.TrimSpace(trimmed[:maxBytes]) + "\n...[truncated]"
	}
	return trimmed
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
	mode = sessionruntime.NormalizeMode(mode)
	setting, hasExecutionSetting := pebblestore.AgentExecutionSetting(agentProfile)
	executionSetting := setting
	exitPlanModeEnabled := pebblestore.AgentExitPlanModeEnabled(agentProfile)
	if executionSetting == "" {
		executionSetting = "unset"
	}

	lines := []string{"Current session mode: " + mode + "."}
	if exitPlanModeEnabled {
		lines = append(lines, "Current agent runtime contract: plan -> auto.")
	} else {
		lines = append(lines, "Current agent runtime contract: "+executionSetting+".")
	}
	lines = append(lines,
		fmt.Sprintf("Current agent exit-plan-mode enabled: %t.", exitPlanModeEnabled),
		"The tool list attached to this run is the authoritative resolved contract for this agent.",
		"Use ask-user only for true product/decision forks; do not use ask-user to request tool permissions.",
		"Tool capability policy (enforced by backend):",
	)
	switch executionSetting {
	case pebblestore.AgentExecutionSettingRead:
		lines = append(lines,
			"- read execution setting provides the baseline non-mutating contract when plan mode is disabled.",
			"- the saved agent profile may still explicitly enable or disable tools beyond that baseline.",
			"- do not assume bash, write, or edit access unless those tools are present in the resolved tool list.",
		)
	case pebblestore.AgentExecutionSettingReadWrite:
		lines = append(lines,
			"- readwrite execution setting provides the baseline mutable contract when plan mode is disabled.",
			"- the saved agent profile may still explicitly disable tools or add scoped tools beyond that baseline.",
			"- do not assume bash access unless bash is present in the resolved tool list.",
		)
	default:
		if exitPlanModeEnabled {
			lines = append(lines,
				"- tool availability is determined by plan mode until exit_plan_mode switches the session to auto.",
				"- read/readwrite execution capability requests are overridden while plan mode is enabled.",
			)
		} else {
			lines = append(lines,
				"- no static execution setting is configured for this agent.",
				"- with plan mode disabled, runs will fail until execution_setting is set to read or readwrite.",
			)
		}
	}
	if exitPlanModeEnabled {
		lines = append(lines,
			fmt.Sprintf("- exit_plan_mode is available for this agent, but still requires explicit approval and only succeeds from session plan mode. Never call it from auto; to revise the active plan in auto, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
			"- plan_manage is available in both plan and auto to inspect or update saved plans; it does not change session mode.",
		)
	} else {
		lines = append(lines,
			"- exit_plan_mode is unavailable for this agent and will be rejected by backend policy.",
		)
	}
	if mode == sessionruntime.ModePlan {
		lines = append(lines,
			"Plan-mode expectation: run targeted discovery, then draft/refine a concrete execution plan quickly.",
			"Do not keep scanning for unrelated edge cases once the plan is actionable.",
			"Do not create or churn agent checklist todos during plan-only discovery unless the user explicitly asks for execution tracking before implementation starts.",
		)
		if exitPlanModeEnabled {
			lines = append(lines,
				"Keep refining the plan with plan_manage as needed. Call exit_plan_mode only when you want approval to leave plan mode. After approval, execution continues in auto on the same active plan/checklist, and plan_manage can still update it.",
			)
		}
	} else {
		lines = append(lines,
			"Execution expectation: continue implementation; ask-user only for true product/decision forks.",
			"When an active plan exists and the work is multi-step, keep the execution checklist in `manage_todos owner_kind=agent` aligned with actual implementation progress.",
		)
		if mode == sessionruntime.ModeAuto && exitPlanModeEnabled {
			lines = append(lines,
				fmt.Sprintf("If an active plan exists, use plan_manage get-active/save to inspect or revise it without switching modes. Do not call exit_plan_mode from auto; it only applies when leaving plan mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
			)
		}
		if !exitPlanModeEnabled && hasExecutionSetting {
			lines = append(lines,
				"With plan mode disabled, the backend uses the execution setting as the effective runtime mode.",
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

		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "assistant":
			input = append(input, map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": content},
				},
			})
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
			Parameters:  definition.Parameters,
		})
	}
	return out
}

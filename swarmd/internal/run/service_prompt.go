package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const autoModePlanManageSaveSnippet = `{"action":"save","document":{"info":{"goal":"..."},"checkpoints":[{"id":"cp-1","title":"...","status":"pending"}]},"update_summary":"what changed","update_scope":"phase or section"}`

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
		"- For multiple independent user-owned manage_todos operations, prefer a single atomic `batch` action with an `operations` array when they should succeed or fail together.",
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
		"- For multi-step implementation work, use `plan_manage` terminal checkpoint actions for checkpoint outcomes; avoid separate checkpoint progress updates unless there is meaningful intermediate state to preserve.",
		"- Preserve manage_todos as the user-owned workspace todo surface. Do not use manage_todos for agent self-tracking or checkpoint lifecycle state.",
		"- Put final checkpoint notes, reports, changed files, and validation evidence on the terminal checkpoint action rather than making a separate routine progress update.",
		"- Keep plan_manage as the single canonical checkpoint lifecycle surface: use terminal checkpoint actions for lifecycle outcomes, with intermediate checkpoint updates only when they add durable value.",
		"- If user feedback asks for more work on an active, approved, running, or final-review plan, use plan_manage action=request_followup_checkpoint with exact change_request text; use request_plan_revision for larger plan rewrites and request_new_plan for a separate replacement direction.",
		"- Terminal checkpoint actions only finish the current checkpoint; do not use complete_checkpoint to encode new user feedback or to re-complete a plan already waiting for final review.",
		"- Searching, reading, and codebase discovery do not count as checkpoint progress by themselves; only mark progress when a concrete implementation step starts or completes.",
		"- If the task is short and can be completed in one concrete step, skip progress churn and use the terminal checkpoint action when done.",
		"- If a branch of investigation is not required to complete the user request, stop and list it as optional follow-up instead of exploring it now.",
		"- If the user explicitly instructs you to change settings, make the settings change directly via the appropriate settings/config tool or file path instead of only suggesting it.",
		"- If the user is only making a suggestion or preference statement rather than an explicit change request, do not silently mutate settings; either note the suggestion as follow-up guidance or redirect them to the relevant settings surface.",
		"- When you provide long commands, config snippets, file contents, or any text the user is likely to copy, wrap that exact payload in <copy>...</copy> tags. Use an optional label attribute like <copy label=\"restart command\">...</copy> when it helps the UI preview.",
		"- Keep copy-tag payloads exact and free of explanatory prose; put context before or after the tagged block. Multiple <copy> blocks are allowed in one response.",
		"- In plan mode, once the plan is actionable, submit it with exit_plan_mode for approval so the session can leave plan mode and continue execution; include the final structured document (info and checkpoints) directly in that exit_plan_mode call instead of doing a separate last-minute plan_manage save first.",
		fmt.Sprintf("- In auto mode, never call exit_plan_mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
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
		"- task (batched subagents): {\"description\":\"Write poem variants\",\"prompt\":\"Write a poem about the sea.\",\"launches\":[{\"subagent_type\":\"parallel\",\"meta_prompt\":\"haiku\"},{\"subagent_type\":\"parallel\",\"meta_prompt\":\"sonnet\"},{\"subagent_type\":\"parallel\",\"meta_prompt\":\"free verse\"}]}",
		"- manage_todos (user todo batch only): use {\"action\":\"batch\",\"owner_kind\":\"user\",\"operations\":[{...},{...},{...}]} when the user asks to mutate their workspace todo list atomically.",
		"- manage_todos (user todo reorder only): use {\"action\":\"reorder\",\"owner_kind\":\"user\",\"ordered_ids\":[\"todo_3\",\"todo_1\",\"todo_2\"]} only when the user asks to reorder their todo list.",
		"- Do not use manage_todos for agent execution checklists or checkpoint progress; use terminal plan_manage checkpoint actions instead. Use update_checkpoint only for meaningful intermediate state, not routine checkpoint completion notes.",
		"- plan_manage terminal checkpoint example: {\"action\":\"complete_checkpoint\",\"checkpoint_id\":\"cp-1\",\"report\":\"Implemented requested change\",\"changed_files\":[\"path/to/file\"],\"validation\":[\"not run; not requested\"],\"result\":\"done\"}",
		"- plan_manage follow-up checkpoint example: {\"action\":\"request_followup_checkpoint\",\"change_request\":\"exact user feedback text\"}; use request_plan_revision for broader approved-plan rewrites and request_new_plan for a separate new plan proposal.",
		fmt.Sprintf("- plan_manage (update active plan without switching modes): %s", autoModePlanManageSaveSnippet),
		"- plan_manage modular document patches: update_info and update_checkpoint merge only provided fields and preserve omitted fields; use replace_info/set_info or replace_checkpoint/set_checkpoint only when intentionally replacing a whole object.",
		"- exit_plan_mode (submit final structured plan document for approval and exit plan mode; include plan_id when reusing an active plan): {\"title\":\"Plan: tighten harness prompt\",\"plan_id\":\"plan_123\",\"document\":{\"info\":{\"goal\":\"Tighten harness prompt\",\"relevant_files\":[\"swarmd/internal/run/service.go\"]},\"checkpoints\":[{\"id\":\"cp-1\",\"title\":\"Update prompt\",\"status\":\"pending\",\"tasks\":[\"Update master prompt\"]}]}}",
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

func (s *Service) resolveAgentProfile(name, targetKind string, integrationFlow bool) (pebblestore.AgentProfile, error) {
	return s.resolveAgentProfileForAccount("", name, targetKind, integrationFlow)
}

func (s *Service) resolveAgentProfileForAccount(accountScopeID, name, targetKind string, integrationFlow bool) (pebblestore.AgentProfile, error) {
	targetKind = normalizeRunTargetKind(targetKind)
	if integrationFlow || agentruntime.IsIntegrationBuilderAgentName(name) {
		if !integrationFlow {
			return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is reserved for integration flows", strings.TrimSpace(name))
		}
		if s.agents == nil {
			return pebblestore.AgentProfile{}, fmt.Errorf("integration builder %q cannot resolve without agent service", strings.TrimSpace(name))
		}
		return s.agents.ResolveIntegrationBuilderAgent(name)
	}
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
			fmt.Sprintf("- exit_plan_mode is available for this agent, but still requires explicit approval and only succeeds from session plan mode. Never call it from auto; to revise the active plan in auto, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
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
				fmt.Sprintf("If an active plan exists, use plan_manage get-active/save to inspect or revise it without switching modes. Do not call exit_plan_mode from auto; it only applies when leaving plan mode. To update the active plan instead, use plan_manage with exactly: %s", autoModePlanManageSaveSnippet),
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

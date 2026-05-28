package run

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const taskLaunchPermissionPathID = "permission.task_launch.v1"

type taskCallArguments struct {
	Action          string
	Description     string
	Prompt          string
	Launches        []taskLaunchSpec
	ReportMaxChars  int
	SourceArguments map[string]any
}

type taskLaunchSpec struct {
	RequestedSubagentType string
	MetaPrompt            string
	AssignmentLabel       string
	SourceArguments       map[string]any
}

type taskLaunchManifest struct {
	PathID              string                         `json:"path_id"`
	Goal                string                         `json:"goal"`
	LaunchCount         int                            `json:"launch_count"`
	Description         string                         `json:"description"`
	Prompt              string                         `json:"prompt"`
	SubagentType        string                         `json:"subagent_type"`
	ResolvedAgentName   string                         `json:"resolved_agent_name"`
	ResolvedAgentError  string                         `json:"resolved_agent_error,omitempty"`
	Action              string                         `json:"action"`
	ReportMaxChars      int                            `json:"report_max_chars"`
	ParentMode          string                         `json:"parent_mode"`
	EffectiveChildMode  string                         `json:"effective_child_mode"`
	DisabledTools       []string                       `json:"disabled_tools,omitempty"`
	ResolvedTools       *taskLaunchResolvedToolSummary `json:"resolved_tools,omitempty"`
	TargetWorkspacePath string                         `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName string                         `json:"target_workspace_name,omitempty"`
	SourceArguments     map[string]any                 `json:"source_arguments,omitempty"`
	Parent              *taskLaunchParentInfo          `json:"parent,omitempty"`
	Launches            []taskLaunchManifestRow        `json:"launches,omitempty"`
}

type manageFlowPermissionPayload struct {
	PathID            string         `json:"path_id,omitempty"`
	Tool              string         `json:"tool,omitempty"`
	Action            string         `json:"action,omitempty"`
	FlowID            string         `json:"flow_id,omitempty"`
	Name              string         `json:"name,omitempty"`
	ApprovalSummary   string         `json:"approval_summary,omitempty"`
	ApprovedArguments map[string]any `json:"approved_arguments,omitempty"`
	Preview           map[string]any `json:"preview,omitempty"`
}

type planManagePermissionPayload struct {
	PathID            string         `json:"path_id,omitempty"`
	Title             string         `json:"title,omitempty"`
	PlanID            string         `json:"plan_id,omitempty"`
	PriorTitle        string         `json:"prior_title,omitempty"`
	PriorPlan         string         `json:"prior_plan,omitempty"`
	Plan              string         `json:"plan,omitempty"`
	DiffLines         []string       `json:"diff_lines,omitempty"`
	Status            string         `json:"status,omitempty"`
	ApprovalState     string         `json:"approval_state,omitempty"`
	Activate          bool           `json:"activate,omitempty"`
	Action            string         `json:"action,omitempty"`
	UpdateType        string         `json:"update_type,omitempty"`
	UpdateSummary     string         `json:"update_summary,omitempty"`
	UpdateScope       string         `json:"update_scope,omitempty"`
	UpdateKind        string         `json:"update_kind,omitempty"`
	Checkpoint        bool           `json:"checkpoint,omitempty"`
	ApprovedArguments map[string]any `json:"approved_arguments,omitempty"`
}

type taskLaunchParentInfo struct {
	SessionID           string `json:"session_id,omitempty"`
	PermissionSessionID string `json:"permission_session_id,omitempty"`
	Mode                string `json:"mode,omitempty"`
	WorkspacePath       string `json:"workspace_path,omitempty"`
	WorkspaceName       string `json:"workspace_name,omitempty"`
	WorktreeEnabled     bool   `json:"worktree_enabled"`
	WorktreeRootPath    string `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch  string `json:"worktree_base_branch,omitempty"`
	WorktreeBranch      string `json:"worktree_branch,omitempty"`
}

type taskLaunchManifestRow struct {
	Description           string                         `json:"description"`
	RequestedSubagentType string                         `json:"requested_subagent_type"`
	ResolvedAgentName     string                         `json:"resolved_agent_name"`
	ResolvedAgentError    string                         `json:"resolved_agent_error,omitempty"`
	Action                string                         `json:"action"`
	ReportMaxChars        int                            `json:"report_max_chars"`
	MetaPrompt            string                         `json:"meta_prompt,omitempty"`
	AssignmentLabel       string                         `json:"assignment_label,omitempty"`
	SubagentProvider      string                         `json:"subagent_provider,omitempty"`
	SubagentModel         string                         `json:"subagent_model,omitempty"`
	ChildTitlePreview     string                         `json:"child_title_preview,omitempty"`
	ChildMode             string                         `json:"effective_child_mode"`
	DisabledTools         []string                       `json:"disabled_tools,omitempty"`
	ResolvedTools         *taskLaunchResolvedToolSummary `json:"resolved_tools,omitempty"`
	Capabilities          map[string]any                 `json:"capabilities,omitempty"`
	TargetWorkspacePath   string                         `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName   string                         `json:"target_workspace_name,omitempty"`
	SourceArguments       map[string]any                 `json:"source_arguments,omitempty"`
}

type taskLaunchResolvedToolSummary struct {
	Preset                 string   `json:"preset,omitempty"`
	RuntimeMode            string   `json:"runtime_mode,omitempty"`
	EffectiveExecutionMode string   `json:"effective_execution_mode,omitempty"`
	InheritPolicy          bool     `json:"inherit_policy,omitempty"`
	AllowedTools           []string `json:"allowed_tools,omitempty"`
	DisabledTools          []string `json:"disabled_tools,omitempty"`
	ProfileAllowedTools    []string `json:"profile_allowed_tools,omitempty"`
	ProfileDisabledTools   []string `json:"profile_disabled_tools,omitempty"`
	LaunchDisabledTools    []string `json:"launch_disabled_tools,omitempty"`
	BashPrefixes           []string `json:"bash_prefixes,omitempty"`
}

func parseTaskCallArguments(arguments string) (taskCallArguments, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if !strings.HasPrefix(arguments, "{") && containsToolParameterMarkup(arguments) {
		return taskCallArguments{}, fmt.Errorf("malformed XML markup in tool call")
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return taskCallArguments{}, fmt.Errorf("task arguments invalid: %w", err)
	}
	if containsMalformedToolParameterMarkupValue(args) {
		return taskCallArguments{}, fmt.Errorf("malformed XML markup in tool call")
	}
	if err := rejectTaskLaunchTrustFields(args, "task"); err != nil {
		return taskCallArguments{}, err
	}

	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	switch action {
	case "", "spawn", "start", "run":
		action = "spawn"
	default:
		return taskCallArguments{}, fmt.Errorf("task action %q is not supported", action)
	}

	description := strings.TrimSpace(mapString(args, "description"))
	if description == "" {
		description = "delegated task"
	}
	prompt := strings.TrimSpace(mapString(args, "prompt"))
	if prompt == "" {
		prompt = strings.TrimSpace(mapString(args, "message"))
	}
	if prompt == "" {
		return taskCallArguments{}, fmt.Errorf("task requires prompt")
	}

	reportMaxChars := mapInt(args, "report_max_chars")
	if reportMaxChars <= 0 {
		reportMaxChars = taskReportDefaultChars
	}
	if reportMaxChars < taskReportMinChars {
		reportMaxChars = taskReportMinChars
	}
	if reportMaxChars > taskReportMaxChars {
		reportMaxChars = taskReportMaxChars
	}

	parseLaunchSpec := func(raw map[string]any, label string) (taskLaunchSpec, error) {
		if err := rejectTaskLaunchTrustFields(raw, label); err != nil {
			return taskLaunchSpec{}, err
		}
		launch := taskLaunchSpec{
			RequestedSubagentType: strings.TrimSpace(firstNonEmptyString(
				mapString(raw, "subagent_type"),
				mapString(raw, "agent"),
				mapString(raw, "purpose"),
			)),
			MetaPrompt: strings.TrimSpace(firstNonEmptyString(
				mapString(raw, "meta_prompt"),
				mapString(raw, "role"),
			)),
			AssignmentLabel: strings.TrimSpace(firstNonEmptyString(
				mapString(raw, "assignment_label"),
				mapString(raw, "label"),
			)),
			SourceArguments: cloneGenericMap(raw),
		}
		if launch.RequestedSubagentType == "" {
			return taskLaunchSpec{}, fmt.Errorf("%s requires subagent_type, agent, or purpose", label)
		}
		if launch.MetaPrompt == "" {
			return taskLaunchSpec{}, fmt.Errorf("%s requires meta_prompt or role assignment", label)
		}
		return launch, nil
	}

	launches := make([]taskLaunchSpec, 0, 8)
	if rawLaunches, ok := args["launches"]; ok {
		typed, ok := rawLaunches.([]any)
		if !ok {
			return taskCallArguments{}, fmt.Errorf("task launches must be an array")
		}
		for i, item := range typed {
			entry, ok := item.(map[string]any)
			if !ok {
				return taskCallArguments{}, fmt.Errorf("task launches[%d] must be an object", i)
			}
			launch, err := parseLaunchSpec(entry, fmt.Sprintf("task launches[%d]", i))
			if err != nil {
				return taskCallArguments{}, err
			}
			launches = append(launches, launch)
		}
		if len(launches) == 0 {
			return taskCallArguments{}, fmt.Errorf("task requires at least one launch")
		}
	}

	if len(launches) == 0 {
		launch, err := parseLaunchSpec(args, "task launch")
		if err != nil {
			return taskCallArguments{}, err
		}
		launches = append(launches, launch)
	}

	return taskCallArguments{
		Action:          action,
		Description:     description,
		Prompt:          prompt,
		Launches:        launches,
		ReportMaxChars:  reportMaxChars,
		SourceArguments: args,
	}, nil
}

func rejectMalformedToolCallArguments(call tool.Call) error {
	if canonicalToolName(call.Name) != "task" {
		return nil
	}
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		return nil
	}
	if !strings.HasPrefix(arguments, "{") && containsToolParameterMarkup(arguments) {
		return fmt.Errorf("malformed XML markup in tool call")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || args == nil {
		return nil
	}
	if containsMalformedToolParameterMarkupValue(args) {
		return fmt.Errorf("malformed XML markup in tool call")
	}
	return nil
}

func containsMalformedToolParameterMarkupValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return containsMalformedToolParameterMarkup(typed)
	case map[string]any:
		for _, entry := range typed {
			if containsMalformedToolParameterMarkupValue(entry) {
				return true
			}
		}
	case []any:
		for _, entry := range typed {
			if containsMalformedToolParameterMarkupValue(entry) {
				return true
			}
		}
	}
	return false
}

func containsToolParameterMarkup(value string) bool {
	lower := strings.ToLower(value)
	return containsParameterOpenTag(lower) || containsParameterClosingTag(lower)
}

func containsMalformedToolParameterMarkup(value string) bool {
	lower := strings.ToLower(value)
	return containsMalformedParameterClosingTag(lower)
}

func containsParameterClosingTag(lower string) bool {
	found, _ := scanParameterClosingTag(lower)
	return found
}

func containsMalformedParameterClosingTag(lower string) bool {
	found, malformed := scanParameterClosingTag(lower)
	return found && malformed
}

func scanParameterClosingTag(lower string) (bool, bool) {
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], "</")
		if idx < 0 {
			return false, false
		}
		idx += offset
		nameStart := idx + len("</")
		endRel := strings.IndexByte(lower[nameStart:], '>')
		if endRel < 0 {
			return strings.Contains(lower[nameStart:], "parameter"), true
		}
		tagName := strings.TrimSpace(lower[nameStart : nameStart+endRel])
		if fields := strings.Fields(tagName); len(fields) > 0 {
			tagName = fields[0]
		}
		if tagName == "parameter" {
			return true, false
		}
		if strings.Contains(tagName, "parameter") {
			return true, true
		}
		offset = nameStart + endRel + 1
	}
	return false, false
}

func containsParameterOpenTag(lower string) bool {
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], "<parameter")
		if idx < 0 {
			return false
		}
		idx += offset
		after := idx + len("<parameter")
		if after >= len(lower) {
			return true
		}
		switch lower[after] {
		case ' ', '\t', '\n', '\r', '>', '/':
			return true
		}
		offset = after
	}
	return false
}

func rejectTaskLaunchTrustFields(args map[string]any, label string) error {
	for _, key := range []string{
		"allow_bash",
		"allow-bash",
		"execution_setting",
		"executionSetting",
		"tool_contract",
		"toolContract",
		"tool_scope",
		"toolScope",
		"tool_permissions",
		"toolPermissions",
		"allow_tools",
		"allowTools",
		"deny_tools",
		"denyTools",
		"disabled_tools",
		"disabledTools",
		"trust",
		"trusted",
	} {
		if _, ok := args[key]; ok {
			return fmt.Errorf("%s cannot set launch-time trust, execution, or tool field %q; update the saved agent profile instead", label, key)
		}
	}
	return nil
}

func effectiveTaskChildMode(sessionMode string) string {
	childMode := sessionruntime.NormalizeMode(sessionMode)
	if childMode == sessionruntime.ModePlan {
		childMode = sessionruntime.ModeAuto
	}
	return childMode
}

func taskAssignmentLabel(explicitLabel, metaPrompt, description, resolvedSubagent string) string {
	candidate := strings.TrimSpace(explicitLabel)
	if candidate == "" {
		candidate = strings.TrimSpace(metaPrompt)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(description)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(resolvedSubagent)
	}
	candidate = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(candidate)
	candidate = strings.Trim(candidate, " \"'`*-:;,.()[]{}")
	lower := strings.ToLower(candidate)
	for _, prefix := range []string{"meta-prompt:", "meta prompt:", "assignment:", "label:", "role:", "task:"} {
		if strings.HasPrefix(lower, prefix) {
			candidate = strings.TrimSpace(candidate[len(prefix):])
			candidate = strings.Trim(candidate, " \"'`*-:;,.()[]{}")
			lower = strings.ToLower(candidate)
			break
		}
	}
	if strings.HasPrefix(lower, "use the ") && (strings.HasSuffix(lower, " role") || strings.HasSuffix(lower, " role.")) {
		candidate = strings.TrimSpace(candidate[len("use the "):])
		candidate = strings.TrimSuffix(candidate, ".")
		candidate = strings.TrimSuffix(candidate, " role")
		candidate = strings.TrimSpace(candidate)
	}
	fields := strings.Fields(candidate)
	if len(fields) > 16 {
		candidate = strings.Join(fields[:16], " ")
	} else if len(fields) > 0 {
		candidate = strings.Join(fields, " ")
	}
	candidate = truncateRunes(candidate, 128)
	if candidate == "" {
		candidate = strings.TrimSpace(resolvedSubagent)
	}
	if candidate == "" {
		candidate = "Delegated task"
	}
	return candidate
}

func taskDisabledToolNames(allowBash bool) []string {
	disabled := taskDisabledTools(allowBash)
	return sortedDisabledToolNames(disabled)
}

func sortedDisabledToolNames(disabled map[string]bool) []string {
	if len(disabled) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(disabled))
	out := make([]string, 0, len(disabled))
	for name, disabled := range disabled {
		if !disabled {
			continue
		}
		canonical := canonicalToolName(name)
		if canonical == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out
}

func buildTaskLaunchResolvedToolSummary(contract ResolvedAgentToolContract, profileDisabled map[string]bool, launchDisabled []string, effectiveMode string) *taskLaunchResolvedToolSummary {
	allowed := append([]string(nil), contract.AvailableTools...)
	disabled := append([]string(nil), contract.UnavailableTools...)
	sort.Strings(allowed)
	sort.Strings(disabled)
	profileDisabledNames := sortedDisabledToolNames(profileDisabled)
	launchDisabledNames := append([]string(nil), launchDisabled...)
	launchDisabledSet := make(map[string]struct{}, len(launchDisabledNames))
	for _, name := range launchDisabledNames {
		name = canonicalToolName(name)
		if name == "" {
			continue
		}
		launchDisabledSet[name] = struct{}{}
	}
	combinedDisabled := make(map[string]bool, len(disabled)+len(launchDisabledSet))
	for _, name := range disabled {
		name = canonicalToolName(name)
		if name != "" {
			combinedDisabled[name] = true
		}
	}
	for name := range launchDisabledSet {
		combinedDisabled[name] = true
	}
	allowedOut := make([]string, 0, len(allowed))
	for _, name := range allowed {
		name = canonicalToolName(name)
		if name == "" {
			continue
		}
		if _, blocked := launchDisabledSet[name]; blocked {
			continue
		}
		allowedOut = append(allowedOut, name)
	}
	bashPrefixes := make([]string, 0)
	if bashTool, ok := contract.Tools["bash"]; ok && bashTool.Enabled && len(bashTool.BashPrefixes) > 0 {
		bashPrefixes = append(bashPrefixes, bashTool.BashPrefixes...)
	}
	sort.Strings(allowedOut)
	sort.Strings(profileDisabledNames)
	sort.Strings(launchDisabledNames)
	sort.Strings(bashPrefixes)
	return &taskLaunchResolvedToolSummary{
		Preset:                 strings.TrimSpace(contract.RawPreset),
		RuntimeMode:            strings.TrimSpace(contract.RuntimeMode),
		EffectiveExecutionMode: strings.TrimSpace(effectiveMode),
		InheritPolicy:          contract.InheritPolicy,
		AllowedTools:           allowedOut,
		DisabledTools:          sortedDisabledToolNames(combinedDisabled),
		ProfileAllowedTools:    allowed,
		ProfileDisabledTools:   profileDisabledNames,
		LaunchDisabledTools:    launchDisabledNames,
		BashPrefixes:           bashPrefixes,
	}
}

func (s *Service) permissionArgumentsForCall(sessionID, sessionMode string, call tool.Call) (string, error) {
	arguments := strings.TrimSpace(call.Arguments)
	marshalPayload := func(payload any) (string, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	switch canonicalToolName(call.Name) {
	case "task":
		payload, err := s.buildTaskLaunchPermissionPayload(sessionID, sessionMode, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_skill":
		payload, err := s.buildManageSkillPermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_agent":
		payload, err := s.buildManageAgentPermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_theme":
		payload, err := s.buildManageThemePermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_image":
		payload, err := s.buildManageImagePermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "plan_manage":
		payload, ok, err := s.buildPlanManagePermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		if !ok {
			return arguments, nil
		}
		return marshalPayload(payload)
	case "manage_worktree":
		return arguments, nil
	case "manage_flow":
		payload, err := s.buildManageFlowPermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_todos":
		payload, err := s.buildManageTodosPermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	default:
		return arguments, nil
	}
}

func (s *Service) buildManageFlowPermissionPayload(sessionID string, call tool.Call) (manageFlowPermissionPayload, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return manageFlowPermissionPayload{}, fmt.Errorf("manage-flow arguments invalid: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = "inspect"
	}
	flowID := strings.TrimSpace(firstNonEmptyString(mapString(args, "flow_id"), mapString(args, "id"), mapString(args, "name")))
	payload := manageFlowPermissionPayload{PathID: "permission.manage_flow.v1", Tool: "manage-flow", Action: action, FlowID: flowID, Name: strings.TrimSpace(mapString(args, "name")), ApprovedArguments: cloneGenericMap(args)}
	if payload.ApprovedArguments == nil {
		payload.ApprovedArguments = map[string]any{}
	}
	payload.ApprovedArguments["confirm"] = true
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return manageFlowPermissionPayload{}, err
	}
	if !ok {
		return manageFlowPermissionPayload{}, fmt.Errorf("session %q not found", sessionID)
	}
	previewScope := buildPermissionWorkspaceScope(session)
	previewArgs := cloneGenericMap(args)
	if previewArgs == nil {
		previewArgs = map[string]any{"action": action}
	}
	delete(previewArgs, "confirm")
	raw, err := json.Marshal(previewArgs)
	if err == nil && s.tools != nil {
		if previewOutput, previewErr := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), previewScope, tool.Call{Name: call.Name, Arguments: string(raw)}); previewErr == nil {
			var preview map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(previewOutput)), &preview) == nil {
				payload.Preview = preview
				if summary := strings.TrimSpace(mapString(preview, "summary")); summary != "" {
					payload.ApprovalSummary = summary
				}
				if change, ok := preview["change"].(map[string]any); ok {
					if summary := strings.TrimSpace(mapString(change, "approval_summary")); summary != "" {
						payload.ApprovalSummary = summary
					}
				}
			}
		}
	}
	if payload.ApprovalSummary == "" {
		payload.ApprovalSummary = fmt.Sprintf("%s flow %s", action, firstNonEmptyString(payload.Name, payload.FlowID, "(new flow)"))
	}
	return payload, nil
}

func (s *Service) buildManageTodosPermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-todos arguments invalid: %w", err)
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	payload := cloneGenericMap(args)
	if payload == nil {
		payload = map[string]any{}
	}
	if strings.TrimSpace(mapString(payload, "workspace_path")) == "" {
		payload["workspace_path"] = strings.TrimSpace(session.WorkspacePath)
	}
	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	ownerKind := strings.ToLower(strings.TrimSpace(mapString(payload, "owner_kind")))
	if ownerKind == "agent" {
		delete(payload, "priority")
		if action == "create" && strings.TrimSpace(mapString(payload, "session_id")) == "" {
			payload["session_id"] = strings.TrimSpace(sessionID)
		}
	}
	if action == "batch" {
		delete(payload, "priority")
		if operations, ok := payload["operations"].([]any); ok {
			normalized := make([]any, 0, len(operations))
			for _, rawOp := range operations {
				entry, ok := rawOp.(map[string]any)
				if !ok {
					normalized = append(normalized, rawOp)
					continue
				}
				cloned := cloneGenericMap(entry)
				opOwnerKind := strings.ToLower(strings.TrimSpace(mapString(cloned, "owner_kind")))
				if opOwnerKind == "" {
					opOwnerKind = ownerKind
				}
				if opOwnerKind != "" {
					cloned["owner_kind"] = opOwnerKind
				}
				if opOwnerKind == "agent" {
					delete(cloned, "priority")
					if strings.ToLower(strings.TrimSpace(mapString(cloned, "action"))) == "create" && strings.TrimSpace(mapString(cloned, "session_id")) == "" {
						cloned["session_id"] = strings.TrimSpace(sessionID)
					}
				}
				normalized = append(normalized, cloned)
			}
			payload["operations"] = normalized
		}
	}
	delete(payload, "approved_arguments")
	payload["approved_arguments"] = cloneGenericMap(payload)
	return payload, nil
}

func (s *Service) buildPlanManagePermissionPayload(sessionID string, call tool.Call) (planManagePermissionPayload, bool, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return planManagePermissionPayload{}, false, err
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(mapString(args, "op")))
	}
	switch action {
	case "ls":
		action = "list"
	case "show":
		action = "get"
	case "active", "current":
		action = "get-active"
	case "activate", "use":
		action = "set-active"
	case "create":
		action = "new"
	case "upsert", "set", "write-active", "write_active":
		action = "save"
	case "update", "edit":
		if strings.TrimSpace(mapString(args, "plan")) == "" {
			action = "patch"
		} else {
			action = "save"
		}
	case "update-section", "update_section":
		action = "update_section"
	}
	if action != "save" && action != "patch" && action != "update_section" {
		return planManagePermissionPayload{}, false, nil
	}
	planBody := strings.TrimSpace(mapString(args, "plan"))
	if action == "save" && planBody == "" {
		return planManagePermissionPayload{}, false, nil
	}
	if s.sessions == nil {
		return planManagePermissionPayload{}, false, fmt.Errorf("session service is not configured")
	}
	planID := strings.TrimSpace(mapString(args, "plan_id"))
	if planID == "" {
		planID = strings.TrimSpace(mapString(args, "id"))
	}
	var existing pebblestore.SessionPlanSnapshot
	var found bool
	var err error
	if planID != "" {
		existing, found, err = s.sessions.GetPlan(sessionID, planID)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
	} else {
		existing, found, err = s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		if found {
			planID = strings.TrimSpace(existing.ID)
		}
	}
	if !found || strings.TrimSpace(existing.ID) == "" {
		return planManagePermissionPayload{}, false, nil
	}
	title := strings.TrimSpace(mapString(args, "title"))
	if title == "" {
		title = strings.TrimSpace(existing.Title)
	}
	status := strings.TrimSpace(mapString(args, "status"))
	if status == "" {
		status = strings.TrimSpace(existing.Status)
	}
	approvalState := strings.TrimSpace(mapString(args, "approval_state"))
	if approvalState == "" {
		approvalState = strings.TrimSpace(existing.ApprovalState)
	}
	activate := true
	if _, hasActivate := args["activate"]; hasActivate {
		activate = mapBool(args, "activate")
	}
	updateSummary := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_summary"), mapString(args, "summary")))
	updateScope := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_scope"), mapString(args, "scope")))
	updateKind := strings.TrimSpace(firstNonEmptyString(mapString(args, "update_kind"), mapString(args, "kind")))
	checkpoint := mapBool(args, "checkpoint")
	previewPlan := planBody
	if action == "patch" || action == "update_section" {
		patch, err := planPatchFromManageArgs(args, action)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		previewPlan, err = sessionruntime.ApplyPlanPatch(existing.Plan, patch)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
	}
	payload := planManagePermissionPayload{
		PathID:        "tool.plan-manage-update.v1",
		Title:         title,
		PlanID:        planID,
		PriorTitle:    strings.TrimSpace(existing.Title),
		PriorPlan:     strings.TrimSpace(existing.Plan),
		Plan:          previewPlan,
		DiffLines:     sessionruntime.BuildPlanDiffLines(existing.Plan, previewPlan),
		Status:        status,
		ApprovalState: approvalState,
		Activate:      activate,
		Action:        action,
		UpdateType:    "existing_plan",
		UpdateSummary: updateSummary,
		UpdateScope:   updateScope,
		UpdateKind:    updateKind,
		Checkpoint:    checkpoint,
		ApprovedArguments: map[string]any{
			"action":         action,
			"plan_id":        planID,
			"title":          title,
			"status":         status,
			"approval_state": approvalState,
			"activate":       activate,
			"update_summary": updateSummary,
			"update_scope":   updateScope,
			"update_kind":    updateKind,
			"checkpoint":     checkpoint,
		},
	}
	if action == "save" {
		payload.ApprovedArguments["plan"] = planBody
	} else {
		for key, value := range args {
			switch key {
			case "patch", "operation", "patch_operation", "patch_action", "section", "old_text", "new_text", "text", "checklist_item", "item", "checked", "replace_all":
				payload.ApprovedArguments[key] = value
			}
		}
	}
	return payload, true, nil
}

func (s *Service) buildTaskLaunchPermissionPayload(sessionID, sessionMode string, call tool.Call) (taskLaunchManifest, error) {
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		return taskLaunchManifest{}, err
	}

	parentSession, _, _ := s.sessions.GetSession(sessionID)
	parentMode := sessionruntime.NormalizeMode(sessionMode)
	childMode := effectiveTaskChildMode(sessionMode)
	disabledTools := taskDisabledToolNames(false)

	launches := make([]taskLaunchManifestRow, 0, len(parsed.Launches))
	resolvedAgentName := ""
	resolvedAgentError := ""
	requestedPrimary := ""
	for i, launch := range parsed.Launches {
		requested := strings.TrimSpace(launch.RequestedSubagentType)
		if requested == "" {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] requires subagent_type, agent, or purpose", i)
		}
		if requestedPrimary == "" {
			requestedPrimary = requested
		}
		if s == nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] cannot resolve subagent %q: run service is not configured", i, requested)
		}
		subagentProfile, err := s.resolveTaskSubagentForAccount(parentSession.AccountScopeID, requested)
		if err != nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] cannot resolve subagent %q: %w", i, requested, err)
		}
		resolvedName := strings.TrimSpace(subagentProfile.Name)
		if resolvedName == "" {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] resolved empty subagent", i)
		}
		if i == 0 {
			resolvedAgentName = resolvedName
			resolvedAgentError = ""
		}
		metaPrompt := strings.TrimSpace(launch.MetaPrompt)
		if metaPrompt == "" {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] requires meta_prompt or role assignment", i)
		}
		assignmentLabel := taskAssignmentLabel(launch.AssignmentLabel, metaPrompt, parsed.Description, resolvedName)
		executionMode, _, modeErr := s.resolveExecutionMode(childMode, subagentProfile)
		if modeErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] cannot resolve subagent %q execution mode: %w", i, requested, modeErr)
		}
		toolContract, _, profileDisabledTools, toolErr := s.ResolveAgentToolContractForAccount(parentSession.AccountScopeID, subagentProfile)
		if toolErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] cannot resolve subagent %q tool contract: %w", i, requested, toolErr)
		}
		resolvedTools := buildTaskLaunchResolvedToolSummary(toolContract, profileDisabledTools, disabledTools, executionMode)
		preference := applyAgentPreferenceOverrides(parentSession.Preference, subagentProfile)
		childTitle := assignmentLabel
		launches = append(launches, taskLaunchManifestRow{
			Description:           parsed.Description,
			RequestedSubagentType: requested,
			ResolvedAgentName:     resolvedName,
			Action:                parsed.Action,
			ReportMaxChars:        parsed.ReportMaxChars,
			MetaPrompt:            metaPrompt,
			AssignmentLabel:       assignmentLabel,
			SubagentProvider:      strings.TrimSpace(preference.Provider),
			SubagentModel:         strings.TrimSpace(preference.Model),
			ChildTitlePreview:     childTitle,
			ChildMode:             childMode,
			DisabledTools:         disabledTools,
			ResolvedTools:         resolvedTools,
			Capabilities: map[string]any{
				"allow_bash":            false,
				"disabled_tools":        disabledTools,
				"effective_child_mode":  childMode,
				"resolved_tools":        resolvedTools,
				"permission_session_id": strings.TrimSpace(sessionID),
			},
			SourceArguments: cloneGenericMap(launch.SourceArguments),
		})
	}
	if len(launches) == 0 {
		return taskLaunchManifest{}, fmt.Errorf("task requires at least one launch")
	}
	if strings.TrimSpace(resolvedAgentName) == "" {
		return taskLaunchManifest{}, fmt.Errorf("task resolved empty primary subagent")
	}
	if strings.TrimSpace(requestedPrimary) == "" {
		return taskLaunchManifest{}, fmt.Errorf("task requires primary subagent")
	}

	manifest := taskLaunchManifest{
		PathID:             taskLaunchPermissionPathID,
		Goal:               parsed.Description,
		LaunchCount:        len(launches),
		Description:        parsed.Description,
		Prompt:             parsed.Prompt,
		SubagentType:       requestedPrimary,
		ResolvedAgentName:  resolvedAgentName,
		ResolvedAgentError: resolvedAgentError,
		Action:             parsed.Action,
		ReportMaxChars:     parsed.ReportMaxChars,
		ParentMode:         parentMode,
		EffectiveChildMode: childMode,
		DisabledTools:      disabledTools,
		ResolvedTools:      launches[0].ResolvedTools,
		SourceArguments:    parsed.SourceArguments,
		Launches:           launches,
	}

	parent, ok := s.lookupTaskLaunchParentSession(sessionID, parentMode)
	if ok {
		manifest.Parent = parent
		manifest.TargetWorkspacePath = strings.TrimSpace(parent.WorkspacePath)
		manifest.TargetWorkspaceName = strings.TrimSpace(parent.WorkspaceName)
		for i := range manifest.Launches {
			manifest.Launches[i].TargetWorkspacePath = strings.TrimSpace(parent.WorkspacePath)
			manifest.Launches[i].TargetWorkspaceName = strings.TrimSpace(parent.WorkspaceName)
		}
	}

	return manifest, nil
}

func buildPermissionWorkspaceScope(session pebblestore.SessionSnapshot) tool.WorkspaceScope {
	workspacePath := strings.TrimSpace(session.WorkspacePath)
	if workspacePath == "" {
		workspacePath = "."
	}
	primaryPath := workspacePath
	roots := make([]string, 0, 2+len(session.TemporaryWorkspaceRoots))
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		for _, existing := range roots {
			if existing == path {
				return
			}
		}
		roots = append(roots, path)
	}
	add(primaryPath)
	if rootPath := strings.TrimSpace(session.WorktreeRootPath); rootPath != "" {
		add(rootPath)
	}
	for _, root := range session.TemporaryWorkspaceRoots {
		add(root)
	}
	scope := tool.WorkspaceScope{PrimaryPath: primaryPath, Roots: roots, SessionID: strings.TrimSpace(session.ID)}
	if userID, accountScopeID := strings.TrimSpace(session.UserID), strings.TrimSpace(session.AccountScopeID); userID != "" && accountScopeID != "" {
		scope.Principal = identity.Principal{
			Type:               identity.PrincipalTypeUser,
			UserID:             userID,
			AccountScopeID:     accountScopeID,
			SessionID:          strings.TrimSpace(session.ID),
			AccountScopeSource: identity.AccountScopeSourceSession,
		}
	}
	return scope
}

func (s *Service) buildManageSkillPermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-skill arguments invalid: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = "inspect"
	}
	confirm := mapBool(args, "confirm")
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	payload := map[string]any{
		"action":  action,
		"confirm": confirm,
	}
	if skill := strings.TrimSpace(firstNonEmptyString(mapString(args, "skill"), mapString(args, "name"))); skill != "" {
		payload["skill"] = skill
	}
	if confirm {
		payload["approved_arguments"] = cloneGenericMap(args)
		return payload, nil
	}
	previewCall := tool.Call{Name: call.Name, Arguments: arguments}
	previewScope := buildPermissionWorkspaceScope(session)
	previewOutput, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), previewScope, previewCall)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(previewOutput)), &payload); err != nil {
		return nil, fmt.Errorf("manage-skill preview output invalid: %w", err)
	}
	payload["approved_arguments"] = cloneGenericMap(args)
	return payload, nil
}

func (s *Service) buildManageAgentPermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-agent arguments invalid: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = "inspect"
	}
	confirm := mapBool(args, "confirm")
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	payload := map[string]any{
		"action":  action,
		"confirm": confirm,
	}
	if agent := strings.TrimSpace(firstNonEmptyString(mapString(args, "agent"), mapString(args, "name"))); agent != "" {
		payload["agent"] = agent
	}
	if toolName := strings.TrimSpace(firstNonEmptyString(mapString(args, "tool_name"), mapString(args, "tool"))); toolName != "" {
		payload["tool_name"] = toolName
	}
	if confirm {
		payload["approved_arguments"] = cloneGenericMap(args)
		return payload, nil
	}
	previewCall := tool.Call{Name: call.Name, Arguments: arguments}
	previewScope := buildPermissionWorkspaceScope(session)
	previewOutput, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), previewScope, previewCall)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(previewOutput)), &payload); err != nil {
		return nil, fmt.Errorf("manage-agent preview output invalid: %w", err)
	}
	payload["approved_arguments"] = cloneGenericMap(args)
	return payload, nil
}

func (s *Service) buildManageThemePermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-theme arguments invalid: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "" {
		action = "inspect"
	}
	confirm := mapBool(args, "confirm")
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	payload := map[string]any{
		"action":  action,
		"confirm": confirm,
	}
	if themeID := strings.TrimSpace(firstNonEmptyString(mapString(args, "theme_id"), mapString(args, "theme"), mapString(args, "id"))); themeID != "" {
		payload["theme_id"] = themeID
	}
	if workspacePath := strings.TrimSpace(mapString(args, "workspace_path")); workspacePath != "" {
		payload["workspace_path"] = workspacePath
	}
	if confirm {
		payload["approved_arguments"] = cloneGenericMap(args)
		return payload, nil
	}
	previewCall := tool.Call{Name: call.Name, Arguments: arguments}
	previewScope := buildPermissionWorkspaceScope(session)
	previewOutput, err := s.tools.ExecuteForWorkspaceScopeWithRuntime(context.Background(), previewScope, previewCall)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(previewOutput)), &payload); err != nil {
		return nil, fmt.Errorf("manage-theme preview output invalid: %w", err)
	}
	payload["approved_arguments"] = cloneGenericMap(args)
	return payload, nil
}

func manageSkillApprovalArguments(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok {
		args := cloneGenericMap(approved)
		action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
		if action != "" {
			args["action"] = action
		}
		if _, ok := args["confirm"]; !ok {
			args["confirm"] = mapBool(payload, "confirm")
		}
		return args
	}

	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	if action == "" {
		return nil
	}
	args := map[string]any{"action": action}
	if skill := strings.TrimSpace(firstNonEmptyString(mapString(payload, "skill"), mapString(payload, "name"))); skill != "" {
		args["skill"] = skill
		args["name"] = skill
	}
	if confirm, ok := payload["confirm"].(bool); ok {
		args["confirm"] = confirm
	}
	if change, ok := payload["change"].(map[string]any); ok {
		if path := strings.TrimSpace(mapString(change, "path")); path != "" {
			args["path"] = path
		}
		if after, ok := change["after"].(string); ok {
			args["content"] = after
		}
	}
	if content := strings.TrimSpace(mapString(payload, "content")); content != "" {
		args["content"] = content
	}
	if len(args) == 1 {
		return nil
	}
	return args
}

func manageAgentApprovalArguments(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok {
		args := cloneGenericMap(approved)
		action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
		if action != "" {
			args["action"] = action
		}
		if _, ok := args["confirm"]; !ok {
			args["confirm"] = mapBool(payload, "confirm")
		}
		return args
	}

	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	if action == "" {
		return nil
	}
	args := map[string]any{"action": action}
	if agent := strings.TrimSpace(firstNonEmptyString(mapString(payload, "agent"), mapString(payload, "name"))); agent != "" {
		args["agent"] = agent
		args["name"] = agent
	}
	if confirm, ok := payload["confirm"].(bool); ok {
		args["confirm"] = confirm
	}
	if payloadAgent, ok := payload["agent"].(map[string]any); ok {
		if name := strings.TrimSpace(mapString(payloadAgent, "name")); name != "" {
			args["agent"] = name
			args["name"] = name
		}
	}
	if purpose := strings.TrimSpace(mapString(payload, "purpose")); purpose != "" {
		args["purpose"] = purpose
	}
	if toolName := strings.TrimSpace(firstNonEmptyString(mapString(payload, "tool_name"), mapString(payload, "tool"))); toolName != "" {
		args["tool_name"] = toolName
	}
	if customTool, ok := payload["custom_tool"].(map[string]any); ok {
		if name := strings.TrimSpace(firstNonEmptyString(mapString(customTool, "name"), mapString(customTool, "tool_name"))); name != "" {
			args["tool_name"] = name
		}
	}
	if change, ok := payload["change"].(map[string]any); ok {
		if purpose := strings.TrimSpace(mapString(change, "purpose")); purpose != "" {
			args["purpose"] = purpose
		}
		if toolName := strings.TrimSpace(firstNonEmptyString(mapString(change, "tool_name"), mapString(payload, "tool_name"))); toolName != "" {
			args["tool_name"] = toolName
		}
		switch action {
		case "create", "update", "create_custom_tool", "update_custom_tool":
			if after, ok := change["after"].(map[string]any); ok {
				args["content"] = cloneGenericMap(after)
			}
		case "delete", "activate_primary":
			if after := strings.TrimSpace(mapString(change, "after")); after != "" {
				args["agent"] = after
				args["name"] = after
			} else if before := strings.TrimSpace(mapString(change, "before")); before != "" {
				args["agent"] = before
				args["name"] = before
			}
		case "delete_custom_tool":
			if before, ok := change["before"].(map[string]any); ok {
				if name := strings.TrimSpace(firstNonEmptyString(mapString(before, "name"), mapString(before, "tool_name"))); name != "" {
					args["tool_name"] = name
				}
			}
		case "set_active_subagent":
			if after, ok := change["after"].(map[string]any); ok {
				purpose := strings.TrimSpace(mapString(change, "purpose"))
				if purpose != "" {
					if agent := strings.TrimSpace(mapString(after, purpose)); agent != "" {
						args["agent"] = agent
						args["name"] = agent
					}
				}
			}
		case "assign_custom_tool", "unassign_custom_tool":
			if agent := strings.TrimSpace(firstNonEmptyString(mapString(change, "agent"), mapString(payload, "agent"))); agent != "" {
				args["agent"] = agent
				args["name"] = agent
			}
		}
	}
	if content, ok := payload["content"].(map[string]any); ok && len(content) > 0 {
		args["content"] = cloneGenericMap(content)
	}
	if len(args) == 1 {
		return nil
	}
	return args
}

func planManageApprovalArguments(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok {
		args := cloneGenericMap(approved)
		action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
		if action != "" {
			args["action"] = action
		}
		if len(args) == 0 {
			return nil
		}
		if strings.TrimSpace(mapString(args, "action")) == "" {
			args["action"] = "save"
		}
		return args
	}

	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	if action == "" {
		action = "save"
	}
	args := map[string]any{"action": action}
	if planID := strings.TrimSpace(firstNonEmptyString(mapString(payload, "plan_id"), mapString(payload, "id"))); planID != "" {
		args["plan_id"] = planID
	}
	if title := strings.TrimSpace(mapString(payload, "title")); title != "" {
		args["title"] = title
	}
	if planBody := strings.TrimSpace(mapString(payload, "plan")); planBody != "" {
		args["plan"] = planBody
	}
	if status := strings.TrimSpace(mapString(payload, "status")); status != "" {
		args["status"] = status
	}
	if approvalState := strings.TrimSpace(mapString(payload, "approval_state")); approvalState != "" {
		args["approval_state"] = approvalState
	}
	if activate, ok := payload["activate"].(bool); ok {
		args["activate"] = activate
	}
	if action == "save" {
		if _, ok := args["plan"]; !ok {
			return nil
		}
	}
	return args
}

func manageThemeApprovalArguments(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok {
		args := cloneGenericMap(approved)
		action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
		if action != "" {
			args["action"] = action
		}
		if _, ok := args["confirm"]; !ok {
			args["confirm"] = mapBool(payload, "confirm")
		}
		return args
	}

	action := strings.ToLower(strings.TrimSpace(mapString(payload, "action")))
	if action == "" {
		return nil
	}
	args := map[string]any{"action": action}
	if themeID := strings.TrimSpace(firstNonEmptyString(mapString(payload, "theme_id"), mapString(payload, "theme"), mapString(payload, "id"))); themeID != "" {
		args["theme_id"] = themeID
		args["theme"] = themeID
	}
	if workspacePath := strings.TrimSpace(mapString(payload, "workspace_path")); workspacePath != "" {
		args["workspace_path"] = workspacePath
	}
	if confirm, ok := payload["confirm"].(bool); ok {
		args["confirm"] = confirm
	}
	if change, ok := payload["change"].(map[string]any); ok {
		if workspacePath := strings.TrimSpace(mapString(change, "workspace_path")); workspacePath != "" {
			args["workspace_path"] = workspacePath
		}
		if themeID := strings.TrimSpace(firstNonEmptyString(mapString(change, "theme_id"), mapString(change, "theme"))); themeID != "" {
			args["theme_id"] = themeID
			args["theme"] = themeID
		}
		if after, ok := change["after"].(map[string]any); ok {
			if record, ok := after["palette"].(map[string]any); ok {
				args["content"] = map[string]any{
					"id":      firstNonEmptyString(mapString(after, "id"), mapString(change, "theme_id")),
					"name":    mapString(after, "name"),
					"palette": cloneGenericMap(record),
				}
			}
		}
	}
	if content, ok := payload["content"].(map[string]any); ok && len(content) > 0 {
		args["content"] = cloneGenericMap(content)
	}
	if len(args) == 1 {
		return nil
	}
	return args
}

func (s *Service) lookupTaskLaunchParentSession(sessionID, mode string) (*taskLaunchParentInfo, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.sessions == nil || sessionID == "" {
		return nil, false
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, false
	}
	return buildTaskLaunchParentInfo(session, mode, sessionID), true
}

func buildTaskLaunchParentInfo(session pebblestore.SessionSnapshot, mode, permissionSessionID string) *taskLaunchParentInfo {
	return &taskLaunchParentInfo{
		SessionID:           strings.TrimSpace(session.ID),
		PermissionSessionID: strings.TrimSpace(permissionSessionID),
		Mode:                sessionruntime.NormalizeMode(mode),
		WorkspacePath:       strings.TrimSpace(session.WorkspacePath),
		WorkspaceName:       strings.TrimSpace(session.WorkspaceName),
		WorktreeEnabled:     session.WorktreeEnabled,
		WorktreeRootPath:    strings.TrimSpace(session.WorktreeRootPath),
		WorktreeBaseBranch:  strings.TrimSpace(session.WorktreeBaseBranch),
		WorktreeBranch:      strings.TrimSpace(session.WorktreeBranch),
	}
}

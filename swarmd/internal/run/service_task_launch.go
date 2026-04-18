package run

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
	AllowBash       bool
	SourceArguments map[string]any
}

type taskLaunchSpec struct {
	RequestedSubagentType string
	MetaPrompt            string
	SourceArguments       map[string]any
}

type taskLaunchManifest struct {
	PathID              string                  `json:"path_id"`
	Goal                string                  `json:"goal"`
	LaunchCount         int                     `json:"launch_count"`
	Description         string                  `json:"description"`
	Prompt              string                  `json:"prompt"`
	SubagentType        string                  `json:"subagent_type"`
	ResolvedAgentName   string                  `json:"resolved_agent_name"`
	ResolvedAgentError  string                  `json:"resolved_agent_error,omitempty"`
	Action              string                  `json:"action"`
	AllowBash           bool                    `json:"allow_bash"`
	ReportMaxChars      int                     `json:"report_max_chars"`
	ParentMode          string                  `json:"parent_mode"`
	EffectiveChildMode  string                  `json:"effective_child_mode"`
	DisabledTools       []string                `json:"disabled_tools,omitempty"`
	TargetWorkspacePath string                  `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName string                  `json:"target_workspace_name,omitempty"`
	SourceArguments     map[string]any          `json:"source_arguments,omitempty"`
	Parent              *taskLaunchParentInfo   `json:"parent,omitempty"`
	Launches            []taskLaunchManifestRow `json:"launches,omitempty"`
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
	Description           string         `json:"description"`
	RequestedSubagentType string         `json:"requested_subagent_type"`
	ResolvedAgentName     string         `json:"resolved_agent_name"`
	ResolvedAgentError    string         `json:"resolved_agent_error,omitempty"`
	Action                string         `json:"action"`
	AllowBash             bool           `json:"allow_bash"`
	ReportMaxChars        int            `json:"report_max_chars"`
	MetaPrompt            string         `json:"meta_prompt,omitempty"`
	ChildTitlePreview     string         `json:"child_title_preview,omitempty"`
	ChildMode             string         `json:"effective_child_mode"`
	DisabledTools         []string       `json:"disabled_tools,omitempty"`
	Capabilities          map[string]any `json:"capabilities,omitempty"`
	TargetWorkspacePath   string         `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName   string         `json:"target_workspace_name,omitempty"`
	SourceArguments       map[string]any `json:"source_arguments,omitempty"`
}

func parseTaskCallArguments(arguments string) (taskCallArguments, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return taskCallArguments{}, fmt.Errorf("task arguments invalid: %w", err)
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

	parseLaunchSpec := func(raw map[string]any) taskLaunchSpec {
		launch := taskLaunchSpec{
			RequestedSubagentType: strings.TrimSpace(firstNonEmptyString(
				mapString(raw, "subagent_type"),
				mapString(raw, "agent"),
				mapString(raw, "purpose"),
			)),
			MetaPrompt:      strings.TrimSpace(firstNonEmptyString(mapString(raw, "meta_prompt"), mapString(raw, "role"))),
			SourceArguments: cloneGenericMap(raw),
		}
		if launch.RequestedSubagentType == "" {
			launch.RequestedSubagentType = "explorer"
		}
		return launch
	}

	launches := make([]taskLaunchSpec, 0, 8)
	if rawLaunches, ok := args["launches"]; ok {
		switch typed := rawLaunches.(type) {
		case []any:
			for _, item := range typed {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				launches = append(launches, parseLaunchSpec(entry))
			}
		}
	}

	if len(launches) == 0 {
		launches = append(launches, parseLaunchSpec(args))
	}

	return taskCallArguments{
		Action:          action,
		Description:     description,
		Prompt:          prompt,
		Launches:        launches,
		ReportMaxChars:  reportMaxChars,
		AllowBash:       mapBool(args, "allow_bash"),
		SourceArguments: args,
	}, nil
}

func effectiveTaskChildMode(sessionMode string) string {
	childMode := sessionruntime.NormalizeMode(sessionMode)
	if childMode == sessionruntime.ModePlan {
		childMode = sessionruntime.ModeAuto
	}
	return childMode
}

func taskDisabledToolNames(allowBash bool) []string {
	disabled := taskDisabledTools(allowBash)
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

func (s *Service) permissionArgumentsForCall(sessionID, sessionMode string, call tool.Call) string {
	arguments := strings.TrimSpace(call.Arguments)
	switch canonicalToolName(call.Name) {
	case "task":
		payload, err := s.buildTaskLaunchPermissionPayload(sessionID, sessionMode, call)
		if err != nil {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	case "manage_skill":
		payload, err := s.buildManageSkillPermissionPayload(sessionID, call)
		if err != nil {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	case "manage_agent":
		payload, err := s.buildManageAgentPermissionPayload(sessionID, call)
		if err != nil {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	case "manage_theme":
		payload, err := s.buildManageThemePermissionPayload(sessionID, call)
		if err != nil {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	case "plan_manage":
		payload, ok, err := s.buildPlanManagePermissionPayload(sessionID, call)
		if err != nil || !ok {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	case "manage_worktree":
		return arguments
	case "manage_todos":
		payload, err := s.buildManageTodosPermissionPayload(sessionID, call)
		if err != nil {
			return arguments
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return arguments
		}
		return string(raw)
	default:
		return arguments
	}
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
	case "upsert", "set", "update", "edit", "write-active", "write_active":
		action = "save"
	}
	if action != "save" {
		return planManagePermissionPayload{}, false, nil
	}
	planBody := strings.TrimSpace(mapString(args, "plan"))
	if planBody == "" {
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
	payload := planManagePermissionPayload{
		PathID:        "tool.plan-manage-update.v1",
		Title:         title,
		PlanID:        planID,
		PriorTitle:    strings.TrimSpace(existing.Title),
		PriorPlan:     strings.TrimSpace(existing.Plan),
		Plan:          planBody,
		DiffLines:     sessionruntime.BuildPlanDiffLines(existing.Plan, planBody),
		Status:        status,
		ApprovalState: approvalState,
		Activate:      activate,
		Action:        action,
		UpdateType:    "existing_plan",
		ApprovedArguments: map[string]any{
			"action":         action,
			"plan_id":        planID,
			"title":          title,
			"plan":           planBody,
			"status":         status,
			"approval_state": approvalState,
			"activate":       activate,
		},
	}
	return payload, true, nil
}

func (s *Service) buildTaskLaunchPermissionPayload(sessionID, sessionMode string, call tool.Call) (taskLaunchManifest, error) {
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		return taskLaunchManifest{}, err
	}

	parentMode := sessionruntime.NormalizeMode(sessionMode)
	childMode := effectiveTaskChildMode(sessionMode)
	disabledTools := taskDisabledToolNames(parsed.AllowBash)

	launches := make([]taskLaunchManifestRow, 0, len(parsed.Launches))
	resolvedAgentName := ""
	resolvedAgentError := ""
	requestedPrimary := ""
	for i, launch := range parsed.Launches {
		requested := strings.TrimSpace(launch.RequestedSubagentType)
		if requested == "" {
			requested = "explorer"
		}
		if requestedPrimary == "" {
			requestedPrimary = requested
		}
		resolvedName := requested
		resolvedErr := ""
		if s != nil {
			if profile, err := s.resolveTaskSubagent(requested); err == nil {
				if strings.TrimSpace(profile.Name) != "" {
					resolvedName = strings.TrimSpace(profile.Name)
				}
			} else {
				resolvedErr = strings.TrimSpace(err.Error())
			}
		}
		if strings.TrimSpace(resolvedName) == "" {
			resolvedName = "explorer"
		}
		if i == 0 {
			resolvedAgentName = resolvedName
			resolvedAgentError = resolvedErr
		}
		metaPrompt := strings.TrimSpace(launch.MetaPrompt)
		if metaPrompt == "" {
			metaPrompt = fmt.Sprintf("Use the %s role.", resolvedName)
		}
		childTitle := fmt.Sprintf("%s (@%s subagent)", truncateRunes(parsed.Description, 80), strings.TrimSpace(resolvedName))
		launches = append(launches, taskLaunchManifestRow{
			Description:           parsed.Description,
			RequestedSubagentType: requested,
			ResolvedAgentName:     resolvedName,
			ResolvedAgentError:    resolvedErr,
			Action:                parsed.Action,
			AllowBash:             parsed.AllowBash,
			ReportMaxChars:        parsed.ReportMaxChars,
			MetaPrompt:            metaPrompt,
			ChildTitlePreview:     childTitle,
			ChildMode:             childMode,
			DisabledTools:         disabledTools,
			Capabilities: map[string]any{
				"allow_bash":            parsed.AllowBash,
				"disabled_tools":        disabledTools,
				"effective_child_mode":  childMode,
				"permission_session_id": strings.TrimSpace(sessionID),
			},
			SourceArguments: cloneGenericMap(launch.SourceArguments),
		})
	}
	if len(launches) == 0 {
		return taskLaunchManifest{}, fmt.Errorf("task requires at least one launch")
	}
	if strings.TrimSpace(resolvedAgentName) == "" {
		resolvedAgentName = "explorer"
	}
	if strings.TrimSpace(requestedPrimary) == "" {
		requestedPrimary = "explorer"
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
		AllowBash:          parsed.AllowBash,
		ReportMaxChars:     parsed.ReportMaxChars,
		ParentMode:         parentMode,
		EffectiveChildMode: childMode,
		DisabledTools:      disabledTools,
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
	return tool.WorkspaceScope{PrimaryPath: primaryPath, Roots: roots, SessionID: strings.TrimSpace(session.ID)}
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
	if _, ok := args["plan"]; !ok {
		return nil
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

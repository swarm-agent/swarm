package run

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	RunTargetKindAgent      = "agent"
	RunTargetKindSubagent   = "subagent"
	RunTargetKindBackground = "background"

	RunWorktreeModeInherit = "inherit"
	RunWorktreeModeOn      = "on"
	RunWorktreeModeOff     = "off"
)

type RunToolScope struct {
	Preset        string   `json:"preset,omitempty"`
	AllowTools    []string `json:"allow_tools,omitempty"`
	DenyTools     []string `json:"deny_tools,omitempty"`
	BashPrefixes  []string `json:"bash_prefixes,omitempty"`
	InheritPolicy bool     `json:"inherit_policy,omitempty"`
}

type RunExecutionContext struct {
	WorkspacePath      string `json:"workspace_path,omitempty"`
	CWD                string `json:"cwd,omitempty"`
	WorktreeMode       string `json:"worktree_mode,omitempty"`
	WorktreeRootPath   string `json:"worktree_root_path,omitempty"`
	WorktreeBranch     string `json:"worktree_branch,omitempty"`
	WorktreeBaseBranch string `json:"worktree_base_branch,omitempty"`
}

type RunRequest struct {
	Prompt           string               `json:"prompt,omitempty"`
	AgentName        string               `json:"agent_name,omitempty"`
	Instructions     string               `json:"instructions,omitempty"`
	Compact          bool                 `json:"compact,omitempty"`
	TargetKind       string               `json:"target_kind,omitempty"`
	TargetName       string               `json:"target_name,omitempty"`
	Background       bool                 `json:"background,omitempty"`
	ToolScope        *RunToolScope        `json:"tool_scope,omitempty"`
	ExecutionContext *RunExecutionContext `json:"execution_context,omitempty"`
}

type RunStartMeta struct {
	AllowSubagent       bool
	DisabledTools       map[string]bool
	PermissionSessionID string
	RunID               string
	OwnerTransport      string
	CompiledPolicy      *permission.Policy
}

func (r RunRequest) Normalized() RunRequest {
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.AgentName = strings.TrimSpace(r.AgentName)
	r.Instructions = strings.TrimSpace(r.Instructions)
	r.TargetKind = strings.TrimSpace(r.TargetKind)
	r.TargetName = strings.TrimSpace(r.TargetName)
	if r.ToolScope != nil {
		scope := normalizeRunToolScope(*r.ToolScope)
		if isRunToolScopeZero(scope) {
			r.ToolScope = nil
		} else {
			r.ToolScope = &scope
		}
	}
	if r.ExecutionContext != nil {
		ctx := normalizeRunExecutionContext(*r.ExecutionContext)
		if isRunExecutionContextZero(ctx) {
			r.ExecutionContext = nil
		} else {
			r.ExecutionContext = &ctx
		}
	}
	return r
}

func NewRunOptions(request RunRequest, meta RunStartMeta) RunOptions {
	request = request.Normalized()
	return RunOptions{
		Prompt:              request.Prompt,
		AgentName:           request.AgentName,
		Instructions:        request.Instructions,
		Compact:             request.Compact,
		AllowSubagent:       meta.AllowSubagent,
		DisabledTools:       cloneDisabledTools(meta.DisabledTools),
		PermissionSessionID: strings.TrimSpace(meta.PermissionSessionID),
		RunID:               strings.TrimSpace(meta.RunID),
		TargetKind:          request.TargetKind,
		TargetName:          request.TargetName,
		Background:          request.Background,
		OwnerTransport:      strings.TrimSpace(meta.OwnerTransport),
		ToolScope:           request.ToolScope,
		CompiledPolicy:      meta.CompiledPolicy,
		ExecutionContext:    request.ExecutionContext,
	}
}

type resolvedRunExecutionContext struct {
	Scope              tool.WorkspaceScope
	WorkspacePath      string
	CWD                string
	WorktreeMode       string
	WorktreeRootPath   string
	WorktreeBranch     string
	WorktreeBaseBranch string
}

func NormalizeRunTargetKind(raw string) string {
	return normalizeRunTargetKind(raw)
}

func NormalizeRunWorktreeMode(raw string) string {
	return normalizeRunWorktreeMode(raw)
}

func normalizeRunTargetKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", RunTargetKindAgent:
		return RunTargetKindAgent
	case RunTargetKindSubagent:
		return RunTargetKindSubagent
	case RunTargetKindBackground:
		return RunTargetKindBackground
	default:
		return ""
	}
}

func normalizeRunWorktreeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", RunWorktreeModeInherit:
		return RunWorktreeModeInherit
	case RunWorktreeModeOn:
		return RunWorktreeModeOn
	case RunWorktreeModeOff:
		return RunWorktreeModeOff
	default:
		return ""
	}
}

func normalizeRunToolNameSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := canonicalToolName(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func normalizeRunStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeRunToolScope(scope RunToolScope) RunToolScope {
	scope.Preset = strings.ToLower(strings.TrimSpace(scope.Preset))
	if scope.Preset == "custom" {
		scope.Preset = ""
	}
	scope.AllowTools = normalizeRunToolNameSlice(scope.AllowTools)
	scope.DenyTools = normalizeRunToolNameSlice(scope.DenyTools)
	scope.BashPrefixes = normalizeRunStringSlice(scope.BashPrefixes)
	return scope
}

func normalizeRunExecutionContext(ctx RunExecutionContext) RunExecutionContext {
	ctx.WorkspacePath = strings.TrimSpace(ctx.WorkspacePath)
	ctx.CWD = strings.TrimSpace(ctx.CWD)
	ctx.WorktreeMode = strings.TrimSpace(ctx.WorktreeMode)
	ctx.WorktreeRootPath = strings.TrimSpace(ctx.WorktreeRootPath)
	ctx.WorktreeBranch = strings.TrimSpace(ctx.WorktreeBranch)
	ctx.WorktreeBaseBranch = strings.TrimSpace(ctx.WorktreeBaseBranch)
	return ctx
}

func isRunToolScopeZero(scope RunToolScope) bool {
	return strings.TrimSpace(scope.Preset) == "" && len(scope.AllowTools) == 0 && len(scope.DenyTools) == 0 && len(scope.BashPrefixes) == 0 && !scope.InheritPolicy
}

func isRunExecutionContextZero(ctx RunExecutionContext) bool {
	return strings.TrimSpace(ctx.WorkspacePath) == "" && strings.TrimSpace(ctx.CWD) == "" && strings.TrimSpace(ctx.WorktreeMode) == "" && strings.TrimSpace(ctx.WorktreeRootPath) == "" && strings.TrimSpace(ctx.WorktreeBranch) == "" && strings.TrimSpace(ctx.WorktreeBaseBranch) == ""
}

func (s *Service) resolveRunTarget(options RunOptions) (targetKind, targetName, agentName string, err error) {
	targetKind = normalizeRunTargetKind(options.TargetKind)
	targetName = strings.TrimSpace(options.TargetName)
	agentName = strings.TrimSpace(options.AgentName)
	if targetKind == "" {
		return "", "", "", fmt.Errorf("unsupported target_kind %q", strings.TrimSpace(options.TargetKind))
	}
	switch targetKind {
	case RunTargetKindSubagent:
		if targetName != "" {
			agentName = targetName
		}
		if agentName == "" {
			return "", "", "", fmt.Errorf("target_name is required for target_kind=%s", RunTargetKindSubagent)
		}
	case RunTargetKindBackground:
		if targetName != "" {
			agentName = targetName
		}
		if agentName == "" {
			return "", "", "", fmt.Errorf("target_name is required for target_kind=%s", RunTargetKindBackground)
		}
	case RunTargetKindAgent:
		if targetName != "" {
			agentName = targetName
		}
		if agentName == "" {
			agentName = "swarm"
		}
		if targetName == "" {
			targetName = agentName
		}
	default:
		return "", "", "", fmt.Errorf("unsupported target_kind %q", strings.TrimSpace(options.TargetKind))
	}
	return targetKind, targetName, agentName, nil
}

func (s *Service) compileRunToolScope(scope RunToolScope) (*permission.Policy, map[string]bool, error) {
	scope = normalizeRunToolScope(scope)
	if isRunToolScopeZero(scope) {
		return nil, nil, nil
	}

	allowSet := make(map[string]struct{})
	denySet := make(map[string]struct{})
	allowBashByPrefix := false
	fullBashAllowed := false
	bashPrefixDecision := permission.PolicyDecisionAsk

	applyToolList := func(target map[string]struct{}, values []string) {
		for _, name := range values {
			if name == "" {
				continue
			}
			target[name] = struct{}{}
		}
	}

	switch scope.Preset {
	case "":
	case "read_only":
		applyToolList(allowSet, []string{"read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"})
		applyToolList(denySet, []string{"write", "edit", "bash", "task"})
	case "read_write":
		applyToolList(allowSet, []string{"read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"})
		applyToolList(denySet, []string{"bash", "task"})
	case "bash_git_only":
		applyToolList(allowSet, []string{"read", "search", "list", "bash", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"})
		applyToolList(denySet, []string{"write", "edit", "task"})
		if len(scope.BashPrefixes) == 0 {
			scope.BashPrefixes = []string{"git status", "git diff", "git log", "git show"}
		}
	case "background_commit":
		applyToolList(allowSet, []string{"read", "search", "list", "git_status", "git_diff", "git_add", "git_commit"})
		applyToolList(denySet, []string{"write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode", "task"})
	default:
		return nil, nil, fmt.Errorf("unsupported tool_scope preset %q", scope.Preset)
	}

	applyToolList(allowSet, scope.AllowTools)
	applyToolList(denySet, scope.DenyTools)

	for denied := range denySet {
		delete(allowSet, denied)
	}
	if _, denied := denySet["bash"]; !denied {
		if len(scope.BashPrefixes) > 0 {
			allowBashByPrefix = true
		} else if _, ok := allowSet["bash"]; ok {
			fullBashAllowed = true
		}
	}
	if allowBashByPrefix {
		delete(allowSet, "bash")
	}

	knownTools := s.knownRunToolNames()
	if len(knownTools) == 0 {
		return nil, nil, errors.New("tool runtime is not configured")
	}

	rules := make([]permission.PolicyRule, 0, len(scope.BashPrefixes)+len(knownTools))
	if allowBashByPrefix {
		for _, prefix := range scope.BashPrefixes {
			rules = append(rules, permission.PolicyRule{
				Kind:     permission.PolicyRuleKindBashPrefix,
				Decision: bashPrefixDecision,
				Tool:     "bash",
				Pattern:  prefix,
			})
		}
	}

	restrictToAllowSet := scope.Preset != "" || len(scope.AllowTools) > 0 || len(scope.BashPrefixes) > 0
	disabled := make(map[string]bool, len(knownTools))
	for name := range knownTools {
		_, allowed := allowSet[name]
		_, denied := denySet[name]
		switch {
		case name == "bash" && allowBashByPrefix:
			rules = append(rules, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: name})
		case denied:
			rules = append(rules, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: name})
			disabled[name] = true
		case name == "bash" && fullBashAllowed:
		case restrictToAllowSet && !allowed:
			rules = append(rules, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: name})
			disabled[name] = true
		default:
		}
	}
	if fullBashAllowed || allowBashByPrefix {
		delete(disabled, "bash")
	}

	compiled := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: rules})
	if len(disabled) == 0 {
		disabled = nil
	}
	return &compiled, disabled, nil
}

func (s *Service) compileAgentToolScope(profile pebblestore.AgentProfile) (*permission.Policy, map[string]bool, error) {
	_, compiled, disabled, err := s.ResolveAgentToolContract(profile)
	if err != nil {
		return nil, nil, err
	}
	return compiled, disabled, nil
}

func mergePermissionPolicies(parts ...*permission.Policy) permission.Policy {
	merged := permission.Policy{Version: 1}
	for _, part := range parts {
		if part == nil {
			continue
		}
		merged.Rules = append(merged.Rules, part.Rules...)
	}
	return permission.NormalizePolicy(merged)
}

func (s *Service) knownRunToolNames() map[string]struct{} {
	known := map[string]struct{}{
		"ask_user":       {},
		"exit_plan_mode": {},
		"plan_manage":    {},
		"task":           {},
	}
	if s == nil || s.tools == nil {
		if customTools := s.customAgentToolNameSet(); len(customTools) > 0 {
			for name := range customTools {
				known[name] = struct{}{}
			}
		}
		return known
	}
	for _, definition := range s.tools.Definitions() {
		name := canonicalToolName(definition.Name)
		if name == "" {
			continue
		}
		known[name] = struct{}{}
	}
	if customTools := s.customAgentToolNameSet(); len(customTools) > 0 {
		for name := range customTools {
			known[name] = struct{}{}
		}
	}
	return known
}

func (s *Service) resolveRunExecutionContext(session pebblestore.SessionSnapshot, requested RunExecutionContext) (resolvedRunExecutionContext, error) {
	requested.WorktreeMode = normalizeRunWorktreeMode(requested.WorktreeMode)
	if requested.WorktreeMode == "" {
		return resolvedRunExecutionContext{}, fmt.Errorf("unsupported worktree_mode %q", strings.TrimSpace(requested.WorktreeMode))
	}

	workspacePath := strings.TrimSpace(session.WorkspacePath)
	worktreeEnabled := session.WorktreeEnabled
	worktreeRootPath := strings.TrimSpace(session.WorktreeRootPath)
	worktreeBranch := strings.TrimSpace(session.WorktreeBranch)
	worktreeBaseBranch := strings.TrimSpace(session.WorktreeBaseBranch)

	switch requested.WorktreeMode {
	case RunWorktreeModeOff:
		if path := strings.TrimSpace(requested.WorkspacePath); path != "" {
			workspacePath = path
		} else if worktreeEnabled {
			if worktreeRootPath == "" {
				return resolvedRunExecutionContext{}, errors.New("worktree-backed session is missing worktree_root_path")
			}
			workspacePath = worktreeRootPath
		}
		worktreeEnabled = false
		worktreeRootPath = ""
		worktreeBranch = ""
		worktreeBaseBranch = ""
	case RunWorktreeModeOn:
		if path := strings.TrimSpace(requested.WorkspacePath); path != "" {
			workspacePath = path
		}
		if root := strings.TrimSpace(requested.WorktreeRootPath); root != "" {
			worktreeEnabled = true
			worktreeRootPath = root
		} else if !worktreeEnabled {
			return resolvedRunExecutionContext{}, errors.New("worktree_mode=on requires a worktree-backed source session or explicit worktree_root_path")
		} else if path := strings.TrimSpace(requested.WorkspacePath); path != "" {
			current, currentErr := normalizeRunScopePath(session.WorkspacePath)
			requestedPath, requestedErr := normalizeRunScopePath(path)
			if currentErr != nil || requestedErr != nil || !strings.EqualFold(current, requestedPath) {
				return resolvedRunExecutionContext{}, errors.New("worktree_mode=on with an overridden workspace_path requires explicit worktree_root_path")
			}
		}
		if path := strings.TrimSpace(requested.WorktreeBranch); path != "" {
			worktreeBranch = path
		}
		if path := strings.TrimSpace(requested.WorktreeBaseBranch); path != "" {
			worktreeBaseBranch = path
		}
	case RunWorktreeModeInherit:
		if path := strings.TrimSpace(requested.WorkspacePath); path != "" {
			if worktreeEnabled && strings.TrimSpace(requested.WorktreeRootPath) == "" {
				current, currentErr := normalizeRunScopePath(session.WorkspacePath)
				requestedPath, requestedErr := normalizeRunScopePath(path)
				if currentErr != nil || requestedErr != nil || !strings.EqualFold(current, requestedPath) {
					return resolvedRunExecutionContext{}, errors.New("overriding workspace_path for a worktree-backed inherited run requires explicit worktree_root_path or worktree_mode=off")
				}
			}
			workspacePath = path
		}
		if root := strings.TrimSpace(requested.WorktreeRootPath); root != "" {
			worktreeEnabled = true
			worktreeRootPath = root
		}
		if path := strings.TrimSpace(requested.WorktreeBranch); path != "" {
			worktreeBranch = path
		}
		if path := strings.TrimSpace(requested.WorktreeBaseBranch); path != "" {
			worktreeBaseBranch = path
		}
	}

	resolvedWorkspacePath, err := normalizeRunScopePath(workspacePath)
	if err != nil {
		return resolvedRunExecutionContext{}, err
	}
	baseRoots, err := s.resolveExecutionRoots(resolvedWorkspacePath, worktreeEnabled, session.TemporaryWorkspaceRoots)
	if err != nil {
		return resolvedRunExecutionContext{}, err
	}

	cwd := strings.TrimSpace(requested.CWD)
	if cwd == "" {
		cwd = resolvedWorkspacePath
	}
	resolvedCWD, err := normalizeRunScopePath(cwd)
	if err != nil {
		return resolvedRunExecutionContext{}, err
	}
	if !runPathWithinRoots(baseRoots, resolvedCWD) {
		return resolvedRunExecutionContext{}, fmt.Errorf("cwd %q escapes effective workspace scope", strings.TrimSpace(requested.CWD))
	}

	roots := normalizeExecutionRoots(resolvedCWD, baseRoots)
	effectiveWorktreeMode := RunWorktreeModeOff
	if worktreeEnabled {
		effectiveWorktreeMode = RunWorktreeModeOn
	}
	return resolvedRunExecutionContext{
		Scope: tool.WorkspaceScope{
			PrimaryPath: resolvedCWD,
			Roots:       roots,
		},
		WorkspacePath:      resolvedWorkspacePath,
		CWD:                resolvedCWD,
		WorktreeMode:       effectiveWorktreeMode,
		WorktreeRootPath:   worktreeRootPath,
		WorktreeBranch:     worktreeBranch,
		WorktreeBaseBranch: worktreeBaseBranch,
	}, nil
}

func (s *Service) resolveExecutionRoots(workspacePath string, worktreeEnabled bool, temporaryRoots []string) ([]string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	if worktreeEnabled {
		return normalizeExecutionRoots(workspacePath, mergeSessionWorkspaceRoots([]string{workspacePath}, temporaryRoots)), nil
	}
	if s != nil && s.workspace != nil {
		resolved, err := s.workspace.ScopeForPath(workspacePath)
		if err == nil {
			return normalizeExecutionRoots(workspacePath, mergeSessionWorkspaceRoots(resolved.Directories, temporaryRoots)), nil
		}
	}
	return normalizeExecutionRoots(workspacePath, mergeSessionWorkspaceRoots([]string{workspacePath}, temporaryRoots)), nil
}

func normalizeExecutionRoots(primary string, roots []string) []string {
	out := make([]string, 0, len(roots)+1)
	seen := make(map[string]struct{}, len(roots)+1)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	add(primary)
	for _, root := range roots {
		add(root)
	}
	return out
}

func runPathWithinRoots(roots []string, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func (s *Service) effectiveRunOwnerTransport(options RunOptions, onEvent StreamHandler) string {
	ownerTransport := strings.TrimSpace(options.OwnerTransport)
	if ownerTransport != "" {
		return ownerTransport
	}
	if options.Background {
		return "background_api"
	}
	return normalizeLifecycleTransport(onEvent)
}

func (o RunOptions) ExecutionContextOrDefault() RunExecutionContext {
	if o.ExecutionContext == nil {
		return RunExecutionContext{WorktreeMode: RunWorktreeModeInherit}
	}
	ctx := normalizeRunExecutionContext(*o.ExecutionContext)
	if strings.TrimSpace(ctx.WorktreeMode) == "" {
		ctx.WorktreeMode = RunWorktreeModeInherit
	}
	return ctx
}

func cloneDisabledTools(input map[string]bool) map[string]bool {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]bool, len(input))
	for key, value := range input {
		out[canonicalToolName(key)] = value
	}
	return out
}

func mergeDisabledTools(base, extra map[string]bool) map[string]bool {
	if len(extra) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]bool, len(extra))
	}
	for key, value := range extra {
		base[canonicalToolName(key)] = value
	}
	return base
}

func buildBackgroundRunMetadata(existing map[string]any, targetKind, targetName string, ctx resolvedRunExecutionContext) map[string]any {
	metadata := cloneGenericMap(existing)
	if metadata == nil {
		metadata = make(map[string]any, 6)
	}
	metadata["launch_mode"] = "background"
	metadata["background"] = true
	if !metadataMarksFlowRun(metadata) {
		metadata["target_kind"] = strings.TrimSpace(targetKind)
		metadata["target_name"] = strings.TrimSpace(targetName)
	}
	metadata["execution_context"] = map[string]any{
		"workspace_path":       strings.TrimSpace(ctx.WorkspacePath),
		"cwd":                  strings.TrimSpace(ctx.CWD),
		"worktree_mode":        strings.TrimSpace(ctx.WorktreeMode),
		"worktree_root_path":   strings.TrimSpace(ctx.WorktreeRootPath),
		"worktree_branch":      strings.TrimSpace(ctx.WorktreeBranch),
		"worktree_base_branch": strings.TrimSpace(ctx.WorktreeBaseBranch),
	}
	return metadata
}

func metadataMarksFlowRun(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	return strings.EqualFold(metadataText(metadata, "source"), "flow") ||
		strings.EqualFold(metadataText(metadata, "lineage_kind"), "flow") ||
		strings.EqualFold(metadataText(metadata, "owner_transport"), "flow_scheduler") ||
		metadataText(metadata, "flow_id") != ""
}

func metadataText(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

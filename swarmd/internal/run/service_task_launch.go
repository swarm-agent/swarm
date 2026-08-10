package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	taskLaunchPermissionPathID = "permission.task_launch.v1"
	taskModeRegular            = "regular"
	taskModeSwarm              = "swarm"
	taskSwarmMaxAgents         = 256
)

type taskCallArguments struct {
	Action          string
	Description     string
	Prompt          string
	Mode            string
	Swarm           *taskSwarmSpec
	Launches        []taskLaunchSpec
	SourceArguments map[string]any
}

type taskSwarmGroup struct {
	Name         string `json:"name"`
	Count        int    `json:"count"`
	Instructions string `json:"instructions,omitempty"`
}

type taskSwarmSpec struct {
	AgentType          string
	Count              int
	Themes             []string
	Groups             []taskSwarmGroup
	OutputContract     string
	OwnedScopeTemplate string
}

type taskLaunchSpec struct {
	RequestedSubagentType string
	MetaPrompt            string
	AssignmentLabel       string
	Deliverable           string
	ConcurrencyReason     string
	OwnedScope            []string
	DependencyEvidence    string
	StreamKey             string
	SwarmMode             bool
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
	ParentMode          string                         `json:"parent_mode"`
	EffectiveChildMode  string                         `json:"effective_child_mode"`
	DisabledTools       []string                       `json:"disabled_tools,omitempty"`
	ResolvedTools       *taskLaunchResolvedToolSummary `json:"resolved_tools,omitempty"`
	TargetWorkspacePath string                         `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName string                         `json:"target_workspace_name,omitempty"`
	SourceArguments     map[string]any                 `json:"source_arguments,omitempty"`
	Parent              *taskLaunchParentInfo          `json:"parent,omitempty"`
	Launches            []taskLaunchManifestRow        `json:"launches,omitempty"`
	TaskMode            string                         `json:"task_mode,omitempty"`
	SwarmAgentType      string                         `json:"swarm_agent_type,omitempty"`
	ManifestHash        string                         `json:"manifest_hash"`
	ApprovedArguments   map[string]any                 `json:"approved_arguments,omitempty"`
}

type planManagePermissionPayload struct {
	PathID                   string              `json:"path_id,omitempty"`
	Title                    string              `json:"title,omitempty"`
	PlanID                   string              `json:"plan_id,omitempty"`
	PriorTitle               string              `json:"prior_title,omitempty"`
	PriorPlan                string              `json:"prior_plan,omitempty"`
	Plan                     string              `json:"plan,omitempty"`
	DiffLines                []string            `json:"diff_lines,omitempty"`
	Document                 any                 `json:"document,omitempty"`
	PriorDocument            any                 `json:"prior_document,omitempty"`
	Version                  int                 `json:"version,omitempty"`
	Revision                 int                 `json:"revision,omitempty"`
	CurrentRevision          int                 `json:"current_revision,omitempty"`
	BaseRevision             int                 `json:"base_revision,omitempty"`
	PlanAmendmentDelta       *planAmendmentDelta `json:"plan_amendment_delta,omitempty"`
	Status                   string              `json:"status,omitempty"`
	ApprovalState            string              `json:"approval_state,omitempty"`
	Activate                 bool                `json:"activate,omitempty"`
	Action                   string              `json:"action,omitempty"`
	UpdateType               string              `json:"update_type,omitempty"`
	UpdateSummary            string              `json:"update_summary,omitempty"`
	UpdateScope              string              `json:"update_scope,omitempty"`
	UpdateKind               string              `json:"update_kind,omitempty"`
	DocumentOperation        string              `json:"document_operation,omitempty"`
	Checkpoint               bool                `json:"checkpoint,omitempty"`
	ChangeRequest            string              `json:"change_request,omitempty"`
	CheckpointTitle          string              `json:"checkpoint_title,omitempty"`
	Tasks                    []string            `json:"tasks,omitempty"`
	AcceptanceCriteria       []string            `json:"acceptance_criteria,omitempty"`
	Notes                    string              `json:"notes,omitempty"`
	FollowupCheckpointPolicy string              `json:"followup_checkpoint_policy,omitempty"`
	PolicyEffective          string              `json:"policy_effective,omitempty"`
	ApprovalRequired         bool                `json:"approval_required,omitempty"`
	RunQueued                bool                `json:"run_queued,omitempty"`
	ApprovedArguments        map[string]any      `json:"approved_arguments,omitempty"`
}

type planAmendmentDelta struct {
	Reason                  string                    `json:"reason,omitempty"`
	BaseRevision            int                       `json:"base_revision,omitempty"`
	CurrentRevision         int                       `json:"current_revision,omitempty"`
	OverrideStale           bool                      `json:"override_stale,omitempty"`
	ReplaceFromCheckpointID string                    `json:"replace_from_checkpoint_id,omitempty"`
	PreservedCheckpoints    []planCheckpointDeltaItem `json:"preserved_checkpoints,omitempty"`
	ReplacedCheckpoints     []planCheckpointDeltaItem `json:"replaced_checkpoints,omitempty"`
	ReplacementCheckpoints  []planCheckpointDeltaItem `json:"replacement_checkpoints,omitempty"`
	NextCheckpoint          *planCheckpointDeltaItem  `json:"next_checkpoint,omitempty"`
	Bullets                 []string                  `json:"bullets,omitempty"`
}

type planCheckpointDeltaItem struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
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
	Description           string                                   `json:"description"`
	RequestedSubagentType string                                   `json:"requested_subagent_type"`
	ResolvedAgentName     string                                   `json:"resolved_agent_name"`
	ResolvedAgentError    string                                   `json:"resolved_agent_error,omitempty"`
	Action                string                                   `json:"action"`
	MetaPrompt            string                                   `json:"meta_prompt,omitempty"`
	AssignmentLabel       string                                   `json:"assignment_label,omitempty"`
	Deliverable           string                                   `json:"deliverable,omitempty"`
	ConcurrencyReason     string                                   `json:"concurrency_reason,omitempty"`
	OwnedScope            []string                                 `json:"owned_scope,omitempty"`
	DependencyEvidence    string                                   `json:"dependency_evidence,omitempty"`
	SubagentProvider      string                                   `json:"subagent_provider,omitempty"`
	SubagentModel         string                                   `json:"subagent_model,omitempty"`
	SubagentThinking      string                                   `json:"subagent_thinking,omitempty"`
	SubagentServiceTier   string                                   `json:"subagent_service_tier,omitempty"`
	ChildTitlePreview     string                                   `json:"child_title_preview,omitempty"`
	ChildMode             string                                   `json:"effective_child_mode"`
	DisabledTools         []string                                 `json:"disabled_tools,omitempty"`
	ResolvedTools         *taskLaunchResolvedToolSummary           `json:"resolved_tools,omitempty"`
	Capabilities          map[string]any                           `json:"capabilities,omitempty"`
	TargetWorkspacePath   string                                   `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName   string                                   `json:"target_workspace_name,omitempty"`
	SourceArguments       map[string]any                           `json:"source_arguments,omitempty"`
	ParentCopy            bool                                     `json:"parent_copy,omitempty"`
	SourceAgentName       string                                   `json:"source_agent_name,omitempty"`
	SourceProfileMode     string                                   `json:"source_profile_mode,omitempty"`
	InheritedRuntimeMode  string                                   `json:"inherited_runtime_mode,omitempty"`
	ProfileSnapshot       *pebblestore.AgentProfile                `json:"profile_snapshot,omitempty"`
	ModelProfileSnapshot  *pebblestore.SessionModelProfileSnapshot `json:"model_profile_snapshot"`
	StreamKey             string                                   `json:"stream_key,omitempty"`
	SwarmMode             bool                                     `json:"swarm_mode,omitempty"`
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

	mode := strings.ToLower(strings.TrimSpace(mapString(args, "mode")))
	if mapBool(args, "swarm_mode") {
		if mode != "" && mode != taskModeSwarm {
			return taskCallArguments{}, errors.New("task mode conflicts with swarm_mode")
		}
		mode = taskModeSwarm
	}
	if mode == "" {
		mode = taskModeRegular
	}
	if mode != taskModeRegular && mode != taskModeSwarm {
		return taskCallArguments{}, fmt.Errorf("task mode must be %q or %q", taskModeRegular, taskModeSwarm)
	}

	parseLaunchSpec := func(raw map[string]any, label string) (taskLaunchSpec, error) {
		if err := rejectTaskLaunchTrustFields(raw, label); err != nil {
			return taskLaunchSpec{}, err
		}
		ownedScope, err := parseTaskOwnedScope(raw, label)
		if err != nil {
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
				mapString(raw, "title"),
				mapString(raw, "assignment_label"),
				mapString(raw, "label"),
			)),
			Deliverable:        strings.TrimSpace(mapString(raw, "deliverable")),
			ConcurrencyReason:  strings.TrimSpace(mapString(raw, "concurrency_reason")),
			OwnedScope:         ownedScope,
			DependencyEvidence: strings.TrimSpace(mapString(raw, "dependency_evidence")),
			SourceArguments:    cloneGenericMap(raw),
		}
		if launch.RequestedSubagentType == "" {
			return taskLaunchSpec{}, fmt.Errorf("%s requires subagent_type, agent, or purpose", label)
		}
		switch {
		case agentruntime.IsCoderAgentName(launch.RequestedSubagentType):
			launch.RequestedSubagentType = "coder"
		case agentruntime.IsFinderAgentName(launch.RequestedSubagentType):
			launch.RequestedSubagentType = "finder"
		case agentruntime.IsDesignerAgentName(launch.RequestedSubagentType):
			launch.RequestedSubagentType = "designer"
		default:
			return taskLaunchSpec{}, fmt.Errorf("%s subagent_type must be coder, finder, or designer; Idea is available only through task mode=swarm", label)
		}
		if launch.MetaPrompt == "" {
			return taskLaunchSpec{}, fmt.Errorf("%s requires meta_prompt or role assignment", label)
		}
		applyCanonicalCoderOwnedScope(&launch)
		return launch, nil
	}

	if mode == taskModeSwarm {
		swarm, launches, err := parseTaskSwarmArguments(args, prompt, description)
		if err != nil {
			return taskCallArguments{}, err
		}
		return taskCallArguments{
			Action: action, Description: description, Prompt: prompt, Mode: mode,
			Swarm: swarm, Launches: launches, SourceArguments: args,
		}, nil
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
	if err := validateTaskDesignerScopes(launches); err != nil {
		return taskCallArguments{}, err
	}

	return taskCallArguments{
		Action:          action,
		Description:     description,
		Prompt:          prompt,
		Mode:            mode,
		Launches:        launches,
		SourceArguments: args,
	}, nil
}

func parseTaskSwarmArguments(args map[string]any, prompt, description string) (*taskSwarmSpec, []taskLaunchSpec, error) {
	allowed := map[string]bool{
		"action": true, "description": true, "prompt": true, "message": true, "mode": true, "swarm_mode": true,
		"agent_type": true, "subagent_type": true, "agent": true, "purpose": true, "count": true,
		"themes": true, "groups": true, "output_contract": true, "owned_scope_template": true, "launches": true,
	}
	for key := range args {
		if !allowed[key] {
			return nil, nil, fmt.Errorf("task swarm mode contains unsupported field %q", key)
		}
	}
	if _, ok := args["launches"]; ok {
		return nil, nil, errors.New("task swarm mode generates its launch wave; launches must be omitted")
	}
	agentType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(mapString(args, "agent_type"), mapString(args, "subagent_type"), mapString(args, "agent"), mapString(args, "purpose"))))
	switch {
	case agentruntime.IsCoderAgentName(agentType):
		agentType = "coder"
	case agentruntime.IsDesignerAgentName(agentType):
		agentType = "designer"
	case agentruntime.IsIdeaAgentName(agentType):
		agentType = "idea"
	default:
		return nil, nil, errors.New("task swarm mode agent_type must be coder, designer, or idea")
	}
	count, err := taskPositiveInt(args, "count")
	if err != nil {
		return nil, nil, err
	}
	if count > taskSwarmMaxAgents {
		return nil, nil, fmt.Errorf("task swarm mode count cannot exceed %d", taskSwarmMaxAgents)
	}
	themes, err := taskStringArray(args, "themes")
	if err != nil {
		return nil, nil, err
	}
	if len(themes) != 0 && len(themes) != count {
		return nil, nil, fmt.Errorf("task swarm mode themes must be omitted or contain exactly %d entries", count)
	}
	groups, err := taskSwarmGroups(args, count)
	if err != nil {
		return nil, nil, err
	}
	outputContract := strings.TrimSpace(mapString(args, "output_contract"))
	if outputContract == "" {
		outputContract = strings.TrimSpace(description)
	}
	ownedScopeTemplate := strings.TrimSpace(mapString(args, "owned_scope_template"))
	if agentType == "designer" && ownedScopeTemplate == "" {
		return nil, nil, errors.New("task Designer swarm requires owned_scope_template with exactly one {index} placeholder")
	}
	if ownedScopeTemplate != "" && strings.Count(ownedScopeTemplate, "{index}") != 1 {
		return nil, nil, errors.New("task swarm owned_scope_template must contain exactly one {index} placeholder")
	}
	if agentType == "idea" {
		if len(themes) != 0 || len(groups) != 0 || strings.TrimSpace(mapString(args, "output_contract")) != "" || ownedScopeTemplate != "" {
			return nil, nil, errors.New("task Idea swarm accepts only mode, prompt, agent_type, count, and optional description")
		}
	}

	swarm := &taskSwarmSpec{AgentType: agentType, Count: count, Themes: themes, Groups: groups, OutputContract: outputContract, OwnedScopeTemplate: ownedScopeTemplate}
	launches := make([]taskLaunchSpec, count)
	for i := range launches {
		index := i + 1
		ownedScope := []string(nil)
		if ownedScopeTemplate != "" {
			target := strings.Replace(ownedScopeTemplate, "{index}", strconv.Itoa(index), 1)
			if err := validateTaskSwarmOwnedScope(target); err != nil {
				return nil, nil, fmt.Errorf("task swarm owned scope %d: %w", index, err)
			}
			ownedScope = []string{filepath.ToSlash(filepath.Clean(target))}
		}
		metaPrompt := prompt
		if agentType != "idea" {
			metaPrompt = fmt.Sprintf("Pending Router hydration for swarm item %d.", index)
		}
		launches[i] = taskLaunchSpec{
			RequestedSubagentType: agentType,
			MetaPrompt:            metaPrompt,
			AssignmentLabel:       fmt.Sprintf("%s swarm %d", taskSwarmAgentLabel(agentType), index),
			Deliverable:           outputContract,
			ConcurrencyReason:     "Independent task swarm iteration",
			OwnedScope:            ownedScope,
			DependencyEvidence:    "The shared parent brief is complete before this task swarm wave starts.",
			StreamKey:             fmt.Sprintf("swarm:%d", index),
			SwarmMode:             true,
			SourceArguments:       map[string]any{"swarm_index": index, "swarm_mode": true},
		}
		applyCanonicalCoderOwnedScope(&launches[i])
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return nil, nil, err
	}
	return swarm, launches, nil
}

func taskSwarmAgentLabel(agentType string) string {
	switch agentType {
	case "coder":
		return "Coder"
	case "designer":
		return "Designer"
	case "idea":
		return "Idea"
	default:
		return "Swarm"
	}
}

func taskPositiveInt(args map[string]any, key string) (int, error) {
	value, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("task swarm mode requires %s", key)
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < 1 {
		return 0, fmt.Errorf("task swarm mode %s must be a positive integer", key)
	}
	return int(number), nil
}

func taskStringArray(args map[string]any, key string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	typed, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("task swarm mode %s must be an array of strings", key)
	}
	result := make([]string, 0, len(typed))
	seen := map[string]struct{}{}
	for i, entry := range typed {
		text, ok := entry.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			return nil, fmt.Errorf("task swarm mode %s[%d] must be a non-empty string", key, i)
		}
		normalized := strings.ToLower(text)
		if _, duplicate := seen[normalized]; duplicate {
			return nil, fmt.Errorf("task swarm mode %s[%d] duplicates another value", key, i)
		}
		seen[normalized] = struct{}{}
		result = append(result, text)
	}
	return result, nil
}

func taskSwarmGroups(args map[string]any, count int) ([]taskSwarmGroup, error) {
	value, ok := args["groups"]
	if !ok || value == nil {
		return nil, nil
	}
	typed, ok := value.([]any)
	if !ok || len(typed) == 0 {
		return nil, errors.New("task swarm mode groups must be a non-empty array")
	}
	groups := make([]taskSwarmGroup, 0, len(typed))
	total := 0
	for i, entry := range typed {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("task swarm mode groups[%d] must be an object", i)
		}
		for key := range row {
			if key != "name" && key != "count" && key != "instructions" {
				return nil, fmt.Errorf("task swarm mode groups[%d] contains unsupported field %q", i, key)
			}
		}
		name := strings.TrimSpace(mapString(row, "name"))
		if name == "" {
			return nil, fmt.Errorf("task swarm mode groups[%d] requires name", i)
		}
		groupCount, err := taskPositiveInt(row, "count")
		if err != nil {
			return nil, fmt.Errorf("task swarm mode groups[%d]: %w", i, err)
		}
		groups = append(groups, taskSwarmGroup{Name: name, Count: groupCount, Instructions: strings.TrimSpace(mapString(row, "instructions"))})
		total += groupCount
	}
	if total != count {
		return nil, fmt.Errorf("task swarm mode group counts total %d, want %d", total, count)
	}
	return groups, nil
}

func validateTaskSwarmOwnedScope(scope string) error {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(scope)))
	canonical := filepath.ToSlash(clean)
	if scope == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsAny(scope, "*?[]") || canonical != scope {
		return errors.New("must be a concrete clean workspace-relative path")
	}
	return nil
}

func parseTaskOwnedScope(raw map[string]any, label string) ([]string, error) {
	value, ok := raw["owned_scope"]
	if !ok || value == nil {
		return nil, nil
	}
	var values []string
	switch typed := value.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for i, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("%s owned_scope[%d] must be a string", label, i)
			}
			values = append(values, text)
		}
	default:
		return nil, fmt.Errorf("%s owned_scope must be an array of strings", label)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out, nil
}

func validateTaskDesignerScopes(launches []taskLaunchSpec) error {
	designerIndexes := make([]int, 0, len(launches))
	for i := range launches {
		if !agentruntime.IsDesignerAgentName(launches[i].RequestedSubagentType) {
			continue
		}
		if len(launches[i].OwnedScope) == 0 {
			return fmt.Errorf("task launches[%d] Designer requires a concrete workspace-relative owned_scope or output target", i)
		}
		for j, raw := range launches[i].OwnedScope {
			scope := strings.TrimSpace(raw)
			clean := filepath.Clean(filepath.FromSlash(scope))
			canonical := filepath.ToSlash(clean)
			if scope == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsAny(scope, "*?[]") || canonical != scope {
				return fmt.Errorf("task launches[%d] Designer owned_scope[%d] must be a concrete clean workspace-relative path", i, j)
			}
			launches[i].OwnedScope[j] = canonical
		}
		designerIndexes = append(designerIndexes, i)
	}
	for left := 0; left < len(designerIndexes); left++ {
		for right := left + 1; right < len(designerIndexes); right++ {
			leftIndex, rightIndex := designerIndexes[left], designerIndexes[right]
			if taskOwnedScopesOverlap(launches[leftIndex].OwnedScope, launches[rightIndex].OwnedScope) {
				return fmt.Errorf("Designer owned scopes overlap between launches[%d] and launches[%d]; each concurrent variant requires a distinct output target", leftIndex, rightIndex)
			}
		}
	}
	return nil
}

func applyCanonicalCoderOwnedScope(launch *taskLaunchSpec) {
	if launch == nil || !agentruntime.IsCoderAgentName(launch.RequestedSubagentType) || len(launch.OwnedScope) != 0 {
		return
	}
	// Owned scope is advisory metadata for review and collision detection, not an
	// isolation boundary. When Coder omitted it, conservatively claim the whole
	// isolated worktree rather than rejecting an otherwise valid launch.
	launch.OwnedScope = []string{"."}
}

func normalizedTaskOwnedScope(scope string) string {
	scope = strings.Trim(strings.TrimSpace(scope), "/")
	scope = strings.TrimPrefix(scope, "./")
	scope = strings.TrimSuffix(strings.TrimSuffix(scope, "/**"), "/*")
	if scope == "" || scope == "." || scope == "*" || scope == "**" {
		return "."
	}
	return scope
}

func taskOwnedScopesOverlap(left, right []string) bool {
	for _, leftScope := range left {
		leftScope = normalizedTaskOwnedScope(leftScope)
		for _, rightScope := range right {
			rightScope = normalizedTaskOwnedScope(rightScope)
			if leftScope == "." || rightScope == "." || leftScope == rightScope || strings.HasPrefix(leftScope, rightScope+"/") || strings.HasPrefix(rightScope, leftScope+"/") {
				return true
			}
		}
	}
	return false
}

func rejectMalformedToolCallArguments(call tool.Call) error {
	canonical := canonicalToolName(call.Name)
	if canonical == "bash" {
		return tool.ValidateBashCallArguments(call.Arguments)
	}
	if canonical == "ask_user" {
		return validateAskUserCallArguments(call.Arguments)
	}
	if canonical != "task" {
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
		"runtime_mode",
		"runtimeMode",
		"agent_profile",
		"agentProfile",
		"profile_snapshot",
		"profileSnapshot",
		"manifest_hash",
		"manifestHash",
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
	return sessionruntime.NormalizeMode(sessionMode)
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
	if len(fields) > 3 {
		candidate = strings.Join(fields[:3], " ")
	} else if len(fields) > 0 {
		candidate = strings.Join(fields, " ")
	}
	candidate = truncateRunes(candidate, 48)
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
	case "ask_user":
		return normalizeAskUserPermissionArguments(arguments)
	case "task":
		payload, err := s.buildTaskLaunchPermissionPayload(sessionID, sessionMode, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "manage_skill":
		if !permission.ShouldApproveManageSkillMutation(arguments) {
			return arguments, nil
		}
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
	case "manage_sessions":
		payload, err := s.buildManageSessionsPermissionPayload(sessionID, call)
		if err != nil {
			return "", err
		}
		return marshalPayload(payload)
	case "exit_plan_mode":
		payload, err := s.buildExitPlanModePermissionPayload(sessionID, call)
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

func (s *Service) buildExitPlanModePermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("exit_plan_mode arguments invalid: %w", err)
	}
	document, err := planDocumentFromArgsForTool(args, "exit_plan_mode")
	if err != nil {
		return nil, err
	}
	planID := strings.TrimSpace(firstNonEmptyString(mapString(args, "plan_id"), mapString(args, "planID"), mapString(args, "id")))
	title := strings.TrimSpace(mapString(args, "title"))
	planBody := strings.TrimSpace(mapString(args, "plan"))
	if document != nil {
		if planID == "" {
			planID = strings.TrimSpace(document.ID)
		}
		if title == "" {
			title = strings.TrimSpace(document.Title)
		}
		if planBody == "" {
			planBody = strings.TrimSpace(firstNonEmptyString(document.DisplayText, document.RenderedText))
		}
	}

	var existing *pebblestore.SessionPlanSnapshot
	if s.sessions != nil {
		if planID == "" {
			active, ok, err := s.sessions.GetActivePlan(sessionID)
			if err != nil {
				return nil, err
			}
			if ok {
				planID = strings.TrimSpace(active.ID)
				existing = &active
			}
		} else if current, ok, err := s.sessions.GetPlan(sessionID, planID); err != nil {
			return nil, err
		} else if ok {
			existing = &current
		}
	}
	if existing != nil {
		if title == "" {
			title = strings.TrimSpace(existing.Title)
		}
		if planBody == "" {
			planBody = strings.TrimSpace(existing.Plan)
		}
	}
	if document == nil {
		return nil, errors.New("exit_plan_mode requires an explicit structured document; plan text and an existing saved plan are display context only")
	}
	if document != nil {
		documentClone := *document
		documentClone.ID = strings.TrimSpace(firstNonEmptyString(planID, documentClone.ID))
		documentClone.Title = strings.TrimSpace(firstNonEmptyString(title, documentClone.Title))
		document = &documentClone
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(document); err != nil {
		return nil, err
	}

	executionRecommendation, err := normalizeExitPlanModeExecutionRecommendation(args, document)
	if err != nil {
		return nil, err
	}

	approved := cloneGenericMap(args)
	if approved == nil {
		approved = map[string]any{}
	}
	if title != "" {
		approved["title"] = title
	}
	if strings.TrimSpace(mapString(approved, "title")) == "" {
		delete(approved, "title")
	}
	if planID != "" {
		approved["plan_id"] = planID
	}
	if planBody != "" {
		approved["plan"] = planBody
	}
	if document != nil {
		approved["document"] = document
	}
	if executionRecommendation != nil {
		approved["execution_granularity"] = executionRecommendation.ExecutionGranularity
		approved["continuation_policy"] = executionRecommendation.ContinuationPolicy
		approved["continue_automatically"] = executionRecommendation.ContinueAutomatically
	}
	delete(approved, "approved_arguments")

	payload := map[string]any{
		"path_id":            "permission.exit-plan-mode.v1",
		"tool":               "exit_plan_mode",
		"title":              title,
		"plan_id":            planID,
		"plan":               planBody,
		"document":           document,
		"approved_arguments": approved,
	}
	if executionRecommendation != nil {
		payload["execution_granularity"] = executionRecommendation.ExecutionGranularity
		payload["continuation_policy"] = executionRecommendation.ContinuationPolicy
		payload["continue_automatically"] = executionRecommendation.ContinueAutomatically
		payload["execution_recommendation"] = map[string]any{
			"execution_granularity":  executionRecommendation.ExecutionGranularity,
			"continuation_policy":    executionRecommendation.ContinuationPolicy,
			"continue_automatically": executionRecommendation.ContinueAutomatically,
		}
	}
	if existing != nil {
		payload["prior_title"] = strings.TrimSpace(existing.Title)
		payload["prior_plan"] = strings.TrimSpace(existing.Plan)
		payload["prior_document"] = existing.Document
		payload["version"] = existing.Version
	}
	return payload, nil
}

type exitPlanModeExecutionRecommendation struct {
	ExecutionGranularity  string
	ContinuationPolicy    string
	ContinueAutomatically bool
}

func normalizeExitPlanModeExecutionRecommendation(args map[string]any, document *pebblestore.SessionPlanDocument) (*exitPlanModeExecutionRecommendation, error) {
	if args == nil {
		args = map[string]any{}
	}
	recommended := false
	continuation := strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode")))
	if continuation != "" {
		recommended = true
	}
	if _, ok := args["continue_automatically"]; ok {
		recommended = true
		if mapBool(args, "continue_automatically") {
			continuation = sessionruntime.PlanAcceptanceContinuationAutomatic
		} else {
			continuation = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	if !recommended && document != nil {
		if document.ExecutionPolicy.Shape != "" || document.ExecutionPolicy.Mode != "" {
			recommended = true
		}
		switch document.ExecutionPolicy.Mode {
		case sessionruntime.PlanExecutionPolicyModeAutomatic:
			continuation = sessionruntime.PlanAcceptanceContinuationAutomatic
		case sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint:
			continuation = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
	if !recommended {
		return nil, nil
	}
	policy, err := sessionruntime.NormalizePlanAcceptanceExecutionPolicy(sessionruntime.PlanAcceptanceExecutionOptions{ContinuationPolicy: continuation})
	if err != nil {
		return nil, err
	}
	result := &exitPlanModeExecutionRecommendation{ExecutionGranularity: sessionruntime.PlanAcceptanceGranularityCheckpointed, ContinuationPolicy: sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint}
	if policy.Mode == sessionruntime.PlanExecutionPolicyModeAutomatic {
		result.ContinuationPolicy = sessionruntime.PlanAcceptanceContinuationAutomatic
		result.ContinueAutomatically = true
	}
	return result, nil
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
	case "create-plan", "create_plan", "propose-plan", "propose_plan":
		action = "request_new_plan"
	case "update", "edit":
		if strings.TrimSpace(mapString(args, "plan")) == "" && args["document"] == nil {
			action = "patch"
		} else {
			action = "save"
		}
	case "update-info", "update_info":
		action = "update_info"
	case "update-execution-policy", "update_execution_policy", "set-execution-policy", "set_execution_policy", "execution-policy", "execution_policy":
		action = "update_execution_policy"
	case "update-execution-state", "update_execution_state", "set-execution-state", "set_execution_state", "execution-state", "execution_state":
		action = "update_execution_state"
	case "upsert-checkpoint", "upsert_checkpoint", "replace-checkpoint", "replace_checkpoint", "set-checkpoint", "set_checkpoint":
		action = "upsert_checkpoint"
	case "update-checkpoint", "update_checkpoint", "patch-checkpoint", "patch_checkpoint":
		action = "update_checkpoint"
	case "start-checkpoint", "start_checkpoint":
		action = "start_checkpoint"
	case "continue-checkpoint", "continue_checkpoint", "advance-checkpoint", "advance_checkpoint", "next-checkpoint", "next_checkpoint":
		action = "continue_checkpoint"
	case "complete-checkpoint", "complete_checkpoint", "finish-checkpoint", "finish_checkpoint", "mark-completed", "mark_completed":
		action = "complete_checkpoint"
	case "checkpoint-outcome", "checkpoint_outcome", "mark-checkpoint-outcome", "mark_checkpoint_outcome", "mark-checkpoint", "mark_checkpoint":
		action = "checkpoint_outcome"
	case "mark-needs-review", "mark_needs_review":
		action = "mark_needs_review"
	case "mark-blocked", "mark_blocked":
		action = "mark_blocked"
	case "resolve-blocked-checkpoint", "resolve_blocked_checkpoint", "resolve-block", "resolve_block", "clear-block", "clear_block", "unblock-checkpoint", "unblock_checkpoint":
		action = "resolve_blocked_checkpoint"
	case "mark-failed", "mark_failed":
		action = "mark_failed"
	case "remove-checkpoint", "remove_checkpoint", "delete-checkpoint", "delete_checkpoint":
		action = "remove_checkpoint"
	case "reorder-checkpoints", "reorder_checkpoints":
		action = "reorder_checkpoints"
	case "set-active-checkpoint", "set_active_checkpoint", "activate-checkpoint", "activate_checkpoint":
		action = "set_active_checkpoint"
	case "approve-and-start", "approve_and_start", "approve-start", "approve_start", "start-plan", "start_plan":
		action = "approve_and_start"
	case "request-followup-checkpoint", "request_followup_checkpoint", "followup-checkpoint", "followup_checkpoint", "request-changes", "request_changes":
		return planManagePermissionPayload{}, false, errors.New("plan_manage request_followup_checkpoint is disabled; use transition_checkpoint_boundary from a parent provider turn")
	case "amend-plan", "amend_plan", "plan-amendment", "plan_amendment", "amend-future-checkpoints", "amend_future_checkpoints":
		action = "amend_plan"
	case "request-new-plan", "request_new_plan", "new-plan-proposal", "new_plan_proposal":
		action = "request_new_plan"
	case "restart-checkpoint", "restart_checkpoint", "retry-checkpoint", "retry_checkpoint", "restart-checkpoint-from-zero", "restart_checkpoint_from_zero":
		action = "restart_checkpoint"
	case "rewind-to-checkpoint", "rewind_to_checkpoint", "rewind-checkpoint", "rewind_checkpoint":
		action = "rewind_to_checkpoint"
	case "update-section", "update_section":
		action = "update_section"
	}
	if action != "save" && action != "patch" && action != "update_section" && action != "update_info" && action != "update_execution_policy" && action != "update_execution_state" && action != "upsert_checkpoint" && action != "update_checkpoint" && action != "start_checkpoint" && action != "continue_checkpoint" && action != "complete_checkpoint" && action != "checkpoint_outcome" && action != "mark_needs_review" && action != "mark_blocked" && action != "mark_failed" && action != "restart_checkpoint" && action != "rewind_to_checkpoint" && action != "resolve_blocked_checkpoint" && action != "approve_and_start" && action != "request_followup_checkpoint" && action != "amend_plan" && action != "request_new_plan" && action != "remove_checkpoint" && action != "reorder_checkpoints" && action != "set_active_checkpoint" {
		return planManagePermissionPayload{}, false, nil
	}
	planBody := strings.TrimSpace(mapString(args, "plan"))
	if action == "save" && planBody == "" && args["document"] == nil {
		return planManagePermissionPayload{}, false, nil
	}
	if s.sessions == nil {
		return planManagePermissionPayload{}, false, fmt.Errorf("session service is not configured")
	}
	var existing pebblestore.SessionPlanSnapshot
	var found bool
	var err error
	var requestNewPlanDocument *pebblestore.SessionPlanDocument
	if action == "request_new_plan" {
		requestNewPlanDocument, err = planDocumentFromArgs(args)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		if requestNewPlanDocument == nil {
			return planManagePermissionPayload{}, false, errors.New("request_new_plan requires an explicit structured document before approval; plan text is display context only")
		}
	}
	planID := strings.TrimSpace(mapString(args, "plan_id"))
	if planID == "" {
		planID = strings.TrimSpace(mapString(args, "id"))
	}
	if planID != "" {
		if strings.EqualFold(planID, "active") {
			existing, found, err = s.sessions.GetActivePlan(sessionID)
			if err != nil {
				return planManagePermissionPayload{}, false, err
			}
			if found {
				planID = strings.TrimSpace(existing.ID)
			}
		} else {
			existing, found, err = s.sessions.GetPlan(sessionID, planID)
			if err != nil {
				return planManagePermissionPayload{}, false, err
			}
		}
	} else if action != "request_new_plan" {
		existing, found, err = s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		if found {
			planID = strings.TrimSpace(existing.ID)
		}
	}
	if !found || strings.TrimSpace(existing.ID) == "" {
		if action != "request_new_plan" {
			return planManagePermissionPayload{}, false, nil
		}
		document := requestNewPlanDocument
		if err := sessionruntime.ValidateExecutablePlanDocument(document); err != nil {
			return planManagePermissionPayload{}, false, err
		}
		approved := map[string]any{"action": action, "approval_confirmed": true}
		for key, value := range args {
			switch key {
			case "title", "plan", "reason", "update_summary", "summary", "execution_granularity", "granularity", "execution_shape", "shape", "continuation_policy", "continuation", "mode", "continue_automatically":
				approved[key] = value
			}
		}
		approved["document"] = document
		applyRequestNewPlanExecutionDefaults(approved)
		return planManagePermissionPayload{
			PathID:            "tool.plan-new-request.v1",
			Title:             firstNonEmptyString(strings.TrimSpace(mapString(args, "title")), "New plan proposal"),
			Plan:              planBody,
			Document:          document,
			Action:            action,
			UpdateType:        "new_plan",
			UpdateSummary:     strings.TrimSpace(firstNonEmptyString(mapString(args, "reason"), mapString(args, "update_summary"), mapString(args, "summary"))),
			UpdateKind:        "request_new_plan",
			DocumentOperation: action,
			ApprovedArguments: approved,
		}, true, nil
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
	document, err := planDocumentFromArgs(args)
	if err != nil {
		return planManagePermissionPayload{}, false, err
	}
	var documentPatch *sessionruntime.PlanDocumentPatch
	if planManageActionUsesDocumentPatch(action) {
		documentPatch, err = planDocumentPatchFromArgs(args)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		if documentPatch != nil && strings.Contains(action, "checkpoint") {
			checkpoint = true
		} else if documentPatch == nil {
			return planManagePermissionPayload{}, false, nil
		}
	}
	previewPlan := planBody
	if previewPlan == "" && document != nil {
		previewPlan = strings.TrimSpace(existing.Plan)
	}
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
	if documentPatch != nil && documentPatch.Operation == "" {
		documentPatch.Operation = action
	}
	if documentPatch != nil && isPlanCheckpointOutcomeAction(action, documentPatch) {
		if err := applyPersistedCheckpointOutcomeOwnershipForPreview(existing.Document, documentPatch); err != nil {
			return planManagePermissionPayload{}, false, err
		}
	}
	changeRequest := strings.TrimSpace(firstNonEmptyString(mapString(args, "change_request"), mapString(args, "user_request"), mapString(args, "request"), mapString(args, "prompt"), mapString(args, "text")))
	checkpointTitle := strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_title"), mapString(args, "title")))
	previewDocument := document
	if documentPatch != nil {
		previewDocument, err = sessionruntime.ApplyPlanDocumentPatch(planID, title, existing.Document, *documentPatch)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
	} else if action == "approve_and_start" || action == "restart_checkpoint" || action == "rewind_to_checkpoint" || action == "resolve_blocked_checkpoint" {
		previewDocument, err = clonePlanDocumentForExecutionAction(existing.Document)
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		checkpointID := strings.TrimSpace(firstNonEmptyString(mapString(args, "checkpoint_id"), mapString(args, "active_checkpoint_id"), mapString(args, "active_checkpoint")))
		switch action {
		case "approve_and_start":
			granularity := strings.TrimSpace(firstNonEmptyString(mapString(args, "execution_granularity"), mapString(args, "granularity"), mapString(args, "execution_shape"), mapString(args, "shape")))
			continuation := strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode")))
			if _, ok := args["continue_automatically"]; ok {
				if mapBool(args, "continue_automatically") {
					continuation = sessionruntime.PlanAcceptanceContinuationAutomatic
				} else {
					continuation = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
				}
			}
			if _, err := sessionruntime.ApplyPlanAcceptanceExecutionPolicy(previewDocument, sessionruntime.PlanAcceptanceExecutionOptions{ExecutionGranularity: granularity, ContinuationPolicy: continuation}); err != nil {
				return planManagePermissionPayload{}, false, err
			}
			status = "approved"
			approvalState = "approved"
		case "restart_checkpoint":
			_, err = sessionruntime.ApplyPlanCheckpointReset(previewDocument, sessionruntime.PlanCheckpointResetOptions{CheckpointID: checkpointID})
		case "rewind_to_checkpoint":
			_, err = sessionruntime.ApplyPlanCheckpointReset(previewDocument, sessionruntime.PlanCheckpointResetOptions{CheckpointID: checkpointID, Rewind: true})
		case "resolve_blocked_checkpoint":
			_, err = sessionruntime.ApplyPlanCheckpointBlockResolution(previewDocument, sessionruntime.PlanCheckpointBlockResolutionOptions{CheckpointID: checkpointID, Result: strings.TrimSpace(firstNonEmptyString(mapString(args, "result"), mapString(args, "resolution_result"))), Notes: strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "resolution_notes"), mapString(args, "report"))), ResolvedAt: int64(mapInt(args, "reviewed_at"))})
		}
		if err != nil {
			return planManagePermissionPayload{}, false, err
		}
		checkpoint = true
	} else if previewDocument == nil {
		previewDocument, err = clonePlanDocumentForExecutionAction(existing.Document)
		if err != nil {
			previewDocument = existing.Document
		}
	}
	if action == "amend_plan" && document != nil {
		if amendedPreview, ok := buildPlanAmendmentPreviewDocument(existing.Document, document, args); ok {
			previewDocument = amendedPreview
		}
	}
	if action == "amend_plan" || action == "request_new_plan" || action == "approve_and_start" {
		if document == nil && action != "approve_and_start" {
			return planManagePermissionPayload{}, false, fmt.Errorf("%s requires an explicit structured document before approval", action)
		}
		if err := sessionruntime.ValidateExecutablePlanDocument(previewDocument); err != nil {
			return planManagePermissionPayload{}, false, err
		}
	}
	payload := planManagePermissionPayload{
		PathID:             "tool.plan-manage-update.v1",
		Title:              title,
		PlanID:             planID,
		PriorTitle:         strings.TrimSpace(existing.Title),
		PriorPlan:          strings.TrimSpace(existing.Plan),
		Plan:               previewPlan,
		Document:           previewDocument,
		PriorDocument:      existing.Document,
		DiffLines:          sessionruntime.BuildPlanDiffLines(existing.Plan, previewPlan),
		Version:            existing.Version,
		Revision:           currentPlanRevision(existing),
		CurrentRevision:    currentPlanRevision(existing),
		BaseRevision:       currentPlanRevision(existing),
		Status:             status,
		ApprovalState:      approvalState,
		Activate:           activate,
		Action:             action,
		UpdateType:         "existing_plan",
		UpdateSummary:      updateSummary,
		UpdateScope:        updateScope,
		UpdateKind:         updateKind,
		DocumentOperation:  action,
		Checkpoint:         checkpoint,
		ChangeRequest:      changeRequest,
		CheckpointTitle:    checkpointTitle,
		Tasks:              mapStringSlice(args, "tasks"),
		AcceptanceCriteria: mapStringSlice(args, "acceptance_criteria"),
		Notes:              strings.TrimSpace(firstNonEmptyString(mapString(args, "notes"), mapString(args, "handoff_notes"), mapString(args, "context"))),
		PolicyEffective:    s.resolvePlanFollowupCheckpointPolicyForPermission(existing, ""),
		ApprovalRequired:   action == "amend_plan" || action == "request_new_plan" || (action == "request_followup_checkpoint" && s.resolvePlanFollowupCheckpointPolicyForPermission(existing, "") == sessionruntime.PlanFollowupCheckpointPolicyRequireApproval),
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
		if planBody != "" {
			payload.ApprovedArguments["plan"] = planBody
		}
		if document != nil {
			payload.ApprovedArguments["document"] = document
		}
	} else {
		approvedKeys := planManageApprovedArgumentKeys(action)
		for key, value := range args {
			if approvedKeys[key] {
				payload.ApprovedArguments[key] = value
				continue
			}
			if key == "checkpoint" && planManageActionUsesDocumentPatch(action) {
				if _, isBool := value.(bool); !isBool {
					payload.ApprovedArguments[key] = value
				}
			}
		}
	}
	if action == "amend_plan" || action == "request_new_plan" {
		// Preserve the exact canonical document that was validated for the
		// permission round-trip instead of copying an unvalidated raw argument.
		payload.ApprovedArguments["document"] = previewDocument
	}
	if changeRequest != "" {
		payload.ApprovedArguments["change_request"] = changeRequest
	}
	if action == "request_followup_checkpoint" || action == "request_new_plan" {
		payload.ApprovedArguments["approval_confirmed"] = true
	}
	if action == "request_new_plan" {
		applyRequestNewPlanExecutionDefaults(payload.ApprovedArguments)
	}
	switch action {
	case "request_followup_checkpoint":
		payload.PathID = "tool.plan-followup-request.v1"
		payload.UpdateKind = "request_followup_checkpoint"
	case "amend_plan":
		payload.PathID = "tool.plan-amendment.v1"
		payload.UpdateKind = "plan_amendment"
		payload.PlanAmendmentDelta = buildPlanAmendmentDelta(existing.Document, previewDocument, payload.ApprovedArguments, updateSummary, currentPlanRevision(existing))
	case "request_new_plan":
		payload.PathID = "tool.plan-new-request.v1"
		payload.UpdateKind = "request_new_plan"
	}
	return payload, true, nil
}

func applyRequestNewPlanExecutionDefaults(args map[string]any) {
	if args == nil {
		return
	}
	granularity := strings.TrimSpace(firstNonEmptyString(mapString(args, "execution_granularity"), mapString(args, "granularity"), mapString(args, "execution_shape"), mapString(args, "shape")))
	continuation := strings.TrimSpace(firstNonEmptyString(mapString(args, "continuation_policy"), mapString(args, "continuation"), mapString(args, "mode")))
	_, hasContinueAutomatically := args["continue_automatically"]
	if granularity == "" {
		args["execution_granularity"] = sessionruntime.PlanAcceptanceGranularityCheckpointed
	}
	if continuation == "" && !hasContinueAutomatically {
		args["continuation_policy"] = sessionruntime.PlanAcceptanceContinuationAutomatic
		args["continue_automatically"] = true
		return
	}
	if hasContinueAutomatically {
		if mapBool(args, "continue_automatically") {
			args["continuation_policy"] = sessionruntime.PlanAcceptanceContinuationAutomatic
		} else {
			args["continuation_policy"] = sessionruntime.PlanAcceptanceContinuationReviewEachCheckpoint
		}
	}
}

func buildPlanAmendmentPreviewDocument(current, proposed *pebblestore.SessionPlanDocument, args map[string]any) (*pebblestore.SessionPlanDocument, bool) {
	if current == nil || proposed == nil {
		return nil, false
	}
	preview, err := clonePlanDocumentForExecutionAction(current)
	if err != nil || preview == nil {
		return nil, false
	}
	replaceID := strings.TrimSpace(firstNonEmptyString(mapString(args, "replace_from_checkpoint_id"), mapString(args, "checkpoint_id")))
	replaceIndex := -1
	if replaceID != "" {
		replaceIndex = findCheckpointDeltaIndex(preview.Checkpoints, replaceID)
	}
	if replaceIndex < 0 && mapBool(args, "amend_future_checkpoints") {
		replaceIndex = firstPendingCheckpointDeltaIndex(preview.Checkpoints)
		if replaceIndex >= 0 {
			replaceID = strings.TrimSpace(preview.Checkpoints[replaceIndex].ID)
		}
	}
	if replaceIndex < 0 || replaceID == "" {
		return nil, false
	}
	proposedIndex := findCheckpointDeltaIndex(proposed.Checkpoints, replaceID)
	if proposedIndex < 0 {
		return nil, false
	}
	future := checkpointDeltaCloneSlice(proposed.Checkpoints[proposedIndex:])
	if len(future) == 0 {
		return nil, false
	}
	preview.Info = proposed.Info
	preview.Checkpoints = append(preview.Checkpoints[:replaceIndex], future...)
	for i := range preview.Checkpoints {
		preview.Checkpoints[i].Order = i + 1
	}
	preview.ExecutionPolicy = proposed.ExecutionPolicy
	preview.RenderedText = strings.TrimSpace(proposed.RenderedText)
	preview.DisplayText = strings.TrimSpace(proposed.DisplayText)
	return preview, true
}

func checkpointDeltaCloneSlice(checkpoints []pebblestore.SessionPlanCheckpoint) []pebblestore.SessionPlanCheckpoint {
	if len(checkpoints) == 0 {
		return nil
	}
	cloned := make([]pebblestore.SessionPlanCheckpoint, len(checkpoints))
	copy(cloned, checkpoints)
	return cloned
}

func buildPlanAmendmentDelta(current, proposed *pebblestore.SessionPlanDocument, approvedArgs map[string]any, reason string, currentRevision int) *planAmendmentDelta {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = strings.TrimSpace(firstNonEmptyString(mapString(approvedArgs, "update_summary"), mapString(approvedArgs, "summary"), mapString(approvedArgs, "reason")))
	}
	delta := &planAmendmentDelta{
		Reason:                  reason,
		BaseRevision:            mapInt(approvedArgs, "base_revision"),
		CurrentRevision:         currentRevision,
		OverrideStale:           mapBool(approvedArgs, "override_stale"),
		ReplaceFromCheckpointID: strings.TrimSpace(firstNonEmptyString(mapString(approvedArgs, "replace_from_checkpoint_id"), mapString(approvedArgs, "checkpoint_id"))),
	}
	if current == nil || len(current.Checkpoints) == 0 {
		delta.Bullets = buildPlanAmendmentDeltaBullets(delta)
		return delta
	}
	replaceIndex := -1
	if delta.ReplaceFromCheckpointID != "" {
		replaceIndex = findCheckpointDeltaIndex(current.Checkpoints, delta.ReplaceFromCheckpointID)
	}
	if replaceIndex < 0 && mapBool(approvedArgs, "amend_future_checkpoints") {
		replaceIndex = firstPendingCheckpointDeltaIndex(current.Checkpoints)
		if replaceIndex >= 0 {
			delta.ReplaceFromCheckpointID = strings.TrimSpace(current.Checkpoints[replaceIndex].ID)
		}
	}
	if replaceIndex < 0 && delta.ReplaceFromCheckpointID != "" && proposed != nil {
		replaceIndex = findCheckpointDeltaIndex(proposed.Checkpoints, delta.ReplaceFromCheckpointID)
	}
	if replaceIndex < 0 {
		delta.Bullets = buildPlanAmendmentDeltaBullets(delta)
		return delta
	}
	for _, checkpoint := range current.Checkpoints[:replaceIndex] {
		delta.PreservedCheckpoints = append(delta.PreservedCheckpoints, checkpointDeltaItem(checkpoint))
	}
	delta.ReplacedCheckpoints = append(delta.ReplacedCheckpoints, checkpointDeltaItems(current.Checkpoints[replaceIndex:])...)
	if proposed != nil {
		proposedIndex := findCheckpointDeltaIndex(proposed.Checkpoints, delta.ReplaceFromCheckpointID)
		if proposedIndex >= 0 {
			delta.ReplacementCheckpoints = append(delta.ReplacementCheckpoints, checkpointDeltaItems(proposed.Checkpoints[proposedIndex:])...)
			if len(delta.ReplacementCheckpoints) > 0 {
				next := delta.ReplacementCheckpoints[0]
				delta.NextCheckpoint = &next
			}
		}
	}
	delta.Bullets = buildPlanAmendmentDeltaBullets(delta)
	return delta
}

func buildPlanAmendmentDeltaBullets(delta *planAmendmentDelta) []string {
	if delta == nil {
		return nil
	}
	bullets := make([]string, 0, 5)
	for _, checkpoint := range delta.PreservedCheckpoints {
		if strings.EqualFold(checkpoint.Status, sessionruntime.PlanCheckpointStatusCompleted) {
			bullets = append(bullets, fmt.Sprintf("%s remains completed and preserved.", checkpointDeltaLabel(checkpoint)))
		}
	}
	if len(delta.ReplacedCheckpoints) > 0 {
		bullets = append(bullets, fmt.Sprintf("Replacing pending future work from %s.", checkpointDeltaLabel(delta.ReplacedCheckpoints[0])))
	} else if delta.ReplaceFromCheckpointID != "" {
		bullets = append(bullets, fmt.Sprintf("Replacing pending future work from %s.", delta.ReplaceFromCheckpointID))
	}
	if delta.NextCheckpoint != nil {
		bullets = append(bullets, fmt.Sprintf("Next checkpoint becomes %s.", checkpointDeltaLabel(*delta.NextCheckpoint)))
	}
	if delta.Reason != "" {
		bullets = append(bullets, fmt.Sprintf("Reason: %s", delta.Reason))
	}
	if delta.BaseRevision > 0 || delta.CurrentRevision > 0 {
		bullets = append(bullets, fmt.Sprintf("Revision guard: base %d, current %d.", delta.BaseRevision, delta.CurrentRevision))
	} else if delta.OverrideStale {
		bullets = append(bullets, "Revision guard: override stale revision enabled.")
	}
	return bullets
}

func checkpointDeltaItems(checkpoints []pebblestore.SessionPlanCheckpoint) []planCheckpointDeltaItem {
	items := make([]planCheckpointDeltaItem, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		items = append(items, checkpointDeltaItem(checkpoint))
	}
	return items
}

func checkpointDeltaItem(checkpoint pebblestore.SessionPlanCheckpoint) planCheckpointDeltaItem {
	return planCheckpointDeltaItem{ID: strings.TrimSpace(checkpoint.ID), Title: strings.TrimSpace(checkpoint.Title), Status: strings.TrimSpace(checkpoint.Status)}
}

func checkpointDeltaLabel(item planCheckpointDeltaItem) string {
	id := strings.TrimSpace(item.ID)
	title := strings.TrimSpace(item.Title)
	if id != "" && title != "" {
		return fmt.Sprintf("%s (%s)", id, title)
	}
	if id != "" {
		return id
	}
	if title != "" {
		return title
	}
	return "checkpoint"
}

func findCheckpointDeltaIndex(checkpoints []pebblestore.SessionPlanCheckpoint, checkpointID string) int {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return -1
	}
	for i := range checkpoints {
		if strings.TrimSpace(checkpoints[i].ID) == checkpointID {
			return i
		}
	}
	return -1
}

func firstPendingCheckpointDeltaIndex(checkpoints []pebblestore.SessionPlanCheckpoint) int {
	for i := range checkpoints {
		status := strings.ToLower(strings.TrimSpace(checkpoints[i].Status))
		if status == "" || status == sessionruntime.PlanCheckpointStatusPending {
			return i
		}
	}
	return -1
}

func cloneTaskAgentProfile(profile pebblestore.AgentProfile) (pebblestore.AgentProfile, error) {
	raw, err := json.Marshal(profile)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	var cloned pebblestore.AgentProfile
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return pebblestore.NormalizeAgentProfile(cloned), nil
}

func isPlanSidechatTaskParent(session pebblestore.SessionSnapshot) bool {
	return strings.EqualFold(strings.TrimSpace(mapString(session.Metadata, "system_sidechat_kind")), agentruntime.SystemSidechatKindPlan) &&
		strings.EqualFold(strings.TrimSpace(mapString(session.Metadata, "lineage_kind")), "system_sidechat") &&
		agentruntime.IsPlanSidechatAgentName(mapString(session.Metadata, "agent_name"))
}

func validatePlanSidechatTaskTargets(parentSession pebblestore.SessionSnapshot, launches []taskLaunchSpec) error {
	if !isPlanSidechatTaskParent(parentSession) {
		return nil
	}
	for i, launch := range launches {
		if !agentruntime.IsFinderAgentName(launch.RequestedSubagentType) {
			return fmt.Errorf("Plan sidechat task launches[%d] may target only Finder", i)
		}
	}
	return nil
}

func (s *Service) resolveTaskLaunchProfile(parentSession pebblestore.SessionSnapshot, requested string) (pebblestore.AgentProfile, bool, string, error) {
	return s.resolveTaskLaunchProfileForMode(parentSession, requested, parentSession.Mode)
}

func (s *Service) resolveTaskLaunchProfileForMode(parentSession pebblestore.SessionSnapshot, requested, childMode string) (pebblestore.AgentProfile, bool, string, error) {
	if strings.EqualFold(strings.TrimSpace(requested), agentruntime.SwarmAgentID) {
		if parentSession.ModelProfile == nil {
			return pebblestore.AgentProfile{}, false, "", fmt.Errorf("Swarm child launch requires the parent immutable model profile")
		}
		state, err := s.agents.ListStateForAccount(parentSession.AccountScopeID, 2000)
		if err != nil {
			return pebblestore.AgentProfile{}, false, "", err
		}
		profiles := make(map[string]pebblestore.AgentProfile, len(state.Profiles))
		for _, profile := range state.Profiles {
			profiles[strings.ToLower(strings.TrimSpace(profile.Name))] = profile
		}
		resolution, found, err := s.resolveManageSessionsDeployAgent(profiles, agentruntime.SwarmAgentID)
		if err != nil {
			return pebblestore.AgentProfile{}, false, "", err
		}
		if !found {
			return pebblestore.AgentProfile{}, false, "", fmt.Errorf("Swarm agent is not configured")
		}
		modelProfile, err := inheritedSessionModelProfile(parentSession.ModelProfile, childMode)
		if err != nil {
			return pebblestore.AgentProfile{}, false, "", err
		}
		preference, err := manageSessionsDeployModelProfilePreference(modelProfile, childMode)
		if err != nil {
			return pebblestore.AgentProfile{}, false, "", err
		}
		profile := resolution.ExecutionProfile
		profile.Provider, profile.Model, profile.Thinking = preference.Provider, preference.Model, preference.Thinking
		profile.AutoServiceTier, profile.ContextMode = preference.ServiceTier, preference.ContextMode
		return profile, false, "", nil
	}
	if agentruntime.IsIdeaAgentName(requested) {
		profile, err := s.agents.ResolveSystemAgent(agentruntime.IdeaAgentID, pebblestore.AgentProfile{
			Provider: parentSession.Preference.Provider, Model: parentSession.Preference.Model, Thinking: parentSession.Preference.Thinking,
			AutoServiceTier: parentSession.Preference.ServiceTier, ContextMode: parentSession.Preference.ContextMode,
		})
		return profile, false, "", err
	}
	if !agentruntime.IsCoderAgentName(requested) {
		profile, err := s.resolveTaskSubagentForAccount(parentSession.AccountScopeID, requested)
		return profile, false, "", err
	}
	sourceName := strings.TrimSpace(mapString(parentSession.Metadata, "agent_name"))
	if parentProfile, err := sessionV3AgentProfileFromMetadataMap(parentSession.Metadata); err == nil && sourceName == "" {
		sourceName = strings.TrimSpace(parentProfile.Name)
	}
	if s.model == nil {
		return pebblestore.AgentProfile{}, true, sourceName, errors.New("system-agent model service is not configured")
	}
	_, profile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, parentSession.AccountScopeID, agentruntime.CoderAgentID, "")
	if err != nil {
		return pebblestore.AgentProfile{}, true, sourceName, err
	}
	return profile, true, sourceName, nil
}

func taskLaunchManifestDigest(manifest taskLaunchManifest) (string, error) {
	manifest.ManifestHash = ""
	manifest.ApprovedArguments = nil
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	// Hash the canonical JSON value rather than the Go struct encoding. Permission
	// storage round-trips nested typed values through map[string]any; canonicalizing
	// here keeps the approved snapshot digest stable across that boundary.
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", err
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func parseApprovedTaskLaunchManifest(approved string, launchSpecs []taskLaunchSpec) (taskLaunchManifest, error) {
	var envelope struct {
		ManifestHash string             `json:"manifest_hash"`
		Manifest     taskLaunchManifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(approved), &envelope); err != nil {
		return taskLaunchManifest{}, fmt.Errorf("approved task manifest invalid: %w", err)
	}
	digest, err := taskLaunchManifestDigest(envelope.Manifest)
	if err != nil || digest != strings.TrimSpace(envelope.ManifestHash) || digest != strings.TrimSpace(envelope.Manifest.ManifestHash) {
		return taskLaunchManifest{}, errors.New("approved task manifest snapshot hash mismatch")
	}
	if len(envelope.Manifest.Launches) != len(launchSpecs) {
		return taskLaunchManifest{}, errors.New("approved task manifest launch count mismatch")
	}
	for i := range launchSpecs {
		row := envelope.Manifest.Launches[i]
		if !strings.EqualFold(strings.TrimSpace(row.RequestedSubagentType), strings.TrimSpace(launchSpecs[i].RequestedSubagentType)) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d target mismatch", i)
		}
		if row.ProfileSnapshot == nil {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d is missing profile snapshot", i)
		}
		if strings.TrimSpace(row.StreamKey) != strings.TrimSpace(launchSpecs[i].StreamKey) || row.SwarmMode != launchSpecs[i].SwarmMode {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d swarm identity mismatch", i)
		}
	}
	return envelope.Manifest, nil
}

func (s *Service) buildTaskLaunchPermissionPayload(sessionID, sessionMode string, call tool.Call) (taskLaunchManifest, error) {
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		return taskLaunchManifest{}, err
	}

	parentSession, ok, sessionErr := s.sessions.GetSession(sessionID)
	if sessionErr != nil {
		return taskLaunchManifest{}, sessionErr
	}
	if !ok {
		return taskLaunchManifest{}, fmt.Errorf("session %q not found", sessionID)
	}
	if err := validatePlanSidechatTaskTargets(parentSession, parsed.Launches); err != nil {
		return taskLaunchManifest{}, err
	}
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
		subagentProfile, virtualTarget, sourceAgentName, err := s.resolveTaskLaunchProfileForMode(parentSession, requested, childMode)
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
		var toolContract ResolvedAgentToolContract
		var profileDisabledTools map[string]bool
		var toolErr error
		if virtualTarget || agentruntime.IsFinderAgentName(resolvedName) || agentruntime.IsDesignerAgentName(resolvedName) || agentruntime.IsIdeaAgentName(resolvedName) {
			// Compiled Coder, Finder, Designer, and Idea profiles are trusted launch snapshots, not
			// persisted agent rows. Compile their immutable
			// contracts directly instead of looking them up in the agent store.
			toolContract, _, profileDisabledTools, toolErr = s.compileResolvedAgentToolContract(parentSession.AccountScopeID, subagentProfile)
		} else {
			toolContract, _, profileDisabledTools, toolErr = s.ResolveAgentToolContractForAccount(parentSession.AccountScopeID, subagentProfile)
		}
		if toolErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] cannot resolve subagent %q tool contract: %w", i, requested, toolErr)
		}
		resolvedTools := buildTaskLaunchResolvedToolSummary(toolContract, profileDisabledTools, disabledTools, executionMode)
		preference := applyAgentPreferenceOverridesForMode(parentSession.Preference, subagentProfile, childMode)
		childTitle := assignmentLabel
		launches = append(launches, taskLaunchManifestRow{
			Description:           parsed.Description,
			RequestedSubagentType: requested,
			ResolvedAgentName:     resolvedName,
			Action:                parsed.Action,
			MetaPrompt:            metaPrompt,
			AssignmentLabel:       assignmentLabel,
			Deliverable:           strings.TrimSpace(launch.Deliverable),
			ConcurrencyReason:     strings.TrimSpace(launch.ConcurrencyReason),
			OwnedScope:            append([]string(nil), launch.OwnedScope...),
			DependencyEvidence:    strings.TrimSpace(launch.DependencyEvidence),
			SubagentProvider:      strings.TrimSpace(preference.Provider),
			SubagentModel:         strings.TrimSpace(preference.Model),
			SubagentThinking:      strings.TrimSpace(preference.Thinking),
			SubagentServiceTier:   strings.TrimSpace(preference.ServiceTier),
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
			SourceArguments:      cloneGenericMap(launch.SourceArguments),
			ParentCopy:           virtualTarget,
			SourceAgentName:      sourceAgentName,
			SourceProfileMode:    strings.TrimSpace(subagentProfile.Mode),
			InheritedRuntimeMode: pebblestore.AgentProfileRuntimeMode(subagentProfile),
			ProfileSnapshot:      &subagentProfile,
			StreamKey:            strings.TrimSpace(launch.StreamKey),
			SwarmMode:            launch.SwarmMode,
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
		ParentMode:         parentMode,
		EffectiveChildMode: childMode,
		DisabledTools:      disabledTools,
		ResolvedTools:      launches[0].ResolvedTools,
		SourceArguments:    parsed.SourceArguments,
		Launches:           launches,
		TaskMode:           parsed.Mode,
	}
	if parsed.Swarm != nil {
		manifest.SwarmAgentType = parsed.Swarm.AgentType
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

	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		return taskLaunchManifest{}, fmt.Errorf("hash task launch manifest: %w", err)
	}
	manifest.ManifestHash = digest
	approvedManifest := manifest
	approvedManifest.ApprovedArguments = nil
	manifest.ApprovedArguments = map[string]any{"manifest_hash": digest, "manifest": approvedManifest}
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
		validated, err := validateTemporaryWorkspaceRoot(root)
		if err != nil {
			continue
		}
		add(validated)
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
	approved := cloneGenericMap(args)
	approved["confirm"] = true
	if change, ok := payload["change"].(map[string]any); ok {
		if revision := strings.TrimSpace(mapString(change, "expected_revision")); revision != "" {
			approved["expected_revision"] = revision
		}
	}
	payload["approved_arguments"] = approved
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
	if action == "create_batch" || action == "create-batch" {
		if themes, ok := args["themes"].([]any); ok {
			payload["generated_count"] = len(themes)
			names := make([]string, 0, len(themes))
			for _, item := range themes {
				theme, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if name := strings.TrimSpace(mapString(theme, "name")); name != "" {
					names = append(names, name)
				}
			}
			payload["generated_names"] = names
		}
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
		return cloneGenericMap(approved)
	}
	if _, legacyPreview := payload["change"].(map[string]any); !legacyPreview {
		return cloneGenericMap(payload)
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
		if revision := strings.TrimSpace(mapString(change, "expected_revision")); revision != "" {
			args["expected_revision"] = revision
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
		if strings.TrimSpace(mapString(args, "action")) == "" {
			args["action"] = "save"
		}
		if strings.TrimSpace(mapString(args, "action")) == "request_new_plan" {
			args["approval_confirmed"] = true
			applyRequestNewPlanExecutionDefaults(args)
		}
		return args
	}
	args := cloneGenericMap(payload)
	if args == nil {
		args = map[string]any{}
	}
	delete(args, "tool")
	delete(args, "path_id")
	delete(args, "approval_summary")
	delete(args, "user_message")
	delete(args, "requested_modifications")
	delete(args, "details_truncated")
	delete(args, "prior_title")
	delete(args, "prior_plan")
	delete(args, "prior_document")
	delete(args, "diff_lines")
	delete(args, "version")
	if strings.TrimSpace(mapString(args, "action")) == "" {
		args["action"] = "save"
	}
	if strings.TrimSpace(mapString(args, "action")) == "request_new_plan" {
		args["approval_confirmed"] = true
		applyRequestNewPlanExecutionDefaults(args)
	}
	if len(args) == 1 {
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

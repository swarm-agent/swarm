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
	"reflect"
	"regexp"
	"sort"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	artifactruntime "swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/taskscope"
	"swarm/packages/swarmd/internal/tool"
)

const (
	taskLaunchPermissionPathID     = "permission.task_launch.v1"
	taskModeRegular                = "regular"
	taskModeSwarm                  = "swarm"
	taskExecutionFormatSubagents   = "subagent_wave"
	taskExecutionFormatImageDirect = "direct_image_swarm"
	taskSwarmStrategyExplore       = "explore"
	taskSwarmStrategyAssembly      = "assembly"
	taskOutputModeManaged          = "managed"
	taskOutputModeWorkspace        = "workspace"
	taskAssemblySwarmLaunchEnabled = false
	taskSwarmMaxAgents             = 256
	taskProgramActionStart         = "start"
	taskProgramActionStatus        = "status"
)

var taskProgramIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type taskCallArguments struct {
	Action               string
	Description          string
	Prompt               string
	Mode                 string
	ProgramWorkspacePath string
	Swarm                *taskSwarmSpec
	Program              *taskProgramSpec
	ProgramID            string
	PlannedProgram       bool
	Launches             []taskLaunchSpec
	SourceArtifact       *pebblestore.SessionArtifactSelectionReference
	ArtifactV3Source     *taskArtifactV3Source
	ArtifactV2Source     *taskArtifactV2Source
	SourceArguments      map[string]any
}

type taskProgramSpec struct {
	ID             string             `json:"id"`
	MaxConcurrency *int               `json:"max_concurrency,omitempty"`
	Stages         []taskProgramStage `json:"stages"`
	Jobs           []taskProgramJob   `json:"jobs"`
}

type taskProgramStage struct {
	ID                 string   `json:"id"`
	DependsOn          []string `json:"depends_on,omitempty"`
	DependencyEvidence string   `json:"dependency_evidence"`
}

type taskProgramJob struct {
	ID                    string                                         `json:"id"`
	StageID               string                                         `json:"stage_id"`
	DependsOn             []string                                       `json:"depends_on,omitempty"`
	RequestedSubagentType string                                         `json:"agent_type"`
	TargetWorkspacePath   string                                         `json:"workspace_path,omitempty"`
	MetaPrompt            string                                         `json:"meta_prompt"`
	AssignmentLabel       string                                         `json:"title"`
	Deliverable           string                                         `json:"deliverable"`
	OwnedScope            []string                                       `json:"owned_scope,omitempty"`
	OutputMode            string                                         `json:"output_mode,omitempty"`
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	AcceptanceCriteria    []string                                       `json:"acceptance_criteria"`
	DependencyEvidence    string                                         `json:"dependency_evidence"`
}

type taskProgramCapacity struct {
	TotalJobs             int  `json:"total_jobs"`
	ReadyJobs             int  `json:"ready_jobs"`
	ActiveAccountCapacity int  `json:"active_account_capacity"`
	ExplicitLowerCap      *int `json:"explicit_lower_cap,omitempty"`
	EffectiveCapacity     int  `json:"effective_capacity"`
}

type taskSwarmGroup struct {
	Name         string `json:"name"`
	Count        int    `json:"count"`
	Instructions string `json:"instructions,omitempty"`
}

type taskSwarmAssemblyPart struct {
	Name         string   `json:"name"`
	Instructions string   `json:"instructions,omitempty"`
	OwnedScope   []string `json:"owned_scope"`
}

type taskSwarmIterationControls struct {
	Preserve []string `json:"preserve,omitempty"`
	Change   []string `json:"change"`
	Exclude  []string `json:"exclude,omitempty"`
}

type taskSwarmSectionTarget struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind,omitempty"`
	Description string  `json:"description,omitempty"`
	StartMs     int64   `json:"start_ms,omitempty"`
	EndMs       int64   `json:"end_ms,omitempty"`
	X           float64 `json:"x,omitempty"`
	Y           float64 `json:"y,omitempty"`
	Width       float64 `json:"width,omitempty"`
	Height      float64 `json:"height,omitempty"`
	Page        int     `json:"page,omitempty"`
	StateID     string  `json:"state_id,omitempty"`
	Selector    string  `json:"selector,omitempty"`
}

type taskArtifactV3Source struct {
	SessionID     string   `json:"session_id"`
	ArtifactID    string   `json:"artifact_id"`
	CommitOID     string   `json:"commit_oid"`
	ProjectionSeq uint64   `json:"projection_seq"`
	TargetPartIDs []string `json:"target_part_ids,omitempty"`
}

type taskArtifactV2Source struct {
	ArtifactID         string
	PublishedHeadID    string
	CompositionID      string
	WorkingRevision    uint64
	CompositionHeadRev uint64
	TargetPartIDs      []string
}

type taskSwarmSpec struct {
	Strategy            string
	AgentType           string
	Count               int
	Themes              []string
	Groups              []taskSwarmGroup
	OutputContract      string
	OutputMode          string
	OutputRequirements  *pebblestore.SessionArtifactOutputRequirements
	AnimationProfile    *pebblestore.SessionArtifactAnimationProfile
	IterationControls   *taskSwarmIterationControls
	SourceArtifact      *pebblestore.SessionArtifactSelectionReference
	ArtifactV3Source    *taskArtifactV3Source
	ArtifactV2Source    *taskArtifactV2Source
	SectionTarget       *taskSwarmSectionTarget
	SectionTargets      []*taskSwarmSectionTarget
	AssemblyParts       []taskSwarmAssemblyPart
	IntegrationContract string
}

type taskLaunchSpec struct {
	RequestedSubagentType string
	TargetWorkspacePath   string
	MetaPrompt            string
	AssignmentLabel       string
	Deliverable           string
	ConcurrencyReason     string
	OwnedScope            []string
	OutputMode            string
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile
	SourceArtifact        *pebblestore.SessionArtifactSelectionReference
	ArtifactV3Source      *taskArtifactV3Source
	ArtifactV2Source      *taskArtifactV2Source
	DependencyEvidence    string
	StreamKey             string
	SwarmMode             bool
	SwarmStrategy         string
	AssemblyPart          *taskSwarmAssemblyPart
	IntegrationContract   string
	SourceArguments       map[string]any
}

type taskImageManifestRow struct {
	Index              int                                            `json:"index"`
	Theme              string                                         `json:"theme,omitempty"`
	StreamKey          string                                         `json:"stream_key"`
	OutputRequirements *pebblestore.SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	SourceArtifact     *pebblestore.SessionArtifactSelectionReference `json:"source_artifact,omitempty"`
}

type taskLaunchManifest struct {
	PathID              string                         `json:"path_id"`
	Goal                string                         `json:"goal"`
	LaunchCount         int                            `json:"launch_count"`
	ImageCount          int                            `json:"image_count,omitempty"`
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
	Images              []taskImageManifestRow         `json:"images,omitempty"`
	ExecutionFormat     string                         `json:"execution_format,omitempty"`
	TaskMode            string                         `json:"task_mode,omitempty"`
	Program             *taskProgramSpec               `json:"program,omitempty"`
	ProgramID           string                         `json:"program_id,omitempty"`
	ProgramReadyCount   int                            `json:"program_ready_count,omitempty"`
	SwarmAgentType      string                         `json:"swarm_agent_type,omitempty"`
	SwarmStrategy       string                         `json:"swarm_strategy,omitempty"`
	AssemblyParts       []taskSwarmAssemblyPart        `json:"assembly_parts,omitempty"`
	IntegrationContract string                         `json:"integration_contract,omitempty"`
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
	Description           string                                         `json:"description"`
	RequestedSubagentType string                                         `json:"requested_subagent_type"`
	ResolvedAgentName     string                                         `json:"resolved_agent_name"`
	ResolvedAgentError    string                                         `json:"resolved_agent_error,omitempty"`
	Action                string                                         `json:"action"`
	MetaPrompt            string                                         `json:"meta_prompt,omitempty"`
	AssignmentLabel       string                                         `json:"assignment_label,omitempty"`
	Deliverable           string                                         `json:"deliverable,omitempty"`
	ConcurrencyReason     string                                         `json:"concurrency_reason,omitempty"`
	OwnedScope            []string                                       `json:"owned_scope,omitempty"`
	OutputMode            string                                         `json:"output_mode,omitempty"`
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	SourceArtifact        *pebblestore.SessionArtifactSelectionReference `json:"source_artifact,omitempty"`
	ArtifactV3Source      *taskArtifactV3Source                          `json:"artifact_v3_source,omitempty"`
	DependencyEvidence    string                                         `json:"dependency_evidence,omitempty"`
	SubagentProvider      string                                         `json:"subagent_provider,omitempty"`
	SubagentModel         string                                         `json:"subagent_model,omitempty"`
	SubagentThinking      string                                         `json:"subagent_thinking,omitempty"`
	SubagentServiceTier   string                                         `json:"subagent_service_tier,omitempty"`
	ChildTitlePreview     string                                         `json:"child_title_preview,omitempty"`
	ChildMode             string                                         `json:"effective_child_mode"`
	DisabledTools         []string                                       `json:"disabled_tools,omitempty"`
	ResolvedTools         *taskLaunchResolvedToolSummary                 `json:"resolved_tools,omitempty"`
	Capabilities          map[string]any                                 `json:"capabilities,omitempty"`
	TargetWorkspacePath   string                                         `json:"target_workspace_path,omitempty"`
	TargetWorkspaceName   string                                         `json:"target_workspace_name,omitempty"`
	SourceArguments       map[string]any                                 `json:"source_arguments,omitempty"`
	ParentCopy            bool                                           `json:"parent_copy,omitempty"`
	SourceAgentName       string                                         `json:"source_agent_name,omitempty"`
	SourceProfileMode     string                                         `json:"source_profile_mode,omitempty"`
	InheritedRuntimeMode  string                                         `json:"inherited_runtime_mode,omitempty"`
	ProfileSnapshot       *pebblestore.AgentProfile                      `json:"profile_snapshot,omitempty"`
	ModelProfileSnapshot  *pebblestore.SessionModelProfileSnapshot       `json:"model_profile_snapshot"`
	StreamKey             string                                         `json:"stream_key,omitempty"`
	SwarmMode             bool                                           `json:"swarm_mode,omitempty"`
	SwarmStrategy         string                                         `json:"swarm_strategy,omitempty"`
	AssemblyPart          *taskSwarmAssemblyPart                         `json:"assembly_part,omitempty"`
	IntegrationContract   string                                         `json:"integration_contract,omitempty"`
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

	_, hasProgram := args["program"]
	programID := strings.TrimSpace(mapString(args, "program_id"))
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if hasProgram {
		switch action {
		case "", "spawn", "start", "run":
			action = taskProgramActionStart
		default:
			return taskCallArguments{}, fmt.Errorf("task program action %q is not supported", action)
		}
	} else {
		switch action {
		case "", "spawn", "run":
			action = "spawn"
		case taskProgramActionStart:
			if programID != "" {
				return taskCallArguments{}, errors.New("task program start requires a new program definition or the approved active checkpoint program; continuing an existing program_id is not supported")
			}
		case taskProgramActionStatus:
		default:
			return taskCallArguments{}, fmt.Errorf("task action %q is not supported", action)
		}
	}

	description := strings.TrimSpace(mapString(args, "description"))
	if description == "" {
		description = "delegated task"
	}
	prompt := strings.TrimSpace(mapString(args, "prompt"))
	if prompt == "" {
		prompt = strings.TrimSpace(mapString(args, "message"))
	}
	if prompt == "" && action != taskProgramActionStatus && !(action == taskProgramActionStart && !hasProgram) {
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
	if (hasProgram || action == taskProgramActionStatus) && mode != taskModeRegular {
		return taskCallArguments{}, errors.New("task program lifecycle supports only mode=regular")
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
			TargetWorkspacePath: strings.TrimSpace(mapString(raw, "workspace_path")),
			Deliverable:         strings.TrimSpace(mapString(raw, "deliverable")),
			ConcurrencyReason:   strings.TrimSpace(mapString(raw, "concurrency_reason")),
			OwnedScope:          ownedScope,
			DependencyEvidence:  strings.TrimSpace(mapString(raw, "dependency_evidence")),
			SourceArguments:     cloneGenericMap(raw),
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
		if err := applyTaskDesignerOutputMode(&launch, mapString(raw, "output_mode"), label); err != nil {
			return taskLaunchSpec{}, err
		}
		if rawRequirements, exists := raw["output_requirements"]; exists {
			if rawRequirements == nil {
				return taskLaunchSpec{}, fmt.Errorf("%s output_requirements must be an object", label)
			}
			if err := applyTaskOutputRequirements(&launch, rawRequirements, label); err != nil {
				return taskLaunchSpec{}, err
			}
		}
		if rawProfile, exists := raw["animation_profile"]; exists {
			if rawProfile == nil {
				return taskLaunchSpec{}, fmt.Errorf("%s animation_profile must be an object", label)
			}
			if err := applyTaskAnimationProfile(&launch, rawProfile, label); err != nil {
				return taskLaunchSpec{}, err
			}
		}
		applyCanonicalCoderOwnedScope(&launch)
		return launch, nil
	}

	if hasProgram {
		if _, ok := args["launches"]; ok {
			return taskCallArguments{}, errors.New("task program start declares jobs in program.jobs; launches must be omitted")
		}
		program, launches, err := parseTaskProgram(args, prompt)
		if err != nil {
			return taskCallArguments{}, err
		}
		return taskCallArguments{
			Action: action, Description: description, Prompt: prompt, Mode: mode,
			ProgramWorkspacePath: strings.TrimSpace(mapString(args, "workspace_path")),
			Program:              program, ProgramID: program.ID, Launches: launches, SourceArguments: args,
		}, nil
	}
	if action == taskProgramActionStart && !hasProgram {
		for key := range args {
			switch key {
			case "action", "description", "prompt", "message", "mode", "workspace_path":
			default:
				return taskCallArguments{}, fmt.Errorf("planned task program start contains unsupported field %q", key)
			}
		}
		return taskCallArguments{Action: action, Description: description, Prompt: prompt, Mode: mode, ProgramWorkspacePath: strings.TrimSpace(mapString(args, "workspace_path")), PlannedProgram: true, SourceArguments: args}, nil
	}
	if action == taskProgramActionStatus {
		if !taskProgramIDPattern.MatchString(programID) {
			return taskCallArguments{}, errors.New("task program_id must match ^[a-z][a-z0-9_-]{0,63}$")
		}
		for key := range args {
			switch key {
			case "action", "description", "prompt", "message", "mode", "program_id":
			default:
				return taskCallArguments{}, fmt.Errorf("task program status contains unsupported field %q", key)
			}
		}
		return taskCallArguments{Action: action, Description: description, Prompt: prompt, Mode: mode, ProgramID: programID, SourceArguments: args}, nil
	}

	if mode == taskModeSwarm {
		swarm, launches, err := parseTaskSwarmArguments(args, prompt, description)
		if err != nil {
			return taskCallArguments{}, err
		}
		return taskCallArguments{
			Action: action, Description: description, Prompt: prompt, Mode: mode,
			Swarm: swarm, Launches: launches, SourceArtifact: cloneTaskImageSourceArtifact(swarm.SourceArtifact), ArtifactV3Source: cloneTaskArtifactV3Source(swarm.ArtifactV3Source), ArtifactV2Source: cloneTaskArtifactV2Source(swarm.ArtifactV2Source), SourceArguments: args,
		}, nil
	}

	launches := make([]taskLaunchSpec, 0, 8)
	if rawLaunches, ok := args["launches"]; ok {
		if _, exists := args["output_requirements"]; exists {
			return taskCallArguments{}, errors.New("task regular launches must declare output_requirements on each Designer launch, not at top level")
		}
		if _, exists := args["animation_profile"]; exists {
			return taskCallArguments{}, errors.New("task regular launches must declare animation_profile on each Designer launch, not at top level")
		}
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
			if launch.OutputRequirements != nil {
				entry["output_requirements"] = cloneTaskOutputRequirements(launch.OutputRequirements)
			}
			if launch.AnimationProfile != nil {
				entry["animation_profile"] = cloneTaskAnimationProfile(launch.AnimationProfile)
			}
			launch.SourceArguments = cloneGenericMap(entry)
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
		if launch.OutputRequirements != nil {
			args["output_requirements"] = cloneTaskOutputRequirements(launch.OutputRequirements)
		}
		if launch.AnimationProfile != nil {
			args["animation_profile"] = cloneTaskAnimationProfile(launch.AnimationProfile)
		}
		launch.SourceArguments = cloneGenericMap(args)
		launches = append(launches, launch)
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return taskCallArguments{}, err
	}
	var sourceArtifact *pebblestore.SessionArtifactSelectionReference
	var sectionTarget *taskSwarmSectionTarget
	if _, legacy := args["source_artifact"]; legacy {
		if _, native := args["artifact_v3_source"]; native {
			return taskCallArguments{}, errors.New("task accepts artifact_v3_source or source_artifact, not both")
		}
	}
	if rawArtifactV3Source, supplied := args["artifact_v3_source"]; supplied {
		parsedSource, err := parseTaskArtifactV3Source(rawArtifactV3Source)
		if err != nil {
			return taskCallArguments{}, err
		}
		if parsedSource == nil {
			return taskCallArguments{}, errors.New("task artifact_v3_source must be an exact native reference object")
		}
		for i := range launches {
			if !agentruntime.IsDesignerAgentName(launches[i].RequestedSubagentType) || strings.TrimSpace(launches[i].OutputMode) != taskOutputModeManaged {
				return taskCallArguments{}, errors.New("task artifact_v3_source requires managed Designer launches")
			}
			launches[i].ArtifactV3Source = cloneTaskArtifactV3Source(parsedSource)
			if launches[i].SourceArguments == nil {
				launches[i].SourceArguments = map[string]any{}
			}
			launches[i].SourceArguments["artifact_v3_source"] = cloneTaskArtifactV3Source(parsedSource)
		}
		args["artifact_v3_source"] = cloneTaskArtifactV3Source(parsedSource)
	}
	if rawSourceArtifact, supplied := args["source_artifact"]; supplied {
		if rawSourceArtifact == nil {
			return taskCallArguments{}, errors.New("task source_artifact must be an exact ready artifact reference object")
		}
		parsedSourceArtifact, err := parseTaskImageSourceArtifact(rawSourceArtifact)
		if err != nil {
			return taskCallArguments{}, err
		}
		sourceArtifact = parsedSourceArtifact
		for i := range launches {
			if !agentruntime.IsDesignerAgentName(launches[i].RequestedSubagentType) {
				return taskCallArguments{}, errors.New("task regular source_artifact requires every launch to be a Designer")
			}
			launches[i].SourceArtifact = cloneTaskImageSourceArtifact(sourceArtifact)
		}
		args["source_artifact"] = cloneTaskImageSourceArtifact(sourceArtifact)
	}
	if _, single := args["section_target"]; single {
		if _, multi := args["section_targets"]; multi {
			return taskCallArguments{}, errors.New("task accepts section_target or section_targets, not both")
		}
	}
	if rawSectionTargets, supplied := args["section_targets"]; supplied {
		if sourceArtifact == nil && (len(launches) == 0 || launches[0].ArtifactV3Source == nil) {
			return taskCallArguments{}, errors.New("task regular section_targets requires an exact source_artifact or artifact_v3_source")
		}
		var parsedTargets []*taskSwarmSectionTarget
		var err error
		if len(launches) != 0 && launches[0].ArtifactV3Source != nil {
			parsedTargets, err = parseTaskArtifactV3TargetHints(rawSectionTargets)
		} else {
			parsedTargets, err = parseTaskSwarmSectionTargets(rawSectionTargets)
		}
		if err != nil {
			return taskCallArguments{}, err
		}
		args["section_targets"] = cloneTaskSwarmSectionTargets(parsedTargets)
		for i := range launches {
			if launches[i].SourceArguments == nil {
				launches[i].SourceArguments = map[string]any{}
			}
			launches[i].SourceArguments["section_targets"] = cloneTaskSwarmSectionTargets(parsedTargets)
		}
	}
	if rawSectionTarget, supplied := args["section_target"]; supplied {
		if sourceArtifact == nil && (len(launches) == 0 || launches[0].ArtifactV3Source == nil) {
			return taskCallArguments{}, errors.New("task regular section_target requires an exact source_artifact or artifact_v3_source")
		}
		var parsedSectionTarget *taskSwarmSectionTarget
		var err error
		if len(launches) != 0 && launches[0].ArtifactV3Source != nil {
			parsedSectionTarget, err = parseTaskArtifactV3TargetHint(rawSectionTarget)
		} else {
			parsedSectionTarget, err = parseTaskSwarmSectionTarget(rawSectionTarget)
		}
		if err != nil {
			return taskCallArguments{}, err
		}
		if parsedSectionTarget == nil {
			return taskCallArguments{}, errors.New("task section_target must be an object")
		}
		sectionTarget = parsedSectionTarget
		args["section_target"] = cloneTaskSwarmSectionTarget(sectionTarget)
		for i := range launches {
			if launches[i].SourceArguments == nil {
				launches[i].SourceArguments = map[string]any{}
			}
			launches[i].SourceArguments["section_target"] = cloneTaskSwarmSectionTarget(sectionTarget)
		}
	}
	if len(launches) != 0 && launches[0].ArtifactV3Source != nil && len(launches[0].ArtifactV3Source.TargetPartIDs) != 0 {
		declared := map[string]bool{}
		if sectionTarget != nil {
			declared[sectionTarget.ID] = true
		}
		if rawTargets, ok := args["section_targets"]; ok {
			values, _ := parseTaskSwarmSectionTargets(rawTargets)
			for _, item := range values {
				if item != nil {
					declared[item.ID] = true
				}
			}
		}
		// artifact_v3_source.target_part_ids is the authoritative target set.
		// Optional section_target(s) carry display intent only; when present they
		// must agree, but callers do not need to reproduce manifest locators.
		if len(declared) != 0 {
			for _, target := range launches[0].ArtifactV3Source.TargetPartIDs {
				if !declared[target] {
					return taskCallArguments{}, fmt.Errorf("task artifact_v3_source target %q does not match section_target(s)", target)
				}
			}
			if len(declared) != len(uniqueNonEmptyStrings(launches[0].ArtifactV3Source.TargetPartIDs)) {
				return taskCallArguments{}, errors.New("task Artifact V3 section_target(s) do not match target_part_ids")
			}
		}
	}

	return taskCallArguments{
		Action:         action,
		Description:    description,
		Prompt:         prompt,
		Mode:           mode,
		Launches:       launches,
		SourceArtifact: cloneTaskImageSourceArtifact(sourceArtifact),
		ArtifactV3Source: func() *taskArtifactV3Source {
			if len(launches) == 0 {
				return nil
			}
			return cloneTaskArtifactV3Source(launches[0].ArtifactV3Source)
		}(),
		SourceArguments: args,
	}, nil
}

func parseTaskProgram(args map[string]any, prompt string) (*taskProgramSpec, []taskLaunchSpec, error) {
	for key := range args {
		switch key {
		case "action", "description", "prompt", "message", "mode", "workspace_path", "program":
		default:
			return nil, nil, fmt.Errorf("task program start contains unsupported field %q", key)
		}
	}
	raw, ok := args["program"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("task program must be an object")
	}
	for key := range raw {
		switch key {
		case "id", "max_concurrency", "stages", "jobs":
		default:
			return nil, nil, fmt.Errorf("task program contains unsupported field %q", key)
		}
	}
	program := &taskProgramSpec{ID: strings.TrimSpace(mapString(raw, "id"))}
	if !taskProgramIDPattern.MatchString(program.ID) {
		return nil, nil, errors.New("task program id must match ^[a-z][a-z0-9_-]{0,63}$")
	}
	if value, exists := raw["max_concurrency"]; exists {
		cap, err := taskRequiredPositiveIntValue(value, "task program max_concurrency")
		if err != nil {
			return nil, nil, err
		}
		program.MaxConcurrency = &cap
	}

	rawStages, ok := raw["stages"].([]any)
	if !ok || len(rawStages) == 0 {
		return nil, nil, errors.New("task program stages must be a non-empty array")
	}
	stageIndexes := make(map[string]int, len(rawStages))
	for i, value := range rawStages {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("task program stages[%d] must be an object", i)
		}
		for key := range row {
			switch key {
			case "id", "depends_on", "dependency_evidence":
			default:
				return nil, nil, fmt.Errorf("task program stages[%d] contains unsupported field %q", i, key)
			}
		}
		stage := taskProgramStage{ID: strings.TrimSpace(mapString(row, "id")), DependencyEvidence: strings.TrimSpace(mapString(row, "dependency_evidence"))}
		if !taskProgramIDPattern.MatchString(stage.ID) {
			return nil, nil, fmt.Errorf("task program stages[%d] id must match ^[a-z][a-z0-9_-]{0,63}$", i)
		}
		if _, duplicate := stageIndexes[stage.ID]; duplicate {
			return nil, nil, fmt.Errorf("task program stages[%d] duplicates stage id %q", i, stage.ID)
		}
		dependsOn, err := taskProgramStringArray(row, "depends_on", fmt.Sprintf("task program stages[%d] depends_on", i), false)
		if err != nil {
			return nil, nil, err
		}
		if i > 0 && len(dependsOn) == 0 {
			return nil, nil, fmt.Errorf("task program stages[%d] requires depends_on identifying an earlier barrier", i)
		}
		if stage.DependencyEvidence == "" {
			return nil, nil, fmt.Errorf("task program stages[%d] requires dependency_evidence", i)
		}
		for _, dependency := range dependsOn {
			dependencyIndex, exists := stageIndexes[dependency]
			if !exists || dependencyIndex >= i {
				return nil, nil, fmt.Errorf("task program stages[%d] dependency %q must identify an earlier stage", i, dependency)
			}
		}
		stage.DependsOn = dependsOn
		stageIndexes[stage.ID] = i
		program.Stages = append(program.Stages, stage)
	}

	rawJobs, ok := raw["jobs"].([]any)
	if !ok || len(rawJobs) == 0 {
		return nil, nil, errors.New("task program jobs must be a non-empty array")
	}
	if len(rawJobs) > permission.MaxSubagentWaveSize {
		return nil, nil, fmt.Errorf("task program jobs cannot exceed backend safety bound of %d", permission.MaxSubagentWaveSize)
	}
	jobIndexes := make(map[string]int, len(rawJobs))
	jobStages := make(map[string]int, len(rawJobs))
	stageJobCounts := make(map[string]int, len(program.Stages))
	assignmentOwners := make(map[string]string, len(rawJobs))
	launches := make([]taskLaunchSpec, 0, len(rawJobs))
	for i, value := range rawJobs {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("task program jobs[%d] must be an object", i)
		}
		for key := range row {
			switch key {
			case "id", "stage_id", "depends_on", "agent_type", "subagent_type", "meta_prompt", "title", "deliverable", "workspace_path", "owned_scope", "output_mode", "output_requirements", "animation_profile", "acceptance_criteria", "dependency_evidence":
			default:
				return nil, nil, fmt.Errorf("task program jobs[%d] contains unsupported field %q", i, key)
			}
		}
		agentType := strings.TrimSpace(mapString(row, "agent_type"))
		subagentType := strings.TrimSpace(mapString(row, "subagent_type"))
		if agentType != "" && subagentType != "" && !strings.EqualFold(agentType, subagentType) {
			return nil, nil, fmt.Errorf("task program jobs[%d] agent_type conflicts with subagent_type", i)
		}
		job := taskProgramJob{
			ID: strings.TrimSpace(mapString(row, "id")), StageID: strings.TrimSpace(mapString(row, "stage_id")),
			RequestedSubagentType: strings.TrimSpace(firstNonEmptyString(agentType, subagentType)),
			TargetWorkspacePath:   strings.TrimSpace(mapString(row, "workspace_path")),
			MetaPrompt:            strings.TrimSpace(mapString(row, "meta_prompt")), AssignmentLabel: strings.TrimSpace(mapString(row, "title")),
			Deliverable: strings.TrimSpace(mapString(row, "deliverable")), DependencyEvidence: strings.TrimSpace(mapString(row, "dependency_evidence")),
		}
		if !taskProgramIDPattern.MatchString(job.ID) {
			return nil, nil, fmt.Errorf("task program jobs[%d] id must match ^[a-z][a-z0-9_-]{0,63}$", i)
		}
		if _, duplicate := jobIndexes[job.ID]; duplicate {
			return nil, nil, fmt.Errorf("task program jobs[%d] duplicates job id %q", i, job.ID)
		}
		stageIndex, exists := stageIndexes[job.StageID]
		if !exists {
			return nil, nil, fmt.Errorf("task program jobs[%d] references unknown stage %q", i, job.StageID)
		}
		switch {
		case agentruntime.IsCoderAgentName(job.RequestedSubagentType):
			job.RequestedSubagentType = "coder"
		case agentruntime.IsFinderAgentName(job.RequestedSubagentType):
			job.RequestedSubagentType = "finder"
		case agentruntime.IsDesignerAgentName(job.RequestedSubagentType):
			job.RequestedSubagentType = "designer"
		default:
			return nil, nil, fmt.Errorf("task program jobs[%d] agent_type must be coder, finder, or designer", i)
		}
		if job.TargetWorkspacePath == "" && (job.RequestedSubagentType == "coder" || job.RequestedSubagentType == "finder") {
			job.TargetWorkspacePath = strings.TrimSpace(mapString(args, "workspace_path"))
		}
		if job.TargetWorkspacePath != "" && job.RequestedSubagentType == "designer" {
			return nil, nil, fmt.Errorf("task program jobs[%d] workspace_path is supported only for Coder or Finder", i)
		}
		if job.MetaPrompt == "" || job.AssignmentLabel == "" || job.Deliverable == "" || job.DependencyEvidence == "" {
			return nil, nil, fmt.Errorf("task program jobs[%d] requires meta_prompt, title, deliverable, and dependency_evidence", i)
		}
		assignmentKey := strings.ToLower(job.AssignmentLabel + "\x00" + job.MetaPrompt + "\x00" + job.Deliverable)
		if owner, duplicate := assignmentOwners[assignmentKey]; duplicate {
			return nil, nil, fmt.Errorf("task program jobs[%d] copies the reviewable assignment of job %q", i, owner)
		}
		assignmentOwners[assignmentKey] = job.ID
		ownedScope, err := parseTaskOwnedScope(row, fmt.Sprintf("task program jobs[%d]", i))
		if err != nil {
			return nil, nil, err
		}
		for scopeIndex, scope := range ownedScope {
			if err := taskscope.ValidateProgram(scope); err != nil {
				return nil, nil, fmt.Errorf("task program jobs[%d].owned_scope[%d]: %w", i, scopeIndex, err)
			}
		}
		job.OwnedScope = ownedScope
		launch := taskLaunchSpec{RequestedSubagentType: job.RequestedSubagentType, TargetWorkspacePath: job.TargetWorkspacePath, OwnedScope: append([]string(nil), ownedScope...)}
		if err := applyTaskDesignerOutputMode(&launch, mapString(row, "output_mode"), fmt.Sprintf("task program jobs[%d]", i)); err != nil {
			return nil, nil, err
		}
		if rawRequirements, exists := row["output_requirements"]; exists {
			if rawRequirements == nil {
				return nil, nil, fmt.Errorf("task program jobs[%d] output_requirements must be an object", i)
			}
			if err := applyTaskOutputRequirements(&launch, rawRequirements, fmt.Sprintf("task program jobs[%d]", i)); err != nil {
				return nil, nil, err
			}
		}
		job.OutputRequirements = cloneTaskOutputRequirements(launch.OutputRequirements)
		if job.OutputRequirements != nil {
			row["output_requirements"] = cloneTaskOutputRequirements(job.OutputRequirements)
		}
		if rawProfile, exists := row["animation_profile"]; exists {
			if rawProfile == nil {
				return nil, nil, fmt.Errorf("task program jobs[%d] animation_profile must be an object", i)
			}
			if err := applyTaskAnimationProfile(&launch, rawProfile, fmt.Sprintf("task program jobs[%d]", i)); err != nil {
				return nil, nil, err
			}
		}
		job.AnimationProfile = cloneTaskAnimationProfile(launch.AnimationProfile)
		if job.AnimationProfile != nil {
			row["animation_profile"] = cloneTaskAnimationProfile(job.AnimationProfile)
		}
		if job.RequestedSubagentType == "designer" {
			job.OutputMode = launch.OutputMode
		} else if len(ownedScope) == 0 {
			return nil, nil, fmt.Errorf("task program jobs[%d] requires a reviewable owned_scope", i)
		}
		criteria, err := taskProgramStringArray(row, "acceptance_criteria", fmt.Sprintf("task program jobs[%d] acceptance_criteria", i), true)
		if err != nil {
			return nil, nil, err
		}
		job.AcceptanceCriteria = criteria
		dependencies, err := taskProgramStringArray(row, "depends_on", fmt.Sprintf("task program jobs[%d] depends_on", i), false)
		if err != nil {
			return nil, nil, err
		}
		for _, dependency := range dependencies {
			dependencyIndex, exists := jobIndexes[dependency]
			if !exists {
				return nil, nil, fmt.Errorf("task program jobs[%d] dependency %q must identify an earlier job", i, dependency)
			}
			if jobStages[dependency] >= stageIndex || dependencyIndex >= i {
				return nil, nil, fmt.Errorf("task program jobs[%d] dependency %q must belong to an earlier stage", i, dependency)
			}
		}
		job.DependsOn = dependencies
		jobIndexes[job.ID] = i
		jobStages[job.ID] = stageIndex
		stageJobCounts[job.StageID]++
		program.Jobs = append(program.Jobs, job)
		sourceArguments := map[string]any{"program_id": program.ID, "program_job_id": job.ID, "program_stage_id": job.StageID, "acceptance_criteria": append([]string(nil), job.AcceptanceCriteria...), "depends_on": append([]string(nil), job.DependsOn...)}
		if job.TargetWorkspacePath != "" {
			sourceArguments["workspace_path"] = job.TargetWorkspacePath
		}
		if job.OutputRequirements != nil {
			sourceArguments["output_requirements"] = cloneTaskOutputRequirements(job.OutputRequirements)
		}
		if job.AnimationProfile != nil {
			sourceArguments["animation_profile"] = cloneTaskAnimationProfile(job.AnimationProfile)
		}
		launches = append(launches, taskLaunchSpec{
			RequestedSubagentType: job.RequestedSubagentType, TargetWorkspacePath: job.TargetWorkspacePath, MetaPrompt: job.MetaPrompt, AssignmentLabel: job.AssignmentLabel,
			Deliverable: job.Deliverable, OwnedScope: append([]string(nil), job.OwnedScope...), OutputMode: job.OutputMode, OutputRequirements: cloneTaskOutputRequirements(job.OutputRequirements), AnimationProfile: cloneTaskAnimationProfile(job.AnimationProfile), DependencyEvidence: job.DependencyEvidence,
			SourceArguments: sourceArguments,
		})
	}
	for i, stage := range program.Stages {
		if stageJobCounts[stage.ID] == 0 {
			return nil, nil, fmt.Errorf("task program stages[%d] %q has no jobs", i, stage.ID)
		}
	}
	for i := range program.Jobs {
		for j := i + 1; j < len(program.Jobs); j++ {
			left, right := program.Jobs[i], program.Jobs[j]
			if left.StageID != right.StageID {
				continue
			}
			if left.RequestedSubagentType == "coder" && right.RequestedSubagentType == "coder" && strings.TrimSpace(left.TargetWorkspacePath) == strings.TrimSpace(right.TargetWorkspacePath) && taskOwnedScopesOverlap(left.OwnedScope, right.OwnedScope) {
				return nil, nil, fmt.Errorf("task program concurrent Coder owned scopes overlap between jobs %q and %q", left.ID, right.ID)
			}
			if left.RequestedSubagentType == "designer" && right.RequestedSubagentType == "designer" && left.OutputMode == taskOutputModeWorkspace && right.OutputMode == taskOutputModeWorkspace && taskOwnedScopesOverlap(left.OwnedScope, right.OwnedScope) {
				return nil, nil, fmt.Errorf("task program concurrent workspace Designer owned scopes overlap between jobs %q and %q", left.ID, right.ID)
			}
		}
	}
	if err := validateTaskProgramCoderWorkspaceTargets(program.Jobs); err != nil {
		return nil, nil, err
	}
	if program.MaxConcurrency != nil && *program.MaxConcurrency > len(program.Jobs) {
		return nil, nil, errors.New("task program max_concurrency cannot exceed total job count")
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return nil, nil, err
	}
	_ = prompt // The parent prompt remains a manifest field; each job assignment is authoritative for child execution.
	return program, launches, nil
}

func validateTaskProgramCoderWorkspaceTargets(jobs []taskProgramJob) error {
	target := ""
	found := false
	for _, job := range jobs {
		if !agentruntime.IsCoderAgentName(job.RequestedSubagentType) {
			continue
		}
		jobTarget := strings.TrimSpace(job.TargetWorkspacePath)
		if !found {
			target, found = jobTarget, true
			continue
		}
		if jobTarget != target {
			return errors.New("task program Coder jobs must target one workspace so staged integration has one durable parent Git history")
		}
	}
	return nil
}

func taskProgramStringArray(raw map[string]any, key, label string, required bool) ([]string, error) {
	value, exists := raw[key]
	if !exists {
		if required {
			return nil, fmt.Errorf("%s must be a non-empty array", label)
		}
		return nil, nil
	}
	rows, ok := value.([]any)
	if !ok || (required && len(rows) == 0) {
		return nil, fmt.Errorf("%s must be a non-empty array", label)
	}
	out := make([]string, 0, len(rows))
	seen := map[string]struct{}{}
	for i, value := range rows {
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			return nil, fmt.Errorf("%s[%d] must be a non-empty string", label, i)
		}
		if _, duplicate := seen[text]; duplicate {
			return nil, fmt.Errorf("%s[%d] duplicates %q", label, i, text)
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out, nil
}

func taskRequiredPositiveIntValue(value any, label string) (int, error) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < 1 || number > math.MaxInt {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return int(number), nil
}

func taskProgramEffectiveCapacity(totalJobs, readyJobs, activeAccountCapacity int, explicitLowerCap *int) taskProgramCapacity {
	capacity := readyJobs
	if activeAccountCapacity < capacity {
		capacity = activeAccountCapacity
	}
	if explicitLowerCap != nil && *explicitLowerCap < capacity {
		capacity = *explicitLowerCap
	}
	if capacity < 0 {
		capacity = 0
	}
	return taskProgramCapacity{TotalJobs: totalJobs, ReadyJobs: readyJobs, ActiveAccountCapacity: activeAccountCapacity, ExplicitLowerCap: explicitLowerCap, EffectiveCapacity: capacity}
}

func parseTaskSwarmArguments(args map[string]any, prompt, description string) (*taskSwarmSpec, []taskLaunchSpec, error) {
	allowed := map[string]bool{
		"action": true, "description": true, "prompt": true, "message": true, "mode": true, "swarm_mode": true,
		"swarm_strategy": true, "agent_type": true, "subagent_type": true, "agent": true, "purpose": true, "count": true,
		"themes": true, "groups": true, "iteration_controls": true, "output_contract": true, "output_mode": true, "assembly_parts": true,
		"integration_contract": true, "output_requirements": true, "animation_profile": true, "source_artifact": true, "artifact_v3_source": true, "artifact_v2_source": true, "section_target": true, "section_targets": true, "launches": true,
		// concurrency_reason is a regular-launch field. Accept and discard it here as a
		// compatibility no-op so one misplaced advisory hint cannot abort a swarm wave.
		"concurrency_reason": true,
	}
	for key := range args {
		if !allowed[key] {
			return nil, nil, fmt.Errorf("task swarm mode contains unsupported field %q", key)
		}
	}
	delete(args, "concurrency_reason")
	if _, ok := args["launches"]; ok {
		return nil, nil, errors.New("task swarm mode generates its launch wave; launches must be omitted")
	}
	strategy := strings.ToLower(strings.TrimSpace(mapString(args, "swarm_strategy")))
	if strategy == "" {
		strategy = taskSwarmStrategyExplore
	}
	if strategy != taskSwarmStrategyExplore && strategy != taskSwarmStrategyAssembly {
		return nil, nil, fmt.Errorf("task swarm_strategy must be %q", taskSwarmStrategyExplore)
	}
	agentType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(mapString(args, "agent_type"), mapString(args, "subagent_type"), mapString(args, "agent"), mapString(args, "purpose"))))
	switch {
	case agentruntime.IsCoderAgentName(agentType):
		agentType = "coder"
	case agentruntime.IsDesignerAgentName(agentType):
		agentType = "designer"
	case agentruntime.IsIdeaAgentName(agentType):
		agentType = "idea"
	case agentruntime.IsImageAgentName(agentType):
		agentType = "image"
	default:
		return nil, nil, errors.New("task swarm mode agent_type must be coder, designer, image, or idea")
	}
	if strategy == taskSwarmStrategyAssembly && (agentType == "idea" || agentType == "image") {
		return nil, nil, fmt.Errorf("task %s swarms support only swarm_strategy=explore", taskSwarmAgentLabel(agentType))
	}
	count, err := taskPositiveInt(args, "count")
	if err != nil {
		return nil, nil, err
	}
	if count > taskSwarmMaxAgents {
		return nil, nil, fmt.Errorf("task swarm mode count cannot exceed %d", taskSwarmMaxAgents)
	}

	if strategy == taskSwarmStrategyAssembly {
		if _, exists := args["section_target"]; exists {
			return nil, nil, errors.New("task Assembly swarm does not accept section_target")
		}
		if _, exists := args["iteration_controls"]; exists {
			return nil, nil, errors.New("task Assembly swarm does not accept iteration_controls")
		}
		if _, exists := args["source_artifact"]; exists {
			return nil, nil, errors.New("task Assembly swarm does not accept source_artifact")
		}
		if _, exists := args["output_requirements"]; exists {
			return nil, nil, errors.New("task Assembly swarm does not accept output_requirements")
		}
		if _, exists := args["output_mode"]; exists {
			return nil, nil, errors.New("task Assembly swarm does not accept output_mode")
		}
		return parseTaskAssemblySwarm(args, agentType, count)
	}
	if _, ok := args["assembly_parts"]; ok || strings.TrimSpace(mapString(args, "integration_contract")) != "" {
		return nil, nil, errors.New("task Iteration Swarm does not accept assembly_parts or integration_contract")
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
	iterationControls, err := taskSwarmIterationControlsFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if iterationControls != nil && agentType != "designer" && agentType != "image" {
		return nil, nil, errors.New("task swarm iteration_controls is supported only for Designer or image")
	}
	outputContract := strings.TrimSpace(mapString(args, "output_contract"))
	if outputContract == "" {
		outputContract = strings.TrimSpace(description)
	}
	_, outputModeProvided := args["output_mode"]
	outputMode := strings.ToLower(strings.TrimSpace(mapString(args, "output_mode")))
	if agentType == "designer" {
		if outputMode == "" {
			outputMode = taskOutputModeManaged
		}
		if outputMode != taskOutputModeManaged {
			return nil, nil, errors.New("task Designer Iteration Swarm output_mode must be managed; use regular task launches for workspace repository output")
		}
	} else if agentType == "image" {
		if outputMode != "" && outputMode != taskOutputModeManaged {
			return nil, nil, errors.New("task image swarm output_mode must be managed when supplied")
		}
		outputMode = taskOutputModeManaged
	} else if outputModeProvided {
		return nil, nil, errors.New("task swarm output_mode is supported only for Designer or image")
	}
	_, animationProfileProvided := args["animation_profile"]
	if animationProfileProvided && agentType != "designer" {
		return nil, nil, errors.New("task swarm animation_profile is supported only for Designer")
	}
	if animationProfileProvided && args["animation_profile"] == nil {
		return nil, nil, errors.New("task swarm animation_profile must be an object")
	}
	var animationProfile *pebblestore.SessionArtifactAnimationProfile
	if animationProfileProvided {
		animationProfile, err = artifactruntime.ParseAnimationProfile(args["animation_profile"])
		if err != nil {
			return nil, nil, fmt.Errorf("task swarm animation_profile: %w", err)
		}
		args["animation_profile"] = cloneTaskAnimationProfile(animationProfile)
	}
	_, outputRequirementsProvided := args["output_requirements"]
	if outputRequirementsProvided && agentType != "designer" && agentType != "image" {
		return nil, nil, errors.New("task swarm output_requirements is supported only for Designer or image")
	}
	if outputRequirementsProvided && args["output_requirements"] == nil {
		return nil, nil, errors.New("task swarm output_requirements must be an object")
	}
	if value, ok := args["output_requirements"].(map[string]any); outputRequirementsProvided && ok && len(value) == 0 {
		return nil, nil, errors.New("task swarm output_requirements must include a preset or paired width and height")
	}
	var outputRequirements *pebblestore.SessionArtifactOutputRequirements
	if outputRequirementsProvided {
		var err error
		outputRequirements, err = artifactruntime.ParseOutputRequirements(args["output_requirements"])
		if err != nil {
			return nil, nil, fmt.Errorf("task swarm output_requirements: %w", err)
		}
		args["output_requirements"] = cloneTaskOutputRequirements(outputRequirements)
	}
	if _, legacy := args["source_artifact"]; legacy {
		if _, native := args["artifact_v3_source"]; native {
			return nil, nil, errors.New("task accepts artifact_v3_source or source_artifact, not both")
		}
	}
	artifactV3Source, err := parseTaskArtifactV3Source(args["artifact_v3_source"])
	if err != nil {
		return nil, nil, err
	}
	if artifactV3Source != nil && (agentType != "designer" || strategy != taskSwarmStrategyExplore || outputMode != taskOutputModeManaged) {
		return nil, nil, errors.New("task artifact_v3_source requires a managed Designer Iteration Swarm")
	}
	if artifactV3Source != nil {
		args["artifact_v3_source"] = cloneTaskArtifactV3Source(artifactV3Source)
	}
	artifactV2Source, err := parseTaskArtifactV2Source(args["artifact_v2_source"])
	if err != nil {
		return nil, nil, err
	}
	if artifactV3Source != nil && artifactV2Source != nil {
		return nil, nil, errors.New("task accepts artifact_v3_source or artifact_v2_source, not both")
	}
	if artifactV2Source != nil && (agentType != "designer" || strategy != taskSwarmStrategyExplore) {
		return nil, nil, errors.New("task artifact_v2_source requires a Designer Iteration Swarm")
	}
	if artifactV2Source != nil {
		args["artifact_v2_source"] = cloneTaskArtifactV2Source(artifactV2Source)
	}
	if artifactV2Source != nil {
		for _, target := range artifactV2Source.TargetPartIDs {
			found := false
			if single, _ := parseTaskSwarmSectionTarget(args["section_target"]); single != nil && single.ID == target {
				found = true
			}
			if many, _ := parseTaskSwarmSectionTargets(args["section_targets"]); !found {
				for _, item := range many {
					if item != nil && item.ID == target {
						found = true
						break
					}
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("task artifact_v2_source target %q is not declared in section_target(s)", target)
			}
		}
	}
	rawSourceArtifact, sourceArtifactProvided := args["source_artifact"]
	if sourceArtifactProvided && rawSourceArtifact == nil {
		return nil, nil, errors.New("task source_artifact must be an exact ready artifact reference object")
	}
	sourceArtifact, err := parseTaskImageSourceArtifact(rawSourceArtifact)
	if err != nil {
		return nil, nil, err
	}
	if outputMode != taskOutputModeManaged && animationProfile != nil {
		return nil, nil, errors.New("task Designer swarm animation_profile requires managed output")
	}
	if sourceArtifact != nil && agentType != "image" && agentType != "designer" {
		return nil, nil, errors.New("task source_artifact is supported only for Designer or direct image Iteration Swarms")
	}
	if sourceArtifact != nil {
		args["source_artifact"] = cloneTaskImageSourceArtifact(sourceArtifact)
	}
	if _, single := args["section_target"]; single {
		if _, multi := args["section_targets"]; multi {
			return nil, nil, errors.New("task accepts section_target or section_targets, not both")
		}
	}
	var sectionTargets []*taskSwarmSectionTarget
	var sectionTarget *taskSwarmSectionTarget
	if artifactV3Source != nil {
		if raw, supplied := args["section_targets"]; supplied {
			sectionTargets, err = parseTaskArtifactV3TargetHints(raw)
		}
		if raw, supplied := args["section_target"]; supplied && err == nil {
			sectionTarget, err = parseTaskArtifactV3TargetHint(raw)
		}
	} else {
		sectionTargets, err = parseTaskSwarmSectionTargets(args["section_targets"])
		if err == nil {
			sectionTarget, err = parseTaskSwarmSectionTarget(args["section_target"])
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if artifactV3Source != nil && len(artifactV3Source.TargetPartIDs) != 0 && (sectionTarget != nil || len(sectionTargets) != 0) {
		declared := map[string]bool{}
		if sectionTarget != nil {
			declared[sectionTarget.ID] = true
		}
		for _, item := range sectionTargets {
			declared[item.ID] = true
		}
		for _, target := range artifactV3Source.TargetPartIDs {
			if !declared[target] {
				return nil, nil, fmt.Errorf("task artifact_v3_source target %q does not match section_target(s)", target)
			}
		}
		if len(declared) != len(uniqueNonEmptyStrings(artifactV3Source.TargetPartIDs)) {
			return nil, nil, errors.New("task Artifact V3 section_target(s) do not match target_part_ids")
		}
	}
	if len(sectionTargets) != 0 {
		if agentType != "designer" || (sourceArtifact == nil && artifactV3Source == nil && artifactV2Source == nil) {
			return nil, nil, errors.New("task section_targets requires a Designer Iteration Swarm with an exact artifact_v3_source, source_artifact, or artifact_v2_source")
		}
		args["section_targets"] = cloneTaskSwarmSectionTargets(sectionTargets)
	}
	if sectionTarget != nil {
		if agentType != "designer" || (sourceArtifact == nil && artifactV3Source == nil && artifactV2Source == nil) {
			return nil, nil, errors.New("task section_target requires a Designer Iteration Swarm with an exact artifact_v3_source, source_artifact, or artifact_v2_source")
		}
		args["section_target"] = cloneTaskSwarmSectionTarget(sectionTarget)
	}
	if agentType == "idea" && (len(themes) != 0 || len(groups) != 0 || iterationControls != nil || strings.TrimSpace(mapString(args, "output_contract")) != "" || outputModeProvided || outputRequirements != nil || animationProfile != nil) {
		return nil, nil, errors.New("task Idea swarm accepts only mode, swarm_strategy=explore, prompt, agent_type, count, and optional description")
	}

	swarm := &taskSwarmSpec{Strategy: strategy, AgentType: agentType, Count: count, Themes: themes, Groups: groups, OutputContract: outputContract, OutputMode: outputMode, OutputRequirements: cloneTaskOutputRequirements(outputRequirements), AnimationProfile: cloneTaskAnimationProfile(animationProfile), IterationControls: cloneTaskSwarmIterationControls(iterationControls), SourceArtifact: cloneTaskImageSourceArtifact(sourceArtifact), ArtifactV3Source: cloneTaskArtifactV3Source(artifactV3Source), ArtifactV2Source: cloneTaskArtifactV2Source(artifactV2Source), SectionTarget: cloneTaskSwarmSectionTarget(sectionTarget), SectionTargets: cloneTaskSwarmSectionTargets(sectionTargets)}
	launches := make([]taskLaunchSpec, count)
	for i := range launches {
		index := i + 1
		metaPrompt := prompt
		if agentType != "idea" {
			metaPrompt = fmt.Sprintf("Pending Router hydration for swarm item %d.", index)
		}
		assignmentLabel := fmt.Sprintf("%s swarm %d", taskSwarmAgentLabel(agentType), index)
		if agentType == "idea" {
			assignmentLabel = fmt.Sprintf("Agent #%d", index)
		}
		sourceArguments := map[string]any{"swarm_index": index, "swarm_count": count, "swarm_mode": true, "swarm_strategy": strategy, "output_mode": outputMode}
		if outputRequirements != nil {
			sourceArguments["output_requirements"] = cloneTaskOutputRequirements(outputRequirements)
		}
		if animationProfile != nil {
			sourceArguments["animation_profile"] = cloneTaskAnimationProfile(animationProfile)
		}
		if iterationControls != nil {
			sourceArguments["iteration_controls"] = cloneTaskSwarmIterationControls(iterationControls)
		}
		if sourceArtifact != nil {
			sourceArguments["source_artifact"] = cloneTaskImageSourceArtifact(sourceArtifact)
		}
		if artifactV3Source != nil {
			sourceArguments["artifact_v3_source"] = cloneTaskArtifactV3Source(artifactV3Source)
		}
		if artifactV2Source != nil {
			sourceArguments["artifact_v2_source"] = cloneTaskArtifactV2Source(artifactV2Source)
		}
		if sectionTarget != nil {
			sourceArguments["section_target"] = cloneTaskSwarmSectionTarget(sectionTarget)
		}
		if len(sectionTargets) != 0 {
			sourceArguments["section_targets"] = cloneTaskSwarmSectionTargets(sectionTargets)
		}
		launches[i] = taskLaunchSpec{
			RequestedSubagentType: agentType, MetaPrompt: metaPrompt, AssignmentLabel: assignmentLabel,
			Deliverable: outputContract, ConcurrencyReason: "Independent Iteration Swarm alternative", OutputMode: outputMode, OutputRequirements: cloneTaskOutputRequirements(outputRequirements), AnimationProfile: cloneTaskAnimationProfile(animationProfile),
			DependencyEvidence: "The shared parent brief is complete before this task swarm wave starts.",
			StreamKey:          fmt.Sprintf("swarm:%d", index), SwarmMode: true, SwarmStrategy: strategy,
			SourceArtifact: cloneTaskImageSourceArtifact(sourceArtifact), ArtifactV3Source: cloneTaskArtifactV3Source(artifactV3Source), ArtifactV2Source: cloneTaskArtifactV2Source(artifactV2Source), SourceArguments: sourceArguments,
		}
		applyCanonicalCoderOwnedScope(&launches[i])
	}
	if err := validateTaskDesignerScopes(launches); err != nil {
		return nil, nil, err
	}
	return swarm, launches, nil
}

func cloneTaskArtifactV2Source(input *taskArtifactV2Source) *taskArtifactV2Source {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.TargetPartIDs = append([]string(nil), input.TargetPartIDs...)
	return &cloned
}

func parseTaskArtifactV2Source(value any) (*taskArtifactV2Source, error) {
	if value == nil {
		return nil, nil
	}
	if normalized, ok := value.(*taskArtifactV2Source); ok {
		return cloneTaskArtifactV2Source(normalized), nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("task artifact_v2_source must be an exact Artifact V2 reference object")
	}
	for key := range raw {
		switch key {
		case "artifact_id", "published_head_id", "composition_id", "working_revision", "composition_head_revision", "target_part_ids":
		default:
			return nil, fmt.Errorf("task artifact_v2_source contains unsupported field %q", key)
		}
	}
	workingRevision, err := taskRequiredPositiveIntValue(raw["working_revision"], "task artifact_v2_source working_revision")
	if err != nil {
		return nil, err
	}
	headRevision, err := taskRequiredPositiveIntValue(raw["composition_head_revision"], "task artifact_v2_source composition_head_revision")
	if err != nil {
		return nil, err
	}
	targets, err := taskStringArray(raw, "target_part_ids")
	if err != nil || len(targets) == 0 {
		return nil, errors.New("task artifact_v2_source target_part_ids must be a non-empty array")
	}
	out := &taskArtifactV2Source{ArtifactID: strings.TrimSpace(mapString(raw, "artifact_id")), PublishedHeadID: strings.TrimSpace(mapString(raw, "published_head_id")), CompositionID: strings.TrimSpace(mapString(raw, "composition_id")), WorkingRevision: uint64(workingRevision), CompositionHeadRev: uint64(headRevision), TargetPartIDs: targets}
	if out.ArtifactID == "" || out.PublishedHeadID == "" || out.CompositionID == "" {
		return nil, errors.New("task artifact_v2_source requires artifact_id, published_head_id, and composition_id")
	}
	return out, nil
}

func cloneTaskSwarmSectionTargets(input []*taskSwarmSectionTarget) []*taskSwarmSectionTarget {
	if len(input) == 0 {
		return nil
	}
	out := make([]*taskSwarmSectionTarget, len(input))
	for i := range input {
		out[i] = cloneTaskSwarmSectionTarget(input[i])
	}
	return out
}

func parseTaskSwarmSectionTargets(value any) ([]*taskSwarmSectionTarget, error) {
	if value == nil {
		return nil, nil
	}
	if normalized, ok := value.([]*taskSwarmSectionTarget); ok {
		return cloneTaskSwarmSectionTargets(normalized), nil
	}
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 || len(raw) > pebblestore.SessionArtifactMaxParts {
		return nil, errors.New("task section_targets must be a non-empty bounded array")
	}
	out := make([]*taskSwarmSectionTarget, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		target, err := parseTaskSwarmSectionTarget(item)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[target.ID]; duplicate {
			return nil, fmt.Errorf("task section_targets contains duplicate id %q", target.ID)
		}
		seen[target.ID] = struct{}{}
		out = append(out, target)
	}
	return out, nil
}

func cloneTaskSwarmSectionTarget(input *taskSwarmSectionTarget) *taskSwarmSectionTarget {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

// Artifact V3 source identity already carries the server-authenticated target
// Part IDs. A model-authored section_target is only a display/intent hint; it
// must not redundantly reproduce selector/state/page/spatial locator details.
// The coordinator validates every ID against the exact Git manifest before a
// Designer is allocated.
func parseTaskArtifactV3TargetHint(value any) (*taskSwarmSectionTarget, error) {
	if value == nil {
		return nil, nil
	}
	if normalized, ok := value.(*taskSwarmSectionTarget); ok {
		return cloneTaskSwarmSectionTarget(normalized), nil
	}
	if normalized, ok := value.(taskSwarmSectionTarget); ok {
		return cloneTaskSwarmSectionTarget(&normalized), nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("task Artifact V3 section_target must be an object")
	}
	for key := range raw {
		switch key {
		case "id", "label", "kind", "description", "start_ms", "end_ms", "x", "y", "width", "height", "page", "state_id", "selector":
		default:
			return nil, fmt.Errorf("task Artifact V3 section_target contains unsupported field %q", key)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("task Artifact V3 section_target must be an object")
	}
	var target taskSwarmSectionTarget
	if err := json.Unmarshal(encoded, &target); err != nil {
		return nil, fmt.Errorf("task Artifact V3 section_target is invalid: %w", err)
	}
	target.ID = strings.TrimSpace(target.ID)
	target.Label = strings.TrimSpace(target.Label)
	target.Kind = strings.ToLower(strings.TrimSpace(target.Kind))
	target.Description = strings.TrimSpace(target.Description)
	if target.ID == "" || target.Label == "" {
		return nil, errors.New("task Artifact V3 section_target requires id and label")
	}
	if target.Kind == "" {
		target.Kind = "semantic"
	}
	switch target.Kind {
	case "temporal", "spatial", "page", "state", "selector", "semantic", "file":
	default:
		return nil, errors.New("task Artifact V3 section_target kind is invalid")
	}
	return &target, nil
}

func parseTaskArtifactV3TargetHints(value any) ([]*taskSwarmSectionTarget, error) {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 || len(raw) > pebblestore.SessionArtifactMaxParts {
		return nil, errors.New("task Artifact V3 section_targets must be a non-empty bounded array")
	}
	out := make([]*taskSwarmSectionTarget, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		target, err := parseTaskArtifactV3TargetHint(item)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[target.ID]; duplicate {
			return nil, fmt.Errorf("task Artifact V3 section_targets contains duplicate id %q", target.ID)
		}
		seen[target.ID] = struct{}{}
		out = append(out, target)
	}
	return out, nil
}

func parseTaskSwarmSectionTarget(value any) (*taskSwarmSectionTarget, error) {
	if value == nil {
		return nil, nil
	}
	if normalized, ok := value.(*taskSwarmSectionTarget); ok {
		if normalized == nil {
			return nil, nil
		}
		return cloneTaskSwarmSectionTarget(normalized), nil
	}
	if normalized, ok := value.(taskSwarmSectionTarget); ok {
		return cloneTaskSwarmSectionTarget(&normalized), nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("task section_target must be an object")
	}
	for key := range raw {
		switch key {
		case "id", "label", "kind", "description", "start_ms", "end_ms", "x", "y", "width", "height", "page", "state_id", "selector":
		default:
			return nil, fmt.Errorf("task section_target contains unsupported field %q", key)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("task section_target must be an object")
	}
	var part pebblestore.SessionArtifactPart
	if err := json.Unmarshal(encoded, &part); err != nil {
		return nil, fmt.Errorf("task section_target is invalid: %w", err)
	}
	part.ID, part.Label, part.Kind = strings.TrimSpace(part.ID), strings.TrimSpace(part.Label), strings.ToLower(strings.TrimSpace(part.Kind))
	if part.ID == "" || part.Label == "" {
		return nil, errors.New("task section_target requires id and label")
	}
	if part.Kind == "" {
		part.Kind = "temporal"
	}
	target := &taskSwarmSectionTarget{ID: part.ID, Label: part.Label, Kind: part.Kind, Description: strings.TrimSpace(part.Description), StartMs: part.StartMs, EndMs: part.EndMs, X: part.X, Y: part.Y, Width: part.Width, Height: part.Height, Page: part.Page, StateID: strings.TrimSpace(part.StateID), Selector: strings.TrimSpace(part.Selector)}
	switch part.Kind {
	case "temporal":
		if part.StartMs < 0 || part.EndMs <= part.StartMs {
			return nil, errors.New("temporal task section_target requires a valid start_ms/end_ms range")
		}
	case "spatial":
		if part.X < 0 || part.Y < 0 || part.Width <= 0 || part.Height <= 0 || part.X+part.Width > 1 || part.Y+part.Height > 1 {
			return nil, errors.New("spatial task section_target requires normalized x/y/width/height")
		}
	case "page":
		if part.Page < 1 {
			return nil, errors.New("page task section_target requires page")
		}
	case "state":
		if target.StateID == "" {
			return nil, errors.New("state task section_target requires state_id")
		}
	case "selector":
		if target.Selector == "" {
			return nil, errors.New("selector task section_target requires selector")
		}
	case "semantic":
	default:
		return nil, errors.New("task section_target kind is invalid")
	}
	return target, nil
}

func validateTaskSwarmLaunchEnabled(parsed taskCallArguments) error {
	if parsed.Swarm != nil && parsed.Swarm.Strategy == taskSwarmStrategyAssembly && !taskAssemblySwarmLaunchEnabled {
		return errors.New("task Assembly Swarm is not available in this launch")
	}
	return nil
}

func parseTaskAssemblySwarm(args map[string]any, agentType string, count int) (*taskSwarmSpec, []taskLaunchSpec, error) {
	for _, key := range []string{"themes", "groups", "iteration_controls", "output_contract", "output_mode", "owned_scope_template"} {
		if _, ok := args[key]; ok {
			return nil, nil, fmt.Errorf("task Assembly swarm does not accept Explore field %q", key)
		}
	}
	integrationContract := strings.TrimSpace(mapString(args, "integration_contract"))
	if integrationContract == "" {
		return nil, nil, errors.New("task Assembly swarm requires integration_contract describing the parent-owned final deliverable")
	}
	raw, ok := args["assembly_parts"]
	if !ok {
		return nil, nil, errors.New("task Assembly swarm requires assembly_parts")
	}
	rows, ok := raw.([]any)
	if !ok || len(rows) == 0 {
		return nil, nil, errors.New("task Assembly swarm assembly_parts must be a non-empty array")
	}
	if len(rows) != count {
		return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts must contain exactly %d entries", count)
	}
	parts := make([]taskSwarmAssemblyPart, len(rows))
	seenNames := map[string]struct{}{}
	for i, rawPart := range rows {
		row, ok := rawPart.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] must be an object", i)
		}
		for key := range row {
			if key != "name" && key != "instructions" && key != "owned_scope" {
				return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] contains unsupported field %q", i, key)
			}
		}
		name := strings.TrimSpace(mapString(row, "name"))
		if name == "" {
			return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] requires name", i)
		}
		normalizedName := strings.ToLower(name)
		if _, duplicate := seenNames[normalizedName]; duplicate {
			return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] duplicates another name", i)
		}
		seenNames[normalizedName] = struct{}{}
		scopes, err := parseTaskOwnedScope(row, fmt.Sprintf("task Assembly swarm assembly_parts[%d]", i))
		if err != nil {
			return nil, nil, err
		}
		if len(scopes) == 0 {
			return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] requires one or more owned_scope paths", i)
		}
		for j, scope := range scopes {
			if err := validateTaskSwarmOwnedScope(scope); err != nil {
				return nil, nil, fmt.Errorf("task Assembly swarm assembly_parts[%d] owned_scope[%d] %w", i, j, err)
			}
		}
		parts[i] = taskSwarmAssemblyPart{Name: name, Instructions: strings.TrimSpace(mapString(row, "instructions")), OwnedScope: scopes}
	}
	for left := range parts {
		for right := left + 1; right < len(parts); right++ {
			if taskOwnedScopesOverlap(parts[left].OwnedScope, parts[right].OwnedScope) {
				return nil, nil, fmt.Errorf("task Assembly swarm owned scopes overlap between assembly_parts[%d] and assembly_parts[%d]", left, right)
			}
		}
	}
	outputMode := ""
	if agentType == "designer" {
		outputMode = taskOutputModeWorkspace
	}
	swarm := &taskSwarmSpec{Strategy: taskSwarmStrategyAssembly, AgentType: agentType, Count: count, OutputMode: outputMode, AssemblyParts: parts, IntegrationContract: integrationContract}
	launches := make([]taskLaunchSpec, count)
	for i := range parts {
		part := parts[i]
		partCopy := part
		outputMode := ""
		if agentType == "designer" {
			outputMode = taskOutputModeWorkspace
		}
		launches[i] = taskLaunchSpec{
			RequestedSubagentType: agentType, MetaPrompt: fmt.Sprintf("Pending Router hydration for Assembly part %q.", part.Name),
			AssignmentLabel: part.Name, Deliverable: integrationContract, ConcurrencyReason: "Complementary Assembly part with distinct ownership.",
			OwnedScope: append([]string(nil), part.OwnedScope...), OutputMode: outputMode, DependencyEvidence: "All Assembly parts and the parent integration contract are complete before launch.",
			StreamKey: fmt.Sprintf("swarm:%d", i+1), SwarmMode: true, SwarmStrategy: taskSwarmStrategyAssembly,
			AssemblyPart: &partCopy, IntegrationContract: integrationContract,
			SourceArguments: map[string]any{"swarm_index": i + 1, "swarm_mode": true, "swarm_strategy": taskSwarmStrategyAssembly, "assembly_part": part, "integration_contract": integrationContract},
		}
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
	case "image":
		return "Image"
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

func taskSwarmIterationControlsFromArgs(args map[string]any) (*taskSwarmIterationControls, error) {
	value, ok := args["iteration_controls"]
	if !ok || value == nil {
		return nil, nil
	}
	row, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("task swarm iteration_controls must be an object")
	}
	for key := range row {
		if key != "preserve" && key != "change" && key != "exclude" {
			return nil, fmt.Errorf("task swarm iteration_controls contains unsupported field %q", key)
		}
	}
	preserve, err := taskStringArray(row, "preserve")
	if err != nil {
		return nil, err
	}
	change, err := taskStringArray(row, "change")
	if err != nil {
		return nil, err
	}
	exclude, err := taskStringArray(row, "exclude")
	if err != nil {
		return nil, err
	}
	if len(change) == 0 {
		return nil, errors.New("task swarm iteration_controls requires at least one parent-controlled change dimension")
	}
	return &taskSwarmIterationControls{Preserve: preserve, Change: change, Exclude: exclude}, nil
}

func cloneTaskSwarmIterationControls(input *taskSwarmIterationControls) *taskSwarmIterationControls {
	if input == nil {
		return nil
	}
	return &taskSwarmIterationControls{
		Preserve: append([]string(nil), input.Preserve...),
		Change:   append([]string(nil), input.Change...),
		Exclude:  append([]string(nil), input.Exclude...),
	}
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
	for i, value := range values {
		if _, _, err := taskscope.Canonical(value); err != nil {
			return nil, fmt.Errorf("%s owned_scope[%d]: %w", label, i, err)
		}
		out = append(out, strings.TrimSpace(value))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func parseTaskArtifactV3Source(raw any) (*taskArtifactV3Source, error) {
	if raw == nil {
		return nil, nil
	}
	if normalized, ok := raw.(*taskArtifactV3Source); ok {
		return cloneTaskArtifactV3Source(normalized), nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("task artifact_v3_source must be an object")
	}
	var source taskArtifactV3Source
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("task artifact_v3_source is invalid: %w", err)
	}
	source.SessionID = strings.TrimSpace(source.SessionID)
	source.ArtifactID = strings.TrimSpace(source.ArtifactID)
	source.CommitOID = strings.TrimSpace(source.CommitOID)
	source.TargetPartIDs = uniqueNonEmptyStrings(source.TargetPartIDs)
	if source.SessionID == "" || source.ArtifactID == "" || source.CommitOID == "" || source.ProjectionSeq == 0 {
		return nil, errors.New("task artifact_v3_source requires session_id, artifact_id, commit_oid, and projection_seq")
	}
	return &source, nil
}

func cloneTaskArtifactV3Source(input *taskArtifactV3Source) *taskArtifactV3Source {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.TargetPartIDs = append([]string(nil), input.TargetPartIDs...)
	return &cloned
}

func parseTaskImageSourceArtifact(raw any) (*pebblestore.SessionArtifactSelectionReference, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("task source_artifact must be an object")
	}
	var ref pebblestore.SessionArtifactSelectionReference
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return nil, fmt.Errorf("task source_artifact is invalid: %w", err)
	}
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
	if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq < 1 || ref.EventSeq > uint64(math.MaxInt) {
		return nil, errors.New("task source_artifact requires session_id, collection_id, variant_id, and event_seq from one exact ready artifact reference")
	}
	return &ref, nil
}

func cloneTaskImageSourceArtifact(input *pebblestore.SessionArtifactSelectionReference) *pebblestore.SessionArtifactSelectionReference {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func equalTaskImageSourceArtifact(left, right *pebblestore.SessionArtifactSelectionReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.SessionID == right.SessionID && left.CollectionID == right.CollectionID && left.VariantID == right.VariantID && left.EventSeq == right.EventSeq
}

func cloneTaskOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func cloneTaskAnimationProfile(input *pebblestore.SessionArtifactAnimationProfile) *pebblestore.SessionArtifactAnimationProfile {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func applyTaskOutputRequirements(launch *taskLaunchSpec, raw any, label string) error {
	if launch == nil {
		return errors.New("task launch is required")
	}
	if raw == nil {
		launch.OutputRequirements = nil
		if launch.SourceArguments != nil {
			delete(launch.SourceArguments, "output_requirements")
		}
		return nil
	}
	if !agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) && !agentruntime.IsImageAgentName(launch.RequestedSubagentType) {
		return fmt.Errorf("%s output_requirements is supported only for Designer or image", label)
	}
	if value, ok := raw.(map[string]any); ok && len(value) == 0 {
		return fmt.Errorf("%s output_requirements must include a preset or paired width and height", label)
	}
	resolved, err := artifactruntime.ParseOutputRequirements(raw)
	if err != nil {
		return fmt.Errorf("%s output_requirements: %w", label, err)
	}
	launch.OutputRequirements = cloneTaskOutputRequirements(resolved)
	if launch.SourceArguments != nil {
		if resolved == nil {
			delete(launch.SourceArguments, "output_requirements")
		} else {
			launch.SourceArguments["output_requirements"] = cloneTaskOutputRequirements(resolved)
		}
	}
	return nil
}

func applyTaskAnimationProfile(launch *taskLaunchSpec, raw any, label string) error {
	if launch == nil {
		return errors.New("task launch is required")
	}
	if !agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) {
		return fmt.Errorf("%s animation_profile is supported only for Designer", label)
	}
	resolved, err := artifactruntime.ParseAnimationProfile(raw)
	if err != nil {
		return fmt.Errorf("%s animation_profile: %w", label, err)
	}
	launch.AnimationProfile = cloneTaskAnimationProfile(resolved)
	if launch.SourceArguments != nil {
		launch.SourceArguments["animation_profile"] = cloneTaskAnimationProfile(resolved)
	}
	return nil
}

func applyTaskDesignerOutputMode(launch *taskLaunchSpec, rawMode, label string) error {
	if launch == nil {
		return errors.New("task launch is required")
	}
	mode := strings.ToLower(strings.TrimSpace(rawMode))
	if !agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) && !agentruntime.IsImageAgentName(launch.RequestedSubagentType) {
		if mode != "" {
			return fmt.Errorf("%s output_mode is supported only for Designer or image", label)
		}
		return nil
	}
	if agentruntime.IsImageAgentName(launch.RequestedSubagentType) {
		if mode != "" && mode != taskOutputModeManaged {
			return fmt.Errorf("%s image output_mode must be managed", label)
		}
		if len(launch.OwnedScope) != 0 {
			return fmt.Errorf("%s managed image must omit owned_scope", label)
		}
		launch.OutputMode = taskOutputModeManaged
		return nil
	}
	if mode == "" {
		mode = taskOutputModeManaged
	}
	if mode != taskOutputModeManaged && mode != taskOutputModeWorkspace {
		return fmt.Errorf("%s Designer output_mode must be managed or workspace", label)
	}
	if mode == taskOutputModeManaged && len(launch.OwnedScope) != 0 {
		return fmt.Errorf("%s managed Designer must omit owned_scope", label)
	}
	if mode == taskOutputModeWorkspace && len(launch.OwnedScope) == 0 {
		return fmt.Errorf("%s workspace Designer requires a concrete workspace-relative owned_scope", label)
	}
	launch.OutputMode = mode
	return nil
}

func validateTaskDesignerScopes(launches []taskLaunchSpec) error {
	designerIndexes := make([]int, 0, len(launches))
	for i := range launches {
		if !agentruntime.IsDesignerAgentName(launches[i].RequestedSubagentType) && !agentruntime.IsImageAgentName(launches[i].RequestedSubagentType) {
			continue
		}
		if launches[i].OutputMode == taskOutputModeManaged {
			if len(launches[i].OwnedScope) != 0 {
				return fmt.Errorf("task launches[%d] managed Designer must omit owned_scope", i)
			}
			continue
		}
		if launches[i].OutputMode != taskOutputModeWorkspace {
			return fmt.Errorf("task launches[%d] Designer output_mode must be managed or workspace", i)
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
	// An omitted scope intentionally retains whole-worktree compatibility.
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
		"target_session_id",
		"targetSessionID",
		"collection_id",
		"collectionID",
		"variant_id",
		"variantID",
		"artifact_target",
		"artifactTarget",
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

func taskToolNameInSlice(values []string, want string) bool {
	want = canonicalToolName(want)
	for _, value := range values {
		if canonicalToolName(value) == want {
			return true
		}
	}
	return false
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
	case "manage_workspace":
		args, parseErr := parseManageWorkspaceArguments(arguments)
		if parseErr != nil {
			return "", parseErr
		}
		if args.Action != "create" && args.Action != "update" && args.Action != "edit" && args.Action != "delete" {
			return arguments, nil
		}
		payload, err := s.buildManageWorkspacePermissionPayload(sessionID, arguments)
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
		payload["workspace_path"] = strings.TrimSpace(firstNonEmptyString(session.WorktreeRootPath, session.WorkspacePath))
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

func taskPathWithinRoot(root, target string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Service) resolveTaskTargetWorkspace(parentSession pebblestore.SessionSnapshot, principal identity.Principal, launch taskLaunchSpec) (string, string, error) {
	requested := strings.TrimSpace(launch.TargetWorkspacePath)
	if requested == "" {
		return strings.TrimSpace(firstNonEmptyString(parentSession.WorktreeRootPath, parentSession.WorkspacePath)), strings.TrimSpace(parentSession.WorkspaceName), nil
	}
	if !agentruntime.IsCoderAgentName(launch.RequestedSubagentType) && !agentruntime.IsFinderAgentName(launch.RequestedSubagentType) {
		return "", "", errors.New("task workspace_path is supported only for Coder or Finder launches")
	}
	principal, err := principalForRunWorkspaceScope(parentSession, principal)
	if err != nil {
		return "", "", err
	}
	scope, err := s.resolveRunWorkspaceScope(parentSession, principal)
	if err != nil {
		return "", "", fmt.Errorf("resolve parent shared workspace roots: %w", err)
	}
	allowedRoots := append([]string(nil), scope.Roots...)
	if s != nil && s.workspace != nil {
		sourcePath := strings.TrimSpace(firstNonEmptyString(mapString(parentSession.Metadata, "swarm_v3_source_workspace_path"), parentSession.WorkspacePath))
		if saved, savedErr := s.workspace.ScopeForPathForPrincipal(principal, sourcePath); savedErr == nil && saved.Matched {
			allowedRoots = append(allowedRoots, saved.Directories...)
		}
	}
	if !filepath.IsAbs(requested) {
		base := strings.TrimSpace(firstNonEmptyString(mapString(parentSession.Metadata, "swarm_v3_source_workspace_path"), parentSession.WorkspacePath))
		requested = filepath.Join(base, requested)
	}
	target, err := normalizeRunScopePath(requested)
	if err != nil {
		return "", "", fmt.Errorf("resolve task workspace_path: %w", err)
	}
	authorized := false
	for _, root := range allowedRoots {
		canonicalRoot, rootErr := normalizeRunScopePath(root)
		if rootErr == nil && taskPathWithinRoot(canonicalRoot, target) {
			authorized = true
			break
		}
	}
	if !authorized {
		return "", "", fmt.Errorf("task workspace_path %q is outside the parent session's authorized shared workspace roots", requested)
	}
	name := filepath.Base(target)
	if s != nil && s.workspace != nil {
		resolved, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, target)
		if scopeErr != nil {
			return "", "", fmt.Errorf("resolve task workspace identity: %w", scopeErr)
		}
		if strings.TrimSpace(resolved.ResolvedPath) != "" {
			target = strings.TrimSpace(resolved.ResolvedPath)
		}
		if strings.TrimSpace(resolved.WorkspaceName) != "" {
			name = strings.TrimSpace(resolved.WorkspaceName)
		}
	}
	return target, name, nil
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
		_, routerProfile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, parentSession.AccountScopeID, agentruntime.RouterAgentID, "")
		if err != nil {
			return pebblestore.AgentProfile{}, false, "", fmt.Errorf("resolve Idea Router model: %w", err)
		}
		profile, err := s.agents.ResolveSystemAgent(agentruntime.IdeaAgentID, routerProfile)
		return profile, false, "", err
	}
	if agentruntime.IsImageAgentName(requested) {
		_, profile, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, parentSession.AccountScopeID, agentruntime.ImageAgentID, "")
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
	approvedLaunches := envelope.Manifest.Launches
	if envelope.Manifest.Program != nil && len(approvedLaunches) != len(launchSpecs) {
		byJobID := make(map[string]taskLaunchManifestRow, len(approvedLaunches))
		for _, row := range approvedLaunches {
			if jobID, _ := row.SourceArguments["program_job_id"].(string); strings.TrimSpace(jobID) != "" {
				byJobID[strings.TrimSpace(jobID)] = row
			}
		}
		approvedLaunches = approvedLaunches[:0]
		for _, spec := range launchSpecs {
			jobID, _ := spec.SourceArguments["program_job_id"].(string)
			row, ok := byJobID[strings.TrimSpace(jobID)]
			if !ok {
				return taskLaunchManifest{}, fmt.Errorf("approved task manifest is missing program job %q", jobID)
			}
			approvedLaunches = append(approvedLaunches, row)
		}
	}
	if len(approvedLaunches) != len(launchSpecs) {
		return taskLaunchManifest{}, errors.New("approved task manifest launch count mismatch")
	}
	envelope.Manifest.Launches = approvedLaunches
	for i := range launchSpecs {
		row := approvedLaunches[i]
		if !strings.EqualFold(strings.TrimSpace(row.RequestedSubagentType), strings.TrimSpace(launchSpecs[i].RequestedSubagentType)) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d target mismatch", i)
		}
		if row.ProfileSnapshot == nil {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d is missing profile snapshot", i)
		}
		if strings.TrimSpace(row.OutputMode) != strings.TrimSpace(launchSpecs[i].OutputMode) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d output mode mismatch", i)
		}
		if !reflect.DeepEqual(row.OutputRequirements, launchSpecs[i].OutputRequirements) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d output requirements mismatch", i)
		}
		if !reflect.DeepEqual(row.AnimationProfile, launchSpecs[i].AnimationProfile) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d animation profile mismatch", i)
		}
		if !reflect.DeepEqual(row.ArtifactV3Source, launchSpecs[i].ArtifactV3Source) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d native source artifact mismatch", i)
		}
		if !equalTaskImageSourceArtifact(row.SourceArtifact, launchSpecs[i].SourceArtifact) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d source artifact mismatch", i)
		}
		if row.SourceArguments != nil {
			row.SourceArguments = cloneGenericMap(row.SourceArguments)
			if launchSpecs[i].OutputRequirements != nil {
				row.SourceArguments["output_requirements"] = cloneTaskOutputRequirements(launchSpecs[i].OutputRequirements)
			}
			if launchSpecs[i].AnimationProfile != nil {
				row.SourceArguments["animation_profile"] = cloneTaskAnimationProfile(launchSpecs[i].AnimationProfile)
			}
		}
		approvedLaunches[i] = row
		if !reflect.DeepEqual(row.OwnedScope, launchSpecs[i].OwnedScope) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d owned scope mismatch", i)
		}
		if strings.TrimSpace(row.TargetWorkspacePath) != strings.TrimSpace(launchSpecs[i].TargetWorkspacePath) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d workspace target mismatch", i)
		}
		if agentruntime.IsImageAgentName(launchSpecs[i].RequestedSubagentType) {
			if row.ResolvedTools == nil || len(row.ResolvedTools.AllowedTools) != 1 || !taskToolNameInSlice(row.ResolvedTools.AllowedTools, "manage_artifact") {
				return taskLaunchManifest{}, fmt.Errorf("approved managed Image manifest launch %d must allow only manage_artifact", i)
			}
		}
		if agentruntime.IsDesignerAgentName(launchSpecs[i].RequestedSubagentType) || agentruntime.IsImageAgentName(launchSpecs[i].RequestedSubagentType) {
			switch strings.TrimSpace(launchSpecs[i].OutputMode) {
			case taskOutputModeManaged:
				requiredManagedTool := "artifact_v3_author"
				if agentruntime.IsImageAgentName(launchSpecs[i].RequestedSubagentType) {
					requiredManagedTool = "manage_artifact"
				}
				if row.ResolvedTools == nil || !taskToolNameInSlice(row.ResolvedTools.AllowedTools, requiredManagedTool) {
					return taskLaunchManifest{}, fmt.Errorf("approved managed creative manifest launch %d is missing %s", i, requiredManagedTool)
				}
				if agentruntime.IsDesignerAgentName(launchSpecs[i].RequestedSubagentType) && taskToolNameInSlice(row.ResolvedTools.AllowedTools, "manage_artifact") {
					return taskLaunchManifest{}, fmt.Errorf("approved managed Designer manifest launch %d retains legacy manage_artifact write authority", i)
				}
				for _, name := range []string{"write", "edit"} {
					if !taskToolNameInSlice(row.DisabledTools, name) || !taskToolNameInSlice(row.ResolvedTools.DisabledTools, name) || taskToolNameInSlice(row.ResolvedTools.AllowedTools, name) {
						return taskLaunchManifest{}, fmt.Errorf("approved managed Designer manifest launch %d retains checkout mutation tool %q", i, name)
					}
				}
			case taskOutputModeWorkspace:
				if row.ResolvedTools == nil {
					return taskLaunchManifest{}, fmt.Errorf("approved workspace Designer manifest launch %d is missing resolved tools", i)
				}
				for _, name := range []string{"read", "search", "find", "list", "write", "edit"} {
					if !taskToolNameInSlice(row.ResolvedTools.AllowedTools, name) {
						return taskLaunchManifest{}, fmt.Errorf("approved workspace Designer manifest launch %d is missing tool %q", i, name)
					}
				}
				for _, name := range []string{"media_inspect", "bash", "git_status", "git_diff", "git_add", "git_commit", "manage_artifact"} {
					if taskToolNameInSlice(row.ResolvedTools.AllowedTools, name) || !taskToolNameInSlice(row.ResolvedTools.DisabledTools, name) {
						return taskLaunchManifest{}, fmt.Errorf("approved workspace Designer manifest launch %d retains forbidden tool %q", i, name)
					}
				}
			}
		}
		if strings.TrimSpace(row.StreamKey) != strings.TrimSpace(launchSpecs[i].StreamKey) || row.SwarmMode != launchSpecs[i].SwarmMode || strings.TrimSpace(row.SwarmStrategy) != strings.TrimSpace(launchSpecs[i].SwarmStrategy) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d swarm identity mismatch", i)
		}
		if !reflect.DeepEqual(row.AssemblyPart, launchSpecs[i].AssemblyPart) || strings.TrimSpace(row.IntegrationContract) != strings.TrimSpace(launchSpecs[i].IntegrationContract) {
			return taskLaunchManifest{}, fmt.Errorf("approved task manifest launch %d Assembly contract mismatch", i)
		}
	}
	return envelope.Manifest, nil
}

func (s *Service) buildTaskLaunchPermissionPayload(sessionID, sessionMode string, call tool.Call) (taskLaunchManifest, error) {
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	parsed, err = s.resolveApprovedCheckpointTaskProgram(sessionID, parsed)
	if err != nil {
		return taskLaunchManifest{}, err
	}
	if err := validateTaskSwarmLaunchEnabled(parsed); err != nil {
		return taskLaunchManifest{}, err
	}
	if parsed.Action == taskProgramActionStatus {
		manifest := taskLaunchManifest{
			PathID: taskLaunchPermissionPathID, Goal: "task program " + parsed.Action, Description: parsed.Description,
			Action: parsed.Action, ParentMode: sessionruntime.NormalizeMode(sessionMode), TaskMode: parsed.Mode,
			ProgramID:       parsed.ProgramID,
			SourceArguments: parsed.SourceArguments,
		}
		digest, digestErr := taskLaunchManifestDigest(manifest)
		if digestErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("hash task program lifecycle manifest: %w", digestErr)
		}
		manifest.ManifestHash = digest
		approvedManifest := manifest
		manifest.ApprovedArguments = map[string]any{"manifest_hash": digest, "manifest": approvedManifest}
		return manifest, nil
	}

	parentSession, ok, sessionErr := s.sessions.GetSession(sessionID)
	if sessionErr != nil {
		return taskLaunchManifest{}, sessionErr
	}
	if !ok {
		return taskLaunchManifest{}, fmt.Errorf("session %q not found", sessionID)
	}
	if parsed.Swarm != nil && parsed.Swarm.AgentType == "image" {
		images := make([]taskImageManifestRow, len(parsed.Launches))
		for i, launch := range parsed.Launches {
			theme := ""
			if i < len(parsed.Swarm.Themes) {
				theme = strings.TrimSpace(parsed.Swarm.Themes[i])
			}
			images[i] = taskImageManifestRow{Index: i + 1, Theme: theme, StreamKey: strings.TrimSpace(launch.StreamKey), OutputRequirements: cloneTaskOutputRequirements(launch.OutputRequirements), SourceArtifact: cloneTaskImageSourceArtifact(parsed.Swarm.SourceArtifact)}
		}
		manifest := taskLaunchManifest{
			PathID: taskLaunchPermissionPathID, Goal: parsed.Description, ImageCount: len(images), Description: parsed.Description,
			Prompt: parsed.Prompt, Action: parsed.Action, ParentMode: sessionruntime.NormalizeMode(sessionMode), TaskMode: parsed.Mode,
			SwarmAgentType: "image", SwarmStrategy: parsed.Swarm.Strategy, Images: images, ExecutionFormat: taskExecutionFormatImageDirect,
			SourceArguments: parsed.SourceArguments,
		}
		if parent, found := s.lookupTaskLaunchParentSession(sessionID, manifest.ParentMode); found {
			manifest.Parent = parent
			manifest.TargetWorkspacePath = strings.TrimSpace(firstNonEmptyString(parent.WorktreeRootPath, parent.WorkspacePath))
			manifest.TargetWorkspaceName = strings.TrimSpace(parent.WorkspaceName)
		}
		digest, digestErr := taskLaunchManifestDigest(manifest)
		if digestErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("hash direct image swarm manifest: %w", digestErr)
		}
		manifest.ManifestHash = digest
		approvedManifest := manifest
		manifest.ApprovedArguments = map[string]any{"manifest_hash": digest, "manifest": approvedManifest}
		return manifest, nil
	}
	if err := validatePlanSidechatTaskTargets(parentSession, parsed.Launches); err != nil {
		return taskLaunchManifest{}, err
	}
	for _, launch := range parsed.Launches {
		if agentruntime.IsDesignerAgentName(launch.RequestedSubagentType) && launch.OutputMode == taskOutputModeManaged {
			messages, err := s.loadDelegationTranscriptMessages(sessionID)
			if err != nil {
				return taskLaunchManifest{}, err
			}
			if err := bindTaskNativeArtifactSelection(&parsed, parsed.Launches, latestTaskArtifactUseSelection(messages)); err != nil {
				return taskLaunchManifest{}, err
			}
			break
		}
	}
	if parsed.SourceArtifact != nil {
		if s == nil || s.tools == nil || s.tools.ArtifactAuthority() == nil {
			return taskLaunchManifest{}, errors.New("task source_artifact requires the authenticated artifact authority")
		}
		principal := artifactruntime.Principal{SessionID: parentSession.ID, AccountScopeID: parentSession.AccountScopeID, UserID: parentSession.UserID}
		sourceVariant, sourceErr := s.tools.ArtifactAuthority().GetReference(principal, *parsed.SourceArtifact)
		if sourceErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("task source_artifact is unavailable: %w", sourceErr)
		}
		if profileErr := applyTaskSourceAnimationProfile(sourceVariant, parsed.Launches); profileErr != nil {
			return taskLaunchManifest{}, profileErr
		}
	}
	parentMode := sessionruntime.NormalizeMode(sessionMode)
	childMode := effectiveTaskChildMode(sessionMode)
	defaultDisabledTools := taskDisabledToolNames(false)

	launches := make([]taskLaunchManifestRow, 0, len(parsed.Launches))
	resolvedAgentName := ""
	resolvedAgentError := ""
	requestedPrimary := ""
	for i, launch := range parsed.Launches {
		targetWorkspacePath, targetWorkspaceName, targetErr := s.resolveTaskTargetWorkspace(parentSession, identity.Principal{}, launch)
		if targetErr != nil {
			return taskLaunchManifest{}, fmt.Errorf("task launches[%d] workspace target: %w", i, targetErr)
		}
		parsed.Launches[i].TargetWorkspacePath = targetWorkspacePath
		launch.TargetWorkspacePath = targetWorkspacePath
		if parsed.Program != nil && i < len(parsed.Program.Jobs) {
			parsed.Program.Jobs[i].TargetWorkspacePath = targetWorkspacePath
		}
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
		if agentruntime.IsDesignerAgentName(requested) && strings.EqualFold(strings.TrimSpace(launch.OutputMode), taskOutputModeWorkspace) {
			// Designer resolution is fail-closed to managed output. Only the
			// validated workspace output mode may replace that immutable contract.
			workspaceProfile := agentruntime.DesignerWorkspaceAgentProfileForParent(subagentProfile)
			workspaceProfile.Provider, workspaceProfile.Model, workspaceProfile.Thinking = subagentProfile.Provider, subagentProfile.Model, subagentProfile.Thinking
			workspaceProfile.AutoServiceTier = subagentProfile.AutoServiceTier
			subagentProfile = workspaceProfile
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
		if virtualTarget || agentruntime.IsFinderAgentName(resolvedName) || agentruntime.IsDesignerAgentName(resolvedName) || agentruntime.IsImageAgentName(resolvedName) || agentruntime.IsIdeaAgentName(resolvedName) {
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
		launchDisabledTools := append([]string(nil), defaultDisabledTools...)
		if (agentruntime.IsDesignerAgentName(requested) || agentruntime.IsImageAgentName(requested)) && strings.TrimSpace(launch.OutputMode) == taskOutputModeManaged {
			launchDisabledTools = disabledTaskToolNames("write", "edit")
		}
		resolvedTools := buildTaskLaunchResolvedToolSummary(toolContract, profileDisabledTools, launchDisabledTools, executionMode)
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
			OutputMode:            strings.TrimSpace(launch.OutputMode),
			OutputRequirements:    cloneTaskOutputRequirements(launch.OutputRequirements),
			AnimationProfile:      cloneTaskAnimationProfile(launch.AnimationProfile),
			SourceArtifact:        cloneTaskImageSourceArtifact(launch.SourceArtifact),
			ArtifactV3Source:      cloneTaskArtifactV3Source(launch.ArtifactV3Source),
			DependencyEvidence:    strings.TrimSpace(launch.DependencyEvidence),
			SubagentProvider:      strings.TrimSpace(preference.Provider),
			SubagentModel:         strings.TrimSpace(preference.Model),
			SubagentThinking:      strings.TrimSpace(preference.Thinking),
			SubagentServiceTier:   strings.TrimSpace(preference.ServiceTier),
			ChildTitlePreview:     childTitle,
			ChildMode:             childMode,
			DisabledTools:         launchDisabledTools,
			ResolvedTools:         resolvedTools,
			TargetWorkspacePath:   targetWorkspacePath,
			TargetWorkspaceName:   targetWorkspaceName,
			Capabilities: map[string]any{
				"allow_bash":            false,
				"disabled_tools":        launchDisabledTools,
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
			SwarmStrategy:        strings.TrimSpace(launch.SwarmStrategy),
			AssemblyPart:         launch.AssemblyPart,
			IntegrationContract:  strings.TrimSpace(launch.IntegrationContract),
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
		ExecutionFormat:    taskExecutionFormatSubagents,
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
		DisabledTools:      launches[0].DisabledTools,
		ResolvedTools:      launches[0].ResolvedTools,
		SourceArguments:    parsed.SourceArguments,
		Launches:           launches,
		TaskMode:           parsed.Mode,
		Program:            parsed.Program,
	}
	if parsed.Program != nil {
		for _, job := range parsed.Program.Jobs {
			if len(job.DependsOn) == 0 && len(parsed.Program.Stages) > 0 && job.StageID == parsed.Program.Stages[0].ID {
				manifest.ProgramReadyCount++
			}
		}
	}
	if parsed.Swarm != nil {
		manifest.SwarmAgentType = parsed.Swarm.AgentType
		manifest.SwarmStrategy = parsed.Swarm.Strategy
		manifest.AssemblyParts = append([]taskSwarmAssemblyPart(nil), parsed.Swarm.AssemblyParts...)
		manifest.IntegrationContract = parsed.Swarm.IntegrationContract
	}

	parent, ok := s.lookupTaskLaunchParentSession(sessionID, parentMode)
	if ok {
		if parsed.Program != nil {
			hasCoder := false
			coderWorkspacePath := ""
			for _, job := range parsed.Program.Jobs {
				if agentruntime.IsCoderAgentName(job.RequestedSubagentType) {
					hasCoder = true
					coderWorkspacePath = strings.TrimSpace(firstNonEmptyString(job.TargetWorkspacePath, parent.WorktreeRootPath, parent.WorkspacePath))
				}
			}
			if hasCoder {
				if s.worktrees == nil {
					return taskLaunchManifest{}, errors.New("task program Coder jobs require separate worktree isolation")
				}
				if _, baseErr := s.worktrees.ResolveTaskBase(coderWorkspacePath); baseErr != nil {
					return taskLaunchManifest{}, fmt.Errorf("validate task program target Git state for %q: %w", coderWorkspacePath, baseErr)
				}
			}
		}
		manifest.Parent = parent
		manifest.TargetWorkspacePath = strings.TrimSpace(firstNonEmptyString(parent.WorktreeRootPath, parent.WorkspacePath))
		manifest.TargetWorkspaceName = strings.TrimSpace(parent.WorkspaceName)
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
	workspacePath := strings.TrimSpace(firstNonEmptyString(session.WorktreeRootPath, session.WorkspacePath))
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
		WorkspacePath:       strings.TrimSpace(firstNonEmptyString(session.WorktreeRootPath, session.WorkspacePath)),
		WorkspaceName:       strings.TrimSpace(session.WorkspaceName),
		WorktreeEnabled:     session.WorktreeEnabled,
		WorktreeRootPath:    strings.TrimSpace(session.WorktreeRootPath),
		WorktreeBaseBranch:  strings.TrimSpace(session.WorktreeBaseBranch),
		WorktreeBranch:      strings.TrimSpace(session.WorktreeBranch),
	}
}

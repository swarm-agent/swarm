package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

type manageProjectStore interface {
	PutProject(accountScopeID string, proj *pebblestore.ProjectRecord) error
	GetProject(accountScopeID, id string) (*pebblestore.ProjectRecord, bool, error)
	ListProjects(accountScopeID string, limit int) ([]pebblestore.ProjectRecord, error)
	DeleteProject(accountScopeID, id string) error
	UpdateProject(accountScopeID, id string, mutate func(*pebblestore.ProjectRecord) error) (*pebblestore.ProjectRecord, error)
	PutProjectTask(accountScopeID string, task *pebblestore.ProjectTaskRecord) error
	GetProjectTask(accountScopeID, projectID, taskID string) (*pebblestore.ProjectTaskRecord, bool, error)
	ListProjectTasks(accountScopeID, projectID string, limit int) ([]pebblestore.ProjectTaskRecord, error)
	UpdateProjectTask(accountScopeID, projectID, taskID string, mutate func(*pebblestore.ProjectTaskRecord) error) (*pebblestore.ProjectTaskRecord, error)
	DeleteProjectTask(accountScopeID, projectID, taskID string) error
}

// ProjectTaskCreateInput specifies arguments for canonical project task creation.
type ProjectTaskCreateInput struct {
	ID                  string                                   `json:"id,omitempty"`
	SessionID           string                                   `json:"session_id,omitempty"`
	Title               string                                   `json:"title"`
	Description         string                                   `json:"description,omitempty"`
	Prompt              string                                   `json:"prompt,omitempty"`
	Agent               string                                   `json:"agent,omitempty"`
	Operation           string                                   `json:"operation,omitempty"`
	WorkerName          string                                   `json:"worker_name,omitempty"`
	FeatureSize         string                                   `json:"feature_size,omitempty"`
	WorkspacePath       string                                   `json:"workspace_path,omitempty"`
	WorkspaceID         string                                   `json:"workspace_id,omitempty"`
	WorkspaceGeneration int64                                    `json:"workspace_generation,omitempty"`
	WorktreeBranch      string                                   `json:"worktree_branch,omitempty"`
	OutcomeType         string                                   `json:"outcome_type,omitempty"`
	Tier                string                                   `json:"tier,omitempty"`
	AspectRatio         string                                   `json:"aspect_ratio,omitempty"`
	Resolution          string                                   `json:"resolution,omitempty"`
	VariantCount        int                                      `json:"variant_count,omitempty"`
	DurationSeconds     int                                      `json:"duration_seconds,omitempty"`
	Model               string                                   `json:"model,omitempty"`
	Provider            string                                   `json:"provider,omitempty"`
	Thinking            string                                   `json:"thinking,omitempty"`
	ServiceTier         string                                   `json:"service_tier,omitempty"`
	ContextMode         string                                   `json:"context_mode,omitempty"`
	Scenes              []pebblestore.ProjectTaskScene           `json:"scenes,omitempty"`
	Soundtrack          string                                   `json:"soundtrack,omitempty"`
	AutoApprove         bool                                     `json:"auto_approve,omitempty"`
	PipelineStages      []string                                 `json:"pipeline_stages,omitempty"`
	Deliverables        []pebblestore.ProjectTaskDeliverable     `json:"deliverables,omitempty"`
	WhatDidDo           []string                                 `json:"what_did_do,omitempty"`
	WhatNotDone         []string                                 `json:"what_not_done,omitempty"`
	AttachedMedia       []pebblestore.ProjectTaskMediaRef        `json:"attached_media,omitempty"`
	Document            *pebblestore.SessionPlanDocument         `json:"document,omitempty"`
	PlanDocument        *pebblestore.SessionPlanDocument         `json:"plan_document,omitempty"`
	CoderAssignments    []pebblestore.ProjectTaskCoderAssignment `json:"coder_assignments,omitempty"`
	TaskProgram         *pebblestore.TaskProgramDefinition       `json:"task_program,omitempty"`
	TaskProgramID       string                                   `json:"task_program_id,omitempty"`
	PlanSummary         string                                   `json:"plan_summary,omitempty"`
	FullPlanMarkdown    string                                   `json:"full_plan_markdown,omitempty"`
	DiffSummary         string                                   `json:"diff_summary,omitempty"`
	ClientRequestID     string                                   `json:"client_request_id,omitempty"`
	Status              string                                   `json:"status,omitempty"`
}

// ProjectTaskApprovalGuards specifies caller-provided guards for task approval.
type ProjectTaskApprovalGuards struct {
	SessionID          string `json:"session_id,omitempty"`
	PlanID             string `json:"plan_id,omitempty"`
	DefinitionRevision int    `json:"definition_revision,omitempty"`
}

// ProjectTaskLifecycleService defines the canonical operations for project task execution and plan lifecycle.
type ProjectTaskLifecycleService interface {
	CreateProjectTask(ctx context.Context, p identity.Principal, projectID string, input ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error)
	DeployProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string) error
	ApproveProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, guards ...ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error)
	SubmitProjectTaskPlan(ctx context.Context, input sessionruntime.ProjectTaskPlanSubmissionInput) (sessionruntime.ProjectTaskPlanSubmissionResult, error)
}

// ProjectTaskDeployer is a functional deployer callback preserved for backwards compatibility.
type ProjectTaskDeployer func(accountScopeID, projectID, taskID string) error

type legacyDeployerLifecycleAdapter struct {
	deployer ProjectTaskDeployer
	store    manageProjectStore
}

func (a *legacyDeployerLifecycleAdapter) CreateProjectTask(ctx context.Context, p identity.Principal, projectID string, input ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error) {
	if a.store == nil {
		return nil, errors.New("project store is not configured")
	}
	taskID := input.ID
	if taskID == "" {
		taskID = fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	agentName := input.Agent
	outcomeType := input.OutcomeType
	tier := input.Tier
	if input.FeatureSize == "small" && (agentName == "" || agentName == "coder") {
		agentName = "coder"
		outcomeType = "code_pr"
	} else if input.FeatureSize == "big" && (agentName == "" || agentName == "swarm" || agentName == "plan") {
		agentName = "plan"
		outcomeType = "plan_spec"
		if tier == "" {
			tier = "complex"
		}
	} else if agentName == "coder" && outcomeType == "" {
		outcomeType = "code_pr"
	}
	if agentName == "" {
		agentName = "swarm"
	}
	if tier == "" {
		tier = "direct"
	}
	task := &pebblestore.ProjectTaskRecord{
		ID:               taskID,
		ProjectID:        projectID,
		AccountID:        p.AccountScopeID,
		Title:            input.Title,
		Description:      input.Description,
		Agent:            agentName,
		WorkerName:       input.WorkerName,
		OutcomeType:      outcomeType,
		WorkspacePath:    input.WorkspacePath,
		WorktreeBranch:   input.WorktreeBranch,
		Operation:        input.Operation,
		Tier:             tier,
		AspectRatio:      input.AspectRatio,
		Resolution:       input.Resolution,
		VariantCount:     input.VariantCount,
		DurationSeconds:  input.DurationSeconds,
		Model:            input.Model,
		Provider:         input.Provider,
		Thinking:         input.Thinking,
		AutoApprove:      input.AutoApprove,
		PipelineStages:   input.PipelineStages,
		Deliverables:     input.Deliverables,
		WhatDidDo:        input.WhatDidDo,
		WhatNotDone:      input.WhatNotDone,
		AttachedMedia:    input.AttachedMedia,
		PlanDocument:     input.PlanDocument,
		TaskProgram:      input.TaskProgram,
		TaskProgramID:    input.TaskProgramID,
		PlanSummary:      input.PlanSummary,
		FullPlanMarkdown: input.FullPlanMarkdown,
		DiffSummary:      input.DiffSummary,
		Status:           "pending_approval",
		ActionNeeded:     "Review plan and click Approve",
		CreatedAt:        time.Now().UnixMilli(),
		UpdatedAt:        time.Now().UnixMilli(),
	}
	if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
		task.ActionNeeded = "Review task and click Approve to start Coder execution"
	}
	if len(task.Deliverables) == 0 {
		if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
			task.Deliverables = []pebblestore.ProjectTaskDeliverable{
				{ID: "deliv_code", Title: task.Title, Kind: "code_pr", Status: "pending"},
			}
		}
	}
	if task.TaskProgram != nil && task.TaskProgramID == "" && task.TaskProgram.ID != "" {
		task.TaskProgramID = task.TaskProgram.ID
	}
	if input.AutoApprove && task.PlanDocument == nil {
		if a.deployer == nil {
			return nil, errors.New("project task deployer is not configured")
		}
		task.Status = "in_progress"
		task.ActionNeeded = ""
		_ = a.deployer(p.AccountScopeID, projectID, task.ID)
	}
	if err := a.store.PutProjectTask(p.AccountScopeID, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (a *legacyDeployerLifecycleAdapter) DeployProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string) error {
	if a.deployer == nil {
		return errors.New("project task deployer is not configured")
	}
	return a.deployer(p.AccountScopeID, projectID, taskID)
}

func (a *legacyDeployerLifecycleAdapter) ApproveProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, guards ...ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	if a.deployer == nil {
		return nil, errors.New("project task deployer is not configured")
	}
	if a.store == nil {
		return nil, errors.New("project store is not configured")
	}
	existingTask, found, err := a.store.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || existingTask == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}
	if existingTask.Status == "in_progress" || existingTask.Status == "completed" {
		return existingTask, nil
	}
	if existingTask.Status == "rejected" {
		return nil, errors.New("cannot approve rejected task")
	}
	if existingTask.Status != "pending_approval" && existingTask.Status != "queued" {
		return nil, fmt.Errorf("task cannot be approved from status %q", existingTask.Status)
	}
	updated, err := a.store.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.Status = "in_progress"
		t.ActionNeeded = ""
		if len(t.WhatDidDo) == 0 {
			t.WhatDidDo = []string{"Mission approved by user", "Worktree session activated"}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := a.deployer(p.AccountScopeID, projectID, taskID); err != nil {
		_, _ = a.store.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = existingTask.Status
			t.ActionNeeded = existingTask.ActionNeeded
			t.LastError = err.Error()
			return nil
		})
		return nil, fmt.Errorf("deploy task: %w", err)
	}
	fresh, found, _ := a.store.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if found && fresh != nil {
		return fresh, nil
	}
	return updated, nil
}

func (a *legacyDeployerLifecycleAdapter) SubmitProjectTaskPlan(ctx context.Context, input sessionruntime.ProjectTaskPlanSubmissionInput) (sessionruntime.ProjectTaskPlanSubmissionResult, error) {
	return sessionruntime.ProjectTaskPlanSubmissionResult{}, errors.New("plan submission requires canonical plan lifecycle service")
}

func (r *Runtime) SetManageProjectStore(store manageProjectStore) {
	if r == nil {
		return
	}
	r.projects = store
}

func (r *Runtime) SetProjectTaskDeployer(deployer ProjectTaskDeployer) {
	if r == nil {
		return
	}
	r.projectTaskDeployer = deployer
}

func (r *Runtime) SetProjectTaskLifecycleService(service ProjectTaskLifecycleService) {
	if r == nil {
		return
	}
	r.projectTaskLifecycle = service
}

func (r *Runtime) getProjectTaskLifecycleService() ProjectTaskLifecycleService {
	if r == nil {
		return nil
	}
	if r.projectTaskLifecycle != nil {
		return r.projectTaskLifecycle
	}
	if r.projects != nil || r.projectTaskDeployer != nil {
		return &legacyDeployerLifecycleAdapter{deployer: r.projectTaskDeployer, store: r.projects}
	}
	return nil
}

func manageProjectsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_projects",
		Description: "Inspect and manage Projects aggregating workspaces, context (project.md), and ongoing tasks. Supported actions: list, get, create, update, delete, synthesize_context, list_media, get_media, propose_task, approve_task, accept_task, deploy_task, refine_task, create_task, list_tasks, get_task, update_task, archive_task, delete_task, reconcile_tasks. Task edits never transition execution status; delete_task only removes an archived, unlaunched task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action: list|get|create|update|delete|synthesize_context|list_media|get_media|propose_task|approve_task|accept_task|deploy_task|refine_task|create_task|list_tasks|get_task|update_task|archive_task|delete_task|reconcile_tasks",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Project ID for get, update, delete",
				},
				"project_id": map[string]any{
					"type":        "string",
					"description": "Project ID for task and media operations (list_media, get_media, propose_task, approve_task, accept_task, deploy_task, refine_task, list_tasks, update_task)",
				},
				"media_id": map[string]any{
					"type":        "string",
					"description": "Media ID for get_media",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task ID for approve_task, accept_task, deploy_task, refine_task, update_task",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "Optional session ID guard for approve_task or accept_task",
				},
				"plan_id": map[string]any{
					"type":        "string",
					"description": "Optional plan ID guard for approve_task or accept_task",
				},
				"definition_revision": map[string]any{
					"type":        "integer",
					"description": "Optional plan definition revision guard for approve_task or accept_task",
				},
				"theme_id": map[string]any{"type": "string", "description": "Optional project theme ID from the account's existing builtin/custom manage-theme catalog; empty string clears to Swarm default. Inspect manage-theme first; create a custom theme there before assigning it here."},
				"name": map[string]any{
					"type":        "string",
					"description": "Project display name",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional project description or task description",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Task title for propose_task or create_task",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "User request or mission prompt for task planning and routing",
				},
				"agent": map[string]any{
					"type":        "string",
					"description": "Optional agent assignment override (coder, finder, designer, image, video, sound, plan, swarm). Small code tasks route to Coder; exploratory big features route to Plan agent.",
				},
				"intent": map[string]any{
					"type":        "string",
					"description": "Optional explicit task intent: 'code', 'audit', 'image', 'video', 'sound', 'plan'",
				},
				"feature_size": map[string]any{
					"type":        "string",
					"description": "Optional feature size for code tasks: 'small' (direct coder bug fix / single component) or 'big' (complex multi-stage architecture requiring plan agent or direct structured plan)",
				},
				"worker_name": map[string]any{
					"type":        "string",
					"description": "Optional cosmetic worker name override (e.g. '@Auth Coder')",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "Read-only task status filter for list_tasks; status changes require canonical lifecycle actions",
				},
				"expected_revision": map[string]any{"type": "integer", "description": "Required exact task revision for update_task, archive_task and delete_task"},
				"priority":          map[string]any{"type": "string", "description": "Task organization: low|medium|high|urgent"},
				"group":             map[string]any{"type": "string", "description": "Task grouping label (empty clears)"},
				"order":             map[string]any{"type": "integer", "description": "Nonnegative task order within group"},
				"include_archived":  map[string]any{"type": "boolean", "description": "Include archived task records in list_tasks"},
				"query":             map[string]any{"type": "string", "description": "Case-insensitive title/description search"},
				"limit":             map[string]any{"type": "integer", "description": "Page size, maximum 100"},
				"cursor":            map[string]any{"type": "integer", "description": "Zero-based result offset"},
				"auto_approve": map[string]any{
					"type":        "boolean",
					"description": "Set true to automatically deploy the task immediately upon creation for small tasks or Task Programs; structured plans require explicit user review in the task card",
				},
				"plan_document": map[string]any{
					"type":        "object",
					"description": "Optional structured plan document {id, title, info: {goal}, checkpoints: [{id, title, tasks, acceptance_criteria}]} for big-feature tasks. When provided, submits the plan directly to the task card for user review without running a separate Plan agent investigation pass.",
				},
				"document": map[string]any{
					"type":        "object",
					"description": "Alias for plan_document.",
				},
				"coder_assignments": map[string]any{"type": "array", "description": "For one small task with independent parallel Coders: [{title, meta_prompt, deliverable, owned_scope, acceptance_criteria, workspace_path}]. Each assignment may target its own authorized project repository; specify its exact workspace_path (or workspace_id). Omitted assignment source inherits the explicit task source. One approved card starts a Swarm parent and one regular parallel task call; no plan or task program. Owned scopes must not overlap within the same repository.", "items": map[string]any{"type": "object"}},
				"task_program": map[string]any{
					"type":        "object",
					"description": "Optional Task Program for dependent/staged work: {id, stages: [{id, depends_on, dependency_evidence}], jobs: [{id, stage_id, agent_type, title, meta_prompt, deliverable, workspace_path, owned_scope, acceptance_criteria, dependency_evidence}]}. For dependent cross-repository changes set workspace_path on every Coder job to its exact authorized source; integration remains per repository. Independent cross-repository changes use parallel workspace-specific tasks/Coders instead.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model selection override (e.g. image model or video model ID)",
				},
				"aspect_ratio": map[string]any{
					"type":        "string",
					"description": "Optional aspect ratio ('1:1', '16:9', '9:16', '4:3')",
				},
				"resolution": map[string]any{
					"type":        "string",
					"description": "Optional video resolution ('720p', '1080p', '4k')",
				},
				"variant_count": map[string]any{
					"type":        "integer",
					"description": "Optional count of variants to generate for image or direct media tasks",
				},
				"attached_media": map[string]any{
					"type":        "array",
					"description": "Optional array of media items to attach / reference for the task [{id, kind, title, media_url, thumbnail, filename}]",
					"items":       map[string]any{"type": "object"},
				},
				"soundtrack": map[string]any{
					"type":        "string",
					"description": "Optional soundtrack description for video tasks",
				},
				"pipeline_stages": map[string]any{
					"type":        "array",
					"description": "Array of stage name strings for the task",
					"items":       map[string]any{"type": "string"},
				},
				"workspace_path": map[string]any{
					"type": "string", "description": "Exact canonical source repository root in the account workspace catalog (not a linked subdirectory). Omission is allowed only when exactly one authorized project repository exists. Project membership, a coordination workspace, and first-workspace order are not execution grants; ambiguous, stale, or conflicting targets are rejected.",
				},
				"workspace_id":         map[string]any{"type": "string", "description": "Exact account workspace catalog ID for the execution source; if supplied with workspace_path, both must identify the same authorized root"},
				"workspace_generation": map[string]any{"type": "integer", "description": "Optional expected catalog generation for that source; stale bindings are rejected"},
				"client_request_id":    map[string]any{"type": "string", "description": "Stable submission identity reused only for retries of the identical payload and resolved source target; it does not authorize deployment"},
				"workspaces": map[string]any{
					"type":        "array",
					"description": "Project workspace references [{path, role, label}]; membership is not an execution grant or default source target",
					"items":       map[string]any{"type": "object"},
				},
				"project_context": map[string]any{
					"type":        "string",
					"description": "Synthesized project.md markdown text",
				},
				"feedback": map[string]any{
					"type":        "string",
					"description": "User or operator feedback for refine_task",
				},
				"error_summary": map[string]any{
					"type":        "string",
					"description": "Error summary for refine_task",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}

func (r *Runtime) resolveProjectThemeID(accountScopeID string, raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	id, ok := raw.(string)
	if !ok {
		return "", errors.New("theme_id must be a string (empty clears the selection)")
	}
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	if r.uiSettings == nil {
		return "", errors.New("account theme catalog is unavailable")
	}
	settings, err := r.uiSettings.GetForAccount(accountScopeID)
	if err != nil {
		return "", fmt.Errorf("read account theme catalog: %w", err)
	}
	return uisettings.ResolveProjectThemeID(settings, id)
}

func (r *Runtime) executeManageProjects(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.projects == nil {
		return "", errors.New("manage_projects service is not configured")
	}
	p := scope.Principal
	accountScopeID := strings.TrimSpace(p.AccountScopeID)
	userID := strings.TrimSpace(p.UserID)
	if !p.Valid() || accountScopeID == "" || userID == "" || strings.TrimSpace(p.Type) != identity.PrincipalTypeUser {
		return "", errors.New("manage_projects requires an authenticated user identity")
	}

	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "" {
		actionName = "list"
	}

	response := map[string]any{
		"tool":              "manage_projects",
		"action":            actionName,
		"status":            "ok",
		"path_id":           toolPathID("manage_projects"),
		"details_truncated": false,
	}

	switch actionName {
	case "list":
		projects, err := r.projects.ListProjects(accountScopeID, 100)
		if err != nil {
			return "", err
		}
		if projects == nil {
			projects = []pebblestore.ProjectRecord{}
		}
		response["projects"] = projects
		response["count"] = len(projects)

	case "get":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for get")
		}
		proj, found, err := r.projects.GetProject(accountScopeID, id)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", id)
		}
		response["project"] = proj

	case "list_media":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		if projectID == "" {
			return "", errors.New("project_id is required for list_media")
		}
		proj, found, err := r.projects.GetProject(accountScopeID, projectID)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", projectID)
		}
		limit := 50
		if l, ok := args["limit"].(float64); ok && int(l) > 0 {
			limit = int(l)
			if limit > 100 {
				limit = 100
			}
		} else if l, ok := args["limit"].(int); ok && l > 0 {
			limit = l
			if limit > 100 {
				limit = 100
			}
		}
		list := proj.UploadedMedia
		if list == nil {
			list = []pebblestore.ProjectTaskMediaRef{}
		}
		if len(list) > limit {
			list = list[:limit]
		}
		response["project_id"] = projectID
		response["media"] = list
		response["count"] = len(list)

	case "get_media":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		mediaID := strings.TrimSpace(asString(args["media_id"]))
		if mediaID == "" {
			mediaID = strings.TrimSpace(asString(args["id"]))
		}
		if projectID == "" || mediaID == "" {
			return "", errors.New("project_id and media_id are required for get_media")
		}
		proj, found, err := r.projects.GetProject(accountScopeID, projectID)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", projectID)
		}
		var target *pebblestore.ProjectTaskMediaRef
		for _, m := range proj.UploadedMedia {
			if m.ID == mediaID {
				item := m
				target = &item
				break
			}
		}
		if target == nil {
			return "", fmt.Errorf("media %q not found in project %q", mediaID, projectID)
		}
		response["project_id"] = projectID
		response["media"] = target

	case "create":
		name := strings.TrimSpace(asString(args["name"]))
		if name == "" {
			return "", errors.New("project name is required for create")
		}
		desc := strings.TrimSpace(asString(args["description"]))
		pCtx := asString(args["project_context"])
		themeID, err := r.resolveProjectThemeID(accountScopeID, args["theme_id"])
		if err != nil {
			return "", err
		}

		var wsRefs []pebblestore.ProjectWorkspaceRef
		if wsRaw, ok := args["workspaces"].([]any); ok {
			for _, item := range wsRaw {
				if m, ok := item.(map[string]any); ok {
					ref := pebblestore.ProjectWorkspaceRef{
						WorkspaceID: asString(m["workspace_id"]),
						Path:        asString(m["path"]),
						Role:        asString(m["role"]),
						Label:       asString(m["label"]),
					}
					if ref.Path != "" {
						wsRefs = append(wsRefs, ref)
					}
				}
			}
		}

		proj := &pebblestore.ProjectRecord{
			Name:           name,
			ThemeID:        themeID,
			Description:    desc,
			Workspaces:     wsRefs,
			ProjectContext: pCtx,
		}
		if err := r.projects.PutProject(accountScopeID, proj); err != nil {
			return "", err
		}
		response["project"] = proj

	case "update":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for update")
		}
		var themeID string
		if raw, present := args["theme_id"]; present {
			if raw == nil {
				return "", errors.New("theme_id must be a string (empty clears the selection)")
			}
			var err error
			themeID, err = r.resolveProjectThemeID(accountScopeID, raw)
			if err != nil {
				return "", err
			}
		}
		updated, err := r.projects.UpdateProject(accountScopeID, id, func(p *pebblestore.ProjectRecord) error {
			if _, present := args["theme_id"]; present {
				p.ThemeID = themeID
			}
			if v := strings.TrimSpace(asString(args["name"])); v != "" {
				p.Name = v
			}
			if v, ok := args["description"]; ok {
				p.Description = strings.TrimSpace(asString(v))
			}
			if v, ok := args["project_context"]; ok {
				p.ProjectContext = asString(v)
			}
			if wsRaw, ok := args["workspaces"].([]any); ok {
				var wsRefs []pebblestore.ProjectWorkspaceRef
				for _, item := range wsRaw {
					if m, ok := item.(map[string]any); ok {
						ref := pebblestore.ProjectWorkspaceRef{
							WorkspaceID: asString(m["workspace_id"]),
							Path:        asString(m["path"]),
							Role:        asString(m["role"]),
							Label:       asString(m["label"]),
						}
						if ref.Path != "" {
							wsRefs = append(wsRefs, ref)
						}
					}
				}
				p.Workspaces = wsRefs
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if updated == nil {
			return "", fmt.Errorf("project %q not found", id)
		}
		response["project"] = updated

	case "delete":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("project id is required for delete")
		}
		if err := r.projects.DeleteProject(accountScopeID, id); err != nil {
			return "", err
		}
		response["id"] = id
		response["deleted"] = true

	case "synthesize_context":
		var wsPaths []string
		if wsRaw, ok := args["workspaces"].([]any); ok {
			for _, item := range wsRaw {
				if m, ok := item.(map[string]any); ok {
					if p := asString(m["path"]); p != "" {
						wsPaths = append(wsPaths, p)
					}
				} else if s, ok := item.(string); ok && s != "" {
					wsPaths = append(wsPaths, s)
				}
			}
		}

		projectName := strings.TrimSpace(asString(args["name"]))
		if projectName == "" {
			projectName = "Project"
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# %s\n\n", projectName))
		sb.WriteString("## Workspaces & Architecture\n")
		for _, p := range wsPaths {
			base := filepath.Base(p)
			sb.WriteString(fmt.Sprintf("- `%s` (`%s`)\n", base, p))
			for _, docName := range []string{"README.md", "AGENTS.md"} {
				docPath := filepath.Join(p, docName)
				if fi, err := os.Stat(docPath); err == nil && !fi.IsDir() {
					if data, err := os.ReadFile(docPath); err == nil {
						lines := strings.Split(string(data), "\n")
						for i, l := range lines {
							if i > 5 {
								break
							}
							trimmed := strings.TrimSpace(l)
							if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
								sb.WriteString(fmt.Sprintf("  > %s\n", trimmed))
								break
							}
						}
					}
				}
			}
		}
		sb.WriteString("\n## Operating Rules\n- Local-first architecture; durable V3 sessions.\n- Keep changes minimal, tested, and high-craft.\n")

		response["synthesized_context"] = sb.String()

	case "propose_task", "create_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		if projectID == "" {
			return "", errors.New("manage_projects propose_task requires project_id")
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		title := strings.TrimSpace(asString(args["title"]))
		if title == "" {
			title = strings.TrimSpace(asString(args["prompt"]))
		}
		if title == "" {
			return "", errors.New("manage_projects propose_task requires title")
		}

		proj, found, err := r.projects.GetProject(accountScopeID, projectID)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", projectID)
		}

		prompt := strings.TrimSpace(asString(args["prompt"]))
		if prompt == "" {
			prompt = strings.TrimSpace(asString(args["description"]))
		}
		if prompt == "" {
			prompt = title
		}
		wsPath := strings.TrimSpace(asString(args["workspace_path"]))
		agentName := strings.TrimSpace(asString(args["agent"]))
		operation := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
		if agentName == "" && (operation == "create" || operation == "edit" || operation == "extend") {
			agentName = "video"
		}
		featureSize := strings.TrimSpace(asString(args["feature_size"]))
		outcomeType := strings.TrimSpace(asString(args["outcome_type"]))
		if outcomeType == "" && agentName == "video" {
			outcomeType = "video_clip"
		}
		tier := strings.TrimSpace(asString(args["tier"]))
		aspectRatio := strings.TrimSpace(asString(args["aspect_ratio"]))
		resolution := strings.TrimSpace(asString(args["resolution"]))
		variantCount := asInt(args["variant_count"], 0)
		modelName := strings.TrimSpace(asString(args["model"]))
		provider := strings.TrimSpace(asString(args["provider"]))
		thinking := strings.TrimSpace(asString(args["thinking"]))
		serviceTier := strings.TrimSpace(asString(args["service_tier"]))
		contextMode := strings.TrimSpace(asString(args["context_mode"]))
		soundtrack := strings.TrimSpace(asString(args["soundtrack"]))

		var attachedMedia []pebblestore.ProjectTaskMediaRef
		if rawMedia, ok := args["attached_media"].([]any); ok {
			for _, item := range rawMedia {
				if m, ok := item.(map[string]any); ok {
					mediaURL := asString(m["url"])
					if mediaURL == "" {
						mediaURL = asString(m["media_url"])
					}
					ref := pebblestore.ProjectTaskMediaRef{
						ID:        asString(m["id"]),
						Kind:      asString(m["kind"]),
						Title:     asString(m["title"]),
						URL:       mediaURL,
						Filename:  asString(m["filename"]),
						MediaType: asString(m["media_type"]),
						Data:      asString(m["data"]),
					}
					if ref.ID != "" || ref.Title != "" || ref.URL != "" {
						attachedMedia = append(attachedMedia, ref)
					}
				}
			}
		} else if rawMedia, ok := args["attached_media"].([]map[string]any); ok {
			for _, m := range rawMedia {
				mediaURL := asString(m["url"])
				if mediaURL == "" {
					mediaURL = asString(m["media_url"])
				}
				ref := pebblestore.ProjectTaskMediaRef{
					ID:        asString(m["id"]),
					Kind:      asString(m["kind"]),
					Title:     asString(m["title"]),
					URL:       mediaURL,
					Filename:  asString(m["filename"]),
					MediaType: asString(m["media_type"]),
					Data:      asString(m["data"]),
				}
				if ref.ID != "" || ref.Title != "" || ref.URL != "" {
					attachedMedia = append(attachedMedia, ref)
				}
			}
		} else if rawMedia, ok := args["attached_media"].([]pebblestore.ProjectTaskMediaRef); ok {
			attachedMedia = append(attachedMedia, rawMedia...)
		}

		// Direct structured plan document argument support
		var planDoc *pebblestore.SessionPlanDocument
		if rawDoc, ok := args["plan_document"]; ok && rawDoc != nil {
			var pErr error
			planDoc, pErr = parseSessionPlanDocument(rawDoc)
			if pErr != nil {
				return "", fmt.Errorf("invalid plan_document: %w", pErr)
			}
		} else if rawDoc, ok := args["document"]; ok && rawDoc != nil {
			var pErr error
			planDoc, pErr = parseSessionPlanDocument(rawDoc)
			if pErr != nil {
				return "", fmt.Errorf("invalid document: %w", pErr)
			}
		}

		var taskProg *pebblestore.TaskProgramDefinition
		if rawProg, ok := args["task_program"]; ok && rawProg != nil {
			var progErr error
			taskProg, progErr = parseTaskProgram(rawProg)
			if progErr != nil {
				return "", progErr
			}
		}

		var coderAssignments []pebblestore.ProjectTaskCoderAssignment
		if raw, ok := args["coder_assignments"]; ok && raw != nil {
			data, err := json.Marshal(raw)
			if err != nil || json.Unmarshal(data, &coderAssignments) != nil {
				return "", errors.New("invalid coder_assignments")
			}
		}

		workerName := strings.TrimSpace(asString(args["worker_name"]))
		worktreeBranch := strings.TrimSpace(asString(args["worktree_branch"]))
		description := strings.TrimSpace(asString(args["description"]))

		var stages []string
		if rawStages, ok := args["pipeline_stages"].([]any); ok {
			for _, st := range rawStages {
				if s := strings.TrimSpace(asString(st)); s != "" {
					stages = append(stages, s)
				}
			}
		}

		var whatDid []string
		if rawDid, ok := args["what_did_do"].([]any); ok {
			for _, item := range rawDid {
				if s := strings.TrimSpace(asString(item)); s != "" {
					whatDid = append(whatDid, s)
				}
			}
		}
		var whatNot []string
		if rawNot, ok := args["what_not_done"].([]any); ok {
			for _, item := range rawNot {
				if s := strings.TrimSpace(asString(item)); s != "" {
					whatNot = append(whatNot, s)
				}
			}
		}

		var deliverables []pebblestore.ProjectTaskDeliverable
		if rawDelivs, ok := args["deliverables"].([]any); ok {
			for _, item := range rawDelivs {
				if m, ok := item.(map[string]any); ok {
					deliverables = append(deliverables, pebblestore.ProjectTaskDeliverable{
						ID:          asString(m["id"]),
						Title:       asString(m["title"]),
						Kind:        asString(m["kind"]),
						Status:      "pending",
						Description: asString(m["description"]),
					})
				}
			}
		}

		planSummary := strings.TrimSpace(asString(args["plan_summary"]))
		fullPlanMarkdown := strings.TrimSpace(asString(args["full_plan_markdown"]))
		diffSummary := strings.TrimSpace(asString(args["diff_summary"]))
		autoApprove, _ := args["auto_approve"].(bool)

		lifecycle := r.getProjectTaskLifecycleService()
		if lifecycle == nil {
			return "", errors.New("project task lifecycle service is not configured")
		}

		createdTask, err := lifecycle.CreateProjectTask(ctx, p, projectID, ProjectTaskCreateInput{
			ID:                  taskID,
			Title:               title,
			Description:         description,
			Prompt:              prompt,
			Agent:               agentName,
			WorkerName:          workerName,
			FeatureSize:         featureSize,
			WorkspacePath:       wsPath,
			WorkspaceID:         strings.TrimSpace(asString(args["workspace_id"])),
			WorkspaceGeneration: int64(asInt(args["workspace_generation"], 0)),
			ClientRequestID:     strings.TrimSpace(asString(args["client_request_id"])),
			WorktreeBranch:      worktreeBranch,
			OutcomeType:         outcomeType,
			Operation:           operation,
			Tier:                tier,
			AspectRatio:         aspectRatio,
			Resolution:          resolution,
			VariantCount:        variantCount,
			DurationSeconds:     asInt(args["duration_seconds"], 0),
			Model:               modelName,
			Provider:            provider,
			Thinking:            thinking,
			ServiceTier:         serviceTier,
			ContextMode:         contextMode,
			Soundtrack:          soundtrack,
			AutoApprove:         autoApprove,
			PipelineStages:      stages,
			Deliverables:        deliverables,
			WhatDidDo:           whatDid,
			WhatNotDone:         whatNot,
			AttachedMedia:       attachedMedia,
			PlanDocument:        planDoc,
			CoderAssignments:    coderAssignments,
			TaskProgram:         taskProg,
			PlanSummary:         planSummary,
			FullPlanMarkdown:    fullPlanMarkdown,
			DiffSummary:         diffSummary,
		})
		if err != nil {
			return "", fmt.Errorf("create task: %w", err)
		}
		task := *createdTask

		response["task"] = task
		response["task_id"] = task.ID
		response["project_id"] = projectID
		response["task_status"] = task.Status
		if task.PlanDocument != nil {
			response["plan_document"] = task.PlanDocument
		}
		if task.SessionID != "" {
			response["session_id"] = task.SessionID
		}
		if task.WorktreeBranch != "" {
			response["worktree_branch"] = task.WorktreeBranch
		}
		if task.WorkspacePath != "" {
			response["workspace_path"] = task.WorkspacePath
		}
		if task.LastError != "" {
			response["last_error"] = task.LastError
		}
		if task.Revision > 0 {
			response["revision"] = task.Revision
		}
		if task.PlanBinding != nil {
			response["plan_binding"] = task.PlanBinding
		}
		if task.TaskProgram != nil {
			response["task_program"] = task.TaskProgram
			response["task_program_id"] = task.TaskProgramID
		}
		if task.TaskProgramStatus != nil {
			response["task_program_status"] = task.TaskProgramStatus
		}

		proposal := map[string]any{
			"task_id":             task.ID,
			"project_id":          projectID,
			"title":               task.Title,
			"agent":               task.Agent,
			"outcome_type":        task.OutcomeType,
			"workspace_path":      task.WorkspacePath,
			"worktree_branch":     task.WorktreeBranch,
			"pipeline_stages":     task.PipelineStages,
			"workspaces_involved": task.WorkspacesInvolved,
			"plan_summary":        task.PlanSummary,
			"tier":                task.Tier,
			"action_needed":       task.ActionNeeded,
			"status":              task.Status,
		}
		if task.SessionID != "" {
			proposal["session_id"] = task.SessionID
		}
		if task.PlanBinding != nil {
			proposal["plan_binding"] = task.PlanBinding
		}
		if task.TaskProgram != nil {
			proposal["task_program"] = task.TaskProgram
			proposal["task_program_id"] = task.TaskProgramID
		}
		if task.LastError != "" {
			proposal["last_error"] = task.LastError
		}
		response["proposal"] = proposal

	case "list_tasks", "get_task", "update_task", "archive_task", "delete_task", "reconcile_tasks":
		return r.executeManageProjectTasks(scope, actionName, args)

	case "approve_task", "accept_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		if projectID == "" || taskID == "" {
			return "", errors.New("manage_projects approve_task requires project_id and task_id")
		}
		var guards ProjectTaskApprovalGuards
		if sid := strings.TrimSpace(asString(args["session_id"])); sid != "" {
			guards.SessionID = sid
		}
		if pid := strings.TrimSpace(asString(args["plan_id"])); pid != "" {
			guards.PlanID = pid
		}
		if rev := asInt(args["definition_revision"], 0); rev > 0 {
			guards.DefinitionRevision = rev
		}
		lifecycle := r.getProjectTaskLifecycleService()
		if a, ok := lifecycle.(*legacyDeployerLifecycleAdapter); (ok && a.deployer == nil) || lifecycle == nil {
			return "", errors.New("project task lifecycle service is not configured")
		}
		approvedTask, err := lifecycle.ApproveProjectTask(ctx, p, projectID, taskID, guards)
		if err != nil {
			return "", fmt.Errorf("approve task: %w", err)
		}
		response["task"] = approvedTask
		response["task_id"] = approvedTask.ID
		response["status"] = approvedTask.Status
		if approvedTask.SessionID != "" {
			response["session_id"] = approvedTask.SessionID
		}
		if approvedTask.WorktreeBranch != "" {
			response["worktree_branch"] = approvedTask.WorktreeBranch
		}
		if approvedTask.WorkspacePath != "" {
			response["workspace_path"] = approvedTask.WorkspacePath
		}
		if approvedTask.LastError != "" {
			response["last_error"] = approvedTask.LastError
		}
		if approvedTask.Revision > 0 {
			response["revision"] = approvedTask.Revision
		}
		if approvedTask.PlanBinding != nil {
			response["plan_binding"] = approvedTask.PlanBinding
		}
		if approvedTask.PlanDocument != nil {
			response["plan_document"] = approvedTask.PlanDocument
		}
		if approvedTask.TaskProgram != nil {
			response["task_program"] = approvedTask.TaskProgram
			response["task_program_id"] = approvedTask.TaskProgramID
		}
		if approvedTask.TaskProgramStatus != nil {
			response["task_program_status"] = approvedTask.TaskProgramStatus
		}

	case "deploy_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		if projectID == "" || taskID == "" {
			return "", errors.New("manage_projects deploy_task requires project_id and task_id")
		}
		lifecycle := r.getProjectTaskLifecycleService()
		if a, ok := lifecycle.(*legacyDeployerLifecycleAdapter); (ok && a.deployer == nil) || lifecycle == nil {
			return "", errors.New("project task deployer service is not configured")
		}
		existingTask, found, err := r.projects.GetProjectTask(accountScopeID, projectID, taskID)
		if err != nil {
			return "", err
		}
		if !found || existingTask == nil {
			return "", errors.New("task not found")
		}
		if existingTask.Status == "completed" {
			response["task"] = existingTask
			response["status"] = "already_completed"
			raw, _ := json.Marshal(response)
			return string(raw), nil
		}
		if existingTask.Status == "rejected" {
			return "", errors.New("cannot deploy rejected task")
		}
		if err := existingTask.Validate(); err != nil {
			return "", fmt.Errorf("task validation failed: %w", err)
		}
		if err := lifecycle.DeployProjectTask(ctx, p, projectID, taskID); err != nil {
			return "", fmt.Errorf("deploy project task: %w", err)
		}
		freshTask, found, err := r.projects.GetProjectTask(accountScopeID, projectID, taskID)
		if err != nil {
			return "", err
		}
		if !found || freshTask == nil {
			return "", fmt.Errorf("task %q not found after deployment", taskID)
		}
		response["task"] = freshTask
		response["task_id"] = freshTask.ID
		response["task_status"] = freshTask.Status
		if freshTask.SessionID != "" {
			response["session_id"] = freshTask.SessionID
		}
		if freshTask.WorktreeBranch != "" {
			response["worktree_branch"] = freshTask.WorktreeBranch
		}
		if freshTask.WorkspacePath != "" {
			response["workspace_path"] = freshTask.WorkspacePath
		}
		if freshTask.LastError != "" {
			response["last_error"] = freshTask.LastError
		}
		if freshTask.Revision > 0 {
			response["revision"] = freshTask.Revision
		}
		if freshTask.PlanBinding != nil {
			response["plan_binding"] = freshTask.PlanBinding
		}
		if freshTask.PlanDocument != nil {
			response["plan_document"] = freshTask.PlanDocument
		}
		if freshTask.TaskProgram != nil {
			response["task_program"] = freshTask.TaskProgram
			response["task_program_id"] = freshTask.TaskProgramID
		}
		if freshTask.TaskProgramStatus != nil {
			response["task_program_status"] = freshTask.TaskProgramStatus
		}

	case "refine_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		if projectID == "" || taskID == "" {
			return "", errors.New("manage_projects refine_task requires project_id and task_id")
		}
		feedback := strings.TrimSpace(asString(args["feedback"]))
		errorSummary := strings.TrimSpace(asString(args["error_summary"]))
		existing, exists, lookupErr := r.projects.GetProjectTask(accountScopeID, projectID, taskID)
		if lookupErr != nil {
			return "", lookupErr
		}
		if !exists || existing == nil {
			return "", errors.New("task not found")
		}
		if existing.PlanBinding != nil {
			refiner, ok := r.getProjectTaskLifecycleService().(interface {
				RefineBoundProjectTask(context.Context, identity.Principal, string, string, ProjectTaskApprovalGuards, string) (*pebblestore.ProjectTaskRecord, error)
			})
			if !ok {
				return "", errors.New("canonical bound plan refinement unavailable")
			}
			guards := ProjectTaskApprovalGuards{SessionID: strings.TrimSpace(asString(args["session_id"])), PlanID: strings.TrimSpace(asString(args["plan_id"])), DefinitionRevision: asInt(args["definition_revision"], 0)}
			updated, err := refiner.RefineBoundProjectTask(ctx, p, projectID, taskID, guards, feedback)
			if err != nil {
				return "", err
			}
			response["task"], response["status"], response["task_id"] = updated, updated.Status, taskID
			break
		}

		proj, found, _ := r.projects.GetProject(accountScopeID, projectID)
		var projCtx string
		var projWs []pebblestore.ProjectWorkspaceRef
		if found && proj != nil {
			projCtx = proj.ProjectContext
			projWs = proj.Workspaces
		}

		updated, err := r.projects.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Revision <= 0 {
				t.Revision = 1
			}
			t.Revision++
			if feedback != "" {
				t.FeedbackHistory = append(t.FeedbackHistory, feedback)
			}
			if errorSummary != "" {
				t.LastError = errorSummary
			} else if feedback != "" && t.LastError != "" {
				t.LastError = ""
			}

			if v := strings.TrimSpace(asString(args["agent"])); v != "" {
				t.Agent = v
			}
			if v := strings.TrimSpace(asString(args["feature_size"])); v != "" {
				t.FeatureSize = v
				if t.FeatureSize == "big" && t.Agent == "coder" {
					t.Agent = "plan"
					t.Tier = "complex"
					t.OutcomeType = "plan_spec"
				}
			}
			if v := strings.TrimSpace(asString(args["outcome_type"])); v != "" {
				t.OutcomeType = v
			}
			if v := strings.TrimSpace(asString(args["tier"])); v != "" {
				t.Tier = v
			}

			seedPrompt := t.Description
			if seedPrompt == "" {
				seedPrompt = t.Title
			}
			routed, rErr := pebblestore.RouteAndPlanProjectTaskWithOptions(pebblestore.TaskPlanOptions{
				Prompt:             seedPrompt,
				RequestedWorkspace: t.WorkspacePath,
				ProjectContext:     projCtx,
				Workspaces:         projWs,
				Feedback:           feedback,
				LastError:          t.LastError,
				FeatureSize:        t.FeatureSize,
				Agent:              t.Agent,
				OutcomeType:        t.OutcomeType,
				Tier:               t.Tier,
				AspectRatio:        t.AspectRatio,
				VariantCount:       t.VariantCount,
				Soundtrack:         t.Soundtrack,
				AttachedMedia:      t.AttachedMedia,
			})
			if rErr != nil {
				return fmt.Errorf("task configuration invalid: %w", rErr)
			}
			t.PipelineStages = routed.Stages
			t.Deliverables = routed.Deliverables
			t.WorkspacesInvolved = routed.WorkspacesInvolved
			t.ContextPoolSummary = routed.ContextPoolSummary
			t.PlanSummary = routed.PlanSummary
			t.FullPlanMarkdown = routed.FullPlanMarkdown
			t.Status = "pending_approval"
			t.ActionNeeded = fmt.Sprintf("Review revised plan (Rev %d) and click Approve", t.Revision)
			if feedback != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Revised plan (Rev %d) based on: %s", t.Revision, feedback))
			} else if t.LastError != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Re-planned error recovery strategy (Rev %d)", t.Revision))
			}
			if err := t.Validate(); err != nil {
				return fmt.Errorf("refined task validation failed: %w", err)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		freshTask, found, err := r.projects.GetProjectTask(accountScopeID, projectID, taskID)
		if err == nil && found && freshTask != nil {
			updated = freshTask
		}
		response["task"] = updated
		response["status"] = "refined"

	default:
		return "", fmt.Errorf("unknown manage_projects action: %q", actionName)
	}

	raw, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func parseSessionPlanDocument(rawDoc any) (*pebblestore.SessionPlanDocument, error) {
	if rawDoc == nil {
		return nil, nil
	}
	var doc pebblestore.SessionPlanDocument
	switch v := rawDoc.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(v), &doc); err != nil {
			return nil, fmt.Errorf("invalid plan document: %w", err)
		}
	case *pebblestore.SessionPlanDocument:
		if v == nil {
			return nil, nil
		}
		doc = *v
	case pebblestore.SessionPlanDocument:
		doc = v
	default:
		docBytes, err := json.Marshal(rawDoc)
		if err != nil {
			return nil, fmt.Errorf("marshal plan document: %w", err)
		}
		if err := json.Unmarshal(docBytes, &doc); err != nil {
			return nil, fmt.Errorf("invalid plan document: %w", err)
		}
	}
	for i := range doc.Checkpoints {
		if doc.Checkpoints[i].Order <= 0 {
			doc.Checkpoints[i].Order = i + 1
		}
		if len(doc.Checkpoints[i].AcceptanceCriteria) == 0 {
			if len(doc.Checkpoints[i].Tasks) > 0 {
				doc.Checkpoints[i].AcceptanceCriteria = []string{fmt.Sprintf("Completed %s", doc.Checkpoints[i].Title)}
			} else {
				doc.Checkpoints[i].AcceptanceCriteria = []string{"Done"}
			}
		}
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(&doc); err != nil {
		return nil, fmt.Errorf("invalid plan document: %w", err)
	}
	return &doc, nil
}

func parseTaskProgram(rawProg any) (*pebblestore.TaskProgramDefinition, error) {
	if rawProg == nil {
		return nil, nil
	}
	var progBytes []byte
	if s, isStr := rawProg.(string); isStr {
		progBytes = []byte(s)
	} else {
		var err error
		progBytes, err = json.Marshal(rawProg)
		if err != nil {
			return nil, fmt.Errorf("marshal task_program: %w", err)
		}
	}
	var tp pebblestore.TaskProgramDefinition
	if err := json.Unmarshal(progBytes, &tp); err != nil {
		return nil, fmt.Errorf("invalid task_program definition: %w", err)
	}
	if len(tp.Stages) == 0 && len(tp.Jobs) > 0 {
		stageSet := make(map[string]bool)
		for i := range tp.Jobs {
			stID := strings.TrimSpace(tp.Jobs[i].StageID)
			if stID == "" {
				stID = "stage-1"
				tp.Jobs[i].StageID = stID
			}
			if !stageSet[stID] {
				stageSet[stID] = true
				tp.Stages = append(tp.Stages, pebblestore.TaskProgramStageSpec{
					ID:                 stID,
					DependencyEvidence: "Synthesized stage for cohort jobs",
				})
			}
		}
	}
	if len(tp.Stages) == 0 || len(tp.Jobs) == 0 {
		return nil, errors.New("task_program requires at least one stage and job")
	}
	if tp.ID == "" {
		tp.ID = fmt.Sprintf("prog-%d", time.Now().UnixMilli())
	}
	return &tp, nil
}

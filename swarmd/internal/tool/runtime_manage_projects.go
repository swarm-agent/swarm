package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

func manageProjectsDefinition() Definition {
	return Definition{
		Type:        "function",
		Name:        "manage_projects",
		Description: "Inspect and manage Projects aggregating workspaces, context (project.md), and ongoing tasks. Supported actions: list, get, create, update, delete, synthesize_context, propose_task, approve_task, refine_task, create_task, list_tasks, update_task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action: list|get|create|update|delete|synthesize_context|propose_task|approve_task|refine_task|create_task|list_tasks|update_task",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Project ID for get, update, delete",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Project display name",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional project description",
				},
				"workspaces": map[string]any{
					"type":        "array",
					"description": "Array of workspace objects [{path, role, label}]",
					"items":       map[string]any{"type": "object"},
				},
				"project_context": map[string]any{
					"type":        "string",
					"description": "Synthesized project.md markdown text",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}

func (r *Runtime) SetManageProjectStore(store manageProjectStore) {
	if r == nil {
		return
	}
	r.projects = store
}

func (r *Runtime) executeManageProjects(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.projects == nil {
		return "", errors.New("manage_projects service is not configured")
	}
	accountScopeID := strings.TrimSpace(scope.Principal.AccountScopeID)
	if accountScopeID == "" {
		return "", errors.New("manage_projects requires an authenticated account scope")
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

	case "create":
		name := strings.TrimSpace(asString(args["name"]))
		if name == "" {
			return "", errors.New("project name is required for create")
		}
		desc := strings.TrimSpace(asString(args["description"]))
		pCtx := asString(args["project_context"])

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
		updated, err := r.projects.UpdateProject(accountScopeID, id, func(p *pebblestore.ProjectRecord) error {
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
			// Bounded scan for README.md or AGENTS.md
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
		title := strings.TrimSpace(asString(args["title"]))
		if title == "" {
			title = strings.TrimSpace(asString(args["prompt"]))
		}
		if title == "" {
			return "", errors.New("manage_projects propose_task requires title")
		}

		proj, found, _ := r.projects.GetProject(accountScopeID, projectID)
		var projectContext string
		var workspaces []pebblestore.ProjectWorkspaceRef
		if found && proj != nil {
			projectContext = proj.ProjectContext
			workspaces = proj.Workspaces
		}

		prompt := strings.TrimSpace(asString(args["prompt"]))
		if prompt == "" {
			prompt = strings.TrimSpace(asString(args["description"]))
		}
		if prompt == "" {
			prompt = title
		}
		wsPath := strings.TrimSpace(asString(args["workspace_path"]))
		routed := pebblestore.RouteAndPlanProjectTask(prompt, wsPath, projectContext, workspaces, "", "")

		agentName := strings.TrimSpace(asString(args["agent"]))
		if agentName == "" {
			agentName = routed.Agent
		}
		workerName := strings.TrimSpace(asString(args["worker_name"]))
		if workerName == "" {
			workerName = fmt.Sprintf("@%s Worker", strings.Title(agentName))
		}
		status := strings.TrimSpace(asString(args["status"]))
		if status == "" || status == "queued" {
			status = "pending_approval"
		}
		outcomeType := strings.TrimSpace(asString(args["outcome_type"]))
		if outcomeType == "" {
			outcomeType = routed.OutcomeType
		}
		worktreeBranch := strings.TrimSpace(asString(args["worktree_branch"]))
		if worktreeBranch == "" {
			worktreeBranch = routed.Branch
		}
		description := strings.TrimSpace(asString(args["description"]))
		if description == "" {
			description = routed.Mission
		}

		var stages []string
		if rawStages, ok := args["pipeline_stages"].([]any); ok {
			for _, st := range rawStages {
				if s := strings.TrimSpace(asString(st)); s != "" {
					stages = append(stages, s)
				}
			}
		}
		if len(stages) == 0 {
			stages = routed.Stages
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

		actionNeeded := strings.TrimSpace(asString(args["action_needed"]))
		if actionNeeded == "" && status == "pending_approval" {
			actionNeeded = "Review plan and click Approve"
		}

		task := pebblestore.ProjectTaskRecord{
			ProjectID:          projectID,
			Title:              title,
			Description:        description,
			Status:             status,
			Agent:              agentName,
			WorkerName:         workerName,
			OutcomeType:        outcomeType,
			WorkspacePath:      wsPath,
			WorktreeBranch:     worktreeBranch,
			ActionNeeded:       actionNeeded,
			WhatDidDo:          whatDid,
			WhatNotDone:        whatNot,
			DiffSummary:        strings.TrimSpace(asString(args["diff_summary"])),
			PipelineStages:     stages,
			Deliverables:       routed.Deliverables,
			WorkspacesInvolved: routed.WorkspacesInvolved,
			ContextPoolSummary: routed.ContextPoolSummary,
			PlanSummary:        routed.PlanSummary,
			FullPlanMarkdown:   routed.FullPlanMarkdown,
			Tier:               routed.Tier,
			RouterAlert:        routed.RouterAlert,
			Revision:           1,
		}
		if err := r.projects.PutProjectTask(accountScopeID, &task); err != nil {
			return "", err
		}
		if found && proj != nil {
			_, _ = r.projects.UpdateProject(accountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				for _, tid := range p.ActiveTaskIDs {
					if tid == task.ID {
						return nil
					}
				}
				p.ActiveTaskIDs = append(p.ActiveTaskIDs, task.ID)
				return nil
			})
		}
		response["task"] = task
		response["task_id"] = task.ID
		response["proposal"] = map[string]any{
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

	case "list_tasks":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		if projectID == "" {
			return "", errors.New("manage_projects list_tasks requires project_id")
		}
		limit := 100
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		tasks, err := r.projects.ListProjectTasks(accountScopeID, projectID, limit)
		if err != nil {
			return "", err
		}
		response["tasks"] = tasks
		response["count"] = len(tasks)

	case "update_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		if projectID == "" || taskID == "" {
			return "", errors.New("manage_projects update_task requires project_id and task_id")
		}
		updated, err := r.projects.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if v := strings.TrimSpace(asString(args["title"])); v != "" {
				t.Title = v
			}
			if v := strings.TrimSpace(asString(args["description"])); v != "" {
				t.Description = v
			}
			if v := strings.TrimSpace(asString(args["status"])); v != "" {
				t.Status = v
			}
			if v := strings.TrimSpace(asString(args["worker_name"])); v != "" {
				t.WorkerName = v
			}
			if stage, ok := args["current_stage_index"].(float64); ok {
				t.CurrentStageIndex = int(stage)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		response["task"] = updated

	case "approve_task":
		projectID := strings.TrimSpace(asString(args["project_id"]))
		if projectID == "" {
			projectID = strings.TrimSpace(asString(args["id"]))
		}
		taskID := strings.TrimSpace(asString(args["task_id"]))
		if projectID == "" || taskID == "" {
			return "", errors.New("manage_projects approve_task requires project_id and task_id")
		}
		updated, err := r.projects.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.ActionNeeded = ""
			if len(t.WhatDidDo) == 0 {
				t.WhatDidDo = []string{"Mission approved by user", "Worktree session activated"}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		response["task"] = updated

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

			seedPrompt := t.Description
			if seedPrompt == "" {
				seedPrompt = t.Title
			}
			routed := pebblestore.RouteAndPlanProjectTask(seedPrompt, t.WorkspacePath, projCtx, projWs, feedback, t.LastError)
			t.Agent = routed.Agent
			t.OutcomeType = routed.OutcomeType
			t.Description = routed.Mission
			t.PipelineStages = routed.Stages
			t.Deliverables = routed.Deliverables
			t.WorkspacesInvolved = routed.WorkspacesInvolved
			t.ContextPoolSummary = routed.ContextPoolSummary
			t.PlanSummary = routed.PlanSummary
			t.FullPlanMarkdown = routed.FullPlanMarkdown
			t.Tier = routed.Tier
			t.RouterAlert = routed.RouterAlert
			t.Status = "pending_approval"
			t.ActionNeeded = fmt.Sprintf("Review revised plan (Rev %d) and click Approve", t.Revision)
			if feedback != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Revised plan (Rev %d) based on: %s", t.Revision, feedback))
			} else if t.LastError != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Re-planned error recovery strategy (Rev %d)", t.Revision))
			}
			return nil
		})
		if err != nil {
			return "", err
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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const ProjectsPath = "/v3/projects"

// inspectTaskGitState probes a workspace/worktree path for dirty status and unintegrated commits.
func inspectTaskGitState(workspacePath, branch string) (unintegratedCommits int, diffSummary string, isDirty bool) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return 0, "", false
	}
	if fi, err := os.Stat(workspacePath); err != nil || !fi.IsDir() {
		return 0, "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmdDirty := exec.CommandContext(ctx, "git", "-C", workspacePath, "status", "--porcelain")
	if out, err := cmdDirty.Output(); err == nil {
		isDirty = len(bytes.TrimSpace(out)) > 0
	}

	cmdLog := exec.CommandContext(ctx, "git", "-C", workspacePath, "rev-list", "--count", "origin/dev..HEAD")
	if out, err := cmdLog.Output(); err == nil {
		var count int
		if _, err := fmt.Sscanf(string(bytes.TrimSpace(out)), "%d", &count); err == nil {
			unintegratedCommits = count
		}
	} else {
		cmdLogDev := exec.CommandContext(ctx, "git", "-C", workspacePath, "rev-list", "--count", "dev..HEAD")
		if out, err := cmdLogDev.Output(); err == nil {
			var count int
			if _, err := fmt.Sscanf(string(bytes.TrimSpace(out)), "%d", &count); err == nil {
				unintegratedCommits = count
			}
		}
	}

	cmdDiff := exec.CommandContext(ctx, "git", "-C", workspacePath, "diff", "--shortstat", "origin/dev...HEAD")
	if out, err := cmdDiff.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
		diffSummary = string(bytes.TrimSpace(out))
	} else {
		cmdDiffDev := exec.CommandContext(ctx, "git", "-C", workspacePath, "diff", "--shortstat", "dev...HEAD")
		if out, err := cmdDiffDev.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			diffSummary = string(bytes.TrimSpace(out))
		}
	}

	return unintegratedCommits, diffSummary, isDirty
}

// TaskRouteResult represents the synthesized plan, tier, and workspace scope for a task.
type TaskRouteResult = pebblestore.TaskRouteResult

func routeAndPlanProjectTask(prompt string, wsPath string, projectContext string, workspaces []pebblestore.ProjectWorkspaceRef, feedback string, lastError string) TaskRouteResult {
	return pebblestore.RouteAndPlanProjectTask(prompt, wsPath, projectContext, workspaces, feedback, lastError)
}

func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// organizeTaskFromEnglishPrompt delegates to routeAndPlanProjectTask for backward compatibility.
func organizeTaskFromEnglishPrompt(prompt string, wsPath string) (title string, agent string, outcomeType string, branch string, mission string, stages []string, deliverables []pebblestore.ProjectTaskDeliverable) {
	routed := routeAndPlanProjectTask(prompt, wsPath, "", nil, "", "")
	return routed.Title, routed.Agent, routed.OutcomeType, routed.Branch, routed.Mission, routed.Stages, routed.Deliverables
}

// SynthesizeProjectContext reads AGENTS.md and README.md from the provided workspace paths
// and synthesizes an authoritative project.md document.
func SynthesizeProjectContext(projectName string, wsPaths []string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		projectName = "Project"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s Architecture & Project Charter\n\n", projectName))
	sb.WriteString("## Bound Workspaces & Architecture\n")

	for _, p := range wsPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		sb.WriteString(fmt.Sprintf("- **`%s`** (`%s`)\n", base, p))

		// Scan for AGENTS.md and README.md
		var docFound bool
		for _, docName := range []string{"AGENTS.md", "README.md"} {
			docPath := filepath.Join(p, docName)
			if fi, err := os.Stat(docPath); err == nil && !fi.IsDir() {
				if data, err := os.ReadFile(docPath); err == nil {
					lines := strings.Split(string(data), "\n")
					var extracted []string
					for _, l := range lines {
						trimmed := strings.TrimSpace(l)
						if trimmed == "" {
							continue
						}
						// Skip markdown headers
						if strings.HasPrefix(trimmed, "#") {
							continue
						}
						// Collect relevant instruction / architecture lines
						if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || len(trimmed) > 20 {
							extracted = append(extracted, trimmed)
							if len(extracted) >= 4 {
								break
							}
						}
					}
					if len(extracted) > 0 {
						docFound = true
						sb.WriteString(fmt.Sprintf("  - *Source (%s)*:\n", docName))
						for _, ex := range extracted {
							sb.WriteString(fmt.Sprintf("    > %s\n", ex))
						}
						break
					}
				}
			}
		}
		if !docFound {
			sb.WriteString("  - *Role*: General repository module.\n")
		}
	}

	sb.WriteString("\n## System Architecture & Integration Boundary\n")
	sb.WriteString("- Coordinated by Swarm Project Orchestrator (`system-orchestrator`).\n")
	sb.WriteString("- Delegated execution runs on isolated worktrees via `coder`, `designer`, `finder`, and `swarm` waves.\n")
	sb.WriteString("- Durable V3 session contracts and Pebble persistence.\n\n")
	sb.WriteString("## Operational Invariants\n")
	sb.WriteString("- Local-first operation; zero external credential leaks.\n")
	sb.WriteString("- Minimal high-craft diffs with automated parent verification.\n")
	sb.WriteString("- Review-first delivery: finished tasks transition to `needs_review` before completion.\n")

	return sb.String()
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return
	}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return
	}

	db := s.sessions.Store()
	rawPath := strings.TrimPrefix(r.URL.Path, ProjectsPath)
	trimmed := strings.Trim(rawPath, "/")

	// 1. Root /v3/projects
	if trimmed == "" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			q := r.URL.Query()
			limit := 100
			if q.Get("limit") != "" {
				if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
					limit = n
				}
			}
			records, err := db.ListProjects(p.AccountScopeID, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if records == nil {
				records = []pebblestore.ProjectRecord{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"projects": records,
				"count":    len(records),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			var rec pebblestore.ProjectRecord
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			if err := json.Unmarshal(body, &rec); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			if err := db.PutProject(p.AccountScopeID, &rec); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusCreated, map[string]any{
				"project": rec,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	segments := strings.Split(trimmed, "/")

	// 2. Synthesize context: POST /v3/projects/synthesize-context
	if len(segments) == 1 && segments[0] == "synthesize-context" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:read", "sessions:read", "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		var req struct {
			Name       string   `json:"name"`
			Workspaces []string `json:"workspaces"`
		}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		ctxText := SynthesizeProjectContext(req.Name, req.Workspaces)
		writeJSON(w, http.StatusOK, map[string]any{
			"project_context": ctxText,
		})
		return
	}

	projectID := segments[0]

	// 3. Single project resource: /v3/projects/{id}
	if len(segments) == 1 {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			rec, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || rec == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"project": rec,
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			updated, err := db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
					p.Name = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					p.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["project_context"].(string); ok {
					p.ProjectContext = v
				}
				if v, ok := patch["primary_session_id"].(string); ok {
					p.PrimarySessionID = strings.TrimSpace(v)
				}
				if workspacesRaw, ok := patch["workspaces"]; ok {
					rawBytes, err := json.Marshal(workspacesRaw)
					if err == nil {
						var ws []pebblestore.ProjectWorkspaceRef
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							p.Workspaces = ws
						}
					}
				}
				if tasksRaw, ok := patch["active_task_ids"]; ok {
					rawBytes, err := json.Marshal(tasksRaw)
					if err == nil {
						var tasks []string
						if err := json.Unmarshal(rawBytes, &tasks); err == nil {
							p.ActiveTaskIDs = tasks
						}
					}
				}
				if autosRaw, ok := patch["automation_ids"]; ok {
					rawBytes, err := json.Marshal(autosRaw)
					if err == nil {
						var autos []string
						if err := json.Unmarshal(rawBytes, &autos); err == nil {
							p.AutomationIDs = autos
						}
					}
				}
				return nil
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"project": updated,
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			if err := db.DeleteProject(p.AccountScopeID, projectID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "deleted",
				"id":     projectID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 4. Project Tasks collection: /v3/projects/{id}/tasks
	if len(segments) == 2 && segments[1] == "tasks" {
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			tasks, err := db.ListProjectTasks(p.AccountScopeID, projectID, 100)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if tasks == nil {
				tasks = []pebblestore.ProjectTaskRecord{}
			}
			for i := range tasks {
				ws := tasks[i].WorkspacePath
				if ws == "" && tasks[i].SessionID != "" {
					if sess, found, _ := db.GetSession(tasks[i].SessionID); found {
						ws = sess.WorkspacePath
						tasks[i].WorkspacePath = ws
					}
				}
				if ws != "" {
					unintegrated, diff, dirty := inspectTaskGitState(ws, tasks[i].WorktreeBranch)
					if unintegrated > 0 || diff != "" || dirty {
						tasks[i].UnintegratedCommits = unintegrated
						tasks[i].DiffSummary = diff
						tasks[i].IsDirty = dirty
						if tasks[i].ActionNeeded == "" && unintegrated > 0 {
							branchName := tasks[i].WorktreeBranch
							if branchName == "" {
								branchName = "worktree"
							}
							tasks[i].ActionNeeded = fmt.Sprintf("Action Needed: %d unintegrated commit(s) on %s ready to integrate.", unintegrated, branchName)
						}
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"tasks": tasks,
				"count": len(tasks),
			})
			return
		}

		if r.Method == http.MethodPost {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var req struct {
				Title               string                               `json:"title"`
				Description         string                               `json:"description,omitempty"`
				Status              string                               `json:"status,omitempty"`
				Agent               string                               `json:"agent,omitempty"`
				WorkerName          string                               `json:"worker_name,omitempty"`
				OutcomeType         string                               `json:"outcome_type,omitempty"`
				WorkspacePath       string                               `json:"workspace_path,omitempty"`
				WorktreeBranch      string                               `json:"worktree_branch,omitempty"`
				UnintegratedCommits int                                  `json:"unintegrated_commits,omitempty"`
				DiffSummary         string                               `json:"diff_summary,omitempty"`
				IsDirty             bool                                 `json:"is_dirty,omitempty"`
				ActionNeeded        string                               `json:"action_needed,omitempty"`
				WhatDidDo           []string                             `json:"what_did_do,omitempty"`
				WhatNotDone         []string                             `json:"what_not_done,omitempty"`
				PipelineStages      []string                             `json:"pipeline_stages,omitempty"`
				CurrentStageIndex   int                                  `json:"current_stage_index,omitempty"`
				Deliverables        []pebblestore.ProjectTaskDeliverable `json:"deliverables,omitempty"`
				WorkspacesInvolved  []string                             `json:"workspaces_involved,omitempty"`
				PlanSummary         string                               `json:"plan_summary,omitempty"`
				FullPlanMarkdown    string                               `json:"full_plan_markdown,omitempty"`
				Tier                string                               `json:"tier,omitempty"`
				Revision            int                                  `json:"revision,omitempty"`
				LastError           string                               `json:"last_error,omitempty"`
				DeploySession       bool                                 `json:"deploy_session,omitempty"`
				Prompt              string                               `json:"prompt,omitempty"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			proj, found, err := db.GetProject(p.AccountScopeID, projectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || proj == nil {
				writeError(w, http.StatusNotFound, errors.New("project not found"))
				return
			}

			prompt := strings.TrimSpace(req.Prompt)
			if prompt == "" && strings.TrimSpace(req.Title) != "" {
				prompt = strings.TrimSpace(req.Title)
			}

			routed := routeAndPlanProjectTask(prompt, req.WorkspacePath, proj.ProjectContext, proj.Workspaces, "", "")

			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = routed.Title
			}
			agentName := strings.TrimSpace(req.Agent)
			if agentName == "" {
				agentName = routed.Agent
			}
			outcomeType := strings.TrimSpace(req.OutcomeType)
			if outcomeType == "" {
				outcomeType = routed.OutcomeType
			}
			worktreeBranch := strings.TrimSpace(req.WorktreeBranch)
			if worktreeBranch == "" {
				worktreeBranch = routed.Branch
			}
			description := strings.TrimSpace(req.Description)
			if description == "" {
				description = routed.Mission
			}
			stages := req.PipelineStages
			if len(stages) == 0 {
				stages = routed.Stages
			}
			deliverables := req.Deliverables
			if len(deliverables) == 0 {
				deliverables = routed.Deliverables
			}
			workspacesInvolved := req.WorkspacesInvolved
			if len(workspacesInvolved) == 0 {
				workspacesInvolved = routed.WorkspacesInvolved
			}
			planSummary := strings.TrimSpace(req.PlanSummary)
			if planSummary == "" {
				planSummary = routed.PlanSummary
			}
			fullPlanMarkdown := strings.TrimSpace(req.FullPlanMarkdown)
			if fullPlanMarkdown == "" {
				fullPlanMarkdown = routed.FullPlanMarkdown
			}
			tier := strings.TrimSpace(req.Tier)
			if tier == "" {
				tier = routed.Tier
			}
			revision := req.Revision
			if revision <= 0 {
				revision = 1
			}
			workerName := strings.TrimSpace(req.WorkerName)
			if workerName == "" {
				workerName = fmt.Sprintf("@%s Worker", strings.Title(agentName))
			}

			taskStatus := strings.TrimSpace(req.Status)
			if taskStatus == "" {
				taskStatus = "pending_approval"
			}

			task := pebblestore.ProjectTaskRecord{
				ProjectID:           projectID,
				Title:               title,
				Description:         description,
				Status:              taskStatus,
				Agent:               agentName,
				WorkerName:          workerName,
				OutcomeType:         outcomeType,
				WorkspacePath:       strings.TrimSpace(req.WorkspacePath),
				WorktreeBranch:      worktreeBranch,
				UnintegratedCommits: req.UnintegratedCommits,
				DiffSummary:         strings.TrimSpace(req.DiffSummary),
				IsDirty:             req.IsDirty,
				ActionNeeded:        strings.TrimSpace(req.ActionNeeded),
				WhatDidDo:           req.WhatDidDo,
				WhatNotDone:         req.WhatNotDone,
				PipelineStages:      stages,
				CurrentStageIndex:   req.CurrentStageIndex,
				Deliverables:        deliverables,
				WorkspacesInvolved:  workspacesInvolved,
				PlanSummary:         planSummary,
				FullPlanMarkdown:    fullPlanMarkdown,
				Tier:                tier,
				Revision:            revision,
				LastError:           strings.TrimSpace(req.LastError),
			}

			// Deploy session whenever task is created so the session exists and can be viewed immediately in chat
			{
				wsPath := strings.TrimSpace(req.WorkspacePath)
				if wsPath == "" && len(proj.Workspaces) > 0 {
					wsPath = proj.Workspaces[0].Path
				}
				if wsPath == "" {
					wsPath = "."
				}

				createOpts := sessionruntime.CreateSessionOptions{
					AccountScopeID: p.AccountScopeID,
					UserID:         p.UserID,
					Title:          fmt.Sprintf("[%s] %s", proj.Name, task.Title),
					WorkspacePath:  wsPath,
					Mode:           sessionruntime.ModeAuto,
					Preference: &pebblestore.ModelPreference{
						Provider: "google",
						Model:    "gemini-3.8-flash",
						Thinking: "low",
					},
					Metadata: map[string]any{
						"project_id":          projectID,
						"task_id":             task.ID,
						"task_title":          task.Title,
						"agent":               agentName,
						"role":                "project_task",
						"workspaces_involved": task.WorkspacesInvolved,
						"plan_summary":        task.PlanSummary,
						"full_plan_markdown":  task.FullPlanMarkdown,
						"tier":                task.Tier,
						"revision":            task.Revision,
					},
				}

				sessionSnapshot, _, createErr := s.sessions.CreateSessionWithOptions(createOpts)
				if createErr == nil && sessionSnapshot.ID != "" {
					task.SessionID = sessionSnapshot.ID
					if taskStatus == "in_progress" && prompt != "" {
						s.EnqueueSessionRun(p, sessionSnapshot.ID, "run-"+sessionSnapshot.ID, proj.PrimarySessionID)
					}
				}
			}

			if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			// Add task ID to project.ActiveTaskIDs
			_, _ = db.UpdateProject(p.AccountScopeID, projectID, func(p *pebblestore.ProjectRecord) error {
				for _, tid := range p.ActiveTaskIDs {
					if tid == task.ID {
						return nil
					}
				}
				p.ActiveTaskIDs = append(p.ActiveTaskIDs, task.ID)
				return nil
			})

			writeJSON(w, http.StatusCreated, map[string]any{
				"task": task,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 5. Single Project Task resource: /v3/projects/{id}/tasks/{taskId}
	if len(segments) == 3 && segments[1] == "tasks" {
		taskID := segments[2]
		if r.Method == http.MethodGet {
			if !s.requireScopeAny(w, r, "projects:read", "sessions:read") {
				return
			}
			task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if !found || task == nil {
				writeError(w, http.StatusNotFound, errors.New("project task not found"))
				return
			}
			if task.WorkspacePath != "" {
				unintegrated, diff, dirty := inspectTaskGitState(task.WorkspacePath, task.WorktreeBranch)
				if unintegrated > 0 || diff != "" || dirty {
					task.UnintegratedCommits = unintegrated
					task.DiffSummary = diff
					task.IsDirty = dirty
					if task.ActionNeeded == "" && unintegrated > 0 {
						task.ActionNeeded = fmt.Sprintf("Action Needed: %d unintegrated commit(s) on %s ready to integrate.", unintegrated, task.WorktreeBranch)
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"task": task,
			})
			return
		}

		if r.Method == http.MethodPatch {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
				return
			}
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid JSON payload"))
				return
			}

			updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				if v, ok := patch["title"].(string); ok && strings.TrimSpace(v) != "" {
					t.Title = strings.TrimSpace(v)
				}
				if v, ok := patch["description"].(string); ok {
					t.Description = strings.TrimSpace(v)
				}
				if v, ok := patch["status"].(string); ok && strings.TrimSpace(v) != "" {
					t.Status = strings.TrimSpace(v)
				}
				if v, ok := patch["agent"].(string); ok {
					t.Agent = strings.TrimSpace(v)
				}
				if v, ok := patch["worker_name"].(string); ok {
					t.WorkerName = strings.TrimSpace(v)
				}
				if v, ok := patch["current_stage_index"].(float64); ok {
					t.CurrentStageIndex = int(v)
				}
				if stagesRaw, ok := patch["pipeline_stages"]; ok {
					rawBytes, err := json.Marshal(stagesRaw)
					if err == nil {
						var stages []string
						if err := json.Unmarshal(rawBytes, &stages); err == nil {
							t.PipelineStages = stages
						}
					}
				}
				if delivRaw, ok := patch["deliverables"]; ok {
					rawBytes, err := json.Marshal(delivRaw)
					if err == nil {
						var delivs []pebblestore.ProjectTaskDeliverable
						if err := json.Unmarshal(rawBytes, &delivs); err == nil {
							t.Deliverables = delivs
						}
					}
				}
				if v, ok := patch["outcome_type"].(string); ok {
					t.OutcomeType = strings.TrimSpace(v)
				}
				if v, ok := patch["workspace_path"].(string); ok {
					t.WorkspacePath = strings.TrimSpace(v)
				}
				if v, ok := patch["worktree_branch"].(string); ok {
					t.WorktreeBranch = strings.TrimSpace(v)
				}
				if v, ok := patch["unintegrated_commits"].(float64); ok {
					t.UnintegratedCommits = int(v)
				}
				if v, ok := patch["diff_summary"].(string); ok {
					t.DiffSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["is_dirty"].(bool); ok {
					t.IsDirty = v
				}
				if v, ok := patch["action_needed"].(string); ok {
					t.ActionNeeded = strings.TrimSpace(v)
				}
				if arr, ok := patch["what_did_do"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatDidDo = items
				}
				if arr, ok := patch["what_not_done"].([]any); ok {
					var items []string
					for _, item := range arr {
						if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
							items = append(items, strings.TrimSpace(s))
						}
					}
					t.WhatNotDone = items
				}
				if wsRaw, ok := patch["workspaces_involved"]; ok {
					rawBytes, err := json.Marshal(wsRaw)
					if err == nil {
						var ws []string
						if err := json.Unmarshal(rawBytes, &ws); err == nil {
							t.WorkspacesInvolved = ws
						}
					}
				}
				if v, ok := patch["plan_summary"].(string); ok {
					t.PlanSummary = strings.TrimSpace(v)
				}
				if v, ok := patch["full_plan_markdown"].(string); ok {
					t.FullPlanMarkdown = strings.TrimSpace(v)
				}
				if v, ok := patch["tier"].(string); ok {
					t.Tier = strings.TrimSpace(v)
				}
				if v, ok := patch["last_error"].(string); ok {
					t.LastError = strings.TrimSpace(v)
				}
				if v, ok := patch["revision"].(float64); ok {
					t.Revision = int(v)
				}
				return nil
			})
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeError(w, http.StatusNotFound, err)
					return
				}
				writeError(w, http.StatusBadRequest, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"task": updated,
			})
			return
		}

		if r.Method == http.MethodDelete {
			if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
				return
			}
			if err := db.DeleteProjectTask(p.AccountScopeID, projectID, taskID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "deleted",
				"task_id": taskID,
			})
			return
		}

		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	// 6. Integrate task commits: POST /v3/projects/{id}/tasks/{taskId}/integrate
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "integrate" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "completed"
			t.UnintegratedCommits = 0
			t.ActionNeeded = "No Action Required: Integrated into dev"
			t.WhatDidDo = append(t.WhatDidDo, "Integrated commits into dev branch")
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "integrated",
			"task":   updated,
		})
		return
	}

	// 7. Approve task session: POST /v3/projects/{id}/tasks/{taskId}/approve
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "approve" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.ActionNeeded = ""
			if len(t.WhatDidDo) == 0 {
				t.WhatDidDo = []string{"Mission approved by user", "Worktree session activated"}
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if updated != nil {
			if updated.SessionID == "" {
				proj, found, _ := db.GetProject(p.AccountScopeID, projectID)
				if found && proj != nil {
					wsPath := updated.WorkspacePath
					if wsPath == "" && len(proj.Workspaces) > 0 {
						wsPath = proj.Workspaces[0].Path
					}
					if wsPath == "" {
						wsPath = "."
					}
					createOpts := sessionruntime.CreateSessionOptions{
						AccountScopeID: p.AccountScopeID,
						UserID:         p.UserID,
						Title:          fmt.Sprintf("[%s] %s", proj.Name, updated.Title),
						WorkspacePath:  wsPath,
						Mode:           sessionruntime.ModeAuto,
						Preference: &pebblestore.ModelPreference{
							Provider: "google",
							Model:    "gemini-3.8-flash",
							Thinking: "low",
						},
						Metadata: map[string]any{
							"project_id":          projectID,
							"task_id":             updated.ID,
							"task_title":          updated.Title,
							"agent":               updated.Agent,
							"role":                "project_task",
							"workspaces_involved": updated.WorkspacesInvolved,
							"plan_summary":        updated.PlanSummary,
							"full_plan_markdown":  updated.FullPlanMarkdown,
							"tier":                updated.Tier,
							"revision":            updated.Revision,
						},
					}
					sessionSnapshot, _, createErr := s.sessions.CreateSessionWithOptions(createOpts)
					if createErr == nil && sessionSnapshot.ID != "" {
						updated.SessionID = sessionSnapshot.ID
						_ = db.PutProjectTask(p.AccountScopeID, updated)
					}
				}
			}
			if updated.SessionID != "" {
				s.EnqueueSessionRun(p, updated.SessionID, "run-approved-"+updated.SessionID, "")
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "approved",
			"task":   updated,
		})
		return
	}

	// 8. Refine task plan / error re-plan: POST /v3/projects/{id}/tasks/{taskId}/refine
	if len(segments) == 4 && segments[1] == "tasks" && segments[3] == "refine" {
		taskID := segments[2]
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		if !s.requireScopeAny(w, r, "projects:write", "sessions:write") {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
			return
		}
		type refineReq struct {
			Feedback     string `json:"feedback,omitempty"`
			ErrorSummary string `json:"error_summary,omitempty"`
		}
		var rReq refineReq
		if len(body) > 0 {
			_ = json.Unmarshal(body, &rReq)
		}

		proj, found, err := db.GetProject(p.AccountScopeID, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || proj == nil {
			writeError(w, http.StatusNotFound, errors.New("project not found"))
			return
		}

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Revision <= 0 {
				t.Revision = 1
			}
			t.Revision++

			fb := strings.TrimSpace(rReq.Feedback)
			errSum := strings.TrimSpace(rReq.ErrorSummary)
			if fb != "" {
				t.FeedbackHistory = append(t.FeedbackHistory, fb)
			}
			if errSum != "" {
				t.LastError = errSum
			} else if fb != "" && t.LastError != "" {
				t.LastError = ""
			}

			seedPrompt := t.Description
			if seedPrompt == "" {
				seedPrompt = t.Title
			}
			routed := routeAndPlanProjectTask(seedPrompt, t.WorkspacePath, proj.ProjectContext, proj.Workspaces, fb, t.LastError)

			t.Agent = routed.Agent
			t.OutcomeType = routed.OutcomeType
			t.Description = routed.Mission
			t.PipelineStages = routed.Stages
			t.Deliverables = routed.Deliverables
			t.WorkspacesInvolved = routed.WorkspacesInvolved
			t.PlanSummary = routed.PlanSummary
			t.FullPlanMarkdown = routed.FullPlanMarkdown
			t.Tier = routed.Tier
			t.Status = "pending_approval"
			t.ActionNeeded = fmt.Sprintf("Review revised plan (Rev %d) and click Approve", t.Revision)
			if fb != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Revised plan (Rev %d) based on: %s", t.Revision, truncateString(fb, 50)))
			} else if t.LastError != "" {
				t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Re-planned error recovery strategy (Rev %d)", t.Revision))
			}
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "refined",
			"task":   updated,
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}

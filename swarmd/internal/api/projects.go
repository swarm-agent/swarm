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

// organizeTaskFromEnglishPrompt synthesizes title, agent, outcome type, branch, mission, stages, and deliverables from plain English.
func organizeTaskFromEnglishPrompt(prompt string, wsPath string) (title string, agent string, outcomeType string, branch string, mission string, stages []string, deliverables []pebblestore.ProjectTaskDeliverable) {
	prompt = strings.TrimSpace(prompt)
	lower := strings.ToLower(prompt)

	if strings.Contains(lower, "video") || strings.Contains(lower, "render") || strings.Contains(lower, "teaser") || strings.Contains(lower, "animation") || strings.Contains(lower, "clip") || strings.Contains(lower, "trailer") {
		agent = "designer"
		outcomeType = "media_bundle"
		stages = []string{"Script & Storyboard", "Scene Generation", "Soundtrack Ingestion", "Final Review"}
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_1", Title: "Cut 1: Launch Teaser (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "cyber_lattice"},
			{ID: "deliv_2", Title: "Cut 2: Architecture Deep Dive (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "neural_core"},
			{ID: "deliv_3", Title: "Cut 3: Feature Callout (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "orbital_data"},
		}
	} else if strings.Contains(lower, "bug") || strings.Contains(lower, "fix") || strings.Contains(lower, "broken") || strings.Contains(lower, "crash") || strings.Contains(lower, "reproduce") || strings.Contains(lower, "error") {
		agent = "coder"
		outcomeType = "bug_patch"
		stages = []string{"Reproduce with Test", "Author Minimal Fix", "Run Regression Gate", "Review & Integrate"}
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_patch", Title: "Regression Test & Minimal Patch", Kind: "patch", Status: "pending"},
		}
	} else if strings.Contains(lower, "audit") || strings.Contains(lower, "investigate") || strings.Contains(lower, "inspect") || strings.Contains(lower, "benchmark") || strings.Contains(lower, "security") || strings.Contains(lower, "review") {
		agent = "finder"
		outcomeType = "audit_report"
		stages = []string{"Inspect Subsystems", "Analyze System Invariants", "Compile Findings Ledger", "Architectural Report"}
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_report", Title: "Technical Audit Findings Ledger", Kind: "report", Status: "pending"},
		}
	} else {
		agent = "coder"
		outcomeType = "code_pr"
		stages = []string{"Inspect Architecture", "Implement Feature", "Run Critical Testbench", "Review & Land"}
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_pr", Title: "Feature Branch & Critical Tests", Kind: "pr", Status: "pending"},
		}
	}

	clean := prompt
	if len(clean) > 80 {
		clean = clean[:80]
		if idx := strings.LastIndex(clean, " "); idx > 40 {
			clean = clean[:idx]
		}
	}
	clean = strings.TrimSpace(clean)
	if len(clean) > 0 {
		title = strings.ToUpper(clean[:1]) + clean[1:]
	} else {
		title = "Autonomous Task"
	}

	var slugParts []string
	words := strings.Fields(strings.ToLower(prompt))
	for _, w := range words {
		var filtered strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				filtered.WriteRune(r)
			}
		}
		f := filtered.String()
		if len(f) > 2 && f != "the" && f != "and" && f != "for" && f != "with" && f != "make" && f != "please" {
			slugParts = append(slugParts, f)
			if len(slugParts) >= 4 {
				break
			}
		}
	}
	slug := strings.Join(slugParts, "-")
	if slug == "" {
		slug = fmt.Sprintf("task-%d", time.Now().Unix()%10000)
	}
	branch = fmt.Sprintf("agent/%s", slug)
	mission = fmt.Sprintf("Autonomous %s mission: %s. Operates in isolated worktree %s.", agent, prompt, branch)
	return
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

			sTitle, sAgent, sOutcome, sBranch, sMission, sStages, sDelivs := organizeTaskFromEnglishPrompt(prompt, req.WorkspacePath)

			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = sTitle
			}
			agentName := strings.TrimSpace(req.Agent)
			if agentName == "" {
				agentName = sAgent
			}
			outcomeType := strings.TrimSpace(req.OutcomeType)
			if outcomeType == "" {
				outcomeType = sOutcome
			}
			worktreeBranch := strings.TrimSpace(req.WorktreeBranch)
			if worktreeBranch == "" {
				worktreeBranch = sBranch
			}
			description := strings.TrimSpace(req.Description)
			if description == "" {
				description = sMission
			}
			stages := req.PipelineStages
			if len(stages) == 0 {
				stages = sStages
			}
			deliverables := req.Deliverables
			if len(deliverables) == 0 {
				deliverables = sDelivs
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
						"project_id": projectID,
						"task_id":    task.ID,
						"task_title": task.Title,
						"agent":      agentName,
						"role":       "project_task",
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
		if updated != nil && updated.SessionID != "" {
			runPrompt := updated.Description
			if runPrompt == "" {
				runPrompt = updated.Title
			}
			s.EnqueueSessionRun(p, updated.SessionID, "run-approved-"+updated.SessionID, "")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "approved",
			"task":   updated,
		})
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}

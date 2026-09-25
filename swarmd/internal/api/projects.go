package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const ProjectsPath = "/v3/projects"

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
				Title             string                               `json:"title"`
				Description       string                               `json:"description,omitempty"`
				Status            string                               `json:"status,omitempty"`
				Agent             string                               `json:"agent,omitempty"`
				WorkerName        string                               `json:"worker_name,omitempty"`
				PipelineStages    []string                             `json:"pipeline_stages,omitempty"`
				CurrentStageIndex int                                  `json:"current_stage_index,omitempty"`
				Deliverables      []pebblestore.ProjectTaskDeliverable `json:"deliverables,omitempty"`
				DeploySession     bool                                 `json:"deploy_session,omitempty"`
				Prompt            string                               `json:"prompt,omitempty"`
				WorkspacePath     string                               `json:"workspace_path,omitempty"`
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

			agentName := strings.TrimSpace(req.Agent)
			if agentName == "" {
				agentName = "coder"
			}
			taskStatus := strings.TrimSpace(req.Status)
			if taskStatus == "" {
				taskStatus = "queued"
			}

			task := pebblestore.ProjectTaskRecord{
				ProjectID:         projectID,
				Title:             strings.TrimSpace(req.Title),
				Description:       strings.TrimSpace(req.Description),
				Status:            taskStatus,
				Agent:             agentName,
				WorkerName:        strings.TrimSpace(req.WorkerName),
				PipelineStages:    req.PipelineStages,
				CurrentStageIndex: req.CurrentStageIndex,
				Deliverables:      req.Deliverables,
			}

			// Deploy session if requested or prompt provided
			if req.DeploySession || strings.TrimSpace(req.Prompt) != "" {
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
				if createErr != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to deploy task session: %w", createErr))
					return
				}
				task.SessionID = sessionSnapshot.ID
				task.Status = "in_progress"

				if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
					s.EnqueueSessionRun(p, sessionSnapshot.ID, "run-"+sessionSnapshot.ID, proj.PrimarySessionID)
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

	writeError(w, http.StatusNotFound, errors.New("not found"))
}

package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// ProjectWorkspaceRef represents a workspace bound to a project.
type ProjectWorkspaceRef struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Path        string `json:"path"`
	Role        string `json:"role,omitempty"` // "primary_code", "auxiliary", "docs"
	Label       string `json:"label,omitempty"`
}

// ProjectTaskMediaRef represents an attached or tagged media reference for a task.
type ProjectTaskMediaRef struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"` // e.g. "image/png", "video/mp4", "text/plain"
	Kind      string `json:"kind,omitempty"`       // "image" | "video" | "audio" | "doc"
	Filename  string `json:"filename,omitempty"`
	Data      string `json:"data,omitempty"` // Optional inline text content (e.g. for pasted doc)
	SizeBytes int64  `json:"size_bytes,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// ProjectRecord represents a top-level Project aggregating workspaces, context, and tasks.
type ProjectRecord struct {
	ID               string                `json:"id"`
	AccountID        string                `json:"account_id"`
	Name             string                `json:"name"`
	Description      string                `json:"description,omitempty"`
	Workspaces       []ProjectWorkspaceRef `json:"workspaces,omitempty"`
	ProjectContext   string                `json:"project_context,omitempty"` // synthesized project.md
	ActiveTaskIDs    []string              `json:"active_task_ids,omitempty"`
	AutomationIDs    []string              `json:"automation_ids,omitempty"`
	PrimarySessionID string                `json:"primary_session_id,omitempty"`
	UploadedMedia    []ProjectTaskMediaRef `json:"uploaded_media,omitempty"`
	CreatedAt        int64                 `json:"created_at"`
	UpdatedAt        int64                 `json:"updated_at"`
}

// Validate checks that the project has valid required fields.
func (p *ProjectRecord) Validate() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("project name is required")
	}
	return nil
}

// PutProject stores or updates a project record for the account scope.
func (s *SessionStore) PutProject(accountScopeID string, proj *ProjectRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	if proj == nil {
		return errors.New("project definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	proj.AccountID = accountScopeID
	if err := proj.Validate(); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	if proj.ID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		proj.ID = "proj_" + hex.EncodeToString(b)
	}
	if proj.CreatedAt == 0 {
		proj.CreatedAt = now
	}
	proj.UpdatedAt = now

	raw, err := json.Marshal(proj)
	if err != nil {
		return err
	}

	return s.store.db.Set([]byte(KeyProject(accountScopeID, proj.ID)), raw, pebble.Sync)
}

// GetProject returns a project record by ID within an account scope.
func (s *SessionStore) GetProject(accountScopeID, id string) (*ProjectRecord, bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, false, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return nil, false, errors.New("account scope id and project id are required")
	}

	raw, closer, err := s.store.db.Get([]byte(KeyProject(accountScopeID, id)))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer closer.Close()

	var record ProjectRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}

// ListProjects lists projects for an account scope ordered by updated_at descending.
func (s *SessionStore) ListProjects(accountScopeID string, limit int) ([]ProjectRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var projects []ProjectRecord
	err := s.store.IteratePrefix(ProjectPrefix(accountScopeID), 10000, func(_ string, value []byte) error {
		var record ProjectRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return nil
		}
		if record.AccountID == accountScopeID {
			projects = append(projects, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort updated_at descending
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].UpdatedAt > projects[j].UpdatedAt
	})

	if len(projects) > limit {
		projects = projects[:limit]
	}
	return projects, nil
}

// DeleteProject deletes a project record by ID.
func (s *SessionStore) DeleteProject(accountScopeID, id string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return errors.New("account scope id and project id are required")
	}

	return s.store.db.Delete([]byte(KeyProject(accountScopeID, id)), pebble.Sync)
}

// UpdateProject reads, mutates, and writes back a project atomically.
func (s *SessionStore) UpdateProject(accountScopeID, id string, mutate func(*ProjectRecord) error) (*ProjectRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	record, found, err := s.GetProject(accountScopeID, id)
	if err != nil {
		return nil, err
	}
	if !found || record == nil {
		return nil, errors.New("project not found")
	}

	if err := mutate(record); err != nil {
		return nil, err
	}

	if err := s.PutProject(accountScopeID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// ProjectTaskDeliverable represents an artifact, video, code diff, or report produced by a task.
type ProjectTaskDeliverable struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`   // "video" | "code_diff" | "artifact" | "report"
	Status      string `json:"status"` // "ready" | "accepted" | "in_progress"
	Duration    string `json:"duration,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Description string `json:"description,omitempty"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
	CodeDiff    string `json:"code_diff,omitempty"`
	MediaURL    string `json:"media_url,omitempty"`
}

// ProjectTaskScene represents a single scene in a compiled multi-scene video story.
type ProjectTaskScene struct {
	SceneNumber int    `json:"scene_number"`
	Title       string `json:"title"`
	DurationSec int    `json:"duration_sec"`
	Prompt      string `json:"prompt"`
	VisualNotes string `json:"visual_notes,omitempty"`
}

// ProjectTaskRecord represents an autonomous task unit in a project.
type ProjectTaskRecord struct {
	ID                  string                   `json:"id"`
	ProjectID           string                   `json:"project_id"`
	AccountID           string                   `json:"account_id"`
	Title               string                   `json:"title"`
	Description         string                   `json:"description,omitempty"`
	Status              string                   `json:"status"` // "queued" | "in_progress" | "needs_review" | "completed" | "failed" | "pending_approval" | "planning"
	SessionID           string                   `json:"session_id,omitempty"`
	Agent               string                   `json:"agent,omitempty"`
	WorkerName          string                   `json:"worker_name,omitempty"`
	OutcomeType         string                   `json:"outcome_type,omitempty"` // "code_pr" | "media_bundle" | "bug_patch" | "audit_report" | "video_story"
	WorkspacePath       string                   `json:"workspace_path,omitempty"`
	WorktreeBranch      string                   `json:"worktree_branch,omitempty"`
	UnintegratedCommits int                      `json:"unintegrated_commits,omitempty"`
	DiffSummary         string                   `json:"diff_summary,omitempty"`
	IsDirty             bool                     `json:"is_dirty,omitempty"`
	ActionNeeded        string                   `json:"action_needed,omitempty"`
	WhatDidDo           []string                 `json:"what_did_do,omitempty"`
	WhatNotDone         []string                 `json:"what_not_done,omitempty"`
	PipelineStages      []string                 `json:"pipeline_stages,omitempty"`
	CurrentStageIndex   int                      `json:"current_stage_index"`
	Deliverables        []ProjectTaskDeliverable `json:"deliverables,omitempty"`
	WorkspacesInvolved  []string                 `json:"workspaces_involved,omitempty"`
	ContextPoolSummary  string                   `json:"context_pool_summary,omitempty"`
	PlanSummary         string                   `json:"plan_summary,omitempty"`
	FullPlanMarkdown    string                   `json:"full_plan_markdown,omitempty"`
	Tier                string                   `json:"tier,omitempty"` // "direct" | "discovery" | "complex"
	Revision            int                      `json:"revision,omitempty"`
	LastError           string                   `json:"last_error,omitempty"`
	FeedbackHistory     []string                 `json:"feedback_history,omitempty"`
	AspectRatio         string                   `json:"aspect_ratio,omitempty"`
	VariantCount        int                      `json:"variant_count,omitempty"`
	Scenes              []ProjectTaskScene       `json:"scenes,omitempty"`
	Soundtrack          string                   `json:"soundtrack,omitempty"`
	AutoApprove         bool                     `json:"auto_approve,omitempty"`
	RouterAlert         string                   `json:"router_alert,omitempty"`
	AttachedMedia       []ProjectTaskMediaRef    `json:"attached_media,omitempty"`
	CreatedAt           int64                    `json:"created_at"`
	UpdatedAt           int64                    `json:"updated_at"`
}

func (t *ProjectTaskRecord) Validate() error {
	t.ProjectID = strings.TrimSpace(t.ProjectID)
	if t.ProjectID == "" {
		return errors.New("project id is required")
	}
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return errors.New("task title is required")
	}
	if t.Status == "" {
		t.Status = "queued"
	}
	if t.Revision <= 0 {
		t.Revision = 1
	}
	if t.Tier == "" {
		t.Tier = "direct"
	}
	if t.OutcomeType == "" {
		if t.Agent == "designer" || t.Agent == "video" || t.Agent == "image" {
			t.OutcomeType = "media_bundle"
		} else if strings.Contains(strings.ToLower(t.Title), "fix") || strings.Contains(strings.ToLower(t.Title), "bug") {
			t.OutcomeType = "bug_patch"
		} else if strings.Contains(strings.ToLower(t.Title), "audit") || strings.Contains(strings.ToLower(t.Title), "inspect") {
			t.OutcomeType = "audit_report"
		} else {
			t.OutcomeType = "code_pr"
		}
	}
	return nil
}

// PutProjectTask stores or updates a task for a project.
func (s *SessionStore) PutProjectTask(accountScopeID string, task *ProjectTaskRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	if task == nil {
		return errors.New("project task definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	task.AccountID = accountScopeID
	if err := task.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if task.ID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		task.ID = "task_" + hex.EncodeToString(b)
	}
	if task.CreatedAt == 0 {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	key := []byte(KeyProjectTask(accountScopeID, task.ProjectID, task.ID))
	return s.store.db.Set(key, raw, pebble.Sync)
}

// GetProjectTask fetches a project task by ID.
func (s *SessionStore) GetProjectTask(accountScopeID, projectID, taskID string) (*ProjectTaskRecord, bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, false, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	if accountScopeID == "" || projectID == "" || taskID == "" {
		return nil, false, errors.New("account scope id, project id, and task id are required")
	}
	key := []byte(KeyProjectTask(accountScopeID, projectID, taskID))
	val, closer, err := s.store.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer closer.Close()
	var rec ProjectTaskRecord
	if err := json.Unmarshal(val, &rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

// ListProjectTasks lists all tasks for a project.
func (s *SessionStore) ListProjectTasks(accountScopeID, projectID string, limit int) ([]ProjectTaskRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	projectID = strings.TrimSpace(projectID)
	if accountScopeID == "" || projectID == "" {
		return nil, errors.New("account scope id and project id are required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var tasks []ProjectTaskRecord
	err := s.store.IteratePrefix(ProjectTaskPrefix(accountScopeID, projectID), 10000, func(_ string, value []byte) error {
		var rec ProjectTaskRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return nil
		}
		if rec.AccountID == accountScopeID && rec.ProjectID == projectID {
			tasks = append(tasks, rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt < tasks[j].CreatedAt
	})
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks, nil
}

// UpdateProjectTask mutates a task atomically.
func (s *SessionStore) UpdateProjectTask(accountScopeID, projectID, taskID string, mutate func(*ProjectTaskRecord) error) (*ProjectTaskRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	record, found, err := s.GetProjectTask(accountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || record == nil {
		return nil, errors.New("project task not found")
	}
	if err := mutate(record); err != nil {
		return nil, err
	}
	if err := s.PutProjectTask(accountScopeID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// DeleteProjectTask deletes a task record.
func (s *SessionStore) DeleteProjectTask(accountScopeID, projectID, taskID string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	if accountScopeID == "" || projectID == "" || taskID == "" {
		return errors.New("account scope id, project id, and task id are required")
	}
	return s.store.db.Delete([]byte(KeyProjectTask(accountScopeID, projectID, taskID)), pebble.Sync)
}

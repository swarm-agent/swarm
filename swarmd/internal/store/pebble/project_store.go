package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	ID           string           `json:"id"`
	Title        string           `json:"title,omitempty"`
	URL          string           `json:"url,omitempty"`
	MediaType    string           `json:"media_type,omitempty"` // e.g. "image/png", "video/mp4", "text/plain"
	Kind         string           `json:"kind,omitempty"`       // "image" | "video" | "audio" | "doc"
	Filename     string           `json:"filename,omitempty"`
	Data         string           `json:"data,omitempty"` // Optional inline text content (e.g. for pasted doc)
	SizeBytes    int64            `json:"size_bytes,omitempty"`
	DigestSHA256 string           `json:"digest_sha256,omitempty"`
	SourceLink   *VideoSourceLink `json:"source_link,omitempty"`
	CreatedAt    int64            `json:"created_at,omitempty"`
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
	s.store.projectsMu.Lock()
	mut, err := s.putProjectLocked(accountScopeID, proj)
	s.store.projectsMu.Unlock()
	if err != nil {
		return err
	}
	s.store.publishProjectRealtime(mut)
	return nil
}

func (s *SessionStore) putProjectLocked(accountScopeID string, proj *ProjectRecord) (*projectRealtimeMutation, error) {
	if proj == nil {
		return nil, errors.New("project definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	proj.AccountID = accountScopeID
	if err := proj.Validate(); err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	isNew := false
	if proj.ID == "" {
		isNew = true
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
		return nil, err
	}
	action := "project_updated"
	if isNew {
		action = "project_created"
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": proj.ID,
		"account_id": accountScopeID,
		"action":     action,
	})
	mutation := &projectRealtimeMutation{
		accountScopeID: accountScopeID,
		projectID:      proj.ID,
		eventPayload:   payload,
	}
	mutation.putBytes(KeyProject(accountScopeID, proj.ID), raw)
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return nil, err
	}
	return mutation, nil
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
	s.store.projectsMu.Lock()
	mut, err := s.deleteProjectLocked(accountScopeID, id)
	s.store.projectsMu.Unlock()
	if err != nil {
		return err
	}
	s.store.publishProjectRealtime(mut)
	return nil
}

func (s *SessionStore) deleteProjectLocked(accountScopeID, id string) (*projectRealtimeMutation, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return nil, errors.New("account scope id and project id are required")
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": id,
		"account_id": accountScopeID,
		"action":     "project_deleted",
	})
	mutation := &projectRealtimeMutation{
		accountScopeID: accountScopeID,
		projectID:      id,
		eventPayload:   payload,
	}
	mutation.delete(KeyProject(accountScopeID, id))
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return nil, err
	}
	return mutation, nil
}

// UpdateProject reads, mutates, and writes back a project atomically.
func (s *SessionStore) UpdateProject(accountScopeID, id string, mutate func(*ProjectRecord) error) (*ProjectRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	s.store.projectsMu.Lock()
	record, found, err := s.GetProject(accountScopeID, id)
	if err != nil {
		s.store.projectsMu.Unlock()
		return nil, err
	}
	if !found || record == nil {
		s.store.projectsMu.Unlock()
		return nil, errors.New("project not found")
	}
	if err := mutate(record); err != nil {
		s.store.projectsMu.Unlock()
		return nil, err
	}
	mut, err := s.putProjectLocked(accountScopeID, record)
	s.store.projectsMu.Unlock()
	if err != nil {
		return nil, err
	}
	s.store.publishProjectRealtime(mut)
	return record, nil
}

// ProjectTaskDeliverable represents an artifact, video, code diff, or report produced by a task.
type ProjectTaskDeliverable struct {
	ID                  string           `json:"id"`
	Title               string           `json:"title"`
	Kind                string           `json:"kind"`   // "video" | "code_diff" | "artifact" | "report"
	Status              string           `json:"status"` // "ready" | "accepted" | "in_progress"
	Duration            string           `json:"duration,omitempty"`
	Thumbnail           string           `json:"thumbnail,omitempty"`
	Description         string           `json:"description,omitempty"`
	ArtifactRef         string           `json:"artifact_ref,omitempty"`
	CodeDiff            string           `json:"code_diff,omitempty"`
	MediaURL            string           `json:"media_url,omitempty"`
	ParentDeliverableID string           `json:"parent_deliverable_id,omitempty"`
	SourceMediaRef      string           `json:"source_media_ref,omitempty"`
	VideoProvenance     *VideoProvenance `json:"video_provenance,omitempty"`
	Model               string           `json:"model,omitempty"`
	AspectRatio         string           `json:"aspect_ratio,omitempty"`
	Resolution          string           `json:"resolution,omitempty"`
	DurationSeconds     int              `json:"duration_seconds,omitempty"`
}

// ProjectTaskScene represents a single scene in a compiled multi-scene video story.
type ProjectTaskScene struct {
	SceneNumber int    `json:"scene_number"`
	Title       string `json:"title"`
	DurationSec int    `json:"duration_sec"`
	Prompt      string `json:"prompt"`
	VisualNotes string `json:"visual_notes,omitempty"`
}

// ProjectTaskPlanBinding binds a project task to a canonical session plan definition.
type ProjectTaskPlanBinding struct {
	PlanID             string `json:"plan_id,omitempty"`
	DefinitionRevision int    `json:"definition_revision,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	Receipt            string `json:"receipt,omitempty"`
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
	WorkerID            string                   `json:"worker_id,omitempty"`
	WorkerName          string                   `json:"worker_name,omitempty"`
	WorkerRunID         string                   `json:"worker_run_id,omitempty"`
	AutomationID        string                   `json:"automation_id,omitempty"`
	OutcomeType         string                   `json:"outcome_type,omitempty"` // "code_pr" | "media_bundle" | "bug_patch" | "audit_report" | "video_story"
	WorkspacePath       string                   `json:"workspace_path,omitempty"`
	WorktreeBranch      string                   `json:"worktree_branch,omitempty"`
	WorktreeName        string                   `json:"worktree_name,omitempty"`
	BaseBranch          string                   `json:"base_branch,omitempty"`
	BaseCommit          string                   `json:"base_commit,omitempty"`
	GitStatus           string                   `json:"git_status,omitempty"` // "clean" | "dirty" | "diverged" | "unknown" | "stale"
	UnintegratedCommits int                      `json:"unintegrated_commits,omitempty"`
	BehindCommits       int                      `json:"behind_commits,omitempty"`
	IsIntegrated        bool                     `json:"is_integrated,omitempty"`
	DiffSummary         string                   `json:"diff_summary,omitempty"`
	IsDirty             bool                     `json:"is_dirty,omitempty"`
	DirtyCount          int                      `json:"dirty_count,omitempty"`
	SyncWarning         string                   `json:"sync_warning,omitempty"`
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
	Tier                string                   `json:"tier,omitempty"`         // "direct" | "discovery" | "complex"
	FeatureSize         string                   `json:"feature_size,omitempty"` // "small" | "big"
	Revision            int                      `json:"revision,omitempty"`
	LastError           string                   `json:"last_error,omitempty"`
	FeedbackHistory     []string                 `json:"feedback_history,omitempty"`
	AspectRatio         string                   `json:"aspect_ratio,omitempty"`
	Resolution          string                   `json:"resolution,omitempty"`
	VariantCount        int                      `json:"variant_count,omitempty"`
	DurationSeconds     int                      `json:"duration_seconds,omitempty"`
	Model               string                   `json:"model,omitempty"`
	Provider            string                   `json:"provider,omitempty"`
	Thinking            string                   `json:"thinking,omitempty"`
	ServiceTier         string                   `json:"service_tier,omitempty"`
	ContextMode         string                   `json:"context_mode,omitempty"`
	Scenes              []ProjectTaskScene       `json:"scenes,omitempty"`
	Soundtrack          string                   `json:"soundtrack,omitempty"`
	Operation           string                   `json:"operation,omitempty"` // "create" | "edit" | "extend"
	SourceDigestSHA256  string                   `json:"source_digest_sha256,omitempty"`
	VideoProvenance     *VideoProvenance         `json:"video_provenance,omitempty"`
	AutoApprove         bool                     `json:"auto_approve,omitempty"`
	RouterAlert         string                   `json:"router_alert,omitempty"`
	AttachedMedia       []ProjectTaskMediaRef    `json:"attached_media,omitempty"`
	PlanBinding         *ProjectTaskPlanBinding  `json:"plan_binding,omitempty"`
	PlanDocument        *SessionPlanDocument     `json:"plan_document,omitempty"`
	TaskProgram         *TaskProgramDefinition   `json:"task_program,omitempty"`
	TaskProgramID       string                   `json:"task_program_id,omitempty"`
	TaskProgramStatus   *TaskProgramRecord       `json:"task_program_status,omitempty"`
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
	t.WorkerID = strings.TrimSpace(t.WorkerID)
	t.WorkerName = strings.TrimSpace(t.WorkerName)
	t.WorkerRunID = strings.TrimSpace(t.WorkerRunID)
	t.AutomationID = strings.TrimSpace(t.AutomationID)
	if t.Tier == "" {
		t.Tier = "direct"
	}
	agent := strings.ToLower(strings.TrimSpace(t.Agent))
	if agent == "" {
		return errors.New("task agent is required")
	}
	t.Agent = agent

	switch agent {
	case "coder", "finder", "plan", "image", "video", "sound", "audio", "designer", "swarm":
		// valid
	default:
		return fmt.Errorf("unknown task agent: %q", agent)
	}

	featSize := strings.ToLower(strings.TrimSpace(t.FeatureSize))
	if featSize != "" && featSize != "small" && featSize != "big" {
		return fmt.Errorf("unknown feature_size %q (expected 'small' or 'big')", t.FeatureSize)
	}
	if agent == "coder" && featSize == "big" {
		return errors.New("incoherent task contract: coder agent cannot have feature_size 'big' (use plan agent)")
	}
	if agent == "plan" && featSize == "small" {
		return errors.New("incoherent task contract: plan agent cannot have feature_size 'small' (use coder agent)")
	}

	if t.OutcomeType == "" {
		switch agent {
		case "coder":
			t.OutcomeType = "code_pr"
		case "finder":
			t.OutcomeType = "audit_report"
		case "plan":
			t.OutcomeType = "plan_spec"
		case "image":
			t.OutcomeType = "media_bundle"
		case "video":
			if len(t.Scenes) <= 1 {
				t.OutcomeType = "video_clip"
			} else {
				t.OutcomeType = "video_story"
			}
		case "sound", "audio":
			t.OutcomeType = "audio_clip"
		case "designer":
			t.OutcomeType = "artifact"
		case "swarm":
			t.OutcomeType = "general"
		default:
			t.OutcomeType = "general"
		}
	}

	outcome := strings.ToLower(strings.TrimSpace(t.OutcomeType))

	// Incoherent contract checks: reject incompatible combinations with actionable errors
	switch agent {
	case "coder":
		if outcome != "code_pr" && outcome != "bug_patch" && outcome != "code" {
			return fmt.Errorf("incoherent task contract: coder agent cannot have outcome %q", t.OutcomeType)
		}
		if t.Tier == "discovery" {
			return fmt.Errorf("incoherent task contract: coder agent cannot have tier %q", t.Tier)
		}
		for _, d := range t.Deliverables {
			k := strings.ToLower(d.Kind)
			if k == "image" || k == "video" || k == "audio" {
				return errors.New("incoherent task contract: coder agent cannot have media deliverables")
			}
		}
	case "finder":
		if outcome != "audit_report" && outcome != "audit" && outcome != "report" {
			return fmt.Errorf("incoherent task contract: finder agent cannot have outcome %q", t.OutcomeType)
		}
		for _, d := range t.Deliverables {
			k := strings.ToLower(d.Kind)
			if k == "image" || k == "video" || k == "audio" {
				return errors.New("incoherent task contract: finder agent cannot have media deliverables")
			}
		}
	case "plan":
		if outcome != "plan_spec" && outcome != "plan" && outcome != "general" {
			return fmt.Errorf("incoherent task contract: plan agent cannot have outcome %q", t.OutcomeType)
		}
		t.Tier = "complex"
		for _, d := range t.Deliverables {
			k := strings.ToLower(d.Kind)
			if k == "image" || k == "video" || k == "audio" {
				return errors.New("incoherent task contract: plan agent cannot have media deliverables")
			}
		}
	case "designer":
		if outcome != "artifact" && outcome != "ui_design" {
			return fmt.Errorf("incoherent task contract: designer agent cannot have outcome %q", t.OutcomeType)
		}
		for _, d := range t.Deliverables {
			k := strings.ToLower(d.Kind)
			if k == "image" || k == "video" || k == "audio" {
				return errors.New("incoherent task contract: designer agent cannot have media deliverables")
			}
		}
	case "image":
		if outcome != "media_bundle" && outcome != "image" {
			return fmt.Errorf("incoherent task contract: image agent cannot have outcome %q", t.OutcomeType)
		}
	case "video":
		if outcome != "video_clip" && outcome != "video_story" && outcome != "video" {
			return fmt.Errorf("incoherent task contract: video agent cannot have outcome %q", t.OutcomeType)
		}
	case "sound", "audio":
		if outcome != "audio_clip" && outcome != "sound" && outcome != "audio" {
			return fmt.Errorf("incoherent task contract: sound agent cannot have outcome %q", t.OutcomeType)
		}
	}

	if t.TaskProgram != nil {
		if err := ValidateTaskProgramDefinition(t.TaskProgram); err != nil {
			return fmt.Errorf("invalid task_program: %w", err)
		}
	}
	if t.Operation != "" {
		t.Operation = strings.ToLower(strings.TrimSpace(t.Operation))
		if t.Operation != VideoOperationCreate && t.Operation != VideoOperationEdit && t.Operation != VideoOperationExtend {
			return fmt.Errorf("task operation %q is invalid; must be create, edit, or extend", t.Operation)
		}
	}
	if t.SourceDigestSHA256 != "" {
		t.SourceDigestSHA256 = strings.ToLower(strings.TrimSpace(t.SourceDigestSHA256))
		if len(t.SourceDigestSHA256) != 64 {
			return errors.New("task source digest must be a 64-character sha256 hex string")
		}
		if _, err := hex.DecodeString(t.SourceDigestSHA256); err != nil {
			return errors.New("task source digest is not valid hex")
		}
	}
	if t.VideoProvenance != nil {
		if err := t.VideoProvenance.Validate(); err != nil {
			return fmt.Errorf("task video provenance invalid: %w", err)
		}
	}
	for i, d := range t.Deliverables {
		if d.VideoProvenance != nil {
			if err := d.VideoProvenance.Validate(); err != nil {
				return fmt.Errorf("task deliverable %d video provenance invalid: %w", i, err)
			}
		}
		if len(d.Model) > 128 {
			return fmt.Errorf("task deliverable %d model exceeds 128 characters", i)
		}
		if len(d.AspectRatio) > 32 {
			return fmt.Errorf("task deliverable %d aspect_ratio exceeds 32 characters", i)
		}
		if len(d.Resolution) > 32 {
			return fmt.Errorf("task deliverable %d resolution exceeds 32 characters", i)
		}
		if d.DurationSeconds < 0 || d.DurationSeconds > 3600 {
			return fmt.Errorf("task deliverable %d duration_seconds is invalid", i)
		}
	}
	for i, m := range t.AttachedMedia {
		if m.DigestSHA256 != "" {
			if len(m.DigestSHA256) != 64 {
				return fmt.Errorf("task attached media %d digest must be a 64-character sha256 hex string", i)
			}
			if _, err := hex.DecodeString(m.DigestSHA256); err != nil {
				return fmt.Errorf("task attached media %d digest is not valid hex", i)
			}
		}
		if m.SourceLink != nil {
			if err := m.SourceLink.Validate(); err != nil {
				return fmt.Errorf("task attached media %d source link invalid: %w", i, err)
			}
		}
	}
	return nil
}

// ClientSafeCopy returns a copy of VideoProvenance with internal provider handles
// and credential identifiers redacted for client-facing serialization.
func (p *VideoProvenance) ClientSafeCopy() *VideoProvenance {
	if p == nil {
		return nil
	}
	cp := p.Clone()
	cp.HasInteraction = strings.TrimSpace(p.InteractionID) != ""
	cp.HasProviderResource = strings.TrimSpace(p.ProviderResource) != ""
	cp.CredentialID = ""
	cp.CredentialVersion = ""
	cp.InteractionID = ""
	cp.ProviderResource = ""
	return cp
}

// PutProjectTask stores or updates a task for a project.
func (s *SessionStore) PutProjectTask(accountScopeID string, task *ProjectTaskRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	s.store.projectsMu.Lock()
	mut, err := s.putProjectTaskLocked(accountScopeID, task)
	s.store.projectsMu.Unlock()
	if err != nil {
		return err
	}
	s.store.publishProjectRealtime(mut)
	return nil
}

func (s *SessionStore) putProjectTaskLocked(accountScopeID string, task *ProjectTaskRecord) (*projectRealtimeMutation, error) {
	if task == nil {
		return nil, errors.New("project task definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	task.AccountID = accountScopeID
	if err := task.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	isNew := false
	if task.ID == "" {
		isNew = true
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		task.ID = "task_" + hex.EncodeToString(b)
	}
	if task.CreatedAt == 0 {
		task.CreatedAt = now
	}
	task.UpdatedAt = now

	toStore := *task
	toStore.PlanDocument = nil
	raw, err := json.Marshal(&toStore)
	if err != nil {
		return nil, err
	}
	key := KeyProjectTask(accountScopeID, task.ProjectID, task.ID)
	action := "task_updated"
	if isNew {
		action = "task_created"
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": task.ProjectID,
		"account_id": accountScopeID,
		"task_id":    task.ID,
		"action":     action,
	})
	mutation := &projectRealtimeMutation{
		accountScopeID: accountScopeID,
		projectID:      task.ProjectID,
		eventPayload:   payload,
	}
	mutation.putBytes(key, raw)
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return nil, err
	}
	return mutation, nil
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
	s.hydrateProjectTaskPlanDocument(&rec)
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
			s.hydrateProjectTaskPlanDocument(&rec)
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

// SetProjectTaskUpdateHookForTest installs a test failure seam before UpdateProjectTask mutates the record.
func (s *SessionStore) SetProjectTaskUpdateHookForTest(hook func(taskID string) error) func() {
	if s == nil || s.store == nil {
		return func() {}
	}
	s.store.projectsMu.Lock()
	previous := s.store.beforeProjectTaskUpdateHook
	s.store.beforeProjectTaskUpdateHook = hook
	s.store.projectsMu.Unlock()
	return func() {
		s.store.projectsMu.Lock()
		s.store.beforeProjectTaskUpdateHook = previous
		s.store.projectsMu.Unlock()
	}
}

// UpdateProjectTask mutates a task atomically.
func (s *SessionStore) UpdateProjectTask(accountScopeID, projectID, taskID string, mutate func(*ProjectTaskRecord) error) (*ProjectTaskRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	s.store.projectsMu.Lock()
	if hook := s.store.beforeProjectTaskUpdateHook; hook != nil {
		if err := hook(taskID); err != nil {
			s.store.projectsMu.Unlock()
			return nil, err
		}
	}
	record, found, err := s.GetProjectTask(accountScopeID, projectID, taskID)
	if err != nil {
		s.store.projectsMu.Unlock()
		return nil, err
	}
	if !found || record == nil {
		s.store.projectsMu.Unlock()
		return nil, errors.New("project task not found")
	}
	if err := mutate(record); err != nil {
		s.store.projectsMu.Unlock()
		return nil, err
	}
	mut, err := s.putProjectTaskLocked(accountScopeID, record)
	s.store.projectsMu.Unlock()
	if err != nil {
		return nil, err
	}
	s.store.publishProjectRealtime(mut)
	return record, nil
}

// DeleteProjectTask deletes a task record.
func (s *SessionStore) DeleteProjectTask(accountScopeID, projectID, taskID string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	s.store.projectsMu.Lock()
	mut, err := s.deleteProjectTaskLocked(accountScopeID, projectID, taskID)
	s.store.projectsMu.Unlock()
	if err != nil {
		return err
	}
	s.store.publishProjectRealtime(mut)
	return nil
}

func (s *SessionStore) hydrateProjectTaskPlanDocument(task *ProjectTaskRecord) {
	if task == nil {
		return
	}
	if task.PlanDocument == nil && task.PlanBinding != nil && task.PlanBinding.PlanID != "" {
		sessID := task.SessionID
		if sessID == "" {
			sessID = task.PlanBinding.SessionID
		}
		if sessID != "" {
			if plan, found, err := s.GetPlan(sessID, task.PlanBinding.PlanID); err == nil && found && plan.Document != nil {
				task.PlanDocument = plan.Document
			}
		}
	}
}

func (s *SessionStore) deleteProjectTaskLocked(accountScopeID, projectID, taskID string) (*projectRealtimeMutation, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	if accountScopeID == "" || projectID == "" || taskID == "" {
		return nil, errors.New("account scope id, project id, and task id are required")
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": projectID,
		"account_id": accountScopeID,
		"task_id":    taskID,
		"action":     "task_deleted",
	})
	mutation := &projectRealtimeMutation{
		accountScopeID: accountScopeID,
		projectID:      projectID,
		eventPayload:   payload,
	}
	key := KeyProjectTask(accountScopeID, projectID, taskID)
	mutation.delete(key)
	if err := s.store.commitProjectRealtime(mutation); err != nil {
		return nil, err
	}
	return mutation, nil
}

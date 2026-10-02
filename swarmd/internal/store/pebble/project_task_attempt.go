package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProjectTaskAttempt records a coordinator reservation, not a copy of its transcript.
// Zero legacy timestamps mean unknown; migration never dates old feedback.
type ProjectTaskAttempt struct {
	ID              string                     `json:"id"`
	ClientRequestID string                     `json:"client_request_id,omitempty"`
	PayloadHash     string                     `json:"payload_hash,omitempty"`
	RequestRevision int                        `json:"request_revision,omitempty"`
	UserID          string                     `json:"user_id,omitempty"`
	Request         string                     `json:"request,omitempty"`
	SessionID       string                     `json:"session_id"`
	RunID           string                     `json:"run_id,omitempty"`
	Role            string                     `json:"role"`
	CreatedAt       int64                      `json:"created_at,omitempty"`
	Status          string                     `json:"status"`
	LaunchState     string                     `json:"launch_state,omitempty"`
	LastError       string                     `json:"last_error,omitempty"`
	Summary         string                     `json:"summary,omitempty"`
	SummaryRunID    string                     `json:"summary_run_id,omitempty"`
	Recovery        *ProjectTaskRecoverySource `json:"recovery,omitempty"`
	PlanBinding     *ProjectTaskPlanBinding    `json:"plan_binding,omitempty"`
	TaskProgramID   string                     `json:"task_program_id,omitempty"`
	Deliverables    []ProjectTaskDeliverable   `json:"deliverables,omitempty"`
	Integration     *ProjectTaskIntegration    `json:"integration,omitempty"`
	WorkspacePath   string                     `json:"workspace_path,omitempty"`
	WorktreeBranch  string                     `json:"worktree_branch,omitempty"`
	BaseBranch      string                     `json:"base_branch,omitempty"`
	BaseCommit      string                     `json:"base_commit,omitempty"`
	AllocationHead  string                     `json:"allocation_head,omitempty"`
}

// ProjectTaskRecoverySource is backend-inspected Git evidence, never UI grants.
type ProjectTaskRecoverySource struct {
	SessionID     string `json:"session_id"`
	WorkspacePath string `json:"workspace_path"`
	Branch        string `json:"branch"`
	HeadCommit    string `json:"head_commit"`
	BaseCommit    string `json:"base_commit"`
	TargetBranch  string `json:"target_branch"`
	TargetHead    string `json:"target_head"`
	PreparedHead  string `json:"prepared_head,omitempty"`
	PreparedRef   string `json:"prepared_ref,omitempty"`
}

// EnsureTaskAttempts is an idempotent, evidence-only upgrade of single-session tasks.
func (t *ProjectTaskRecord) EnsureTaskAttempts() {
	if len(t.Attempts) != 0 || t.SessionID == "" {
		return
	}
	a := ProjectTaskAttempt{ID: "initial", SessionID: t.SessionID, Role: t.Agent, CreatedAt: t.CreatedAt, Status: t.Status}
	t.Attempts = []ProjectTaskAttempt{a}
	t.ActiveAttemptID = a.ID
	t.CaptureActiveAttempt()
}

func (t *ProjectTaskRecord) CaptureActiveAttempt() {
	for i := range t.Attempts {
		a := &t.Attempts[i]
		if a.ID == "initial" && t.ActiveAttemptID == "initial" {
			a.SessionID = t.SessionID
		}
		if a.ID != t.ActiveAttemptID || a.SessionID != t.SessionID {
			continue
		}
		a.Status, a.LastError = t.Status, t.LastError
		a.PlanBinding, a.TaskProgramID = t.PlanBinding, t.TaskProgramID
		a.Deliverables = append([]ProjectTaskDeliverable(nil), t.Deliverables...)
		a.Integration = t.Integration
		a.WorkspacePath, a.WorktreeBranch, a.BaseBranch, a.BaseCommit = t.WorkspacePath, t.WorktreeBranch, t.BaseBranch, t.BaseCommit
	}
}

func (t *ProjectTaskRecord) ActiveAttempt() *ProjectTaskAttempt {
	for i := range t.Attempts {
		if t.Attempts[i].ID == t.ActiveAttemptID {
			return &t.Attempts[i]
		}
	}
	return nil
}

func (t *ProjectTaskRecord) ExecutionRunID() string {
	if a := t.ActiveAttempt(); a != nil && a.RunID != "" {
		return a.RunID
	}
	return fmt.Sprintf("desktop-v3-run:task-%s", t.ID)
}

// ReserveTaskFollowup runs inside UpdateProjectTask's account/project lock. Identity
// is durable before any worktree, V3 mutation, or enqueue side effect.
func (s *SessionStore) ReserveTaskFollowup(account, project, taskID, user, key, request string, revision int, now int64) (*ProjectTaskRecord, error) {
	return s.ReserveTaskFollowupWithRecovery(account, project, taskID, user, key, request, revision, now, nil)
}

func (s *SessionStore) ReserveTaskFollowupWithRecovery(account, project, taskID, user, key, request string, revision int, now int64, recovery *ProjectTaskRecoverySource) (*ProjectTaskRecord, error) {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(key) == "" || len(key) > 128 {
		return nil, errors.New("user and bounded client_request_id required")
	}
	if strings.TrimSpace(request) == "" || len(request) > 32000 {
		return nil, errors.New("feedback must contain 1-32000 bytes; no truncation is performed")
	}
	payload, _ := json.Marshal([]any{user, request, revision, recovery != nil})
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	identity := sha256.Sum256([]byte(account + "\x00" + project + "\x00" + taskID + "\x00" + key))
	id := hex.EncodeToString(identity[:16])
	return s.UpdateProjectTask(account, project, taskID, func(t *ProjectTaskRecord) error {
		if t.AccountID != account || t.ProjectID != project {
			return errors.New("task identity mismatch")
		}
		t.EnsureTaskAttempts()
		for _, a := range t.Attempts {
			if a.ClientRequestID != key {
				continue
			}
			if a.PayloadHash != hash {
				return errors.New("client_request_id reused with changed payload")
			}
			if a.ID != t.ActiveAttemptID {
				return errors.New("follow-up is historical; retrieve history instead of relaunching")
			}
			return nil
		}
		if t.Status == "in_progress" && t.SessionID != "" {
			state, found, err := s.GetV3SessionRunState(t.SessionID)
			if err != nil {
				return err
			}
			if found && state.AccountScopeID == account && !state.Active {
				switch state.Status {
				case V3RunIntentCompleted:
					t.Status = "needs_review"
				case V3RunIntentFailed, V3RunIntentCancelled, V3RunIntentExpired, V3RunIntentInterrupted, V3RunIntentDispatchBlocked:
					t.Status = "failed"
				}
			}
		}
		if t.Archived || (t.Status != "needs_review" && t.Status != "completed" && t.Status != "failed") {
			return errors.New("task is running or requires structured review; follow-up rejected without mutation")
		}
		if revision <= 0 || revision != t.Revision {
			return errors.New("stale or missing task revision")
		}
		if t.SessionID != "" {
			active, found, err := s.GetV3SessionActiveRunIntent(t.SessionID)
			if err != nil {
				return err
			}
			if found && (active.Status == V3RunIntentPendingExecutor || active.Status == V3RunIntentRunning) {
				return errors.New("task session still has an active run")
			}
		}
		if t.SessionID != "" {
			activePlan, found, err := s.GetActivePlan(t.SessionID)
			if err != nil {
				return err
			}
			if found && activePlan.PlanID != "" {
				plan, exists, err := s.GetPlan(t.SessionID, activePlan.PlanID)
				if err != nil {
					return err
				}
				if !exists || plan.AccountScopeID != account || plan.ApprovalState != "approved" {
					return errors.New("current session plan requires review")
				}
			}
		}
		if t.TaskProgramID != "" && t.SessionID != "" {
			program, found, err := s.GetTaskProgram(t.SessionID, t.TaskProgramID)
			if err != nil {
				return err
			}
			if !found || program.State == TaskProgramStateRunning || program.State == TaskProgramStateDeclared {
				return errors.New("task program has unresolved active work")
			}
		}
		if t.PlanBinding != nil {
			plan, found, err := s.GetPlan(t.PlanBinding.SessionID, t.PlanBinding.PlanID)
			if err != nil {
				return err
			}
			if !found || plan.AccountScopeID != account || plan.ApprovalState != "approved" {
				return errors.New("prior plan requires structured review")
			}
		}
		if recovery != nil && (recovery.SessionID != t.SessionID || recovery.WorkspacePath != t.WorkspacePath || recovery.Branch != t.WorktreeBranch || recovery.BaseCommit != t.BaseCommit || recovery.TargetBranch != t.BaseBranch || recovery.HeadCommit == "" || recovery.HeadCommit == recovery.BaseCommit || recovery.TargetHead == "") {
			return errors.New("repair evidence does not match originating task attempt")
		}
		t.CaptureActiveAttempt()
		a := ProjectTaskAttempt{ID: id, ClientRequestID: key, PayloadHash: hash, RequestRevision: revision, UserID: user, Request: request, SessionID: "task-followup-" + id, RunID: "desktop-v3-run:task-followup-" + id, Role: "swarm", CreatedAt: now, Status: "in_progress", LaunchState: "reserved", Recovery: recovery}
		t.Attempts = append(t.Attempts, a)
		t.ActiveAttemptID, t.SessionID, t.Status, t.Agent = id, a.SessionID, "in_progress", "swarm"
		t.WorkspacePath = t.SourceWorkspace.Path
		t.WorktreeBranch, t.WorktreeName, t.BaseBranch, t.BaseCommit = "agent/followup-"+id, "followup-"+id, "", ""
		t.PlanBinding, t.PlanDocument, t.TaskProgram, t.TaskProgramStatus = nil, nil, nil, nil
		t.TaskProgramID, t.FullPlanMarkdown, t.PlanSummary, t.FeatureSize = "", "", "", "small"
		t.OutcomeType, t.Tier = "general", "direct"
		t.WorkerID, t.WorkerName, t.WorkerRunID, t.AutomationID = "", "", "", ""
		t.ProgramSources = nil
		t.CoderAssignments = nil
		t.Model, t.Provider, t.Thinking, t.ServiceTier, t.ContextMode = "", "", "", "", ""
		t.IsIntegrated, t.IsDirty = false, false
		t.Integration, t.Deliverables = nil, nil
		t.LastError, t.ActionNeeded, t.GitStatus = "", "Launching task-linked Swarm follow-up", "unknown"
		t.WhatDidDo, t.WhatNotDone = nil, nil
		t.Revision++
		return nil
	})
}

// TaskAttemptPage bounds returned history without trimming durable attempts.
func (t *ProjectTaskRecord) TaskAttemptPage(cursor, limit int) ([]ProjectTaskAttempt, int, error) {
	if cursor < 0 || limit < 1 || limit > 50 || cursor > len(t.Attempts) {
		return nil, 0, errors.New("invalid history cursor or limit (1-50)")
	}
	end := cursor + limit
	if end > len(t.Attempts) {
		end = len(t.Attempts)
	}
	rows := append([]ProjectTaskAttempt{}, t.Attempts[cursor:end]...)
	next := 0
	if end < len(t.Attempts) {
		next = end
	}
	return rows, next, nil
}

// Read projection derives current outcome only from matching durable session/run
// provenance; it never publishes an event or changes the historical request.
func (s *SessionStore) hydrateTaskAttemptOutcome(task *ProjectTaskRecord) {
	a := task.ActiveAttempt()
	if a == nil {
		return
	}
	if a.SummaryRunID != a.RunID {
		a.Summary, a.SummaryRunID = "", ""
	}
	state, found, err := s.GetV3SessionRunState(a.SessionID)
	if err != nil || !found || state.AccountScopeID != task.AccountID {
		return
	}
	if a.RunID != "" && a.RunID != state.RunID {
		a.Summary, a.SummaryRunID = "", ""
		return
	}
	a.RunID = state.RunID
	if state.Active {
		a.Summary, a.SummaryRunID = "", ""
		return
	}
	owned, found, err := s.GetSession(a.SessionID)
	if err != nil || !found || owned.AccountScopeID != task.AccountID {
		return
	}
	if task.PlanBinding != nil && task.PlanBinding.SessionID == a.SessionID {
		plan, found, err := s.GetPlan(a.SessionID, task.PlanBinding.PlanID)
		if err == nil && found && plan.AccountScopeID == task.AccountID && plan.Document != nil {
			for _, checkpoint := range plan.Document.Checkpoints {
				if !state.Active && checkpoint.Status == "completed" && checkpoint.SessionID == a.SessionID && checkpoint.RunID == state.RunID && checkpoint.Handoff != nil && len(checkpoint.Handoff.Overview) <= 4000 {
					a.Summary, a.SummaryRunID = checkpoint.Handoff.Overview, checkpoint.RunID
				}
			}
		}
	}
	if summary, ok := s.completedTaskRunSummary(state); ok {
		a.Summary, a.SummaryRunID = summary, state.RunID
		return
	}
	if !state.Active && owned.Metadata["lifecycle_summary_run_id"] == state.RunID && owned.Metadata["lifecycle_signal"] == "needs_review" {
		if summary, ok := owned.Metadata["lifecycle_summary"].(string); ok && len(summary) <= 4000 {
			a.Summary, a.SummaryRunID = summary, state.RunID
		}
	}
}

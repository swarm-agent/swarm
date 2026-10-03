package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const V3SessionMutationReportTask = "project_task.report"

type ProjectTaskUpdateKind string

const (
	ProjectTaskUpdateProgress    ProjectTaskUpdateKind = "progress"
	ProjectTaskUpdateAttention   ProjectTaskUpdateKind = "attention"
	ProjectTaskUpdateWakeRequest ProjectTaskUpdateKind = "wake_request"
)

// V3ProjectTaskReportMutation is supplied only by the authenticated provider tool.
// Session and run identity come from execution context, never model arguments.
// A report records intent; it does not authorize a new run or task transition.
type V3ProjectTaskReportMutation struct {
	RunID     string
	ProjectID string
	TaskID    string
	Kind      ProjectTaskUpdateKind
	Summary   string
}

// ProjectTaskUpdate is immutable event data, not instructions or accepted scope.
// EventSeq is scoped to SessionID. Delivery must retain this exact identity.
type ProjectTaskUpdate struct {
	AccountScopeID  string                `json:"account_scope_id"`
	UserID          string                `json:"user_id"`
	ProjectID       string                `json:"project_id"`
	ParentSessionID string                `json:"parent_session_id"`
	ParentRunID     string                `json:"parent_run_id,omitempty"`
	ParentEpochID   string                `json:"parent_epoch_id,omitempty"`
	ParentFence     string                `json:"parent_fence,omitempty"`
	QueueSeq        uint64                `json:"queue_seq,omitempty"`
	TaskID          string                `json:"task_id"`
	AttemptID       string                `json:"attempt_id"`
	SessionID       string                `json:"session_id"`
	RunID           string                `json:"run_id"`
	EventID         string                `json:"event_id"`
	EventSeq        uint64                `json:"event_seq"`
	Kind            ProjectTaskUpdateKind `json:"kind"`
	Summary         string                `json:"summary"`
}

// prepareProjectTaskReport runs under projectsMu and account/session mutation
// locks, before idempotency lookup: stale attempts cannot replay a saved receipt.
func (s *SessionStore) prepareProjectTaskReport(input *V3SessionMutationInput) error {
	op := input.TaskReport
	if op == nil {
		return nil
	}
	if input.Kind != V3SessionMutationReportTask || input.Message != nil || input.RunIntent != nil || input.Session != nil || input.TaskWait != nil {
		return errors.New("task reports require the dedicated report mutation")
	}
	if op.RunID == "" || op.ProjectID == "" || op.TaskID == "" || strings.TrimSpace(op.Summary) == "" || len(op.Summary) > 4000 || !utf8.ValidString(op.Summary) {
		return errors.New("report_task requires a current run, project, task and summary (1-4000 UTF-8 bytes)")
	}
	switch op.Kind {
	case ProjectTaskUpdateProgress, ProjectTaskUpdateAttention, ProjectTaskUpdateWakeRequest:
	default:
		return errors.New("update_kind must be progress, attention or wake_request")
	}
	child, found, err := s.GetSession(input.SessionID)
	if err != nil {
		return err
	}
	if !found || child.AccountScopeID != input.AccountScopeID || child.UserID != input.UserID || child.Metadata["project_id"] != op.ProjectID || (child.Metadata["task_id"] != op.TaskID && child.Metadata["project_task_id"] != op.TaskID) {
		return errors.New("reporting session is not linked to the authenticated task")
	}
	task, found, err := s.GetProjectTask(input.AccountScopeID, op.ProjectID, op.TaskID)
	if err != nil {
		return err
	}
	if !found || task.Archived || task.AccountID != input.AccountScopeID || task.ProjectID != op.ProjectID {
		return errors.New("report task is unavailable")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	if attempt == nil || attempt.SessionID != input.SessionID || task.SessionID != input.SessionID || (attempt.UserID != "" && attempt.UserID != input.UserID) {
		return errors.New("reporting session does not own the active task attempt")
	}
	switch task.Status {
	case "in_progress", "planning":
	default:
		return errors.New("only executing task attempts may report")
	}
	state, found, err := s.GetV3SessionRunState(input.SessionID)
	if err != nil {
		return err
	}
	if !found || state.RunID != op.RunID || state.Status != V3RunIntentRunning {
		return errors.New("reporting provider run is no longer current")
	}
	run, found, err := s.GetV3SessionRunIntent(input.SessionID, op.RunID)
	if err != nil {
		return err
	}
	if !found || run.AccountScopeID != input.AccountScopeID || run.UserID != input.UserID || run.Status != V3RunIntentRunning {
		return errors.New("reporting run ownership mismatch")
	}
	if epoch, ok, err := s.GetActiveExecutionEpoch(input.SessionID); err != nil {
		return err
	} else if run.EpochID != "" && (!ok || epoch.EpochID != run.EpochID) {
		return errors.New("reporting execution epoch is no longer current")
	}
	// Deployment lineage survives checkpoint runs whose run parent is self.
	// Never retarget reports through the project's mutable primary pointer.
	parentID, _ := child.Metadata["parent_session_id"].(string)
	if parentID == "" {
		parentID = run.ParentSessionID
	}
	if parentID == "" || parentID == child.ID {
		return errors.New("task has no captured Orchestrator session")
	}
	parent, found, err := s.GetSession(parentID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("task Orchestrator session unavailable")
	}
	if err := s.ValidateProjectConversation(parent, input.AccountScopeID, input.UserID); err != nil {
		return err
	}
	if ProjectConversationID(parent) != op.ProjectID {
		return errors.New("task Orchestrator project mismatch")
	}
	for _, id := range []string{child.ID, parent.ID} {
		tomb, found, err := s.GetV3SessionTombstone(id)
		if err != nil {
			return err
		}
		if found && (tomb.Archived || tomb.Deleted) {
			return errors.New("task report session archived or deleted")
		}
	}
	// Hash actual typed content here rather than trusting a caller-provided hash.
	payload, _ := json.Marshal(op)
	sum := sha256.Sum256(payload)
	input.PayloadHash, input.RequestHash = hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])
	event := sha256.Sum256([]byte(input.AccountScopeID + "\x00" + input.SessionID + "\x00" + input.ClientRequestID))
	input.EventID = "task-update-" + hex.EncodeToString(event[:16])
	input.EventType = "session.task.reported"
	input.taskUpdate = &ProjectTaskUpdate{AccountScopeID: input.AccountScopeID, UserID: input.UserID, ProjectID: op.ProjectID, ParentSessionID: parent.ID, TaskID: op.TaskID, AttemptID: attempt.ID, SessionID: child.ID, RunID: op.RunID, EventID: input.EventID, Kind: op.Kind, Summary: op.Summary}
	input.EventPayload = nil
	return s.bindProjectTaskUpdate(input.taskUpdate)
}

package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ProjectTaskRepositoryContinuation pins a result, not a grant to an old session.
// Allocation always creates a new session-owned lane in Source's repository.
type ProjectTaskRepositoryContinuation struct {
	Source          ProjectTaskSource         `json:"source"`
	AttemptID       string                    `json:"attempt_id"`
	SessionID       string                    `json:"session_id"`
	ProgramID       string                    `json:"program_id"`
	ProgramRevision int                       `json:"program_revision"`
	Lane            TaskProgramRepositoryLane `json:"lane"`
	HeadCommit      string                    `json:"head_commit"`
	TargetBranch    string                    `json:"target_branch"`
	TargetHead      string                    `json:"target_head"`
}

func equalTaskRepositoryContinuations(a, b []ProjectTaskRepositoryContinuation) bool {
	if len(a) != len(b) {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// AuthenticateTaskRepositoryProgram proves the task/attempt owner before either
// selecting results or disregarding a terminal, provably empty program. Checkpoint
// execution can have another run ID; its exact run intent and recorded attempt in
// the bound accepted plan must agree. A session ID alone never authorizes a run.
func (s *SessionStore) AuthenticateTaskRepositoryProgram(task *ProjectTaskRecord, user, attemptID, sessionID, programID string) (TaskProgramRecord, error) {
	fail := errors.New("retained repository program provenance mismatch")
	var zero TaskProgramRecord
	if task == nil || attemptID == "" || sessionID == "" || programID == "" {
		return zero, fail
	}
	var attempt *ProjectTaskAttempt
	copyTask := *task
	copyTask.Attempts = append([]ProjectTaskAttempt(nil), task.Attempts...)
	copyTask.EnsureTaskAttempts()
	copyTask.CaptureActiveAttempt()
	for i := range copyTask.Attempts {
		a := &copyTask.Attempts[i]
		if a.ID == attemptID && a.SessionID == sessionID {
			attempt = a
		}
	}
	if attempt == nil {
		return zero, fail
	}
	owner, found, err := s.GetSession(sessionID)
	if err != nil {
		return zero, err
	}
	if !found || owner.AccountScopeID != task.AccountID || owner.UserID != user || owner.Metadata["project_id"] != task.ProjectID || owner.Metadata["task_id"] != task.ID || owner.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || owner.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path || fmt.Sprint(owner.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
		return zero, fail
	}
	if attempt.ID != "initial" && owner.Metadata["task_attempt_id"] != attempt.ID {
		return zero, fail
	}
	program, found, err := s.GetTaskProgram(sessionID, programID)
	if err != nil {
		return zero, err
	}
	if !found || program.ParentSessionID != sessionID || program.ProgramID != programID {
		return zero, fail
	}
	expectedRun := attempt.RunID
	if expectedRun == "" && attempt.ID == "initial" {
		expectedRun = fmt.Sprintf("desktop-v3-run:task-%s", task.ID)
	}
	if expectedRun == "" || program.ReservationRunID == "" {
		return zero, fail
	}
	if program.ReservationRunID != expectedRun {
		intent, found, err := s.GetV3SessionRunIntent(sessionID, program.ReservationRunID)
		if err != nil {
			return zero, err
		}
		binding := attempt.PlanBinding
		if !found || intent.AccountScopeID != task.AccountID || intent.UserID != user || intent.SessionID != sessionID || (intent.RunSessionID != "" && intent.RunSessionID != sessionID) || (intent.ParentSessionID != sessionID && intent.ParentSessionID != task.OriginSessionID) || binding == nil || binding.SessionID != sessionID || binding.PlanID != intent.PlanID || intent.CheckpointID == "" || intent.AttemptID == "" {
			return zero, fail
		}
		plan, found, err := s.GetPlan(sessionID, binding.PlanID)
		if err != nil {
			return zero, err
		}
		if !found || plan.Document == nil || plan.AcceptedDefinitionReceipt != binding.Receipt || plan.AccountScopeID != task.AccountID || plan.UserID != user || plan.SessionID != sessionID || plan.ApprovalState != "approved" {
			return zero, fail
		}
		definition, found, err := s.GetPlanRevision(sessionID, binding.PlanID, binding.DefinitionRevision)
		if err != nil {
			return zero, err
		}
		if !found && plan.Version == binding.DefinitionRevision {
			definition, found = plan, true
		}
		if !found || definition.Document == nil || definition.SessionID != sessionID || definition.UserID != user || definition.AccountScopeID != task.AccountID || definition.Version != binding.DefinitionRevision {
			return zero, fail
		}
		raw, err := json.Marshal(definition.Document)
		if err != nil {
			return zero, err
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != binding.Receipt {
			return zero, fail
		}
		matched := false
		for _, checkpoint := range plan.Document.Checkpoints {
			if checkpoint.ID != intent.CheckpointID {
				continue
			}
			for _, execution := range checkpoint.Attempts {
				matched = matched || (execution.ID == intent.AttemptID && execution.RunID == intent.RunID && execution.SessionID == sessionID && execution.ParentSessionID == sessionID)
			}
		}
		if !matched {
			return zero, fail
		}
	}
	return program, nil
}

// AuthenticateTaskRepositoryContinuation binds immutable evidence to the recorded
// same-task attempt and terminal durable program, never caller-supplied Git facts.
// The caller must additionally verify catalog identity and real Git ownership.
func (s *SessionStore) AuthenticateTaskRepositoryContinuation(task *ProjectTaskRecord, user string, ref ProjectTaskRepositoryContinuation) (TaskProgramRecord, error) {
	fail := errors.New("retained repository result provenance mismatch")
	var zero TaskProgramRecord
	if ref.HeadCommit == "" || ref.TargetBranch == "" || ref.TargetHead == "" {
		return zero, fail
	}
	program, err := s.AuthenticateTaskRepositoryProgram(task, user, ref.AttemptID, ref.SessionID, ref.ProgramID)
	if err != nil {
		return zero, err
	}
	if program.Revision != ref.ProgramRevision || program.State != TaskProgramStateCompleted {
		return zero, fail
	}
	lane, found := program.RepositoryLanes[ref.Source.Path]
	if !found || lane != ref.Lane || lane.SourcePath != ref.Source.Path || lane.WorkspaceID != ref.Source.WorkspaceID || lane.WorkspaceGeneration != ref.Source.WorkspaceGeneration || lane.BaseCommit == "" || ref.HeadCommit == lane.BaseCommit || program.LaneHeads[ref.Source.Path] != ref.HeadCommit {
		return zero, fail
	}
	owner, found, err := s.GetSession(ref.SessionID)
	if err != nil {
		return zero, err
	}
	if !found {
		return zero, fail
	}
	granted := ref.Source.SameIdentity(task.SourceWorkspace)
	for _, grant := range owner.WorkspaceGrants {
		granted = granted || (grant.WorkspaceID == ref.Source.WorkspaceID && grant.WorkspaceGeneration == ref.Source.WorkspaceGeneration && grant.Path == ref.Source.Path)
	}
	if !granted {
		return zero, fail
	}
	integrated := false
	for _, job := range program.Jobs {
		if job.SourceWorkspacePath != ref.Source.Path {
			continue
		}
		coder := false
		for _, def := range program.Definition.Jobs {
			if def.ID == job.JobID && def.AgentType == "coder" && def.WorkspacePath == ref.Source.Path {
				coder = true
			}
		}
		if !coder {
			continue
		}
		if job.State != TaskProgramJobIntegrated || job.IntegrationState != "integrated" || job.ChildHead == "" || job.ChildHead == job.ImmutableStageBase {
			return zero, fail
		}
		integrated = true
	}
	if !integrated {
		return zero, fail
	}
	for _, def := range program.Definition.Jobs {
		if def.AgentType != "coder" || def.WorkspacePath != ref.Source.Path {
			continue
		}
		matched := false
		for _, job := range program.Jobs {
			matched = matched || (job.JobID == def.ID && job.SourceWorkspacePath == ref.Source.Path && job.State == TaskProgramJobIntegrated && job.IntegrationState == "integrated" && job.ChildHead != "" && job.ChildHead != job.ImmutableStageBase)
		}
		if !matched {
			return zero, fail
		}
	}
	return program, nil
}

// TaskRepositoryContinuationsForSession is scheduler-only. Session metadata does
// not confer continuation authority; the current durable attempt must own it.
func (s *SessionStore) TaskRepositoryContinuationsForSession(owner SessionSnapshot) ([]ProjectTaskRepositoryContinuation, error) {
	if attempt, _ := owner.Metadata["task_attempt_id"].(string); attempt == "" || attempt == "initial" {
		return nil, nil
	}
	project, _ := owner.Metadata["project_id"].(string)
	taskID, _ := owner.Metadata["task_id"].(string)
	if project == "" || taskID == "" {
		return nil, nil
	}
	task, found, err := s.GetProjectTask(owner.AccountScopeID, project, taskID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("task continuation owner unavailable")
	}
	a := task.ActiveAttempt()
	if a == nil || len(a.RepositoryContinuations) == 0 {
		return nil, nil
	}
	if a.SessionID != owner.ID || task.SessionID != owner.ID {
		return nil, errors.New("task continuation attempt owner mismatch")
	}
	if a.UserID != owner.UserID || owner.Metadata["task_attempt_id"] != a.ID {
		return nil, errors.New("task continuation principal mismatch")
	}
	for _, ref := range a.RepositoryContinuations {
		if _, err := s.AuthenticateTaskRepositoryContinuation(task, owner.UserID, ref); err != nil {
			return nil, err
		}
	}
	return append([]ProjectTaskRepositoryContinuation(nil), a.RepositoryContinuations...), nil
}

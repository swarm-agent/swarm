package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SameIdentity compares catalog authority, not how that authority was selected.
func (s ProjectTaskSource) SameIdentity(other ProjectTaskSource) bool {
	return s.Path == other.Path && s.WorkspaceID == other.WorkspaceID && s.WorkspaceGeneration == other.WorkspaceGeneration
}

// ValidateTaskPlanSources never admits authority from authored prose or project
// membership. Additional repositories must enter through explicit task admission.
func ValidateTaskPlanSources(task *ProjectTaskRecord, doc *SessionPlanDocument) error {
	if doc == nil {
		return nil
	}
	for _, cp := range doc.Checkpoints {
		if cp.TaskProgram == nil {
			continue
		}
		for _, job := range cp.TaskProgram.Jobs {
			if job.AgentType != "coder" && job.AgentType != "finder" {
				continue
			}
			path := strings.TrimSpace(job.WorkspacePath)
			if path == "" {
				path = task.SourceWorkspace.Path
			}
			admitted := false
			for _, source := range append([]ProjectTaskSource{task.SourceWorkspace}, task.ProgramSources...) {
				if source.Path == path && source.WorkspaceID != "" && source.WorkspaceGeneration > 0 {
					admitted = true
				}
			}
			if !admitted {
				return fmt.Errorf("plan job %q requires unadmitted source %q: submit this unchanged structured plan as a new task with explicit repository admission and review; publication cannot grant repository authority", job.ID, path)
			}
		}
	}
	return nil
}

// prepareTaskPlanPublication joins the plan and task binding in one durable batch.
// The project lock excludes archive, reopen and source changes during this CAS.
func (s *SessionStore) prepareTaskPlanPublication(input V3SessionMutationInput) (*projectRealtimeMutation, error) {
	if input.PlanSave == nil || input.PlanSave.TaskPublication == nil {
		return nil, nil
	}
	next := input.PlanSave.TaskPublication
	current, found, err := s.GetProjectTask(input.AccountScopeID, next.ProjectID, next.ID)
	if err != nil {
		return nil, err
	}
	if !found || current.Archived || current.Revision != next.Revision || current.AccountID != input.AccountScopeID || (current.SessionID != "" && current.SessionID != input.SessionID) || current.ActiveAttemptID != next.ActiveAttemptID {
		return nil, errors.New("task plan publication changed concurrently or task is archived; reload before republishing")
	}
	session, found, err := s.GetSession(input.SessionID)
	if err != nil {
		return nil, err
	}
	if !found || session.AccountScopeID != input.AccountScopeID || session.UserID != input.UserID || session.Metadata["project_id"] != next.ProjectID || session.Metadata["task_id"] != next.ID {
		return nil, errors.New("task publication session ownership mismatch")
	}
	if attemptID, _ := session.Metadata["task_attempt_id"].(string); attemptID != "" && current.ActiveAttemptID != "" && attemptID != current.ActiveAttemptID {
		return nil, errors.New("task publication attempt mismatch")
	}
	plan := input.PlanSave.Plan
	raw, err := json.Marshal(plan.Document)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	binding := next.PlanBinding
	if binding == nil || binding.SessionID != input.SessionID || binding.PlanID != plan.ID || binding.DefinitionRevision != plan.Version || binding.Receipt != hex.EncodeToString(digest[:]) {
		return nil, errors.New("task publication must bind the exact plan definition")
	}
	if !next.SourceWorkspace.SameIdentity(current.SourceWorkspace) || len(next.ProgramSources) != len(current.ProgramSources) {
		return nil, errors.New("task publication cannot change admitted sources")
	}
	for i := range current.ProgramSources {
		if !next.ProgramSources[i].SameIdentity(current.ProgramSources[i]) {
			return nil, errors.New("task publication cannot change admitted sources")
		}
	}
	if err := ValidateTaskPlanSources(current, input.PlanSave.Plan.Document); err != nil {
		return nil, err
	}
	next.Revision++
	next.CaptureActiveAttempt()
	mutation := &projectRealtimeMutation{accountScopeID: input.AccountScopeID, projectID: next.ProjectID}
	if err := mutation.put(KeyProjectTask(input.AccountScopeID, next.ProjectID, next.ID), next); err != nil {
		return nil, err
	}
	return mutation, nil
}

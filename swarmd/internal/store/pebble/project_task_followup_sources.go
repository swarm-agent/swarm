package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResolveTaskFollowupSources recovers source authority, not executable plan state.
// Historical paths are usable only when the exact accepted definition and its
// task-owned session's catalog grants agree. Catalog/readiness revalidation is
// still required by the API before allocation; project membership is not evidence.
func (s *SessionStore) ResolveTaskFollowupSources(task *ProjectTaskRecord, user string) ([]ProjectTaskSource, error) {
	const reapprove = "follow-up source approval evidence unavailable: explicitly submit the unchanged structured plan for this task's current attempt and accept its exact session, plan and definition revision before retrying; no source authority was inferred"
	binding := task.PlanBinding
	if binding == nil {
		for i := len(task.Attempts) - 1; i >= 0; i-- {
			if task.Attempts[i].PlanBinding != nil {
				binding = task.Attempts[i].PlanBinding
				break
			}
		}
	}
	if binding == nil && len(task.ProgramSources) == 0 && len(task.CoderAssignments) == 0 {
		for _, attempt := range task.Attempts {
			if attempt.TaskProgramID != "" {
				return nil, errors.New(reapprove)
			}
		}
		return nil, nil // The ordinary single-source contract needs no extra grant.
	}
	sessionID := task.SessionID
	if binding == nil && task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" && len(task.Attempts) > 0 {
		// Direct multi-source admission predates this fresh coordinator. Authenticate
		// against its retained original owner, not a not-yet-created follow-up.
		sessionID = task.Attempts[0].SessionID
	}
	if binding != nil {
		sessionID = binding.SessionID
		associated := sessionID == task.SessionID
		for _, attempt := range task.Attempts {
			associated = associated || attempt.SessionID == sessionID
		}
		if !associated || binding.Receipt == "" || binding.DefinitionRevision <= 0 {
			return nil, errors.New(reapprove)
		}
	}
	owned, found, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !found || owned.AccountScopeID != task.AccountID || owned.UserID != user || owned.Metadata["project_id"] != task.ProjectID || owned.Metadata["task_id"] != task.ID || owned.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path || owned.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || fmt.Sprint(owned.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
		return nil, errors.New(reapprove)
	}
	for _, attempt := range task.Attempts {
		if attempt.SessionID == sessionID && attempt.ID != "initial" && owned.Metadata["task_attempt_id"] != attempt.ID {
			return nil, errors.New(reapprove)
		}
	}
	candidates := append([]ProjectTaskSource(nil), task.ProgramSources...)
	if binding != nil {
		accepted, ok, err := s.GetPlan(sessionID, binding.PlanID)
		if err != nil {
			return nil, err
		}
		if !ok || accepted.AccountScopeID != task.AccountID || accepted.UserID != user || accepted.SessionID != sessionID || accepted.ApprovalState != "approved" || accepted.AcceptedDefinitionReceipt != binding.Receipt {
			return nil, errors.New(reapprove)
		}
		definition, ok, err := s.GetPlanRevision(sessionID, binding.PlanID, binding.DefinitionRevision)
		if err != nil {
			return nil, err
		}
		if !ok && accepted.Version == binding.DefinitionRevision {
			definition, ok = accepted, true
		}
		raw, err := json.Marshal(definition.Document)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		if !ok || definition.ID != binding.PlanID || definition.Version != binding.DefinitionRevision || definition.AccountScopeID != task.AccountID || definition.UserID != user || definition.SessionID != sessionID || definition.Document == nil || hex.EncodeToString(digest[:]) != binding.Receipt {
			return nil, errors.New(reapprove)
		}
		candidates = []ProjectTaskSource{task.SourceWorkspace}
		seen := map[string]bool{task.SourceWorkspace.Path: true}
		for _, cp := range definition.Document.Checkpoints {
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
				if seen[path] {
					continue
				}
				seen[path] = true
				var source ProjectTaskSource
				for _, grant := range owned.WorkspaceGrants {
					if grant.Path != path || grant.WorkspaceID == "" || grant.WorkspaceGeneration <= 0 {
						continue
					}
					if source.Path != "" && (source.WorkspaceID != grant.WorkspaceID || source.WorkspaceGeneration != grant.WorkspaceGeneration) {
						return nil, errors.New(reapprove)
					}
					source = ProjectTaskSource{Path: path, WorkspaceID: grant.WorkspaceID, WorkspaceGeneration: grant.WorkspaceGeneration, Provenance: "approved_plan"}
				}
				if source.Path == "" {
					return nil, errors.New(reapprove)
				}
				candidates = append(candidates, source)
			}
		}
		// Never use recovery to silently replace a conflicting current admission.
		for _, current := range task.ProgramSources {
			matched := false
			for i, source := range candidates {
				if current.SameIdentity(source) {
					candidates[i].Provenance = current.Provenance
					matched = true
				}
			}
			if !matched {
				return nil, errors.New(reapprove)
			}
		}
	} else {
		// Retain admitted assignment sources, never the mutable assignments themselves.
		for _, assignment := range task.CoderAssignments {
			candidates = append(candidates, assignment.SourceWorkspace)
		}
	}
	var sources []ProjectTaskSource
	seen := map[string]ProjectTaskSource{}
	for _, source := range candidates {
		if source.WorkspaceID == "" || source.WorkspaceGeneration <= 0 || source.Path == "" || source.Provenance == "" {
			return nil, errors.New(reapprove)
		}
		if prior, ok := seen[source.Path]; ok {
			if !prior.SameIdentity(source) {
				return nil, errors.New(reapprove)
			}
			continue
		}
		matched := source.SameIdentity(task.SourceWorkspace)
		for _, grant := range owned.WorkspaceGrants {
			matched = matched || (grant.Path == source.Path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration)
		}
		if !matched {
			return nil, errors.New(reapprove)
		}
		seen[source.Path] = source
		sources = append(sources, source)
	}
	return sources, nil
}

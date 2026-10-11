package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

func projectTaskSubmissionHash(projectID string, input tool.ProjectTaskCreateInput, source pebblestore.ProjectTaskSource) (string, error) {
	// Client transport metadata is carried separately; source provenance is included
	// so an implicit selection cannot be replayed as an explicit different contract.
	input.ID = ""
	input.SessionID = ""
	input.ClientRequestID = ""
	raw, err := json.Marshal(struct {
		ProjectID string
		Input     tool.ProjectTaskCreateInput
		Source    pebblestore.ProjectTaskSource
	}{projectID, input, source})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// resolveProjectTaskSource binds execution to an exact catalog root, not a project
// description, a linked subdirectory, or the first workspace in a list.
func (s *Server) resolveProjectTaskSource(p identity.Principal, proj *pebblestore.ProjectRecord, requestedPath, requestedID string, requestedGeneration int64, requireRepository bool) (pebblestore.ProjectTaskSource, error) {
	if proj == nil || s.workspace == nil {
		return pebblestore.ProjectTaskSource{}, errors.New("project workspace catalog is unavailable")
	}
	path, id := strings.TrimSpace(requestedPath), strings.TrimSpace(requestedID)
	if requestedGeneration < 0 {
		return pebblestore.ProjectTaskSource{}, errors.New("invalid source workspace generation")
	}
	var candidates []pebblestore.ProjectTaskSource
	seen := make(map[string]bool)
	for _, ref := range proj.Workspaces {
		root := strings.TrimSpace(ref.Path)
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || seen[root] {
			continue
		}
		seen[root] = true
		if path != "" && path != root || id != "" && ref.WorkspaceID != "" && id != ref.WorkspaceID {
			continue
		}
		scope, err := s.workspace.ScopeForPathForPrincipal(p, root)
		if err != nil || !scope.Matched || scope.WorkspacePath != root || scope.ResolvedPath != root || scope.WorkspaceID == "" || scope.WorkspaceGeneration <= 0 {
			if path == root || id != "" && id == ref.WorkspaceID {
				return pebblestore.ProjectTaskSource{}, fmt.Errorf("project workspace %q is not an authorized canonical catalog root: %v", root, err)
			}
			continue
		}
		if ref.WorkspaceID != "" && ref.WorkspaceID != scope.WorkspaceID {
			return pebblestore.ProjectTaskSource{}, fmt.Errorf("project workspace %q has a stale workspace ID", root)
		}
		if id != "" && id != scope.WorkspaceID {
			continue
		}
		if requestedGeneration > 0 && requestedGeneration != scope.WorkspaceGeneration {
			return pebblestore.ProjectTaskSource{}, errors.New("source workspace generation is stale")
		}
		if requireRepository {
			state, err := s.workspace.InspectRepositoryForPrincipal(p, root)
			if err != nil {
				return pebblestore.ProjectTaskSource{}, fmt.Errorf("inspect coding source %q: %w; restore source access or select another workspace; project chat remains available", root, err)
			}
			if state.State != workspace.RepositoryStateReady || state.Repository != root {
				if path == root || id == scope.WorkspaceID {
					return pebblestore.ProjectTaskSource{}, fmt.Errorf("project workspace %q is not coding-ready (state: %s): use workspace Git setup to initialize and commit the intended repository, or select another source; project chat remains available", root, state.State)
				}
				continue
			}
		}
		provenance := "unique_project_workspace"
		if path != "" || id != "" {
			provenance = "explicit"
		}
		candidates = append(candidates, pebblestore.ProjectTaskSource{WorkspaceID: scope.WorkspaceID, WorkspaceGeneration: scope.WorkspaceGeneration, Path: root, Provenance: provenance})
	}
	if len(candidates) != 1 {
		return pebblestore.ProjectTaskSource{}, fmt.Errorf("execution target unresolved: expected one authorized project workspace, found %d; specify workspace_id or workspace_path", len(candidates))
	}
	return candidates[0], nil
}

func (s *Server) revalidateProjectTaskSource(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) error {
	if isOrdinaryMediaAgent(task.Agent) && task.SessionID == "" && task.TaskProgram == nil && task.TaskProgramID == "" && task.PlanBinding == nil && task.PlanDocument == nil && len(task.CoderAssignments) == 0 && len(task.ProgramSources) == 0 && task.SourceWorkspace.Path == "" {
		_, err := pebblestore.RouteAndPlanProjectTaskWithOptions(pebblestore.TaskPlanOptions{Agent: task.Agent, OutcomeType: task.OutcomeType, Tier: task.Tier, VariantCount: task.VariantCount})
		return err
	}
	if task.SourceWorkspace.WorkspaceID == "" || task.SourceWorkspace.Path == "" || task.SourceWorkspace.WorkspaceGeneration <= 0 {
		return errors.New("task has no durable source workspace binding")
	}
	bound, err := s.resolveProjectTaskSource(p, proj, task.SourceWorkspace.Path, task.SourceWorkspace.WorkspaceID, task.SourceWorkspace.WorkspaceGeneration, !isDirectMediaTask(task))
	if err != nil {
		return err
	}
	if bound.Path != task.SourceWorkspace.Path || bound.WorkspaceID != task.SourceWorkspace.WorkspaceID {
		return errors.New("task source workspace changed")
	}
	for _, source := range task.ContextSources {
		if source.WorkspaceID == "" || source.WorkspaceGeneration <= 0 || source.Path == "" {
			return errors.New("context has no durable catalog binding")
		}
		if _, err := s.resolveProjectTaskSource(p, proj, source.Path, source.WorkspaceID, source.WorkspaceGeneration, false); err != nil {
			return fmt.Errorf("context source: %w", err)
		}
	}
	for _, source := range task.ProgramSources {
		if source.WorkspaceID == "" || source.WorkspaceGeneration <= 0 || source.Path == "" {
			return errors.New("program has no durable source binding")
		}
		if _, err := s.resolveProjectTaskSource(p, proj, source.Path, source.WorkspaceID, source.WorkspaceGeneration, true); err != nil {
			return fmt.Errorf("program source: %w", err)
		}
	}
	if task.PlanDocument != nil {
		resolved, err := s.resolveProjectPlanSources(p, proj, task.PlanDocument, task.SourceWorkspace)
		if err != nil {
			return err
		}
		for _, source := range resolved {
			found := source.Path == task.SourceWorkspace.Path && source.WorkspaceID == task.SourceWorkspace.WorkspaceID && source.WorkspaceGeneration == task.SourceWorkspace.WorkspaceGeneration
			for _, bound := range task.ProgramSources {
				found = found || source.SameIdentity(bound)
			}
			if !found {
				return fmt.Errorf("plan program source %q differs from durable admission binding; preserve this document and explicitly submit it as a new structured task for source admission and review (archived tasks cannot be accepted)", source.Path)
			}
		}
	}
	for i, a := range task.CoderAssignments {
		if a.SourceWorkspace.WorkspaceID == "" || a.SourceWorkspace.WorkspaceGeneration <= 0 || a.SourceWorkspace.Path == "" {
			return fmt.Errorf("coder assignment %d has no durable source binding", i+1)
		}
		bound, err := s.resolveProjectTaskSource(p, proj, a.SourceWorkspace.Path, a.SourceWorkspace.WorkspaceID, a.SourceWorkspace.WorkspaceGeneration, true)
		if err != nil {
			return fmt.Errorf("coder assignment %d: %w", i+1, err)
		}
		if bound.Path != a.WorkspacePath || bound.WorkspaceID != a.WorkspaceID || bound.WorkspaceGeneration != a.WorkspaceGeneration {
			return fmt.Errorf("coder assignment %d source changed", i+1)
		}
	}
	return nil
}

// workerRunTaskSession reports whether session is the worker execution
// session of exactly this task's worker run.
func workerRunTaskSession(task *pebblestore.ProjectTaskRecord, session pebblestore.SessionSnapshot) bool {
	return task.WorkerRunID != "" && task.WorkerID != "" &&
		session.Metadata[pebblestore.SessionPurposeMetadataKey] == pebblestore.SessionPurposeAutomationExecution &&
		session.Metadata["worker_execution_run_id"] == task.WorkerRunID &&
		session.Metadata["worker_id"] == task.WorkerID
}

func verifyProjectTaskSession(task *pebblestore.ProjectTaskRecord, session pebblestore.SessionSnapshot, accountID string) error {
	if session.ID != task.SessionID || session.AccountScopeID != accountID || session.Metadata == nil || session.Metadata["project_id"] != task.ProjectID || session.Metadata["task_id"] != task.ID {
		return errors.New("task session ownership does not match reservation")
	}
	if task.SourceWorkspace.Path != "" {
		if session.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path || session.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || fmt.Sprint(session.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
			return errors.New("task session source binding does not match reservation")
		}
	}
	if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" && session.Metadata["task_attempt_id"] != task.ActiveAttemptID {
		return errors.New("task attempt provenance mismatch")
	}
	if session.WorktreeEnabled {
		// Project task sessions run with the worktree as their workspace. A
		// worker run's session keeps its approved source workspace and runs in
		// its own worktree; accept that shape only for this task's own run.
		workspaceOK := session.WorkspacePath == session.WorktreeRootPath ||
			(workerRunTaskSession(task, session) && task.SourceWorkspace.Path != "" && session.WorkspacePath == task.SourceWorkspace.Path)
		if session.WorktreeRootPath == "" || !workspaceOK || session.Metadata["swarm_v3_worktree_owner_session_id"] != session.ID || session.Metadata["swarm_v3_runtime_workspace_path"] != session.WorktreeRootPath || task.WorkspacePath != session.WorktreeRootPath {
			return errors.New("task session worktree owner or runtime path does not match reservation")
		}
	} else if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" || task.PlanBinding != nil {
		return errors.New("coding or plan task session has no isolated worktree")
	}
	return nil
}

// resolveProjectPlanSources resolves only declared Coder/Finder sources before
// task reservation. Project membership alone never grants filesystem authority.
func (s *Server) resolveProjectPlanSources(p identity.Principal, proj *pebblestore.ProjectRecord, doc *pebblestore.SessionPlanDocument, primary pebblestore.ProjectTaskSource) ([]pebblestore.ProjectTaskSource, error) {
	var sources []pebblestore.ProjectTaskSource
	if doc == nil {
		return sources, nil
	}
	seen := map[string]bool{}
	for _, checkpoint := range doc.Checkpoints {
		if checkpoint.TaskProgram == nil {
			continue
		}
		for _, job := range checkpoint.TaskProgram.Jobs {
			if job.AgentType != "coder" && job.AgentType != "finder" {
				continue
			}
			path := strings.TrimSpace(job.WorkspacePath)
			if path == "" {
				path = primary.Path
			}
			if seen[path] {
				continue
			}
			source, err := s.resolveProjectTaskSource(p, proj, path, "", 0, true)
			if err != nil {
				return nil, fmt.Errorf("program job %q: %w", job.ID, err)
			}
			seen[path] = true
			sources = append(sources, source)
		}
	}
	return sources, nil
}

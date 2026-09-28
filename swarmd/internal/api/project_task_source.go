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
			if err != nil || state.State != workspace.RepositoryStateReady || state.Repository != root {
				if path == root || id == scope.WorkspaceID {
					return pebblestore.ProjectTaskSource{}, fmt.Errorf("project workspace %q requires a committed repository: %v", root, err)
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
	return nil
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
	if session.WorktreeEnabled {
		if session.WorktreeRootPath == "" || session.WorkspacePath != session.WorktreeRootPath || session.Metadata["swarm_v3_worktree_owner_session_id"] != session.ID || session.Metadata["swarm_v3_runtime_workspace_path"] != session.WorktreeRootPath || task.WorkspacePath != session.WorktreeRootPath {
			return errors.New("task session worktree owner or runtime path does not match reservation")
		}
	} else if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" || task.PlanBinding != nil {
		return errors.New("coding or plan task session has no isolated worktree")
	}
	return nil
}

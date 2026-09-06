package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Recovery does not grant general filesystem scope. Only exact durable
// destinations of the authenticated parent can cross the managed-lane boundary.
func (r *Runtime) manageWorktreeRecoveryParent(scope WorkspaceScope) (pebblestore.SessionSnapshot, error) {
	if !scope.Principal.Valid() || strings.TrimSpace(scope.SessionID) == "" || (scope.Principal.SessionID != "" && scope.Principal.SessionID != scope.SessionID) {
		return pebblestore.SessionSnapshot{}, errors.New("manage-worktree recovery requires authenticated current parent context")
	}
	parent, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return parent, err
	}
	if !found || parent.ID != scope.SessionID || parent.AccountScopeID != scope.Principal.AccountScopeID || parent.UserID != scope.Principal.UserID {
		return parent, errors.New("manage-worktree recovery parent is not owned by the authenticated principal")
	}
	return parent, nil
}

func (r *Runtime) manageWorktreeRecoveryChild(parent pebblestore.SessionSnapshot, row map[string]any) (pebblestore.SessionSnapshot, error) {
	id := strings.TrimSpace(asString(row["child_session_id"]))
	child, found, err := r.sessions.GetSession(id)
	if err != nil {
		return child, err
	}
	if !found || id == "" || child.ID != id || child.AccountScopeID != parent.AccountScopeID || child.UserID != parent.UserID ||
		asString(child.Metadata["parent_session_id"]) != parent.ID || asString(child.Metadata["lineage_kind"]) != "delegated_subagent" || !agentruntime.IsCoderAgentName(asString(child.Metadata["subagent"])) {
		return child, fmt.Errorf("selected child %q is not an owned Coder child of this parent", id)
	}
	if path := strings.TrimSpace(firstNonEmptyString(asString(row["worktree_root_path"]), asString(row["workspace_path"]))); path != "" && filepath.Clean(path) != filepath.Clean(child.WorktreeRootPath) {
		return child, errors.New("child worktree path disagrees with durable session")
	}
	if branch := strings.TrimSpace(asString(row["worktree_branch"])); branch != "" && branch != child.WorktreeBranch {
		return child, errors.New("child worktree branch disagrees with durable session")
	}
	return child, nil
}

func (r *Runtime) manageWorktreeRecoveryDestination(scope WorkspaceScope, parent, child pebblestore.SessionSnapshot, row map[string]any) (string, error) {
	requested := strings.TrimSpace(firstNonEmptyString(asString(row["parent_workspace_path"]), asString(child.Metadata["target_workspace_path"]), parent.WorktreeRootPath))
	if target := strings.TrimSpace(asString(child.Metadata["target_workspace_path"])); target != "" && filepath.Clean(target) != filepath.Clean(requested) {
		return "", errors.New("child destination disagrees with durable session")
	}
	var lane pebblestore.TaskProgramRepositoryLane
	seed := parent.ID
	if parent.WorktreeEnabled && requested != "" && filepath.Clean(requested) == filepath.Clean(parent.WorktreeRootPath) {
		if runtimePath := strings.TrimSpace(asString(parent.Metadata["swarm_v3_runtime_workspace_path"])); runtimePath != "" && filepath.Clean(runtimePath) != filepath.Clean(requested) {
			return "", errors.New("parent lane disagrees with authenticated runtime workspace")
		}
		if parent.WorktreeBranch == parent.WorktreeBaseBranch {
			return "", errors.New("parent lane cannot use its captured base branch")
		}
		lane = pebblestore.TaskProgramRepositoryLane{SourcePath: asString(parent.Metadata["swarm_v3_source_workspace_path"]), WorkspacePath: parent.WorktreeRootPath, Branch: parent.WorktreeBranch, BaseCommit: asString(parent.Metadata["base_commit"])}
	} else {
		authority, ok := r.sessions.(interface {
			TaskProgramRepositoryLanes(string) ([]pebblestore.TaskProgramRepositoryLane, error)
		})
		if !ok {
			return "", errors.New("durable Task Program repository lane authority unavailable")
		}
		lanes, err := authority.TaskProgramRepositoryLanes(parent.ID)
		if err != nil {
			return "", err
		}
		for _, saved := range lanes {
			if requested != "" && filepath.Clean(saved.WorkspacePath) == filepath.Clean(requested) {
				if lane.WorkspacePath != "" && lane != saved {
					return "", errors.New("ambiguous durable Task Program repository lane")
				}
				lane = saved
			}
		}
		if lane.BaseCommit == "" {
			return "", errors.New("durable Task Program repository lane has no captured base")
		}
		if lane.WorkspacePath == "" {
			return "", errors.New("child destination is not an exact durable parent-owned repository lane")
		}
		digest := sha256.Sum256([]byte(parent.ID + "\x00" + lane.SourcePath))
		seed = "program-lane-" + hex.EncodeToString(digest[:12])
	}
	if !filepath.IsAbs(lane.SourcePath) || !filepath.IsAbs(lane.WorkspacePath) || lane.Branch == "" || filepath.Clean(lane.SourcePath) == filepath.Clean(lane.WorkspacePath) {
		return "", errors.New("recovery requires a distinct authenticated session-owned parent lane")
	}
	if branch := strings.TrimSpace(asString(row["parent_branch"])); branch != "" && branch != lane.Branch {
		return "", errors.New("child parent branch disagrees with durable destination")
	}
	// Authenticate the exact recorded source, which may be saved independently
	// of the parent's captured workspace. The flat account catalog revalidates
	// its canonical primary path; Directories and cached active roots are not
	// authority here, including after revocation. Never widen the caller scope.
	if r.workspace == nil {
		return "", errors.New("authorize recovery source: authenticated workspace authority unavailable")
	}
	saved, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, lane.SourcePath)
	if err != nil {
		return "", fmt.Errorf("authorize recovery source workspace: %w", err)
	}
	if !saved.Matched || saved.WorkspacePath != lane.SourcePath || saved.ResolvedPath != lane.SourcePath {
		return "", errors.New("authorize recovery source: recorded source is not an exact canonical account-owned workspace")
	}
	validator, ok := r.worktrees.(interface {
		ValidateTaskRepositoryLane(string, string, string, string) error
	})
	if !ok {
		return "", errors.New("task repository lane ownership validator unavailable")
	}
	if err := validator.ValidateTaskRepositoryLane(lane.SourcePath, lane.WorkspacePath, seed, lane.Branch); err != nil {
		return "", err
	}
	if lane.BaseCommit != "" {
		state, err := r.worktrees.InspectTaskWorkspace(lane.WorkspacePath)
		if err != nil {
			return "", err
		}
		descends, err := r.worktrees.TaskCommitDescendsFrom(lane.WorkspacePath, lane.BaseCommit, state.HeadCommit)
		if err != nil {
			return "", err
		}
		if !descends {
			return "", errors.New("recovery lane no longer descends from its captured base")
		}
	}
	return lane.WorkspacePath, nil
}

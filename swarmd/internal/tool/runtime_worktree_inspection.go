package tool

import (
	"errors"
	"path/filepath"
	"strings"
)

// Inspection separates saved configuration authority from the Git execution
// directory. This read-only resolver is not used by integration or promotion.
func (r *Runtime) manageWorktreeInspectionPaths(scope WorkspaceScope, requested string) (string, string, error) {
	if r == nil || r.sessions == nil {
		path, err := r.manageWorktreeResolveWorkspacePath(scope, requested)
		return path, path, err
	}
	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return "", "", err
	}
	if !parent.WorktreeEnabled {
		path, err := r.manageWorktreeResolveWorkspacePath(scope, requested)
		return path, path, err
	}
	source := strings.TrimSpace(asString(parent.Metadata["swarm_v3_source_workspace_path"]))
	lane := strings.TrimSpace(parent.WorktreeRootPath)
	if source == "" || lane == "" || filepath.Clean(scope.PrimaryPath) != filepath.Clean(lane) {
		return "", "", errors.New("inspection session repository identity is incomplete or stale")
	}
	if owner := asString(parent.Metadata["swarm_v3_worktree_owner_session_id"]); owner != "" && owner != parent.ID {
		return "", "", errors.New("inspection session lane owner mismatch")
	}
	if runtime := asString(parent.Metadata["swarm_v3_runtime_workspace_path"]); runtime != "" && filepath.Clean(runtime) != filepath.Clean(lane) {
		return "", "", errors.New("inspection runtime lane mismatch")
	}
	if r.workspace == nil {
		return "", "", errors.New("inspection saved workspace authority unavailable")
	}
	saved, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, source)
	if err != nil {
		return "", "", err
	}
	if !saved.Matched || filepath.Clean(saved.WorkspacePath) != filepath.Clean(source) {
		return "", "", errors.New("inspection source is not an exact account-owned saved workspace")
	}
	validator, ok := r.worktrees.(interface {
		ValidateSessionRepositoryLaneForRead(string, string, string, string) error
	})
	if !ok {
		return "", "", errors.New("inspection session lane validator unavailable")
	}
	if err := validator.ValidateSessionRepositoryLaneForRead(source, lane, parent.ID, parent.WorktreeBranch); err != nil {
		return "", "", err
	}
	if requested == "" {
		requested = lane
	}
	absolute, resolved, err := normalizeWorkspaceCandidatePath(scope.PrimaryPath, requested)
	if err != nil {
		return "", "", err
	}
	if filepath.Clean(absolute) != filepath.Clean(resolved) {
		return "", "", errors.New("inspection requires a canonical workspace path")
	}
	// The default lane and its source remain exact aliases. Explicit secondary
	// targets do not change that default or grant general filesystem access.
	if absolute == filepath.Clean(source) || absolute == filepath.Clean(lane) {
		return absolute, source, nil
	}
	for _, root := range scope.Roots {
		if absolute != filepath.Clean(root) {
			continue
		}
		if _, err := resolveWorkspacePath(scope, absolute); err != nil {
			return "", "", err
		}
		saved, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, absolute)
		if err != nil {
			return "", "", err
		}
		if !saved.Matched || saved.WorkspacePath != absolute || saved.ResolvedPath != absolute {
			return "", "", errors.New("inspection target is not an exact account-owned saved workspace")
		}
		return absolute, absolute, nil
	}
	// A previously allocated program lane is selectable only through its durable
	// owner record and independent Git validation, never a caller-supplied root.
	selected, err := r.selectedRepositoryLane(scope, parent, absolute, "")
	if err != nil {
		return "", "", err
	}
	return absolute, selected.SourcePath, nil
}

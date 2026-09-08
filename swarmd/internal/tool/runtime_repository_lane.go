package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// selectedRepositoryLane resolves an explicit repository selector against durable
// lanes owned by this session. A path is a selector, never a filesystem grant.
// Git status and promotion must use the same authenticated source identity.
func (r *Runtime) selectedRepositoryLane(scope WorkspaceScope, owner pebblestore.SessionSnapshot, requested, branch string) (pebblestore.TaskProgramRepositoryLane, error) {
	var selected pebblestore.TaskProgramRepositoryLane
	if !scope.Principal.Valid() || owner.ID == "" || owner.AccountScopeID != scope.Principal.AccountScopeID || owner.UserID != scope.Principal.UserID {
		return selected, errors.New("repository lane requires an owned session")
	}
	if requested == "" || !filepath.IsAbs(requested) {
		return selected, errors.New("repository lane requires an exact absolute workspace selector")
	}
	authority, ok := r.sessions.(interface {
		TaskProgramRepositoryLanes(string) ([]pebblestore.TaskProgramRepositoryLane, error)
	})
	if !ok {
		return selected, errors.New("durable repository lane authority unavailable")
	}
	lanes, err := authority.TaskProgramRepositoryLanes(owner.ID)
	if err != nil {
		return selected, err
	}
	for _, lane := range lanes {
		if filepath.Clean(requested) != filepath.Clean(lane.SourcePath) && filepath.Clean(requested) != filepath.Clean(lane.WorkspacePath) {
			continue
		}
		if branch != "" && lane.Branch != branch {
			continue
		}
		if selected.WorkspacePath != "" && selected != lane {
			return selected, errors.New("ambiguous durable repository lane")
		}
		selected = lane
	}
	if !filepath.IsAbs(selected.SourcePath) || !filepath.IsAbs(selected.WorkspacePath) || selected.SourcePath == selected.WorkspacePath || strings.TrimSpace(selected.Branch) == "" || strings.TrimSpace(selected.BaseCommit) == "" {
		return selected, errors.New("workspace selector does not identify a complete owned repository lane")
	}
	if r.workspace == nil || r.worktrees == nil {
		return selected, errors.New("repository lane authentication services unavailable")
	}
	saved, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, selected.SourcePath)
	if err != nil {
		return selected, err
	}
	if !saved.Matched || saved.WorkspacePath != selected.SourcePath || saved.ResolvedPath != selected.SourcePath {
		return selected, errors.New("repository lane source is not an exact canonical account-owned workspace")
	}
	validator, ok := r.worktrees.(interface {
		ValidateTaskRepositoryLane(string, string, string, string) error
	})
	if !ok {
		return selected, errors.New("repository lane ownership validator unavailable")
	}
	digest := sha256.Sum256([]byte(owner.ID + "\x00" + selected.SourcePath))
	seed := "program-lane-" + hex.EncodeToString(digest[:12])
	if err := validator.ValidateTaskRepositoryLane(selected.SourcePath, selected.WorkspacePath, seed, selected.Branch); err != nil {
		return selected, err
	}
	state, err := r.worktrees.InspectTaskWorkspace(selected.WorkspacePath)
	if err != nil {
		return selected, err
	}
	descends, err := r.worktrees.TaskCommitDescendsFrom(selected.WorkspacePath, selected.BaseCommit, state.HeadCommit)
	if err != nil {
		return selected, err
	}
	if !descends {
		return selected, errors.New("repository lane no longer descends from its captured base")
	}
	return selected, nil
}

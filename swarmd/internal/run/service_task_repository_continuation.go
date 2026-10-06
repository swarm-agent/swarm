package run

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Continuation selection comes only from the current durable task attempt. Every
// launch verifies all allocated bases, so partial materialization cannot execute.
func (s *Service) validateTaskRepositoryContinuationBases(parent pebblestore.SessionSnapshot, principal identity.Principal) error {
	if s.sessions == nil { return nil }
	refs, err := s.sessions.Store().TaskRepositoryContinuationsForSession(parent)
	if err != nil || len(refs) == 0 { return err }
	validator, ok := s.worktrees.(interface {
		ValidateSessionRepositoryLane(string, string, string, string) error
		TaskCommitDescendsFrom(string, string, string) (bool, error)
	})
	if !ok { return errors.New("task continuation lane authority unavailable") }
	for _, ref := range refs {
		canonical, err := s.canonicalSessionWorkspace(principal, ref.Source.WorkspaceID, ref.Source.WorkspaceGeneration)
		if err != nil { return err }
		if canonical.SourceWorkspacePath != ref.Source.Path { return errors.New("task continuation catalog source changed") }
		path, branch := "", ""
		if mapString(parent.Metadata, "swarm_v3_source_workspace_path") == ref.Source.Path {
			path, branch = parent.WorktreeRootPath, parent.WorktreeBranch
		} else {
			for _, item := range sessionWorktreeHistory(parent.Metadata["swarm_v3_worktree_history"]) {
				if mapString(item, "source_workspace_path") != ref.Source.Path { continue }
				if path != "" || mapString(item, "owner_session_id") != parent.ID || mapString(item, "workspace_id") != ref.Source.WorkspaceID || manageWorkspaceInt64(item["workspace_generation"]) != ref.Source.WorkspaceGeneration { return errors.New("task continuation lane identity mismatch") }
				path, branch = mapString(item, "path"), mapString(item, "branch")
			}
			digest := sha256.Sum256([]byte(parent.ID + "\x00" + ref.Source.Path))
			if branch != "agent/followup-repository-"+hex.EncodeToString(digest[:12]) { return errors.New("task continuation lane branch mismatch") }
		}
		if path == "" { return errors.New("task continuation allocation incomplete") }
		if err := validator.ValidateSessionRepositoryLane(ref.Source.Path, path, parent.ID, branch); err != nil { return err }
		state, err := s.worktrees.InspectTaskWorkspace(path)
		if err != nil || !state.Clean || state.BranchName != branch { return errors.New("task continuation lane dirty or changed") }
		contains, err := validator.TaskCommitDescendsFrom(path, ref.HeadCommit, state.HeadCommit)
		if err != nil || !contains { return errors.New("task continuation lane lost retained result") }
	}
	return nil
}

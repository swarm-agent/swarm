package app

import (
	"context"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

// Saved workspace Git state belongs to the daemon's authenticated workspace
// authority, not the invoking terminal user's filesystem identity.
func (a *App) workspaceGitStatus(ctx context.Context, path string) (gitRepoStatus, error) {
	failed := gitRepoStatus{Branch: "-", Readiness: model.GitReadinessCheckFailed}
	if a == nil || a.api == nil || normalizePath(path) == "" {
		return failed, fmt.Errorf("workspace Git status requires a client and path")
	}
	snapshot, err := a.api.GetGitStatus(ctx, path, 1)
	if err != nil {
		return failed, err
	}
	if !snapshot.HasGit {
		return gitRepoStatus{Branch: "-", Readiness: model.GitReadinessNotRepository}, nil
	}
	if !pathsEqual(normalizePath(snapshot.WorkspacePath), normalizePath(path)) || !pathsEqual(normalizePath(snapshot.RepoRoot), normalizePath(path)) {
		return failed, fmt.Errorf("workspace Git status resolved a different repository")
	}
	readiness := model.GitReadinessReady
	if strings.TrimSpace(snapshot.HeadOID) == "" {
		readiness = model.GitReadinessNeedsCommit
	}
	return gitRepoStatus{
		HasGit: true, RepoRoot: snapshot.RepoRoot, Branch: snapshot.Branch,
		Readiness: readiness, Upstream: snapshot.Upstream,
		AheadCount: snapshot.AheadCount, BehindCount: snapshot.BehindCount,
		DirtyCount: snapshot.DirtyCount, StagedCount: snapshot.StagedCount,
		ModifiedCount: snapshot.ModifiedCount, UntrackedCount: snapshot.UntrackedCount,
		ConflictCount: snapshot.ConflictCount,
	}, nil
}

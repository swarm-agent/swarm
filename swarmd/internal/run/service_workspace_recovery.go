package run

import (
	"errors"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type sessionRecoveryCandidate struct {
	Path string `json:"path"`
	OwnerSessionID string `json:"owner_session_id"`
	OwnershipRevision uint64 `json:"ownership_revision"`
	HEAD string `json:"head"`
	Fingerprint string `json:"fingerprint"`
}

// authorizedRecoveryCandidates deliberately separates registration from content
// inspection. Missing and foreign claims are indistinguishable and never opened.
func authorizedRecoveryCandidates(paths []string, authorize func(string) ([]pebblestore.WorktreeOwnership, error), inspect func(string) (worktreeruntime.RecoveryIdentity, error)) ([]sessionRecoveryCandidate, error) {
	if len(paths) > 100 {
		return nil, errors.New("recovery inventory exceeds 100 worktrees")
	}
	out := make([]sessionRecoveryCandidate, 0, len(paths))
	for _, path := range paths {
		claims, err := authorize(path)
		if errors.Is(err, pebblestore.ErrWorktreeRecoveryConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(claims) != 1 || claims[0].Path != path {
			return nil, pebblestore.ErrWorktreeRecoveryConflict
		}
		id, err := inspect(path)
		if err != nil {
			return nil, err
		}
		out = append(out, sessionRecoveryCandidate{Path: path, OwnerSessionID: claims[0].OwnerSessionID, OwnershipRevision: claims[0].Revision, HEAD: id.HEAD, Fingerprint: id.Fingerprint})
	}
	return out, nil
}

func (s *Service) discoverSessionRecoveryWorktrees(principal identity.Principal, args manageWorkspaceArguments) (string, error) {
	if s.sessionWorkspaceCanonicalize == nil {
		return "", errors.New("workspace canonicalizer unavailable")
	}
	if args.WorkspaceGeneration <= 0 || args.WorkspaceID == "" {
		return "", errors.New("discover_worktrees requires exact workspace_id and workspace_generation")
	}
	if args.WorkspaceIDs != nil || args.PrimaryWorkspaceID != "" || args.WorktreePath != "" || args.WorktreeName != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet || args.ContentSet || args.ExpectedRevision != 0 || args.Intent != "" || args.PermissionScope != "" {
		return "", errors.New("discover_worktrees accepts only workspace_id and workspace_generation")
	}
	canonical, err := s.canonicalSessionWorkspace(principal, args.WorkspaceID, args.WorkspaceGeneration)
	if err != nil {
		return "", err
	}
	entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, args.WorkspaceID)
	if err != nil {
		return "", err
	}
	if !found || entry.State != "active" || entry.Path != canonical.SourceWorkspacePath || entry.WorkspaceGeneration != canonical.WorkspaceGeneration {
		return "", errors.New("workspace identity unauthorized or stale")
	}
	paths, err := worktreeruntime.DiscoverRecoveryPaths(canonical.SourceWorkspacePath)
	if err != nil {
		return "", err
	}
	candidates, err := authorizedRecoveryCandidates(paths, func(path string) ([]pebblestore.WorktreeOwnership, error) {
		return s.sessions.Store().InspectWorktreeOwnership(principal.AccountScopeID, principal.UserID, []string{path})
	}, func(path string) (worktreeruntime.RecoveryIdentity, error) {
		return worktreeruntime.InspectRecoveryWorktree(canonical.SourceWorkspacePath, path)
	})
	if err != nil {
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": "discover_worktrees", "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration, "candidates": candidates, "unknown_provenance": "excluded; not authorized for recovery"})
}

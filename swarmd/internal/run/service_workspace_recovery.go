package run

import (
	"errors"
	"path/filepath"
	"sort"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type sessionRecoveryCandidate struct {
	Diagnostic             string `json:"diagnostic,omitempty"`
	Path                   string `json:"path"`
	OwnerSessionID         string `json:"owner_session_id"`
	OwnershipRevision      uint64 `json:"ownership_revision"`
	HEAD                   string `json:"head"`
	Fingerprint            string `json:"fingerprint"`
	OperationID            string `json:"operation_id,omitempty"`
	OperationState         string `json:"operation_state,omitempty"`
	ClaimantSessionID      string `json:"claimant_session_id,omitempty"`
	ReservationFingerprint string `json:"reservation_fingerprint,omitempty"`
	RetainedDestination    string `json:"retained_destination,omitempty"`
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
		claim := claims[0]
		if claim.ClaimantSessionID != "" || claim.OperationState == "retained_copy" {
			// Repair must remain discoverable even if a crashed source no longer
			// has a readable index. This is ownership metadata, not fresh Git evidence.
			out = append(out, sessionRecoveryCandidate{Path: path, OwnerSessionID: claim.OwnerSessionID, OwnershipRevision: claim.Revision, OperationID: claim.OperationID, OperationState: claim.OperationState, ClaimantSessionID: claim.ClaimantSessionID, ReservationFingerprint: claim.Evidence, RetainedDestination: claim.DestinationPath})
			continue
		}
		id, err := inspect(path)
		if err != nil {
			out = append(out, sessionRecoveryCandidate{Path: path, OwnerSessionID: claim.OwnerSessionID, OwnershipRevision: claim.Revision, Diagnostic: "inspection failed; no fresh recovery identity; retry exact worktree_path after repairing the source"})
			continue
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
	if args.WorkspaceIDs != nil || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet || args.ContentSet || args.ExpectedRevision != 0 || args.Intent != "" || args.PermissionScope != "" {
		return "", errors.New("discover_worktrees accepts only workspace_id, workspace_generation and optional exact worktree_path")
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
	paths, truncated, err := selectRecoveryPaths(paths, args.WorktreePath)
	if err != nil {
		return "", err
	}
	candidates, err := authorizedRecoveryCandidates(paths, func(path string) ([]pebblestore.WorktreeOwnership, error) {
		return s.sessions.Store().InspectRecoveryOwnership(principal.AccountScopeID, principal.UserID, canonical.SourceWorkspacePath, path)
	}, func(path string) (worktreeruntime.RecoveryIdentity, error) {
		return worktreeruntime.InspectRecoveryWorktree(canonical.SourceWorkspacePath, path)
	})
	if err != nil {
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": "discover_worktrees", "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration, "candidates": candidates, "truncated": truncated, "continuation": "For a known source, repeat discover_worktrees with the same workspace identity and exact worktree_path; truncated inventories are not exhaustive. Candidates with diagnostic have no fresh HEAD/fingerprint and cannot authorize recovery.", "unknown_provenance": "excluded; not authorized for recovery"})
}

// selectRecoveryPaths bounds ownership/content inspection independently of the
// byte-bounded Git registration list. Exact selection is not authorization.
func selectRecoveryPaths(paths []string, exact string) ([]string, bool, error) {
	if exact != "" {
		if !filepath.IsAbs(exact) || filepath.Clean(exact) != exact {
			return nil, false, errors.New("discovery requires a clean absolute worktree_path")
		}
		for _, path := range paths {
			if path == exact {
				return []string{path}, false, nil
			}
		}
		return []string{}, false, nil
	}
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	truncated := len(paths) > 100
	if truncated {
		paths = paths[:100]
	}
	return paths, truncated, nil
}

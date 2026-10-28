package run

import (
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	// Bounds for proving descendant worktree ownership. Paths deeper than this
	// or lineage chains longer than this fail closed to the normal prompt.
	descendantWorktreeMaxPathDepth    = 64
	descendantWorktreeMaxLineageDepth = 32
	// Each scope-checked call may carry several paths; each proven descendant
	// root covers at least one of them.
	descendantWorktreeMaxRootsPerCall = 16
)

// descendantWorktreeLineage is the durable authority that proves a worktree
// belongs to a delegated descendant of the reading session: the worktree
// ownership inventory and the session lineage recorded at launch.
type descendantWorktreeLineage interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	InspectWorktreeOwnership(account, user string, paths []string) ([]pebblestore.WorktreeOwnership, error)
}

func (s *Service) descendantWorktreeLineage() descendantWorktreeLineage {
	if s == nil || s.sessions == nil {
		return nil
	}
	store := s.sessions.Store()
	if store == nil {
		return nil
	}
	return store
}

// resolveDescendantReadRoots admits a read-only call whose out-of-scope paths
// all lie in worktrees of the session's own descendant tasks. It returns the
// proven roots and the scope extended with them, or ok=false when any path
// still needs the normal workspace-scope decision.
func resolveDescendantReadRoots(lineage descendantWorktreeLineage, principal identity.Principal, sessionID string, scope tool.WorkspaceScope, call tool.Call, request tool.ScopeExpansionRequest) ([]string, tool.WorkspaceScope, bool) {
	if lineage == nil || !tool.ScopeCallReadsOnly(call.Name) {
		return nil, scope, false
	}
	added := []string(nil)
	for i := 0; i < descendantWorktreeMaxRootsPerCall; i++ {
		root, ok := resolveDescendantWorktreeReadRoot(lineage, principal, sessionID, request.DirectoryPath, filepath.Dir(request.TargetPath))
		if !ok {
			return nil, scope, false
		}
		added = append(added, root)
		scope.DescendantReadRoots = append(append([]string(nil), scope.DescendantReadRoots...), root)
		next, needsApproval, err := tool.ScopeExpansionForCall(scope, call)
		if err != nil {
			return nil, scope, false
		}
		if !needsApproval {
			return added, scope, true
		}
		request = next
	}
	return nil, scope, false
}

// resolveDescendantWorktreeReadRoot walks up from each candidate directory to
// a recorded worktree root, then proves through durable lineage that the
// root's owner descends from sessionID. Any lookup failure, foreign owner,
// reserved claim, cycle or exceeded bound returns ok=false.
func resolveDescendantWorktreeReadRoot(lineage descendantWorktreeLineage, principal identity.Principal, sessionID string, candidates ...string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	account := strings.TrimSpace(principal.AccountScopeID)
	user := strings.TrimSpace(principal.UserID)
	if lineage == nil || sessionID == "" || account == "" || user == "" {
		return "", false
	}
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		dir := filepath.Clean(candidate)
		for depth := 0; depth < descendantWorktreeMaxPathDepth; depth++ {
			if _, checked := seen[dir]; !checked {
				seen[dir] = struct{}{}
				if descendantWorktreeOwnedBy(lineage, account, user, sessionID, dir) {
					return dir, true
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", false
}

func descendantWorktreeOwnedBy(lineage descendantWorktreeLineage, account, user, sessionID, root string) bool {
	claims, err := lineage.InspectWorktreeOwnership(account, user, []string{root})
	if err != nil || len(claims) != 1 {
		return false
	}
	claim := claims[0]
	owner := strings.TrimSpace(claim.OwnerSessionID)
	if claim.Path != root || owner == "" || owner == sessionID || claim.ClaimantSessionID != "" ||
		claim.AccountScopeID != account || claim.UserID != user {
		return false
	}
	session, ok, err := lineage.GetSession(owner)
	if err != nil || !ok || !descendantWorktreeSameOwner(session, account, user) ||
		!session.WorktreeEnabled || filepath.Clean(strings.TrimSpace(session.WorktreeRootPath)) != root {
		return false
	}
	visited := map[string]struct{}{owner: {}}
	cursor := mapString(session.Metadata, "parent_session_id")
	for depth := 0; depth < descendantWorktreeMaxLineageDepth && cursor != ""; depth++ {
		if cursor == sessionID {
			return true
		}
		if _, cycle := visited[cursor]; cycle {
			return false
		}
		visited[cursor] = struct{}{}
		ancestor, ok, err := lineage.GetSession(cursor)
		if err != nil || !ok || !descendantWorktreeSameOwner(ancestor, account, user) {
			return false
		}
		cursor = mapString(ancestor.Metadata, "parent_session_id")
	}
	return false
}

func descendantWorktreeSameOwner(session pebblestore.SessionSnapshot, account, user string) bool {
	return strings.TrimSpace(session.AccountScopeID) == account && strings.TrimSpace(session.UserID) == user
}

package run

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func resolveRunWorkspaceContext(execCtx resolvedRunExecutionContext) runWorkspaceContext {
	scope := execCtx.Scope
	originRoots := normalizeExecutionRoots(execCtx.WorkspacePath, scope.Roots)
	return runWorkspaceContext{
		WorkspacePath:        scope.PrimaryPath,
		WorkspaceRoots:       append([]string(nil), scope.Roots...),
		OriginWorkspacePath:  execCtx.WorkspacePath,
		OriginWorkspaceRoots: append([]string(nil), originRoots...),
		Scope:                scope,
	}
}

func (s *Service) ResolveRuntimeWorkspaceScope(session pebblestore.SessionSnapshot, principal identity.Principal) (tool.WorkspaceScope, error) {
	return s.resolveRunWorkspaceScope(session, principal)
}

func (s *Service) resolveRunWorkspaceScope(session pebblestore.SessionSnapshot, principal identity.Principal) (tool.WorkspaceScope, error) {
	workspacePath := strings.TrimSpace(session.WorkspacePath)
	principal, err := principalForRunWorkspaceScope(session, principal)
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	if session.WorktreeEnabled {
		resolvedPath, err := normalizeRunScopePath(firstNonEmptyString(session.WorktreeRootPath, workspacePath))
		if err != nil {
			return tool.WorkspaceScope{}, err
		}
		if s != nil && s.workspace != nil {
			sourceWorkspacePath := strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_path"))
			if sourceWorkspacePath == "" {
				return tool.WorkspaceScope{}, errors.New("worktree-backed session is missing canonical source workspace path")
			}
			resolvedSource, sourceErr := s.workspace.ScopeForPathForPrincipal(principal, sourceWorkspacePath)
			if sourceErr != nil {
				return tool.WorkspaceScope{}, sourceErr
			}
			if !resolvedSource.Matched {
				return tool.WorkspaceScope{}, errors.New("worktree-backed session source workspace is no longer authorized")
			}
			if identityErr := validateSessionWorkspaceIdentity(session, resolvedSource.WorkspaceID, resolvedSource.WorkspaceGeneration); identityErr != nil {
				return tool.WorkspaceScope{}, identityErr
			}
		}
		roots := make([]string, 0, 2+len(session.TemporaryWorkspaceRoots))
		roots = append(roots, resolvedPath)
		if rootPath := strings.TrimSpace(session.WorktreeRootPath); rootPath != "" {
			resolvedRootPath, rootErr := normalizeRunScopePath(rootPath)
			if rootErr != nil {
				return tool.WorkspaceScope{}, rootErr
			}
			roots = append(roots, resolvedRootPath)
		}
		roots, err = mergeValidatedTemporaryWorkspaceRoots(roots, session.TemporaryWorkspaceRoots)
		if err != nil {
			return tool.WorkspaceScope{}, err
		}
		roots, err = s.mergeAuthorizedSessionWorkspaceGrantRoots(principal, roots, session.WorkspaceGrants)
		if err != nil {
			return tool.WorkspaceScope{}, err
		}
		mutationScopes := []string(nil)
		readOnlyRoots := []string(nil)
		sourceWorkspacePath := strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_path"))
		if agentruntime.IsCoderAgentName(mapString(session.Metadata, "requested_subagent")) {
			mutationScopes = mapStringSlice(session.Metadata, "owned_scope")
			if gitAdminRoot, gitErr := linkedWorktreeGitAdminRoot(resolvedPath); gitErr != nil {
				return tool.WorkspaceScope{}, gitErr
			} else if gitAdminRoot != "" {
				readOnlyRoots = append(readOnlyRoots, gitAdminRoot)
			}
			if sourceWorkspacePath != "" && sourceWorkspacePath != resolvedPath {
				normalizedSource, sourceErr := normalizeRunScopePath(sourceWorkspacePath)
				if sourceErr != nil {
					return tool.WorkspaceScope{}, fmt.Errorf("resolve Coder source workspace: %w", sourceErr)
				}
				sourceWorkspacePath = normalizedSource
				readOnlyRoots = append(readOnlyRoots, normalizedSource)
				if s != nil && s.workspace != nil {
					// A delegated Coder writes only in its isolated worktree, but it may
					// need to inspect another directory already linked to the source
					// workspace (for example, a sibling product repository used as
					// source authority). Re-authenticate the account-scoped saved
					// workspace at run time and expose its linked directories as
					// read-only roots. Do not inherit session-only temporary roots.
					linkedScope, linkedErr := s.workspace.ScopeForPathForPrincipal(principal, normalizedSource)
					if linkedErr != nil {
						return tool.WorkspaceScope{}, fmt.Errorf("resolve Coder linked read-only roots: %w", linkedErr)
					}
					if linkedScope.Matched {
						readOnlyRoots = mergeSessionWorkspaceRoots(readOnlyRoots, linkedScope.Directories)
					}
				}
			}
			pooledReadOnlyRoots, pooledErr := s.mergeAccountSavedWorkspaceRoots(principal, nil)
			if pooledErr != nil {
				return tool.WorkspaceScope{}, fmt.Errorf("resolve Coder account workspace pool: %w", pooledErr)
			}
			readOnlyRoots = mergeSessionWorkspaceRoots(readOnlyRoots, pooledReadOnlyRoots)
		}
		return tool.WorkspaceScope{
			PrimaryPath:          resolvedPath,
			Roots:                roots,
			ReadOnlyRoots:        readOnlyRoots,
			MutationScopes:       mutationScopes,
			RejectScopeExpansion: agentruntime.IsCoderAgentName(mapString(session.Metadata, "requested_subagent")),
			Principal:            principal,
			SessionID:            strings.TrimSpace(session.ID),
			WorktreeEnabled:      true,
			WorktreeRootPath:     resolvedPath,
			WorktreeBranch:       strings.TrimSpace(session.WorktreeBranch),
			WorktreeBaseBranch:   strings.TrimSpace(session.WorktreeBaseBranch),
			WorktreeBaseCommit:   strings.TrimSpace(mapString(session.Metadata, "base_commit")),
			SourceWorkspacePath:  sourceWorkspacePath,
		}, nil
	}
	if s != nil && s.workspace != nil {
		resolved, err := s.workspace.ScopeForPathForPrincipal(principal, workspacePath)
		if err != nil {
			return tool.WorkspaceScope{}, fmt.Errorf("resolve account-scoped workspace scope: %w", err)
		}
		if resolved.Matched && strings.TrimSpace(resolved.WorkspacePath) != "" {
			if err := validateSessionWorkspaceIdentity(session, resolved.WorkspaceID, resolved.WorkspaceGeneration); err != nil {
				return tool.WorkspaceScope{}, err
			}
			roots, err := s.mergeAccountSavedWorkspaceRoots(principal, resolved.Directories)
			if err != nil {
				return tool.WorkspaceScope{}, err
			}
			roots, err = mergeValidatedTemporaryWorkspaceRoots(roots, session.TemporaryWorkspaceRoots)
			if err != nil {
				return tool.WorkspaceScope{}, err
			}
			roots, err = s.mergeAuthorizedSessionWorkspaceGrantRoots(principal, roots, session.WorkspaceGrants)
			if err != nil {
				return tool.WorkspaceScope{}, err
			}
			return tool.WorkspaceScope{
				PrimaryPath: strings.TrimSpace(resolved.WorkspacePath),
				Roots:       roots,
				Principal:   principal,
				SessionID:   strings.TrimSpace(session.ID),
			}, nil
		}
		if strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_id")) != "" {
			return tool.WorkspaceScope{}, errors.New("session canonical workspace is no longer authorized for this account")
		}
	}
	resolvedPath, err := normalizeRunScopePath(workspacePath)
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	roots, err := s.mergeAccountSavedWorkspaceRoots(principal, []string{resolvedPath})
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	roots, err = mergeValidatedTemporaryWorkspaceRoots(roots, session.TemporaryWorkspaceRoots)
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	roots, err = s.mergeAuthorizedSessionWorkspaceGrantRoots(principal, roots, session.WorkspaceGrants)
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	return tool.WorkspaceScope{
		PrimaryPath: resolvedPath,
		Roots:       roots,
		Principal:   principal,
		SessionID:   strings.TrimSpace(session.ID),
	}, nil
}

func (s *Service) mergeAccountSavedWorkspaceRoots(principal identity.Principal, baseRoots []string) ([]string, error) {
	if s == nil || s.workspace == nil {
		return mergeSessionWorkspaceRoots(baseRoots, nil), nil
	}
	pooledRoots, err := s.workspace.AvailableSavedRootsForPrincipal(principal)
	if err != nil {
		return nil, fmt.Errorf("list account-scoped workspace roots: %w", err)
	}
	roots := append([]string(nil), baseRoots...)
	roots = append(roots, pooledRoots...)
	return mergeSessionWorkspaceRoots(roots, nil), nil
}

func linkedWorktreeGitAdminRoot(worktreePath string) (string, error) {
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return "", nil
	}
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--path-format=absolute", "--git-dir")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve linked worktree Git admin path: %s", strings.TrimSpace(string(output)))
	}
	gitDir := filepath.Clean(strings.TrimSpace(string(output)))
	if gitDir == "" || !filepath.IsAbs(gitDir) {
		return "", fmt.Errorf("resolve linked worktree Git admin path: Git returned %q", strings.TrimSpace(string(output)))
	}
	worktreeRoot := filepath.Clean(worktreePath)
	if rel, relErr := filepath.Rel(worktreeRoot, gitDir); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", nil
	}
	info, err := os.Stat(filepath.Join(gitDir, "gitdir"))
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("resolve linked worktree Git admin path: canonical gitdir marker is missing")
	}
	marker, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return "", fmt.Errorf("resolve linked worktree Git admin path marker: %w", err)
	}
	markerPath := strings.TrimSpace(string(marker))
	if !filepath.IsAbs(markerPath) {
		markerPath = filepath.Join(gitDir, markerPath)
	}
	markerPath = filepath.Clean(markerPath)
	wantMarker := filepath.Join(worktreeRoot, ".git")
	if markerPath != wantMarker {
		return "", fmt.Errorf("resolve linked worktree Git admin path: marker %q does not identify %q", markerPath, wantMarker)
	}
	return gitDir, nil
}

func (s *Service) mergeAuthorizedSessionWorkspaceGrantRoots(principal identity.Principal, base []string, grants []pebblestore.WorkspaceGrant) ([]string, error) {
	roots := append([]string(nil), base...)
	if s == nil || s.workspace == nil {
		return roots, nil
	}
	for _, grant := range grants {
		workspaceID := strings.TrimSpace(grant.WorkspaceID)
		if workspaceID == "" || grant.Kind == pebblestore.WorkspaceGrantWorktree {
			continue
		}
		entry, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, workspaceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("session workspace grant %q is no longer authorized for this account", workspaceID)
		}
		if !strings.EqualFold(strings.TrimSpace(entry.State), "active") {
			return nil, fmt.Errorf("session workspace grant %q is not active", workspaceID)
		}
		if grant.WorkspaceGeneration > 0 && grant.WorkspaceGeneration != entry.WorkspaceGeneration {
			return nil, fmt.Errorf("session workspace grant %q generation is stale: captured %d, current %d", workspaceID, grant.WorkspaceGeneration, entry.WorkspaceGeneration)
		}
		if grantPath := strings.TrimSpace(grant.Path); grantPath != "" && grantPath != strings.TrimSpace(entry.Path) {
			return nil, fmt.Errorf("session workspace grant %q path is stale", workspaceID)
		}
		path, err := normalizeRunScopePath(entry.Path)
		if err != nil {
			return nil, err
		}
		roots = append(roots, path)
	}
	return mergeSessionWorkspaceRoots(nil, roots), nil
}

func validateSessionWorkspaceIdentity(session pebblestore.SessionSnapshot, workspaceID string, workspaceGeneration int64) error {
	if session.Metadata == nil {
		return nil
	}
	capturedID := strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_id"))
	if capturedID != "" && capturedID != strings.TrimSpace(workspaceID) {
		return fmt.Errorf("session workspace identity is stale: captured id %q, current id %q", capturedID, strings.TrimSpace(workspaceID))
	}
	capturedGeneration := strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_generation"))
	if capturedGeneration != "" && capturedGeneration != fmt.Sprintf("%d", workspaceGeneration) {
		return fmt.Errorf("session workspace generation is stale: captured generation %q, current generation %d", capturedGeneration, workspaceGeneration)
	}
	return nil
}

func principalForRunWorkspaceScope(session pebblestore.SessionSnapshot, principal identity.Principal) (identity.Principal, error) {
	if principal.Valid() {
		if strings.TrimSpace(principal.SessionID) == "" {
			principal.SessionID = strings.TrimSpace(session.ID)
		}
		return principal, nil
	}
	userID := strings.TrimSpace(session.UserID)
	accountScopeID := strings.TrimSpace(session.AccountScopeID)
	if userID == "" || accountScopeID == "" {
		return identity.Principal{}, fmt.Errorf("run workspace scope requires principal: %w", identity.ErrPrincipalRequired)
	}
	return identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             userID,
		AccountScopeID:     accountScopeID,
		SessionID:          strings.TrimSpace(session.ID),
		AccountScopeSource: identity.AccountScopeSourceSession,
	}, nil
}

func appendHostRuntimeContext(base string, workspacePath string, workspaceRoots []string) string {
	base = strings.TrimSpace(base)
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		workspacePath = "."
	}
	workspaceRoots = append([]string(nil), workspaceRoots...)
	if len(workspaceRoots) == 0 {
		workspaceRoots = []string{workspacePath}
	}

	var block strings.Builder
	block.WriteString("Workspace runtime policy:\n")
	block.WriteString("- Tools run directly on the host workspace path: ")
	block.WriteString(workspacePath)
	block.WriteString("\n")
	for _, line := range workspaceGitContext(workspacePath) {
		block.WriteString("- ")
		block.WriteString(line)
		block.WriteString("\n")
	}
	if len(workspaceRoots) == 1 {
		block.WriteString("- Allowed workspace root: ")
		block.WriteString(strings.TrimSpace(workspaceRoots[0]))
		block.WriteString("\n")
	} else {
		block.WriteString("- Allowed workspace roots:\n")
		for _, root := range workspaceRoots {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			block.WriteString("  - ")
			block.WriteString(root)
			block.WriteString("\n")
		}
	}

	if base == "" {
		return block.String()
	}
	return base + "\n\n" + block.String()
}

func workspaceGitContext(workspacePath string) []string {
	if _, err := exec.LookPath("git"); err != nil {
		return []string{
			"workspace_git_state: unavailable",
			"Git is not installed or not on PATH. Ordinary workspace reads and edits remain available.",
			"Before a requested Git-managed operation, explain that installation changes the machine, request the required permission, install Git with the detected Linux distribution's package manager, verify `git --version`, and then retry the original operation. Never claim the Git operation completed if installation is denied or fails.",
		}
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		workspacePath = "."
	}
	if err := exec.Command("git", "-C", workspacePath, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return []string{
			"workspace_git_state: not_repository",
			"This is a normal usable workspace, but it is not a Git repository. Do not run Git-managed operations unless the user requests one; then explain and obtain permission before initializing a repository.",
		}
	}
	if err := exec.Command("git", "-C", workspacePath, "rev-parse", "--verify", "HEAD").Run(); err != nil {
		return []string{
			"workspace_git_state: needs_initial_commit",
			"This Git repository has no commits. Ordinary workspace work remains available, but managed worktrees and commit-relative operations require an initial commit. Do not create one without the user's explicit request and the normal Git permission path.",
		}
	}
	return []string{"workspace_git_state: ready"}
}

func appendWorktreeRuntimeContext(base string, scope tool.WorkspaceScope) string {
	if !scope.WorktreeEnabled {
		return strings.TrimSpace(base)
	}
	worktreePath := strings.TrimSpace(scope.WorktreeRootPath)
	if worktreePath == "" {
		worktreePath = strings.TrimSpace(scope.PrimaryPath)
	}
	lines := []string{
		"Managed worktree context (authoritative for this run):",
		"- This session is operating inside an isolated Git worktree.",
		"- active_worktree: " + worktreePath,
		"- Treat active_worktree as the project root and default path for list, search, read, find, and all other workspace tools.",
		"- Do not substitute the source workspace or another checkout when inspecting or planning changes.",
	}
	if branch := strings.TrimSpace(scope.WorktreeBranch); branch != "" {
		lines = append(lines, "- worktree_branch: "+branch)
	}
	if source := strings.TrimSpace(scope.SourceWorkspacePath); source != "" && source != worktreePath {
		lines = append(lines, "- source_workspace: "+source+" (reference identity only; the active worktree remains the project root)")
	}
	block := strings.Join(lines, "\n")
	base = strings.TrimSpace(base)
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

func mergeValidatedTemporaryWorkspaceRoots(baseRoots, temporaryRoots []string) ([]string, error) {
	validated := make([]string, 0, len(temporaryRoots))
	for _, raw := range temporaryRoots {
		root, err := validateTemporaryWorkspaceRoot(raw)
		if err != nil {
			return nil, err
		}
		validated = append(validated, root)
	}
	return mergeSessionWorkspaceRoots(baseRoots, validated), nil
}

func validateTemporaryWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("temporary workspace root must be an absolute canonical path: %q", root)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("stat temporary workspace root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("temporary workspace root must not be a symlink: %s", root)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("temporary workspace root must be a directory: %s", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve temporary workspace root %q: %w", root, err)
	}
	if resolved != root {
		return "", fmt.Errorf("temporary workspace root is no longer canonical: %s", root)
	}
	return root, nil
}

func mergeSessionWorkspaceRoots(baseRoots, temporaryRoots []string) []string {
	combined := make([]string, 0, len(baseRoots)+len(temporaryRoots))
	seen := make(map[string]struct{}, len(baseRoots)+len(temporaryRoots))
	for _, raw := range baseRoots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		combined = append(combined, root)
	}
	for _, raw := range temporaryRoots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		combined = append(combined, root)
	}
	if len(combined) == 0 {
		return nil
	}
	return combined
}

func normalizeRunScopePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat workspace path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path must be a directory: %s", abs)
	}
	return abs, nil
}

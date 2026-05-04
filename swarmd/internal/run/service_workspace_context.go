package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	}
}

func (s *Service) resolveRunWorkspaceScope(session pebblestore.SessionSnapshot) (tool.WorkspaceScope, error) {
	workspacePath := strings.TrimSpace(session.WorkspacePath)
	if session.WorktreeEnabled {
		resolvedPath, err := normalizeRunScopePath(workspacePath)
		if err != nil {
			return tool.WorkspaceScope{}, err
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
		roots = mergeSessionWorkspaceRoots(roots, session.TemporaryWorkspaceRoots)
		return tool.WorkspaceScope{
			PrimaryPath: resolvedPath,
			Roots:       roots,
		}, nil
	}
	scope := tool.WorkspaceScope{}
	if s != nil && s.workspace != nil {
		resolved, err := s.workspace.ScopeForPath(workspacePath)
		if err == nil {
			scope = tool.WorkspaceScope{
				PrimaryPath: resolved.WorkspacePath,
				Roots:       append([]string(nil), resolved.Directories...),
			}
		}
	}
	if strings.TrimSpace(scope.PrimaryPath) != "" {
		scope.Roots = mergeSessionWorkspaceRoots(scope.Roots, session.TemporaryWorkspaceRoots)
		return scope, nil
	}
	resolvedPath, err := normalizeRunScopePath(workspacePath)
	if err != nil {
		return tool.WorkspaceScope{}, err
	}
	roots := mergeSessionWorkspaceRoots([]string{resolvedPath}, session.TemporaryWorkspaceRoots)
	return tool.WorkspaceScope{
		PrimaryPath: resolvedPath,
		Roots:       roots,
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

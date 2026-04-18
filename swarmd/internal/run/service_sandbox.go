package run

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) resolveRunSandboxContext(execCtx resolvedRunExecutionContext, runID string) (runSandboxContext, func(), error) {
	scope := execCtx.Scope
	originRoots := normalizeExecutionRoots(execCtx.WorkspacePath, scope.Roots)
	base := runSandboxContext{
		Enabled:              false,
		WorkspacePath:        scope.PrimaryPath,
		WorkspaceRoots:       append([]string(nil), scope.Roots...),
		OriginWorkspacePath:  execCtx.WorkspacePath,
		OriginWorkspaceRoots: append([]string(nil), originRoots...),
	}
	if s == nil || s.sandbox == nil {
		return base, func() {}, nil
	}

	status, err := s.sandbox.GetStatus()
	if err != nil {
		return runSandboxContext{}, func() {}, fmt.Errorf("read sandbox status: %w", err)
	}
	if !status.Enabled {
		return base, func() {}, nil
	}
	if !status.Ready {
		summary := strings.TrimSpace(status.Summary)
		if summary == "" {
			summary = "sandbox prerequisites are not ready"
		}
		remediation := strings.TrimSpace(strings.Join(status.Remediation, " | "))
		if remediation != "" {
			return runSandboxContext{}, func() {}, fmt.Errorf("sandbox is ON but unavailable: %s. remediation: %s", summary, remediation)
		}
		return runSandboxContext{}, func() {}, fmt.Errorf("sandbox is ON but unavailable: %s", summary)
	}

	sandboxScope, cleanup, err := prepareSandboxWorkspace(scope, runID)
	if err != nil {
		return runSandboxContext{}, func() {}, fmt.Errorf("prepare sandbox workspace: %w", err)
	}
	return runSandboxContext{
		Enabled:              true,
		WorkspacePath:        sandboxScope.PrimaryPath,
		WorkspaceRoots:       append([]string(nil), sandboxScope.Roots...),
		OriginWorkspacePath:  execCtx.WorkspacePath,
		OriginWorkspaceRoots: append([]string(nil), originRoots...),
	}, cleanup, nil
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

func appendSandboxRuntimeContext(base string, enabled bool, workspacePath string, workspaceRoots []string) string {
	base = strings.TrimSpace(base)
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		workspacePath = "."
	}
	workspaceRoots = append([]string(nil), workspaceRoots...)
	if len(workspaceRoots) == 0 {
		workspaceRoots = []string{workspacePath}
	}

	var sandboxBlock strings.Builder
	sandboxBlock.WriteString("Sandbox runtime policy:\n")
	if enabled {
		sandboxBlock.WriteString("- Global sandbox: ON\n")
		sandboxBlock.WriteString("- Bash tool execution is wrapped with bubblewrap strict networking isolation.\n")
		sandboxBlock.WriteString("- This agent run uses an isolated workspace path: ")
		sandboxBlock.WriteString(workspacePath)
		sandboxBlock.WriteString("\n")
		sandboxBlock.WriteString("- Do not assume host-level network/system access beyond sandbox limits.\n")
	} else {
		sandboxBlock.WriteString("- Global sandbox: OFF\n")
		sandboxBlock.WriteString("- Tools run against the host workspace path: ")
		sandboxBlock.WriteString(workspacePath)
		sandboxBlock.WriteString("\n")
	}
	if len(workspaceRoots) == 1 {
		sandboxBlock.WriteString("- Allowed workspace root: ")
		sandboxBlock.WriteString(strings.TrimSpace(workspaceRoots[0]))
		sandboxBlock.WriteString("\n")
	} else {
		sandboxBlock.WriteString("- Allowed workspace roots:\n")
		for _, root := range workspaceRoots {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			sandboxBlock.WriteString("  - ")
			sandboxBlock.WriteString(root)
			sandboxBlock.WriteString("\n")
		}
	}

	if base == "" {
		return sandboxBlock.String()
	}
	return base + "\n\n" + sandboxBlock.String()
}

func prepareSandboxWorkspace(scope tool.WorkspaceScope, runID string) (tool.WorkspaceScope, func(), error) {
	sourcePath := strings.TrimSpace(scope.PrimaryPath)
	if sourcePath == "" {
		return tool.WorkspaceScope{}, func() {}, errors.New("workspace path is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return tool.WorkspaceScope{}, func() {}, errors.New("run id is required for sandbox workspace")
	}

	absSource, err := normalizeRunScopePath(sourcePath)
	if err != nil {
		return tool.WorkspaceScope{}, func() {}, fmt.Errorf("resolve workspace path: %w", err)
	}

	sandboxRoot := filepath.Join(absSource, ".swarm", "sandboxes", runID)
	mirrorRoot := filepath.Join(sandboxRoot, "roots")
	if err := os.RemoveAll(sandboxRoot); err != nil {
		return tool.WorkspaceScope{}, func() {}, fmt.Errorf("reset sandbox context: %w", err)
	}
	if err := os.MkdirAll(mirrorRoot, 0o755); err != nil {
		return tool.WorkspaceScope{}, func() {}, fmt.Errorf("create sandbox workspace root: %w", err)
	}

	roots := append([]string(nil), scope.Roots...)
	if len(roots) == 0 {
		roots = []string{absSource}
	}
	mappedRoots := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, rootErr := normalizeRunScopePath(root)
		if rootErr != nil {
			_ = os.RemoveAll(sandboxRoot)
			return tool.WorkspaceScope{}, func() {}, fmt.Errorf("resolve workspace root: %w", rootErr)
		}
		if _, ok := seen[absRoot]; ok {
			continue
		}
		seen[absRoot] = struct{}{}
		targetRoot := filepath.Join(mirrorRoot, sandboxMirrorRelativePath(absRoot))
		if mirrorErr := mirrorSandboxTree(absRoot, targetRoot); mirrorErr != nil {
			_ = os.RemoveAll(sandboxRoot)
			return tool.WorkspaceScope{}, func() {}, mirrorErr
		}
		mappedRoots = append(mappedRoots, targetRoot)
		if absRoot == absSource {
			absSource = targetRoot
		}
	}
	if len(mappedRoots) == 0 {
		_ = os.RemoveAll(sandboxRoot)
		return tool.WorkspaceScope{}, func() {}, errors.New("sandbox workspace has no mirrored roots")
	}

	cleanup := func() {
		_ = os.RemoveAll(sandboxRoot)
	}
	return tool.WorkspaceScope{
		PrimaryPath: absSource,
		Roots:       mappedRoots,
	}, cleanup, nil
}

func ensureSandboxMirroredRoot(primaryHostPath, hostRoot, runID string) (string, error) {
	primaryHostPath = strings.TrimSpace(primaryHostPath)
	hostRoot = strings.TrimSpace(hostRoot)
	runID = strings.TrimSpace(runID)
	if primaryHostPath == "" {
		return "", errors.New("workspace path is required")
	}
	if hostRoot == "" {
		return "", errors.New("workspace root is required")
	}
	if runID == "" {
		return "", errors.New("run id is required for sandbox workspace")
	}

	absPrimary, err := normalizeRunScopePath(primaryHostPath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	absRoot, err := normalizeRunScopePath(hostRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}

	targetRoot := filepath.Join(absPrimary, ".swarm", "sandboxes", runID, "roots", sandboxMirrorRelativePath(absRoot))
	if info, err := os.Stat(targetRoot); err == nil && info.IsDir() {
		return targetRoot, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect sandbox mirror root: %w", err)
	}
	if err := mirrorSandboxTree(absRoot, targetRoot); err != nil {
		return "", err
	}
	return targetRoot, nil
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

func sandboxMirrorDirPerm(sourceMode os.FileMode) os.FileMode {
	// Keep source visibility bits but always allow owner write/execute so recursive
	// copy can create nested entries even when source directories are read-only.
	return sourceMode.Perm() | 0o700
}

func copySandboxFile(sourcePath, destinationPath string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer target.Close()

	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	return nil
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

func mirrorSandboxTree(sourceRoot, destinationRoot string) error {
	if err := os.MkdirAll(destinationRoot, 0o755); err != nil {
		return fmt.Errorf("create sandbox mirror root: %w", err)
	}
	return filepath.WalkDir(sourceRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}

		rel, relErr := filepath.Rel(sourceRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.Clean(rel)
		relSlash := filepath.ToSlash(rel)
		if relSlash == ".swarm/sandboxes" || strings.HasPrefix(relSlash, ".swarm/sandboxes/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		destination := filepath.Join(destinationRoot, rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			return os.MkdirAll(destination, sandboxMirrorDirPerm(info.Mode()))
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			if mkErr := os.MkdirAll(filepath.Dir(destination), 0o755); mkErr != nil {
				return mkErr
			}
			return os.Symlink(target, destination)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported workspace entry %q (%s)", relSlash, d.Type().String())
		}
		return copySandboxFile(path, destination, info.Mode().Perm())
	})
}

func sandboxMirrorRelativePath(root string) string {
	clean := filepath.Clean(strings.TrimSpace(root))
	volume := filepath.VolumeName(clean)
	if volume != "" {
		clean = strings.TrimPrefix(clean, volume)
		volume = strings.TrimSuffix(volume, ":")
	}
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	if volume != "" {
		return filepath.Join(volume, clean)
	}
	return clean
}

package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ScopeExpansionRequest struct {
	ToolName      string
	ArgumentName  string
	RequestedPath string
	TargetPath    string
	DirectoryPath string
}

func ScopeExpansionForCall(scope WorkspaceScope, call Call) (ScopeExpansionRequest, bool, error) {
	readOnlyRoots := append([]string(nil), scope.ReadOnlyRoots...)
	mutationScopes := append([]string(nil), scope.MutationScopes...)
	scope = normalizeWorkspaceScope(scope.PrimaryPath, scope.Roots)
	scope.ReadOnlyRoots = readOnlyRoots
	scope.MutationScopes = mutationScopes
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		return ScopeExpansionRequest{}, false, nil
	}

	argumentName, requestedPaths, ok := scopeExpansionArguments(call)
	if !ok {
		return ScopeExpansionRequest{}, false, nil
	}
	for _, requestedPath := range requestedPaths {
		request, needed, err := scopeExpansionForPath(scope, call.Name, argumentName, requestedPath)
		if err != nil || needed {
			return request, needed, err
		}
	}
	return ScopeExpansionRequest{}, false, nil
}

func scopeExpansionForPath(scope WorkspaceScope, toolName, argumentName, requestedPath string) (ScopeExpansionRequest, bool, error) {
	targetPath, resolvedTarget, err := normalizeWorkspaceCandidatePath(scope.PrimaryPath, requestedPath)
	if err != nil {
		return ScopeExpansionRequest{}, false, err
	}
	if pathAllowedForScopeCall(scope, toolName, resolvedTarget) {
		return ScopeExpansionRequest{}, false, nil
	}

	directoryPath, err := scopeExpansionDirectory(targetPath, resolvedTarget)
	if err != nil {
		return ScopeExpansionRequest{}, false, err
	}
	if pathAllowedForScopeCall(scope, toolName, directoryPath) {
		return ScopeExpansionRequest{}, false, nil
	}

	return ScopeExpansionRequest{
		ToolName:      strings.TrimSpace(toolName),
		ArgumentName:  argumentName,
		RequestedPath: requestedPath,
		TargetPath:    targetPath,
		DirectoryPath: directoryPath,
	}, true, nil
}

func pathAllowedForScopeCall(scope WorkspaceScope, toolName, candidate string) bool {
	if scopeCallMutatesWorkspace(toolName) {
		if len(scope.MutationScopes) > 0 {
			return workspaceMutationAllowed(scope, candidate)
		}
		return pathWithinAllowedRoots(resolveMutableRoots(scope), candidate)
	}
	return pathWithinAllowedRoots(resolveAllowedRoots(scope), candidate)
}

func scopeCallMutatesWorkspace(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "write", "edit", "webdownload":
		return true
	default:
		return false
	}
}

func normalizeWorkspaceCandidatePath(workspacePath, requested string) (string, string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", "", fmt.Errorf("workspace path is empty")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", "", fmt.Errorf("path is required")
	}

	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Clean(filepath.Join(workspacePath, candidate))
	} else {
		candidate = filepath.Clean(candidate)
	}

	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	resolvedCandidate := candidateAbs
	if resolvedTarget, err := filepath.EvalSymlinks(candidateAbs); err == nil && strings.TrimSpace(resolvedTarget) != "" {
		resolvedCandidate = resolvedTarget
	} else {
		parent := filepath.Dir(candidateAbs)
		resolvedParent := parent
		if rp, parentErr := filepath.EvalSymlinks(parent); parentErr == nil && strings.TrimSpace(rp) != "" {
			resolvedParent = rp
		}
		resolvedCandidate = filepath.Join(resolvedParent, filepath.Base(candidateAbs))
	}
	return candidateAbs, resolvedCandidate, nil
}

func scopeExpansionArguments(call Call) (string, []string, bool) {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if name == "" {
		return "", nil, false
	}

	raw := strings.TrimSpace(call.Arguments)
	if raw == "" {
		raw = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", nil, false
	}

	switch name {
	case "read", "write", "edit", "list", "search", "find", "agentic_search":
		path := strings.TrimSpace(asString(args["path"]))
		if path != "" {
			return "path", []string{path}, true
		}
		paths := asStringSlice(args["paths"])
		requested := make([]string, 0, len(paths))
		for _, candidate := range paths {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				requested = append(requested, candidate)
			}
		}
		if len(requested) == 0 {
			return "", nil, false
		}
		return "paths", requested, true
	case "task":
		requested := make([]string, 0)
		if launches, ok := args["launches"].([]any); ok {
			for _, raw := range launches {
				row, _ := raw.(map[string]any)
				if path := strings.TrimSpace(asString(row["workspace_path"])); path != "" {
					requested = append(requested, path)
				}
			}
		}
		if program, ok := args["program"].(map[string]any); ok {
			if jobs, ok := program["jobs"].([]any); ok {
				for _, raw := range jobs {
					row, _ := raw.(map[string]any)
					if path := strings.TrimSpace(asString(row["workspace_path"])); path != "" {
						requested = append(requested, path)
					}
				}
			}
		}
		if path := strings.TrimSpace(asString(args["workspace_path"])); path != "" {
			requested = append(requested, path)
		}
		if len(requested) == 0 {
			return "", nil, false
		}
		return "workspace_path", requested, true
	case "webdownload":
		path := strings.TrimSpace(asString(args["output_dir"]))
		if path == "" {
			return "", nil, false
		}
		return "output_dir", []string{path}, true
	default:
		return "", nil, false
	}
}

func scopeExpansionDirectory(candidatePath, resolvedTarget string) (string, error) {
	targetInfo, err := os.Stat(resolvedTarget)
	switch {
	case err == nil && targetInfo.IsDir():
		directoryPath := normalizeScopePath(resolvedTarget)
		if scopeExpansionFilesystemRoot(directoryPath) {
			return "", fmt.Errorf("refusing to add filesystem root %q to workspace scope", directoryPath)
		}
		return directoryPath, nil
	case err == nil:
		return nearestExistingScopeDirectory(filepath.Dir(resolvedTarget))
	case os.IsNotExist(err):
		return nearestExistingScopeDirectory(filepath.Dir(candidatePath))
	case err != nil:
		return "", fmt.Errorf("inspect requested path %q: %w", candidatePath, err)
	default:
		return nearestExistingScopeDirectory(filepath.Dir(candidatePath))
	}
}

func nearestExistingScopeDirectory(path string) (string, error) {
	current := filepath.Clean(strings.TrimSpace(path))
	if current == "" {
		return "", fmt.Errorf("path is required")
	}
	for {
		info, err := os.Stat(current)
		switch {
		case err == nil && info.IsDir():
			current = normalizeScopePath(current)
			if scopeExpansionFilesystemRoot(current) {
				return "", fmt.Errorf("refusing to add filesystem root %q to workspace scope", current)
			}
			return current, nil
		case err == nil:
			current = filepath.Dir(current)
		case os.IsNotExist(err):
			next := filepath.Dir(current)
			if next == current {
				return "", fmt.Errorf("no existing directory found for %q", path)
			}
			current = next
		default:
			return "", fmt.Errorf("inspect parent directory %q: %w", current, err)
		}
	}
}

func scopeExpansionFilesystemRoot(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	return filepath.Dir(path) == path
}

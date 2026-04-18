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
	scope = normalizeWorkspaceScope(scope.PrimaryPath, scope.Roots)
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		return ScopeExpansionRequest{}, false, nil
	}

	argumentName, requestedPath, ok := scopeExpansionArgument(call)
	if !ok {
		return ScopeExpansionRequest{}, false, nil
	}

	targetPath, resolvedTarget, err := normalizeWorkspaceCandidatePath(scope.PrimaryPath, requestedPath)
	if err != nil {
		return ScopeExpansionRequest{}, false, err
	}
	if pathWithinAllowedRoots(resolveAllowedRoots(scope), resolvedTarget) {
		return ScopeExpansionRequest{}, false, nil
	}

	directoryPath, err := scopeExpansionDirectory(targetPath, resolvedTarget)
	if err != nil {
		return ScopeExpansionRequest{}, false, err
	}
	if pathWithinAllowedRoots(resolveAllowedRoots(scope), directoryPath) {
		return ScopeExpansionRequest{}, false, nil
	}

	return ScopeExpansionRequest{
		ToolName:      strings.TrimSpace(call.Name),
		ArgumentName:  argumentName,
		RequestedPath: requestedPath,
		TargetPath:    targetPath,
		DirectoryPath: directoryPath,
	}, true, nil
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

func scopeExpansionArgument(call Call) (string, string, bool) {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if name == "" {
		return "", "", false
	}

	raw := strings.TrimSpace(call.Arguments)
	if raw == "" {
		raw = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", "", false
	}

	switch name {
	case "read", "write", "edit", "list", "search", "agentic_search":
		path := strings.TrimSpace(asString(args["path"]))
		if path == "" {
			return "", "", false
		}
		return "path", path, true
	case "webdownload":
		path := strings.TrimSpace(asString(args["output_dir"]))
		if path == "" {
			return "", "", false
		}
		return "output_dir", path, true
	default:
		return "", "", false
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

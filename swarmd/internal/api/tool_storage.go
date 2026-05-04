package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
)

func ensureWorkspaceToolStorage(workspacePath string, toolKind string, threadID string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	toolKind = strings.TrimSpace(toolKind)
	threadID = strings.TrimSpace(threadID)
	if workspacePath == "" {
		return "", errors.New("workspace path is required")
	}
	if toolKind == "" || threadID == "" {
		return "", errors.New("tool storage path requires tool kind and thread id")
	}
	storagePath, err := appstorage.WorkspaceDataDir(workspacePath, "tools", toolKind, "sessions", threadID)
	if err != nil {
		return "", fmt.Errorf("create workspace tool storage: %w", err)
	}
	return storagePath, nil
}

func ensureManagedToolStorageMetadata(metadata map[string]any, storagePath string) map[string]any {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["tool_storage_path"] = storagePath
	return metadata
}

func ensureManagedToolStorageFolders(workspacePath string, storagePath string, folders []string) []string {
	storagePath = filepath.Clean(strings.TrimSpace(storagePath))
	out := []string{storagePath}
	seen := map[string]struct{}{storagePath: {}}
	for _, folder := range folders {
		folder = strings.TrimSpace(folder)
		if folder == "" || isLegacyWorkspaceToolStoragePath(workspacePath, folder) {
			continue
		}
		clean := filepath.Clean(folder)
		if clean == "." {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func isLegacyWorkspaceToolStoragePath(workspacePath string, candidate string) bool {
	workspacePath = strings.TrimSpace(workspacePath)
	candidate = strings.TrimSpace(candidate)
	if workspacePath == "" || candidate == "" {
		return false
	}
	workspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		workspacePath = filepath.Clean(workspacePath)
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspacePath, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		candidate = filepath.Clean(candidate)
	}
	legacyRoot := filepath.Join(workspacePath, ".swarm", "tools")
	return pathWithinRoot(legacyRoot, candidate)
}

func pathWithinRoot(root string, path string) bool {
	root = filepath.Clean(strings.TrimSpace(root))
	path = filepath.Clean(strings.TrimSpace(path))
	if root == "." || root == "" || path == "." || path == "" {
		return false
	}
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

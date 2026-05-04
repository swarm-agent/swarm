package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	absWorkspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", err
	}
	storagePath := filepath.Join(filepath.Clean(absWorkspacePath), ".swarm", "tools", toolKind, "sessions", threadID)
	if err := os.MkdirAll(storagePath, 0o755); err != nil {
		return "", fmt.Errorf("create workspace tool storage: %w", err)
	}
	return storagePath, nil
}

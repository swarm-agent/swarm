package api

import (
	"errors"
	"fmt"
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

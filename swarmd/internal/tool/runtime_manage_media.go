package tool

import (
	"context"
	"errors"
	"os"
)

const ManageMediaToolName = "manage_media"

// MediaLifecycleService reuses the daemon's media task authority without MCP.
type MediaLifecycleService interface {
	ManageMedia(context.Context, WorkspaceScope, map[string]any) (any, error)
}

// manageMediaDefinition is the Orchestrator's manage_media, generated from
// the catalog declaration (catalog_manage_media.go).
func manageMediaDefinition() Definition {
	return manageMediaCatalog().OrchestratorDefinition()
}

// OpenMediaWorkspacePath applies workspace protection and ownership checks.
// The caller must retain and close the root and perform all I/O relative to it.
func OpenMediaWorkspacePath(scope WorkspaceScope, path string, write bool) (*os.Root, string, error) {
	rooted, err := openRootedWorkspacePath(scope, path)
	if err != nil {
		return nil, "", err
	}
	if write && !rooted.mutable {
		rooted.root.Close()
		return nil, "", errors.New("media save is outside the owned mutation scope")
	}
	return rooted.root, rooted.relative, nil
}

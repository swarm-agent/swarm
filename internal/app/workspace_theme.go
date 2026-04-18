package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/client"
)

func (a *App) effectiveWorkspaceThemeID(path string) string {
	path = normalizePath(path)
	if path == "" {
		return ""
	}
	for _, ws := range a.homeModel.Workspaces {
		if normalizePath(ws.Path) != path {
			continue
		}
		return strings.TrimSpace(ws.ThemeID)
	}
	return ""
}

func (a *App) hasActiveWorkspaceThemeScope() bool {
	workspacePath := normalizePath(a.activeWorkspacePath())
	if workspacePath == "" {
		return false
	}
	for _, ws := range a.homeModel.Workspaces {
		if pathsEqual(ws.Path, workspacePath) {
			return true
		}
	}
	return false
}

func workspaceThemeIDFromResolution(resolution client.WorkspaceResolution) string {
	return strings.TrimSpace(resolution.ThemeID)
}

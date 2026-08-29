package pebblestore

import "strings"

func sessionWorkspaceScopeIndexPaths(session SessionSnapshot) []string {
	paths := []string{}
	seen := map[string]struct{}{}
	appendPath := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		normalized, err := normalizeSessionPath(path)
		if err != nil {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		paths = append(paths, normalized)
	}
	canonicalSource := sessionMetadataString(session.Metadata, "swarm_v3_source_workspace_path")
	if canonicalSource != "" {
		appendPath(canonicalSource)
		return paths
	}
	appendPath(session.WorkspacePath)
	// Legacy sessions without canonical source metadata may still need their
	// historical aliases for migration/recovery grouping.
	appendPath(session.WorktreeRootPath)
	for _, grant := range NormalizeSessionWorkspaceGrants(session) {
		appendPath(grant.Path)
	}
	appendPath(sessionMetadataString(session.Metadata, "swarm_v3_tui_cwd_path"))
	appendPath(sessionMetadataString(session.Metadata, "swarm_v3_tui_original_cwd_path"))
	return paths
}

func sessionMatchesWorkspaceScope(session SessionSnapshot, normalizedScopePath string) bool {
	normalizedScopePath = strings.TrimSpace(normalizedScopePath)
	if normalizedScopePath == "" {
		return true
	}
	for _, workspacePath := range sessionWorkspaceScopeIndexPaths(session) {
		if normalizedPathInScope(workspacePath, normalizedScopePath) {
			return true
		}
	}
	return false
}

package session

import "strings"

// IsDeployedSession inspects session metadata to determine if a session was created
// via the session deployment workflow. It strictly distinguishes deployed sessions
// from other child sessions (such as delegated subagents or system sidechats).
func IsDeployedSession(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if val, ok := metadata["lineage_kind"]; ok {
		if s, ok := val.(string); ok && strings.EqualFold(strings.TrimSpace(s), "session_deploy") {
			return true
		}
	}
	if val, ok := metadata["deployment_proposal_id"]; ok {
		if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
	}
	if val, ok := metadata["deployment_manifest_digest"]; ok {
		if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

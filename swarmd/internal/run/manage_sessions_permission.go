package run

import (
	"encoding/json"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) buildManageSessionsPermissionPayload(sessionID string, call tool.Call) (map[string]any, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("manage-sessions arguments invalid: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(mapString(args, "action"))) != "archive" {
		return args, nil
	}
	if s == nil || s.sessions == nil {
		return nil, fmt.Errorf("manage-sessions service is not configured")
	}
	owner, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}

	ids := manageSessionsPermissionIDs(args)
	facts := make([]any, 0, len(ids))
	for _, id := range ids {
		target, found, err := s.sessions.GetSession(id)
		if err != nil {
			return nil, err
		}
		if !found {
			tombstone, archived, tombstoneErr := s.sessions.GetSessionTombstone(id)
			if tombstoneErr != nil {
				return nil, tombstoneErr
			}
			if !archived || !tombstone.Archived {
				return nil, fmt.Errorf("session not found")
			}
			target = tombstone.Session
		}
		if target.AccountScopeID != owner.AccountScopeID || target.UserID != owner.UserID {
			return nil, fmt.Errorf("session not found")
		}
		fact := map[string]any{
			"title":          target.Title,
			"workspace_name": target.WorkspaceName,
			"state":          manageSessionsPermissionState(target.Lifecycle),
			"updated_at":     target.UpdatedAt,
		}
		if branch := strings.TrimSpace(target.WorktreeBranch); branch != "" {
			fact["worktree_branch"] = branch
		}
		facts = append(facts, fact)
	}

	return map[string]any{
		"action":             "archive",
		"sessions":           facts,
		"approved_arguments": cloneGenericMap(args),
	}, nil
}

func manageSessionsPermissionIDs(args map[string]any) []string {
	ids := make([]string, 0, 10)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	if raw, ok := args["session_ids"].([]any); ok {
		for _, value := range raw {
			if id, ok := value.(string); ok {
				add(id)
			}
		}
	}
	add(mapString(args, "session_id"))
	return ids
}

func manageSessionsPermissionState(lifecycle *pebblestore.SessionLifecycleSnapshot) string {
	if lifecycle == nil {
		return "idle"
	}
	phase := strings.ToLower(strings.TrimSpace(lifecycle.Phase))
	if lifecycle.Active && phase == "" {
		return "running"
	}
	switch phase {
	case "needs_review", "review", "final_review":
		return "needs_review"
	case "running", "in_progress":
		return "running"
	case "pending", "queued":
		return "pending"
	case "failed", "blocked", "completed", "cancelled":
		return phase
	default:
		return "idle"
	}
}

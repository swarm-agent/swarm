package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
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
	action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
	if action == "deploy" {
		manifest, err := s.buildManageSessionsDeployManifest(sessionID, call)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
	if action == "commit" {
		return s.buildManageSessionsCommitPermissionPayload(sessionID, args)
	}
	if action != "archive" && action != "unarchive" {
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
	expectedVersions := make(map[string]any, len(ids))
	for _, id := range ids {
		target, found, err := s.sessions.GetSession(id)
		mutationVersion := target.UpdatedAt
		if err != nil {
			return nil, err
		}
		if !found {
			tombstone, archived, tombstoneErr := s.sessions.GetSessionTombstone(id)
			if tombstoneErr != nil {
				return nil, tombstoneErr
			}
			if !archived || !tombstone.Archived || tombstone.Deleted {
				return nil, fmt.Errorf("session not found")
			}
			target = tombstone.Session
			mutationVersion = tombstone.UpdatedAt
		}
		if target.AccountScopeID != owner.AccountScopeID || target.UserID != owner.UserID {
			return nil, fmt.Errorf("session not found")
		}
		if action == "archive" && !found {
			return nil, fmt.Errorf("session %s is already archived", id)
		}
		if action == "unarchive" && found {
			return nil, fmt.Errorf("session %s is not archived", id)
		}
		expectedVersions[id] = mutationVersion
		state := manageSessionsPermissionState(target.Lifecycle)
		if !found {
			state = "archived"
		}
		fact := map[string]any{
			"session_id":     target.ID,
			"title":          target.Title,
			"workspace_name": target.WorkspaceName,
			"workspace_path": target.WorkspacePath,
			"state":          state,
			"updated_at":     mutationVersion,
		}
		if branch := strings.TrimSpace(target.WorktreeBranch); branch != "" {
			fact["worktree_branch"] = branch
		}
		facts = append(facts, fact)
	}

	approved := map[string]any{
		"action":                    action,
		"session_ids":               append([]string(nil), ids...),
		"expected_updated_at_by_id": expectedVersions,
	}
	return map[string]any{
		"action":             action,
		"sessions":           facts,
		"approved_arguments": approved,
	}, nil
}

func (s *Service) buildManageSessionsCommitPermissionPayload(sessionID string, args map[string]any) (map[string]any, error) {
	if s == nil || s.sessions == nil || s.tools == nil {
		return nil, fmt.Errorf("manage-sessions commit services are not configured")
	}
	owner, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, SessionID: owner.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	scope := tool.ManageSessionsCommitScope(owner, principal)
	payload, err := s.tools.PrepareManageSessionsCommitManifest(context.Background(), scope, args)
	if err != nil {
		return nil, err
	}
	approved := cloneGenericMap(payload)
	payload["approved_arguments"] = approved
	payload["path_id"] = "permission.session-commit.v1"
	payload["tool"] = "manage_sessions"
	return payload, nil
}

func isCanonicalManageSessionsMutation(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "commit", "archive", "unarchive":
		return true
	default:
		return false
	}
}

func (s *Service) executeManageSessionsCanonicalMutation(ctx context.Context, sessionID string, call tool.Call, approvedArguments string) (string, error) {
	if s == nil || s.sessions == nil || s.tools == nil {
		return "", fmt.Errorf("manage-sessions mutation services are not configured")
	}
	owner, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, SessionID: owner.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.ManageSessionsCommitScope(owner, principal), tool.Call{CallID: call.CallID, Name: call.Name, Arguments: approvedArguments})
}

func (s *Service) executeManageSessionsCommit(ctx context.Context, sessionID string, call tool.Call, approvedArguments string) (string, error) {
	if s == nil || s.sessions == nil || s.tools == nil {
		return "", fmt.Errorf("manage-sessions commit services are not configured")
	}
	if strings.TrimSpace(approvedArguments) == "" {
		return "", fmt.Errorf("manage-sessions commit requires approved canonical arguments")
	}
	owner, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, SessionID: owner.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.ManageSessionsCommitScope(owner, principal), tool.Call{CallID: call.CallID, Name: call.Name, Arguments: approvedArguments})
}

func manageSessionsPermissionIDs(args map[string]any) []string {
	ids := make([]string, 0, 50)
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

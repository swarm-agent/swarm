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
	if machine := strings.TrimSpace(mapString(args, "machine")); machine != "" {
		switch action {
		case "deploy", "commit", "review_worktrees", "git_status":
			// This machine's own session tooling: never a linked call, and
			// never run here under a machine the person did not mean.
			return nil, fmt.Errorf("manage_sessions %s runs on this machine only; omit machine", action)
		case "archive", "unarchive":
			return s.buildLinkedManageSessionsPermissionPayload(sessionID, machine, action, args)
		}
		// Other actions run as written on the linked machine's handler.
		return args, nil
	}
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
		if mapBool(args, "recovery") {
			// Recovery authorizes an explicit repository, not an attributed target
			// session. The ordinary permission gate still reviews these exact args;
			// runtime independently checks catalog ownership and Git guards.
			return map[string]any{"action": action, "recovery": true, "approved_arguments": cloneGenericMap(args), "request": cloneGenericMap(args)}, nil
		}
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
	if len(ids) == 0 && action == "archive" && (mapBool(args, "all") || strings.TrimSpace(mapString(args, "category")) != "") {
		categoryFilter := strings.ToLower(strings.TrimSpace(mapString(args, "category")))
		searchOpts := pebblestore.V3SessionSearchOptions{
			AccountScopeID: owner.AccountScopeID,
			UserID:         owner.UserID,
			Global:         true,
			ArchivedMode:   "exclude",
			Limit:          50,
		}
		if result, searchErr := s.sessions.SearchSessions(searchOpts); searchErr == nil {
			for _, item := range result.Items {
				if item.ID == sessionID {
					continue
				}
				if categoryFilter != "" && categoryFilter != "all" {
					cat := tool.ManageSessionSidebarCategory(item.Archived, item.Metadata, item.Attention, manageSessionsPermissionState(item.Lifecycle))
					if cat != categoryFilter {
						continue
					}
				}
				ids = append(ids, item.ID)
			}
		}
	}
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
		if target.AccountScopeID != owner.AccountScopeID || (owner.UserID != "" && target.UserID != "" && target.UserID != owner.UserID) {
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

// buildLinkedManageSessionsPermissionPayload is the approval of an archive or
// unarchive on a linked machine: the sessions are that machine's, read through
// its own manage_sessions (list for all/category, get for each one), so the
// person sees what will change there. The approved call keeps machine and
// binds each session's version, so it runs on the linked machine's handler
// and refuses there if a session changed after the person approved it.
func (s *Service) buildLinkedManageSessionsPermissionPayload(sessionID, machine, action string, args map[string]any) (map[string]any, error) {
	if s == nil || s.sessions == nil || s.tools == nil {
		return nil, fmt.Errorf("manage-sessions service is not configured")
	}
	owner, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, SessionID: owner.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	read := func(readArgs map[string]any) (map[string]any, error) {
		return s.tools.LinkedCatalogRead(ctx, principal, machine, "manage_sessions", readArgs)
	}
	ids := manageSessionsPermissionIDs(args)
	if len(ids) == 0 && action == "archive" && (mapBool(args, "all") || strings.TrimSpace(mapString(args, "category")) != "") {
		categoryFilter := strings.ToLower(strings.TrimSpace(mapString(args, "category")))
		listArgs := map[string]any{"action": "list", "limit": float64(50)}
		if categoryFilter != "" && categoryFilter != "all" {
			listArgs["category"] = categoryFilter
		}
		listed, err := read(listArgs)
		if err != nil {
			return nil, err
		}
		items, _ := listed["items"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			id := strings.TrimSpace(mapString(item, "id"))
			if id == "" || mapBool(item, "archived") {
				continue
			}
			if categoryFilter != "" && categoryFilter != "all" && !strings.EqualFold(mapString(item, "category"), categoryFilter) {
				continue
			}
			ids = append(ids, id)
			if len(ids) == 50 {
				break
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s requires session_id, session_ids, category, or all=true", action)
	}
	if len(ids) > 50 {
		return nil, fmt.Errorf("%s supports at most 50 sessions per call", action)
	}
	facts := make([]any, 0, len(ids))
	expectedVersions := make(map[string]any, len(ids))
	for _, id := range ids {
		target, err := read(map[string]any{"action": "get", "session_id": id})
		if err != nil {
			return nil, err
		}
		archived := mapBool(target, "archived")
		if action == "archive" && archived {
			return nil, fmt.Errorf("session %s on %s is already archived", id, machine)
		}
		if action == "unarchive" && !archived {
			return nil, fmt.Errorf("session %s on %s is not archived", id, machine)
		}
		// The version is bound as the local approval binds it: a session
		// that was never updated (0) is approved as the handler treats it
		// on this machine.
		version, ok := target["updated_at"].(float64)
		if !ok || version < 0 {
			return nil, fmt.Errorf("linked machine %s gave no version for session %s", machine, id)
		}
		expectedVersions[id] = int64(version)
		facts = append(facts, map[string]any{
			"machine":        machine,
			"session_id":     id,
			"title":          mapString(target, "title"),
			"workspace_name": mapString(target, "workspace_name"),
			"workspace_path": mapString(target, "workspace_path"),
			"state":          mapString(target, "state"),
			"updated_at":     int64(version),
		})
	}
	approved := map[string]any{
		"action":                    action,
		"machine":                   machine,
		"session_ids":               append([]string(nil), ids...),
		"expected_updated_at_by_id": expectedVersions,
	}
	return map[string]any{
		"action":             action,
		"machine":            machine,
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

func selectedSessionDeployCount(approvedArguments string) int {
	var payload struct {
		SelectedProposalIDs []string `json:"selected_proposal_ids"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(approvedArguments)), &payload) != nil {
		return 0
	}
	return len(payload.SelectedProposalIDs)
}

// manageSessionsApprovedMachineMatches refuses an approved manage_sessions
// change whose approved arguments target another machine than the call: an
// archive or unarchive runs only where the person approved it (the approved
// arguments carry machine for a linked change), and this machine's own
// session tooling (deploy, commit) never runs under a machine argument.
func manageSessionsApprovedMachineMatches(callArguments, approvedArguments string) error {
	var call map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(callArguments)), &call) != nil {
		return nil
	}
	requested := strings.TrimSpace(mapString(call, "machine"))
	switch strings.ToLower(strings.TrimSpace(mapString(call, "action"))) {
	case "deploy", "commit":
		if requested != "" {
			return fmt.Errorf("manage_sessions %s runs on this machine only; omit machine", mapString(call, "action"))
		}
	case "archive", "unarchive":
		var approved map[string]any
		if strings.TrimSpace(approvedArguments) != "" && json.Unmarshal([]byte(strings.TrimSpace(approvedArguments)), &approved) != nil {
			return fmt.Errorf("manage-sessions approved arguments are invalid")
		}
		if strings.TrimSpace(mapString(approved, "machine")) != requested {
			return fmt.Errorf("manage_sessions %s was approved for another machine than it names", mapString(call, "action"))
		}
	}
	return nil
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

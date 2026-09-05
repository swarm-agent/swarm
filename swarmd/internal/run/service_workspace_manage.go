package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const manageWorkspacePathID = "tool.manage-workspace.v1"

// Workspace catalog CRUD is account-scoped and uses exact stable identities.
// Active targets are never mutated in place: the session switches durably to a
// different authorized workspace first, restores after create/update when valid,
// and remains in the safe workspace after delete. Worktree runtime checkout
// identities fail closed. Delete only unlinks catalog data.
type manageWorkspaceArguments struct {
	Action               string
	WorkspaceID          string
	WorkspaceGeneration  int64
	WorkspaceIDs         []string
	PrimaryWorkspaceID   string
	WorktreeName         string
	WorktreePath         string
	ExpectedWorktreePath string
	WorkspacePath        string
	WorkspaceName        string
	ThemeID              string
	WorkspacePathSet     bool
	WorkspaceNameSet     bool
	ThemeIDSet           bool
	Intent               string
	PermissionScope      string
	ExpectedRevision     int64
	Content              string
	ContentSet           bool
}

func (s *Service) executeManageWorkspaceTool(sessionID, arguments string, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.sessions == nil {
		return "", errors.New("manage_workspace is not configured")
	}
	args, err := parseManageWorkspaceArguments(arguments)
	if err != nil {
		return "", err
	}
	if !principal.Valid() {
		return "", identity.ErrPrincipalRequired
	}
	if strings.TrimSpace(sessionID) == "" || (principal.SessionID != "" && principal.SessionID != strings.TrimSpace(sessionID)) {
		return "", errors.New("manage_workspace requires the authenticated active session")
	}
	ownedSession, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return "", err
	}
	if !ok || ownedSession.UserID != principal.UserID || ownedSession.AccountScopeID != principal.AccountScopeID {
		return "", errors.New("manage_workspace session ownership does not match the authenticated principal")
	}
	if args.Action == "inspect_map" || args.Action == "get_map" {
		return s.inspectWorkspaceMap(strings.TrimSpace(sessionID), principal, args)
	}
	if args.Action == "update_map" {
		return s.updateWorkspaceMap(strings.TrimSpace(sessionID), principal, args)
	}
	if s.workspace == nil {
		return "", errors.New("manage_workspace catalog is not configured")
	}
	if args.Action == "inspect" || args.Action == "list" {
		if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || args.PrimaryWorkspaceID != "" || len(args.WorkspaceIDs) > 0 || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet || args.Intent != "" || args.PermissionScope != "" || args.ExpectedRevision != 0 || args.ContentSet {
			return "", errors.New("manage_workspace inspect/list do not accept workspace selectors")
		}
		return s.inspectManageWorkspace(principal, args.Action)
	}
	if args.Action == "create" || args.Action == "update" || args.Action == "edit" || args.Action == "delete" {
		if args.ExpectedRevision != 0 || args.ContentSet {
			return "", fmt.Errorf("manage_workspace %s does not accept Workspace Map fields", args.Action)
		}
		return s.mutateWorkspaceCatalog(strings.TrimSpace(sessionID), principal, args, applySessionMutation)
	}
	if args.ExpectedRevision != 0 || args.ContentSet {
		return "", fmt.Errorf("manage_workspace %s does not accept Workspace Map fields", args.Action)
	}
	if args.Action != "set_session" && args.Action != "set_default" && args.Action != "adopt_worktree" {
		return "", fmt.Errorf("unsupported manage_workspace action %q", args.Action)
	}
	if s.sessionWorkspaceCanonicalize == nil {
		return "", errors.New("manage_workspace workspace canonicalizer is not configured")
	}
	if args.Action == "set_default" {
		return s.setDefaultWorkspace(principal, args)
	}
	if args.Action == "adopt_worktree" {
		return s.adoptSessionWorktree(strings.TrimSpace(sessionID), principal, args, applySessionMutation)
	}
	return s.setSessionWorkspaces(strings.TrimSpace(sessionID), principal, args, applySessionMutation)
}

func parseManageWorkspaceArguments(arguments string) (manageWorkspaceArguments, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return manageWorkspaceArguments{}, fmt.Errorf("manage_workspace arguments invalid: %w", err)
	}
	if raw == nil {
		return manageWorkspaceArguments{}, errors.New("manage_workspace arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return manageWorkspaceArguments{}, errors.New("manage_workspace arguments must contain exactly one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return manageWorkspaceArguments{}, fmt.Errorf("manage_workspace arguments invalid: %w", err)
	}
	allowed := map[string]bool{"action": true, "workspace_id": true, "workspace_generation": true, "workspace_ids": true, "primary_workspace_id": true, "worktree_name": true, "worktree_path": true, "expected_worktree_path": true, "workspace_path": true, "workspace_name": true, "theme_id": true, "intent": true, "permission_scope": true, "expected_revision": true, "content": true}
	for key := range raw {
		if !allowed[key] {
			return manageWorkspaceArguments{}, fmt.Errorf("manage_workspace unknown field %q", key)
		}
	}
	args := manageWorkspaceArguments{
		Action:               strings.ToLower(strings.TrimSpace(mapString(raw, "action"))),
		WorkspaceID:          strings.TrimSpace(mapString(raw, "workspace_id")),
		WorkspaceGeneration:  manageWorkspaceInt64(raw["workspace_generation"]),
		PrimaryWorkspaceID:   strings.TrimSpace(mapString(raw, "primary_workspace_id")),
		WorktreeName:         strings.TrimSpace(mapString(raw, "worktree_name")),
		WorktreePath:         strings.TrimSpace(mapString(raw, "worktree_path")),
		ExpectedWorktreePath: strings.TrimSpace(mapString(raw, "expected_worktree_path")),
		WorkspacePath:        strings.TrimSpace(mapString(raw, "workspace_path")),
		WorkspaceName:        strings.TrimSpace(mapString(raw, "workspace_name")),
		ThemeID:              strings.TrimSpace(mapString(raw, "theme_id")),
		WorkspacePathSet:     raw["workspace_path"] != nil,
		WorkspaceNameSet:     raw["workspace_name"] != nil,
		ThemeIDSet:           raw["theme_id"] != nil,
		Intent:               strings.TrimSpace(mapString(raw, "intent")),
		PermissionScope:      strings.TrimSpace(mapString(raw, "permission_scope")),
		ExpectedRevision:     manageWorkspaceInt64(raw["expected_revision"]),
		Content:              mapString(raw, "content"),
		ContentSet:           raw["content"] != nil,
	}
	if rawAction, provided := raw["action"]; provided {
		if _, ok := rawAction.(string); !ok {
			return manageWorkspaceArguments{}, errors.New("manage_workspace action must be a string")
		}
	}
	if args.Action == "" {
		if _, provided := raw["action"]; provided {
			return manageWorkspaceArguments{}, errors.New("manage_workspace action must be a non-empty string")
		}
		args.Action = "inspect"
	}
	if _, provided := raw["workspace_generation"]; provided && args.WorkspaceGeneration <= 0 {
		return manageWorkspaceArguments{}, errors.New("manage_workspace workspace_generation must be a positive integer")
	}
	if _, provided := raw["expected_revision"]; provided && args.ExpectedRevision <= 0 {
		return manageWorkspaceArguments{}, errors.New("manage_workspace expected_revision must be a positive integer")
	}
	if value, ok := raw["workspace_ids"]; ok {
		items, ok := value.([]any)
		if !ok {
			return manageWorkspaceArguments{}, errors.New("manage_workspace workspace_ids must be an array")
		}
		seen := map[string]struct{}{}
		for _, item := range items {
			id := strings.TrimSpace(fmt.Sprint(item))
			if id == "" {
				return manageWorkspaceArguments{}, errors.New("manage_workspace workspace_ids must not contain empty values")
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			args.WorkspaceIDs = append(args.WorkspaceIDs, id)
		}
	}
	return args, nil
}

func manageWorkspaceInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case float64:
		if typed != float64(int64(typed)) {
			return 0
		}
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func (s *Service) inspectWorkspaceMap(sessionID string, principal identity.Principal, args manageWorkspaceArguments) (string, error) {
	if s == nil || s.workspaceMap == nil {
		return "", errors.New("manage_workspace workspace map is not configured")
	}
	if err := s.validateWorkspaceMapSessionOwner(sessionID, principal); err != nil {
		return "", err
	}
	if args.ExpectedRevision != 0 || args.ContentSet || args.Intent != "" || args.PermissionScope != "" || args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || len(args.WorkspaceIDs) != 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet {
		return "", errors.New("manage_workspace inspect_map/get_map do not accept mutation or workspace selector fields")
	}
	record, err := s.workspaceMap.GetOrCreateDefault(principal.AccountScopeID)
	if err != nil {
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": args.Action, "status": "ok", "workspace_map": record})
}

func (s *Service) updateWorkspaceMap(sessionID string, principal identity.Principal, args manageWorkspaceArguments) (string, error) {
	if s == nil || s.workspaceMap == nil {
		return "", errors.New("manage_workspace workspace map is not configured")
	}
	if err := s.validateWorkspaceMapSessionOwner(sessionID, principal); err != nil {
		return "", err
	}
	if args.PermissionScope != "workspace_map_update" {
		return "", errors.New("manage_workspace update_map approved permission_scope is invalid")
	}
	if args.ExpectedRevision <= 0 {
		return "", errors.New("manage_workspace update_map requires expected_revision")
	}
	if !args.ContentSet {
		return "", errors.New("manage_workspace update_map requires content")
	}
	if strings.TrimSpace(args.Intent) == "" || len([]byte(args.Intent)) > 500 {
		return "", errors.New("manage_workspace update_map requires a user-readable intent of at most 500 characters")
	}
	if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || len(args.WorkspaceIDs) != 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet {
		return "", errors.New("manage_workspace update_map accepts only map mutation fields")
	}
	updated, err := s.workspaceMap.Update(principal.AccountScopeID, args.ExpectedRevision, args.Content)
	if err != nil {
		return "", err
	}
	// Revision and digest come from the synchronously persisted record itself, so
	// success never depends on a second non-atomic event append.
	evidence := map[string]any{"durable": true, "account_scoped": true, "session_id": sessionID, "revision": updated.Revision, "digest": updated.Digest, "intent": args.Intent, "updated_at": updated.UpdatedAt}
	return marshalManageWorkspace(map[string]any{"action": "update_map", "status": "ok", "permission_scope": args.PermissionScope, "intent": args.Intent, "workspace_map": updated, "mutation_evidence": evidence})
}

func (s *Service) validateWorkspaceMapSessionOwner(sessionID string, principal identity.Principal) error {
	if sessionID == "" {
		return errors.New("manage_workspace workspace map action requires an active session")
	}
	if !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if principal.SessionID != "" && principal.SessionID != sessionID {
		return errors.New("manage_workspace principal session does not match the active session")
	}
	return nil
}

func (s *Service) inspectManageWorkspace(principal identity.Principal, action string) (string, error) {
	if !principal.Valid() {
		return "", identity.ErrPrincipalRequired
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 2000)
	if err != nil {
		return "", err
	}
	workspaces := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		workspaces = append(workspaces, map[string]any{
			"workspace_id": entry.WorkspaceID, "workspace_generation": entry.WorkspaceGeneration,
			"workspace_name": entry.WorkspaceName, "workspace_path": entry.Path,
			"theme_id": entry.ThemeID, "state": entry.State, "default": entry.Active,
		})
	}
	return marshalManageWorkspace(map[string]any{
		"action": action, "status": "ok", "workspaces": workspaces,
		"actions": []string{"inspect", "list", "inspect_map", "get_map", "update_map", "create", "update", "delete", "set_session", "set_default", "adopt_worktree"},
	})
}

// mutateWorkspaceCatalog accepts only backend-approved canonical arguments.
func (s *Service) mutateWorkspaceCatalog(sessionID string, principal identity.Principal, args manageWorkspaceArguments, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	action := args.Action
	if action == "edit" {
		action = "update"
	}
	if action != "create" && action != "update" && action != "delete" {
		return "", fmt.Errorf("unsupported manage_workspace mutation action %q", action)
	}
	if args.PermissionScope != "workspace_"+action {
		return "", errors.New("manage_workspace approved permission_scope permission scope is invalid")
	}
	if strings.TrimSpace(args.Intent) == "" || len([]byte(args.Intent)) > 500 {
		return "", errors.New("manage_workspace requires a user-readable intent of at most 500 characters")
	}
	if args.ThemeIDSet && len(args.ThemeID) > 128 {
		return "", errors.New("manage_workspace theme_id must not exceed 128 characters")
	}
	if len(args.WorkspaceName) > 200 {
		return "", errors.New("manage_workspace workspace_name must not exceed 200 characters")
	}
	if sessionID == "" {
		return "", fmt.Errorf("manage_workspace %s requires an active session", action)
	}
	if !principal.Valid() {
		return "", identity.ErrPrincipalRequired
	}
	if principal.SessionID != "" && principal.SessionID != sessionID {
		return "", errors.New("manage_workspace principal session does not match the active session")
	}
	if s.sessionWorkspaceCanonicalize == nil {
		return "", errors.New("manage_workspace workspace canonicalizer is not configured")
	}
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" {
		return "", fmt.Errorf("manage_workspace %s accepts only catalog mutation fields", action)
	}

	prior, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if prior.UserID != principal.UserID || prior.AccountScopeID != principal.AccountScopeID {
		return "", errors.New("manage_workspace session ownership does not match the authenticated principal")
	}

	if action == "create" {
		return s.createWorkspaceCatalogEntry(sessionID, principal, prior, args, applySessionMutation)
	}
	if applySessionMutation == nil {
		return "", fmt.Errorf("manage_workspace %s requires the canonical V3 mutation publisher", action)
	}
	if args.WorkspaceID == "" || args.WorkspaceGeneration <= 0 {
		return "", fmt.Errorf("manage_workspace %s requires workspace_id and workspace_generation", action)
	}
	if action == "update" && args.WorkspaceNameSet && args.WorkspaceName == "" {
		return "", errors.New("manage_workspace update workspace_name must not be empty when provided")
	}
	if action == "update" && !args.WorkspacePathSet && !args.WorkspaceNameSet && !args.ThemeIDSet {
		return "", errors.New("manage_workspace update requires at least one requested change")
	}
	if action == "delete" && (args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet) {
		return "", errors.New("manage_workspace delete does not accept workspace changes")
	}
	target, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, args.WorkspaceID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("workspace id %q not found", args.WorkspaceID)
	}
	if target.WorkspaceGeneration != args.WorkspaceGeneration {
		return "", fmt.Errorf("workspace generation is stale: expected %d, current %d", args.WorkspaceGeneration, target.WorkspaceGeneration)
	}
	if !strings.EqualFold(target.State, "active") {
		return "", fmt.Errorf("workspace id %q is not active", target.WorkspaceID)
	}
	activeTarget := sessionTargetsWorkspace(prior, target.WorkspaceID, target.Path)
	if cleanManageWorkspacePath(prior.WorktreeRootPath) != "" && cleanManageWorkspacePath(prior.WorktreeRootPath) == cleanManageWorkspacePath(target.Path) {
		return "", errors.New("manage_workspace cannot mutate a catalog identity whose path is the runtime checkout of a worktree-backed session; mutate its source workspace identity instead")
	}
	safety := map[string]any{"filesystem_contents_changed": false, "session_switch_required": activeTarget, "switched_before_mutation": false, "restored_after_mutation": false, "left_in_safe_workspace": false}
	var safe SessionWorkspaceCanonicalization
	if activeTarget {
		safe, err = s.selectSafeCatalogMutationWorkspace(principal, target.WorkspaceID, target.Path)
		if err != nil {
			return "", err
		}
		setSafeWorkspaceMetadata(safety, safe)
		if _, err := s.setSessionWorkspaces(sessionID, principal, manageWorkspaceArguments{Action: "set_session", WorkspaceID: safe.WorkspaceID, WorkspaceGeneration: safe.WorkspaceGeneration}, applySessionMutation); err != nil {
			return "", fmt.Errorf("switch to safe workspace before %s: %w", action, err)
		}
		safety["switched_before_mutation"] = true
	}

	var resolution any
	if action == "update" {
		var name, themeID *string
		if args.WorkspaceNameSet {
			value := args.WorkspaceName
			name = &value
		}
		if args.ThemeIDSet {
			value := args.ThemeID
			themeID = &value
		}
		updated, updateErr := s.workspace.UpdateCatalogEntryForPrincipal(principal, args.WorkspaceID, args.WorkspaceGeneration, args.WorkspacePath, name, themeID)
		if updateErr != nil {
			updateErr = manageWorkspaceRepositoryError(updateErr)
			if activeTarget {
				if restoreErr := s.restoreWorkspaceByID(sessionID, principal, target.WorkspaceID, target.WorkspaceGeneration, applySessionMutation); restoreErr != nil {
					return "", errors.Join(updateErr, fmt.Errorf("update failed and session restoration failed: %w", restoreErr))
				}
			}
			return "", updateErr
		}
		resolution = updated
		// Reconcile every captured grant in the current session before restoring
		// its primary routing identity, so the next tool scope cannot be stale.
		if err := s.reconcileUpdatedWorkspaceGrant(sessionID, principal, manageWorkspaceResolutionTarget(updated), applySessionMutation); err != nil {
			return "", fmt.Errorf("workspace catalog update committed but session grant reconciliation failed: %w", err)
		}
		if activeTarget {
			if err := s.ensureSessionCanRestoreToWorkspace(prior, updated); err != nil {
				safety["left_in_safe_workspace"] = true
				safety["restore_error"] = err.Error()
			} else if err := s.restoreWorkspaceByID(sessionID, principal, updated.WorkspaceID, updated.WorkspaceGeneration, applySessionMutation); err != nil {
				safety["left_in_safe_workspace"] = true
				safety["restore_error"] = err.Error()
			} else {
				safety["restored_after_mutation"] = true
			}
		}
	} else {
		deleted, deleteErr := s.workspace.DeleteCatalogEntryForPrincipal(principal, args.WorkspaceID, args.WorkspaceGeneration)
		if deleteErr != nil {
			if activeTarget {
				if restoreErr := s.restoreWorkspaceByID(sessionID, principal, target.WorkspaceID, target.WorkspaceGeneration, applySessionMutation); restoreErr != nil {
					return "", errors.Join(deleteErr, fmt.Errorf("delete failed and session restoration failed: %w", restoreErr))
				}
			}
			return "", deleteErr
		}
		resolution = deleted
		// A deleted catalog identity must not remain in this session's durable
		// grants even when it was not the current primary workspace.
		if err := s.removeDeletedWorkspaceGrant(sessionID, principal, target.WorkspaceID, applySessionMutation); err != nil {
			return "", fmt.Errorf("workspace catalog deletion committed but session recovery failed: %w", err)
		}
		if activeTarget {
			safety["left_in_safe_workspace"] = true
			safety["delete_restore_valid"] = false
		}
	}
	status := "ok"
	if safety["left_in_safe_workspace"] == true && action == "update" {
		status = "committed_restore_failed"
	}
	return marshalManageWorkspace(map[string]any{"action": action, "status": status, "intent": args.Intent, "permission_scope": args.PermissionScope, "target": manageWorkspaceResolutionTarget(resolution), "requested_changes": manageWorkspaceRequestedChanges(args), "safety": safety, "restart_turn": activeTarget})
}

func (s *Service) createWorkspaceCatalogEntry(sessionID string, principal identity.Principal, prior pebblestore.SessionSnapshot, args manageWorkspaceArguments, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || !args.WorkspacePathSet || args.WorkspacePath == "" {
		return "", errors.New("manage_workspace create requires workspace_path and does not accept workspace identity fields")
	}
	if args.WorkspaceNameSet && args.WorkspaceName == "" {
		return "", errors.New("manage_workspace create workspace_name must not be empty when provided")
	}
	if len(args.WorkspaceName) > 200 {
		return "", errors.New("manage_workspace workspace_name must not exceed 200 characters")
	}
	activeTarget := cleanManageWorkspacePath(prior.WorkspacePath) == cleanManageWorkspacePath(args.WorkspacePath) || cleanManageWorkspacePath(prior.WorktreeRootPath) == cleanManageWorkspacePath(args.WorkspacePath)
	if activeTarget && prior.WorktreeEnabled && cleanManageWorkspacePath(prior.WorktreeRootPath) == cleanManageWorkspacePath(args.WorkspacePath) {
		return "", errors.New("manage_workspace cannot create a catalog entry for the runtime checkout of a worktree-backed session; create the source workspace entry instead")
	}
	if applySessionMutation == nil {
		return "", errors.New("manage_workspace create requires the canonical V3 mutation publisher for session grant maintenance")
	}
	safety := map[string]any{"filesystem_contents_changed": false, "session_switch_required": activeTarget, "switched_before_mutation": false, "restored_after_mutation": false, "left_in_safe_workspace": false}
	var safe SessionWorkspaceCanonicalization
	var err error
	if activeTarget {
		safe, err = s.selectSafeCatalogMutationWorkspace(principal, "", args.WorkspacePath)
		if err != nil {
			return "", err
		}
		setSafeWorkspaceMetadata(safety, safe)
		if _, err := s.setSessionWorkspaces(sessionID, principal, manageWorkspaceArguments{Action: "set_session", WorkspaceID: safe.WorkspaceID, WorkspaceGeneration: safe.WorkspaceGeneration}, applySessionMutation); err != nil {
			return "", fmt.Errorf("switch to safe workspace before create: %w", err)
		}
		safety["switched_before_mutation"] = true
	}
	created, err := s.workspace.CreateCatalogEntryForPrincipal(principal, args.WorkspacePath, args.WorkspaceName, args.ThemeID)
	if err != nil {
		err = manageWorkspaceRepositoryError(err)
		if activeTarget {
			if restoreErr := s.restoreExactWorkspaceSession(prior, principal, applySessionMutation, "create_failed"); restoreErr != nil {
				return "", errors.Join(err, fmt.Errorf("create failed and restoration failed; session remains in safe workspace %q: %w", safe.WorkspaceID, restoreErr))
			}
		}
		return "", err
	}
	if err := s.ensureCreatedWorkspaceGrant(sessionID, principal, created, applySessionMutation); err != nil {
		return "", fmt.Errorf("workspace catalog creation committed but session grant reconciliation failed: %w", err)
	}
	status := "ok"
	if activeTarget {
		if err := s.ensureSessionCanRestoreToWorkspace(prior, created); err != nil {
			status = "committed_restore_failed"
			safety["left_in_safe_workspace"] = true
			safety["restore_error"] = err.Error()
		} else if err := s.restoreWorkspaceByID(sessionID, principal, created.WorkspaceID, created.WorkspaceGeneration, applySessionMutation); err != nil {
			status = "committed_restore_failed"
			safety["left_in_safe_workspace"] = true
			safety["restore_error"] = err.Error()
		} else {
			safety["restored_after_mutation"] = true
		}
	}
	return marshalManageWorkspace(map[string]any{"action": "create", "status": status, "intent": args.Intent, "permission_scope": args.PermissionScope, "target": manageWorkspaceResolutionTarget(created), "requested_changes": manageWorkspaceRequestedChanges(args), "safety": safety, "restart_turn": activeTarget})
}

func manageWorkspaceRepositoryError(err error) error {
	if repository, ok := workspaceruntime.RepositoryStateFromError(err); ok {
		payload, marshalErr := json.Marshal(map[string]any{"code": "workspace_repository_not_ready", "repository": repository})
		if marshalErr == nil {
			return fmt.Errorf("%s: %s", err.Error(), payload)
		}
	}
	return err
}

func sessionTargetsWorkspace(session pebblestore.SessionSnapshot, workspaceID, path string) bool {
	return strings.TrimSpace(mapString(session.Metadata, "swarm_v3_source_workspace_id")) == strings.TrimSpace(workspaceID) || cleanManageWorkspacePath(session.WorkspacePath) == cleanManageWorkspacePath(path) || cleanManageWorkspacePath(session.WorktreeRootPath) == cleanManageWorkspacePath(path)
}

func setSafeWorkspaceMetadata(safety map[string]any, safe SessionWorkspaceCanonicalization) {
	safety["safe_workspace_id"] = safe.WorkspaceID
	safety["safe_workspace_generation"] = safe.WorkspaceGeneration
	safety["safe_workspace_name"] = safe.WorkspaceName
	safety["safe_workspace_path"] = safe.SourceWorkspacePath
}

func (s *Service) selectSafeCatalogMutationWorkspace(principal identity.Principal, excludedWorkspaceID, excludedPath string) (SessionWorkspaceCanonicalization, error) {
	entries, err := s.workspace.ListKnownForPrincipal(principal, 2000)
	if err != nil {
		return SessionWorkspaceCanonicalization{}, err
	}
	for _, entry := range entries {
		if entry.WorkspaceID == excludedWorkspaceID || cleanManageWorkspacePath(entry.Path) == cleanManageWorkspacePath(excludedPath) || !strings.EqualFold(entry.State, "active") {
			continue
		}
		canonical, canonicalErr := s.canonicalSessionWorkspace(principal, entry.WorkspaceID, entry.WorkspaceGeneration)
		if canonicalErr == nil {
			return canonical, nil
		}
	}
	return SessionWorkspaceCanonicalization{}, errors.New("manage_workspace cannot mutate the active workspace because no different authorized safe workspace is available")
}

func (s *Service) restoreWorkspaceByID(sessionID string, principal identity.Principal, workspaceID string, generation int64, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	_, err := s.setSessionWorkspaces(sessionID, principal, manageWorkspaceArguments{Action: "set_session", WorkspaceID: workspaceID, WorkspaceGeneration: generation}, applySessionMutation)
	return err
}

func (s *Service) restoreExactWorkspaceSession(prior pebblestore.SessionSnapshot, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), reason string) error {
	if applySessionMutation == nil {
		return errors.New("canonical V3 mutation publisher is required")
	}
	next := prior
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": next.ID, "reason": reason, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return err
	}
	key := manageWorkspaceMutationKey("manage-workspace-restore", next.ID, payload)
	_, err = applySessionMutation(sessionruntime.SessionMutationInput{SessionID: next.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.restored", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	return err
}

func (s *Service) ensureCreatedWorkspaceGrant(sessionID string, principal identity.Principal, created any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	target := manageWorkspaceResolutionTarget(created)
	workspaceID, _ := target["workspace_id"].(string)
	workspacePath, _ := target["workspace_path"].(string)
	workspaceName, _ := target["workspace_name"].(string)
	generation := manageWorkspaceInt64(target["workspace_generation"])
	if workspaceID == "" || workspacePath == "" || workspaceName == "" || generation <= 0 {
		return errors.New("created workspace target metadata is incomplete")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(session) {
		if grant.WorkspaceID == workspaceID {
			return nil
		}
	}
	available := true
	next := session
	next.WorkspaceGrants = append(pebblestore.NormalizeSessionWorkspaceGrants(session), pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: workspaceID, WorkspaceGeneration: generation, Path: workspacePath, Name: workspaceName, Available: &available})
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(next)
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "workspace_id": workspaceID, "workspace_generation": generation, "workspace_grants": next.WorkspaceGrants, "workspace_usage": next.WorkspaceUsage, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return err
	}
	key := manageWorkspaceMutationKey("manage-workspace-create-grant", sessionID, payload)
	_, err = applySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.catalog_created", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	return err
}

func (s *Service) reconcileUpdatedWorkspaceGrant(sessionID string, principal identity.Principal, target map[string]any, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if applySessionMutation == nil {
		return errors.New("canonical V3 mutation publisher is required")
	}
	workspaceID, _ := target["workspace_id"].(string)
	workspacePath, _ := target["workspace_path"].(string)
	workspaceName, _ := target["workspace_name"].(string)
	generation := manageWorkspaceInt64(target["workspace_generation"])
	if workspaceID == "" || workspacePath == "" || workspaceName == "" || generation <= 0 {
		return errors.New("updated workspace target metadata is incomplete")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	next := session
	grants := pebblestore.NormalizeSessionWorkspaceGrants(session)
	changed := false
	for i := range grants {
		if grants[i].WorkspaceID != workspaceID {
			continue
		}
		grants[i].WorkspaceGeneration, grants[i].Path, grants[i].Name = generation, workspacePath, workspaceName
		changed = true
	}
	if !changed {
		return nil
	}
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants})
	for _, grant := range next.WorkspaceGrants {
		if grant.WorkspaceID == workspaceID && (grant.WorkspaceGeneration != generation || grant.Path != workspacePath || grant.Name != workspaceName) {
			return fmt.Errorf("updated workspace grant %q remains stale after normalization", workspaceID)
		}
	}
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "workspace_id": workspaceID, "workspace_generation": generation, "workspace_path": workspacePath, "workspace_name": workspaceName, "workspace_grants": next.WorkspaceGrants, "workspace_usage": next.WorkspaceUsage, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return err
	}
	key := manageWorkspaceMutationKey("manage-workspace-grant-reconcile", sessionID, payload)
	_, err = applySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.catalog_updated", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	return err
}

func (s *Service) removeDeletedWorkspaceGrant(sessionID string, principal identity.Principal, workspaceID string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
	if applySessionMutation == nil {
		return errors.New("canonical V3 mutation publisher is required")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	next := session
	grants := make([]pebblestore.WorkspaceGrant, 0, len(session.WorkspaceGrants))
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(session) {
		if grant.WorkspaceID != workspaceID {
			grants = append(grants, grant)
		}
	}
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants})
	for _, grant := range next.WorkspaceGrants {
		if grant.WorkspaceID == workspaceID {
			return fmt.Errorf("deleted workspace grant %q remains after normalization", workspaceID)
		}
	}
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "deleted_workspace_id": workspaceID, "workspace_grants": next.WorkspaceGrants, "workspace_usage": next.WorkspaceUsage, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return err
	}
	key := manageWorkspaceMutationKey("manage-workspace-delete-reconcile", sessionID, payload)
	_, err = applySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.catalog_deleted", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	return err
}

func manageWorkspaceMutationKey(prefix, sessionID string, payload []byte) string {
	digest := sha256.Sum256(payload)
	return prefix + ":" + sessionID + ":" + hex.EncodeToString(digest[:])
}

func (s *Service) ensureSessionCanRestoreToWorkspace(prior pebblestore.SessionSnapshot, resolution any) error {
	if resolution == nil {
		return errors.New("manage_workspace restoration target is unavailable")
	}
	if !prior.WorktreeEnabled {
		return nil
	}
	target := manageWorkspaceResolutionTarget(resolution)
	workspacePath, _ := target["workspace_path"].(string)
	if workspacePath != "" && cleanManageWorkspacePath(prior.WorktreeRootPath) == cleanManageWorkspacePath(workspacePath) {
		return errors.New("manage_workspace cannot restore a worktree-backed session to a catalog identity whose source path is the runtime checkout")
	}
	return nil
}

func manageWorkspaceResolutionTarget(resolution any) map[string]any {
	raw, err := json.Marshal(resolution)
	if err != nil {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]any{}
	}
	return map[string]any{"workspace_id": payload["workspace_id"], "workspace_generation": payload["workspace_generation"], "workspace_name": payload["workspace_name"], "workspace_path": payload["workspace_path"], "theme_id": payload["theme_id"]}
}

func manageWorkspaceRequestedChanges(args manageWorkspaceArguments) map[string]any {
	changes := map[string]any{}
	if args.WorkspacePathSet {
		changes["workspace_path"] = args.WorkspacePath
	}
	if args.WorkspaceNameSet {
		changes["workspace_name"] = args.WorkspaceName
	}
	if args.ThemeIDSet {
		changes["theme_id"] = args.ThemeID
	}
	return changes
}

func cleanManageWorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// buildManageWorkspacePermissionPayload emits only bounded metadata needed for
// user review and injects backend-owned canonical approved arguments.
func (s *Service) buildManageWorkspacePermissionPayload(sessionID, callArguments string) (map[string]any, error) {
	if s == nil || s.sessions == nil {
		return nil, errors.New("manage_workspace is not configured")
	}
	args, err := parseManageWorkspaceArguments(callArguments)
	if err != nil {
		return nil, err
	}
	if args.PermissionScope != "" {
		return nil, errors.New("manage_workspace model-authored mutation must omit permission_scope")
	}
	if len([]byte(args.Intent)) > 500 {
		return nil, errors.New("manage_workspace intent must not exceed 500 characters")
	}
	if args.Action == "update_map" {
		if strings.TrimSpace(sessionID) == "" {
			return nil, errors.New("manage_workspace update_map requires an active session")
		}
		if s.workspaceMap == nil {
			return nil, errors.New("manage_workspace workspace map is not configured")
		}
		if args.ExpectedRevision <= 0 || !args.ContentSet {
			return nil, errors.New("manage_workspace update_map requires expected_revision and content")
		}
		if strings.TrimSpace(args.Intent) == "" {
			return nil, errors.New("manage_workspace update_map requires an explicit user-readable intent")
		}
		if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || len(args.WorkspaceIDs) != 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet {
			return nil, errors.New("manage_workspace update_map accepts only map mutation fields")
		}
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
		if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.UserID) == "" {
			return nil, errors.New("manage_workspace update_map session ownership is incomplete")
		}
		current, err := s.workspaceMap.GetOrCreateDefault(session.AccountScopeID)
		if err != nil {
			return nil, err
		}
		if current.Revision != args.ExpectedRevision {
			return nil, fmt.Errorf("%w: expected %d, current %d", pebblestore.ErrWorkspaceMapRevisionConflict, args.ExpectedRevision, current.Revision)
		}
		normalizedContent, err := pebblestore.NormalizeWorkspaceMapContent(args.Content)
		if err != nil {
			return nil, err
		}
		approved := map[string]any{"action": "update_map", "expected_revision": args.ExpectedRevision, "content": normalizedContent, "intent": args.Intent, "permission_scope": "workspace_map_update"}
		return map[string]any{
			"action": "update_map", "permission_scope": "workspace_map_update", "intent": args.Intent,
			"target":             map[string]any{"account_scoped": true, "revision": current.Revision, "digest": current.Digest},
			"requested_changes":  map[string]any{"content_bytes": len(args.Content)},
			"safety":             map[string]any{"account_scoped": true, "optimistic_concurrency": true, "filesystem_contents_changed": false},
			"approved_arguments": approved,
		}, nil
	}
	if len(args.WorkspaceName) > 200 || len(args.ThemeID) > 128 {
		return nil, errors.New("manage_workspace workspace_name or theme_id is too long")
	}
	if s.workspace == nil {
		return nil, errors.New("manage_workspace catalog is not configured")
	}
	if args.Action == "edit" {
		args.Action = "update"
	}
	if args.Action != "create" && args.Action != "update" && args.Action != "delete" {
		return nil, fmt.Errorf("unsupported manage_workspace mutation action %q", args.Action)
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID, SessionID: session.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	payload := map[string]any{"action": args.Action, "permission_scope": "workspace_" + args.Action, "requested_changes": manageWorkspaceRequestedChanges(args)}
	approved := map[string]any{}
	if err := json.Unmarshal([]byte(firstNonEmptyString(strings.TrimSpace(callArguments), "{}")), &approved); err != nil {
		return nil, err
	}
	approved["action"] = args.Action
	approved["permission_scope"] = payload["permission_scope"]
	if args.Intent == "" {
		if args.Action == "create" {
			args.Intent = "Create a saved workspace catalog entry for this existing directory."
		} else if args.Action == "update" {
			args.Intent = "Update this saved workspace after a stale identity check."
		} else {
			args.Intent = "Unlink this saved workspace from the catalog without deleting any files."
		}
		approved["intent"] = args.Intent
	}
	payload["intent"] = args.Intent
	payload["approved_arguments"] = approved

	if args.Action == "create" {
		if args.ThemeIDSet && len(args.ThemeID) > 128 {
			return nil, errors.New("manage_workspace theme_id must not exceed 128 characters")
		}
		if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" {
			return nil, errors.New("manage_workspace create accepts only catalog mutation fields")
		}
		if args.WorkspaceNameSet && args.WorkspaceName == "" {
			return nil, errors.New("manage_workspace create workspace_name must not be empty when provided")
		}
		if args.WorkspacePath == "" || args.WorkspaceID != "" || args.WorkspaceGeneration != 0 {
			return nil, errors.New("manage_workspace create requires workspace_path and no workspace identity")
		}
		active := cleanManageWorkspacePath(session.WorkspacePath) == cleanManageWorkspacePath(args.WorkspacePath) || cleanManageWorkspacePath(session.WorktreeRootPath) == cleanManageWorkspacePath(args.WorkspacePath)
		if cleanManageWorkspacePath(session.WorktreeRootPath) != "" && cleanManageWorkspacePath(session.WorktreeRootPath) == cleanManageWorkspacePath(args.WorkspacePath) {
			return nil, errors.New("manage_workspace cannot create a catalog entry for the runtime checkout of a worktree-backed session; create the source workspace entry instead")
		}
		payload["target"] = map[string]any{"workspace_name": args.WorkspaceName, "workspace_path": args.WorkspacePath, "theme_id": args.ThemeID}
		payload["safety"] = map[string]any{"session_switch_required": active, "switch_before_mutation": active, "restore_after_mutation": active, "remain_in_safe_workspace": false, "filesystem_contents_changed": false, "catalog_only": true}
		return payload, nil
	}
	if args.ThemeIDSet && len(args.ThemeID) > 128 {
		return nil, errors.New("manage_workspace theme_id must not exceed 128 characters")
	}
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" {
		return nil, fmt.Errorf("manage_workspace %s accepts only catalog mutation fields", args.Action)
	}
	if args.WorkspaceID == "" || args.WorkspaceGeneration <= 0 {
		return nil, fmt.Errorf("manage_workspace %s requires workspace_id and workspace_generation", args.Action)
	}
	if args.Action == "update" && args.WorkspaceNameSet && args.WorkspaceName == "" {
		return nil, errors.New("manage_workspace update workspace_name must not be empty when provided")
	}
	if args.Action == "update" && !args.WorkspacePathSet && !args.WorkspaceNameSet && !args.ThemeIDSet {
		return nil, errors.New("manage_workspace update requires at least one requested change")
	}
	if args.Action == "delete" && (args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet) {
		return nil, errors.New("manage_workspace delete does not accept workspace changes")
	}
	entry, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, args.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("workspace id %q not found", args.WorkspaceID)
	}
	if entry.WorkspaceGeneration != args.WorkspaceGeneration {
		return nil, fmt.Errorf("workspace generation is stale: expected %d, current %d", args.WorkspaceGeneration, entry.WorkspaceGeneration)
	}
	if !strings.EqualFold(strings.TrimSpace(entry.State), "active") {
		return nil, fmt.Errorf("workspace id %q is not active", entry.WorkspaceID)
	}
	active := sessionTargetsWorkspace(session, entry.WorkspaceID, entry.Path)
	if cleanManageWorkspacePath(session.WorktreeRootPath) != "" && cleanManageWorkspacePath(session.WorktreeRootPath) == cleanManageWorkspacePath(entry.Path) {
		return nil, errors.New("manage_workspace cannot mutate a catalog identity whose path is the runtime checkout of a worktree-backed session; mutate its source workspace identity instead")
	}
	payload["target"] = map[string]any{"workspace_id": entry.WorkspaceID, "workspace_generation": entry.WorkspaceGeneration, "workspace_name": entry.Name, "workspace_path": entry.Path, "theme_id": entry.ThemeID}
	payload["safety"] = map[string]any{"session_switch_required": active, "switch_before_mutation": active, "restore_after_mutation": active && args.Action == "update", "remain_in_safe_workspace": active && args.Action == "delete", "filesystem_contents_changed": false, "catalog_only": true}
	return payload, nil
}

func (s *Service) adoptSessionWorktree(sessionID string, principal identity.Principal, args manageWorkspaceArguments, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.sessions == nil || s.worktrees == nil {
		return "", errors.New("manage_workspace worktree adoption is not configured")
	}
	if applySessionMutation == nil {
		return "", errors.New("manage_workspace adopt_worktree requires the canonical V3 mutation publisher")
	}
	if sessionID == "" {
		return "", errors.New("manage_workspace adopt_worktree requires an active session")
	}
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" {
		return "", errors.New("manage_workspace adopt_worktree accepts workspace_id, not workspace_ids or primary_workspace_id")
	}
	if args.WorktreeName != "" && args.WorktreePath != "" {
		return "", errors.New("manage_workspace adopt_worktree accepts worktree_name or worktree_path, not both")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if session.UserID != principal.UserID || session.AccountScopeID != principal.AccountScopeID {
		return "", errors.New("manage_workspace session ownership does not match the authenticated principal")
	}
	currentPath := strings.TrimSpace(session.WorktreeRootPath)
	if expected := args.ExpectedWorktreePath; expected != "" && filepath.Clean(expected) != filepath.Clean(currentPath) {
		return "", fmt.Errorf("manage_workspace current worktree is stale: expected %q, current %q", expected, currentPath)
	}
	workspaceID := firstNonEmptyString(args.WorkspaceID, mapString(session.Metadata, "swarm_v3_source_workspace_id"))
	canonical, err := s.canonicalSessionWorkspace(principal, workspaceID, args.WorkspaceGeneration)
	if err != nil {
		return "", err
	}
	var allocation worktreeruntime.Allocation
	allocated := false
	if args.WorktreePath != "" {
		allocation, err = s.resolveOwnedSessionWorktree(session, canonical, args.WorktreePath)
	} else {
		requestedName := firstNonEmptyString(args.WorktreeName, "session-"+compactManageWorkspaceSessionID(sessionID))
		allocation, err = s.allocateManagedSessionWorktree(principal, canonical.SourceWorkspacePath, sessionID, requestedName)
		allocated = err == nil
	}
	if err != nil {
		return "", err
	}
	if currentPath != "" && filepath.Clean(currentPath) != filepath.Clean(allocation.WorkspacePath) {
		state, inspectErr := s.worktrees.InspectTaskWorkspace(currentPath)
		if inspectErr != nil || !state.Clean {
			if allocated {
				_ = s.worktrees.RollbackAllocation(allocation)
			}
			if inspectErr != nil {
				return "", fmt.Errorf("inspect current worktree before move: %w", inspectErr)
			}
			return "", errors.New("manage_workspace cannot leave a dirty worktree; commit or clean the current checkout first")
		}
	}
	if err := s.rejectSessionWorktreeOwnershipConflict(principal, sessionID, allocation.WorkspacePath); err != nil {
		if allocated {
			_ = s.worktrees.RollbackAllocation(allocation)
		}
		return "", err
	}
	next := session
	next.Metadata = cloneGenericMap(session.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	setCanonicalSessionWorkspaceMetadata(next.Metadata, canonical, pebblestore.SessionSnapshot{})
	next.WorkspacePath, next.WorkspaceName = canonical.SourceWorkspacePath, canonical.WorkspaceName
	next.WorktreeEnabled, next.WorktreeRootPath = true, allocation.WorkspacePath
	next.WorktreeBaseBranch, next.WorktreeBranch = allocation.BaseBranch, allocation.BranchName
	next.Metadata["swarm_v3_runtime_workspace_path"] = next.WorktreeRootPath
	next.Metadata["swarm_v3_mandatory_worktree"] = true
	next.Metadata["swarm_v3_worktree_owner_session_id"] = sessionID
	next.Metadata["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
	next.Metadata["base_commit"] = allocation.BaseCommit
	next.Metadata["swarm_v3_worktree_history"] = appendSessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"], canonical, allocation, sessionID)
	available := true
	grants := []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: canonical.WorkspaceID, WorkspaceGeneration: canonical.WorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.WorkspaceName, Available: &available}}
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(session) {
		if grant.Kind != pebblestore.WorkspaceGrantWorktree && grant.Kind != pebblestore.WorkspaceGrantPrimary {
			grants = append(grants, grant)
		}
	}
	grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantWorktree, WorkspaceID: canonical.WorkspaceID, WorkspaceGeneration: canonical.WorkspaceGeneration, Path: next.WorktreeRootPath, Name: canonical.WorkspaceName, Available: &available})
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants})
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration, "source_workspace_path": canonical.SourceWorkspacePath, "runtime_worktree_path": next.WorktreeRootPath, "worktree_branch": next.WorktreeBranch, "worktree_base_branch": next.WorktreeBaseBranch, "worktree_base_commit": allocation.BaseCommit, "workspace_grants": next.WorkspaceGrants, "workspace_usage": next.WorkspaceUsage, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return "", err
	}
	key := manageWorkspaceMutationKey("manage-workspace-adopt", sessionID, payload)
	result, err := applySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.worktree.adopted", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	if err != nil {
		if allocated {
			if rollbackErr := s.worktrees.RollbackAllocation(allocation); rollbackErr != nil {
				return "", errors.Join(err, rollbackErr)
			}
		}
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": "adopt_worktree", "status": "ok", "session_id": sessionID, "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration, "source_workspace_path": canonical.SourceWorkspacePath, "runtime_worktree_path": next.WorktreeRootPath, "worktree_branch": next.WorktreeBranch, "worktree_base_branch": next.WorktreeBaseBranch, "worktree_base_commit": allocation.BaseCommit, "allocated": allocated, "last_event_seq": result.LastSeq, "replayed": result.Replayed, "restart_turn": true})
}

func (s *Service) allocateManagedSessionWorktree(principal identity.Principal, workspacePath, sessionID, requestedName string) (worktreeruntime.Allocation, error) {
	config, err := s.worktrees.GetConfigForPrincipal(principal, workspacePath)
	if err != nil {
		return worktreeruntime.Allocation{}, fmt.Errorf("read worktree config: %w", err)
	}
	branchName, err := worktreeruntime.CanonicalizeRequestedWorktreeName(requestedName, config.BranchName)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	baseBranch := config.BaseBranch
	if config.UseCurrentBranch {
		baseBranch = ""
	}
	allocation, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, sessionID, baseBranch, branchName)
	if err == nil || !worktreeruntime.IsRequestedWorktreeNameConflict(err) {
		return allocation, err
	}
	retryBranch, _, retryErr := worktreeruntime.CanonicalizeRequestedWorktreeNameRetry(requestedName, config.BranchName)
	if retryErr != nil {
		return worktreeruntime.Allocation{}, retryErr
	}
	return s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, sessionID, baseBranch, retryBranch)
}

func (s *Service) resolveOwnedSessionWorktree(session pebblestore.SessionSnapshot, canonical SessionWorkspaceCanonicalization, targetPath string) (worktreeruntime.Allocation, error) {
	targetPath = filepath.Clean(targetPath)
	if !filepath.IsAbs(targetPath) {
		return worktreeruntime.Allocation{}, errors.New("manage_workspace worktree_path must be absolute")
	}
	for _, item := range sessionWorktreeHistory(session.Metadata["swarm_v3_worktree_history"]) {
		if filepath.Clean(mapString(item, "path")) != targetPath {
			continue
		}
		if mapString(item, "owner_session_id") != session.ID || mapString(item, "workspace_id") != canonical.WorkspaceID || manageWorkspaceInt64(item["workspace_generation"]) != canonical.WorkspaceGeneration {
			return worktreeruntime.Allocation{}, errors.New("manage_workspace worktree ownership or source identity is stale")
		}
		state, err := s.worktrees.InspectTaskWorkspace(targetPath)
		if err != nil || !state.Clean {
			if err != nil {
				return worktreeruntime.Allocation{}, err
			}
			return worktreeruntime.Allocation{}, errors.New("manage_workspace selected worktree is dirty")
		}
		return worktreeruntime.Allocation{WorkspacePath: targetPath, BaseBranch: mapString(item, "base_branch"), BaseCommit: mapString(item, "base_commit"), BranchName: state.BranchName, WorkspaceID: filepath.Base(targetPath)}, nil
	}
	return worktreeruntime.Allocation{}, errors.New("manage_workspace worktree_path is not owned by this session")
}

func (s *Service) rejectSessionWorktreeOwnershipConflict(principal identity.Principal, sessionID, path string) error {
	sessions, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, 10000)
	if err != nil {
		return err
	}
	path = filepath.Clean(path)
	for _, candidate := range sessions {
		if candidate.ID != sessionID && candidate.WorktreeEnabled && filepath.Clean(candidate.WorktreeRootPath) == path {
			return fmt.Errorf("manage_workspace worktree is owned by session %q", candidate.ID)
		}
	}
	return nil
}

func compactManageWorkspaceSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if len(sessionID) > 12 {
		return sessionID[:12]
	}
	if sessionID == "" {
		return "current"
	}
	return sessionID
}

func sessionWorktreeHistory(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if record, ok := item.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

func appendSessionWorktreeHistory(value any, canonical SessionWorkspaceCanonicalization, allocation worktreeruntime.Allocation, sessionID string) []any {
	path := filepath.Clean(allocation.WorkspacePath)
	out := make([]any, 0, 8)
	for _, item := range sessionWorktreeHistory(value) {
		if filepath.Clean(mapString(item, "path")) != path {
			out = append(out, item)
		}
	}
	out = append(out, map[string]any{"path": path, "branch": allocation.BranchName, "base_branch": allocation.BaseBranch, "base_commit": allocation.BaseCommit, "owner_session_id": sessionID, "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration})
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return out
}

// setDefaultWorkspace updates only the account/user current record; existing
// session snapshots remain unchanged.
func (s *Service) setDefaultWorkspace(principal identity.Principal, args manageWorkspaceArguments) (string, error) {
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" {
		return "", errors.New("manage_workspace set_default accepts one workspace_id")
	}
	if args.WorkspaceID == "" {
		return "", errors.New("manage_workspace set_default requires workspace_id")
	}
	canonical, err := s.canonicalSessionWorkspace(principal, args.WorkspaceID, args.WorkspaceGeneration)
	if err != nil {
		return "", err
	}
	if _, err := s.workspace.SelectForPrincipal(principal, canonical.SourceWorkspacePath); err != nil {
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": "set_default", "status": "ok", "workspace_id": canonical.WorkspaceID, "workspace_generation": canonical.WorkspaceGeneration, "workspace_name": canonical.WorkspaceName, "affects_later_sessions": true, "existing_sessions_changed": false})
}

// setSessionWorkspaces moves the durable primary identity through the canonical
// V3 mutation boundary while preserving account-global saved-workspace grants.
func (s *Service) setSessionWorkspaces(sessionID string, principal identity.Principal, args manageWorkspaceArguments, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" {
		return "", errors.New("manage_workspace set_session does not accept worktree selectors")
	}
	if applySessionMutation == nil {
		return "", errors.New("manage_workspace requires the canonical V3 mutation publisher")
	}
	if sessionID == "" {
		return "", errors.New("manage_workspace set_session requires an active session")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if session.UserID != principal.UserID || session.AccountScopeID != principal.AccountScopeID {
		return "", errors.New("manage_workspace session ownership does not match the authenticated principal")
	}
	primaryID := strings.TrimSpace(firstNonEmptyString(args.PrimaryWorkspaceID, args.WorkspaceID))
	ids := append([]string(nil), args.WorkspaceIDs...)
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	if primaryID == "" && len(ids) > 0 {
		primaryID = ids[0]
	}
	if primaryID == "" {
		return "", errors.New("manage_workspace set_session requires a workspace identity")
	}
	if !containsTrimmedString(ids, primaryID) {
		ids = append([]string{primaryID}, ids...)
	}
	seenIDs := map[string]struct{}{}
	uniqueIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, exists := seenIDs[id]; exists {
			continue
		}
		seenIDs[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	ids = uniqueIDs
	if len(ids) > 64 {
		return "", errors.New("manage_workspace set_session supports at most 64 workspace identities")
	}
	canonicalByID := make(map[string]SessionWorkspaceCanonicalization, len(ids))
	for _, id := range ids {
		if id == "" {
			return "", errors.New("manage_workspace set_session workspace identities must not be empty")
		}
		expected := int64(0)
		if id == firstNonEmptyString(args.WorkspaceID, args.PrimaryWorkspaceID) {
			expected = args.WorkspaceGeneration
		}
		canonical, err := s.canonicalSessionWorkspace(principal, id, expected)
		if err != nil {
			return "", err
		}
		if existing, duplicate := canonicalByID[canonical.WorkspaceID]; duplicate && existing.SourceWorkspacePath != canonical.SourceWorkspacePath {
			return "", errors.New("manage_workspace resolved duplicate workspace identity with conflicting paths")
		}
		canonicalByID[canonical.WorkspaceID] = canonical
	}
	primary, ok := canonicalByID[primaryID]
	if !ok {
		return "", errors.New("manage_workspace primary workspace was not resolved")
	}
	next := session
	next.Metadata = cloneGenericMap(session.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	setCanonicalSessionWorkspaceMetadata(next.Metadata, primary, session)
	next.WorkspaceName, next.WorkspacePath = primary.WorkspaceName, primary.SourceWorkspacePath
	if !session.WorktreeEnabled {
		next.Metadata["swarm_v3_runtime_workspace_path"] = primary.RuntimeWorkspacePath
	}
	available := true
	grants := []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: primary.WorkspaceID, WorkspaceGeneration: primary.WorkspaceGeneration, Path: primary.SourceWorkspacePath, Name: primary.WorkspaceName, Available: &available}}
	for id, canonical := range canonicalByID {
		if id != primaryID {
			grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: id, WorkspaceGeneration: canonical.WorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.WorkspaceName, Available: &available})
		}
	}
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(session) {
		if grant.Kind == pebblestore.WorkspaceGrantPrimary || grant.Kind == pebblestore.WorkspaceGrantAdditional {
			if _, selected := canonicalByID[grant.WorkspaceID]; selected {
				continue
			}
			grant.Kind = pebblestore.WorkspaceGrantAdditional
		}
		if grant.Kind == pebblestore.WorkspaceGrantWorktree {
			grant.WorkspaceID, grant.WorkspaceGeneration, grant.Name = "", 0, ""
		}
		grants = append(grants, grant)
	}
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants})
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "workspace_id": primary.WorkspaceID, "workspace_generation": primary.WorkspaceGeneration, "workspace_name": primary.WorkspaceName, "workspace_grants": next.WorkspaceGrants, "workspace_usage": next.WorkspaceUsage, "session": next, "updated_at": next.UpdatedAt})
	if err != nil {
		return "", err
	}
	key := manageWorkspaceMutationKey("manage-workspace", sessionID, payload)
	result, err := applySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.updated", EventPayload: payload, Session: &next, NowUnixMs: next.UpdatedAt})
	if err != nil {
		return "", err
	}
	return marshalManageWorkspace(map[string]any{"action": "set_session", "status": "ok", "session_id": sessionID, "workspace_id": primary.WorkspaceID, "workspace_generation": primary.WorkspaceGeneration, "workspace_name": primary.WorkspaceName, "workspace_usage": next.WorkspaceUsage, "last_event_seq": result.LastSeq, "replayed": result.Replayed, "restart_turn": true})
}

func (s *Service) canonicalSessionWorkspace(principal identity.Principal, workspaceID string, expectedGeneration int64) (SessionWorkspaceCanonicalization, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return SessionWorkspaceCanonicalization{}, errors.New("workspace_id is required")
	}
	canonical, err := s.sessionWorkspaceCanonicalize(SessionWorkspaceCanonicalizeInput{Principal: principal, WorkspaceID: workspaceID, WorkspaceGeneration: expectedGeneration})
	if err != nil {
		return SessionWorkspaceCanonicalization{}, err
	}
	if canonical.WorkspaceID != workspaceID || canonical.WorkspaceGeneration <= 0 || !strings.EqualFold(canonical.WorkspaceState, "active") || canonical.WorkspaceName == "" || canonical.SourceWorkspacePath == "" || canonical.RuntimeWorkspacePath == "" || canonical.WorkspaceBindingID == "" || canonical.RuntimeSwarmID == "" || canonical.PlacementGeneration <= 0 || canonical.BindingGeneration <= 0 {
		return SessionWorkspaceCanonicalization{}, errors.New("canonical workspace routing identity is incomplete or mismatched")
	}
	if expectedGeneration > 0 && canonical.WorkspaceGeneration != expectedGeneration {
		return SessionWorkspaceCanonicalization{}, fmt.Errorf("workspace generation is stale: expected %d, current %d", expectedGeneration, canonical.WorkspaceGeneration)
	}
	return canonical, nil
}

func setCanonicalSessionWorkspaceMetadata(metadata map[string]any, canonical SessionWorkspaceCanonicalization, session pebblestore.SessionSnapshot) {
	metadata["workspace_id"] = canonical.WorkspaceID
	metadata["swarm_v3_workspace_binding_id"] = canonical.WorkspaceBindingID
	metadata["local_workspace_binding_id"] = canonical.WorkspaceBindingID
	metadata["swarm_v3_source_workspace_id"] = canonical.WorkspaceID
	metadata["swarm_v3_source_workspace_generation"] = strconv.FormatInt(canonical.WorkspaceGeneration, 10)
	metadata["swarm_v3_source_workspace_name"] = canonical.WorkspaceName
	metadata["swarm_v3_source_workspace_path"] = canonical.SourceWorkspacePath
	metadata["swarm_v3_runtime_swarm_id"] = canonical.RuntimeSwarmID
	metadata["swarm_v3_runtime_kind"] = pebblestore.TopologyRuntimeKindHost
	metadata["swarm_v3_authority_host_swarm_id"] = firstNonEmptyString(canonical.AuthorityHostSwarmID, canonical.RuntimeSwarmID)
	metadata["swarm_v3_placement_generation"] = canonical.PlacementGeneration
	metadata["swarm_v3_binding_generation"] = canonical.BindingGeneration
	if !session.WorktreeEnabled {
		metadata["swarm_v3_runtime_workspace_path"] = canonical.RuntimeWorkspacePath
	}
}

func containsTrimmedString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func marshalManageWorkspace(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["tool"] = "manage_workspace"
	payload["path_id"] = manageWorkspacePathID
	payload["details_truncated"] = false
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

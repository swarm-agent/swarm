package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// manage_workspace in the tool catalog (tool.manageWorkspaceCatalog). A
// conversation's call runs executeManageWorkspaceCall with its session and
// the person's approved arguments; a caller with no conversation (Swarm
// Control, or a linked home) runs ManageWorkspaceControl: the same catalog
// and Workspace Map functions, with no session to retarget. Both answers go
// into the catalog envelope through tool.Runtime.ManageWorkspaceCatalog.

var _ tool.WorkspaceCatalogService = (*Service)(nil)

// manageWorkspaceMachine is a manage_workspace call's machine argument.
func manageWorkspaceMachine(arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(arguments)), &args) != nil {
		return ""
	}
	machine, _ := args["machine"].(string)
	return strings.TrimSpace(machine)
}

// manageWorkspaceLocalArguments removes an empty machine argument ("",
// whitespace or null) from a manage_workspace call: the catalog schema offers
// machine on every action and an empty one means this machine, as the
// catalog handler treats it, while the local argument parser does not know
// the field. Any other machine value is left as written (a linked call, or a
// malformed one the parser refuses).
func manageWorkspaceLocalArguments(arguments string) string {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(arguments)))
	decoder.UseNumber()
	var args map[string]any
	if decoder.Decode(&args) != nil || args == nil {
		return arguments
	}
	value, present := args["machine"]
	if !present {
		return arguments
	}
	if value != nil {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) != "" {
			return arguments
		}
	}
	delete(args, "machine")
	raw, err := json.Marshal(args)
	if err != nil {
		return arguments
	}
	return string(raw)
}

// ExecuteManageWorkspace runs a conversation's manage_workspace call (the
// Orchestrator's or the Swarm agent's, after gateToolCalls) and answers in
// the catalog envelope. With machine= it runs the linked machine's own
// manage_workspace: the person approved the call as written (catalog
// changes always ask), and the linked work rule applies. approvedArguments
// is the person's approval of a change on this machine.
func (s *Service) ExecuteManageWorkspace(ctx context.Context, sessionID string, principal identity.Principal, call tool.Call, approvedArguments string, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.tools == nil {
		return "", errors.New("manage_workspace is not configured")
	}
	call.Arguments = manageWorkspaceLocalArguments(call.Arguments)
	scope := tool.WorkspaceScope{SessionID: strings.TrimSpace(sessionID), Principal: principal}
	return s.tools.ManageWorkspaceCatalog(identity.ContextWithPrincipal(ctx, principal), scope, tool.ManageWorkspaceArgs(call.Arguments), func(map[string]any) (string, error) {
		return s.executeManageWorkspaceCall(sessionID, call, approvedArguments, principal, applySessionMutation)
	})
}

// executeManageWorkspaceCall runs a conversation's manage_workspace call: a
// catalog or Workspace Map change runs only with the backend-owned approved
// arguments the person approved.
func (s *Service) executeManageWorkspaceCall(sessionID string, call tool.Call, approvedArguments string, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if manageWorkspaceMutationAction(call.Arguments) != "" {
		if strings.TrimSpace(approvedArguments) == "" {
			return "", errors.New("manage_workspace catalog mutation requires approved canonical arguments")
		}
		var permissionPayload map[string]any
		// Permission resolution returns the stored structured permission payload;
		// unwrap its backend-owned approved_arguments before execution.
		if json.Unmarshal([]byte(strings.TrimSpace(approvedArguments)), &permissionPayload) != nil {
			return "", errors.New("manage_workspace approved permission payload is invalid")
		}
		approved, ok := permissionPayload["approved_arguments"].(map[string]any)
		if !ok {
			// Older permission resolvers may already return the canonical nested
			// arguments directly; accept only when the backend scope binding exists.
			approved = permissionPayload
		}
		approvedAction := manageWorkspaceMutationAction(manageWorkspaceArgumentsJSON(approved))
		requestedAction := manageWorkspaceMutationAction(call.Arguments)
		if len(approved) == 0 || approvedAction != requestedAction {
			return "", errors.New("manage_workspace approved arguments do not match the requested action")
		}
		expectedScope := "workspace_" + manageWorkspaceMutationAction(call.Arguments)
		if requestedAction == "map_update" {
			expectedScope = "workspace_map_update"
		}
		if approvedScope := strings.TrimSpace(mapString(approved, "permission_scope")); approvedScope != expectedScope {
			return "", errors.New("manage_workspace approved permission scope is invalid")
		}
		if strings.TrimSpace(mapString(approved, "intent")) == "" {
			return "", errors.New("manage_workspace approved intent is missing")
		}
		if requestedAction == "map_update" {
			requestedArgs, parseErr := parseManageWorkspaceArguments(call.Arguments)
			if parseErr != nil {
				return "", parseErr
			}
			approvedContent, normalizeErr := pebblestore.NormalizeWorkspaceMapContent(mapString(approved, "content"))
			if normalizeErr != nil {
				return "", normalizeErr
			}
			requestedContent, normalizeErr := pebblestore.NormalizeWorkspaceMapContent(requestedArgs.Content)
			if normalizeErr != nil {
				return "", normalizeErr
			}
			if manageWorkspaceInt64(approved["expected_revision"]) != requestedArgs.ExpectedRevision || strings.TrimSpace(mapString(approved, "intent")) != requestedArgs.Intent || approvedContent != requestedContent {
				return "", errors.New("manage_workspace approved map arguments do not match the requested revision, intent, and content")
			}
		}
		rawApproved, marshalErr := json.Marshal(approved)
		if marshalErr != nil {
			return "", marshalErr
		}
		arguments = string(rawApproved)
	}
	return s.executeManageWorkspaceTool(sessionID, arguments, principal, applySessionMutation)
}

// manageWorkspaceDefaultIntent is a catalog change's intent when the call
// names none (the same text the person's approval card shows).
func manageWorkspaceDefaultIntent(action string) string {
	switch action {
	case "create":
		return "Create a saved workspace catalog entry for this existing directory."
	case "update":
		return "Update this saved workspace after a stale identity check."
	default:
		return "Unlink this saved workspace from the catalog without deleting any files."
	}
}

// ManageWorkspaceControl runs manage_workspace for a caller with no
// conversation. The caller's key level was checked for the action (that is
// the person's approval for Swarm Control); the action-bound permission
// scope is set here, never taken from the caller. Session-bound actions are
// refused: they need a conversation.
func (s *Service) ManageWorkspaceControl(_ context.Context, p identity.Principal, raw map[string]any) (string, error) {
	if s == nil || s.workspace == nil {
		return "", errors.New("manage_workspace catalog is not configured")
	}
	if !p.Valid() {
		return "", identity.ErrPrincipalRequired
	}
	call := make(map[string]any, len(raw))
	for key, value := range raw {
		if key != "machine" {
			call[key] = value
		}
	}
	if _, set := call["permission_scope"]; set {
		return "", errors.New("manage_workspace permission_scope is set by Swarm, not the caller")
	}
	action := strings.ToLower(strings.TrimSpace(mapString(call, "action")))
	switch action {
	case "create", "update", "edit", "delete":
		if action == "edit" {
			action = "update"
		}
		call["action"], call["permission_scope"] = action, "workspace_"+action
		if strings.TrimSpace(mapString(call, "intent")) == "" {
			call["intent"] = manageWorkspaceDefaultIntent(action)
		}
	case "update_map":
		call["permission_scope"] = "workspace_map_update"
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		return "", err
	}
	args, err := parseManageWorkspaceArguments(string(encoded))
	if err != nil {
		return "", err
	}
	switch args.Action {
	case "inspect", "list":
		if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet || args.Intent != "" || args.ExpectedRevision != 0 || args.ContentSet {
			return "", errors.New("manage_workspace inspect/list do not accept workspace selectors")
		}
		return s.inspectManageWorkspace(p, args.Action)
	case "inspect_map", "get_map":
		return s.readWorkspaceMap(p, args)
	case "update_map":
		return s.writeWorkspaceMap("", p, args)
	case "create":
		return s.controlCreateWorkspace(p, args)
	case "update", "delete":
		return s.controlChangeWorkspace(p, args)
	case "set_default":
		if s.sessionWorkspaceCanonicalize == nil {
			return "", errors.New("manage_workspace workspace canonicalizer is not configured")
		}
		return s.setDefaultWorkspace(p, args)
	case "add_source_media_directory", "list_source_media_directories", "remove_source_media_directory":
		return s.manageSourceMediaDirectories("", pebblestore.SessionSnapshot{}, p, args)
	}
	return "", fmt.Errorf("manage_workspace %s needs a Swarm conversation", args.Action)
}

// controlCreateWorkspace registers an existing folder for a caller with no
// conversation, with the checks swarm_add_workspace applies through the
// workspace route (absolute, outside system and Swarm storage, not nested in
// a registered workspace, not empty).
func (s *Service) controlCreateWorkspace(p identity.Principal, args manageWorkspaceArguments) (string, error) {
	if args.WorkspaceID != "" || args.WorkspaceGeneration != 0 || !args.WorkspacePathSet || args.WorkspacePath == "" {
		return "", errors.New("manage_workspace create requires workspace_path and does not accept workspace identity fields")
	}
	if args.WorkspaceNameSet && args.WorkspaceName == "" {
		return "", errors.New("manage_workspace create workspace_name must not be empty when provided")
	}
	if len(args.WorkspaceName) > 200 || len(args.ThemeID) > 128 || len([]byte(args.Intent)) > 500 {
		return "", errors.New("manage_workspace workspace_name, theme_id or intent is too long")
	}
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.ExpectedRevision != 0 || args.ContentSet {
		return "", errors.New("manage_workspace create accepts only catalog mutation fields")
	}
	created, err := s.workspace.RegisterExistingFolderForPrincipal(p, args.WorkspacePath, args.WorkspaceName, args.ThemeID)
	if err != nil {
		return "", manageWorkspaceRepositoryError(err)
	}
	return marshalManageWorkspace(map[string]any{"action": "create", "status": "ok", "intent": args.Intent, "permission_scope": args.PermissionScope, "target": manageWorkspaceResolutionTarget(created), "requested_changes": manageWorkspaceRequestedChanges(args), "safety": manageWorkspaceControlSafety(), "restart_turn": false})
}

// controlChangeWorkspace renames, moves or unlinks a catalog entry for a
// caller with no conversation, after the same identity, generation and
// state checks a conversation's change makes. No session is retargeted.
func (s *Service) controlChangeWorkspace(p identity.Principal, args manageWorkspaceArguments) (string, error) {
	action := args.Action
	if len(args.WorkspaceIDs) > 0 || args.PrimaryWorkspaceID != "" || args.WorktreeName != "" || args.WorktreePath != "" || args.ExpectedWorktreePath != "" || args.ExpectedRevision != 0 || args.ContentSet {
		return "", fmt.Errorf("manage_workspace %s accepts only catalog mutation fields", action)
	}
	if args.WorkspaceID == "" || args.WorkspaceGeneration <= 0 {
		return "", fmt.Errorf("manage_workspace %s requires workspace_id and workspace_generation", action)
	}
	if len(args.WorkspaceName) > 200 || len(args.ThemeID) > 128 || len([]byte(args.Intent)) > 500 {
		return "", errors.New("manage_workspace workspace_name, theme_id or intent is too long")
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
	target, ok, err := s.workspace.GetByWorkspaceIDForPrincipal(p, args.WorkspaceID)
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
		updated, err := s.workspace.UpdateCatalogEntryForPrincipal(p, args.WorkspaceID, args.WorkspaceGeneration, args.WorkspacePath, name, themeID)
		if err != nil {
			return "", manageWorkspaceRepositoryError(err)
		}
		resolution = updated
	} else {
		deleted, err := s.workspace.DeleteCatalogEntryForPrincipal(p, args.WorkspaceID, args.WorkspaceGeneration)
		if err != nil {
			return "", err
		}
		resolution = deleted
	}
	return marshalManageWorkspace(map[string]any{"action": action, "status": "ok", "intent": args.Intent, "permission_scope": args.PermissionScope, "target": manageWorkspaceResolutionTarget(resolution), "requested_changes": manageWorkspaceRequestedChanges(args), "safety": manageWorkspaceControlSafety(), "restart_turn": false})
}

// manageWorkspaceControlSafety is a catalog change's safety note when no
// session is retargeted.
func manageWorkspaceControlSafety() map[string]any {
	return map[string]any{"filesystem_contents_changed": false, "session_switch_required": false, "switched_before_mutation": false, "restored_after_mutation": false, "left_in_safe_workspace": false}
}

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	workspaceScopePermissionRequirement = "workspace_scope"
	workspaceScopePermissionPathID      = "permission.workspace_scope.v2"
	workspaceScopeResultPathID          = "permission.workspace_scope.result.v2"
	workspaceScopeDecisionPathID        = "permission.workspace_scope.decision.v1"

	workspaceScopeDecisionSessionAllow workspaceScopeApprovalDecision = "session_allow"
	workspaceScopeDecisionAddDir       workspaceScopeApprovalDecision = "workspace_add_dir"
)

type workspaceScopeApprovalDecision string

type workspaceScopePermissionTarget struct {
	Exists bool
	Path   string
	Name   string
}

func (s *Service) gateWorkspaceScopeCalls(
	ctx context.Context,
	sessionID,
	permissionSessionID,
	runID string,
	step int,
	sessionMode string,
	workspaceOwnerPath,
	workspaceName string,
	sandboxCtx *runSandboxContext,
	calls []tool.Call,
	emit StreamHandler,
) ([]tool.Result, []tool.Call, []int, bool, error) {
	results := make([]tool.Result, len(calls))
	approvedCalls := make([]tool.Call, 0, len(calls))
	approvedIndexes := make([]int, 0, len(calls))
	scopeChanged := false

	hostScope := tool.WorkspaceScope{
		PrimaryPath: strings.TrimSpace(sandboxCtx.OriginWorkspacePath),
		Roots:       append([]string(nil), sandboxCtx.OriginWorkspaceRoots...),
	}
	for i := range calls {
		results[i] = tool.Result{
			CallID: strings.TrimSpace(calls[i].CallID),
			Name:   strings.TrimSpace(calls[i].Name),
		}
	}

	for i, call := range calls {
		request, needsApproval, err := tool.ScopeExpansionForCall(hostScope, call)
		if err != nil {
			results[i] = workspaceScopeErrorResult(call, err)
			continue
		}
		if !needsApproval {
			approvedCalls = append(approvedCalls, call)
			approvedIndexes = append(approvedIndexes, i)
			continue
		}

		permissionResult, decision, approved, err := s.requestWorkspaceScopePermission(
			ctx,
			permissionSessionID,
			runID,
			step,
			sessionMode,
			workspaceOwnerPath,
			workspaceName,
			call,
			request,
			emit,
		)
		if err != nil {
			return nil, nil, nil, scopeChanged, err
		}
		if !approved {
			results[i] = permissionResult
			continue
		}

		changed, err := s.applyWorkspaceScopeApproval(sessionID, workspaceOwnerPath, workspaceName, runID, decision, request, sandboxCtx)
		if err != nil {
			results[i] = workspaceScopeErrorResult(call, fmt.Errorf("workspace scope approval failed: %w", err))
			continue
		}
		if changed {
			scopeChanged = true
		}
		hostScope = tool.WorkspaceScope{
			PrimaryPath: strings.TrimSpace(sandboxCtx.OriginWorkspacePath),
			Roots:       append([]string(nil), sandboxCtx.OriginWorkspaceRoots...),
		}
		approvedCalls = append(approvedCalls, call)
		approvedIndexes = append(approvedIndexes, i)
	}

	return results, approvedCalls, approvedIndexes, scopeChanged, nil
}

func (s *Service) requestWorkspaceScopePermission(
	ctx context.Context,
	permissionSessionID,
	runID string,
	step int,
	sessionMode string,
	workspaceOwnerPath,
	workspaceName string,
	call tool.Call,
	request tool.ScopeExpansionRequest,
	emit StreamHandler,
) (tool.Result, workspaceScopeApprovalDecision, bool, error) {
	result := tool.Result{
		CallID: strings.TrimSpace(call.CallID),
		Name:   strings.TrimSpace(call.Name),
	}
	if result.CallID == "" {
		result.CallID = "tool_call"
	}
	if result.Name == "" {
		result.Name = "tool"
	}
	if s == nil || s.permissions == nil {
		err := errors.New("workspace scope permission system is not configured")
		return workspaceScopeErrorResult(call, err), "", false, nil
	}

	target := s.resolveWorkspaceScopePermissionTarget(workspaceOwnerPath, workspaceName)
	waitStarted := time.Now()
	record, err := s.permissions.CreatePending(permission.CreateInput{
		SessionID:     strings.TrimSpace(permissionSessionID),
		RunID:         strings.TrimSpace(runID),
		CallID:        strings.TrimSpace(call.CallID),
		ToolName:      strings.TrimSpace(call.Name),
		ToolArguments: workspaceScopePermissionArguments(target, call, request),
		Requirement:   workspaceScopePermissionRequirement,
		Mode:          strings.TrimSpace(sessionMode),
	})
	if err != nil {
		result.Output = workspaceScopePermissionOutputPayload(false, "error", "permission request failed", "", target, call, request)
		result.Error = fmt.Sprintf("permission request failed: %v", err)
		return result, "", false, nil
	}
	if emit != nil {
		emit(StreamEvent{
			Type:       StreamEventPermissionReq,
			Step:       step,
			ToolName:   strings.TrimSpace(call.Name),
			CallID:     strings.TrimSpace(call.CallID),
			Arguments:  record.ToolArguments,
			Permission: &record,
		})
	}

	resolved, waitErr := s.permissions.WaitForResolution(ctx, record.SessionID, record.ID)
	if waitErr != nil {
		return tool.Result{}, "", false, waitErr
	}
	if emit != nil {
		emit(StreamEvent{
			Type:       StreamEventPermissionUpdate,
			Step:       step,
			ToolName:   strings.TrimSpace(call.Name),
			CallID:     strings.TrimSpace(call.CallID),
			Arguments:  record.ToolArguments,
			Permission: &resolved,
		})
	}

	result.DurationMS = time.Since(waitStarted).Milliseconds()
	switch strings.ToLower(strings.TrimSpace(resolved.Status)) {
	case pebblestore.PermissionStatusApproved:
		decision := workspaceScopeDecisionFromReason(resolved.Reason)
		if decision == workspaceScopeDecisionAddDir && !target.Exists {
			decision = workspaceScopeDecisionSessionAllow
		}
		result.Output = workspaceScopePermissionOutputPayload(true, "approved", resolved.Reason, string(decision), target, call, request)
		return result, decision, true, nil
	case pebblestore.PermissionStatusDenied:
		result.Output = workspaceScopePermissionOutputPayload(false, "denied", resolved.Reason, "", target, call, request)
		result.Error = "workspace scope permission denied"
		return result, "", false, nil
	default:
		result.Output = workspaceScopePermissionOutputPayload(false, "cancelled", resolved.Reason, "", target, call, request)
		result.Error = "workspace scope permission cancelled"
		return result, "", false, nil
	}
}

func (s *Service) applyWorkspaceScopeApproval(
	sessionID,
	workspaceOwnerPath,
	workspaceName,
	runID string,
	decision workspaceScopeApprovalDecision,
	request tool.ScopeExpansionRequest,
	sandboxCtx *runSandboxContext,
) (bool, error) {
	switch decision {
	case "", workspaceScopeDecisionSessionAllow:
		return s.applyTemporaryWorkspaceScopeAccess(sessionID, runID, request, sandboxCtx)
	case workspaceScopeDecisionAddDir:
		return s.applyPersistentWorkspaceScopeAccess(sessionID, workspaceOwnerPath, workspaceName, runID, request, sandboxCtx)
	default:
		return false, fmt.Errorf("unsupported workspace scope decision %q", strings.TrimSpace(string(decision)))
	}
}

func (s *Service) applyTemporaryWorkspaceScopeAccess(
	sessionID,
	runID string,
	request tool.ScopeExpansionRequest,
	sandboxCtx *runSandboxContext,
) (bool, error) {
	if s == nil || s.sessions == nil {
		return false, errors.New("session service is not configured")
	}
	if sandboxCtx == nil {
		return false, errors.New("sandbox context is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, errors.New("session id is required")
	}

	sessionSnapshot, _, err := s.sessions.AddTemporaryWorkspaceRoot(sessionID, request.DirectoryPath)
	if err != nil {
		return false, err
	}
	return s.syncSandboxScopeFromSession(sessionSnapshot, runID, sandboxCtx)
}

func (s *Service) applyPersistentWorkspaceScopeAccess(
	sessionID,
	workspaceOwnerPath,
	workspaceName,
	runID string,
	request tool.ScopeExpansionRequest,
	sandboxCtx *runSandboxContext,
) (bool, error) {
	if s == nil || s.workspace == nil || s.sessions == nil {
		return false, errors.New("workspace services are not configured")
	}
	if sandboxCtx == nil {
		return false, errors.New("sandbox context is required")
	}

	target := s.resolveWorkspaceScopePermissionTarget(workspaceOwnerPath, workspaceName)
	if !target.Exists || strings.TrimSpace(target.Path) == "" {
		return false, errors.New("no saved workspace is active; temporary session access is the only available action")
	}

	if _, err := s.workspace.AddDirectory(target.Path, request.DirectoryPath); err != nil {
		scope, scopeErr := s.workspace.ScopeForPath(request.DirectoryPath)
		if scopeErr != nil || !scope.Matched || strings.TrimSpace(scope.WorkspacePath) != strings.TrimSpace(target.Path) {
			return false, err
		}
	}

	sessionSnapshot, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("session %q not found", strings.TrimSpace(sessionID))
	}
	return s.syncSandboxScopeFromSession(sessionSnapshot, runID, sandboxCtx)
}

func (s *Service) syncSandboxScopeFromSession(
	sessionSnapshot pebblestore.SessionSnapshot,
	runID string,
	sandboxCtx *runSandboxContext,
) (bool, error) {
	if sandboxCtx == nil {
		return false, errors.New("sandbox context is required")
	}

	beforePrimary := strings.TrimSpace(sandboxCtx.OriginWorkspacePath)
	beforeRoots := append([]string(nil), sandboxCtx.OriginWorkspaceRoots...)

	scope, err := s.resolveRunWorkspaceScope(sessionSnapshot)
	if err != nil {
		return false, err
	}
	hostPrimary := strings.TrimSpace(scope.PrimaryPath)
	if hostPrimary == "" {
		return false, errors.New("workspace scope primary path is required")
	}
	hostRoots := append([]string(nil), scope.Roots...)
	if len(hostRoots) == 0 {
		hostRoots = []string{hostPrimary}
	}

	sandboxCtx.OriginWorkspacePath = hostPrimary
	sandboxCtx.OriginWorkspaceRoots = hostRoots
	if !sandboxCtx.Enabled {
		sandboxCtx.WorkspacePath = hostPrimary
		sandboxCtx.WorkspaceRoots = append([]string(nil), hostRoots...)
	} else {
		runtimeRoots := make([]string, 0, len(hostRoots))
		runtimePrimary := strings.TrimSpace(sandboxCtx.WorkspacePath)
		for _, root := range hostRoots {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			mirrorPath, err := ensureSandboxMirroredRoot(hostPrimary, root, runID)
			if err != nil {
				return false, err
			}
			runtimeRoots = append(runtimeRoots, mirrorPath)
			if root == hostPrimary {
				runtimePrimary = mirrorPath
			}
		}
		if runtimePrimary == "" && len(runtimeRoots) > 0 {
			runtimePrimary = runtimeRoots[0]
		}
		sandboxCtx.WorkspacePath = runtimePrimary
		sandboxCtx.WorkspaceRoots = runtimeRoots
	}

	return beforePrimary != sandboxCtx.OriginWorkspacePath || !sameTrimmedStrings(beforeRoots, sandboxCtx.OriginWorkspaceRoots), nil
}

func (s *Service) resolveWorkspaceScopePermissionTarget(workspaceOwnerPath, workspaceName string) workspaceScopePermissionTarget {
	target := workspaceScopePermissionTarget{
		Exists: false,
		Path:   strings.TrimSpace(workspaceOwnerPath),
		Name:   strings.TrimSpace(workspaceName),
	}
	if s == nil || s.workspace == nil {
		return target
	}
	scope, err := s.workspace.ScopeForPath(workspaceOwnerPath)
	if err != nil || !scope.Matched {
		return target
	}
	target.Exists = true
	target.Path = strings.TrimSpace(scope.WorkspacePath)
	target.Name = strings.TrimSpace(scope.WorkspaceName)
	if target.Name == "" {
		target.Name = strings.TrimSpace(workspaceName)
	}
	return target
}

func workspaceScopePermissionArguments(target workspaceScopePermissionTarget, call tool.Call, request tool.ScopeExpansionRequest) string {
	accessLabel := workspaceScopeAccessLabel(call.Name)
	temporaryBehavior := fmt.Sprintf("Approving this allows %s to %s for this chat session only. It does not save or change the workspace.", accessLabel, strings.TrimSpace(request.DirectoryPath))
	workspaceBehavior := "No saved workspace is active for this session, so permanent add-dir access is not available here."
	if target.Exists {
		workspaceLabel := emptyWorkspaceScopeName(target.Name, target.Path)
		workspaceBehavior = fmt.Sprintf("You can instead add %s to workspace %q. That updates the saved workspace so future access inside that workspace stops asking for permission.", strings.TrimSpace(request.DirectoryPath), workspaceLabel)
	}
	payload := map[string]any{
		"path_id": workspaceScopePermissionPathID,
		"title":   fmt.Sprintf("Allow %s outside the current workspace?", accessLabel),
		"summary": workspaceScopePermissionSummary(accessLabel, target.Exists),
		"tool": map[string]any{
			"name":          strings.TrimSpace(call.Name),
			"argument_name": strings.TrimSpace(request.ArgumentName),
			"arguments":     decodeWorkspaceScopeToolArguments(call.Arguments),
		},
		"request": map[string]any{
			"requested_path":       strings.TrimSpace(request.RequestedPath),
			"resolved_target_path": strings.TrimSpace(request.TargetPath),
			"directory_path":       strings.TrimSpace(request.DirectoryPath),
			"access_label":         accessLabel,
			"temporary_behavior":   temporaryBehavior,
		},
		"workspace": map[string]any{
			"exists":              target.Exists,
			"path":                strings.TrimSpace(target.Path),
			"name":                strings.TrimSpace(target.Name),
			"persistent_behavior": workspaceBehavior,
		},
		"actions": map[string]any{
			"session_allow": map[string]any{
				"decision":    string(workspaceScopeDecisionSessionAllow),
				"label":       "Allow This Session",
				"description": temporaryBehavior,
			},
			"workspace_add_dir": map[string]any{
				"decision":    string(workspaceScopeDecisionAddDir),
				"available":   target.Exists,
				"label":       "Add To Workspace",
				"description": workspaceBehavior,
			},
		},
		"details_truncated": false,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"path_id":"%s","summary":"workspace scope approval requested"}`, workspaceScopePermissionPathID)
	}
	return string(encoded)
}

func workspaceScopePermissionOutputPayload(approved bool, status, reason, decision string, target workspaceScopePermissionTarget, call tool.Call, request tool.ScopeExpansionRequest) string {
	payload := map[string]any{
		"path_id": workspaceScopeResultPathID,
		"permission": map[string]any{
			"approved": approved,
			"status":   strings.TrimSpace(status),
			"reason":   strings.TrimSpace(reason),
			"decision": strings.TrimSpace(decision),
		},
		"workspace": map[string]any{
			"exists":          target.Exists,
			"path":            strings.TrimSpace(target.Path),
			"name":            strings.TrimSpace(target.Name),
			"requested_scope": strings.TrimSpace(request.DirectoryPath),
		},
		"tool": map[string]any{
			"name":      strings.TrimSpace(call.Name),
			"arguments": decodeWorkspaceScopeToolArguments(call.Arguments),
		},
		"request": map[string]any{
			"requested_path":       strings.TrimSpace(request.RequestedPath),
			"resolved_target_path": strings.TrimSpace(request.TargetPath),
		},
		"details_truncated": false,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return permissionOutputPayload(approved, status, reason, call.Name, call.Arguments)
	}
	return string(encoded)
}

func workspaceScopeDecisionFromReason(reason string) workspaceScopeApprovalDecision {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return workspaceScopeDecisionSessionAllow
	}
	if strings.EqualFold(trimmed, string(workspaceScopeDecisionAddDir)) {
		return workspaceScopeDecisionAddDir
	}
	if strings.EqualFold(trimmed, string(workspaceScopeDecisionSessionAllow)) {
		return workspaceScopeDecisionSessionAllow
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return workspaceScopeDecisionSessionAllow
	}
	decision := strings.ToLower(strings.TrimSpace(workspaceScopeStringValue(payload["decision"])))
	switch decision {
	case string(workspaceScopeDecisionAddDir):
		return workspaceScopeDecisionAddDir
	case string(workspaceScopeDecisionSessionAllow):
		return workspaceScopeDecisionSessionAllow
	default:
		return workspaceScopeDecisionSessionAllow
	}
}

func workspaceScopeDecisionReason(decision workspaceScopeApprovalDecision) string {
	payload := map[string]any{
		"path_id":  workspaceScopeDecisionPathID,
		"decision": strings.TrimSpace(string(decision)),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(string(decision))
	}
	return string(encoded)
}

func workspaceScopePermissionSummary(accessLabel string, hasWorkspace bool) string {
	if hasWorkspace {
		return fmt.Sprintf("This path is outside the current workspace. You can allow %s for this chat session only, or add the directory to the saved workspace permanently.", accessLabel)
	}
	return fmt.Sprintf("This path is outside the current workspace. You can allow %s for this chat session only.", accessLabel)
}

func workspaceScopeAccessLabel(toolName string) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read", "list", "search", "agentic_search":
		return "read access"
	default:
		return "access"
	}
}

func emptyWorkspaceScopeName(name, path string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	path = strings.TrimSpace(path)
	if path != "" {
		return path
	}
	return "workspace"
}

func workspaceScopeStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func decodeWorkspaceScopeToolArguments(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw
	}
	return payload
}

func workspaceScopeErrorResult(call tool.Call, err error) tool.Result {
	message := "workspace scope access failed"
	if err != nil {
		message = strings.TrimSpace(err.Error())
	}
	return tool.Result{
		CallID: strings.TrimSpace(call.CallID),
		Name:   strings.TrimSpace(call.Name),
		Output: message,
		Error:  message,
	}
}

func sameTrimmedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if strings.TrimSpace(left[i]) != strings.TrimSpace(right[i]) {
			return false
		}
	}
	return true
}

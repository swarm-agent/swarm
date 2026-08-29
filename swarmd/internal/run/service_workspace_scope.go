package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
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
	principal identity.Principal,
	workspaceCtx *runWorkspaceContext,
	calls []tool.Call,
	emit StreamHandler,
) ([]tool.Result, []tool.Call, []int, bool, int64, error) {
	results := make([]tool.Result, len(calls))
	approvedCalls := make([]tool.Call, 0, len(calls))
	approvedIndexes := make([]int, 0, len(calls))
	scopeChanged := false
	permissionWaitMS := int64(0)

	hostScope := workspaceScopeForGate(workspaceCtx, principal, sessionID)
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
			principal,
			call,
			request,
			emit,
		)
		permissionWaitMS += permissionResult.DurationMS
		if err != nil {
			return nil, nil, nil, scopeChanged, permissionWaitMS, err
		}
		if !approved {
			results[i] = permissionResult
			continue
		}

		changed, err := s.applyWorkspaceScopeApproval(sessionID, workspaceOwnerPath, workspaceName, principal, decision, request, workspaceCtx)
		if err != nil {
			results[i] = workspaceScopeErrorResult(call, fmt.Errorf("workspace scope approval failed: %w", err))
			continue
		}
		if changed {
			scopeChanged = true
		}
		hostScope = workspaceScopeForGate(workspaceCtx, principal, sessionID)
		approvedCalls = append(approvedCalls, call)
		approvedIndexes = append(approvedIndexes, i)
	}

	return results, approvedCalls, approvedIndexes, scopeChanged, permissionWaitMS, nil
}

func workspaceScopeForGate(workspaceCtx *runWorkspaceContext, principal identity.Principal, sessionID string) tool.WorkspaceScope {
	if workspaceCtx == nil {
		return tool.WorkspaceScope{Principal: principal, SessionID: strings.TrimSpace(sessionID)}
	}
	// Preserve the resolved runtime scope's authenticated read-only roots and
	// mutation limits. Rebuilding this value from only PrimaryPath and Roots
	// drops a Coder worktree's canonical linked-Git administrative root, causing
	// ordinary Git-backed read/list discovery to request workspace expansion.
	scope := workspaceCtx.Scope
	scope.PrimaryPath = strings.TrimSpace(workspaceCtx.OriginWorkspacePath)
	scope.Roots = append([]string(nil), workspaceCtx.OriginWorkspaceRoots...)
	scope.ReadOnlyRoots = append([]string(nil), workspaceCtx.Scope.ReadOnlyRoots...)
	scope.MutationScopes = append([]string(nil), workspaceCtx.Scope.MutationScopes...)
	scope.RejectScopeExpansion = workspaceCtx.Scope.RejectScopeExpansion
	scope.Principal = principal
	scope.SessionID = strings.TrimSpace(sessionID)
	return scope
}

func (s *Service) requestWorkspaceScopePermission(
	ctx context.Context,
	permissionSessionID,
	runID string,
	step int,
	sessionMode string,
	workspaceOwnerPath,
	workspaceName string,
	principal identity.Principal,
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

	target := s.resolveWorkspaceScopePermissionTarget(workspaceOwnerPath, workspaceName, principal)
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
	workspaceName string,
	principal identity.Principal,
	decision workspaceScopeApprovalDecision,
	request tool.ScopeExpansionRequest,
	workspaceCtx *runWorkspaceContext,
) (bool, error) {
	switch decision {
	case "", workspaceScopeDecisionSessionAllow:
		return s.applyTemporaryWorkspaceScopeAccess(sessionID, principal, request, workspaceCtx)
	default:
		return false, fmt.Errorf("unsupported workspace scope decision %q", strings.TrimSpace(string(decision)))
	}
}

func (s *Service) applyTemporaryWorkspaceScopeAccess(
	sessionID string,
	principal identity.Principal,
	request tool.ScopeExpansionRequest,
	workspaceCtx *runWorkspaceContext,
) (bool, error) {
	if s == nil || s.sessions == nil {
		return false, errors.New("session service is not configured")
	}
	if workspaceCtx == nil {
		return false, errors.New("workspace context is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, errors.New("session id is required")
	}

	sessionSnapshot, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("session %q not found", sessionID)
	}
	roots := append([]string(nil), sessionSnapshot.TemporaryWorkspaceRoots...)
	roots = append(roots, request.DirectoryPath)
	sessionSnapshot.TemporaryWorkspaceRoots = pebblestore.NormalizeSessionTemporaryWorkspaceRoots(sessionSnapshot.WorkspacePath, roots)
	available := true
	sessionSnapshot.WorkspaceGrants = append(sessionSnapshot.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantTemporary, Path: request.DirectoryPath, Available: &available})
	now := time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "workspace_grants": pebblestore.NormalizeSessionWorkspaceGrants(sessionSnapshot), "updated_at": now})
	if err != nil {
		return false, err
	}
	key := fmt.Sprintf("workspace-scope:%s:%x", sessionID, payload)
	result, err := s.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: pebblestore.V3SessionMutationUpdateSettings, EventType: "session.workspace_grants.updated",
		EventPayload: payload, Session: &sessionSnapshot, NowUnixMs: now,
	})
	if err != nil {
		return false, err
	}
	if result.Session != nil {
		sessionSnapshot = *result.Session
	}
	return s.syncWorkspaceScopeFromSession(sessionSnapshot, principal, workspaceCtx)
}

func (s *Service) syncWorkspaceScopeFromSession(
	sessionSnapshot pebblestore.SessionSnapshot,
	principal identity.Principal,
	workspaceCtx *runWorkspaceContext,
) (bool, error) {
	if workspaceCtx == nil {
		return false, errors.New("workspace context is required")
	}

	beforePrimary := strings.TrimSpace(workspaceCtx.OriginWorkspacePath)
	beforeRoots := append([]string(nil), workspaceCtx.OriginWorkspaceRoots...)

	scope, err := s.resolveRunWorkspaceScope(sessionSnapshot, principal)
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

	workspaceCtx.OriginWorkspacePath = hostPrimary
	workspaceCtx.OriginWorkspaceRoots = hostRoots
	workspaceCtx.WorkspacePath = hostPrimary
	workspaceCtx.WorkspaceRoots = append([]string(nil), hostRoots...)
	workspaceCtx.Scope = scope

	return beforePrimary != workspaceCtx.OriginWorkspacePath || !sameTrimmedStrings(beforeRoots, workspaceCtx.OriginWorkspaceRoots), nil
}

func (s *Service) resolveWorkspaceScopePermissionTarget(workspaceOwnerPath, workspaceName string, principal identity.Principal) workspaceScopePermissionTarget {
	target := workspaceScopePermissionTarget{
		Exists: false,
		Path:   strings.TrimSpace(workspaceOwnerPath),
		Name:   strings.TrimSpace(workspaceName),
	}
	if s == nil || s.workspace == nil {
		return target
	}
	scope, err := s.workspace.ScopeForPathForPrincipal(principal, workspaceOwnerPath)
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
			"exists": target.Exists,
			"path":   strings.TrimSpace(target.Path),
			"name":   strings.TrimSpace(target.Name),
		},
		"actions": map[string]any{
			"session_allow": map[string]any{
				"decision":    string(workspaceScopeDecisionSessionAllow),
				"label":       "Allow This Session",
				"description": temporaryBehavior,
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
	if strings.EqualFold(trimmed, string(workspaceScopeDecisionSessionAllow)) {
		return workspaceScopeDecisionSessionAllow
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return workspaceScopeDecisionSessionAllow
	}
	decision := strings.ToLower(strings.TrimSpace(workspaceScopeStringValue(payload["decision"])))
	switch decision {
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

func workspaceScopePermissionSummary(accessLabel string, _ bool) string {
	return fmt.Sprintf("This path is outside the current workspace. You can allow %s for this chat session only. For durable access, add this folder as a new workspace from the workspace picker.", accessLabel)
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

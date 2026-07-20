package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestProviderManagedV3ClientEffectsOnlyForAppliedManageMutations(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		callName  string
		result    tool.Result
		wantType  string
	}{
		{name: "manage agent hyphen alias", eventType: "session.tool.completed", callName: "manage-agent", result: tool.Result{Output: `{"status":"ok","action":"update","applied":true}`}, wantType: providerManagedClientEffectRefreshAgents},
		{name: "manage agent underscore alias", eventType: "session.tool.completed", callName: "manage_agent", result: tool.Result{Output: `{"status":"ok","action":"create_custom_tool","applied":true}`}, wantType: providerManagedClientEffectRefreshAgents},
		{name: "manage theme hyphen alias", eventType: "session.tool.completed", callName: "manage-theme", result: tool.Result{Output: `{"status":"ok","action":"set","applied":true}`}, wantType: providerManagedClientEffectRefreshThemes},
		{name: "manage theme underscore alias", eventType: "session.tool.completed", callName: "manage_theme", result: tool.Result{Output: `{"status":"ok","action":"delete","applied":true}`}, wantType: providerManagedClientEffectRefreshThemes},
		{name: "result name is normalized", eventType: "session.tool.completed", callName: "tool", result: tool.Result{Name: "manage_theme", Output: `{"status":"ok","action":"update","applied":true}`}, wantType: providerManagedClientEffectRefreshThemes},
		{name: "preview", eventType: "session.tool.completed", callName: "manage-agent", result: tool.Result{Output: `{"status":"proposed_update","action":"update"}`}},
		{name: "read action", eventType: "session.tool.completed", callName: "manage-theme", result: tool.Result{Output: `{"status":"ok","action":"inspect"}`}},
		{name: "applied without ok status", eventType: "session.tool.completed", callName: "manage-agent", result: tool.Result{Output: `{"status":"approval_required","action":"update","applied":true}`}},
		{name: "failed result", eventType: "session.tool.failed", callName: "manage-agent", result: tool.Result{Output: `{"status":"ok","action":"update","applied":true}`, Error: "write failed"}},
		{name: "cancelled result", eventType: "session.tool.cancelled", callName: "manage-theme", result: tool.Result{Output: `{"status":"ok","action":"set","applied":true}`, Error: "context canceled"}},
		{name: "unrelated tool", eventType: "session.tool.completed", callName: "manage-skill", result: tool.Result{Output: `{"status":"ok","action":"update","applied":true}`}},
		{name: "malformed output", eventType: "session.tool.completed", callName: "manage-agent", result: tool.Result{Output: "not json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effects := providerManagedV3ClientEffects(test.eventType, tool.Call{Name: test.callName}, test.result)
			if test.wantType == "" {
				if len(effects) != 0 {
					t.Fatalf("effects = %#v, want none", effects)
				}
				return
			}
			if len(effects) != 1 || effects[0].Type != test.wantType {
				t.Fatalf("effects = %#v, want one %q effect", effects, test.wantType)
			}
		})
	}
}

func TestProviderManagedV3ToolEventPayloadCarriesTypedClientEffects(t *testing.T) {
	call := tool.Call{CallID: "call-theme", Name: "manage-theme", Arguments: `{"action":"set","confirm":true}`}
	result := tool.Result{CallID: call.CallID, Name: call.Name, Output: `{"status":"ok","action":"set","applied":true}`}
	raw, err := providerManagedV3ToolEventPayload("session.tool.completed", providerToolInvokerConfig{runID: "run-theme", step: 2}, call, nil, result, 1234)
	if err != nil {
		t.Fatalf("build event payload: %v", err)
	}
	var payload struct {
		Type          string                          `json:"type"`
		Status        string                          `json:"status"`
		ClientEffects []providerManagedV3ClientEffect `json:"client_effects"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if payload.Type != "session.tool.completed" || payload.Status != "completed" {
		t.Fatalf("terminal identity = type %q status %q", payload.Type, payload.Status)
	}
	if len(payload.ClientEffects) != 1 || payload.ClientEffects[0].Type != providerManagedClientEffectRefreshThemes {
		t.Fatalf("client_effects = %#v", payload.ClientEffects)
	}
}

func TestStoreProviderManagedToolResultV3PassesClientEffectsToSessionMutation(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, _, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	var captured sessionruntime.SessionMutationInput
	config := providerToolInvokerConfig{
		sessionID:         sessionID,
		runID:             "run-agent-refresh",
		step:              3,
		providerManagedV3: true,
		applySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
			captured = input
			return sessionruntime.SessionMutationResult{}, nil
		},
	}
	call := tool.Call{CallID: "call-agent", Name: "manage_agent", Arguments: `{"action":"update","confirm":true}`}
	result := tool.Result{CallID: call.CallID, Name: call.Name, Output: `{"status":"ok","action":"update","applied":true}`}
	if err := svc.storeProviderManagedToolResultV3(config, call, nil, result); err != nil {
		t.Fatalf("store provider tool result: %v", err)
	}
	if captured.EventType != "session.tool.completed" {
		t.Fatalf("event type = %q", captured.EventType)
	}
	var payload struct {
		ClientEffects []providerManagedV3ClientEffect `json:"client_effects"`
	}
	if err := json.Unmarshal(captured.EventPayload, &payload); err != nil {
		t.Fatalf("decode captured event payload: %v", err)
	}
	if len(payload.ClientEffects) != 1 || payload.ClientEffects[0].Type != providerManagedClientEffectRefreshAgents {
		t.Fatalf("captured client effects = %#v", payload.ClientEffects)
	}
}

func TestProviderManagedV3ToolCallBypassesPermissionRequests(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("hello v3 pass-through"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-pass-through",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	args := mustProviderToolInvokerJSON(t, map[string]any{"path": "note.txt", "max_lines": 20})
	result, err := invoker.ExecuteTool(context.Background(), toolInvocation("call-read", "read", args))
	if err != nil {
		t.Fatalf("execute v3 provider tool: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result error = %q", result.Error)
	}
	if !strings.Contains(result.Output, "hello v3 pass-through") {
		t.Fatalf("result output missing file content: %s", result.Output)
	}

	pending, err := permissions.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permission records for v3 pass-through, got %#v", pending)
	}
}

func TestProviderManagedToolCallRefreshesTemporaryWorkspaceRoots(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(outsideFile, []byte("temporary scope content"), 0o644); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}

	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	permissions.SetBypassPermissions(true)
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-refresh-temporary-root",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	firstArgs := mustProviderToolInvokerJSON(t, map[string]any{"path": outsideFile, "max_lines": 20})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := invoker.ExecuteTool(firstCtx, toolInvocation("call-read-outside-first", "read", firstArgs))
		firstErrCh <- err
	}()

	var pending []pebblestore.PermissionRecord
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, err = permissions.ListPending(sessionID, 10)
		if err != nil {
			firstCancel()
			t.Fatalf("list pending permissions: %v", err)
		}
		if len(pending) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 1 {
		firstCancel()
		t.Fatalf("expected first outside read to request one pending permission, got %#v", pending)
	}
	var permissionArgs map[string]any
	if err := json.Unmarshal([]byte(pending[0].ToolArguments), &permissionArgs); err != nil {
		firstCancel()
		t.Fatalf("decode workspace scope permission arguments: %v", err)
	}
	requestPayload, ok := permissionArgs["request"].(map[string]any)
	if !ok {
		firstCancel()
		t.Fatalf("permission arguments missing request payload: %s", pending[0].ToolArguments)
	}
	if got := strings.TrimSpace(asTestString(requestPayload["directory_path"])); got != outside {
		firstCancel()
		t.Fatalf("temporary approval scope = %q, want directory %q", got, outside)
	}
	if got := strings.TrimSpace(asTestString(requestPayload["directory_path"])); got == outsideFile {
		firstCancel()
		t.Fatalf("temporary approval scope is file-scoped, want directory-scoped approval: %s", pending[0].ToolArguments)
	}
	if _, err := permissions.Resolve(sessionID, pending[0].ID, permission.ActionAllowOnce, string(workspaceScopeDecisionSessionAllow)); err != nil {
		firstCancel()
		t.Fatalf("approve temporary workspace scope permission: %v", err)
	}
	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("first provider tool execution after approval: %v", err)
		}
	case <-time.After(2 * time.Second):
		firstCancel()
		t.Fatalf("first provider tool execution did not finish after approval")
	}

	secondArgs := mustProviderToolInvokerJSON(t, map[string]any{"path": outsideFile, "max_lines": 20})
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	result, err := invoker.ExecuteTool(secondCtx, toolInvocation("call-read-outside-second", "read", secondArgs))
	if err != nil {
		t.Fatalf("execute second v3 provider tool: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result error = %q", result.Error)
	}
	if !strings.Contains(result.Output, "temporary scope content") {
		t.Fatalf("result output missing file content: %s", result.Output)
	}

	pending, err = permissions.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected refreshed session root to avoid second permission request, got %#v", pending)
	}
}

func TestProviderManagedV3ControlPlaneToolRequestsPermission(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-control-permission",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	args := mustProviderToolInvokerJSON(t, map[string]any{"question": "Continue?", "options": []string{"yes", "no"}})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := invoker.ExecuteTool(ctx, toolInvocation("call-ask", "ask_user", args))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected v3 control tool to wait on permission request until context deadline, got %v", err)
	}

	pending, listErr := permissions.ListPending(sessionID, 10)
	if listErr != nil {
		t.Fatalf("list pending permissions: %v", listErr)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending permission record for v3 control tool, got %#v", pending)
	}
}

func TestProviderManagedAskUserReasonBecomesProviderResponse(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-ask-user-response",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	})
	args := mustProviderToolInvokerJSON(t, map[string]any{
		"question": "Which sessions?",
		"options":  []string{"All 8 listed sessions", "Only active sessions"},
	})
	type execution struct {
		result provideriface.ToolExecutionResult
		err    error
	}
	executionCh := make(chan execution, 1)
	go func() {
		result, err := invoker.ExecuteTool(context.Background(), toolInvocation("call-ask-response", "ask_user", args))
		executionCh <- execution{result: result, err: err}
	}()

	var pending []pebblestore.PermissionRecord
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		pending, err = permissions.ListPending(sessionID, 10)
		if err != nil {
			t.Fatalf("list pending permissions: %v", err)
		}
		if len(pending) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending ask-user permission, got %#v", pending)
	}
	const answer = "All 8 listed sessions"
	if _, err := permissions.Resolve(sessionID, pending[0].ID, permission.ActionAllowOnce, answer); err != nil {
		t.Fatalf("resolve ask-user permission: %v", err)
	}

	select {
	case executed := <-executionCh:
		if executed.err != nil {
			t.Fatalf("execute ask-user after approval: %v", executed.err)
		}
		if !strings.Contains(executed.result.Output, `"status":"answered"`) || !strings.Contains(executed.result.Output, `"answer":"`+answer+`"`) {
			t.Fatalf("ask-user output did not capture reason-only answer: %s", executed.result.Output)
		}
		if !strings.Contains(executed.result.TextForModel, answer) || !strings.Contains(executed.result.TextForModel, `"status":"answered"`) {
			t.Fatalf("provider-facing tool text did not capture answer: %s", executed.result.TextForModel)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ask-user execution did not finish after approval")
	}

	selectionTests := []struct {
		name     string
		toolName string
		feedback PermissionFeedback
		want     string
	}{
		{name: "approved arguments take precedence", toolName: "ask_user", feedback: PermissionFeedback{Message: "reason answer", ApprovedArguments: "explicit answer"}, want: "explicit answer"},
		{name: "ask user falls back to reason", toolName: "ask-user", feedback: PermissionFeedback{Message: "reason answer", ApprovedArguments: "{}"}, want: "reason answer"},
		{name: "empty ask user approval stays empty", toolName: "ask_user", feedback: PermissionFeedback{ApprovedArguments: "{}"}, want: ""},
		{name: "other control tools ignore reason", toolName: "manage_theme", feedback: PermissionFeedback{Message: "permission note"}, want: ""},
	}
	for _, test := range selectionTests {
		t.Run(test.name, func(t *testing.T) {
			if got := providerManagedControlPlaneResponse(tool.Call{Name: test.toolName}, test.feedback); got != test.want {
				t.Fatalf("response = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProviderManagedV3BypassPermissionsAllowsControlPlaneTool(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	permissions.SetBypassPermissions(true)
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-control-bypass",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	args := mustProviderToolInvokerJSON(t, map[string]any{"question": "Continue?", "options": []string{"yes", "no"}})
	result, err := invoker.ExecuteTool(context.Background(), toolInvocation("call-ask", "ask_user", args))
	if err != nil {
		t.Fatalf("execute v3 provider control tool with bypass: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result error = %q", result.Error)
	}
	if !strings.Contains(result.Output, "approved_no_response") {
		t.Fatalf("result output missing ask-user bypass response: %s", result.Output)
	}

	pending, err := permissions.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permission records while bypass is enabled, got %#v", pending)
	}
}

func TestProviderManagedLegacyToolCallStillRequestsPermission(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-legacy-permission",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	args := mustProviderToolInvokerJSON(t, map[string]any{"question": "Continue?"})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := invoker.ExecuteTool(ctx, toolInvocation("call-ask", "ask_user", args))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected legacy invoker to wait on permission request until context deadline, got %v", err)
	}

	pending, listErr := permissions.ListPending(sessionID, 10)
	if listErr != nil {
		t.Fatalf("list pending permissions: %v", listErr)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending permission record for legacy provider tool, got %#v", pending)
	}
}

func newProviderManagedV3PermissionTestService(t testing.TB, workspace string) (*Service, string, *permission.Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Provider managed v3",
		WorkspacePath: workspace,
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "off",
		},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create session: %v", err)
	}
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, nil, nil, events)
	return svc, session.ID, permissions, cleanup
}

func providerManagedV3NoopMutation(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func toolInvocation(callID, name, arguments string) provideriface.ToolInvocation {
	return provideriface.ToolInvocation{CallID: callID, Name: name, Arguments: arguments}
}

func mustProviderToolInvokerJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(raw)
}

func asTestString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

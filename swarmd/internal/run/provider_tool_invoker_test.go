package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestValidateAskUserCallArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		wantError string
	}{
		{name: "single question accepts two choices", arguments: `{"question":"Pick one","options":["A","B"]}`},
		{name: "multi question accepts two choices each", arguments: `{"questions":[{"id":"q1","question":"First?","options":["A","B"]},{"id":"q2","question":"Second?","options":["C","D"]}]}`},
		{name: "rejects one choice", arguments: `{"question":"Pick one","options":["A"]}`, wantError: "at least two concrete choices"},
		{name: "rejects model authored custom option", arguments: `{"question":"Pick one","options":["A",{"label":"Other","value":"__custom__","allowCustom":true}]}`, wantError: "must not include a custom response option"},
		{name: "rejects reserved custom response label", arguments: `{"question":"Pick one","options":["A",{"label":"Custom response","value":"model-custom"}]}`, wantError: "must not include a custom response option"},
		{name: "rejects other alias", arguments: `{"question":"Pick one","options":["A","Other"]}`, wantError: "must not include a custom response option"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAskUserCallArguments(tt.arguments)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateAskUserCallArguments() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateAskUserCallArguments() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestNormalizeAskUserPermissionArgumentsAppendsOwnedCustomResponse(t *testing.T) {
	normalized, err := normalizeAskUserPermissionArguments(`{"questions":[{"id":"q1","question":"First?","options":["A","B"]},{"id":"q2","question":"Second?","options":["C","D"]}]}`)
	if err != nil {
		t.Fatalf("normalizeAskUserPermissionArguments() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		t.Fatalf("decode normalized arguments: %v", err)
	}
	questions, _ := payload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("questions = %#v", questions)
	}
	for index, rawQuestion := range questions {
		question, _ := rawQuestion.(map[string]any)
		options, _ := question["options"].([]any)
		if len(options) != 3 {
			t.Fatalf("question %d options = %#v", index+1, options)
		}
		custom, _ := options[2].(map[string]any)
		if custom["label"] != askUserCustomResponseLabel || custom["value"] != askUserCustomResponseValue || custom["allow_custom"] != true {
			t.Fatalf("question %d custom response = %#v", index+1, custom)
		}
	}
}

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

func TestStoreProviderManagedWebResultV3BoundsSessionSearchIndexContent(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, _, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()

	for _, toolName := range []string{"websearch", "webfetch"} {
		t.Run(toolName, func(t *testing.T) {
			var captured sessionruntime.SessionMutationInput
			config := providerToolInvokerConfig{
				sessionID:         sessionID,
				runID:             "run-" + toolName,
				step:              1,
				providerManagedV3: true,
				applySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
					captured = input
					return sessionruntime.SessionMutationResult{}, nil
				},
			}
			call := tool.Call{CallID: "call-" + toolName, Name: toolName, Arguments: `{}`}
			result := tool.Result{CallID: call.CallID, Name: call.Name, Output: `{"summary":"bounded web result","results":[{"title":"unique-index-token","text":"large page content"}]}`}
			if err := svc.storeProviderManagedToolResultV3(config, call, nil, result); err != nil {
				t.Fatalf("store provider web result: %v", err)
			}
			if captured.Message == nil {
				t.Fatal("captured message is nil")
			}
			bounded, _ := captured.Message.Metadata["search_index_content"].(string)
			if bounded == "" {
				t.Fatal("search_index_content is empty")
			}
			if strings.Contains(bounded, "unique-index-token") || strings.Contains(bounded, "large page content") {
				t.Fatalf("search index content retained raw result payload: %q", bounded)
			}
			if !strings.Contains(captured.Message.Content, "unique-index-token") {
				t.Fatal("durable tool message lost the full web result")
			}
			var eventPayload map[string]any
			if err := json.Unmarshal(captured.EventPayload, &eventPayload); err != nil {
				t.Fatalf("decode event payload: %v", err)
			}
			rawOutput, _ := eventPayload["raw_output"].(string)
			if !strings.Contains(rawOutput, "unique-index-token") {
				t.Fatal("realtime event lost the full web result")
			}
			completedOutput, _ := eventPayload["output"].(string)
			if strings.Contains(completedOutput, "unique-index-token") || !strings.Contains(completedOutput, `"result_details_omitted":true`) {
				t.Fatalf("realtime completion payload was not compacted: %q", completedOutput)
			}
		})
	}
}

func TestResolveProviderMediaWorkspacePathAllowsContainedFileAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "image.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceCtx := runWorkspaceContext{WorkspacePath: root, WorkspaceRoots: []string{root}}
	resolved, err := resolveProviderMediaWorkspacePath(workspaceCtx, "image.png")
	if err != nil || resolved != imagePath {
		t.Fatalf("resolved path = %q err=%v", resolved, err)
	}
	if _, err := resolveProviderMediaWorkspacePath(workspaceCtx, filepath.Join("..", "outside.png")); err == nil {
		t.Fatal("workspace media path escape was accepted")
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

func TestProviderManagedChatUpgradedVideoStudioImageGeneration(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, _, cleanup := newProviderManagedV3PermissionTestServiceWithMetadata(t, workspace, map[string]any{
		"experience":    "video_studio",
		"launch_source": "chat_upgrade",
		"lineage_kind":  "video_project",
	})
	defer cleanup()
	if !svc.providerManagedVideoStudioImageGeneration(providerToolInvokerConfig{sessionID: sessionID, providerManagedV3: true}) {
		t.Fatal("expected chat-upgraded Video Studio session to authorize still generation")
	}
}

func TestProviderManagedVideoStudioImageGenerationSkipsDuplicatePermissionPrompt(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestServiceWithMetadata(t, workspace, map[string]any{
		"experience":    "video_studio",
		"launch_source": "video_tool",
		"lineage_kind":  "video_project",
	})
	defer cleanup()

	config := providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-video-studio-image",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
	}
	if !svc.providerManagedVideoStudioImageGeneration(config) {
		t.Fatal("expected code-owned Video Studio session to authorize still generation")
	}

	invoker := svc.newProviderToolInvoker(config)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := invoker.ExecuteTool(ctx, toolInvocation("call-video-still", "manage_artifact", `{"action":"generate_image","prompt":"one still"}`))
	if err != nil {
		t.Fatalf("execute Video Studio image call: %v", err)
	}
	if result.PermissionWaitMS != 0 {
		t.Fatalf("Video Studio image call permission wait = %dms, want 0", result.PermissionWaitMS)
	}
	if strings.Contains(strings.ToLower(result.Error), "permission") || strings.Contains(strings.ToLower(result.Output), "permission") {
		t.Fatalf("Video Studio image call unexpectedly stopped at the permission gate: %+v", result)
	}
	videoResult, err := invoker.ExecuteTool(ctx, toolInvocation("call-video-project", "manage_video", `{"action":"create_project","title":"Pending video"}`))
	if err != nil {
		t.Fatalf("execute Video Studio project call: %v", err)
	}
	if videoResult.PermissionWaitMS != 0 {
		t.Fatalf("Video Studio project call permission wait = %dms, want 0", videoResult.PermissionWaitMS)
	}
	if strings.Contains(strings.ToLower(videoResult.Error), "permission") || strings.Contains(strings.ToLower(videoResult.Output), "permission") {
		t.Fatalf("Video Studio project call unexpectedly stopped at the permission gate: %+v", videoResult)
	}
	pending, err := permissions.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("Video Studio still/project authorization created permission records: %#v", pending)
	}
}

func TestProviderManagedOrdinarySessionDoesNotAuthorizeImageGeneration(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, _, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	if svc.providerManagedVideoStudioImageGeneration(providerToolInvokerConfig{sessionID: sessionID, providerManagedV3: true}) {
		t.Fatal("ordinary session unexpectedly authorized image generation")
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
	type toolExecution struct {
		result provideriface.ToolExecutionResult
		err    error
	}
	firstExecutionCh := make(chan toolExecution, 1)
	go func() {
		result, err := invoker.ExecuteTool(firstCtx, toolInvocation("call-read-outside-first", "read", firstArgs))
		firstExecutionCh <- toolExecution{result: result, err: err}
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
	// Ensure the permission wait is observable independently from the fast tool.
	time.Sleep(5 * time.Millisecond)
	if _, err := permissions.Resolve(sessionID, pending[0].ID, permission.ActionAllowOnce, string(workspaceScopeDecisionSessionAllow)); err != nil {
		firstCancel()
		t.Fatalf("approve temporary workspace scope permission: %v", err)
	}
	select {
	case execution := <-firstExecutionCh:
		if execution.err != nil {
			t.Fatalf("first provider tool execution after approval: %v", execution.err)
		}
		if execution.result.PermissionWaitMS <= 0 {
			t.Fatalf("permission wait = %dms, want a separately reported positive wait", execution.result.PermissionWaitMS)
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

func TestProviderManagedMediaInspectSkipsPermissionGateButStillRunsBackendValidation(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-media-auto-allow",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		providerManagedV3:    true,
		applySessionMutation: providerManagedV3NoopMutation,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := invoker.ExecuteTool(ctx, toolInvocation("call-media", mediaInspectToolName, `{"path":"missing.png"}`))
	if err != nil {
		t.Fatalf("execute media_inspect: %v", err)
	}
	if result.Error != "media inspection runtime is not configured" {
		t.Fatalf("media_inspect backend validation error = %q", result.Error)
	}
	pending, err := permissions.ListPending(sessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("media_inspect created permission records: %#v", pending)
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

func TestProviderManagedToolCallRejectsMissingPermissionService(t *testing.T) {
	workspace := t.TempDir()
	svc := &Service{tools: tool.NewRuntime(1)}
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID: "session", permissionSessionID: "session", runID: "run", step: 1,
		sessionMode: sessionruntime.ModeAuto, workspacePath: workspace, workspaceRoots: []string{workspace},
	})
	if invoker == nil {
		t.Fatal("provider tool invoker is nil")
	}
	result, err := invoker.ExecuteTool(context.Background(), toolInvocation("call-read", "read", mustProviderToolInvokerJSON(t, map[string]any{"path": "README.md"})))
	if err != nil {
		t.Fatalf("execute provider tool: %v", err)
	}
	if result.Error != "permission service is not configured" || !strings.Contains(result.Output, `"approved":false`) {
		t.Fatalf("missing permission service result = %+v, want fail-closed rejection", result)
	}
}

func TestProviderManagedV3AskUserInputBoundaryIgnoresPermissionAllow(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-v3-control-allowed",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		applySessionMutation: providerManagedV3NoopMutation,
		providerManagedV3:    true,
		policy: &permission.Policy{Version: 1, Rules: []permission.PolicyRule{{
			Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "ask_user",
		}}},
	})
	if invoker == nil {
		t.Fatalf("provider tool invoker is nil")
	}

	args := mustProviderToolInvokerJSON(t, map[string]any{"question": "Continue?", "options": []string{"yes", "no"}})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := invoker.ExecuteTool(ctx, toolInvocation("call-ask", "ask_user", args))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ask-user to wait for input despite a broad allow rule, got %v", err)
	}
	pending, listErr := permissions.ListPending(sessionID, 10)
	if listErr != nil {
		t.Fatalf("list pending permissions: %v", listErr)
	}
	if len(pending) != 1 || canonicalToolName(pending[0].ToolName) != "ask_user" {
		t.Fatalf("expected one pending ask-user input request despite a broad allow rule, got %#v", pending)
	}
}

func TestProviderManagedV3AskUserInputBoundaryIgnoresPermissionBypass(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := invoker.ExecuteTool(ctx, toolInvocation("call-ask", "ask_user", args))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ask-user to wait for input while ordinary permissions are bypassed, got %v", err)
	}

	pending, listErr := permissions.ListPending(sessionID, 10)
	if listErr != nil {
		t.Fatalf("list pending permissions: %v", listErr)
	}
	if len(pending) != 1 || canonicalToolName(pending[0].ToolName) != "ask_user" {
		t.Fatalf("expected one pending ask-user input request while bypass is enabled, got %#v", pending)
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

func TestProviderManagedV3BashEmitsEachLineBeforeCompletion(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	permissions.SetBypassPermissions(true)

	var mu sync.Mutex
	var events []StreamEvent
	firstDelta := make(chan struct{}, 1)
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID:            sessionID,
		permissionSessionID:  sessionID,
		runID:                "run-bash-stream",
		step:                 1,
		sessionMode:          sessionruntime.ModeAuto,
		workspacePath:        workspace,
		workspaceRoots:       []string{workspace},
		workspaceOriginPath:  workspace,
		workspaceOriginRoots: []string{workspace},
		workspaceName:        "workspace",
		providerManagedV3:    true,
		applySessionMutation: providerManagedV3NoopMutation,
		emit: func(event StreamEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			if event.Type == StreamEventToolDelta {
				select {
				case firstDelta <- struct{}{}:
				default:
				}
			}
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := invoker.ExecuteTool(context.Background(), toolInvocation("call-bash-stream", "bash", `{"command":"printf '1\\n'; sleep 2; printf '2\\n'","timeout_ms":5000}`))
		done <- err
	}()

	select {
	case <-firstDelta:
	case err := <-done:
		t.Fatalf("bash completed before first streamed line: %v", err)
	case <-time.After(time.Second):
		t.Fatal("first bash line was not emitted incrementally")
	}
	if err := <-done; err != nil {
		t.Fatalf("execute streaming bash: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var deltas []string
	for _, event := range events {
		if event.Type == StreamEventToolDelta {
			deltas = append(deltas, event.Output)
		}
	}
	if len(deltas) < 2 || deltas[0] != "1\n" || deltas[1] != "2\n" {
		t.Fatalf("bash deltas = %#v, want separately streamed lines", deltas)
	}
}

func TestProviderManagedArtifactRunContextPreservesTrustedManagedDestination(t *testing.T) {
	svc := &Service{}
	trusted := &tool.ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", CollectionID: "collection-1", VariantID: "variant-1", TaskCallID: "call-1"}
	run := svc.providerManagedArtifactRunContext(providerToolInvokerConfig{sessionID: "child-1", runID: "run-1", artifactRunContext: trusted})
	if run.SessionID != "parent-1" || run.ChildSessionID != "child-1" || run.RunID != "run-1" || run.CollectionID != "collection-1" {
		t.Fatalf("trusted child artifact context = %#v", run)
	}
	contextCopy := svc.providerManagedArtifactRunContext(providerToolInvokerConfig{sessionID: "child-1", runID: "run-2", artifactRunContext: trusted})
	contextCopy.CollectionID = "mutated"
	if trusted.CollectionID != "collection-1" {
		t.Fatalf("trusted artifact context was mutated through provider copy: %#v", trusted)
	}
	redirected := svc.providerManagedArtifactRunContext(providerToolInvokerConfig{sessionID: "other-child", runID: "run-3", artifactRunContext: trusted})
	if redirected.SessionID != "__invalid_managed_artifact_context__" || redirected.CollectionID != "collection-1" {
		t.Fatalf("redirected trusted artifact context = %#v", redirected)
	}
}

func TestProviderManagedArtifactRunContextUsesTrustedRunIntentLineage(t *testing.T) {
	workspace := t.TempDir()
	svc, sessionID, _, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()

	_, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		ClientRequestID: "artifact-lineage-intent",
		IdempotencyKey:  "artifact-lineage-intent",
		PayloadHash:     "artifact-lineage-intent",
		RequestHash:     "artifact-lineage-intent",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent: &pebblestore.V3SessionRunIntent{
			RunID: "run-artifact", Status: sessionruntime.RunIntentRunning,
			PlanID: "plan-1", CheckpointID: "cp-2", AttemptID: "cp-2:attempt-3",
		},
	})
	if err != nil {
		t.Fatalf("persist run intent: %v", err)
	}

	run := svc.providerManagedArtifactRunContext(providerToolInvokerConfig{sessionID: sessionID, runID: "run-artifact"})
	if run.SessionID != sessionID || run.RunID != "run-artifact" || run.PlanID != "plan-1" || run.CheckpointID != "cp-2" || run.AttemptID != "cp-2:attempt-3" {
		t.Fatalf("artifact run context = %#v", run)
	}
}

func newProviderManagedV3PermissionTestService(t testing.TB, workspace string) (*Service, string, *permission.Service, func()) {
	return newProviderManagedV3PermissionTestServiceWithMetadata(t, workspace, nil)
}

func newProviderManagedV3PermissionTestServiceWithMetadata(t testing.TB, workspace string, metadata map[string]any) (*Service, string, *permission.Service, func()) {
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
		Metadata: metadata,
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

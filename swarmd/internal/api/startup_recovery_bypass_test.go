package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a run resumed while the daemon starts (newSessionV3Executor, built
// by NewCoreServer) authorizes its first ordinary tool call with the bypass
// state configured on the permission service before the executor was built:
// the call is approved and runs, and no pending approval is recorded.
// Threat (forge, 2026-10-29): bypass was applied after the executor resumed
// runs, so resumed runs' first bash calls became pending approvals that
// nobody answered while every caller saw the runs as running.
// Owners: newSessionV3Executor (recoverDurableRuns + refillPendingBacklog),
// the V3 provider tool invoker, run.Service and
// permission.Service.AuthorizeToolCall over a real store and the real bash
// tool. The daemon's startup order is pinned by
// runtime.TestApplyStartupPermissionPolicyBypassBeforeRecovery; this test is
// the narrowest layer that holds a durable run replayed after a restart, its
// tool call and the permission gate together.
func TestStartupRecoveredRunToolCallUsesConfiguredBypass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovered-bypass.pebble")
	const bashArgs = `{"command":"printf resumed > resumed.txt","category":"write","critical":false,"explanation":["Write a marker."]}`

	// Before the restart: the run is held behind a drain, so it is still
	// queued when the daemon stops.
	before, beforeSessions, beforePermissions, closeBefore, _ := newRoutedSessionTestServerAt(t, path, false)
	beforeProviders := registry.New()
	beforeRunner := &sessionsV3RecordingProviderRunner{id: "codex", text: "unused"}
	beforeProviders.RegisterRunner(beforeRunner)
	before.providers = beforeProviders
	before.runner = runruntime.NewService(beforeSessions, before.model, beforeProviders, tool.NewRuntime(1), beforePermissions, before.agents, nil, nil)
	beforeExec := newSessionV3Executor(before)
	beforeExec.startDelay = 0
	before.v3SessionExecutor = beforeExec
	workspace := t.TempDir()
	session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, before, "resume-bypass-create", "resume-bypass", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	if w := postBoxDrain(t, before, `{"max_seconds":3600}`); w.Code != http.StatusOK {
		t.Fatalf("drain: %d %s", w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(map[string]any{"client_request_id": "resume-bypass-message", "role": "user", "content": "check the tree"})
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/messages", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	before.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("message: %d %s", rec.Code, rec.Body.String())
	}
	runID := stableSessionsV3PrimaryRunID(session.ID, "resume-bypass-message")
	deadline := time.Now().Add(5 * time.Second)
	for {
		intent, ok, err := beforeSessions.GetSessionRunIntent(session.ID, runID)
		if err == nil && ok && intent.Status == sessionruntime.RunIntentPendingExecutor && before.ExecutionCapacity().Pending(testPrincipal().AccountScopeID) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never queued behind the drain: %+v ok=%v err=%v", intent, ok, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	closeBefore.(func())()

	// The restart: bypass is configured on the permission service before the
	// executor is built, as newDaemon now does.
	after, afterSessions, permissions, _, _ := newRoutedSessionTestServerAt(t, path, true)
	permissions.SetBypassPermissions(true)
	var (
		mu        sync.Mutex
		toolCalls int
		toolErr   error
		toolRes   provideriface.ToolExecutionResult
	)
	runner := &sessionsV3RecordingProviderRunner{id: "codex"}
	runner.handler = func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		mu.Lock()
		first := toolCalls == 0
		toolCalls++
		mu.Unlock()
		if first && req.ToolInvoker != nil {
			result, err := req.ToolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "resumed-bash", Name: "bash", Arguments: bashArgs})
			mu.Lock()
			toolRes, toolErr = result, err
			mu.Unlock()
		}
		return provideriface.Response{Text: "resumed and done", StopReason: "stop"}, nil
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	after.providers = providers
	after.runner = runruntime.NewService(afterSessions, after.model, providers, tool.NewRuntime(1), permissions, after.agents, nil, nil)
	exec := newSessionV3Executor(after)
	exec.startDelay = 0
	after.v3SessionExecutor = exec

	deadline = time.Now().Add(15 * time.Second)
	for {
		intent, ok, err := afterSessions.GetSessionRunIntent(session.ID, runID)
		if err == nil && ok && intent.Status == sessionruntime.RunIntentCompleted {
			break
		}
		if err == nil && ok && (intent.Status == sessionruntime.RunIntentFailed || intent.Status == sessionruntime.RunIntentCancelled || intent.Status == sessionruntime.RunIntentInterrupted) {
			t.Fatalf("resumed run ended %s: %+v", intent.Status, intent)
		}
		if count, _ := permissions.PendingCount(session.ID); count > 0 {
			records, _ := permissions.ListPermissions(session.ID, 10)
			t.Fatalf("the resumed run's tool call is waiting on an approval: %+v", records)
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed run never completed: %+v ok=%v err=%v", intent, ok, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if toolCalls < 1 || toolErr != nil {
		t.Fatalf("tool calls %d err %v", toolCalls, toolErr)
	}
	if strings.TrimSpace(toolRes.Error) != "" {
		t.Fatalf("the resumed run's bash call failed: %+v", toolRes)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "resumed.txt")); err != nil || string(got) != "resumed" {
		t.Fatalf("bash did not run: %q %v (result %+v)", got, err, toolRes)
	}
	if count, err := permissions.PendingCount(session.ID); err != nil || count != 0 {
		t.Fatalf("pending permissions %d err %v", count, err)
	}
}

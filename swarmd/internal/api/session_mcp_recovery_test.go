package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider/claudecode"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: newSessionV3Executor must recover a durable pending intent without
// failing a Claude Code turn before the listener binds. Use a reopened store,
// real run service, Claude Code runner and shell CLI fixture. This is the
// narrowest integration layer proving recovery respects MCP readiness rather
// than merely testing a channel. Assert no early CLI dispatch, then completed
// durable intent and exactly one CLI launch after the address is published.
func TestSessionMCPRecoveredPendingRunWaitsForAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.pebble")
	before, sessions, _, closeBefore, _ := newRoutedSessionTestServerAt(t, path, false)
	workspace := t.TempDir()
	session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, before, "pending-mcp-create", "pending-mcp", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	if w := postBoxDrain(t, before, `{"max_seconds":3600}`); w.Code != http.StatusOK {
		t.Fatalf("drain: %d %s", w.Code, w.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session.ID+"/messages", strings.NewReader(`{"client_request_id":"pending-mcp-message","role":"user","content":"continue"}`))
	w := httptest.NewRecorder()
	before.Handler().ServeHTTP(w, withTestPrincipal(req))
	if w.Code != http.StatusOK {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	runID := stableSessionsV3PrimaryRunID(session.ID, "pending-mcp-message")
	if intent, ok, err := sessions.GetSessionRunIntent(session.ID, runID); err != nil || !ok || intent.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("pending intent=%+v %v %v", intent, ok, err)
	}
	closeBefore.(func())()

	after, afterSessions, permissions, _, _ := newRoutedSessionTestServerAt(t, path, true)
	store := afterSessions.Store().Underlying()
	auth := pebblestore.NewAuthStore(store)
	p := testPrincipal()
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{Provider: claudecode.ProviderID, AccountScopeID: p.AccountScopeID, Type: pebblestore.AuthTypeCLI, SetActive: true}); err != nil {
		t.Fatal(err)
	}
	scriptDir := t.TempDir()
	launches := filepath.Join(scriptDir, "launches")
	script := filepath.Join(scriptDir, "claude-fixture")
	source := "#!/bin/sh\nIFS= read -r line\nprintf 'launch\\n' >> '" + launches + "'\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"recovered-cli\"}' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"recovered\"}'\ncat >/dev/null\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudecode.CLIPathEnv, script)
	realRunner := claudecode.NewRunner(auth, claudecode.NewCLI())
	entered := make(chan struct{}, 1)
	wrapper := &sessionsV3RecordingProviderRunner{id: "codex"}
	wrapper.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		return realRunner.CreateResponseStreaming(ctx, req, onEvent)
	}
	providers := registry.New()
	providers.RegisterRunner(realRunner)
	providers.RegisterRunner(wrapper)
	after.security = security.NewService(pebblestore.NewClientAuthStore(store), nil)
	after.SetDataDir(t.TempDir())
	after.connectClaudeCodeSessionMCP(providers)
	after.providers = providers
	after.runner = runruntime.NewService(afterSessions, after.model, providers, tool.NewRuntime(1), permissions, after.agents, nil, nil)
	// Hold dispatch while publishing the fixture's executor pointer; its
	// constructor starts recovery goroutines, which read that pointer too.
	drain, err := permissions.ExecutionCapacity().BeginDrain(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after.v3SessionExecutor = newSessionV3Executor(after)
	permissions.ExecutionCapacity().CancelDrain(drain.Generation)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("recovered intent never reached runner")
	}
	select {
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(launches); !os.IsNotExist(err) {
		t.Fatalf("CLI launched before bind: %v", err)
	}
	intent, ok, err := afterSessions.GetSessionRunIntent(session.ID, runID)
	if err != nil || !ok || intent.Status == sessionruntime.RunIntentFailed {
		t.Fatalf("early intent=%+v %v %v", intent, ok, err)
	}
	after.SetProviderKeyPageAddr("127.0.0.1:7781")
	deadline := time.Now().Add(10 * time.Second)
	for {
		intent, ok, err = afterSessions.GetSessionRunIntent(session.ID, runID)
		if err == nil && ok && intent.Status == sessionruntime.RunIntentCompleted {
			break
		}
		if time.Now().After(deadline) || intent.Status == sessionruntime.RunIntentFailed {
			t.Fatalf("recovered intent=%+v %v %v", intent, ok, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, err := os.ReadFile(launches); err != nil || string(got) != "launch\n" {
		t.Fatalf("CLI launches=%q %v", got, err)
	}
}

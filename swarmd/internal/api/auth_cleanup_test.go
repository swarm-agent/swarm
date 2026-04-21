package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	authruntime "swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestCredentialDeleteCleanupClearsSessionPreferencesForDeletedProvider(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth-delete-cleanup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)

	authSvc := authruntime.NewService(pebblestore.NewAuthStore(store), eventLog)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}

	providers := registry.New(testProviderAdapter{status: provideriface.Status{ID: "codex", Ready: false, Reason: "auth missing"}})
	providers.RegisterRunner(testProviderRunner{id: "codex"})

	server := NewServer("test", authSvc, agentSvc, modelSvc, nil, sessionSvc, nil, nil, nil, providers, nil, nil, eventLog, hub)
	server.SetStartupConfigPath(writeAuthCleanupStartupConfig(t))

	ctx := context.Background()
	if _, _, err := authSvc.UpsertCredential(authruntime.CredentialUpsertInput{
		ID:       "cred-1",
		Provider: "codex",
		Type:     "api",
		APIKey:   "test-key",
	}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	if _, _, err := authSvc.SetActiveCredential("codex", "cred-1"); err != nil {
		t.Fatalf("set active credential: %v", err)
	}

	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-codex",
		Title:         "Codex Session",
		WorkspacePath: "/tmp/workspace",
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "high",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/credentials/delete", strings.NewReader(`{"provider":"codex","id":"cred-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Cleanup struct {
			ClearedSessionCount int      `json:"cleared_session_count"`
			ClearedSessionIDs   []string `json:"cleared_session_ids"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Cleanup.ClearedSessionCount != 1 {
		t.Fatalf("cleared session count = %d, want 1", resp.Cleanup.ClearedSessionCount)
	}
	if len(resp.Cleanup.ClearedSessionIDs) != 1 || resp.Cleanup.ClearedSessionIDs[0] != session.ID {
		t.Fatalf("cleared session ids = %#v, want [%q]", resp.Cleanup.ClearedSessionIDs, session.ID)
	}

	pref, err := sessionSvc.GetSessionPreference(session.ID)
	if err != nil {
		t.Fatalf("GetSessionPreference() error = %v", err)
	}
	if pref.Provider != "" || pref.Model != "" || pref.Thinking != "" {
		t.Fatalf("session preference not cleared: %#v", pref)
	}

	_ = ctx
}

type testProviderAdapter struct {
	status provideriface.Status
}

func (a testProviderAdapter) ID() string { return a.status.ID }
func (a testProviderAdapter) Status(context.Context) (provideriface.Status, error) {
	return a.status, nil
}

type testProviderRunner struct {
	id string
}

func (r testProviderRunner) ID() string { return r.id }
func (r testProviderRunner) CreateResponse(context.Context, provideriface.Request) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}
func (r testProviderRunner) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return provideriface.Response{}, nil
}

func writeAuthCleanupStartupConfig(t *testing.T) string {
	t.Helper()
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = false
	cfg.SwarmName = "local-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	return startupPath
}

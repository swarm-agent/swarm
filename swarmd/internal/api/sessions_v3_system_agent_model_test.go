package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

// Purpose: a top-level Coder (or Finder/Designer) session created without a
// model must run on that role's account-configured model, never with an empty
// provider/model ("resolved v3 provider/model is empty" at first run). When
// the caller names a model, that model is kept, and a retry with the same
// client_request_id replays even if the role model changed in between.
// Owner: createSessionsV3Primary through the authenticated /v3/sessions
// handler, the narrowest layer where the request, the agent resolution and the
// stored snapshot meet.
func TestSessionsV3TopLevelSystemAgentUsesRoleModel(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "role-model.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := newSystemAgentModelTestServer(t, store)
	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	record := testAgentModelSettingsRecord(testPrincipal().AccountScopeID)
	if _, err := settingsStore.PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore), settingsStore)

	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	create := func(clientRequestID string, preference map[string]any) pebblestore.SessionSnapshot {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"client_request_id":    clientRequestID,
			"workspace_path":       workspace,
			"workspace_name":       filepath.Base(workspace),
			"swarm_id":             "host-swarm-id",
			"workspace_binding_id": bindingID,
			"target_kind":          "host",
			"target_relationship":  "self",
			"agent_name":           "system-coder",
			"preference":           preference,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var created struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.Session
	}

	coder := create("coder-role-model", nil)
	if coder.Preference.Provider != "codex" || coder.Preference.Model != "coder" {
		t.Fatalf("coder session preference = %+v, want the Coder role model codex/coder", coder.Preference)
	}
	if _, alerted := coder.Metadata["model_alert"]; alerted {
		t.Fatalf("configured role model raised an alert: %v", coder.Metadata["model_alert"])
	}

	// A retry replays the same session even after the role model changes: the
	// server-resolved model is not part of the request.
	record.SystemAgents.Coder.Model = "coder-v2"
	record.UpdatedAt++
	if _, err := settingsStore.PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	if replay := create("coder-role-model", nil); replay.ID != coder.ID || replay.Preference.Model != "coder" {
		t.Fatalf("retry = %s/%+v, want replay of %s on codex/coder", replay.ID, replay.Preference, coder.ID)
	}

	// A model the caller chose is kept, never replaced by the role default.
	chosen := create("coder-chosen-model", map[string]any{"provider": "codex", "model": "chosen", "thinking": "low"})
	if chosen.Preference.Model != "chosen" || chosen.Preference.Thinking != "low" {
		t.Fatalf("explicit preference = %+v, want codex/chosen/low", chosen.Preference)
	}
}

// newSystemAgentModelTestServer wires the real session, agent, model, run and
// topology services over store, with compiled agent defaults only.
func newSystemAgentModelTestServer(t *testing.T, store *pebblestore.Store) *Server {
	t.Helper()
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	providers := registry.New()
	providers.RegisterRunner(&sessionsV3RecordingProviderRunner{text: "ok"})
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), permissionSvc, agentSvc, nil, nil)
	server := NewServer(nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
	server.SetSwarmStore(swarmStore)
	server.v3SessionExecutor = nil
	return server
}

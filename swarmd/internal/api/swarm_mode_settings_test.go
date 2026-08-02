package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAgentModelSettingsHTTPPatchAndGetUnifiedRecord(t *testing.T) {
	server, principal := openAgentModelSettingsHTTPTest(t)
	swarm := `{"swarm":{"action":{"provider":"codex","model":"action-next","thinking":"high","service_tier":"fast","context_mode":"full"},"plan":{"provider":"openai","model":"plan-next","thinking":"xhigh","service_tier":"priority","context_mode":"compact"}}}`
	patched := agentModelSettingsHTTP(t, server, principal, http.MethodPatch, swarm, http.StatusOK)
	assertAgentModelSettingsHTTPResponse(t, patched, "action-next", "plan-next", "compact")

	systemAgent := `{"system_agents":{"coder":{"provider":"codex","model":"coder-next","thinking":"high","service_tier":"fast"}}}`
	patched = agentModelSettingsHTTP(t, server, principal, http.MethodPatch, systemAgent, http.StatusOK)
	assertAgentModelSettingsHTTPResponse(t, patched, "action-next", "plan-next", "compact")
	settings := patched["agent_model_settings"].(map[string]any)
	coder := settings["system_agents"].(map[string]any)["coder"].(map[string]any)
	if coder["model"] != "coder-next" {
		t.Fatalf("coder = %#v", coder)
	}

	got := agentModelSettingsHTTP(t, server, principal, http.MethodGet, "", http.StatusOK)
	assertAgentModelSettingsHTTPResponse(t, got, "action-next", "plan-next", "compact")
}

func TestAgentModelSettingsHTTPRejectsInvalidTargetedPatches(t *testing.T) {
	server, principal := openAgentModelSettingsHTTPTest(t)
	for _, body := range []string{
		`{}`,
		`{"unknown":true}`,
		`{"swarm":{"action":{"provider":"codex","model":"action","thinking":"high"}}}`,
		`{"swarm":{"action":{"provider":"codex","model":"action","thinking":"high"},"plan":{"provider":"codex","model":"plan","thinking":"high"}},"system_agents":{"coder":{"provider":"codex","model":"coder","thinking":"high"}}}`,
		`{"system_agents":{}}`,
		`{"system_agents":{"coder":{"provider":"codex","model":"coder","thinking":"high"},"finder":{"provider":"codex","model":"finder","thinking":"medium"}}}`,
		`{"system_agents":{"unknown":{"provider":"codex","model":"model","thinking":"high"}}}`,
	} {
		agentModelSettingsHTTP(t, server, principal, http.MethodPatch, body, http.StatusBadRequest)
	}
}

func TestAgentModelSettingsHTTPMissingRecordRequiresPrincipalAndSupportedMethod(t *testing.T) {
	server, principal := openAgentModelSettingsHTTPTest(t)
	emptyStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "empty-agent-model-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = emptyStore.Close() })
	emptyServer := &Server{}
	emptyServer.SetAgentModelSettingsService(agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(emptyStore)))
	agentModelSettingsHTTP(t, emptyServer, principal, http.MethodGet, "", http.StatusNotFound)
	agentModelSettingsHTTP(t, server, identity.Principal{}, http.MethodGet, "", http.StatusUnauthorized)
	agentModelSettingsHTTP(t, server, principal, http.MethodPut, `{}`, http.StatusMethodNotAllowed)
}

func openAgentModelSettingsHTTPTest(t *testing.T) (*Server, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-model-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settingsStore := pebblestore.NewAgentModelSettingsStore(store)
	if _, err := settingsStore.PutForAccount(testAgentModelSettingsRecord("account-one")); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(settingsStore))
	return server, identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
}

func agentModelSettingsHTTP(t *testing.T, server *Server, principal identity.Principal, method, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, AgentModelSettingsPath, bytes.NewBufferString(body))
	if principal.Valid() {
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	}
	res := httptest.NewRecorder()
	server.handleAgentModelSettings(res, req)
	if res.Code != want {
		t.Fatalf("%s = %d, want %d: %s", method, res.Code, want, res.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertAgentModelSettingsHTTPResponse(t *testing.T, out map[string]any, actionModel, planModel, compactModel string) {
	t.Helper()
	settings := out["agent_model_settings"].(map[string]any)
	swarm := settings["swarm"].(map[string]any)
	action := swarm["action"].(map[string]any)
	plan := swarm["plan"].(map[string]any)
	compact := settings["system_agents"].(map[string]any)["compact"].(map[string]any)
	if action["model"] != actionModel || plan["model"] != planModel || compact["model"] != compactModel {
		t.Fatalf("agent_model_settings = %#v", settings)
	}
}

func testAgentModelSettingsRecord(accountScopeID string) pebblestore.AgentModelSettingsRecord {
	assignment := func(model string) pebblestore.AgentModelAssignment {
		return pebblestore.AgentModelAssignment{Provider: "codex", Model: model, Thinking: "high"}
	}
	return pebblestore.AgentModelSettingsRecord{
		AccountScopeID: accountScopeID,
		Swarm: pebblestore.SwarmAgentModelAssignments{Action: assignment("action"), Plan: assignment("plan")},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: assignment("compact"), Finder: assignment("finder"), Coder: assignment("coder"),
			Designer: assignment("designer"), Router: assignment("router"),
		},
		UpdatedAt: 1,
	}
}

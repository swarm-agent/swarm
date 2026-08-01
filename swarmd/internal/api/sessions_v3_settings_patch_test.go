package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PrimarySettingsPatchUpdatesAgentPreferenceWithoutChangingMode(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "settings-patch.pebble"))
	defer func() { _ = closeStore() }()
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name:                "freeform",
		Mode:                agentruntime.ModePrimary,
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}},
		Enabled:             pebblestore.BoolPtr(true),
		Prompt:              "Freeform test prompt",
	}); err != nil {
		t.Fatalf("create freeform agent: %v", err)
	}
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "settings-patch-create", "settings patch", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	body := `{"client_request_id":"settings-patch-1","if_projection_seq":1,"agent_name":"freeform","preference":{"provider":"test-provider","model":"test-model-2","thinking":"high","service_tier":"fast"}}`
	req := httptest.NewRequest(http.MethodPatch, "/v3/sessions/"+created.ID+"/settings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings patch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK             bool                                 `json:"ok"`
		SessionID      string                               `json:"session_id"`
		SessionView    sessionsV3SessionView                `json:"session_view"`
		Mutation       sessionruntime.SessionMutationResult `json:"mutation"`
		RealtimeOutbox *pebblestore.V3RealtimeOutboxRecord  `json:"realtime_outbox"`
		Session        any                                  `json:"session"`
		Messages       any                                  `json:"messages"`
		Events         any                                  `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode settings response: %v", err)
	}
	if !payload.OK || payload.SessionID != created.ID || payload.RealtimeOutbox == nil {
		t.Fatalf("settings payload = %+v", payload)
	}
	if payload.Session != nil || payload.Messages != nil || payload.Events != nil {
		t.Fatalf("settings response leaked hydrate fields: %s", rec.Body.String())
	}
	if payload.Mutation.Event.EventType != "session.settings.updated" || payload.Mutation.Projection.LastEventSeq != 2 {
		t.Fatalf("settings mutation = %+v", payload.Mutation)
	}
	settings := payload.SessionView.AgenticSettings
	if settings.Mode != sessionruntime.ModeAuto || settings.AgentName != "freeform" || settings.EffectivePreference.Model != "test-model-2" || settings.EffectivePreference.Thinking != "high" || settings.ProjectionSeq != 2 {
		t.Fatalf("agentic settings = %+v", settings)
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get stored session ok=%v err=%v", ok, err)
	}
	if stored.Mode != sessionruntime.ModeAuto || stored.Metadata["agent_name"] != "freeform" || stored.Preference.Model != "test-model-2" || stored.Preference.Thinking != "high" {
		t.Fatalf("stored settings = %+v pref=%+v", stored.Metadata, stored.Preference)
	}
}

func TestSessionsV3PrimarySettingsPatchRejectsGenericMode(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "settings-mode-rejected.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "settings-mode-rejected-create", "settings mode rejected", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	req := httptest.NewRequest(http.MethodPatch, "/v3/sessions/"+created.ID+"/settings", bytes.NewBufferString(`{"client_request_id":"settings-mode-rejected-1","mode":"plan"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("settings mode status = %d body=%s", rec.Code, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || stored.Mode != sessionruntime.ModeAuto {
		t.Fatalf("stored mode changed: mode=%q ok=%t err=%v", stored.Mode, ok, err)
	}
}

func TestSessionsV3PrimaryModeSubresourceIsRetired(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "mode-subresource-retired.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "mode-subresource-retired-create", "mode subresource retired", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/mode", bytes.NewBufferString(`{"client_request_id":"mode-retired-1","mode":"plan"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown sessions v3 path") {
		t.Fatalf("mode subresource status = %d body=%s", rec.Code, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || stored.Mode != sessionruntime.ModeAuto {
		t.Fatalf("stored mode changed: mode=%q ok=%t err=%v", stored.Mode, ok, err)
	}
}

func TestSessionsV3PrimarySettingsPatchRejectsRemovedModelFields(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "settings-removed-model-fields.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "settings-removed-model-fields-create", "settings removed model fields", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	for _, field := range []string{"model_mode", "single", "plan", "auto"} {
		t.Run(field, func(t *testing.T) {
			body := `{"client_request_id":"settings-removed-` + field + `","` + field + `":{}}`
			req := httptest.NewRequest(http.MethodPatch, "/v3/sessions/"+created.ID+"/settings", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
				t.Fatalf("removed field %q status=%d body=%s", field, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSessionsV3PrimarySettingsPatchRejectsStaleProjectionSeq(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "settings-conflict.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "settings-conflict-create", "settings conflict", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	req := httptest.NewRequest(http.MethodPatch, "/v3/sessions/"+created.ID+"/settings", bytes.NewBufferString(`{"client_request_id":"settings-conflict-1","if_projection_seq":0,"preference":{"thinking":"high"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "projection conflict") {
		t.Fatalf("settings conflict status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsV3PrimarySettingsPatchRejectsPreferenceWhenAgentModelLocked(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "settings-locked.pebble"))
	defer func() { _ = closeStore() }()
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name:                "locked",
		Mode:                agentruntime.ModePrimary,
		Provider:            "test-provider",
		Model:               "locked-model",
		Thinking:            "high",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}},
		Enabled:             pebblestore.BoolPtr(true),
		Prompt:              "Locked model agent",
	}); err != nil {
		t.Fatalf("create locked agent: %v", err)
	}
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "settings-locked-create", "settings locked", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	req := httptest.NewRequest(http.MethodPatch, "/v3/sessions/"+created.ID+"/settings", bytes.NewBufferString(`{"client_request_id":"settings-locked-1","agent_name":"locked","preference":{"provider":"test-provider","model":"other-model"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Default") {
		t.Fatalf("settings locked status = %d body=%s", rec.Code, rec.Body.String())
	}
}

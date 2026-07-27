package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PrimaryHydrateIncludesActiveRunIntentFromDurableStore(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	created := createSessionsV3PrimaryResourceTestSession(t, server, "v3-active-run", "V3 Active Run")

	pending, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:      created.ID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "v3-active-run-pending",
		PayloadHash:    "hash-v3-active-run-pending",
		Kind:           sessionruntime.SessionMutationAppendMessage,
		Message:        &pebblestore.MessageSnapshot{Role: "user", Content: "hydrate durable run"},
		RunIntent:      &pebblestore.V3SessionRunIntent{RunID: "run-active", Status: sessionruntime.RunIntentPendingExecutor},
		NowUnixMs:      1000,
	})
	if err != nil || pending.RunIntent == nil {
		t.Fatalf("record pending run intent: result=%+v err=%v", pending, err)
	}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:      created.ID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "v3-active-run-running",
		PayloadHash:    "hash-v3-active-run-running",
		Kind:           sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:      &pebblestore.V3SessionRunIntent{RunID: "run-active", Status: sessionruntime.RunIntentRunning},
		NowUnixMs:      3000,
	}); err != nil {
		t.Fatalf("record running run intent: %v", err)
	}

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK              bool                            `json:"ok"`
		ActiveRunIntent *pebblestore.V3SessionRunIntent `json:"active_run_intent"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || hydrated.ActiveRunIntent == nil || hydrated.ActiveRunIntent.RunID != "run-active" || hydrated.ActiveRunIntent.Status != sessionruntime.RunIntentRunning || hydrated.ActiveRunIntent.CreatedAt != 1000 || hydrated.ActiveRunIntent.UpdatedAt != 3000 {
		t.Fatalf("active run intent = %+v", hydrated.ActiveRunIntent)
	}
}

func TestSessionsV3PrimaryHydrateIgnoresLifecycleActiveWithoutCanonicalRunState(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryResourceTestSession(t, server, "hydrate-lifecycle-only", "Hydrate Lifecycle Only")
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, true)

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK              bool                            `json:"ok"`
		ActiveRunIntent *pebblestore.V3SessionRunIntent `json:"active_run_intent"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || hydrated.ActiveRunIntent != nil {
		t.Fatalf("hydrate active_run_intent = %+v, want nil for lifecycle-only active", hydrated.ActiveRunIntent)
	}
}

func TestSessionsV3PrimaryHydrateKeepsCanonicalActiveWithInactiveLifecycle(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryResourceTestSession(t, server, "hydrate-stale-lifecycle", "Hydrate Stale Lifecycle")
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:      created.ID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "hydrate-stale-lifecycle-pending",
		PayloadHash:    "hash-hydrate-stale-lifecycle-pending",
		Kind:           sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:      &pebblestore.V3SessionRunIntent{RunID: "run-canonical", Status: sessionruntime.RunIntentPendingExecutor},
		NowUnixMs:      1000,
	}); err != nil {
		t.Fatalf("record pending run intent: %v", err)
	}
	if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:      created.ID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: "hydrate-stale-lifecycle-running",
		PayloadHash:    "hash-hydrate-stale-lifecycle-running",
		Kind:           sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:      &pebblestore.V3SessionRunIntent{RunID: "run-canonical", Status: sessionruntime.RunIntentRunning},
		NowUnixMs:      2000,
	}); err != nil {
		t.Fatalf("record running run intent: %v", err)
	}
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, false)

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK              bool                            `json:"ok"`
		ActiveRunIntent *pebblestore.V3SessionRunIntent `json:"active_run_intent"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || hydrated.ActiveRunIntent == nil || hydrated.ActiveRunIntent.RunID != "run-canonical" || hydrated.ActiveRunIntent.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("hydrate canonical active_run_intent = %+v", hydrated.ActiveRunIntent)
	}
}

func TestSessionsV3PrimaryHydrateIncludesPermissionsAndUsage(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	created := struct {
		Session pebblestore.SessionSnapshot
	}{Session: createSessionsV3PrimaryResourceTestSession(t, server, "session-v3-resources", "V3 Resources")}

	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: created.Session.ID, RunID: "run-v3", CallID: "call-v3", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}
	_, summary, _, err := sessionSvc.RecordTurnUsage(created.Session.ID, pebblestore.SessionTurnUsageSnapshot{RunID: "run-v3", Provider: "codex", Model: "gpt-5.4", Source: "provider", ContextWindow: 1000, InputTokens: 20, OutputTokens: 5, TotalTokens: 25})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK                 bool                             `json:"ok"`
		PendingPermissions []pebblestore.PermissionRecord   `json:"pending_permissions"`
		UsageSummary       *pebblestore.SessionUsageSummary `json:"usage_summary"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || len(hydrated.PendingPermissions) != 1 || hydrated.PendingPermissions[0].ID != pending.ID {
		t.Fatalf("pending permissions = %+v", hydrated.PendingPermissions)
	}
	if hydrated.UsageSummary == nil || hydrated.UsageSummary.SessionID != created.Session.ID || hydrated.UsageSummary.TotalTokens != summary.TotalTokens || hydrated.UsageSummary.RemainingTokens != summary.RemainingTokens {
		t.Fatalf("usage summary = %+v want %+v", hydrated.UsageSummary, summary)
	}
}

func createSessionsV3PrimaryResourceTestSession(t *testing.T, server *Server, sessionID, title string) pebblestore.SessionSnapshot {
	t.Helper()
	now := int64(1000)
	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/workspace/v3",
		WorkspaceName:  "v3",
		Title:          title,
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:      sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		IdempotencyKey: sessionID + "-create",
		PayloadHash:    sessionID + "-hash",
		Kind:           sessionruntime.SessionMutationCreateSession,
		Session:        &session,
		NowUnixMs:      now,
	}); err != nil {
		t.Fatalf("create v3 session: %v", err)
	}
	return session
}

func TestSessionsV3PrimaryPermissionsListAndResolveUseV3Path(t *testing.T) {
	server, _, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	created := struct {
		Session pebblestore.SessionSnapshot
	}{Session: createSessionsV3PrimaryResourceTestSession(t, server, "session-v3-permission", "V3 Permission")}
	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: created.Session.ID, RunID: "run-v3", CallID: "call-v3", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID+"/permissions?status=pending&limit=20", nil)
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, withTestPrincipal(listReq))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list permissions status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	var listed struct {
		Permissions []pebblestore.PermissionRecord `json:"permissions"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.Permissions) != 1 || listed.Permissions[0].ID != pending.ID {
		t.Fatalf("listed permissions = %+v", listed.Permissions)
	}

	resolveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/permissions/"+pending.ID+"/resolve", bytes.NewBufferString(`{"action":"approve","reason":"v3"}`))
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(resolveRec, withTestPrincipal(resolveReq))
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d, body=%s", resolveRec.Code, http.StatusOK, resolveRec.Body.String())
	}
	var resolved struct {
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolved); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolved.Permission.ID != pending.ID || resolved.Permission.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("resolved permission = %+v", resolved.Permission)
	}
}

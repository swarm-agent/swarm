package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionsV3SyncBootstrapPermissionSummariesIncludePendingOutsideRecentLimit(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-sync-summary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permSvc.SetSessionResolver(sessionSvc)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, permSvc, nil, eventLog, stream.NewHub(eventLog))

	principal := testPrincipal()
	pendingSession, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Needs approval outside recent limit",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace-a",
		Preference:     &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create pending session: %v", err)
	}
	pendingSession.UpdatedAt = 1
	if err := sessionSvc.Store().UpdateSession(pendingSession); err != nil {
		t.Fatalf("age pending session: %v", err)
	}
	if _, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     pendingSession.ID,
		RunID:         "run-pending",
		CallID:        "call-pending",
		ToolName:      "bash",
		ToolArguments: "{}",
		Requirement:   "bash",
		Mode:          sessionruntime.ModeAuto,
	}); err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	recentSession, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Recent without approvals",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace-b",
		Preference:     &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		t.Fatalf("create recent session: %v", err)
	}

	body := []byte(`{"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":1},"attention":{"pending_permissions":true}},"history":{"mode":"none"},"resources":{"current_run_state":true,"permission_summaries":true},"include_active":true}`)
	req := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sync/bootstrap", bytes.NewReader(body)))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		SessionsByID                 map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		PermissionSummariesBySession map[string]sessionsV3PermissionSummary `json:"permission_summaries_by_session"`
		SessionViewsByID             map[string]sessionsV3SessionView       `json:"session_views_by_id"`
		SyncScope                    map[string]string                      `json:"sync_scope"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if _, ok := payload.SessionsByID[recentSession.ID]; !ok {
		t.Fatalf("recent session missing from bootstrap sessions: %+v", payload.SessionsByID)
	}
	if _, ok := payload.SessionsByID[pendingSession.ID]; !ok {
		t.Fatalf("pending attention session outside recent limit missing from bootstrap sessions: %+v", payload.SessionsByID)
	}
	summary, ok := payload.PermissionSummariesBySession[pendingSession.ID]
	if !ok || summary.PendingApprovalCount != 1 {
		t.Fatalf("pending summary = %+v ok=%v", summary, ok)
	}
	if _, ok := payload.PermissionSummariesBySession[recentSession.ID]; ok {
		t.Fatalf("unexpected zero-count summary for recent session: %+v", payload.PermissionSummariesBySession[recentSession.ID])
	}
	if len(payload.SessionViewsByID) != 0 {
		t.Fatalf("bootstrap permission summaries must not hydrate full permission details: %+v", payload.SessionViewsByID)
	}
	if got := payload.SyncScope["resource_set"]; got == "" || !strings.Contains(got, "permission_summaries") {
		t.Fatalf("sync scope resource_set missing permission_summaries: %q", got)
	}
}

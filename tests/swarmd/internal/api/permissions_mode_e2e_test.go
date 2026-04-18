package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func TestSessionModeAndPermissionsAPIEndToEnd(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "api-e2e.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, hub.Publish)

	server := NewServer("test", nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, permSvc, eventLog, hub)
	handler := server.Handler()

	workspacePath := t.TempDir()
	session := createSessionViaAPI(t, handler, workspacePath)

	modeResp := getSessionModeViaAPI(t, handler, session.ID)
	if modeResp.Mode != sessionruntime.ModePlan {
		t.Fatalf("expected initial mode plan, got %q", modeResp.Mode)
	}

	setSessionModeViaAPI(t, handler, session.ID, sessionruntime.ModeAuto, http.StatusOK)
	modeResp = getSessionModeViaAPI(t, handler, session.ID)
	if modeResp.Mode != sessionruntime.ModeAuto {
		t.Fatalf("expected mode auto, got %q", modeResp.Mode)
	}

	setSessionModeViaAPI(t, handler, session.ID, sessionruntime.ModeYolo, http.StatusOK)
	modeResp = getSessionModeViaAPI(t, handler, session.ID)
	if modeResp.Mode != sessionruntime.ModeYolo {
		t.Fatalf("expected mode yolo, got %q", modeResp.Mode)
	}

	setSessionModeViaAPI(t, handler, session.ID, "not-a-mode", http.StatusBadRequest)

	created := make([]pebblestore.PermissionRecord, 0, 3)
	for i, toolName := range []string{"bash", "write", "bash"} {
		record, createErr := permSvc.CreatePending(permission.CreateInput{
			SessionID:     session.ID,
			RunID:         "run_e2e",
			CallID:        fmt.Sprintf("call_%d", i),
			ToolName:      toolName,
			ToolArguments: "{}",
			Requirement:   toolName,
			Mode:          sessionruntime.ModeAuto,
		})
		if createErr != nil {
			t.Fatalf("create pending %d: %v", i, createErr)
		}
		created = append(created, record)
	}

	pending := listPendingViaAPI(t, handler, session.ID)
	if len(pending.Permissions) != 3 {
		t.Fatalf("expected 3 pending permissions, got %d", len(pending.Permissions))
	}

	resolved := resolvePermissionViaAPI(t, handler, session.ID, created[0].ID, permission.DecisionDeny, "deny first")
	if resolved.Permission.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("expected first permission denied, got %q", resolved.Permission.Status)
	}

	resolveAll := resolveAllViaAPI(t, handler, session.ID, permission.DecisionApprove, "approve rest")
	if resolveAll.Count != 2 {
		t.Fatalf("expected resolve_all to resolve 2 remaining permissions, got %d", resolveAll.Count)
	}

	cancelRecord, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     session.ID,
		RunID:         "run_e2e_cancel",
		CallID:        "call_cancel",
		ToolName:      "bash",
		ToolArguments: "{}",
		Requirement:   "bash",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create cancel pending: %v", err)
	}
	cancelled := resolvePermissionViaAPI(t, handler, session.ID, cancelRecord.ID, permission.DecisionCancel, "cancelled by user")
	if cancelled.Permission.Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("expected cancelled permission status, got %q", cancelled.Permission.Status)
	}

	pending = listPendingViaAPI(t, handler, session.ID)
	if len(pending.Permissions) != 0 {
		t.Fatalf("expected no pending permissions, got %d", len(pending.Permissions))
	}

	sessions := listSessionsViaAPI(t, handler)
	found := false
	for _, current := range sessions.Sessions {
		if current.ID != session.ID {
			continue
		}
		found = true
		if current.Mode != sessionruntime.ModeYolo {
			t.Fatalf("expected session mode yolo in list response, got %q", current.Mode)
		}
	}
	if !found {
		t.Fatalf("session %q missing from list response", session.ID)
	}
}

func TestModeAndPermissionPendingPersistenceAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist-e2e.pebble")

	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store (first): %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log (first): %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)

	session, _, err := sessionSvc.CreateSession("Persist", t.TempDir(), "persist")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := sessionSvc.SetMode(session.ID, sessionruntime.ModeAuto); err != nil {
		t.Fatalf("set mode auto: %v", err)
	}
	record, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     session.ID,
		RunID:         "run_persist",
		CallID:        "call_persist",
		ToolName:      "bash",
		ToolArguments: `{"command":"echo persist"}`,
		Requirement:   "bash",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store (first): %v", err)
	}

	store, err = pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store (second): %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	eventLog, err = pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log (second): %v", err)
	}
	sessionSvc = sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	permSvc = permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	if err := permSvc.ReconcilePendingRuns("daemon restarted"); err != nil {
		t.Fatalf("reconcile pending runs after restart: %v", err)
	}

	mode, err := sessionSvc.GetMode(session.ID)
	if err != nil {
		t.Fatalf("get mode after restart: %v", err)
	}
	if mode != sessionruntime.ModeAuto {
		t.Fatalf("expected persisted mode auto, got %q", mode)
	}

	pending, err := permSvc.ListPending(session.ID, 10)
	if err != nil {
		t.Fatalf("list pending after restart: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permissions after restart reconciliation, got %d", len(pending))
	}

	all, err := permSvc.ListPermissions(session.ID, 10)
	if err != nil {
		t.Fatalf("list permissions after restart: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 persisted permission after restart, got %d", len(all))
	}
	if all[0].ID != record.ID {
		t.Fatalf("expected persisted permission id %q after restart, got %q", record.ID, all[0].ID)
	}
	if all[0].Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("expected cancelled status after restart reconciliation, got %q", all[0].Status)
	}
	if all[0].Reason != "daemon restarted" {
		t.Fatalf("expected restart reconciliation reason, got %q", all[0].Reason)
	}
}

type apiSession struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

type apiSessionCreateResponse struct {
	OK      bool       `json:"ok"`
	Session apiSession `json:"session"`
}

type apiModeResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
}

type apiPendingPermissionsResponse struct {
	OK          bool                           `json:"ok"`
	SessionID   string                         `json:"session_id"`
	Count       int                            `json:"count"`
	Permissions []pebblestore.PermissionRecord `json:"permissions"`
}

type apiResolvePermissionResponse struct {
	OK         bool                         `json:"ok"`
	SessionID  string                       `json:"session_id"`
	Permission pebblestore.PermissionRecord `json:"permission"`
}

type apiResolveAllResponse struct {
	OK        bool                           `json:"ok"`
	SessionID string                         `json:"session_id"`
	Count     int                            `json:"count"`
	Resolved  []pebblestore.PermissionRecord `json:"resolved"`
}

type apiListSessionsResponse struct {
	OK       bool         `json:"ok"`
	Sessions []apiSession `json:"sessions"`
}

func createSessionViaAPI(t *testing.T, handler http.Handler, workspacePath string) apiSession {
	t.Helper()
	resp := apiSessionCreateResponse{}
	status := doJSONRequest(t, handler, http.MethodPost, "/v1/sessions", map[string]any{
		"title":          "E2E Session",
		"workspace_path": workspacePath,
		"workspace_name": "e2e",
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("create session status=%d", status)
	}
	if strings.TrimSpace(resp.Session.ID) == "" {
		t.Fatalf("missing session id in create response")
	}
	return resp.Session
}

func getSessionModeViaAPI(t *testing.T, handler http.Handler, sessionID string) apiModeResponse {
	t.Helper()
	resp := apiModeResponse{}
	status := doJSONRequest(t, handler, http.MethodGet, fmt.Sprintf("/v1/sessions/%s/mode", sessionID), nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("get mode status=%d", status)
	}
	return resp
}

func setSessionModeViaAPI(t *testing.T, handler http.Handler, sessionID, mode string, expectedStatus int) {
	t.Helper()
	var out map[string]any
	status := doJSONRequest(t, handler, http.MethodPost, fmt.Sprintf("/v1/sessions/%s/mode", sessionID), map[string]any{
		"mode": mode,
	}, &out)
	if status != expectedStatus {
		t.Fatalf("set mode %q status=%d expected=%d payload=%v", mode, status, expectedStatus, out)
	}
}

func listPendingViaAPI(t *testing.T, handler http.Handler, sessionID string) apiPendingPermissionsResponse {
	t.Helper()
	resp := apiPendingPermissionsResponse{}
	status := doJSONRequest(t, handler, http.MethodGet, fmt.Sprintf("/v1/sessions/%s/permissions", sessionID), nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("list pending status=%d", status)
	}
	return resp
}

func resolvePermissionViaAPI(t *testing.T, handler http.Handler, sessionID, permissionID, action, reason string) apiResolvePermissionResponse {
	t.Helper()
	resp := apiResolvePermissionResponse{}
	status := doJSONRequest(t, handler, http.MethodPost, fmt.Sprintf("/v1/sessions/%s/permissions/%s/resolve", sessionID, permissionID), map[string]any{
		"action": action,
		"reason": reason,
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("resolve permission status=%d", status)
	}
	return resp
}

func resolveAllViaAPI(t *testing.T, handler http.Handler, sessionID, action, reason string) apiResolveAllResponse {
	t.Helper()
	resp := apiResolveAllResponse{}
	status := doJSONRequest(t, handler, http.MethodPost, fmt.Sprintf("/v1/sessions/%s/permissions/resolve_all", sessionID), map[string]any{
		"action": action,
		"reason": reason,
		"limit":  200,
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("resolve all status=%d", status)
	}
	return resp
}

func listSessionsViaAPI(t *testing.T, handler http.Handler) apiListSessionsResponse {
	t.Helper()
	resp := apiListSessionsResponse{}
	status := doJSONRequest(t, handler, http.MethodGet, "/v1/sessions", nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("list sessions status=%d", status)
	}
	return resp
}

func doJSONRequest(t *testing.T, handler http.Handler, method, path string, payload any, out any) int {
	t.Helper()

	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if out != nil {
		decoder := json.NewDecoder(recorder.Body)
		if err := decoder.Decode(out); err != nil {
			t.Fatalf("decode response body: %v", err)
		}
	}
	return recorder.Code
}

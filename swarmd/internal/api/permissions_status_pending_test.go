package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionPermissionsStatusPendingBypassesHistoricalLimit(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "api-pending-limit.pebble"))
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
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, permSvc, nil, eventLog, hub)
	handler := server.Handler()

	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Long permission history",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	for i := 0; i < 201; i++ {
		record, createErr := permSvc.CreatePending(permission.CreateInput{
			SessionID:     session.ID,
			RunID:         "run_history",
			CallID:        fmt.Sprintf("resolved_%03d", i),
			ToolName:      "bash",
			ToolArguments: "{}",
			Requirement:   "bash",
			Mode:          sessionruntime.ModeAuto,
		})
		if createErr != nil {
			t.Fatalf("create historical pending %d: %v", i, createErr)
		}
		if _, resolveErr := permSvc.Resolve(session.ID, record.ID, permission.DecisionDeny, "historical"); resolveErr != nil {
			t.Fatalf("resolve historical permission %d: %v", i, resolveErr)
		}
	}

	active, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     session.ID,
		RunID:         "zz_run_active",
		CallID:        "zz_active_pending",
		ToolName:      "bash",
		ToolArguments: "{}",
		Requirement:   "bash",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create active pending permission: %v", err)
	}

	allHistory := listSessionPermissionsForTest(t, handler, session.ID, "?limit=200")
	for _, record := range allHistory.Permissions {
		if record.ID == active.ID {
			t.Fatalf("test setup expected active pending permission beyond all-history limit")
		}
	}

	pending := listSessionPermissionsForTest(t, handler, session.ID, "?status=pending&limit=200")
	if len(pending.Permissions) != 1 {
		t.Fatalf("expected 1 pending permission beyond history limit, got %d", len(pending.Permissions))
	}
	if pending.Permissions[0].ID != active.ID {
		t.Fatalf("expected pending permission %q, got %q", active.ID, pending.Permissions[0].ID)
	}
}

type permissionsResponseForTest struct {
	OK          bool                           `json:"ok"`
	SessionID   string                         `json:"session_id"`
	Count       int                            `json:"count"`
	Permissions []pebblestore.PermissionRecord `json:"permissions"`
}

func listSessionPermissionsForTest(t *testing.T, handler http.Handler, sessionID, query string) permissionsResponseForTest {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/sessions/%s/permissions%s", sessionID, query), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list permissions status=%d query=%q body=%s", rec.Code, query, rec.Body.String())
	}
	var out permissionsResponseForTest
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode permissions response: %v", err)
	}
	return out
}

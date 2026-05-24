package run

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExecutePlanManageNewRefusesWithoutOverrideWhenActivePlanExists(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	firstRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"new","title":"First Plan"}`, "")
	if err != nil {
		t.Fatalf("create first plan: %v output=%s", err, firstRaw)
	}
	first := decodePlanManageTestPlanID(t, firstRaw)

	_, err = runSvc.executePlanManageTool(sessionID, `{"action":"new","title":"Second Plan"}`, "")
	if err == nil {
		t.Fatal("expected plan_manage new without override to fail when active plan exists")
	}
	if got := err.Error(); !strings.Contains(got, "already has active plan") || !strings.Contains(got, first) || !strings.Contains(got, "override=true") {
		t.Fatalf("error = %q, want active-plan warning with override guidance", got)
	}
}

func TestExecutePlanManageNewOverrideCreatesReplacementPlan(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	firstRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"new","title":"First Plan"}`, "")
	if err != nil {
		t.Fatalf("create first plan: %v output=%s", err, firstRaw)
	}
	first := decodePlanManageTestPlanID(t, firstRaw)

	secondRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"new","title":"Second Plan","override":true}`, "")
	if err != nil {
		t.Fatalf("create replacement plan: %v output=%s", err, secondRaw)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(secondRaw), &payload); err != nil {
		t.Fatalf("decode second payload: %v", err)
	}
	if payload["override"] != true {
		t.Fatalf("payload override = %v, want true", payload["override"])
	}
	if payload["warning"] == "" {
		t.Fatalf("expected override warning in payload: %#v", payload)
	}
	second := decodePlanManageTestPlanID(t, secondRaw)
	if second == first {
		t.Fatalf("replacement plan id = %q, want different from first", second)
	}
}

func newPlanManageRunTestService(t *testing.T) (*Service, *sessionruntime.Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("open event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessions, events)
	return &Service{sessions: sessionSvc}, sessionSvc, func() { _ = store.Close() }
}

func createPlanManageTestSession(t *testing.T, svc *sessionruntime.Service) string {
	t.Helper()
	snapshot, _, err := svc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-plan-manage-test",
		Title:         "Plan Manage Test",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return snapshot.ID
}

func decodePlanManageTestPlanID(t *testing.T, raw string) string {
	t.Helper()
	var payload struct {
		Plan struct {
			ID string `json:"id"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode plan_manage payload: %v", err)
	}
	if payload.Plan.ID == "" {
		t.Fatalf("plan id missing in payload: %s", raw)
	}
	return payload.Plan.ID
}

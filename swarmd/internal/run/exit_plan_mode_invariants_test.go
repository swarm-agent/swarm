package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExitPlanModeDisabledAgentRejectsBeforePlanMutation(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)

	before, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session before disabled exit: ok=%t err=%v", ok, err)
	}
	args := mustMarshalPlanToolTestArgs(t, map[string]any{
		"title": "Disabled exit invariant",
		"document": pebblestore.SessionPlanDocument{
			Title: "Disabled exit invariant",
			Info:  pebblestore.SessionPlanInfo{Goal: "reject before persisting a plan"},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{
				ID: "cp-1", Title: "Reject", Status: sessionruntime.PlanCheckpointStatusPending, Order: 1,
				Tasks: []string{"do not persist"}, AcceptanceCriteria: []string{"no plan exists"},
			}},
		},
	})
	raw, err := runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm", ExitPlanModeEnabled: pebblestore.BoolPtr(false)}, args, "", nil)
	if err != nil {
		t.Fatalf("disabled exit returned execution error: %v", err)
	}
	var payload struct {
		Status        string `json:"status"`
		ApprovalState string `json:"approval_state"`
		Summary       string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode disabled exit response: %v raw=%s", err, raw)
	}
	if payload.Status != "rejected" || payload.ApprovalState != "disabled_for_agent" || !strings.Contains(payload.Summary, "disabled for agent") {
		t.Fatalf("disabled exit response = %#v raw=%s", payload, raw)
	}
	after, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session after disabled exit: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("disabled exit mutated session:\nbefore=%#v\nafter=%#v", before, after)
	}
	if active, ok, err := sessionSvc.GetActivePlan(sessionID); err != nil || ok {
		t.Fatalf("disabled exit persisted active plan: ok=%t err=%v plan=%#v", ok, err, active)
	}
}

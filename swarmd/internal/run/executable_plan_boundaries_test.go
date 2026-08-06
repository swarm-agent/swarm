package run

import (
	"encoding/json"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestExitPlanModePermissionRejectsIncompleteExecutablePlans(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)

	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "missing document", args: `{"title":"Markdown only","plan":"# Markdown only"}`, want: "explicit structured document"},
		{name: "empty checkpoints", args: `{"document":{"title":"Empty","info":{"goal":"reject empty"},"checkpoints":[]}}`, want: "at least one checkpoint"},
		{name: "missing checkpoint fields", args: `{"document":{"title":"Partial","info":{"goal":"reject partial"},"checkpoints":[{"status":"pending","order":1}]}}`, want: "checkpoints[0].id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSvc.buildExitPlanModePermissionPayload(sessionID, tool.Call{Name: "exit_plan_mode", Arguments: tc.args})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRequestNewPlanPermissionRoundTripPreservesValidatedDocument(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)

	doc := completeExecutablePlanDocument("Round trip")
	rawDoc, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	payload, needsApproval, err := runSvc.buildPlanManagePermissionPayload(sessionID, tool.Call{Name: "plan_manage", Arguments: `{"action":"request_new_plan","document":` + string(rawDoc) + `}`})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if !needsApproval {
		t.Fatal("request_new_plan did not require approval")
	}
	approvedRaw, err := json.Marshal(payload.ApprovedArguments["document"])
	if err != nil {
		t.Fatal(err)
	}
	var approved pebblestore.SessionPlanDocument
	if err := json.Unmarshal(approvedRaw, &approved); err != nil {
		t.Fatal(err)
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(&approved); err != nil {
		t.Fatalf("approved document lost executable fields: %v", err)
	}
	if approved.Checkpoints[0].AcceptanceCriteria[0] != doc.Checkpoints[0].AcceptanceCriteria[0] || approved.Checkpoints[0].Order != 1 {
		t.Fatalf("approved document = %#v", approved)
	}
}

func TestMasterHarnessPromptSeparatesStoppedRedirectionFromTerminalConversation(t *testing.T) {
	prompt := masterHarnessPrompt("/workspace")
	for _, want := range []string{
		"A user message after an explicit pause/stop already reactivates the paused checkpoint",
		"treat the checkpoint as nonterminal",
		"you must call restart_checkpoint with the complete replacement contract",
		"do not refuse or dismiss the redirection",
		"emit a final handoff instead of restarting",
		"Terminal checkpoint actions only finish the current checkpoint",
		"re-complete a plan already waiting for final review",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("master harness prompt missing %q", want)
		}
	}
}

func TestRejectedExitPlanModeCreatesNoLifecycleEffects(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)
	initial, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-guard", "Guard", "# Guard", "draft", "draft", true, sessionruntime.PlanSaveMetadata{Document: completeExecutablePlanDocument("Guard")})
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}

	var mutations []sessionruntime.SessionMutationInput
	applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		mutations = append(mutations, input)
		return sessionSvc.ApplySessionMutation(input)
	}
	_, err = runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, `{"plan_id":"plan-guard","title":"Invalid","plan":"# prose only"}`, "", applyMutation)
	if err == nil || !strings.Contains(err.Error(), "structured document") {
		t.Fatalf("rejection error = %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("rejected submission emitted mutations: %#v", mutations)
	}
	stored, ok, err := sessionSvc.GetPlan(sessionID, "plan-guard")
	if err != nil || !ok {
		t.Fatalf("get plan ok=%v err=%v", ok, err)
	}
	if stored.Version != initial.Version || stored.Status != "draft" || stored.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending {
		t.Fatalf("rejected submission mutated plan: %#v", stored)
	}
	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok || sessionruntime.NormalizeMode(session.Mode) != sessionruntime.ModePlan {
		t.Fatalf("rejected submission changed mode: session=%#v ok=%v err=%v", session, ok, err)
	}
}

func completeExecutablePlanDocument(title string) *pebblestore.SessionPlanDocument {
	return &pebblestore.SessionPlanDocument{
		Title: title,
		Info:  pebblestore.SessionPlanInfo{Goal: "Exercise executable plan boundaries"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Execute boundary test",
			Tasks:              []string{"Keep the structured checkpoint intact"},
			AcceptanceCriteria: []string{"The exact approved document remains runnable"},
			Status:             sessionruntime.PlanCheckpointStatusPending,
			Order:              1,
		}},
		ActiveCheckpointID: "cp-1",
	}
}

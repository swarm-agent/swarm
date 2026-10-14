package run

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: reviewFromResult must never approve failed/stopped or malformed
// reviewer output, even if it contains OK. This pure terminal-result boundary
// is the narrowest deterministic proof of verdict mapping.
func TestReviewerResultVerdicts(t *testing.T) {
	ok := "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: test"
	for _, tc := range []struct{ status, report, want string }{
		{"succeeded", ok, "ok"}, {"succeeded", "VERDICT: BLOCK\nSUMMARY: broken\nFINDINGS: 1. high file:1\nTESTS RUN: failed", "block"},
		{"succeeded", "garbage", "block"}, {"succeeded", "", "block"}, {"failed", ok, "block"}, {"cancelled", ok, "block"},
	} {
		r := reviewFromResult(store.WorkerRunRecord{Status: tc.status, WorkerID: "w", ID: "r", ModelProfile: &store.SessionModelProfileSnapshot{Action: store.ModelProfileSelection{Provider: "provider", Model: "model"}}}, tc.report, "head", 123)
		if r.Verdict != tc.want || r.Head != "head" || r.RecordedAt != 123 || r.Model != "provider/model" || (r.Verdict == "block" && r.Reason == "") {
			t.Fatal(tc, r)
		}
	}
}

// Purpose: workerRoleAssignment preserves explicit reviewer settings and uses
// coder only when absent, avoiding hardcoded models and sibling mutation.
func TestReviewerModelAssignmentFallback(t *testing.T) {
	settings := store.AgentModelSettingsRecord{SystemAgents: store.SystemAgentModelAssignments{Coder: store.AgentModelAssignment{Provider: "p", Model: "coder"}}}
	a, ok := workerRoleAssignment(settings, store.WorkerRoleReviewer)
	if !ok || a.Model != "coder" {
		t.Fatal(a)
	}
	settings.SystemAgents.Reviewer = store.AgentModelAssignment{Provider: "p", Model: "review"}
	a, ok = workerRoleAssignment(settings, store.WorkerRoleReviewer)
	if !ok || a.Model != "review" || settings.SystemAgents.Coder.Model != "coder" {
		t.Fatal(a)
	}
}

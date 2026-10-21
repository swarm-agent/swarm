package api

import "testing"

func TestControlMCPSessionSummaryNamesProjectAndRole(t *testing.T) {
	summary := controlMCPSessionSummary(map[string]any{
		"id":    "s1",
		"title": "Plan the release",
		"metadata": map[string]any{
			"agent_name": "system-orchestrator",
			"project_id": "proj_1",
			"role":       "project_orchestrator",
		},
		"lifecycle": map[string]any{"active": true},
	})
	if summary["project_id"] != "proj_1" || summary["role"] != "project_orchestrator" || summary["agent"] != "system-orchestrator" {
		t.Fatalf("summary = %v", summary)
	}
	if summary["running"] != true {
		t.Fatalf("running missing: %v", summary)
	}
	plain := controlMCPSessionSummary(map[string]any{"id": "s2", "metadata": map[string]any{"agent_name": "swarm"}})
	if _, ok := plain["project_id"]; ok {
		t.Fatalf("a session outside a project names none: %v", plain)
	}
}

func TestControlMCPSessionTimesComeFromSwarmsRecord(t *testing.T) {
	summary := controlMCPSessionSummary(map[string]any{
		"id": "s1", "created_at": float64(1000), "updated_at": float64(5000), "last_message_at": float64(4000),
		"lifecycle": map[string]any{"active": true, "started_at": float64(3000)},
	})
	if summary["created_at"] != float64(1000) || summary["last_message_at"] != float64(4000) || summary["run_started_at"] != float64(3000) {
		t.Fatalf("summary = %v", summary)
	}
	if _, ok := summary["run_ended_at"]; ok {
		t.Fatalf("an unset time is left out: %v", summary)
	}
	run := controlMCPRunTiming(map[string]any{"run_id": "r1", "status": "running", "active": true,
		"started_at": float64(3000), "duration_ms": float64(0), "cumulative_duration_ms": float64(42000)})
	if run["active"] != true || run["started_at"] != float64(3000) || run["cumulative_duration_ms"] != float64(42000) {
		t.Fatalf("run = %v", run)
	}
	if _, ok := run["duration_ms"]; ok {
		t.Fatalf("an active run has no duration yet: %v", run)
	}
	if controlMCPRunTiming(nil) != nil {
		t.Fatal("no run state, no run")
	}
}

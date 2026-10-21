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

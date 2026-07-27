package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestTUIListSessionsForActiveContextUsesV3TUIWorkset(t *testing.T) {
	workspacePath := t.TempDir()
	var gotPath string
	var gotRequest client.SessionV3TUIWorksetRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"sessions_by_id": map[string]any{
				"session-a": map[string]any{"id": "session-a", "workspace_path": workspacePath, "title": "A", "mode": "auto", "updated_at": 2000},
			},
			"projections_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "last_event_seq": 7, "projection_high_watermark_seq": 8, "updated_at": 2000},
			},
			"messages_by_session":          map[string]any{},
			"events_by_session":            map[string]any{},
			"plans_by_session":             map[string]any{},
			"plan_revisions_by_session":    map[string]any{},
			"permissions_by_session":       map[string]any{},
			"usage_by_session":             map[string]any{},
			"preferences_by_session":       map[string]any{},
			"run_intents_by_session":       map[string]any{},
			"history_manifests_by_session": map[string]any{},
			"history_chunks_by_id":         map[string]any{},
			"pagination":                   map[string]any{"has_more": true, "next_before_updated_at": 1234, "next_before_session_id": "session-a"},
			"watermarks":                   map[string]any{"loaded_at": 3000},
			"session_order":                []string{"session-a"},
		})
	}))
	defer server.Close()

	app := &App{api: testAPIWithToken(server.URL)}
	sessions, err := app.listSessionsForActiveContext(context.Background(), 25, workspacePath)
	if err != nil {
		t.Fatalf("listSessionsForActiveContext() error = %v", err)
	}
	if gotPath != "POST /v3/tui/sessions:workset" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotRequest.Recent.Limit != 25 || gotRequest.History.Mode != "tail" || !gotRequest.History.IncludeEvents {
		t.Fatalf("bounded workset options not encoded: recent=%#v history=%#v", gotRequest.Recent, gotRequest.History)
	}
	if len(gotRequest.Scope.WorkspacePaths) != 1 || gotRequest.Scope.WorkspacePaths[0] != workspacePath || gotRequest.Scope.CWDPath != "" {
		t.Fatalf("workspace scope = %#v, want only %q", gotRequest.Scope, workspacePath)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-a" || sessions[0].SessionAPI != "v3" || sessions[0].LastEventSeq != 7 || sessions[0].ProjectionHighWatermarkSeq != 8 {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestTUIWorksetModelMappingPreservesHydratedV3State(t *testing.T) {
	workset := client.SessionV3Workset{
		SessionsByID: map[string]client.SessionSummary{
			"session-a": {ID: "session-a", WorkspacePath: "/workspace", Title: "A", Mode: "plan", SessionAPI: "v3", UpdatedAt: 2000},
		},
		ProjectionsBySession: map[string]client.SessionV3Projection{
			"session-a": {SessionID: "session-a", LastEventSeq: 11, ProjectionHighWatermarkSeq: 12},
		},
		PermissionsBySession: map[string][]client.PermissionRecord{
			"session-a": {{ID: "perm-1", SessionID: "session-a", Status: "pending"}},
		},
		PreferencesBySession: map[string]client.ModelPreference{
			"session-a": {Provider: "openai", Model: "gpt", Thinking: "medium", ServiceTier: "auto", ContextMode: "standard"},
		},
		UsageBySession: map[string]client.SessionUsageSummary{
			"session-a": {SessionID: "session-a", Provider: "openai", Model: "gpt", TurnCount: 1},
		},
		PlansBySession: map[string][]client.SessionPlan{
			"session-a": {{ID: "plan-1", SessionID: "session-a", Title: "Plan"}},
		},
		PlanRevisionsBySession: map[string][]client.SessionPlan{
			"session-a": {{ID: "plan-1", SessionID: "session-a", Version: 1, Title: "Plan"}},
		},
		AgentModelPolicyBySession: map[string]client.SessionV3AgentModelPolicy{
			"session-a": {AgentName: "swarm", ResolvedAgent: "swarm", Locked: true},
		},
		RunIntentsBySession: map[string][]client.SessionV3RunIntent{
			"session-a": {{SessionID: "session-a", RunID: "run-1", Status: "running", CreatedAt: 1000, UpdatedAt: 1100, EventSeq: 11}},
		},
		SessionOrder: []string{"session-a"},
	}

	summaries := modelSessionSummariesFromTUIWorkset(workset)
	if len(summaries) != 1 {
		t.Fatalf("summaries len = %d", len(summaries))
	}
	summary := summaries[0]
	if summary.PendingPermissionCount != 1 || summary.Preference.Model != "gpt" || summary.LastEventSeq != 11 || summary.ProjectionHighWatermarkSeq != 12 {
		t.Fatalf("summary state not mapped: %#v", summary)
	}
	if summary.ActiveRunIntent == nil || summary.ActiveRunIntent.RunID != "run-1" || summary.Lifecycle == nil || !summary.Lifecycle.Active {
		t.Fatalf("run intent/lifecycle not mapped: intent=%#v lifecycle=%#v", summary.ActiveRunIntent, summary.Lifecycle)
	}
	for _, key := range []string{"v3_pending_permissions", "v3_usage_summary", "v3_plans", "v3_plan_revisions", "v3_agent_model_policy", "v3_run_intents"} {
		if _, ok := summary.Metadata[key]; !ok {
			t.Fatalf("metadata missing %s: %#v", key, summary.Metadata)
		}
	}
}

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionV3SyncBootstrapClientUsesCanonicalGlobalRecentSelector(t *testing.T) {
	var gotRequest SessionV3SyncBootstrapRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sync/bootstrap" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                       true,
			"snapshot_endpoint_cursor": "cursor-1",
			"sessions_by_id": map[string]any{
				"session-a": map[string]any{"id": "session-a", "workspace_path": "/workspace-a", "title": "A", "updated_at": 2000},
			},
			"projections_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "last_event_seq": 7, "projection_high_watermark_seq": 8},
			},
			"current_run_state_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "run_id": "run-a", "active": true, "status": "running", "started_at": 1000, "updated_at": 2000},
			},
			"permission_summaries_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "pending_approval_count": 1},
			},
			"active_session_ids": []string{"session-a"},
			"session_order":      []string{"session-a"},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	snapshot, err := api.GetSessionV3SyncBootstrap(context.Background(), SessionV3SyncBootstrapRequest{
		Surface: "tui",
		Selector: SessionV3SyncSelector{
			Kind:      "recent",
			Global:    true,
			Recent:    SessionV3WorksetRecent{Limit: 200},
			Attention: SessionV3SyncAttention{PendingPermissions: true},
		},
		History:       SessionV3WorksetHistory{Mode: "none"},
		Resources:     SessionV3SyncResources{CurrentRunState: true, PermissionSummaries: true},
		IncludeActive: true,
	})
	if err != nil {
		t.Fatalf("GetSessionV3SyncBootstrap() error = %v", err)
	}
	if gotRequest.Surface != "tui" || gotRequest.Selector.Kind != "recent" || !gotRequest.Selector.Global || gotRequest.Selector.Recent.Limit != 200 {
		t.Fatalf("bootstrap selector = %#v", gotRequest)
	}
	if gotRequest.History.Mode != "none" || !gotRequest.Resources.CurrentRunState || !gotRequest.Resources.PermissionSummaries || !gotRequest.IncludeActive {
		t.Fatalf("bootstrap resources = %#v", gotRequest)
	}
	session := snapshot.SessionsByID["session-a"]
	if session.SessionAPI != "v3" || session.LastEventSeq != 7 || session.ProjectionHighWatermarkSeq != 8 {
		t.Fatalf("session = %#v", session)
	}
	if snapshot.CurrentRunStateBySession["session-a"].RunID != "run-a" || snapshot.PermissionSummariesBySession["session-a"].PendingApprovalCount != 1 {
		t.Fatalf("snapshot activity state = %#v", snapshot)
	}
}

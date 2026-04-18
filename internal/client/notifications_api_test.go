package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetNotificationSummary(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotPath string
	var gotSwarmID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSwarmID = r.URL.Query().Get("swarm_id")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"summary": map[string]any{
				"swarm_id":     "swarm-test",
				"total_count":  9,
				"unread_count": 7,
				"active_count": 4,
				"updated_at":   int64(12345),
			},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	summary, err := api.GetNotificationSummary(context.Background(), "")
	if err != nil {
		t.Fatalf("GetNotificationSummary() error = %v", err)
	}
	if gotPath != "/v1/notifications/summary" {
		t.Fatalf("request path = %q, want /v1/notifications/summary", gotPath)
	}
	if gotSwarmID != "" {
		t.Fatalf("swarm_id query = %q, want empty", gotSwarmID)
	}
	if summary.UnreadCount != 7 {
		t.Fatalf("UnreadCount = %d, want 7", summary.UnreadCount)
	}
	if summary.TotalCount != 9 {
		t.Fatalf("TotalCount = %d, want 9", summary.TotalCount)
	}
	if summary.ActiveCount != 4 {
		t.Fatalf("ActiveCount = %d, want 4", summary.ActiveCount)
	}
}

func TestGetNotificationSummaryIncludesSwarmIDQuery(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotSwarmID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSwarmID = r.URL.Query().Get("swarm_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"summary": map[string]any{
				"swarm_id":     gotSwarmID,
				"total_count":  1,
				"unread_count": 1,
				"active_count": 1,
				"updated_at":   int64(1),
			},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	summary, err := api.GetNotificationSummary(context.Background(), "child-swarm")
	if err != nil {
		t.Fatalf("GetNotificationSummary() error = %v", err)
	}
	if gotSwarmID != "child-swarm" {
		t.Fatalf("swarm_id query = %q, want child-swarm", gotSwarmID)
	}
	if summary.SwarmID != "child-swarm" {
		t.Fatalf("summary swarm id = %q, want child-swarm", summary.SwarmID)
	}
}

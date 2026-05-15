package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListPendingPermissionsRequestsPendingStatus(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotPath string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("status") != "pending" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"session_id": "session-test",
			"count":      1,
			"permissions": []map[string]any{{
				"id":         "perm-pending",
				"session_id": "session-test",
				"status":     "pending",
			}},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	permissions, err := api.ListPendingPermissions(context.Background(), "session-test", 200)
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if gotPath != "/v1/sessions/session-test/permissions" {
		t.Fatalf("request path = %q, want /v1/sessions/session-test/permissions", gotPath)
	}
	if gotQuery != "status=pending&limit=200" {
		t.Fatalf("request query = %q, want status=pending&limit=200", gotQuery)
	}
	if len(permissions) != 1 || permissions[0].ID != "perm-pending" {
		t.Fatalf("permissions = %#v, want perm-pending", permissions)
	}
}

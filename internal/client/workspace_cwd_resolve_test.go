package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceCWDResolveClientCallsPathScopedResolver(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var gotPath string
	var gotCWD string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCWD = r.URL.Query().Get("cwd")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"cwd":             "/cwd",
			"resolved_path":   "/cwd",
			"resolution_kind": "non_workspace",
			"primary_swarm_target": map[string]any{
				"swarm_id":     "host-swarm",
				"name":         "Primary Desk",
				"relationship": "self",
				"kind":         "host",
				"online":       true,
				"selectable":   true,
				"current":      true,
			},
			"routes": []map[string]any{{
				"route_id":               "host",
				"route_source":           "tui/primary_cwd",
				"runtime_swarm_id":       "host-swarm",
				"runtime_swarm_name":     "Primary Desk",
				"runtime_kind":           "host",
				"runtime_relationship":   "self",
				"host_workspace_path":    "/cwd",
				"runtime_workspace_path": "/cwd",
				"tui_primary_cwd":        true,
			}},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	resp, err := api.WorkspaceCWDResolve(context.Background(), "/cwd")
	if err != nil {
		t.Fatalf("WorkspaceCWDResolve() error = %v", err)
	}
	if gotPath != "/v1/workspace/cwd/resolve" || gotCWD != "/cwd" {
		t.Fatalf("request = %s cwd=%q", gotPath, gotCWD)
	}
	if resp.PrimarySwarmTarget == nil || resp.PrimarySwarmTarget.Name != "Primary Desk" {
		t.Fatalf("primary target = %+v", resp.PrimarySwarmTarget)
	}
	if len(resp.Routes) != 1 || !resp.Routes[0].TUIPrimaryCWD || resp.Routes[0].RuntimeSwarmName != "Primary Desk" {
		t.Fatalf("routes = %+v", resp.Routes)
	}
}

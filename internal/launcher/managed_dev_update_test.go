package launcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestManagedDevTargetIDsForBindingsSelectsUniqueManagedTargets(t *testing.T) {
	bindings := []managedDevTopologyWorkspaceBindingResponse{
		{DestinationRuntimeSwarmID: "managed-2", DestinationWorkspacePath: "/work/2"},
		{DestinationRuntimeSwarmID: "local", DestinationWorkspacePath: "/work/local"},
		{DestinationHostSwarmID: "managed-1", DestinationWorkspacePath: "/work/1"},
		{DestinationRuntimeSwarmID: "managed-1", DestinationWorkspacePath: "/work/1-again"},
		{DestinationRuntimeSwarmID: "child", DestinationWorkspacePath: "/work/child"},
	}
	targets := []managedDevSwarmTarget{
		{SwarmID: "managed-1", Relationship: "managed", Kind: "host", Selectable: true},
		{SwarmID: "managed-2", Relationship: "managed", Kind: "manual", Selectable: true},
		{SwarmID: "child", Relationship: "child", Kind: "remote", Selectable: true},
		{SwarmID: "local", Relationship: "self", Kind: "self", Selectable: true},
		{SwarmID: "managed-offline", Relationship: "managed", Kind: "host", Selectable: false},
	}

	got := managedDevTargetIDsForBindings(bindings, targets)
	want := []string{"managed-1", "managed-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managedDevTargetIDsForBindings() = %#v, want %#v", got, want)
	}
}

func TestSyncManagedDevHostGitPostsDestructiveIdentity(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != managedHostGitSyncApplyPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, managedHostGitSyncApplyPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(managedDevManagedHostGitSyncResponse{OK: true})
	}))
	defer server.Close()

	profile := Profile{URL: server.URL}
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "/definitely/not/present.sock")
	inspect := managedDevGitInspectResponse{RepoRoot: "/repo", Branch: "dev", Head: "0123456789abcdef", Tree: "abcdef0123456789"}
	if err := syncManagedDevHostGit(t.Context(), profile, "managed-swarm", inspect); err != nil {
		t.Fatalf("syncManagedDevHostGit() error = %v", err)
	}
	if payload["target_swarm_id"] != "managed-swarm" || payload["source_workspace_path"] != "/repo" || payload["branch"] != "dev" || payload["commit_sha"] != inspect.Head || payload["tree_sha"] != inspect.Tree || payload["destructive"] != true {
		t.Fatalf("payload = %#v", payload)
	}
}

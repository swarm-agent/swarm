package launcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/pkg/localupdate"
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

func TestAdvanceManagedDevHostStatusMergesPhases(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(updateJobIDEnv, "job-1")
	if err := localupdate.WriteUpdateJobStatus(dataDir, localupdate.UpdateJobStatus{ID: "job-1", Kind: updateKindDev, Status: updateJobStatusRunning}); err != nil {
		t.Fatalf("WriteUpdateJobStatus: %v", err)
	}
	profile := Profile{DataDir: dataDir}
	target := managedDevSwarmTarget{SwarmID: "managed-1", Name: "swarm-bomb-2"}

	advanceManagedDevHostStatus(profile, target, managedDevPhaseInspect, updateJobStatusCompleted, "selected", "")
	advanceManagedDevHostStatus(profile, target, managedDevPhaseSync, updateJobStatusRunning, "syncing", "")
	advanceManagedDevHostStatus(profile, target, managedDevPhaseSync, updateJobStatusCompleted, "synced", "")

	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(dataDir))
	if err != nil || !ok {
		t.Fatalf("ReadUpdateJobStatusPath ok=%v err=%v", ok, err)
	}
	if len(status.Hosts) != 1 {
		t.Fatalf("hosts = %#v", status.Hosts)
	}
	host := status.Hosts[0]
	if host.HostID != "managed-1" || host.Name != "swarm-bomb-2" || host.CurrentPhase != managedDevPhaseSync || host.Status != updateJobStatusRunning {
		t.Fatalf("host = %#v", host)
	}
	if len(host.Phases) != 2 || host.Phases[0].Name != managedDevPhaseInspect || host.Phases[1].Name != managedDevPhaseSync || host.Phases[1].Status != updateJobStatusCompleted {
		t.Fatalf("phases = %#v", host.Phases)
	}
}

func TestMarkManagedDevHostPhaseCompletesExistingManagedHosts(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(updateJobIDEnv, "job-1")
	if err := localupdate.WriteUpdateJobStatus(dataDir, localupdate.UpdateJobStatus{
		ID:     "job-1",
		Kind:   updateKindDev,
		Status: updateJobStatusRunning,
		Hosts:  []localupdate.UpdateJobHostStatus{{HostID: "managed-1", Name: "swarm-bomb-2", Role: "managed"}},
	}); err != nil {
		t.Fatalf("WriteUpdateJobStatus: %v", err)
	}
	profile := Profile{DataDir: dataDir}
	markManagedDevHostPhase(profile, managedDevPhaseVerify, updateJobStatusCompleted, "verified", "")

	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(dataDir))
	if err != nil || !ok {
		t.Fatalf("ReadUpdateJobStatusPath ok=%v err=%v", ok, err)
	}
	if len(status.Hosts) != 1 || status.Hosts[0].Status != updateJobStatusCompleted || status.Hosts[0].CurrentPhase != managedDevPhaseVerify {
		t.Fatalf("hosts = %#v", status.Hosts)
	}
}

func TestAdvanceManagedDevHostStatusRequiresJobID(t *testing.T) {
	dataDir := t.TempDir()
	if err := localupdate.WriteUpdateJobStatus(dataDir, localupdate.UpdateJobStatus{ID: "job-1", Kind: updateKindDev, Status: updateJobStatusRunning}); err != nil {
		t.Fatalf("WriteUpdateJobStatus: %v", err)
	}
	_ = os.Unsetenv(updateJobIDEnv)
	advanceManagedDevHostStatus(Profile{DataDir: dataDir}, managedDevSwarmTarget{SwarmID: "managed-1"}, managedDevPhaseSync, updateJobStatusRunning, "syncing", "")
	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(dataDir))
	if err != nil || !ok {
		t.Fatalf("ReadUpdateJobStatusPath ok=%v err=%v", ok, err)
	}
	if len(status.Hosts) != 0 {
		t.Fatalf("hosts = %#v", status.Hosts)
	}
}

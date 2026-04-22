package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestSwarmReplicateLeavesRuntimeEmptyWhenCallerOmitsIt(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-a",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Runtime != "" {
		t.Fatalf("create runtime = %q, want empty", fakeDeploy.lastCreateInput.Runtime)
	}
	if fakeDeploy.lastCreateInput.BypassPermissions {
		t.Fatalf("create bypass = %t, want false", fakeDeploy.lastCreateInput.BypassPermissions)
	}
}

func TestSwarmReplicatePassesRequestedRuntimeThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":               "local",
		"swarm_name":         "replica-b",
		"runtime":            "docker",
		"bypass_permissions": true,
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Runtime != "docker" {
		t.Fatalf("create runtime = %q, want %q", fakeDeploy.lastCreateInput.Runtime, "docker")
	}
	if !fakeDeploy.lastCreateInput.BypassPermissions {
		t.Fatalf("create bypass = %t, want true", fakeDeploy.lastCreateInput.BypassPermissions)
	}
	var response struct {
		Swarm struct {
			BypassPermissions bool `json:"bypass_permissions"`
		} `json:"swarm"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Swarm.BypassPermissions {
		t.Fatalf("response swarm bypass_permissions = %t, want true", response.Swarm.BypassPermissions)
	}
}

func TestSwarmReplicatePassesVaultPasswordThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-c",
		"sync": map[string]any{
			"enabled":        true,
			"mode":           "managed",
			"vault_password": "vault-password",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.SyncVaultPassword != "vault-password" {
		t.Fatalf("create sync vault password = %q, want %q", fakeDeploy.lastCreateInput.SyncVaultPassword, "vault-password")
	}
}

func TestSwarmReplicateDefaultsSyncModulesToCredentialsOnly(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-d",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got, want := fakeDeploy.lastCreateInput.SyncModules, []string{"credentials"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("create sync modules = %#v, want %#v", got, want)
	}
}

func TestSwarmReplicatePassesRequestedSyncModulesThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-e",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
			"modules": []string{"credentials", "agents", "custom_tools", "agents"},
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	got := fakeDeploy.lastCreateInput.SyncModules
	want := []string{"credentials", "agents", "custom_tools"}
	if len(got) != len(want) {
		t.Fatalf("create sync modules length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("create sync modules = %#v, want %#v", got, want)
		}
	}
}

func newReplicateTestHandler(t *testing.T) (http.Handler, *fakeReplicateDeployService, string) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "replicate-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	workspacePath := filepath.Join(t.TempDir(), "workspace-one")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if _, err := workspaceSvc.Add(workspacePath, "workspace-one", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	server := NewServer("test", nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	cfg.DesktopPort = 5555
	cfg.NetworkMode = startupconfig.NetworkModeLAN
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)

	server.SetSwarmService(fakeReplicateSwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "host-swarm-id",
				Name:    "host-swarm",
				Role:    "master",
			},
			CurrentGroupID: "group-1",
			Groups: []swarmruntime.GroupState{{
				Group: swarmruntime.Group{
					ID:          "group-1",
					Name:        "Primary Group",
					NetworkName: "group-net",
					HostSwarmID: "host-swarm-id",
				},
			}},
		},
	})

	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	return server.Handler(), fakeDeploy, workspacePath
}

func postReplicateRequest(t *testing.T, handler http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/swarm/replicate", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type fakeReplicateDeployService struct {
	lastCreateInput          deployruntime.ContainerCreateInput
	lastAttachApproveInput   deployruntime.ContainerAttachApproveInput
	lastSyncAgentBundleInput deployruntime.ContainerSyncCredentialRequestInput
}

func (f *fakeReplicateDeployService) RuntimeStatus(context.Context) (deployruntime.ContainerRuntimeStatus, error) {
	return deployruntime.ContainerRuntimeStatus{Recommended: "podman", Available: []string{"podman", "docker"}}, nil
}

func (f *fakeReplicateDeployService) List(context.Context) ([]deployruntime.ContainerDeployment, error) {
	return []deployruntime.ContainerDeployment{{
		ID:                "deployment-1",
		Name:              "replica",
		Status:            "running",
		AttachStatus:      "attached",
		BypassPermissions: f.lastCreateInput.BypassPermissions,
		ChildSwarmID:      "child-swarm-1",
		ChildDisplayName:  "replica",
	}}, nil
}

func (f *fakeReplicateDeployService) Create(_ context.Context, input deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error) {
	f.lastCreateInput = input
	return deployruntime.ContainerDeployment{
		ID:                "deployment-1",
		Name:              input.Name,
		Runtime:           input.Runtime,
		GroupID:           input.GroupID,
		GroupName:         input.GroupName,
		SyncEnabled:       input.SyncEnabled,
		BypassPermissions: input.BypassPermissions,
		Status:            "running",
		AttachStatus:      "starting",
	}, nil
}

func (f *fakeReplicateDeployService) Act(context.Context, deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f *fakeReplicateDeployService) Delete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeReplicateDeployService) ChildAttachState(context.Context, deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	return swarmruntime.LocalState{}, nil
}

func (f *fakeReplicateDeployService) AttachRequest(context.Context, deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) AttachStatus(context.Context, deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) AttachApprove(_ context.Context, input deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error) {
	f.lastAttachApproveInput = input
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) FinalizeAttachFromHost(context.Context, deployruntime.ContainerAttachFinalizeInput) error {
	return nil
}

func (f *fakeReplicateDeployService) SyncCredentialBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f *fakeReplicateDeployService) SyncAgentBundle(_ context.Context, input deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error) {
	f.lastSyncAgentBundleInput = input
	return deployruntime.ContainerSyncAgentBundle{}, nil
}

func (f *fakeReplicateDeployService) WorkspaceBootstrap(context.Context, deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error) {
	return nil, nil
}

func (f *fakeReplicateDeployService) AutoAttachChild(context.Context) error {
	return nil
}

func (f *fakeReplicateDeployService) UnlockManagedLocalChildVaults(context.Context) error {
	return nil
}

type fakeReplicateSwarmService struct {
	state swarmruntime.LocalState
}

func (f fakeReplicateSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeReplicateSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeReplicateSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeReplicateSwarmService) DeleteGroup(string) error {
	return nil
}

func (f fakeReplicateSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeReplicateSwarmService) OutgoingPeerAuthToken(string) (string, bool, error) {
	return "", false, nil
}

func (f fakeReplicateSwarmService) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return false, nil
}

func (f fakeReplicateSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeReplicateSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeReplicateSwarmService) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeReplicateSwarmService) SubmitEnrollment(swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}

func (f fakeReplicateSwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeReplicateSwarmService) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeReplicateSwarmService) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeReplicateSwarmService) FinalizeRemoteBootstrapChildPairing(swarmruntime.FinalizeRemoteBootstrapChildPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeReplicateSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeReplicateSwarmService) DetachToStandalone(string) error {
	return nil
}

var (
	_ deployContainerService = (*fakeReplicateDeployService)(nil)
	_ swarmService           = fakeReplicateSwarmService{}
)

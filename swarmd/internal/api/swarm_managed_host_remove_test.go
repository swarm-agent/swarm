package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/localcontainers"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmManagedHostRemoveDetachesManagedHostConfig(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", Role: startupconfig.SwarmRoleManaged},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "manager-swarm-1"},
	}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.ParentSwarmID = "manager-swarm-1"
		cfg.PairingState = startupconfig.PairingStatePaired
		cfg.ManagedHostSync = startupconfig.ManagedHostSyncConfig{
			Mode:              "managed",
			Modules:           []string{"credentials", "agents"},
			OwnerSwarmID:      "manager-swarm-1",
			HostAPIBaseURL:    "https://manager.example.test",
			SyncCredentialURL: "https://manager.example.test/v1/deploy/container/sync/credentials",
			SyncAgentURL:      "https://manager.example.test/v1/deploy/container/sync/agents",
		}
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"propagate": false,
		"reason":    "test detach",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || !response.LocalRemoved || response.Pairing.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("response = %+v", response)
	}
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.Child || cfg.SwarmRole != "" || cfg.ParentSwarmID != "" || cfg.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("config not detached: child=%t role=%q parent=%q pairing=%q", cfg.Child, cfg.SwarmRole, cfg.ParentSwarmID, cfg.PairingState)
	}
	if cfg.ManagedHostSync.Mode != "" || len(cfg.ManagedHostSync.Modules) != 0 || cfg.ManagedHostSync.OwnerSwarmID != "" || cfg.ManagedHostSync.HostAPIBaseURL != "" || cfg.ManagedHostSync.SyncCredentialURL != "" || cfg.ManagedHostSync.SyncAgentURL != "" {
		t.Fatalf("managed sync config not scrubbed: %+v", cfg.ManagedHostSync)
	}
}

func TestSwarmManagedHostRemoveDetachesBootstrapPreparedManagerPeer(t *testing.T) {
	var detachCalls int
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{
		state:       swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", Role: ""}},
		detachCalls: &detachCalls,
	}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.ParentSwarmID = "stale-manager-swarm"
		cfg.PairingState = startupconfig.PairingStatePaired
		cfg.ManagedHostSync = startupconfig.ManagedHostSyncConfig{
			Mode:              "managed",
			Modules:           []string{"credentials", "agents"},
			OwnerSwarmID:      "stale-manager-swarm",
			HostAPIBaseURL:    "https://stale-manager.example.test",
			SyncCredentialURL: "https://stale-manager.example.test/v1/deploy/container/sync/credentials",
			SyncAgentURL:      "https://stale-manager.example.test/v1/deploy/container/sync/agents",
		}
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"manager_swarm_id": "manager-swarm-1",
		"managed_swarm_id": "managed-swarm-1",
		"propagate":        false,
		"reason":           "manager rejected link request",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if detachCalls != 1 {
		t.Fatalf("detach calls = %d, want 1", detachCalls)
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || !response.LocalRemoved || response.Pairing.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("response = %+v", response)
	}
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if cfg.Child || cfg.SwarmRole != "" || cfg.ParentSwarmID != "" || cfg.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("config not detached: child=%t role=%q parent=%q pairing=%q", cfg.Child, cfg.SwarmRole, cfg.ParentSwarmID, cfg.PairingState)
	}
	if cfg.ManagedHostSync.Mode != "" || len(cfg.ManagedHostSync.Modules) != 0 || cfg.ManagedHostSync.OwnerSwarmID != "" || cfg.ManagedHostSync.HostAPIBaseURL != "" || cfg.ManagedHostSync.SyncCredentialURL != "" || cfg.ManagedHostSync.SyncAgentURL != "" {
		t.Fatalf("managed sync config not scrubbed: %+v", cfg.ManagedHostSync)
	}
}

func TestSwarmManagedHostRemoveRejectsManagerWithoutManagedID(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
		cfg.PairingState = ""
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "managed swarm id is required") {
		t.Fatalf("remove status/body = %d/%s", rec.Code, rec.Body.String())
	}
}

func TestSwarmManagedHostRemoveFromManagerCanPropagate(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{
		state: swarmruntime.LocalState{
			Node:         swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"},
			TrustedPeers: []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm-1", Relationship: swarmruntime.RelationshipManaged}},
		},
		outgoingPeerAuthTokens: map[string]string{"managed-swarm-1": "manager-to-managed-token"},
	}
	server.SetDeployContainerService(&fakeManagedHostRemoveDeployService{deployments: []deployruntime.ContainerDeployment{{
		ID:             "deploy-a",
		AccountScopeID: "acct_local_auth_test",
		ChildSwarmID:   "managed-swarm-1",
	}}})
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
	})

	var remoteCalled bool
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/managed-host/remove" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "manager-swarm-1" || r.Header.Get(peerAuthTokenHeader) == "" {
			t.Fatalf("missing peer auth headers")
		}
		remoteCalled = true
		writeJSON(w, http.StatusOK, swarmManagedHostRemoveResponse{OK: true, Role: startupconfig.SwarmRoleManaged, LocalRemoved: true})
	}))
	defer remoteServer.Close()

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"managed_swarm_id": "managed-swarm-1",
		"endpoint":         remoteServer.URL,
		"propagate":        true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !remoteCalled {
		t.Fatalf("manager removal did not propagate to managed host")
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.LocalRemoved || !response.RemoteRemoved || !response.Cleanup.RemovedTrustedPeer {
		t.Fatalf("response = %+v", response)
	}
}

func TestSwarmManagedHostRemoveFromManagerReportsMissingPropagationInputs(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:         swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"},
		TrustedPeers: []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm-1", Relationship: swarmruntime.RelationshipManaged}},
	}}
	server.SetDeployContainerService(&fakeManagedHostRemoveDeployService{deployments: []deployruntime.ContainerDeployment{{
		ID:             "deploy-a",
		AccountScopeID: "acct_local_auth_test",
		ChildSwarmID:   "managed-swarm-1",
	}}})
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"managed_swarm_id": "managed-swarm-1",
		"propagate":        true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.LocalRemoved || response.RemoteError == "" {
		t.Fatalf("expected local removal with remote propagation error, got %+v", response)
	}
}

func TestSwarmManagedHostRemoveRejectsCrossAccountManagedHost(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:         swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"},
		TrustedPeers: []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm-1", Relationship: swarmruntime.RelationshipManaged}},
	}}
	server.SetDeployContainerService(fakeManagedHostRemoveDeployService{deployments: []deployruntime.ContainerDeployment{{
		ID:             "deploy-a",
		AccountScopeID: "account-a",
		ChildSwarmID:   "managed-swarm-1",
	}}})
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"managed_swarm_id": "managed-swarm-1",
		"propagate":        false,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestSwarmManagedHostRemoveCascadesAccountCleanup(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:         swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"},
		TrustedPeers: []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm-1", Relationship: swarmruntime.RelationshipManaged}},
	}}
	cleanup := &fakeManagedHostRemoveDeployService{deployments: []deployruntime.ContainerDeployment{{
		ID:             "deploy-a",
		AccountScopeID: "acct_local_auth_test",
		ChildSwarmID:   "managed-swarm-1",
	}}}
	server.SetDeployContainerService(cleanup)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"managed_swarm_id": "managed-swarm-1",
		"propagate":        false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cleanup.deletedAccountScopeID != "acct_local_auth_test" || cleanup.deletedManagedSwarmID != "managed-swarm-1" {
		t.Fatalf("cleanup not account scoped: account=%q swarm=%q", cleanup.deletedAccountScopeID, cleanup.deletedManagedSwarmID)
	}
}

type fakeManagedHostRemoveDeployService struct {
	deployments           []deployruntime.ContainerDeployment
	deletedAccountScopeID string
	deletedManagedSwarmID string
}

func (f fakeManagedHostRemoveDeployService) RuntimeStatus(context.Context) (deployruntime.ContainerRuntimeStatus, error) {
	return deployruntime.ContainerRuntimeStatus{}, nil
}

func (f fakeManagedHostRemoveDeployService) List(context.Context) ([]deployruntime.ContainerDeployment, error) {
	return append([]deployruntime.ContainerDeployment(nil), f.deployments...), nil
}

func (f fakeManagedHostRemoveDeployService) Create(context.Context, deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f fakeManagedHostRemoveDeployService) Act(context.Context, deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f fakeManagedHostRemoveDeployService) Delete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f fakeManagedHostRemoveDeployService) ChildAttachState(context.Context, deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	return swarmruntime.LocalState{}, nil
}

func (f fakeManagedHostRemoveDeployService) AttachRequest(context.Context, deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostRemoveDeployService) AttachStatus(context.Context, deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostRemoveDeployService) AttachApprove(context.Context, deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostRemoveDeployService) FinalizeAttachFromHost(context.Context, deployruntime.ContainerAttachFinalizeInput) error {
	return nil
}

func (f fakeManagedHostRemoveDeployService) SyncCredentialBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f fakeManagedHostRemoveDeployService) SyncAgentBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error) {
	return deployruntime.ContainerSyncAgentBundle{}, nil
}

func (f fakeManagedHostRemoveDeployService) WorkspaceBootstrap(context.Context, deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error) {
	return nil, nil
}

func (f fakeManagedHostRemoveDeployService) AutoAttachChild(context.Context) error { return nil }

func (f fakeManagedHostRemoveDeployService) UnlockManagedLocalChildVaults(context.Context) error {
	return nil
}

func (f *fakeManagedHostRemoveDeployService) DeleteManagedHostForAccount(_ context.Context, accountScopeID, managedSwarmID string) (localcontainers.DeleteResult, error) {
	f.deletedAccountScopeID = accountScopeID
	f.deletedManagedSwarmID = managedSwarmID
	return localcontainers.DeleteResult{Count: 1, Deleted: []string{"deploy-a"}}, nil
}

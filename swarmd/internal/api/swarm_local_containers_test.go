package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestSwarmLocalContainersProxyToManagedHostWithPersistedPairingPrincipal(t *testing.T) {
	managedStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed.pebble"))
	if err != nil {
		t.Fatalf("open managed store: %v", err)
	}
	defer func() { _ = managedStore.Close() }()
	managedSwarmStore := pebblestore.NewSwarmStore(managedStore)
	if _, err := managedSwarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: "manager-swarm", UserID: "linked-user", AccountScopeID: "linked-account", PairingState: startupconfig.PairingStatePaired}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	managed := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "managed.conf"),
		security:          security.NewService(pebblestore.NewClientAuthStore(managedStore), nil),
	}
	managed.SetSwarmStore(managedSwarmStore)
	managed.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm", Name: "managed", Role: "managed"}}, token: "manager-token"})
	managed.SetLocalContainerService(&recordingLocalContainerService{containersByAccount: map[string][]localcontainers.Container{
		"linked-account": {{ID: "managed-container", Name: "managed container", Status: "running", HostAPIBaseURL: "http://127.0.0.1:7782"}},
	}})
	managedHTTP := httptest.NewServer(managed.Handler())
	defer managedHTTP.Close()

	primaryStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "primary.pebble"))
	if err != nil {
		t.Fatalf("open primary store: %v", err)
	}
	defer func() { _ = primaryStore.Close() }()
	primary := &Server{startupConfigPath: filepath.Join(t.TempDir(), "primary.conf")}
	primary.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{
		Node:           swarmruntime.LocalNodeState{SwarmID: "manager-swarm", Name: "manager", Role: "master"},
		TrustedPeers:   []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm", Name: "managed", Role: swarmruntime.RelationshipManaged, Relationship: swarmruntime.RelationshipManaged, RendezvousTransports: []swarmruntime.TransportSummary{{Kind: startupconfig.NetworkModeTailscale, Primary: managedHTTP.URL}}}},
		Groups:         []swarmruntime.GroupState{{Group: swarmruntime.Group{ID: "group-1", HostSwarmID: "manager-swarm"}, Members: []swarmruntime.GroupMember{{GroupID: "group-1", SwarmID: "manager-swarm", MembershipRole: swarmruntime.GroupMembershipRoleHost}, {GroupID: "group-1", SwarmID: "managed-swarm", MembershipRole: swarmruntime.GroupMembershipRoleMember}}}},
		CurrentGroupID: "group-1",
	}, token: "manager-token"})
	primary.SetSwarmDesktopTargetSelectionStore(pebblestore.NewSwarmDesktopTargetSelectionStore(primaryStore))

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/containers/local?swarm_id=managed-swarm", nil)
	req.Header.Set("X-Swarm-Principal-User-ID", "spoof-user")
	req.Header.Set("X-Swarm-Principal-Account-Scope-ID", "spoof-account")
	rec := httptest.NewRecorder()
	primary.handleSwarmLocalContainers(rec, requestWithTestPrincipalForAccount(req, "linked-user", "linked-account"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Containers []localcontainers.Container `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Containers) != 1 || response.Containers[0].ID != "managed-container" {
		t.Fatalf("containers = %+v, want managed container", response.Containers)
	}
}

func TestSwarmLocalContainersRejectsCrossAccountManagedTargetWithoutLocalFallback(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "primary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-a", pebblestore.TopologyRuntimeRecord{SwarmID: "managed-swarm", UserID: "user-a", AccountScopeID: "account-a", Name: "managed", Relationship: swarmruntime.RelationshipManaged, BackendURL: "https://managed.example.test"}); err != nil {
		t.Fatalf("put managed topology: %v", err)
	}
	server := &Server{startupConfigPath: filepath.Join(t.TempDir(), "primary.conf")}
	server.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm", Name: "manager", Role: "master"}}, token: "peer-token"})
	server.SetTopologyService(topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, nil))
	server.SetLocalContainerService(&recordingLocalContainerService{containersByAccount: map[string][]localcontainers.Container{
		"account-b": {{ID: "local-b", Name: "local b", Status: "running", HostAPIBaseURL: "http://127.0.0.1:7783"}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/containers/local?swarm_id=managed-swarm", nil)
	rec := httptest.NewRecorder()
	server.handleSwarmLocalContainers(rec, requestWithTestPrincipalForAccount(req, "user-b", "account-b"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "local-b") {
		t.Fatalf("cross-account denied target fell back to local containers: %s", rec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodPost, "/v1/swarm/containers/local/delete?swarm_id=managed-swarm", bytes.NewBufferString(`{"ids":["managed-container","managed-name"]}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	server.handleSwarmLocalContainerDelete(deleteRec, requestWithTestPrincipalForAccount(deleteReq, "user-b", "account-b"))
	if deleteRec.Code != http.StatusBadGateway {
		t.Fatalf("delete status = %d, want %d, body=%s", deleteRec.Code, http.StatusBadGateway, deleteRec.Body.String())
	}
	if strings.Contains(deleteRec.Body.String(), "managed-container") || strings.Contains(deleteRec.Body.String(), "managed-name") {
		t.Fatalf("cross-account delete leaked guessed ids: %s", deleteRec.Body.String())
	}
}

func TestTrustedPairingPrincipalForPeerRequestIgnoresSpoofedHeaders(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: "manager-swarm", UserID: "linked-user", AccountScopeID: "linked-account", PairingState: startupconfig.PairingStatePaired}); err != nil {
		t.Fatalf("put pairing: %v", err)
	}
	server := &Server{startupConfigPath: filepath.Join(t.TempDir(), "managed.conf")}
	server.SetSwarmStore(swarmStore)
	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/containers/local", nil)
	req.Header.Set("X-Swarm-Principal-User-ID", "spoof-user")
	req.Header.Set("X-Swarm-Principal-Account-Scope-ID", "spoof-account")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "manager-swarm"}))

	principal, ok := PrincipalFromRequest(server.requestWithPeerSessionPrincipal(req))
	if !ok {
		t.Fatal("expected trusted pairing principal")
	}
	if principal.UserID != "linked-user" || principal.AccountScopeID != "linked-account" {
		t.Fatalf("principal = %q/%q, want persisted pairing", principal.UserID, principal.AccountScopeID)
	}
}

type recordingLocalContainerService struct {
	containersByAccount map[string][]localcontainers.Container
}

func (f *recordingLocalContainerService) RuntimeStatus(context.Context) (localcontainers.RuntimeStatus, error) {
	return localcontainers.RuntimeStatus{PathID: localcontainers.PathRuntimeStatus}, nil
}

func (f *recordingLocalContainerService) List(context.Context) ([]localcontainers.Container, error) {
	return nil, nil
}

func (f *recordingLocalContainerService) ListForAccount(_ context.Context, accountScopeID string) ([]localcontainers.Container, error) {
	return append([]localcontainers.Container(nil), f.containersByAccount[accountScopeID]...), nil
}

func (f *recordingLocalContainerService) Create(_ context.Context, input localcontainers.CreateInput) (localcontainers.Container, error) {
	return localcontainers.Container{ID: input.Name, Name: input.Name, Status: "running", HostAPIBaseURL: input.HostAPIBaseURL}, nil
}

func (f *recordingLocalContainerService) Act(_ context.Context, input localcontainers.ActionInput) (localcontainers.Container, error) {
	return localcontainers.Container{ID: input.ID, Status: input.Action}, nil
}

func (f *recordingLocalContainerService) BulkDelete(_ context.Context, containerIDs []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{Deleted: append([]string(nil), containerIDs...), Count: len(containerIDs)}, nil
}

func (f *recordingLocalContainerService) PruneMissing(context.Context) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *recordingLocalContainerService) UpdatePlan(context.Context, localcontainers.UpdatePlanInput) (localcontainers.UpdatePlan, error) {
	return localcontainers.UpdatePlan{}, nil
}

func (f *recordingLocalContainerService) RunUpdateJob(context.Context, localcontainers.UpdateJobInput) (localcontainers.UpdateJobResult, error) {
	return localcontainers.UpdateJobResult{}, nil
}

func (f *recordingLocalContainerService) SetHostCallbackURL(string, string) {}

func (f *recordingLocalContainerService) HostCallbackURL(string) (string, bool) { return "", false }

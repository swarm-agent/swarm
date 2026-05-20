package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestPeerMirrorSnapshotRequiresPeerAuth(t *testing.T) {
	server, cleanup := newMirrorTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, peerMirrorSnapshotPath, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot without peer auth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPeerMirrorSnapshotListsResourcesAndWatchReturnsBookmark(t *testing.T) {
	server, cleanup := newMirrorTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, peerMirrorSnapshotPath+"?resources=host,workspaces", nil)
	req.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	req.Header.Set(peerAuthTokenHeader, "manager-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var snapshot peerMirrorSnapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshot.OK || snapshot.Sequence == 0 {
		t.Fatalf("snapshot ok=%t sequence=%d", snapshot.OK, snapshot.Sequence)
	}
	kinds := map[string]bool{}
	for _, resource := range snapshot.Resources {
		kinds[resource.Kind] = true
	}
	if !kinds[mirrorResourceHost] || !kinds[mirrorResourceWorkspace] {
		t.Fatalf("snapshot kinds = %#v, want host and workspace", kinds)
	}

	watchReq := httptest.NewRequest(http.MethodGet, peerMirrorWatchPath+"?since_seq="+uintToString(snapshot.Sequence+1000)+"&allow_bookmarks=true", nil)
	watchReq.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	watchReq.Header.Set(peerAuthTokenHeader, "manager-token")
	watchRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(watchRec, watchReq)
	if watchRec.Code != http.StatusConflict {
		t.Fatalf("watch status = %d body=%s", watchRec.Code, watchRec.Body.String())
	}
	var watch peerMirrorWatchResponse
	if err := json.Unmarshal(watchRec.Body.Bytes(), &watch); err != nil {
		t.Fatalf("decode watch: %v", err)
	}
	if !watch.ResyncRequired {
		t.Fatalf("watch resync_required = false, response=%#v", watch)
	}
}

func TestMirroredManagedHostChildTargetIsSelectableViaOwnerHostGroup(t *testing.T) {
	primary, primaryCleanup := newMirrorTestServer(t)
	defer primaryCleanup()

	primaryState := swarmStateWithManagedPeer("https://managed.example.test", "host-to-managed-token")
	setReplicateFakeSwarmState(primary, primaryState)
	primary.SetDeployContainerService(&fakeReplicateDeployService{lastMirroredDeployment: deployruntime.ContainerDeployment{
		ID:              "managed-child-deployment",
		Name:            "managed-child",
		AttachStatus:    "attached",
		HostSwarmID:     "managed-swarm-1",
		ChildSwarmID:    "child-swarm-1",
		ChildBackendURL: "http://127.0.0.1:7782",
	}})
	targetBytes, err := json.Marshal(swarmTarget{
		SwarmID:      "child-swarm-1",
		Name:         "managed-child",
		Role:         "child",
		Relationship: swarmruntime.RelationshipChild,
		Kind:         "local",
		Online:       true,
		Selectable:   true,
		BackendURL:   "http://127.0.0.1:7782",
	})
	if err != nil {
		t.Fatalf("marshal mirrored target: %v", err)
	}
	if _, err := primary.swarmMirror.UpsertRemoteResource("managed-swarm-1", pebblestore.SwarmMirrorEventRecord{Sequence: 1, EventType: pebblestore.SwarmMirrorEventTypeUpsert, Kind: mirrorResourceTarget, ID: "child-swarm-1", Resource: targetBytes}); err != nil {
		t.Fatalf("upsert mirrored target: %v", err)
	}
	if err := primary.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:          "child-swarm-1",
		Name:             "managed-child",
		Relationship:     "child",
		BackendURL:       "http://127.0.0.1:7782",
		OwnerHostSwarmID: "managed-swarm-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets?swarm_id=child-swarm-1", nil)
	rec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("targets status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp swarmTargetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode targets: %v", err)
	}
	foundCurrent := false
	for _, target := range resp.Targets {
		if target.SwarmID == "child-swarm-1" && target.Kind == "mirrored" && target.Current {
			foundCurrent = true
			if target.BackendURL != "http://127.0.0.1:7782" || target.HostSwarmID != "managed-swarm-1" || !target.Online || !target.Selectable {
				t.Fatalf("mirrored target = %+v, want child backend plus owner host pointer", target)
			}
			if got := primary.proxyBackendURLForTarget(target); got != "https://managed.example.test" {
				t.Fatalf("proxy backend url = %q, want managed owner host backend", got)
			}
		}
	}
	if !foundCurrent {
		t.Fatalf("targets = %#v, want current mirrored managed child", resp.Targets)
	}
	if peerSwarmID := primary.peerAuthSwarmIDForTarget(swarmTarget{SwarmID: "child-swarm-1", Kind: "mirrored"}); peerSwarmID != "managed-swarm-1" {
		t.Fatalf("peer auth swarm id = %q, want managed-swarm-1", peerSwarmID)
	}
	if token, err := primary.outgoingPeerAuthTokenForTarget(req, swarmTarget{SwarmID: "child-swarm-1", Kind: "mirrored"}); err != nil || token != "host-to-managed-token" {
		t.Fatalf("mirrored child peer token = %q err=%v, want managed host token", token, err)
	}
}

func TestSyncMirrorFromTargetAppliesTargetToSwarmTargets(t *testing.T) {
	primary, primaryCleanup := newMirrorTestServer(t)
	defer primaryCleanup()
	managed, managedCleanup := newMirrorTestServer(t)
	defer managedCleanup()

	managed.SetDeployContainerService(&fakeReplicateDeployService{})
	managed.SetSwarmService(fakeReplicateSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "managed-host", Role: "managed"}}, incomingTokens: map[string]string{"host-swarm-id": "host-to-managed-token"}})
	managedMux := managed.Handler()
	managedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/swarm/peer/mirror/") && (r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "host-to-managed-token") {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		managedMux.ServeHTTP(w, r)
	}))
	defer managedServer.Close()

	primaryState := swarmStateWithManagedPeer(managedServer.URL, "host-to-managed-token")
	primaryState.Groups[0].Members = append(primaryState.Groups[0].Members, swarmruntime.GroupMember{GroupID: "group-1", SwarmID: "child-swarm-1", Name: "child", SwarmRole: "child", MembershipRole: swarmruntime.GroupMembershipRoleMember})
	setReplicateFakeSwarmState(primary, primaryState)
	if err := primary.syncMirrorFromTarget(reqContext(), swarmTarget{SwarmID: "managed-swarm-1", BackendURL: managedServer.URL}); err != nil {
		t.Fatalf("sync mirror: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	rec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("targets status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp swarmTargetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode targets: %v", err)
	}
	found := false
	for _, target := range resp.Targets {
		if target.SwarmID == "child-swarm-1" && target.Kind == "mirrored" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mirrored child target not found in %#v", resp.Targets)
	}
}

func newMirrorTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("event log: %v", err)
	}
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	workspacePath := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		_ = store.Close()
		t.Fatalf("create workspace dir: %v", err)
	}
	if _, err := workspaceSvc.AddForPrincipal(testPrincipal(), workspacePath, "workspace", "", false); err != nil {
		_ = store.Close()
		t.Fatalf("add workspace: %v", err)
	}
	startupPath := filepath.Join(dir, "startup.json")
	if err := startupconfig.Write(startupconfig.FileConfig{Path: startupPath, Mode: startupconfig.ModeInteractive, Host: "127.0.0.1", Port: startupconfig.DefaultPort, AdvertiseHost: "127.0.0.1", AdvertisePort: startupconfig.DefaultPort, DesktopPort: startupconfig.DefaultDesktopPort, PeerTransportPort: startupconfig.DefaultPeerTransportPort, SwarmName: "host-swarm", NetworkMode: startupconfig.NetworkModeTailscale}); err != nil {
		_ = store.Close()
		t.Fatalf("write startup config: %v", err)
	}
	server := NewServer("test", nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmMirrorStore(pebblestore.NewSwarmMirrorStore(store))
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, nil, workspaceStore))
	server.SetSwarmNodeStore(pebblestore.NewSwarmNodeStore(store))
	server.SetSwarmDesktopTargetSelectionStore(pebblestore.NewSwarmDesktopTargetSelectionStore(store))
	server.SetSwarmService(fakeReplicateSwarmService{
		state: swarmruntime.LocalState{
			Node:    swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
			Pairing: swarmruntime.PairingState{PairingState: "managed", ParentSwarmID: "manager-swarm"},
		},
		outgoingTokens: map[string]string{"managed-swarm-1": "host-to-managed-token"},
		incomingTokens: map[string]string{"manager-swarm": "manager-token", "host-swarm-id": "host-to-managed-token"},
	})
	return server, func() { _ = store.Close() }
}

func reqContext() context.Context { return context.Background() }

func uintToString(value uint64) string {
	return strconv.FormatUint(value, 10)
}

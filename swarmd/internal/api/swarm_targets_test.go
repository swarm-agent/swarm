package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestHandleSwarmTargetsReturnsRenamedSelfTargetImmediately(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-local-rename.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	swarmSvc := swarmruntime.NewService(pebblestore.NewSwarmStore(store), nil, nil)
	initial, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{Name: "Initial Primary", Role: "master", SwarmMode: true})
	if err != nil {
		t.Fatalf("ensure local swarm: %v", err)
	}
	if _, err := swarmSvc.RenameLocalSwarm(swarmruntime.RenameLocalSwarmInput{Name: "Renamed Primary"}); err != nil {
		t.Fatalf("rename local swarm: %v", err)
	}

	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm:             swarmSvc,
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	rec := httptest.NewRecorder()
	server.handleSwarmTargets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/swarm/targets status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmTargetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Targets) == 0 {
		t.Fatal("expected at least one target")
	}
	self := response.Targets[0]
	if self.Name != "Renamed Primary" {
		t.Fatalf("self target name = %q, want renamed DB name", self.Name)
	}
	if self.SwarmID != initial.Node.SwarmID {
		t.Fatalf("self target swarm id = %q, want stable %q", self.SwarmID, initial.Node.SwarmID)
	}
	if !self.Current {
		t.Fatalf("self target should be current: %+v", self)
	}
}

func TestSwarmTargetsForRequestPrefersRegistryNodes(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	nodes := pebblestore.NewSwarmNodeStore(store)
	backendURL := "http://swarm-child.tailnet.ts.net:8421"
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{
		SwarmID:      "swarm-child-1",
		Name:         "registry-child",
		Role:         "child",
		Kind:         "remote",
		Transport:    "tailscale",
		BackendURL:   backendURL,
		DesktopURL:   "https://swarm-child.tailnet.ts.net",
		DeploymentID: "deploy-registry",
		Status:       "online",
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}

	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm: fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{
			SwarmID: "local-swarm",
			Name:    "controller",
			Role:    "master",
		}}},
		swarmNodes: nodes,
		remoteDeploys: &fakeRemoteDeployService{sessions: []remotedeploy.Session{{
			ID:             "legacy-remote-1",
			Name:           "legacy-child",
			Status:         "attached",
			ChildSwarmID:   "swarm-child-1",
			RemoteEndpoint: "http://legacy-child:7781",
		}}},
		swarmTargetHealth: swarmTargetHealthCache{entries: map[string]swarmTargetHealthEntry{
			"remote|swarm-child-1|" + backendURL: {online: true, checkedAt: time.Now()},
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets?swarm_id=swarm-child-1", nil)
	targets, current, err := server.swarmTargetsForRequest(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if current == nil {
		t.Fatal("expected current target")
	}
	if current.SwarmID != "swarm-child-1" {
		t.Fatalf("current swarm id = %q", current.SwarmID)
	}

	var childTargets []swarmTarget
	for _, target := range targets {
		if target.SwarmID == "swarm-child-1" {
			childTargets = append(childTargets, target)
		}
	}
	if len(childTargets) != 1 {
		t.Fatalf("registry/legacy child target count = %d, targets=%+v", len(childTargets), targets)
	}
	child := childTargets[0]
	if child.Name != "registry-child" {
		t.Fatalf("child name = %q", child.Name)
	}
	if child.BackendURL != backendURL {
		t.Fatalf("child backend url = %q", child.BackendURL)
	}
	if child.DeploymentID != "deploy-registry" {
		t.Fatalf("child deployment id = %q", child.DeploymentID)
	}
	if !child.Online || !child.Selectable {
		t.Fatalf("child should be online/selectable: %+v", child)
	}
	if !child.Current {
		t.Fatalf("child should be current: %+v", child)
	}
}

func TestSwarmTargetsForRequestKeepsMirroredTargetsSharingHostLocalBackend(t *testing.T) {
	server, _ := newFlowPeerTestServer(t)
	targetBytes, err := json.Marshal(swarmTarget{
		SwarmID:      "managed-child-new",
		Name:         "managed child new",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "deployment-new",
		HostSwarmID:  "managed-swarm-1",
		Online:       true,
		Selectable:   true,
		BackendURL:   "http://127.0.0.1:7782",
	})
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	if _, err := server.swarmMirror.UpsertRemoteResource("managed-swarm-1", pebblestore.SwarmMirrorEventRecord{Sequence: 1, EventType: pebblestore.SwarmMirrorEventTypeUpsert, Kind: mirrorResourceTarget, ID: "managed-child-new", Resource: targetBytes}); err != nil {
		t.Fatalf("upsert new mirrored target: %v", err)
	}
	olderTargetBytes, err := json.Marshal(swarmTarget{
		SwarmID:      "managed-child-old",
		Name:         "managed child old",
		Relationship: "child",
		Kind:         "local",
		DeploymentID: "deployment-old",
		HostSwarmID:  "managed-swarm-1",
		Online:       true,
		Selectable:   true,
		BackendURL:   "http://127.0.0.1:7782",
	})
	if err != nil {
		t.Fatalf("marshal older target: %v", err)
	}
	if _, err := server.swarmMirror.UpsertRemoteResource("managed-swarm-1", pebblestore.SwarmMirrorEventRecord{Sequence: 2, EventType: pebblestore.SwarmMirrorEventTypeUpsert, Kind: mirrorResourceTarget, ID: "managed-child-old", Resource: olderTargetBytes}); err != nil {
		t.Fatalf("upsert older mirrored target: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	targets, _, err := server.swarmTargetsForRequest(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if target.SwarmID == "managed-child-new" || target.SwarmID == "managed-child-old" {
			seen[target.SwarmID] = true
		}
	}
	if !seen["managed-child-new"] || !seen["managed-child-old"] {
		t.Fatalf("mirrored targets sharing owner-local backend were deduped: seen=%v targets=%+v", seen, targets)
	}
}

func TestSwarmTargetsForRequestIncludesTrustedManagedPeerTargets(t *testing.T) {
	managedBackendURL := "https://managed-host.tailnet.ts.net"
	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm: fakeRoutedSwarmService{state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm", Name: "manager", Role: "master"},
			TrustedPeers: []swarmruntime.TrustedPeer{{
				SwarmID:       "managed-swarm-1",
				Name:          "managed-host",
				Role:          swarmruntime.RelationshipManaged,
				Relationship:  swarmruntime.RelationshipManaged,
				TransportMode: startupconfig.NetworkModeTailscale,
				RendezvousTransports: []swarmruntime.TransportSummary{{
					Kind:    startupconfig.NetworkModeTailscale,
					Primary: managedBackendURL,
					All:     []string{managedBackendURL},
				}},
			}},
			Groups: []swarmruntime.GroupState{{
				Group: swarmruntime.Group{ID: "group-1", HostSwarmID: "manager-swarm", Name: "manager group"},
				Members: []swarmruntime.GroupMember{
					{GroupID: "group-1", SwarmID: "manager-swarm", Name: "manager", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
					{GroupID: "group-1", SwarmID: "managed-swarm-1", Name: "managed-host", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
				},
			}},
			CurrentGroupID: "group-1",
		}},
		swarmTargetHealth: swarmTargetHealthCache{entries: map[string]swarmTargetHealthEntry{
			"host|managed-swarm-1|" + managedBackendURL: {online: true, checkedAt: time.Now()},
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets?swarm_id=managed-swarm-1", nil)
	targets, current, err := server.swarmTargetsForRequest(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if current == nil || current.SwarmID != "managed-swarm-1" {
		t.Fatalf("current = %+v, want managed-swarm-1", current)
	}
	var managedTargets []swarmTarget
	for _, target := range targets {
		if target.SwarmID == "managed-swarm-1" {
			managedTargets = append(managedTargets, target)
		}
	}
	if len(managedTargets) != 1 {
		t.Fatalf("managed target count = %d, targets=%+v", len(managedTargets), targets)
	}
	managed := managedTargets[0]
	if managed.Name != "managed-host" || managed.Role != swarmruntime.RelationshipManaged || managed.Relationship != swarmruntime.RelationshipManaged {
		t.Fatalf("managed target identity = %+v", managed)
	}
	if managed.Kind != "host" {
		t.Fatalf("managed target kind = %q, want host", managed.Kind)
	}
	if managed.BackendURL != managedBackendURL || managed.DesktopURL != managedBackendURL {
		t.Fatalf("managed urls = backend %q desktop %q", managed.BackendURL, managed.DesktopURL)
	}
	if !managed.Online || !managed.Selectable || !managed.Current {
		t.Fatalf("managed should be online/selectable/current: %+v", managed)
	}
}

func TestSwarmTargetsForRequestPrefersTrustedManagedPeerOverRegistryNode(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-targets-dedupe.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	nodes := pebblestore.NewSwarmNodeStore(store)
	registryBackendURL := "http://registry-managed.tailnet.ts.net:8421"
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{
		SwarmID:    "managed-swarm-1",
		Name:       "registry-managed",
		Role:       swarmruntime.RelationshipManaged,
		Kind:       "host",
		BackendURL: registryBackendURL,
		Status:     "online",
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}

	server := &Server{
		startupConfigPath: filepath.Join(t.TempDir(), "swarm.conf"),
		swarm: fakeRoutedSwarmService{state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm", Name: "manager", Role: "master"},
			TrustedPeers: []swarmruntime.TrustedPeer{{
				SwarmID:              "managed-swarm-1",
				Name:                 "trusted-managed",
				Role:                 swarmruntime.RelationshipManaged,
				Relationship:         swarmruntime.RelationshipManaged,
				RendezvousTransports: []swarmruntime.TransportSummary{{Kind: startupconfig.NetworkModeTailscale, Primary: "https://trusted-managed.tailnet.ts.net"}},
			}},
		}},
		swarmNodes: nodes,
		swarmTargetHealth: swarmTargetHealthCache{entries: map[string]swarmTargetHealthEntry{
			"host|managed-swarm-1|" + registryBackendURL: {online: true, checkedAt: time.Now()},
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
	targets, _, err := server.swarmTargetsForRequest(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	var managedTargets []swarmTarget
	for _, target := range targets {
		if target.SwarmID == "managed-swarm-1" {
			managedTargets = append(managedTargets, target)
		}
	}
	if len(managedTargets) != 1 {
		t.Fatalf("managed target count = %d, targets=%+v", len(managedTargets), targets)
	}
	if managedTargets[0].Name != "trusted-managed" || managedTargets[0].BackendURL != "https://trusted-managed.tailnet.ts.net" {
		t.Fatalf("expected trusted managed peer target to win, got %+v", managedTargets[0])
	}
	if managedTargets[0].Role != swarmruntime.RelationshipManaged || managedTargets[0].Relationship != swarmruntime.RelationshipManaged || managedTargets[0].Kind != "host" {
		t.Fatalf("expected trusted managed peer identity, got %+v", managedTargets[0])
	}
}

func TestMapRemoteDeployTargetAttachedSession(t *testing.T) {
	target, ok := mapRemoteDeployTarget(remotedeploy.Session{
		ID:               "remote-session-1",
		Name:             "remote-child",
		Status:           "attached",
		ChildSwarmID:     "swarm-child-1",
		RemoteTailnetURL: "https://remote-child.tailnet.ts.net",
	})
	if !ok {
		t.Fatal("expected remote deploy session to map to a swarm target")
	}
	if target.SwarmID != "swarm-child-1" {
		t.Fatalf("swarm_id = %q, want %q", target.SwarmID, "swarm-child-1")
	}
	if target.Relationship != swarmruntime.RelationshipChild {
		t.Fatalf("relationship = %q, want %q", target.Relationship, swarmruntime.RelationshipChild)
	}
	if !target.Online || !target.Selectable {
		t.Fatalf("target should be online and selectable: %+v", target)
	}
	if target.BackendURL != "https://remote-child.tailnet.ts.net" {
		t.Fatalf("backend_url = %q", target.BackendURL)
	}
	if target.DesktopURL != "https://remote-child.tailnet.ts.net" {
		t.Fatalf("desktop_url = %q", target.DesktopURL)
	}
}

func TestMapRemoteDeployTargetAttachedLANSessionUsesRemoteEndpoint(t *testing.T) {
	target, ok := mapRemoteDeployTarget(remotedeploy.Session{
		ID:             "remote-session-1",
		Name:           "remote-child",
		Status:         "attached",
		ChildSwarmID:   "swarm-child-1",
		RemoteEndpoint: "http://10.44.1.10:7781",
	})
	if !ok {
		t.Fatal("expected remote deploy session to map to a swarm target")
	}
	if !target.Online || !target.Selectable {
		t.Fatalf("target should be online and selectable: %+v", target)
	}
	if target.BackendURL != "http://10.44.1.10:7781" {
		t.Fatalf("backend_url = %q", target.BackendURL)
	}
	if target.DesktopURL != "http://10.44.1.10:7781" {
		t.Fatalf("desktop_url = %q", target.DesktopURL)
	}
}

func TestMapRemoteDeployTargetRequiresChildSwarmID(t *testing.T) {
	if _, ok := mapRemoteDeployTarget(remotedeploy.Session{
		ID:               "remote-session-1",
		Status:           "attached",
		RemoteTailnetURL: "https://remote-child.tailnet.ts.net",
	}); ok {
		t.Fatal("expected remote deploy session without child swarm id to be skipped")
	}
}

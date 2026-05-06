package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

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

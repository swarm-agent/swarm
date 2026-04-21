package api

import (
	"testing"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

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

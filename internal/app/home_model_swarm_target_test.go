package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestWorkspaceOverviewSwarmTargetMapsIntoHomeModel(t *testing.T) {
	overview := client.WorkspaceOverviewResponse{
		SwarmTarget: &client.WorkspaceOverviewSwarmTarget{
			SwarmID:      " host-swarm ",
			Name:         " Host Swarm ",
			Role:         " master ",
			Relationship: " self ",
			Kind:         " local ",
			Online:       true,
			Selectable:   true,
			Current:      true,
		},
	}
	home := model.EmptyHome()
	home.CurrentSwarmTarget = modelSwarmTargetFromClient(overview.SwarmTarget)

	if home.CurrentSwarmTarget == nil {
		t.Fatalf("CurrentSwarmTarget is nil")
	}
	if home.CurrentSwarmTarget.SwarmID != "host-swarm" {
		t.Fatalf("CurrentSwarmTarget.SwarmID = %q, want host-swarm", home.CurrentSwarmTarget.SwarmID)
	}
	if home.CurrentSwarmTarget.Name != "Host Swarm" {
		t.Fatalf("CurrentSwarmTarget.Name = %q, want Host Swarm", home.CurrentSwarmTarget.Name)
	}
	if !home.CurrentSwarmTarget.Online || !home.CurrentSwarmTarget.Selectable || !home.CurrentSwarmTarget.Current {
		t.Fatalf("CurrentSwarmTarget booleans not propagated: %+v", home.CurrentSwarmTarget)
	}
}

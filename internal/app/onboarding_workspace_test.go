package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeModelHasReadyWorkspaceRequiresActiveLaunchPath(t *testing.T) {
	home := model.HomeModel{Workspaces: []model.Workspace{
		{Name: "Other", Path: "/other", Active: false},
		{Name: "Launch", Path: "/repo/project", Active: true},
	}}
	if !homeModelHasReadyWorkspace(home, "/repo/project") {
		t.Fatal("active launch workspace was not recognized as ready")
	}
	if homeModelHasReadyWorkspace(home, "/other") {
		t.Fatal("inactive workspace must not release onboarding")
	}
	if homeModelHasReadyWorkspace(home, "/missing") {
		t.Fatal("missing workspace must not release onboarding")
	}
}

package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: onboarding completes only for the active launch workspace once Git
// has a resolvable HEAD. The threat is releasing first launch after registration
// alone, leaving later managed-worktree creation to fail. This helper-level test is
// the narrowest proof of the readiness gate used after the API refresh.
func TestHomeModelHasReadyWorkspaceRequiresActiveLaunchPathAndGitHead(t *testing.T) {
	home := model.HomeModel{
		Workspaces: []model.Workspace{
			{Name: "Other", Path: "/other", Active: false},
			{Name: "Launch", Path: "/repo/project", Active: true},
		},
		Directories: []model.DirectoryItem{{ResolvedPath: "/repo/project", HasGit: true, GitReadiness: model.GitReadinessReady, IsWorkspace: true}},
	}
	if !homeModelHasReadyWorkspace(home, "/repo/project") {
		t.Fatal("active launch workspace was not recognized as ready")
	}
	if homeModelHasReadyWorkspace(home, "/other") {
		t.Fatal("inactive workspace must not release onboarding")
	}
	if homeModelHasReadyWorkspace(home, "/missing") {
		t.Fatal("missing workspace must not release onboarding")
	}
	home.Directories[0].GitReadiness = model.GitReadinessNeedsCommit
	if homeModelHasReadyWorkspace(home, "/repo/project") {
		t.Fatal("workspace without a first commit must not release onboarding")
	}
	if got := homeModelWorkspaceGitReadiness(home, "/repo/project"); got != model.GitReadinessNeedsCommit {
		t.Fatalf("workspace git readiness = %q", got)
	}
}

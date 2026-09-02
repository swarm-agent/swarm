package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: onboarding completes when the requested workspace is registered
// and active; Git readiness affects optional Git features, not workspace use.
// Threat: coupling registration to a repository HEAD locks new users out of plain
// directories and unborn repositories. This helper is the narrowest post-refresh
// gate used by onboarding.
func TestHomeModelHasActiveWorkspaceDoesNotRequireGit(t *testing.T) {
	for _, readiness := range []model.GitReadiness{
		model.GitReadinessUnavailable,
		model.GitReadinessNotRepository,
		model.GitReadinessNeedsCommit,
		model.GitReadinessReady,
	} {
		home := model.HomeModel{
			Workspaces: []model.Workspace{
				{Name: "Other", Path: "/other", Active: false},
				{Name: "Launch", Path: "/repo/project", Active: true},
			},
			Directories: []model.DirectoryItem{{ResolvedPath: "/repo/project", HasGit: readiness == model.GitReadinessReady || readiness == model.GitReadinessNeedsCommit, GitReadiness: readiness, IsWorkspace: true}},
		}
		if !homeModelHasActiveWorkspace(home, "/repo/project") {
			t.Errorf("active workspace rejected for Git readiness %q", readiness)
		}
		if homeModelHasActiveWorkspace(home, "/other") {
			t.Fatal("inactive workspace must not release onboarding")
		}
		if homeModelHasActiveWorkspace(home, "/missing") {
			t.Fatal("missing workspace must not release onboarding")
		}
		if got := homeModelWorkspaceGitReadiness(home, "/repo/project"); got != readiness {
			t.Fatalf("workspace git readiness = %q, want %q", got, readiness)
		}
	}
}

package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: onboarding releases only for an active workspace backed by a
// committed Git repository, because every normal agent session uses managed
// worktree isolation. The threat is admitting a plain or unborn directory that
// cannot satisfy the runtime contract. This helper is the narrowest post-refresh
// gate used by onboarding.
func TestHomeModelHasActiveWorkspaceRequiresCommittedRepository(t *testing.T) {
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
		gotReady := homeModelHasActiveWorkspace(home, "/repo/project")
		if wantReady := readiness == model.GitReadinessReady; gotReady != wantReady {
			t.Errorf("active workspace readiness %q = %v, want %v", readiness, gotReady, wantReady)
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

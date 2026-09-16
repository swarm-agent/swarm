package app

import (
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: acknowledged onboarding selects the admitted project without an
// obsolete warning for the original launch directory. Rejected completion must
// retain the locked UI and diagnostic state. acknowledgeOnboardingHomeModel is
// the narrow app boundary after workspace admission and daemon finalization.
func TestAcknowledgeOnboardingHomeModelClearsOnlyAfterAcknowledgement(t *testing.T) {
	original := model.HomeModel{OnboardingRequired: true, CWD: "/project",
		WorkspaceSetupPath: "/launch", WorkspaceSetupHasGit: true,
		WorkspaceSetupGitReadiness: model.GitReadinessNeedsCommit,
		Workspaces:                 []model.Workspace{{Path: "/project", Active: true}}}
	for _, status := range []client.OnboardingStatus{
		{}, {OK: true, NeedsOnboarding: true}, {NeedsOnboarding: false},
	} {
		got, err := acknowledgeOnboardingHomeModel(original, status)
		if err == nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("unacknowledged completion changed model: %+v, %v", got, err)
		}
	}
	got, err := acknowledgeOnboardingHomeModel(original, client.OnboardingStatus{OK: true})
	if err != nil || got.OnboardingRequired || got.WorkspaceSetupPath != "" || got.WorkspaceSetupHasGit || got.WorkspaceSetupGitReadiness != model.GitReadinessUnknown {
		t.Fatalf("acknowledged completion retained obsolete setup state: %+v, %v", got, err)
	}
	if got.CWD != original.CWD || !reflect.DeepEqual(got.Workspaces, original.Workspaces) {
		t.Fatal("completion changed admitted workspace identity")
	}
}

// Requirement: later canonical workspace refreshes must not revive stale launch
// warnings once explicit selection owns routing. applyHomeWorkspaceBootstrap is
// the narrow model boundary; its existing launch-CWD tests separately retain
// real unregistered/unborn launch-directory warnings.
func TestHomeBootstrapDropsObsoleteSetupAfterExplicitSelection(t *testing.T) {
	original := model.HomeModel{WorkspaceSetupPath: "/old-launch", WorkspaceSetupHasGit: true, WorkspaceSetupGitReadiness: model.GitReadinessNeedsCommit}
	got, selected, _ := applyHomeWorkspaceBootstrap(original, homeBootstrapData{
		current: client.WorkspaceResolution{WorkspacePath: "/project", ResolvedPath: "/project"}, hasCurrent: true,
		workspaces:      []client.WorkspaceEntry{{Path: "/project"}},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/project", Workspace: &client.WorkspaceResolution{WorkspacePath: "/project", ResolvedPath: "/project"}},
	}, "/old-launch")
	if selected != "/project" || got.WorkspaceSetupPath != "" || got.WorkspaceSetupHasGit || got.WorkspaceSetupGitReadiness != model.GitReadinessUnknown {
		t.Fatalf("refresh retained obsolete setup: selected=%q model=%+v", selected, got)
	}
}

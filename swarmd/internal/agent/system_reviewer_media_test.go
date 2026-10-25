package agent_test

import (
	"slices"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/run"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the report-only reviewer must resolve media_inspect without gaining
// mutation or generation tools. ReviewerAgentProfileForParent owns the contract;
// ResolveAgentToolContract and AgentProfileAuthorizesMedia enforce its runtime
// authorization. This focused boundary test prevents visual reviews being
// stranded by missing authorization, and checks denied tools rather than only
// the happy path. It does not prove provider image support or live inspection.
func TestReviewerResolvedToolsIncludeMediaInspectionOnly(t *testing.T) {
	profile := agent.ReviewerAgentProfileForParent(store.AgentProfile{})
	if profile.RuntimeMode != store.AgentRuntimeModeRead || profile.ExecutionSetting != store.AgentExecutionSettingRead {
		t.Fatal("reviewer must remain read-only")
	}
	svc := run.NewService(nil, nil, nil, &tool.Runtime{}, nil, nil, nil, nil)
	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resolved.AvailableTools, "media_inspect") || disabled["media_inspect"] || !run.AgentProfileAuthorizesMedia(profile) {
		t.Fatal("reviewer media inspection must be available and authorized")
	}
	for _, name := range []string{"write", "edit", "manage_media", "manage_artifact", "manage_video", "git_init", "git_add", "git_commit", "git_commit_initial"} {
		if slices.Contains(resolved.AvailableTools, name) || resolved.Tools[name].Enabled {
			t.Errorf("report-only reviewer unexpectedly enables %s", name)
		}
	}
	delete(profile.ToolContract.Tools, "media_inspect")
	resolved, _, _, err = svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(resolved.AvailableTools, "media_inspect") || run.AgentProfileAuthorizesMedia(profile) {
		t.Fatal("media inspection must require explicit contract authorization")
	}
}

package run

import (
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestIntegrationBuilderToolContractRestrictsRuntimeTools(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	profile := agentruntime.IntegrationBuilderProfile()

	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatalf("ResolveAgentToolContract() error = %v", err)
	}

	for _, name := range []string{"read", "search", "list", "websearch", "webfetch", "manage_integrations"} {
		if !resolved.Tools[name].Enabled {
			t.Fatalf("builder tool %s disabled: %+v", name, resolved.Tools[name])
		}
		if disabled[name] {
			t.Fatalf("builder tool %s is marked disabled", name)
		}
	}
	for _, name := range []string{"bash", "write", "edit", "task", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"} {
		if resolved.Tools[name].Enabled {
			t.Fatalf("builder tool %s enabled, want disabled", name)
		}
		if !disabled[name] {
			t.Fatalf("builder tool %s missing from disabled policy map", name)
		}
	}
}

func TestResolveAgentProfileRequiresIntegrationFlowForBuilder(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)

	if _, err := svc.resolveAgentProfile(agentruntime.IntegrationBuilderAgentID, RunTargetKindAgent, false); err == nil {
		t.Fatalf("resolveAgentProfile without integration flow unexpectedly succeeded")
	}
	if _, err := svc.resolveAgentProfile(agentruntime.IntegrationBuilderAgentID, RunTargetKindAgent, true); err == nil {
		t.Fatalf("resolveAgentProfile without agent service unexpectedly resolved builder")
	}

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integration-builder-agent.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), nil)
	svc = NewService(nil, nil, nil, tool.NewRuntime(1), nil, agents, nil, nil)
	profile, err := svc.resolveAgentProfile(agentruntime.IntegrationBuilderAgentID, RunTargetKindAgent, true)
	if err != nil {
		t.Fatalf("resolveAgentProfile integration flow error = %v", err)
	}
	if profile.Name != agentruntime.IntegrationBuilderAgentID || profile.Prompt != agentruntime.IntegrationBuilderPrompt() {
		t.Fatalf("resolved integration builder profile = %+v", profile)
	}
}

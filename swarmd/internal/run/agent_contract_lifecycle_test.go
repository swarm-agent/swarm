package run

import (
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestResolveAgentToolContractUsesSavedBuiltInContracts(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, name := range []string{"swarm", "explorer", "memory", "parallel"} {
		profile, ok := agentruntime.DefaultProfileByName(name)
		if !ok {
			t.Fatalf("DefaultProfileByName(%s) missing", name)
		}
		resolved, _, _, err := svc.ResolveAgentToolContract(profile)
		if err != nil {
			t.Fatalf("ResolveAgentToolContract(%s) error = %v", name, err)
		}
		if strings.TrimSpace(resolved.RawPreset) == "" && len(profile.ToolContract.Tools) == 0 {
			t.Fatalf("%s resolved without persisted preset or explicit tools: %+v", name, resolved)
		}
	}
}

func TestResolveAgentToolContractInheritsOnlyAccountPolicy(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-contract-account-policy.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	if _, err := permissions.UpsertRuleForAccount("account-a", permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: "write"}); err != nil {
		t.Fatalf("upsert account-a rule: %v", err)
	}
	if _, err := permissions.UpsertRuleForAccount("account-b", permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "write"}); err != nil {
		t.Fatalf("upsert account-b rule: %v", err)
	}
	if _, err := permissions.UpsertRule(permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "write"}); err != nil {
		t.Fatalf("upsert unscoped rule: %v", err)
	}
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), permissions, nil, nil, nil)
	profile := pebblestore.AgentProfile{Name: "account-agent", ToolContract: &pebblestore.AgentToolContract{Preset: "custom", InheritPolicy: true}}
	_, compiled, _, err := svc.compileResolvedAgentToolContract("account-a", profile)
	if err != nil {
		t.Fatalf("resolve account contract: %v", err)
	}
	explain := permission.ExplainPolicy("auto", "write", `{}`, *compiled)
	if explain.Decision != permission.PolicyDecisionDeny {
		t.Fatalf("account-a inherited decision = %q, want deny", explain.Decision)
	}
}

func TestResolveAgentToolContractFailsClosedWhenSavedContractMissing(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	_, _, _, err := svc.ResolveAgentToolContract(pebblestore.AgentProfile{
		Name:        "legacy-custom",
		Mode:        agentruntime.ModeSubagent,
		RuntimeMode: pebblestore.AgentRuntimeModeRead,
		Enabled:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "tool_contract is not configured") {
		t.Fatalf("ResolveAgentToolContract() error = %v, want missing tool_contract", err)
	}
}

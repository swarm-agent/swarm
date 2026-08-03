package run

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestResolveAgentToolContractUsesSavedBuiltInContracts(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, name := range []string{"swarm", "finder", "memory", "parallel"} {
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
		if name == agentruntime.SwarmAgentID {
			if _, configured := profile.ToolContract.Tools["manage_todos"]; configured {
				t.Fatalf("Swarm tool contract still configures manage_todos: %+v", profile.ToolContract)
			}
			if todo := resolved.Tools["manage_todos"]; todo.Enabled || slices.Contains(resolved.AvailableTools, "manage_todos") {
				t.Fatalf("Swarm resolved toolkit advertises manage_todos: %+v", resolved)
			}
			if plan := resolved.Tools["plan_manage"]; !plan.Enabled || !slices.Contains(resolved.AvailableTools, "plan_manage") {
				t.Fatalf("Swarm resolved toolkit omits plan_manage: %+v", resolved)
			}
			if !slices.ContainsFunc(svc.ListAgentToolDefinitions(), func(definition tool.Definition) bool { return definition.Name == "manage_todos" }) {
				t.Fatal("shared tool inventory no longer implements manage_todos")
			}
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

func TestResolveAgentToolContractForcesTaskOffForEverySubagent(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, preset := range []string{"custom", "read_only", "read_write"} {
		profile := pebblestore.AgentProfile{
			Name: "subagent-" + preset,
			Mode: agentruntime.ModeSubagent,
			ToolContract: &pebblestore.AgentToolContract{
				Preset: preset,
				Tools: map[string]pebblestore.AgentToolConfig{"task": {Enabled: pebblestore.BoolPtr(true)}},
			},
		}
		resolved, compiled, disabled, err := svc.ResolveAgentToolContract(profile)
		if err != nil {
			t.Fatalf("ResolveAgentToolContract(%s): %v", preset, err)
		}
		if task := resolved.Tools["task"]; task.Enabled || task.Source != "runtime.subagent_boundary" {
			t.Fatalf("subagent preset %q resolved task = %+v", preset, task)
		}
		if !disabled["task"] || slices.Contains(resolved.AvailableTools, "task") {
			t.Fatalf("subagent preset %q advertises task: resolved=%+v disabled=%+v", preset, resolved, disabled)
		}
		if explain := permission.ExplainPolicy("auto", "task", `{}`, *compiled); explain.Decision != permission.PolicyDecisionDeny {
			t.Fatalf("subagent preset %q task policy = %q, want deny", preset, explain.Decision)
		}
	}

	primary := pebblestore.AgentProfile{Name: "primary", Mode: agentruntime.ModePrimary, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"task": {Enabled: pebblestore.BoolPtr(true)}}}}
	resolved, _, disabled, err := svc.ResolveAgentToolContract(primary)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Tools["task"].Enabled || disabled["task"] {
		t.Fatalf("primary task delegation was removed: resolved=%+v disabled=%+v", resolved.Tools["task"], disabled)
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

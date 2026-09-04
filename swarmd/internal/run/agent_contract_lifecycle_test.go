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
			if sessions := resolved.Tools["manage_sessions"]; !sessions.Enabled || !slices.Contains(resolved.AvailableTools, "manage_sessions") {
				t.Fatalf("Swarm resolved toolkit omits manage_sessions: %+v", resolved)
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

func TestResolveAgentToolContractRestoresManageSessionsOnlyForSwarmPrimary(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, tc := range []struct {
		name string
		mode string
		want bool
	}{
		{name: agentruntime.SwarmAgentID, mode: agentruntime.ModePrimary, want: true},
		{name: "other-primary", mode: agentruntime.ModePrimary, want: false},
		{name: agentruntime.SwarmAgentID, mode: agentruntime.ModeSubagent, want: false},
	} {
		t.Run(tc.name+"-"+tc.mode, func(t *testing.T) {
			profile := pebblestore.AgentProfile{Name: tc.name, Mode: tc.mode, ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
				"manage_sessions": {Enabled: pebblestore.BoolPtr(false)},
			}}}
			resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
			if err != nil {
				t.Fatal(err)
			}
			if got := resolved.Tools["manage_sessions"]; got.Enabled != tc.want {
				t.Fatalf("manage_sessions = %+v, want enabled=%v", got, tc.want)
			}
			if disabled["manage_sessions"] == tc.want {
				t.Fatalf("disabled manage_sessions = %v, want %v", disabled["manage_sessions"], !tc.want)
			}
		})
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
				Tools:  map[string]pebblestore.AgentToolConfig{"task": {Enabled: pebblestore.BoolPtr(true)}},
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

// Requirement: onboarding repository mutations always ask even if the account
// policy would otherwise allow them, while discovery remains allowed and broad
// product authority remains denied. Threat: an allow rule or mutable snapshot
// could silently initialize/commit user files. The compiled policy is the
// narrowest layer proving the effective decisions before tool execution.
func TestWorkspaceOnboardingToolContractForcesMutationApproval(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	profile := agentruntime.WorkspaceOnboardingAgentProfileForParent(pebblestore.AgentProfile{Provider: "test", Model: "model", Thinking: "high"})
	resolved, compiled, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"write", "edit", "git_add", "git_commit"} {
		if !resolved.Tools[name].Enabled || disabled[name] {
			t.Fatalf("mutation tool %q not available for approval: resolved=%+v disabled=%v", name, resolved.Tools[name], disabled[name])
		}
		if explain := permission.ExplainPolicy("auto", name, `{}`, *compiled); explain.Decision != permission.PolicyDecisionAsk {
			t.Fatalf("mutation tool %q decision=%q want ask", name, explain.Decision)
		}
	}
	if explain := permission.ExplainPolicy("auto", "bash", `{"command":"git init","explanation":["Initialize the selected folder"],"category":"write","critical":true}`, *compiled); explain.Decision != permission.PolicyDecisionAsk {
		t.Fatalf("git init decision=%q want ask", explain.Decision)
	}
	if explain := permission.ExplainPolicy("auto", "bash", `{"command":"git status","explanation":["Inspect status"],"category":"read","critical":false}`, *compiled); explain.Decision != permission.PolicyDecisionDeny {
		t.Fatalf("non-init bash decision=%q want deny", explain.Decision)
	}
	for _, deniedName := range []string{"task", "manage_sessions", "manage_worktree", "plan_manage"} {
		if resolved.Tools[deniedName].Enabled || !disabled[deniedName] {
			t.Fatalf("escalation tool %q resolved=%+v disabled=%v", deniedName, resolved.Tools[deniedName], disabled[deniedName])
		}
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

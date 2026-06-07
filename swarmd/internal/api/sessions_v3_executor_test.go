package api

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type sessionsV3ProviderToolsRunner struct {
	definitions []tool.Definition
	contract    runruntime.ResolvedAgentToolContract
	disabled    map[string]bool
}

func (r *sessionsV3ProviderToolsRunner) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionsV3ProviderToolsRunner) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionsV3ProviderToolsRunner) StopSessionRun(string, string, string) error { return nil }

func (r *sessionsV3ProviderToolsRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "{}", nil
}

func (r *sessionsV3ProviderToolsRunner) ListAgentToolDefinitions() []tool.Definition {
	return r.definitions
}

func (r *sessionsV3ProviderToolsRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return r.definitions
}

func (r *sessionsV3ProviderToolsRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, nil, r.disabled, nil
}

func (r *sessionsV3ProviderToolsRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, nil, r.disabled, nil
}

func (r *sessionsV3ProviderToolsRunner) CompileStoredV3AgentToolContract(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error) {
	return r.contract, r.disabled, nil
}

func TestResolveSessionV3ProviderToolsCanonicalizesDefinitionNames(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{
		definitions: []tool.Definition{
			{Type: "function", Name: "ask-user"},
			{Type: "function", Name: "bash"},
			{Type: "function", Name: "manage-agent"},
			{Type: "function", Name: "manage-flow"},
			{Type: "function", Name: "manage-skill"},
			{Type: "function", Name: "manage-worktree"},
			{Type: "function", Name: "skill-use"},
		},
		contract: runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
			"ask_user":        {Enabled: true},
			"bash":            {Enabled: true},
			"manage_agent":    {Enabled: true},
			"manage_flow":     {Enabled: true},
			"manage_skill":    {Enabled: true},
			"manage_worktree": {Enabled: true},
			"skill_use":       {Enabled: true},
		}},
		disabled: map[string]bool{},
	}
	exec := &sessionV3Executor{server: &Server{runner: runner}}

	tools, err := exec.resolveSessionV3ProviderTools("acct_test", pebblestore.AgentProfile{Name: "swarm", ToolContract: &pebblestore.AgentToolContract{}})
	if err != nil {
		t.Fatalf("resolveSessionV3ProviderTools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, definition := range tools {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	expected := []string{"ask-user", "bash", "manage-agent", "manage-flow", "manage-skill", "manage-worktree", "skill-use"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("provider tool names mismatch\n got: %v\nwant: %v", names, expected)
	}
}

package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type sessionV3ToolHydrationRunner struct {
	definitions []tool.Definition
	contract    runruntime.ResolvedAgentToolContract
	policy      *permission.Policy
	disabled    map[string]bool
	err         error
}

func (r *sessionV3ToolHydrationRunner) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionV3ToolHydrationRunner) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionV3ToolHydrationRunner) StopSessionRun(string, string, string) error { return nil }

func (r *sessionV3ToolHydrationRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (r *sessionV3ToolHydrationRunner) ListAgentToolDefinitions() []tool.Definition {
	return append([]tool.Definition(nil), r.definitions...)
}

func (r *sessionV3ToolHydrationRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return append([]tool.Definition(nil), r.definitions...)
}

func (r *sessionV3ToolHydrationRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, r.policy, r.disabled, r.err
}

func (r *sessionV3ToolHydrationRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, r.policy, r.disabled, r.err
}

func TestSessionV3PlanAutoAgentHydratesSavedCanonicalToolContract(t *testing.T) {
	runner := &sessionV3ToolHydrationRunner{
		definitions: []tool.Definition{
			{Type: "function", Name: "read", Description: "read"},
			{Type: "function", Name: "manage-flow", Description: "flow"},
		},
		contract: runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
			"read":        {Enabled: false},
			"manage_flow": {Enabled: true},
		}},
		policy: &permission.Policy{Version: 1},
	}
	executor := &sessionV3Executor{server: &Server{runner: runner}}
	profile := pebblestore.AgentProfile{
		Name:                "swarm",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{
			"manage_flow": {Enabled: pebblestore.BoolPtr(true)},
		}},
	}

	tools, err := executor.resolveSessionV3ProviderTools("acct", profile)
	if err != nil {
		t.Fatalf("resolveSessionV3ProviderTools() error = %v", err)
	}
	if got := sessionV3ProviderToolNames(tools); strings.Join(got, ",") != "manage-flow" {
		t.Fatalf("provider tools = %v, want saved canonical manage-flow contract", got)
	}
}

func TestSessionV3ProviderToolsUseResolvedContractAndCanonicalInventory(t *testing.T) {
	runner := &sessionV3ToolHydrationRunner{
		definitions: []tool.Definition{
			{Type: "function", Name: "read", Description: "read"},
			{Type: "function", Name: "manage-flow", Description: "flow"},
			{Type: "function", Name: "bash", Description: "bash"},
		},
		contract: runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
			"read":        {Enabled: false},
			"manage_flow": {Enabled: true},
			"bash":        {Enabled: true, BashPrefixes: []string{"git status"}},
		}},
		policy:   &permission.Policy{Version: 1},
		disabled: map[string]bool{"bash": true},
	}
	executor := &sessionV3Executor{server: &Server{runner: runner}}

	tools, err := executor.resolveSessionV3ProviderTools("acct", pebblestore.AgentProfile{Name: "swarm"})
	if err != nil {
		t.Fatalf("resolveSessionV3ProviderTools() error = %v", err)
	}
	if got := sessionV3ProviderToolNames(tools); strings.Join(got, ",") != "manage-flow" {
		t.Fatalf("provider tools = %v, want only canonical manage-flow from saved contract", got)
	}
}

func TestSessionV3ProviderToolsFailClosed(t *testing.T) {
	profile := pebblestore.AgentProfile{Name: "swarm"}
	baseContract := runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
		"read": {Enabled: true},
	}}

	cases := []struct {
		name    string
		runner  runService
		wantErr string
	}{
		{name: "missing runner", runner: nil, wantErr: "run service is not configured"},
		{name: "contract error", runner: &sessionV3ToolHydrationRunner{err: errors.New("contract boom")}, wantErr: "contract boom"},
		{name: "empty inventory", runner: &sessionV3ToolHydrationRunner{contract: baseContract, policy: &permission.Policy{Version: 1}}, wantErr: "agent tool inventory is empty"},
		{name: "enabled tool missing inventory", runner: &sessionV3ToolHydrationRunner{definitions: []tool.Definition{{Type: "function", Name: "search"}}, contract: baseContract, policy: &permission.Policy{Version: 1}}, wantErr: "missing from provider inventory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := &sessionV3Executor{server: &Server{runner: tc.runner}}
			_, err := executor.resolveSessionV3ProviderTools("acct", profile)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("resolveSessionV3ProviderTools() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func sessionV3ProviderToolNames(tools []provideriface.ToolDefinition) []string {
	out := make([]string, 0, len(tools))
	for _, definition := range tools {
		out = append(out, definition.Name)
	}
	return out
}

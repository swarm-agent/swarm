package api

import (
	"context"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	"swarm/packages/swarmd/internal/tool"
)

type workspaceOverviewNoopRunService struct{}

func (workspaceOverviewNoopRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (workspaceOverviewNoopRunService) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (workspaceOverviewNoopRunService) StopSessionRun(string, string, string) error { return nil }

func (workspaceOverviewNoopRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (workspaceOverviewNoopRunService) ListAgentToolDefinitions() []tool.Definition { return nil }

func (workspaceOverviewNoopRunService) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (workspaceOverviewNoopRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (workspaceOverviewNoopRunService) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (workspaceOverviewNoopRunService) ExecuteTaskProgramForCoordinator(context.Context, identity.Principal, string, string, pebblestore.TaskProgramRecord) (string, error) {
	return "completed", nil
}

package api

import (
	"context"

	"swarm/packages/swarmd/internal/identity"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	"swarm/packages/swarmd/internal/tool"
)

type recordingRunService struct {
	sessionID string
	request   runruntime.RunRequest
	meta      runruntime.RunStartMeta
}

func (r *recordingRunService) RunTurn(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	return runruntime.RunResult{SessionID: sessionID}, nil
}

func (r *recordingRunService) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	return runruntime.RunResult{SessionID: sessionID}, nil
}

func (r *recordingRunService) StopSessionRun(sessionID, runID, reason string) error { return nil }
func (r *recordingRunService) ExecuteToolForSessionScope(_ context.Context, workspacePath string, call tool.Call) (string, error) {
	return "{}", nil
}
func (r *recordingRunService) ListAgentToolDefinitions() []tool.Definition { return nil }
func (r *recordingRunService) ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	return nil
}
func (r *recordingRunService) ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (r *recordingRunService) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *recordingRunService) ExecuteTaskProgramForCoordinator(_ context.Context, _ identity.Principal, _ string, _ string, _ pebblestore.TaskProgramRecord) (string, error) {
	return "completed", nil
}

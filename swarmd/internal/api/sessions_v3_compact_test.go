package api

import (
	"context"

	"errors"

	"sync/atomic"

	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type directCompactRunService struct {
	summary             string
	err                 error
	started             chan struct{}
	release             chan struct{}
	compactCalls        atomic.Int32
	streamingCalls      atomic.Int32
	includeAssistantAck atomic.Bool
}

func (r *directCompactRunService) RunManualCompaction(_ context.Context, sessionID string, input runruntime.ManualCompactionInput) (runruntime.ManualCompactionResult, error) {
	r.compactCalls.Add(1)
	r.includeAssistantAck.Store(input.IncludeAssistantAck)
	if r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		<-r.release
	}
	if r.err != nil {
		return runruntime.ManualCompactionResult{}, r.err
	}
	summary := r.summary
	if summary == "" {
		summary = "direct compact summary"
	}
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "", SessionID: sessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, Role: "system", Content: "[context-compact] index=2 origin=manual\n\n" + summary, CreatedAt: now}
	clientRequestID := "test-direct-compact:" + input.RunID + ":checkpoint"
	mutation, err := input.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: "hash-" + clientRequestID, RequestHash: "hash-" + clientRequestID, Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message, NowUnixMs: now})
	if err != nil {
		return runruntime.ManualCompactionResult{}, err
	}
	if mutation.Message != nil {
		message = *mutation.Message
	}
	return runruntime.ManualCompactionResult{Summary: summary, CompactIndex: 2, CheckpointMessage: message, CheckpointMutation: mutation}, nil
}

func (r *directCompactRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, errors.New("RunTurn should not be used by compact endpoint")
}

func (r *directCompactRunService) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.streamingCalls.Add(1)
	return runruntime.RunResult{}, errors.New("RunTurnStreaming should not be used by compact endpoint")
}

func (r *directCompactRunService) StopSessionRun(string, string, string) error { return nil }

func (r *directCompactRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (r *directCompactRunService) ListAgentToolDefinitions() []tool.Definition { return nil }

func (r *directCompactRunService) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (r *directCompactRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *directCompactRunService) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *directCompactRunService) ExecuteTaskProgramForCoordinator(context.Context, identity.Principal, string, string, pebblestore.TaskProgramRecord) (string, error) {
	return "completed", nil
}

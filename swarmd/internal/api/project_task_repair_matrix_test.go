package api

import (
	"context"

	"errors"

	"path/filepath"

	"sync"
	"testing"

	"swarm/packages/swarmd/internal/identity"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type testMockWorktreeService struct {
	mu          sync.Mutex
	allocCalls  int
	failAlloc   bool
	allocResult worktreeruntime.Allocation
}

func (m *testMockWorktreeService) GetConfig(workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) GetConfigForSavedWorkspaceForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{}, nil
}
func (m *testMockWorktreeService) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return worktreeruntime.Config{}, nil, nil
}
func (m *testMockWorktreeService) SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error) {
	return worktreeruntime.Config{}, nil, nil
}
func (m *testMockWorktreeService) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal{}, workspacePath, nameSeed, "dev", "agent/test")
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, nameSeed, "dev", "agent/test")
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return m.AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal{}, workspacePath, nameSeed, baseBranch, branchName)
}
func (m *testMockWorktreeService) AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allocCalls++
	if m.failAlloc {
		return worktreeruntime.Allocation{}, errors.New("simulated worktree allocation error")
	}
	res := m.allocResult
	if res.WorkspacePath == "" {
		res.WorkspacePath = filepath.Join(workspacePath, "worktree-"+nameSeed)
		res.BranchName = branchName
		res.BaseBranch = "dev"
		res.BaseCommit = "base-commit-sha-001"
		res.RepoRoot = workspacePath
	}
	return res, nil
}
func (m *testMockWorktreeService) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	return nil
}
func (m *testMockWorktreeService) AttachBranch(workspacePath, sessionID, title string) (string, error) {
	return "agent/test", nil
}
func (m *testMockWorktreeService) ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return nil, nil
}
func (m *testMockWorktreeService) ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]worktreeruntime.ManagedWorktree, error) {
	return nil, nil
}
func (m *testMockWorktreeService) PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error) {
	return worktreeruntime.PruneResult{}, nil
}
func (m *testMockWorktreeService) PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.PruneResult, error) {
	return worktreeruntime.PruneResult{}, nil
}
func (m *testMockWorktreeService) InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error) {
	return worktreeruntime.TaskWorkspaceState{BranchName: "dev", HeadCommit: "base-commit-sha-001"}, nil
}

type testMockRunService struct {
	mu              sync.Mutex
	enqueuedRuns    []string
	tpExecutions    int
	lastTPRecord    pebblestore.TaskProgramRecord
	lastTPSessionID string
}

func (m *testMockRunService) RunTurn(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}
func (m *testMockRunService) RunTurnStreaming(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}
func (m *testMockRunService) StopSessionRun(sessionID, runID, reason string) error {
	return nil
}
func (m *testMockRunService) ExecuteToolForSessionScope(ctx context.Context, workspacePath string, call tool.Call) (string, error) {
	return "{}", nil
}
func (m *testMockRunService) ListAgentToolDefinitions() []tool.Definition {
	return nil
}
func (m *testMockRunService) ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	return nil
}
func (m *testMockRunService) ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (m *testMockRunService) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (m *testMockRunService) ExecuteTaskProgramForCoordinator(ctx context.Context, p identity.Principal, parentSessionID, runID string, record pebblestore.TaskProgramRecord) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tpExecutions++
	m.lastTPSessionID = parentSessionID
	m.lastTPRecord = record
	return "completed", nil
}

func requireMatrixRepositoryHistoryReady(t *testing.T, store *pebblestore.SessionStore) {
	t.Helper()
	// Backfill advances one bounded phase at a time, including empty stores.
	for i := 0; i < 20; i++ {
		ready, err := store.BackfillRepositoryHistory(100)
		if err != nil {
			t.Fatalf("backfill repository history: %v", err)
		}
		if ready {
			return
		}
	}
	t.Fatal("repository history not ready after bounded backfill")
}

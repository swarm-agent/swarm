package run

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
)

type workerTestQueue struct{ service *todo.Service }

func (q workerTestQueue) ListAITaskAccounts(limit int) ([]string, error) {
	return q.service.ListAITaskAccounts(limit)
}
func (q workerTestQueue) ListActiveAITasks(account string, limit int) ([]pebblestore.WorkspaceTodoItem, error) {
	return q.service.ListActiveAITasks(account, limit)
}
func (q workerTestQueue) GetAITask(account, workspace, task string) (pebblestore.WorkspaceTodoItem, bool, error) {
	return q.service.GetAITask(account, workspace, task)
}
func (q workerTestQueue) AppendAITaskAudit(account, workspace, task string, record pebblestore.AITaskAuditRecord) error {
	return q.service.AppendAITaskAudit(account, workspace, task, record)
}
func (q workerTestQueue) TransitionAITask(input AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error) {
	return q.service.TransitionAITaskAuthority(todo.AITaskTransitionInput{AccountScopeID: input.Item.AccountScopeID, WorkspacePath: input.Item.WorkspacePath, ID: input.Item.ID, ExpectedState: input.ExpectedState, ExpectedVersion: input.Item.AIStateVersion, State: input.State, Mode: input.Mode, Worktree: input.Worktree, ManagedSessionID: input.ManagedSessionID, FinalRunID: input.FinalRunID, PreparationSessionID: input.PreparationSessionID, PreparationRunID: input.PreparationRunID, PreparationAttemptID: input.PreparationAttemptID, Error: input.Error, Disposition: input.Disposition})
}

func TestAITaskWorkerStartupRecoveryClaimsQueuedTaskOnce(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "queue.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	todoStore := pebblestore.NewWorkspaceTodoStore(db)
	todoService := todo.NewService(todoStore, nil, nil, nil)
	queued, _, _, err := todoService.CreateAITask(todo.CreateAITaskInput{AccountScopeID: "account-worker", UserID: "user-worker", WorkspaceID: "workspace-worker", WorkspacePath: t.TempDir(), Request: "inspect and prepare", IdempotencyKey: "worker-key"})
	if err != nil {
		t.Fatalf("create queued task: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processed := make(chan pebblestore.WorkspaceTodoItem, 2)
	worker := &aiTaskWorker{queue: workerTestQueue{todoService}, wake: make(chan pebblestore.WorkspaceTodoItem, 4), active: map[string]struct{}{}}
	worker.execute = func(_ context.Context, _ AITaskQueue, item pebblestore.WorkspaceTodoItem, _ func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
		processed <- item
		return nil
	}
	worker.wg.Add(1)
	go worker.loop(ctx)
	select {
	case item := <-processed:
		if item.ID != queued.ID || item.AIState != pebblestore.WorkspaceTodoAIStatePreparing || item.PreparationSessionID == "" || item.PreparationRunID == "" || item.PreparationAttemptID == "" {
			t.Fatalf("recovered claim = %#v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not consume queued task")
	}
	select {
	case duplicate := <-processed:
		t.Fatalf("task processed twice: %#v", duplicate)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	worker.wg.Wait()

	audit, err := todoStore.ListAITaskAudit("account-worker", queued.ID, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	stages := map[string]int{}
	for _, record := range audit {
		stages[record.Stage]++
	}
	if stages["queued"] != 1 || stages["preparing"] != 1 {
		t.Fatalf("audit stages = %#v records=%#v", stages, audit)
	}
}

func TestAITaskWorkerSingleFlightSuppressesDuplicateWake(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker := &aiTaskWorker{wake: make(chan pebblestore.WorkspaceTodoItem, 4), active: map[string]struct{}{}}
	worker.wg.Add(0)
	worker.execute = func(context.Context, AITaskQueue, pebblestore.WorkspaceTodoItem, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	}
	item := pebblestore.WorkspaceTodoItem{ID: "task-one", AccountScopeID: "account-one"}
	worker.dispatch(context.Background(), item)
	<-started
	worker.dispatch(context.Background(), item)
	worker.mu.Lock()
	active := len(worker.active)
	worker.mu.Unlock()
	if active != 1 {
		t.Fatalf("active executions = %d", active)
	}
	close(release)
	worker.wg.Wait()
}

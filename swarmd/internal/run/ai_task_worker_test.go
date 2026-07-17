package run

import (
	"context"
	"path/filepath"
	"strings"
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

func TestBuildAITaskPreparationPromptIncludesFullAuthorizedOriginConversation(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "origin-ai-task", UserID: "user-origin", AccountScopeID: "account-origin",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}
	for _, message := range []struct{ role, content string }{{"user", "First request"}, {"assistant", "First answer"}, {"tool", "Tool result"}, {"user", "Latest detail"}} {
		if _, _, _, err := sessions.AppendMessage(origin.ID, message.role, message.content, nil); err != nil {
			t.Fatalf("append origin message: %v", err)
		}
	}
	before, err := sessions.ListSessionMessages(origin.ID, 0, 100)
	if err != nil {
		t.Fatalf("list origin before assembly: %v", err)
	}

	prompt, err := svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{
		OriginSessionID: origin.ID, AccountScopeID: origin.AccountScopeID, UserID: origin.UserID, WorkspacePath: origin.WorkspacePath, AIRequest: "  Implement the queued change  ",
	})
	if err != nil {
		t.Fatalf("build preparation prompt: %v", err)
	}
	want := aiTaskOriginContextOpen + "\n\n[user]\nFirst request\n\n[assistant]\nFirst answer\n\n[tool]\nTool result\n\n[user]\nLatest detail\n\n" + aiTaskOriginContextEnd + "\n\n" + aiTaskRequestOpen + "\nImplement the queued change\n" + aiTaskRequestEnd
	if prompt != want {
		t.Fatalf("preparation prompt mismatch\nwant:\n%s\n\ngot:\n%s", want, prompt)
	}
	if strings.Index(prompt, "First request") > strings.Index(prompt, "Latest detail") || strings.Index(prompt, aiTaskOriginContextEnd) > strings.Index(prompt, aiTaskRequestOpen) {
		t.Fatalf("preparation prompt ordering is not deterministic: %s", prompt)
	}

	after, err := sessions.ListSessionMessages(origin.ID, 0, 100)
	if err != nil {
		t.Fatalf("list origin after assembly: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("origin message count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Role != after[i].Role || before[i].Content != after[i].Content || before[i].GlobalSeq != after[i].GlobalSeq {
			t.Fatalf("origin message %d mutated: before=%#v after=%#v", i, before[i], after[i])
		}
	}
}

func TestListAllAITaskOriginMessagesPaginatesEntireDurableTranscript(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "paged-origin-ai-task", UserID: "user-origin", AccountScopeID: "account-origin",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create paged origin: %v", err)
	}
	for i := 0; i < aiTaskMessagePageLimit+1; i++ {
		if _, _, _, err := sessions.AppendMessage(origin.ID, "user", "message", nil); err != nil {
			t.Fatalf("append paged origin message %d: %v", i, err)
		}
	}

	messages, err := svc.listAllAITaskOriginMessages(origin.ID)
	if err != nil {
		t.Fatalf("list entire origin transcript: %v", err)
	}
	if len(messages) != aiTaskMessagePageLimit+1 {
		t.Fatalf("origin transcript length = %d, want %d", len(messages), aiTaskMessagePageLimit+1)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i].GlobalSeq <= messages[i-1].GlobalSeq {
			t.Fatalf("origin transcript order did not advance at %d: %#v then %#v", i, messages[i-1], messages[i])
		}
	}
}

func TestBuildAITaskPreparationPromptStandaloneAndAuthorization(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	standalone, err := svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{AIRequest: "  Standalone task  "})
	if err != nil || standalone != "Standalone task" {
		t.Fatalf("standalone prompt = %q, err=%v", standalone, err)
	}

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "foreign-origin-ai-task", UserID: "user-owner", AccountScopeID: "account-owner",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create foreign origin: %v", err)
	}
	_, err = svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{OriginSessionID: origin.ID, AccountScopeID: origin.AccountScopeID, UserID: "different-user", WorkspacePath: origin.WorkspacePath, AIRequest: "task"})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("authorization error = %v", err)
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

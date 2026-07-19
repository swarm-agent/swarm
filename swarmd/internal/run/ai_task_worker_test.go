package run

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type recordingAITaskLifecycle struct {
	mu          sync.Mutex
	transitions []AITaskQueueTransition
	audits      []pebblestore.AITaskAuditRecord
	started     chan struct{}
	unblock     chan struct{}
}

func (l *recordingAITaskLifecycle) TransitionAITask(input AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error) {
	l.mu.Lock()
	l.transitions = append(l.transitions, input)
	l.mu.Unlock()
	if l.started != nil {
		select {
		case l.started <- struct{}{}:
		default:
		}
	}
	if l.unblock != nil {
		<-l.unblock
	}
	return pebblestore.WorkspaceTodoItem{}, errors.New("stop before provider execution")
}

func (l *recordingAITaskLifecycle) AppendAITaskAudit(_, _, _ string, record pebblestore.AITaskAuditRecord) error {
	l.mu.Lock()
	l.audits = append(l.audits, record)
	l.mu.Unlock()
	return nil
}

func completeQueuedAITaskFixture(id string) pebblestore.WorkspaceTodoItem {
	return pebblestore.WorkspaceTodoItem{
		ID: id, AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace-id",
		WorkspacePath: "/workspace", AIRequest: "do the work", AIState: pebblestore.WorkspaceTodoAIStateQueued,
	}
}

func TestAITaskDispatcherDispatchesCompleteJobImmediatelyWithoutDurableLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycle := &recordingAITaskLifecycle{started: make(chan struct{}, 1)}
	dispatcher := (&Service{}).StartAITaskDispatcher(ctx, lifecycle, nil)
	defer dispatcher.Close()

	item := completeQueuedAITaskFixture("task-immediate")
	if !dispatcher.Enqueue(item) {
		t.Fatal("complete in-memory job was rejected")
	}
	select {
	case <-lifecycle.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not receive the directly enqueued job")
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if len(lifecycle.transitions) != 1 || lifecycle.transitions[0].Item.ID != item.ID || lifecycle.transitions[0].Item.AIRequest != item.AIRequest {
		t.Fatalf("worker did not receive the immutable request payload: %#v", lifecycle.transitions)
	}
}

func TestAITaskDispatcherBoundsAdmissionDeduplicatesAndRejectsShutdown(t *testing.T) {
	item := pebblestore.WorkspaceTodoItem{
		ID: "task-1", AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace-id",
		WorkspacePath: "/workspace", AIRequest: "do the work", AIState: pebblestore.WorkspaceTodoAIStateQueued,
	}
	job, err := NewAITaskJob(item)
	if err != nil {
		t.Fatalf("new job: %v", err)
	}
	d := &AITaskDispatcher{jobs: make(chan AITaskJob, 1), done: make(chan struct{}), inflight: map[string]struct{}{}}
	if !d.EnqueueJob(job) {
		t.Fatal("first enqueue rejected")
	}
	if !d.EnqueueJob(job) || len(d.jobs) != 1 {
		t.Fatalf("duplicate enqueue was not idempotent: queue length=%d", len(d.jobs))
	}
	second := job
	second.Task.ID = "task-2"
	if d.EnqueueJob(second) {
		t.Fatal("saturated queue accepted another job")
	}
	d.Close()
	if d.EnqueueJob(second) {
		t.Fatal("closed queue accepted another job")
	}
}

type panickingAITaskLifecycle struct {
	mu          sync.Mutex
	transitions []AITaskQueueTransition
	started     chan struct{}
}

func (l *panickingAITaskLifecycle) TransitionAITask(input AITaskQueueTransition) (pebblestore.WorkspaceTodoItem, error) {
	l.mu.Lock()
	l.transitions = append(l.transitions, input)
	l.mu.Unlock()
	if input.State == pebblestore.WorkspaceTodoAIStatePreparing {
		panic("task preparation exploded")
	}
	if input.State == pebblestore.WorkspaceTodoAIStateFailed && l.started != nil {
		select {
		case l.started <- struct{}{}:
		default:
		}
	}
	return input.Item, nil
}

func (l *panickingAITaskLifecycle) AppendAITaskAudit(_, _, _ string, _ pebblestore.AITaskAuditRecord) error {
	return nil
}

func TestAITaskDispatcherContainsPanicsAndContinuesServingJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycle := &panickingAITaskLifecycle{started: make(chan struct{}, 2)}
	dispatcher := (&Service{}).StartAITaskDispatcher(ctx, lifecycle, nil)
	defer dispatcher.Close()

	for _, id := range []string{"task-panics-first", "task-runs-after-panic"} {
		if !dispatcher.Enqueue(completeQueuedAITaskFixture(id)) {
			t.Fatalf("dispatcher rejected %s", id)
		}
		select {
		case <-lifecycle.started:
		case <-time.After(time.Second):
			t.Fatalf("dispatcher did not contain and terminalize panic for %s", id)
		}
	}

	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	var preparing, failed int
	for _, transition := range lifecycle.transitions {
		switch transition.State {
		case pebblestore.WorkspaceTodoAIStatePreparing:
			preparing++
		case pebblestore.WorkspaceTodoAIStateFailed:
			failed++
		}
	}
	if preparing != 2 || failed != 2 {
		t.Fatalf("panic containment transitions preparing=%d failed=%d, want 2 each: %#v", preparing, failed, lifecycle.transitions)
	}
}

func TestAITaskDispatcherShutdownStopsWorkerAndRejectsAdmission(t *testing.T) {
	ctx := context.Background()
	lifecycle := &recordingAITaskLifecycle{started: make(chan struct{}, 1), unblock: make(chan struct{})}
	dispatcher := (&Service{}).StartAITaskDispatcher(ctx, lifecycle, nil)
	if !dispatcher.Enqueue(completeQueuedAITaskFixture("task-running")) {
		t.Fatal("initial job was rejected")
	}
	select {
	case <-lifecycle.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	closed := make(chan struct{})
	go func() {
		dispatcher.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before the active worker exited")
	case <-time.After(10 * time.Millisecond):
	}
	close(lifecycle.unblock)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for deterministic worker shutdown")
	}
	if dispatcher.Enqueue(completeQueuedAITaskFixture("task-after-close")) {
		t.Fatal("closed dispatcher accepted new work")
	}
}

func TestAITaskWorkerSourceCannotRegressToDurableQueueReadsOrTickerScans(t *testing.T) {
	source, err := os.ReadFile("ai_task_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"GetAITask(", "LoadRecoverableAITasks(", "ListActiveAITasks(", "time.NewTicker", "time.Tick("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("AI task worker regained forbidden durable scheduling dependency %q", forbidden)
		}
	}
	for _, required := range []string{"jobs chan AITaskJob", "case job := <-d.jobs", "item := job.Task"} {
		if !strings.Contains(text, required) {
			t.Fatalf("AI task worker missing in-memory job contract %q", required)
		}
	}
}

func TestNewAITaskJobRequiresCompleteTrustedPayload(t *testing.T) {
	if _, err := NewAITaskJob(pebblestore.WorkspaceTodoItem{ID: "task"}); err == nil {
		t.Fatal("incomplete task payload was accepted")
	}
	item := completeQueuedAITaskFixture("task-preparing")
	item.AIState = pebblestore.WorkspaceTodoAIStatePreparing
	if _, err := NewAITaskJob(item); err == nil {
		t.Fatal("non-queued task was accepted by the in-memory dispatcher")
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

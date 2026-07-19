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

type recordingAITaskV2Store struct {
	mu          sync.Mutex
	recovery    []pebblestore.AITaskV2QueueRecord
	transitions []AITaskV2Transition
	deleted     []string
	started     chan string
}

func (s *recordingAITaskV2Store) LoadAITaskV2RecoveryQueue(int) ([]pebblestore.AITaskV2QueueRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pebblestore.AITaskV2QueueRecord(nil), s.recovery...), nil
}

func (s *recordingAITaskV2Store) DeleteAITaskV2QueueRecord(key string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return nil
}

func (s *recordingAITaskV2Store) AppendAITaskAudit(_, _, _ string, _ pebblestore.AITaskAuditRecord) error {
	return nil
}

func (s *recordingAITaskV2Store) TransitionAITaskV2(input AITaskV2Transition) (pebblestore.WorkspaceTodoItem, error) {
	s.mu.Lock()
	s.transitions = append(s.transitions, input)
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- input.Item.ID:
		default:
		}
	}
	return pebblestore.WorkspaceTodoItem{}, errors.New("stop before deployment")
}

func completeAITaskV2Fixture(id string) pebblestore.WorkspaceTodoItem {
	return pebblestore.WorkspaceTodoItem{
		ID: id, AccountScopeID: "account", UserID: "user", WorkspaceID: "workspace-id",
		WorkspacePath: "/workspace", AIRequest: "do the work", AIState: pebblestore.WorkspaceTodoAIStateQueued,
		CreatedAt: time.Now().UnixMilli(), AIStateVersion: 1,
	}
}

func TestAITaskV2DispatcherFIFOAndWakeupWithoutSteadyStateStoreReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &recordingAITaskV2Store{started: make(chan string, 3)}
	dispatcher, err := (&Service{}).StartAITaskV2Dispatcher(ctx, store, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		return sessionruntime.SessionMutationResult{}, nil
	})
	if err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	defer dispatcher.Close()

	for _, id := range []string{"one", "two", "three"} {
		if !dispatcher.Enqueue(completeAITaskV2Fixture(id)) {
			t.Fatalf("enqueue %s rejected", id)
		}
	}
	for _, want := range []string{"one", "two", "three"} {
		select {
		case got := <-store.started:
			if got != want {
				t.Fatalf("dispatch order got %q want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestAITaskV2DispatcherRecoversDurableQueueOnceAtStartup(t *testing.T) {
	store := &recordingAITaskV2Store{
		recovery: []pebblestore.AITaskV2QueueRecord{{Key: "ai_task/v2_queue/recovered", Task: completeAITaskV2Fixture("recovered")}},
		started:  make(chan string, 1),
	}
	dispatcher, err := (&Service{}).StartAITaskV2Dispatcher(context.Background(), store, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		return sessionruntime.SessionMutationResult{}, nil
	})
	if err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	defer dispatcher.Close()
	select {
	case got := <-store.started:
		if got != "recovered" {
			t.Fatalf("recovered %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("durable recovery record was not dispatched")
	}
}

func TestAITaskV2SourceHasNoPollingOrOldWorkerDependency(t *testing.T) {
	source, err := os.ReadFile("ai_task_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"time.NewTicker", "time.Tick(", "ListAITasksForAccount(", "GetAITask(", "StartAITaskDispatcher", "AITaskQueueTransition"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("AI task V2 source regained forbidden dependency %q", forbidden)
		}
	}
	for _, required := range []string{"wake:     make(chan struct{}, 1)", "LoadAITaskV2RecoveryQueue", "PrepareAITaskMetadata", "ExecutePreparedAITask"} {
		if !strings.Contains(text, required) {
			t.Fatalf("AI task V2 source missing contract %q", required)
		}
	}

	retired, err := os.ReadFile("ai_task_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"type AITaskDispatcher", "StartAITaskDispatcher", "func (d *AITaskDispatcher)"} {
		if strings.Contains(string(retired), forbidden) {
			t.Fatalf("retired worker remains operational via %q", forbidden)
		}
	}
}

func TestAITaskV2RetryBackoffIsBoundedAndPermanentErrorsAreExplicit(t *testing.T) {
	if got := aiTaskV2RetryDelay(1); got != aiTaskV2RetryBase {
		t.Fatalf("first retry delay=%s", got)
	}
	if got := aiTaskV2RetryDelay(100); got != aiTaskV2RetryMax {
		t.Fatalf("bounded retry delay=%s", got)
	}
	if !isNonRetryableAITaskV2Error(permanentAITaskV2Error(errors.New("ownership"))) || isNonRetryableAITaskV2Error(errors.New("transport")) {
		t.Fatal("retry classification is not explicit")
	}
}

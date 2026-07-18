package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestCancelledTaskLaunchReasonPreservesUserStopClassification(t *testing.T) {
	svc, sessionID, cleanup := newStopCancelService(t)
	defer cleanup()

	lifecycle, err := svc.beginSessionLifecycle(sessionID, "run-task-cancel", "http")
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	if lifecycle.RunID != "run-task-cancel" {
		t.Fatalf("run id = %q, want run-task-cancel", lifecycle.RunID)
	}
	if err := svc.StopSessionRun(sessionID, "run-task-cancel", "user stopped subagent"); err != nil {
		t.Fatalf("stop session run: %v", err)
	}

	reason, cancelled := svc.cancelledTaskLaunchReason(sessionID, context.Canceled)
	if !cancelled {
		t.Fatalf("cancelled = false, want true")
	}
	if reason != "user stopped subagent" {
		t.Fatalf("reason = %q, want user stopped subagent", reason)
	}
}

func newStopCancelService(t *testing.T) (*Service, string, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "stop-cancel-helper.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Task cancellation",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		cleanup()
		t.Fatalf("create session: %v", err)
	}
	return NewService(sessions, nil, nil, nil, nil, nil, nil, eventLog), session.ID, cleanup
}

func TestStopSessionRunSuppressesProviderOutputAfterCancel(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "stop-cancel.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Stop cancel",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "fake-stop-late",
			Model:    "fake-model",
			Thinking: "off",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	runner := &lateStopProviderRunner{
		entered: make(chan struct{}),
		resume:  make(chan struct{}),
	}
	providers.RegisterRunner(runner)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	svc := NewService(sessions, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, eventLog)

	var events []StreamEvent
	done := make(chan error, 1)
	go func() {
		_, runErr := svc.RunTurnStreaming(context.Background(), session.ID, RunRequest{Prompt: "stop me"}, RunStartMeta{RunID: "run_stop_late"}, func(event StreamEvent) {
			events = append(events, event)
		})
		done <- runErr
	}()

	<-runner.entered
	if err := svc.StopSessionRun(session.ID, "run_stop_late", "test stop"); err != nil {
		t.Fatalf("stop session run: %v", err)
	}
	close(runner.resume)

	runErr := <-done
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("run error = %v, want context canceled", runErr)
	}

	messages, err := sessions.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, message := range messages {
		if message.Role == "assistant" && strings.Contains(message.Content, "late assistant text") {
			t.Fatalf("stored assistant output after stop: %#v", message)
		}
	}
	for _, event := range events {
		if event.Type == StreamEventAssistantDelta && strings.Contains(event.Delta, "late assistant text") {
			t.Fatalf("emitted assistant delta after stop: %#v", event)
		}
		if event.Type == StreamEventMessageStored && event.Message != nil && event.Message.Role == "assistant" && strings.Contains(event.Message.Content, "late assistant text") {
			t.Fatalf("emitted stored assistant message after stop: %#v", event)
		}
		if event.Type == StreamEventTurnError {
			t.Fatalf("emitted turn error for user stop: %#v", event)
		}
	}

	snapshot, ok, err := sessions.GetLifecycle(session.ID)
	if err != nil {
		t.Fatalf("get lifecycle: %v", err)
	}
	if !ok {
		t.Fatal("missing lifecycle snapshot")
	}
	if snapshot.Active {
		t.Fatalf("lifecycle still active: %#v", snapshot)
	}
	if snapshot.Phase != lifecyclePhaseCancelled {
		t.Fatalf("lifecycle phase = %q, want cancelled; snapshot=%#v", snapshot.Phase, snapshot)
	}
}

type lateStopProviderRunner struct {
	entered chan struct{}
	resume  chan struct{}
}

func (r *lateStopProviderRunner) ID() string { return "fake-stop-late" }

func (r *lateStopProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *lateStopProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	close(r.entered)
	<-r.resume
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "late assistant text"})
	}
	return provideriface.Response{Text: "late assistant text"}, nil
}

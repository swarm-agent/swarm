package v3chat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

type fakeTransport struct {
	mu                sync.Mutex
	calls             []string
	created           client.SessionV3Hydrated
	result            client.SessionV3MessageResult
	streamBlock       chan struct{}
	streamFrames      []client.V3RealtimeFrame
	streamOptions     []client.V3RealtimeResumeOptions
	hydrateCount      int
	preference        client.ModelResolved
	mode              client.SessionV3ModeResult
	modeRequest       string
	providers         []client.ProviderStatus
	catalog           map[string][]client.ModelCatalogRecord
	preferenceRequest map[string]any
}

func (f *fakeTransport) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}
func (f *fakeTransport) CreateSessionV3WithOptions(context.Context, client.SessionCreateOptions) (client.SessionV3Hydrated, error) {
	f.record("create")
	return f.created, nil
}
func (f *fakeTransport) CreateSessionV3TUIWithOptions(context.Context, client.SessionCreateOptions) (client.SessionV3Hydrated, error) {
	f.record("create-tui")
	return f.created, nil
}
func (f *fakeTransport) GetSessionV3TUI(context.Context, string, string, string) (client.SessionV3Hydrated, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "hydrate")
	f.hydrateCount++
	f.mu.Unlock()
	return f.created, nil
}
func (f *fakeTransport) StreamV3Realtime(ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error {
	f.mu.Lock()
	f.calls = append(f.calls, "stream")
	f.streamOptions = append(f.streamOptions, options)
	f.mu.Unlock()
	if options.OnResumeSent != nil {
		options.OnResumeSent()
	}
	f.record("ready")
	for _, frame := range f.streamFrames {
		onFrame(frame)
	}
	if f.streamBlock == nil {
		<-ctx.Done()
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-f.streamBlock:
		return errors.New("closed")
	}
}
func (f *fakeTransport) StopSessionV3Run(context.Context, string, string, string, string) error {
	f.record("stop")
	return nil
}
func (f *fakeTransport) SetSessionV3ModeResolved(_ context.Context, _ string, mode string) (client.SessionV3ModeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "set-mode")
	f.modeRequest = mode
	f.mu.Unlock()
	return f.mode, nil
}
func (f *fakeTransport) SetSessionV3Preference(_ context.Context, _ string, request map[string]any) (client.ModelResolved, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "set-preference")
	f.preferenceRequest = request
	f.mu.Unlock()
	return f.preference, nil
}
func (f *fakeTransport) ListProviders(context.Context) ([]client.ProviderStatus, error) {
	f.record("list-providers")
	return append([]client.ProviderStatus(nil), f.providers...), nil
}
func (f *fakeTransport) ListModelCatalog(_ context.Context, provider string, _ int) ([]client.ModelCatalogRecord, error) {
	f.record("list-catalog:" + provider)
	return append([]client.ModelCatalogRecord(nil), f.catalog[provider]...), nil
}
func (f *fakeTransport) SendSessionV3Message(_ context.Context, sessionID string, options client.SessionV3MessageOptions) (client.SessionV3MessageResult, error) {
	f.record("send")
	f.result.Session.ID = sessionID
	f.result.Message.ID = options.MessageID
	f.result.Message.SessionID = sessionID
	f.result.Message.Role = options.Role
	f.result.Message.Content = options.Content
	f.result.Message.Metadata = options.Metadata
	return f.result, nil
}

func TestCreateAndSendOrdersCreateStoreReadyThenMessage(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "created"}, Projection: client.SessionV3Projection{SessionID: "s", LastEventSeq: 3}, SnapshotEndpointCursor: "cursor"}}
	runtime := NewRuntime(transport, nil, nil)
	result, err := runtime.CreateAndSend(context.Background(), NewSessionRequest{Create: client.SessionCreateOptions{}, InitialPrompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Stop()
	transport.mu.Lock()
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if !reflect.DeepEqual(calls[:4], []string{"create", "stream", "ready", "send"}) {
		t.Fatalf("calls = %#v", calls)
	}
	state := runtime.Store().Snapshot()
	if state.Session.Title != "created" || len(state.Messages) != 1 || len(state.Pending) != 0 {
		t.Fatalf("state = %#v result=%#v", state, result)
	}
	if state.Messages[0].OperationID == "" {
		t.Fatalf("stable operation metadata missing: %#v", state.Messages[0])
	}
}

func TestCreateAndSendEmptySessionStartsAtCurrentWithoutSnapshotCursor(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "created"}, Projection: client.SessionV3Projection{SessionID: "s"}}}
	runtime := NewRuntime(transport, nil, nil)
	if _, err := runtime.CreateAndSend(context.Background(), NewSessionRequest{Create: client.SessionCreateOptions{}, InitialPrompt: "first"}); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	transport.mu.Lock()
	calls := append([]string(nil), transport.calls...)
	options := append([]client.V3RealtimeResumeOptions(nil), transport.streamOptions...)
	transport.mu.Unlock()
	if !reflect.DeepEqual(calls[:4], []string{"create", "stream", "ready", "send"}) {
		t.Fatalf("calls = %#v", calls)
	}
	if len(options) != 1 || !options[0].StartAtCurrent || options[0].EndpointCursor != "" || len(options[0].Subscriptions) != 1 || options[0].Subscriptions[0].SessionID != "s" {
		t.Fatalf("stream options = %#v", options)
	}
	state := runtime.Store().Snapshot()
	if state.Connection != ConnectionReady || len(SelectMessages(state)) != 1 || SelectMessages(state)[0].Content != "first" {
		t.Fatalf("connected first-message state = %#v", state)
	}
}

func TestConnectExistingSessionWithoutCursorStillRejectsResume(t *testing.T) {
	runtime := NewRuntime(&fakeTransport{}, nil, nil)
	runtime.Store().Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}}})
	err := runtime.Connect(context.Background())
	if err == nil || err.Error() != "v3 chat hydration must provide an endpoint cursor" {
		t.Fatalf("Connect() error = %v", err)
	}
}

func TestCreateSendAndRealtimeSequenceProjectsVisibleCanonicalState(t *testing.T) {
	assistantPayload, err := json.Marshal(map[string]any{"message": client.SessionMessage{ID: "assistant-1", SessionID: "s", GlobalSeq: 2, Role: "assistant", Content: "hello world", Metadata: map[string]any{"run_id": "run-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	titlePayload, err := json.Marshal(map[string]any{"title": "renamed live"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{
		created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "created"}, Projection: client.SessionV3Projection{SessionID: "s"}, SnapshotEndpointCursor: "cursor-1"},
		result: client.SessionV3MessageResult{
			Session:    client.SessionSummary{ID: "s"},
			Message:    client.SessionMessage{GlobalSeq: 1},
			RunIntent:  client.SessionV3RunIntent{SessionID: "s", RunID: "run-1", Status: "running"},
			Projection: client.SessionV3Projection{SessionID: "s", LastEventSeq: 0},
		},
		streamFrames: []client.V3RealtimeFrame{
			{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{SessionID: "s", RunID: "run-1", StreamID: "assistant:run-1", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 6, Text: "hello "}},
			{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{SessionID: "s", RunID: "run-1", StreamID: "assistant:run-1", LiveSeqStart: 2, LiveSeqEnd: 2, OffsetStart: 6, OffsetEnd: 11, Text: "world"}},
			{Kind: "event", EndpointCursor: "cursor-2", Event: &client.SessionV3Event{ID: "event-assistant", SessionID: "s", Seq: 1, EventType: "message.appended", Payload: assistantPayload}},
			{Kind: "event", EndpointCursor: "cursor-3", Event: &client.SessionV3Event{ID: "event-title", SessionID: "s", Seq: 2, EventType: "session.title.updated", Payload: titlePayload}},
		},
	}
	runtime := NewRuntime(transport, nil, nil)
	if _, err := runtime.CreateAndSend(context.Background(), NewSessionRequest{Create: client.SessionCreateOptions{}, InitialPrompt: "question"}); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	transport.mu.Lock()
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if !reflect.DeepEqual(calls[:4], []string{"create", "stream", "ready", "send"}) {
		t.Fatalf("request ordering = %#v", calls)
	}
	state := runtime.Store().Snapshot()
	if SelectTitle(state) != "renamed live" {
		t.Fatalf("visible title = %q", SelectTitle(state))
	}
	messages := SelectMessages(state)
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "question" || messages[1].Role != "assistant" || messages[1].Content != "hello world" {
		t.Fatalf("canonical messages = %#v", messages)
	}
	if live := SelectLiveSegments(state); len(live) != 0 {
		t.Fatalf("durable assistant message did not replace live overlay: %#v", live)
	}
	if cursor, seq := SelectCursor(state); cursor != "cursor-3" || seq != 2 {
		t.Fatalf("cursor state = %q/%d", cursor, seq)
	}
}

func TestExistingSessionHydratesOnceAndDoesNotRefreshWhileIdle(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "cursor"}}
	runtime := NewRuntime(transport, nil, nil)
	if err := runtime.Hydrate(context.Background(), "s", "/workspace", ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	time.Sleep(25 * time.Millisecond)
	transport.mu.Lock()
	hydrates := transport.hydrateCount
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if hydrates != 1 || !reflect.DeepEqual(calls, []string{"hydrate", "stream", "ready"}) {
		t.Fatalf("idle transport calls = %#v hydrates=%d", calls, hydrates)
	}
}

func TestSetModeCommitsBackendResolvedModeAndModelState(t *testing.T) {
	transport := &fakeTransport{mode: client.SessionV3ModeResult{
		Mode:             "plan",
		Preference:       client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "high"},
		ContextWindow:    200000,
		AgentModelPolicy: client.SessionV3AgentModelPolicy{Locked: true, ProfileName: "Planning", ProfileSource: "saved", Preference: client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "high"}, ContextWindow: 200000},
	}}
	runtime := NewRuntime(transport, nil, nil)
	runtime.Store().Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Mode: "auto"}, Preference: client.ModelPreference{Provider: "codex", Model: "auto-model"}}})
	resolved, err := runtime.SetMode(context.Background(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Mode != "plan" || transport.modeRequest != "plan" {
		t.Fatalf("mode result/request = %#v / %q", resolved, transport.modeRequest)
	}
	state := runtime.Store().Snapshot()
	if state.Session.Mode != "plan" || state.Model.Preference.Model != "plan-model" || state.Model.ProfileName != "Planning" || !state.Model.Locked {
		t.Fatalf("backend-resolved mode/model state not committed: %#v", state)
	}
}

func TestSetModelPreferenceCommitsBackendResolvedState(t *testing.T) {
	transport := &fakeTransport{preference: client.ModelResolved{Preference: client.ModelPreference{Provider: "codex", Model: "resolved-model", Thinking: "high", ServiceTier: "fast"}, ContextWindow: 200000}}
	runtime := NewRuntime(transport, nil, nil)
	runtime.Store().Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, Preference: client.ModelPreference{Provider: "codex", Model: "before"}}})
	resolved, err := runtime.SetModelPreference(context.Background(), client.ModelPreference{Provider: "codex", Model: "requested", Thinking: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Preference.Model != "resolved-model" {
		t.Fatalf("resolved = %#v", resolved)
	}
	state := runtime.Store().Snapshot()
	if state.Model.Preference.Model != "resolved-model" || state.Model.ContextWindow != 200000 {
		t.Fatalf("store did not commit backend response: %#v", state.Model)
	}
	transport.mu.Lock()
	request := transport.preferenceRequest
	transport.mu.Unlock()
	if request["model"] != "requested" || request["thinking"] != "medium" {
		t.Fatalf("preference request = %#v", request)
	}
}

func TestListModelOptionsUsesRunnableBackendCatalog(t *testing.T) {
	transport := &fakeTransport{
		providers: []client.ProviderStatus{{ID: "codex", Runnable: true}, {ID: "offline", Runnable: false}},
		catalog:   map[string][]client.ModelCatalogRecord{"codex": {{Model: "gpt-test"}}},
	}
	runtime := NewRuntime(transport, nil, nil)
	options, err := runtime.ListModelOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Provider != "codex" || options[0].Model != "gpt-test" {
		t.Fatalf("options = %#v", options)
	}
	transport.mu.Lock()
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"list-providers", "list-catalog:codex"}) {
		t.Fatalf("catalog calls = %#v", calls)
	}
}

func TestStopUnblocksSignalDrivenStream(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "cursor"}}
	runtime := NewRuntime(transport, nil, nil)
	runtime.Store().Dispatch(HydrateAction{Snapshot: transport.created})
	if err := runtime.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.Stop()
}

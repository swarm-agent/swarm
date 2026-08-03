package v3chat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

type fakeTransport struct {
	mu                 sync.Mutex
	calls              []string
	createRequests     []client.SessionCreateOptions
	created            client.SessionV3Hydrated
	result             client.SessionV3MessageResult
	streamBlock        chan struct{}
	streamFrames       []client.V3RealtimeFrame
	streamOptions      []client.V3RealtimeResumeOptions
	hydrateCount       int
	preference         client.ModelResolved
	mode               client.SessionV3ModeResult
	modeRequest        string
	providers          []client.ProviderStatus
	catalog            map[string][]client.ModelCatalogRecord
	preferenceRequest  map[string]any
	resolvedPermission client.PermissionRecord
	permissionExplain  client.PermissionExplain
	messageRequest     client.SessionV3MessageOptions
	routedRequests     []client.RoutedSessionV3StartRequest
	routedResponses    []client.RoutedSessionV3StartResponse
	routedErrors       []error
	compactRequest     client.SessionV3CompactOptions
	compactSessionID   string
	permissionRequest  struct {
		sessionID         string
		permissionID      string
		action            string
		reason            string
		approvedArguments string
	}
	stopRequest struct {
		sessionID     string
		runID         string
		targetSwarmID string
		reason        string
	}
}

func (f *fakeTransport) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}
func (f *fakeTransport) StartRoutedSessionV3(_ context.Context, request client.RoutedSessionV3StartRequest) (client.RoutedSessionV3StartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "route")
	f.routedRequests = append(f.routedRequests, request)
	index := len(f.routedRequests) - 1
	if index < len(f.routedErrors) && f.routedErrors[index] != nil {
		return client.RoutedSessionV3StartResponse{}, f.routedErrors[index]
	}
	if index < len(f.routedResponses) {
		return f.routedResponses[index], nil
	}
	return client.RoutedSessionV3StartResponse{}, nil
}

func (f *fakeTransport) CreateSessionV3WithOptions(_ context.Context, options client.SessionCreateOptions) (client.SessionV3Hydrated, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "create")
	f.createRequests = append(f.createRequests, options)
	f.mu.Unlock()
	return f.created, nil
}
func (f *fakeTransport) CreateSessionV3TUIWithOptions(_ context.Context, options client.SessionCreateOptions) (client.SessionV3Hydrated, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "create-tui")
	f.createRequests = append(f.createRequests, options)
	f.mu.Unlock()
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
func (f *fakeTransport) StopSessionV3Run(_ context.Context, sessionID, runID, targetSwarmID, reason string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "stop")
	f.stopRequest.sessionID = sessionID
	f.stopRequest.runID = runID
	f.stopRequest.targetSwarmID = targetSwarmID
	f.stopRequest.reason = reason
	f.mu.Unlock()
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
func (f *fakeTransport) ExplainPermission(context.Context, string, string, string) (client.PermissionExplain, error) {
	return f.permissionExplain, nil
}

func (f *fakeTransport) ResolvePermission(ctx context.Context, sessionID, permissionID, action, reason string) (client.PermissionRecord, error) {
	return f.ResolvePermissionWithArguments(ctx, sessionID, permissionID, action, reason, "")
}

func (f *fakeTransport) ResolvePermissionWithArguments(_ context.Context, sessionID, permissionID, action, reason, approvedArguments string) (client.PermissionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "resolve-permission")
	f.permissionRequest.sessionID = sessionID
	f.permissionRequest.permissionID = permissionID
	f.permissionRequest.action = action
	f.permissionRequest.reason = reason
	f.permissionRequest.approvedArguments = approvedArguments
	record := f.resolvedPermission
	if record.ID == "" {
		record = client.PermissionRecord{ID: permissionID, SessionID: sessionID, Status: "approved", Decision: action}
	}
	return record, nil
}

func (f *fakeTransport) CompactSessionV3(_ context.Context, sessionID string, options client.SessionV3CompactOptions) (client.SessionV3CompactResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "compact")
	f.compactSessionID = sessionID
	f.compactRequest = options
	return client.SessionV3CompactResult{OK: true, SessionID: sessionID, Status: "completed"}, nil
}

func (f *fakeTransport) SendSessionV3Message(_ context.Context, sessionID string, options client.SessionV3MessageOptions) (client.SessionV3MessageResult, error) {
	f.record("send")
	f.mu.Lock()
	f.messageRequest = options
	f.mu.Unlock()
	f.result.Session.ID = sessionID
	f.result.Message.ID = options.MessageID
	f.result.Message.SessionID = sessionID
	f.result.Message.Role = options.Role
	f.result.Message.Content = options.Content
	f.result.Message.Metadata = options.Metadata
	return f.result, nil
}

func TestRoutedActivationFailureRestoresRetryableLocalOperation(t *testing.T) {
	response := routedRuntimeResponse("session-routed")
	transport := &fakeTransport{routedResponses: []client.RoutedSessionV3StartResponse{response, response}}
	runtime := NewRuntime(transport, NewStore(), nil)
	activationCalls := 0
	runtime.SetRoutedActivation(func(context.Context, client.RoutedSessionV3StartResponse) error {
		activationCalls++
		if activationCalls == 1 {
			return errors.New("workspace activation failed")
		}
		return nil
	})
	if err := runtime.PrimeRoutedDraft(routedTestDraft("route this", true)); err != nil {
		t.Fatal(err)
	}
	identity, _ := SelectRoutedDraft(runtime.Store().Snapshot())
	if _, err := runtime.StartRoutedDraft(context.Background()); err == nil || !strings.Contains(err.Error(), "workspace activation failed") {
		t.Fatalf("activation error = %v", err)
	}
	failed := runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(failed)
	if !ok || draft.Status != RoutedDraftFailed || draft.ClientRequestID != identity.ClientRequestID || draft.Prompt != "route this" || failed.Session.ID != "" {
		t.Fatalf("failed activation state = %#v draft=%#v", failed.Session, draft)
	}
	if _, err := runtime.RetryRoutedDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolved := runtime.Store().Snapshot()
	if resolved.Session.ID != "session-routed" || resolved.Connection != ConnectionReady {
		t.Fatalf("retry state = session %#v connection %q", resolved.Session, resolved.Connection)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.routedRequests) != 2 || transport.routedRequests[0].ClientRequestID != transport.routedRequests[1].ClientRequestID {
		t.Fatalf("retry identities = %#v", transport.routedRequests)
	}
}

func TestRoutedTransitionUsesSignedCursorHandshakeInsteadOfMutationStorageCursor(t *testing.T) {
	response := routedRuntimeResponse("session-routed")
	transport := &fakeTransport{routedResponses: []client.RoutedSessionV3StartResponse{response}}
	runtime := NewRuntime(transport, NewStore(), nil)
	if err := runtime.PrimeRoutedDraft(routedTestDraft("route this", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.StartRoutedDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	transport.mu.Lock()
	options := append([]client.V3RealtimeResumeOptions(nil), transport.streamOptions...)
	transport.mu.Unlock()
	if len(options) != 1 || !options[0].StartAtCurrent || options[0].EndpointCursor != "" {
		t.Fatalf("routed stream options = %#v", options)
	}
	if len(options[0].Subscriptions) != 1 || options[0].Subscriptions[0].EndpointCursor != "" || options[0].Subscriptions[0].SessionID != "session-routed" {
		t.Fatalf("routed subscription = %#v", options[0].Subscriptions)
	}
	state := runtime.Store().Snapshot()
	if state.Connection != ConnectionReady || state.NeedsRehydrate || state.StaleReason != "" || state.EndpointCursor != "" {
		t.Fatalf("routed transition state = %#v", state)
	}
}

func TestRoutedDraftFailureRestoresLocalIntentAndRetryKeepsIdentity(t *testing.T) {
	response := routedRuntimeResponse("session-routed")
	transport := &fakeTransport{routedErrors: []error{errors.New("router unavailable"), nil}, routedResponses: []client.RoutedSessionV3StartResponse{{}, response}}
	runtime := NewRuntime(transport, nil, nil)
	if err := runtime.PrimeRoutedDraft(routedTestDraft("route this", true)); err != nil {
		t.Fatal(err)
	}
	pending := runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(pending)
	if !ok || draft.ClientRequestID == "" || pending.Session.ID != "" || pending.Connection != ConnectionDisconnected {
		t.Fatalf("local pending state = %#v", pending)
	}
	identity := draft.ClientRequestID
	if _, err := runtime.StartRoutedDraft(context.Background()); err == nil {
		t.Fatal("expected routed start failure")
	}
	failed := runtime.Store().Snapshot()
	draft, ok = SelectRoutedDraft(failed)
	if !ok || draft.Status != RoutedDraftFailed || draft.Prompt != "route this" || !draft.PlanModeRequested || !draft.ManagedWorktreeRequested || draft.ClientRequestID != identity || failed.Session.ID != "" {
		t.Fatalf("failed draft = %#v state=%#v", draft, failed)
	}
	if _, err := runtime.RetryRoutedDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	resolved := runtime.Store().Snapshot()
	draft, ok = SelectRoutedDraft(resolved)
	if !ok || draft.Status != RoutedDraftResolved || draft.ClientRequestID != identity || resolved.Session.ID != "session-routed" || resolved.Connection != ConnectionReady {
		t.Fatalf("resolved state = %#v", resolved)
	}
	transport.mu.Lock()
	requests := append([]client.RoutedSessionV3StartRequest(nil), transport.routedRequests...)
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if len(requests) != 2 || requests[0].ClientRequestID != identity || requests[1].ClientRequestID != identity || requests[1].IdempotencyKey != identity {
		t.Fatalf("retry requests = %#v", requests)
	}
	if !reflect.DeepEqual(calls, []string{"route", "route", "stream", "ready"}) {
		t.Fatalf("routed calls = %#v", calls)
	}
}

func routedTestDraft(prompt string, plan bool) RoutedDraft {
	return RoutedDraft{
		Prompt: prompt, PlanModeRequested: plan, ManagedWorktreeRequested: true,
		AgentName: "swarm", WorkspacePath: "/source", HostWorkspacePath: "/source", RuntimeWorkspacePath: "/source",
		WorkspaceBindingID: "binding-1", SwarmID: "swarm-1", TargetKind: "host", TargetRelationship: "self",
		Metadata: map[string]any{"source": "tui"},
	}
}

func routedRuntimeResponse(sessionID string) client.RoutedSessionV3StartResponse {
	projection := client.SessionV3Projection{SessionID: sessionID, LastEventSeq: 1, ProjectionHighWatermarkSeq: 1}
	message := client.SessionMessage{ID: "message-1", SessionID: sessionID, GlobalSeq: 1, Role: "user", Content: "route this"}
	run := client.SessionV3RunIntent{SessionID: sessionID, RunID: "run-1", Status: "pending_executor", EventSeq: 1}
	outbox := &client.SessionV3RealtimeOutboxRow{EndpointCursor: "cursor-1", SessionID: sessionID, Projection: projection, Event: client.SessionV3Event{ID: "event-1", SessionID: sessionID, Seq: 1}}
	return client.RoutedSessionV3StartResponse{
		OK: true, SessionID: sessionID, Title: "Routed", StartingMode: "plan",
		Session: client.SessionSummary{ID: sessionID, Title: "Routed", Mode: "plan", WorkspacePath: "/runtime", WorktreeEnabled: true, WorktreeRootPath: "/runtime"},
		SessionView: client.RoutedSessionV3SessionView{
			Identity:        &client.RoutedSessionV3Identity{SessionID: sessionID, Title: "Routed", WorkspaceBindingID: "binding-1", SourceWorkspacePath: "/source", RuntimeWorkspacePath: "/runtime", RuntimeSwarmID: "swarm-1", AuthorityHostSwarmID: "swarm-1", WorktreeEnabled: true, WorktreeRootPath: "/runtime"},
			AgenticSettings: &client.RoutedSessionV3AgenticSettings{Mode: "plan", EffectivePreference: client.ModelPreference{Provider: "codex", Model: "gpt"}},
		},
		FirstMessage: message, Projection: projection,
		Mutation: client.SessionV3MutationResult{SessionID: sessionID, Event: client.SessionV3Event{ID: "event-1", SessionID: sessionID, Seq: 1}, Message: &message, RunIntent: &run, Projection: projection, RealtimeOutbox: outbox},
	}
}

func TestRoutedDraftRejectsChangedSourceAuthority(t *testing.T) {
	response := routedRuntimeResponse("session-routed")
	response.SessionView.Identity.SourceWorkspacePath = "/other"
	transport := &fakeTransport{routedResponses: []client.RoutedSessionV3StartResponse{response}}
	runtime := NewRuntime(transport, NewStore(), nil)
	if err := runtime.PrimeRoutedDraft(routedTestDraft("route this", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.StartRoutedDraft(context.Background()); err == nil || !strings.Contains(err.Error(), "changed the captured source workspace") {
		t.Fatalf("authority error = %v", err)
	}
	state := runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(state)
	if !ok || draft.Status != RoutedDraftFailed || state.Session.ID != "" {
		t.Fatalf("authority rejection state = session %#v draft %#v", state.Session, draft)
	}
}

func TestCompactUsesCanonicalSessionAPIWithoutNotes(t *testing.T) {
	transport := &fakeTransport{}
	runtime := NewRuntime(transport, nil, nil)
	runtime.Store().Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-compact"}}})

	result, err := runtime.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if result.SessionID != "session-compact" || transport.compactSessionID != "session-compact" {
		t.Fatalf("compact session = result %q request %q", result.SessionID, transport.compactSessionID)
	}
	if transport.compactRequest != (client.SessionV3CompactOptions{}) {
		t.Fatalf("compact options = %#v, want no note or threshold options", transport.compactRequest)
	}
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

func TestCreateAndSendWithoutInitialPromptPrimesDraftUntilFirstMessage(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "New Session", WorkspacePath: "/workspace", Mode: "plan"}, Preference: client.ModelPreference{Provider: "codex", Model: "resolved"}, SnapshotEndpointCursor: "cursor"}}
	runtime := NewRuntime(transport, nil, nil)
	request := NewSessionRequest{Create: client.SessionCreateOptions{Title: "New Session", WorkspacePath: "/workspace", Mode: "plan", Preference: client.ModelPreference{Provider: "codex", Model: "draft", Thinking: "high"}}}
	result, err := runtime.CreateAndSend(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	transport.mu.Lock()
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("priming persisted an empty session: %#v", calls)
	}
	state := runtime.Store().Snapshot()
	if result.Session.ID != "" || state.Session.ID != "" || state.Session.WorkspacePath != "/workspace" || state.Session.Mode != "plan" || state.Model.Preference.Model != "draft" || len(SelectMessages(state)) != 0 {
		t.Fatalf("primed draft state = %#v result=%#v", state, result)
	}

	if _, err := runtime.Send(context.Background(), "first message", nil); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	calls = append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if !reflect.DeepEqual(calls[:4], []string{"create", "stream", "ready", "send"}) {
		t.Fatalf("first send calls = %#v", calls)
	}
	state = runtime.Store().Snapshot()
	if state.Session.ID != "s" || state.Session.WorkspacePath != "/workspace" || state.Model.Preference.Model != "resolved" || len(SelectMessages(state)) != 1 {
		t.Fatalf("created first-message state = %#v", state)
	}
}

func TestDraftModeUpdatesEffectiveSelectionAndPrimedCreateLosslesslyUntilHydration(t *testing.T) {
	useCurrentBranch := false
	useAccountDefault := true
	useAgentDefault := true
	metadata := map[string]any{"source": "draft", "nested": map[string]any{"keep": true}}
	autoPreference := client.ModelPreference{Provider: "openrouter", Model: "auto-model", Thinking: "medium", ServiceTier: "flex", ContextMode: "auto-context"}
	planPreference := client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "high", ServiceTier: "fast", ContextMode: "plan-context"}
	create := client.SessionCreateOptions{
		Title:                    "New Session",
		WorkspacePath:            "/workspace",
		CWDPath:                  "/workspace/cwd",
		HostWorkspacePath:        "/host/workspace",
		RuntimeWorkspacePath:     "/runtime/workspace",
		WorkspaceName:            "Workspace",
		WorkspaceBindingID:       "binding-1",
		TUIPrimaryCWD:            true,
		Mode:                     "auto",
		AgentName:                "swarm",
		SwarmID:                  "target-1",
		ExecutionClass:           "local",
		TargetKind:               "host",
		TargetRelationship:       "self",
		Metadata:                 metadata,
		Preference:               autoPreference,
		ModelProfile:             &client.SessionV3ModelProfileChoice{UseAccountDefault: &useAccountDefault},
		WorktreeMode:             "on",
		WorktreeUseCurrentBranch: &useCurrentBranch,
		WorktreeBaseBranch:       "dev",
		WorktreeBranchName:       "agent/draft",
	}
	selections := map[string]DraftModeSelection{
		"auto": {
			Preference: autoPreference, ModelProfile: create.ModelProfile, ContextWindow: 180000,
			AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Automatic", ProfileSource: "saved", Preference: autoPreference, ContextWindow: 180000},
		},
		"plan": {
			Preference: planPreference, ModelProfile: &client.SessionV3ModelProfileChoice{UseAgentDefault: &useAgentDefault}, ContextWindow: 200000,
			AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Planning", ProfileSource: "saved", Preference: planPreference, ContextWindow: 200000},
		},
	}
	transport := &fakeTransport{created: client.SessionV3Hydrated{
		Session:                client.SessionSummary{ID: "s", Title: "Backend title", WorkspacePath: "/backend/workspace", Mode: "auto"},
		Preference:             client.ModelPreference{Provider: "codex", Model: "backend-model", Thinking: "medium"},
		AgentModelPolicy:       client.SessionV3AgentModelPolicy{ProfileName: "Backend profile", ProfileSource: "saved", Locked: true},
		SnapshotEndpointCursor: "cursor",
	}}
	runtime := NewRuntime(transport, nil, nil)
	defer runtime.Stop()
	if err := runtime.PrimeNewSession(NewSessionRequest{Create: create, DraftModeSelections: selections}); err != nil {
		t.Fatal(err)
	}
	if state := runtime.Store().Snapshot(); state.Session.Mode != "auto" || state.Model.Preference != autoPreference || state.Model.ProfileName != "Automatic" || state.Model.ContextWindow != 180000 {
		t.Fatalf("initial auto draft selection = %#v", state)
	}
	if err := runtime.SetDraftMode("plan"); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	if len(transport.calls) != 0 || len(transport.createRequests) != 0 || transport.modeRequest != "" {
		transport.mu.Unlock()
		t.Fatalf("draft mode made backend calls: calls=%#v creates=%#v mode=%q", transport.calls, transport.createRequests, transport.modeRequest)
	}
	transport.mu.Unlock()
	if state := runtime.Store().Snapshot(); state.Session.ID != "" || state.Session.Mode != "plan" || state.Model.Preference != planPreference || state.Model.ProfileName != "Planning" || state.Model.ContextWindow != 200000 {
		t.Fatalf("local plan draft selection = %#v", state)
	}
	if err := runtime.SetDraftMode("auto"); err != nil {
		t.Fatal(err)
	}
	if state := runtime.Store().Snapshot(); state.Session.Mode != "auto" || state.Model.Preference != autoPreference || state.Model.ProfileName != "Automatic" || state.Model.ContextWindow != 180000 {
		t.Fatalf("local auto draft selection after round trip = %#v", state)
	}
	if err := runtime.SetDraftMode("plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Send(context.Background(), "first message", map[string]any{"prompt": "metadata"}); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	requests := append([]client.SessionCreateOptions(nil), transport.createRequests...)
	calls := append([]string(nil), transport.calls...)
	transport.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("create requests = %#v", requests)
	}
	want := create
	want.Mode = "plan"
	want.Preference = planPreference
	want.ModelProfile = selections["plan"].ModelProfile
	if !reflect.DeepEqual(requests[0], want) {
		t.Fatalf("first-send create options did not preserve unrelated fields:\n got %#v\nwant %#v", requests[0], want)
	}
	if !reflect.DeepEqual(calls[:4], []string{"create-tui", "stream", "ready", "send"}) {
		t.Fatalf("first-send calls = %#v", calls)
	}
	state := runtime.Store().Snapshot()
	if state.Session.ID != "s" || state.Session.Mode != "auto" || state.Session.Title != "Backend title" || state.Session.WorkspacePath != "/backend/workspace" || state.Model.Preference.Model != "backend-model" || state.Model.ProfileName != "Backend profile" || !state.Model.Locked {
		t.Fatalf("hydrated backend authority did not replace draft assumptions: %#v", state)
	}
}

func TestDraftModeRejectsInvalidModeWithoutChangingDraft(t *testing.T) {
	runtime := NewRuntime(&fakeTransport{}, nil, nil)
	if err := runtime.PrimeNewSession(NewSessionRequest{Create: client.SessionCreateOptions{Mode: "auto"}}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetDraftMode("manual"); err == nil || err.Error() != "session mode must be auto or plan" {
		t.Fatalf("SetDraftMode() error = %v", err)
	}
	if got := runtime.Store().Snapshot().Session.Mode; got != "auto" {
		t.Fatalf("draft mode after invalid update = %q", got)
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
	if len(options) != 1 || !options[0].StartAtCurrent || options[0].EndpointCursor != "" || len(options[0].Subscriptions) != 1 || options[0].Subscriptions[0].SessionID != "s" || !reflect.DeepEqual(options[0].Capabilities, []string{client.V3RealtimeCapabilityLivePatchV1}) {
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

func TestCanonicalRuntimeProviderToolStartRendersVisibleToolRow(t *testing.T) {
	text, err := json.Marshal(map[string]any{
		"type": "session.provider_tool_call.started", "run_id": "run-1", "step": 1, "step_id": "step-1", "event_index": 1,
		"output_index": 0, "call_id": "call-edit", "tool_name": "edit", "status": "started", "recorded_at": int64(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{
		created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "cursor-1"},
		streamFrames: []client.V3RealtimeFrame{{
			Kind: "live.patch",
			Live: &client.V3RealtimeLivePatch{
				SessionID: "s", RunID: "run-1", StreamID: "provider-tool:run-1:step:1:event:1", StreamKind: "provider_tool_call", Operation: "append",
				Step: 1, StepID: "step-1", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: uint64(len(text)), Text: string(text), RecordedAt: 100,
			},
		}},
	}
	runtime := NewRuntime(transport, nil, nil)
	if err := runtime.Hydrate(context.Background(), "s", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	state := runtime.Store().Snapshot()
	tools := SelectLiveTools(state)
	if len(tools) != 1 || tools[0].CallID != "call-edit" || tools[0].Name != "edit" || tools[0].Status != "constructing" {
		t.Fatalf("canonical runtime tools = %#v", tools)
	}
	page := NewPage(runtime, testPageStyles())
	rows := page.renderRows(state, 80, testPageStyles())
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	if got := rendered.String(); !strings.Contains(got, "• edit") || !strings.Contains(got, "editing…") {
		t.Fatalf("canonical provider tool start was not visible:\n%s", got)
	}
}

func TestConnectExistingSessionRequestsLivePatchCapability(t *testing.T) {
	transport := &fakeTransport{created: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "cursor-1"}}
	runtime := NewRuntime(transport, nil, nil)
	if err := runtime.Hydrate(context.Background(), "s", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	transport.mu.Lock()
	options := append([]client.V3RealtimeResumeOptions(nil), transport.streamOptions...)
	transport.mu.Unlock()
	if len(options) != 1 || options[0].StartAtCurrent || options[0].EndpointCursor != "cursor-1" || !reflect.DeepEqual(options[0].Capabilities, []string{client.V3RealtimeCapabilityLivePatchV1}) {
		t.Fatalf("stream options = %#v", options)
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

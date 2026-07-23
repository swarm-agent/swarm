package v3chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/internal/client"
)

// Transport is deliberately limited to the canonical V3 APIs used by chat.
type Transport interface {
	CreateSessionV3WithOptions(context.Context, client.SessionCreateOptions) (client.SessionV3Hydrated, error)
	CreateSessionV3TUIWithOptions(context.Context, client.SessionCreateOptions) (client.SessionV3Hydrated, error)
	GetSessionV3TUI(context.Context, string, string, string) (client.SessionV3Hydrated, error)
	StreamV3Realtime(context.Context, client.V3RealtimeResumeOptions, func(client.V3RealtimeFrame)) error
	SendSessionV3Message(context.Context, string, client.SessionV3MessageOptions) (client.SessionV3MessageResult, error)
	CompactSessionV3(context.Context, string, client.SessionV3CompactOptions) (client.SessionV3CompactResult, error)
	StopSessionV3Run(context.Context, string, string, string, string) error
	SetSessionV3ModeResolved(context.Context, string, string) (client.SessionV3ModeResult, error)
	SetSessionV3Preference(context.Context, string, map[string]any) (client.ModelResolved, error)
	ListProviders(context.Context) ([]client.ProviderStatus, error)
	ListModelCatalog(context.Context, string, int) ([]client.ModelCatalogRecord, error)
}

type permissionTransport interface {
	ResolvePermission(context.Context, string, string, string, string) (client.PermissionRecord, error)
	ExplainPermission(context.Context, string, string, string) (client.PermissionExplain, error)
}

type permissionArgumentsTransport interface {
	ResolvePermissionWithArguments(context.Context, string, string, string, string, string) (client.PermissionRecord, error)
}

type Runtime struct {
	transport Transport
	store     *Store
	wake      func()

	mu           sync.Mutex
	cancel       context.CancelFunc
	ready        chan struct{}
	readyErr     error
	readyOnce    sync.Once
	primedCreate *client.SessionCreateOptions

	workspacePath string
	cwdPath       string
}

func NewRuntime(transport Transport, store *Store, wake func()) *Runtime {
	if store == nil {
		store = NewStore()
	}
	return &Runtime{transport: transport, store: store, wake: wake}
}

func (r *Runtime) Store() *Store {
	if r == nil {
		return nil
	}
	return r.store
}

// PrimeNewSession keeps creation settings local until the first message is sent.
// This lets /new and workspace switching open a configured composer without
// persisting an empty session into the sidebar.
func (r *Runtime) PrimeNewSession(request NewSessionRequest) error {
	if r == nil || r.transport == nil {
		return errors.New("v3 chat transport is not configured")
	}
	create := request.Create
	r.mu.Lock()
	r.primedCreate = &create
	r.workspacePath = strings.TrimSpace(create.WorkspacePath)
	r.cwdPath = strings.TrimSpace(create.CWDPath)
	r.mu.Unlock()
	r.store.Dispatch(PrimeNewSessionAction{Create: create})
	r.signalWake()
	return nil
}

// Hydrate performs one explicit bounded read. It is never called on a timer.
func (r *Runtime) Hydrate(ctx context.Context, sessionID, workspacePath, cwdPath string) error {
	if r == nil || r.transport == nil {
		return errors.New("v3 chat transport is not configured")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	cwdPath = strings.TrimSpace(cwdPath)
	hydrated, err := r.transport.GetSessionV3TUI(ctx, strings.TrimSpace(sessionID), workspacePath, cwdPath)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.workspacePath = workspacePath
	r.cwdPath = cwdPath
	r.mu.Unlock()
	r.store.Dispatch(HydrateAction{Snapshot: hydrated})
	r.signalWake()
	return nil
}

// Connect opens one session-scoped /v3/realtime/stream generation. Read
// cancellation is context/connection-close driven by the transport.
func (r *Runtime) Connect(ctx context.Context) error {
	return r.connect(ctx, false)
}

func (r *Runtime) connect(ctx context.Context, startAtCurrent bool) error {
	if r == nil || r.transport == nil {
		return errors.New("v3 chat transport is not configured")
	}
	state := r.store.Snapshot()
	if state.Session.ID == "" {
		return errors.New("v3 chat session must be committed before connecting")
	}
	if state.EndpointCursor == "" && !startAtCurrent {
		return errors.New("v3 chat hydration must provide an endpoint cursor")
	}

	r.Stop()
	streamCtx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	r.mu.Lock()
	r.cancel = cancel
	r.ready = ready
	r.readyErr = nil
	r.readyOnce = sync.Once{}
	r.mu.Unlock()
	r.store.Dispatch(ConnectionAction{Status: ConnectionConnecting})
	r.signalWake()

	subscription := client.V3RealtimeSubscription{SessionID: state.Session.ID, EndpointCursor: state.EndpointCursor, LastSeq: state.LastEventSeq, SubscriptionID: "tui-v3-chat:" + state.Session.ID}
	go func() {
		err := r.transport.StreamV3Realtime(streamCtx, client.V3RealtimeResumeOptions{EndpointCursor: state.EndpointCursor, Surface: "tui", Subscriptions: []client.V3RealtimeSubscription{subscription}, Capabilities: []string{client.V3RealtimeCapabilityLivePatchV1}, StartAtCurrent: startAtCurrent, OnResumeSent: func() {
			r.markReady(nil)
			r.store.Dispatch(ConnectionAction{Status: ConnectionReady})
			r.signalWake()
		}}, func(frame client.V3RealtimeFrame) {
			next := r.store.Dispatch(RealtimeFrameAction{Frame: frame})
			r.signalWake()
			if next.NeedsRehydrate {
				cancel()
			}
		})
		if streamCtx.Err() != nil {
			return
		}
		if err != nil {
			r.markReady(err)
			r.store.Dispatch(ConnectionAction{Status: ConnectionReconnecting, Reason: err.Error()})
			r.signalWake()
		}
	}()

	select {
	case <-ready:
		r.mu.Lock()
		err := r.readyErr
		r.mu.Unlock()
		return err
	case <-ctx.Done():
		r.Stop()
		return ctx.Err()
	}
}

// Send appends an optimistic user message and reduces the canonical mutation
// result into the same store used by realtime frames.
func (r *Runtime) Send(ctx context.Context, prompt string, metadata map[string]any) (client.SessionV3MessageResult, error) {
	if r == nil || r.transport == nil {
		return client.SessionV3MessageResult{}, errors.New("v3 chat transport is not configured")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return client.SessionV3MessageResult{}, errors.New("message is required")
	}
	state := r.store.Snapshot()
	if strings.TrimSpace(state.Session.ID) == "" {
		r.mu.Lock()
		create := r.primedCreate
		r.mu.Unlock()
		if create == nil {
			return client.SessionV3MessageResult{}, errors.New("v3 chat session is not connected")
		}
		return r.createAndSend(ctx, NewSessionRequest{Create: *create, InitialPrompt: prompt, Metadata: metadata})
	}
	op := strings.ReplaceAll(uuid.NewString(), "-", "")
	messageID := "tui-v3-message:" + op
	clientRequestID := "tui-v3-message-request:" + state.Session.ID + ":" + op
	runID := "tui-v3-run:" + op
	metadata = cloneMetadata(metadata)
	metadata["operation_id"] = op
	r.store.Dispatch(PendingUserAction{Pending: PendingMessage{Message: Message{ID: messageID, SessionID: state.Session.ID, Role: "user", Content: prompt, RunID: runID, OperationID: op}, ClientRequestID: clientRequestID}})
	r.signalWake()
	result, err := r.transport.SendSessionV3Message(ctx, state.Session.ID, client.SessionV3MessageOptions{ClientRequestID: clientRequestID, MessageID: messageID, RunID: runID, Role: "user", Content: prompt, Metadata: metadata})
	if err != nil {
		return client.SessionV3MessageResult{}, err
	}
	r.store.Dispatch(MessageResultAction{Result: result})
	r.signalWake()
	return result, nil
}

func (r *Runtime) Compact(ctx context.Context) (client.SessionV3CompactResult, error) {
	if r == nil || r.transport == nil {
		return client.SessionV3CompactResult{}, errors.New("v3 chat transport is not configured")
	}
	state := r.store.Snapshot()
	sessionID := strings.TrimSpace(state.Session.ID)
	if sessionID == "" {
		return client.SessionV3CompactResult{}, errors.New("v3 chat session is not connected")
	}
	return r.transport.CompactSessionV3(ctx, sessionID, client.SessionV3CompactOptions{})
}

func (r *Runtime) ResolvePermission(ctx context.Context, permissionID, action, reason string) (client.PermissionRecord, error) {
	return r.ResolvePermissionWithArguments(ctx, permissionID, action, reason, "")
}

func (r *Runtime) ResolvePermissionWithArguments(ctx context.Context, permissionID, action, reason, approvedArguments string) (client.PermissionRecord, error) {
	if r == nil || r.transport == nil {
		return client.PermissionRecord{}, errors.New("v3 chat transport is not configured")
	}
	resolver, ok := r.transport.(permissionTransport)
	if !ok {
		return client.PermissionRecord{}, errors.New("v3 chat permission transport is not configured")
	}
	state := r.store.Snapshot()
	sessionID := strings.TrimSpace(state.Session.ID)
	if sessionID == "" {
		return client.PermissionRecord{}, errors.New("v3 chat session is not connected")
	}
	permissionID = strings.TrimSpace(permissionID)
	if permissionID == "" {
		return client.PermissionRecord{}, errors.New("permission id is required")
	}
	var record client.PermissionRecord
	var err error
	if approvedArguments = strings.TrimSpace(approvedArguments); approvedArguments != "" {
		withArguments, supported := r.transport.(permissionArgumentsTransport)
		if !supported {
			return client.PermissionRecord{}, errors.New("v3 chat permission transport does not support approved arguments")
		}
		record, err = withArguments.ResolvePermissionWithArguments(ctx, sessionID, permissionID, strings.TrimSpace(action), strings.TrimSpace(reason), approvedArguments)
	} else {
		record, err = resolver.ResolvePermission(ctx, sessionID, permissionID, strings.TrimSpace(action), strings.TrimSpace(reason))
	}
	if err != nil {
		return client.PermissionRecord{}, err
	}
	r.store.Dispatch(PermissionAction{Record: record})
	r.signalWake()
	return record, nil
}

func (r *Runtime) SetMode(ctx context.Context, mode string) (client.SessionV3ModeResult, error) {
	if r == nil || r.transport == nil {
		return client.SessionV3ModeResult{}, errors.New("v3 chat transport is not configured")
	}
	state := r.store.Snapshot()
	sessionID := strings.TrimSpace(state.Session.ID)
	if sessionID == "" {
		return client.SessionV3ModeResult{}, errors.New("v3 chat session is not connected")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "auto" && mode != "plan" {
		return client.SessionV3ModeResult{}, errors.New("session mode must be auto or plan")
	}
	resolved, err := r.transport.SetSessionV3ModeResolved(ctx, sessionID, mode)
	if err != nil {
		return client.SessionV3ModeResult{}, err
	}
	r.store.Dispatch(ModeAction{Resolved: resolved})
	r.signalWake()
	return resolved, nil
}

func (r *Runtime) SetModelPreference(ctx context.Context, preference client.ModelPreference) (client.ModelResolved, error) {
	if r == nil || r.transport == nil {
		return client.ModelResolved{}, errors.New("v3 chat transport is not configured")
	}
	state := r.store.Snapshot()
	sessionID := strings.TrimSpace(state.Session.ID)
	if sessionID == "" {
		return client.ModelResolved{}, errors.New("v3 chat session is not connected")
	}
	if state.Model.Locked {
		return client.ModelResolved{}, errors.New(firstNonEmpty(state.Model.LockReason, "session model is controlled by its agent policy"))
	}
	preference = normalizeModelPreference(preference)
	if preference.Provider == "" || preference.Model == "" {
		return client.ModelResolved{}, errors.New("provider and model are required")
	}
	resolved, err := r.transport.SetSessionV3Preference(ctx, sessionID, map[string]any{
		"provider":     preference.Provider,
		"model":        preference.Model,
		"thinking":     preference.Thinking,
		"service_tier": preference.ServiceTier,
		"context_mode": preference.ContextMode,
	})
	if err != nil {
		return client.ModelResolved{}, err
	}
	r.store.Dispatch(ModelPreferenceAction{Resolved: resolved})
	r.signalWake()
	return resolved, nil
}

func (r *Runtime) ListModelOptions(ctx context.Context) ([]client.ModelCatalogRecord, error) {
	if r == nil || r.transport == nil {
		return nil, errors.New("v3 chat transport is not configured")
	}
	providers, err := r.transport.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	var options []client.ModelCatalogRecord
	for _, provider := range providers {
		providerID := strings.ToLower(strings.TrimSpace(provider.ID))
		if providerID == "" || !provider.Runnable {
			continue
		}
		records, err := r.transport.ListModelCatalog(ctx, providerID, 1200)
		if err != nil {
			return nil, fmt.Errorf("list %s model catalog: %w", providerID, err)
		}
		for i := range records {
			if strings.TrimSpace(records[i].Provider) == "" {
				records[i].Provider = providerID
			}
			options = append(options, records[i])
		}
	}
	return options, nil
}

func (r *Runtime) StopActiveRun(ctx context.Context, reason string) error {
	if r == nil || r.transport == nil {
		return errors.New("v3 chat transport is not configured")
	}
	state := r.store.Snapshot()
	run, ok := SelectActiveRun(state)
	if !ok || strings.TrimSpace(run.ID) == "" {
		return errors.New("v3 chat has no active run")
	}
	targetSwarmID := strings.TrimSpace(state.Session.TargetSwarmID)
	if targetSwarmID == "" {
		return errors.New("v3 chat session swarm target is not configured")
	}
	return r.transport.StopSessionV3Run(ctx, state.Session.ID, run.ID, targetSwarmID, strings.TrimSpace(reason))
}

func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RecoverStale performs the only automatic read after bootstrap: a named
// cursor-gap recovery. The caller must explicitly invoke it after observing
// NeedsRehydrate, preventing periodic refresh from becoming an authority.
func (r *Runtime) RecoverStale(ctx context.Context, workspacePath, cwdPath string) error {
	state := r.store.Snapshot()
	if !state.NeedsRehydrate {
		return errors.New("v3 chat state is not stale")
	}
	if strings.TrimSpace(workspacePath) == "" && strings.TrimSpace(cwdPath) == "" {
		r.mu.Lock()
		workspacePath, cwdPath = r.workspacePath, r.cwdPath
		r.mu.Unlock()
	}
	if err := r.Hydrate(ctx, state.Session.ID, workspacePath, cwdPath); err != nil {
		return err
	}
	return r.Connect(ctx)
}

type NewSessionRequest struct {
	Create        client.SessionCreateOptions
	InitialPrompt string
	Metadata      map[string]any
}

// CreateAndSend keeps an empty request local and performs the canonical create
// only when the request includes the first message.
func (r *Runtime) CreateAndSend(ctx context.Context, request NewSessionRequest) (client.SessionV3MessageResult, error) {
	if strings.TrimSpace(request.InitialPrompt) == "" {
		if err := r.PrimeNewSession(request); err != nil {
			return client.SessionV3MessageResult{}, err
		}
		return client.SessionV3MessageResult{}, nil
	}
	return r.createAndSend(ctx, request)
}

func (r *Runtime) createAndSend(ctx context.Context, request NewSessionRequest) (client.SessionV3MessageResult, error) {
	if r == nil || r.transport == nil {
		return client.SessionV3MessageResult{}, errors.New("v3 chat transport is not configured")
	}
	prompt := strings.TrimSpace(request.InitialPrompt)
	var (
		created client.SessionV3Hydrated
		err     error
	)
	if request.Create.TUIPrimaryCWD {
		created, err = r.transport.CreateSessionV3TUIWithOptions(ctx, request.Create)
	} else {
		created, err = r.transport.CreateSessionV3WithOptions(ctx, request.Create)
	}
	if err != nil {
		return client.SessionV3MessageResult{}, err
	}
	if strings.TrimSpace(created.Session.ID) == "" {
		return client.SessionV3MessageResult{}, errors.New("v3 session create returned no session id")
	}
	r.mu.Lock()
	r.primedCreate = nil
	r.workspacePath = strings.TrimSpace(request.Create.WorkspacePath)
	r.cwdPath = strings.TrimSpace(request.Create.CWDPath)
	r.mu.Unlock()
	r.store.Dispatch(HydrateAction{Snapshot: created})
	r.signalWake()
	if err := r.connect(ctx, strings.TrimSpace(created.SnapshotEndpointCursor) == ""); err != nil {
		return client.SessionV3MessageResult{}, fmt.Errorf("connect created v3 session: %w", err)
	}
	op := strings.ReplaceAll(uuid.NewString(), "-", "")
	messageID := "tui-v3-message:" + op
	clientRequestID := "tui-v3-new-message:" + created.Session.ID + ":" + op
	runID := "tui-v3-run:" + op
	metadata := cloneMetadata(request.Metadata)
	metadata["operation_id"] = op
	pending := PendingMessage{Message: Message{ID: messageID, SessionID: created.Session.ID, Role: "user", Content: prompt, RunID: runID, OperationID: op}, ClientRequestID: clientRequestID}
	r.store.Dispatch(PendingUserAction{Pending: pending})
	r.signalWake()
	result, err := r.transport.SendSessionV3Message(ctx, created.Session.ID, client.SessionV3MessageOptions{ClientRequestID: clientRequestID, MessageID: messageID, RunID: runID, Role: "user", Content: prompt, Metadata: metadata})
	if err != nil {
		return client.SessionV3MessageResult{}, err
	}
	r.store.Dispatch(MessageResultAction{Result: result})
	r.signalWake()
	return result, nil
}

func (r *Runtime) markReady(err error) {
	r.mu.Lock()
	r.readyOnce.Do(func() {
		r.readyErr = err
		if r.ready != nil {
			close(r.ready)
		}
	})
	r.mu.Unlock()
}
func (r *Runtime) signalWake() {
	if r != nil && r.wake != nil {
		r.wake()
	}
}
func cloneMetadata(value map[string]any) map[string]any {
	out := make(map[string]any, len(value)+1)
	for key, item := range value {
		out[key] = item
	}
	return out
}

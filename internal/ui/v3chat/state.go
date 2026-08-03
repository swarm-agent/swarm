// Package v3chat owns the state boundary for the V3-native TUI chat.
//
// It intentionally depends only on the V3 client contract. It does not share
// state or rendering machinery with the legacy chat page.
package v3chat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
)

const (
	// State is a bounded render cache. Older history remains durable and can be
	// requested explicitly; the live page retains only a tail window.
	maxResidentMessages = 2000
	maxLiveSegmentBytes = 4 << 20
)

type ConnectionStatus string

const (
	ConnectionDisconnected ConnectionStatus = "disconnected"
	ConnectionConnecting   ConnectionStatus = "connecting"
	ConnectionReady        ConnectionStatus = "ready"
	ConnectionReconnecting ConnectionStatus = "reconnecting"
	ConnectionStale        ConnectionStatus = "stale"
)

type Session struct {
	ID            string
	Title         string
	WorkspacePath string
	WorkspaceName string
	Mode          string
	TargetSwarmID string
	CreatedAt     int64
	UpdatedAt     int64
}

type ModelState struct {
	Preference      client.ModelPreference
	ContextWindow   int
	MaxOutputTokens int
	Locked          bool
	LockReason      string
	ProfileName     string
	ProfileSource   string
	ProfileMode     string
}

type UsageState struct {
	Available       bool
	ContextWindow   int
	RemainingTokens int64
	TotalTokens     int64
}

type PlanState struct {
	HasActivePlan bool
	ActivePlan    *client.SessionPlan
}

type Message struct {
	ID           string
	SessionID    string
	GlobalSeq    uint64
	Role         string
	Content      string
	CreatedAt    int64
	RunID        string
	OperationID  string
	Metadata     map[string]any
	FinalHandoff *client.PlanFinalHandoff
}

type PendingMessage struct {
	Message
	ClientRequestID string
}

type RoutedDraftStatus string

const (
	RoutedDraftReady    RoutedDraftStatus = "ready"
	RoutedDraftRouting  RoutedDraftStatus = "routing"
	RoutedDraftResolved RoutedDraftStatus = "resolved"
	RoutedDraftFailed   RoutedDraftStatus = "failed"
)

type RoutedDraft struct {
	Prompt                   string
	PlanModeRequested        bool
	ManagedWorktreeRequested bool
	ClientRequestID          string
	AgentName                string
	WorkspacePath            string
	HostWorkspacePath        string
	RuntimeWorkspacePath     string
	WorkspaceBindingID       string
	SwarmID                  string
	TargetKind               string
	TargetRelationship       string
	Metadata                 map[string]any
	Status                   RoutedDraftStatus
	Error                    string
}

type PermissionTimelineItem struct {
	Record    client.PermissionRecord
	GlobalSeq uint64
}

type PermissionState struct {
	Records []PermissionTimelineItem
}

func permissionsFromClient(records []client.PermissionRecord) PermissionState {
	items := make([]PermissionTimelineItem, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.ID) != "" {
			items = append(items, PermissionTimelineItem{Record: record})
		}
	}
	sortPermissionTimeline(items)
	return PermissionState{Records: items}
}

func sortPermissionTimeline(items []PermissionTimelineItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.GlobalSeq != right.GlobalSeq {
			if left.GlobalSeq == 0 {
				return false
			}
			if right.GlobalSeq == 0 {
				return true
			}
			return left.GlobalSeq < right.GlobalSeq
		}
		leftAt := firstPositiveInt64(left.Record.PermissionRequestedAt, left.Record.CreatedAt)
		rightAt := firstPositiveInt64(right.Record.PermissionRequestedAt, right.Record.CreatedAt)
		if leftAt != rightAt {
			return leftAt < rightAt
		}
		return left.Record.ID < right.Record.ID
	})
}

// applyPermissionRecord preserves the request's first durable sequence so a
// permission.updated event changes the existing timeline card in place.
func applyPermissionRecord(state State, record client.PermissionRecord, eventSeq uint64) State {
	records := append([]PermissionTimelineItem(nil), state.Permissions.Records...)
	replaced := false
	for index := range records {
		if strings.TrimSpace(records[index].Record.ID) == strings.TrimSpace(record.ID) {
			records[index].Record = record
			if records[index].GlobalSeq == 0 {
				records[index].GlobalSeq = eventSeq
			}
			replaced = true
			break
		}
	}
	if !replaced {
		records = append(records, PermissionTimelineItem{Record: record, GlobalSeq: eventSeq})
	}
	sortPermissionTimeline(records)
	state.Permissions = PermissionState{Records: records}
	return state
}

type RunState struct {
	ID                   string
	Status               string
	CreatedAt            int64
	StartedAt            int64
	CompletedAt          int64
	DurationMS           int64
	CumulativeDurationMS int64
	UpdatedAt            int64
	EventSeq             uint64
}

func runStateFromClient(value client.SessionV3RunIntent) RunState {
	return RunState{
		ID:                   strings.TrimSpace(value.RunID),
		Status:               strings.ToLower(strings.TrimSpace(value.Status)),
		CreatedAt:            value.CreatedAt,
		StartedAt:            value.StartedAt,
		CompletedAt:          value.CompletedAt,
		DurationMS:           value.DurationMS,
		CumulativeDurationMS: value.CumulativeDurationMS,
		UpdatedAt:            value.UpdatedAt,
		EventSeq:             value.EventSeq,
	}
}

func runStatusActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending_executor", "running":
		return true
	default:
		return false
	}
}

func applyRunIntent(state State, value client.SessionV3RunIntent, eventSeq uint64) State {
	run := runStateFromClient(value)
	if run.EventSeq == 0 {
		run.EventSeq = eventSeq
	}
	if state.LatestRun != nil {
		previous := *state.LatestRun
		if previous.ID == run.ID {
			if run.EventSeq != 0 && previous.EventSeq != 0 && run.EventSeq <= previous.EventSeq {
				return state
			}
			run = mergeRunTiming(previous, run)
		}
	}
	state.LatestRun = &run
	if runStatusActive(run.Status) {
		state.CurrentRun = &run
	} else {
		state.CurrentRun = nil
	}
	return state
}

// Realtime event payloads are sometimes assembled before the mutation store
// attaches canonical timing. Preserve the authoritative anchor already reduced
// for the same run instead of treating omitted timing as a new start.
func mergeRunTiming(previous, incoming RunState) RunState {
	incoming.CreatedAt = firstPositiveInt64(previous.CreatedAt, incoming.CreatedAt)
	incoming.StartedAt = firstPositiveInt64(previous.StartedAt, incoming.StartedAt)
	incoming.CompletedAt = firstPositiveInt64(incoming.CompletedAt, previous.CompletedAt)
	incoming.DurationMS = maxInt64Value(previous.DurationMS, incoming.DurationMS)
	incoming.CumulativeDurationMS = maxInt64Value(previous.CumulativeDurationMS, incoming.CumulativeDurationMS)
	incoming.UpdatedAt = maxInt64Value(previous.UpdatedAt, incoming.UpdatedAt)
	incoming.EventSeq = maxUint64(previous.EventSeq, incoming.EventSeq)
	if incoming.Status == "" {
		incoming.Status = previous.Status
	}
	return incoming
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func maxInt64Value(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

type LiveSegment struct {
	RunID      string
	StreamID   string
	Text       string
	GlobalSeq  uint64
	CreatedAt  int64
	LiveSeqEnd uint64
	OffsetEnd  uint64
}

type ReasoningSegment struct {
	Key          string
	RunID        string
	Step         int
	StepID       string
	ReasoningID  string
	ReasoningKey string
	Text         string
	Summary      string
	Status       string
	GlobalSeq    uint64
	StartedAt    int64
	CompletedAt  int64
	UpdatedAt    int64
}

type State struct {
	Session        Session
	Messages       []Message
	Pending        map[string]PendingMessage
	CurrentRun     *RunState
	LatestRun      *RunState
	Live           map[string]LiveSegment
	Reasoning      map[string]ReasoningSegment
	Tools          map[string]ToolTimelineItem
	Connection     ConnectionStatus
	EndpointCursor string
	LastEventSeq   uint64
	StaleReason    string
	NeedsRehydrate bool
	Model          ModelState
	Usage          UsageState
	Plan           PlanState
	Permissions    PermissionState
	RoutedDraft    *RoutedDraft
}

type Action interface{ isV3ChatAction() }

type HydrateAction struct{ Snapshot client.SessionV3Hydrated }

func (HydrateAction) isV3ChatAction() {}

type PrimeNewSessionAction struct {
	Create    client.SessionCreateOptions
	Selection DraftModeSelection
}

func (PrimeNewSessionAction) isV3ChatAction() {}

type DraftModeAction struct {
	Mode      string
	Selection DraftModeSelection
}

func (DraftModeAction) isV3ChatAction() {}

type PendingUserAction struct{ Pending PendingMessage }

func (PendingUserAction) isV3ChatAction() {}

type PrimeRoutedDraftAction struct{ Draft RoutedDraft }

func (PrimeRoutedDraftAction) isV3ChatAction() {}

type RoutedDraftRoutingAction struct{}

func (RoutedDraftRoutingAction) isV3ChatAction() {}

type RoutedDraftFailedAction struct{ Error string }

func (RoutedDraftFailedAction) isV3ChatAction() {}

type RoutedDraftResolvedAction struct {
	Response client.RoutedSessionV3StartResponse
}

func (RoutedDraftResolvedAction) isV3ChatAction() {}

type MessageResultAction struct{ Result client.SessionV3MessageResult }

func (MessageResultAction) isV3ChatAction() {}

type RealtimeFrameAction struct{ Frame client.V3RealtimeFrame }

func (RealtimeFrameAction) isV3ChatAction() {}

type ConnectionAction struct {
	Status ConnectionStatus
	Reason string
}

func (ConnectionAction) isV3ChatAction() {}

type ModelPreferenceAction struct {
	Resolved client.ModelResolved
}

func (ModelPreferenceAction) isV3ChatAction() {}

type ModelProfileAction struct {
	Policy client.SessionV3AgentModelPolicy
}

func (ModelProfileAction) isV3ChatAction() {}

type ModeAction struct {
	Resolved client.SessionV3ModeResult
}

func (ModeAction) isV3ChatAction() {}

type PermissionsAction struct {
	Records []client.PermissionRecord
}

func (PermissionsAction) isV3ChatAction() {}

type PermissionAction struct {
	Record client.PermissionRecord
}

func (PermissionAction) isV3ChatAction() {}

// Reduce returns a detached next state. Maps and slices are copied before they
// are changed so render selectors can safely retain an older snapshot.
func Reduce(current State, action Action) State {
	next := cloneState(current)
	switch value := action.(type) {
	case HydrateAction:
		next = reduceHydrated(next, value.Snapshot)
	case PrimeNewSessionAction:
		next = reducePrimedNewSession(value.Create, value.Selection)
	case DraftModeAction:
		next.Session.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
		applyDraftModeSelection(&next.Model, value.Selection)
	case PrimeRoutedDraftAction:
		draft := value.Draft
		draft.Prompt = strings.TrimSpace(draft.Prompt)
		draft.ClientRequestID = strings.TrimSpace(draft.ClientRequestID)
		draft.AgentName = strings.TrimSpace(draft.AgentName)
		draft.WorkspacePath = strings.TrimSpace(draft.WorkspacePath)
		draft.HostWorkspacePath = strings.TrimSpace(draft.HostWorkspacePath)
		draft.RuntimeWorkspacePath = strings.TrimSpace(draft.RuntimeWorkspacePath)
		draft.WorkspaceBindingID = strings.TrimSpace(draft.WorkspaceBindingID)
		draft.SwarmID = strings.TrimSpace(draft.SwarmID)
		draft.TargetKind = strings.TrimSpace(draft.TargetKind)
		draft.TargetRelationship = strings.TrimSpace(draft.TargetRelationship)
		draft.Metadata = cloneAnyMap(draft.Metadata)
		draft.Status = RoutedDraftReady
		draft.Error = ""
		next = NewState()
		next.RoutedDraft = &draft
	case RoutedDraftRoutingAction:
		if next.RoutedDraft != nil && next.RoutedDraft.Status != RoutedDraftResolved {
			next.RoutedDraft.Status = RoutedDraftRouting
			next.RoutedDraft.Error = ""
		}
	case RoutedDraftFailedAction:
		if next.RoutedDraft != nil && next.RoutedDraft.Status != RoutedDraftResolved {
			next.RoutedDraft.Status = RoutedDraftFailed
			next.RoutedDraft.Error = strings.TrimSpace(value.Error)
		}
	case RoutedDraftResolvedAction:
		draft := next.RoutedDraft
		next = reduceHydrated(NewState(), value.Response.Hydrated())
		if draft != nil {
			resolved := *draft
			resolved.Metadata = cloneAnyMap(draft.Metadata)
			resolved.Status = RoutedDraftResolved
			resolved.Error = ""
			next.RoutedDraft = &resolved
		}
	case PendingUserAction:
		pending := value.Pending
		if id := strings.TrimSpace(pending.ID); id != "" {
			pending.ID = id
			if pending.Role == "" {
				pending.Role = "user"
			}
			next.Pending[id] = pending
		}
	case MessageResultAction:
		next = reduceMessageResult(next, value.Result)
	case RealtimeFrameAction:
		next = reduceRealtimeFrame(next, value.Frame)
	case ConnectionAction:
		next.Connection = value.Status
		next.StaleReason = strings.TrimSpace(value.Reason)
		if value.Status != ConnectionStale {
			next.NeedsRehydrate = false
		}
	case ModelPreferenceAction:
		next.Model.Preference = normalizeModelPreference(value.Resolved.Preference)
		next.Model.ContextWindow = value.Resolved.ContextWindow
		next.Model.MaxOutputTokens = value.Resolved.MaxOutputTokens
	case ModelProfileAction:
		applyAgentModelPolicy(&next.Model, value.Policy.Preference, value.Policy.ContextWindow, value.Policy.MaxOutputTokens, value.Policy)
	case ModeAction:
		next.Session.Mode = strings.ToLower(strings.TrimSpace(value.Resolved.Mode))
		applyAgentModelPolicy(&next.Model, value.Resolved.Preference, value.Resolved.ContextWindow, value.Resolved.MaxOutputTokens, value.Resolved.AgentModelPolicy)
	case PermissionsAction:
		next.Permissions = permissionsFromClient(value.Records)
	case PermissionAction:
		next = applyPermissionRecord(next, value.Record, 0)
	}
	return next
}

func reducePrimedNewSession(create client.SessionCreateOptions, selection DraftModeSelection) State {
	state := NewState()
	state.Session = Session{
		Title:         strings.TrimSpace(create.Title),
		WorkspacePath: strings.TrimSpace(firstNonEmpty(create.WorkspacePath, create.CWDPath)),
		WorkspaceName: strings.TrimSpace(create.WorkspaceName),
		Mode:          strings.ToLower(strings.TrimSpace(create.Mode)),
	}
	applyDraftModeSelection(&state.Model, selection)
	return state
}

func applyDraftModeSelection(state *ModelState, selection DraftModeSelection) {
	if state == nil {
		return
	}
	*state = ModelState{}
	applyAgentModelPolicy(state, selection.Preference, selection.ContextWindow, selection.MaxOutputTokens, selection.AgentModelPolicy)
}

func reduceHydrated(state State, hydrated client.SessionV3Hydrated) State {
	state.Session = sessionFromClient(hydrated.Session, hydrated.Projection.SessionID)
	preference := normalizeModelPreference(hydrated.Preference)
	if preference.Provider == "" && preference.Model == "" {
		preference = normalizeModelPreference(hydrated.Session.Preference)
	}
	state.Model = ModelState{}
	applyAgentModelPolicy(&state.Model, preference, hydrated.ContextWindow, hydrated.MaxOutputTokens, hydrated.AgentModelPolicy)
	state.Usage = usageStateFromSummary(hydrated.UsageSummary)
	state.Plan = planStateFromHydrated(hydrated)
	state.Permissions = permissionsFromClient(hydrated.PendingPermissions)
	state.Messages = mergeMessages(nil, hydrated.Messages)
	state.Reasoning = make(map[string]ReasoningSegment)
	state.Tools = make(map[string]ToolTimelineItem)
	state.LastEventSeq = 0
	state.EndpointCursor = strings.TrimSpace(hydrated.SnapshotEndpointCursor)
	state.CurrentRun = nil
	state.LatestRun = nil
	if hydrated.ActiveRunIntent != nil {
		state = applyRunIntent(state, *hydrated.ActiveRunIntent, hydrated.ActiveRunIntent.EventSeq)
	} else {
		state.CurrentRun = nil
		state.LatestRun = nil
	}
	for _, event := range sortedEvents(hydrated.Events) {
		state = applyEvent(state, event)
	}
	if hydrated.Projection.LastEventSeq > state.LastEventSeq {
		state.LastEventSeq = hydrated.Projection.LastEventSeq
	}
	state = reconcilePending(state)
	state = reconcileDurableTools(state)
	state = reconcileDurableLive(state)
	state.NeedsRehydrate = false
	state.StaleReason = ""
	return state
}

func reduceMessageResult(state State, result client.SessionV3MessageResult) State {
	if strings.TrimSpace(result.Session.ID) != "" {
		incoming := sessionFromClient(result.Session, state.Session.ID)
		state.Session.ID = incoming.ID
		if incoming.Title != "" {
			state.Session.Title = incoming.Title
		}
		if incoming.Mode != "" {
			state.Session.Mode = incoming.Mode
		}
		if incoming.CreatedAt != 0 {
			state.Session.CreatedAt = incoming.CreatedAt
		}
		if incoming.UpdatedAt != 0 {
			state.Session.UpdatedAt = incoming.UpdatedAt
		}
	}
	state.Messages = mergeMessages(state.Messages, append(result.Messages, result.Message))
	for _, event := range sortedEvents(result.Events) {
		state = applyEvent(state, event)
	}
	if result.Projection.LastEventSeq > state.LastEventSeq {
		state.LastEventSeq = result.Projection.LastEventSeq
	}
	if strings.TrimSpace(result.RunIntent.RunID) != "" {
		state = applyRunIntent(state, result.RunIntent, result.RunIntent.EventSeq)
	}
	// RealtimeOutbox.EndpointCursor is an internal durable outbox cursor. Only
	// signed cursors received from hydration or realtime frames may advance the
	// transport resume cursor retained by this store.
	state = reconcilePending(state)
	state = reconcileDurableTools(state)
	return reconcileDurableLive(state)
}

func reduceRealtimeFrame(state State, frame client.V3RealtimeFrame) State {
	kind := strings.ToLower(strings.TrimSpace(frame.Kind))
	switch kind {
	case "cursor.error", "slow_consumer.reconnect_required":
		state.Connection = ConnectionStale
		state.NeedsRehydrate = true
		state.StaleReason = firstNonEmpty(frame.Error, frame.Reason, frame.ErrorCode, kind)
		return state
	case "auth.denied":
		state.Connection = ConnectionStale
		state.StaleReason = firstNonEmpty(frame.Error, frame.Reason, frame.ErrorCode, kind)
		return state
	case "live.patch":
		state = applyLivePatch(state, frame.Live)
	case "event":
		if frame.Event != nil && frame.Event.Seq > state.LastEventSeq {
			state = applyEvent(state, *frame.Event)
			state.LastEventSeq = frame.Event.Seq
			state = reconcilePending(state)
			state = reconcileDurableTools(state)
			state = reconcileDurableLive(state)
		}
	case "projection.high_watermark", "endpoint.watermark", "hello", "keepalive", "replay.started", "replay.complete":
		// Control state only; durable chat state is unchanged.
	}
	// Cursor progress is committed only after the frame above has reduced.
	if cursor := strings.TrimSpace(frame.EndpointCursor); cursor != "" {
		state.EndpointCursor = cursor
	}
	return state
}

func applyEvent(state State, event client.SessionV3Event) State {
	if event.Seq != 0 && event.Seq <= state.LastEventSeq {
		return state
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(event.Payload, &payload)
	if strings.EqualFold(strings.TrimSpace(event.EventType), "session.mode.updated") {
		if updatedAt := rawInt64(payload, "updated_at"); updatedAt > 0 && state.Session.UpdatedAt > 0 && updatedAt < state.Session.UpdatedAt {
			if event.Seq > state.LastEventSeq {
				state.LastEventSeq = event.Seq
			}
			return state
		}
	}
	if raw := payload["session"]; len(raw) > 0 {
		var session client.SessionSummary
		if json.Unmarshal(raw, &session) == nil && strings.TrimSpace(session.ID) != "" {
			state.Session = sessionFromClient(session, state.Session.ID)
		}
	}
	if raw := payload["title"]; len(raw) > 0 {
		var title string
		if json.Unmarshal(raw, &title) == nil && strings.TrimSpace(title) != "" {
			state.Session.Title = strings.TrimSpace(title)
		}
	}
	if raw := payload["preference"]; len(raw) > 0 {
		var preference client.ModelPreference
		if json.Unmarshal(raw, &preference) == nil {
			state.Model.Preference = normalizeModelPreference(preference)
		}
	}
	if strings.EqualFold(strings.TrimSpace(event.EventType), "session.mode.updated") {
		state = applySessionModeUpdatedEvent(state, payload)
	}
	if raw := payload["usage_summary"]; len(raw) > 0 {
		var summary client.SessionUsageSummary
		if json.Unmarshal(raw, &summary) == nil {
			state.Usage = usageStateFromSummary(&summary)
		}
	}
	if strings.EqualFold(strings.TrimSpace(event.EventType), "session.plan.saved") {
		state.Plan = planStateFromPayload(state.Plan, payload)
	}
	if raw := payload["message"]; len(raw) > 0 {
		var message client.SessionMessage
		if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message.ID) != "" {
			state.Messages = mergeMessages(state.Messages, []client.SessionMessage{message})
		}
	}
	if raw := payload["permission"]; len(raw) > 0 {
		var permission client.PermissionRecord
		if json.Unmarshal(raw, &permission) == nil && strings.TrimSpace(permission.ID) != "" {
			state = applyPermissionRecord(state, permission, event.Seq)
		}
	}
	if raw := payload["run_intent"]; len(raw) > 0 {
		var value client.SessionV3RunIntent
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.RunID) != "" {
			state = applyRunIntent(state, value, event.Seq)
		}
	}
	state = applyAssistantTimelineEvent(state, event, payload)
	state = applyReasoningEvent(state, event, payload)
	state = applyToolEvent(state, clientSessionV3Event{Seq: event.Seq, EventType: event.EventType, Timestamp: event.TsUnixMS}, payload)
	if event.Seq > state.LastEventSeq {
		state.LastEventSeq = event.Seq
	}
	return state
}

func applySessionModeUpdatedEvent(state State, payload map[string]json.RawMessage) State {
	if raw := payload["mode"]; len(raw) > 0 {
		var mode string
		if json.Unmarshal(raw, &mode) == nil {
			state.Session.Mode = strings.ToLower(strings.TrimSpace(mode))
		}
	}
	if raw := payload["updated_at"]; len(raw) > 0 {
		var updatedAt int64
		if json.Unmarshal(raw, &updatedAt) == nil {
			state.Session.UpdatedAt = updatedAt
		}
	}
	if raw := payload["context_window"]; len(raw) > 0 {
		var contextWindow int
		if json.Unmarshal(raw, &contextWindow) == nil {
			state.Model.ContextWindow = contextWindow
		}
	}
	if raw := payload["max_output_tokens"]; len(raw) > 0 {
		var maxOutputTokens int
		if json.Unmarshal(raw, &maxOutputTokens) == nil {
			state.Model.MaxOutputTokens = maxOutputTokens
		}
	}
	if raw := payload["agent_model_policy"]; len(raw) > 0 {
		var policy client.SessionV3AgentModelPolicy
		if json.Unmarshal(raw, &policy) == nil {
			applyAgentModelPolicy(&state.Model, state.Model.Preference, state.Model.ContextWindow, state.Model.MaxOutputTokens, policy)
		}
	}
	return state
}

func applyLivePatch(state State, patch *client.V3RealtimeLivePatch) State {
	if patch == nil || strings.TrimSpace(patch.StreamID) == "" {
		return state
	}
	if strings.EqualFold(strings.TrimSpace(patch.StreamKind), "provider_tool_call") {
		var payload map[string]json.RawMessage
		if json.Unmarshal([]byte(patch.Text), &payload) != nil {
			return state
		}
		eventType := rawString(payload, "type")
		if eventType == "" {
			return state
		}
		return applyToolEvent(state, clientSessionV3Event{EventType: eventType, Timestamp: patch.RecordedAt}, payload)
	}
	key := liveKey(patch.RunID, patch.StreamID)
	current := state.Live[key]
	if patch.LiveSeqEnd <= current.LiveSeqEnd {
		return state
	}
	if current.LiveSeqEnd != 0 && (patch.LiveSeqStart != current.LiveSeqEnd+1 || patch.OffsetStart != current.OffsetEnd) {
		state.Connection = ConnectionStale
		state.NeedsRehydrate = true
		state.StaleReason = "live patch continuity gap"
		return state
	}
	if current.LiveSeqEnd == 0 && patch.OffsetStart != 0 {
		state.Connection = ConnectionStale
		state.NeedsRehydrate = true
		state.StaleReason = "live patch initial offset gap"
		return state
	}
	if len(current.Text)+len(patch.Text) > maxLiveSegmentBytes {
		state.Connection = ConnectionStale
		state.NeedsRehydrate = true
		state.StaleReason = "live patch memory limit exceeded"
		return state
	}
	current.RunID = strings.TrimSpace(patch.RunID)
	current.StreamID = strings.TrimSpace(patch.StreamID)
	current.CreatedAt = firstPositiveInt64(current.CreatedAt, patch.RecordedAt)
	current.Text += patch.Text
	current.LiveSeqEnd = patch.LiveSeqEnd
	current.OffsetEnd = patch.OffsetEnd
	state.Live[key] = current
	return state
}

func reconcilePending(state State) State {
	for _, message := range state.Messages {
		delete(state.Pending, message.ID)
		if message.OperationID != "" {
			for id, pending := range state.Pending {
				if pending.OperationID == message.OperationID {
					delete(state.Pending, id)
				}
			}
		}
	}
	return state
}

func applyReasoningEvent(state State, event client.SessionV3Event, payload map[string]json.RawMessage) State {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	switch eventType {
	case "session.reasoning.started", "session.reasoning.delta", "session.reasoning.completed", "session.reasoning.error", "session.reasoning.failed":
	default:
		return state
	}

	runID := rawString(payload, "run_id")
	step := int(rawInt64(payload, "step"))
	if step <= 0 {
		step = 1
	}
	stepID := firstNonEmpty(rawString(payload, "step_id"), fmt.Sprintf("step-%d", step))
	reasoningID := firstNonEmpty(rawString(payload, "reasoning_id"), rawString(payload, "reasoning_key"), fmt.Sprintf("reasoning-%d", step))
	key := strings.Join([]string{runID, stepID, reasoningID}, ":")
	segment := state.Reasoning[key]
	if segment.Key == "" {
		segment = ReasoningSegment{
			Key:          key,
			RunID:        runID,
			Step:         step,
			StepID:       stepID,
			ReasoningID:  reasoningID,
			ReasoningKey: firstNonEmpty(rawString(payload, "reasoning_key"), reasoningID),
			Status:       "running",
			GlobalSeq:    event.Seq,
			StartedAt:    firstPositiveInt64(rawInt64(payload, "recorded_at"), event.TsUnixMS),
		}
	}
	if segment.GlobalSeq == 0 {
		segment.GlobalSeq = event.Seq
	}
	recordedAt := firstPositiveInt64(rawInt64(payload, "recorded_at"), event.TsUnixMS)
	segment.UpdatedAt = maxInt64Value(segment.UpdatedAt, recordedAt)

	switch eventType {
	case "session.reasoning.delta":
		delta := rawText(payload, "delta")
		switch strings.ToLower(rawString(payload, "delta_mode")) {
		case "append":
			segment.Text += delta
		default:
			if delta != "" {
				segment.Text = delta
			}
		}
		segment.Status = "running"
	case "session.reasoning.completed":
		segment.Summary = rawText(payload, "summary")
		if segment.Summary == "" {
			segment.Summary = segment.Text
		}
		segment.Status = "done"
		segment.CompletedAt = recordedAt
	case "session.reasoning.error", "session.reasoning.failed":
		segment.Summary = firstNonEmpty(rawText(payload, "summary"), segment.Text)
		segment.Status = "error"
		segment.CompletedAt = recordedAt
	}
	state.Reasoning[key] = segment
	return state
}

func applyAssistantTimelineEvent(state State, event client.SessionV3Event, payload map[string]json.RawMessage) State {
	switch strings.ToLower(strings.TrimSpace(event.EventType)) {
	case "session.assistant.delta", "session.message.delta":
	default:
		return state
	}
	runID := rawString(payload, "run_id")
	streamID := rawString(payload, "stream_id")
	if runID == "" || streamID == "" {
		return state
	}
	key := liveKey(runID, streamID)
	segment := state.Live[key]
	segment.RunID = runID
	segment.StreamID = streamID
	segment.GlobalSeq = maxUint64(segment.GlobalSeq, event.Seq)
	segment.CreatedAt = firstPositiveInt64(segment.CreatedAt, event.TsUnixMS)
	state.Live[key] = segment
	return state
}

func reconcileDurableTools(state State) State {
	for _, message := range state.Messages {
		tool, ok := parseToolMessage(message)
		if !ok {
			continue
		}
		for key, live := range state.Tools {
			if (tool.CallID != "" && live.CallID == tool.CallID) || (tool.ToolInstanceID != "" && live.ToolInstanceID == tool.ToolInstanceID) {
				delete(state.Tools, key)
			}
		}
	}
	return state
}

func reconcileDurableLive(state State) State {
	for _, message := range state.Messages {
		if message.Role != "assistant" || message.RunID == "" {
			continue
		}
		for key, segment := range state.Live {
			if segment.RunID == message.RunID {
				delete(state.Live, key)
			}
		}
	}
	return state
}

func sessionFromClient(value client.SessionSummary, fallbackID string) Session {
	return Session{
		ID:            firstNonEmpty(value.ID, fallbackID),
		Title:         strings.TrimSpace(value.Title),
		WorkspacePath: strings.TrimSpace(value.WorkspacePath),
		WorkspaceName: strings.TrimSpace(value.WorkspaceName),
		Mode:          strings.TrimSpace(value.Mode),
		TargetSwarmID: metadataString(value.Metadata, "swarm_v3_runtime_swarm_id"),
		CreatedAt:     value.CreatedAt,
		UpdatedAt:     value.UpdatedAt,
	}
}

func mergeMessages(existing []Message, incoming []client.SessionMessage) []Message {
	byID := make(map[string]Message, len(existing)+len(incoming))
	for _, message := range existing {
		if message.ID != "" {
			byID[message.ID] = message
		}
	}
	for _, raw := range incoming {
		if strings.TrimSpace(raw.ID) == "" {
			continue
		}
		byID[raw.ID] = messageFromClient(raw)
	}
	out := make([]Message, 0, len(byID))
	for _, message := range byID {
		out = append(out, message)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GlobalSeq != out[j].GlobalSeq {
			if out[i].GlobalSeq == 0 {
				return false
			}
			if out[j].GlobalSeq == 0 {
				return true
			}
			return out[i].GlobalSeq < out[j].GlobalSeq
		}
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > maxResidentMessages {
		out = append([]Message(nil), out[len(out)-maxResidentMessages:]...)
	}
	return out
}

func messageFromClient(value client.SessionMessage) Message {
	metadata := cloneAnyMap(value.Metadata)
	return Message{
		ID:           strings.TrimSpace(value.ID),
		SessionID:    strings.TrimSpace(value.SessionID),
		GlobalSeq:    value.GlobalSeq,
		Role:         strings.TrimSpace(value.Role),
		Content:      value.Content,
		CreatedAt:    value.CreatedAt,
		RunID:        metadataString(metadata, "run_id"),
		OperationID:  firstNonEmpty(metadataString(metadata, "operation_id"), metadataString(metadata, "client_request_id")),
		Metadata:     metadata,
		FinalHandoff: finalHandoffFromMetadata(metadata),
	}
}

func finalHandoffFromMetadata(metadata map[string]any) *client.PlanFinalHandoff {
	value, ok := metadata["final_handoff"]
	if !ok || value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var handoff client.PlanFinalHandoff
	if json.Unmarshal(raw, &handoff) != nil || handoff.SchemaVersion != 1 || strings.TrimSpace(handoff.Title) == "" || strings.TrimSpace(handoff.Overview) == "" {
		return nil
	}
	handoff.Title = strings.TrimSpace(handoff.Title)
	handoff.Overview = strings.TrimSpace(handoff.Overview)
	handoff.ImpactBullets = cleanStrings(handoff.ImpactBullets, 3)
	handoff.SuggestedPrompts = cleanHandoffPrompts(handoff.SuggestedPrompts, 3)
	handoff.Details.ChangedFiles = cleanStrings(handoff.Details.ChangedFiles, 0)
	handoff.Details.Validation = cleanStrings(handoff.Details.Validation, 0)
	return &handoff
}

func cleanStrings(values []string, limit int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

func cleanHandoffPrompts(values []client.PlanFinalHandoffSuggestedPrompt, limit int) []client.PlanFinalHandoffSuggestedPrompt {
	out := make([]client.PlanFinalHandoffSuggestedPrompt, 0, len(values))
	for _, value := range values {
		value.Label = strings.TrimSpace(value.Label)
		value.Prompt = strings.TrimSpace(value.Prompt)
		if value.Label != "" && value.Prompt != "" {
			out = append(out, value)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func sortedEvents(events []client.SessionV3Event) []client.SessionV3Event {
	out := append([]client.SessionV3Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func cloneState(value State) State {
	out := value
	out.Messages = append([]Message(nil), value.Messages...)
	for index := range out.Messages {
		out.Messages[index].Metadata = cloneAnyMap(value.Messages[index].Metadata)
		if value.Messages[index].FinalHandoff != nil {
			handoff := *value.Messages[index].FinalHandoff
			handoff.ImpactBullets = append([]string(nil), handoff.ImpactBullets...)
			handoff.SuggestedPrompts = append([]client.PlanFinalHandoffSuggestedPrompt(nil), handoff.SuggestedPrompts...)
			handoff.Details.ChangedFiles = append([]string(nil), handoff.Details.ChangedFiles...)
			handoff.Details.Validation = append([]string(nil), handoff.Details.Validation...)
			out.Messages[index].FinalHandoff = &handoff
		}
	}
	out.Permissions.Records = append([]PermissionTimelineItem(nil), value.Permissions.Records...)
	out.Pending = make(map[string]PendingMessage, len(value.Pending))
	for key, pending := range value.Pending {
		out.Pending[key] = pending
	}
	out.Live = make(map[string]LiveSegment, len(value.Live))
	for key, segment := range value.Live {
		out.Live[key] = segment
	}
	out.Reasoning = make(map[string]ReasoningSegment, len(value.Reasoning))
	for key, segment := range value.Reasoning {
		out.Reasoning[key] = segment
	}
	out.Tools = make(map[string]ToolTimelineItem, len(value.Tools))
	for key, item := range value.Tools {
		if item.TaskStream != nil {
			stream := *item.TaskStream
			stream.LaunchOrder = append([]string(nil), item.TaskStream.LaunchOrder...)
			stream.LaunchesByKey = make(map[string]map[string]any, len(item.TaskStream.LaunchesByKey))
			for launchKey, launch := range item.TaskStream.LaunchesByKey {
				cloned := make(map[string]any, len(launch))
				for field, fieldValue := range launch {
					cloned[field] = fieldValue
				}
				stream.LaunchesByKey[launchKey] = cloned
			}
			item.TaskStream = &stream
		}
		out.Tools[key] = item
	}
	if value.CurrentRun != nil {
		run := *value.CurrentRun
		out.CurrentRun = &run
	}
	if value.LatestRun != nil {
		run := *value.LatestRun
		out.LatestRun = &run
	}
	if value.Plan.ActivePlan != nil {
		plan := cloneSessionPlan(*value.Plan.ActivePlan)
		out.Plan.ActivePlan = &plan
	}
	if value.RoutedDraft != nil {
		draft := *value.RoutedDraft
		draft.Metadata = cloneAnyMap(value.RoutedDraft.Metadata)
		out.RoutedDraft = &draft
	}
	return out
}

func NewState() State { return cloneState(State{Connection: ConnectionDisconnected}) }

func applyAgentModelPolicy(state *ModelState, preference client.ModelPreference, contextWindow, maxOutputTokens int, policy client.SessionV3AgentModelPolicy) {
	if state == nil {
		return
	}
	state.Preference = normalizeModelPreference(preference)
	state.ContextWindow = contextWindow
	state.MaxOutputTokens = maxOutputTokens
	state.Locked = policy.Locked
	state.LockReason = strings.TrimSpace(policy.Reason)
	state.ProfileName = strings.TrimSpace(policy.ProfileName)
	state.ProfileSource = strings.TrimSpace(policy.ProfileSource)
	state.ProfileMode = strings.TrimSpace(policy.ProfileMode)
	if state.Locked {
		if effective := normalizeModelPreference(policy.Preference); effective.Provider != "" || effective.Model != "" {
			state.Preference = effective
			state.ContextWindow = policy.ContextWindow
			state.MaxOutputTokens = policy.MaxOutputTokens
		}
	}
}

func planStateFromHydrated(hydrated client.SessionV3Hydrated) PlanState {
	state := PlanState{HasActivePlan: hydrated.HasActivePlan}
	if hydrated.ActivePlan != nil {
		plan := cloneSessionPlan(*hydrated.ActivePlan)
		state.ActivePlan = &plan
		state.HasActivePlan = true
	}
	return state
}

func planStateFromPayload(current PlanState, payload map[string]json.RawMessage) PlanState {
	next := current
	if raw := payload["has_active_plan"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &next.HasActivePlan)
	}
	if raw := payload["active_plan"]; len(raw) > 0 {
		if string(raw) == "null" {
			next.ActivePlan = nil
		} else {
			var plan client.SessionPlan
			if json.Unmarshal(raw, &plan) == nil {
				cloned := cloneSessionPlan(plan)
				next.ActivePlan = &cloned
				next.HasActivePlan = true
			}
		}
	}
	if !next.HasActivePlan {
		next.ActivePlan = nil
	}
	return next
}

func cloneSessionPlan(value client.SessionPlan) client.SessionPlan {
	out := value
	if value.Document != nil {
		document := *value.Document
		document.Checkpoints = append([]client.SessionPlanCheckpoint(nil), value.Document.Checkpoints...)
		for i := range document.Checkpoints {
			document.Checkpoints[i].Tasks = append([]string(nil), document.Checkpoints[i].Tasks...)
			document.Checkpoints[i].Subtasks = append([]client.SessionPlanSubtask(nil), document.Checkpoints[i].Subtasks...)
			document.Checkpoints[i].Attempts = append([]client.SessionPlanCheckpointAttempt(nil), document.Checkpoints[i].Attempts...)
		}
		out.Document = &document
	}
	return out
}

func usageStateFromSummary(summary *client.SessionUsageSummary) UsageState {
	if summary == nil {
		return UsageState{}
	}
	window := summary.ContextWindow
	remaining := summary.RemainingTokens
	if window <= 0 {
		return UsageState{}
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(window) {
		remaining = int64(window)
	}
	return UsageState{Available: true, ContextWindow: window, RemainingTokens: remaining, TotalTokens: summary.TotalTokens}
}

func normalizeModelPreference(value client.ModelPreference) client.ModelPreference {
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	value.Model = strings.TrimSpace(value.Model)
	value.Thinking = strings.TrimSpace(value.Thinking)
	value.ServiceTier = strings.TrimSpace(value.ServiceTier)
	value.ContextMode = strings.TrimSpace(value.ContextMode)
	return value
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func liveKey(runID, streamID string) string {
	return strings.TrimSpace(runID) + ":" + strings.TrimSpace(streamID)
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

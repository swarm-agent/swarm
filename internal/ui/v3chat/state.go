// Package v3chat owns the state boundary for the V3-native TUI chat.
//
// It intentionally depends only on the V3 client contract. It does not share
// state or rendering machinery with the legacy chat page.
package v3chat

import (
	"encoding/json"
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
	ID        string
	Title     string
	Mode      string
	CreatedAt int64
	UpdatedAt int64
}

type Message struct {
	ID          string
	SessionID   string
	GlobalSeq   uint64
	Role        string
	Content     string
	CreatedAt   int64
	RunID       string
	OperationID string
}

type PendingMessage struct {
	Message
	ClientRequestID string
}

type ActiveRun struct {
	ID     string
	Status string
}

type LiveSegment struct {
	RunID      string
	StreamID   string
	Text       string
	LiveSeqEnd uint64
	OffsetEnd  uint64
}

type State struct {
	Session        Session
	Messages       []Message
	Pending        map[string]PendingMessage
	ActiveRun      *ActiveRun
	Live           map[string]LiveSegment
	Connection     ConnectionStatus
	EndpointCursor string
	LastEventSeq   uint64
	StaleReason    string
	NeedsRehydrate bool
}

type Action interface{ isV3ChatAction() }

type HydrateAction struct{ Snapshot client.SessionV3Hydrated }

func (HydrateAction) isV3ChatAction() {}

type PendingUserAction struct{ Pending PendingMessage }

func (PendingUserAction) isV3ChatAction() {}

type MessageResultAction struct{ Result client.SessionV3MessageResult }

func (MessageResultAction) isV3ChatAction() {}

type RealtimeFrameAction struct{ Frame client.V3RealtimeFrame }

func (RealtimeFrameAction) isV3ChatAction() {}

type ConnectionAction struct {
	Status ConnectionStatus
	Reason string
}

func (ConnectionAction) isV3ChatAction() {}

// Reduce returns a detached next state. Maps and slices are copied before they
// are changed so render selectors can safely retain an older snapshot.
func Reduce(current State, action Action) State {
	next := cloneState(current)
	switch value := action.(type) {
	case HydrateAction:
		next = reduceHydrated(next, value.Snapshot)
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
	}
	return next
}

func reduceHydrated(state State, hydrated client.SessionV3Hydrated) State {
	state.Session = sessionFromClient(hydrated.Session, hydrated.Projection.SessionID)
	state.Messages = mergeMessages(nil, hydrated.Messages)
	state.LastEventSeq = 0
	state.EndpointCursor = strings.TrimSpace(hydrated.SnapshotEndpointCursor)
	if hydrated.ActiveRunIntent != nil {
		state.ActiveRun = &ActiveRun{ID: strings.TrimSpace(hydrated.ActiveRunIntent.RunID), Status: strings.TrimSpace(hydrated.ActiveRunIntent.Status)}
	} else {
		state.ActiveRun = nil
	}
	for _, event := range sortedEvents(hydrated.Events) {
		state = applyEvent(state, event)
	}
	if hydrated.Projection.LastEventSeq > state.LastEventSeq {
		state.LastEventSeq = hydrated.Projection.LastEventSeq
	}
	state = reconcilePending(state)
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
		state.ActiveRun = &ActiveRun{ID: strings.TrimSpace(result.RunIntent.RunID), Status: strings.TrimSpace(result.RunIntent.Status)}
	}
	if result.RealtimeOutbox != nil && strings.TrimSpace(result.RealtimeOutbox.EndpointCursor) != "" {
		state.EndpointCursor = strings.TrimSpace(result.RealtimeOutbox.EndpointCursor)
	}
	state = reconcilePending(state)
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
	if raw := payload["message"]; len(raw) > 0 {
		var message client.SessionMessage
		if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message.ID) != "" {
			state.Messages = mergeMessages(state.Messages, []client.SessionMessage{message})
		}
	}
	if raw := payload["run_intent"]; len(raw) > 0 {
		var run client.SessionV3RunIntent
		if json.Unmarshal(raw, &run) == nil && strings.TrimSpace(run.RunID) != "" {
			state.ActiveRun = &ActiveRun{ID: strings.TrimSpace(run.RunID), Status: strings.TrimSpace(run.Status)}
		}
	}
	if event.Seq > state.LastEventSeq {
		state.LastEventSeq = event.Seq
	}
	return state
}

func applyLivePatch(state State, patch *client.V3RealtimeLivePatch) State {
	if patch == nil || strings.TrimSpace(patch.StreamID) == "" {
		return state
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
	return Session{ID: firstNonEmpty(value.ID, fallbackID), Title: strings.TrimSpace(value.Title), Mode: strings.TrimSpace(value.Mode), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
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
	return Message{ID: strings.TrimSpace(value.ID), SessionID: strings.TrimSpace(value.SessionID), GlobalSeq: value.GlobalSeq, Role: strings.TrimSpace(value.Role), Content: value.Content, CreatedAt: value.CreatedAt, RunID: metadataString(value.Metadata, "run_id"), OperationID: firstNonEmpty(metadataString(value.Metadata, "operation_id"), metadataString(value.Metadata, "client_request_id"))}
}

func sortedEvents(events []client.SessionV3Event) []client.SessionV3Event {
	out := append([]client.SessionV3Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func cloneState(value State) State {
	out := value
	out.Messages = append([]Message(nil), value.Messages...)
	out.Pending = make(map[string]PendingMessage, len(value.Pending))
	for key, pending := range value.Pending {
		out.Pending[key] = pending
	}
	out.Live = make(map[string]LiveSegment, len(value.Live))
	for key, segment := range value.Live {
		out.Live[key] = segment
	}
	if value.ActiveRun != nil {
		run := *value.ActiveRun
		out.ActiveRun = &run
	}
	return out
}

func NewState() State { return cloneState(State{Connection: ConnectionDisconnected}) }

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

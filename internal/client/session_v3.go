package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	v3RealtimeStreamPath                   = "/v3/realtime/stream"
	v3RealtimeProtocol                     = "v3.realtime"
	v3RealtimeProtocolVersion              = 1
	v3RealtimeKindHello                    = "hello"
	v3RealtimeKindEvent                    = "event"
	v3RealtimeKindReplayStart              = "replay.started"
	v3RealtimeKindReplayDone               = "replay.complete"
	v3RealtimeKindCursorError              = "cursor.error"
	v3RealtimeKindKeepalive                = "keepalive"
	v3RealtimeKindEndpointWatermark        = "endpoint.watermark"
	v3RealtimeKindHighWater                = "projection.high_watermark"
	v3RealtimeKindSubscribe                = "subscribe.session"
	v3RealtimeKindUnsubscribe              = "unsubscribe.session"
	v3RealtimeKindResume                   = "resume"
	v3RealtimeKindWorksetSessionDiscovered = "workset.session.discovered"
	v3RealtimeKindWorksetSessionUpdated    = "workset.session.updated"
	v3RealtimeKindWorksetSessionRemoved    = "workset.session.removed"
	v3RealtimeKindAuthDenied               = "auth.denied"
	v3RealtimeKindSlowConsumer             = "slow_consumer.reconnect_required"
	v3RealtimeKindLivePatch                = "live.patch"
	V3RealtimeCapabilityLivePatchV1        = "live_patch_v1"
)

type V3RealtimeSubscription struct {
	SessionID      string `json:"session_id"`
	EndpointCursor string `json:"endpoint_cursor,omitempty"`
	LastSeq        uint64 `json:"last_seq,omitempty"`
	SubscriptionID string `json:"subscription_id"`
}

type V3RealtimeWorksetSubscription struct {
	WorksetID             string                    `json:"workset_id"`
	SubscriptionID        string                    `json:"subscription_id"`
	Surface               string                    `json:"surface,omitempty"`
	Selector              V3RealtimeWorksetSelector `json:"selector"`
	Resources             []string                  `json:"resources,omitempty"`
	AutoSubscribeSessions bool                      `json:"auto_subscribe_sessions"`
}

type V3RealtimeWorksetSelector struct {
	Kind           string                 `json:"kind,omitempty"`
	Global         bool                   `json:"global,omitempty"`
	WorkspacePath  string                 `json:"workspace_path,omitempty"`
	WorkspacePaths []string               `json:"workspace_paths,omitempty"`
	SessionIDs     []string               `json:"session_ids,omitempty"`
	Recent         SessionV3WorksetRecent `json:"recent,omitempty"`
}

type V3RealtimeResumeOptions struct {
	EndpointCursor string
	Surface        string
	Subscriptions  []V3RealtimeSubscription
	Worksets       []V3RealtimeWorksetSubscription
	Capabilities   []string
	StartAtCurrent bool
	OnResumeSent   func()
}

type V3RealtimeLivePatch struct {
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	StreamID     string `json:"stream_id"`
	StreamKind   string `json:"stream_kind"`
	Operation    string `json:"operation"`
	Step         int    `json:"step"`
	StepID       string `json:"step_id"`
	LiveSeqStart uint64 `json:"live_seq_start"`
	LiveSeqEnd   uint64 `json:"live_seq_end"`
	OffsetStart  uint64 `json:"offset_start"`
	OffsetEnd    uint64 `json:"offset_end"`
	Text         string `json:"text"`
	RecordedAt   int64  `json:"recorded_at"`
}

type V3RealtimeFrame struct {
	Protocol              string                          `json:"protocol"`
	ProtocolVersion       int                             `json:"protocol_version"`
	Kind                  string                          `json:"kind"`
	SessionID             string                          `json:"session_id,omitempty"`
	SubscriptionID        string                          `json:"subscription_id,omitempty"`
	WorksetID             string                          `json:"workset_id,omitempty"`
	WorksetSubscriptionID string                          `json:"workset_subscription_id,omitempty"`
	AutoSubscribed        bool                            `json:"auto_subscribed,omitempty"`
	AfterSeq              uint64                          `json:"after_seq,omitempty"`
	AfterRev              uint64                          `json:"afterRev,omitempty"`
	LastSeq               uint64                          `json:"last_seq,omitempty"`
	NextSeq               uint64                          `json:"next_seq,omitempty"`
	HighWatermarkSeq      uint64                          `json:"high_watermark_seq,omitempty"`
	EndpointCursor        string                          `json:"endpoint_cursor,omitempty"`
	Subscriptions         []V3RealtimeSubscription        `json:"subscriptions,omitempty"`
	Worksets              []V3RealtimeWorksetSubscription `json:"worksets,omitempty"`
	Capabilities          []string                        `json:"capabilities,omitempty"`
	Rev                   uint64                          `json:"rev,omitempty"`
	PrevRev               uint64                          `json:"prevRev"`
	EventType             string                          `json:"event_type,omitempty"`
	Event                 *SessionV3Event                 `json:"event,omitempty"`
	Projection            *SessionV3Projection            `json:"projection,omitempty"`
	Session               *SessionSummary                 `json:"session,omitempty"`
	ErrorCode             string                          `json:"error_code,omitempty"`
	Error                 string                          `json:"error,omitempty"`
	Reason                string                          `json:"reason,omitempty"`
	Live                  *V3RealtimeLivePatch            `json:"live,omitempty"`
}

func (f V3RealtimeFrame) Err() error {
	msg := strings.TrimSpace(f.Error)
	if msg == "" {
		msg = strings.TrimSpace(f.Reason)
	}
	if msg == "" {
		msg = strings.TrimSpace(f.ErrorCode)
	}
	if msg == "" {
		msg = strings.TrimSpace(f.Kind)
	}
	if msg == "" {
		msg = "v3 realtime stream failed"
	}
	return errors.New(msg)
}

func (c *API) StreamSessionsV3Realtime(ctx context.Context, subscriptions []V3RealtimeSubscription, onFrame func(V3RealtimeFrame)) error {
	return c.StreamV3Realtime(ctx, V3RealtimeResumeOptions{Subscriptions: subscriptions}, onFrame)
}

func (c *API) StreamV3Realtime(ctx context.Context, options V3RealtimeResumeOptions, onFrame func(V3RealtimeFrame)) error {
	if c == nil {
		return errors.New("api client is not configured")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	normalized, endpointCursor, err := normalizeV3RealtimeResumeOptions(options)
	if err != nil {
		return err
	}

	streamPath := v3RealtimeStreamPath
	query := url.Values{}
	if endpointCursor != "" {
		query.Set("endpoint_cursor", endpointCursor)
	}
	if surface := strings.TrimSpace(normalized.Surface); surface != "" {
		query.Set("surface", surface)
	}
	if len(query) > 0 {
		streamPath = streamPath + "?" + query.Encode()
	}
	baseURL, _, socketPath := c.requestTarget()
	conn, err := dialDaemonWS(ctx, baseURL, c.Token(), socketPath, streamPath, "")
	if err != nil {
		return fmt.Errorf("connect v3 realtime stream: %w", err)
	}
	defer conn.Close()

	lastSeqBySession := make(map[string]uint64, len(normalized.Subscriptions))
	unknownAutoSubscribedSeq := map[string]bool{}
	for _, sub := range normalized.Subscriptions {
		lastSeqBySession[sub.SessionID] = sub.LastSeq
	}
	if endpointCursor == "" {
		if err := startV3RealtimeAtCurrent(ctx, conn, normalized, onFrame); err != nil {
			return err
		}
	} else {
		resume := V3RealtimeFrame{
			Protocol:        v3RealtimeProtocol,
			ProtocolVersion: v3RealtimeProtocolVersion,
			Kind:            v3RealtimeKindResume,
			EndpointCursor:  endpointCursor,
			Subscriptions:   normalized.Subscriptions,
			Worksets:        normalized.Worksets,
			Capabilities:    normalized.Capabilities,
		}
		raw, err := json.Marshal(resume)
		if err != nil {
			return err
		}
		if err := conn.WriteText(raw); err != nil {
			return fmt.Errorf("send v3 realtime resume: %w", err)
		}
		if normalized.OnResumeSent != nil {
			normalized.OnResumeSent()
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		raw, readErr := conn.ReadText(ctx)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read v3 realtime stream: %w", readErr)
		}
		var frame V3RealtimeFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("decode v3 realtime frame: %w", err)
		}
		if err := validateV3RealtimeFrameProtocol(frame); err != nil {
			return err
		}
		kind := strings.ToLower(strings.TrimSpace(frame.Kind))
		deliver := true
		switch kind {
		case v3RealtimeKindEvent:
			if err := applyV3RealtimeSessionOrder(lastSeqBySession, unknownAutoSubscribedSeq, &frame, onFrame); err != nil {
				return err
			}
			if frame.Event == nil || frame.Event.Seq != frame.LastSeq {
				deliver = false
			}
		case v3RealtimeKindWorksetSessionDiscovered:
			if sessionID := strings.TrimSpace(frame.SessionID); sessionID != "" {
				lastSeqBySession[sessionID] = frame.LastSeq
				if frame.AutoSubscribed && frame.LastSeq == 0 {
					unknownAutoSubscribedSeq[sessionID] = true
				} else {
					delete(unknownAutoSubscribedSeq, sessionID)
				}
			}
		case v3RealtimeKindWorksetSessionUpdated:
			// Workset membership/resource frames update list state but do not advance
			// per-session event order. Subsequent event frames remain the session-local
			// ordering authority for direct or auto-subscribed session streams.
		case v3RealtimeKindWorksetSessionRemoved:
			if sessionID := strings.TrimSpace(frame.SessionID); sessionID != "" {
				delete(lastSeqBySession, sessionID)
				delete(unknownAutoSubscribedSeq, sessionID)
			}
		case v3RealtimeKindLivePatch:
			if err := validateV3RealtimeLivePatch(frame); err != nil {
				return err
			}
		case v3RealtimeKindCursorError, v3RealtimeKindAuthDenied:
			// Per-session recovery state belongs to the named session; keep the shared
			// connection alive so other sessions can continue to receive events.
		case v3RealtimeKindSlowConsumer:
			if onFrame != nil {
				onFrame(frame)
			}
			return frame.Err()
		case v3RealtimeKindReplayStart, v3RealtimeKindReplayDone:
			// Replay control frames can carry the session-local sequence boundary that
			// corresponds to the endpoint cursor. The endpoint cursor remains the only
			// transport resume token; event.seq is used only for idempotent reduction.
			if frame.SessionID != "" && frame.LastSeq > lastSeqBySession[frame.SessionID] {
				lastSeqBySession[frame.SessionID] = frame.LastSeq
			}
		case v3RealtimeKindHello, v3RealtimeKindKeepalive, v3RealtimeKindEndpointWatermark, v3RealtimeKindHighWater:
			// Control frames are delivered to the caller, but they do not advance
			// application order. Only event.seq advances session state.
		default:
			return fmt.Errorf("unsupported v3 realtime kind %q", frame.Kind)
		}
		if deliver && onFrame != nil {
			onFrame(frame)
		}
	}
}

// startV3RealtimeAtCurrent is reserved for a newly committed session whose
// create response has no snapshot cursor. The cursorless websocket handshake
// establishes the daemon's current durable outbox head; only then is the
// session subscribed at that signed cursor. Resume and recovery callers remain
// cursor-bound through normalizeV3RealtimeResumeOptions.
func startV3RealtimeAtCurrent(ctx context.Context, conn *wsClientConn, options V3RealtimeResumeOptions, onFrame func(V3RealtimeFrame)) error {
	raw, err := conn.ReadText(ctx)
	if err != nil {
		return fmt.Errorf("read v3 realtime hello: %w", err)
	}
	var hello V3RealtimeFrame
	if err := json.Unmarshal(raw, &hello); err != nil {
		return fmt.Errorf("decode v3 realtime hello: %w", err)
	}
	if err := validateV3RealtimeFrameProtocol(hello); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(hello.Kind)) != v3RealtimeKindHello {
		return fmt.Errorf("v3 realtime start-at-current expected hello, got %q", hello.Kind)
	}
	cursor := strings.TrimSpace(hello.EndpointCursor)
	if cursor == "" {
		return errors.New("v3 realtime start-at-current hello missing endpoint cursor")
	}
	if onFrame != nil {
		onFrame(hello)
	}
	resume := V3RealtimeFrame{
		Protocol:        v3RealtimeProtocol,
		ProtocolVersion: v3RealtimeProtocolVersion,
		Kind:            v3RealtimeKindResume,
		EndpointCursor:  cursor,
		Capabilities:    options.Capabilities,
	}
	rawResume, err := json.Marshal(resume)
	if err != nil {
		return err
	}
	if err := conn.WriteText(rawResume); err != nil {
		return fmt.Errorf("send v3 realtime current-head resume: %w", err)
	}
	for _, sub := range options.Subscriptions {
		subscribe := V3RealtimeFrame{
			Protocol:        v3RealtimeProtocol,
			ProtocolVersion: v3RealtimeProtocolVersion,
			Kind:            v3RealtimeKindSubscribe,
			SessionID:       sub.SessionID,
			SubscriptionID:  sub.SubscriptionID,
			EndpointCursor:  cursor,
		}
		rawSubscribe, err := json.Marshal(subscribe)
		if err != nil {
			return err
		}
		if err := conn.WriteText(rawSubscribe); err != nil {
			return fmt.Errorf("send v3 realtime current-head subscription: %w", err)
		}
	}
	if options.OnResumeSent != nil {
		options.OnResumeSent()
	}
	return nil
}

func normalizeV3RealtimeResumeOptions(options V3RealtimeResumeOptions) (V3RealtimeResumeOptions, string, error) {
	cursor := strings.TrimSpace(options.EndpointCursor)
	normalized := V3RealtimeResumeOptions{
		EndpointCursor: cursor,
		Surface:        strings.TrimSpace(options.Surface),
		Subscriptions:  make([]V3RealtimeSubscription, 0, len(options.Subscriptions)),
		Worksets:       make([]V3RealtimeWorksetSubscription, 0, len(options.Worksets)),
		Capabilities:   normalizeV3RealtimeCapabilities(options.Capabilities),
		StartAtCurrent: options.StartAtCurrent,
		OnResumeSent:   options.OnResumeSent,
	}
	seenSessions := make(map[string]struct{}, len(options.Subscriptions))
	for _, sub := range options.Subscriptions {
		sessionID := strings.TrimSpace(sub.SessionID)
		if sessionID == "" {
			return V3RealtimeResumeOptions{}, "", errors.New("session id is required")
		}
		if _, ok := seenSessions[sessionID]; ok {
			return V3RealtimeResumeOptions{}, "", fmt.Errorf("duplicate v3 realtime session subscription %q", sessionID)
		}
		seenSessions[sessionID] = struct{}{}
		sub.SessionID = sessionID
		sub.EndpointCursor = strings.TrimSpace(sub.EndpointCursor)
		if cursor == "" && sub.EndpointCursor != "" {
			cursor = sub.EndpointCursor
		}
		if strings.TrimSpace(sub.SubscriptionID) == "" {
			sub.SubscriptionID = fmt.Sprintf("tui-%s-%d", sessionID, time.Now().UnixNano())
		} else {
			sub.SubscriptionID = strings.TrimSpace(sub.SubscriptionID)
		}
		normalized.Subscriptions = append(normalized.Subscriptions, sub)
	}
	for i := range normalized.Subscriptions {
		if cursor != "" {
			normalized.Subscriptions[i].EndpointCursor = cursor
		}
	}
	seenWorksets := make(map[string]struct{}, len(options.Worksets))
	for _, workset := range options.Worksets {
		worksetID := strings.TrimSpace(workset.WorksetID)
		if worksetID == "" {
			return V3RealtimeResumeOptions{}, "", errors.New("v3 realtime workset_id is required")
		}
		if _, ok := seenWorksets[worksetID]; ok {
			return V3RealtimeResumeOptions{}, "", fmt.Errorf("duplicate v3 realtime workset subscription %q", worksetID)
		}
		seenWorksets[worksetID] = struct{}{}
		workset.WorksetID = worksetID
		workset.SubscriptionID = strings.TrimSpace(workset.SubscriptionID)
		if workset.SubscriptionID == "" {
			return V3RealtimeResumeOptions{}, "", fmt.Errorf("v3 realtime workset %q requires subscription_id", worksetID)
		}
		workset.Surface = strings.TrimSpace(workset.Surface)
		workset.Selector.Kind = strings.TrimSpace(workset.Selector.Kind)
		workset.Selector.WorkspacePath = strings.TrimSpace(workset.Selector.WorkspacePath)
		workset.Selector.WorkspacePaths = trimNonEmptyStrings(workset.Selector.WorkspacePaths)
		workset.Selector.SessionIDs = trimNonEmptyStrings(workset.Selector.SessionIDs)
		workset.Selector.Recent.BeforeSessionID = strings.TrimSpace(workset.Selector.Recent.BeforeSessionID)
		workset.Resources = trimNonEmptyStrings(workset.Resources)
		normalized.Worksets = append(normalized.Worksets, workset)
	}
	if cursor == "" && !normalized.StartAtCurrent {
		return V3RealtimeResumeOptions{}, "", errors.New("v3 realtime endpoint cursor is required")
	}
	if cursor == "" && len(normalized.Worksets) != 0 {
		return V3RealtimeResumeOptions{}, "", errors.New("v3 realtime start-at-current supports session subscriptions only")
	}
	if len(normalized.Subscriptions) == 0 && len(normalized.Worksets) == 0 {
		return V3RealtimeResumeOptions{}, "", errors.New("at least one v3 realtime session or workset subscription is required")
	}
	normalized.EndpointCursor = cursor
	return normalized, cursor, nil
}

func trimNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeV3RealtimeCapabilities(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateV3RealtimeLivePatch(frame V3RealtimeFrame) error {
	if frame.Live == nil {
		return errors.New("v3 realtime live.patch missing live payload")
	}
	live := frame.Live
	if strings.TrimSpace(frame.SessionID) == "" || strings.TrimSpace(frame.SessionID) != strings.TrimSpace(live.SessionID) {
		return errors.New("v3 realtime live.patch session_id must match live.session_id")
	}
	if strings.TrimSpace(live.RunID) == "" || strings.TrimSpace(live.StreamID) == "" {
		return errors.New("v3 realtime live.patch requires run_id and stream_id")
	}
	switch strings.TrimSpace(live.StreamKind) {
	case "assistant_text", "provider_tool_call":
	default:
		return errors.New("v3 realtime live.patch stream_kind must be assistant_text or provider_tool_call")
	}
	if strings.TrimSpace(live.Operation) != "append" {
		return errors.New("v3 realtime live.patch requires append operation")
	}
	if live.Text == "" || live.LiveSeqStart == 0 || live.LiveSeqEnd < live.LiveSeqStart || live.OffsetEnd-live.OffsetStart != uint64(len([]byte(live.Text))) {
		return errors.New("v3 realtime live.patch has invalid text sequence or offsets")
	}
	return nil
}

func validateV3RealtimeFrameProtocol(frame V3RealtimeFrame) error {
	if frame.Protocol != v3RealtimeProtocol {
		return fmt.Errorf("v3 realtime protocol must be %q", v3RealtimeProtocol)
	}
	if frame.ProtocolVersion != v3RealtimeProtocolVersion {
		return fmt.Errorf("v3 realtime protocol_version must be %d", v3RealtimeProtocolVersion)
	}
	if strings.TrimSpace(frame.Kind) == "" {
		return errors.New("v3 realtime kind is required")
	}
	return nil
}

func applyV3RealtimeSessionOrder(lastSeqBySession map[string]uint64, unknownAutoSubscribedSeq map[string]bool, frame *V3RealtimeFrame, onFrame func(V3RealtimeFrame)) error {
	if frame == nil || frame.Event == nil {
		return errors.New("v3 realtime event frame missing event")
	}
	sessionID := strings.TrimSpace(frame.SessionID)
	if sessionID == "" {
		return errors.New("v3 realtime event missing session_id")
	}
	if strings.TrimSpace(frame.Event.SessionID) == "" {
		return errors.New("v3 realtime event payload missing session_id")
	}
	if frame.Event.SessionID != sessionID {
		return errors.New("v3 realtime event session_id conflicts with payload session_id")
	}
	if frame.Rev == 0 {
		return errors.New("v3 realtime event missing rev")
	}
	if frame.Rev != frame.PrevRev+1 {
		return errors.New("v3 realtime event requires continuous rev/prevRev")
	}
	if frame.Event.Seq == 0 {
		return errors.New("v3 realtime event missing event.seq")
	}
	lastSeq, ok := lastSeqBySession[sessionID]
	if !ok {
		return fmt.Errorf("v3 realtime event for unsubscribed session %q", sessionID)
	}
	if frame.Event.Seq <= lastSeq && !unknownAutoSubscribedSeq[sessionID] {
		frame.Event = nil
		return nil
	}
	if unknownAutoSubscribedSeq[sessionID] {
		delete(unknownAutoSubscribedSeq, sessionID)
		lastSeqBySession[sessionID] = frame.Event.Seq
		frame.LastSeq = frame.Event.Seq
		if strings.TrimSpace(frame.EventType) == "" {
			frame.EventType = frame.Event.EventType
		}
		return nil
	}
	// Session event sequences are authoritative ordering keys, not a promise that
	// every subscriber receives every intermediate event. A realtime subscription
	// can intentionally omit event classes, so accepted events may have sparse seqs.
	// The endpoint cursor and explicit cursor.error/session_cursor_gap frames are
	// the transport authorities for detecting lost delivery.
	lastSeqBySession[sessionID] = frame.Event.Seq
	frame.LastSeq = frame.Event.Seq
	if strings.TrimSpace(frame.EventType) == "" {
		frame.EventType = frame.Event.EventType
	}
	return nil
}

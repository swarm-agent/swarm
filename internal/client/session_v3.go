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
	v3RealtimeStreamPath            = "/v3/realtime/stream"
	v3RealtimeProtocol              = "v3.realtime"
	v3RealtimeProtocolVersion       = 1
	v3RealtimeKindHello             = "hello"
	v3RealtimeKindEvent             = "event"
	v3RealtimeKindReplayStart       = "replay.started"
	v3RealtimeKindReplayDone        = "replay.complete"
	v3RealtimeKindCursorError       = "cursor.error"
	v3RealtimeKindKeepalive         = "keepalive"
	v3RealtimeKindEndpointWatermark = "endpoint.watermark"
	v3RealtimeKindHighWater         = "projection.high_watermark"
	v3RealtimeKindSubscribe         = "subscribe.session"
	v3RealtimeKindUnsubscribe       = "unsubscribe.session"
	v3RealtimeKindResume            = "resume"
	v3RealtimeKindAuthDenied        = "auth.denied"
	v3RealtimeKindSlowConsumer      = "slow_consumer.reconnect_required"
)

type V3RealtimeSubscription struct {
	SessionID      string
	EndpointCursor string
	LastSeq        uint64
	SubscriptionID string
}

type V3RealtimeFrame struct {
	Protocol         string               `json:"protocol"`
	ProtocolVersion  int                  `json:"protocol_version"`
	Kind             string               `json:"kind"`
	SessionID        string               `json:"session_id,omitempty"`
	SubscriptionID   string               `json:"subscription_id,omitempty"`
	AfterSeq         uint64               `json:"after_seq,omitempty"`
	AfterRev         uint64               `json:"afterRev,omitempty"`
	LastSeq          uint64               `json:"last_seq,omitempty"`
	NextSeq          uint64               `json:"next_seq,omitempty"`
	HighWatermarkSeq uint64               `json:"high_watermark_seq,omitempty"`
	EndpointCursor   string               `json:"endpoint_cursor,omitempty"`
	Rev              uint64               `json:"rev,omitempty"`
	PrevRev          uint64               `json:"prevRev"`
	EventType        string               `json:"event_type,omitempty"`
	Event            *SessionV3Event      `json:"event,omitempty"`
	Projection       *SessionV3Projection `json:"projection,omitempty"`
	ErrorCode        string               `json:"error_code,omitempty"`
	Error            string               `json:"error,omitempty"`
	Reason           string               `json:"reason,omitempty"`
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
	if c == nil {
		return errors.New("api client is not configured")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	normalized := make([]V3RealtimeSubscription, 0, len(subscriptions))
	seen := make(map[string]struct{}, len(subscriptions))
	for _, sub := range subscriptions {
		sessionID := strings.TrimSpace(sub.SessionID)
		if sessionID == "" {
			return errors.New("session id is required")
		}
		if _, ok := seen[sessionID]; ok {
			return fmt.Errorf("duplicate v3 realtime session subscription %q", sessionID)
		}
		seen[sessionID] = struct{}{}
		sub.SessionID = sessionID
		if strings.TrimSpace(sub.SubscriptionID) == "" {
			sub.SubscriptionID = fmt.Sprintf("tui-%s-%d", sessionID, time.Now().UnixNano())
		}
		normalized = append(normalized, sub)
	}
	if len(normalized) == 0 {
		return errors.New("at least one v3 realtime session subscription is required")
	}

	streamPath := v3RealtimeStreamPath
	if len(normalized) == 1 && strings.TrimSpace(normalized[0].EndpointCursor) != "" {
		streamPath = streamPath + "?endpoint_cursor=" + url.QueryEscape(strings.TrimSpace(normalized[0].EndpointCursor))
	}
	baseURL, _, socketPath := c.requestTarget()
	conn, err := dialDaemonWS(ctx, baseURL, c.Token(), socketPath, streamPath, "")
	if err != nil {
		return fmt.Errorf("connect v3 realtime stream: %w", err)
	}
	defer conn.Close()

	lastSeqBySession := make(map[string]uint64, len(normalized))
	for _, sub := range normalized {
		lastSeqBySession[sub.SessionID] = sub.LastSeq
		msg := V3RealtimeFrame{
			Protocol:        v3RealtimeProtocol,
			ProtocolVersion: v3RealtimeProtocolVersion,
			Kind:            v3RealtimeKindSubscribe,
			SessionID:       sub.SessionID,
			SubscriptionID:  strings.TrimSpace(sub.SubscriptionID),
			EndpointCursor:  strings.TrimSpace(sub.EndpointCursor),
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if err := conn.WriteText(raw); err != nil {
			return fmt.Errorf("send v3 realtime subscribe: %w", err)
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
			if err := applyV3RealtimeSessionOrder(lastSeqBySession, &frame, onFrame); err != nil {
				return err
			}
			if frame.Event == nil || frame.Event.Seq != frame.LastSeq {
				deliver = false
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

func applyV3RealtimeSessionOrder(lastSeqBySession map[string]uint64, frame *V3RealtimeFrame, onFrame func(V3RealtimeFrame)) error {
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
	if frame.Event.Seq <= lastSeq {
		frame.Event = nil
		return nil
	}
	if frame.Event.Seq != lastSeq+1 {
		if onFrame != nil {
			onFrame(V3RealtimeFrame{
				Protocol:         v3RealtimeProtocol,
				ProtocolVersion:  v3RealtimeProtocolVersion,
				Kind:             v3RealtimeKindCursorError,
				SessionID:        sessionID,
				Rev:              frame.Rev,
				PrevRev:          frame.PrevRev,
				LastSeq:          lastSeq,
				NextSeq:          frame.Event.Seq,
				HighWatermarkSeq: frame.HighWatermarkSeq,
				ErrorCode:        "session_cursor_gap",
				Error:            fmt.Sprintf("session event sequence gap at %d, want %d; refetch required", frame.Event.Seq, lastSeq+1),
			})
		}
		frame.Event = nil
		return nil
	}
	lastSeqBySession[sessionID] = frame.Event.Seq
	frame.LastSeq = frame.Event.Seq
	if strings.TrimSpace(frame.EventType) == "" {
		frame.EventType = frame.Event.EventType
	}
	return nil
}

type SessionV3StreamFrame struct {
	Type             string              `json:"type"`
	OK               bool                `json:"ok,omitempty"`
	SessionID        string              `json:"session_id,omitempty"`
	AfterSeq         uint64              `json:"after_seq,omitempty"`
	LastSeq          uint64              `json:"last_seq,omitempty"`
	HighWatermarkSeq uint64              `json:"high_watermark_seq,omitempty"`
	NextSeq          uint64              `json:"next_seq,omitempty"`
	Error            string              `json:"error,omitempty"`
	Event            *SessionV3Event     `json:"event,omitempty"`
	Projection       SessionV3Projection `json:"projection,omitempty"`
}

func (f SessionV3StreamFrame) Err() error {
	msg := strings.TrimSpace(f.Error)
	if msg == "" {
		msg = strings.TrimSpace(f.Type)
	}
	if msg == "" {
		msg = "sessions v3 stream failed"
	}
	return errors.New(msg)
}

func (c *API) StreamSessionV3(ctx context.Context, sessionID string, afterSeq uint64, onFrame func(SessionV3StreamFrame)) error {
	return c.streamSessionV3WebSocket(ctx, sessionID, afterSeq, onFrame)
}

func (c *API) StreamSessionV3Replay(ctx context.Context, sessionID string, afterSeq uint64, onFrame func(SessionV3StreamFrame)) error {
	return c.streamSessionV3Replay(ctx, sessionID, afterSeq, onFrame)
}

func (c *API) streamSessionV3Replay(ctx context.Context, sessionID string, afterSeq uint64, onFrame func(SessionV3StreamFrame)) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	lastSeq := afterSeq
	for {
		replay, err := c.ReplaySessionV3Events(ctx, sessionID, lastSeq, 500)
		if err != nil {
			return err
		}
		if onFrame != nil {
			onFrame(SessionV3StreamFrame{Type: "replay.started", OK: true, SessionID: sessionID, LastSeq: lastSeq, HighWatermarkSeq: replay.HighWatermarkSeq, NextSeq: replay.NextSeq, Projection: replay.Projection})
		}
		for _, event := range replay.Events {
			if event.Seq <= lastSeq {
				continue
			}
			if event.Seq != lastSeq+1 {
				return fmt.Errorf("sessions v3 replay gap at %d, want %d; refetch required", event.Seq, lastSeq+1)
			}
			lastSeq = event.Seq
			frame := SessionV3StreamFrame{Type: "event", OK: true, SessionID: sessionID, LastSeq: lastSeq, HighWatermarkSeq: replay.HighWatermarkSeq, Event: &event, Projection: replay.Projection}
			if onFrame != nil {
				onFrame(frame)
			}
		}
		if onFrame != nil {
			onFrame(SessionV3StreamFrame{Type: "replay.complete", OK: true, SessionID: sessionID, LastSeq: lastSeq, HighWatermarkSeq: replay.HighWatermarkSeq, NextSeq: replay.NextSeq, Projection: replay.Projection})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c *API) streamSessionV3WebSocket(ctx context.Context, sessionID string, afterSeq uint64, onFrame func(SessionV3StreamFrame)) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	baseURL, _, socketPath := c.requestTarget()
	conn, err := dialDaemonWS(ctx, baseURL, c.Token(), socketPath, sessionV3PrimaryPath(sessionID, "stream"), "")
	if err != nil {
		return fmt.Errorf("connect sessions v3 stream: %w", err)
	}
	defer conn.Close()

	hello, err := json.Marshal(map[string]any{"type": "session.stream.hello", "after_seq": afterSeq})
	if err != nil {
		return err
	}
	if err := conn.WriteText(hello); err != nil {
		return fmt.Errorf("send sessions v3 stream hello: %w", err)
	}

	lastSeq := afterSeq
	for {
		raw, readErr := conn.ReadText(ctx)
		if readErr != nil {
			if ctx != nil && ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read sessions v3 stream: %w", readErr)
		}
		var frame SessionV3StreamFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("decode sessions v3 stream frame: %w", err)
		}
		if strings.TrimSpace(frame.SessionID) == "" {
			frame.SessionID = sessionID
		}
		frameType := strings.ToLower(strings.TrimSpace(frame.Type))
		switch frameType {
		case "cursor.error", "error":
			if onFrame != nil {
				onFrame(frame)
			}
			continue
		case "event":
			if frame.Event == nil {
				return errors.New("sessions v3 stream event frame missing event")
			}
			if frame.Event.Seq <= lastSeq {
				continue
			}
			if frame.Event.Seq != lastSeq+1 {
				return fmt.Errorf("sessions v3 stream gap at %d, want %d; refetch required", frame.Event.Seq, lastSeq+1)
			}
			lastSeq = frame.Event.Seq
			frame.LastSeq = lastSeq
		}
		if onFrame != nil {
			onFrame(frame)
		}
	}
}

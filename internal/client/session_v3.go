package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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
			return frame.Err()
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

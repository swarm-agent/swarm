package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	runStreamReplayLimit       = 2000
	runStreamReplayTTL         = 15 * time.Minute
	runStreamSendQueueSize     = runStreamReplayLimit + 64
	runStreamKeepaliveInterval = 15 * time.Second
	runStreamRunIDPrefix       = "run"
	runStreamSubscriberPref    = "sub"
)

var errRunStreamNotFound = errors.New("run stream not found")

type runStreamResumeGapError struct {
	RunID       string
	LastSeq     uint64
	EarliestSeq uint64
}

func (e *runStreamResumeGapError) Error() string {
	if e == nil {
		return "run stream resume gap"
	}
	return fmt.Sprintf("run stream resume cursor too old (run_id=%s last_seq=%d earliest_seq=%d)", e.RunID, e.LastSeq, e.EarliestSeq)
}

type runStreamInboundMessage struct {
	Type string `json:"type"`
	runruntime.RunRequest
	RunID   string `json:"run_id,omitempty"`
	LastSeq uint64 `json:"last_seq,omitempty"`
}

type runStreamControlMessage struct {
	Type        string `json:"type"`
	OK          bool   `json:"ok,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	LastSeq     uint64 `json:"last_seq,omitempty"`
	EarliestSeq uint64 `json:"earliest_seq,omitempty"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
}

type runStreamKeepaliveMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}

type runStreamWireEvent struct {
	Type         string                                `json:"type"`
	SessionID    string                                `json:"session_id,omitempty"`
	RunID        string                                `json:"run_id,omitempty"`
	Seq          uint64                                `json:"seq,omitempty"`
	Agent        string                                `json:"agent,omitempty"`
	Step         int                                   `json:"step,omitempty"`
	Delta        string                                `json:"delta,omitempty"`
	Summary      string                                `json:"summary,omitempty"`
	ToolName     string                                `json:"tool_name,omitempty"`
	CallID       string                                `json:"call_id,omitempty"`
	Arguments    string                                `json:"arguments,omitempty"`
	Output       string                                `json:"output,omitempty"`
	RawOutput    string                                `json:"raw_output,omitempty"`
	Error        string                                `json:"error,omitempty"`
	DurationMS   int64                                 `json:"duration_ms,omitempty"`
	Message      *pebblestore.MessageSnapshot          `json:"message,omitempty"`
	Permission   *pebblestore.PermissionRecord         `json:"permission,omitempty"`
	TurnUsage    *pebblestore.SessionTurnUsageSnapshot `json:"turn_usage,omitempty"`
	UsageSummary *pebblestore.SessionUsageSummary      `json:"usage_summary,omitempty"`
	Title        string                                `json:"title,omitempty"`
	TitleStage   string                                `json:"title_stage,omitempty"`
	Warning      string                                `json:"warning,omitempty"`
	Lifecycle    *pebblestore.SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	Result       runruntime.RunResult                  `json:"result,omitempty"`
	Background   bool                                  `json:"background,omitempty"`
	TargetKind   string                                `json:"target_kind,omitempty"`
	TargetName   string                                `json:"target_name,omitempty"`
}

type runStreamReplayFrame struct {
	seq     uint64
	evtType string
	payload []byte
}

type runStreamSubscriber struct {
	id   string
	send chan runStreamReplayFrame
}

type runStreamState struct {
	runID      string
	sessionID  string
	createdAt  time.Time
	updatedAt  time.Time
	done       bool
	nextSeq    uint64
	events     []runStreamReplayFrame
	subs       map[string]*runStreamSubscriber
	cancel     context.CancelFunc
	stopReason string
	mu         sync.Mutex
}

type runStreamActiveRun struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	LastSeq uint64 `json:"last_seq,omitempty"`
}

type runStreamManager struct {
	runs      map[string]*runStreamState
	replayTTL time.Duration
	maxReplay int
	nextRun   atomic.Uint64
	nextSub   atomic.Uint64
	mu        sync.Mutex
}

func newRunStreamManager() *runStreamManager {
	return &runStreamManager{
		runs:      make(map[string]*runStreamState),
		replayTTL: runStreamReplayTTL,
		maxReplay: runStreamReplayLimit,
	}
}

func (m *runStreamManager) newRun(sessionID string) (*runStreamState, error) {
	if m == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	now := time.Now()
	runOrdinal := m.nextRun.Add(1)
	runID := fmt.Sprintf("%s_%d_%06d", runStreamRunIDPrefix, now.UnixMilli(), runOrdinal)
	state := &runStreamState{
		runID:     runID,
		sessionID: sessionID,
		createdAt: now,
		updatedAt: now,
		events:    make([]runStreamReplayFrame, 0, 32),
		subs:      make(map[string]*runStreamSubscriber),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	m.runs[runID] = state
	return state, nil
}

func (m *runStreamManager) currentRun(sessionID string) (runStreamActiveRun, bool) {
	if m == nil {
		return runStreamActiveRun{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runStreamActiveRun{}, false
	}

	m.mu.Lock()
	m.cleanupLocked(time.Now())
	var latest *runStreamState
	for _, state := range m.runs {
		if state == nil || !strings.EqualFold(strings.TrimSpace(state.sessionID), sessionID) {
			continue
		}
		if latest == nil || state.updatedAt.After(latest.updatedAt) {
			latest = state
		}
	}
	m.mu.Unlock()
	if latest == nil {
		return runStreamActiveRun{}, false
	}

	latest.mu.Lock()
	defer latest.mu.Unlock()
	status := "running"
	if latest.done {
		status = "completed"
	}
	return runStreamActiveRun{RunID: latest.runID, Status: status, LastSeq: latest.nextSeq}, true
}

func (m *runStreamManager) subscribe(runID string, lastSeq uint64) (*runStreamState, *runStreamSubscriber, []runStreamReplayFrame, error) {
	if m == nil {
		return nil, nil, nil, errRunStreamNotFound
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil, nil, errRunStreamNotFound
	}

	m.mu.Lock()
	m.cleanupLocked(time.Now())
	state, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok || state == nil {
		return nil, nil, nil, errRunStreamNotFound
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	earliestSeq := uint64(1)
	if len(state.events) > 0 {
		earliestSeq = state.events[0].seq
	}
	if len(state.events) > 0 && lastSeq+1 < earliestSeq {
		return nil, nil, nil, &runStreamResumeGapError{RunID: runID, LastSeq: lastSeq, EarliestSeq: earliestSeq}
	}

	subscriberID := fmt.Sprintf("%s_%06d", runStreamSubscriberPref, m.nextSub.Add(1))
	subscriber := &runStreamSubscriber{
		id:   subscriberID,
		send: make(chan runStreamReplayFrame, runStreamSendQueueSize),
	}
	state.subs[subscriberID] = subscriber
	state.updatedAt = time.Now()

	replay := make([]runStreamReplayFrame, 0, len(state.events))
	for _, frame := range state.events {
		if frame.seq <= lastSeq {
			continue
		}
		replay = append(replay, frame)
	}
	return state, subscriber, replay, nil
}

func (m *runStreamManager) stopRun(sessionID, runID, reason string) error {
	if m == nil {
		return errRunStreamNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	reason = strings.TrimSpace(reason)
	if sessionID == "" || runID == "" {
		return errRunStreamNotFound
	}

	m.mu.Lock()
	m.cleanupLocked(time.Now())
	state := m.runs[runID]
	m.mu.Unlock()
	if state == nil {
		return errRunStreamNotFound
	}

	state.mu.Lock()
	if state.sessionID != sessionID {
		state.mu.Unlock()
		return errRunStreamNotFound
	}
	if state.done {
		state.mu.Unlock()
		return nil
	}
	cancel := state.cancel
	if reason != "" {
		state.stopReason = reason
	}
	state.updatedAt = time.Now()
	state.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *runStreamManager) attachCancel(runID string, cancel context.CancelFunc) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || cancel == nil {
		return
	}

	m.mu.Lock()
	state := m.runs[runID]
	m.mu.Unlock()
	if state == nil {
		return
	}

	state.mu.Lock()
	state.cancel = cancel
	state.updatedAt = time.Now()
	state.mu.Unlock()
}

func (m *runStreamManager) setStopReason(runID, reason string) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	reason = strings.TrimSpace(reason)
	if runID == "" || reason == "" {
		return
	}

	m.mu.Lock()
	state := m.runs[runID]
	m.mu.Unlock()
	if state == nil {
		return
	}

	state.mu.Lock()
	state.stopReason = reason
	state.updatedAt = time.Now()
	state.mu.Unlock()
}

func (m *runStreamManager) unsubscribe(runID, subscriberID string) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	subscriberID = strings.TrimSpace(subscriberID)
	if runID == "" || subscriberID == "" {
		return
	}

	m.mu.Lock()
	state := m.runs[runID]
	m.mu.Unlock()
	if state == nil {
		return
	}

	state.mu.Lock()
	state.removeSubscriberLocked(subscriberID)
	state.updatedAt = time.Now()
	state.mu.Unlock()
}

func (m *runStreamManager) publishRuntimeEvent(runID string, event runruntime.StreamEvent) {
	msg := runStreamWireEvent{
		Type:         strings.TrimSpace(event.Type),
		SessionID:    strings.TrimSpace(event.SessionID),
		RunID:        strings.TrimSpace(event.RunID),
		Agent:        strings.TrimSpace(event.Agent),
		Step:         event.Step,
		Delta:        event.Delta,
		Summary:      event.Summary,
		ToolName:     event.ToolName,
		CallID:       event.CallID,
		Arguments:    event.Arguments,
		Output:       event.Output,
		RawOutput:    event.RawOutput,
		Error:        event.Error,
		DurationMS:   event.DurationMS,
		Message:      event.Message,
		Permission:   event.Permission,
		TurnUsage:    event.TurnUsage,
		UsageSummary: event.UsageSummary,
		Title:        event.Title,
		TitleStage:   event.TitleStage,
		Warning:      event.Warning,
		Lifecycle:    event.Lifecycle,
	}
	if event.Lifecycle != nil {
		msg.Background = strings.EqualFold(strings.TrimSpace(event.Lifecycle.OwnerTransport), "background_api")
	}
	m.publish(runID, msg)
}

func (m *runStreamManager) publishCompleted(runID, sessionID string, result runruntime.RunResult) {
	msg := runStreamWireEvent{
		Type:      "turn.completed",
		SessionID: strings.TrimSpace(sessionID),
		Result:    result,
	}
	msg.Background = result.Background
	msg.TargetKind = strings.TrimSpace(result.TargetKind)
	msg.TargetName = strings.TrimSpace(result.TargetName)
	m.publish(runID, msg)
}

func (m *runStreamManager) publishError(runID, sessionID string, runErr error) {
	message := "stream run failed"
	if runErr != nil {
		if trimmed := strings.TrimSpace(runErr.Error()); trimmed != "" {
			message = trimmed
		}
	}
	if errors.Is(runErr, context.Canceled) {
		message = "Run stopped"
		m.mu.Lock()
		state := m.runs[strings.TrimSpace(runID)]
		m.mu.Unlock()
		if state != nil {
			state.mu.Lock()
			if strings.TrimSpace(state.stopReason) != "" {
				message = state.stopReason
			}
			state.mu.Unlock()
		}
	}
	msg := runStreamWireEvent{
		Type:      "turn.error",
		SessionID: strings.TrimSpace(sessionID),
		Error:     message,
	}
	m.publish(runID, msg)
}

func (m *runStreamManager) publish(runID string, msg runStreamWireEvent) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}

	m.mu.Lock()
	state := m.runs[runID]
	m.mu.Unlock()
	if state == nil {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if strings.TrimSpace(msg.SessionID) == "" {
		msg.SessionID = state.sessionID
	}
	msg.RunID = state.runID
	state.nextSeq++
	msg.Seq = state.nextSeq

	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	frame := runStreamReplayFrame{
		seq:     msg.Seq,
		evtType: strings.ToLower(strings.TrimSpace(msg.Type)),
		payload: raw,
	}
	state.events = append(state.events, frame)
	if m.maxReplay > 0 && len(state.events) > m.maxReplay {
		state.events = state.events[len(state.events)-m.maxReplay:]
	}
	if frame.evtType == "turn.completed" || frame.evtType == "turn.error" {
		state.done = true
	}
	state.updatedAt = time.Now()

	overflow := make([]string, 0, 4)
	for _, sub := range state.subs {
		if sub == nil {
			continue
		}
		select {
		case sub.send <- frame:
		default:
			overflow = append(overflow, sub.id)
		}
	}
	for _, subscriberID := range overflow {
		state.removeSubscriberLocked(subscriberID)
		log.Printf("run stream: dropping slow subscriber %s for run %s (queue overflow)", subscriberID, state.runID)
	}
}

func (m *runStreamManager) cleanupLocked(now time.Time) {
	if m == nil {
		return
	}
	for runID, state := range m.runs {
		if state == nil {
			delete(m.runs, runID)
			continue
		}
		state.mu.Lock()
		done := state.done
		idle := len(state.subs) == 0
		expired := now.After(state.updatedAt.Add(m.replayTTL))
		state.mu.Unlock()
		if done && idle && expired {
			delete(m.runs, runID)
		}
	}
}

func (s *runStreamState) removeSubscriberLocked(subscriberID string) {
	if s == nil {
		return
	}
	subscriber, ok := s.subs[subscriberID]
	if !ok {
		return
	}
	delete(s.subs, subscriberID)
	if subscriber != nil {
		close(subscriber.send)
	}
}

func (s *Server) handleRunStreamWebsocket(w http.ResponseWriter, r *http.Request, sessionID string) {
	remoteTarget, ok, err := s.routedSessionTarget(sessionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !ok {
		remoteTarget, err = s.currentRemoteSwarmTargetForRequest(r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
	}
	if remoteTarget != nil {
		if err := s.proxyRequestToSwarmTarget(w, r, *remoteTarget); err != nil {
			if errors.Is(err, transportws.ErrUpgradeRequired) {
				writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
				return
			}
			writeError(w, http.StatusBadGateway, err)
		}
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	if s.runStreams == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run stream manager not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}

	conn, err := transportws.Accept(w, r)
	if err != nil {
		log.Printf("run stream websocket accept failed session_id=%s remote_addr=%s path=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), r.URL.Path, err)
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()

	raw, err := conn.ReadText()
	if err != nil {
		log.Printf("run stream websocket initial read failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		return
	}
	inbound, err := decodeRunStreamInbound(raw)
	if err != nil {
		log.Printf("run stream websocket decode failed session_id=%s remote_addr=%s err=%v", sessionID, strings.TrimSpace(r.RemoteAddr), err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}

	switch inbound.Type {
	case "run.start", "start":
		s.handleRunStreamStart(conn, sessionID, inbound)
		return
	case "run.resume", "resume":
		s.handleRunStreamResume(conn, sessionID, inbound)
		return
	case "run.stop", "stop":
		s.handleRunStreamStop(conn, sessionID, inbound)
		return
	default:
		log.Printf("run stream websocket unsupported message session_id=%s remote_addr=%s type=%q", sessionID, strings.TrimSpace(r.RemoteAddr), inbound.Type)
		s.sendRunStreamControl(conn, runStreamControlMessage{
			Type:      "error",
			OK:        false,
			SessionID: sessionID,
			Error:     fmt.Sprintf("unsupported run stream message type %q", inbound.Type),
		})
		return
	}
}

func decodeRunStreamInbound(raw []byte) (runStreamInboundMessage, error) {
	var inbound runStreamInboundMessage
	if err := json.Unmarshal(raw, &inbound); err != nil {
		return runStreamInboundMessage{}, fmt.Errorf("decode run stream payload: %w", err)
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunRequest = inbound.RunRequest.Normalized()
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	return inbound, nil
}

func (s *Server) handleRunStreamStart(conn *transportws.Conn, sessionID string, inbound runStreamInboundMessage) {
	if inbound.RunRequest.Prompt == "" && !inbound.RunRequest.Compact {
		log.Printf("run stream start rejected session_id=%s err=%s", sessionID, "prompt is required")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: "prompt is required"})
		return
	}
	if normalized := runruntime.NormalizeRunTargetKind(inbound.RunRequest.TargetKind); inbound.RunRequest.TargetKind != "" && normalized == "" {
		log.Printf("run stream start rejected session_id=%s err=%s", sessionID, "unsupported target_kind")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: fmt.Sprintf("unsupported target_kind %q", inbound.RunRequest.TargetKind)})
		return
	}
	state, err := s.runStreams.newRun(sessionID)
	if err != nil {
		log.Printf("run stream start allocation failed session_id=%s err=%v", sessionID, err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	if state == nil {
		log.Printf("run stream start allocation failed session_id=%s err=%s", sessionID, "unable to allocate run stream")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: "unable to allocate run stream"})
		return
	}
	_, sub, replay, err := s.runStreams.subscribe(state.runID, inbound.LastSeq)
	if err != nil {
		log.Printf("run stream subscribe failed session_id=%s run_id=%s err=%v", sessionID, state.runID, err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: state.runID, Error: err.Error()})
		return
	}
	defer s.runStreams.unsubscribe(state.runID, sub.id)
	started := s.startRunStreamExecution(state.runID, sessionID, inbound)
	if startErr := <-started; startErr != nil {
		log.Printf("run stream start failed session_id=%s run_id=%s err=%v", sessionID, state.runID, startErr)
		status := runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: state.runID, Error: startErr.Error()}
		if errors.Is(startErr, runruntime.ErrSessionAlreadyActive) {
			status.Message = "run rejected"
		}
		s.sendRunStreamControl(conn, status)
		return
	}
	if inbound.RunRequest.Background {
		raw, err := json.Marshal(map[string]any{
			"ok":              true,
			"session_id":      sessionID,
			"run_id":          state.runID,
			"status":          "accepted",
			"background":      true,
			"target_kind":     strings.TrimSpace(inbound.RunRequest.TargetKind),
			"target_name":     strings.TrimSpace(inbound.RunRequest.TargetName),
			"owner_transport": "background_api",
		})
		if err == nil {
			_ = conn.WriteText(raw)
		}
		return
	}
	s.sendRunStreamControl(conn, runStreamControlMessage{
		Type:      "run.accepted",
		OK:        true,
		SessionID: sessionID,
		RunID:     state.runID,
		LastSeq:   inbound.LastSeq,
	})
	s.streamRunFrames(conn, state.runID, sub, replay)
}

func (s *Server) handleRunStreamResume(conn *transportws.Conn, sessionID string, inbound runStreamInboundMessage) {
	if inbound.RunID == "" {
		log.Printf("run stream resume rejected session_id=%s err=%s", sessionID, "run_id is required for resume")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: "run_id is required for resume"})
		return
	}
	state, sub, replay, err := s.runStreams.subscribe(inbound.RunID, inbound.LastSeq)
	if err != nil {
		if errors.Is(err, errRunStreamNotFound) {
			log.Printf("run stream resume failed session_id=%s run_id=%s err=%s", sessionID, inbound.RunID, "run stream not found")
			s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: inbound.RunID, Error: "run stream not found"})
			return
		}
		if gapErr := (*runStreamResumeGapError)(nil); errors.As(err, &gapErr) {
			log.Printf("run stream resume gap session_id=%s run_id=%s last_seq=%d earliest_seq=%d", sessionID, inbound.RunID, gapErr.LastSeq, gapErr.EarliestSeq)
			s.sendRunStreamControl(conn, runStreamControlMessage{
				Type:        "resume.error",
				OK:          false,
				SessionID:   sessionID,
				RunID:       inbound.RunID,
				LastSeq:     gapErr.LastSeq,
				EarliestSeq: gapErr.EarliestSeq,
				Error:       gapErr.Error(),
			})
			return
		}
		log.Printf("run stream resume subscribe failed session_id=%s run_id=%s err=%v", sessionID, inbound.RunID, err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: inbound.RunID, Error: err.Error()})
		return
	}
	if state.sessionID != sessionID {
		s.runStreams.unsubscribe(inbound.RunID, sub.id)
		log.Printf("run stream resume rejected session_id=%s run_id=%s err=%s", sessionID, inbound.RunID, "run/session mismatch")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: inbound.RunID, Error: "run/session mismatch"})
		return
	}
	defer s.runStreams.unsubscribe(inbound.RunID, sub.id)

	s.sendRunStreamControl(conn, runStreamControlMessage{
		Type:      "resume.accepted",
		OK:        true,
		SessionID: sessionID,
		RunID:     inbound.RunID,
		LastSeq:   inbound.LastSeq,
	})
	s.streamRunFrames(conn, inbound.RunID, sub, replay)
}

func (s *Server) handleRunStreamStop(conn *transportws.Conn, sessionID string, inbound runStreamInboundMessage) {
	if inbound.RunID == "" {
		log.Printf("run stream stop rejected session_id=%s err=%s", sessionID, "run_id is required for stop")
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, Error: "run_id is required for stop"})
		return
	}
	s.runStreams.setStopReason(inbound.RunID, "run stopped by user")
	if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
		log.Printf("run stream stop failed session_id=%s run_id=%s err=%v", sessionID, inbound.RunID, err)
		s.sendRunStreamControl(conn, runStreamControlMessage{Type: "error", OK: false, SessionID: sessionID, RunID: inbound.RunID, Error: err.Error()})
		return
	}
	s.sendRunStreamControl(conn, runStreamControlMessage{Type: "run.stop.accepted", OK: true, SessionID: sessionID, RunID: inbound.RunID})
}

func (s *Server) handleRunStreamControl(w http.ResponseWriter, r *http.Request, sessionID string) {
	remoteTarget, ok, err := s.routedSessionTarget(sessionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !ok {
		remoteTarget, err = s.currentRemoteSwarmTargetForRequest(r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
	}
	if remoteTarget != nil {
		if err := s.proxyRequestToSwarmTarget(w, r, *remoteTarget); err != nil {
			writeError(w, http.StatusBadGateway, err)
		}
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
		return
	}
	var inbound runStreamInboundMessage
	if err := decodeJSON(r, &inbound); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inbound.Type = strings.ToLower(strings.TrimSpace(inbound.Type))
	inbound.RunRequest = inbound.RunRequest.Normalized()
	inbound.RunID = strings.TrimSpace(inbound.RunID)
	switch inbound.Type {
	case "run.start", "start":
		state, err := s.runStreams.newRun(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		started := s.startRunStreamExecution(state.runID, sessionID, inbound)
		if startErr := <-started; startErr != nil {
			status := http.StatusConflict
			if !errors.Is(startErr, runruntime.ErrSessionAlreadyActive) {
				status = http.StatusBadRequest
			}
			writeError(w, status, startErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":              true,
			"session_id":      sessionID,
			"run_id":          state.runID,
			"status":          "accepted",
			"background":      inbound.RunRequest.Background,
			"target_kind":     strings.TrimSpace(inbound.RunRequest.TargetKind),
			"target_name":     strings.TrimSpace(inbound.RunRequest.TargetName),
			"owner_transport": "background_api",
		})
	case "run.stop", "stop":
		if inbound.RunID == "" {
			writeError(w, http.StatusBadRequest, errors.New("run_id is required for stop"))
			return
		}
		s.runStreams.setStopReason(inbound.RunID, "run stopped by user")
		if err := s.runner.StopSessionRun(sessionID, inbound.RunID, "run stopped by user"); err != nil {
			status := http.StatusConflict
			if errors.Is(err, runruntime.ErrSessionRunNotActive) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"session_id": sessionID,
			"run_id":     inbound.RunID,
		})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported run stream control type %q", inbound.Type))
	}
}

func (s *Server) startRunStreamExecution(runID, sessionID string, inbound runStreamInboundMessage) <-chan error {
	started := make(chan error, 1)
	if s == nil || s.runner == nil || s.runStreams == nil {
		started <- errors.New("run service is not configured")
		return started
	}
	s.beginActiveRun()
	go func() {
		defer s.endActiveRun()
		defer close(started)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("run stream panic run_id=%s session_id=%s panic=%v stack=%s", runID, sessionID, recovered, strings.TrimSpace(string(debug.Stack())))
				err := fmt.Errorf("run panicked: %v", recovered)
				select {
				case started <- err:
				default:
				}
				s.runStreams.publishError(runID, sessionID, err)
			}
		}()

		// Run lifecycle survives websocket disconnects; reconnect/resume attaches
		// to this same run_id on the same canonical websocket path.
		runCtx, runCancel := context.WithCancel(s.runCtx)
		defer runCancel()

		startSignaled := false
		result, err := s.runner.RunTurnStreaming(runCtx, sessionID, inbound.RunRequest, runruntime.RunStartMeta{
			RunID:          runID,
			OwnerTransport: "background_api",
		}, func(event runruntime.StreamEvent) {
			if !startSignaled && strings.EqualFold(strings.TrimSpace(event.Type), runruntime.StreamEventSessionLifecycle) && event.Lifecycle != nil && event.Lifecycle.Active {
				startSignaled = true
				select {
				case started <- nil:
				default:
				}
			}
			s.runStreams.publishRuntimeEvent(runID, event)
		})
		if err != nil {
			if !startSignaled {
				select {
				case started <- err:
				default:
				}
			}
			s.runStreams.publishError(runID, sessionID, err)
			return
		}
		if !startSignaled {
			select {
			case started <- errors.New("run started without lifecycle acknowledgement"):
			default:
			}
		}
		for _, event := range result.Events {
			s.hub.Publish(event)
		}
		streamResult := result
		streamResult.ToolMessages = nil
		s.runStreams.publishCompleted(runID, sessionID, streamResult)
	}()
	return started
}

func (s *Server) streamRunFrames(conn *transportws.Conn, runID string, sub *runStreamSubscriber, replay []runStreamReplayFrame) {
	for _, frame := range replay {
		if err := conn.WriteText(frame.payload); err != nil {
			return
		}
		if isTerminalRunFrame(frame.evtType) {
			return
		}
	}

	keepaliveRaw, err := json.Marshal(runStreamKeepaliveMessage{Type: "keepalive", RunID: strings.TrimSpace(runID)})
	if err != nil {
		keepaliveRaw = nil
	}
	keepaliveTicker := time.NewTicker(runStreamKeepaliveInterval)
	defer keepaliveTicker.Stop()

	for {
		select {
		case frame, ok := <-sub.send:
			if !ok {
				return
			}
			if err := conn.WriteText(frame.payload); err != nil {
				return
			}
			if isTerminalRunFrame(frame.evtType) {
				return
			}
		case <-keepaliveTicker.C:
			if len(keepaliveRaw) == 0 {
				continue
			}
			if err := conn.WriteText(keepaliveRaw); err != nil {
				return
			}
		}
	}
}

func isTerminalRunFrame(evtType string) bool {
	evtType = strings.ToLower(strings.TrimSpace(evtType))
	return evtType == "turn.completed" || evtType == "turn.error"
}

func (s *Server) sendRunStreamControl(conn *transportws.Conn, msg runStreamControlMessage) {
	if conn == nil {
		return
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = conn.WriteText(raw)
}

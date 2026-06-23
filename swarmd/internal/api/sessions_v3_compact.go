package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionV3ManualCompactOwnerTransport = "manual_compact"

type sessionV3ManualCompactTerminal struct {
	Result    sessionruntime.SessionMutationResult
	Status    string
	Phase     string
	EventType string
	Error     string
}

type sessionV3ManualCompactExecution struct {
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	terminal sessionV3ManualCompactTerminal
}

func newSessionV3ManualCompactExecution() *sessionV3ManualCompactExecution {
	return &sessionV3ManualCompactExecution{done: make(chan struct{})}
}

func (e *sessionV3ManualCompactExecution) complete(terminal sessionV3ManualCompactTerminal) {
	if e == nil {
		return
	}
	e.once.Do(func() {
		e.mu.Lock()
		e.terminal = terminal
		e.mu.Unlock()
		close(e.done)
	})
}

func (e *sessionV3ManualCompactExecution) terminalResult() sessionV3ManualCompactTerminal {
	if e == nil {
		return sessionV3ManualCompactTerminal{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.terminal
}

func sessionV3CompactMutationResponse(sessionID string, runIntent pebblestore.V3SessionRunIntent, mutation *sessionruntime.SessionMutationResult) map[string]any {
	var mutationPayload any
	var realtimeOutbox any
	if mutation != nil {
		mutationPayload = sessionV3MutationResultResponse(*mutation)
		realtimeOutbox = mutation.RealtimeOutbox
	}
	status := strings.TrimSpace(runIntent.Status)
	if status == "" {
		status = sessionruntime.RunIntentCompleted
	}
	return map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"run_id":          runIntent.RunID,
		"status":          status,
		"run_intent":      runIntent,
		"compaction":      map[string]any{"run_id": runIntent.RunID, "status": status, "owner_transport": sessionV3ManualCompactOwnerTransport},
		"terminal":        sessionV3CompactTerminalResponse(mutation, status),
		"mutation":        mutationPayload,
		"realtime_outbox": realtimeOutbox,
	}
}

func sessionV3CompactTerminalResponse(mutation *sessionruntime.SessionMutationResult, status string) map[string]any {
	out := map[string]any{"event_type": "session.lifecycle.updated"}
	if mutation != nil {
		out["event_type"] = firstNonEmptyString(strings.TrimSpace(mutation.Event.EventType), "session.lifecycle.updated")
		if mutation.Lifecycle != nil {
			out["phase"] = strings.TrimSpace(mutation.Lifecycle.Phase)
		}
	}
	if out["phase"] == nil || strings.TrimSpace(fmt.Sprint(out["phase"])) == "" {
		out["phase"] = sessionV3CompactPhaseForStatus(status)
	}
	return out
}

func sessionV3CompactTerminalErrorResponse(sessionID, runID string, terminal sessionV3ManualCompactTerminal) map[string]any {
	message := strings.TrimSpace(terminal.Error)
	if message == "" && terminal.Result.RunIntent != nil {
		message = strings.TrimSpace(terminal.Result.RunIntent.BlockedReason)
	}
	if message == "" && terminal.Result.Lifecycle != nil {
		message = firstNonEmptyString(strings.TrimSpace(terminal.Result.Lifecycle.Error), strings.TrimSpace(terminal.Result.Lifecycle.StopReason))
	}
	if message == "" {
		message = "manual compact failed"
	}
	status := strings.TrimSpace(terminal.Status)
	if status == "" && terminal.Result.RunIntent != nil {
		status = strings.TrimSpace(terminal.Result.RunIntent.Status)
	}
	if status == "" {
		status = sessionruntime.RunIntentFailed
	}
	return map[string]any{
		"ok":              false,
		"session_id":      sessionID,
		"run_id":          runID,
		"status":          status,
		"error":           message,
		"compaction":      map[string]any{"run_id": runID, "status": status, "owner_transport": sessionV3ManualCompactOwnerTransport},
		"terminal":        sessionV3CompactTerminalResponse(&terminal.Result, status),
		"mutation":        sessionV3MutationResultResponse(terminal.Result),
		"realtime_outbox": terminal.Result.RealtimeOutbox,
	}
}

func (s *Server) handleSessionV3PrimaryCompact(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("run service is not configured"))
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if s.isShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
		return
	}
	var req sessionsV3CompactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = stableSessionsV3PrimaryRunID(sessionID, clientRequestID)
	}
	if active, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if ok {
		if !strings.EqualFold(strings.TrimSpace(active.RunID), runID) {
			writeError(w, http.StatusConflict, runruntime.ErrSessionAlreadyActive)
			return
		}
		if terminal, terminalOK, err := s.sessionV3ManualCompactTerminalFromStore(principal, sessionID, runID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if terminalOK {
			s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, terminal)
			return
		}
		execution, found := s.getSessionV3ManualCompactExecution(sessionID, runID)
		if !found {
			writeError(w, http.StatusConflict, errors.New("manual compact is already active and no owned execution is available to await"))
			return
		}
		s.awaitSessionV3ManualCompactExecution(w, r.Context(), principal, sessionID, runID, execution)
		return
	}

	accepted, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: clientRequestID + ":accepted",
		EventType:       "session.run_intent.recorded",
		Status:          sessionruntime.RunIntentPendingExecutor,
		Payload: map[string]any{
			"type":            "session.run_intent.recorded",
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          sessionruntime.RunIntentPendingExecutor,
			"owner_transport": sessionV3ManualCompactOwnerTransport,
		},
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if terminal, terminalOK := sessionV3ManualCompactTerminalFromMutation(accepted); terminalOK {
		s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, terminal)
		return
	}

	execution := s.ensureSessionV3ManualCompactExecution(sessionID, runID)
	if !accepted.Replayed {
		if _, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
			Principal:       principal,
			SessionID:       sessionID,
			RunID:           runID,
			ClientRequestID: clientRequestID + ":running",
			EventType:       "session.run_intent.recorded",
			Status:          sessionruntime.RunIntentRunning,
			Payload: map[string]any{
				"type":            "session.run_intent.recorded",
				"session_id":      sessionID,
				"run_id":          runID,
				"status":          sessionruntime.RunIntentRunning,
				"owner_transport": sessionV3ManualCompactOwnerTransport,
			},
		}); err != nil {
			s.forgetSessionV3ManualCompactExecution(sessionID, runID, execution)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.startSessionV3ManualCompactExecution(sessionID, runID, req, principal, execution)
	} else if terminal, terminalOK, err := s.sessionV3ManualCompactTerminalFromStore(principal, sessionID, runID); err != nil {
		s.forgetSessionV3ManualCompactExecution(sessionID, runID, execution)
		writeError(w, http.StatusBadRequest, err)
		return
	} else if terminalOK {
		execution.complete(terminal)
	}

	s.awaitSessionV3ManualCompactExecution(w, r.Context(), principal, sessionID, runID, execution)
}

func (s *Server) awaitSessionV3ManualCompactExecution(w http.ResponseWriter, ctx context.Context, principal identity.Principal, sessionID, runID string, execution *sessionV3ManualCompactExecution) {
	if execution == nil {
		writeError(w, http.StatusInternalServerError, errors.New("manual compact execution is not available"))
		return
	}
	select {
	case <-execution.done:
		s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, execution.terminalResult())
	case <-ctx.Done():
		writeError(w, http.StatusRequestTimeout, ctx.Err())
	case <-s.runCtx.Done():
		writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
	}
}

func (s *Server) writeSessionV3ManualCompactTerminal(w http.ResponseWriter, principal identity.Principal, sessionID, runID string, terminal sessionV3ManualCompactTerminal) {
	status := strings.TrimSpace(terminal.Status)
	if status == "" && terminal.Result.RunIntent != nil {
		status = strings.TrimSpace(terminal.Result.RunIntent.Status)
	}
	if signed, ok := s.sessionV3ManualCompactTerminalWithHydrateCursor(principal, sessionID, terminal); ok {
		terminal = signed
	}
	if status == sessionruntime.RunIntentCompleted {
		intent := pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, Status: status}
		if terminal.Result.RunIntent != nil {
			intent = *terminal.Result.RunIntent
		}
		writeJSON(w, http.StatusAccepted, sessionV3CompactMutationResponse(sessionID, intent, &terminal.Result))
		return
	}
	writeJSON(w, http.StatusInternalServerError, sessionV3CompactTerminalErrorResponse(sessionID, runID, terminal))
}

func (s *Server) ensureSessionV3ManualCompactExecution(sessionID, runID string) *sessionV3ManualCompactExecution {
	key := sessionV3ManualCompactExecutionKey(sessionID, runID)
	s.manualCompactMu.Lock()
	defer s.manualCompactMu.Unlock()
	if s.manualCompactRuns == nil {
		s.manualCompactRuns = make(map[string]*sessionV3ManualCompactExecution)
	}
	if existing := s.manualCompactRuns[key]; existing != nil {
		return existing
	}
	execution := newSessionV3ManualCompactExecution()
	s.manualCompactRuns[key] = execution
	return execution
}

func (s *Server) getSessionV3ManualCompactExecution(sessionID, runID string) (*sessionV3ManualCompactExecution, bool) {
	key := sessionV3ManualCompactExecutionKey(sessionID, runID)
	s.manualCompactMu.Lock()
	defer s.manualCompactMu.Unlock()
	execution := s.manualCompactRuns[key]
	return execution, execution != nil
}

func (s *Server) forgetSessionV3ManualCompactExecution(sessionID, runID string, execution *sessionV3ManualCompactExecution) {
	key := sessionV3ManualCompactExecutionKey(sessionID, runID)
	s.manualCompactMu.Lock()
	defer s.manualCompactMu.Unlock()
	if s.manualCompactRuns[key] == execution {
		delete(s.manualCompactRuns, key)
	}
}

func sessionV3ManualCompactExecutionKey(sessionID, runID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(runID)
}

func (s *Server) sessionV3ManualCompactTerminalWithHydrateCursor(principal identity.Principal, sessionID string, terminal sessionV3ManualCompactTerminal) (sessionV3ManualCompactTerminal, bool) {
	if s == nil || terminal.Result.RealtimeOutbox == nil {
		return terminal, false
	}
	outbox := *terminal.Result.RealtimeOutbox
	if outbox.EndpointSeq == 0 {
		return terminal, false
	}
	selector := sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: canonicalV3SyncSessionIDs([]string{sessionID})}
	resources := sessionsV3SyncResourceSet(
		sessionsV3WorksetResources{Messages: true, RunIntents: true, ActivePlan: true},
		sessionsV3WorksetHistory{Mode: pebblestore.V3SyncSnapshotHistoryModeTail, MaxMessagesPerSession: 200, ManifestPolicy: "manifest"},
		true,
	)
	signed, err := s.signV3SyncEndpointCursor(v3SyncCursorScopeForSnapshot(principal, "desktop", "v3.sync.snapshot", selector, resources), outbox.EndpointSeq)
	if err != nil || strings.TrimSpace(signed) == "" {
		return terminal, false
	}
	outbox.EndpointCursor = signed
	terminal.Result.RealtimeOutbox = &outbox
	return terminal, true
}

func (s *Server) sessionV3ManualCompactTerminalFromStore(principal identity.Principal, sessionID, runID string) (sessionV3ManualCompactTerminal, bool, error) {
	intent, ok, err := s.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok || !sessionV3ManualCompactTerminalStatus(intent.Status) {
		return sessionV3ManualCompactTerminal{}, false, err
	}
	afterSeq := uint64(0)
	if intent.EventSeq > 0 {
		afterSeq = intent.EventSeq - 1
	}
	records, err := s.sessions.ListRealtimeOutboxForSessionAfterSeq(sessionID, afterSeq, 16)
	if err != nil {
		return sessionV3ManualCompactTerminal{}, false, err
	}
	for _, record := range records {
		if record.Event.Seq != intent.EventSeq {
			continue
		}
		result := sessionruntime.SessionMutationResult{SessionID: sessionID, PrimarySeq: record.Event.Seq, FirstSeq: record.Event.Seq, LastSeq: record.Event.Seq, EventIDs: []string{record.Event.ID}, Event: record.Event, RunIntent: &intent, Projection: record.Projection, RealtimeOutbox: &record}
		if lifecycle, ok := sessionV3CompactLifecycleFromPayload(record.Event.Payload); ok {
			result.Lifecycle = &lifecycle
		}
		terminal, ok := sessionV3ManualCompactTerminalFromMutation(result)
		if ok {
			if signed, signedOK := s.sessionV3ManualCompactTerminalWithHydrateCursor(principal, sessionID, terminal); signedOK {
				terminal = signed
			}
		}
		return terminal, ok, nil
	}
	return sessionV3ManualCompactTerminal{}, false, nil
}

func sessionV3CompactLifecycleFromPayload(raw json.RawMessage) (pebblestore.SessionLifecycleSnapshot, bool) {
	if len(raw) == 0 {
		return pebblestore.SessionLifecycleSnapshot{}, false
	}
	var payload struct {
		Lifecycle *pebblestore.SessionLifecycleSnapshot `json:"lifecycle"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Lifecycle == nil {
		return pebblestore.SessionLifecycleSnapshot{}, false
	}
	return *payload.Lifecycle, true
}

func sessionV3ManualCompactTerminalFromMutation(result sessionruntime.SessionMutationResult) (sessionV3ManualCompactTerminal, bool) {
	if strings.TrimSpace(result.Event.EventType) != "session.lifecycle.updated" || result.Lifecycle == nil {
		return sessionV3ManualCompactTerminal{}, false
	}
	phase := strings.ToLower(strings.TrimSpace(result.Lifecycle.Phase))
	if !sessionV3ManualCompactTerminalPhase(phase) {
		return sessionV3ManualCompactTerminal{}, false
	}
	status := ""
	if result.RunIntent != nil {
		status = strings.TrimSpace(result.RunIntent.Status)
	}
	if status == "" {
		status = sessionV3CompactStatusForPhase(phase)
	}
	terminal := sessionV3ManualCompactTerminal{Result: result, Status: status, Phase: phase, EventType: result.Event.EventType}
	if status != sessionruntime.RunIntentCompleted {
		terminal.Error = firstNonEmptyString(strings.TrimSpace(result.RunIntent.BlockedReason), strings.TrimSpace(result.Lifecycle.Error), strings.TrimSpace(result.Lifecycle.StopReason), "manual compact "+status)
	}
	return terminal, true
}

func sessionV3ManualCompactTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case sessionruntime.RunIntentCompleted, sessionruntime.RunIntentFailed, sessionruntime.RunIntentCancelled, sessionruntime.RunIntentInterrupted:
		return true
	default:
		return false
	}
}

func sessionV3ManualCompactTerminalPhase(phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "completed", "errored", "failed", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func sessionV3CompactStatusForPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "completed":
		return sessionruntime.RunIntentCompleted
	case "cancelled":
		return sessionruntime.RunIntentCancelled
	case "interrupted":
		return sessionruntime.RunIntentInterrupted
	case "errored", "failed":
		return sessionruntime.RunIntentFailed
	default:
		return sessionruntime.RunIntentRunning
	}
}

func sessionV3CompactPhaseForStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case sessionruntime.RunIntentCompleted:
		return "completed"
	case sessionruntime.RunIntentCancelled:
		return "cancelled"
	case sessionruntime.RunIntentInterrupted:
		return "interrupted"
	case sessionruntime.RunIntentFailed:
		return "errored"
	default:
		return strings.TrimSpace(status)
	}
}

type sessionV3ManualCompactRunEventInput struct {
	Principal       identity.Principal
	SessionID       string
	RunID           string
	ClientRequestID string
	EventType       string
	Status          string
	BlockedReason   string
	Payload         map[string]any
	Lifecycle       *pebblestore.SessionLifecycleSnapshot
}

func (s *Server) startSessionV3ManualCompactExecution(sessionID, runID string, req sessionsV3CompactRequest, principal identity.Principal, execution *sessionV3ManualCompactExecution) {
	if s == nil || s.runner == nil {
		return
	}
	s.beginActiveRun()
	go func() {
		defer s.endActiveRun()
		defer s.forgetSessionV3ManualCompactExecution(sessionID, runID, execution)
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("manual compact panicked: %v", recovered)
				log.Printf("manual compact panic session_id=%s run_id=%s panic=%v", sessionID, runID, recovered)
				terminal, _ := s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, err)
				if execution != nil {
					execution.complete(terminal)
				}
			}
		}()

		runCtx, runCancel := context.WithCancel(s.runCtx)
		defer runCancel()
		if principal.Valid() {
			runCtx = identity.ContextWithPrincipal(runCtx, principal)
		}
		bridge := newSessionV3ManualCompactEventBridge(s, principal, sessionID, runID, runCancel, execution)
		result, err := s.runner.RunTurnStreaming(runCtx, sessionID, runruntime.RunRequest{
			Prompt:       strings.TrimSpace(req.Note),
			AgentName:    strings.TrimSpace(req.AgentName),
			Instructions: strings.TrimSpace(req.Instructions),
			Compact:      true,
		}, runruntime.RunStartMeta{
			RunID:                runID,
			OwnerTransport:       sessionV3ManualCompactOwnerTransport,
			Principal:            principal,
			ApplySessionMutation: s.applySessionV3PrimaryMutation,
		}, bridge.Handle)
		if bridgeErr := bridge.Err(); bridgeErr != nil && err == nil {
			err = bridgeErr
		}
		if err != nil {
			if bridge.TerminalSeen() {
				return
			}
			terminal, _ := s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, err)
			if execution != nil {
				execution.complete(terminal)
			}
			return
		}
		for _, event := range result.Events {
			if s.hub != nil {
				s.hub.Publish(event)
			}
		}
	}()
}

type sessionV3ManualCompactEventBridge struct {
	server    *Server
	principal identity.Principal
	sessionID string
	runID     string
	cancel    context.CancelFunc
	execution *sessionV3ManualCompactExecution

	mu           sync.Mutex
	nextSeq      int
	firstErr     error
	terminalSeen bool
}

func newSessionV3ManualCompactEventBridge(server *Server, principal identity.Principal, sessionID, runID string, cancel context.CancelFunc, execution *sessionV3ManualCompactExecution) *sessionV3ManualCompactEventBridge {
	return &sessionV3ManualCompactEventBridge{server: server, principal: principal, sessionID: strings.TrimSpace(sessionID), runID: strings.TrimSpace(runID), cancel: cancel, execution: execution}
}

func (b *sessionV3ManualCompactEventBridge) Err() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.firstErr
}

func (b *sessionV3ManualCompactEventBridge) TerminalSeen() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.terminalSeen
}

func (b *sessionV3ManualCompactEventBridge) Handle(event runruntime.StreamEvent) {
	if b == nil || b.server == nil {
		return
	}
	if err := b.publish(event); err != nil {
		b.mu.Lock()
		if b.firstErr == nil {
			b.firstErr = err
		}
		b.mu.Unlock()
		if b.cancel != nil {
			b.cancel()
		}
	}
}

func (b *sessionV3ManualCompactEventBridge) publish(event runruntime.StreamEvent) error {
	eventType, status, blockedReason, payload, lifecycle := b.v3EventForRuntimeEvent(event)
	if eventType == "" {
		return nil
	}
	b.mu.Lock()
	b.nextSeq++
	clientRequestID := fmt.Sprintf("manual-compact:%s:%06d:%s", b.runID, b.nextSeq, eventType)
	b.mu.Unlock()
	result, err := b.server.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       b.principal,
		SessionID:       b.sessionID,
		RunID:           b.runID,
		ClientRequestID: clientRequestID,
		EventType:       eventType,
		Status:          status,
		BlockedReason:   blockedReason,
		Payload:         payload,
		Lifecycle:       lifecycle,
	})
	if err != nil {
		return err
	}
	if terminal, ok := sessionV3ManualCompactTerminalFromMutation(result); ok {
		b.mu.Lock()
		b.terminalSeen = true
		b.mu.Unlock()
		if b.execution != nil {
			b.execution.complete(terminal)
		}
	}
	return nil
}

func (b *sessionV3ManualCompactEventBridge) v3EventForRuntimeEvent(event runruntime.StreamEvent) (eventType, status, blockedReason string, payload map[string]any, lifecycle *pebblestore.SessionLifecycleSnapshot) {
	sessionID := firstNonEmptyString(strings.TrimSpace(event.SessionID), b.sessionID)
	runID := firstNonEmptyString(strings.TrimSpace(event.RunID), b.runID)
	payload = map[string]any{
		"session_id":      sessionID,
		"run_id":          runID,
		"owner_transport": sessionV3ManualCompactOwnerTransport,
	}
	if event.Step != 0 {
		payload["step"] = event.Step
	}
	if event.Agent != "" {
		payload["agent"] = event.Agent
	}
	switch strings.TrimSpace(event.Type) {
	case runruntime.StreamEventSessionLifecycle:
		if event.Lifecycle == nil {
			return "", "", "", nil, nil
		}
		copy := *event.Lifecycle
		lifecycle = &copy
		payload["type"] = "session.lifecycle.updated"
		payload["lifecycle"] = lifecycle
		switch strings.ToLower(strings.TrimSpace(lifecycle.Phase)) {
		case "active", "running":
			status = sessionruntime.RunIntentRunning
		case "completed":
			status = sessionruntime.RunIntentCompleted
		case "cancelled":
			status = sessionruntime.RunIntentCancelled
		case "interrupted":
			status = sessionruntime.RunIntentInterrupted
		case "errored", "failed":
			status = sessionruntime.RunIntentFailed
		default:
			status = sessionruntime.RunIntentRunning
		}
		if status != sessionruntime.RunIntentRunning {
			blockedReason = firstNonEmptyString(strings.TrimSpace(lifecycle.Error), strings.TrimSpace(lifecycle.StopReason))
		}
		return "session.lifecycle.updated", status, blockedReason, payload, lifecycle
	case runruntime.StreamEventTurnStarted:
		payload["type"] = "session.assistant.started"
		status = sessionruntime.RunIntentRunning
		return "session.assistant.started", status, "", payload, nil
	case runruntime.StreamEventStepStarted:
		payload["type"] = "session.step.started"
		status = sessionruntime.RunIntentRunning
		return "session.step.started", status, "", payload, nil
	case runruntime.StreamEventSessionStatus:
		payload["type"] = "session.status"
		payload["status"] = strings.TrimSpace(event.Status)
		payload["summary"] = strings.TrimSpace(event.Summary)
		status = sessionruntime.RunIntentRunning
		return "session.status", status, "", payload, nil
	case runruntime.StreamEventToolStarted:
		payload["type"] = "session.tool.started"
		payload["tool_name"] = strings.TrimSpace(event.ToolName)
		payload["call_id"] = strings.TrimSpace(event.CallID)
		payload["arguments"] = event.Arguments
		payload["output"] = event.Output
		payload["raw_output"] = event.RawOutput
		payload["summary"] = event.Summary
		status = sessionruntime.RunIntentRunning
		return "session.tool.started", status, "", payload, nil
	case runruntime.StreamEventToolDelta:
		payload["type"] = "session.tool.delta"
		payload["tool_name"] = strings.TrimSpace(event.ToolName)
		payload["call_id"] = strings.TrimSpace(event.CallID)
		payload["output"] = event.Output
		payload["raw_output"] = event.RawOutput
		payload["summary"] = event.Summary
		status = sessionruntime.RunIntentRunning
		return "session.tool.delta", status, "", payload, nil
	case runruntime.StreamEventToolCompleted:
		payload["type"] = "session.tool.completed"
		payload["tool_name"] = strings.TrimSpace(event.ToolName)
		payload["call_id"] = strings.TrimSpace(event.CallID)
		payload["arguments"] = event.Arguments
		payload["output"] = event.Output
		payload["raw_output"] = event.RawOutput
		payload["duration_ms"] = event.DurationMS
		payload["summary"] = event.Summary
		if strings.TrimSpace(event.Error) != "" {
			payload["error"] = strings.TrimSpace(event.Error)
			blockedReason = strings.TrimSpace(event.Error)
			eventType = "session.tool.failed"
		} else {
			eventType = "session.tool.completed"
		}
		status = sessionruntime.RunIntentRunning
		return eventType, status, blockedReason, payload, nil
	case runruntime.StreamEventUsageUpdated:
		payload["type"] = "run.usage.updated"
		if event.TurnUsage != nil {
			payload["turn_usage"] = event.TurnUsage
		}
		if event.UsageSummary != nil {
			payload["usage_summary"] = event.UsageSummary
		}
		status = sessionruntime.RunIntentRunning
		return "run.usage.updated", status, "", payload, nil
	case runruntime.StreamEventSessionTitle:
		payload["type"] = "session.title.updated"
		payload["title"] = strings.TrimSpace(event.Title)
		payload["title_stage"] = strings.TrimSpace(event.TitleStage)
		status = sessionruntime.RunIntentRunning
		return "session.title.updated", status, "", payload, nil
	case runruntime.StreamEventSessionWarning:
		payload["type"] = "session.warning"
		payload["warning"] = strings.TrimSpace(event.Warning)
		status = sessionruntime.RunIntentRunning
		return "session.warning", status, "", payload, nil
	case runruntime.StreamEventTurnCompleted, runruntime.StreamEventTurnError:
		// The preceding terminal lifecycle event is the authoritative Sessions V3
		// terminal event. Recording another terminal run-intent event here would
		// race or duplicate the same state transition.
		return "", "", "", nil, nil
	default:
		return "", "", "", nil, nil
	}
}

func (s *Server) recordSessionV3ManualCompactRunEvent(input sessionV3ManualCompactRunEventInput) (sessionruntime.SessionMutationResult, error) {
	if s == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("server is not configured")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	input.EventType = strings.TrimSpace(input.EventType)
	input.Status = strings.TrimSpace(input.Status)
	input.BlockedReason = strings.TrimSpace(input.BlockedReason)
	if input.Status == "" {
		input.Status = sessionruntime.RunIntentRunning
	}
	if input.EventType == "" {
		input.EventType = "session.run_intent.recorded"
	}
	if input.Payload == nil {
		input.Payload = map[string]any{}
	}
	if input.Payload["session_id"] == nil {
		input.Payload["session_id"] = input.SessionID
	}
	if input.Payload["run_id"] == nil {
		input.Payload["run_id"] = input.RunID
	}
	if input.Payload["status"] == nil && input.Status != "" {
		input.Payload["status"] = input.Status
	}
	now := time.Now().UnixMilli()
	intent := pebblestore.V3SessionRunIntent{
		SessionID:      input.SessionID,
		UserID:         input.Principal.UserID,
		AccountScopeID: input.Principal.AccountScopeID,
		RunID:          input.RunID,
		Status:         input.Status,
		BlockedReason:  input.BlockedReason,
		UpdatedAt:      now,
	}
	if existing, ok, err := s.sessions.GetSessionRunIntent(input.SessionID, input.RunID); err == nil && ok && existing.CreatedAt != 0 {
		intent.CreatedAt = existing.CreatedAt
	}
	input.Payload["run_intent"] = intent
	raw, err := json.Marshal(input.Payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(input.SessionID, input.RunID, input.Status, input.BlockedReason, input.EventType, string(raw))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	kind := sessionruntime.SessionMutationRecordRunIntent
	mutation := sessionruntime.SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          input.Principal.UserID,
		AccountScopeID:  input.Principal.AccountScopeID,
		ClientRequestID: input.ClientRequestID,
		IdempotencyKey:  input.ClientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            kind,
		EventType:       input.EventType,
		EventPayload:    raw,
		RunIntent:       &intent,
		NowUnixMs:       now,
	}
	if input.Lifecycle != nil {
		kind = sessionruntime.SessionMutationUpsertLifecycle
		mutation.Kind = kind
		mutation.Lifecycle = input.Lifecycle
	}
	return s.applySessionV3PrimaryMutation(mutation)
}

func (s *Server) publishSessionV3ManualCompactFailure(principal identity.Principal, sessionID, runID string, runErr error) (sessionV3ManualCompactTerminal, error) {
	message := "manual compact failed"
	if runErr != nil && strings.TrimSpace(runErr.Error()) != "" {
		message = strings.TrimSpace(runErr.Error())
	}
	lifecycle := &pebblestore.SessionLifecycleSnapshot{
		SessionID:      strings.TrimSpace(sessionID),
		RunID:          strings.TrimSpace(runID),
		Active:         false,
		Phase:          "errored",
		EndedAt:        time.Now().UnixMilli(),
		UpdatedAt:      time.Now().UnixMilli(),
		Error:          message,
		OwnerTransport: sessionV3ManualCompactOwnerTransport,
	}
	if current, ok, err := s.sessions.GetLifecycle(sessionID); err == nil && ok {
		lifecycle.UserID = current.UserID
		lifecycle.AccountScopeID = current.AccountScopeID
		lifecycle.StartedAt = current.StartedAt
		lifecycle.Generation = current.Generation
	}
	result, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: fmt.Sprintf("manual-compact:%s:failure", strings.TrimSpace(runID)),
		EventType:       "session.lifecycle.updated",
		Status:          sessionruntime.RunIntentFailed,
		BlockedReason:   message,
		Payload: map[string]any{
			"type":            "session.lifecycle.updated",
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          sessionruntime.RunIntentFailed,
			"error":           message,
			"owner_transport": sessionV3ManualCompactOwnerTransport,
			"lifecycle":       lifecycle,
		},
		Lifecycle: lifecycle,
	})
	if err != nil {
		return sessionV3ManualCompactTerminal{Status: sessionruntime.RunIntentFailed, Phase: "errored", EventType: "session.lifecycle.updated", Error: message}, err
	}
	terminal, ok := sessionV3ManualCompactTerminalFromMutation(result)
	if !ok {
		terminal = sessionV3ManualCompactTerminal{Result: result, Status: sessionruntime.RunIntentFailed, Phase: "errored", EventType: "session.lifecycle.updated", Error: message}
	}
	return terminal, nil
}

func sessionsV3CompactStreamEvents(events []runruntime.StreamEvent) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, sessionsV3CompactStreamEvent(event))
	}
	return out
}

func sessionsV3CompactStreamEvent(event runruntime.StreamEvent) map[string]any {
	item := map[string]any{
		"type":       event.Type,
		"session_id": event.SessionID,
		"run_id":     event.RunID,
	}
	if event.Agent != "" {
		item["agent"] = event.Agent
	}
	if event.Status != "" {
		item["status"] = event.Status
	}
	if event.Step != 0 {
		item["step"] = event.Step
	}
	if event.Delta != "" {
		item["delta"] = event.Delta
	}
	if event.Summary != "" {
		item["summary"] = event.Summary
	}
	if event.ToolName != "" {
		item["tool_name"] = event.ToolName
	}
	if event.CallID != "" {
		item["call_id"] = event.CallID
	}
	if event.Arguments != "" {
		item["arguments"] = event.Arguments
	}
	if event.Output != "" {
		item["output"] = event.Output
	}
	if event.RawOutput != "" {
		item["raw_output"] = event.RawOutput
	}
	if event.Error != "" {
		item["error"] = event.Error
	}
	if event.DurationMS != 0 {
		item["duration_ms"] = event.DurationMS
	}
	if event.Message != nil {
		item["message"] = event.Message
	}
	if event.TurnUsage != nil {
		item["turn_usage"] = event.TurnUsage
	}
	if event.UsageSummary != nil {
		item["usage_summary"] = event.UsageSummary
	}
	if event.Title != "" {
		item["title"] = event.Title
	}
	if event.TitleStage != "" {
		item["title_stage"] = event.TitleStage
	}
	if event.Warning != "" {
		item["warning"] = event.Warning
	}
	if event.Branch != "" {
		item["branch"] = event.Branch
	}
	if event.Lifecycle != nil {
		item["lifecycle"] = event.Lifecycle
	}
	return item
}

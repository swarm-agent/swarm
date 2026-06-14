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
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
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
	if current, ok, err := s.sessions.GetLifecycle(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if ok && current.Active {
		if !strings.EqualFold(strings.TrimSpace(current.RunID), runID) {
			writeError(w, http.StatusConflict, runruntime.ErrSessionAlreadyActive)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":              true,
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          "accepted",
			"owner_transport": sessionV3ManualCompactOwnerTransport,
		})
		return
	}
	if active, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if ok {
		if !strings.EqualFold(strings.TrimSpace(active.RunID), runID) {
			writeError(w, http.StatusConflict, runruntime.ErrSessionAlreadyActive)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":              true,
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          "accepted",
			"owner_transport": sessionV3ManualCompactOwnerTransport,
			"run_intent":      active,
		})
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
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.startSessionV3ManualCompactExecution(sessionID, runID, req, principal)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"run_id":          runID,
		"status":          "accepted",
		"owner_transport": sessionV3ManualCompactOwnerTransport,
		"run_intent":      accepted.RunIntent,
		"realtime_outbox": accepted.RealtimeOutbox,
	})
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

func (s *Server) startSessionV3ManualCompactExecution(sessionID, runID string, req sessionsV3CompactRequest, principal identity.Principal) {
	if s == nil || s.runner == nil {
		return
	}
	s.beginActiveRun()
	go func() {
		defer s.endActiveRun()
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("manual compact panicked: %v", recovered)
				log.Printf("manual compact panic session_id=%s run_id=%s panic=%v", sessionID, runID, recovered)
				_ = s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, err)
			}
		}()

		runCtx, runCancel := context.WithCancel(s.runCtx)
		defer runCancel()
		if principal.Valid() {
			runCtx = identity.ContextWithPrincipal(runCtx, principal)
		}
		bridge := newSessionV3ManualCompactEventBridge(s, principal, sessionID, runID, runCancel)
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
			_ = s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, err)
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

	mu       sync.Mutex
	nextSeq  int
	firstErr error
}

func newSessionV3ManualCompactEventBridge(server *Server, principal identity.Principal, sessionID, runID string, cancel context.CancelFunc) *sessionV3ManualCompactEventBridge {
	return &sessionV3ManualCompactEventBridge{server: server, principal: principal, sessionID: strings.TrimSpace(sessionID), runID: strings.TrimSpace(runID), cancel: cancel}
}

func (b *sessionV3ManualCompactEventBridge) Err() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.firstErr
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
	_, err := b.server.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
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
	return err
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
		if lifecycle.Active {
			status = sessionruntime.RunIntentRunning
		} else {
			switch strings.ToLower(strings.TrimSpace(lifecycle.Phase)) {
			case "completed":
				status = sessionruntime.RunIntentCompleted
			case "cancelled":
				status = sessionruntime.RunIntentCancelled
			case "interrupted":
				status = sessionruntime.RunIntentInterrupted
			case "errored":
				status = sessionruntime.RunIntentFailed
			default:
				status = sessionruntime.RunIntentCompleted
			}
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

func (s *Server) publishSessionV3ManualCompactFailure(principal identity.Principal, sessionID, runID string, runErr error) error {
	message := "manual compact failed"
	if runErr != nil && strings.TrimSpace(runErr.Error()) != "" {
		message = strings.TrimSpace(runErr.Error())
	}
	_, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: fmt.Sprintf("manual-compact:%s:failure", strings.TrimSpace(runID)),
		EventType:       "session.run.failed",
		Status:          sessionruntime.RunIntentFailed,
		BlockedReason:   message,
		Payload: map[string]any{
			"type":       "session.run.failed",
			"session_id": sessionID,
			"run_id":     runID,
			"status":     sessionruntime.RunIntentFailed,
			"error":      message,
		},
	})
	return err
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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

type sessionV3ManualCompactResponseInput struct {
	SessionID          string
	RunID              string
	Status             string
	Summary            string
	CompactIndex       int
	CheckpointMessage  *pebblestore.MessageSnapshot
	CheckpointMutation *sessionruntime.SessionMutationResult
	TerminalMutation   *sessionruntime.SessionMutationResult
	TitleMutation      *sessionruntime.SessionMutationResult
	UsageMutation      *sessionruntime.SessionMutationResult
	ToolMutation       *sessionruntime.SessionMutationResult
	AssistantMutation  *sessionruntime.SessionMutationResult
}

func sessionV3CompactDirectResponse(input sessionV3ManualCompactResponseInput) map[string]any {
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = sessionruntime.RunIntentCompleted
	}
	var checkpointMutation any
	var checkpointOutbox any
	var checkpointMessage any
	var runIntent any
	var terminal any
	var titleMutation any
	var usageMutation any
	var toolMutation any
	var assistantMutation any
	checkpoint := map[string]any{"message_id": "", "event_seq": uint64(0), "primary_seq": uint64(0), "endpoint_seq": uint64(0), "endpoint_cursor": ""}
	if input.CheckpointMessage != nil {
		checkpointMessage = input.CheckpointMessage
		checkpoint["message_id"] = input.CheckpointMessage.ID
	}
	if input.CheckpointMutation != nil {
		checkpointMutation = sessionV3MutationResultResponse(*input.CheckpointMutation)
		checkpointOutbox = input.CheckpointMutation.RealtimeOutbox
		checkpoint["event_seq"] = input.CheckpointMutation.Event.Seq
		checkpoint["primary_seq"] = input.CheckpointMutation.PrimarySeq
		if input.CheckpointMutation.Message != nil {
			checkpoint["message_id"] = input.CheckpointMutation.Message.ID
		}
		if input.CheckpointMutation.RealtimeOutbox != nil {
			checkpoint["endpoint_seq"] = input.CheckpointMutation.RealtimeOutbox.EndpointSeq
			checkpoint["endpoint_cursor"] = input.CheckpointMutation.RealtimeOutbox.EndpointCursor
		}
	}
	if input.TerminalMutation != nil {
		terminal = sessionV3CompactTerminalResponse(input.TerminalMutation, status)
		if input.TerminalMutation.RunIntent != nil {
			runIntent = input.TerminalMutation.RunIntent
		}
	}
	if terminal == nil {
		terminal = map[string]any{"event_type": "session.lifecycle.updated", "phase": sessionV3CompactPhaseForStatus(status)}
	}
	if input.TitleMutation != nil {
		titleMutation = sessionV3MutationResultResponse(*input.TitleMutation)
	}
	if input.UsageMutation != nil {
		usageMutation = sessionV3MutationResultResponse(*input.UsageMutation)
	}
	if input.ToolMutation != nil {
		toolMutation = sessionV3MutationResultResponse(*input.ToolMutation)
	}
	if input.AssistantMutation != nil {
		assistantMutation = sessionV3MutationResultResponse(*input.AssistantMutation)
	}
	return map[string]any{
		"ok":                  true,
		"session_id":          input.SessionID,
		"run_id":              input.RunID,
		"status":              status,
		"run_intent":          runIntent,
		"compaction":          map[string]any{"run_id": input.RunID, "status": status, "owner_transport": sessionV3ManualCompactOwnerTransport, "summary": input.Summary, "compact_index": input.CompactIndex},
		"compact_checkpoint":  checkpoint,
		"checkpoint_message":  checkpointMessage,
		"checkpoint_mutation": checkpointMutation,
		"terminal":            terminal,
		"terminal_mutation":   mutationResponsePtr(input.TerminalMutation),
		"title_mutation":      titleMutation,
		"usage_mutation":      usageMutation,
		"tool_mutation":       toolMutation,
		"assistant_mutation":  assistantMutation,
		// Compatibility aliases for existing clients/tests. These now point at the
		// committed checkpoint append, not a transient lifecycle observation.
		"mutation":        checkpointMutation,
		"realtime_outbox": checkpointOutbox,
	}
}

func mutationResponsePtr(result *sessionruntime.SessionMutationResult) any {
	if result == nil {
		return nil
	}
	return sessionV3MutationResultResponse(*result)
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
		"ok":                false,
		"session_id":        sessionID,
		"run_id":            runID,
		"status":            status,
		"error":             message,
		"compaction":        map[string]any{"run_id": runID, "status": status, "owner_transport": sessionV3ManualCompactOwnerTransport},
		"terminal":          sessionV3CompactTerminalResponse(&terminal.Result, status),
		"terminal_mutation": sessionV3MutationResultResponse(terminal.Result),
		"realtime_outbox":   terminal.Result.RealtimeOutbox,
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
		if replay, replayOK, replayErr := s.sessionV3ManualCompactCheckpointFromStore(principal, sessionID, runID); replayErr != nil {
			writeError(w, http.StatusBadRequest, replayErr)
			return
		} else if replayOK {
			writeJSON(w, http.StatusAccepted, replay)
			return
		}
		writeError(w, http.StatusConflict, errors.New("manual compact is already active"))
		return
	} else if replay, replayOK, replayErr := s.sessionV3ManualCompactCheckpointFromStore(principal, sessionID, runID); replayErr != nil {
		writeError(w, http.StatusBadRequest, replayErr)
		return
	} else if replayOK {
		writeJSON(w, http.StatusAccepted, replay)
		return
	}

	compactRunner, ok := s.runner.(interface {
		RunManualCompaction(context.Context, string, runruntime.ManualCompactionInput) (runruntime.ManualCompactionResult, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("run service does not support direct manual compaction"))
		return
	}

	startingLifecycle := s.newSessionV3ManualCompactLifecycle(principal, sessionID, runID, true, "starting", "")
	accepted, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: clientRequestID + ":accepted",
		EventType:       "session.lifecycle.updated",
		Status:          sessionruntime.RunIntentPendingExecutor,
		Payload: map[string]any{
			"type":            "session.lifecycle.updated",
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          sessionruntime.RunIntentPendingExecutor,
			"owner_transport": sessionV3ManualCompactOwnerTransport,
			"lifecycle":       startingLifecycle,
		},
		Lifecycle: startingLifecycle,
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

	runningLifecycle := s.newSessionV3ManualCompactLifecycle(principal, sessionID, runID, true, "running", "")
	if _, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: clientRequestID + ":running",
		EventType:       "session.lifecycle.updated",
		Status:          sessionruntime.RunIntentRunning,
		Payload: map[string]any{
			"type":            "session.lifecycle.updated",
			"session_id":      sessionID,
			"run_id":          runID,
			"status":          sessionruntime.RunIntentRunning,
			"owner_transport": sessionV3ManualCompactOwnerTransport,
			"lifecycle":       runningLifecycle,
		},
		Lifecycle: runningLifecycle,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.beginActiveRun()
	result, compactErr := compactRunner.RunManualCompaction(r.Context(), sessionID, runruntime.ManualCompactionInput{
		RunID:                runID,
		Note:                 req.Note,
		Origin:               "manual",
		Principal:            principal,
		OwnerTransport:       sessionV3ManualCompactOwnerTransport,
		ApplySessionMutation: s.applySessionV3PrimaryMutation,
		IncludeAssistantAck:  true,
	})
	s.endActiveRun()
	if compactErr != nil {
		terminal, publishErr := s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, compactErr)
		if publishErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("manual compact failed and failure mutation failed: %w", publishErr))
			return
		}
		s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, terminal)
		return
	}
	if result.CheckpointMutation.Message == nil || result.CheckpointMutation.RealtimeOutbox == nil || result.CheckpointMutation.RealtimeOutbox.EndpointSeq == 0 || strings.TrimSpace(result.CheckpointMessage.ID) == "" {
		terminal, publishErr := s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, errors.New("manual compact did not return a committed checkpoint mutation"))
		if publishErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("manual compact checkpoint result invalid and failure mutation failed: %w", publishErr))
			return
		}
		s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, terminal)
		return
	}

	checkpointMutation := result.CheckpointMutation
	if signed, ok := s.sessionV3MutationWithHydrateCursor(principal, sessionID, checkpointMutation); ok {
		checkpointMutation = signed
	}
	completedLifecycle := s.newSessionV3ManualCompactLifecycle(principal, sessionID, runID, false, "completed", "")
	terminalMutation, err := s.recordSessionV3ManualCompactRunEvent(sessionV3ManualCompactRunEventInput{
		Principal:       principal,
		SessionID:       sessionID,
		RunID:           runID,
		ClientRequestID: clientRequestID + ":completed",
		EventType:       "session.lifecycle.updated",
		Status:          sessionruntime.RunIntentCompleted,
		Payload: map[string]any{
			"type":                  "session.lifecycle.updated",
			"session_id":            sessionID,
			"run_id":                runID,
			"status":                sessionruntime.RunIntentCompleted,
			"owner_transport":       sessionV3ManualCompactOwnerTransport,
			"compact_checkpoint_id": result.CheckpointMessage.ID,
			"compact_index":         result.CompactIndex,
			"lifecycle":             completedLifecycle,
		},
		Lifecycle: completedLifecycle,
	})
	if err != nil {
		terminal, publishErr := s.publishSessionV3ManualCompactFailure(principal, sessionID, runID, fmt.Errorf("manual compact completion mutation failed: %w", err))
		if publishErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("manual compact completion mutation failed and failure mutation failed: %w", publishErr))
			return
		}
		s.writeSessionV3ManualCompactTerminal(w, principal, sessionID, runID, terminal)
		return
	}
	writeJSON(w, http.StatusAccepted, sessionV3CompactDirectResponse(sessionV3ManualCompactResponseInput{
		SessionID:          sessionID,
		RunID:              runID,
		Status:             sessionruntime.RunIntentCompleted,
		Summary:            result.Summary,
		CompactIndex:       result.CompactIndex,
		CheckpointMessage:  &result.CheckpointMessage,
		CheckpointMutation: &checkpointMutation,
		TerminalMutation:   &terminalMutation,
		TitleMutation:      result.TitleMutation,
		UsageMutation:      result.UsageMutation,
		ToolMutation:       result.ToolMutation,
		AssistantMutation:  result.AssistantMutation,
	}))
}

func (s *Server) newSessionV3ManualCompactLifecycle(principal identity.Principal, sessionID, runID string, active bool, phase, errText string) *pebblestore.SessionLifecycleSnapshot {
	now := time.Now().UnixMilli()
	lifecycle := &pebblestore.SessionLifecycleSnapshot{SessionID: strings.TrimSpace(sessionID), UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, RunID: strings.TrimSpace(runID), Active: active, Phase: strings.TrimSpace(phase), UpdatedAt: now, Error: strings.TrimSpace(errText), OwnerTransport: sessionV3ManualCompactOwnerTransport}
	if active {
		lifecycle.StartedAt = now
	} else {
		lifecycle.EndedAt = now
	}
	if current, ok, err := s.sessions.GetLifecycle(sessionID); err == nil && ok && strings.EqualFold(strings.TrimSpace(current.RunID), strings.TrimSpace(runID)) {
		lifecycle.UserID = firstNonEmptyString(lifecycle.UserID, current.UserID)
		lifecycle.AccountScopeID = firstNonEmptyString(lifecycle.AccountScopeID, current.AccountScopeID)
		lifecycle.StartedAt = current.StartedAt
		lifecycle.Generation = current.Generation
	}
	return lifecycle
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
		if replay, ok, err := s.sessionV3ManualCompactCheckpointFromStore(principal, sessionID, runID); err == nil && ok {
			writeJSON(w, http.StatusAccepted, replay)
			return
		}
	}
	writeJSON(w, http.StatusInternalServerError, sessionV3CompactTerminalErrorResponse(sessionID, runID, terminal))
}

func (s *Server) sessionV3MutationWithHydrateCursor(principal identity.Principal, sessionID string, result sessionruntime.SessionMutationResult) (sessionruntime.SessionMutationResult, bool) {
	if s == nil || result.RealtimeOutbox == nil {
		return result, false
	}
	outbox := *result.RealtimeOutbox
	if outbox.EndpointSeq == 0 {
		return result, false
	}
	selector := sessionsV3SyncSelector{Kind: "session_ids", SessionIDs: canonicalV3SyncSessionIDs([]string{sessionID})}
	resources := sessionsV3SyncResourceSet(
		sessionsV3WorksetResources{Messages: true, RunIntents: true, CurrentRunState: true, SessionView: true},
		sessionsV3WorksetHistory{Mode: pebblestore.V3SyncSnapshotHistoryModeTail, MaxMessagesPerSession: 200, ManifestPolicy: "manifest"},
		true,
	)
	signed, err := s.signV3SyncEndpointCursor(v3SyncCursorScopeForSnapshot(principal, "desktop", "v3.sync.snapshot", selector, resources), outbox.EndpointSeq)
	if err != nil || strings.TrimSpace(signed) == "" {
		return result, false
	}
	outbox.EndpointCursor = signed
	result.RealtimeOutbox = &outbox
	return result, true
}

func (s *Server) sessionV3ManualCompactTerminalWithHydrateCursor(principal identity.Principal, sessionID string, terminal sessionV3ManualCompactTerminal) (sessionV3ManualCompactTerminal, bool) {
	result, ok := s.sessionV3MutationWithHydrateCursor(principal, sessionID, terminal.Result)
	if !ok {
		return terminal, false
	}
	terminal.Result = result
	return terminal, true
}

func (s *Server) sessionV3ManualCompactCheckpointFromStore(principal identity.Principal, sessionID, runID string) (map[string]any, bool, error) {
	intent, ok, err := s.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok || !sessionV3ManualCompactTerminalStatus(intent.Status) {
		return nil, false, err
	}
	if intent.Status != sessionruntime.RunIntentCompleted {
		terminal, terminalOK, terminalErr := s.sessionV3ManualCompactTerminalFromStore(principal, sessionID, runID)
		if terminalErr != nil || !terminalOK {
			return nil, terminalOK, terminalErr
		}
		return sessionV3CompactTerminalErrorResponse(sessionID, runID, terminal), true, nil
	}
	messages, err := s.sessions.ListSessionMessageTail(sessionID, 200)
	if err != nil {
		return nil, false, err
	}
	var checkpointMessage *pebblestore.MessageSnapshot
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.EqualFold(strings.TrimSpace(msg.Role), "system") && strings.HasPrefix(strings.TrimSpace(msg.Content), "[context-compact]") {
			msgCopy := msg
			checkpointMessage = &msgCopy
			break
		}
	}
	if checkpointMessage == nil || checkpointMessage.GlobalSeq == 0 {
		return nil, false, nil
	}
	records, err := s.sessions.ListRealtimeOutboxForSessionAfterSeq(sessionID, checkpointMessage.GlobalSeq-1, 1)
	if err != nil {
		return nil, false, err
	}
	if len(records) == 0 || records[0].Event.Seq != checkpointMessage.GlobalSeq {
		return nil, false, nil
	}
	checkpointMutation := sessionruntime.SessionMutationResult{SessionID: sessionID, PrimarySeq: records[0].Event.Seq, FirstSeq: records[0].Event.Seq, LastSeq: records[0].Event.Seq, EventIDs: []string{records[0].Event.ID}, Event: records[0].Event, Message: checkpointMessage, Projection: records[0].Projection, RealtimeOutbox: &records[0]}
	if signed, signedOK := s.sessionV3MutationWithHydrateCursor(principal, sessionID, checkpointMutation); signedOK {
		checkpointMutation = signed
	}
	terminal, terminalOK, err := s.sessionV3ManualCompactTerminalFromStore(principal, sessionID, runID)
	if err != nil {
		return nil, false, err
	}
	var terminalMutation *sessionruntime.SessionMutationResult
	if terminalOK {
		terminalMutation = &terminal.Result
	}
	return sessionV3CompactDirectResponse(sessionV3ManualCompactResponseInput{SessionID: sessionID, RunID: runID, Status: intent.Status, CheckpointMessage: checkpointMessage, CheckpointMutation: &checkpointMutation, TerminalMutation: terminalMutation}), true, nil
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
		blockedReason := ""
		if result.RunIntent != nil {
			blockedReason = strings.TrimSpace(result.RunIntent.BlockedReason)
		}
		terminal.Error = firstNonEmptyString(blockedReason, strings.TrimSpace(result.Lifecycle.Error), strings.TrimSpace(result.Lifecycle.StopReason), "manual compact "+status)
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
	intent := pebblestore.V3SessionRunIntent{SessionID: input.SessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, RunID: input.RunID, Status: input.Status, BlockedReason: input.BlockedReason, UpdatedAt: now}
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
	mutation := sessionruntime.SessionMutationInput{SessionID: input.SessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, ClientRequestID: input.ClientRequestID, IdempotencyKey: input.ClientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: input.EventType, EventPayload: raw, RunIntent: &intent, NowUnixMs: now}
	if input.Lifecycle != nil {
		mutation.Kind = sessionruntime.SessionMutationUpsertLifecycle
		mutation.Lifecycle = input.Lifecycle
	}
	return s.applySessionV3PrimaryMutation(mutation)
}

func (s *Server) publishSessionV3ManualCompactFailure(principal identity.Principal, sessionID, runID string, runErr error) (sessionV3ManualCompactTerminal, error) {
	message := "manual compact failed"
	if runErr != nil && strings.TrimSpace(runErr.Error()) != "" {
		message = strings.TrimSpace(runErr.Error())
	}
	lifecycle := s.newSessionV3ManualCompactLifecycle(principal, sessionID, runID, false, "errored", message)
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

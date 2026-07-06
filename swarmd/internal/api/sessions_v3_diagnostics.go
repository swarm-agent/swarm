package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionV3DiagnosticInput struct {
	SessionID      string
	UserID         string
	AccountScopeID string
	RunID          string
	Stage          string
	Source         string
	SequenceLabel  string
	Payload        any
	NowUnixMs      int64
	Force          bool
}

func (s *Server) appendSessionV3Diagnostic(input sessionV3DiagnosticInput) (sessionruntime.SessionMutationResult, error) {
	if !input.Force && !sessionV3DiagnosticsEnabled() {
		return sessionruntime.SessionMutationResult{}, nil
	}
	if s == nil || s.sessions == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("sessions v3 service is not configured")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.SessionID == "" {
		return sessionruntime.SessionMutationResult{}, errors.New("diagnostic session id is required")
	}
	stage := strings.TrimSpace(input.Stage)
	if stage == "" {
		stage = "session.diagnostic"
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "backend"
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	envelope := map[string]any{
		"diagnostic":  true,
		"session_id":  input.SessionID,
		"run_id":      strings.TrimSpace(input.RunID),
		"stage":       stage,
		"source":      source,
		"recorded_at": now,
		"payload":     input.Payload,
	}
	if sequence := strings.TrimSpace(input.SequenceLabel); sequence != "" {
		envelope["sequence_label"] = sequence
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, fmt.Errorf("marshal v3 diagnostic payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	payloadHash := hex.EncodeToString(sum[:])
	clientRequestID := sessionV3DiagnosticClientRequestID(stage, input.RunID, input.SequenceLabel, payloadHash)
	result, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       input.SessionID,
		UserID:          strings.TrimSpace(input.UserID),
		AccountScopeID:  strings.TrimSpace(input.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordDiagnostic,
		EventType:       stage,
		EventPayload:    raw,
		NowUnixMs:       now,
	})
	if err != nil {
		return result, err
	}
	if err := s.publishCommittedSessionV3MutationResult(result); err != nil {
		return result, err
	}
	return result, nil
}

func sessionV3DiagnosticClientRequestID(stage, runID, sequenceLabel, payloadHash string) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(stage))
	if label == "" {
		label = "session_diagnostic"
	}
	runID = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(runID))
	sequenceLabel = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(sequenceLabel))
	parts := []string{"v3-diagnostic", label}
	if runID != "" {
		parts = append(parts, runID)
	}
	if sequenceLabel != "" {
		parts = append(parts, sequenceLabel)
	}
	if len(payloadHash) > 16 {
		payloadHash = payloadHash[:16]
	}
	if payloadHash != "" {
		parts = append(parts, payloadHash)
	}
	return strings.Join(parts, "-")
}

func sessionV3DiagnosticsEnabled() bool {
	return os.Getenv("SWARM_V3_DIAGNOSTICS") == "1"
}

func sessionV3IsDiagnosticEventType(eventType string) bool {
	return strings.HasPrefix(strings.TrimSpace(eventType), "session.diagnostic")
}

func shouldRecordV3StoreDiagnostic(input sessionruntime.SessionMutationInput) bool {
	if !sessionV3DiagnosticsEnabled() {
		return false
	}
	if strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.Kind) == sessionruntime.SessionMutationRecordDiagnostic {
		return false
	}
	return true
}

func (s *Server) recordV3StoreInputDiagnostic(input sessionruntime.SessionMutationInput) {
	if !shouldRecordV3StoreDiagnostic(input) || strings.TrimSpace(input.Kind) == sessionruntime.SessionMutationCreateSession {
		return
	}
	runID := ""
	if input.RunIntent != nil {
		runID = input.RunIntent.RunID
	}
	if _, err := s.appendSessionV3Diagnostic(sessionV3DiagnosticInput{
		SessionID:      input.SessionID,
		UserID:         input.UserID,
		AccountScopeID: input.AccountScopeID,
		RunID:          runID,
		Stage:          "session.diagnostic.store.input",
		Source:         "backend.store",
		SequenceLabel:  sessionV3StoreDiagnosticSequence("input", input.Kind, input.ClientRequestID, time.Now().UnixNano()),
		Payload: map[string]any{
			"mutation_input": input,
		},
	}); err != nil {
		log.Printf("warning: failed to record v3 store input diagnostic session=%q kind=%q request=%q: %v", input.SessionID, input.Kind, input.ClientRequestID, err)
	}
}

func (s *Server) recordV3StoreResultDiagnostic(input sessionruntime.SessionMutationInput, result sessionruntime.SessionMutationResult, applyErr error) {
	if !shouldRecordV3StoreDiagnostic(input) {
		return
	}
	runID := ""
	if input.RunIntent != nil {
		runID = input.RunIntent.RunID
	}
	payload := map[string]any{
		"mutation_input":  input,
		"mutation_result": result,
		"summary": map[string]any{
			"session_id":        input.SessionID,
			"run_id":            runID,
			"kind":              input.Kind,
			"event_type":        input.EventType,
			"client_request_id": input.ClientRequestID,
			"idempotency_key":   input.IdempotencyKey,
			"payload_hash":      input.PayloadHash,
			"request_hash":      input.RequestHash,
		},
	}
	if len(input.EventPayload) > 0 {
		payload["incoming_event_payload"] = decodeSessionV3DiagnosticRawJSON(input.EventPayload)
	}
	if result.Conflict != nil {
		payload["idempotency_conflict"] = map[string]any{
			"code":                  result.Conflict.Code,
			"message":               result.Conflict.Message,
			"existing_payload_hash": result.Conflict.ExistingPayloadHash,
			"incoming_payload_hash": result.Conflict.IncomingPayloadHash,
			"client_request_id":     input.ClientRequestID,
			"kind":                  input.Kind,
			"event_type":            input.EventType,
		}
	}
	if applyErr != nil {
		payload["error"] = applyErr.Error()
	}
	if _, err := s.appendSessionV3Diagnostic(sessionV3DiagnosticInput{
		SessionID:      input.SessionID,
		UserID:         input.UserID,
		AccountScopeID: input.AccountScopeID,
		RunID:          runID,
		Stage:          "session.diagnostic.store.result",
		Source:         "backend.store",
		SequenceLabel:  sessionV3StoreDiagnosticSequence("result", input.Kind, input.ClientRequestID, int64(result.Event.Seq)),
		Payload:        payload,
	}); err != nil {
		log.Printf("warning: failed to record v3 store result diagnostic session=%q kind=%q request=%q: %v", input.SessionID, input.Kind, input.ClientRequestID, err)
	}
}

func decodeSessionV3DiagnosticRawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"decode_error": err.Error(), "raw": string(raw)}
	}
	return decoded
}

func sessionV3StoreDiagnosticSequence(prefix, kind, requestID string, discriminator int64) string {
	label := strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(kind))
	requestID = strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(requestID))
	return fmt.Sprintf("%s-%s-%s-%d", strings.TrimSpace(prefix), label, requestID, discriminator)
}

func (e *sessionV3Executor) recordSessionV3ProviderAPIDiagnostic(job sessionV3ExecutorJob, event providerdiagnostics.Event) {
	if e == nil || e.server == nil {
		return
	}
	stage := "session.diagnostic.provider.api"
	if eventStage := strings.TrimSpace(event.Stage); eventStage != "" {
		stage += "." + strings.NewReplacer(" ", "_", "/", "_", ":", "_", "-", "_").Replace(eventStage)
	}
	sequence := fmt.Sprintf("provider-api-%s-%s-%s-%d-%d", strings.TrimSpace(event.Provider), strings.TrimSpace(event.Operation), strings.TrimSpace(event.Stage), event.RecordedAt, time.Now().UnixNano())
	if _, err := e.server.appendSessionV3Diagnostic(sessionV3DiagnosticInput{
		SessionID:      job.SessionID,
		UserID:         job.Principal.UserID,
		AccountScopeID: job.Principal.AccountScopeID,
		RunID:          job.RunID,
		Stage:          stage,
		Source:         "backend.provider.api",
		SequenceLabel:  sequence,
		Payload:        event,
		NowUnixMs:      event.RecordedAt,
		Force:          true,
	}); err != nil {
		log.Printf("warning: failed to record provider api diagnostic session=%q run=%q provider=%q stage=%q: %v", job.SessionID, job.RunID, event.Provider, event.Stage, err)
	}
}

func (e *sessionV3Executor) recordSessionV3Diagnostic(job sessionV3ExecutorJob, stage, source, sequenceLabel string, payload any) {
	if e == nil || e.server == nil {
		return
	}
	if _, err := e.server.appendSessionV3Diagnostic(sessionV3DiagnosticInput{
		SessionID:      job.SessionID,
		UserID:         job.Principal.UserID,
		AccountScopeID: job.Principal.AccountScopeID,
		RunID:          job.RunID,
		Stage:          stage,
		Source:         source,
		SequenceLabel:  sequenceLabel,
		Payload:        payload,
	}); err != nil {
		log.Printf("warning: failed to record v3 diagnostic session=%q run=%q stage=%q sequence=%q: %v", job.SessionID, job.RunID, stage, sequenceLabel, err)
	}
}

func sessionV3ProviderResponseDiagnostic(response any, streamed string, flushCount int) map[string]any {
	return map[string]any{
		"response":    response,
		"streamed":    streamed,
		"flush_count": flushCount,
	}
}

func sessionV3MessageDiagnostic(message pebblestore.MessageSnapshot, response sessionV3AssistantResponse) map[string]any {
	return map[string]any{
		"message":  message,
		"response": response,
	}
}

func sessionV3DiagnosticErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sessionV3ProviderRequestDiagnostic(req provideriface.Request) map[string]any {
	inputCount, inputCharCount := sessionV3ProviderInputDiagnosticShape(req.Input)
	toolsCount := len(req.Tools)
	return map[string]any{
		"session_id":                        req.SessionID,
		"provider_lineage_id":               req.ProviderLineageID,
		"context_branch_id":                 req.ContextBranchID,
		"provider_cache_key_present":        strings.TrimSpace(req.ProviderCacheKey) != "",
		"session_affinity_key_present":      strings.TrimSpace(req.SessionAffinityKey) != "",
		"boundary_reason":                   req.BoundaryReason,
		"previous_provider_lineage_id":      req.PreviousProviderLineageID,
		"previous_provider":                 req.PreviousProviderID,
		"previous_model":                    req.PreviousModel,
		"new_provider":                      req.NewProviderID,
		"new_model":                         req.NewModel,
		"handoff_summary_message_id":        req.HandoffSummaryMessageID,
		"handoff_summary_global_seq":        req.HandoffSummaryGlobalSeq,
		"provider_lineage_start_message_id": req.ProviderLineageStartMessageID,
		"provider_lineage_start_run_id":     req.ProviderLineageStartRunID,
		"provider_lineage_start_global_seq": req.ProviderLineageStartGlobalSeq,
		"native_continuation_allowed":       req.NativeContinuationAllowed,
		"force_fresh_provider_context":      req.ForceFreshProviderContext,
		"model":                             req.Model,
		"thinking":                          req.Thinking,
		"instructions_present":              strings.TrimSpace(req.Instructions) != "",
		"input_items":                       inputCount,
		"input_text_chars":                  inputCharCount,
		"tools_count":                       toolsCount,
		"tool_choice":                       req.ToolChoice,
		"service_tier":                      req.ServiceTier,
		"context_mode":                      req.ContextMode,
		"context_window":                    req.ContextWindow,
		"parallel_tool_calls":               req.ParallelToolCalls,
		"workspace_path_present":            strings.TrimSpace(req.WorkspacePath) != "",
		"tool_invoker_present":              req.ToolInvoker != nil,
	}
}

func sessionV3ProviderInputDiagnosticShape(input []map[string]any) (int, int) {
	chars := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			chars += len([]rune(typed))
		case []map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(input)
	return len(input), chars
}

func sessionV3ProviderStreamEventDiagnostic(event provideriface.StreamEvent, step, index int) map[string]any {
	return map[string]any{
		"step":               step,
		"stream_index":       index,
		"type":               event.Type,
		"delta":              event.Delta,
		"phase":              event.Phase,
		"reasoning_key":      event.ReasoningKey,
		"tool_call_id":       event.ToolCallID,
		"tool_call_index":    event.ToolCallIndex,
		"tool_name":          event.ToolName,
		"arguments":          event.Arguments,
		"arguments_delta":    event.ArgumentsDelta,
		"arguments_snapshot": event.ArgumentsSnapshot,
		"metadata":           event.Metadata,
	}
}

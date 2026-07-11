package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ManualCompactionInput is the direct, non-streaming Sessions V3 manual compact
// entrypoint. Callers own canonical run-intent/lifecycle transitions and pass the
// canonical V3 mutation callback used for every committed artifact.
type ManualCompactionInput struct {
	RunID                string
	Note                 string
	Origin               string
	Principal            identity.Principal
	OwnerTransport       string
	ApplySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
	IncludeAssistantAck  bool
}

type ManualCompactionResult struct {
	Summary            string
	CompactIndex       int
	CheckpointMessage  pebblestore.MessageSnapshot
	CheckpointMutation sessionruntime.SessionMutationResult
	TitleMutation      *sessionruntime.SessionMutationResult
	UsageMutation      *sessionruntime.SessionMutationResult
	ToolMessage        *pebblestore.MessageSnapshot
	ToolMutation       *sessionruntime.SessionMutationResult
	AssistantMessage   *pebblestore.MessageSnapshot
	AssistantMutation  *sessionruntime.SessionMutationResult
	UsageSummary       *pebblestore.SessionUsageSummary
}

func (s *Service) RunManualCompaction(ctx context.Context, sessionID string, input ManualCompactionInput) (ManualCompactionResult, error) {
	if s == nil || s.sessions == nil {
		return ManualCompactionResult{}, errors.New("run service is not fully configured")
	}
	if input.ApplySessionMutation == nil {
		return ManualCompactionResult{}, errors.New("manual compact requires canonical session mutation callback")
	}
	sessionID = strings.TrimSpace(sessionID)
	runID := strings.TrimSpace(input.RunID)
	if sessionID == "" {
		return ManualCompactionResult{}, errors.New("session id is required")
	}
	if runID == "" {
		return ManualCompactionResult{}, errors.New("run id is required")
	}
	prompt := strings.TrimSpace(input.Note)
	if prompt == "" {
		prompt = "manual context compact request"
	}
	origin := normalizeContextCompactionOrigin(input.Origin)
	if strings.TrimSpace(input.Origin) == "" {
		origin = contextCompactionOriginManual
	}
	principal := input.Principal
	if principal.Valid() {
		ctx = identity.ContextWithPrincipal(ctx, principal)
	}

	sessionSnapshot, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return ManualCompactionResult{}, err
	}
	if !ok {
		return ManualCompactionResult{}, fmt.Errorf("session %q not found", sessionID)
	}
	if strings.TrimSpace(principal.UserID) == "" {
		principal.UserID = strings.TrimSpace(sessionSnapshot.UserID)
	}
	if strings.TrimSpace(principal.AccountScopeID) == "" {
		principal.AccountScopeID = strings.TrimSpace(sessionSnapshot.AccountScopeID)
	}

	resolvedPreference, err := s.resolveMainSessionPreference(sessionID)
	if err != nil {
		return ManualCompactionResult{}, err
	}
	providerID := strings.ToLower(strings.TrimSpace(resolvedPreference.Preference.Provider))
	if providerID == "" {
		return ManualCompactionResult{}, errors.New("resolved model provider is empty")
	}

	step := 1
	var compactionToolStream *memoryCompactionToolStream
	compactedSummary, compactErr := s.compactRunContextWithMemory(
		ctx,
		sessionID,
		runID,
		prompt,
		"",
		resolvedPreference.Preference,
		resolvedPreference.ContextWindow,
		resolvedPreference.MaxOutputTokens,
		true,
		origin,
		true,
		step,
		1,
		nil,
		&compactionToolStream,
	)
	if compactErr != nil {
		return ManualCompactionResult{}, fmt.Errorf("manual compact failed: %w", compactErr)
	}

	result := ManualCompactionResult{Summary: compactedSummary}
	if toolMessage, toolMutation, persistErr := s.persistMemoryCompactionToolMessageMutation(sessionID, compactionToolStream, runAppendMessageInput{RunID: runID, Step: step, LogicalKey: fmt.Sprintf("tool:%d:%s", step, strings.TrimSpace(compactionToolStream.CallID)), Principal: principal, ApplySessionMutation: input.ApplySessionMutation}); persistErr != nil {
		return ManualCompactionResult{}, persistErr
	} else if toolMessage != nil && toolMutation != nil {
		result.ToolMessage = toolMessage
		result.ToolMutation = toolMutation
	}

	activePlan, planErr := s.activePlanForCompaction(sessionID)
	if planErr != nil {
		return ManualCompactionResult{}, fmt.Errorf("manual compact active plan lookup failed: %w", planErr)
	}
	nextTitle, compactIndex := nextCompactSessionTitle(sessionSnapshot.Title)
	result.CompactIndex = compactIndex
	checkpoint := buildCompactionCheckpointMessage(compactedSummary, origin, compactIndex, compactedActivePlanLabel(activePlan))
	checkpointMetadata := contextCompactionCheckpointMetadata(activePlan, compactedSummary, origin, compactIndex)
	checkpointMessage, _, checkpointMutation, appendErr := s.appendRunMessageWithMutation(runAppendMessageInput{SessionID: sessionID, Role: "system", Content: checkpoint, Metadata: checkpointMetadata, RunID: runID, Step: step, LogicalKey: fmt.Sprintf("system:context_compaction:%d", compactIndex), Principal: principal, ApplySessionMutation: input.ApplySessionMutation})
	if appendErr != nil {
		return ManualCompactionResult{}, fmt.Errorf("manual compact checkpoint append failed: %w", appendErr)
	}
	if checkpointMutation == nil || checkpointMutation.Message == nil || checkpointMutation.RealtimeOutbox == nil || checkpointMutation.RealtimeOutbox.EndpointSeq == 0 {
		return ManualCompactionResult{}, errors.New("manual compact checkpoint mutation did not return committed realtime outbox")
	}
	result.CheckpointMessage = checkpointMessage
	result.CheckpointMutation = *checkpointMutation

	if titleMutation, titleErr := s.applyManualCompactionTitleMutation(sessionID, runID, nextTitle, principal, input.ApplySessionMutation); titleErr != nil {
		return ManualCompactionResult{}, titleErr
	} else if titleMutation != nil {
		result.TitleMutation = titleMutation
	}

	if usageMutation, usageErr := s.applyManualCompactionUsageMutation(sessionID, runID, resolvedPreference.ContextWindow, providerID, resolvedPreference.Preference.Model, principal, input.ApplySessionMutation); usageErr != nil {
		return ManualCompactionResult{}, usageErr
	} else if usageMutation != nil {
		result.UsageMutation = usageMutation
		if usageMutation.UsageSummary != nil {
			usageSummary := *usageMutation.UsageSummary
			result.UsageSummary = &usageSummary
		}
	}

	if input.IncludeAssistantAck {
		assistantText := buildManualCompactionAssistantText(compactedSummary, compactIndex, compactedActivePlanLabel(activePlan))
		assistantMessage, _, assistantMutation, appendErr := s.appendRunMessageWithMutation(runAppendMessageInput{SessionID: sessionID, Role: "assistant", Content: assistantText, Metadata: map[string]any{"source": "manual_context_compaction_ack"}, RunID: runID, Step: step, LogicalKey: "assistant:manual_context_compaction_ack", Principal: principal, ApplySessionMutation: input.ApplySessionMutation})
		if appendErr != nil {
			return ManualCompactionResult{}, fmt.Errorf("manual compact assistant ack append failed: %w", appendErr)
		}
		if assistantMutation == nil || assistantMutation.Message == nil || assistantMutation.RealtimeOutbox == nil || assistantMutation.RealtimeOutbox.EndpointSeq == 0 {
			return ManualCompactionResult{}, errors.New("manual compact assistant ack mutation did not return committed realtime outbox")
		}
		result.AssistantMessage = &assistantMessage
		result.AssistantMutation = assistantMutation
	}

	return result, nil
}

func (s *Service) appendRunMessageWithMutation(input runAppendMessageInput) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, *sessionruntime.SessionMutationResult, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("session service is not configured")
	}
	if input.ApplySessionMutation == nil {
		message, session, _, err := s.appendRunMessage(input)
		return message, session, nil, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	role := strings.ToLower(strings.TrimSpace(input.Role))
	content := strings.TrimSpace(input.Content)
	if sessionID == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("session id is required")
	}
	if !isRunMessageRoleAllowed(role) {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("invalid role %q", role)
	}
	if content == "" {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, errors.New("message content is required")
	}
	metadata := cloneGenericMap(input.Metadata)
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if !ok {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, fmt.Errorf("session %q not found", sessionID)
	}
	principal := input.Principal
	if strings.TrimSpace(principal.UserID) == "" {
		principal.UserID = strings.TrimSpace(session.UserID)
	}
	if strings.TrimSpace(principal.AccountScopeID) == "" {
		principal.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
	}
	now := time.Now().UnixMilli()
	logicalKey := strings.TrimSpace(input.LogicalKey)
	if logicalKey == "" {
		logicalKey = fmt.Sprintf("%s:%d", role, now)
	}
	runID := strings.TrimSpace(input.RunID)
	message := pebblestore.MessageSnapshot{
		ID:             runMessageV3ID(sessionID, runID, logicalKey, role),
		SessionID:      sessionID,
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		Role:           role,
		Content:        content,
		Metadata:       metadata,
		CreatedAt:      now,
	}
	payloadHash, err := runMessageV3PayloadHash(sessionID, runID, logicalKey, input.Step, role, content, metadata)
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	clientRequestID := runMessageV3ClientRequestID(sessionID, runID, logicalKey)
	mutation, err := input.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		NowUnixMs:       now,
	})
	if err != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, err
	}
	if mutation.Message != nil {
		message = *mutation.Message
	}
	if mutation.Session != nil {
		session = *mutation.Session
	} else if updated, found, getErr := s.sessions.GetSession(sessionID); getErr != nil {
		return pebblestore.MessageSnapshot{}, pebblestore.SessionSnapshot{}, nil, getErr
	} else if found {
		session = updated
	}
	return message, session, &mutation, nil
}

func (s *Service) persistMemoryCompactionToolMessageMutation(sessionID string, stream *memoryCompactionToolStream, appendInput runAppendMessageInput) (*pebblestore.MessageSnapshot, *sessionruntime.SessionMutationResult, error) {
	if s == nil || s.sessions == nil || stream == nil || !stream.Finalized {
		return nil, nil, nil
	}
	output := strings.TrimSpace(stream.Output)
	if output == "" {
		return nil, nil, nil
	}
	durationMS := int64(0)
	if !stream.StartedAt.IsZero() {
		durationMS = time.Since(stream.StartedAt).Milliseconds()
	}
	call := tool.Call{CallID: strings.TrimSpace(stream.CallID), Name: memoryCompactionToolName, Arguments: memoryCompactionToolArguments(stream.Origin, stream.Attempt)}
	toolResult := tool.Result{CallID: strings.TrimSpace(stream.CallID), Name: memoryCompactionToolName, Output: output, DurationMS: durationMS}
	appendInput.SessionID = sessionID
	appendInput.Role = "tool"
	appendInput.Content = formatToolHistory(call, toolResult)
	message, _, mutation, err := s.appendRunMessageWithMutation(appendInput)
	if err != nil {
		return nil, nil, err
	}
	return &message, mutation, nil
}

func (s *Service) applyManualCompactionTitleMutation(sessionID, runID, title string, principal identity.Principal, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (*sessionruntime.SessionMutationResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	current, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	now := time.Now().UnixMilli()
	current.Title = title
	current.UpdatedAt = now
	current.Metadata = cloneGenericMap(current.Metadata)
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "run_id": runID, "title": title, "stage": "compact", "updated_at": now, "session": current})
	if err != nil {
		return nil, err
	}
	payloadHash := manualCompactionPayloadHash("title", sessionID, runID, title, string(payload))
	clientRequestID := "manual-compact:" + strings.TrimSpace(runID) + ":title"
	mutation, err := apply(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationUpdateTitle, EventType: "session.title.updated", EventPayload: payload, Session: &current, NowUnixMs: now})
	if err != nil {
		return nil, fmt.Errorf("manual compact title update failed: %w", err)
	}
	return &mutation, nil
}

func (s *Service) applyManualCompactionUsageMutation(sessionID, runID string, contextWindow int, providerID, modelName string, principal identity.Principal, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (*sessionruntime.SessionMutationResult, error) {
	if contextWindow <= 0 {
		if usageState, hasUsage, usageErr := s.sessions.GetUsageSummary(sessionID); usageErr == nil && hasUsage && usageState.ContextWindow > 0 {
			contextWindow = usageState.ContextWindow
		}
	}
	if contextWindow < 0 {
		contextWindow = 0
	}
	now := time.Now().UnixMilli()
	usage := pebblestore.SessionTurnUsageSnapshot{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, RunID: runID, Provider: strings.ToLower(strings.TrimSpace(providerID)), Model: strings.TrimSpace(modelName), Source: contextCompactionUsageSource, ContextWindow: contextWindow, CreatedAt: now, UpdatedAt: now}
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "run_id": runID, "turn_usage": usage, "updated_at": now})
	if err != nil {
		return nil, err
	}
	payloadHash := manualCompactionPayloadHash("usage", sessionID, runID, string(payload))
	clientRequestID := "manual-compact:" + strings.TrimSpace(runID) + ":usage-reset"
	mutation, err := apply(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordUsage, EventType: "run.usage.updated", EventPayload: payload, TurnUsage: &usage, NowUnixMs: now})
	if err != nil {
		return nil, fmt.Errorf("manual compact usage reset failed: %w", err)
	}
	return &mutation, nil
}

func manualCompactionPayloadHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strings.TrimSpace(part)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

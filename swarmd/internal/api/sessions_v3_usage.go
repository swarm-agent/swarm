package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func sessionV3ProviderUsageRecord(providerID string, modelName string, contextWindow int, runID string, usage provideriface.TokenUsage) (pebblestore.SessionTurnUsageSnapshot, bool) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if !sessionV3ShouldTrackProviderUsage(providerID, usage) {
		return pebblestore.SessionTurnUsageSnapshot{}, false
	}
	return pebblestore.SessionTurnUsageSnapshot{
		RunID:            strings.TrimSpace(runID),
		Provider:         providerID,
		Model:            strings.TrimSpace(modelName),
		Source:           strings.TrimSpace(usage.Source),
		Transport:        strings.ToLower(strings.TrimSpace(usage.Transport)),
		ConnectedViaWS:   cloneSessionV3BoolPointer(usage.ConnectedViaWS),
		ContextWindow:    contextWindow,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		ThinkingTokens:   usage.ThinkingTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		TotalTokens:      usage.TotalTokens,
	}, true
}

func sessionV3ShouldTrackProviderUsage(providerID string, usage provideriface.TokenUsage) bool {
	if strings.TrimSpace(providerID) == "" {
		return false
	}
	return usage.TotalTokens > 0
}

func (e *sessionV3Executor) recordProviderUsage(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, providerID string, modelName string, usage provideriface.TokenUsage, now int64) (sessionruntime.SessionMutationResult, bool, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	turnUsage, ok := sessionV3ProviderUsageRecord(providerID, modelName, resolved.ContextWindow, job.RunID, usage)
	if !ok {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	payloadHash, err := sessionV3UsagePayloadHash(job.SessionID, job.RunID, turnUsage)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	clientRequestID := sessionV3ExecutorClientRequestID("run.usage.updated", job.RunID)
	result, err := e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordUsage,
		EventType:       "run.usage.updated",
		TurnUsage:       &turnUsage,
		NowUnixMs:       now,
	})
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	return result, true, nil
}

func sessionV3UsagePayloadHash(sessionID, runID string, usage pebblestore.SessionTurnUsageSnapshot) (string, error) {
	canonical := struct {
		Operation string                               `json:"operation"`
		SessionID string                               `json:"session_id"`
		RunID     string                               `json:"run_id"`
		Usage     pebblestore.SessionTurnUsageSnapshot `json:"usage"`
	}{
		Operation: "v3.executor.usage",
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Usage:     usage,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal v3 usage payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneSessionV3BoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

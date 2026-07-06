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

func sessionV3ProviderUsageRecord(providerID string, modelName string, contextWindow int, runID string, step int, usage provideriface.TokenUsage) (pebblestore.SessionTurnUsageSnapshot, bool) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if !sessionV3ShouldTrackProviderUsage(providerID, usage) {
		return pebblestore.SessionTurnUsageSnapshot{}, false
	}
	if step < 0 {
		step = 0
	}
	return pebblestore.SessionTurnUsageSnapshot{
		RunID:            sessionV3ProviderUsageRunID(runID, step),
		Provider:         providerID,
		Model:            strings.TrimSpace(modelName),
		Source:           strings.TrimSpace(usage.Source),
		Transport:        strings.ToLower(strings.TrimSpace(usage.Transport)),
		ConnectedViaWS:   cloneSessionV3BoolPointer(usage.ConnectedViaWS),
		ContextWindow:    contextWindow,
		Steps:            step,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		ThinkingTokens:   usage.ThinkingTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		TotalTokens:      usage.TotalTokens,
		ServiceTier:      strings.TrimSpace(usage.ServiceTier),
		EstimatedCostUSD: usage.EstimatedCostUSD,
		APIUsageRaw:      cloneSessionV3UsageMap(usage.APIUsageRaw),
		APIUsageRawPath:  strings.TrimSpace(usage.APIUsageRawPath),
		APIUsageHistory:  cloneSessionV3UsageHistory(usage.APIUsageHistory),
		APIUsagePaths:    append([]string(nil), usage.APIUsagePaths...),
	}, true
}

func sessionV3ProviderUsageRunID(runID string, step int) string {
	runID = strings.TrimSpace(runID)
	if step <= 0 {
		return runID
	}
	return fmt.Sprintf("%s/usage-step-%d", runID, step)
}

func sessionV3ShouldTrackProviderUsage(providerID string, usage provideriface.TokenUsage) bool {
	if usage.TotalTokens <= 0 {
		return false
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	source := strings.ToLower(strings.TrimSpace(usage.Source))
	rawPath := strings.TrimSpace(usage.APIUsageRawPath)
	switch providerID {
	case "codex":
		return source == "codex_api_usage" && rawPath == "response.usage"
	case "google":
		return source == "google_api_usage" && rawPath == "usageMetadata"
	case "copilot":
		return source == "copilot_session_usage" && rawPath == "session.usage_info"
	case "anthropic":
		return source == "anthropic_api_usage" && rawPath == "usage"
	case "fireworks":
		return source == "fireworks_api_usage" && rawPath == "usage"
	case "openrouter":
		return source == "openrouter_api_usage" && rawPath == "usage"
	default:
		return false
	}
}

func (e *sessionV3Executor) recordProviderUsage(job sessionV3ExecutorJob, resolved sessionV3ResolvedRuntime, providerID string, modelName string, step int, usage provideriface.TokenUsage, now int64) (sessionruntime.SessionMutationResult, bool, error) {
	if e == nil || e.server == nil {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	turnUsage, ok := sessionV3ProviderUsageRecord(providerID, modelName, resolved.ContextWindow, job.RunID, step, usage)
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
	clientRequestID := sessionV3ExecutorStepClientRequestID("run.usage.updated", job.RunID, step)
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

func cloneSessionV3UsageMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneSessionV3UsageHistory(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		out = append(out, cloneSessionV3UsageMap(item))
	}
	return out
}

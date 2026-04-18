package run

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const contextCompactionThresholdMetadataKey = "context_compaction_threshold_percent"

func sessionContextCompactionThresholdPercent(metadata map[string]any) (float64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[contextCompactionThresholdMetadataKey]
	if !ok || value == nil {
		return 0, false
	}
	threshold, ok := parseContextCompactionThresholdValue(value)
	if !ok {
		return 0, false
	}
	if threshold <= 0 {
		return 0, false
	}
	if threshold > 100 {
		threshold = 100
	}
	return threshold, true
}

func parseContextCompactionThresholdValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case string:
		raw := strings.TrimSpace(strings.TrimSuffix(typed, "%"))
		if raw == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func shouldAutoCompactForThreshold(summary pebblestore.SessionUsageSummary, thresholdPercent float64) bool {
	if thresholdPercent <= 0 || summary.ContextWindow <= 0 {
		return false
	}
	remaining := summary.RemainingTokens
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(summary.ContextWindow) {
		remaining = int64(summary.ContextWindow)
	}
	remainingPercent := (float64(remaining) * 100) / float64(summary.ContextWindow)
	return remainingPercent <= thresholdPercent
}

func remainingContextPercent(summary pebblestore.SessionUsageSummary) float64 {
	if summary.ContextWindow <= 0 {
		return 0
	}
	remaining := summary.RemainingTokens
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(summary.ContextWindow) {
		remaining = int64(summary.ContextWindow)
	}
	return (float64(remaining) * 100) / float64(summary.ContextWindow)
}

func compactedContinuationLead(origin string) string {
	switch strings.ToLower(strings.TrimSpace(origin)) {
	case "threshold":
		return "The previous conversation context was proactively compacted before the model hit its context limit."
	default:
		return "The previous conversation context exceeded the model context window and was compacted by the memory subagent."
	}
}

func formatThresholdCompactionStatus(summary pebblestore.SessionUsageSummary, thresholdPercent float64) string {
	return fmt.Sprintf(
		"remaining context %.1f%% is at or below the configured auto-compact threshold %.1f%%; compacting before the next provider step",
		remainingContextPercent(summary),
		thresholdPercent,
	)
}

func (s *Service) maybeAutoCompactRunContext(ctx context.Context, sessionID, runPrompt, providerID, modelName string, metadata map[string]any, preference pebblestore.ModelPreference, contextWindow, maxOutputTokens, step int, emit StreamHandler) ([]map[string]any, *pebblestore.SessionUsageSummary, []pebblestore.EventEnvelope, error) {
	if s == nil || s.sessions == nil {
		return nil, nil, nil, errors.New("run service is not fully configured")
	}
	thresholdPercent, ok := sessionContextCompactionThresholdPercent(metadata)
	if !ok {
		return nil, nil, nil, nil
	}
	usageSummary, hasUsage, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !hasUsage || !shouldAutoCompactForThreshold(usageSummary, thresholdPercent) {
		return nil, nil, nil, nil
	}

	emitMemoryCompactionStatus(emit, step, formatThresholdCompactionStatus(usageSummary, thresholdPercent))
	compactedSummary, compactErr := s.compactRunContextWithMemory(
		ctx,
		sessionID,
		runPrompt,
		"",
		preference,
		contextWindow,
		maxOutputTokens,
		false,
		step,
		1,
		emit,
	)
	if compactErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact failed: %w", compactErr)
	}
	resetSummary, _, compactEvents, compactErr := s.applyContextCompactionArtifacts(
		sessionID,
		compactedSummary,
		"threshold",
		contextWindow,
		providerID,
		modelName,
		step,
		emit,
	)
	if compactErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact bookkeeping failed: %w", compactErr)
	}
	var activePlan *pebblestore.SessionPlanSnapshot
	plan, ok, planErr := s.sessions.GetActivePlan(sessionID)
	if planErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact active plan lookup failed: %w", planErr)
	}
	if ok {
		activePlan = &plan
	}
	compactedInput := buildCompactedContinuationInput(runPrompt, compactedSummary, activePlan, "threshold")
	if len(compactedInput) == 0 {
		return nil, nil, nil, errors.New("threshold auto compact produced empty input")
	}
	return compactedInput, resetSummary, compactEvents, nil
}

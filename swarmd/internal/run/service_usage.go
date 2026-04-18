package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) newRunID() string {
	seq := s.runCounter.Add(1)
	return fmt.Sprintf("run_%d_%06d", time.Now().UnixMilli(), seq)
}

func mergeTokenUsage(acc, next provideriface.TokenUsage) provideriface.TokenUsage {
	source := strings.ToLower(strings.TrimSpace(next.Source))
	if source == "codex_api_usage" || source == "google_api_usage" || source == "fireworks_api_usage" || source == "copilot_session_usage" || source == "anthropic_api_usage" || source == "openrouter_api_usage" {
		if !hasConcreteUsageSnapshot(next) {
			return acc
		}
		return mergeSnapshotTokenUsage(acc, next)
	}

	acc.InputTokens += next.InputTokens
	acc.OutputTokens += next.OutputTokens
	acc.ThinkingTokens += next.ThinkingTokens
	acc.CacheReadTokens += next.CacheReadTokens
	acc.CacheWriteTokens += next.CacheWriteTokens
	acc.TotalTokens += next.TotalTokens
	if strings.TrimSpace(acc.Source) == "" && strings.TrimSpace(next.Source) != "" {
		acc.Source = strings.TrimSpace(next.Source)
	}
	if transport := strings.ToLower(strings.TrimSpace(next.Transport)); transport != "" {
		acc.Transport = transport
	}
	if next.ConnectedViaWS != nil {
		acc.ConnectedViaWS = cloneBoolPointer(next.ConnectedViaWS)
	}
	if len(next.APIUsageRaw) > 0 {
		acc.APIUsageRaw = nil
		acc.APIUsageRawPath = ""
	}
	if len(next.APIUsageHistory) > 0 {
		acc.APIUsageHistory = nil
	}
	if len(next.APIUsagePaths) > 0 {
		acc.APIUsagePaths = nil
	}
	return acc
}

func hasConcreteUsageSnapshot(next provideriface.TokenUsage) bool {
	return next.InputTokens > 0 ||
		next.OutputTokens > 0 ||
		next.ThinkingTokens > 0 ||
		next.CacheReadTokens > 0 ||
		next.CacheWriteTokens > 0 ||
		next.TotalTokens > 0 ||
		strings.TrimSpace(next.Transport) != "" ||
		next.ConnectedViaWS != nil
}

func mergeSnapshotTokenUsage(acc, next provideriface.TokenUsage) provideriface.TokenUsage {
	// Snapshot sources represent current API-reported usage for the latest provider response.
	// Keep counters as latest values (not additive) and append raw history for forensic traceability.
	acc.InputTokens = next.InputTokens
	acc.OutputTokens = next.OutputTokens
	acc.ThinkingTokens = next.ThinkingTokens
	acc.CacheReadTokens = next.CacheReadTokens
	acc.CacheWriteTokens = next.CacheWriteTokens
	acc.TotalTokens = next.TotalTokens
	if strings.TrimSpace(next.Source) != "" {
		acc.Source = strings.TrimSpace(next.Source)
	}
	if transport := strings.ToLower(strings.TrimSpace(next.Transport)); transport != "" {
		acc.Transport = transport
	}
	if next.ConnectedViaWS != nil {
		acc.ConnectedViaWS = cloneBoolPointer(next.ConnectedViaWS)
	}
	if len(next.APIUsageRaw) > 0 {
		acc.APIUsageRaw = nil
		acc.APIUsageRawPath = ""
	}
	if len(next.APIUsageHistory) > 0 {
		acc.APIUsageHistory = nil
	}
	if len(next.APIUsagePaths) > 0 {
		acc.APIUsagePaths = nil
	}
	return acc
}

func normalizeUsageSource(source string) string {
	source = strings.TrimSpace(source)
	return source
}

func shouldPersistProviderUsage(providerID string, usage provideriface.TokenUsage) bool {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	switch providerID {
	case "codex":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "codex_api_usage" {
			return false
		}
		return strings.TrimSpace(usage.Transport) != "" || usage.ConnectedViaWS != nil
	case "google":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "google_api_usage" {
			return false
		}
		return usage.InputTokens > 0 ||
			usage.OutputTokens > 0 ||
			usage.ThinkingTokens > 0 ||
			usage.TotalTokens > 0 ||
			usage.CacheReadTokens > 0 ||
			usage.CacheWriteTokens > 0
	case "copilot":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "copilot_session_usage" {
			return false
		}
		return usage.InputTokens > 0 ||
			usage.OutputTokens > 0 ||
			usage.TotalTokens > 0 ||
			usage.CacheReadTokens > 0 ||
			usage.CacheWriteTokens > 0
	case "anthropic":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "anthropic_api_usage" {
			return false
		}
		return usage.InputTokens > 0 ||
			usage.OutputTokens > 0 ||
			usage.TotalTokens > 0 ||
			usage.CacheReadTokens > 0 ||
			usage.CacheWriteTokens > 0
	case "fireworks":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "fireworks_api_usage" {
			return false
		}
		return usage.InputTokens > 0 ||
			usage.OutputTokens > 0 ||
			usage.TotalTokens > 0
	case "openrouter":
		source := strings.ToLower(strings.TrimSpace(usage.Source))
		if source != "openrouter_api_usage" {
			return false
		}
		return usage.InputTokens > 0 ||
			usage.OutputTokens > 0 ||
			usage.TotalTokens > 0
	default:
		return false
	}
}

func (s *Service) recordProviderUsageSnapshot(sessionID, runID, providerID, modelName string, contextWindow, stepsCompleted int, usage provideriface.TokenUsage) (pebblestore.SessionTurnUsageSnapshot, pebblestore.SessionUsageSummary, *pebblestore.EventEnvelope, error) {
	if s == nil || s.sessions == nil {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, errors.New("session service is not configured")
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return pebblestore.SessionTurnUsageSnapshot{}, pebblestore.SessionUsageSummary{}, nil, errors.New("provider id is required")
	}
	return s.sessions.RecordTurnUsage(sessionID, pebblestore.SessionTurnUsageSnapshot{
		RunID:            runID,
		Provider:         providerID,
		Model:            modelName,
		Source:           normalizeUsageSource(usage.Source),
		Transport:        strings.ToLower(strings.TrimSpace(usage.Transport)),
		ConnectedViaWS:   cloneBoolPointer(usage.ConnectedViaWS),
		ContextWindow:    contextWindow,
		Steps:            stepsCompleted,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		ThinkingTokens:   usage.ThinkingTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		TotalTokens:      usage.TotalTokens,
	})
}

func cloneUsageHistory(history []map[string]any) []map[string]any {
	if len(history) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(history))
	for _, sample := range history {
		if len(sample) == 0 {
			continue
		}
		out = append(out, cloneGenericMap(sample))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneUsagePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

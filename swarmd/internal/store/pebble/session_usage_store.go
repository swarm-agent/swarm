package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/privacy"

	"github.com/cockroachdb/pebble"
)

type SessionTurnUsageSnapshot struct {
	SessionID        string           `json:"session_id"`
	UserID           string           `json:"user_id,omitempty"`
	AccountScopeID   string           `json:"account_scope_id,omitempty"`
	RunID            string           `json:"run_id"`
	Provider         string           `json:"provider"`
	Model            string           `json:"model"`
	Source           string           `json:"source"`
	Transport        string           `json:"transport,omitempty"`
	ConnectedViaWS   *bool            `json:"connected_via_websocket,omitempty"`
	ContextWindow    int              `json:"context_window"`
	Steps            int              `json:"steps"`
	InputTokens      int64            `json:"input_tokens"`
	OutputTokens     int64            `json:"output_tokens"`
	ThinkingTokens   int64            `json:"thinking_tokens"`
	CacheReadTokens  int64            `json:"cache_read_tokens"`
	CacheWriteTokens int64            `json:"cache_write_tokens"`
	TotalTokens      int64            `json:"total_tokens"`
	ServiceTier      string           `json:"service_tier,omitempty"`
	EstimatedCostUSD float64          `json:"estimated_cost_usd,omitempty"`
	APIUsageRaw      map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath  string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory  []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths    []string         `json:"api_usage_paths,omitempty"`
	CreatedAt        int64            `json:"created_at"`
	UpdatedAt        int64            `json:"updated_at"`
}

type SessionUsageSummary struct {
	SessionID          string  `json:"session_id"`
	UserID             string  `json:"user_id,omitempty"`
	AccountScopeID     string  `json:"account_scope_id,omitempty"`
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	Source             string  `json:"source"`
	LastTransport      string  `json:"last_transport,omitempty"`
	LastConnectedViaWS *bool   `json:"last_connected_via_websocket,omitempty"`
	ContextWindow      int     `json:"context_window"`
	TurnCount          int     `json:"turn_count"`
	InputTokens        int64   `json:"input_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	ThinkingTokens     int64   `json:"thinking_tokens"`
	CacheReadTokens    int64   `json:"cache_read_tokens"`
	CacheWriteTokens   int64   `json:"cache_write_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	ServiceTier        string  `json:"service_tier,omitempty"`
	EstimatedCostUSD   float64 `json:"estimated_cost_usd,omitempty"`
	RemainingTokens    int64   `json:"remaining_tokens"`
	LastRunID          string  `json:"last_run_id"`
	UpdatedAt          int64   `json:"updated_at"`
}

func ApplyProviderUsageSnapshotToSummary(summary SessionUsageSummary, usage SessionTurnUsageSnapshot) SessionUsageSummary {
	// Provider usage counters describe the latest request's context occupancy.
	// Billing totals such as EstimatedCostUSD are accumulated separately by the
	// session service; summing repeated prompt snapshots corrupts remaining context.
	summary.InputTokens = clampUsageTokenCount(usage.InputTokens)
	summary.OutputTokens = clampUsageTokenCount(usage.OutputTokens)
	summary.ThinkingTokens = clampUsageTokenCount(usage.ThinkingTokens)
	summary.CacheReadTokens = clampUsageTokenCount(usage.CacheReadTokens)
	summary.CacheWriteTokens = clampUsageTokenCount(usage.CacheWriteTokens)
	summary.TotalTokens = clampUsageTokenCount(usage.TotalTokens)
	summary.ServiceTier = strings.ToLower(strings.TrimSpace(usage.ServiceTier))
	if summary.ContextWindow > 0 {
		remaining := int64(summary.ContextWindow) - summary.TotalTokens
		if remaining < 0 {
			remaining = 0
		}
		summary.RemainingTokens = remaining
	} else {
		summary.RemainingTokens = 0
	}
	return summary
}

func ApplyProviderUsageSnapshotReplacementToSummary(summary SessionUsageSummary, _, usage SessionTurnUsageSnapshot) SessionUsageSummary {
	return ApplyProviderUsageSnapshotToSummary(summary, usage)
}

func clampUsageTokenCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func (s *SessionStore) PutTurnUsage(record SessionTurnUsageSnapshot) error {
	record = sanitizeTurnUsageSnapshot(record)
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal turn usage %q/%q: %w", record.SessionID, record.RunID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySessionTurnUsage(record.SessionID, record.RunID)), payload, nil); err != nil {
		return err
	}
	if record.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionTurnUsageByAccount(record.AccountScopeID, record.SessionID, record.RunID)), []byte(record.RunID), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) GetTurnUsage(sessionID, runID string) (SessionTurnUsageSnapshot, bool, error) {
	var record SessionTurnUsageSnapshot
	ok, err := s.store.GetJSON(KeySessionTurnUsage(sessionID, runID), &record)
	if err != nil {
		return SessionTurnUsageSnapshot{}, false, err
	}
	if !ok {
		return SessionTurnUsageSnapshot{}, false, nil
	}
	return record, true, nil
}

func (s *SessionStore) ListTurnUsage(sessionID string, limit int) ([]SessionTurnUsageSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	const iterateAll = int(^uint(0) >> 1)
	out := make([]SessionTurnUsageSnapshot, 0, limit)
	err := s.store.IteratePrefix(SessionTurnUsagePrefix(sessionID), iterateAll, func(_ string, value []byte) error {
		var record SessionTurnUsageSnapshot
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.RunID) == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].RunID > out[j].RunID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SessionStore) PutUsageSummary(summary SessionUsageSummary) error {
	summary.UserID = strings.TrimSpace(summary.UserID)
	summary.AccountScopeID = strings.TrimSpace(summary.AccountScopeID)
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal usage summary %q: %w", summary.SessionID, err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySessionUsageSummary(summary.SessionID)), payload, nil); err != nil {
		return err
	}
	if summary.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionUsageSummaryByAccount(summary.AccountScopeID, summary.SessionID)), payload, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) GetUsageSummary(sessionID string) (SessionUsageSummary, bool, error) {
	var summary SessionUsageSummary
	ok, err := s.store.GetJSON(KeySessionUsageSummary(sessionID), &summary)
	if err != nil {
		return SessionUsageSummary{}, false, err
	}
	if !ok {
		return SessionUsageSummary{}, false, nil
	}
	return summary, true, nil
}

func (s *SessionStore) ResetUsage(sessionID string, summary SessionUsageSummary) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}

	keys := make([]string, 0, 64)
	const iterateAll = int(^uint(0) >> 1)
	if err := s.store.IteratePrefix(SessionTurnUsagePrefix(sessionID), iterateAll, func(key string, _ []byte) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		return err
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return fmt.Errorf("delete turn usage key %q: %w", key, err)
		}
	}

	summaryKey := KeySessionUsageSummary(sessionID)
	summary.SessionID = sessionID
	summary.UserID = strings.TrimSpace(summary.UserID)
	summary.AccountScopeID = strings.TrimSpace(summary.AccountScopeID)
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal usage summary reset payload: %w", err)
	}
	if err := batch.Set([]byte(summaryKey), payload, nil); err != nil {
		return fmt.Errorf("set usage summary reset key %q: %w", summaryKey, err)
	}
	if summary.AccountScopeID != "" {
		accountKey := KeySessionUsageSummaryByAccount(summary.AccountScopeID, sessionID)
		if err := batch.Set([]byte(accountKey), payload, nil); err != nil {
			return fmt.Errorf("set usage summary account key %q: %w", accountKey, err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit usage reset batch: %w", err)
	}
	return nil
}

func sanitizeUsageHistory(history []map[string]any) []map[string]any {
	if len(history) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(history))
	for _, sample := range history {
		if len(sample) == 0 {
			continue
		}
		out = append(out, privacy.SanitizeMap(sample))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeUsagePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = privacy.SanitizeText(path)
		if strings.TrimSpace(path) != "" {
			out = append(out, path)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeTurnUsageSnapshot(record SessionTurnUsageSnapshot) SessionTurnUsageSnapshot {
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.RunID = strings.TrimSpace(record.RunID)
	record.APIUsageRaw = privacy.SanitizeMap(record.APIUsageRaw)
	record.APIUsageRawPath = privacy.SanitizeText(record.APIUsageRawPath)
	record.APIUsageHistory = sanitizeUsageHistory(record.APIUsageHistory)
	record.APIUsagePaths = sanitizeUsagePaths(record.APIUsagePaths)
	record.Source = privacy.SanitizeText(record.Source)
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	record.ServiceTier = strings.ToLower(strings.TrimSpace(record.ServiceTier))
	if record.EstimatedCostUSD < 0 {
		record.EstimatedCostUSD = 0
	}
	return record
}

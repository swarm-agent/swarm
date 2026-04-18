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
	APIUsageRaw      map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath  string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory  []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths    []string         `json:"api_usage_paths,omitempty"`
	CreatedAt        int64            `json:"created_at"`
	UpdatedAt        int64            `json:"updated_at"`
}

type SessionUsageSummary struct {
	SessionID          string `json:"session_id"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Source             string `json:"source"`
	LastTransport      string `json:"last_transport,omitempty"`
	LastConnectedViaWS *bool  `json:"last_connected_via_websocket,omitempty"`
	ContextWindow      int    `json:"context_window"`
	TurnCount          int    `json:"turn_count"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	ThinkingTokens     int64  `json:"thinking_tokens"`
	CacheReadTokens    int64  `json:"cache_read_tokens"`
	CacheWriteTokens   int64  `json:"cache_write_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	RemainingTokens    int64  `json:"remaining_tokens"`
	LastRunID          string `json:"last_run_id"`
	UpdatedAt          int64  `json:"updated_at"`
}

func (s *SessionStore) PutTurnUsage(record SessionTurnUsageSnapshot) error {
	record = sanitizeTurnUsageSnapshot(record)
	return s.store.PutJSON(KeySessionTurnUsage(record.SessionID, record.RunID), record)
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
	return s.store.PutJSON(KeySessionUsageSummary(summary.SessionID), summary)
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
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal usage summary reset payload: %w", err)
	}
	if err := batch.Set([]byte(summaryKey), payload, nil); err != nil {
		return fmt.Errorf("set usage summary reset key %q: %w", summaryKey, err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit usage reset batch: %w", err)
	}
	return nil
}

func sanitizeTurnUsageSnapshot(record SessionTurnUsageSnapshot) SessionTurnUsageSnapshot {
	record.APIUsageRaw = nil
	record.APIUsageRawPath = ""
	record.APIUsageHistory = nil
	record.APIUsagePaths = nil
	record.Source = privacy.SanitizeText(record.Source)
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	return record
}

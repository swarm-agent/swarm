package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/privacy"

	"github.com/cockroachdb/pebble"
)

type SessionTurnUsageSnapshot struct {
	SessionID            string           `json:"session_id"`
	UserID               string           `json:"user_id,omitempty"`
	AccountScopeID       string           `json:"account_scope_id,omitempty"`
	RunID                string           `json:"run_id"`
	Provider             string           `json:"provider"`
	Model                string           `json:"model"`
	Source               string           `json:"source"`
	Transport            string           `json:"transport,omitempty"`
	ConnectedViaWS       *bool            `json:"connected_via_websocket,omitempty"`
	ContextWindow        int              `json:"context_window"`
	Steps                int              `json:"steps"`
	InputTokens          int64            `json:"input_tokens"`
	OutputTokens         int64            `json:"output_tokens"`
	ThinkingTokens       int64            `json:"thinking_tokens"`
	CacheReadTokens      int64            `json:"cache_read_tokens"`
	CacheWriteTokens     int64            `json:"cache_write_tokens"`
	TotalTokens          int64            `json:"total_tokens"`
	BilledTokens         int64            `json:"billed_tokens,omitempty"`
	RequestedServiceTier string           `json:"requested_service_tier,omitempty"`
	ServiceTier          string           `json:"service_tier,omitempty"`
	ServiceTierStatus    string           `json:"service_tier_status,omitempty"`
	EstimatedCostUSD     float64          `json:"estimated_cost_usd,omitempty"`
	APIUsageRaw          map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath      string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory      []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths        []string         `json:"api_usage_paths,omitempty"`
	CreatedAt            int64            `json:"created_at"`
	UpdatedAt            int64            `json:"updated_at"`
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

type SessionMediaUsageRecord struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	AccountScopeID  string  `json:"account_scope_id"`
	UserID          string  `json:"user_id,omitempty"`
	MediaType       string  `json:"media_type"`
	Kind            string  `json:"kind"` // "image", "video", "audio"
	Provider        string  `json:"provider,omitempty"`
	Model           string  `json:"model,omitempty"`
	Filename        string  `json:"filename"`
	Label           string  `json:"label"`
	Size            int64   `json:"size"`
	CostUSD         float64 `json:"cost_usd"`
	PriceStatus     string  `json:"price_status,omitempty"`     // "known", "unknown", "subscription", "free"
	PricingSummary  string  `json:"pricing_summary,omitempty"`  // human-scannable pricing provenance
	SnapshotID      string  `json:"snapshot_id,omitempty"`      // catalog snapshot ID
	SnapshotVersion string  `json:"snapshot_version,omitempty"` // catalog snapshot version
	CreatedAt       int64   `json:"created_at"`
}

func ApplyProviderUsageSnapshotToSummary(summary SessionUsageSummary, usage SessionTurnUsageSnapshot) SessionUsageSummary {
	// Provider usage counters describe the latest request's context occupancy.
	// Billing totals such as EstimatedCostUSD are accumulated separately by the
	// session service; summing repeated prompt snapshots corrupts remaining context.
	if strings.EqualFold(usage.Source, "router") || strings.EqualFold(usage.Source, "compaction") {
		return summary
	}
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
	if record.SessionID == "" {
		return errors.New("turn usage session_id is required")
	}
	if record.RunID == "" {
		return errors.New("turn usage run_id is required")
	}
	if record.EstimatedCostUSD <= 0 && !strings.EqualFold(record.Provider, "codex") {
		record.EstimatedCostUSD = s.CalculateCost(record.Provider, record.Model, record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.ThinkingTokens)
	}

	unlockSession := s.store.sessionMutations.lockSessions(record.SessionID)
	defer unlockSession()

	previous, hadPrevious, err := s.GetTurnUsage(record.SessionID, record.RunID)
	if err != nil {
		return fmt.Errorf("read previous turn usage: %w", err)
	}

	deltaCost := record.EstimatedCostUSD
	deltaTokens := record.TotalTokens
	if record.BilledTokens > 0 {
		deltaTokens = record.BilledTokens
	}
	if hadPrevious {
		deltaCost = record.EstimatedCostUSD - previous.EstimatedCostUSD
		if deltaCost < 0 {
			deltaCost = 0
		}
		prevTokens := previous.TotalTokens
		if previous.BilledTokens > 0 {
			prevTokens = previous.BilledTokens
		}
		currTokens := record.TotalTokens
		if record.BilledTokens > 0 {
			currTokens = record.BilledTokens
		}
		deltaTokens = currTokens - prevTokens
		if deltaTokens < 0 {
			deltaTokens = 0
		}
	}

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

	ts := record.CreatedAt
	if ts <= 0 {
		ts = record.UpdatedAt
	}
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}
	dateStr := time.UnixMilli(ts).UTC().Format("2006-01-02")
	if deltaCost > 0 || deltaTokens > 0 || !hadPrevious {
		acc, _, err := s.GetDailyUsageAccumulator(record.AccountScopeID, dateStr)
		if err != nil {
			return fmt.Errorf("get daily usage accumulator: %w", err)
		}
		if acc.AccountScopeID == "" {
			acc.AccountScopeID = record.AccountScopeID
			acc.Date = dateStr
		}
		acc.TotalCostUSD += deltaCost
		acc.TotalTokens += deltaTokens
		acc.InputTokens += clampUsageTokenCount(record.InputTokens)
		acc.OutputTokens += clampUsageTokenCount(record.OutputTokens)
		acc.CachedTokens += clampUsageTokenCount(record.CacheReadTokens)
		acc.ThinkingTokens += clampUsageTokenCount(record.ThinkingTokens)
		if !hadPrevious {
			acc.TurnCount++
		}
		if acc.ModelsUsed == nil {
			acc.ModelsUsed = make(map[string]int64)
		}
		if record.Model != "" {
			acc.ModelsUsed[record.Model] += deltaTokens
		}
		acc.UpdatedAt = time.Now().UnixMilli()

		accPayload, err := json.Marshal(acc)
		if err != nil {
			return fmt.Errorf("marshal daily accumulator: %w", err)
		}
		if err := batch.Set([]byte(KeyDailyUsageAccumulator(acc.AccountScopeID, acc.Date)), accPayload, nil); err != nil {
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

func (s *SessionStore) ListAllTurnUsage(accountScopeID string, limit int) ([]SessionTurnUsageSnapshot, error) {
	if limit <= 0 {
		limit = 5000
	}
	const iterateAll = int(^uint(0) >> 1)
	out := make([]SessionTurnUsageSnapshot, 0, 128)
	accountScopeID = strings.TrimSpace(accountScopeID)
	err := s.store.IteratePrefix("session_turn_usage/", iterateAll, func(_ string, value []byte) error {
		var record SessionTurnUsageSnapshot
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.RunID) == "" {
			return nil
		}
		if accountScopeID != "" && strings.TrimSpace(record.AccountScopeID) != "" && strings.TrimSpace(record.AccountScopeID) != accountScopeID {
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

func (s *SessionStore) ListAllUsageSummaries(accountScopeID string, limit int) ([]SessionUsageSummary, error) {
	if limit <= 0 {
		limit = 500
	}
	const iterateAll = int(^uint(0) >> 1)
	out := make([]SessionUsageSummary, 0, 64)
	accountScopeID = strings.TrimSpace(accountScopeID)
	err := s.store.IteratePrefix("session_usage_summary/", iterateAll, func(_ string, value []byte) error {
		var summary SessionUsageSummary
		if err := json.Unmarshal(value, &summary); err != nil {
			return err
		}
		if strings.TrimSpace(summary.SessionID) == "" {
			return nil
		}
		if accountScopeID != "" && strings.TrimSpace(summary.AccountScopeID) != "" && strings.TrimSpace(summary.AccountScopeID) != accountScopeID {
			return nil
		}
		out = append(out, summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
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
	record.RequestedServiceTier = strings.ToLower(strings.TrimSpace(record.RequestedServiceTier))
	record.ServiceTier = strings.ToLower(strings.TrimSpace(record.ServiceTier))
	record.ServiceTierStatus = strings.ToLower(strings.TrimSpace(record.ServiceTierStatus))
	if record.EstimatedCostUSD < 0 {
		record.EstimatedCostUSD = 0
	}
	return record
}

func (s *SessionStore) PutMediaUsage(rec SessionMediaUsageRecord) error {
	if s == nil || s.store == nil {
		return errors.New("store is not configured")
	}
	rec.ID = strings.TrimSpace(rec.ID)
	if rec.ID == "" {
		return errors.New("media usage id is required")
	}
	rec.SessionID = strings.TrimSpace(rec.SessionID)
	if rec.SessionID == "" {
		return errors.New("media usage session_id is required")
	}
	rec.AccountScopeID = strings.TrimSpace(rec.AccountScopeID)
	if rec.AccountScopeID == "" {
		rec.AccountScopeID = "default"
	}

	// Validate session and account scope ownership
	session, ok, err := s.GetSession(rec.SessionID)
	if err != nil {
		return fmt.Errorf("verify session %q: %w", rec.SessionID, err)
	}
	if !ok {
		return fmt.Errorf("session %q not found", rec.SessionID)
	}
	if strings.TrimSpace(session.AccountScopeID) != "" && rec.AccountScopeID != strings.TrimSpace(session.AccountScopeID) {
		return fmt.Errorf("account scope mismatch: record account %q does not match session account %q", rec.AccountScopeID, session.AccountScopeID)
	}

	// Serialized session locking ensures atomicity
	unlock := s.store.sessionMutations.lockSessions(rec.SessionID)
	defer unlock()

	key := KeySessionMediaUsage(rec.AccountScopeID, rec.ID)
	// Check if already persisted to ensure idempotent retry does not double-count
	var existing SessionMediaUsageRecord
	ok, err = s.store.GetJSON(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	if rec.CreatedAt <= 0 {
		rec.CreatedAt = time.Now().UnixMilli()
	}

	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal media usage %q: %w", rec.ID, err)
	}

	dateStr := time.UnixMilli(rec.CreatedAt).UTC().Format("2006-01-02")
	acc, _, err := s.GetDailyUsageAccumulator(rec.AccountScopeID, dateStr)
	if err != nil {
		return fmt.Errorf("get daily usage accumulator: %w", err)
	}
	if acc.AccountScopeID == "" {
		acc.AccountScopeID = rec.AccountScopeID
		acc.Date = dateStr
	}
	acc.TotalCostUSD += rec.CostUSD
	acc.MediaCostUSD += rec.CostUSD
	acc.MediaCalls++
	switch strings.ToLower(rec.Kind) {
	case "image":
		acc.ImageCount++
		acc.ImageCostUSD += rec.CostUSD
	case "video":
		acc.VideoCount++
		acc.VideoCostUSD += rec.CostUSD
	case "audio":
		acc.AudioCount++
		acc.AudioCostUSD += rec.CostUSD
	}
	acc.UpdatedAt = time.Now().UnixMilli()
	accPayload, err := json.Marshal(acc)
	if err != nil {
		return fmt.Errorf("marshal daily accumulator: %w", err)
	}

	summary, found, err := s.GetUsageSummary(rec.SessionID)
	if err != nil {
		return fmt.Errorf("get usage summary: %w", err)
	}
	if !found {
		summary = SessionUsageSummary{
			SessionID:      rec.SessionID,
			AccountScopeID: rec.AccountScopeID,
			UserID:         rec.UserID,
		}
	}
	summary.EstimatedCostUSD += rec.CostUSD
	summary.UpdatedAt = time.Now().UnixMilli()
	summaryPayload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal usage summary: %w", err)
	}

	// Single atomic Pebble batch for record, session index, daily accumulator, and usage summary
	batch := s.store.NewBatch()
	defer batch.Close()

	if err := batch.Set([]byte(key), payload, nil); err != nil {
		return err
	}
	sessionMediaKey := KeySessionMediaUsageBySession(rec.SessionID, rec.ID)
	if err := batch.Set([]byte(sessionMediaKey), payload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyDailyUsageAccumulator(acc.AccountScopeID, acc.Date)), accPayload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeySessionUsageSummary(summary.SessionID)), summaryPayload, nil); err != nil {
		return err
	}
	if summary.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionUsageSummaryByAccount(summary.AccountScopeID, summary.SessionID)), summaryPayload, nil); err != nil {
			return err
		}
	}

	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) ListMediaUsageBySession(sessionID string, limit int) ([]SessionMediaUsageRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("store is not configured")
	}
	if limit <= 0 {
		limit = 2000
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	prefix := SessionMediaUsageBySessionPrefix(sessionID)
	out := make([]SessionMediaUsageRecord, 0, 32)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(prefix, iterateAll, func(_ string, value []byte) error {
		var rec SessionMediaUsageRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return err
		}
		if rec.ID == "" {
			return nil
		}
		out = append(out, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SessionStore) ListMediaUsage(accountScopeID string, limit int) ([]SessionMediaUsageRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("store is not configured")
	}
	if limit <= 0 {
		limit = 2000
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		accountScopeID = "default"
	}
	prefix := SessionMediaUsagePrefix(accountScopeID)
	out := make([]SessionMediaUsageRecord, 0, 64)
	const iterateAll = int(^uint(0) >> 1)
	err := s.store.IteratePrefix(prefix, iterateAll, func(_ string, value []byte) error {
		var rec SessionMediaUsageRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return err
		}
		if rec.ID == "" {
			return nil
		}
		out = append(out, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

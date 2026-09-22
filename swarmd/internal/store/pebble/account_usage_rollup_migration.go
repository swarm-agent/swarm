package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	accountUsageRollupMigrationVersion = 1
	accountUsageRollupMigrationKey     = "meta/migrations/account_usage_rollup/v1"
)

// AccountUsageRollupMigrationResult describes the result of the historical usage rollup backfill.
type AccountUsageRollupMigrationResult struct {
	Version          int  `json:"version"`
	Applied          bool `json:"applied"`
	AlreadyApplied   bool `json:"already_applied"`
	TurnsScanned     int  `json:"turns_scanned"`
	MediaScanned     int  `json:"media_scanned"`
	RollupsCommitted int  `json:"rollups_committed"`
}

type accountUsageRollupMigrationMarker struct {
	Version          int   `json:"version"`
	TurnsScanned     int   `json:"turns_scanned"`
	MediaScanned     int   `json:"media_scanned"`
	RollupsCommitted int   `json:"rollups_committed"`
	AppliedAt        int64 `json:"applied_at"`
}

// RunAccountUsageRollupBackfillMigration backfills AccountUsageRollup records
// from historical SessionTurnUsageSnapshot and SessionMediaUsageRecord entries.
func RunAccountUsageRollupBackfillMigration(store *Store) (AccountUsageRollupMigrationResult, error) {
	result := AccountUsageRollupMigrationResult{Version: accountUsageRollupMigrationVersion}
	if store == nil || store.db == nil {
		return result, errors.New("store is not configured")
	}

	if payload, found, err := store.GetBytes(accountUsageRollupMigrationKey); err != nil {
		return result, fmt.Errorf("read account usage rollup migration marker: %w", err)
	} else if found {
		var marker accountUsageRollupMigrationMarker
		if err := decodeStrictJSON(payload, &marker); err != nil {
			return result, fmt.Errorf("decode account usage rollup migration marker: %w", err)
		}
		if marker.Version >= accountUsageRollupMigrationVersion {
			result.AlreadyApplied = true
			result.TurnsScanned = marker.TurnsScanned
			result.MediaScanned = marker.MediaScanned
			result.RollupsCommitted = marker.RollupsCommitted
			return result, nil
		}
	}

	rollups := make(map[string]*AccountUsageRollup)
	sessionStore := NewSessionStore(store)
	now := time.Now().UnixMilli()

	const iterateAll = int(^uint(0) >> 1)
	err := store.IteratePrefix("session_turn_usage/", iterateAll, func(_ string, value []byte) error {
		var rec SessionTurnUsageSnapshot
		if err := json.Unmarshal(value, &rec); err != nil {
			return nil
		}
		sessID := strings.TrimSpace(rec.SessionID)
		if sessID == "" {
			return nil
		}
		result.TurnsScanned++

		acctID := strings.TrimSpace(rec.AccountScopeID)
		if acctID == "" {
			acctID = "default"
		}
		provID := strings.ToLower(strings.TrimSpace(rec.Provider))
		if provID == "" {
			provID = "unknown"
		}
		modelID := strings.TrimSpace(rec.Model)
		if modelID == "" {
			modelID = "unknown"
		}
		ts := rec.CreatedAt
		if ts <= 0 {
			ts = rec.UpdatedAt
		}
		if ts <= 0 {
			ts = now
		}
		dateStr := time.UnixMilli(ts).UTC().Format("2006-01-02")

		key := KeyAccountUsageRollup(acctID, dateStr, sessID, provID, modelID)
		r, exists := rollups[key]
		if !exists {
			r = &AccountUsageRollup{
				AccountScopeID: acctID,
				Date:           dateStr,
				SessionID:      sessID,
				Provider:       provID,
				Model:          modelID,
			}
			rollups[key] = r
		}

		tot, in, out, cache, _, think := billedComponents(rec)
		cost, status := sessionStore.CalculateCostWithStatus(provID, modelID, in, out, cache, think)
		codexNominal := 0.0
		if provID == "codex" {
			codexNominal = CalculateBaselineCost("openai", modelID, in, out, cache, think)
			cost = 0.0
		} else if status != "known" || cost <= 0 {
			baseCost := CalculateBaselineCost(provID, modelID, in, out, cache, think)
			if baseCost > 0 {
				cost = baseCost
			} else if rec.EstimatedCostUSD > 0 {
				cost = rec.EstimatedCostUSD
			}
		}

		r.TotalTokens += tot
		r.InputTokens += in
		r.OutputTokens += out
		r.CachedTokens += cache
		r.ThinkingTokens += think
		r.TokenCostUSD += cost
		r.CodexNominalCostUSD += codexNominal
		r.Turns++
		if status == "unknown" || strings.EqualFold(rec.PriceStatus, "unknown") {
			r.UnknownCount++
		}
		if ts > r.LastActiveAt {
			r.LastActiveAt = ts
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan session_turn_usage: %w", err)
	}

	err = store.IteratePrefix("session_media_usage/", iterateAll, func(_ string, value []byte) error {
		var media SessionMediaUsageRecord
		if err := json.Unmarshal(value, &media); err != nil {
			return nil
		}
		sessID := strings.TrimSpace(media.SessionID)
		if sessID == "" {
			return nil
		}
		result.MediaScanned++

		acctID := strings.TrimSpace(media.AccountScopeID)
		if acctID == "" {
			acctID = "default"
		}
		provID := strings.ToLower(strings.TrimSpace(media.Provider))
		if provID == "" {
			provID = "unknown"
		}
		modelID := strings.TrimSpace(media.Model)
		if modelID == "" {
			modelID = "unknown"
		}
		ts := media.CreatedAt
		if ts <= 0 {
			ts = now
		}
		dateStr := time.UnixMilli(ts).UTC().Format("2006-01-02")

		key := KeyAccountUsageRollup(acctID, dateStr, sessID, provID, modelID)
		r, exists := rollups[key]
		if !exists {
			r = &AccountUsageRollup{
				AccountScopeID: acctID,
				Date:           dateStr,
				SessionID:      sessID,
				Provider:       provID,
				Model:          modelID,
			}
			rollups[key] = r
		}

		r.MediaCalls++
		r.MediaCostUSD += media.CostUSD
		switch strings.ToLower(media.Kind) {
		case "image":
			r.ImageCount++
			r.ImageCostUSD += media.CostUSD
		case "video":
			r.VideoCount++
			r.VideoCostUSD += media.CostUSD
		case "audio":
			r.AudioCount++
			r.AudioCostUSD += media.CostUSD
		}
		if strings.EqualFold(media.PriceStatus, "unknown") {
			r.UnknownCount++
		}
		if ts > r.LastActiveAt {
			r.LastActiveAt = ts
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan session_media_usage: %w", err)
	}

	batch := store.db.NewBatch()
	defer batch.Close()
	batchCount := 0

	for key, rollup := range rollups {
		rollup.UpdatedAt = now
		payload, err := json.Marshal(rollup)
		if err != nil {
			return result, fmt.Errorf("marshal rollup %q: %w", key, err)
		}
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return result, fmt.Errorf("set rollup %q: %w", key, err)
		}
		batchCount++
		result.RollupsCommitted++
		if batchCount >= 500 {
			if err := batch.Commit(pebble.Sync); err != nil {
				return result, fmt.Errorf("commit rollup batch: %w", err)
			}
			_ = batch.Close()
			batch = store.db.NewBatch()
			batchCount = 0
		}
	}

	marker := accountUsageRollupMigrationMarker{
		Version:          accountUsageRollupMigrationVersion,
		TurnsScanned:     result.TurnsScanned,
		MediaScanned:     result.MediaScanned,
		RollupsCommitted: result.RollupsCommitted,
		AppliedAt:        now,
	}
	markerBytes, err := json.Marshal(marker)
	if err != nil {
		return result, fmt.Errorf("marshal marker: %w", err)
	}
	if err := batch.Set([]byte(accountUsageRollupMigrationKey), markerBytes, nil); err != nil {
		return result, fmt.Errorf("set migration marker: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return result, fmt.Errorf("commit final batch: %w", err)
	}

	result.Applied = true
	return result, nil
}

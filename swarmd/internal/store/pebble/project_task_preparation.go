package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/cockroachdb/pebble"
)

// PrepareProjectTaskIndexes is the startup barrier, not an HTTP read side effect.
// Each canonical batch commits its cursor with its summaries. Related rows use
// their canonical mutation locks and commit independently, so an interrupted
// startup can resume without overwriting newer writes. No original media is
// converted here. A failure prevents serving a misleading partially ready board.
func (s *SessionStore) PrepareProjectTaskIndexes(ctx context.Context) error {
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(KeyProjectTaskAccountPrefix), UpperBound: []byte(KeyProjectTaskAccountPrefix + "\xff")})
	if err != nil {
		return err
	}
	defer iter.Close()
	for valid := iter.First(); valid; {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts := strings.Split(strings.TrimPrefix(string(iter.Key()), KeyProjectTaskAccountPrefix), "/")
		if len(parts) != 3 {
			return ErrProjectTaskSummaryCorrupt
		}
		account, err := url.PathUnescape(parts[0])
		if err != nil {
			return err
		}
		project, err := url.PathUnescape(parts[1])
		if err != nil {
			return err
		}
		if err := s.PrepareProjectTaskIndex(ctx, account, project); err != nil {
			return err
		}
		// Seek past this project without visiting any more canonical task bodies.
		valid = iter.SeekGE([]byte(ProjectTaskPrefix(account, project) + "\xff"))
	}
	return iter.Error()
}

// PrepareProjectTaskIndex is also usable by controlled import/repair callers.
// Normal requests only read the completed projections; they never call this.
func (s *SessionStore) PrepareProjectTaskIndex(ctx context.Context, account, project string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := s.BackfillProjectTaskSummaries(account, project)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrProjectTaskSummariesNotReady) {
			return fmt.Errorf("task index preparation failed (summary phase); records retained; correct the reported storage error and restart: %w", err)
		}
	}
	// Page compact rows, never re-scan full task bodies to discover dependencies.
	prefix := taskSummaryPrefix(account, project) + "rows/"
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []ProjectTaskRecord
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Context: ctx, Prefix: prefix, StartKey: cursor, Limit: taskSummaryBackfillRows}, func(key string, raw []byte) (bool, error) {
			var row taskSummaryRow
			if len(raw) > taskSummaryMaxBytes {
				return false, ErrProjectTaskSummaryCorrupt
			}
			if err := json.Unmarshal(raw, &row); err != nil {
				return false, err
			}
			if row.Version != taskSummaryVersion || row.Task.AccountID != account || row.Task.ProjectID != project || taskSummaryRowKey(row.Task) != key {
				return false, ErrProjectTaskSummaryCorrupt
			}
			rows = append(rows, row.Task)
			cursor = key + "\x00"
			return true, nil
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			snapshot := s.store.db.NewSnapshot()
			stats := ProjectTaskReadStats{}
			reader := newProjectTaskBoardReader(snapshot, account, rows, &stats)
			reader.prepare(rows)
			snapshot.Close()
			if reader.err != nil {
				return reader.err
			}
			if len(reader.missing) == 0 {
				break
			}
			if err := s.backfillTaskRelated(reader.missing, &stats); err != nil && !errors.Is(err, ErrProjectTaskSummariesNotReady) {
				return fmt.Errorf("task index preparation failed (related phase); records retained; correct the reported storage error and restart: %w", err)
			}
		}
	}
}

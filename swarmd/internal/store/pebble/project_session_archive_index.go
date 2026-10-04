package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

func projectArchivePrefix(account, user, project string) string {
	return "project_conversation_archive/v1/" + keyPart(account) + "/" + keyPart(user) + "/" + keyPart(project) + "/"
}

func projectArchiveKey(t V3SessionTombstone) string {
	s := t.Session
	if !t.Archived || t.Deleted || t.SessionID != s.ID || t.AccountScopeID != s.AccountScopeID || t.UserID != s.UserID || projectConversationKey(s) == "" {
		return ""
	}
	project, _ := s.Metadata["project_id"].(string)
	return fmt.Sprintf("%s%016x/%s", projectArchivePrefix(t.AccountScopeID, t.UserID, project), ^(uint64(t.UpdatedAt) ^ (uint64(1) << 63)), keyPart(t.SessionID))
}

func setProjectArchiveInBatch(batch *pebble.Batch, t V3SessionTombstone) error {
	key := projectArchiveKey(t)
	if key == "" {
		return nil
	}
	// Display-only projection: retain restore identity, never messages, prompts or
	// workspace grants from the canonical tombstone.
	row := V3SessionTombstone{SessionID: t.SessionID, AccountScopeID: t.AccountScopeID, UserID: t.UserID, Kind: t.Kind, Archived: true, Hidden: t.Hidden, EndpointSeq: t.EndpointSeq, EventSeq: t.EventSeq, UpdatedAt: t.UpdatedAt, Session: compactProjectConversation(t.Session)}
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return batch.Set([]byte(key), raw, nil)
}

// Called exclusively before the store is exposed to runtime traffic. Cursor and
// derived rows commit together, so interruption is resumable without GET retries.
func (s *SessionStore) prepareProjectArchiveIndex() error {
	const stateKey = "project_conversation_archive_migration/v1"
	var state struct {
		Cursor string
		Ready  bool
	}
	if _, err := s.store.GetJSON(stateKey, &state); err != nil {
		return err
	}
	prefix := V3SessionTombstonePrefix()
	if state.Cursor != "" && !strings.HasPrefix(state.Cursor, prefix) {
		return errors.New("invalid project archive migration cursor")
	}
	for !state.Ready {
		batch := s.store.db.NewBatch()
		count, decoded := 0, 0
		byteLimited := false
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: state.Cursor, Limit: 32}, func(key string, raw []byte) (bool, error) {
			if len(raw) > 256<<20 {
				return false, errors.New("legacy tombstone exceeds migration limit")
			}
			if count > 0 && decoded+len(raw) > 16<<20 {
				byteLimited = true
				return false, nil
			}
			var t V3SessionTombstone
			if err := json.Unmarshal(raw, &t); err != nil {
				return false, err
			}
			if KeyV3SessionTombstone(t.SessionID) != key {
				return false, errors.New("archive migration identity mismatch")
			}
			if err := setProjectArchiveInBatch(batch, t); err != nil {
				return false, err
			}
			count++
			decoded += len(raw)
			state.Cursor = key + "\x00"
			return true, nil
		})
		if err == nil {
			state.Ready = count < 32 && !byteLimited
			var raw []byte
			raw, err = json.Marshal(state)
			if err == nil {
				err = batch.Set([]byte(stateKey), raw, nil)
			}
			if err == nil {
				err = batch.Commit(pebble.Sync)
			}
		}
		batch.Close()
		if err != nil {
			return fmt.Errorf("project archive preparation failed; records retained: %w", err)
		}
	}
	return nil
}

func (s *SessionStore) listArchivedProjectConversationSummaries(account, user, project string, limit int) ([]V3SessionTombstone, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows := make([]V3SessionTombstone, 0)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: projectArchivePrefix(account, user, project), Limit: limit}, func(key string, raw []byte) (bool, error) {
		if len(raw) > 64<<10 {
			return false, errors.New("project archive summary exceeds limit")
		}
		var row V3SessionTombstone
		if err := json.Unmarshal(raw, &row); err != nil {
			return false, err
		}
		if row.AccountScopeID != account || row.UserID != user || row.Session.Metadata["project_id"] != project || projectArchiveKey(row) != key {
			return false, errors.New("project archive summary identity mismatch")
		}
		rows = append(rows, row)
		return true, nil
	})
	return rows, err
}

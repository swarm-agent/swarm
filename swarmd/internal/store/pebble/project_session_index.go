package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

func projectConversationPrefix(account, user, project string) string {
	return "project_conversation/v1/" + keyPart(account) + "/" + keyPart(user) + "/" + keyPart(project) + "/"
}
func projectConversationKey(s SessionSnapshot) string {
	project, _ := s.Metadata["project_id"].(string)
	if project == "" || s.AccountScopeID == "" || s.UserID == "" || s.Metadata["agent_name"] != "system-orchestrator" || s.Metadata["task_id"] != nil || s.Metadata["parent_session_id"] != nil {
		return ""
	}
	return fmt.Sprintf("%s%016x/%s", projectConversationPrefix(s.AccountScopeID, s.UserID, project), ^(uint64(s.CreatedAt) ^ (uint64(1) << 63)), keyPart(s.ID))
}

// This is a display read model, never a session mutation input or access grant.
func compactProjectConversation(s SessionSnapshot) SessionSnapshot {
	out := SessionSnapshot{ID: s.ID, UserID: s.UserID, AccountScopeID: s.AccountScopeID, Title: taskSummaryBrief(s.Title), Mode: s.Mode, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, MessageCount: s.MessageCount, LastMessageAt: s.LastMessageAt}
	out.Metadata = map[string]any{}
	for _, key := range []string{"project_id", "swarm_v3_project_id", "agent_name", "resolved_agent_name", "session_purpose", "system_sidechat"} {
		if value, ok := s.Metadata[key]; ok {
			out.Metadata[key] = value
		}
	}
	return out
}

func replaceProjectConversationInBatch(batch *pebble.Batch, previous, next *SessionSnapshot) error {
	if previous != nil {
		if key := projectConversationKey(*previous); key != "" {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return err
			}
		}
	}
	if next != nil {
		if key := projectConversationKey(*next); key != "" {
			raw, err := json.Marshal(compactProjectConversation(*next))
			if err != nil {
				return err
			}
			return batch.Set([]byte(key), raw, nil)
		}
	}
	return nil
}

// Startup-only migration: a durable cursor advances with each bounded batch;
// collection reads never visit the account's canonical session bodies.
func (s *SessionStore) prepareProjectConversationIndex() error {
	const stateKey = "project_conversation_migration/v1"
	var state struct {
		Cursor string
		Ready  bool
	}
	if _, err := s.store.GetJSON(stateKey, &state); err != nil {
		return err
	}
	if state.Ready {
		return nil
	}
	prefix := KeySession("")
	if state.Cursor != "" && !strings.HasPrefix(state.Cursor, prefix) {
		return errors.New("invalid project conversation migration cursor")
	}
	for !state.Ready {
		batch := s.store.db.NewBatch()
		count := 0
		decodedBytes := 0
		byteLimited := false
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: state.Cursor, Limit: 32}, func(key string, raw []byte) (bool, error) {
			if len(raw) > 256<<20 {
				return false, errors.New("legacy session exceeds migration limit")
			}
			if count > 0 && decodedBytes+len(raw) > 16<<20 {
				byteLimited = true
				return false, nil
			}
			decodedBytes += len(raw)
			var session SessionSnapshot
			if err := json.Unmarshal(raw, &session); err != nil {
				return false, err
			}
			if KeySession(session.ID) != key {
				return false, errors.New("session migration identity mismatch")
			}
			if err := replaceProjectConversationInBatch(batch, nil, &session); err != nil {
				return false, err
			}
			state.Cursor = key + "\x00"
			count++
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
			return fmt.Errorf("project conversation preparation failed; records retained: %w", err)
		}
	}
	return nil
}

func (s *SessionStore) listProjectConversationSummaries(account, user, project string, limit int) ([]SessionSnapshot, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows := make([]SessionSnapshot, 0)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: projectConversationPrefix(account, user, project), Limit: limit}, func(key string, raw []byte) (bool, error) {
		if len(raw) > 64<<10 {
			return false, errors.New("project conversation summary exceeds limit")
		}
		var row SessionSnapshot
		if err := json.Unmarshal(raw, &row); err != nil {
			return false, err
		}
		if row.AccountScopeID != account || row.UserID != user || row.Metadata["project_id"] != project || projectConversationKey(row) != key {
			return false, errors.New("project conversation summary identity mismatch")
		}
		rows = append(rows, row)
		return true, nil
	})
	return rows, err
}

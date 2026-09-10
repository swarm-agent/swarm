package pebblestore

import (
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
)

// AutomationSchedulerPosition is private daemon continuation, not an execution
// grant. Catalog keys remain seek anchors even after their source is deleted.
// Each sweep visits one workspace page; wrapping revisits failures and insertions
// behind the cursor. Per-definition occurrence positions survive process restart.
type AutomationSchedulerPosition struct {
	AccountKey   string
	AccountID    string
	WorkspaceKey string
	WorkspaceID  string
	Definitions  string
}

func (s *Store) GetAutomationSchedulerPosition(key string, out any) error {
	_, err := s.GetJSON("automation-scheduler:v1:"+automationPart(key), out)
	return err
}

// CAS and synced writes prevent stale workers from overwriting continuation.
func (s *Store) SaveAutomationSchedulerPosition(key string, expected, next any) error {
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	k := "automation-scheduler:v1:" + automationPart(key)
	old, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var current json.RawMessage
	found, err := s.GetJSON(k, &current)
	if err != nil {
		return err
	}
	if found && string(current) != string(old) {
		return ErrAutomationConflict
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return s.db.Set([]byte(k), data, pebble.Sync)
}

// SchedulerCatalogNext reads exactly one canonical catalog record, without
// invoking ListForAccount's whole-catalog migration scan. Missing anchors seek
// forward. Foreign/malformed anchors fail closed rather than crossing prefixes.
func (s *Store) SchedulerCatalogNext(accountID, after string) (key string, account AccountScopeRecord, workspace WorkspaceEntry, err error) {
	prefix := AccountScopePrefix()
	if accountID != "" {
		prefix = WorkspaceEntryPrefixForAccount(accountID)
	}
	if after != "" && !strings.HasPrefix(after, prefix) {
		err = ErrAutomationInvalid
		return
	}
	iter, e := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if e != nil {
		err = e
		return
	}
	defer iter.Close()
	start := prefix
	if after != "" {
		start = after + "\x00"
	}
	if !iter.SeekGE([]byte(start)) {
		err = iter.Error()
		return
	}
	key = string(iter.Key())
	if accountID == "" {
		err = json.Unmarshal(iter.Value(), &account)
	} else {
		err = json.Unmarshal(iter.Value(), &workspace)
		workspace = normalizeWorkspaceEntryForAccount(accountID, workspace)
	}
	return
}

func AutomationRecoveryPositionKey(scope AutomationScope, id string) string {
	return "recovery:" + automationPart(scope.AccountID) + ":" + automationPart(scope.WorkspaceID) + ":" + automationPart(id)
}

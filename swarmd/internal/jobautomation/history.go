package jobautomation

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/cockroachdb/pebble"
)

// Change identifies the authenticated writer; it never contains credentials.
type Change struct{ Source, Who string }

type HistoryRecord struct {
	Revision      uint64      `json:"revision"`
	Time          int64       `json:"time"`
	Kind          string      `json:"kind"`
	Source        string      `json:"source"`
	Who           string      `json:"who"`
	ChangedFields []string    `json:"changed_fields"`
	Definition    *Automation `json:"definition,omitempty"`
	Deleted       bool        `json:"deleted,omitempty"`
}

func historyKey(account, id string) string {
	return "job_automation/history/" + hexKey(account) + "/" + hexKey(id)
}
func historyNameKey(account, name string) string {
	return "job_automation/history_name/" + hexKey(account) + "/" + hexKey(name)
}

func definitionFields(a *Automation) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return map[string]any{"inputs": a.Inputs, "with": a.With, "on": a.On, "trigger_owner": a.TriggerOwner, "name": a.Name, "scope": a.Scope, "when": a.When, "enabled": a.Enabled, "retries": a.Retries, "steps": a.Steps, "result": a.Result}
}
func changedFields(a, b *Automation) []string {
	before, after := definitionFields(a), definitionFields(b)
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	out := []string{}
	for k := range keys {
		// Compare the persisted representation: omitempty makes empty and nil
		// step lists equivalent after a store round trip. Failed encoding must
		// never suppress a write (and its eventual persistence error).
		beforeJSON, beforeErr := json.Marshal(before[k])
		afterJSON, afterErr := json.Marshal(after[k])
		if beforeErr != nil || afterErr != nil || !bytes.Equal(beforeJSON, afterJSON) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
func (s *Store) historyLocked(account, id, name string) ([]HistoryRecord, error) {
	if id == "" {
		found, err := s.db.GetJSON(historyNameKey(account, name), &id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrNotFound
		}
	}
	records := []HistoryRecord{}
	found, err := s.db.GetJSON(historyKey(account, id), &records)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return records, nil
}

// History returns retained immutable snapshots newest first, including after deletion.
func (s *Store) History(account, id, name string) ([]HistoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyLocked(account, id, name)
}
func (s *Store) saveHistoryLocked(a Automation, previous *Automation, kind string, change Change) error {
	return s.saveHistoryRemovingProposalLocked(a, previous, kind, change, "")
}

func (s *Store) saveHistoryRemovingProposalLocked(a Automation, previous *Automation, kind string, change Change, proposalID string) error {
	records, err := s.historyLocked(a.Account, a.ID, "")
	if err != nil && err != ErrNotFound {
		return err
	}
	if change.Source == "" {
		change.Source = "set"
	}
	if change.Who == "" {
		change.Who = "owner"
	}
	record := HistoryRecord{Revision: a.Revision, Time: a.UpdatedAt, Kind: kind, Source: change.Source, Who: change.Who, Definition: &a}
	if kind == "deleted" {
		record.Definition = nil
		record.Deleted = true
	}
	record.ChangedFields = changedFields(previous, record.Definition)
	records = append([]HistoryRecord{record}, records...)
	if len(records) > 100 {
		records = records[:100]
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	put := func(key string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return batch.Set([]byte(key), data, nil)
	}
	if kind == "deleted" {
		err = batch.Delete([]byte(definitionKey(a.Account, a.ID)), nil)
	} else {
		err = put(definitionKey(a.Account, a.ID), a)
	}
	if err != nil {
		return err
	}
	if err = put(historyKey(a.Account, a.ID), records); err != nil {
		return err
	}
	if !a.Proposed {
		if err = put(historyNameKey(a.Account, a.Name), a.ID); err != nil {
			return err
		}
	}
	if proposalID != "" && proposalID != a.ID {
		if err = batch.Delete([]byte(definitionKey(a.Account, proposalID)), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
)

// ClaudeCodeSlot contains machine-local routing metadata only. Claude's own CLI
// owns the sign-in files; they must never enter the credential vault.
type ClaudeCodeSlot struct {
	ID         string    `json:"id"`
	Legacy     bool      `json:"legacy,omitempty"`
	LimitUntil time.Time `json:"limit_until,omitempty"`
}

var claudeCodeSlotsKey = []byte("provider/claudecode/accounts/v1")

func (s *AuthStore) claudeCodeSlotsLocked() ([]ClaudeCodeSlot, error) {
	raw, closer, err := s.store.db.Get(claudeCodeSlotsKey)
	if errors.Is(err, pebble.ErrNotFound) {
		return []ClaudeCodeSlot{{ID: "default", Legacy: true}}, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var slots []ClaudeCodeSlot
	if err := json.Unmarshal(raw, &slots); err != nil {
		return nil, err
	}
	if len(slots) == 0 || len(slots) > 32 {
		return nil, errors.New("invalid Claude Code account slots")
	}
	return slots, nil
}

func (s *AuthStore) saveClaudeCodeSlotsLocked(slots []ClaudeCodeSlot) error {
	raw, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	return s.store.db.Set(claudeCodeSlotsKey, raw, pebble.Sync)
}

func (s *AuthStore) ClaudeCodeSlots() ([]ClaudeCodeSlot, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	return s.claudeCodeSlotsLocked()
}

func (s *AuthStore) AddClaudeCodeSlot(id string) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	slots, err := s.claudeCodeSlotsLocked()
	if err != nil {
		return err
	}
	if len(slots) >= 32 {
		return errors.New("at most 32 Claude Code accounts are supported")
	}
	for _, slot := range slots {
		if slot.ID == id {
			return errors.New("Claude Code account already exists")
		}
	}
	if !validClaudeCodeSlotID(id) {
		return errors.New("invalid Claude Code account id")
	}
	return s.saveClaudeCodeSlotsLocked(append(slots, ClaudeCodeSlot{ID: id}))
}

func validClaudeCodeSlotID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// OrderClaudeCodeSlots requires an exact permutation, committing it atomically.
func (s *AuthStore) OrderClaudeCodeSlots(ids []string) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	slots, err := s.claudeCodeSlotsLocked()
	if err != nil {
		return err
	}
	if len(ids) != len(slots) {
		return errors.New("order must name every Claude Code account exactly once")
	}
	byID := make(map[string]ClaudeCodeSlot, len(slots))
	for _, slot := range slots {
		byID[slot.ID] = slot
	}
	ordered := make([]ClaudeCodeSlot, 0, len(ids))
	for _, id := range ids {
		slot, ok := byID[id]
		if !ok {
			return fmt.Errorf("unknown or duplicate Claude Code account %q", id)
		}
		ordered = append(ordered, slot)
		delete(byID, id)
	}
	return s.saveClaudeCodeSlotsLocked(ordered)
}

func (s *AuthStore) RecordClaudeCodeLimit(id string, until time.Time) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	slots, err := s.claudeCodeSlotsLocked()
	if err != nil {
		return err
	}
	for i := range slots {
		if slots[i].ID == id {
			// Concurrent turns cannot shorten a weekly window with a session window.
			if until.After(slots[i].LimitUntil) {
				slots[i].LimitUntil = until.UTC()
			}
			return s.saveClaudeCodeSlotsLocked(slots)
		}
	}
	return errors.New("Claude Code account not found")
}

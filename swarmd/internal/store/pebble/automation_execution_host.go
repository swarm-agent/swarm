package pebblestore

import (
	"errors"
	"fmt"
	"strings"
)

var ErrAutomationExecutionCancelled = errors.New("automation execution is cancelled")

// WithAutomationExecutionFence serializes host admission and cancellation across
// host instances sharing this store. Cancellation is synced BEFORE invoking the
// canonical lifecycle callback, so a crash or callback failure stays fail-closed.
// Callbacks must not recursively mutate automation records. Session state itself
// must only be written through ApplyV3SessionMutation / the plan lifecycle.
func (s *SessionStore) WithAutomationExecutionFence(account, key string, cancel bool, apply func() error) error {
	if s == nil || s.store == nil || strings.TrimSpace(account) == "" || len(key) != 64 || strings.ContainsAny(key, "/\\\x00") || apply == nil {
		return errors.New("invalid automation execution fence")
	}
	for _, c := range key { if !strings.ContainsRune("0123456789abcdef", c) { return errors.New("invalid automation execution key") } }
	s.store.automationsMu.Lock()
	defer s.store.automationsMu.Unlock()
	path := fmt.Sprintf("automation/execution_cancel/%s/%s", keyPart(account), key)
	_, cancelled, err := s.store.GetBytes(path)
	if err != nil { return err }
	if cancel {
		if !cancelled { if err := s.store.PutBytes(path, []byte("cancelled")); err != nil { return err } }
	} else if cancelled { return ErrAutomationExecutionCancelled }
	return apply()
}

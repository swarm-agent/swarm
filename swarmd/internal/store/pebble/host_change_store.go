package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Host changes are the owner-approved way to change the machine Swarm runs
// on. The daemon runs as its own locked-down user and cannot see the person's
// folders; an AI setting the machine up records a request here (which folder,
// why), the machine owner approves it, and an installer-placed helper outside
// the daemon makes exactly that folder visible. The daemon never changes the
// host itself: it only keeps the record and asks the helper.
const (
	KeyHostChangePrefix = "host_change/"

	// MaxHostChangesPerAccount bounds the kept history; older records go first.
	MaxHostChangesPerAccount = 200
	MaxHostChangeReasonChars = 500
	MaxHostChangePathBytes   = 512
	MaxHostChangeResultBytes = 2 << 10

	HostChangeOpMountWorkspace   = "mount-workspace"
	HostChangeOpUnmountWorkspace = "unmount-workspace"

	HostChangeStatusPending  = "pending"
	HostChangeStatusApplying = "applying"
	HostChangeStatusApplied  = "applied"
	HostChangeStatusFailed   = "failed"
	HostChangeStatusDenied   = "denied"
)

var (
	ErrHostChangeNotFound = errors.New("host change not found")
	// ErrHostChangeNotPending reports a decision on a record that already has one.
	ErrHostChangeNotPending = errors.New("host change is no longer pending")
)

// HostChange is one requested change to the machine.
type HostChange struct {
	ID          string `json:"id"`
	Op          string `json:"op"`
	Path        string `json:"path"`
	Reason      string `json:"reason,omitempty"`
	Status      string `json:"status"`
	RequestedBy string `json:"requested_by"`
	RequestedAt int64  `json:"requested_at"`
	DecidedAt   int64  `json:"decided_at,omitempty"`
	AppliedAt   int64  `json:"applied_at,omitempty"`
	// Result is the helper's reply (one JSON line, at most 2 KB).
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HostChangeOps lists the operations a request may name.
func HostChangeOps() []string {
	return []string{HostChangeOpMountWorkspace, HostChangeOpUnmountWorkspace}
}

// ValidHostChangeOp reports whether op is in the menu.
func ValidHostChangeOp(op string) bool {
	for _, known := range HostChangeOps() {
		if op == known {
			return true
		}
	}
	return false
}

// ValidHostChangeStatus reports whether status names a record state.
func ValidHostChangeStatus(status string) bool {
	switch status {
	case HostChangeStatusPending, HostChangeStatusApplying, HostChangeStatusApplied, HostChangeStatusFailed, HostChangeStatusDenied:
		return true
	}
	return false
}

type HostChangeStore struct {
	store *Store
	now   func() time.Time
}

func NewHostChangeStore(store *Store) *HostChangeStore {
	return &HostChangeStore{store: store, now: time.Now}
}

func hostChangeKey(account, id string) string {
	return KeyHostChangePrefix + keyPart(account) + "/" + keyPart(id)
}

// Create records a pending request. The caller has validated op and path.
func (s *HostChangeStore) Create(account, op, path, reason, requestedBy string) (HostChange, error) {
	if strings.TrimSpace(account) == "" {
		return HostChange{}, errAccountScopeRequired
	}
	if !ValidHostChangeOp(op) {
		return HostChange{}, fmt.Errorf("unsupported host change %q", op)
	}
	if strings.TrimSpace(path) == "" {
		return HostChange{}, errors.New("host change needs a path")
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > MaxHostChangeReasonChars {
		return HostChange{}, fmt.Errorf("reason is limited to %d characters", MaxHostChangeReasonChars)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return HostChange{}, err
	}
	now := s.now()
	// Time-ordered ids keep the key order equal to request order.
	change := HostChange{
		ID:          fmt.Sprintf("hc_%013d_%s", now.UnixMilli(), hex.EncodeToString(suffix)),
		Op:          op,
		Path:        path,
		Reason:      reason,
		Status:      HostChangeStatusPending,
		RequestedBy: strings.TrimSpace(requestedBy),
		RequestedAt: now.UnixMilli(),
	}
	if err := s.store.PutJSON(hostChangeKey(account, change.ID), change); err != nil {
		return HostChange{}, err
	}
	return change, s.trim(account)
}

// trim drops the oldest records beyond MaxHostChangesPerAccount.
func (s *HostChangeStore) trim(account string) error {
	all, err := s.List(account, "")
	if err != nil {
		return err
	}
	for i := 0; i < len(all)-MaxHostChangesPerAccount; i++ {
		if err := s.store.Delete(hostChangeKey(account, all[i].ID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *HostChangeStore) Get(account, id string) (HostChange, bool, error) {
	var change HostChange
	ok, err := s.store.GetJSON(hostChangeKey(account, id), &change)
	return change, ok, err
}

// List returns the account's records oldest first, optionally one status.
func (s *HostChangeStore) List(account, status string) ([]HostChange, error) {
	out := []HostChange{}
	err := s.store.IteratePrefix(KeyHostChangePrefix+keyPart(account)+"/", 10_000, func(_ string, value []byte) error {
		var change HostChange
		if err := json.Unmarshal(value, &change); err != nil {
			return err
		}
		if status == "" || change.Status == status {
			out = append(out, change)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// FindPending returns the account's pending request for exactly op and path.
func (s *HostChangeStore) FindPending(account, op, path string) (HostChange, bool, error) {
	pending, err := s.List(account, HostChangeStatusPending)
	if err != nil {
		return HostChange{}, false, err
	}
	for _, change := range pending {
		if change.Op == op && change.Path == path {
			return change, true, nil
		}
	}
	return HostChange{}, false, nil
}

// Deny settles a pending request without running anything.
func (s *HostChangeStore) Deny(account, id, reason string) (HostChange, error) {
	return s.transition(account, id, HostChangeStatusPending, func(change *HostChange) {
		change.Status = HostChangeStatusDenied
		change.DecidedAt = s.now().UnixMilli()
		change.Error = truncateHostChangeText(strings.TrimSpace(reason), MaxHostChangeResultBytes)
	})
}

// MarkApplying records the owner's approval before the helper runs, so a
// crash in between leaves a visible "applying" record, never a lost one.
func (s *HostChangeStore) MarkApplying(account, id string) (HostChange, error) {
	return s.transition(account, id, HostChangeStatusPending, func(change *HostChange) {
		change.Status = HostChangeStatusApplying
		change.DecidedAt = s.now().UnixMilli()
	})
}

// Finish records the helper's outcome for a record that is applying.
func (s *HostChangeStore) Finish(account, id string, ok bool, result, errText string) (HostChange, error) {
	return s.transition(account, id, HostChangeStatusApplying, func(change *HostChange) {
		change.Result = truncateHostChangeText(result, MaxHostChangeResultBytes)
		change.Error = truncateHostChangeText(errText, MaxHostChangeResultBytes)
		now := s.now().UnixMilli()
		if ok {
			change.Status = HostChangeStatusApplied
			change.AppliedAt = now
		} else {
			change.Status = HostChangeStatusFailed
		}
	})
}

func (s *HostChangeStore) transition(account, id, from string, apply func(*HostChange)) (HostChange, error) {
	change, ok, err := s.Get(account, id)
	if err != nil {
		return HostChange{}, err
	}
	if !ok {
		return HostChange{}, ErrHostChangeNotFound
	}
	if change.Status != from {
		return change, fmt.Errorf("%w: it is %s", ErrHostChangeNotPending, change.Status)
	}
	apply(&change)
	return change, s.store.PutJSON(hostChangeKey(account, id), change)
}

// FailInterrupted turns every record still applying (across accounts) into a
// failed one. The daemon calls it at startup: an applying record means the
// previous process stopped before the helper answered.
func (s *HostChangeStore) FailInterrupted() (int, error) {
	type pending struct {
		key    string
		change HostChange
	}
	var found []pending
	err := s.store.IteratePrefix(KeyHostChangePrefix, 100_000, func(key string, value []byte) error {
		var change HostChange
		if err := json.Unmarshal(value, &change); err != nil {
			return err
		}
		if change.Status == HostChangeStatusApplying {
			found = append(found, pending{key: key, change: change})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, p := range found {
		p.change.Status = HostChangeStatusFailed
		p.change.Error = "interrupted"
		if err := s.store.PutJSON(p.key, p.change); err != nil {
			return 0, err
		}
	}
	return len(found), nil
}

func truncateHostChangeText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) { // keep whole runes
		cut = cut[:len(cut)-1]
	}
	return cut
}

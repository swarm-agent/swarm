package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Secret slots let agents use a credential without seeing it. A slot names
// a secret and the websites it may be sent to; a grant lets one project's
// sandbox use it until a deadline. Agents see only a stand-in; the egress
// gateway swaps in the real value (sealed in the secret store, set only by
// the owner) on requests to an allowed website. Every use is logged.
const (
	KeySecretSlotPrefix  = "secret_slot/"
	KeySecretGrantPrefix = "secret_grant/"
	KeySecretUsePrefix   = "secret_use/"

	MaxSecretSlotHosts    = 8
	MaxSecretGrantSeconds = 365 * 24 * 60 * 60
)

var (
	secretSlotNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	secretSlotHostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

	ErrSecretSlotNotFound = errors.New("secret slot not found")
)

type SecretSlot struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Hosts          []string `json:"hosts"`
	HasValue       bool     `json:"has_value"`
	CreatedAt      int64    `json:"created_at"`
	UpdatedAt      int64    `json:"updated_at"`
	ValueUpdatedAt int64    `json:"value_updated_at,omitempty"`
}

type SecretGrant struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	ExpiresAt     int64  `json:"expires_at"`
	CreatedAt     int64  `json:"created_at"`
}

// SecretUse records one attempt to use a secret through the gateway.
// Outcome is "injected" or a refusal reason; the value is never recorded.
type SecretUse struct {
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	Host          string `json:"host"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Outcome       string `json:"outcome"`
	At            int64  `json:"at"`
}

type SecretSlotStore struct {
	store *Store
	now   func() time.Time
}

func NewSecretSlotStore(store *Store) *SecretSlotStore {
	return &SecretSlotStore{store: store, now: time.Now}
}

// ValidateSecretSlotName requires an environment-variable-style name.
func ValidateSecretSlotName(name string) error {
	if !secretSlotNamePattern.MatchString(name) {
		return errors.New("secret name must be 2-64 characters: capital letters, digits and _ (starting with a letter)")
	}
	return nil
}

// NormalizeSecretSlotHosts lowercases, dedupes and validates exact host names.
// Wildcards, IP addresses and ports are refused: a secret goes to named
// public websites only.
func NormalizeSecretSlotHosts(hosts []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" {
			continue
		}
		if !secretSlotHostPattern.MatchString(h) {
			return nil, fmt.Errorf("allowed website %q must be an exact host name such as api.example.com", h)
		}
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	if len(out) == 0 || len(out) > MaxSecretSlotHosts {
		return nil, fmt.Errorf("a secret needs 1 to %d allowed websites", MaxSecretSlotHosts)
	}
	sort.Strings(out)
	return out, nil
}

func secretSlotKey(account, name string) string {
	return KeySecretSlotPrefix + keyPart(account) + "/" + keyPart(name)
}

func secretGrantPrefix(account, name string) string {
	return KeySecretGrantPrefix + keyPart(account) + "/" + keyPart(name) + "/"
}

// UpsertSlot creates a slot or updates its description and hosts. The value
// is set separately, by the owner only.
func (s *SecretSlotStore) UpsertSlot(account, name, description string, hosts []string) (SecretSlot, error) {
	if strings.TrimSpace(account) == "" {
		return SecretSlot{}, errAccountScopeRequired
	}
	if err := ValidateSecretSlotName(name); err != nil {
		return SecretSlot{}, err
	}
	hosts, err := NormalizeSecretSlotHosts(hosts)
	if err != nil {
		return SecretSlot{}, err
	}
	description = strings.TrimSpace(description)
	if len(description) > 500 {
		return SecretSlot{}, errors.New("secret description is limited to 500 characters")
	}
	now := s.now().UnixMilli()
	slot, ok, err := s.GetSlot(account, name)
	if err != nil {
		return SecretSlot{}, err
	}
	if !ok {
		slot = SecretSlot{Name: name, CreatedAt: now}
	}
	slot.Description = description
	slot.Hosts = hosts
	slot.UpdatedAt = now
	return slot, s.store.PutJSON(secretSlotKey(account, name), slot)
}

// MarkValue records that the slot's value was set (or cleared) at now.
func (s *SecretSlotStore) MarkValue(account, name string, has bool) (SecretSlot, error) {
	slot, ok, err := s.GetSlot(account, name)
	if err != nil {
		return SecretSlot{}, err
	}
	if !ok {
		return SecretSlot{}, ErrSecretSlotNotFound
	}
	slot.HasValue = has
	slot.ValueUpdatedAt = s.now().UnixMilli()
	slot.UpdatedAt = slot.ValueUpdatedAt
	return slot, s.store.PutJSON(secretSlotKey(account, name), slot)
}

func (s *SecretSlotStore) GetSlot(account, name string) (SecretSlot, bool, error) {
	var slot SecretSlot
	ok, err := s.store.GetJSON(secretSlotKey(account, name), &slot)
	return slot, ok, err
}

func (s *SecretSlotStore) ListSlots(account string) ([]SecretSlot, error) {
	out := []SecretSlot{}
	err := s.store.IteratePrefix(KeySecretSlotPrefix+keyPart(account)+"/", 10_000, func(_ string, value []byte) error {
		var slot SecretSlot
		if err := json.Unmarshal(value, &slot); err != nil {
			return err
		}
		out = append(out, slot)
		return nil
	})
	return out, err
}

// DeleteSlot removes a slot and its grants. The caller removes the value.
func (s *SecretSlotStore) DeleteSlot(account, name string) error {
	grants, err := s.ListGrants(account, name)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if err := s.store.Delete(secretGrantPrefix(account, name) + keyPart(g.ID)); err != nil {
			return err
		}
	}
	return s.store.Delete(secretSlotKey(account, name))
}

// Grant lets the sandbox of workspacePath use the slot for ttl.
func (s *SecretSlotStore) Grant(account, name, workspacePath string, ttl time.Duration) (SecretGrant, error) {
	if _, ok, err := s.GetSlot(account, name); err != nil {
		return SecretGrant{}, err
	} else if !ok {
		return SecretGrant{}, ErrSecretSlotNotFound
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if !filepath.IsAbs(workspacePath) {
		return SecretGrant{}, errors.New("grant needs an absolute workspace path")
	}
	if ttl <= 0 || ttl > MaxSecretGrantSeconds*time.Second {
		return SecretGrant{}, errors.New("grant duration must be between 1 second and 365 days")
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return SecretGrant{}, err
	}
	now := s.now()
	grant := SecretGrant{
		ID:            "sg_" + hex.EncodeToString(id),
		Account:       account,
		Name:          name,
		WorkspacePath: filepath.Clean(workspacePath),
		ExpiresAt:     now.Add(ttl).UnixMilli(),
		CreatedAt:     now.UnixMilli(),
	}
	return grant, s.store.PutJSON(secretGrantPrefix(account, name)+keyPart(grant.ID), grant)
}

func (s *SecretSlotStore) Revoke(account, name, grantID string) error {
	return s.store.Delete(secretGrantPrefix(account, name) + keyPart(grantID))
}

func (s *SecretSlotStore) ListGrants(account, name string) ([]SecretGrant, error) {
	out := []SecretGrant{}
	err := s.store.IteratePrefix(secretGrantPrefix(account, name), 10_000, func(_ string, value []byte) error {
		var g SecretGrant
		if err := json.Unmarshal(value, &g); err != nil {
			return err
		}
		out = append(out, g)
		return nil
	})
	return out, err
}

// ActiveGrant is a live grant joined with its slot and owning account.
type ActiveGrant struct {
	Account string
	Slot    SecretSlot
	Grant   SecretGrant
}

// ActiveGrantsForWorkspace returns every unexpired grant, across accounts,
// for exactly workspacePath. The sandbox of one project sees only these.
func (s *SecretSlotStore) ActiveGrantsForWorkspace(workspacePath string) ([]ActiveGrant, error) {
	workspacePath = filepath.Clean(workspacePath)
	now := s.now().UnixMilli()
	slots := map[string]SecretSlot{}
	if err := s.store.IteratePrefix(KeySecretSlotPrefix, 100_000, func(key string, value []byte) error {
		var slot SecretSlot
		if err := json.Unmarshal(value, &slot); err != nil {
			return err
		}
		slots[strings.TrimPrefix(key, KeySecretSlotPrefix)] = slot
		return nil
	}); err != nil {
		return nil, err
	}
	out := []ActiveGrant{}
	err := s.store.IteratePrefix(KeySecretGrantPrefix, 100_000, func(_ string, value []byte) error {
		var g SecretGrant
		if err := json.Unmarshal(value, &g); err != nil {
			return err
		}
		if g.WorkspacePath != workspacePath || g.ExpiresAt <= now {
			return nil
		}
		slot, ok := slots[keyPart(g.Account)+"/"+keyPart(g.Name)]
		if !ok {
			return nil
		}
		out = append(out, ActiveGrant{Account: g.Account, Slot: slot, Grant: g})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Slot.Name < out[j].Slot.Name })
	return out, err
}

// RecordUse appends one gateway use to the log.
func (s *SecretSlotStore) RecordUse(account string, use SecretUse) error {
	use.At = s.now().UnixMilli()
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	key := fmt.Sprintf("%s%s/%020d-%s", KeySecretUsePrefix, keyPart(account), use.At, hex.EncodeToString(id))
	return s.store.PutJSON(key, use)
}

// ListUses returns up to limit log entries for the account, oldest first.
func (s *SecretSlotStore) ListUses(account string, limit int) ([]SecretUse, error) {
	out := []SecretUse{}
	err := s.store.IteratePrefix(KeySecretUsePrefix+keyPart(account)+"/", limit, func(_ string, value []byte) error {
		var u SecretUse
		if err := json.Unmarshal(value, &u); err != nil {
			return err
		}
		out = append(out, u)
		return nil
	})
	return out, err
}

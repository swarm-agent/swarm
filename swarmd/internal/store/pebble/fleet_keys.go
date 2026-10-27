package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Fleet keys: a home is its fleet's key manager for provider API keys. The
// person enters a key once, on the home's own key page; the home keeps the
// value sealed in its secret store (the same account key that seals provider
// credentials) and installs it on the machines the person chose, over the
// pinned link. This record holds what may be shown: which keys exist, a short
// fingerprint, and each machine's outcome. Never the value.

// FleetKeys is one account's fleet key record, revisioned like the account
// permissions record: every change bumps Revision.
type FleetKeys struct {
	Revision uint64     `json:"revision"`
	Keys     []FleetKey `json:"keys"`
}

// FleetKey is one provider key the home manages. Fingerprint is the first
// eight hex characters of the key's SHA-256; Last4 its last four characters.
type FleetKey struct {
	ID          string                    `json:"id"`
	Provider    string                    `json:"provider"`
	Label       string                    `json:"label,omitempty"`
	Fingerprint string                    `json:"fingerprint"`
	Last4       string                    `json:"last4"`
	CreatedAt   int64                     `json:"created_at"`
	CreatedBy   string                    `json:"created_by,omitempty"`
	Home        *FleetKeyTarget           `json:"home,omitempty"`
	Machines    map[string]FleetKeyTarget `json:"machines"`
	// RevokedAt is set once the key was removed everywhere and the home
	// deleted its stored value; it cannot be installed again.
	RevokedAt int64 `json:"revoked_at,omitempty"`
}

// Fleet key target states.
const (
	FleetKeyPending   = "pending"
	FleetKeyInstalled = "installed"
	FleetKeyFailed    = "failed"
	FleetKeyRemoved   = "removed"
)

// FleetKeyTarget is the key's state on one machine (or on the home itself).
// Error is short and never carries key material.
type FleetKeyTarget struct {
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
	InstalledAt int64  `json:"installed_at,omitempty"`
	RemovedAt   int64  `json:"removed_at,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Key returns the key with id, if the record has it.
func (k *FleetKeys) Key(id string) (*FleetKey, bool) {
	for i := range k.Keys {
		if k.Keys[i].ID == id {
			return &k.Keys[i], true
		}
	}
	return nil, false
}

// RemovedEverywhere reports whether every target the key ever had is removed.
func (k FleetKey) RemovedEverywhere() bool {
	if k.Home != nil && k.Home.State != FleetKeyRemoved {
		return false
	}
	for _, target := range k.Machines {
		if target.State != FleetKeyRemoved {
			return false
		}
	}
	return true
}

const (
	fleetKeysPrefix = "fleet/keys/"
	// KeyFleetKeyValuePrefix holds each fleet key's value in the secret store,
	// sealed like provider credentials.
	KeyFleetKeyValuePrefix = "fleet/key_value/"
	maxFleetKeyValueBytes  = 16 << 10
)

var ErrFleetKeyNotFound = errors.New("no fleet key with that id")

// GetFleetKeys reads an account's fleet key record (empty when none).
func (s *FleetStore) GetFleetKeys(account string) (FleetKeys, error) {
	keys := FleetKeys{Keys: []FleetKey{}}
	if strings.TrimSpace(account) == "" {
		return keys, errors.New("fleet keys account required")
	}
	_, err := s.store.GetJSON(fleetKeysPrefix+keyPart(account), &keys)
	if keys.Keys == nil {
		keys.Keys = []FleetKey{}
	}
	return keys, err
}

// UpdateFleetKeys changes an account's fleet key record under its lock and
// stores it with the next revision. An error from change stores nothing.
func (s *FleetStore) UpdateFleetKeys(account string, change func(*FleetKeys) error) (FleetKeys, error) {
	if strings.TrimSpace(account) == "" {
		return FleetKeys{}, errors.New("fleet keys account required")
	}
	unlock := s.store.sessionMutations.lockSessions("fleet_keys:" + account)
	defer unlock()
	current, err := s.GetFleetKeys(account)
	if err != nil {
		return current, err
	}
	if err := change(&current); err != nil {
		return current, err
	}
	current.Revision++
	return current, s.store.PutJSON(fleetKeysPrefix+keyPart(account), current)
}

func fleetKeyValueKey(accountScopeID, id string) string {
	return KeyFleetKeyValuePrefix + keyPart(accountScopeID) + "/" + keyPart(id)
}

// PutFleetKeyValue seals and keeps a fleet key's value on the home.
func (s *AuthStore) PutFleetKeyValue(accountScopeID, id string, value []byte) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("a fleet key needs an id")
	}
	if len(value) == 0 || len(value) > maxFleetKeyValueBytes {
		return fmt.Errorf("a fleet key must be 1 to %d bytes", maxFleetKeyValueBytes)
	}
	meta, dek, err := s.ensureSecretDEKForAccount(accountScopeID)
	if err != nil {
		return err
	}
	mode := storageModePebbleEncrypted
	if meta != nil && meta.Enabled {
		mode = storageModePebbleVault
	}
	sealed, err := encryptVaultBlob(dek, value)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealedAuthCredentialRecord{
		Version:          vaultVersion,
		StorageMode:      mode,
		NonceBase64:      base64.StdEncoding.EncodeToString(sealed[:nonceSizeX]),
		CiphertextBase64: base64.StdEncoding.EncodeToString(sealed[nonceSizeX:]),
	})
	if err != nil {
		return fmt.Errorf("marshal sealed fleet key: %w", err)
	}
	return s.secretStore.PutBytes(fleetKeyValueKey(accountScopeID, id), payload)
}

// GetFleetKeyValue opens a fleet key's value. It returns ErrVaultLocked while
// a password vault is locked.
func (s *AuthStore) GetFleetKeyValue(accountScopeID, id string) ([]byte, bool, error) {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return nil, false, err
	}
	payload, ok, err := s.secretStore.GetBytes(fleetKeyValueKey(accountScopeID, id))
	if err != nil || !ok {
		return nil, ok, err
	}
	var sealed sealedAuthCredentialRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return nil, false, fmt.Errorf("decode sealed fleet key: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.NonceBase64)
	if err != nil {
		return nil, false, fmt.Errorf("decode sealed fleet key nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.CiphertextBase64)
	if err != nil {
		return nil, false, fmt.Errorf("decode sealed fleet key ciphertext: %w", err)
	}
	_, dek, err := s.ensureSecretDEKForAccount(accountScopeID)
	if err != nil {
		return nil, false, err
	}
	value, err := decryptVaultBlob(dek, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return nil, false, fmt.Errorf("decrypt fleet key: %w", err)
	}
	return value, true, nil
}

// DeleteFleetKeyValue forgets a fleet key's value.
func (s *AuthStore) DeleteFleetKeyValue(accountScopeID, id string) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	return s.secretStore.Delete(fleetKeyValueKey(accountScopeID, id))
}

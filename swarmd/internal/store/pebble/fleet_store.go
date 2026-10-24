package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A link goes one way: a home controls the Swarms that invited it. An
// invited Swarm keeps its invites and its homes (who may call it, by key,
// and the owner session each holds); a home keeps its machines, with the
// session each gave it sealed in the secret store. Nothing a machine keeps
// reaches its home.

// FleetInvite is a one-time link code this Swarm made. Only the secret's
// hash is kept.
type FleetInvite struct {
	ID          string `json:"id"`
	SecretHash  string `json:"secret_hash"`
	Address     string `json:"address"`
	Name        string `json:"name,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UsedAt      int64  `json:"used_at,omitempty"`
	UsedBy      string `json:"used_by,omitempty"`
	CancelledAt int64  `json:"cancelled_at,omitempty"`
}

// FleetHome is a Swarm that controls this one: its key, and the owner
// session it was given (a client session, listed and revocable with the
// others).
type FleetHome struct {
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
	SessionID   string `json:"session_id"`
	InviteID    string `json:"invite_id,omitempty"`
	LinkedAt    int64  `json:"linked_at"`
	LastCallAt  int64  `json:"last_call_at,omitempty"`
}

// FleetMachine is a Swarm this home controls.
type FleetMachine struct {
	Name           string `json:"name"`
	Address        string `json:"address"`
	Fingerprint    string `json:"fingerprint"`
	SwarmID        string `json:"swarm_id,omitempty"`
	SessionID      string `json:"session_id"`
	AccountScopeID string `json:"account_scope_id"`
	LinkedAt       int64  `json:"linked_at"`
	LastContactAt  int64  `json:"last_contact_at,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

// FleetListen is where this Swarm listens for its homes ("" while it does not).
type FleetListen struct {
	Address string `json:"address"`
}

const (
	fleetInvitePrefix  = "fleet/invite/"
	fleetHomePrefix    = "fleet/home/"
	fleetMachinePrefix = "fleet/machine/"
	fleetListenKey     = "fleet/listen"
	// KeyFleetLinkTokenPrefix holds a home's session on each machine, in the
	// secret store, sealed like provider credentials.
	KeyFleetLinkTokenPrefix = "fleet/link_token/"
)

// FleetStore keeps the links of this Swarm.
type FleetStore struct {
	store *Store
}

func NewFleetStore(store *Store) *FleetStore { return &FleetStore{store: store} }

func listFleet[T any](s *FleetStore, prefix string) ([]T, error) {
	out := []T{}
	err := s.store.IteratePrefix(prefix, 0, func(_ string, value []byte) error {
		var item T
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		out = append(out, item)
		return nil
	})
	return out, err
}

func (s *FleetStore) PutInvite(invite FleetInvite) error {
	if invite.ID == "" || invite.SecretHash == "" {
		return errors.New("a link invite needs an id and a secret")
	}
	return s.store.PutJSON(fleetInvitePrefix+keyPart(invite.ID), invite)
}

func (s *FleetStore) GetInvite(id string) (FleetInvite, bool, error) {
	var invite FleetInvite
	found, err := s.store.GetJSON(fleetInvitePrefix+keyPart(id), &invite)
	return invite, found, err
}

func (s *FleetStore) ListInvites() ([]FleetInvite, error) {
	return listFleet[FleetInvite](s, fleetInvitePrefix)
}

func (s *FleetStore) PutHome(home FleetHome) error {
	if home.Fingerprint == "" || home.SessionID == "" {
		return errors.New("a home needs a key and a session")
	}
	return s.store.PutJSON(fleetHomePrefix+keyPart(home.Fingerprint), home)
}

func (s *FleetStore) GetHome(fingerprint string) (FleetHome, bool, error) {
	var home FleetHome
	found, err := s.store.GetJSON(fleetHomePrefix+keyPart(fingerprint), &home)
	return home, found, err
}

func (s *FleetStore) ListHomes() ([]FleetHome, error) {
	return listFleet[FleetHome](s, fleetHomePrefix)
}

func (s *FleetStore) DeleteHome(fingerprint string) error {
	return s.store.Delete(fleetHomePrefix + keyPart(fingerprint))
}

func (s *FleetStore) PutMachine(machine FleetMachine) error {
	if machine.Name == "" || machine.Address == "" || machine.Fingerprint == "" {
		return errors.New("a machine needs a name, an address and a key")
	}
	return s.store.PutJSON(fleetMachinePrefix+keyPart(machine.Name), machine)
}

func (s *FleetStore) GetMachine(name string) (FleetMachine, bool, error) {
	var machine FleetMachine
	found, err := s.store.GetJSON(fleetMachinePrefix+keyPart(name), &machine)
	return machine, found, err
}

func (s *FleetStore) ListMachines() ([]FleetMachine, error) {
	return listFleet[FleetMachine](s, fleetMachinePrefix)
}

func (s *FleetStore) DeleteMachine(name string) error {
	return s.store.Delete(fleetMachinePrefix + keyPart(name))
}

func (s *FleetStore) GetListen() (FleetListen, error) {
	var listen FleetListen
	_, err := s.store.GetJSON(fleetListenKey, &listen)
	return listen, err
}

func (s *FleetStore) PutListen(listen FleetListen) error {
	return s.store.PutJSON(fleetListenKey, listen)
}

func fleetLinkTokenKey(accountScopeID, machine string) string {
	return KeyFleetLinkTokenPrefix + keyPart(accountScopeID) + "/" + keyPart(machine)
}

// PutFleetLinkToken seals and keeps the session a machine gave this home.
func (s *AuthStore) PutFleetLinkToken(accountScopeID, machine, token string) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(machine) == "" || strings.TrimSpace(token) == "" {
		return errors.New("a link token needs a machine and a token")
	}
	meta, dek, err := s.ensureSecretDEKForAccount(accountScopeID)
	if err != nil {
		return err
	}
	mode := storageModePebbleEncrypted
	if meta != nil && meta.Enabled {
		mode = storageModePebbleVault
	}
	sealed, err := encryptVaultBlob(dek, []byte(token))
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
		return fmt.Errorf("marshal sealed link token: %w", err)
	}
	return s.secretStore.PutBytes(fleetLinkTokenKey(accountScopeID, machine), payload)
}

// GetFleetLinkToken opens the session a machine gave this home. It returns
// ErrVaultLocked while a password vault is locked.
func (s *AuthStore) GetFleetLinkToken(accountScopeID, machine string) (string, bool, error) {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return "", false, err
	}
	payload, ok, err := s.secretStore.GetBytes(fleetLinkTokenKey(accountScopeID, machine))
	if err != nil || !ok {
		return "", ok, err
	}
	var sealed sealedAuthCredentialRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return "", false, fmt.Errorf("decode sealed link token: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.NonceBase64)
	if err != nil {
		return "", false, fmt.Errorf("decode sealed link token nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.CiphertextBase64)
	if err != nil {
		return "", false, fmt.Errorf("decode sealed link token ciphertext: %w", err)
	}
	_, dek, err := s.ensureSecretDEKForAccount(accountScopeID)
	if err != nil {
		return "", false, err
	}
	value, err := decryptVaultBlob(dek, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return "", false, fmt.Errorf("decrypt link token: %w", err)
	}
	return string(value), true, nil
}

// DeleteFleetLinkToken forgets the session a machine gave this home.
func (s *AuthStore) DeleteFleetLinkToken(accountScopeID, machine string) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	return s.secretStore.Delete(fleetLinkTokenKey(accountScopeID, machine))
}

package pebblestore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	vaultVersion                 = 1
	vaultArgon2Time              = uint32(1)
	vaultArgon2MemoryKiB         = uint32(64 * 1024)
	vaultArgon2Parallelism       = uint8(4)
	vaultDerivedKeyLength        = uint32(32)
	nonceSizeX                   = chacha20poly1305.NonceSizeX
	storageModePebblePlain       = "pebble/plain"
	storageModePebbleVault       = "pebble/vault"
	vaultEnableWarning           = ""
	vaultUnlockRequiredMessage   = "vault is locked; unlock it first"
	vaultPasswordRequiredMessage = "vault password is required"
)

var ErrVaultLocked = errors.New(vaultUnlockRequiredMessage)

type VaultMetadata struct {
	Version       int    `json:"version"`
	KDF           string `json:"kdf"`
	SaltBase64    string `json:"salt_base64"`
	MemoryKiB     uint32 `json:"memory_kib"`
	TimeCost      uint32 `json:"time_cost"`
	Parallelism   uint8  `json:"parallelism"`
	WrappedDEK    string `json:"wrapped_dek"`
	UpdatedAt     int64  `json:"updated_at"`
	Enabled       bool   `json:"enabled"`
	StorageMode   string `json:"storage_mode"`
	UnlockWarning string `json:"unlock_warning,omitempty"`
}

type VaultStatus struct {
	Enabled        bool   `json:"enabled"`
	Unlocked       bool   `json:"unlocked"`
	UnlockRequired bool   `json:"unlock_required"`
	StorageMode    string `json:"storage_mode"`
	Warning        string `json:"warning,omitempty"`
}

type sealedAuthCredentialRecord struct {
	Version          int    `json:"version"`
	StorageMode      string `json:"storage_mode"`
	NonceBase64      string `json:"nonce_base64"`
	CiphertextBase64 string `json:"ciphertext_base64"`
}

func (s *AuthStore) StorageMode() string {
	status, err := s.VaultStatus()
	if err != nil {
		return storageModePebblePlain
	}
	return status.StorageMode
}

func (s *AuthStore) VaultStatus() (VaultStatus, error) {
	meta, unlocked, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	return vaultStatusFromState(meta, unlocked), nil
}

func (s *AuthStore) EnableVault(password string) (VaultStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return VaultStatus{}, errors.New(vaultPasswordRequiredMessage)
	}

	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta != nil && meta.Enabled {
		return VaultStatus{}, errors.New("vault is already enabled")
	}

	if _, migrated, err := s.migrateLegacyCodexRecord(); err != nil {
		return VaultStatus{}, err
	} else if migrated {
		meta = nil
	}

	records, err := s.ListCredentials("", 10_000)
	if err != nil {
		return VaultStatus{}, err
	}

	dek, err := randomBytes(int(vaultDerivedKeyLength))
	if err != nil {
		return VaultStatus{}, err
	}
	salt, err := randomBytes(16)
	if err != nil {
		return VaultStatus{}, err
	}
	kek := deriveVaultKey(password, salt)
	wrappedDEK, err := encryptVaultBlob(kek, dek)
	if err != nil {
		return VaultStatus{}, err
	}

	now := time.Now().UnixMilli()
	nextMeta := &VaultMetadata{
		Version:       vaultVersion,
		KDF:           "argon2id",
		SaltBase64:    base64.StdEncoding.EncodeToString(salt),
		MemoryKiB:     vaultArgon2MemoryKiB,
		TimeCost:      vaultArgon2Time,
		Parallelism:   vaultArgon2Parallelism,
		WrappedDEK:    base64.StdEncoding.EncodeToString(wrappedDEK),
		UpdatedAt:     now,
		Enabled:       true,
		StorageMode:   storageModePebbleVault,
		UnlockWarning: vaultEnableWarning,
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	for _, record := range records {
		payload, err := encodeSealedCredential(dek, record)
		if err != nil {
			return VaultStatus{}, err
		}
		if err := batch.Set([]byte(KeyAuthCredential(record.Provider, record.ID)), payload, nil); err != nil {
			return VaultStatus{}, fmt.Errorf("write vaulted auth record %s/%s: %w", record.Provider, record.ID, err)
		}
	}
	metaPayload, err := json.Marshal(nextMeta)
	if err != nil {
		return VaultStatus{}, fmt.Errorf("marshal vault metadata: %w", err)
	}
	if err := batch.Set([]byte(KeyAuthVaultMeta), metaPayload, nil); err != nil {
		return VaultStatus{}, fmt.Errorf("write vault metadata: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return VaultStatus{}, fmt.Errorf("commit vault enable: %w", err)
	}

	s.mu.Lock()
	s.vaultMeta = nextMeta
	s.vaultDEK = append([]byte(nil), dek...)
	s.mu.Unlock()
	return vaultStatusFromState(nextMeta, true), nil
}

func (s *AuthStore) UnlockVault(password string) (VaultStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return VaultStatus{}, errors.New(vaultPasswordRequiredMessage)
	}

	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta == nil || !meta.Enabled {
		return vaultStatusFromState(nil, false), nil
	}

	dek, err := unlockVaultDEK(password, meta)
	if err != nil {
		return VaultStatus{}, err
	}
	s.mu.Lock()
	s.vaultDEK = dek
	s.mu.Unlock()
	return vaultStatusFromState(meta, true), nil
}

func (s *AuthStore) LockVault() (VaultStatus, error) {
	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	s.mu.Lock()
	s.vaultDEK = nil
	s.mu.Unlock()
	return vaultStatusFromState(meta, false), nil
}

func (s *AuthStore) DisableVault(password string) (VaultStatus, error) {
	password = strings.TrimSpace(password)

	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta == nil || !meta.Enabled {
		return vaultStatusFromState(nil, false), nil
	}
	if password == "" {
		return VaultStatus{}, errors.New(vaultPasswordRequiredMessage)
	}

	dek, err := unlockVaultDEK(password, meta)
	if err != nil {
		return VaultStatus{}, err
	}

	records, err := s.listStoredCredentialsWithDEK("", 10_000, meta, dek)
	if err != nil {
		return VaultStatus{}, err
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	for _, record := range records {
		payload, err := json.Marshal(record)
		if err != nil {
			return VaultStatus{}, fmt.Errorf("marshal plaintext auth record %s/%s: %w", record.Provider, record.ID, err)
		}
		if err := batch.Set([]byte(KeyAuthCredential(record.Provider, record.ID)), payload, nil); err != nil {
			return VaultStatus{}, fmt.Errorf("write plaintext auth record %s/%s: %w", record.Provider, record.ID, err)
		}
	}
	if err := batch.Delete([]byte(KeyAuthVaultMeta), nil); err != nil {
		return VaultStatus{}, fmt.Errorf("delete vault metadata: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return VaultStatus{}, fmt.Errorf("commit vault disable: %w", err)
	}

	s.mu.Lock()
	s.vaultMeta = nil
	s.vaultDEK = nil
	s.mu.Unlock()
	return vaultStatusFromState(nil, false), nil
}

func (s *AuthStore) encodeStoredCredential(record AuthCredentialRecord) ([]byte, error) {
	meta, unlocked, err := s.snapshotVaultState()
	if err != nil {
		return nil, err
	}
	if meta == nil || !meta.Enabled {
		return json.Marshal(record)
	}
	if !unlocked {
		return nil, ErrVaultLocked
	}
	s.mu.RLock()
	dek := append([]byte(nil), s.vaultDEK...)
	s.mu.RUnlock()
	return encodeSealedCredential(dek, record)
}

func (s *AuthStore) decodeStoredCredential(payload []byte) (AuthCredentialRecord, error) {
	meta, unlocked, err := s.snapshotVaultState()
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	if meta == nil || !meta.Enabled {
		var record AuthCredentialRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return AuthCredentialRecord{}, err
		}
		return record, nil
	}
	if !unlocked {
		return AuthCredentialRecord{}, ErrVaultLocked
	}
	s.mu.RLock()
	dek := append([]byte(nil), s.vaultDEK...)
	s.mu.RUnlock()
	return decodeSealedCredential(dek, payload)
}

func (s *AuthStore) loadVaultMetaLocked() (*VaultMetadata, error) {
	if s.vaultMeta != nil {
		return s.vaultMeta, nil
	}
	payload, ok, err := s.store.GetBytes(KeyAuthVaultMeta)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var meta VaultMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return nil, fmt.Errorf("decode vault metadata: %w", err)
	}
	if !meta.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(meta.StorageMode) == "" {
		meta.StorageMode = storageModePebbleVault
	}
	s.vaultMeta = &meta
	return s.vaultMeta, nil
}

func (s *AuthStore) snapshotVaultState() (*VaultMetadata, bool, error) {
	s.mu.RLock()
	if s.vaultMeta != nil {
		meta := *s.vaultMeta
		unlocked := len(s.vaultDEK) > 0
		s.mu.RUnlock()
		return &meta, unlocked, nil
	}
	s.mu.RUnlock()

	meta, err := s.readVaultMeta()
	if err != nil {
		return nil, false, err
	}
	if meta == nil {
		return nil, false, nil
	}

	s.mu.Lock()
	if s.vaultMeta == nil {
		s.vaultMeta = meta
	}
	unlocked := len(s.vaultDEK) > 0
	s.mu.Unlock()
	return meta, unlocked, nil
}

func (s *AuthStore) readVaultMeta() (*VaultMetadata, error) {
	payload, ok, err := s.store.GetBytes(KeyAuthVaultMeta)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var meta VaultMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return nil, fmt.Errorf("decode vault metadata: %w", err)
	}
	if !meta.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(meta.StorageMode) == "" {
		meta.StorageMode = storageModePebbleVault
	}
	return &meta, nil
}

func (s *AuthStore) listStoredCredentialsWithDEK(provider string, limit int, meta *VaultMetadata, dek []byte) ([]AuthCredentialRecord, error) {
	provider = normalizeProvider(provider)
	if limit <= 0 {
		limit = 200
	}

	prefix := AuthCredentialPrefix()
	if provider != "" {
		prefix = AuthCredentialProviderPrefix(provider)
	}
	records := make([]AuthCredentialRecord, 0, minInt(limit, 256))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var record AuthCredentialRecord
		var err error
		if meta != nil && meta.Enabled {
			record, err = decodeSealedCredential(dek, value)
		} else {
			err = json.Unmarshal(value, &record)
		}
		if err != nil {
			return err
		}
		records = append(records, normalizeCredentialRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func vaultStatusFromState(meta *VaultMetadata, unlocked bool) VaultStatus {
	if meta == nil || !meta.Enabled {
		return VaultStatus{
			Enabled:        false,
			Unlocked:       true,
			UnlockRequired: false,
			StorageMode:    storageModePebblePlain,
		}
	}
	return VaultStatus{
		Enabled:        true,
		Unlocked:       unlocked,
		UnlockRequired: !unlocked,
		StorageMode:    storageModePebbleVault,
		Warning:        strings.TrimSpace(meta.UnlockWarning),
	}
}

func deriveVaultKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, vaultArgon2Time, vaultArgon2MemoryKiB, vaultArgon2Parallelism, vaultDerivedKeyLength)
}

func unlockVaultDEK(password string, meta *VaultMetadata) ([]byte, error) {
	if meta == nil || !meta.Enabled {
		return nil, nil
	}
	salt, err := base64.StdEncoding.DecodeString(meta.SaltBase64)
	if err != nil {
		return nil, fmt.Errorf("decode vault salt: %w", err)
	}
	wrappedDEK, err := base64.StdEncoding.DecodeString(meta.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped vault key: %w", err)
	}
	kek := deriveVaultKey(password, salt)
	dek, err := decryptVaultBlob(kek, wrappedDEK)
	if err != nil {
		return nil, errors.New("invalid vault password")
	}
	return dek, nil
}

func encodeSealedCredential(dek []byte, record AuthCredentialRecord) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal vaulted credential: %w", err)
	}
	sealed, err := encryptVaultBlob(dek, raw)
	if err != nil {
		return nil, err
	}
	nonceLen := nonceSizeX
	if len(sealed) < nonceLen {
		return nil, errors.New("vault ciphertext is incomplete")
	}
	out, err := json.Marshal(sealedAuthCredentialRecord{
		Version:          vaultVersion,
		StorageMode:      storageModePebbleVault,
		NonceBase64:      base64.StdEncoding.EncodeToString(sealed[:nonceLen]),
		CiphertextBase64: base64.StdEncoding.EncodeToString(sealed[nonceLen:]),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal sealed auth record: %w", err)
	}
	return out, nil
}

func decodeSealedCredential(dek, payload []byte) (AuthCredentialRecord, error) {
	var sealed sealedAuthCredentialRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return AuthCredentialRecord{}, fmt.Errorf("decode sealed auth record: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.NonceBase64)
	if err != nil {
		return AuthCredentialRecord{}, fmt.Errorf("decode sealed auth nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.CiphertextBase64)
	if err != nil {
		return AuthCredentialRecord{}, fmt.Errorf("decode sealed auth ciphertext: %w", err)
	}
	raw, err := decryptVaultBlob(dek, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return AuthCredentialRecord{}, fmt.Errorf("decrypt vaulted credential: %w", err)
	}
	var record AuthCredentialRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return AuthCredentialRecord{}, fmt.Errorf("decode vaulted credential: %w", err)
	}
	return record, nil
}

func encryptVaultBlob(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create vault cipher: %w", err)
	}
	nonce, err := randomBytes(nonceSizeX)
	if err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

func decryptVaultBlob(key, sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create vault cipher: %w", err)
	}
	if len(sealed) < nonceSizeX {
		return nil, errors.New("vault payload is too short")
	}
	nonce := sealed[:nonceSizeX]
	ciphertext := sealed[nonceSizeX:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func randomBytes(length int) ([]byte, error) {
	if length <= 0 {
		return nil, errors.New("random length must be positive")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return buf, nil
}

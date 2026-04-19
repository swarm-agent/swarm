package pebblestore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	vaultVersion                 = 2
	vaultArgon2Time              = uint32(1)
	vaultArgon2MemoryKiB         = uint32(64 * 1024)
	vaultArgon2Parallelism       = uint8(4)
	vaultDerivedKeyLength        = uint32(32)
	nonceSizeX                   = chacha20poly1305.NonceSizeX
	storageModePebbleEncrypted   = "pebble/encrypted"
	storageModePebbleVault       = "pebble/vault"
	vaultEnableWarning           = ""
	vaultUnlockRequiredMessage   = "vault is locked; unlock it first"
	vaultPasswordRequiredMessage = "vault password is required"
)

var ErrVaultLocked = errors.New(vaultUnlockRequiredMessage)

type VaultMetadata struct {
	Version           int    `json:"version"`
	KDF               string `json:"kdf,omitempty"`
	SaltBase64        string `json:"salt_base64,omitempty"`
	MemoryKiB         uint32 `json:"memory_kib,omitempty"`
	TimeCost          uint32 `json:"time_cost,omitempty"`
	Parallelism       uint8  `json:"parallelism,omitempty"`
	WrappedDEK        string `json:"wrapped_dek,omitempty"`
	ManagedWrappedDEK string `json:"managed_wrapped_dek,omitempty"`
	LocalWrappedDEK   string `json:"local_wrapped_dek,omitempty"`
	UpdatedAt         int64  `json:"updated_at"`
	Enabled           bool   `json:"enabled"`
	StorageMode       string `json:"storage_mode"`
	UnlockWarning     string `json:"unlock_warning,omitempty"`
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
		return storageModePebbleEncrypted
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
	return s.enableVaultWithManagedKey(password, "")
}

func (s *AuthStore) enableVaultWithManagedKey(password, managedKey string) (VaultStatus, error) {
	password = strings.TrimSpace(password)
	managedKey = strings.TrimSpace(managedKey)
	if password == "" {
		return VaultStatus{}, errors.New(vaultPasswordRequiredMessage)
	}

	if _, migrated, err := s.migrateLegacyCodexRecord(); err != nil {
		return VaultStatus{}, err
	} else if migrated {
		s.clearVaultMetadataCache()
	}

	meta, dek, err := s.ensureSecretDEK()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta != nil && meta.Enabled {
		return VaultStatus{}, errors.New("vault is already enabled")
	}

	records, err := s.ListCredentials("", 10_000)
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
	managedWrappedDEK := ""
	if managedKey != "" {
		wrapped, err := encryptVaultBlob(managedVaultKEK(managedKey), dek)
		if err != nil {
			return VaultStatus{}, err
		}
		managedWrappedDEK = base64.StdEncoding.EncodeToString(wrapped)
	}

	now := time.Now().UnixMilli()
	nextMeta := &VaultMetadata{
		Version:           vaultVersion,
		KDF:               "argon2id",
		SaltBase64:        base64.StdEncoding.EncodeToString(salt),
		MemoryKiB:         vaultArgon2MemoryKiB,
		TimeCost:          vaultArgon2Time,
		Parallelism:       vaultArgon2Parallelism,
		WrappedDEK:        base64.StdEncoding.EncodeToString(wrappedDEK),
		ManagedWrappedDEK: managedWrappedDEK,
		UpdatedAt:         now,
		Enabled:           true,
		StorageMode:       storageModePebbleVault,
		UnlockWarning:     vaultEnableWarning,
	}

	batch := s.secretStore.NewBatch()
	defer batch.Close()
	for _, record := range records {
		payload, err := encodeSealedCredential(dek, record, storageModePebbleVault)
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

	s.cacheVaultState(nextMeta, dek, nil)
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
		return vaultStatusFromState(meta, true), nil
	}

	dek, err := unlockVaultDEK(password, meta)
	if err != nil {
		return VaultStatus{}, err
	}
	s.cacheVaultState(meta, dek, nil)
	return vaultStatusFromState(meta, true), nil
}

func (s *AuthStore) ConfigureManagedVaultAccess(password, managedKey string) (VaultStatus, error) {
	password = strings.TrimSpace(password)
	managedKey = strings.TrimSpace(managedKey)
	if managedKey == "" {
		return VaultStatus{}, errors.New("managed vault key is required")
	}

	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta == nil || !meta.Enabled {
		return s.enableVaultWithManagedKey(password, managedKey)
	}

	dek, err := s.readableDEK()
	if err != nil {
		return VaultStatus{}, err
	}
	nextMeta := *meta
	if password != "" {
		salt, err := randomBytes(16)
		if err != nil {
			return VaultStatus{}, err
		}
		kek := deriveVaultKey(password, salt)
		wrappedDEK, err := encryptVaultBlob(kek, dek)
		if err != nil {
			return VaultStatus{}, err
		}
		nextMeta.KDF = "argon2id"
		nextMeta.SaltBase64 = base64.StdEncoding.EncodeToString(salt)
		nextMeta.MemoryKiB = vaultArgon2MemoryKiB
		nextMeta.TimeCost = vaultArgon2Time
		nextMeta.Parallelism = vaultArgon2Parallelism
		nextMeta.WrappedDEK = base64.StdEncoding.EncodeToString(wrappedDEK)
	}
	managedWrappedDEK, err := encryptVaultBlob(managedVaultKEK(managedKey), dek)
	if err != nil {
		return VaultStatus{}, err
	}
	nextMeta.ManagedWrappedDEK = base64.StdEncoding.EncodeToString(managedWrappedDEK)
	nextMeta.UpdatedAt = time.Now().UnixMilli()
	nextMeta.Enabled = true
	nextMeta.StorageMode = storageModePebbleVault
	if err := s.persistVaultMetadata(&nextMeta); err != nil {
		return VaultStatus{}, err
	}
	s.cacheVaultState(&nextMeta, dek, nil)
	return vaultStatusFromState(&nextMeta, true), nil
}

func (s *AuthStore) LockVault() (VaultStatus, error) {
	meta, _, err := s.snapshotVaultState()
	if err != nil {
		return VaultStatus{}, err
	}
	if meta == nil || !meta.Enabled {
		return vaultStatusFromState(meta, true), nil
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
		return vaultStatusFromState(meta, true), nil
	}
	if password == "" {
		return VaultStatus{}, errors.New(vaultPasswordRequiredMessage)
	}

	dek, err := unlockVaultDEK(password, meta)
	if err != nil {
		return VaultStatus{}, err
	}
	localRootKey, err := s.loadOrCreateLocalRootKey()
	if err != nil {
		return VaultStatus{}, err
	}
	localWrappedDEK, err := encryptVaultBlob(localRootKey, dek)
	if err != nil {
		return VaultStatus{}, err
	}

	nextMeta := &VaultMetadata{
		Version:         vaultVersion,
		LocalWrappedDEK: base64.StdEncoding.EncodeToString(localWrappedDEK),
		UpdatedAt:       time.Now().UnixMilli(),
		Enabled:         false,
		StorageMode:     storageModePebbleEncrypted,
	}
	if err := s.persistVaultMetadata(nextMeta); err != nil {
		return VaultStatus{}, err
	}
	s.cacheVaultState(nextMeta, dek, localRootKey)
	return vaultStatusFromState(nextMeta, true), nil
}

func (s *AuthStore) encodeStoredCredential(record AuthCredentialRecord) ([]byte, error) {
	meta, dek, err := s.ensureSecretDEK()
	if err != nil {
		return nil, err
	}
	mode := storageModePebbleEncrypted
	if meta != nil && meta.Enabled {
		mode = storageModePebbleVault
	}
	return encodeSealedCredential(dek, record, mode)
}

func (s *AuthStore) decodeStoredCredential(payload []byte) (AuthCredentialRecord, error) {
	if !isSealedCredentialPayload(payload) {
		var record AuthCredentialRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return AuthCredentialRecord{}, err
		}
		return record, nil
	}

	dek, err := s.readableDEK()
	if err != nil {
		return AuthCredentialRecord{}, err
	}
	return decodeSealedCredential(dek, payload)
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
	payload, ok, err := s.secretStore.GetBytes(KeyAuthVaultMeta)
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
	if strings.TrimSpace(meta.StorageMode) == "" {
		if meta.Enabled {
			meta.StorageMode = storageModePebbleVault
		} else {
			meta.StorageMode = storageModePebbleEncrypted
		}
	}
	return &meta, nil
}

func (s *AuthStore) ensureSecretDEK() (*VaultMetadata, []byte, error) {
	meta, unlocked, err := s.snapshotVaultState()
	if err != nil {
		return nil, nil, err
	}
	if meta == nil {
		return s.initializeEncryptedStore()
	}
	if meta.Enabled {
		if !unlocked {
			return nil, nil, ErrVaultLocked
		}
		return meta, s.copyVaultDEK(), nil
	}
	if unlocked {
		return meta, s.copyVaultDEK(), nil
	}

	localRootKey, err := s.loadOrCreateLocalRootKey()
	if err != nil {
		return nil, nil, err
	}
	dek, err := unlockLocalDEK(localRootKey, meta)
	if err != nil {
		return nil, nil, err
	}
	s.cacheVaultState(meta, dek, localRootKey)
	return meta, append([]byte(nil), dek...), nil
}

func (s *AuthStore) readableDEK() ([]byte, error) {
	meta, unlocked, err := s.snapshotVaultState()
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, errors.New("sealed auth record is missing vault metadata")
	}
	if meta.Enabled {
		if !unlocked {
			return nil, ErrVaultLocked
		}
		return s.copyVaultDEK(), nil
	}
	if unlocked {
		return s.copyVaultDEK(), nil
	}
	localRootKey, err := s.loadOrCreateLocalRootKey()
	if err != nil {
		return nil, err
	}
	dek, err := unlockLocalDEK(localRootKey, meta)
	if err != nil {
		return nil, err
	}
	s.cacheVaultState(meta, dek, localRootKey)
	return append([]byte(nil), dek...), nil
}

func (s *AuthStore) initializeEncryptedStore() (*VaultMetadata, []byte, error) {
	localRootKey, err := s.loadOrCreateLocalRootKey()
	if err != nil {
		return nil, nil, err
	}
	dek, err := randomBytes(int(vaultDerivedKeyLength))
	if err != nil {
		return nil, nil, err
	}
	localWrappedDEK, err := encryptVaultBlob(localRootKey, dek)
	if err != nil {
		return nil, nil, err
	}
	meta := &VaultMetadata{
		Version:         vaultVersion,
		LocalWrappedDEK: base64.StdEncoding.EncodeToString(localWrappedDEK),
		UpdatedAt:       time.Now().UnixMilli(),
		Enabled:         false,
		StorageMode:     storageModePebbleEncrypted,
	}
	if err := s.persistVaultMetadata(meta); err != nil {
		return nil, nil, err
	}
	s.cacheVaultState(meta, dek, localRootKey)
	return meta, append([]byte(nil), dek...), nil
}

func (s *AuthStore) persistVaultMetadata(meta *VaultMetadata) error {
	if meta == nil {
		return errors.New("vault metadata is required")
	}
	return s.secretStore.PutJSON(KeyAuthVaultMeta, meta)
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
	err := s.secretStore.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var (
			record AuthCredentialRecord
			err    error
		)
		if isSealedCredentialPayload(value) {
			if len(dek) == 0 {
				if meta != nil && meta.Enabled {
					return ErrVaultLocked
				}
				return errors.New("credential decryption key is required")
			}
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
			StorageMode:    storageModePebbleEncrypted,
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
	if strings.TrimSpace(password) == "" {
		return nil, errors.New(vaultPasswordRequiredMessage)
	}
	if dek, err := unlockVaultDEKWithPassword(password, meta); err == nil {
		return dek, nil
	}
	if dek, err := unlockVaultDEKWithManagedKey(password, meta); err == nil {
		return dek, nil
	}
	return nil, errors.New("invalid vault password")
}

func unlockVaultDEKWithPassword(password string, meta *VaultMetadata) ([]byte, error) {
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
		return nil, err
	}
	return dek, nil
}

func unlockVaultDEKWithManagedKey(managedKey string, meta *VaultMetadata) ([]byte, error) {
	if meta == nil || !meta.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(meta.ManagedWrappedDEK) == "" {
		return nil, errors.New("managed vault key wrapper is missing")
	}
	wrappedDEK, err := base64.StdEncoding.DecodeString(meta.ManagedWrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("decode managed wrapped key: %w", err)
	}
	dek, err := decryptVaultBlob(managedVaultKEK(managedKey), wrappedDEK)
	if err != nil {
		return nil, err
	}
	return dek, nil
}

func managedVaultKEK(managedKey string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(managedKey)))
	return append([]byte(nil), sum[:]...)
}

func unlockLocalDEK(localRootKey []byte, meta *VaultMetadata) ([]byte, error) {
	if len(localRootKey) == 0 {
		return nil, errors.New("local secret root key is required")
	}
	if meta == nil {
		return nil, errors.New("vault metadata is required")
	}
	if strings.TrimSpace(meta.LocalWrappedDEK) == "" {
		return nil, errors.New("local secret store key wrapper is missing")
	}
	wrappedDEK, err := base64.StdEncoding.DecodeString(meta.LocalWrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("decode local wrapped key: %w", err)
	}
	dek, err := decryptVaultBlob(localRootKey, wrappedDEK)
	if err != nil {
		return nil, errors.New("local secret store key is invalid")
	}
	return dek, nil
}

func encodeSealedCredential(dek []byte, record AuthCredentialRecord, storageMode string) ([]byte, error) {
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
	if strings.TrimSpace(storageMode) == "" {
		storageMode = storageModePebbleEncrypted
	}
	out, err := json.Marshal(sealedAuthCredentialRecord{
		Version:          vaultVersion,
		StorageMode:      storageMode,
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

func isSealedCredentialPayload(payload []byte) bool {
	var sealed sealedAuthCredentialRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return false
	}
	mode := strings.TrimSpace(sealed.StorageMode)
	return strings.TrimSpace(sealed.NonceBase64) != "" &&
		strings.TrimSpace(sealed.CiphertextBase64) != "" &&
		(mode == "" || mode == storageModePebbleEncrypted || mode == storageModePebbleVault)
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

func (s *AuthStore) loadOrCreateLocalRootKey() ([]byte, error) {
	s.mu.RLock()
	if len(s.localRootKey) > 0 {
		key := append([]byte(nil), s.localRootKey...)
		s.mu.RUnlock()
		return key, nil
	}
	s.mu.RUnlock()

	path := strings.TrimSpace(s.localKeyPath)
	if path == "" {
		return nil, errors.New("local secret key path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create local secret key directory: %w", err)
	}
	if key, err := s.readLocalRootKey(path); err == nil {
		s.mu.Lock()
		s.localRootKey = append([]byte(nil), key...)
		s.mu.Unlock()
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	key, err := randomBytes(int(vaultDerivedKeyLength))
	if err != nil {
		return nil, err
	}
	payload := []byte(base64.StdEncoding.EncodeToString(key))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return s.loadOrCreateLocalRootKey()
		}
		return nil, fmt.Errorf("create local secret key: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write local secret key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close local secret key: %w", err)
	}
	s.mu.Lock()
	s.localRootKey = append([]byte(nil), key...)
	s.mu.Unlock()
	return append([]byte(nil), key...), nil
}

func (s *AuthStore) readLocalRootKey(path string) ([]byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("decode local secret key: %w", err)
	}
	if len(key) != int(vaultDerivedKeyLength) {
		return nil, fmt.Errorf("local secret key length = %d, want %d", len(key), vaultDerivedKeyLength)
	}
	return key, nil
}

func (s *AuthStore) cacheVaultState(meta *VaultMetadata, dek, localRootKey []byte) {
	s.mu.Lock()
	if meta == nil {
		s.vaultMeta = nil
	} else {
		copyMeta := *meta
		s.vaultMeta = &copyMeta
	}
	s.vaultDEK = append([]byte(nil), dek...)
	if len(localRootKey) > 0 {
		s.localRootKey = append([]byte(nil), localRootKey...)
	}
	s.mu.Unlock()
}

func (s *AuthStore) clearVaultMetadataCache() {
	s.mu.Lock()
	s.vaultMeta = nil
	s.vaultDEK = nil
	s.mu.Unlock()
}

func (s *AuthStore) copyVaultDEK() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.vaultDEK...)
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

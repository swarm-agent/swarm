package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type managedVaultKeyRecord struct {
	ScopeID   string `json:"scope_id"`
	Key       string `json:"key"`
	UpdatedAt int64  `json:"updated_at"`
}

type sealedManagedVaultKeyRecord struct {
	Version          int    `json:"version"`
	StorageMode      string `json:"storage_mode"`
	NonceBase64      string `json:"nonce_base64"`
	CiphertextBase64 string `json:"ciphertext_base64"`
}

func (s *AuthStore) PutManagedVaultKey(scopeID, managedKey string) error {
	scopeID = strings.TrimSpace(scopeID)
	managedKey = strings.TrimSpace(managedKey)
	if scopeID == "" {
		return errors.New("managed vault key scope is required")
	}
	if managedKey == "" {
		return errors.New("managed vault key is required")
	}
	meta, dek, err := s.ensureSecretDEK()
	if err != nil {
		return err
	}
	payload, err := encodeManagedVaultKeyPayload(dek, managedVaultKeyRecord{
		ScopeID:   scopeID,
		Key:       managedKey,
		UpdatedAt: time.Now().UnixMilli(),
	}, meta)
	if err != nil {
		return err
	}
	return s.secretStore.PutBytes(KeyAuthManagedVaultKey(scopeID), payload)
}

func (s *AuthStore) ManagedVaultKey(scopeID string) (string, bool, error) {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return "", false, errors.New("managed vault key scope is required")
	}
	payload, ok, err := s.secretStore.GetBytes(KeyAuthManagedVaultKey(scopeID))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	dek, err := s.readableDEK()
	if err != nil {
		return "", false, err
	}
	record, err := decodeManagedVaultKeyPayload(dek, payload)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(record.Key), strings.TrimSpace(record.Key) != "", nil
}

func (s *AuthStore) DeleteManagedVaultKey(scopeID string) error {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return errors.New("managed vault key scope is required")
	}
	return s.secretStore.Delete(KeyAuthManagedVaultKey(scopeID))
}

func encodeManagedVaultKeyPayload(dek []byte, record managedVaultKeyRecord, meta *VaultMetadata) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal managed vault key: %w", err)
	}
	sealed, err := encryptVaultBlob(dek, raw)
	if err != nil {
		return nil, err
	}
	if len(sealed) < nonceSizeX {
		return nil, errors.New("managed vault key ciphertext is incomplete")
	}
	storageMode := storageModePebbleEncrypted
	if meta != nil && meta.Enabled {
		storageMode = storageModePebbleVault
	}
	payload, err := json.Marshal(sealedManagedVaultKeyRecord{
		Version:          vaultVersion,
		StorageMode:      storageMode,
		NonceBase64:      base64.StdEncoding.EncodeToString(sealed[:nonceSizeX]),
		CiphertextBase64: base64.StdEncoding.EncodeToString(sealed[nonceSizeX:]),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal sealed managed vault key: %w", err)
	}
	return payload, nil
}

func decodeManagedVaultKeyPayload(dek, payload []byte) (managedVaultKeyRecord, error) {
	var sealed sealedManagedVaultKeyRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return managedVaultKeyRecord{}, fmt.Errorf("decode sealed managed vault key: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.NonceBase64)
	if err != nil {
		return managedVaultKeyRecord{}, fmt.Errorf("decode managed vault key nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.CiphertextBase64)
	if err != nil {
		return managedVaultKeyRecord{}, fmt.Errorf("decode managed vault key ciphertext: %w", err)
	}
	raw, err := decryptVaultBlob(dek, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return managedVaultKeyRecord{}, fmt.Errorf("decrypt managed vault key: %w", err)
	}
	var record managedVaultKeyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return managedVaultKeyRecord{}, fmt.Errorf("decode managed vault key record: %w", err)
	}
	return record, nil
}

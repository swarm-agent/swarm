package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// KeyAuthSecretValuePrefix holds secret slot values in the secret store,
// sealed with the account's data encryption key exactly like provider
// credentials. Slot metadata (name, hosts, grants) lives in the main store;
// only the value is here.
const KeyAuthSecretValuePrefix = "auth/secret_value/"

const maxSecretValueBytes = 16 << 10

func authSecretValueKey(accountScopeID, name string) string {
	return KeyAuthSecretValuePrefix + keyPart(accountScopeID) + "/" + keyPart(name)
}

// PutSecretValueForAccount seals and stores a secret slot's value. The value
// never leaves the daemon except through the egress gateway's injection.
func (s *AuthStore) PutSecretValueForAccount(accountScopeID, name string, value []byte) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("secret name is required")
	}
	if len(value) == 0 || len(value) > maxSecretValueBytes {
		return fmt.Errorf("secret value must be 1 to %d bytes", maxSecretValueBytes)
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
		return fmt.Errorf("marshal sealed secret value: %w", err)
	}
	return s.secretStore.PutBytes(authSecretValueKey(accountScopeID, name), payload)
}

// GetSecretValueForAccount opens a secret slot's value. It returns
// ErrVaultLocked while a password vault is locked.
func (s *AuthStore) GetSecretValueForAccount(accountScopeID, name string) ([]byte, bool, error) {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return nil, false, err
	}
	payload, ok, err := s.secretStore.GetBytes(authSecretValueKey(accountScopeID, name))
	if err != nil || !ok {
		return nil, ok, err
	}
	var sealed sealedAuthCredentialRecord
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return nil, false, fmt.Errorf("decode sealed secret value: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.NonceBase64)
	if err != nil {
		return nil, false, fmt.Errorf("decode sealed secret nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.CiphertextBase64)
	if err != nil {
		return nil, false, fmt.Errorf("decode sealed secret ciphertext: %w", err)
	}
	_, dek, err := s.ensureSecretDEKForAccount(accountScopeID)
	if err != nil {
		return nil, false, err
	}
	value, err := decryptVaultBlob(dek, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return nil, false, fmt.Errorf("decrypt secret value: %w", err)
	}
	return value, true, nil
}

// DeleteSecretValueForAccount removes a secret slot's value.
func (s *AuthStore) DeleteSecretValueForAccount(accountScopeID, name string) error {
	accountScopeID, err := requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	return s.secretStore.Delete(authSecretValueKey(accountScopeID, name))
}

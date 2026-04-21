package pebblestore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	credentialBundleFormat  = "swarm.credentials.bundle"
	credentialBundleVersion = 1
)

type CredentialBundle struct {
	Format              string                 `json:"format"`
	Version             int                    `json:"version"`
	ExportedAt          int64                  `json:"exported_at"`
	SnapshotHash        string                 `json:"snapshot_hash,omitempty"`
	ActiveCredentialIDs map[string]string      `json:"active_credential_ids,omitempty"`
	Credentials         []AuthCredentialRecord `json:"credentials"`
}

type EncryptedCredentialBundle struct {
	Format           string `json:"format"`
	Version          int    `json:"version"`
	Encryption       string `json:"encryption"`
	KDF              string `json:"kdf"`
	SaltBase64       string `json:"salt_base64"`
	MemoryKiB        uint32 `json:"memory_kib"`
	TimeCost         uint32 `json:"time_cost"`
	Parallelism      uint8  `json:"parallelism"`
	NonceBase64      string `json:"nonce_base64"`
	CiphertextBase64 string `json:"ciphertext_base64"`
}

type CredentialImportResult struct {
	Imported     int         `json:"imported"`
	Vault        VaultStatus `json:"vault"`
	SnapshotHash string      `json:"snapshot_hash,omitempty"`
}

type CredentialBundleMetadata struct {
	ExportedAt   int64  `json:"exported_at,omitempty"`
	SnapshotHash string `json:"snapshot_hash,omitempty"`
	Exported     int    `json:"exported"`
}

func (s *AuthStore) ExportCredentials(bundlePassword, vaultPassword string) ([]byte, int, error) {
	bundlePassword = strings.TrimSpace(bundlePassword)
	vaultPassword = strings.TrimSpace(vaultPassword)
	if bundlePassword == "" {
		return nil, 0, errors.New("bundle password is required")
	}

	status, err := s.VaultStatus()
	if err != nil {
		return nil, 0, err
	}
	if status.Enabled && !status.Unlocked {
		if vaultPassword == "" {
			return nil, 0, ErrVaultLocked
		}
		if _, err := s.UnlockVault(vaultPassword); err != nil {
			return nil, 0, err
		}
	}

	bundle, err := s.buildCredentialBundle()
	if err != nil {
		return nil, 0, err
	}
	rawBundle, err := json.Marshal(bundle)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal credential bundle: %w", err)
	}

	salt, err := randomBytes(16)
	if err != nil {
		return nil, 0, err
	}
	derivedKey := deriveVaultKey(bundlePassword, salt)
	sealed, err := encryptVaultBlob(derivedKey, rawBundle)
	if err != nil {
		return nil, 0, err
	}
	if len(sealed) < nonceSizeX {
		return nil, 0, errors.New("credential bundle ciphertext is incomplete")
	}

	envelope := EncryptedCredentialBundle{
		Format:           credentialBundleFormat,
		Version:          credentialBundleVersion,
		Encryption:       "xchacha20poly1305",
		KDF:              "argon2id",
		SaltBase64:       base64.StdEncoding.EncodeToString(salt),
		MemoryKiB:        vaultArgon2MemoryKiB,
		TimeCost:         vaultArgon2Time,
		Parallelism:      vaultArgon2Parallelism,
		NonceBase64:      base64.StdEncoding.EncodeToString(sealed[:nonceSizeX]),
		CiphertextBase64: base64.StdEncoding.EncodeToString(sealed[nonceSizeX:]),
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal encrypted credential bundle: %w", err)
	}
	return out, len(bundle.Credentials), nil
}

func (s *AuthStore) CredentialBundleMetadata(bundlePassword string, payload []byte) (CredentialBundleMetadata, error) {
	bundle, err := s.readCredentialBundle(bundlePassword, payload)
	if err != nil {
		return CredentialBundleMetadata{}, err
	}
	return CredentialBundleMetadata{
		ExportedAt:   bundle.ExportedAt,
		SnapshotHash: strings.TrimSpace(bundle.SnapshotHash),
		Exported:     len(bundle.Credentials),
	}, nil
}

func (s *AuthStore) ImportCredentials(bundlePassword, vaultPassword string, payload []byte) (CredentialImportResult, error) {
	return s.importCredentialBundle(bundlePassword, vaultPassword, "", false, payload)
}

func (s *AuthStore) ImportManagedCredentials(ownerSwarmID, bundlePassword, vaultPassword string, payload []byte) (CredentialImportResult, error) {
	return s.ImportManagedCredentialsWithVaultAccess(ownerSwarmID, bundlePassword, vaultPassword, "", payload)
}

func (s *AuthStore) ImportManagedCredentialsWithVaultAccess(ownerSwarmID, bundlePassword, vaultPassword, managedVaultKey string, payload []byte) (CredentialImportResult, error) {
	ownerSwarmID = strings.TrimSpace(ownerSwarmID)
	if ownerSwarmID == "" {
		return CredentialImportResult{}, errors.New("owner swarm id is required")
	}
	return s.importCredentialBundleWithManagedVault(bundlePassword, vaultPassword, managedVaultKey, ownerSwarmID, true, payload)
}

func (s *AuthStore) importCredentialBundle(bundlePassword, vaultPassword, ownerSwarmID string, managed bool, payload []byte) (CredentialImportResult, error) {
	return s.importCredentialBundleWithManagedVault(bundlePassword, vaultPassword, "", ownerSwarmID, managed, payload)
}

func (s *AuthStore) importCredentialBundleWithManagedVault(bundlePassword, vaultPassword, managedVaultKey, ownerSwarmID string, managed bool, payload []byte) (CredentialImportResult, error) {
	bundlePassword = strings.TrimSpace(bundlePassword)
	vaultPassword = strings.TrimSpace(vaultPassword)
	managedVaultKey = strings.TrimSpace(managedVaultKey)
	if bundlePassword == "" {
		return CredentialImportResult{}, errors.New("bundle password is required")
	}
	if len(payload) == 0 {
		return CredentialImportResult{}, errors.New("credential bundle is empty")
	}

	bundle, err := s.readCredentialBundle(bundlePassword, payload)
	if err != nil {
		return CredentialImportResult{}, err
	}

	status, err := s.VaultStatus()
	if err != nil {
		return CredentialImportResult{}, err
	}
	if !status.Enabled {
		if managedVaultKey != "" {
			status, err = s.ConfigureManagedVaultAccess(vaultPassword, managedVaultKey)
			if err != nil {
				return CredentialImportResult{}, err
			}
		} else if vaultPassword != "" {
			status, err = s.EnableVault(vaultPassword)
			if err != nil {
				return CredentialImportResult{}, err
			}
		}
	} else if managedVaultKey != "" {
		status, err = s.ConfigureManagedVaultAccess(vaultPassword, managedVaultKey)
		if err != nil {
			return CredentialImportResult{}, err
		}
	} else if !status.Unlocked {
		if vaultPassword == "" {
			return CredentialImportResult{}, ErrVaultLocked
		}
		status, err = s.UnlockVault(vaultPassword)
		if err != nil {
			return CredentialImportResult{}, err
		}
	}

	imported := 0
	incomingManaged := make(map[string]struct{}, len(bundle.Credentials))
	for _, record := range bundle.Credentials {
		record = normalizeCredentialRecord(record)
		if managed {
			record.Managed = true
			record.OwnerSwarmID = ownerSwarmID
			incomingManaged[managedCredentialBundleKey(record.Provider, record.ID)] = struct{}{}
		}
		if _, err := s.saveCredential(record, false); err != nil {
			return CredentialImportResult{}, fmt.Errorf("import credential %s/%s: %w", record.Provider, record.ID, err)
		}
		imported++
	}
	if managed {
		existing, err := s.ListCredentials("", 10_000)
		if err != nil {
			return CredentialImportResult{}, err
		}
		for _, record := range existing {
			if strings.TrimSpace(record.OwnerSwarmID) != ownerSwarmID {
				continue
			}
			if _, ok := incomingManaged[managedCredentialBundleKey(record.Provider, record.ID)]; ok {
				continue
			}
			if _, err := s.deleteCredentialRecord(record); err != nil {
				return CredentialImportResult{}, err
			}
		}
		for provider, credentialID := range bundle.ActiveCredentialIDs {
			if normalizeProvider(provider) == "" || normalizeCredentialID(credentialID) == "" {
				continue
			}
			if _, ok := incomingManaged[managedCredentialBundleKey(provider, credentialID)]; !ok {
				continue
			}
			if _, err := s.SetActiveCredential(provider, credentialID); err != nil {
				return CredentialImportResult{}, err
			}
		}
	}
	status, err = s.VaultStatus()
	if err != nil {
		return CredentialImportResult{}, err
	}
	return CredentialImportResult{Imported: imported, Vault: status, SnapshotHash: bundle.SnapshotHash}, nil
}

func (s *AuthStore) buildCredentialBundle() (CredentialBundle, error) {
	records, err := s.ListCredentials("", 10_000)
	if err != nil {
		return CredentialBundle{}, err
	}
	activeCredentialIDs, err := s.activeCredentialIDsForRecords(records)
	if err != nil {
		return CredentialBundle{}, err
	}
	bundle := CredentialBundle{
		Format:              credentialBundleFormat,
		Version:             credentialBundleVersion,
		ExportedAt:          time.Now().UnixMilli(),
		ActiveCredentialIDs: activeCredentialIDs,
		Credentials:         records,
	}
	bundle.SnapshotHash, err = credentialBundleSnapshotHash(bundle)
	if err != nil {
		return CredentialBundle{}, err
	}
	return bundle, nil
}

func (s *AuthStore) readCredentialBundle(bundlePassword string, payload []byte) (CredentialBundle, error) {
	return decryptCredentialBundle(bundlePassword, payload)
}

func (s *AuthStore) activeCredentialIDsForRecords(records []AuthCredentialRecord) (map[string]string, error) {
	seen := make(map[string]struct{}, len(records))
	activeCredentialIDs := make(map[string]string, len(records))
	for _, record := range records {
		provider := normalizeProvider(record.Provider)
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		credentialID, ok, err := s.getActiveCredentialID(provider)
		if err != nil {
			return nil, err
		}
		if !ok || normalizeCredentialID(credentialID) == "" {
			continue
		}
		activeCredentialIDs[provider] = normalizeCredentialID(credentialID)
	}
	if len(activeCredentialIDs) == 0 {
		return nil, nil
	}
	return activeCredentialIDs, nil
}

func decryptCredentialBundle(bundlePassword string, payload []byte) (CredentialBundle, error) {
	var envelope EncryptedCredentialBundle
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return CredentialBundle{}, fmt.Errorf("decode credential bundle: %w", err)
	}
	if strings.TrimSpace(envelope.Format) != credentialBundleFormat {
		return CredentialBundle{}, errors.New("unsupported credential bundle format")
	}
	if envelope.Version != credentialBundleVersion {
		return CredentialBundle{}, fmt.Errorf("unsupported credential bundle version: %d", envelope.Version)
	}
	if strings.TrimSpace(envelope.KDF) != "argon2id" {
		return CredentialBundle{}, errors.New("unsupported credential bundle kdf")
	}
	if strings.TrimSpace(envelope.Encryption) != "xchacha20poly1305" {
		return CredentialBundle{}, errors.New("unsupported credential bundle encryption")
	}

	salt, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.SaltBase64))
	if err != nil {
		return CredentialBundle{}, fmt.Errorf("decode credential bundle salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.NonceBase64))
	if err != nil {
		return CredentialBundle{}, fmt.Errorf("decode credential bundle nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.CiphertextBase64))
	if err != nil {
		return CredentialBundle{}, fmt.Errorf("decode credential bundle ciphertext: %w", err)
	}

	derivedKey := deriveVaultKey(bundlePassword, salt)
	rawBundle, err := decryptVaultBlob(derivedKey, append(append([]byte(nil), nonce...), ciphertext...))
	if err != nil {
		return CredentialBundle{}, errors.New("invalid credential bundle password")
	}

	var bundle CredentialBundle
	if err := json.Unmarshal(rawBundle, &bundle); err != nil {
		return CredentialBundle{}, fmt.Errorf("decode credential bundle payload: %w", err)
	}
	if strings.TrimSpace(bundle.Format) != credentialBundleFormat {
		return CredentialBundle{}, errors.New("unsupported credential bundle payload format")
	}
	if bundle.Version != credentialBundleVersion {
		return CredentialBundle{}, fmt.Errorf("unsupported credential bundle payload version: %d", bundle.Version)
	}
	for i, record := range bundle.Credentials {
		bundle.Credentials[i] = normalizeCredentialRecord(record)
	}
	if len(bundle.ActiveCredentialIDs) > 0 {
		normalized := make(map[string]string, len(bundle.ActiveCredentialIDs))
		for provider, credentialID := range bundle.ActiveCredentialIDs {
			provider = normalizeProvider(provider)
			credentialID = normalizeCredentialID(credentialID)
			if provider == "" || credentialID == "" {
				continue
			}
			normalized[provider] = credentialID
		}
		if len(normalized) == 0 {
			bundle.ActiveCredentialIDs = nil
		} else {
			bundle.ActiveCredentialIDs = normalized
		}
	}
	sortCredentials(bundle.Credentials)
	snapshotHash, err := credentialBundleSnapshotHash(bundle)
	if err != nil {
		return CredentialBundle{}, err
	}
	if strings.TrimSpace(bundle.SnapshotHash) == "" {
		bundle.SnapshotHash = snapshotHash
	} else if !strings.EqualFold(strings.TrimSpace(bundle.SnapshotHash), snapshotHash) {
		return CredentialBundle{}, errors.New("credential bundle snapshot hash mismatch")
	}
	return bundle, nil
}

func credentialBundleSnapshotHash(bundle CredentialBundle) (string, error) {
	snapshot := struct {
		Format              string                 `json:"format"`
		Version             int                    `json:"version"`
		ActiveCredentialIDs map[string]string      `json:"active_credential_ids,omitempty"`
		Credentials         []AuthCredentialRecord `json:"credentials"`
	}{
		Format:              credentialBundleFormat,
		Version:             credentialBundleVersion,
		ActiveCredentialIDs: bundle.ActiveCredentialIDs,
		Credentials:         append([]AuthCredentialRecord(nil), bundle.Credentials...),
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal credential bundle snapshot: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func managedCredentialBundleKey(provider, credentialID string) string {
	return normalizeProvider(provider) + "/" + normalizeCredentialID(credentialID)
}

func bundleProviderSummary(records []AuthCredentialRecord) []string {
	seen := make(map[string]struct{}, len(records))
	out := make([]string, 0, len(records))
	for _, record := range records {
		provider := normalizeProvider(record.Provider)
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}

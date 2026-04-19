package pebblestore_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openSplitAuthStore(t *testing.T, root string) (*pebblestore.AuthStore, *pebblestore.Store, *pebblestore.Store) {
	t.Helper()
	metaStore, err := pebblestore.Open(filepath.Join(root, "main.pebble"))
	if err != nil {
		t.Fatalf("open main store: %v", err)
	}
	secretStore, err := pebblestore.Open(filepath.Join(root, "secrets.pebble"))
	if err != nil {
		_ = metaStore.Close()
		t.Fatalf("open secret store: %v", err)
	}
	return pebblestore.NewAuthStoreWithSecretStore(metaStore, secretStore), metaStore, secretStore
}

func closeStores(t *testing.T, stores ...*pebblestore.Store) {
	t.Helper()
	for _, store := range stores {
		if store == nil {
			continue
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	}
}

func assertNoRawSecretOnDisk(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("found raw secret %q in %s", secret, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan files for raw secret: %v", err)
	}
}

func TestAuthStoreEncryptsSecretsBeforePebbleWrite(t *testing.T) {
	root := t.TempDir()
	authStore, metaStore, secretStore := openSplitAuthStore(t, root)

	const apiKey = "fw_encrypt_never_write_plaintext_123456"
	record, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    apiKey,
		SetActive: true,
	})
	if err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	if record.APIKey != apiKey {
		t.Fatalf("stored api key = %q, want %q", record.APIKey, apiKey)
	}

	status, err := authStore.VaultStatus()
	if err != nil {
		t.Fatalf("vault status: %v", err)
	}
	if status.Enabled {
		t.Fatalf("vault enabled = true, want false")
	}
	if status.StorageMode != "pebble/encrypted" {
		t.Fatalf("storage mode = %q, want pebble/encrypted", status.StorageMode)
	}

	closeStores(t, secretStore, metaStore)
	assertNoRawSecretOnDisk(t, root, apiKey)
}

func TestAuthStorePasswordVaultKeepsSecretsEncryptedAcrossRestart(t *testing.T) {
	root := t.TempDir()
	authStore, metaStore, secretStore := openSplitAuthStore(t, root)

	const (
		apiKey   = "fw_encrypt_password_mode_abcdef_123456"
		password = "vault-password"
	)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    apiKey,
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	if _, err := authStore.EnableVault(password); err != nil {
		t.Fatalf("enable vault: %v", err)
	}
	if _, err := authStore.LockVault(); err != nil {
		t.Fatalf("lock vault: %v", err)
	}

	closeStores(t, secretStore, metaStore)
	assertNoRawSecretOnDisk(t, root, apiKey)

	authStore, metaStore, secretStore = openSplitAuthStore(t, root)

	status, err := authStore.VaultStatus()
	if err != nil {
		t.Fatalf("vault status after reopen: %v", err)
	}
	if !status.Enabled {
		t.Fatalf("vault enabled after reopen = false, want true")
	}
	if status.Unlocked {
		t.Fatalf("vault unlocked after reopen = true, want false")
	}
	if status.StorageMode != "pebble/vault" {
		t.Fatalf("storage mode after reopen = %q, want pebble/vault", status.StorageMode)
	}

	_, _, err = authStore.GetActiveCredential("fireworks")
	if !errors.Is(err, pebblestore.ErrVaultLocked) {
		t.Fatalf("locked vault read error = %v, want ErrVaultLocked", err)
	}

	if _, err := authStore.UnlockVault(password); err != nil {
		t.Fatalf("unlock vault: %v", err)
	}
	record, ok, err := authStore.GetActiveCredential("fireworks")
	if err != nil {
		t.Fatalf("read unlocked credential: %v", err)
	}
	if !ok || record.APIKey != apiKey {
		t.Fatalf("active credential after unlock = %#v, want api key restored", record)
	}

	closeStores(t, secretStore, metaStore)
	assertNoRawSecretOnDisk(t, root, apiKey)
}

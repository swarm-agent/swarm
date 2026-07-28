package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyUnsealedCredentialIsResealedOnRead(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "metadata.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()

	auth := NewAuthStoreWithSecretStore(store, secrets)
	record := AuthCredentialRecord{AccountScopeID: "account-1", Provider: "openai", ID: "default", Type: AuthTypeAPI, APIKey: "test-secret"}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	key := authCredentialKey(record.AccountScopeID, record.Provider, record.ID)
	if err := secrets.PutBytes(key, payload); err != nil {
		t.Fatal(err)
	}

	got, ok, err := auth.GetCredentialForAccount(record.AccountScopeID, record.Provider, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.APIKey != record.APIKey {
		t.Fatalf("credential = %#v, found = %v", got, ok)
	}
	stored, ok, err := secrets.GetBytes(key)
	if err != nil || !ok {
		t.Fatalf("read resealed credential: found=%v err=%v", ok, err)
	}
	if !isSealedCredentialPayload(stored) {
		t.Fatal("legacy plaintext credential remained unsealed after successful read")
	}
}

func TestReadLocalRootKeyRejectsNonPrivateAndSymlinkFiles(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, vaultDerivedKeyLength)
	payload := []byte(base64.StdEncoding.EncodeToString(key))
	path := filepath.Join(dir, "root.key")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	auth := &AuthStore{}
	if _, err := auth.readLocalRootKey(path); err == nil {
		t.Fatal("accepted a group/world-readable local root key")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "root-link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.readLocalRootKey(link); err == nil {
		t.Fatal("accepted a symlink local root key")
	}
}

func TestCredentialBundlePreparationFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "metadata.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()

	auth := NewAuthStoreWithSecretStore(store, secrets)
	bundle := CredentialBundle{Credentials: []AuthCredentialRecord{
		{Provider: "openai", ID: "duplicate", Type: AuthTypeAPI, APIKey: "first", UpdatedAt: 1},
		{Provider: "openai", ID: "duplicate", Type: AuthTypeAPI, APIKey: "second", UpdatedAt: 2},
	}}
	auth.credentialMu.Lock()
	err = auth.importCredentialBundleLocked("account-1", bundle)
	auth.credentialMu.Unlock()
	if err == nil {
		t.Fatal("accepted a duplicate credential bundle")
	}
	if _, ok, err := secrets.GetBytes(authCredentialKey("account-1", "openai", "duplicate")); err != nil || ok {
		t.Fatalf("preparation failure wrote credential: found=%v err=%v", ok, err)
	}
}

func TestCredentialUpdatePathsDoNotReenterCredentialLock(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "metadata.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()

	auth := NewAuthStoreWithSecretStore(store, secrets)
	record, err := auth.UpsertCredential(AuthCredentialInput{AccountScopeID: "account-1", Provider: "openai", ID: "primary", Type: AuthTypeAPI, APIKey: "first", SetActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UpdateCredentialConnectionForAccount(record.AccountScopeID, record.Provider, record.ID, &AuthCredentialConnectionRecord{Connected: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SetActiveCredentialForAccount(record.AccountScopeID, record.Provider, record.ID); err != nil {
		t.Fatal(err)
	}
	if removed, err := auth.DeleteCredentialForAccount(record.AccountScopeID, record.Provider, record.ID); err != nil || !removed {
		t.Fatalf("delete credential: removed=%v err=%v", removed, err)
	}
}

func TestCredentialSaveCommitsSecretIndexesAndActivePointerTogether(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "metadata.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := Open(filepath.Join(dir, "secrets.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()

	auth := NewAuthStoreWithSecretStore(store, secrets)
	record, err := auth.saveCredential(AuthCredentialRecord{AccountScopeID: "account-1", Provider: "openai", ID: "primary", Type: AuthTypeAPI, APIKey: "test-secret", Tags: []string{"prod"}, UpdatedAt: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		authCredentialKey(record.AccountScopeID, record.Provider, record.ID),
		authCredentialTagKey(record.AccountScopeID, "prod", record.Provider, record.ID),
		authCredentialActiveKey(record.AccountScopeID, record.Provider),
	} {
		if _, ok, err := secrets.GetBytes(key); err != nil || !ok {
			t.Fatalf("secret transaction key %q: found=%v err=%v", key, ok, err)
		}
	}
	if _, ok, err := store.GetBytes(authCredentialActiveKey(record.AccountScopeID, record.Provider)); err != nil || ok {
		t.Fatalf("active pointer leaked into metadata store: found=%v err=%v", ok, err)
	}
}

package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestClientAuthStoreMigratesAttachTokenToSecretStore(t *testing.T) {
	root := t.TempDir()
	mainStore, err := Open(filepath.Join(root, "main"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()
	secretStore, err := Open(filepath.Join(root, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer secretStore.Close()

	legacy := AttachAuthRecord{Token: "legacy-token", CreatedAt: 1, UpdatedAt: 2}
	if err := mainStore.PutJSON(KeyAuthAttachDefault, legacy); err != nil {
		t.Fatal(err)
	}
	store := NewClientAuthStoreWithSecretStore(mainStore, secretStore)
	got, ok, err := store.GetAttachAuth()
	if err != nil {
		t.Fatalf("GetAttachAuth: %v", err)
	}
	if !ok || got.Token != legacy.Token {
		t.Fatalf("record = %#v, ok=%v", got, ok)
	}
	if _, ok, err := mainStore.GetBytes(KeyAuthAttachDefault); err != nil || ok {
		t.Fatalf("legacy record remains: ok=%v err=%v", ok, err)
	}
	var migrated AttachAuthRecord
	if ok, err := secretStore.GetJSON(KeyAuthAttachDefault, &migrated); err != nil || !ok || migrated.Token != legacy.Token {
		t.Fatalf("migrated record = %#v, ok=%v err=%v", migrated, ok, err)
	}
}

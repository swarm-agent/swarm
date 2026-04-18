package pebblestore_test

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openAuthStore(t *testing.T, name string) *pebblestore.AuthStore {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return pebblestore.NewAuthStore(store)
}

func TestImportManagedCredentialsKeepsPlainStorageAndReplacesSnapshot(t *testing.T) {
	const ownerSwarmID = "swarm_host"
	const bundlePassword = "bundle-password"

	hostStore := openAuthStore(t, "host-auth.pebble")
	if _, err := hostStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    "fw-primary-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert primary host credential: %v", err)
	}
	if _, err := hostStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-secondary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "secondary",
		APIKey:    "fw-secondary-key",
		SetActive: false,
	}); err != nil {
		t.Fatalf("upsert secondary host credential: %v", err)
	}
	if _, err := hostStore.SetActiveCredential("fireworks", "fw-secondary"); err != nil {
		t.Fatalf("set active host credential: %v", err)
	}

	initialBundle, exported, err := hostStore.ExportCredentials(bundlePassword, "")
	if err != nil {
		t.Fatalf("export initial credentials: %v", err)
	}
	if exported != 2 {
		t.Fatalf("initial export count = %d, want 2", exported)
	}

	childStore := openAuthStore(t, "child-auth.pebble")
	result, err := childStore.ImportManagedCredentials(ownerSwarmID, bundlePassword, "", initialBundle)
	if err != nil {
		t.Fatalf("import initial managed credentials: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("initial imported count = %d, want 2", result.Imported)
	}
	if result.SnapshotHash == "" {
		t.Fatalf("initial snapshot hash was empty")
	}
	childVault, err := childStore.VaultStatus()
	if err != nil {
		t.Fatalf("child vault status: %v", err)
	}
	if childVault.Enabled {
		t.Fatalf("child vault enabled = true, want false for plain-stage import")
	}
	if childVault.StorageMode != "pebble/plain" {
		t.Fatalf("child storage mode = %q, want pebble/plain", childVault.StorageMode)
	}
	childRecords, err := childStore.ListCredentials("fireworks", 10)
	if err != nil {
		t.Fatalf("list child credentials after initial import: %v", err)
	}
	if len(childRecords) != 2 {
		t.Fatalf("child credential count after initial import = %d, want 2", len(childRecords))
	}
	for _, record := range childRecords {
		if !record.Managed {
			t.Fatalf("child credential %s/%s managed = false, want true", record.Provider, record.ID)
		}
		if record.OwnerSwarmID != ownerSwarmID {
			t.Fatalf("child credential %s/%s owner = %q, want %q", record.Provider, record.ID, record.OwnerSwarmID, ownerSwarmID)
		}
	}
	active, ok, err := childStore.GetActiveCredential("fireworks")
	if err != nil {
		t.Fatalf("get child active credential after initial import: %v", err)
	}
	if !ok || active.ID != "fw-secondary" {
		t.Fatalf("child active credential after initial import = %#v, want fw-secondary", active)
	}

	if _, err := hostStore.DeleteCredential("fireworks", "fw-secondary"); err != nil {
		t.Fatalf("delete secondary host credential: %v", err)
	}
	if _, err := hostStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary-updated",
		APIKey:    "fw-primary-key-updated",
		SetActive: true,
	}); err != nil {
		t.Fatalf("update primary host credential: %v", err)
	}

	updatedBundle, exported, err := hostStore.ExportCredentials(bundlePassword, "")
	if err != nil {
		t.Fatalf("export updated credentials: %v", err)
	}
	if exported != 1 {
		t.Fatalf("updated export count = %d, want 1", exported)
	}

	result, err = childStore.ImportManagedCredentials(ownerSwarmID, bundlePassword, "", updatedBundle)
	if err != nil {
		t.Fatalf("import updated managed credentials: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("updated imported count = %d, want 1", result.Imported)
	}
	if result.SnapshotHash == "" {
		t.Fatalf("updated snapshot hash was empty")
	}
	childRecords, err = childStore.ListCredentials("fireworks", 10)
	if err != nil {
		t.Fatalf("list child credentials after updated import: %v", err)
	}
	if len(childRecords) != 1 {
		t.Fatalf("child credential count after updated import = %d, want 1", len(childRecords))
	}
	if childRecords[0].ID != "fw-primary" {
		t.Fatalf("remaining child credential id = %q, want fw-primary", childRecords[0].ID)
	}
	if childRecords[0].APIKey != "fw-primary-key-updated" {
		t.Fatalf("remaining child credential api key = %q, want updated key", childRecords[0].APIKey)
	}
	if childRecords[0].Label != "primary-updated" {
		t.Fatalf("remaining child credential label = %q, want primary-updated", childRecords[0].Label)
	}
	active, ok, err = childStore.GetActiveCredential("fireworks")
	if err != nil {
		t.Fatalf("get child active credential after updated import: %v", err)
	}
	if !ok || active.ID != "fw-primary" {
		t.Fatalf("child active credential after updated import = %#v, want fw-primary", active)
	}
}

func TestImportManagedCredentialsEnablesChildVaultWhenPasswordProvided(t *testing.T) {
	const ownerSwarmID = "swarm_host"
	const bundlePassword = "bundle-password"
	const vaultPassword = "vault-password"

	hostStore := openAuthStore(t, "host-auth.pebble")
	if _, err := hostStore.UpsertCredential(pebblestore.AuthCredentialInput{
		ID:        "fw-primary",
		Provider:  "fireworks",
		Type:      pebblestore.AuthTypeAPI,
		Label:     "primary",
		APIKey:    "fw-primary-key",
		SetActive: true,
	}); err != nil {
		t.Fatalf("upsert primary host credential: %v", err)
	}
	if _, err := hostStore.EnableVault(vaultPassword); err != nil {
		t.Fatalf("enable host vault: %v", err)
	}

	bundle, exported, err := hostStore.ExportCredentials(bundlePassword, vaultPassword)
	if err != nil {
		t.Fatalf("export vaulted credentials: %v", err)
	}
	if exported != 1 {
		t.Fatalf("exported count = %d, want 1", exported)
	}

	childStore := openAuthStore(t, "child-auth.pebble")
	result, err := childStore.ImportManagedCredentials(ownerSwarmID, bundlePassword, vaultPassword, bundle)
	if err != nil {
		t.Fatalf("import managed credentials into vaulted child: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("imported count = %d, want 1", result.Imported)
	}
	childVault, err := childStore.VaultStatus()
	if err != nil {
		t.Fatalf("child vault status: %v", err)
	}
	if !childVault.Enabled {
		t.Fatalf("child vault enabled = false, want true")
	}
	if !childVault.Unlocked {
		t.Fatalf("child vault unlocked = false, want true")
	}
	if childVault.StorageMode != "pebble/vault" {
		t.Fatalf("child storage mode = %q, want pebble/vault", childVault.StorageMode)
	}
	active, ok, err := childStore.GetActiveCredential("fireworks")
	if err != nil {
		t.Fatalf("get child active credential: %v", err)
	}
	if !ok || active.ID != "fw-primary" {
		t.Fatalf("child active credential = %#v, want fw-primary", active)
	}
}

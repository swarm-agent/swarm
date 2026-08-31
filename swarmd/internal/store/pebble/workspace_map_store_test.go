package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openWorkspaceMapTestStore(t *testing.T) (*Store, *WorkspaceMapStore) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace-map.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewWorkspaceMapStore(store)
}

func TestWorkspaceMapCreateDefaultAndAccountIsolation(t *testing.T) {
	_, store := openWorkspaceMapTestStore(t)
	svc := NewWorkspaceMapService(store)

	first, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatalf("create default: %v", err)
	}
	if first.Revision != 1 || first.SchemaVersion != WorkspaceMapSchemaVersion || len(first.Digest) != 64 {
		t.Fatalf("unexpected default metadata: %+v", first)
	}
	if !strings.HasPrefix(first.Content, "# Workspace Map\n") || first.CreatedAt <= 0 || first.UpdatedAt != first.CreatedAt {
		t.Fatalf("unexpected default: %+v", first)
	}

	again, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatalf("get existing default: %v", err)
	}
	if again != first {
		t.Fatalf("default creation was not idempotent: got %+v want %+v", again, first)
	}
	if _, ok, err := store.GetForAccount("account-b"); err != nil || ok {
		t.Fatalf("cross-account read leaked: ok=%v err=%v", ok, err)
	}
	other, err := svc.GetOrCreateDefault("account-b")
	if err != nil {
		t.Fatalf("create other default: %v", err)
	}
	if other.Revision != 1 {
		t.Fatalf("other account revision = %d, want 1", other.Revision)
	}
}

func TestWorkspaceMapUpdateRejectsStaleRevisionWithoutMutation(t *testing.T) {
	_, store := openWorkspaceMapTestStore(t)
	svc := NewWorkspaceMapService(store)
	initial, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatalf("create default: %v", err)
	}
	updated, err := svc.Update("account-a", initial.Revision, "# Workspace Map\r\n\r\n## Workspaces\r\n\r\n- api: backend services")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != 2 || strings.Contains(updated.Content, "\r") || !strings.HasSuffix(updated.Content, "\n") || updated.Digest == initial.Digest {
		t.Fatalf("unexpected update: %+v", updated)
	}

	if _, err := svc.Update("account-a", initial.Revision, "# Workspace Map\n\n- stale"); !errors.Is(err, ErrWorkspaceMapRevisionConflict) {
		t.Fatalf("stale update error = %v, want revision conflict", err)
	}
	persisted, ok, err := store.GetForAccount("account-a")
	if err != nil || !ok {
		t.Fatalf("get persisted ok=%v err=%v", ok, err)
	}
	if persisted != updated {
		t.Fatalf("stale update mutated record: got %+v want %+v", persisted, updated)
	}
}

func TestWorkspaceMapFailedValidationDoesNotMutate(t *testing.T) {
	_, store := openWorkspaceMapTestStore(t)
	svc := NewWorkspaceMapService(store)
	initial, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatalf("create default: %v", err)
	}

	invalid := []string{
		"not a workspace map",
		"# Workspace Map\n\x00",
		"# Workspace Map\n" + strings.Repeat("x", WorkspaceMapMaxBytes),
	}
	for _, content := range invalid {
		if _, err := svc.Update("account-a", initial.Revision, content); err == nil {
			t.Fatalf("invalid update unexpectedly succeeded")
		}
		persisted, ok, getErr := store.GetForAccount("account-a")
		if getErr != nil || !ok || persisted != initial {
			t.Fatalf("failed update mutated record: ok=%v err=%v got=%+v want=%+v", ok, getErr, persisted, initial)
		}
	}
}

func TestWorkspaceMapUpdateDoesNotCrossAccounts(t *testing.T) {
	_, store := openWorkspaceMapTestStore(t)
	svc := NewWorkspaceMapService(store)
	a, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetOrCreateDefault("account-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update("account-a", a.Revision, "# Workspace Map\n\n- private-a"); err != nil {
		t.Fatal(err)
	}
	persistedB, ok, err := store.GetForAccount("account-b")
	if err != nil || !ok || persistedB != b {
		t.Fatalf("account-a update mutated account-b: ok=%v err=%v got=%+v want=%+v", ok, err, persistedB, b)
	}
}

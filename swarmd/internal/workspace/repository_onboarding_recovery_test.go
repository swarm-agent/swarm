package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: InspectRepositoryForPrincipal and SetupRepositoryForPrincipal must
// distinguish absent Git from an empty repository and never mutate the selected
// folder or catalog when Git is unavailable. PATH isolation at the service layer
// is the narrowest deterministic proof of this prerequisite failure.
func TestOnboardingMissingGitLeavesFolderAndCatalogUnchanged(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	state, err := svc.InspectRepositoryForPrincipal(testPrincipal(), path)
	if err != nil || state.State != RepositoryStateGitUnavailable || state.CanSetup {
		t.Fatalf("inspect state=%+v err=%v", state, err)
	}
	state, err = svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if _, typed := RepositoryStateFromError(err); !typed || state.State != RepositoryStateGitUnavailable {
		t.Fatalf("setup state=%+v err=%v", state, err)
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Fatalf("Git prerequisite rejection changed folder: %v", err)
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("catalog changed: %+v err=%v", entries, err)
	}
}

// Requirement: SetupRepositoryForPrincipal must roll back metadata if Git fails
// after metadata reservation, leaving the exact folder retryable. An executable
// fake Git fails every command; no real global Git configuration is consulted.
// This service test proves rollback and absence of catalog/selection mutation.
func TestOnboardingSetupCommandFailureRollsBackForRetry(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path); err == nil {
			t.Fatal("failed Git setup reported success")
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("attempt %d left partial metadata: %+v err=%v", attempt, entries, err)
		}
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("catalog changed: %+v err=%v", entries, err)
	}
	if _, selected, err := svc.CurrentBindingForPrincipal(testPrincipal()); err != nil || selected {
		t.Fatalf("selection changed: selected=%v err=%v", selected, err)
	}
}

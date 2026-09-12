package workspace

import (
	"os"
	"os/user"
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

// Requirement: legacy setup must refuse to omit existing content silently.
// Explicit reviewed omission may create an empty baseline while preserving the
// index and files. Service-level Git inspection proves the tree postconditions.
func TestOnboardingUnbornSetupPreservesIndexAndFiles(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if _, err := runRepositoryGit(path, "init", "--initial-branch=main", "--template="); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "existing.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runRepositoryGit(path, "add", "existing.txt"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(path, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if err == nil || state.State != RepositoryStateNeedsInitialCommit {
		t.Fatalf("unreviewed setup: %+v %v", state, err)
	}
	review, err := svc.ReviewRepositoryForPrincipal(testPrincipal(), path)
	if err != nil {
		t.Fatal(err)
	}
	state, err = svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), RepositoryBaselineRequest{Path: path, ExpectedResolvedPath: path, ReviewDigest: review.Digest, ConfirmBaseline: true, ConfirmOmissions: true})
	if err != nil || state.State != RepositoryStateReady {
		t.Fatalf("reviewed setup: %+v %v", state, err)
	}
	after, err := os.ReadFile(filepath.Join(path, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("index changed")
	}
	files, err := runRepositoryGit(path, "ls-tree", "--name-only", "HEAD")
	if err != nil || files != "" {
		t.Fatalf("committed user files: %q %v", files, err)
	}
	content, err := os.ReadFile(filepath.Join(path, "existing.txt"))
	if err != nil || string(content) != "keep me" {
		t.Fatal("user file changed")
	}
}

// Requirement: repository setup must reject root's home before writing metadata.
// A verified non-root runtime home is permitted (repository_home_test.go).
// Inject root identity so the service guard is independent of the test runner UID.
func TestOnboardingSetupRejectsRootHome(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	home := t.TempDir()
	account := &user.User{Uid: "0", HomeDir: home}
	if _, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, home, account); err == nil {
		t.Fatal("initialized home")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("home changed: %v %v", entries, err)
	}
}

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: every saved workspace can immediately support mandatory managed
// worktrees. The threat is persisting a catalog identity before discovering that
// Git, a repository, or HEAD is missing. This service test is the narrowest
// boundary that proves validation precedes store mutation.
func TestAddForPrincipalRejectsRepositoryWithoutHEADBeforeMutation(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	if _, err := runRepositoryGit(path, "init", "--initial-branch=main"); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}

	_, _, _, err := svc.AddForPrincipalWithEntry(testPrincipal(), path, "workspace", "", true)
	state, ok := RepositoryStateFromError(err)
	if !ok || state.State != RepositoryStateNeedsInitialCommit {
		t.Fatalf("add error=%v state=%+v, want needs_initial_commit", err, state)
	}
	entries, listErr := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if listErr != nil {
		t.Fatalf("list workspaces: %v", listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed prerequisite validation persisted workspaces: %+v", entries)
	}
	if _, selected, currentErr := svc.CurrentBindingForPrincipal(testPrincipal()); currentErr != nil || selected {
		t.Fatalf("failed prerequisite validation selected workspace: selected=%v err=%v", selected, currentErr)
	}
}

// Requirement: a saved workspace that becomes non-repository cannot be selected.
// Threat: stale catalog entries could otherwise reopen an unsupported direct-session path.
// Boundary: SelectForPrincipal must revalidate repository readiness before mutating current selection.
func TestSelectForPrincipalRejectsRepositoryDriftBeforeSelectionMutation(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	readyPath := t.TempDir()
	if _, err := runRepositoryGit(readyPath, "init", "--initial-branch=main"); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if _, err := runRepositoryGit(readyPath, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit"); err != nil {
		t.Fatalf("create initial commit: %v", err)
	}
	if _, err := svc.AddForPrincipal(testPrincipal(), readyPath, "ready", "", false); err != nil {
		t.Fatalf("save ready workspace: %v", err)
	}
	gitPath := filepath.Join(readyPath, ".git")
	removedGitPath := filepath.Join(t.TempDir(), "removed-git")
	if err := os.Rename(gitPath, removedGitPath); err != nil {
		t.Fatalf("remove repository metadata: %v", err)
	}

	_, err := svc.SelectForPrincipal(testPrincipal(), readyPath)
	state, ok := RepositoryStateFromError(err)
	if !ok || (state.State != RepositoryStateNeedsAssistedSetup && state.State != RepositoryStateNotRepository) {
		t.Fatalf("select error=%v state=%+v, want non-repository prerequisite", err, state)
	}
	if _, selected, currentErr := svc.CurrentBindingForPrincipal(testPrincipal()); currentErr != nil || selected {
		t.Fatalf("failed selection changed current workspace: selected=%v err=%v", selected, currentErr)
	}
}

// Requirement: bare repositories cannot back workspace files or managed worktrees.
// Threat: --show-toplevel output alone can be misleading for non-worktree repositories.
// Boundary: repository inspection must reject the repository before catalog mutation.
func TestAddForPrincipalRejectsBareRepositoryBeforeMutation(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	if _, err := runRepositoryGit(path, "init", "--bare"); err != nil {
		t.Fatalf("initialize bare repository: %v", err)
	}
	_, _, _, err := svc.AddForPrincipalWithEntry(testPrincipal(), path, "bare", "", true)
	state, ok := RepositoryStateFromError(err)
	if !ok || state.State != RepositoryStateNotRepository || state.Message != repositoryMessageNonWorkTree {
		t.Fatalf("bare repository add error=%v state=%+v", err, state)
	}
	entries, listErr := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("bare repository persisted workspace: entries=%+v err=%v", entries, listErr)
	}
}

func TestSetupRepositoryForPrincipalInitializesOnlyEmptyUnsavedDirectory(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()

	state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if err != nil {
		t.Fatalf("setup repository: %v", err)
	}
	if state.State != RepositoryStateReady || state.HeadCommit == "" || state.Repository != path {
		t.Fatalf("repository state=%+v, want ready committed repository", state)
	}
	status, err := runRepositoryGit(path, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		t.Fatalf("repository status=%q err=%v, want clean", status, err)
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("setup must not save workspace: entries=%+v err=%v", entries, err)
	}
}

func TestSetupRepositoryForPrincipalLeavesNonEmptyDirectoryUnchanged(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	userFile := filepath.Join(path, "private.txt")
	if err := os.WriteFile(userFile, []byte("do not stage"), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}

	state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if err == nil || state.State != RepositoryStateNeedsAssistedSetup || !state.NeedsReview {
		t.Fatalf("setup state=%+v err=%v, want assisted setup", state, err)
	}
	if _, statErr := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("non-empty setup created .git: %v", statErr)
	}
	contents, readErr := os.ReadFile(userFile)
	if readErr != nil || string(contents) != "do not stage" {
		t.Fatalf("user file changed: %q err=%v", contents, readErr)
	}
}

func TestSetupRepositoryForPrincipalRejectsSavedDirectory(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	if _, err := runRepositoryGit(path, "init", "--initial-branch=main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := runRepositoryGit(path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := svc.AddForPrincipal(testPrincipal(), path, "saved", "", false); err != nil {
		t.Fatalf("seed saved workspace: %v", err)
	}
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path); err == nil || !strings.Contains(err.Error(), "already saved") {
		t.Fatalf("saved setup error=%v, want rejection", err)
	}
}

func TestSetupRepositoryForPrincipalRejectsSymlinkAndStaleSelection(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	realPath := t.TempDir()
	linkPath := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), linkPath, realPath); err == nil || !strings.Contains(err.Error(), "symlinked paths") {
		t.Fatalf("symlink setup error=%v, want rejection", err)
	}
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), realPath, filepath.Join(filepath.Dir(realPath), "stale")); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale setup error=%v, want rejection", err)
	}
	if _, statErr := os.Lstat(filepath.Join(realPath, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected setup created .git: %v", statErr)
	}
}

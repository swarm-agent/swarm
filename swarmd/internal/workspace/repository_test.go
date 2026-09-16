package workspace

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Requirement: the assisted onboarding validator admits only one canonical,
// unsaved, non-empty non-repository path. Threat: stale/symlink/saved/empty/ready
// paths could grant pre-admission mutation scope outside the selected folder.
// The workspace service is the narrowest layer proving no catalog mutation.
func TestInspectAssistedRepositoryForPrincipalRequiresExactPreAdmissionState(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	principal := testPrincipal()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "existing.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := svc.InspectAssistedRepositoryForPrincipal(principal, path, path)
	if err != nil || state.State != RepositoryStateNeedsAssistedSetup || !state.NeedsReview {
		t.Fatalf("assisted state=%+v err=%v", state, err)
	}
	if entries, listErr := svc.ListKnownForPrincipal(principal, 10); listErr != nil || len(entries) != 0 {
		t.Fatalf("inspection mutated catalog: entries=%+v err=%v", entries, listErr)
	}
	if _, err := svc.InspectAssistedRepositoryForPrincipal(principal, path, filepath.Join(filepath.Dir(path), "stale")); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale path error=%v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InspectAssistedRepositoryForPrincipal(principal, link, path); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error=%v", err)
	}
	empty := t.TempDir()
	if _, err := svc.InspectAssistedRepositoryForPrincipal(principal, empty, empty); err == nil || !strings.Contains(err.Error(), "empty folders") {
		t.Fatalf("empty error=%v", err)
	}
	ready := newReadyRepository(t)
	readyState, err := svc.InspectOnboardingRepositoryForPrincipal(principal, ready, ready)
	if err != nil || readyState.State != RepositoryStateReady {
		t.Fatalf("ready onboarding state=%+v err=%v", readyState, err)
	}
	if _, err := svc.InspectAssistedRepositoryForPrincipal(principal, ready, ready); err == nil || !strings.Contains(err.Error(), "existing files") {
		t.Fatalf("ready initial admission error=%v", err)
	}
	if _, err := store.AddForAccount(principal.AccountScopeID, path, "Saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InspectAssistedRepositoryForPrincipal(principal, path, path); err == nil || !strings.Contains(err.Error(), "already saved") {
		t.Fatalf("saved error=%v", err)
	}
}

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

// Requirement: retrying setup after save must acknowledge the existing HEAD,
// never duplicate a commit or catalog entry; invalid consent must still reject.
func TestSetupRepositoryForPrincipalResumesSavedDirectory(t *testing.T) {
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
	before, err := runRepositoryGit(path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if err != nil || state.HeadCommit != before {
		t.Fatalf("saved setup=%+v %v", state, err)
	}
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path+"-stale"); err == nil {
		t.Fatal("stale saved consent accepted")
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 1 {
		t.Fatal("saved retry changed catalog")
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

// Requirement: daemon guidance offers its verified non-root home without inventing
// a project name or mutating it. Threat: unsafe homes, symlinks, or caller
// identity could redirect setup onto existing data. Pure guidance is
// the narrowest layer proving read-only selection with deterministic fixtures.
func TestDaemonWorkspaceGuidanceRejectsUnsafeHomesWithoutMutation(t *testing.T) {
	uid := strconv.Itoa(os.Geteuid())
	home := t.TempDir()
	account := &user.User{Uid: uid, Username: "daemon-fixture", HomeDir: home}
	first := filepath.Join(home, "swarm-workspace")
	if err := os.WriteFile(first, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	guidance := daemonWorkspaceGuidance(account, uid)
	if guidance.HomePath != func() string {
		if uid == "0" {
			return ""
		}
		return home
	}() || guidance.RuntimeUsername != account.Username || !guidance.SetupRequired {
		t.Fatalf("unexpected guidance: %+v", guidance)
	}
	if _, err := os.Lstat(first + "-2"); !os.IsNotExist(err) {
		t.Fatalf("guidance created directory: %v", err)
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"", "relative", string(filepath.Separator), link, first, filepath.Join(home, "absent")} {
		copy := *account
		copy.HomeDir = unsafe
		if got := daemonWorkspaceGuidance(&copy, uid); got.HomePath != "" {
			t.Fatalf("unsafe home %q suggested %+v", unsafe, got)
		}
	}
	if got := daemonWorkspaceGuidance(account, uid+"1"); got.HomePath != "" || got.RuntimeUsername != "" {
		t.Fatalf("mismatched identity accepted: %+v", got)
	}
	for _, mode := range []os.FileMode{0o500, 0o777, 0o000} {
		if err := os.Chmod(home, mode); err != nil {
			t.Fatal(err)
		}
		got := daemonWorkspaceGuidance(account, uid)
		info, err := os.Stat(home)
		if err != nil || info.Mode().Perm() != mode || got.HomePath != "" {
			t.Fatalf("unsafe permissions accepted or changed: guidance=%+v err=%v", got, err)
		}
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(first)
	if err != nil || string(content) != "preserve" {
		t.Fatalf("existing file changed: %q %v", content, err)
	}
}

// Requirement: missing folders are created only through explicit setup, not
// inspection, in an accessible canonical parent (not restricted to daemon home).
// Threat: stale consent could create arbitrary paths. Service assertions prove rejection
// leaves filesystem and workspace catalog unchanged.
func TestSetupRepositoryMissingPathRejectsCallerHomeAndStaleConsent(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "new-project")
	if _, err := svc.InspectRepositoryForPrincipal(testPrincipal(), path); err == nil {
		t.Fatal("inspection accepted absent directory")
	}
	for _, expected := range []string{"", path + "-stale"} {
		if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, expected); err == nil {
			t.Fatalf("setup accepted caller home with expected=%q", expected)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected operation created path: %v", err)
		}
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejection mutated catalog: %v %v", entries, err)
	}
}

// Requirement: explicit setup can create a new daemon-owned project and empty
// commit without enrolling it or staging existing files. The injected account
// fixture exercises the same service implementation without touching real home.
func TestSetupRepositoryCreatesNewDaemonHomeChildOnlyAfterConsent(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	home := t.TempDir()
	account := &user.User{Uid: strconv.Itoa(os.Geteuid()), HomeDir: home}
	path := filepath.Join(home, "new-project")
	if _, err := svc.setupRepositoryForPrincipal(testPrincipal(), path, "", account); err == nil {
		t.Fatal("missing exact consent accepted")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("missing consent created directory: %v", err)
	}
	state, err := svc.setupRepositoryForPrincipal(testPrincipal(), path, path, account)
	if err != nil || state.State != RepositoryStateReady || state.HeadCommit == "" {
		t.Fatalf("new child setup: %+v %v", state, err)
	}
	files, err := runRepositoryGit(path, "ls-tree", "--name-only", "HEAD")
	if err != nil || files != "" {
		t.Fatalf("initial commit is not empty: %q %v", files, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("new directory permissions: %v %v", info, err)
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("setup enrolled workspace: %v %v", entries, err)
	}
}

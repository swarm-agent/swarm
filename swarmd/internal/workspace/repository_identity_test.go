package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a repository Swarm initialises must accept an ordinary agent
// `git commit` on a fresh machine with no Git identity (dogfooding: the first
// agent commit failed with "Please tell me who you are"). The fix writes a
// repository-local, non-personal identity only when none is configured, and
// never touches global config or overrides an identity the owner already has.
// SetupRepositoryForPrincipal is the service that runs `git init`, so it is
// the narrowest layer; GIT_CONFIG_GLOBAL/NOSYSTEM make the ambient config
// hermetic.
func TestSetupRepositorySetsLocalIdentityOnlyWhenNoneIsConfigured(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("EMAIL", "")
	globalDir := t.TempDir()
	global := filepath.Join(globalDir, "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)

	fresh := t.TempDir()
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), fresh, fresh); err != nil {
		t.Fatalf("setup: %v", err)
	}
	name, nameErr := runRepositoryGit(fresh, "config", "--local", "--get", "user.name")
	email, emailErr := runRepositoryGit(fresh, "config", "--local", "--get", "user.email")
	if nameErr != nil || emailErr != nil || name != WorkspaceGitIdentityName || email != WorkspaceGitIdentityEmail {
		t.Fatalf("local identity = %q/%q (%v/%v)", name, email, nameErr, emailErr)
	}
	// useConfigOnly forbids hostname guessing: the commit must succeed from
	// configuration alone, exactly as an agent's plain commit would.
	if _, err := runRepositoryGit(fresh, "-c", "user.useConfigOnly=true", "commit", "--allow-empty", "-m", "agent commit"); err != nil {
		t.Fatalf("agent commit without global identity failed: %v", err)
	}
	if data, err := os.ReadFile(global); err != nil || len(data) != 0 {
		t.Fatalf("global config was modified: %q %v", data, err)
	}

	// An owner identity that already resolves is preserved, not overridden.
	if err := os.WriteFile(global, []byte("[user]\n\tname = Owner\n\temail = owner@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned := t.TempDir()
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), owned, owned); err != nil {
		t.Fatalf("setup with owner identity: %v", err)
	}
	if local, err := runRepositoryGit(owned, "config", "--local", "--get-regexp", "^user\\."); err == nil || local != "" {
		t.Fatalf("owner identity was overridden by local config: %q", local)
	}
}

// Purpose: repository setup accepts an explicit owner-chosen absolute path,
// but must refuse relative paths, system directories and Swarm's own storage
// roots, and must not nest inside an existing repository/workspace. A setup
// call creates directories and Git metadata, so every rejection must leave the
// filesystem untouched.
func TestSetupRepositoryRejectsUnsafeExplicitPaths(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	dataDir := t.TempDir()
	svc.SetProtectedRoots(dataDir)

	for _, path := range []string{"/etc/swarm-workspace-test", "/usr/local/swarm-workspace-test", "/proc/swarm-workspace-test", "/sys", "/dev/swarm-workspace-test", "/boot/x", "/bin/x", "/var/lib/swarmd/workspaces/x"} {
		_, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
		if err == nil || !strings.Contains(err.Error(), "system or Swarm storage directory") {
			t.Fatalf("%s: error = %v, want system directory rejection", path, err)
		}
		if _, statErr := os.Lstat(path); statErr == nil && path != "/sys" {
			t.Fatalf("%s exists after rejected setup", path)
		}
	}
	inData := filepath.Join(dataDir, "workspace")
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), inData, inData); err == nil || !strings.Contains(err.Error(), "system or Swarm storage directory") {
		t.Fatalf("data dir child: error = %v", err)
	}
	if _, err := os.Lstat(inData); !os.IsNotExist(err) {
		t.Fatalf("data dir child was created: %v", err)
	}
	for _, path := range []string{"", "relative/workspace", "./workspace"} {
		if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path); err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("%q: error = %v, want absolute path requirement", path, err)
		}
	}
	if _, err := os.Lstat("relative"); !os.IsNotExist(err) {
		t.Fatalf("relative path created a directory: %v", err)
	}

	// Explicit absolute path outside any workspace succeeds, including a new
	// leaf directory under an existing parent.
	parent := t.TempDir()
	chosen := filepath.Join(parent, "chosen")
	if state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), chosen, chosen); err != nil || state.State != RepositoryStateReady {
		t.Fatalf("explicit path setup: %+v %v", state, err)
	}
	if _, err := store.AddForAccount(testPrincipal().AccountScopeID, chosen, "chosen"); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(chosen, "nested")
	if _, err := svc.SetupRepositoryForPrincipal(testPrincipal(), nested, nested); err == nil {
		t.Fatal("setup nested inside a registered workspace was accepted")
	}
	if _, err := os.Lstat(filepath.Join(nested, ".git")); !os.IsNotExist(err) {
		t.Fatalf("nested setup created Git metadata: %v", err)
	}
}

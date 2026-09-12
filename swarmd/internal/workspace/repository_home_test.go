package workspace

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// Requirement: explicit setup of the verified runtime user's home creates only
// an empty starting commit. Threat: shell/config files or an existing index get
// imported, a retry adds commits, or caller HOME grants authority. The service
// boundary proves Git tree/index and filesystem postconditions in temporary homes.
func TestSetupRuntimeHomeEmptyBaselinePreservesContent(t *testing.T) {
	for _, unborn := range []bool{false, true} {
		name := "non-git"
		if unborn {
			name = "unborn-staged"
		}
		t.Run(name, func(t *testing.T) {
			store, cleanup := newTestWorkspaceStore(t)
			defer cleanup()
			svc := NewService(store)
			home := t.TempDir()
			account := &user.User{Uid: strconv.Itoa(os.Geteuid()), HomeDir: home}
			file := filepath.Join(home, "personal.txt")
			if err := os.WriteFile(file, []byte("keep outside baseline"), 0600); err != nil {
				t.Fatal(err)
			}
			if os.Geteuid() == 0 {
				if _, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, home, account); err == nil {
					t.Fatal("root home admitted")
				}
				if _, err := os.Lstat(filepath.Join(home, ".git")); !os.IsNotExist(err) {
					t.Fatal("root rejection mutated home")
				}
				return
			}
			if unborn {
				if _, err := runRepositoryGit(home, "init", "--initial-branch=main", "--template="); err != nil {
					t.Fatal(err)
				}
				if _, err := runRepositoryGit(home, "add", "--", "personal.txt"); err != nil {
					t.Fatal(err)
				}
			}
			var indexBefore []byte
			if unborn {
				indexBefore, _ = os.ReadFile(filepath.Join(home, ".git", "index"))
			}
			state := inspectRepositoryForAccount(home, account)
			if !state.CanSetup || state.NeedsReview {
				t.Fatalf("home stranded in content review: %+v", state)
			}
			for _, expected := range []string{"", home + "-stale"} {
				if _, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, expected, account); err == nil {
					t.Fatal("stale consent accepted")
				}
			}
			ready, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, home, account)
			if err != nil || ready.State != RepositoryStateReady {
				t.Fatalf("home setup: %+v %v", ready, err)
			}
			tree, err := runRepositoryGit(home, "ls-tree", "-r", "--name-only", "HEAD")
			if err != nil || tree != "" {
				t.Fatalf("home files committed: %q %v", tree, err)
			}
			contents, err := os.ReadFile(file)
			if err != nil || string(contents) != "keep outside baseline" {
				t.Fatal("home contents changed")
			}
			if unborn {
				after, err := os.ReadFile(filepath.Join(home, ".git", "index"))
				if err != nil || string(after) != string(indexBefore) {
					t.Fatal("existing index changed")
				}
			}
			retry, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, home, account)
			if err != nil || retry.HeadCommit != ready.HeadCommit {
				t.Fatal("retry changed HEAD")
			}
			count, err := runRepositoryGit(home, "rev-list", "--count", "HEAD")
			if err != nil || count != "1" {
				t.Fatalf("expected one starting commit: %s %v", count, err)
			}
			entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
			if err != nil || len(entries) != 0 {
				t.Fatal("setup implicitly saved workspace")
			}
		})
	}
}

// Requirement: the home exception must not apply to arbitrary non-empty folders
// or an account whose UID does not match the runtime. Reject before Git mutation.
func TestSetupRuntimeHomeRejectsMismatchedAccount(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	home := t.TempDir()
	file := filepath.Join(home, "personal.txt")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	account := &user.User{Uid: strconv.Itoa(os.Geteuid()) + "1", HomeDir: home}
	if _, err := svc.setupRepositoryForPrincipal(testPrincipal(), home, home, account); err == nil {
		t.Fatal("mismatched account admitted")
	}
	if _, err := os.Lstat(filepath.Join(home, ".git")); !os.IsNotExist(err) {
		t.Fatal("rejection created Git metadata")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "preserve" {
		t.Fatal("rejection changed personal file")
	}
}

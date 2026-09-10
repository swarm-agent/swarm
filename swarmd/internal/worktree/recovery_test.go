package worktree

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Requirement R03/R08-R13/R19/R21/R22: recovery copies only explicit files,
// preserves source bytes/index/refs and separate destination layers, and fails
// closed on stale or unsupported input. Real temporary Git repositories exercise
// SnapshotRecovery/CopyRecovery rather than mocking their authority checks.
func TestRecoveryPrimitives(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(root, "source")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(path string, args ...string) []byte {
		t.Helper()
		out, err := recoveryGit(path, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	put := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-b", "dev")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	put(filepath.Join(repo, "binary"), []byte{0, 1}, 0644)
	put(filepath.Join(repo, "unselected"), []byte("base"), 0644)
	put(filepath.Join(repo, ".gitignore"), []byte("ignored\n"), 0644)
	git(repo, "add", ".")
	git(repo, "commit", "-m", "base")
	put(filepath.Join(repo, "binary"), []byte{0, 2}, 0644)
	git(repo, "add", "binary")
	put(filepath.Join(repo, "binary"), []byte{0, 3}, 0644)
	put(filepath.Join(repo, "unselected"), []byte("not selected"), 0644)
	put(filepath.Join(repo, "new\n file"), []byte{0, 4}, 0755)
	put(filepath.Join(repo, "ignored"), []byte("private"), 0644)
	before, err := inspectRecovery(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := git(repo, "show-ref")
	inventoryBefore := git(repo, "worktree", "list", "--porcelain", "-z")
	for _, selection := range []RecoverySelection{
		{Files: []string{"../escape"}}, {Files: []string{"ignored"}},
		{Files: []string{"binary"}, Commits: []string{before.HEAD}},
		{Files: []string{"binary"}, Patch: []byte("patch")},
	} {
		if _, err := SnapshotRecovery(repo, before, selection); err == nil {
			t.Fatalf("accepted invalid selection %+v", selection)
		}
	}
	if !bytes.Equal(inventoryBefore, git(repo, "worktree", "list", "--porcelain", "-z")) {
		t.Fatal("rejection allocated resources")
	}
	snapshot, err := SnapshotRecovery(repo, before, RecoverySelection{Files: []string{"binary", "new\n file"}})
	if err != nil {
		t.Fatal(err)
	}
	// R22: journal failure precedes allocation, and a destination created by
	// another writer after the journal is never removed by allocator rollback.
	failed, err := (&Service{}).CopyRecoveryJournaled(snapshot, "journal-fail", "agent/journal-fail", func(a Allocation) error { return os.ErrPermission })
	if err == nil {
		t.Fatal("journal failure ignored")
	}
	if _, err := os.Lstat(failed.Allocation.WorkspacePath); !os.IsNotExist(err) {
		t.Fatal("allocated before durable journal")
	}
	raced, err := (&Service{}).CopyRecoveryJournaled(snapshot, "journal-race", "agent/journal-race", func(a Allocation) error {
		if err := os.MkdirAll(a.WorkspacePath, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(a.WorkspacePath, "external"), []byte("keep"), 0600)
	})
	if err == nil {
		t.Fatal("accepted occupied allocation")
	}
	if data, err := os.ReadFile(filepath.Join(raced.Allocation.WorkspacePath, "external")); err != nil || string(data) != "keep" {
		t.Fatal("deleted external writer bytes")
	}
	result, err := (&Service{}).CopyRecovery(snapshot, "recovery-proof", "agent/recovery-proof")
	if err != nil {
		t.Fatalf("copy: %v; %+v", err, result)
	}
	dest := result.Allocation.WorkspacePath
	if !bytes.Equal(git(dest, "show", ":binary"), []byte{0, 2}) {
		t.Fatal("staged layer lost")
	}
	data, err := os.ReadFile(filepath.Join(dest, "binary"))
	if err != nil || !bytes.Equal(data, []byte{0, 3}) {
		t.Fatal("unstaged binary lost")
	}
	data, err = os.ReadFile(filepath.Join(dest, "new\n file"))
	if err != nil || !bytes.Equal(data, []byte{0, 4}) {
		t.Fatal("untracked bytes lost")
	}
	st, err := os.Stat(filepath.Join(dest, "new\n file"))
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatal("untracked mode lost")
	}
	if _, err := os.Stat(filepath.Join(dest, "ignored")); !os.IsNotExist(err) {
		t.Fatal("ignored file imported")
	}
	data, err = os.ReadFile(filepath.Join(dest, "unselected"))
	if err != nil || string(data) != "base" {
		t.Fatal("unselected edit imported")
	}
	after, err := inspectRecovery(repo, repo)
	if err != nil || after != before {
		t.Fatalf("source changed: %v", err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("source index bytes changed")
	}
	if !bytes.Contains(git(repo, "show-ref"), refsBefore) {
		t.Fatal("source ref changed")
	}
	put(filepath.Join(repo, "binary"), []byte("drift"), 0644)
	inventory := git(repo, "worktree", "list", "--porcelain", "-z")
	failed, err = (&Service{}).CopyRecovery(snapshot, "stale", "agent/stale")
	if err == nil || failed.Allocation.WorkspacePath != "" || failed.Diagnostic == "" {
		t.Fatal("stale snapshot not rejected before allocation")
	}
	if !bytes.Equal(inventory, git(repo, "worktree", "list", "--porcelain", "-z")) {
		t.Fatal("stale rejection changed inventory")
	}
	if err := os.Symlink(root, filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRecovery(repo, repo); err == nil {
		t.Fatal("untracked symlink accepted")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryIdentity(repo, alias); err == nil {
		t.Fatal("symlink checkout accepted")
	}
	if _, err := recoveryIdentity(repo, root); err == nil {
		t.Fatal("unregistered directory accepted")
	}
}

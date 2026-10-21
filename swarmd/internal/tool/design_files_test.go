//go:build linux

package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/design"
)

func newDesignFilesFixture(t *testing.T, results string) (*DesignFiles, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewDesignFiles(root, results)
	if err != nil {
		t.Fatal(err)
	}
	return files, root
}

func designDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Requirement: an edit replaces the user's file only while it still has the
// digest the job read, and a refused replacement changes nothing and leaves
// no temporary file. Threat: a design job overwrites edits made while it ran
// or leaves partial files in the person's folder. Owners: DesignFiles.Read,
// DesignFiles.ReplaceIfDigest and designReplace (both digest checks and the
// temp cleanup). A real temp directory is the narrowest layer that proves the
// on-disk postconditions.
func TestDesignFilesReplaceIfDigestRefusesStaleBase(t *testing.T) {
	files, root := newDesignFilesFixture(t, "")
	ctx := context.Background()
	target := filepath.Join(root, "page.html")
	if err := os.WriteFile(target, []byte("<p>v1</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, base, err := files.Read(ctx, "page.html")
	if err != nil || string(data) != "<p>v1</p>" || len(base) != 64 {
		t.Fatalf("read: %q %q %v", data, base, err)
	}

	// Changed before the job writes: refused at the first check.
	if err := os.WriteFile(target, []byte("<p>person</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := files.ReplaceIfDigest(ctx, "page.html", base, []byte("<p>job</p>")); !errors.Is(err, design.ErrStaleBase) {
		t.Fatalf("expected ErrStaleBase, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "<p>person</p>" {
		t.Fatalf("stale replace changed the file: %q", got)
	}

	// Changed after the replacement was written: refused at the second check,
	// and the temporary file is removed.
	_, base, err = files.Read(ctx, "page.html")
	if err != nil {
		t.Fatal(err)
	}
	designReplaceAfterTempHook = func() { _ = os.WriteFile(target, []byte("<p>late</p>"), 0o644) }
	t.Cleanup(func() { designReplaceAfterTempHook = nil })
	if err := files.ReplaceIfDigest(ctx, "page.html", base, []byte("<p>job</p>")); !errors.Is(err, design.ErrStaleBase) {
		t.Fatalf("expected ErrStaleBase on the recheck, got %v", err)
	}
	designReplaceAfterTempHook = nil
	if got, _ := os.ReadFile(target); string(got) != "<p>late</p>" {
		t.Fatalf("late change was overwritten: %q", got)
	}
	if names := designDirEntries(t, root); len(names) != 1 || names[0] != "page.html" {
		t.Fatalf("temporary file left behind: %v", names)
	}

	// Unchanged: replaced atomically, nothing else left.
	_, base, _ = files.Read(ctx, "page.html")
	if err := files.ReplaceIfDigest(ctx, "page.html", base, []byte("<p>job</p>")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "<p>job</p>" {
		t.Fatalf("replace did not apply: %q", got)
	}
	if names := designDirEntries(t, root); len(names) != 1 {
		t.Fatalf("unexpected files after replace: %v", names)
	}
}

// Requirement: design files stay inside the workspace and never follow a
// symlink, in any path component, for reads and writes. Threat: a symlink
// planted in the folder (or a '..' path) makes the daemon read or write
// outside the workspace. Owners: DesignFiles.target (path validation) and the
// per-component O_NOFOLLOW walk in openLocalMutationParent/designOpenRegular.
func TestDesignFilesRefuseEscapesAndSymlinks(t *testing.T) {
	files, root := newDesignFilesFixture(t, "designs/batch/results.json")
	ctx := context.Background()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.html"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.html"), filepath.Join(root, "alias.html")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x.html", "/etc/x.html", "a/../../x.html", "./x.html", "", ".git/x.html", "a//b.html"} {
		if err := files.CreateExclusive(ctx, rel, []byte("x")); err == nil {
			t.Errorf("CreateExclusive accepted %q", rel)
		}
		if _, _, err := files.Read(ctx, rel); err == nil {
			t.Errorf("Read accepted %q", rel)
		}
	}
	if err := files.CreateExclusive(ctx, "linked/new.html", []byte("x")); err == nil {
		t.Fatal("CreateExclusive followed a symlinked folder")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file was written outside the workspace")
	}
	if _, _, err := files.Read(ctx, "linked/secret.html"); err == nil {
		t.Fatal("Read followed a symlinked folder")
	}
	if _, _, err := files.Read(ctx, "alias.html"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Read followed a symlinked file: %v", err)
	}
	if err := files.ReplaceIfDigest(ctx, "alias.html", strings.Repeat("0", 64), []byte("x")); err == nil {
		t.Fatal("ReplaceIfDigest followed a symlinked file")
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "secret.html")); string(got) != "secret" {
		t.Fatal("the symlink target changed")
	}
	if err := files.CheckFolder(ctx, "linked/designs"); err == nil {
		t.Fatal("CheckFolder accepted a symlinked folder")
	}
	if err := files.CheckFolder(ctx, "designs/batch"); err != nil {
		t.Fatalf("CheckFolder refused a new folder: %v", err)
	}
	if _, err := NewDesignFiles(filepath.Join(root, "linked"), ""); err == nil {
		t.Fatal("a symlinked workspace root was accepted")
	}
}

// Requirement: a new design never replaces an existing file, and the results
// file is the only file WriteResults replaces. Threat: a batch overwrites a
// person's file or a results write lands anywhere else. Owners:
// DesignFiles.CreateExclusive (O_EXCL) and DesignFiles.WriteResults.
func TestDesignFilesCreateExclusiveAndResultsScope(t *testing.T) {
	files, root := newDesignFilesFixture(t, "designs/batch/results.json")
	ctx := context.Background()
	if err := files.CreateExclusive(ctx, "designs/batch/01.html", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := files.CreateExclusive(ctx, "designs/batch/01.html", []byte("two")); err == nil {
		t.Fatal("CreateExclusive replaced an existing file")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "designs/batch/01.html")); string(got) != "one" {
		t.Fatalf("existing file changed: %q", got)
	}
	if err := files.WriteResults(ctx, "designs/batch/01.html", []byte("{}")); err == nil {
		t.Fatal("WriteResults wrote a file other than the job's results")
	}
	if err := files.WriteResults(ctx, "designs/batch/results.json", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteResults(ctx, "designs/batch/results.json", []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "designs/batch/results.json")); string(got) != `{"v":2}` {
		t.Fatalf("results not replaced: %q", got)
	}
	if names := designDirEntries(t, filepath.Join(root, "designs/batch")); len(names) != 2 {
		t.Fatalf("temporary files left: %v", names)
	}
	// A results path that is not a regular file is refused before any write.
	if err := os.MkdirAll(filepath.Join(root, "designs/other/results.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	other, err := NewDesignFiles(root, "designs/other/results.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.WriteResults(ctx, "designs/other/results.json", []byte("{}")); err == nil {
		t.Fatal("WriteResults replaced a directory")
	}
	if names := designDirEntries(t, filepath.Join(root, "designs/other")); len(names) != 1 {
		t.Fatalf("refused results write left files: %v", names)
	}
	edit, err := NewDesignFiles(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.WriteResults(ctx, "designs/batch/results.json", []byte("{}")); err == nil {
		t.Fatal("an edit job wrote a results file")
	}
}

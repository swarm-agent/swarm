package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectGitSyncRepoRequiresCleanNamedBranch(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	result, err := inspectGitSyncRepo(context.Background(), repo, true)
	if err == nil {
		t.Fatal("inspectGitSyncRepo error = nil, want dirty repo error")
	}
	if result.Clean {
		t.Fatalf("dirty repo Clean = true")
	}
	if len(result.StatusShort) == 0 {
		t.Fatalf("dirty repo StatusShort is empty")
	}

	result, err = inspectGitSyncRepo(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("inspectGitSyncRepo(requireClean=false) error = %v", err)
	}
	if result.Branch == "" || result.Head == "" || result.Tree == "" || result.RepoRoot == "" {
		t.Fatalf("inspectGitSyncRepo incomplete result: %+v", result)
	}
}

func TestApplyGitSyncRequiresDestructiveConfirmation(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	state, err := inspectGitSyncRepo(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("inspectGitSyncRepo error = %v", err)
	}

	_, err = applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath: repo,
		Branch:     state.Branch,
		CommitSHA:  state.Head,
		TreeSHA:    state.Tree,
	})
	if err == nil {
		t.Fatal("applyGitSync error = nil, want destructive confirmation error")
	}
	if !strings.Contains(err.Error(), "destructive=true") {
		t.Fatalf("applyGitSync error = %q, want destructive=true", err.Error())
	}
}

func TestApplyGitSyncFetchesResetsCleansAndVerifies(t *testing.T) {
	source := initGitCommitTestRepo(t)
	branch := strings.TrimSpace(runGitCommitTestCommand(t, source, "branch", "--show-current"))

	target := filepath.Join(t.TempDir(), "target")
	runGitCommitTestCommand(t, t.TempDir(), "clone", source, target)

	if err := os.WriteFile(filepath.Join(source, "note.txt"), []byte("synced\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	runGitCommitTestCommand(t, source, "add", "note.txt")
	runGitCommitTestCommand(t, source, "commit", "-m", "feat: sync target")
	sourceState, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(target, "note.txt"), []byte("target dirty\n"), 0o644); err != nil {
		t.Fatalf("write target dirty change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "untracked.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write target untracked: %v", err)
	}

	result, err := applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath:  target,
		SourceRepo:  source,
		Branch:      branch,
		CommitSHA:   sourceState.Head,
		TreeSHA:     sourceState.Tree,
		Destructive: true,
	})
	if err != nil {
		t.Fatalf("applyGitSync error = %v result=%+v", err, result)
	}
	if !result.OK {
		t.Fatalf("applyGitSync OK = false result=%+v", result)
	}
	if result.After.Head != sourceState.Head || result.After.Tree != sourceState.Tree || result.After.Branch != branch || !result.After.Clean {
		t.Fatalf("after = %+v, want source head/tree branch clean", result.After)
	}
	if _, err := os.Stat(filepath.Join(target, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked target file still exists or stat error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(target, "note.txt"))
	if err != nil {
		t.Fatalf("read target note: %v", err)
	}
	if string(content) != "synced\n" {
		t.Fatalf("target note = %q, want synced", string(content))
	}
}

func TestApplyGitSyncImportsBundleWhenCommitIsMissing(t *testing.T) {
	source := initGitCommitTestRepo(t)
	branch := strings.TrimSpace(runGitCommitTestCommand(t, source, "branch", "--show-current"))
	target := filepath.Join(t.TempDir(), "target")
	runGitCommitTestCommand(t, t.TempDir(), "clone", source, target)

	if err := os.WriteFile(filepath.Join(source, "bundled.txt"), []byte("from bundle\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	runGitCommitTestCommand(t, source, "add", "bundled.txt")
	runGitCommitTestCommand(t, source, "commit", "-m", "feat: bundled sync")
	sourceState, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source: %v", err)
	}
	if _, err := runGitSyncCommand(context.Background(), target, "cat-file", "-e", sourceState.Head+"^{commit}"); err == nil {
		t.Fatalf("target unexpectedly already has source commit")
	}

	bundlePath, err := createGitBundle(context.Background(), source)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer os.Remove(bundlePath)
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	result, err := applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath:  target,
		GitBundle:   bundle,
		Branch:      branch,
		CommitSHA:   sourceState.Head,
		TreeSHA:     sourceState.Tree,
		Destructive: true,
	})
	if err != nil {
		t.Fatalf("applyGitSync error = %v result=%+v", err, result)
	}
	if result.After.Head != sourceState.Head || result.After.Tree != sourceState.Tree || !result.After.Clean {
		t.Fatalf("after=%+v want source head/tree clean", result.After)
	}
	content, err := os.ReadFile(filepath.Join(target, "bundled.txt"))
	if err != nil {
		t.Fatalf("read bundled file: %v", err)
	}
	if string(content) != "from bundle\n" {
		t.Fatalf("bundled file=%q", string(content))
	}
}

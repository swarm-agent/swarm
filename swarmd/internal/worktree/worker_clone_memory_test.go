package worktree

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: allocateWorkerClone and allocateReviewWorkspace must bound the
// upload-pack pack build without abandoning --no-local isolation. Real Git with
// generated loose blobs is the narrowest layer that proves allocation accepts
// the spaced upload-pack command and propagates its config to pack-objects.
// This is a configuration regression, not a sandbox peak-memory benchmark.
func TestWorkerCloneMemory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := runGitAllocation(path, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git(repo, "init", "-b", "dev")
	git(repo, "config", "gc.auto", "0")
	data := make([]byte, 2*1024*1024)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	for i := 0; i < 3; i++ {
		data[i] ^= 0xff
		if err := os.WriteFile(filepath.Join(repo, "catalog.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		git(repo, "add", "catalog.json")
		git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", fmt.Sprint("catalog ", i))
	}
	if counts := git(repo, "count-objects", "-v"); !strings.Contains(counts, "in-pack: 0") {
		t.Fatalf("fixture not loose: %s", counts)
	}
	head := git(repo, "rev-parse", "HEAD")
	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	t.Setenv("GIT_TRACE2_CONFIG_PARAMS", "pack.threads,pack.windowMemory,pack.deltaCacheSize,core.bigFileThreshold,pack.window")
	s := &Service{}
	worker, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/memory", "worker-execution-memory", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := removeManagedWorktree(repo, worker.WorkspacePath); err != nil {
			t.Error(err)
		}
	})
	review, err := s.allocateReviewWorkspace(repo, "review-memory", head)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.RemoveReviewWorkspace(repo, review.WorkspacePath, "review-memory", head); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{worker.WorkspacePath, review.WorkspacePath} {
		if got := git(path, "rev-parse", "HEAD"); got != head {
			t.Fatalf("clone head = %s, want %s", got, head)
		}
		if _, err := os.Stat(filepath.Join(path, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
			t.Fatalf("clone has alternates: %v", err)
		}
		packs, err := filepath.Glob(filepath.Join(path, ".git", "objects", "pack", "*.idx"))
		if err != nil || len(packs) == 0 {
			t.Fatalf("missing clone pack: %v", err)
		}
		large := 0
		for _, pack := range packs {
			for _, line := range strings.Split(git(path, "verify-pack", "-v", pack), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 5 && fields[1] == "blob" && fields[2] == "2097152" {
					large++
					if len(fields) != 5 {
						t.Errorf("large blob delta-compressed: %s", line)
					}
				}
			}
		}
		if large != 3 {
			t.Errorf("found %d full large blobs, want 3", large)
		}
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	configs := map[string]map[string]string{}
	packSIDs := map[string]bool{}
	clones := 0
	const upload = "--upload-pack=git -c pack.threads=1 -c pack.windowMemory=64m -c pack.deltaCacheSize=16m -c core.bigFileThreshold=1m -c pack.window=10 upload-pack"
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event struct {
			Event, SID, Param string
			Value             json.RawMessage
			Argv              []string
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "start" {
			joined := "\x00" + strings.Join(event.Argv, "\x00") + "\x00"
			if strings.Contains(joined, "\x00pack-objects\x00") {
				packSIDs[event.SID] = true
			}
			if strings.Contains(joined, "\x00clone\x00") {
				clones++
				for _, option := range []string{upload, "--no-local", "--no-checkout", "core.bigFileThreshold=1m"} {
					if !strings.Contains(joined, "\x00"+option+"\x00") {
						t.Errorf("clone missing %q: %v", option, event.Argv)
					}
				}
			}
		}
		if event.Event == "def_param" {
			if configs[event.SID] == nil {
				configs[event.SID] = map[string]string{}
			}
			var value string
			if err := json.Unmarshal(event.Value, &value); err != nil {
				t.Fatal(err)
			}
			configs[event.SID][strings.ToLower(event.Param)] = value
		}
	}
	if clones != 2 {
		t.Fatalf("observed %d clones, want worker and review", clones)
	}
	if len(packSIDs) < 2 {
		t.Fatalf("observed %d pack-objects processes, want at least 2", len(packSIDs))
	}
	for sid := range packSIDs {
		for key, want := range map[string]string{"pack.threads": "1", "pack.windowmemory": "64m", "pack.deltacachesize": "16m", "core.bigfilethreshold": "1m", "pack.window": "10"} {
			if got := configs[sid][key]; got != want {
				t.Errorf("pack-objects %s = %q, want %q", key, got, want)
			}
		}
	}
}

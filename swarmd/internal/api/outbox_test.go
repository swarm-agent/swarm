package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/outbox"
)

// Purpose: a finished worker job's export (outbox.go) is what other
// machines, panels and AIs see of the job. Requirement: from the job's
// swarm_get_job view, exactly as jobView returns it in-process (Go-typed
// lists, not yet JSON), the export names the job, worker and status, marks
// a job that committed images as media, ships each committed media file
// from the job's head commit and a Git bundle of its commits, and asks to be
// retried while the job's commits are not readable yet (exporting what it
// has on the last attempt). Threats: a result exported as a bare report
// because typed lists were not read, media read from the working folder
// instead of the commit, and a result exported before its commits exist.
// Owner: outboxResultFromView over a real Git repository.

func outboxGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOutboxResultFromJobView(t *testing.T) {
	repo := t.TempDir()
	outboxGit(t, repo, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644)
	outboxGit(t, repo, "add", ".")
	outboxGit(t, repo, "commit", "-q", "-m", "base")
	base := outboxGit(t, repo, "rev-parse", "HEAD")
	outboxGit(t, repo, "checkout", "-q", "-b", "agent/worker-r1")
	png := []byte("\x89PNG\r\n\x1a\nnot-really-a-png")
	_ = os.MkdirAll(filepath.Join(repo, "media"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "media", "card.png"), png, 0o644)
	_ = os.WriteFile(filepath.Join(repo, "gen.go"), []byte("package main\n"), 0o644)
	outboxGit(t, repo, "add", ".")
	outboxGit(t, repo, "commit", "-q", "-m", "add card")
	head := outboxGit(t, repo, "rev-parse", "HEAD")
	// The working folder changes after the commit: the export must not see it.
	_ = os.WriteFile(filepath.Join(repo, "media", "card.png"), []byte("edited later"), 0o644)

	view := func(state string, commits []string) map[string]any {
		return map[string]any{
			"status": "needs_review", "project_id": "proj_1", "session_id": "sess_1",
			"result": map[string]any{
				"workspace_path": repo, "worktree_branch": "agent/worker-r1", "base_branch": "main", "base_commit": base, "head": head,
				"delivery_state": state, "commits": commits, "report": "made a card",
				"files": []map[string]any{{"path": "gen.go", "status": "added"}, {"path": "media/card.png", "status": "added"}},
				"cost":  map[string]any{"total_tokens": 10},
			},
		}
	}
	ctx := context.Background()
	b := outbox.NewBuilder("pc", 1<<20)
	if _, err := outboxResultFromView(ctx, b, "w1", "Coder", "r1", view("candidate_work", []string{})); err == nil {
		t.Fatal("a job with work but no readable commits must be retried")
	}
	if _, err := outboxResultFromView(ctx, b, "w1", "Coder", "r1", view("", nil)); err == nil {
		t.Fatal("a job not assessed yet must be retried")
	}

	export, err := outboxResultFromView(ctx, b, "w1", "Coder", "r1", view("candidate_work", []string{head[:7] + " add card"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, blob := range export.Blobs {
			_ = os.Remove(blob.Path)
		}
	})
	m := export.Manifest
	if export.JobID != "worker/w1/r1" || m["outcome"] != "media" || m["status"] != "needs_review" || m["worker"].(map[string]any)["name"] != "Coder" || m["report"] != "made a card" {
		t.Fatalf("manifest: %v", m)
	}
	files := m["files"].([]map[string]any)
	sum := sha256.Sum256(png)
	if len(files) != 1 || files[0]["path"] != "media/card.png" || files[0]["sha256"] != hex.EncodeToString(sum[:]) || files[0]["type"] != "image/png" || files[0]["key"] != "machines/pc/blobs/sha256/"+hex.EncodeToString(sum[:]) {
		t.Fatalf("media from the head commit: %v", files)
	}
	git := m["git"].(map[string]any)
	bundle, _ := git["bundle"].(map[string]any)
	if bundle == nil || len(export.Blobs) != 2 || git["head"] != head {
		t.Fatalf("git: %v blobs %d", git, len(export.Blobs))
	}
	var bundlePath string
	for _, blob := range export.Blobs {
		if blob.SHA256 == bundle["sha256"] {
			bundlePath = blob.Path
		}
	}
	if heads := outboxGit(t, repo, "bundle", "list-heads", bundlePath); !strings.Contains(heads, head+" refs/heads/agent/worker-r1") {
		t.Fatalf("bundle heads: %q", heads)
	}

	// On the last attempt an unsettled job is exported as it is.
	last := outbox.NewBuilder("pc", 1<<20)
	last.LastAttempt = true
	if export, err := outboxResultFromView(ctx, last, "w1", "Coder", "r1", view("candidate_work", nil)); err != nil || export.Manifest["outcome"] != "report" {
		t.Fatalf("last attempt: %v %v", export, err)
	}
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/outbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: the outbox result manifest of a worker job and of a media job
// keeps its exact shape (keys, value types and values), byte for byte, while
// the manifest builder is shared with job automations.
// Threat: refactoring the shared result builder silently renames, drops or
// retypes a field an outbox reader (an operator's ingest) depends on.
// Owners: outboxResultFromView, outboxMediaExport and outbox.Builder.
// Layer: the two functions with a real Git repository and real outbox
// builder are the narrowest layer that produces the exported manifest.

func outboxPinGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Pin", "GIT_AUTHOR_EMAIL=pin@example.invalid", "GIT_COMMITTER_NAME=Pin",
		"GIT_COMMITTER_EMAIL=pin@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func outboxPinFileSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestOutboxWorkerManifestShapeIsPinned(t *testing.T) {
	repo := t.TempDir()
	outboxPinGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, repo, "add", ".")
	outboxPinGit(t, repo, "commit", "-q", "-m", "base")
	base := outboxPinGit(t, repo, "rev-parse", "HEAD")
	outboxPinGit(t, repo, "checkout", "-q", "-b", "agent/job-1")
	if err := os.WriteFile(filepath.Join(repo, "logo.png"), []byte("\x89PNG pin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, repo, "add", ".")
	outboxPinGit(t, repo, "commit", "-q", "-m", "add logo")
	head := outboxPinGit(t, repo, "rev-parse", "HEAD")

	view := map[string]any{
		"status": "needs_review", "project_id": "proj-1", "session_id": "sess-1", "error": "",
		"result": map[string]any{
			"workspace_path": repo, "worktree_branch": "agent/job-1", "base_branch": "main", "base_commit": base, "head": head,
			"commits": []string{head + " add logo"}, "report": "Added the logo.", "what_did_do": "logo", "what_not_done": "none",
			"cost":           map[string]any{"tokens": 1200, "estimated_usd": 0.5},
			"delivery_state": "candidate_work",
			"files":          []map[string]any{{"path": "logo.png", "status": "added"}, {"path": "main.go", "status": "added"}},
		},
	}
	b := outbox.NewBuilder("m1", 1<<20)
	export, err := outboxResultFromView(context.Background(), b, "wkr-1", "Coder", "run-1", view)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, blob := range export.Blobs {
			_ = os.Remove(blob.Path)
		}
	})
	if len(export.Blobs) != 2 {
		t.Fatalf("blobs = %d, want the media file and the bundle", len(export.Blobs))
	}
	logo, bundle := export.Blobs[0], export.Blobs[1]
	if outboxPinFileSHA(t, logo.Path) != logo.SHA256 || outboxPinFileSHA(t, bundle.Path) != bundle.SHA256 {
		t.Fatal("blob hashes do not match their files")
	}
	got, err := json.Marshal(export.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cost":{"estimated_usd":0.5,"tokens":1200},"delivery_state":"candidate_work","error":"",` +
		`"files":[{"key":"machines/m1/blobs/sha256/LOGO","path":"logo.png","role":"primary","sha256":"LOGO","size":8,"type":"image/png"}],` +
		`"git":{"base_branch":"main","base_commit":"BASE","branch":"agent/job-1","bundle":{"key":"machines/m1/blobs/sha256/BUNDLE","sha256":"BUNDLE","size":BSIZE},` +
		`"commits":[{"sha":"HEAD","subject":"add logo"}],"files":["logo.png","main.go"],"head":"HEAD"},` +
		`"job_id":"worker/wkr-1/run-1","outcome":"media","project_id":"proj-1","report":"Added the logo.","run_id":"run-1","session_id":"sess-1","status":"needs_review",` +
		`"what_did_do":"logo","what_not_done":"none","worker":{"id":"wkr-1","name":"Coder"},"workspace_path":"REPO"}`
	repoJSON, _ := json.Marshal(repo)
	want = strings.NewReplacer("LOGO", logo.SHA256, "BUNDLE", bundle.SHA256, "BSIZE", outboxPinInt(bundle.Size), "BASE", base, "HEAD", head,
		`"REPO"`, string(repoJSON)).Replace(want)
	if string(got) != want {
		t.Fatalf("worker manifest changed:\n got %s\nwant %s", got, want)
	}
	if export.JobID != "worker/wkr-1/run-1" || export.RunID != "run-1" {
		t.Fatalf("export ids = %q %q", export.JobID, export.RunID)
	}
}

func outboxPinInt(n int64) string {
	out, _ := json.Marshal(n)
	return string(out)
}

func TestOutboxMediaManifestShapeIsPinned(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nmedia-pin")
	task := &pebblestore.ProjectTaskRecord{
		ID: "task-9", ProjectID: "proj-1", Title: "Launch Art", Description: "a logo", Status: "needs_review", Agent: "image",
		Model: "img-model", AspectRatio: "1:1", WorkspacePath: "/w/site",
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "d1", Kind: "artifact", Status: "ready", MediaURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), Model: "img-model", Resolution: "1K"},
			{ID: "d2", Kind: "artifact", Status: "failed", Description: "provider refused"},
		},
	}
	b := outbox.NewBuilder("m1", 1<<20)
	export := outboxMediaExport(task, b)
	t.Cleanup(func() {
		for _, blob := range export.Blobs {
			_ = os.Remove(blob.Path)
		}
	})
	sum := sha256.Sum256(png)
	sha := hex.EncodeToString(sum[:])
	got, err := json.Marshal(export.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"aspect_ratio":"1:1","brief":"a logo","files":[{"key":"machines/m1/blobs/sha256/SHA","model":"img-model","path":"launch-art-1.png","resolution":"1K","role":"primary","sha256":"SHA","size":17,"type":"image/png"}],` +
		`"job_id":"media/proj-1/task-9","kind":"image","model":"img-model","outcome":"media","project_id":"proj-1","run_id":"task-9",` +
		`"skipped":[{"path":"launch-art-2.bin","reason":"provider refused"}],"status":"needs_review","title":"Launch Art","workspace_path":"/w/site"}`
	want = strings.ReplaceAll(want, "SHA", sha)
	if string(got) != want {
		t.Fatalf("media manifest changed:\n got %s\nwant %s", got, want)
	}
}

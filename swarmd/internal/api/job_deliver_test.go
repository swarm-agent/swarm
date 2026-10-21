package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/outbox"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Purpose: every workflow result lists what it can hand over (its branch,
// each file its commits left at the head with the file's sha256 read from
// the folder's repository, its media and its report), and a deliver step
// hands exactly those bytes over: into a directory of the folder (an equal
// file is kept, a different one is never overwritten, a path that leaves
// the folder is refused), and to the machine's outbox as blobs plus a
// manifest at a stable key. A deliverable whose content changed since it was
// recorded is refused, and so is the outbox on a machine without one. A
// produce step's file is found committed at its job's head or in the folder.
// Threat: a deliver step that hands over other bytes than the work recorded
// (what was reviewed is not what is delivered), overwrites a person's file,
// writes outside the folder, or silently drops a deliverable.
// Owners: addResultDeliverables, jobAutomationDeliverer (Deliver: folder and
// outbox; Locate) and outbox.Exporter.WriteDelivery over real Git, real
// files and a real folder outbox.
func TestDeliverablesAreListedAndHandedOverExactly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder := t.TempDir()
	git(folder, "init", "-q", "-b", "dev")
	git(folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(folder, "rev-parse", "HEAD")
	lane := filepath.Join(t.TempDir(), "lane")
	git(folder, "worktree", "add", "-q", "-b", "agent/task-1", lane, "HEAD")
	report := []byte("# Audit\n\nThe login flow checks the password twice.\n")
	logo := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.MkdirAll(filepath.Join(lane, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, "reports", "audit.md"), report, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, "logo.png"), logo, 0o644); err != nil {
		t.Fatal(err)
	}
	git(lane, "add", ".")
	git(lane, "commit", "-q", "-m", "audit and logo")
	head := git(lane, "rev-parse", "HEAD")
	ctx := context.Background()

	result := &jobautomation.Result{JobID: "task/proj-1/task-1", Kind: jobautomation.KindTask, Folder: folder, Status: jobautomation.StatusSucceeded, Report: "Wrote the audit.",
		Git: &jobautomation.ResultGit{Branch: "agent/task-1", BaseCommit: base, Head: head, Files: []string{"logo.png", "reports/audit.md", "gone.txt"}, Worktree: lane}}
	addResultDeliverables(ctx, result)
	byName := map[string]jobautomation.Deliverable{}
	for _, d := range result.Deliverables {
		byName[d.Name] = d
	}
	if len(result.Deliverables) != 4 || byName["branch"].Head != head || byName["branch"].Kind != jobautomation.DeliverableBranch {
		t.Fatalf("deliverables = %+v", result.Deliverables)
	}
	audit := byName["reports/audit.md"]
	if audit.Kind != jobautomation.DeliverableFile || audit.Source != jobautomation.SourceGit || audit.SHA256 != sha256Hex(report) || audit.Size != int64(len(report)) || audit.Type != "text/markdown" {
		t.Fatalf("audit deliverable = %+v", audit)
	}
	if png := byName["logo.png"]; png.Kind != jobautomation.DeliverableMedia || png.SHA256 != sha256Hex(logo) || png.Type != "image/png" {
		t.Fatalf("logo deliverable = %+v", png)
	}
	if byName["report"].Kind != jobautomation.DeliverableReport || byName["gone.txt"].Name != "" {
		t.Fatalf("report or removed file = %+v", result.Deliverables)
	}

	server := &Server{}
	d := jobAutomationDeliverer{s: server}
	req := jobautomation.DeliverRequest{DeliveryID: "jdel_1", StepID: "save", Folder: folder, To: jobautomation.DeliverFolder, Path: "deliverables", Result: *result,
		Deliverables: []jobautomation.Deliverable{audit, byName["report"]}}
	out, err := d.Deliver(ctx, req)
	if err != nil {
		t.Fatalf("deliver to folder: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(folder, "deliverables", "audit.md"))
	if err != nil || string(got) != string(report) || len(out.Delivered) != 2 || out.Delivered[0].SHA256 != sha256Hex(report) {
		t.Fatalf("delivered %q (%v), outcome %+v", got, err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(folder, "deliverables", "report.md")); string(got) != "Wrote the audit." {
		t.Fatalf("report.md = %q", got)
	}
	// Again: the same content is kept, not a failure.
	if _, err := d.Deliver(ctx, req); err != nil {
		t.Fatalf("delivering the same file again = %v", err)
	}
	// A different file at the destination is never overwritten.
	if err := os.WriteFile(filepath.Join(folder, "deliverables", "audit.md"), []byte("the person's own edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var refused *jobautomation.RefusedError
	if _, err := d.Deliver(ctx, req); !errors.As(err, &refused) || !strings.Contains(err.Error(), "never overwrites") {
		t.Fatalf("overwrite = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(folder, "deliverables", "audit.md")); string(got) != "the person's own edit\n" {
		t.Fatalf("the person's file was replaced: %q", got)
	}
	// A destination that leaves the folder through a symlink is refused.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(folder, "escape")); err != nil {
		t.Fatal(err)
	}
	escape := req
	escape.Path = "escape"
	if _, err := d.Deliver(ctx, escape); err == nil {
		t.Fatal("delivered through a symlink out of the folder")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the folder: %v", entries)
	}
	// What was recorded is what is delivered.
	changed := audit
	changed.SHA256 = strings.Repeat("0", 64)
	stale := req
	stale.Path, stale.Deliverables = "fresh", []jobautomation.Deliverable{changed}
	if _, err := d.Deliver(ctx, stale); !errors.As(err, &refused) || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("changed content = %v", err)
	}

	// The outbox: refused without one, blobs plus a manifest with one.
	toOutbox := req
	toOutbox.To, toOutbox.Path, toOutbox.StepID = jobautomation.DeliverOutbox, "", "bucket"
	toOutbox.Deliverables = []jobautomation.Deliverable{audit, byName["logo.png"]}
	if _, err := d.Deliver(ctx, toOutbox); !errors.As(err, &refused) || !strings.Contains(err.Error(), "no outbox") {
		t.Fatalf("outbox without one = %v", err)
	}
	cfg := outbox.Config{Scheme: "file", Dir: t.TempDir(), Machine: "pc"}
	dest, err := cfg.Open()
	if err != nil {
		t.Fatal(err)
	}
	server.SetOutboxDeliveryWriter(outbox.NewExporter(cfg, dest, nil, nil, nil, nil).WriteDelivery)
	out, err = d.Deliver(ctx, toOutbox)
	if err != nil || out.Where != "machines/pc/deliveries/jdel_1/bucket.json" {
		t.Fatalf("outbox = %+v %v", out, err)
	}
	var manifest map[string]any
	data, err := os.ReadFile(filepath.Join(cfg.Dir, out.Where))
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest["job_id"] != "task/proj-1/task-1" || len(manifest["deliverables"].([]any)) != 2 {
		t.Fatalf("manifest = %s (%v)", data, err)
	}
	for _, content := range [][]byte{report, logo} {
		if blob, err := os.ReadFile(filepath.Join(cfg.Dir, "machines/pc/blobs/sha256", sha256Hex(content))); err != nil || string(blob) != string(content) {
			t.Fatalf("blob %s = %q %v", sha256Hex(content), blob, err)
		}
	}

	// Locate: a produce step's file at its job's head, or in the folder.
	spec := jobautomation.ResultSpec{Kind: jobautomation.DeliverableFile, Path: "reports/audit.md"}
	found, err := d.Locate(ctx, folder, spec, jobautomation.AgentOutcome{Head: head})
	if err != nil || found.Source != jobautomation.SourceGit || found.SHA256 != sha256Hex(report) || found.Head != head {
		t.Fatalf("located at head = %+v %v", found, err)
	}
	inFolder := []byte("written in the folder\n")
	if err := os.WriteFile(filepath.Join(folder, "notes.md"), inFolder, 0o644); err != nil {
		t.Fatal(err)
	}
	found, err = d.Locate(ctx, folder, jobautomation.ResultSpec{Kind: jobautomation.DeliverableFile, Path: "notes.md"}, jobautomation.AgentOutcome{})
	if err != nil || found.Source != jobautomation.SourceFolder || found.SHA256 != sha256Hex(inFolder) {
		t.Fatalf("located in the folder = %+v %v", found, err)
	}
	if _, err := d.Locate(ctx, folder, jobautomation.ResultSpec{Kind: jobautomation.DeliverableFile, Path: "missing.md"}, jobautomation.AgentOutcome{Head: head}); err == nil {
		t.Fatal("located a file nobody wrote")
	}
}

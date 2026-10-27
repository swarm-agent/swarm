package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: TakeLease and recoverLeases must retain the borrowed review's mount
// authority, independent of cwd and registration recovery order. Inspecting the
// actual engine argv is the narrow hermetic layer; this does not prove kernel
// read-only enforcement or execute a live container.
func TestBorrowedReviewLeaseMounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m, runner, project := newLeaseTestManager(t, 4, nil)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", project, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-b", "dev")
	git("commit", "--allow-empty", "-m", "base")
	trees := t.TempDir()
	lane := filepath.Join(trees, "bucket", "lane")
	git("worktree", "add", "-b", "review", lane)
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{project}, nil }, WorktreesRoot: trees, Bucket: func(string) (string, error) { return "bucket", nil }})
	const owner = "lease-review-fixture"
	if err := m.RegisterBorrowedReviewCheckout(lane, owner); err != nil {
		t.Fatal(err)
	}
	if err := m.BindReviewSession(owner, lane); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { borrowedReviews.Delete(lane); reviewSessions.Delete(owner) })
	lease, err := m.TakeLease(ctx, lane, owner)
	if err != nil {
		t.Fatal(err)
	}
	assertMounts := func(args []string) {
		t.Helper()
		text := strings.Join(args, " ")
		guards, err := reviewGitMounts(lane, project)
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{lane}
		for _, g := range guards {
			paths = append(paths, g.HostPath)
		}
		for _, p := range paths {
			if !strings.Contains(text, "type=bind,src="+p+",dst="+p+",readonly") {
				t.Fatalf("missing readonly mount %s: %s", p, text)
			}
		}
	}
	assertMounts(runner.runArgs(lease.Container))
	// A pre-upgrade lease without review mounts must never execute for a
	// registered reviewer, even if it was recovered under the same owner.
	stored := m.leases.byID[lease.ID]
	stored.scope.ReviewCheckout = ""
	before := runner.count()
	if _, err := m.ExecLease(ctx, lease.ID, owner, ExecRequest{}); err == nil {
		t.Fatal("legacy writable lease accepted")
	}
	if runner.count() != before {
		t.Fatal("legacy lease executed before refusal")
	}
	stored.scope.ReviewCheckout = lane
	if !strings.Contains(strings.Join(runner.runArgs(lease.Container), " "), labelLeaseReview+"="+lane) {
		t.Fatal("missing durable review label")
	}
	ordinary, err := m.TakeLease(ctx, lane, "lane-owner")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.scope.ReviewCheckout != "" {
		t.Fatal("owner inherited reviewer restrictions")
	}
	ordinaryArgs := strings.Join(runner.runArgs(ordinary.Container), " ")
	if !strings.Contains(ordinaryArgs, "type=bind,src="+project+",dst="+project) || strings.Contains(ordinaryArgs, "type=bind,src="+project+",dst="+project+",readonly") {
		t.Fatalf("ordinary lease lost its writable project mount: %s", ordinaryArgs)
	}
	if _, err := m.TakeLease(ctx, project, owner); err == nil {
		t.Fatal("reviewer escaped via project cwd")
	}
	// Simulate daemon restart before review registrations have been restored.
	borrowedReviews.Delete(lane)
	reviewSessions.Delete(owner)
	m.leases.byID = map[string]*Lease{}
	runner.out["ps -a"] = lease.Container + "\t" + lease.ID + "\t" + owner + "\t" + project + "\tdaemon\t" + lane + "\n"
	if err := m.recoverLeases(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := m.OwnerLeases(owner)
	if len(recovered) != 1 || recovered[0].scope.ReviewCheckout != lane {
		t.Fatalf("lost review scope: %+v", recovered)
	}
	assertMounts(m.scopeRunArgs(lease.Container, project, "spec", []string{project}, SecretSetup{}, EnvironmentImage{Image: m.cfg.Image}, recovered[0].scope.ReviewCheckout))
}

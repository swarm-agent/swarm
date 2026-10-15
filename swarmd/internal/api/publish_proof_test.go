package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: verifyPublishProof and Queue.Process must reject a vacuous proof
// or broken fix before delivery, persist both revision-bound results, and default
// to allowing undeclared proofs. Real Git checkouts and shell reproductions are
// the narrowest layer proving the negative control is actually executed.
func TestPublishProofRealGit(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
		required, manifest  bool
	}{
		{"fixed", "test \"$(cat value)\" = fixed", "", false, false},
		{"vacuous", "true", "the reproduction does not reproduce the problem", false, false},
		{"broken", "false", "the fix does not fix it", false, false},
		{"required", "", "landing requires a proof", true, false},
		{"optional", "", "", false, false},
		{"new_script", "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			put := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-b", "dev")
			put("value", "broken")
			git("add", ".")
			git("commit", "-m", "base")
			base := git("rev-parse", "HEAD")
			git("checkout", "-b", "agent/job")
			put("value", "fixed")
			if tc.manifest {
				put("repro.sh", "test \"$(cat value)\" = fixed\n")
				put(".swarm-proof.json", `{"command":"bash repro.sh","files":["repro.sh"]}`)
			}
			git("add", ".")
			git("commit", "-m", "fix")
			head := git("rev-parse", "HEAD")
			db, err := store.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			q := publish.NewQueue(db)
			e := publish.Entry{Account: "account", Request: publish.Request{Folder: dir, JobID: "worker/test/run", Head: head, BaseCommit: base, BaseBranch: "dev", Branch: "agent/job"}}
			if _, err := q.Enqueue(e); err != nil {
				t.Fatal(err)
			}
			landed := false
			err = q.Process(ctx, "account", dir, publish.QueueHooks{
				Prepare: func(ctx context.Context, e *publish.Entry) error {
					return verifyPublishProof(ctx, publish.Rule{Folder: dir, RequireProof: tc.required}, e, tc.command)
				},
				Land: func(ctx context.Context, e publish.Entry) (publish.LandResult, error) {
					// Real compare-and-swap integration; refusal must leave dev at base.
					git("update-ref", "refs/heads/dev", e.Request.Head, e.Request.BaseCommit)
					landed = true
					return publish.LandResult{Commit: e.Request.Head}, nil
				},
			})
			if err != nil && (tc.want == "" || err.Error() != "worker follow-up unavailable") {
				t.Fatal(err)
			}
			got, _, err := q.Get("account", e.Request.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" {
				if landed || !strings.Contains(got.Reason, tc.want) || git("rev-parse", "dev") != base {
					t.Fatalf("unauthorized landing or wrong rejection: %+v", got)
				}
			} else if !landed || got.State != "landed" || git("rev-parse", "dev") != head {
				t.Fatalf("not landed: %+v", got)
			}
			if tc.command != "" || tc.manifest {
				if got.Proof == nil || got.Proof.Base == nil || got.Proof.Head == nil || got.Proof.Base.Commit != base || got.Proof.Head.Commit != head {
					t.Fatalf("missing durable proof: %+v", got.Proof)
				}
				if tc.want == "" && (got.Proof.Base.ExitCode == 0 || got.Proof.Head.ExitCode != 0) {
					t.Fatalf("wrong proof: %+v", got.Proof)
				}
			}
			// Requirement: a landed declaration belongs to its introducing job,
			// not later jobs. Exercise the real queue and Git CAS again, checking
			// both default delivery and fail-closed require-proof policy.
			if tc.manifest {
				put("later", "unrelated change")
				git("add", ".")
				git("commit", "-m", "later job without proof")
				laterHead := git("rev-parse", "HEAD")
				for _, required := range []bool{true, false} {
					laterDB, err := store.Open(filepath.Join(t.TempDir(), "db"))
					if err != nil {
						t.Fatal(err)
					}
					defer laterDB.Close()
					laterQueue := publish.NewQueue(laterDB)
					later := publish.Entry{Account: "account", Request: publish.Request{Folder: dir, JobID: "worker/test/later", Head: laterHead, BaseCommit: head, BaseBranch: "dev", Branch: "agent/job"}}
					if _, err := laterQueue.Enqueue(later); err != nil {
						t.Fatal(err)
					}
					err = laterQueue.Process(ctx, "account", dir, publish.QueueHooks{
						Prepare: func(ctx context.Context, e *publish.Entry) error {
							return verifyPublishProof(ctx, publish.Rule{Folder: dir, RequireProof: required}, e, "")
						},
						Land: func(ctx context.Context, e publish.Entry) (publish.LandResult, error) {
							git("update-ref", "refs/heads/dev", e.Request.Head, e.Request.BaseCommit)
							return publish.LandResult{Commit: e.Request.Head}, nil
						},
					})
					if err != nil && (!required || err.Error() != "worker follow-up unavailable") {
						t.Fatal(err)
					}
					got, _, err := laterQueue.Get("account", later.Request.JobID)
					if err != nil {
						t.Fatal(err)
					}
					if got.Proof != nil {
						t.Fatalf("inherited proof: %+v", got.Proof)
					}
					if required {
						if !strings.Contains(got.Reason, "landing requires a proof") || git("rev-parse", "dev") != head {
							t.Fatalf("wrong rejection: %+v", got)
						}
					} else if got.State != "landed" || git("rev-parse", "dev") != laterHead {
						t.Fatalf("later job did not land: %+v", got)
					}
				}
			}
			if git("status", "--porcelain") != "" {
				t.Fatal("proof changed source")
			}
			if strings.Count(git("worktree", "list", "--porcelain"), "worktree ") != 1 {
				t.Fatal("proof leaked checkouts")
			}
		})
	}
}

// Requirement: runProof bounds output and treats cancellation as infrastructure,
// never as the expected failing negative control. Real shell execution exercises
// process cancellation without substituting a fake executor.
func TestPublishProofExecutionBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := runProof(ctx, t.TempDir(), "revision", "printf '%05000d' 0; exit 3")
	if got.ExitCode != 3 || got.Error != "" || len(got.Output) != 4096 {
		t.Fatalf("unexpected bounded result: %+v", got)
	}
	missing := runProof(ctx, t.TempDir(), "revision", "swarm-nonexistent-proof-command")
	if missing.Error == "" {
		t.Fatal("missing command counted as reproduction failure")
	}
	stopped, stop := context.WithCancel(context.Background())
	stop()
	got = runProof(stopped, t.TempDir(), "revision", "true")
	if got.Error == "" || got.ExitCode != -1 {
		t.Fatalf("cancellation treated as proof: %+v", got)
	}
}

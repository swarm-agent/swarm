package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: PrepareTaskDeltaRecovery and the existing integration service must
// deliver only recorded task net edits after rewritten shared history. Real Git
// trees/ancestry are the narrowest proof against mass replay, lost target edits,
// false original-ancestry receipts, dirty/stale admission and conflict mutation.
func TestTaskDeltaRecovery(t *testing.T) {
	for _, scenario := range []string{"missing", "equivalent", "conflict", "merge", "dirty-source", "dirty-target", "stale-source", "stale-target", "wrong-repository", "ambiguous-base"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			in, git := deliveryFixture(t)
			git(in.TargetPath, "config", "user.name", "Fixture")
			git(in.TargetPath, "config", "user.email", "fixture@example.invalid")
			write := func(path, name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Rewrite the twenty historical ancestors, retaining their tree but
			// not their IDs. Only the child's unique feature is recoverable.
			tree := git(in.TargetPath, "rev-parse", "HEAD^{tree}")
			rewritten := git(in.TargetPath, "commit-tree", tree, "-m", "rewritten shared history")
			git(in.TargetPath, "update-ref", "refs/heads/dev", rewritten)
			write(in.TargetPath, "target-only", "retain me")
			git(in.TargetPath, "add", ".")
			git(in.TargetPath, "commit", "-m", "unrelated target work")
			if scenario == "equivalent" || scenario == "conflict" {
				value := "feature"
				if scenario == "conflict" {
					value = "overlapping target feature"
				}
				write(in.TargetPath, "feature", value)
				git(in.TargetPath, "add", ".")
				git(in.TargetPath, "commit", "-m", "target feature")
			}
			if scenario == "merge" {
				side := filepath.Join(t.TempDir(), "side")
				git(in.TargetPath, "worktree", "add", "-b", "task-side", side, in.Identity.BaseOID)
				write(side, "merged-feature", "legitimate task merge")
				git(side, "add", ".")
				git(side, "commit", "-m", "task side")
				git(in.SourcePath, "merge", "--no-edit", "task-side")
			}
			in.Identity.SourceOID = git(in.SourcePath, "rev-parse", "HEAD")
			in.Identity.TargetOID = git(in.TargetPath, "rev-parse", "HEAD")
			original, target := in.Identity.SourceOID, in.Identity.TargetOID
			switch scenario {
			case "dirty-source":
				write(in.SourcePath, "dirty", "preserve")
			case "dirty-target":
				write(in.TargetPath, "dirty", "preserve")
			case "stale-source":
				in.Identity.SourceOID = in.Identity.BaseOID
			case "stale-target":
				in.Identity.TargetOID = rewritten
			case "wrong-repository":
				other, _ := deliveryFixture(t)
				in.TargetPath = other.TargetPath
			case "ambiguous-base":
				in.Identity.BaseOID = rewritten
			}
			svc := &Service{}
			prepared, err := svc.PrepareTaskDeltaRecovery(context.Background(), in)
			if strings.HasPrefix(scenario, "dirty-") || strings.HasPrefix(scenario, "stale-") || scenario == "wrong-repository" || scenario == "ambiguous-base" {
				if err == nil {
					t.Fatalf("unsafe admission: %+v", prepared)
				}
				if git(in.SourcePath, "rev-parse", "HEAD") != original {
					t.Fatal("original source changed")
				}
				if scenario != "wrong-repository" && git(in.TargetPath, "rev-parse", "HEAD") != target {
					t.Fatal("rejected recovery changed target")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if git(in.TargetPath, "rev-parse", "HEAD") != target || git(in.TargetPath, "status", "--porcelain") != "" {
				t.Fatal("preparation changed captured target")
			}
			if scenario == "conflict" {
				if _, err := svc.PrepareTaskIntegration(in.TargetPath, "dev", target, []TaskIntegrationChild{{SessionID: "task", BaseCommit: target, HeadCommit: prepared.PreparedHead, PreserveAncestry: true}}); err == nil {
					t.Fatal("unresolved recovery input promoted")
				}
				if !strings.Contains(prepared.Conflict, "feature") || prepared.PreparedHead == "" {
					t.Fatalf("missing actionable conflict: %+v", prepared)
				}
				if git(in.TargetPath, "rev-parse", prepared.PreparedHead+"^") != target {
					t.Fatal("repair replays old history")
				}
				if git(in.TargetPath, "rev-parse", prepared.RetainedRef) != prepared.PreparedHead {
					t.Fatal("repair source not retained")
				}
				return
			}
			if scenario == "equivalent" {
				if !prepared.Equivalent || prepared.PreparedHead != "" {
					t.Fatal("equivalence fabricated merge")
				}
				return
			}
			retry, err := svc.PrepareTaskDeltaRecovery(context.Background(), in)
			if err != nil || retry.PreparedHead != prepared.PreparedHead {
				t.Fatalf("retry changed prepared identity: %v", err)
			}
			if git(in.TargetPath, "rev-list", "--count", target+".."+prepared.PreparedHead) != "1" {
				t.Fatal("old history replayed")
			}
			plan, err := svc.PrepareTaskIntegration(in.TargetPath, "dev", target, []TaskIntegrationChild{{SessionID: "task", BaseCommit: target, HeadCommit: prepared.PreparedHead, PreserveAncestry: true}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.ApplyTaskIntegration(in.TargetPath, plan)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := svc.TaskCommitDescendsFrom(in.TargetPath, prepared.PreparedHead, result.ResultingParentHead); err != nil || !ok {
				t.Fatal("recovered ancestry missing")
			}
			if ok, _ := svc.TaskCommitDescendsFrom(in.TargetPath, original, result.ResultingParentHead); ok {
				t.Fatal("original history replayed")
			}
			if git(in.TargetPath, "show", "HEAD:target-only") != "retain me" || git(in.TargetPath, "show", "HEAD:feature") != "feature" {
				t.Fatal("wrong recovered tree")
			}
			if scenario == "merge" && git(in.TargetPath, "show", "HEAD:merged-feature") != "legitimate task merge" {
				t.Fatal("merge delta lost")
			}
			if git(in.SourcePath, "rev-parse", "HEAD") != original {
				t.Fatal("source changed")
			}
			in.Identity.TargetOID = result.ResultingParentHead
			in.Identity.Freshness = "observed"
			receipt := &pebblestore.ProjectTaskIntegration{State: "recovered", SourceHead: original, SourceBranch: in.Identity.SourceBranch, RecoveryBase: in.Identity.BaseOID, TargetBranch: "dev", TargetWorkspacePath: in.TargetPath, RecoveredHead: prepared.PreparedHead, ResultingTargetHead: result.ResultingParentHead}
			if got := ObserveTaskDeltaReceipt(context.Background(), in, receipt); got.State != "recovered" || len(got.AllowedActions) != 0 {
				t.Fatalf("receipt projection: %+v", got)
			}
			again, err := svc.PrepareTaskDeltaRecovery(context.Background(), in)
			if err != nil || !again.Equivalent {
				t.Fatalf("repeat duplicates delivered edits: %+v %v", again, err)
			}
			// Prepared integration may not follow an advanced target implicitly.
			if _, err := svc.ApplyTaskIntegration(in.TargetPath, plan); err == nil {
				t.Fatal("stale plan applied")
			}
		})
	}
}

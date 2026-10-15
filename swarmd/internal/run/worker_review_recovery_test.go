package run

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: SweepReviewAdmissions must recover pre-session failures from durable
// receipts without deleting live reviewer lanes or other roles. Real Pebble and
// Git are the narrowest layer proving receipt ownership and disk postconditions.
func TestSweepReviewAdmissions(t *testing.T) {
	_, ss, s, workspace := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "reviewer", Role: store.WorkerRoleReviewer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.activeWorkspace("account", "owner", workspace)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", entry.Path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))
	paths := map[string]string{}
	for _, status := range []string{"failed", "admitted", "running"} {
		r, err := ws.RecordWorkerRun("account", store.WorkerRunRecord{WorkerID: w.ID, Role: store.WorkerRoleReviewer, UserID: "owner", WorkspaceID: workspace, SessionID: "review-" + status, Status: status, RequestSource: "direct"})
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.host.trees.AllocateReviewWorkspace(identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner", AccountScopeSource: identity.AccountScopeSourceServerState}, entry.Path, r.SessionID, head)
		if err != nil {
			t.Fatal(err)
		}
		paths[status] = a.WorkspacePath
		if status == "failed" {
			// Persisted session, but no admission intent or captured-head metadata:
			// ordinary cleanupReview cannot validate this interrupted admission.
			snapshot := store.SessionSnapshot{ID: r.SessionID, AccountScopeID: "account", UserID: "owner", WorkspacePath: entry.Path, WorktreeRootPath: a.WorkspacePath, Metadata: map[string]any{"worker_execution_run_id": r.ID}}
			_, err := ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, AccountScopeID: "account", UserID: "owner", Kind: sessions.SessionMutationCreateSession, ClientRequestID: "partial", IdempotencyKey: "partial", PayloadHash: "partial", RequestHash: "partial", Session: &snapshot})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.cleanupReview(r); err == nil {
				t.Fatal("fixture must fail normal cleanup")
			}
		}
	}
	// An unowned detached lane is not a review merely because its name is ws_*.
	unowned, err := s.host.trees.AllocateReviewWorkspace(identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner", AccountScopeSource: identity.AccountScopeSourceServerState}, entry.Path, "unowned-lane", head)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.SweepReviewAdmissions(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(paths["failed"]); !os.IsNotExist(err) {
			t.Fatal("orphan retained", err)
		}
		for _, path := range []string{paths["admitted"], paths["running"], unowned.WorkspacePath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("live or unowned checkout removed", err)
			}
		}
	}
	out, err = exec.Command("git", "-C", entry.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), paths["failed"]) || !strings.Contains(string(out), paths["admitted"]) {
		t.Fatalf("wrong registrations: %s", out)
	}
}

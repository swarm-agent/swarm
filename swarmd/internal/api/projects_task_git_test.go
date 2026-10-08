package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: inspectTaskGitState/reconcileTaskGitState require source ancestry on
// the captured checkout; identical patches are not integration evidence.
// Threat: patch-equivalent histories fabricate Done and block repair, or a
// zero-commit lane is marked integrated. Real temporary Git is the narrowest
// layer proving commit identity, classification and response reconciliation.
func TestInspectTaskGitStateCapturedTargetAndPatchEquivalentPromotion(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-config"), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run(root, "init", "-b", "release")
	if err := os.WriteFile(filepath.Join(root, "base"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "base")
	run(root, "commit", "-m", "base")
	base := trimGit(run(root, "rev-parse", "HEAD"))
	child := filepath.Join(t.TempDir(), "child")
	run(root, "worktree", "add", "-b", "agent/task", child)
	server, _, raw := newWorkspaceOverviewTopologyTestServer(t)
	db := pebblestore.NewSessionStore(raw)
	if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	account := testPrincipal().AccountScopeID
	entry, err := pebblestore.NewWorkspaceStore(raw).AddForAccount(account, root, "Delivery fixture")
	if err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "project", Name: "Delivery", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: root}}}
	if err := db.PutProject(account, project); err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{WorkspacePath: child, ID: "task-git-session", UserID: testPrincipal().UserID, AccountScopeID: account, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "release", Metadata: map[string]any{"project_id": "project", "task_id": "task", "swarm_v3_worktree_owner_session_id": "task-git-session", "swarm_v3_runtime_workspace_path": child, "swarm_v3_source_workspace_id": entry.WorkspaceID, "swarm_v3_source_workspace_generation": entry.WorkspaceGeneration, "swarm_v3_source_workspace_path": root, "base_commit": base}}
	if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{SessionID: session.ID, UserID: testPrincipal().UserID, AccountScopeID: account, ClientRequestID: "create:task-git", IdempotencyKey: "create:task-git", PayloadHash: "create:task-git", RequestHash: "create:task-git", Kind: sessionruntime.SessionMutationCreateSession, Session: &session, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: child, SourcePath: root, OwnerSessionID: session.ID, Branch: session.WorktreeBranch, AllocatedRuntimeRoot: true}, NowUnixMs: 1}); err != nil {
		t.Fatal(err)
	}
	task := pebblestore.ProjectTaskRecord{ID: "task", Title: "Delivery fixture", Agent: "coder", ProjectID: "project", WorkspacePath: child, SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: root, Provenance: "explicit"}, SessionID: session.ID, AccountID: account, Status: "needs_review", WorktreeBranch: "agent/task", BaseBranch: "release", BaseCommit: base}
	if err := db.PutProjectTask(account, &task); err != nil {
		t.Fatal(err)
	}
	// Registered HTTP reads and rejection must preserve both durable rows and
	// project outbox, not just report a conservative status label.
	assertHTTPReadOnly := func(want string, reject bool) {
		t.Helper()
		before, _, err := db.GetProjectTask(account, task.ProjectID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		refs := run(root, "show-ref")
		published := 0
		raw.SetProjectPublisher(func(pebblestore.V3RealtimeOutboxRecord) { published++ })
		call := func(method, suffix, body string, foreign bool) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, ProjectsPath+"/project/tasks/task"+suffix, strings.NewReader(body))
			p := testPrincipal()
			if foreign {
				p.AccountScopeID = "foreign"
			}
			r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, p))
			w := httptest.NewRecorder()
			server.handleProjects(w, r)
			return w
		}
		response := call(http.MethodGet, "", "", false)
		var result struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Task.DeliveryAssessment == nil || result.Task.DeliveryAssessment.State != want {
			t.Fatalf("detail: %d %s", response.Code, response.Body)
		}
		if result.Task.DeliveryAssessment.TaskRevision != before.Revision || result.Task.DeliveryAssessment.WorkspaceID != entry.WorkspaceID {
			t.Fatal("assessment identity lost")
		}
		if reject {
			response = call(http.MethodPost, "/integrate", `{"session_id":"task-git-session","source_branch":"agent/task","target_branch":"release"}`, false)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "not actionable") {
				t.Fatalf("admission: %d %s", response.Code, response.Body)
			}
		}
		if response := call(http.MethodGet, "", "", true); response.Code != http.StatusNotFound {
			t.Fatalf("foreign read: %d", response.Code)
		}
		after, _, err := db.GetProjectTask(account, task.ProjectID, task.ID)
		if err != nil || !reflect.DeepEqual(before, after) || published != 0 || refs != run(root, "show-ref") {
			t.Fatal("read/rejection mutated task, revision, outbox or Git refs")
		}
	}
	assertHTTPReadOnly("empty", true)
	zero := inspectTaskGitState(task, db)
	if zero.isIntegrated || zero.unintegratedCommits != 0 || zero.baseBranch != "release" {
		t.Fatalf("zero-commit source falsely integrated: %+v", zero)
	}
	if err := os.WriteFile(filepath.Join(child, "change"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	run(child, "add", "change")
	run(child, "commit", "-m", "change")
	assertHTTPReadOnly("candidate_work", false)
	pending := inspectTaskGitState(task, db)
	if pending.isIntegrated || pending.unintegratedCommits != 1 || pending.gitStatus != "diverged" {
		t.Fatalf("missing commit not actionable: %+v", pending)
	}
	// Force divergent history; an immediate cherry-pick can preserve the same OID.
	run(root, "commit", "--allow-empty", "-m", "target advance")
	run(root, "cherry-pick", trimGit(run(child, "rev-parse", "HEAD")))
	assertHTTPReadOnly("history_equivalent", true)
	equivalent := inspectTaskGitState(task, db)
	if equivalent.isIntegrated || equivalent.unintegratedCommits != 0 || equivalent.deliveryAssessment.State != "history_equivalent" || equivalent.gitStatus != "diverged" {
		t.Fatalf("patch equivalence fabricated integration: %+v", equivalent)
	}
	task.Status, task.IsIntegrated = "completed", true // Delivery facts must not rewrite the execution outcome.
	if err := reconcileTaskGitState(db, &task); err != nil || task.IsIntegrated || task.Status != "completed" {
		t.Fatalf("patch equivalence fabricated Done: %+v %v", task, err)
	}
	run(root, "merge", "--no-edit", "agent/task")
	integrated := inspectTaskGitState(task, db)
	if !integrated.isIntegrated || integrated.unintegratedCommits != 0 || integrated.baseBranch != "release" {
		t.Fatalf("landed source not reconciled: %+v", integrated)
	}
	for _, mutate := range []func(*pebblestore.ProjectTaskRecord){
		func(v *pebblestore.ProjectTaskRecord) { v.AccountID = "another-account" },
		func(v *pebblestore.ProjectTaskRecord) { v.SourceWorkspace.WorkspaceGeneration++ },
		func(v *pebblestore.ProjectTaskRecord) { v.ActiveAttemptID = "unknown-attempt" },
		func(v *pebblestore.ProjectTaskRecord) { v.ID = "other-task" },
		func(v *pebblestore.ProjectTaskRecord) { v.Archived = true },
		func(v *pebblestore.ProjectTaskRecord) { v.SessionID = "deleted-session" },
		func(v *pebblestore.ProjectTaskRecord) { v.WorkspacePath = root },
		func(v *pebblestore.ProjectTaskRecord) { v.BaseCommit = strings.Repeat("a", 40) },
	} {
		invalid := task
		mutate(&invalid)
		if state := inspectTaskGitState(invalid, db); state.isIntegrated || state.gitStatus != "unknown" {
			t.Fatalf("unowned lane trusted: %+v", state)
		}
	}
	// Ancestry delivery must not rewrite failed execution or erase its receipt.
	failed, err := db.UpdateProjectTask(account, task.ProjectID, task.ID, func(row *pebblestore.ProjectTaskRecord) error {
		row.Status, row.LastError, row.ActionNeeded = "failed", "retained conflict", "repair conflict"
		row.Integration = &pebblestore.ProjectTaskIntegration{State: "conflict", Error: "retained conflict"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertHTTPReadOnly("integrated", false)
	if err := reconcileTaskGitState(db, failed); err != nil {
		t.Fatal(err)
	}
	syncTaskSessionState(failed, db)
	if failed.Status != "failed" || failed.LastError != "retained conflict" || failed.Integration.Error != "retained conflict" || !failed.IsIntegrated {
		t.Fatalf("delivery rewrote failed execution: %+v", failed)
	}
	task.LastError, task.ActionNeeded = "retained conflict", "repair conflict"
	if err := reconcileTaskGitState(db, &task); err != nil || task.LastError != "retained conflict" || task.ActionNeeded != "repair conflict" {
		t.Fatal("read erased actionable error")
	}
	task.BaseBranch = "other"
	mismatch := inspectTaskGitState(task, db)
	if mismatch.gitStatus != "unknown" || mismatch.isIntegrated {
		t.Fatalf("mismatched target trusted: %+v", mismatch)
	}
}

func trimGit(s string) string { return strings.TrimSpace(s) }

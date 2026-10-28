package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type deltaIntegrationFixture struct {
	f          *confirmationFixture
	project    string
	task       *pebblestore.ProjectTaskRecord
	session    pebblestore.SessionSnapshot
	source     string
	child      string
	baseHead   string
	targetHead string
}

func deltaTally(changes map[int]string) []byte {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		line := fmt.Sprintf("line %d", i)
		if changed, ok := changes[i]; ok {
			line = changed
		}
		b.WriteString(line + "\n")
	}
	return []byte(b.String())
}

func writeDeltaTally(t *testing.T, dir string, changes map[int]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "tally.sh"), deltaTally(changes), 0600); err != nil {
		t.Fatal(err)
	}
}

// newDeltaIntegrationFixture builds the reported situation against real Git and
// a real store: a task branch whose agent merged the advanced target (another
// task's commit on a different hunk of tally.sh) into its branch instead of
// rebasing. With conflict set, the target then advances again over the same
// line the task changed, so the task's net change truly conflicts.
func newDeltaIntegrationFixture(t *testing.T, conflict bool) *deltaIntegrationFixture {
	t.Helper()
	return newDeltaFixture(t, func(source, child string) {
		writeDeltaTally(t, child, map[int]string{18: "line 18 task"})
		integrationGit(t, child, "commit", "-am", "task change")
		writeDeltaTally(t, source, map[int]string{2: "line 2 other task"})
		integrationGit(t, source, "commit", "-am", "other task change")
		// The agent merged the target in instead of rebasing.
		integrationGit(t, child, "merge", "--no-edit", "dev")
		if conflict {
			writeDeltaTally(t, source, map[int]string{2: "line 2 other task", 18: "line 18 target"})
			integrationGit(t, source, "commit", "-am", "conflicting target change")
		}
	})
}

// newDeltaFixture creates the target repository (dev, one base commit of
// tally.sh), the task lane at the allocator location on agent/task, and the
// task's store records; history builds the task and target commits.
func newDeltaFixture(t *testing.T, history func(source, child string)) *deltaIntegrationFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Recovery admits only lanes at the deterministic allocator location.
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, source, "init", "-b", "dev")
	// Recovery's commit-tree uses the repository's configured identity, as in
	// a person's repository; global config is disabled for hermeticity.
	integrationGit(t, source, "config", "user.name", "Fixture")
	integrationGit(t, source, "config", "user.email", "fixture@example.invalid")
	writeDeltaTally(t, source, nil)
	integrationGit(t, source, "add", "tally.sh")
	integrationGit(t, source, "commit", "-m", "base")
	base := integrationGit(t, source, "rev-parse", "HEAD")
	const sessionID = "delta-child"
	laneParent, err := appstorage.WorktreeDataDir(source)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(laneParent, worktreeruntime.WorkspaceIdentityForSession(sessionID))
	integrationGit(t, source, "worktree", "add", "-b", "agent/task", child)
	history(source, child)

	f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
	t.Cleanup(f.close)
	p := testPrincipal()
	db := f.sessions.Store()
	catalogDB, err := pebblestore.Open(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalogDB.Close() })
	f.server.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(catalogDB))
	if _, _, err := pebblestore.NewWorkspaceStore(catalogDB).CreateForAccountIfAbsent(p.AccountScopeID, source, "source", ""); err != nil {
		t.Fatal(err)
	}
	proj := &pebblestore.ProjectRecord{ID: "delta-project", AccountID: p.AccountScopeID, Name: "Delta", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: source}}}
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	bound, err := f.server.resolveProjectTaskSource(p, proj, source, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: child, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "dev", Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "base_commit": base, "project_id": proj.ID, "task_id": "delta-task", "swarm_v3_source_workspace_id": bound.WorkspaceID, "swarm_v3_source_workspace_generation": bound.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": sessionID, "swarm_v3_runtime_workspace_path": child}}
	if err := db.CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", AllocatedRuntimeRoot: true, Path: child, SourcePath: source, OwnerSessionID: sessionID, Branch: session.WorktreeBranch}, Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create-delta-child", PayloadHash: "create-delta-child", Session: &session}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "delta-task", ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: "Delta", Agent: "coder", Status: "needs_review", SessionID: sessionID, WorkspacePath: child, WorktreeBranch: "agent/task", BaseBranch: "dev", BaseCommit: base, SourceWorkspace: bound}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	f.server.worktrees = &worktreeruntime.Service{}
	return &deltaIntegrationFixture{f: f, project: proj.ID, task: task, session: session, source: source, child: child, baseHead: base, targetHead: integrationGit(t, source, "rev-parse", "HEAD")}
}

// assertDeltaIntegrated checks the target postconditions of a recovered
// merged-up branch: exactly one new commit on dev directly over the previous
// head, both changes in tally.sh, a clean target checkout and a durable
// recovered receipt naming that commit.
func (x *deltaIntegrationFixture) assertDeltaIntegrated(t *testing.T, resultingHead string) {
	t.Helper()
	head := integrationGit(t, x.source, "rev-parse", "HEAD")
	if head != resultingHead || head == x.targetHead {
		t.Fatalf("target head %s, reported %s, previous %s", head, resultingHead, x.targetHead)
	}
	if count := integrationGit(t, x.source, "rev-list", "--count", x.targetHead+"..HEAD"); count != "1" {
		t.Fatalf("expected exactly one new target commit, got %s", count)
	}
	if parents := integrationGit(t, x.source, "rev-list", "--parents", "-n", "1", "HEAD"); len(strings.Fields(parents)) != 2 {
		t.Fatalf("integrated commit is not a single-parent commit: %s", parents)
	}
	contents, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
	if err != nil || string(contents) != string(deltaTally(map[int]string{2: "line 2 other task", 18: "line 18 task"})) {
		t.Fatalf("target tally.sh lost a change: %q %v", contents, err)
	}
	if status := integrationGit(t, x.source, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("target checkout not clean: %q", status)
	}
	updated, _, _ := x.f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, x.project, x.task.ID)
	if updated.Integration == nil || updated.Integration.State != "recovered" || updated.Integration.ResultingTargetHead != head || updated.Integration.RecoveredHead != head || updated.Status != "completed" {
		t.Fatalf("receipt: %#v status %s", updated.Integration, updated.Status)
	}
}

// Purpose: a task branch whose agent merged the advanced target in (instead of
// rebasing) must still integrate through the one integrate service, delivering
// its net change as one commit, with a result an AI can relay. The regression:
// IntegrateProjectTask refused history_rewritten (task_merge_delta) and pointed
// at /recover-integrate, which MCP AI keys and the Orchestrator cannot call.
// Owners: IntegrateProjectTask → integrateProjectTaskDelta →
// recoverProjectTaskDelta (shared with /recover-integrate) over
// worktree.PrepareTaskDeltaRecovery, and controlMCPIntegrate for MCP output.
// Real Git and a real store are the narrowest layer that proves the target's
// commit count, content and cleanliness and the durable receipt; the MCP case
// crosses the real HTTP route with an AI-key (write level) scoped request.
func TestIntegrateProjectTaskDeliversMergedUpBranchNetChange(t *testing.T) {
	t.Run("service", func(t *testing.T) {
		x := newDeltaIntegrationFixture(t, false)
		result, err := x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result["status"] != "recovered" {
			t.Fatalf("status: %#v", result)
		}
		integration, _ := result["integration"].(map[string]any)
		warning, _ := integration["warning"].(string)
		head, _ := integration["resulting_target_head"].(string)
		if integration["target_branch"] != "dev" || integration["previous_target_head"] != x.targetHead || integration["commit"] != head || !strings.Contains(warning, "one new commit "+head) || !strings.Contains(warning, "not kept") {
			t.Fatalf("integration: %#v", integration)
		}
		x.assertDeltaIntegrated(t, head)
		// A retry (for example after a lost response) returns the same result
		// without another target change.
		again, err := x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		if err != nil || again["status"] != "recovered" || again["integration"].(map[string]any)["resulting_target_head"] != head {
			t.Fatalf("retry: %#v %v", again, err)
		}
		x.assertDeltaIntegrated(t, head)
	})
	t.Run("mcp", func(t *testing.T) {
		x := newDeltaIntegrationFixture(t, false)
		if controlMCPRouteAllowed(http.MethodPost, "/v3/projects/"+x.project+"/tasks/"+x.task.ID+"/recover-integrate") {
			t.Fatal("premise changed: recover-integrate is reachable from Swarm Control")
		}
		scopes, err := aiKeyScopes("write")
		if err != nil {
			t.Fatal(err)
		}
		request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopes})
		call := &controlMCPCall{request: request, server: x.f.server, next: x.f.server.internalRouteHandler()}
		out, err := controlMCPIntegrate(call, map[string]any{"project_id": x.project, "task_id": x.task.ID}, "swarm_integrate_task", "swarm_list_tasks")
		if err != nil {
			t.Fatal(err)
		}
		result := out.(map[string]any)
		integration, _ := result["integration"].(map[string]any)
		head, _ := integration["resulting_target_head"].(string)
		warning, _ := integration["warning"].(string)
		if result["status"] != "recovered" || integration["commit"] != head || !strings.Contains(warning, "net change was integrated into dev as one new commit") {
			t.Fatalf("mcp result: %#v", result)
		}
		x.assertDeltaIntegrated(t, head)
	})
	t.Run("recover-integrate-route", func(t *testing.T) {
		x := newDeltaIntegrationFixture(t, false)
		current, _, _ := x.f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, x.project, x.task.ID)
		selection := tool.ProjectTaskIntegrationSelection{SessionID: x.session.ID, SourceBranch: "agent/task", TargetBranch: "dev", Revision: current.Revision, AttemptID: current.ActiveAttemptID, SourceHead: integrationGit(t, x.child, "rev-parse", "HEAD"), TargetHead: x.targetHead}
		stale := selection
		stale.TargetHead = x.baseHead
		body, _ := json.Marshal(stale)
		if code, out := x.f.do(http.MethodPost, "/v3/projects/"+x.project+"/tasks/"+x.task.ID+"/recover-integrate", string(body)); code != http.StatusConflict || !strings.Contains(out, "target HEAD changed") {
			t.Fatalf("stale pin accepted: %d %s", code, out)
		}
		if integrationGit(t, x.source, "rev-parse", "HEAD") != x.targetHead {
			t.Fatal("stale pin moved target")
		}
		body, _ = json.Marshal(selection)
		code, out := x.f.do(http.MethodPost, "/v3/projects/"+x.project+"/tasks/"+x.task.ID+"/recover-integrate", string(body))
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, out)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if result["status"] != "recovered" {
			t.Fatalf("result: %s", out)
		}
		x.assertDeltaIntegrated(t, integrationGit(t, x.source, "rev-parse", "HEAD"))
	})
}

// Purpose: when a merged-up branch's net change truly conflicts with the
// target, integration must refuse with the conflicting file named and an
// action an AI can take (follow up with the agent to rebase, not merge), and
// must leave the target branch head and working tree untouched while keeping
// the retained repair commit and conflict receipt. Owners: IntegrateProjectTask
// → recoverProjectTaskDelta → worktree.PrepareTaskDeltaRecovery (ConflictFiles).
// Real Git is required to produce a genuine merge-tree conflict.
func TestIntegrateProjectTaskRefusesConflictingMergedUpBranch(t *testing.T) {
	x := newDeltaIntegrationFixture(t, true)
	before, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
	if err == nil {
		t.Fatal("conflicting net change was integrated")
	}
	message := err.Error()
	for _, want := range []string{"tally.sh", "swarm_send_message", "rebase its branch agent/task onto dev", "not merge", "Nothing was changed on dev"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal lacks %q: %s", want, message)
		}
	}
	if strings.Contains(message, "/v3/") || strings.Contains(message, "recover-integrate") {
		t.Fatalf("refusal names an HTTP route: %s", message)
	}
	var failure *projectTaskIntegrationError
	if !errors.As(err, &failure) || failure.status != http.StatusConflict {
		t.Fatalf("not a 409 integration failure: %#v", err)
	}
	if delivery, ok := failure.delivery.(*pebblestore.TaskDeliveryAssessment); !ok || delivery.State != "conflict" || len(delivery.Files) != 1 || delivery.Files[0] != "tally.sh" {
		t.Fatalf("delivery: %#v", failure.delivery)
	}
	if head := integrationGit(t, x.source, "rev-parse", "HEAD"); head != x.targetHead {
		t.Fatalf("target head moved: %s", head)
	}
	after, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("target working tree changed: %q %v", after, err)
	}
	if status := integrationGit(t, x.source, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("target checkout not clean: %q", status)
	}
	updated, _, _ := x.f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, x.project, x.task.ID)
	if updated.Integration == nil || updated.Integration.State != "conflict" || updated.Integration.RecoveryRef == "" || updated.Integration.RecoveredHead == "" || updated.Integration.Error != message {
		t.Fatalf("conflict receipt: %#v", updated.Integration)
	}
	if retained := integrationGit(t, x.source, "rev-parse", updated.Integration.RecoveryRef); retained != updated.Integration.RecoveredHead {
		t.Fatalf("retained repair commit %s, receipt %s", retained, updated.Integration.RecoveredHead)
	}
}

// resolvedSameLineHistory is the reported live case: another task changed
// line 10 of tally.sh to NAME=alpha on dev; this task changed the same line to
// NAME=beta and added notes.txt; its agent merged dev in and resolved the
// conflict keeping NAME=beta (a two-parent merge commit).
func resolvedSameLineHistory(t *testing.T, source, child string) {
	t.Helper()
	writeDeltaTally(t, child, map[int]string{10: "NAME=beta"})
	if err := os.WriteFile(filepath.Join(child, "notes.txt"), []byte("notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, child, "add", "tally.sh", "notes.txt")
	integrationGit(t, child, "commit", "-m", "task B")
	writeDeltaTally(t, source, map[int]string{10: "NAME=alpha"})
	integrationGit(t, source, "commit", "-am", "task A")
	cmd := exec.Command("git", "-C", child, "merge", "--no-edit", "dev")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("premise: merge should conflict: %v %s", err, out)
	}
	writeDeltaTally(t, child, map[int]string{10: "NAME=beta"})
	integrationGit(t, child, "add", "tally.sh")
	integrationGit(t, child, "commit", "--no-edit")
	if parents := integrationGit(t, child, "rev-list", "--parents", "-n", "1", "HEAD"); len(strings.Fields(parents)) != 3 {
		t.Fatalf("premise: task head is not a merge commit: %s", parents)
	}
}

// assertTargetUnchanged checks that a refusal left the target branch head,
// tally.sh and the target checkout exactly as before.
func (x *deltaIntegrationFixture) assertTargetUnchanged(t *testing.T, before []byte) {
	t.Helper()
	if head := integrationGit(t, x.source, "rev-parse", "HEAD"); head != x.targetHead {
		t.Fatalf("target head moved: %s", head)
	}
	after, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("target working tree changed: %q %v", after, err)
	}
	if status := integrationGit(t, x.source, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("target checkout not clean: %q", status)
	}
}

func assertAgentConflictGuidance(t *testing.T, err error) *projectTaskIntegrationError {
	t.Helper()
	if err == nil {
		t.Fatal("conflicting change was integrated")
	}
	message := err.Error()
	for _, want := range []string{"tally.sh", "swarm_send_message", "rebase its branch agent/task onto dev", "not merge", "Nothing was changed on dev"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal lacks %q: %s", want, message)
		}
	}
	for _, raw := range []string{"merge-tree", "/v3/", "recover-integrate", "CONFLICT (content)"} {
		if strings.Contains(message, raw) {
			t.Fatalf("refusal contains raw/unreachable %q: %s", raw, message)
		}
	}
	var failure *projectTaskIntegrationError
	if !errors.As(err, &failure) || failure.status != http.StatusConflict {
		t.Fatalf("not a 409 integration failure: %#v", err)
	}
	if delivery, ok := failure.delivery.(*pebblestore.TaskDeliveryAssessment); !ok || delivery.State != "conflict" || len(delivery.Files) != 1 || delivery.Files[0] != "tally.sh" {
		t.Fatalf("delivery: %#v", failure.delivery)
	}
	return failure
}

// Purpose: a branch whose agent merged the target in and resolved a same-line
// conflict must integrate its resolution. Regression (seen live): recovery
// 3-way merged from the task's recorded base, ignoring the merge the branch
// already contains, so the resolved branch was refused as conflicting with the
// line it had already resolved. Owners: IntegrateProjectTask →
// recoverProjectTaskDelta → worktree.PrepareTaskDeltaRecovery (taskDeltaBase).
// Real Git and a real store are required: the defect is in which commit
// merge-tree uses as its base. Cases: (a) the resolved branch integrates as one
// single-parent commit with the branch's resolution and file; (a2) a later
// target change elsewhere still merges 3-way; (b) a later target change over
// the same line is refused with the file named and the target untouched.
func TestIntegrateProjectTaskDeliversResolvedMergedUpBranch(t *testing.T) {
	t.Run("resolved-same-line", func(t *testing.T) {
		x := newDeltaFixture(t, func(source, child string) { resolvedSameLineHistory(t, source, child) })
		result, err := x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		head, _ := result["integration"].(map[string]any)["resulting_target_head"].(string)
		x.assertResolvedIntegrated(t, result, head, map[int]string{10: "NAME=beta"})
	})
	t.Run("target-moved-elsewhere-after-merge", func(t *testing.T) {
		x := newDeltaFixture(t, func(source, child string) {
			resolvedSameLineHistory(t, source, child)
			writeDeltaTally(t, source, map[int]string{2: "line 2 later", 10: "NAME=alpha"})
			integrationGit(t, source, "commit", "-am", "later target change elsewhere")
		})
		result, err := x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		head, _ := result["integration"].(map[string]any)["resulting_target_head"].(string)
		x.assertResolvedIntegrated(t, result, head, map[int]string{2: "line 2 later", 10: "NAME=beta"})
	})
	t.Run("target-moved-same-line-after-merge", func(t *testing.T) {
		x := newDeltaFixture(t, func(source, child string) {
			resolvedSameLineHistory(t, source, child)
			writeDeltaTally(t, source, map[int]string{10: "NAME=gamma"})
			integrationGit(t, source, "commit", "-am", "later target change on the same line")
		})
		before, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		assertAgentConflictGuidance(t, err)
		x.assertTargetUnchanged(t, before)
		if _, statErr := os.Stat(filepath.Join(x.source, "notes.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("refused task file reached the target: %v", statErr)
		}
	})
}

func (x *deltaIntegrationFixture) assertResolvedIntegrated(t *testing.T, result map[string]any, head string, tally map[int]string) {
	t.Helper()
	if result["status"] != "recovered" {
		t.Fatalf("status: %#v", result)
	}
	if got := integrationGit(t, x.source, "rev-parse", "HEAD"); got != head || head == x.targetHead {
		t.Fatalf("target head %s, reported %s, previous %s", got, head, x.targetHead)
	}
	if count := integrationGit(t, x.source, "rev-list", "--count", x.targetHead+"..HEAD"); count != "1" {
		t.Fatalf("expected exactly one new target commit, got %s", count)
	}
	if parents := integrationGit(t, x.source, "rev-list", "--parents", "-n", "1", "HEAD"); len(strings.Fields(parents)) != 2 || strings.Fields(parents)[1] != x.targetHead {
		t.Fatalf("integrated commit is not one single-parent commit on the previous target: %s", parents)
	}
	contents, err := os.ReadFile(filepath.Join(x.source, "tally.sh"))
	if err != nil || string(contents) != string(deltaTally(tally)) {
		t.Fatalf("target tally.sh: %q %v", contents, err)
	}
	if notes, err := os.ReadFile(filepath.Join(x.source, "notes.txt")); err != nil || string(notes) != "notes\n" {
		t.Fatalf("target notes.txt: %q %v", notes, err)
	}
	if status := integrationGit(t, x.source, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("target checkout not clean: %q", status)
	}
	updated, _, _ := x.f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, x.project, x.task.ID)
	if updated.Integration == nil || updated.Integration.State != "recovered" || updated.Integration.RecoveredHead != head || updated.Integration.RecoveryBase != x.baseHead {
		t.Fatalf("receipt: %#v", updated.Integration)
	}
}

// Purpose: an ordinary (unmerged) task branch that conflicts with the target
// must be refused as a 409 whose text an AI can act on (the conflicting files,
// nothing changed on the target, ask the agent to rebase and resolve), not raw
// git merge-tree output reported as HTTP 400. Owners: IntegrateProjectTask's
// PrepareTaskIntegration conflict branch and worktree.preflightSourceMerge
// (TaskIntegrationConflictError.Files); the MCP case crosses controlMCPIntegrate
// and the real HTTP route with an AI-key scoped request. Real Git produces the
// genuine conflict; the target head, file and checkout must stay unchanged.
func TestIntegrateProjectTaskPlainConflictGuidesAgentRebase(t *testing.T) {
	plain := func(source, child string) {
		writeDeltaTally(t, child, map[int]string{10: "NAME=beta"})
		integrationGit(t, child, "commit", "-am", "task B")
		writeDeltaTally(t, source, map[int]string{10: "NAME=alpha"})
		integrationGit(t, source, "commit", "-am", "task A")
	}
	t.Run("service", func(t *testing.T) {
		x := newDeltaFixture(t, plain)
		before, _ := os.ReadFile(filepath.Join(x.source, "tally.sh"))
		_, err := x.f.server.IntegrateProjectTask(context.Background(), testPrincipal(), x.project, x.task.ID)
		assertAgentConflictGuidance(t, err)
		x.assertTargetUnchanged(t, before)
		updated, _, _ := x.f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, x.project, x.task.ID)
		if updated.Integration == nil || updated.Integration.State != "conflict" || updated.Integration.Error != err.Error() {
			t.Fatalf("conflict receipt: %#v", updated.Integration)
		}
	})
	t.Run("mcp", func(t *testing.T) {
		x := newDeltaFixture(t, plain)
		before, _ := os.ReadFile(filepath.Join(x.source, "tally.sh"))
		scopes, err := aiKeyScopes("write")
		if err != nil {
			t.Fatal(err)
		}
		request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopes})
		call := &controlMCPCall{request: request, server: x.f.server, next: x.f.server.internalRouteHandler()}
		_, err = controlMCPIntegrate(call, map[string]any{"project_id": x.project, "task_id": x.task.ID}, "swarm_integrate_task", "swarm_list_tasks")
		if err == nil {
			t.Fatal("conflicting branch was integrated through MCP")
		}
		message := err.Error()
		for _, want := range []string{"409", "tally.sh", "swarm_send_message", "rebase its branch agent/task onto dev"} {
			if !strings.Contains(message, want) {
				t.Fatalf("MCP refusal lacks %q: %s", want, message)
			}
		}
		if strings.Contains(message, "merge-tree") || strings.Contains(message, "HTTP 400") {
			t.Fatalf("MCP refusal is raw Git: %s", message)
		}
		x.assertTargetUnchanged(t, before)
	})
}

package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: authorized AI saves must update active worker definitions without a
// human acceptance gate, while unsafe reviewer edits are atomic refusals.
// controlMCPUpdateWorker, handleWorker and WorkerStore.UpdateWorker own this
// boundary; real MCP dispatch and an isolated store prove persisted results.
func TestMCPWorkerDirectUpdate(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"admin"}})
	account := testPrincipal().AccountScopeID
	deployed, err := controlMCPDeployWorker(c, map[string]any{"name": "direct-reviewer", "role": "reviewer", "instructions": "old guidance"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(deployed)["worker"])["id"].(string)
	before, _, err := ws.GetWorker(account, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controlMCPUpdateWorker(c, map[string]any{"worker_id": id, "instructions": "new guidance"}); err != nil {
		t.Fatal(err)
	}
	after, _, err := ws.GetWorker(account, id)
	if err != nil || after.Instructions != "new guidance" || after.Revision != before.Revision+1 || after.PendingReview != nil || after.LifecycleState != before.LifecycleState {
		t.Fatalf("direct update %+v %v", after, err)
	}
	oldRevision, found, err := ws.GetWorkerRevision(account, id, before.Revision)
	if err != nil || !found || oldRevision.Worker.Instructions != "old guidance" {
		t.Fatalf("old revision changed %v", err)
	}
	newRevision, found, err := ws.GetWorkerRevision(account, id, after.Revision)
	if err != nil || !found || newRevision.Worker.Instructions != "new guidance" || newRevision.CommittedBy != testPrincipal().UserID || newRevision.ChangeSummary == "" {
		t.Fatalf("new revision not recorded %+v %v", newRevision, err)
	}
	if _, err = controlMCPDefineAgent(c, map[string]any{"name": "writing-agent", "instructions": "Write", "tools": "build"}); err != nil {
		t.Fatal(err)
	}
	if _, err = controlMCPUpdateWorker(c, map[string]any{"worker_id": id, "agent": "writing-agent"}); err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("unsafe update accepted %v", err)
	}
	unchanged, _, err := ws.GetWorker(account, id)
	if err != nil || !reflect.DeepEqual(after, unchanged) {
		t.Fatalf("failed save mutated worker %v", err)
	}
	if _, err = c.dispatch(http.MethodPut, "/v3/workers/"+id, nil, map[string]any{"expected_revision": before.Revision, "instructions": "stale"}); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = controlMCPDeployWorker(c, map[string]any{"worker": id, "instructions": "deployed guidance", "description": "updated"}); err != nil {
		t.Fatal(err)
	}
	final, _, err := ws.GetWorker(account, id)
	if err != nil || final.Instructions != "deployed guidance" || final.Description != "updated" || final.PendingReview != nil {
		t.Fatalf("deploy did not save %+v %v", final, err)
	}
	if _, err = controlMCPDeployWorker(c, map[string]any{"worker": id, "role": "coder", "instructions": "forbidden"}); err == nil {
		t.Fatal("role change accepted")
	}
	unchanged, _, err = ws.GetWorker(account, id)
	if err != nil || !reflect.DeepEqual(final, unchanged) {
		t.Fatal("role refusal changed worker")
	}
}

// Purpose: AcceptWorker must refuse a stale legacy snapshot without losing
// later direct saves. The real store proves atomic refusal and compatibility
// for an untouched legacy proposal without reintroducing proposal creation.
func TestWorkerLegacyReviewAfterDirectSave(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	account := testPrincipal().AccountScopeID
	result, err := controlMCPDeployWorker(c, map[string]any{"name": "legacy-review", "role": "finder", "instructions": "original"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(result)["worker"])["id"].(string)
	w, _, err := ws.GetWorker(account, id)
	if err != nil {
		t.Fatal(err)
	}
	// Admission receipts must retain the exact pre-save revision.
	admit := func(key string, worker store.WorkerRecord) store.WorkerRunRecord {
		t.Helper()
		model := store.CloneSessionModelProfileSnapshot(worker.ModelProfile)
		if model != nil {
			model.UseAccountDefault, model.ActionUseAccountDefault, model.PlanUseAccountDefault = false, false, false
		}
		run, err := ws.AdmitWorkerRun(account, store.WorkerRunAdmission{WorkerID: id, UserID: testPrincipal().UserID, WorkspaceID: "fixture-workspace", ResolvedWorkspaceID: "fixture-workspace", RequestSource: "direct", Input: map[string]any{"prompt": "Inspect the requested change"}, IdempotencyKey: key, ExpectedWorkerRevision: worker.Revision, ResolvedModelProfile: model})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	oldRun := admit("before-save", w)
	proposal := w
	proposal.Instructions = "legacy proposed instructions"
	proposal.LocalBindings = nil
	proposal.LifecycleState = store.WorkerLifecycleStatePending
	w.PendingReview = &proposal
	if err = c.server.sessions.Store().Underlying().PutJSON(store.KeyWorker(account, id), w); err != nil {
		t.Fatal(err)
	}
	description := "saved directly"
	saved, err := ws.UpdateWorker(account, testPrincipal().UserID, id, w.Revision, store.UpdateWorkerRequest{Description: &description, Instructions: &description}, nil)
	if err != nil || saved.Description != description || !reflect.DeepEqual(saved.PendingReview, &proposal) {
		t.Fatalf("legacy proposal lost %v", err)
	}
	newRun := admit("after-save", saved)
	for _, check := range []struct {
		run  store.WorkerRunRecord
		want string
	}{{oldRun, "original"}, {newRun, description}} {
		history, found, err := ws.GetWorkerRevision(account, id, check.run.WorkerRevision)
		if err != nil || !found || history.Worker.Instructions != check.want {
			t.Fatalf("pinned instructions %+v %v", history, err)
		}
	}
	if oldRun.WorkerRevision == newRun.WorkerRevision {
		t.Fatal("admission did not pin new revision")
	}
	_, err = ws.AcceptWorker(account, testPrincipal().UserID, id, saved.Revision, nil)
	if !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "predates a direct save") {
		t.Fatalf("stale acceptance not refused %v", err)
	}
	unchanged, _, err := ws.GetWorker(account, id)
	if err != nil || !reflect.DeepEqual(saved, unchanged) {
		t.Fatal("stale acceptance mutated saved edits")
	}
	// Restore the untouched legacy fixture, which must still be acceptable.
	if err = c.server.sessions.Store().Underlying().PutJSON(store.KeyWorker(account, id), w); err != nil {
		t.Fatal(err)
	}
	accepted, err := ws.AcceptWorker(account, testPrincipal().UserID, id, w.Revision, nil)
	if err != nil || accepted.PendingReview != nil || accepted.Instructions != proposal.Instructions {
		t.Fatalf("legacy acceptance failed %+v %v", accepted, err)
	}
}

// Purpose: UpdateWorker retains pending capability authoring but never grants
// capabilities to established workers. Real store saves and acceptance prove
// both rejection postconditions at the narrow persistence boundary.
func TestWorkerDirectUpdateCapabilities(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	p := testPrincipal()
	caps := []store.WorkerCapabilityRequest{{Name: "write", Type: "tool"}}
	pending, err := ws.CreateWorker(p.AccountScopeID, p.UserID, store.CreateWorkerRequest{Name: "pending-capabilities", InitialLifecycleState: store.WorkerLifecycleStatePending, RequestedCapabilities: caps}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := "edited pending instructions"
	saved, err := ws.UpdateWorker(p.AccountScopeID, p.UserID, pending.ID, pending.Revision, store.UpdateWorkerRequest{Instructions: &text}, nil)
	if err != nil || saved.Instructions != text || !reflect.DeepEqual(saved.RequestedCapabilities, caps) {
		t.Fatalf("pending edit refused %+v %v", saved, err)
	}
	if _, err = ws.AcceptWorker(p.AccountScopeID, p.UserID, saved.ID, saved.Revision, nil); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("capability acceptance %v", err)
	}
	unchanged, _, err := ws.GetWorker(p.AccountScopeID, saved.ID)
	if err != nil || !reflect.DeepEqual(saved, unchanged) {
		t.Fatal("acceptance mutated pending worker")
	}
	deployed, err := controlMCPDeployWorker(c, map[string]any{"name": "established-capabilities", "role": "finder", "instructions": "Inspect"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(deployed)["worker"])["id"].(string)
	before, _, err := ws.GetWorker(p.AccountScopeID, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.UpdateWorker(p.AccountScopeID, p.UserID, id, before.Revision, store.UpdateWorkerRequest{Instructions: &text, RequestedCapabilities: caps}, nil); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("capability save %v", err)
	}
	unchanged, _, err = ws.GetWorker(p.AccountScopeID, id)
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatal("refused capability save mutated worker")
	}
}

// Purpose: normal Dispatch must choose the current revision itself, while Start
// prepares an earlier admitted job from its immutable revision after a save.
// Real admission, Git allocation and V3 session messages prove the prepared
// instructions. Preparation is held before allocation until after the edit;
// executor wake is suppressed, so no provider is required.
func TestWorkerDirectUpdatePreparedInstructions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c, _ := newMCPPresetTestCall(t)
	server := c.server
	sessions := server.sessions
	p := testPrincipal()
	db := sessions.Store().Underlying()
	for _, name := range []string{"action", "plan", "finder"} {
		if err := store.NewModelCatalogStore(db).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: name, ThinkingOptions: []string{"high"}, ContextWindow: 32000, MaxOutputTokens: 4096}); err != nil {
			t.Fatal(err)
		}
	}
	workspaces := workspace.NewService(store.NewWorkspaceStore(db))
	server.workspace = workspaces
	trees := worktree.NewService(store.NewWorktreeStore(db), workspaces, nil)
	server.SetWorktreeService(trees)
	for ready := false; !ready; {
		var err error
		ready, err = sessions.Store().BackfillRepositoryHistory(100)
		if err != nil {
			t.Fatal(err)
		}
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "primary")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "fixture")
	added, err := workspaces.AddForPrincipal(p, folder, "fixture", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.topology.EnsureLocalSelfPlacementForPrincipal(p.AccountScopeID, p.UserID); err != nil {
		t.Fatal(err)
	}
	entry, _, err := workspaces.GetByWorkspaceIDForPrincipal(p, added.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(p.AccountScopeID, p.UserID, entry); err != nil {
		t.Fatal(err)
	}
	project := &store.ProjectRecord{ID: "prepared-project", Name: "Fixture", Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: added.WorkspacePath, Role: "primary_code"}}}
	if err = sessions.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	runner := server.runner.(*runruntime.Service)
	runner.SetWorkspaceService(workspaces)
	hold := true
	held := errors.New("fixture holds preparation after admission")
	runner.SetSessionDeployCanonicalizer(func(in runruntime.SessionDeployCanonicalizeInput) (runruntime.SessionDeployCanonicalization, error) {
		if hold {
			return runruntime.SessionDeployCanonicalization{}, held
		}
		return server.CanonicalizeSessionDeploy(in)
	})
	execution, err := runruntime.NewWorkerExecutionService(runner, sessions.Store(), trees, server.applySessionV3PrimaryMutation, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	deployed, err := controlMCPDeployWorker(c, map[string]any{"name": "prepared-finder", "role": "finder", "instructions": "original standing guidance"})
	if err != nil {
		t.Fatal(err)
	}
	id := controlMCPMap(controlMCPMap(deployed)["worker"])["id"].(string)
	dispatch := func(key string) (store.WorkerRunRecord, error) {
		return execution.Dispatch(context.Background(), p.AccountScopeID, p.UserID, store.WorkerRunAdmission{WorkerID: id, WorkspaceID: added.WorkspaceID, RequestSource: "direct", IdempotencyKey: key, Input: map[string]any{"prompt": "Inspect files"}})
	}
	old, err := dispatch("before")
	if !errors.Is(err, held) {
		t.Fatalf("expected held preparation got %v", err)
	}
	if _, err = controlMCPUpdateWorker(c, map[string]any{"worker_id": id, "instructions": "new standing guidance"}); err != nil {
		t.Fatal(err)
	}
	hold = false
	if err = execution.Start(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	next, err := dispatch("after")
	if err != nil {
		t.Fatal(err)
	}
	if next.WorkerRevision <= old.WorkerRevision {
		t.Fatal("normal dispatch did not select saved revision")
	}
	for _, check := range []struct {
		run          store.WorkerRunRecord
		want, absent string
	}{{old, "original standing guidance", "new standing guidance"}, {next, "new standing guidance", "original standing guidance"}} {
		messages, err := sessions.ListMessages(check.run.SessionID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range messages {
			if m.Role == "user" && strings.Contains(m.Content, check.want) {
				found = true
			}
			if strings.Contains(m.Content, check.absent) {
				t.Fatal("prepared wrong revision")
			}
		}
		if !found {
			t.Fatalf("missing prepared instructions %q", check.want)
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// enableProposalWorkers gives a proposal test Swarm its worker service: the
// run service's ManageWorkers with its worker execution service, over a
// fixture catalog model and account model settings (as seedWorkers does; no
// provider is reached and admission's enqueue refuses, so no run starts).
func enableProposalWorkers(t *testing.T, p *proposalTestSwarm) {
	t.Helper()
	db := p.s.sessions.Store().Underlying()
	if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "fixture-provider", Model: "fixture-model", ThinkingOptions: []string{"low"}, DefaultThinking: "low"}); err != nil {
		t.Fatal(err)
	}
	assigned := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: "fixture-model", Thinking: "low"}
	settings := pebblestore.NewAgentModelSettingsStore(db)
	if _, err := settings.PutForAccount(pebblestore.AgentModelSettingsRecord{AccountScopeID: p.account, Swarm: pebblestore.SwarmAgentModelAssignments{Action: assigned, Plan: assigned}, SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: assigned, Finder: assigned, Coder: assigned, Designer: assigned, Router: assigned}}); err != nil {
		t.Fatal(err)
	}
	models := model.NewService(pebblestore.NewModelStore(db), nil, model.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
	runner := runruntime.NewService(p.s.sessions, models, nil, nil, nil, nil, nil, nil)
	runner.SetAgentModelSettingsService(agentmodelsettings.NewService(settings))
	execution, err := runruntime.NewWorkerExecutionService(runner, p.s.sessions.Store(), &worktree.Service{}, p.s.applySessionV3PrimaryMutation, func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	p.s.runner = runner
}

// pendingWorker stores a worker waiting for the person's acceptance.
func (p *proposalTestSwarm) pendingWorker(t *testing.T, name string) pebblestore.WorkerRecord {
	t.Helper()
	w, err := p.s.sessions.Store().WorkerStore().CreateWorker(p.account, p.user, pebblestore.CreateWorkerRequest{Name: name, Instructions: "Check the build.", Role: "finder", InitialLifecycleState: pebblestore.WorkerLifecycleStatePending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (p *proposalTestSwarm) worker(t *testing.T, id string) (pebblestore.WorkerRecord, bool) {
	t.Helper()
	w, found, err := p.s.sessions.Store().WorkerStore().GetWorker(p.account, id)
	if err != nil {
		t.Fatal(err)
	}
	return w, found && w.LifecycleState != pebblestore.WorkerLifecycleStateDeleted
}

// Purpose: the person accepts or discards a worker proposal on this machine
// through POST /v3/proposals/decide, which runs the machine's own worker
// accept and delete routes in process. Requirement: a stale revision is
// refused 409 with nothing changed; accept at the revision shown activates
// the worker; discard deletes a new worker waiting for acceptance; a pending
// change of an active worker cannot be discarded alone (deleting would
// remove the worker), so it is refused 409 with the worker and its change
// untouched. Threat: a decision on a revision the person did not see; a
// discard deleting an active worker. Boundary: handleProposals,
// DecideProposal, decideWorkerProposal over handleWorkerAccept and
// handleWorkerDelete with the real worker store and execution service.
// Narrowest layer: the HTTP route over real stores.
func TestProposalsDecideLocalWorker(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	enableProposalWorkers(t, home)
	w := home.pendingWorker(t, "nightly")
	listed := home.list(t, "")
	if len(listed) != 1 || listed[0]["kind"] != "worker" || listed[0]["id"] != w.ID || listed[0]["change"] != "new" || listed[0]["waiting_on"] != "person" {
		t.Fatalf("pending list: %v", listed)
	}
	if code, out := home.decide(t, ProposalDecision{Kind: "worker", ID: w.ID, ExpectedRevision: w.Revision + 1, Decision: "accept"}, nil); code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "no longer pending") {
		t.Fatalf("stale accept: %d %v", code, out)
	}
	if code, _ := home.decide(t, ProposalDecision{Kind: "worker", ID: w.ID, ExpectedRevision: w.Revision + 1, Decision: "discard"}, nil); code != http.StatusConflict {
		t.Fatalf("stale discard: %d", code)
	}
	if still, ok := home.worker(t, w.ID); !ok || still.LifecycleState != pebblestore.WorkerLifecycleStatePending || still.Revision != w.Revision {
		t.Fatalf("a stale decision changed the worker: %+v", still)
	}
	if code, out := home.decide(t, ProposalDecision{Kind: "worker", ID: w.ID, ExpectedRevision: w.Revision, Decision: "accept"}, nil); code != http.StatusOK || out["decision"] != "accept" {
		t.Fatalf("accept: %d %v", code, out)
	}
	active, ok := home.worker(t, w.ID)
	if !ok || active.LifecycleState == pebblestore.WorkerLifecycleStatePending {
		t.Fatalf("accepted worker: %+v", active)
	}
	if len(home.list(t, "")) != 0 {
		t.Fatal("an accepted worker is still listed")
	}

	// A new worker, discarded.
	other := home.pendingWorker(t, "other")
	if code, out := home.decide(t, ProposalDecision{Kind: "worker", ID: other.ID, ExpectedRevision: other.Revision, Decision: "discard"}, nil); code != http.StatusOK {
		t.Fatalf("discard: %d %v", code, out)
	}
	if _, ok := home.worker(t, other.ID); ok {
		t.Fatal("discard kept the worker")
	}

	// A pending change of the active worker: discarding it alone is refused.
	change := active
	change.Instructions = "Proposed instructions"
	change.LifecycleState = pebblestore.WorkerLifecycleStatePending
	active.PendingReview = &change
	if err := home.s.sessions.Store().Underlying().PutJSON(pebblestore.KeyWorker(home.account, active.ID), active); err != nil {
		t.Fatal(err)
	}
	listed = home.list(t, "")
	if len(listed) != 1 || listed[0]["change"] != "change" || listed[0]["replaces"] != active.ID {
		t.Fatalf("the pending change: %v", listed)
	}
	code, out := home.decide(t, ProposalDecision{Kind: "worker", ID: active.ID, ExpectedRevision: active.Revision, Decision: "discard"}, nil)
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "discarding the change alone is not available") {
		t.Fatalf("discarding an active worker's change: %d %v", code, out)
	}
	if still, ok := home.worker(t, active.ID); !ok || still.PendingReview == nil || still.Instructions != active.Instructions || still.Revision != active.Revision {
		t.Fatalf("the refused discard changed the worker: %+v", still)
	}
}

// Purpose: a worker proposal on a linked machine is made from the home's
// conversation (manage_workers propose machine=) and decided by the person
// from the home, over the existing link. Requirement: the propose answer
// carries the conversation's pending request naming the machine and
// next_action says to tell the person and wait; manage_workers list
// machine= shows it under pending_proposals; the route accepts at the
// expected revision on the machine (stale is refused 409), discards a second
// one there and answers the chat's request for it; the home's worker store
// is never touched. Threat: worker proposals on linked machines being a dead
// end; a decision applied on the wrong machine. Boundary: ManageWorkers
// (machine=) -> CallFleetWork -> the machine's worker actions; ListProposals,
// DecideProposal over two in-process daemons linked like production.
func TestProposalsDecideWorkerOnLinkedMachine(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	enableProposalWorkers(t, home)
	enableProposalWorkers(t, box)
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	// A worker on box works in one of box's folders.
	box.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(box.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := box.s.workspace.AddForPrincipal(box.principal(), folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	box.s.runner.(*runruntime.Service).SetWorkspaceService(box.s.workspace)
	if err := box.s.sessions.Store().PutProject(box.account, &pebblestore.ProjectRecord{ID: "box-workers", AccountID: box.account, Name: "Box workers", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithProposingSession(context.Background(), session)
	propose := func(name string) (string, uint64, string) {
		t.Helper()
		out, err := home.s.ManageWorkers(ctx, home.principal(), session, agentruntime.SwarmOrchestratorAgentID, map[string]any{"action": "propose", "machine": "box", "name": name, "instructions": "Check the build.", "role": "finder", "local_bindings": map[string]any{"primary": added.WorkspaceID}})
		if err != nil {
			t.Fatalf("propose machine=box: %v", err)
		}
		answer := answerData(answerMap(mustJSONRoundTrip(out)))
		worker, _ := answer["worker"].(map[string]any)
		id := mapText(worker, "id")
		request, _ := answer["decision_request"].(map[string]any)
		permissionID, _ := request["permission_id"].(string)
		proposal, _ := request["proposal"].(map[string]any)
		if id == "" || permissionID == "" || proposal["machine"] != "box" || proposal["id"] != id || !strings.Contains(fmt.Sprint(answer["next_action"]), "an AI cannot accept it") {
			t.Fatalf("propose machine=box answer: %v", answer)
		}
		return id, mapUint(worker, "revision"), permissionID
	}
	id, revision, _ := propose("box-nightly")
	if w, ok := box.worker(t, id); !ok || w.LifecycleState != pebblestore.WorkerLifecycleStatePending {
		t.Fatalf("the proposal is not on box: %+v", w)
	}
	if _, ok := home.worker(t, id); ok {
		t.Fatal("the proposal landed on the home")
	}
	out, err := home.s.ManageWorkers(ctx, home.principal(), session, agentruntime.SwarmOrchestratorAgentID, map[string]any{"action": "list", "machine": "box"})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := answerData(answerMap(mustJSONRoundTrip(out)))["pending_proposals"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["machine"] != "box" || pending[0].(map[string]any)["id"] != id {
		t.Fatalf("list machine=box pending_proposals: %v", answerMap(mustJSONRoundTrip(out)))
	}
	if code, out := home.decide(t, ProposalDecision{Machine: "box", Kind: "worker", ID: id, ExpectedRevision: revision + 5, Decision: "accept"}, nil); code != http.StatusConflict {
		t.Fatalf("stale linked accept: %d %v", code, out)
	}
	if code, out := home.decide(t, ProposalDecision{Machine: "box", Kind: "worker", ID: id, ExpectedRevision: revision, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("linked accept: %d %v", code, out)
	}
	if w, ok := box.worker(t, id); !ok || w.LifecycleState == pebblestore.WorkerLifecycleStatePending {
		t.Fatalf("not accepted on box: %+v", w)
	}
	otherID, otherRevision, otherPermission := propose("box-other")
	code, decided := home.decide(t, ProposalDecision{Machine: "box", Kind: "worker", ID: otherID, ExpectedRevision: otherRevision, Decision: "discard"}, nil)
	if code != http.StatusOK || decided["requests_answered"] != float64(1) {
		t.Fatalf("linked discard: %d %v", code, decided)
	}
	if _, ok := box.worker(t, otherID); ok {
		t.Fatal("discard kept the worker on box")
	}
	if record, _, _ := home.perms.GetPermission(session, otherPermission); record.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("the chat request after the discard: %+v", record)
	}
}

// Purpose: discarding a workflow proposal never races an accept or a
// re-proposal. Requirement: DELETE /v3/workflows/{id}?expected_revision=N
// deletes only that revision under the store's lock; a re-proposed revision
// and an already-accepted workflow (same id, newer revision) are refused 409
// with the workflow unchanged; a malformed expected_revision is refused 400;
// the revision shown deletes it. Threat: a discard the person made of
// revision 1 deleting revision 2 the AI proposed since, or the workflow the
// person just accepted. Boundary: handleJobAutomations DELETE ->
// DeleteWorkflow -> jobautomation.Store.DeleteChangeAt. Narrowest layer: the
// route, called directly so DecideProposal's own read cannot hide a race.
func TestWorkflowDeleteRevisionGuard(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	del := func(id, revision string) int {
		t.Helper()
		path := JobAutomationsPath + "/" + id
		if revision != "" {
			path += "?expected_revision=" + revision
		}
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodDelete, path, nil), home.user, home.account)
		w := httptest.NewRecorder()
		home.s.handleJobAutomations(w, r)
		return w.Code
	}
	first := home.propose(t, "", map[string]any{"name": "guard", "project": "Project home", "steps": proposalSteps("v1")})
	id := first["automation"].(map[string]any)["id"].(string)
	home.propose(t, "", map[string]any{"name": "guard", "project": "Project home", "steps": proposalSteps("v2")})
	if code := del(id, "1"); code != http.StatusConflict {
		t.Fatalf("discarding revision 1 after a re-proposal: %d", code)
	}
	if a, ok := home.workflow(t, id); !ok || !a.Proposed || a.Revision != 2 {
		t.Fatalf("a refused discard changed the proposal: %+v", a)
	}
	if code := del(id, "x"); code != http.StatusBadRequest {
		t.Fatalf("a malformed expected_revision: %d", code)
	}
	// Accepted since: the person's discard of revision 2 must not delete it.
	if code, out := home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 2, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("accept: %d %v", code, out)
	}
	if code := del(id, "2"); code != http.StatusConflict {
		t.Fatalf("discarding an accepted workflow: %d", code)
	}
	accepted, ok := home.workflow(t, id)
	if !ok || accepted.Proposed {
		t.Fatalf("a refused discard removed the accepted workflow: %+v", accepted)
	}
	if code := del(id, fmt.Sprint(accepted.Revision)); code != http.StatusOK {
		t.Fatalf("delete at the current revision: %d", code)
	}
	if _, ok := home.workflow(t, id); ok {
		t.Fatal("delete at the current revision kept the workflow")
	}
}

// Purpose: proposal reads carry the caller's scoped token, never an
// unscoped internal request. Requirement: a caller whose key cannot read
// automations (a request's scoped token, or a Swarm Control call's held
// scopes) is refused the proposal list here (the workflow route's own scope
// check) and on a linked machine (before the link is used); a caller with
// automations:read, and one with no key (the person's app, the
// Orchestrator), reads them. Threat: a narrow key reading automations
// through the proposals service as the unscoped owner. Boundary:
// listProposals -> proposalReadTransportFor / proposalReadToken ->
// localProposalCallAs over the machine's routes.
func TestProposalReadsUseCallerScopedToken(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	home.propose(t, "", map[string]any{"name": "scoped", "project": "Project home", "steps": proposalSteps("Review")})
	narrow := context.WithValue(context.Background(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{ID: "narrow", Scopes: []string{"sessions:read"}})
	for _, machine := range []string{"", "box"} {
		if _, err := home.s.ListProposals(narrow, home.principal(), machine); err == nil || linkedTaskStatus(err) != http.StatusForbidden {
			t.Fatalf("machine %q: a key without automations:read read proposals: %v", machine, err)
		}
		control := tool.WithControlScopes(context.Background(), []string{"sessions:read"})
		if _, err := home.s.ListProposals(control, home.principal(), machine); err == nil || linkedTaskStatus(err) != http.StatusForbidden {
			t.Fatalf("machine %q: Swarm Control scopes without automations:read read proposals: %v", machine, err)
		}
	}
	reader := context.WithValue(context.Background(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{ID: "reader", Scopes: []string{"automations:read"}})
	for _, ctx := range []context.Context{reader, context.Background()} {
		list, err := home.s.ListProposals(ctx, home.principal(), "")
		if err != nil || len(list) != 1 {
			t.Fatalf("a reader's list: %v %v", list, err)
		}
	}
	token := proposalReadToken(context.Background(), home.principal())
	if !reflect.DeepEqual(token.Scopes, []string{"automations:read"}) {
		t.Fatalf("a keyless caller's read token: %+v", token)
	}
}

// Purpose: a webhook workflow's trigger key, returned once by the accept
// route, reaches only the person's decision answer. Requirement: the key is
// moved to the answer's top level with a copy-it-now note and removed from
// the relayed result; an answer without a key gains neither field. Threat:
// the one-time secret copied into a result an AI or a log could read twice.
// Boundary: hoistProposalTriggerKey, the one place DecideProposal shapes the
// answer (the pending request records only the decision and its reason).
func TestProposalDecisionTriggerKeyShownOnce(t *testing.T) {
	result := map[string]any{"ok": true, "automation": map[string]any{"id": "jauto_1"}, "trigger_key": "whk_secret"}
	answer := map[string]any{"ok": true}
	hoistProposalTriggerKey(answer, result)
	if answer["trigger_key"] != "whk_secret" || !strings.Contains(fmt.Sprint(answer["trigger_key_note"]), "copy it now") {
		t.Fatalf("the person's answer: %v", answer)
	}
	if _, kept := result["trigger_key"]; kept {
		t.Fatalf("the relayed result kept the key: %v", result)
	}
	plain := map[string]any{"ok": true}
	hoistProposalTriggerKey(plain, map[string]any{"automation": map[string]any{}})
	if _, ok := plain["trigger_key"]; ok {
		t.Fatalf("an answer without a key: %v", plain)
	}
	raw, _ := json.Marshal(answer)
	if strings.Count(string(raw), "whk_secret") != 1 {
		t.Fatalf("the key appears more than once: %s", raw)
	}
}

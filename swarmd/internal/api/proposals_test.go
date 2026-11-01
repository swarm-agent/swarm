package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// proposalTestSwarm is one in-process Swarm with workflows, a permission
// service and a project whose folder workflows bind to.
type proposalTestSwarm struct {
	*fleetTestSwarm
	workflows *jobautomation.Store
	perms     *permission.Service
}

func newProposalTestSwarm(t *testing.T, name string) *proposalTestSwarm {
	t.Helper()
	f := newFleetTestSwarm(t, name)
	t.Cleanup(f.cleanup)
	workflows := jobautomation.NewStore(f.s.sessions.Store().Underlying())
	f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: workflows}))
	perms := permission.NewService(pebblestore.NewPermissionStore(f.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(f.s.sessions)
	f.s.perm = perms
	if err := f.s.sessions.Store().PutProject(f.account, &pebblestore.ProjectRecord{ID: "p-" + name, AccountID: f.account, Name: "Project " + name, Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: t.TempDir(), Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	return &proposalTestSwarm{fleetTestSwarm: f, workflows: workflows, perms: perms}
}

// propose runs manage_workflows propose as the Orchestrator's conversation
// session (session "" for a caller with none).
func (p *proposalTestSwarm) propose(t *testing.T, session string, args map[string]any) map[string]any {
	t.Helper()
	ctx, who := context.Background(), p.user
	if session != "" {
		ctx, who = tool.WithProposingSession(ctx, session), session
	}
	args["action"] = "propose"
	out, err := p.s.ManageWorkflows(ctx, p.principal(), who, args)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	// As a caller reads it (JSON).
	return answerMap(mustJSONRoundTrip(out))
}

func (p *proposalTestSwarm) principal() identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: p.user, AccountScopeID: p.account}
}

// decide is the person's (scopes nil) or a scoped key's POST
// /v3/proposals/decide.
func (p *proposalTestSwarm) decide(t *testing.T, in ProposalDecision, scopes []string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(in)
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, ProposalsPath+"/decide", strings.NewReader(string(body))), p.user, p.account)
	if scopes != nil {
		r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{ID: "key", Scopes: scopes})
	}
	w := httptest.NewRecorder()
	p.s.handleProposals(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (p *proposalTestSwarm) list(t *testing.T, machine string) []map[string]any {
	t.Helper()
	path := ProposalsPath
	if machine != "" {
		path += "?machine=" + machine
	}
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, path, nil), p.user, p.account)
	w := httptest.NewRecorder()
	p.s.handleProposals(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list %q: %d %s", machine, w.Code, w.Body)
	}
	var out struct {
		Proposals []map[string]any `json:"proposals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Proposals
}

// answer is a session's permission answer through the session route: the
// person's (scopes nil) or a scoped token's (an AI key, or resolve_permission's
// internal call).
func (p *proposalTestSwarm) answer(t *testing.T, session, permissionID, action string, scopes []string) (int, map[string]any) {
	t.Helper()
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session+"/permissions/"+permissionID+"/resolve", strings.NewReader(`{"action":"`+action+`"}`)), p.user, p.account)
	if scopes != nil {
		r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{ID: "key", Scopes: scopes})
	}
	w := httptest.NewRecorder()
	p.s.handleSessionV3PrimaryPermissionResolve(w, r, p.principal(), session, permissionID)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (p *proposalTestSwarm) workflow(t *testing.T, id string) (jobautomation.Automation, bool) {
	t.Helper()
	a, err := p.workflows.Get(p.account, id)
	if errors.Is(err, jobautomation.ErrNotFound) {
		return jobautomation.Automation{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return a, true
}

func proposalSteps(briefs ...string) []any {
	steps := []any{}
	for i, brief := range briefs {
		steps = append(steps, map[string]any{"id": fmt.Sprintf("s%d", i+1), "type": "agent", "worker": "reviewer", "brief": brief})
	}
	return steps
}

// linkProposalSwarms links home to box (box's owner session held by home),
// as production links them.
func linkProposalSwarms(t *testing.T, home, box *proposalTestSwarm) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": box.invite(t), "name": "box"})
	if code, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	fleetActivityTestSetWork(t, home.fleetTestSwarm, "box", pebblestore.RuleAuto)
}

// Purpose: the person decides a workflow proposal on this machine through
// POST /v3/proposals/decide, which reuses the machine's own accept and delete
// routes in process. Requirement: accept makes the proposal the workflow at
// the expected revision; discard removes only a still-pending proposal; a
// revision that moved (the AI re-proposed) is refused 409 naming why, with
// nothing changed. Threat: a stale accept publishing a definition the person
// never saw; a discard deleting an active workflow. Boundary:
// Server.handleProposals, DecideProposal, decideWorkflowProposal over
// handleJobAutomations. Narrowest layer: the HTTP route over real stores.
func TestProposalsDecideLocalWorkflow(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	first := home.propose(t, "", map[string]any{"name": "review", "project": "Project home", "steps": proposalSteps("Review v1")})
	proposal := first["automation"].(map[string]any)
	id := proposal["id"].(string)
	if request, _ := first["decision_request"].(map[string]any); request["status"] != "not_requested" || !strings.HasPrefix(fmt.Sprint(first["next_action"]), "Tell the person this workflow proposal waits for them") {
		t.Fatalf("a route-less propose answer: %v", first)
	}
	listed := home.list(t, "")
	if len(listed) != 1 || listed[0]["id"] != id || listed[0]["kind"] != "workflow" || listed[0]["waiting_on"] != "person" || listed[0]["change"] != "new" {
		t.Fatalf("pending list: %v", listed)
	}
	// The AI revises it: the person's revision 1 is stale.
	second := home.propose(t, "", map[string]any{"name": "review", "project": "Project home", "steps": proposalSteps("Review v2")})
	revised := second["automation"].(map[string]any)
	if revised["id"] != id || revised["revision"] != float64(2) {
		t.Fatalf("re-proposal: %v", revised)
	}
	code, out := home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 1, Decision: "accept"}, nil)
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "no longer pending at revision 1") {
		t.Fatalf("stale accept: %d %v", code, out)
	}
	if a, ok := home.workflow(t, id); !ok || !a.Proposed || a.Revision != 2 {
		t.Fatalf("a stale accept changed the proposal: %+v %v", a, ok)
	}
	code, out = home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 1, Decision: "discard"}, nil)
	if code != http.StatusConflict {
		t.Fatalf("stale discard: %d %v", code, out)
	}
	if _, ok := home.workflow(t, id); !ok {
		t.Fatal("a stale discard removed the proposal")
	}
	// Accept at the revision shown.
	code, out = home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 2, Decision: "accept"}, nil)
	if code != http.StatusOK || out["decision"] != "accept" {
		t.Fatalf("accept: %d %v", code, out)
	}
	active, ok := home.workflow(t, id)
	if !ok || active.Proposed || active.Steps[0].Brief != "Review v2" {
		t.Fatalf("accepted workflow: %+v", active)
	}
	if len(home.list(t, "")) != 0 {
		t.Fatal("an accepted proposal is still listed")
	}
	// Discard never touches an active workflow.
	code, _ = home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: active.Revision, Decision: "discard"}, nil)
	if code != http.StatusConflict {
		t.Fatalf("discarding an active workflow: %d", code)
	}
	if _, ok := home.workflow(t, id); !ok {
		t.Fatal("discard removed an active workflow")
	}
	// A change of it, discarded: the active revision stays.
	change := home.propose(t, "", map[string]any{"id": id, "steps": []any{map[string]any{"id": "s1", "brief": "Review v3"}}})
	changeID := change["automation"].(map[string]any)["id"].(string)
	code, out = home.decide(t, ProposalDecision{Kind: "workflow", ID: changeID, ExpectedRevision: 1, Decision: "discard"}, nil)
	if code != http.StatusOK {
		t.Fatalf("discard: %d %v", code, out)
	}
	if _, ok := home.workflow(t, changeID); ok {
		t.Fatal("discard kept the proposal")
	}
	if still, _ := home.workflow(t, id); still.Steps[0].Brief != "Review v2" || still.Revision != active.Revision {
		t.Fatalf("discard changed the active workflow: %+v", still)
	}
	// Bad input is refused before anything is read.
	for _, bad := range []ProposalDecision{{Kind: "flow", ID: id, ExpectedRevision: 1, Decision: "accept"}, {Kind: "workflow", ID: "../x", ExpectedRevision: 1, Decision: "accept"}, {Kind: "workflow", ID: id, Decision: "accept"}, {Kind: "workflow", ID: id, ExpectedRevision: 1, Decision: "approve"}} {
		if code, _ := home.decide(t, bad, nil); code != http.StatusBadRequest {
			t.Fatalf("bad decision %+v: %d", bad, code)
		}
	}
}

// Purpose: a proposal on a linked machine is decided from the home, by the
// person, over the existing link: the machine's own accept route does the
// work. Requirement: GET /v3/proposals?machine= lists the machine's pending
// proposals; POST /v3/proposals/decide with machine accepts or discards there
// at the expected revision, and a stale one is refused 409; the home's store
// is never touched. An Orchestrator's propose with machine= gives its
// conversation a pending request naming the machine, which the person's
// answer resolves (accepting on the machine). Threat: proposals on linked
// machines being a dead end; a decision applied on the wrong machine.
// Boundary: CallFleetWork (manage_workflows propose/list), ListProposals,
// DecideProposal, answerProposalRequest over two in-process daemons linked
// like production (the transport seam the fleet tests use).
func TestProposalsDecideOnLinkedMachine(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)

	// The Orchestrator proposes on box.
	ctx := tool.WithProposingSession(context.Background(), session)
	raw, err := home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "propose", map[string]any{"name": "box-review", "project": "Project box", "steps": proposalSteps("Review on box")})
	if err != nil {
		t.Fatal(err)
	}
	answer := answerMap(raw)
	id := answer["automation"].(map[string]any)["id"].(string)
	request, _ := answer["decision_request"].(map[string]any)
	permissionID, _ := request["permission_id"].(string)
	if permissionID == "" || request["session_id"] != session {
		t.Fatalf("propose on box made no pending request: %v", answer)
	}
	if proposal, _ := request["proposal"].(map[string]any); proposal["machine"] != "box" || proposal["id"] != id {
		t.Fatalf("the request does not name box's proposal: %v", request)
	}
	if a, ok := box.workflow(t, id); !ok || !a.Proposed {
		t.Fatalf("the proposal is not on box: %+v", a)
	}
	if _, ok := home.workflow(t, id); ok {
		t.Fatal("the proposal landed on the home")
	}
	// The AI sees it, waiting on the person, over machine=.
	raw, err = home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := answerMap(raw)["pending_proposals"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["machine"] != "box" || pending[0].(map[string]any)["waiting_on"] != "person" {
		t.Fatalf("linked list pending_proposals: %v", answerMap(raw))
	}
	listed := home.list(t, "box")
	if len(listed) != 1 || listed[0]["id"] != id || listed[0]["machine"] != "box" {
		t.Fatalf("GET proposals?machine=box: %v", listed)
	}
	if len(home.list(t, "")) != 0 {
		t.Fatal("box's proposal listed as the home's")
	}

	// Stale over the link.
	if code, out := home.decide(t, ProposalDecision{Machine: "box", Kind: "workflow", ID: id, ExpectedRevision: 9, Decision: "accept"}, nil); code != http.StatusConflict {
		t.Fatalf("stale linked accept: %d %v", code, out)
	}
	// The person answers the chat's pending request: accepted on box.
	code, out := home.answer(t, session, permissionID, "allow_once", nil)
	if code != http.StatusOK {
		t.Fatalf("the person's answer: %d %v", code, out)
	}
	if a, ok := box.workflow(t, id); !ok || a.Proposed || a.Steps[0].Brief != "Review on box" {
		t.Fatalf("not accepted on box: %+v", a)
	}
	if record, _, _ := home.perms.GetPermission(session, permissionID); record.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("the request after the answer: %+v", record)
	}

	// A second proposal on box, discarded from the console's route: the
	// chat's request for it is answered too.
	raw, err = home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "propose", map[string]any{"name": "box-other", "project": "Project box", "steps": proposalSteps("Other")})
	if err != nil {
		t.Fatal(err)
	}
	answer = answerMap(raw)
	otherID := answer["automation"].(map[string]any)["id"].(string)
	otherPermission := answer["decision_request"].(map[string]any)["permission_id"].(string)
	code, out = home.decide(t, ProposalDecision{Machine: "box", Kind: "workflow", ID: otherID, ExpectedRevision: 1, Decision: "discard"}, nil)
	if code != http.StatusOK || out["requests_answered"] != float64(1) {
		t.Fatalf("linked discard: %d %v", code, out)
	}
	if _, ok := box.workflow(t, otherID); ok {
		t.Fatal("discard kept the proposal on box")
	}
	if record, _, _ := home.perms.GetPermission(session, otherPermission); record.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("the chat request after the console's discard: %+v", record)
	}
}

// Purpose: an AI never decides a proposal. Requirement: the decide route
// refuses every scoped token (an AI key at any level, an app key) with 403;
// the session's permission route refuses an AI key's (or resolve_permission's
// internal scoped call's) answer to a proposal request; resolve_task_permission's
// service refuses it; the permission service refuses Resolve, a lasting rule
// and resolve-all for it. After every refusal the proposal and the request
// are unchanged. Threat: self-approval by the proposing AI through any path.
// Boundary: handleProposals, answerProposalRequest, ResolveTaskPermission,
// permission.Service (resolveForDeviceLocked, ResolveWithPolicyAndArguments,
// ResolveAll).
func TestProposalsAIKeyCannotDecide(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	answer := home.propose(t, session, map[string]any{"name": "guarded", "project": "Project home", "steps": proposalSteps("Review")})
	id := answer["automation"].(map[string]any)["id"].(string)
	permissionID := answer["decision_request"].(map[string]any)["permission_id"].(string)
	unchanged := func(step string) {
		t.Helper()
		if a, ok := home.workflow(t, id); !ok || !a.Proposed || a.Revision != 1 {
			t.Fatalf("%s changed the proposal: %+v", step, a)
		}
		if record, _, _ := home.perms.GetPermission(session, permissionID); record.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("%s resolved the request: %+v", step, record)
		}
	}
	for _, level := range []string{"read", "write", "full"} {
		if code, _ := home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 1, Decision: "accept"}, scopesForLevel(t, level)); code != http.StatusForbidden {
			t.Fatalf("an AI key at %s decided: %d", level, code)
		}
	}
	if code, _ := home.decide(t, ProposalDecision{Kind: "workflow", ID: id, ExpectedRevision: 1, Decision: "accept"}, []string{"automations:write"}); code != http.StatusForbidden {
		t.Fatalf("an app key decided: %d", code)
	}
	unchanged("the decide route")
	// resolve_permission (MCP or the Orchestrator's internal call) reaches
	// the session route with a scoped token.
	for _, scopes := range [][]string{scopesForLevel(t, "full"), {"sessions:read", "sessions:write"}} {
		if code, out := home.answer(t, session, permissionID, "allow_once", scopes); code != http.StatusForbidden || !strings.Contains(fmt.Sprint(out["error"]), "only the person decides it") {
			t.Fatalf("a scoped answer: %d %v", code, out)
		}
	}
	unchanged("resolve_permission")
	if _, err := home.perms.Resolve(session, permissionID, permission.ActionAllowOnce, "ai"); !errors.Is(err, permission.ErrProposalDecisionPersonOnly) {
		t.Fatalf("Resolve: %v", err)
	}
	if _, _, err := home.perms.ResolveWithPolicy(session, permissionID, permission.ActionAllowAlways, "ai"); !errors.Is(err, permission.ErrProposalDecisionPersonOnly) {
		t.Fatalf("a lasting rule: %v", err)
	}
	if resolved, err := home.perms.ResolveAll(session, permission.ActionAllowOnce, "all", 100); err != nil || len(resolved) != 0 {
		t.Fatalf("resolve-all: %v %v", resolved, err)
	}
	unchanged("the permission service")
	// resolve_task_permission for a task whose session holds the request.
	if err := home.s.sessions.Store().PutProject(home.account, &pebblestore.ProjectRecord{ID: "tp", AccountID: home.account, Name: "Task project"}); err != nil {
		t.Fatal(err)
	}
	if err := home.s.sessions.Store().PutProjectTask(home.account, &pebblestore.ProjectTaskRecord{ID: "t1", ProjectID: "tp", AccountID: home.account, Title: "t", Agent: "swarm", Status: "running", SessionID: session, ActiveAttemptID: "a1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "a1", SessionID: session, Role: "swarm", Status: "running"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := home.s.ResolveTaskPermission(context.Background(), home.principal(), "", "tp", "t1", permissionID, "approve", ""); err == nil || !strings.Contains(err.Error(), "only the person decides it") {
		t.Fatalf("resolve_task_permission: %v", err)
	}
	unchanged("resolve_task_permission")
	// No catalog action decides a proposal.
	for _, name := range []string{tool.ManageWorkersToolName, "manage_workflows"} {
		catalog, _ := tool.CatalogToolNamed(name)
		for _, action := range catalog.Actions {
			if action.Action == "accept" || action.Action == "discard" || action.Action == "decide" {
				t.Fatalf("%s declares %s", name, action.Action)
			}
		}
	}
	if controlMCPRouteAllowed(http.MethodPost, ProposalsPath+"/decide") {
		t.Fatal("Swarm Control may reach the decide route")
	}
}

// Purpose: an Orchestrator's propose gives its conversation a pending request
// the person answers, with a readable summary of the change. Requirement: the
// request names the workflow, its machine and what accepting does (steps
// added, removed and changed against the current revision); a re-proposal
// refreshes the same request to the new revision; the person's allow accepts
// and deny discards through DecideProposal; an answer to a request whose
// proposal moved since it was shown is refused 409 with the reason and the
// request cancelled, the proposal unchanged. Threat: a decision on a revision
// the person did not see; requests piling up per revision. Boundary:
// withProposalRequest, requestProposalDecision, workflowChangeSummary,
// answerProposalRequest, permission.Service.RequestProposalDecision.
func TestProposalPendingRequestOnPropose(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	// An accepted workflow with two steps.
	base := home.propose(t, "", map[string]any{"name": "pipeline", "project": "Project home", "steps": proposalSteps("Review", "Check")})
	baseID := base["automation"].(map[string]any)["id"].(string)
	if code, out := home.decide(t, ProposalDecision{Kind: "workflow", ID: baseID, ExpectedRevision: 1, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("seed accept: %d %v", code, out)
	}
	// The Orchestrator proposes a whole new step set (replace): s1
	// changed, s2 removed, s3 added.
	change := home.propose(t, session, map[string]any{"id": baseID, "replace": true, "steps": []any{
		map[string]any{"id": "s1", "type": "agent", "worker": "reviewer", "brief": "Review harder"},
		map[string]any{"id": "s3", "type": "agent", "worker": "reviewer", "brief": "Summarise"},
	}})
	request := change["decision_request"].(map[string]any)
	permissionID, _ := request["permission_id"].(string)
	if permissionID == "" || request["status"] != pebblestore.PermissionStatusPending {
		t.Fatalf("no pending request: %v", change)
	}
	summary := fmt.Sprint(request["summary"])
	for _, want := range []string{"Change to workflow pipeline", "steps added: s3", "steps removed: s2", "steps changed: s1"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q lacks %q", summary, want)
		}
	}
	record, ok, _ := home.perms.GetPermission(session, permissionID)
	if !ok || !permission.IsProposalDecision(record) || record.RunID != "" {
		t.Fatalf("the request: %+v", record)
	}
	var payload proposalRequestPayload
	if err := json.Unmarshal([]byte(record.ToolArguments), &payload); err != nil || payload.Proposal.Machine != "" || payload.Proposal.Name != "pipeline" || payload.Decide.ExpectedRevision != 1 || !strings.Contains(payload.Title, "pipeline") {
		t.Fatalf("payload: %+v %v", payload, err)
	}
	changeID := payload.Decide.ID
	// A re-proposal refreshes the same request to revision 2.
	again := home.propose(t, session, map[string]any{"id": baseID, "replace": true, "steps": []any{map[string]any{"id": "s1", "type": "agent", "worker": "reviewer", "brief": "Review hardest"}}})
	if again["decision_request"].(map[string]any)["permission_id"] != permissionID {
		t.Fatalf("a re-proposal made a second request: %v", again["decision_request"])
	}
	record, _, _ = home.perms.GetPermission(session, permissionID)
	_ = json.Unmarshal([]byte(record.ToolArguments), &payload)
	if payload.Decide.ExpectedRevision != 2 || record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("the refreshed request: %+v", payload)
	}
	// The proposal moves behind the request (saved past the hook): the
	// person's answer is refused as stale and the request cancelled.
	moved, _ := home.workflow(t, changeID)
	moved.BaseID = baseID
	if _, err := home.workflows.Propose(home.account, session, moved); err != nil {
		t.Fatal(err)
	}
	code, out := home.answer(t, session, permissionID, "allow_once", nil)
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "no longer pending at revision 2") {
		t.Fatalf("stale answer: %d %v", code, out)
	}
	if record, _, _ := home.perms.GetPermission(session, permissionID); record.Status != pebblestore.PermissionStatusCancelled {
		t.Fatalf("the stale request: %+v", record)
	}
	if a, _ := home.workflow(t, changeID); !a.Proposed || a.Revision != 3 {
		t.Fatalf("a stale answer changed the proposal: %+v", a)
	}
	if active, _ := home.workflow(t, baseID); len(active.Steps) != 2 {
		t.Fatalf("a stale answer changed the workflow: %+v", active)
	}
	// Proposed again: a fresh request; deny discards it.
	fresh := home.propose(t, session, map[string]any{"id": baseID, "replace": true, "steps": []any{map[string]any{"id": "s1", "type": "agent", "worker": "reviewer", "brief": "Final"}}})
	permissionID = fresh["decision_request"].(map[string]any)["permission_id"].(string)
	if code, out := home.answer(t, session, permissionID, "deny_once", nil); code != http.StatusOK {
		t.Fatalf("deny: %d %v", code, out)
	}
	if _, ok := home.workflow(t, changeID); ok {
		t.Fatal("deny kept the proposal")
	}
	if record, _, _ := home.perms.GetPermission(session, permissionID); record.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("the denied request: %+v", record)
	}
	// A new workflow, accepted by the person's allow.
	created := home.propose(t, session, map[string]any{"name": "fresh", "project": "Project home", "steps": proposalSteps("Look")})
	newID := created["automation"].(map[string]any)["id"].(string)
	createdRequest := created["decision_request"].(map[string]any)
	if !strings.HasPrefix(fmt.Sprint(createdRequest["summary"]), "New workflow fresh with 1 step(s): s1 (agent reviewer)") {
		t.Fatalf("new summary: %v", createdRequest["summary"])
	}
	if code, out := home.answer(t, session, createdRequest["permission_id"].(string), "allow_once", nil); code != http.StatusOK {
		t.Fatalf("allow: %d %v", code, out)
	}
	if a, ok := home.workflow(t, newID); !ok || a.Proposed {
		t.Fatalf("allow did not accept: %+v", a)
	}
}

// Purpose: the proposals route and the AI's views never drift. Requirement:
// one ProposalView shape: the route's proposals, manage_workflows list's and
// get's pending_proposals (here and over machine=) carry exactly the same
// fields with the same values; the decision a pending request carries has
// exactly the decide route's body fields (ProposalDecision), so the person's
// answer can always be replayed on the route. Threat: a field added to the
// route but not to the MCP actions (or the reverse). Boundary: handleProposals,
// ManageWorkflows list/get, CallFleetWork list, proposalRequestPayload.
func TestProposalsRouteAndMCPActionsAgree(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	answer := home.propose(t, session, map[string]any{"name": "agree", "project": "Project home", "steps": proposalSteps("Review")})
	id := answer["automation"].(map[string]any)["id"].(string)
	ctx := tool.WithProposingSession(context.Background(), session)
	if _, err := home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "propose", map[string]any{"name": "agree-box", "project": "Project box", "steps": proposalSteps("Review")}); err != nil {
		t.Fatal(err)
	}
	normalize := func(v any) []map[string]any {
		raw, _ := json.Marshal(v)
		var out []map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	viewFields := func() []string {
		var fields []string
		kind := reflect.TypeOf(ProposalView{})
		for i := 0; i < kind.NumField(); i++ {
			fields = append(fields, strings.Split(kind.Field(i).Tag.Get("json"), ",")[0])
		}
		sort.Strings(fields)
		return fields
	}()
	keys := func(m map[string]any) []string {
		var out []string
		for key := range m {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	for _, machine := range []string{"", "box"} {
		route := home.list(t, machine)
		var mcpList, mcpGet []map[string]any
		if machine == "" {
			out, err := home.s.ManageWorkflows(context.Background(), home.principal(), session, map[string]any{"action": "list"})
			if err != nil {
				t.Fatal(err)
			}
			mcpList = normalize(answerMap(out)["pending_proposals"])
			out, err = home.s.ManageWorkflows(context.Background(), home.principal(), session, map[string]any{"action": "get", "id": id})
			if err != nil {
				t.Fatal(err)
			}
			mcpGet = normalize(answerMap(out)["pending_proposals"])
		} else {
			raw, err := home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "list", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			mcpList = normalize(answerMap(raw)["pending_proposals"])
			raw, err = home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "get", map[string]any{"name": "agree-box"})
			if err != nil {
				t.Fatal(err)
			}
			mcpGet = normalize(answerMap(raw)["pending_proposals"])
		}
		if len(route) != 1 || !reflect.DeepEqual(route, mcpList) || !reflect.DeepEqual(route, mcpGet) {
			t.Fatalf("machine %q: route %v, list %v, get %v", machine, route, mcpList, mcpGet)
		}
		if got := keys(route[0]); !reflect.DeepEqual(got, viewFields) {
			t.Fatalf("machine %q: proposal fields %v, ProposalView %v", machine, got, viewFields)
		}
	}
	// The pending request's decision is the decide route's body.
	permissionID := answer["decision_request"].(map[string]any)["permission_id"].(string)
	record, _, _ := home.perms.GetPermission(session, permissionID)
	var stored map[string]any
	if err := json.Unmarshal([]byte(record.ToolArguments), &stored); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ProposalDecision{})
	var routeBody map[string]any
	_ = json.Unmarshal(body, &routeBody)
	if got, want := keys(stored["decide"].(map[string]any)), keys(routeBody); !reflect.DeepEqual(got, want) {
		t.Fatalf("the request's decision %v, the route's body %v", got, want)
	}
	if got := keys(stored["proposal"].(map[string]any)); !reflect.DeepEqual(got, viewFields) {
		t.Fatalf("the request's proposal %v, ProposalView %v", got, viewFields)
	}
	// The route refuses a field it does not take (strict body).
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, ProposalsPath+"/decide", strings.NewReader(`{"kind":"workflow","id":"`+id+`","expected_revision":1,"decision":"accept","approved":true}`)), home.user, home.account)
	w := httptest.NewRecorder()
	home.s.handleProposals(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d %s", w.Code, w.Body)
	}
}

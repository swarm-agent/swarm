package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// linkedChangeProposal sets up the production scenario: an active workflow
// on box, and the home Orchestrator's conversation proposing a change of it
// on box (proposeTimes times, so the request names that revision). It
// returns the active workflow id, the proposal id and the request id.
func linkedChangeProposal(t *testing.T, home, box *proposalTestSwarm, session, name string, proposeTimes int) (string, string, string) {
	t.Helper()
	first := box.propose(t, "", map[string]any{"name": name, "project": "Project box", "steps": proposalSteps("v1")})
	baseID := first["automation"].(map[string]any)["id"].(string)
	if code, out := box.decide(t, ProposalDecision{Kind: "workflow", ID: baseID, ExpectedRevision: 1, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("base accept on box: %d %v", code, out)
	}
	ctx := tool.WithProposingSession(context.Background(), session)
	var proposalID, permissionID string
	for i := 1; i <= proposeTimes; i++ {
		raw, err := home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "propose", map[string]any{"id": baseID, "steps": []any{map[string]any{"id": "s1", "brief": fmt.Sprintf("v2 take %d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
		answer := answerMap(raw)
		proposalID = answer["automation"].(map[string]any)["id"].(string)
		request, _ := answer["decision_request"].(map[string]any)
		permissionID, _ = request["permission_id"].(string)
		if permissionID == "" {
			t.Fatalf("propose made no pending request: %v", answer)
		}
	}
	return baseID, proposalID, permissionID
}

// pendingRead is the home's GET /v3/sessions/{id}/permissions?status=pending.
func (p *proposalTestSwarm) pendingRead(t *testing.T, session string) []pebblestore.PermissionRecord {
	t.Helper()
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/sessions/"+session+"/permissions?status=pending", nil), p.user, p.account)
	w := httptest.NewRecorder()
	p.s.handleSessionV3PrimaryPermissions(w, r, p.principal(), session)
	if w.Code != http.StatusOK {
		t.Fatalf("pending read: %d %s", w.Code, w.Body)
	}
	var out struct {
		Permissions []pebblestore.PermissionRecord `json:"permissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Permissions
}

// Purpose: the production failure. The home Orchestrator proposed a change
// of a linked machine's workflow (revision 2 of the proposal, replacing the
// active workflow); the person clicked Accept and the change was applied,
// but they saw "Could not answer: ... no longer pending at revision 2 (not
// found)" and the request stayed pending. Requirement: answering a
// proposal_decision request decides the proposal exactly once and answers
// the request (approved, outcome accepted) with no error; a second answer,
// sequential or concurrent (a double click), never decides again: it returns
// the recorded outcome (200, already_answered), and box's workflow moved by
// exactly one revision. Threat: a second decision failing on the already
// merged proposal and surfacing a false error, or leaving the request
// pending. Boundary: handleSessionV3PrimaryPermissionResolve ->
// answerProposalRequest (proposalLock, decideProposalLocked) over two
// in-process daemons linked like production. Narrowest layer: the session
// resolve route the console calls, over the real link and stores.
func TestProposalAnswerLinkedChangeDecidesOnceAndSettles(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	baseID, proposalID, permissionID := linkedChangeProposal(t, home, box, session, "review", 2)
	if a, ok := box.workflow(t, proposalID); !ok || !a.Proposed || a.Revision != 2 {
		t.Fatalf("the proposal on box: %+v %v", a, ok)
	}
	base, _ := box.workflow(t, baseID)

	// Two answers at once (a double click): one decides, one reports.
	type result struct {
		code int
		out  map[string]any
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, out := home.answer(t, session, permissionID, "allow_once", nil)
			results[i] = result{code, out}
		}(i)
	}
	wg.Wait()
	decided, reported := 0, 0
	for _, res := range results {
		if res.code != http.StatusOK || res.out["ok"] != true || res.out["outcome"] != "accepted" {
			t.Fatalf("an answer: %d %v", res.code, res.out)
		}
		if res.out["already_answered"] == true {
			reported++
		} else if res.out["proposal"] != nil {
			decided++
		}
	}
	if decided != 1 || reported != 1 {
		t.Fatalf("answers: %d decided, %d reported (want 1 and 1): %v", decided, reported, results)
	}
	after, ok := box.workflow(t, baseID)
	if !ok || after.Proposed || after.Revision != base.Revision+1 || after.Steps[0].Brief != "v2 take 2" {
		t.Fatalf("box's workflow after the answer: %+v (was revision %d)", after, base.Revision)
	}
	if _, ok := box.workflow(t, proposalID); ok {
		t.Fatal("the proposal is still on box")
	}
	record, _, _ := home.perms.GetPermission(session, permissionID)
	if record.Status != pebblestore.PermissionStatusApproved || record.Reason != "accepted by the person" {
		t.Fatalf("the request after the answer: %+v", record)
	}
	// A later click returns the outcome; nothing is decided again.
	code, out := home.answer(t, session, permissionID, "allow_once", nil)
	if code != http.StatusOK || out["already_answered"] != true || out["outcome"] != "accepted" {
		t.Fatalf("a later answer: %d %v", code, out)
	}
	if again, _ := box.workflow(t, baseID); again.Revision != after.Revision {
		t.Fatalf("a later answer moved box's workflow: %d -> %d", after.Revision, again.Revision)
	}
	if pending := home.pendingRead(t, session); len(pending) != 0 {
		t.Fatalf("still pending on the home: %+v", pending)
	}
}

// Purpose: a proposal decided outside the home's request (on the linked
// machine's own app or routes) must not leave the home's request pending or
// answer the person with an error. Requirement: the next read of the home's
// pending requests settles it with what became of the proposal (accepted:
// approved, "already accepted: now revision N"), and answering a request
// whose proposal was already discarded elsewhere is a 200 that settles it
// (denied, outcome discarded), not a "Could not answer" 409. A leftover
// request from an older build (no base revision in its payload) is settled
// too (cancelled, "already decided"). An AI key still cannot answer a
// settled request (403, unchanged). Threat: a request the person can never
// clear. Boundary: handleSessionV3PrimaryPermissions and answerProposalRequest
// -> settleProposalRequestsFromState (proposalOutcomeFor over the link).
func TestProposalRequestDecidedElsewhereSettlesWithItsOutcome(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)

	// Accepted on box itself: the home's request clears on its next read.
	baseID, proposalID, permissionID := linkedChangeProposal(t, home, box, session, "review", 2)
	if code, out := box.decide(t, ProposalDecision{Kind: "workflow", ID: proposalID, ExpectedRevision: 2, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("accept on box: %d %v", code, out)
	}
	accepted, _ := box.workflow(t, baseID)
	if record, _, _ := home.perms.GetPermission(session, permissionID); record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("precondition: box's own accept cannot reach the home's store: %+v", record)
	}
	if pending := home.pendingRead(t, session); len(pending) != 0 {
		t.Fatalf("the read still lists it: %+v", pending)
	}
	record, _, _ := home.perms.GetPermission(session, permissionID)
	if want := fmt.Sprintf("already accepted: now revision %d", accepted.Revision); record.Status != pebblestore.PermissionStatusApproved || record.Reason != want {
		t.Fatalf("the settled request: %s %q, want approved %q", record.Status, record.Reason, want)
	}
	// An AI key cannot answer it, settled or not.
	if code, _ := home.answer(t, session, permissionID, "allow_once", []string{"swarm:manage"}); code != http.StatusForbidden {
		t.Fatalf("an AI key's answer: %d", code)
	}

	// Discarded on box: the person's later click settles it, no error.
	_, otherID, otherPermission := linkedChangeProposal(t, home, box, session, "other", 1)
	if code, out := box.decide(t, ProposalDecision{Kind: "workflow", ID: otherID, ExpectedRevision: 1, Decision: "discard"}, nil); code != http.StatusOK {
		t.Fatalf("discard on box: %d %v", code, out)
	}
	code, out := home.answer(t, session, otherPermission, "allow_once", nil)
	if code != http.StatusOK || out["ok"] != true || out["already_decided"] != true || out["outcome"] != "discarded" || !strings.HasPrefix(fmt.Sprint(out["outcome_text"]), "already discarded") {
		t.Fatalf("answering a proposal discarded elsewhere: %d %v", code, out)
	}
	if record, _, _ := home.perms.GetPermission(session, otherPermission); record.Status != pebblestore.PermissionStatusDenied {
		t.Fatalf("the request after the answer: %+v", record)
	}

	// A request from an older build (no base revision), decided on box.
	oldBase, oldID, oldPermission := linkedChangeProposal(t, home, box, session, "older", 1)
	record, _, _ = home.perms.GetPermission(session, oldPermission)
	var payload map[string]any
	_ = json.Unmarshal([]byte(record.ToolArguments), &payload)
	delete(payload, "changes")
	raw, _ := json.Marshal(payload)
	if _, err := home.perms.RequestProposalDecision(session, record.CallID, record.ToolName, string(raw)); err != nil {
		t.Fatal(err)
	}
	if code, out := box.decide(t, ProposalDecision{Kind: "workflow", ID: oldID, ExpectedRevision: 1, Decision: "accept"}, nil); code != http.StatusOK {
		t.Fatalf("accept on box: %d %v", code, out)
	}
	if pending := home.pendingRead(t, session); len(pending) != 0 {
		t.Fatalf("the older request still lists: %+v", pending)
	}
	oldNow, _ := box.workflow(t, oldBase)
	record, _, _ = home.perms.GetPermission(session, oldPermission)
	if want := fmt.Sprintf("already decided: the proposal is no longer pending; the workflow it changes is now revision %d", oldNow.Revision); record.Status != pebblestore.PermissionStatusCancelled || record.Reason != want {
		t.Fatalf("the older request: %s %q, want cancelled %q", record.Status, record.Reason, want)
	}
}

// Purpose: a genuinely stale answer. Requirement: when the request names
// revision 1 but revision 2 of the proposal waits (re-proposed without this
// conversation), the answer decides nothing: 409 with the reason, the old
// revision's request cancelled and the conversation's request now naming
// revision 2, which the person's next answer accepts. Threat: accepting a
// definition the person never saw, or a stale request that can never be
// cleared. Boundary: answerProposalRequest -> settleProposalRequestsFromState
// (stale outcome, RequestProposalDecision for the new revision).
func TestProposalStaleRevisionCancelledAndNewRevisionRequested(t *testing.T) {
	home := newProposalTestSwarm(t, "home")
	box := newProposalTestSwarm(t, "box")
	linkProposalSwarms(t, home, box)
	session := "home-orch"
	fleetActivityTestSession(t, home.fleetTestSwarm, session)
	ctx := tool.WithProposingSession(context.Background(), session)
	raw, err := home.s.CallFleetWork(ctx, home.principal(), "box", "manage_workflows", "propose", map[string]any{"name": "fresh", "project": "Project box", "steps": proposalSteps("first")})
	if err != nil {
		t.Fatal(err)
	}
	answer := answerMap(raw)
	id := answer["automation"].(map[string]any)["id"].(string)
	permissionID := answer["decision_request"].(map[string]any)["permission_id"].(string)
	// Re-proposed on box without this conversation: revision 2.
	again := box.propose(t, "", map[string]any{"name": "fresh", "project": "Project box", "steps": proposalSteps("second")})
	if again["automation"].(map[string]any)["revision"] != float64(2) {
		t.Fatalf("re-proposal: %v", again)
	}
	code, out := home.answer(t, session, permissionID, "allow_once", nil)
	if code != http.StatusConflict || out["outcome"] != "stale" || !strings.Contains(fmt.Sprint(out["error"]), "revision 2") {
		t.Fatalf("a stale answer: %d %v", code, out)
	}
	if a, _ := box.workflow(t, id); !a.Proposed || a.Revision != 2 {
		t.Fatalf("a stale answer changed the proposal: %+v", a)
	}
	record, _, _ := home.perms.GetPermission(session, permissionID)
	payload, ok := proposalRequestPayloadOf(record)
	if record.Status != pebblestore.PermissionStatusPending || !ok || payload.Decide.ExpectedRevision != 2 {
		t.Fatalf("the request for the new revision: %+v", record)
	}
	code, out = home.answer(t, session, permissionID, "allow_once", nil)
	if code != http.StatusOK || out["outcome"] != "accepted" {
		t.Fatalf("answering revision 2: %d %v", code, out)
	}
	if a, _ := box.workflow(t, id); a.Proposed || a.Steps[0].Brief != "second" {
		t.Fatalf("revision 2 not accepted: %+v", a)
	}
}

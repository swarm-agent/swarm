package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Proposals: the person's one place to see and decide the workflow and
// worker proposals an AI made, on this machine or on a linked one.
//
//	GET  /v3/proposals[?machine=name]   pending proposals (automations:read)
//	POST /v3/proposals/decide            accept or discard one (the person only)
//
// ListProposals and DecideProposal are the one service behind the route, the
// proposing conversation's pending request (answerProposalRequest) and the
// visibility manage_workflows and manage_workers give an AI. Neither adds
// rules of its own: each runs the machine's existing routes, here in process
// as the person and on a linked machine over the link as its home, so the
// accept routes (handleWorkflowAccept, handleWorkerAccept) keep the revision
// checks and the run-step owner rule. A decision is only ever the person's:
// an owner session (their Swarm app) or their answer to the pending request,
// never an AI key or an agent's tool.

// ProposalsPath is the proposals route.
const ProposalsPath = "/v3/proposals"

const (
	proposalKindWorkflow = "workflow"
	proposalKindWorker   = "worker"
	proposalAccept       = "accept"
	proposalDiscard      = "discard"
	// proposalWaitingOn is who a pending proposal waits on.
	proposalWaitingOn = "person"
)

// ProposalView is one pending proposal: the one shape the proposals route,
// manage_workflows list/get and manage_workers list/review answer. machine is
// "" for this machine.
type ProposalView struct {
	Kind      string `json:"kind"`
	Machine   string `json:"machine"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Revision  uint64 `json:"revision"`
	CreatedAt int64  `json:"created_at"`
	WaitingOn string `json:"waiting_on"`
	// Change is "new" (accepting adds it) or "change" (accepting replaces
	// Replaces, the workflow or worker it changes).
	Change   string `json:"change"`
	Replaces string `json:"replaces"`
}

// ProposalDecision is the body of POST /v3/proposals/decide, and the
// decision a pending request carries.
type ProposalDecision struct {
	Machine          string `json:"machine"`
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Decision         string `json:"decision"`
}

// proposalStaleError is a decision refused because the proposal moved: a
// newer revision, already decided, or gone.
type proposalStaleError struct{ reason string }

func (e proposalStaleError) Error() string { return e.reason }

// proposalTransport runs one call to a machine's own routes: in process as
// the person here, or over the link as the machine's home.
type proposalTransport func(ctx context.Context, method, path string, body, out any) error

func (s *Server) proposalTransportFor(p identity.Principal, machine string) (proposalTransport, error) {
	if machine == "" {
		return s.localProposalCall(p), nil
	}
	if s.fleet == nil || s.fleetTokens == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	return func(ctx context.Context, method, path string, body, out any) error {
		err := client.JSON(ctx, method, path, body, out)
		s.noteFleetContact(linked, err)
		return err
	}, nil
}

// proposalReadTransportFor is the transport proposal reads use: here, the
// machine's own routes with the caller's scoped token (proposalReadToken),
// never an unscoped internal request; on a linked machine, the link, once
// the caller's token may read automations.
func (s *Server) proposalReadTransportFor(ctx context.Context, p identity.Principal, machine string) (proposalTransport, error) {
	token := proposalReadToken(ctx, p)
	if machine == "" {
		return s.localProposalCallAs(p, token), nil
	}
	if !token.HasScope("automations:read") && !token.HasScope("automations:write") {
		return nil, linkedTaskFail(http.StatusForbidden, "reading proposals needs automations:read")
	}
	return s.proposalTransportFor(p, machine)
}

// proposalReadToken is the scoped token a proposal read carries: the
// caller's own (a key's request, or a Swarm Control call's held scopes), or
// for a caller with none (the Orchestrator, the person's app) a read-only
// one that may only read automations.
func proposalReadToken(ctx context.Context, p identity.Principal) *pebblestore.ScopedTokenRecord {
	if ctx != nil {
		if rec, ok := ctx.Value(productScopedTokenRequestContextKey).(*pebblestore.ScopedTokenRecord); ok && rec != nil {
			return rec
		}
	}
	scopes := []string{"automations:read"}
	name := "Proposal read (read-only)"
	if held, ok := tool.ControlScopesFromContext(ctx); ok {
		scopes, name = held, "Proposal read (the caller's key scopes)"
	}
	return &pebblestore.ScopedTokenRecord{ID: "proposal-read", Name: name, Scopes: scopes, AccountScopeID: p.AccountScopeID, UserID: p.UserID}
}

// localProposalCall runs this machine's own route in process as the person
// (a decision, already checked to be theirs): the route's own checks decide.
func (s *Server) localProposalCall(p identity.Principal) proposalTransport {
	return s.localProposalCallAs(p, nil)
}

// localProposalCallAs runs this machine's own route in process with the
// person's principal and, when token is set, that scoped token.
func (s *Server) localProposalCallAs(p identity.Principal, token *pebblestore.ScopedTokenRecord) proposalTransport {
	return func(ctx context.Context, method, path string, body, out any) error {
		var reader io.Reader = http.NoBody
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(identity.ContextWithPrincipal(ctx, p), method, path, reader)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != nil {
			req = requestWithScopedToken(req, token)
		}
		recorder := &proposalRecorder{header: http.Header{}, status: http.StatusOK}
		s.internalRouteHandler().ServeHTTP(recorder, req)
		if recorder.status < 200 || recorder.status > 299 {
			var refusal struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(recorder.body.Bytes(), &refusal)
			return &fleet.Error{Status: recorder.status, Message: refusal.Error}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(recorder.body.Bytes(), out)
	}
}

type proposalRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *proposalRecorder) Header() http.Header         { return r.header }
func (r *proposalRecorder) WriteHeader(status int)      { r.status = status }
func (r *proposalRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }

// proposalSafeID is a record id that can name a route segment.
func proposalSafeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#% ") && len(id) <= 256
}

// ListProposals lists the pending workflow and worker proposals on machine
// ("" for this one), newest first.
func (s *Server) ListProposals(ctx context.Context, p identity.Principal, machine string) ([]ProposalView, error) {
	return s.listProposals(ctx, p, machine, "")
}

// listProposals is ListProposals of one kind ("" for both).
func (s *Server) listProposals(ctx context.Context, p identity.Principal, machine, kind string) ([]ProposalView, error) {
	if s == nil || !p.Valid() {
		return nil, identity.ErrPrincipalRequired
	}
	machine = strings.TrimSpace(machine)
	call, err := s.proposalReadTransportFor(ctx, p, machine)
	if err != nil {
		return nil, err
	}
	out := []ProposalView{}
	if kind != proposalKindWorker {
		var workflows struct {
			Automations []map[string]any `json:"automations"`
		}
		if err := call(ctx, http.MethodGet, JobAutomationsPath, nil, &workflows); err != nil {
			return nil, proposalCallError(machine, "the workflow list", err)
		}
		for _, a := range workflows.Automations {
			if view, ok := workflowProposalView(machine, a); ok {
				out = append(out, view)
			}
		}
	}
	cursor := ""
	for page := 0; page < 20 && kind != proposalKindWorkflow; page++ {
		var workers struct {
			Workers []map[string]any `json:"workers"`
			Next    string           `json:"next_cursor"`
		}
		path := WorkersPath + "?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		if err := call(ctx, http.MethodGet, path, nil, &workers); err != nil {
			return nil, proposalCallError(machine, "the worker list", err)
		}
		for _, w := range workers.Workers {
			if view, ok := workerProposalView(machine, w); ok {
				out = append(out, view)
			}
		}
		if workers.Next == "" || workers.Next == cursor {
			break
		}
		cursor = workers.Next
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// workflowProposalView is a workflow list entry's proposal view, when it is
// a pending proposal.
func workflowProposalView(machine string, a map[string]any) (ProposalView, bool) {
	if state, _ := a["state"].(string); state != "proposed" {
		return ProposalView{}, false
	}
	view := ProposalView{Kind: proposalKindWorkflow, Machine: machine, ID: mapText(a, "id"), Name: mapText(a, "name"), Revision: mapUint(a, "revision"), CreatedAt: int64(mapUint(a, "created_at")), WaitingOn: proposalWaitingOn, Change: "new"}
	if replaces, ok := a["replaces"].(map[string]any); ok {
		view.Change, view.Replaces = "change", mapText(replaces, "id")
	}
	return view, view.ID != ""
}

// workerProposalView is a worker record's proposal view: a new worker that
// waits for acceptance, or a change of a worker that does.
func workerProposalView(machine string, w map[string]any) (ProposalView, bool) {
	view := ProposalView{Kind: proposalKindWorker, Machine: machine, ID: mapText(w, "id"), Name: mapText(w, "name"), Revision: mapUint(w, "revision"), CreatedAt: int64(mapUint(w, "created_at")), WaitingOn: proposalWaitingOn}
	switch {
	case mapText(w, "lifecycle_state") == string(pebblestore.WorkerLifecycleStatePending):
		view.Change = "new"
	case w["pending_review"] != nil && w["pending_review_stale"] != true:
		view.Change, view.Replaces = "change", view.ID
		if review, ok := w["pending_review"].(map[string]any); ok && mapUint(review, "updated_at") > 0 {
			view.CreatedAt = int64(mapUint(review, "updated_at"))
		}
	default:
		return ProposalView{}, false
	}
	return view, view.ID != ""
}

func mapText(m map[string]any, key string) string {
	text, _ := m[key].(string)
	return strings.TrimSpace(text)
}

func mapUint(m map[string]any, key string) uint64 {
	switch n := m[key].(type) {
	case float64:
		if n > 0 {
			return uint64(n)
		}
	case json.Number:
		v, _ := n.Int64()
		if v > 0 {
			return uint64(v)
		}
	case uint64:
		return n
	case int64:
		if n > 0 {
			return uint64(n)
		}
	case int:
		if n > 0 {
			return uint64(n)
		}
	}
	return 0
}

// proposalCallError is a failed call in the proposals service.
func proposalCallError(machine, what string, err error) error {
	var refused *fleet.Error
	if errors.As(err, &refused) {
		where := "this machine"
		if machine != "" {
			where = machine
		}
		return linkedTaskFail(refused.Status, "%s refused %s: %s", where, what, refused.Error())
	}
	if machine != "" {
		return linkedTaskFail(http.StatusBadGateway, "linked machine %s could not be reached for %s: %v", machine, what, err)
	}
	return err
}

// DecideProposal applies the person's decision on one proposal: accept runs
// the machine's accept route at the expected revision; discard removes the
// proposal only while it is still that pending revision. A moved proposal is
// refused with a proposalStaleError. Then every pending request for it in the
// person's conversations is answered with the decision.
func (s *Server) DecideProposal(ctx context.Context, p identity.Principal, in ProposalDecision) (map[string]any, error) {
	if s == nil || !p.Valid() {
		return nil, identity.ErrPrincipalRequired
	}
	in.Machine, in.Kind, in.ID, in.Decision = strings.TrimSpace(in.Machine), strings.TrimSpace(in.Kind), strings.TrimSpace(in.ID), strings.TrimSpace(in.Decision)
	if in.Kind != proposalKindWorkflow && in.Kind != proposalKindWorker {
		return nil, linkedTaskFail(http.StatusBadRequest, "kind must be workflow or worker")
	}
	if in.Decision != proposalAccept && in.Decision != proposalDiscard {
		return nil, linkedTaskFail(http.StatusBadRequest, "decision must be accept or discard")
	}
	if !proposalSafeID(in.ID) {
		return nil, linkedTaskFail(http.StatusBadRequest, "a valid proposal id is required")
	}
	if in.ExpectedRevision == 0 {
		return nil, linkedTaskFail(http.StatusBadRequest, "expected_revision is required (the revision the person saw)")
	}
	call, err := s.proposalTransportFor(p, in.Machine)
	if err != nil {
		return nil, err
	}
	var applied any
	if in.Kind == proposalKindWorkflow {
		applied, err = decideWorkflowProposal(ctx, call, in)
	} else {
		applied, err = decideWorkerProposal(ctx, call, in)
	}
	if err != nil {
		return nil, err
	}
	answer := map[string]any{"ok": true, "kind": in.Kind, "machine": in.Machine, "id": in.ID, "revision": in.ExpectedRevision, "decision": in.Decision, "result": applied}
	if in.Machine != "" {
		answer["untrusted_content"] = true
	}
	hoistProposalTriggerKey(answer, applied)
	// The machine's accept and delete routes answer their own machine's
	// requests (settleProposalRequestsAfter); a local decision counts those.
	var routeSettled uint64
	var routeErr string
	if result, ok := applied.(map[string]any); ok {
		routeSettled, routeErr = mapUint(result, "requests_answered"), mapText(result, "requests_error")
		delete(result, "requests_answered")
		delete(result, "requests_error")
	}
	settled, settleErr := s.settleProposalRequests(p, in, "")
	if in.Machine == "" {
		settled += int(routeSettled)
		if routeErr != "" {
			settleErr = errors.Join(settleErr, errors.New(routeErr))
		}
	}
	answer["requests_answered"] = settled
	if settleErr != nil {
		answer["requests_error"] = settleErr.Error()
	}
	return answer, nil
}

// hoistProposalTriggerKey moves a webhook workflow's trigger key from the
// accept route's result to the top of the person's decision answer, once,
// with a note to copy it now. The key is a one-time secret for the person
// only: it never enters a pending request, a conversation or an AI's tool
// result (the pending request only records the decision and its reason).
func hoistProposalTriggerKey(answer map[string]any, applied any) {
	result, ok := applied.(map[string]any)
	if !ok {
		return
	}
	key, _ := result["trigger_key"].(string)
	delete(result, "trigger_key")
	if strings.TrimSpace(key) == "" {
		return
	}
	answer["trigger_key"] = key
	answer["trigger_key_note"] = "Shown once, to you only: copy it now. Swarm does not show it again; rotate the key to get a new one."
}

// proposalStatusStale reports whether a machine's refusal means the
// proposal moved (a conflict, or it is gone).
func proposalStatusStale(err error) bool {
	var refused *fleet.Error
	return errors.As(err, &refused) && (refused.Status == http.StatusConflict || refused.Status == http.StatusNotFound || refused.Status == http.StatusPreconditionFailed)
}

func proposalStale(in ProposalDecision, detail string) error {
	return proposalStaleError{reason: fmt.Sprintf("the %s proposal %s is no longer pending at revision %d (%s): it changed, was already decided or was removed; read it again (GET %s) and decide the current revision", in.Kind, in.ID, in.ExpectedRevision, detail, ProposalsPath)}
}

func decideWorkflowProposal(ctx context.Context, call proposalTransport, in ProposalDecision) (any, error) {
	base := JobAutomationsPath + "/" + url.PathEscape(in.ID)
	var current struct {
		Automation map[string]any `json:"automation"`
	}
	if err := call(ctx, http.MethodGet, base, nil, &current); err != nil {
		if proposalStatusStale(err) {
			return nil, proposalStale(in, "not found")
		}
		return nil, proposalCallError(in.Machine, "the workflow read", err)
	}
	if state := mapText(current.Automation, "state"); state != "proposed" || mapUint(current.Automation, "revision") != in.ExpectedRevision {
		return nil, proposalStale(in, fmt.Sprintf("now %s at revision %d", firstNonEmptyString(state, "unknown"), mapUint(current.Automation, "revision")))
	}
	var out map[string]any
	var err error
	if in.Decision == proposalAccept {
		err = call(ctx, http.MethodPost, base+"/accept", map[string]any{"expected_revision": in.ExpectedRevision}, &out)
	} else {
		// The delete is guarded by the revision the person saw, so a
		// re-proposed or already-accepted revision is refused (409).
		err = call(ctx, http.MethodDelete, fmt.Sprintf("%s?expected_revision=%d", base, in.ExpectedRevision), nil, &out)
	}
	if err != nil {
		if proposalStatusStale(err) {
			return nil, proposalStale(in, err.Error())
		}
		return nil, proposalCallError(in.Machine, "the "+in.Decision, err)
	}
	return out, nil
}

func decideWorkerProposal(ctx context.Context, call proposalTransport, in ProposalDecision) (any, error) {
	base := WorkersPath + "/" + url.PathEscape(in.ID)
	var current struct {
		Worker map[string]any `json:"worker"`
	}
	if err := call(ctx, http.MethodGet, base, nil, &current); err != nil {
		if proposalStatusStale(err) {
			return nil, proposalStale(in, "not found")
		}
		return nil, proposalCallError(in.Machine, "the worker read", err)
	}
	view, pending := workerProposalView(in.Machine, current.Worker)
	if !pending || view.Revision != in.ExpectedRevision {
		return nil, proposalStale(in, fmt.Sprintf("now %s at revision %d", firstNonEmptyString(mapText(current.Worker, "lifecycle_state"), "unknown"), mapUint(current.Worker, "revision")))
	}
	var out map[string]any
	var err error
	if in.Decision == proposalAccept {
		err = call(ctx, http.MethodPost, base+"/accept", map[string]any{"expected_revision": in.ExpectedRevision}, &out)
	} else {
		if view.Change != "new" {
			// The worker routes have no discard for a change of an active
			// worker; deleting would remove the worker itself.
			return nil, linkedTaskFail(http.StatusConflict, "worker %s is active and this proposal changes it: discarding the change alone is not available (deleting would remove the worker). Accept it, or ask the AI to propose again", in.ID)
		}
		err = call(ctx, http.MethodDelete, fmt.Sprintf("%s?expected_revision=%d", base, in.ExpectedRevision), nil, &out)
	}
	if err != nil {
		if proposalStatusStale(err) {
			return nil, proposalStale(in, err.Error())
		}
		return nil, proposalCallError(in.Machine, "the "+in.Decision, err)
	}
	return out, nil
}

// proposalRequestKey names a proposal's pending request (its call id).
func proposalRequestKey(kind, machine, id string) string {
	where := machine
	if where == "" {
		where = "here"
	}
	return "proposal:" + kind + ":" + where + ":" + id
}

// settleProposalRequests answers every pending request for the decided
// proposal in the person's conversations, so a decision in the app also
// clears the chat. skip names a request the caller answers itself.
func (s *Server) settleProposalRequests(p identity.Principal, in ProposalDecision, skip string) (int, error) {
	action, reason := permission.ActionAllowOnce, "accepted by the person"
	if in.Decision == proposalDiscard {
		action, reason = permission.ActionDenyOnce, "discarded by the person"
	}
	return s.settleProposalRequestsAs(p, proposalRequestKey(in.Kind, in.Machine, in.ID), action, reason, skip)
}

// settleProposalRequestsAfter answers this machine's pending requests for a
// local proposal the accept or delete route just decided, whoever called it
// (the proposals route, the console's or SDK's direct call, manage_workflows
// delete), so no decision path leaves the chat waiting. It adds the count to
// the route's answer (requests_answered, requests_error).
func (s *Server) settleProposalRequestsAfter(p identity.Principal, kind, id, decision string, answer map[string]any) {
	action, reason := permission.ActionAllowOnce, "accepted"
	if decision == proposalDiscard {
		action, reason = permission.ActionDenyOnce, "discarded: the proposal was deleted"
	}
	settled, err := s.settleProposalRequestsAs(p, proposalRequestKey(kind, "", id), action, reason, "")
	if answer == nil {
		return
	}
	answer["requests_answered"] = settled
	if err != nil {
		answer["requests_error"] = err.Error()
	}
}

func (s *Server) settleProposalRequestsAs(p identity.Principal, key, action, reason, skip string) (int, error) {
	if s.perm == nil || !p.Valid() {
		return 0, nil
	}
	summaries, err := s.perm.ListPendingSummaries(p.AccountScopeID, p.UserID, 500)
	if err != nil {
		return 0, err
	}
	answered := 0
	var firstErr error
	for _, summary := range summaries {
		pending, err := s.perm.ListPending(summary.SessionID, 1000)
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		for _, record := range pending {
			if !permission.IsProposalDecision(record) || record.CallID != key || record.ID == skip {
				continue
			}
			if _, err := s.perm.ResolveProposalDecision(record.SessionID, record.ID, action, reason); err != nil {
				firstErr = errors.Join(firstErr, err)
				continue
			}
			answered++
		}
	}
	return answered, firstErr
}

// proposalPersonRequest reports whether r is the person: a user principal on
// an owner session, never a scoped token (an AI key, an app key, an agent's
// internal tool call).
func proposalPersonRequest(r *http.Request) (identity.Principal, bool) {
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() || (p.Type != identity.PrincipalTypeUser && p.Type != "user") {
		return identity.Principal{}, false
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped {
		return identity.Principal{}, false
	}
	return p, true
}

func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, ProposalsPath), "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		if !s.requireScopeAny(w, r, "automations:read", "automations:write") {
			return
		}
		machine := strings.TrimSpace(r.URL.Query().Get("machine"))
		list, err := s.ListProposals(r.Context(), principal, machine)
		if err != nil {
			writeError(w, linkedTaskStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "machine": machine, "proposals": list, "count": len(list)})
	case rest == "decide" && r.Method == http.MethodPost:
		person, ok := proposalPersonRequest(r)
		if !ok {
			writeError(w, http.StatusForbidden, errors.New("only the person decides a proposal, in their Swarm app; an AI key, app key or agent cannot accept or discard one"))
			return
		}
		var in ProposalDecision
		if err := decodeJSONStrict(w, r, 4096, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		out, err := s.DecideProposal(r.Context(), person, in)
		if err != nil {
			var stale proposalStaleError
			if errors.As(err, &stale) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, linkedTaskStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown proposals route"))
	}
}

// proposalRequestPayload is the pending request the person answers: the
// proposal, a readable summary of the change, and its decision.
type proposalRequestPayload struct {
	PathID     string           `json:"path_id"`
	ReviewKind string           `json:"review_kind"`
	Title      string           `json:"title"`
	Proposal   ProposalView     `json:"proposal"`
	Summary    string           `json:"summary"`
	Changes    map[string]any   `json:"changes,omitempty"`
	Decide     ProposalDecision `json:"decide"`
	Route      string           `json:"route"`
	Answers    string           `json:"answers"`
}

const proposalRequestAnswers = "Accept (allow) applies the proposal; Discard (deny) removes it. Only the person answers; an AI cannot."

// requestProposalDecision gives the proposing conversation sessionID a
// pending request for the proposal a propose answer names (answer is that
// action's answer; kind workflow or worker; machine "" for here). It returns
// what the propose answer tells the AI.
func (s *Server) requestProposalDecision(ctx context.Context, p identity.Principal, sessionID, machine, kind string, answer map[string]any) map[string]any {
	if s.perm == nil || s.sessions == nil {
		return map[string]any{"error": "the permission service is not configured; the person decides in their Swarm app"}
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
		return map[string]any{"error": "no conversation of this person to ask in; the person decides in their Swarm app"}
	}
	payload, err := s.proposalRequestFor(ctx, p, machine, kind, answer)
	if err != nil {
		return map[string]any{"error": "the pending request could not be made: " + err.Error() + "; the person decides in their Swarm app"}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	record, err := s.perm.RequestProposalDecision(sessionID, proposalRequestKey(kind, machine, payload.Proposal.ID), "manage_"+kind+"s", string(raw))
	if err != nil {
		return map[string]any{"error": "the pending request could not be made: " + err.Error() + "; the person decides in their Swarm app"}
	}
	return map[string]any{"permission_id": record.ID, "session_id": sessionID, "status": record.Status, "proposal": payload.Proposal, "summary": payload.Summary}
}

// proposalRequestFor builds the request for the proposal in a propose answer.
func (s *Server) proposalRequestFor(ctx context.Context, p identity.Principal, machine, kind string, answer map[string]any) (proposalRequestPayload, error) {
	// As JSON reads it: a local answer holds typed values, a linked one
	// JSON; the summary compares either with the route's JSON.
	answer = answerData(answerMap(mustJSONRoundTrip(answer)))
	if answer == nil {
		return proposalRequestPayload{}, errors.New("the propose answer is unreadable")
	}
	call, err := s.proposalReadTransportFor(ctx, p, machine)
	if err != nil {
		return proposalRequestPayload{}, err
	}
	var view ProposalView
	var summary string
	var changes map[string]any
	if kind == proposalKindWorkflow {
		proposal, _ := answer["automation"].(map[string]any)
		var ok bool
		if view, ok = workflowProposalView(machine, proposal); !ok {
			return proposalRequestPayload{}, errors.New("the propose answer names no pending workflow")
		}
		var base map[string]any
		if view.Replaces != "" && proposalSafeID(view.Replaces) {
			var current struct {
				Automation map[string]any `json:"automation"`
			}
			if err := call(ctx, http.MethodGet, JobAutomationsPath+"/"+url.PathEscape(view.Replaces), nil, &current); err != nil {
				return proposalRequestPayload{}, proposalCallError(machine, "the current workflow read", err)
			}
			base = current.Automation
		}
		summary, changes = workflowChangeSummary(base, proposal)
	} else {
		worker, _ := answer["worker"].(map[string]any)
		var ok bool
		if view, ok = workerProposalView(machine, worker); !ok {
			return proposalRequestPayload{}, errors.New("the propose answer names no worker waiting for acceptance")
		}
		summary, changes = workerChangeSummary(worker)
	}
	where := "this machine"
	if machine != "" {
		where = "machine " + machine
	}
	title := fmt.Sprintf("Accept %s %q on %s?", kind, view.Name, where)
	if view.Change == "change" {
		title = fmt.Sprintf("Accept the change to %s %q on %s?", kind, view.Name, where)
	}
	return proposalRequestPayload{
		PathID: "permission.proposal-decision.v1", ReviewKind: kind, Title: title, Proposal: view,
		Summary: summary, Changes: changes, Route: ProposalsPath + "/decide", Answers: proposalRequestAnswers,
		Decide: ProposalDecision{Machine: machine, Kind: kind, ID: view.ID, ExpectedRevision: view.Revision},
	}, nil
}

// workflowStepLabel is a step in words: its id and what it does.
func workflowStepLabel(step map[string]any) string {
	label := mapText(step, "id")
	kind := mapText(step, "type")
	switch kind {
	case "agent":
		kind = "agent " + mapText(step, "worker")
	case "run":
		kind = "run " + mapText(step, "command")
	case "deliver":
		kind = "deliver to " + mapText(step, "to")
	}
	if strings.TrimSpace(kind) != "" {
		label += " (" + strings.TrimSpace(kind) + ")"
	}
	return label
}

// workflowChangeSummary says what accepting a workflow proposal does against
// its current revision (base nil: a new workflow): steps added, removed and
// changed, and the other fields it changes.
func workflowChangeSummary(base, proposal map[string]any) (string, map[string]any) {
	steps := func(view map[string]any) ([]string, map[string]map[string]any) {
		var order []string
		byID := map[string]map[string]any{}
		list, _ := view["steps"].([]any)
		for i, raw := range list {
			step, _ := raw.(map[string]any)
			id := mapText(step, "id")
			if id == "" {
				id = fmt.Sprintf("#%d", i+1)
			}
			order = append(order, id)
			byID[id] = step
		}
		return order, byID
	}
	newOrder, newSteps := steps(proposal)
	name := mapText(proposal, "name")
	if base == nil {
		labels := make([]string, 0, len(newOrder))
		for _, id := range newOrder {
			labels = append(labels, workflowStepLabel(newSteps[id]))
		}
		return fmt.Sprintf("New workflow %s with %d step(s): %s.", name, len(newOrder), strings.Join(labels, ", ")), map[string]any{"steps_added": newOrder}
	}
	oldOrder, oldSteps := steps(base)
	var added, removed, changed []string
	for _, id := range newOrder {
		old, ok := oldSteps[id]
		if !ok {
			added = append(added, id)
		} else if !reflect.DeepEqual(old, newSteps[id]) {
			changed = append(changed, id)
		}
	}
	for _, id := range oldOrder {
		if _, ok := newSteps[id]; !ok {
			removed = append(removed, id)
		}
	}
	var fields []string
	for _, key := range []string{"name", "description", "scope", "when", "enabled", "retries", "result", "on", "inputs", "with"} {
		if !reflect.DeepEqual(base[key], proposal[key]) {
			fields = append(fields, key)
		}
	}
	parts := []string{}
	describe := func(what string, ids []string, from map[string]map[string]any) {
		if len(ids) == 0 {
			return
		}
		labels := make([]string, 0, len(ids))
		for _, id := range ids {
			labels = append(labels, workflowStepLabel(from[id]))
		}
		parts = append(parts, what+" "+strings.Join(labels, ", "))
	}
	describe("steps added:", added, newSteps)
	describe("steps removed:", removed, oldSteps)
	describe("steps changed:", changed, newSteps)
	if len(fields) > 0 {
		parts = append(parts, "also changes "+strings.Join(fields, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "no change to its definition")
	}
	summary := fmt.Sprintf("Change to workflow %s (now revision %d): %s.", firstNonEmptyString(mapText(base, "name"), name), mapUint(base, "revision"), strings.Join(parts, "; "))
	return summary, map[string]any{"base_revision": mapUint(base, "revision"), "steps_added": nonNilStrings(added), "steps_removed": nonNilStrings(removed), "steps_changed": nonNilStrings(changed), "fields_changed": nonNilStrings(fields)}
}

// workerChangeSummary says what accepting a worker proposal does: a new
// worker, or the fields its pending change sets.
func workerChangeSummary(worker map[string]any) (string, map[string]any) {
	name := mapText(worker, "name")
	automations := func(w map[string]any) int {
		list, _ := w["automations"].([]any)
		return len(list)
	}
	review, isChange := worker["pending_review"].(map[string]any)
	if !isChange || mapText(worker, "lifecycle_state") == string(pebblestore.WorkerLifecycleStatePending) {
		summary := fmt.Sprintf("New worker %s with %d automation(s)", name, automations(worker))
		if description := mapText(worker, "description"); description != "" {
			summary += ": " + controlMCPTruncate(description, 240)
		}
		return summary + ". Accepting starts its schedules.", map[string]any{"automations": automations(worker)}
	}
	var fields []string
	for _, key := range []string{"name", "description", "instructions", "role", "model_profile", "automations", "budget", "workspace_requirements", "requested_capabilities"} {
		if !reflect.DeepEqual(worker[key], review[key]) {
			fields = append(fields, key)
		}
	}
	if len(fields) == 0 {
		fields = append(fields, "no definition field")
	}
	return fmt.Sprintf("Change to worker %s (revision %d): changes %s.", name, mapUint(worker, "revision"), strings.Join(fields, ", ")), map[string]any{"fields_changed": fields}
}

func nonNilStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// answerProposalRequest handles an answer to a session's pending request
// when it is a proposal request; handled is false for any other request. The
// answer must be the person's (proposalPersonRequest): allow accepts, deny
// discards, through DecideProposal. A proposal that moved is refused and its
// request cancelled with the reason.
func (s *Server) answerProposalRequest(w http.ResponseWriter, r *http.Request, sessionID, permissionID, action, reason string) bool {
	if s.perm == nil {
		return false
	}
	record, ok, err := s.perm.GetPermission(sessionID, permissionID)
	if err != nil || !ok || !permission.IsProposalDecision(record) || record.Status != pebblestore.PermissionStatusPending {
		return false
	}
	person, isPerson := proposalPersonRequest(r)
	if !isPerson {
		writeError(w, http.StatusForbidden, permission.ErrProposalDecisionPersonOnly)
		return true
	}
	var decision string
	switch strings.ToLower(strings.TrimSpace(action)) {
	case permission.ActionAllowOnce, "allow", "approve", "accept", "yes":
		decision = proposalAccept
	case permission.ActionDenyOnce, "deny", "reject", "discard", "no":
		decision = proposalDiscard
	default:
		writeError(w, http.StatusBadRequest, errors.New("a proposal is answered with allow_once (Accept) or deny_once (Discard); a lasting rule cannot decide proposals"))
		return true
	}
	var payload proposalRequestPayload
	if err := json.Unmarshal([]byte(record.ToolArguments), &payload); err != nil || proposalRequestKey(payload.Decide.Kind, payload.Decide.Machine, payload.Decide.ID) != record.CallID {
		writeError(w, http.StatusConflict, errors.New("this pending request's proposal reference is unreadable; decide the proposal in the app's Automations page"))
		return true
	}
	in := payload.Decide
	in.Decision = decision
	out, err := s.DecideProposal(r.Context(), person, in)
	if err != nil {
		var stale proposalStaleError
		if errors.As(err, &stale) {
			cancelled, cancelErr := s.perm.ResolveProposalDecision(sessionID, permissionID, permission.ActionCancel, controlMCPTruncate(stale.Error(), 900))
			body := map[string]any{"ok": false, "error": stale.Error(), "session_id": sessionID}
			if cancelErr == nil {
				body["permission"] = cancelled
			}
			writeJSON(w, http.StatusConflict, body)
			return true
		}
		writeError(w, linkedTaskStatus(err), err)
		return true
	}
	updated, _, _ := s.perm.GetPermission(sessionID, permissionID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": updated, "proposal": out})
	return true
}

// proposalsAnswerWith adds pending_proposals to a list answer: the proposals
// on machine, of kind ("" for both).
func (s *Server) proposalsAnswerWith(ctx context.Context, p identity.Principal, machine, kind string, filter func(ProposalView) bool) ([]ProposalView, error) {
	list, err := s.listProposals(ctx, p, machine, kind)
	if err != nil {
		return nil, err
	}
	out := []ProposalView{}
	for _, view := range list {
		if (kind == "" || view.Kind == kind) && (filter == nil || filter(view)) {
			out = append(out, view)
		}
	}
	return out, nil
}

// proposingSession is the conversation that proposed, for a propose call:
// the Orchestrator's session, as its tool call marked it
// (tool.WithProposingSession). A Swarm Control caller's chat is not a Swarm
// conversation, and a route's caller (the person, or a linked home) has none.
func proposingSession(ctx context.Context) string {
	if _, control := tool.ControlScopesFromContext(ctx); control {
		return ""
	}
	return tool.ProposingSessionFromContext(ctx)
}

// answerMap decodes a service answer (a map, JSON text or raw JSON) into a
// map, or nil.
func answerMap(out any) map[string]any {
	switch v := out.(type) {
	case map[string]any:
		return v
	case nil:
		return nil
	}
	var raw []byte
	switch v := out.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = encoded
	}
	var answer map[string]any
	if json.Unmarshal(raw, &answer) != nil {
		return nil
	}
	return answer
}

// mustJSONRoundTrip is v as JSON text (nil when it cannot be encoded).
func mustJSONRoundTrip(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// answerData is an answer's own fields: its data when it is an envelope.
func answerData(answer map[string]any) map[string]any {
	if data, ok := answer["data"].(map[string]any); ok {
		return data
	}
	return answer
}

// withProposalRequest finishes a propose (or a worker create) answer: when
// it left a proposal waiting for the person, the proposing conversation gets
// its pending request (decision_request), and next_action says to tell the
// person and wait. Any other answer (a worker change applied at once) is
// returned as it came.
func (s *Server) withProposalRequest(ctx context.Context, p identity.Principal, machine, kind string, out any) (any, error) {
	answer := answerMap(out)
	if answer == nil {
		return out, nil
	}
	target := answerData(answer)
	var pending bool
	if kind == proposalKindWorkflow {
		proposal, _ := target["automation"].(map[string]any)
		_, pending = workflowProposalView(machine, proposal)
	} else {
		worker, _ := target["worker"].(map[string]any)
		_, pending = workerProposalView(machine, worker)
	}
	if !pending {
		return out, nil
	}
	// decision_request is always there: the conversation's pending request,
	// or why there is none (a Swarm Control caller's chat is not a Swarm
	// conversation; a route's caller has none).
	request := map[string]any{"status": "not_requested", "reason": "no Swarm conversation proposed this: the person decides in their Swarm app's Automations page (POST " + ProposalsPath + "/decide)"}
	if session := proposingSession(ctx); session != "" {
		request = s.requestProposalDecision(ctx, p, session, machine, kind, target)
	}
	target["decision_request"] = request
	target["next_action"] = proposeNextAction(kind)
	return answer, nil
}

// withPendingProposals adds pending_proposals (and what they mean) to an
// answer.
func withPendingProposals(out any, pending []ProposalView, note string) any {
	answer := answerMap(out)
	if answer == nil {
		return out
	}
	target := answerData(answer)
	target["pending_proposals"] = pending
	if len(pending) > 0 {
		target["proposals_note"] = note
	}
	return answer
}

// proposeNextAction is the next_action of a propose answer: tell the person
// and wait. It is the same for every caller; decision_request says whether
// this conversation shows the pending request.
func proposeNextAction(kind string) string {
	return fmt.Sprintf("Tell the person this %s proposal waits for them: they Accept or Discard it in the pending request of the conversation that proposed it, or in their Swarm app's Automations page (also for a linked machine). Then wait; an AI cannot accept it.", kind)
}

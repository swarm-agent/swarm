package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Settling a proposal's pending requests from what became of the proposal.
//
// A proposal_decision request names one proposal revision (its decide). The
// proposal can be decided without that request: the person accepts it in
// their app, on the linked machine that holds it, or a second answer races
// the first. Every such path ends here instead of in an error that leaves
// the request pending: the home reads the proposal's current state and
// settles the request with that outcome (proposalOutcomeFor,
// settleProposalRequestsFromState):
//
//   - accepted elsewhere: answered allow, "already accepted: now revision N";
//   - discarded or removed: answered deny;
//   - decided, but which way is unknown (a request from an older build that
//     carries no base revision): cancelled with what is known;
//   - a newer revision is pending: this revision is cancelled with the
//     reason, and the conversation gets a request for the new revision.
//
// The home settles on the person's answer and whenever it lists a pending
// request (reconcilePendingProposalRequests), so a request left pending by a
// decision on a linked machine clears itself on the next read.

// Outcome states of proposalOutcome.
const (
	proposalOutcomePending   = "pending"
	proposalOutcomeAccepted  = "accepted"
	proposalOutcomeDiscarded = "discarded"
	proposalOutcomeDecided   = "decided"
	proposalOutcomeStale     = "stale"
)

// proposalReconcileTimeout bounds one read of a proposal's state when a
// pending request is listed; proposalReconcileInterval is how long a
// request's checked state is trusted before a listing reads it again.
const (
	proposalReconcileTimeout  = 5 * time.Second
	proposalReconcileInterval = 30 * time.Second
)

// proposalOutcome is what became of the proposal revision a request names.
type proposalOutcome struct {
	State string
	// Revision is the workflow's or worker's current revision (accepted,
	// discarded) or the newer pending revision (stale).
	Revision uint64
	Text     string
	// Current is a stale outcome's newer proposal, as a propose answer
	// carries it ({"automation": ...} or {"worker": ...}); nil when no newer
	// revision waits.
	Current map[string]any
}

// settlement is the request answer an outcome makes: action and reason.
func (o proposalOutcome) settlement() (string, string, bool) {
	switch o.State {
	case proposalOutcomeAccepted:
		return permission.ActionAllowOnce, o.Text, true
	case proposalOutcomeDiscarded:
		return permission.ActionDenyOnce, o.Text, true
	case proposalOutcomeDecided, proposalOutcomeStale:
		return permission.ActionCancel, o.Text, true
	}
	return "", "", false
}

// proposalLocks serializes the decisions on one proposal (by server, account
// and request key), so two answers never both decide it.
var proposalLocks sync.Map

func (s *Server) proposalLock(account, key string) *sync.Mutex {
	lock, _ := proposalLocks.LoadOrStore(fmt.Sprintf("%p|%s|%s", s, account, key), &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// proposalChecks remembers when a pending request's proposal was last read
// by a listing (by server, session, request and its update time).
var proposalChecks sync.Map

func (s *Server) proposalCheckDue(record pebblestore.PermissionRecord, now time.Time) bool {
	key := fmt.Sprintf("%p|%s|%s|%d", s, record.SessionID, record.ID, record.UpdatedAt)
	if last, ok := proposalChecks.Load(key); ok && now.Sub(last.(time.Time)) < proposalReconcileInterval {
		return false
	}
	proposalChecks.Store(key, now)
	return true
}

// proposalRequestPayloadOf reads a proposal request's payload; ok is false
// when its proposal reference is unreadable or does not match the request.
func proposalRequestPayloadOf(record pebblestore.PermissionRecord) (proposalRequestPayload, bool) {
	var payload proposalRequestPayload
	if err := json.Unmarshal([]byte(record.ToolArguments), &payload); err != nil {
		return proposalRequestPayload{}, false
	}
	d := payload.Decide
	if (d.Kind != proposalKindWorkflow && d.Kind != proposalKindWorker) || !proposalSafeID(d.ID) || proposalRequestKey(d.Kind, d.Machine, d.ID) != record.CallID {
		return proposalRequestPayload{}, false
	}
	return payload, true
}

// proposalNotFound reports whether a machine's refusal means the record is
// gone.
func proposalNotFound(err error) bool {
	var refused *fleet.Error
	return errors.As(err, &refused) && refused.Status == http.StatusNotFound
}

// proposalOutcomeFor reads what became of the proposal revision payload
// names, on its machine, with the person's read-only proposal read.
func (s *Server) proposalOutcomeFor(ctx context.Context, p identity.Principal, payload proposalRequestPayload) (proposalOutcome, error) {
	d := payload.Decide
	call, err := s.proposalReadTransportFor(ctx, p, d.Machine)
	if err != nil {
		return proposalOutcome{}, err
	}
	if d.Kind == proposalKindWorker {
		return workerProposalOutcome(ctx, call, payload)
	}
	return workflowProposalOutcome(ctx, call, payload)
}

func workflowProposalOutcome(ctx context.Context, call proposalTransport, payload proposalRequestPayload) (proposalOutcome, error) {
	d := payload.Decide
	var current struct {
		Automation map[string]any `json:"automation"`
	}
	err := call(ctx, http.MethodGet, JobAutomationsPath+"/"+url.PathEscape(d.ID), nil, &current)
	if err == nil {
		state, revision := mapText(current.Automation, "state"), mapUint(current.Automation, "revision")
		if state == "proposed" {
			if revision == d.ExpectedRevision {
				return proposalOutcome{State: proposalOutcomePending, Revision: revision}, nil
			}
			return proposalOutcome{State: proposalOutcomeStale, Revision: revision, Current: map[string]any{"automation": current.Automation},
				Text: fmt.Sprintf("revision %d of this workflow proposal is no longer pending: the AI proposed it again (revision %d), which this conversation now asks about", d.ExpectedRevision, revision)}, nil
		}
		// A new workflow keeps its id when accepted.
		return proposalOutcome{State: proposalOutcomeAccepted, Revision: revision, Text: fmt.Sprintf("already accepted: now revision %d", revision)}, nil
	}
	if !proposalNotFound(err) {
		return proposalOutcome{}, proposalCallError(d.Machine, "the workflow read", err)
	}
	replaces := payload.Proposal.Replaces
	if payload.Proposal.Change == "" {
		// Known only from a decision (no request names it): a gone change
		// proposal may have been merged as well as removed.
		return proposalOutcome{State: proposalOutcomeDecided, Text: "already decided: the proposal is no longer pending (accepted, discarded or removed)"}, nil
	}
	if replaces == "" || !proposalSafeID(replaces) {
		return proposalOutcome{State: proposalOutcomeDiscarded, Text: "already discarded: the proposal was removed"}, nil
	}
	// A change proposal is merged into the workflow it replaces when
	// accepted, and removed when discarded: the replaced workflow's
	// revision says which.
	var base struct {
		Automation map[string]any `json:"automation"`
	}
	if err := call(ctx, http.MethodGet, JobAutomationsPath+"/"+url.PathEscape(replaces), nil, &base); err != nil {
		if proposalNotFound(err) {
			return proposalOutcome{State: proposalOutcomeDiscarded, Text: "already decided: the proposal and the workflow it changed were removed"}, nil
		}
		return proposalOutcome{}, proposalCallError(d.Machine, "the workflow read", err)
	}
	revision, baseRevision := mapUint(base.Automation, "revision"), mapUint(payload.Changes, "base_revision")
	switch {
	case baseRevision == 0:
		// A request from an older build names no base revision.
		return proposalOutcome{State: proposalOutcomeDecided, Revision: revision, Text: fmt.Sprintf("already decided: the proposal is no longer pending; the workflow it changes is now revision %d", revision)}, nil
	case revision > baseRevision:
		return proposalOutcome{State: proposalOutcomeAccepted, Revision: revision, Text: fmt.Sprintf("already accepted: now revision %d", revision)}, nil
	default:
		return proposalOutcome{State: proposalOutcomeDiscarded, Revision: revision, Text: fmt.Sprintf("already discarded: the workflow is unchanged at revision %d", revision)}, nil
	}
}

func workerProposalOutcome(ctx context.Context, call proposalTransport, payload proposalRequestPayload) (proposalOutcome, error) {
	d := payload.Decide
	var current struct {
		Worker map[string]any `json:"worker"`
	}
	if err := call(ctx, http.MethodGet, WorkersPath+"/"+url.PathEscape(d.ID), nil, &current); err != nil {
		if proposalNotFound(err) {
			return proposalOutcome{State: proposalOutcomeDiscarded, Text: "already discarded: the worker was removed"}, nil
		}
		return proposalOutcome{}, proposalCallError(d.Machine, "the worker read", err)
	}
	revision := mapUint(current.Worker, "revision")
	if view, pending := workerProposalView(d.Machine, current.Worker); pending {
		if view.Revision == d.ExpectedRevision {
			return proposalOutcome{State: proposalOutcomePending, Revision: revision}, nil
		}
		return proposalOutcome{State: proposalOutcomeStale, Revision: view.Revision, Current: map[string]any{"worker": current.Worker},
			Text: fmt.Sprintf("revision %d of this worker proposal is no longer pending: it is now revision %d, which this conversation now asks about", d.ExpectedRevision, view.Revision)}, nil
	}
	if current.Worker["pending_review"] != nil && current.Worker["pending_review_stale"] == true {
		return proposalOutcome{State: proposalOutcomeStale, Revision: revision, Text: fmt.Sprintf("this worker change is stale: the worker changed since it was proposed (now revision %d); ask the AI to propose again", revision)}, nil
	}
	return proposalOutcome{State: proposalOutcomeAccepted, Revision: revision, Text: fmt.Sprintf("already accepted: now revision %d", revision)}, nil
}

// proposalSettled is what settling a proposal's requests from its state did.
type proposalSettled struct {
	Outcome proposalOutcome
	// Answered counts the requests settled; Sessions are their
	// conversations.
	Answered int
	Sessions []string
	// Requested are the new requests a stale outcome made, by session.
	Requested map[string]pebblestore.PermissionRecord
}

// settleProposalRequestsFromState reads what became of the proposal revision
// payload names and, when it is no longer pending, settles every pending
// request for it in the person's conversations with that outcome; a newer
// pending revision gets a request in each of those conversations. The caller
// holds the proposal's lock.
func (s *Server) settleProposalRequestsFromState(ctx context.Context, p identity.Principal, payload proposalRequestPayload) (proposalSettled, error) {
	outcome, err := s.proposalOutcomeFor(ctx, p, payload)
	if err != nil {
		return proposalSettled{}, err
	}
	out := proposalSettled{Outcome: outcome}
	action, reason, settle := outcome.settlement()
	if !settle {
		return out, nil
	}
	d := payload.Decide
	key := proposalRequestKey(d.Kind, d.Machine, d.ID)
	out.Sessions, err = s.settleProposalRequestsIn(p, key, action, controlMCPTruncate(reason, 900))
	out.Answered = len(out.Sessions)
	if outcome.Current == nil || len(out.Sessions) == 0 {
		return out, err
	}
	request, buildErr := s.proposalRequestFor(ctx, p, d.Machine, d.Kind, outcome.Current)
	if buildErr != nil {
		return out, errors.Join(err, buildErr)
	}
	raw, buildErr := json.Marshal(request)
	if buildErr != nil {
		return out, errors.Join(err, buildErr)
	}
	out.Requested = map[string]pebblestore.PermissionRecord{}
	for _, session := range out.Sessions {
		record, reqErr := s.perm.RequestProposalDecision(session, key, "manage_"+d.Kind+"s", string(raw))
		if reqErr != nil {
			err = errors.Join(err, reqErr)
			continue
		}
		out.Requested[session] = record
	}
	return out, err
}

// settleProposalRequestsIn answers every pending request with key in the
// person's conversations and returns their sessions.
func (s *Server) settleProposalRequestsIn(p identity.Principal, key, action, reason string) ([]string, error) {
	if s.perm == nil || !p.Valid() {
		return nil, nil
	}
	summaries, err := s.perm.ListPendingSummaries(p.AccountScopeID, p.UserID, 500)
	if err != nil {
		return nil, err
	}
	var sessions []string
	var firstErr error
	for _, summary := range summaries {
		pending, err := s.perm.ListPending(summary.SessionID, 1000)
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		for _, record := range pending {
			if !permission.IsProposalDecision(record) || record.CallID != key {
				continue
			}
			if _, err := s.perm.ResolveProposalDecision(record.SessionID, record.ID, action, reason); err != nil {
				firstErr = errors.Join(firstErr, err)
				continue
			}
			sessions = append(sessions, record.SessionID)
		}
	}
	return sessions, firstErr
}

// reconcilePendingProposalRequests settles, before a listing shows them, the
// pending proposal requests among records whose proposal was decided
// elsewhere (on a linked machine, in the app, by another answer). It returns
// records with each settled request's new state (a stale one's new request),
// so a pending listing drops what is no longer pending. A proposal that
// cannot be read now (a machine offline) stays pending as it is; each
// request is read at most once per proposalReconcileInterval, and never
// while an answer to it is in flight.
func (s *Server) reconcilePendingProposalRequests(p identity.Principal, records []pebblestore.PermissionRecord) []pebblestore.PermissionRecord {
	if s == nil || s.perm == nil || !p.Valid() {
		return records
	}
	now := time.Now()
	var out []pebblestore.PermissionRecord
	for i, record := range records {
		if !permission.IsProposalDecision(record) || record.Status != pebblestore.PermissionStatusPending || !s.proposalCheckDue(record, now) {
			if out != nil {
				out = append(out, record)
			}
			continue
		}
		updated, changed := s.reconcileProposalRequest(p, record)
		if !changed {
			if out != nil {
				out = append(out, record)
			}
			continue
		}
		if out == nil {
			out = append(make([]pebblestore.PermissionRecord, 0, len(records)), records[:i]...)
		}
		out = append(out, updated)
	}
	if out == nil {
		return records
	}
	return out
}

// reconcileProposalRequest settles one listed pending proposal request from
// its proposal's state; changed is false when it stays as it was.
func (s *Server) reconcileProposalRequest(p identity.Principal, record pebblestore.PermissionRecord) (pebblestore.PermissionRecord, bool) {
	payload, ok := proposalRequestPayloadOf(record)
	if !ok {
		return record, false
	}
	lock := s.proposalLock(p.AccountScopeID, record.CallID)
	if !lock.TryLock() {
		return record, false
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), proposalReconcileTimeout)
	defer cancel()
	settled, _ := s.settleProposalRequestsFromState(ctx, p, payload)
	if settled.Answered == 0 {
		return record, false
	}
	if again, ok := settled.Requested[record.SessionID]; ok {
		return again, true
	}
	updated, found, err := s.perm.GetPermission(record.SessionID, record.ID)
	if err != nil || !found {
		return record, false
	}
	return updated, true
}

// pendingOnly keeps the pending records.
func pendingOnly(records []pebblestore.PermissionRecord) []pebblestore.PermissionRecord {
	out := make([]pebblestore.PermissionRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
			out = append(out, record)
		}
	}
	return out
}

// proposalRequestOutcome is the outcome a settled proposal request records:
// accepted, discarded or cancelled, with its reason.
func proposalRequestOutcome(record pebblestore.PermissionRecord) string {
	switch record.Status {
	case pebblestore.PermissionStatusApproved:
		return proposalOutcomeAccepted
	case pebblestore.PermissionStatusDenied:
		return proposalOutcomeDiscarded
	case pebblestore.PermissionStatusPending:
		return proposalOutcomePending
	}
	return "cancelled"
}

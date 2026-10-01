package pebblestore

import (
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble"
)

// DesignAllocation is trusted runtime input, never a tool-supplied child identity.
// The session, run and lineage are derived inside the canonical transaction.
type DesignAllocation struct {
	RouterAlert      string
	RequestID        string
	ExpectedRevision uint64
	Candidate        int
	Attempt          int
	Preference       ModelPreference
}

func DesignChildID(p DesignPrincipal, request string, candidate, attempt int) string {
	b, _ := json.Marshal([]any{p, request, candidate, attempt})
	return "design-" + designDigest(b)
}

func NewDesignAllocationMutation(p DesignPrincipal, a DesignAllocation) V3SessionMutationInput {
	id := DesignChildID(p, a.RequestID, a.Candidate, a.Attempt)
	b, _ := json.Marshal(a)
	return V3SessionMutationInput{SessionID: id, UserID: p.PrincipalID, AccountScopeID: p.AccountID,
		Kind: V3SessionMutationCreateSession, IdempotencyKey: id, PayloadHash: designDigest(b),
		Session: &SessionSnapshot{ID: id}, DesignAllocation: &a}
}

// Called under designMu and canonical account/session locks, before any writes.
func (s *SessionStore) prepareDesignAllocation(in *V3SessionMutationInput) error {
	a := in.DesignAllocation
	if a == nil {
		return nil
	}
	p := DesignPrincipal{AccountID: in.AccountScopeID, PrincipalID: in.UserID}
	if designOwner(p) != nil || in.Kind != V3SessionMutationCreateSession || a.Attempt < 1 || a.Attempt > MaxDesignAttempts || in.DesignAcceptance != nil || in.RunIntent != nil || in.Message != nil || in.PlanSave != nil || in.Artifact != nil || in.ArtifactV2 != nil || in.ArtifactV3 != nil || a.Preference.Provider == "" || a.Preference.Model == "" {
		return ErrDesignInvalid
	}
	r, err := s.store.GetDesignRequest(p, a.RequestID)
	if err != nil {
		return err
	}
	if r.Revision != a.ExpectedRevision {
		return ErrDesignConflict
	}
	if a.Candidate < 0 || a.Candidate >= len(r.Candidates) {
		return ErrDesignInvalid
	}
	c := r.Candidates[a.Candidate]
	if a.Attempt != len(c.Attempts)+1 || (c.State != DesignQueued && c.State != DesignFailed && c.State != DesignInterrupted) {
		return ErrDesignConflict
	}
	id := DesignChildID(p, r.ID, a.Candidate, a.Attempt)
	if in.SessionID != id {
		return ErrDesignInvalid
	}
	if _, ok, err := s.GetSession(id); err != nil {
		return err
	} else if ok {
		return ErrDesignConflict
	}
	parent, ok, err := s.GetSession(r.ParentSessionID)
	if err != nil {
		return err
	}
	if !ok || parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	run, ok, err := s.GetV3SessionRunIntent(r.ParentSessionID, r.ParentRunID)
	if err != nil {
		return err
	}
	if !ok || run.AccountScopeID != p.AccountID || run.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	in.Session = &SessionSnapshot{ID: id, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Mode: "auto", Title: "Designer request", Preference: a.Preference,
		Metadata: map[string]any{"lineage_kind": "delegated_subagent", "lineage_label": "@designer", "launch_source": "manage_design", "subagent": "system-designer", "requested_subagent": "designer", "agent_name": "designer", "resolved_agent_name": "system-designer", "agent_mode": "subagent", "parent_session_id": r.ParentSessionID, "parent_run_id": r.ParentRunID, "design_request_id": r.ID, "design_candidate": a.Candidate, "design_attempt": a.Attempt, "router_alert": a.RouterAlert}}
	in.RunIntent = &V3SessionRunIntent{SessionID: id, RunID: id, ParentSessionID: r.ParentSessionID, RunSessionID: id, AttemptID: fmt.Sprint(a.Attempt), Status: V3RunIntentPendingExecutor}
	return nil
}

func (s *SessionStore) setDesignAllocationInBatch(b *pebble.Batch, in V3SessionMutationInput) error {
	a := in.DesignAllocation
	if a == nil {
		return nil
	}
	p := DesignPrincipal{AccountID: in.AccountScopeID, PrincipalID: in.UserID}
	r, err := s.store.GetDesignRequest(p, a.RequestID)
	if err != nil {
		return err
	}
	c := &r.Candidates[a.Candidate]
	c.State = DesignRunning
	c.Attempts = append(c.Attempts, DesignAttempt{Number: a.Attempt, ChildSessionID: in.SessionID, RunID: in.SessionID, State: DesignRunning, RouterAlert: a.RouterAlert})
	if r.Revision == ^uint64(0) {
		return ErrDesignConflict
	}
	r.Revision++
	r.State = designRequestState(r)
	if err := designSet(b, designKey(p, "request", r.ID), r); err != nil {
		return err
	}
	return designSet(b, designKey(p, "pending", r.ID), r.ID)
}

// VerifyDesignChild checks persisted provenance rather than trusting supplied IDs.
func (s *SessionStore) VerifyDesignChild(p DesignPrincipal, r DesignRequest, candidate int) (DesignAttempt, error) {
	if r.Owner != p || candidate < 0 || candidate >= len(r.Candidates) {
		return DesignAttempt{}, ErrDesignInvalid
	}
	attempts := r.Candidates[candidate].Attempts
	if len(attempts) == 0 {
		return DesignAttempt{}, ErrDesignConflict
	}
	a := attempts[len(attempts)-1]
	id := DesignChildID(p, r.ID, candidate, a.Number)
	child, ok, err := s.GetSession(id)
	if err != nil {
		return DesignAttempt{}, err
	}
	if !ok || a.ChildSessionID != id || a.RunID != id || child.AccountScopeID != p.AccountID || child.UserID != p.PrincipalID || child.Metadata["parent_session_id"] != r.ParentSessionID || child.Metadata["design_request_id"] != r.ID || child.Metadata["agent_name"] != "designer" || child.Metadata["lineage_kind"] != "delegated_subagent" || child.Metadata["launch_source"] != "manage_design" || child.Metadata["parent_run_id"] != r.ParentRunID || fmt.Sprint(child.Metadata["design_candidate"]) != fmt.Sprint(candidate) || fmt.Sprint(child.Metadata["design_attempt"]) != fmt.Sprint(a.Number) {
		return DesignAttempt{}, fmt.Errorf("%w: child lineage", ErrDesignConflict)
	}
	intent, ok, err := s.GetV3SessionRunIntent(id, id)
	if err != nil {
		return DesignAttempt{}, err
	}
	if !ok || intent.AccountScopeID != p.AccountID || intent.UserID != p.PrincipalID || intent.ParentSessionID != r.ParentSessionID || intent.RunSessionID != id || intent.AttemptID != fmt.Sprint(a.Number) {
		return DesignAttempt{}, ErrDesignConflict
	}
	return a, nil
}

package run

import (
	"context"
	"errors"
	"fmt"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/executioncapacity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// AllocateDesignChild is scheduler-only: no provider call, file hydration or
// parent-run cancellation registration occurs here. The caller owns the returned
// lease and must release it, or attach it to execution via WithLease. Recovery
// returns the exact persisted child without authorizing automatic provider replay.
func (s *Service) AllocateDesignChild(ctx context.Context, p store.DesignPrincipal, requestID string, revision uint64, candidate, attempt int) (store.DesignAttempt, executioncapacity.Lease, error) {
	if s == nil || s.sessions == nil || s.permissions == nil {
		return store.DesignAttempt{}, nil, errors.New("design allocation services unavailable")
	}
	r, err := s.sessions.DesignStore().GetDesignRequest(p, requestID)
	if err != nil {
		return store.DesignAttempt{}, nil, err
	}
	if candidate < 0 || candidate >= len(r.Candidates) || attempt < 1 {
		return store.DesignAttempt{}, nil, store.ErrDesignInvalid
	}
	c := r.Candidates[candidate]
	if len(c.Attempts) == attempt {
		a, err := s.sessions.Store().VerifyDesignChild(p, r, candidate)
		return a, nil, err
	}
	if r.Revision != revision || len(c.Attempts)+1 != attempt {
		return store.DesignAttempt{}, nil, store.ErrDesignConflict
	}
	id := store.DesignChildID(p, requestID, candidate, attempt)
	lease, err := s.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: p.AccountID, SessionID: id, RunID: id, Kind: executioncapacity.ExecutionKindOrdinary})
	if err != nil {
		return store.DesignAttempt{}, nil, err
	}
	resolved, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, p.AccountID, agentruntime.DesignerAgentID, "")
	alert := ""
	if err != nil || !resolved.CatalogPresent {
		if s.model == nil {
			lease.Release()
			return store.DesignAttempt{}, nil, err
		}
		resolved, err = s.model.GetResolvedPreferenceForAccount(p.AccountID)
		if err != nil || !resolved.CatalogPresent {
			lease.Release()
			return store.DesignAttempt{}, nil, errors.New("Designer assignment and account default model are unavailable")
		}
		alert = fmt.Sprintf("Designer assignment unavailable; using account default %s/%s.", resolved.Preference.Provider, resolved.Preference.Model)
	}
	input := store.NewDesignAllocationMutation(p, store.DesignAllocation{RequestID: requestID, ExpectedRevision: revision, Candidate: candidate, Attempt: attempt, Preference: resolved.Preference, RouterAlert: alert})
	result, err := s.sessions.ApplySessionMutation(input)
	if err != nil {
		lease.Release()
		return store.DesignAttempt{}, nil, err
	}
	if result.Replayed {
		lease.Release()
		lease = nil
	}
	return store.DesignAttempt{Number: attempt, ChildSessionID: id, RunID: id, State: store.DesignRunning, RouterAlert: alert}, lease, nil
}

// ReconcileDesignCancellation dispatches canonical cancellation and only confirms
// the independent observation after the canonical run intent is cancelled.
// A running executor must acknowledge stop; an unavailable executor is not success.
func (s *Service) ReconcileDesignCancellation(p store.DesignPrincipal, requestID string, candidate int) (store.DesignRequest, error) {
	r, err := s.sessions.DesignStore().GetDesignRequest(p, requestID)
	if err != nil {
		return r, err
	}
	if candidate < 0 || candidate >= len(r.Candidates) || r.Candidates[candidate].State != store.DesignCancelRequested {
		return r, store.ErrDesignConflict
	}
	a, err := s.sessions.Store().VerifyDesignChild(p, r, candidate)
	if err != nil {
		return r, err
	}
	projection, found, err := s.sessions.Store().GetV3SessionProjection(a.ChildSessionID)
	if err != nil {
		return r, err
	}
	if !found {
		return r, store.ErrDesignNotFound
	}
	intent, ok, err := s.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if err != nil {
		return r, err
	}
	if !ok || intent.AccountScopeID != p.AccountID || intent.UserID != p.PrincipalID {
		return r, store.ErrDesignNotFound
	}
	if intent.Status != store.V3RunIntentCancelled {
		if intent.Status != store.V3RunIntentPendingExecutor {
			return r, s.StopSessionRun(a.ChildSessionID, a.RunID, "design cancellation requested")
		}
		// Pending allocation has no provider owner. Sequence CAS prevents racing an
		// executor's canonical claim; on conflict reconciliation retries from storage.
		intent.Status = store.V3RunIntentCancelled
		_, err = s.sessions.ApplySessionMutation(store.V3SessionMutationInput{SessionID: a.ChildSessionID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: store.V3SessionMutationRecordRunIntent, IdempotencyKey: "design-cancel", PayloadHash: "design-cancel", RunIntent: &intent, ExpectedLastEventSeq: &projection.LastEventSeq})
		if err != nil {
			return r, err
		}
	}
	return s.sessions.DesignStore().RecordDesignAttempt(p, requestID, store.DesignAttemptMutation{IdempotencyKey: "cancel-confirm-" + a.RunID, ExpectedRevision: r.Revision, Candidate: candidate, ChildSessionID: a.ChildSessionID, RunID: a.RunID, State: store.DesignCancelled, ReasonCode: "canonical_cancelled"})
}

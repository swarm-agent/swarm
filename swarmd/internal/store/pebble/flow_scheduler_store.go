package pebblestore

import (
	"context"
	"errors"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
)

type FlowSchedulerStore struct {
	flows *FlowStore
}

func NewFlowSchedulerStore(flows *FlowStore) *FlowSchedulerStore {
	return &FlowSchedulerStore{flows: flows}
}

func (s *FlowSchedulerStore) ListDue(ctx context.Context, now time.Time, limit int) ([]flow.DueRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.flows == nil {
		return nil, errors.New("flow scheduler store is not configured")
	}
	dueRecords, err := s.flows.ListDue(now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]flow.DueRun, 0, len(dueRecords))
	for _, record := range dueRecords {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		accepted, ok, err := s.flows.GetAcceptedAssignmentForAccount(record.AccountScopeID, record.FlowID)
		if err != nil {
			return nil, err
		}
		if !ok || accepted.Revision != record.Revision {
			continue
		}
		if strings.TrimSpace(accepted.AccountScopeID) == "" || strings.TrimSpace(accepted.UserID) == "" {
			return nil, errors.New("accepted flow assignment owner is required")
		}
		out = append(out, flow.DueRun{Assignment: accepted, ScheduledAt: record.ScheduledAt})
	}
	return out, nil
}

func (s *FlowSchedulerStore) ClaimRun(ctx context.Context, claim flow.RunClaim) (flow.RunClaim, bool, error) {
	if err := ctx.Err(); err != nil {
		return flow.RunClaim{}, false, err
	}
	if s == nil || s.flows == nil {
		return flow.RunClaim{}, false, errors.New("flow scheduler store is not configured")
	}
	record, inserted, err := s.flows.ClaimRun(FlowRunClaimRecord{
		AccountScopeID: claim.AccountScopeID,
		UserID:         claim.UserID,
		FlowID:         claim.FlowID,
		Revision:       claim.Revision,
		ScheduledAt:    claim.ScheduledAt,
		RunID:          claim.RunID,
		ClaimedAt:      claim.ClaimedAt,
		LeaseUntil:     claim.LeaseUntil,
	})
	if err != nil {
		return flow.RunClaim{}, false, err
	}
	return flow.RunClaim{
		AccountScopeID: record.AccountScopeID,
		UserID:         record.UserID,
		FlowID:         record.FlowID,
		Revision:       record.Revision,
		ScheduledAt:    record.ScheduledAt,
		RunID:          record.RunID,
		ClaimedAt:      record.ClaimedAt,
		LeaseUntil:     record.LeaseUntil,
	}, inserted, nil
}

func (s *FlowSchedulerStore) DeleteDue(ctx context.Context, accountScopeID, flowID string, revision int64, scheduledAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.flows == nil {
		return errors.New("flow scheduler store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account_scope_id is required to delete due run")
	}
	accepted, ok, err := s.flows.GetAcceptedAssignmentForAccount(accountScopeID, flowID)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(accepted.UserID) == "" {
		return errors.New("accepted flow assignment owner is required to delete due run")
	}
	return s.flows.DeleteDue(FlowDueRecord{AccountScopeID: accepted.AccountScopeID, UserID: accepted.UserID, FlowID: flowID, Revision: revision, DueAt: scheduledAt, ScheduledAt: scheduledAt})
}

func (s *FlowSchedulerStore) ScheduleNext(ctx context.Context, assignment flow.AcceptedAssignment, after time.Time) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	if s == nil || s.flows == nil {
		return time.Time{}, false, errors.New("flow scheduler store is not configured")
	}
	next, ok, err := flow.NextFireAfter(assignment.Assignment, after)
	if err != nil || !ok {
		return next, ok, err
	}
	_, err = s.flows.PutDue(FlowDueRecord{
		AccountScopeID: assignment.AccountScopeID,
		UserID:         assignment.UserID,
		FlowID:         assignment.FlowID,
		Revision:       assignment.Revision,
		DueAt:          next,
		ScheduledAt:    next,
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return next, true, nil
}

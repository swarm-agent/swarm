package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DueRun struct {
	Assignment  AcceptedAssignment `json:"assignment"`
	ScheduledAt time.Time          `json:"scheduled_at"`
}

type RunClaim struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	ScheduledAt time.Time `json:"scheduled_at"`
	RunID       string    `json:"run_id"`
	ClaimedAt   time.Time `json:"claimed_at"`
	LeaseUntil  time.Time `json:"lease_until,omitempty"`
}

type SchedulerStore interface {
	ListDue(ctx context.Context, now time.Time, limit int) ([]DueRun, error)
	ClaimRun(ctx context.Context, claim RunClaim) (RunClaim, bool, error)
	DeleteDue(ctx context.Context, flowID string, revision int64, scheduledAt time.Time) error
	ScheduleNext(ctx context.Context, assignment AcceptedAssignment, after time.Time) (time.Time, bool, error)
}

type Scheduler struct {
	Store    SchedulerStore
	Runner   FlowRunner
	Now      func() time.Time
	NewRunID func(DueRun) string
	LeaseFor time.Duration
}

func (s Scheduler) Tick(ctx context.Context, limit int) ([]RunStart, error) {
	if s.Store == nil {
		return nil, errors.New("flow scheduler store is not configured")
	}
	if s.Runner == nil {
		return nil, errors.New("flow scheduler runner is not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	due, err := s.Store.ListDue(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	starts := make([]RunStart, 0, len(due))
	for _, item := range due {
		start, claimed, err := s.runDue(ctx, item, now)
		if err != nil {
			return starts, err
		}
		if claimed {
			starts = append(starts, start)
		}
	}
	return starts, nil
}

func (s Scheduler) runDue(ctx context.Context, item DueRun, now time.Time) (RunStart, bool, error) {
	assignment := item.Assignment
	assignment.Assignment.FlowID = strings.TrimSpace(assignment.Assignment.FlowID)
	if assignment.Assignment.FlowID == "" || assignment.Assignment.Revision <= 0 {
		return RunStart{}, false, errors.New("due flow_id and revision are required")
	}
	scheduledAt := item.ScheduledAt.UTC()
	if scheduledAt.IsZero() {
		scheduledAt = now.UTC()
	}
	runID := ""
	if s.NewRunID != nil {
		runID = strings.TrimSpace(s.NewRunID(item))
	}
	if runID == "" {
		runID = fmt.Sprintf("flow-%s-%d-%d", assignment.Assignment.FlowID, assignment.Assignment.Revision, scheduledAt.UnixMilli())
	}
	claim := RunClaim{
		FlowID:      assignment.Assignment.FlowID,
		Revision:    assignment.Assignment.Revision,
		ScheduledAt: scheduledAt,
		RunID:       runID,
		ClaimedAt:   now.UTC(),
	}
	if s.LeaseFor > 0 {
		claim.LeaseUntil = claim.ClaimedAt.Add(s.LeaseFor).UTC()
	}
	storedClaim, claimed, err := s.Store.ClaimRun(ctx, claim)
	if err != nil {
		return RunStart{}, false, err
	}
	if !claimed {
		return RunStart{}, false, nil
	}
	request := RunRequest{FlowID: claim.FlowID, Revision: claim.Revision, ScheduledAt: claim.ScheduledAt, RunID: storedClaim.RunID}
	start, err := s.Runner.RunAcceptedFlow(ctx, assignment, request)
	if err != nil {
		return RunStart{}, true, err
	}
	if start.RunID == "" {
		start.RunID = storedClaim.RunID
	}
	if start.FlowID == "" {
		start.FlowID = claim.FlowID
	}
	if start.Revision == 0 {
		start.Revision = claim.Revision
	}
	if start.ScheduledAt.IsZero() {
		start.ScheduledAt = claim.ScheduledAt
	}
	if err := s.Store.DeleteDue(ctx, claim.FlowID, claim.Revision, claim.ScheduledAt); err != nil {
		return start, true, err
	}
	if _, _, err := s.Store.ScheduleNext(ctx, assignment, claim.ScheduledAt); err != nil {
		return start, true, err
	}
	return start, true, nil
}

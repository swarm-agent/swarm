package session

import (
	"context"
	"errors"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

var ErrAutomationV2PreparationFailed = errors.New("automation v2 preparation cannot be safely replayed")

type AutomationV2ExecutionHost interface {
	Start(context.Context, store.AutomationV2Occurrence) error
	Cancel(context.Context, store.AutomationV2Occurrence) error
	Outcome(store.AutomationV2Occurrence) (string, string, error)
}

// Each sweep visits at most ten definitions and 25 pending occurrences per
// definition, sequentially. Cursors are accelerators; due slots, immutable
// snapshots, pending indexes and run intents are the restart authorities.
type AutomationV2Scheduler struct {
	sessions *Service
	host     AutomationV2ExecutionHost
	cursor   string
	pending  map[string]string
}

func NewAutomationV2Scheduler(s *Service, host AutomationV2ExecutionHost) *AutomationV2Scheduler {
	return &AutomationV2Scheduler{sessions: s, host: host, pending: map[string]string{}}
}
func (s *AutomationV2Scheduler) Sweep(ctx context.Context, now time.Time) error {
	if s.sessions == nil || s.sessions.store == nil || s.host == nil {
		return errors.New("automation v2 scheduler authorities required")
	}
	rows, next, err := s.sessions.store.ScanAutomationV2Accepted(s.cursor)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		failures = append(failures, s.Tick(ctx, r, now.UnixMilli()))
	}
	s.cursor = next
	return errors.Join(failures...)
}
func (s *AutomationV2Scheduler) Tick(ctx context.Context, ref store.AutomationV2Record, now int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db := s.sessions.store
	r, found, err := db.GetAutomationV2Record(ref.AccountID, ref.UserID, ref.WorkspaceID, ref.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrAutomationV2Conflict
	}
	// Admission always compares the current accepted revision and generation in
	// the same V3 mutation that advances due time and writes the pending receipt.
	var failures []error
	if r.Enabled && !r.Cancelled && !r.Archived && r.NextDueAt <= now && !(r.Authorization.Kind == "at" && now >= r.Authorization.ExpiresAt) {
		_, err := db.AdmitAutomationV2(r, now)
		if err != nil && !errors.Is(err, store.ErrAutomationV2Conflict) {
			failures = append(failures, err)
		}
	}
	occurrences, next, err := db.ListAutomationV2Occurrences(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, s.pending[r.AutomationID], true, 25)
	if err != nil {
		return err
	}
	for _, o := range occurrences {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, _, err := db.GetAutomationV2Record(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if int64(o.Record.Generation) <= current.CancelThrough {
			err = s.host.Cancel(ctx, o)
			if err == nil {
				err = db.ObserveAutomationV2(o, "cancelled", "explicit cancel_all acknowledged by execution host", now)
			}
			failures = append(failures, err)
			continue
		}
		state, detail, err := s.host.Outcome(o)
		if err != nil {
			failures = append(failures, err)
		}
		if state == "admitted" {
			if startErr := s.host.Start(ctx, o); startErr != nil {
				state, detail = "unavailable", "execution preparation or wake failed; retry retained"
				if errors.Is(startErr, ErrAutomationV2PreparationFailed) {
					state, detail = "failed", "unpublished allocation collision; lane preserved, no execution"
				}
				failures = append(failures, startErr)
			} else {
				state, detail, err = s.host.Outcome(o)
				failures = append(failures, err)
			}
		}
		// Preparation may have journaled a newer receipt before session creation.
		latest, found, readErr := db.GetAutomationV2Occurrence(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, o.ID)
		if readErr != nil || !found {
			failures = append(failures, readErr)
			continue
		}
		o = latest
		if state != "" && (state != o.State || detail != o.Detail) {
			failures = append(failures, db.ObserveAutomationV2(o, state, detail, now))
		}
	}
	if next == "" {
		delete(s.pending, r.AutomationID)
	} else {
		s.pending[r.AutomationID] = next
	}
	return errors.Join(failures...)
}

type AutomationV2Progress struct {
	Record              store.AutomationV2Record       `json:"record"`
	ObservedAt          int64                          `json:"observed_at"`
	Timezone            string                         `json:"timezone"`
	Forecast            []int64                        `json:"forecast"`
	ForecastIsAdmission bool                           `json:"forecast_is_admission"`
	NoNextReason        string                         `json:"no_next_reason,omitempty"`
	Occurrences         []store.AutomationV2Occurrence `json:"occurrences"`
	NextCursor          string                         `json:"next_cursor,omitempty"`
	Complete            bool                           `json:"complete"`
}

func (s *Service) AutomationV2Progress(account, user, workspace, id, timezone, cursor string, now int64) (AutomationV2Progress, error) {
	var out AutomationV2Progress
	if timezone == "" || timezone == "Local" {
		return out, errors.New("explicit display timezone required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return out, err
	}
	r, found, err := s.store.GetAutomationV2Record(account, user, workspace, id)
	if err != nil {
		return out, err
	}
	if !found {
		return out, store.ErrAutomationV2Conflict
	}
	rows, next, err := s.store.ListAutomationV2Occurrences(account, user, workspace, r.SessionID, cursor, false, 25)
	if err != nil {
		return out, err
	}
	out = AutomationV2Progress{Record: r, ObservedAt: now, Timezone: timezone, Forecast: []int64{}, Occurrences: rows, NextCursor: next, Complete: next == ""}
	switch {
	case r.Archived:
		out.NoNextReason = "archived"
	case r.Cancelled:
		out.NoNextReason = "cancelled"
	case !r.Enabled:
		out.NoNextReason = "paused"
	case r.Authorization.Kind == "at" && now >= r.Authorization.ExpiresAt:
		out.NoNextReason = "expired"
	default:
		due := r.NextDueAt
		if due <= now {
			due, err = store.AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, now)
			if err != nil {
				return out, err
			}
		}
		for i := 0; i < 5; i++ {
			if r.Authorization.Kind == "at" && due >= r.Authorization.ExpiresAt {
				if len(out.Forecast) == 0 {
					out.NoNextReason = "expiration_before_next_slot"
				}
				break
			}
			out.Forecast = append(out.Forecast, due)
			due, err = store.AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, due)
			if err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
func (s *Service) ControlAutomationV2(account, user, workspace, id string, generation uint64, action string) (store.AutomationV2Record, error) {
	if r, found, err := s.store.GetAutomationV2Record(account, user, workspace, id); err == nil && found && r.SessionID != "" {
		id = r.SessionID
	}
	return s.store.ControlAutomationV2(account, user, workspace, id, generation, action, time.Now().UnixMilli())
}

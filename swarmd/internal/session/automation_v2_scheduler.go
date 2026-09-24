package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/webhook"
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
	sessions          *Service
	host              AutomationV2ExecutionHost
	cursor            string
	pending           map[string]string
	webhookDispatcher *webhook.Dispatcher
}

func NewAutomationV2Scheduler(s *Service, host AutomationV2ExecutionHost) *AutomationV2Scheduler {
	return &AutomationV2Scheduler{sessions: s, host: host, pending: map[string]string{}}
}

func (s *AutomationV2Scheduler) SetWebhookDispatcher(d *webhook.Dispatcher) {
	s.webhookDispatcher = d
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
	r, found, err := db.GetAutomationV2Record(ref.AccountID, ref.UserID, ref.WorkspaceID, ref.AutomationID)
	if !found {
		r, found, err = db.GetAutomationV2Record(ref.AccountID, ref.UserID, ref.WorkspaceID, ref.SessionID)
	}
	if err != nil {
		return err
	}
	if !found {
		return store.ErrAutomationV2Conflict
	}
	// Admission always compares the current accepted revision and generation in
	// the same V3 mutation that advances due time and writes the pending receipt.
	var failures []error
	if r.Enabled && !r.Cancelled && !r.Archived && r.NextDueAt > 0 && r.NextDueAt <= now && !(r.Authorization.Kind == "at" && now >= r.Authorization.ExpiresAt) {
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
		current, _, err := db.GetAutomationV2Record(r.AccountID, r.UserID, r.WorkspaceID, r.AutomationID)
		if err != nil || current.AutomationID == "" {
			current, _, err = db.GetAutomationV2Record(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID)
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current.Archived || int64(o.Record.Generation) <= current.CancelThrough {
			err = s.host.Cancel(ctx, o)
			if err == nil {
				detail := "explicit cancel_all acknowledged by execution host"
				if current.Archived {
					detail = "parent session archived; execution cancelled"
				}
				err = db.ObserveAutomationV2(o, "cancelled", detail, now)
			}
			failures = append(failures, err)
			continue
		}
		if o.NextRetryAt > now {
			continue
		}
		state, detail, err := s.host.Outcome(o)
		if err != nil {
			failures = append(failures, err)
		}
		if state == "admitted" {
			if startErr := s.host.Start(ctx, o); startErr != nil {
				o.AttemptCount++
				if o.AttemptCount >= 5 || errors.Is(startErr, ErrAutomationV2PreparationFailed) {
					state, detail = "failed", "maximum execution start attempts exceeded; lane halted"
					if errors.Is(startErr, ErrAutomationV2PreparationFailed) {
						detail = "unpublished allocation collision; lane preserved, no execution"
					}
					o.NextRetryAt = 0
					s.dispatchWebhook(webhook.EventOccurrenceRetryExhausted, r, o, state, detail)
				} else {
					state = "unavailable"
					backoffMs := int64(30000) * (1 << (o.AttemptCount - 1))
					o.NextRetryAt = now + backoffMs
					detail = fmt.Sprintf("execution start failed (attempt %d/5): %v; retry in %ds", o.AttemptCount, startErr, backoffMs/1000)
					s.dispatchWebhook(webhook.EventOccurrenceFailed, r, o, state, detail)
				}
				failures = append(failures, startErr)
			} else {
				o.AttemptCount = 0
				o.NextRetryAt = 0
				state, detail, err = s.host.Outcome(o)
				failures = append(failures, err)
				s.dispatchWebhook(webhook.EventOccurrenceStarted, r, o, "running", "execution started")
			}
		}
		// Preparation may have journaled a newer receipt before session creation.
		latest, found, readErr := db.GetAutomationV2Occurrence(r.AccountID, r.UserID, r.WorkspaceID, r.SessionID, o.ID)
		if readErr != nil || !found {
			failures = append(failures, readErr)
			continue
		}
		latest.AttemptCount = o.AttemptCount
		latest.NextRetryAt = o.NextRetryAt
		o = latest
		if state != "" && (state != o.State || detail != o.Detail || o.NextRetryAt != latest.NextRetryAt || o.AttemptCount != latest.AttemptCount) {
			if state == "running" && o.State != "running" {
				s.dispatchWebhook(webhook.EventOccurrenceStarted, r, o, state, detail)
			} else if state == "succeeded" && o.State != "succeeded" {
				s.dispatchWebhook(webhook.EventOccurrenceSucceeded, r, o, state, detail)
			} else if state == "failed" && o.State != "failed" {
				if o.AttemptCount >= 5 {
					s.dispatchWebhook(webhook.EventOccurrenceRetryExhausted, r, o, state, detail)
				} else {
					s.dispatchWebhook(webhook.EventOccurrenceFailed, r, o, state, detail)
				}
			}
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
	loc, err := time.LoadLocation(timezone)
	if err != nil {
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
	filteredRows := make([]store.AutomationV2Occurrence, 0, len(rows))
	for _, o := range rows {
		if r.AutomationID != "" && o.Record.AutomationID != "" && o.Record.AutomationID != r.AutomationID {
			continue
		}
		filteredRows = append(filteredRows, o)
	}
	out = AutomationV2Progress{Record: r, ObservedAt: now, Timezone: timezone, Forecast: []int64{}, Occurrences: filteredRows, NextCursor: next, Complete: next == ""}
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
		localNow := time.UnixMilli(now).In(loc)
		dayEnd := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+1, 0, 0, 0, 0, loc).UnixMilli()
		due := r.NextDueAt
		if due <= now {
			due, err = store.AutomationV2NextDue(*r.Document.AutomationV2, r.AcceptedAt, now)
			if err != nil {
				return out, err
			}
		}
		const maxForecast = 500
		for len(out.Forecast) < maxForecast {
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
			if due >= dayEnd && len(out.Forecast) >= 5 {
				break
			}
		}
	}
	return out, nil
}
func (s *Service) ControlAutomationV2(account, user, workspace, id string, generation uint64, action string) (store.AutomationV2Record, error) {
	if r, found, err := s.store.GetAutomationV2Record(account, user, workspace, id); err == nil && found {
		if r.AutomationID != "" {
			id = r.AutomationID
		} else if r.SessionID != "" {
			id = r.SessionID
		}
	}
	return s.store.ControlAutomationV2(account, user, workspace, id, generation, action, time.Now().UnixMilli())
}

func (s *Service) TriggerAutomationV2(account, user, workspace, id string, triggerContext map[string]any) (store.AutomationV2Occurrence, error) {
	if r, found, err := s.store.GetAutomationV2Record(account, user, workspace, id); err == nil && found {
		if r.AutomationID != "" {
			id = r.AutomationID
		} else if r.SessionID != "" {
			id = r.SessionID
		}
	}
	return s.store.TriggerAutomationV2(account, user, workspace, id, triggerContext, time.Now().UnixMilli())
}

func (s *AutomationV2Scheduler) resolveDestinations(r store.AutomationV2Record) []webhook.Destination {
	destMap := make(map[string]webhook.Destination)
	settings := r.Document.WorkerV2
	if settings == nil {
		settings = r.Document.AutomationV2
	}
	if settings != nil {
		for _, wh := range settings.Webhooks {
			if wh.Enabled && wh.URL != "" {
				destMap[wh.URL] = webhook.Destination{
					ID:      wh.ID,
					URL:     wh.URL,
					Secret:  wh.Secret,
					Format:  wh.Format,
					Events:  wh.Events,
					Enabled: wh.Enabled,
				}
			}
		}
	}
	if s.sessions != nil && s.sessions.Store() != nil {
		globals, err := s.sessions.Store().ListAutomationV2Webhooks(r.AccountID)
		if err == nil {
			for _, gw := range globals {
				if !gw.Enabled || gw.URL == "" {
					continue
				}
				if gw.WorkspaceID != "" && gw.WorkspaceID != r.WorkspaceID {
					continue
				}
				if gw.WorkerID != "" && gw.WorkerID != r.AutomationID {
					continue
				}
				destMap[gw.URL] = webhook.Destination{
					ID:      gw.ID,
					URL:     gw.URL,
					Secret:  gw.Secret,
					Format:  gw.Format,
					Events:  gw.Events,
					Enabled: gw.Enabled,
				}
			}
		}
	}
	out := make([]webhook.Destination, 0, len(destMap))
	for _, d := range destMap {
		out = append(out, d)
	}
	return out
}

func (s *AutomationV2Scheduler) dispatchWebhook(eventType string, r store.AutomationV2Record, o store.AutomationV2Occurrence, state, detail string) {
	if s == nil || s.webhookDispatcher == nil {
		return
	}
	dests := s.resolveDestinations(r)
	if len(dests) == 0 {
		return
	}
	workerTitle := ""
	if r.Document.Title != "" {
		workerTitle = r.Document.Title
	} else if r.Document.Info.Goal != "" {
		workerTitle = r.Document.Info.Goal
	}
	event := webhook.WebhookEvent{
		Type:           eventType,
		Timestamp:      time.Now().UnixMilli(),
		AccountID:      r.AccountID,
		WorkspaceID:    r.WorkspaceID,
		WorkerID:       r.AutomationID,
		WorkerTitle:    workerTitle,
		SessionID:      r.SessionID,
		OccurrenceID:   o.ID,
		State:          state,
		Detail:         detail,
		AttemptCount:   o.AttemptCount,
		TriggerContext: o.TriggerContext,
	}
	s.webhookDispatcher.DispatchAsync(event, dests)
}

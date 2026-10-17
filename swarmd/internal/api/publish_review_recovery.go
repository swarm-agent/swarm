package api

import (
	"context"
	"errors"
	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func (p *AutoPublisher) recoverReviews(ctx context.Context, deliver func(context.Context, store.Signal) error) error {
	p.mu.Lock()
	if !p.lastRecovery.IsZero() && time.Since(p.lastRecovery) < time.Minute {
		p.mu.Unlock()
		return nil
	}
	p.lastRecovery = time.Now()
	p.mu.Unlock()
	account := p.server.OutboxAccount()
	if account == "" {
		return nil
	}
	signals, next, err := store.NewWorkerStore(p.state).ReviewRecoveryPage(account, p.reviewCursor)
	if err != nil {
		return err
	}
	q := publish.NewQueue(p.state)
	var failures []error
	for _, sig := range signals {
		if err := ctx.Err(); err != nil {
			return err
		}
		retired, err := p.isRetired(sig)
		if err != nil {
			return err
		}
		if retired {
			continue
		}
		e, _, err := q.Get(account, controlMCPJobID(controlMCPJobWorker, sig.Refs["worker_id"], sig.Refs["run_id"]))
		if err != nil {
			return err
		}
		var reopened autoPublishRetirement
		if _, err := p.state.GetJSON(publishRetiredKey(sig), &reopened); err != nil {
			return err
		}
		// Only completion ingestion can persist a newer head. Old landing
		// records must not revoke that reopening while its review is pending.
		if e.State == "landed" && (reopened.Head == "" || reopened.Head == e.Request.Head) {
			if err := p.retire(sig, e.Request.Head); err != nil {
				return err
			}
			continue
		}
		if e.State == "queued" || e.State == "landing" || e.State == "landing_stopped" {
			continue
		}
		if err := deliver(ctx, sig); err != nil {
			failures = append(failures, err)
		}
	}
	p.reviewCursor = next
	return errors.Join(failures...)
}

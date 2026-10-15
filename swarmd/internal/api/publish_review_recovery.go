package api

import (
	"context"
	"errors"
	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func (p *AutoPublisher) recoverReviews(ctx context.Context, deliver func(context.Context, store.Signal) error) error {
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
		e, _, err := q.Get(account, controlMCPJobID(controlMCPJobWorker, sig.Refs["worker_id"], sig.Refs["run_id"]))
		if err != nil {
			return err
		}
		if e.State == "landed" || e.State == "queued" || e.State == "landing" || e.State == "landing_stopped" {
			continue
		}
		if err := deliver(ctx, sig); err != nil {
			failures = append(failures, err)
		}
	}
	p.reviewCursor = next
	return errors.Join(failures...)
}

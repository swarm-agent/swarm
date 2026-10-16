package publish

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// SetParallel configures the startup-only per-folder candidate window.
func (q *Queue) SetParallel(n int) { q.parallel = max(1, n) }

func (q *Queue) trainFinish(ctx context.Context, e *Entry, h QueueHooks, err error) error {
	if err != nil {
		var back *SendBackError
		var review *RebaseReviewError
		switch {
		case errors.As(err, &review):
			e.State, e.Reason = "review_pending", review.Reason
			e.Review = nil
		case errors.As(err, &back):
			e.SendBacks++
			e.State, e.Reason = "sent_back", back.Reason
			if e.SendBacks >= maxLandingSendBacks {
				e.State = "landing_stopped"
			}
			e.FollowUpID = fmt.Sprintf("land-back-%s-%d", e.Request.JobID, e.SendBacks)
			if e.Attempt > 0 {
				e.FollowUpID = fmt.Sprintf("land-back-%s-attempt-%d-%d", e.Request.JobID, e.Attempt, e.SendBacks)
			}
			e.SendBackPending = e.SendBacks <= maxLandingSendBacks
			if !e.SendBackPending {
				e.FollowUpID = ""
				e.Reason = "Landing fix rounds exhausted: " + e.Reason
			}
		default:
			e.State, e.Reason = "land_failed", oneLine(err.Error(), 200)
			var infrastructure interface{ EnvironmentFailure() bool }
			if errors.As(err, &infrastructure) && infrastructure.EnvironmentFailure() {
				e.State = "environment_failed"
			}
		}
	}
	e.TransitionSequence++
	e.NotificationID = fmt.Sprintf("attempt-%d-transition-%d", e.Attempt, e.TransitionSequence)
	e.NotificationPending = true
	if err := q.save(*e); err != nil {
		return err
	}
	return q.finish(ctx, e, h)
}

// processTrain holds the folder lock, not the store lock. Only the serial push
// lane can publish results; speculative suffix results are never reused.
func (q *Queue) processTrain(ctx context.Context, account, folder string, h QueueHooks) error {
	moves := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		entries, err := q.load(account)
		q.mu.Unlock()
		if err != nil {
			return err
		}
		var train []*Entry
		for _, entry := range entries {
			if entry.Request.Folder != folder || !pending(entry) {
				continue
			}
			e := entry
			if e.NotificationPending || e.SendBackPending {
				if err := q.finish(ctx, &e, h); err != nil {
					return err
				}
				continue
			}
			if e.TrainReady && h.Recover != nil {
				landed, err := h.Recover(ctx, e)
				if err != nil {
					return err
				}
				if landed {
					e.State, e.Commit = "landed", e.Request.Head
					if err := q.trainFinish(ctx, &e, h, nil); err != nil {
						return err
					}
					continue
				}
			}
			e.TrainReady = false
			train = append(train, &e)
			if len(train) == q.parallel {
				break
			}
		}
		if len(train) == 0 {
			return nil
		}
		var previous *Entry
		var candidates []*Entry
		for _, e := range train {
			if e.State == "queued" && h.Queued != nil {
				if err := h.Queued(ctx, *e); err != nil {
					return err
				}
			}
			if e.State != "landing" || e.NotificationID == "" {
				e.TransitionSequence++
				e.NotificationID = fmt.Sprintf("attempt-%d-transition-%d", e.Attempt, e.TransitionSequence)
			}
			e.State = "landing"
			if err := q.save(*e); err != nil {
				return err
			}
			e.Checkpoint = func() error { return q.save(*e) }
			err = nil
			if h.Stack != nil {
				var release func()
				if h.Admit != nil {
					release, err = h.Admit(ctx, *e)
					if err != nil {
						return err
					}
				}
				err = h.Stack(ctx, e, previous)
				if release != nil {
					release()
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				if errors.Is(err, ErrNotSettled) {
					return nil
				}
				if err := q.trainFinish(ctx, e, h, err); err != nil {
					return err
				}
				continue
			}
			if err := q.save(*e); err != nil {
				return err
			}
			candidates = append(candidates, e)
			previous = e
		}
		results := make([]error, len(candidates))
		admissionErrors := make([]error, len(candidates))
		var wg sync.WaitGroup
		for i, e := range candidates {
			wg.Add(1)
			go func(i int, e *Entry) {
				defer wg.Done()
				if h.Admit != nil {
					release, err := h.Admit(ctx, *e)
					if err != nil {
						admissionErrors[i] = err
						return
					}
					defer release()
				}
				results[i] = h.Prepare(ctx, e)
			}(i, e)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for i, e := range candidates {
			if admissionErrors[i] != nil {
				return admissionErrors[i]
			}
			err := results[i]
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotSettled) {
				return err
			}
			if err == nil {
				e.TrainReady = true
				if err = q.save(*e); err != nil {
					return err
				}
				var receipt LandResult
				var release func()
				if h.Admit != nil {
					release, err = h.Admit(ctx, *e)
					if err != nil {
						return err
					}
				}
				receipt, err = h.Land(ctx, *e)
				if release != nil {
					release()
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				if err == nil && receipt.Moved {
					moves++
					if moves >= 3 {
						return errors.New("base kept moving")
					}
					break
				}
				moves = 0
				if err == nil {
					e.State, e.Commit = "landed", receipt.Commit
				}
			}
			if finishErr := q.trainFinish(ctx, e, h, err); finishErr != nil {
				return finishErr
			}
			if err != nil {
				break
			}
		}
	}
}

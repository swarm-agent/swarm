package run

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

const (
	providerAttemptActivityTimeout    = 5 * time.Minute
	providerAttemptTerminationTimeout = 30 * time.Second
	providerAttemptRetryLimit         = 1
)

var errProviderAttemptActivityTimeout = errors.New("provider attempt activity timeout")

type providerAttemptResult struct {
	response provideriface.Response
	err      error
}

// runProviderAttempt supervises one provider continuation at a time. An
// activity timeout first fences and cancels the current generation, then waits
// for it to terminate before making one replacement attempt with the exact same
// durable continuation request.
type providerAttemptObserver interface {
	AttemptTimedOut(attempt int)
	AttemptCancelled(attempt int)
	AttemptRetrying(attempt int)
	AttemptFailed(attempt int, err error)
}

type providerAttemptObserverKey struct{}

func withProviderAttemptObserver(ctx context.Context, observer providerAttemptObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, providerAttemptObserverKey{}, observer)
}

func providerAttemptObserverFromContext(ctx context.Context) providerAttemptObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(providerAttemptObserverKey{}).(providerAttemptObserver)
	return observer
}

func runProviderAttempt(
	ctx context.Context,
	runner provideriface.Runner,
	req provideriface.Request,
	timeout time.Duration,
	onEvent func(provideriface.StreamEvent),
) (provideriface.Response, error) {
	if runner == nil {
		return provideriface.Response{}, errors.New("provider runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return runner.CreateResponseStreaming(ctx, req, onEvent)
	}

	var acceptedGeneration atomic.Uint64
	observer := providerAttemptObserverFromContext(ctx)
	for attempt := 0; attempt <= providerAttemptRetryLimit; attempt++ {
		generation := acceptedGeneration.Add(1)
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		activity := make(chan struct{}, 1)
		reportActivity := func(provideriface.AttemptActivityPhase) {
			if acceptedGeneration.Load() != generation {
				return
			}
			select {
			case activity <- struct{}{}:
			default:
			}
		}
		attemptCtx = provideriface.WithAttemptActivityReporter(attemptCtx, reportActivity)
		provideriface.ReportAttemptActivity(attemptCtx, provideriface.AttemptActivityStarted)

		resultCh := make(chan providerAttemptResult, 1)
		go func() {
			response, err := runner.CreateResponseStreaming(attemptCtx, req, func(event provideriface.StreamEvent) {
				provideriface.ReportAttemptActivity(attemptCtx, provideriface.AttemptActivityStreamEvent)
				if attemptCtx.Err() != nil || acceptedGeneration.Load() != generation || onEvent == nil {
					return
				}
				onEvent(event)
			})
			resultCh <- providerAttemptResult{response: response, err: err}
		}()

		timer := time.NewTimer(timeout)
		timedOut := false
		var result providerAttemptResult
	waitAttempt:
		for {
			select {
			case result = <-resultCh:
				break waitAttempt
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				timedOut = true
				if observer != nil {
					observer.AttemptTimedOut(attempt + 1)
				}
				acceptedGeneration.CompareAndSwap(generation, generation+1)
				cancelAttempt()
				if observer != nil {
					observer.AttemptCancelled(attempt + 1)
				}
				// Termination is a hard overlap boundary: never start the
				// replacement while callbacks or transport work remain live.
				select {
				case result = <-resultCh:
					break waitAttempt
				case <-time.After(providerAttemptTerminationTimeout):
					timer.Stop()
					return provideriface.Response{}, fmt.Errorf("%w: expired attempt did not terminate within %s", errProviderAttemptActivityTimeout, providerAttemptTerminationTimeout)
				}
			case <-ctx.Done():
				acceptedGeneration.CompareAndSwap(generation, generation+1)
				cancelAttempt()
				select {
				case <-resultCh:
				case <-time.After(providerAttemptTerminationTimeout):
				}
				timer.Stop()
				return provideriface.Response{}, ctx.Err()
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		cancelAttempt()

		if !timedOut {
			if err := ctx.Err(); err != nil {
				acceptedGeneration.CompareAndSwap(generation, generation+1)
				return provideriface.Response{}, err
			}
			return result.response, result.err
		}
		if attempt == providerAttemptRetryLimit {
			terminalErr := fmt.Errorf("%w after %d attempts", errProviderAttemptActivityTimeout, attempt+1)
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				terminalErr = fmt.Errorf("%w after %d attempts: %v", errProviderAttemptActivityTimeout, attempt+1, result.err)
			}
			if observer != nil {
				observer.AttemptFailed(attempt+1, terminalErr)
			}
			return provideriface.Response{}, terminalErr
		}
		if observer != nil {
			observer.AttemptRetrying(attempt + 2)
		}
	}
	return provideriface.Response{}, errProviderAttemptActivityTimeout
}

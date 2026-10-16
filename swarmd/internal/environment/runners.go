package environment

import (
	"context"
	"errors"
	"fmt"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Failure identifies infrastructure failure, never a source-code rejection.
type Failure struct{ Reason string }

func (e *Failure) EnvironmentFailure() bool { return true }
func (e *Failure) Error() string            { return "landing environment failed: " + e.Reason }

// Runners submits immutable source through the hub's archive and lease authority.
type Runners struct {
	Store *pebblestore.Store
	Wait  time.Duration
}

func (p Runners) Acquire(ctx context.Context, spec Spec) (Environment, error) {
	if p.Store == nil {
		return nil, &Failure{"runner store unavailable"}
	}
	id, err := pebblestore.GenerateToken(16)
	if err != nil {
		return nil, err
	}
	l := pebblestore.RunnerLanding{ID: id, SourceClonePath: spec.Checkout, BaseCommit: spec.BaseCommit, HeadCommit: spec.Commit, BaseImage: spec.Definition.BaseImage, Setup: spec.Definition.Setup, Env: spec.Definition.Env, Checks: spec.Checks, CheckTimeout: int(spec.CheckTimeout / time.Second)}
	if err := p.Store.CreateRunnerLanding(spec.Account, l); err != nil {
		return nil, &Failure{err.Error()}
	}
	e := &runnerEnvironment{store: p.Store, spec: spec, id: id}
	wait := p.Wait
	if wait < 0 {
		wait = 120 * time.Second
	}
	claimCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	err = e.wait(claimCtx, func(l *pebblestore.RunnerLanding) bool {
		return l.State == "claimed" || l.State == "succeeded" || l.State == "failed"
	}, nil)
	if err != nil {
		cleanup := e.Release()
		if ctx.Err() != nil {
			return nil, errors.Join(ctx.Err(), cleanup)
		}
		return nil, errors.Join(&Failure{fmt.Sprintf("no runner claim within %s or runner unavailable: %v", wait, err)}, cleanup)
	}
	if spec.Selected != nil {
		spec.Selected("runner:" + e.last.Runner)
	}
	return e, nil
}

type runnerEnvironment struct {
	store       *pebblestore.Store
	spec        Spec
	id          string
	index       int
	last        *pebblestore.RunnerLanding
	outputBytes int64
}

func (e *runnerEnvironment) wait(ctx context.Context, ready func(*pebblestore.RunnerLanding) bool, output func([]byte)) error {
	for {
		wake := e.store.RunnerLandingWake()
		l, err := e.store.RunnerLandingSnapshot(e.spec.Account, e.id)
		if err != nil {
			return &Failure{err.Error()}
		}
		e.last = l
		if output != nil && l.OutputBytes > e.outputBytes {
			n := l.OutputBytes - e.outputBytes
			if n > int64(len(l.Output)) {
				n = int64(len(l.Output))
			}
			output([]byte(l.Output[len(l.Output)-int(n):]))
			e.outputBytes = l.OutputBytes
		}
		if l.State == "environment_failed" {
			return &Failure{l.DeliveryReason}
		}
		if ready(l) {
			return nil
		}
		select {
		case <-ctx.Done():
			return &Failure{ctx.Err().Error()}
		case <-wake:
		}
	}
}
func (e *runnerEnvironment) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	if e.index >= len(e.spec.Checks) || len(req.Command) != 3 || req.Command[0] != "bash" || req.Command[1] != "-lc" || req.Command[2] != e.spec.Checks[e.index] {
		return nil, &Failure{"runner check sequence mismatch"}
	}
	if err := e.wait(ctx, func(l *pebblestore.RunnerLanding) bool { return l.State == "succeeded" || l.State == "failed" }, req.OnOutput); err != nil {
		return nil, err
	}
	if len(e.last.ExitCodes) != len(e.spec.Checks) {
		return nil, &Failure{"incomplete runner results"}
	}
	// Batch output has no check boundaries; preserve the bounded diagnostic
	// excerpt for a later failing check rather than sending an empty failure.
	if e.index > 0 && e.last.ExitCodes[e.index] != 0 && req.OnOutput != nil {
		req.OnOutput([]byte(e.last.Output))
	}
	result := &ExecResult{ExitCode: e.last.ExitCodes[e.index]}
	e.index++
	return result, nil
}
func (e *runnerEnvironment) Sync(context.Context, string) error { return nil }
func (e *runnerEnvironment) Release() error                     { return e.store.FinishRunnerLanding(e.spec.Account, e.id) }

// Auto retries infrastructure failure once, locally. Check failures are results,
// not errors, and never trigger fallback. The remote batch completes before the
// first result is exposed, so fallback cannot mix remote and local checks.
type Auto struct {
	Remote Runners
	Local  Provider
}

func (p Auto) Acquire(ctx context.Context, spec Spec) (Environment, error) {
	remote, err := p.Remote.Acquire(ctx, spec)
	if err != nil {
		var failure *Failure
		if !errors.As(err, &failure) || ctx.Err() != nil {
			return nil, err
		}
		return p.local(ctx, spec)
	}
	return &autoEnvironment{Environment: remote, provider: p, spec: spec}, nil
}
func (p Auto) local(ctx context.Context, spec Spec) (Environment, error) {
	e, err := p.Local.Acquire(ctx, spec)
	if err != nil {
		return nil, &Failure{err.Error()}
	}
	if spec.Selected != nil {
		spec.Selected("local")
	}
	return e, nil
}

type autoEnvironment struct {
	Environment
	provider Auto
	spec     Spec
	retried  bool
}

func (e *autoEnvironment) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	result, err := e.Environment.Exec(ctx, req)
	var failure *Failure
	if !errors.As(err, &failure) || e.retried || ctx.Err() != nil {
		return result, err
	}
	e.retried = true
	if releaseErr := e.Environment.Release(); releaseErr != nil {
		return nil, errors.Join(err, releaseErr)
	}
	local, localErr := e.provider.local(ctx, e.spec)
	if localErr != nil {
		return nil, &Failure{localErr.Error()}
	}
	e.Environment = local
	return local.Exec(ctx, req)
}

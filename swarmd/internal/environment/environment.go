// Package environment supplies one fresh environment per landing, released after
// that landing. Providers own execution and tree transfer; a remote runner can
// implement this contract without changing landing orchestration.
package environment

import (
	"context"
	"errors"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/sandbox"
)

type Definition struct {
	BaseImage string
	Setup     []string
	Env       map[string]string
}

type Spec struct {
	Account      string
	Checks       []string
	CheckTimeout time.Duration
	BaseCommit   string
	Selected     func(string)
	Folder       string
	Checkout     string
	Definition   Definition
	Commit       string
}

type ExecRequest = sandbox.ExecRequest
type ExecResult = provider.ExecResult

type Provider interface {
	Acquire(context.Context, Spec) (Environment, error)
}
type Environment interface {
	Exec(context.Context, ExecRequest) (*ExecResult, error)
	Sync(context.Context, string) error
	Release() error
}

// Local uses the manager's canonical folder image resolver, including the built
// setup and environment definition. Git preparation remains daemon-owned.
type Local struct{ Manager *sandbox.Manager }

func (p Local) Acquire(ctx context.Context, spec Spec) (Environment, error) {
	lease, err := p.Manager.AcquireLanding(ctx, spec.Folder, spec.Checkout)
	if err != nil {
		return nil, err
	}
	env := &localEnvironment{Landing: lease, checkout: spec.Checkout}
	if err := env.Sync(ctx, spec.Commit); err != nil {
		return nil, errors.Join(err, env.Release())
	}
	return env, nil
}

type localEnvironment struct {
	*sandbox.Landing
	checkout string
}

// Sync adopts the daemon-prepared mounted tree only if its exact commit matches.
// A remote implementation transfers this same tree before returning.
func (e *localEnvironment) Sync(ctx context.Context, commit string) error {
	if len(commit) != 40 && len(commit) != 64 {
		return errors.New("invalid environment commit")
	}
	res, err := e.Exec(ctx, ExecRequest{Command: []string{"git", "rev-parse", "HEAD"}, WorkingDir: e.checkout, Timeout: time.Minute, MaxTimeout: time.Minute, MaxOutput: 4096})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != commit {
		return errors.New("environment tree does not match landing commit")
	}
	return nil
}

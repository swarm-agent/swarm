package api

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
)

type landExecutorProvider struct {
	exec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error)
}

func (p landExecutorProvider) Acquire(context.Context, environment.Spec) (environment.Environment, error) {
	return landExecutorEnvironment{p.exec}, nil
}

type landExecutorEnvironment struct {
	exec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error)
}

func (e landExecutorEnvironment) Exec(ctx context.Context, r environment.ExecRequest) (*environment.ExecResult, error) {
	return e.exec(ctx, r)
}
func (e landExecutorEnvironment) Sync(context.Context, string) error { return nil }
func (e landExecutorEnvironment) Release() error                     { return nil }

// Existing preparation fixtures use a fake provider; no production path can
// select the old shared agent executor.
func prepareLandWorktree(ctx context.Context, rule publish.Rule, e *publish.Entry, path string, checks []string, timeout int,
	exec func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error), update func(context.Context, publish.Rule, string) (publish.Base, error), reviewChecks ...func(string) error) error {
	return prepareLandWorktreeWithExecutors(ctx, rule, e, path, checks, timeout, exec, landExecutorProvider{exec}, environment.Spec{Folder: rule.Folder, Checkout: path}, update, reviewChecks...)
}

type recordingLandProvider struct {
	acquired, executed, released int
	failure                      error
	exit                         int
}

func (p *recordingLandProvider) Acquire(_ context.Context, s environment.Spec) (environment.Environment, error) {
	p.acquired++
	if s.Commit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		return nil, errors.New("wrong commit")
	}
	return p, nil
}
func (p *recordingLandProvider) Exec(_ context.Context, r environment.ExecRequest) (*environment.ExecResult, error) {
	p.executed++
	r.OnOutput([]byte("check output"))
	return &environment.ExecResult{ExitCode: p.exit}, p.failure
}
func (p *recordingLandProvider) Sync(context.Context, string) error { return nil }
func (p *recordingLandProvider) Release() error                     { p.released++; return nil }

// Purpose: prepareLandWorktreeWithExecutors owns one provider lease across all
// checks, and releases it on success, rejection, cancellation and timeout. This
// orchestration-level fake observes the lifecycle and durable send-back payload
// without requiring Docker or a forge.
func TestLandEnvironmentLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		exit    int
	}{
		{name: "success"}, {name: "failure", exit: 1}, {name: "timeout", failure: provider.ErrOperationTimedOut}, {name: "cancelled", failure: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &recordingLandProvider{failure: tc.failure, exit: tc.exit}
			e := &publish.Entry{Request: publish.Request{Branch: "job"}}
			git := func(_ context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
				out := ""
				if r.Command[1] == "branch" {
					out = "job"
				}
				if r.Command[1] == "rev-parse" {
					out = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				}
				return &provider.ExecResult{Stdout: out}, nil
			}
			err := prepareLandWorktreeWithExecutors(context.Background(), publish.Rule{}, e, "checkout", []string{"first", "second"}, 1, git, p, environment.Spec{}, func(context.Context, publish.Rule, string) (publish.Base, error) {
				return publish.Base{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branch: "dev"}, nil
			})
			wantExec := 1
			if tc.failure == nil && tc.exit == 0 {
				wantExec = 2
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if p.acquired != 1 || p.released != 1 || p.executed != wantExec {
				t.Fatalf("lifecycle %+v", p)
			}
			if tc.exit != 0 || errors.Is(tc.failure, provider.ErrOperationTimedOut) {
				var back *publish.SendBackError
				if !errors.As(err, &back) || len(e.Checks) != 1 || e.Checks[0].Output != "check output" {
					t.Fatalf("lost send-back: %v %+v", err, e.Checks)
				}
			}
		})
	}
}

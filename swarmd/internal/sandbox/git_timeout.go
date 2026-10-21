package sandbox

import (
	"context"
	"os/exec"
	"time"
)

type gitTimeoutKey struct{}

// WithGitCommandTimeout applies only to command execution, not sandbox startup.
// Caller cancellation still bounds acquisition and execution.
func WithGitCommandTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, gitTimeoutKey{}, timeout)
}

// armGitTimeout is called after routing/acquisition, immediately before Run.
func armGitTimeout(ctx context.Context, cancel context.CancelFunc) func() {
	timeout, _ := ctx.Value(gitTimeoutKey{}).(time.Duration)
	if timeout <= 0 {
		return func() {}
	}
	timer := time.AfterFunc(timeout, cancel)
	return func() { timer.Stop() }
}

// TimedGitOutput separates acquisition from the command's execution budget.
func TimedGitOutput(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir, cmd.Env = dir, env
	manager := Default()
	if err := manager.RouteGit(runCtx, cmd); err != nil {
		return nil, err
	}
	defer ReleaseGit(cmd)
	stop := armGitTimeout(ctx, cancel)
	defer stop()
	return cmd.Output()
}

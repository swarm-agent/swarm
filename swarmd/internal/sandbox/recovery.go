package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrInterrupted is retryable by a follow-up, not by replaying a possibly
// mutating command. Bind-mounted checkouts are never removed during recovery.
var ErrInterrupted = errors.New("sandbox interrupted; retryable")

func ContainerStopped(detail string) bool {
	s := strings.ToLower(detail)
	return strings.Contains(s, "is not running") || strings.Contains(s, "state stopped") || strings.Contains(s, "state restarting") || strings.Contains(s, "no such container") || strings.Contains(s, "container is restarting") || strings.Contains(s, "containermanager.waitpid") || strings.Contains(s, "urpc method")
}

func (m *Manager) interruption(ctx context.Context, scope Scope, detail string) error {
	name := scopeContainerName(scope)
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	out, err := m.runner.Run(inspectCtx, m.cfg.Engine, "container", "inspect", "--format", "{{.State.OOMKilled}} {{.State.Running}} {{.State.Restarting}}", name)
	state := strings.Fields(string(out))
	cause := "container stopped or restarted"
	dead := ContainerStopped(detail)
	if err == nil && len(state) == 3 {
		if state[0] == "true" {
			cause, dead = "OOM (container memory limit exceeded)", true
		}
		if state[1] == "false" || state[2] == "true" {
			dead = true
		}
	}
	if !dead {
		return nil
	}
	m.invalidate(name)
	return fmt.Errorf("%w: %s; uncommitted work is preserved; continue this job with a follow-up", ErrInterrupted, cause)
}

// GitCombinedOutput retries only explicitly read-only daemon Git queries once
// after recovering a stopped container. Mutations must never be replayed.
func GitCombinedOutput(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = dir, env
		manager := Default()
		if err := manager.RouteGit(ctx, cmd); err != nil {
			return nil, err
		}
		return cmd.CombinedOutput()
	}
	return gitOutputWithRecovery(ctx, Default(), dir, args, run)
}

func gitOutputWithRecovery(ctx context.Context, m *Manager, dir string, args []string, run func() ([]byte, error)) ([]byte, error) {
	out, err := run()
	if err == nil || m == nil || !m.Active() {
		return out, err
	}
	scope, ok, scopeErr := m.ScopeFor(dir)
	scope = jobScope(ctx, scope)
	if scopeErr != nil || !ok {
		return out, err
	}
	interrupted := m.interruption(ctx, scope, string(out))
	if interrupted == nil {
		return out, err
	}
	if ReadOnlyGit(args) && ctx.Err() == nil {
		out, err = run() // routing reacquires and restarts the invalidated box
		if err == nil {
			return out, nil
		}
	}
	return out, interrupted
}

func ReadOnlyGit(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "rev-parse", "show-ref", "merge-base", "check-ref-format":
		return true
	}
	return false
}

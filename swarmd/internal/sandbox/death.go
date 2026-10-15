package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ErrSandboxDied means the command lost its execution container, not that the
// program rejected its input. Callers must not attribute this to worker code.
type ErrSandboxDied struct {
	OOMKilled bool
	Memory    string
}

func (e *ErrSandboxDied) Error() string {
	if e.OOMKilled {
		return fmt.Sprintf("the sandbox ran out of memory (limit %s); commands running in it were stopped; it will be restarted on retry", e.Memory)
	}
	return fmt.Sprintf("the sandbox stopped or restarted (memory limit %s); commands running in it were stopped", e.Memory)
}

type containerState struct {
	Running   bool
	OOMKilled bool
	StartedAt string
}

func (m *Manager) inspectState(ctx context.Context, name string) (containerState, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	out, err := m.runner.Run(ctx, m.cfg.Engine, "container", "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		return containerState{}, err
	}
	var state containerState
	err = json.Unmarshal(out, &state)
	return state, err
}

func sandboxDied(before, after containerState) bool {
	return !after.Running || after.OOMKilled || (before.StartedAt != "" && after.StartedAt != before.StartedAt)
}

func (m *Manager) prepareGoTemp(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, m.cfg.Engine, append([]string{"exec", name}, withSandboxUmask("mkdir", "-p", sandboxHome+"/.cache/go-tmp")...)...)
	if err != nil {
		return fmt.Errorf("prepare sandbox Go temporary directory: %w", err)
	}
	return nil
}

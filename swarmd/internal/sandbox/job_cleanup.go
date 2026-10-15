package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EndJob removes only the disposable container, never the shared home volume or
// checkout. Lifecycle leases refuse removal while a command is still in flight.
func (m *Manager) EndJob(ctx context.Context, checkout string) error {
	if m == nil || !m.Active() || checkout == "" {
		return nil
	}
	scope, ok, err := m.ScopeFor(checkout)
	if err != nil {
		return err
	}
	if !ok || scope.JobCheckout == "" || scope.ReviewCheckout != "" {
		return nil
	}
	return m.removeJobContainer(ctx, scopeContainerName(scope))
}

func (m *Manager) removeJobContainer(ctx context.Context, name string) error {
	m.mu.Lock()
	b := m.boxes[name]
	if b == nil {
		b = &box{name: name}
		m.boxes[name] = b
	}
	if b.inflight != 0 || b.changing != nil {
		m.mu.Unlock()
		return fmt.Errorf("job sandbox still has active commands")
	}
	b.changing = make(chan struct{})
	m.mu.Unlock()
	defer func() { m.mu.Lock(); b.spec = ""; close(b.changing); b.changing = nil; m.mu.Unlock() }()
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := m.runner.Run(cleanupCtx, m.cfg.Engine, "rm", "-f", name)
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no such container") {
		return fmt.Errorf("remove job sandbox: %w", err)
	}
	return nil
}

// ReconcileContainers discovers previous-process containers by the product
// label, not the in-memory lease map. active is derived from durable job state;
// unknown and terminal jobs are removed, live jobs are stopped until reacquired.
// Startup calls this before executor admission. It never removes volumes.
func (m *Manager) ReconcileContainers(ctx context.Context, active func(string) (bool, error)) error {
	if m == nil || !m.Active() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := m.runner.Run(ctx, m.cfg.Engine, "ps", "-a", "--filter", "label="+labelSandbox+"=1", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("discover sandbox containers: %w", err)
	}
	for _, name := range strings.Fields(string(out)) {
		if !strings.HasPrefix(name, "swarm-sandbox-") {
			continue
		}
		if strings.Contains(name, "-job-"+containerPrefix) {
			live, err := active(name)
			if err != nil {
				return err
			}
			if !live {
				if err = m.removeJobContainer(ctx, name); err != nil {
					return err
				}
				continue
			}
		}
		if _, err = m.runner.Run(ctx, m.cfg.Engine, "stop", "--time", "5", name); err != nil {
			return fmt.Errorf("stop startup sandbox: %w", err)
		}
	}
	return nil
}

// JobContainerName is used only to join durable checkout ownership to engine
// inventory during startup recovery.
func JobContainerName(root, checkout string) string {
	return scopeContainerName(Scope{Root: root, JobCheckout: checkout})
}

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ErrCleanupDeferred means no removal was confirmed; durable cleanup must retry.
var ErrCleanupDeferred = errors.New("sandbox cleanup deferred")

// EndJob removes only the disposable container, never the shared home volume or
// checkout. Lifecycle leases refuse removal while a command is still in flight.
func (m *Manager) EndJob(ctx context.Context, checkout string, terminal func() (bool, error)) error {
	if m == nil || !m.Active() || checkout == "" {
		return ErrCleanupDeferred
	}
	scope, ok, err := m.ScopeFor(checkout)
	if err != nil {
		return err
	}
	if !ok || scope.JobCheckout == "" || scope.ReviewCheckout != "" {
		return ErrCleanupDeferred
	}
	name := scopeContainerName(scope)
	m.mu.Lock()
	b := m.boxes[name]
	var generation uint64
	if b != nil {
		generation = b.generation
	}
	m.mu.Unlock()
	if terminal == nil {
		return fmt.Errorf("job cleanup requires durable terminal state")
	}
	done, err := terminal()
	if err != nil {
		return err
	}
	if !done {
		return ErrCleanupDeferred
	}
	return m.endJobGeneration(ctx, name, b, generation, terminal)
}

func (m *Manager) endJobGeneration(ctx context.Context, name string, b *box, generation uint64, terminal func() (bool, error)) error {
	m.mu.Lock()
	if b != nil && m.boxes[name] == b && b.generation == generation && b.inflight > 0 {
		b.cleanup = func() {
			done, err := terminal()
			if err == nil && done {
				err = m.endJobGeneration(context.Background(), name, b, generation, terminal)
			}
			if err != nil {
				slog.Warn("deferred sandbox cleanup failed", "error", err)
			}
		}
		m.mu.Unlock()
		return ErrCleanupDeferred
	}
	m.mu.Unlock()
	return m.removeJobContainerFenced(ctx, name, b, generation, true)
}

func (m *Manager) removeJobContainer(ctx context.Context, name string) error {
	return m.removeJobContainerFenced(ctx, name, nil, 0, false)
}

func (m *Manager) removeJobContainerFenced(ctx context.Context, name string, expected *box, generation uint64, fenced bool) error {
	m.mu.Lock()
	b := m.boxes[name]
	if fenced && (b != expected || (b != nil && b.generation != generation)) {
		m.mu.Unlock()
		return ErrCleanupDeferred
	}
	if b == nil {
		b = &box{name: name}
		m.boxes[name] = b
	}
	if b.inflight != 0 || b.preparing != 0 || b.changing != nil {
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
	m.mu.Lock()
	m.releaseProjectReferenceLocked(b)
	m.mu.Unlock()
	return nil
}

func (m *Manager) releaseProjectReferenceLocked(b *box) {
	root := b.root
	// boxes is the project registration reference set. Stopped containers
	// retain references; only successful removal releases one.
	b.root = ""
	remaining := false
	for _, other := range m.boxes {
		if other.root == root && root != "" {
			remaining = true
			break
		}
	}
	if !remaining && root != "" {
		if broker := m.cfg.SecretBroker; broker != nil {
			broker.ReleaseSandbox(root)
		}
	}
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
		inspection, err := m.runner.Run(ctx, m.cfg.Engine, "inspect", name)
		if err != nil {
			slog.Warn("sandbox reconciliation left container alone: ownership inspect failed", "container", name)
			continue
		}
		var items []instanceInspection
		if err := json.Unmarshal(inspection, &items); err != nil {
			slog.Warn("sandbox reconciliation left container alone: invalid ownership inspect", "container", name)
			continue
		}
		if len(items) != 1 || !ownsInstance(m.cfg.StateDir, items[0]) {
			continue
		}
		if strings.Contains(name, "-job-"+containerPrefix) || strings.Contains(name, "-review-") {
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

// SetTerminalCleanup installs the durable retry sweep before Start.
func (m *Manager) SetTerminalCleanup(fn func(context.Context)) { m.terminalCleanup = fn }

// EndTerminalReview removes the container independently of checkout cleanup.
func (m *Manager) EndTerminalReview(ctx context.Context, root, checkout string) error {
	m.mu.Lock()
	_, registered := m.reviews[checkout]
	m.mu.Unlock()
	if registered {
		return m.EndReview(ctx, checkout)
	}
	return m.removeJobContainer(ctx, ReviewContainerName(root, checkout))
}
func ReviewContainerName(root, checkout string) string {
	return scopeContainerName(Scope{Root: root, ReviewCheckout: checkout})
}

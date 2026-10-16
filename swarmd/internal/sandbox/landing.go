package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

// Landing is a fresh, daemon-owned sandbox lease. It never selects or replaces
// an agent's container. The mounted clone retains its original absolute layout.
type Landing struct {
	manager  *Manager
	scope    Scope
	mu       sync.Mutex
	released bool
}

func (m *Manager) AcquireLanding(ctx context.Context, folder, checkout string) (*Landing, error) {
	if m == nil || !m.Active() {
		return nil, ErrInactive
	}
	scope, err := m.CommandScope(checkout)
	if err != nil {
		return nil, err
	}
	if scope.Root != folder || scope.ReviewCheckout != "" {
		return nil, errors.New("landing checkout does not belong to folder")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	scope.landingID = hex.EncodeToString(id[:])
	l := &Landing{manager: m, scope: scope}
	b, err := m.acquire(ctx, scope)
	if err != nil {
		return nil, errors.Join(err, l.Release())
	}
	m.release(b)
	return l, nil
}

func (l *Landing) Exec(ctx context.Context, req ExecRequest) (*provider.ExecResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, errors.New("landing environment released")
	}
	return l.manager.Exec(ctx, l.scope, req)
}

func (l *Landing) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	m := l.manager
	name := scopeContainerName(l.scope)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "rm", "-f", name); err != nil {
		return err
	}
	m.mu.Lock()
	if b := m.boxes[name]; b != nil {
		m.releaseProjectReferenceLocked(b)
	}
	delete(m.boxes, name)
	m.mu.Unlock()
	if _, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "rm", homeVolumeName(name, l.scope.Root)); err != nil {
		return err
	}
	l.released = true
	return nil
}

package executioncapacity

import (
	"context"
	"sync"
)

type lease struct {
	id             string
	accountScopeID string
	sessionID      string
	runID          string
	kind           ExecutionKind
	m              *Manager

	mu       sync.Mutex
	parked   bool
	released bool
}

func (l *lease) ID() string {
	return l.id
}

func (l *lease) AccountScopeID() string {
	return l.accountScopeID
}

func (l *lease) SessionID() string {
	return l.sessionID
}

func (l *lease) RunID() string {
	return l.runID
}

func (l *lease) Kind() ExecutionKind {
	return l.kind
}

func (l *lease) IsActive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.released && !l.parked
}

func (l *lease) IsParked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.released && l.parked
}

func (l *lease) IsReleased() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released
}

func (l *lease) Park() error {
	return l.ParkWithContext(context.Background())
}

func (l *lease) ParkWithContext(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return l.m.parkLease(l)
}

func (l *lease) Reacquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.m.reacquireLease(ctx, l)
}

func (l *lease) Release() error {
	return l.m.releaseLease(l)
}

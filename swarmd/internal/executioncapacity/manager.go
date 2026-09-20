package executioncapacity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ManagerConfig configures an account-scoped execution capacity manager.
type ManagerConfig struct {
	DefaultLimit    int
	MaxQueueWaiters int
	LimitResolver   func(accountScopeID string) int
}

// Manager coordinates execution admission, per-session serialization,
// and account-scoped capacity limits without polling.
type Manager struct {
	defaultLimit    int
	maxQueueWaiters int
	limitResolver   func(accountScopeID string) int

	mu       sync.Mutex
	accounts map[string]*accountState
	counter  atomic.Uint64
	closed   bool
}

type accountState struct {
	accountScopeID string
	limitOverride  int

	totalActive    int
	deployedActive int

	// Session ownership: sessionID -> current lease holding the session (active or parked)
	sessions map[string]*lease

	// FIFO queue of waiting admissions or re-acquisitions
	waiters []*waiter
}

type waiter struct {
	id        string
	accountID string
	ctx       context.Context
	req       AcquireRequest
	reacquire *lease // non-nil if this waiter is for a parked lease reacquiring

	grant   chan struct{}
	granted bool
	lease   *lease
}

// NewManager creates a new account-scoped execution capacity manager.
func NewManager(cfg ManagerConfig) *Manager {
	defLimit := cfg.DefaultLimit
	if defLimit <= 0 {
		defLimit = DefaultActiveExecutionLimit
	}
	maxWaiters := cfg.MaxQueueWaiters
	if maxWaiters <= 0 {
		maxWaiters = DefaultMaxQueueWaiters
	}
	return &Manager{
		defaultLimit:    defLimit,
		maxQueueWaiters: maxWaiters,
		limitResolver:   cfg.LimitResolver,
		accounts:        make(map[string]*accountState),
	}
}

// Admit is an alias for Acquire.
func (m *Manager) Admit(ctx context.Context, req AcquireRequest) (Lease, error) {
	return m.Acquire(ctx, req)
}

// Acquire requests atomic execution admission for a session. It serializes same-session
// runs, respects the account execution limit, and enqueues if capacity is exhausted.
func (m *Manager) Acquire(ctx context.Context, req AcquireRequest) (Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req.AccountScopeID = strings.TrimSpace(req.AccountScopeID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.RunID = strings.TrimSpace(req.RunID)
	req.Kind = req.Kind.Normalized()

	if req.SessionID == "" {
		return nil, ErrSessionRequired
	}
	if req.RunID == "" {
		return nil, ErrRunRequired
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}

	acc := m.getAccountLocked(req.AccountScopeID)
	limit := m.effectiveLimitLocked(req.AccountScopeID)

	// Can we admit immediately?
	// Must have: no waiters ahead of us, session not busy/parked, and totalActive < limit.
	if len(acc.waiters) == 0 && acc.sessions[req.SessionID] == nil && acc.totalActive < limit {
		l := m.newLeaseLocked(acc, req)
		acc.sessions[req.SessionID] = l
		acc.totalActive++
		if l.kind == ExecutionKindDeployed {
			acc.deployedActive++
		}
		m.mu.Unlock()
		return l, nil
	}

	// Must enqueue waiter. Check queue bounds.
	if len(acc.waiters) >= m.maxQueueWaiters {
		m.mu.Unlock()
		return nil, ErrQueueFull
	}

	w := &waiter{
		id:        m.nextWaiterIDLocked(),
		accountID: req.AccountScopeID,
		ctx:       ctx,
		req:       req,
		grant:     make(chan struct{}),
	}
	acc.waiters = append(acc.waiters, w)
	m.mu.Unlock()

	select {
	case <-w.grant:
		if ctx.Err() != nil {
			// Grant raced with context cancellation:
			// Release granted lease immediately to prevent slot/session leak.
			_ = w.lease.Release()
			return nil, ctx.Err()
		}
		return w.lease, nil

	case <-ctx.Done():
		m.mu.Lock()
		if w.granted {
			m.mu.Unlock()
			_ = w.lease.Release()
			return nil, ctx.Err()
		}
		m.removeWaiterLocked(acc, w)
		m.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Release idempotently releases a lease.
func (m *Manager) Release(l Lease) error {
	if l == nil {
		return nil
	}
	return l.Release()
}

// SetLimit updates the active execution limit override for an account and wakes eligible waiters.
func (m *Manager) SetLimit(accountScopeID string, limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	acc := m.getAccountLocked(accountScopeID)
	if limit > 0 {
		acc.limitOverride = limit
	} else {
		acc.limitOverride = 0
	}
	m.wakeEligibleWaitersLocked(acc)
}

// NotifyLimitChanged re-evaluates the limit for an account and wakes eligible waiters.
func (m *Manager) NotifyLimitChanged(accountScopeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	acc := m.getAccountLocked(accountScopeID)
	m.wakeEligibleWaitersLocked(acc)
}

// Snapshot returns the atomic capacity snapshot for an account.
func (m *Manager) Snapshot(accountScopeID string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.TrimSpace(accountScopeID)
	limit := m.effectiveLimitLocked(key)
	acc := m.getAccountLocked(key)

	avail := limit - acc.totalActive
	if avail < 0 {
		avail = 0
	}

	return Snapshot{
		AccountScopeID:       key,
		EffectiveLimit:       limit,
		TotalActive:          acc.totalActive,
		DeployedActive:       acc.deployedActive,
		Pending:              len(acc.waiters),
		Available:            avail,
		DeploymentBatchBound: DeploymentBatchBound,
		SavedQuota:           SavedQuotaNoneConfigured,
	}
}

// TotalActive returns the count of active executions for an account.
func (m *Manager) TotalActive(accountScopeID string) int {
	return m.Snapshot(accountScopeID).TotalActive
}

// DeployedActive returns the count of active deployed executions for an account.
func (m *Manager) DeployedActive(accountScopeID string) int {
	return m.Snapshot(accountScopeID).DeployedActive
}

// Pending returns the count of pending admission requests for an account.
func (m *Manager) Pending(accountScopeID string) int {
	return m.Snapshot(accountScopeID).Pending
}

// Available returns the count of available execution slots for an account.
func (m *Manager) Available(accountScopeID string) int {
	return m.Snapshot(accountScopeID).Available
}

// EffectiveLimit returns the current active execution limit for an account.
func (m *Manager) EffectiveLimit(accountScopeID string) int {
	return m.Snapshot(accountScopeID).EffectiveLimit
}

// Close closes the manager and signals any waiting admissions.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, acc := range m.accounts {
		for _, w := range acc.waiters {
			close(w.grant)
		}
		acc.waiters = nil
	}
}

func (m *Manager) parkLease(l *lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return ErrLeaseReleased
	}
	if l.parked {
		l.mu.Unlock()
		return ErrLeaseAlreadyParked
	}
	l.parked = true
	l.mu.Unlock()

	acc := m.getAccountLocked(l.accountScopeID)
	acc.totalActive--
	if l.kind == ExecutionKindDeployed {
		acc.deployedActive--
	}
	// Session ownership acc.sessions[l.sessionID] remains held by l!
	m.wakeEligibleWaitersLocked(acc)
	return nil
}

func (m *Manager) reacquireLease(ctx context.Context, l *lease) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}

	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		m.mu.Unlock()
		return ErrLeaseReleased
	}
	if !l.parked {
		l.mu.Unlock()
		m.mu.Unlock()
		return ErrLeaseNotParked
	}
	l.mu.Unlock()

	acc := m.getAccountLocked(l.accountScopeID)
	limit := m.effectiveLimitLocked(l.accountScopeID)

	// Can reacquire immediately if no waiters in queue and totalActive < limit
	if len(acc.waiters) == 0 && acc.totalActive < limit {
		l.mu.Lock()
		l.parked = false
		l.mu.Unlock()

		acc.totalActive++
		if l.kind == ExecutionKindDeployed {
			acc.deployedActive++
		}
		m.mu.Unlock()
		return nil
	}

	// Must enqueue waiter
	if len(acc.waiters) >= m.maxQueueWaiters {
		m.mu.Unlock()
		return ErrQueueFull
	}

	w := &waiter{
		id:        m.nextWaiterIDLocked(),
		accountID: l.accountScopeID,
		ctx:       ctx,
		reacquire: l,
		grant:     make(chan struct{}),
	}
	acc.waiters = append(acc.waiters, w)
	m.mu.Unlock()

	select {
	case <-w.grant:
		if ctx.Err() != nil {
			// Grant raced with context cancellation.
			// Return active slot back to pool and stay parked!
			m.mu.Lock()
			l.mu.Lock()
			l.parked = true
			l.mu.Unlock()

			acc.totalActive--
			if l.kind == ExecutionKindDeployed {
				acc.deployedActive--
			}
			m.wakeEligibleWaitersLocked(acc)
			m.mu.Unlock()
			return ctx.Err()
		}
		return nil

	case <-ctx.Done():
		m.mu.Lock()
		if w.granted {
			// Raced with grant
			l.mu.Lock()
			l.parked = true
			l.mu.Unlock()

			acc.totalActive--
			if l.kind == ExecutionKindDeployed {
				acc.deployedActive--
			}
			m.wakeEligibleWaitersLocked(acc)
			m.mu.Unlock()
			return ctx.Err()
		}
		m.removeWaiterLocked(acc, w)
		m.mu.Unlock()
		return ctx.Err()
	}
}

func (m *Manager) releaseLease(l *lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil // idempotent!
	}
	wasParked := l.parked
	l.released = true
	l.parked = false
	l.mu.Unlock()

	acc := m.getAccountLocked(l.accountScopeID)

	// Release session ownership
	if acc.sessions[l.sessionID] == l {
		delete(acc.sessions, l.sessionID)
	}

	// Release active execution slot if not previously parked
	if !wasParked {
		acc.totalActive--
		if l.kind == ExecutionKindDeployed {
			acc.deployedActive--
		}
	}

	m.wakeEligibleWaitersLocked(acc)
	return nil
}

func (m *Manager) wakeEligibleWaitersLocked(acc *accountState) {
	if len(acc.waiters) == 0 {
		return
	}
	limit := m.effectiveLimitLocked(acc.accountScopeID)

	i := 0
	for i < len(acc.waiters) && acc.totalActive < limit {
		w := acc.waiters[i]

		// Drop already-cancelled waiters
		if w.ctx != nil && w.ctx.Err() != nil {
			acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
			continue
		}

		// 1. Reacquiring waiter:
		if w.reacquire != nil {
			// Already owns the session. Only needs capacity slot.
			w.granted = true
			w.reacquire.mu.Lock()
			w.reacquire.parked = false
			w.reacquire.mu.Unlock()

			acc.totalActive++
			if w.reacquire.kind == ExecutionKindDeployed {
				acc.deployedActive++
			}
			w.lease = w.reacquire

			acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
			close(w.grant)
			continue
		}

		// 2. New acquire waiter:
		if acc.sessions[w.req.SessionID] == nil {
			// Session is free and capacity slot is available.
			w.granted = true
			l := m.newLeaseLocked(acc, w.req)
			acc.sessions[w.req.SessionID] = l

			acc.totalActive++
			if l.kind == ExecutionKindDeployed {
				acc.deployedActive++
			}
			w.lease = l

			acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
			close(w.grant)
			continue
		}

		// Session is busy (active or parked by someone else), so this waiter cannot run yet.
		// Advance to next waiter in line so we don't stall unrelated sessions!
		i++
	}
}

func (m *Manager) removeWaiterLocked(acc *accountState, target *waiter) {
	for i, w := range acc.waiters {
		if w == target {
			acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
			return
		}
	}
}

func (m *Manager) getAccountLocked(accountScopeID string) *accountState {
	key := strings.TrimSpace(accountScopeID)
	acc, ok := m.accounts[key]
	if !ok {
		acc = &accountState{
			accountScopeID: key,
			sessions:       make(map[string]*lease),
			waiters:        make([]*waiter, 0),
		}
		m.accounts[key] = acc
	}
	return acc
}

func (m *Manager) effectiveLimitLocked(accountScopeID string) int {
	key := strings.TrimSpace(accountScopeID)
	if acc, ok := m.accounts[key]; ok && acc.limitOverride > 0 {
		return acc.limitOverride
	}
	if m.limitResolver != nil {
		if lim := m.limitResolver(key); lim > 0 {
			return lim
		}
	}
	return m.defaultLimit
}

func (m *Manager) newLeaseLocked(acc *accountState, req AcquireRequest) *lease {
	seq := m.counter.Add(1)
	return &lease{
		id:             fmt.Sprintf("lease_%d", seq),
		accountScopeID: req.AccountScopeID,
		sessionID:      req.SessionID,
		runID:          req.RunID,
		kind:           req.Kind,
		m:              m,
	}
}

func (m *Manager) nextWaiterIDLocked() string {
	seq := m.counter.Add(1)
	return fmt.Sprintf("waiter_%d", seq)
}

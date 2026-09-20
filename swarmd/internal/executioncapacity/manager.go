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
	LimitResolver   func(accountScopeID string) (int, error)
}

// Manager coordinates execution admission, per-session serialization,
// and account-scoped capacity limits without polling.
type Manager struct {
	defaultLimit    int
	maxQueueWaiters int
	limitResolver   func(accountScopeID string) (int, error)

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
	limit, err := m.effectiveLimitLocked(req.AccountScopeID)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("execution capacity limit resolution failed: %w", err)
	}

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
	// Dispatch immediately on enqueue so unrelated free sessions or eligible waiters don't stall
	m.wakeEligibleWaitersLocked(acc)
	m.mu.Unlock()

	select {
	case <-w.grant:
		if !w.granted {
			return nil, ErrClosed
		}
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
		m.wakeEligibleWaitersLocked(acc)
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

// ClearLimitOverride clears any limit override for an account.
func (m *Manager) ClearLimitOverride(accountScopeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	acc := m.getAccountLocked(accountScopeID)
	acc.limitOverride = 0
	m.wakeEligibleWaitersLocked(acc)
}

// NotifyLimitChanged clears any stale limit override, re-evaluates the limit for an account, and wakes eligible waiters.
func (m *Manager) NotifyLimitChanged(accountScopeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	acc := m.getAccountLocked(accountScopeID)
	if m.limitResolver != nil {
		acc.limitOverride = 0
	}
	m.wakeEligibleWaitersLocked(acc)
}

// Snapshot returns the atomic capacity snapshot for an account.
func (m *Manager) Snapshot(accountScopeID string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.TrimSpace(accountScopeID)
	acc := m.getAccountLocked(key)
	limit, err := m.effectiveLimitLocked(key)
	if err != nil {
		return Snapshot{
			AccountScopeID:       key,
			EffectiveLimit:       0,
			TotalActive:          acc.totalActive,
			DeployedActive:       acc.deployedActive,
			Pending:              len(acc.waiters),
			Available:            0,
			DeploymentBatchBound: DeploymentBatchBound,
			SavedQuota:           SavedQuotaNoneConfigured,
			Unavailable:          true,
			Error:                err.Error(),
		}
	}

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

// ExecutionCapacitySnapshot returns the atomic capacity snapshot for an account, implementing manageSessionCapacityProvider.
func (m *Manager) ExecutionCapacitySnapshot(accountScopeID string) Snapshot {
	return m.Snapshot(accountScopeID)
}

// ActiveLeaseForSession returns the current active (non-parked, non-released) lease
// for the given account and session, if any.
func (m *Manager) ActiveLeaseForSession(accountScopeID, sessionID string) Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc := m.accounts[strings.TrimSpace(accountScopeID)]
	if acc == nil {
		return nil
	}
	l := acc.sessions[strings.TrimSpace(sessionID)]
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.parked {
		return nil
	}
	return l
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

// Close closes the manager and signals any waiting admissions with ErrClosed.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, acc := range m.accounts {
		for _, w := range acc.waiters {
			w.granted = false
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
	if l.reacquiring {
		l.mu.Unlock()
		m.mu.Unlock()
		return ErrLeaseAlreadyReacquiring
	}
	l.reacquiring = true
	l.mu.Unlock()

	acc := m.getAccountLocked(l.accountScopeID)
	limit, err := m.effectiveLimitLocked(l.accountScopeID)
	if err != nil {
		l.mu.Lock()
		l.reacquiring = false
		l.mu.Unlock()
		m.mu.Unlock()
		return fmt.Errorf("execution capacity limit resolution failed: %w", err)
	}

	// Can reacquire immediately if no waiters in queue and totalActive < limit
	if len(acc.waiters) == 0 && acc.totalActive < limit {
		l.mu.Lock()
		l.parked = false
		l.reacquiring = false
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
		l.mu.Lock()
		l.reacquiring = false
		l.mu.Unlock()
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
	// Dispatch immediately on enqueue so reacquire behind own-session blocked waiter succeeds
	m.wakeEligibleWaitersLocked(acc)
	m.mu.Unlock()

	select {
	case <-w.grant:
		if !w.granted {
			l.mu.Lock()
			l.reacquiring = false
			wasReleased := l.released
			l.mu.Unlock()
			if wasReleased {
				return ErrLeaseReleased
			}
			return ErrClosed
		}
		if ctx.Err() != nil {
			// Grant raced with context cancellation.
			// Return active slot back to pool and stay parked!
			m.mu.Lock()
			l.mu.Lock()
			l.parked = true
			l.reacquiring = false
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
			l.mu.Lock()
			l.parked = true
			l.reacquiring = false
			l.mu.Unlock()

			acc.totalActive--
			if l.kind == ExecutionKindDeployed {
				acc.deployedActive--
			}
			m.wakeEligibleWaitersLocked(acc)
			m.mu.Unlock()
			return ctx.Err()
		}
		l.mu.Lock()
		l.reacquiring = false
		l.mu.Unlock()
		m.removeWaiterLocked(acc, w)
		m.wakeEligibleWaitersLocked(acc)
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
	l.reacquiring = false
	l.mu.Unlock()

	acc := m.getAccountLocked(l.accountScopeID)

	// Remove any pending reacquire waiter for this lease
	for i := 0; i < len(acc.waiters); i++ {
		w := acc.waiters[i]
		if w.reacquire == l {
			acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
			w.granted = false
			close(w.grant)
			i--
		}
	}

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
		if acc.totalActive < 0 {
			acc.totalActive = 0
		}
		if acc.deployedActive < 0 {
			acc.deployedActive = 0
		}
	}

	m.wakeEligibleWaitersLocked(acc)
	return nil
}

func (m *Manager) wakeEligibleWaitersLocked(acc *accountState) {
	if len(acc.waiters) == 0 {
		return
	}
	limit, err := m.effectiveLimitLocked(acc.accountScopeID)
	if err != nil || limit <= 0 {
		// Fail closed: cannot admit any waiters when limit resolution fails
		return
	}

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
			w.reacquire.mu.Lock()
			if w.reacquire.released {
				w.reacquire.mu.Unlock()
				acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
				continue
			}
			w.granted = true
			w.reacquire.parked = false
			w.reacquire.reacquiring = false
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

func (m *Manager) effectiveLimitLocked(accountScopeID string) (int, error) {
	key := strings.TrimSpace(accountScopeID)
	if acc, ok := m.accounts[key]; ok && acc.limitOverride > 0 {
		return acc.limitOverride, nil
	}
	if m.limitResolver != nil {
		lim, err := m.limitResolver(key)
		if err != nil {
			return 0, err
		}
		if lim > 0 {
			return lim, nil
		}
	}
	return m.defaultLimit, nil
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

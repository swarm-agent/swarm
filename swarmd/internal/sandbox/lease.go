package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

// Leases: an agent's bash runs in an instance of its folder's environment.
// Swarm takes the first lease for the agent on its first bash command (the
// tool package's autoLease); the agent may take more, up to the folder's
// maximum. Each lease is its own
// container of the folder's environment image, with the folder mounted at its
// own path (so the agent's file tools on the host and bash in the lease see
// the same files) and the folder's granted secrets as stand-ins. The agent
// never builds or cleans up anything: when its run ends, Swarm releases every
// lease it holds. Leases work whatever the sandbox setting is.

const (
	labelLease      = "swarm.lease"
	labelLeaseOwner = "swarm.lease.owner"
	// LeaseReapInterval is how often leases whose agent no longer runs are released.
	LeaseReapInterval = 15 * time.Second
)

// Lease is one leased instance of a folder's environment.
type Lease struct {
	ID        string    `json:"id"`
	Root      string    `json:"folder"`
	Owner     string    `json:"-"`
	Container string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	scope     Scope
}

// LeaseLimit reports how many leases a folder allows. A folder without an
// environment, or whose environment is not ready, returns an error saying so.
type LeaseLimit func(root string) (int, error)

// LeaseOwnerRunning reports whether a lease owner (an agent session) still runs.
type LeaseOwnerRunning func(owner string) (bool, error)

// LeasesFullError says every lease the folder allows is in use.
type LeasesFullError struct {
	Root  string
	Max   int
	InUse int
}

func (e *LeasesFullError) Error() string {
	return fmt.Sprintf("all %d leases of %s are in use; release one or wait until another agent's run ends", e.Max, e.Root)
}

type leaseState struct {
	mu      sync.Mutex
	byID    map[string]*Lease
	limit   LeaseLimit
	running LeaseOwnerRunning
	started bool
}

var (
	leaseMu      sync.RWMutex
	leaseManager *Manager
)

// SetLeases installs the manager that runs leases (nil turns leases off).
func SetLeases(m *Manager) {
	leaseMu.Lock()
	leaseManager = m
	leaseMu.Unlock()
}

// Leases returns the manager that runs leases, or nil when leases are unavailable.
func Leases() *Manager {
	leaseMu.RLock()
	defer leaseMu.RUnlock()
	return leaseManager
}

func (m *Manager) leaseTable() *leaseState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leases == nil {
		m.leases = &leaseState{byID: map[string]*Lease{}}
	}
	return m.leases
}

// SetLeasePolicy installs the folder maximum and the owner liveness check.
func (m *Manager) SetLeasePolicy(limit LeaseLimit, running LeaseOwnerRunning) {
	t := m.leaseTable()
	t.mu.Lock()
	t.limit, t.running = limit, running
	t.mu.Unlock()
}

func leaseContainerName(root, id string) string {
	return ContainerName(root) + "-lease-" + id
}

func newLeaseID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// leaseLabels returns the labels a lease container is created with.
// Callers hold m.mu.
func (m *Manager) leaseLabelsLocked(name string) []string {
	t := m.leases
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range t.byID {
		if l.Container == name {
			return []string{"--label", labelLease + "=" + l.ID, "--label", labelLeaseOwner + "=" + l.Owner}
		}
	}
	return nil
}

// TakeLease starts a new lease of path's folder environment for owner.
func (m *Manager) TakeLease(ctx context.Context, path, owner string) (Lease, error) {
	if m == nil {
		return Lease{}, errors.New("leases need Docker on this machine")
	}
	if m.Local() || !m.Active() {
		return Lease{}, errors.New("leases need Docker on this machine: " + m.Status().Reason)
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Lease{}, errors.New("a lease needs the agent's session")
	}
	scope, err := m.CommandScope(path)
	if err != nil {
		return Lease{}, err
	}
	scope.JobCheckout, scope.ReviewCheckout = "", ""
	root := filepath.Clean(scope.Root)
	t := m.leaseTable()
	t.mu.Lock()
	limit := t.limit
	t.mu.Unlock()
	if limit == nil {
		return Lease{}, errors.New("leases are not configured on this machine")
	}
	max, err := limit(root)
	if err != nil {
		return Lease{}, err
	}
	if max <= 0 {
		return Lease{}, fmt.Errorf("no leases are set up for %s: the person's AI sets max_leases with swarm_set_environment", root)
	}
	id, err := newLeaseID()
	if err != nil {
		return Lease{}, err
	}
	lease := &Lease{ID: id, Root: root, Owner: owner, Container: leaseContainerName(root, id), CreatedAt: time.Now(), scope: scope}
	lease.scope.Lease = id
	t.mu.Lock()
	inUse := 0
	for _, l := range t.byID {
		if l.Root == root {
			inUse++
		}
	}
	if inUse >= max {
		t.mu.Unlock()
		return Lease{}, &LeasesFullError{Root: root, Max: max, InUse: inUse}
	}
	t.byID[id] = lease
	t.mu.Unlock()
	b, err := m.acquire(ctx, lease.scope, "lease")
	if err != nil {
		m.dropLease(context.WithoutCancel(ctx), id)
		return Lease{}, fmt.Errorf("start lease: %w", err)
	}
	m.release(b)
	return *lease, nil
}

// LeaseInfo is a folder's lease maximum and how many are in use.
type LeaseInfo struct {
	Root  string
	Max   int
	InUse int
}

// LeaseInfo reports the lease maximum and use of path's folder.
func (m *Manager) LeaseInfo(path string) (LeaseInfo, error) {
	if m == nil {
		return LeaseInfo{}, errors.New("leases need Docker on this machine")
	}
	scope, err := m.CommandScope(path)
	if err != nil {
		return LeaseInfo{}, err
	}
	root := filepath.Clean(scope.Root)
	t := m.leaseTable()
	t.mu.Lock()
	limit := t.limit
	t.mu.Unlock()
	if limit == nil {
		return LeaseInfo{}, errors.New("leases are not configured on this machine")
	}
	max, err := limit(root)
	if err != nil {
		return LeaseInfo{}, err
	}
	return LeaseInfo{Root: root, Max: max, InUse: m.LeasesInUse(root)}, nil
}

// OwnerLeases lists owner's leases, oldest first.
func (m *Manager) OwnerLeases(owner string) []Lease {
	if m == nil {
		return nil
	}
	t := m.leaseTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Lease
	for _, l := range t.byID {
		if l.Owner == owner {
			out = append(out, *l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// LeasesInUse counts the leases of a folder.
func (m *Manager) LeasesInUse(root string) int {
	if m == nil {
		return 0
	}
	root = filepath.Clean(root)
	t := m.leaseTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, l := range t.byID {
		if l.Root == root {
			n++
		}
	}
	return n
}

func (m *Manager) ownedLease(id, owner string) (*Lease, error) {
	t := m.leaseTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.byID[strings.TrimSpace(id)]
	if l == nil || l.Owner != owner {
		return nil, fmt.Errorf("no lease %q is held by this agent", id)
	}
	return l, nil
}

// ExecLease runs req in owner's lease id.
func (m *Manager) ExecLease(ctx context.Context, id, owner string, req ExecRequest) (*provider.ExecResult, error) {
	l, err := m.ownedLease(id, owner)
	if err != nil {
		return nil, err
	}
	result, err := m.execOnce(ctx, l.scope, req)
	if errors.Is(err, provider.ErrSupervisorUnavailable) && ctx.Err() == nil {
		// The container went away (stopped or removed outside Swarm): nothing
		// ran. Start it again and retry once.
		m.invalidate(l.Container)
		result, err = m.execOnce(ctx, l.scope, req)
	}
	return result, err
}

// ReleaseLease releases one of owner's leases.
func (m *Manager) ReleaseLease(ctx context.Context, id, owner string) error {
	if _, err := m.ownedLease(id, owner); err != nil {
		return err
	}
	return m.dropLease(ctx, id)
}

// ReleaseOwner releases every lease owner holds.
func (m *Manager) ReleaseOwner(ctx context.Context, owner string) error {
	var errs []error
	for _, l := range m.OwnerLeases(owner) {
		if err := m.dropLease(ctx, l.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropLease removes the lease's container and its home volume. The folder's
// shared project cache stays.
func (m *Manager) dropLease(ctx context.Context, id string) error {
	t := m.leaseTable()
	t.mu.Lock()
	l := t.byID[id]
	t.mu.Unlock()
	if l == nil {
		return nil
	}
	rmCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := m.runner.Run(rmCtx, m.cfg.Engine, "rm", "-f", l.Container); err != nil && !strings.Contains(err.Error(), "No such container") {
		return fmt.Errorf("release lease %s: %w", id, err)
	}
	_, _ = m.runner.Run(rmCtx, m.cfg.Engine, "volume", "rm", "-f", homeVolumeName(l.Container, l.Root))
	m.mu.Lock()
	delete(m.boxes, l.Container)
	m.mu.Unlock()
	t.mu.Lock()
	delete(t.byID, id)
	t.mu.Unlock()
	return nil
}

// recoverLeases rebuilds the lease table from this instance's lease
// containers (after a daemon restart), so the reaper can release the ones
// whose agent no longer runs.
func (m *Manager) recoverLeases(ctx context.Context) error {
	out, err := m.runner.Run(ctx, m.cfg.Engine, "ps", "-a",
		"--filter", "label="+labelLease,
		"--filter", "label="+labelInstance+"="+instanceIdentity(m.cfg.StateDir),
		"--format", "{{.Names}}\t{{.Label \""+labelLease+"\"}}\t{{.Label \""+labelLeaseOwner+"\"}}\t{{.Label \""+labelRoot+"\"}}")
	if err != nil {
		return err
	}
	t := m.leaseTable()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || f[1] == "" || f[3] == "" {
			continue
		}
		l := &Lease{ID: f[1], Owner: f[2], Root: filepath.Clean(f[3]), Container: f[0]}
		if scope, err := m.CommandScope(l.Root); err == nil {
			scope.JobCheckout, scope.ReviewCheckout, scope.Lease = "", "", l.ID
			l.scope = scope
		} else {
			l.scope = Scope{Root: l.Root, Mounts: []string{l.Root}, Lease: l.ID}
		}
		t.mu.Lock()
		if _, ok := t.byID[l.ID]; !ok {
			t.byID[l.ID] = l
		}
		t.mu.Unlock()
	}
	return nil
}

// reapLeases releases the leases of owners that no longer run.
func (m *Manager) reapLeases(ctx context.Context) {
	t := m.leaseTable()
	t.mu.Lock()
	running := t.running
	owners := map[string]bool{}
	for _, l := range t.byID {
		owners[l.Owner] = true
	}
	t.mu.Unlock()
	if running == nil {
		return
	}
	for owner := range owners {
		alive, err := running(owner)
		if err != nil || alive {
			continue
		}
		if err := m.ReleaseOwner(ctx, owner); err != nil {
			log.Printf("swarmd release leases of a finished agent: %v", err)
		}
	}
}

// StartLeases recovers leases left by an earlier process and releases leases
// whose agent no longer runs, every LeaseReapInterval until ctx ends.
func (m *Manager) StartLeases(ctx context.Context) {
	if m == nil || m.Local() || !m.Active() {
		return
	}
	t := m.leaseTable()
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.mu.Unlock()
	if err := m.recoverLeases(ctx); err != nil {
		log.Printf("swarmd recover leases: %v", err)
	}
	m.reapLeases(ctx)
	go func() {
		ticker := time.NewTicker(LeaseReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-ticker.C:
				m.reapLeases(ctx)
			}
		}
	}()
}

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
	labelLease       = "swarm.lease"
	labelLeaseOwner  = "swarm.lease.owner"
	labelLeaseUser   = "swarm.lease.user"
	labelLeaseReview = "swarm.lease.review"
	// labelLeaseStep marks a workflow run step's lease (TakeStepLease).
	labelLeaseStep = "swarm.lease.step"
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
	// asDaemon runs the lease as Swarm's own user: the agent works in a
	// directory Swarm's user owns (its worktree) in a folder that belongs to
	// someone else, so the folder owner could not open it.
	asDaemon bool
	// step is a workflow run step's lease (TakeStepLease): every project
	// and worktree mount is read-only, and it is held only while the step
	// runs in this process.
	step bool
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
	// liveSteps are the owners of step leases whose step runs now; the
	// reaper releases any other step lease (one left by an earlier process).
	liveSteps map[string]bool
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
			labels := []string{"--label", labelLease + "=" + l.ID, "--label", labelLeaseOwner + "=" + l.Owner}
			if l.scope.ReviewCheckout != "" {
				labels = append(labels, "--label", labelLeaseReview+"="+l.scope.ReviewCheckout)
			}
			if l.asDaemon {
				labels = append(labels, "--label", labelLeaseUser+"=daemon")
			}
			if l.step {
				labels = append(labels, "--label", labelLeaseStep+"=1")
			}
			return labels
		}
	}
	return nil
}

// TakeLease starts a new lease of path's folder environment for owner.
func (m *Manager) TakeLease(ctx context.Context, path, owner string) (Lease, error) {
	return m.takeLease(ctx, path, owner, false)
}

// TakeStepLease starts a lease of folder's environment for a workflow run
// step (owner names the step's run, not an agent session). It is the lease
// agents' bash runs in (the folder's environment image, isolation, lease
// user, network policy and granted secret stand-ins), except that the folder
// and its worktree bucket are mounted read-only: the step works in a
// checkout of its own in the lease's home, and nothing it does reaches the
// folder's repository or a job's worktree. The reaper keeps it while the
// step runs in this process and releases it otherwise (after a restart).
// A full folder returns *LeasesFullError, like TakeLease.
func (m *Manager) TakeStepLease(ctx context.Context, folder, owner string) (Lease, error) {
	return m.takeLease(ctx, folder, owner, true)
}

func (m *Manager) takeLease(ctx context.Context, path, owner string, step bool) (Lease, error) {
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
	if checkout := ReviewSessionCheckout(owner); checkout != "" && !step {
		if !BorrowedReviewSession(owner) {
			return Lease{}, errors.New("a disposable review cannot lease an environment")
		}
		reviewCtx, bindErr := WithReviewSession(ctx, owner, checkout)
		if bindErr != nil {
			return Lease{}, bindErr
		}
		scope, err = m.enforceReviewIdentity(reviewCtx, scope, path)
		if err != nil {
			return Lease{}, err
		}
	}
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
	lease := &Lease{ID: id, Root: root, Owner: owner, Container: leaseContainerName(root, id), CreatedAt: time.Now(), scope: scope, step: step}
	lease.asDaemon = m.leaseRunsAsDaemon(root, path)
	lease.scope.Lease = id
	t.mu.Lock()
	if step {
		for _, l := range t.byID {
			if l.Owner == owner {
				t.mu.Unlock()
				return Lease{}, fmt.Errorf("the step %s already holds a lease", owner)
			}
		}
	}
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
	if step {
		if t.liveSteps == nil {
			t.liveSteps = map[string]bool{}
		}
		t.liveSteps[owner] = true
	}
	t.mu.Unlock()
	b, err := m.acquire(ctx, lease.scope, "lease")
	if err != nil {
		m.dropLease(context.WithoutCancel(ctx), id)
		return Lease{}, fmt.Errorf("start lease: %w", err)
	}
	m.release(b)
	return *lease, nil
}

// EnvironmentsOffMessage is the answer when this machine cannot run
// environments: they need Docker, which a headless install adds only when
// the person turns environments on with its installer.
const EnvironmentsOffMessage = "environments are off on this machine (they need Docker): the person turns them on with: sudo bash /var/lib/swarm-headless/install.sh environments on"

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
	if checkout := ReviewSessionCheckout(owner); checkout != "" && l.scope.ReviewCheckout != checkout {
		return nil, errors.New("review lease lacks read-only mounts; release it and take a new lease")
	}
	// The lease runs as the folder's owner, while the agent's worktree and
	// bucket belong to Swarm's user (and the folder may belong to the person):
	// Git in the lease needs safe.directory for each of them, by exact path.
	safe := append([]string(nil), l.scope.Mounts...)
	safe = append(safe, filepath.Join(l.Root, ".git"))
	if req.WorkingDir != "" {
		safe = append(safe, filepath.Clean(req.WorkingDir))
	}
	req.Env = safeDirectoryEnvMap(req.Env, safe)
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
// shared project cache stays. A step lease's step has ended once its lease
// is dropped, whatever the removal's result: its owner leaves liveSteps
// first, so if the removal fails the reaper retries it instead of keeping
// the lease (and one of the folder's slots) until a restart.
func (m *Manager) dropLease(ctx context.Context, id string) error {
	t := m.leaseTable()
	t.mu.Lock()
	l := t.byID[id]
	if l != nil && l.step {
		delete(t.liveSteps, l.Owner)
	}
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
		"--format", "{{.Names}}\t{{.Label \""+labelLease+"\"}}\t{{.Label \""+labelLeaseOwner+"\"}}\t{{.Label \""+labelRoot+"\"}}\t{{.Label \""+labelLeaseUser+"\"}}\t{{.Label \""+labelLeaseReview+"\"}}\t{{.Label \""+labelLeaseStep+"\"}}")
	if err != nil {
		return err
	}
	t := m.leaseTable()
	var errs []error
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[1] == "" || f[3] == "" {
			continue
		}
		l := &Lease{ID: f[1], Owner: f[2], Root: filepath.Clean(f[3]), Container: f[0], asDaemon: len(f) > 4 && f[4] == "daemon", step: len(f) > 6 && f[6] == "1"}
		if scope, err := m.CommandScope(l.Root); err == nil {
			scope.JobCheckout, scope.ReviewCheckout, scope.Lease = "", "", l.ID
			l.scope = scope
		} else {
			l.scope = Scope{Root: l.Root, Mounts: []string{l.Root}, Lease: l.ID}
		}
		// The immutable container label preserves confinement even before session
		// registrations are restored. Never downgrade a recovered review lease.
		if len(f) > 5 && f[5] != "" {
			checkout := resolveExisting(f[5])
			if _, err := reviewGitMounts(checkout, l.Root); err != nil {
				// Keep failed removals accounted for and reapable, never reusable
				// as an ordinary writable lease. One stale row must not hide others.
				l.scope.ReviewCheckout = checkout
				t.mu.Lock()
				t.byID[l.ID] = l
				t.mu.Unlock()
				if dropErr := m.dropLease(ctx, l.ID); dropErr != nil {
					errs = append(errs, fmt.Errorf("recover stale review lease: %w", dropErr))
				}
				log.Printf("swarmd stale review lease: %v", err)
				continue
			}
			l.scope.ReviewCheckout = checkout
		}
		t.mu.Lock()
		if _, ok := t.byID[l.ID]; !ok {
			t.byID[l.ID] = l
		}
		t.mu.Unlock()
	}
	return errors.Join(errs...)
}

// reapLeases releases the leases of owners that no longer run.
func (m *Manager) reapLeases(ctx context.Context) {
	t := m.leaseTable()
	t.mu.Lock()
	running := t.running
	owners := map[string]bool{}
	var deadSteps []string
	for _, l := range t.byID {
		if l.step {
			// A step lease is held only by its step running in this
			// process; its owner is no agent session.
			if !t.liveSteps[l.Owner] {
				deadSteps = append(deadSteps, l.ID)
			}
			continue
		}
		owners[l.Owner] = true
	}
	t.mu.Unlock()
	for _, id := range deadSteps {
		if err := m.dropLease(ctx, id); err != nil {
			log.Printf("swarmd release a finished workflow step's lease: %v", err)
		}
	}
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

// leaseRunsAsDaemon reports whether a lease for an agent working at path must
// run as Swarm's own user: the folder's sandbox runs as its owner, but path
// (the agent's worktree) belongs to Swarm's user, which the owner cannot open.
func (m *Manager) leaseRunsAsDaemon(root, path string) bool {
	folderUID, _ := m.sandboxUser(root)
	if folderUID == m.cfg.UID {
		return false
	}
	owner := m.owner
	if owner == nil {
		owner = fileOwnerUID
	}
	uid, ok := owner(filepath.Clean(path))
	return ok && uid == m.cfg.UID
}

// leaseContainerStep reports whether a container is a step lease's, whose
// project and worktree mounts are read-only (TakeStepLease).
func (m *Manager) leaseContainerStep(name string) bool {
	t := m.leaseTable()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range t.byID {
		if l.Container == name {
			return l.step
		}
	}
	return false
}

// containerUser is the uid:gid a container runs as and its project cache
// volume: the folder's sandbox user and the folder's cache, or Swarm's own
// user and a cache of its own for a lease that runs as the daemon (the
// folder's cache belongs to the folder owner).
func (m *Manager) containerUser(name, root string) (int, int, string) {
	if t := m.leaseTable(); t != nil {
		t.mu.Lock()
		for _, l := range t.byID {
			if l.Container == name && l.asDaemon {
				t.mu.Unlock()
				return m.cfg.UID, m.cfg.GID, ProjectCacheVolume(root) + "-daemon"
			}
		}
		t.mu.Unlock()
	}
	uid, gid := m.sandboxUser(root)
	return uid, gid, ProjectCacheVolume(root)
}

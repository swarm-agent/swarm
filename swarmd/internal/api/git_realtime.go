package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/gitwatch"
	"swarm/packages/swarmd/internal/identity"
)

const (
	gitRealtimeDebounce       = 180 * time.Millisecond
	gitRealtimeMaxDelay       = 1200 * time.Millisecond
	gitRealtimeIdleLease      = 2 * time.Minute
	gitRealtimeFallbackMin    = 5 * time.Second
	gitRealtimeFallbackMax    = 60 * time.Second
	gitRealtimeReconcileMin   = 30 * time.Second
	gitRealtimeReconcileRange = 30 * time.Second
	gitRealtimeLongPoll       = 25 * time.Second
)

type gitRealtimeDiagnostics struct {
	Backend           string    `json:"backend"`
	WatchCount        int       `json:"watch_count"`
	EventCount        uint64    `json:"event_count"`
	OverflowCount     uint64    `json:"overflow_count"`
	RecrawlCount      uint64    `json:"recrawl_count"`
	RefreshCount      uint64    `json:"refresh_count"`
	LastEventAt       time.Time `json:"last_event_at,omitempty"`
	LastRefreshAt     time.Time `json:"last_refresh_at,omitempty"`
	FallbackReason    string    `json:"fallback_reason,omitempty"`
	RefreshDurationMS int64     `json:"refresh_duration_ms"`
}

type gitRealtimeResponse struct {
	OK            bool                   `json:"ok"`
	WorkspacePath string                 `json:"workspace_path"`
	WatchToken    string                 `json:"watch_token"`
	Status        gitstatus.Snapshot     `json:"status"`
	Diagnostics   gitRealtimeDiagnostics `json:"diagnostics"`
}

type gitRealtimeBackendFactory func(gitstatus.WatchPaths) (gitwatch.Backend, error)

type gitRealtimeRuntimeConfig struct {
	debounce     time.Duration
	maxDelay     time.Duration
	idleLease    time.Duration
	reconcileMin time.Duration
	reconcileMax time.Duration
	fallbackMin  time.Duration
	fallbackMax  time.Duration
}

type gitRealtimeManager struct {
	server         *Server
	mu             sync.Mutex
	repos          map[string]*gitRealtimeRepo
	backendFactory gitRealtimeBackendFactory
	runtime        gitRealtimeRuntimeConfig
}

type gitRealtimeRepo struct {
	manager       *gitRealtimeManager
	key           string
	workspacePath string
	paths         gitstatus.WatchPaths
	stop          chan struct{}
	stopped       chan struct{}
	manual        chan gitwatch.Scope
	stopOnce      sync.Once

	stateMu     sync.RWMutex
	generation  uint64
	snapshot    gitstatus.Snapshot
	diagnostics gitRealtimeDiagnostics
	leaseUntil  time.Time
	changed     chan struct{}
}

func newGitRealtimeManager(server *Server) *gitRealtimeManager {
	return &gitRealtimeManager{
		server: server,
		repos:  make(map[string]*gitRealtimeRepo),
		backendFactory: func(paths gitstatus.WatchPaths) (gitwatch.Backend, error) {
			return gitwatch.NewFSNotify(gitwatch.Config{WorktreeRoot: paths.RepoRoot, GitDir: paths.GitDir, CommonDir: paths.CommonDir})
		},
		runtime: gitRealtimeRuntimeConfig{
			debounce: gitRealtimeDebounce, maxDelay: gitRealtimeMaxDelay,
			idleLease: gitRealtimeIdleLease, reconcileMin: gitRealtimeReconcileMin,
			reconcileMax: gitRealtimeReconcileRange, fallbackMin: gitRealtimeFallbackMin, fallbackMax: gitRealtimeFallbackMax,
		},
	}
}

func (m *gitRealtimeManager) ensure(workspacePath string) (*gitRealtimeRepo, error) {
	if m == nil {
		return nil, errors.New("git realtime manager is not configured")
	}
	target := gitstatus.NormalizePath(workspacePath)
	if target == "" {
		return nil, errors.New("workspace_path is required")
	}
	// The common renewal path must stay in-memory. Resolving GitDir/CommonDir
	// invokes Git and is only needed while installing a new repository watcher.
	m.mu.Lock()
	for key, repo := range m.repos {
		if key == target || gitstatus.NormalizePath(repo.workspacePath) == target {
			repo.renewLease()
			m.mu.Unlock()
			return repo, nil
		}
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	paths, err := gitstatus.ResolveWatchPaths(ctx, target)
	cancel()
	if err != nil {
		return nil, err
	}
	key := gitstatus.NormalizePath(paths.RepoRoot)

	// Initialization remains under the manager lock so concurrent requests cannot
	// install duplicate native watchers for the same normalized worktree root.
	m.mu.Lock()
	defer m.mu.Unlock()
	if repo, ok := m.repos[key]; ok {
		repo.renewLease()
		return repo, nil
	}
	repo, err := newGitRealtimeRepo(m, key, target, paths)
	if err != nil {
		return nil, err
	}
	m.repos[key] = repo
	go repo.run()
	return repo, nil
}

func (m *gitRealtimeManager) remove(key string, repo *gitRealtimeRepo) {
	m.mu.Lock()
	if m.repos[key] == repo {
		delete(m.repos, key)
	}
	m.mu.Unlock()
}

func (m *gitRealtimeManager) signal(workspacePath string) {
	if m == nil {
		return
	}
	target := gitstatus.NormalizePath(workspacePath)
	m.mu.Lock()
	for key, repo := range m.repos {
		if key == target || gitstatus.NormalizePath(repo.workspacePath) == target {
			repo.signalRefresh(gitwatch.ScopeMetadata)
		}
	}
	m.mu.Unlock()
}

func (m *gitRealtimeManager) stopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	repos := make([]*gitRealtimeRepo, 0, len(m.repos))
	for _, repo := range m.repos {
		repos = append(repos, repo)
	}
	m.repos = make(map[string]*gitRealtimeRepo)
	m.mu.Unlock()
	for _, repo := range repos {
		repo.stopWatching()
	}
}

func newGitRealtimeRepo(manager *gitRealtimeManager, key, workspacePath string, paths gitstatus.WatchPaths) (*gitRealtimeRepo, error) {
	repo := &gitRealtimeRepo{
		manager: manager, key: key, workspacePath: workspacePath, paths: paths,
		stop: make(chan struct{}), stopped: make(chan struct{}), manual: make(chan gitwatch.Scope, 1),
		leaseUntil: time.Now().Add(manager.runtime.idleLease), changed: make(chan struct{}),
	}
	if !repo.refresh(gitwatch.ScopeMetadata) {
		return nil, errors.New("failed to read initial git snapshot")
	}
	return repo, nil
}

func (r *gitRealtimeRepo) newBackend() (gitwatch.Backend, error) {
	return r.manager.backendFactory(r.paths)
}

func (r *gitRealtimeRepo) run() {
	defer close(r.stopped)
	defer r.manager.remove(r.key, r)

	backend, err := r.newBackend()
	if err != nil {
		r.setFallback(err)
	}
	defer func() {
		if backend != nil {
			_ = backend.Close()
		}
	}()
	if backend != nil {
		r.setNativeDiagnostics(backend.Diagnostics())
	}

	reconcile := time.NewTimer(r.reconcileDelay())
	lease := time.NewTimer(time.Until(r.leaseDeadline()))
	defer reconcile.Stop()
	defer lease.Stop()
	var fallback *time.Timer
	var fallbackC <-chan time.Time
	fallbackDelay := r.manager.runtime.fallbackMin
	if backend == nil {
		fallback = time.NewTimer(fallbackDelay)
		fallbackC = fallback.C
	}
	var debounce *time.Timer
	var debounceC <-chan time.Time
	var firstDirty time.Time
	dirtyScope := gitwatch.ScopeWorktree

	markDirty := func(scope gitwatch.Scope) {
		now := time.Now()
		if firstDirty.IsZero() {
			firstDirty = now
		}
		if scope == gitwatch.ScopeMetadata {
			dirtyScope = scope
		}
		wait := r.manager.runtime.debounce
		if remaining := r.manager.runtime.maxDelay - now.Sub(firstDirty); remaining < wait {
			wait = remaining
		}
		if wait < 0 {
			wait = 0
		}
		if debounce == nil {
			debounce = time.NewTimer(wait)
		} else {
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(wait)
		}
		debounceC = debounce.C
	}

	for {
		var events <-chan gitwatch.Event
		if backend != nil {
			events = backend.Events()
		}
		select {
		case <-r.stop:
			return
		case scope := <-r.manual:
			markDirty(scope)
		case event, ok := <-events:
			if !ok {
				event = gitwatch.Event{RebuildRequired: true, Err: errors.New("native watcher event channel closed")}
			}
			r.noteEvent(backend)
			if event.RebuildRequired {
				_ = backend.Close()
				backend = nil
				r.noteRecrawl(event.Err)
				rebuilt, rebuildErr := r.newBackend()
				if rebuildErr != nil {
					r.setFallback(rebuildErr)
					if fallback == nil {
						fallback = time.NewTimer(fallbackDelay)
					} else {
						fallback.Reset(fallbackDelay)
					}
					fallbackC = fallback.C
				} else {
					backend = rebuilt
					r.setNativeDiagnostics(backend.Diagnostics())
					fallbackC = nil
				}
				markDirty(gitwatch.ScopeMetadata)
			} else {
				markDirty(event.Scope)
			}
		case <-debounceC:
			if r.refresh(dirtyScope) {
				firstDirty = time.Time{}
				dirtyScope = gitwatch.ScopeWorktree
				debounceC = nil
			} else {
				markDirty(dirtyScope)
			}
		case <-reconcile.C:
			if !r.refresh(gitwatch.ScopeMetadata) && backend != nil {
				_ = backend.Close()
				backend = nil
				reason := errors.New("canonical reconciliation failed")
				r.noteRecrawl(reason)
				r.setFallback(reason)
				fallbackDelay = r.manager.runtime.fallbackMin
				if fallback == nil {
					fallback = time.NewTimer(fallbackDelay)
				} else {
					fallback.Reset(fallbackDelay)
				}
				fallbackC = fallback.C
			}
			reconcile.Reset(r.reconcileDelay())
		case <-fallbackC:
			if r.refresh(gitwatch.ScopeMetadata) {
				if rebuilt, rebuildErr := r.newBackend(); rebuildErr == nil {
					backend = rebuilt
					r.noteRecrawl(nil)
					r.setNativeDiagnostics(backend.Diagnostics())
					fallbackC = nil
					fallbackDelay = r.manager.runtime.fallbackMin
					continue
				}
			}
			fallbackDelay *= 2
			if fallbackDelay > r.manager.runtime.fallbackMax {
				fallbackDelay = r.manager.runtime.fallbackMax
			}
			fallback.Reset(fallbackDelay)
		case <-lease.C:
			if time.Now().After(r.leaseDeadline()) {
				return
			}
			lease.Reset(time.Until(r.leaseDeadline()))
		}
	}
}

func (r *gitRealtimeRepo) reconcileDelay() time.Duration {
	if r.manager.runtime.reconcileMax <= 0 {
		return r.manager.runtime.reconcileMin
	}
	return r.manager.runtime.reconcileMin + time.Duration(rand.Int63n(int64(r.manager.runtime.reconcileMax)))
}

func (r *gitRealtimeRepo) stopWatching() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.stopped
}

func (r *gitRealtimeRepo) renewLease() {
	r.stateMu.Lock()
	r.leaseUntil = time.Now().Add(r.manager.runtime.idleLease)
	r.stateMu.Unlock()
}

func (r *gitRealtimeRepo) leaseDeadline() time.Time {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.leaseUntil
}

func (r *gitRealtimeRepo) signalRefresh(scope gitwatch.Scope) {
	if r == nil {
		return
	}
	select {
	case r.manual <- scope:
	default:
	}
}

func (r *gitRealtimeRepo) refresh(scope gitwatch.Scope) bool {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	includeDetails := scope != gitwatch.ScopeWorktree
	snapshot, err := gitstatus.SnapshotForResolvedPaths(ctx, r.workspacePath, r.paths, gitstatus.Options{RecentLimit: 8, IncludeDetails: includeDetails})
	if err != nil {
		log.Printf("git realtime snapshot failed workspace=%q err=%v", r.workspacePath, err)
		return false
	}
	r.stateMu.Lock()
	if !includeDetails && r.generation > 0 {
		snapshot.Remotes = r.snapshot.Remotes
		snapshot.RecentCommits = r.snapshot.RecentCommits
	}
	if r.generation == 0 || gitSnapshotFingerprint(snapshot) != gitSnapshotFingerprint(r.snapshot) {
		r.generation++
		close(r.changed)
		r.changed = make(chan struct{})
	}
	// Always retain the canonical result. Detail-only metadata changes need not
	// advance the watch token, but must still be visible in subsequent reads.
	r.snapshot = snapshot
	r.diagnostics.RefreshCount++
	r.diagnostics.LastRefreshAt = time.Now()
	r.diagnostics.RefreshDurationMS = time.Since(started).Milliseconds()
	r.stateMu.Unlock()
	return true
}

func (r *gitRealtimeRepo) noteEvent(backend gitwatch.Backend) {
	r.stateMu.Lock()
	r.diagnostics.LastEventAt = time.Now()
	if backend != nil {
		d := backend.Diagnostics()
		r.diagnostics.EventCount = d.RawEvents
		r.diagnostics.OverflowCount = d.Overflows
		r.diagnostics.WatchCount = d.WatchedDirs
	}
	r.stateMu.Unlock()
}

func (r *gitRealtimeRepo) noteRecrawl(reason error) {
	r.stateMu.Lock()
	r.diagnostics.RecrawlCount++
	if reason != nil {
		r.diagnostics.FallbackReason = reason.Error()
	}
	r.stateMu.Unlock()
}

func (r *gitRealtimeRepo) setFallback(err error) {
	r.stateMu.Lock()
	r.diagnostics.Backend = "polling"
	r.diagnostics.WatchCount = 0
	if err != nil {
		r.diagnostics.FallbackReason = err.Error()
	}
	r.stateMu.Unlock()
}

func (r *gitRealtimeRepo) setNativeDiagnostics(d gitwatch.Diagnostics) {
	r.stateMu.Lock()
	r.diagnostics.Backend = d.BackendKind
	r.diagnostics.WatchCount = d.WatchedDirs
	r.diagnostics.EventCount = d.RawEvents
	r.diagnostics.OverflowCount = d.Overflows
	r.diagnostics.FallbackReason = ""
	r.stateMu.Unlock()
}

func (r *gitRealtimeRepo) current() (gitstatus.Snapshot, string, gitRealtimeDiagnostics) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.snapshot, strconv.FormatUint(r.generation, 10), r.diagnostics
}

// waitForChange blocks a cache hit until the canonical snapshot changes or the
// long-poll window expires. The channel is captured under the same lock as the
// generation so an update cannot be missed between checking and waiting.
func (r *gitRealtimeRepo) waitForChange(ctx context.Context, token string, timeout time.Duration) {
	r.stateMu.RLock()
	current := strconv.FormatUint(r.generation, 10)
	changed := r.changed
	r.stateMu.RUnlock()
	if token == "" || token != current {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-changed:
	case <-ctx.Done():
	case <-timer.C:
	}
}

func gitSnapshotFingerprint(snapshot gitstatus.Snapshot) string {
	payload, err := json.Marshal(struct {
		Branch         string                 `json:"branch"`
		HeadOID        string                 `json:"head_oid"`
		Upstream       string                 `json:"upstream"`
		AheadCount     int                    `json:"ahead_count"`
		BehindCount    int                    `json:"behind_count"`
		StashCount     int                    `json:"stash_count"`
		DirtyCount     int                    `json:"dirty_count"`
		StagedCount    int                    `json:"staged_count"`
		ModifiedCount  int                    `json:"modified_count"`
		UntrackedCount int                    `json:"untracked_count"`
		ConflictCount  int                    `json:"conflict_count"`
		Files          []gitstatus.FileStatus `json:"files"`
	}{snapshot.Branch, snapshot.HeadOID, snapshot.Upstream, snapshot.AheadCount, snapshot.BehindCount, snapshot.StashCount,
		snapshot.DirtyCount, snapshot.StagedCount, snapshot.ModifiedCount, snapshot.UntrackedCount, snapshot.ConflictCount, snapshot.Files})
	if err != nil {
		return snapshot.RefreshedAt.String()
	}
	return string(payload)
}

func (s *Server) handleGitRealtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	workspacePath, err := s.resolveGitStatusWorkspacePath(r, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.gitRealtime == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("git realtime manager is not configured"))
		return
	}
	repo, err := s.gitRealtime.ensure(workspacePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	repo.renewLease()
	repo.waitForChange(r.Context(), r.URL.Query().Get("watch_token"), gitRealtimeLongPoll)
	snapshot, token, diagnostics := repo.current()
	writeJSON(w, http.StatusOK, gitRealtimeResponse{OK: true, WorkspacePath: gitstatus.NormalizePath(workspacePath), WatchToken: token, Status: snapshot, Diagnostics: diagnostics})
}

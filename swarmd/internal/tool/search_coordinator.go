package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarm/packages/swarmd/internal/tool/searchipc"
)

const (
	defaultSearchResidentRoots = 4
	defaultSearchQueueDepth    = 64
	searchHardTimeoutGrace     = 2 * time.Second
)

// SearchCoordinator owns a bounded set of watcher-backed helper processes. A
// worker is permanently bound to one canonical index root and serializes all
// native FFF operations for that root.
type SearchCoordinator struct {
	mu         sync.Mutex
	workers    map[string]*residentSearchWorker
	inflight   map[string]*residentSearchCall
	closed     bool
	maxRoots   int
	queueCap   int
	resolve    func() (string, error)
	helperArgs []string
	nextID     atomic.Uint64
	stats      SearchCoordinatorStats
}

// SearchCoordinatorStats contains monotonic process-lifetime diagnostics.
type SearchCoordinatorStats struct {
	ColdStarts       atomic.Uint64
	ResidentHits     atomic.Uint64
	InitialScanNanos atomic.Int64
	WatcherWaitNanos atomic.Int64
	QueueWaitNanos   atomic.Int64
	CoalescedWaiters atomic.Uint64
	NativeExecutions atomic.Uint64
	WorkerRestarts   atomic.Uint64
	Evictions        atomic.Uint64
	Shutdowns        atomic.Uint64
}

type SearchCoordinatorSnapshot struct {
	ColdStarts, ResidentHits, CoalescedWaiters, NativeExecutions uint64
	WorkerRestarts, Evictions, Shutdowns                         uint64
	InitialScan, WatcherWait, QueueWait                          time.Duration
	ResidentRoots, Inflight, PendingCalls                        int
}

type residentSearchCall struct {
	req      searchipc.Request
	key      string
	queuedAt time.Time
	done     chan struct{}
	resp     searchipc.Response
	err      error
	waiters  atomic.Int64
}

type residentSearchWorker struct {
	root        string
	coordinator *SearchCoordinator
	queue       chan *residentSearchCall
	stop        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	pending     atomic.Int64
	lastUsedNS  atomic.Int64

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	enc    *json.Encoder
	dec    *json.Decoder
	stderr *boundedSearchDiagnostic
}

type boundedSearchDiagnostic struct {
	mu  sync.Mutex
	buf []byte
}

func (b *boundedSearchDiagnostic) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const limit = 4096
	if len(p) >= limit {
		b.buf = append(b.buf[:0], p[len(p)-limit:]...)
	} else {
		need := len(b.buf) + len(p) - limit
		if need > 0 {
			b.buf = append(b.buf[:0], b.buf[need:]...)
		}
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

func (b *boundedSearchDiagnostic) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.buf...))
}

func NewSearchCoordinator(maxRoots int) *SearchCoordinator {
	if maxRoots <= 0 {
		maxRoots = defaultSearchResidentRoots
	}
	return &SearchCoordinator{
		workers: make(map[string]*residentSearchWorker), inflight: make(map[string]*residentSearchCall),
		maxRoots: maxRoots, queueCap: defaultSearchQueueDepth, resolve: resolveSearchHelperPath,
	}
}

func (c *SearchCoordinator) Snapshot() SearchCoordinatorSnapshot {
	if c == nil {
		return SearchCoordinatorSnapshot{}
	}
	c.mu.Lock()
	roots := len(c.workers)
	inflight := len(c.inflight)
	pending := 0
	for _, worker := range c.workers {
		if worker != nil {
			pending += int(worker.pending.Load())
		}
	}
	c.mu.Unlock()
	return SearchCoordinatorSnapshot{
		ColdStarts: c.stats.ColdStarts.Load(), ResidentHits: c.stats.ResidentHits.Load(),
		CoalescedWaiters: c.stats.CoalescedWaiters.Load(), NativeExecutions: c.stats.NativeExecutions.Load(),
		WorkerRestarts: c.stats.WorkerRestarts.Load(), Evictions: c.stats.Evictions.Load(), Shutdowns: c.stats.Shutdowns.Load(),
		InitialScan: time.Duration(c.stats.InitialScanNanos.Load()), WatcherWait: time.Duration(c.stats.WatcherWaitNanos.Load()),
		QueueWait: time.Duration(c.stats.QueueWaitNanos.Load()), ResidentRoots: roots, Inflight: inflight, PendingCalls: pending,
	}
}

func (c *SearchCoordinator) Execute(ctx context.Context, req searchipc.Request) (searchipc.Response, error) {
	if c == nil {
		return searchipc.Response{}, errors.New("FFF search coordinator is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root, target, err := canonicalSearchScope(req)
	if err != nil {
		return searchipc.Response{}, err
	}
	req.ProtocolVersion = searchipc.ProtocolVersion
	req.IndexRoot, req.TargetPath, req.SearchRoot = root, target, ""
	req.RequestID = ""
	keyBytes, err := json.Marshal(req)
	if err != nil {
		return searchipc.Response{}, fmt.Errorf("normalize FFF search request: %w", err)
	}
	key := string(keyBytes)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return searchipc.Response{}, errors.New("FFF search coordinator is closed")
	}
	if existing := c.inflight[key]; existing != nil {
		existing.waiters.Add(1)
		c.stats.CoalescedWaiters.Add(1)
		c.mu.Unlock()
		return waitResidentSearchCall(ctx, existing)
	}
	worker, err := c.workerLocked(root)
	if err != nil {
		c.mu.Unlock()
		return searchipc.Response{}, err
	}
	req.RequestID = fmt.Sprintf("search-%d", c.nextID.Add(1))
	call := &residentSearchCall{req: req, key: key, queuedAt: time.Now(), done: make(chan struct{})}
	call.waiters.Store(1)
	c.inflight[key] = call
	worker.pending.Add(1)
	select {
	case worker.queue <- call:
		c.mu.Unlock()
	case <-ctx.Done():
		delete(c.inflight, key)
		worker.pending.Add(-1)
		c.mu.Unlock()
		return searchipc.Response{}, ctx.Err()
	default:
		delete(c.inflight, key)
		worker.pending.Add(-1)
		c.mu.Unlock()
		return searchipc.Response{}, fmt.Errorf("FFF search queue for root %q is full", root)
	}
	return waitResidentSearchCall(ctx, call)
}

func waitResidentSearchCall(ctx context.Context, call *residentSearchCall) (searchipc.Response, error) {
	defer call.waiters.Add(-1)
	select {
	case <-ctx.Done():
		return searchipc.Response{}, ctx.Err()
	case <-call.done:
		return call.resp, call.err
	}
}

func (c *SearchCoordinator) workerLocked(root string) (*residentSearchWorker, error) {
	if worker := c.workers[root]; worker != nil {
		c.stats.ResidentHits.Add(1)
		return worker, nil
	}
	if len(c.workers) >= c.maxRoots {
		var victim *residentSearchWorker
		for _, candidate := range c.workers {
			if candidate.pending.Load() != 0 {
				continue
			}
			if victim == nil || candidate.lastUsedNS.Load() < victim.lastUsedNS.Load() {
				victim = candidate
			}
		}
		if victim == nil {
			return nil, fmt.Errorf("FFF resident worker limit %d reached; all roots are active", c.maxRoots)
		}
		delete(c.workers, victim.root)
		c.stats.Evictions.Add(1)
		victim.close()
	}
	worker := &residentSearchWorker{root: root, coordinator: c, queue: make(chan *residentSearchCall, c.queueCap), stop: make(chan struct{}), done: make(chan struct{})}
	worker.lastUsedNS.Store(time.Now().UnixNano())
	c.workers[root] = worker
	go worker.run()
	return worker, nil
}

func (w *residentSearchWorker) run() {
	defer close(w.done)
	defer w.stopProcess()
	for {
		select {
		case <-w.stop:
			return
		case call := <-w.queue:
			if call == nil {
				continue
			}
			w.coordinator.stats.QueueWaitNanos.Add(time.Since(call.queuedAt).Nanoseconds())
			if call.waiters.Load() == 0 {
				w.finish(call, searchipc.Response{}, context.Canceled)
				continue
			}
			resp, err := w.execute(call.req)
			w.finish(call, resp, err)
		}
	}
}

func (w *residentSearchWorker) finish(call *residentSearchCall, resp searchipc.Response, err error) {
	call.resp, call.err = resp, err
	w.lastUsedNS.Store(time.Now().UnixNano())
	w.pending.Add(-1)
	w.coordinator.mu.Lock()
	delete(w.coordinator.inflight, call.key)
	w.coordinator.mu.Unlock()
	close(call.done)
}

func (w *residentSearchWorker) execute(req searchipc.Request) (searchipc.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if w.cmd == nil {
			if err := w.startProcess(); err != nil {
				return searchipc.Response{}, err
			}
		}
		w.coordinator.stats.NativeExecutions.Add(1)
		resp, err := w.exchange(req)
		if err == nil {
			if resp.Diagnostics.ColdStartCount > 0 {
				w.coordinator.stats.InitialScanNanos.Store(resp.Diagnostics.InitialScanMillis * int64(time.Millisecond))
				w.coordinator.stats.WatcherWaitNanos.Store(resp.Diagnostics.WatcherWaitMillis * int64(time.Millisecond))
			}
			if resp.ErrorCode == "hard_timeout" {
				w.stopProcess()
			}
			return resp, nil
		}
		w.stopProcess()
		if attempt == 0 {
			w.coordinator.stats.WorkerRestarts.Add(1)
			continue
		}
		return searchipc.Response{}, err
	}
	return searchipc.Response{}, errors.New("FFF search worker failed")
}

func (w *residentSearchWorker) startProcess() error {
	helperPath, err := w.coordinator.resolve()
	if err != nil {
		return err
	}
	cmd := exec.Command(helperPath, w.coordinator.helperArgs...)
	prepareCommandForCancellation(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open FFF helper stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open FFF helper stdout: %w", err)
	}
	stderr := &boundedSearchDiagnostic{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("start FFF search helper: %w", err)
	}
	w.cmd, w.stdin, w.stdout, w.stderr = cmd, stdin, stdout, stderr
	w.enc, w.dec = json.NewEncoder(stdin), json.NewDecoder(bufio.NewReader(stdout))
	w.coordinator.stats.ColdStarts.Add(1)
	return nil
}

func (w *residentSearchWorker) exchange(req searchipc.Request) (searchipc.Response, error) {
	type outcome struct {
		resp searchipc.Response
		err  error
	}
	result := make(chan outcome, 1)
	enc, dec, stderr := w.enc, w.dec, w.stderr
	go func() {
		if err := enc.Encode(req); err != nil {
			result <- outcome{err: fmt.Errorf("write FFF helper request: %w", err)}
			return
		}
		var resp searchipc.Response
		if err := dec.Decode(&resp); err != nil {
			result <- outcome{err: fmt.Errorf("read FFF helper response: %w%s", err, formatSearchHelperDiagnostic(stderr.String(), ""))}
			return
		}
		if resp.RequestID != req.RequestID {
			result <- outcome{err: fmt.Errorf("FFF helper response id mismatch: got %q want %q", resp.RequestID, req.RequestID)}
			return
		}
		result <- outcome{resp: resp}
	}()
	timeout := time.Duration(req.TimeoutMillis)*time.Millisecond + searchHardTimeoutGrace
	if timeout <= searchHardTimeoutGrace {
		timeout = defaultSearchTimeout + searchHardTimeoutGrace
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-result:
		return out.resp, out.err
	case <-timer.C:
		return searchipc.Response{}, fmt.Errorf("FFF helper exceeded hard deadline %s", timeout)
	case <-w.stop:
		return searchipc.Response{}, errors.New("FFF search worker stopped")
	}
}

func (w *residentSearchWorker) stopProcess() {
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
	if w.stdout != nil {
		_ = w.stdout.Close()
	}
	if w.cmd != nil {
		if w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
		_ = w.cmd.Wait()
	}
	w.cmd, w.stdin, w.stdout, w.enc, w.dec, w.stderr = nil, nil, nil, nil, nil, nil
}

func (w *residentSearchWorker) close() {
	w.stopOnce.Do(func() { close(w.stop) })
}

func (c *SearchCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	workers := make([]*residentSearchWorker, 0, len(c.workers))
	for _, worker := range c.workers {
		workers = append(workers, worker)
	}
	c.workers = make(map[string]*residentSearchWorker)
	c.mu.Unlock()
	for _, worker := range workers {
		worker.close()
		<-worker.done
	}
	c.stats.Shutdowns.Add(1)
	return nil
}

func canonicalSearchScope(req searchipc.Request) (string, string, error) {
	root := strings.TrimSpace(req.IndexRoot)
	target := strings.TrimSpace(req.TargetPath)
	if root == "" {
		root = strings.TrimSpace(req.SearchRoot)
	}
	if target == "" {
		target = strings.TrimSpace(req.SearchRoot)
	}
	if target == "" {
		target = root
	}
	if root == "" {
		return "", "", errors.New("FFF search index root is required")
	}
	canonical := func(path string) (string, error) {
		abs, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolved), nil
	}
	var err error
	if root, err = canonical(root); err != nil {
		return "", "", fmt.Errorf("resolve FFF index root: %w", err)
	}
	if target, err = canonical(target); err != nil {
		return "", "", fmt.Errorf("resolve FFF target path: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("FFF target %q is outside index root %q", target, root)
	}
	return root, target, nil
}

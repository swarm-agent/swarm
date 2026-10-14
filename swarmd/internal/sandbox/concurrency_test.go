package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type lifecycleRunner struct {
	*fakeRunner
	started     chan struct{}
	unblock     chan struct{}
	stopStarted chan struct{}
	stopUnblock chan struct{}
	once        sync.Once
	stateMu     sync.Mutex
	stopped     bool
	unsafeExec  bool
}

func (r *lifecycleRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "stop" {
		if r.stopStarted != nil {
			close(r.stopStarted)
			select {
			case <-r.stopUnblock:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		r.stateMu.Lock()
		r.stopped = true
		r.stateMu.Unlock()
	}
	if len(args) > 0 && (args[0] == "run" || args[0] == "start") {
		r.stateMu.Lock()
		r.stopped = false
		r.stateMu.Unlock()
	}
	return r.fakeRunner.Run(ctx, name, args...)
}
func (r *lifecycleRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
	r.stateMu.Lock()
	r.unsafeExec = r.unsafeExec || r.stopped
	r.stateMu.Unlock()
	block := false
	r.once.Do(func() {
		block = true
		if r.started != nil {
			close(r.started)
		}
	})
	if block && r.unblock != nil {
		select {
		case <-r.unblock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func concurrencyManager(t *testing.T, r *lifecycleRunner) (*Manager, Scope) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(context.Background(), Config{Mode: ModeRequired, UID: 10001, GID: 10001, Engine: "sh", IdleTimeout: time.Second}, r)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }})
	scope, ok, err := m.ScopeFor(root)
	if err != nil || !ok {
		t.Fatalf("scope: %v %v", ok, err)
	}
	b, err := m.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	m.release(b)
	return m, scope
}
func runTestExec(m *Manager, scope Scope) error {
	result, err := m.Exec(context.Background(), scope, ExecRequest{Command: []string{"true"}, WorkingDir: scope.Root, Timeout: 5 * time.Second})
	if err == nil && (result == nil || result.ExitCode != 0) {
		return fmt.Errorf("unexpected result: %+v", result)
	}
	return err
}
func awaitResult(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("command queued behind a running exec")
	}
}

// Purpose: Manager.acquire and execOnce must not serialize unrelated commands
// behind stale verification. RouteGit and a second Exec must finish while a
// first Exec is held by the runner. This channel-controlled unit layer is the
// narrowest proof of lock scheduling, without Docker or provider dependencies.
func TestAcquireStaleVerificationDoesNotBlockCommands(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner(), started: make(chan struct{}), unblock: make(chan struct{})}
	m, scope := concurrencyManager(t, r)
	first := make(chan error, 1)
	go func() { first <- runTestExec(m, scope) }()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("first exec did not start")
	}
	defer func() { close(r.unblock); awaitResult(t, first) }()
	m.mu.Lock()
	m.boxes[ContainerName(scope.Root)].verified = time.Now().Add(-2 * verifyInterval)
	m.mu.Unlock()
	git := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		git <- m.RouteGit(context.Background(), exec.Command("git", "-C", scope.Root, "rev-parse", "--show-toplevel"))
	}()
	go func() { second <- runTestExec(m, scope) }()
	awaitResult(t, git)
	awaitResult(t, second)
}

// Purpose: Manager.stopIdle must not stop a container leased by Exec, even
// when lastUsed looks idle. The runner-held command proves the negative
// postcondition at the lifecycle boundary without a real engine.
func TestStopIdlePreservesInflightCommands(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner(), started: make(chan struct{}), unblock: make(chan struct{})}
	m, scope := concurrencyManager(t, r)
	first := make(chan error, 1)
	go func() { first <- runTestExec(m, scope) }()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("exec did not start")
	}
	defer func() { close(r.unblock); awaitResult(t, first) }()
	m.stopIdle(context.Background(), time.Now().Add(time.Hour))
	if r.ran("stop ") {
		t.Fatal("stopped an inflight container")
	}
}

// Purpose: Manager.stopIdle's reservation and acquire's lease must be atomic.
// Hold the fake stop in progress and concurrently acquire through Exec; it
// must wait, restart, and never execute against the stopped container. This
// controlled interleaving tests the race's postconditions at the narrowest layer.
func TestStopIdleSerializesNewAcquire(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner(), stopStarted: make(chan struct{}), stopUnblock: make(chan struct{})}
	m, scope := concurrencyManager(t, r)
	stopped := make(chan error, 1)
	go func() { m.stopIdle(context.Background(), time.Now().Add(time.Hour)); stopped <- nil }()
	select {
	case <-r.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("stop did not start")
	}
	second := make(chan error, 1)
	go func() { second <- runTestExec(m, scope) }()
	// An acquisition with a short deadline must reject while stop is reserved,
	// rather than issue an exec on a container being stopped.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := m.acquire(ctx, scope)
	cancel()
	if err == nil {
		t.Error("acquire bypassed an in-progress stop")
	}
	close(r.stopUnblock)
	awaitResult(t, stopped)
	awaitResult(t, second)
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.unsafeExec {
		t.Fatal("exec ran on stopped container")
	}
	if r.stopped {
		t.Fatal("acquire did not restart container")
	}
}

// Purpose: acquire must reject spec replacement while leased, without changing
// container state or starving acquisitions of the original spec. After release
// replacement may proceed. Fake engine calls are the narrowest proof that no
// destructive lifecycle operation crosses an active command lease.
func TestAcquireSpecChangeRejectsBusyContainer(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner()}
	m, scope := concurrencyManager(t, r)
	r.mu.Lock()
	r.calls = nil // exclude initial creation's inspect/replace calls
	r.mu.Unlock()
	b, err := m.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	extra := t.TempDir()
	changed := Scope{Root: scope.Root, Mounts: append(append([]string(nil), scope.Mounts...), extra)}
	_, err = m.acquire(context.Background(), changed)
	if err == nil {
		t.Error("spec change accepted while busy")
	}
	if r.ran("rm ") {
		t.Error("busy container removed")
	}
	original, err := m.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	m.release(original)
	m.release(b)
	replacement, err := m.acquire(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.requireMounted(replacement, extra); err != nil {
		t.Error(err)
	}
	m.release(replacement)
}

// Purpose: explicit supervisor failure must not be treated as periodic stale
// verification of a live container. invalidate and acquire must fail closed
// while other leases remain, then recover after release; this unit layer pins
// that distinction without inducing an actual engine outage.
func TestAcquireInvalidationRejectsBusyContainer(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner()}
	m, scope := concurrencyManager(t, r)
	b, err := m.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	m.invalidate(b.name)
	if _, err = m.acquire(context.Background(), scope); err == nil {
		t.Error("invalid container accepted while busy")
	}
	m.release(b)
	b, err = m.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	m.release(b)
}

type waitLogWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
}

func (w *waitLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return n, err
}

// Purpose: slow RouteGit acquisitions must emit one privacy-safe warning
// while still blocked. Holding a lifecycle reservation proves the real 2s
// timer and Git entrypoint without logging arguments, paths or credentials;
// a hermetic unit test is sufficient for this logging contract.
func TestAcquireSlowGitWaitLogsNameOnly(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: readyRunner()}
	m, scope := concurrencyManager(t, r)
	m.mu.Lock()
	b := m.boxes[ContainerName(scope.Root)]
	b.changing = make(chan struct{})
	m.mu.Unlock()
	w := &waitLogWriter{written: make(chan struct{}, 2)}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.RouteGit(ctx, exec.Command("git", "-C", scope.Root, "rev-parse", "private-argument"))
	}()
	select {
	case <-w.written:
	case <-time.After(3 * time.Second):
		t.Error("missing slow acquisition warning")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("cancelled acquisition succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("acquisition ignored cancellation")
	}
	w.mu.Lock()
	text := w.buf.String()
	w.mu.Unlock()
	if strings.Count(text, "sandbox acquisition waiting longer than 2s") != 1 || !strings.Contains(text, "command=git") || strings.Contains(text, "private-argument") || strings.Contains(text, scope.Root) {
		t.Fatalf("unexpected warning: %s", text)
	}
	m.mu.Lock()
	close(b.changing)
	b.changing = nil
	if b.inflight != 0 {
		t.Error("cancelled acquisition leaked a lease")
	}
	m.mu.Unlock()
}

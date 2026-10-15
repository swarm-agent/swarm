package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: real Manager.Exec saturation must not queue routed daemon Git behind
// agent slots or their CPU quota. Only a Docker integration test can prove the
// separate cgroups; thousands of real files exercise worktree deletion itself.
func TestCPUDockerGitUnderLoad(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker unavailable; real cgroup load proof required")
	}
	if os.Getuid() == 0 {
		t.Skip("requires non-root sandbox owner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	checkout := filepath.Join(root, "checkout")
	git("worktree", "add", "--detach", checkout)
	for i := 0; i < 5000; i++ {
		if err := os.WriteFile(filepath.Join(checkout, fmt.Sprint(i)), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewManager(ctx, Config{Mode: ModeRequired, Network: "none", Memory: "1g", MaxWorkerRuns: 2, CPUs: "0.5", CPUSlots: 1, StateDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }})
	defer func() {
		for _, name := range []string{ContainerName(root), ContainerName(root) + "-daemon-git"} {
			_ = m.removeJobContainer(context.Background(), name)
		}
	}()
	loadCtx, stopLoad := context.WithCancel(ctx)
	var wg sync.WaitGroup
	started := make(chan struct{})
	var once sync.Once
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = m.Exec(loadCtx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"sh", "-c", "printf 'ready\\n'; while :; do :; done"}, Timeout: time.Minute, MaxTimeout: time.Minute, OnOutput: func(p []byte) {
			if strings.Contains(string(p), "ready") {
				once.Do(func() { close(started) })
			}
		}})
	}()
	defer func() { stopLoad(); wg.Wait() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Code-owned landing preparation must run while the only agent slot is held.
	for _, args := range [][]string{{"git", "branch", "--show-current"}, {"git", "status", "--porcelain"}, {"git", "rebase", "HEAD"}, {"bash", "-o", "pipefail", "-c", "git diff --binary HEAD | git patch-id --stable"}} {
		res, err := m.ExecDaemon(ctx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: args, Timeout: 12 * time.Second, MaxTimeout: 12 * time.Second})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("landing command %v: %+v %v", args, res, err)
		}
	}
	old := Default()
	SetDefault(m)
	defer SetDefault(old)
	out, err := GitCombinedOutput(WithGitCommandTimeout(ctx, 12*time.Second), root, nil, "worktree", "remove", "--force", checkout)
	if err != nil {
		t.Fatalf("git under CPU load: %s %v", out, err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout remains: %v", err)
	}
	m.mu.Lock()
	n := len(m.cpuSlots)
	m.mu.Unlock()
	if n != 1 {
		t.Fatal("load ended before Git completed")
	}
	// Queue a one-second command budget behind the real CPU-bound process.
	waiting := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		res, err := m.Exec(ctx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"git", "rev-parse", "HEAD"}, Timeout: time.Second, MaxTimeout: time.Second, OnOutput: func(p []byte) {
			if strings.Contains(string(p), "waiting for a CPU slot") {
				close(waiting)
			}
		}})
		if err == nil && res.ExitCode != 0 {
			err = fmt.Errorf("queued git exit %d", res.ExitCode)
		}
		finished <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Real arithmetic, not a sleep surrogate; keep the slot occupied longer
	// than the queued request's execution budget before releasing it.
	res, err := m.ExecDaemon(ctx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"bash", "-c", "end=$((SECONDS+3)); i=0; while ((SECONDS<end)); do ((i+=1)); done"}, Timeout: 10 * time.Second, MaxTimeout: 10 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("CPU workload: %+v %v", res, err)
	}
	select {
	case err := <-finished:
		t.Fatalf("queued command finished early: %v", err)
	default:
	}
	stopLoad()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

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

// Purpose: real Manager.Exec saturation must not throttle routed daemon Git with
// an explicit agent CPU quota. Only a Docker integration test can prove the
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
	m, err := NewManager(ctx, Config{Mode: ModeRequired, Network: "none", Memory: "1g", MaxWorkerRuns: 2, CPUs: "0.5", StateDir: t.TempDir()}, nil)
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
	// Code-owned landing preparation must run while an agent consumes CPU.
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
	// Another agent command can execute without waiting for the load to end.
	res, err := m.Exec(ctx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"git", "rev-parse", "HEAD"}, Timeout: 10 * time.Second, MaxTimeout: 10 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("concurrent agent command: %+v %v", res, err)
	}
}

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

func localTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(context.Background(), Config{Mode: ModeRequired, Backend: BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Active() {
		t.Skipf("actual bubblewrap isolation unavailable: %s", m.Status().Reason)
	}
	t.Cleanup(m.Close)
	return m
}
func localRun(t *testing.T, m *Manager, root, script string) *provider.ExecResult {
	t.Helper()
	scope, err := m.CommandScope(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Exec(context.Background(), scope, ExecRequest{WorkingDir: root, Command: []string{"bash", "-c", script}, Timeout: 5 * time.Second, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLocalProcessIsolationAndParallelCheckouts(t *testing.T) {
	// Purpose: actual OS process proof for the headless local executor, not an
	// AI/provider benchmark. Owner: Manager.execLocal/localArgv. Threat: cwd-only
	// execution exposes daemon state, host processes/network and peer worktrees.
	m := localTestManager(t)
	base := t.TempDir()
	host := filepath.Join(base, "host-marker")
	if err := os.WriteFile(host, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	roots := []string{filepath.Join(base, "one"), filepath.Join(base, "two")}
	for _, root := range roots {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	hostNet, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, root := range roots {
		i, root := i, root
		wg.Add(1)
		go func() {
			defer wg.Done()
			peer := roots[1-i]
			script := "test ! -e '" + host + "' && test ! -e '" + peer + "' && printf escape > '" + host + "' && printf own > own && readlink /proc/self/ns/net"
			r := localRun(t, m, root, script)
			if r.ExitCode != 0 {
				t.Errorf("isolated process exit=%d output=%s", r.ExitCode, r.Stdout)
			}
			if strings.TrimSpace(r.Stdout) == hostNet {
				t.Error("host network namespace shared")
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(host)
	if err != nil || string(data) != "private" {
		t.Fatal("host marker changed")
	}
	for _, root := range roots {
		if data, err := os.ReadFile(filepath.Join(root, "own")); err != nil || string(data) != "own" {
			t.Fatal("assigned checkout not writable")
		}
	}
}

func TestLocalMissingBackendFailsClosed(t *testing.T) {
	// Purpose/owner: NewManager + Exec/RouteGit retain enforced mode when the
	// backend is absent. Threat: silently executing Bash or Git on the host.
	t.Setenv("PATH", t.TempDir())
	m, err := NewManager(context.Background(), Config{Mode: ModeRequired, Backend: BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() || !m.Enforced() {
		t.Fatal("missing backend did not remain enforced")
	}
	root := t.TempDir()
	_, err = m.Exec(context.Background(), Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"bash", "-c", "touch escaped"}})
	if !errors.Is(err, ErrSandboxRequired) {
		t.Fatalf("execution did not fail closed: %v", err)
	}
	cmd := exec.Command("git", "status")
	cmd.Dir = root
	if err := m.RouteGit(context.Background(), cmd); !errors.Is(err, ErrSandboxRequired) {
		t.Fatalf("Git did not fail closed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatal("host command ran")
	}
}

func TestLocalCancellationStopsProcess(t *testing.T) {
	// Purpose: bounded real-process cancellation; no synthetic agent workload.
	// Owner: execLocal. Threat: cancelled commands continue writing after return.
	m := localTestManager(t)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.Exec(ctx, Scope{Root: root}, ExecRequest{WorkingDir: root, Command: []string{"bash", "-c", "(sleep 0.3; printf escaped > escaped) & printf ready > ready; wait"}})
		done <- err
	}()
	readyDeadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		select {
		case <-readyDeadline:
			t.Fatal("process never became ready")
		case err := <-done:
			t.Fatalf("process ended before cancellation: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, provider.ErrOperationCancelled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled process did not finish")
	}
	// The child would write after 300 ms if cancellation only killed the shell.
	<-time.After(400 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatal("cancelled command wrote marker")
	}
}

func localFixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func localRoutedGit(t *testing.T, m *Manager, root string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = []string{}
	if err := m.RouteGit(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	defer ReleaseGit(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("routed Git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestLocalLinkedGitCommitAndMetadataProtection(t *testing.T) {
	// Purpose: actual routed Git creates a descendant commit while Bash cannot
	// mutate shared Git state or a peer checkout. Owners: localGitMounts and
	// routeLocalGit. Threat: a repository hook/filter runs with daemon authority,
	// or rewriting .git/HEAD redirects canonical Git to another checkout.
	m := localTestManager(t)
	base := t.TempDir()
	source := filepath.Join(base, "source")
	one := filepath.Join(base, "one")
	two := filepath.Join(base, "two")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, source, "init", "-b", "primary")
	if err := os.WriteFile(filepath.Join(source, "base"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, source, "add", "base")
	localFixtureGit(t, source, "commit", "-m", "base")
	baseOID := localFixtureGit(t, source, "rev-parse", "HEAD")
	localFixtureGit(t, source, "worktree", "add", "-b", "one", one)
	localFixtureGit(t, source, "worktree", "add", "-b", "two", two)
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }})
	marker := filepath.Join(base, "host-hook-marker")
	hook := filepath.Join(source, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf escaped > '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// A repository include/filter must not execute even with writable objects.
	localFixtureGit(t, source, "config", "filter.escape.clean", "printf escaped > '"+marker+"'; cat")
	if err := os.WriteFile(filepath.Join(one, ".gitattributes"), []byte("change filter=escape\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "change"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	r := localRun(t, m, one, "! printf bad > .git && ! printf bad > '"+filepath.Join(source, ".git", "refs", "heads", "two")+"' && ! printf bad > '"+filepath.Join(source, ".git", "worktrees", "one", "HEAD")+"'")
	if r.ExitCode != 0 {
		t.Fatalf("Bash metadata protection: %s", r.Stdout)
	}
	localRoutedGit(t, m, one, "add", "--all")
	localRoutedGit(t, m, one, "commit", "-m", "isolated commit")
	scratch, err := m.localScratchDir(one, "alternate-index-")
	if err != nil {
		t.Fatal(err)
	}
	indexCmd := exec.Command("git", "read-tree", "HEAD")
	indexCmd.Dir = one
	indexCmd.Env = []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	if err := m.RouteGit(context.Background(), indexCmd); err != nil {
		t.Fatal(err)
	}
	if out, err := indexCmd.CombinedOutput(); err != nil {
		ReleaseGit(indexCmd)
		t.Fatalf("alternate index unavailable: %v: %s", err, out)
	}
	ReleaseGit(indexCmd)
	head := localFixtureGit(t, one, "rev-parse", "HEAD")
	if head == baseOID {
		t.Fatal("commit was not created")
	}
	localFixtureGit(t, one, "merge-base", "--is-ancestor", baseOID, head)
	if got := localFixtureGit(t, two, "rev-parse", "HEAD"); got != baseOID {
		t.Fatal("peer branch changed")
	}
	if got := localFixtureGit(t, source, "rev-parse", "HEAD"); got != baseOID {
		t.Fatal("source branch changed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("untrusted Git executable ran on host")
	}
	if _, err := os.Stat(filepath.Join(two, "change")); !os.IsNotExist(err) {
		t.Fatal("peer checkout changed")
	}
}

func TestLocalRejectsSymlinkedGitMetadata(t *testing.T) {
	// Purpose: malicious metadata never turns a narrow writable Git bind into
	// another host directory. Owner: localGitMounts/localMetadataDir. Threat:
	// objects or refs symlinks bypass scope/protected-path validation.
	m := localTestManager(t)
	base := t.TempDir()
	source := filepath.Join(base, "source")
	checkout := filepath.Join(base, "checkout")
	outside := filepath.Join(base, "outside")
	for _, p := range []string{source, outside} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	localFixtureGit(t, source, "init", "-b", "primary")
	localFixtureGit(t, source, "commit", "--allow-empty", "-m", "base")
	localFixtureGit(t, source, "worktree", "add", "-b", "job", checkout)
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }})
	objects := filepath.Join(source, ".git", "objects")
	saved := objects + "-saved"
	if err := os.Rename(objects, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, objects); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "--all")
	cmd.Dir = checkout
	if err := m.RouteGit(context.Background(), cmd); err == nil {
		t.Fatal("accepted symlinked objects mount")
	}
	if err := os.Remove(objects); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, objects); err != nil {
		t.Fatal(err)
	}
	peerRef := filepath.Join(source, ".git", "refs", "heads", "symlink-peer")
	if err := os.Symlink(outside, peerRef); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "status")
	cmd.Dir = checkout
	if err := m.RouteGit(context.Background(), cmd); err == nil {
		ReleaseGit(cmd)
		t.Fatal("accepted symlinked peer ref mount")
	}
	if err := os.Remove(peerRef); err != nil {
		t.Fatal(err)
	}
	// A forged gitdir pointer cannot borrow the peer's valid common directory.
	pointer := filepath.Join(checkout, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: "+filepath.Join(source, ".git", "worktrees", "other")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "status")
	cmd.Dir = checkout
	if err := m.RouteGit(context.Background(), cmd); err == nil {
		t.Fatal("accepted forged gitdir")
	}
}

func TestLocalContainerAPIsRefuseWithoutEngine(t *testing.T) {
	// Purpose: local Active status must never invoke Docker or dereference a nil
	// container runner. Owner: Manager public lifecycle/cache APIs. Threat:
	// unrelated API calls crash a correctly running headless control plane.
	m := localTestManager(t)
	root := t.TempDir()
	if _, err := m.ProjectCacheUsage(context.Background(), root); err == nil {
		t.Fatal("container cache accepted")
	}
	if _, err := m.BuildEnvironment(context.Background(), EnvironmentBuild{}); err == nil {
		t.Fatal("container build accepted")
	}
	if err := m.ReconcileContainers(context.Background(), func(string) (bool, error) {
		t.Fatal("local reconciliation called container callback")
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if m.MaxWorkerRuns() != 0 || m.Status().Memory != "" {
		t.Fatal("local mode set a default run or RAM limit")
	}
}

func TestLocalWorkerAdmissionWithoutRAMReservation(t *testing.T) {
	// Purpose: local execution sets no default run or RAM limit; an owner
	// limit is kept. Owner: newLocalManager/MaxWorkerRuns; worker capacity
	// treats 0 as no limit.
	for _, tc := range []struct {
		limit int
		want  int
	}{{0, 0}, {7, 7}} {
		m, err := NewManager(context.Background(), Config{Backend: BackendBubblewrap, Mode: ModeOff, MaxWorkerRuns: tc.limit}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.MaxWorkerRuns() != tc.want || m.Status().Memory != "" {
			t.Fatalf("limit=%d status=%+v actual=%d", tc.limit, m.Status(), m.MaxWorkerRuns())
		}
		m.Close()
	}
	if _, err := NewManager(context.Background(), Config{Backend: BackendBubblewrap, Mode: ModeOff, MaxWorkerRuns: -1}, nil); err == nil {
		t.Fatal("negative worker limit accepted")
	}
}

func TestLocalRoutedWorktreeLifecycle(t *testing.T) {
	// Purpose: real daemon Git can allocate and remove a managed checkout; this
	// is OS/Git qualification, not provider qualification. Owner:
	// routeLocalGit/localWorktreeMounts. Threat: exact command confinement breaks
	// Coder allocation or widens a trusted Git destination to arbitrary host data.
	m := localTestManager(t)
	base := t.TempDir()
	source := filepath.Join(base, "source")
	storage := filepath.Join(base, "worktrees")
	bucket := filepath.Join(storage, "project")
	peer := filepath.Join(bucket, "peer")
	job := filepath.Join(bucket, "job")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bucket, 0700); err != nil {
		t.Fatal(err)
	}
	localFixtureGit(t, source, "init", "-b", "primary")
	localFixtureGit(t, source, "commit", "--allow-empty", "-m", "base")
	localFixtureGit(t, source, "worktree", "add", "-b", "peer", peer)
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }, WorktreesRoot: storage, Bucket: func(string) (string, error) { return "project", nil }})
	peerBefore := localFixtureGit(t, peer, "rev-parse", "HEAD")
	localRoutedGit(t, m, source, "worktree", "add", "-b", "job", job, "HEAD")
	if _, err := os.Stat(filepath.Join(job, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(job, "change"), []byte("job"), 0600); err != nil {
		t.Fatal(err)
	}
	localRoutedGit(t, m, job, "add", "change")
	localRoutedGit(t, m, job, "commit", "-m", "job")
	localRoutedGit(t, m, source, "worktree", "remove", job)
	if _, err := os.Stat(job); !os.IsNotExist(err) {
		t.Fatalf("worktree not removed: %v", err)
	}
	if got := localFixtureGit(t, peer, "rev-parse", "HEAD"); got != peerBefore {
		t.Fatal("peer changed")
	}
	outside := filepath.Join(base, "outside")
	cmd := exec.Command("git", "worktree", "add", "-b", "escape", outside, "HEAD")
	cmd.Dir = source
	if err := m.RouteGit(context.Background(), cmd); err == nil {
		ReleaseGit(cmd)
		t.Fatal("arbitrary worktree destination accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("rejected destination touched")
	}
}

func TestLocalNestedPeerRemainsReadOnly(t *testing.T) {
	// Purpose: a broad registered root must not make its nested peer writable.
	// Owner: localArgv's peer overlays. Threat: parent workspace selection
	// bypasses per-checkout separation even with correct mount namespaces.
	m := localTestManager(t)
	root := t.TempDir()
	peer := filepath.Join(root, "peer")
	if err := os.Mkdir(peer, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(peer, "marker")
	if err := os.WriteFile(marker, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root, peer}, nil }})
	r := localRun(t, m, root, "! printf escaped > peer/marker && printf own > own")
	if r.ExitCode != 0 {
		t.Fatalf("nested peer protection: %s", r.Stdout)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "private" {
		t.Fatal("nested peer changed")
	}
}

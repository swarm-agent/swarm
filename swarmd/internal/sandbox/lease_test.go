package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func newLeaseTestManager(t *testing.T, max int, running func(string) (bool, error)) (*Manager, *fakeRunner, string) {
	t.Helper()
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	project := t.TempDir()
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{project}, nil }})
	m.SetLeasePolicy(func(string) (int, error) { return max, nil }, running)
	return m, runner, project
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) ranSince(n int, prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls[n:] {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) runArgs(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 4 && c[0] == "run" && c[4] == name {
			return c
		}
	}
	return nil
}

// Purpose: an agent can take leases up to the folder's maximum, each its own
// labelled container of the folder, and the next lease past the maximum is
// refused with the counts.
func TestLeasesUpToTheFolderMaximum(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, nil)
	ctx := context.Background()
	first, err := m.TakeLease(ctx, project, "session-a")
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if _, err := m.TakeLease(ctx, project, "session-b"); err != nil {
		t.Fatalf("second lease: %v", err)
	}
	_, err = m.TakeLease(ctx, project, "session-a")
	var full *LeasesFullError
	if !errors.As(err, &full) || full.Max != 2 || full.InUse != 2 {
		t.Fatalf("third lease: %v", err)
	}
	if !strings.Contains(first.Container, "-lease-"+first.ID) || first.Container == ContainerName(project) {
		t.Fatalf("lease container %q", first.Container)
	}
	args := strings.Join(runner.runArgs(first.Container), " ")
	if !strings.Contains(args, "--label swarm.lease="+first.ID) || !strings.Contains(args, "--label swarm.lease.owner=session-a") {
		t.Fatalf("lease labels missing: %s", args)
	}
	if !strings.Contains(args, "type=bind,src="+project+",dst="+project) {
		t.Fatalf("folder not mounted at its own path: %s", args)
	}
	if got := m.LeasesInUse(project); got != 2 {
		t.Fatalf("in use %d", got)
	}
}

// Purpose: a folder without a maximum, or whose environment is not ready,
// cannot be leased, and nothing is started.
func TestLeaseRefusedWithoutMaximum(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 0, nil)
	if _, err := m.TakeLease(context.Background(), project, "s"); err == nil || !strings.Contains(err.Error(), "max_leases") {
		t.Fatalf("lease with max 0: %v", err)
	}
	m.SetLeasePolicy(func(string) (int, error) { return 0, errors.New("the environment is building") }, nil)
	if _, err := m.TakeLease(context.Background(), project, "s"); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatalf("lease of a building environment: %v", err)
	}
	if runner.ran("run ") {
		t.Fatal("a refused lease started a container")
	}
}

// Purpose: only the agent holding a lease can run in or release it, and a
// release removes the container and its home volume and frees the slot.
func TestLeaseReleaseIsOwnersAndFreesTheSlot(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 1, nil)
	ctx := context.Background()
	lease, err := m.TakeLease(ctx, project, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseLease(ctx, lease.ID, "someone-else"); err == nil {
		t.Fatal("another agent released the lease")
	}
	if _, err := m.ExecLease(ctx, lease.ID, "someone-else", ExecRequest{Command: []string{"true"}}); err == nil {
		t.Fatal("another agent ran in the lease")
	}
	before := runner.count()
	if err := m.ReleaseLease(ctx, lease.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if !runner.ranSince(before, "rm -f "+lease.Container) || !runner.ranSince(before, "volume rm -f "+lease.Container+"-home") {
		t.Fatal("release did not remove the lease container and its home volume")
	}
	if runner.ran("rm -f " + ContainerName(project) + " ") {
		t.Fatal("release removed the folder's own sandbox")
	}
	if _, err := m.TakeLease(ctx, project, "owner"); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
}

// Purpose: Swarm releases every lease of an agent whose run has ended, and
// keeps the leases of agents still running.
func TestLeasesOfFinishedAgentsAreReleased(t *testing.T) {
	alive := map[string]bool{"running": true}
	m, runner, project := newLeaseTestManager(t, 3, func(owner string) (bool, error) { return alive[owner], nil })
	ctx := context.Background()
	done1, _ := m.TakeLease(ctx, project, "done")
	done2, _ := m.TakeLease(ctx, project, "done")
	keep, _ := m.TakeLease(ctx, project, "running")
	before := runner.count()
	m.reapLeases(ctx)
	if !runner.ranSince(before, "rm -f "+done1.Container) || !runner.ranSince(before, "rm -f "+done2.Container) {
		t.Fatal("leases of a finished agent were kept")
	}
	if runner.ranSince(before, "rm -f "+keep.Container) {
		t.Fatal("a running agent's lease was released")
	}
	if got := m.OwnerLeases("done"); len(got) != 0 {
		t.Fatalf("finished agent still holds %v", got)
	}
	if got := m.OwnerLeases("running"); len(got) != 1 {
		t.Fatalf("running agent holds %v", got)
	}
}

// Purpose: after a daemon restart, lease containers this instance made are
// found again from their labels, so finished agents' leases are released.
func TestLeasesRecoveredAfterRestart(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, func(string) (bool, error) { return false, nil })
	name := leaseContainerName(project, "abc123")
	runner.out["ps -a"] = name + "\tabc123\told-session\t" + project + "\n"
	if err := m.recoverLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.OwnerLeases("old-session"); len(got) != 1 || got[0].Container != name {
		t.Fatalf("recovered %v", got)
	}
	m.reapLeases(context.Background())
	if !runner.ran("rm -f " + name) {
		t.Fatal("recovered lease of a finished agent was not released")
	}
}

// Purpose: a command in a lease runs as the folder's owner while the agent's
// worktree belongs to Swarm's user (and the folder may belong to the person),
// so Git there gets safe.directory for the folder, its .git and the working
// directory, instead of refusing "dubious ownership".
func TestLeaseCommandsCarrySafeDirectories(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 1, nil)
	ctx := context.Background()
	lease, err := m.TakeLease(ctx, project, "owner")
	if err != nil {
		t.Fatal(err)
	}
	before := runner.count()
	_, _ = m.ExecLease(ctx, lease.ID, "owner", ExecRequest{Command: []string{"git", "status"}, WorkingDir: project})
	runner.mu.Lock()
	calls := append([][]string(nil), runner.calls[before:]...)
	runner.mu.Unlock()
	joined := ""
	for _, c := range calls {
		joined += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{"safe.directory", "GIT_CONFIG_VALUE_", project + "/.git"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("lease exec lacks %q:\n%s", want, joined)
		}
	}
}

// Purpose: in a folder that belongs to the person, a lease for an agent
// working in its own worktree (owned by Swarm's user) runs as Swarm's user
// with a cache of its own, so the agent can open its worktree; a lease for an
// agent working in the folder itself runs as the folder's owner.
func TestLeaseRunsAsTheOwnerOfTheWorkingDirectory(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, nil)
	worktree := project + "/wt"
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	person := m.cfg.UID + 1
	m.owner = func(path string) (int, bool) {
		if path == worktree {
			return m.cfg.UID, true
		}
		return person, true
	}
	ctx := context.Background()
	inWorktree, err := m.TakeLease(ctx, worktree, "job")
	if err != nil {
		t.Fatal(err)
	}
	inFolder, err := m.TakeLease(ctx, project, "chat")
	if err != nil {
		t.Fatal(err)
	}
	daemon := strings.Join(runner.runArgs(inWorktree.Container), " ")
	if !strings.Contains(daemon, fmt.Sprintf("--user %d:%d", m.cfg.UID, m.cfg.GID)) || !strings.Contains(daemon, ProjectCacheVolume(project)+"-daemon,") || !strings.Contains(daemon, "swarm.lease.user=daemon") {
		t.Fatalf("worktree lease does not run as Swarm's user with its own cache: %s", daemon)
	}
	owner := strings.Join(runner.runArgs(inFolder.Container), " ")
	if !strings.Contains(owner, fmt.Sprintf("--user %d:%d", person, m.cfg.GID)) || !strings.Contains(owner, "src="+ProjectCacheVolume(project)+",") {
		t.Fatalf("folder lease does not run as the folder owner: %s", owner)
	}
}

// Purpose: recoverLeases must isolate a stale review row so later containers
// remain counted and reapable. The fake engine is the narrowest deterministic
// boundary proving removal, accounting and reaping without Docker.
func TestLeasesRecoveryContinuesAfterStaleReview(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, func(string) (bool, error) { return false, nil })
	stale := leaseContainerName(project, "stale")
	ordinary := leaseContainerName(project, "ordinary")
	runner.out["ps -a"] = stale + "\tstale\treviewer\t" + project + "\tdaemon\t" + project + "/removed\n" + ordinary + "\tordinary\tworker\t" + project + "\n"
	if err := m.recoverLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f "+stale) || len(m.OwnerLeases("reviewer")) != 0 {
		t.Fatal("stale review was not removed")
	}
	if len(m.OwnerLeases("worker")) != 1 || m.LeasesInUse(project) != 1 {
		t.Fatal("later lease lost from accounting")
	}
	m.reapLeases(context.Background())
	if !runner.ran("rm -f "+ordinary) || m.LeasesInUse(project) != 0 {
		t.Fatal("later lease was not reaped")
	}
}

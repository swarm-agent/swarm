package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Purpose: a workflow run step's lease (TakeStepLease) is the folder's
// environment lease agents use (its own labelled container, same image,
// user, network and hardening) but every project mount is read-only, so a
// script from an agent-written branch cannot change the folder or a job's
// worktree; it shares the folder's lease maximum, and an agent's ordinary
// lease of the same folder stays writable. Owners: Manager.takeLease,
// leaseLabelsLocked, runArgsImage (mountOptions) and leaseSpec. The fake
// engine is the narrowest deterministic layer that shows the container
// arguments without Docker.
func TestStepLeaseMountsTheFolderReadOnly(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, nil)
	ctx := context.Background()
	step, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-1/console/1")
	if err != nil {
		t.Fatalf("step lease: %v", err)
	}
	args := strings.Join(runner.runArgs(step.Container), " ")
	if !strings.Contains(args, "type=bind,src="+project+",dst="+project+",readonly") {
		t.Fatalf("folder not mounted read-only in a step lease: %s", args)
	}
	if strings.Contains(args, "type=bind,src="+project+",dst="+project+" ") {
		t.Fatalf("folder also mounted writable in a step lease: %s", args)
	}
	for _, want := range []string{"--label swarm.lease=" + step.ID, "--label swarm.lease.owner=workflow-step/dlv-1/console/1", "--label swarm.lease.step=1", "--cap-drop ALL", "no-new-privileges"} {
		if !strings.Contains(args, want) {
			t.Fatalf("step lease lacks %q: %s", want, args)
		}
	}
	if _, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-1/console/1"); err == nil {
		t.Fatal("one step took two leases")
	}
	agent, err := m.TakeLease(ctx, project, "session-a")
	if err != nil {
		t.Fatalf("agent lease: %v", err)
	}
	agentArgs := strings.Join(runner.runArgs(agent.Container), " ")
	if !strings.Contains(agentArgs, "type=bind,src="+project+",dst="+project+" ") || strings.Contains(agentArgs, "dst="+project+",readonly") {
		t.Fatalf("an agent's lease became read-only: %s", agentArgs)
	}
	if strings.Contains(agentArgs, "swarm.lease.step") {
		t.Fatalf("an agent's lease is labelled a step lease: %s", agentArgs)
	}
	// The step's lease counts against the folder's maximum.
	var full *LeasesFullError
	if _, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-2/console/1"); !errors.As(err, &full) || full.InUse != 2 {
		t.Fatalf("third lease: %v", err)
	}
}

// Purpose: a workflow run step's lease runs as the user and groups of the
// agents that write the folder's repository from their worktrees (Swarm's
// own user, plus the folder's Git directory group), not as the folder's
// owner: agents' objects are 0440 under the sandbox umask, owned by Swarm's
// user and grouped to the Git directory, so a step lease as the folder's
// owner without that group could not read them and Git reported them as
// "corrupt" (the production console step). An agent's ordinary lease of the
// folder itself keeps running as the folder's owner. Owners:
// Manager.takeLease (stepLeaseRunsAsDaemon), containerUser,
// leaseGroupArgs and runArgsImage; the fake engine is the narrowest
// deterministic layer that shows the container's user and groups.
func TestStepLeaseRunsAsTheAgentWorktreeUser(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 2, nil)
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(filepath.Join(project, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitGroup := strconv.FormatUint(uint64(st.Sys().(*syscall.Stat_t).Gid), 10)
	if gitGroup == strconv.Itoa(m.cfg.GID) {
		t.Skip("the test folder's group is the daemon's group: no group to add")
	}
	if uid, _ := m.sandboxUser(project); uid == m.cfg.UID {
		t.Fatal("fixture: the folder must belong to another user than Swarm's")
	}
	ctx := context.Background()
	step, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-1/console/1")
	if err != nil {
		t.Fatalf("step lease: %v", err)
	}
	args := strings.Join(runner.runArgs(step.Container), " ")
	daemonUser := "--user " + strconv.Itoa(m.cfg.UID) + ":" + strconv.Itoa(m.cfg.GID)
	if !strings.Contains(args, daemonUser) || !strings.Contains(args, "--group-add "+gitGroup) || !strings.Contains(args, "--label swarm.lease.user=daemon") {
		t.Fatalf("step lease does not run as the agent worktree user (%s, group %s): %s", daemonUser, gitGroup, args)
	}
	if !strings.Contains(args, "dst="+project+",readonly") {
		t.Fatalf("step lease lost its read-only mount: %s", args)
	}
	agent, err := m.TakeLease(ctx, project, "session-a")
	if err != nil {
		t.Fatalf("agent lease: %v", err)
	}
	agentArgs := strings.Join(runner.runArgs(agent.Container), " ")
	ownerUID, _ := m.sandboxUser(project)
	if !strings.Contains(agentArgs, "--user "+strconv.Itoa(ownerUID)+":"+strconv.Itoa(m.cfg.GID)) || strings.Contains(agentArgs, "--group-add") {
		t.Fatalf("an agent's lease of the folder itself changed user: %s", agentArgs)
	}
}

// Purpose: the reaper keeps a step lease while its step runs in this
// process (its owner is no agent session, so the session liveness check
// must never release it), releases it with the step, and releases a step
// lease recovered after a restart (its step is resumed with a new lease).
// Owners: reapLeases (liveSteps), dropLease and recoverLeases (step label).
func TestStepLeaseReaping(t *testing.T) {
	askedFor := []string{}
	m, runner, project := newLeaseTestManager(t, 3, func(owner string) (bool, error) {
		askedFor = append(askedFor, owner)
		return false, nil
	})
	ctx := context.Background()
	step, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-1/console/1")
	if err != nil {
		t.Fatal(err)
	}
	before := runner.count()
	m.reapLeases(ctx)
	if runner.ranSince(before, "rm -f "+step.Container) || len(m.OwnerLeases(step.Owner)) != 1 {
		t.Fatal("the reaper released a running step's lease")
	}
	for _, owner := range askedFor {
		if owner == step.Owner {
			t.Fatal("the reaper asked the session liveness check about a step")
		}
	}
	if err := m.ReleaseLease(ctx, step.ID, step.Owner); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f "+step.Container) || m.LeasesInUse(project) != 0 {
		t.Fatal("releasing the step's lease did not remove it")
	}

	// After a restart: a recovered step lease has no running step.
	name := leaseContainerName(project, "old")
	runner.out["ps -a"] = name + "\told\tworkflow-step/dlv-0/console/2\t" + project + "\t\t\t1\n"
	if err := m.recoverLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.OwnerLeases("workflow-step/dlv-0/console/2"); len(got) != 1 || !got[0].step {
		t.Fatalf("recovered %+v", got)
	}
	m.reapLeases(ctx)
	if !runner.ran("rm -f "+name) || m.LeasesInUse(project) != 0 {
		t.Fatal("a recovered step lease was not released")
	}
}

// Purpose: a step lease whose release fails (the engine's `rm -f` errors or
// times out) must not keep one of the folder's max_leases slots until the
// daemon restarts. The step has ended whatever the removal's result, so the
// reaper must retry the release on its next pass. Regression: dropLease
// returned before clearing the step from liveSteps, so the reaper, which
// releases only step leases whose step no longer runs, never retried.
// Owners: dropLease (liveSteps) and reapLeases. The fake engine is the
// narrowest deterministic layer that can fail one removal.
func TestStepLeaseFailedReleaseIsRetriedByTheReaper(t *testing.T) {
	m, runner, project := newLeaseTestManager(t, 1, nil)
	ctx := context.Background()
	step, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-1/console/1")
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.fail = map[string]error{"rm -f": errors.New("engine timed out")}
	runner.mu.Unlock()
	if err := m.ReleaseLease(ctx, step.ID, step.Owner); err == nil {
		t.Fatal("a failed removal reported the lease released")
	}
	if m.LeasesInUse(project) != 1 {
		t.Fatal("a lease whose container was not removed left the table")
	}
	runner.mu.Lock()
	runner.fail = nil
	runner.mu.Unlock()
	before := runner.count()
	m.reapLeases(ctx)
	if !runner.ranSince(before, "rm -f "+step.Container) || m.LeasesInUse(project) != 0 {
		t.Fatal("the reaper did not retry the failed release of a finished step's lease")
	}
	// The folder's only slot is free again.
	if _, err := m.TakeStepLease(ctx, project, "workflow-step/dlv-2/console/1"); err != nil {
		t.Fatalf("slot still held after the retried release: %v", err)
	}
}

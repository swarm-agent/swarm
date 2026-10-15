package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"swarm/packages/swarmd/internal/egress"
	"testing"
	"time"
)

// Tests must not inherit injected Git config from the caller/CI command router.
func TestMain(m *testing.M) {
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(key, "GIT_") {
			_ = os.Unsetenv(key)
		}
	}
	os.Exit(m.Run())
}

// Purpose: mounting and initializeHome must repair the same volume for jobs,
// while reviewers retain an independent home. Fake engine argv is the narrowest
// layer proving this ownership target (not a claim about Docker permissions).
func TestWorkerHomeRepairVolume(t *testing.T) {
	for _, review := range []bool{false, true} {
		runner := readyRunner()
		m := newTestManager(t, ModeRequired, runner, Config{})
		root := t.TempDir()
		scope := Scope{Root: root, JobCheckout: root + "/job"}
		if review {
			scope.ReviewCheckout = root + "/review"
		}
		name := scopeContainerName(scope)
		mount := "type=volume,src=" + homeVolumeName(name, root) + ",dst=" + sandboxHome
		args := m.RunArgs(name, root, "spec", []string{root}, SecretSetup{})
		if !strings.Contains(strings.Join(args, " "), mount) {
			t.Fatal("home mount missing")
		}
		m.initializeHome(context.Background(), name, root, EnvironmentImage{Image: DefaultImage})
		if !runner.ran("run --rm") {
			t.Fatal("repair not called")
		}
		found := false
		for _, c := range runner.calls {
			if len(c) > 1 && c[0] == "run" && c[1] == "--rm" && strings.Contains(strings.Join(c, " "), mount) {
				found = true
			}
		}
		if !found {
			t.Fatal("repair targets different volume")
		}
	}
}

// Purpose: EndJob and ReconcileContainers remove terminal/unknown job containers
// without deleting caches or checkouts, and refuse to race active command leases.
// Fake engine plus a fresh manager proves restart discovery independently of boxes.
func TestWorkerContainerCleanup(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{})
	root := t.TempDir()
	checkout := root + "/trees/bucket/job"
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root + "/source"}, nil }, WorktreesRoot: root + "/trees", Bucket: func(string) (string, error) { return "bucket", nil }})
	name := JobContainerName(root+"/source", checkout)
	m.boxes[name] = &box{name: name, inflight: 1}
	if err := m.EndJob(context.Background(), checkout, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if runner.ran("rm -f " + name) {
		t.Fatal("removed active command")
	}
	m.release(m.boxes[name])
	if !runner.ran("rm -f " + name) {
		t.Fatal("terminal cleanup not retried after lease release")
	}
	if err := m.EndJob(context.Background(), checkout, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f " + name) {
		t.Fatal("job not removed")
	}
	fresh := newTestManager(t, ModeRequired, runner, Config{})
	live := JobContainerName(root+"/source", checkout+"live")
	unknown := JobContainerName(root+"/source", checkout+"unknown")
	runner.out["ps -a"] = name + "\n" + live + "\n" + unknown + "\n"
	if err := fresh.ReconcileContainers(context.Background(), func(n string) (bool, error) { return n == live, nil }); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f "+unknown) || !runner.ran("stop --time 5 "+live) {
		t.Fatal("startup did not reconcile discovered containers")
	}
	idle := JobContainerName(root+"/source", checkout+"idle")
	fresh.boxes[idle] = &box{name: idle, lastUsed: time.Now().Add(-2 * fresh.cfg.IdleTimeout)}
	fresh.stopIdle(context.Background(), time.Now())
	if !runner.ran("stop --time 5 "+idle) || runner.ran("rm -f "+idle) {
		t.Fatal("idle live job must be stopped, not removed")
	}
	for _, c := range runner.calls {
		if len(c) > 0 && c[0] == "rm" && strings.Contains(strings.Join(c, " "), "-v") {
			t.Fatal("cache removal")
		}
	}
}

// Purpose: EndJob requires durable terminal evidence and fences that evidence
// against a newer acquisition. The manager/engine boundary proves a delayed
// terminal callback cannot remove a next checkpoint's container.
func TestWorkerCleanupFences(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{})
	root := t.TempDir()
	checkout := root + "/trees/bucket/job"
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root + "/source"}, nil }, WorktreesRoot: root + "/trees", Bucket: func(string) (string, error) { return "bucket", nil }})
	name := JobContainerName(root+"/source", checkout)
	b := &box{name: name, root: root + "/source", generation: 1}
	m.boxes[name] = b
	if err := m.EndJob(context.Background(), checkout, func() (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if runner.ran("rm -f " + name) {
		t.Fatal("removed live job")
	}
	if err := m.EndJob(context.Background(), checkout, func() (bool, error) { m.mu.Lock(); b.generation++; m.mu.Unlock(); return true, nil }); err != nil {
		t.Fatal(err)
	}
	if runner.ran("rm -f " + name) {
		t.Fatal("stale callback removed newer acquisition")
	}
}

type registryBroker struct {
	registry *egress.Registry
	token    string
}

func (b *registryBroker) PrepareSandbox(root string) (SecretSetup, error) { return SecretSetup{}, nil }
func (b *registryBroker) ReleaseSandbox(root string)                      { b.registry.Unregister(root) }

// Purpose: project gateway registration is shared by sibling job containers.
// Manager removal must retain authentication until its last reference is gone;
// a real egress.Registry checks token resolution, not merely a callback count.
func TestWorkerCleanupRetainsSiblingGateway(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{})
	root := t.TempDir()
	registry := egress.NewRegistry()
	token, err := registry.Register(egress.Sandbox{WorkspacePath: root})
	if err != nil {
		t.Fatal(err)
	}
	m.SetSecretBroker(&registryBroker{registry: registry, token: token})
	a, b := JobContainerName(root, "a"), JobContainerName(root, "b")
	m.boxes[a] = &box{name: a, root: root}
	m.boxes[b] = &box{name: b, root: root}
	if err := m.removeJobContainer(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.ResolveSandbox(token); !ok {
		t.Fatal("sibling authentication revoked")
	}
	m.stopIdle(context.Background(), time.Now().Add(2*m.cfg.IdleTimeout))
	if _, ok := registry.ResolveSandbox(token); !ok {
		t.Fatal("idle stop revoked live authentication")
	}
	if err := m.removeJobContainer(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.ResolveSandbox(token); ok {
		t.Fatal("last container retained registration")
	}
}

// Purpose: RouteGit must retain its manager lease until the execution caller
// releases it. Reaper/removal rejection before release protects in-flight daemon
// Git; explicit release also covers command start failure.
func TestWorkerGitLeaseLifetime(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{Engine: "true"})
	root := t.TempDir()
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }})
	cmd := exec.Command("git", "-C", root, "status")
	if err := m.RouteGit(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	defer ReleaseGit(cmd)
	name := scopeContainerName(Scope{Root: root, daemonGit: true})
	if err := m.removeJobContainer(context.Background(), name); err == nil {
		t.Fatal("removed routed Git container before execution")
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	ReleaseGit(cmd)
	if err := m.removeJobContainer(context.Background(), name); err != nil {
		t.Fatal(err)
	}
}

package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Tests must not inherit injected Git config from the caller/CI command router.
func TestMain(m *testing.M) {
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(key, "GIT_CONFIG_") {
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
	if err := m.EndJob(context.Background(), checkout); err == nil {
		t.Fatal("removed active command")
	}
	m.boxes[name].inflight = 0
	if err := m.EndJob(context.Background(), checkout); err != nil {
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
	if !runner.ran("rm -f " + idle) {
		t.Fatal("idle job retained")
	}
	for _, c := range runner.calls {
		if len(c) > 0 && c[0] == "rm" && strings.Contains(strings.Join(c, " "), "-v") {
			t.Fatal("cache removal")
		}
	}
}

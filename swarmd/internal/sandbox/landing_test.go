package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Purpose: AcquireLanding must create a distinct container with the canonical
// folder image and existing hardened limits, never reuse an agent container.
// The manager's engine double is the narrowest boundary proving creation and
// removal, including a retryable engine removal failure without losing the lease.
func TestLandEnvironmentManagerLifecycle(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	root := t.TempDir()
	m.SetEnvironmentResolver(fakeResolver{root: root, img: EnvironmentImage{Image: "fixture-image", Path: "/usr/bin"}})
	first, err := m.AcquireLanding(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.AcquireLanding(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	a, b := scopeContainerName(first.scope), scopeContainerName(second.scope)
	if a == b || a == ContainerName(root) {
		t.Fatal("reused container")
	}
	found := false
	for _, args := range runner.calls {
		text := strings.Join(args, " ")
		if strings.Contains(text, "--name "+a+" ") {
			found = true
			for _, want := range []string{"fixture-image", "--memory 4g", "--cap-drop ALL", "--network ", "dst=" + root} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %s", want, text)
				}
			}
		}
	}
	if !found {
		t.Fatal("no fresh container")
	}
	runner.fail["rm -f"] = errors.New("engine failure")
	if first.Release() == nil {
		t.Fatal("hidden removal failure")
	}
	delete(runner.fail, "rm -f")
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f "+a) || !runner.ran("volume rm "+homeVolumeName(a, root)) {
		t.Fatal("lease leaked")
	}
	if _, err := first.Exec(context.Background(), ExecRequest{}); err == nil {
		t.Fatal("released lease executed")
	}
}

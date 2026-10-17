package sandbox

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Purpose: the landing API must support opt-in persistent volumes; this is the
// missing API reproduction, supplemented below by Docker-boundary assertions.
func TestLandingVolumesAPI(t *testing.T) {
	if _, ok := reflect.TypeOf((*Manager)(nil)).MethodByName("AcquireLandingWithVolumes"); !ok {
		t.Fatal("landing leases cannot mount persistent runner cache volumes")
	}
}

// Purpose: AcquireLandingWithVolumes must reject malformed or checkout-masking
// mounts before any Docker mutation, mount valid caches read-write with default
// copy-up, and retain them on release. The existing command fake is the narrowest
// boundary proving exact engine arguments without requiring a live Docker host.
func TestLandingVolumes(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	root := t.TempDir()
	m.SetEnvironmentResolver(fakeResolver{root: root, img: EnvironmentImage{Image: "fixture-image", Path: "/usr/bin"}})
	for _, v := range []NamedVolume{
		{"Bad", "/opt/cache"}, {"", "/opt/cache"}, {strings.Repeat("a", 129), "/opt/cache"}, {"a,b", "/opt/cache"},
		{"ok", "relative"}, {"ok", "/opt/../cache"}, {"ok", root}, {"ok", root + "/cache"}, {"ok", "/"}, {"ok", "/opt/cache,readonly"},
	} {
		before := len(runner.calls)
		if _, err := m.AcquireLandingWithVolumes(context.Background(), root, root, []NamedVolume{v}); err == nil {
			t.Fatalf("accepted %+v", v)
		}
		if len(runner.calls) != before {
			t.Fatal("invalid mount mutated engine")
		}
	}
	volume := NamedVolume{Name: "swarm-runner-cache-0123456789abcdef-go-build", Dest: "/opt/go/cache"}
	landing, err := m.AcquireLandingWithVolumes(context.Background(), root, root, []NamedVolume{volume})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, args := range runner.calls {
		for i, arg := range args {
			if arg == "type=volume,src="+volume.Name+",dst="+volume.Dest {
				if i == 0 || args[i-1] != "--mount" {
					t.Fatal("not a mount argument")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("cache volume not mounted")
	}
	if err := landing.Release(); err != nil {
		t.Fatal(err)
	}
	if runner.ran("volume rm " + volume.Name) {
		t.Fatal("persistent cache removed")
	}
	before := len(runner.calls)
	ordinary, err := m.AcquireLanding(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range runner.calls[before:] {
		if strings.Contains(strings.Join(args, " "), volume.Name) {
			t.Fatal("ordinary landing inherited runner cache")
		}
	}
	if err := ordinary.Release(); err != nil {
		t.Fatal(err)
	}
}

// Purpose: AcquireLandingWithVolumes must reject checkout-contained mounts even
// when the checkout has a trailing slash. The Docker command fake proves the
// rejection happens before any container or volume mutation at the API boundary.
func TestLandingVolumesTrailingSlashCheckout(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	root := t.TempDir()
	m.SetEnvironmentResolver(fakeResolver{root: root, img: EnvironmentImage{Image: "fixture-image", Path: "/usr/bin"}})
	before := len(runner.calls)
	landing, err := m.AcquireLandingWithVolumes(context.Background(), root, root+"/", []NamedVolume{{Name: "cache", Dest: root + "/cache"}})
	if landing != nil {
		t.Cleanup(func() { _ = landing.Release() })
	}
	if err == nil {
		t.Error("accepted cache volume beneath trailing-slash checkout")
	}
	if len(runner.calls) != before {
		t.Error("checkout-contained mount mutated engine")
	}
}

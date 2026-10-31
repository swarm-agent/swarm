package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

// Purpose: a build's status write (Finish) never replaces fields the
// definition writers set: max_leases set by SetMaxLeases and a newer
// definition set by Define both survive a build that finishes concurrently,
// even when Finish read the record before those writes. Threat: the observed
// flake where update_environment with max_leases and wait_seconds read back
// max_leases 0 (Finish stored a record read before SetMaxLeases applied), and
// its twin that would revert a new definition to the old ready one.
// Boundary: SandboxEnvironmentStore.Finish, SetMaxLeases, DefineWithMaxLeases
// over a temporary Pebble store; sandboxEnvironmentFinishReadHook forces the
// bad ordering (the other writer runs after Finish read and before it writes).
func TestSandboxEnvironmentFinishNeverDropsDefinitionWrites(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	envs := NewSandboxEnvironmentStore(store)
	t.Cleanup(func() { sandboxEnvironmentFinishReadHook = nil })

	// raceFinish runs Finish for hash while write runs from inside Finish's
	// read-to-write window. Without serialization write completes inside the
	// window and Finish then overwrites it; with it, write waits for Finish.
	raceFinish := func(t *testing.T, path, hash string, write func() error) {
		t.Helper()
		done := make(chan error, 1)
		sandboxEnvironmentFinishReadHook = func() {
			go func() { done <- write() }()
			select {
			case err := <-done:
				done <- err // unserialized: write landed inside the window
			case <-time.After(100 * time.Millisecond):
			}
		}
		if _, err := envs.Finish(path, hash, SandboxEnvironmentImage(hash), "/usr/bin", "built", nil); err != nil {
			t.Fatal(err)
		}
		sandboxEnvironmentFinishReadHook = nil
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the concurrent definition write never completed")
		}
	}

	def := SandboxEnvironmentDefinition{BaseImage: "debian:12", Setup: []string{"apt-get install -y git"}, Env: map[string]string{"APP_MODE": "dev"}}
	path := "/work/race"
	two := 2
	env, build, err := envs.DefineWithMaxLeases("acct-a", path, def, &two)
	if err != nil || !build || env.MaxLeases != 2 || env.Status != SandboxEnvironmentBuilding {
		t.Fatalf("define with max_leases: %+v build=%v err=%v", env, build, err)
	}
	if got, _, _ := envs.Get(path); got.MaxLeases != 2 {
		t.Fatalf("max_leases not stored with the definition: %+v", got)
	}

	// max_leases set while the build finishes is kept, and the build is ready.
	raceFinish(t, path, env.Hash, func() error { _, err := envs.SetMaxLeases("acct-a", path, 3); return err })
	got, _, err := envs.Get(path)
	if err != nil || got.MaxLeases != 3 || got.Status != SandboxEnvironmentReady || got.ReadyHash != env.Hash || got.Definition.BaseImage != "debian:12" || len(got.Definition.Setup) != 1 || got.Definition.Env["APP_MODE"] != "dev" {
		t.Fatalf("finish dropped max_leases or status: %+v err=%v", got, err)
	}

	// A new definition written while the old build finishes is kept: the
	// stale result is ignored, the new build stays pending, max_leases stays.
	if _, err := envs.Rebuild("acct-a", path); err != nil {
		t.Fatal(err)
	}
	next := SandboxEnvironmentDefinition{BaseImage: "debian:12", Setup: []string{"apt-get install -y git", "cargo --version"}, Env: map[string]string{"APP_MODE": "dev", "X": "1"}}
	raceFinish(t, path, env.Hash, func() error { _, _, err := envs.Define("acct-a", path, next); return err })
	got, _, err = envs.Get(path)
	if err != nil || got.Hash != SandboxEnvironmentHash(next) || got.Status != SandboxEnvironmentBuilding || len(got.Definition.Setup) != 2 || got.Definition.Env["X"] != "1" || got.MaxLeases != 3 {
		t.Fatalf("finish replaced the newer definition: %+v err=%v", got, err)
	}

	// An out-of-range max_leases is refused before anything is written.
	bad := MaxLeasesLimit + 1
	if _, _, err := envs.DefineWithMaxLeases("acct-a", path, def, &bad); err == nil {
		t.Fatal("out-of-range max_leases accepted")
	}
	if after, _, _ := envs.Get(path); after.Hash != got.Hash || after.MaxLeases != 3 {
		t.Fatalf("refused define changed the record: %+v", after)
	}
}

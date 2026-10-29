package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: List returns only the caller account's folder environments, and
// Rebuild marks the current definition for a new build without changing it,
// refusing another account's folder, a folder without one, and a build
// already running. Threat: a home listing or rebuilding another account's
// environment, or a rebuild that loses the definition. Boundary:
// SandboxEnvironmentStore.List and Rebuild over a temporary Pebble store.
func TestSandboxEnvironmentListAndRebuild(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	envs := NewSandboxEnvironmentStore(store)
	def := SandboxEnvironmentDefinition{BaseImage: "debian:12", Setup: []string{"apt-get install -y git"}, Env: map[string]string{"APP_MODE": "dev"}}
	mine, _, err := envs.Define("acct-a", "/work/b", def)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := envs.Define("acct-a", "/work/a", def); err != nil {
		t.Fatal(err)
	}
	if _, _, err := envs.Define("acct-b", "/work/other", def); err != nil {
		t.Fatal(err)
	}
	list, err := envs.List("acct-a")
	if err != nil || len(list) != 2 || list[0].WorkspacePath != "/work/a" || list[1].WorkspacePath != "/work/b" {
		t.Fatalf("list: %+v %v", list, err)
	}

	if _, err := envs.Rebuild("acct-a", "/work/b"); !errors.Is(err, ErrSandboxEnvironmentBuilding) {
		t.Fatalf("rebuild while building: %v", err)
	}
	if _, err := envs.Finish("/work/b", mine.Hash, "", "", "log", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := envs.Rebuild("acct-a", "/work/b")
	if err != nil || rebuilt.Status != SandboxEnvironmentBuilding || rebuilt.Error != "" || rebuilt.Hash != mine.Hash || len(rebuilt.Definition.Setup) != 1 {
		t.Fatalf("rebuild: %+v %v", rebuilt, err)
	}
	if _, err := envs.Rebuild("acct-b", "/work/a"); !errors.Is(err, ErrSandboxEnvironmentNotFound) {
		t.Fatalf("cross-account rebuild: %v", err)
	}
	if _, err := envs.Rebuild("acct-a", "/work/none"); !errors.Is(err, ErrSandboxEnvironmentNotFound) {
		t.Fatalf("rebuild without environment: %v", err)
	}
	other, _, _ := envs.Get("/work/a")
	if other.Status != SandboxEnvironmentBuilding || other.Account != "acct-a" {
		t.Fatalf("refused rebuild changed the folder: %+v", other)
	}
}

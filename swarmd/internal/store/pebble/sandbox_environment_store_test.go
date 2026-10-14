package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a folder's sandbox environment (sandbox_environment_store.go) is
// what its jobs run on. Requirement: a definition is validated (no reserved
// or secret-steering variables, a real image reference, bounded setup);
// the same definition always has the same hash and image name; a failed or
// replaced build never takes away the folder's last ready image; a build
// result for a replaced definition is ignored; another account cannot
// replace or clear a folder's environment; and a restart never leaves a
// build "building". Threats: a definition that overrides the proxy or CA
// variables the secret gateway relies on, a folder bricked by a failed
// rebuild, a stale build overwriting a newer definition, and cross-account
// changes. Owner: SandboxEnvironmentStore over a real pebble store.

func newSandboxEnvironmentStore(t *testing.T) *SandboxEnvironmentStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewSandboxEnvironmentStore(store)
}

func TestSandboxEnvironmentDefinitionValidation(t *testing.T) {
	for name, def := range map[string]SandboxEnvironmentDefinition{
		"no image":        {},
		"bad image":       {BaseImage: "-rm node"},
		"swarm image":     {BaseImage: "swarm-env:abc"},
		"proxy override":  {BaseImage: "node:22", Env: map[string]string{"HTTPS_PROXY": "http://evil"}},
		"ca override":     {BaseImage: "node:22", Env: map[string]string{"SSL_CERT_FILE": "/x"}},
		"path override":   {BaseImage: "node:22", Env: map[string]string{"PATH": "/x"}},
		"preload":         {BaseImage: "node:22", Env: map[string]string{"LD_PRELOAD": "/x.so"}},
		"bad name":        {BaseImage: "node:22", Env: map[string]string{"1X": "y"}},
		"multiline value": {BaseImage: "node:22", Env: map[string]string{"X": "a\nb"}},
		"long command":    {BaseImage: "node:22", Setup: []string{strings.Repeat("x", MaxSandboxEnvironmentSetupLen+1)}},
	} {
		if _, err := NormalizeSandboxEnvironmentDefinition(def); err == nil {
			t.Errorf("%s: accepted %+v", name, def)
		}
	}
	def, err := NormalizeSandboxEnvironmentDefinition(SandboxEnvironmentDefinition{BaseImage: " golang:1.24 ", Setup: []string{" ", "go version "}, Env: map[string]string{"GOFLAGS": "-mod=mod"}})
	if err != nil || def.BaseImage != "golang:1.24" || len(def.Setup) != 1 || def.Setup[0] != "go version" {
		t.Fatalf("normalized %+v %v", def, err)
	}
	a := SandboxEnvironmentHash(SandboxEnvironmentDefinition{BaseImage: "x", Env: map[string]string{"A": "1", "B": "2"}})
	b := SandboxEnvironmentHash(SandboxEnvironmentDefinition{BaseImage: "x", Env: map[string]string{"B": "2", "A": "1"}})
	c := SandboxEnvironmentHash(SandboxEnvironmentDefinition{BaseImage: "x", Env: map[string]string{"A": "1", "B": "3"}})
	if a != b || a == c || SandboxEnvironmentImage(a) != "swarm-env:"+a[:24] {
		t.Fatalf("hashes %s %s %s", a, b, c)
	}
}

func TestSandboxEnvironmentKeepsLastReadyImage(t *testing.T) {
	s := newSandboxEnvironmentStore(t)
	folder := "/srv/app"
	first, build, err := s.Define("acct", folder, SandboxEnvironmentDefinition{BaseImage: "golang:1.24"})
	if err != nil || !build || first.Status != SandboxEnvironmentBuilding || first.ReadyImage != "" {
		t.Fatalf("define: %+v %v %v", first, build, err)
	}
	if _, again, _ := s.Define("acct", folder, SandboxEnvironmentDefinition{BaseImage: "golang:1.24"}); again {
		t.Fatal("the same definition while building must not start a second build")
	}
	ready, err := s.Finish(folder, first.Hash, "swarm-env:one", "/usr/local/go/bin:/usr/bin", "ok", nil)
	if err != nil || ready.Status != SandboxEnvironmentReady || ready.ReadyImage != "swarm-env:one" || ready.ReadyPath == "" {
		t.Fatalf("finish: %+v %v", ready, err)
	}

	second, build, err := s.Define("acct", folder, SandboxEnvironmentDefinition{BaseImage: "golang:1.25"})
	if err != nil || !build || second.ReadyImage != "swarm-env:one" {
		t.Fatalf("redefine must keep the ready image while building: %+v %v", second, err)
	}
	// A late result for the first definition is ignored.
	if stale, _ := s.Finish(folder, first.Hash, "swarm-env:stale", "", "", nil); stale.ReadyImage != "swarm-env:one" || stale.Status != SandboxEnvironmentBuilding {
		t.Fatalf("stale build result applied: %+v", stale)
	}
	failed, err := s.Finish(folder, second.Hash, "", "", strings.Repeat("l", MaxSandboxEnvironmentLogBytes+10), errors.New("setup command 1 failed"))
	if err != nil || failed.Status != SandboxEnvironmentFailed || failed.ReadyImage != "swarm-env:one" || len(failed.Log) != MaxSandboxEnvironmentLogBytes {
		t.Fatalf("a failed build must keep the last ready image: %+v %v", failed, err)
	}

	if _, _, err := s.Define("other", folder, SandboxEnvironmentDefinition{BaseImage: "node:22"}); err == nil {
		t.Fatal("another account replaced the folder's environment")
	}
	if err := s.Clear("other", folder); err == nil {
		t.Fatal("another account cleared the folder's environment")
	}
	if err := s.Clear("acct", folder); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get(folder); found {
		t.Fatal("cleared environment still present")
	}
}

func TestSandboxEnvironmentRestartEndsBuilds(t *testing.T) {
	s := newSandboxEnvironmentStore(t)
	env, _, err := s.Define("acct", "/srv/app", SandboxEnvironmentDefinition{BaseImage: "node:22"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InterruptBuilds(); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Get("/srv/app")
	if after.Status != SandboxEnvironmentFailed || after.Hash != env.Hash || !strings.Contains(after.Error, "restart") {
		t.Fatalf("interrupted build: %+v", after)
	}
	// Setting the same definition again rebuilds it.
	if _, build, err := s.Define("acct", "/srv/app", SandboxEnvironmentDefinition{BaseImage: "node:22"}); err != nil || !build {
		t.Fatalf("redefine after an interrupted build: %v %v", build, err)
	}
}

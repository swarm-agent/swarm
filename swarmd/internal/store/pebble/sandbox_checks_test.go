package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: NormalizeSandboxEnvironmentDefinition and SandboxEnvironmentHash own
// check bounds and image identity. This unit test prevents checks-only changes
// from rebuilding images and rejects excessive commands before persistence.
func TestSandboxChecksDefinition(t *testing.T) {
	def := SandboxEnvironmentDefinition{BaseImage: "ubuntu:24.04", Checks: []string{"go test ./pkg"}, CheckTimeoutMinutes: 20}
	normalized, err := NormalizeSandboxEnvironmentDefinition(def)
	if err != nil {
		t.Fatal(err)
	}
	hash := SandboxEnvironmentHash(normalized)
	normalized.Checks = []string{"false"}
	normalized.CheckTimeoutMinutes = 60
	if SandboxEnvironmentHash(normalized) != hash {
		t.Fatal("checks changed image hash")
	}
	for _, bad := range []SandboxEnvironmentDefinition{
		{BaseImage: def.BaseImage, Checks: make([]string, 11)},
		{BaseImage: def.BaseImage, Checks: []string{strings.Repeat("x", 4001)}},
		{BaseImage: def.BaseImage, CheckTimeoutMinutes: 61},
		{BaseImage: def.BaseImage, Checks: []string{"echo\x00secret"}},
	} {
		if _, err := NormalizeSandboxEnvironmentDefinition(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

// Purpose: SandboxEnvironmentStore.Define must persist checks-only changes
// without rebuilding or losing ready-image state, and reject foreign accounts
// without mutation. A real temporary Pebble store proves durable postconditions.
func TestSandboxChecksPersistWithoutBuild(t *testing.T) {
	s := newSandboxEnvironmentStore(t)
	folder := t.TempDir()
	def := SandboxEnvironmentDefinition{BaseImage: "ubuntu:24.04"}
	first, _, err := s.Define("a", folder, def)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Finish(folder, first.Hash, "image", "/usr/bin", "", nil); err != nil {
		t.Fatal(err)
	}
	def.Checks = []string{"false"}
	def.CheckTimeoutMinutes = 60
	updated, build, err := s.Define("a", folder, def)
	if err != nil || build || updated.Status != SandboxEnvironmentReady || updated.ReadyImage != "image" {
		t.Fatalf("%+v build=%v err=%v", updated, build, err)
	}
	got, found, err := s.Get(folder)
	if err != nil || !found || len(got.Definition.Checks) != 1 || got.Definition.Checks[0] != "false" {
		t.Fatalf("%+v %v", got, err)
	}
	def.Checks = []string{"true"}
	if _, _, err = s.Define("foreign", folder, def); err == nil {
		t.Fatal("foreign update accepted")
	}
	got, _, _ = s.Get(folder)
	if got.Definition.Checks[0] != "false" {
		t.Fatal("foreign mutation")
	}
}

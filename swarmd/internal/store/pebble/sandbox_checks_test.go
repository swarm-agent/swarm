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

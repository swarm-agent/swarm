package environments

import (
	"errors"
	"testing"
)

// Purpose: ValidateManagedBuildConnection owns transport admission. Persisted
// capability claims cannot turn SSH deployment support into supervised builds.
// This pure domain test is the narrowest proof of rejection and local parity.
func TestManagedBuildConnectionCapability(t *testing.T) {
	ssh := &Connection{Kind: ConnectionKindSSH, Capabilities: ConnectionCapabilities{SupportsDocker: true, SupportsPodman: true, RootlessSystemd: true}}
	before := *ssh
	if err := ValidateManagedBuildConnection(ssh); !errors.Is(err, ErrSSHManagedBuildUnavailable) {
		t.Fatalf("SSH capability claims admitted: %v", err)
	}
	if *ssh != before {
		t.Fatal("capability rejection mutated connection")
	}
	if err := ValidateManagedBuildConnection(&Connection{Kind: ConnectionKindLocalPodman}); err != nil {
		t.Fatalf("local Podman contract regressed: %v", err)
	}
	for _, conn := range []*Connection{nil, {Kind: ConnectionKindLocalDocker}, {Kind: "unknown"}} {
		if err := ValidateManagedBuildConnection(conn); err == nil {
			t.Fatal("unsupported connection admitted")
		}
	}
}

package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/config"
)

func TestNewHeadlessRejectsDesktopBeforeOpeningState(t *testing.T) {
	// Requirement: direct Headless construction must reject a Desktop listener.
	// Threat: a caller bypasses the CLI and opens state before rejection. Owner:
	// NewHeadless. The constructor with a fresh path is the narrowest layer
	// proving rejection and absence of filesystem effects, without live state.
	root := filepath.Join(t.TempDir(), "unopened")
	daemon, err := NewHeadless(config.Config{DesktopPort: 7780, DataDir: root, DBPath: filepath.Join(root, "db"), LockPath: filepath.Join(root, "lock")})
	if err == nil || daemon != nil {
		t.Fatal("Headless accepted a Desktop listener")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected composition touched state: %v", err)
	}
}

func TestNewHeadlessRejectsAutoBeforeOpeningState(t *testing.T) {
	// Requirement: direct construction must preserve headless no-fallback policy.
	// Threat: bypassing CLI validation reintroduces auto or touches live state.
	// Owner: NewHeadless; constructor refusal is the narrowest relevant boundary.
	root := filepath.Join(t.TempDir(), "unopened")
	d, err := NewHeadless(config.Config{SandboxMode: "auto", DataDir: root, DBPath: filepath.Join(root, "db"), LockPath: filepath.Join(root, "lock")})
	if err == nil || d != nil {
		t.Fatal("accepted auto host fallback")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected configuration touched state: %v", err)
	}
	mode, err := config.HeadlessSandboxMode("")
	if err != nil || mode != "required" {
		t.Fatal("direct constructor default is not required")
	}
}

package app

import (
	"os"
	"testing"
)

func TestAnnounceAppliedUpdateConsumesToastEnv(t *testing.T) {
	a := newCommandTestApp()
	t.Setenv(appliedUpdateToastEnv, "Updated to v1.2.3")

	a.announceAppliedUpdate()

	if got := a.home.Status(); got != "Updated to v1.2.3" {
		t.Fatalf("status = %q, want update toast", got)
	}
	if got := os.Getenv(appliedUpdateToastEnv); got != "" {
		t.Fatalf("%s still set to %q", appliedUpdateToastEnv, got)
	}
}

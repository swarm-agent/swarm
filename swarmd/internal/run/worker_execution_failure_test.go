package run

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: observeRun must propagate the canonical executor's sanitized durable
// BlockedReason rather than hide provider exhaustion. This pure policy test is
// the narrowest layer checking reason preservation and a meaningful fallback;
// it does not claim store/restart or live worker integration proof.
func TestWorkerExecutionFailureReason(t *testing.T) {
	reason := "provider unavailable (HTTP 503) after 4 attempts"
	if got := workerExecutionFailureReason(store.V3SessionRunIntent{Status: "failed", BlockedReason: reason}); got != reason {
		t.Fatalf("reason=%q", got)
	}
	if got := workerExecutionFailureReason(store.V3SessionRunIntent{Status: "expired"}); got != "execution expired" {
		t.Fatalf("fallback=%q", got)
	}
}

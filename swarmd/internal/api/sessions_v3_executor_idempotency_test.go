package api

import (
	"strings"
	"testing"
)

func TestSessionV3ExecutorJobMutationIdentityScopesContinuationEpochs(t *testing.T) {
	base := sessionV3ExecutorJob{RunID: "run-1", EpochID: "epoch-1"}
	continuation := sessionV3ExecutorJob{RunID: "run-1", EpochID: "epoch-2"}

	baseID := sessionV3ExecutorJobClientRequestID("session.provider.first_event", base)
	continuationID := sessionV3ExecutorJobClientRequestID("session.provider.first_event", continuation)
	if baseID == continuationID {
		t.Fatalf("continuation epoch reused mutation identity %q", baseID)
	}
	if !strings.Contains(baseID, "run-1") || !strings.Contains(continuationID, "run-1") {
		t.Fatalf("epoch-scoped identities lost canonical run id: base=%q continuation=%q", baseID, continuationID)
	}
	if got := sessionV3ExecutorJobClientRequestID("session.provider.first_event", base); got != baseID {
		t.Fatalf("same epoch identity is not stable: got=%q want=%q", got, baseID)
	}
}

func TestSessionV3ExecutorEpochScopedIDPreservesLegacyIdentityWithoutEpoch(t *testing.T) {
	const id = "v3-executor-session_provider_first_event-run-1"
	if got := sessionV3ExecutorEpochScopedID(id, ""); got != id {
		t.Fatalf("identity without epoch = %q, want %q", got, id)
	}
}

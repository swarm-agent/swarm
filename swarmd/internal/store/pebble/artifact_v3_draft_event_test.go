package pebblestore

import "testing"

// Requirement: recoverable native draft changes retain their native event type,
// rather than becoming a generic session event. This mapping test does not prove
// draft admission, durable persistence or realtime delivery; foundation tests do.
func TestArtifactV3DraftEventType(t *testing.T) {
	if got := normalizeV3SessionEventType(V3SessionMutationInput{Kind: "artifact.v3.draft.saved"}); got != "artifact.v3.draft.saved" {
		t.Fatalf("draft event type = %q", got)
	}
}

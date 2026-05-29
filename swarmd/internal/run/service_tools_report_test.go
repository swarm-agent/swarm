package run

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestTaskReportRefFromMessageUsesChildSessionTranscript(t *testing.T) {
	ref := taskReportRefFromMessage(pebblestore.MessageSnapshot{
		ID:        "msg_00000000000000000042",
		SessionID: "child-session-1",
		GlobalSeq: 42,
		Role:      "assistant",
		Content:   "full report body",
	})
	if ref == nil {
		t.Fatalf("expected report ref")
	}
	if ref.SessionID != "child-session-1" || ref.MessageID != "msg_00000000000000000042" || ref.GlobalSeq != 42 || ref.Role != "assistant" {
		t.Fatalf("unexpected report ref: %#v", ref)
	}
	if ref.Source != "child_session_transcript" {
		t.Fatalf("report ref source = %q, want child_session_transcript", ref.Source)
	}
}

func TestTaskReportRefFromMessageRequiresPersistedMessage(t *testing.T) {
	if ref := taskReportRefFromMessage(pebblestore.MessageSnapshot{SessionID: "child-session-1"}); ref != nil {
		t.Fatalf("expected nil ref for message without global seq, got %#v", ref)
	}
}

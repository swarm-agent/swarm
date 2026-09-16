package api

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: reconnect/hydrate must retain canonical Automation occurrence
// identity for protected composers and occurrence-specific handoffs. Threat:
// metadata filtering drops attribution or leaks unrelated private agent context.
// Authority: sessionsV3SyncSessionShell, shared by canonical sync reads. This
// boundary test checks observable retained/dropped fields, not source strings.
func TestAutomationV2SyncOccurrenceAttribution(t *testing.T) {
	metadata := map[string]any{"automation_v2_occurrence_id": "occurrence", "automation_v2_authoring_session_id": "author", "automation_v2_digest": "digest", "automation_v2_revision": uint64(5), "private_agent_prompt": "do not expose"}
	out, err := sessionsV3SyncSessionShell(store.SessionSnapshot{ID: "execution", Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"automation_v2_occurrence_id", "automation_v2_authoring_session_id", "automation_v2_digest", "automation_v2_revision"} {
		if out.Metadata[key] != metadata[key] {
			t.Fatalf("lost %s", key)
		}
	}
	if _, ok := out.Metadata["private_agent_prompt"]; ok {
		t.Fatal("private metadata leaked")
	}
	out.Metadata["automation_v2_occurrence_id"] = "changed"
	if metadata["automation_v2_occurrence_id"] != "occurrence" {
		t.Fatal("source mutated")
	}
}

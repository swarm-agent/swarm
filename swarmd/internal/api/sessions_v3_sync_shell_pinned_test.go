package api

import "testing"

// A session's pin (metadata "pinned") travels in the sync shell, so a client's
// list shows it after a bootstrap; other client keys still do not.
func TestSyncShellMetadataKeepsPinned(t *testing.T) {
	out := sessionsV3SyncShellMetadata(map[string]any{"pinned": true, "agent_name": "system-orchestrator", "console_colour": "red"})
	if out["pinned"] != true {
		t.Fatalf("pinned missing from shell metadata: %v", out)
	}
	if _, ok := out["console_colour"]; ok {
		t.Fatalf("an unknown client key reached the shell metadata: %v", out)
	}
}

package run

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestShouldGenerateMemorySessionTitleSkipsFlowLockedSessions(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]any
	}{
		{name: "explicit lock", metadata: map[string]any{"title_locked": true}},
		{name: "string explicit lock", metadata: map[string]any{"title_locked": "true"}},
		{name: "flow title source", metadata: map[string]any{"title_source": "flow_task"}},
		{name: "flow source", metadata: map[string]any{"source": "flow"}},
		{name: "flow owner transport", metadata: map[string]any{"owner_transport": "flow_scheduler"}},
		{name: "flow lineage", metadata: map[string]any{"lineage_kind": "flow"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if shouldGenerateMemorySessionTitle(pebblestore.SessionSnapshot{Title: "Refresh memory", Metadata: tc.metadata}) {
				t.Fatalf("expected memory title generation to be disabled for metadata %+v", tc.metadata)
			}
		})
	}
}

func TestShouldGenerateMemorySessionTitleAllowsUnownedUntitledSession(t *testing.T) {
	if !shouldGenerateMemorySessionTitle(pebblestore.SessionSnapshot{Title: "New Session"}) {
		t.Fatal("expected title generation for ordinary empty session")
	}
}

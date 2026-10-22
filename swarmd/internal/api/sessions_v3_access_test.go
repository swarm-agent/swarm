package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionAccessInstructions(t *testing.T) {
	if got := sessionAccessInstructions(pebblestore.SessionSnapshot{}); got != "" {
		t.Fatalf("a session without a choice got %q", got)
	}
	session := pebblestore.SessionSnapshot{Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "free", "machine": "none"}}}
	got := sessionAccessInstructions(session)
	for _, want := range []string{"start tasks freely", "nothing for them to approve", "no access"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions %q lack %q", got, want)
		}
	}
	if !isProtectedSessionsV3MetadataKey(pebblestore.SessionAccessMetadataKey) {
		t.Fatal("clients could set a session's access through metadata")
	}
	if err := validateSessionsV3CreateMetadata(map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "free"}}); err == nil {
		t.Fatal("create metadata could set a session's access")
	}
}

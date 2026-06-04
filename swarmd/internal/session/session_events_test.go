package session

import (
	"os"
	"strings"
	"testing"
)

func TestApplySessionMutationIsOnlyV3ServiceWriteBoundary(t *testing.T) {
	source, err := os.ReadFile("session_events.go")
	if err != nil {
		t.Fatalf("read session_events.go: %v", err)
	}
	body := string(source)
	if !strings.Contains(body, "func (s *Service) ApplySessionMutation(input SessionMutationInput) (SessionMutationResult, error) {") {
		t.Fatal("ApplySessionMutation service boundary is missing")
	}
	if !strings.Contains(body, "return s.store.ApplyV3SessionMutation(input)") {
		t.Fatal("ApplySessionMutation must delegate to the Pebble atomic mutation boundary")
	}
	for _, forbidden := range []string{
		"SetSession(",
		"PutSession",
		"PutJSON(",
		"NewBatch(",
		"dispatchRuntimeSession",
		"runtimeSession",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("session_events.go must not stitch V3 writes or dispatch runtime work directly; found %q", forbidden)
		}
	}
}

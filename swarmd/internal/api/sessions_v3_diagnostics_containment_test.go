package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestSessionV3StoreDiagnosticMetadataExcludesMutationPayloads(t *testing.T) {
	input := sessionruntime.SessionMutationInput{
		SessionID: "session-1", Kind: "append", EventType: "session.message.appended",
		ClientRequestID: "request-1", EventPayload: json.RawMessage(`{"secret":"large-payload-marker"}`),
	}
	metadata := sessionV3StoreDiagnosticMetadata(input, sessionruntime.SessionMutationResult{}, errors.New("sensitive-error-marker"))
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"large-payload-marker", "sensitive-error-marker", "mutation_input", "mutation_result", "incoming_event_payload"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostic metadata contains %q: %s", forbidden, text)
		}
	}
	if metadata["event_payload_bytes"] != len(input.EventPayload) || metadata["event_payload_sha256"] == "" || metadata["error_hash"] == "" {
		t.Fatalf("compact metadata missing size/hash fields: %+v", metadata)
	}
}

func TestSessionV3DiagnosticsRequireExplicitOptIn(t *testing.T) {
	t.Setenv("SWARM_V3_DIAGNOSTICS", "")
	if sessionV3DiagnosticsEnabled() {
		t.Fatal("diagnostics enabled without opt-in")
	}
	t.Setenv("SWARM_V3_DIAGNOSTICS", "true")
	if sessionV3DiagnosticsEnabled() {
		t.Fatal("diagnostics enabled for non-authoritative value")
	}
	t.Setenv("SWARM_V3_DIAGNOSTICS", "1")
	if !sessionV3DiagnosticsEnabled() {
		t.Fatal("diagnostics not enabled for explicit opt-in")
	}
}

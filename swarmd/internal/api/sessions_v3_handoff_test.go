package api

import (
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionV3ProviderHandoffPacketBoundsLongTranscript(t *testing.T) {
	messages := make([]pebblestore.MessageSnapshot, 0, 530)
	for i := 0; i < 500; i++ {
		messages = append(messages, pebblestore.MessageSnapshot{Role: "user", Content: "OLD-HUGE-TRANSCRIPT-" + strings.Repeat("x", 1000)})
	}
	messages = append(messages, pebblestore.MessageSnapshot{Role: "system", Content: "[context-compact] index=2 origin=manual\n\nCompacted recap:\nImportant compacted facts."})
	messages = append(messages, pebblestore.MessageSnapshot{Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","type":"tool.completed","tool_name":"read","call_id":"call_read","arguments":"{\"path\":\"file.go\"}","completed_output":"` + strings.Repeat("tool-output-", 80) + `"}`})
	messages = append(messages, pebblestore.MessageSnapshot{Role: "user", Content: "Recent user request"})
	messages = append(messages, pebblestore.MessageSnapshot{Role: "assistant", Content: "Recent assistant answer"})
	messages = append(messages, pebblestore.MessageSnapshot{Role: "user", Content: "RECENT-HUGE-SENTINEL-" + strings.Repeat("y", 1000)})

	exec := &sessionV3Executor{}
	packet, err := exec.sessionV3ProviderHandoffPacket(sessionV3ExecutorJob{}, sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}, messages, provideriface.Request{BoundaryReason: "provider_model_runtime_handoff", PreviousProviderLineageID: "old-lineage", ProviderLineageID: "new-lineage", ContextBranchID: "branch"}, sessionV3ProviderHandoffCaps{TailMessages: 6, ToolOutputChars: 40, TotalChars: 6000})
	if err != nil {
		t.Fatalf("handoff packet: %v", err)
	}
	if strings.Contains(packet, "OLD-HUGE-TRANSCRIPT") {
		t.Fatalf("handoff packet replayed old transcript: %s", packet)
	}
	if strings.Contains(packet, strings.Repeat("y", 500)) {
		t.Fatalf("handoff packet replayed unbounded recent visible message: %s", packet)
	}
	for _, want := range []string{"[provider-handoff]", "Important compacted facts", "Recent user request", "Recent assistant answer", "RECENT-HUGE-SENTINEL", "read call_id=call_read", "[truncated"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("handoff packet missing %q:\n%s", want, packet)
		}
	}
}

func TestSessionV3ProviderHandoffPacketFailsOverTotalCap(t *testing.T) {
	exec := &sessionV3Executor{}
	_, err := exec.sessionV3ProviderHandoffPacket(sessionV3ExecutorJob{}, sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}, []pebblestore.MessageSnapshot{{Role: "user", Content: strings.Repeat("x", 2000)}}, provideriface.Request{BoundaryReason: "provider_model_runtime_handoff", PreviousProviderLineageID: "old-lineage", ProviderLineageID: "new-lineage"}, sessionV3ProviderHandoffCaps{TailMessages: 4, ToolOutputChars: 100, TotalChars: 500})
	if err == nil || !strings.Contains(err.Error(), "exceeds safety cap") {
		t.Fatalf("expected safety cap error, got %v", err)
	}
}

func TestSessionV3ProviderRequiresBoundedHandoffOnlyAcrossLineage(t *testing.T) {
	if sessionV3ProviderRequiresBoundedHandoff(provideriface.Request{PreviousProviderLineageID: "same", ProviderLineageID: "same", BoundaryReason: "session_turn"}) {
		t.Fatalf("same lineage should not require handoff")
	}
	if !sessionV3ProviderRequiresBoundedHandoff(provideriface.Request{PreviousProviderLineageID: "old", ProviderLineageID: "new", BoundaryReason: "provider_model_runtime_handoff"}) {
		t.Fatalf("provider/model lineage change should require bounded handoff")
	}
}

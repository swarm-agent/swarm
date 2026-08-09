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
	// Recovery passes only the explicitly named epoch range; predecessor text is
	// not present for the handoff builder to rediscover.
	messages = messages[len(messages)-4:]
	packet, err := exec.sessionV3ProviderHandoffPacket(sessionV3ExecutorJob{}, sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}, messages, provideriface.Request{BoundaryReason: "provider_model_runtime_handoff", PreviousProviderLineageID: "old-lineage", PreviousProviderID: "anthropic", PreviousModel: "claude-sonnet-4", ProviderLineageID: "new-lineage", ContextBranchID: "branch", NewProviderID: "codex", NewModel: "gpt-5"}, sessionV3ProviderHandoffCaps{TailMessages: 6, ToolOutputChars: 40, TotalChars: 6000})
	if err != nil {
		t.Fatalf("handoff packet: %v", err)
	}
	if strings.Contains(packet, "OLD-HUGE-TRANSCRIPT") {
		t.Fatalf("handoff packet replayed old transcript: %s", packet)
	}
	if strings.Contains(packet, strings.Repeat("y", 500)) {
		t.Fatalf("handoff packet replayed unbounded recent visible message: %s", packet)
	}
	for _, want := range []string{"[provider-handoff]", "target_provider: codex", "target_model: gpt-5", "previous_provider: anthropic", "previous_model: claude-sonnet-4", "new_provider: codex", "new_model: gpt-5", "No compacted summary is available", "Recent user request", "Recent assistant answer", "RECENT-HUGE-SENTINEL", "read call_id=call_read", "[truncated"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("handoff packet missing %q:\n%s", want, packet)
		}
	}
}

func TestSessionV3ProviderHandoffPacketFitsOverTotalCap(t *testing.T) {
	exec := &sessionV3Executor{}
	packet, err := exec.sessionV3ProviderHandoffPacket(sessionV3ExecutorJob{}, sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}, []pebblestore.MessageSnapshot{{Role: "user", Content: strings.Repeat("x", 2000)}}, provideriface.Request{BoundaryReason: "provider_model_runtime_handoff", PreviousProviderLineageID: "old-lineage", ProviderLineageID: "new-lineage"}, sessionV3ProviderHandoffCaps{TailMessages: 4, ToolOutputChars: 100, TotalChars: 500})
	if err != nil {
		t.Fatalf("handoff packet: %v", err)
	}
	if got := len([]rune(packet)); got != 500 {
		t.Fatalf("handoff packet length = %d, want 500", got)
	}
	if !strings.Contains(packet, "provider-handoff exceeded") {
		t.Fatalf("handoff packet missing truncation marker: %s", packet)
	}
	if !strings.Contains(packet, "--- user ---") {
		t.Fatalf("handoff packet dropped recent conversation tail: %s", packet)
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

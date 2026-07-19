package uisettings

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExplorerServiceTierRoundTripsThroughUISettings(t *testing.T) {
	settings := UISettings{Agents: AgentSettings{Explorer: CompactAgentSettings{
		Provider: "CODEX", Model: "gpt-5.4", Thinking: "high", ServiceTier: "PRIORITY",
	}}}
	record := agentRecordFromSettings(settings.Agents)
	if record.Explorer.ServiceTier != "priority" {
		t.Fatalf("stored Explorer service tier = %q, want priority", record.Explorer.ServiceTier)
	}
	got := uiSettingsFromRecord(pebblestore.UISettingsRecord{Agents: *record})
	if got.Agents.Explorer.ServiceTier != "priority" {
		t.Fatalf("resolved Explorer service tier = %q, want priority", got.Agents.Explorer.ServiceTier)
	}
	if got.Agents.Coder.Provider != "" || got.Agents.Coder.Model != "" {
		t.Fatalf("default Coder settings = %#v, want empty override", got.Agents.Coder)
	}
}

func TestCoderServiceTierRoundTripsThroughUISettings(t *testing.T) {
	settings := UISettings{Agents: AgentSettings{Coder: CompactAgentSettings{
		Provider: "CODEX", Model: "gpt-5.4", Thinking: "high", ServiceTier: "PRIORITY",
	}}}
	record := agentRecordFromSettings(settings.Agents)
	if record.Coder.Provider != "codex" || record.Coder.Model != "gpt-5.4" || record.Coder.ServiceTier != "priority" {
		t.Fatalf("stored Coder settings = %#v", record.Coder)
	}
	got := uiSettingsFromRecord(pebblestore.UISettingsRecord{Agents: *record})
	if got.Agents.Coder.Provider != "codex" || got.Agents.Coder.Model != "gpt-5.4" || got.Agents.Coder.ServiceTier != "priority" {
		t.Fatalf("resolved Coder settings = %#v", got.Agents.Coder)
	}
}

func TestDefaultUISettingsEnableThinkingTags(t *testing.T) {
	settings := defaultUISettings()
	if !settings.Chat.ThinkingTags {
		t.Fatal("default thinking tags = false, want true")
	}
	if !settings.Chat.ShowHeader {
		t.Fatal("default show header = false, want true")
	}
	if !settings.Chat.ToolStream.ShowAnchor {
		t.Fatal("default tool stream anchor = false, want true")
	}
}

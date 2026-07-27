package uisettings

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFinderServiceTierRoundTripsThroughUISettings(t *testing.T) {
	settings := UISettings{Agents: AgentSettings{Finder: CompactAgentSettings{
		Provider: "CODEX", Model: "gpt-5.4", Thinking: "high", ServiceTier: "PRIORITY",
	}}}
	record := agentRecordFromSettings(settings.Agents)
	if record.Finder.ServiceTier != "priority" {
		t.Fatalf("stored Finder service tier = %q, want priority", record.Finder.ServiceTier)
	}
	got := uiSettingsFromRecord(pebblestore.UISettingsRecord{Agents: *record})
	if got.Agents.Finder.ServiceTier != "priority" {
		t.Fatalf("resolved Finder service tier = %q, want priority", got.Agents.Finder.ServiceTier)
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

func TestDesignerServiceTierRoundTripsThroughUISettings(t *testing.T) {
	settings := UISettings{Agents: AgentSettings{Designer: CompactAgentSettings{
		Provider: "OPENAI", Model: "utility-model", Thinking: "medium", ServiceTier: "PRIORITY",
	}}}
	record := agentRecordFromSettings(settings.Agents)
	if record.Designer.Provider != "openai" || record.Designer.Model != "utility-model" || record.Designer.ServiceTier != "priority" {
		t.Fatalf("stored Designer settings = %#v", record.Designer)
	}
	got := uiSettingsFromRecord(pebblestore.UISettingsRecord{Agents: *record})
	if got.Agents.Designer.Provider != "openai" || got.Agents.Designer.Model != "utility-model" || got.Agents.Designer.Thinking != "medium" || got.Agents.Designer.ServiceTier != "priority" {
		t.Fatalf("resolved Designer settings = %#v", got.Agents.Designer)
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

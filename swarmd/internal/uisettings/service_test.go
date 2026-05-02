package uisettings

import "testing"

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

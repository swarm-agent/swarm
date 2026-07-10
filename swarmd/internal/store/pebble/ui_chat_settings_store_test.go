package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestUISettingsStoreDefaultsEnableThinkingTags(t *testing.T) {
	defaults := DefaultUISettingsRecord()
	if !defaults.Chat.ThinkingTags {
		t.Fatal("default thinking tags = false, want true")
	}
	if !defaults.Chat.ShowHeader {
		t.Fatal("default show header = false, want true")
	}
	if !defaults.Chat.ToolStream.ShowAnchor {
		t.Fatal("default tool stream anchor = false, want true")
	}
	if defaults.Chat.FollowupCheckpointPolicyDefault != "auto_start" {
		t.Fatalf("default follow-up checkpoint policy = %q, want auto_start", defaults.Chat.FollowupCheckpointPolicyDefault)
	}
	if defaults.Chat.SidebarHideInactiveHours == nil || *defaults.Chat.SidebarHideInactiveHours != 12 {
		t.Fatalf("default sidebar hide inactive hours = %v, want 12", defaults.Chat.SidebarHideInactiveHours)
	}
}

func TestUISettingsStoreUpdateFromEmptyStorePreservesTrueDefaults(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ui-settings-update-empty.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	settings := NewUISettingsStore(store)
	record, err := settings.Update(UISettingsPatch{
		Swarm: &UISwarmSettingsRecord{Name: "Desk"},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if !record.Chat.ThinkingTags {
		t.Fatal("thinking tags = false after unrelated update from empty store, want true")
	}
	if !record.Chat.ShowHeader {
		t.Fatal("show header = false after unrelated update from empty store, want true")
	}
	if !record.Chat.ToolStream.ShowAnchor {
		t.Fatal("tool stream anchor = false after unrelated update from empty store, want true")
	}
	if record.Swarm.Name != "Desk" {
		t.Fatalf("swarm name = %q, want Desk", record.Swarm.Name)
	}

	stored, ok, err := settings.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false after Update, want true")
	}
	if !stored.Chat.ThinkingTags {
		t.Fatal("stored thinking tags = false, want true")
	}
}

func TestUISettingsStorePreservesExplicitNeverHideSidebarValue(t *testing.T) {
	never := 0
	record := NormalizeUISettingsRecordForExternal(UISettingsRecord{Chat: UIChatSettingsRecord{SidebarHideInactiveHours: &never}})
	if record.Chat.SidebarHideInactiveHours == nil || *record.Chat.SidebarHideInactiveHours != 0 {
		t.Fatalf("sidebar hide inactive hours = %v, want explicit Never (0)", record.Chat.SidebarHideInactiveHours)
	}
}

func TestUISettingsStoreCanPersistThinkingTagsDisabled(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ui-settings-thinking-disabled.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	settings := NewUISettingsStore(store)
	record, err := settings.Update(UISettingsPatch{
		Chat: &UIChatSettingsRecord{
			ShowHeader:            true,
			ShowHeaderSet:         true,
			ThinkingTags:          false,
			ThinkingTagsSet:       true,
			DefaultNewSessionMode: "auto",
			ToolStream: UIChatToolStreamSettingsRecord{
				ShowAnchor: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if record.Chat.ThinkingTags {
		t.Fatal("thinking tags = true after explicit disable, want false")
	}

	stored, ok, err := settings.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false after Update, want true")
	}
	if stored.Chat.ThinkingTags {
		t.Fatal("stored thinking tags = true after explicit disable, want false")
	}
}

func TestUISettingsStoreFollowupCheckpointPolicyDefaultNormalization(t *testing.T) {
	cases := map[string]string{
		"":                 "auto_start",
		"unknown":          "auto_start",
		"auto":             "auto_start",
		"auto_start":       "auto_start",
		"append_and_start": "auto_start",
		"ask":              "require_approval",
		"manual":           "require_approval",
		"require_approval": "require_approval",
	}
	for input, want := range cases {
		if got := normalizeFollowupCheckpointPolicyDefault(input); got != want {
			t.Fatalf("normalizeFollowupCheckpointPolicyDefault(%q) = %q, want %q", input, got, want)
		}
	}
}

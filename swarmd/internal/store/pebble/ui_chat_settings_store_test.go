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

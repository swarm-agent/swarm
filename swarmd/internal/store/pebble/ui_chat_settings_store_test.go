package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestUISettingsStoreDefaultsEnableThinkingTags(t *testing.T) {
	defaults := DefaultUISettingsRecord()
	if defaults.Theme.ActiveID != "tide" {
		t.Fatalf("default theme = %q, want tide", defaults.Theme.ActiveID)
	}
	if !defaults.Chat.ThinkingTags {
		t.Fatal("default thinking tags = false, want true")
	}
	if !defaults.Chat.ShowHeader {
		t.Fatal("default show header = false, want true")
	}
	if defaults.Chat.ShowTips == nil || !*defaults.Chat.ShowTips {
		t.Fatalf("default show tips = %v, want true", defaults.Chat.ShowTips)
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
	if defaults.Chat.PlanContextGuardEnabled == nil || !*defaults.Chat.PlanContextGuardEnabled || defaults.Chat.PlanContextGuardUsedPercent != 80 || defaults.Chat.PlanContextGuardMaxCompactions == nil || *defaults.Chat.PlanContextGuardMaxCompactions != 1 {
		t.Fatalf("unexpected plan context guard defaults: %+v", defaults.Chat)
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

func TestUISettingsStoreNormalizesLegacyPlanContextGuardFields(t *testing.T) {
	disabled := false
	tooMany := 99
	record := NormalizeUISettingsRecordForExternal(UISettingsRecord{Chat: UIChatSettingsRecord{
		PlanContextGuardEnabled:        &disabled,
		PlanContextGuardUsedPercent:    10,
		PlanContextGuardMaxCompactions: &tooMany,
		UpdatedAt:                      123,
	}})
	if record.Chat.PlanContextGuardEnabled == nil || *record.Chat.PlanContextGuardEnabled {
		t.Fatalf("explicit disabled guard was not preserved: %+v", record.Chat)
	}
	if record.Chat.PlanContextGuardUsedPercent != 50 {
		t.Fatalf("legacy guard threshold = %d, want clamped 50", record.Chat.PlanContextGuardUsedPercent)
	}
	if record.Chat.PlanContextGuardMaxCompactions == nil || *record.Chat.PlanContextGuardMaxCompactions != 3 {
		t.Fatalf("legacy max compactions = %v, want clamped 3", record.Chat.PlanContextGuardMaxCompactions)
	}

	missing := NormalizeUISettingsRecordForExternal(UISettingsRecord{Chat: UIChatSettingsRecord{UpdatedAt: 123}})
	if missing.Chat.PlanContextGuardEnabled == nil || !*missing.Chat.PlanContextGuardEnabled || missing.Chat.PlanContextGuardUsedPercent != 80 || missing.Chat.PlanContextGuardMaxCompactions == nil || *missing.Chat.PlanContextGuardMaxCompactions != 1 {
		t.Fatalf("legacy missing guard fields did not receive defaults: %+v", missing.Chat)
	}
}

func TestUISettingsStorePersistsPlanContextGuardPerAccount(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ui-settings-plan-guard.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := NewUISettingsStore(store)
	disabled := false
	zero := 0
	updated, err := settings.UpdateForAccount("account-a", UISettingsPatch{Chat: &UIChatSettingsRecord{
		PlanContextGuardEnabled: &disabled, PlanContextGuardUsedPercent: 95, PlanContextGuardMaxCompactions: &zero,
	}})
	if err != nil {
		t.Fatalf("update account guard: %v", err)
	}
	if updated.Chat.PlanContextGuardEnabled == nil || *updated.Chat.PlanContextGuardEnabled || updated.Chat.PlanContextGuardUsedPercent != 95 || updated.Chat.PlanContextGuardMaxCompactions == nil || *updated.Chat.PlanContextGuardMaxCompactions != 0 {
		t.Fatalf("updated guard settings = %+v", updated.Chat)
	}
	loaded, ok, err := settings.GetForAccount("account-a")
	if err != nil || !ok {
		t.Fatalf("get account guard ok=%v err=%v", ok, err)
	}
	if loaded.Chat.PlanContextGuardEnabled == nil || *loaded.Chat.PlanContextGuardEnabled || loaded.Chat.PlanContextGuardUsedPercent != 95 || loaded.Chat.PlanContextGuardMaxCompactions == nil || *loaded.Chat.PlanContextGuardMaxCompactions != 0 {
		t.Fatalf("persisted guard settings = %+v", loaded.Chat)
	}
	if _, ok, err := settings.GetForAccount("account-b"); err != nil || ok {
		t.Fatalf("account-b unexpectedly observed account-a settings ok=%v err=%v", ok, err)
	}
}

func TestUISettingsStoreNormalizesMissingThemeToTide(t *testing.T) {
	record := NormalizeUISettingsRecordForExternal(UISettingsRecord{})
	if record.Theme.ActiveID != "tide" {
		t.Fatalf("normalized missing theme = %q, want tide", record.Theme.ActiveID)
	}
}

func TestUISettingsStoreLegacyRecordDefaultsShowTipsOn(t *testing.T) {
	record := NormalizeUISettingsRecordForExternal(UISettingsRecord{
		Chat: UIChatSettingsRecord{UpdatedAt: 123},
	})
	if record.Chat.ShowTips == nil || !*record.Chat.ShowTips {
		t.Fatalf("legacy show tips = %v, want true", record.Chat.ShowTips)
	}
}

func TestUISettingsStorePersistsExplicitShowTipsOff(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ui-settings-tips-disabled.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	settings := NewUISettingsStore(store)
	disabled := false
	record, err := settings.Update(UISettingsPatch{
		Chat: &UIChatSettingsRecord{ShowTips: &disabled},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if record.Chat.ShowTips == nil || *record.Chat.ShowTips {
		t.Fatalf("updated show tips = %v, want false", record.Chat.ShowTips)
	}

	stored, ok, err := settings.Get()
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v error=%v", ok, err)
	}
	if stored.Chat.ShowTips == nil || *stored.Chat.ShowTips {
		t.Fatalf("stored show tips = %v, want false", stored.Chat.ShowTips)
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

func TestUISettingsRecordSchemaOmitsAgentModels(t *testing.T) {
	payload, err := json.Marshal(DefaultUISettingsRecord())
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if _, found := object["agents"]; found {
		t.Fatalf("UI settings schema still persists agents: %s", payload)
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

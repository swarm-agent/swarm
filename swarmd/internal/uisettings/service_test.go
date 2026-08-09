package uisettings

import (
	"encoding/json"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDefaultUISettingsEnableThinkingTags(t *testing.T) {
	settings := defaultUISettings()
	if !settings.Chat.ShowTips || !settings.Chat.ThinkingTags || !settings.Chat.ShowHeader || !settings.Chat.ToolStream.ShowAnchor {
		t.Fatalf("unexpected UI defaults: %+v", settings.Chat)
	}
}

func TestUISettingsServicePreservesExplicitShowTipsOff(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-tips.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewService(pebblestore.NewUISettingsStore(store))
	settings := defaultUISettings()
	settings.Chat.ShowTips = false
	if _, err := service.SetForAccount("account-a", settings); err != nil {
		t.Fatalf("SetForAccount(): %v", err)
	}
	loaded, err := service.GetForAccount("account-a")
	if err != nil {
		t.Fatalf("GetForAccount(): %v", err)
	}
	if loaded.Chat.ShowTips {
		t.Fatal("show tips = true after explicit false persistence")
	}
}

func TestUISettingsServiceRoundTripsNormalizedPlanContextGuard(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-plan-guard.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewService(pebblestore.NewUISettingsStore(store))
	settings := defaultUISettings()
	settings.Chat.PlanContextGuardEnabled = false
	settings.Chat.PlanContextGuardUsedPercent = 12
	settings.Chat.PlanContextGuardMaxCompactions = 9
	saved, err := service.SetForAccount("account-a", settings)
	if err != nil {
		t.Fatalf("SetForAccount(): %v", err)
	}
	if saved.Chat.PlanContextGuardEnabled || saved.Chat.PlanContextGuardUsedPercent != 12 || saved.Chat.PlanContextGuardMaxCompactions != 3 {
		t.Fatalf("saved guard settings = %+v", saved.Chat)
	}
	loaded, err := service.GetForAccount("account-a")
	if err != nil {
		t.Fatalf("GetForAccount(): %v", err)
	}
	if loaded.Chat.PlanContextGuardEnabled || loaded.Chat.PlanContextGuardUsedPercent != 12 || loaded.Chat.PlanContextGuardMaxCompactions != 3 {
		t.Fatalf("loaded guard settings = %+v", loaded.Chat)
	}
}

func TestUISettingsServiceDoesNotExposeOrPersistAgentModels(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewService(pebblestore.NewUISettingsStore(store))
	stored, err := service.SetForAccount("account-a", defaultUISettings())
	if err != nil {
		t.Fatalf("SetForAccount(): %v", err)
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal service response: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode service response: %v", err)
	}
	if _, found := object["agents"]; found {
		t.Fatalf("service response exposed agent models: %s", payload)
	}
	persisted, found, err := store.GetBytes(pebblestore.KeyUISettingsForAccount("account-a"))
	if err != nil || !found {
		t.Fatalf("read persisted UI settings found=%v err=%v", found, err)
	}
	if err := json.Unmarshal(persisted, &object); err != nil {
		t.Fatalf("decode persisted UI settings: %v", err)
	}
	if _, found := object["agents"]; found {
		t.Fatalf("persisted UI settings retained agent models: %s", persisted)
	}
}

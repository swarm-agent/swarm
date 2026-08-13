package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	uisettings "swarm/packages/swarmd/internal/uisettings"
)

type manageThemeSettingsStub struct {
	settings uisettings.UISettings
	saves    int
}

func (s *manageThemeSettingsStub) Get() (uisettings.UISettings, error) { return s.settings, nil }
func (s *manageThemeSettingsStub) GetForAccount(string) (uisettings.UISettings, error) {
	return s.settings, nil
}
func (s *manageThemeSettingsStub) Set(settings uisettings.UISettings) (uisettings.UISettings, error) {
	s.settings = settings
	s.saves++
	return settings, nil
}
func (s *manageThemeSettingsStub) SetForAccount(_ string, settings uisettings.UISettings) (uisettings.UISettings, error) {
	s.settings = settings
	s.saves++
	return settings, nil
}

func TestManageThemeCreateBatchPreviewAndConfirm(t *testing.T) {
	settings := &manageThemeSettingsStub{settings: uisettings.UISettings{Theme: uisettings.ThemeSettings{ActiveID: "crimson"}}}
	runtime := NewRuntime(1)
	runtime.SetManageThemeServices(settings, nil)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}}
	arguments := map[string]any{
		"action": "create_batch",
		"themes": []any{
			map[string]any{"id": "dawn", "name": "Dawn", "base_theme_id": "crimson"},
			map[string]any{"id": "dusk", "name": "Dusk", "base_theme_id": "black"},
		},
	}

	preview := executeManageThemeTestCall(t, runtime, scope, arguments)
	if preview["status"] != "proposed_create_batch" || preview["generated_count"] != float64(2) {
		t.Fatalf("preview metadata = %#v", preview)
	}
	if got := manageThemeTestStringSlice(preview["generated_names"]); strings.Join(got, ",") != "Dawn,Dusk" {
		t.Fatalf("generated_names = %v", got)
	}
	if settings.saves != 0 || len(settings.settings.Theme.CustomThemes) != 0 {
		t.Fatalf("preview mutated settings: saves=%d themes=%d", settings.saves, len(settings.settings.Theme.CustomThemes))
	}

	arguments["confirm"] = true
	created := executeManageThemeTestCall(t, runtime, scope, arguments)
	if created["status"] != "ok" || created["applied"] != true {
		t.Fatalf("confirmed metadata = %#v", created)
	}
	if settings.saves != 1 || len(settings.settings.Theme.CustomThemes) != 2 {
		t.Fatalf("confirmed batch was not saved atomically: saves=%d themes=%d", settings.saves, len(settings.settings.Theme.CustomThemes))
	}
}

func TestManageThemeCreateBatchRejectsDuplicateIDs(t *testing.T) {
	settings := &manageThemeSettingsStub{settings: uisettings.UISettings{Theme: uisettings.ThemeSettings{ActiveID: "crimson"}}}
	runtime := NewRuntime(1)
	runtime.SetManageThemeServices(settings, nil)
	arguments := map[string]any{
		"action": "create_batch",
		"themes": []any{
			map[string]any{"id": "same", "name": "First", "base_theme_id": "crimson"},
			map[string]any{"id": "same", "name": "Second", "base_theme_id": "black"},
		},
	}
	raw, _ := json.Marshal(arguments)
	result := runtime.ExecuteBatch(context.Background(), "", []Call{{Name: "manage-theme", Arguments: string(raw)}})
	if len(result) != 1 || !strings.Contains(result[0].Error, "duplicate theme id") {
		t.Fatalf("result = %#v", result)
	}
	if settings.saves != 0 {
		t.Fatalf("invalid batch saved settings %d times", settings.saves)
	}
}

func executeManageThemeTestCall(t *testing.T, runtime *Runtime, scope WorkspaceScope, arguments map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.ExecuteBatch(WithWorkspaceScope(context.Background(), scope), "", []Call{{Name: "manage-theme", Arguments: string(raw)}})
	if len(result) != 1 || result[0].Error != "" {
		t.Fatalf("result = %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result[0].Output), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func manageThemeTestStringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

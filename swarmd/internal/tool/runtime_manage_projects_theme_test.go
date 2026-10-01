package tool

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/uisettings"
)

// Requirement: manage_projects assigns only saved catalog themes and clears on
// explicit empty ID; invalid selections never mutate a project or its metadata.
// Boundary: executeManageProjects account lookup and UpdateProject callback.
func TestManageProjectsThemeSelection(t *testing.T) {
	rt := NewRuntime(1)
	projects := newMockProjectStore()
	rt.SetManageProjectStore(projects)
	rt.SetManageThemeServices(&manageThemeSettingsStub{settings: uisettings.UISettings{Theme: uisettings.ThemeSettings{CustomThemes: []uisettings.ThemeCustomTheme{{ID: "saved", Name: "Saved"}}}}}, nil)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "owner"}}
	invoke := func(args map[string]any) (string, error) {
		return rt.executeManageProjects(context.Background(), scope, args)
	}
	if _, err := invoke(map[string]any{"action": "create", "name": "bad", "theme_id": "unknown"}); err == nil {
		t.Fatal("unknown theme accepted on create")
	}
	if len(projects.projects) != 0 {
		t.Fatal("rejected create mutated project store")
	}
	raw, err := invoke(map[string]any{"action": "create", "name": "kept", "theme_id": "saved", "description": "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Project struct {
			ID      string `json:"id"`
			ThemeID string `json:"theme_id"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	if id == "" || created.Project.ThemeID != "saved" {
		t.Fatalf("created: %+v", created)
	}
	for _, invalid := range []any{"unknown", 42, nil} {
		if _, err := invoke(map[string]any{"action": "update", "id": id, "name": "tampered", "theme_id": invalid}); err == nil {
			t.Fatalf("accepted invalid theme %v", invalid)
		}
		stored, _, _ := projects.GetProject("owner", id)
		if stored.Name != "kept" || stored.ThemeID != "saved" {
			t.Fatalf("invalid update mutated: %+v", stored)
		}
	}
	if _, err := invoke(map[string]any{"action": "update", "id": id, "theme_id": "", "description": "preserved"}); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := projects.GetProject("owner", id)
	if stored.ThemeID != "" || stored.Description != "preserved" {
		t.Fatalf("clear lost metadata: %+v", stored)
	}
	if _, err := invoke(map[string]any{"action": "update", "id": id, "theme_id": "nord"}); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = projects.GetProject("owner", id)
	if stored.ThemeID != "nord" {
		t.Fatalf("builtin not stored: %+v", stored)
	}
	foreign := scope
	foreign.Principal.AccountScopeID = "foreign"
	if _, err := rt.executeManageProjects(context.Background(), foreign, map[string]any{"action": "update", "id": id, "theme_id": "saved"}); err == nil {
		t.Fatal("foreign account updated owner project")
	}
}

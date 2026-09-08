package tool

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	uisettings "swarm/packages/swarmd/internal/uisettings"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type themeWorkspaceTargetStub struct {
	root, lookedUp, written string
	ancestor, missing       bool
}

func (s *themeWorkspaceTargetStub) ScopeForPathForPrincipal(_ identity.Principal, path string) (workspaceruntime.Scope, error) {
	s.lookedUp = path
	root := s.root
	if s.ancestor {
		root = filepath.Dir(root)
	}
	return workspaceruntime.Scope{ResolvedPath: path, WorkspacePath: root, Matched: !s.missing}, nil
}
func (s *themeWorkspaceTargetStub) SetThemeIDForPrincipal(_ identity.Principal, path, themeID string) (workspaceruntime.Resolution, error) {
	if path != s.root {
		return workspaceruntime.Resolution{}, errors.New("wrong theme target")
	}
	s.written = path
	return workspaceruntime.Resolution{}, nil
}
func (s *themeWorkspaceTargetStub) ListKnownForPrincipal(identity.Principal, int) ([]workspaceruntime.Entry, error) {
	return nil, nil
}

// Requirement: theme writes belong to the saved source workspace, never a managed
// lane or its ancestor. Exercise manageThemeSet/manageThemeUpsert/
// manageThemeCreateBatch directly: this is the narrowest layer that can observe
// both selected service paths and absence of settings writes on rejection.
func TestManageThemeManagedWorkspaceTarget(t *testing.T) {
	for _, action := range []string{"set", "create", "update", "create_batch"} {
		for _, failure := range []string{"", "missing-source", "ancestor", "unsaved"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				source := filepath.Join(root, "project")
				scope := WorkspaceScope{PrimaryPath: filepath.Join(root, "lanes", "job"), SourceWorkspacePath: source, WorktreeEnabled: true,
					Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}}
				workspace := &themeWorkspaceTargetStub{root: source, ancestor: failure == "ancestor", missing: failure == "unsaved"}
				settings := &manageThemeSettingsStub{settings: uisettings.UISettings{Theme: uisettings.ThemeSettings{ActiveID: "tide"}}}
				runtime := NewRuntime(1)
				runtime.SetManageThemeServices(settings, workspace)
				args := map[string]any{"theme_id": "nord"}
				invoke := func(confirm bool) (string, error) { return runtime.manageThemeSet(scope, args, confirm) }
				if action == "create" || action == "update" {
					args = map[string]any{"theme_id": "local", "name": "Local", "base_theme_id": "nord"}
					if action == "update" {
						settings.settings.Theme.CustomThemes = []uisettings.ThemeCustomTheme{{ID: "local", Name: "Local"}}
						args["apply_to"] = "workspace"
					}
					invoke = func(confirm bool) (string, error) {
						return runtime.manageThemeUpsert(scope, args, action == "update", confirm)
					}
				} else if action == "create_batch" {
					args = map[string]any{"themes": []any{map[string]any{"id": "local", "name": "Local", "base_theme_id": "nord"}}, "apply_to": "workspace", "apply_theme_id": "local"}
					invoke = func(confirm bool) (string, error) { return runtime.manageThemeCreateBatch(scope, args, confirm) }
				}
				if failure == "missing-source" {
					scope.SourceWorkspacePath = ""
				}
				for _, confirm := range []bool{false, true} {
					_, err := invoke(confirm)
					if failure != "" {
						if err == nil || workspace.written != "" || settings.saves != 0 {
							t.Fatalf("unsafe failure: err=%v workspace=%+v saves=%d", err, workspace, settings.saves)
						}
						if failure == "missing-source" && (!strings.Contains(err.Error(), "source workspace") || workspace.lookedUp != "") {
							t.Fatalf("missing source reached workspace lookup: %v %+v", err, workspace)
						}
						continue
					}
					if err != nil || workspace.lookedUp != source {
						t.Fatalf("wrong lookup: err=%v workspace=%+v", err, workspace)
					}
					if !confirm && (workspace.written != "" || settings.saves != 0) {
						t.Fatal("preview mutated settings")
					}
					if confirm && (workspace.written != source || settings.settings.Theme.ActiveID != "tide") {
						t.Fatal("confirmation did not preserve workspace-only application")
					}
				}
			})
		}
	}
}

// Requirement: inspection and explicit current-lane targeting use the same
// source identity as writes; explicit other saved workspaces remain selectable.
func TestManageThemeWorkspacePathSelection(t *testing.T) {
	scope := WorkspaceScope{PrimaryPath: "lane", WorktreeRootPath: "lane", SourceWorkspacePath: "source", WorktreeEnabled: true}
	for _, requested := range []string{"", "lane", "other"} {
		want := "source"
		if requested == "other" {
			want = "other"
		}
		got, err := manageThemeWorkspacePath(scope, map[string]any{"workspace_path": requested})
		if err != nil || got != want {
			t.Fatalf("request %q: got %q, %v", requested, got, err)
		}
	}
	contract := manageThemeActionContracts(scope)["workspace_default"].(map[string]any)
	if contract["active_workspace_path"] != "source" {
		t.Fatalf("contract = %#v", contract)
	}
}

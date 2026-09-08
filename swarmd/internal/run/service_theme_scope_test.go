package run

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type themeDispatchSettings struct{ saves int }

func (s *themeDispatchSettings) Get() (uisettings.UISettings, error) {
	return uisettings.UISettings{Theme: uisettings.ThemeSettings{ActiveID: "tide"}}, nil
}
func (s *themeDispatchSettings) GetForAccount(string) (uisettings.UISettings, error) { return s.Get() }
func (s *themeDispatchSettings) Set(v uisettings.UISettings) (uisettings.UISettings, error) {
	s.saves++
	return v, nil
}
func (s *themeDispatchSettings) SetForAccount(_ string, v uisettings.UISettings) (uisettings.UISettings, error) {
	return s.Set(v)
}

type themeDispatchWorkspace struct {
	source  string
	writes  []string
	missing bool
}

func (s *themeDispatchWorkspace) ScopeForPathForPrincipal(p identity.Principal, path string) (workspaceruntime.Scope, error) {
	if p.AccountScopeID != "account" {
		return workspaceruntime.Scope{}, fmt.Errorf("wrong account")
	}
	return workspaceruntime.Scope{RequestedPath: path, ResolvedPath: path, WorkspacePath: s.source, Matched: !s.missing}, nil
}
func (s *themeDispatchWorkspace) SetThemeIDForPrincipal(p identity.Principal, path, themeID string) (workspaceruntime.Resolution, error) {
	if p.AccountScopeID != "account" || path != s.source || themeID != "nord" {
		return workspaceruntime.Resolution{}, fmt.Errorf("wrong theme mutation")
	}
	s.writes = append(s.writes, path)
	return workspaceruntime.Resolution{}, nil
}
func (s *themeDispatchWorkspace) ListKnownForPrincipal(identity.Principal, int) ([]workspaceruntime.Entry, error) {
	return nil, nil
}

// Requirement: the actual theme permission dispatcher must preserve the durable
// source identity through buildPermissionWorkspaceScope and runtime context
// normalization. Helper-only tests miss this boundary. Inspect, preview, direct
// confirmation and approval replay must target the source, with no writes on
// missing identity or revoked catalog membership and no account-theme mutation.
func TestThemePermissionDispatchSavedWorkspace(t *testing.T) {
	for _, failure := range []string{"", "missing-source", "unsaved"} {
		t.Run(failure, func(t *testing.T) {
			store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			events, err := pebblestore.NewEventLog(store)
			if err != nil {
				t.Fatal(err)
			}
			sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
			source, lane := t.TempDir(), t.TempDir()
			metadata := map[string]any{"swarm_v3_source_workspace_path": source}
			if failure == "missing-source" {
				delete(metadata, "swarm_v3_source_workspace_path")
			}
			session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
				SessionID: "theme-session", UserID: "user", AccountScopeID: "account", WorkspacePath: lane, Mode: sessionruntime.ModeAuto,
				Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "medium"},
				Worktree:   &sessionruntime.CreateSessionWorktree{RootPath: lane, BranchName: "agent/theme", BaseBranch: "dev"}, Metadata: metadata,
			})
			if err != nil {
				t.Fatal(err)
			}
			settings := &themeDispatchSettings{}
			workspace := &themeDispatchWorkspace{source: source, missing: failure == "unsaved"}
			runtime := tool.NewRuntime(1)
			runtime.SetManageThemeServices(settings, workspace)
			svc := &Service{sessions: sessions, tools: runtime}
			inspect, err := svc.executeManageThemeTool(session.ID, tool.Call{Name: "manage-theme", Arguments: `{"action":"inspect"}`}, "")
			if err != nil {
				t.Fatal(err)
			}
			var inspected map[string]any
			if err := json.Unmarshal([]byte(inspect), &inspected); err != nil {
				t.Fatal(err)
			}
			defaultPath := inspected["action_contracts"].(map[string]any)["workspace_default"].(map[string]any)["active_workspace_path"]
			want := source
			if failure == "missing-source" {
				want = ""
			}
			if defaultPath != want {
				t.Fatalf("advertised default %v, want %q", defaultPath, want)
			}
			call := tool.Call{Name: "manage-theme", Arguments: `{"action":"set","theme_id":"nord"}`}
			preview, err := svc.buildManageThemePermissionPayload(session.ID, call)
			if failure != "" {
				if err == nil {
					t.Fatal("unsafe preview succeeded")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				change := preview["change"].(map[string]any)
				if change["workspace_path"] != source {
					t.Fatalf("wrong preview: %v", change)
				}
			}
			if len(workspace.writes) != 0 || settings.saves != 0 {
				t.Fatal("preview mutated state")
			}
			for _, feedback := range []string{"", `{"approved_arguments":{"action":"set","theme_id":"nord","confirm":true}}`} {
				call.Arguments = `{"action":"set","theme_id":"nord","confirm":true}`
				_, err = svc.executeManageThemeTool(session.ID, call, feedback)
				if failure != "" {
					if err == nil || len(workspace.writes) != 0 || settings.saves != 0 {
						t.Fatalf("unsafe rejection: %v, writes=%v saves=%d", err, workspace.writes, settings.saves)
					}
					if failure == "missing-source" && !strings.Contains(err.Error(), "source workspace") {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if failure == "" && (len(workspace.writes) != 2 || settings.saves != 0) {
				t.Fatalf("writes=%v account saves=%d", workspace.writes, settings.saves)
			}
		})
	}
}

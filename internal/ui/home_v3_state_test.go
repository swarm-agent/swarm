package ui

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestHomepageStateCapturesComposerAndSessionIntentBoundary(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ActiveAgent:        "swarm",
		ModelProvider:      "codex",
		ModelName:          "gpt-test",
		ThinkingLevel:      "high",
		ActiveModelProfile: model.ActiveModelProfile{Source: "saved", ProfileID: "profile-1", Name: "Profile"},
		Workspaces: []model.Workspace{{
			Name:                    "Default",
			Path:                    "/default",
			WorkspaceID:             "workspace-1",
			WorkspaceGeneration:     7,
			LocalWorkspaceBindingID: "binding-1",
			Active:                  true,
		}},
	})
	page.SetPrompt("build the feature")
	page.SetSessionIntent(HomeSessionIntent{
		Workspace:  HomepageWorkspaceSelection{Name: "Default", Path: "/default", WorkspaceID: "workspace-1", WorkspaceGeneration: 7, LocalWorkspaceBindingID: "binding-1"},
		Agent:      "swarm",
		Mode:       "auto",
		Preference: client.ModelPreference{Provider: "codex", Model: "gpt-test", Thinking: "high"},
		RouteID:    "host:binding:binding-1",
	})

	state := page.HomepageState()
	if state.SelectedWorkspace.Path != "/default" || state.SelectedWorkspace.LocalWorkspaceBindingID != "binding-1" {
		t.Fatalf("selected workspace = %#v", state.SelectedWorkspace)
	}
	if state.SelectedAgent != "swarm" || state.ModelProvider != "codex" || state.ModelName != "gpt-test" || state.Profile.Name != "Profile" || state.ComposerInput != "build the feature" {
		t.Fatalf("homepage state = %#v", state)
	}
	intent := page.SessionIntent()
	if intent.InitialPrompt != "build the feature" || intent.RouteID != "host:binding:binding-1" {
		t.Fatalf("session intent = %#v", intent)
	}
}

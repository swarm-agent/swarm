package ui

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestSessionIntentUsesCurrentHomepageModeAndModeModel(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ActiveAgent:             "swarm",
		ActiveAgentExitPlanMode: true,
		AutoModelProvider:       "codex",
		AutoModelName:           "auto-model",
		AutoThinkingLevel:       "medium",
		PlanModelProvider:       "codex",
		PlanModelName:           "plan-model",
		PlanThinkingLevel:       "high",
	})
	page.SetSessionMode("auto")
	page.SetSessionIntent(HomeSessionIntent{Mode: "auto", Preference: client.ModelPreference{Provider: "codex", Model: "auto-model", Thinking: "medium"}})

	page.SetSessionMode("plan")
	planIntent := page.SessionIntent()
	if planIntent.Mode != "plan" || planIntent.Preference.Model != "plan-model" || planIntent.Preference.Thinking != "high" {
		t.Fatalf("plan intent = %#v, want current plan mode/model", planIntent)
	}

	page.SetSessionMode("auto")
	autoIntent := page.SessionIntent()
	if autoIntent.Mode != "auto" || autoIntent.Preference.Model != "auto-model" || autoIntent.Preference.Thinking != "medium" {
		t.Fatalf("auto intent = %#v, want current auto mode/model", autoIntent)
	}

	projectedPlan := page.SessionIntentForMode("plan")
	if projectedPlan.Mode != "plan" || projectedPlan.Preference.Model != "plan-model" || projectedPlan.Preference.Thinking != "high" {
		t.Fatalf("projected plan intent = %#v, want canonical plan selection", projectedPlan)
	}
	if page.SessionMode() != "auto" || page.SessionIntent().Preference.Model != "auto-model" {
		t.Fatalf("projecting plan intent mutated homepage mode: mode=%q intent=%#v", page.SessionMode(), page.SessionIntent())
	}
}

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

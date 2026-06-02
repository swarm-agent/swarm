package ui

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeFooterUsesActiveRouteAsSwarmLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "swarm:remote:/repo",
		ChatRoutes: []model.ChatRoute{
			{ID: "swarm:primary:binding:primary-binding", Label: "Primary Desk", SwarmID: "primary", WorkspaceBindingID: "primary-binding", TargetKind: "host", TargetRelationship: "self"},
			{ID: "swarm:remote:/repo", Label: "Remote Desk", SwarmID: "remote", WorkspaceBindingID: "remote-binding"},
		},
	})
	page.SetSwarmName("Ignored Desk")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "Remote Desk" {
		t.Fatalf("home primary footer token = %q, want active route swarm name", tokens[0].Text)
	}
	if tokens[0].Action != "cycle-route" {
		t.Fatalf("home primary footer action = %q, want cycle-route", tokens[0].Action)
	}
	for _, token := range tokens {
		if strings.HasPrefix(token.Text, "[r:") {
			t.Fatalf("home footer still renders separate route token %q", token.Text)
		}
	}
}

func TestHomeFooterUsesPrimarySelfHostRouteLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "swarm:primary:binding:primary-binding",
		CurrentSwarmTarget:  &model.SwarmTarget{SwarmID: "primary", Name: "Primary Desk", Relationship: "self", Kind: "host"},
		ChatRoutes: []model.ChatRoute{
			{ID: "swarm:primary:binding:primary-binding", Label: "Stored Stale", SwarmID: "primary", WorkspaceBindingID: "primary-binding", TargetKind: "host", TargetRelationship: "self"},
			{ID: "swarm:container:binding:container-binding", Label: "Container Desk", SwarmID: "container", WorkspaceBindingID: "container-binding", TargetKind: "container", TargetRelationship: "child"},
		},
	})
	page.SetSwarmName("Ignored Desk")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "Primary Desk" {
		t.Fatalf("home primary footer token = %q, want primary self host route label", tokens[0].Text)
	}
}

func TestHomeFooterDoesNotFallbackToSwarmNameForMissingRouteLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "swarm:primary:binding:primary-binding",
		ChatRoutes:          []model.ChatRoute{{ID: "swarm:primary:binding:primary-binding", SwarmID: "primary", WorkspaceBindingID: "primary-binding"}},
	})
	page.SetSwarmName("Ignored Desk")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "" {
		t.Fatalf("home primary footer token = %q, want empty hydrated label", tokens[0].Text)
	}
}

func TestChatFooterUsesActiveRouteAsSwarmLabel(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "Ignored Desk",
		Meta:           ChatSessionMeta{Route: "Remote Desk"},
	})

	tokens := page.footerSettingsTokens()
	if len(tokens) == 0 {
		t.Fatal("footerSettingsTokens() returned no tokens")
	}
	if tokens[0].Text != "Remote Desk" {
		t.Fatalf("chat primary footer token = %q, want active route swarm name", tokens[0].Text)
	}
	if tokens[0].Action != "cycle-route" {
		t.Fatalf("chat primary footer action = %q, want cycle-route", tokens[0].Action)
	}
	for _, token := range tokens {
		if strings.HasPrefix(token.Text, "[r:") {
			t.Fatalf("chat footer still renders separate route token %q", token.Text)
		}
	}
}

func TestChatFooterDoesNotFallbackToSwarmNameForHostRoute(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "Ignored Desk",
		Meta:           ChatSessionMeta{Route: "host"},
	})

	tokens := page.footerSettingsTokens()
	if len(tokens) == 0 {
		t.Fatal("footerSettingsTokens() returned no tokens")
	}
	if tokens[0].Text != "Ignored Desk" {
		t.Fatalf("chat primary footer token = %q, want app-provided swarm name for legacy host route", tokens[0].Text)
	}
	page.SetSwarmName("")
	tokens = page.footerSettingsTokens()
	if tokens[0].Text != "" {
		t.Fatalf("chat primary footer token after clearing swarm name = %q, want empty", tokens[0].Text)
	}
}

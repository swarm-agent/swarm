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
			{ID: "host", Label: "host"},
			{ID: "swarm:remote:/repo", Label: "Remote Desk"},
		},
	})
	page.SetSwarmName("Local Desk")

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

func TestHomeFooterUsesLocalSwarmNameForHostRoute(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "host",
		ChatRoutes:          []model.ChatRoute{{ID: "host", Label: "host"}},
	})
	page.SetSwarmName("Local Desk")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "Local Desk" {
		t.Fatalf("home primary footer token = %q, want local swarm name", tokens[0].Text)
	}
}

func TestChatFooterUsesActiveRouteAsSwarmLabel(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "Local Desk",
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

func TestChatFooterUsesLocalSwarmNameForHostRoute(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "Local Desk",
		Meta:           ChatSessionMeta{Route: "host"},
	})

	tokens := page.footerSettingsTokens()
	if len(tokens) == 0 {
		t.Fatal("footerSettingsTokens() returned no tokens")
	}
	if tokens[0].Text != "Local Desk" {
		t.Fatalf("chat primary footer token = %q, want local swarm name", tokens[0].Text)
	}
}

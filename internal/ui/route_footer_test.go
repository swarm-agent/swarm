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

func TestHomeFooterUsesPrimarySelfHostRouteLabel(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "swarm:primary:binding:primary-binding",
		CurrentSwarmTarget:  &model.SwarmTarget{SwarmID: "primary", Name: "Primary Desk", Relationship: "self", Kind: "host"},
		ChatRoutes: []model.ChatRoute{
			{ID: "host", Label: "host", TargetKind: "host", TargetRelationship: "self"},
			{ID: "swarm:primary:binding:primary-binding", Label: "Local", SwarmID: "primary", WorkspaceBindingID: "primary-binding", TargetKind: "host", TargetRelationship: "self"},
			{ID: "swarm:container:binding:container-binding", Label: "Local", SwarmID: "container", WorkspaceBindingID: "container-binding", TargetKind: "container", TargetRelationship: "child"},
		},
	})
	page.SetSwarmName("Fallback Local")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "Primary Desk" {
		t.Fatalf("home primary footer token = %q, want primary self host route label", tokens[0].Text)
	}
}

func TestHomeFooterUsesCurrentPrimaryTargetForLegacyHostRoute(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:          "local",
		SelectedChatRouteID: "host",
		CurrentSwarmTarget:  &model.SwarmTarget{SwarmID: "primary", Name: "Primary Desk", Relationship: "self", Kind: "host"},
		ChatRoutes:          []model.ChatRoute{{ID: "host", Label: "host", TargetKind: "host", TargetRelationship: "self"}},
	})
	page.SetSwarmName("Fallback Local")

	tokens := page.homeFooterTokens()
	if len(tokens) == 0 {
		t.Fatal("homeFooterTokens() returned no tokens")
	}
	if tokens[0].Text != "Primary Desk" {
		t.Fatalf("home primary footer token = %q, want current primary target name", tokens[0].Text)
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

func TestHomeFooterAgentModelControlOpensCanonicalAgents(t *testing.T) {
	home := NewHomePage(model.HomeModel{
		ServerMode:    "local",
		ActiveAgent:   "swarm",
		ModelProvider: "anthropic",
		ModelName:     "claude",
		ChatRoutes:    []model.ChatRoute{{ID: "host", Label: "host"}},
	})
	home.SetSwarmName("Local Desk")

	tokens := home.homeFooterTokens()
	if len(tokens) != 3 {
		t.Fatalf("home footer token count = %d, want route, plan, and agent/model", len(tokens))
	}
	if tokens[2].Action != "open-agents-modal" {
		t.Fatalf("home agent/model action = %q, want open-agents-modal", tokens[2].Action)
	}
	if got, want := tokens[2].Text, "[swarm · claude]"; got != want {
		t.Fatalf("home agent/model unit = %q, want %q", got, want)
	}
}

func TestChatFooterAgentModelControlOpensCanonicalAgents(t *testing.T) {
	chat := NewChatPage(ChatPageOptions{
		SessionID: "session-test", SessionMode: "plan", AuthConfigured: true,
		Meta: ChatSessionMeta{Agent: "swarm"},
	})
	chat.SetModelState("codex", "gpt-5.4", "high", "fast", "")
	tokens := chat.footerSettingsTokens()
	if len(tokens) != 3 || tokens[1].Text != "Plan" || tokens[2].Action != "open-agents-modal" {
		t.Fatalf("chat footer tokens = %#v, want route, Plan, and canonical agent/model control", tokens)
	}
}

func TestFooterAgentModelUsesConciseResolvedFacts(t *testing.T) {
	got := footerAgentModelUnit(FooterState{
		Agent:       "swarm",
		ModelLabel:  "gpt-5.4",
		Thinking:    "high",
		ServiceTier: "fast",
	})
	if want := "[swarm · gpt-5.4 · high · fast]"; got != want {
		t.Fatalf("footerAgentModelUnit() = %q, want %q", got, want)
	}
	for _, unwanted := range []string{"Profile", "Action", "thinking", "tier"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("footerAgentModelUnit() retained obsolete label %q: %q", unwanted, got)
		}
	}
}

func TestFooterStateKeepsPageSpecificRightFacts(t *testing.T) {
	home := NewHomePage(model.HomeModel{WorktreesEnabled: true, Version: "1.2.3"})
	homeState := home.homeFooterState()
	if got := strings.Join(homeState.RightFacts, "|"); strings.Contains(got, "wt on") || !strings.Contains(got, "v 1.2.3") {
		t.Fatalf("home footer right facts = %#v, want version without obsolete worktree mode", homeState.RightFacts)
	}

	chat := NewChatPage(ChatPageOptions{SessionID: "session-test", AuthConfigured: true, Meta: ChatSessionMeta{WorktreeEnabled: true}})
	chat.contextUsageSet = true
	chat.contextWindow = 1000
	chat.contextRemain = 250
	chatState := chat.chatFooterState()
	if got := strings.Join(chatState.RightFacts, "|"); strings.Contains(got, "wt on") || !strings.Contains(got, "25% left") {
		t.Fatalf("chat footer right facts = %#v, want context without obsolete worktree mode", chatState.RightFacts)
	}
}

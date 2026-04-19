package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestChatFooterClearsModelStateWhenSetModelStateReceivesEmptyValues(t *testing.T) {
	page := ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		ModelProvider:  "codex",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
		Meta:           ui.ChatSessionMeta{Agent: "swarm"},
	})

	before := page.footerSettingsLine(1000)
	if !strings.Contains(before, "[m:gpt-5-codex]") {
		t.Fatalf("expected seeded footer model chip, got %q", before)
	}

	page.SetModelState("", "", "", "", "")

	after := page.footerSettingsLine(1000)
	if !strings.Contains(after, "[m:unset]") {
		t.Fatalf("expected cleared footer model chip, got %q", after)
	}
	if !strings.Contains(after, "[t:-]") {
		t.Fatalf("expected cleared footer thinking chip, got %q", after)
	}
	if strings.Contains(after, "gpt-5-codex") {
		t.Fatalf("did not expect stale model label after clear, got %q", after)
	}
}

func TestAuthDeleteFlowClearsOpenChatFooterAfterReload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/providers":
			_, _ = w.Write([]byte(`{"providers":[]}`))
		case "/v1/model":
			_, _ = w.Write([]byte(`{"preference":{"provider":"","model":"","thinking":"","service_tier":"","context_mode":"","updated_at":0},"context_window":0,"max_output_tokens":0,"catalog_source":"","catalog_fetched_at":0,"catalog_expires_at":0}`))
		case "/v1/workspace-overview":
			_, _ = w.Write([]byte(`{"workspaces":[],"directories":[],"current_workspace":null}`))
		case "/v1/vault/status":
			_, _ = w.Write([]byte(`{"enabled":false,"unlocked":false}`))
		case "/v1/health":
			_, _ = w.Write([]byte(`{"mode":"single","bypass_permissions":false}`))
		case "/v1/worktree-settings":
			_, _ = w.Write([]byte(`{"enabled":false}`))
		case "/v1/agents":
			_, _ = w.Write([]byte(`{"profiles":[{"name":"swarm","prompt":"primary","execution_setting":"readwrite"}]}`))
		case "/v1/context/sources":
			_, _ = w.Write([]byte(`{"rules":[],"skills":[]}`))
		case "/v1/sessions":
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &App{
		home:     ui.NewHomePage(model.EmptyHome()),
		route:    "chat",
		api:      client.New(srv.URL),
		config:   defaultAppConfig(),
		reloadCh: make(chan homeReloadResult, 1),
		homeModel: model.HomeModel{
			AuthConfigured: true,
			ModelProvider:  "codex",
			ModelName:      "gpt-5-codex",
			ThinkingLevel:  "high",
		},
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-test",
			ShowHeader:     true,
			SessionMode:    "auto",
			AuthConfigured: true,
			ModelProvider:  "codex",
			ModelName:      "gpt-5-codex",
			ThinkingLevel:  "high",
			Meta:           ui.ChatSessionMeta{Agent: "swarm"},
		}),
	}

	before := a.chat.footerSettingsLine(1000)
	if !strings.Contains(before, "[m:gpt-5-codex]") {
		t.Fatalf("expected seeded chat footer before reload, got %q", before)
	}

	next, err := a.refreshHomeModel(context.Background())
	if err != nil {
		t.Fatalf("refreshHomeModel() error = %v", err)
	}
	a.reloadCh <- homeReloadResult{model: next}
	a.consumeReloadResult()

	after := a.chat.footerSettingsLine(1000)
	if !strings.Contains(after, "[m:unset]") {
		t.Fatalf("expected cleared chat footer model after reload, got %q", after)
	}
	if strings.Contains(after, "gpt-5-codex") {
		t.Fatalf("did not expect stale chat footer model after reload, got %q", after)
	}
}

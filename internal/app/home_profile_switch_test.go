package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestAgentsModalProfileSwitchClosesOnlyAfterSuccess(t *testing.T) {
	profilesJSON := `{"model_profiles":[{"profile_id":"default","name":"Default","model_mode":"single","single":{"provider":"codex","model":"gpt-default"}},{"profile_id":"selected","name":"Selected","model_mode":"single","single":{"provider":"codex","model":"gpt-selected"}}],"default_profile_id":"default"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/model-profiles" || r.Method != http.MethodGet {
			t.Fatalf("unexpected profile switch request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(profilesJSON))
	}))
	defer server.Close()

	initial := model.EmptyHome()
	initial.DefaultModelProfileID = "default"
	initial.ActiveModelProfile = model.ActiveModelProfile{Source: "saved", ProfileID: "default", Name: "Default", ModelMode: "single"}
	page := ui.NewHomePage(initial)
	page.ShowAgentsModal()
	app := &App{api: testAPIWithToken(server.URL), home: page, homeModel: initial, route: "home"}

	app.handleAgentsModalAction(ui.AgentsModalAction{Kind: ui.AgentsModalActionSwitchProfile, ModelProfileID: "selected"})

	if page.AgentsModalVisible() {
		t.Fatal("successful profile switch left the Agents modal open")
	}
	if got := app.homeModel.ActiveModelProfile.ProfileID; got != "selected" {
		t.Fatalf("active profile = %q, want selected", got)
	}
	if got := app.homeModel.DefaultModelProfileID; got != "default" {
		t.Fatalf("account default changed to %q during active-profile switch", got)
	}
}

func TestAgentsModalProfileSwitchFailureKeepsModalOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "profile service unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	initial := model.EmptyHome()
	initial.DefaultModelProfileID = "default"
	initial.ActiveModelProfile = model.ActiveModelProfile{Source: "saved", ProfileID: "default", Name: "Default", ModelMode: "single"}
	page := ui.NewHomePage(initial)
	page.ShowAgentsModal()
	app := &App{api: client.New(server.URL), home: page, homeModel: initial, route: "home"}
	app.api.SetToken("test-token")

	app.handleAgentsModalAction(ui.AgentsModalAction{Kind: ui.AgentsModalActionSwitchProfile, ModelProfileID: "selected"})

	if !page.AgentsModalVisible() {
		t.Fatal("failed profile switch closed the Agents modal")
	}
	if got := app.homeModel.ActiveModelProfile.ProfileID; got != "default" {
		t.Fatalf("failed switch changed active profile to %q", got)
	}
}

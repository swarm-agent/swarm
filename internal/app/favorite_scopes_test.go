package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

// Purpose: selectModelFavorite must keep chat-only and account-default writes
// separate, target Orchestrator's Plan versus deployed Swarm's Action, and not
// claim a combined save succeeded after either API rejects it. An HTTP fixture
// at the TUI client boundary is the narrowest layer proving the actual requests
// and the confirmed footer state without a provider or running daemon.
func TestFavoriteScopesTargetChatAgentAndPreserveOtherDefault(t *testing.T) {
	for _, agent := range []string{"system-orchestrator", "swarm"} {
		for _, scope := range []ui.FavoriteScope{ui.FavoriteScopeChat, ui.FavoriteScopeDefault, ui.FavoriteScopeDefaultAndChat} {
			for _, failure := range []string{"", "default", "chat"} {
				t.Run(agent+"/"+string(scope)+"/"+failure, func(t *testing.T) {
					settings := client.AgentModelSettings{}
					settings.Swarm.Action = client.AgentModelAssignment{Provider: "codex", Model: "old-action", Thinking: "medium"}
					settings.Swarm.Plan = client.AgentModelAssignment{Provider: "codex", Model: "old-plan", Thinking: "high"}
					before := settings
					var writes []string
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch {
						case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/favorite-chat":
							_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{Session: client.SessionSummary{ID: "favorite-chat", Metadata: map[string]any{"agent_name": agent}}, AgentModelPolicy: client.SessionV3AgentModelPolicy{ResolvedAgent: agent}})
						case r.Method == http.MethodGet && r.URL.Path == "/v1/agent-model-settings":
							_ = json.NewEncoder(w).Encode(map[string]any{"agent_model_settings": settings})
						case r.Method == http.MethodPatch && r.URL.Path == "/v1/agent-model-settings":
							writes = append(writes, "default")
							if failure == "default" {
								http.Error(w, "default rejected", http.StatusConflict)
								return
							}
							var patch client.AgentModelSettingsPatch
							if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || patch.Swarm == nil {
								t.Errorf("invalid patch: %#v, %v", patch, err)
								http.Error(w, "invalid patch", http.StatusBadRequest)
								return
							}
							settings.Swarm.Action, settings.Swarm.Plan = patch.Swarm.Action, patch.Swarm.Plan
							_ = json.NewEncoder(w).Encode(map[string]any{"agent_model_settings": settings})
						case r.Method == http.MethodPut && r.URL.Path == "/v3/sessions/favorite-chat/model-profile":
							writes = append(writes, "chat")
							var request struct {
								Choice struct {
									SavedProfileID string `json:"saved_profile_id"`
								} `json:"choice"`
								ClientRequestID string `json:"client_request_id"`
							}
							if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Choice.SavedProfileID != "focus" || request.ClientRequestID == "" {
								t.Errorf("invalid chat choice: %#v, %v", request, err)
							}
							if failure == "chat" {
								http.Error(w, "chat rejected", http.StatusConflict)
								return
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"agent_model_policy": client.SessionV3AgentModelPolicy{Preference: client.ModelPreference{Provider: "codex", Model: "favorite"}}})
						default:
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
							http.Error(w, "unexpected request", http.StatusBadRequest)
						}
					}))
					defer server.Close()
					store := v3chat.NewStore()
					store.Dispatch(v3chat.HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "favorite-chat"}, Preference: client.ModelPreference{Provider: "codex", Model: "original-chat"}}})
					page := v3chat.NewPage(v3chat.NewRuntime(nil, store, nil), v3chat.PageStyles{})
					defer page.Close()
					// Surrounding context deliberately differs from deployed Swarm.
					initial := model.HomeModel{ActiveAgent: "system-orchestrator", ModelProfiles: []client.ModelProfile{{ProfileID: "focus", Provider: "codex", Model: "favorite"}}}
					a := &App{api: testAPIWithToken(server.URL), home: ui.NewHomePage(initial), homeModel: initial, route: "v3chat", v3Chat: page}
					err := a.selectModelFavorite("focus", scope)
					defaultFails := scope != ui.FavoriteScopeChat && failure == "default"
					chatFails := scope != ui.FavoriteScopeDefault && failure == "chat"
					if (err != nil) != (defaultFails || chatFails) {
						t.Fatalf("error = %v", err)
					}
					var wantWrites []string
					if scope != ui.FavoriteScopeChat {
						wantWrites = append(wantWrites, "default")
					}
					if scope != ui.FavoriteScopeDefault && !defaultFails {
						wantWrites = append(wantWrites, "chat")
					}
					if !reflect.DeepEqual(writes, wantWrites) {
						t.Fatalf("writes = %v, want %v", writes, wantWrites)
					}
					want := before
					if scope != ui.FavoriteScopeChat && !defaultFails {
						assignment := client.AgentModelAssignment{Provider: "codex", Model: "favorite"}
						if agent == "swarm" {
							want.Swarm.Action = assignment
						} else {
							want.Swarm.Plan = assignment
						}
					}
					if !reflect.DeepEqual(settings, want) {
						t.Fatalf("default state = %#v, want %#v", settings, want)
					}
					wantChat := "original-chat"
					if scope != ui.FavoriteScopeDefault && !defaultFails && !chatFails {
						wantChat = "favorite"
					}
					if got := store.Snapshot().Model.Preference.Model; got != wantChat {
						t.Fatalf("chat model = %q, want %q", got, wantChat)
					}
					if scope == ui.FavoriteScopeDefaultAndChat && chatFails && !strings.Contains(err.Error(), "default saved, but this chat was not changed") {
						t.Fatalf("partial save not disclosed: %v", err)
					}
				})
			}
		}
	}
}

// Purpose: missing chat context must fail before any API writes, and command
// discovery must expose /favorites instead of the retired /profiles name.
// selectModelFavorite and buildHomeCommandSuggestions own these boundaries.
func TestFavoritesRequireChatAndReplaceProfilesCommand(t *testing.T) {
	initial := model.HomeModel{ModelProfiles: []client.ModelProfile{{ProfileID: "focus", Provider: "codex", Model: "favorite"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request without chat: %s", r.URL.Path)
	}))
	defer server.Close()
	a := &App{api: testAPIWithToken(server.URL), home: ui.NewHomePage(initial), homeModel: initial, route: "home"}
	for _, scope := range []ui.FavoriteScope{ui.FavoriteScopeChat, ui.FavoriteScopeDefaultAndChat, "invalid"} {
		if err := a.selectModelFavorite("focus", scope); err == nil {
			t.Fatalf("scope %q accepted without a chat", scope)
		}
	}
	found := false
	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if suggestion.Command == "/profiles" {
			t.Fatal("retired command still advertised")
		}
		found = found || suggestion.Command == "/favorites"
	}
	if !found {
		t.Fatal("/favorites missing")
	}
}

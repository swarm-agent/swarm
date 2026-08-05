package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestSaveTipsSettingUsesCanonicalPartialPatch(t *testing.T) {
	var postBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected tips request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&postBody); err != nil {
			t.Fatalf("decode post body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UISettings{Chat: client.UIChatSettings{ShowTips: false}})
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	if err := saveTipsSetting(testAPIWithToken(server.URL), false); err != nil {
		t.Fatalf("saveTipsSetting: %v", err)
	}
	chat, ok := postBody["chat"].(map[string]any)
	if !ok || chat["show_tips"] != false || len(chat) != 1 || len(postBody) != 1 {
		t.Fatalf("tips patch = %#v, want only chat.show_tips=false", postBody)
	}
}

func TestTipsCommandTogglesAndPersists(t *testing.T) {
	var persisted []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var patch client.UISettingsPatch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Fatal(err)
		}
		persisted = append(persisted, *patch.Chat.ShowTips)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UISettings{Chat: client.UIChatSettings{ShowTips: *patch.Chat.ShowTips}})
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{
		home:          home,
		api:           testAPIWithToken(server.URL),
		config:        defaultAppConfig(),
		settingsLabel: settingsBackendLabel,
	}

	app.handleTipsCommand(nil)
	if app.config.Chat.ShowTips || home.HomeTipsVisible() || len(persisted) != 1 || persisted[0] {
		t.Fatalf("bare /tips did not persist disabled state: config=%v home=%v persisted=%v", app.config.Chat.ShowTips, home.HomeTipsVisible(), persisted)
	}
	app.handleTipsCommand([]string{"on"})
	if !app.config.Chat.ShowTips || !home.HomeTipsVisible() || len(persisted) != 2 || !persisted[1] {
		t.Fatalf("/tips on did not persist enabled state: config=%v home=%v persisted=%v", app.config.Chat.ShowTips, home.HomeTipsVisible(), persisted)
	}
	app.handleTipsCommand([]string{"status"})
	if !strings.Contains(strings.Join(home.CommandOverlayLines(), "\n"), "home tips: on") {
		t.Fatalf("/tips status overlay = %v", home.CommandOverlayLines())
	}
}

func TestTipsCommandRollsBackOnSaveFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{home: home, api: testAPIWithToken(server.URL), config: defaultAppConfig()}
	app.handleTipsCommand([]string{"off"})
	if !app.config.Chat.ShowTips || !home.HomeTipsVisible() {
		t.Fatal("failed /tips save did not restore enabled state")
	}
	if !strings.Contains(home.Status(), "unchanged (on)") {
		t.Fatalf("rollback status = %q", home.Status())
	}
}

func TestTipsCommandSuggestedOnHomeAndChat(t *testing.T) {
	for _, suggestions := range [][]ui.CommandSuggestion{buildHomeCommandSuggestions(false), buildChatCommandSuggestions(false)} {
		found := false
		for _, suggestion := range suggestions {
			if suggestion.Command == "/tips" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("/tips missing from command suggestions")
		}
	}
}

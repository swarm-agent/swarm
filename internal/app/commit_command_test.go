package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func TestHandleCommitCommandBlankShowsUsageWithoutRequest(t *testing.T) {
	home := ui.NewHomePage(model.EmptyHome())
	a := &App{home: home, route: "v3chat"}
	a.handleCommitCommand("/commit   ")
	if got := home.Status(); got != "usage: /commit <message>|ai" {
		t.Fatalf("status = %q", got)
	}
}

func TestRunCommitCommandManualAndAIUseCanonicalAPIs(t *testing.T) {
	for _, test := range []struct {
		name      string
		command   v3chat.CommitCommand
		wantCalls []string
		wantBody  string
	}{
		{name: "manual", command: v3chat.CommitCommand{Message: "manual message"}, wantCalls: []string{"commit"}, wantBody: "manual message"},
		{name: "ai", command: v3chat.CommitCommand{AI: true}, wantCalls: []string{"suggestion", "commit"}, wantBody: "generated message"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("session_id"); got != "session-1" {
					t.Fatalf("session_id = %q", got)
				}
				switch r.URL.Path {
				case "/v1/workspace/git/commit/suggestion":
					calls = append(calls, "suggestion")
					_ = json.NewEncoder(w).Encode(client.GitCommitSuggestionResponse{OK: true, Message: "generated message"})
				case "/v1/workspace/git/commit":
					calls = append(calls, "commit")
					var body struct {
						Message string `json:"message"`
						All     bool   `json:"all"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.Message != test.wantBody || !body.All {
						t.Fatalf("commit body = %#v", body)
					}
					_ = json.NewEncoder(w).Encode(client.GitCommitResponse{OK: true, Summary: "git commit exited 0"})
				default:
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
			}))
			defer server.Close()

			api := client.New(server.URL)
			api.SetToken("test-token")
			a := &App{api: api}
			a.runCommitCommand(test.command, "/workspace", "session-1")
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", calls, test.wantCalls)
			}
		})
	}
}

func TestCommitCommandSuggestionDocumentsManualAndAIForms(t *testing.T) {
	items := buildHomeCommandSuggestions(false)
	for _, item := range items {
		if item.Command != "/commit" {
			continue
		}
		want := []string{"/commit <message>", "/commit ai"}
		if !reflect.DeepEqual(item.QuickTips, want) {
			t.Fatalf("commit quick tips = %#v, want %#v", item.QuickTips, want)
		}
		return
	}
	t.Fatal("/commit suggestion missing")
}

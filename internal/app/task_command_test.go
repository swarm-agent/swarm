package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestTaskCommandReturnsBeforeRoutedDispatchCompletes(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		wantMode string
	}{
		{name: "auto", args: []string{"fix", "routing"}, wantMode: "auto"},
		{name: "plan", args: []string{"plan", "fix", "routing"}, wantMode: "plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestSeen := make(chan map[string]any, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				requestSeen <- body
				<-release
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":            true,
					"session_id":    "session-task",
					"title":         "Fix routing",
					"starting_mode": test.wantMode,
					"session": map[string]any{
						"id": "session-task", "title": "Fix routing", "mode": test.wantMode,
						"worktree_enabled": true, "worktree_root_path": "/worktree",
					},
				})
			}))
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				server.Close()
			}()

			app := newTaskCommandTestApp(server.URL)
			returned := make(chan struct{})
			go func() {
				app.handleTaskCommand(test.args)
				close(returned)
			}()

			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("task command blocked while the routed request was still running")
			}

			var body map[string]any
			select {
			case body = <-requestSeen:
			case <-time.After(time.Second):
				t.Fatal("task command did not dispatch the routed request")
			}
			if got := body["plan_mode_requested"]; got != (test.wantMode == "plan") {
				t.Fatalf("plan_mode_requested = %#v", got)
			}
			close(release)
			awaitTaskCommandResult(t, app)
			if got := app.home.Status(); got != "Fix routing started in a worktree." {
				t.Fatalf("status = %q", got)
			}
		})
	}
}

func TestTaskCommandReportsBackgroundDispatchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"router unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	app := newTaskCommandTestApp(server.URL)
	app.handleTaskCommand([]string{"fix", "routing"})
	awaitTaskCommandResult(t, app)
	if got := app.home.Status(); !strings.HasPrefix(got, "/task failed:") {
		t.Fatalf("status = %q", got)
	}
}

func newTaskCommandTestApp(baseURL string) *App {
	return &App{
		home:          ui.NewHomePage(model.EmptyHome()),
		route:         "home",
		api:           testAPIWithToken(baseURL),
		taskCommandCh: make(chan taskCommandResult, 8),
	}
}

func awaitTaskCommandResult(t *testing.T, app *App) {
	t.Helper()
	select {
	case result := <-app.taskCommandCh:
		app.presentTaskCommandResult(result)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task command result")
	}
}

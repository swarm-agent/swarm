package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the actual manual POST branch delegates to CreateProjectTask, not
// RouteTask. Its persisted title must be Router-named exactly once, retain the
// full instructions, survive reads/replays/renames, and never start execution.
// Temp-store API tests are the narrowest layer proving that canonical boundary.
func TestManualProjectTaskNamingPersistenceAndReplay(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projectID := f.createProject(t)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Fix sidebar layout jumps","agent":"image","mission":"ignore instructions"}`}}
	configureManualNamingRouter(t, f, p, runner)
	prompt := "ok can you fix the sidebar jumping whenever I resize the window and please preserve all the keyboard shortcuts and don't change the theme"
	body := map[string]any{"id": "manual-naming", "prompt": prompt, "agent": "coder", "feature_size": "small"}
	created := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+projectID+"/tasks", body, p), http.StatusCreated)
	if created["title"] != "Fix sidebar layout jumps" || created["description"] != prompt || created["agent"] != "coder" {
		t.Fatalf("created task: %+v", created)
	}
	if runner.createCalls != 1 || len(runner.requests[0].Tools) != 0 || runner.requests[0].Model != "router-model" {
		t.Fatalf("Router calls/settings: %+v", runner.requests)
	}
	saved, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, "manual-naming")
	if err != nil || !found || saved.Title != created["title"] || saved.Description != prompt {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	if saved.SessionID != "" {
		intents, err := f.server.sessions.Store().ListRunIntents(saved.SessionID, 10)
		if err != nil || len(intents) != 0 {
			t.Fatalf("naming started execution: %+v %v", intents, err)
		}
	}
	_, err = f.server.sessions.Store().UpdateProjectTask(f.accountID, projectID, saved.ID, func(task *pebblestore.ProjectTaskRecord) error { task.Title = "User renamed task"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	runner.err = errors.New("Router now unavailable")
	replayed := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+projectID+"/tasks", body, p), http.StatusCreated)
	if replayed["title"] != "User renamed task" || runner.createCalls != 1 {
		t.Fatalf("replay renamed/called Router: %+v calls=%d", replayed, runner.createCalls)
	}
	read := requireMatrixTaskResponse(t, f.callAPI(http.MethodGet, "/"+projectID+"/tasks/"+saved.ID, nil, p), http.StatusOK)
	if read["title"] != replayed["title"] {
		t.Fatalf("read disagrees: %+v", read)
	}
	body["id"], body["title"] = "custom-naming", "My deliberate title"
	custom := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+projectID+"/tasks", body, p), http.StatusCreated)
	if custom["title"] != "My deliberate title" || runner.createCalls != 1 {
		t.Fatalf("custom title: %+v", custom)
	}
}

// Purpose: failure/empty/overlong Router output must reject before task/session
// reservation instead of persisting an apparently successful clipped title.
func TestManualProjectTaskNamingFailureNoReservation(t *testing.T) {
	for name, raw := range map[string]string{"empty": `{"title":""}`, "malformed": `not JSON`, "overlong": `{"title":"` + strings.Repeat("界", 81) + `"}`, "provider": ""} {
		t.Run(name, func(t *testing.T) {
			f := setupMatrixTestFixture(t)
			defer f.db.Close()
			projectID := f.createProject(t)
			p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
			runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: raw}}
			if name == "provider" {
				runner.err = errors.New("provider unavailable")
			}
			configureManualNamingRouter(t, f, p, runner)
			w := f.callAPI(http.MethodPost, "/"+projectID+"/tasks", map[string]any{"id": "invalid-naming", "prompt": "ok please fix the sidebar and retain all existing settings", "agent": "coder"}, p)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "task router naming") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if _, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, projectID, "invalid-naming"); err != nil || found {
				t.Fatalf("partial reservation: %v found=%v", err, found)
			}
			if runner.createCalls != 1 || f.wt.allocCalls != 0 {
				t.Fatalf("calls=%d allocations=%d", runner.createCalls, f.wt.allocCalls)
			}
		})
	}
}

func configureManualNamingRouter(t *testing.T, f *matrixTestFixture, p identity.Principal, runner *sessionRouterRecordingRunner) {
	t.Helper()
	if err := f.server.agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	catalog := pebblestore.NewModelCatalogStore(f.db)
	if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{Provider: runner.id, Model: "router-model", ThinkingOptions: []string{"high"}, ServiceTiers: []string{"priority"}}); err != nil {
		t.Fatal(err)
	}
	f.server.model = model.NewService(pebblestore.NewModelStore(f.db), nil, model.NewCatalogService(catalog))
	if _, err := f.server.agentModelSettings.UpdateSystemAgent(identity.ContextWithPrincipal(context.Background(), p), "router", pebblestore.AgentModelAssignment{Provider: runner.id, Model: "router-model", Thinking: "high", ServiceTier: "priority"}); err != nil {
		t.Fatal(err)
	}
	f.server.providers = registry.New()
	f.server.providers.RegisterRunner(runner)
}

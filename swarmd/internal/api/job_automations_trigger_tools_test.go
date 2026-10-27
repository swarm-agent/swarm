package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: MCP saves and native proposals must use the same HTTP/service trigger
// record, never leak signing keys on reads or proposals, and reject invalid starts
// without persistence. Real routes and Pebble are the narrowest layer proving save,
// explicit human acceptance, rotation, and list postconditions together.
func TestWorkflowTriggerTools(t *testing.T) {
	f := newReopenFixture(t)
	s := f.server
	p := testPrincipal()
	workers := s.sessions.Store().WorkerStore()
	worker, _, err := workers.GetWorker(p.AccountScopeID, f.workerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workers.ActivateWorker(p.AccountScopeID, p.UserID, worker.ID, worker.Revision, nil); err != nil {
		t.Fatal(err)
	}
	db := s.sessions.Store().Underlying()
	s.secretValues = store.NewAuthStore(db)
	s.workspace = workspace.NewService(store.NewWorkspaceStore(db))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := s.workspace.AddForPrincipal(p, folder, "triggers", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().PutProject(p.AccountScopeID, &store.ProjectRecord{ID: "trigger-project", Name: "Triggers", Workspaces: []store.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	repository := jobautomation.NewStore(db)
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: repository}))
	c := &controlMCPCall{server: s, request: withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), next: s.internalRouteHandler()}
	on := map[string]any{"event": map[string]any{"source": "webhook", "id": "id"}, "start": map[string]any{"worker": f.workerID, "brief": "Handle {{event.id}}"}}
	args := map[string]any{"name": "hook", "project": "trigger-project", "on": on, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "Review"}}}
	first, err := controlMCPSetAutomation(c, args)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := first.(map[string]any)["trigger_key"].(string)
	if len(key) != 64 || !strings.Contains(first.(map[string]any)["next"].(string), "shown only now") {
		t.Fatal("missing one-time key guidance")
	}
	again, err := controlMCPSetAutomation(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.(map[string]any)["trigger_key"]; ok {
		t.Fatal("key returned twice")
	}
	args["rotate_key"] = true
	rotated, err := controlMCPSetAutomation(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := rotated.(map[string]any)["trigger_key"].(string); len(value) != 64 || value == key {
		t.Fatal("key not rotated")
	}
	delete(args, "rotate_key")
	listed, err := controlMCPListAutomations(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(listed)
	if !strings.Contains(string(data), `"on"`) || !strings.Contains(string(data), `"events"`) || strings.Contains(string(data), key) || strings.Contains(string(data), "trigger_key") {
		t.Fatal("unsafe or incomplete list", string(data))
	}
	args["name"] = "proposal"
	args["action"] = "propose"
	proposed, err := s.ManageWorkflows(context.Background(), p, "author", args)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(proposed)
	if strings.Contains(string(data), "trigger_key") {
		t.Fatal("key exposed to agent")
	}
	proposalID := proposed.(map[string]any)["automation"].(map[string]any)["id"].(string)
	a, err := repository.Get(p.AccountScopeID, proposalID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Proposed || a.On == nil {
		t.Fatal("missing inert trigger proposal")
	}
	req := withTestPrincipal(httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+a.ID+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%d}`, a.Revision))))
	response := httptest.NewRecorder()
	s.handleJobAutomations(response, req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if value, _ := accepted["trigger_key"].(string); len(value) != 64 {
		t.Fatal("accept response missing key")
	}
	active, err := repository.Get(p.AccountScopeID, a.ID)
	if err != nil || active.Proposed || active.On == nil || active.On.Start.Worker != f.workerID {
		t.Fatal("trigger not activated", err)
	}
	on["start"].(map[string]any)["worker"] = "missing-worker"
	args["name"] = "invalid"
	delete(args, "action")
	if _, err := controlMCPSetAutomation(c, args); err == nil || !strings.Contains(err.Error(), "deployed, active worker") {
		t.Fatal("missing service refusal", err)
	}
	if _, err := repository.FindByName(p.AccountScopeID, "invalid"); err == nil {
		t.Fatal("invalid trigger persisted")
	}
}

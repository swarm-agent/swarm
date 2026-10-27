package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: the home adapter must use the pinned authenticated link and expose
// only account-owned link summaries. This in-process two-daemon boundary test
// checks the actual remote project API, cross-account denial and unsupported
// mutations without creating an alternate task store or exposing credentials.
func TestFleetProjectAdapter(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	machines, err := home.s.ListFleetMachines(ctx, p)
	if err != nil || len(machines) != 1 || machines[0].Name != "box" || !machines[0].Online {
		t.Fatalf("machines: %+v %v", machines, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v3/sessions/session/access", strings.NewReader(`{"fleet":{"box":"full"},"folders":["ignored"]}`))
	var accessRequest sessionsV3AccessRequest
	if err := decodeJSON(request, &accessRequest); err != nil || accessRequest.Fleet["box"] != "full" {
		t.Fatalf("access request: %+v %v", accessRequest, err)
	}
	request = request.WithContext(context.WithValue(request.Context(), applicationAgentContextKey{}, applicationAgentBinding{}))
	if err := sessionAccessAuthority(request); err == nil {
		t.Fatal("agent gateway may change access")
	}
	access := pebblestore.SessionAccess{Fleet: map[string]string{"box": "full"}}
	if err := home.s.validateFleetAccess(p, access); err != nil {
		t.Fatal(err)
	}
	if err := home.s.validateFleetAccess(p, pebblestore.SessionAccess{Fleet: map[string]string{"missing": "none"}}); err == nil {
		t.Fatal("unknown machine accepted")
	}
	other := p
	other.AccountScopeID = "other-account"
	if err := home.s.validateFleetAccess(other, access); err == nil {
		t.Fatal("other account accepted")
	}
	if got, err := home.s.ListFleetMachines(ctx, other); err != nil || len(got) != 0 {
		t.Fatalf("cross account list: %+v %v", got, err)
	}
	remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	remotePrincipal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}
	added, err := remote.s.workspace.AddForPrincipal(remotePrincipal, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.s.sessions.Store().PutProject(remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	created, err := home.s.CallFleetProject(ctx, p, "box", "propose_task", map[string]any{"project_id": "remote-project", "title": "Audit project", "agent": "finder", "auto_approve": false})
	if err != nil {
		link, _, _ := home.s.fleet.GetMachine("box")
		t.Fatalf("create: %v; remote detail: %s", err, link.LastError)
	}
	var response struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(created, &response); err != nil || response.Task.ID == "" {
		t.Fatalf("create response: %s %v", created, err)
	}
	stored, found, err := remote.s.sessions.Store().GetProjectTask(remote.account, "remote-project", response.Task.ID)
	if err != nil || !found || stored.Title != "Audit project" {
		t.Fatalf("remote store: %+v %v %v", stored, found, err)
	}
	if _, found, err := home.s.sessions.Store().GetProjectTask(home.account, "remote-project", response.Task.ID); err != nil || found {
		t.Fatalf("task incorrectly in home: %v %v", found, err)
	}
	if _, err := home.s.CallFleetProject(ctx, p, "box", "get_task", map[string]any{"project_id": "remote-project", "task_id": response.Task.ID}); err != nil {
		t.Fatal(err)
	}
	raw, err := home.s.CallFleetProject(ctx, p, "box", "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := home.s.fleetTokens.GetFleetLinkToken(home.account, "box")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("link token leaked")
	}
	for _, action := range []string{"integrate_task", "deploy_task", "delete", "wait_tasks"} {
		if _, err := home.s.CallFleetProject(ctx, p, "box", action, nil); err == nil || !strings.Contains(err.Error(), "not available on a linked machine") {
			t.Fatalf("%s: %v", action, err)
		}
	}
}

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
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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
	testFleetPermissionExecution(t, ctx, home, remote, p)
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
	read, err := home.s.CallFleetProject(ctx, p, "box", "get_task", map[string]any{"project_id": "remote-project", "task_id": response.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	var taskRead struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(read, &taskRead); err != nil || taskRead.Task.ID != stored.ID || taskRead.Task.Status != stored.Status || taskRead.Task.Title != stored.Title {
		t.Fatalf("remote read: %s %v", read, err)
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
	for _, action := range []string{"report_task", "integrate_task", "deploy_task", "delete", "wait_tasks"} {
		if _, err := home.s.CallFleetProject(ctx, p, "box", action, nil); err == nil || !strings.Contains(err.Error(), "not available on a linked machine") {
			t.Fatalf("%s: %v", action, err)
		}
	}
}

// Purpose: exercise the real permission store and native runtime against the
// linked API. Pending/denied calls cannot create remote tasks; approval of the
// exact call permits creation. This boundary test needs no provider execution.
func testFleetPermissionExecution(t *testing.T, ctx context.Context, home, remote *fleetTestSwarm, p identity.Principal) {
	t.Helper()
	runtime := tool.NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageSessionService(home.s.sessions)
	runtime.SetProjectTaskLifecycleService(home.s)
	perms := permission.NewService(pebblestore.NewPermissionStore(home.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(home.s.sessions)
	for _, level := range []string{"none", "ask", "full"} {
		id := "fleet-" + level
		session := pebblestore.SessionSnapshot{ID: id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: pebblestore.SessionAccessMetadata(pebblestore.SessionAccess{Fleet: map[string]string{"box": level}})}}
		if err := home.s.sessions.Store().CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
			t.Fatal(err)
		}
		// Exercise the owner mutation boundary, including rejection postconditions.
		for _, tc := range []struct {
			body   string
			agent  bool
			status int
		}{
			{`{"client_request_id":"valid","fleet":{"box":"` + level + `"},"folders":[]}`, false, http.StatusOK},
			{`{"client_request_id":"unknown","fleet":{"missing":"full"}}`, false, http.StatusBadRequest},
			{`{"client_request_id":"agent","fleet":{"box":"none"}}`, true, http.StatusForbidden},
		} {
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+id+"/access", strings.NewReader(tc.body))
			if tc.agent {
				req = req.WithContext(context.WithValue(req.Context(), applicationAgentContextKey{}, applicationAgentBinding{}))
			}
			w := httptest.NewRecorder()
			home.s.handleSessionV3Access(w, req, p, id)
			if w.Code != tc.status {
				t.Fatalf("access route: %d %s", w.Code, w.Body.String())
			}
			got, found, err := home.s.sessions.GetSession(id)
			if err != nil || !found || pebblestore.SessionAccessFromMetadata(got.Metadata).Fleet["box"] != level {
				t.Fatalf("access changed unexpectedly: %+v %v", got, err)
			}
		}
		scope := tool.WorkspaceScope{SessionID: id, Principal: p}
		listing, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: `{"action":"list_machines"}`})
		if err != nil || !strings.Contains(listing, `"access":"`+level+`"`) {
			t.Fatalf("list: %s %v", listing, err)
		}
		args := `{"action":"propose_task","machine":"box","project_id":"remote-project","title":"Gate ` + level + `","agent":"finder","auto_approve":false}`
		auth, err := perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: id, AccountScopeID: p.AccountScopeID, RunID: "run", CallID: "call", ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
		if err != nil {
			t.Fatal(err)
		}
		before, err := remote.s.sessions.Store().ListProjectTasks(remote.account, "remote-project", 100)
		expected := 0
		if level == "full" {
			expected = 1
		}
		if err != nil || len(before) != expected {
			t.Fatalf("task created before authorization: %+v %v", before, err)
		}
		if level == "none" {
			if auth.Decision != permission.AuthorizationDeny {
				t.Fatalf("none: %+v", auth)
			}
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: args}); err == nil {
				t.Fatal("runtime allowed none")
			}
			continue
		}
		if level == "ask" {
			if auth.Decision != permission.AuthorizationPending || auth.Record == nil {
				t.Fatalf("ask: %+v", auth)
			}
			var pendingArgs map[string]any
			if err := json.Unmarshal([]byte(auth.Record.ToolArguments), &pendingArgs); err != nil || pendingArgs["machine"] != "box" || pendingArgs["action"] != "propose_task" || pendingArgs["title"] != "Gate ask" {
				t.Fatalf("pending card missing machine/action/brief: %s", auth.Record.ToolArguments)
			}
			pending, err := perms.ListPending(id, 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if _, err := perms.Resolve(id, auth.Record.ID, "deny", "owner denial"); err != nil {
				t.Fatal(err)
			}
			unchanged, err := remote.s.sessions.Store().ListProjectTasks(remote.account, "remote-project", 100)
			if err != nil || len(unchanged) != 0 {
				t.Fatal("denial created remote work")
			}
			auth, err = perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: id, AccountScopeID: p.AccountScopeID, RunID: "run", CallID: "approved-call", ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
			if err != nil || auth.Decision != permission.AuthorizationPending || auth.Record == nil {
				t.Fatalf("second call must ask again: %+v %v", auth, err)
			}
			if _, err := perms.Resolve(id, auth.Record.ID, "approve", "owner approval"); err != nil {
				t.Fatal(err)
			}
			if _, err := perms.WaitForResolution(ctx, id, auth.Record.ID); err != nil {
				t.Fatal(err)
			}
		} else if auth.Decision != permission.AuthorizationApprove {
			t.Fatalf("full: %+v", auth)
		}
		output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Data struct {
				Task pebblestore.ProjectTaskRecord `json:"task"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		task, found, err := remote.s.sessions.Store().GetProjectTask(remote.account, "remote-project", result.Data.Task.ID)
		if err != nil || !found || task.Title != "Gate "+level {
			t.Fatalf("remote task: %+v %v %v", task, found, err)
		}
		token, _, _ := home.s.fleetTokens.GetFleetLinkToken(home.account, "box")
		if token == "" || strings.Contains(output, token) {
			t.Fatal("unsafe output")
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// fakeEnvironmentBuilder stands in for the machine's Docker manager
// (sandbox.Manager.BuildEnvironment), which these tests cannot run: it
// records each build and fails one whose setup names FAIL, with a log.
type fakeEnvironmentBuilder struct {
	mu     sync.Mutex
	builds []sandbox.EnvironmentBuild
}

func (f *fakeEnvironmentBuilder) BuildEnvironment(_ context.Context, b sandbox.EnvironmentBuild) (sandbox.EnvironmentBuildResult, error) {
	f.mu.Lock()
	f.builds = append(f.builds, b)
	f.mu.Unlock()
	for _, command := range b.Setup {
		if strings.Contains(command, "FAIL") {
			return sandbox.EnvironmentBuildResult{Log: "$ " + command + "\nerror: toolchain download failed\n"}, errors.New("setup command failed (" + command + ")")
		}
	}
	return sandbox.EnvironmentBuildResult{Image: b.Image, Path: "/usr/local/bin:/usr/bin:/bin", Log: "built\n"}, nil
}

func (f *fakeEnvironmentBuilder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

type linkedEnvironmentFixture struct {
	home, remote *fleetTestSwarm
	folder       string
	builder      *fakeEnvironmentBuilder
	homeP        identity.Principal
	remoteP      identity.Principal
	envs         *pebblestore.SandboxEnvironmentStore
}

// newLinkedEnvironmentFixture links a home to a machine "box" with one
// registered folder and the machine's real environment store and route.
func newLinkedEnvironmentFixture(t *testing.T) *linkedEnvironmentFixture {
	t.Helper()
	home := newFleetTestSwarm(t, "home")
	t.Cleanup(home.cleanup)
	remote := newFleetTestSwarm(t, "remote")
	t.Cleanup(remote.cleanup)
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	remoteP := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}
	if _, err := remote.s.workspace.AddForPrincipal(remoteP, folder, "project", "", false); err != nil {
		t.Fatal(err)
	}
	envs := pebblestore.NewSandboxEnvironmentStore(remote.s.sessions.Store().Underlying())
	remote.s.SetSandboxEnvironmentStore(envs)
	builder := &fakeEnvironmentBuilder{}
	remote.s.envBuilder = builder
	return &linkedEnvironmentFixture{
		home: home, remote: remote, folder: folder, builder: builder, envs: envs, remoteP: remoteP,
		homeP: identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account},
	}
}

func (f *linkedEnvironmentFixture) call(t *testing.T, machine, action string, args map[string]any) (map[string]any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server, p := f.home.s, f.homeP
	if machine == "" {
		server, p = f.remote.s, f.remoteP
	}
	raw, err := server.ManageFolderEnvironment(ctx, p, machine, action, args)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("answer: %s %v", raw, err)
	}
	return out, nil
}

func (f *linkedEnvironmentFixture) setLinkedRule(t *testing.T, rule string) {
	t.Helper()
	store := f.home.s.sessions.Store()
	account, err := store.GetAccountPermissions(f.home.account)
	if err != nil {
		t.Fatal(err)
	}
	account.Linked["box"] = pebblestore.LinkedPermissionRule{Work: rule}
	if _, err := store.Underlying().SetAccountPermissions(f.home.account, account.Revision, account); err != nil {
		t.Fatal(err)
	}
}

func (f *linkedEnvironmentFixture) stored(t *testing.T) pebblestore.SandboxEnvironment {
	t.Helper()
	env, found, err := f.envs.Get(f.folder)
	if err != nil || !found {
		t.Fatalf("stored environment: %v %v", found, err)
	}
	return env
}

func environmentDefinition(t *testing.T, view any) pebblestore.SandboxEnvironmentDefinition {
	t.Helper()
	raw, _ := json.Marshal(view.(map[string]any)["definition"])
	var def pebblestore.SandboxEnvironmentDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	return def
}

// Purpose: a home manages a linked machine's folder environment through the
// link with the same service as local callers: it defines, merges (setup
// appended, untouched fields kept), reads, lists and rebuilds it, and a
// failed build names its error and log tail. Threat: a home that can only
// change an environment by SSH (the observed dead end), or a merge that drops
// the existing setup. Boundary: Server.ManageFolderEnvironment (home),
// fleetLinkHandler and handleSandboxEnvironments / UpdateSandboxEnvironment
// (machine), SandboxEnvironmentStore. The two in-process daemons talk over the
// real pinned TLS link; only the Docker build is a test double.
func TestHomeManagesLinkedFolderEnvironment(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	out, err := f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{
		"workspace_path": f.folder, "base_image": "debian:12", "setup": []any{"apt-get install -y git"},
		"env": map[string]any{"APP_MODE": "dev"}, "max_leases": float64(2), "wait_seconds": float64(10),
	})
	if err != nil {
		t.Fatalf("define over the link: %v", err)
	}
	view := out["environment"].(map[string]any)
	if view["status"] != pebblestore.SandboxEnvironmentReady || out["build_started"] != true || view["max_leases"] != float64(2) {
		t.Fatalf("defined view: %v", out)
	}

	// Merge: append a toolchain and set one variable; the rest is kept.
	out, err = f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{
		"workspace_path": f.folder, "append_setup": []any{"curl -sSf https://sh.rustup.rs | sh -s -- -y"},
		"env": map[string]any{"RUSTUP_HOME": "/opt/rustup"}, "wait_seconds": float64(10),
	})
	if err != nil {
		t.Fatalf("merge over the link: %v", err)
	}
	def := environmentDefinition(t, out["environment"])
	if def.BaseImage != "debian:12" || len(def.Setup) != 2 || def.Setup[0] != "apt-get install -y git" || !strings.Contains(def.Setup[1], "rustup") {
		t.Fatalf("merge lost or reordered setup: %+v", def)
	}
	if def.Env["APP_MODE"] != "dev" || def.Env["RUSTUP_HOME"] != "/opt/rustup" {
		t.Fatalf("merge lost env: %+v", def.Env)
	}
	stored := f.stored(t)
	if stored.MaxLeases != 2 || stored.Status != pebblestore.SandboxEnvironmentReady || len(stored.Definition.Setup) != 2 || stored.Account != f.remote.account {
		t.Fatalf("machine store after merge: %+v", stored)
	}

	// Max leases alone changes no definition and starts no build.
	builds := f.builder.count()
	out, err = f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "max_leases": float64(4)})
	if err != nil || out["build_started"] != false || f.builder.count() != builds || f.stored(t).MaxLeases != 4 || len(f.stored(t).Definition.Setup) != 2 {
		t.Fatalf("max_leases only: %v %v builds=%d", out, err, f.builder.count())
	}

	got, err := f.call(t, "box", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder})
	if err != nil || environmentDefinition(t, got["environment"]).Env["RUSTUP_HOME"] != "/opt/rustup" {
		t.Fatalf("get over the link: %v %v", got, err)
	}
	listed, err := f.call(t, "box", tool.FolderEnvironmentList, nil)
	if err != nil || listed["count"] != float64(1) {
		t.Fatalf("list over the link: %v %v", listed, err)
	}
	if first := listed["environments"].([]any)[0].(map[string]any); first["workspace_path"] != f.folder {
		t.Fatalf("listed folder: %v", first)
	}

	builds = f.builder.count()
	rebuilt, err := f.call(t, "box", tool.FolderEnvironmentRebuild, map[string]any{"workspace_path": f.folder, "wait_seconds": float64(10)})
	if err != nil || rebuilt["environment"].(map[string]any)["status"] != pebblestore.SandboxEnvironmentReady || f.builder.count() != builds+1 {
		t.Fatalf("rebuild over the link: %v %v builds=%d", rebuilt, err, f.builder.count())
	}

	// A failed build names its error and log tail; the folder keeps the
	// previous ready image.
	failed, err := f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "append_setup": []any{"install FAIL"}, "wait_seconds": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	failedView := failed["environment"].(map[string]any)
	if failedView["status"] != pebblestore.SandboxEnvironmentFailed || !strings.Contains(failedView["error"].(string), "setup command failed") || !strings.Contains(failedView["log"].(string), "toolchain download failed") {
		t.Fatalf("failed build view: %v", failedView)
	}
	if !strings.Contains(failedView["runs_on"].(string), "previous definition") {
		t.Fatalf("failed build lost the ready image: %v", failedView["runs_on"])
	}
	// setup replaces the commands (the explicit replacement).
	fixed, err := f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "setup": []any{"apt-get install -y git"}, "wait_seconds": float64(10)})
	if err != nil || len(environmentDefinition(t, fixed["environment"]).Setup) != 1 || fixed["environment"].(map[string]any)["status"] != pebblestore.SandboxEnvironmentReady {
		t.Fatalf("replace setup: %v %v", fixed, err)
	}

	// The local path is the same service without the link.
	local, err := f.call(t, "", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder})
	if err != nil || environmentDefinition(t, local["environment"]).BaseImage != "debian:12" {
		t.Fatalf("local get: %v %v", local, err)
	}
	if _, err := f.call(t, "", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "append_setup": []any{"echo ok"}, "setup": []any{"x"}}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("setup with append_setup: %v", err)
	}
	other := t.TempDir()
	if _, err := f.call(t, "", tool.FolderEnvironmentRebuild, map[string]any{"workspace_path": other}); err == nil || !strings.Contains(err.Error(), "not a registered folder") {
		t.Fatalf("rebuild of an unregistered folder: %v", err)
	}
}

// Purpose: the home's linked work rule gates every environment call and
// secrets/permissions never cross the link. Off refuses before the machine
// is contacted; ask waits for the person only before a change; secret and
// permission fields are refused by name on the home and on the machine,
// with nothing stored; reads carry no secret names or values. Threat: a home
// with linked work off changing a machine, or a home managing secret grants
// or permission rules through an environment update. Boundary:
// permission.Service.AuthorizeToolCall, Server.ManageFolderEnvironment,
// fleetLinkHandler + decodeSandboxEnvironmentBody on the machine.
func TestLinkedFolderEnvironmentAccessAndSecretRules(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	if _, err := f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "base_image": "debian:12", "wait_seconds": float64(10)}); err != nil {
		t.Fatal(err)
	}
	before := f.stored(t)

	// Off: refused, nothing reaches the machine.
	f.setLinkedRule(t, pebblestore.RuleOff)
	for _, action := range []string{tool.FolderEnvironmentGet, tool.FolderEnvironmentUpdate, tool.FolderEnvironmentRebuild, tool.FolderEnvironmentList} {
		if _, err := f.call(t, "box", action, map[string]any{"workspace_path": f.folder, "append_setup": []any{"echo off"}}); err == nil || !strings.Contains(err.Error(), `linked work for machine "box" is off`) {
			t.Fatalf("%s with linked work off: %v", action, err)
		}
	}
	if after := f.stored(t); after.Hash != before.Hash || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("off changed the machine: %+v", after)
	}

	// The permission gate: off denies, ask waits only for changes, auto runs.
	perms := permission.NewService(pebblestore.NewPermissionStore(f.home.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(f.home.s.sessions)
	session := pebblestore.SessionSnapshot{ID: "env-orch", AccountScopeID: f.homeP.AccountScopeID, UserID: f.homeP.UserID, Metadata: map[string]any{}}
	if err := f.home.s.sessions.Store().CreateSessionForAccount(session, f.homeP.UserID, f.homeP.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	authorize := func(call, args string) permission.AuthorizationResult {
		t.Helper()
		got, err := perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: "env-orch", AccountScopeID: f.homeP.AccountScopeID, RunID: "run", CallID: call, ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	update := `{"action":"update_environment","machine":"box","workspace_path":"` + f.folder + `","environment":{"append_setup":["echo hi"]}}`
	read := `{"action":"get_environment","machine":"box","workspace_path":"` + f.folder + `"}`
	if got := authorize("off-update", update); got.Decision != permission.AuthorizationDeny {
		t.Fatalf("off update: %+v", got)
	}
	f.setLinkedRule(t, pebblestore.RuleAsk)
	if got := authorize("ask-update", update); got.Decision != permission.AuthorizationPending || got.Record == nil {
		t.Fatalf("ask update must wait for the person: %+v", got)
	}
	if got := authorize("ask-rebuild", strings.Replace(update, "update_environment", "rebuild_environment", 1)); got.Decision != permission.AuthorizationPending {
		t.Fatalf("ask rebuild must wait for the person: %+v", got)
	}
	if got := authorize("ask-read", read); got.Decision != permission.AuthorizationApprove || got.Record != nil {
		t.Fatalf("ask read waited: %+v", got)
	}
	f.setLinkedRule(t, pebblestore.RuleAuto)
	if got := authorize("auto-update", update); got.Decision != permission.AuthorizationApprove {
		t.Fatalf("auto update: %+v", got)
	}

	// Secret and permission fields are refused by name on the home...
	for _, field := range []string{"secret_grants", "permissions", "secrets"} {
		_, err := f.call(t, "box", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, field: []any{"DEPLOY_TOKEN"}})
		if err == nil || !strings.Contains(err.Error(), field+" refused") || !strings.Contains(err.Error(), "box itself") {
			t.Fatalf("home %s: %v", field, err)
		}
	}
	// ...and by the machine when they arrive over the link anyway.
	client, _, _, err := f.home.s.fleetMachineClient(f.home.account, "box")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct{ method, field string }{{http.MethodPatch, "secret_grants"}, {http.MethodPut, "permissions"}, {http.MethodPatch, "grants"}} {
		err := client.JSON(ctx, tc.method, SandboxEnvironmentsPath, map[string]any{"workspace_path": f.folder, "base_image": "debian:12", tc.field: []string{"DEPLOY_TOKEN"}}, nil)
		var refused *fleet.Error
		if !errors.As(err, &refused) || refused.Status != http.StatusForbidden || !strings.Contains(refused.Message, tc.field+" refused") || !strings.Contains(refused.Message, "never managed over the link") {
			t.Fatalf("machine %s %s: %v", tc.method, tc.field, err)
		}
	}
	if after := f.stored(t); after.Hash != before.Hash || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("refused fields changed the machine: %+v", after)
	}

	// Reads over the link carry no secret names or values: a secret granted
	// to the folder stays on the machine.
	slots := pebblestore.NewSecretSlotStore(f.remote.s.sessions.Store().Underlying())
	if _, err := slots.UpsertSlot(f.remote.account, "DEPLOY_TOKEN", "deploy", []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.Grant(f.remote.account, "DEPLOY_TOKEN", f.folder, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{tool.FolderEnvironmentGet, tool.FolderEnvironmentList} {
		out, err := f.call(t, "box", action, map[string]any{"workspace_path": f.folder})
		raw, _ := json.Marshal(out)
		// The folder path holds the test's name; the rest must not name secrets.
		text := strings.ReplaceAll(string(raw), f.folder, "<folder>")
		if err != nil || strings.Contains(text, "DEPLOY_TOKEN") || strings.Contains(strings.ToLower(text), "secret") {
			t.Fatalf("%s carries secrets: %s %v", action, raw, err)
		}
	}
	if err := client.JSON(ctx, http.MethodGet, "/v1/secrets/DEPLOY_TOKEN/value", nil, nil); err == nil {
		t.Fatal("secret value crossed the link")
	}
}

// Purpose: callers that were refused before are still refused: a request
// without a principal on the environment route, the deployment environments
// route (/v1/environments) over the link without a Swarm or Orchestrator
// session, and a task session using the Orchestrator's environment actions.
// A machine that does not answer is named with the last successful contact
// instead of a bare "could not reach". Boundary: handleSandboxEnvironments,
// handleEnvironments (authorizeEnvironmentHTTPSession), Runtime
// executeFolderEnvironments, fleetUnreachableError.
func TestFolderEnvironmentRefusalsStayAndUnreachableIsNamed(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	w := httptest.NewRecorder()
	f.remote.s.handleSandboxEnvironments(w, httptest.NewRequest(http.MethodGet, SandboxEnvironmentsPath+"?workspace_path="+f.folder, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.remote.s.handleSandboxEnvironments(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPatch, SandboxEnvironmentsPath, strings.NewReader(`{"workspace_path":"`+f.folder+`","secrets":["X"]}`)), f.remote.user, f.remote.account))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "secrets refused") {
		t.Fatalf("local secrets field: %d %s", w.Code, w.Body.String())
	}
	if _, found, _ := f.envs.Get(f.folder); found {
		t.Fatal("refused local request stored an environment")
	}
	w = httptest.NewRecorder()
	f.remote.s.handleSandboxEnvironments(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPatch, SandboxEnvironmentsPath, strings.NewReader(`{"workspace_path":"`+f.folder+`","base_image":"debian:12"}`)), f.remote.user, f.remote.account))
	if w.Code != http.StatusAccepted {
		t.Fatalf("local PATCH: %d %s", w.Code, w.Body.String())
	}

	// MCP (swarm_set_environment's merge and rebuild) reaches the same routes.
	for _, route := range []struct{ method, path string }{{http.MethodPatch, SandboxEnvironmentsPath}, {http.MethodPost, SandboxEnvironmentsPath + "/rebuild"}, {http.MethodGet, SandboxEnvironmentsPath + "/list"}} {
		if !controlMCPRouteAllowed(route.method, route.path) {
			t.Fatalf("MCP cannot reach %s %s", route.method, route.path)
		}
	}

	client, _, _, err := f.home.s.fleetMachineClient(f.home.account, "box")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = client.JSON(ctx, http.MethodGet, "/v1/environments?workspace_path="+f.folder, nil, nil)
	var refused *fleet.Error
	if !errors.As(err, &refused) || refused.Status != http.StatusForbidden || !strings.Contains(refused.Message, "environment access denied") {
		t.Fatalf("deployment environments over the link: %v", err)
	}

	// A task session cannot use the Orchestrator's environment actions.
	runtime := tool.NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageSessionService(f.home.s.sessions)
	runtime.SetProjectTaskLifecycleService(f.home.s)
	task := pebblestore.SessionSnapshot{ID: "env-task", AccountScopeID: f.homeP.AccountScopeID, UserID: f.homeP.UserID, Metadata: map[string]any{"task_id": "t1"}}
	if err := f.home.s.sessions.Store().CreateSessionForAccount(task, f.homeP.UserID, f.homeP.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "env-task", Principal: f.homeP}, tool.Call{Name: "manage_projects", Arguments: `{"action":"get_environment","machine":"box","workspace_path":"` + f.folder + `"}`}); err == nil || !strings.Contains(err.Error(), "managed by the Orchestrator") {
		t.Fatalf("task session: %v", err)
	}

	// Unreachable: the machine's address stops answering.
	machine, _, err := f.home.s.fleet.GetMachine("box")
	if err != nil || machine.LastContactAt == 0 {
		t.Fatalf("machine record: %+v %v", machine, err)
	}
	machine.Address = "127.0.0.1:1"
	if err := f.home.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}
	_, err = f.call(t, "box", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder})
	if err == nil || !strings.Contains(err.Error(), "box unreachable") || !strings.Contains(err.Error(), "last successful contact at") || strings.Contains(err.Error(), machine.Address) {
		t.Fatalf("unreachable machine: %v", err)
	}
}

// Purpose: the Orchestrator's manage_projects environment actions run
// through the tool runtime here (no machine) and on a linked machine, with the
// environment object flattened into the call. Boundary: Runtime
// executeManageProjects -> executeFolderEnvironments -> the API server's
// ManageFolderEnvironment (tool.FolderEnvironments).
func TestManageProjectsEnvironmentActionsLocalAndLinked(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(server *Server, sessions *fleetTestSwarm, p identity.Principal, args string) map[string]any {
		t.Helper()
		runtime := tool.NewRuntime(1)
		defer runtime.Close()
		runtime.SetManageSessionService(server.sessions)
		runtime.SetProjectTaskLifecycleService(server)
		id := "orch-" + sessions.account
		if _, found, _ := server.sessions.GetSession(id); !found {
			if err := server.sessions.Store().CreateSessionForAccount(pebblestore.SessionSnapshot{ID: id, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{}}, p.UserID, p.AccountScopeID); err != nil {
				t.Fatal(err)
			}
		}
		output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: id, Principal: p}, tool.Call{Name: "manage_projects", Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(output), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Local, on the machine itself.
	local := run(f.remote.s, f.remote, f.remoteP, `{"action":"update_environment","workspace_path":"`+f.folder+`","environment":{"base_image":"debian:12","setup":["apt-get install -y git"],"wait_seconds":10}}`)
	if local["action"] != tool.FolderEnvironmentUpdate || local["machine"] != nil || local["untrusted_content"] != nil {
		t.Fatalf("local answer: %v", local)
	}
	// Linked, from the home.
	linked := run(f.home.s, f.home, f.homeP, `{"action":"update_environment","machine":"box","workspace_path":"`+f.folder+`","environment":{"append_setup":["cargo --version"],"max_leases":3,"wait_seconds":10}}`)
	if linked["machine"] != "box" || linked["untrusted_content"] != true {
		t.Fatalf("linked answer: %v", linked)
	}
	stored := f.stored(t)
	if len(stored.Definition.Setup) != 2 || stored.Definition.Setup[1] != "cargo --version" || stored.MaxLeases != 3 || stored.Definition.BaseImage != "debian:12" {
		t.Fatalf("linked merge: %+v", stored)
	}
	listed := run(f.home.s, f.home, f.homeP, `{"action":"list_environments","machine":"box"}`)
	data := listed["data"].(map[string]any)
	if data["count"] != float64(1) {
		t.Fatalf("linked list: %v", listed)
	}
	got := run(f.remote.s, f.remote, f.remoteP, `{"action":"get_environment","workspace_path":"`+f.folder+`"}`)
	if environmentDefinition(t, got["data"].(map[string]any)["environment"]).Setup[1] != "cargo --version" {
		t.Fatalf("local get: %v", got)
	}
}

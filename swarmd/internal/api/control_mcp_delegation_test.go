package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/worktree"
)

// catalogListingCall is a Swarm Control call with a catalog runtime wired, so
// tools/list and advertisedToolAllowed answer from the catalog declarations.
func catalogListingCall(scopes []string) *controlMCPCall {
	return &controlMCPCall{
		request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &pebblestore.ScopedTokenRecord{Scopes: scopes}),
		server:  &Server{toolCatalog: tool.NewRuntime(1)},
	}
}

// Requirement: external goal delegation exposes every lifecycle control in
// Swarm Control's default listing (catalog tools and actions) while retaining
// permission levels. Threat: hidden delegate/wait/result/answer/stop/merge
// actions, or a read key that sees or calls a change. Owners: visibleTools,
// advertisedToolAllowed, callTool and the catalog declarations. This adapter
// test is the narrowest layer for discovery and pre-dispatch authorization.
func TestControlMCPDefaultGoalLifecycle(t *testing.T) {
	required := []string{"manage_projects propose_task", "manage_projects wait_tasks", "manage_projects get_task", "manage_projects list_tasks", "manage_sessions get", "manage_sessions resolve_permission", "manage_sessions stop", "manage_sessions send_message", "manage_projects integrate_task", "manage_projects get_task_changes", "manage_projects read_task_file", "manage_media generate", "manage_jobs get_job", "manage_media save", "manage_jobs schedule_job"}
	c := catalogListingCall([]string{"swarm:read", "swarm:write", "swarm:approve", "swarm:manage", "sessions:read", "sessions:write"})
	for _, name := range required {
		if !c.advertisedToolAllowed(name) {
			t.Errorf("default tools hide %s", name)
		}
	}
	for _, listed := range c.visibleTools() {
		if _, ok := tool.CatalogToolNamed(listed.Name); !ok {
			t.Errorf("Swarm Control lists %s, which is not a catalog tool", listed.Name)
		}
	}
	changes := []string{"manage_projects propose_task", "manage_sessions resolve_permission", "manage_sessions stop", "manage_projects integrate_task", "manage_projects update"}
	c = catalogListingCall([]string{"swarm:read", "sessions:read"})
	for _, name := range changes {
		if c.advertisedToolAllowed(name) {
			t.Errorf("read key advertises %s", name)
		}
	}
	dispatched := false
	c.next = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched = true })
	for _, name := range changes {
		toolName, action, _ := strings.Cut(name, " ")
		raw, _ := json.Marshal(map[string]any{"name": toolName, "arguments": map[string]any{"action": action}})
		result, rpcErr := c.callTool(raw)
		if rpcErr != nil || result["isError"] != true {
			t.Fatalf("read key call %s not refused: %v %v", name, result, rpcErr)
		}
	}
	if dispatched {
		t.Fatal("read key reached a mutation handler")
	}
}

// Requirement: omitted role delegates to Swarm without forcing a plan; explicit
// roles and media remain intact. Threat: silent direct-Coder or plan rewrite.
// Owner: controlMCPRunTask. An HTTP adapter capture proves the exact canonical
// request without executing a model or inventing provider proof.
func TestControlMCPRunTaskDefaultsToSwarm(t *testing.T) {
	for _, role := range []string{"", "swarm", "coder", "finder", "designer", "image", "video", "sound"} {
		t.Run("role="+role, func(t *testing.T) {
			args := map[string]any{"project_id": "project-test", "brief": "Inspect the requested work and return evidence."}
			if role != "" {
				args["role"] = role
			}
			if role == "coder" {
				args["workspace"] = "workspace-test"
			}
			var captured map[string]any
			calls := 0
			c := &controlMCPCall{request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:write"}}), next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v3/projects/project-test/tasks" {
					t.Fatalf("unexpected provisioning/route: %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatal(err)
				}
				writeJSON(w, http.StatusOK, map[string]any{"task": map[string]any{"id": "task-test", "agent": captured["agent"], "status": "queued", "session_id": "session-test"}})
			})}
			out, err := controlMCPRunTask(c, args)
			if err != nil {
				t.Fatal(err)
			}
			expected := role
			if expected == "" {
				expected = "swarm"
			}
			if expected == "swarm" {
				name, mode := projectTaskExecutionAgent(&pebblestore.ProjectTaskRecord{Agent: expected, Status: "queued"})
				if name != "swarm" || mode != "auto" {
					t.Fatalf("ordinary Swarm task forced to %s/%s", name, mode)
				}
			}
			if captured["agent"] != expected || out.(map[string]any)["role"] != expected || calls != 1 {
				t.Fatalf("role/dispatch mismatch: %v %v", captured, out)
			}
			for _, key := range []string{"plan", "execution_mode", "worker_id", "task_program"} {
				if _, found := captured[key]; found {
					t.Fatalf("unexpected forced %s: %v", key, captured)
				}
			}
			c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
			if _, err = controlMCPRunTask(c, args); err == nil || calls != 1 {
				t.Fatal("read-only delegation did not fail before dispatch")
			}
		})
	}
}

func newMCPPresetTestCall(t *testing.T) (*controlMCPCall, *pebblestore.WorkerStore) {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	ids := pebblestore.NewIdentityStore(sessions.Store().Underlying())
	if _, err := ids.PutUser(principal.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(principal.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(principal.AccountUser); err != nil {
		t.Fatal(err)
	}
	runner := server.runner.(*runruntime.Service)
	execution, err := runruntime.NewWorkerExecutionService(runner, sessions.Store(), &worktree.Service{}, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		t.Fatal("preset operation tried to execute work")
		return sessionruntime.SessionMutationResult{}, nil
	}, func(identity.Principal, pebblestore.V3SessionRunIntent) bool {
		t.Fatal("preset operation tried to enqueue work")
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "automations:read", "automations:write"}})
	return &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}, sessions.Store().WorkerStore()
}

// Requirement: discovery/delegation never provision specialist presets; explicit
// deployments remain durable and account-scoped. Threat: read side effects,
// missing-name implicit creation, unrelated-role writes and cross-account access.
// Owners: controlMCPListWorkerJobs, resolveWorker, DeployWorker, EnsurePreset and
// WorkerStore. Real temporary Pebble/API state is the narrowest proof of writes.
func TestControlMCPWorkerPresetsRequireExplicitDeployment(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	snapshot := func() []pebblestore.WorkerRecord {
		t.Helper()
		got, err := ws.ListWorkers(testPrincipal().AccountScopeID, pebblestore.ListWorkersQuery{Limit: 100, IncludeDeleted: true})
		if err != nil {
			t.Fatal(err)
		}
		return got.Workers
	}
	if len(snapshot()) != 0 {
		t.Fatal("fixture has workers")
	}
	if _, err := controlMCPListWorkerJobs(c, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.resolveWorker("coder"); err == nil {
		t.Fatal("absent preset resolved")
	}
	if len(snapshot()) != 0 {
		t.Fatal("discovery or missing worker resolution provisioned presets")
	}
	readOnly := *c
	readOnly.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "automations:read"}})
	if _, err := readOnly.resolveWorker("coder"); err == nil || strings.Contains(err.Error(), "manage_workers create") || strings.Contains(err.Error(), "propose_task") {
		t.Fatalf("read-only missing-worker hint: %v", err)
	}
	if len(snapshot()) != 0 {
		t.Fatal("read-only lookup changed worker state")
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write", "automations:read", "automations:write"}})
	if _, err := controlMCPRunTask(c, map[string]any{"project_id": "missing-project", "brief": "A goal without an existing project"}); err == nil {
		t.Fatal("missing project delegation accepted")
	}
	if len(snapshot()) != 0 {
		t.Fatal("rejected delegation provisioned workers")
	}
	if _, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder"}); err != nil {
		t.Fatal(err)
	}
	before := snapshot()
	if len(before) != 1 || before[0].Role != "coder" || before[0].LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("explicit deployment did not create only active Coder: %v", before)
	}
	for i := 0; i < 2; i++ {
		if _, err := controlMCPListWorkerJobs(c, map[string]any{"include_jobs": true}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolveWorker("coder"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolveWorker("designer"); err == nil {
			t.Fatal("missing Designer resolved")
		}
		if _, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder"}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("repeat/list/resolve changed existing deployment or provisioned others")
	}
	for _, tc := range []struct {
		name    string
		request *http.Request
		preset  string
	}{
		{"read-only", requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}}), "designer"},
		{"other-account", requestWithTestPrincipalForAccount(c.request, "other-user", "other-account"), "designer"},
		{"invalid-preset", c.request, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scoped := *c
			scoped.request = tc.request
			if _, err := controlMCPDeployWorker(&scoped, map[string]any{"preset": tc.preset}); err == nil {
				t.Fatal("unauthorized/invalid deploy accepted")
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("rejected deployment changed state")
			}
		})
	}
	other, err := ws.ListWorkers("other-account", pebblestore.ListWorkersQuery{Limit: 100})
	if err != nil || len(other.Workers) != 0 {
		t.Fatalf("cross-account write: %v %v", other, err)
	}
	if _, err := c.dispatch(http.MethodPost, WorkersEnsurePresetsPath, nil, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, worker := range snapshot() {
		roles = append(roles, worker.Role)
	}
	slices.Sort(roles)
	if !slices.Equal(roles, []string{"coder", "designer", "finder", "reviewer"}) {
		t.Fatalf("explicit legacy all-presets operation lost compatibility: %v", roles)
	}
}

// Requirement: pending questions point at their permission receipt, and terminal
// status never fabricates integration. Threat: wrong answer path or false Git
// success. Owner: controlMCPTaskNext; the status projection is the narrowest layer.
func TestControlMCPTaskNextUsesCanonicalReceipts(t *testing.T) {
	c := catalogListingCall([]string{"sessions:read", "sessions:write"})
	next := c.taskNext(map[string]any{"status": "needs_input"})
	if !strings.Contains(next, "resolve_permission") || !strings.Contains(next, "pending_permissions") {
		t.Fatal(next)
	}
	next = c.taskNext(map[string]any{"status": "completed"})
	if strings.Contains(next, "finished and integrated") || !strings.Contains(next, "integration evidence") {
		t.Fatal(next)
	}
	c.request = requestWithScopedToken(c.request, &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
	for _, status := range []string{"needs_input", "pending_approval", "needs_review", "failed"} {
		next = c.taskNext(map[string]any{"status": status})
		for _, hidden := range []string{"resolve_permission", "approve_task", "integrate_task", "send_message", "propose_task"} {
			if strings.Contains(next, hidden) {
				t.Fatalf("read-only next action names unavailable %s: %s", hidden, next)
			}
		}
	}

}

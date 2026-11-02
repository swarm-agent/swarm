package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// sourceRouteCall calls a machine's project source route directly, as the
// router hands it a request: principal p, an optional scoped token and an
// optional attested linked home (only fleetLinkHandler sets one).
func sourceRouteCall(t *testing.T, s *Server, method, path string, p identity.Principal, scopes []string, home *pebblestore.FleetHome) (int, string) {
	t.Helper()
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"workspace_path":"","inspection":null}`)
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req = requestWithPrincipalContext(req, p)
	if scopes != nil {
		req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{Scopes: scopes, UserID: p.UserID})
	}
	if home != nil {
		req = req.WithContext(context.WithValue(req.Context(), fleetHomeContextKey{}, *home))
	}
	w := httptest.NewRecorder()
	s.handleProjects(w, req)
	return w.Code, w.Body.String()
}

// Purpose:
//   - Requirement: the project source routes are the linked-machine side of
//     manage_projects list_sources, inspect_source and inspect_files and
//     answer only this Swarm's linked home over the link, as that home's own
//     session here; they never widen who may inspect sources beyond the
//     audience the home actions already had (the Orchestrator, Swarm
//     Control's catalog list_sources/inspect_source).
//   - Threat/regression: the route as first added admitted any caller holding
//     projects:read or sessions:read (or an owner client), so an ordinary
//     read token could list sources, read branch/HEAD/untracked names and
//     read committed task files with no bound conversation; a caller
//     self-asserting the link's session, a link context carrying another
//     session, or a link session of another account reaching data.
//   - Boundary/authority: handleProjects -> handleProjectSources ->
//     linkedSourceCaller (fleetHomeFromRequest, set only by fleetLinkHandler)
//     and, past it, InspectProjectSources' account-scoped project lookup.
//   - Narrowest layer: the route handler with the request context the router
//     and link listener build; the real link is TestLinkedProjectSourceInspection's.
func TestProjectSourceRoutesAnswerOnlyTheLinkedHome(t *testing.T) {
	f := newLinkedSourceFixture(t)
	box := f.h.remote
	owner := identity.Principal{Type: identity.PrincipalTypeUser, UserID: box.user, AccountScopeID: box.account, SessionID: "link-session", AccountScopeSource: identity.AccountScopeSourceSession}
	home := pebblestore.FleetHome{Fingerprint: "home-fingerprint", Name: "home", SessionID: "link-session"}
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v3/projects/remote-project/sources"},
		{http.MethodGet, "/v3/projects/remote-project/sources/inspect?workspace_id=" + f.workspace},
		{http.MethodPost, "/v3/projects/remote-project/sources/inspect-files"},
	}
	client := owner
	client.SessionID = ""
	otherSession := owner
	otherSession.SessionID = "another-client-session"
	otherAccount := owner
	otherAccount.AccountScopeID = "another-account"
	for _, route := range routes {
		refused := []struct {
			name   string
			p      identity.Principal
			scopes []string
			home   *pebblestore.FleetHome
		}{
			{"projects:read token", owner, []string{"projects:read"}, nil},
			{"sessions:read token", owner, []string{"sessions:read"}, nil},
			{"read and write token", owner, []string{"swarm:read", "projects:read", "projects:write", "sessions:read", "sessions:write"}, nil},
			{"owner client without the link", client, nil, nil},
			{"self-asserted link session without the link", owner, nil, nil},
			{"link context with another session (forged proof)", otherSession, nil, &home},
			{"link context without a session", owner, nil, &pebblestore.FleetHome{Fingerprint: "home-fingerprint"}},
		}
		for _, c := range refused {
			code, body := sourceRouteCall(t, box.s, route.method, route.path, c.p, c.scopes, c.home)
			if code != http.StatusForbidden || strings.Contains(body, f.folder) || strings.Contains(body, linkedSourceMarker) {
				t.Fatalf("%s %s as %s: HTTP %d %s", route.method, route.path, c.name, code, body)
			}
		}
		// Another account's link session: admitted as the home, but its
		// account holds no such project, so nothing of this one comes back.
		if code, body := sourceRouteCall(t, box.s, route.method, route.path, otherAccount, nil, &home); code == http.StatusOK || strings.Contains(body, f.folder) {
			t.Fatalf("%s %s across accounts: HTTP %d %s", route.method, route.path, code, body)
		}
	}
	// The linked home's own session: answered.
	code, body := sourceRouteCall(t, box.s, http.MethodGet, "/v3/projects/remote-project/sources", owner, nil, &home)
	if code != http.StatusOK || !strings.Contains(body, f.folder) {
		t.Fatalf("linked home list: HTTP %d %s", code, body)
	}
	code, body = sourceRouteCall(t, box.s, http.MethodGet, "/v3/projects/remote-project/sources/inspect?workspace_id="+f.workspace, owner, nil, &home)
	if code != http.StatusOK || !strings.Contains(body, f.head) {
		t.Fatalf("linked home inspect: HTTP %d %s", code, body)
	}
}

// Purpose:
//   - Requirement: a home reaches a machine's project source routes only
//     through CallFleetProject, behind its bound-conversation check; its raw
//     machine proxy (/v3/fleet/machines/{name}/api/...) refuses every path
//     the machine would route to them, judged on the path as it is sent and
//     decoded again on the machine, while other proxied routes still work.
//   - Threat/regression: a home owner client or admin token reading a
//     machine's sources and committed task files as a bare owner request
//     through the proxy, around the Orchestrator-only gate. That includes
//     percent-encoded spellings the home decodes into one segment but the
//     machine decodes into a source route (%252F, %252e), and ? or #
//     spelled %3F / %23 that end the path once the request is sent.
//   - Boundary/authority: handleFleet -> handleFleetForward ->
//     fleetSourceProxyRefused.
//   - Narrowest layer: the home's fleet route over the real link.
func TestHomeProxyRefusesMachineSourceRoutes(t *testing.T) {
	f := newLinkedSourceFixture(t)
	home := f.h.home
	for _, path := range []string{
		"/v3/fleet/machines/box/api/v3/projects/remote-project/sources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/sources/inspect?workspace_id=" + f.workspace,
		"/v3/fleet/machines/box/api/v3/projects/remote-project/x/../sources",
		"/v3/fleet/machines/box/api/v3//projects/remote-project/sources/inspect",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/./sources/inspect-files",
		"/v3/fleet/machines/box/api/v3/projects/remote-project%2Fsources%2Finspect",
		"/v3/fleet/machines/box/api/v3/projects/remote-project%252Fsources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project%252Fsources%252Finspect?workspace_id=" + f.workspace,
		"/v3/fleet/machines/box/api/v3/projects/remote-project%252Fsources%252Finspect-files",
		"/v3/fleet/machines/box/api/v3%252Fprojects%252Fremote-project%252Fsources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/x/%2e%2e/sources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/x%252F%252e%252e%252Fsources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/%2573ources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project%25252Fsources",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/sources%3Fx",
		"/v3/fleet/machines/box/api/v3/projects/remote-project/sources%23x",
	} {
		method := http.MethodGet
		if strings.HasSuffix(path, "inspect-files") {
			method = http.MethodPost
		}
		code, out := home.call(t, method, path, `{}`, false)
		if code != http.StatusForbidden || !strings.Contains(asStringValue(out["error"]), "never through the proxy") {
			t.Fatalf("%s %s: HTTP %d %v", method, path, code, out)
		}
	}
	// Another machine route still passes through the proxy.
	if code, out := home.call(t, http.MethodGet, "/v3/fleet/machines/box/api/v3/projects/remote-project", "", false); code != http.StatusOK {
		t.Fatalf("proxied project read: HTTP %d %v", code, out)
	}
}

func asStringValue(v any) string {
	s, _ := v.(string)
	return s
}

// Purpose:
//   - Requirement: the home side of the linked source actions keeps the home
//     actions' audience: the home's own Orchestrator reaches them; a home
//     task conversation does not (linked delegation is the Orchestrator's),
//     and Swarm Control never runs inspect_files, here or linked. The home's
//     local list_sources and inspect_files keep their own behavior
//     (inspect_files still needs a bound project or task conversation).
//   - Threat/regression: the linked path admitting a caller the home action
//     refuses, so linked inspection is wider than local inspection.
//   - Boundary/authority: tool.Runtime.executeFleetProjects
//     (homeConversation, the inspect_files Swarm Control refusal) and, at home,
//     InspectProjectSources and ResolveProjectInspection.
//   - Narrowest layer: the native manage_projects runtime over the real link.
func TestLinkedSourceInspectionKeepsTheHomeAudience(t *testing.T) {
	f := newLinkedSourceFixture(t)
	h := f.h
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(scope tool.WorkspaceScope, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	}
	list := map[string]any{"action": "list_sources", "machine": "box", "project_id": "remote-project"}
	files := map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "workspace_id": f.workspace, "inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "notes.txt"}}}

	// The home's own Orchestrator: served.
	if out, err := run(tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: h.p}, files); err != nil || !strings.Contains(out, linkedSourceMarker) {
		t.Fatalf("home Orchestrator linked inspect_files: %v %s", err, out)
	}
	// A home task conversation: refused before the link.
	h.createGoal(t, "home-task-conversation", map[string]any{"task_id": "home-task", "project_id": "home-project"})
	for _, args := range []map[string]any{list, files} {
		if out, err := run(tool.WorkspaceScope{SessionID: "home-task-conversation", Principal: h.p}, args); err == nil || !strings.Contains(err.Error(), "only available to the home's Orchestrator") {
			t.Fatalf("task conversation linked %s: %v %s", args["action"], err, out)
		}
	}
	// Another account's principal on the Orchestrator's conversation.
	stranger := h.p
	stranger.AccountScopeID, stranger.UserID = "another-account", "another-user"
	if out, err := run(tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: stranger}, list); err == nil {
		t.Fatalf("another account's linked list_sources answered: %s", out)
	}
	// Swarm Control never runs inspect_files, linked or not.
	if out, err := run(tool.WorkspaceScope{Principal: h.p, ControlCaller: true}, files); err == nil || strings.Contains(out, linkedSourceMarker) {
		t.Fatalf("Swarm Control linked inspect_files: %v %s", err, out)
	}

	// The home's own sources: local list_sources still answers, and local
	// inspect_files still needs a bound project or task conversation.
	h.runtime.SetManageProjectStore(h.home.s.sessions.Store())
	h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.home.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	if err := os.WriteFile(filepath.Join(folder, "home.txt"), []byte("home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", "home.txt")
	outboxPinGit(t, folder, "commit", "-q", "-m", "home")
	added, err := h.home.s.workspace.AddForPrincipal(h.p, folder, "home", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.home.s.sessions.Store().PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "home-project", Name: "Home", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder}}}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: h.p}, map[string]any{"action": "list_sources", "project_id": "home-project"}); err != nil || !strings.Contains(out, folder) {
		t.Fatalf("home list_sources: %v %s", err, out)
	}
	if out, err := run(tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: h.p}, map[string]any{"action": "inspect_files", "project_id": "home-project", "workspace_id": added.WorkspaceID}); err == nil || !strings.Contains(err.Error(), "bound project task conversation") {
		t.Fatalf("home inspect_files without a bound conversation: %v %s", err, out)
	}
}

// Purpose:
//   - Requirement: with machine, inspect_files reads one exact committed task
//     result of the linked machine (a real retained task worktree with a
//     commit beyond its captured base), never the machine's current source
//     branch; target.reference keeps the exact task, attempt, session,
//     source and head_commit across bounded pages; a stale head_commit, a
//     wrong attempt or session, or a source that is not the task's is
//     refused on the machine.
//   - Threat/regression: a linked committed-result read silently answering
//     with the source's dev (or the worktree after it moved on), or a
//     continuation dropping the pinned identity.
//   - Boundary/authority: on the home, executeFleetProjects ->
//     CallFleetProject (fleetSourceRequest, verifyFleetSourceAnswer); on the
//     machine, fleetLinkHandler -> handleProjectSources -> linkedSourceCaller
//     -> ResolveProjectInspectionForPrincipal (resolveProjectInspectionTarget:
//     attempt, session, quiescence, ownership, Git lane, head_commit) ->
//     tool.Runtime.RunProjectInspection.
//   - Narrowest layer: two in-process daemons over the real pinned link with
//     a real Git repository and task worktree on the machine; no provider
//     runs.
func TestLinkedCommittedTaskResultInspection(t *testing.T) {
	root := t.TempDir()
	remote, fx := newLinkedReopenRemote(t, root)
	remotePrincipal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}
	retained := seedRetainedTask(t, remote.s, fx.sessions, remotePrincipal, filepath.Join(root, "repo"), "needs_review")
	boxRuntime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = boxRuntime.Close() })
	remote.s.SetToolCatalogRuntime(boxRuntime)
	// The task's committed result: tracked.txt changed and a paged file,
	// only in the task worktree; the source stays on its base.
	if err := os.WriteFile(filepath.Join(retained.child, "tracked.txt"), []byte("task result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retained.child, "paged.txt"), []byte("line-1\nline-2\nline-3\nline-4\nline-5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, retained.child, "add", "tracked.txt", "paged.txt")
	integrationGit(t, retained.child, "commit", "-m", "task result")
	head := integrationGit(t, retained.child, "rev-parse", "HEAD")
	attempt := retained.task(t, remote.s).ActiveAttempt()
	if attempt == nil || attempt.SessionID != retained.sessionID {
		t.Fatalf("fixture attempt: %+v", attempt)
	}

	home := newFleetTestSwarm(t, "home")
	t.Cleanup(home.cleanup)
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if code, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	fleetActivityTestSetWork(t, home, "box", pebblestore.RuleAuto)
	h := &remoteWaitHarness{home: home, remote: remote, p: identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}}
	h.createGoal(t, remoteWaitGoal, map[string]any{})
	h.runtime = tool.NewRuntime(1)
	t.Cleanup(func() { _ = h.runtime.Close() })
	h.runtime.SetManageSessionService(home.s.sessions)
	h.runtime.SetProjectTaskLifecycleService(home.s)
	call := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		return linkedTaskCall(t, h, remoteWaitGoal, args)
	}
	pinned := func(extra map[string]any) map[string]any {
		args := map[string]any{"action": "inspect_files", "machine": "box", "project_id": retained.projectID, "task_id": retained.taskID, "attempt_id": attempt.ID, "source_session_id": retained.sessionID}
		for key, value := range extra {
			args[key] = value
		}
		return args
	}
	readPage := func(t *testing.T, args map[string]any) (map[string]any, map[string]any, string) {
		t.Helper()
		out, err := call(args)
		if err != nil {
			t.Fatalf("linked task inspect_files %v: %v", args, err)
		}
		data := envelopeDataMap(out)
		target, _ := data["target"].(map[string]any)
		reference, _ := target["reference"].(map[string]any)
		encoded, _ := json.Marshal(data["inspection"])
		if out["machine"] != "box" || reference == nil {
			t.Fatalf("linked task answer: %v", out)
		}
		return target, reference, string(encoded)
	}

	// The committed result, not the source's dev.
	target, reference, read := readPage(t, pinned(map[string]any{"inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "tracked.txt"}}}))
	if !strings.Contains(read, "task result") || strings.Contains(read, "base") {
		t.Fatalf("read the source instead of the task result: %s", read)
	}
	if reference["head_commit"] != head || reference["task_id"] != retained.taskID || reference["attempt_id"] != attempt.ID || reference["source_session_id"] != retained.sessionID || reference["workspace_path"] != retained.source || target["base_commit"] != retained.base || target["branch"] != retained.branch {
		t.Fatalf("task result target: %v", target)
	}
	if source, err := os.ReadFile(filepath.Join(retained.source, "tracked.txt")); err != nil || string(source) != "base\n" {
		t.Fatalf("fixture: the source moved: %q %v", source, err)
	}

	// Bounded pages: the reference pins each continuation to the same result.
	continuation := func(extra map[string]any) map[string]any {
		args := map[string]any{"action": "inspect_files", "machine": "box"}
		for key, value := range reference {
			args[key] = value
		}
		for key, value := range extra {
			args[key] = value
		}
		return args
	}
	_, first, page1 := readPage(t, continuation(map[string]any{"inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "paged.txt", "line_start": 1, "max_lines": 2}}}))
	if !strings.Contains(page1, "line-2") || strings.Contains(page1, "line-3") || !strings.Contains(page1, `"next_line_start":3`) {
		t.Fatalf("first page: %s", page1)
	}
	_, second, page2 := readPage(t, continuation(map[string]any{"inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "paged.txt", "line_start": 3, "max_lines": 2}}}))
	if !strings.Contains(page2, "line-3") || !strings.Contains(page2, "line-4") || strings.Contains(page2, "line-2") {
		t.Fatalf("second page: %s", page2)
	}
	firstRaw, _ := json.Marshal(first)
	secondRaw, _ := json.Marshal(second)
	referenceRaw, _ := json.Marshal(reference)
	if string(firstRaw) != string(referenceRaw) || string(secondRaw) != string(referenceRaw) {
		t.Fatalf("pages changed the reference: %s / %s / %s", referenceRaw, firstRaw, secondRaw)
	}

	refusals := []struct {
		name string
		args map[string]any
		want string
	}{
		{"wrong attempt", pinned(map[string]any{"attempt_id": "another-attempt"}), "stale or mismatched"},
		{"wrong session", pinned(map[string]any{"source_session_id": "another-session"}), "stale or mismatched"},
		{"another source path", pinned(map[string]any{"workspace_path": filepath.Join(root, "elsewhere")}), "task source identity mismatch"},
		{"another source id", pinned(map[string]any{"workspace_id": "another-workspace"}), "task source identity mismatch"},
		{"stale source generation", pinned(map[string]any{"workspace_generation": float64(asInt64(reference["workspace_generation"]) + 5)}), "task source identity mismatch"},
		{"another head", pinned(map[string]any{"head_commit": retained.base}), "head_commit mismatch"},
	}
	for _, refusal := range refusals {
		if out, err := call(refusal.args); err == nil || !strings.Contains(err.Error(), refusal.want) {
			t.Fatalf("%s: %v %v", refusal.name, out, err)
		}
	}

	// The worktree moves on: the pinned head is now stale and refused, never
	// answered with the newer commit.
	if err := os.WriteFile(filepath.Join(retained.child, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, retained.child, "add", "later.txt")
	integrationGit(t, retained.child, "commit", "-m", "later")
	if out, err := call(continuation(map[string]any{"inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "tracked.txt"}}})); err == nil || !strings.Contains(err.Error(), "head_commit mismatch") {
		t.Fatalf("stale pinned head: %v %v", out, err)
	}
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

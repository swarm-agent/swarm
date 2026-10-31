package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// seedSessions gives each machine what the manage_sessions parity cases
// need: plain sessions (ms-* here, rms-* on the linked machine) with a long
// message to page through, twins for each change, archived twins to
// restore, a registered workspace for create, and the linked machine its
// own catalog runtime (the handler a home reaches with machine=).
func (h *catalogParityHarness) seedSessions(t *testing.T) {
	t.Helper()
	h.sessionFolders = map[string]string{}
	h.sessionPermissions = map[string]string{}
	remoteRuntime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = remoteRuntime.Close() })
	h.remoteRuntime = remoteRuntime
	remoteRuntime.SetManageSessionService(h.remote.s.sessions)
	remoteRuntime.SetProjectTaskLifecycleService(h.remote.s)
	h.remote.s.SetToolCatalogRuntime(remoteRuntime)
	if h.home.s.workspace == nil {
		h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.home.s.sessions.Store().Underlying()))
	}
	for _, machine := range []struct {
		name, prefix string
		swarm        *fleetTestSwarm
		runtime      *tool.Runtime
	}{{"home", "ms-", h.home, h.runtime}, {"remote", "rms-", h.remote, remoteRuntime}} {
		folder := t.TempDir()
		outboxPinGit(t, folder, "init", "-q", "-b", "dev")
		outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: machine.swarm.user, AccountScopeID: machine.swarm.account}
		if _, err := machine.swarm.s.workspace.AddForPrincipal(p, folder, "sessions", "", false); err != nil {
			t.Fatal(err)
		}
		h.sessionFolders[machine.name] = folder
		for _, id := range []string{"read", "arc-1", "arc-2", "unarc-1", "unarc-2", "msg-1", "msg-2"} {
			fleetActivityTestSession(t, machine.swarm, machine.prefix+id)
		}
		catalogSessionMessages(t, machine.swarm, machine.prefix+"read")
		// Each machine's permission service with one pending request per
		// twin (perm:ms-perm-1 and so on in the cases).
		perms := permission.NewService(pebblestore.NewPermissionStore(machine.swarm.s.sessions.Store().Underlying()), nil, nil)
		perms.SetSessionResolver(machine.swarm.s.sessions)
		machine.swarm.s.perm = perms
		for _, id := range []string{"perm-1", "perm-2"} {
			session := machine.prefix + id
			fleetActivityTestSession(t, machine.swarm, session)
			command, _ := json.Marshal(map[string]any{"command": "make test"})
			record, err := perms.CreatePending(permission.CreateInput{SessionID: session, RunID: "run-" + session, CallID: "call-" + session, ToolName: "bash", ToolArguments: string(command), Requirement: "tool", Mode: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			h.sessionPermissions[session] = record.ID
		}
		raw, _ := json.Marshal(map[string]any{"action": "archive", "session_ids": []string{machine.prefix + "unarc-1", machine.prefix + "unarc-2"}})
		if _, err := machine.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{Principal: p}, tool.Call{Name: "manage-sessions", Arguments: string(raw)}); err != nil {
			t.Fatalf("archive seed on %s: %v", machine.name, err)
		}
	}
}

// catalogSessionLong is the long message a read_messages page cuts.
var catalogSessionLong = strings.Repeat("Long report line é. ", 200)

// catalogSessionMessages gives a session a short message, a long one and a
// short one after it.
func catalogSessionMessages(t *testing.T, f *fleetTestSwarm, session string) {
	t.Helper()
	for _, m := range []pebblestore.MessageSnapshot{{ID: session + "-m1", Role: "user", Content: "Start."}, {ID: session + "-m2", Role: "assistant", Content: catalogSessionLong}, {ID: session + "-m3", Role: "assistant", Content: "Done."}} {
		m := m
		if _, err := f.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: f.account, UserID: f.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: m.ID, PayloadHash: m.ID, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
}

// manageSessionsParityCases: every manage_sessions action, here and with
// machine= on the linked machine. Changes run on twin sessions. Actions
// that need a service the harness does not run (execution capacity, a
// permission request, a provider's model catalog) compare the same error
// through both surfaces; the end-to-end test answers a real request.
func manageSessionsParityCases() map[string][]catalogParityCase {
	local := func(action string, more ...any) map[string]any {
		return parityArgs(append([]any{"action", action}, more...)...)
	}
	remote := func(action string, more ...any) map[string]any {
		return parityArgs(append([]any{"action", action, "machine", "box"}, more...)...)
	}
	both := func(action string, more ...any) []catalogParityCase {
		here := append([]any{}, more...)
		there := append([]any{}, more...)
		for i := 1; i < len(here); i += 2 {
			if text, ok := here[i].(string); ok && strings.HasPrefix(text, "ms-") {
				there[i] = "r" + text
			}
		}
		return []catalogParityCase{{orchestrator: local(action, here...), control: local(action, here...), compare: parityExact}, {orchestrator: remote(action, there...), control: remote(action, there...), compare: parityExact}}
	}
	twin := func(action, a, b string, more ...any) []catalogParityCase {
		withID := func(build func(string, ...any) map[string]any, id string) map[string]any {
			return build(action, append([]any{"session_id", id}, more...)...)
		}
		return []catalogParityCase{
			{orchestrator: withID(local, a), control: withID(local, b), compare: parityShape, twin: map[string]string{b: a}},
			{orchestrator: withID(remote, "r"+a), control: withID(remote, "r"+b), compare: parityShape, twin: map[string]string{"r" + b: "r" + a}},
		}
	}
	notServed := func(action string) []catalogParityCase {
		args := local(action, "session_id", "ms-read")
		return []catalogParityCase{{orchestrator: args, control: args, compare: parityNotServed}}
	}
	create := func(machine string, args map[string]any) catalogParityCase {
		args["title"], args["workspace_path"] = "Parity", "folder:"+machine
		return catalogParityCase{orchestrator: args, control: args, compare: parityShape}
	}
	return map[string][]catalogParityCase{
		"inspect":       both("inspect"),
		"list":          both("list", "limit", float64(5)),
		"list_by_state": both("list_by_state", "state", "running"),
		"search":        both("search", "query", "Fleet"),
		"get":           both("get", "session_id", "ms-read"),
		"read_messages": both("read_messages", "session_id", "ms-read", "limit", float64(3), "max_chars", float64(300)),
		"archive":       twin("archive", "ms-arc-1", "ms-arc-2"),
		"unarchive":     twin("unarchive", "ms-unarc-1", "ms-unarc-2"),
		"create":        {create("home", local("create")), create("remote", remote("create"))},
		"stop":          both("stop", "session_id", "ms-read"),
		"pause":         both("pause", "session_id", "ms-read"),
		"send_message":  twin("send_message", "ms-msg-1", "ms-msg-2", "prompt", "A note.", "trigger", false, "client_request_id", "note-1"),
		"compact":       both("compact", "session_id", "ms-read"),
		tool.ManageSessionsResolvePermissionAction: {
			{orchestrator: local(tool.ManageSessionsResolvePermissionAction, "session_id", "ms-perm-1", "permission_id", "perm:ms-perm-1", "decision", "allow_once"), control: local(tool.ManageSessionsResolvePermissionAction, "session_id", "ms-perm-2", "permission_id", "perm:ms-perm-2", "decision", "allow_once"), compare: parityShape, twin: map[string]string{"ms-perm-2": "ms-perm-1", "perm:ms-perm-2": "perm:ms-perm-1"}},
			{orchestrator: remote(tool.ManageSessionsResolvePermissionAction, "session_id", "rms-perm-1", "permission_id", "perm:rms-perm-1", "decision", "deny_once"), control: remote(tool.ManageSessionsResolvePermissionAction, "session_id", "rms-perm-2", "permission_id", "perm:rms-perm-2", "decision", "deny_once"), compare: parityShape, twin: map[string]string{"rms-perm-2": "rms-perm-1", "perm:rms-perm-2": "perm:rms-perm-1"}},
			{orchestrator: local(tool.ManageSessionsResolvePermissionAction, "session_id", "ms-read", "permission_id", "no-such-request", "decision", "allow_once"), control: local(tool.ManageSessionsResolvePermissionAction, "session_id", "ms-read", "permission_id", "no-such-request", "decision", "allow_once"), compare: parityExact},
		},
		tool.ManageSessionsSetModelAction: both(tool.ManageSessionsSetModelAction, "session_id", "ms-read", "provider", "fixture", "model", "no-such-model"),
		"review_worktrees":                notServed("review_worktrees"),
		"git_status":                      notServed("git_status"),
		"commit":                          notServed("commit"),
		"deploy":                          notServed("deploy"),
	}
}

// Purpose: the Orchestrator's manage_sessions definition is generated from
// the catalog's orchestrator profile: the actions it saw before plus
// resolve_permission and set_model, none of the Swarm agent's own session
// tooling, and every input a declared field. The Swarm agent keeps the
// runtime's full definition. Threat: the hand-written trim coming back as a
// second schema, or the Orchestrator shown launch or commit tooling.
// Boundary: tool.OrchestratorDefinitions over the runtime's definitions.
func TestManageSessionsOrchestratorDefinitionIsTheCatalogs(t *testing.T) {
	catalog, ok := tool.CatalogToolNamed("manage_sessions")
	if !ok {
		t.Fatal("manage_sessions is not a catalog tool")
	}
	var shown tool.Definition
	for _, definition := range tool.OrchestratorDefinitions((*tool.Runtime)(nil).Definitions()) {
		if definition.Name == "manage-sessions" || definition.Name == "manage_sessions" {
			shown = definition
		}
	}
	want := catalog.OrchestratorDefinition()
	want.Name = shown.Name
	a, _ := json.Marshal(shown)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("the Orchestrator's manage_sessions is not the catalog's:\n%s\n%s", a, b)
	}
	enum := shown.Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	got := strings.Join(enum, ",")
	if got != "inspect,list,list_by_state,search,get,read_messages,archive,unarchive,create,stop,pause,send_message,compact,resolve_permission,set_model" {
		t.Fatalf("Orchestrator actions: %s", got)
	}
	for _, banned := range []string{"deploy", "proposals", "recovery", "commits", "review_worktrees", "git_status"} {
		if strings.Contains(string(a), banned) {
			t.Fatalf("the Orchestrator's manage_sessions shows %q", banned)
		}
	}
	for _, action := range catalog.Actions {
		if action.Listed(tool.CatalogProfileOrchestrator) != action.ControlCallable() {
			t.Fatalf("%s: the Orchestrator and Swarm Control see different actions", action.Action)
		}
		for _, input := range action.Inputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: input %q is not a declared field", action.Action, input)
			}
		}
	}
}

// catalogSessionController stands in for the session executor: it records
// a run it is asked to start as running and one it is asked to stop as
// cancelled, in the real session store. No provider runs.
type catalogSessionController struct {
	t                   *testing.T
	f                   *fleetTestSwarm
	enqueued, cancelled []string
}

func (c *catalogSessionController) record(sessionID, runID, status string) {
	c.t.Helper()
	key := "e2e-" + runID + "-" + status
	if _, err := c.f.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: sessionID, AccountScopeID: c.f.account, UserID: c.f.user, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: key, PayloadHash: key, RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, Status: status}}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *catalogSessionController) EnqueueSessionRun(_ identity.Principal, sessionID, runID, _ string) bool {
	c.enqueued = append(c.enqueued, runID)
	c.record(sessionID, runID, pebblestore.V3RunIntentRunning)
	return true
}

func (c *catalogSessionController) CancelSessionRun(_ identity.Principal, sessionID, runID, _ string) (bool, error) {
	c.cancelled = append(c.cancelled, runID)
	c.record(sessionID, runID, pebblestore.V3RunIntentCancelled)
	return true, nil
}

func (c *catalogSessionController) CompactSession(context.Context, identity.Principal, string, string) (map[string]any, error) {
	return map[string]any{}, nil
}

// Purpose: an MCP client drives a plain session end to end through
// manage_sessions alone: create it in a registered workspace, send it a
// message (its run starts), read its messages page by page following next
// until the long message is whole, answer its pending permission request,
// and stop its run. A read key is refused the answer and the stop before
// anything changes, the Orchestrator cannot answer its own conversation's
// requests, and a session's run status stays in data while the call's
// outcome is the state. Threat: an MCP client with no way to answer a
// plain session's request (the gap before this stage), a level bypass, or
// a cut page with no way to read on. Boundary: controlMCPCall.callTool ->
// Runtime.ExecuteControlCatalog -> executeManageSessions, the permission
// resolve route through ManageSessionControl, over a real store; the run
// executor is the recording stand-in above.
func TestManageSessionsEndToEndOverSwarmControl(t *testing.T) {
	h := newCatalogParityHarness(t)
	perms := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(h.home.s.sessions)
	h.home.s.perm = perms
	controller := &catalogSessionController{t: t, f: h.home}
	h.runtime.SetManageSessionController(controller)
	call := func(scopes []string, args map[string]any) (map[string]any, map[string]any, string) {
		t.Helper()
		out, refused := h.controlTool(t, scopes, "manage_sessions", args)
		if refused != "" {
			return nil, nil, refused
		}
		envelope := catalogEnvelopeCheck(t, "manage_sessions "+args["action"].(string), out)
		return envelope, envelopeDataMap(envelope), ""
	}
	must := func(args map[string]any) (map[string]any, map[string]any) {
		t.Helper()
		envelope, data, refused := call(catalogParityFullKey, args)
		if refused != "" {
			t.Fatalf("%v: %s", args["action"], refused)
		}
		return envelope, data
	}

	// Create in a registered workspace; an unregistered one is refused.
	if _, _, refused := call(catalogParityFullKey, map[string]any{"action": "create", "title": "E2E", "workspace_path": t.TempDir()}); !strings.Contains(refused, "not a registered workspace") {
		t.Fatalf("unregistered workspace: %q", refused)
	}
	envelope, data := must(map[string]any{"action": "create", "title": "E2E", "workspace_path": h.sessionFolders["home"]})
	sessionID, _ := data["session_id"].(string)
	if envelope["state"] != tool.EnvelopeStateOK || data["status"] != "created" || sessionID == "" {
		t.Fatalf("create: %v", envelope)
	}

	// Send a message: its run starts; the run's status is data, the
	// outcome is ok.
	envelope, data = must(map[string]any{"action": "send_message", "session_id": sessionID, "prompt": "Write the report."})
	runID, _ := data["run_id"].(string)
	if envelope["state"] != tool.EnvelopeStateOK || data["status"] != "queued" || runID == "" || len(controller.enqueued) != 1 || controller.enqueued[0] != runID {
		t.Fatalf("send_message: %v (enqueued %v)", envelope, controller.enqueued)
	}

	// Read the messages page by page, following next, until done: the long
	// message is whole and the message after it on the same page (cut there
	// by the budget) is read too.
	catalogSessionMessages(t, h.home, sessionID)
	read := catalogFollowReadMessages(t, h, map[string]any{"action": "read_messages", "session_id": sessionID, "limit": float64(4), "max_chars": float64(400)})
	if read.content[sessionID+"-m2"] != catalogSessionLong {
		t.Fatalf("paging read %d of %d bytes of the long message", len(read.content[sessionID+"-m2"]), len(catalogSessionLong))
	}
	if read.content[sessionID+"-m1"] != "Start." || read.content[sessionID+"-m3"] != "Done." {
		t.Fatalf("paging skipped a message on the cut page: %v", read.order)
	}

	// A pending request: a read key is refused before anything runs, the
	// Orchestrator cannot answer its own conversation's request, an
	// approve key answers it once.
	args, _ := json.Marshal(map[string]any{"command": "make test"})
	pending, err := perms.CreatePending(permission.CreateInput{SessionID: sessionID, RunID: runID, CallID: "call-1", ToolName: "bash", ToolArguments: string(args), Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	answer := map[string]any{"action": tool.ManageSessionsResolvePermissionAction, "session_id": sessionID, "permission_id": pending.ID, "decision": "allow_once"}
	if _, _, refused := call([]string{"swarm:read", "swarm:write", "sessions:read", "sessions:write"}, answer); !strings.Contains(refused, "needs swarm:approve") {
		t.Fatalf("write key answered a permission: %q", refused)
	}
	if record, ok, _ := perms.GetPermission(sessionID, pending.ID); !ok || record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("a refused answer changed the request: %+v", record)
	}
	// A decoy key that differs from "action" only in case is refused by the
	// handler itself, on every surface, and the request stays pending.
	for _, decoy := range []map[string]any{
		{"action": tool.ManageSessionsResolvePermissionAction, "session_id": sessionID, "permission_id": pending.ID, "decision": "allow_once", "ACTION": "get"},
		{"action": "get", "session_id": sessionID, "permission_id": pending.ID, "decision": "allow_once", "Action": tool.ManageSessionsResolvePermissionAction},
	} {
		if _, _, refused := call(catalogParityFullKey, decoy); !strings.Contains(refused, `is not "action"`) {
			t.Fatalf("Swarm Control ran a call with a decoy action key: %q", refused)
		}
		decoyRaw, _ := json.Marshal(decoy)
		if _, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage_sessions", Arguments: string(decoyRaw)}); err == nil || !strings.Contains(err.Error(), `is not "action"`) {
			t.Fatalf("a conversation ran a call with a decoy action key: %v", err)
		}
		if record, ok, _ := perms.GetPermission(sessionID, pending.ID); !ok || record.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("a decoy call changed the request: %+v", record)
		}
	}
	raw, _ := json.Marshal(answer)
	if _, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{SessionID: sessionID, Principal: h.p}, tool.Call{Name: "manage_sessions", Arguments: string(raw)}); err == nil || !strings.Contains(err.Error(), "own requests") {
		t.Fatalf("a conversation answered its own request: %v", err)
	}
	envelope, data = must(answer)
	if envelope["state"] != tool.EnvelopeStateOK || data["permission_id"] != pending.ID {
		t.Fatalf("resolve_permission: %v", envelope)
	}
	if record, ok, _ := perms.GetPermission(sessionID, pending.ID); !ok || record.Status == pebblestore.PermissionStatusPending {
		t.Fatalf("the request is still pending: %+v", record)
	}

	// Stop: a read key is refused and the run keeps running; a write key
	// stops it.
	if _, _, refused := call([]string{"swarm:read", "sessions:read", "sessions:write"}, map[string]any{"action": "stop", "session_id": sessionID}); !strings.Contains(refused, "needs swarm:write") || len(controller.cancelled) != 0 {
		t.Fatalf("read key stopped the run: %q %v", refused, controller.cancelled)
	}
	envelope, data = must(map[string]any{"action": "stop", "session_id": sessionID, "reason": "done"})
	if envelope["state"] != tool.EnvelopeStateOK || data["status"] != "cancelled" || data["run_id"] != runID || len(controller.cancelled) != 1 || controller.cancelled[0] != runID {
		t.Fatalf("stop: %v (cancelled %v)", envelope, controller.cancelled)
	}
}

// catalogFollowTool calls a catalog tool's next through Swarm Control.
func catalogFollowTool(t *testing.T, h *catalogParityHarness, name string, next map[string]any) map[string]any {
	t.Helper()
	out, refused := h.controlTool(t, catalogParityFullKey, name, next)
	if refused != "" {
		t.Fatalf("following %v: %s", next, refused)
	}
	return catalogEnvelopeCheck(t, name+" next", out)
}

// Purpose: manage_sessions reaches a linked machine's sessions with the
// same answers, through that machine's own handler only when the home's
// linked work rule allows it: off refuses every call, ask refuses only the
// calls that start a run there, and nothing reaches the machine's catalog
// route except a home over the link. Threat: a home driving a machine its
// person turned off, a run started there without the person under ask, or
// the linked route serving an ordinary client as a second API. Boundary:
// Server.CallFleetCatalog and handleCatalogLinked over the real link.
func TestManageSessionsLinkedWorkRule(t *testing.T) {
	h := newCatalogParityHarness(t)
	create := map[string]any{"action": "create", "machine": "box", "title": "Ask", "workspace_path": h.sessionFolders["remote"], "prompt": "Start."}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", create); !strings.Contains(refused, "is ask") {
		t.Fatalf("ask started a run on the linked machine: %q", refused)
	}
	if out, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", map[string]any{"action": "get", "machine": "box", "session_id": "rms-read"}); refused != "" || !strings.Contains(out, `"machine":"box"`) {
		t.Fatalf("ask refused a read: %s %q", out, refused)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", map[string]any{"action": "list", "machine": "box"}); !strings.Contains(refused, "is off") {
		t.Fatalf("off reached the linked machine: %q", refused)
	}
	// The linked catalog route serves homes over the link only.
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, CatalogLinkedPath+"/manage_sessions", strings.NewReader(`{"action":"list"}`)), h.remote.user, h.remote.account)
	w := httptest.NewRecorder()
	h.remote.s.handleCatalogLinked(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a local caller reached the linked catalog route: %d %s", w.Code, w.Body.String())
	}
}

// catalogReadMessages is what following a read_messages chain read: each
// message's content (chunks joined), the order messages first came in, their
// seqs, and every continuation followed.
type catalogReadMessages struct {
	content map[string]string
	order   []string
	seq     map[string]float64
	nexts   []map[string]any
	cut     bool
}

// catalogFollowReadMessages calls read_messages over Swarm Control and follows
// the envelope's next until there is none.
func catalogFollowReadMessages(t *testing.T, h *catalogParityHarness, first map[string]any) catalogReadMessages {
	t.Helper()
	read := catalogReadMessages{content: map[string]string{}, seq: map[string]float64{}}
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", first)
	if refused != "" {
		t.Fatalf("read_messages: %s", refused)
	}
	envelope := catalogEnvelopeCheck(t, "manage_sessions read_messages", out)
	for step := 0; ; step++ {
		if step > 40 {
			t.Fatal("paging did not finish")
		}
		for _, raw := range envelopeDataMap(envelope)["messages"].([]any) {
			m := raw.(map[string]any)
			id, _ := m["id"].(string)
			if _, seen := read.content[id]; !seen {
				read.order = append(read.order, id)
			}
			if m["truncated"] == true {
				read.cut = true
			}
			read.content[id] += m["content"].(string)
			read.seq[id], _ = m["seq"].(float64)
		}
		next, _ := envelope["next"].(map[string]any)
		if next == nil {
			return read
		}
		if next["action"] != "read_messages" || next["session_id"] != first["session_id"] {
			t.Fatalf("next is not a read_messages call: %v", next)
		}
		read.nexts = append(read.nexts, next)
		envelope = catalogFollowTool(t, h, "manage_sessions", next)
	}
}

// Purpose: following read_messages' next reads exactly the window the first
// call asked for, with its role filter, past a message the budget cut: every
// continuation (the page's and each chunk's of a cut message) keeps the role
// and the call's window, and the chain reads each message in the window once
// and in full. A role page returns only that role's messages; a before page
// never reads at or after its before_seq nor older than its limit. Threat: a
// continuation that dropped role or turned a before/tail page into an
// open-ended after read (reading past the asked window), or a cut message
// whose last chunk ended the chain so later messages on its page were never
// returned. Boundary: Runtime.manageSessionsRead and the catalog envelope's
// next over Swarm Control (controlMCPCall.callTool).
func TestManageSessionsReadMessagesContinuationsKeepTheWindow(t *testing.T) {
	h := newCatalogParityHarness(t)
	const session = "ms-page"
	fleetActivityTestSession(t, h.home, session)
	long := strings.Repeat("Assistant detail é. ", 60)
	messages := []pebblestore.MessageSnapshot{
		{ID: "u1", Role: "user", Content: "Question one."},
		{ID: "a1", Role: "assistant", Content: long},
		{ID: "u2", Role: "user", Content: "Question two."},
		{ID: "a2", Role: "assistant", Content: "Answer two."},
		{ID: "u3", Role: "user", Content: "Last question."},
		{ID: "a3", Role: "assistant", Content: "After the window."},
	}
	for _, m := range messages {
		m := m
		if _, err := h.home.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: m.ID, PayloadHash: m.ID, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	all := catalogFollowReadMessages(t, h, map[string]any{"action": "read_messages", "session_id": session, "limit": float64(30), "max_chars": float64(20000)})
	for _, m := range messages {
		if all.content[m.ID] != m.Content {
			t.Fatalf("the whole read lost %s: %v", m.ID, all.order)
		}
	}

	// Role: only assistant messages, the cut one whole, the ones after it
	// on the page read; every continuation keeps role.
	byRole := catalogFollowReadMessages(t, h, map[string]any{"action": "read_messages", "session_id": session, "role": "assistant", "limit": float64(10), "max_chars": float64(300)})
	if !byRole.cut || len(byRole.nexts) == 0 {
		t.Fatalf("the role page was not cut: %v", byRole.order)
	}
	if strings.Join(byRole.order, ",") != "a1,a2,a3" || byRole.content["a1"] != long || byRole.content["a2"] != "Answer two." || byRole.content["a3"] != "After the window." {
		t.Fatalf("role read: %v", byRole.order)
	}
	for _, next := range byRole.nexts {
		if next["role"] != "assistant" {
			t.Fatalf("a continuation dropped role: %v", next)
		}
	}

	// Before: the window is the three messages before u3 (a1, u2, a2); the
	// chain reads exactly them, a1 whole, and nothing at or after u3 or
	// older than the window.
	beforeSeq := all.seq["u3"]
	before := catalogFollowReadMessages(t, h, map[string]any{"action": "read_messages", "session_id": session, "mode": "before", "before_seq": beforeSeq, "limit": float64(3), "max_chars": float64(300)})
	if !before.cut || len(before.nexts) == 0 {
		t.Fatalf("the before page was not cut: %v", before.order)
	}
	if strings.Join(before.order, ",") != "a1,u2,a2" || before.content["a1"] != long || before.content["u2"] != "Question two." || before.content["a2"] != "Answer two." {
		t.Fatalf("before read left its window: %v", before.order)
	}
	for _, next := range before.nexts {
		if next["mode"] != "before" || next["before_seq"] != beforeSeq {
			t.Fatalf("a continuation of a before page left its window: %v", next)
		}
	}
}

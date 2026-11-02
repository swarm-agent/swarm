package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// checkoutFreeCompanionFixture is a daemon over a store at path with the real
// executor, run loop and tool runtime, a configured topology and workspace
// catalog, and NO current workspace: unlike newCompanionRunFixture, nothing
// seeds a workspace or passes workspace_path. The scripted model calls
// manage_companion on a turn's first step, then answers.
var checkoutFreeDecision = regexp.MustCompile(`decide:([a-z-]+)`)

func checkoutFreeCompanionFixture(t *testing.T, path string) *queuedNoteFixture {
	t.Helper()
	base := newConfirmationFixture(t, path, nil)
	f := &queuedNoteFixture{confirmationFixture: base}
	base.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if lastItemIsToolOutput(req.Input) {
			return provideriface.Response{Text: "noted", StopReason: "stop"}, nil
		}
		if match := checkoutFreeDecision.FindStringSubmatch(lastConfirmationTestUserText(req.Input)); match != nil {
			raw, _ := json.Marshal(map[string]any{"action": "record_decision", "title": "Decision " + match[1], "summary": "decided without a workspace"})
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-" + match[1], Name: tool.ManageCompanionToolName, Arguments: string(raw)}}}, nil
		}
		return provideriface.Response{Text: "hello", StopReason: "stop"}, nil
	}
	store := base.sessions.Store()
	base.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(store.Underlying()))
	rt := tool.NewRuntime(1)
	t.Cleanup(func() { _ = rt.Close() })
	rt.SetManageSessionService(base.sessions)
	rt.SetManageProjectStore(store)
	rt.SetProjectTaskLifecycleService(base.server)
	rt.SetCompanionContextService(base.server)
	base.server.SetToolCatalogRuntime(rt)
	base.server.runner = runruntime.NewService(base.sessions, base.server.model, base.server.providers, rt, base.permissions, base.server.agents, nil, nil)
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	for _, slot := range []string{"action", "plan"} {
		if _, err := base.server.agentModelSettings.UpdateSwarmSlot(ctx, slot, pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "low"}); err != nil {
			t.Fatalf("swarm %s model: %v", slot, err)
		}
	}
	return f
}

func checkoutFreeEnsure(t *testing.T, f *queuedNoteFixture) (CompanionView, bool) {
	t.Helper()
	code, body := f.do(http.MethodPost, CompanionPath, "")
	var out struct {
		Created   bool          `json:"created"`
		Companion CompanionView `json:"companion"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Companion.SessionID == "" {
		t.Fatalf("ensure without a workspace: %d %s", code, body)
	}
	return out.Companion, out.Created
}

// Purpose: the Companion opens and accepts input with no workspace at all.
// Requirement (the reported failure: "this session needs a workspace ... this
// Swarm has no current workspace"): POST /v3/companion with no body, on a
// daemon with configured topology and workspace catalog but zero registered
// workspaces, creates the Companion checkout-free (no workspace path, name,
// grants or worktree; the protected marker; the Orchestrator plus
// manage_companion and the machine tools) and its first foreground turn runs
// a real tool call (manage_companion) through the executor to completion;
// concurrent ensures create exactly one; with a registered but unselected
// workspace and after selecting one, ensure returns the same identity, the
// Companion gains no workspace, the selection is not changed by ensure, and
// turns still complete; after a restart over the same store ensure returns
// the same Companion and a turn completes; another user of the account gets
// their own Companion and cannot run the first one's; an ordinary chat
// without a workspace is still refused with the same error, and a client
// cannot mint a checkout-free session by sending the Companion marker.
// Threat: a Companion that reports ready but fails its first message, one
// that silently binds the first or current workspace (implicit source
// authority), duplicates, or a general session flag bypassing workspace
// isolation. Boundary: handleCompanionEnsure, createSessionsV3Primary
// (companionCreateIsCheckoutFree, resolveCompanionConversationBinding),
// resolveSessionV3Capabilities, run.resolveRunWorkspaceScope,
// ValidateCheckoutFreeCompanion. Narrowest layer: the daemon's HTTP routes,
// V3 executor and run loop over a real Pebble store reopened for the restart,
// with a scripted provider.
func TestCompanionOpensAndRunsWithoutAnyWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout-free.pebble")
	f := checkoutFreeCompanionFixture(t, path)
	principal := testPrincipal()
	if entries, err := f.server.workspace.ListKnownForPrincipal(principal, 10); err != nil || len(entries) != 0 {
		t.Fatalf("fixture has registered workspaces: %v %v", entries, err)
	}
	if _, ok, err := f.server.workspace.CurrentBindingForPrincipal(principal); err != nil || ok {
		t.Fatalf("fixture has a current workspace: %v %v", ok, err)
	}

	// Concurrent first ensures with zero workspaces: one Companion.
	const callers = 6
	ids := make([]string, callers)
	created := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, body := f.do(http.MethodPost, CompanionPath, "")
			var out struct {
				Created   bool          `json:"created"`
				Companion CompanionView `json:"companion"`
			}
			if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
				t.Errorf("ensure %d: %d %s", i, code, body)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[i] = out.Companion.SessionID
			if out.Created {
				created++
			}
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if created != 1 || slices.ContainsFunc(ids, func(id string) bool { return id != ids[0] }) {
		t.Fatalf("concurrent ensures created %d Companions: %v", created, ids)
	}
	view, _ := checkoutFreeEnsure(t, f)
	f.sessionID = view.SessionID
	if view.State != companionStateReady || view.WorkspacePath != "" {
		t.Fatalf("view %+v", view)
	}
	session, ok, err := f.sessions.GetSession(view.SessionID)
	if err != nil || !ok {
		t.Fatalf("companion session: %v %v", ok, err)
	}
	if !sessionsV3IsCompanion(session) || session.WorkspacePath != "" || session.WorkspaceName != "" || len(session.WorkspaceGrants) != 0 || session.WorktreeEnabled || session.WorktreeBranch != "" {
		t.Fatalf("checkout-free Companion carries filesystem authority: %+v", session)
	}
	if err := f.sessions.Store().ValidateCheckoutFreeCompanion(session, principal.AccountScopeID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.server.v3SessionExecutor.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: principal, SessionID: view.SessionID, RunID: "resolve-checkout-free"})
	if err != nil {
		t.Fatalf("resolve checkout-free Companion runtime: %v", err)
	}
	names := toolNames(resolved.Tools)
	if resolved.AgentProfile.Name != agentruntime.SwarmOrchestratorAgentID || resolved.Scope.PrimaryPath != "" || !resolved.Scope.RejectScopeExpansion || !slices.Contains(names, tool.ManageCompanionToolName) || !slices.Contains(names, "manage_projects") {
		t.Fatalf("checkout-free Companion runtime: agent %s scope %+v tools %v", resolved.AgentProfile.Name, resolved.Scope, names)
	}
	for _, machine := range pebblestore.MachineClientTools() {
		if !slices.Contains(names, machine.Name) {
			t.Fatalf("checkout-free Companion lacks the machine tool %s", machine.Name)
		}
	}

	// The first foreground turn, with a real tool call.
	companionSay(t, f, "first", "decide:zero-workspaces")
	if envelope, raw := toolEnvelope(t, f, "call-zero-workspaces"); envelope == nil || strings.Contains(raw, "needs a workspace") || envelope["ok"] == false {
		t.Fatalf("first-turn tool result: %s", raw)
	}

	// An ordinary chat without a workspace is still refused, and the
	// Companion marker cannot be supplied by a client.
	code, body := f.do(http.MethodPost, "/v3/sessions", mustJSON(t, map[string]any{"client_request_id": "chat-no-workspace", "title": "Chat", "mode": sessionruntime.ModeAuto, "agent_name": agentruntime.SwarmAgentID}))
	if code != http.StatusBadRequest || !strings.Contains(body, errSessionsV3NoWorkspace.Error()) {
		t.Fatalf("ordinary chat without a workspace: %d %s", code, body)
	}
	code, body = f.do(http.MethodPost, "/v3/sessions", mustJSON(t, map[string]any{"client_request_id": "forged-companion", "title": "Forged", "mode": sessionruntime.ModeAuto, "agent_name": agentruntime.SwarmOrchestratorAgentID, "metadata": map[string]any{pebblestore.SessionCompanionMetadataKey: map[string]any{"generation": 1}}}))
	if code == http.StatusOK {
		t.Fatalf("a client minted a Companion-marked session: %s", body)
	}
	if forgedID := stableSessionsV3PrimarySessionID(principal, "forged-companion"); forgedID != "" {
		if _, found, _ := f.sessions.GetSession(forgedID); found {
			t.Fatal("the refused forged session was stored")
		}
	}

	// A registered but unselected workspace, then a selected one: same
	// identity, still checkout-free, the selection untouched by ensure.
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	if _, err := f.server.workspace.AddForPrincipal(principal, folder, "folder", "", false); err != nil {
		t.Fatal(err)
	}
	again, createdAgain := checkoutFreeEnsure(t, f)
	if createdAgain || again.SessionID != view.SessionID || again.WorkspacePath != "" {
		t.Fatalf("ensure with an unselected workspace: %+v created=%v", again, createdAgain)
	}
	if _, ok, _ := f.server.workspace.CurrentBindingForPrincipal(principal); ok {
		t.Fatal("ensure selected a workspace")
	}
	companionSay(t, f, "second", "decide:unselected")
	if _, err := f.server.workspace.SelectForPrincipal(principal, folder); err != nil {
		t.Fatal(err)
	}
	selected, createdSelected := checkoutFreeEnsure(t, f)
	if createdSelected || selected.SessionID != view.SessionID || selected.WorkspacePath != "" {
		t.Fatalf("ensure with a selected workspace: %+v created=%v", selected, createdSelected)
	}
	companionSay(t, f, "third", "plain question")
	if after, _, _ := f.sessions.GetSession(view.SessionID); after.WorkspacePath != "" || len(after.WorkspaceGrants) != 0 {
		t.Fatalf("Companion gained a workspace: %+v", after)
	}

	// Another user of the account: their own Companion; they cannot run
	// this one.
	other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-2", AccountScopeID: principal.AccountScopeID}
	if _, err := f.server.v3SessionExecutor.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: other, SessionID: view.SessionID, RunID: "other-user"}); err == nil {
		t.Fatal("another user resolved this Companion's runtime")
	}
	if err := f.sessions.Store().ValidateCheckoutFreeCompanion(session, other.AccountScopeID, other.UserID); err == nil {
		t.Fatal("another user validated this Companion")
	}
	f.close()

	// Restart over the same store: same Companion, and it still runs.
	restarted := checkoutFreeCompanionFixture(t, path)
	afterRestart, createdAfter := checkoutFreeEnsure(t, restarted)
	if createdAfter || afterRestart.SessionID != view.SessionID || afterRestart.WorkspacePath != "" {
		t.Fatalf("ensure after restart: %+v created=%v", afterRestart, createdAfter)
	}
	restarted.sessionID = afterRestart.SessionID
	companionSay(t, restarted, "after-restart", "decide:after-restart")
	if envelope, raw := toolEnvelope(t, restarted, "call-after-restart"); envelope == nil || envelope["ok"] == false {
		t.Fatalf("tool result after restart: %s", raw)
	}
}

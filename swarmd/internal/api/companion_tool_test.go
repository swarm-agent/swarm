package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// companionParityMessage is the home owner's person message in the seeded
// Companion: the source manage_companion's decisions cite by default.
const companionParityMessage = "Let's keep the dashboard on the blue theme for the launch"

// seedCompanion gives the parity harness's home owner a current Companion
// (identity record plus marked session, as handleCompanionEnsure makes it)
// with one person message and records for get and forget, and registers the
// context bank service on the catalog runtime, as the daemon does.
func (h *catalogParityHarness) seedCompanion(t *testing.T) {
	t.Helper()
	h.runtime.SetCompanionContextService(h.home.s)
	store := h.home.s.sessions.Store()
	record, _, err := store.EnsureCompanion(h.home.account, h.home.user, 1)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{pebblestore.SessionCompanionMetadataKey: companionCreateBinding{Generation: record.Generation}, "agent_name": agentruntime.SwarmOrchestratorAgentID, "resolved_agent_name": agentruntime.SwarmOrchestratorAgentID}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: record.SessionID, AccountScopeID: h.home.account, UserID: h.home.user, Mode: "auto", Metadata: metadata}, h.home.user, h.home.account); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: record.SessionID, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationAppendMessage,
		ClientRequestID: "companion-parity-message", PayloadHash: "companion-parity-message",
		Message: &pebblestore.MessageSnapshot{Role: "user", Content: companionParityMessage},
	}); err != nil {
		t.Fatal(err)
	}
	h.companionSession = record.SessionID
	messages, err := store.ListV3SessionMessageTail(record.SessionID, 5)
	if err != nil || len(messages) == 0 {
		t.Fatalf("companion message: %v %v", messages, err)
	}
	source := pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceMessage, SessionID: record.SessionID, MessageSeq: messages[len(messages)-1].GlobalSeq}
	for _, id := range []string{"seed-decision", "forget-a", "forget-b"} {
		if _, _, err := store.PutCompanionContext(pebblestore.CompanionContextRecord{ID: id, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.CompanionContextKindDecision, Title: "Blue theme for the dashboard launch", Source: source}, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
}

// manageCompanionParityCases: every manage_companion action through the
// Companion's native call and Swarm Control with the same inputs. Changes run
// on twin records. The actions reach this machine only, so there is no linked
// case; the machine= refusal (the same reason on both surfaces) is compared
// in TestManageCompanionNativeMCPAndRouteAgree.
func manageCompanionParityCases() map[string][]catalogParityCase {
	same := func(compare catalogParityCompare, args map[string]any) []catalogParityCase {
		return []catalogParityCase{{orchestrator: args, control: args, compare: compare}}
	}
	twin := func(orch, control map[string]any, ids map[string]string) []catalogParityCase {
		return []catalogParityCase{{orchestrator: orch, control: control, compare: parityShape, twin: ids}}
	}
	return map[string][]catalogParityCase{
		"record_decision": twin(parityArgs("action", "record_decision", "id", "cdec-a", "title", "Blue theme", "summary", "Keep the blue theme"), parityArgs("action", "record_decision", "id", "cdec-b", "title", "Blue theme", "summary", "Keep the blue theme"), map[string]string{"cdec-b": "cdec-a"}),
		"record_work":     twin(parityArgs("action", "record_work", "id", "cwork-a", "title", "Watched work", "session_id", "hs-w1"), parityArgs("action", "record_work", "id", "cwork-b", "title", "Watched work", "session_id", "hs-w1"), map[string]string{"cwork-b": "cwork-a"}),
		// query and kind are a choice: search needs one of them.
		"search": same(parityExact, parityArgs("action", "search", "query", "blue theme dashboard", "kind", "decision")),
		"get":    same(parityExact, parityArgs("action", "get", "id", "seed-decision")),
		"list":   same(parityExact, parityArgs("action", "list", "limit", float64(3))),
		"forget": twin(parityArgs("action", "forget", "id", "forget-a", "expected_revision", float64(1)), parityArgs("action", "forget", "id", "forget-b", "expected_revision", float64(1)), map[string]string{"forget-b": "forget-a"}),
	}
}

// companionNative runs a manage_companion call in session as the
// Orchestrator, with principal p, through the catalog runtime (the path the
// run takes for a provider's tool call).
func (h *catalogParityHarness) companionNative(t *testing.T, session string, p identity.Principal, args map[string]any) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), agentruntime.SwarmOrchestratorAgentID), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(args)
	return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: p}, tool.Call{Name: tool.ManageCompanionToolName, Arguments: string(raw)})
}

// companionRoute is the home owner's GET of a context bank route on the
// home daemon's own API handler.
func (h *catalogParityHarness) companionRoute(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, path, nil), h.home.user, h.home.account)
	rec := httptest.NewRecorder()
	h.home.s.internalRouteHandler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// envelopeDataOf decodes a catalog answer's data object.
func envelopeDataOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	data, err := tool.EnvelopeData(raw)
	if err != nil {
		t.Fatalf("envelope: %v (%s)", err, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("envelope data: %v (%s)", err, data)
	}
	return out
}

// Purpose: the Companion's context bank is one capability on three surfaces.
// Requirement: manage_companion called natively by the Companion, through
// Swarm Control (MCP) and the HTTP route /v3/companion/context run one service
// and give the same record and source for the same input; a native call from
// any session but the account user's current Companion (a project or other
// Orchestrator conversation, the Companion with another account's principal)
// is refused and writes nothing; machine= is refused with the same stable
// reason on both tool surfaces; a stale revision, a workspace outside the
// Companion's scope and a key without write scope are refused; account memory
// never changes; every tool and action the Companion's contract names exists
// in the catalog; every JSON field the service answers is typed in the SDK.
// Threat: a second rule set or store behind one surface, another agent or
// conversation writing the person's Companion context, a linked machine
// silently writing a different Companion, contract text naming tools the
// Companion does not have, or an SDK missing an answer field. Boundary:
// Runtime.executeManageCompanion, Runtime.ExecuteControlCatalog,
// Server.ManageCompanionContext and handleCompanionContext over a real store.
// Narrowest layer: the catalog runtime and HTTP handler of one daemon (the
// provider path is covered by TestCompanionKeepsItsContextBankThroughItsOwnTool).
func TestManageCompanionNativeMCPAndRouteAgree(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	memoryBefore, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(h.home.account)
	if err != nil {
		t.Fatal(err)
	}

	// One record through the three surfaces.
	nativeOut, err := h.companionNative(t, h.companionSession, h.p, parityArgs("action", "get", "id", "seed-decision"))
	if err != nil {
		t.Fatal(err)
	}
	controlOut, controlErr := h.controlTool(t, catalogParityFullKey, tool.ManageCompanionToolName, parityArgs("action", "get", "id", "seed-decision"))
	if controlErr != "" {
		t.Fatal(controlErr)
	}
	code, route := h.companionRoute(t, CompanionContextPath+"?id=seed-decision")
	if code != http.StatusOK {
		t.Fatalf("route get: %d %v", code, route)
	}
	native, control := envelopeDataOf(t, nativeOut), envelopeDataOf(t, controlOut)
	a, _ := json.Marshal(native["record"])
	b, _ := json.Marshal(control["record"])
	c, _ := json.Marshal(route["record"])
	if string(a) != string(b) || string(a) != string(c) || native["source_text"] != companionParityMessage || control["source_text"] != native["source_text"] || route["source_text"] != native["source_text"] {
		t.Fatalf("surfaces disagree:\nnative  %s %v\ncontrol %s %v\nroute   %s %v", a, native["source_text"], b, control["source_text"], c, route["source_text"])
	}
	var envelope struct {
		Untrusted bool `json:"untrusted_content"`
	}
	if json.Unmarshal([]byte(nativeOut), &envelope) != nil || !envelope.Untrusted {
		t.Fatalf("answer is not marked untrusted: %s", nativeOut)
	}

	// A decision recorded natively cites the Companion's latest person
	// message when none is named; MCP's default cites the same message.
	out, err := h.companionNative(t, h.companionSession, h.p, parityArgs("action", "record_decision", "title", "Launch on the blue theme"))
	if err != nil {
		t.Fatal(err)
	}
	recorded, _ := envelopeDataOf(t, out)["record"].(map[string]any)
	source, _ := recorded["source"].(map[string]any)
	if source["session_id"] != h.companionSession || source["kind"] != "message" || source["message_seq"] == nil {
		t.Fatalf("decision source: %v", recorded)
	}

	// Refusals; none writes.
	before, _, _, _ := store.ListCompanionContext(h.home.account, h.home.user, "", 100)
	for _, refusal := range []struct {
		name    string
		session string
		p       identity.Principal
		args    map[string]any
		want    string
	}{
		{"another conversation", "home-orch", h.p, parityArgs("action", "record_decision", "title", "Hijack", "session_id", h.companionSession, "message_seq", float64(1)), "not your Companion"},
		{"a project Orchestrator", "home-project-orch", h.p, parityArgs("action", "search", "query", "blue"), "not your Companion"},
		{"another account", h.companionSession, identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.home.user, AccountScopeID: "other-account"}, parityArgs("action", "search", "query", "blue"), "not your Companion"},
		{"no conversation", "", h.p, parityArgs("action", "search", "query", "blue"), "Companion's own tool"},
		{"stale revision", h.companionSession, h.p, parityArgs("action", "forget", "id", "seed-decision", "expected_revision", float64(9)), "changed; reload"},
		{"workspace outside scope", h.companionSession, h.p, parityArgs("action", "record_decision", "title", "Elsewhere", "workspace_path", filepath.Join(t.TempDir(), "elsewhere")), "outside the Companion's current workspace scope"},
		{"machine", h.companionSession, h.p, parityArgs("action", "get", "id", "seed-decision", "machine", "box"), tool.ManageCompanionMachineRefusal},
	} {
		if _, err := h.companionNative(t, refusal.session, refusal.p, refusal.args); err == nil || !strings.Contains(err.Error(), refusal.want) {
			t.Fatalf("%s: %v, want %q", refusal.name, err, refusal.want)
		}
	}
	if _, errText := h.controlTool(t, catalogParityFullKey, tool.ManageCompanionToolName, parityArgs("action", "search", "query", "blue", "machine", "box")); !strings.Contains(errText, tool.ManageCompanionMachineRefusal) {
		t.Fatalf("MCP machine refusal differs: %s", errText)
	}
	if _, errText := h.controlTool(t, []string{"swarm:read", "swarm:write", "sessions:read"}, tool.ManageCompanionToolName, parityArgs("action", "record_decision", "title", "No write scope")); errText == "" {
		t.Fatal("a key without sessions:write recorded a decision")
	}
	after, _, _, _ := store.ListCompanionContext(h.home.account, h.home.user, "", 100)
	if len(after) != len(before) {
		t.Fatalf("a refused call wrote: %d -> %d records", len(before), len(after))
	}
	if memoryAfter, err := pebblestore.NewMemoryStore(store.Underlying()).GetForAccount(h.home.account); err != nil || memoryAfter.Revision != memoryBefore.Revision {
		t.Fatalf("account memory changed: %v %v", memoryAfter.Revision, err)
	}

	// The contract names only tools and actions that exist.
	contract := companionInstructions(pebblestore.SessionSnapshot{Metadata: map[string]any{pebblestore.SessionCompanionMetadataKey: true}})
	catalog, _ := tool.CatalogToolNamed(tool.ManageCompanionToolName)
	for _, action := range []string{"record_decision", "record_work", "search", "get", "forget"} {
		if !strings.Contains(contract, action) {
			t.Fatalf("contract does not name manage_companion %s", action)
		}
		if _, ok := catalog.Action(action); !ok {
			t.Fatalf("contract names manage_companion %s, which the catalog does not declare", action)
		}
	}
	projects, _ := tool.CatalogToolNamed("manage_projects")
	if _, ok := projects.Action("propose_task"); !ok || !strings.Contains(contract, "manage_projects propose_task") {
		t.Fatal("contract's delegation action is not a manage_projects action")
	}
	for _, name := range regexp.MustCompile(`\bmanage_[a-z_]+`).FindAllString(contract, -1) {
		if _, ok := tool.CatalogToolNamed(name); !ok {
			t.Fatalf("contract names %s, which is not a catalog tool", name)
		}
	}

	// Every field the service answers is typed in the SDK.
	sdkRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "sdk", "src", "companion.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, answer := range []string{nativeOut, out} {
		for key := range envelopeDataOf(t, answer) {
			fields[key] = true
		}
	}
	forgot, err := h.companionNative(t, h.companionSession, h.p, parityArgs("action", "forget", "id", "seed-decision", "expected_revision", float64(1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{forgot} {
		for key := range envelopeDataOf(t, answer) {
			fields[key] = true
		}
	}
	for _, query := range []string{"?q=blue", "?limit=2"} {
		_, answer := h.companionRoute(t, CompanionContextPath+query)
		for key := range answer {
			fields[key] = true
		}
	}
	for field := range fields {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\??:`).MatchString(string(sdkRaw)) {
			t.Errorf("SDK companion.ts does not type the context bank answer field %q", field)
		}
	}
}

// companionToolFixture is a Companion on the real executor and run loop whose
// tool runtime has the services the daemon registers (sessions, project
// tasks, the context bank), with a scripted model.
func companionToolFixture(t *testing.T, model func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response) (*queuedNoteFixture, *tool.Runtime) {
	t.Helper()
	f := newCompanionRunFixture(t, model)
	rt := tool.NewRuntime(1)
	t.Cleanup(func() { _ = rt.Close() })
	rt.SetManageSessionService(f.sessions)
	rt.SetManageProjectStore(f.sessions.Store())
	rt.SetProjectTaskLifecycleService(f.server)
	rt.SetCompanionContextService(f.server)
	f.server.SetToolCatalogRuntime(rt)
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, rt, f.permissions, f.server.agents, nil, nil)
	return f, rt
}

func toolNames(definitions []provideriface.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	return names
}

func toolDefinition(definitions []provideriface.ToolDefinition, name string) (provideriface.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return provideriface.ToolDefinition{}, false
}

// Purpose: the Companion really has the Orchestrator's tools. Requirement: the
// runtime the V3 executor resolves for a Companion created through POST
// /v3/companion (resolveSessionV3Runtime: agent profile, tool contract,
// provider tool definitions, instructions) runs as the primary Orchestrator,
// so manage_projects is the Orchestrator's (propose_task, wait_tasks,
// message_task, stop_task, cancel_watch, list_machines; the invoker gives
// TaskHistoryOnly only to non-Orchestrators), with the machine tools and
// catalog tools, plus manage_companion; a project Orchestrator resolves the
// same tools without manage_companion; an ordinary Swarm chat resolves
// neither manage_companion nor the Orchestrator-only tools and keeps the Swarm
// agent; the Companion's model is the account's Swarm Action assignment, the
// one a conversation gets, even when the Plan slot differs. Threat: a
// Companion whose prompt promises task delegation and watches its runtime does
// not give (the reviewed failure), the Companion tool leaking to project
// Orchestrators or chats, or the switch to the Orchestrator silently moving
// the Companion onto the Plan model. Boundary: handleCompanionEnsure,
// createSessionsV3Primary (companionModelProfile),
// resolveSessionV3CurrentAgentToolContract (withCompanionContextTool),
// run.resolveAgentToolStates, resolveSessionV3ProviderTools,
// resolveSessionV3EffectivePreference. Narrowest layer: the executor's own
// resolution over the real run service, agent registry and store.
func TestCompanionResolvesTheOrchestratorRuntime(t *testing.T) {
	f, _ := companionToolFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "ok", StopReason: "stop"}
	})
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	if _, err := f.server.agentModelSettings.UpdateSwarmSlot(ctx, "plan", pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "plan-model", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	// A Companion created now (the fixture's was created with equal slots).
	code, body := f.do(http.MethodDelete, CompanionPath, mustJSON(t, map[string]any{"session_id": f.sessionID, "confirm": true}))
	if code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	workspace := t.TempDir()
	seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	code, body = f.do(http.MethodPost, CompanionPath, mustJSON(t, map[string]any{"workspace_path": workspace}))
	var ensured struct {
		Companion CompanionView `json:"companion"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &ensured) != nil {
		t.Fatalf("ensure: %d %s", code, body)
	}
	companionID := ensured.Companion.SessionID

	// A project Orchestrator conversation and an ordinary Swarm chat.
	account := testPrincipal().AccountScopeID
	if err := f.sessions.Store().PutProject(account, &pebblestore.ProjectRecord{ID: "resolve-project", AccountID: account, Name: "Resolve"}); err != nil {
		t.Fatal(err)
	}
	code, body = f.do(http.MethodPost, "/v3/sessions", mustJSON(t, map[string]any{"client_request_id": "resolve-project-orch", "project_id": "resolve-project", "title": "Project"}))
	var project struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &project) != nil {
		t.Fatalf("project conversation: %d %s", code, body)
	}
	chatWorkspace := t.TempDir()
	binding := seedSessionsV3PrimaryAuthority(t, f.server, chatWorkspace)
	code, body = f.do(http.MethodPost, "/v3/sessions", mustJSON(t, map[string]any{"client_request_id": "resolve-chat", "workspace_path": chatWorkspace, "workspace_name": filepath.Base(chatWorkspace), "swarm_id": "host-swarm-id", "workspace_binding_id": binding, "target_kind": "host", "target_relationship": "self", "title": "Chat", "mode": sessionruntime.ModeAuto, "agent_name": agentruntime.SwarmAgentID}))
	var chat struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &chat) != nil {
		t.Fatalf("chat: %d %s", code, body)
	}

	executor := f.server.v3SessionExecutor
	resolve := func(sessionID string) sessionV3ResolvedRuntime {
		t.Helper()
		resolved, err := executor.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sessionID, RunID: "resolve-" + sessionID})
		if err != nil {
			t.Fatalf("resolve %s: %v", sessionID, err)
		}
		return resolved
	}
	companion, projectOrch, swarmChat := resolve(companionID), resolve(project.Session.ID), resolve(chat.Session.ID)

	if companion.AgentProfile.Name != agentruntime.SwarmOrchestratorAgentID || companion.AgentProfile.Mode != agentruntime.ModePrimary || !agentruntime.IsSwarmOrchestratorAgentName(companion.AgentProfile.Name) {
		t.Fatalf("Companion agent = %s/%s, want the primary Orchestrator (so its manage_projects is never TaskHistoryOnly)", companion.AgentProfile.Name, companion.AgentProfile.Mode)
	}
	names := toolNames(companion.Tools)
	for _, want := range []string{"manage_projects", tool.ManageCompanionToolName, tool.ManageFleetToolName, tool.ManageMediaToolName, "manage-sessions", "manage_workspace", "manage_memory", "manage_custom_agents"} {
		if !slices.Contains(names, want) {
			t.Fatalf("Companion tools lack %s: %v", want, names)
		}
	}
	for _, machine := range pebblestore.MachineClientTools() {
		if !slices.Contains(names, machine.Name) {
			t.Fatalf("Companion tools lack the machine tool %s: %v", machine.Name, names)
		}
	}
	projectsTool, _ := toolDefinition(companion.Tools, "manage_projects")
	actions, _ := json.Marshal(projectsTool.Parameters["properties"].(map[string]any)["action"])
	for _, action := range []string{"propose_task", "wait_tasks", "message_task", "stop_task", "cancel_watch", "list_machines", "get_task"} {
		if !strings.Contains(string(actions), `"`+action+`"`) {
			t.Fatalf("Companion manage_projects lacks %s: %s", action, actions)
		}
	}
	projectNames := toolNames(projectOrch.Tools)
	if slices.Contains(projectNames, tool.ManageCompanionToolName) {
		t.Fatal("a project Orchestrator got the Companion's context bank tool")
	}
	withoutCompanion := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == tool.ManageCompanionToolName })
	if strings.Join(withoutCompanion, ",") != strings.Join(projectNames, ",") {
		t.Fatalf("Companion tools are not the Orchestrator's plus manage_companion:\ncompanion %v\nproject   %v", names, projectNames)
	}
	chatNames := toolNames(swarmChat.Tools)
	if swarmChat.AgentProfile.Name != agentruntime.SwarmAgentID || slices.Contains(chatNames, tool.ManageCompanionToolName) || slices.Contains(chatNames, "manage_custom_agents") || slices.Contains(chatNames, tool.ManageFleetToolName) {
		t.Fatalf("an ordinary Swarm chat changed: agent %s tools %v", swarmChat.AgentProfile.Name, chatNames)
	}
	if !strings.Contains(companion.Instructions, "Companion operating contract") || strings.Contains(projectOrch.Instructions, "Companion operating contract") || strings.Contains(swarmChat.Instructions, "Companion operating contract") {
		t.Fatal("the Companion contract is not the Companion's alone")
	}
	if companion.Preference.Model != "test-model" || swarmChat.Preference.Model != "test-model" {
		t.Fatalf("Companion model = %s (chat %s), want the Swarm Action assignment test-model", companion.Preference.Model, swarmChat.Preference.Model)
	}
}

// Purpose: a Companion created before it ran as the Orchestrator moves onto it
// without losing anything. Requirement: POST /v3/companion on a Companion
// persisted with agent swarm moves it to the Orchestrator through the
// canonical V3 metadata mutation, keeping its session, history and model
// (the Swarm Action assignment), once (a second ensure records no further
// event); chat controls still cannot switch its agent. Threat: existing
// Companions stuck without the tools their prompt promises, a destructive
// re-creation that drops history, or a model change on upgrade. Boundary:
// companionRunsAsOrchestrator via handleCompanionEnsure,
// applySessionV3PrimaryMutation. Narrowest layer: the HTTP route over the real
// session service and store, with the pre-change creation path called
// directly.
func TestCompanionCreatedAsSwarmMovesOntoTheOrchestratorOnEnsure(t *testing.T) {
	f, _ := companionToolFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "ok", StopReason: "stop"}
	})
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	if _, err := f.server.agentModelSettings.UpdateSwarmSlot(ctx, "plan", pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "plan-model", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	if code, body := f.do(http.MethodDelete, CompanionPath, mustJSON(t, map[string]any{"session_id": f.sessionID, "confirm": true})); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	principal := testPrincipal()
	record, _, err := f.sessions.Store().EnsureCompanion(principal.AccountScopeID, principal.UserID, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	// The pre-change creation: the Companion as the Swarm agent.
	req := withTestPrincipal(httptest.NewRequest(http.MethodPost, CompanionPath, nil))
	capture := newCompanionCaptureWriter()
	f.server.createSessionsV3Primary(capture, req.WithContext(context.WithValue(req.Context(), companionCreateContextKey{}, companionCreateBinding{Generation: record.Generation})), principal, sessionsV3CreateRequest{
		SessionID: record.SessionID, ClientRequestID: "companion:" + record.SessionID, AgentName: agentruntime.SwarmAgentID, Title: companionTitle, WorkspacePath: workspace,
	})
	if capture.status != http.StatusOK {
		t.Fatalf("legacy create: %d %s", capture.status, capture.body.String())
	}
	if _, err := f.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: record.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: "legacy-history", PayloadHash: "legacy-history", Message: &pebblestore.MessageSnapshot{Role: "user", Content: "history from before the upgrade"}}); err != nil {
		t.Fatal(err)
	}
	legacy, _, _ := f.sessions.GetSession(record.SessionID)
	if legacy.Metadata["agent_name"] != agentruntime.SwarmAgentID {
		t.Fatalf("legacy Companion agent = %v", legacy.Metadata["agent_name"])
	}
	legacyModel := legacy.Preference.Model

	for i := 0; i < 2; i++ {
		code, body := f.do(http.MethodPost, CompanionPath, "")
		if code != http.StatusOK || !strings.Contains(body, record.SessionID) || strings.Contains(body, `"created":true`) {
			t.Fatalf("ensure %d: %d %s", i, code, body)
		}
	}
	moved, _, _ := f.sessions.GetSession(record.SessionID)
	if moved.Metadata["agent_name"] != agentruntime.SwarmOrchestratorAgentID || moved.Metadata["resolved_agent_name"] != agentruntime.SwarmOrchestratorAgentID || !sessionsV3IsCompanion(moved) || moved.Metadata["navigation_hidden"] != true {
		t.Fatalf("Companion not moved onto the Orchestrator: %v", moved.Metadata)
	}
	messages, _ := f.sessions.ListSessionMessages(record.SessionID, 0, 50)
	if len(messages) == 0 || messages[len(messages)-1].Content != "history from before the upgrade" {
		t.Fatalf("history lost: %v", messages)
	}
	events, err := f.sessions.Store().ListV3SessionEvents(record.SessionID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	agentEvents := 0
	for _, event := range events {
		if event.EventType == "session.agent.updated" {
			agentEvents++
		}
	}
	if agentEvents != 1 {
		t.Fatalf("agent move recorded %d times, want once", agentEvents)
	}
	resolved, err := f.server.v3SessionExecutor.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: principal, SessionID: record.SessionID, RunID: "moved"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AgentProfile.Name != agentruntime.SwarmOrchestratorAgentID || resolved.Preference.Model != legacyModel || legacyModel != "test-model" || !slices.Contains(toolNames(resolved.Tools), tool.ManageCompanionToolName) {
		t.Fatalf("moved Companion resolves %s on %s (was %s), tools %v", resolved.AgentProfile.Name, resolved.Preference.Model, legacyModel, toolNames(resolved.Tools))
	}
	if code, body := f.do(http.MethodPost, "/v3/sessions/"+record.SessionID+"/agent", mustJSON(t, map[string]any{"agent_name": agentruntime.SwarmAgentID, "client_request_id": "switch-back"})); code != http.StatusConflict {
		t.Fatalf("agent switch back: %d %s", code, body)
	}
}

// companionContextScript is the scripted model of the context bank run: the
// Companion's turns call manage_companion through the real run loop; the
// model answers every other session (none here) plainly.
type companionContextScript struct {
	mu          sync.Mutex
	inputs      map[string][]map[string]any
	workSession string
	decisionID  string
	topicID     string
	revisions   map[string]float64
}

func (s *companionContextScript) step(step string, input []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs[step] = input
}

const (
	contextDecide = "We decided to use PostgreSQL for the inventory service database"
	contextRecall = "Which database did we choose for the inventory service again"
	contextForget = "Please forget the inventory database decision and that topic"
	contextReplay = "Replay the earlier decision record exactly"
	contextAfter  = "What do we still know about the inventory service database"
)

func lastItemIsToolOutput(input []map[string]any) bool {
	return len(input) > 0 && stringMapValue(input[len(input)-1], "type") == "function_call_output"
}

func (s *companionContextScript) call(id string, args map[string]any) provideriface.FunctionCall {
	raw, _ := json.Marshal(args)
	return provideriface.FunctionCall{CallID: id, Name: tool.ManageCompanionToolName, Arguments: string(raw)}
}

var contextDecisionArgs = map[string]any{"action": "record_decision", "title": "Inventory database is PostgreSQL", "summary": "Use PostgreSQL for the inventory service database"}

func (s *companionContextScript) model(_ *queuedNoteFixture, req provideriface.Request) provideriface.Response {
	if lastItemIsToolOutput(req.Input) {
		return provideriface.Response{Text: "done", StopReason: "stop"}
	}
	last := lastConfirmationTestUserText(req.Input)
	s.mu.Lock()
	work, decision, topic, revisions := s.workSession, s.decisionID, s.topicID, s.revisions
	s.mu.Unlock()
	switch {
	case strings.Contains(last, contextDecide):
		s.step("decide", req.Input)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{
			s.call("rec-decision", contextDecisionArgs),
			s.call("rec-work", map[string]any{"action": "record_work", "title": "Inventory schema draft", "session_id": work}),
		}}
	case strings.Contains(last, contextRecall):
		s.step("recall", req.Input)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{s.call("search", map[string]any{"action": "search", "query": "inventory database postgresql"})}}
	case strings.Contains(last, contextForget):
		s.step("forget", req.Input)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{
			s.call("forget-decision", map[string]any{"action": "forget", "id": decision, "expected_revision": revisions[decision]}),
			s.call("forget-topic", map[string]any{"action": "forget", "id": topic, "expected_revision": revisions[topic]}),
		}}
	case strings.Contains(last, contextReplay):
		s.step("replay", req.Input)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{s.call("replay-decision", contextDecisionArgs)}}
	case strings.Contains(last, contextAfter):
		s.step("after", req.Input)
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{s.call("search-after", map[string]any{"action": "search", "query": "inventory database postgresql"})}}
	}
	return provideriface.Response{Text: "ok", StopReason: "stop"}
}

// companionSay posts the person's message and waits for its run; on a stuck
// run it reports what holds it (pending permissions, stored messages).
func companionSay(t *testing.T, f *queuedNoteFixture, requestID, content string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": content})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body)); code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	runID := stableSessionsV3PrimaryRunID(f.sessionID, requestID)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if intent, ok, err := f.sessions.GetSessionRunIntent(f.sessionID, runID); err == nil && ok && intent.Status == sessionruntime.RunIntentCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	intent, _, _ := f.sessions.GetSessionRunIntent(f.sessionID, runID)
	var recent []string
	for _, message := range f.messages() {
		recent = append(recent, fmt.Sprintf("%s: %.300s", message.Role, message.Content))
	}
	if len(recent) > 8 {
		recent = recent[len(recent)-8:]
	}
	t.Fatalf("run for %q is %s (%s); pending permissions %+v; recent messages:\n%s", content, intent.Status, intent.BlockedReason, f.pendingClientCalls(f.sessionID), strings.Join(recent, "\n"))
}

// toolEnvelope is the stored result of one tool call of the Companion.
func toolEnvelope(t *testing.T, f *queuedNoteFixture, callID string) (map[string]any, string) {
	t.Helper()
	output := f.toolOutput(f.sessionID, callID)
	if output == "" {
		t.Fatalf("no stored result for tool call %s", callID)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, output
	}
	return envelope, output
}

// Purpose: the Companion keeps and uses its own context bank through its own
// tool. Requirement: in a real Companion run (V3 executor, run loop, provider
// tool invoker, catalog runtime, store; scripted model), the Companion records
// a decision citing the person's message (message_seq omitted: the latest
// person message) and a work pointer citing a session from its tool results;
// after the source message left the bounded recent tail (40 messages), its
// manage_companion search returns the decision with that source, and the
// next turn's assembled context block (CompanionV3ContextMessages) carries it
// with its source; forgetting the decision and the message's topic through
// the tool removes both; replaying the identical record call (as a
// re-executed tool call would) is refused as forgotten and recreates nothing,
// and a later turn's assembly and indexing bring back neither. Threat: a
// context bank the Companion cannot write (the reviewed failure: HTTP/SDK
// only), decisions without a citable source, recall that depends on the
// unbounded transcript, or a forgotten record resurrected by replay or
// re-indexing. Boundary: Runtime.executeManageCompanion via the provider tool
// invoker, Server.ManageCompanionContext (companionDecisionSource, derived
// ids), SessionStore.Put/DeleteCompanionContext (tombstone),
// CompanionV3ContextMessages. Narrowest layer: the daemon's executor and run
// loop with a scripted provider (no live model), because the requirement is
// that the model's own tool calls do this.
func TestCompanionKeepsItsContextBankThroughItsOwnTool(t *testing.T) {
	script := &companionContextScript{inputs: map[string][]map[string]any{}, revisions: map[string]float64{}}
	f, _ := companionToolFixture(t, script.model)
	principal := testPrincipal()
	store := f.sessions.Store()
	script.mu.Lock()
	script.workSession = f.createSession("inventory-schema")
	script.mu.Unlock()

	// 1. A decision and a work pointer, recorded by the Companion's own calls.
	companionSay(t, f, "decide-1", contextDecide)
	decided, raw := toolEnvelope(t, f, "rec-decision")
	data, _ := decided["data"].(map[string]any)
	record, _ := data["record"].(map[string]any)
	source, _ := record["source"].(map[string]any)
	decideSeq := uint64(0)
	for _, message := range f.messages() {
		if message.Role == "user" && message.Content == contextDecide {
			decideSeq = message.GlobalSeq
		}
	}
	if decided["state"] != tool.EnvelopeStateOK || record["kind"] != "decision" || source["session_id"] != f.sessionID || source["message_seq"] != float64(decideSeq) || decided["untrusted_content"] != true {
		t.Fatalf("recorded decision (message #%d): %s", decideSeq, raw)
	}
	decisionID, _ := record["id"].(string)
	worked, raw := toolEnvelope(t, f, "rec-work")
	workData, _ := worked["data"].(map[string]any)
	workRecord, _ := workData["record"].(map[string]any)
	workSource, _ := workRecord["source"].(map[string]any)
	if worked["state"] != tool.EnvelopeStateOK || workRecord["kind"] != "work" || workSource["session_id"] != script.workSession {
		t.Fatalf("recorded work: %s", raw)
	}

	// 2. A long conversation: the decision's message leaves the tail.
	for i := 0; i < 45; i++ {
		key := fmt.Sprintf("filler-%d", i)
		if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: f.sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: key, PayloadHash: key, Message: &pebblestore.MessageSnapshot{Role: "assistant", Content: "noted " + key}}); err != nil {
			t.Fatal(err)
		}
	}

	// 3. Recall: the search tool and the turn's context block both carry it.
	companionSay(t, f, "recall-1", contextRecall)
	found, raw := toolEnvelope(t, f, "search")
	results, _ := found["data"].(map[string]any)["results"].([]any)
	hit := false
	for _, result := range results {
		row, _ := result.(map[string]any)
		rec, _ := row["record"].(map[string]any)
		src, _ := rec["source"].(map[string]any)
		if rec["id"] == decisionID && src["message_seq"] == float64(decideSeq) && strings.Contains(fmt.Sprint(row["source_text"]), "PostgreSQL") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("search did not return the decision with its source: %s", raw)
	}
	script.mu.Lock()
	recallInput := script.inputs["recall"]
	script.mu.Unlock()
	block := contextBlock(t, recallInput)
	if !strings.Contains(block, "decision "+decisionID) || !strings.Contains(block, fmt.Sprintf("message #%d", decideSeq)) {
		t.Fatalf("the turn's context block lacks the decision and its source:\n%s", block)
	}
	for _, item := range recallInput {
		rawItem, _ := json.Marshal(item["content"])
		if strings.Contains(string(rawItem), contextDecide) && !strings.Contains(string(rawItem), "Companion context bank") {
			t.Fatalf("the decision's message was reloaded as history, not retrieved: %v", item)
		}
	}

	// 4. Forget the decision and its message's topic through the tool.
	topicID := pebblestore.CompanionMessageContextID(f.sessionID, decideSeq)
	topic, ok, err := store.GetCompanionContext(principal.AccountScopeID, principal.UserID, topicID)
	if err != nil || !ok {
		t.Fatalf("the decision message was not indexed as a topic: %v %v", ok, err)
	}
	current, _, _ := store.GetCompanionContext(principal.AccountScopeID, principal.UserID, decisionID)
	script.mu.Lock()
	script.decisionID, script.topicID = decisionID, topicID
	script.revisions[decisionID], script.revisions[topicID] = float64(current.Revision), float64(topic.Revision)
	script.mu.Unlock()
	companionSay(t, f, "forget-1", contextForget)
	for _, call := range []string{"forget-decision", "forget-topic"} {
		if envelope, raw := toolEnvelope(t, f, call); envelope["state"] != tool.EnvelopeStateOK {
			t.Fatalf("%s: %s", call, raw)
		}
	}

	// 5. Replaying the identical record call is refused and recreates nothing.
	companionSay(t, f, "replay-1", contextReplay)
	if _, raw := toolEnvelope(t, f, "replay-decision"); !strings.Contains(raw, "forgotten") {
		t.Fatalf("replayed record was not refused as forgotten: %s", raw)
	}
	companionSay(t, f, "after-1", contextAfter)
	for _, id := range []string{decisionID, topicID} {
		if _, ok, err := store.GetCompanionContext(principal.AccountScopeID, principal.UserID, id); err != nil || ok {
			t.Fatalf("forgotten %s came back: %v %v", id, ok, err)
		}
	}
	if _, raw := toolEnvelope(t, f, "search-after"); strings.Contains(raw, decisionID) || strings.Contains(raw, topicID) {
		t.Fatalf("search after forgetting still returns it: %s", raw)
	}
	script.mu.Lock()
	afterInput := script.inputs["after"]
	script.mu.Unlock()
	if block := contextBlock(t, afterInput); strings.Contains(block, decisionID) || strings.Contains(block, topicID) {
		t.Fatalf("the next turn's context block still carries a forgotten record:\n%s", block)
	}
}

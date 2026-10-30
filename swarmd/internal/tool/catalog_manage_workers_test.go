package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// manageWorkersDefinitionSHA256 is the hash of the Orchestrator's
// manage_workers definition (json.Marshal, sorted keys). The definition is a
// prompt contract: it changes only on purpose, with this hash.
const manageWorkersDefinitionSHA256 = "02e36ca1d6cd5b78bbe66bba140411fea00f7f0378bbba6dedc8f5c00abc0a3b"

// Purpose: manage_workers exists once, in the catalog. The Orchestrator's
// definition (and its manage_automation alias) is generated from the
// declarations (held by hash) and keeps every field and the description the
// hand-written definition had, adding assign_task's task; every action is in
// the Orchestrator's definition and Swarm Control's default listing, takes
// machine, declares the worker routes' API scopes, and every field is used.
// Threat: a second hand-written schema, an action the model is not shown, or
// a field silently dropped from what the model may send. Boundary:
// manageWorkersCatalog, manageWorkersV2Definition, manageAutomationV2Definition.
func TestManageWorkersCatalogGeneratesTheOrchestratorDefinition(t *testing.T) {
	definition := manageWorkersV2Definition()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != manageWorkersDefinitionSHA256 {
		t.Fatalf("the Orchestrator's manage_workers definition changed (sha256 %s): %s", got, raw)
	}
	if alias := manageAutomationV2Definition(); alias.Name != "manage_automation" || alias.Description != definition.Description {
		t.Fatalf("manage_automation is not the catalog definition: %+v", alias)
	}
	properties := definition.Parameters["properties"].(map[string]any)
	enum := properties["action"].(map[string]any)["enum"].([]string)
	want := "help,list,inspect,create,update,attach,disable_automation,delete,test,request,activate,pause,resume,archive,propose,review,context,progress,assign_task"
	if strings.Join(enum, ",") != want {
		t.Fatalf("Orchestrator actions %v, want %s", enum, want)
	}
	// Every field of the hand-written definition, plus task.
	for _, field := range []string{"machine", "budget", "model", "agent", "worker_id", "automation_id", "expected_revision", "name", "description", "instructions", "requested_capabilities", "workspace_requirements", "local_bindings", "automations", "automation", "idempotency_key", "prompt", "input", "change_summary", "cursor", "limit", "timezone", "include_deleted", "document", "worker_review", "task"} {
		if properties[field] == nil {
			t.Fatalf("the definition lost field %q", field)
		}
	}
	catalog := manageWorkersCatalog()
	if named, ok := CatalogToolNamed("manage_workers"); !ok || len(named.Actions) != len(catalog.Actions) {
		t.Fatal("manage_workers is not in the catalog")
	}
	used := map[string]bool{}
	for _, action := range catalog.Actions {
		if action.Tool != "manage_workers" || action.Description == "" || len(action.APIScopes) == 0 || action.LocalOnly || action.Reach != CatalogReachAny {
			t.Fatalf("bad declaration: %+v", action)
		}
		if !action.Listed(CatalogProfileOrchestrator) || !action.Listed(CatalogProfileEssentials) {
			t.Fatalf("%s is not in the Orchestrator's definition and Swarm Control's default listing", action.Action)
		}
		takesMachine := false
		for _, input := range action.Inputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: input %q is not a declared field", action.Action, input)
			}
			used[input] = true
			takesMachine = takesMachine || input == "machine"
		}
		if !takesMachine {
			t.Fatalf("%s does not take machine", action.Action)
		}
	}
	for _, field := range catalog.Fields {
		if !used[field.Name] {
			t.Fatalf("field %q is declared but no action takes it", field.Name)
		}
	}
}

// Purpose: every worker action reaches a linked machine: the linked gate
// (FleetWorkActionAllowed, which the permission service also reads) admits
// exactly the declared actions, for manage_workers and its manage_automation
// alias; the actions that start a run there (test, request, assign_task) are
// the ones the ask rule holds (FleetTaskStart). Threat: the catalog promising
// a reach the gate refuses, a gate wider than the catalog, or a run started
// on an ask machine without the person's approval. Boundary: CatalogAction
// against FleetWorkActionAllowed and FleetTaskStart.
func TestManageWorkersCatalogReachMatchesTheHandler(t *testing.T) {
	starts := map[string]bool{"test": true, "request": true, "assign_task": true}
	for _, name := range []string{"manage_workers", "manage_automation"} {
		for _, action := range manageWorkersCatalog().Actions {
			if !FleetWorkActionAllowed(name, action.Action) {
				t.Fatalf("%s %s is refused on a linked machine", name, action.Action)
			}
			if FleetTaskStart(name, action.Action) != starts[action.Action] {
				t.Fatalf("%s %s: FleetTaskStart %v", name, action.Action, FleetTaskStart(name, action.Action))
			}
		}
		for _, refused := range []string{"", "approve", "accept", "get", "runs", "set_permissions"} {
			if FleetWorkActionAllowed(name, refused) {
				t.Fatalf("linked gate admits %s %q", name, refused)
			}
		}
	}
}

// recordingWorkers records each worker service call and answers with a
// fixed record.
type recordingWorkers struct {
	calls  []string
	who    []string
	agents []string
	answer map[string]any
}

func (r *recordingWorkers) ManageWorkflows(context.Context, identity.Principal, string, map[string]any) (any, error) {
	return nil, context.Canceled
}

func (r *recordingWorkers) ManageWorkers(_ context.Context, _ identity.Principal, who, agent string, args map[string]any) (any, error) {
	r.calls = append(r.calls, asString(args["machine"])+"/"+asString(args["action"]))
	r.who = append(r.who, who)
	r.agents = append(r.agents, agent)
	if asString(args["machine"]) != "" {
		return json.RawMessage(`{"workers":[{"id":"w1"}]}`), nil
	}
	return r.answer, nil
}

// Purpose: Swarm Control and the Orchestrator run manage_workers through the
// one handler: the same envelope for the same call; the caller named by its
// AI key or its session and agent; the agent required for a session caller
// (the Orchestrator-only rule reads it); a key below an action's level
// refused before anything runs (create needs manage, assign_task write);
// machine= reaches the service with machine set and its answer is untrusted
// envelope data; a cut worker list carries next with the opaque cursor.
// Threat: two implementations of one tool, a read or write key creating or
// deleting workers, an agent-less session call, or a cut list with no way on.
// Boundary: Runtime.ExecuteControlCatalog, executeManageWorkers,
// catalogEnvelope, CheckEnvelope, a recording service.
func TestManageWorkersControlAndOrchestratorShareTheHandler(t *testing.T) {
	_, store := openTaskViewStore(t)
	account, user := "account-workers", "user-workers"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "orch", AccountScopeID: account, UserID: user}, user, account); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	service := &recordingWorkers{answer: map[string]any{"found": true, "worker": map[string]any{"id": "w1"}}}
	r.SetWorkflowLifecycleService(service)
	control := func(levels []string, args map[string]any) (string, error) {
		return r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: p, Levels: levels, KeyID: "k1", Tool: "manage_workers", Arguments: args})
	}
	orchestrator := func(agent string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		ctx := context.Background()
		if agent != "" {
			ctx = WithCatalogAgent(ctx, agent)
		}
		return r.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "orch", Principal: p}, Call{Name: "manage_workers", Arguments: string(raw)})
	}
	for _, refused := range []struct {
		levels []string
		args   map[string]any
		need   string
	}{
		{[]string{"swarm:read", "swarm:write"}, map[string]any{"action": "create", "name": "x", "instructions": "y"}, "needs swarm:manage"},
		{[]string{"swarm:read", "swarm:write"}, map[string]any{"action": "delete", "worker_id": "w1", "expected_revision": 1}, "needs swarm:manage"},
		{[]string{"swarm:read"}, map[string]any{"action": "assign_task", "worker_id": "w1", "task": "t"}, "needs swarm:write"},
	} {
		if _, err := control(refused.levels, refused.args); err == nil || !strings.Contains(err.Error(), refused.need) {
			t.Fatalf("%v with %v: %v", refused.args["action"], refused.levels, err)
		}
	}
	if _, err := orchestrator("", map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "calling agent") {
		t.Fatalf("agent-less session call: %v", err)
	}
	if len(service.calls) != 0 {
		t.Fatalf("a refused call reached the service: %v", service.calls)
	}
	inspect := map[string]any{"action": "inspect", "worker_id": "w1"}
	a, err := control([]string{"swarm:read"}, inspect)
	if err != nil {
		t.Fatal(err)
	}
	b, err := orchestrator("swarm", inspect)
	if err != nil || a != b || CheckEnvelope(a) != nil {
		t.Fatalf("envelopes differ or break the contract:\n%s\n%s %v", a, b, err)
	}
	if strings.Join(service.who, ",") != "key:k1,orch" || strings.Join(service.agents, ",") != ",swarm" {
		t.Fatalf("callers named %v agents %v", service.who, service.agents)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(a), &e); err != nil || e.State != EnvelopeStateOK || !e.UntrustedContent || e.Tool != "manage_workers" {
		t.Fatalf("inspect envelope: %s %v", a, err)
	}
	linked, err := control([]string{"swarm:read"}, map[string]any{"action": "list", "machine": "box"})
	if err != nil || CheckEnvelope(linked) != nil {
		t.Fatalf("linked: %s %v", linked, err)
	}
	if err := json.Unmarshal([]byte(linked), &e); err != nil || e.Machine != "box" || !e.UntrustedContent {
		t.Fatalf("linked envelope: %s", linked)
	}
	if service.calls[len(service.calls)-1] != "box/list" {
		t.Fatalf("linked call %v", service.calls)
	}
	service.answer = map[string]any{"workers": []any{}, "next_cursor": "opaque-2"}
	listed, err := control([]string{"swarm:read"}, map[string]any{"action": "list", "limit": 5})
	if err != nil || CheckEnvelope(listed) != nil {
		t.Fatalf("list: %s %v", listed, err)
	}
	if err := json.Unmarshal([]byte(listed), &e); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.Next.(map[string]any); next["action"] != "list" || next["cursor"] != "opaque-2" {
		t.Fatalf("list next: %s", listed)
	}
}

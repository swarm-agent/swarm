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

// manageWorkflowsDefinitionSHA256 is the hash of the Orchestrator's
// manage_workflows definition (json.Marshal, sorted keys). The definition is
// a prompt contract: it changes only on purpose, with this hash. (Stage 3:
// its FleetWorkDescription names manage_jobs' and manage_workers' machine
// reach.)
const manageWorkflowsDefinitionSHA256 = "6b1e16a34a2dfb9938e383a12cef71a1ddea87439f70408e01e90894f8bfe333"

// Purpose: manage_workflows exists once, in the catalog. The Orchestrator's
// definition is generated from the declarations (held by hash) and names
// every action the handler serves: the nine the Orchestrator had, plus
// export, import, restore and stop_all. Every input is a declared field,
// every field is used, each action's own schema holds only its inputs, and
// every action declares the API scopes of its route. Threat: a second
// hand-written schema, an action the model is not shown, an input no action
// declares, or a silent change to what the model is shown. Boundary:
// manageWorkflowsCatalog, CatalogTool.OrchestratorDefinition, InputSchema.
func TestManageWorkflowsCatalogGeneratesTheOrchestratorDefinition(t *testing.T) {
	definition := manageWorkflowsDefinition()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != manageWorkflowsDefinitionSHA256 {
		t.Fatalf("the Orchestrator's manage_workflows definition changed (sha256 %s): %s", got, raw)
	}
	properties := definition.Parameters["properties"].(map[string]any)
	enum := properties["action"].(map[string]any)["enum"].([]string)
	want := "list,get,deliveries,history,propose,disable,enable,delete,help,export,import,restore,stop_all"
	if strings.Join(enum, ",") != want {
		t.Fatalf("Orchestrator actions %v, want %s", enum, want)
	}
	catalog := manageWorkflowsCatalog()
	if named, ok := CatalogToolNamed("manage_workflows"); !ok || len(named.Actions) != len(catalog.Actions) {
		t.Fatal("manage_workflows is not in the catalog")
	}
	used := map[string]bool{}
	for _, action := range catalog.Actions {
		if action.Tool != "manage_workflows" || action.Description == "" || len(action.APIScopes) == 0 || action.LocalOnly {
			t.Fatalf("bad declaration: %+v", action)
		}
		if !action.Listed(CatalogProfileOrchestrator) || !action.Listed(CatalogProfileEssentials) {
			t.Fatalf("%s is not in the Orchestrator's definition and Swarm Control's default listing", action.Action)
		}
		schema := catalog.InputSchema(action)["properties"].(map[string]any)
		if len(schema) != len(action.Inputs)+1 {
			t.Fatalf("%s: schema has %d properties for %d inputs", action.Action, len(schema), len(action.Inputs))
		}
		for _, input := range action.Inputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: input %q is not a declared field", action.Action, input)
			}
			used[input] = true
		}
	}
	for _, field := range catalog.Fields {
		if !used[field.Name] || properties[field.Name] == nil {
			t.Fatalf("field %q is declared but no action takes it, or the definition lacks it", field.Name)
		}
	}
}

// Purpose: an action's declared reach is what the handler does with
// machine=: every action but help reaches a linked machine (and takes
// machine), the permission and fleet gate (FleetWorkActionAllowed) admits
// exactly those, and help never leaves this machine. Threat: the catalog
// promising a reach the linked routes refuse, or a linked gate wider than the
// catalog. Boundary: CatalogAction.Reach against FleetWorkActionAllowed.
func TestManageWorkflowsCatalogReachMatchesTheHandler(t *testing.T) {
	for _, action := range manageWorkflowsCatalog().Actions {
		takesMachine := false
		for _, input := range action.Inputs {
			takesMachine = takesMachine || input == "machine"
		}
		linked := action.Reach == CatalogReachAny
		if linked != (action.Action != "help") || linked != takesMachine || linked != FleetWorkActionAllowed("manage_workflows", action.Action) {
			t.Fatalf("%s: reach %s, machine input %v, fleet gate %v", action.Action, action.Reach, takesMachine, FleetWorkActionAllowed("manage_workflows", action.Action))
		}
	}
	for _, refused := range []string{"accept", "rotate_key", "secrets", "set"} {
		if FleetWorkActionAllowed("manage_workflows", refused) {
			t.Fatalf("linked gate admits %s", refused)
		}
	}
}

// recordingWorkflows records each lifecycle and linked call.
type recordingWorkflows struct {
	calls  []string
	who    []string
	linked []string
}

func (r *recordingWorkflows) ManageWorkflows(_ context.Context, _ identity.Principal, who string, args map[string]any) (any, error) {
	r.calls = append(r.calls, asString(args["action"]))
	r.who = append(r.who, who)
	if _, has := args["machine"]; has {
		return nil, context.Canceled
	}
	return map[string]any{"automations": []any{}, "stopped": false}, nil
}

func (r *recordingWorkflows) CallFleetWork(_ context.Context, _ identity.Principal, machine, name, action string, _ map[string]any) (json.RawMessage, error) {
	r.linked = append(r.linked, machine+"/"+name+"/"+action)
	return json.RawMessage(`{"ok":true,"stop":{"stopped":true}}`), nil
}

// Purpose: Swarm Control and the Orchestrator run manage_workflows through
// the one handler: the same envelope for the same call, the caller named by
// its AI key (Swarm Control) or its session (Orchestrator), machine= sent to
// the linked machine's workflow routes for the declared action, help kept
// home, and a key below an action's level (stop_all needs swarm:manage)
// refused before anything runs. Threat: two implementations of one tool, a
// read or write key turning the machine-wide stop switch, or a linked call
// reaching an action the catalog does not declare. Boundary:
// Runtime.ExecuteControlCatalog, executeManageWorkflows, a recording service.
func TestManageWorkflowsControlAndOrchestratorShareTheHandler(t *testing.T) {
	_, store := openTaskViewStore(t)
	account, user := "account-wf", "user-wf"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "orch", AccountScopeID: account, UserID: user}, user, account); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	service := &recordingWorkflows{}
	r.SetWorkflowLifecycleService(service)
	control := func(levels []string, args map[string]any) (string, error) {
		return r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: p, Levels: levels, KeyID: "k1", Tool: "manage_workflows", Arguments: args})
	}
	orchestrator := func(args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{SessionID: "orch", Principal: p}, Call{Name: "manage_workflows", Arguments: string(raw)})
	}
	if _, err := control([]string{"swarm:read", "swarm:write"}, map[string]any{"action": "stop_all", "stopped": true}); err == nil || !strings.Contains(err.Error(), "needs swarm:manage") {
		t.Fatalf("write key stop_all: %v", err)
	}
	if len(service.calls)+len(service.linked) != 0 {
		t.Fatal("a refused call reached the service")
	}
	list := map[string]any{"action": "list"}
	a, err := control([]string{"swarm:read"}, list)
	if err != nil {
		t.Fatal(err)
	}
	b, err := orchestrator(list)
	if err != nil || a != b || CheckEnvelope(a) != nil {
		t.Fatalf("envelopes differ or break the contract:\n%s\n%s %v", a, b, err)
	}
	if strings.Join(service.who, ",") != "key:k1,orch" {
		t.Fatalf("callers named %v", service.who)
	}
	linked, err := control([]string{"swarm:manage"}, map[string]any{"action": "stop_all", "machine": "box", "stopped": true})
	if err != nil {
		t.Fatal(err)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(linked), &e); err != nil || e.Machine != "box" || !e.UntrustedContent || e.Action != "stop_all" || CheckEnvelope(linked) != nil {
		t.Fatalf("linked envelope: %s %v", linked, err)
	}
	if strings.Join(service.linked, ",") != "box/manage_workflows/stop_all" {
		t.Fatalf("linked calls %v", service.linked)
	}
	if _, err := orchestrator(map[string]any{"action": "help", "machine": "box"}); err == nil {
		t.Fatal("help went to a linked machine")
	}
	if len(service.linked) != 1 {
		t.Fatal("a refused linked call reached the machine")
	}
}

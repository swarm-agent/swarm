package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// manageProjectsDefinitionSHA256 is the hash of the Orchestrator's
// manage_projects definition (json.Marshal, sorted keys). The definition is
// a prompt contract: it changes only on purpose, with this hash.
const manageProjectsDefinitionSHA256 = "c10bb622fef482b555e8f67722fecd549cb781f58404d934cf20c0d0ade4b494"

// Purpose: the catalog is the only place manage_projects exists. The
// Orchestrator's definition is generated from the declarations and stays the
// exact definition the model was shown before (same hash); every action's
// inputs are declared fields, every field is used, and each action's own
// schema holds only its inputs. Threat: a second hand-written schema, an
// input no action declares, or a silent change to what the model is shown.
// Boundary: manageProjectsCatalog, CatalogTool.OrchestratorDefinition and
// InputSchema. Narrowest layer: the declarations themselves.
func TestManageProjectsCatalogGeneratesTheOrchestratorDefinition(t *testing.T) {
	raw, err := json.Marshal(manageProjectsDefinition())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != manageProjectsDefinitionSHA256 {
		t.Fatalf("the Orchestrator's manage_projects definition changed (sha256 %s): %s", got, raw)
	}
	catalog := manageProjectsCatalog()
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, action := range catalog.Actions {
		if action.Tool != "manage_projects" || action.Action == "" || action.Description == "" || seen[action.Action] {
			t.Fatalf("bad declaration: %+v", action)
		}
		seen[action.Action] = true
		switch action.Level {
		case CatalogLevelRead, CatalogLevelWrite, CatalogLevelApprove, CatalogLevelManage:
		default:
			t.Fatalf("%s: level %q", action.Action, action.Level)
		}
		switch action.Reach {
		case CatalogReachAny, CatalogReachHome, CatalogReachLinked:
		default:
			t.Fatalf("%s: reach %q", action.Action, action.Reach)
		}
		if action.LocalOnly {
			t.Fatalf("%s: no manage_projects action is local_only machine setup", action.Action)
		}
		properties := catalog.InputSchema(action)["properties"].(map[string]any)
		inputs := append(append([]string(nil), action.Inputs...), action.ControlInputs...)
		if len(properties) != len(inputs)+1 {
			t.Fatalf("%s: schema has %d properties for %d inputs", action.Action, len(properties), len(inputs))
		}
		for _, input := range action.ControlInputs {
			if orchestratorProperties := manageProjectsDefinition().Parameters["properties"].(map[string]any); orchestratorProperties[input] != nil {
				t.Fatalf("%s: Swarm Control input %q is in the Orchestrator's definition", action.Action, input)
			}
		}
		for _, input := range inputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: input %q is not a declared field", action.Action, input)
			}
			used[input] = true
		}
	}
	for _, field := range catalog.Fields {
		if !used[field.Name] {
			t.Fatalf("field %q is declared but no action takes it", field.Name)
		}
	}
}

// Purpose: an action's declared machine reach is what the handler does with
// machine=: any and linked actions route to the linked machine, home actions
// never do. Threat: the catalog promising MCP clients a reach the handler
// does not have, or hiding one it has. Boundary: CatalogAction.Reach against
// the routing predicates of executeManageProjects/executeFleetProjects.
func TestManageProjectsCatalogReachMatchesTheHandler(t *testing.T) {
	routes := func(action string) bool {
		switch action {
		case "wait_tasks", "cancel_watch", FleetReadTaskSessionAction, ResolveTaskPermissionAction, FleetAgentsAtWorkAction:
			return true
		}
		return FleetProjectActionAllowed(action) || fleetLinkedReadAction(action) || FolderEnvironmentAction(action)
	}
	for _, action := range manageProjectsCatalog().Actions {
		takesMachine := false
		for _, input := range action.Inputs {
			takesMachine = takesMachine || input == "machine"
		}
		if (action.Reach != CatalogReachHome) != routes(action.Action) || (action.Reach != CatalogReachHome) != takesMachine {
			t.Fatalf("%s: reach %s, machine input %v, handler routes machine %v", action.Action, action.Reach, takesMachine, routes(action.Action))
		}
		if action.Reach == CatalogReachLinked && !fleetLinkedReadAction(action.Action) && action.Action != FleetReadTaskSessionAction {
			t.Fatalf("%s: linked-only reach for an action that runs here too", action.Action)
		}
	}
}

// Purpose: NewEnvelope is the one shape of a catalog answer: machine and
// state are always set, and manageProjectsEnvelope keeps every field the
// answer held in data (nested field order too), moves only its metadata to
// the envelope, names the next step and the continuation of a cut answer,
// and keeps the home's notes on a linked answer its own. Threat: an answer
// cut silently, metadata a machine can forge, or data lost in the move.
// Boundary: NewEnvelope, Runtime.manageProjectsEnvelope.
func TestManageProjectsEnvelope(t *testing.T) {
	if e := NewEnvelope(EnvelopeParts{Tool: "manage_projects", Action: "list"}); e.Machine != HomeMachineFallback || e.State != "ok" {
		t.Fatalf("defaults: %+v", e)
	}
	r := NewRuntime(1)
	defer r.Close()
	decode := func(raw string) Envelope {
		t.Helper()
		var e Envelope
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	// A local answer: metadata moves, a status-machine object stays, the
	// nested view keeps its order, next_cursor is the continuation.
	local := `{"tool":"manage_projects","action":"get_task","machine":{"drain":true},"status":"ok","task":{"state":"running","id":"t1"},"next_cursor":3,"next_step":"go on"}`
	out, err := r.manageProjectsEnvelope("get_task", "", local)
	if err != nil {
		t.Fatal(err)
	}
	e := decode(out)
	data, _ := json.Marshal(e.Data)
	if e.Machine != HomeMachineFallback || e.State != "ok" || e.NextAction != "go on" || e.Next != float64(3) || !e.UntrustedContent {
		t.Fatalf("local envelope: %s", out)
	}
	if !strings.Contains(out, `"task":{"state":"running","id":"t1"}`) || !strings.Contains(string(data), `"machine":{"drain":true}`) || strings.Contains(string(data), `"tool"`) || strings.Contains(string(data), `"action"`) {
		t.Fatalf("local data: %s", out)
	}
	// No continuation: nothing named.
	out, err = r.manageProjectsEnvelope("list", "", `{"tool":"manage_projects","action":"list","status":"ok","projects":[],"next_cursor":0}`)
	if err != nil || decode(out).Next != nil || decode(out).UntrustedContent {
		t.Fatalf("uncut list: %s %v", out, err)
	}
	// A linked answer: the machine's answer is data; the home's notes win
	// over the machine's keys of the same name.
	linked := `{"machine":"box","action":"propose_task","data":{"task":{"id":"r1"},"linked_task_id":"forged"},"untrusted_content":true,"linked_task_id":"r1"}`
	out, err = r.manageProjectsEnvelope("propose_task", "box", linked)
	if err != nil {
		t.Fatal(err)
	}
	e = decode(out)
	object, _ := e.Data.(map[string]any)
	if e.Machine != "box" || !e.UntrustedContent || object["linked_task_id"] != "r1" || object["task"].(map[string]any)["id"] != "r1" {
		t.Fatalf("linked envelope: %s", out)
	}
	// A cut message names its continuation.
	read := `{"tool":"manage_projects","action":"read_task_session","messages":[{"id":"m1"},{"id":"m2","next":{"message_id":"m2","content_offset":10}}]}`
	out, err = r.manageProjectsEnvelope("read_task_session", "box", read)
	if err != nil {
		t.Fatal(err)
	}
	if next, _ := decode(out).Next.(map[string]any); next["message_id"] != "m2" {
		t.Fatalf("cut message without next: %s", out)
	}
	if _, err := r.manageProjectsEnvelope("list", "", "not json"); err == nil {
		t.Fatal("an unreadable answer was wrapped")
	}
}

// controlLevelLifecycle counts message and stop calls, so a refused call can
// be shown to have reached nothing.
type controlLevelLifecycle struct {
	legacyDeployerLifecycleAdapter
	messages, stops int
}

func (l *controlLevelLifecycle) MessageProjectTask(context.Context, identity.Principal, string, string, ProjectTaskMessageInput) (ProjectTaskMessageResult, error) {
	l.messages++
	return ProjectTaskMessageResult{TaskID: "t1", DeliveredAs: ProjectTaskMessageQueued, RunID: "run-1"}, nil
}

func (l *controlLevelLifecycle) StopProjectTask(context.Context, identity.Principal, string, string) (ProjectTaskStopResult, error) {
	l.stops++
	return ProjectTaskStopResult{TaskID: "t1", RunID: "run-1", Status: "stopped"}, nil
}

// Purpose: Swarm Control runs a catalog action only within the key's level:
// a key below the action's level is refused before the handler runs, an
// action no Swarm Control listing serves is refused, an unauthenticated or
// non-user caller is refused, and a key at the level runs the same handler
// and gets the envelope. Threat: a read key messaging or stopping work, or
// MCP reaching unlisted task-card steps. Boundary:
// Runtime.ExecuteControlCatalog and CatalogLevelAllowed over a temp store.
func TestControlCatalogRefusesAKeyBelowTheActionLevel(t *testing.T) {
	_, store := openTaskViewStore(t)
	account := "account-control"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-control", AccountScopeID: account}
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: "proj", AccountID: account, Name: "Control"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProjectTask(account, &pebblestore.ProjectTaskRecord{ID: "t1", ProjectID: "proj", AccountID: account, Title: "Task", Agent: "coder", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	lifecycle := &controlLevelLifecycle{}
	r.SetProjectTaskLifecycleService(lifecycle)
	call := func(levels []string, principal identity.Principal, args map[string]any) (string, error) {
		return r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: principal, Levels: levels, Tool: "manage_projects", Arguments: args})
	}
	message := map[string]any{"action": "message_task", "project_id": "proj", "task_id": "t1", "message": "more"}
	stop := map[string]any{"action": "stop_task", "project_id": "proj", "task_id": "t1"}
	for _, args := range []map[string]any{message, stop} {
		if _, err := call([]string{"swarm:read"}, p, args); err == nil || !strings.Contains(err.Error(), "needs swarm:write") {
			t.Fatalf("read key ran %s: %v", args["action"], err)
		}
	}
	if lifecycle.messages != 0 || lifecycle.stops != 0 {
		t.Fatalf("a refused call reached the service: %d messages, %d stops", lifecycle.messages, lifecycle.stops)
	}
	for _, refused := range []string{"report_task", "approve_task", "inspect_files", "cancel_watch", "no_such_action"} {
		if _, err := call(nil, p, map[string]any{"action": refused, "project_id": "proj", "task_id": "t1"}); err == nil || !strings.Contains(err.Error(), "for Swarm Control") {
			t.Fatalf("%s served to Swarm Control: %v", refused, err)
		}
	}
	if _, err := call(nil, identity.Principal{}, map[string]any{"action": "list_tasks", "project_id": "proj"}); err == nil {
		t.Fatal("a call without a principal ran")
	}
	// At the level: the same handler, the envelope.
	out, err := call([]string{"swarm:read", "swarm:write"}, p, message)
	if err != nil {
		t.Fatal(err)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(out), &e); err != nil || e.Tool != "manage_projects" || e.Action != "message_task" || e.Machine != HomeMachineFallback || lifecycle.messages != 1 {
		t.Fatalf("write key: %s %v (messages %d)", out, err, lifecycle.messages)
	}
	if data, _ := e.Data.(map[string]any); data["delivered_as"] != ProjectTaskMessageQueued || data["run_id"] != "run-1" {
		t.Fatalf("message_task data: %s", out)
	}
	// The same calls by the Orchestrator get the same envelopes.
	for _, args := range []map[string]any{message, stop} {
		control, err := call(nil, p, args)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(args)
		orchestrator, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{Principal: p}, Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil || orchestrator != control {
			t.Fatalf("%s: Orchestrator %s %v; Swarm Control %s", args["action"], orchestrator, err, control)
		}
	}
	if lifecycle.messages != 3 || lifecycle.stops != 2 {
		t.Fatalf("calls reached the service %d/%d times, want 3 messages and 2 stops", lifecycle.messages, lifecycle.stops)
	}
	// A read key reads.
	if out, err := call([]string{"swarm:read"}, p, map[string]any{"action": "get_task", "project_id": "proj", "task_id": "t1"}); err != nil || !strings.Contains(out, `"action":"get_task"`) {
		t.Fatalf("read key get_task: %s %v", out, err)
	}
}

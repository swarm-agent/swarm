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

// manageJobsDefinitionSHA256 is the hash of the Orchestrator's manage_jobs
// definition (json.Marshal, sorted keys). The definition is a prompt
// contract: it changes only on purpose, with this hash. Stage 7a changed it on
// purpose: the swarm_* job tools' inputs joined (get_job's include_diff,
// diff_chars, include_transcript, transcript_messages and detail_chars;
// give_job's workspace, files, review_job and review_checkout; deliver_job's
// automation); then, model and wait_seconds are Swarm Control inputs only.
const manageJobsDefinitionSHA256 = "b20b764e8ef48f20fdcbe618e9579488466b738240603ed9ca09e08dee0a7a39"

// Purpose: manage_jobs exists once, in the catalog. The Orchestrator's
// definition is generated from the declarations (held by hash) and names
// every action the handler serves: the six the Orchestrator had, plus
// answer_job, deliver_job, stop_job and schedule_job. Every input is a
// declared field, every field is used, every action declares the API scopes
// of its route, is in the Orchestrator's definition and Swarm Control's
// default listing, and takes machine. Threat: a second hand-written schema, an
// action the model is not shown, or a silent change to what it is shown.
// Boundary: manageJobsCatalog, CatalogTool.OrchestratorDefinition.
func TestManageJobsCatalogGeneratesTheOrchestratorDefinition(t *testing.T) {
	definition := manageJobsDefinition()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != manageJobsDefinitionSHA256 {
		t.Fatalf("the Orchestrator's manage_jobs definition changed (sha256 %s): %s", got, raw)
	}
	properties := definition.Parameters["properties"].(map[string]any)
	enum := properties["action"].(map[string]any)["enum"].([]string)
	want := "list_runs,get_job,deliveries,rerun_failed_steps,give_job,message_job,answer_job,deliver_job,stop_job,schedule_job"
	if strings.Join(enum, ",") != want {
		t.Fatalf("Orchestrator actions %v, want %s", enum, want)
	}
	catalog := manageJobsCatalog()
	if named, ok := CatalogToolNamed("manage_jobs"); !ok || len(named.Actions) != len(catalog.Actions) {
		t.Fatal("manage_jobs is not in the catalog")
	}
	used, controlOnly := map[string]bool{}, map[string]bool{}
	for _, action := range catalog.Actions {
		if action.Tool != "manage_jobs" || action.Description == "" || len(action.APIScopes) == 0 || action.LocalOnly {
			t.Fatalf("bad declaration: %+v", action)
		}
		if !action.Listed(CatalogProfileOrchestrator) || !action.Listed(CatalogProfileEssentials) {
			t.Fatalf("%s is not in the Orchestrator's definition and Swarm Control's default listing", action.Action)
		}
		schema := catalog.InputSchema(action)["properties"].(map[string]any)
		if len(schema) != len(action.Inputs)+len(action.ControlInputs)+1 {
			t.Fatalf("%s: schema has %d properties for %d inputs", action.Action, len(schema), len(action.Inputs)+len(action.ControlInputs))
		}
		for _, input := range action.Inputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: input %q is not a declared field", action.Action, input)
			}
			used[input] = true
		}
		// Swarm Control inputs (then, model, held waits) are declared
		// fields that the Orchestrator's definition never shows.
		for _, input := range action.ControlInputs {
			if _, ok := catalog.Field(input); !ok {
				t.Fatalf("%s: Swarm Control input %q is not a declared field", action.Action, input)
			}
			if properties[input] != nil {
				t.Fatalf("%s: Swarm Control input %q is in the Orchestrator's definition", action.Action, input)
			}
			controlOnly[input] = true
		}
	}
	for _, field := range catalog.Fields {
		if controlOnly[field.Name] && !used[field.Name] {
			continue
		}
		if !used[field.Name] || properties[field.Name] == nil {
			t.Fatalf("field %q is declared but no action takes it, or the definition lacks it", field.Name)
		}
	}
}

// Purpose: every job action reaches a linked machine: each declares reach
// any and takes machine, and the linked gate (FleetWorkActionAllowed)
// admits exactly the declared actions. The actions that start or continue
// agent work there are the ones the ask rule holds (FleetTaskStart); reads
// and stop/deliver are not. Threat: the catalog promising a reach the linked
// gate refuses, or a linked gate wider than the catalog. Boundary:
// CatalogAction.Reach against FleetWorkActionAllowed and FleetTaskStart.
func TestManageJobsCatalogReachMatchesTheHandler(t *testing.T) {
	starts := map[string]bool{"give_job": true, "message_job": true, "answer_job": true, "rerun_failed_steps": true, "schedule_job": true}
	for _, action := range manageJobsCatalog().Actions {
		takesMachine := false
		for _, input := range action.Inputs {
			takesMachine = takesMachine || input == "machine"
		}
		if action.Reach != CatalogReachAny || !takesMachine || !FleetWorkActionAllowed("manage_jobs", action.Action) {
			t.Fatalf("%s: reach %s, machine input %v, fleet gate %v", action.Action, action.Reach, takesMachine, FleetWorkActionAllowed("manage_jobs", action.Action))
		}
		if FleetTaskStart("manage_jobs", action.Action) != starts[action.Action] {
			t.Fatalf("%s: FleetTaskStart %v", action.Action, FleetTaskStart("manage_jobs", action.Action))
		}
	}
	for _, refused := range []string{"", "approve", "delete_worker", "set_permissions", "list"} {
		if FleetWorkActionAllowed("manage_jobs", refused) {
			t.Fatalf("linked gate admits %q", refused)
		}
	}
}

// recordingJobs records each job service call and answers with a fixed
// record per action.
type recordingJobs struct {
	calls  []string
	who    []string
	agents []string
	answer map[string]any
}

func (r *recordingJobs) ManageWorkflows(context.Context, identity.Principal, string, map[string]any) (any, error) {
	return nil, context.Canceled
}

func (r *recordingJobs) ManageJobs(_ context.Context, _ identity.Principal, who, agent string, args map[string]any) (any, error) {
	r.calls = append(r.calls, asString(args["machine"])+"/"+asString(args["action"]))
	r.who = append(r.who, who)
	r.agents = append(r.agents, agent)
	if asString(args["machine"]) != "" {
		return json.RawMessage(`{"job_id":"worker/w/r","status":"needs_input"}`), nil
	}
	return r.answer, nil
}

// Purpose: Swarm Control and the Orchestrator run manage_jobs through the one
// handler: the same envelope for the same call, the caller named by its AI
// key (Swarm Control) or its session and agent (Orchestrator); the agent is
// required for a session caller (it is what the owner's grant names); a key
// below an action's level is refused before anything runs; machine= reaches
// the service with machine set and its answer is the envelope's untrusted
// data; a job waiting on a question is in state waiting; a cut page carries
// next with page_offset and page_digest, and a cut run list next with the
// opaque string cursor. Threat: two implementations of one tool, a read key
// starting a job, an agent-less session call bypassing per-agent grants, or
// a cut answer with no way to read on. Boundary: Runtime.ExecuteControlCatalog,
// executeManageJobs, catalogEnvelope, CheckEnvelope, a recording service.
func TestManageJobsControlAndOrchestratorShareTheHandler(t *testing.T) {
	_, store := openTaskViewStore(t)
	account, user := "account-jobs", "user-jobs"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "orch", AccountScopeID: account, UserID: user}, user, account); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	service := &recordingJobs{answer: map[string]any{"job_id": "worker/w/r", "status": "running"}}
	r.SetWorkflowLifecycleService(service)
	control := func(levels []string, args map[string]any) (string, error) {
		return r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: p, Levels: levels, KeyID: "k1", Tool: "manage_jobs", Arguments: args})
	}
	orchestrator := func(agent string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		ctx := context.Background()
		if agent != "" {
			ctx = WithCatalogAgent(ctx, agent)
		}
		return r.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "orch", Principal: p}, Call{Name: "manage_jobs", Arguments: string(raw)})
	}
	if _, err := control([]string{"swarm:read"}, map[string]any{"action": "give_job", "worker": "w", "brief": "b"}); err == nil || !strings.Contains(err.Error(), "needs swarm:write") {
		t.Fatalf("read key give_job: %v", err)
	}
	if _, err := orchestrator("", map[string]any{"action": "get_job", "job_id": "worker/w/r"}); err == nil || !strings.Contains(err.Error(), "calling agent") {
		t.Fatalf("agent-less session call: %v", err)
	}
	if len(service.calls) != 0 {
		t.Fatalf("a refused call reached the service: %v", service.calls)
	}
	get := map[string]any{"action": "get_job", "job_id": "worker/w/r"}
	a, err := control([]string{"swarm:read"}, get)
	if err != nil {
		t.Fatal(err)
	}
	b, err := orchestrator("swarm", get)
	if err != nil || a != b || CheckEnvelope(a) != nil {
		t.Fatalf("envelopes differ or break the contract:\n%s\n%s %v", a, b, err)
	}
	if strings.Join(service.who, ",") != "key:k1,orch" || strings.Join(service.agents, ",") != ",swarm" {
		t.Fatalf("callers named %v agents %v", service.who, service.agents)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(a), &e); err != nil || e.State != EnvelopeStateOK || !e.UntrustedContent || e.Tool != "manage_jobs" {
		t.Fatalf("running job envelope: %s %v", a, err)
	}
	// A job waiting on its question says so in the envelope's state.
	service.answer = map[string]any{"job_id": "worker/w/r", "status": "needs_input", "questions": []any{map[string]any{"id": "q1"}}}
	waiting, err := control([]string{"swarm:read"}, get)
	if err != nil || CheckEnvelope(waiting) != nil {
		t.Fatalf("waiting: %s %v", waiting, err)
	}
	if err := json.Unmarshal([]byte(waiting), &e); err != nil || e.State != EnvelopeStateWaiting {
		t.Fatalf("a waiting job's state: %s", waiting)
	}
	// From a linked machine: its answer is the data, untrusted, and the
	// waiting state is read from it.
	linked, err := control([]string{"swarm:read"}, map[string]any{"action": "get_job", "job_id": "worker/w/r", "machine": "box"})
	if err != nil || CheckEnvelope(linked) != nil {
		t.Fatalf("linked: %s %v", linked, err)
	}
	if err := json.Unmarshal([]byte(linked), &e); err != nil || e.Machine != "box" || !e.UntrustedContent || e.State != EnvelopeStateWaiting {
		t.Fatalf("linked envelope: %s", linked)
	}
	if service.calls[len(service.calls)-1] != "box/get_job" {
		t.Fatalf("linked call %v", service.calls)
	}
	// A cut page: next reads the same job at the next offset.
	service.answer = map[string]any{"json_page": "{", "page_offset": 0, "page_digest": "abc", "total_bytes": 70000, "next_page_offset": 8192}
	paged, err := control([]string{"swarm:read"}, get)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckEnvelope(paged); err != nil {
		t.Fatalf("paged envelope: %v\n%s", err, paged)
	}
	if err := json.Unmarshal([]byte(paged), &e); err != nil {
		t.Fatal(err)
	}
	next, _ := e.Next.(map[string]any)
	if next["action"] != "get_job" || next["job_id"] != "worker/w/r" || next["page_offset"] != float64(8192) || next["page_digest"] != "abc" || e.NextAction == "" {
		t.Fatalf("paged next: %s", paged)
	}
	// A cut run list: next reads on at the opaque cursor with its filters.
	service.answer = map[string]any{"runs": []any{}, "next_cursor": "opaque-2"}
	runs, err := control([]string{"swarm:read"}, map[string]any{"action": "list_runs", "worker": "w"})
	if err != nil || CheckEnvelope(runs) != nil {
		t.Fatalf("runs: %s %v", runs, err)
	}
	if err := json.Unmarshal([]byte(runs), &e); err != nil {
		t.Fatal(err)
	}
	next, _ = e.Next.(map[string]any)
	if next["action"] != "list_runs" || next["cursor"] != "opaque-2" || next["worker"] != "w" {
		t.Fatalf("runs next: %s", runs)
	}
	// A cut answer without next breaks the contract.
	if err := CheckEnvelope(`{"tool":"manage_jobs","action":"get_job","machine":"home","state":"ok","data":{"json_page":"{","page_digest":"abc","next_page_offset":8192}}`); err == nil {
		t.Fatal("a cut page without next passed the contract")
	}
	if err := CheckEnvelope(`{"tool":"manage_jobs","action":"list_runs","machine":"home","state":"ok","data":{"runs":[],"next_cursor":"opaque-2"}}`); err == nil {
		t.Fatal("a cut run list without next passed the contract")
	}
}

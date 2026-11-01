package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// This file is the general guard that an MCP (catalog) action and the HTTP
// route behind it stay in step. A catalog action runs on this machine through
// the same service its route runs (serveCatalogLinked, handleJobAutomations,
// the project routes), and with machine=NAME through a forwarder that turns
// the action's inputs into a call of the linked machine's route
// (Server.CallFleetWork, Server.CallFleetProject). The forwarders are where
// a route and its action drift apart: an input the action declares that the
// forwarder never sends, a key the forwarder sends that the action does not
// declare, a route that refuses what it is sent, or an answer field the home
// drops. The mapping from action to route is not written here: it is the
// forwarders' own dispatch (tool.FleetWorkActionAllowed,
// tool.FleetProjectActionAllowed), observed on the wire.
//
// A change to a route or an MCP action must keep these tests green; an
// allowlist entry needs a reason (AGENTS.md, "Testing a change (agents)").

// mcpParityKind is one kind of drift between an action and its route.
type mcpParityKind string

const (
	// mcpParityNotForwarded: the action declares the input but the linked
	// call never sends it (not in the body, the query or the path, under
	// its own name or any other).
	mcpParityNotForwarded mcpParityKind = "not_forwarded"
	// mcpParityNotDeclared: the linked call sends a body or query key the
	// action does not declare: a route field the MCP schema lacks.
	mcpParityNotDeclared mcpParityKind = "not_declared"
	// mcpParityRouteRejects: the linked machine's route refuses a key the
	// action declares (a strict decode's unknown field, or a catalog
	// action's "does not take").
	mcpParityRouteRejects mcpParityKind = "route_rejects"
	// mcpParityAnswerDrops: the route's answer field does not reach the
	// home's answer (the envelope's data for a linked call).
	mcpParityAnswerDrops mcpParityKind = "answer_drops"
	// mcpParityRouteUnmapped: the action reaches a route shape that
	// mcpRouteSpecs does not bind to a handler branch (field "*").
	mcpParityRouteUnmapped mcpParityKind = "route_unmapped"
	// mcpParityRouteIgnores: the forwarder sends a key the route's handler
	// never reads (derived from its source).
	mcpParityRouteIgnores mcpParityKind = "route_ignores"
	// mcpParityRouteUndeclared: the route reads a field (query parameter or
	// body field, derived from its source) that no declared input of the
	// action is sent as: a route field the MCP schema lacks.
	mcpParityRouteUndeclared mcpParityKind = "route_undeclared"
)

// mcpRouteParityAllow are the known differences, by "tool action field kind"
// (field "*" for a whole answer). Each needs its reason; a TODO marks drift
// still to fix. An entry that no longer matches fails the test, so the list
// only shrinks with the drift it records.
var mcpRouteParityAllow = map[string]string{
	// Home-side inputs: the home applies them to the linked answer itself
	// (tool.Runtime.executeFleetProjects, manageProjectsSwarmExtras); they
	// are not fields of the linked route.
	"manage_projects get detail not_forwarded":                "home-side: a linked get answers the project route's whole record; detail only trims this machine's own answer and is stripped before the call",
	"manage_projects get_task detail not_forwarded":           "home-side: the home compacts the linked task answer by detail (CompactProjectTaskAnswer)",
	"manage_projects list_tasks detail not_forwarded":         "home-side: the home compacts the linked task list (compactFleetTaskList); detail is stripped before the call",
	"manage_projects get_task include_diff not_forwarded":     "home-side: the patch is read with get_task_changes on the linked machine (manageProjectsSwarmExtras), not by the task route",
	"manage_projects get_task diff_chars not_forwarded":       "home-side: passed to get_task_changes with include_diff (manageProjectsSwarmExtras)",
	"manage_projects list_tasks include_linked not_forwarded": "home-only: lists this home's records of tasks it sent to linked machines; it has no meaning on the machine (manageProjectsSwarmExtras ignores it with machine)",
	// Resolved on the home: a workflow named by name is resolved against the
	// machine's own list (fleetWorkflowID) and its id goes in the route path.
	"manage_workflows get name not_forwarded":     "resolved on the home into the route path's workflow id (fleetWorkflowID)",
	"manage_workflows enable name not_forwarded":  "resolved on the home into the route path's workflow id (fleetWorkflowID)",
	"manage_workflows disable name not_forwarded": "resolved on the home into the route path's workflow id (fleetWorkflowID)",
	"manage_workflows delete name not_forwarded":  "resolved on the home into the route path's workflow id (fleetWorkflowID)",
	// Refused or fixed by the forwarder on purpose.
	"manage_workflows propose rotate_key not_forwarded": "refused on a linked machine: remote webhook key rotation is forbidden (callFleetWork)",
	"manage_workflows deliveries limit not_declared":    "the forwarder asks for one page of 50, as this machine's deliveries reads it; not a caller input",
	// Bounded linked summaries: a linked list answers a whitelist of fields;
	// definitions, instructions and step outcomes stay in get/inspect.
	"manage_projects list parity_probe answer_drops":       "bounded linked list (compactFleetList): whitelisted fields only",
	"manage_projects list_tasks parity_probe answer_drops": "bounded linked task page (compactFleetTaskList): page facts and list-row fields only",
	"manage_workflows list parity_probe answer_drops":      "bounded linked list (compactFleetList): whitelisted fields only",
	"manage_workers list parity_probe answer_drops":        "bounded linked worker summary (compactFleetWorkers); inspect reads one worker",
	// Home-side inputs the projects forwarder passes in the body (it sends
	// every input but the ids) and the task routes never read
	// (route_ignores, derived from the handler source).
	"manage_projects accept_task detail route_ignores":        "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects approve_task detail route_ignores":       "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects archive_task detail route_ignores":       "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects refine_task detail route_ignores":        "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects reopen_task detail route_ignores":        "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects create_task detail route_ignores":        "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects propose_task detail route_ignores":       "home-side: detail shapes the home's answer; the task route ignores it",
	"manage_projects propose_task wait_seconds route_ignores": "home-side: Swarm Control's held wait; the task route answers at once",
	// Large drift: the task create route reads title, description, prompt
	// and workspace_path; the MCP create inputs below reach it and are
	// dropped. A fix maps them as this machine's handler does.
	"manage_projects create_task brief route_ignores":      "TODO: a linked create_task's brief is not read by the task create route (it reads description/prompt); map it in CallFleetProject as the local handler does",
	"manage_projects create_task files route_ignores":      "TODO: a linked create_task's files are not read by the task create route; map them in CallFleetProject as the local handler does",
	"manage_projects create_task workspace route_ignores":  "TODO: a linked create_task's workspace is not read by the task create route (it reads workspace_path); map it in CallFleetProject",
	"manage_projects propose_task brief route_ignores":     "TODO: a linked propose_task's brief is not read by the task create route (it reads description/prompt); map it in CallFleetProject as the local handler does",
	"manage_projects propose_task files route_ignores":     "TODO: a linked propose_task's files are not read by the task create route; map them in CallFleetProject as the local handler does",
	"manage_projects propose_task workspace route_ignores": "TODO: a linked propose_task's workspace is not read by the task create route (it reads workspace_path); map it in CallFleetProject",
	// Drift found by this test and not fixed in the change that added it.
	"manage_projects propose_task task_id not_forwarded":        "TODO: declared (proposeInputs) but read neither by this machine's propose_task nor by the task route; remove it with a manage_projects definition hash change",
	"manage_projects create_task task_id not_forwarded":         "TODO: declared (proposeInputs) but read neither by this machine's create_task nor by the task route; remove it with a manage_projects definition hash change",
	"manage_projects get wait_seconds not_forwarded":            "TODO: Swarm Control's held wait for project.md is not held on a linked get, which answers at once",
	"manage_projects get_task cursor not_forwarded":             "TODO: the attempt-history page (cursor/limit) has no linked route; a linked get_task reads the task route, which takes no page",
	"manage_projects get_task limit not_forwarded":              "TODO: the attempt-history page (cursor/limit) has no linked route; a linked get_task reads the task route, which takes no page",
	"manage_projects list_tasks include_archived not_forwarded": "TODO: the linked task list route selects archived tasks only with view=archived, with no current-and-archived page; a linked list_tasks ignores include_archived",
}

// mcpParityProbeOmit are inputs left out of the answer probe because the
// forwarder refuses them on purpose (their own entry above says why), so the
// answer of the rest of the call is still checked.
var mcpParityProbeOmit = map[string][]string{
	"manage_workflows propose": {"rotate_key"},
}

// mcpParityLinkedElsewhere are the actions that reach a linked machine but
// through none of the forwarders this test drives, with the forwarder and
// the existing tests that hold their parity. A new action that reaches a
// linked machine must be served by one of the driven forwarders or be listed
// here with its reason.
var mcpParityLinkedElsewhere = map[string]string{
	"manage_fleet status":                     "linked read through manage_projects machine_status (tool.Runtime.executeManageFleet; inputs held by TestMCPRouteParityManageFleetLinkedReads in the tool package)",
	"manage_fleet activity":                   "linked read through manage_projects activity (tool.Runtime.executeManageFleet; inputs held by TestMCPRouteParityManageFleetLinkedReads; stream by TestFleetActivityPage)",
	"manage_fleet agents_at_work":             "Server.FleetAgentsAtWork (TestControlManageFleetAgentsAtWorkReturnsTheServiceAnswer, TestFleetAgentsListsActiveRunsAcrossTheFleet)",
	"manage_fleet list_runs":                  "runs manage_jobs list_runs (manageFleetListRuns), which this test drives through CallFleetWork",
	"manage_projects activity":                "Server.ReadFleetActivity (TestFleetActivityPage, TestFleetActivityScope)",
	"manage_projects machine_status":          "Server.ReadFleetMachineStatusView (TestCatalogParityOrchestratorAndControl machine_status case)",
	"manage_projects get_task_session":        "Server.ReadFleetTaskSessionView (TestCatalogParityOrchestratorAndControl get_task_session case)",
	"manage_projects read_task_session":       "Server.ReadFleetLinkedTaskSession (TestFleetLinkedTaskSessionRead)",
	"manage_projects agents_at_work":          "Server.FleetAgentsAtWork (TestManageProjectsAgentsAtWorkReturnsTheServiceAnswer)",
	"manage_projects wait_tasks":              "the home's linked watch over CallFleetProject get_task (executeFleetWaitTasks; TestRemoteWaitTasksNoticeNamesMachineAndTasks)",
	"manage_projects resolve_task_permission": "Server.ResolveTaskPermission (TestCatalogParityOrchestratorAndControl resolve_task_permission case)",
	"manage_projects list_environments":       "linked folder environment service (TestManageProjectsEnvironmentActionsLocalAndLinked)",
	"manage_projects get_environment":         "linked folder environment service (TestManageProjectsEnvironmentActionsLocalAndLinked, TestHomeManagesLinkedFolderEnvironment)",
	"manage_projects update_environment":      "linked folder environment service (TestHomeManagesLinkedFolderEnvironment, TestLinkedFolderEnvironmentAccessAndSecretRules)",
	"manage_projects rebuild_environment":     "linked folder environment service (TestManageProjectsEnvironmentActionsLocalAndLinked)",
	"manage_projects wait_environment":        "the home's environment watch (TestEnvironmentWatchWakesWithTheBuildOutcome)",
	"manage_projects wait_machine":            "the home's durable machine update watch over the linked machine's status (tool.executeMachineWatch; TestMachineWatchWakesWhenALinkedMachineUpdates, TestMachineWatchWakesWhenAnUpdateIsStuckOrFails)",
	"manage_projects cancel_watch":            "cancels the home's own watches; nothing is sent to the machine (TestEnvironmentWatchPausesAndEndsOnAMachineThatCannotHold)",
}

// mcpParityCompanions are inputs a linked call cannot go without, sent with
// every single-input probe of the action (besides its Required inputs).
var mcpParityCompanions = map[string][]string{
	// The stop route refuses a call without stopped (true or false).
	"manage_workflows stop_all": {"stopped"},
}

// mcpParityRequest is one request the home sent over the link, with the
// linked machine's answer when it was replayed there.
type mcpParityRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Status int
	Answer string
}

// mcpParityLink is a home linked to a machine "box" over the real pinned
// link. The home's record of box points at a recording listener that holds
// box's own certificate: each request is recorded, then either replayed to
// box's real link handler (its routes, auth and decoding) or answered with a
// probe field (probe mode), to see what the home keeps of an answer.
type mcpParityLink struct {
	home, box *fleetTestSwarm
	p         identity.Principal
	mu        sync.Mutex
	seen      []mcpParityRequest
	probe     bool
}

// mcpParityProbeField is the field a probe-mode answer carries.
const mcpParityProbeField = "parity_probe"

func newMCPParityLink(t *testing.T) *mcpParityLink {
	t.Helper()
	home := newFleetTestSwarm(t, "parity-home")
	t.Cleanup(home.cleanup)
	box := newFleetTestSwarm(t, "parity-box")
	t.Cleanup(box.cleanup)
	body, _ := json.Marshal(map[string]string{"code": box.invite(t), "name": "box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	fleetActivityTestSetWork(t, home, "box", pebblestore.RuleAuto)
	cert, _, err := box.s.fleetCertificate()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	link := &mcpParityLink{home: home, box: box, p: identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}}
	server := &http.Server{Handler: link.handler(box.s.fleetLinkHandler()), TLSConfig: fleet.ServerTLS(cert), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = server.Close() })
	machine, found, err := home.s.fleet.GetMachine("box")
	if err != nil || !found {
		t.Fatalf("linked machine record: %v %v", found, err)
	}
	machine.Address = ln.Addr().String()
	if err := home.s.fleet.PutMachine(machine); err != nil {
		t.Fatal(err)
	}
	return link
}

func (l *mcpParityLink) handler(box http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		// The home's own lookups before the call it makes: a workflow named
		// by id or name is resolved against the machine's list
		// (fleetWorkflowID), and a linked worker list reads each worker's
		// runs (compactFleetWorkers). They are answered here, not recorded.
		if r.Method == http.MethodGet && r.URL.Path == JobAutomationsPath && r.URL.RawQuery == "" {
			writeJSON(w, http.StatusOK, map[string]any{"automations": []any{map[string]any{"id": "pf-id", "name": "pf-name", "state": "active"}}})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, WorkersPath+"/") && strings.HasSuffix(r.URL.Path, "/runs") {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}})
			return
		}
		record := mcpParityRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()}
		if len(bytes.TrimSpace(raw)) > 0 {
			_ = json.Unmarshal(raw, &record.Body)
		}
		// What was sent is recorded before the machine answers, so a route
		// this test daemon cannot serve (no provider registry, no workflow
		// store) still shows what reached it.
		l.mu.Lock()
		probe := l.probe
		index := len(l.seen)
		l.seen = append(l.seen, record)
		l.mu.Unlock()
		if probe {
			l.answered(index, http.StatusOK, "")
			writeJSON(w, http.StatusOK, map[string]any{mcpParityProbeField: "probe"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		recorder := httptest.NewRecorder()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					recorder = httptest.NewRecorder()
					writeError(recorder, http.StatusServiceUnavailable, fmt.Errorf("test machine cannot serve this route: %v", recovered))
				}
			}()
			box.ServeHTTP(recorder, r)
		}()
		l.answered(index, recorder.Code, recorder.Body.String())
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
}

// answered keeps the machine's answer to the recorded request.
func (l *mcpParityLink) answered(index, status int, answer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if index < len(l.seen) {
		l.seen[index].Status, l.seen[index].Answer = status, answer
	}
}

// call runs one linked call through the forwarder that serves the action and
// returns the requests it sent, the home's answer and its error.
func (l *mcpParityLink) call(t *testing.T, forwarder, toolName, action string, args map[string]any, probe bool) ([]mcpParityRequest, json.RawMessage, error) {
	t.Helper()
	l.mu.Lock()
	l.seen, l.probe = nil, probe
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	call := map[string]any{"action": action}
	for key, value := range args {
		call[key] = value
	}
	var out json.RawMessage
	var err error
	switch forwarder {
	case mcpParityProjects:
		out, err = l.home.s.CallFleetProject(ctx, l.p, "box", action, call)
	case mcpParityCatalog:
		out, err = l.home.s.CallFleetCatalog(ctx, l.p, "box", toolName, call)
	default:
		out, err = l.home.s.CallFleetWork(ctx, l.p, "box", toolName, action, call)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]mcpParityRequest(nil), l.seen...), out, err
}

const (
	mcpParityWork     = "CallFleetWork"
	mcpParityProjects = "CallFleetProject"
	mcpParityCatalog  = "CallFleetCatalog"
)

// mcpParityForwarder is the forwarder that runs the action with machine=, from
// the forwarders' own dispatch; "" when none serves it.
func mcpParityForwarder(toolName, action string) string {
	if toolName == "manage_projects" && tool.FleetProjectActionAllowed(action) {
		return mcpParityProjects
	}
	if catalogLinkedTools[toolName] {
		return mcpParityCatalog
	}
	if tool.FleetWorkActionAllowed(toolName, action) {
		return mcpParityWork
	}
	return ""
}

// mcpParityLinkedActions are the catalog actions that reach a linked machine
// (reach any or linked), by "tool action".
func mcpParityLinkedActions() []struct {
	catalog tool.CatalogTool
	action  tool.CatalogAction
} {
	var out []struct {
		catalog tool.CatalogTool
		action  tool.CatalogAction
	}
	for _, catalog := range tool.Catalog() {
		for _, action := range catalog.Actions {
			if action.Reach == tool.CatalogReachHome {
				continue
			}
			out = append(out, struct {
				catalog tool.CatalogTool
				action  tool.CatalogAction
			}{catalog, action})
		}
	}
	return out
}

// mcpParityInputs are the inputs an action declares (its own and its Swarm
// Control inputs), without machine, which names the link.
func mcpParityInputs(action tool.CatalogAction) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		if name != "machine" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// mcpParitySample is a value of the field's schema that names the field: a
// string "pf-<field>", a distinct integer, true, an array of one sample, an
// object with its required properties. A field that takes an enum gets its
// first value.
func mcpParitySample(name string, schema map[string]any, index int) any {
	if values, ok := schema["enum"].([]string); ok && len(values) > 0 {
		return values[0]
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	kind, _ := schema["type"].(string)
	switch kind {
	case "integer", "number":
		low, high := 1.0, 1e9
		if v, ok := mcpParityNumber(schema["minimum"]); ok && v > low {
			low = v
		}
		if v, ok := mcpParityNumber(schema["maximum"]); ok {
			high = v
		}
		// Small and distinct per input, inside the range a linked route
		// takes (a task list page is at most 50).
		value := low + float64(index)
		if value > high {
			value = high
		}
		return value
	case "boolean":
		return true
	case "array":
		items, _ := schema["items"].(map[string]any)
		return []any{mcpParitySample(name, items, index)}
	case "object":
		properties, _ := schema["properties"].(map[string]any)
		out := map[string]any{}
		required, _ := schema["required"].([]string)
		for i, key := range required {
			property, _ := properties[key].(map[string]any)
			out[key] = mcpParitySample(name+"."+key, property, index+i)
		}
		if len(out) == 0 {
			out["pf"] = "pf-" + name
		}
		return out
	}
	return "pf-" + name
}

func mcpParityNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

// mcpParityArgs are sample values for the named inputs of the action.
func mcpParityArgs(catalog tool.CatalogTool, names []string) map[string]any {
	args := map[string]any{}
	for i, name := range names {
		field, _ := catalog.Field(name)
		args[name] = mcpParitySample(name, field.Schema, i+2)
	}
	return args
}

// mcpParityForwarded reports how a request carried the input: by its own name
// in the body or query, in the path, or under another key (a rename) with the
// same value. "" when it did not.
func mcpParityForwarded(requests []mcpParityRequest, name string, value any) string {
	text := fmt.Sprint(value)
	encoded, _ := json.Marshal(value)
	for _, request := range requests {
		if _, ok := request.Body[name]; ok {
			return "body"
		}
		if _, ok := request.Query[name]; ok {
			return "query"
		}
	}
	if _, flag := value.(bool); flag {
		// true names no field: another key's true is not this input.
		return ""
	}
	for _, request := range requests {
		if s, ok := value.(string); ok && len(s) > 3 && strings.Contains(request.Path, url.PathEscape(s)) {
			return "path"
		}
		for key, got := range request.Body {
			if encodedGot, _ := json.Marshal(got); bytes.Equal(encodedGot, encoded) {
				return "body:" + key
			}
		}
		for key, got := range request.Query {
			if len(got) == 1 && got[0] == text {
				return "query:" + key
			}
		}
	}
	return ""
}

// mcpParityRouteRefusal reports a linked route that refused a key it was sent.
var mcpParityRouteRefusal = regexp.MustCompile(`unknown field "([^"]+)"|does not take ([a-z_, ]+)`)

// Purpose:
//   - Requirement: every catalog action that runs on a linked machine
//     (machine=) reaches that machine's route with the inputs the action
//     declares, sends nothing the action does not declare, is accepted by
//     the route it reaches, and carries the route's answer back to the
//     caller. Drift between an MCP action and its HTTP route fails here
//     unless mcpRouteParityAllow records it with a reason.
//   - Threat/regression: a field added to a route or a forwarder but not
//     to the MCP schema (or the reverse) — an input an AI passes that a
//     linked machine silently never sees, a key only the forwarder knows,
//     a route that refuses what the home forwards, or an answer field the
//     home drops.
//   - Boundary/authority: tool.Catalog (declared inputs), the forwarders'
//     own dispatch (tool.FleetWorkActionAllowed, FleetProjectActionAllowed),
//     Server.CallFleetWork and Server.CallFleetProject over the real pinned
//     link, and the linked machine's real link handler and routes.
//   - Narrowest layer: the wire between the two daemons, recorded by a
//     listener holding the linked machine's own certificate; the forwarders
//     are called directly, so key levels and the person's approval (held by
//     their own tests) do not hide what is forwarded. No provider runs.
func TestMCPRouteParityLinkedForwarding(t *testing.T) {
	link := newMCPParityLink(t)
	var drift []string
	matched := map[string]bool{}
	note := func(toolName, action, field string, kind mcpParityKind, detail string) {
		key := fmt.Sprintf("%s %s %s %s", toolName, action, field, kind)
		if _, ok := mcpRouteParityAllow[key]; ok {
			matched[key] = true
			return
		}
		drift = append(drift, fmt.Sprintf("%q: %s", key, detail))
	}
	elsewhere := map[string]bool{}
	routeOnlyUsed, shapeSeen := map[string]bool{}, map[string]bool{}
	for _, entry := range mcpParityLinkedActions() {
		catalog, action := entry.catalog, entry.action
		id := catalog.Name + " " + action.Action
		forwarder := mcpParityForwarder(catalog.Name, action.Action)
		if forwarder == "" {
			if _, ok := mcpParityLinkedElsewhere[id]; !ok {
				drift = append(drift, fmt.Sprintf("%s reaches a linked machine (reach %s) but none of CallFleetWork, CallFleetProject or CallFleetCatalog serves it: serve it there or list it in mcpParityLinkedElsewhere with the tests that hold it", id, action.Reach))
			}
			elsewhere[id] = true
			continue
		}
		if _, ok := mcpParityLinkedElsewhere[id]; ok {
			drift = append(drift, fmt.Sprintf("%s is listed in mcpParityLinkedElsewhere but %s serves it: remove the entry", id, forwarder))
		}
		inputs := mcpParityInputs(action)
		all := mcpParityArgs(catalog, inputs)
		requests, _, err := link.call(t, forwarder, catalog.Name, action.Action, all, false)
		how := map[string]string{}
		for _, name := range inputs {
			how[name] = mcpParityForwarded(requests, name, all[name])
		}
		// An input missing from the call with every input may be one of a
		// pair (delivery_id reads one delivery instead of a workflow's):
		// it is probed alone, with the action's required inputs.
		for _, name := range inputs {
			if how[name] != "" {
				continue
			}
			alone := append(append([]string(nil), action.Required...), mcpParityCompanions[id]...)
			alone = append(alone, name)
			args := map[string]any{}
			for _, key := range alone {
				args[key] = all[key]
			}
			single, _, singleErr := link.call(t, forwarder, catalog.Name, action.Action, args, false)
			if how[name] = mcpParityForwarded(single, name, all[name]); how[name] == "" {
				detail := "never sent to the linked route"
				if singleErr != nil {
					detail += " (alone: " + singleErr.Error() + ")"
				} else if err != nil {
					detail += " (with every input: " + err.Error() + ")"
				}
				note(catalog.Name, action.Action, name, mcpParityNotForwarded, detail)
			}
			requests = append(requests, single...)
		}
		declared := map[string]bool{"action": true}
		for _, name := range inputs {
			declared[name] = true
			if strings.HasPrefix(how[name], "body:") || strings.HasPrefix(how[name], "query:") {
				declared[strings.SplitN(how[name], ":", 2)[1]] = true
			}
		}
		sentExtra := map[string]string{}
		for _, request := range requests {
			for key := range request.Body {
				if !declared[key] {
					sentExtra[key] = request.Method + " " + request.Path + " body"
				}
			}
			for key := range request.Query {
				if !declared[key] {
					sentExtra[key] = request.Method + " " + request.Path + " query"
				}
			}
			if request.Status == http.StatusBadRequest || request.Status == http.StatusForbidden {
				if found := mcpParityRouteRefusal.FindStringSubmatch(request.Answer); found != nil {
					fields := found[1]
					if fields == "" {
						fields = found[2]
					}
					for _, field := range strings.Split(fields, ",") {
						if field = strings.TrimSpace(field); field != "" {
							note(catalog.Name, action.Action, field, mcpParityRouteRejects, request.Method+" "+request.Path+": "+strings.TrimSpace(request.Answer))
						}
					}
				}
			}
			if request.Status == http.StatusNotFound && strings.Contains(request.Answer, "no "+catalog.Name+" action") {
				note(catalog.Name, action.Action, "*", mcpParityRouteRejects, request.Path+" serves no such action")
			}
		}
		for _, key := range mcpParitySortedKeys(sentExtra) {
			note(catalog.Name, action.Action, key, mcpParityNotDeclared, "sent as "+sentExtra[key]+" but the action does not declare it")
		}
		// What the route accepts (derived from its source,
		// mcp_route_accepts_test.go): every input the forwarder sends is a
		// field the route reads, and every field the route reads is sent
		// from a declared input, unless recorded as route-only.
		shapes := map[string]map[string]bool{}
		for _, request := range requests {
			if mcpIsCatalogRoute(request.Path) {
				continue
			}
			shape := mcpRouteShape(request.Method, request.Path)
			shapeSeen[shape] = true
			if shapes[shape] == nil {
				shapes[shape] = map[string]bool{}
			}
			for key := range request.Body {
				shapes[shape][key] = true
			}
			for key := range request.Query {
				shapes[shape][key] = true
			}
		}
		for _, shape := range mcpParitySortedKeys(shapes) {
			spec, fields, ok, err := mcpRouteAccepts(shape)
			switch {
			case !ok:
				note(catalog.Name, action.Action, "*", mcpParityRouteUnmapped, shape+" has no mcpRouteSpecs entry naming the handler branch that serves it")
				continue
			case err != nil:
				drift = append(drift, fmt.Sprintf("%s: deriving %s: %v", id, shape, err))
				continue
			case spec.Catalog != "":
				continue
			}
			accepted := map[string]bool{}
			for key := range fields.Query {
				accepted[key] = true
			}
			for key := range fields.Body {
				accepted[key] = true
			}
			for _, key := range mcpParitySortedKeys(shapes[shape]) {
				if !accepted[key] {
					note(catalog.Name, action.Action, key, mcpParityRouteIgnores, shape+" is sent "+key+" but its handler never reads it")
				}
			}
			for _, key := range mcpParitySortedKeys(accepted) {
				if declared[key] {
					continue
				}
				if reason := mcpRouteOnlyField(shape, key); reason != "" {
					routeOnlyUsed[shape+" "+key] = true
					continue
				}
				note(catalog.Name, action.Action, key, mcpParityRouteUndeclared, shape+" reads "+key+" but the action declares no input sent as it")
			}
		}
		// A key only the forwarder knows: the call also carries every other
		// catalog field (canaries). A forwarder that passes all of them on
		// passes the whole call (the route's service keeps its declared
		// inputs); a forwarder that picks keys must pick declared ones only.
		canaries := mcpParityArgs(catalog, nil)
		for name, value := range mcpParityCanaries(declared) {
			canaries[name] = value
		}
		if len(canaries) > 0 {
			call := map[string]any{}
			for key, value := range probeArgsFor(all, mcpParityProbeOmit[id]) {
				call[key] = value
			}
			for key, value := range canaries {
				call[key] = value
			}
			sent, _, _ := link.call(t, forwarder, catalog.Name, action.Action, call, true)
			passed := map[string]string{}
			for _, request := range sent {
				for key := range canaries {
					if _, ok := request.Body[key]; ok {
						passed[key] = request.Method + " " + request.Path + " body"
					}
					if _, ok := request.Query[key]; ok {
						passed[key] = request.Method + " " + request.Path + " query"
					}
				}
			}
			if len(passed) < len(canaries) {
				for _, key := range mcpParitySortedKeys(passed) {
					note(catalog.Name, action.Action, key, mcpParityNotDeclared, "the forwarder picks "+key+" (sent as "+passed[key]+") but the action does not declare it")
				}
			}
		}
		// The answer: a probe field in the route's answer reaches the home's.
		_, out, probeErr := link.call(t, forwarder, catalog.Name, action.Action, probeArgsFor(all, mcpParityProbeOmit[id]), true)
		var answer map[string]any
		switch {
		case probeErr != nil:
			note(catalog.Name, action.Action, "*", mcpParityAnswerDrops, "the home refused the route's answer: "+probeErr.Error())
		case json.Unmarshal(out, &answer) != nil:
			note(catalog.Name, action.Action, "*", mcpParityAnswerDrops, "the home's answer is not an object: "+string(out))
		case answer[mcpParityProbeField] == nil:
			note(catalog.Name, action.Action, mcpParityProbeField, mcpParityAnswerDrops, "a route answer field is not in the home's answer: "+string(out))
		}
	}
	for id := range mcpParityLinkedElsewhere {
		if !elsewhere[id] {
			drift = append(drift, fmt.Sprintf("mcpParityLinkedElsewhere lists %s, which is not a catalog action that reaches a linked machine outside the two forwarders: remove it", id))
		}
	}
	for _, shape := range mcpParitySortedKeys(mcpRouteParityRouteOnly) {
		for _, group := range mcpRouteParityRouteOnly[shape] {
			for _, field := range strings.Fields(group.Fields) {
				if !routeOnlyUsed[shape+" "+field] {
					drift = append(drift, fmt.Sprintf("mcpRouteParityRouteOnly %s lists %s, which no action reaching it meets as an undeclared route field: remove it", shape, field))
				}
			}
		}
	}
	for shape := range mcpRouteSpecs {
		if !shapeSeen[shape] {
			drift = append(drift, fmt.Sprintf("mcpRouteSpecs entry %q is reached by no linked action: remove it", shape))
		}
	}
	for key := range mcpRouteParityAllow {
		if !matched[key] {
			drift = append(drift, fmt.Sprintf("mcpRouteParityAllow entry %q no longer matches any drift: remove it", key))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("MCP action / HTTP route drift (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}

// probeArgsFor is args without the omitted inputs.
func probeArgsFor(args map[string]any, omit []string) map[string]any {
	out := map[string]any{}
	for key, value := range args {
		out[key] = value
	}
	for _, key := range omit {
		delete(out, key)
	}
	return out
}

// mcpParityCanaries are sample values for every catalog field (of any tool)
// the action does not declare: keys a forwarder could know of on its own.
func mcpParityCanaries(declared map[string]bool) map[string]any {
	out := map[string]any{}
	for _, catalog := range tool.Catalog() {
		for i, field := range catalog.Fields {
			if declared[field.Name] || field.Name == "machine" || field.Name == "rotate_key" {
				continue
			}
			if _, ok := out[field.Name]; !ok {
				out[field.Name] = mcpParitySample(field.Name, field.Schema, 40+i)
			}
		}
	}
	return out
}

func mcpParitySortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// mcpHelpIdentifier is a field-like identifier in help text: lower-case words
// joined by underscores (job_id, wait_seconds). One-word field names (name,
// worker) cannot be told from prose and are not read as field mentions.
var mcpHelpIdentifier = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// mcpHelpTextAllow are help-text differences, by "tool action field kind"
// (action "*" for the tool's own description; kind undocumented or
// unknown_field), each with its reason.
var mcpHelpTextAllow = map[string]string{
	"manage_workflows propose inputs undocumented":                     "the workflow definition's fields are documented by manage_workflows help (the workflow guide), which propose's description points to",
	"manage_workflows propose result undocumented":                     "the workflow definition's fields are documented by manage_workflows help (the workflow guide), which propose's description points to",
	"manage_workflows propose retries undocumented":                    "the workflow definition's fields are documented by manage_workflows help (the workflow guide), which propose's description points to",
	"manage_workflows propose scope undocumented":                      "the workflow definition's fields are documented by manage_workflows help (the workflow guide), which propose's description points to",
	"manage_media generate wait_seconds undocumented":                  "TODO: the tool description says a media job is read 'optionally waiting up to 30 seconds' without naming the field; give wait_seconds its own description",
	"manage_media get wait_seconds undocumented":                       "TODO: the tool description says get waits 'up to 30 seconds' without naming the field; give wait_seconds its own description",
	"manage_media retry wait_seconds undocumented":                     "TODO: the tool description says a media job is read 'optionally waiting up to 30 seconds' without naming the field; give wait_seconds its own description",
	"manage_projects propose_task feature_size undocumented":           "TODO: small or big has no description; describe it with a manage_projects definition hash change",
	"manage_sessions archive stop_active undocumented":                 "TODO: give stop_active its own description; archive's help does not say what it does",
	"manage_sessions archive expected_updated_at_by_id undocumented":   "TODO: give expected_updated_at_by_id its own description (an object of session id to the updated_at each change expects)",
	"manage_sessions unarchive expected_updated_at_by_id undocumented": "TODO: give expected_updated_at_by_id its own description (an object of session id to the updated_at each change expects)",
	"manage_sessions send_message trigger_run undocumented":            "TODO: give trigger_run its own description; it is read with trigger (catalogLinkedStartsRun), which the help names",
}

// mcpHelpSelfDescribing are inputs whose name says what they take: standard
// ids, paging, text and selection inputs. An action need not repeat them in
// its help.
var mcpHelpSelfDescribing = map[string]bool{
	"limit": true, "cursor": true, "query": true, "title": true, "description": true, "state": true, "since": true, "until": true, "count": true,
	"project": true, "project_id": true, "task_id": true, "session_id": true, "session_ids": true,
	"workspace": true, "workspace_path": true, "workspace_id": true, "workspace_generation": true, "workspace_name": true,
	"content_offset": true, "message_seq": true, "after_seq": true, "before_seq": true, "around_seq": true,
	"client_request_id": true, "idempotency_key": true, "include_archived": true,
	"aspect_ratio": true, "resolution": true, "duration_seconds": true,
}

// mcpHelpTextProse are identifiers help text may name that are not inputs:
// answer fields, statuses, milestones and record names, with the reason they
// appear.
var mcpHelpTextProse = func() map[string]string {
	out := map[string]string{"swarm_workflow": "the portable workflow format's version key (export, import)"}
	for _, word := range []string{"job_id", "next_cursor", "json_page", "retry_of", "pending_request", "pending_proposals", "waiting_on", "created_at", "steps_result", "workflow_steps_result", "reopen_failure", "summary_source", "result_truncated", "read_full_args", "task_progress", "durable_log", "delivery_attempt_id", "delivery_run", "previous_run"} {
		out[word] = "an answer field the help tells the caller to read"
	}
	for _, word := range []string{"needs_input", "needs_review", "needs_you", "blocked_infra", "workflow_failed", "timed_out", "agent_stall", "long_command", "no_activity", "sleep_polling", "up_to_date"} {
		out[word] = "a status, milestone or progress kind an answer carries"
	}
	return out
}()

// mcpHelpText is the help an MCP client is shown for one action: the tool's
// description, the action's description with its required-inputs note, and
// the descriptions of the action's input properties.
func mcpHelpText(catalog tool.CatalogTool, action tool.CatalogAction) string {
	parts := []string{catalog.Description, action.Description + action.RequirementNote()}
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		if field, ok := catalog.Field(name); ok {
			parts = append(parts, mcpSchemaDescriptions(field.Schema)...)
		}
	}
	return strings.Join(parts, "\n")
}

// mcpSchemaDescriptions are a schema's description and its properties' and
// items' descriptions.
func mcpSchemaDescriptions(schema map[string]any) []string {
	var out []string
	if text, _ := schema["description"].(string); text != "" {
		out = append(out, text)
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, key := range mcpParitySortedKeys(properties) {
			if property, ok := properties[key].(map[string]any); ok {
				out = append(out, mcpSchemaDescriptions(property)...)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		out = append(out, mcpSchemaDescriptions(items)...)
	}
	return out
}

// mcpSchemaProperties adds the property names nested in a schema.
func mcpSchemaProperties(schema map[string]any, into map[string]bool) {
	if properties, ok := schema["properties"].(map[string]any); ok {
		for key, value := range properties {
			into[key] = true
			if property, ok := value.(map[string]any); ok {
				mcpSchemaProperties(property, into)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		mcpSchemaProperties(items, into)
	}
}

// mcpHelpMentions reports whether text names the identifier as a word.
func mcpHelpMentions(text, name string) bool {
	return regexp.MustCompile(`(^|[^a-z0-9_])` + regexp.QuoteMeta(name) + `($|[^a-z0-9_])`).MatchString(text)
}

// Purpose:
//   - Requirement: an MCP action's help (what Swarm Control and the
//     Orchestrator show for it) documents every input its schema declares,
//     and names no field-like identifier that is neither an input of the
//     tool, an action, a tool, nor a listed answer field or status.
//   - Threat/regression: help describing old behaviour — an input renamed
//     or removed while the description still tells the AI to pass it, or a
//     new input with no word of help — so an AI calls the action wrong.
//   - Boundary/authority: tool.Catalog (CatalogTool.Description,
//     CatalogAction.Description, RequirementNote and each field's schema
//     description), the text both surfaces are generated from.
//   - Narrowest layer: the declarations; nothing else writes help.
func TestMCPHelpTextMatchesSchema(t *testing.T) {
	var drift []string
	matched := map[string]bool{}
	note := func(toolName, action, field, kind, detail string) {
		key := fmt.Sprintf("%s %s %s %s", toolName, action, field, kind)
		if _, ok := mcpHelpTextAllow[key]; ok {
			matched[key] = true
			return
		}
		drift = append(drift, fmt.Sprintf("%q: %s", key, detail))
	}
	// Names help may always use: catalog tools and their actions (help
	// points from one action to another), and the removed swarm_* tools an
	// action names as the tool it replaced (docs/tool-catalog.md).
	names := map[string]bool{}
	for _, catalog := range tool.Catalog() {
		names[catalog.Name] = true
		for _, action := range catalog.Actions {
			names[action.Action] = true
		}
	}
	for _, removed := range controlMCPFrozenSwarmTools {
		names[removed] = true
	}
	for _, catalog := range tool.Catalog() {
		// The tool's fields, with the properties nested in them
		// (inputs[].asset_id, steps[].for_each).
		known := map[string]bool{}
		for _, field := range catalog.Fields {
			known[field.Name] = true
			mcpSchemaProperties(field.Schema, known)
		}
		check := func(action, text string, own map[string]bool) {
			for _, word := range mcpHelpIdentifier.FindAllString(text, -1) {
				if own[word] || known[word] || names[word] {
					continue
				}
				if _, ok := mcpHelpTextProse[word]; ok {
					matched["prose "+word] = true
					continue
				}
				note(catalog.Name, action, word, "unknown_field", "help names "+word+", which is no input of "+catalog.Name+", no action and no listed answer field")
			}
		}
		check("*", catalog.Description, nil)
		for _, action := range catalog.Actions {
			if len(action.Profiles) == 0 {
				// Not shown to any caller (an alias, the person's own step).
				continue
			}
			help := mcpHelpText(catalog, action)
			own := map[string]bool{}
			for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
				own[name] = true
				field, _ := catalog.Field(name)
				if mcpHelpSelfDescribing[name] || len(mcpSchemaDescriptions(field.Schema)) > 0 || mcpHelpMentions(help, name) {
					continue
				}
				note(catalog.Name, action.Action, name, "undocumented", "the input has no description and its action's help does not name it")
			}
			check(action.Action, action.Description+action.RequirementNote(), own)
		}
	}
	for key := range mcpHelpTextAllow {
		if !matched[key] {
			drift = append(drift, fmt.Sprintf("mcpHelpTextAllow entry %q no longer matches: remove it", key))
		}
	}
	for word := range mcpHelpTextProse {
		if !matched["prose "+word] {
			drift = append(drift, fmt.Sprintf("mcpHelpTextProse entry %q is named by no help text: remove it", word))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("MCP help text / schema drift (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}

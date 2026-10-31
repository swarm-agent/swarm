package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// catalogEnvelopeCheck checks one answer against the envelope contract and
// the action's declared states (ok, partial and CatalogAction.States).
func catalogEnvelopeCheck(t *testing.T, name, answer string) map[string]any {
	t.Helper()
	if err := tool.CheckEnvelope(answer); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, answer)
	}
	envelope := parityDecode(t, answer)
	catalog, _ := tool.CatalogToolNamed(envelope["tool"].(string))
	action, _ := catalog.Action(envelope["action"].(string))
	state := envelope["state"].(string)
	allowed := state == tool.EnvelopeStateOK || state == tool.EnvelopeStatePartial
	for _, declared := range action.States {
		allowed = allowed || state == declared
	}
	if !allowed {
		t.Fatalf("%s: state %q is not one %s declares", name, state, action.Action)
	}
	return envelope
}

// catalogFollow runs an envelope's next through both surfaces and returns
// the Swarm Control envelope, after checking the Orchestrator gets the same.
func catalogFollow(t *testing.T, h *catalogParityHarness, name string, next map[string]any) map[string]any {
	t.Helper()
	controlOut, controlErr := h.control(t, catalogParityFullKey, next)
	orchOut, orchErr := h.orchestrator(t, next)
	if controlErr != "" || orchErr != nil {
		t.Fatalf("%s: following next %v failed: %s / %v", name, next, controlErr, orchErr)
	}
	a, _ := json.Marshal(parityStrip(parityDecode(t, orchOut)))
	b, _ := json.Marshal(parityStrip(parityDecode(t, controlOut)))
	if string(a) != string(b) {
		t.Fatalf("%s: next answers differ\n%s\n%s", name, a, b)
	}
	return catalogEnvelopeCheck(t, name+" next", controlOut)
}

// Purpose: every manage_projects action, through the Orchestrator and
// through Swarm Control, here and on a linked machine, answers in an
// envelope that follows one contract (docs/tool-catalog.md): state is the
// call's outcome from the documented set (never a task's run status), and
// only a state the action declares; next_action is never "none" (omitted
// when nothing is to do) and is present for the reads that lead somewhere;
// next has one shape, the exact arguments of the read that continues; and a
// cut answer always carries next: a cut list (here and on the linked
// machine, whose cursor comes as a string) and a cut task report (get_task
// and Swarm Control's wait_tasks), each next followed to the rest it
// promises. Threat: a client that must special-case each action's state,
// next step or continuation, or cannot read the rest of a cut answer.
// Boundary: tool.Runtime.manageProjectsEnvelope via
// Runtime.ExecuteForWorkspaceScopeWithRuntime (Orchestrator) and
// controlMCPCall.callTool (MCP), over two in-process daemons linked like
// production; tool.CheckEnvelope states the contract. No provider runs.
func TestCatalogEnvelopeContractEveryAction(t *testing.T) {
	h := newCatalogParityHarness(t)
	all := catalogParityCases()
	checked := map[string]bool{}
	// The reads that always lead somewhere carry next_action.
	leads := map[string]map[string]bool{
		"manage_projects":  {"list": true, "get": true, "list_tasks": true, "list_machines": true, "list_linked_tasks": true, "agents_at_work": true, "machine_status": true, "get_task_session": true, "activity": true, "read_task_session": true, "wait_tasks": true},
		"manage_workflows": {"list": true, "get": true, "deliveries": true, "history": true, "export": true},
		"manage_fleet":     {"list_machines": true, "status": true, "agents_at_work": true, "activity": true, "list_runs": true, "list_linked_tasks": true},
	}
	for _, toolName := range catalogParityToolNames(all) {
		for _, action := range catalogParityActionNames(all[toolName]) {
			for _, c := range all[toolName][action] {
				if c.compare == parityNotServed {
					continue
				}
				name := action
				if toolName != "manage_projects" {
					name = toolName + " " + action
				}
				if machine, _ := c.orchestrator["machine"].(string); machine != "" {
					name += "@" + machine
				}
				c = h.resolveCase(t, c)
				if out, err := h.orchestratorTool(t, toolName, c.orchestrator); err == nil && c.compare != parityControlOnly {
					catalogEnvelopeCheck(t, name+" (Orchestrator)", out)
					checked[name] = true
				}
				if out, refused := h.controlTool(t, catalogParityFullKey, toolName, c.control); refused == "" {
					envelope := catalogEnvelopeCheck(t, name+" (Swarm Control)", out)
					checked[name] = true
					if leads[toolName][action] {
						if text, _ := envelope["next_action"].(string); text == "" {
							t.Fatalf("%s: a read with a next step has no next_action: %s", name, out)
						}
					}
				}
			}
		}
	}
	// Every manage_workflows action answered an envelope, here and (but help)
	// on the linked machine.
	workflows, _ := tool.CatalogToolNamed("manage_workflows")
	for _, action := range workflows.Actions {
		for _, name := range []string{"manage_workflows " + action.Action, "manage_workflows " + action.Action + "@box"} {
			if strings.HasSuffix(name, "@box") && action.Reach != tool.CatalogReachAny {
				continue
			}
			if !checked[name] {
				t.Fatalf("%s answered no envelope to check", name)
			}
		}
	}
	for _, read := range []string{"list", "list@box", "list_tasks", "list_tasks@box", "get_task", "get_task@box", "list_machines", "agents_at_work", "agents_at_work@box", "machine_status@box", "read_task_session@box", "wait_tasks", "wait_tasks@box"} {
		if !checked[read] {
			t.Fatalf("%s answered no envelope to check", read)
		}
	}

	// A cut list, here: next is list_tasks again at the next cursor with
	// the call's own filters, and it reads the rest.
	local := map[string]any{"action": "list_tasks", "project_id": "home-project", "scope": "all", "limit": float64(2)}
	out, refused := h.control(t, catalogParityFullKey, local)
	if refused != "" {
		t.Fatal(refused)
	}
	envelope := catalogEnvelopeCheck(t, "cut list_tasks", out)
	next, _ := envelope["next"].(map[string]any)
	if next["action"] != "list_tasks" || next["cursor"] != float64(2) || next["project_id"] != "home-project" || next["scope"] != "all" || next["limit"] != float64(2) {
		t.Fatalf("cut list_tasks next: %s", out)
	}
	rest := catalogFollow(t, h, "cut list_tasks", next)
	if data := envelopeDataMap(rest); data["cursor"] != float64(2) || len(data["tasks"].([]any)) == 0 {
		t.Fatalf("list_tasks next did not read on: %v", data)
	}

	// The linked machine's cut list: its string cursor becomes the integer
	// cursor input, on that machine.
	remote := map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project", "limit": float64(1)}
	out, refused = h.control(t, catalogParityFullKey, remote)
	if refused != "" {
		t.Fatal(refused)
	}
	envelope = catalogEnvelopeCheck(t, "cut list_tasks@box", out)
	next, _ = envelope["next"].(map[string]any)
	if next["action"] != "list_tasks" || next["machine"] != "box" || next["cursor"] != float64(1) || next["project_id"] != "remote-project" {
		t.Fatalf("cut list_tasks@box next: %s", out)
	}
	rest = catalogFollow(t, h, "cut list_tasks@box", next)
	if rows, _ := envelopeDataMap(rest)["tasks"].([]any); len(rows) != 1 {
		t.Fatalf("list_tasks@box next did not read on: %v", rest)
	}

	// A cut task report: get_task's next is the full record at the
	// report's attempt, whose summary is the whole report; Swarm Control's
	// wait_tasks carries the same next.
	report := "Report: " + strings.Repeat("the parser now keeps comments and the tests cover nested blocks. ", 120)
	store := h.home.s.sessions.Store()
	if err := store.PutProjectTask(h.home.account, &pebblestore.ProjectTaskRecord{ID: "long", ProjectID: "home-project", AccountID: h.home.account, Title: "Long report", Agent: "coder", Status: "completed", Revision: 1, SessionID: "hs-long", ActiveAttemptID: "attempt-long-2",
		Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-long-1", SessionID: "hs-long-0", Role: "coder", Status: "superseded", Summary: "first"}, {ID: "attempt-long-2", SessionID: "hs-long", Role: "coder", Status: "completed", Summary: report}}}); err != nil {
		t.Fatal(err)
	}
	out, refused = h.control(t, catalogParityFullKey, map[string]any{"action": "get_task", "project_id": "home-project", "task_id": "long"})
	if refused != "" {
		t.Fatal(refused)
	}
	envelope = catalogEnvelopeCheck(t, "cut get_task", out)
	next, _ = envelope["next"].(map[string]any)
	if next["action"] != "get_task" || next["detail"] != "full" || next["cursor"] != float64(1) || next["limit"] != float64(1) || next["task_id"] != "long" || !strings.Contains(out, `"result_truncated":true`) {
		t.Fatalf("cut get_task next: %s", out)
	}
	rest = catalogFollow(t, h, "cut get_task", next)
	full, _ := envelopeDataMap(rest)["task"].(map[string]any)
	attempts, _ := full["attempts"].([]any)
	if len(attempts) != 1 || attempts[0].(map[string]any)["summary"] != report {
		t.Fatalf("get_task next does not read the full report: %v", full["attempts"])
	}
	out, refused = h.control(t, catalogParityFullKey, map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_ids": []any{"long"}, "wait_seconds": float64(1)})
	if refused != "" {
		t.Fatal(refused)
	}
	envelope = catalogEnvelopeCheck(t, "cut wait_tasks", out)
	if waitNext, _ := json.Marshal(envelope["next"]); string(waitNext) != mustJSON(t, next) {
		t.Fatalf("cut wait_tasks next %s, want %s", waitNext, mustJSON(t, next))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// catalogMachineRows reads box's reachability from list_machines and
// agents_at_work, each through the catalog.
func catalogMachineRows(t *testing.T, h *catalogParityHarness) (map[string]any, map[string]any) {
	t.Helper()
	machines, refused := h.control(t, catalogParityFullKey, map[string]any{"action": "list_machines"})
	if refused != "" {
		t.Fatal(refused)
	}
	agents, refused := h.control(t, catalogParityFullKey, map[string]any{"action": "agents_at_work"})
	if refused != "" {
		t.Fatal(refused)
	}
	find := func(raw, key string) map[string]any {
		for _, row := range envelopeDataMap(parityDecode(t, raw))[key].([]any) {
			if row := row.(map[string]any); row["name"] == "box" {
				return row
			}
		}
		t.Fatalf("box not in %s: %s", key, raw)
		return nil
	}
	return find(machines, "machines"), find(agents, "machines")
}

// Purpose: list_machines and agents_at_work use one reachability rule
// (tool.FleetMachineReachable): the answer's own contact with the machine
// decides, so a machine whose last recorded contact failed but answers now
// is online in both, and a machine that cannot be reached now is offline in
// both, never online next to an unreachable error. Threat: an Orchestrator
// or MCP client told a machine is online by one action and offline by the
// other. Boundary: Runtime.executeFleetProjects list_machines and
// Server.FleetAgents behind the catalog, over two in-process daemons linked
// like production; the linked machine's listener is closed to make it
// unreachable.
func TestCatalogMachineReachabilityAgrees(t *testing.T) {
	h := newCatalogParityHarness(t)
	// A refused call records a failed last contact.
	if _, refused := h.control(t, catalogParityFullKey, map[string]any{"action": tool.FolderEnvironmentList, "machine": "box"}); refused == "" {
		t.Fatal("list_environments on box was expected to fail (no environments there)")
	}
	machines, err := h.home.s.fleet.ListMachines()
	if err != nil || len(machines) != 1 || machines[0].LastError == "" {
		t.Fatalf("the failed contact was not recorded: %+v %v", machines, err)
	}
	listed, agents := catalogMachineRows(t, h)
	if listed["online"] != true || listed["last_error"] != nil || listed["status"] == nil || agents["state"] != "online" {
		t.Fatalf("reachable machine: list_machines %v, agents_at_work %v", listed, agents)
	}

	// Unreachable now: offline in both, with why.
	h.remote.s.CloseFleetListener()
	listed, agents = catalogMachineRows(t, h)
	if listed["online"] != false || listed["status_error"] == nil || agents["state"] != "offline" || agents["reason"] == nil {
		t.Fatalf("unreachable machine: list_machines %v, agents_at_work %v", listed, agents)
	}
	for _, rule := range []struct {
		recorded, contacted bool
		err                 error
		want                bool
	}{{true, false, nil, true}, {false, false, nil, false}, {false, true, nil, true}, {true, true, errTestUnreachable, false}} {
		if got := tool.FleetMachineReachable(rule.recorded, rule.contacted, rule.err); got != rule.want {
			t.Fatalf("FleetMachineReachable(%v, %v, %v) = %v", rule.recorded, rule.contacted, rule.err, got)
		}
	}
}

var errTestUnreachable = errors.New("unreachable")

// Purpose: task progress carries each delivery's latest review as a field
// (review: step, verdict, round, findings capped to maxProgressReviewRunes),
// whatever the verdict, so get_task can show a passed or warning review and
// not only a blocking one; a delivery whose steps recorded no verdict has
// none. Threat: a review outcome visible only when it blocked. Boundary:
// deliveryReview, the pure function computeTaskProgress uses for each
// delivery.
func TestDeliveryReviewIsTheLatestVerdict(t *testing.T) {
	findings := strings.Repeat("ü", maxProgressReviewRunes+10)
	d := jobautomation.Delivery{ID: "dl", Round: 2, Steps: []jobautomation.StepRecord{
		{ID: "review-1", Outcome: map[string]any{"verdict": "block", "findings": "old"}},
		{ID: "review-2", Outcome: map[string]any{"verdict": "ok", "findings": findings}},
		{ID: "push", Outcome: map[string]any{"head": "abc"}},
	}}
	review := deliveryReview(d)
	if review == nil || review.Step != "review-2" || review.Verdict != "ok" || review.Round != 2 || !review.FindingsTruncated || len([]rune(review.Findings)) != maxProgressReviewRunes {
		t.Fatalf("latest review: %+v", review)
	}
	var view taskProgressDelivery
	deliveryFindings(d, &view)
	if view.Verdict != "block" {
		t.Fatalf("the blocking findings are unchanged: %+v", view)
	}
	if deliveryReview(jobautomation.Delivery{Steps: []jobautomation.StepRecord{{ID: "push", Outcome: map[string]any{"head": "abc"}}}}) != nil {
		t.Fatal("a delivery without a verdict has a review")
	}
}

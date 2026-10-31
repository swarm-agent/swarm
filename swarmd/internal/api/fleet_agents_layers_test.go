package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// fleetAgentsLayersHarness links a remote machine "box" (linked work auto)
// with one active run to a home with an Orchestrator session, the
// remoteWaitHarness the other linked manage_projects tests use.
func fleetAgentsLayersHarness(t *testing.T) *remoteWaitHarness {
	t.Helper()
	h := newRemoteWaitHarness(t)
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	fleetAgentsTestSession(t, h.remote, "box-run", nil)
	fleetAgentsTestRun(t, h.remote, "box-run", "run-b")
	fleetAgentsTestTool(t, h.remote, "box-run", "run-b", "started", "b1", "bash", `{"command":"cargo test"}`)
	lease := fleetAgentsTestLease(t, h.remote, "box-run", "run-b")
	t.Cleanup(func() { _ = lease.Release() })
	return h
}

// fleetAgentsComparable is what does not move between two reads a moment
// apart: each run's machine, session, run and last call, and every machine's
// state and count.
func fleetAgentsComparable(t *testing.T, raw []byte) []string {
	t.Helper()
	var view fleetAgentsView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("answer: %v: %s", err, raw)
	}
	out := []string{}
	for _, a := range view.Agents {
		now := ""
		if a.Now != nil {
			now = a.Now.Kind + ":" + a.Now.Target
		}
		out = append(out, "agent "+a.Machine+"/"+a.SessionID+"/"+a.RunID+" "+now)
	}
	for _, m := range view.Machines {
		raw, _ := json.Marshal(m)
		out = append(out, "machine "+string(raw))
	}
	raw, _ = json.Marshal(view.Totals)
	out = append(out, "totals "+string(raw))
	sort.Strings(out)
	return out
}

func fleetAgentsServiceAnswer(t *testing.T, h *remoteWaitHarness, machine string) []byte {
	t.Helper()
	now := time.Now().UnixMilli()
	view, err := h.home.s.FleetAgents(context.Background(), h.p, now, fleetAgentsUTCDay(now), false, machine)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	return raw
}

// Purpose: Swarm Control's manage_fleet agents_at_work (which replaced
// swarm_agents_at_work) is a thin layer: its data is exactly what the service
// (FleetAgents, behind GET /v3/fleet/agents) answers, here and for the linked
// machine, and machine narrows it to that machine; a key without
// sessions:read is refused before anything is read.
// Threat: a second implementation that drifts from the console's read, or a
// key without sessions:read reading other sessions' commands. Boundary:
// controlMCPCall.callTool (Swarm Control tools/call) running the
// Orchestrator's handler over two linked in-process daemons.
func TestControlManageFleetAgentsAtWorkReturnsTheServiceAnswer(t *testing.T) {
	h := fleetAgentsLayersHarness(t)
	h.home.s.SetToolCatalogRuntime(h.runtime)
	call := func(scopes []string, args map[string]any) (map[string]any, error) {
		req := requestWithScopedToken(requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), h.home.user, h.home.account), &pebblestore.ScopedTokenRecord{ID: "fleet-agents-key", Scopes: scopes})
		c := &controlMCPCall{request: req, next: h.home.s.internalRouteHandler(), server: h.home.s}
		params, _ := json.Marshal(map[string]any{"name": "manage_fleet", "arguments": args})
		result, rpcErr := c.callTool(params)
		if rpcErr != nil {
			return nil, errors.New(rpcErr.Message)
		}
		content, _ := result["content"].([]map[string]any)
		if len(content) == 0 {
			return nil, errors.New("no content")
		}
		text, _ := content[0]["text"].(string)
		if result["isError"] == true {
			return nil, errors.New(text)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			return nil, err
		}
		return envelope, nil
	}
	for _, machine := range []string{"", "box"} {
		args := map[string]any{"action": "agents_at_work"}
		if machine != "" {
			args["machine"] = machine
		}
		envelope, err := call([]string{"sessions:read"}, args)
		if err != nil {
			t.Fatalf("machine %q: %v", machine, err)
		}
		if envelope["tool"] != "manage_fleet" || envelope["action"] != "agents_at_work" || envelope["state"] != "ok" {
			t.Fatalf("machine %q: envelope %v", machine, envelope)
		}
		raw, _ := json.Marshal(envelope["data"])
		want := fleetAgentsComparable(t, fleetAgentsServiceAnswer(t, h, machine))
		if have := fleetAgentsComparable(t, raw); strings.Join(have, "\n") != strings.Join(want, "\n") {
			t.Fatalf("machine %q: tool answered\n%s\nservice answered\n%s", machine, strings.Join(have, "\n"), strings.Join(want, "\n"))
		}
		if !strings.Contains(string(raw), `"box-run"`) {
			t.Fatalf("machine %q: the linked machine's run is missing: %s", machine, raw)
		}
	}
	if _, err := call([]string{"sessions:read"}, map[string]any{"action": "agents_at_work", "machine": "nowhere"}); err == nil || !strings.Contains(err.Error(), "no machine is named") {
		t.Fatalf("unknown machine: %v", err)
	}
	if _, err := call([]string{"signals:read"}, map[string]any{"action": "agents_at_work"}); err == nil || !strings.Contains(err.Error(), "sessions:read") {
		t.Fatalf("a key without sessions:read read the agents: %v", err)
	}
}

// Purpose: the Orchestrator's manage_projects action=agents_at_work is a thin
// layer over the same service: its data is FleetAgents' answer for this
// machine and the linked one (or one machine), marked untrusted; a task's own
// session (not the home's Orchestrator) is refused, as the other fleet
// actions are. Threat: an Orchestrator answer that disagrees with the
// console's and Swarm Control's, or a delegated agent reading the fleet.
// Boundary: tool.Runtime.ExecuteForWorkspaceScopeWithRuntime (manage_projects
// → executeFleetAgentsAtWork) with the Server as its service, over two linked
// in-process daemons.
func TestManageProjectsAgentsAtWorkReturnsTheServiceAnswer(t *testing.T) {
	h := fleetAgentsLayersHarness(t)
	for _, machine := range []string{"", "box"} {
		args := map[string]any{"action": "agents_at_work"}
		if machine != "" {
			args["machine"] = machine
		}
		out, err := linkedTaskCall(t, h, "home-orch", args)
		if err != nil {
			t.Fatalf("machine %q: %v", machine, err)
		}
		if out["untrusted_content"] != true || out["action"] != "agents_at_work" {
			t.Fatalf("machine %q: answer %v", machine, out)
		}
		raw, _ := json.Marshal(out["data"])
		want := fleetAgentsComparable(t, fleetAgentsServiceAnswer(t, h, machine))
		if have := fleetAgentsComparable(t, raw); strings.Join(have, "\n") != strings.Join(want, "\n") {
			t.Fatalf("machine %q: action answered\n%s\nservice answered\n%s", machine, strings.Join(have, "\n"), strings.Join(want, "\n"))
		}
		if !strings.Contains(string(raw), `"box-run"`) {
			t.Fatalf("machine %q: the linked machine's run is missing: %s", machine, raw)
		}
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "agents_at_work", "machine": "nowhere"}); err == nil || !strings.Contains(err.Error(), "no machine is named") {
		t.Fatalf("unknown machine: %v", err)
	}
	h.createGoal(t, "task-session", map[string]any{"project_id": "home-project", "task_id": "some-task"})
	if _, err := linkedTaskCall(t, h, "task-session", map[string]any{"action": "agents_at_work"}); err == nil || !strings.Contains(err.Error(), "only available to the home's Orchestrator") {
		t.Fatalf("a task's session read the fleet: %v", err)
	}
}

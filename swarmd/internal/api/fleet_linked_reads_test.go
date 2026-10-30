package api

import (
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the home's Orchestrator sees on a linked machine what an MCP
// client holding that machine's key sees, so it never has to guess why work
// stalled (on 2026-10-29 it could only list tasks and machines, and told the
// person "probably hit the usage limit" while three tasks waited on a person
// behind an update drain). Requirement: over the existing link,
// machine_status is the machine's box summary as swarm_status shows it
// (attention with blocked sessions, drain state with its time, running_now);
// get_task_session is a sent task's session as swarm_get_session shows it
// (the session, its pending permission with the tool, the latest messages,
// last activity time), marked untrusted; activity is the machine's activity
// page after a cursor (here the task session's needs_you event) and, without
// a cursor, its signals (the drain). Threats: a read of a session this home
// did not send, a read of a machine whose linked work rule is off, remote
// text treated as trusted. Boundary: Server.ReadFleetMachineStatusView,
// ReadFleetTaskSessionView and ReadFleetActivity behind the native
// manage_projects runtime and the permission gate, over two in-process
// daemons linked like production; the views are built by the same
// functions as MCP (controlMCPSessionStateView, controlMCPSignalPage).
func TestLinkedMachineReadsLikeMCP(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	h.createGoal(t, "home-orch", map[string]any{})
	perms, manager := attentionWireRemote(t, h)
	signals := pebblestore.NewSignalStore(h.remote.s.sessions.Store().Underlying())
	h.remote.s.SetSignalStore(signals)
	attentionSent(t, h, "t1")
	if _, err := h.remote.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "rs-t1", AccountScopeID: h.remote.account, UserID: h.remote.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: "msg-1", PayloadHash: "msg-1", Message: &pebblestore.MessageSnapshot{ID: "msg-1", Role: "assistant", Content: "Running the test suite now."}}); err != nil {
		t.Fatal(err)
	}
	record := attentionPending(t, perms, "rs-t1", "call-1", "go test ./... -count=1")
	attentionPending(t, perms, "rs-t2", "call-2", "make release")
	drain, err := manager.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signals.Append(pebblestore.Signal{Source: pebblestore.SignalSourceSwarmd, Kind: "update.drain.started", Severity: pebblestore.SignalSeverityInfo, Summary: "New executions held for installer update", DedupKey: "update.drain:" + drain.Since.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	// read returns the answer's data after checking its envelope: the
	// machine read, untrusted.
	read := func(args map[string]any) (map[string]any, error) {
		args["machine"] = "box"
		envelope, err := linkedTaskCall(t, h, "home-orch", args)
		if err != nil {
			return nil, err
		}
		if envelope["untrusted_content"] != true || envelope["machine"] != "box" || envelope["action"] != args["action"] {
			t.Fatalf("envelope: %v", envelope)
		}
		return envelopeDataMap(envelope), nil
	}

	// machine_status: swarm_status's data.
	status, err := read(map[string]any{"action": "machine_status"})
	if err != nil {
		t.Fatal(err)
	}
	attention, _ := status["attention"].(map[string]any)
	machine, _ := status["machine"].(map[string]any)
	drainView, _ := machine["drain"].(map[string]any)
	if status["machine_name"] != "box" || attention == nil || attention["blocked_sessions"] != float64(2) || drainView == nil || drainView["draining"] != true || drainView["since_ms"] != float64(drain.Since.UnixMilli()) {
		t.Fatalf("machine_status: %v", status)
	}
	if _, ok := status["running_now"]; !ok {
		t.Fatalf("machine_status without running_now: %v", status)
	}

	// get_task_session: swarm_get_session's view of the sent task's session.
	view, err := read(map[string]any{"action": "get_task_session", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := view["pending_permissions"].([]any)
	messages, _ := view["messages"].([]any)
	if view["session_id"] != "rs-t1" || view["read_only"] != true || len(pending) != 1 || len(messages) == 0 {
		t.Fatalf("get_task_session: %v", view)
	}
	first := pending[0].(map[string]any)
	if arguments, _ := first["tool_arguments"].(map[string]any); first["id"] != record.ID || first["tool_name"] != "bash" || first["kind"] != "approval" || arguments["command"] != "go test ./... -count=1" {
		t.Fatalf("pending permission: %v", first)
	}
	if last, _ := view["last_activity_at"].(float64); last <= 0 {
		t.Fatalf("last_activity_at: %v", view)
	}
	found := false
	for _, raw := range messages {
		if strings.Contains(raw.(map[string]any)["content"].(string), "Running the test suite") {
			found = true
		}
	}
	if !found {
		t.Fatalf("messages: %v", messages)
	}
	// A session this home did not send is not read.
	if _, err := read(map[string]any{"action": "get_task_session", "project_id": "remote-project", "task_id": "t2"}); err == nil || !strings.Contains(err.Error(), "not sent from this home") {
		t.Fatalf("an unsent task's session was read: %v", err)
	}

	// activity after a cursor: the session's needs_you event, metadata only.
	page, err := read(map[string]any{"action": "activity", "since_cursor": "0"})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := page["events"].([]any)
	waiting := false
	for _, raw := range events {
		event := raw.(map[string]any)
		if event["kind"] == "permission.summary.updated" && event["session_id"] == "rs-t1" && event["state"] == "needs_you" {
			waiting = true
			if strings.Contains(event["summary"].(string), "go test") {
				t.Fatalf("activity carried the request's text: %v", event)
			}
		}
	}
	if !waiting || page["cursor"] == nil {
		t.Fatalf("activity page: %v", page)
	}
	// activity without a cursor: the machine's signals.
	feed, err := read(map[string]any{"action": "activity"})
	if err != nil {
		t.Fatal(err)
	}
	drained := false
	for _, raw := range feed["events"].([]any) {
		if raw.(map[string]any)["kind"] == "update.drain.started" {
			drained = true
		}
	}
	if !drained || feed["cursor"] == nil {
		t.Fatalf("activity signals: %v", feed)
	}

	// The gate: reads run under ask and auto; off refuses each read.
	gate := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	gate.SetSessionResolver(h.home.s.sessions)
	for _, level := range []string{pebblestore.RuleAsk, pebblestore.RuleOff} {
		fleetActivityTestSetWork(t, h.home, "box", level)
		for _, action := range []string{"machine_status", "get_task_session", "activity"} {
			result, err := gate.AuthorizeToolCall(permission.AuthorizationInput{SessionID: "home-orch", AccountScopeID: h.home.account, RunID: "run-1", CallID: level + action, ToolName: "manage_projects", ToolArguments: `{"action":"` + action + `","machine":"box","project_id":"remote-project","task_id":"t1"}`, Mode: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			want := permission.AuthorizationApprove
			if level == pebblestore.RuleOff {
				want = permission.AuthorizationDeny
			}
			if result.Decision != want || result.Record != nil {
				t.Fatalf("%s under %s: %+v", action, level, result)
			}
			if level == pebblestore.RuleOff {
				if _, err := read(map[string]any{"action": action, "project_id": "remote-project", "task_id": "t1"}); err == nil || !strings.Contains(err.Error(), "off") {
					t.Fatalf("%s ran with the rule off: %v", action, err)
				}
				if _, err := h.home.s.ReadFleetMachineStatusView(t.Context(), h.p, "box"); err == nil || linkedTaskStatus(err) != 403 {
					t.Fatalf("service read with the rule off: %v", err)
				}
			}
		}
	}
}

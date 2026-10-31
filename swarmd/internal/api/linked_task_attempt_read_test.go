package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: after a follow-up, a linked task's earlier attempt's full report
// stays readable. get_task shows the report it has (the initial attempt's,
// while the follow-up runs) cut at its budget, and its next names that
// attempt (attempt_id); read_task_session with that next reads the
// attempt's own session, and following each cut message's next (which keeps
// the attempt) returns the report exactly. Without an attempt the read is
// the current attempt's session, as before; get_task_session takes the same
// attempt; an attempt the task does not have is refused with the attempts it
// does have.
// Threat (GPUI-vs-MCP parity rerun): get_task said result_truncated with a
// next pointing at read_task_session, which read only the follow-up's
// session, so the first attempt's full summary could not be read on either
// surface.
// Boundary: tool.compactProjectTask/markProjectTaskResultCut (get_task's
// next), executeFleetProjects -> Server.ReadFleetLinkedTaskSession and
// Server.ReadFleetTaskSessionView (linkedTaskAttemptSession), behind the
// native manage_projects actions the catalog serves on both surfaces.
// Narrowest layer: two in-process daemons over the real link, where the
// machine's task record, its sessions and the home's selection meet.
func TestLinkedTaskEarlierAttemptReportReadsInFull(t *testing.T) {
	h := newRemoteWaitHarness(t)
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	report := longTaskReport()
	fleetActivityTestSession(t, h.remote, "rs-t1")
	fleetActivityTestSession(t, h.remote, "rs-t1-follow")
	appendRemote := func(session, id, content string) {
		t.Helper()
		if _, err := h.remote.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: h.remote.account, UserID: h.remote.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: id, PayloadHash: id, Message: &pebblestore.MessageSnapshot{ID: id, Role: "assistant", Content: content}}); err != nil {
			t.Fatal(err)
		}
	}
	appendRemote("rs-t1", "initial-report", report)
	appendRemote("rs-t1-follow", "follow-progress", "Looking at the follow-up é.")
	// The initial attempt finished with its report; the follow-up runs in
	// its own session and has no summary yet.
	if err := h.remote.s.sessions.Store().PutProjectTask(h.remote.account, &pebblestore.ProjectTaskRecord{ID: "t1", ProjectID: "remote-project", Title: "Remote t1", Agent: "coder", Status: "running", SessionID: "rs-t1-follow", ActiveAttemptID: "attempt-follow", Attempts: []pebblestore.ProjectTaskAttempt{
		{ID: "attempt-t1", SessionID: "rs-t1", Role: "coder", Status: "done", Summary: report},
		{ID: "attempt-follow", SessionID: "rs-t1-follow", Role: "coder", Status: "running"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.home.s.fleet.PutLinkedTask(pebblestore.FleetLinkedTask{AccountScopeID: h.home.account, Machine: "box", ProjectID: "remote-project", TaskID: "t1", HomeSessionID: "home-orch"}); err != nil {
		t.Fatal(err)
	}

	call := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, _ := json.Marshal(args)
		out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			return nil, err
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal(err)
		}
		if err := tool.CheckEnvelope(out); err != nil {
			t.Fatalf("envelope: %v\n%s", err, out)
		}
		return envelope, nil
	}
	data := func(envelope map[string]any) map[string]any {
		view, _ := envelope["data"].(map[string]any)
		return view
	}

	// get_task: the initial attempt's report, cut, and next names it.
	got, err := call(map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustJSON(t, got), `"result_truncated":true`) {
		t.Fatalf("the report is not cut: %s", mustJSON(t, got))
	}
	next, _ := got["next"].(map[string]any)
	if next["action"] != "read_task_session" || next["attempt_id"] != "attempt-t1" || next["machine"] != "box" || next["task_id"] != "t1" {
		t.Fatalf("get_task's next does not name the attempt whose report was cut: %v", next)
	}

	// Without an attempt the read is the follow-up's session (unchanged).
	current, err := call(map[string]any{"action": "read_task_session", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if view := data(current); view["session_id"] != "rs-t1-follow" || view["attempt_id"] != "attempt-follow" {
		t.Fatalf("default read is not the current attempt's: %v", view)
	}

	// get_task's next, then each cut message's next, reads the report exactly.
	args := map[string]any{"max_chars": 20000, "limit": 1}
	for key, value := range next {
		args[key] = value
	}
	text := ""
	for step := 0; ; step++ {
		if step > 10 {
			t.Fatal("continuation did not finish")
		}
		envelope, err := call(args)
		if err != nil {
			t.Fatal(err)
		}
		view := data(envelope)
		if view["session_id"] != "rs-t1" || view["attempt_id"] != "attempt-t1" {
			t.Fatalf("step %d read another attempt: %v", step, view)
		}
		chunk := view["messages"].([]any)[0].(map[string]any)
		if chunk["id"] != "initial-report" {
			t.Fatalf("step %d answered another message: %v", step, chunk["id"])
		}
		text += chunk["content"].(string)
		cut, _ := chunk["next"].(map[string]any)
		if cut == nil {
			break
		}
		if cut["attempt_id"] != "attempt-t1" {
			t.Fatalf("a cut message's next drops the attempt: %v", cut)
		}
		args = cut
	}
	if text != report {
		t.Fatalf("read %d bytes, want the %d-byte initial report exactly", len(text), len(report))
	}

	// session_id names the same attempt; get_task_session takes it too.
	bySession, err := call(map[string]any{"action": "read_task_session", "machine": "box", "project_id": "remote-project", "task_id": "t1", "session_id": "rs-t1", "limit": 1})
	if err != nil || data(bySession)["attempt_id"] != "attempt-t1" {
		t.Fatalf("session_id did not pick the initial attempt: %v %v", err, bySession)
	}
	view, err := call(map[string]any{"action": tool.FleetTaskSessionAction, "machine": "box", "project_id": "remote-project", "task_id": "t1", "attempt_id": "attempt-t1"})
	if err != nil || data(view)["session_id"] != "rs-t1" {
		t.Fatalf("get_task_session did not read the named attempt: %v %v", err, view)
	}

	// An attempt the task does not have is refused with the ones it has;
	// so is a session that is not one of its attempts'.
	for name, extra := range map[string]map[string]any{
		"unknown attempt":           {"attempt_id": "attempt-nope"},
		"foreign session":           {"session_id": "rs-other"},
		"attempt and other session": {"attempt_id": "attempt-t1", "session_id": "rs-t1-follow"},
	} {
		args := map[string]any{"action": "read_task_session", "machine": "box", "project_id": "remote-project", "task_id": "t1"}
		for key, value := range extra {
			args[key] = value
		}
		_, err := call(args)
		if err == nil || !strings.Contains(err.Error(), "has no attempt") || !strings.Contains(err.Error(), "attempt-t1 (session rs-t1)") || !strings.Contains(err.Error(), "attempt-follow (session rs-t1-follow)") {
			t.Fatalf("%s: not refused with the task's attempts: %v", name, err)
		}
	}
}

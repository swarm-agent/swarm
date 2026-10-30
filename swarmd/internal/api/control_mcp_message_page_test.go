package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an outside AI reading through Swarm Control (MCP) can read a
// task's long final message in full. swarm_get_session shows a cut message
// with truncated, content_offset, total_bytes and next; passing next's
// message_id, message_seq and content_offset back returns the following
// chunk from the session messages route, until the concatenation equals
// the ~50,000-byte multi-byte message exactly. swarm_task_result flags a
// shortened report (result_truncated, result_bytes) and names the paged
// read (read_full). Short messages are unchanged; a stale or invented
// continuation is refused.
// Threat: the MCP tools cutting a long report with only a
// "[truncated N characters]" marker, so the delegating AI reports from half
// a document; a chunk splitting a multi-byte character; a continuation
// reading another message.
// Boundary: controlMCPGetSession (controlMCPSessionStateView,
// controlMCPMessageText, controlMCPCall.sessionMessageChunk) over
// GET /v3/sessions/{id}/messages (readSessionMessageChunk,
// tool.ChunkMessageContent), and controlMCPTaskResult over the task route.
// Narrowest layer: the MCP tool functions dispatching through the real
// route allowlist and the daemon's internal route handler over a real
// temporary store; no provider runs.
func TestControlMCPReadsALongTaskMessageInFull(t *testing.T) {
	f := newFleetTestSwarm(t, "mcp")
	t.Cleanup(f.cleanup)
	if err := f.s.sessions.Store().PutProject(f.account, &pebblestore.ProjectRecord{ID: "mcp-project", Name: "MCP project"}); err != nil {
		t.Fatal(err)
	}
	fleetActivityTestSession(t, f, "task-session")
	report := longTaskReport()
	for _, m := range []pebblestore.MessageSnapshot{{ID: "short-note", Role: "assistant", Content: "Short note é."}, {ID: "final-report", Role: "assistant", Content: report}} {
		if _, err := f.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "task-session", AccountScopeID: f.account, UserID: f.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: m.ID, PayloadHash: m.ID, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task-long", ProjectID: "mcp-project", Title: "Long report", Agent: "finder", Status: "completed", SessionID: "task-session", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-1", SessionID: "task-session", Role: "finder", Status: "completed", Summary: report}}}
	if err := f.s.sessions.Store().PutProjectTask(f.account, task); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request = requestWithTestPrincipalForAccount(request, f.user, f.account)
	request = requestWithScopedToken(request, &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "projects:read"}})
	call := &controlMCPCall{request: request, next: f.s.internalRouteHandler(), server: f.s}

	// swarm_task_result: the shortened report says so and names the read.
	result, err := controlMCPTaskResult(call, map[string]any{"project_id": "mcp-project", "task_id": "task-long"})
	if err != nil {
		t.Fatal(err)
	}
	out := controlMCPMap(result)
	readFull := controlMCPMap(out["read_full"])
	if out["result_truncated"] != true || out["result_bytes"] != len(report) || readFull["tool"] != "swarm_get_session" || readFull["session_id"] != "task-session" {
		t.Fatalf("shortened report is not flagged: truncated=%v bytes=%v read_full=%v", out["result_truncated"], out["result_bytes"], readFull)
	}

	// swarm_get_session: the cut report carries its continuation.
	view, err := controlMCPGetSession(call, map[string]any{"session_id": "task-session", "messages": float64(2), "detail_chars": float64(20000)})
	if err != nil {
		t.Fatal(err)
	}
	var item, short map[string]any
	messages, _ := controlMCPMap(view)["messages"].([]map[string]any)
	for _, m := range messages {
		content, _ := m["content"].(string)
		switch {
		case m["id"] == "final-report":
			item = m
		case strings.HasPrefix(content, "Short note"):
			short = m
		}
	}
	if short == nil || short["content"] != "Short note é." || short["truncated"] != nil || short["next"] != nil {
		t.Fatalf("short message changed: %#v", short)
	}
	if item == nil || item["truncated"] != true || item["total_bytes"] != len(report) {
		t.Fatalf("the cut report does not say so: %v", controlMCPMap(view)["messages"])
	}
	got := item["content"].(string)
	if item["content_offset"] != len(got) || !strings.HasPrefix(report, got) || strings.Contains(got, "[truncated") {
		t.Fatalf("first part is not an exact prefix ending at content_offset %v", item["content_offset"])
	}
	next := controlMCPMap(item["next"])
	if next["tool"] != "swarm_get_session" || next["session_id"] != "task-session" || next["message_id"] != "final-report" || next["content_offset"] != len(got) {
		t.Fatalf("next does not carry the exact continuation: %v", next)
	}

	// Following next reads the rest exactly, as an MCP client would: the
	// arguments travel as JSON numbers.
	for step := 0; next != nil; step++ {
		if step > 10 {
			t.Fatal("continuation did not finish")
		}
		args := map[string]any{"session_id": "task-session", "message_id": next["message_id"], "message_seq": controlMCPTestNumber(next["message_seq"]), "content_offset": controlMCPTestNumber(next["content_offset"])}
		chunkView, err := controlMCPGetSession(call, args)
		if err != nil {
			t.Fatal(err)
		}
		chunk := controlMCPMap(controlMCPMap(chunkView)["message"])
		if chunk["id"] != "final-report" || controlMCPTestNumber(chunk["content_start"]) != float64(len(got)) {
			t.Fatalf("continuation answered another part: %v", chunk["content_start"])
		}
		got += chunk["content"].(string)
		next = controlMCPMap(chunk["next"])
		if (next == nil) != (chunk["truncated"] != true) {
			t.Fatalf("truncated and next disagree: %v", chunk)
		}
	}
	if got != report {
		t.Fatalf("the chunks concatenate to %d bytes, want the %d-byte report exactly", len(got), len(report))
	}

	// A stale or invented continuation is refused, never another message.
	seq := controlMCPTestNumber(item["global_seq"])
	mid := float64(strings.Index(report, "é") + 1)
	for name, args := range map[string]map[string]any{
		"wrong id":      {"message_id": "short-note", "message_seq": seq, "content_offset": float64(0)},
		"wrong seq":     {"message_id": "final-report", "message_seq": seq + 50, "content_offset": float64(0)},
		"inside a rune": {"message_id": "final-report", "message_seq": seq, "content_offset": mid},
		"past the end":  {"message_id": "final-report", "message_seq": seq, "content_offset": float64(len(report) + 1)},
		"no seq":        {"message_id": "final-report", "content_offset": float64(0)},
	} {
		args["session_id"] = "task-session"
		if out, err := controlMCPGetSession(call, args); err == nil {
			t.Fatalf("%s: continuation accepted: %v", name, out)
		}
	}
}

// controlMCPTestNumber is a number as an MCP client sends it back (JSON
// numbers decode to float64).
func controlMCPTestNumber(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case uint64:
		return float64(v)
	case float64:
		return v
	}
	return -1
}

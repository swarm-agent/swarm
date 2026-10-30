package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// longTaskReport is a ~50,000-byte final report mixing one- to four-byte
// runes and JSON-escaped characters, so chunk boundaries fall inside
// multi-byte runes.
func longTaskReport() string {
	var b strings.Builder
	for i := 0; b.Len() < 50000; i++ {
		b.WriteString("## Section " + strconv.Itoa(i) + " é漢🙂 <a & \"b\"> \\ ü\n")
	}
	return b.String()
}

// Purpose: a linked task's long final message (here ~50 KB, larger than the
// 20 KB max_chars) is readable in full through read_task_session: the cut
// message says truncated with content_offset, total_bytes and a next hint
// carrying the exact arguments, and following next returns the following
// chunks until the concatenation equals the message exactly; the same works
// over the HTTP route. Short messages are unchanged.
// Threat: the Orchestrator reporting from half a document because the rest
// of one message was unreachable (the read only paged across messages), a
// chunk splitting a multi-byte character, or a stale/invented continuation
// reading another message.
// Boundary: Server.ReadFleetLinkedTaskSession (tool.ChunkMessageContent,
// linkedTaskMessageItem) behind the native read_task_session action and
// handleFleetLinkedTasks, with the remote /v3/sessions/{id}/messages page.
// Narrowest layer: two in-process daemons over the real link, where the
// remote page, the proxy's parameters and the home's chunking meet.
func TestFleetLinkedTaskSessionReadsALongMessageInFull(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	report := longTaskReport()
	appendRemote := func(id, role, content string) {
		t.Helper()
		if _, err := h.remote.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "rs-t1", AccountScopeID: h.remote.account, UserID: h.remote.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: id, PayloadHash: id, Message: &pebblestore.MessageSnapshot{ID: id, Role: role, Content: content}}); err != nil {
			t.Fatal(err)
		}
	}
	appendRemote("short-before", "assistant", "Short note é.")
	appendRemote("final-report", "assistant", report)

	read := func(args map[string]any) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		args["action"], args["machine"], args["project_id"], args["task_id"] = "read_task_session", "box", "remote-project", "t1"
		raw, _ := json.Marshal(args)
		out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}

	// The latest read cuts the report and says how to read on.
	first := read(map[string]any{"limit": 1, "max_chars": 20000})
	messages := first["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("latest read: %v", first)
	}
	item := messages[0].(map[string]any)
	if item["id"] != "final-report" || item["truncated"] != true || item["total_bytes"] != float64(len(report)) {
		t.Fatalf("the cut report does not say so: %v", item)
	}
	got := item["content"].(string)
	if item["content_offset"] != float64(len(got)) {
		t.Fatalf("content_offset %v does not end the %d-byte chunk", item["content_offset"], len(got))
	}
	next, _ := item["next"].(map[string]any)
	if next["action"] != "read_task_session" || next["machine"] != "box" || next["project_id"] != "remote-project" || next["task_id"] != "t1" || next["message_id"] != "final-report" || next["message_seq"] != item["global_seq"] || next["content_offset"] != item["content_offset"] {
		t.Fatalf("next does not carry the exact continuation: %v", next)
	}

	// Following next reads the rest exactly, on rune boundaries.
	for step := 0; next != nil; step++ {
		if step > 10 {
			t.Fatal("continuation did not finish")
		}
		args := map[string]any{}
		for _, key := range []string{"message_id", "message_seq", "content_offset", "max_chars"} {
			args[key] = next[key]
		}
		view := read(args)
		chunk := view["messages"].([]any)[0].(map[string]any)
		if chunk["id"] != "final-report" || chunk["content_start"] != float64(len(got)) {
			t.Fatalf("continuation answered another part: %v", chunk)
		}
		got += chunk["content"].(string)
		if chunk["content_offset"] != float64(len(got)) || chunk["total_bytes"] != float64(len(report)) {
			t.Fatalf("continuation bounds: %v (have %d bytes)", chunk, len(got))
		}
		next, _ = chunk["next"].(map[string]any)
		if (next == nil) != (chunk["truncated"] != true) {
			t.Fatalf("truncated and next disagree: %v", chunk)
		}
	}
	if got != report {
		t.Fatalf("the chunks concatenate to %d bytes, want the %d-byte report exactly", len(got), len(report))
	}

	// A short message is unchanged: whole, with no continuation fields.
	shortView := read(map[string]any{"limit": 2, "max_chars": 20000})
	for _, raw := range shortView["messages"].([]any) {
		m := raw.(map[string]any)
		if m["id"] != "short-before" {
			continue
		}
		if m["content"] != "Short note é." || m["truncated"] != nil || m["next"] != nil || m["content_offset"] != nil {
			t.Fatalf("short message changed: %v", m)
		}
	}

	// The HTTP route serves the same continuation.
	seq := strconv.FormatUint(uint64(item["global_seq"].(float64)), 10)
	base := fleetLinkedTaskSessionPath + "?machine=box&project_id=remote-project&task_id=t1&max_chars=20000"
	code, routed := linkedTaskRoute(t, h, h.home.user, base+"&message_id=final-report&message_seq="+seq+"&content_offset="+strconv.Itoa(len(item["content"].(string))))
	if code != http.StatusOK {
		t.Fatalf("route continuation: %d %v", code, routed)
	}
	if chunk := routed["messages"].([]any)[0].(map[string]any); !strings.HasPrefix(report[len(item["content"].(string)):], chunk["content"].(string)) || chunk["content"] == "" {
		t.Fatalf("route continuation returned another part: %.60v", chunk["content"])
	}

	// A stale or invented continuation is refused, never another message.
	mid := strings.Index(report, "é") + 1
	for name, query := range map[string]string{
		"wrong id":         "&message_id=short-before&message_seq=" + seq + "&content_offset=0",
		"wrong seq":        "&message_id=final-report&message_seq=1&content_offset=0",
		"inside a rune":    "&message_id=final-report&message_seq=" + seq + "&content_offset=" + strconv.Itoa(mid),
		"past the end":     "&message_id=final-report&message_seq=" + seq + "&content_offset=" + strconv.Itoa(len(report)+1),
		"no seq":           "&message_id=final-report&content_offset=0",
		"negative offset":  "&message_id=final-report&message_seq=" + seq + "&content_offset=-1",
		"id is not a name": "&message_id=" + url.QueryEscape(" ") + "&message_seq=" + seq,
	} {
		if code, body := linkedTaskRoute(t, h, h.home.user, base+query); code == http.StatusOK {
			t.Fatalf("%s: continuation accepted: %v", name, body)
		}
	}
}

// Purpose: manage-sessions read_messages pages within one long message the
// same way (same ChunkMessageContent rule): a cut message says truncated
// with content_offset, total_bytes and next, following next reads the rest
// exactly, and messages the budget leaves out are reported instead of being
// skipped by next_after_seq.
// Threat: read_messages silently cutting a message with no flag and no way
// to read the rest, or its after-page skipping messages it never returned.
// Boundary: tool.Runtime manage-sessions read_messages (manageSessionsRead,
// manageSessionsReadMessageChunk) over the home daemon's real session store.
// Narrowest layer: the native tool over a real store; no provider runs.
func TestManageSessionsReadMessagesPagesALongMessage(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	report := longTaskReport()
	for _, m := range []pebblestore.MessageSnapshot{{ID: "local-report", Role: "assistant", Content: report}, {ID: "local-after", Role: "assistant", Content: "After é."}} {
		if _, err := h.home.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "home-orch", AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: m.ID, PayloadHash: m.ID, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(args map[string]any) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		args["action"], args["session_id"] = "read_messages", "home-orch"
		raw, _ := json.Marshal(args)
		out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage-sessions", Arguments: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	view := read(map[string]any{"limit": 2, "max_chars": 20000})
	var item map[string]any
	for _, raw := range view["messages"].([]any) {
		if m := raw.(map[string]any); m["id"] == "local-report" {
			item = m
		}
	}
	if item == nil || item["truncated"] != true || item["total_bytes"] != float64(len(report)) {
		t.Fatalf("the cut message does not say so: %v", view)
	}
	// The message after the cut one is either returned (a chunk of it, with
	// its own next) or reported as left out; never skipped by the page.
	var after map[string]any
	for _, raw := range view["messages"].([]any) {
		if m := raw.(map[string]any); m["id"] == "local-after" {
			after = m
		}
	}
	if after == nil && (view["budget_exhausted"] != true || view["has_more_newer"] != true || view["next_after_seq"] != item["seq"]) {
		t.Fatalf("the message the budget left out is not reported: %v", view)
	}
	if after != nil && after["content"] != "After é." && (after["truncated"] != true || after["next"] == nil) {
		t.Fatalf("the message after the cut one was cut without a continuation: %v", after)
	}
	got := item["content"].(string)
	next, _ := item["next"].(map[string]any)
	for step := 0; next != nil; step++ {
		if step > 10 {
			t.Fatal("continuation did not finish")
		}
		args := map[string]any{}
		for _, key := range []string{"message_id", "message_seq", "content_offset", "max_chars"} {
			args[key] = next[key]
		}
		chunk := read(args)["messages"].([]any)[0].(map[string]any)
		if chunk["id"] != "local-report" || chunk["content_start"] != float64(len(got)) {
			t.Fatalf("continuation answered another part: %v", chunk)
		}
		got += chunk["content"].(string)
		next, _ = chunk["next"].(map[string]any)
	}
	if got != report {
		t.Fatalf("the chunks concatenate to %d bytes, want the %d-byte message exactly", len(got), len(report))
	}
	short := read(map[string]any{"mode": "after", "after_seq": item["seq"]})
	if m := short["messages"].([]any)[0].(map[string]any); m["id"] != "local-after" || m["content"] != "After é." || m["truncated"] != nil || m["next"] != nil {
		t.Fatalf("short message changed: %v", m)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"action": "read_messages", "session_id": "home-orch", "message_id": "local-after", "message_seq": item["seq"], "content_offset": 0})
	if _, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage-sessions", Arguments: string(raw)}); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("a continuation naming another message was not refused: %v", err)
	}
}

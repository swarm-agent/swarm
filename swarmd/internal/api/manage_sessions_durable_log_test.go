package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// durableLogNeedle is the text every matching fixture event carries.
const durableLogNeedle = "needle-dlog"

// durableLogBig is a message whose event is far larger than the search's
// max_chars, with multi-byte characters, quotes, angle brackets and newlines
// so chunk cuts land inside JSON escapes and UTF-8 sequences.
var durableLogBig = durableLogNeedle + " big " + strings.Repeat("é€ \"quoted\" <tag> line\n", 260)

// durableLogSeed gives a session an older small match, the big match, a
// message that does not match and a newer small match.
func durableLogSeed(t *testing.T, f *fleetTestSwarm, session string) {
	t.Helper()
	fleetActivityTestSession(t, f, session)
	for _, m := range []pebblestore.MessageSnapshot{
		{ID: session + "-older", Role: "user", Content: durableLogNeedle + " older small"},
		{ID: session + "-big", Role: "assistant", Content: durableLogBig},
		{ID: session + "-other", Role: "user", Content: "nothing to find here"},
		{ID: session + "-newer", Role: "assistant", Content: durableLogNeedle + " newer small"},
	} {
		m := m
		if _, err := f.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: f.account, UserID: f.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: m.ID, PayloadHash: m.ID, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
}

// durableLogMatching is every event of the session the needle matches, by
// seq: the exact set a complete search must return, each once.
func durableLogMatching(t *testing.T, f *fleetTestSwarm, session string) map[uint64]pebblestore.V3SessionEvent {
	t.Helper()
	events, err := f.s.sessions.ListSessionEventsBefore(session, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uint64]pebblestore.V3SessionEvent{}
	for _, event := range events {
		if strings.Contains(strings.ToLower(event.EventType+"\n"+string(event.Payload)), durableLogNeedle) {
			out[event.Seq] = event
		}
	}
	return out
}

// durableLogHasString reports whether a decoded JSON value holds text.
func durableLogHasString(value any, text string) bool {
	switch v := value.(type) {
	case string:
		return v == text
	case map[string]any:
		for _, item := range v {
			if durableLogHasString(item, text) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if durableLogHasString(item, text) {
				return true
			}
		}
	}
	return false
}

// durableLogRead is what following a durable_log chain read.
type durableLogRead struct {
	whole   map[uint64]int    // whole events returned, and how often
	chunks  map[uint64]string // a chunked event's content, chunks joined
	chunked map[uint64]int    // how many chunks each chunked event came in
	nexts   []map[string]any
}

// durableLogFollow runs a durable_log search through Swarm Control and the
// Orchestrator, checks both answer the same, and follows the envelope's next
// until there is none. Every step must stay within max_chars and every next
// must differ from the call it follows (the search always moves on).
func durableLogFollow(t *testing.T, h *catalogParityHarness, first map[string]any) durableLogRead {
	t.Helper()
	read := durableLogRead{whole: map[uint64]int{}, chunks: map[uint64]string{}, chunked: map[uint64]int{}}
	budget := first["max_chars"].(float64)
	call := first
	for step := 0; ; step++ {
		if step > 60 {
			t.Fatalf("durable_log paging did not finish: %v", read.nexts)
		}
		controlOut, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", call)
		if refused != "" {
			t.Fatalf("durable_log %v: %s", call, refused)
		}
		orchOut, err := h.orchestratorTool(t, "manage_sessions", call)
		if err != nil {
			t.Fatalf("durable_log %v natively: %v", call, err)
		}
		a, _ := json.Marshal(parityStrip(parityDecode(t, orchOut)))
		b, _ := json.Marshal(parityStrip(parityDecode(t, controlOut)))
		if string(a) != string(b) {
			t.Fatalf("Orchestrator and Swarm Control answer durable_log differently:\n%s\n%s", a, b)
		}
		envelope := catalogEnvelopeCheck(t, "manage_sessions search durable_log", controlOut)
		data := envelopeDataMap(envelope)
		if data["content_trust"] != "untrusted" || data["search_mode"] != "durable_log" {
			t.Fatalf("durable_log answer lost its framing: %v", data)
		}
		if chars, _ := data["characters"].(float64); chars > budget {
			t.Fatalf("durable_log answer spent %v characters over max_chars %v", chars, budget)
		}
		for _, raw := range data["events"].([]any) {
			read.whole[uint64(raw.(map[string]any)["seq"].(float64))]++
		}
		if chunk, ok := data["event_chunk"].(map[string]any); ok {
			seq := uint64(chunk["seq"].(float64))
			if chunk["content_start"] != float64(len(read.chunks[seq])) || chunk["encoding"] != "json" {
				t.Fatalf("event %d chunk does not continue where the last ended (%d bytes): %v", seq, len(read.chunks[seq]), chunk)
			}
			read.chunks[seq] += chunk["content"].(string)
			read.chunked[seq]++
		}
		next, _ := envelope["next"].(map[string]any)
		if data["has_more"] == true && next == nil {
			t.Fatalf("durable_log answer has more but no next: %v", envelope)
		}
		if next == nil {
			return read
		}
		if next["action"] != "search" || next["search_mode"] != "durable_log" || next["session_id"] != first["session_id"] || next["query"] != first["query"] || next["max_chars"] != budget || next["machine"] != first["machine"] {
			t.Fatalf("next is not the same durable_log search: %v", next)
		}
		was, _ := json.Marshal(call)
		now, _ := json.Marshal(next)
		if string(was) == string(now) {
			t.Fatalf("next repeats the call it follows: %s", now)
		}
		read.nexts = append(read.nexts, next)
		call = next
	}
}

// durableLogCheckCoverage checks a chain returned every matching event
// exactly once (whole, or in chunks that concatenate to its exact JSON).
func durableLogCheckCoverage(t *testing.T, read durableLogRead, want map[uint64]pebblestore.V3SessionEvent) {
	t.Helper()
	for seq, count := range read.whole {
		if _, ok := want[seq]; !ok || count != 1 || read.chunked[seq] != 0 {
			t.Fatalf("event %d returned %d times whole and %d chunked (matching %v)", seq, count, read.chunked[seq], ok)
		}
	}
	bigSeen := false
	for seq, content := range read.chunks {
		event, ok := want[seq]
		if !ok {
			t.Fatalf("chunked event %d does not match", seq)
		}
		var got pebblestore.V3SessionEvent
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatalf("event %d chunks do not concatenate to JSON: %v", seq, err)
		}
		encoded, _ := json.Marshal(event)
		if content != string(encoded) || got.Seq != seq || got.EventType != event.EventType {
			t.Fatalf("event %d chunks are not its exact encoding", seq)
		}
		var payload any
		if err := json.Unmarshal(got.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if durableLogHasString(payload, durableLogBig) {
			bigSeen = true
			if read.chunked[seq] < 2 {
				t.Fatalf("the big event came in %d chunk(s), want it cut", read.chunked[seq])
			}
		}
	}
	if !bigSeen {
		t.Fatal("the big message's event was never returned in chunks")
	}
	for seq := range want {
		if read.whole[seq]+min(read.chunked[seq], 1) != 1 {
			t.Fatalf("matching event %d was not returned exactly once (whole %d, chunked %d)", seq, read.whole[seq], read.chunked[seq])
		}
	}
}

// Purpose:
//   - Requirement: manage_sessions search search_mode durable_log returns
//     every matching event of an owned session within max_chars, each once,
//     and always makes progress: a matching event larger than the budget
//     comes back as event_chunk (its exact JSON, cut on a character
//     boundary) with next naming event_seq and content_offset, and after its
//     last chunk next moves before it. The Orchestrator, Swarm Control and a
//     linked machine (machine=) answer and continue the same way.
//   - Threat/regression: an oversized first match answered events=[] with
//     character_truncated and next_before_seq = its seq + 1, so following
//     the continuation re-read the same event forever and its data could
//     never be read; a continuation dropped by the MCP envelope because the
//     search action did not declare its inputs; chunk offsets that split a
//     UTF-8 sequence; a continuation reading a non-matching event or one
//     cleared from the caller's own context.
//   - Boundary: Runtime.manageSessionsDurableLogSearch through
//     executeManageSessions and the catalog envelope (controlMCPCall.callTool
//     and the Orchestrator's tool call), CallFleetCatalog for machine=, over
//     real session stores.
//   - Narrowest layer: the envelope's next and the linked passthrough exist
//     only above the runtime; no provider runs.
func TestManageSessionsDurableLogSearchReadsOversizedEvents(t *testing.T) {
	h := newCatalogParityHarness(t)
	durableLogSeed(t, h.home, "ms-dlog")
	durableLogSeed(t, h.remote, "rms-dlog")
	want := durableLogMatching(t, h.home, "ms-dlog")
	var bigSeq, otherSeq uint64
	for seq, event := range want {
		var payload any
		_ = json.Unmarshal(event.Payload, &payload)
		if durableLogHasString(payload, durableLogBig) {
			bigSeq = seq
		}
	}
	all, err := h.home.s.sessions.ListSessionEventsBefore("ms-dlog", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range all {
		if _, ok := want[event.Seq]; !ok && otherSeq == 0 {
			otherSeq = event.Seq
		}
	}
	if bigSeq == 0 || otherSeq == 0 || len(want) < 3 {
		t.Fatalf("fixture: big %d, other %d, %d matching events", bigSeq, otherSeq, len(want))
	}
	const budget = float64(1500)
	search := func(more ...any) map[string]any {
		return parityArgs(append([]any{"action", "search", "search_mode", "durable_log", "session_id", "ms-dlog", "query", durableLogNeedle, "limit", float64(8), "max_chars", budget}, more...)...)
	}

	// The whole log, from the newest event: every match once, the big one
	// in chunks that concatenate to its exact JSON.
	durableLogCheckCoverage(t, durableLogFollow(t, h, search()), want)

	// The reported case: the oversized event is the page's first match.
	// The first answer is its first chunk and next continues it, never the
	// same before_seq again.
	first := durableLogFollow(t, h, search("before_seq", float64(bigSeq+1)))
	if len(first.nexts) == 0 || first.nexts[0]["event_seq"] != float64(bigSeq) || first.nexts[0]["content_offset"] == nil || first.nexts[0]["before_seq"] != nil {
		t.Fatalf("an oversized first match did not continue within the event: %v", first.nexts)
	}
	for seq := range first.whole {
		if seq >= bigSeq {
			t.Fatalf("a page before %d returned event %d", bigSeq+1, seq)
		}
	}
	if first.chunked[bigSeq] < 2 {
		t.Fatalf("the oversized first match was not read in chunks: %v", first.chunked)
	}

	// The same chain on the linked machine (machine= passthrough): every
	// next keeps machine and the remote log is read whole.
	remote := search("machine", "box", "session_id", "rms-dlog")
	durableLogCheckCoverage(t, durableLogFollow(t, h, remote), durableLogMatching(t, h.remote, "rms-dlog"))

	// Continuations that name no matching event, mix in before_seq or cut a
	// character are refused before anything is returned.
	encoded, _ := json.Marshal(want[bigSeq])
	inRune := strings.Index(string(encoded), "é") + 1
	for name, args := range map[string]map[string]any{
		"non-matching event":    search("event_seq", float64(otherSeq), "content_offset", float64(0)),
		"missing event":         search("event_seq", float64(bigSeq+10000), "content_offset", float64(0)),
		"offset without event":  search("content_offset", float64(10)),
		"event with before_seq": search("event_seq", float64(bigSeq), "content_offset", float64(0), "before_seq", float64(bigSeq)),
		"offset inside a rune":  search("event_seq", float64(bigSeq), "content_offset", float64(inRune)),
		"offset past the event": search("event_seq", float64(bigSeq), "content_offset", float64(len(encoded)+1)),
		"zero event_seq":        search("event_seq", float64(0)),
	} {
		if _, err := h.orchestratorTool(t, "manage_sessions", args); err == nil {
			t.Fatalf("%s: continuation accepted", name)
		}
		if _, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", args); refused == "" {
			t.Fatalf("%s: continuation accepted over Swarm Control", name)
		}
	}
}

// Purpose:
//   - Requirement: a conversation's durable_log search of its own session
//     stops at its context clear, for page reads and event_seq chunk
//     continuations alike.
//   - Threat/regression: event_seq naming an event from before the clear
//     reading cleared history back into the conversation.
//   - Boundary: Runtime.selfReadContextBoundary as applied by
//     manageSessionsDurableLogSearch, over a real store with a real
//     V3SessionMutationClearContext.
//   - Narrowest layer: the native tool call scoped to the session itself.
func TestManageSessionsDurableLogSearchKeepsTheContextClear(t *testing.T) {
	h := newCatalogParityHarness(t)
	const session = "ms-dlog-clear"
	// Context clears are a project conversation's.
	if err := h.home.s.sessions.Store().PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "dlog-project", Name: "Durable log project"}); err != nil {
		t.Fatal(err)
	}
	snapshot := pebblestore.SessionSnapshot{ID: session, UserID: h.home.user, AccountScopeID: h.home.account, Title: fleetActivityTestTitle, Metadata: map[string]any{"swarm_v3_project_id": "dlog-project", "project_id": "dlog-project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}}
	if _, err := h.home.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session, UserID: h.home.user, AccountScopeID: h.home.account, Session: &snapshot, ClientRequestID: session, IdempotencyKey: session, PayloadHash: session, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(id, content string) {
		t.Helper()
		m := pebblestore.MessageSnapshot{ID: id, Role: "user", Content: content}
		if _, err := h.home.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: id, PayloadHash: id, Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("before-clear", durableLogNeedle+" cleared "+durableLogBig)
	cleared := durableLogMatching(t, h.home, session)
	latest, err := h.home.s.sessions.ListSessionEventsBefore(session, 0, 1)
	if err != nil || len(latest) != 1 {
		t.Fatalf("latest event: %v %v", latest, err)
	}
	seq := latest[0].Seq
	if _, err := h.home.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationClearContext, ClientRequestID: "clear", PayloadHash: "clear", ExpectedLastEventSeq: &seq}); err != nil {
		t.Fatal(err)
	}
	appendMessage("after-clear", durableLogNeedle+" kept")
	self := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		raw, _ := json.Marshal(args)
		out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: h.p}, tool.Call{Name: "manage-sessions", Arguments: string(raw)})
		if err != nil {
			return nil, err
		}
		data, err := tool.EnvelopeData(out)
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		return view, nil
	}
	search := map[string]any{"action": "search", "search_mode": "durable_log", "session_id": session, "query": durableLogNeedle, "max_chars": float64(1500)}
	view, err := self(search)
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; ; step++ {
		if step > 20 {
			t.Fatal("paging did not finish")
		}
		for _, raw := range view["events"].([]any) {
			if _, old := cleared[uint64(raw.(map[string]any)["seq"].(float64))]; old {
				t.Fatalf("a page returned a cleared event: %v", raw)
			}
		}
		if chunk, ok := view["event_chunk"].(map[string]any); ok {
			if _, old := cleared[uint64(chunk["seq"].(float64))]; old {
				t.Fatalf("a chunk returned a cleared event: %v", chunk)
			}
		}
		next, _ := view["next"].(map[string]any)
		if next == nil {
			break
		}
		if view, err = self(next); err != nil {
			t.Fatal(err)
		}
	}
	for old := range cleared {
		if _, err := self(map[string]any{"action": "search", "search_mode": "durable_log", "session_id": session, "query": durableLogNeedle, "max_chars": float64(1500), "event_seq": float64(old), "content_offset": float64(0)}); err == nil || !strings.Contains(err.Error(), "not a matching event") {
			t.Fatalf("event_seq %d from before the clear was read back: %v", old, err)
		}
	}
}

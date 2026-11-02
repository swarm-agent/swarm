package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// compactionRecordingRunner is the real run service with RunTurn compaction
// requests recorded and answered by a simulated failure instead of running a
// memory compaction (which would need a second scripted model). Every other
// method, including the optional interfaces the executor asserts, is the real
// service's.
type compactionRecordingRunner struct {
	*runruntime.Service
	mu       sync.Mutex
	compacts []string
}

func (r *compactionRecordingRunner) RunTurn(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	if request.Compact {
		r.mu.Lock()
		r.compacts = append(r.compacts, sessionID+":"+request.CompactOrigin)
		r.mu.Unlock()
		return runruntime.RunResult{}, errors.New("simulated compaction recorded by the test")
	}
	return r.Service.RunTurn(ctx, sessionID, request, meta)
}

func (r *compactionRecordingRunner) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.compacts...)
}

// companionStoreReads counts the session's message records read by store
// range scans while installed.
type companionStoreReads struct {
	mu      sync.Mutex
	prefix  []string
	records int
}

func observeSessionMessageReads(t *testing.T, sessionID string) *companionStoreReads {
	t.Helper()
	reads := &companionStoreReads{prefix: []string{pebblestore.V3SessionMessagePrefix(sessionID), pebblestore.MessagePrefix(sessionID)}}
	t.Cleanup(pebblestore.ObserveScanReadsForTest(func(key string, _ int) {
		for _, prefix := range reads.prefix {
			if strings.HasPrefix(key, prefix) {
				reads.mu.Lock()
				reads.records++
				reads.mu.Unlock()
				return
			}
		}
	}))
	return reads
}

func (r *companionStoreReads) take() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.records
	r.records = 0
	return n
}

// withoutRequestTime blanks the per-request "- current_utc_time: ..." line
// of the request runtime context, which differs between any two requests.
func withoutRequestTime(instructions string) string {
	lines := strings.Split(instructions, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "- current_utc_time: ") {
			lines[i] = "- current_utc_time: <request time>"
		}
	}
	return strings.Join(lines, "\n")
}

func providerInputBytes(req provideriface.Request) int {
	raw, _ := json.Marshal(req.Input)
	return len(raw)
}

// assertProviderToolPairing fails when a request carries a tool result
// without its call before it, or a call without its result.
func assertProviderToolPairing(t *testing.T, label string, input []map[string]any) {
	t.Helper()
	calls := map[string]bool{}
	for i, item := range input {
		id := stringMapValue(item, "call_id")
		switch stringMapValue(item, "type") {
		case "function_call":
			calls[id] = false
		case "function_call_output":
			answered, ok := calls[id]
			if !ok || answered {
				t.Fatalf("%s: tool result %q at %d has no preceding call", label, id, i)
			}
			calls[id] = true
		}
	}
	for id, answered := range calls {
		if !answered {
			t.Fatalf("%s: tool call %q was sent without its result", label, id)
		}
	}
}

// seedCompanionHistory appends n stored messages (user, assistant and
// self-contained tool records in turn, about 2 KB each) to the session as its
// owner, without starting runs.
func seedCompanionHistory(t *testing.T, f *queuedNoteFixture, n int) {
	t.Helper()
	p := testPrincipal()
	filler := strings.Repeat("printer spooler diagnostics ", 72)
	for i := 0; i < n; i++ {
		message := pebblestore.MessageSnapshot{Role: "user", Content: fmt.Sprintf("Question %d: %s", i, filler)}
		switch i % 3 {
		case 1:
			message = pebblestore.MessageSnapshot{Role: "assistant", Content: fmt.Sprintf("Answer %d: %s", i, filler)}
		case 2:
			raw, _ := json.Marshal(map[string]any{"path_id": "run.v3.provider-tool-result.v1", "type": "tool_result", "tool_name": "bash", "call_id": fmt.Sprintf("seed-call-%d", i), "arguments": `{"command":"lpstat"}`, "output": filler})
			message = pebblestore.MessageSnapshot{Role: "tool", Content: string(raw)}
		}
		id := fmt.Sprintf("seed-%d", i)
		if _, err := f.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: f.sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: id, PayloadHash: id, Kind: pebblestore.V3SessionMutationAppendMessage, Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *queuedNoteFixture) post(sessionID, requestID, content string) string {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": content})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
		f.t.Fatalf("message: %d %s", code, out)
	}
	return stableSessionsV3PrimaryRunID(sessionID, requestID)
}

// Purpose: a Companion's context overflow is recovered with bounded reads
// only and never by whole-history compaction. Requirement: when the provider
// rejects a Companion request as a context overflow, the V3 executor retries
// once at each smaller fixed budget of runruntime.CompanionContextBudgetAt
// (fresh provider chain, unchanged instructions, the context bank block
// present, every tool result with its call), reading only a bounded number of
// the Companion's message records however long its history is; it never asks
// the run service to compact the Companion; when every budget still
// overflows the run fails once, honestly, with a clear reason and no further
// request; after a simulated daemon restart (a new executor) the next turn
// again starts from the ordinary bounded budget and recovers. An ordinary
// chat's overflow still takes the existing RunTurn overflow compaction path.
// Threat: overflow recovery scanning the unbounded Companion transcript
// (session.MessageCount + slack), a retry loop or forever-running turn, lost
// instructions, orphaned tool results, or a regression of ordinary chats.
// Boundary: sessionV3Executor.run's overflow branch,
// sessionV3IsCompanionOverflow, companionOverflowRecoveredAssistantResponse,
// sessionV3CompanionContextMessages / CompanionV3ContextMessagesAtLevel,
// contextOverflowCompactedAssistantResponse. Simulated parts: the provider
// is a deterministic double that answers "prompt is too long" when a
// request's input exceeds a fixed byte window, and RunTurn compaction is
// recorded and failed rather than run. Narrowest layer: the real HTTP routes,
// V3 executor and Pebble store, since the contract spans the executor's retry
// loop, context assembly and store reads.
func TestCompanionOverflowRecoveryStaysBoundedAndNeverCompacts(t *testing.T) {
	const reply = "recovered: the printer uses the vendor driver"
	f := newCompanionRunFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: reply, StopReason: "stop"}
	})
	recorder := &compactionRecordingRunner{Service: f.server.runner.(*runruntime.Service)}
	f.server.runner = recorder
	companionID := f.sessionID
	plainID := f.createSession("plain-chat")

	var windowMu sync.Mutex
	window := 16 * 1024
	setWindow := func(bytes int) { windowMu.Lock(); window = bytes; windowMu.Unlock() }
	f.stepErr = func(req provideriface.Request) error {
		windowMu.Lock()
		limit := window
		windowMu.Unlock()
		if size := providerInputBytes(req); size > limit {
			return fmt.Errorf("prompt is too long: %d tokens > %d maximum", size/4, limit/4)
		}
		return nil
	}
	const history = 3000
	seedCompanionHistory(t, f, history)
	reads := observeSessionMessageReads(t, companionID)
	levels := runruntime.CompanionContextBudgetLevels
	// Per attempt: the tail scan, the context bank's source excerpts and the
	// executor's fixed queued-note windows; far below the seeded history.
	readBound := levels * (60 + 2*sessionV3QueuedNoteHistoryWindow)
	if readBound >= history {
		t.Fatalf("read bound %d does not prove the absence of a %d-message scan", readBound, history)
	}
	companionRequests := func(from int) []provideriface.Request {
		var out []provideriface.Request
		for _, req := range f.requests()[from:] {
			if req.SessionID == companionID {
				out = append(out, req)
			}
		}
		return out
	}

	// 1. Recovery: the ordinary and first smaller budgets overflow, the
	// smallest fits.
	f.say(companionID, "start-1", "back to the printer: which driver did we pick?")
	requests := companionRequests(0)
	if len(requests) != levels {
		t.Fatalf("provider requests = %d, want one per budget level (%d)", len(requests), levels)
	}
	for i, req := range requests {
		label := fmt.Sprintf("attempt %d", i)
		budget, _ := runruntime.CompanionContextBudgetAt(i)
		size := providerInputBytes(req)
		if maxBytes := budget.TailBytes + budget.ContextBlockBytes + budget.TailMessages*1024; size > maxBytes {
			t.Fatalf("%s sent %d input bytes, over its budget bound %d", label, size, maxBytes)
		}
		if i > 0 && size >= providerInputBytes(requests[i-1]) {
			t.Fatalf("%s did not shrink: %d bytes after %d", label, size, providerInputBytes(requests[i-1]))
		}
		if (i < levels-1) != (size > 16*1024) {
			t.Fatalf("%s: %d bytes against the 16 KiB window (only the last attempt should fit)", label, size)
		}
		// Each provider request stamps its own current_utc_time; everything
		// else in the instructions must be identical across attempts.
		if strings.TrimSpace(req.Instructions) == "" || withoutRequestTime(req.Instructions) != withoutRequestTime(requests[0].Instructions) {
			t.Fatalf("%s changed or dropped the run's instructions", label)
		}
		if _, users := countInInput(req.Input, "Companion context bank"); users != 1 {
			t.Fatalf("%s lacks the bounded context bank block", label)
		}
		if i > 0 && (!req.StartNewChain || req.AllowContinuation) {
			t.Fatalf("%s reused the overflowing provider chain", label)
		}
		assertProviderToolPairing(t, label, req.Input)
	}
	if n := reads.take(); n > readBound {
		t.Fatalf("recovery read %d of %d Companion message records (bound %d)", n, history, readBound)
	} else {
		sizes := make([]int, 0, len(requests))
		for _, req := range requests {
			sizes = append(sizes, providerInputBytes(req))
		}
		t.Logf("recovery: request input bytes %v; read %d of %d message records (bound %d)", sizes, n, history, readBound)
	}
	if got := f.latest(companionID, "assistant"); got != reply {
		t.Fatalf("recovered reply = %q", got)
	}
	reads.take()

	// 2. Honest failure: every budget overflows; the run fails once.
	setWindow(512)
	before := len(f.requests())
	failedRun := f.post(companionID, "start-2", "and the spooler fix?")
	f.waitRun(companionID, failedRun, sessionruntime.RunIntentFailed)
	if n := reads.take(); n > readBound {
		t.Fatalf("failed recovery read %d Companion message records (bound %d)", n, readBound)
	}
	if got := len(companionRequests(before)); got != levels {
		t.Fatalf("failing run made %d provider requests, want exactly %d (no loop)", got, levels)
	}
	intent, _, _ := f.sessions.GetSessionRunIntent(companionID, failedRun)
	failure := intent.BlockedReason + " | " + f.latest(companionID, "system")
	if !strings.Contains(failure, "companion context overflow") || !strings.Contains(failure, "without compacting") {
		t.Fatalf("failure reason = %q, want the bounded Companion overflow reason", failure)
	}
	reads.take()

	// 3. Restart: a new executor (in-memory state lost) runs the next turn
	// from the ordinary budget, bounded, and recovers.
	exec := newSessionV3Executor(f.server)
	exec.startDelay = 0
	f.server.v3SessionExecutor = exec
	setWindow(16 * 1024)
	before = len(f.requests())
	f.say(companionID, "start-3", "one more printer question")
	restarted := companionRequests(before)
	if len(restarted) != levels || providerInputBytes(restarted[0]) <= 16*1024 {
		t.Fatalf("after restart: %d requests, first %d bytes; want a fresh ordinary-budget start and %d attempts", len(restarted), providerInputBytes(restarted[0]), levels)
	}
	if n := reads.take(); n > readBound {
		t.Fatalf("after restart the turn read %d Companion message records (bound %d)", n, readBound)
	}
	for _, compact := range recorder.recorded() {
		if strings.HasPrefix(compact, companionID+":") {
			t.Fatalf("the Companion was sent to whole-history compaction: %v", recorder.recorded())
		}
	}

	// 4. Contrast: an ordinary chat's overflow still takes the existing
	// RunTurn overflow compaction path.
	setWindow(1)
	plainRun := f.post(plainID, "plain-1", "plain lookup please")
	f.waitRun(plainID, plainRun, sessionruntime.RunIntentFailed)
	if got := recorder.recorded(); len(got) != 1 || got[0] != plainID+":overflow" {
		t.Fatalf("ordinary chat compaction calls = %v, want one overflow compaction", got)
	}
	plainIntent, _, _ := f.sessions.GetSessionRunIntent(plainID, plainRun)
	if failure := plainIntent.BlockedReason + " | " + f.latest(plainID, "system"); !strings.Contains(failure, "v3 context overflow compact failed") {
		t.Fatalf("ordinary chat failure = %q, want the existing compaction path's error", failure)
	}
}

// latest returns the content of the session's newest message with role,
// read from a bounded tail.
func (f *queuedNoteFixture) latest(sessionID, role string) string {
	f.t.Helper()
	messages, err := f.sessions.ListSessionMessageTail(sessionID, 20)
	if err != nil {
		f.t.Fatal(err)
	}
	var best pebblestore.MessageSnapshot
	for _, message := range messages {
		if message.Role == role && message.GlobalSeq >= best.GlobalSeq {
			best = message
		}
	}
	return best.Content
}

// Purpose: one oversized message cannot push a Companion request past its
// budget, on the ordinary turn or on any overflow retry. Requirement: after
// a long history, the person pastes a message several times larger than the
// whole tail budget; the V3 executor's request at each attempt stays within
// that level's bound, carries the start of the message with a pointer to the
// original (never the whole message), keeps the context block and tool
// pairing, and the turn completes; with a small provider window the ladder
// still recovers at a smaller level instead of failing on the pasted text.
// Threat: the bounded tail read always admitting its newest record whole, so
// a pasted log overflows every attempt. Simulated parts: the provider is the
// deterministic double of TestCompanionOverflowRecoveryStaysBoundedAndNeverCompacts.
// Boundary: sessionV3CompanionContextMessages → CompanionV3ContextMessagesAtLevel
// (boundCompanionTail) and the executor's overflow ladder. Narrowest layer:
// the real HTTP route, executor and store, since the bound must hold on the
// request the provider actually receives.
func TestCompanionOversizedMessageStaysWithinRequestBudget(t *testing.T) {
	const reply = "got the log"
	f := newCompanionRunFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: reply, StopReason: "stop"}
	})
	companionID := f.sessionID
	var windowMu sync.Mutex
	window := 1 << 30
	setWindow := func(bytes int) { windowMu.Lock(); window = bytes; windowMu.Unlock() }
	f.stepErr = func(req provideriface.Request) error {
		windowMu.Lock()
		limit := window
		windowMu.Unlock()
		if size := providerInputBytes(req); size > limit {
			return fmt.Errorf("prompt is too long: %d tokens > %d maximum", size/4, limit/4)
		}
		return nil
	}
	seedCompanionHistory(t, f, 600)
	pasted := "Here is the whole build log: " + strings.Repeat("ERROR cache miss at layer 42; ", 12000)
	companionRequests := func(from int) []provideriface.Request {
		var out []provideriface.Request
		for _, req := range f.requests()[from:] {
			if req.SessionID == companionID {
				out = append(out, req)
			}
		}
		return out
	}
	check := func(label string, requests []provideriface.Request) {
		t.Helper()
		for i, req := range requests {
			budget, _ := runruntime.CompanionContextBudgetAt(i)
			size := providerInputBytes(req)
			if maxBytes := budget.TailBytes + budget.ContextBlockBytes + budget.TailMessages*1024; size > maxBytes || size >= len(pasted) {
				t.Fatalf("%s attempt %d sent %d input bytes (bound %d, pasted %d)", label, i, size, maxBytes, len(pasted))
			}
			if _, n := countInInput(req.Input, "only its start is shown"); n != 1 {
				t.Fatalf("%s attempt %d does not point to the truncated original", label, i)
			}
			if _, n := countInInput(req.Input, "Companion context bank"); n != 1 {
				t.Fatalf("%s attempt %d lacks the context block", label, i)
			}
			assertProviderToolPairing(t, fmt.Sprintf("%s attempt %d", label, i), req.Input)
		}
	}

	f.say(companionID, "paste-1", pasted)
	requests := companionRequests(0)
	if len(requests) != 1 {
		t.Fatalf("ordinary turn made %d requests", len(requests))
	}
	check("ordinary", requests)
	if got := f.latest(companionID, "assistant"); got != reply {
		t.Fatalf("reply = %q", got)
	}

	setWindow(16 * 1024)
	before := len(f.requests())
	f.say(companionID, "paste-2", pasted)
	requests = companionRequests(before)
	if len(requests) != runruntime.CompanionContextBudgetLevels {
		t.Fatalf("pressured turn made %d requests, want one per level", len(requests))
	}
	check("pressured", requests)
	if last := providerInputBytes(requests[len(requests)-1]); last > 16*1024 {
		t.Fatalf("smallest attempt %d bytes still over the window", last)
	}
	if got := f.latest(companionID, "assistant"); got != reply {
		t.Fatalf("pressured reply = %q", got)
	}
}

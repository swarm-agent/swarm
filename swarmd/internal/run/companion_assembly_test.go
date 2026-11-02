package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// companionScanReads counts every key the store's range scans visit while
// installed, split by the Companion session's message and event prefixes.
type companionScanReads struct {
	mu       sync.Mutex
	messages string
	events   string
	keys     int
	bytes    int
	msgKeys  int
	evKeys   int
}

func observeCompanionScanReads(t *testing.T, sessionID string) *companionScanReads {
	t.Helper()
	reads := &companionScanReads{messages: pebblestore.V3SessionMessagePrefix(sessionID), events: pebblestore.V3SessionEventPrefix(sessionID)}
	restore := pebblestore.ObserveScanReadsForTest(func(key string, valueBytes int) {
		reads.mu.Lock()
		defer reads.mu.Unlock()
		reads.keys++
		reads.bytes += valueBytes
		switch {
		case strings.HasPrefix(key, reads.messages):
			reads.msgKeys++
		case strings.HasPrefix(key, reads.events):
			reads.evKeys++
		}
	})
	t.Cleanup(restore)
	return reads
}

type companionScanCount struct{ keys, bytes, messages, events int }

func (r *companionScanReads) take() companionScanCount {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := companionScanCount{r.keys, r.bytes, r.msgKeys, r.evKeys}
	r.keys, r.bytes, r.msgKeys, r.evKeys = 0, 0, 0, 0
	return out
}

func (f *companionStateFixture) assemble(level int, roots []string) CompanionV3Context {
	f.t.Helper()
	turn, err := CompanionV3ContextAtLevel(f.db, f.session, roots, 0, 5000, level)
	if err != nil {
		f.t.Fatalf("assemble level %d: %v", level, err)
	}
	if len(turn.Messages) == 0 || turn.Messages[0].Role != "system" || !strings.HasPrefix(turn.Messages[0].Content, companionContextBlockHeader) {
		f.t.Fatalf("level %d: no context block first", level)
	}
	return turn
}

// companionAssemblyScanBound is the most keys one assembly's range scans may
// visit, from the fixed limits alone (none depends on history size):
// maintenance with nothing pending (one empty event page), the tail scan, the
// unfolded-gap scan, the context bank's keyword and kind index scans, the
// segment keyword and recency scans, segment source checks and the active
// task-watch list (none in these fixtures).
func companionAssemblyScanBound(budget CompanionContextBudget) int {
	return 1 + // maintenance event page (empty)
		budget.TailMessages + 1 +
		companionUnfoldedReadMessages + 1 +
		pebblestore.CompanionContextMaxQueryKeywords*pebblestore.CompanionContextIndexScanPerKeyword + 2*(companionDecisionMax+companionWorkMax) +
		pebblestore.CompanionContextMaxQueryKeywords*pebblestore.CompanionSegmentIndexScanPerKeyword + 2*1 + 8 +
		(2*companionSegmentsMax+1)*pebblestore.CompanionSegmentMaxItems +
		2*(companionRetrievedMax+companionDecisionMax+companionWorkMax) +
		16 // fixed point lookups done through scans (identity, marks)
}

// Purpose: each Companion turn's storage reads and input size stay fixed as
// history grows, at every budget level, including the run-start maintenance
// of pending events. Requirement: CompanionV3ContextAtLevel (the history the
// V3 executor sends, and each overflow retry) visits at most
// companionAssemblyScanBound keys of range scans and reports a history of at
// most TailBytes+ContextBlockBytes; the same measurement after the history
// grows about thirtyfold stays under the same bound; a backlog of pending
// events is folded at most CompanionMaintainRunStartEvents per turn (the rest
// reported pending, not read). Threat: a per-turn read or prompt that grows
// with the conversation (scoring every topic, reading the whole log to catch
// up). Boundary: CompanionV3ContextAtLevel, loadCompanionContinuity,
// MaintainCompanionState, RetrieveCompanionContext,
// RetrieveCompanionSegmentsInScope over the real Pebble store, measured with
// the store's scan read observer and the assembly report. Narrowest layer:
// the assembly function every Companion provider request uses.
func TestCompanionAssemblyReadsStayFixedAsHistoryGrows(t *testing.T) {
	f := newCompanionStateFixture(t)
	filler := strings.Repeat("details about the drip emitters and soil moisture ", 4)
	exchange := func(from, to int) {
		for i := from; i < to; i++ {
			f.ask(fmt.Sprintf("Garden irrigation question %d about zone %d: %s", i, i%7, filler))
			f.reply(fmt.Sprintf("Irrigation answer %d: %s", i, filler))
		}
		f.drain(pebblestore.CompanionSegmentMaxItems * 64)
	}
	measure := func(label string) []companionScanCount {
		reads := observeCompanionScanReads(t, f.session.ID)
		out := make([]companionScanCount, CompanionContextBudgetLevels)
		for level := 0; level < CompanionContextBudgetLevels; level++ {
			budget := companionContextBudgets[level]
			reads.take()
			turn := f.assemble(level, nil)
			got := reads.take()
			out[level] = got
			r := turn.Report
			if bound := companionAssemblyScanBound(budget); got.keys > bound {
				t.Fatalf("%s level %d visited %d keys, bound %d (report %+v)", label, level, got.keys, bound, r)
			}
			if r.HistoryBytes > budget.TailBytes+budget.ContextBlockBytes || r.TailBytes > budget.TailBytes || r.BlockBytes > budget.ContextBlockBytes || r.TailMessages > budget.TailMessages {
				t.Fatalf("%s level %d history %d bytes (tail %d/%d msgs %d, block %d/%d)", label, level, r.HistoryBytes, r.TailBytes, budget.TailBytes, r.TailMessages, r.BlockBytes, budget.ContextBlockBytes)
			}
			if r.Segments.Segments > 2*companionSegmentsMax+1 || r.Records.Records > 2*(companionRetrievedMax+companionDecisionMax+companionWorkMax) || r.GapMessages > companionUnfoldedReadMessages {
				t.Fatalf("%s level %d report reads %+v", label, level, r)
			}
			if r.Continuity.Reason != "" || r.Continuity.UnfoldedTo != 0 {
				t.Fatalf("%s level %d continuity %+v, want current", label, level, r.Continuity)
			}
			t.Logf("%s level %d: %d scan keys (%d message, %d event), %d bytes read; history %d bytes (tail %d, block %d); segments %d, records %d",
				label, level, got.keys, got.messages, got.events, got.bytes, r.HistoryBytes, r.TailBytes, r.BlockBytes, r.Segments.Segments, r.Records.Records)
		}
		return out
	}
	exchange(0, 40)
	f.ask("Back to irrigation zone 3: which emitters did we pick?")
	f.drain(0)
	small := measure("80 messages")

	exchange(40, 1300)
	f.ask("Back to irrigation zone 3: which emitters did we pick?")
	f.drain(0)
	head := f.head()
	if head < 2500 {
		t.Fatalf("history has %d events, want thousands", head)
	}
	large := measure(fmt.Sprintf("%d events", head))
	for level := range large {
		if large[level].messages > small[level].messages+companionUnfoldedReadMessages {
			t.Fatalf("level %d message reads grew with history: %d -> %d", level, small[level].messages, large[level].messages)
		}
	}

	// A backlog: the turn folds a bounded batch and reports the rest
	// pending; it never reads the whole backlog.
	for i := 0; i < 300; i++ {
		f.message("session.assistant.completed", "assistant", fmt.Sprintf("progress note %d", i), nil)
	}
	reads := observeCompanionScanReads(t, f.session.ID)
	turn := f.assemble(0, nil)
	got := reads.take()
	if got.events > CompanionMaintainRunStartEvents+1 {
		t.Fatalf("run-start maintenance read %d events, bound %d", got.events, CompanionMaintainRunStartEvents+1)
	}
	if c := turn.Report.Continuity; c.Reason != "behind" || c.Pending != 300-CompanionMaintainRunStartEvents {
		t.Fatalf("backlog continuity %+v, want behind with %d pending", c, 300-CompanionMaintainRunStartEvents)
	}
	if !strings.Contains(turn.Messages[0].Content, fmt.Sprintf("%d newer events are not folded yet", 300-CompanionMaintainRunStartEvents)) {
		t.Fatalf("block does not say continuity is behind:\n%s", turn.Messages[0].Content)
	}
}

// Purpose: returning to older work outside the recent tail brings back what
// was said and decided about it, from the original events, and nothing that
// was forgotten. Requirement: after hundreds of messages on another topic, a
// turn about the early topic carries (a) the early request as relevant
// earlier context with its message number and source excerpt and (b) the
// Companion's early reply as a continuity item citing its message number
// with assistant authority, neither of which is in the tail; the precedence
// rule is stated; once the early request's topic is forgotten, neither the
// request nor the reply about it is assembled. Threat: a Companion that only
// knows its recent tail, or that keeps serving forgotten text. Boundary:
// CompanionV3ContextAtLevel, RetrieveCompanionSegmentsInScope,
// RetrieveCompanionContext, DeleteCompanionContext propagation. Narrowest
// layer: the real assembly over the real store with continuity built by the
// real maintainer.
func TestCompanionAssemblyReturnsToOlderWorkThroughContinuity(t *testing.T) {
	f := newCompanionStateFixture(t)
	ask := f.ask("Plan the Postgres inventory migration for the warehouse service")
	reply := f.reply("Agreed: run the Postgres inventory migration on Sunday night after a full dump.")
	filler := strings.Repeat("notes on drip emitters ", 6)
	for i := 0; i < 150; i++ {
		f.ask(fmt.Sprintf("Garden irrigation question %d: %s", i, filler))
		f.reply(fmt.Sprintf("Irrigation answer %d: %s", i, filler))
	}
	f.drain(0)
	back := f.ask("Back to the Postgres inventory migration: when do we run it?")
	f.drain(0)

	turn := f.assemble(0, nil)
	tail := turn.Messages[1:]
	if tail[0].GlobalSeq <= reply.GlobalSeq || tail[len(tail)-1].GlobalSeq != back.GlobalSeq {
		t.Fatalf("tail #%d..#%d should not reach the early exchange (#%d)", tail[0].GlobalSeq, tail[len(tail)-1].GlobalSeq, reply.GlobalSeq)
	}
	block := turn.Messages[0].Content
	for _, want := range []string{
		"Precedence: the person's messages in the recent conversation override everything here",
		"Relevant earlier context:",
		fmt.Sprintf("message #%d", ask.GlobalSeq),
		"Plan the Postgres inventory migration",
		"Earlier conversation (continuity",
		fmt.Sprintf("[message #%d, reply, assistant]", reply.GlobalSeq),
		"run the Postgres inventory migration on Sunday night",
		"Continuity: current through event #",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block lacks %q:\n%s", want, block)
		}
	}
	if turn.Report.Shown["continuity"] == 0 || turn.Report.Shown["related"] == 0 {
		t.Fatalf("shown %+v", turn.Report.Shown)
	}

	topic := pebblestore.CompanionMessageContextID(f.session.ID, ask.GlobalSeq)
	rec, ok, err := f.db.GetCompanionContext("acct", "user", topic)
	if err != nil || !ok {
		t.Fatalf("early topic: %v %v", ok, err)
	}
	if err := f.db.DeleteCompanionContext("acct", "user", topic, rec.Revision); err != nil {
		t.Fatal(err)
	}
	forgotten := f.assemble(0, nil).Messages[0].Content
	for _, gone := range []string{"Plan the Postgres inventory migration", "Sunday night", fmt.Sprintf("message #%d sha256", ask.GlobalSeq), fmt.Sprintf("[message #%d,", ask.GlobalSeq), fmt.Sprintf("[message #%d,", reply.GlobalSeq)} {
		if strings.Contains(forgotten, gone) {
			t.Fatalf("forgotten request still assembled (%q):\n%s", gone, forgotten)
		}
	}
}

// Purpose: when continuity lags or its maintenance fails, a turn says so and
// still carries the person's unfolded words; nothing accepted is silently
// dropped and older summaries cannot pose as newer. Requirement: with a
// backlog larger than one run-start batch and a small tail, the block names
// the exact unfolded event range before the tail, quotes the newest person
// messages of it from the original messages and says older ones are not
// shown; with the maintainer failing, the turn still assembles from the
// stored state and says continuity may be stale; owed replies opened before
// the tail are listed. Threat: a stale or missing summary that hides the
// person's latest instructions, or a turn failing outright because background
// maintenance broke. Boundary: loadCompanionContinuity,
// readCompanionUnfoldedGap, companionCommitmentLines,
// renderCompanionContextBlock through CompanionV3ContextAtLevel. Narrowest
// layer: the real assembly with an injected maintenance failure
// (companionMaintainHook).
func TestCompanionAssemblyFallbackKeepsUnfoldedInput(t *testing.T) {
	f := newCompanionStateFixture(t)
	f.ask("Use the blue theme for the dashboard header")
	f.reply("Noted: blue theme for the dashboard header.")
	f.drain(0)
	// The person keeps writing; no reply and no maintenance in between.
	var last pebblestore.MessageSnapshot
	for i := 0; i < 150; i++ {
		last = f.ask(fmt.Sprintf("Instruction %d: remember codeword alpha%d for the deploy", i, i))
	}
	newest := f.ask("Actually switch the dashboard header to the green theme")

	smallest := CompanionContextBudgetLevels - 1
	turn := f.assemble(smallest, nil)
	r := turn.Report
	tail := turn.Messages[1:]
	oldest := tail[0].GlobalSeq
	if tail[len(tail)-1].GlobalSeq != newest.GlobalSeq {
		t.Fatal("the newest instruction is not in the tail")
	}
	c := r.Continuity
	if c.Reason != "behind" || c.Pending == 0 || c.UnfoldedFrom != c.CursorSeq+1 || c.UnfoldedTo != oldest-1 || !c.UnfoldedPartial || c.UnfoldedShown == 0 {
		t.Fatalf("continuity %+v (tail starts #%d)", c, oldest)
	}
	block := turn.Messages[0].Content
	for _, want := range []string{
		fmt.Sprintf("Events #%d–#%d come before the recent conversation and are not in continuity", c.UnfoldedFrom, c.UnfoldedTo),
		"Not yet folded (the person's own words, oldest first):",
		fmt.Sprintf("message #%d: \"Instruction", oldest-1),
		"more not counted",
		"read_messages",
		"Precedence:",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block lacks %q:\n%s", want, block)
		}
	}
	if len(block) > companionContextBudgets[smallest].ContextBlockBytes {
		t.Fatalf("block %d bytes over %d", len(block), companionContextBudgets[smallest].ContextBlockBytes)
	}
	_ = last

	// Ordinary budget, maintenance caught up: replies are still owed for the
	// person's messages before the tail.
	f.drain(0)
	caught := f.assemble(0, nil)
	if caught.Report.Continuity.Reason != "" || !strings.Contains(caught.Messages[0].Content, "reply owed since message #") {
		t.Fatalf("caught-up turn: %+v\n%s", caught.Report.Continuity, caught.Messages[0].Content)
	}

	// Maintenance fails: the turn still assembles, honestly.
	f.ask("One more instruction about the rollout window")
	injected := errors.New("injected maintenance failure")
	companionMaintainHook = func(int) error { return injected }
	failed, err := CompanionV3ContextAtLevel(f.db, f.session, nil, 0, 5000, 0)
	companionMaintainHook = nil
	if err != nil {
		t.Fatalf("a maintenance failure failed the turn: %v", err)
	}
	if fc := failed.Report.Continuity; fc.Reason != "maintenance_failed" || !fc.Available || fc.Pending == 0 {
		t.Fatalf("failed maintenance continuity %+v", fc)
	}
	if !strings.Contains(failed.Messages[0].Content, "maintenance failed this turn") || !strings.Contains(failed.Messages[1+len(failed.Messages[1:])-1].Content, "rollout window") {
		t.Fatalf("failed maintenance block:\n%s", failed.Messages[0].Content)
	}
}

// Purpose: a turn never carries context outside the caller's permission
// scope, including through continuity. Requirement: a work record bound to a
// workspace, and the continuity item that is that record, are assembled only
// when the run's authorized roots include the workspace; with no roots the
// block holds neither, while an unbound earlier topic is still retrieved.
// Threat: continuity segments leaking a workspace's work to a run that may
// not see it. Boundary: RetrieveCompanionContext (AllowWorkspace),
// RetrieveCompanionSegmentsInScope / ListCompanionSegmentsInScope /
// FilterCompanionItemsInScope (companionSegmentScope) through
// CompanionV3ContextAtLevel. Narrowest layer: the real assembly over records
// and segments written by the real maintainer.
func TestCompanionAssemblyKeepsWorkspaceScope(t *testing.T) {
	f := newCompanionStateFixture(t)
	root := t.TempDir()
	f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationCreateSession, Session: &pebblestore.SessionSnapshot{ID: "scoped-work", Mode: "auto"}}, "scoped-work")
	f.ask("Start the Kubernetes cluster upgrade work")
	f.recordContext(pebblestore.CompanionContextRecord{ID: "work-k8s", Kind: pebblestore.CompanionContextKindWork, Title: "Kubernetes cluster upgrade zeta", WorkspacePath: root + "/repo", Source: pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceSession, SessionID: "scoped-work"}})
	f.reply("The Kubernetes cluster upgrade is delegated.")
	for i := 0; i < 40; i++ {
		f.ask(fmt.Sprintf("Unrelated garden question %d about tomato blight", i))
		f.reply(fmt.Sprintf("Garden answer %d", i))
	}
	f.ask("How is the Kubernetes cluster upgrade going?")
	f.drain(0)
	// The work record's tool message must be outside the tail for the
	// check to concern retrieval and continuity, not the tail.
	unscoped := f.assemble(0, nil)
	for _, m := range unscoped.Messages[1:] {
		if strings.Contains(m.Content, "upgrade zeta") {
			t.Fatal("fixture: the work record is still in the tail")
		}
	}
	if strings.Contains(unscoped.Messages[0].Content, "upgrade zeta") {
		t.Fatalf("out-of-scope work assembled:\n%s", unscoped.Messages[0].Content)
	}
	if !strings.Contains(unscoped.Messages[0].Content, "Start the Kubernetes cluster upgrade work") {
		t.Fatalf("the unbound earlier topic was not retrieved:\n%s", unscoped.Messages[0].Content)
	}
	scoped := f.assemble(0, []string{root})
	if !strings.Contains(scoped.Messages[0].Content, "upgrade zeta") {
		t.Fatalf("in-scope work not assembled:\n%s", scoped.Messages[0].Content)
	}
}

// Purpose: no single message breaks a turn's input budget. Requirement: the
// newest message far larger than the tail budget is truncated to at most
// half the tail with a pointer to the original (person text) or summarized
// or left out whole (tool record) at every level, the tail stays within
// TailBytes, the history within TailBytes+ContextBlockBytes, and the history
// shrinks at each smaller level. Threat: a pasted log or giant tool result
// making every attempt, including overflow retries, exceed the model window.
// Boundary: boundCompanionTail through CompanionV3ContextAtLevel. Narrowest
// layer: the assembly the executor's every attempt uses.
func TestCompanionAssemblyBoundsOversizedMessages(t *testing.T) {
	f := newCompanionStateFixture(t)
	for i := 0; i < 20; i++ {
		f.ask(fmt.Sprintf("Question %d about the build cache", i))
		f.reply(fmt.Sprintf("Answer %d about the build cache", i))
	}
	huge := f.ask("Here is the full log: " + strings.Repeat("ERROR cache miss at layer 42; ", 8000))
	check := func(label string, wantNote string) {
		previous := -1
		for level := 0; level < CompanionContextBudgetLevels; level++ {
			budget := companionContextBudgets[level]
			turn := f.assemble(level, nil)
			r := turn.Report
			if r.TailBytes > budget.TailBytes || r.HistoryBytes > budget.TailBytes+budget.ContextBlockBytes {
				t.Fatalf("%s level %d: tail %d (budget %d), history %d", label, level, r.TailBytes, budget.TailBytes, r.HistoryBytes)
			}
			if previous >= 0 && r.HistoryBytes >= previous {
				t.Fatalf("%s level %d history %d did not shrink from %d", label, level, r.HistoryBytes, previous)
			}
			previous = r.HistoryBytes
			if wantNote != "" {
				last := turn.Messages[len(turn.Messages)-1]
				if last.GlobalSeq != huge.GlobalSeq || !strings.Contains(last.Content, wantNote) || len(last.Content) > budget.TailBytes/2 {
					t.Fatalf("%s level %d newest message #%d (%d bytes) lacks %q", label, level, last.GlobalSeq, len(last.Content), wantNote)
				}
			}
			if r.TailShortened+r.TailDropped == 0 {
				t.Fatalf("%s level %d: nothing was shortened", label, level)
			}
		}
	}
	check("person message", fmt.Sprintf("Message #%d is", huge.GlobalSeq))

	output := strings.Repeat("x", 300*1024)
	raw, _ := json.Marshal(v3ProviderManagedToolResultRecord{PathID: v3ProviderManagedToolResultPathID, Type: "tool_result", ToolName: "bash", CallID: "call-big", Arguments: `{"command":"cat big.log"}`, CompletedOutput: output})
	f.message("session.tool.completed", "tool", string(raw), map[string]any{"tool_name": "bash", "call_id": "call-big"})
	check("tool record", "")
}

package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a Companion with a long life (more than ten thousand durable
// events) is maintained and assembled with the same bounded work as a young
// one. Requirement: with at least 10,000 Companion session events appended
// through ApplyV3SessionMutation, background maintenance drains them one
// bounded page per call (each MaintainCompanionState call reads at most its
// page of events plus the end probe, consumes at most that page, and moves
// the committed cursor by exactly what it consumed), the cursor reaches the
// log head and every event is counted once (Counters.Events equals the head);
// one assembly at every budget level (level 0 and every overflow retry level)
// visits at most companionAssemblyScanBound keys of range scans and its
// history, tail and block stay within that level's budget; its message and
// event reads do not grow versus the same measurement at a small history;
// the maintained lists stay within their fixed limits, and sealed segments
// read back at most CompanionSegmentMaxRead at a time with at most
// CompanionSegmentMaxItems items and a span of at most
// CompanionSegmentMaxEvents events each. Threat: per-pass or per-turn work
// that grows with the conversation (re-reading the log, scoring every topic,
// an unbounded draft or segment), or a cursor that skips or double-counts
// events at scale. Boundary: MaintainCompanionState, CommitCompanionState,
// companionContinuityStep, CompanionV3ContextAtLevel, ListCompanionSegments
// over the real Pebble store, measured with the store's scan read observer.
// Narrowest layer: the maintainer and the assembly every Companion provider
// request uses, over events written by the real mutation path (no executor
// or provider is needed to prove read bounds).
func TestCompanionTenThousandEventHistoryStaysBounded(t *testing.T) {
	f := newCompanionStateFixture(t)
	exchange := func(from, to int) {
		for i := from; i < to; i++ {
			if i%4 == 0 {
				f.ask(fmt.Sprintf("Garden irrigation question %d about zone %d drip emitters and soil moisture", i, i%7))
			} else {
				f.reply(fmt.Sprintf("Irrigation answer %d for zone %d", i, i%7))
			}
		}
	}
	measure := func(label string) []companionScanCount {
		t.Helper()
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
				t.Fatalf("%s level %d history %d bytes (tail %d/%d msgs %d/%d, block %d/%d)", label, level, r.HistoryBytes, r.TailBytes, budget.TailBytes, r.TailMessages, budget.TailMessages, r.BlockBytes, budget.ContextBlockBytes)
			}
			if r.Continuity.Reason != "" || r.Continuity.Pending != 0 {
				t.Fatalf("%s level %d continuity %+v, want current", label, level, r.Continuity)
			}
			t.Logf("%s level %d: %d scan keys (%d message, %d event), %d bytes read (bound %d keys); history %d bytes (tail %d/%d, block %d/%d)",
				label, level, got.keys, got.messages, got.events, got.bytes, companionAssemblyScanBound(budget), r.HistoryBytes, r.TailBytes, budget.TailBytes, r.BlockBytes, budget.ContextBlockBytes)
		}
		return out
	}

	exchange(0, 80)
	f.ask("Back to irrigation zone 3: which drip emitters did we pick?")
	f.drain(CompanionMaintainBackgroundEvents)
	small := measure(fmt.Sprintf("%d events", f.head()))

	started := time.Now()
	exchange(80, 10080)
	f.ask("Back to irrigation zone 3: which drip emitters did we pick?")
	head := f.head()
	if head < 10000 {
		t.Fatalf("history has %d events, want at least 10,000", head)
	}
	t.Logf("appended %d events through ApplyV3SessionMutation in %s", head, time.Since(started).Round(time.Millisecond))

	// Background maintenance: one bounded page per call until nothing is
	// pending, each call from the cursor the previous one committed.
	cursor := f.state().Cursor.EventSeq
	eventsBefore := f.state().Counters.Events
	reads := observeCompanionScanReads(t, f.session.ID)
	reads.take()
	started = time.Now()
	calls, consumed, maxRead := 0, 0, 0
	for {
		if calls > int(head)/companionMaintainPage+2 {
			t.Fatalf("maintenance did not converge: cursor %d of %d after %d calls", cursor, head, calls)
		}
		out := f.maintain(CompanionMaintainBackgroundEvents)
		got := reads.take()
		calls++
		if got.events > CompanionMaintainBackgroundEvents+1 {
			t.Fatalf("pass %d read %d events, bound %d", calls, got.events, CompanionMaintainBackgroundEvents+1)
		}
		maxRead = max(maxRead, got.events)
		if out.Consumed > CompanionMaintainBackgroundEvents || out.State.Cursor.EventSeq != cursor+uint64(out.Consumed) {
			t.Fatalf("pass %d consumed %d and moved the cursor %d -> %d", calls, out.Consumed, cursor, out.State.Cursor.EventSeq)
		}
		if out.Pending != head-out.State.Cursor.EventSeq {
			t.Fatalf("pass %d reports %d pending at cursor %d of %d", calls, out.Pending, out.State.Cursor.EventSeq, head)
		}
		cursor = out.State.Cursor.EventSeq
		consumed += out.Consumed
		if out.Pending == 0 {
			break
		}
		if out.Consumed == 0 {
			t.Fatalf("pass %d consumed nothing with %d pending", calls, out.Pending)
		}
	}
	state := f.state()
	if cursor != head || state.Cursor.EventSeq != head || state.Counters.Events != head || state.Counters.Events-eventsBefore != uint64(consumed) {
		t.Fatalf("after %d passes: cursor %d, events %d (+%d), consumed %d, head %d", calls, state.Cursor.EventSeq, state.Counters.Events, state.Counters.Events-eventsBefore, consumed, head)
	}
	t.Logf("maintenance folded %d events in %d passes (at most %d event keys read per pass) in %s", consumed, calls, maxRead, time.Since(started).Round(time.Millisecond))

	// The maintained state and continuity stay within their store bounds.
	assertCompanionStateUnique(t, state)
	if len(state.Topics) > pebblestore.CompanionStateMaxTopics || len(state.Decisions) > pebblestore.CompanionStateMaxDecisions || len(state.Work) > pebblestore.CompanionStateMaxWork || len(state.Results) > pebblestore.CompanionStateMaxResults || len(state.Commitments) > pebblestore.CompanionStateMaxOpen+pebblestore.CompanionStateMaxResolved {
		t.Fatalf("state lists over their bounds: topics %d decisions %d work %d results %d commitments %d", len(state.Topics), len(state.Decisions), len(state.Work), len(state.Results), len(state.Commitments))
	}
	if state.Counters.EvictedTopics == 0 {
		t.Fatalf("2,500 requests evicted no topic: counters %+v", state.Counters)
	}
	if len(state.Continuity.Open) > pebblestore.CompanionSegmentMaxItems || state.Continuity.Sealed == 0 {
		t.Fatalf("continuity draft %d items, %d sealed", len(state.Continuity.Open), state.Continuity.Sealed)
	}
	segments := f.segments()
	if len(segments) == 0 || len(segments) > pebblestore.CompanionSegmentMaxRead {
		t.Fatalf("segment read returned %d, bound %d", len(segments), pebblestore.CompanionSegmentMaxRead)
	}
	for _, seg := range segments {
		if len(seg.Items) > pebblestore.CompanionSegmentMaxItems || seg.EndEventSeq < seg.StartEventSeq || seg.EndEventSeq-seg.StartEventSeq+1 > pebblestore.CompanionSegmentMaxEvents {
			t.Fatalf("segment %s: %d items, events %d..%d", seg.ID, len(seg.Items), seg.StartEventSeq, seg.EndEventSeq)
		}
	}
	t.Logf("state: %d topics (%d evicted), %d commitments, %d sealed segments, open draft %d items",
		len(state.Topics), state.Counters.EvictedTopics, len(state.Commitments), state.Continuity.Sealed, len(state.Continuity.Open))

	large := measure(fmt.Sprintf("%d events", head))
	for level := range large {
		if large[level].messages > small[level].messages+companionUnfoldedReadMessages {
			t.Fatalf("level %d message reads grew with history: %d -> %d", level, small[level].messages, large[level].messages)
		}
		if large[level].events > small[level].events+1 {
			t.Fatalf("level %d event reads grew with history: %d -> %d", level, small[level].events, large[level].events)
		}
	}
}

// Purpose: the Companion's maintained state is exact when its events arrive
// twice, out of order, or across a failed pass, and only its owner's current
// Companion is maintained. Requirement: (a) a mutation retried with the same
// client request ID and payload hash is answered as a replay with the same
// message and appends no event, so the person's message is folded once (one
// topic at revision 1, one open reply) and a later replay changes nothing;
// (b) through the real store, a task outcome arriving before any watch start
// reports the task once and opens no watch, a second identical outcome
// changes no work revision, a late watch start opens exactly one watch
// commitment and keeps the reported status, and the next outcome settles it
// once (a further identical one leaves it as it was); (c) a permission wait
// opens an approval citing its request event and the decision resolves it
// citing the decision event, and a redelivered decision leaves it resolved
// and is counted unmatched; (d) a maintenance pass failing before its commit
// leaves the stored state, cursor and revision unchanged, a turn assembled
// meanwhile says maintenance failed with the exact pending count, and the
// next pass folds exactly the pending events once; (e) a snapshot claiming
// another account, another user, or a session without the Companion marker
// is refused with ErrCompanionNotCurrent and writes no state anywhere.
// Threat: a retried write or re-delivered report doubling a fold, a reordered
// report inventing or losing a commitment, a failed pass advancing the cursor
// or hiding pending input, a spoofed principal reading or writing another
// person's state. This test covers the store-path, replay, ordering and
// spoofing gaps of TestCompanionStateFoldsEachEventOnceWithItsSource,
// TestCompanionStateCommitmentsFollowDecisionsAndWatches (pure reducer) and
// TestCompanionStateRefusesForeignStaleAndConcurrentWriters. Boundary:
// ApplyV3SessionMutation idempotency, ClassifyCompanionEvent,
// applyCompanionEvent, companionReconcileWatches, MaintainCompanionState
// (companionMaintainHook marks the commit window), CompanionV3ContextAtLevel,
// IsCurrentCompanion. Narrowest layer: the maintainer over a real Pebble
// store with events written through the mutation kinds and payload shapes
// their production writers use.
func TestCompanionEventsDuplicateOutOfOrderAndFailureRecovery(t *testing.T) {
	f := newCompanionStateFixture(t)
	t.Cleanup(func() { companionMaintainHook = nil })

	// (a) A retried mutation is a replay, not a second event.
	retry := pebblestore.V3SessionMutationInput{
		SessionID: f.session.ID, AccountScopeID: "acct", UserID: "user",
		ClientRequestID: "person-retry-1", PayloadHash: "person-retry-1",
		Kind:    pebblestore.V3SessionMutationAppendMessage,
		Message: &pebblestore.MessageSnapshot{Role: "user", Content: "Schedule the greenhouse heater maintenance before the frost"},
	}
	first, err := f.db.ApplyV3SessionMutation(retry)
	if err != nil || first.Message == nil || first.Replayed {
		t.Fatalf("first write: %+v %v", first, err)
	}
	headAfterFirst := f.head()
	again, err := f.db.ApplyV3SessionMutation(retry)
	if err != nil || !again.Replayed || again.Message == nil || again.Message.ID != first.Message.ID || f.head() != headAfterFirst {
		t.Fatalf("retried write: replayed %v, message %+v, head %d -> %d (%v)", again.Replayed, again.Message, headAfterFirst, f.head(), err)
	}
	folded := f.maintain(0)
	if folded.State.Counters.Events != f.head() || folded.Pending != 0 {
		t.Fatalf("after the retry: events %d, head %d, pending %d", folded.State.Counters.Events, f.head(), folded.Pending)
	}
	topicID := pebblestore.CompanionMessageContextID(f.session.ID, first.Message.GlobalSeq)
	if len(folded.State.Topics) != 1 || folded.State.Topics[0].RecordID != topicID {
		t.Fatalf("topics after the retry = %+v, want only %s", folded.State.Topics, topicID)
	}
	if rec, ok, err := f.db.GetCompanionContext("acct", "user", topicID); err != nil || !ok || rec.Revision != 1 {
		t.Fatalf("topic record after the retry: %+v %v %v, want revision 1", rec, ok, err)
	}
	if c, ok := companionCommitment(folded.State, "reply:"+first.Message.ID); !ok || c.State != pebblestore.CompanionCommitmentOpen {
		t.Fatalf("reply owed after the retry = %+v %v", c, ok)
	}
	assertCompanionStateUnique(t, folded.State)
	if late, err := f.db.ApplyV3SessionMutation(retry); err != nil || !late.Replayed || f.head() != headAfterFirst {
		t.Fatalf("late retry: %+v %v (head %d)", late.Replayed, err, f.head())
	}
	if again := f.maintain(0); again.Consumed != 0 || again.State.Revision != folded.State.Revision {
		t.Fatalf("a replayed write changed the state: consumed %d, revision %d -> %d", again.Consumed, folded.State.Revision, again.State.Revision)
	}

	// Events the runtime records with an explicit payload (tool, permission
	// and task-watch writers) go through a run intent record.
	runStatus := pebblestore.V3RunIntentPendingExecutor
	record := func(eventType string, payload map[string]any) uint64 {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: eventType, EventPayload: raw, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-events", Status: runStatus}}, f.session.ID)
		runStatus = pebblestore.V3RunIntentRunning
		return f.head()
	}
	report, _ := json.Marshal(map[string]any{"project_id": "garden", "tasks": []map[string]any{{"task_id": "heater", "attempt_id": "attempt-1", "session_id": "heater-session", "title": "Heater maintenance", "status": "needs_review"}}})
	outcome := map[string]any{"id": "", "role": "user", "content": "Delegated project task outcomes:\n" + string(report), "metadata": map[string]any{"task_watch_id": "watch-9", "task_watch_delivery": "outcome"}}
	workID := "task:/garden/heater"
	work := func(state pebblestore.CompanionState) (pebblestore.CompanionWorkRef, int) {
		var found pebblestore.CompanionWorkRef
		n := 0
		for _, w := range state.Work {
			if w.ID == workID {
				found, n = w, n+1
			}
		}
		return found, n
	}

	// (b) An outcome before any start, then the same outcome again.
	f.message("session.run.tasks_ready", "user", outcome["content"].(string), outcome["metadata"].(map[string]any))
	state := f.maintain(0).State
	reported, n := work(state)
	if n != 1 || reported.Status != "needs_review" || reported.AttemptID != "attempt-1" || reported.WatchID != "" || reported.Revision != 1 {
		t.Fatalf("work after an outcome without a start = %+v (%d entries)", reported, n)
	}
	if _, opened := companionCommitment(state, "task_watch:watch-9"); opened {
		t.Fatal("an outcome without a start invented a watch commitment")
	}
	f.message("session.run.tasks_ready", "user", outcome["content"].(string), outcome["metadata"].(map[string]any))
	state = f.maintain(0).State
	if again, n := work(state); n != 1 || again.Revision != reported.Revision || again.Status != reported.Status {
		t.Fatalf("a repeated outcome changed the work: %+v (%d entries), was %+v", again, n, reported)
	}

	// The late start: one watch, the reported status kept.
	watch := pebblestore.V3TaskWatchSummary{WatchID: "watch-9", ProjectID: "garden", TaskIDs: []string{"heater"}, Titles: []string{"Heater maintenance"}}
	startSeq := record("session.task_watch.started", map[string]any{"message": map[string]any{"id": "watch-9-notice", "role": "system", "content": "Watching 1 task(s)", "metadata": map[string]any{"task_watch_id": "watch-9", "project_id": "garden"}}, "task_watches": []any{watch}})
	state = f.maintain(0).State
	if c, ok := companionCommitment(state, "task_watch:watch-9"); !ok || c.State != pebblestore.CompanionCommitmentOpen || c.Opened.EventSeq != startSeq {
		t.Fatalf("late watch start = %+v %v, want open at event %d", c, ok, startSeq)
	}
	if watched, n := work(state); n != 1 || watched.Status != "needs_review" || watched.WatchID != "watch-9" {
		t.Fatalf("work after the late start = %+v (%d entries)", watched, n)
	}
	// The next outcome settles it once; an identical redelivery changes nothing.
	outcome["id"] = "outcome-delivery-2"
	settleSeq := record("session.run.tasks_ready", map[string]any{"message": outcome, "task_watches": []any{}})
	state = f.maintain(0).State
	settled, ok := companionCommitment(state, "task_watch:watch-9")
	if !ok || settled.State != pebblestore.CompanionCommitmentResolved || settled.Resolution != "settled" || settled.Resolved == nil || settled.Resolved.EventSeq != settleSeq {
		t.Fatalf("watch after its outcome = %+v %v, want settled by event %d", settled, ok, settleSeq)
	}
	after, _ := work(state)
	outcome["id"] = "outcome-delivery-3"
	record("session.run.tasks_ready", map[string]any{"message": outcome, "task_watches": []any{}})
	state = f.maintain(0).State
	if c, _ := companionCommitment(state, "task_watch:watch-9"); c.Resolved == nil || c.Resolved.EventSeq != settleSeq {
		t.Fatalf("a redelivered outcome moved the settled watch: %+v", c)
	}
	if again, n := work(state); n != 1 || again.Revision != after.Revision || again.WatchID != "" {
		t.Fatalf("a redelivered outcome changed the work: %+v (%d entries), was %+v", again, n, after)
	}
	assertCompanionStateUnique(t, state)

	// (c) A permission wait opens, then settles, citing its events.
	requested := record("permission.requested", map[string]any{"type": "permission.requested", "tool_name": "bash", "permission": map[string]any{"id": "perm-7", "tool_name": "bash", "status": pebblestore.PermissionStatusPending}})
	state = f.maintain(0).State
	if c, ok := companionCommitment(state, "approval:perm-7"); !ok || c.State != pebblestore.CompanionCommitmentOpen || c.Opened.EventSeq != requested || c.Opened.EventType != "permission.requested" {
		t.Fatalf("waiting approval = %+v %v, want open at event %d", c, ok, requested)
	}
	decision := map[string]any{"permission": map[string]any{"id": "perm-7", "tool_name": "bash", "status": pebblestore.PermissionStatusApproved}}
	decided := record("permission.updated", decision)
	state = f.maintain(0).State
	approval, ok := companionCommitment(state, "approval:perm-7")
	if !ok || approval.State != pebblestore.CompanionCommitmentResolved || approval.Resolution != pebblestore.PermissionStatusApproved || approval.Resolved == nil || approval.Resolved.EventSeq != decided {
		t.Fatalf("decided approval = %+v %v, want approved by event %d", approval, ok, decided)
	}
	unmatched := state.Counters.UnmatchedResolution
	record("permission.updated", decision)
	state = f.maintain(0).State
	if c, _ := companionCommitment(state, "approval:perm-7"); c.Resolved == nil || c.Resolved.EventSeq != decided || state.Counters.UnmatchedResolution != unmatched+1 {
		t.Fatalf("a redelivered decision: approval %+v, unmatched %d -> %d", c, unmatched, state.Counters.UnmatchedResolution)
	}
	assertCompanionStateUnique(t, state)

	// (d) A failed pass changes nothing; the next one recovers exactly once.
	before := f.state()
	const pendingInput = 5
	for i := 0; i < pendingInput; i++ {
		f.ask(fmt.Sprintf("Greenhouse follow-up %d: check the vent motor wiring", i))
	}
	injected := errors.New("injected maintenance failure")
	companionMaintainHook = func(int) error { return injected }
	if _, err := MaintainCompanionState(f.db, f.session, 0, 2000); !errors.Is(err, injected) {
		t.Fatalf("the injected failure did not stop the pass: %v", err)
	}
	turn, err := CompanionV3ContextAtLevel(f.db, f.session, nil, 0, 2000, 0)
	companionMaintainHook = nil
	if err != nil {
		t.Fatalf("a maintenance failure failed the turn: %v", err)
	}
	if c := turn.Report.Continuity; c.Reason != "maintenance_failed" || c.Pending != pendingInput {
		t.Fatalf("turn during the failure: continuity %+v, want maintenance_failed with %d pending", c, pendingInput)
	}
	failed := f.state()
	if failed.Revision != before.Revision || failed.Cursor != before.Cursor || failed.Counters != before.Counters {
		t.Fatalf("a failed pass changed the state: revision %d -> %d, cursor %+v -> %+v", before.Revision, failed.Revision, before.Cursor, failed.Cursor)
	}
	recovered := f.maintain(0)
	if recovered.Consumed != pendingInput || recovered.Pending != 0 || recovered.State.Counters.Events != before.Counters.Events+pendingInput || recovered.State.Cursor.EventSeq != f.head() {
		t.Fatalf("recovery consumed %d (pending %d), events %d -> %d, cursor %d of %d", recovered.Consumed, recovered.Pending, before.Counters.Events, recovered.State.Counters.Events, recovered.State.Cursor.EventSeq, f.head())
	}
	if again := f.maintain(0); again.Consumed != 0 || again.State.Revision != recovered.State.Revision {
		t.Fatalf("a pass after recovery consumed %d", again.Consumed)
	}
	assertCompanionStateUnique(t, recovered.State)

	// (e) Spoofed principals and unmarked sessions are refused, writing nothing.
	settledState := f.state()
	otherAccount := f.session
	otherAccount.AccountScopeID = "other-acct"
	otherUser := f.session
	otherUser.UserID = "intruder"
	f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationCreateSession, Session: &pebblestore.SessionSnapshot{ID: "plain-chat", Mode: "auto"}}, "plain-chat")
	plain, ok, err := f.db.GetSession("plain-chat")
	if err != nil || !ok {
		t.Fatalf("plain session: %v %v", ok, err)
	}
	for label, session := range map[string]pebblestore.SessionSnapshot{"another account": otherAccount, "another user": otherUser, "no Companion marker": plain} {
		if _, err := MaintainCompanionState(f.db, session, 0, 3000); !errors.Is(err, ErrCompanionNotCurrent) {
			t.Fatalf("%s: maintenance answered %v, want ErrCompanionNotCurrent", label, err)
		}
	}
	for _, principal := range [][2]string{{"other-acct", "user"}, {"acct", "intruder"}} {
		if _, stored, err := f.db.GetCompanionState(principal[0], principal[1]); err != nil || stored {
			t.Fatalf("a refused maintainer stored state for %s/%s: %v %v", principal[0], principal[1], stored, err)
		}
	}
	if now := f.state(); now.Revision != settledState.Revision || now.Cursor != settledState.Cursor {
		t.Fatalf("a refused maintainer changed the owner's state: revision %d -> %d", settledState.Revision, now.Revision)
	}
	if strings.TrimSpace(plain.AccountScopeID) == "" {
		t.Fatal("fixture: the plain session has no owner, so its refusal proves nothing")
	}
}

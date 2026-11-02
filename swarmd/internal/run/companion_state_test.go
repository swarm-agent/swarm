package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// companionStateFixture is a Companion (identity generation 1 of acct/user)
// over a real Pebble store at a path the test can close and reopen.
type companionStateFixture struct {
	t        *testing.T
	path     string
	raw      *pebblestore.Store
	db       *pebblestore.SessionStore
	session  pebblestore.SessionSnapshot
	requests int
}

func newCompanionStateFixture(t *testing.T) *companionStateFixture {
	t.Helper()
	f := &companionStateFixture{t: t, path: filepath.Join(t.TempDir(), "store")}
	f.open()
	t.Cleanup(func() { _ = f.raw.Close() })
	identity, _, err := f.db.EnsureCompanion("acct", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationCreateSession, Session: &pebblestore.SessionSnapshot{ID: identity.SessionID, Mode: "auto", Metadata: map[string]any{pebblestore.SessionCompanionMetadataKey: map[string]any{"generation": 1}}}}, identity.SessionID)
	f.reload(identity.SessionID)
	return f
}

func (f *companionStateFixture) open() {
	f.t.Helper()
	raw, err := pebblestore.Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.raw, f.db = raw, pebblestore.NewSessionStore(raw)
}

// restart closes the store and opens it again at the same path (a daemon
// restart over the same data).
func (f *companionStateFixture) restart() {
	f.t.Helper()
	if err := f.raw.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
	f.reload(f.session.ID)
}

func (f *companionStateFixture) reload(id string) {
	f.t.Helper()
	session, ok, err := f.db.GetSession(id)
	if err != nil || !ok {
		f.t.Fatalf("session %s: %v %v", id, ok, err)
	}
	f.session = session
}

func (f *companionStateFixture) apply(in pebblestore.V3SessionMutationInput, session string) pebblestore.V3SessionMutationResult {
	f.t.Helper()
	f.requests++
	in.SessionID, in.AccountScopeID, in.UserID = session, "acct", "user"
	in.ClientRequestID = fmt.Sprintf("state-req-%d", f.requests)
	in.PayloadHash = in.ClientRequestID
	result, err := f.db.ApplyV3SessionMutation(in)
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

// message appends one message as the event type its production writer uses.
func (f *companionStateFixture) message(eventType, role, content string, metadata map[string]any) pebblestore.MessageSnapshot {
	f.t.Helper()
	result := f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationAppendMessage, EventType: eventType, Message: &pebblestore.MessageSnapshot{Role: role, Content: content, Metadata: metadata}}, f.session.ID)
	if result.Message == nil {
		f.t.Fatal("append returned no message")
	}
	return *result.Message
}

func (f *companionStateFixture) maintain(maxEvents int) CompanionMaintenance {
	f.t.Helper()
	out, err := MaintainCompanionState(f.db, f.session, maxEvents, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *companionStateFixture) head() uint64 {
	f.t.Helper()
	projection, _, err := f.db.GetV3SessionProjection(f.session.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return projection.LastEventSeq
}

func companionCommitment(state pebblestore.CompanionState, id string) (pebblestore.CompanionCommitment, bool) {
	for _, c := range state.Commitments {
		if c.ID == id {
			return c, true
		}
	}
	return pebblestore.CompanionCommitment{}, false
}

func assertCompanionStateUnique(t *testing.T, state pebblestore.CompanionState) {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range state.Commitments {
		if seen["c/"+c.ID] {
			t.Fatalf("duplicate commitment %s", c.ID)
		}
		seen["c/"+c.ID] = true
	}
	for _, w := range state.Work {
		if seen["w/"+w.ID] {
			t.Fatalf("duplicate work %s", w.ID)
		}
		seen["w/"+w.ID] = true
	}
	for _, r := range state.Topics {
		if seen["t/"+r.RecordID] {
			t.Fatalf("duplicate topic %s", r.RecordID)
		}
		seen["t/"+r.RecordID] = true
	}
}

// Purpose: the Companion's state is derived from its own durable event log,
// each event once, in order, with its exact source. Requirement:
// MaintainCompanionState folds the person's message (a topic, the focus and
// an open reply), a final reply (resolves it), a device tool result, a
// background wake and a failed run into the state, each entry citing the
// event's sequence, ID and type; the cursor ends at the log head and every
// event is counted once; repeating maintenance (a replayed run start)
// changes nothing, not even the revision; the topic index mark moves with
// the cursor. A short follow-up opens a reply but makes no topic and keeps
// the focus. Threat: state that drifts from the log (a missed or doubly
// applied event), references without provenance, or replays that churn
// revisions. Boundary: ClassifyCompanionEvent, applyCompanionEvent,
// MaintainCompanionState, CommitCompanionState. Narrowest layer: the
// maintainer over a real Pebble store with events appended through
// ApplyV3SessionMutation with the event types and metadata their production
// writers use (the API scenario test drives the real call paths).
func TestCompanionStateFoldsEachEventOnceWithItsSource(t *testing.T) {
	f := newCompanionStateFixture(t)
	person := f.message("", "user", "My laptop printer driver keeps failing after the update", nil)
	reply := f.message("session.assistant.completed", "assistant", "Let's reinstall the printer driver.", nil)
	short := f.message("", "user", "ok do that", nil)
	device := f.message("session.tool.completed", "tool", `{"output":"done"}`, map[string]any{"tool_name": pebblestore.MachineToolRun, "call_id": "call-1"})
	failed := f.message("session.tool.failed", "tool", `{"error":"refused"}`, map[string]any{"tool_name": pebblestore.MachineToolWrite, "call_id": "call-2", "error": "refused by the device"})
	wake := f.message("", "user", "Background command finished: exit 0", map[string]any{"wake_id": "wake-1", "source": pebblestore.SessionWakeSourceBackgroundCommand})
	f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run_intent.recorded", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-x", Status: pebblestore.V3RunIntentPendingExecutor}}, f.session.ID)
	f.apply(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.failed", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-x", Status: pebblestore.V3RunIntentFailed, BlockedReason: "provider error"}}, f.session.ID)

	out := f.maintain(0)
	state := out.State
	head := f.head()
	if state.Cursor.EventSeq != head || state.Counters.Events != head || out.Pending != 0 || out.LatestSeq != head {
		t.Fatalf("cursor %d events %d pending %d latest %d, want all at head %d", state.Cursor.EventSeq, state.Counters.Events, out.Pending, out.LatestSeq, head)
	}
	assertCompanionStateUnique(t, state)
	topicID := pebblestore.CompanionMessageContextID(f.session.ID, person.GlobalSeq)
	if len(state.Topics) != 1 || state.Topics[0].RecordID != topicID || state.Topics[0].Source.EventSeq != person.GlobalSeq || state.Topics[0].Source.MessageID != person.ID || state.Topics[0].Source.EventID == "" {
		t.Fatalf("topics = %+v, want only the person's substantive message %d with its event", state.Topics, person.GlobalSeq)
	}
	first, ok := companionCommitment(state, "reply:"+person.ID)
	if !ok || first.State != pebblestore.CompanionCommitmentResolved || first.Resolution != "answered" || first.Resolved == nil || first.Resolved.EventSeq != reply.GlobalSeq {
		t.Fatalf("first reply = %+v %v, want answered by event %d", first, ok, reply.GlobalSeq)
	}
	if second, ok := companionCommitment(state, "reply:"+short.ID); !ok || second.State != pebblestore.CompanionCommitmentOpen || second.Opened.EventSeq != short.GlobalSeq {
		t.Fatalf("short follow-up reply = %+v %v, want open (no later reply)", second, ok)
	}
	if focus, ok, err := f.db.GetCompanionFocus("acct", "user"); err != nil || !ok || focus.RecordID != topicID || focus.Source == nil || focus.Source.EventSeq != person.GlobalSeq || focus.Revision != state.Revision {
		t.Fatalf("focus = %+v %v %v, want the person's topic set by revision %d", focus, ok, err, state.Revision)
	}
	want := []struct {
		kind, name, status string
		seq                uint64
	}{
		{"device", pebblestore.MachineToolRun, "completed", device.GlobalSeq},
		{"device", pebblestore.MachineToolWrite, "failed", failed.GlobalSeq},
		{"wake", pebblestore.SessionWakeSourceBackgroundCommand, "delivered", wake.GlobalSeq},
		{"run", "run-x", pebblestore.V3RunIntentFailed, head},
	}
	if len(state.Results) != len(want) {
		t.Fatalf("results = %+v", state.Results)
	}
	for i, w := range want {
		got := state.Results[i]
		if got.Kind != w.kind || got.Name != w.name || got.Status != w.status || got.Source.EventSeq != w.seq {
			t.Fatalf("result %d = %+v, want %+v", i, got, w)
		}
	}
	if state.Results[1].Detail != "refused by the device" {
		t.Fatalf("failed device detail = %q", state.Results[1].Detail)
	}
	if mark, err := f.db.GetCompanionIndexMark("acct", "user"); err != nil || mark.SessionID != f.session.ID || mark.MessageSeq != head {
		t.Fatalf("index mark = %+v %v, want the cursor %d", mark, err, head)
	}

	again := f.maintain(0)
	a, _ := json.Marshal(state)
	b, _ := json.Marshal(again.State)
	if again.Consumed != 0 || string(a) != string(b) {
		t.Fatalf("a repeated maintenance changed the state (consumed %d):\n%s\n%s", again.Consumed, a, b)
	}
}

// Purpose: approvals, confirmations and task watches become commitments
// opened and resolved by exact events, and duplicate or unmatched events do
// not corrupt them. Requirement: permission.requested opens an approval and
// permission.updated with a decision resolves it with that decision (a
// pending update does not); a client-tool confirmation request opens one and
// its decision resolves it (its execution and delivery writes are not new
// commitments); a watch in an event's complete task_watches set opens a
// task_watch commitment and work references for its tasks, a task outcome
// reports the task's status and attempt (a new work revision) and settles
// the watch, a repeated identical report changes no revision, and a watch
// missing from a later set ends; a decision with no request is counted as
// unmatched and opens nothing. Threat: approvals shown as still waiting (or
// resolved by the wrong event), a re-delivered report bumping revisions, a
// stray decision inventing a commitment. Boundary: ClassifyCompanionEvent,
// applyCompanionEvent, companionReconcileWatches, companionUpsertWork.
// Narrowest layer: the pure classifier and reducer over event payloads
// shaped exactly as their production writers marshal them.
func TestCompanionStateCommitmentsFollowDecisionsAndWatches(t *testing.T) {
	seq := uint64(0)
	event := func(eventType string, payload any) pebblestore.V3SessionEvent {
		seq++
		raw, _ := json.Marshal(payload)
		return pebblestore.V3SessionEvent{ID: fmt.Sprintf("evt-%d", seq), SessionID: "companion", Seq: seq, EventType: eventType, Payload: raw}
	}
	state := pebblestore.NewCompanionState(pebblestore.CompanionIdentity{AccountScopeID: "acct", UserID: "user", SessionID: "companion", Generation: 1}, 1)
	apply := func(ev pebblestore.V3SessionEvent) {
		t.Helper()
		classified, ok := ClassifyCompanionEvent(ev)
		if !ok {
			t.Fatalf("event %s was not classified", ev.EventType)
		}
		applyCompanionEvent(&state, classified, nil, &companionStateMutation{})
	}
	watch := pebblestore.V3TaskWatchSummary{WatchID: "watch-1", ProjectID: "research", TaskIDs: []string{"battery"}, Titles: []string{"Battery research"}}
	outcome := func(status, attempt string) map[string]any {
		body, _ := json.Marshal(map[string]any{"project_id": "research", "tasks": []map[string]any{{"task_id": "battery", "attempt_id": attempt, "session_id": "task-session", "title": "Battery research", "status": status}}})
		return map[string]any{"message": map[string]any{"id": "outcome-" + status + attempt, "role": "user", "content": "Delegated project task outcomes:\n" + string(body), "metadata": map[string]any{"task_watch_id": "watch-1", "task_watch_delivery": "outcome"}}, "task_watches": []any{}}
	}

	apply(event("permission.requested", map[string]any{"permission": map[string]any{"id": "perm-1", "tool_name": "bash", "status": "pending"}}))
	apply(event("permission.updated", map[string]any{"permission": map[string]any{"id": "perm-1", "tool_name": "bash", "status": "pending"}}))
	if c, _ := companionCommitment(state, "approval:perm-1"); c.State != pebblestore.CompanionCommitmentOpen || c.Opened.EventSeq != 1 {
		t.Fatalf("approval after a pending update = %+v", c)
	}
	apply(event("permission.updated", map[string]any{"permission": map[string]any{"id": "perm-1", "tool_name": "bash", "status": pebblestore.PermissionStatusApproved}}))
	if c, _ := companionCommitment(state, "approval:perm-1"); c.State != pebblestore.CompanionCommitmentResolved || c.Resolution != pebblestore.PermissionStatusApproved || c.Resolved.EventSeq != 3 {
		t.Fatalf("approved approval = %+v", c)
	}
	apply(event("permission.updated", map[string]any{"permission": map[string]any{"id": "perm-stray", "status": pebblestore.PermissionStatusDenied}}))
	if _, found := companionCommitment(state, "approval:perm-stray"); found || state.Counters.UnmatchedResolution != 1 {
		t.Fatalf("a decision without a request opened %v (unmatched %d)", found, state.Counters.UnmatchedResolution)
	}

	apply(event("session.tool.started", map[string]any{"client_tool_confirmation": map[string]any{"id": "conf-1", "status": ClientToolConfirmationPending, "tool": "place_order", "summary": "Order two batteries"}}))
	if _, ok := ClassifyCompanionEvent(event("session.tool.started", map[string]any{"client_tool_confirmation": map[string]any{"id": "conf-1", "status": ClientToolConfirmationApproved, "execution_permission_id": "perm-9"}})); ok {
		t.Fatal("a confirmation's execution write was classified as a decision")
	}
	apply(event("session.tool.started", map[string]any{"client_tool_confirmation": map[string]any{"id": "conf-1", "status": ClientToolConfirmationRejected}}))
	if c, _ := companionCommitment(state, "confirmation:conf-1"); c.State != pebblestore.CompanionCommitmentResolved || c.Resolution != ClientToolConfirmationRejected || c.Title != "Order two batteries" {
		t.Fatalf("confirmation = %+v", c)
	}

	apply(event("session.task_watch.started", map[string]any{"message": map[string]any{"id": "watch-1-watch-notice", "role": "system", "content": "Watching 1 task(s)", "metadata": map[string]any{"task_watch_id": "watch-1", "project_id": "research"}}, "task_watches": []any{watch}}))
	if c, _ := companionCommitment(state, "task_watch:watch-1"); c.State != pebblestore.CompanionCommitmentOpen {
		t.Fatalf("watch commitment = %+v", c)
	}
	if len(state.Work) != 1 || state.Work[0].ID != "task:/research/battery" || state.Work[0].Status != "watching" || state.Work[0].WatchID != "watch-1" || state.Work[0].Revision != 1 {
		t.Fatalf("watched work = %+v", state.Work)
	}
	apply(event("session.run.tasks_ready", outcome("needs_review", "attempt-1")))
	work := state.Work[0]
	if work.Status != "needs_review" || work.AttemptID != "attempt-1" || work.SessionID != "task-session" || work.WatchID != "" || work.Revision < 2 {
		t.Fatalf("work after the outcome = %+v", work)
	}
	if c, _ := companionCommitment(state, "task_watch:watch-1"); c.Resolution != "settled" || c.Resolved == nil || c.Resolved.EventSeq != seq {
		t.Fatalf("watch after its outcome = %+v", c)
	}
	revision := work.Revision
	apply(event("session.run.tasks_ready", outcome("needs_review", "attempt-1")))
	if state.Work[0].Revision != revision {
		t.Fatalf("a repeated identical report moved the work revision %d -> %d", revision, state.Work[0].Revision)
	}

	second := pebblestore.V3TaskWatchSummary{WatchID: "watch-2", ProjectID: "research", TaskIDs: []string{"battery"}}
	apply(event("session.task_watch.started", map[string]any{"task_watches": []any{second}}))
	if state.Work[0].Status != "needs_review" || state.Work[0].WatchID != "watch-2" {
		t.Fatalf("a new watch on a reported task = %+v, want its last status kept", state.Work[0])
	}
	apply(event("session.task_watch.cancelled", map[string]any{"message": map[string]any{"id": "watch-cancel-1", "role": "system", "content": "Stopped watching"}, "task_watches": []any{}}))
	if c, _ := companionCommitment(state, "task_watch:watch-2"); c.Resolution != "ended" || c.Resolved.EventType != "session.task_watch.cancelled" {
		t.Fatalf("cancelled watch = %+v", c)
	}
	if state.Work[0].WatchID != "" {
		t.Fatalf("work still names the ended watch: %+v", state.Work[0])
	}
	assertCompanionStateUnique(t, state)
}

// Purpose: maintenance is restart-safe across its crash window and bounded.
// Requirement: a pass that wrote a topic record but crashed before its state
// commit leaves no state (the cursor did not move); after a store restart the
// next pass folds the same events and the topic exists once (revision 1); a
// topic the person forgot in between stays forgotten (no reference, no
// focus); a pass consumes at most maxEvents and reports the rest as pending,
// and the next pass continues from the cursor. Threat: a crash between the
// topic write and the state commit duplicating or resurrecting topics, a
// lost cursor, or a pass whose work grows with the history. Boundary:
// MaintainCompanionState (companionMaintainHook marks the crash window),
// CommitCompanionState, PutCompanionContext tombstones. Narrowest layer: the
// maintainer over a real Pebble store closed and reopened at the same path.
func TestCompanionStateSurvivesCrashWindowAndRestartAndStaysBounded(t *testing.T) {
	f := newCompanionStateFixture(t)
	printer := f.message("", "user", "The office printer drops jobs from my laptop over wifi", nil)
	scanner := f.message("", "user", "The scanner calibration also fails after the firmware update", nil)

	companionMaintainHook = func(int) error { return errors.New("injected crash before the state commit") }
	if _, err := MaintainCompanionState(f.db, f.session, 0, 1000); err == nil {
		companionMaintainHook = nil
		t.Fatal("the injected crash did not stop the pass")
	}
	companionMaintainHook = nil
	if _, stored, err := f.db.GetCompanionState("acct", "user"); err != nil || stored {
		t.Fatalf("a crashed pass stored state: %v %v", stored, err)
	}
	scannerTopic := pebblestore.CompanionMessageContextID(f.session.ID, scanner.GlobalSeq)
	record, found, err := f.db.GetCompanionContext("acct", "user", scannerTopic)
	if err != nil || !found {
		t.Fatalf("the crash window did not leave the topic written: %v %v", found, err)
	}
	// The person forgets the scanner topic before maintenance resumes.
	if err := f.db.DeleteCompanionContext("acct", "user", scannerTopic, record.Revision); err != nil {
		t.Fatal(err)
	}
	f.restart()

	state := f.maintain(0).State
	head := f.head()
	if state.Cursor.EventSeq != head || state.Counters.Events != head {
		t.Fatalf("after restart: cursor %d, events %d, want head %d once", state.Cursor.EventSeq, state.Counters.Events, head)
	}
	printerTopic := pebblestore.CompanionMessageContextID(f.session.ID, printer.GlobalSeq)
	if len(state.Topics) != 1 || state.Topics[0].RecordID != printerTopic {
		t.Fatalf("topics after restart = %+v, want only the printer topic (the scanner one was forgotten)", state.Topics)
	}
	if rec, _, _ := f.db.GetCompanionContext("acct", "user", printerTopic); rec.Revision != 1 {
		t.Fatalf("the replayed topic write changed the record: revision %d", rec.Revision)
	}
	if _, found, _ := f.db.GetCompanionContext("acct", "user", scannerTopic); found {
		t.Fatal("the forgotten topic was resurrected by the replayed pass")
	}
	if focus, _, _ := f.db.GetCompanionFocus("acct", "user"); focus.RecordID != printerTopic {
		t.Fatalf("focus = %+v, want the printer topic (the forgotten one cannot be the focus)", focus)
	}

	for i := 0; i < 600; i++ {
		f.message("", "assistant", fmt.Sprintf("note %d", i), nil)
	}
	head = f.head()
	first := f.maintain(companionMaintainPage)
	if first.Consumed != companionMaintainPage || first.State.Cursor.EventSeq != state.Cursor.EventSeq+companionMaintainPage || first.Pending != head-first.State.Cursor.EventSeq {
		t.Fatalf("bounded pass: consumed %d, cursor %d, pending %d (head %d)", first.Consumed, first.State.Cursor.EventSeq, first.Pending, head)
	}
	// Background maintenance continues one page per call until nothing is
	// pending, each call from the cursor the previous one committed.
	rest := first
	for calls := 0; rest.Pending > 0; calls++ {
		if calls > int(head)/companionMaintainPage+1 {
			t.Fatalf("maintenance did not converge: pending %d after %d calls", rest.Pending, calls)
		}
		before := rest.State.Cursor.EventSeq
		rest = f.maintain(CompanionMaintainBackgroundEvents)
		if rest.Consumed == 0 || rest.Consumed > companionMaintainPage || rest.State.Cursor.EventSeq != before+uint64(rest.Consumed) {
			t.Fatalf("background call consumed %d from cursor %d to %d", rest.Consumed, before, rest.State.Cursor.EventSeq)
		}
	}
	if rest.State.Cursor.EventSeq != head || rest.State.Counters.Events != head {
		t.Fatalf("continuing calls: cursor %d, events %d, head %d", rest.State.Cursor.EventSeq, rest.State.Counters.Events, head)
	}
}

// Purpose: only the owner's current Companion is maintained, and only by a
// writer holding the current revision. Requirement: concurrent maintainers
// of one Companion fold every event exactly once; a session marked as a
// Companion but owned by another user, or one that is not the identity's
// current session, is refused (ErrCompanionNotCurrent) and writes no state;
// a commit at a stale revision, or for a deleted generation, is refused with
// ErrCompanionStateConflict and leaves the stored state as it was; after the
// Companion is deleted (context purged, identity retired) the old session's
// maintainer is refused. Threat: duplicate folding under concurrency, one
// user's events landing in another's state, a late maintainer of a deleted
// Companion writing state the new one inherits. Boundary:
// MaintainCompanionState, CommitCompanionState, IsCurrentCompanion,
// RetireCompanion, PurgeCompanionContext. Narrowest layer: the maintainer and
// store over a real Pebble store.
func TestCompanionStateRefusesForeignStaleAndConcurrentWriters(t *testing.T) {
	f := newCompanionStateFixture(t)
	for i := 0; i < 40; i++ {
		f.message("", "user", fmt.Sprintf("Question %d about the printer driver configuration", i), nil)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := MaintainCompanionState(f.db, f.session, 0, 1000); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	state, stored, err := f.db.GetCompanionState("acct", "user")
	if err != nil || !stored || state.Counters.Events != f.head() || state.Cursor.EventSeq != f.head() || len(state.Topics) != pebblestore.CompanionStateMaxTopics || state.Counters.EvictedTopics != 40-pebblestore.CompanionStateMaxTopics {
		t.Fatalf("concurrent maintenance: events %d cursor %d head %d topics %d evicted %d (%v %v)", state.Counters.Events, state.Cursor.EventSeq, f.head(), len(state.Topics), state.Counters.EvictedTopics, stored, err)
	}
	assertCompanionStateUnique(t, state)

	// Another user's session carrying the Companion marker is not theirs.
	foreignID := "companion_foreign"
	f.requests++
	if _, err := f.db.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: foreignID, AccountScopeID: "acct", UserID: "intruder", ClientRequestID: "foreign", PayloadHash: "foreign", Kind: pebblestore.V3SessionMutationCreateSession, Session: &pebblestore.SessionSnapshot{ID: foreignID, Mode: "auto", Metadata: map[string]any{pebblestore.SessionCompanionMetadataKey: true}}}); err != nil {
		t.Fatal(err)
	}
	foreign, _, _ := f.db.GetSession(foreignID)
	if _, err := MaintainCompanionState(f.db, foreign, 0, 1000); !errors.Is(err, ErrCompanionNotCurrent) {
		t.Fatalf("foreign session maintenance: %v", err)
	}
	if _, stored, _ := f.db.GetCompanionState("acct", "intruder"); stored {
		t.Fatal("a foreign session wrote state")
	}

	// A stale writer is refused and changes nothing.
	stale := state
	stale.Topics = nil
	if _, err := f.db.CommitCompanionState(pebblestore.CompanionStateCommit{Next: stale, ExpectedRevision: state.Revision - 1}); !errors.Is(err, pebblestore.ErrCompanionStateConflict) {
		t.Fatalf("stale commit: %v", err)
	}
	backward := state
	backward.Cursor.EventSeq--
	if _, err := f.db.CommitCompanionState(pebblestore.CompanionStateCommit{Next: backward, ExpectedRevision: state.Revision}); !errors.Is(err, pebblestore.ErrCompanionStateConflict) {
		t.Fatalf("backward cursor commit: %v", err)
	}
	if now, _, _ := f.db.GetCompanionState("acct", "user"); now.Revision != state.Revision || len(now.Topics) != len(state.Topics) {
		t.Fatalf("a refused commit changed the state: rev %d -> %d", state.Revision, now.Revision)
	}

	// Deleting the Companion: purge, then retire. The old session's late
	// maintainer and a commit for its generation are refused.
	if err := f.db.PurgeCompanionContext("acct", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RetireCompanion("acct", "user", f.session.ID, 2000); err != nil {
		t.Fatal(err)
	}
	f.message("", "user", "A late message to the deleted Companion about printers", nil)
	if _, err := MaintainCompanionState(f.db, f.session, 0, 3000); !errors.Is(err, ErrCompanionNotCurrent) {
		t.Fatalf("deleted generation maintenance: %v", err)
	}
	if _, err := f.db.CommitCompanionState(pebblestore.CompanionStateCommit{Next: state, ExpectedRevision: 0}); !errors.Is(err, pebblestore.ErrCompanionStateConflict) {
		t.Fatalf("deleted generation commit: %v", err)
	}
	if _, stored, _ := f.db.GetCompanionState("acct", "user"); stored {
		t.Fatal("the deleted Companion's state came back")
	}
}

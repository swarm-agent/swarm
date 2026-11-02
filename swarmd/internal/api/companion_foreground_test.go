package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// seedInterruptedRunWithQueuedNote records, for sessionID, what the daemon
// holds after a crash during a run that the person wrote to: the run intent
// still running (no executor holds it) and the person's message queued for it
// exactly as acceptSessionsV3QueuedNote stores it, without a delivery
// receipt. It returns the note.
func seedInterruptedRunWithQueuedNote(t *testing.T, f *queuedNoteFixture, sessionID, runID, noteRequest, content string) pebblestore.MessageSnapshot {
	t.Helper()
	principal := testPrincipal()
	now := time.Now().UnixMilli()
	// The run's pending intent, then its start, as the executor records them
	// (no executor is handed the run: nothing enqueues it).
	for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
		key := "seed-" + status + ":" + runID
		eventType := ""
		if status == sessionruntime.RunIntentRunning {
			eventType = "session.run.started"
		}
		if _, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
			Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: eventType, NowUnixMs: now,
			RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, RunID: runID, RunSessionID: sessionID, Status: status, CreatedAt: now, StartedAt: now, UpdatedAt: now},
		}); err != nil {
			t.Fatalf("seed %s run: %v", status, err)
		}
	}
	note := pebblestore.MessageSnapshot{SessionID: sessionID, Role: "user", Content: content, CreatedAt: now, Metadata: map[string]any{
		"feedback_note": true, runruntime.QueuedNoteMetadataKey: true, runruntime.QueuedNoteStatusMetadata: runruntime.QueuedNoteStatusQueued, runruntime.QueuedNoteRunMetadataKey: runID,
	}}
	result, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: noteRequest, IdempotencyKey: noteRequest, PayloadHash: noteRequest, RequestHash: noteRequest,
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &note, NowUnixMs: now,
	})
	if err != nil || result.Message == nil {
		t.Fatalf("seed queued note: %v", err)
	}
	return *result.Message
}

// Purpose: the person's input survives a restart. Requirement: when the
// daemon restarts during a Companion run that the person had written to, the
// recovery pass (recoverDurableRuns) marks that run interrupted and the
// person's queued message, which no model step received, starts the
// Companion's next turn: that turn carries it once as the person's message and
// receipts it once; the interrupted run is not replayed. A second recovery
// pass starts nothing more. An ordinary chat in the same state keeps the
// earlier contract (its note waits for the person's next turn; no turn
// starts). Threat: a message the person sent while the Companion was busy is
// stored but never answered after a restart (activity said "queued", nothing
// ran), or answered twice when recovery repeats. Boundary:
// sessionV3Executor.recoverDurableRuns, failStaleRunningRunForRecovery,
// resumeCompanionQueuedInput, startQueuedNoteFollowup, the executor's backlog
// refill and run loop with a scripted provider. Narrowest layer: the real
// executor and durable store, since the contract is the recovery path itself;
// the crash is represented by the durable records it leaves (a running intent
// no executor holds and an unreceipted queued note), the same input recovery
// reads on daemon start.
func TestCompanionQueuedInputStartsTheNextTurnAfterARestart(t *testing.T) {
	const question = "while that runs: is my backup drive still mounted"
	f := newCompanionRunFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "yes, the backup drive is mounted", StopReason: "stop"}
	})
	interrupted := "companion-run-before-restart"
	note := seedInterruptedRunWithQueuedNote(t, f, f.sessionID, interrupted, "question-during-crash", question)
	if activity := f.companionActivity(); !activity.Run.Active || len(activity.QueuedInputs) != 1 || activity.QueuedInputs[0].DeliveryStatus != CompanionInputQueued {
		t.Fatalf("activity before recovery = %+v, want the running run with the note queued", activity)
	}

	chat := f.createSession("ordinary-chat")
	chatNote := seedInterruptedRunWithQueuedNote(t, f, chat, "chat-run-before-restart", "chat-question-during-crash", "an ordinary chat question")

	f.server.v3SessionExecutor.recoverDurableRuns(context.Background())

	f.waitRun(f.sessionID, interrupted, sessionruntime.RunIntentInterrupted)
	followup := stableSessionsV3PrimaryRunID(f.sessionID, "queued-notes:"+note.ID)
	f.waitRun(f.sessionID, followup, sessionruntime.RunIntentCompleted)
	if receipts := f.receipts(note.ID); len(receipts) != 1 || receipts[0].Metadata["run_id"] != followup {
		t.Fatalf("receipts = %+v, want exactly one in the follow-up %s", receipts, followup)
	}
	requests := f.requests()
	if len(requests) != 1 {
		t.Fatalf("provider calls = %d, want one (the follow-up turn; the interrupted run is not replayed)", len(requests))
	}
	if toolResults, users := countInInput(requests[0].Input, question); toolResults != 0 || users != 1 {
		t.Fatalf("follow-up carried the question %d times as tool output and %d as user input, want 0 and 1", toolResults, users)
	}
	if got := f.latest(f.sessionID, "assistant"); !strings.Contains(got, "backup drive is mounted") {
		t.Fatalf("the Companion did not answer the queued question: %q", got)
	}

	// Recovery again (a second daemon start) starts nothing more.
	f.server.v3SessionExecutor.recoverDurableRuns(context.Background())
	intents := f.runIntents()
	if len(intents) != 2 {
		t.Fatalf("run intents after a repeated recovery = %+v, want the interrupted run and one follow-up", intents)
	}
	if activity := f.companionActivity(); activity.Run.Active || len(activity.QueuedInputs) != 0 || activity.InputDelivery != CompanionInputStartsTurn {
		t.Fatalf("activity after the follow-up = %+v, want idle with nothing queued", activity)
	}

	// The ordinary chat: interrupted, nothing started, its note still waits.
	f.waitRun(chat, "chat-run-before-restart", sessionruntime.RunIntentInterrupted)
	chatIntents, err := f.sessions.Store().ListRunIntents(chat, 10)
	if err != nil || len(chatIntents) != 1 {
		t.Fatalf("ordinary chat run intents = %+v (%v), want only the interrupted run", chatIntents, err)
	}
	if receipts := f.receipts(chatNote.ID); len(receipts) != 0 {
		t.Fatalf("ordinary chat note receipted: %+v", receipts)
	}
	if len(f.requests()) != 1 {
		t.Fatalf("provider calls = %d, the ordinary chat must not have started a turn", len(f.requests()))
	}
}

// Purpose: only a run the daemon ended hands the Companion's queued input to
// a new turn. Requirement: companionRunEndLeavesQueuedInput is true for an
// interrupted run (restart) and for a cancel during shutdown that the person
// did not make; false for the person's stop (even during shutdown), for any
// other cancel (usage limit, deletion) and for statuses the ordinary rule
// already covers or that are not terminal. Threat: a person's explicit stop
// immediately restarted by their own queued note, or a usage-limit cancel
// looping into new turns. Boundary: companionRunEndLeavesQueuedInput.
// Narrowest layer: the pure decision over durable intent shapes.
func TestCompanionRunEndLeavesQueuedInputOnlyWhenTheDaemonEndedIt(t *testing.T) {
	cases := []struct {
		name     string
		intent   sessionruntime.SessionRunIntent
		shutdown bool
		want     bool
	}{
		{"interrupted by restart", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentInterrupted}, false, true},
		{"cancelled by shutdown", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentCancelled}, true, true},
		{"person stop during shutdown", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentCancelled, StoppedBy: pebblestore.V3RunStopperUser}, true, false},
		{"person stop", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentCancelled, StoppedBy: pebblestore.V3RunStopperUser}, false, false},
		{"usage limit or deletion cancel", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentCancelled}, false, false},
		{"completed (ordinary rule)", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentCompleted}, true, false},
		{"still running", sessionruntime.SessionRunIntent{Status: sessionruntime.RunIntentRunning}, true, false},
	}
	for _, tc := range cases {
		if got := companionRunEndLeavesQueuedInput(tc.intent, tc.shutdown); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Purpose: activity says truthfully what new input waits for. Requirement:
// a started tool call of the active run is listed (device calls marked) and
// input_delivery is after_tool_calls until its terminal event is recorded,
// whether the call ID arrives in the tool message metadata or only in the
// event payload; a terminal event of another run or call leaves it listed;
// when the run ends every call of that run is dropped while another run's
// stays; with no active run input starts a turn, with no call in flight it
// reaches the next step. Threat: "your message reaches it now" while a device
// command runs, or a call shown running forever after its run ended.
// Boundary: noteToolStarted, noteToolEnded, clearToolCalls,
// companionToolCallsActivity. Narrowest layer: the executor's tracker and the
// activity derivation over it (the end-to-end turn below checks the real
// writers clear what they start).
func TestCompanionActivityTracksToolCallsInFlight(t *testing.T) {
	server := &Server{}
	e := &sessionV3Executor{server: server}
	server.v3SessionExecutor = e
	job := sessionV3ExecutorJob{SessionID: "cmp", RunID: "run-1"}
	other := sessionV3ExecutorJob{SessionID: "cmp", RunID: "run-0"}

	if calls, _, delivery := server.companionToolCallsActivity("cmp", ""); len(calls) != 0 || delivery != CompanionInputStartsTurn {
		t.Fatalf("idle: %+v %s", calls, delivery)
	}
	if calls, _, delivery := server.companionToolCallsActivity("cmp", "run-1"); len(calls) != 0 || delivery != CompanionInputNextStep {
		t.Fatalf("model step: %+v %s", calls, delivery)
	}
	e.noteToolStarted(job, runruntime.StreamEvent{CallID: "c-device", ToolName: "machine_run"}, 10)
	e.noteToolStarted(job, runruntime.StreamEvent{CallID: "c-read", ToolName: "read"}, 11)
	e.noteToolStarted(other, runruntime.StreamEvent{CallID: "c-old", ToolName: "read"}, 5)
	calls, more, delivery := server.companionToolCallsActivity("cmp", "run-1")
	if delivery != CompanionInputAfterToolCalls || more || len(calls) != 2 || calls[0].CallID != "c-device" || !calls[0].Device || calls[1].Device {
		t.Fatalf("in flight: %+v more=%v %s", calls, more, delivery)
	}
	// Another run's terminal event for a reused call id, and a non-terminal
	// event, end nothing.
	e.noteToolEnded(other, sessionruntime.SessionMutationInput{EventType: "session.tool.completed", Message: &pebblestore.MessageSnapshot{Metadata: map[string]any{"call_id": "c-device"}}})
	e.noteToolEnded(job, sessionruntime.SessionMutationInput{EventType: "session.tool.delta", Message: &pebblestore.MessageSnapshot{Metadata: map[string]any{"call_id": "c-device"}}})
	if calls, _, _ := server.companionToolCallsActivity("cmp", "run-1"); len(calls) != 2 {
		t.Fatalf("a foreign or non-terminal event ended a call: %+v", calls)
	}
	e.noteToolEnded(job, sessionruntime.SessionMutationInput{EventType: "session.tool.completed", Message: &pebblestore.MessageSnapshot{Metadata: map[string]any{"call_id": "c-device"}}})
	payload, _ := json.Marshal(map[string]any{"call_id": "c-read"})
	e.noteToolEnded(job, sessionruntime.SessionMutationInput{EventType: "session.tool.failed", EventPayload: payload})
	if calls, _, delivery := server.companionToolCallsActivity("cmp", "run-1"); len(calls) != 0 || delivery != CompanionInputNextStep {
		t.Fatalf("after terminal events: %+v %s", calls, delivery)
	}
	e.noteToolStarted(job, runruntime.StreamEvent{CallID: "c-stuck", ToolName: "bash"}, 12)
	e.clearToolCalls(job)
	if calls := e.inFlightToolCalls("cmp", "run-1"); len(calls) != 0 {
		t.Fatalf("calls survive their run's end: %+v", calls)
	}
	if calls := e.inFlightToolCalls("cmp", "run-0"); len(calls) != 1 || calls[0].CallID != "c-old" {
		t.Fatalf("another run's call was dropped: %+v", calls)
	}
}

// Purpose: background results that reach the Companion mid-turn are reported
// truthfully. Requirement: a background command's outcome delivered while a
// Companion run is active is pinned to that run as a wake note (store
// decision, prepareSessionWake); activity lists it as waiting_for_run for that
// run, with its source and wake id; once the run's next step has read it (its
// delivery receipt is recorded) activity no longer lists it, and it started
// no competing turn. Threat: a result shown as delivered before any step read
// it, a read result shown as still waiting, or a background result starting a
// second run that races the person's turn. Boundary: prepareSessionWake,
// the run loop's feedback claim and receipt, companionBackgroundDeliveries.
// Narrowest layer: the real executor, store and HTTP route with a scripted
// provider.
func TestCompanionActivityReportsBackgroundDeliveries(t *testing.T) {
	var (
		mu     sync.Mutex
		during CompanionActivity
		runID  string
	)
	f := newCompanionRunFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "the build finished; back to your question", StopReason: "stop"}
		}
		principal := testPrincipal()
		key := "wake-build-1"
		res, err := f.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
			SessionID: f.sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
			Kind:        pebblestore.V3SessionMutationAppendMessage,
			Message:     &pebblestore.MessageSnapshot{Role: "user", Content: "Background command finished: make build (exit 0)"},
			SessionWake: &pebblestore.V3SessionWakeMutation{ID: "build-1", Source: pebblestore.SessionWakeSourceBackgroundCommand},
			NowUnixMs:   time.Now().UnixMilli(),
		})
		if err != nil || res.Conflict != nil || res.Error != nil {
			t.Errorf("deliver background outcome: %v %v %v", err, res.Conflict, res.Error)
		}
		activity := f.companionActivity()
		mu.Lock()
		during, runID = activity, activity.Run.RunID
		mu.Unlock()
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "state1", Name: "manage_companion", Arguments: `{"action":"state"}`}}}
	})
	f.say(f.sessionID, "turn-1", "start the build and tell me when it is done")
	mu.Lock()
	activity, active := during, runID
	mu.Unlock()
	if len(activity.BackgroundDeliveries) != 1 {
		t.Fatalf("deliveries during the run = %+v, want the build outcome", activity.BackgroundDeliveries)
	}
	if d := activity.BackgroundDeliveries[0]; d.WakeID != "build-1" || d.Source != pebblestore.SessionWakeSourceBackgroundCommand || d.RunID != active || d.Status != CompanionDeliveryWaitingForRun {
		t.Fatalf("delivery = %+v, want build-1 waiting for run %s", d, active)
	}
	after := f.companionActivity()
	if len(after.BackgroundDeliveries) != 0 || after.Run.Active {
		t.Fatalf("activity after the run read it = %+v", after)
	}
	if intents := f.runIntents(); len(intents) != 1 {
		t.Fatalf("run intents = %+v, want only the person's turn", intents)
	}
	if requests := f.requests(); len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(requests))
	} else if toolResults, users := countInInput(requests[1].Input, "make build (exit 0)"); toolResults != 0 || users != 1 {
		t.Fatalf("outcome reached the step %d times as tool output and %d as user input, want 0 and 1", toolResults, users)
	}
}

// Purpose: the real tool writers leave nothing in flight, and activity
// reports context freshness. Requirement: during a Companion turn's model
// step after a real tool call (its started and terminal events written by the
// provider tool invoker through the executor), activity lists no tool call
// and says input reaches the next step; after the turn it lists none and
// starts a turn, and its context section names the state cursor against the
// event head with maintenance freshness. Threat: tracking keyed on a field
// the real writers do not carry, leaving a call "running" for the rest of
// the run; a context section that does not follow the log. Boundary:
// emitSessionV3ProviderToolEvent, applySessionV3ProviderToolMutation,
// companionActivity. Narrowest layer: the real executor, run loop and HTTP
// route with a scripted provider.
func TestCompanionActivityAfterARealToolCall(t *testing.T) {
	var (
		mu     sync.Mutex
		during CompanionActivity
	)
	f := newCompanionRunFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			activity := f.companionActivity()
			mu.Lock()
			during = activity
			mu.Unlock()
			return provideriface.Response{Text: "done", StopReason: "stop"}
		}
		// A tool the Companion is offered (a read), so its call is started
		// and settled by the real invoker.
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "state1", Name: "manage_companion", Arguments: `{"action":"state"}`}}}
	})
	f.say(f.sessionID, "turn-1", "check the printer queue on my laptop please")
	mu.Lock()
	activity := during
	mu.Unlock()
	if !activity.Run.Active || len(activity.ToolCalls) != 0 || activity.InputDelivery != CompanionInputNextStep {
		t.Fatalf("activity during the step after the tool = %+v, want the run active, no call in flight, next_step", activity)
	}
	// The call really went through the started and terminal writers.
	events, err := f.sessions.Store().ListV3SessionEvents(f.sessionID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if strings.HasPrefix(event.EventType, "session.tool.") && strings.Contains(string(event.Payload), `"state1"`) {
			seen[event.EventType] = true
		}
	}
	if !seen["session.tool.started"] || !(seen["session.tool.completed"] || seen["session.tool.failed"]) {
		t.Fatalf("tool events of state1 = %v, want started and a terminal event", seen)
	}
	f.server.waitCompanionMaintenance()
	after := f.companionActivity()
	if after.Run.Active || len(after.ToolCalls) != 0 || after.InputDelivery != CompanionInputStartsTurn || len(after.BackgroundDeliveries) != 0 {
		t.Fatalf("activity after the turn = %+v", after)
	}
	if after.Context == nil || after.Context.LatestSeq == 0 || after.Context.Cursor > after.Context.LatestSeq || after.Context.Pending != after.Context.LatestSeq-after.Context.Cursor {
		t.Fatalf("context freshness = %+v, want the cursor against the event head", after.Context)
	}
	if (after.Context.Pending == 0) != (after.Context.Freshness == "current") {
		t.Fatalf("freshness %q disagrees with pending %d", after.Context.Freshness, after.Context.Pending)
	}
}

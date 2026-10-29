//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Shared fixture for background bash tests: a real temporary Pebble session
// store and session service (the canonical V3 mutation path), host execution
// (no sandbox or lease configured), and a fake run controller that records the
// runs a report wakes. No provider, network or ambient state is used.

const bgTestWait = 15 * time.Second

type bgFakeController struct {
	enqueued chan string
}

func (f *bgFakeController) CancelSessionRun(identity.Principal, string, string, string) (bool, error) {
	return false, nil
}

func (f *bgFakeController) EnqueueSessionRun(_ identity.Principal, sessionID, runID, _ string) bool {
	f.enqueued <- sessionID + "|" + runID
	return true
}

func (f *bgFakeController) CompactSession(context.Context, identity.Principal, string, string) (map[string]any, error) {
	return nil, nil
}

type bgFixture struct {
	runtime    *Runtime
	sessions   *sessionruntime.Service
	store      *pebblestore.SessionStore
	db         *pebblestore.Store
	controller *bgFakeController
	workspace  string
	principal  identity.Principal
	reported   chan string
}

func newBGFixture(t *testing.T, sessionIDs ...string) *bgFixture {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := pebblestore.Open(filepath.Join(root, "state.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	store := pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	f := &bgFixture{
		sessions:   sessionruntime.NewService(store, events),
		store:      store,
		db:         db,
		controller: &bgFakeController{enqueued: make(chan string, 16)},
		workspace:  workspace,
		principal:  identity.Principal{Type: identity.PrincipalTypeUser, UserID: "bg-user", AccountScopeID: "bg-account"},
		reported:   make(chan string, 64),
	}
	for _, id := range sessionIDs {
		if err := store.CreateSession(pebblestore.SessionSnapshot{ID: id, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID, WorkspacePath: workspace}); err != nil {
			t.Fatal(err)
		}
	}
	f.runtime = NewRuntime(2)
	f.runtime.SetManageSessionService(f.sessions)
	f.runtime.SetManageSessionController(f.controller)
	f.runtime.SetBackgroundWorkStore(store)
	// Production delivery, plus a signal so tests wait on the event instead
	// of sleeping.
	f.runtime.background.report = func(c *backgroundCommand, ended string) {
		f.runtime.reportBackgroundCommand(c, ended)
		f.reported <- c.id
	}
	t.Cleanup(func() {
		_ = f.runtime.Close()
		_ = db.Close()
	})
	return f
}

func (f *bgFixture) scope(sessionID string) WorkspaceScope {
	return WorkspaceScope{PrimaryPath: f.workspace, Roots: []string{f.workspace}, SessionID: sessionID, Principal: f.principal}
}

func (f *bgFixture) bash(t *testing.T, sessionID string, args map[string]any) (map[string]any, error) {
	t.Helper()
	if _, ok := args["explanation"]; !ok {
		args["explanation"] = []any{"Run a bounded fixture command in the temporary workspace."}
	}
	if _, ok := args["category"]; !ok {
		args["category"] = "read"
	}
	if _, ok := args["critical"]; !ok {
		args["critical"] = false
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), bgTestWait)
	defer cancel()
	out, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, f.scope(sessionID), Call{CallID: "bg-call", Name: "bash", Arguments: string(raw)})
	payload := map[string]any{}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		if decodeErr := json.Unmarshal([]byte(out), &payload); decodeErr != nil {
			t.Fatalf("decode %q: %v", out, decodeErr)
		}
	}
	return payload, err
}

func (f *bgFixture) waitReported(t *testing.T, id string) {
	t.Helper()
	deadline := time.After(bgTestWait)
	for {
		select {
		case got := <-f.reported:
			if got == id {
				return
			}
		case <-deadline:
			t.Fatalf("background command %s did not report", id)
		}
	}
}

func (f *bgFixture) command(t *testing.T, sessionID, id string) *backgroundCommand {
	t.Helper()
	c, err := f.runtime.background.get(sessionID, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitDone(t *testing.T, c *backgroundCommand) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(bgTestWait):
		t.Fatalf("background command %s did not end", c.id)
	}
}

func (f *bgFixture) messages(t *testing.T, sessionID string) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := f.sessions.ListSessionMessages(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func (f *bgFixture) backgroundMessages(t *testing.T, sessionID string) []pebblestore.MessageSnapshot {
	t.Helper()
	var out []pebblestore.MessageSnapshot
	for _, m := range f.messages(t, sessionID) {
		if m.Metadata["source"] == "background_command" {
			out = append(out, m)
		}
	}
	return out
}

// recordRun records a run intent status for sessionID through the canonical
// V3 mutation (the same RecordRunIntent the executor uses).
func (f *bgFixture) recordRun(t *testing.T, sessionID, runID, status string) {
	t.Helper()
	now := time.Now().UnixMilli()
	key := runID + ":" + status
	f.apply(t, pebblestore.V3SessionMutationInput{
		SessionID: sessionID, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: pebblestore.V3SessionMutationRecordRunIntent, NowUnixMs: now,
		RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, EpochID: "epoch-00000000000000000001", UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID, Status: status, CreatedAt: now, UpdatedAt: now},
	})
}

func (f *bgFixture) startRun(t *testing.T, sessionID, runID string) {
	t.Helper()
	f.recordRun(t, sessionID, runID, pebblestore.V3RunIntentPendingExecutor)
	f.recordRun(t, sessionID, runID, pebblestore.V3RunIntentRunning)
	state, ok, err := f.sessions.GetSessionRunState(sessionID)
	if err != nil || !ok || !state.Active || state.RunID != runID {
		t.Fatalf("fixture run is not active: state=%+v ok=%v err=%v", state, ok, err)
	}
}

func (f *bgFixture) endRun(t *testing.T, sessionID, runID string) {
	t.Helper()
	f.recordRun(t, sessionID, runID, pebblestore.V3RunIntentCompleted)
	if state, ok, err := f.sessions.GetSessionRunState(sessionID); err != nil || (ok && state.Active) {
		t.Fatalf("fixture run still active: state=%+v err=%v", state, err)
	}
}

func (f *bgFixture) apply(t *testing.T, input pebblestore.V3SessionMutationInput) {
	t.Helper()
	res, err := f.sessions.ApplySessionMutation(input)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict != nil || res.Error != nil {
		t.Fatalf("fixture mutation rejected: conflict=%v error=%v", res.Conflict, res.Error)
	}
}

func expectNoWake(t *testing.T, f *bgFixture) {
	t.Helper()
	select {
	case got := <-f.controller.enqueued:
		t.Fatalf("unexpected run enqueued: %s", got)
	default:
	}
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(bgTestWait)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not record its pid in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundBashAtLimitDeliversExitCodeAndOutputToIdleConversation(t *testing.T) {
	// Purpose: a command that outlives its time limit is not lost: the call
	// returns at the limit with a background id and no exit code (neither pass
	// nor failure), and when the command ends its exit code and output are
	// pinned to the originating idle conversation, which is woken through the
	// canonical message + run intent mutation and the run controller.
	// Threat: the result is killed/lost at the limit, or reported as a pass.
	// Owners: Runtime.executeBashTool, backgroundCommands.adopt/launch,
	// Runtime.deliverBackgroundReport/wakeWithBackgroundReport.
	// Layer: the tool boundary with a real session store is the narrowest
	// layer that proves both the call answer and the durable conversation update.
	f := newBGFixture(t, "s-idle")
	start := time.Now()
	payload, err := f.bash(t, "s-idle", map[string]any{"command": "echo early; sleep 1; echo done-late; exit 3", "timeout_ms": 200})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("call did not return at its limit: %s", elapsed)
	}
	id, _ := payload["background_id"].(string)
	if payload["backgrounded"] != true || payload["status"] != "running" || id == "" || payload["timed_out"] != false {
		t.Fatalf("not moved to the background: %#v", payload)
	}
	if _, has := payload["exit_code"]; has {
		t.Fatalf("a running command must not carry an exit code: %#v", payload)
	}
	if !strings.Contains(fmt.Sprint(payload["output"]), "early") {
		t.Fatalf("output before the limit missing: %#v", payload)
	}
	f.waitReported(t, id)
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-idle|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
	case <-time.After(bgTestWait):
		t.Fatal("idle conversation was not woken")
	}
	reports := f.backgroundMessages(t, "s-idle")
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}
	report := reports[0]
	if report.Role != "user" || report.Metadata["feedback_note"] == true || report.Metadata["background_id"] != id {
		t.Fatalf("wake report has wrong shape: %+v", report)
	}
	if !strings.Contains(report.Content, "exited with code 3") || !strings.Contains(report.Content, "done-late") {
		t.Fatalf("report lacks exit code or output: %q", report.Content)
	}
	state, ok, err := f.sessions.GetSessionRunState("s-idle")
	if err != nil || !ok || !state.Active || state.SourceMessageID != report.ID {
		t.Fatalf("wake did not record a run intent for the report: %+v ok=%v err=%v", state, ok, err)
	}
	status, err := f.bash(t, "s-idle", map[string]any{"background_id": id, "command": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if status["status"] != "exited" || status["exit_code"] != float64(3) || !strings.Contains(fmt.Sprint(status["output"]), "done-late") {
		t.Fatalf("status by id is wrong: %#v", status)
	}
}

func TestBackgroundBashExplicitReturnsIDAtOnceAndReportsToRunningConversation(t *testing.T) {
	// Purpose: background=true returns an id at once; the agent keeps working;
	// on completion a running conversation gets the report as a non-triggering
	// note (read by its next provider step) without a second run, and when that
	// run ends without consuming the note the now idle conversation is woken.
	// Threat: the report is lost when the run ends in its last step, or a
	// second concurrent run is started for a running conversation.
	// Owners: Runtime.executeBashTool, deliverSessionWake (the session wake
	// every background outcome takes), SessionRunEnded.
	// Layer: tool boundary plus real store; the executor hook only forwards.
	f := newBGFixture(t, "s-run")
	f.startRun(t, "s-run", "run-1")
	start := time.Now()
	payload, err := f.bash(t, "s-run", map[string]any{"command": "sleep 0.3; echo finished-explicit", "background": true, "timeout_ms": 60000})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("explicit background did not return at once: %s", elapsed)
	}
	id, _ := payload["background_id"].(string)
	if id == "" || payload["status"] != "running" {
		t.Fatalf("no background id: %#v", payload)
	}
	f.waitReported(t, id)
	expectNoWake(t, f)
	reports := f.backgroundMessages(t, "s-run")
	if len(reports) != 1 || reports[0].Metadata["feedback_note"] != true {
		t.Fatalf("running conversation did not get one note: %+v", reports)
	}
	if !strings.Contains(reports[0].Content, "exited with code 0") || !strings.Contains(reports[0].Content, "finished-explicit") {
		t.Fatalf("note lacks result: %q", reports[0].Content)
	}
	// The run ends without a delivery receipt for the note: wake once.
	notes, err := f.store.ListV3SessionWakeNotes("s-run")
	if err != nil || len(notes) != 1 || notes[0].RunID != "run-1" || notes[0].NoteID != reports[0].ID {
		t.Fatalf("no durable wake note for the running run: %+v %v", notes, err)
	}
	f.endRun(t, "s-run", "run-1")
	f.runtime.SessionRunEnded("s-run", "run-1")
	var wokenRun string
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-run|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
		wokenRun = strings.TrimPrefix(got, "s-run|")
	case <-time.After(bgTestWait):
		t.Fatal("undelivered note did not wake the idle conversation")
	}
	// The wake recorded a pending run intent and enqueued it: that run is the
	// conversation's next run. The executor starts it (pending -> running);
	// any other run is refused while it is active.
	if state, ok, err := f.sessions.GetSessionRunState("s-run"); err != nil || !ok || state.RunID != wokenRun {
		t.Fatalf("wake did not record the enqueued run %q: state=%+v ok=%v err=%v", wokenRun, state, ok, err)
	}
	f.recordRun(t, "s-run", wokenRun, pebblestore.V3RunIntentRunning)
	// A delivered note (receipt present) is not delivered again.
	second, err := f.bash(t, "s-run", map[string]any{"command": "echo second", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	secondID := second["background_id"].(string)
	f.waitReported(t, secondID)
	notes, err = f.store.ListV3SessionWakeNotes("s-run")
	if err != nil || len(notes) != 1 || notes[0].WakeID != secondID {
		t.Fatalf("note for the running conversation was not recorded: %+v %v", notes, err)
	}
	if notes[0].RunID != wokenRun {
		t.Fatalf("note pinned to run %q, want the running woken run %q", notes[0].RunID, wokenRun)
	}
	if err := RecordFeedbackDeliveryForTest(f, "s-run", wokenRun, notes[0].NoteID); err != nil {
		t.Fatal(err)
	}
	f.endRun(t, "s-run", wokenRun)
	f.runtime.SessionRunEnded("s-run", wokenRun)
	expectNoWake(t, f)
	if left, _ := f.store.ListV3SessionWakeNotes("s-run"); len(left) != 0 {
		t.Fatalf("read note not settled: %+v", left)
	}
}

// RecordFeedbackDeliveryForTest writes the provider delivery receipt the run
// loop records after a step consumed a note (run.RecordFeedbackDelivery).
func RecordFeedbackDeliveryForTest(f *bgFixture, sessionID, runID, noteID string) error {
	key := "feedback-delivery:" + noteID + ":" + runID
	now := time.Now().UnixMilli()
	res, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: sessionID, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: pebblestore.V3SessionMutationAppendMessage, NowUnixMs: now,
		Message: &pebblestore.MessageSnapshot{ID: key, SessionID: sessionID, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID, Role: "system", Content: "delivered", CreatedAt: now},
	})
	if err != nil {
		return err
	}
	if res.Conflict != nil || res.Error != nil {
		return errors.New("receipt rejected")
	}
	return nil
}

func TestBackgroundBashStopAndArchiveKillCommandsWithoutReport(t *testing.T) {
	// Purpose: stopping a conversation's run, or archiving it, kills its
	// background commands (the process is gone) and no completion is pinned
	// to the conversation or wakes it.
	// Threat: orphaned processes keep running, or a killed command reports
	// back (possibly as success) into a stopped/archived conversation.
	// Owners: Runtime.StopSessionBackgroundCommands (called by the executor's
	// CancelRun), session.Service.SetTombstoneObserver (archive/delete funnel).
	// Layer: real host processes and the real archive funnel of the session service.
	f := newBGFixture(t, "s-stop", "s-archive")
	f.sessions.SetTombstoneObserver(func(sessionID, kind string) {
		f.runtime.StopSessionBackgroundCommands(sessionID, "conversation "+kind)
	})
	for _, tc := range []struct {
		session string
		stop    func()
	}{
		{"s-stop", func() { f.runtime.StopSessionBackgroundCommands("s-stop", "stopped by user") }},
		{"s-archive", func() {
			if _, err := f.sessions.ArchiveSessionsWithEvents([]string{"s-archive"}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		pidFile := filepath.Join(t.TempDir(), "pid")
		payload, err := f.bash(t, tc.session, map[string]any{"command": "sleep 30 & echo $! > '" + pidFile + "'; wait", "background": true, "category": "write"})
		if err != nil {
			t.Fatal(err)
		}
		id := payload["background_id"].(string)
		pid := readPID(t, pidFile)
		c := f.command(t, tc.session, id)
		if !f.runtime.SessionBackgroundCommandsRunning(tc.session) {
			t.Fatal("running command not visible to lease liveness")
		}
		tc.stop()
		waitDone(t, c)
		deadline := time.Now().Add(bgTestWait)
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("%s: background process %d survived", tc.session, pid)
		}
		if f.runtime.SessionBackgroundCommandsRunning(tc.session) {
			t.Fatalf("%s: still reported running", tc.session)
		}
		if _, err := f.runtime.background.get(tc.session, id); err == nil {
			t.Fatalf("%s: stopped command still readable", tc.session)
		}
		select {
		case got := <-f.reported:
			t.Fatalf("%s: killed command reported %s", tc.session, got)
		case <-time.After(200 * time.Millisecond):
		}
		if tc.session == "s-stop" && len(f.backgroundMessages(t, tc.session)) != 0 {
			t.Fatalf("%s: report pinned after stop", tc.session)
		}
		expectNoWake(t, f)
	}
}

func TestBackgroundBashCapsRejectWithoutStateChange(t *testing.T) {
	// Purpose: the per-conversation and per-account caps on running background
	// commands reject a new one with a clear error, start nothing and record
	// nothing.
	// Threat: unbounded background processes per conversation or account.
	// Owners: backgroundCommands.adopt and the named caps.
	// Layer: tool boundary with real host processes; the account cap uses
	// registry records of other conversations of the same account.
	f := newBGFixture(t, "s-cap", "s-other")
	var ids []string
	for i := 0; i < maxBackgroundCommandsPerSession; i++ {
		payload, err := f.bash(t, "s-cap", map[string]any{"command": "sleep 30", "background": true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, payload["background_id"].(string))
	}
	marker := filepath.Join(t.TempDir(), "over-cap-ran")
	before := len(f.runtime.background.byID)
	_, err := f.bash(t, "s-cap", map[string]any{"command": "touch '" + marker + "'", "background": true, "category": "write"})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("over the conversation cap was not rejected: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("rejected command ran")
	}
	if after := len(f.runtime.background.byID); after != before {
		t.Fatalf("rejected command changed the registry: %d -> %d", before, after)
	}
	// Fill the account with running records of other conversations.
	for i := len(ids); i < maxBackgroundCommandsPerAccount; i++ {
		c := &backgroundCommand{id: fmt.Sprintf("bg_fake_%d", i), sessionID: fmt.Sprintf("s-fake-%d", i), principal: f.principal, done: make(chan struct{}), cancel: func() {}, state: backgroundStateRunning, sink: &bashOutputSink{tail: newTailBuffer(16)}}
		f.runtime.background.byID[c.id] = c
		f.runtime.background.bySession[c.sessionID] = append(f.runtime.background.bySession[c.sessionID], c)
	}
	_, err = f.bash(t, "s-other", map[string]any{"command": "touch '" + marker + "'", "background": true, "category": "write"})
	if err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("over the account cap was not rejected: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("rejected command ran")
	}
	if f.runtime.background.running("s-other") {
		t.Fatal("rejected command recorded for s-other")
	}
	for i := len(ids); i < maxBackgroundCommandsPerAccount; i++ {
		f.runtime.background.stopSession(fmt.Sprintf("s-fake-%d", i))
	}
	// At the limit a foreground command that cannot move to the background
	// is stopped as before (timed_out) and says why.
	payload, err := f.bash(t, "s-cap", map[string]any{"command": "sleep 5", "timeout_ms": 100})
	if err != nil {
		t.Fatal(err)
	}
	if payload["timed_out"] != true || payload["backgrounded"] == true || !strings.Contains(fmt.Sprint(payload["background_error"]), "limit") {
		t.Fatalf("over-cap command at its limit not stopped with a reason: %#v", payload)
	}
	f.runtime.StopSessionBackgroundCommands("s-cap", "test cleanup")
}

func TestBackgroundBashOutputKeepsTailAndMarksTruncated(t *testing.T) {
	// Purpose: retained background output is capped: the end is kept, earlier
	// output dropped and marked truncated; what the model reads (the status
	// read by id and the report pinned to the conversation on wake) is bounded
	// by the one tool output cap, with the retained output saved for read.
	// Threat: unbounded daemon memory, a wake feeding hundreds of KB to the
	// model, or losing the end (where results are).
	// Owners: tailBuffer, maxBackgroundOutputBytes, ToolOutputCapBytes,
	// capToolOutputText, backgroundStatusResponse, backgroundReportText.
	// Layer: tool boundary with a real host command producing real output.
	f := newBGFixture(t, "s-tail")
	payload, err := f.bash(t, "s-tail", map[string]any{"command": fmt.Sprintf("head -c %d /dev/zero | tr '\\0' a; echo; echo END-MARKER", maxBackgroundOutputBytes+50000), "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	f.waitReported(t, id)
	<-f.controller.enqueued
	status, err := f.bash(t, "s-tail", map[string]any{"background_id": id, "command": "status"})
	if err != nil {
		t.Fatal(err)
	}
	output := fmt.Sprint(status["output"])
	if status["truncated"] != true || !strings.HasSuffix(strings.TrimSpace(output), "END-MARKER") || len(output) > ToolOutputCapBytes() {
		t.Fatalf("tail not kept or not marked: truncated=%v len=%d suffix=%q", status["truncated"], len(output), output[max(0, len(output)-20):])
	}
	if total, _ := status["output_bytes"].(float64); int(total) <= maxBackgroundOutputBytes {
		t.Fatalf("total output bytes not reported: %#v", status["output_bytes"])
	}
	saved, _ := status["full_output_path"].(string)
	if data, err := os.ReadFile(saved); err != nil || len(data) < maxBackgroundOutputBytes-1024 || !strings.Contains(string(data), "END-MARKER") {
		t.Fatalf("retained output not saved in full at %q: %v (%d bytes)", saved, err, len(data))
	}
	reports := f.backgroundMessages(t, "s-tail")
	if len(reports) != 1 || !strings.Contains(reports[0].Content, "END-MARKER") || len(reports[0].Content) > ToolOutputCapBytes()+2048 {
		t.Fatalf("report is not bounded by the tool output cap: count=%d", len(reports))
	}
	if !strings.Contains(reports[0].Content, "bytes omitted; full output saved to ") || !strings.Contains(reports[0].Content, fmt.Sprintf("last %d of", maxBackgroundOutputBytes)) {
		t.Fatalf("report does not say the output was cut and where it is: %q", reports[0].Content[:200])
	}
}

func TestBackgroundBashOtherConversationCannotReadKillOrReceive(t *testing.T) {
	// Purpose: a background command belongs to its conversation only:
	// another conversation of the same account cannot read or kill it by id,
	// and its report is pinned only to its own conversation.
	// Threat: cross-session disclosure of command output, interference, or a
	// report waking the wrong conversation.
	// Owners: backgroundCommands.get, Runtime.backgroundCommandAction,
	// Runtime.deliverBackgroundReport.
	// Layer: tool boundary with two real conversations in one store.
	f := newBGFixture(t, "s-owner", "s-intruder")
	payload, err := f.bash(t, "s-owner", map[string]any{"command": "sleep 0.5; echo owner-secret", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	for _, action := range []string{"status", "kill"} {
		out, err := f.bash(t, "s-intruder", map[string]any{"background_id": id, "command": action})
		if err == nil || !strings.Contains(err.Error(), "no background command") {
			t.Fatalf("%s from another conversation was not refused: %v %#v", action, err, out)
		}
		if strings.Contains(fmt.Sprint(out), "owner-secret") {
			t.Fatalf("%s leaked output", action)
		}
	}
	c := f.command(t, "s-owner", id)
	c.mu.Lock()
	killed := c.killed
	c.mu.Unlock()
	if killed {
		t.Fatal("another conversation killed the command")
	}
	f.waitReported(t, id)
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-owner|") {
			t.Fatalf("report woke another conversation: %s", got)
		}
	case <-time.After(bgTestWait):
		t.Fatal("owner was not woken")
	}
	if len(f.backgroundMessages(t, "s-intruder")) != 0 {
		t.Fatal("report pinned to another conversation")
	}
	if reports := f.backgroundMessages(t, "s-owner"); len(reports) != 1 || !strings.Contains(reports[0].Content, "owner-secret") {
		t.Fatalf("owner did not receive its report: %+v", reports)
	}
	expectNoWake(t, f)
}

func TestBackgroundBashHardLimitAndKillByID(t *testing.T) {
	// Purpose: a background command still has a hard upper bound (it is
	// stopped and reported as timed out, not as a pass), and the owning
	// conversation can kill one by id (which does not report back).
	// Owners: backgroundCommandBudget/maxBackgroundCommandDuration,
	// backgroundCommandAction kill.
	// Layer: tool boundary with real host processes and a shortened budget.
	f := newBGFixture(t, "s-hard")
	old := backgroundCommandBudget
	backgroundCommandBudget = 300 * time.Millisecond
	t.Cleanup(func() { backgroundCommandBudget = old })
	payload, err := f.bash(t, "s-hard", map[string]any{"command": "echo begun; sleep 20", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	f.waitReported(t, id)
	<-f.controller.enqueued
	reports := f.backgroundMessages(t, "s-hard")
	if len(reports) != 1 || !strings.Contains(reports[0].Content, "stopped at the background limit") || strings.Contains(reports[0].Content, "exited with code 0") {
		t.Fatalf("hard limit not reported as timed out: %+v", reports)
	}
	backgroundCommandBudget = old
	payload, err = f.bash(t, "s-hard", map[string]any{"command": "sleep 20", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id = payload["background_id"].(string)
	killed, err := f.bash(t, "s-hard", map[string]any{"background_id": id, "command": "kill", "category": "update"})
	if err != nil {
		t.Fatal(err)
	}
	if killed["status"] != "killed" {
		t.Fatalf("kill by id: %#v", killed)
	}
	select {
	case got := <-f.reported:
		t.Fatalf("killed command reported %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBackgroundBashRefusedWhereItCannotReportOrMustNotDetach(t *testing.T) {
	// Purpose: background is refused, with no command run, where a result
	// must come back in the same call (committed-source validation) or where
	// there is no conversation to report to.
	// Threat: a validation reported before it ends; a report with no owner.
	// Owners: Runtime.executeOne bash dispatch, Runtime.backgroundEligible.
	f := newBGFixture(t, "s-refuse")
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := f.bash(t, "s-refuse", map[string]any{"command": "touch '" + marker + "'", "background": true, "category": "write", "committed_source": map[string]any{"task_call_id": "x", "child_session_id": "y", "head_commit": "z"}})
	if err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("committed_source background not refused: %v", err)
	}
	_, err = f.bash(t, "", map[string]any{"command": "touch '" + marker + "'", "background": true, "category": "write"})
	if err == nil || !strings.Contains(err.Error(), "conversation") {
		t.Fatalf("background without a conversation not refused: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("refused command ran")
	}
}

func TestBackgroundBashAdoptRefusesCommandWhoseOutcomeIsRecorded(t *testing.T) {
	// Purpose: a command that ends right at its time limit is either adopted
	// before its outcome is recorded (and then reports back) or not adopted
	// at all (and the call answers in the foreground). It is never answered
	// "moved to the background" with no report coming.
	// Threat: the completing goroutine in launch records the state and
	// decides report=false under c.mu, then unlocks before close(c.done).
	// If adopt only checked done it adopted the command in that window: the
	// tool said "moved to the background" and the report was silently lost.
	// Owner: backgroundCommands.adopt (the timer branch of the bash tool calls
	// it). Layer: the window sits between two statements of one goroutine, so
	// the test builds that exact state directly instead of racing a process.
	f := newBGFixture(t, "s-race")
	newCommand := func() *backgroundCommand {
		return f.runtime.background.start(context.Background(), f.scope("s-race"), "true", "host", &bashOutputSink{tail: newTailBuffer(1024)}, false)
	}

	ended := newCommand()
	defer ended.cancel()
	// The goroutine's locked section: outcome and state recorded, report
	// decided while not adopted. done is still open.
	ended.mu.Lock()
	ended.outcome = bashOutcome{exitCode: 0}
	ended.state = backgroundStateExited
	willReport := ended.adopted && !ended.stopped && !ended.killed
	ended.mu.Unlock()
	if willReport || ended.finished() {
		t.Fatal("fixture is not in the unlock-before-close window")
	}
	if err := f.runtime.background.adopt(ended); !errors.Is(err, errBackgroundCommandEnded) {
		t.Fatalf("adopt in the window = %v, want errBackgroundCommandEnded", err)
	}
	ended.mu.Lock()
	adopted := ended.adopted
	ended.mu.Unlock()
	if adopted {
		t.Fatal("command adopted after its report decision: its report would be lost")
	}
	if _, err := f.runtime.background.get("s-race", ended.id); err == nil {
		t.Fatal("refused command registered in the conversation")
	}
	if f.runtime.SessionBackgroundCommandsRunning("s-race") {
		t.Fatal("refused command counted as running")
	}

	// Control: a command whose outcome is not yet recorded is adopted, so the
	// check does not refuse every command at the limit.
	running := newCommand()
	if err := f.runtime.background.adopt(running); err != nil {
		t.Fatalf("adopt of a running command: %v", err)
	}
	if _, err := f.runtime.background.get("s-race", running.id); err != nil {
		t.Fatalf("adopted command not readable: %v", err)
	}
	f.runtime.StopSessionBackgroundCommands("s-race", "test cleanup")
}

func TestBackgroundBashDeletingConversationStopsAutomationChildCommands(t *testing.T) {
	// Purpose: deleting a conversation also deletes its automation child
	// sessions; their background commands must be killed and forgotten too.
	// Threat: children were deleted without notifying the tombstone observer,
	// so their commands ran on for up to the hard limit, their completion was
	// silently dropped, and lease liveness kept their leases.
	// Owners: session.Service.tombstoneSessionsWithEventsExpected (delete
	// funnel and observer), Runtime.StopSessionBackgroundCommands.
	// Layer: the real session service delete path over a real store holding
	// the automation occurrence that links the child, with a real process.
	f := newBGFixture(t, "s-parent", "s-child", "s-bystander")
	f.sessions.SetTombstoneObserver(func(sessionID, kind string) {
		f.runtime.StopSessionBackgroundCommands(sessionID, "conversation "+kind)
	})
	// The occurrence record that links an automation child to its authoring
	// conversation (layout of SessionStore.ListAutomationV2ChildSessionIDs).
	occurrenceKey := fmt.Sprintf("automation/v2/occurrence/%x/%x/%s", f.principal.AccountScopeID, "s-parent", "occ-1")
	if err := f.db.PutJSON(occurrenceKey, pebblestore.AutomationV2Occurrence{ID: "occ-1", SessionID: "s-child"}); err != nil {
		t.Fatal(err)
	}
	children, err := f.store.ListAutomationV2ChildSessionIDs(f.principal.AccountScopeID, "s-parent")
	if err != nil || len(children) != 1 || children[0] != "s-child" {
		t.Fatalf("child link = %v, %v; want [s-child]", children, err)
	}

	start := func(sessionID string) (string, int, *backgroundCommand) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		payload, err := f.bash(t, sessionID, map[string]any{"command": "sleep 30 & echo $! > '" + pidFile + "'; wait", "background": true, "category": "write"})
		if err != nil {
			t.Fatal(err)
		}
		id := payload["background_id"].(string)
		return id, readPID(t, pidFile), f.command(t, sessionID, id)
	}
	childID, childPID, child := start("s-child")
	_, bystanderPID, bystander := start("s-bystander")
	defer func() { _ = syscall.Kill(bystanderPID, syscall.SIGKILL) }()

	if _, err := f.sessions.DeleteSessionsWithEvents([]string{"s-parent"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, child)
	deadline := time.Now().Add(bgTestWait)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(childPID) {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("child background process %d survived its deletion", childPID)
	}
	if f.runtime.SessionBackgroundCommandsRunning("s-child") {
		t.Fatal("deleted child still running for lease liveness")
	}
	if _, err := f.runtime.background.get("s-child", childID); err == nil {
		t.Fatal("deleted child's command still readable")
	}
	select {
	case got := <-f.reported:
		t.Fatalf("killed child command reported %s", got)
	case <-time.After(200 * time.Millisecond):
	}
	// An unrelated conversation is untouched.
	if bystander.finished() || !processAlive(bystanderPID) {
		t.Fatal("deleting another conversation stopped an unrelated command")
	}
	f.runtime.StopSessionBackgroundCommands("s-bystander", "test cleanup")
	waitDone(t, bystander)
}

func TestBackgroundBashReportDecidedUnderTheConversationLock(t *testing.T) {
	// Purpose: a report is never lost or doubled between reading the
	// conversation's run and writing the report. Requirement: the store
	// decides note or new turn under the conversation's mutation lock (one
	// mutation, no read-then-write window): a report for a conversation whose
	// run ended just before it is a new turn; one whose run started just
	// before it is a note pinned to that run, never a second concurrent run;
	// when that run ends unread, SessionRunEnded delivers it again as one new
	// turn and settles the durable wake note. Threat: the earlier windows
	// (run ending between the state read and the pin; a run starting between
	// the idle read and the wake) dropping the report or refusing it.
	// Owners: deliverSessionWake, pebblestore prepareSessionWake,
	// redeliverSessionWakeNotes.
	f := newBGFixture(t, "s-edge")
	f.startRun(t, "s-edge", "run-1")
	f.endRun(t, "s-edge", "run-1")
	payload, err := f.bash(t, "s-edge", map[string]any{"command": "echo ended-before", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	f.waitReported(t, payload["background_id"].(string))
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-edge|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
	case <-time.After(bgTestWait):
		t.Fatal("report for a conversation whose run ended did not wake it")
	}
	state, _, _ := f.sessions.GetSessionRunState("s-edge")
	// That turn is now active: the next report is pinned to it.
	f.recordRun(t, "s-edge", state.RunID, pebblestore.V3RunIntentRunning)
	payload, err = f.bash(t, "s-edge", map[string]any{"command": "echo started-before", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	f.waitReported(t, payload["background_id"].(string))
	expectNoWake(t, f)
	notes, err := f.store.ListV3SessionWakeNotes("s-edge")
	if err != nil || len(notes) != 1 || notes[0].RunID != state.RunID {
		t.Fatalf("report not pinned to the run that started first: %+v %v", notes, err)
	}
	f.endRun(t, "s-edge", state.RunID)
	f.runtime.SessionRunEnded("s-edge", state.RunID)
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-edge|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
	case <-time.After(bgTestWait):
		t.Fatal("unread note was not delivered again")
	}
	expectNoWake(t, f)
	if left, _ := f.store.ListV3SessionWakeNotes("s-edge"); len(left) != 0 {
		t.Fatalf("redelivered note kept: %+v", left)
	}
	reports := f.backgroundMessages(t, "s-edge")
	if len(reports) != 3 {
		t.Fatalf("want two reports and one redelivery, got %d", len(reports))
	}
}

func TestBackgroundBashDaemonRestartReportsStoppedNotExitCode(t *testing.T) {
	// Purpose: a daemon restart never turns a running background command into
	// a bare "exited with code -1, Output: none". Requirement: a running
	// command has a durable record; the daemon's shutdown (Runtime.Close)
	// stops it, delivers no report and marks the record stopped; the next
	// start (a new Runtime over the same store, RecoverBackgroundWork) tells
	// the conversation "Background command <id> was stopped by a daemon
	// restart at <time>; it did not finish", starts a turn for it, and forgets
	// the record, once. The command is not run again (it may have side
	// effects). A command the conversation stopped itself leaves no record.
	// Threat: the observed silent -1 after an auto-update restart.
	// Owners: backgroundCommands.stopAll, Runtime.recordBackgroundCommand,
	// RecoverBackgroundWork, BackgroundCommandRestartText.
	f := newBGFixture(t, "s-restart", "s-stopped")
	payload, err := f.bash(t, "s-restart", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	records, err := f.store.ListV3BackgroundCommands()
	if err != nil || len(records) != 1 || records[0].ID != id || records[0].SessionID != "s-restart" || records[0].StoppedAt != 0 {
		t.Fatalf("running command not recorded: %+v %v", records, err)
	}
	stopped, err := f.bash(t, "s-stopped", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.StopSessionBackgroundCommands("s-stopped", "test stop")
	if records, _ := f.store.ListV3BackgroundCommands(); len(records) != 1 {
		t.Fatalf("a command its conversation stopped kept a record: %+v (%s)", records, stopped["background_id"])
	}
	c := f.command(t, "s-restart", id)
	_ = f.runtime.Close() // the daemon shuts down
	waitDone(t, c)
	expectNoWake(t, f)
	if reports := f.backgroundMessages(t, "s-restart"); len(reports) != 0 {
		t.Fatalf("shutdown delivered a report: %+v", reports)
	}
	records, err = f.store.ListV3BackgroundCommands()
	if err != nil || len(records) != 1 || records[0].StoppedAt <= 0 {
		t.Fatalf("shutdown did not mark the record stopped: %+v %v", records, err)
	}
	// The next start, over the same store.
	next := NewRuntime(2)
	t.Cleanup(func() { _ = next.Close() })
	next.SetManageSessionService(f.sessions)
	next.SetManageSessionController(f.controller)
	next.SetBackgroundWorkStore(f.store)
	next.RecoverBackgroundWork()
	select {
	case got := <-f.controller.enqueued:
		if !strings.HasPrefix(got, "s-restart|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
	case <-time.After(bgTestWait):
		t.Fatal("restart did not tell the conversation")
	}
	reports := f.backgroundMessages(t, "s-restart")
	if len(reports) != 1 {
		t.Fatalf("restart reports: %+v", reports)
	}
	text := reports[0].Content
	if !strings.Contains(text, "Background command "+id+" was stopped by a daemon restart at ") || !strings.Contains(text, "it did not finish") || strings.Contains(text, "code -1") || strings.Contains(text, "exited") || strings.Contains(text, "Output: none") || reports[0].Metadata["stopped_by_restart"] != true {
		t.Fatalf("restart report: %q", text)
	}
	if records, _ := f.store.ListV3BackgroundCommands(); len(records) != 0 {
		t.Fatalf("record kept after the restart report: %+v", records)
	}
	next.RecoverBackgroundWork()
	expectNoWake(t, f)
	if reports := f.backgroundMessages(t, "s-restart"); len(reports) != 1 {
		t.Fatalf("restart reported twice: %d", len(reports))
	}
	// A daemon killed before it could record the stop: still reported.
	when := BackgroundCommandRestartText(pebblestore.V3BackgroundCommandRecord{ID: "bg_x", Command: "make"}, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(when, "stopped by a daemon restart before 2026-10-01T12:00:00Z; it did not finish") {
		t.Fatalf("unrecorded stop: %q", when)
	}
}

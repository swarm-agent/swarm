package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// The decision's text lives only in the Companion's own tool calls: the
// person's messages never contain these words, so where they appear is where
// the maintained context put them.
const (
	acceptanceDecisionID  = "decision-inventory-db"
	acceptanceOldText     = "PostgreSQL"
	acceptanceCorrectText = "CockroachDB"

	acceptanceDecide   = "For the inventory service database, record the choice from our vendor review"
	acceptanceRecall   = "Which database did we choose for the inventory service again"
	acceptanceCorrect  = "Correction on the inventory service database: we switched after the load test"
	acceptanceCheck    = "Remind me what the inventory service database is now"
	acceptanceForget   = "Please forget the inventory service database decision entirely"
	acceptanceAfter    = "What do we still know about the inventory service database"
	acceptanceReplay   = "Replay the original inventory service database record exactly"
	acceptanceFillerFm = "Garden question %d: when should I water the tomato seedlings on the balcony"
)

var acceptanceDecisionArgs = map[string]any{
	"action": "record_decision", "id": acceptanceDecisionID,
	"title":   "Inventory service database choice",
	"summary": "Use " + acceptanceOldText + " 16 for the inventory service database",
}

// olderDecisionScript is the scripted model (no live provider) of the
// Companion: each person message maps to the native manage_companion calls a
// model would make; once a tool answer is the last item it replies plainly.
// It keeps every request so the test can read the context block it carried.
type olderDecisionScript struct {
	mu       sync.Mutex
	inputs   map[string][]map[string]any
	revision int64
}

func (s *olderDecisionScript) step(name string, input []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs[name] = input
}

func (s *olderDecisionScript) input(t *testing.T, name string) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	input, ok := s.inputs[name]
	if !ok {
		t.Fatalf("the model never ran step %q", name)
	}
	return input
}

func (s *olderDecisionScript) setRevision(revision int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision = revision
}

func acceptanceCompanionCall(id string, args map[string]any) provideriface.FunctionCall {
	raw, _ := json.Marshal(args)
	return provideriface.FunctionCall{CallID: id, Name: tool.ManageCompanionToolName, Arguments: string(raw)}
}

func (s *olderDecisionScript) model(req provideriface.Request) provideriface.Response {
	if lastItemIsToolOutput(req.Input) {
		return provideriface.Response{Text: "Done.", StopReason: "stop"}
	}
	last := lastConfirmationTestUserText(req.Input)
	s.mu.Lock()
	revision := s.revision
	s.mu.Unlock()
	calls := func(c ...provideriface.FunctionCall) provideriface.Response {
		return provideriface.Response{FunctionCalls: c}
	}
	switch {
	case strings.Contains(last, acceptanceDecide):
		s.step("decide", req.Input)
		return calls(acceptanceCompanionCall("record-original", acceptanceDecisionArgs))
	case strings.Contains(last, acceptanceRecall):
		s.step("recall", req.Input)
		return provideriface.Response{Text: "We chose the database recorded in your decision.", StopReason: "stop"}
	case strings.Contains(last, acceptanceCorrect):
		s.step("correct", req.Input)
		return calls(acceptanceCompanionCall("record-correction", map[string]any{
			"action": "record_decision", "id": acceptanceDecisionID, "expected_revision": revision,
			"title":   "Inventory service database choice",
			"summary": "Use " + acceptanceCorrectText + " for the inventory service database",
		}))
	case strings.Contains(last, acceptanceCheck):
		s.step("check", req.Input)
		return calls(acceptanceCompanionCall("search-corrected", map[string]any{"action": "search", "query": "inventory service database"}))
	case strings.Contains(last, acceptanceForget):
		s.step("forget", req.Input)
		return calls(acceptanceCompanionCall("forget-decision", map[string]any{"action": "forget", "id": acceptanceDecisionID, "expected_revision": revision}))
	case strings.Contains(last, acceptanceAfter):
		s.step("after", req.Input)
		return calls(acceptanceCompanionCall("search-forgotten", map[string]any{"action": "search", "query": "inventory service database"}))
	case strings.Contains(last, acceptanceReplay):
		s.step("replay", req.Input)
		return calls(
			acceptanceCompanionCall("record-replayed", acceptanceDecisionArgs),
			acceptanceCompanionCall("search-replayed", map[string]any{"action": "search", "query": "inventory service database"}),
		)
	}
	return provideriface.Response{Text: "Water them early in the morning.", StopReason: "stop"}
}

// acceptanceCompanion opens a checkout-free Companion daemon over path with
// background state maintenance wired as on the daemon and the script as its
// model.
func acceptanceCompanion(t *testing.T, path string, script *olderDecisionScript) *queuedNoteFixture {
	t.Helper()
	f := checkoutFreeCompanionFixture(t, path)
	f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return script.model(req), nil
	}
	f.server.ConfigureCompanionMaintenance(f.sessions.Store().Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	view, _ := checkoutFreeEnsure(t, f)
	f.sessionID = view.SessionID
	return f
}

func acceptancePersonSeq(t *testing.T, f *queuedNoteFixture, content string) uint64 {
	t.Helper()
	for _, message := range f.messages() {
		if message.Role == "user" && message.Content == content {
			return message.GlobalSeq
		}
	}
	t.Fatalf("person message %q not found", content)
	return 0
}

// acceptanceNotReloaded fails when content was sent to the model outside the
// Companion context block (as transcript history).
func acceptanceNotReloaded(t *testing.T, input []map[string]any, content string) {
	t.Helper()
	for _, item := range input {
		raw, _ := json.Marshal(item["content"])
		if strings.Contains(string(raw), content) && !strings.Contains(string(raw), "Companion context bank") {
			t.Fatalf("%q was sent as history, not retrieved: %v", content, item)
		}
	}
}

// acceptanceNoFalseRunFailure fails (without stopping) when the state reports
// a run failure for a run that completed.
func acceptanceNoFalseRunFailure(t *testing.T, state *pebblestore.CompanionState, completedRuns ...string) {
	t.Helper()
	for _, result := range state.Results {
		for _, runID := range completedRuns {
			if result.Kind == "run" && result.Name == runID {
				t.Errorf("Companion state reports the completed run %s as %s: %+v", runID, result.Status, result)
			}
		}
	}
}

func acceptanceSegmentDecisionText(view CompanionStateView) (string, bool) {
	for _, seg := range view.Segments {
		for _, item := range seg.Items {
			if item.RecordID == acceptanceDecisionID {
				return item.Text, true
			}
		}
	}
	return "", false
}

// Purpose: an older decision the Companion recorded itself survives automatic
// summarization and restarts, is retrieved with its source when the person
// returns to it, and its correction and forgetting hold everywhere the
// Companion's maintained context reaches, through replay. Requirement: in a
// checkout-free Companion over a real Pebble path, (a) the model's own
// manage_companion record_decision call records the decision citing the
// person's message; (b) further real turns on another topic make background
// maintenance (the commit nudge, no record or maintenance call) seal at least
// one continuity segment holding the decision, while its source message leaves
// the recent tail; (c) after a daemon restart over the same store (d) a turn
// returning to the topic carries the decision with its source message number
// in the one bounded context block and not as history; (e) the model's
// record_decision with the same id at the expected revision corrects it: the
// next turn's block, GET /v3/companion/state (state and sealed segment) and
// the model's own search show only the corrected text; (f) the model's forget
// removes it: the next turn's block, the state read with its segments and
// search contain neither text; (g) after another restart and an explicit
// maintenance pass, the re-issued identical original record_decision is
// refused as forgotten and recreates nothing, and block, state, segments and
// search stay clean. Explicit account memory is unchanged throughout. Threat:
// a decision lost once history grows or the daemon restarts, a stale summary
// overriding the person's correction, a forgotten fact surviving in a sealed
// summary or resurrected by a replayed tool call or re-maintenance, or
// automatic context leaking into account memory. Boundary: the message route,
// sessionV3Executor and run loop, Runtime.executeManageCompanion via the
// provider tool invoker, Server.ManageCompanionContext,
// SessionStore.Put/DeleteCompanionContext (tombstone,
// propagateCompanionRecordChange), the session commit observer,
// MaintainCompanionState, companionStateRead and CompanionV3ContextMessages.
// Narrowest layer: the daemon's HTTP routes, executor, run loop and tool
// runtime over a real Pebble store reopened for each restart, because the
// requirement is the model's own tool calls through real runs; the model is a
// scripted provider double (no live model).
func TestCompanionOlderDecisionAcrossSummaryRestartCorrectionAndForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "older-decision.pebble")
	script := &olderDecisionScript{inputs: map[string][]map[string]any{}}
	f := acceptanceCompanion(t, path, script)
	principal := testPrincipal()
	account, user := principal.AccountScopeID, principal.UserID
	companion := f.sessionID
	memoryBefore, err := pebblestore.NewMemoryStore(f.sessions.Store().Underlying()).GetForAccount(account)
	if err != nil {
		t.Fatal(err)
	}

	// (a) The decision, recorded by the model's own call.
	companionSay(t, f, "decide-1", acceptanceDecide)
	decideSeq := acceptancePersonSeq(t, f, acceptanceDecide)
	recorded, raw := toolEnvelope(t, f, "record-original")
	data, _ := recorded["data"].(map[string]any)
	record, _ := data["record"].(map[string]any)
	source, _ := record["source"].(map[string]any)
	if recorded["state"] != tool.EnvelopeStateOK || record["id"] != acceptanceDecisionID || source["message_seq"] != float64(decideSeq) {
		t.Fatalf("recorded decision (message #%d): %s", decideSeq, raw)
	}

	// (b) Turns on another topic: maintenance seals a segment by itself.
	for i := 0; i < 22; i++ {
		companionSay(t, f, fmt.Sprintf("garden-%d", i), fmt.Sprintf(acceptanceFillerFm, i))
	}
	sealed := companionStateNow(t, f.confirmationFixture, companion)
	if sealed.State.Continuity.Sealed < 1 || sealed.Maintenance.Freshness != "current" {
		t.Fatalf("no segment sealed automatically: continuity %+v maintenance %+v", sealed.State.Continuity, sealed.Maintenance)
	}
	if text, ok := acceptanceSegmentDecisionText(sealed); !ok || !strings.Contains(text, acceptanceOldText) {
		t.Errorf("the sealed segments lack the decision the model recorded (%q %v); state decisions %+v", text, ok, sealed.State.Decisions)
	}

	// (c) Restart over the same store.
	f.server.waitCompanionMaintenance()
	f.close()
	f = acceptanceCompanion(t, path, script)
	if f.sessionID != companion {
		t.Fatalf("a different Companion after restart: %s, was %s", f.sessionID, companion)
	}

	// (d) Returning to the topic: the decision with its source, in the block.
	companionSay(t, f, "recall-1", acceptanceRecall)
	recallInput := script.input(t, "recall")
	block := contextBlock(t, recallInput)
	if !strings.Contains(block, acceptanceOldText) || !strings.Contains(block, acceptanceDecisionID) || !strings.Contains(block, fmt.Sprintf("message #%d", decideSeq)) || !strings.Contains(block, "Older messages are not loaded") {
		t.Fatalf("the recall turn's block lacks the decision with its source (message #%d):\n%s", decideSeq, block)
	}
	acceptanceNotReloaded(t, recallInput, acceptanceDecide)

	// (e) The model corrects it: same id, current revision.
	current, ok, err := f.sessions.Store().GetCompanionContext(account, user, acceptanceDecisionID)
	if err != nil || !ok {
		t.Fatalf("decision before correction: %v %v", ok, err)
	}
	script.setRevision(current.Revision)
	companionSay(t, f, "correct-1", acceptanceCorrect)
	if envelope, raw := toolEnvelope(t, f, "record-correction"); envelope["state"] != tool.EnvelopeStateOK || !strings.Contains(raw, acceptanceCorrectText) {
		t.Fatalf("correction: %s", raw)
	}
	corrected, _, _ := f.sessions.Store().GetCompanionContext(account, user, acceptanceDecisionID)
	if corrected.Revision != current.Revision+1 {
		t.Fatalf("correction revision %d, want %d", corrected.Revision, current.Revision+1)
	}
	companionSay(t, f, "check-1", acceptanceCheck)
	if block := contextBlock(t, script.input(t, "check")); !strings.Contains(block, acceptanceCorrectText) || strings.Contains(block, acceptanceOldText) {
		t.Fatalf("the turn after the correction:\n%s", block)
	}
	if _, raw := toolEnvelope(t, f, "search-corrected"); !strings.Contains(raw, acceptanceCorrectText) || strings.Contains(raw, acceptanceOldText) {
		t.Fatalf("search after the correction: %s", raw)
	}
	view := companionStateNow(t, f.confirmationFixture, companion)
	if text, ok := acceptanceSegmentDecisionText(view); !ok || !strings.Contains(text, acceptanceCorrectText) {
		t.Errorf("sealed segment decision after the correction = %q %v", text, ok)
	}
	if raw, _ := json.Marshal(view); strings.Contains(string(raw), acceptanceOldText) {
		t.Fatalf("the state read still has the old text: %s", raw)
	}

	// (f) The model forgets it.
	script.setRevision(corrected.Revision)
	companionSay(t, f, "forget-1", acceptanceForget)
	if envelope, raw := toolEnvelope(t, f, "forget-decision"); envelope["state"] != tool.EnvelopeStateOK {
		t.Fatalf("forget: %s", raw)
	}
	companionSay(t, f, "after-1", acceptanceAfter)
	gone := func(when string, view CompanionStateView, block, search string) {
		t.Helper()
		raw, _ := json.Marshal(view)
		for name, text := range map[string]string{"block": block, "search": search, "state": string(raw)} {
			words := []string{acceptanceOldText, acceptanceCorrectText}
			if name != "state" {
				// The state may name the id in a tool's refusal; the
				// decision itself must be gone from it.
				words = append(words, acceptanceDecisionID)
			}
			for _, word := range words {
				if at := strings.Index(text, word); at >= 0 {
					t.Fatalf("%s: the %s still has %q at: %s", when, name, word, text[max(0, at-300):min(len(text), at+300)])
				}
			}
		}
		for _, ref := range view.State.Decisions {
			if ref.RecordID == acceptanceDecisionID {
				t.Fatalf("%s: the state still lists the decision: %+v", when, ref)
			}
		}
		if _, ok := acceptanceSegmentDecisionText(view); ok {
			t.Fatalf("%s: a sealed segment still holds the decision", when)
		}
		if _, ok, err := f.sessions.Store().GetCompanionContext(account, user, acceptanceDecisionID); err != nil || ok {
			t.Fatalf("%s: the forgotten decision is stored: %v %v", when, ok, err)
		}
	}
	_, search := toolEnvelope(t, f, "search-forgotten")
	gone("after forget", companionStateNow(t, f.confirmationFixture, companion), contextBlock(t, script.input(t, "after")), search)

	// (g) Restart again, maintain again, and replay the original call.
	f.server.waitCompanionMaintenance()
	f.close()
	f = acceptanceCompanion(t, path, script)
	session, ok, err := f.sessions.GetSession(companion)
	if err != nil || !ok {
		t.Fatalf("Companion after the second restart: %v %v", ok, err)
	}
	if _, err := runruntime.MaintainCompanionState(f.sessions.Store(), session, 0, time.Now().UnixMilli()); err != nil {
		t.Fatalf("maintenance after the second restart: %v", err)
	}
	companionSay(t, f, "replay-1", acceptanceReplay)
	if envelope, raw := toolEnvelope(t, f, "record-replayed"); envelope["state"] == tool.EnvelopeStateOK || !strings.Contains(raw, "forgotten") {
		t.Fatalf("the replayed record was not refused as forgotten: %s", raw)
	}
	_, search = toolEnvelope(t, f, "search-replayed")
	replayed := companionStateNow(t, f.confirmationFixture, companion)
	gone("after restart and replay", replayed, contextBlock(t, script.input(t, "replay")), search)
	// The refused call is a failed tool call in a run that completed.
	acceptanceNoFalseRunFailure(t, replayed.State, stableSessionsV3PrimaryRunID(companion, "replay-1"))

	memoryAfter, err := pebblestore.NewMemoryStore(f.sessions.Store().Underlying()).GetForAccount(account)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memoryBefore, memoryAfter) {
		t.Fatal("the Companion's context changed explicit account memory")
	}
}

// pcRoundTripScript is the scripted model (no live provider) of a
// checkout-free Companion asked for help with the person's PC: it calls the
// real machine_run client tool and replies once a tool answer is the last
// item. It keeps every request it got.
type pcRoundTripScript struct {
	mu       sync.Mutex
	requests [][]map[string]any
}

func (s *pcRoundTripScript) model(req provideriface.Request) provideriface.Response {
	s.mu.Lock()
	s.requests = append(s.requests, req.Input)
	s.mu.Unlock()
	if lastItemIsToolOutput(req.Input) {
		return provideriface.Response{Text: "Thanks, I read your machine's answer.", StopReason: "stop"}
	}
	last := lastConfirmationTestUserText(req.Input)
	run := func(callID, command string) provideriface.Response {
		raw, _ := json.Marshal(map[string]any{"command": command})
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: callID, Name: pebblestore.MachineToolRun, Arguments: string(raw)}}}
	}
	switch {
	case strings.Contains(last, "check my disk space"):
		return run("pc-disk", "df -h /")
	case strings.Contains(last, "check the printer queue"):
		return run("pc-print", "lpstat -o")
	case strings.Contains(last, "check my memory"):
		return run("pc-mem", "free -m")
	}
	return provideriface.Response{Text: "Happy to help.", StopReason: "stop"}
}

func (s *pcRoundTripScript) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// toolOutputSeen is the output of callID the model received, if any.
func (s *pcRoundTripScript) toolOutputSeen(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, input := range s.requests {
		for _, item := range input {
			if stringMapValue(item, "type") == "function_call_output" && stringMapValue(item, "call_id") == callID {
				return stringMapValue(item, "output"), true
			}
		}
	}
	return "", false
}

// deviceDo sends a request as the person's app on their PC (its own client
// session), or as the owner without a device when clientSessionID is "".
func deviceDo(f *queuedNoteFixture, method, path, body, clientSessionID string) (int, string) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, withSendingDeviceTestActor(req, testPrincipal(), clientSessionID))
	return rec.Code, rec.Body.String()
}

func deviceSay(t *testing.T, f *queuedNoteFixture, requestID, content string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": content})
	if code, out := deviceDo(f, http.MethodPost, "/v3/sessions/"+f.sessionID+"/messages", string(body), sendingDeviceTestClient); code != http.StatusOK {
		t.Fatalf("message from the PC: %d %s", code, out)
	}
	return stableSessionsV3PrimaryRunID(f.sessionID, requestID)
}

func waitDeviceCall(t *testing.T, f *queuedNoteFixture, callID string) pebblestore.PermissionRecord {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, record := range f.pendingClientCalls(f.sessionID) {
			if record.CallID == callID {
				return record
			}
		}
	}
	t.Fatalf("no pending device call %s: %+v", callID, f.pendingClientCalls(f.sessionID))
	return pebblestore.PermissionRecord{}
}

func companionDeviceResult(state *pebblestore.CompanionState, callID string) (pebblestore.CompanionResultRef, bool) {
	for _, result := range state.Results {
		if result.Kind == "device" && result.CallID == callID {
			return result, true
		}
	}
	return pebblestore.CompanionResultRef{}, false
}

// Purpose: a Companion with no workspace helps with the person's PC through
// the real device path. Requirement: on a daemon with zero registered
// workspaces, a foreground Companion turn sent from the person's PC app whose
// model calls machine_run becomes one ready pending client call targeted at
// that app's client session (the device delivery path), and the run waits:
// Companion activity shows the run active with the device call in flight, and
// the person's message sent meanwhile is reported as queued behind it; the
// app's answer through POST /v3/sessions/{id}/permissions/{pid}/resolve reaches
// the model, which completes the turn; Companion state records the device
// result (tool, call, status) without copying its output. Negative paths: an
// answer without the target device's client session is refused and leaves the
// call pending; a second answer for an answered call starts no model step and
// adds no tool result; an error answer from the app reaches the model as the
// call's output and the turn ends cleanly, and the next turn works; with the
// device's machine rule turned off (the account-permissions record PATCH
// /v3/permissions writes) the next machine_run is refused to the model with no
// pending call and the turn still ends. Threat: a checkout-free Companion that
// cannot reach the person's PC, a device call answered by another caller or
// twice (a duplicate model step), a failed device call wedging the
// Companion, device output copied into the maintained context, or a revoked
// device still receiving commands. Boundary: the message route (sending
// device), sessionV3Executor and run loop, the client tool gate
// (permission.authorizeDeviceCall, CreatePending, ResolveFromClient /
// resolveForDeviceLocked), companionActivity, MaintainCompanionState
// (CompanionEventToolResult). Narrowest layer: the daemon's HTTP routes,
// executor, run loop and permission service over a real Pebble store with the
// device's client session in a separate secrets store as on the daemon; the
// model is a scripted provider double and the app's answers are HTTP calls
// made as its client session (no live model or real device). The machine rule
// is changed through the account-permissions store write behind the route
// because this fixture has no identity session service for the route's
// device check.
func TestCompanionNoWorkspacePCClientToolRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pc-round-trip.pebble")
	f := checkoutFreeCompanionFixture(t, path)
	script := &pcRoundTripScript{}
	f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return script.model(req), nil
	}
	principal := testPrincipal()
	if entries, err := f.server.workspace.ListKnownForPrincipal(principal, 10); err != nil || len(entries) != 0 {
		t.Fatalf("fixture has registered workspaces: %v %v", entries, err)
	}
	f.server.ConfigureCompanionMaintenance(f.sessions.Store().Underlying())
	t.Cleanup(f.server.waitCompanionMaintenance)
	view, _ := checkoutFreeEnsure(t, f)
	f.sessionID = view.SessionID
	connectSendingDeviceTestClient(t, f.server, f.permissions, principal)
	const diskOutput = "DEVICE-STDOUT-MARKER /dev/nvme0n1p2 42% used"

	// 1. machine_run becomes a ready call for the person's app; the run waits.
	diskRun := deviceSay(t, f, "disk-1", "Can you check my disk space on this PC")
	call := waitDeviceCall(t, f, "pc-disk")
	if call.ToolName != pebblestore.MachineToolRun || call.TargetDevice != sendingDeviceTestClient || call.Execution != "ready" || call.RunID != diskRun {
		t.Fatalf("device call = %+v, want a ready machine_run for %s in run %s", call, sendingDeviceTestClient, diskRun)
	}
	steps := script.count()
	deviceSay(t, f, "fan-1", "Also, is the fan supposed to be this loud")
	var activity CompanionActivity
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		activity = f.companionActivity()
		if len(activity.QueuedInputs) == 1 || time.Now().After(deadline) {
			break
		}
	}
	inFlight := false
	for _, c := range activity.ToolCalls {
		inFlight = inFlight || (c.CallID == "pc-disk" && c.Device && c.ToolName == pebblestore.MachineToolRun)
	}
	if !activity.Run.Active || activity.Run.RunID != diskRun || activity.PendingPermissions < 1 || len(activity.QueuedInputs) != 1 || activity.QueuedInputs[0].DeliveryStatus != CompanionInputQueued {
		t.Fatalf("activity while the device call waits = %+v", activity)
	}
	if !inFlight || activity.InputDelivery != CompanionInputAfterToolCalls {
		t.Errorf("activity while the device call waits on the app: tool calls %+v, input delivery %q; want pc-disk in flight on the device and after_tool_calls", activity.ToolCalls, activity.InputDelivery)
	}
	if script.count() != steps {
		t.Fatalf("the model ran while the device call waited: %d steps, was %d", script.count(), steps)
	}
	// An answer that is not the target device's is refused; the call waits.
	result := func(v map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"action": "allow_once", "approved_arguments": map[string]any{"result": v}})
		return string(raw)
	}
	resolvePath := "/v3/sessions/" + f.sessionID + "/permissions/" + call.ID + "/resolve"
	if code, out := deviceDo(f, http.MethodPost, resolvePath, result(map[string]any{"exit_code": 0, "stdout": "forged"}), ""); code == http.StatusOK {
		t.Fatalf("an answer without the device's client session was accepted: %s", out)
	}
	if pending := f.pendingClientCalls(f.sessionID); len(pending) != 1 || pending[0].ID != call.ID {
		t.Fatalf("the refused answer changed the call: %+v", pending)
	}

	// 2. The app's answer reaches the model; the turn completes.
	if code, out := deviceDo(f, http.MethodPost, resolvePath, result(map[string]any{"exit_code": 0, "stdout": diskOutput, "stderr": ""}), sendingDeviceTestClient); code != http.StatusOK {
		t.Fatalf("the app's answer: %d %s", code, out)
	}
	f.waitRun(f.sessionID, diskRun, sessionruntime.RunIntentCompleted)
	if seen, ok := script.toolOutputSeen("pc-disk"); !ok || !strings.Contains(seen, diskOutput) || strings.Contains(seen, "forged") {
		t.Fatalf("the model's view of the device answer = %q %v", seen, ok)
	}
	state := companionStateNow(t, f.confirmationFixture, f.sessionID)
	if got, ok := companionDeviceResult(state.State, "pc-disk"); !ok || got.Name != pebblestore.MachineToolRun || got.Status != "completed" {
		t.Errorf("Companion state device result = %+v %v in %+v", got, ok, state.State.Results)
	}
	if raw, _ := json.Marshal(state); strings.Contains(string(raw), "DEVICE-STDOUT-MARKER") {
		t.Fatalf("the device's output was copied into Companion state: %s", raw)
	}

	// 3. A second answer for the answered call: no model step, no result.
	steps = script.count()
	results := 0
	for _, m := range f.messages() {
		if record, ok := sessionsV3DecodeProviderToolResultRecord(m.Content); ok && m.Role == "tool" && record.CallID == "pc-disk" {
			results++
		}
	}
	code, out := deviceDo(f, http.MethodPost, resolvePath, result(map[string]any{"exit_code": 0, "stdout": "second answer"}), sendingDeviceTestClient)
	t.Logf("second answer for an answered call: %d %.200s", code, out)
	f.server.waitCompanionMaintenance()
	again := 0
	for _, m := range f.messages() {
		if record, ok := sessionsV3DecodeProviderToolResultRecord(m.Content); ok && m.Role == "tool" && record.CallID == "pc-disk" {
			again++
			if strings.Contains(record.Output, "second answer") {
				t.Fatalf("a second answer replaced the result: %+v", record)
			}
		}
	}
	if script.count() != steps || again != results || len(f.pendingClientCalls(f.sessionID)) != 0 {
		t.Fatalf("a second answer: %d model steps (was %d), %d results (was %d), pending %+v", script.count(), steps, again, results, f.pendingClientCalls(f.sessionID))
	}

	// 4. The app reports a failure: the model gets it, the turn ends, the
	// next turn works.
	printRun := deviceSay(t, f, "print-1", "Please check the printer queue on my PC")
	printCall := waitDeviceCall(t, f, "pc-print")
	if code, out := deviceDo(f, http.MethodPost, "/v3/sessions/"+f.sessionID+"/permissions/"+printCall.ID+"/resolve", result(map[string]any{"error": "lpstat: command not found"}), sendingDeviceTestClient); code != http.StatusOK {
		t.Fatalf("the app's error answer: %d %s", code, out)
	}
	f.waitRun(f.sessionID, printRun, sessionruntime.RunIntentCompleted)
	if seen, ok := script.toolOutputSeen("pc-print"); !ok || !strings.Contains(seen, "command not found") {
		t.Fatalf("the model's view of the failed device call = %q %v", seen, ok)
	}
	f.waitRun(f.sessionID, deviceSay(t, f, "thanks-1", "Thanks for checking"), sessionruntime.RunIntentCompleted)
	if got, ok := companionDeviceResult(companionStateNow(t, f.confirmationFixture, f.sessionID).State, "pc-print"); !ok || got.Name != pebblestore.MachineToolRun {
		t.Errorf("Companion state lacks the failed device call: %+v %v", got, ok)
	}

	// 5. The device's machine rule is turned off: the next call is refused.
	store := f.sessions.Store().Underlying()
	rules, err := store.GetAccountPermissions(principal.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	rule := rules.Devices[sendingDeviceTestClient]
	rule.Files = pebblestore.RuleOff
	rules.Devices[sendingDeviceTestClient] = rule
	if _, err := store.SetAccountPermissions(principal.AccountScopeID, rules.Revision, rules); err != nil {
		t.Fatal(err)
	}
	memRun := deviceSay(t, f, "mem-1", "Now check my memory usage please")
	f.waitRun(f.sessionID, memRun, sessionruntime.RunIntentCompleted)
	if pending := f.pendingClientCalls(f.sessionID); len(pending) != 0 {
		t.Fatalf("a call reached the app after its machine rule was turned off: %+v", pending)
	}
	if seen, ok := script.toolOutputSeen("pc-mem"); !ok || !strings.Contains(seen, "off") {
		t.Fatalf("the model's view of the refused call = %q %v", seen, ok)
	}
	if activity := f.companionActivity(); activity.Run.Active || len(activity.ToolCalls) != 0 || len(activity.QueuedInputs) != 0 {
		t.Fatalf("activity after the turns = %+v, want idle", activity)
	}
	acceptanceNoFalseRunFailure(t, companionStateNow(t, f.confirmationFixture, f.sessionID).State, diskRun, printRun, memRun)
}

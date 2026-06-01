package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV2RunStreamReadOnlyBindingMayResumeButCannotStartOrStop(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunStreamPolicyRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-readonly-resume-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadOnly, false)

	runID := seedPrimaryV2RunStreamForResumePolicyTest(t, server, sessionID)
	resumeFrame := readPrimaryV2RunStreamWebsocketFrame(t, server, sessionID, map[string]any{"type": "run.resume", "run_id": runID})
	if resumeFrame.Type != "resume.accepted" || !resumeFrame.OK || resumeFrame.SessionID != sessionID || resumeFrame.RunID != runID {
		t.Fatalf("resume frame = %+v, want accepted for read-only binding", resumeFrame)
	}
	if calls, stops := runner.snapshot(); calls != 0 || stops != 0 {
		t.Fatalf("resume mutated runner state: calls=%d stops=%d", calls, stops)
	}

	startFrame := readPrimaryV2RunStreamWebsocketFrame(t, server, sessionID, map[string]any{"type": "run.start", "prompt": "blocked"})
	if startFrame.Type != "error" || startFrame.OK || !strings.Contains(startFrame.Error, "read-only") {
		t.Fatalf("start frame = %+v, want read-only error", startFrame)
	}
	if calls, stops := runner.snapshot(); calls != 0 || stops != 0 {
		t.Fatalf("read-only start reached runner: calls=%d stops=%d", calls, stops)
	}

	stopFrame := readPrimaryV2RunStreamWebsocketFrame(t, server, sessionID, map[string]any{"type": "run.stop", "run_id": runID})
	if stopFrame.Type != "error" || stopFrame.OK || !strings.Contains(stopFrame.Error, "read-only") {
		t.Fatalf("stop frame = %+v, want read-only error", stopFrame)
	}
	if calls, stops := runner.snapshot(); calls != 0 || stops != 0 {
		t.Fatalf("read-only stop reached runner: calls=%d stops=%d", calls, stops)
	}
}

func TestSessionsV2RunStreamResumeRejectsDifferentSessionRunIDWithoutMutation(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunStreamPolicyRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-resume-session-a-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	otherSessionID := sessionID + "-other"

	otherRunID := seedPrimaryV2RunStreamForResumePolicyTest(t, server, otherSessionID)
	frame := readPrimaryV2RunStreamWebsocketFrame(t, server, sessionID, map[string]any{"type": "run.resume", "run_id": otherRunID})
	if frame.Type != "error" || frame.OK || frame.RunID != otherRunID || !strings.Contains(frame.Error, "run/session mismatch") {
		t.Fatalf("resume frame = %+v, want run/session mismatch", frame)
	}
	if calls, stops := runner.snapshot(); calls != 0 || stops != 0 {
		t.Fatalf("mismatched resume mutated runner state: calls=%d stops=%d", calls, stops)
	}
}

func TestSessionsV2RunStreamResumeDoesNotChangeLifecycleOwnerTransport(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunStreamPolicyRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-resume-owner-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	runID := seedPrimaryV2RunStreamForResumePolicyTest(t, server, sessionID)

	before := pebblestore.SessionLifecycleSnapshot{SessionID: sessionID, RunID: runID, Active: true, OwnerTransport: "background_api", Generation: 7, UpdatedAt: 10}
	if err := sessionSvc.UpsertLifecycle(before); err != nil {
		t.Fatalf("upsert lifecycle: %v", err)
	}

	frame := readPrimaryV2RunStreamWebsocketFrame(t, server, sessionID, map[string]any{"type": "run.resume", "run_id": runID})
	if frame.Type != "resume.accepted" || !frame.OK {
		t.Fatalf("resume frame = %+v, want accepted", frame)
	}
	after, ok, err := sessionSvc.GetLifecycle(sessionID)
	if err != nil || !ok {
		t.Fatalf("get lifecycle ok=%v err=%v", ok, err)
	}
	if after.OwnerTransport != before.OwnerTransport || after.Active != before.Active || after.RunID != before.RunID || after.Generation != before.Generation {
		t.Fatalf("lifecycle changed after resume: before=%+v after=%+v", before, after)
	}
	if calls, stops := runner.snapshot(); calls != 0 || stops != 0 {
		t.Fatalf("resume mutated runner state: calls=%d stops=%d", calls, stops)
	}
}

func seedPrimaryV2RunStreamForResumePolicyTest(t *testing.T, server *Server, sessionID string) string {
	t.Helper()
	state, err := server.runStreams.newRun(sessionID)
	if err != nil {
		t.Fatalf("new run stream: %v", err)
	}
	if state == nil || strings.TrimSpace(state.runID) == "" {
		t.Fatalf("new run stream returned empty state: %+v", state)
	}
	server.runStreams.publishRuntimeEvent(state.runID, runruntime.StreamEvent{Type: runruntime.StreamEventAssistantDelta, SessionID: sessionID, RunID: state.runID, Delta: "existing frame"})
	return state.runID
}

func readPrimaryV2RunStreamWebsocketFrame(t *testing.T, server *Server, sessionID string, payload map[string]any) runStreamControlMessage {
	t.Helper()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.apiMux().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer primary.Close()

	wsURL := "ws" + strings.TrimPrefix(primary.URL, "http") + "/v2/sessions/" + sessionID + "/run/stream"
	client, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			t.Fatalf("dial primary v2 websocket: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial primary v2 websocket: %v", err)
	}
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, ok := payload["session_id"]; !ok {
		payload["session_id"] = sessionID
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := client.WriteMessage(gorillaws.TextMessage, rawPayload); err != nil {
		t.Fatalf("write websocket payload: %v", err)
	}
	_, rawFrame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket frame: %v", err)
	}
	var frame runStreamControlMessage
	if err := json.Unmarshal(rawFrame, &frame); err != nil {
		t.Fatalf("decode websocket frame %s: %v", string(rawFrame), err)
	}
	return frame
}

type primaryV2RunStreamPolicyRunner struct {
	mu    sync.Mutex
	calls int
	stops int
}

func (r *primaryV2RunStreamPolicyRunner) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return runruntime.RunResult{}, errors.New("unexpected RunTurn call")
}

func (r *primaryV2RunStreamPolicyRunner) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return runruntime.RunResult{}, errors.New("unexpected RunTurnStreaming call")
}

func (r *primaryV2RunStreamPolicyRunner) StopSessionRun(string, string, string) error {
	r.mu.Lock()
	r.stops++
	r.mu.Unlock()
	return nil
}

func (r *primaryV2RunStreamPolicyRunner) snapshot() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.stops
}

func (r *primaryV2RunStreamPolicyRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "{}", nil
}

func (r *primaryV2RunStreamPolicyRunner) ListAgentToolDefinitions() []tool.Definition { return nil }

func (r *primaryV2RunStreamPolicyRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (r *primaryV2RunStreamPolicyRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *primaryV2RunStreamPolicyRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

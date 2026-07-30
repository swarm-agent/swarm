package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3BlockingDurableProgressWriter struct {
	base    sessionV3DurableProgressWriter
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu             sync.Mutex
	assistantCalls []sessionV3AssistantProgress
	phaseCalls     []string
}

func newSessionsV3BlockingDurableProgressWriter(base sessionV3DurableProgressWriter) *sessionsV3BlockingDurableProgressWriter {
	return &sessionsV3BlockingDurableProgressWriter{base: base, entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *sessionsV3BlockingDurableProgressWriter) blockFirst() {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	w.mu.Lock()
	w.phaseCalls = append(w.phaseCalls, eventType)
	w.mu.Unlock()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordRunPhase(job, phase, eventType)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	w.mu.Lock()
	w.assistantCalls = append(w.assistantCalls, progress)
	w.mu.Unlock()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordRunProgress(job, progress, deltaIndex)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, deltaMode string, summary string) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordReasoningEvent(job, eventType, step, eventIndex, reasoningKey, delta, deltaMode, summary)
}

func (w *sessionsV3BlockingDurableProgressWriter) RecordProviderToolConstructionEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, event provideriface.StreamEvent) (sessionruntime.SessionMutationResult, error) {
	w.blockFirst()
	if w.base == nil {
		return sessionruntime.SessionMutationResult{}, nil
	}
	return w.base.RecordProviderToolConstructionEvent(job, eventType, step, eventIndex, event)
}

func TestV3ProviderToolConstructionPersistsIdentityMetadataAndTimestamps(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-tool-construction-create", "provider tool construction", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	liveSub := server.v3LiveHub.subscribe()
	defer server.v3LiveHub.unsubscribe(liveSub)
	server.v3LiveHub.replaceSessions(liveSub, testPrincipal().AccountScopeID, []string{created.ID})
	runner := installSessionsV3TestProvider(server, "done")
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		index := 4
		startedAt := time.Now().UnixMilli() - 5
		base := provideriface.StreamEvent{ToolCallID: "call-construction", ToolCallIndex: &index, ToolName: "edit", ProviderID: "codex", Model: "gpt-test", StartedAtUnixMs: startedAt, Metadata: map[string]any{"provider_item_id": "fc-construction"}}
		onEvent(provideriface.StreamEvent{
			Type:         provideriface.StreamEventReasoningSummaryDelta,
			Delta:        "inspect the relevant files",
			DeltaMode:    provideriface.StreamEventDeltaModeReplace,
			ReasoningKey: "summary-1",
		})
		started := base
		started.Type = provideriface.StreamEventToolCallStarted
		started.Status = "started"
		started.RecordedAtUnixMs = startedAt
		onEvent(started)
		delta := base
		delta.Type = provideriface.StreamEventToolCallArgumentsDelta
		delta.Status = "building"
		delta.RecordedAtUnixMs = startedAt + 1
		delta.ArgumentsDelta = `{"path":"file.txt"}`
		onEvent(delta)
		completed := base
		completed.Type = provideriface.StreamEventToolCallCompleted
		completed.Status = "completed"
		completed.RecordedAtUnixMs = startedAt + 2
		completed.Arguments = `{"path":"file.txt"}`
		onEvent(completed)
		return provideriface.Response{Text: "done", StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-construction-message", "construct")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var construction []pebblestore.V3SessionEvent
	var causalOrder []string
	for _, event := range events {
		if strings.HasPrefix(event.EventType, "session.reasoning.") || strings.HasPrefix(event.EventType, "session.provider_tool_call.") {
			causalOrder = append(causalOrder, event.EventType)
		}
		if strings.HasPrefix(event.EventType, "session.provider_tool_call.") {
			construction = append(construction, event)
		}
	}
	wantCausalOrder := []string{
		"session.reasoning.started",
		"session.reasoning.delta",
		"session.reasoning.completed",
		"session.provider_tool_call.started",
		"session.provider_tool_call.arguments.delta",
		"session.provider_tool_call.completed",
	}
	if fmt.Sprint(causalOrder) != fmt.Sprint(wantCausalOrder) {
		t.Fatalf("reasoning/tool causal order = %v, want %v", causalOrder, wantCausalOrder)
	}
	if len(construction) != 3 {
		t.Fatalf("construction events = %d, want 3: %+v", len(construction), construction)
	}
	replay, err := sessionSvc.ReplaySessionEvents(created.ID, 0, 50)
	if err != nil {
		t.Fatalf("replay session events: %v", err)
	}
	var replayedConstruction int
	for _, event := range replay.Events {
		if !strings.HasPrefix(event.EventType, "session.provider_tool_call.") {
			continue
		}
		if replayedConstruction >= len(construction) || event.Seq != construction[replayedConstruction].Seq {
			t.Fatalf("replayed construction ordering mismatch at %d: %+v", replayedConstruction, event)
		}
		replayedConstruction++
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode replayed construction event: %v", err)
		}
		if payload["call_id"] != "call-construction" || payload["provider"] != "codex" || payload["started_at"] == nil {
			t.Fatalf("replayed construction payload = %#v", payload)
		}
	}
	if replayedConstruction != 3 {
		t.Fatalf("replayed construction events = %d, want 3", replayedConstruction)
	}
	wantTypes := []string{"session.provider_tool_call.started", "session.provider_tool_call.arguments.delta", "session.provider_tool_call.completed"}
	for i, event := range construction {
		if event.EventType != wantTypes[i] {
			t.Fatalf("event[%d].type = %q, want %q", i, event.EventType, wantTypes[i])
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode event[%d]: %v", i, err)
		}
		if payload["call_id"] != "call-construction" || payload["tool_name"] != "edit" || payload["provider"] != "codex" || payload["model"] != "gpt-test" {
			t.Fatalf("event[%d] identity = %#v", i, payload)
		}
		outputIndex, ok := payload["output_index"].(float64)
		if !ok {
			if typed, typedOK := payload["output_index"].(int); typedOK {
				outputIndex = float64(typed)
			}
		}
		if payload["started_at"] == nil || payload["recorded_at"] == nil || outputIndex != 4 {
			t.Fatalf("event[%d] timing/index = %#v", i, payload)
		}
		metadata, _ := payload["metadata"].(map[string]any)
		if metadata["provider_item_id"] != "fc-construction" {
			t.Fatalf("event[%d] metadata = %#v", i, metadata)
		}
	}
	for _, event := range events {
		if event.EventType == "session.tool.started" || event.EventType == "session.tool.completed" {
			t.Fatalf("construction-only provider stream emitted execution event %q", event.EventType)
		}
	}
	patches := liveSub.drain(16, 64<<10)
	var constructionPatches int
	for _, patch := range patches {
		if patch.StreamKind == "provider_tool_call" {
			constructionPatches++
			if err := ValidateV3RealtimeLiveMessage(NewV3RealtimeLiveMessage(patch)); err != nil {
				t.Fatalf("validate construction live patch: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(patch.Text), &payload); err != nil {
				t.Fatalf("decode construction live patch: %v", err)
			}
			if payload["call_id"] != "call-construction" || payload["provider"] != "codex" {
				t.Fatalf("construction live payload = %#v", payload)
			}
		}
	}
	if constructionPatches != 0 {
		t.Fatalf("provider construction bypassed durable reasoning order with %d live patches: %+v", constructionPatches, patches)
	}
	outbox, err := sessionSvc.ListRealtimeOutboxForSessionAfterEndpoint(created.ID, 0, 50)
	if err != nil {
		t.Fatalf("list realtime outbox: %v", err)
	}
	var durableRealtimeConstruction int
	for _, record := range outbox {
		if strings.HasPrefix(record.Event.EventType, "session.provider_tool_call.") {
			durableRealtimeConstruction++
		}
	}
	if durableRealtimeConstruction != 3 {
		t.Fatalf("durable realtime construction records = %d, want 3", durableRealtimeConstruction)
	}
}

func TestV3ReasoningUpdatesUseExplicitProviderDeltaMode(t *testing.T) {
	if got := sessionV3ApplyReasoningUpdate("The model is thin", "king fast", provideriface.StreamEventDeltaModeAppend); got != "The model is thinking fast" {
		t.Fatalf("append update = %q, want exact concatenation", got)
	}
	if got := sessionV3ApplyReasoningUpdate("The model is thinking fast", "Corrected summary", provideriface.StreamEventDeltaModeReplace); got != "Corrected summary" {
		t.Fatalf("replace update = %q, want exact snapshot", got)
	}
	if got := sessionV3ApplyReasoningUpdate("thinking through files", "thinking", ""); got != "thinking through files" {
		t.Fatalf("legacy update = %q, want contained snapshot preserved", got)
	}
}

func TestV3AssistantCallbackPublishesAllDeltasWhileDurableWriterIsBlocked(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "callback-live-blocked-create", "callback live blocked", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	liveSub := server.v3LiveHub.subscribe()
	defer server.v3LiveHub.unsubscribe(liveSub)
	server.v3LiveHub.replaceSessions(liveSub, testPrincipal().AccountScopeID, []string{created.ID})

	const deltaCount = 10000
	expected := strings.Repeat("x", deltaCount)
	allDeltasEmitted := make(chan struct{})
	runner := installSessionsV3TestProvider(server, expected)
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		for i := 0; i < deltaCount; i++ {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "x"})
		}
		close(allDeltasEmitted)
		return provideriface.Response{Text: expected, StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	writer := newSessionsV3BlockingDurableProgressWriter(sessionV3ExecutorDurableProgressWriter{exec: exec})
	exec.durableProgressWriterForTest = writer
	server.v3SessionExecutor = exec

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "callback-live-blocked-message", "stream fast")
	select {
	case <-writer.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("durable writer was not entered")
	}
	select {
	case <-allDeltasEmitted:
	case <-time.After(2 * time.Second):
		t.Fatalf("provider callbacks did not finish while durable writer was blocked")
	}

	patches := liveSub.drain(16, 64<<10)
	if len(patches) != 1 {
		t.Fatalf("live patches before release = %d, want one coalesced patch", len(patches))
	}
	patch := patches[0]
	if patch.Text != expected || patch.LiveSeqStart != 1 || patch.LiveSeqEnd != deltaCount || patch.OffsetStart != 0 || patch.OffsetEnd != deltaCount {
		t.Fatalf("live patch before release = seq %d-%d offsets %d-%d text len %d", patch.LiveSeqStart, patch.LiveSeqEnd, patch.OffsetStart, patch.OffsetEnd, len(patch.Text))
	}
	close(writer.release)
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if messages[1].Role != "assistant" || messages[1].Content != expected {
		t.Fatalf("canonical assistant = %+v", messages[1])
	}
}

func TestV3CommittedAssistantMetadataMatchesFinalStream(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := installSessionsV3TestProvider(server, "héllo 🌍")
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "hé"})
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "llo 🌍"})
		return provideriface.Response{Text: "héllo 🌍", StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "final-stream-metadata-create", "stream metadata", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "final-stream-metadata-message", "metadata")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	metadata := messages[1].Metadata
	runID, _ := metadata["run_id"].(string)
	if runID == "" {
		t.Fatalf("assistant metadata missing run_id: %+v", metadata)
	}
	if metadata["stream_id"] != sessionV3AssistantLiveStreamID(runID, 1) || metadata["stream_step"] != float64(1) && metadata["stream_step"] != 1 || metadata["stream_offset_end"] != float64(len([]byte("héllo 🌍"))) && metadata["stream_offset_end"] != len([]byte("héllo 🌍")) {
		t.Fatalf("assistant metadata = %+v", metadata)
	}
}

func TestV3ProviderStreamPreservesCanonicalBytesExactly(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	canonical := "  héllo 🌍  "
	runner := installSessionsV3TestProvider(server, strings.TrimSpace(canonical))
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "  hé"})
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "llo 🌍  "})
		return provideriface.Response{Text: strings.TrimSpace(canonical), StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "canonical-bytes-create", "canonical bytes", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "canonical-bytes-message", "preserve")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if messages[1].Content != canonical {
		t.Fatalf("canonical content = %q, want %q", messages[1].Content, canonical)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var reconstructed string
	for _, event := range events {
		if event.EventType != "session.assistant.delta" {
			continue
		}
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode delta: %v", err)
		}
		reconstructed += payload.Delta
	}
	if reconstructed != canonical {
		t.Fatalf("durable deltas reconstruct %q, want %q", reconstructed, canonical)
	}
}

func TestV3ProviderResponseWithoutDeltasUsesOneSyntheticRange(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	text := "synthetic héllo"
	runner := installSessionsV3TestProvider(server, text)
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: text, StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "synthetic-range-create", "synthetic range", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "synthetic-range-message", "synthetic")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltas []struct {
		LiveSeqStart uint64 `json:"live_seq_start"`
		LiveSeqEnd   uint64 `json:"live_seq_end"`
		OffsetStart  uint64 `json:"offset_start"`
		OffsetEnd    uint64 `json:"offset_end"`
		Delta        string `json:"delta"`
	}
	for _, event := range events {
		if event.EventType != "session.assistant.delta" {
			continue
		}
		var payload struct {
			LiveSeqStart uint64 `json:"live_seq_start"`
			LiveSeqEnd   uint64 `json:"live_seq_end"`
			OffsetStart  uint64 `json:"offset_start"`
			OffsetEnd    uint64 `json:"offset_end"`
			Delta        string `json:"delta"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode delta: %v", err)
		}
		deltas = append(deltas, payload)
	}
	if len(deltas) != 1 || deltas[0].LiveSeqStart != 1 || deltas[0].LiveSeqEnd != 1 || deltas[0].OffsetStart != 0 || deltas[0].OffsetEnd != uint64(len([]byte(text))) || deltas[0].Delta != text {
		t.Fatalf("synthetic deltas = %+v", deltas)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) < 2 || messages[1].Content != text {
		t.Fatalf("canonical synthetic assistant messages = %+v", messages)
	}
}

func TestV3ProviderStreamDiagnosticsDoNotPersistPerDelta(t *testing.T) {
	t.Setenv("SWARM_V3_DIAGNOSTICS", "1")
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := installSessionsV3TestProvider(server, strings.Repeat("x", 100))
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		for i := 0; i < 100; i++ {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "x"})
		}
		return provideriface.Response{Text: strings.Repeat("x", 100), StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "diagnostics-no-stream-create", "diagnostics", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "diagnostics-no-stream-message", "diagnostics")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 1000)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var providerStreamDiagnostics int
	var assistantDeltas int
	for _, event := range events {
		if event.EventType == "session.diagnostic.provider.stream" {
			providerStreamDiagnostics++
		}
		if event.EventType == "session.assistant.delta" {
			assistantDeltas++
		}
	}
	if providerStreamDiagnostics != 0 {
		t.Fatalf("provider stream diagnostics = %d, want zero", providerStreamDiagnostics)
	}
	if assistantDeltas == 0 || assistantDeltas >= 100 {
		t.Fatalf("assistant durable mutations = %d, want coalesced nonzero count below per-delta", assistantDeltas)
	}
}

func TestV3ProviderHotCallbackHasNoDurableCalls(t *testing.T) {
	bodyBytes, err := os.ReadFile("sessions_v3_provider_stream.go")
	if err != nil {
		t.Fatalf("read provider stream source: %v", err)
	}
	body := string(bodyBytes)
	handleBody, err := sourceFuncBodyByBraceDepth(body, "func (s *sessionV3ProviderStreamState) Handle")
	if err != nil {
		t.Fatalf("extract Handle body: %v", err)
	}
	for _, forbidden := range []string{"ApplySessionMutation", "applySessionV3PrimaryMutation", "appendSessionV3Diagnostic", "recordSessionV3Diagnostic", "recordRunPhase", "recordRunProgress", "recordReasoningEvent", "recordProviderToolEvent", "recordProviderToolConstructionEvent(", "WriteText", "Sleep", ".Flush(", ".CloseAndFlush("} {
		if strings.Contains(handleBody, forbidden) {
			t.Fatalf("Handle contains forbidden durable/blocking symbol %q", forbidden)
		}
	}
	for _, required := range []string{"TryAppendAssistant", "TryRecordProviderToolConstruction", "completeActiveReasoningLocked"} {
		if !strings.Contains(handleBody, required) {
			t.Fatalf("Handle missing required symbol %q", required)
		}
	}
}

func sourceFuncBodyByBraceDepth(source, signature string) (string, error) {
	start := strings.Index(source, signature)
	if start < 0 {
		return "", fmt.Errorf("signature %q not found", signature)
	}
	brace := strings.Index(source[start:], "{")
	if brace < 0 {
		return "", errors.New("opening brace not found")
	}
	pos := start + brace
	depth := 0
	for i := pos; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[pos+1 : i], nil
			}
		}
	}
	return "", errors.New("closing brace not found")
}

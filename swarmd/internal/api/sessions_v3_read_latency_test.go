package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV3ProviderManagedRead2000LineLatencyAndPayload(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	fixturePath := filepath.Join(workspace, "read-2000-lines.txt")
	if err := os.WriteFile(fixturePath, []byte(sessionsV3ReadLatencyFixture(2000)), 0o644); err != nil {
		t.Fatalf("write 2,000-line fixture: %v", err)
	}
	const readArgs = `{"path":"read-2000-lines.txt","max_lines":2000}`
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-2000", Name: "read", Arguments: readArgs}}},
		{Text: "captured 2,000-line read continuation", StopReason: "stop", Usage: provideriface.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2, Source: "test_usage"}},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.runner = runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), nil, server.agents, nil, nil)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name:                "swarm",
		Mode:                agentruntime.ModePrimary,
		Provider:            "test-provider",
		Model:               "test-model",
		Thinking:            "medium",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true), BashPrefixes: []string{"*"}}}},
		Enabled:             pebblestore.BoolPtr(true),
		Prompt:              "Swarm prompt",
	}); err != nil {
		t.Fatalf("upsert read-enabled agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "read-2000-create", "read 2000 lines", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	exec = newSessionV3Executor(server)
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: "run-read-2000", EpochID: "epoch-00000000000000000001"}
	if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentPendingExecutor, "", "session.assistant.queued"); err != nil {
		t.Fatalf("record pending test run intent: %v", err)
	}
	if _, err := exec.recordRunStatus(job, sessionruntime.RunIntentRunning, "", "session.assistant.started"); err != nil {
		t.Fatalf("record running test run intent: %v", err)
	}
	resolved, err := exec.resolveSessionV3Runtime(job)
	if err != nil {
		t.Fatalf("resolve V3 runtime: %v", err)
	}
	baseReq, err := exec.sessionV3ProviderBaseRequest(job, resolved, []map[string]any{{"role": "user", "content": "read exactly 2,000 lines once"}})
	if err != nil {
		t.Fatalf("build V3 provider request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sink := newSessionV3DurableProgressSinkWithWriter(exec, job, func() {}, sessionsV3ReadLatencyNoopProgressWriter{})
	searchBefore := pebblestore.SnapshotV3PlanAcceptanceTelemetry()
	totalStart := time.Now()
	loopResult, err := exec.runProviderToolLoop(ctx, job, resolved, runner, baseReq, sink)
	if err != nil {
		runner.mu.Lock()
		requestCount := len(runner.requests)
		runner.mu.Unlock()
		messages, _ := sessionSvc.ListSessionMessages(created.ID, 0, 10)
		events, _ := sessionSvc.ListSessionEvents(created.ID, 0, 50)
		eventTypes := make([]string, 0, len(events))
		for _, event := range events {
			eventTypes = append(eventTypes, event.EventType)
		}
		t.Fatalf("run provider tool loop: %v (provider_requests=%d messages=%d events=%v)", err, requestCount, len(messages), eventTypes)
	}
	if err := sink.CloseAndFlush(ctx); err != nil {
		t.Fatalf("close durable progress sink: %v", err)
	}
	totalElapsed := time.Since(totalStart)
	if loopResult.FinalContent != "captured 2,000-line read continuation" {
		t.Fatalf("provider continuation result = %q", loopResult.FinalContent)
	}

	runner.mu.Lock()
	requests := append([]provideriface.Request(nil), runner.requests...)
	runner.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want initial request plus one continuation", len(requests))
	}
	if got := sessionsV3ReadLatencyFunctionCallCount(requests[0].Input); got != 0 {
		t.Fatalf("initial provider input already contained %d function calls", got)
	}
	if len(runner.responses[0].FunctionCalls) != 1 || runner.responses[0].FunctionCalls[0].Arguments != readArgs {
		t.Fatalf("fake provider read calls = %+v, want exactly one max_lines=2000 call", runner.responses[0].FunctionCalls)
	}

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var toolMessage pebblestore.MessageSnapshot
	for _, message := range messages {
		if message.Role == "tool" {
			if toolMessage.ID != "" {
				t.Fatalf("found more than one durable tool message: %+v", messages)
			}
			toolMessage = message
		}
	}
	if toolMessage.ID == "" {
		t.Fatalf("durable read tool message missing: %+v", messages)
	}
	record, ok := sessionsV3DecodeProviderToolResultRecord(toolMessage.Content)
	if !ok {
		t.Fatalf("decode durable provider tool record: %s", toolMessage.Content)
	}
	var rawRead struct {
		Bytes         int `json:"bytes"`
		Count         int `json:"count"`
		LineStart     int `json:"line_start"`
		NextLineStart int `json:"next_line_start"`
		MaxLines      int `json:"max_lines"`
		Lines         []struct {
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"lines"`
	}
	if err := json.Unmarshal([]byte(record.Output), &rawRead); err != nil {
		t.Fatalf("decode raw read output: %v", err)
	}
	if rawRead.Count != 2000 || len(rawRead.Lines) != 2000 || rawRead.MaxLines != 2000 || rawRead.LineStart != 1 || rawRead.NextLineStart != 2001 {
		t.Fatalf("raw read range = count:%d lines:%d max:%d start:%d next:%d, want exactly lines 1-2000", rawRead.Count, len(rawRead.Lines), rawRead.MaxLines, rawRead.LineStart, rawRead.NextLineStart)
	}
	if rawRead.Lines[0].Line != 1 || rawRead.Lines[len(rawRead.Lines)-1].Line != 2000 {
		t.Fatalf("raw read retained line range = %d-%d, want 1-2000", rawRead.Lines[0].Line, rawRead.Lines[len(rawRead.Lines)-1].Line)
	}

	modelOutput, ok := sessionsV3ReadLatencyFunctionOutput(requests[1].Input, "call-read-2000")
	if !ok {
		t.Fatalf("continuation request missing function_call_output: %+v", requests[1].Input)
	}
	var modelPayload struct {
		PathID                   string `json:"path_id"`
		Truncated                bool   `json:"truncated_for_model"`
		OriginalBytes            int    `json:"original_bytes"`
		ReturnedContentBytes     int    `json:"returned_content_bytes"`
		RetainedContentBytes     int    `json:"retained_content_bytes"`
		LineStart                int    `json:"line_start"`
		ReturnedLineCount        int    `json:"returned_line_count"`
		NextLineStart            int    `json:"next_line_start"`
		EOF                      bool   `json:"eof"`
		RetainedLineStart        int    `json:"retained_line_start"`
		RetainedLineEnd          int    `json:"retained_line_end"`
		RetainedLineCount        int    `json:"retained_line_count"`
		AllReturnedLinesRetained bool   `json:"all_returned_lines_retained"`
		BinarySuppressed         bool   `json:"binary_suppressed"`
		PromptInjectionTag       string `json:"prompt_injection_tag"`
		Lines                    []struct {
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"lines"`
	}
	if err := json.Unmarshal([]byte(modelOutput), &modelPayload); err != nil {
		t.Fatalf("decode model-facing read payload: %v\npayload=%s", err, modelOutput)
	}
	if modelPayload.PathID != "run.tool-output.read.v1" || !modelPayload.Truncated || modelPayload.OriginalBytes != len(record.Output) || modelPayload.ReturnedContentBytes != rawRead.Bytes {
		t.Fatalf("model retention contract = path:%q truncated:%t original:%d returned_content:%d, raw_output=%d raw_content=%d", modelPayload.PathID, modelPayload.Truncated, modelPayload.OriginalBytes, modelPayload.ReturnedContentBytes, len(record.Output), rawRead.Bytes)
	}
	if modelPayload.AllReturnedLinesRetained || modelPayload.LineStart != 1 || modelPayload.ReturnedLineCount != 2000 || modelPayload.NextLineStart != 2001 || !modelPayload.EOF {
		t.Fatalf("model pagination contract = all_retained:%t start:%d returned:%d next:%d eof:%t", modelPayload.AllReturnedLinesRetained, modelPayload.LineStart, modelPayload.ReturnedLineCount, modelPayload.NextLineStart, modelPayload.EOF)
	}
	if modelPayload.RetainedLineStart != 1 || modelPayload.RetainedLineEnd <= 0 || modelPayload.RetainedLineEnd >= 2000 || modelPayload.RetainedLineCount != len(modelPayload.Lines) || modelPayload.RetainedContentBytes <= 0 || modelPayload.RetainedContentBytes > 1200 {
		t.Fatalf("model retained lines = %d-%d count:%d/%d bytes:%d", modelPayload.RetainedLineStart, modelPayload.RetainedLineEnd, modelPayload.RetainedLineCount, len(modelPayload.Lines), modelPayload.RetainedContentBytes)
	}
	if modelPayload.BinarySuppressed || modelPayload.PromptInjectionTag != "tool_output_untrusted" || len(modelOutput) >= 8*1024 {
		t.Fatalf("model safety/bound = binary:%t tag:%q bytes:%d", modelPayload.BinarySuppressed, modelPayload.PromptInjectionTag, len(modelOutput))
	}
	previewStart, previewEnd := modelPayload.RetainedLineStart, modelPayload.RetainedLineEnd

	serializeStart := time.Now()
	continuationJSON, err := json.Marshal(requests[1].Input)
	if err != nil {
		t.Fatalf("serialize captured continuation: %v", err)
	}
	continuationSerializeElapsed := time.Since(serializeStart)
	modelPrepareStart := time.Now()
	preparedAgain := runruntime.PrepareToolOutputForModel(tool.Call{CallID: record.CallID, Name: record.ToolName, Arguments: record.Arguments}, tool.Result{CallID: record.CallID, Name: record.ToolName, Output: record.Output, DurationMS: record.DurationMS})
	modelPrepareElapsed := time.Since(modelPrepareStart)
	if preparedAgain != modelOutput {
		t.Fatalf("reprepared model output differs from captured continuation output")
	}
	modelPrepareAllocs := testing.AllocsPerRun(5, func() {
		_ = runruntime.PrepareToolOutputForModel(tool.Call{CallID: record.CallID, Name: record.ToolName, Arguments: record.Arguments}, tool.Result{CallID: record.CallID, Name: record.ToolName, Output: record.Output, DurationMS: record.DurationMS})
	})
	continuationAllocs := testing.AllocsPerRun(5, func() {
		_, _ = json.Marshal(requests[1].Input)
	})

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 50)
	if err != nil {
		t.Fatalf("list V3 events: %v", err)
	}
	var terminalEventBytes int
	for _, event := range events {
		if event.EventType == "session.tool.completed" {
			terminalEventBytes = len(event.Payload)
		}
	}
	if terminalEventBytes == 0 {
		t.Fatalf("session.tool.completed event missing from %d V3 events", len(events))
	}

	searchDelta := pebblestore.DeltaV3PlanAcceptanceTelemetry(pebblestore.SnapshotV3PlanAcceptanceTelemetry(), searchBefore)
	if searchDelta.SearchPostingsSet <= 0 || searchDelta.SearchPostingsSet > 200 {
		t.Fatalf("bounded read search postings = %d, want 1-200", searchDelta.SearchPostingsSet)
	}
	t.Logf("read-2000 optimized: total_local_pipeline=%s tool_runtime_ms=%d model_prepare=%s raw_content_bytes=%d structured_output_bytes=%d durable_record_bytes=%d terminal_event_bytes=%d model_output_bytes=%d continuation_request_bytes=%d continuation_serialize=%s raw_lines=1-2000 model_retained_lines=%d-%d search_postings=%d model_prepare_allocs=%.0f continuation_allocs=%.0f",
		totalElapsed, record.DurationMS, modelPrepareElapsed, rawRead.Bytes, len(record.Output), len(toolMessage.Content), terminalEventBytes, len(modelOutput), len(continuationJSON), continuationSerializeElapsed, previewStart, previewEnd, searchDelta.SearchPostingsSet, modelPrepareAllocs, continuationAllocs)
}

type sessionsV3ReadLatencyNoopProgressWriter struct{}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordRunPhase(sessionV3ExecutorJob, RunPhase, string) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordRunProgress(sessionV3ExecutorJob, sessionV3AssistantProgress, int) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordReasoningEvent(sessionV3ExecutorJob, string, int, int, string, string, string, string) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func (sessionsV3ReadLatencyNoopProgressWriter) RecordProviderToolConstructionEvent(sessionV3ExecutorJob, string, int, int, provideriface.StreamEvent) (sessionruntime.SessionMutationResult, error) {
	return sessionruntime.SessionMutationResult{}, nil
}

func sessionsV3ReadLatencyFixture(lineCount int) string {
	var b strings.Builder
	for line := 1; line <= lineCount; line++ {
		fmt.Fprintf(&b, "line-%04d %s\n", line, "representative-payload")
	}
	return b.String()
}

func sessionsV3ReadLatencyFunctionCallCount(input []map[string]any) int {
	count := 0
	for _, item := range input {
		if strings.EqualFold(strings.TrimSpace(sessionsV3MapString(item, "type")), "function_call") {
			count++
		}
	}
	return count
}

func sessionsV3ReadLatencyFunctionOutput(input []map[string]any, callID string) (string, bool) {
	for _, item := range input {
		if strings.EqualFold(strings.TrimSpace(sessionsV3MapString(item, "type")), "function_call_output") && strings.TrimSpace(sessionsV3MapString(item, "call_id")) == callID {
			return sessionsV3MapString(item, "output"), true
		}
	}
	return "", false
}

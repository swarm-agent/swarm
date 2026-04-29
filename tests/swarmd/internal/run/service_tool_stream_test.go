package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type scriptedRunner struct {
	id           string
	responses    []provideriface.Response
	streamEvents [][]provideriface.StreamEvent
	index        atomic.Int64
	reqMu        sync.Mutex
	requests     []provideriface.Request
}

type failingRunner struct {
	id  string
	err error
}

func (r *scriptedRunner) ID() string {
	return r.id
}

func (r *scriptedRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *scriptedRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.reqMu.Lock()
	r.requests = append(r.requests, req)
	r.reqMu.Unlock()

	next := int(r.index.Add(1) - 1)
	if next < 0 {
		next = 0
	}
	if onEvent != nil && next >= 0 && next < len(r.streamEvents) {
		for _, event := range r.streamEvents[next] {
			onEvent(event)
		}
	}
	if next >= len(r.responses) {
		if len(r.responses) == 0 {
			return provideriface.Response{Text: "ok"}, nil
		}
		return r.responses[len(r.responses)-1], nil
	}
	return r.responses[next], nil
}

func (r *scriptedRunner) requestsSnapshot() []provideriface.Request {
	r.reqMu.Lock()
	defer r.reqMu.Unlock()
	out := make([]provideriface.Request, len(r.requests))
	copy(out, r.requests)
	return out
}

func providersRunnerRequests(reg *registry.Registry) []provideriface.Request {
	if reg == nil {
		return nil
	}
	runner, ok := reg.GetRunner("codex")
	if !ok {
		return nil
	}
	scripted, ok := runner.(*scriptedRunner)
	if !ok {
		return nil
	}
	return scripted.requestsSnapshot()
}

func (r *failingRunner) ID() string {
	return r.id
}

func (r *failingRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *failingRunner) CreateResponseStreaming(_ context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.err == nil {
		return provideriface.Response{}, errors.New("runner failure")
	}
	return provideriface.Response{}, r.err
}

func TestExecuteControlPlaneToolAskUserReturnsStructuredResponse(t *testing.T) {
	svc := &Service{}
	handled, result, err := svc.executeControlPlaneTool(context.Background(), "session-1", sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "ask_1",
		Name:      "ask-user",
		Arguments: `{"question":"Choose model","options":["A","B"]}`,
	}, "Use option A", nil)
	if err != nil {
		t.Fatalf("execute control-plane ask-user: %v", err)
	}
	if !handled {
		t.Fatalf("expected ask-user to be handled by control-plane path")
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatalf("expected ask-user output payload")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode ask-user payload: %v", err)
	}
	if got, _ := payload["status"].(string); got != "answered" {
		t.Fatalf("expected status=answered, got %q", got)
	}
	if got, _ := payload["path_id"].(string); got != "tool.ask-user.v3" {
		t.Fatalf("expected ask-user path_id, got %q", got)
	}
}

func TestExecuteControlPlaneToolAskUserParsesStructuredAnswers(t *testing.T) {
	svc := &Service{}
	feedback := `{"answers":{"q_mode":"auto","q_apply":"yes"}}`
	handled, result, err := svc.executeControlPlaneTool(context.Background(), "session-1", sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "ask_2",
		Name:      "ask-user",
		Arguments: `{"title":"Need input","questions":[{"id":"q_mode","question":"Mode?","options":["auto","yolo"]},{"id":"q_apply","question":"Apply?","options":["yes","no"]}]}`,
	}, feedback, nil)
	if err != nil {
		t.Fatalf("execute control-plane ask-user: %v", err)
	}
	if !handled {
		t.Fatalf("expected ask-user to be handled by control-plane path")
	}

	var payload struct {
		Status  string            `json:"status"`
		Answer  string            `json:"answer"`
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode ask-user payload: %v", err)
	}
	if payload.Status != "answered" {
		t.Fatalf("status = %q, want answered", payload.Status)
	}
	if strings.TrimSpace(payload.Answer) != "" {
		t.Fatalf("answer = %q, want empty for structured feedback", payload.Answer)
	}
	if got := payload.Answers["q_mode"]; got != "auto" {
		t.Fatalf("q_mode answer = %q, want auto", got)
	}
	if got := payload.Answers["q_apply"]; got != "yes" {
		t.Fatalf("q_apply answer = %q, want yes", got)
	}
}

func TestSummarizeToolOutputAskUserKeepsStructuredPayload(t *testing.T) {
	raw := `{
		"tool":"ask_user",
		"status":"answered",
		"summary":"ask-user response captured",
		"questions":[{"id":"q_mode","question":"Mode?"}],
		"answers":{"q_mode":"auto"}
	}`
	summary := summarizeToolOutput("ask-user", raw, 80, 1)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("summary should not be empty")
	}
	if summary == "ask-user response captured" {
		t.Fatalf("expected structured payload, got summary-only string")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(summary), &payload); err != nil {
		t.Fatalf("decode summarized ask-user payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "tool")); got != "ask_user" {
		t.Fatalf("tool = %q, want ask_user", got)
	}
	answers, ok := payload["answers"].(map[string]any)
	if !ok {
		t.Fatalf("answers missing from summarized payload: %#v", payload)
	}
	if got, _ := answers["q_mode"].(string); got != "auto" {
		t.Fatalf("q_mode answer = %q, want auto", got)
	}
}

func TestSummarizeToolOutputPermissionKeepsStructuredPayload(t *testing.T) {
	raw := `{
		"permission":{"approved":false,"status":"denied","reason":"Need user confirmation"},
		"tool":{"name":"exit_plan_mode","arguments":"{\"title\":\"Plan\"}"}
	}`
	summary := summarizeToolOutput("permission", raw, 80, 1)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("summary should not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(summary), &payload); err != nil {
		t.Fatalf("decode summarized permission payload: %v", err)
	}
	permissionObj, ok := payload["permission"].(map[string]any)
	if !ok {
		t.Fatalf("permission object missing from summarized payload: %#v", payload)
	}
	if got, _ := permissionObj["reason"].(string); got != "Need user confirmation" {
		t.Fatalf("permission reason = %q, want %q", got, "Need user confirmation")
	}
}

func TestSummarizeToolOutputExitPlanModeKeepsStructuredPayload(t *testing.T) {
	raw := `{
		"tool":"exit_plan_mode",
		"status":"approved",
		"title":"Implementation Plan",
		"plan_id":"plan_123",
		"approval_state":"approved",
		"requested_modifications":[],
		"mode_changed":true,
		"target_mode":"auto",
		"user_message":"Ship it",
		"path_id":"tool.exit-plan-mode.v3",
		"summary":"plan saved, approved; mode switched to auto",
		"details_truncated":false
	}`
	summary := summarizeToolOutput("exit_plan_mode", raw, 80, 1)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("summary should not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(summary), &payload); err != nil {
		t.Fatalf("decode summarized exit_plan_mode payload: %v", err)
	}
	if got, _ := payload["user_message"].(string); got != "Ship it" {
		t.Fatalf("user_message = %q, want %q", got, "Ship it")
	}
	if got, _ := payload["plan_id"].(string); got != "plan_123" {
		t.Fatalf("plan_id = %q, want plan_123", got)
	}
	if got, _ := payload["target_mode"].(string); got != "auto" {
		t.Fatalf("target_mode = %q, want auto", got)
	}
	if got, _ := payload["status"].(string); got != "approved" {
		t.Fatalf("status = %q, want approved", got)
	}
}

func TestSummarizeToolOutputEditKeepsStructuredPayload(t *testing.T) {
	raw := `{
		"path":"/tmp/demo.txt",
		"replacements":1,
		"replace_all":false,
		"old_string_preview":"foo()",
		"new_string_preview":"bar()",
		"old_string_truncated":false,
		"new_string_truncated":false,
		"summary":"edit /tmp/demo.txt replacements=1"
	}`
	summary := summarizeToolOutput("edit", raw, 80, 1)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("summary should not be empty")
	}
	if strings.Contains(summary, "replacements=1") && !strings.Contains(summary, "old_string_preview") {
		t.Fatalf("expected structured payload, got summary-only string: %q", summary)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(summary), &payload); err != nil {
		t.Fatalf("decode summarized edit payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "old_string_preview")); got != "foo()" {
		t.Fatalf("old_string_preview = %q, want foo()", got)
	}
	if got := strings.TrimSpace(mapString(payload, "new_string_preview")); got != "bar()" {
		t.Fatalf("new_string_preview = %q, want bar()", got)
	}
}

func TestSummarizeToolOutputMultiEditKeepsStructuredPayload(t *testing.T) {
	raw := `{
		"path":"/tmp/demo.txt",
		"edit_count":2,
		"replacements":2,
		"edits":[
			{"index":1,"old_string_preview":"foo()","new_string_preview":"bar()","old_string_truncated":false,"new_string_truncated":false},
			{"index":2,"old_string_preview":"baz()","new_string_preview":"qux()","old_string_truncated":false,"new_string_truncated":false}
		],
		"summary":"edit /tmp/demo.txt (2 edits, 2 replacements)"
	}`
	summary := summarizeToolOutput("edit", raw, 80, 1)
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("summary should not be empty")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(summary), &payload); err != nil {
		t.Fatalf("decode summarized edit payload: %v", err)
	}
	if got := mapInt(payload, "edit_count"); got != 2 {
		t.Fatalf("edit_count = %d, want 2", got)
	}
	edits, ok := payload["edits"].([]any)
	if !ok || len(edits) != 2 {
		t.Fatalf("edits missing from summarized payload: %#v", payload)
	}
}

func TestFormatToolHistoryEditUsesCallArgumentsWhenResultIsSummaryOnly(t *testing.T) {
	call := tool.Call{
		CallID:    "call_edit_1",
		Name:      "edit",
		Arguments: `{"path":"/tmp/demo.txt","old_string":"alpha","new_string":"beta","replace_all":false}`,
	}
	result := tool.Result{
		CallID: "call_edit_1",
		Name:   "edit",
		Output: "edit /tmp/demo.txt replacements=1",
	}

	history := formatToolHistory(call, result)
	if !strings.Contains(history, "tool=edit") {
		t.Fatalf("history = %q, want edit tool record", history)
	}
	idx := strings.Index(history, " output=")
	if idx < 0 {
		t.Fatalf("history missing output payload: %q", history)
	}
	payloadRaw := strings.TrimSpace(history[idx+len(" output="):])
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatalf("decode history payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "old_string_preview")); got != "alpha" {
		t.Fatalf("old_string_preview = %q, want alpha", got)
	}
	if got := strings.TrimSpace(mapString(payload, "new_string_preview")); got != "beta" {
		t.Fatalf("new_string_preview = %q, want beta", got)
	}
}

func TestFormatToolCompletedOutputEditUsesCallArgumentsWhenResultIsSummaryOnly(t *testing.T) {
	call := tool.Call{
		CallID:    "call_edit_1",
		Name:      "edit",
		Arguments: `{"path":"/tmp/demo.txt","old_string":"alpha","new_string":"beta","replace_all":false}`,
	}
	result := tool.Result{
		CallID: "call_edit_1",
		Name:   "edit",
		Output: "edit /tmp/demo.txt replacements=1",
	}

	output := formatToolCompletedOutput(call, result)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode completed output payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "old_string_preview")); got != "alpha" {
		t.Fatalf("old_string_preview = %q, want alpha", got)
	}
	if got := strings.TrimSpace(mapString(payload, "new_string_preview")); got != "beta" {
		t.Fatalf("new_string_preview = %q, want beta", got)
	}
}

func TestFormatToolCompletedOutputPreservesRawStructuredPayloadForNonEditTools(t *testing.T) {
	call := tool.Call{
		CallID:    "call_grep_1",
		Name:      "grep",
		Arguments: `{"pattern":"TODO","path":"."}`,
	}
	raw := `{"path":".","pattern":"TODO","count":0,"matches":[],"truncated":false}`
	result := tool.Result{
		CallID: "call_grep_1",
		Name:   "grep",
		Output: raw,
	}

	output := formatToolCompletedOutput(call, result)
	if strings.TrimSpace(output) != "grep pattern=\"TODO\" in . 0 matches" {
		t.Fatalf("completed output should summarize grep payload, got %q", output)
	}
}

func TestSummarizeToolOutputReadPrefersLineRangeOverSummaryBytes(t *testing.T) {
	raw := `{
		"path":"/tmp/demo.txt",
		"line_start":20,
		"count":12,
		"bytes":84,
		"truncated":false,
		"summary":"read /tmp/demo.txt (84 bytes)"
	}`

	summary := summarizeToolOutput("read", raw, 80, 1)
	if summary != "read /tmp/demo.txt (lines 20-31)" {
		t.Fatalf("summary = %q, want line-range label", summary)
	}
	if strings.Contains(summary, "bytes") {
		t.Fatalf("summary should prefer line-range label over bytes summary: %q", summary)
	}
}

func TestSummarizeToolOutputGrepZeroMatchesUsesUnparenthesizedLabel(t *testing.T) {
	raw := `{
		"path":".",
		"pattern":"TODO",
		"count":0,
		"truncated":false
	}`
	summary := summarizeToolOutput("grep", raw, 120, 1)
	if strings.Contains(summary, "(0 matches") {
		t.Fatalf("summary should avoid parenthesized zero-match label: %q", summary)
	}
	if !strings.Contains(summary, "0 matches") {
		t.Fatalf("summary missing zero-match label: %q", summary)
	}
}

func TestSummarizeToolOutputReadZeroLinesUsesUnparenthesizedLabel(t *testing.T) {
	raw := `{
		"path":"/tmp/demo.txt",
		"line_start":20,
		"count":0,
		"truncated":false
	}`
	summary := summarizeToolOutput("read", raw, 120, 1)
	if strings.Contains(summary, "(0 lines") {
		t.Fatalf("summary should avoid parenthesized zero-line label: %q", summary)
	}
	if !strings.Contains(summary, "0 lines") {
		t.Fatalf("summary missing zero-line label: %q", summary)
	}
}

func TestBuildInputSkipsToolDBDebugSystemMessages(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{Role: "system", Content: `[tool-db-debug] {"kind":"tool.store","call_id":"call_1"}`},
		{Role: "system", Content: "normal system note"},
		{Role: "user", Content: "hello"},
	}

	input := buildInput(messages)
	if len(input) != 3 {
		t.Fatalf("len(input) = %d, want 3 when debug messages are no longer emitted", len(input))
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if strings.Contains(string(encoded), "tool-db-debug") {
		t.Fatalf("debug system message should now pass through unchanged: %s", string(encoded))
	}
}

func TestBuildInputIncludesCompactionPlanTextFromMetadata(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{
			Role:    "system",
			Content: "[context-compact] index=2 origin=manual\n\nCompacted recap:\n\nrecap text\n\nAttached plan: Execution Plan (plan_123)",
			Metadata: map[string]any{
				contextCompactionPlanTextMetadataKey: "Plan ID: plan_123\nTitle: Execution Plan\n# Plan\n\n- [ ] hidden body",
			},
		},
	}

	input := buildInput(messages)
	if len(input) != 1 {
		t.Fatalf("len(input) = %d, want 1", len(input))
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected content payload: %#v", input[0]["content"])
	}
	text, _ := content[0]["text"].(string)
	for _, expected := range []string{"Attached plan: Execution Plan (plan_123)", "Active session plan (still in effect after compaction):", "Plan ID: plan_123", "# Plan"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in provider input:\n%s", expected, text)
		}
	}
}

func TestExecuteControlPlaneToolExitPlanModeSwitchesSessionMode(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "exit-plan-mode.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	workspace := t.TempDir()
	session, _, err := sessionSvc.CreateSession("plan-mode", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := &Service{sessions: sessionSvc}
	handled, result, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "exit_1",
		Name:      "exit_plan_mode",
		Arguments: `{"title":"Implementation Plan","plan":"# Plan\n\n- [ ] task"}`,
	}, "Ship it", nil)
	if err != nil {
		t.Fatalf("execute control-plane exit_plan_mode: %v", err)
	}
	if !handled {
		t.Fatalf("expected exit_plan_mode to be handled by control-plane path")
	}

	mode, err := sessionSvc.GetMode(session.ID)
	if err != nil {
		t.Fatalf("get session mode: %v", err)
	}
	if mode != sessionruntime.ModeAuto {
		t.Fatalf("expected mode auto after exit_plan_mode, got %q", mode)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatalf("expected exit_plan_mode output payload")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode exit_plan_mode output: %v", err)
	}
	if got, _ := payload["user_message"].(string); got != "Ship it" {
		t.Fatalf("user_message = %q, want %q", got, "Ship it")
	}

	plans, activeID, err := sessionSvc.ListPlans(session.ID, 10)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected one saved plan, got %d", len(plans))
	}
	if strings.TrimSpace(activeID) == "" {
		t.Fatalf("expected active plan id after exit_plan_mode")
	}
}

func TestExecuteControlPlaneToolExitPlanModeNormalizesDefaultApprovalFeedback(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	session := mustCreateSessionWithMode(t, svc, store, "plan")

	call := tool.Call{
		CallID:    "call_exit_feedback_default",
		Name:      "exit_plan_mode",
		Arguments: `{"title":"Implementation Plan","plan":"# Plan\n1. Ship it"}`,
	}

	handled, result, err := svc.executeControlPlaneTool(ctx, session.ID, "plan", pebblestore.AgentProfile{}, 1, call, "approved by user", nil)
	if err != nil {
		t.Fatalf("execute control-plane exit_plan_mode: %v", err)
	}
	if !handled {
		t.Fatalf("expected exit_plan_mode to be handled by control-plane path")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode exit_plan_mode output: %v", err)
	}
	if got, _ := payload["user_message"].(string); got != "" {
		t.Fatalf("user_message = %q, want empty after default approval normalization", got)
	}
}

func TestExecuteControlPlaneToolExitPlanModeRejectsOutsidePlanModeWithPlanManageSnippet(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "exit-plan-mode-auto.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	workspace := t.TempDir()
	session, _, err := sessionSvc.CreateSession("auto-mode", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := &Service{sessions: sessionSvc}
	handled, result, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "swarm"}, 1, tool.Call{
		CallID:    "exit_auto_1",
		Name:      "exit_plan_mode",
		Arguments: `{"title":"Implementation Plan","plan":"# Plan\n\n- [ ] task"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("execute control-plane exit_plan_mode in auto: %v", err)
	}
	if !handled {
		t.Fatalf("expected exit_plan_mode to be handled by control-plane path")
	}

	mode, err := sessionSvc.GetMode(session.ID)
	if err != nil {
		t.Fatalf("get session mode: %v", err)
	}
	if mode != sessionruntime.ModePlan {
		t.Fatalf("expected stored session mode to remain plan default, got %q", mode)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode exit_plan_mode output: %v", err)
	}
	if got, _ := payload["status"].(string); got != "rejected" {
		t.Fatalf("status = %q, want rejected", got)
	}
	if got, _ := payload["approval_state"].(string); got != "not_in_plan_mode" {
		t.Fatalf("approval_state = %q, want not_in_plan_mode", got)
	}
	mods, ok := payload["requested_modifications"].([]any)
	if !ok || len(mods) != 1 {
		t.Fatalf("requested_modifications = %#v, want one entry", payload["requested_modifications"])
	}
	gotMod, _ := mods[0].(string)
	expectedMod := "Do not call exit_plan_mode from auto. To update the active plan instead, use plan_manage with exactly: {\"action\":\"save\",\"plan\":\"# Plan\\n1. ...\"}. Only call exit_plan_mode when leaving plan mode."
	if gotMod != expectedMod {
		t.Fatalf("requested_modifications[0] = %q, want %q", gotMod, expectedMod)
	}
	if got, _ := payload["summary"].(string); got != "plan saved but exit_plan_mode rejected: session not in plan mode; use plan_manage save to update the active plan instead" {
		t.Fatalf("summary = %q", got)
	}
}

func TestExecuteControlPlaneToolPlanManageLifecycle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "plan-manage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("plan-manage", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := &Service{sessions: sessionSvc}

	handled, created, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, 1, tool.Call{
		CallID:    "plan_new",
		Name:      "plan_manage",
		Arguments: `{"action":"new","title":"Implementation Plan"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("plan_manage new: %v", err)
	}
	if !handled {
		t.Fatalf("expected plan_manage to be handled by control-plane path")
	}
	var createdPayload map[string]any
	if err := json.Unmarshal([]byte(created.Output), &createdPayload); err != nil {
		t.Fatalf("decode created output: %v", err)
	}
	planObj, ok := createdPayload["plan"].(map[string]any)
	if !ok {
		t.Fatalf("expected created payload plan object")
	}
	createdID, _ := planObj["id"].(string)
	if strings.TrimSpace(createdID) == "" {
		t.Fatalf("expected created plan id")
	}

	_, listed, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, 1, tool.Call{
		CallID:    "plan_list",
		Name:      "plan_manage",
		Arguments: `{"action":"list"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("plan_manage list: %v", err)
	}
	var listPayload map[string]any
	if err := json.Unmarshal([]byte(listed.Output), &listPayload); err != nil {
		t.Fatalf("decode list output: %v", err)
	}
	if got, _ := listPayload["active_plan_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("expected active plan id in list payload")
	}

	handled, updated, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModeAuto, pebblestore.AgentProfile{Name: "swarm"}, 1, tool.Call{
		CallID:    "plan_save_active",
		Name:      "plan_manage",
		Arguments: `{"action":"save","plan":"# Updated Plan\n\n- [x] first\n- [ ] second"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("plan_manage save active: %v", err)
	}
	if !handled {
		t.Fatalf("expected save to be handled")
	}
	var updatedPayload map[string]any
	if err := json.Unmarshal([]byte(updated.Output), &updatedPayload); err != nil {
		t.Fatalf("decode updated output: %v", err)
	}
	updatedPlan, ok := updatedPayload["plan"].(map[string]any)
	if !ok {
		t.Fatalf("expected updated payload plan object")
	}
	if got, _ := updatedPlan["id"].(string); got != createdID {
		t.Fatalf("updated plan id = %q, want %q", got, createdID)
	}
	if got, _ := updatedPlan["plan"].(string); !strings.Contains(got, "# Updated Plan") {
		t.Fatalf("updated plan body missing changes: %q", got)
	}
	mode, err := sessionSvc.GetMode(session.ID)
	if err != nil {
		t.Fatalf("get mode after save: %v", err)
	}
	if mode != sessionruntime.ModeAuto {
		t.Fatalf("session mode after plan_manage save = %q, want auto", mode)
	}

	handled, activated, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, 1, tool.Call{
		CallID:    "plan_set_active",
		Name:      "plan_manage",
		Arguments: `{"action":"set-active","plan_id":"` + createdID + `"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("plan_manage set-active: %v", err)
	}
	if !handled {
		t.Fatalf("expected set-active to be handled")
	}
	if strings.TrimSpace(activated.Output) == "" {
		t.Fatalf("expected set-active output")
	}
}

func TestExecuteControlPlaneToolTaskDelegatesToSubagent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-control-plane.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}
	if _, _, _, err := sessionSvc.AppendMessage(parentSession.ID, "user", "check the repo layout", map[string]any{"source": "run_turn"}); err != nil {
		t.Fatalf("append parent user message: %v", err)
	}
	if _, _, _, err := sessionSvc.AppendMessage(parentSession.ID, "assistant", "I think explorer should inspect the main runtime files.", nil); err != nil {
		t.Fatalf("append parent assistant message: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{Text: "Repository scan complete. Key files identified with line references."},
		},
	})

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, 6)
	handled, result, err := svc.executeControlPlaneTool(context.Background(), parentSession.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "task_1",
		Name:      "task",
		Arguments: `{"description":"Inspect repo","prompt":"Find the key files and summarize.","subagent_type":"explorer"}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("execute task control-plane tool: %v", err)
	}
	if !handled {
		t.Fatalf("expected task to be handled by control-plane path")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode task payload: %v", err)
	}
	if got := strings.TrimSpace(mapString(payload, "tool")); got != "task" {
		t.Fatalf("tool = %q, want task", got)
	}
	if got := strings.TrimSpace(mapString(payload, "status")); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	if got := strings.TrimSpace(mapString(payload, "subagent")); got != "explorer" {
		t.Fatalf("subagent = %q, want explorer", got)
	}
	if got := strings.TrimSpace(mapString(payload, "session_id")); got == "" {
		t.Fatalf("expected delegated session_id in task payload")
	} else {
		childSession, ok, err := sessionSvc.GetSession(got)
		if err != nil {
			t.Fatalf("get delegated child session: %v", err)
		}
		if !ok {
			t.Fatalf("delegated child session %q not found", got)
		}
		if gotParent := strings.TrimSpace(mapString(childSession.Metadata, "parent_session_id")); gotParent != strings.TrimSpace(parentSession.ID) {
			t.Fatalf("child parent_session_id = %q, want %q", gotParent, strings.TrimSpace(parentSession.ID))
		}
		if gotKind := strings.TrimSpace(mapString(childSession.Metadata, "lineage_kind")); gotKind != "delegated_subagent" {
			t.Fatalf("child lineage_kind = %q, want delegated_subagent", gotKind)
		}
		if gotLabel := strings.TrimSpace(mapString(childSession.Metadata, "lineage_label")); gotLabel != "@explorer" {
			t.Fatalf("child lineage_label = %q, want @explorer", gotLabel)
		}
		if gotSource := strings.TrimSpace(mapString(childSession.Metadata, "launch_source")); gotSource != "task" {
			t.Fatalf("child launch_source = %q, want task", gotSource)
		}
	}
	requests := providersRunnerRequests(providers)
	if len(requests) == 0 {
		t.Fatalf("expected provider request for delegated child")
	}
	inputJSON, err := json.Marshal(requests[0].Input)
	if err != nil {
		t.Fatalf("marshal provider input: %v", err)
	}
	inputText := string(inputJSON)
	for _, expected := range []string{"Recent visible parent transcript:", "- user: check the repo layout", "- assistant: I think explorer should inspect the main runtime files."} {
		if !strings.Contains(inputText, expected) {
			t.Fatalf("expected %q in delegated provider input: %s", expected, inputText)
		}
	}
}

func TestExecuteControlPlaneToolTaskEmitsStreamingDeltas(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-streaming.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", workspace, "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "bash_1", Name: "bash", Arguments: `{"command":"echo delegated-permission","timeout_ms":1000}`},
					{CallID: "read_1", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
				},
			},
			{Text: "Done."},
		},
	})

	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), permSvc, agentSvc, nil, 6)
	seenDeltas := make([]StreamEvent, 0, 8)
	var deltaMu sync.Mutex
	emit := func(event StreamEvent) {
		if strings.TrimSpace(event.Type) == StreamEventToolDelta {
			deltaMu.Lock()
			seenDeltas = append(seenDeltas, event)
			deltaMu.Unlock()
		}
	}

	runCtx, runCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer runCancel()
	type taskRunOutcome struct {
		handled bool
		err     error
	}
	outcomeCh := make(chan taskRunOutcome, 1)
	go func() {
		handled, _, err := svc.executeControlPlaneTool(runCtx, parentSession.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
			CallID:    "task_stream_1",
			Name:      "task",
			Arguments: `{"description":"Read file","prompt":"Read README and summarize.","subagent_type":"explorer"}`,
		}, "", emit)
		outcomeCh <- taskRunOutcome{handled: handled, err: err}
	}()

	var pending []pebblestore.PermissionRecord
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, err = permSvc.ListPending(parentSession.ID, 10)
		if err == nil && len(pending) > 0 {
			break
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("list pending permissions in parent session: %v", err)
			}
			t.Fatalf("expected delegated task to create parent-scoped pending permission for child bash")
		}
		time.Sleep(10 * time.Millisecond)
	}
	record := pending[0]
	if got := strings.TrimSpace(record.ToolName); got != "bash" {
		t.Fatalf("pending tool = %q, want bash", got)
	}
	if got := strings.TrimSpace(record.SessionID); got != strings.TrimSpace(parentSession.ID) {
		t.Fatalf("pending session id = %q, want parent session %q", got, parentSession.ID)
	}
	if _, err := permSvc.Resolve(parentSession.ID, record.ID, permission.DecisionApprove, "allow delegated bash"); err != nil {
		t.Fatalf("resolve delegated pending permission via parent session: %v", err)
	}

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("execute task control-plane tool: %v", outcome.err)
	}
	if !outcome.handled {
		t.Fatalf("expected task to be handled by control-plane path")
	}

	deltaMu.Lock()
	captured := append([]StreamEvent(nil), seenDeltas...)
	deltaMu.Unlock()
	if len(captured) == 0 {
		t.Fatalf("expected task tool.delta events during delegated run")
	}

	foundStructured := false
	for _, event := range captured {
		if strings.TrimSpace(event.ToolName) != "task" {
			continue
		}
		if strings.Contains(event.Output, `"tool":"task"`) && strings.Contains(event.Output, `"phase"`) && strings.Contains(event.Output, `"launches"`) {
			foundStructured = true
			if strings.Contains(event.Output, `"report_excerpt"`) {
				t.Fatalf("task delta leaked report excerpt: %s", event.Output)
			}
			break
		}
	}
	if !foundStructured {
		t.Fatalf("expected structured task delta payload, got %#v", captured)
	}
}

func TestExecuteControlPlaneToolTaskPermissionsRemainParentScopedForChildSessionID(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-parent-permission-scope.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", workspace, "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "bash_1", Name: "bash", Arguments: `{"command":"echo delegated-permission","timeout_ms":1000}`},
				},
			},
			{Text: "Done."},
		},
	})

	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), permSvc, agentSvc, nil, 6)

	runCtx, runCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer runCancel()
	type taskRunOutcome struct {
		handled bool
		err     error
	}
	outcomeCh := make(chan taskRunOutcome, 1)
	go func() {
		handled, _, err := svc.executeControlPlaneTool(runCtx, parentSession.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
			CallID:    "task_scope_1",
			Name:      "task",
			Arguments: `{"description":"Scope check","prompt":"Run one bash and summarize.","subagent_type":"explorer"}`,
		}, "", nil)
		outcomeCh <- taskRunOutcome{handled: handled, err: err}
	}()

	var (
		record pebblestore.PermissionRecord
		found  bool
	)
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, listErr := permSvc.ListPending(parentSession.ID, 10)
		if listErr == nil {
			for i := range pending {
				current := pending[i]
				if strings.TrimSpace(current.ToolName) != "bash" {
					continue
				}
				record = current
				found = true
				break
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected delegated pending bash permission in parent session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if strings.TrimSpace(record.SessionID) != strings.TrimSpace(parentSession.ID) {
		t.Fatalf("pending permission session=%q want parent=%q", record.SessionID, parentSession.ID)
	}
	if strings.TrimSpace(record.ID) == "" {
		t.Fatalf("pending permission id is empty")
	}

	if _, err := permSvc.Resolve(parentSession.ID, record.ID, permission.DecisionApprove, "allow delegated bash"); err != nil {
		t.Fatalf("resolve delegated pending permission via parent session: %v", err)
	}
	if _, err := permSvc.Resolve("session_child_fake", record.ID, permission.DecisionApprove, "child should not resolve parent-scoped permission"); err == nil {
		t.Fatalf("expected resolving parent-scoped permission via non-parent session to fail")
	}

	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("execute task control-plane tool: %v", outcome.err)
	}
	if !outcome.handled {
		t.Fatalf("expected task to be handled by control-plane path")
	}
}

func TestRunTurnStreamingEmitsToolDelta(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "tool-delta.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("stream", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{
						CallID:    "call_bash_1",
						Name:      "bash",
						Arguments: `{"command":"for i in 1 2 3; do echo chunk-$i; sleep 0.03; done","timeout_ms":4000}`,
					},
				},
			},
			{
				Text: "done",
			},
		},
	}
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, nil, nil, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var mu sync.Mutex
	eventsSeen := make([]StreamEvent, 0, 32)
	result, err := svc.RunTurnStreaming(ctx, session.ID, RunOptions{Prompt: "run tool"}, func(event StreamEvent) {
		mu.Lock()
		eventsSeen = append(eventsSeen, event)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("run turn streaming: %v", err)
	}
	if strings.TrimSpace(result.AssistantMessage.Content) == "" {
		t.Fatalf("expected assistant message after tool run")
	}

	mu.Lock()
	defer mu.Unlock()
	sawDelta := false
	sawCompleted := false
	for _, event := range eventsSeen {
		switch strings.ToLower(strings.TrimSpace(event.Type)) {
		case StreamEventToolDelta:
			if strings.TrimSpace(event.Output) != "" {
				sawDelta = true
			}
		case StreamEventToolCompleted:
			sawCompleted = true
		}
	}
	if !sawDelta {
		t.Fatalf("expected at least one tool.delta stream event")
	}
	if !sawCompleted {
		t.Fatalf("expected tool.completed stream event")
	}
}

func TestRunTurnStreamingEmitsUsageUpdatedForCodex(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "usage-updated.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("usage-stream", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{
						CallID:    "call_read_1",
						Name:      "read",
						Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`,
					},
				},
				Usage: provideriface.TokenUsage{
					Source:          "codex_api_usage",
					InputTokens:     100,
					OutputTokens:    20,
					CacheReadTokens: 30,
					TotalTokens:     120,
					APIUsageRaw: map[string]any{
						"input_tokens":  float64(100),
						"output_tokens": float64(20),
						"total_tokens":  float64(120),
					},
				},
			},
			{
				Text: "done",
				Usage: provideriface.TokenUsage{
					Source:          "codex_api_usage",
					InputTokens:     150,
					OutputTokens:    30,
					CacheReadTokens: 35,
					TotalTokens:     180,
					APIUsageRaw: map[string]any{
						"input_tokens":  float64(150),
						"output_tokens": float64(30),
						"total_tokens":  float64(180),
					},
				},
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, nil, nil, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	usageEvents := make([]StreamEvent, 0, 4)
	var mu sync.Mutex
	result, err := svc.RunTurnStreaming(ctx, session.ID, RunOptions{Prompt: "run with usage"}, func(event StreamEvent) {
		if strings.TrimSpace(event.Type) != StreamEventUsageUpdated {
			return
		}
		mu.Lock()
		usageEvents = append(usageEvents, event)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("run turn streaming: %v", err)
	}

	mu.Lock()
	captured := append([]StreamEvent(nil), usageEvents...)
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("usage.updated events = %d, want 2", len(captured))
	}
	last := captured[len(captured)-1]
	if last.TurnUsage == nil {
		t.Fatalf("last usage.updated missing turn_usage")
	}
	if last.UsageSummary == nil {
		t.Fatalf("last usage.updated missing usage_summary")
	}
	if last.TurnUsage.APIUsageRaw != nil {
		t.Fatalf("expected raw api usage payload to be omitted, got %#v", last.TurnUsage.APIUsageRaw)
	}
	if got := len(last.TurnUsage.APIUsageHistory); got != 0 {
		t.Fatalf("last usage history entries = %d, want 0", got)
	}
	if got := len(last.TurnUsage.APIUsagePaths); got != 0 {
		t.Fatalf("last usage path entries = %d, want 0", got)
	}
	if got := last.UsageSummary.TotalTokens; got != 180 {
		t.Fatalf("last usage summary total = %d, want 180", got)
	}
	if got := last.UsageSummary.CacheReadTokens; got != 35 {
		t.Fatalf("last usage summary cache_read_tokens = %d, want 35", got)
	}

	if result.UsageSummary == nil {
		t.Fatalf("run result missing usage summary")
	}
	if got := result.UsageSummary.TotalTokens; got != 180 {
		t.Fatalf("run result usage summary total = %d, want 180", got)
	}
}

func TestRunTurnStreamingEmitsUsageUpdatedForGoogle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "usage-updated-google.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("google", "gemini-2.5-pro", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("usage-stream-google", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "google",
		responses: []provideriface.Response{
			{
				Text: "done",
				Usage: provideriface.TokenUsage{
					Source:          "google_api_usage",
					InputTokens:     140,
					OutputTokens:    60,
					ThinkingTokens:  22,
					CacheReadTokens: 50,
					TotalTokens:     200,
				},
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	usageEvents := make([]StreamEvent, 0, 2)
	var mu sync.Mutex
	result, err := svc.RunTurnStreaming(ctx, session.ID, RunOptions{Prompt: "hello"}, func(event StreamEvent) {
		if strings.TrimSpace(event.Type) != StreamEventUsageUpdated {
			return
		}
		mu.Lock()
		usageEvents = append(usageEvents, event)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("run turn streaming: %v", err)
	}

	mu.Lock()
	captured := append([]StreamEvent(nil), usageEvents...)
	mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("usage.updated events = %d, want 1", len(captured))
	}
	last := captured[0]
	if last.UsageSummary == nil {
		t.Fatalf("usage.updated missing usage_summary")
	}
	if got := last.UsageSummary.Provider; got != "google" {
		t.Fatalf("usage.updated provider = %q, want google", got)
	}
	if got := last.UsageSummary.CacheReadTokens; got != 50 {
		t.Fatalf("usage.updated cache_read_tokens = %d, want 50", got)
	}
	if got := last.UsageSummary.ThinkingTokens; got != 22 {
		t.Fatalf("usage.updated thinking_tokens = %d, want 22", got)
	}
	if result.UsageSummary == nil {
		t.Fatalf("run result missing usage summary")
	}
	if got := result.UsageSummary.TotalTokens; got != 200 {
		t.Fatalf("run result total tokens = %d, want 200", got)
	}
}

func TestRunTurnStreamingGoogleUsesLatestAPISnapshotPerRun(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "usage-updated-google-latest-snapshot.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("google", "gemini-2.5-pro", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("usage-stream-google-latest", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstRaw := map[string]any{
		"promptTokenCount":        int64(100000),
		"candidatesTokenCount":    int64(30000),
		"thoughtsTokenCount":      int64(0),
		"totalTokenCount":         int64(130000),
		"cachedContentTokenCount": int64(25000),
	}
	secondRaw := map[string]any{
		"promptTokenCount":        int64(90000),
		"candidatesTokenCount":    int64(30000),
		"thoughtsTokenCount":      int64(0),
		"totalTokenCount":         int64(120000),
		"cachedContentTokenCount": int64(20000),
	}
	runner := &scriptedRunner{
		id: "google",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{
						CallID:    "google_call_1",
						Name:      "read",
						Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`,
					},
				},
				Usage: provideriface.TokenUsage{
					Source:           "google_api_usage",
					InputTokens:      100000,
					OutputTokens:     30000,
					TotalTokens:      130000,
					CacheReadTokens:  25000,
					APIUsageRaw:      firstRaw,
					APIUsageRawPath:  "usageMetadata",
					APIUsageHistory:  []map[string]any{firstRaw},
					APIUsagePaths:    []string{"usageMetadata"},
					CacheWriteTokens: 0,
				},
			},
			{
				Text: "done",
				Usage: provideriface.TokenUsage{
					Source:           "google_api_usage",
					InputTokens:      90000,
					OutputTokens:     30000,
					TotalTokens:      120000,
					CacheReadTokens:  20000,
					APIUsageRaw:      secondRaw,
					APIUsageRawPath:  "usageMetadata",
					APIUsageHistory:  []map[string]any{secondRaw},
					APIUsagePaths:    []string{"usageMetadata"},
					CacheWriteTokens: 0,
				},
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, nil, nil, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := svc.RunTurnStreaming(ctx, session.ID, RunOptions{Prompt: "run with usage"}, nil)
	if err != nil {
		t.Fatalf("run turn streaming: %v", err)
	}
	if result.TurnUsage == nil {
		t.Fatalf("run result missing turn usage")
	}
	if result.UsageSummary == nil {
		t.Fatalf("run result missing usage summary")
	}

	const wantLatestTotal = int64(120000)
	if got := result.TurnUsage.TotalTokens; got != wantLatestTotal {
		t.Fatalf("run result turn total tokens = %d, want %d", got, wantLatestTotal)
	}
	if got := result.UsageSummary.TotalTokens; got != wantLatestTotal {
		t.Fatalf("run result usage summary total tokens = %d, want %d", got, wantLatestTotal)
	}
	if result.TurnUsage.APIUsageRaw != nil {
		t.Fatalf("expected raw api usage payload to be omitted, got %#v", result.TurnUsage.APIUsageRaw)
	}
	if got := len(result.TurnUsage.APIUsageHistory); got != 0 {
		t.Fatalf("run result usage history entries = %d, want 0", got)
	}

	storedSummary, ok, err := sessionSvc.GetUsageSummary(session.ID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok {
		t.Fatalf("expected usage summary to be stored")
	}
	if got := storedSummary.TotalTokens; got != wantLatestTotal {
		t.Fatalf("stored usage summary total tokens = %d, want %d", got, wantLatestTotal)
	}

	expectedRemaining := int64(storedSummary.ContextWindow) - wantLatestTotal
	if expectedRemaining < 0 {
		expectedRemaining = 0
	}
	if got := storedSummary.RemainingTokens; got != expectedRemaining {
		t.Fatalf("stored remaining tokens = %d, want %d", got, expectedRemaining)
	}
}

func TestRunTurnReplaysFunctionCallsBeforeOutputsWithMetadata(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "tool-order-metadata.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("tool-order", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{
						CallID:    "call_1",
						Name:      "bash",
						Arguments: `{"command":"echo one"}`,
						Metadata: map[string]any{
							"google": map[string]any{
								"thought_signature": "sig-a",
							},
						},
					},
					{
						CallID:    "call_2",
						Name:      "read",
						Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`,
					},
				},
			},
			{Text: "done"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, nil, nil, 2)
	if _, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "run tools"}); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	requests := runner.requestsSnapshot()
	if len(requests) < 2 {
		t.Fatalf("runner requests = %d, want at least 2", len(requests))
	}
	secondInput := requests[1].Input
	if len(secondInput) == 0 {
		t.Fatalf("second request input is empty")
	}

	callIndexes := make([]int, 0, 2)
	outputIndexes := make([]int, 0, 2)
	functionCalls := make([]map[string]any, 0, 2)
	for idx, item := range secondInput {
		typeName, _ := item["type"].(string)
		switch strings.TrimSpace(typeName) {
		case "function_call":
			callIndexes = append(callIndexes, idx)
			functionCalls = append(functionCalls, item)
		case "function_call_output":
			outputIndexes = append(outputIndexes, idx)
		}
	}
	if len(callIndexes) != 2 || len(outputIndexes) != 2 {
		t.Fatalf("function_call indexes=%v function_call_output indexes=%v", callIndexes, outputIndexes)
	}
	if !(callIndexes[0] < callIndexes[1] && callIndexes[1] < outputIndexes[0] && outputIndexes[0] < outputIndexes[1]) {
		t.Fatalf("unexpected call/output ordering call=%v output=%v", callIndexes, outputIndexes)
	}

	firstMetadata, ok := functionCalls[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("first function call missing metadata: %#v", functionCalls[0])
	}
	googleMetadata, ok := firstMetadata["google"].(map[string]any)
	if !ok {
		t.Fatalf("first function call missing google metadata: %#v", firstMetadata)
	}
	if got, _ := googleMetadata["thought_signature"].(string); got != "sig-a" {
		t.Fatalf("thought_signature = %q, want sig-a", got)
	}
	if _, ok := functionCalls[1]["metadata"]; ok {
		t.Fatalf("second function call should not include metadata: %#v", functionCalls[1])
	}
}

func TestRunTurnStreamingEmitsReasoningLifecycleEvents(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "reasoning-stream-events.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("reasoning-stream", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{{
			Text:             "final answer",
			ReasoningSummary: "Inspecting current project state before applying changes.",
		}},
		streamEvents: [][]provideriface.StreamEvent{{
			{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: "Inspecting"},
			{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: "Inspecting current project state before applying changes."},
		}},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 1)
	seen := make([]string, 0, 8)
	_, err = svc.RunTurnStreaming(context.Background(), session.ID, RunOptions{Prompt: "hello"}, func(event StreamEvent) {
		seen = append(seen, strings.TrimSpace(event.Type))
	})
	if err != nil {
		t.Fatalf("run turn streaming: %v", err)
	}

	joined := strings.Join(seen, ",")
	for _, want := range []string{StreamEventReasoningStarted, StreamEventReasoningDelta, StreamEventReasoningSummary, StreamEventReasoningCompleted} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream events = %v, missing %s", seen, want)
		}
	}
}

func TestRunTurnPersistsReasoningSummaryMessage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "reasoning-summary.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("reasoning", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				Text:             "final answer",
				ReasoningSummary: "Inspecting current project state before applying changes.",
			},
		},
	})

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 1)
	result, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "hello"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if strings.TrimSpace(result.ReasoningSummary) == "" {
		t.Fatalf("expected reasoning summary in run result")
	}

	messages, err := sessionSvc.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	found := false
	for _, message := range messages {
		if strings.ToLower(strings.TrimSpace(message.Role)) != "reasoning" {
			continue
		}
		if strings.TrimSpace(message.Content) != strings.TrimSpace(result.ReasoningSummary) {
			t.Fatalf("reasoning message content = %q, want %q", message.Content, result.ReasoningSummary)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("expected persisted reasoning role message")
	}
}

func TestRunTurnContinuesAfterReasoningOnlyStep(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "reasoning-only-step.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("reasoning-only", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				ReasoningSummary: "Inspecting thinking animations in the UI.",
			},
			{
				Text: "I found the issue and prepared a fix.",
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 3)
	result, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "debug"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "I found the issue and prepared a fix." {
		t.Fatalf("assistant message = %q", got)
	}
	if strings.TrimSpace(result.AssistantMessage.Content) == "No assistant text output." {
		t.Fatalf("assistant message should not use placeholder when later step returns text")
	}
	if calls := runner.index.Load(); calls < 2 {
		t.Fatalf("runner calls = %d, want at least 2", calls)
	}
}

func TestRunTurnFailsOnTrulyEmptyProviderStep(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "empty-step.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("empty-step", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id:        "codex",
		responses: []provideriface.Response{{}},
	})

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 1)
	_, err = svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "hello"})
	if err == nil {
		t.Fatalf("expected error on empty provider step")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty step") {
		t.Fatalf("unexpected error: %v", err)
	}

	messages, err := sessionSvc.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			t.Fatalf("assistant placeholder should not be persisted on empty provider step")
		}
	}
}

func TestRunTurnCompletesWithoutRunOptionStepCap(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "max-steps-option.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("max-steps-option", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{ReasoningSummary: "step-1"},
			{ReasoningSummary: "step-2"},
			{Text: "completed loop"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 10)
	result, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "loop"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "completed loop" {
		t.Fatalf("assistant content = %q, want completed loop", got)
	}
	if calls := runner.index.Load(); calls != 3 {
		t.Fatalf("runner calls = %d, want 3", calls)
	}
}

func TestRunTurnIgnoresServiceMaxStepsDefault(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "max-steps-default.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("max-steps-default", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{ReasoningSummary: "step-1"},
			{ReasoningSummary: "step-2"},
			{Text: "completed loop"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 2)
	result, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "loop"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "completed loop" {
		t.Fatalf("assistant content = %q, want completed loop", got)
	}
	if calls := runner.index.Load(); calls != 3 {
		t.Fatalf("runner calls = %d, want 3", calls)
	}
}

func TestExecuteControlPlaneToolTaskIgnoresDelegatedMaxSteps(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-max-steps.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{ReasoningSummary: "child step 1"},
			{ReasoningSummary: "child step 2"},
			{Text: "delegated complete"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, agentSvc, nil, 10)
	handled, result, err := svc.executeControlPlaneTool(context.Background(), parentSession.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "task_max_steps",
		Name:      "task",
		Arguments: `{"description":"Enforce step cap","prompt":"Loop with reasoning only.","subagent_type":"explorer","max_steps":2}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("execute task control-plane tool: %v", err)
	}
	if !handled {
		t.Fatalf("expected task to be handled by control-plane path")
	}
	if calls := runner.index.Load(); calls != 3 {
		t.Fatalf("runner calls = %d, want 3", calls)
	}
	var payload map[string]any
	if decodeErr := json.Unmarshal([]byte(result.Output), &payload); decodeErr != nil {
		t.Fatalf("decode task payload: %v", decodeErr)
	}
	if got := strings.TrimSpace(mapString(payload, "status")); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
}

func TestRunTurnPersistsFailureMessageOnRunnerError(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "stream-error-log.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("stream-error", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&failingRunner{
		id:  "codex",
		err: errors.New("stream error: stream ID 59; INTERNAL_ERROR; received from peer"),
	})

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, nil, nil, 1)
	_, err = svc.RunTurnStreaming(context.Background(), session.ID, RunOptions{Prompt: "hello"}, nil)
	if err == nil {
		t.Fatalf("expected streaming run to fail")
	}

	messages, err := sessionSvc.ListMessages(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}

	foundFailure := false
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "assistant":
			t.Fatalf("assistant message should not be persisted on runner failure")
		case "system":
			content := strings.TrimSpace(message.Content)
			if strings.Contains(content, "Run failed ["+runFailurePathID+"]") && strings.Contains(content, "INTERNAL_ERROR") {
				foundFailure = true
			}
		}
	}
	if !foundFailure {
		t.Fatalf("expected persisted system failure message with path id %q", runFailurePathID)
	}
}

func TestLiveStreamRawOutputPreservesBashForOutputViewer(t *testing.T) {
	result := tool.Result{Output: strings.Repeat("x", 2048)}
	got := liveStreamRawOutput(tool.Call{Name: "bash"}, result)
	if got != result.Output {
		t.Fatalf("bash raw output should remain unchanged for /output viewer, got length %d want %d", len(got), len(result.Output))
	}

	nonBash := liveStreamRawOutput(tool.Call{Name: "grep"}, result)
	if nonBash != result.Output {
		t.Fatalf("non-bash raw output should remain unchanged")
	}
}

func TestExecuteControlPlaneToolTaskStreamsThreeParallelLaunchesMetaOnly(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "task-parallel-meta-streaming.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", workspace, "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(&scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "read_1", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_1", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_2", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_2", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_3", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_3", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
				},
			},
			{Text: "haiku done"},
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "read_4", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_4", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_5", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_5", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_6", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_6", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
				},
			},
			{Text: "sonnet done"},
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "read_7", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_7", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_8", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_8", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
					{CallID: "read_9", Name: "read", Arguments: `{"path":"README.md","line_start":1,"max_lines":5}`},
					{CallID: "list_9", Name: "list", Arguments: `{"path":".","mode":"tree","max_depth":1,"max_entries":20}`},
				},
			},
			{Text: "free verse done"},
		},
	})

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, 6)
	seenDeltas := make([]StreamEvent, 0, 64)
	var deltaMu sync.Mutex
	emit := func(event StreamEvent) {
		if strings.TrimSpace(event.Type) == StreamEventToolDelta {
			deltaMu.Lock()
			seenDeltas = append(seenDeltas, event)
			deltaMu.Unlock()
		}
	}

	handled, result, err := svc.executeControlPlaneTool(context.Background(), parentSession.ID, sessionruntime.ModeAuto, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "task_parallel_poem_1",
		Name:      "task",
		Arguments: `{"description":"Write poem variants","prompt":"Write a poem about the sea. Before writing, each subagent must alternate read and list three times in a row.","launches":[{"subagent_type":"parallel","meta_prompt":"haiku"},{"subagent_type":"parallel","meta_prompt":"sonnet"},{"subagent_type":"parallel","meta_prompt":"free verse"}]}`,
	}, "", emit)
	if err != nil {
		t.Fatalf("execute task control-plane tool: %v", err)
	}
	if !handled {
		t.Fatalf("expected task to be handled by control-plane path")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode task payload: %v", err)
	}
	launches, ok := payload["launches"].([]any)
	if !ok || len(launches) != 3 {
		t.Fatalf("expected 3 launches in final payload, got %#v", payload["launches"])
	}
	if got := jsonInt(payload, "success_count"); got != 3 {
		t.Fatalf("success_count = %d, want 3", got)
	}
	if got := jsonInt(payload, "tool_started"); got < 18 {
		t.Fatalf("tool_started = %d, want at least 18", got)
	}
	if strings.Contains(result.Output, `"report_excerpt"`) {
		t.Fatalf("final task payload leaked report excerpt: %s", result.Output)
	}
	if strings.Contains(result.Output, `"current_preview_kind":"assistant"`) || strings.Contains(result.Output, `"current_preview_text":"haiku done`) || strings.Contains(result.Output, `"current_preview_text":"sonnet done`) || strings.Contains(result.Output, `"current_preview_text":"free verse done`) {
		t.Fatalf("final task payload leaked delegated assistant text: %s", result.Output)
	}

	deltaMu.Lock()
	captured := append([]StreamEvent(nil), seenDeltas...)
	deltaMu.Unlock()
	if len(captured) == 0 {
		t.Fatalf("expected task tool.delta events during delegated run")
	}
	seenLaunches := map[int]bool{}
	for _, event := range captured {
		if strings.TrimSpace(event.ToolName) != "task" {
			continue
		}
		var deltaPayload map[string]any
		if err := json.Unmarshal([]byte(event.Output), &deltaPayload); err != nil {
			continue
		}
		rows, _ := deltaPayload["launches"].([]any)
		if len(rows) == 0 {
			continue
		}
		row, _ := rows[0].(map[string]any)
		idx := jsonInt(row, "launch_index")
		if idx > 0 {
			seenLaunches[idx] = true
		}
		if strings.Contains(event.Output, `"report_excerpt"`) {
			t.Fatalf("stream delta leaked report excerpt: %s", event.Output)
		}
		if strings.Contains(event.Output, `"current_preview_kind":"assistant"`) || strings.Contains(event.Output, `"current_preview_text":"haiku done`) || strings.Contains(event.Output, `"current_preview_text":"sonnet done`) || strings.Contains(event.Output, `"current_preview_text":"free verse done`) {
			t.Fatalf("stream delta leaked delegated assistant text: %s", event.Output)
		}
	}
	for _, idx := range []int{1, 2, 3} {
		if !seenLaunches[idx] {
			t.Fatalf("missing launch %d in streamed task deltas", idx)
		}
	}
}

func TestProviderManagedToolRequiresTurnRestartForExitPlanMode(t *testing.T) {
	if !providerManagedToolRequiresTurnRestart(tool.Call{Name: "exit_plan_mode"}, tool.Result{Output: `{"tool":"exit_plan_mode","mode_changed":true,"target_mode":"auto"}`}) {
		t.Fatalf("expected exit_plan_mode mode change to require restart")
	}
	if providerManagedToolRequiresTurnRestart(tool.Call{Name: "read"}, tool.Result{Output: `{"ok":true}`}) {
		t.Fatalf("unexpected restart for ordinary tool result")
	}
}

type modeSwitchingRunner struct {
	id        string
	sessionID string
	sessions  *sessionruntime.Service
	calls     atomic.Int64
	reqMu     sync.Mutex
	requests  []provideriface.Request
}

func (r *modeSwitchingRunner) ID() string { return r.id }

func (r *modeSwitchingRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *modeSwitchingRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.reqMu.Lock()
	r.requests = append(r.requests, req)
	r.reqMu.Unlock()
	call := int(r.calls.Add(1))
	if call == 1 {
		if _, _, err := r.sessions.SetMode(r.sessionID, sessionruntime.ModeAuto); err != nil {
			return provideriface.Response{}, err
		}
		return provideriface.Response{RestartTurn: true}, nil
	}
	return provideriface.Response{Text: "implementation resumed"}, nil
}

func (r *modeSwitchingRunner) requestsSnapshot() []provideriface.Request {
	r.reqMu.Lock()
	defer r.reqMu.Unlock()
	out := make([]provideriface.Request, len(r.requests))
	copy(out, r.requests)
	return out
}

func TestRunTurnRestartTurnRebuildsModeAwareInstructionsAfterExitPlanMode(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "copilot-restart-turn.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSession("copilot-mode-restart", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := sessionSvc.SetMode(session.ID, sessionruntime.ModePlan); err != nil {
		t.Fatalf("set plan mode: %v", err)
	}

	runner := &modeSwitchingRunner{
		id:        "codex",
		sessionID: session.ID,
		sessions:  sessionSvc,
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, nil, nil, 3)
	result, err := svc.RunTurn(context.Background(), session.ID, RunOptions{Prompt: "ship it"})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if strings.TrimSpace(result.AssistantMessage.Content) != "implementation resumed" {
		t.Fatalf("assistant content = %q, want implementation resumed", result.AssistantMessage.Content)
	}

	requests := runner.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[0].Instructions, "Current session mode: plan.") {
		t.Fatalf("first request instructions missing plan mode: %q", requests[0].Instructions)
	}
	if !strings.Contains(requests[1].Instructions, "Current session mode: auto.") {
		t.Fatalf("second request instructions missing auto mode: %q", requests[1].Instructions)
	}
}

func TestManageSkillApprovalArgumentsFallsBackToPreviewPayload(t *testing.T) {
	payload := map[string]any{
		"action":  "create",
		"confirm": true,
		"skill":   "e2e-manage-skill-demo",
		"change": map[string]any{
			"path":  "/tmp/e2e-manage-skill-demo/SKILL.md",
			"after": "---\nname: e2e-manage-skill-demo\ndescription: demo\n---\n",
		},
	}
	args := manageSkillApprovalArguments(payload)
	if got := mapString(args, "action"); got != "create" {
		t.Fatalf("action = %q, want create", got)
	}
	if got := mapString(args, "skill"); got != "e2e-manage-skill-demo" {
		t.Fatalf("skill = %q, want e2e-manage-skill-demo", got)
	}
	if got := mapString(args, "name"); got != "e2e-manage-skill-demo" {
		t.Fatalf("name = %q, want e2e-manage-skill-demo", got)
	}
	if got := mapString(args, "content"); !strings.Contains(got, "name: e2e-manage-skill-demo") {
		t.Fatalf("content mismatch: %q", got)
	}
	if got := mapString(args, "path"); got != "/tmp/e2e-manage-skill-demo/SKILL.md" {
		t.Fatalf("path = %q, want fallback path", got)
	}
	if confirm, ok := args["confirm"].(bool); !ok || !confirm {
		t.Fatalf("confirm = %v, want true", args["confirm"])
	}
}

func TestExecuteControlPlaneToolManageSkillFallsBackToDirectArguments(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-skill-control-plane.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	workspace := t.TempDir()
	session, _, err := sessionSvc.CreateSession("manage-skill-e2e", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := &Service{sessions: sessionSvc}
	content := "---\nname: e2e-manage-skill-demo\ndescription: Initial end-to-end skill validation.\n---\n\n# e2e-manage-skill-demo\n"
	handled, result, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "manage_skill_create",
		Name:      "manage-skill",
		Arguments: `{"action":"create","confirm":true,"skill":"e2e-manage-skill-demo","content":` + strconv.Quote(content) + `}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("execute manage-skill create: %v", err)
	}
	if !handled {
		t.Fatalf("expected manage-skill to be handled")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode manage-skill output: %v", err)
	}
	if got, _ := payload["status"].(string); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agents", "skills", "e2e-manage-skill-demo", "SKILL.md")); err != nil {
		t.Fatalf("expected created skill file: %v", err)
	}
}

func TestManageAgentApprovalArgumentsFallsBackToPreviewPayload(t *testing.T) {
	payload := map[string]any{
		"action":  "create",
		"confirm": true,
		"agent":   "e2e-manage-agent-demo",
		"change": map[string]any{
			"after": map[string]any{
				"name":              "e2e-manage-agent-demo",
				"mode":              "subagent",
				"description":       "demo",
				"prompt":            "Help with agent tasks.",
				"execution_setting": "read",
			},
		},
	}
	args := manageAgentApprovalArguments(payload)
	if got := mapString(args, "action"); got != "create" {
		t.Fatalf("action = %q, want create", got)
	}
	if got := mapString(args, "agent"); got != "e2e-manage-agent-demo" {
		t.Fatalf("agent = %q, want e2e-manage-agent-demo", got)
	}
	content, ok := args["content"].(map[string]any)
	if !ok {
		t.Fatalf("content type = %T, want map[string]any", args["content"])
	}
	if got := mapString(content, "name"); got != "e2e-manage-agent-demo" {
		t.Fatalf("content.name = %q, want e2e-manage-agent-demo", got)
	}
	if confirm, ok := args["confirm"].(bool); !ok || !confirm {
		t.Fatalf("confirm = %v, want true", args["confirm"])
	}
}

func TestExecuteControlPlaneToolManageAgentFallsBackToDirectArguments(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-agent-control-plane.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	agentStore := pebblestore.NewAgentStore(store)
	agentSvc := agentruntime.NewService(agentStore, events)
	workspace := t.TempDir()
	session, _, err := sessionSvc.CreateSession("manage-agent-e2e", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rt := tool.NewRuntime()
	rt.SetManageAgentService(agentSvc)
	svc := &Service{sessions: sessionSvc, tools: rt}
	handled, result, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "manage_agent_create",
		Name:      "manage-agent",
		Arguments: `{"action":"create","confirm":true,"agent":"e2e-manage-agent-demo","content":{"name":"e2e-manage-agent-demo","mode":"subagent","description":"Demo agent","prompt":"Help with repo review.","execution_setting":"read","tool_scope":{"allow_tools":["search","list","read"],"deny_tools":["write","edit"]}}}`,
	}, "", nil)
	if err != nil {
		t.Fatalf("execute manage-agent create: %v", err)
	}
	if !handled {
		t.Fatalf("expected manage-agent to be handled")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode manage-agent output: %v", err)
	}
	if got, _ := payload["status"].(string); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	profile, ok, err := agentSvc.GetProfile("e2e-manage-agent-demo")
	if err != nil {
		t.Fatalf("get created profile: %v", err)
	}
	if !ok {
		t.Fatalf("expected created agent profile")
	}
	if strings.TrimSpace(profile.Name) != "e2e-manage-agent-demo" {
		t.Fatalf("created profile name = %q, want e2e-manage-agent-demo", profile.Name)
	}
	if strings.TrimSpace(profile.Description) != "Demo agent" {
		t.Fatalf("description = %q, want Demo agent", profile.Description)
	}
	if strings.TrimSpace(profile.Prompt) != "Help with repo review." {
		t.Fatalf("prompt = %q, want Help with repo review.", profile.Prompt)
	}
	if profile.ToolScope == nil {
		t.Fatalf("expected tool scope to persist")
	}
	if got := strings.Join(profile.ToolScope.AllowTools, ","); got != "search,list,read" {
		t.Fatalf("allow_tools = %q, want search,list,read", got)
	}
	if got := strings.Join(profile.ToolScope.DenyTools, ","); got != "write,edit" {
		t.Fatalf("deny_tools = %q, want write,edit", got)
	}
}

func TestExecuteControlPlaneToolManageAgentUsesApprovedArgumentsFeedback(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-agent-feedback.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	agentStore := pebblestore.NewAgentStore(store)
	agentSvc := agentruntime.NewService(agentStore, events)
	workspace := t.TempDir()
	session, _, err := sessionSvc.CreateSession("manage-agent-feedback", workspace, "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rt := tool.NewRuntime()
	rt.SetManageAgentService(agentSvc)
	svc := &Service{sessions: sessionSvc, tools: rt}
	approvedArguments := `{"action":"create","confirm":true,"agent":"feedback-agent","content":{"name":"feedback-agent","mode":"subagent","description":"Feedback agent","prompt":"Use API directly.","execution_setting":"read"}}`
	handled, result, err := svc.executeControlPlaneTool(context.Background(), session.ID, sessionruntime.ModePlan, pebblestore.AgentProfile{}, 1, tool.Call{
		CallID:    "manage_agent_feedback",
		Name:      "manage-agent",
		Arguments: `{"action":"create","confirm":true}`,
	}, approvedArguments, nil)
	if err != nil {
		t.Fatalf("execute manage-agent create from approved arguments: %v", err)
	}
	if !handled {
		t.Fatalf("expected manage-agent to be handled")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("decode manage-agent output: %v", err)
	}
	if got, _ := payload["status"].(string); got != "ok" {
		t.Fatalf("status = %q, want ok", got)
	}
	profile, ok, err := agentSvc.GetProfile("feedback-agent")
	if err != nil {
		t.Fatalf("get created profile: %v", err)
	}
	if !ok {
		t.Fatalf("expected created agent profile")
	}
	if strings.TrimSpace(profile.Description) != "Feedback agent" {
		t.Fatalf("description = %q, want Feedback agent", profile.Description)
	}
}

func TestBuildCompactedContinuationInputIncludesActivePlan(t *testing.T) {
	plan := &pebblestore.SessionPlanSnapshot{
		ID:            "plan_123",
		Title:         "Execution Plan",
		Plan:          "# Plan\n\n- [ ] patch compaction",
		Status:        "approved",
		ApprovalState: "approved",
	}
	input := buildCompactedContinuationInput("ship it", "recap text", plan)
	if len(input) != 1 {
		t.Fatalf("input length = %d, want 1", len(input))
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected content payload: %#v", input[0]["content"])
	}
	text, _ := content[0]["text"].(string)
	for _, expected := range []string{
		"Active session plan (still in effect after compaction):",
		"Plan ID: plan_123",
		"Title: Execution Plan",
		"# Plan",
		"Compacted conversation recap:",
		"recap text",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in compacted continuation text:\n%s", expected, text)
		}
	}
}

func TestBuildCompactionCheckpointMessageAppendsAttachedPlanLabelOnly(t *testing.T) {
	text := buildCompactionCheckpointMessage("recap text", "manual", 2, "Execution Plan (plan_123)")
	if !strings.Contains(text, "Compacted recap:\n\nrecap text") {
		t.Fatalf("expected compact recap in checkpoint text:\n%s", text)
	}
	if !strings.Contains(text, "Attached plan: Execution Plan (plan_123)") {
		t.Fatalf("expected attached plan label in checkpoint text:\n%s", text)
	}
	if strings.Contains(text, "# Plan") {
		t.Fatalf("checkpoint text should not include full plan body:\n%s", text)
	}
}

func TestBuildManualCompactionAssistantTextAppendsAttachedPlanLabelOnly(t *testing.T) {
	text := buildManualCompactionAssistantText("recap text", 2, "Execution Plan (plan_123)")
	if !strings.Contains(text, "Attached plan: Execution Plan (plan_123)") {
		t.Fatalf("expected attached plan label in manual compact text:\n%s", text)
	}
	if strings.Contains(text, "# Plan") {
		t.Fatalf("manual compact text should not include full plan body:\n%s", text)
	}
}

func TestCompactedContextCheckpointMetadataIncludesFullPlanBody(t *testing.T) {
	plan := &pebblestore.SessionPlanSnapshot{
		ID:            "plan_123",
		Title:         "Execution Plan",
		Plan:          "# Plan\n\n- [ ] patch compaction",
		Status:        "approved",
		ApprovalState: "approved",
	}
	metadata := compactedContextCheckpointMetadata(plan)
	if metadata == nil {
		t.Fatalf("expected metadata")
	}
	label, _ := metadata[contextCompactionPlanLabelMetadataKey].(string)
	if label != "Execution Plan (plan_123)" {
		t.Fatalf("attached plan label = %q", label)
	}
	planText, _ := metadata[contextCompactionPlanTextMetadataKey].(string)
	for _, expected := range []string{"Plan ID: plan_123", "Title: Execution Plan", "# Plan"} {
		if !strings.Contains(planText, expected) {
			t.Fatalf("expected %q in attached plan text:\n%s", expected, planText)
		}
	}
}

func TestRunTurnWithOptions_TargetedSubagentPersistsParentAssistant(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "targeted-subagent-run.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	workspace := t.TempDir()
	parentSession, _, err := sessionSvc.CreateSession("parent", workspace, "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	updatedMetadata := cloneGenericMap(parentSession.Metadata)
	if updatedMetadata == nil {
		updatedMetadata = map[string]any{}
	}
	updatedMetadata["custom"] = "value"
	updatedMetadata["target_kind"] = "subagent"
	parentSession, _, err = sessionSvc.UpdateMetadata(parentSession.ID, updatedMetadata)
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if _, _, _, err := sessionSvc.AppendMessage(parentSession.ID, "user", "check the subagent flow", map[string]any{"source": "run_turn"}); err != nil {
		t.Fatalf("append parent user message: %v", err)
	}
	if _, _, _, err := sessionSvc.AppendMessage(parentSession.ID, "assistant", "I found the delegation prompt builder.", nil); err != nil {
		t.Fatalf("append parent assistant message: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	runner := &scriptedRunner{
		id:        "codex",
		responses: []provideriface.Response{{Text: "Repository scan complete. Key files identified with line references."}},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, 6)
	result, err := svc.RunTurnWithOptions(context.Background(), parentSession.ID, RunOptions{
		Prompt:     "inspect src",
		TargetKind: RunTargetKindSubagent,
		TargetName: "explorer",
		RunID:      "run-targeted-subagent",
	})
	if err != nil {
		t.Fatalf("run targeted subagent: %v", err)
	}
	if got := strings.TrimSpace(result.TargetKind); got != RunTargetKindSubagent {
		t.Fatalf("TargetKind = %q, want %q", got, RunTargetKindSubagent)
	}
	if got := strings.TrimSpace(result.TargetName); got != "explorer" {
		t.Fatalf("TargetName = %q, want explorer", got)
	}
	if got := strings.TrimSpace(result.UserMessage.Content); got != "inspect src" {
		t.Fatalf("UserMessage.Content = %q, want inspect src", got)
	}
	if len(result.ToolMessages) != 0 {
		t.Fatalf("tool message count = %d, want 0", len(result.ToolMessages))
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "Repository scan complete. Key files identified with line references." {
		t.Fatalf("AssistantMessage.Content = %q, want delegated report", got)
	}
	if got := strings.TrimSpace(mapString(result.AssistantMessage.Metadata, "source")); got != "targeted_subagent" {
		t.Fatalf("assistant metadata source = %q, want targeted_subagent", got)
	}
	if got := strings.TrimSpace(mapString(result.AssistantMessage.Metadata, "subagent")); got != "explorer" {
		t.Fatalf("assistant metadata subagent = %q, want explorer", got)
	}
	childID := strings.TrimSpace(mapString(result.AssistantMessage.Metadata, "child_session_id"))
	if childID == "" {
		t.Fatalf("expected delegated child session id")
	}
	childSession, ok, err := sessionSvc.GetSession(childID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q not found", childID)
	}
	if got := strings.TrimSpace(mapString(childSession.Metadata, "parent_session_id")); got != parentSession.ID {
		t.Fatalf("parent_session_id = %q, want %q", got, parentSession.ID)
	}
	if got := strings.TrimSpace(mapString(childSession.Metadata, "launch_source")); got != "targeted_subagent" {
		t.Fatalf("launch_source = %q, want targeted_subagent", got)
	}
	if got := strings.TrimSpace(mapString(childSession.Metadata, "targeted_subagent")); got != "explorer" {
		t.Fatalf("targeted_subagent = %q, want explorer", got)
	}

	requests := runner.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatalf("expected provider request for delegated child")
	}
	firstRequest := requests[0]
	if strings.TrimSpace(firstRequest.SessionID) != childID {
		t.Fatalf("provider session id = %q, want child session %q", firstRequest.SessionID, childID)
	}
	if got := strings.TrimSpace(result.AssistantMessage.ID); got == "" {
		t.Fatalf("expected persisted assistant message id")
	}
	parentMessages, err := sessionSvc.ListMessages(parentSession.ID, 0, 20)
	if err != nil {
		t.Fatalf("list parent messages: %v", err)
	}
	toolCount := 0
	assistantCount := 0
	for _, message := range parentMessages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "tool":
			toolCount++
		case "assistant":
			assistantCount++
		}
	}
	if toolCount != 0 {
		t.Fatalf("parent tool message count = %d, want 0", toolCount)
	}
	if assistantCount != 1 {
		t.Fatalf("parent assistant message count = %d, want 1", assistantCount)
	}
	inputJSON, err := json.Marshal(firstRequest.Input)
	if err != nil {
		t.Fatalf("marshal provider input: %v", err)
	}
	inputText := string(inputJSON)
	for _, expected := range []string{"Parent session context:", "\"custom\":\"value\"", "- requested subagent: @explorer", "Recent visible parent transcript:", "- user: check the subagent flow", "- assistant: I found the delegation prompt builder."} {
		if !strings.Contains(inputText, expected) {
			t.Fatalf("expected %q in delegated provider input: %s", expected, inputText)
		}
	}
}

func TestRunTurnStreamingWithOptions_TargetedSubagentEmitsTaskProgress(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "targeted-subagent-stream.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parentSession, _, err := sessionSvc.CreateSession("parent", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	runner := &scriptedRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				FunctionCalls: []provideriface.FunctionCall{
					{CallID: "call_list_1", Name: "list", Arguments: `{"path":"."}`},
				},
			},
			{Text: "Repository scan complete."},
		},
		streamEvents: [][]provideriface.StreamEvent{
			nil,
			{{Type: provideriface.StreamEventOutputTextDelta, Delta: "Repository scan complete."}},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	svc := NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, 6)
	var seen []StreamEvent
	result, err := svc.RunTurnStreamingWithOptions(context.Background(), parentSession.ID, RunOptions{
		Prompt:     "inspect src",
		TargetKind: RunTargetKindSubagent,
		TargetName: "explorer",
		RunID:      "run-targeted-subagent-stream",
	}, func(event StreamEvent) {
		seen = append(seen, event)
	})
	if err != nil {
		t.Fatalf("run targeted subagent stream: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "Repository scan complete." {
		t.Fatalf("AssistantMessage.Content = %q, want delegated report", got)
	}

	sawAssistantDelta := false
	sawTaskStart := false
	sawTaskDelta := false
	sawTaskComplete := false
	for _, event := range seen {
		switch strings.TrimSpace(event.Type) {
		case StreamEventToolStarted:
			if strings.TrimSpace(event.ToolName) == "task" {
				sawTaskStart = true
			}
		case StreamEventAssistantDelta:
			if strings.Contains(strings.TrimSpace(event.Delta), "Repository scan complete.") {
				sawAssistantDelta = true
			}
		case StreamEventToolDelta:
			if strings.TrimSpace(event.ToolName) == "task" {
				sawTaskDelta = true
			}
		case StreamEventToolCompleted:
			if strings.TrimSpace(event.ToolName) == "task" {
				sawTaskComplete = true
			}
		}
	}
	if !sawTaskStart {
		t.Fatalf("expected synthetic task tool.start event on parent stream")
	}
	if !sawAssistantDelta {
		t.Fatalf("expected child assistant delta on parent stream")
	}
	if !sawTaskDelta {
		t.Fatalf("expected synthetic task delta on targeted parent stream")
	}
	if !sawTaskComplete {
		t.Fatalf("expected synthetic task tool.completed event on targeted parent stream")
	}
}

package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	fakeCLIEnvMode     = "SWARM_CLAUDECODE_FAKE_CLI"
	fakeCLIEnvScenario = "SWARM_CLAUDECODE_FAKE_SCENARIO"
	fakeCLIEnvRecord   = "SWARM_CLAUDECODE_FAKE_RECORD"
)

// fakeRecord is what the fake CLI observed about its own invocation.
type fakeRecord struct {
	Args   []string `json:"args"`
	Env    []string `json:"env"`
	Dir    string   `json:"dir"`
	Prompt string   `json:"prompt"`
	MCPDir string   `json:"mcp_dir"`
}

// TestFakeClaudeCLIProcess is not a test: it is the fake `claude` executable,
// started through a wrapper script by the tests below. It speaks the documented
// stream-json protocol and calls the Swarm tool bridge over real HTTP.
func TestFakeClaudeCLIProcess(t *testing.T) {
	if os.Getenv(fakeCLIEnvMode) != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(runFakeClaude(args))
}

func runFakeClaude(args []string) int {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	emit := func(value any) {
		encoded, _ := json.Marshal(value)
		out.Write(encoded)
		out.WriteString("\n")
		out.Flush()
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintln(out, "2.1.296 (Claude Code)")
		return 0
	}
	if len(args) > 1 && args[0] == "auth" && args[1] == "status" {
		fmt.Fprintln(out, `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}`)
		return 0
	}
	scenario := os.Getenv(fakeCLIEnvScenario)
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)

	var input struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
	_ = json.Unmarshal(line, &input)
	record := fakeRecord{Args: args, Env: os.Environ()}
	record.Dir, _ = os.Getwd()
	if len(input.Message.Content) > 0 {
		record.Prompt = input.Message.Content[0].Text
	}
	mcpConfigPath := flagValue(args, "--mcp-config")
	record.MCPDir = filepath.Dir(mcpConfigPath)
	encoded, _ := json.Marshal(record)
	_ = os.WriteFile(os.Getenv(fakeCLIEnvRecord), encoded, 0o600)

	emit(map[string]any{"type": "system", "subtype": "init", "model": "claude-test-model", "session_id": "fake"})
	if scenario == "error" {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"model_not_found"}})
		return 1
	}

	emit(streamEvt(map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-test-model", "usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 5, "output_tokens": 1}}}))
	emit(streamEvt(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}}))
	emit(streamEvt(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Reading."}}))
	emit(streamEvt(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 7}}))
	toolInput := map[string]any{"path": "README.md"}
	emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{"model": "claude-test-model", "content": []map[string]any{
		{"type": "text", "text": "Reading."},
		{"type": "tool_use", "id": "toolu_fake_1", "name": mcpToolPrefix + "read", "input": toolInput},
	}}})

	toolText, err := callFakeBridge(mcpConfigPath, toolInput)
	if err != nil {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{err.Error()}})
		return 1
	}
	if scenario == "restart" {
		select {
		case <-interrupted:
			return 130
		case <-time.After(10 * time.Second):
			emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"was not interrupted"}})
			return 1
		}
	}

	emit(streamEvt(map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-test-model", "usage": map[string]any{"input_tokens": 3, "cache_read_input_tokens": 40, "cache_creation_input_tokens": 2, "output_tokens": 1}}}))
	emit(streamEvt(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking"}}))
	emit(streamEvt(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "Tool said: " + toolText}}))
	emit(streamEvt(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text"}}))
	emit(streamEvt(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "Done: " + toolText}}))
	emit(streamEvt(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 9}}))
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "Done: " + toolText, "num_turns": 2, "total_cost_usd": 0.01, "usage": map[string]any{"input_tokens": 13, "output_tokens": 16}})
	return 0
}

func streamEvt(event map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "parent_tool_use_id": nil, "event": event}
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func callFakeBridge(configPath string, arguments map[string]any) (string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var config struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", err
	}
	server := config.MCPServers[mcpServerName]
	post := func(body map[string]any) (map[string]any, error) {
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(encoded))
		for key, value := range server.Headers {
			req.Header.Set(key, value)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			return nil, nil
		}
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	if _, err := post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}}); err != nil {
		return "", err
	}
	if _, err := post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return "", err
	}
	// No _meta tool-use id: the bridge must correlate with the stream's tool_use.
	result, err := post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "read", "arguments": arguments}})
	if err != nil {
		return "", err
	}
	content, _ := result["result"].(map[string]any)["content"].([]any)
	if len(content) == 0 {
		return "", fmt.Errorf("tool call returned no content: %v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text, nil
}

type recordingInvoker struct {
	mu          sync.Mutex
	invocations []provideriface.ToolInvocation
	result      provideriface.ToolExecutionResult
}

func (i *recordingInvoker) ExecuteTool(_ context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.invocations = append(i.invocations, invocation)
	return i.result, nil
}

type fakeCLIHarness struct {
	authStore *pebblestore.AuthStore
	cli       *CLI
	record    string
	principal identity.Principal
}

func newFakeCLIHarness(t *testing.T, scenario string, enable bool) fakeCLIHarness {
	t.Helper()
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	authStore := pebblestore.NewAuthStore(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	if enable {
		if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{Provider: ProviderID, AccountScopeID: principal.AccountScopeID, Type: pebblestore.AuthTypeCLI, Label: "Claude Code CLI login", SetActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record.json")
	script := filepath.Join(dir, "claude")
	body := fmt.Sprintf("#!/bin/sh\n%s=1 %s=%q %s=%q exec %q -test.run='^TestFakeClaudeCLIProcess$' -- \"$@\"\n",
		fakeCLIEnvMode, fakeCLIEnvScenario, scenario, fakeCLIEnvRecord, record, executable)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	daemonEnv := map[string]string{
		CLIPathEnv:                script,
		"PATH":                    os.Getenv("PATH"),
		"TMPDIR":                  dir,
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth-should-not-pass",
		"ANTHROPIC_API_KEY":       "sk-should-not-pass",
	}
	cli := NewCLI()
	cli.lookupEnv = func(key string) (string, bool) { value, ok := daemonEnv[key]; return value, ok }
	return fakeCLIHarness{authStore: authStore, cli: cli, record: record, principal: principal}
}

func (h fakeCLIHarness) readRecord(t *testing.T) fakeRecord {
	t.Helper()
	raw, err := os.ReadFile(h.record)
	if err != nil {
		t.Fatalf("fake CLI did not record its invocation: %v", err)
	}
	var record fakeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func fakeRequest(invoker provideriface.ToolInvoker) provideriface.Request {
	return provideriface.Request{
		Model:        "claude-opus-test",
		Thinking:     "high",
		Instructions: "You are Swarm.\n\n[request-runtime-context]\n- current_provider: claudecode",
		Input: []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "Summarize the README."}}},
		},
		Tools: []provideriface.ToolDefinition{{
			Type:        "function",
			Name:        "read",
			Description: "Read a workspace file.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
		ToolInvoker: invoker,
	}
}

// Purpose: one Swarm provider request must become one isolated Claude Code
// turn whose only tools are Swarm's, executed by the Swarm ToolInvoker (the
// permission/persistence authority), with the model's own tool_use ID, real
// stream text/thinking/usage, and no credential material or Swarm private
// files left behind. Threats: built-in Claude Code tools or user hooks running
// outside Swarm permissions; tokens forwarded to the CLI; tool calls
// misattributed; turn files leaking. Owners: Runner.planTurn/executeTurn,
// mcpBridge, toolUseCorrelator, turnState. The fake CLI is the narrowest layer
// that exercises the real subprocess, stdin/stdout protocol and HTTP bridge
// without a live provider.
func TestRunnerExecutesIsolatedTurnThroughSwarmToolBridge(t *testing.T) {
	harness := newFakeCLIHarness(t, "success", true)
	invoker := &recordingInvoker{result: provideriface.ToolExecutionResult{Output: "raw", TextForModel: "README says hello"}}
	runner := NewRunner(harness.authStore, harness.cli)
	ctx := identity.ContextWithPrincipal(context.Background(), harness.principal)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var events []provideriface.StreamEvent
	response, err := runner.CreateResponseStreaming(ctx, fakeRequest(invoker), func(event provideriface.StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("CreateResponseStreaming: %v", err)
	}

	if response.Text != "Reading.\n\nDone: README says hello" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.StopReason != "end_turn" || response.Model != "claude-test-model" || response.RestartTurn {
		t.Fatalf("response = %+v", response)
	}
	if response.ReasoningSummary != "Tool said: README says hello" {
		t.Fatalf("reasoning = %q", response.ReasoningSummary)
	}
	usage := response.Usage
	if usage.Source != usageSource || usage.APIUsageRawPath != usageRawPath || usage.InputTokens != 3 || usage.OutputTokens != 9 || usage.CacheReadTokens != 40 || usage.CacheWriteTokens != 2 || usage.TotalTokens != 54 {
		t.Fatalf("usage = %+v", usage)
	}
	if len(usage.APIUsageHistory) != 2 {
		t.Fatalf("usage history = %v", usage.APIUsageHistory)
	}
	if usage.EstimatedCostUSD != 0 {
		t.Fatalf("client-side cost estimate must not be reported as cost: %v", usage.EstimatedCostUSD)
	}
	var streamed strings.Builder
	for _, event := range events {
		if event.Type == provideriface.StreamEventOutputTextDelta {
			streamed.WriteString(event.Delta)
		}
	}
	if streamed.String() != response.Text {
		t.Fatalf("streamed %q != response %q", streamed.String(), response.Text)
	}

	if len(invoker.invocations) != 1 {
		t.Fatalf("invocations = %+v", invoker.invocations)
	}
	call := invoker.invocations[0]
	if call.Name != "read" || call.CallID != "toolu_fake_1" || call.Arguments != `{"path":"README.md"}` {
		t.Fatalf("invocation = %+v", call)
	}

	record := harness.readRecord(t)
	args := strings.Join(record.Args, "\x00")
	for _, want := range [][]string{
		{"-p"}, {"--restricted"}, {"--tools", ""}, {"--strict-mcp-config"}, {"--no-session-persistence"},
		{"--disable-slash-commands"}, {"--permission-mode", "dontAsk"}, {"--allowedTools", "mcp__swarm"},
		{"--model", "claude-opus-test"}, {"--effort", "high"}, {"--input-format", "stream-json"},
	} {
		if !strings.Contains("\x00"+args+"\x00", "\x00"+strings.Join(want, "\x00")+"\x00") {
			t.Fatalf("args %q missing %q", record.Args, want)
		}
	}
	for _, forbidden := range []string{"--bare", "--dangerously-skip-permissions", "bypassPermissions", "You are Swarm"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("args contain %q: %q", forbidden, record.Args)
		}
	}
	env := strings.Join(record.Env, "\n")
	for _, forbidden := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "should-not-pass"} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("CLI env leaked %q", forbidden)
		}
	}
	if !strings.Contains(record.Prompt, "[user]\nSummarize the README.") || !strings.HasSuffix(record.Prompt, "- current_provider: claudecode") {
		t.Fatalf("prompt = %q", record.Prompt)
	}
	if record.Dir == "" || record.Dir != record.MCPDir {
		t.Fatalf("turn dir %q / mcp dir %q", record.Dir, record.MCPDir)
	}
	if _, err := os.Stat(record.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("turn directory with prompt and bridge token was not removed: %v", err)
	}
}

// Purpose: Claude Code is enabled per Swarm account. Threat: an account that
// never opted in could run work on another person's machine-level Claude
// sign-in. Owner: Runner.run (activeCLICredential). Asserts the rejection and
// that no CLI process was started.
func TestRunnerRejectsAccountWithoutClaudeCodeOptIn(t *testing.T) {
	harness := newFakeCLIHarness(t, "success", false)
	invoker := &recordingInvoker{}
	runner := NewRunner(harness.authStore, harness.cli)
	ctx := identity.ContextWithPrincipal(context.Background(), harness.principal)
	_, err := runner.CreateResponse(ctx, fakeRequest(invoker))
	if err == nil || !strings.Contains(err.Error(), "not enabled for this account") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(harness.record); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("CLI was started for an account without opt-in")
	}
	if len(invoker.invocations) != 0 {
		t.Fatalf("tools ran: %+v", invoker.invocations)
	}
	if _, err := runner.CreateResponse(context.Background(), fakeRequest(invoker)); !errors.Is(err, identity.ErrPrincipalRequired) {
		t.Fatalf("missing principal err = %v", err)
	}
}

// Purpose: a failed Claude Code turn must fail the Swarm step with the CLI's
// reason. Threat: treating an error result as an empty success. Owner:
// turnState.resultFailure.
func TestRunnerSurfacesClaudeCodeErrorResult(t *testing.T) {
	harness := newFakeCLIHarness(t, "error", true)
	runner := NewRunner(harness.authStore, harness.cli)
	ctx := identity.ContextWithPrincipal(context.Background(), harness.principal)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := runner.CreateResponse(ctx, fakeRequest(&recordingInvoker{}))
	if err == nil || !strings.Contains(err.Error(), "error_during_execution") || !strings.Contains(err.Error(), "model_not_found") {
		t.Fatalf("err = %v", err)
	}
}

// Purpose: a Swarm tool result that requires a turn restart (plan/mode changes)
// must stop the Claude Code turn and report RestartTurn so the V3 executor
// rehydrates from durable state. Threat: Claude continuing past a restart
// boundary with stale context. Owners: mcpBridge.callTool -> restartSignal ->
// executeTurn (SIGINT). The fake CLI exits only when interrupted.
func TestRunnerInterruptsTurnWhenToolRequiresRestart(t *testing.T) {
	harness := newFakeCLIHarness(t, "restart", true)
	invoker := &recordingInvoker{result: provideriface.ToolExecutionResult{TextForModel: "plan approved", RestartTurn: true}}
	runner := NewRunner(harness.authStore, harness.cli)
	ctx := identity.ContextWithPrincipal(context.Background(), harness.principal)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := runner.CreateResponse(ctx, fakeRequest(invoker))
	if err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if !response.RestartTurn {
		t.Fatalf("response = %+v", response)
	}
	if len(invoker.invocations) != 1 {
		t.Fatalf("invocations = %+v", invoker.invocations)
	}
}

func TestEffortForThinking(t *testing.T) {
	cases := map[string]struct {
		effort  string
		disable bool
	}{
		"":      {},
		"low":   {effort: "low"},
		"xhigh": {effort: "xhigh"},
		"max":   {effort: "max"},
		"ultra": {effort: "max"},
		"off":   {disable: true},
	}
	for thinking, want := range cases {
		effort, disable := effortForThinking(thinking)
		if effort != want.effort || disable != want.disable {
			t.Fatalf("effortForThinking(%q) = %q,%v", thinking, effort, disable)
		}
	}
}

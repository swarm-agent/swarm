package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/tool"
)

// cliRequestTimeout models the ~60 s cut-off seen with the Claude Code CLI
// in the forge runs. Inspecting the published CLI 2.1.303 package, its
// wrapFetchWithTimeout aborts each POST to an HTTP MCP server after the
// server's timeout, else MCP_TOOL_TIMEOUT, else 60000 ms, and clears the
// timer once the response headers arrive: a response-header timeout, which
// is what this client sets. The abort cancels the tool call on Swarm's
// side. A live claudecode turn has not yet confirmed it end to end. It is
// shortened so the test is fast; the commands below run well past it.
const cliRequestTimeout = 300 * time.Millisecond

// runtimeBash is the Swarm tool runtime behind the bridge: the real bash tool
// in a temporary workspace, with the sandbox off (host execution under
// ordinary OS permissions), as the ToolInvoker runs it for any provider.
type runtimeBash struct {
	runtime *tool.Runtime
	scope   tool.WorkspaceScope
}

func (b runtimeBash) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	out, err := b.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, b.scope, tool.Call{CallID: invocation.CallID, Name: invocation.Name, Arguments: invocation.Arguments})
	result := provideriface.ToolExecutionResult{CallID: invocation.CallID, Name: invocation.Name, Output: out}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}

func newBashBridge(t *testing.T, ctx context.Context) (*mcpBridge, string) {
	t.Helper()
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	old := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() { sandbox.SetDefault(old) })
	workspace := t.TempDir()
	invoker := runtimeBash{runtime: tool.NewRuntime(1), scope: tool.WorkspaceScope{PrimaryPath: workspace}}
	bridge, err := newMCPBridge(ctx, []provideriface.ToolDefinition{{Name: "bash", Parameters: map[string]any{"type": "object"}}}, invoker, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Close)
	return bridge, workspace
}

// callBash sends one tools/call for bash as the Claude Code CLI does
// (Streamable HTTP: it accepts a JSON or an event-stream answer) with a
// client that abandons a request whose response has not started within
// cliRequestTimeout, and returns the bash tool's own receipt.
func callBash(t *testing.T, bridge *mcpBridge, command string, timeoutMS int) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"command": command, "timeout_ms": timeoutMS, "category": "read", "critical": false, "explanation": []string{"Run a fixture command in the temporary workspace"}})
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "bash", "arguments": json.RawMessage(args)}})
	req, err := http.NewRequest(http.MethodPost, bridge.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bridge.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: cliRequestTimeout}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the tool call was abandoned by the client's request timeout: %v", err)
	}
	defer resp.Body.Close()
	var message struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *jsonRPCError `json:"error"`
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// The answer is the event whose data is the response to id 7;
		// progress notifications and keep-alive comments come before it.
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 8<<20)
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &message); err != nil {
				t.Fatalf("event data is not JSON-RPC: %q", line)
			}
			if string(message.ID) == "7" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the event stream ended without the tool call's response: %v", scanner.Err())
		}
	} else if err := json.NewDecoder(resp.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if message.Error != nil || len(message.Result.Content) != 1 {
		t.Fatalf("tools/call answer = %+v", message)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(message.Result.Content[0].Text), &receipt); err != nil {
		t.Fatalf("bash receipt is not JSON: %q", message.Result.Content[0].Text)
	}
	return receipt
}

// Requirement: a bash call whose timeout_ms is far longer than the Claude
// Code CLI's per-request timeout runs to completion through Swarm's MCP
// bridge and returns the command's own exit code. The bridge starts its
// answer at once and keeps the request alive, so the client never abandons
// it while the tool runs.
// Threat (forge run): every coder's bash call returned after about 60
// seconds with "bash execution cancelled: context canceled" whatever
// timeout_ms it set, because the CLI dropped the request and the bridge
// cancelled the tool with it; long checks could never finish as one command.
// Owners: mcpBridge.handle/callTool (the claudecode provider's tool path)
// over the real bash tool (tool.Runtime, executeBash) and a loopback HTTP
// client with a request timeout. No model or CLI binary is involved.
func TestBridgeBashOutlivesTheClientRequestTimeout(t *testing.T) {
	bridge, _ := newBashBridge(t, context.Background())
	receipt := callBash(t, bridge, "sleep 1; exit 7", 30000)
	if receipt["exit_code"] != float64(7) || receipt["timed_out"] != false {
		t.Fatalf("bash receipt = %+v, want the command's own exit code 7", receipt)
	}
}

// Requirement: a command that runs past its own timeout_ms is stopped and
// reported as timed out through the same bridge, and does not keep running
// after the tool call has returned.
// Threat: a cut-off tool call that leaves its command running in the
// sandbox, so a later check reads a half-done state.
// Owner: mcpBridge.callTool over the real bash tool.
func TestBridgeBashPastItsTimeoutIsStoppedAndReported(t *testing.T) {
	bridge, workspace := newBashBridge(t, context.Background())
	marker := filepath.Join(workspace, "finished")
	receipt := callBash(t, bridge, "sleep 2; touch "+strconv.Quote(marker), 600)
	if receipt["timed_out"] != true {
		t.Fatalf("bash receipt = %+v, want timed_out", receipt)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the timed-out command kept running after the tool call returned")
	}
}

// Requirement: while a tool call runs, the bridge keeps telling the
// provider-attempt watchdog that the turn is alive, so a long command is not
// ended by the watchdog's inactivity timeout (the CLI is silent meanwhile).
// Threat: a bash call within its documented maximum cut off after the
// watchdog's few minutes and the turn retried under it.
// Owner: mcpBridge.callTool (heartbeat) with the attempt-activity reporter
// runProviderAttempt installs.
func TestBridgeKeepsTheAttemptAliveWhileAToolRuns(t *testing.T) {
	oldHeartbeat, oldKeepAlive := mcpToolHeartbeat, mcpStreamKeepAlive
	mcpToolHeartbeat, mcpStreamKeepAlive = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { mcpToolHeartbeat, mcpStreamKeepAlive = oldHeartbeat, oldKeepAlive })
	var reports atomic.Int64
	ctx := provideriface.WithAttemptActivityReporter(context.Background(), func(provideriface.AttemptActivityPhase) { reports.Add(1) })
	bridge, _ := newBashBridge(t, ctx)
	receipt := callBash(t, bridge, "sleep 1", 30000)
	if receipt["exit_code"] != float64(0) {
		t.Fatalf("bash receipt = %+v", receipt)
	}
	if n := reports.Load(); n < 8 {
		t.Fatalf("attempt activity reported %d times during a 1 s tool call with a 50 ms heartbeat", n)
	}
}

// Requirement: the Claude Code CLI is told (MCP_TOOL_TIMEOUT) to wait for an
// MCP tool call at least as long as Swarm's longest tool call, so Swarm's own
// timeout, not the client's, ends a long command.
// Threat: the CLI's default tool timeout cutting bash off below the
// documented maximum.
// Owner: Runner.planTurn (the CLI environment for a turn with tools).
func TestTurnGivesTheCLIAToolTimeoutAboveSwarmsLongestToolCall(t *testing.T) {
	r := &Runner{cli: &CLI{lookupEnv: func(string) (string, bool) { return "", false }}}
	invoker := runtimeBash{runtime: tool.NewRuntime(1), scope: tool.WorkspaceScope{PrimaryPath: t.TempDir()}}
	plan, err := r.planTurn(context.Background(), provideriface.Request{Model: "claude-test", Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: invoker}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer plan.bridge.Close()
	for _, entry := range plan.env {
		if value, ok := strings.CutPrefix(entry, "MCP_TOOL_TIMEOUT="); ok {
			ms, err := strconv.Atoi(value)
			if err != nil || ms < 1800000 {
				t.Fatalf("MCP_TOOL_TIMEOUT = %q, want at least bash's 1800000 ms maximum", value)
			}
			return
		}
	}
	t.Fatalf("the CLI environment has no MCP_TOOL_TIMEOUT: %q", plan.env)
}

// Requirement: the Claude Code CLI's MCP tool idle watchdog
// (CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT) is set from Swarm's own longest tool
// call plus the margin, the same single value as MCP_TOOL_TIMEOUT, so a
// tool call that sends no progress the CLI counts is not ended before
// Swarm's own timeout.
// Threat: a bash call longer than the CLI's 300 s default idle timeout for
// an HTTP server cut off, because whether the CLI passes a progressToken
// (needed for progress to reset that watchdog) could not be confirmed.
// Owner: Runner.planTurn via mcpToolTimeoutEnv (the CLI environment for a
// turn with tools). A turn without tools gets neither variable.
func TestTurnGivesTheCLIAToolIdleTimeoutFromSwarmsLongestToolCall(t *testing.T) {
	r := &Runner{cli: &CLI{lookupEnv: func(string) (string, bool) { return "", false }}}
	invoker := runtimeBash{runtime: tool.NewRuntime(1), scope: tool.WorkspaceScope{PrimaryPath: t.TempDir()}}
	plan, err := r.planTurn(context.Background(), provideriface.Request{Model: "claude-test", Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: invoker}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer plan.bridge.Close()
	want := strconv.FormatInt((provideriface.MaxToolCallDuration + mcpToolTimeoutMargin).Milliseconds(), 10)
	found := map[string]string{}
	for _, entry := range plan.env {
		for _, key := range []string{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT", "MCP_TOOL_TIMEOUT"} {
			if value, ok := strings.CutPrefix(entry, key+"="); ok {
				if _, dup := found[key]; dup {
					t.Fatalf("%s set twice in the CLI environment: %q", key, plan.env)
				}
				found[key] = value
			}
		}
	}
	for _, key := range []string{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT", "MCP_TOOL_TIMEOUT"} {
		if found[key] != want {
			t.Fatalf("%s = %q, want %s (Swarm's longest tool call plus the margin); env %q", key, found[key], want, plan.env)
		}
	}

	noTools, err := r.planTurn(context.Background(), provideriface.Request{Model: "claude-test"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range noTools.env {
		if strings.HasPrefix(entry, "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=") || strings.HasPrefix(entry, "MCP_TOOL_TIMEOUT=") {
			t.Fatalf("a turn without tools set %q", entry)
		}
	}
}

// Requirement: when the client passes _meta.progressToken on tools/call, the
// bridge sends notifications/progress for that token, with increasing
// progress, for the whole time the tool runs, and only then the response.
// Threat: progress that stops (or never starts) partway through a long call,
// so the CLI's idle watchdog ends it.
// Owner: mcpBridge.streamToolCall over the real bash tool; the keep-alive
// interval is shortened so the test is fast.
func TestBridgeSendsProgressUntilALongToolCallEnds(t *testing.T) {
	oldKeepAlive := mcpStreamKeepAlive
	mcpStreamKeepAlive = 50 * time.Millisecond
	t.Cleanup(func() { mcpStreamKeepAlive = oldKeepAlive })
	bridge, _ := newBashBridge(t, context.Background())

	args, _ := json.Marshal(map[string]any{"command": "sleep 1", "timeout_ms": 30000, "category": "read", "critical": false, "explanation": []string{"Run a fixture command in the temporary workspace"}})
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{
		"name": "bash", "arguments": json.RawMessage(args), "_meta": map[string]any{"progressToken": "tok-9"}}})
	req, err := http.NewRequest(http.MethodPost, bridge.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bridge.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type = %q, want an event stream", resp.Header.Get("Content-Type"))
	}
	start := time.Now()
	var progressAt []time.Duration
	lastProgress := 0.0
	var responseAt time.Duration
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProgressToken string  `json:"progressToken"`
				Progress      float64 `json:"progress"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatalf("event data is not JSON-RPC: %q", line)
		}
		if event.Method == "notifications/progress" {
			if responseAt != 0 {
				t.Fatal("progress notification after the response")
			}
			if event.Params.ProgressToken != "tok-9" || event.Params.Progress <= lastProgress {
				t.Fatalf("progress notification %+v after progress %v, want token tok-9 and increasing progress", event.Params, lastProgress)
			}
			lastProgress = event.Params.Progress
			progressAt = append(progressAt, time.Since(start))
			continue
		}
		if string(event.ID) == "9" {
			responseAt = time.Since(start)
		}
	}
	if responseAt == 0 {
		t.Fatalf("the event stream ended without the tool call's response: %v", scanner.Err())
	}
	// A 1 s call with a 50 ms interval: progress must keep coming throughout,
	// up to shortly before the response, not just at the start.
	if len(progressAt) < 10 {
		t.Fatalf("%d progress notifications during a 1 s tool call with a 50 ms interval", len(progressAt))
	}
	if gap := responseAt - progressAt[len(progressAt)-1]; gap > 500*time.Millisecond {
		t.Fatalf("last progress %v before the response; progress stopped while the tool ran", gap)
	}
	for i := 1; i < len(progressAt); i++ {
		if gap := progressAt[i] - progressAt[i-1]; gap > 500*time.Millisecond {
			t.Fatalf("progress gap of %v while the tool ran", gap)
		}
	}
}

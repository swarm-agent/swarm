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
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/tool"
)

// cliRequestTimeout stands in for the Claude Code CLI's own per-request
// timeout on its MCP HTTP transport (about 60 seconds in the forge runs): a
// request whose response has not started by then is abandoned, which cancels
// the tool call on Swarm's side. It is shortened so the test is fast; the
// commands below run well past it.
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

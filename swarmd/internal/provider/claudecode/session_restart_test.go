package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type restartInvoker struct {
	entered chan struct{}
	calls   atomic.Int64
}

func (i *restartInvoker) ExecuteTool(ctx context.Context, call provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	if i.calls.Add(1) == 1 {
		close(i.entered)
		<-ctx.Done()
		return provideriface.ToolExecutionResult{}, ctx.Err()
	}
	return provideriface.ToolExecutionResult{Output: "retried tool completed"}, nil
}

// Purpose: a failed in-flight MCP transport must remain reconnectable at the
// same URL with the same credential. SessionMCP authenticates the retried call
// and delegates it to the next active ToolInvoker, not a dead per-turn server.
// A child fixture process makes a real HTTP request, observes its connection
// fail, and retries once after an explicit stdin signal (no timing simulation).
// This proves our endpoint contract, not the proprietary CLI's retry policy.
func TestSessionMCPInFlightDisconnectRetrySameToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	endpoint := testEndpoint(t)
	server := httptest.NewServer(endpoint)
	defer func() { server.Close() }()
	addr := server.Listener.Addr().String()
	if err := endpoint.SetAddress(addr); err != nil {
		t.Fatal(err)
	}
	_, config, err := endpoint.prepare(testPrincipal(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(config)
	invoker := &restartInvoker{entered: make(chan struct{})}
	plan := turnPlan{input: &turnInput{}, restart: &restartSignal{}, correlat: newToolUseCorrelator()}
	req := provideriface.Request{SessionID: "restart", Tools: []provideriface.ToolDefinition{{Name: "bash"}}, ToolInvoker: invoker}
	detach, err := endpoint.attach(ctx, testPrincipal(), req, &plan)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSessionMCPRetryCLIProcess$")
	cmd.Env = append(os.Environ(), "SWARM_MCP_RETRY_FIXTURE="+config)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	select {
	case <-invoker.entered:
	case <-ctx.Done():
		t.Fatal("fixture never called tool")
	}
	server.CloseClientConnections()
	server.Close()
	detach()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "transport-failed" {
		t.Fatalf("CLI did not see one transport failure: %q %s", scanner.Text(), stderr.String())
	}
	// Recreate the endpoint, not just its HTTP server; persistent authentication
	// is the same secret store. Store reopen is covered by the resume test.
	endpoint = NewSessionMCP(endpoint.security, endpoint.auth, endpoint.root)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewUnstartedServer(endpoint)
	server.Listener = listener
	server.Start()
	if err = endpoint.SetAddress(addr); err != nil {
		t.Fatal(err)
	}
	nextPlan := turnPlan{input: &turnInput{}, restart: &restartSignal{}, correlat: newToolUseCorrelator()}
	detachNext, err := endpoint.attach(ctx, testPrincipal(), req, &nextPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer detachNext()
	_, nextConfig, err := endpoint.prepare(testPrincipal(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(nextConfig)
	if nextConfig != config || !bytes.Equal(original, after) {
		t.Fatal("restart changed config or token")
	}
	io.WriteString(stdin, "retry\n")
	if !scanner.Scan() || scanner.Text() != "retried tool completed" {
		t.Fatalf("CLI retry failed: %q %s", scanner.Text(), stderr.String())
	}
	stdin.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatalf("fixture: %v %s", err, stderr.String())
	}
	if invoker.calls.Load() != 2 {
		t.Fatalf("calls=%d, want one failed call and one retry", invoker.calls.Load())
	}
}

// Purpose: executable half of the restart fixture above; real HTTP client with
// bounded requests proves transport failure is distinguishable from tool loss.
func TestSessionMCPRetryCLIProcess(t *testing.T) {
	path := os.Getenv("SWARM_MCP_RETRY_FIXTURE")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	call := func() (string, error) {
		req, err := http.NewRequest(http.MethodPost, cfg.Servers["swarm"].URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bash","arguments":{},"_meta":{"claudecode/toolUseId":"restart-call"}}}`))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", cfg.Servers["swarm"].Headers["Authorization"])
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		var result struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err = json.Unmarshal(body, &result); err != nil {
			return "", err
		}
		if len(result.Result.Content) != 1 {
			return "", fmt.Errorf("unexpected response: %s", body)
		}
		return result.Result.Content[0].Text, nil
	}
	if _, err = call(); err == nil {
		t.Fatal("first call unexpectedly succeeded")
	}
	fmt.Println("transport-failed")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "retry" {
		t.Fatal("retry signal missing")
	}
	result, err := call()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(result)
}

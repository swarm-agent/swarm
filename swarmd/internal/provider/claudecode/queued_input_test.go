package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// queuedInputStub hands out its messages once, as the run's tracker does.
type queuedInputStub struct {
	mu       sync.Mutex
	messages []string
	claims   int
}

func (s *queuedInputStub) ClaimQueuedUserMessages() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	out := s.messages
	s.messages = nil
	return out, nil
}

type fixedToolInvoker struct{ output string }

func (i fixedToolInvoker) ExecuteTool(_ context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	return provideriface.ToolExecutionResult{CallID: invocation.CallID, Name: invocation.Name, Output: i.output, TextForModel: i.output}, nil
}

// fakeClaudeScript stands in for the Claude Code CLI in stream-json mode with
// shell builtins only: it records its first stdin line (the turn input),
// announces itself, records the next stdin line (input the CLI receives while
// the turn runs), emits the turn's result, then records whether stdin closed.
func fakeClaudeScript(t *testing.T, dir string) string {
	t.Helper()
	quote := func(name string) string { return "'" + filepath.Join(dir, name) + "'" }
	script := "#!/bin/sh\n" +
		"IFS= read -r first\n" +
		"printf '%s\\n' \"$first\" > " + quote("first.jsonl") + "\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"claude-test\"}'\n" +
		": > " + quote("ready") + "\n" +
		"IFS= read -r second\n" +
		"printf '%s\\n' \"$second\" > " + quote("second.jsonl") + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done BANANA\"}'\n" +
		"if IFS= read -r extra; then printf '%s\\n' \"$extra\" > " + quote("extra.jsonl") + "; fi\n" +
		": > " + quote("stdin-closed") + "\n"
	path := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// callBridgeTool sends one tools/call as the Claude Code CLI does and returns
// the text of the tool result the CLI would hand the model.
func callBridgeTool(t *testing.T, bridge *mcpBridge) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "bash", "arguments": map[string]any{"command": "ls"}}})
	req, err := http.NewRequest(http.MethodPost, bridge.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bridge.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var message struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *jsonRPCError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if message.Error != nil || len(message.Result.Content) != 1 {
		t.Fatalf("tools/call answer = %+v", message)
	}
	return message.Result.Content[0].Text
}

// Requirement: a message the person queued while a tool ran reaches the
// model as the person's own turn: the runner writes it to the running CLI's
// stdin as a stream-json user message (how Claude Code takes input queued
// during a turn), after the tool runs and before the CLI gets its result.
// The tool result the CLI receives is the tool's own output only. Stdin stays
// open until the turn's result arrives, then closes so the CLI exits.
// Threat: the note appended inside the tool output, where the model rightly
// treats it as untrusted and refuses it (live run: "end your reply with
// BANANA" ignored), or stdin closed before the note could be sent.
// Owners: Runner.planTurn (queuedInputToolInvoker over the MCP bridge),
// executeTurn / turnInput (stdin lifecycle), turnInput.deliverQueued.
// Narrowest layer: the real runner turn with a shell-builtin stand-in for
// the CLI and a loopback tools/call as the CLI sends it; no model involved.
func TestQueuedNoteIsWrittenToCLIStdinAsUserMessageNotToolOutput(t *testing.T) {
	const note = "Also: end your final reply with the single word BANANA."
	const toolOutput = `{"exit_code":0,"output":"file.txt"}`
	dir := t.TempDir()
	script := fakeClaudeScript(t, dir)
	source := &queuedInputStub{messages: []string{note}}
	r := &Runner{cli: &CLI{lookupEnv: func(string) (string, bool) { return "", false }}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	plan, err := r.planTurn(ctx, provideriface.Request{
		Model:           "claude-test",
		Input:           []map[string]any{{"role": "user", "content": "begin the task"}},
		Tools:           []provideriface.ToolDefinition{{Name: "bash", Parameters: map[string]any{"type": "object"}}},
		ToolInvoker:     fixedToolInvoker{output: toolOutput},
		QueuedUserInput: source,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer plan.bridge.Close()

	type outcome struct {
		response provideriface.Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := executeTurn(ctx, script, plan, "claude-test", nil)
		done <- outcome{response, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("the stand-in CLI never started: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	result := callBridgeTool(t, plan.bridge)
	if result != toolOutput || strings.Contains(result, "BANANA") {
		t.Fatalf("tool result the CLI received = %q, want the tool's own output only", result)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("turn failed: %v", got.err)
	}
	if got.response.Text != "done BANANA" {
		t.Fatalf("response text = %q", got.response.Text)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "second.jsonl"))
	if err != nil {
		t.Fatalf("the CLI received nothing on stdin after the tool call: %v", err)
	}
	var line struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &line); err != nil {
		t.Fatalf("stdin line %q is not stream-json: %v", raw, err)
	}
	if line.Type != "user" || line.Message.Role != "user" || len(line.Message.Content) != 1 || line.Message.Content[0].Type != "text" || line.Message.Content[0].Text != note {
		t.Fatalf("stdin line = %s, want a stream-json user message carrying the note", raw)
	}
	if first, err := os.ReadFile(filepath.Join(dir, "first.jsonl")); err != nil || strings.Contains(string(first), "BANANA") {
		t.Fatalf("turn input = %q (%v), want the transcript without the note", first, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the CLI got more input after the note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stdin-closed")); err != nil {
		t.Fatalf("stdin was not closed after the result: %v", err)
	}
	if source.claims != 1 {
		t.Fatalf("queued input claimed %d times, want once after the one tool call", source.claims)
	}
}

// Requirement: once the turn's input has closed, a tool result claims no
// queued message, so a message that could not reach the CLI stays queued for
// the run (never receipted as delivered, never folded into tool output).
// Threat: a note claimed after the CLI stopped reading, then receipted
// although the model never saw it.
// Owner: turnInput.deliverQueued.
// Narrowest layer: the stdin wrapper over a pipe.
func TestQueuedNoteIsNotClaimedAfterTurnInputCloses(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	in := &turnInput{}
	in.attach(writer)
	if err := in.close(); err != nil {
		t.Fatal(err)
	}
	source := &queuedInputStub{messages: []string{"late note"}}
	in.deliverQueued(source)
	if source.claims != 0 || len(source.messages) != 1 {
		t.Fatalf("claims = %d, remaining = %v; want the note left unclaimed", source.claims, source.messages)
	}
	if err := in.deliveryError(); err != nil {
		t.Fatalf("delivery error = %v", err)
	}
}

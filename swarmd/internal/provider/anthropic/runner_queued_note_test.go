package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: a message the person queued while a tool ran reaches Claude in the
// request after the tool call as the person's own text block in the user
// message that carries the tool_result, after the tool_result block and
// outside its content. The run places the note as a user input item after
// the function_call_output; this adapter turns that into the Messages API
// shape.
// Threat: the note sent inside the tool result's content (the model rightly
// treats tool output as untrusted and refuses the person's instruction), or
// split into a second user message instead of the turn that answers the
// tool call.
// Boundary: buildAnthropicMessages (function_call_output folding).
// Narrowest layer: the pure input-to-Messages conversion; no API call.
func TestQueuedNoteAfterToolResultIsUserTextBlockBesideToolResult(t *testing.T) {
	const note = "Also: end your final reply with the single word BANANA."
	input := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "begin the task"}}},
		{"type": "function_call", "call_id": "toolu_1", "name": "bash", "arguments": `{"command":"ls"}`},
		{"type": "function_call_output", "call_id": "toolu_1", "output": `{"exit_code":0}`},
		{"role": "user", "content": []map[string]any{{"type": "input_text", "text": note}}},
	}
	messages, err := buildAnthropicMessages(input, provideriface.SessionMediaContract{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			ToolUseID string `json:"tool_use_id"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 {
		t.Fatalf("messages = %s, want user, assistant tool_use, then one user turn with the tool result and the note", raw)
	}
	turn := decoded[2]
	if turn.Role != "user" || len(turn.Content) != 2 {
		t.Fatalf("tool-result turn = %s, want a user message with two blocks", raw)
	}
	result, text := turn.Content[0], turn.Content[1]
	if result.Type != "tool_result" || result.ToolUseID != "toolu_1" {
		t.Fatalf("first block = %+v, want the tool_result for toolu_1", result)
	}
	for _, part := range result.Content {
		if strings.Contains(part.Text, "BANANA") {
			t.Fatalf("the note is inside the tool result content: %+v", result)
		}
	}
	if text.Type != "text" || text.Text != note {
		t.Fatalf("second block = %+v, want the person's note as a text block", text)
	}
}

// Purpose: tool results not followed by user input keep their own user
// message, and an assistant item after them is not folded in.
// Threat: the folding above swallowing the next assistant turn or reordering
// the transcript.
// Boundary: buildAnthropicMessages.
// Narrowest layer: the pure conversion.
func TestToolResultFoldingStopsAtNonUserInput(t *testing.T) {
	input := []map[string]any{
		{"role": "user", "content": "go"},
		{"type": "function_call", "call_id": "toolu_1", "name": "bash", "arguments": `{}`},
		{"type": "function_call_output", "call_id": "toolu_1", "output": "ok"},
		{"role": "assistant", "content": "done"},
	}
	messages, err := buildAnthropicMessages(input, provideriface.SessionMediaContract{})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[2].Role != "user" || len(messages[2].Content) != 1 || messages[3].Role != "assistant" {
		raw, _ := json.Marshal(messages)
		t.Fatalf("messages = %s, want the tool result alone, then the assistant turn", raw)
	}
}

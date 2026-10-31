package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: a session read (read_task_session, get_task_session, manage-sessions
// read_messages) spends its text budget on readable text, never on provider
// internals. A stored tool record carrying a Gemini thought signature (and
// encrypted reasoning) reads with the signature left out, a one-line
// provider_internal marker in its place and the readable fields first, so a
// 3,000-character read shows the tool's output. Threat: the live defect where
// such a read went almost entirely to a thought_signature blob. Content that
// is not a JSON object, or has no provider-internal field, reads unchanged;
// the transform is deterministic, so a chunk's content_offset continues the
// same text. Authority: ReadableMessageContent and
// ChunkReadableMessageContent, the one rule every session read pages with.
func TestReadableMessageContentLeavesOutProviderInternals(t *testing.T) {
	signature := strings.Repeat("Q2lwaGVy", 2000) // 16,000 bytes of opaque base64
	record := map[string]any{
		"path_id": "run.v3.provider-tool-result.v1", "type": "tool_result", "tool_name": "bash", "call_id": "call-1",
		"arguments": `{"command":"go test ./internal/api"}`,
		"metadata":  map[string]any{"google": map[string]any{"thought_signature": signature}},
		"reasoning": []any{map[string]any{"encrypted_content": signature, "summary": "checked the tests"}},
		"output":    "ok  swarm/packages/swarmd/internal/api 2.1s",
	}
	raw, _ := json.Marshal(record)
	content := string(raw)

	readable := ReadableMessageContent(content)
	if strings.Contains(readable, "Q2lwaGVy") || strings.Contains(readable, "thought_signature\":") {
		t.Fatalf("provider internals survived: %.300s", readable)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(readable), &decoded); err != nil {
		t.Fatalf("readable content is not JSON: %v", err)
	}
	marker, _ := decoded[ProviderInternalMarkerKey].(string)
	if marker != "2 provider-internal field(s) left out (encrypted_content, thought_signature)" || strings.Contains(marker, "\n") {
		t.Fatalf("marker = %q", marker)
	}
	// The wrapper that only held the signature is gone; readable fields
	// stay, the reasoning summary too.
	if _, ok := decoded["metadata"]; ok || decoded["output"] != record["output"] || decoded["tool_name"] != "bash" || decoded["arguments"] != record["arguments"] {
		t.Fatalf("readable fields: %v", decoded)
	}
	if reasoning, _ := decoded["reasoning"].([]any); len(reasoning) != 1 || reasoning[0].(map[string]any)["summary"] != "checked the tests" {
		t.Fatalf("reasoning summary: %v", decoded["reasoning"])
	}
	// Readable text first: the tool's output is inside the first 3,000
	// characters a read spends, where before the signature filled them.
	chunk, err := ChunkReadableMessageContent(content, 0, 3000)
	if err != nil || chunk.Truncated || !strings.Contains(chunk.Content, record["output"].(string)) ||
		strings.Index(chunk.Content, "\"output\"") > strings.Index(chunk.Content, ProviderInternalMarkerKey) {
		t.Fatalf("3,000-character read: %+v %v", chunk, err)
	}
	if raw, err := ChunkMessageContent(content, 0, 3000); err != nil || strings.Contains(raw.Content, record["output"].(string)) {
		t.Fatalf("the raw content was expected to bury the output behind the signature: %v", err)
	}
	// Deterministic: a continuation at a returned offset reads the same text.
	first, _ := ChunkReadableMessageContent(content, 0, 120)
	second, err := ChunkReadableMessageContent(content, first.End, 120)
	if err != nil || !strings.HasPrefix(readable, first.Content+second.Content) || ReadableMessageContent(content) != readable {
		t.Fatalf("continuation: %q + %q, %v", first.Content, second.Content, err)
	}

	// Unchanged: plain text, JSON without internals, arrays and broken JSON.
	for _, same := range []string{
		"Plain assistant text with a thought_signature mention.",
		`{"tool_name":"bash","output":"ok","metadata":{"google":{"candidate_index":0}}}`,
		`[{"thought_signature":"abc"}]`,
		`{"thought_signature": "abc"`,
		`{"a":1} {"thought_signature":"abc"}`,
	} {
		if got := ReadableMessageContent(same); got != same {
			t.Fatalf("changed %q to %q", same, got)
		}
	}
	// HTML-like text reads as written.
	if got := ReadableMessageContent(`{"output":"a < b && c","thoughtSignature":"x"}`); !strings.HasPrefix(got, `{"output":"a < b && c",`) {
		t.Fatalf("escaped readable text: %s", got)
	}
}

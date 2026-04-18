package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestReadToolLongLineNotTruncated(t *testing.T) {
	workspace := t.TempDir()
	longLine := strings.Repeat("x", 6000)
	if err := os.WriteFile(filepath.Join(workspace, "long-line.txt"), []byte(longLine+"\n"), 0o644); err != nil {
		t.Fatalf("write long-line fixture: %v", err)
	}

	rt := tool.NewRuntime(2)
	results := rt.ExecuteBatch(context.Background(), workspace, []tool.Call{
		{
			CallID:    "read-long-line",
			Name:      "read",
			Arguments: `{"path":"long-line.txt","line_start":1,"max_lines":1}`,
		},
	})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if got := strings.TrimSpace(results[0].Error); got != "" {
		t.Fatalf("unexpected read error: %s", got)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(results[0].Output), &payload); err != nil {
		t.Fatalf("decode read payload: %v", err)
	}
	if asBool(t, payload, "line_text_truncated") {
		t.Fatalf("expected line_text_truncated=false, payload=%v", payload)
	}
	if asBool(t, payload, "truncated") {
		t.Fatalf("expected truncated=false, payload=%v", payload)
	}

	lines, ok := payload["lines"].([]any)
	if !ok {
		t.Fatalf("expected lines array, got %T", payload["lines"])
	}
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}
	entry, ok := lines[0].(map[string]any)
	if !ok {
		t.Fatalf("expected line entry object, got %T", lines[0])
	}
	gotText := asString(t, entry, "text")
	if gotText != longLine {
		t.Fatalf("read text mismatch: got_len=%d want_len=%d", len(gotText), len(longLine))
	}
}

func asBool(t *testing.T, payload map[string]any, key string) bool {
	t.Helper()
	value, ok := payload[key]
	if !ok {
		t.Fatalf("missing key %q in payload=%v", key, payload)
	}
	typed, ok := value.(bool)
	if !ok {
		t.Fatalf("expected %q bool, got %T", key, value)
	}
	return typed
}

func asString(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, ok := payload[key]
	if !ok {
		t.Fatalf("missing key %q in payload=%v", key, payload)
	}
	typed, ok := value.(string)
	if !ok {
		t.Fatalf("expected %q string, got %T", key, value)
	}
	return typed
}

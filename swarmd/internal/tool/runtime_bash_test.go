package tool

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestBashKeepsLargeTextForOutputViewer(t *testing.T) {
	workspace := t.TempDir()
	line := strings.Repeat("x", 1024)
	repeat := 64
	expected := strings.Repeat(line+"\n", repeat)
	rt := NewRuntime(2)
	results := rt.ExecuteBatch(context.Background(), workspace, []Call{
		{
			CallID: "bash-large-text",
			Name:   "bash",
			Arguments: mustJSON(t, map[string]any{
				"command":    "yes " + strings.TrimSpace(line) + " | head -n " + strconv.Itoa(repeat),
				"timeout_ms": 5000,
			}),
		},
	})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if strings.TrimSpace(results[0].Error) != "" {
		t.Fatalf("unexpected bash error: %s", results[0].Error)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(results[0].Output), &decoded); err != nil {
		t.Fatalf("decode bash payload: %v\n%s", err, results[0].Output)
	}
	if truncated, _ := decoded["truncated"].(bool); truncated {
		t.Fatalf("did not expect output viewer payload to be truncated")
	}
	got, _ := decoded["output"].(string)
	if got != expected {
		t.Fatalf("large bash output length = %d, want %d", len(got), len(expected))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(encoded)
}

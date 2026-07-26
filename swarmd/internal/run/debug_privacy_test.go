package run

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunRequestDebugEventExcludesPrivateContent(t *testing.T) {
	const sentinel = "SEC08_PRIVATE_PROMPT_SENTINEL"
	t.Setenv("SWARMD_RUN_REQUEST_DEBUG", "1")

	output := captureRunDebugStderr(t, func() {
		runRequestDebugEvent("provider_request", map[string]any{
			"session_id":        "session-correlation",
			"input_item_count":  3,
			"instruction_runes": len([]rune(sentinel)),
		})
	})
	if strings.Contains(output, sentinel) {
		t.Fatalf("request debug output exposed private sentinel: %s", output)
	}
	if !strings.Contains(output, `"input_item_count":3`) || !strings.Contains(output, `"instruction_runes":29`) {
		t.Fatalf("request debug output lost bounded metadata: %s", output)
	}
}

func TestRunCompactionDebugEventDoesNotWriteEnvironmentSelectedFile(t *testing.T) {
	const sentinel = "SEC08_PRIVATE_COMPACTION_SENTINEL"
	logPath := t.TempDir() + "/compaction.jsonl"
	t.Setenv("SWARMD_COMPACTION_DEBUG", "1")
	t.Setenv("SWARMD_COMPACTION_LOG_PATH", logPath)

	output := captureRunDebugStderr(t, func() {
		runCompactionDebugEvent("memory_compaction_failed", map[string]any{
			"session_id":     "session-correlation",
			"error_category": "provider_error",
		})
	})
	if strings.Contains(output, sentinel) {
		t.Fatalf("compaction debug output exposed private sentinel: %s", output)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("environment-selected compaction file exists or stat failed: %v", err)
	}
}

func TestRunPanicRecoveryDoesNotExposeRecoveredValuesOrStacks(t *testing.T) {
	for _, path := range []string{"ai_task_v2.go", "service.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(source)
		for _, forbidden := range []string{"runtime/debug", "debug.Stack()", "panic=%v", "stack=%s", "panic: %v"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s retains private panic sink %q", path, forbidden)
			}
		}
	}
}

func TestRunDebugEventsRemainJSONMetadata(t *testing.T) {
	t.Setenv("SWARMD_RUN_REQUEST_DEBUG", "1")
	output := captureRunDebugStderr(t, func() {
		runRequestDebugEvent("provider_response", map[string]any{"response_text_runes": 17})
	})
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(output), "[swarmd.run.request] "))
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("debug event is not JSON: %v output=%q", err, output)
	}
}

func captureRunDebugStderr(t *testing.T, emit func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = original }()

	emit()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return string(output)
}

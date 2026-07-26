package api

import (
	"os"
	"strings"
	"testing"
)

func TestPanicRecoveryDoesNotExposeRecoveredValuesOrStacks(t *testing.T) {
	for _, path := range []string{"run_stream_ws.go", "sessions_v3_executor.go"} {
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

package pebblestore

import (
	"io"
	"strings"
	"testing"
)

// Purpose: the archive sink enforces the 512 MiB cap before writing excess
// bytes. Testing its boundary directly avoids a large fixture and proves the
// same writer used by buildRunnerPayload rejects excess without partial writes.
func TestRunnerArchiveSizeLimit(t *testing.T) {
	w := &runnerArchiveWriter{dst: io.Discard, remaining: RunnerArchiveLimit}
	chunk := []byte(strings.Repeat("x", 1<<20))
	for i := 0; i < 512; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := w.Write([]byte("x")); err == nil || n != 0 {
		t.Fatal("oversize accepted")
	}
	if w.size != RunnerArchiveLimit {
		t.Fatal("partial excess write")
	}
}

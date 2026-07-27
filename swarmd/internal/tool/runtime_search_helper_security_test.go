package tool

import (
	"bytes"
	"testing"
)

func TestBoundedSearchHelperBufferCapsCapturedOutput(t *testing.T) {
	buffer := newBoundedSearchHelperBuffer(8)
	payload := []byte("0123456789abcdef")
	written, err := buffer.Write(payload)
	if err != nil {
		t.Fatalf("write bounded buffer: %v", err)
	}
	if written != len(payload) {
		t.Fatalf("reported bytes written = %d, want %d", written, len(payload))
	}
	if !buffer.Overflowed() {
		t.Fatalf("overflow was not recorded")
	}
	if got := buffer.String(); got != "01234567" {
		t.Fatalf("captured output = %q, want bounded prefix", got)
	}

	more := bytes.Repeat([]byte("x"), 1024)
	written, err = buffer.Write(more)
	if err != nil || written != len(more) {
		t.Fatalf("write after overflow = (%d, %v), want (%d, nil)", written, err, len(more))
	}
	if len(buffer.Bytes()) != 8 {
		t.Fatalf("captured bytes after overflow = %d, want 8", len(buffer.Bytes()))
	}
}

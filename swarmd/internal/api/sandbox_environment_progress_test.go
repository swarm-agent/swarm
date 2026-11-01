package api

import (
	"strings"
	"testing"
)

// Purpose: a build log page is addressed by absolute byte offset; bytes the
// bound dropped from the log's start are named (dropped_bytes, the actual
// offset served), an offset before them is not served as if it were there,
// and a page never splits a character. Threat: a paged log that silently
// skips or garbles text. Boundary: sandboxEnvironmentLogPage, which every
// get_environment log read (here and over the link) uses. Narrowest layer:
// the pure paging function.
func TestSandboxEnvironmentLogPageNamesDroppedBytesAndKeepsCharacters(t *testing.T) {
	log := strings.Repeat("é", sandboxEnvironmentLogPageBytes) // 2 bytes each
	dropped := 1000
	zero := 0
	text, page := sandboxEnvironmentLogPage(log, dropped, &zero, true)
	if page["offset"] != dropped || page["dropped_bytes"] != dropped || page["requested_offset"] != 0 || page["complete"] != false {
		t.Fatalf("offset before the kept log: %v", page)
	}
	if len(text) != sandboxEnvironmentLogPageBytes || page["next_offset"] != dropped+sandboxEnvironmentLogPageBytes || page["more"] != true {
		t.Fatalf("first page: %d bytes %v", len(text), page)
	}
	// An offset inside a character starts at the next whole one.
	odd := dropped + 1
	text, page = sandboxEnvironmentLogPage(log, dropped, &odd, false)
	if page["offset"] != dropped+2 || !strings.HasPrefix(text, "é") || strings.ContainsRune(text, '�') {
		t.Fatalf("page inside a character: %v %q", page, text[:4])
	}
	// The default page is the last one and reaches the end.
	text, page = sandboxEnvironmentLogPage(log, dropped, nil, false)
	if page["next_offset"] != dropped+len(log) || page["more"] != false || !strings.HasSuffix(log, text) || page["complete"] != true {
		t.Fatalf("last page: %v", page)
	}
}

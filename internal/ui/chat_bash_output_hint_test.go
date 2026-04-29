package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestBashPreviewReplacesEllipsizedLineWithOutputHint(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		State:    "done",
		Output:   strings.Repeat("very long bash output ", 12),
	}

	lines := toolPreviewLines(entry, 80, 5)
	if len(lines) == 0 {
		t.Fatal("expected bash preview lines")
	}
	if got := lines[len(lines)-1]; got != "write /output to see full output" {
		t.Fatalf("last preview line = %q, want /output hint; lines=%q", got, lines)
	}
	for _, line := range lines {
		if strings.HasSuffix(line, "...") {
			t.Fatalf("preview must not end with raw ellipsis when /output is available: %q in %q", line, lines)
		}
	}
}

func TestBashPreviewUsesOutputHintWhenTailDropsLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	entry := chatToolStreamEntry{
		ToolName: "bash",
		State:    "done",
		Output:   b.String(),
	}

	lines := toolPreviewLines(entry, 80, 5)
	if len(lines) != 5 {
		t.Fatalf("line count = %d, want 5; lines=%q", len(lines), lines)
	}
	if got := lines[len(lines)-1]; got != "write /output to see full output" {
		t.Fatalf("last preview line = %q, want /output hint; lines=%q", got, lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "30") {
		t.Fatalf("expected final dropped preview line to be replaced by hint, got %q", lines)
	}
}

func TestBashPreviewDoesNotShowOutputHintWhenComplete(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		State:    "done",
		Output:   "1\n2\n3\n",
	}

	lines := toolPreviewLines(entry, 80, 5)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "/output") {
		t.Fatalf("unexpected /output hint for complete preview: %q", lines)
	}
	if joined != "1\n2\n3" {
		t.Fatalf("preview = %q, want count output", joined)
	}
}

func TestBashUnifiedEntryPreservesOutputHint(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		State:    "done",
		Output:   strings.Repeat("lorem ipsum dolor sit amet ", 80),
	}

	formatted := formatUnifiedToolEntry(entry)
	if !strings.Contains(formatted, "write /output to see full output") {
		t.Fatalf("formatted entry missing /output hint:\n%s", formatted)
	}
	lastLine := formatted[strings.LastIndex(formatted, "\n")+1:]
	if strings.HasSuffix(lastLine, "...") {
		t.Fatalf("formatted entry still ends with ellipsis instead of /output hint:\n%s", formatted)
	}
}

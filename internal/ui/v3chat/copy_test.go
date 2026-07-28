package v3chat

import (
	"strings"
	"testing"
)

func TestPageCopyBlockTextUsesV3AssistantTimeline(t *testing.T) {
	store := NewStore()
	store.state = State{
		Messages: []Message{
			{ID: "user", Role: "user", Content: "<copy>ignore user blocks</copy>", GlobalSeq: 1},
			{ID: "assistant-1", Role: "assistant", Content: "First\n<copy label=\"restart\">swarm restart</copy>", GlobalSeq: 2},
			{ID: "assistant-2", Role: "assistant", Content: "```html\n<copy>literal example</copy>\n```\n<copy>swarm status</copy>", GlobalSeq: 3},
		},
		Live: map[string]LiveSegment{"run": {Text: "<copy>swarm logs</copy>", GlobalSeq: 4}},
	}
	page := NewPage(NewRuntime(nil, store, nil), PageStyles{})

	for index, want := range []string{"swarm restart", "swarm status", "swarm logs"} {
		got, ok := page.CopyBlockText(index + 1)
		if !ok || got != want {
			t.Fatalf("CopyBlockText(%d) = %q, %v; want %q, true", index+1, got, ok, want)
		}
	}
	if _, ok := page.CopyBlockText(4); ok {
		t.Fatal("CopyBlockText(4) unexpectedly found a block")
	}
}

func TestPageCopyBlockSuggestionsAndRenderingUseV3State(t *testing.T) {
	store := NewStore()
	store.state = State{Messages: []Message{{ID: "assistant", Role: "assistant", Content: "Before\n<copy label=\"restart\">swarm restart</copy>\nAfter", GlobalSeq: 1}}}
	page := NewPage(NewRuntime(nil, store, nil), PageStyles{})
	page.SetCommandSuggestions([]CommandSuggestion{{Command: "/copy", Hint: "Copy chat snapshot or /copy N block to clipboard"}})
	page.input = []rune("/copy")

	matches := page.commandPaletteMatchesLocked()
	if len(matches) != 2 || matches[0].Command != "/copy" || matches[1].Command != "/copy 1" || matches[1].Hint != "swarm restart" {
		t.Fatalf("copy palette matches = %#v", matches)
	}

	rows := page.renderRows(store.Snapshot(), 80, PageStyles{})
	text := renderRowsText(rows)
	for _, want := range []string{"Before", "/copy 1 · restart", "swarm restart", "After"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered rows missing %q:\n%s", want, text)
		}
	}
}

func TestPageClipboardTextUsesV3SessionSnapshot(t *testing.T) {
	store := NewStore()
	store.state = State{
		Session:  Session{ID: "session-v3", Title: "V3 copy", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: "auto"},
		Messages: []Message{{ID: "user", Role: "user", Content: "hello", CreatedAt: 1}},
	}
	page := NewPage(NewRuntime(nil, store, nil), PageStyles{})
	got := page.ClipboardText()
	for _, want := range []string{"session_title: V3 copy", "session_id: session-v3", "path: /workspace", "timeline_messages: 1", "hello"} {
		if !strings.Contains(got, want) {
			t.Fatalf("clipboard snapshot missing %q:\n%s", want, got)
		}
	}
}

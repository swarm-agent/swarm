package ui

import (
	"strings"
	"testing"
)

func TestChatCommandPaletteIncludesCopyBlockCommands(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		CommandSuggestions: []CommandSuggestion{
			{Command: "/copy", Hint: "Copy chat snapshot"},
		},
	})
	page.appendMessage("assistant", "Here you go:\n\n<copy label=\"restart command\">swarm restart</copy>", 1)
	page.appendMessage("assistant", "Another:\n<copy>swarm status</copy>", 2)
	page.input = "/copy 1"
	page.inputCursor = len(page.input)

	matches := page.commandPaletteMatches()
	if len(matches) == 0 {
		t.Fatal("expected command palette matches")
	}
	if matches[0].Command != "/copy 1" {
		t.Fatalf("expected /copy 1 as first match, got %q", matches[0].Command)
	}
	if !strings.Contains(matches[0].Hint, "restart command") {
		t.Fatalf("expected copy block label in hint, got %q", matches[0].Hint)
	}

	selected, ok := page.selectedCommandSuggestion()
	if !ok {
		t.Fatal("expected selected command suggestion")
	}
	if selected.Command != "/copy 1" {
		t.Fatalf("expected selected /copy 1, got %q", selected.Command)
	}
}

func TestChatCommandPaletteIncludesLiveCopyBlockCommands(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		CommandSuggestions: []CommandSuggestion{
			{Command: "/copy", Hint: "Copy chat snapshot"},
		},
	})
	page.liveAssistant = "Streaming:\n<copy>swarm logs</copy>"
	page.input = "/copy 1"
	page.inputCursor = len(page.input)

	selected, ok := page.selectedCommandSuggestion()
	if !ok {
		t.Fatal("expected selected command suggestion")
	}
	if selected.Command != "/copy 1" {
		t.Fatalf("expected selected live /copy 1, got %q", selected.Command)
	}
}

func TestChatCopyBlocksIgnoreMarkdownCodeFences(t *testing.T) {
	page := NewChatPage(ChatPageOptions{})
	message := "Example fenced HTML:\n\n```html\n<copy label=\"literal\">not a tui copy block</copy>\n```\n\n<copy label=\"real\">swarm status</copy>"
	page.appendMessage("assistant", message, 1)

	if count := countChatCopyBlocks(message); count != 1 {
		t.Fatalf("expected only the real copy tag to be indexed, got %d", count)
	}
	copyText, ok := page.CopyBlockText(1)
	if !ok {
		t.Fatal("expected real copy block")
	}
	if copyText != "swarm status" {
		t.Fatalf("expected real copy block content, got %q", copyText)
	}

	lines := page.renderAssistantMessageLines(chatMessageItem{Role: "assistant", Text: message, CreatedAt: 1}, 100)
	rendered := chatRenderLinesText(lines)
	if strings.Contains(rendered, "/copy 1 · literal") {
		t.Fatalf("fenced literal copy tag rendered as tui copy block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<copy label=\"literal\">not a tui copy block</copy>") {
		t.Fatalf("fenced literal copy tag missing from markdown render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "/copy 1 · real") {
		t.Fatalf("real copy block marker missing from render:\n%s", rendered)
	}
}

func TestChatCopyBlocksLeaveUnclosedTagsInMarkdown(t *testing.T) {
	message := "Literal HTML-ish text: <copy label=\"example\">not closed"
	segments := splitChatCopySegments(message)
	if chatCopySegmentsContainBlock(segments) {
		t.Fatalf("unclosed copy tag should remain markdown text, got %#v", segments)
	}
	if got := chatCopySegmentsText(segments); got != message {
		t.Fatalf("expected original text, got %q", got)
	}
}

func chatCopySegmentsText(segments []chatCopySegment) string {
	var out strings.Builder
	for _, segment := range segments {
		out.WriteString(segment.Text)
		if segment.Copy != nil {
			out.WriteString(segment.Copy.Content)
		}
	}
	return out.String()
}

func chatRenderLinesText(lines []chatRenderLine) string {
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(chatRenderLineText(line))
	}
	return out.String()
}

package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/copyblock"
)

type chatCopyBlock = copyblock.Block
type chatCopySegment = copyblock.Segment

func splitChatCopySegments(text string) []chatCopySegment { return copyblock.Split(text) }

func normalizeChatCopyContent(content string) string { return copyblock.Normalize(content) }

func chatCopyCommandPreview(content string) string { return copyblock.CommandPreview(content) }

func countChatCopyBlocks(text string) int { return copyblock.Count(text) }

func chatCopyBlockMessageMatch(left, right chatMessageItem) bool {
	leftID := strings.TrimSpace(left.MessageID)
	if leftID != "" {
		return leftID == strings.TrimSpace(right.MessageID)
	}
	return strings.EqualFold(strings.TrimSpace(left.Role), strings.TrimSpace(right.Role)) &&
		left.CreatedAt == right.CreatedAt &&
		left.Text == right.Text
}

func (p *ChatPage) copyBlockBaseIndexForMessage(message chatMessageItem) int {
	if p == nil {
		return 0
	}
	count := 0
	for _, item := range p.timeline {
		if chatCopyBlockMessageMatch(item, message) {
			return count
		}
		if strings.EqualFold(strings.TrimSpace(item.Role), "assistant") {
			count += countChatCopyBlocks(item.Text)
		}
	}
	return count
}

func (p *ChatPage) CopyBlockText(index int) (string, bool) {
	block, ok := p.copyBlockAt(index)
	if !ok {
		return "", false
	}
	return block.Content, true
}

func (p *ChatPage) copyBlockAt(index int) (chatCopyBlock, bool) {
	if p == nil || index <= 0 {
		return chatCopyBlock{}, false
	}
	current := 0
	for _, item := range p.timeline {
		if !strings.EqualFold(strings.TrimSpace(item.Role), "assistant") {
			continue
		}
		for _, segment := range splitChatCopySegments(item.Text) {
			if segment.Copy == nil {
				continue
			}
			current++
			if current == index {
				return *segment.Copy, true
			}
		}
	}
	if strings.TrimSpace(p.liveAssistant) != "" {
		for _, segment := range splitChatCopySegments(p.liveAssistant) {
			if segment.Copy == nil {
				continue
			}
			current++
			if current == index {
				return *segment.Copy, true
			}
		}
	}
	return chatCopyBlock{}, false
}

func (p *ChatPage) copyBlockCommandSuggestions() []CommandSuggestion {
	if p == nil {
		return nil
	}
	suggestions := make([]CommandSuggestion, 0, 4)
	current := 0
	appendBlocks := func(text string) {
		for _, segment := range splitChatCopySegments(text) {
			if segment.Copy == nil {
				continue
			}
			current++
			hint := chatCopyCommandPreview(segment.Copy.Content)
			suggestions = append(suggestions, CommandSuggestion{
				Command:    fmt.Sprintf("/copy %d", current),
				Hint:       hint,
				InlineHint: true,
			})
		}
	}
	for _, item := range p.timeline {
		if !strings.EqualFold(strings.TrimSpace(item.Role), "assistant") {
			continue
		}
		appendBlocks(item.Text)
	}
	if strings.TrimSpace(p.liveAssistant) != "" {
		appendBlocks(p.liveAssistant)
	}
	return suggestions
}

func (p *ChatPage) renderAssistantCopyAwareMessageLines(firstPrefix, continuationPrefix, body string, width int, baseStyle tcell.Style, message chatMessageItem) []chatRenderLine {
	segments := splitChatCopySegments(body)
	if len(segments) == 0 || !chatCopySegmentsContainBlock(segments) {
		return p.renderAssistantMarkdownMessageLines(firstPrefix, continuationPrefix, body, width, baseStyle)
	}

	out := make([]chatRenderLine, 0, len(segments)*3)
	firstLine := true
	baseIndex := p.copyBlockBaseIndexForMessage(message)
	copyOffset := 0
	for _, segment := range segments {
		if segment.Copy == nil {
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			prefix := continuationPrefix
			if firstLine {
				prefix = firstPrefix
			}
			lines := p.renderAssistantMarkdownMessageLines(prefix, continuationPrefix, text, width, baseStyle)
			out = append(out, lines...)
			if len(lines) > 0 {
				firstLine = false
			}
			continue
		}
		copyOffset++
		prefix := continuationPrefix
		if firstLine {
			prefix = firstPrefix
		}
		lines := p.renderCopyBlockLines(baseIndex+copyOffset, segment.Copy.Label, segment.Copy.Content, prefix, continuationPrefix, width)
		out = append(out, lines...)
		if len(lines) > 0 {
			firstLine = false
		}
	}
	if len(out) == 0 {
		return p.renderAssistantMarkdownMessageLines(firstPrefix, continuationPrefix, body, width, baseStyle)
	}
	return out
}

func chatCopySegmentsContainBlock(segments []chatCopySegment) bool {
	for _, segment := range segments {
		if segment.Copy != nil {
			return true
		}
	}
	return false
}

func (p *ChatPage) renderCopyBlockLines(index int, label, content, firstPrefix, continuationPrefix string, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	if index <= 0 {
		index = 1
	}
	label = strings.TrimSpace(label)
	header := fmt.Sprintf("%s/copy %d", firstPrefix, index)
	if label != "" {
		header += " · " + label
	}
	out := []chatRenderLine{{Text: clampEllipsis(header, width), Style: p.theme.Accent.Bold(true)}}

	preview := chatCopyPreviewLines(content, 8)
	if len(preview) == 0 {
		preview = []string{"(empty copy block)"}
	}
	linePrefix := continuationPrefix + "  │ "
	for _, line := range preview {
		if line == "" {
			out = append(out, chatRenderLine{Text: clampEllipsis(linePrefix, width), Style: p.theme.MarkdownCode})
			continue
		}
		for _, wrapped := range wrapWithPrefix(linePrefix, line, width) {
			out = append(out, chatRenderLine{Text: wrapped, Style: p.theme.MarkdownCode})
		}
	}
	return out
}

func chatCopyPreviewLines(content string, maxLines int) []string {
	content = normalizeChatCopyContent(content)
	if strings.TrimSpace(content) == "" || maxLines <= 0 {
		return nil
	}
	parts := strings.Split(content, "\n")
	out := make([]string, 0, minInt(len(parts), maxLines))
	for i, line := range parts {
		if i >= maxLines {
			remaining := len(parts) - maxLines
			out = append(out, fmt.Sprintf("… %d more %s", remaining, pluralizeCopyLine(remaining)))
			break
		}
		out = append(out, line)
	}
	return out
}

func pluralizeCopyLine(count int) string {
	if count == 1 {
		return "line"
	}
	return "lines"
}

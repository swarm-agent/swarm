package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

var chatAssistantVariantNames = []string{
	"classic",
	"bold",
	"soft",
	"rail",
	"timestamp",
	"tag",
	"bubble",
	"quote",
	"compact",
	"framed",
}

var chatUserVariantNames = []string{
	"muted",
	"soft",
	"plain",
	"rail",
	"timestamp",
	"tag",
	"bubble",
	"quote",
	"compact",
	"framed",
}

func (p *ChatPage) userVariantName() string {
	if len(chatUserVariantNames) == 0 {
		return "muted"
	}
	idx := p.userVariant % len(chatUserVariantNames)
	if idx < 0 {
		idx += len(chatUserVariantNames)
	}
	return chatUserVariantNames[idx]
}

func (p *ChatPage) assistantVariantName() string {
	if len(chatAssistantVariantNames) == 0 {
		return "classic"
	}
	idx := p.assistantVariant % len(chatAssistantVariantNames)
	if idx < 0 {
		idx += len(chatAssistantVariantNames)
	}
	return chatAssistantVariantNames[idx]
}

func (p *ChatPage) renderUserMessageLines(message chatMessageItem, width int) []chatRenderLine {
	body := strings.TrimSpace(message.Text)
	if body == "" {
		body = " "
	}
	variant := normalizeVariant(p.userVariant, chatUserVariantCount)
	switch variant {
	case 1:
		return styledWrapped("› ", "", body, width, p.theme.TextMuted)
	case 2:
		return styledWrapped("> ", "", body, width, p.theme.Secondary.Dim(true))
	case 3:
		return styledWrapped("> ", "│ ", body, width, p.theme.TextMuted)
	case 4:
		prefix := fmt.Sprintf("> %s ", formatMessageClock(message.CreatedAt))
		return styledWrapped(prefix, "  ", body, width, p.theme.TextMuted)
	case 5:
		return styledWrapped("[u] ", "    ", body, width, p.theme.TextMuted)
	case 6:
		return bubbleWrappedWithTitle(body, width, p.theme.TextMuted, "╭─> user")
	case 7:
		return styledWrapped("> » ", "  » ", body, width, p.theme.TextMuted)
	case 8:
		return styledWrapped(">· ", "   ", body, width, p.theme.TextMuted.Dim(true))
	case 9:
		return styledWrapped("▣ user ", "▣ ", body, width, p.theme.Secondary.Dim(true))
	default:
		return styledWrapped("> ", "", body, width, p.theme.TextMuted.Dim(true))
	}
}

func (p *ChatPage) renderAssistantMessageLines(message chatMessageItem, width int) []chatRenderLine {
	body := strings.TrimSpace(message.Text)
	if body == "" {
		body = " "
	}
	if label := assistantTimelineLabel(message.Metadata); label != "" {
		body = label + "\n" + body
	}
	variant := normalizeVariant(p.assistantVariant, chatAssistantVariantCount)

	switch variant {
	case 1:
		return p.renderAssistantMarkdownMessageLines("□ ", "", body, width, p.theme.Accent.Bold(true))
	case 2:
		return p.renderAssistantMarkdownMessageLines("▢ ", "", body, width, p.theme.Accent)
	case 3:
		return p.renderAssistantMarkdownMessageLines("□ ", "│ ", body, width, p.theme.Accent)
	case 4:
		prefix := fmt.Sprintf("□ %s ", formatMessageClock(message.CreatedAt))
		return p.renderAssistantMarkdownMessageLines(prefix, "  ", body, width, p.theme.Accent)
	case 5:
		return p.renderAssistantMarkdownMessageLines("[□] ", "    ", body, width, p.theme.Accent)
	case 6:
		return p.renderAssistantMarkdownBubble(body, width, p.theme.Accent, "╭─□ assistant")
	case 7:
		return p.renderAssistantMarkdownMessageLines("□ » ", "  » ", body, width, p.theme.Accent)
	case 8:
		return p.renderAssistantMarkdownMessageLines("□· ", "   ", body, width, p.theme.Accent)
	case 9:
		return p.renderAssistantMarkdownMessageLines("▣ assistant ", "▣ ", body, width, p.theme.Accent.Bold(true))
	default:
		return p.renderAssistantMarkdownMessageLines("□ ", "", body, width, p.theme.Accent)
	}
}

func assistantTimelineLabel(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	source, _ := metadata["source"].(string)
	lineageKind, _ := metadata["lineage_kind"].(string)
	lineageLabel, _ := metadata["lineage_label"].(string)
	subagent, _ := metadata["subagent"].(string)
	backgroundAgent, _ := metadata["background_agent"].(string)
	targetName, _ := metadata["target_name"].(string)
	if !strings.EqualFold(strings.TrimSpace(source), "targeted_subagent") &&
		!strings.EqualFold(strings.TrimSpace(lineageKind), "delegated_subagent") &&
		!strings.EqualFold(strings.TrimSpace(lineageKind), "background_agent") &&
		strings.TrimSpace(lineageLabel) == "" &&
		strings.TrimSpace(subagent) == "" &&
		strings.TrimSpace(backgroundAgent) == "" &&
		strings.TrimSpace(targetName) == "" {
		return ""
	}
	name := strings.TrimSpace(subagent)
	if name == "" {
		name = strings.TrimSpace(backgroundAgent)
	}
	if name == "" {
		name = strings.TrimSpace(lineageLabel)
		name = strings.TrimPrefix(name, "@")
		name = strings.TrimSpace(name)
	}
	if name == "" {
		name = strings.TrimSpace(targetName)
	}
	if name == "" {
		return "@subagent"
	}
	return "@" + name
}

func styledWrapped(firstPrefix, continuationPrefix, body string, width int, style tcell.Style) []chatRenderLine {
	lines := wrapWithCustomPrefixes(firstPrefix, continuationPrefix, body, width)
	out := make([]chatRenderLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, chatRenderLine{Text: line, Style: style})
	}
	return out
}

func bubbleWrappedWithTitle(body string, width int, style tcell.Style, title string) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	lines := []chatRenderLine{
		{Text: clampEllipsis(title, width), Style: style},
	}
	for _, line := range wrapWithCustomPrefixes("│ ", "│ ", body, width) {
		lines = append(lines, chatRenderLine{Text: line, Style: style})
	}
	lines = append(lines, chatRenderLine{Text: clampEllipsis("╰", width), Style: style})
	return lines
}

func wrapWithCustomPrefixes(firstPrefix, continuationPrefix, body string, width int) []string {
	if width <= 0 {
		return nil
	}
	if continuationPrefix == "" {
		continuationPrefix = strings.Repeat(" ", utf8.RuneCountInString(firstPrefix))
	}
	if body == "" {
		return []string{clampEllipsis(firstPrefix, width)}
	}

	parts := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(parts)*2)
	firstSegment := true
	for _, part := range parts {
		prefix := continuationPrefix
		if firstSegment {
			prefix = firstPrefix
		}
		if part == "" {
			// Preserve explicit paragraph breaks while keeping the active gutter/prefix.
			out = append(out, prefix)
			firstSegment = false
			continue
		}
		out = append(out, wrapSingleLineWithCustomPrefixes(prefix, continuationPrefix, part, width)...)
		firstSegment = false
	}
	if len(out) == 0 {
		return []string{clampEllipsis(firstPrefix, width)}
	}
	return out
}

func wrapSingleLineWithCustomPrefixes(firstPrefix, continuationPrefix, body string, width int) []string {
	if width <= 0 {
		return nil
	}
	firstPrefixRunes := []rune(firstPrefix)
	firstPrefixW := len(firstPrefixRunes)
	if firstPrefixW >= width {
		return []string{string(firstPrefixRunes[:width])}
	}

	body = strings.ReplaceAll(body, "\t", " ")
	if body == "" {
		return []string{firstPrefix}
	}

	availableFirst := maxInt(1, width-firstPrefixW)
	continuationRunes := []rune(continuationPrefix)
	continuationW := len(continuationRunes)
	if continuationW >= width {
		continuationPrefix = strings.Repeat(" ", width-1)
		continuationW = width - 1
	}
	availableContinuation := maxInt(1, width-continuationW)

	lines := make([]string, 0, 4)
	remaining := body
	prefix := firstPrefix
	available := availableFirst
	for remaining != "" {
		if utf8.RuneCountInString(remaining) <= available {
			lines = append(lines, prefix+remaining)
			break
		}
		head, tail := wrapWordAwareChunk(remaining, available)
		if head == "" {
			runes := []rune(remaining)
			n := minInt(available, len(runes))
			head = string(runes[:n])
			tail = string(runes[n:])
		}
		lines = append(lines, prefix+head)
		remaining = tail
		prefix = continuationPrefix
		available = availableContinuation
	}
	if len(lines) == 0 {
		return []string{firstPrefix}
	}
	return lines
}

func wrapWordAwareChunk(text string, width int) (string, string) {
	if width <= 0 || text == "" {
		return "", text
	}
	text = strings.ReplaceAll(text, "\t", " ")
	runes := []rune(text)
	if len(runes) <= width {
		return text, ""
	}
	headEnd, tailStart := wrapLineBreakIndicesRunes(runes, width)
	if headEnd <= 0 || headEnd > len(runes) {
		headEnd = minInt(width, len(runes))
	}
	if tailStart < headEnd || tailStart > len(runes) {
		tailStart = headEnd
	}
	return string(runes[:headEnd]), string(runes[tailStart:])
}

func formatMessageClock(createdAt int64) string {
	if createdAt <= 0 {
		return "--:--"
	}
	return time.UnixMilli(createdAt).Format("15:04")
}

func normalizeVariant(variant, count int) int {
	if count <= 0 {
		return 0
	}
	next := variant % count
	if next < 0 {
		next += count
	}
	return next
}

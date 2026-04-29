package ui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type chatCopyBlock struct {
	Label   string
	Content string
}

type chatCopySegment struct {
	Text string
	Copy *chatCopyBlock
}

var (
	chatCopyOpenTagPattern = regexp.MustCompile(`(?is)<copy(?:\s+[^>]*)?>`)
	chatCopyAttrPattern    = regexp.MustCompile(`(?is)\b(?:label|title|name)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func splitChatCopySegments(text string) []chatCopySegment {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}

	segments := make([]chatCopySegment, 0, 4)
	remaining := text
	for remaining != "" {
		loc := chatCopyOpenTagPattern.FindStringIndex(remaining)
		if loc == nil {
			segments = appendCopyTextSegment(segments, remaining)
			break
		}
		if loc[0] > 0 {
			segments = appendCopyTextSegment(segments, remaining[:loc[0]])
		}

		openTag := remaining[loc[0]:loc[1]]
		afterOpen := remaining[loc[1]:]
		lowerAfterOpen := strings.ToLower(afterOpen)
		closeIdx := strings.Index(lowerAfterOpen, "</copy>")
		content := afterOpen
		if closeIdx >= 0 {
			content = afterOpen[:closeIdx]
			remaining = afterOpen[closeIdx+len("</copy>"):]
		} else {
			remaining = ""
		}

		segments = append(segments, chatCopySegment{Copy: &chatCopyBlock{
			Label:   chatCopyTagLabel(openTag),
			Content: normalizeChatCopyContent(content),
		}})
	}
	return segments
}

func appendCopyTextSegment(segments []chatCopySegment, text string) []chatCopySegment {
	if text == "" {
		return segments
	}
	return append(segments, chatCopySegment{Text: text})
}

func chatCopyTagLabel(openTag string) string {
	match := chatCopyAttrPattern.FindStringSubmatch(openTag)
	if len(match) == 0 {
		return ""
	}
	for _, candidate := range match[2:] {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func normalizeChatCopyContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.Trim(content, "\n")
	return content
}

func countChatCopyBlocks(text string) int {
	count := 0
	for _, segment := range splitChatCopySegments(text) {
		if segment.Copy != nil {
			count++
		}
	}
	return count
}

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
	if p == nil || index <= 0 {
		return "", false
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
				return segment.Copy.Content, true
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
				return segment.Copy.Content, true
			}
		}
	}
	return "", false
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

func ParseCopyBlockIndexArg(args []string) (int, bool) {
	if len(args) != 1 {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || index <= 0 {
		return 0, false
	}
	return index, true
}

func CopyBlockPreviewStatus(index int, text string) string {
	first := strings.TrimSpace(strings.Split(normalizeChatCopyContent(text), "\n")[0])
	if first == "" {
		return fmt.Sprintf("copied /copy %d", index)
	}
	if utf8.RuneCountInString(first) > 48 {
		first = clampEllipsis(first, 48)
	}
	return fmt.Sprintf("copied /copy %d: %s", index, first)
}

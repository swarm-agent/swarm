package ui

import (
	"strings"
	"time"
)

func (p *ChatPage) liveAssistantParseFallbackLines(width int) []chatRenderLine {
	if p == nil || width <= 0 {
		return nil
	}
	text := p.liveAssistant
	if text == "" {
		return nil
	}
	if p.liveRunVisible() {
		return p.renderLiveAssistantStreamingPreviewLines(text, width)
	}
	message := chatMessageItem{
		Role:      "assistant",
		Text:      text,
		CreatedAt: time.Now().UnixMilli(),
	}
	lines, _ := p.renderLiveAssistantMessageLines(message, width, nil)
	return lines
}

func (p *ChatPage) renderLiveAssistantMessageLines(message chatMessageItem, width int, _ []chatRenderLine) (lines []chatRenderLine, recovered bool) {
	defer func() {
		if recover() != nil {
			recovered = true
			lines = p.renderLiveAssistantEmergencyLines(message, width)
		}
	}()
	if p.liveRunVisible() {
		return p.renderLiveAssistantStreamingLines(message, width), false
	}
	return p.renderAssistantMessageLines(message, width), false
}

const chatMaxLiveAssistantMarkdownRenderBytes = 8 * 1024

func (p *ChatPage) renderLiveAssistantStreamingLines(message chatMessageItem, width int) []chatRenderLine {
	return p.renderLiveAssistantStreamingPreviewLines(message.Text, width)
}

func (p *ChatPage) renderLiveAssistantStreamingPreviewLines(text string, width int) []chatRenderLine {
	if p == nil || width <= 0 {
		return nil
	}
	body := liveRenderTail(text, chatMaxLiveAssistantMarkdownRenderBytes)
	if body == "" {
		body = " "
	}
	return p.renderAssistantPlainPreviewMessageLines(body, width)
}

func (p *ChatPage) renderAssistantPlainPreviewMessageLines(body string, width int) []chatRenderLine {
	if p == nil || width <= 0 {
		return nil
	}
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		body = " "
	}
	variant := normalizeVariant(p.assistantVariant, chatAssistantVariantCount)
	switch variant {
	case 1:
		return styledWrapped("□ ", "", body, width, p.theme.Accent.Bold(true))
	case 2:
		return styledWrapped("▢ ", "", body, width, p.theme.Accent)
	case 3:
		return styledWrapped("□ ", "│ ", body, width, p.theme.Accent)
	case 4:
		prefix := "□ " + formatMessageClock(time.Now().UnixMilli()) + " "
		return styledWrapped(prefix, "  ", body, width, p.theme.Accent)
	case 5:
		return styledWrapped("[□] ", "    ", body, width, p.theme.Accent)
	case 6:
		return bubbleWrappedWithTitle(body, width, p.theme.Accent, "╭─□ assistant")
	case 7:
		return styledWrapped("□ » ", "  » ", body, width, p.theme.Accent)
	case 8:
		return styledWrapped("□· ", "   ", body, width, p.theme.Accent)
	case 9:
		return styledWrapped("▣ assistant ", "▣ ", body, width, p.theme.Accent.Bold(true))
	default:
		return styledWrapped("▢ ", "", body, width, p.theme.Accent)
	}
}

func liveRenderTail(text string, maxBytes int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	if idx := strings.IndexByte(text[start:], '\n'); idx >= 0 && start+idx+1 < len(text) {
		start += idx + 1
	}
	return "… live stream truncated; full rich render after completion …\n" + text[start:]
}

func (p *ChatPage) renderLiveAssistantEmergencyLines(message chatMessageItem, width int) []chatRenderLine {
	body := message.Text
	if body == "" {
		body = " "
	}
	return styledWrapped("▢ ", "", body, width, p.theme.Accent)
}

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
	body := liveRenderTail(message.Text, chatMaxLiveAssistantMarkdownRenderBytes)
	if body == "" {
		body = " "
	}
	streamMessage := message
	streamMessage.Text = body
	return p.renderAssistantMessageLines(streamMessage, width)
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

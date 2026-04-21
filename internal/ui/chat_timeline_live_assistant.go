package ui

import (
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
	return p.renderAssistantMessageLines(message, width), false
}

func (p *ChatPage) renderLiveAssistantEmergencyLines(message chatMessageItem, width int) []chatRenderLine {
	body := message.Text
	if body == "" {
		body = " "
	}
	return styledWrapped("▢ ", "", body, width, p.theme.Accent)
}

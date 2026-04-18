package ui

import (
	"fmt"
	"strings"
	"time"
)

// ClipboardText returns a user-facing chat snapshot suitable for quick sharing.
func (p *ChatPage) ClipboardText() string {
	if p == nil {
		return ""
	}

	lines := make([]string, 0, 64+len(p.timeline)*3)
	lines = append(lines, "swarm chat snapshot")
	lines = append(lines, fmt.Sprintf("captured_at: %s", time.Now().UTC().Format(time.RFC3339)))
	lines = append(lines, fmt.Sprintf("session_title: %s", chatClipboardTextValue(p.sessionTitle, "-")))
	lines = append(lines, fmt.Sprintf("session_id: %s", chatClipboardTextValue(p.sessionID, "-")))
	lines = append(lines, fmt.Sprintf("mode: %s", chatClipboardTextValue(currentDisplayedSessionMode(p), "auto")))
	lines = append(lines, fmt.Sprintf("agent: %s", chatClipboardTextValue(p.meta.Agent, "-")))
	lines = append(lines, fmt.Sprintf("workspace: %s", chatClipboardTextValue(p.meta.Workspace, "-")))
	lines = append(lines, fmt.Sprintf("path: %s", chatClipboardTextValue(p.meta.Path, ".")))
	lines = append(lines, fmt.Sprintf("branch: %s", chatClipboardTextValue(p.meta.Branch, "-")))
	lines = append(lines, fmt.Sprintf("dirty_files: %d", p.meta.Dirty))

	model := strings.TrimSpace(p.modelName)
	if provider := strings.TrimSpace(p.modelProvider); provider != "" {
		if model == "" {
			model = provider
		} else {
			model = provider + "/" + model
		}
	}
	lines = append(lines, fmt.Sprintf("model: %s", chatClipboardTextValue(model, "-")))
	lines = append(lines, fmt.Sprintf("thinking: %s", chatClipboardTextValue(p.thinkingLevel, "-")))
	lines = append(lines, fmt.Sprintf("status: %s", chatClipboardTextValue(p.statusLine, "-")))
	if errLine := strings.TrimSpace(p.errorLine); errLine != "" {
		lines = append(lines, fmt.Sprintf("error: %s", errLine))
	}

	if p.contextUsageSet && p.contextWindow > 0 {
		lines = append(lines, fmt.Sprintf("context_remaining_tokens: %d", p.contextRemain))
		lines = append(lines, fmt.Sprintf("context_window_tokens: %d", p.contextWindow))
	}

	if len(p.pendingPerms) > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("pending_permissions: %d", len(p.pendingPerms)))
		for i, record := range p.pendingPerms {
			tool := chatClipboardTextValue(record.ToolName, "tool")
			status := chatClipboardTextValue(record.Status, "pending")
			lines = append(lines, fmt.Sprintf("%d. %s [%s] id=%s", i+1, tool, status, chatClipboardTextValue(record.ID, "-")))
		}
	}

	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("timeline_messages: %d", len(p.timeline)))
	for i, item := range p.timeline {
		role := chatClipboardTextValue(item.Role, "system")
		lines = append(lines, fmt.Sprintf("%d. [%s] %s", i+1, chatClipboardTimestamp(item.CreatedAt), role))
		lines = append(lines, chatClipboardIndentedLines(item.Text, "   ")...)
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func chatClipboardTextValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func chatClipboardTimestamp(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func chatClipboardIndentedLines(text, indent string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return []string{indent + "(empty)"}
	}
	parts := strings.Split(text, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, indent+part)
	}
	return out
}

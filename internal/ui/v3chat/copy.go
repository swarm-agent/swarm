package v3chat

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/copyblock"
)

type copyContentItem struct {
	seq       uint64
	createdAt int64
	order     int
	text      string
}

// ClipboardText returns a user-facing snapshot built only from V3 store state.
func (p *Page) ClipboardText() string {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return ""
	}
	state := p.runtime.Store().Snapshot()
	p.mu.Lock()
	status := firstNonEmpty(p.errText, p.status)
	profileLabel := p.profileLabel
	p.mu.Unlock()

	lines := make([]string, 0, 64+len(state.Messages)*3)
	lines = append(lines, "swarm chat snapshot")
	lines = append(lines, fmt.Sprintf("captured_at: %s", time.Now().UTC().Format(time.RFC3339)))
	lines = append(lines, fmt.Sprintf("session_title: %s", clipboardValue(state.Session.Title, "-")))
	lines = append(lines, fmt.Sprintf("session_id: %s", clipboardValue(state.Session.ID, "-")))
	lines = append(lines, fmt.Sprintf("mode: %s", clipboardValue(state.Session.Mode, "auto")))
	lines = append(lines, fmt.Sprintf("workspace: %s", clipboardValue(state.Session.WorkspaceName, "-")))
	lines = append(lines, fmt.Sprintf("path: %s", clipboardValue(state.Session.WorkspacePath, ".")))

	model := strings.TrimSpace(state.Model.Preference.Model)
	if provider := strings.TrimSpace(state.Model.Preference.Provider); provider != "" {
		if model == "" {
			model = provider
		} else {
			model = provider + "/" + model
		}
	}
	lines = append(lines, fmt.Sprintf("model: %s", clipboardValue(model, "-")))
	lines = append(lines, fmt.Sprintf("profile: %s", clipboardValue(firstNonEmpty(state.Model.ProfileName, profileLabel), "-")))
	lines = append(lines, fmt.Sprintf("connection: %s", clipboardValue(string(state.Connection), "-")))
	lines = append(lines, fmt.Sprintf("status: %s", clipboardValue(status, "-")))
	if state.Usage.Available && state.Usage.ContextWindow > 0 {
		lines = append(lines, fmt.Sprintf("context_remaining_tokens: %d", state.Usage.RemainingTokens))
		lines = append(lines, fmt.Sprintf("context_window_tokens: %d", state.Usage.ContextWindow))
	}
	if pendingPermissions := SelectPendingPermissions(state); len(pendingPermissions) > 0 {
		lines = append(lines, "", fmt.Sprintf("pending_permissions: %d", len(pendingPermissions)))
		for i, record := range pendingPermissions {
			lines = append(lines, fmt.Sprintf("%d. %s [%s] id=%s", i+1, clipboardValue(record.ToolName, "tool"), clipboardValue(record.Status, "pending"), clipboardValue(record.ID, "-")))
		}
	}

	messages := SelectMessages(state)
	lines = append(lines, "", fmt.Sprintf("timeline_messages: %d", len(messages)))
	for i, message := range messages {
		lines = append(lines, fmt.Sprintf("%d. [%s] %s", i+1, clipboardTimestamp(message.CreatedAt), clipboardValue(message.Role, "system")))
		lines = append(lines, clipboardIndentedLines(message.Content, "   ")...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// CopyBlockText returns the one-based assistant copy block from the canonical
// V3 message/live timeline.
func (p *Page) CopyBlockText(index int) (string, bool) {
	if index <= 0 {
		return "", false
	}
	current := 0
	for _, item := range p.copyContentItems() {
		for _, segment := range copyblock.Split(item.text) {
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

func (p *Page) copyBlockCommandSuggestionsLocked() []CommandSuggestion {
	items := p.copyContentItems()
	suggestions := make([]CommandSuggestion, 0, 4)
	current := 0
	for _, item := range items {
		for _, segment := range copyblock.Split(item.text) {
			if segment.Copy == nil {
				continue
			}
			current++
			suggestions = append(suggestions, CommandSuggestion{
				Command: fmt.Sprintf("/copy %d", current),
				Hint:    copyblock.CommandPreview(segment.Copy.Content),
			})
		}
	}
	return suggestions
}

func (p *Page) copyContentItems() []copyContentItem {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return nil
	}
	state := p.runtime.Store().Snapshot()
	items := make([]copyContentItem, 0, len(state.Messages)+len(state.Live))
	for _, message := range SelectMessages(state) {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		if _, isTool := parseToolMessage(message); isTool {
			continue
		}
		items = append(items, copyContentItem{seq: message.GlobalSeq, createdAt: message.CreatedAt, order: len(items), text: message.Content})
	}
	for _, segment := range SelectLiveSegments(state) {
		items = append(items, copyContentItem{seq: segment.GlobalSeq, createdAt: segment.CreatedAt, order: len(items), text: segment.Text})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.seq != right.seq {
			if left.seq == 0 {
				return false
			}
			if right.seq == 0 {
				return true
			}
			return left.seq < right.seq
		}
		if left.createdAt != right.createdAt {
			return left.createdAt < right.createdAt
		}
		return left.order < right.order
	})
	return items
}

func (p *Page) renderCopyAwareAssistantRows(content string, baseIndex, width int, styles PageStyles) []renderRow {
	segments := copyblock.Split(content)
	if len(segments) == 0 || !copySegmentsContainBlock(segments) {
		return p.renderAssistantRows(content, width, styles)
	}
	rows := make([]renderRow, 0, len(segments)*3)
	copyOffset := 0
	for _, segment := range segments {
		if segment.Copy == nil {
			if text := strings.TrimSpace(segment.Text); text != "" {
				rows = append(rows, p.renderAssistantRows(text, width, styles)...)
			}
			continue
		}
		copyOffset++
		index := baseIndex + copyOffset
		header := fmt.Sprintf("/copy %d", index)
		if label := strings.TrimSpace(segment.Copy.Label); label != "" {
			header += " · " + label
		}
		rows = append(rows, renderRow{text: header, style: styles.Accent.Bold(true)})
		preview := copyPreviewLines(segment.Copy.Content, 8)
		if len(preview) == 0 {
			preview = []string{"(empty copy block)"}
		}
		for _, line := range preview {
			for _, wrapped := range wrapText("  │ "+line, width) {
				rows = append(rows, renderRow{text: wrapped, style: styles.Secondary})
			}
		}
	}
	return rows
}

func copyBlockCount(content string) int { return copyblock.Count(content) }

func copySegmentsContainBlock(segments []copyblock.Segment) bool {
	for _, segment := range segments {
		if segment.Copy != nil {
			return true
		}
	}
	return false
}

func copyPreviewLines(content string, maxLines int) []string {
	content = copyblock.Normalize(content)
	if strings.TrimSpace(content) == "" || maxLines <= 0 {
		return nil
	}
	parts := strings.Split(content, "\n")
	lines := make([]string, 0, minInt(len(parts), maxLines))
	for i, line := range parts {
		if i >= maxLines {
			remaining := len(parts) - maxLines
			label := "lines"
			if remaining == 1 {
				label = "line"
			}
			lines = append(lines, fmt.Sprintf("… %d more %s", remaining, label))
			break
		}
		lines = append(lines, line)
	}
	return lines
}

func clipboardValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func clipboardTimestamp(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func clipboardIndentedLines(text, indent string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return []string{indent + "(empty)"}
	}
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, indent+part)
	}
	return lines
}

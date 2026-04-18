package ui

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func normalizeMentionSubagents(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

func chatMentionCandidates(query string, subagents []string) []string {
	candidates := normalizeMentionSubagents(subagents)
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return candidates
	}
	prefixMatches := make([]string, 0, len(candidates))
	containsMatches := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate))
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, query) {
			prefixMatches = append(prefixMatches, candidate)
			continue
		}
		if strings.Contains(name, query) {
			containsMatches = append(containsMatches, candidate)
		}
	}
	return append(prefixMatches, containsMatches...)
}

func resolveMentionSubagent(name string, subagents []string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, candidate := range normalizeMentionSubagents(subagents) {
		if strings.EqualFold(strings.TrimSpace(candidate), name) {
			return candidate, true
		}
	}
	return "", false
}

func parseTargetedSubagentPrompt(prompt string, subagents []string) (string, string, bool) {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, "@") {
		return "", "", false
	}
	tokenEnd := len(trimmed)
	for i, r := range trimmed {
		if unicode.IsSpace(r) {
			tokenEnd = i
			break
		}
	}
	token := strings.TrimSpace(strings.TrimPrefix(trimmed[:tokenEnd], "@"))
	if token == "" {
		return "", "", false
	}
	task := strings.TrimSpace(trimmed[tokenEnd:])
	if task == "" {
		return "", "", false
	}
	target, ok := resolveMentionSubagent(token, subagents)
	if !ok {
		return "", "", false
	}
	return target, task, true
}

func mentionPaletteQuery(prompt string) string {
	raw := strings.TrimLeft(prompt, " \t\r\n")
	if !strings.HasPrefix(raw, "@") {
		return ""
	}
	raw = strings.TrimPrefix(raw, "@")
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fields[0]))
}

func mentionHasArgs(prompt string) bool {
	raw := strings.TrimLeft(prompt, " \t\r\n")
	if !strings.HasPrefix(raw, "@") {
		return false
	}
	return len(strings.Fields(strings.TrimPrefix(raw, "@"))) > 1
}

func (p *ChatPage) setMentionSubagents(items []string) {
	p.mentionSubagents = normalizeMentionSubagents(items)
	p.mentionPaletteIndex = 0
}

func (p *ChatPage) mentionPaletteActive() bool {
	if len(p.mentionSubagents) == 0 {
		return false
	}
	raw := strings.TrimLeft(p.input, " \t\r\n")
	if !strings.HasPrefix(raw, "@") {
		return false
	}
	return !mentionHasArgs(raw)
}

func (p *ChatPage) mentionPaletteMatches() []string {
	if !p.mentionPaletteActive() {
		return nil
	}
	return chatMentionCandidates(mentionPaletteQuery(p.input), p.mentionSubagents)
}

func (p *ChatPage) syncMentionPaletteSelection() []string {
	matches := p.mentionPaletteMatches()
	if len(matches) == 0 {
		p.mentionPaletteIndex = 0
		return matches
	}
	if p.mentionPaletteIndex < 0 {
		p.mentionPaletteIndex = 0
	}
	if p.mentionPaletteIndex >= len(matches) {
		p.mentionPaletteIndex = len(matches) - 1
	}
	return matches
}

func (p *ChatPage) moveMentionPaletteSelection(delta int) {
	matches := p.syncMentionPaletteSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	next := p.mentionPaletteIndex + delta
	if next < 0 {
		next = len(matches) - 1
	}
	if next >= len(matches) {
		next = 0
	}
	p.mentionPaletteIndex = next
}

func (p *ChatPage) selectedMentionCandidate() (string, bool) {
	matches := p.syncMentionPaletteSelection()
	if len(matches) == 0 {
		return "", false
	}
	return matches[p.mentionPaletteIndex], true
}

func (p *ChatPage) completeMentionFromPalette() bool {
	selected, ok := p.selectedMentionCandidate()
	if !ok {
		return false
	}
	p.input = "@" + strings.TrimSpace(selected) + " "
	p.inputCursor = utf8.RuneCountInString(p.input)
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.maybeWarnLargeInput("", p.input)
	p.mentionPaletteIndex = 0
	p.statusLine = "selected @" + strings.TrimSpace(selected)
	return true
}

func (p *ChatPage) acceptMentionPaletteEnter() bool {
	if !p.mentionPaletteActive() {
		return false
	}
	prompt := strings.TrimSpace(p.input)
	if prompt == "" || !strings.HasPrefix(prompt, "@") {
		return false
	}
	if mentionHasArgs(prompt) {
		return false
	}
	_, ok := p.selectedMentionCandidate()
	if !ok {
		return false
	}
	return p.completeMentionFromPalette()
}

func (p *ChatPage) drawMentionPalette(s tcell.Screen, inputRect Rect, topBound, bottomBound int) {
	if !p.mentionPaletteActive() {
		return
	}
	matches := p.syncMentionPaletteSelection()

	const maxVisible = 5
	visible := len(matches)
	if visible > maxVisible {
		visible = maxVisible
	}
	if visible == 0 {
		visible = 1
	}

	popupH := visible + 3
	popup, ok := chatCommandPaletteRect(inputRect, popupH, topBound, bottomBound)
	if !ok || popup.W < 14 || popup.H < 3 {
		return
	}

	FillRect(s, popup, p.theme.Panel)
	DrawBox(s, popup, p.theme.BorderActive)
	rowY := popup.Y + 1
	if len(matches) == 0 {
		DrawText(s, popup.X+2, rowY, popup.W-4, p.theme.Warning, "no matching subagents")
		DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, "Type more or press Backspace")
		return
	}

	start := 0
	if len(matches) > visible {
		start = p.mentionPaletteIndex - (visible - 1)
		if start < 0 {
			start = 0
		}
		maxStart := len(matches) - visible
		if start > maxStart {
			start = maxStart
		}
	}

	for i := 0; i < visible; i++ {
		idx := start + i
		suggestion := matches[idx]
		prefix := "  "
		style := p.theme.Text
		if idx == p.mentionPaletteIndex {
			prefix = "› "
			style = p.theme.Primary.Bold(true)
		}
		DrawText(s, popup.X+2, rowY, popup.W-4, style, prefix+"@"+suggestion)
		rowY++
	}

	DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, "Enter inserts selection • Tab completes • ↑/↓ select")
}

func (p *ChatPage) syncComposerPalettes() {
	p.syncCommandPaletteSelection()
	p.syncMentionPaletteSelection()
}

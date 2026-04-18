package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) setCommandSuggestions(items []CommandSuggestion) {
	p.commandSuggestions = p.commandSuggestions[:0]
	for _, item := range items {
		command := normalizeCommand(item.Command)
		if command == "" {
			continue
		}
		p.commandSuggestions = append(p.commandSuggestions, CommandSuggestion{
			Command:   command,
			Hint:      item.Hint,
			QuickTips: append([]string(nil), item.QuickTips...),
		})
	}
	p.commandPaletteIndex = 0
}

func (p *ChatPage) commandPaletteActive() bool {
	if len(p.commandSuggestions) == 0 {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(p.input), "/")
}

func (p *ChatPage) commandPaletteMatches() []CommandSuggestion {
	if !p.commandPaletteActive() {
		return nil
	}
	query := commandPaletteQuery(p.input)
	if query == "" {
		return append([]CommandSuggestion(nil), p.commandSuggestions...)
	}
	prefixMatches := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	containsMatches := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	for _, suggestion := range p.commandSuggestions {
		candidate := commandPaletteQuery(suggestion.Command)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, query) {
			prefixMatches = append(prefixMatches, suggestion)
			continue
		}
		if strings.Contains(candidate, query) {
			containsMatches = append(containsMatches, suggestion)
		}
	}
	return append(prefixMatches, containsMatches...)
}

func (p *ChatPage) syncCommandPaletteSelection() []CommandSuggestion {
	matches := p.commandPaletteMatches()
	if len(matches) == 0 {
		p.commandPaletteIndex = 0
		return matches
	}
	if p.commandPaletteIndex < 0 {
		p.commandPaletteIndex = 0
	}
	if p.commandPaletteIndex >= len(matches) {
		p.commandPaletteIndex = len(matches) - 1
	}
	return matches
}

func (p *ChatPage) moveCommandPaletteSelection(delta int) {
	matches := p.syncCommandPaletteSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	next := p.commandPaletteIndex + delta
	if next < 0 {
		next = len(matches) - 1
	}
	if next >= len(matches) {
		next = 0
	}
	p.commandPaletteIndex = next
}

func (p *ChatPage) selectedCommandSuggestion() (CommandSuggestion, bool) {
	matches := p.syncCommandPaletteSelection()
	if len(matches) == 0 {
		return CommandSuggestion{}, false
	}
	return matches[p.commandPaletteIndex], true
}

func (p *ChatPage) completeCommandFromPalette() bool {
	selected, ok := p.selectedCommandSuggestion()
	if !ok {
		return false
	}
	p.input = selected.Command + " "
	p.inputCursor = utf8.RuneCountInString(p.input)
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.maybeWarnLargeInput("", p.input)
	p.commandPaletteIndex = 0
	if selected.Hint != "" {
		p.statusLine = selected.Hint
	}
	return true
}

func (p *ChatPage) acceptCommandPaletteEnter() bool {
	if !p.commandPaletteActive() {
		return false
	}

	prompt := strings.TrimSpace(p.input)
	if prompt == "" || !strings.HasPrefix(prompt, "/") {
		return false
	}
	// Once arguments are being typed, Enter should execute the command.
	if hasArgsQuery(strings.TrimPrefix(prompt, "/")) {
		return false
	}

	selected, ok := p.selectedCommandSuggestion()
	if !ok {
		return false
	}
	if normalizeCommand(prompt) == selected.Command {
		return false
	}
	return p.completeCommandFromPalette()
}

func (p *ChatPage) drawCommandPalette(s tcell.Screen, inputRect Rect, topBound, bottomBound int) {
	if !p.commandPaletteActive() {
		return
	}
	matches := p.syncCommandPaletteSelection()

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
		DrawText(s, popup.X+2, rowY, popup.W-4, p.theme.Warning, "no matching commands")
		DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, "Type more or press Backspace")
		return
	}

	start := 0
	if len(matches) > visible {
		start = p.commandPaletteIndex - (visible - 1)
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
		if idx == p.commandPaletteIndex {
			prefix = "› "
			style = p.theme.Primary.Bold(true)
		}
		DrawText(s, popup.X+2, rowY, popup.W-4, style, prefix+suggestion.Command)
		rowY++
	}

	hint := "Enter runs selection • Tab completes • ↑/↓ select"
	if selected, ok := p.selectedCommandSuggestion(); ok && selected.Hint != "" {
		hint = selected.Hint
	}
	DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, hint)
}

func chatCommandPaletteRect(inputRect Rect, popupH, topBound, bottomBound int) (Rect, bool) {
	if bottomBound-topBound < 3 {
		return Rect{}, false
	}

	aboveY := inputRect.Y - popupH - 1
	belowY := inputRect.Y + inputRect.H + 1
	availableAbove := inputRect.Y - topBound - 1
	availableBelow := bottomBound - belowY

	popup := Rect{
		X: inputRect.X,
		Y: aboveY,
		W: inputRect.W,
		H: popupH,
	}
	switch {
	case availableAbove >= popupH:
		popup.Y = aboveY
	case availableBelow >= popupH:
		popup.Y = belowY
	case availableBelow > availableAbove:
		popup.Y = minInt(belowY, bottomBound-popupH)
	default:
		popup.Y = maxInt(topBound, aboveY)
	}

	if popup.Y < topBound {
		popup.Y = topBound
	}
	if popup.Y+popup.H > bottomBound {
		popup.Y = bottomBound - popup.H
	}
	if popup.Y < topBound {
		return Rect{}, false
	}
	return popup, true
}

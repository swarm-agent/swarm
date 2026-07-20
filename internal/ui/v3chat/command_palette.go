package v3chat

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// CommandSuggestion is the presentation-safe command metadata supplied by the
// app shell from the canonical TUI command registry.
type CommandSuggestion struct {
	Command   string
	Hint      string
	QuickTips []string
}

func normalizeCommand(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func commandQuery(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "/")))
}

func (p *Page) SetCommandSuggestions(items []CommandSuggestion) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commandSuggestions = p.commandSuggestions[:0]
	for _, item := range items {
		command := normalizeCommand(item.Command)
		if command == "" {
			continue
		}
		p.commandSuggestions = append(p.commandSuggestions, CommandSuggestion{
			Command:   command,
			Hint:      strings.TrimSpace(item.Hint),
			QuickTips: append([]string(nil), item.QuickTips...),
		})
	}
	p.commandPaletteIndex = 0
}

func (p *Page) commandPaletteActiveLocked() bool {
	return len(p.commandSuggestions) > 0 && strings.HasPrefix(strings.TrimSpace(string(p.input)), "/")
}

func (p *Page) commandPaletteMatchesLocked() []CommandSuggestion {
	if !p.commandPaletteActiveLocked() {
		return nil
	}
	query := commandQuery(string(p.input))
	if query == "" {
		return append([]CommandSuggestion(nil), p.commandSuggestions...)
	}
	prefix := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	contains := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	for _, suggestion := range p.commandSuggestions {
		candidate := commandQuery(suggestion.Command)
		switch {
		case strings.HasPrefix(candidate, query) || strings.HasPrefix(query, candidate+" "):
			prefix = append(prefix, suggestion)
		case strings.Contains(candidate, query):
			contains = append(contains, suggestion)
		}
	}
	return append(prefix, contains...)
}

func (p *Page) syncCommandPaletteSelectionLocked() []CommandSuggestion {
	matches := p.commandPaletteMatchesLocked()
	if len(matches) == 0 {
		p.commandPaletteIndex = 0
		return nil
	}
	if p.commandPaletteIndex < 0 {
		p.commandPaletteIndex = 0
	}
	if p.commandPaletteIndex >= len(matches) {
		p.commandPaletteIndex = len(matches) - 1
	}
	return matches
}

func (p *Page) moveCommandPaletteSelectionLocked(delta int) bool {
	matches := p.syncCommandPaletteSelectionLocked()
	if len(matches) == 0 || delta == 0 {
		return false
	}
	next := p.commandPaletteIndex + delta
	if next < 0 {
		next = len(matches) - 1
	}
	if next >= len(matches) {
		next = 0
	}
	p.commandPaletteIndex = next
	return true
}

func (p *Page) selectedCommandSuggestionLocked() (CommandSuggestion, bool) {
	matches := p.syncCommandPaletteSelectionLocked()
	if len(matches) == 0 {
		return CommandSuggestion{}, false
	}
	return matches[p.commandPaletteIndex], true
}

func (p *Page) completeCommandFromPaletteLocked() bool {
	selected, ok := p.selectedCommandSuggestionLocked()
	if !ok {
		return false
	}
	p.input = []rune(selected.Command + " ")
	p.cursor = len(p.input)
	p.pasteBuffer = nil
	p.commandPaletteIndex = 0
	if selected.Hint != "" {
		p.status = selected.Hint
	}
	return true
}

func (p *Page) acceptCommandPaletteEnterLocked() bool {
	if !p.commandPaletteActiveLocked() {
		return false
	}
	prompt := strings.TrimSpace(string(p.input))
	if prompt == "" || !strings.HasPrefix(prompt, "/") {
		return false
	}
	if strings.Contains(strings.TrimSpace(strings.TrimPrefix(prompt, "/")), " ") {
		return false
	}
	selected, ok := p.selectedCommandSuggestionLocked()
	if !ok || strings.EqualFold(normalizeCommand(prompt), selected.Command) {
		return false
	}
	return p.completeCommandFromPaletteLocked()
}

func (p *Page) drawCommandPalette(screen tcell.Screen, width, top, bottom int, styles PageStyles, input string, selected int, suggestions []CommandSuggestion) {
	if len(suggestions) == 0 || !strings.HasPrefix(strings.TrimSpace(input), "/") || width < 14 || bottom-top < 3 {
		return
	}
	query := commandQuery(input)
	matches := make([]CommandSuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		candidate := commandQuery(suggestion.Command)
		if query == "" || strings.HasPrefix(candidate, query) || strings.HasPrefix(query, candidate+" ") || strings.Contains(candidate, query) {
			matches = append(matches, suggestion)
		}
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(matches) && len(matches) > 0 {
		selected = len(matches) - 1
	}
	visible := minInt(5, len(matches))
	if visible == 0 {
		visible = 1
	}
	height := visible + 2
	if height > bottom-top {
		height = bottom - top
	}
	y := bottom - height
	fill(screen, 1, y, width-2, height, styles.Panel)
	drawBox(screen, 1, y, width-2, height, styles.Border)
	if len(matches) == 0 {
		drawText(screen, 3, y+1, width-6, styles.Warning, "no matching commands")
		return
	}
	start := 0
	if len(matches) > visible {
		start = selected - visible + 1
		if start < 0 {
			start = 0
		}
	}
	for row := 0; row < visible && start+row < len(matches); row++ {
		idx := start + row
		prefix := "  "
		style := styles.Text
		if idx == selected {
			prefix = "› "
			style = styles.Primary.Bold(true)
		}
		text := prefix + matches[idx].Command
		if matches[idx].Hint != "" {
			text += "  " + matches[idx].Hint
		}
		drawText(screen, 3, y+1+row, width-6, style, text)
	}
}

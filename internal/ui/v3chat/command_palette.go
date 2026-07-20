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

type commandPaletteOption struct {
	Label   string
	Command string
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
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "/")))
}

func normalizePaletteCandidate(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "/"))
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
	p.resetCommandPaletteOptionSelectionLocked()
}

func (p *Page) commandPaletteActiveLocked() bool {
	return len(p.commandSuggestions) > 0 && strings.HasPrefix(strings.TrimSpace(string(p.input)), "/")
}

func (p *Page) commandPaletteMatchesLocked() []CommandSuggestion {
	if !p.commandPaletteActiveLocked() {
		return nil
	}
	return commandPaletteMatches(p.commandSuggestions, commandQuery(string(p.input)))
}

func commandPaletteMatches(suggestions []CommandSuggestion, query string) []CommandSuggestion {
	if query == "" {
		return append([]CommandSuggestion(nil), suggestions...)
	}
	prefix := make([]CommandSuggestion, 0, len(suggestions))
	contains := make([]CommandSuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		switch commandSuggestionMatchKind(suggestion, query) {
		case 2:
			prefix = append(prefix, suggestion)
		case 1:
			contains = append(contains, suggestion)
		}
	}
	return append(prefix, contains...)
}

func commandSuggestionMatchKind(suggestion CommandSuggestion, query string) int {
	query = commandSuggestionCanonicalQuery(suggestion, query)
	best := commandCandidateMatchKind(normalizePaletteCandidate(suggestion.Command), query)
	for _, option := range commandPaletteOptions(suggestion) {
		if kind := commandCandidateMatchKind(normalizePaletteCandidate(option.Command), query); kind > best {
			best = kind
		}
	}
	return best
}

func commandSuggestionCanonicalQuery(suggestion CommandSuggestion, query string) string {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(parts) == 0 {
		return ""
	}
	canonical := normalizePaletteCandidate(suggestion.Command)
	if canonical == "" || parts[0] == canonical {
		return strings.Join(parts, " ")
	}
	for _, alias := range commandSuggestionAliases(suggestion) {
		if parts[0] != alias {
			continue
		}
		if len(parts) == 1 {
			return canonical
		}
		return canonical + " " + strings.Join(parts[1:], " ")
	}
	return strings.Join(parts, " ")
}

func commandSuggestionAliases(suggestion CommandSuggestion) []string {
	canonical := normalizePaletteCandidate(suggestion.Command)
	seen := map[string]struct{}{canonical: {}}
	aliases := make([]string, 0, len(suggestion.QuickTips))
	for _, item := range suggestion.QuickTips {
		trimmed := strings.TrimSpace(item)
		if !strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, " ") {
			continue
		}
		alias := normalizePaletteCandidate(trimmed)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases
}

func commandCandidateMatchKind(candidate, query string) int {
	candidate = strings.TrimSpace(candidate)
	query = strings.TrimSpace(query)
	if candidate == "" || query == "" {
		return 0
	}
	if candidate == query || strings.HasPrefix(candidate, query) || strings.HasPrefix(query, candidate+" ") {
		return 2
	}
	if strings.Contains(candidate, query) {
		return 1
	}
	return 0
}

func (p *Page) syncCommandPaletteSelectionLocked() []CommandSuggestion {
	matches := p.commandPaletteMatchesLocked()
	if len(matches) == 0 {
		p.commandPaletteIndex = 0
		p.resetCommandPaletteOptionSelectionLocked()
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
	p.resetCommandPaletteOptionSelectionLocked()
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

func commandPaletteOptions(selected CommandSuggestion) []commandPaletteOption {
	options := make([]commandPaletteOption, 0, len(selected.QuickTips))
	seen := make(map[string]struct{}, len(selected.QuickTips))
	baseCommand := normalizeCommand(selected.Command)
	for _, item := range selected.QuickTips {
		command := normalizeCommand(item)
		if command == "" {
			continue
		}
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		options = append(options, commandPaletteOption{
			Label:   commandPaletteOptionLabel(baseCommand, command),
			Command: command,
		})
	}
	return options
}

func commandPaletteOptionLabel(baseCommand, command string) string {
	baseCommand = normalizeCommand(baseCommand)
	command = normalizeCommand(command)
	if baseCommand != "" && strings.HasPrefix(command, baseCommand+" ") {
		if label := strings.TrimSpace(strings.TrimPrefix(command, baseCommand)); label != "" {
			return label
		}
	}
	return command
}

func commandPaletteAutoOptionIndex(selected CommandSuggestion, query string) (int, bool) {
	query = strings.TrimSpace(commandSuggestionCanonicalQuery(selected, query))
	if query == "" {
		return 0, false
	}
	base := normalizePaletteCandidate(selected.Command)
	if !strings.Contains(query, " ") && commandCandidateMatchKind(base, query) > 0 {
		return 0, false
	}
	bestKind := 0
	bestIndex := -1
	for i, option := range commandPaletteOptions(selected) {
		candidate := normalizePaletteCandidate(option.Command)
		if candidate == query {
			return i, true
		}
		if kind := commandCandidateMatchKind(candidate, query); kind > bestKind {
			bestKind = kind
			bestIndex = i
		}
	}
	if bestIndex >= 0 {
		return bestIndex, true
	}
	return 0, false
}

func currentCommandPaletteOptionIndex(selected CommandSuggestion, query, owner string, index int) (int, bool) {
	options := commandPaletteOptions(selected)
	if len(options) == 0 {
		return 0, false
	}
	if owner == selected.Command && index >= 0 && index < len(options) {
		return index, true
	}
	return commandPaletteAutoOptionIndex(selected, query)
}

func (p *Page) selectedCommandPaletteOptionLocked() (commandPaletteOption, bool) {
	selected, ok := p.selectedCommandSuggestionLocked()
	if !ok {
		return commandPaletteOption{}, false
	}
	index, ok := currentCommandPaletteOptionIndex(selected, commandQuery(string(p.input)), p.commandPaletteOptionOwner, p.commandPaletteOptionIndex)
	if !ok {
		return commandPaletteOption{}, false
	}
	options := commandPaletteOptions(selected)
	if index < 0 || index >= len(options) {
		return commandPaletteOption{}, false
	}
	return options[index], true
}

func (p *Page) moveCommandPaletteOptionSelectionLocked(delta int) bool {
	selected, ok := p.selectedCommandSuggestionLocked()
	if !ok || delta == 0 {
		return false
	}
	options := commandPaletteOptions(selected)
	if len(options) == 0 {
		return false
	}
	index, selectedOption := currentCommandPaletteOptionIndex(selected, commandQuery(string(p.input)), p.commandPaletteOptionOwner, p.commandPaletteOptionIndex)
	if !selectedOption {
		if delta > 0 {
			index = 0
		} else {
			index = len(options) - 1
		}
	} else {
		index += delta
		for index < 0 {
			index += len(options)
		}
		index %= len(options)
	}
	p.commandPaletteOptionOwner = selected.Command
	p.commandPaletteOptionIndex = index
	return true
}

func (p *Page) resetCommandPaletteOptionSelectionLocked() {
	p.commandPaletteOptionIndex = 0
	p.commandPaletteOptionOwner = ""
}

func (p *Page) commandPaletteChoiceLocked() (command, hint string, isOption, ok bool) {
	selected, ok := p.selectedCommandSuggestionLocked()
	if !ok {
		return "", "", false, false
	}
	if option, selectedOption := p.selectedCommandPaletteOptionLocked(); selectedOption {
		return option.Command, selected.Hint, true, true
	}
	return selected.Command, selected.Hint, false, true
}

func (p *Page) completeCommandFromPaletteLocked() bool {
	command, hint, _, ok := p.commandPaletteChoiceLocked()
	if !ok {
		return false
	}
	p.input = []rune(normalizeCommand(command) + " ")
	p.cursor = len(p.input)
	p.pasteBuffer = nil
	p.commandPaletteIndex = 0
	p.resetCommandPaletteOptionSelectionLocked()
	if hint != "" {
		p.status = hint
	}
	return true
}

func (p *Page) executeCommandPaletteSelectionLocked() bool {
	if !p.commandPaletteActiveLocked() {
		return false
	}
	command, _, isOption, ok := p.commandPaletteChoiceLocked()
	if !ok {
		return false
	}
	prompt := strings.TrimSpace(string(p.input))
	if !isOption && strings.Contains(commandQuery(prompt), " ") {
		command = prompt
	}
	p.pendingCommand = normalizeCommand(command)
	p.input = nil
	p.cursor = 0
	p.pasteBuffer = nil
	p.commandPaletteIndex = 0
	p.resetCommandPaletteOptionSelectionLocked()
	return true
}

func commandSuggestionFallbackLabel(suggestion CommandSuggestion) string {
	label := suggestion.Command
	if hint := strings.TrimSpace(suggestion.Hint); hint != "" {
		label += "  " + hint
	}
	return label
}

func (p *Page) drawCommandPalette(screen tcell.Screen, width, top, bottom int, styles PageStyles, input string, selected int, optionOwner string, optionIndex int, suggestions []CommandSuggestion) {
	if len(suggestions) == 0 || !strings.HasPrefix(strings.TrimSpace(input), "/") || width < 14 || bottom-top < 3 {
		return
	}
	query := commandQuery(input)
	matches := commandPaletteMatches(suggestions, query)
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
	height := visible + 3
	if height > bottom-top {
		height = bottom - top
	}
	y := bottom - height
	fill(screen, 1, y, width-2, height, styles.Panel)
	drawBox(screen, 1, y, width-2, height, styles.Border)
	if len(matches) == 0 {
		drawText(screen, 3, y+1, width-6, styles.Warning, "no matching commands")
		drawText(screen, 3, y+height-2, width-6, styles.Muted, "Type more or press Backspace")
		return
	}
	start := 0
	if len(matches) > visible {
		start = selected - visible + 1
		if start < 0 {
			start = 0
		}
		if maxStart := len(matches) - visible; start > maxStart {
			start = maxStart
		}
	}
	for row := 0; row < visible && start+row < len(matches); row++ {
		index := start + row
		suggestion := matches[index]
		prefix := "  "
		style := styles.Text
		if index == selected {
			prefix = "› "
			style = styles.Primary.Bold(true)
		}
		rowY := y + 1 + row
		options := commandPaletteOptions(suggestion)
		if len(options) == 0 {
			drawText(screen, 3, rowY, width-6, style, prefix+commandSuggestionFallbackLabel(suggestion))
			continue
		}
		base := prefix + suggestion.Command
		drawText(screen, 3, rowY, width-6, style, base)
		if index != selected {
			continue
		}
		x := 3 + len([]rune(base)) + 2
		remaining := width - 3 - x
		selectedOption, hasSelectedOption := currentCommandPaletteOptionIndex(suggestion, query, optionOwner, optionIndex)
		for i, option := range options {
			if remaining <= 0 {
				break
			}
			label := "[" + option.Label + "]"
			labelWidth := len([]rune(label))
			if labelWidth > remaining {
				if remaining > 1 {
					drawText(screen, x, rowY, remaining, styles.Muted, "…")
				}
				break
			}
			optionStyle := styles.Muted
			if index == selected && hasSelectedOption && i == selectedOption {
				optionStyle = styles.Primary.Reverse(true).Bold(true)
			}
			drawText(screen, x, rowY, remaining, optionStyle, label)
			x += labelWidth + 1
			remaining -= labelWidth + 1
		}
	}
	help := "Enter runs selection • Tab completes • ↑/↓ select"
	if selected >= 0 && selected < len(matches) && len(commandPaletteOptions(matches[selected])) > 0 {
		help = "Enter runs selection • ←/→ options • Tab completes • ↑/↓ select"
	}
	drawText(screen, 3, y+height-2, width-6, styles.Muted, help)
}

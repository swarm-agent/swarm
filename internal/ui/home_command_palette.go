package ui

import (
	"strings"
	"unicode/utf8"
)

type CommandSuggestion struct {
	Command   string
	Hint      string
	QuickTips []string
}

type commandPaletteOption struct {
	Label   string
	Command string
}

func (p *HomePage) SetCommandSuggestions(items []CommandSuggestion) {
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
	p.resetCommandPaletteOptionSelection()
}

func (p *HomePage) commandPaletteActive() bool {
	if len(p.commandSuggestions) == 0 {
		return false
	}
	prompt := strings.TrimSpace(p.prompt)
	return strings.HasPrefix(prompt, "/")
}

func (p *HomePage) commandPaletteMatches() []CommandSuggestion {
	if !p.commandPaletteActive() {
		return nil
	}
	query := commandPaletteQuery(p.prompt)
	if query == "" {
		return append([]CommandSuggestion(nil), p.commandSuggestions...)
	}
	prefixMatches := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	containsMatches := make([]CommandSuggestion, 0, len(p.commandSuggestions))
	for _, suggestion := range p.commandSuggestions {
		switch commandSuggestionMatchKind(suggestion, query) {
		case 2:
			prefixMatches = append(prefixMatches, suggestion)
		case 1:
			containsMatches = append(containsMatches, suggestion)
		}
	}
	return append(prefixMatches, containsMatches...)
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
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return ""
	}
	parts := strings.Fields(query)
	if len(parts) == 0 {
		return ""
	}
	canonical := normalizePaletteCandidate(suggestion.Command)
	if canonical == "" {
		return strings.Join(parts, " ")
	}
	if parts[0] == canonical {
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
	seen := make(map[string]struct{}, len(suggestion.QuickTips)+1)
	if canonical != "" {
		seen[canonical] = struct{}{}
	}
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

func (p *HomePage) syncCommandPaletteSelection() []CommandSuggestion {
	matches := p.commandPaletteMatches()
	if len(matches) == 0 {
		p.commandPaletteIndex = 0
		p.resetCommandPaletteOptionSelection()
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

func (p *HomePage) moveCommandPaletteSelection(delta int) {
	matches := p.syncCommandPaletteSelection()
	if len(matches) == 0 || delta == 0 {
		return
	}
	p.resetCommandPaletteOptionSelection()
	next := p.commandPaletteIndex + delta
	if next < 0 {
		next = len(matches) - 1
	}
	if next >= len(matches) {
		next = 0
	}
	p.commandPaletteIndex = next
}

func (p *HomePage) selectedCommandSuggestion() (CommandSuggestion, bool) {
	matches := p.syncCommandPaletteSelection()
	if len(matches) == 0 {
		return CommandSuggestion{}, false
	}
	return matches[p.commandPaletteIndex], true
}

func (p *HomePage) completeCommandPaletteChoice(command, hint string) bool {
	command = normalizeCommand(command)
	if command == "" {
		return false
	}
	p.prompt = command + " "
	p.promptCursor = utf8.RuneCountInString(p.prompt)
	p.pasteBuffer = p.pasteBuffer[:0]
	p.lastPasteBatchSize = 0
	p.commandPaletteIndex = 0
	p.resetCommandPaletteOptionSelection()
	if hint != "" {
		p.statusLine = hint
	}
	return true
}

func (p *HomePage) completeCommandFromPalette() bool {
	command, hint, _, _, ok := p.commandPaletteChoice()
	if !ok {
		return false
	}
	return p.completeCommandPaletteChoice(command, hint)
}

func (p *HomePage) acceptCommandPaletteEnter() bool {
	if !p.commandPaletteActive() {
		return false
	}

	prompt := strings.TrimSpace(p.prompt)
	if prompt == "" || !strings.HasPrefix(prompt, "/") {
		return false
	}

	command, hint, isOption, exact, ok := p.commandPaletteChoice()
	if !ok {
		return false
	}
	if exact {
		return false
	}
	if hasArgsQuery(strings.TrimPrefix(prompt, "/")) && !isOption {
		return false
	}
	return p.completeCommandPaletteChoice(command, hint)
}

func (p *HomePage) commandPaletteChoice() (command, hint string, isOption, exact, ok bool) {
	selected, ok := p.selectedCommandSuggestion()
	if !ok {
		return "", "", false, false, false
	}
	canonicalQuery := commandSuggestionCanonicalQuery(selected, commandPaletteQuery(p.prompt))
	if option, ok := p.selectedCommandPaletteOption(); ok {
		return option.Command, selected.Hint, true, canonicalQuery == normalizePaletteCandidate(option.Command), true
	}
	return selected.Command, selected.Hint, false, canonicalQuery == normalizePaletteCandidate(selected.Command), true
}

func commandPaletteOptions(selected CommandSuggestion) []commandPaletteOption {
	if len(selected.QuickTips) == 0 {
		return nil
	}
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

func (p *HomePage) selectedCommandPaletteOption() (commandPaletteOption, bool) {
	selected, ok := p.selectedCommandSuggestion()
	if !ok {
		return commandPaletteOption{}, false
	}
	idx, ok := p.currentCommandPaletteOptionIndex(selected)
	if !ok {
		return commandPaletteOption{}, false
	}
	options := commandPaletteOptions(selected)
	if idx < 0 || idx >= len(options) {
		return commandPaletteOption{}, false
	}
	return options[idx], true
}

func (p *HomePage) currentCommandPaletteOptionIndex(selected CommandSuggestion) (int, bool) {
	options := commandPaletteOptions(selected)
	if len(options) == 0 {
		return 0, false
	}
	if p.commandPaletteOptionOwner == selected.Command {
		if p.commandPaletteOptionIndex >= 0 && p.commandPaletteOptionIndex < len(options) {
			return p.commandPaletteOptionIndex, true
		}
	}
	return commandPaletteAutoOptionIndex(selected, commandPaletteQuery(p.prompt))
}

func commandPaletteAutoOptionIndex(selected CommandSuggestion, query string) (int, bool) {
	query = commandSuggestionCanonicalQuery(selected, query)
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, false
	}
	base := normalizePaletteCandidate(selected.Command)
	if !strings.Contains(query, " ") && commandCandidateMatchKind(base, query) > 0 {
		return 0, false
	}
	options := commandPaletteOptions(selected)
	bestKind := 0
	bestIdx := -1
	for i, option := range options {
		candidate := normalizePaletteCandidate(option.Command)
		if candidate == query {
			return i, true
		}
		if kind := commandCandidateMatchKind(candidate, query); kind > bestKind {
			bestKind = kind
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return bestIdx, true
	}
	return 0, false
}

func (p *HomePage) moveCommandPaletteOptionSelection(delta int) {
	selected, ok := p.selectedCommandSuggestion()
	if !ok || delta == 0 {
		return
	}
	options := commandPaletteOptions(selected)
	if len(options) == 0 {
		return
	}
	idx, ok := p.currentCommandPaletteOptionIndex(selected)
	if !ok {
		if delta > 0 {
			idx = 0
		} else {
			idx = len(options) - 1
		}
		p.commandPaletteOptionOwner = selected.Command
		p.commandPaletteOptionIndex = idx
		return
	}
	idx += delta
	for idx < 0 {
		idx += len(options)
	}
	idx %= len(options)
	p.commandPaletteOptionOwner = selected.Command
	p.commandPaletteOptionIndex = idx
}

func (p *HomePage) resetCommandPaletteOptionSelection() {
	p.commandPaletteOptionIndex = 0
	p.commandPaletteOptionOwner = ""
}

func commandPaletteQuery(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "/"))
	return strings.ToLower(trimmed)
}

func normalizePaletteCandidate(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "/"))
}

func hasQueryMatch(candidate, query string) bool {
	return strings.HasPrefix(candidate, query) || strings.Contains(candidate, query)
}

func hasArgsQuery(query string) bool {
	return strings.Contains(strings.TrimSpace(query), " ")
}

func normalizeCommand(raw string) string {
	command := strings.TrimSpace(raw)
	if command == "" {
		return ""
	}
	if !strings.HasPrefix(command, "/") {
		command = "/" + command
	}
	return command
}

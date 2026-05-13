package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func drawCommandSuggestionRow(s tcell.Screen, x, y, maxWidth int, commandStyle, hintStyle tcell.Style, prefix string, suggestion CommandSuggestion) int {
	if maxWidth <= 0 {
		return 0
	}
	label := prefix + suggestion.Command
	hint := ""
	if suggestion.InlineHint {
		hint = strings.TrimSpace(suggestion.Hint)
	}
	if hint == "" {
		return DrawTextCount(s, x, y, maxWidth, commandStyle, label)
	}
	if utf8.RuneCountInString(label) >= maxWidth-3 {
		return DrawTextCount(s, x, y, maxWidth, commandStyle, label)
	}

	written := DrawTextCount(s, x, y, maxWidth, commandStyle, label)
	remaining := maxWidth - written
	if remaining <= 3 {
		return written
	}
	sep := "  "
	written += DrawTextCount(s, x+written, y, remaining, hintStyle, sep)
	remaining = maxWidth - written
	if remaining <= 0 {
		return written
	}
	return written + DrawTextCount(s, x+written, y, remaining, hintStyle, clampEllipsis(hint, remaining))
}

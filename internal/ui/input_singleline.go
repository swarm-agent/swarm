package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const singleLinePasteFlushChunkRunes = 256

func normalizeSingleLineRune(r rune) (rune, bool) {
	switch r {
	case '\n', '\r', '\t':
		return ' ', true
	default:
		if unicode.IsPrint(r) {
			return r, true
		}
		return 0, false
	}
}

func clampSingleLineInput(text string, maxRunes int) string {
	if text == "" {
		return ""
	}
	if maxRunes == 0 {
		return ""
	}
	var b strings.Builder
	count := 0
	for _, r := range text {
		normalized, ok := normalizeSingleLineRune(r)
		if !ok {
			continue
		}
		if maxRunes > 0 && count >= maxRunes {
			break
		}
		b.WriteRune(normalized)
		count++
	}
	return b.String()
}

func appendSingleLineInput(current, chunk string, maxRunes int) string {
	base := clampSingleLineInput(current, maxRunes)
	if chunk == "" {
		return base
	}

	count := utf8.RuneCountInString(base)
	if maxRunes > 0 && count >= maxRunes {
		return base
	}

	var b strings.Builder
	b.Grow(len(base) + len(chunk))
	b.WriteString(base)
	for _, r := range chunk {
		normalized, ok := normalizeSingleLineRune(r)
		if !ok {
			continue
		}
		if maxRunes > 0 && count >= maxRunes {
			break
		}
		b.WriteRune(normalized)
		count++
	}
	return b.String()
}

func normalizeMultilineText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func normalizeMultilineRune(r rune) (rune, bool) {
	switch r {
	case '\n':
		return '\n', true
	case '\t':
		return ' ', true
	default:
		if unicode.IsPrint(r) {
			return r, true
		}
		return 0, false
	}
}

func clampMultilineInput(text string, maxRunes int) string {
	text = normalizeMultilineText(text)
	if text == "" {
		return ""
	}
	if maxRunes == 0 {
		return ""
	}
	var b strings.Builder
	count := 0
	for _, r := range text {
		normalized, ok := normalizeMultilineRune(r)
		if !ok {
			continue
		}
		if maxRunes > 0 && count >= maxRunes {
			break
		}
		b.WriteRune(normalized)
		count++
	}
	return b.String()
}

func appendMultilineInput(current, chunk string, maxRunes int) string {
	base := clampMultilineInput(current, maxRunes)
	chunk = normalizeMultilineText(chunk)
	if chunk == "" {
		return base
	}

	count := utf8.RuneCountInString(base)
	if maxRunes > 0 && count >= maxRunes {
		return base
	}

	var b strings.Builder
	b.Grow(len(base) + len(chunk))
	b.WriteString(base)
	for _, r := range chunk {
		normalized, ok := normalizeMultilineRune(r)
		if !ok {
			continue
		}
		if maxRunes > 0 && count >= maxRunes {
			break
		}
		b.WriteRune(normalized)
		count++
	}
	return b.String()
}

func clampRuneCursor(text string, cursor int) int {
	if cursor < 0 {
		return 0
	}
	total := utf8.RuneCountInString(text)
	if cursor > total {
		return total
	}
	return cursor
}

func moveRuneCursorLeft(text string, cursor int) int {
	cursor = clampRuneCursor(text, cursor)
	if cursor > 0 {
		cursor--
	}
	return cursor
}

func moveRuneCursorRight(text string, cursor int) int {
	cursor = clampRuneCursor(text, cursor)
	total := utf8.RuneCountInString(text)
	if cursor < total {
		cursor++
	}
	return cursor
}

func splitAtRuneCursor(text string, cursor int) (string, string) {
	runes := []rune(text)
	cursor = clampRuneCursor(text, cursor)
	return string(runes[:cursor]), string(runes[cursor:])
}

func insertMultilineAtCursor(current string, cursor int, chunk string, maxRunes int) (string, int, int) {
	current = clampMultilineInput(current, maxRunes)
	cursor = clampRuneCursor(current, cursor)
	chunk = normalizeMultilineText(chunk)
	if chunk == "" {
		return current, cursor, 0
	}
	currentRunes := utf8.RuneCountInString(current)
	if maxRunes > 0 && currentRunes >= maxRunes {
		return current, cursor, 0
	}
	before, after := splitAtRuneCursor(current, cursor)
	allowed := maxRunes - currentRunes
	if maxRunes <= 0 {
		allowed = -1
	}
	inserted := appendMultilineInput("", chunk, allowed)
	insertedRunes := utf8.RuneCountInString(inserted)
	if insertedRunes == 0 {
		return current, cursor, 0
	}
	return before + inserted + after, cursor + insertedRunes, insertedRunes
}

func backspaceMultilineAtCursor(current string, cursor int) (string, int, bool) {
	current = normalizeMultilineText(current)
	cursor = clampRuneCursor(current, cursor)
	if cursor <= 0 {
		return current, cursor, false
	}
	runes := []rune(current)
	next := string(append(runes[:cursor-1], runes[cursor:]...))
	return next, cursor - 1, true
}

type wrappedInputCursorLayout struct {
	Lines      []string
	CursorLine int
	CursorCol  int
}

func wrapWithCustomPrefixesCursor(firstPrefix, continuationPrefix, body string, width, cursor int) wrappedInputCursorLayout {
	if width <= 0 {
		return wrappedInputCursorLayout{}
	}
	if continuationPrefix == "" {
		continuationPrefix = strings.Repeat(" ", utf8.RuneCountInString(firstPrefix))
	}
	body = normalizeMultilineText(body)
	cursor = clampRuneCursor(body, cursor)
	parts := strings.Split(body, "\n")
	lines := make([]string, 0, maxInt(1, len(parts)*2))
	cursorLine := 0
	cursorCol := minInt(width-1, utf8.RuneCountInString(firstPrefix))
	cursorFound := false
	consumed := 0
	firstSegment := true

	appendLine := func(prefix, chunk string) {
		line := prefix + chunk
		lineIndex := len(lines)
		if !cursorFound {
			chunkRunes := utf8.RuneCountInString(chunk)
			if cursor >= consumed && cursor <= consumed+chunkRunes {
				cursorLine = lineIndex
				cursorCol = utf8.RuneCountInString(prefix) + (cursor - consumed)
				cursorFound = true
			}
		}
		lines = append(lines, line)
		consumed += utf8.RuneCountInString(chunk)
	}

	for i, part := range parts {
		prefix := continuationPrefix
		if firstSegment {
			prefix = firstPrefix
		}
		if part == "" {
			line := clampEllipsis(prefix, width)
			lineIndex := len(lines)
			lines = append(lines, line)
			if !cursorFound && cursor == consumed {
				cursorLine = lineIndex
				cursorCol = utf8.RuneCountInString(line)
				cursorFound = true
			}
			firstSegment = false
		} else {
			remaining := part
			linePrefix := prefix
			available := maxInt(1, width-utf8.RuneCountInString(linePrefix))
			continuationAvailable := maxInt(1, width-utf8.RuneCountInString(continuationPrefix))
			for remaining != "" {
				chunk := remaining
				tail := ""
				if utf8.RuneCountInString(remaining) > available {
					chunk, tail = wrapWordAwareChunk(remaining, available)
					if chunk == "" {
						runes := []rune(remaining)
						n := minInt(available, len(runes))
						chunk = string(runes[:n])
						tail = string(runes[n:])
					}
				}
				appendLine(linePrefix, chunk)
				remaining = tail
				linePrefix = continuationPrefix
				available = continuationAvailable
			}
			firstSegment = false
		}
		if i < len(parts)-1 {
			consumed++
		}
	}

	if len(lines) == 0 {
		line := clampEllipsis(firstPrefix, width)
		lines = []string{line}
		return wrappedInputCursorLayout{Lines: lines, CursorLine: 0, CursorCol: utf8.RuneCountInString(line)}
	}
	if !cursorFound {
		cursorLine = len(lines) - 1
		cursorCol = utf8.RuneCountInString(lines[cursorLine])
	}
	if cursorLine < 0 {
		cursorLine = 0
	}
	if cursorLine >= len(lines) {
		cursorLine = len(lines) - 1
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	return wrappedInputCursorLayout{Lines: lines, CursorLine: cursorLine, CursorCol: cursorCol}
}

func inputVisibleWindow(totalLines, visibleHeight, cursorLine int) int {
	if visibleHeight <= 0 || totalLines <= visibleHeight {
		return 0
	}
	cursorLine = maxInt(0, minInt(cursorLine, totalLines-1))
	start := cursorLine - visibleHeight + 1
	if start < 0 {
		start = 0
	}
	maxStart := totalLines - visibleHeight
	if start > maxStart {
		start = maxStart
	}
	return start
}

func drawWrappedInputArea(s tcell.Screen, lineStart, contentY, innerW, contentH int, style tcell.Style, prefix, body string, cursor int) (int, int, bool) {
	if s == nil || innerW <= 0 || contentH <= 0 {
		return 0, 0, false
	}
	layout := wrapWithCustomPrefixesCursor(prefix, "", body, innerW, cursor)
	if len(layout.Lines) == 0 {
		return 0, 0, false
	}
	start := inputVisibleWindow(len(layout.Lines), contentH, layout.CursorLine)
	end := minInt(len(layout.Lines), start+contentH)
	visible := layout.Lines[start:end]
	for i, line := range visible {
		DrawText(s, lineStart, contentY+i, innerW, style, line)
	}
	cursorLine := layout.CursorLine - start
	if cursorLine < 0 || cursorLine >= len(visible) {
		return 0, 0, false
	}
	cursorX := lineStart + layout.CursorCol
	maxX := lineStart + innerW - 1
	if cursorX > maxX {
		cursorX = maxX
	}
	if cursorX < lineStart {
		cursorX = lineStart
	}
	return cursorX, contentY + cursorLine, true
}

func singleLineInputView(text string, cursor, width int) (string, int) {
	if width <= 0 {
		return "", 0
	}
	runes := []rune(normalizeMultilineText(text))
	cursor = clampRuneCursor(string(runes), cursor)
	lineStart := 0
	for i := 0; i < cursor && i < len(runes); i++ {
		if runes[i] == '\n' {
			lineStart = i + 1
		}
	}
	lineEnd := len(runes)
	for i := cursor; i < len(runes); i++ {
		if runes[i] == '\n' {
			lineEnd = i
			break
		}
	}
	lineRunes := runes[lineStart:lineEnd]
	cursorInLine := cursor - lineStart
	if cursorInLine < 0 {
		cursorInLine = 0
	}
	if cursorInLine > len(lineRunes) {
		cursorInLine = len(lineRunes)
	}
	start := 0
	if len(lineRunes) > width {
		start = cursorInLine - width + 1
		if start < 0 {
			start = 0
		}
		maxStart := len(lineRunes) - width
		if start > maxStart {
			start = maxStart
		}
	}
	end := len(lineRunes)
	if width > 0 && end-start > width {
		end = start + width
	}
	visible := string(lineRunes[start:end])
	cursorCol := cursorInLine - start
	if cursorCol < 0 {
		cursorCol = 0
	}
	if cursorCol > utf8.RuneCountInString(visible) {
		cursorCol = utf8.RuneCountInString(visible)
	}
	return visible, cursorCol
}

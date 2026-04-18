package ui

func wrapLineBreakIndicesRunes(runes []rune, width int) (headEnd, tailStart int) {
	if width <= 0 {
		return 0, 0
	}
	if len(runes) <= width {
		return len(runes), len(runes)
	}
	if headEnd, tailStart, ok := wrapWhitespaceBoundaryRunes(runes, width); ok {
		return headEnd, tailStart
	}
	return width, width
}

func wrapWhitespaceBoundaryRunes(runes []rune, width int) (int, int, bool) {
	limit := minInt(width, len(runes))
	bestHeadEnd, bestTailStart := 0, 0
	bestFound := false
	for i := 0; i < limit; i++ {
		if !isWrapSpace(runes[i]) {
			continue
		}
		headEnd := i
		for headEnd > 0 && isWrapSpace(runes[headEnd-1]) {
			headEnd--
		}
		if headEnd == 0 {
			continue
		}
		tailStart := i + 1
		for tailStart < len(runes) && isWrapSpace(runes[tailStart]) {
			tailStart++
		}
		bestHeadEnd, bestTailStart, bestFound = headEnd, tailStart, true
	}
	if bestFound {
		return bestHeadEnd, bestTailStart, true
	}
	return 0, 0, false
}

func wrapPlainLine(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	if text == "" {
		return []string{""}
	}

	runes := []rune(text)
	lines := make([]string, 0, 4)
	for len(runes) > 0 {
		if len(runes) <= width {
			lines = append(lines, string(runes))
			break
		}
		headEnd, tailStart := wrapLineBreakIndicesRunes(runes, width)
		if headEnd <= 0 || headEnd > len(runes) {
			headEnd = minInt(width, len(runes))
		}
		if tailStart < headEnd || tailStart > len(runes) {
			tailStart = headEnd
		}
		lines = append(lines, string(runes[:headEnd]))
		runes = runes[tailStart:]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func isWrapSpace(r rune) bool {
	return r == ' ' || r == '\t'
}

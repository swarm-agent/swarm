package ui

import (
	"regexp"
	"strings"
)

var (
	thinkingSummaryWhitespace = regexp.MustCompile(`\s+`)
	thinkingSummaryCollapsed  = regexp.MustCompile(`[*_]{4,}`)
	thinkingSummaryLeading    = regexp.MustCompile(`(^|\s)[*_](\S)`)
	thinkingSummaryTrailing   = regexp.MustCompile(`(\S)[*_]($|\s)`)
	thinkingSummaryXMLTag     = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9:_\-]*(?:\s+[^<>]*)?>`)
	thinkingSummaryBracketTag = regexp.MustCompile(`\[(?:/?(?:thinking|analysis|reasoning|summary)[^\]]*)\]`)
	thinkingSummaryBoldLead   = regexp.MustCompile(`^(?:\*\*|__)(.+?)(?:\*\*|__)(?:\s+|$)(.*)$`)
	reasoningLeadLabelLine    = regexp.MustCompile(`(?i)^(?:\*\*|__)?(?:thinking|reasoning|analysis)(?:\*\*|__)?$`)
	reasoningLeadLabelPrefix  = regexp.MustCompile(`(?i)^(?:\*\*|__)?(?:thinking|reasoning|analysis)(?:\*\*|__)?\s*[:\-—]\s*(.+)$`)
)

type thinkingDisplayBlock struct {
	Text string
	Bold bool
}

func normalizeThinkingSummary(raw string) string {
	raw = thinkingSummaryXMLTag.ReplaceAllString(raw, " ")
	raw = thinkingSummaryBracketTag.ReplaceAllString(raw, " ")

	normalized := thinkingSummaryWhitespace.ReplaceAllString(strings.TrimSpace(raw), " ")
	if normalized == "" {
		return ""
	}

	normalized = thinkingSummaryCollapsed.ReplaceAllString(normalized, " ")
	normalized = strings.ReplaceAll(normalized, "**", "")
	normalized = strings.ReplaceAll(normalized, "__", "")

	for {
		stripped := thinkingSummaryLeading.ReplaceAllString(normalized, "$1$2")
		stripped = thinkingSummaryTrailing.ReplaceAllString(stripped, "$1$2")
		stripped = thinkingSummaryWhitespace.ReplaceAllString(strings.TrimSpace(stripped), " ")
		if stripped == normalized {
			break
		}
		normalized = stripped
	}

	return dedupeThinkingSummarySentence(strings.TrimSpace(normalized))
}

func dedupeThinkingSummarySentence(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	summary = dedupeRepeatedSentenceChunks(summary)
	summary = dedupeRepeatedTokenHalves(summary)
	return strings.TrimSpace(summary)
}

func dedupeRepeatedSentenceChunks(summary string) string {
	chunks := splitThinkingSummaryChunks(summary)
	if len(chunks) < 2 {
		return summary
	}
	deduped := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if len(deduped) > 0 && strings.EqualFold(deduped[len(deduped)-1], chunk) {
			continue
		}
		deduped = append(deduped, chunk)
	}
	if len(deduped) == 0 {
		return ""
	}
	return strings.Join(deduped, " ")
}

func splitThinkingSummaryChunks(summary string) []string {
	chunks := make([]string, 0, 4)
	var current strings.Builder
	for _, r := range summary {
		current.WriteRune(r)
		switch r {
		case '.', '!', '?':
			chunk := strings.TrimSpace(current.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			current.Reset()
		}
	}
	tail := strings.TrimSpace(current.String())
	if tail != "" {
		chunks = append(chunks, tail)
	}
	return chunks
}

func dedupeRepeatedTokenHalves(summary string) string {
	tokens := strings.Fields(summary)
	if len(tokens) < 4 || len(tokens)%2 != 0 {
		return summary
	}
	half := len(tokens) / 2
	left := strings.Join(tokens[:half], " ")
	right := strings.Join(tokens[half:], " ")
	if strings.EqualFold(left, right) {
		return left
	}
	return summary
}

func mergeThinkingStream(current, delta string) string {
	if merged, replaced := mergeThinkingSnapshot(current, delta); replaced {
		return merged
	}
	return mergeStreamDelta(current, delta, normalizeThinkingSummary)
}

func mergeThinkingSnapshot(current, snapshot string) (string, bool) {
	snapshotCanonical := canonicalThinkingText(snapshot)
	if snapshotCanonical == "" {
		return "", false
	}
	currentCanonical := canonicalThinkingText(current)
	if currentCanonical == "" {
		return snapshot, true
	}
	if currentCanonical == snapshotCanonical {
		return current, true
	}
	if strings.HasPrefix(snapshotCanonical, currentCanonical) {
		return snapshot, true
	}
	if strings.HasPrefix(currentCanonical, snapshotCanonical) {
		return current, true
	}
	if shouldReplaceThinkingSnapshot(currentCanonical, snapshotCanonical) {
		return snapshot, true
	}
	return "", false
}

func shouldReplaceThinkingSnapshot(current, snapshot string) bool {
	currentLead := thinkingSnapshotLead(current)
	snapshotLead := thinkingSnapshotLead(snapshot)
	if currentLead != "" && snapshotLead != "" && currentLead == snapshotLead {
		return true
	}
	if looksLikeFullThinkingSnapshot(current) && looksLikeFullThinkingSnapshot(snapshot) {
		return true
	}
	return sharedThinkingPrefixLength(normalizeThinkingSummary(current), normalizeThinkingSummary(snapshot)) >= 48
}

func thinkingSnapshotLead(raw string) string {
	blocks := thinkingSummaryBlocks(raw)
	if len(blocks) == 0 {
		return ""
	}
	return normalizeThinkingSummary(blocks[0].Text)
}

func looksLikeFullThinkingSnapshot(raw string) bool {
	canonical := canonicalThinkingText(raw)
	if canonical == "" {
		return false
	}
	return strings.Contains(canonical, "\n\n") || len(thinkingSummaryBlocks(canonical)) > 1 || len(canonical) >= 96
}

func sharedThinkingPrefixLength(left, right string) int {
	max := len(left)
	if len(right) < max {
		max = len(right)
	}
	count := 0
	for count < max && left[count] == right[count] {
		count++
	}
	return count
}

func mergeAssistantStream(current, delta string) string {
	current = normalizeAssistantLineEndings(current)
	delta = normalizeAssistantLineEndings(delta)
	if delta == "" {
		return current
	}
	if current == "" {
		return delta
	}
	if strings.TrimSpace(delta) == "" {
		return current + delta
	}
	return mergeStreamDelta(current, delta, func(value string) string {
		return strings.TrimSpace(normalizeAssistantLineEndings(value))
	})
}

func normalizeAssistantLineEndings(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func mergeStreamDelta(current, delta string, normalize func(string) string) string {
	if strings.TrimSpace(delta) == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return delta
	}

	currentNormalized := strings.TrimSpace(current)
	deltaNormalized := strings.TrimSpace(delta)
	if normalize != nil {
		currentNormalized = normalize(current)
		deltaNormalized = normalize(delta)
	}
	if currentNormalized != "" && deltaNormalized != "" {
		if currentNormalized == deltaNormalized {
			return current
		}
		if strings.HasPrefix(deltaNormalized, currentNormalized) {
			return delta
		}
	}

	if strings.HasSuffix(current, delta) {
		return current
	}
	if strings.HasPrefix(delta, current) {
		return delta
	}

	maxOverlap := len(current)
	if len(delta) < maxOverlap {
		maxOverlap = len(delta)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if strings.HasSuffix(current, delta[:overlap]) {
			return current + delta[overlap:]
		}
	}
	return current + delta
}

func defaultSummaryFromText(raw string) string {
	normalized := normalizeThinkingSummary(raw)
	if normalized == "" {
		return ""
	}
	sentence := firstSentence(normalized)
	return clampEllipsis(sentence, 120)
}

func canonicalThinkingText(raw string) string {
	blocks := thinkingSummaryBlocks(raw)
	if len(blocks) == 0 {
		return ""
	}

	serialized := make([]string, 0, len(blocks))
	for _, block := range blocks {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		if block.Bold {
			text = "**" + text + "**"
		}
		serialized = append(serialized, text)
	}
	return strings.Join(serialized, "\n\n")
}

func thinkingSummaryKey(raw string) string {
	return normalizeThinkingSummary(canonicalThinkingText(raw))
}

func thinkingSummaryBlocks(raw string) []thinkingDisplayBlock {
	paragraphs := splitThinkingParagraphs(raw)
	if len(paragraphs) == 0 {
		return nil
	}
	paragraphs = trimLeadingReasoningLabelParagraphs(paragraphs)
	if len(paragraphs) == 0 {
		return nil
	}

	blocks := make([]thinkingDisplayBlock, 0, len(paragraphs)+1)
	appendBlock := func(text string, bold bool) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		key := normalizeThinkingSummary(text)
		if key == "" {
			return
		}
		if len(blocks) > 0 {
			last := blocks[len(blocks)-1]
			if last.Bold == bold && normalizeThinkingSummary(last.Text) == key {
				return
			}
		}
		blocks = append(blocks, thinkingDisplayBlock{Text: text, Bold: bold})
	}

	for _, paragraph := range paragraphs {
		if heading, remainder, ok := splitThinkingHeading(paragraph); ok {
			appendBlock(heading, true)
			appendBlock(remainder, false)
			continue
		}
		appendBlock(sanitizeThinkingParagraph(paragraph), false)
	}
	return dedupeRepeatedThinkingBlocks(blocks)
}

func trimLeadingReasoningLabelParagraphs(paragraphs []string) []string {
	for len(paragraphs) > 0 {
		first := sanitizeThinkingHeadingInput(paragraphs[0])
		if first == "" {
			paragraphs = paragraphs[1:]
			continue
		}
		if reasoningLeadLabelLine.MatchString(first) {
			paragraphs = paragraphs[1:]
			continue
		}
		if match := reasoningLeadLabelPrefix.FindStringSubmatch(first); len(match) == 2 {
			remainder := sanitizeThinkingParagraph(match[1])
			if remainder == "" {
				paragraphs = paragraphs[1:]
				continue
			}
			paragraphs = append([]string{remainder}, paragraphs[1:]...)
		}
		break
	}
	return paragraphs
}

func splitThinkingParagraphs(raw string) []string {
	raw = normalizeAssistantLineEndings(raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	paragraphs := make([]string, 0, 4)
	current := make([]string, 0, len(lines))
	flush := func() {
		if len(current) == 0 {
			return
		}
		paragraph := strings.TrimSpace(strings.Join(current, "\n"))
		current = current[:0]
		if paragraph != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return paragraphs
}

func splitThinkingHeading(paragraph string) (heading, remainder string, ok bool) {
	normalized := sanitizeThinkingHeadingInput(paragraph)
	if normalized == "" {
		return "", "", false
	}
	match := thinkingSummaryBoldLead.FindStringSubmatch(normalized)
	if len(match) != 3 {
		return "", "", false
	}
	heading = sanitizeThinkingParagraph(match[1])
	remainder = sanitizeThinkingParagraph(match[2])
	if heading == "" {
		return "", "", false
	}
	return heading, remainder, true
}

func sanitizeThinkingHeadingInput(raw string) string {
	raw = normalizeAssistantLineEndings(raw)
	raw = thinkingSummaryXMLTag.ReplaceAllString(raw, " ")
	raw = thinkingSummaryBracketTag.ReplaceAllString(raw, " ")
	return strings.TrimSpace(thinkingSummaryWhitespace.ReplaceAllString(raw, " "))
}

func sanitizeThinkingParagraph(raw string) string {
	raw = normalizeAssistantLineEndings(raw)
	raw = thinkingSummaryXMLTag.ReplaceAllString(raw, " ")
	raw = thinkingSummaryBracketTag.ReplaceAllString(raw, " ")
	raw = thinkingSummaryCollapsed.ReplaceAllString(raw, " ")
	raw = strings.ReplaceAll(raw, "**", "")
	raw = strings.ReplaceAll(raw, "__", "")

	normalized := thinkingSummaryWhitespace.ReplaceAllString(strings.TrimSpace(raw), " ")
	if normalized == "" {
		return ""
	}
	for {
		stripped := thinkingSummaryLeading.ReplaceAllString(normalized, "$1$2")
		stripped = thinkingSummaryTrailing.ReplaceAllString(stripped, "$1$2")
		stripped = thinkingSummaryWhitespace.ReplaceAllString(strings.TrimSpace(stripped), " ")
		if stripped == normalized {
			break
		}
		normalized = stripped
	}
	return dedupeThinkingSummarySentence(strings.TrimSpace(normalized))
}

func dedupeRepeatedThinkingBlocks(blocks []thinkingDisplayBlock) []thinkingDisplayBlock {
	if len(blocks) < 2 || len(blocks)%2 != 0 {
		return blocks
	}
	half := len(blocks) / 2
	for i := 0; i < half; i++ {
		left := blocks[i]
		right := blocks[i+half]
		if left.Bold != right.Bold {
			return blocks
		}
		if normalizeThinkingSummary(left.Text) != normalizeThinkingSummary(right.Text) {
			return blocks
		}
	}
	return append([]thinkingDisplayBlock(nil), blocks[:half]...)
}

func firstSentence(text string) string {
	runes := []rune(text)
	for i, r := range runes {
		switch r {
		case '.', '!', '?':
			chunk := strings.TrimSpace(string(runes[:i+1]))
			if chunk != "" {
				return chunk
			}
		}
	}
	return strings.TrimSpace(text)
}

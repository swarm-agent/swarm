package codex

import "strings"

const emptyReasoningSummaryHTMLComment = "<!-- -->"

type ReasoningSummaryFormatter struct{}

func (ReasoningSummaryFormatter) NormalizeSummary(summary string) string {
	return normalizeReasoningSummary(summary)
}

func (ReasoningSummaryFormatter) MergeDelta(current, delta string) string {
	if strings.TrimSpace(delta) == "" {
		return normalizeReasoningSummary(current)
	}
	return mergeReasoningSummaryChunk(current, delta)
}

// isEmptyReasoningSummaryPart recognizes the provider's structured empty-part
// sentinel without treating a literal HTML comment embedded in real prose as
// empty. Codex summary indexes keep parts separate, so this check belongs at
// the provider boundary before the part is persisted or rendered.
func isEmptyReasoningSummaryPart(summary string) bool {
	summary = strings.TrimSpace(strings.ReplaceAll(summary, "\r\n", "\n"))
	if summary == "" || isEmptyReasoningSummaryHTMLComment(summary) {
		return true
	}
	_, body, hasHeading := splitReasoningSummaryHeading(summary)
	return hasHeading && isEmptyReasoningSummaryHTMLComment(strings.TrimSpace(body))
}

func isEmptyReasoningSummaryHTMLComment(value string) bool {
	value = strings.TrimSpace(value)
	if value == emptyReasoningSummaryHTMLComment {
		return true
	}
	if !strings.HasPrefix(value, "<!--") || !strings.HasSuffix(value, "-->") {
		return false
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "<!--"), "-->")) == ""
}

func splitReasoningSummaryHeading(summary string) (heading, body string, ok bool) {
	summary = strings.TrimSpace(summary)
	for _, marker := range []string{"**", "__"} {
		if !strings.HasPrefix(summary, marker) {
			continue
		}
		remainder := summary[len(marker):]
		closeIndex := strings.Index(remainder, marker)
		if closeIndex <= 0 {
			continue
		}
		headingEnd := len(marker) + closeIndex + len(marker)
		return summary[:headingEnd], strings.TrimSpace(summary[headingEnd:]), true
	}
	return "", summary, false
}

func isHeadingOnlyReasoningSummaryPart(summary string) bool {
	_, body, hasHeading := splitReasoningSummaryHeading(summary)
	return hasHeading && strings.TrimSpace(body) == ""
}

func shouldEmitReasoningSummaryPart(summary string, final bool) bool {
	if isEmptyReasoningSummaryPart(summary) {
		return false
	}
	return final || !isHeadingOnlyReasoningSummaryPart(summary)
}

func mergeReasoningSummaryEvent(previous, delta string, snapshot bool) string {
	if isEmptyReasoningSummaryPart(delta) || (isHeadingOnlyReasoningSummaryPart(previous) && isEmptyReasoningSummaryHTMLComment(delta)) {
		return ""
	}
	if snapshot {
		return mergeReasoningSummarySnapshot(previous, delta)
	}
	return mergeReasoningSummaryChunk(previous, delta)
}

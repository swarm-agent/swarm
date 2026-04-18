package codex

import "strings"

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

package codex

import "testing"

func TestEmptyReasoningSummaryPartClassification(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		empty   bool
	}{
		{name: "comment only", summary: "<!-- -->", empty: true},
		{name: "heading and comment", summary: "**Trying to resolve this**\n\n<!-- -->", empty: true},
		{name: "underscored heading and comment", summary: "__Checking tests__\r\n\r\n<!--   -->", empty: true},
		{name: "real body", summary: "**Plan**\n\ndone", empty: false},
		{name: "bold only content", summary: "**Important conclusion**", empty: false},
		{name: "literal comment in prose", summary: "**Plan**\n\nUse `<!-- -->` in JSX.", empty: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyReasoningSummaryPart(tc.summary); got != tc.empty {
				t.Fatalf("isEmptyReasoningSummaryPart(%q) = %t, want %t", tc.summary, got, tc.empty)
			}
		})
	}
}

func TestReasoningSummaryPartEmissionWaitsForBody(t *testing.T) {
	if shouldEmitReasoningSummaryPart("**Trying to resolve this**", false) {
		t.Fatal("heading-only partial summary emitted before its body")
	}
	if !shouldEmitReasoningSummaryPart("**Important conclusion**", true) {
		t.Fatal("completed bold-only summary was suppressed")
	}
	if shouldEmitReasoningSummaryPart("**Trying to resolve this**\n\n<!-- -->", true) {
		t.Fatal("completed empty placeholder summary was emitted")
	}
	if !shouldEmitReasoningSummaryPart("**Trying to resolve this**\n\nFound the cause.", false) {
		t.Fatal("summary with real body was suppressed")
	}
}

func TestMergeReasoningSummaryEventClearsStreamedEmptyPart(t *testing.T) {
	const heading = "**Trying to resolve this**"
	if got := mergeReasoningSummaryEvent("", heading, false); got != heading {
		t.Fatalf("merged heading = %q, want %q", got, heading)
	}
	if got := mergeReasoningSummaryEvent(heading, "\n\n<!-- -->", false); got != "" {
		t.Fatalf("merged empty body = %q, want empty", got)
	}
	if got := mergeReasoningSummaryEvent(heading, heading+"\n\n<!-- -->", true); got != "" {
		t.Fatalf("merged empty snapshot = %q, want empty", got)
	}
	if got := mergeReasoningSummaryEvent(heading, heading+"\n\nFound the cause.", true); got != heading+"\n\nFound the cause." {
		t.Fatalf("merged real snapshot = %q", got)
	}
}

func TestExtractReasoningSummaryFromOutputDropsOnlyEmptyParts(t *testing.T) {
	output := []any{map[string]any{
		"type": "reasoning",
		"summary": []any{
			map[string]any{"type": "summary_text", "text": "**Plan**\n\ndone"},
			map[string]any{"type": "summary_text", "text": "**Checking tests**\n\n<!-- -->"},
			map[string]any{"type": "summary_text", "text": "**Important conclusion**"},
		},
	}}
	const want = "**Plan**\n\ndone\n\n**Important conclusion**"
	if got := extractReasoningSummaryFromOutput(output); got != want {
		t.Fatalf("extracted reasoning summary = %q, want %q", got, want)
	}
}

func TestNormalizeReasoningSummaryDropsEmptyPlaceholderPart(t *testing.T) {
	if got := normalizeReasoningSummary("**Trying to resolve this**\n\n<!-- -->"); got != "" {
		t.Fatalf("normalized empty summary = %q, want empty", got)
	}
	const literal = "**Plan**\n\nUse `<!-- -->` in JSX."
	if got := normalizeReasoningSummary(literal); got != literal {
		t.Fatalf("normalized literal comment summary = %q, want %q", got, literal)
	}
}

package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestAssistantMarkdownRows_BlockStyling(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("# Heading\n- item\n> quoted\n```\ncode\n```", p.theme.Accent)
	if len(rows) < 7 {
		t.Fatalf("assistantMarkdownRows() len = %d, want >= 7", len(rows))
	}
	if rows[0].Text != "Heading" {
		t.Fatalf("rows[0].Text = %q, want %q", rows[0].Text, "Heading")
	}
	if rows[1].Text != "" {
		t.Fatalf("rows[1].Text = %q, want blank spacer", rows[1].Text)
	}
	if rows[2].Text != "• item" {
		t.Fatalf("rows[2].Text = %q, want %q", rows[2].Text, "• item")
	}
	if rows[3].Text != "" {
		t.Fatalf("rows[3].Text = %q, want blank spacer", rows[3].Text)
	}
	if rows[4].Text != "│ quoted" {
		t.Fatalf("rows[4].Text = %q, want %q", rows[4].Text, "│ quoted")
	}
	if rows[5].Text != "" {
		t.Fatalf("rows[5].Text = %q, want blank spacer", rows[5].Text)
	}
	if rows[6].Text != "  code" {
		t.Fatalf("rows[6].Text = %q, want %q", rows[6].Text, "  code")
	}
}

func TestAssistantMarkdownRows_TildeCodeFenceStyling(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("~~~go\nfunc main() {}\n~~~", p.theme.Accent)
	if len(rows) == 0 {
		t.Fatalf("expected rows for tilde code fence")
	}
	if got := rows[0].Text; got != "  func main() {}" {
		t.Fatalf("rows[0].Text = %q, want %q", got, "  func main() {}")
	}
}

func TestAssistantMarkdownRows_RecoversFromShortBacktickFenceClose(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("```\ncode\n``\n# Heading", p.theme.Accent)
	if len(rows) < 3 {
		t.Fatalf("expected code row + spacer + heading row, got %d", len(rows))
	}
	if got := rows[0].Text; got != "  code" {
		t.Fatalf("rows[0].Text = %q, want %q", got, "  code")
	}
	if got := rows[1].Text; got != "" {
		t.Fatalf("rows[1].Text = %q, want blank spacer", got)
	}
	if got := rows[2].Text; got != "Heading" {
		t.Fatalf("rows[2].Text = %q, want %q", got, "Heading")
	}
}

func TestAssistantMarkdownRows_InsertsTopLevelBlockSpacing(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("Paragraph one\n\nParagraph two", p.theme.Accent)
	if len(rows) < 3 {
		t.Fatalf("assistantMarkdownRows() len = %d, want >= 3", len(rows))
	}
	if got := rows[0].Text; got != "Paragraph one" {
		t.Fatalf("rows[0].Text = %q, want %q", got, "Paragraph one")
	}
	if got := rows[1].Text; got != "" {
		t.Fatalf("rows[1].Text = %q, want blank spacer", got)
	}
	if got := rows[2].Text; got != "Paragraph two" {
		t.Fatalf("rows[2].Text = %q, want %q", got, "Paragraph two")
	}
}

func TestAssistantInlineMarkdownSpans_MixedFormatting(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	spans, hasLink := p.assistantInlineMarkdownSpans("**bold** _italic_ `code` [docs](https://example.com)", p.theme.MarkdownText)
	if !hasLink {
		t.Fatalf("hasLink = false, want true")
	}
	if len(spans) < 4 {
		t.Fatalf("len(spans) = %d, want >= 4", len(spans))
	}

	got := chatRenderSpansText(spans)
	want := "bold italic code docs (https://example.com)"
	if got != want {
		t.Fatalf("chatRenderSpansText(spans) = %q, want %q", got, want)
	}

	hasBold := false
	hasItalic := false
	hasCode := false
	hasLinkStyle := false
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		_, _, attrs := span.Style.Decompose()
		if attrs&tcell.AttrBold != 0 {
			hasBold = true
		}
		if attrs&tcell.AttrItalic != 0 {
			hasItalic = true
		}
		if span.Text == "code" {
			hasCode = true
		}
		if span.Text == "docs (https://example.com)" {
			hasLinkStyle = true
		}
	}
	if !hasBold {
		t.Fatalf("expected at least one bold span")
	}
	if !hasItalic {
		t.Fatalf("expected at least one italic span")
	}
	if !hasCode {
		t.Fatalf("expected inline code span")
	}
	if !hasLinkStyle {
		t.Fatalf("expected link span text")
	}
}

func TestAssistantMarkdownRows_NumberedCommitSummaryRendersInlineMarkdown(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	body := "1. **`f5264357b3a64dd28bac924cea3617ced938b64`**\n**ui: suppress read tool.started preview to avoid duplicate 0-lines**\n• Files changed:\n• `internal/ui/chat_page.go`\n• `internal/ui/chat_stream_status_test.go`\n\n2. **`8e87e092e2e35e232ad14698b38fb6f06451f7e`**\n**docs: add critical next-day worktree and dual-lane priorities**\n• Files changed:\n• `docs/master-fix-roadmap.md`"
	rows := p.assistantMarkdownRows(body, p.theme.Accent)
	if len(rows) == 0 {
		t.Fatalf("expected markdown rows for commit summary")
	}

	hasFirstNumber := false
	hasSecondNumber := false
	hasBold := false
	hasCode := false
	for _, row := range rows {
		if strings.Contains(row.Text, "**") || strings.Contains(row.Text, "`") {
			t.Fatalf("markdown markers leaked in rendered row: %q", row.Text)
		}
		if strings.HasPrefix(row.Text, "1. ") {
			hasFirstNumber = true
		}
		if strings.HasPrefix(row.Text, "2. ") {
			hasSecondNumber = true
		}
		for _, span := range row.Spans {
			_, _, attrs := span.Style.Decompose()
			if attrs&tcell.AttrBold != 0 {
				hasBold = true
			}
			if stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeFunction)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeKeyword)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeString)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeNumber)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeOperator)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeType)) ||
				stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCode)) {
				hasCode = true
			}
		}
	}
	if !hasFirstNumber {
		t.Fatalf("expected ordered list prefix 1. in rendered rows")
	}
	if !hasSecondNumber {
		t.Fatalf("expected ordered list prefix 2. in rendered rows")
	}
	if !hasBold {
		t.Fatalf("expected bold styling from markdown strong markers")
	}
	if !hasCode {
		t.Fatalf("expected code styling from markdown code spans")
	}
}

func TestAssistantMarkdownRows_ListStrongCodePreservesCodeAndBoldStyles(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("- **`f526435`**", p.theme.Accent)
	if len(rows) == 0 {
		t.Fatalf("expected markdown rows for list item")
	}
	first := rows[0]
	if first.Text != "• f526435" {
		t.Fatalf("rows[0].Text = %q, want %q", first.Text, "• f526435")
	}
	if strings.Contains(first.Text, "**") || strings.Contains(first.Text, "`") {
		t.Fatalf("markdown markers leaked in rendered row: %q", first.Text)
	}
	if len(first.Spans) == 0 {
		t.Fatalf("expected styled spans for list row")
	}

	wantCodeStyles := []tcell.Style{
		p.theme.MarkdownCodeFunction,
		p.theme.MarkdownCodeKeyword,
		p.theme.MarkdownCodeString,
		p.theme.MarkdownCodeNumber,
		p.theme.MarkdownCodeOperator,
		p.theme.MarkdownCodeType,
		p.theme.MarkdownCode,
	}
	isCodeStyle := func(style tcell.Style) bool {
		for _, want := range wantCodeStyles {
			if markdownStyleExtendsBase(style, want) {
				return true
			}
		}
		return false
	}

	hasBoldCode := false
	for _, span := range first.Spans {
		if strings.TrimSpace(span.Text) != "f526435" {
			continue
		}
		_, _, attrs := span.Style.Decompose()
		if attrs&tcell.AttrBold == 0 {
			continue
		}
		if isCodeStyle(span.Style) {
			hasBoldCode = true
			break
		}
	}
	if !hasBoldCode {
		t.Fatalf("expected bold + code styling on nested strong/code list token; spans=%#v", first.Spans)
	}
}

func TestWrapRenderLineWithCustomPrefixes_PreservesSpans(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	line := chatRenderLine{
		Style: p.theme.MarkdownText,
		Spans: []chatRenderSpan{
			{Text: "abcdef", Style: p.theme.MarkdownText},
			{Text: "ghijkl", Style: p.theme.MarkdownCode},
		},
	}

	wrapped := wrapRenderLineWithCustomPrefixes("□ ", "  ", line, 8)
	if len(wrapped) < 2 {
		t.Fatalf("len(wrapped) = %d, want >= 2", len(wrapped))
	}
	if wrapped[0].Text != "□ abcdef" {
		t.Fatalf("wrapped[0].Text = %q, want %q", wrapped[0].Text, "□ abcdef")
	}
	if wrapped[1].Text != "  ghijkl" {
		t.Fatalf("wrapped[1].Text = %q, want %q", wrapped[1].Text, "  ghijkl")
	}
}

func TestAssistantMarkdownRows_NestedListContinuationPreservesIndentation(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	body := "1. Keep the existing first-add behavior that seeds the global model and built-in utility subagents.\n2. Remove the post-onboarding fallback that currently reassigns subagents when their provider is blank.\n   - Result: adding or activating credentials after the first provider will not mutate agent settings.\n   - Add/adjust coverage in tests/swarmd/internal/api/auth_defaults_test.go to lock in the rule that later auth-key additions do not reapply agent defaults or override user-managed agent settings.\n   - Leave the UI behavior unchanged except for the backend no longer returning applied auto-defaults after the first onboarding, which prevents the misleading \"defaults applied\" messaging on later key additions."

	rows := p.assistantMarkdownRows(body, p.theme.Accent)
	if len(rows) == 0 {
		t.Fatalf("expected markdown rows for nested list")
	}
	texts := make([]string, 0, len(rows))
	for _, row := range rows {
		texts = append(texts, row.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "1. Keep the existing first-add behavior") {
		t.Fatalf("expected first ordered item in rendered rows: %q", joined)
	}
	if !strings.Contains(joined, "2. Remove the post-onboarding fallback") {
		t.Fatalf("expected second ordered item in rendered rows: %q", joined)
	}
	if !strings.Contains(joined, "   built-in utility subagents.") {
		t.Fatalf("expected wrapped continuation to preserve ordered-list indent: %q", joined)
	}
	if !strings.Contains(joined, "   provider is blank.") {
		t.Fatalf("expected second ordered item continuation to preserve indent: %q", joined)
	}
	if !strings.Contains(joined, "   • Result: adding or activating") {
		t.Fatalf("expected nested bullet prefix in rendered rows: %q", joined)
	}
	if !strings.Contains(joined, "     credentials after the first") {
		t.Fatalf("expected nested bullet continuation indent in rendered rows: %q", joined)
	}
	if !strings.Contains(joined, "   • Add/adjust coverage in") {
		t.Fatalf("expected second nested bullet in rendered rows: %q", joined)
	}
	if !strings.Contains(joined, "     tests/swarmd/internal/api/auth_defaults_test.go") {
		t.Fatalf("expected second nested bullet continuation indent in rendered rows: %q", joined)
	}
}

func TestAssistantInlineMarkdownSpans_PreservesWhitespace(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	spans, _ := p.assistantInlineMarkdownSpans("a  b", p.theme.MarkdownText)
	if got := chatRenderSpansText(spans); got != "a  b" {
		t.Fatalf("chatRenderSpansText(spans) = %q, want %q", got, "a  b")
	}
}

func TestAssistantInlineMarkdownSpans_InlineCodeUsesUniformCodeStyle(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	spans, _ := p.assistantInlineMarkdownSpans("run `go test ./internal/ui -run TestRender` now", p.theme.MarkdownText)

	hasCode := false
	hasTokenHighlight := false
	for _, span := range spans {
		switch {
		case strings.Contains(span.Text, "go test ./internal/ui -run TestRender") && stylesEqual(span.Style, p.theme.MarkdownCode):
			hasCode = true
		case stylesEqual(span.Style, p.theme.MarkdownCodeFunction) ||
			stylesEqual(span.Style, p.theme.MarkdownCodeKeyword) ||
			stylesEqual(span.Style, p.theme.MarkdownCodeString) ||
			stylesEqual(span.Style, p.theme.MarkdownCodeNumber) ||
			stylesEqual(span.Style, p.theme.MarkdownCodeOperator) ||
			stylesEqual(span.Style, p.theme.MarkdownCodeType):
			hasTokenHighlight = true
		}
	}
	if !hasCode {
		t.Fatalf("expected uniform inline code styling; spans=%#v", spans)
	}
	if hasTokenHighlight {
		t.Fatalf("expected inline code to avoid token-level syntax highlighting; spans=%#v", spans)
	}
}

func TestAssistantMarkdownRows_PlainFileLabelDoesNotHighlightPath(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("File: internal/ui/chat_component_toolstream_test.go", p.theme.Accent)
	if len(rows) == 0 || len(rows[0].Spans) == 0 {
		t.Fatalf("expected rendered spans for file label")
	}
	wantPathStyle := p.markdownPathStyleForBase(p.theme.MarkdownText)
	for _, span := range rows[0].Spans {
		if strings.Contains(span.Text, "internal/ui/chat_component_toolstream_test.go") && stylesEqual(span.Style, wantPathStyle) {
			t.Fatalf("expected plain prose path to keep markdown text styling; spans=%#v", rows[0].Spans)
		}
	}
}

func TestAssistantMarkdownRows_PlainTextDoesNotSyntaxHighlightKeywords(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	rows := p.assistantMarkdownRows("When I wrote func main as plain text, it should stay plain.", p.theme.Accent)
	if len(rows) == 0 || len(rows[0].Spans) == 0 {
		t.Fatalf("expected rendered spans for plain prose")
	}
	for _, span := range rows[0].Spans {
		if span.Text == "func" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeKeyword)) {
			t.Fatalf("expected plain prose to avoid syntax-highlighted keywords; spans=%#v", rows[0].Spans)
		}
	}
}

func TestAssistantMarkdownRows_CodeFenceCompilerLineHighlightsPath(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	body := "```text\ninternal/ui/chat_components_shell.go:169:2: undefined: foo\n```"
	rows := p.assistantMarkdownRows(body, p.theme.Accent)
	if len(rows) == 0 {
		t.Fatalf("expected rows for compiler output code fence")
	}
	hasPath := false
	for _, row := range rows {
		for _, span := range row.Spans {
			if strings.Contains(span.Text, "internal/ui/chat_components_shell.go:169:2:") && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeFunction)) {
				hasPath = true
			}
		}
	}
	if !hasPath {
		t.Fatalf("expected filepath token highlighting in fenced compiler output")
	}
}

func TestAssistantMarkdownRows_CodeFenceSyntaxHighlight(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	body := "```go\nfunc add(x int) string {\n  // note\n  return \"ok\" + strconv.Itoa(42)\n}\n```"
	rows := p.assistantMarkdownRows(body, p.theme.Accent)
	if len(rows) < 4 {
		t.Fatalf("assistantMarkdownRows() len = %d, want >= 4", len(rows))
	}

	var (
		hasKeyword  bool
		hasType     bool
		hasString   bool
		hasNumber   bool
		hasComment  bool
		hasFunction bool
		hasOperator bool
	)

	for _, row := range rows {
		for _, span := range row.Spans {
			switch {
			case span.Text == "func" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeKeyword)):
				hasKeyword = true
			case (span.Text == "int" || span.Text == "string") && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeType)):
				hasType = true
			case span.Text == "\"ok\"" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeString)):
				hasString = true
			case span.Text == "42" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeNumber)):
				hasNumber = true
			case span.Text == "// note" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeComment)):
				hasComment = true
			case span.Text == "add" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeFunction)):
				hasFunction = true
			case span.Text == "+" && stylesEqual(span.Style, styleWithoutBackground(p.theme.MarkdownCodeOperator)):
				hasOperator = true
			}
		}
	}

	if !hasKeyword {
		t.Fatalf("expected keyword-highlighted span")
	}
	if !hasType {
		t.Fatalf("expected type-highlighted span")
	}
	if !hasString {
		t.Fatalf("expected string-highlighted span")
	}
	if !hasNumber {
		t.Fatalf("expected number-highlighted span")
	}
	if !hasComment {
		t.Fatalf("expected comment-highlighted span")
	}
	if !hasFunction {
		t.Fatalf("expected function-highlighted span")
	}
	if !hasOperator {
		t.Fatalf("expected operator-highlighted span")
	}
}

func stylesEqual(a, b tcell.Style) bool {
	afg, abg, aa := a.Decompose()
	bfg, bbg, ba := b.Decompose()
	return afg == bfg && abg == bbg && aa == ba
}

func styleWithoutBackground(style tcell.Style) tcell.Style {
	fg, _, attrs := style.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(tcell.ColorDefault).Attributes(attrs)
}

func TestLiveAssistantFallbackLines_RenderMarkdownBullets(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	p.liveAssistant = "## Plan\n- step 1\n- step 2"

	rows := p.liveAssistantParseFallbackLines(80)
	if len(rows) < 4 {
		t.Fatalf("expected markdown rows for live assistant, got %d", len(rows))
	}
	joined := make([]string, 0, len(rows))
	for _, row := range rows {
		joined = append(joined, row.Text)
	}
	text := strings.Join(joined, "\n")
	if !strings.Contains(text, "Plan") {
		t.Fatalf("missing heading in live assistant rendering: %q", text)
	}
	if !strings.Contains(text, "• step 1") || !strings.Contains(text, "• step 2") {
		t.Fatalf("bullet rows not rendered correctly: %q", text)
	}
}

func TestLiveAssistantFallbackLines_RenderMarkdownNumberedList(t *testing.T) {
	p := NewChatPage(ChatPageOptions{SessionID: "s1", ShowHeader: true})
	p.liveAssistant = "1. first\n2. second"

	rows := p.liveAssistantParseFallbackLines(80)
	joined := make([]string, 0, len(rows))
	for _, row := range rows {
		joined = append(joined, row.Text)
	}
	text := strings.Join(joined, "\n")
	if !strings.Contains(text, "1. first") || !strings.Contains(text, "2. second") {
		t.Fatalf("numbered list rows not rendered correctly: %q", text)
	}
}

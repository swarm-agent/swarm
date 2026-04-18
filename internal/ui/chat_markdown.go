package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/gdamore/tcell/v2"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

var (
	chatMarkdownParser = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
	).Parser()
)

func (p *ChatPage) renderAssistantMarkdownMessageLines(firstPrefix, continuationPrefix, body string, width int, baseStyle tcell.Style) []chatRenderLine {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if body == "" {
		body = " "
	}
	rows := p.assistantMarkdownRows(body, baseStyle)
	out := make([]chatRenderLine, 0, len(rows)*2)
	firstLine := true
	for _, row := range rows {
		if chatRenderLineText(row) == "" {
			out = append(out, chatRenderLine{Text: "", Style: row.Style})
			continue
		}
		prefix := continuationPrefix
		if firstLine {
			prefix = firstPrefix
		}
		wrapped := wrapRenderLineWithCustomPrefixes(prefix, continuationPrefix, row, width)
		for _, wrappedLine := range wrapped {
			out = append(out, wrappedLine)
			firstLine = false
		}
	}
	if len(out) == 0 {
		out = append(out, chatRenderLine{Text: clampEllipsis(firstPrefix, width), Style: baseStyle})
	}
	return out
}

func (p *ChatPage) renderAssistantMarkdownBubble(body string, width int, style tcell.Style, title string) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	lines := []chatRenderLine{
		{Text: clampEllipsis(title, width), Style: style},
	}
	content := p.renderAssistantMarkdownMessageLines("│ ", "│ ", body, width, style)
	lines = append(lines, content...)
	lines = append(lines, chatRenderLine{Text: clampEllipsis("╰", width), Style: style})
	return lines
}

func (p *ChatPage) assistantMarkdownRows(body string, baseStyle tcell.Style) []chatRenderLine {
	return p.renderMarkdownRows(body, p.theme.MarkdownText, baseStyle)
}

func (p *ChatPage) renderMarkdownRows(body string, textStyle, fallbackStyle tcell.Style) []chatRenderLine {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	if normalized == "" {
		normalized = " "
	}
	normalized = recoverShortBacktickFenceClosures(normalized)
	source := []byte(normalized)
	doc := chatMarkdownParser.Parse(gmtext.NewReader(source))

	renderer := chatMarkdownASTRenderer{
		page:   p,
		source: source,
	}
	out := renderer.renderBlocks(doc, markdownASTBlockContext{
		Prefix:       "",
		TextStyle:    textStyle,
		BlockPadding: true,
	})
	if len(out) == 0 {
		out = append(out, chatRenderLine{Text: " ", Style: fallbackStyle})
	}
	return out
}

func (p *ChatPage) assistantSingleMarkdownRow(body string, baseStyle tcell.Style) (chatRenderLine, bool) {
	rows := p.renderMarkdownRows(body, baseStyle, baseStyle)
	nonEmpty := make([]chatRenderLine, 0, len(rows))
	for _, row := range rows {
		if chatRenderLineText(row) == "" {
			continue
		}
		if len(row.Spans) == 0 && row.Text != "" {
			row.Spans = []chatRenderSpan{{Text: row.Text, Style: row.Style}}
		}
		nonEmpty = append(nonEmpty, row)
	}
	if len(nonEmpty) != 1 {
		return chatRenderLine{}, false
	}
	return nonEmpty[0], true
}

type markdownASTBlockContext struct {
	Prefix       string
	TextStyle    tcell.Style
	BlockPadding bool
}

type chatMarkdownASTRenderer struct {
	page   *ChatPage
	source []byte
}

func recoverShortBacktickFenceClosures(body string) string {
	lines := strings.Split(body, "\n")
	fence := markdownFenceState{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\t \r"))
		fenceLine, ok := parseMarkdownFenceLine(trimmed)
		if !ok {
			continue
		}
		if !fence.active() {
			if fenceLine.Marker == '`' && fenceLine.Count >= 3 {
				fence = markdownFenceState{
					Active: true,
					Marker: '`',
					Count:  fenceLine.Count,
				}
			}
			continue
		}
		if fence.canClose(fenceLine) {
			fence = markdownFenceState{}
			continue
		}
		if fence.shouldRecoverClose(fenceLine) {
			leading := line[:len(line)-len(strings.TrimLeft(line, "\t "))]
			lines[i] = leading + strings.Repeat(string(fence.Marker), fence.Count)
			fence = markdownFenceState{}
		}
	}
	return strings.Join(lines, "\n")
}

func (r chatMarkdownASTRenderer) renderBlocks(parent gast.Node, ctx markdownASTBlockContext) []chatRenderLine {
	out := make([]chatRenderLine, 0, 8)
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		rendered := r.renderBlock(node, ctx)
		if len(rendered) == 0 {
			continue
		}
		if ctx.BlockPadding && len(out) > 0 && chatRenderLineText(out[len(out)-1]) != "" && chatRenderLineText(rendered[0]) != "" {
			out = append(out, chatRenderLine{Text: "", Style: ctx.TextStyle})
		}
		out = append(out, rendered...)
	}
	return out
}

func (r chatMarkdownASTRenderer) renderBlock(node gast.Node, ctx markdownASTBlockContext) []chatRenderLine {
	switch n := node.(type) {
	case *gast.Heading:
		style := r.page.theme.MarkdownHeading
		if n.Level >= 3 {
			style = style.Dim(true)
		}
		return r.renderInlineBlock(n, ctx.Prefix, style)
	case *gast.Paragraph:
		return r.renderInlineBlock(n, ctx.Prefix, ctx.TextStyle)
	case *gast.Blockquote:
		inner := ctx
		inner.Prefix = ctx.Prefix + "│ "
		inner.TextStyle = r.page.theme.MarkdownQuote
		return r.renderBlocks(n, inner)
	case *gast.List:
		return r.renderList(n, ctx)
	case *gast.FencedCodeBlock:
		return r.renderCodeLines(n.Lines(), normalizeCodeFenceLanguage(string(n.Language(r.source))), ctx)
	case *gast.CodeBlock:
		return r.renderCodeLines(n.Lines(), "", ctx)
	case *gast.ThematicBreak:
		line := markdownLineWithInlineSpans(ctx.Prefix, []chatRenderSpan{{Text: "────────", Style: r.page.theme.MarkdownRule}}, r.page.theme.MarkdownRule)
		return []chatRenderLine{line}
	case *gast.HTMLBlock:
		return r.renderPlainBlockLines(n.Lines(), ctx, ctx.TextStyle)
	case *gast.TextBlock:
		if n.HasChildren() {
			return r.renderInlineBlock(n, ctx.Prefix, ctx.TextStyle)
		}
		return r.renderPlainBlockLines(n.Lines(), ctx, ctx.TextStyle)
	default:
		if node.HasChildren() {
			return r.renderBlocks(node, ctx)
		}
	}
	return nil
}

func (r chatMarkdownASTRenderer) renderList(list *gast.List, ctx markdownASTBlockContext) []chatRenderLine {
	out := make([]chatRenderLine, 0, 8)
	itemIndex := list.Start
	if itemIndex <= 0 {
		itemIndex = 1
	}
	for node := list.FirstChild(); node != nil; node = node.NextSibling() {
		item, ok := node.(*gast.ListItem)
		if !ok {
			continue
		}
		itemLines := r.renderBlocks(item, markdownASTBlockContext{
			Prefix:       "",
			TextStyle:    r.page.theme.MarkdownList,
			BlockPadding: true,
		})
		marker := "• "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", itemIndex)
			itemIndex++
		}
		renderedItem := prefixListLines(itemLines, ctx.Prefix, marker, r.page.theme.MarkdownList)
		if len(renderedItem) == 0 {
			continue
		}
		if len(out) > 0 && chatRenderLineText(out[len(out)-1]) != "" && chatRenderLineText(renderedItem[0]) != "" {
			out = append(out, chatRenderLine{Text: "", Style: ctx.TextStyle})
		}
		out = append(out, renderedItem...)
	}
	return out
}

func prefixListLines(lines []chatRenderLine, outerPrefix, marker string, markerStyle tcell.Style) []chatRenderLine {
	if len(lines) == 0 {
		return []chatRenderLine{{
			Text:  outerPrefix + marker,
			Style: markerStyle,
			Spans: []chatRenderSpan{{Text: outerPrefix + marker, Style: markerStyle}},
		}}
	}
	out := make([]chatRenderLine, 0, len(lines))
	continuation := strings.Repeat(" ", utf8.RuneCountInString(marker))
	for i, line := range lines {
		prefix := continuation
		if i == 0 {
			prefix = marker
		}
		out = append(out, prefixRenderLine(outerPrefix+prefix, line, markerStyle))
	}
	return out
}

func prefixRenderLine(prefix string, line chatRenderLine, prefixStyle tcell.Style) chatRenderLine {
	if prefix == "" {
		return line
	}
	if chatRenderLineText(line) == "" {
		return chatRenderLine{Text: "", Style: line.Style}
	}
	body := cloneRenderSpans(line.Spans)
	if len(body) == 0 && line.Text != "" {
		body = []chatRenderSpan{{Text: line.Text, Style: line.Style}}
	}
	return markdownLineWithInlineSpans(prefix, body, prefixStyle)
}

func (r chatMarkdownASTRenderer) renderCodeLines(lines *gmtext.Segments, language string, ctx markdownASTBlockContext) []chatRenderLine {
	if lines == nil || lines.Len() == 0 {
		return nil
	}
	out := make([]chatRenderLine, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		raw := strings.TrimRight(string(segment.Value(r.source)), "\r\n")
		if strings.TrimSpace(raw) == "" {
			out = append(out, chatRenderLine{Text: "", Style: r.page.theme.MarkdownText})
			continue
		}
		line := markdownLineWithInlineSpans("  ", flattenCodeFenceBackground(r.page.highlightCodeFenceLine(raw, language)), r.page.theme.MarkdownText)
		out = append(out, prefixRenderLine(ctx.Prefix, line, ctx.TextStyle))
	}
	return out
}

func (r chatMarkdownASTRenderer) renderPlainBlockLines(lines *gmtext.Segments, ctx markdownASTBlockContext, style tcell.Style) []chatRenderLine {
	if lines == nil || lines.Len() == 0 {
		return nil
	}
	out := make([]chatRenderLine, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		raw := strings.TrimRight(string(segment.Value(r.source)), "\r\n")
		if raw == "" {
			out = append(out, chatRenderLine{Text: "", Style: style})
			continue
		}
		line := markdownLineWithInlineSpans(ctx.Prefix, r.page.markdownDecoratePlainText(raw, style), style)
		out = append(out, line)
	}
	return out
}

func (r chatMarkdownASTRenderer) renderInlineBlock(node gast.Node, prefix string, style tcell.Style) []chatRenderLine {
	inlineLines, _ := r.renderInlineChildren(node, style)
	if len(inlineLines) == 0 {
		return nil
	}
	out := make([]chatRenderLine, 0, len(inlineLines))
	for _, inline := range inlineLines {
		inline = compactRenderSpans(inline)
		if len(inline) == 0 {
			out = append(out, chatRenderLine{Text: "", Style: style})
			continue
		}
		out = append(out, markdownLineWithInlineSpans(prefix, inline, style))
	}
	return out
}

func (r chatMarkdownASTRenderer) renderInlineChildren(node gast.Node, baseStyle tcell.Style) ([][]chatRenderSpan, bool) {
	lines := [][]chatRenderSpan{{}}
	hasLink := false
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		segmentLines, segmentLink := r.renderInlineNode(child, baseStyle)
		if len(segmentLines) == 0 {
			continue
		}
		mergeInlineLineSpans(&lines, segmentLines)
		hasLink = hasLink || segmentLink
	}
	return lines, hasLink
}

func mergeInlineLineSpans(dst *[][]chatRenderSpan, src [][]chatRenderSpan) {
	if len(src) == 0 {
		return
	}
	if len(*dst) == 0 {
		*dst = append(*dst, []chatRenderSpan{})
	}
	last := len(*dst) - 1
	(*dst)[last] = append((*dst)[last], src[0]...)
	for i := 1; i < len(src); i++ {
		line := make([]chatRenderSpan, 0, len(src[i]))
		line = append(line, src[i]...)
		*dst = append(*dst, line)
	}
}

func markdownInlineContainsURL(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://")
}

func markdownInlineNodeText(node gast.Node, source []byte) string {
	var b strings.Builder
	var walk func(gast.Node)
	walk = func(parent gast.Node) {
		for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
			switch n := child.(type) {
			case *gast.Text:
				b.Write(n.Segment.Value(source))
				if n.HardLineBreak() || n.SoftLineBreak() {
					b.WriteByte(' ')
				}
			case *gast.String:
				b.Write(n.Value)
			default:
				if child.HasChildren() {
					walk(child)
					continue
				}
				b.Write(child.Text(source))
			}
		}
	}
	walk(node)
	return b.String()
}

func markdownRawHTMLText(node *gast.RawHTML, source []byte) string {
	if node == nil || node.Segments == nil || node.Segments.Len() == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < node.Segments.Len(); i++ {
		segment := node.Segments.At(i)
		b.Write(segment.Value(source))
	}
	return b.String()
}

func mergeMarkdownInlineStyleAttrs(baseStyle, targetStyle tcell.Style) tcell.Style {
	_, _, baseAttrs := baseStyle.Decompose()
	if baseAttrs == 0 {
		return targetStyle
	}
	fg, bg, attrs := targetStyle.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs | baseAttrs)
}

func mergeMarkdownInlineSpanStyles(spans []chatRenderSpan, baseStyle tcell.Style) []chatRenderSpan {
	if len(spans) == 0 {
		return nil
	}
	_, _, baseAttrs := baseStyle.Decompose()
	if baseAttrs == 0 {
		return compactRenderSpans(spans)
	}
	out := make([]chatRenderSpan, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		out = append(out, chatRenderSpan{
			Text:  span.Text,
			Style: mergeMarkdownInlineStyleAttrs(baseStyle, span.Style),
		})
	}
	return compactRenderSpans(out)
}

func (r chatMarkdownASTRenderer) renderInlineNode(node gast.Node, baseStyle tcell.Style) ([][]chatRenderSpan, bool) {
	switch n := node.(type) {
	case *gast.Text:
		text := string(n.Segment.Value(r.source))
		lines := [][]chatRenderSpan{{}}
		if text != "" {
			lines[0] = append(lines[0], r.page.markdownDecoratePlainText(text, baseStyle)...)
		}
		if n.HardLineBreak() || n.SoftLineBreak() {
			lines = append(lines, []chatRenderSpan{})
		}
		return lines, markdownInlineContainsURL(text)
	case *gast.String:
		text := string(n.Value)
		if text == "" {
			return [][]chatRenderSpan{{}}, false
		}
		if n.IsCode() {
			spans := mergeMarkdownInlineSpanStyles(r.page.inlineCodeMarkdownSpans(text), baseStyle)
			return [][]chatRenderSpan{spans}, false
		}
		return [][]chatRenderSpan{r.page.markdownDecoratePlainText(text, baseStyle)}, markdownInlineContainsURL(text)
	case *gast.CodeSpan:
		spans := mergeMarkdownInlineSpanStyles(r.page.inlineCodeMarkdownSpans(markdownInlineNodeText(n, r.source)), baseStyle)
		return [][]chatRenderSpan{spans}, false
	case *gast.Emphasis:
		style := baseStyle.Italic(true)
		if n.Level >= 2 {
			style = baseStyle.Bold(true)
		}
		return r.renderInlineChildren(n, style)
	case *extensionast.Strikethrough:
		return r.renderInlineChildren(n, baseStyle.Dim(true))
	case *gast.Link:
		label := strings.TrimSpace(markdownInlineNodeText(n, r.source))
		url := strings.TrimSpace(string(n.Destination))
		linkText := markdownLinkText(label, url, false)
		linkStyle := mergeMarkdownInlineStyleAttrs(baseStyle, r.page.theme.MarkdownLink)
		return [][]chatRenderSpan{{{Text: linkText, Style: linkStyle}}}, true
	case *gast.Image:
		label := strings.TrimSpace(markdownInlineNodeText(n, r.source))
		url := strings.TrimSpace(string(n.Destination))
		linkText := markdownLinkText(label, url, true)
		linkStyle := mergeMarkdownInlineStyleAttrs(baseStyle, r.page.theme.MarkdownLink)
		return [][]chatRenderSpan{{{Text: linkText, Style: linkStyle}}}, true
	case *gast.AutoLink:
		label := strings.TrimSpace(string(n.Label(r.source)))
		url := strings.TrimSpace(string(n.URL(r.source)))
		linkText := markdownLinkText(label, url, false)
		linkStyle := mergeMarkdownInlineStyleAttrs(baseStyle, r.page.theme.MarkdownLink)
		return [][]chatRenderSpan{{{Text: linkText, Style: linkStyle}}}, true
	case *gast.RawHTML:
		text := markdownRawHTMLText(n, r.source)
		return [][]chatRenderSpan{r.page.markdownDecoratePlainText(text, baseStyle)}, markdownInlineContainsURL(text)
	default:
		if node.HasChildren() {
			return r.renderInlineChildren(node, baseStyle)
		}
		text := string(node.Text(r.source))
		if text == "" {
			return [][]chatRenderSpan{{}}, false
		}
		return [][]chatRenderSpan{r.page.markdownDecoratePlainText(text, baseStyle)}, markdownInlineContainsURL(text)
	}
}

func (p *ChatPage) normalizeMarkdownInlinePathTokens(spans []chatRenderSpan, baseStyle tcell.Style) []chatRenderSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]chatRenderSpan, 0, len(spans))
	tokenParts := make([]chatRenderSpan, 0, 4)
	flushToken := func() {
		if len(tokenParts) == 0 {
			return
		}
		tokenText := chatRenderSpansText(tokenParts)
		if looksLikePathToken(tokenText) {
			out = append(out, chatRenderSpan{Text: tokenText, Style: p.markdownPathStyleForBase(baseStyle)})
		} else {
			out = append(out, tokenParts...)
		}
		tokenParts = tokenParts[:0]
	}
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		if p.markdownTokenStyleProtected(span.Style) {
			flushToken()
			out = append(out, span)
			continue
		}
		runes := []rune(span.Text)
		for i := 0; i < len(runes); {
			if unicode.IsSpace(runes[i]) {
				flushToken()
				j := i + 1
				for j < len(runes) && unicode.IsSpace(runes[j]) {
					j++
				}
				out = append(out, chatRenderSpan{Text: string(runes[i:j]), Style: span.Style})
				i = j
				continue
			}
			j := i + 1
			for j < len(runes) && !unicode.IsSpace(runes[j]) {
				j++
			}
			part := chatRenderSpan{Text: string(runes[i:j]), Style: span.Style}
			tokenParts = append(tokenParts, part)
			i = j
		}
	}
	flushToken()
	return compactRenderSpans(out)
}

func (p *ChatPage) markdownTokenStyleProtected(style tcell.Style) bool {
	for _, candidate := range []tcell.Style{
		p.theme.MarkdownCode,
		p.theme.MarkdownCodeKeyword,
		p.theme.MarkdownCodeType,
		p.theme.MarkdownCodeString,
		p.theme.MarkdownCodeNumber,
		p.theme.MarkdownCodeComment,
		p.theme.MarkdownCodeFunction,
		p.theme.MarkdownCodeOperator,
		p.theme.MarkdownLink,
	} {
		if markdownStyleExtendsBase(style, candidate) {
			return true
		}
	}
	return false
}

func markdownStyleExtendsBase(style, base tcell.Style) bool {
	sfg, sbg, sattrs := style.Decompose()
	bfg, bbg, battrs := base.Decompose()
	if sfg != bfg || sbg != bbg {
		return false
	}
	return sattrs&battrs == battrs
}

func markdownStylesEqualExact(a, b tcell.Style) bool {
	afg, abg, aa := a.Decompose()
	bfg, bbg, ba := b.Decompose()
	return afg == bfg && abg == bbg && aa == ba
}

type markdownFenceLine struct {
	Marker byte
	Count  int
	Info   string
}

type markdownFenceState struct {
	Active   bool
	Marker   byte
	Count    int
	Language string
}

func parseMarkdownFenceLine(line string) (markdownFenceLine, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return markdownFenceLine{}, false
	}
	marker := line[0]
	if marker != '`' && marker != '~' {
		return markdownFenceLine{}, false
	}
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	if count == 0 {
		return markdownFenceLine{}, false
	}
	return markdownFenceLine{
		Marker: marker,
		Count:  count,
		Info:   strings.TrimSpace(line[count:]),
	}, true
}

func (f markdownFenceState) active() bool {
	return f.Active && f.Count >= 3 && (f.Marker == '`' || f.Marker == '~')
}

func (f markdownFenceState) canClose(line markdownFenceLine) bool {
	if !f.active() {
		return false
	}
	if line.Marker != f.Marker {
		return false
	}
	if strings.TrimSpace(line.Info) != "" {
		return false
	}
	return line.Count >= f.Count
}

func (f markdownFenceState) shouldRecoverClose(line markdownFenceLine) bool {
	if !f.active() {
		return false
	}
	if f.Marker != '`' || line.Marker != f.Marker {
		return false
	}
	if strings.TrimSpace(line.Info) != "" {
		return false
	}
	return f.Count == 3 && line.Count == 2
}

func (p *ChatPage) assistantInlineMarkdownSpans(text string, baseStyle tcell.Style) ([]chatRenderSpan, bool) {
	if text == "" {
		return nil, false
	}
	leading, inline, trailing := splitMarkdownEdgeWhitespace(text)
	if inline == "" {
		return []chatRenderSpan{{Text: text, Style: baseStyle}}, false
	}
	if strings.TrimSpace(inline) == "" {
		return []chatRenderSpan{{Text: text, Style: baseStyle}}, false
	}
	source := []byte(inline)
	doc := chatMarkdownParser.Parse(gmtext.NewReader(source))
	renderer := chatMarkdownASTRenderer{
		page:   p,
		source: source,
	}
	inlineLines, hasLink := renderer.renderInlineChildren(doc, baseStyle)
	spans := make([]chatRenderSpan, 0, 8)
	if leading != "" {
		spans = append(spans, chatRenderSpan{Text: leading, Style: baseStyle})
	}
	appendedContent := false
	for _, line := range inlineLines {
		line = compactRenderSpans(line)
		if len(line) == 0 {
			continue
		}
		if appendedContent {
			spans = append(spans, chatRenderSpan{Text: " ", Style: baseStyle})
		}
		spans = append(spans, line...)
		appendedContent = true
	}
	if trailing != "" {
		spans = append(spans, chatRenderSpan{Text: trailing, Style: baseStyle})
	}
	spans = compactRenderSpans(spans)
	if len(spans) == 0 {
		spans = []chatRenderSpan{{Text: text, Style: baseStyle}}
	}
	return spans, hasLink
}

func splitMarkdownEdgeWhitespace(text string) (leading, core, trailing string) {
	if text == "" {
		return "", "", ""
	}
	runes := []rune(text)
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	end := len(runes)
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return string(runes[:start]), string(runes[start:end]), string(runes[end:])
}

func (p *ChatPage) markdownDecoratePlainText(text string, baseStyle tcell.Style) []chatRenderSpan {
	if text == "" {
		return nil
	}
	return []chatRenderSpan{{Text: text, Style: baseStyle}}
}

func (p *ChatPage) markdownPathStyleForBase(baseStyle tcell.Style) tcell.Style {
	return p.chatSyntaxPalette(baseStyle).Path
}

func (p *ChatPage) inlineCodeMarkdownSpans(content string) []chatRenderSpan {
	if content == "" {
		return nil
	}
	return []chatRenderSpan{{Text: content, Style: p.theme.MarkdownCode}}
}

func markdownLinkText(label, url string, image bool) string {
	label = strings.TrimSpace(label)
	url = strings.TrimSpace(url)
	if label == "" {
		if image {
			label = "image"
		} else {
			label = url
		}
	}
	if url == "" {
		return label
	}
	return fmt.Sprintf("%s (%s)", label, url)
}

func markdownLineWithInlineSpans(prefix string, inline []chatRenderSpan, baseStyle tcell.Style) chatRenderLine {
	spans := make([]chatRenderSpan, 0, len(inline)+1)
	if prefix != "" {
		spans = append(spans, chatRenderSpan{Text: prefix, Style: baseStyle})
	}
	for _, span := range inline {
		if span.Text == "" {
			continue
		}
		spans = append(spans, span)
	}
	text := chatRenderSpansText(spans)
	if text == "" {
		return chatRenderLine{Text: "", Style: baseStyle}
	}
	return chatRenderLine{Text: text, Style: baseStyle, Spans: spans}
}

func normalizeCodeFenceLanguage(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	lang := strings.TrimSpace(fields[0])
	switch lang {
	case "golang":
		return "go"
	case "javascript", "node", "mjs", "cjs", "jsx":
		return "js"
	case "typescript", "tsx":
		return "ts"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "c++", "cc", "cxx", "hpp", "hxx":
		return "cpp"
	case "yml":
		return "yaml"
	case "zsh", "shell":
		return "bash"
	case "rb":
		return "ruby"
	default:
		return lang
	}
}

func (p *ChatPage) highlightCodeFenceLine(line, language string) []chatRenderSpan {
	if line == "" {
		return nil
	}
	if spans, ok := p.highlightCodeFenceLineWithChroma(line, language); ok {
		return spans
	}
	spans := []chatRenderSpan{{Text: line, Style: p.theme.MarkdownCode}}
	return p.markdownAccentPathTokensInCodeSpans(spans)
}

func (p *ChatPage) highlightCodeFenceLineWithChroma(line, language string) ([]chatRenderSpan, bool) {
	lang := normalizeCodeFenceLanguage(language)
	if lang == "" {
		return nil, false
	}
	lexer := chromaLexerForCodeLanguage(lang)
	if lexer == nil {
		return nil, false
	}
	tokens, err := lexer.Tokenise(nil, line)
	if err != nil {
		return nil, false
	}
	shellLike := isShellLanguage(lang)
	out := make([]chatRenderSpan, 0, len(line)/2+2)
	for token := tokens(); token != chroma.EOF; token = tokens() {
		if token.Value == "" {
			continue
		}
		out = append(out, p.markdownCodeSpansForChromaToken(token.Type, token.Value, shellLike)...)
	}
	out = p.markdownAccentPathTokensInCodeSpans(out)
	out = compactRenderSpans(out)
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func chromaLexerForCodeLanguage(language string) chroma.Lexer {
	lexerLang := language
	if isShellLanguage(language) {
		// Fish lexer yields richer command/flag tokens than bash/sh in chroma.
		lexerLang = "fish"
	}
	lexer := lexers.Get(lexerLang)
	if lexer == nil {
		return nil
	}
	return chroma.Coalesce(lexer)
}

func (p *ChatPage) markdownCodeSpansForChromaToken(tokenType chroma.TokenType, tokenValue string, shellLike bool) []chatRenderSpan {
	if tokenValue == "" {
		return nil
	}
	if shellLike && tokenType == chroma.Text {
		return p.markdownShellTextSpans(tokenValue)
	}
	return []chatRenderSpan{{
		Text:  tokenValue,
		Style: p.markdownCodeStyleForChromaToken(tokenType, tokenValue, shellLike),
	}}
}

func (p *ChatPage) markdownCodeStyleForChromaToken(tokenType chroma.TokenType, tokenValue string, shellLike bool) tcell.Style {
	value := strings.TrimSpace(tokenValue)
	if value != "" && looksLikePathToken(value) && !tokenType.InCategory(chroma.Comment) && !tokenType.InSubCategory(chroma.LiteralString) {
		return p.theme.MarkdownCodeFunction
	}
	if shellLike {
		switch {
		case tokenType == chroma.NameAttribute:
			return p.theme.MarkdownCodeKeyword
		case tokenType.InSubCategory(chroma.NameFunction):
			return p.theme.MarkdownCodeFunction
		case tokenType == chroma.NameBuiltin || tokenType.InCategory(chroma.Keyword):
			return p.theme.MarkdownCodeFunction
		}
	}
	if tokenType == chroma.KeywordType || tokenType == chroma.NameBuiltin || tokenType == chroma.NameBuiltinPseudo {
		return p.theme.MarkdownCodeType
	}
	if tokenType.InCategory(chroma.Keyword) {
		return p.theme.MarkdownCodeKeyword
	}
	if tokenType.InSubCategory(chroma.NameFunction) {
		return p.theme.MarkdownCodeFunction
	}
	if tokenType.InSubCategory(chroma.LiteralNumber) {
		return p.theme.MarkdownCodeNumber
	}
	if tokenType.InSubCategory(chroma.LiteralString) {
		return p.theme.MarkdownCodeString
	}
	if tokenType.InCategory(chroma.Comment) {
		return p.theme.MarkdownCodeComment
	}
	if tokenType.InCategory(chroma.Operator) || tokenType == chroma.Punctuation {
		return p.theme.MarkdownCodeOperator
	}
	return p.theme.MarkdownCode
}

func (p *ChatPage) markdownShellTextSpans(text string) []chatRenderSpan {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	out := make([]chatRenderSpan, 0, len(runes)/2+1)
	inComment := false
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			style := p.theme.MarkdownCode
			if inComment {
				style = p.theme.MarkdownCodeComment
			}
			out = append(out, chatRenderSpan{Text: string(runes[i:j]), Style: style})
			i = j
			continue
		}

		j := i + 1
		for j < len(runes) && !unicode.IsSpace(runes[j]) {
			j++
		}
		token := string(runes[i:j])
		style := p.theme.MarkdownCode
		switch {
		case inComment:
			style = p.theme.MarkdownCodeComment
		case strings.HasPrefix(token, "#"):
			style = p.theme.MarkdownCodeComment
			inComment = true
		case isLikelyFlagToken(token):
			style = p.theme.MarkdownCodeKeyword
		case looksLikePathToken(token):
			style = p.theme.MarkdownCodeFunction
		case looksNumericToken(token):
			style = p.theme.MarkdownCodeNumber
		}
		out = append(out, chatRenderSpan{Text: token, Style: style})
		i = j
	}
	return compactRenderSpans(out)
}

func (p *ChatPage) markdownAccentPathTokensInCodeSpans(spans []chatRenderSpan) []chatRenderSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]chatRenderSpan, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		if markdownStylesEqualExact(span.Style, p.theme.MarkdownCodeString) || markdownStylesEqualExact(span.Style, p.theme.MarkdownCodeComment) {
			out = append(out, span)
			continue
		}
		runes := []rune(span.Text)
		for i := 0; i < len(runes); {
			if unicode.IsSpace(runes[i]) {
				j := i + 1
				for j < len(runes) && unicode.IsSpace(runes[j]) {
					j++
				}
				out = append(out, chatRenderSpan{Text: string(runes[i:j]), Style: span.Style})
				i = j
				continue
			}
			j := i + 1
			for j < len(runes) && !unicode.IsSpace(runes[j]) {
				j++
			}
			token := string(runes[i:j])
			style := span.Style
			if looksLikePathToken(token) {
				style = p.theme.MarkdownCodeFunction
			}
			out = append(out, chatRenderSpan{Text: token, Style: style})
			i = j
		}
	}
	return compactRenderSpans(out)
}

func isShellLanguage(lang string) bool {
	switch normalizeCodeFenceLanguage(lang) {
	case "bash", "sh", "zsh", "fish":
		return true
	default:
		return false
	}
}

func flattenCodeFenceBackground(spans []chatRenderSpan) []chatRenderSpan {
	if len(spans) == 0 {
		return spans
	}
	out := make([]chatRenderSpan, 0, len(spans))
	for _, span := range spans {
		fg, _, attrs := span.Style.Decompose()
		out = append(out, chatRenderSpan{
			Text:  span.Text,
			Style: tcell.StyleDefault.Foreground(fg).Background(tcell.ColorDefault).Attributes(attrs),
		})
	}
	return out
}

func wrapRenderLineWithCustomPrefixes(firstPrefix, continuationPrefix string, line chatRenderLine, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	if continuationPrefix == "" {
		continuationPrefix = strings.Repeat(" ", len([]rune(firstPrefix)))
	}

	lineSpans := cloneRenderSpans(line.Spans)
	if len(lineSpans) == 0 {
		if line.Text == "" {
			return []chatRenderLine{{Text: firstPrefix, Style: line.Style, Spans: []chatRenderSpan{{Text: firstPrefix, Style: line.Style}}}}
		}
		lineSpans = []chatRenderSpan{{Text: line.Text, Style: line.Style}}
	}
	if renderSpansRuneCount(lineSpans) == 0 {
		return []chatRenderLine{{Text: firstPrefix, Style: line.Style, Spans: []chatRenderSpan{{Text: firstPrefix, Style: line.Style}}}}
	}

	firstPrefixRunes := []rune(firstPrefix)
	firstPrefixW := len(firstPrefixRunes)
	if firstPrefixW >= width {
		truncated := string(firstPrefixRunes[:width])
		return []chatRenderLine{{
			Text:  truncated,
			Style: line.Style,
			Spans: []chatRenderSpan{{Text: truncated, Style: line.Style}},
		}}
	}

	continuationRunes := []rune(continuationPrefix)
	continuationW := len(continuationRunes)
	if continuationW >= width {
		continuationPrefix = strings.Repeat(" ", maxInt(0, width-1))
		continuationW = len([]rune(continuationPrefix))
	}

	out := make([]chatRenderLine, 0, 4)
	remaining := lineSpans
	prefix := firstPrefix
	available := maxInt(1, width-firstPrefixW)
	continuationAvailable := maxInt(1, width-continuationW)
	for renderSpansRuneCount(remaining) > 0 {
		if renderSpansRuneCount(remaining) <= available {
			out = append(out, composeRenderLineWithPrefix(prefix, line.Style, remaining))
			remaining = nil
			break
		}

		headEnd, tailStart := wrapRenderLineBreakIndices(remaining, available)
		if headEnd <= 0 {
			headEnd = minInt(available, renderSpansRuneCount(remaining))
			tailStart = headEnd
		}
		head, tail := splitRenderSpansAtRange(remaining, headEnd, tailStart)
		out = append(out, composeRenderLineWithPrefix(prefix, line.Style, head))
		remaining = tail
		prefix = continuationPrefix
		available = continuationAvailable
	}
	if len(out) == 0 {
		return []chatRenderLine{{Text: firstPrefix, Style: line.Style, Spans: []chatRenderSpan{{Text: firstPrefix, Style: line.Style}}}}
	}
	return out
}

func wrapMarkdownRenderLine(line chatRenderLine, width int) []chatRenderLine {
	prefix, continuation, ok := markdownWrapPrefixes(chatRenderLineText(line))
	if !ok || prefix == "" {
		return wrapRenderLineWithCustomPrefixes("", "", line, width)
	}
	prefixRunes := utf8.RuneCountInString(prefix)
	lineSpans := cloneRenderSpans(line.Spans)
	if len(lineSpans) == 0 {
		if line.Text == "" {
			return wrapRenderLineWithCustomPrefixes(prefix, continuation, chatRenderLine{Style: line.Style}, width)
		}
		lineSpans = []chatRenderSpan{{Text: line.Text, Style: line.Style}}
	}
	if renderSpansRuneCount(lineSpans) < prefixRunes {
		return wrapRenderLineWithCustomPrefixes("", "", line, width)
	}
	_, bodySpans := splitRenderSpansByRunes(lineSpans, prefixRunes)
	body := chatRenderLine{Style: line.Style, Spans: bodySpans}
	if len(bodySpans) > 0 {
		body.Text = chatRenderSpansText(bodySpans)
	}
	return wrapRenderLineWithCustomPrefixes(prefix, continuation, body, width)
}

func markdownWrapPrefixes(text string) (string, string, bool) {
	runes := []rune(text)
	if len(runes) == 0 {
		return "", "", false
	}
	indent := 0
	for indent < len(runes) && isWrapSpace(runes[indent]) {
		indent++
	}
	if prefixLen, ok := markdownListWrapPrefixLength(runes, indent); ok {
		prefix := string(runes[:prefixLen])
		return prefix, strings.Repeat(" ", prefixLen), true
	}
	if prefixLen, ok := markdownQuoteWrapPrefixLength(runes, indent); ok {
		prefix := string(runes[:prefixLen])
		return prefix, prefix, true
	}
	if indent >= 2 {
		prefix := string(runes[:2])
		return prefix, prefix, true
	}
	return "", "", false
}

func markdownListWrapPrefixLength(runes []rune, indent int) (int, bool) {
	if indent >= len(runes) {
		return 0, false
	}
	if runes[indent] == '•' && indent+1 < len(runes) && isWrapSpace(runes[indent+1]) {
		prefixLen := indent + 2
		for prefixLen < len(runes) && isWrapSpace(runes[prefixLen]) {
			prefixLen++
		}
		return prefixLen, true
	}
	cursor := indent
	for cursor < len(runes) && unicode.IsDigit(runes[cursor]) {
		cursor++
	}
	if cursor == indent || cursor+1 >= len(runes) {
		return 0, false
	}
	if (runes[cursor] != '.' && runes[cursor] != ')') || !isWrapSpace(runes[cursor+1]) {
		return 0, false
	}
	prefixLen := cursor + 2
	for prefixLen < len(runes) && isWrapSpace(runes[prefixLen]) {
		prefixLen++
	}
	return prefixLen, true
}

func markdownQuoteWrapPrefixLength(runes []rune, indent int) (int, bool) {
	cursor := indent
	for cursor+1 < len(runes) && runes[cursor] == '│' && isWrapSpace(runes[cursor+1]) {
		cursor += 2
	}
	if cursor == indent {
		return 0, false
	}
	return cursor, true
}

func composeRenderLineWithPrefix(prefix string, prefixStyle tcell.Style, body []chatRenderSpan) chatRenderLine {
	spans := make([]chatRenderSpan, 0, len(body)+1)
	if prefix != "" {
		spans = append(spans, chatRenderSpan{Text: prefix, Style: prefixStyle})
	}
	for _, span := range body {
		if span.Text == "" {
			continue
		}
		spans = append(spans, span)
	}
	text := chatRenderSpansText(spans)
	if text == "" {
		return chatRenderLine{Text: "", Style: prefixStyle}
	}
	return chatRenderLine{Text: text, Style: prefixStyle, Spans: spans}
}

func splitRenderSpansAtRange(spans []chatRenderSpan, headEnd, tailStart int) ([]chatRenderSpan, []chatRenderSpan) {
	total := renderSpansRuneCount(spans)
	if headEnd < 0 {
		headEnd = 0
	}
	if tailStart < headEnd {
		tailStart = headEnd
	}
	if headEnd > total {
		headEnd = total
	}
	if tailStart > total {
		tailStart = total
	}
	if headEnd == tailStart {
		return splitRenderSpansByRunes(spans, headEnd)
	}
	head, rest := splitRenderSpansByRunes(spans, headEnd)
	_, tail := splitRenderSpansByRunes(rest, tailStart-headEnd)
	return head, tail
}

func wrapRenderLineBreakIndices(spans []chatRenderSpan, width int) (headEnd, tailStart int) {
	if width <= 0 {
		return 0, 0
	}
	flat := []rune(chatRenderSpansText(spans))
	return wrapLineBreakIndicesRunes(flat, width)
}

func splitRenderSpansByRunes(spans []chatRenderSpan, limit int) ([]chatRenderSpan, []chatRenderSpan) {
	if limit <= 0 {
		return nil, cloneRenderSpans(spans)
	}

	head := make([]chatRenderSpan, 0, len(spans))
	tail := make([]chatRenderSpan, 0, len(spans))
	remaining := limit

	for i, span := range spans {
		if span.Text == "" {
			continue
		}
		runes := []rune(span.Text)
		if remaining <= 0 {
			tail = append(tail, chatRenderSpan{Text: string(runes), Style: span.Style})
			tail = append(tail, cloneRenderSpans(spans[i+1:])...)
			break
		}
		if len(runes) <= remaining {
			head = append(head, chatRenderSpan{Text: string(runes), Style: span.Style})
			remaining -= len(runes)
			continue
		}

		head = append(head, chatRenderSpan{Text: string(runes[:remaining]), Style: span.Style})
		tail = append(tail, chatRenderSpan{Text: string(runes[remaining:]), Style: span.Style})
		tail = append(tail, cloneRenderSpans(spans[i+1:])...)
		remaining = 0
		break
	}

	return compactRenderSpans(head), compactRenderSpans(tail)
}

func compactRenderSpans(spans []chatRenderSpan) []chatRenderSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]chatRenderSpan, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		out = append(out, span)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneRenderSpans(spans []chatRenderSpan) []chatRenderSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]chatRenderSpan, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		out = append(out, span)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneRenderLines(lines []chatRenderLine) []chatRenderLine {
	if len(lines) == 0 {
		return nil
	}
	out := make([]chatRenderLine, 0, len(lines))
	for _, line := range lines {
		cloned := chatRenderLine{Text: line.Text, Style: line.Style}
		if len(line.Spans) > 0 {
			cloned.Spans = cloneRenderSpans(line.Spans)
		}
		out = append(out, cloned)
	}
	return out
}

func chatRenderSpansText(spans []chatRenderSpan) string {
	if len(spans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		b.WriteString(span.Text)
	}
	return b.String()
}

func renderSpansRuneCount(spans []chatRenderSpan) int {
	total := 0
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		total += utf8.RuneCountInString(span.Text)
	}
	return total
}

func chatRenderLineText(line chatRenderLine) string {
	if len(line.Spans) > 0 {
		return chatRenderSpansText(line.Spans)
	}
	return line.Text
}

func appendRenderLineSuffix(line chatRenderLine, suffix string, suffixStyle tcell.Style, width int) chatRenderLine {
	if suffix == "" {
		return line
	}
	if len(line.Spans) > 0 {
		spans := cloneRenderSpans(line.Spans)
		spans = append(spans, chatRenderSpan{Text: suffix, Style: suffixStyle})
		if width > 0 && renderSpansRuneCount(spans) > width {
			head, _ := splitRenderSpansByRunes(spans, width)
			spans = head
		}
		line.Spans = spans
		line.Text = chatRenderSpansText(spans)
		return line
	}
	text := line.Text + suffix
	if width > 0 {
		runes := []rune(text)
		if len(runes) > width {
			text = string(runes[:width])
		}
	}
	line.Text = text
	return line
}

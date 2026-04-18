package ui

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type chatSyntaxSurface uint8

const (
	chatSyntaxSurfaceTool chatSyntaxSurface = iota
	chatSyntaxSurfaceMarkdown
	chatSyntaxSurfaceCommand
)

type chatSyntaxRole uint8

const (
	chatSyntaxRolePlain chatSyntaxRole = iota
	chatSyntaxRoleVerb
	chatSyntaxRoleCommand
	chatSyntaxRoleFlag
	chatSyntaxRolePath
	chatSyntaxRolePattern
	chatSyntaxRoleString
	chatSyntaxRoleNumber
	chatSyntaxRoleKeyword
	chatSyntaxRoleOperator
)

type chatSyntaxRequest struct {
	Surface            chatSyntaxSurface
	PreferredTool      string
	PreferCommand      bool
	AllowInlineCommand bool
}

type chatSyntaxLexToken struct {
	Text  string
	Space bool
}

type chatSyntaxSpan struct {
	Text string
	Role chatSyntaxRole
}

type chatSyntaxClassifierState struct {
	preferredTool  string
	previousToken  string
	wordIndex      int
	commandContext bool
	verbSeen       bool
}

type chatSyntaxPalette struct {
	Plain    tcell.Style
	Verb     tcell.Style
	Command  tcell.Style
	Flag     tcell.Style
	Path     tcell.Style
	Pattern  tcell.Style
	String   tcell.Style
	Number   tcell.Style
	Keyword  tcell.Style
	Operator tcell.Style
}

func (p *ChatPage) styleToolSummaryLine(text, toolNameHint string, baseStyle tcell.Style) chatRenderLine {
	if text == "" {
		return chatRenderLine{Text: "", Style: baseStyle}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return chatRenderLine{Text: text, Style: baseStyle}
	}
	if strings.HasPrefix(trimmed, "|") {
		return chatRenderLine{Text: text, Style: baseStyle}
	}
	preferredTool := strings.ToLower(strings.TrimSpace(toolNameHint))
	if isPlainToolSummaryTool(preferredTool) {
		return chatRenderLine{Text: text, Style: baseStyle}
	}

	line := p.styleSyntaxLine(text, chatSyntaxRequest{
		Surface:            chatSyntaxSurfaceTool,
		PreferredTool:      preferredTool,
		PreferCommand:      preferredTool == "bash",
		AllowInlineCommand: true,
	}, baseStyle)
	if len(line.Spans) == 0 {
		return chatRenderLine{Text: text, Style: baseStyle}
	}
	line.Spans = styleZeroResultSpans(line.Spans)
	line.Text = chatRenderSpansText(line.Spans)
	return line
}

func (p *ChatPage) styleToolPreviewLine(text, toolNameHint, languageHint string, baseStyle tcell.Style, editMode bool) chatRenderLine {
	trimmed := strings.TrimSpace(text)
	if editMode && trimmed != "" {
		if strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") {
			return p.styleToolDiffLine(text, languageHint, baseStyle)
		}
	}
	preferredTool := normalizeToolToken(toolNameHint)
	if preferredTool == "" {
		preferredTool = extractToolNameHintFromHeadline(trimmed)
	}
	if line, ok := p.styleToolCodePreviewLine(text, preferredTool, languageHint, baseStyle); ok {
		return line
	}
	if line, ok := p.styleMarkdownToolPreviewLine(text, baseStyle); ok {
		return line
	}
	return plainRenderLine(text, baseStyle)
}

func (p *ChatPage) styleMarkdownToolPreviewLine(text string, baseStyle tcell.Style) (chatRenderLine, bool) {
	trimmed := strings.TrimSpace(text)
	if !looksLikeMarkdownToolPreviewLine(trimmed) {
		return chatRenderLine{}, false
	}
	line, ok := p.assistantSingleMarkdownRow(text, baseStyle)
	if !ok {
		return chatRenderLine{}, false
	}
	if strings.TrimSpace(line.Text) == "" {
		return chatRenderLine{}, false
	}
	return line, true
}

func looksLikeMarkdownToolPreviewLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "```") {
		return false
	}
	return containsMarkdownInlineMarkers(trimmed)
}

func containsMarkdownInlineMarkers(text string) bool {
	return strings.Contains(text, "**") || strings.Contains(text, "__") || strings.Contains(text, "`")
}

func plainRenderLine(text string, style tcell.Style) chatRenderLine {
	if text == "" {
		return chatRenderLine{Text: "", Style: style}
	}
	return chatRenderLine{
		Text:  text,
		Style: style,
		Spans: []chatRenderSpan{{Text: text, Style: style}},
	}
}

func (p *ChatPage) styleToolCodePreviewLine(text, toolNameHint, languageHint string, baseStyle tcell.Style) (chatRenderLine, bool) {
	toolNameHint = normalizeToolToken(toolNameHint)
	if line, ok := p.styleLocatedToolPreviewLine(text, toolNameHint, baseStyle); ok {
		return line, true
	}

	languageHint = normalizeCodeFenceLanguage(languageHint)
	if languageHint == "" {
		return chatRenderLine{}, false
	}
	prefix, body := splitPreviewLineNumberPrefix(text)
	return p.styleCodePreviewBodyLine(prefix, body, languageHint, baseStyle)
}

func (p *ChatPage) styleLocatedToolPreviewLine(text, toolNameHint string, baseStyle tcell.Style) (chatRenderLine, bool) {
	if toolNameHint != "grep" {
		return chatRenderLine{}, false
	}
	location, ok := parseToolPreviewLocation(text)
	if !ok {
		return chatRenderLine{}, false
	}
	language := inferCodeLanguageFromPath(location.Path)
	if language == "" {
		return chatRenderLine{}, false
	}

	spans := make([]chatRenderSpan, 0, 6)
	pathStyle := p.toolTokenPathStyleForBase(baseStyle)
	numberStyle := mergeSyntaxStyle(baseStyle, toolStyleWithoutBackground(p.theme.MarkdownCodeNumber), 0)
	operatorStyle := mergeSyntaxStyle(baseStyle, toolStyleWithoutBackground(p.theme.MarkdownCodeOperator), 0)

	spans = append(spans, chatRenderSpan{Text: location.Path, Style: pathStyle})
	if location.Line != "" {
		spans = append(spans,
			chatRenderSpan{Text: ":", Style: operatorStyle},
			chatRenderSpan{Text: location.Line, Style: numberStyle},
		)
	}
	if location.Column != "" {
		spans = append(spans,
			chatRenderSpan{Text: ":", Style: operatorStyle},
			chatRenderSpan{Text: location.Column, Style: numberStyle},
		)
	}
	spans = append(spans, chatRenderSpan{Text: ": ", Style: operatorStyle})

	bodyLine, ok := p.styleCodePreviewBodyLine("", location.Body, language, baseStyle)
	if !ok {
		return chatRenderLine{}, false
	}
	spans = append(spans, bodyLine.Spans...)
	spans = compactRenderSpans(spans)
	return chatRenderLine{
		Text:  chatRenderSpansText(spans),
		Style: baseStyle,
		Spans: spans,
	}, true
}

func (p *ChatPage) styleCodePreviewBodyLine(prefix, body, language string, baseStyle tcell.Style) (chatRenderLine, bool) {
	if body == "" {
		return chatRenderLine{}, false
	}
	bodySpans := flattenCodeFenceBackground(p.highlightCodeFenceLine(body, language))
	if len(bodySpans) == 0 {
		return chatRenderLine{}, false
	}
	spans := make([]chatRenderSpan, 0, len(bodySpans)+1)
	if prefix != "" {
		spans = append(spans, chatRenderSpan{Text: prefix, Style: baseStyle})
	}
	spans = append(spans, bodySpans...)
	spans = compactRenderSpans(spans)
	return chatRenderLine{
		Text:  chatRenderSpansText(spans),
		Style: baseStyle,
		Spans: spans,
	}, true
}

func splitPreviewLineNumberPrefix(text string) (prefix, body string) {
	runes := []rune(text)
	idx := 0
	for idx < len(runes) && unicode.IsSpace(runes[idx]) {
		idx++
	}
	startDigits := idx
	for idx < len(runes) && runes[idx] >= '0' && runes[idx] <= '9' {
		idx++
	}
	if idx == startDigits || idx >= len(runes) || runes[idx] != ':' {
		return "", text
	}
	idx++
	for idx < len(runes) && unicode.IsSpace(runes[idx]) {
		idx++
	}
	return string(runes[:idx]), string(runes[idx:])
}

type toolPreviewLocation struct {
	Path   string
	Line   string
	Column string
	Body   string
}

func parseToolPreviewLocation(text string) (toolPreviewLocation, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return toolPreviewLocation{}, false
	}
	firstColon := strings.IndexByte(text, ':')
	if firstColon <= 0 || firstColon >= len(text)-1 {
		return toolPreviewLocation{}, false
	}
	path := strings.TrimSpace(text[:firstColon])
	if !looksLikePathToken(path) {
		return toolPreviewLocation{}, false
	}
	rest := text[firstColon+1:]

	line, remainder, ok := consumePreviewLocationNumber(rest)
	if ok {
		column, afterColumn, hasColumn := consumePreviewLocationNumber(remainder)
		if hasColumn {
			remainder = afterColumn
		}
		body := strings.TrimLeft(remainder, " \t")
		if body == "" {
			return toolPreviewLocation{}, false
		}
		location := toolPreviewLocation{Path: path, Line: line, Body: body}
		if hasColumn {
			location.Column = column
		}
		return location, true
	}

	body := strings.TrimLeft(rest, " \t")
	if body == "" {
		return toolPreviewLocation{}, false
	}
	return toolPreviewLocation{Path: path, Body: body}, true
}

func consumePreviewLocationNumber(text string) (digits, remainder string, ok bool) {
	text = strings.TrimLeft(text, " \t")
	if text == "" {
		return "", "", false
	}
	idx := 0
	for idx < len(text) && text[idx] >= '0' && text[idx] <= '9' {
		idx++
	}
	if idx == 0 || idx >= len(text) || text[idx] != ':' {
		return "", "", false
	}
	return text[:idx], text[idx+1:], true
}

func (p *ChatPage) styleToolDiffLine(line, language string, diffStyle tcell.Style) chatRenderLine {
	if line == "" {
		return chatRenderLine{Text: "", Style: diffStyle}
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return chatRenderLine{Text: "", Style: diffStyle}
	}
	prefix := runes[0]
	if prefix != '+' && prefix != '-' {
		return p.styleToolSummaryLine(line, "edit", diffStyle)
	}

	body := string(runes[1:])
	bodySpans := flattenCodeFenceBackground(p.highlightCodeFenceLine(body, language))
	if len(bodySpans) == 0 {
		bodySpans = []chatRenderSpan{{Text: body, Style: diffStyle}}
	}

	spans := make([]chatRenderSpan, 0, len(bodySpans)+1)
	spans = append(spans, chatRenderSpan{Text: string(prefix), Style: diffStyle})
	spans = append(spans, bodySpans...)
	spans = compactRenderSpans(spans)

	return chatRenderLine{
		Text:  chatRenderSpansText(spans),
		Style: diffStyle,
		Spans: spans,
	}
}

func (p *ChatPage) styleSyntaxLine(text string, request chatSyntaxRequest, baseStyle tcell.Style) chatRenderLine {
	if text == "" {
		return chatRenderLine{Text: "", Style: baseStyle}
	}
	spans := p.styleSyntaxText(text, request, baseStyle)
	if len(spans) == 0 {
		return chatRenderLine{Text: text, Style: baseStyle}
	}
	return chatRenderLine{
		Text:  chatRenderSpansText(spans),
		Style: baseStyle,
		Spans: spans,
	}
}

func (p *ChatPage) styleSyntaxText(text string, request chatSyntaxRequest, baseStyle tcell.Style) []chatRenderSpan {
	if text == "" {
		return nil
	}
	if !request.AllowInlineCommand {
		request.AllowInlineCommand = request.Surface == chatSyntaxSurfaceTool || request.Surface == chatSyntaxSurfaceMarkdown
	}

	lexed := lexChatSyntaxTokens(text)
	if len(lexed) == 0 {
		return nil
	}
	classified := p.classifyChatSyntaxTokens(lexed, request)
	if len(classified) == 0 {
		return nil
	}

	palette := p.chatSyntaxPalette(baseStyle)
	out := make([]chatRenderSpan, 0, len(classified))
	for _, segment := range classified {
		if segment.Text == "" {
			continue
		}
		out = append(out, chatRenderSpan{
			Text:  segment.Text,
			Style: palette.styleFor(segment.Role),
		})
	}
	return compactRenderSpans(out)
}

func (p *ChatPage) classifyChatSyntaxTokens(tokens []chatSyntaxLexToken, request chatSyntaxRequest) []chatSyntaxSpan {
	state := chatSyntaxClassifierState{
		preferredTool: strings.ToLower(strings.TrimSpace(request.PreferredTool)),
	}
	out := make([]chatSyntaxSpan, 0, len(tokens)+2)
	for _, token := range tokens {
		if token.Text == "" {
			continue
		}
		if token.Space {
			out = append(out, chatSyntaxSpan{Text: token.Text, Role: chatSyntaxRolePlain})
			continue
		}
		segments, normalized := p.classifyChatSyntaxWord(token.Text, request, &state)
		out = append(out, segments...)
		if normalized != "" {
			state.previousToken = normalized
		}
		state.wordIndex++
	}
	return compactChatSyntaxSpans(out)
}

func (p *ChatPage) classifyChatSyntaxWord(raw string, request chatSyntaxRequest, state *chatSyntaxClassifierState) ([]chatSyntaxSpan, string) {
	leading, core, trailing := splitSyntaxTokenDecor(raw)
	if core == "" {
		return []chatSyntaxSpan{{Text: raw, Role: chatSyntaxRolePlain}}, ""
	}

	if key, value, ok := splitSyntaxKeyValue(core); ok {
		keyNorm := normalizeToolToken(key)
		valueSegments, valueNorm, valueRole := p.classifyChatSyntaxCore(value, request, state, keyNorm)

		segments := make([]chatSyntaxSpan, 0, len(valueSegments)+5)
		if leading != "" {
			segments = append(segments, chatSyntaxSpan{Text: leading, Role: chatSyntaxRolePlain})
		}
		segments = append(segments,
			chatSyntaxSpan{Text: key, Role: chatSyntaxRoleKeyword},
			chatSyntaxSpan{Text: "=", Role: chatSyntaxRoleOperator},
		)
		segments = append(segments, valueSegments...)
		if trailing != "" {
			segments = append(segments, chatSyntaxSpan{Text: trailing, Role: chatSyntaxRolePlain})
		}

		if valueRole == chatSyntaxRoleCommand || valueRole == chatSyntaxRoleVerb {
			state.commandContext = true
		}
		if keyNorm == "pattern" || keyNorm == "regex" {
			state.commandContext = false
		}
		if valueNorm != "" {
			state.previousToken = keyNorm
		}
		return segments, keyNorm
	}

	coreSegments, normalized, role := p.classifyChatSyntaxCore(core, request, state, "")
	segments := make([]chatSyntaxSpan, 0, len(coreSegments)+2)
	if leading != "" {
		segments = append(segments, chatSyntaxSpan{Text: leading, Role: chatSyntaxRolePlain})
	}
	segments = append(segments, coreSegments...)
	if trailing != "" {
		segments = append(segments, chatSyntaxSpan{Text: trailing, Role: chatSyntaxRolePlain})
	}

	if role == chatSyntaxRoleCommand || role == chatSyntaxRoleVerb {
		state.commandContext = true
	}
	if role == chatSyntaxRolePattern {
		state.commandContext = false
	}
	if role == chatSyntaxRoleVerb {
		state.verbSeen = true
	}
	return segments, normalized
}

func (p *ChatPage) classifyChatSyntaxCore(core string, request chatSyntaxRequest, state *chatSyntaxClassifierState, keyName string) ([]chatSyntaxSpan, string, chatSyntaxRole) {
	if core == "" {
		return nil, "", chatSyntaxRolePlain
	}

	if request.AllowInlineCommand && isBacktickToken(core) {
		content := unwrapMatchingTokenQuotes(core)
		if strings.TrimSpace(content) == "" {
			return []chatSyntaxSpan{{Text: core, Role: chatSyntaxRoleString}}, "", chatSyntaxRoleString
		}
		inner := chatSyntaxRequest{
			Surface:            chatSyntaxSurfaceCommand,
			PreferCommand:      true,
			AllowInlineCommand: false,
		}
		innerTokens := lexChatSyntaxTokens(content)
		innerSpans := p.classifyChatSyntaxTokens(innerTokens, inner)

		segments := make([]chatSyntaxSpan, 0, len(innerSpans)+2)
		segments = append(segments, chatSyntaxSpan{Text: "`", Role: chatSyntaxRoleOperator})
		segments = append(segments, innerSpans...)
		segments = append(segments, chatSyntaxSpan{Text: "`", Role: chatSyntaxRoleOperator})
		return compactChatSyntaxSpans(segments), normalizeToolToken(extractToolLeadingToken(content)), chatSyntaxRoleCommand
	}

	normalized := normalizeToolToken(core)
	role := p.classifyChatSyntaxRole(core, normalized, request, state, keyName)
	return []chatSyntaxSpan{{Text: core, Role: role}}, normalized, role
}

func (p *ChatPage) classifyChatSyntaxRole(core, normalized string, request chatSyntaxRequest, state *chatSyntaxClassifierState, keyName string) chatSyntaxRole {
	if normalized == "" {
		return chatSyntaxRolePlain
	}

	unquoted := maybeUnquoteToken(core)
	if unquoted == "" {
		unquoted = core
	}

	if keyName != "" {
		switch keyName {
		case "pattern", "regex":
			return chatSyntaxRolePattern
		case "command", "cmd":
			if isLikelyFlagToken(unquoted) {
				return chatSyntaxRoleFlag
			}
			if looksLikePathToken(unquoted) {
				return chatSyntaxRolePath
			}
			return chatSyntaxRoleCommand
		case "path", "file", "filepath", "root", "cwd":
			if looksLikePathToken(unquoted) {
				return chatSyntaxRolePath
			}
			return chatSyntaxRoleString
		case "line_start", "line_end", "count", "bytes", "exit", "exit_code", "duration", "duration_ms", "replacements":
			return chatSyntaxRoleNumber
		}
	}

	if request.Surface == chatSyntaxSurfaceTool && !state.verbSeen && shouldAccentToolToken(normalized, state.preferredTool) {
		return chatSyntaxRoleVerb
	}
	if request.Surface == chatSyntaxSurfaceTool && state.preferredTool != "" && normalized == state.preferredTool && !state.verbSeen {
		return chatSyntaxRoleVerb
	}

	if isToolQuotedToken(core) {
		if request.Surface == chatSyntaxSurfaceTool && (state.previousToken == "grep" || state.previousToken == "glob") {
			return chatSyntaxRolePattern
		}
		if looksLikePathToken(unquoted) {
			return chatSyntaxRolePath
		}
		if looksRegexPatternToken(unquoted) {
			return chatSyntaxRolePattern
		}
		if isLikelyFlagToken(unquoted) {
			return chatSyntaxRoleFlag
		}
		return chatSyntaxRoleString
	}

	if looksLikePathToken(unquoted) {
		return chatSyntaxRolePath
	}
	if looksNumericToken(unquoted) {
		return chatSyntaxRoleNumber
	}
	if isLikelyFlagToken(unquoted) && (request.Surface == chatSyntaxSurfaceCommand || request.PreferCommand || state.commandContext) {
		return chatSyntaxRoleFlag
	}
	if looksRegexPatternToken(unquoted) {
		switch state.previousToken {
		case "pattern", "regex", "match", "matches", "grep", "glob":
			return chatSyntaxRolePattern
		}
		if request.Surface == chatSyntaxSurfaceTool {
			return chatSyntaxRolePattern
		}
	}

	switch state.previousToken {
	case "in", "from", "to", "at", "path", "file", "filepath", "root", "cwd":
		if looksLikePathToken(unquoted) {
			return chatSyntaxRolePath
		}
	case "pattern", "regex", "match", "matches":
		return chatSyntaxRolePattern
	case "command", "cmd":
		return chatSyntaxRoleCommand
	}

	if request.Surface == chatSyntaxSurfaceCommand {
		if state.wordIndex == 0 || isLikelyShellCommandToken(normalized) {
			return chatSyntaxRoleCommand
		}
	}
	if request.PreferCommand && (state.wordIndex == 0 || isLikelyShellCommandToken(normalized)) {
		return chatSyntaxRoleCommand
	}
	if request.Surface == chatSyntaxSurfaceTool && isToolSummaryKeywordToken(normalized) {
		return chatSyntaxRoleKeyword
	}
	return chatSyntaxRolePlain
}

func lexChatSyntaxTokens(text string) []chatSyntaxLexToken {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	out := make([]chatSyntaxLexToken, 0, len(runes)/2+1)
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			out = append(out, chatSyntaxLexToken{Text: string(runes[i:j]), Space: true})
			i = j
			continue
		}
		end := scanChatSyntaxWordEnd(runes, i)
		if end <= i {
			end = i + 1
		}
		out = append(out, chatSyntaxLexToken{Text: string(runes[i:end])})
		i = end
	}
	return out
}

func scanChatSyntaxWordEnd(runes []rune, start int) int {
	if start < 0 || start >= len(runes) {
		return start
	}
	if unicode.IsSpace(runes[start]) {
		return start + 1
	}

	quote := rune(0)
	for idx := start; idx < len(runes); idx++ {
		r := runes[idx]
		if quote != 0 {
			if quote != '`' && r == '\\' && idx+1 < len(runes) {
				idx++
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}
		if isSyntaxQuoteRune(r) {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			return idx
		}
	}
	return len(runes)
}

func splitSyntaxTokenDecor(token string) (leading, core, trailing string) {
	if token == "" {
		return "", "", ""
	}
	runes := []rune(token)
	start := 0
	for start < len(runes) && isLeadingSyntaxDecorRune(runes[start]) {
		start++
	}
	end := len(runes)
	for end > start && isTrailingSyntaxDecorRune(runes[end-1]) {
		end--
	}
	return string(runes[:start]), string(runes[start:end]), string(runes[end:])
}

func isLeadingSyntaxDecorRune(r rune) bool {
	switch r {
	case '(', '[', '{':
		return true
	default:
		return false
	}
}

func isTrailingSyntaxDecorRune(r rune) bool {
	switch r {
	case ')', ']', '}', ',', ':', ';':
		return true
	default:
		return false
	}
}

func isSyntaxQuoteRune(r rune) bool {
	switch r {
	case '\'', '"', '`':
		return true
	default:
		return false
	}
}

func splitSyntaxKeyValue(core string) (key, value string, ok bool) {
	idx := strings.IndexRune(core, '=')
	if idx <= 0 || idx >= len(core)-1 {
		return "", "", false
	}
	key = core[:idx]
	value = core[idx+1:]
	keyNorm := normalizeToolToken(key)
	if keyNorm == "" {
		return "", "", false
	}
	return key, value, true
}

func compactChatSyntaxSpans(spans []chatSyntaxSpan) []chatSyntaxSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]chatSyntaxSpan, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Role == span.Role {
			out[len(out)-1].Text += span.Text
			continue
		}
		out = append(out, span)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *ChatPage) chatSyntaxPalette(baseStyle tcell.Style) chatSyntaxPalette {
	plain := toolStyleWithoutBackground(baseStyle)

	verbSeed := toolStyleWithoutBackground(p.theme.Accent.Bold(true))
	keywordSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeKeyword)
	functionSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeFunction)
	typeSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeType)
	stringSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeString)
	numberSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeNumber)
	operatorSeed := toolStyleWithoutBackground(p.theme.MarkdownCodeOperator)
	secondarySeed := toolStyleWithoutBackground(p.theme.Secondary)
	primarySeed := toolStyleWithoutBackground(p.theme.Primary)
	warningSeed := toolStyleWithoutBackground(p.theme.Warning)

	pathSeed := pickDistinctSyntaxSeed(plain, nil,
		functionSeed,
		stringSeed,
		keywordSeed,
		typeSeed,
		secondarySeed,
		primarySeed,
	)
	patternSeed := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed},
		stringSeed,
		keywordSeed,
		typeSeed,
		warningSeed,
		primarySeed,
	)
	commandSeed := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, patternSeed},
		functionSeed,
		primarySeed,
		secondarySeed,
		keywordSeed,
	)
	flagSeed := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, patternSeed, commandSeed},
		keywordSeed,
		operatorSeed,
		warningSeed,
	)
	keywordResolved := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, patternSeed, commandSeed},
		keywordSeed,
		operatorSeed,
	)
	stringResolved := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, commandSeed},
		stringSeed,
		patternSeed,
	)
	operatorResolved := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, patternSeed},
		operatorSeed,
		keywordSeed,
	)
	verbResolved := pickDistinctSyntaxSeed(plain, []tcell.Style{pathSeed, patternSeed},
		verbSeed,
		commandSeed,
	)

	verbStyle := mergeSyntaxStyle(baseStyle, verbResolved, tcell.AttrBold)
	commandStyle := mergeSyntaxStyle(baseStyle, commandSeed, 0)
	flagStyle := mergeSyntaxStyle(baseStyle, flagSeed, 0)
	pathStyle := mergeSyntaxStyle(baseStyle, pathSeed, 0)
	patternStyle := mergeSyntaxStyle(baseStyle, patternSeed, 0)
	patternStyle = ensureDistinctSyntaxStyle(patternStyle, pathStyle)

	return chatSyntaxPalette{
		Plain:    plain,
		Verb:     verbStyle,
		Command:  commandStyle,
		Flag:     flagStyle,
		Path:     pathStyle,
		Pattern:  patternStyle,
		String:   mergeSyntaxStyle(baseStyle, stringResolved, 0),
		Number:   mergeSyntaxStyle(baseStyle, numberSeed, 0),
		Keyword:  mergeSyntaxStyle(baseStyle, keywordResolved, 0),
		Operator: mergeSyntaxStyle(baseStyle, operatorResolved, 0),
	}
}

func ensureDistinctSyntaxStyle(style, avoid tcell.Style) tcell.Style {
	if !stylesEquivalent(style, avoid) {
		return style
	}
	for _, attr := range []tcell.AttrMask{tcell.AttrItalic, tcell.AttrBold} {
		next := addSyntaxStyleAttrs(style, attr)
		if !stylesEquivalent(next, avoid) {
			return next
		}
		style = next
	}
	return style
}

func addSyntaxStyleAttrs(style tcell.Style, attrs tcell.AttrMask) tcell.Style {
	fg, bg, current := style.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(current | attrs)
}

func mergeSyntaxStyle(baseStyle, seed tcell.Style, forceAttrs tcell.AttrMask) tcell.Style {
	_, _, baseAttrs := baseStyle.Decompose()
	seedFG, _, seedAttrs := seed.Decompose()
	attrs := baseAttrs | seedAttrs | forceAttrs
	return tcell.StyleDefault.Foreground(seedFG).Background(tcell.ColorDefault).Attributes(attrs)
}

func pickDistinctSyntaxSeed(base tcell.Style, avoid []tcell.Style, candidates ...tcell.Style) tcell.Style {
	if len(candidates) == 0 {
		return base
	}
	for _, candidate := range candidates {
		if stylesEquivalent(candidate, base) {
			continue
		}
		if styleInSet(candidate, avoid) {
			continue
		}
		return candidate
	}
	for _, candidate := range candidates {
		if styleInSet(candidate, avoid) {
			continue
		}
		return candidate
	}
	return candidates[0]
}

func styleInSet(style tcell.Style, list []tcell.Style) bool {
	for _, candidate := range list {
		if stylesEquivalent(style, candidate) {
			return true
		}
	}
	return false
}

func (palette chatSyntaxPalette) styleFor(role chatSyntaxRole) tcell.Style {
	switch role {
	case chatSyntaxRoleVerb:
		return palette.Verb
	case chatSyntaxRoleCommand:
		return palette.Command
	case chatSyntaxRoleFlag:
		return palette.Flag
	case chatSyntaxRolePath:
		return palette.Path
	case chatSyntaxRolePattern:
		return palette.Pattern
	case chatSyntaxRoleString:
		return palette.String
	case chatSyntaxRoleNumber:
		return palette.Number
	case chatSyntaxRoleKeyword:
		return palette.Keyword
	case chatSyntaxRoleOperator:
		return palette.Operator
	default:
		return palette.Plain
	}
}

func (p *ChatPage) toolTokenPathStyleForBase(baseStyle tcell.Style) tcell.Style {
	return p.chatSyntaxPalette(baseStyle).Path
}

func stylesEquivalent(a, b tcell.Style) bool {
	afg, _, aa := a.Decompose()
	bfg, _, ba := b.Decompose()
	return afg == bfg && aa == ba
}

func (p *ChatPage) toolTokenVerbStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.Accent.Bold(true))
}

func (p *ChatPage) toolTokenKeywordStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.MarkdownCodeKeyword)
}

func (p *ChatPage) toolTokenPathStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.MarkdownCodeFunction)
}

func (p *ChatPage) toolTokenStringStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.MarkdownCodeString)
}

func (p *ChatPage) toolTokenNumberStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.MarkdownCodeNumber)
}

func (p *ChatPage) toolTokenOperatorStyle() tcell.Style {
	return toolStyleWithoutBackground(p.theme.MarkdownCodeOperator)
}

func toolStyleWithoutBackground(style tcell.Style) tcell.Style {
	return styleForCurrentCellBackground(style)
}

func isToolQuotedToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	if utf8.RuneCountInString(trimmed) < 2 {
		return false
	}
	runes := []rune(trimmed)
	first := runes[0]
	last := runes[len(runes)-1]
	switch first {
	case '"', '\'', '`':
		return first == last
	default:
		return false
	}
}

func isBacktickToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	if utf8.RuneCountInString(trimmed) < 2 {
		return false
	}
	runes := []rune(trimmed)
	return runes[0] == '`' && runes[len(runes)-1] == '`'
}

func unwrapMatchingTokenQuotes(token string) string {
	trimmed := strings.TrimSpace(token)
	runes := []rune(trimmed)
	if len(runes) >= 2 && runes[0] == runes[len(runes)-1] && isSyntaxQuoteRune(runes[0]) {
		return string(runes[1 : len(runes)-1])
	}
	return strings.Trim(trimmed, "\"'`")
}

func isKnownToolSummaryName(token string) bool {
	switch token {
	case "read", "write", "append", "bash", "glob", "grep", "edit", "search", "websearch",
		"ask-user", "ask_user", "exit-plan-mode", "exit_plan_mode", "plan-manage", "plan_manage":
		return true
	default:
		return false
	}
}

func isToolSummaryKeywordToken(token string) bool {
	switch token {
	case "line", "lines", "bytes", "matches", "files", "hit", "hits", "query", "queries",
		"pattern", "regex", "in", "from", "to", "at", "exit", "path", "domains",
		"replace_all", "replacements", "timed_out", "output_truncated", "truncated", "failed", "partial",
		"timed", "out", "results", "result", "output", "showing", "of", "entries", "entry",
		"view", "tree", "flat", "binary", "hidden", "scan", "limited":
		return true
	default:
		return false
	}
}

func shouldAccentToolToken(token, preferredTool string) bool {
	if token == "" {
		return false
	}
	if preferredTool != "" && token == preferredTool {
		return true
	}
	return isKnownToolSummaryName(token)
}

func isPlainToolSummaryTool(token string) bool {
	if token == "" {
		return false
	}
	switch token {
	case "ask-user", "ask_user", "exit-plan-mode", "exit_plan_mode":
		return true
	default:
		return false
	}
}

func normalizeToolToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	token = strings.Trim(token, "\"'`")
	_, core, _ := splitSyntaxTokenDecor(token)
	if core == "" {
		core = token
	}
	return strings.ToLower(strings.TrimSpace(core))
}

func styleZeroResultSpans(spans []chatRenderSpan) []chatRenderSpan {
	if len(spans) == 0 {
		return spans
	}
	out := cloneRenderSpans(spans)
	for i := 0; i < len(out); i++ {
		if normalizeToolToken(out[i].Text) != "0" {
			continue
		}
		j := i + 1
		for j < len(out) && strings.TrimSpace(out[j].Text) == "" {
			j++
		}
		if j >= len(out) {
			continue
		}
		word := normalizeToolToken(out[j].Text)
		if word != "matches" && word != "match" && word != "lines" && word != "line" {
			continue
		}
		out[i].Style = italicToolStyle(out[i].Style)
		out[j].Style = italicToolStyle(out[j].Style)
	}
	return compactRenderSpans(out)
}

func italicToolStyle(style tcell.Style) tcell.Style {
	fg, bg, attrs := style.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs | tcell.AttrItalic)
}

func maybeUnquoteToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) >= 2 && runes[0] == runes[len(runes)-1] && isSyntaxQuoteRune(runes[0]) {
		return strings.TrimSpace(string(runes[1 : len(runes)-1]))
	}
	return strings.Trim(trimmed, "\"'`")
}

func looksLikePathToken(token string) bool {
	value := pathCandidateForDetection(token)
	if value == "" {
		return false
	}
	if value == "." || value == ".." {
		return true
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") {
		return true
	}
	if len(value) > 2 && unicode.IsLetter(rune(value[0])) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	if strings.ContainsRune(value, '/') || strings.ContainsRune(value, '\\') {
		return true
	}
	ext := strings.ToLower(filepath.Ext(value))
	if ext != "" {
		switch ext {
		case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".c", ".h", ".cpp", ".hpp", ".json", ".yaml", ".yml", ".toml", ".sh", ".bash", ".zsh", ".rb", ".sql", ".lua", ".md", ".txt", ".log", ".xml", ".ini":
			return true
		}
	}
	return false
}

func pathCandidateForDetection(token string) string {
	value := strings.TrimSpace(token)
	if value == "" {
		return ""
	}
	value = maybeUnquoteToken(value)
	value = strings.Trim(value, "(),[]{}")
	value = strings.TrimSuffix(value, ":")
	if base, ok := stripLineColumnSuffix(value); ok {
		value = base
	}
	return strings.TrimSpace(value)
}

func stripLineColumnSuffix(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	parts := strings.Split(token, ":")
	if len(parts) < 2 {
		return token, false
	}

	numericCount := 0
	for i := len(parts) - 1; i >= 1 && numericCount < 3; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" || !allDigits(part) {
			break
		}
		numericCount++
	}
	if numericCount == 0 {
		return token, false
	}
	base := strings.Join(parts[:len(parts)-numericCount], ":")
	base = strings.TrimSpace(base)
	if base == "" {
		return token, false
	}
	return base, true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func looksRegexPatternToken(token string) bool {
	value := strings.TrimSpace(maybeUnquoteToken(token))
	if value == "" {
		return false
	}
	if looksLikePathToken(value) {
		return false
	}

	metaCount := 0
	for _, r := range value {
		switch r {
		case '[', ']', '(', ')', '{', '}', '*', '+', '?', '|', '^', '$', '\\':
			metaCount++
		}
	}
	if metaCount == 0 {
		return false
	}
	if strings.ContainsRune(value, '/') && !strings.ContainsAny(value, "[](){}*+?|^$\\") {
		return false
	}
	return true
}

func isLikelyFlagToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) < 2 {
		return false
	}
	if token[0] != '-' {
		return false
	}
	if len(token) >= 2 && token[1] == '-' {
		return len(token) > 2
	}
	return true
}

func looksNumericToken(token string) bool {
	value := strings.TrimSpace(token)
	if value == "" {
		return false
	}
	value = strings.Trim(value, "\"'`()[]{}:,")
	value = strings.TrimSuffix(strings.ToLower(value), "ms")
	value = strings.ReplaceAll(value, "_", "")
	if value == "" {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return true
	}
	return false
}

func isLikelyShellCommandToken(token string) bool {
	switch token {
	case "bash", "sh", "zsh", "fish", "go", "git", "rg", "grep", "sed", "awk", "cat", "ls", "cp", "mv", "rm", "mkdir", "make",
		"npm", "yarn", "pnpm", "python", "pip", "pytest", "cargo", "docker", "kubectl", "helm", "uv", "swarm", "swarmtui", "swarmd":
		return true
	default:
		return false
	}
}

func toolEntryPreviewLanguage(entry chatToolStreamEntry) string {
	path := toolEntryPath(entry)
	if path == "" {
		return ""
	}
	return inferCodeLanguageFromPath(path)
}

func toolEntryPath(entry chatToolStreamEntry) string {
	if path := parseToolPayloadPath(strings.TrimSpace(entry.Output)); path != "" {
		return path
	}
	if path := parseToolPayloadPath(strings.TrimSpace(entry.Raw)); path != "" {
		return path
	}
	if path := extractPathFromToolHeadline(entry.ToolName, toolHeadline(entry, 512)); path != "" {
		return path
	}
	return ""
}

func parseToolPayloadPath(raw string) string {
	payload := parseToolJSON(raw)
	if payload == nil {
		return ""
	}
	return strings.TrimSpace(jsonString(payload, "path"))
}

func extractPathFromEditHeadline(headline string) string {
	headline = strings.TrimSpace(headline)
	if headline == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(headline), "edit ") {
		return ""
	}
	rest := strings.TrimSpace(headline[len("edit "):])
	if rest == "" {
		return ""
	}
	token := extractToolLeadingToken(rest)
	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'`")
	token = strings.Trim(token, "(),[]{}")
	return token
}

func extractPathFromToolHeadline(toolName, headline string) string {
	toolName = normalizeToolToken(toolName)
	headline = trimToolHeadlineLeadingTokens(headline, toolName)
	switch toolName {
	case "edit":
		return extractPathFromEditHeadline(headline)
	case "read", "write", "append", "list", "webfetch":
		return extractPathFromVerbHeadline(toolName, headline)
	default:
		return ""
	}
}

func extractPathFromVerbHeadline(verb, headline string) string {
	headline = strings.TrimSpace(headline)
	if headline == "" {
		return ""
	}
	prefix := verb + " "
	if !strings.HasPrefix(strings.ToLower(headline), prefix) {
		return ""
	}
	rest := strings.TrimSpace(headline[len(prefix):])
	if rest == "" {
		return ""
	}
	token := extractToolLeadingToken(rest)
	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'`")
	token = strings.Trim(token, "(),[]{}")
	if token == "" || !looksLikePathToken(token) {
		return ""
	}
	return token
}

func extractToolNameHintFromHeadline(headline string) string {
	headline = trimToolHeadlineLeadingTokens(headline, "")
	return normalizeToolToken(extractToolLeadingToken(headline))
}

func trimToolHeadlineLeadingTokens(headline, preferredTool string) string {
	trimmed := strings.TrimSpace(headline)
	if trimmed == "" {
		return ""
	}
	preferredTool = normalizeToolToken(preferredTool)
	for trimmed != "" {
		token := extractToolLeadingToken(trimmed)
		if token == "" {
			break
		}
		normalized := normalizeToolToken(token)
		if normalized != "" {
			if preferredTool != "" && normalized == preferredTool {
				return trimmed
			}
			if preferredTool == "" && isKnownToolSummaryName(normalized) {
				return trimmed
			}
		}
		next := strings.TrimSpace(strings.TrimPrefix(trimmed, token))
		if next == "" || next == trimmed {
			break
		}
		trimmed = next
	}
	return strings.TrimSpace(headline)
}

func extractToolLeadingToken(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	if runes[0] == '"' || runes[0] == '\'' || runes[0] == '`' {
		quote := runes[0]
		for i := 1; i < len(runes); i++ {
			if runes[i] == quote {
				return string(runes[:i+1])
			}
		}
		return string(runes)
	}
	for i, r := range runes {
		if unicode.IsSpace(r) {
			return string(runes[:i])
		}
	}
	return string(runes)
}

func inferCodeLanguageFromPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "\"'`")
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return ""
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "makefile":
		return "make"
	case "dockerfile":
		return "dockerfile"
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "js"
	case ".ts", ".tsx":
		return "ts"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hxx":
		return "cpp"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".rb":
		return "ruby"
	case ".sql":
		return "sql"
	case ".lua":
		return "lua"
	default:
		return ""
	}
}

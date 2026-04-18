package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func (p *ChatPage) drawHeader(s tcell.Screen, rect Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.userVariantPrev = Rect{}
	p.userVariantTarget = Rect{}
	p.userVariantNext = Rect{}
	p.assistantVariantPrev = Rect{}
	p.assistantVariantTarget = Rect{}
	p.assistantVariantNext = Rect{}

	textX := rect.X
	textW := rect.W
	if rect.W > 2 {
		textX = rect.X + 1
		textW = rect.W - 2
	}
	if textW < 1 {
		textX = rect.X
		textW = rect.W
	}

	status := "idle"
	statusStyle := p.theme.TextMuted
	if p.liveRunVisible() {
		status = fmt.Sprintf("running %s", p.runElapsedLabel())
		statusStyle = p.theme.Primary
	}

	right := status
	rightW := utf8.RuneCountInString(right)
	leftW := textW
	if rightW > 0 && leftW > rightW+2 {
		leftW -= rightW + 2
	}

	DrawText(s, textX, rect.Y, leftW, p.theme.Secondary, clampEllipsis(p.sessionTitle, leftW))
	if rightW > 0 && textW > rightW+2 {
		DrawTextRight(s, textX+textW-1, rect.Y, rightW, statusStyle, right)
	}
}

func (p *ChatPage) drawComposer(s tcell.Screen, rect Rect) {
	if rect.W < 10 || rect.H < 1 {
		return
	}

	if rect.H < 3 {
		prefix := "› "
		prefixW := utf8.RuneCountInString(prefix)
		lineY := rect.Y + rect.H - 1
		lineStart := rect.X
		contentX := lineStart + prefixW
		contentW := rect.W - prefixW
		if contentW < 1 {
			return
		}
		DrawText(s, lineStart, lineY, rect.W, p.theme.Accent, prefix)
		if overlay, ok := voiceInputOverlayText(p.voiceInput, time.Now()); ok {
			style := p.theme.Primary
			if p.voiceInput.Phase == VoiceInputPhaseRecording {
				style = p.theme.Warning
			}
			DrawText(s, lineStart, lineY, rect.W, style, clampEllipsis(overlay, rect.W))
			return
		}
		visible, cursorCol := singleLineInputView(p.input, p.inputCursor, maxInt(1, contentW-1))
		if visible != "" {
			DrawText(s, contentX, lineY, contentW, p.theme.Text, visible)
		}
		cursorX := contentX + cursorCol
		if cursorX >= contentX+contentW {
			cursorX = contentX + contentW - 1
		}
		if cursorX < contentX {
			cursorX = contentX
		}
		if !p.effectiveRunActive() {
			s.SetContent(cursorX, lineY, chatCursorRune, nil, p.theme.Primary)
		}
		return
	}

	borderStyle := p.theme.BorderActive
	if p.effectiveRunActive() {
		borderStyle = p.theme.Border
	}
	DrawOpenBox(s, rect, borderStyle)

	prefix := "› "
	lineStart := rect.X + 1
	contentY := rect.Y + 1
	contentH := rect.H - 2
	if contentH <= 0 {
		return
	}
	innerW := rect.W - 2
	if innerW <= 0 {
		return
	}

	if overlay, ok := voiceInputOverlayText(p.voiceInput, time.Now()); ok {
		style := p.theme.Primary
		if p.voiceInput.Phase == VoiceInputPhaseRecording {
			style = p.theme.Warning
		}
		DrawText(s, lineStart, contentY, innerW, style, clampEllipsis(overlay, innerW))
		return
	}

	cursorX, cursorY, ok := drawWrappedInputArea(s, lineStart, contentY, innerW, contentH, p.theme.Text, prefix, p.input, p.inputCursor)
	if ok && !p.effectiveRunActive() {
		s.SetContent(cursorX, cursorY, chatCursorRune, nil, p.theme.Primary)
	}
}

func (p *ChatPage) permissionComposerHeight(width, availableHeight int) int {
	height := p.permissionComposerDesiredHeight(width)
	if height < 1 {
		height = 1
	}
	if availableHeight > 0 && height > availableHeight {
		height = availableHeight
	}
	if height < 1 {
		height = 1
	}
	return height
}

type footerToken struct {
	Text   string
	Style  tcell.Style
	Action string
}

func (p *ChatPage) drawFooterBar(s tcell.Screen, rect Rect) {
	if rect.H <= 0 || rect.W <= 0 {
		return
	}
	if rect.H >= 2 {
		DrawHLine(s, rect.X, rect.Y, rect.W, p.theme.Border)
	}

	textX := rect.X
	textW := rect.W
	if rect.W > 2 {
		textX = rect.X + 1
		textW = rect.W - 2
	}
	if textW < 1 {
		textX = rect.X
		textW = rect.W
	}

	lineY := rect.Y
	if rect.H >= 2 {
		lineY = rect.Y + 1
	}
	p.footerTargets = p.footerTargets[:0]

	right := clampEllipsis(p.footerRightLine(28), 28)
	rightW := utf8.RuneCountInString(right)
	leftW := textW
	if rightW > 0 && leftW > rightW+2 {
		leftW -= rightW + 2
	}

	tokens := p.footerSettingsTokens()
	drawFooterTokenRowWithTargets(s, textX, lineY, leftW, tokens, func(rect Rect, token footerToken) {
		if token.Action == "" {
			return
		}
		p.footerTargets = append(p.footerTargets, clickTarget{Rect: rect, Action: token.Action})
	})
	if rightW > 0 && textW > rightW+2 {
		DrawTextRight(s, textX+textW-1, lineY, rightW, p.theme.Secondary, right)
	}
}

func chatDisplayedMode(meta ChatSessionMeta, sessionMode string) string {
	if meta.AgentRuntimeKnown {
		if meta.AgentExitPlanMode {
			return normalizeSessionMode(sessionMode)
		}
		if setting := normalizeAgentExecutionSetting(meta.AgentExecutionSetting); setting != "" {
			return setting
		}
	}
	return normalizeSessionMode(sessionMode)
}

func normalizeAgentExecutionSetting(setting string) string {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "read":
		return "read"
	case "readwrite":
		return "readwrite"
	default:
		return ""
	}
}

func (p *ChatPage) footerSettingsTokens() []footerToken {
	swarmLabel := emptyValue(strings.TrimSpace(p.swarmName), "Local")
	if p.swarmNotificationCount > 0 {
		swarmLabel = fmt.Sprintf("%s !%d", swarmLabel, p.swarmNotificationCount)
	}
	primaryStyle := styleForCurrentCellBackground(p.theme.Accent.Bold(true))
	modeStyle := styleForCurrentCellBackground(p.theme.Secondary.Bold(true))
	metaStyle := styleForCurrentCellBackground(p.theme.Text)
	displayedMode := chatDisplayedMode(p.meta, p.sessionMode)
	return []footerToken{
		{Text: clampEllipsis(swarmLabel, 20), Style: primaryStyle},
		{Text: displayedMode, Style: modeStyle},
		{Text: "[a:" + clampEllipsis(emptyValue(strings.TrimSpace(p.meta.Agent), "swarm"), 12) + "]", Style: metaStyle, Action: "open-agents-modal"},
		{Text: "[m:" + clampEllipsis(model.DisplayModelLabel(p.modelProvider, p.modelName, p.serviceTier, p.contextMode), 24) + "]", Style: metaStyle, Action: "open-models-modal"},
		{Text: "[t:" + clampEllipsis(emptyValue(strings.TrimSpace(p.thinkingLevel), "-"), 10) + "]", Style: metaStyle, Action: "cycle-thinking"},
	}
}

func drawFooterTokenRow(s tcell.Screen, x, y, maxWidth int, tokens []footerToken) {
	drawFooterTokenRowWithTargets(s, x, y, maxWidth, tokens, nil)
}

func drawFooterTokenRowWithTargets(s tcell.Screen, x, y, maxWidth int, tokens []footerToken, register func(Rect, footerToken)) {
	if maxWidth <= 0 || len(tokens) == 0 {
		return
	}
	selected := make([]footerToken, 0, len(tokens))
	used := 0
	for _, token := range tokens {
		label := " " + strings.TrimSpace(token.Text) + " "
		if strings.TrimSpace(label) == "" {
			continue
		}
		width := utf8.RuneCountInString(label)
		if len(selected) > 0 {
			width++
		}
		if used+width > maxWidth {
			break
		}
		selected = append(selected, footerToken{Text: label, Style: token.Style, Action: token.Action})
		used += width
	}
	cx := x
	for i, token := range selected {
		if i > 0 {
			cx++
		}
		DrawText(s, cx, y, maxWidth-(cx-x), token.Style, token.Text)
		width := utf8.RuneCountInString(token.Text)
		if register != nil && token.Action != "" {
			register(Rect{X: cx, Y: y, W: width, H: 1}, token)
		}
		cx += width
	}
}

func (p *ChatPage) footerSettingsLine(maxWidth int) string {
	parts := make([]string, 0, 5)
	for _, token := range p.footerSettingsTokens() {
		text := strings.TrimSpace(token.Text)
		if text == "" {
			continue
		}
		candidate := append(append([]string(nil), parts...), text)
		line := strings.Join(candidate, "  ·  ")
		if maxWidth > 0 && utf8.RuneCountInString(line) > maxWidth {
			break
		}
		parts = candidate
	}
	return clampEllipsis(strings.Join(parts, "  ·  "), maxWidth)
}

func (p *ChatPage) footerWorkspaceLine(maxWidth int) string {
	workspace := clampEllipsis(emptyValue(strings.TrimSpace(p.meta.Workspace), "directory"), 20)
	branch := clampEllipsis(emptyValue(strings.TrimSpace(p.meta.Branch), "-"), 28)
	cwd := clampTail(emptyValue(strings.TrimSpace(p.meta.Path), "."), 42)

	segments := []string{fmt.Sprintf("workspace %s", workspace)}
	if p.meta.WorktreeEnabled {
		segments = append(segments, "wt on")
	}
	if p.meta.BypassPermissions {
		segments = append(segments, "bypass permissions")
	}
	segments = append(segments, fmt.Sprintf("branch %s", branch), fmt.Sprintf("cwd %s", cwd))
	return clampEllipsis(strings.Join(segments, "  |  "), maxWidth)
}

func (p *ChatPage) footerInfoLine(maxWidth int) string {
	const footerInfoThinMaxWidth = 60

	plan := footerPlanLabel(p.meta.Plan)
	mode := chatDisplayedMode(p.meta, p.sessionMode)
	swarmLabel := emptyValue(strings.TrimSpace(p.swarmName), "Local")
	if p.swarmNotificationCount > 0 {
		swarmLabel = fmt.Sprintf("%s !%d", swarmLabel, p.swarmNotificationCount)
	}
	swarmName := clampEllipsis(swarmLabel, 20)
	workspace := clampEllipsis(emptyValue(strings.TrimSpace(p.meta.Workspace), "directory"), 20)
	branch := clampEllipsis(emptyValue(strings.TrimSpace(p.meta.Branch), "-"), 36)
	cwd := clampTail(emptyValue(strings.TrimSpace(p.meta.Path), "."), 42)

	if maxWidth <= footerInfoThinMaxWidth {
		segments := []string{swarmName, mode, workspace}
		if plan != "" {
			segments = append(segments, clampEllipsis(plan, 22))
		}
		if p.meta.WorktreeEnabled {
			segments = append(segments, "wt:on")
		}
		segments = append(segments, branch, cwd)
		line := strings.Join(segments, "  |  ")
		return clampEllipsis(line, maxWidth)
	}

	segments := []string{swarmName, fmt.Sprintf("mode %s", mode), fmt.Sprintf("workspace %s", workspace)}
	if p.meta.BypassPermissions {
		segments = append(segments, "bypass permissions")
	}
	if p.meta.WorktreeEnabled {
		segments = append(segments, "wt on")
	}
	segments = append(segments, fmt.Sprintf("branch %s", branch), fmt.Sprintf("cwd %s", cwd))
	line := strings.Join(segments, "  |  ")

	return clampEllipsis(line, maxWidth)
}

func footerPlanLabel(value string) string {
	plan := strings.TrimSpace(value)
	if plan == "" || strings.EqualFold(plan, "none") {
		return ""
	}
	return plan
}

func (p *ChatPage) drawFooterRunIndicator(s tcell.Screen, rect Rect) {
	if rect.W < 3 || rect.H <= 0 || !p.liveRunVisible() {
		return
	}
	left := p.thinkingAnchorLine(maxInt(12, rect.W-2))
	DrawText(s, rect.X+1, rect.Y, rect.W-2, p.thinkingPulseStyle(), left)
}

func (p *ChatPage) thinkingAnchorLine(maxWidth int) string {
	label := strings.TrimSpace(p.swarmingTitle)
	if isCompactRunPrompt(p.runPrompt) {
		label = "Compacting"
	}
	if label == "" {
		label = "Swarming"
	}
	line := fmt.Sprintf("%s %s", p.pulseFrame(), label)
	return clampEllipsis(line, maxWidth)
}

func isCompactRunPrompt(prompt string) bool {
	return strings.HasPrefix(strings.TrimSpace(prompt), "/compact")
}

func (p *ChatPage) liveToolAnchorLine(maxWidth int) (string, tcell.Style) {
	if maxWidth <= 0 {
		return "", p.theme.TextMuted
	}
	entries := p.liveToolEntries(1)
	if len(entries) == 0 {
		return "", p.theme.TextMuted
	}
	entry := entries[len(entries)-1]
	state := strings.ToLower(strings.TrimSpace(entry.State))
	symbol := p.toolRunningSymbol
	style := p.thinkingPulseStyle()
	switch state {
	case "done", "ok", "success":
		symbol = p.toolSuccessSymbol
		style = p.theme.Secondary
	case "error":
		symbol = p.toolErrorSymbol
		style = p.theme.Error
	}
	label := toolHeadline(entry, maxInt(8, maxWidth-8))
	if duration := p.toolEntryDurationLabel(entry); duration != "" {
		label += "  ·  " + duration
	}
	return clampEllipsis(fmt.Sprintf("%s %s", symbol, label), maxWidth), style
}

func (p *ChatPage) footerContextUsageLabel() string {
	if !p.contextUsageSet || p.contextWindow <= 0 {
		return ""
	}
	window := p.contextWindow
	remaining := p.contextRemain
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(window) {
		remaining = int64(window)
	}
	leftPercent := int((remaining*100 + int64(window/2)) / int64(window))
	if leftPercent < 0 {
		leftPercent = 0
	}
	if leftPercent > 100 {
		leftPercent = 100
	}
	return fmt.Sprintf("%d%% left", leftPercent)
}

func (p *ChatPage) footerRightLine(maxWidth int) string {
	segments := make([]string, 0, 2)
	if p.meta.WorktreeEnabled {
		segments = append(segments, "wt on")
	}
	if usage := strings.TrimSpace(p.footerContextUsageLabel()); usage != "" {
		segments = append(segments, usage)
	}
	return clampEllipsis(strings.Join(segments, "  "), maxWidth)
}

func (p *ChatPage) footerStatusText() string {
	status := strings.TrimSpace(p.statusLine)
	if errLine := strings.TrimSpace(p.errorLine); errLine != "" {
		status = errLine
	}
	if status == "" {
		return "ready"
	}
	return status
}

func (p *ChatPage) drawFooterSessionTabs(s tcell.Screen, rect Rect) {
	if rect.W <= 0 {
		return
	}

	prefix := "sessions "
	DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, prefix)
	x := rect.X + utf8.RuneCountInString(prefix)
	maxX := rect.X + rect.W
	if x >= maxX {
		return
	}

	currentID := strings.TrimSpace(p.sessionID)
	for _, tab := range p.sessionTabs {
		title := strings.TrimSpace(tab.Title)
		if title == "" {
			title = strings.TrimSpace(tab.ID)
		}
		if title == "" {
			continue
		}
		label := "[" + clampEllipsis(title, 26) + "]"
		style := p.theme.TextMuted
		if strings.TrimSpace(tab.ID) == currentID {
			label = "[" + clampEllipsis(title, 24) + "*]"
			style = p.theme.Primary
		}

		remaining := maxX - x
		if remaining <= 0 {
			return
		}
		label = clampEllipsis(label, remaining)
		labelW := utf8.RuneCountInString(label)
		if labelW <= 0 {
			return
		}
		DrawText(s, x, rect.Y, remaining, style, label)
		x += labelW
		if x >= maxX {
			return
		}
		DrawText(s, x, rect.Y, maxX-x, p.theme.TextMuted, " ")
		x++
	}
}

func (p *ChatPage) thinkingAvatar() string {
	return p.pulseFrame()
}

func (p *ChatPage) pulseFrame() string {
	if len(p.toolPulseFrames) == 0 {
		return chatDefaultToolRunningSymbol
	}
	// Slow frame advance to keep the thinking indicator calm.
	frameIndex := (p.frameTick / 4) % len(p.toolPulseFrames)
	return p.toolPulseFrames[frameIndex]
}

func (p *ChatPage) thinkingPulseStyle() tcell.Style {
	// Single theme-accent pulse to avoid flashy color cycling.
	if (p.frameTick/8)%2 == 0 {
		return p.theme.Accent
	}
	return p.theme.Accent.Bold(true)
}

func (p *ChatPage) statusStyle() tcell.Style {
	if strings.TrimSpace(p.errorLine) != "" {
		return p.theme.Error
	}
	if p.effectiveRunActive() {
		return p.theme.Primary
	}
	return p.theme.TextMuted
}

func emptyValue(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

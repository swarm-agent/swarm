package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func (p *HomePage) drawMeta(s tcell.Screen, rect Rect, variant layoutVariant) {
	if rect.H < 1 {
		return
	}
	d := p.primaryDirectory()
	if variant.ShowWorkspaceList {
		x := rect.X
		DrawText(s, x, rect.Y, rect.W, p.theme.TextMuted, "workspaces: ")
		x += utf8.RuneCountInString("workspaces: ")
		for _, ws := range p.model.Workspaces {
			label := p.workspaceDisplayLabel(fmt.Sprintf("%s %s", ws.Icon, ws.Name), ws.ThemeID)
			style := p.theme.Text
			if ws.Active {
				label = "[" + label + "]"
				style = p.theme.Primary.Bold(true)
			}
			w := utf8.RuneCountInString(label)
			if x+w > rect.X+rect.W-1 {
				break
			}
			DrawText(s, x, rect.Y, rect.W-(x-rect.X), style, label)
			x += w + 2
		}
		return
	}

	name := strings.TrimSpace(p.contextDisplayName())
	if name == "" {
		name = "directory"
	}
	branch := strings.TrimSpace(d.Branch)
	if branch == "" {
		branch = "-"
	}
	gitLine := fmt.Sprintf("workspace:%s  git:%s", name, branch)
	if d.DirtyCount > 0 {
		gitLine += fmt.Sprintf(" +%d", d.DirtyCount)
	}
	cwd := strings.TrimSpace(d.Path)
	if cwd == "" {
		cwd = "."
	}
	cwdLine := fmt.Sprintf("cwd:%s", cwd)
	if !d.IsWorkspace {
		cwdLine += "  /workspace save"
	}

	if rect.H <= 1 || !variant.ShowDirectory {
		minimal := gitLine + "  " + cwdLine
		DrawText(s, rect.X, rect.Y, rect.W, p.theme.Primary, minimal)
		return
	}

	DrawText(s, rect.X, rect.Y, rect.W, p.theme.Primary, gitLine)
	DrawText(s, rect.X, rect.Y+1, rect.W, p.theme.TextMuted, cwdLine)
}

func (p *HomePage) drawInputBar(s tcell.Screen, rect Rect, centered bool) {
	_ = centered
	if rect.H < 3 || rect.W < 8 {
		return
	}

	DrawOpenBox(s, rect, p.theme.BorderActive)
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

	cursorX, cursorY, ok := drawWrappedInputArea(s, lineStart, contentY, innerW, contentH, p.theme.Text, prefix, p.prompt, p.promptCursor)
	if ok {
		s.SetContent(cursorX, cursorY, inputCursorRune, nil, p.theme.Primary)
	}
}

func (p *HomePage) desiredInputBarHeight(width int) int {
	if width < 8 {
		return 3
	}
	innerW := width - 2
	if innerW <= 1 {
		innerW = 1
	}
	contentLines := len(wrapWithCustomPrefixes("› ", "", p.prompt, innerW))
	if contentLines < 1 {
		contentLines = 1
	}
	if contentLines > 8 {
		contentLines = 8
	}
	return contentLines + 2
}

func (p *HomePage) drawCommandPalette(s tcell.Screen, inputRect Rect, variant layoutVariant, bottomBarH int) {
	if !p.commandPaletteActive() {
		return
	}
	matches := p.syncCommandPaletteSelection()

	const maxVisible = 5
	visible := len(matches)
	if visible > maxVisible {
		visible = maxVisible
	}
	if visible == 0 {
		visible = 1
	}

	popupH := visible + 3
	_, screenH := s.Size()
	popup, ok := p.commandPaletteRect(inputRect, popupH, screenH, variant, bottomBarH)
	if !ok || popup.W < 12 || popup.H < 3 {
		return
	}

	FillRect(s, popup, p.theme.Panel)
	DrawBox(s, popup, p.theme.BorderActive)
	rowY := popup.Y + 1
	if len(matches) == 0 {
		DrawText(s, popup.X+2, rowY, popup.W-4, p.theme.Warning, "no matching commands")
		DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, "Type more or press Backspace")
		return
	}

	start := 0
	if len(matches) > visible {
		start = p.commandPaletteIndex - (visible - 1)
		if start < 0 {
			start = 0
		}
		maxStart := len(matches) - visible
		if start > maxStart {
			start = maxStart
		}
	}

	for i := 0; i < visible; i++ {
		idx := start + i
		suggestion := matches[idx]
		prefix := "  "
		style := p.theme.Text
		if idx == p.commandPaletteIndex {
			prefix = "› "
			style = p.theme.Primary.Bold(true)
		}
		baseX := popup.X + 2
		available := popup.W - 4
		written := DrawTextCount(s, baseX, rowY, available, style, prefix+suggestion.Command)
		if written > 0 {
			p.commandPaletteTargets = append(p.commandPaletteTargets, clickTarget{
				Rect:   Rect{X: baseX, Y: rowY, W: written, H: 1},
				Action: "palette-row",
				Index:  idx,
			})
		}
		if idx == p.commandPaletteIndex {
			p.drawCommandPaletteOptions(s, suggestion, baseX+written, rowY, maxInt(0, available-written))
		}
		rowY++
	}

	DrawText(s, popup.X+2, popup.Y+popup.H-2, popup.W-4, p.theme.TextMuted, p.commandPaletteHelpLine())
}

func (p *HomePage) drawCommandPaletteOptions(s tcell.Screen, suggestion CommandSuggestion, x, y, width int) {
	options := commandPaletteOptions(suggestion)
	if len(options) == 0 || width <= 0 {
		return
	}
	selectedIdx, selected := p.currentCommandPaletteOptionIndex(suggestion)
	cx := x
	remaining := width
	if remaining >= 2 {
		DrawText(s, cx, y, remaining, p.theme.TextMuted, "  ")
		cx += 2
		remaining -= 2
	}
	for i, option := range options {
		if remaining <= 0 {
			break
		}
		label := "[" + option.Label + "]"
		if utf8.RuneCountInString(label) > remaining {
			if remaining > 1 {
				DrawText(s, cx, y, remaining, p.theme.TextMuted, "…")
			}
			break
		}
		style := p.theme.TextMuted
		if selected && i == selectedIdx {
			style = p.theme.Primary.Reverse(true).Bold(true)
		}
		written := DrawTextCount(s, cx, y, remaining, style, label)
		if written <= 0 {
			break
		}
		p.commandPaletteTargets = append(p.commandPaletteTargets, clickTarget{
			Rect:   Rect{X: cx, Y: y, W: written, H: 1},
			Action: "palette-option",
			Index:  i,
			Meta:   option.Command,
		})
		cx += written
		remaining -= written
		if i < len(options)-1 && remaining > 0 {
			DrawText(s, cx, y, remaining, p.theme.TextMuted, " ")
			cx++
			remaining--
		}
	}
}

func (p *HomePage) commandPaletteHelpLine() string {
	selected, ok := p.selectedCommandSuggestion()
	if ok && len(commandPaletteOptions(selected)) > 0 {
		return "Enter runs selection • ←/→ options • Tab completes • ↑/↓ select"
	}
	return "Enter runs selection • Tab completes • ↑/↓ select"
}

func (p *HomePage) commandPaletteRect(inputRect Rect, popupH, screenH int, variant layoutVariant, bottomBarH int) (Rect, bool) {
	topBound := 0
	if variant.UseSwarmTopBar {
		topBound = homeTopBarHeight + 1
	}
	if bottomBarH < 0 {
		bottomBarH = 0
	}
	bottomBound := screenH - bottomBarH
	if bottomBound-topBound < 3 {
		return Rect{}, false
	}

	aboveY := inputRect.Y - popupH - 1
	belowY := inputRect.Y + inputRect.H + 1
	availableAbove := inputRect.Y - topBound - 1
	availableBelow := bottomBound - belowY

	popup := Rect{
		X: inputRect.X,
		Y: aboveY,
		W: inputRect.W,
		H: popupH,
	}
	switch {
	case availableAbove >= popupH:
		popup.Y = aboveY
	case availableBelow >= popupH:
		popup.Y = belowY
	case availableBelow > availableAbove:
		popup.Y = minInt(belowY, bottomBound-popupH)
	default:
		popup.Y = maxInt(topBound, aboveY)
	}

	if popup.Y < topBound {
		popup.Y = topBound
	}
	if popup.Y+popup.H > bottomBound {
		popup.Y = bottomBound - popup.H
	}
	if popup.Y < topBound {
		return Rect{}, false
	}
	return popup, true
}

func (p *HomePage) drawPresetsRow(s tcell.Screen, rect Rect, centered bool) {
	if rect.H < 1 {
		return
	}
	if len(p.model.QuickActions) == 0 {
		line := "/auth"
		if p.model.AuthConfigured {
			line = "/models"
		}
		DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, line)
		return
	}

	compact := rect.W < 68
	type presetChip struct {
		Label  string
		Action string
	}
	chips := make([]presetChip, 0, len(p.model.QuickActions))
	gap := 2
	totalW := 0
	for _, raw := range p.model.QuickActions {
		chip := homePresetChip(raw, compact)
		chips = append(chips, presetChip{
			Label:  chip,
			Action: homePresetChipAction(raw),
		})
		if totalW > 0 {
			totalW += gap
		}
		totalW += utf8.RuneCountInString(chip)
	}

	startX := rect.X
	if centered && totalW > 0 && totalW < rect.W {
		startX = rect.X + (rect.W-totalW)/2
	}
	limitX := rect.X + rect.W
	x := startX
	for i, chip := range chips {
		chipW := utf8.RuneCountInString(chip.Label)
		if chipW <= 0 || x+chipW > limitX {
			break
		}

		style := p.theme.TextMuted
		if chip.Action != "" {
			state := p.workspaceButtonState(chip.Action)
			style = p.workspaceButtonStyle(style, state)
		}
		DrawText(s, x, rect.Y, limitX-x, style, chip.Label)
		if chip.Action != "" {
			p.registerTopTarget(Rect{X: x, Y: rect.Y, W: chipW, H: 1}, chip.Action, 0)
		}
		x += chipW

		if i < len(chips)-1 {
			if x+gap > limitX {
				break
			}
			x += gap
		}
	}
}

func homePresetChip(action string, compact bool) string {
	action = strings.TrimSpace(action)
	if !compact {
		return "[" + action + "]"
	}

	key := ""
	value := action
	if sep := strings.Index(action, ":"); sep >= 0 {
		key = strings.ToLower(strings.TrimSpace(action[:sep]))
		value = strings.TrimSpace(action[sep+1:])
	}

	switch key {
	case "agent":
		return "[a:" + clampEllipsis(emptyValue(value, "swarm"), 16) + "]"
	case "model":
		return "[m:" + clampEllipsis(emptyValue(value, "unset"), 24) + "]"
	case "thinking":
		return "[t:" + clampEllipsis(emptyValue(value, "unset"), 10) + "]"
	default:
		return "[" + action + "]"
	}
}

func homePresetChipAction(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return ""
	}
	sep := strings.Index(action, ":")
	if sep < 0 {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(action[:sep]))
	switch key {
	case "agent":
		return "open-agents-modal"
	case "model":
		return "open-models-modal"
	case "thinking":
		return "cycle-thinking"
	default:
		return ""
	}
}

func (p *HomePage) drawTipsRow(s tcell.Screen, rect Rect, centered bool) {
	if rect.H < 1 {
		return
	}
	hint := strings.TrimSpace(p.model.HintLine)
	tip := strings.TrimSpace(p.model.TipLine)
	modeKeyLabel := "Shift+Tab"
	if p.keybinds != nil {
		if label := strings.TrimSpace(p.keybinds.Label(KeybindChatCycleMode)); label != "" {
			modeKeyLabel = label
		}
	}
	modeHint := fmt.Sprintf("%s cycle mode", modeKeyLabel)
	line := ""
	switch {
	case hint != "" && tip != "":
		line = fmt.Sprintf("%s • %s", hint, tip)
	case hint != "":
		line = hint
	case tip != "":
		line = tip
	default:
		line = modeHint
	}
	if !strings.Contains(strings.ToLower(line), "cycle mode") {
		line = fmt.Sprintf("%s • %s", line, modeHint)
	}
	if centered {
		DrawCenteredText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, line)
		return
	}
	DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, line)
}

func (p *HomePage) homeFooterTokens() []footerToken {
	swarmLabel := emptyValue(strings.TrimSpace(p.swarmName), displayRuntimeMode(p.model.ServerMode))
	if p.swarmNotificationCount > 0 {
		swarmLabel = fmt.Sprintf("%s !%d", swarmLabel, p.swarmNotificationCount)
	}
	primaryStyle := styleForCurrentCellBackground(p.theme.Accent.Bold(true))
	modeStyle := styleForCurrentCellBackground(p.theme.Secondary.Bold(true))
	metaStyle := styleForCurrentCellBackground(p.theme.Text)
	return []footerToken{
		{Text: clampEllipsis(swarmLabel, 18), Style: primaryStyle},
		{Text: currentDisplayedHomeSessionMode(p), Style: modeStyle},
		{Text: "[a:" + clampEllipsis(emptyValue(strings.TrimSpace(p.model.ActiveAgent), "swarm"), 12) + "]", Style: metaStyle, Action: "open-agents-modal"},
		{Text: "[m:" + clampEllipsis(model.DisplayModelLabel(p.model.ModelProvider, p.model.ModelName, p.model.ServiceTier, p.model.ContextMode), 24) + "]", Style: metaStyle, Action: "open-models-modal"},
		{Text: "[t:" + clampEllipsis(emptyValue(strings.TrimSpace(p.model.ThinkingLevel), "-"), 10) + "]", Style: metaStyle, Action: "cycle-thinking"},
	}
}

func (p *HomePage) homeFooterRightLine(maxWidth int) string {
	segments := make([]string, 0, 1)
	if p.model.WorktreesEnabled {
		segments = append(segments, "wt on")
	}
	return clampEllipsis(strings.Join(segments, "  "), maxWidth)
}

func (p *HomePage) homeStatusStyle(status string) tcell.Style {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch {
	case normalized == "":
		return p.theme.TextMuted
	case strings.Contains(normalized, "failed"),
		strings.Contains(normalized, "error"),
		strings.Contains(normalized, "unavailable"),
		strings.Contains(normalized, "unknown command"),
		strings.Contains(normalized, "usage:"),
		strings.Contains(normalized, "locked"),
		strings.Contains(normalized, "warning"):
		return p.theme.Error
	case strings.Contains(normalized, "loaded"),
		strings.Contains(normalized, "reloading"),
		strings.Contains(normalized, "rebuilding"),
		strings.Contains(normalized, "complete"),
		strings.Contains(normalized, "selected"):
		return p.theme.Secondary
	default:
		return p.theme.TextMuted
	}
}

func (p *HomePage) drawBottomBar(s tcell.Screen, rect Rect, variant layoutVariant) {
	if rect.H <= 0 || rect.W <= 0 {
		return
	}
	_ = variant

	footerY := rect.Y + rect.H - 1
	if rect.H >= 2 {
		DrawHLine(s, rect.X, footerY-1, rect.W, p.theme.Border)
	}

	textX := rect.X
	textW := rect.W
	if rect.W > 2 {
		textX = rect.X + 1
		textW = rect.W - 2
	}
	if textW <= 0 {
		return
	}
	p.bottomBarTargets = p.bottomBarTargets[:0]

	right := clampEllipsis(p.homeFooterRightLine(28), 28)
	rightW := utf8.RuneCountInString(right)
	leftW := textW
	if rightW > 0 && leftW > rightW+2 {
		leftW -= rightW + 2
	}

	status := strings.TrimSpace(p.statusLine)
	if status != "" {
		statusStyle := p.homeStatusStyle(status)
		statusWidth := maxInt(1, minInt(leftW, maxInt(leftW/2, 18)))
		if rect.H >= 3 {
			DrawTextRight(s, textX+textW-1, rect.Y, statusWidth, statusStyle, clampEllipsis(status, statusWidth))
		} else if leftW > statusWidth+2 {
			leftW -= statusWidth + 2
			DrawTextRight(s, textX+leftW-1, footerY, statusWidth, statusStyle, clampEllipsis(status, statusWidth))
		}
	}

	drawFooterTokenRowWithTargets(s, textX, footerY, leftW, p.homeFooterTokens(), func(rect Rect, token footerToken) {
		if token.Action == "" {
			return
		}
		p.registerBottomTarget(rect, token.Action, 0)
	})
	if rightW > 0 && textW > rightW+2 {
		DrawTextRight(s, textX+textW-1, footerY, rightW, p.theme.Secondary, right)
	}
}

package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type FooterState struct {
	RouteLabel        string
	NotificationCount int
	DisplayedMode     string
	Agent             string
	ProfileLabel      string
	ModelLabel        string
	Thinking          string
	ServiceTier       string
	UnifiedProfile    bool
	PlanToggle        bool
	RightFacts        []string
	StatusLine        string
	StatusStyle       tcell.Style
}

type footerToken struct {
	Text   string
	Style  tcell.Style
	Action string
	Shrink bool
}

func footerTokensFromState(theme Theme, state FooterState) []footerToken {
	routeLabel := emptyValue(strings.TrimSpace(state.RouteLabel), "Local")
	if state.NotificationCount > 0 {
		routeLabel = routeLabel + " !" + intLabel(state.NotificationCount)
	}
	routeLabel = clampSwarmNotificationLabel(routeLabel, state.NotificationCount, 20)
	primaryStyle := styleForCurrentCellBackground(theme.Accent.Bold(true))
	modeStyle := styleForCurrentCellBackground(theme.Secondary.Bold(true))
	metaStyle := styleForCurrentCellBackground(theme.Text)
	modeText := emptyValue(strings.TrimSpace(state.DisplayedMode), "plan")
	if state.PlanToggle {
		modeText = "Plan: " + emptyValue(strings.TrimSpace(state.DisplayedMode), "on")
	}
	tokens := []footerToken{
		{Text: routeLabel, Style: primaryStyle, Action: "cycle-route"},
		{Text: modeText, Style: modeStyle},
	}
	if state.UnifiedProfile {
		return append(tokens,
			footerToken{Text: footerProfileUnit(state), Style: metaStyle, Action: "open-profiles-modal", Shrink: true},
		)
	}
	return append(tokens,
		footerToken{Text: "[a:" + clampEllipsis(emptyValue(strings.TrimSpace(state.Agent), "swarm"), 12) + "]", Style: metaStyle, Action: "open-agents-modal"},
		footerToken{Text: "[m:" + clampEllipsis(emptyValue(strings.TrimSpace(state.ModelLabel), "-"), 24) + "]", Style: metaStyle, Action: "open-models-modal"},
		footerToken{Text: "[t:" + clampEllipsis(emptyValue(strings.TrimSpace(state.Thinking), "-"), 10) + "]", Style: metaStyle, Action: "cycle-thinking"},
	)
}

func footerProfileUnit(state FooterState) string {
	parts := []string{
		emptyValue(strings.TrimSpace(state.ProfileLabel), "Agent model default"),
		emptyValue(strings.TrimSpace(state.ModelLabel), "Model"),
	}
	if thinking := strings.TrimSpace(state.Thinking); thinking != "" {
		parts = append(parts, thinking)
	}
	if priority := strings.TrimSpace(state.ServiceTier); priority != "" {
		parts = append(parts, priority)
	}
	return "[" + clampEllipsis(strings.Join(parts, " · "), 58) + "]"
}

func intLabel(value int) string {
	if value <= 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}

func (state FooterState) rightLine(maxWidth int) string {
	segments := make([]string, 0, len(state.RightFacts))
	for _, fact := range state.RightFacts {
		fact = strings.TrimSpace(fact)
		if fact == "" {
			continue
		}
		segments = append(segments, fact)
	}
	return clampEllipsis(strings.Join(segments, "  "), maxWidth)
}

func drawUnifiedFooterBar(s tcell.Screen, theme Theme, rect Rect, state FooterState, register func(Rect, footerToken)) {
	if rect.H <= 0 || rect.W <= 0 {
		return
	}
	lineY := rect.Y + rect.H - 1
	if rect.H >= 2 {
		DrawHLine(s, rect.X, lineY-1, rect.W, theme.Border)
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

	right := state.rightLine(28)
	rightW := utf8.RuneCountInString(right)
	leftW := textW
	if rightW > 0 && leftW > rightW+2 {
		leftW -= rightW + 2
	}

	status := strings.TrimSpace(state.StatusLine)
	if status != "" {
		statusStyle := state.StatusStyle
		statusWidth := maxInt(1, minInt(leftW, maxInt(leftW/2, 18)))
		if rect.H >= 3 {
			DrawTextRight(s, textX+textW-1, rect.Y, statusWidth, statusStyle, clampEllipsis(status, statusWidth))
		} else if leftW > statusWidth+2 {
			leftW -= statusWidth + 2
			DrawTextRight(s, textX+leftW-1, lineY, statusWidth, statusStyle, clampEllipsis(status, statusWidth))
		}
	}

	drawFooterTokenRowWithTargets(s, textX, lineY, leftW, footerTokensFromState(theme, state), register)
	if rightW > 0 && textW > rightW+2 {
		DrawTextRight(s, textX+textW-1, lineY, rightW, theme.Secondary, right)
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
			remaining := maxWidth - used
			if len(selected) > 0 {
				remaining--
			}
			if !token.Shrink || remaining < 8 {
				break
			}
			label = " " + clampEllipsis(strings.TrimSpace(token.Text), remaining-2) + " "
			width = utf8.RuneCountInString(label)
		}
		selected = append(selected, footerToken{Text: label, Style: token.Style, Action: token.Action, Shrink: token.Shrink})
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

package ui

import (
	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

type FooterState footerbar.State
type footerToken = footerbar.Token

func responsiveFooterHeight(width, normalHeight int) int {
	return footerbar.ResponsiveHeight(width, normalHeight)
}

func footerBarStyles(theme Theme) footerbar.Styles {
	return footerbar.Styles{
		Border:    theme.Border,
		Accent:    theme.Accent,
		Secondary: theme.Secondary,
		Text:      theme.Text,
	}
}

func footerTokensFromState(theme Theme, state FooterState) []footerToken {
	return footerbar.Tokens(footerBarStyles(theme), footerbar.State(state))
}

func footerAgentModelUnit(state FooterState) string {
	return footerbar.AgentModelUnit(footerbar.State(state))
}

func (state FooterState) rightLine(maxWidth int) string {
	return footerbar.RightLine(footerbar.State(state), maxWidth)
}

func drawUnifiedFooterBar(s tcell.Screen, theme Theme, rect Rect, state FooterState, register func(Rect, footerToken)) {
	var adapted func(footerbar.Rect, footerbar.Token)
	if register != nil {
		adapted = func(rect footerbar.Rect, token footerbar.Token) {
			register(Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H}, token)
		}
	}
	footerbar.Draw(s, footerBarStyles(theme), footerbar.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H}, footerbar.State(state), adapted)
}

func drawFooterTokenRow(s tcell.Screen, x, y, maxWidth int, tokens []footerToken) {
	drawFooterTokenRowWithTargets(s, x, y, maxWidth, tokens, nil)
}

func drawFooterTokenRowWithTargets(s tcell.Screen, x, y, maxWidth int, tokens []footerToken, register func(Rect, footerToken)) {
	var adapted func(footerbar.Rect, footerbar.Token)
	if register != nil {
		adapted = func(rect footerbar.Rect, token footerbar.Token) {
			register(Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H}, token)
		}
	}
	footerbar.DrawTokenRow(s, x, y, maxWidth, tokens, adapted)
}

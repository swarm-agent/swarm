package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func (p *HomePage) drawHeroPanel(s tcell.Screen, rect Rect, centered bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}

	FillRect(s, rect, p.theme.Background)

	innerW := rect.W
	if innerW > 84 {
		innerW = 84
	}
	if innerW < 24 {
		innerW = rect.W
	}
	startX := rect.X
	if centered && innerW < rect.W {
		startX = rect.X + (rect.W-innerW)/2
	}
	innerRect := Rect{X: startX, Y: rect.Y, W: innerW, H: rect.H}
	boxRect := innerRect
	if boxRect.H > 5 {
		boxRect.H = 5
	}
	if boxRect.W < 20 || boxRect.H < 5 {
		return
	}

	FillRect(s, boxRect, p.theme.Panel)
	DrawBox(s, boxRect, p.theme.BorderActive)

	badge := " SWARM HOME "
	badgeW := utf8.RuneCountInString(badge)
	badgeX := boxRect.X + (boxRect.W-badgeW)/2
	DrawText(s, badgeX, boxRect.Y, badgeW, filledButtonStyle(p.theme.Primary), badge)

	headline := "Center hive. Start fast."
	subline := p.heroSubline()
	ornament := p.heroOrnamentLine(boxRect.W - 4)

	DrawCenteredText(s, boxRect.X+2, boxRect.Y+1, boxRect.W-4, p.theme.Accent.Bold(true), ornament)
	DrawCenteredText(s, boxRect.X+2, boxRect.Y+2, boxRect.W-4, p.theme.Text.Bold(true), headline)
	DrawCenteredText(s, boxRect.X+2, boxRect.Y+3, boxRect.W-4, p.theme.TextMuted, subline)
	DrawCenteredText(s, boxRect.X+2, boxRect.Y+4, boxRect.W-4, p.theme.Secondary, "Type to launch a session • / for commands • ↑ to revisit recents")
}

func (p *HomePage) heroSubline() string {
	workspace := strings.TrimSpace(p.contextDisplayName())
	if workspace == "" {
		workspace = "current workspace"
	}
	mode := currentDisplayedHomeSessionMode(p)
	agent := strings.TrimSpace(p.model.ActiveAgent)
	if agent == "" {
		agent = "swarm"
	}
	modelLabel := model.DisplayModelLabel(p.model.ModelProvider, p.model.ModelName, p.model.ServiceTier, p.model.ContextMode)
	if strings.TrimSpace(modelLabel) == "" || modelLabel == "-" {
		modelLabel = "model ready"
	}
	return clampEllipsis(fmt.Sprintf("%s • %s mode • %s • %s", workspace, mode, agent, modelLabel), 72)
}

func (p *HomePage) heroOrnamentLine(width int) string {
	if width < 18 {
		return "~~~"
	}
	center := " swarm:// "
	centerW := utf8.RuneCountInString(center)
	if centerW >= width {
		return clampEllipsis(center, width)
	}
	side := (width - centerW) / 2
	left := strings.Repeat("~", side)
	right := strings.Repeat("~", width-centerW-side)
	return left + center + right
}

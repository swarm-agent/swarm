package ui

import "github.com/gdamore/tcell/v2"

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

	headline := "Talk to Swarm"
	centerY := rect.Y + maxInt(0, (rect.H-2)/2)
	DrawCenteredText(s, startX, centerY, innerW, p.theme.Text.Bold(true), headline)
	if !p.showHomeTips || len(homeTips) == 0 || centerY+1 >= rect.Y+rect.H {
		return
	}
	maxLines := rect.Y + rect.H - (centerY + 1)
	for i, line := range homeTipLines(p.CurrentHomeTip(), innerW, maxLines) {
		DrawCenteredText(s, startX, centerY+1+i, innerW, p.theme.TextMuted, line)
	}
}

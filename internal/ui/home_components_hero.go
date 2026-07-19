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
	hint := "Type below to begin • / for commands • ↑ to revisit recents"
	centerY := rect.Y + maxInt(0, (rect.H-2)/2)
	DrawCenteredText(s, startX, centerY, innerW, p.theme.Text.Bold(true), headline)
	if centerY+1 < rect.Y+rect.H {
		DrawCenteredText(s, startX, centerY+1, innerW, p.theme.TextMuted, hint)
	}
}

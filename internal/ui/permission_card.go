package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// permissionCardModel is a tool-agnostic card description. Tool-specific
// permission presenters provide the title, metadata, and content while this
// component owns the shared themed surface and layout.
type permissionCardModel struct {
	Title      string
	Badge      string
	Meta       string
	Content    []chatRenderLine
	FooterRows int
}

type permissionCardLayout struct {
	Content Rect
	FooterY int
}

func drawPermissionCard(s tcell.Screen, rect Rect, theme Theme, model permissionCardModel, scroll int) permissionCardLayout {
	footerRows := model.FooterRows
	if footerRows < 1 {
		footerRows = 1
	}
	layout := permissionCardLayout{FooterY: rect.Y + rect.H - 1 - footerRows}
	if rect.W < 8 || rect.H < 6+footerRows-1 {
		return layout
	}

	FillRect(s, rect, theme.Panel)
	DrawBox(s, rect, styleOnPermissionCard(theme.BorderActive, theme.Panel))

	innerX := rect.X + 2
	innerW := rect.W - 4
	titleStyle := styleOnPermissionCard(theme.Text.Bold(true), theme.Panel)
	metaStyle := styleOnPermissionCard(theme.TextMuted, theme.Panel)
	badgeStyle := styleOnPermissionCard(theme.Secondary.Bold(true), theme.Panel)
	dividerStyle := styleOnPermissionCard(theme.Border, theme.Panel)

	badge := strings.ToUpper(strings.TrimSpace(model.Badge))
	badgeW := 0
	if badge != "" {
		badge = " " + badge + " "
		badgeW = utf8.RuneCountInString(badge)
		if badgeW+1 < innerW {
			badgeX := innerX + innerW - badgeW
			FillRect(s, Rect{X: badgeX, Y: rect.Y + 1, W: badgeW, H: 1}, badgeStyle)
			DrawText(s, badgeX, rect.Y+1, badgeW, badgeStyle, badge)
		}
	}

	titleW := innerW
	if badgeW > 0 && badgeW+1 < innerW {
		titleW -= badgeW + 1
	}
	DrawText(s, innerX, rect.Y+1, titleW, titleStyle, clampEllipsis(strings.TrimSpace(model.Title), titleW))
	DrawText(s, innerX, rect.Y+2, innerW, metaStyle, clampEllipsis(strings.TrimSpace(model.Meta), innerW))
	DrawHLine(s, rect.X+1, rect.Y+3, rect.W-2, dividerStyle)
	DrawHLine(s, rect.X+1, layout.FooterY-1, rect.W-2, dividerStyle)

	layout.Content = Rect{X: innerX, Y: rect.Y + 4, W: innerW, H: maxInt(0, layout.FooterY-rect.Y-5)}
	if layout.Content.H == 0 || len(model.Content) == 0 {
		return layout
	}
	maxScroll := maxInt(0, len(model.Content)-layout.Content.H)
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	end := minInt(len(model.Content), scroll+layout.Content.H)
	y := layout.Content.Y
	for i := scroll; i < end && y < layout.Content.Y+layout.Content.H; i++ {
		line := model.Content[i]
		line.Style = styleOnPermissionCard(line.Style, theme.Panel)
		for spanIndex := range line.Spans {
			line.Spans[spanIndex].Style = styleOnPermissionCard(line.Spans[spanIndex].Style, theme.Panel)
		}
		DrawTimelineLine(s, layout.Content.X, y, layout.Content.W, line)
		y++
	}
	return layout
}

func styleOnPermissionCard(style, surface tcell.Style) tcell.Style {
	fg, _, attrs := style.Decompose()
	_, bg, _ := surface.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs)
}

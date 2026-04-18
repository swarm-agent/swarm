package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type Rect struct {
	X int
	Y int
	W int
	H int
}

func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func FillRect(s tcell.Screen, rect Rect, style tcell.Style) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			s.SetContent(x, y, ' ', nil, style)
		}
	}
}

func DrawBox(s tcell.Screen, rect Rect, style tcell.Style) {
	if rect.W < 2 || rect.H < 2 {
		return
	}
	x0, y0 := rect.X, rect.Y
	x1, y1 := rect.X+rect.W-1, rect.Y+rect.H-1
	for x := x0 + 1; x < x1; x++ {
		s.SetContent(x, y0, tcell.RuneHLine, nil, style)
		s.SetContent(x, y1, tcell.RuneHLine, nil, style)
	}
	for y := y0 + 1; y < y1; y++ {
		s.SetContent(x0, y, tcell.RuneVLine, nil, style)
		s.SetContent(x1, y, tcell.RuneVLine, nil, style)
	}
	s.SetContent(x0, y0, tcell.RuneULCorner, nil, style)
	s.SetContent(x1, y0, tcell.RuneURCorner, nil, style)
	s.SetContent(x0, y1, tcell.RuneLLCorner, nil, style)
	s.SetContent(x1, y1, tcell.RuneLRCorner, nil, style)
}

func DrawHLine(s tcell.Screen, x, y, w int, style tcell.Style) {
	for i := 0; i < w; i++ {
		s.SetContent(x+i, y, tcell.RuneHLine, nil, style)
	}
}

func DrawOpenBox(s tcell.Screen, rect Rect, style tcell.Style) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	DrawHLine(s, rect.X, rect.Y, rect.W, style)
	if rect.H > 1 {
		DrawHLine(s, rect.X, rect.Y+rect.H-1, rect.W, style)
	}
}

func DrawTimelineLine(s tcell.Screen, x, y, maxWidth int, line chatRenderLine) {
	if maxWidth <= 0 {
		return
	}
	if len(line.Spans) == 0 {
		DrawText(s, x, y, maxWidth, line.Style, line.Text)
		return
	}

	cx := x
	remaining := maxWidth
	for _, span := range line.Spans {
		if remaining <= 0 {
			break
		}
		if span.Text == "" {
			continue
		}
		written := DrawTextCount(s, cx, y, remaining, span.Style, span.Text)
		if written <= 0 {
			continue
		}
		cx += written
		remaining -= written
	}
}

func DrawTextCount(s tcell.Screen, x, y, maxWidth int, style tcell.Style, text string) int {
	if maxWidth <= 0 {
		return 0
	}
	cx := x
	for _, r := range text {
		if cx-x >= maxWidth {
			break
		}
		_, _, existing, _ := s.GetContent(cx, y)
		fg, _, attrs := style.Decompose()
		_, bg, _ := style.Decompose()
		if bg == tcell.ColorDefault {
			_, existingBG, _ := existing.Decompose()
			bg = existingBG
		}
		resolved := tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs)
		s.SetContent(cx, y, r, nil, resolved)
		cx++
	}
	return cx - x
}

func DrawText(s tcell.Screen, x, y, maxWidth int, style tcell.Style, text string) {
	DrawTextCount(s, x, y, maxWidth, style, text)
}

func DrawTextRight(s tcell.Screen, xRight, y, maxWidth int, style tcell.Style, text string) {
	w := utf8.RuneCountInString(text)
	if w > maxWidth {
		w = maxWidth
	}
	start := xRight - w + 1
	DrawText(s, start, y, maxWidth, style, text)
}

func DrawCenteredText(s tcell.Screen, x, y, maxWidth int, style tcell.Style, text string) {
	if maxWidth <= 0 {
		return
	}
	w := utf8.RuneCountInString(text)
	if w > maxWidth {
		w = maxWidth
	}
	start := x + (maxWidth-w)/2
	DrawText(s, start, y, maxWidth, style, text)
}

func Wrap(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts)*2)
	for _, part := range parts {
		if part == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapPlainLine(part, width)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

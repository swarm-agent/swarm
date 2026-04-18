package ui

import "github.com/gdamore/tcell/v2"

func styleForCurrentCellBackground(style tcell.Style) tcell.Style {
	fg, _, attrs := style.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(tcell.ColorDefault).Attributes(attrs)
}

func styleWithBackgroundFrom(foregroundStyle, backgroundStyle tcell.Style) tcell.Style {
	fg, _, fgAttrs := foregroundStyle.Decompose()
	_, bg, _ := backgroundStyle.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(fgAttrs)
}

func filledButtonStyle(roleStyle tcell.Style) tcell.Style {
	fill, _, attrs := roleStyle.Decompose()
	if !fill.Valid() || fill == tcell.ColorDefault {
		fill = tcell.ColorWhite
	}
	label := contrastingTextColor(fill)
	return tcell.StyleDefault.Foreground(label).Background(fill).Attributes(attrs | tcell.AttrBold)
}

func contrastingTextColor(background tcell.Color) tcell.Color {
	if !background.Valid() || background == tcell.ColorDefault {
		return tcell.ColorWhite
	}
	r, g, b := background.TrueColor().RGB()
	brightness := (299*r + 587*g + 114*b) / 1000
	if brightness >= 160 {
		return tcell.ColorBlack
	}
	return tcell.ColorWhite
}

func renderLineForCurrentCellBackground(line chatRenderLine) chatRenderLine {
	line.Style = styleForCurrentCellBackground(line.Style)
	if len(line.Spans) == 0 {
		return line
	}
	spans := make([]chatRenderSpan, len(line.Spans))
	for i, span := range line.Spans {
		spans[i] = chatRenderSpan{
			Text:  span.Text,
			Style: styleForCurrentCellBackground(span.Style),
		}
	}
	line.Spans = spans
	return line
}

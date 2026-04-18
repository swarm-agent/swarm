package ui

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarning
	ToastError
)

const (
	toastDefaultDuration = 3200 * time.Millisecond
	toastMargin          = 2
	toastMinWidth        = 26
	toastMaxWidth        = 72
	toastMaxBodyRows     = 3
)

type toastState struct {
	Message   string
	Level     ToastLevel
	ExpiresAt time.Time
}

func (t *toastState) show(level ToastLevel, message string, duration time.Duration) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	if duration <= 0 {
		duration = toastDefaultDuration
	}
	t.Message = message
	t.Level = level
	t.ExpiresAt = time.Now().Add(duration)
	return true
}

func (t *toastState) clear() bool {
	if strings.TrimSpace(t.Message) == "" {
		return false
	}
	t.Message = ""
	t.ExpiresAt = time.Time{}
	return true
}

func (t *toastState) visible(now time.Time) bool {
	if strings.TrimSpace(t.Message) == "" {
		return false
	}
	if t.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(t.ExpiresAt)
}

func (t *toastState) tick(now time.Time) bool {
	if strings.TrimSpace(t.Message) == "" {
		return false
	}
	if !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt) {
		return t.clear()
	}
	return false
}

func drawToastOverlay(s tcell.Screen, theme Theme, toast *toastState, bounds Rect, topInset int) {
	if toast == nil || !toast.visible(time.Now()) {
		return
	}
	if bounds.W < toastMinWidth+2*toastMargin || bounds.H < 6 {
		return
	}

	levelTitle := strings.ToUpper(strings.TrimSpace(toast.levelLabel()))
	if levelTitle == "" {
		levelTitle = "INFO"
	}
	maxOuterW := minInt(toastMaxWidth, bounds.W-(2*toastMargin))
	if maxOuterW < toastMinWidth {
		return
	}
	textW := maxInt(1, maxOuterW-4)

	bodyLines := Wrap(strings.TrimSpace(toast.Message), textW)
	if len(bodyLines) > toastMaxBodyRows {
		bodyLines = bodyLines[:toastMaxBodyRows]
		last := len(bodyLines) - 1
		bodyLines[last] = clampEllipsis(bodyLines[last], textW)
	}

	contentW := utf8.RuneCountInString(levelTitle)
	for i := range bodyLines {
		bodyLines[i] = strings.TrimSpace(bodyLines[i])
		if w := utf8.RuneCountInString(bodyLines[i]); w > contentW {
			contentW = w
		}
	}

	outerW := contentW + 4
	if outerW < toastMinWidth {
		outerW = toastMinWidth
	}
	if outerW > maxOuterW {
		outerW = maxOuterW
	}
	outerH := len(bodyLines) + 3
	if outerH > bounds.H-2 {
		return
	}

	x := bounds.X + bounds.W - outerW - toastMargin
	if x < bounds.X+1 {
		x = bounds.X + 1
	}
	minY := bounds.Y + 1
	maxY := bounds.Y + bounds.H - outerH - 1
	if maxY < minY {
		maxY = minY
	}
	y := bounds.Y + maxInt(1, topInset)
	if y < minY {
		y = minY
	}
	if y > maxY {
		y = maxY
	}

	rect := Rect{X: x, Y: y, W: outerW, H: outerH}
	borderStyle := toast.levelStyle(theme)

	FillRect(s, rect, theme.Panel)
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, borderStyle, levelTitle)
	for i, line := range bodyLines {
		DrawText(s, rect.X+2, rect.Y+2+i, rect.W-4, theme.Text, clampEllipsis(line, rect.W-4))
	}
}

func (t *toastState) levelLabel() string {
	switch t.Level {
	case ToastSuccess:
		return "success"
	case ToastWarning:
		return "warning"
	case ToastError:
		return "error"
	default:
		return "info"
	}
}

func (t *toastState) levelStyle(theme Theme) tcell.Style {
	switch t.Level {
	case ToastSuccess:
		return theme.Success
	case ToastWarning:
		return theme.Warning
	case ToastError:
		return theme.Error
	default:
		return theme.Primary
	}
}

// Package footerbar owns the canonical TUI home/footer bar renderer.
package footerbar

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type Styles struct {
	Border    tcell.Style
	Accent    tcell.Style
	Secondary tcell.Style
	Text      tcell.Style
}

type State struct {
	RouteLabel        string
	NotificationCount int
	DisplayedMode     string
	Agent             string
	ModelLabel        string
	Thinking          string
	ServiceTier       string
	PlanToggle        bool
	RightFacts        []string
	StatusLine        string
	StatusStyle       tcell.Style
}

type Token struct {
	Text   string
	Style  tcell.Style
	Action string
	Shrink bool
}

type Rect struct {
	X int
	Y int
	W int
	H int
}

const stackedWidthThreshold = 54

// ResponsiveHeight preserves the caller's normal footer height while reserving
// enough rows to stack controls on ultra-thin terminals.
func ResponsiveHeight(width, normalHeight int) int {
	minimum := normalHeight
	switch {
	case width < 32:
		minimum = 5
	case width < stackedWidthThreshold:
		minimum = 4
	}
	if normalHeight < minimum {
		return minimum
	}
	return normalHeight
}

func Tokens(styles Styles, state State) []Token {
	routeLabel := fallback(strings.TrimSpace(state.RouteLabel), "Local")
	if state.NotificationCount > 0 {
		routeLabel += " !" + strconv.Itoa(state.NotificationCount)
	}
	routeLabel = clampSwarmNotificationLabel(routeLabel, state.NotificationCount, 20)
	primaryStyle := currentCellBackground(styles.Accent.Bold(true))
	modeStyle := currentCellBackground(styles.Secondary.Bold(true))
	metaStyle := currentCellBackground(styles.Text)
	modeText := fallback(strings.TrimSpace(state.DisplayedMode), "plan")
	tokens := []Token{{Text: routeLabel, Style: primaryStyle, Action: "cycle-route"}}
	if state.PlanToggle {
		if planIndicatorVisible(modeText) {
			tokens = append(tokens, Token{Text: "Plan", Style: modeStyle})
		}
	} else {
		tokens = append(tokens, Token{Text: modeText, Style: modeStyle})
	}
	return append(tokens, Token{Text: AgentModelUnit(state), Style: metaStyle, Action: "open-agents-modal", Shrink: true})
}

func AgentModelUnit(state State) string {
	parts := make([]string, 0, 4)
	parts = append(parts, fallback(strings.TrimSpace(state.Agent), "swarm"))
	parts = append(parts, fallback(strings.TrimSpace(state.ModelLabel), "Model"))
	if thinking := strings.TrimSpace(state.Thinking); thinking != "" {
		parts = append(parts, thinking)
	}
	if priority := strings.TrimSpace(state.ServiceTier); priority != "" {
		parts = append(parts, priority)
	}
	return "[" + clamp(strings.Join(parts, " · "), 58) + "]"
}

func RightLine(state State, maxWidth int) string {
	segments := make([]string, 0, len(state.RightFacts))
	for _, fact := range state.RightFacts {
		if fact = strings.TrimSpace(fact); fact != "" {
			segments = append(segments, fact)
		}
	}
	return clamp(strings.Join(segments, "  "), maxWidth)
}

func Draw(screen tcell.Screen, styles Styles, rect Rect, state State, register func(Rect, Token)) {
	if rect.H <= 0 || rect.W <= 0 {
		return
	}
	lineY := rect.Y + rect.H - 1
	stacked := rect.W < stackedWidthThreshold && rect.H >= 3
	if stacked {
		drawHLine(screen, rect.X, rect.Y, rect.W, styles.Border)
	} else if rect.H >= 2 {
		drawHLine(screen, rect.X, lineY-1, rect.W, styles.Border)
	}
	textX, textW := rect.X, rect.W
	if rect.W > 2 {
		textX, textW = rect.X+1, rect.W-2
	}
	if textW <= 0 {
		return
	}
	if stacked {
		drawTokenRows(screen, textX, rect.Y+1, textW, rect.H-1, Tokens(styles, state), register)
		return
	}
	right := RightLine(state, 28)
	rightW := utf8.RuneCountInString(right)
	leftW := textW
	if rightW > 0 && leftW > rightW+2 {
		leftW -= rightW + 2
	}
	if status := strings.TrimSpace(state.StatusLine); status != "" {
		statusWidth := max(1, min(leftW, max(leftW/2, 18)))
		if rect.H >= 3 {
			drawTextRight(screen, textX+textW-1, rect.Y, statusWidth, state.StatusStyle, clamp(status, statusWidth))
		} else if leftW > statusWidth+2 {
			leftW -= statusWidth + 2
			drawTextRight(screen, textX+leftW-1, lineY, statusWidth, state.StatusStyle, clamp(status, statusWidth))
		}
	}
	DrawTokenRow(screen, textX, lineY, leftW, Tokens(styles, state), register)
	if rightW > 0 && textW > rightW+2 {
		drawTextRight(screen, textX+textW-1, lineY, rightW, styles.Secondary, right)
	}
}

func drawTokenRows(screen tcell.Screen, x, y, maxWidth, maxRows int, tokens []Token, register func(Rect, Token)) {
	for row := 0; row < maxRows && len(tokens) > 0; row++ {
		used := 0
		end := 0
		for end < len(tokens) {
			labelWidth := utf8.RuneCountInString(strings.TrimSpace(tokens[end].Text)) + 2
			if end > 0 {
				labelWidth++
			}
			if used+labelWidth > maxWidth {
				break
			}
			used += labelWidth
			end++
		}
		if end == 0 {
			end = 1
			shrunken := tokens[0]
			shrunken.Shrink = true
			DrawTokenRow(screen, x, y+row, maxWidth, []Token{shrunken}, register)
		} else {
			DrawTokenRow(screen, x, y+row, maxWidth, tokens[:end], register)
		}
		tokens = tokens[end:]
	}
}

func DrawTokenRow(screen tcell.Screen, x, y, maxWidth int, tokens []Token, register func(Rect, Token)) {
	if maxWidth <= 0 || len(tokens) == 0 {
		return
	}
	selected := make([]Token, 0, len(tokens))
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
			label = " " + clamp(strings.TrimSpace(token.Text), remaining-2) + " "
			width = utf8.RuneCountInString(label)
		}
		token.Text = label
		selected = append(selected, token)
		used += width
	}
	cx := x
	for i, token := range selected {
		if i > 0 {
			cx++
		}
		drawText(screen, cx, y, maxWidth-(cx-x), token.Style, token.Text)
		width := utf8.RuneCountInString(token.Text)
		if register != nil && token.Action != "" {
			register(Rect{X: cx, Y: y, W: width, H: 1}, token)
		}
		cx += width
	}
}

func currentCellBackground(style tcell.Style) tcell.Style {
	fg, _, attrs := style.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(tcell.ColorDefault).Attributes(attrs)
}

func drawText(screen tcell.Screen, x, y, maxWidth int, style tcell.Style, text string) {
	for _, r := range text {
		if maxWidth <= 0 {
			return
		}
		_, _, existing, _ := screen.GetContent(x, y)
		fg, bg, attrs := style.Decompose()
		if bg == tcell.ColorDefault {
			_, bg, _ = existing.Decompose()
		}
		screen.SetContent(x, y, r, nil, tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs))
		x++
		maxWidth--
	}
}

func drawTextRight(screen tcell.Screen, xRight, y, maxWidth int, style tcell.Style, text string) {
	width := min(utf8.RuneCountInString(text), maxWidth)
	drawText(screen, xRight-width+1, y, maxWidth, style, text)
}

func drawHLine(screen tcell.Screen, x, y, width int, style tcell.Style) {
	for i := 0; i < width; i++ {
		screen.SetContent(x+i, y, tcell.RuneHLine, nil, style)
	}
}

func clamp(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func clampSwarmNotificationLabel(label string, count, width int) string {
	label = strings.TrimSpace(label)
	if count <= 0 {
		return clamp(label, width)
	}
	suffix := " !" + strconv.Itoa(count)
	base := strings.TrimSpace(strings.TrimSuffix(label, suffix))
	baseWidth := width - utf8.RuneCountInString(suffix)
	if baseWidth < 1 {
		return clamp(strings.TrimSpace(suffix), width)
	}
	return clamp(base, baseWidth) + suffix
}

func planIndicatorVisible(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on", "plan":
		return true
	default:
		return false
	}
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

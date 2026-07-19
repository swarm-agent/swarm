package v3chat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	maxComposerRunes = 32 * 1024
	maxRowCacheItems = maxResidentMessages
)

// PageStyles are supplied by the app shell so this package does not depend on
// the legacy UI package or its chat rendering implementation.
type PageStyles struct {
	Background tcell.Style
	Panel      tcell.Style
	Border     tcell.Style
	Text       tcell.Style
	Muted      tcell.Style
	Primary    tcell.Style
	Accent     tcell.Style
	Success    tcell.Style
	Warning    tcell.Style
	Error      tcell.Style
	Prompt     tcell.Style
	Cursor     tcell.Style
}

type PageAction int

const (
	PageActionNone PageAction = iota
	PageActionHome
)

type cachedRows struct {
	signature string
	width     int
	lines     []string
}

// Page is a V3-native terminal view. The Runtime/Store remain the only session
// state authority; Page owns only ephemeral presentation state.
type Page struct {
	runtime *Runtime
	styles  PageStyles

	mu        sync.Mutex
	input     []rune
	cursor    int
	scroll    int
	follow    bool
	status    string
	errText   string
	busy      bool
	rowCache  map[string]cachedRows
	lastWidth int
}

func NewPage(runtime *Runtime, styles PageStyles) *Page {
	return &Page{runtime: runtime, styles: styles, follow: true, rowCache: make(map[string]cachedRows)}
}

func (p *Page) Runtime() *Runtime { return p.runtime }

func (p *Page) SetStyles(styles PageStyles) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.styles = styles
	p.mu.Unlock()
}

func (p *Page) SetStatus(status string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.status = strings.TrimSpace(status)
	p.mu.Unlock()
}

func (p *Page) InputValue() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.input)
}

func (p *Page) ClearInput() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.input = nil
	p.cursor = 0
	p.mu.Unlock()
}

func (p *Page) OpenNew(request NewSessionRequest) {
	if p == nil || p.runtime == nil {
		return
	}
	p.setBusy("creating session…", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := p.runtime.CreateAndSend(ctx, request)
		p.finishAsync("connected", err)
	}()
}

func (p *Page) Send(text string) {
	if p == nil || p.runtime == nil || strings.TrimSpace(text) == "" {
		return
	}
	p.setBusy("sending…", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := p.runtime.Send(ctx, text, nil)
		p.finishAsync("sent", err)
	}()
}

func (p *Page) StopRun() {
	if p == nil || p.runtime == nil {
		return
	}
	p.setBusy("stopping run…", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		err := p.runtime.StopActiveRun(ctx, "stopped from TUI")
		p.finishAsync("stop requested", err)
	}()
}

func (p *Page) Recover(workspacePath, cwdPath string) {
	if p == nil || p.runtime == nil {
		return
	}
	p.setBusy("rehydrating…", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := p.runtime.RecoverStale(ctx, workspacePath, cwdPath)
		p.finishAsync("reconnected", err)
	}()
}

func (p *Page) finishAsync(ok string, err error) {
	p.mu.Lock()
	p.busy = false
	if err != nil {
		p.errText = err.Error()
		p.status = ""
	} else {
		p.errText = ""
		p.status = ok
	}
	p.mu.Unlock()
	if p.runtime != nil {
		p.runtime.signalWake()
	}
}

func (p *Page) setBusy(status string, busy bool) {
	p.mu.Lock()
	p.status = status
	p.errText = ""
	p.busy = busy
	p.mu.Unlock()
	if p.runtime != nil {
		p.runtime.signalWake()
	}
}

func (p *Page) HandleKey(ev *tcell.EventKey) PageAction {
	if p == nil || ev == nil {
		return PageActionNone
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Key() {
	case tcell.KeyEscape:
		return PageActionHome
	case tcell.KeyEnter:
		text := strings.TrimSpace(string(p.input))
		if text != "" && !p.busy {
			p.input = nil
			p.cursor = 0
			p.follow = true
			p.scroll = 0
			go p.Send(text)
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.cursor > 0 {
			p.input = append(p.input[:p.cursor-1], p.input[p.cursor:]...)
			p.cursor--
		}
	case tcell.KeyDelete:
		if p.cursor < len(p.input) {
			p.input = append(p.input[:p.cursor], p.input[p.cursor+1:]...)
		}
	case tcell.KeyLeft:
		if p.cursor > 0 {
			p.cursor--
		}
	case tcell.KeyRight:
		if p.cursor < len(p.input) {
			p.cursor++
		}
	case tcell.KeyHome:
		p.cursor = 0
	case tcell.KeyEnd:
		p.cursor = len(p.input)
	case tcell.KeyPgUp:
		p.scroll += 8
		p.follow = false
	case tcell.KeyPgDn:
		p.scroll -= 8
		if p.scroll <= 0 {
			p.scroll = 0
			p.follow = true
		}
	case tcell.KeyCtrlX:
		go p.StopRun()
	case tcell.KeyCtrlR:
		// Recovery scope is already retained by the runtime's hydrated session.
		go p.Recover("", "")
	case tcell.KeyRune:
		if len(p.input) < maxComposerRunes {
			r := ev.Rune()
			p.input = append(p.input, 0)
			copy(p.input[p.cursor+1:], p.input[p.cursor:])
			p.input[p.cursor] = r
			p.cursor++
		}
	}
	return PageActionNone
}

func (p *Page) HandleMouse(ev *tcell.EventMouse) {
	if p == nil || ev == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	buttons := ev.Buttons()
	if buttons&tcell.WheelUp != 0 {
		p.scroll += 3
		p.follow = false
	}
	if buttons&tcell.WheelDown != 0 {
		p.scroll -= 3
		if p.scroll <= 0 {
			p.scroll = 0
			p.follow = true
		}
	}
}

type renderRow struct {
	text  string
	style tcell.Style
}

func (p *Page) Draw(screen tcell.Screen) {
	if p == nil || screen == nil {
		return
	}
	width, height := screen.Size()
	if width <= 0 || height <= 0 {
		return
	}
	p.mu.Lock()
	styles := p.styles
	input := append([]rune(nil), p.input...)
	cursor := p.cursor
	scroll := p.scroll
	status, errText, busy := p.status, p.errText, p.busy
	p.mu.Unlock()

	fill(screen, 0, 0, width, height, styles.Background)
	state := NewState()
	if p.runtime != nil && p.runtime.Store() != nil {
		state = p.runtime.Store().Snapshot()
	}
	title := strings.TrimSpace(SelectTitle(state))
	if title == "" {
		title = "New V3 session"
	}
	connection, stale, reason := SelectReconnect(state)
	header := " Swarm  •  " + title
	drawText(screen, 0, 0, width, styles.Panel.Bold(true), padRight(header, width))
	statusLine := fmt.Sprintf(" %s", connection)
	statusStyle := styles.Muted
	if stale {
		statusLine = " stale • Ctrl-R to rehydrate • " + reason
		statusStyle = styles.Warning
	} else if errText != "" {
		statusLine = " error • " + errText
		statusStyle = styles.Error
	} else if status != "" {
		statusLine = " " + status
		if !busy {
			statusStyle = styles.Success
		}
	}
	drawText(screen, 0, 1, width, statusStyle, padRight(statusLine, width))

	composerHeight := 3
	transcriptTop := 3
	transcriptHeight := height - transcriptTop - composerHeight
	if transcriptHeight < 1 {
		transcriptHeight = 1
	}
	rows := p.renderRows(state, maxInt(1, width-4), styles)
	start := len(rows) - transcriptHeight - scroll
	if start < 0 {
		start = 0
	}
	end := minInt(len(rows), start+transcriptHeight)
	for i := start; i < end; i++ {
		drawText(screen, 2, transcriptTop+i-start, width-4, rows[i].style, rows[i].text)
	}

	composerY := height - composerHeight
	if composerY < 2 {
		composerY = 2
	}
	drawHLine(screen, 0, composerY, width, styles.Border)
	hint := " Enter send  •  Ctrl-X stop  •  PgUp/PgDn scroll  •  Esc home "
	drawText(screen, 0, composerY+2, width, styles.Muted, padRight(hint, width))
	prefix := "> "
	available := maxInt(1, width-len(prefix)-1)
	inputText := string(input)
	visible, visibleCursor := composerWindow(inputText, cursor, available)
	drawText(screen, 0, composerY+1, width, styles.Prompt, prefix+visible)
	cursorX := len(prefix) + visibleCursor
	if cursorX >= width {
		cursorX = width - 1
	}
	if composerY+1 < height && cursorX >= 0 {
		r := ' '
		visibleRunes := []rune(visible)
		if visibleCursor < len(visibleRunes) {
			r = visibleRunes[visibleCursor]
		}
		screen.SetContent(cursorX, composerY+1, r, nil, styles.Cursor)
	}
}

func (p *Page) renderRows(state State, width int, styles PageStyles) []renderRow {
	rows := make([]renderRow, 0, len(state.Messages)*3+len(state.Pending)*2+len(state.Live)*2)
	messages := SelectMessages(state)
	for _, message := range messages {
		style := styles.Text
		label := "assistant"
		if strings.EqualFold(message.Role, "user") {
			style = styles.Primary
			label = "you"
		}
		rows = append(rows, renderRow{text: label, style: style.Bold(true)})
		for _, line := range p.cachedWrap("message:"+message.ID, message.Content, width) {
			rows = append(rows, renderRow{text: line, style: style})
		}
		rows = append(rows, renderRow{text: "", style: style})
	}
	pending := SelectPending(state)
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	for _, message := range pending {
		rows = append(rows, renderRow{text: "you • sending", style: styles.Warning.Bold(true)})
		for _, line := range p.cachedWrap("pending:"+message.ID, message.Content, width) {
			rows = append(rows, renderRow{text: line, style: styles.Text})
		}
		rows = append(rows, renderRow{text: "", style: styles.Text})
	}
	live := SelectLiveSegments(state)
	sort.SliceStable(live, func(i, j int) bool { return live[i].StreamID < live[j].StreamID })
	for _, segment := range live {
		rows = append(rows, renderRow{text: "assistant • streaming", style: styles.Accent.Bold(true)})
		for _, line := range wrapText(segment.Text, width) {
			rows = append(rows, renderRow{text: line, style: styles.Text})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, renderRow{text: "No messages yet.", style: styles.Muted})
	}
	return rows
}

func (p *Page) cachedWrap(key, text string, width int) []string {
	signature := fmt.Sprintf("%d:%s", len(text), text)
	p.mu.Lock()
	defer p.mu.Unlock()
	if cached, ok := p.rowCache[key]; ok && cached.width == width && cached.signature == signature {
		return cached.lines
	}
	lines := wrapText(text, width)
	if _, exists := p.rowCache[key]; !exists && len(p.rowCache) >= maxRowCacheItems {
		clear(p.rowCache)
	}
	p.rowCache[key] = cachedRows{signature: signature, width: width, lines: lines}
	return lines
}

func composerWindow(text string, cursor, width int) (string, int) {
	runes := []rune(text)
	cursor = maxInt(0, minInt(cursor, len(runes)))
	if width <= 0 {
		return "", 0
	}
	start := 0
	if cursor >= width {
		start = cursor - width + 1
	}
	end := minInt(len(runes), start+width)
	return string(runes[start:end]), cursor - start
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		runes := []rune(paragraph)
		if len(runes) == 0 {
			out = append(out, "")
			continue
		}
		for len(runes) > width {
			cut := width
			for i := width - 1; i > 0; i-- {
				if runes[i] == ' ' || runes[i] == '\t' {
					cut = i
					break
				}
			}
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
			for len(runes) > 0 && (runes[0] == ' ' || runes[0] == '\t') {
				runes = runes[1:]
			}
		}
		out = append(out, string(runes))
	}
	return out
}

func fill(s tcell.Screen, x, y, width, height int, style tcell.Style) {
	for row := y; row < y+height; row++ {
		for col := x; col < x+width; col++ {
			s.SetContent(col, row, ' ', nil, style)
		}
	}
}
func drawText(s tcell.Screen, x, y, width int, style tcell.Style, text string) {
	if width <= 0 {
		return
	}
	col := 0
	for _, r := range text {
		if col >= width {
			break
		}
		s.SetContent(x+col, y, r, nil, style)
		col++
	}
}
func drawHLine(s tcell.Screen, x, y, width int, style tcell.Style) {
	for i := 0; i < width; i++ {
		s.SetContent(x+i, y, tcell.RuneHLine, nil, style)
	}
}
func padRight(value string, width int) string {
	count := utf8.RuneCountInString(value)
	if count >= width {
		return value
	}
	return value + strings.Repeat(" ", width-count)
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package v3chat

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

const (
	maxComposerRunes       = 32 * 1024
	maxComposerVisibleRows = 8
	maxRowCacheItems       = maxResidentMessages
)

// PageStyles are supplied by the app shell so this package does not depend on
// the legacy UI package or its chat rendering implementation.
type PageStyles struct {
	Background     tcell.Style
	Panel          tcell.Style
	Element        tcell.Style
	Border         tcell.Style
	Text           tcell.Style
	Muted          tcell.Style
	Primary        tcell.Style
	Accent         tcell.Style
	Secondary      tcell.Style
	Success        tcell.Style
	Warning        tcell.Style
	Error          tcell.Style
	Prompt         tcell.Style
	Cursor         tcell.Style
	RenderMarkdown func(string, int) []MarkdownLine
}

type MarkdownSpan struct {
	Text  string
	Style tcell.Style
}

type MarkdownLine struct {
	Text  string
	Style tcell.Style
	Spans []MarkdownSpan
}

type PageAction int

const (
	PageActionNone PageAction = iota
	PageActionHome
	PageActionCommand
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

	mu                  sync.Mutex
	input               []rune
	cursor              int
	pasteActive         bool
	pasteBuffer         []rune
	scroll              int
	follow              bool
	status              string
	errText             string
	busy                bool
	rowCache            map[string]cachedRows
	lastWidth           int
	lastHeight          int
	modelTarget         footerbar.Rect
	routeLabel          string
	profileLabel        string
	modelPicker         bool
	modelLoading        bool
	modelOptions        []client.ModelCatalogRecord
	modelIndex          int
	commandSuggestions  []CommandSuggestion
	commandPaletteIndex int
	pendingCommand      string
	matchKey            func(*tcell.EventKey, string) bool
	runTimer            *time.Timer
}

const (
	KeyEscape        = "escape"
	KeyMoveUp        = "move_up"
	KeyMoveDown      = "move_down"
	KeyMoveUpAlt     = "move_up_alt"
	KeyMoveDownAlt   = "move_down_alt"
	KeyPageUp        = "page_up"
	KeyPageDown      = "page_down"
	KeyJumpHome      = "jump_home"
	KeyJumpEnd       = "jump_end"
	KeyBackspace     = "backspace"
	KeyMoveLeft      = "move_left"
	KeyMoveRight     = "move_right"
	KeyClear         = "clear"
	KeyCycleMode     = "cycle_mode"
	KeyComplete      = "complete"
	KeyInsertNewline = "insert_newline"
	KeySubmit        = "submit"
)

func NewPage(runtime *Runtime, styles PageStyles) *Page {
	return &Page{runtime: runtime, styles: styles, follow: true, rowCache: make(map[string]cachedRows), matchKey: defaultKeyMatcher}
}

func (p *Page) SetKeyMatcher(match func(*tcell.EventKey, string) bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if match == nil {
		p.matchKey = defaultKeyMatcher
	} else {
		p.matchKey = match
	}
	p.mu.Unlock()
}

func defaultKeyMatcher(ev *tcell.EventKey, action string) bool {
	if ev == nil {
		return false
	}
	switch action {
	case KeyEscape:
		return ev.Key() == tcell.KeyEscape
	case KeyMoveUp:
		return ev.Key() == tcell.KeyUp
	case KeyMoveDown:
		return ev.Key() == tcell.KeyDown
	case KeyMoveUpAlt:
		return ev.Key() == tcell.KeyRune && ev.Rune() == 'k' && ev.Modifiers()&tcell.ModAlt != 0
	case KeyMoveDownAlt:
		return ev.Key() == tcell.KeyRune && ev.Rune() == 'j' && ev.Modifiers()&tcell.ModAlt != 0
	case KeyPageUp:
		return ev.Key() == tcell.KeyPgUp
	case KeyPageDown:
		return ev.Key() == tcell.KeyPgDn
	case KeyJumpHome:
		return ev.Key() == tcell.KeyHome
	case KeyJumpEnd:
		return ev.Key() == tcell.KeyEnd
	case KeyBackspace:
		return ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2
	case KeyMoveLeft:
		return ev.Key() == tcell.KeyLeft
	case KeyMoveRight:
		return ev.Key() == tcell.KeyRight
	case KeyClear:
		return ev.Key() == tcell.KeyCtrlU
	case KeyCycleMode:
		return ev.Key() == tcell.KeyBacktab || ev.Key() == tcell.KeyTab && ev.Modifiers()&tcell.ModShift != 0
	case KeyComplete:
		return ev.Key() == tcell.KeyTab
	case KeyInsertNewline:
		return ev.Key() == tcell.KeyCtrlJ
	case KeySubmit:
		return ev.Key() == tcell.KeyEnter
	}
	return false
}

func (p *Page) Runtime() *Runtime { return p.runtime }

func (p *Page) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.runTimer != nil {
		p.runTimer.Stop()
		p.runTimer = nil
	}
	p.mu.Unlock()
}

func (p *Page) SetStyles(styles PageStyles) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.styles = styles
	p.mu.Unlock()
}

// SetRouteLabel supplies the backend-resolved Swarm target name used by the
// shared canonical footer. An empty value deliberately leaves fallback
// presentation to footerbar rather than inventing session identity here.
func (p *Page) SetRouteLabel(label string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.routeLabel = strings.TrimSpace(label)
	p.mu.Unlock()
}

func (p *Page) SetProfileLabel(label string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.profileLabel = strings.TrimSpace(label)
	p.mu.Unlock()
}

func (p *Page) RouteLabel() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.routeLabel
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

func (p *Page) ConsumeCommand() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	command := p.pendingCommand
	p.pendingCommand = ""
	return command
}

func (p *Page) ClearInput() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.input = nil
	p.cursor = 0
	p.pasteBuffer = nil
	p.mu.Unlock()
}

func (p *Page) SetPasteActive(active bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	wasActive := p.pasteActive
	p.pasteActive = active
	if wasActive && !active {
		p.flushPasteBufferLocked()
	}
	p.mu.Unlock()
}

func (p *Page) flushPasteBufferLocked() {
	if len(p.pasteBuffer) == 0 {
		return
	}
	p.insertRunesLocked(p.pasteBuffer)
	p.pasteBuffer = nil
}

func (p *Page) insertRunesLocked(chunk []rune) {
	remaining := maxComposerRunes - len(p.input)
	if remaining <= 0 || len(chunk) == 0 {
		return
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	inserted := append([]rune(nil), chunk...)
	p.input = append(p.input, make([]rune, len(inserted))...)
	copy(p.input[p.cursor+len(inserted):], p.input[p.cursor:len(p.input)-len(inserted)])
	copy(p.input[p.cursor:], inserted)
	p.cursor += len(inserted)
}

func (p *Page) handlePasteKeyLocked(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyRune:
		r := ev.Rune()
		if r == '\r' {
			r = '\n'
		}
		if r == '\n' || r == '\t' || r >= ' ' {
			if r == '\t' {
				r = ' '
			}
			p.pasteBuffer = append(p.pasteBuffer, r)
		}
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		p.pasteBuffer = append(p.pasteBuffer, '\n')
	case tcell.KeyTab, tcell.KeyBacktab:
		p.pasteBuffer = append(p.pasteBuffer, ' ')
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(p.pasteBuffer) > 0 {
			p.pasteBuffer = p.pasteBuffer[:len(p.pasteBuffer)-1]
		}
	}
	if len(p.pasteBuffer) >= 256 {
		p.flushPasteBufferLocked()
	}
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
	if p.modelPicker {
		return p.handleModelPickerKeyLocked(ev)
	}
	if p.pasteActive {
		p.handlePasteKeyLocked(ev)
		return PageActionNone
	}
	match := func(action string) bool { return p.matchKey != nil && p.matchKey(ev, action) }
	if match(KeyCycleMode) {
		p.cycleModeLocked()
		return PageActionNone
	}
	switch {
	case match(KeyEscape):
		if p.runtime != nil {
			if _, active := SelectActiveRun(p.runtime.Store().Snapshot()); active {
				go p.StopRun()
				return PageActionNone
			}
		}
		return PageActionHome
	case match(KeyMoveUp):
		if !p.moveCommandPaletteSelectionLocked(-1) {
			p.scroll++
			p.follow = false
		}
	case match(KeyMoveDown):
		if !p.moveCommandPaletteSelectionLocked(1) {
			p.scroll--
			if p.scroll <= 0 {
				p.scroll = 0
				p.follow = true
			}
		}
	case match(KeyPageUp):
		p.scroll += 8
		p.follow = false
	case match(KeyPageDown):
		p.scroll -= 8
		if p.scroll <= 0 {
			p.scroll = 0
			p.follow = true
		}
	case match(KeyJumpHome):
		p.scroll = 1 << 30
		p.follow = false
	case match(KeyJumpEnd):
		p.scroll = 0
		p.follow = true
	case match(KeyComplete):
		p.completeCommandFromPaletteLocked()
	case match(KeySubmit):
		if p.acceptCommandPaletteEnterLocked() {
			return PageActionNone
		}
		text := strings.TrimSpace(string(p.input))
		if strings.HasPrefix(text, "/") {
			p.pendingCommand = text
			p.input = nil
			p.cursor = 0
			return PageActionCommand
		}
		if text != "" && !p.busy {
			p.input = nil
			p.cursor = 0
			p.follow = true
			p.scroll = 0
			go p.Send(text)
		}
	case match(KeyInsertNewline):
		p.insertRunesLocked([]rune{'\n'})
	case match(KeyBackspace):
		if p.cursor > 0 {
			p.input = append(p.input[:p.cursor-1], p.input[p.cursor:]...)
			p.cursor--
		}
	case ev.Key() == tcell.KeyDelete:
		if p.cursor < len(p.input) {
			p.input = append(p.input[:p.cursor], p.input[p.cursor+1:]...)
		}
	case match(KeyMoveLeft):
		if p.cursor > 0 {
			p.cursor--
		}
	case match(KeyMoveRight):
		if p.cursor < len(p.input) {
			p.cursor++
		}
	case ev.Key() == tcell.KeyHome:
		p.cursor = 0
	case ev.Key() == tcell.KeyEnd:
		p.cursor = len(p.input)
	case match(KeyClear):
		p.input = nil
		p.cursor = 0
		p.pasteBuffer = nil
		p.commandPaletteIndex = 0
	case ev.Key() == tcell.KeyF2:
		p.openModelPickerLocked()
	case ev.Key() == tcell.KeyCtrlX:
		go p.StopRun()
	case ev.Key() == tcell.KeyCtrlR:
		// Recovery scope is already retained by the runtime's hydrated session.
		go p.Recover("", "")
	case ev.Key() == tcell.KeyRune:
		if match(KeyMoveUpAlt) {
			p.scroll++
			p.follow = false
			break
		}
		if match(KeyMoveDownAlt) {
			p.scroll--
			if p.scroll <= 0 {
				p.scroll = 0
				p.follow = true
			}
			break
		}
		p.insertRunesLocked([]rune{ev.Rune()})
		p.syncCommandPaletteSelectionLocked()
	}
	return PageActionNone
}

func (p *Page) cycleModeLocked() {
	if p.runtime == nil || p.busy {
		return
	}
	state := p.runtime.Store().Snapshot()
	if strings.TrimSpace(state.Session.ID) == "" {
		p.errText = "plan mode is available after the session connects"
		return
	}
	next := "plan"
	if strings.EqualFold(strings.TrimSpace(state.Session.Mode), "plan") {
		next = "auto"
	}
	p.busy = true
	p.status = "switching Plan " + map[bool]string{true: "on", false: "off"}[next == "plan"] + "…"
	p.errText = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resolved, err := p.runtime.SetMode(ctx, next)
		if err != nil {
			p.finishAsync("", err)
			return
		}
		p.finishAsync("Plan: "+map[bool]string{true: "on", false: "off"}[strings.EqualFold(resolved.Mode, "plan")], nil)
	}()
}

func (p *Page) openModelPickerLocked() {
	if p.runtime == nil || p.modelLoading {
		return
	}
	state := p.runtime.Store().Snapshot()
	if state.Session.ID == "" {
		p.errText = "model selection is available after the session connects"
		return
	}
	if state.Model.Locked {
		p.errText = firstNonEmpty(state.Model.LockReason, "session model is controlled by its agent policy")
		return
	}
	p.modelPicker = true
	p.modelLoading = true
	p.modelOptions = nil
	p.modelIndex = 0
	p.errText = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		options, err := p.runtime.ListModelOptions(ctx)
		sort.SliceStable(options, func(i, j int) bool {
			left := strings.ToLower(strings.TrimSpace(options[i].Provider)) + "/" + strings.ToLower(strings.TrimSpace(options[i].Model))
			right := strings.ToLower(strings.TrimSpace(options[j].Provider)) + "/" + strings.ToLower(strings.TrimSpace(options[j].Model))
			return left < right
		})
		p.mu.Lock()
		p.modelLoading = false
		if err != nil {
			p.errText = err.Error()
			p.modelPicker = false
		} else {
			p.modelOptions = options
			current := SelectModel(p.runtime.Store().Snapshot()).Preference
			for i, option := range options {
				if strings.EqualFold(option.Provider, current.Provider) && option.Model == current.Model && option.ContextMode == current.ContextMode {
					p.modelIndex = i
					break
				}
			}
		}
		p.mu.Unlock()
		p.runtime.signalWake()
	}()
}

func (p *Page) handleModelPickerKeyLocked(ev *tcell.EventKey) PageAction {
	switch ev.Key() {
	case tcell.KeyEscape:
		p.modelPicker = false
		p.modelOptions = nil
	case tcell.KeyUp:
		if p.modelIndex > 0 {
			p.modelIndex--
		}
	case tcell.KeyDown:
		if p.modelIndex+1 < len(p.modelOptions) {
			p.modelIndex++
		}
	case tcell.KeyPgUp:
		p.modelIndex = maxInt(0, p.modelIndex-8)
	case tcell.KeyPgDn:
		p.modelIndex = minInt(maxInt(0, len(p.modelOptions)-1), p.modelIndex+8)
	case tcell.KeyEnter:
		if p.modelLoading || p.modelIndex < 0 || p.modelIndex >= len(p.modelOptions) {
			return PageActionNone
		}
		option := p.modelOptions[p.modelIndex]
		current := SelectModel(p.runtime.Store().Snapshot()).Preference
		thinking := current.Thinking
		if !containsFold(option.ThinkingOptions, thinking) {
			thinking = strings.TrimSpace(option.DefaultThinking)
		}
		serviceTier := current.ServiceTier
		if !containsFold(option.ServiceTiers, serviceTier) {
			serviceTier = strings.TrimSpace(option.DefaultServiceTier)
		}
		preference := client.ModelPreference{Provider: option.Provider, Model: option.Model, Thinking: thinking, ServiceTier: serviceTier, ContextMode: option.ContextMode}
		p.modelPicker = false
		p.modelOptions = nil
		p.busy = true
		p.status = "updating model…"
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resolved, err := p.runtime.SetModelPreference(ctx, preference)
			if err != nil {
				p.finishAsync("", err)
				return
			}
			p.finishAsync("model set • "+modelPreferenceLabel(resolved.Preference), nil)
		}()
	}
	return PageActionNone
}

func (p *Page) HandleMouse(ev *tcell.EventMouse) {
	if p == nil || ev == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 && containsFooterPoint(p.modelTarget, x, y) {
		p.openModelPickerLocked()
		return
	}
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

type renderSpan struct {
	text  string
	style tcell.Style
}

type renderRow struct {
	text           string
	style          tcell.Style
	spans          []renderSpan
	prefixWidth    int
	prefixStyle    tcell.Style
	highlightStart int
	highlightWidth int
	highlightStyle tcell.Style
}

func styleWithForeground(style, foregroundStyle tcell.Style) tcell.Style {
	_, background, attributes := style.Decompose()
	foreground, _, _ := foregroundStyle.Decompose()
	return tcell.StyleDefault.Foreground(foreground).Background(background).Attributes(attributes)
}

func (p *Page) Draw(screen tcell.Screen) {
	p.DrawAt(screen, time.Now())
}

func (p *Page) DrawAt(screen tcell.Screen, now time.Time) {
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
	errText, status := p.errText, p.status
	routeLabel, profileLabel := p.routeLabel, p.profileLabel
	p.lastWidth, p.lastHeight = width, height
	modelPicker, modelLoading, modelIndex := p.modelPicker, p.modelLoading, p.modelIndex
	modelOptions := append([]client.ModelCatalogRecord(nil), p.modelOptions...)
	commandSuggestions := append([]CommandSuggestion(nil), p.commandSuggestions...)
	commandPaletteIndex := p.commandPaletteIndex
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
	_, stale, reason := SelectReconnect(state)
	runStatus, hasRunStatus := BuildRunStatus(state, now)
	headerRight := ""
	if hasRunStatus {
		headerRight = runStatus.Label
		if runStatus.Timer != "" {
			headerRight += "  " + runStatus.Timer
		}
	}
	drawHeader(screen, width, styles.Panel.Bold(true), title, headerRight)
	statusLine := ""
	statusStyle := styles.Muted
	if stale {
		statusLine = "stale • Ctrl-R to rehydrate • " + reason
		statusStyle = styles.Warning
	} else if errText != "" {
		statusLine = "error • " + errText
		statusStyle = styles.Error
	} else if status != "" {
		statusLine = status
	}
	p.scheduleRunTimer(runStatus.Active)

	// Keep the footer to its separator and token row so the separator is also
	// the composer's bottom border, directly beneath the editable rows.
	footerHeight := 2
	composerLines, composerCursorLine, composerCursorCol := composerLayout(string(input), cursor, width)
	composerVisibleRows := minInt(len(composerLines), maxComposerVisibleRows)
	composerVisibleRows = minInt(composerVisibleRows, maxInt(1, height-footerHeight-3))
	composerStart := inputVisibleWindow(len(composerLines), composerVisibleRows, composerCursorLine)
	composerHeight := 1 + composerVisibleRows + footerHeight
	transcriptTop := 1
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
		row := rows[i]
		y := transcriptTop + i - start
		if len(row.spans) > 0 {
			drawSpans(screen, 2, y, width-4, row.spans)
		} else {
			drawText(screen, 2, y, width-4, row.style, row.text)
		}
		if row.prefixWidth > 0 {
			drawText(screen, 2, y, minInt(width-4, row.prefixWidth), row.prefixStyle, row.text)
		}
		if row.highlightWidth > 0 && row.highlightStart < width-4 {
			highlight := runeSlice(row.text, row.highlightStart, row.highlightStart+row.highlightWidth)
			drawText(screen, 2+row.highlightStart, y, minInt(width-4-row.highlightStart, row.highlightWidth), row.highlightStyle, highlight)
		}
	}

	composerY := height - composerHeight
	if composerY < 2 {
		composerY = 2
	}
	drawHLine(screen, 0, composerY, width, styles.Border)
	modelState := SelectModel(state)
	footerY := height - footerHeight
	p.drawCanonicalFooter(screen, footerbar.Rect{X: 0, Y: footerY, W: width, H: footerHeight}, state, routeLabel, profileLabel, statusLine, statusStyle)
	composerEnd := minInt(len(composerLines), composerStart+composerVisibleRows)
	for i := composerStart; i < composerEnd; i++ {
		drawText(screen, 0, composerY+1+i-composerStart, width, styles.Prompt, composerLines[i])
	}
	cursorY := composerY + 1 + composerCursorLine - composerStart
	cursorX := minInt(maxInt(0, composerCursorCol), width-1)
	if cursorY >= composerY+1 && cursorY < footerY {
		r := ' '
		lineRunes := []rune(composerLines[composerCursorLine])
		if composerCursorCol < len(lineRunes) {
			r = lineRunes[composerCursorCol]
		}
		screen.SetContent(cursorX, cursorY, r, nil, styles.Cursor)
	}
	if modelPicker {
		p.drawModelPicker(screen, width, height, styles, modelOptions, modelIndex, modelLoading, modelState)
	} else {
		p.drawCommandPalette(screen, width, transcriptTop, composerY, styles, string(input), commandPaletteIndex, commandSuggestions)
	}
}

type RunStatus struct {
	Label  string
	Timer  string
	Active bool
}

func BuildRunStatus(state State, now time.Time) (RunStatus, bool) {
	run, ok := SelectActiveRun(state)
	if !ok {
		run, ok = SelectLatestRun(state)
	}
	if !ok {
		return RunStatus{}, false
	}
	status := strings.ToLower(strings.TrimSpace(run.Status))
	model := RunStatus{}
	switch status {
	case "pending_executor", "running":
		model.Label, model.Active = "Running", true
	case "dispatch_blocked":
		model.Label = "Paused"
	case "completed":
		model.Label = "Completed"
	case "failed":
		model.Label = "Failed"
	case "cancelled":
		model.Label = "Stopped"
	case "interrupted":
		model.Label = "Interrupted"
	case "expired":
		model.Label = "Expired"
	default:
		return RunStatus{}, false
	}
	currentMS := run.DurationMS
	hasCurrentTiming := run.DurationMS > 0
	if model.Active {
		startedAt := firstPositive(run.StartedAt, run.CreatedAt)
		if startedAt > 0 {
			currentMS = maxInt64(0, now.UnixMilli()-startedAt)
			hasCurrentTiming = true
		}
	}
	if hasCurrentTiming {
		model.Timer = formatDurationMS(currentMS)
	}
	if run.CumulativeDurationMS > 0 {
		totalMS := run.CumulativeDurationMS
		if model.Active && hasCurrentTiming {
			totalMS += currentMS
		}
		total := formatDurationMS(totalMS)
		if model.Timer == "" {
			model.Timer = total
		} else if total != model.Timer {
			model.Timer += " (" + total + ")"
		}
	}
	return model, true
}

func formatDurationMS(elapsedMS int64) string {
	if elapsedMS < 0 {
		return ""
	}
	totalSeconds := elapsedMS / 1000
	seconds := totalSeconds % 60
	minutes := (totalSeconds / 60) % 60
	hours := totalSeconds / 3600
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func drawHeader(screen tcell.Screen, width int, style tcell.Style, title, right string) {
	drawText(screen, 0, 0, width, style, padRight("", width))
	rightRunes := []rune(strings.TrimSpace(right))
	titleWidth := width
	if len(rightRunes) > 0 {
		titleWidth = maxInt(0, width-len(rightRunes)-2)
		drawText(screen, width-len(rightRunes), 0, len(rightRunes), style, string(rightRunes))
	}
	drawText(screen, 0, 0, titleWidth, style, strings.TrimSpace(title))
}

func (p *Page) scheduleRunTimer(active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !active {
		if p.runTimer != nil {
			p.runTimer.Stop()
			p.runTimer = nil
		}
		return
	}
	if p.runTimer != nil {
		return
	}
	delay := time.Second - time.Duration(time.Now().UnixNano()%int64(time.Second))
	p.runTimer = time.AfterFunc(delay, func() {
		p.mu.Lock()
		p.runTimer = nil
		p.mu.Unlock()
		if p.runtime != nil {
			p.runtime.signalWake()
		}
	})
}

func (p *Page) drawCanonicalFooter(screen tcell.Screen, rect footerbar.Rect, state State, routeLabel, profileLabel, status string, statusStyle tcell.Style) {
	modelState := SelectModel(state)
	usage := SelectUsage(state)
	displayedMode := "off"
	if strings.EqualFold(strings.TrimSpace(state.Session.Mode), "plan") {
		displayedMode = "on"
	}
	resolvedProfileLabel := modelProfileLabel(modelState)
	if resolvedProfileLabel == "" {
		resolvedProfileLabel = strings.TrimSpace(profileLabel)
	}
	footerState := footerbar.State{
		RouteLabel:     strings.TrimSpace(routeLabel),
		DisplayedMode:  displayedMode,
		ProfileLabel:   resolvedProfileLabel,
		ModelLabel:     displayModelLabel(modelState.Preference),
		Thinking:       strings.TrimSpace(modelState.Preference.Thinking),
		ServiceTier:    strings.TrimSpace(modelState.Preference.ServiceTier),
		UnifiedProfile: true,
		PlanToggle:     true,
		RightFacts:     conversationContextFacts(usage, modelState.ContextWindow),
		StatusLine:     strings.TrimSpace(status),
		StatusStyle:    statusStyle,
	}
	footerbar.Draw(screen, footerbar.Styles{Border: p.styles.Border, Accent: p.styles.Accent, Secondary: p.styles.Secondary, Text: p.styles.Text}, rect, footerState, func(target footerbar.Rect, token footerbar.Token) {
		if token.Action == "open-profiles-modal" {
			p.mu.Lock()
			p.modelTarget = target
			p.mu.Unlock()
		}
	})
}

func conversationContextFacts(usage UsageState, fallbackWindow int) []string {
	window := usage.ContextWindow
	if window <= 0 {
		window = fallbackWindow
	}
	if window <= 0 {
		return nil
	}

	remaining := int64(window)
	if usage.Available {
		remaining = usage.RemainingTokens
	}
	remaining = maxInt64(0, remaining)
	if remaining > int64(window) {
		remaining = int64(window)
	}
	percentage := int(math.Round(float64(remaining) * 100 / float64(window)))
	return []string{fmt.Sprintf("ctx %d%%", percentage)}
}

func modelProfileLabel(state ModelState) string {
	source := strings.ToLower(strings.TrimSpace(state.ProfileSource))
	switch source {
	case "saved":
		if name := strings.TrimSpace(state.ProfileName); name != "" {
			return name
		}
		return "Saved profile"
	case "temporary":
		return "Temporary/customized"
	default:
		return ""
	}
}

func displayModelLabel(preference client.ModelPreference) string {
	return model.DisplayModelLabel(preference.Provider, preference.Model, preference.ServiceTier, preference.ContextMode)
}

func containsFooterPoint(rect footerbar.Rect, x, y int) bool {
	return rect.W > 0 && rect.H > 0 && x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H
}

func modelPreferenceLabel(preference client.ModelPreference) string {
	provider, model := strings.TrimSpace(preference.Provider), strings.TrimSpace(preference.Model)
	label := strings.Trim(strings.Join([]string{provider, model}, "/"), "/")
	if tier := strings.TrimSpace(preference.ServiceTier); tier != "" {
		label += " · " + tier
	}
	if contextMode := strings.TrimSpace(preference.ContextMode); contextMode != "" {
		label += " · " + contextMode
	}
	return label
}

func containsFold(values []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}

func (p *Page) drawModelPicker(screen tcell.Screen, width, height int, styles PageStyles, options []client.ModelCatalogRecord, selected int, loading bool, modelState ModelState) {
	modalWidth := minInt(maxInt(44, width-12), 84)
	modalHeight := minInt(maxInt(8, height-6), 18)
	if modalWidth > width {
		modalWidth = width
	}
	if modalHeight > height {
		modalHeight = height
	}
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawText(screen, x+2, y, modalWidth-4, styles.Primary.Bold(true), "Model • backend catalog")
	if loading {
		drawText(screen, x+2, y+2, modalWidth-4, styles.Muted, "Loading available models…")
		return
	}
	if len(options) == 0 {
		drawText(screen, x+2, y+2, modalWidth-4, styles.Warning, "No runnable models are available.")
		return
	}
	visibleRows := maxInt(1, modalHeight-3)
	start := maxInt(0, selected-visibleRows/2)
	if start+visibleRows > len(options) {
		start = maxInt(0, len(options)-visibleRows)
	}
	for i := start; i < minInt(len(options), start+visibleRows); i++ {
		option := options[i]
		label := modelPreferenceLabel(client.ModelPreference{Provider: option.Provider, Model: option.Model, ContextMode: option.ContextMode})
		prefix := "  "
		style := styles.Text
		if i == selected {
			prefix = "› "
			style = styles.Accent.Bold(true)
		}
		if strings.EqualFold(option.Provider, modelState.Preference.Provider) && option.Model == modelState.Preference.Model && option.ContextMode == modelState.Preference.ContextMode {
			label += "  ✓"
		}
		drawText(screen, x+2, y+1+i-start, modalWidth-4, style, prefix+label)
	}
}

type timelineRenderItem struct {
	kind      string
	seq       uint64
	createdAt int64
	order     int
	message   Message
	tool      ToolTimelineItem
	live      LiveSegment
}

func (p *Page) renderRows(state State, width int, styles PageStyles) []renderRow {
	items := make([]timelineRenderItem, 0, len(state.Messages)+len(state.Tools)+len(state.Live))
	for _, message := range SelectMessages(state) {
		item := timelineRenderItem{kind: "message", seq: message.GlobalSeq, createdAt: message.CreatedAt, order: len(items), message: message}
		if tool, ok := parseToolMessage(message); ok {
			item.kind, item.tool = "tool", tool
		}
		items = append(items, item)
	}
	for _, tool := range SelectLiveTools(state) {
		items = append(items, timelineRenderItem{kind: "tool", seq: tool.GlobalSeq, createdAt: tool.CreatedAt, order: len(items), tool: tool})
	}
	for _, segment := range SelectLiveSegments(state) {
		items = append(items, timelineRenderItem{kind: "live", seq: segment.GlobalSeq, createdAt: segment.CreatedAt, order: len(items), live: segment})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.seq != right.seq {
			if left.seq == 0 {
				return false
			}
			if right.seq == 0 {
				return true
			}
			return left.seq < right.seq
		}
		if left.createdAt != right.createdAt {
			return left.createdAt < right.createdAt
		}
		return left.order < right.order
	})

	rows := make([]renderRow, 0, len(items)*3+len(state.Pending)*2)
	for _, item := range items {
		switch item.kind {
		case "tool":
			rows = append(rows, p.renderToolRows(item.tool, width, styles)...)
		case "live":
			rows = append(rows, p.renderAssistantRows(item.live.Text, width, styles)...)
		case "message":
			message := item.message
			if strings.EqualFold(message.Role, "user") {
				if len(rows) == 0 {
					rows = append(rows, renderRow{text: "", style: styles.Text})
				}
				rows = append(rows, p.renderUserRows("message:"+message.ID, message.Content, width, styles)...)
				continue
			}
			rows = append(rows, p.renderAssistantRows(message.Content, width, styles)...)
			rows = append(rows, renderRow{text: "", style: styles.Text})
		}
	}

	pending := SelectPending(state)
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	for _, message := range pending {
		rows = append(rows, p.renderUserRows("pending:"+message.ID, message.Content, width, styles)...)
	}
	return rows
}

func (p *Page) renderAssistantRows(content string, width int, styles PageStyles) []renderRow {
	if styles.RenderMarkdown == nil {
		rows := make([]renderRow, 0)
		for _, line := range wrapText(content, width) {
			rows = append(rows, renderRow{text: line, style: styles.Text})
		}
		return rows
	}
	lines := styles.RenderMarkdown(content, width)
	rows := make([]renderRow, 0, len(lines))
	for _, line := range lines {
		row := renderRow{text: line.Text, style: line.Style, spans: make([]renderSpan, 0, len(line.Spans))}
		for _, span := range line.Spans {
			row.spans = append(row.spans, renderSpan{text: span.Text, style: span.Style})
		}
		rows = append(rows, row)
	}
	return rows
}

func (p *Page) renderToolRows(tool ToolTimelineItem, width int, styles PageStyles) []renderRow {
	status := strings.ToLower(strings.TrimSpace(tool.Status))
	symbol, headerStyle := "•", styles.Accent
	switch status {
	case "completed", "done", "success":
		symbol, headerStyle = "✓", styles.Success
	case "failed", "error", "cancelled", "canceled":
		symbol, headerStyle = "✕", styles.Error
	}

	presentation := buildToolPresentation(tool)
	toolName := normalizeToolDisplayName(tool.Name)
	summary := strings.TrimSpace(presentation.Summary)
	if summary != toolName && !strings.HasPrefix(summary, toolName+" ") {
		summary = toolName + " · " + summary
	}
	header := symbol + " " + summary
	if duration := toolDurationLabel(tool.DurationMS); duration != "" {
		header += " · " + duration
	}
	rows := []renderRow{{
		text:           header,
		style:          styles.Text.Bold(true),
		prefixWidth:    utf8.RuneCountInString(symbol),
		prefixStyle:    headerStyle.Bold(true),
		highlightStart: utf8.RuneCountInString(symbol + " "),
		highlightWidth: utf8.RuneCountInString(toolName),
		highlightStyle: styleWithForeground(styles.Text.Bold(true), styles.Primary),
	}}
	bodyLimit := 14
	if normalizeToolDisplayName(tool.Name) == "bash" {
		bodyLimit = 10
	}
	bodyRows := 0
	bodyClipped := false
	appendLines := func(lines []toolPresentationLine, key string) {
		for i, line := range lines {
			style := styles.Muted
			switch line.Tone {
			case "added":
				style = styles.Success
			case "removed", "error":
				style = styles.Error
			case "command", "label":
				style = styles.Text
			case "path":
				style = styles.Secondary
			}
			for _, wrapped := range p.cachedWrap("tool:"+tool.ID+":"+key+":"+fmt.Sprint(i), line.Text, maxInt(1, width-2)) {
				if bodyRows >= bodyLimit {
					bodyClipped = true
					return
				}
				rows = append(rows, renderRow{text: "  " + wrapped, style: style})
				bodyRows++
			}
		}
	}
	appendLines(presentation.Lines, "presentation")
	if tool.Error != "" {
		appendLines([]toolPresentationLine{{Text: tool.Error, Tone: "error"}}, "error")
	}
	if bodyClipped && bodyRows > 0 {
		rows[len(rows)-1] = renderRow{text: "  … output clipped", style: styles.Muted}
	}
	rows = append(rows, renderRow{text: "", style: styles.Text})
	return rows
}

func (p *Page) renderUserRows(key, content string, width int, styles PageStyles) []renderRow {
	if width <= 0 {
		return nil
	}
	lines := p.cachedWrap(key, content, maxInt(1, width-2))
	rows := make([]renderRow, 0, len(lines)+1)
	userStyle := styleWithForeground(styles.Text, styles.Secondary)
	for i, line := range lines {
		prefix := "  "
		row := renderRow{style: userStyle}
		if i == 0 {
			prefix = "> "
			row.prefixWidth = 1
			row.prefixStyle = userStyle.Bold(true)
		}
		row.text = prefix + line
		rows = append(rows, row)
	}
	rows = append(rows, renderRow{text: "", style: userStyle})
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

func composerLayout(text string, cursor, width int) ([]string, int, int) {
	const firstPrefix = "> "
	const continuationPrefix = "  "
	width = maxInt(1, width)
	runes := []rune(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	cursor = maxInt(0, minInt(cursor, len(runes)))
	lines := []string{firstPrefix}
	line := []rune(firstPrefix)
	cursorLine, cursorCol := 0, len(line)

	startContinuation := func() {
		lines = append(lines, continuationPrefix)
		line = []rune(continuationPrefix)
	}
	commitLine := func() {
		lines[len(lines)-1] = string(line)
	}
	for i, r := range runes {
		if r != '\n' && len(line) >= width {
			commitLine()
			startContinuation()
		}
		if i == cursor {
			cursorLine, cursorCol = len(lines)-1, len(line)
		}
		if r == '\n' {
			commitLine()
			startContinuation()
			continue
		}
		line = append(line, r)
	}
	if len(line) >= width {
		commitLine()
		startContinuation()
	}
	if cursor == len(runes) {
		cursorLine, cursorCol = len(lines)-1, len(line)
	}
	commitLine()
	return lines, cursorLine, cursorCol
}

func inputVisibleWindow(totalLines, visibleHeight, cursorLine int) int {
	if visibleHeight <= 0 || totalLines <= visibleHeight {
		return 0
	}
	cursorLine = maxInt(0, minInt(cursorLine, totalLines-1))
	start := cursorLine - visibleHeight + 1
	return maxInt(0, minInt(start, totalLines-visibleHeight))
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
func runeSlice(value string, start, end int) string {
	runes := []rune(value)
	start = maxInt(0, minInt(start, len(runes)))
	end = maxInt(start, minInt(end, len(runes)))
	return string(runes[start:end])
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

func drawSpans(s tcell.Screen, x, y, width int, spans []renderSpan) {
	col := 0
	for _, span := range spans {
		for _, r := range span.text {
			if col >= width {
				return
			}
			s.SetContent(x+col, y, r, nil, span.style)
			col++
		}
	}
}
func drawHLine(s tcell.Screen, x, y, width int, style tcell.Style) {
	for i := 0; i < width; i++ {
		s.SetContent(x+i, y, tcell.RuneHLine, nil, style)
	}
}

func drawBox(s tcell.Screen, x, y, width, height int, style tcell.Style) {
	if width < 2 || height < 2 {
		return
	}
	drawHLine(s, x+1, y, width-2, style)
	drawHLine(s, x+1, y+height-1, width-2, style)
	for row := y + 1; row < y+height-1; row++ {
		s.SetContent(x, row, tcell.RuneVLine, nil, style)
		s.SetContent(x+width-1, row, tcell.RuneVLine, nil, style)
	}
	s.SetContent(x, y, tcell.RuneULCorner, nil, style)
	s.SetContent(x+width-1, y, tcell.RuneURCorner, nil, style)
	s.SetContent(x, y+height-1, tcell.RuneLLCorner, nil, style)
	s.SetContent(x+width-1, y+height-1, tcell.RuneLRCorner, nil, style)
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

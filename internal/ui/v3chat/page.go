package v3chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"

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
	BorderActive   tcell.Style
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
	PageActionOpenCurrentPlan
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

	mu                           sync.Mutex
	input                        []rune
	cursor                       int
	pasteActive                  bool
	pasteBuffer                  []rune
	scroll                       int
	follow                       bool
	status                       string
	errText                      string
	busy                         bool
	rowCache                     map[string]cachedRows
	lastWidth                    int
	lastHeight                   int
	agentModelTarget             footerbar.Rect
	openAgentsRequested          bool
	routeLabel                   string
	profileLabel                 string
	showHeader                   bool
	showThinkingTags             bool
	commandEmission              string
	modelPicker                  bool
	modelLoading                 bool
	planModal                    bool
	planModalScroll              int
	planModalPlan                *client.SessionPlan
	bashOutputModal              bool
	bashOutputModalScroll        int
	bashOutputModalTool          ToolTimelineItem
	modelOptions                 []client.ModelCatalogRecord
	modelIndex                   int
	commandSuggestions           []CommandSuggestion
	commandPaletteIndex          int
	commandPaletteOptionIndex    int
	commandPaletteOptionOwner    string
	pendingCommand               string
	matchKey                     func(*tcell.EventKey, string) bool
	runTimer                     *time.Timer
	permissionIndex              int
	permissionInput              []rune
	permissionBusy               bool
	permissionError              string
	permissionPrefix             string
	permissionPrefixID           string
	permissionPrefixLoading      bool
	permissionContentScroll      int
	permissionContentMaxScroll   int
	permissionContentID          string
	permissionPlanReview         bool
	permissionPlanReviewID       string
	permissionInteractionID      string
	permissionAskQuestion        int
	permissionAskSelections      map[string]int
	permissionAskAnswers         map[string]string
	permissionAskCustomInput     []rune
	permissionAskCustomMode      bool
	permissionWorkspaceSelection int
	permissionApproveTarget      footerbar.Rect
	permissionDenyTarget         footerbar.Rect
	permissionAlwaysTarget       footerbar.Rect
	permissionAlwaysDenyTarget   footerbar.Rect
	permissionAskSelectTarget    footerbar.Rect
	permissionAskSubmitTarget    footerbar.Rect
	permissionWorkspaceTarget    footerbar.Rect
	permissionWorkspaceAddTarget footerbar.Rect
	handoffFocus                 bool
	handoffMessageID             string
	handoffControl               int
	handoffTargets               map[string]footerbar.Rect
	handoffDetailsModal          bool
	handoffDetailsScroll         int
	handoffDetailsMessageID      string
	handoffDetails               *client.PlanFinalHandoff
}

const (
	composerPasteFlushChunkRunes = 256

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
	return &Page{runtime: runtime, styles: styles, showHeader: true, showThinkingTags: true, follow: true, rowCache: make(map[string]cachedRows), handoffTargets: make(map[string]footerbar.Rect), matchKey: defaultKeyMatcher}
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

// SetProfileLabel is retained for session hydration compatibility; footer
// presentation no longer exposes model-profile semantics.
func (p *Page) SetProfileLabel(label string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.profileLabel = strings.TrimSpace(label)
	p.mu.Unlock()
}

func (p *Page) ApplyModelProfile(policy client.SessionV3AgentModelPolicy) {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return
	}
	p.runtime.Store().Dispatch(ModelProfileAction{Policy: policy})
	p.SetProfileLabel(policy.ProfileName)
}

func (p *Page) SetHeaderVisible(show bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.showHeader = show
	p.mu.Unlock()
}

func (p *Page) SetThinkingTagsVisible(show bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.showThinkingTags = show
	p.rowCache = make(map[string]cachedRows)
	p.mu.Unlock()
}

func (p *Page) SetCommandEmission(emission string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.commandEmission = strings.TrimSpace(emission)
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

func (p *Page) Status() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errText != "" {
		return p.errText
	}
	return p.status
}

// SetInput replaces the local composer value without creating or mutating a
// durable session. App command dispatch uses this when opening a bare primer.
func (p *Page) SetInput(value string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.input = []rune(value)
	if len(p.input) > maxComposerRunes {
		p.input = p.input[:maxComposerRunes]
	}
	p.cursor = len(p.input)
	p.pasteBuffer = nil
	p.syncCommandPaletteSelectionLocked()
	p.resetCommandPaletteOptionSelectionLocked()
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

func (p *Page) SessionID() string {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return ""
	}
	return strings.TrimSpace(p.runtime.Store().Snapshot().Session.ID)
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

func (p *Page) ConsumeOpenAgentsRequest() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	requested := p.openAgentsRequested
	p.openAgentsRequested = false
	return requested
}

func (p *Page) permissionVisibleLocked() bool {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return false
	}
	return len(SelectPendingPermissions(p.runtime.Store().Snapshot())) > 0
}

func (p *Page) PendingPermissionVisible() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.permissionVisibleLocked()
}

func (p *Page) ClearInput() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.input = nil
	p.cursor = 0
	p.pasteBuffer = nil
	p.commandPaletteIndex = 0
	p.resetCommandPaletteOptionSelectionLocked()
	p.mu.Unlock()
}

// ConsumeQuitScrollbackJump returns the transcript to its live bottom position
// before the app treats a subsequent quit key as an exit request.
func (p *Page) ConsumeQuitScrollbackJump() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handoffDetailsModal || p.planModal || p.bashOutputModal || p.permissionVisibleLocked() || p.modelPicker || p.pasteActive {
		return false
	}
	if p.scroll <= 0 && p.follow {
		return false
	}
	p.scroll = 0
	p.follow = true
	return true
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
	p.syncCommandPaletteSelectionLocked()
	p.resetCommandPaletteOptionSelectionLocked()
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
	p.clearCommandEmissionForConversationLocked()
}

func (p *Page) clearCommandEmissionForConversationLocked() {
	input := strings.TrimSpace(string(p.input))
	if input != "" && !strings.HasPrefix(input, "/") {
		p.commandEmission = ""
	}
}

// HandlePasteKey buffers bracketed-paste key events and reports whether a
// complete chunk was inserted. Callers can defer redraws until this returns
// true (or SetPasteActive(false) flushes the final partial chunk), matching the
// legacy chat composer's large-paste behavior.
func (p *Page) HandlePasteKey(ev *tcell.EventKey) bool {
	if p == nil || ev == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.pasteActive {
		return false
	}
	return p.handlePasteKeyLocked(ev)
}

func (p *Page) handlePasteKeyLocked(ev *tcell.EventKey) bool {
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
	if len(p.pasteBuffer) >= composerPasteFlushChunkRunes {
		p.flushPasteBufferLocked()
		return true
	}
	return false
}

// PrimeRoutedDraft opens the local Router primer. It deliberately leaves
// workspace, branch, title, mode, and model authority unset until Router
// returns a canonical response.
func (p *Page) PrimeRoutedDraft(draft RoutedDraft) error {
	if p == nil || p.runtime == nil {
		return fmt.Errorf("v3 chat runtime is not configured")
	}
	if err := p.runtime.PrimeRoutedDraft(draft); err != nil {
		return err
	}
	p.SetInput(draft.Prompt)
	p.SetStatus("Waiting...")
	return nil
}

// OpenRoutedNew applies one parsed /new command. Bare commands leave an
// editable primer; prompt forms begin routing immediately.
func (p *Page) OpenRoutedNew(command NewCommand, agentName string, metadata map[string]any) error {
	if err := p.PrimeRoutedDraft(RoutedDraft{
		Prompt:                   command.Prompt,
		PlanModeRequested:        command.PlanModeRequested,
		ManagedWorktreeRequested: command.ManagedWorktreeRequested,
		AgentName:                strings.TrimSpace(agentName),
		Metadata:                 cloneAnyMap(metadata),
	}); err != nil {
		return err
	}
	if strings.TrimSpace(command.Prompt) != "" {
		p.ClearInput()
		p.StartRoutedDraft()
	}
	return nil
}

// StartRoutedDraft submits the primed local intent asynchronously.
func (p *Page) StartRoutedDraft() {
	if p == nil || p.runtime == nil {
		return
	}
	p.setBusy("Routing...", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := p.runtime.StartRoutedDraft(ctx)
		p.finishAsync("", err)
	}()
}

// RetryRoutedDraft retries the same failed operation identity.
func (p *Page) RetryRoutedDraft() {
	if p == nil || p.runtime == nil {
		return
	}
	p.setBusy("Routing...", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := p.runtime.RetryRoutedDraft(ctx)
		p.finishAsync("", err)
	}()
}

// ApplyWorktreeCommand changes only the ready local primer flag.
func (p *Page) ApplyWorktreeCommand(input string) (bool, error) {
	command, matched, err := ParseWorktreeCommand(input)
	if !matched || err != nil {
		return matched, err
	}
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return true, fmt.Errorf("v3 chat runtime is not configured")
	}
	state := p.runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(state)
	if !ok {
		return true, fmt.Errorf("worktree priming is available only for a new session draft")
	}
	if err := p.runtime.UpdateRoutedDraftIntent(draft.Prompt, draft.PlanModeRequested, command.Enabled); err != nil {
		return true, err
	}
	p.SetStatus("Worktree: " + map[bool]string{true: "on", false: "off"}[command.Enabled])
	return true, nil
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
		ok := ""
		if err == nil && strings.TrimSpace(request.InitialPrompt) == "" {
			ok = "new session ready"
		}
		p.finishAsync(ok, err)
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
		p.finishAsync("", err)
	}()
}

func (p *Page) Compact() {
	if p == nil || p.runtime == nil {
		return
	}
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if busy {
		p.SetStatus("/compact ignored (run already active)")
		return
	}
	p.setBusy("compacting context…", true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := p.runtime.Compact(ctx)
		p.finishAsync("context compacted", err)
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
	if p.handoffDetailsModal {
		return p.handleFinalHandoffDetailsKeyLocked(ev)
	}
	if p.planModal {
		return p.handlePlanModalKeyLocked(ev)
	}
	if p.bashOutputModal {
		return p.handleBashOutputModalKeyLocked(ev)
	}
	if p.permissionVisibleLocked() {
		p.ensurePermissionPrefixLocked()
		return p.handlePermissionKeyLocked(ev)
	}
	if p.modelPicker {
		return p.handleModelPickerKeyLocked(ev)
	}
	if p.pasteActive {
		p.handlePasteKeyLocked(ev)
		return PageActionNone
	}
	if p.handleFinalHandoffKeyLocked(ev) {
		return PageActionNone
	}
	match := func(action string) bool { return p.matchKey != nil && p.matchKey(ev, action) }
	if len(p.input) == 0 && ev.Key() == tcell.KeyRune && ev.Rune() >= '1' && ev.Rune() <= '3' {
		if message, ok := p.latestFinalHandoffLocked(); ok {
			index := int(ev.Rune() - '1')
			action := finalHandoffPromptAction(message.ID, index)
			if message.FinalHandoff != nil && index < len(message.FinalHandoff.SuggestedPrompts) && p.handoffTargets[action].W > 0 {
				p.handoffMessageID, p.handoffControl, p.handoffFocus = message.ID, index, true
				p.activateFinalHandoffControlLocked(message, index)
				return PageActionNone
			}
		}
	}
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
		if len(p.input) == 0 && p.focusLatestFinalHandoffLocked() {
			break
		}
		p.completeCommandFromPaletteLocked()
	case match(KeySubmit):
		if p.executeCommandPaletteSelectionLocked() {
			p.status = ""
			return PageActionCommand
		}
		text := strings.TrimSpace(string(p.input))
		if strings.HasPrefix(text, "/") {
			p.pendingCommand = text
			p.input = nil
			p.cursor = 0
			p.status = ""
			return PageActionCommand
		}
		if text != "" && !p.busy {
			if p.runtime != nil && p.runtime.Store() != nil {
				state := p.runtime.Store().Snapshot()
				if draft, ok := SelectRoutedDraft(state); ok && strings.TrimSpace(state.Session.ID) == "" && draft.Status == RoutedDraftReady {
					if err := p.runtime.UpdateRoutedDraftIntent(text, draft.PlanModeRequested, draft.ManagedWorktreeRequested); err != nil {
						p.errText = err.Error()
						break
					}
					p.input = nil
					p.cursor = 0
					p.follow = true
					p.scroll = 0
					go p.StartRoutedDraft()
					break
				}
			}
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
			p.syncCommandPaletteSelectionLocked()
			p.resetCommandPaletteOptionSelectionLocked()
			p.clearCommandEmissionForConversationLocked()
		}
	case ev.Key() == tcell.KeyDelete:
		if p.cursor < len(p.input) {
			p.input = append(p.input[:p.cursor], p.input[p.cursor+1:]...)
			p.syncCommandPaletteSelectionLocked()
			p.resetCommandPaletteOptionSelectionLocked()
			p.clearCommandEmissionForConversationLocked()
		}
	case match(KeyMoveLeft):
		if p.commandPaletteActiveLocked() {
			p.moveCommandPaletteOptionSelectionLocked(-1)
		} else if p.cursor > 0 {
			p.cursor--
		}
	case match(KeyMoveRight):
		if p.commandPaletteActiveLocked() {
			p.moveCommandPaletteOptionSelectionLocked(1)
		} else if p.cursor < len(p.input) {
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
		p.resetCommandPaletteOptionSelectionLocked()
	case ev.Key() == tcell.KeyF2:
		p.openModelPickerLocked()
	case ev.Key() == tcell.KeyCtrlR:
		// Recovery scope is already retained by the runtime's hydrated session.
		go p.Recover("", "")
	case ev.Key() == tcell.KeyCtrlP:
		return PageActionOpenCurrentPlan
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
		p.resetCommandPaletteOptionSelectionLocked()
	}
	return PageActionNone
}

// OpenCurrentPlanModal opens the plan returned by the current-plan API.
func (p *Page) OpenCurrentPlanModal(plan client.SessionPlan) bool {
	if p == nil || plan.Document == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planModal = true
	p.planModalScroll = 0
	p.planModalPlan = &plan
	return true
}

func (p *Page) PlanModalVisible() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.planModal
}

func (p *Page) ClosePlanModal() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planModal = false
	p.planModalScroll = 0
	p.planModalPlan = nil
}

// ToggleLatestBashOutput opens the complete output from the latest Bash tool in
// a bounded, scrollable modal. Calling it again while open closes the modal.
func (p *Page) ToggleLatestBashOutput() bool {
	if p == nil || p.runtime == nil || p.runtime.Store() == nil {
		return false
	}
	p.mu.Lock()
	if p.bashOutputModal {
		p.bashOutputModal = false
		p.bashOutputModalScroll = 0
		p.bashOutputModalTool = ToolTimelineItem{}
		p.status = "bash output closed"
		p.mu.Unlock()
		return true
	}
	p.mu.Unlock()

	tool, ok := latestBashTool(p.runtime.Store().Snapshot())
	if !ok || strings.TrimSpace(bashToolOutputText(tool)) == "" {
		return false
	}
	p.mu.Lock()
	p.bashOutputModal = true
	p.bashOutputModalScroll = 0
	p.bashOutputModalTool = tool
	p.status = "full bash output"
	p.mu.Unlock()
	return true
}

func latestBashTool(state State) (ToolTimelineItem, bool) {
	var latest ToolTimelineItem
	found := false
	consider := func(tool ToolTimelineItem) {
		if normalizeToolDisplayName(tool.Name) != "bash" || strings.TrimSpace(bashToolOutputText(tool)) == "" {
			return
		}
		if !found || tool.GlobalSeq > latest.GlobalSeq || (tool.GlobalSeq == latest.GlobalSeq && tool.CreatedAt >= latest.CreatedAt) {
			latest, found = tool, true
		}
	}
	for _, message := range SelectMessages(state) {
		if tool, ok := parseToolMessage(message); ok {
			consider(tool)
		}
	}
	for _, tool := range SelectLiveTools(state) {
		consider(tool)
	}
	return latest, found
}

func bashToolOutputText(tool ToolTimelineItem) string {
	output := parseToolObject(tool.Output)
	if output != nil {
		if raw := firstNonEmptyToolRaw(toolStringRaw(output, "output"), toolStringRaw(output, "stdout"), toolStringRaw(output, "stderr")); raw != "" {
			return raw
		}
		if looksLikeTerminalBashPayload(output) {
			return ""
		}
	}
	return tool.Output
}

func (p *Page) handleBashOutputModalKeyLocked(ev *tcell.EventKey) PageAction {
	if ev == nil {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.bashOutputModal = false
		p.bashOutputModalScroll = 0
		p.bashOutputModalTool = ToolTimelineItem{}
		p.status = "bash output closed"
	case tcell.KeyUp:
		p.bashOutputModalScroll = maxInt(0, p.bashOutputModalScroll-1)
	case tcell.KeyDown:
		p.bashOutputModalScroll++
	case tcell.KeyPgUp:
		p.bashOutputModalScroll = maxInt(0, p.bashOutputModalScroll-8)
	case tcell.KeyPgDn:
		p.bashOutputModalScroll += 8
	case tcell.KeyHome:
		p.bashOutputModalScroll = 0
	}
	return PageActionNone
}

func (p *Page) handlePlanModalKeyLocked(ev *tcell.EventKey) PageAction {
	if ev == nil {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.planModal = false
		p.planModalScroll = 0
		p.planModalPlan = nil
	case tcell.KeyUp:
		p.planModalScroll = maxInt(0, p.planModalScroll-1)
	case tcell.KeyDown:
		p.planModalScroll++
	case tcell.KeyPgUp:
		p.planModalScroll = maxInt(0, p.planModalScroll-8)
	case tcell.KeyPgDn:
		p.planModalScroll += 8
	case tcell.KeyHome:
		p.planModalScroll = 0
	}
	return PageActionNone
}

func (p *Page) cycleModeLocked() {
	if p.runtime == nil || p.busy || p.runtime.Store() == nil {
		return
	}
	state := p.runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(state)
	if !ok || strings.TrimSpace(state.Session.ID) != "" || draft.Status != RoutedDraftReady {
		p.errText = "Plan toggle is available only for a new session draft"
		p.status = ""
		return
	}
	next := !draft.PlanModeRequested
	if err := p.runtime.UpdateRoutedDraftIntent(draft.Prompt, next, draft.ManagedWorktreeRequested); err != nil {
		p.errText = err.Error()
		p.status = ""
		return
	}
	p.errText = ""
	p.status = "Plan: " + map[bool]string{true: "on", false: "off"}[next]
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

func (p *Page) ensurePermissionPrefixLocked() {
	permissions := SelectPendingPermissions(p.runtime.Store().Snapshot())
	if len(permissions) == 0 {
		p.permissionPrefix, p.permissionPrefixID, p.permissionPrefixLoading = "", "", false
		p.permissionContentScroll, p.permissionContentMaxScroll, p.permissionContentID = 0, 0, ""
		p.permissionPlanReview, p.permissionPlanReviewID = false, ""
		return
	}
	p.permissionIndex = maxInt(0, minInt(p.permissionIndex, len(permissions)-1))
	permission := permissions[p.permissionIndex]
	p.syncPermissionInteractionLocked(permission)
	p.syncPermissionContentLocked(permission)
	if intent, ok := parsePlanPermissionIntent(permission); ok && p.permissionPlanReviewID != permission.ID {
		p.permissionPlanReviewID = permission.ID
		p.permissionPlanReview = !intent.ContinueAutomatically || strings.EqualFold(intent.ContinuationPolicy, "review_each_checkpoint")
	} else if !ok {
		p.permissionPlanReview, p.permissionPlanReviewID = false, ""
	}
	if normalizePermissionToolName(permission.ToolName) != "bash" || p.permissionPrefixID == permission.ID || p.permissionPrefixLoading {
		return
	}
	resolver, ok := p.runtime.transport.(permissionTransport)
	if !ok {
		return
	}
	p.permissionPrefixID = permission.ID
	p.permissionPrefixLoading = true
	go func(record client.PermissionRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		explain, err := resolver.ExplainPermission(ctx, record.Mode, record.ToolName, record.ToolArguments)
		p.mu.Lock()
		if p.permissionPrefixID == record.ID {
			p.permissionPrefixLoading = false
			if err == nil {
				p.permissionPrefix = bashPermissionPreviewPrefix(explain.RulePreview)
			}
		}
		p.mu.Unlock()
		p.runtime.signalWake()
	}(permission)
}

func (p *Page) syncPermissionContentLocked(permission client.PermissionRecord) {
	if p.permissionContentID == permission.ID {
		p.permissionContentScroll = minInt(maxInt(0, p.permissionContentScroll), p.permissionContentMaxScroll)
		return
	}
	p.permissionContentID = permission.ID
	p.permissionContentScroll = 0
	p.permissionContentMaxScroll = 0
}

func (p *Page) handlePermissionKeyLocked(ev *tcell.EventKey) PageAction {
	permissions := SelectPendingPermissions(p.runtime.Store().Snapshot())
	if len(permissions) == 0 {
		return PageActionNone
	}
	p.permissionIndex = maxInt(0, minInt(p.permissionIndex, len(permissions)-1))
	permission := permissions[p.permissionIndex]
	p.syncPermissionInteractionLocked(permission)
	p.syncPermissionContentLocked(permission)
	if isAskUserPermission(permission) {
		return p.handleAskUserPermissionKeyLocked(permission, ev)
	}
	if isWorkspaceScopePermission(permission) {
		return p.handleWorkspaceScopePermissionKeyLocked(permission, ev)
	}
	planIntent, planPermission := parsePlanPermissionIntent(permission)
	if planPermission && p.permissionPlanReviewID != permissions[p.permissionIndex].ID {
		p.permissionPlanReviewID = permissions[p.permissionIndex].ID
		p.permissionPlanReview = !planIntent.ContinueAutomatically || strings.EqualFold(planIntent.ContinuationPolicy, "review_each_checkpoint")
	}
	_, manageSessionsPermission := parseManageSessionsPermissionIntent(permissions[p.permissionIndex])
	hidePermissionNote := planPermission || manageSessionsPermission
	if p.permissionBusy {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyUp:
		if p.permissionIndex > 0 {
			p.permissionIndex--
			p.permissionPrefixID = ""
			p.ensurePermissionPrefixLocked()
		}
	case tcell.KeyDown:
		if p.permissionIndex+1 < len(permissions) {
			p.permissionIndex++
			p.permissionPrefixID = ""
			p.ensurePermissionPrefixLocked()
		}
	case tcell.KeyPgUp:
		if isBashPermissionRequest(permission) && p.permissionContentMaxScroll > 0 {
			p.permissionContentScroll = maxInt(0, p.permissionContentScroll-6)
		} else {
			p.scroll += 8
			p.follow = false
		}
	case tcell.KeyPgDn:
		if isBashPermissionRequest(permission) && p.permissionContentMaxScroll > 0 {
			p.permissionContentScroll = minInt(p.permissionContentMaxScroll, p.permissionContentScroll+6)
		} else {
			p.scroll = maxInt(0, p.scroll-8)
			p.follow = p.scroll == 0
		}
	case tcell.KeyHome:
		if isBashPermissionRequest(permission) && p.permissionContentMaxScroll > 0 {
			p.permissionContentScroll = 0
		} else {
			p.scroll = 1 << 30
			p.follow = false
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if hidePermissionNote {
			if planPermission && p.cursor > 0 {
				p.input = append(p.input[:p.cursor-1], p.input[p.cursor:]...)
				p.cursor--
			}
		} else if len(p.permissionInput) > 0 {
			p.permissionInput = p.permissionInput[:len(p.permissionInput)-1]
		}
	case tcell.KeyCtrlU:
		if hidePermissionNote {
			if planPermission {
				p.input = nil
				p.cursor = 0
			}
		} else {
			p.permissionInput = nil
		}
	case tcell.KeyCtrlA:
		if !planPermission || normalizePermissionToolName(permissions[p.permissionIndex].ToolName) != "exit_plan_mode" {
			if !manageSessionsPermission {
				p.resolvePermissionLocked(permissions[p.permissionIndex], "allow_always")
			}
		}
	case tcell.KeyCtrlD:
		if !planPermission || normalizePermissionToolName(permissions[p.permissionIndex].ToolName) != "exit_plan_mode" {
			if !manageSessionsPermission {
				p.resolvePermissionLocked(permissions[p.permissionIndex], "deny_always")
			}
		}
	case tcell.KeyEscape:
		p.resolvePermissionLocked(permissions[p.permissionIndex], "deny_once")
	case tcell.KeyEnter:
		if planPermission {
			command := strings.ToLower(strings.TrimSpace(string(p.input)))
			if command == "/plan" || command == "/plan show" {
				p.input = nil
				p.cursor = 0
				return PageActionOpenCurrentPlan
			}
		}
		p.resolvePermissionLocked(permissions[p.permissionIndex], "allow_once")
	case tcell.KeyCtrlP:
		return PageActionOpenCurrentPlan
	case tcell.KeyRune:
		if !utf8.ValidRune(ev.Rune()) || ev.Rune() < ' ' {
			break
		}
		if planPermission && normalizePermissionToolName(permissions[p.permissionIndex].ToolName) == "exit_plan_mode" && (ev.Rune() == 'm' || ev.Rune() == 'M') && len(p.input) == 0 {
			p.permissionPlanReview = !p.permissionPlanReview
			break
		}
		if hidePermissionNote {
			if planPermission && len(p.input) < maxComposerRunes {
				p.insertRunesLocked([]rune{ev.Rune()})
			}
		} else if len(p.permissionInput) < maxComposerRunes {
			p.permissionInput = append(p.permissionInput, ev.Rune())
		}
	}
	return PageActionNone
}

func (p *Page) resolvePermissionLocked(permission client.PermissionRecord, action string) {
	p.resolvePermissionWithReasonLocked(permission, action, strings.TrimSpace(string(p.permissionInput)))
}

func (p *Page) resolvePermissionWithReasonLocked(permission client.PermissionRecord, action, reason string) {
	p.permissionBusy = true
	p.permissionError = ""
	manualReview := p.permissionPlanReview
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		approvedArguments := ""
		if strings.HasPrefix(action, "allow") {
			if _, ok := parsePlanPermissionIntent(permission); ok && normalizePermissionToolName(permission.ToolName) == "exit_plan_mode" {
				approvedArguments = planPermissionApprovedArguments(permission, manualReview)
			} else if _, ok := parseManageSessionsPermissionIntent(permission); ok {
				approvedArguments = strings.TrimSpace(permission.ApprovedArguments)
				if approvedArguments == "" {
					if payload := parseToolObject(permission.ToolArguments); payload != nil {
						if approved := toolObject(payload, "approved_arguments"); approved != nil {
							if encoded, marshalErr := json.Marshal(approved); marshalErr == nil {
								approvedArguments = string(encoded)
							}
						}
					}
				}
			}
		}
		_, err := p.runtime.ResolvePermissionWithArguments(ctx, permission.ID, action, reason, approvedArguments)
		p.mu.Lock()
		p.permissionBusy = false
		if err != nil {
			p.permissionError = err.Error()
		} else {
			p.permissionInput = nil
			p.resetPermissionInteractionLocked()
			permissions := SelectPendingPermissions(p.runtime.Store().Snapshot())
			if p.permissionIndex >= len(permissions) {
				p.permissionIndex = maxInt(0, len(permissions)-1)
			}
		}
		p.mu.Unlock()
		p.runtime.signalWake()
	}()
}

func (p *Page) HandleMouse(ev *tcell.EventMouse) {
	if p == nil || ev == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	x, y := ev.Position()
	buttons := ev.Buttons()
	if p.handoffDetailsModal {
		if buttons&tcell.WheelUp != 0 {
			p.handoffDetailsScroll = maxInt(0, p.handoffDetailsScroll-3)
		}
		if buttons&tcell.WheelDown != 0 {
			p.handoffDetailsScroll += 3
		}
		return
	}
	if p.permissionVisibleLocked() {
		permissions := SelectPendingPermissions(p.runtime.Store().Snapshot())
		if len(permissions) == 0 || p.permissionBusy {
			return
		}
		p.permissionIndex = maxInt(0, minInt(p.permissionIndex, len(permissions)-1))
		if buttons&tcell.Button1 != 0 {
			permission := permissions[p.permissionIndex]
			switch {
			case containsFooterPoint(p.permissionAskSelectTarget, x, y) && isAskUserPermission(permission):
				p.handleAskUserPermissionKeyLocked(permission, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			case containsFooterPoint(p.permissionAskSubmitTarget, x, y) && isAskUserPermission(permission):
				p.handleAskUserPermissionKeyLocked(permission, tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
			case containsFooterPoint(p.permissionWorkspaceTarget, x, y) && isWorkspaceScopePermission(permission):
				intent, _ := parseWorkspaceScopeIntent(permission)
				p.resolvePermissionWithReasonLocked(permission, "allow_once", workspaceScopeResolutionReason(intent.SessionAllow.Decision))
			case containsFooterPoint(p.permissionWorkspaceAddTarget, x, y) && isWorkspaceScopePermission(permission):
				intent, _ := parseWorkspaceScopeIntent(permission)
				if intent.AddToWorkspace.Available {
					p.resolvePermissionWithReasonLocked(permission, "allow_once", workspaceScopeResolutionReason(intent.AddToWorkspace.Decision))
				}
			case containsFooterPoint(p.permissionApproveTarget, x, y):
				p.resolvePermissionLocked(permissions[p.permissionIndex], "allow_once")
			case containsFooterPoint(p.permissionDenyTarget, x, y):
				p.resolvePermissionLocked(permissions[p.permissionIndex], "deny_once")
			case containsFooterPoint(p.permissionAlwaysTarget, x, y):
				p.resolvePermissionLocked(permissions[p.permissionIndex], "allow_always")
			case containsFooterPoint(p.permissionAlwaysDenyTarget, x, y):
				p.resolvePermissionLocked(permissions[p.permissionIndex], "deny_always")
			}
		}
		permission := permissions[p.permissionIndex]
		p.syncPermissionContentLocked(permission)
		if buttons&tcell.WheelUp != 0 {
			if isBashPermissionRequest(permission) && p.permissionContentMaxScroll > 0 {
				p.permissionContentScroll = maxInt(0, p.permissionContentScroll-3)
			} else {
				p.scroll += 3
				p.follow = false
			}
		}
		if buttons&tcell.WheelDown != 0 {
			if isBashPermissionRequest(permission) && p.permissionContentMaxScroll > 0 {
				p.permissionContentScroll = minInt(p.permissionContentMaxScroll, p.permissionContentScroll+3)
			} else {
				p.scroll = maxInt(0, p.scroll-3)
				p.follow = p.scroll == 0
			}
		}
		return
	}
	if buttons&tcell.Button1 != 0 {
		if action := finalHandoffTargetAt(p.handoffTargets, x, y); action != "" && p.activateFinalHandoffTargetLocked(action) {
			return
		}
		if containsFooterPoint(p.agentModelTarget, x, y) {
			p.openAgentsRequested = true
			return
		}
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
	text           string
	style          tcell.Style
	keepBackground bool
}

type renderActionTarget struct {
	x      int
	width  int
	action string
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
	actions        []renderActionTarget
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
	routeLabel := p.routeLabel
	showHeader, commandEmission := p.showHeader, p.commandEmission
	p.lastWidth, p.lastHeight = width, height
	modelPicker, modelLoading, modelIndex := p.modelPicker, p.modelLoading, p.modelIndex
	planModal, planModalScroll, planModalPlan := p.planModal, p.planModalScroll, p.planModalPlan
	bashOutputModal, bashOutputModalScroll, bashOutputModalTool := p.bashOutputModal, p.bashOutputModalScroll, p.bashOutputModalTool
	handoffDetailsModal, handoffDetailsScroll := p.handoffDetailsModal, p.handoffDetailsScroll
	var handoffDetails *client.PlanFinalHandoff
	if p.handoffDetails != nil {
		copy := cloneFinalHandoff(p.handoffDetails)
		handoffDetails = &copy
	}
	modelOptions := append([]client.ModelCatalogRecord(nil), p.modelOptions...)
	commandSuggestions := append([]CommandSuggestion(nil), p.commandSuggestions...)
	p.ensurePermissionPrefixLocked()
	commandPaletteIndex := p.commandPaletteIndex
	commandPaletteOptionIndex := p.commandPaletteOptionIndex
	commandPaletteOptionOwner := p.commandPaletteOptionOwner
	p.mu.Unlock()

	fill(screen, 0, 0, width, height, styles.Background)
	state := NewState()
	if p.runtime != nil && p.runtime.Store() != nil {
		state = p.runtime.Store().Snapshot()
	}
	title := strings.TrimSpace(SelectTitle(state))
	if title == "" {
		if draft, ok := SelectRoutedDraft(state); ok && draft.Status != RoutedDraftResolved {
			title = "Waiting..."
		} else {
			title = "New V3 session"
		}
	}
	workspaceName := strings.TrimSpace(state.Session.WorkspaceName)
	_, stale, reason := SelectReconnect(state)
	transcriptTop := 0
	if showHeader {
		transcriptTop = drawCanonicalHeader(screen, width, height, styles, title, workspaceName, SelectPlanHeader(state))
	}
	runStatus, hasRunStatus := BuildRunStatus(state, now)
	statusLine := ""
	statusStyle := styles.Muted
	if stale {
		statusLine = "stale • Ctrl-R to rehydrate • " + reason
		statusStyle = styles.Warning
	} else if draft, ok := SelectRoutedDraft(state); ok && draft.Status != RoutedDraftResolved {
		statusLine = routedDraftStatusLine(draft)
		if draft.Status == RoutedDraftFailed {
			statusStyle = styles.Error
		}
	} else {
		switch composerNoticeKind(status) {
		case "stop":
			statusLine = status
		case "warning":
			statusLine = status
			statusStyle = styles.Warning
		case "notice":
			statusLine = status
		}
	}
	// Keep the activity indicator moving without turning the page into a
	// heartbeat: each draw schedules exactly one wake for the next second.
	p.scheduleRunTimer(runStatus.Active)

	// Keep the normal footer to its separator and token row, but reserve extra
	// token rows when the canonical footer stacks on ultra-thin terminals.
	footerHeight := footerbar.ResponsiveHeight(width, 2)
	composerLines, composerCursorLine, composerCursorCol := composerLayout(string(input), cursor, width)
	composerVisibleRows := minInt(len(composerLines), maxComposerVisibleRows)
	conversationStatusHeight := 0
	if hasRunStatus {
		conversationStatusHeight = 1
	}
	composerVisibleRows = minInt(composerVisibleRows, maxInt(1, height-footerHeight-conversationStatusHeight-3))
	composerStart := inputVisibleWindow(len(composerLines), composerVisibleRows, composerCursorLine)
	composerHeight := 1 + composerVisibleRows + footerHeight
	transcriptHeight := height - transcriptTop - composerHeight - conversationStatusHeight
	if transcriptHeight < 1 {
		transcriptHeight = 1
	}
	rows := p.renderRowsForHeight(state, maxInt(1, width-4), transcriptHeight, styles)
	start := len(rows) - transcriptHeight - scroll
	if start < 0 {
		start = 0
	}
	end := minInt(len(rows), start+transcriptHeight)
	actionTargets := map[string]footerbar.Rect{}
	for i := start; i < end; i++ {
		row := rows[i]
		y := transcriptTop + i - start
		if len(row.spans) > 0 {
			drawSpans(screen, 2, y, width-4, row.spans)
		} else {
			drawText(screen, 2, y, width-4, row.style, row.text)
		}
		for _, target := range row.actions {
			actionTargets[target.action] = footerbar.Rect{X: 2 + target.x, Y: y, W: target.width, H: 1}
		}
		if row.prefixWidth > 0 {
			drawText(screen, 2, y, minInt(width-4, row.prefixWidth), row.prefixStyle, row.text)
		}
		if row.highlightWidth > 0 && row.highlightStart < width-4 {
			highlight := runeSlice(row.text, row.highlightStart, row.highlightStart+row.highlightWidth)
			drawText(screen, 2+row.highlightStart, y, minInt(width-4-row.highlightStart, row.highlightWidth), row.highlightStyle, highlight)
		}
	}
	p.mu.Lock()
	p.permissionApproveTarget = actionTargets["allow_once"]
	p.permissionDenyTarget = actionTargets["deny_once"]
	p.permissionAlwaysTarget = actionTargets["allow_always"]
	p.permissionAlwaysDenyTarget = actionTargets["deny_always"]
	p.permissionAskSelectTarget = actionTargets["ask_select"]
	p.permissionAskSubmitTarget = actionTargets["ask_submit"]
	p.permissionWorkspaceTarget = actionTargets["workspace_session"]
	p.permissionWorkspaceAddTarget = actionTargets["workspace_add"]
	p.handoffTargets = make(map[string]footerbar.Rect)
	for action, target := range actionTargets {
		if strings.HasPrefix(action, "handoff:") {
			p.handoffTargets[action] = target
		}
	}
	p.mu.Unlock()

	composerY := height - composerHeight
	if composerY < 2 {
		composerY = 2
	}
	if hasRunStatus {
		drawConversationStatus(screen, width, composerY-1, styles, runStatus)
	}
	drawHLine(screen, 0, composerY, width, styles.Border)
	if strings.TrimSpace(errText) != "" {
		drawComposerError(screen, width, composerY, styles, errText)
	} else if strings.TrimSpace(statusLine) != "" {
		drawComposerStatus(screen, width, composerY, statusStyle, statusLine)
	} else {
		drawCommandEmission(screen, width, composerY, styles, commandEmission)
	}
	modelState := SelectModel(state)
	footerY := height - footerHeight
	p.drawCanonicalFooter(screen, footerbar.Rect{X: 0, Y: footerY, W: width, H: footerHeight}, state, routeLabel)
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
	if handoffDetailsModal {
		p.drawFinalHandoffDetailsModal(screen, width, height, styles, handoffDetails, handoffDetailsScroll)
	} else if planModal {
		plan := state.Plan.ActivePlan
		if planModalPlan != nil {
			plan = planModalPlan
		}
		p.drawPlanModal(screen, width, height, styles, plan, planModalScroll)
	} else if bashOutputModal {
		p.drawBashOutputModal(screen, width, height, styles, bashOutputModalTool, bashOutputModalScroll)
	} else if modelPicker {
		p.drawModelPicker(screen, width, height, styles, modelOptions, modelIndex, modelLoading, modelState)
	} else {
		p.drawCommandPalette(screen, width, transcriptTop, composerY, styles, string(input), commandPaletteIndex, commandPaletteOptionOwner, commandPaletteOptionIndex, commandSuggestions)
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

func drawCommandEmission(screen tcell.Screen, width, y int, styles PageStyles, emission string) {
	emission = strings.TrimSpace(emission)
	if emission == "" || width < 12 {
		return
	}
	maxWidth := maxInt(1, width/2)
	label := " " + truncateRunes(emission, maxWidth-2) + " "
	labelRunes := []rune(label)
	drawText(screen, maxInt(0, width-len(labelRunes)-1), y, len(labelRunes), styles.Secondary, label)
}

func composerNoticeKind(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		return ""
	}
	for _, marker := range []string{"stop", "cancel"} {
		if strings.Contains(normalized, marker) {
			return "stop"
		}
	}
	for _, marker := range []string{"warning", "warn:", "error", "failed", "failure", "unavailable", "required", "missing", "denied", "blocked", "stale"} {
		if strings.Contains(normalized, marker) {
			return "warning"
		}
	}
	if strings.HasPrefix(normalized, "thinking tags ") {
		return "notice"
	}
	return ""
}

func drawComposerStatus(screen tcell.Screen, width, y int, style tcell.Style, status string) {
	status = strings.TrimSpace(status)
	if status == "" || width < 12 {
		return
	}
	maxWidth := minInt(width-2, maxInt(18, width/2))
	label := " " + truncateRunes(status, maxWidth-2) + " "
	labelWidth := utf8.RuneCountInString(label)
	drawText(screen, width-labelWidth, y, labelWidth, styleWithForeground(tcell.StyleDefault, style).Bold(true), label)
}

func drawComposerError(screen tcell.Screen, width, y int, styles PageStyles, errText string) {
	errText = strings.TrimSpace(errText)
	if errText == "" || width < 14 {
		return
	}
	maxWidth := minInt(width-2, maxInt(18, width*2/3))
	prefix, suffix := " error • ", " "
	messageWidth := maxInt(1, maxWidth-utf8.RuneCountInString(prefix)-utf8.RuneCountInString(suffix))
	label := prefix + truncateRunes(errText, messageWidth) + suffix
	labelWidth := utf8.RuneCountInString(label)
	drawText(screen, width-labelWidth, y, labelWidth, styleWithForeground(styles.Border, styles.Error).Bold(true), label)
}

func truncateRunes(value string, width int) string {
	return truncateCells(value, width)
}

func displayWidth(value string) int {
	return uniseg.StringWidth(value)
}

func truncateCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	limit := width - displayWidth("…")
	var out strings.Builder
	used := 0
	state := -1
	for value != "" {
		cluster, rest, clusterWidth, nextState := uniseg.FirstGraphemeClusterInString(value, state)
		if cluster == "" || clusterWidth <= 0 || used+clusterWidth > limit {
			break
		}
		out.WriteString(cluster)
		used += clusterWidth
		value, state = rest, nextState
	}
	return out.String() + "…"
}

func drawConversationStatus(screen tcell.Screen, width, y int, styles PageStyles, status RunStatus) {
	label := strings.TrimSpace(status.Label)
	if timer := strings.TrimSpace(status.Timer); timer != "" {
		label += "  " + timer
	}
	if label == "" || width < 4 || y < 0 {
		return
	}
	style := styles.Muted
	switch status.Label {
	case "Running":
		style = styles.Accent
	case "Completed":
		style = styles.Success
	case "Paused":
		style = styles.Warning
	case "Failed", "Interrupted", "Expired":
		style = styles.Error
	}
	label = " " + label + " "
	drawText(screen, 0, y, minInt(width, utf8.RuneCountInString(label)), styleWithForeground(styles.Background, style).Bold(status.Active), label)
}

func drawCanonicalHeader(screen tcell.Screen, width, height int, styles PageStyles, title, workspaceName string, plan PlanHeader) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	panel := styles.Panel.Bold(true)
	text := styleWithForeground(panel, styles.Text)
	muted := styleWithForeground(panel, styles.Muted)
	accent := styleWithForeground(panel, styles.Accent).Bold(true)

	spans := []renderSpan{{text: strings.TrimSpace(title), style: text}}
	if workspaceName = strings.TrimSpace(workspaceName); workspaceName != "" {
		spans = append(spans,
			renderSpan{text: "  /  ", style: muted},
			renderSpan{text: workspaceName, style: muted},
		)
	}
	if plan.Active {
		spans = append(spans, renderSpan{text: "  •  ", style: muted}, renderSpan{text: plan.StatusLabel, style: accent})
		if checkpoint := strings.TrimSpace(plan.CheckpointLabel); checkpoint != "" {
			spans = append(spans, renderSpan{text: "  ", style: muted}, renderSpan{text: checkpoint, style: text})
		}
	}

	rows := wrapHeaderSpans(spans, width, 2)
	if height < 2 && len(rows) > 1 {
		rows = rows[:1]
	}
	for y, row := range rows {
		drawText(screen, 0, y, width, panel, padRight("", width))
		drawSpans(screen, 0, y, width, row)
	}
	return len(rows)
}

func wrapHeaderSpans(spans []renderSpan, width, maxRows int) [][]renderSpan {
	return wrapStyledSpans(spans, width, maxRows)
}

// wrapStyledSpans is the V3 transcript's final edge guard. It wraps on word
// boundaries when possible, falls back to grapheme boundaries for long tokens,
// and measures terminal cells rather than bytes or runes while retaining each
// span's style.
func wrapStyledSpans(spans []renderSpan, width, maxRows int) [][]renderSpan {
	if width <= 0 || maxRows < 0 {
		return nil
	}
	type cluster struct {
		text           string
		style          tcell.Style
		keepBackground bool
		width          int
	}
	clusters := make([]cluster, 0)
	for _, span := range spans {
		remaining := span.text
		for remaining != "" {
			text, rest, cells, _ := uniseg.FirstGraphemeClusterInString(remaining, -1)
			if text == "" {
				break
			}
			if cells <= 0 {
				cells = 1
			}
			clusters = append(clusters, cluster{text: text, style: span.style, keepBackground: span.keepBackground, width: cells})
			remaining = rest
		}
	}
	if len(clusters) == 0 {
		return [][]renderSpan{{}}
	}

	rows := make([][]renderSpan, 0, 4)
	appendRow := func(items []cluster) bool {
		for len(items) > 0 && strings.TrimSpace(items[len(items)-1].text) == "" {
			items = items[:len(items)-1]
		}
		row := make([]renderSpan, 0, len(items))
		for _, item := range items {
			if len(row) > 0 {
				last := len(row) - 1
				if row[last].style == item.style && row[last].keepBackground == item.keepBackground {
					row[last].text += item.text
					continue
				}
			}
			row = append(row, renderSpan{text: item.text, style: item.style, keepBackground: item.keepBackground})
		}
		rows = append(rows, row)
		return maxRows > 0 && len(rows) >= maxRows
	}

	trimLeading := false
	for len(clusters) > 0 {
		if trimLeading {
			for len(clusters) > 0 && strings.TrimSpace(clusters[0].text) == "" && clusters[0].text != "\n" {
				clusters = clusters[1:]
			}
			trimLeading = false
		}
		if len(clusters) == 0 {
			break
		}
		used, fit, newline := 0, 0, false
		for fit < len(clusters) {
			if clusters[fit].text == "\n" {
				newline = true
				break
			}
			if used > 0 && used+clusters[fit].width > width {
				break
			}
			used += clusters[fit].width
			fit++
			if used >= width {
				break
			}
		}
		if newline {
			if appendRow(clusters[:fit]) {
				return rows
			}
			clusters = clusters[fit+1:]
			continue
		}
		if fit >= len(clusters) {
			appendRow(clusters)
			break
		}
		if fit == 0 {
			fit = 1
		}
		cut := fit
		for index := fit - 1; index > 0; index-- {
			if strings.TrimSpace(clusters[index].text) == "" {
				cut = index
				break
			}
		}
		if appendRow(clusters[:cut]) {
			return rows
		}
		clusters = clusters[cut:]
		trimLeading = true
	}
	return rows
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

func (p *Page) drawCanonicalFooter(screen tcell.Screen, rect footerbar.Rect, state State, routeLabel string) {
	modelState := SelectModel(state)
	usage := SelectUsage(state)
	displayedMode := "off"
	planToggle := true
	worktreeRequested := false
	if strings.EqualFold(strings.TrimSpace(state.Session.Mode), "plan") {
		displayedMode = "on"
	}
	if draft, ok := SelectRoutedDraft(state); ok && strings.TrimSpace(state.Session.ID) == "" && draft.Status != RoutedDraftResolved {
		worktreeRequested = draft.ManagedWorktreeRequested
		if draft.PlanModeRequested {
			displayedMode = "on"
		} else {
			displayedMode = "off"
		}
	}
	localRoutedDraft := false
	if draft, ok := SelectRoutedDraft(state); ok && draft.Status != RoutedDraftResolved && strings.TrimSpace(state.Session.ID) == "" {
		localRoutedDraft = true
	}
	footerState := footerbar.State{
		RouteLabel:        strings.TrimSpace(routeLabel),
		DisplayedMode:     displayedMode,
		Agent:             "swarm",
		ModelLabel:        displayModelLabel(modelState.Preference),
		Thinking:          strings.TrimSpace(modelState.Preference.Thinking),
		ServiceTier:       strings.TrimSpace(modelState.Preference.ServiceTier),
		PlanToggle:        planToggle,
		WorktreeRequested: worktreeRequested,
		HideAgentModel:    localRoutedDraft,
		RightFacts:        conversationContextFacts(usage, modelState.ContextWindow),
	}
	footerbar.Draw(screen, footerbar.Styles{Border: p.styles.Border, Accent: p.styles.Accent, Secondary: p.styles.Secondary, Text: p.styles.Text}, rect, footerState, func(target footerbar.Rect, token footerbar.Token) {
		if token.Action == "open-agents-modal" {
			p.mu.Lock()
			p.agentModelTarget = target
			p.mu.Unlock()
		}
	})
}

func routedDraftStatusLine(draft RoutedDraft) string {
	switch draft.Status {
	case RoutedDraftRouting:
		return "Routing..."
	case RoutedDraftFailed:
		message := strings.TrimSpace(draft.Error)
		if message == "" {
			message = "Router start failed"
		}
		return message + " • retry the same request"
	default:
		return "Waiting..."
	}
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

func (p *Page) drawBashOutputModal(screen tcell.Screen, width, height int, styles PageStyles, tool ToolTimelineItem, scroll int) {
	if width < 20 || height < 8 {
		return
	}
	modalWidth := minInt(112, maxInt(20, width-2))
	modalHeight := minInt(maxInt(8, height-2), height)
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawBox(screen, x, y, modalWidth, modalHeight, styles.BorderActive)
	contentWidth := maxInt(1, modalWidth-4)
	title := "BASH OUTPUT"
	if command := firstToolString(parseToolObject(tool.Output), parseToolObject(tool.Arguments), "command"); command != "" {
		title += "  ·  " + command
	}
	drawText(screen, x+2, y+1, contentWidth, styles.Primary.Bold(true), truncateCells(title, contentWidth))
	drawText(screen, x+2, y+2, contentWidth, styles.Muted, "↑/↓ or PgUp/PgDn scroll  ·  Esc close")
	lines := wrapText(bashToolOutputText(tool), contentWidth)
	if len(lines) == 0 {
		lines = []string{"No Bash output."}
	}
	visibleRows := maxInt(1, modalHeight-5)
	maxScroll := maxInt(0, len(lines)-visibleRows)
	scroll = minInt(maxInt(0, scroll), maxScroll)
	p.mu.Lock()
	p.bashOutputModalScroll = scroll
	p.mu.Unlock()
	for row := 0; row < visibleRows && scroll+row < len(lines); row++ {
		drawText(screen, x+2, y+3+row, contentWidth, styles.Text, lines[scroll+row])
	}
	if maxScroll > 0 {
		indicator := fmt.Sprintf("%d/%d", scroll+1, maxScroll+1)
		drawText(screen, x+modalWidth-2-utf8.RuneCountInString(indicator), y+modalHeight-2, utf8.RuneCountInString(indicator), styles.Muted, indicator)
	}
}

func (p *Page) drawPlanModal(screen tcell.Screen, width, height int, styles PageStyles, plan *client.SessionPlan, scroll int) {
	if plan == nil || plan.Document == nil || width < 38 || height < 12 {
		return
	}
	modalWidth := minInt(112, width-4)
	modalHeight := minInt(height-4, maxInt(12, height-6))
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawBox(screen, x, y, modalWidth, modalHeight, styles.BorderActive)
	title := firstNonEmpty(plan.Title, plan.Document.Title, "Structured plan")
	if planID := strings.TrimSpace(plan.ID); planID != "" {
		title += "  ·  " + planID
	}
	drawText(screen, x+2, y+1, modalWidth-4, styles.Primary.Bold(true), "PLAN  ·  "+title)
	drawText(screen, x+2, y+2, modalWidth-4, styles.Muted, "Structured plan  ·  ↑/↓ scroll  ·  Esc close")
	lines := structuredPlanModalLines(plan.Document, modalWidth-4, styles)
	visibleRows := maxInt(1, modalHeight-5)
	maxScroll := maxInt(0, len(lines)-visibleRows)
	scroll = minInt(maxInt(0, scroll), maxScroll)
	p.mu.Lock()
	p.planModalScroll = scroll
	p.mu.Unlock()
	for row := 0; row < visibleRows && scroll+row < len(lines); row++ {
		line := lines[scroll+row]
		drawText(screen, x+2, y+3+row, modalWidth-4, line.Style, line.Text)
	}
	if maxScroll > 0 {
		indicator := fmt.Sprintf("%d/%d", scroll+1, maxScroll+1)
		drawText(screen, x+modalWidth-2-utf8.RuneCountInString(indicator), y+modalHeight-2, utf8.RuneCountInString(indicator), styles.Muted, indicator)
	}
}

func structuredPlanModalLines(document *client.SessionPlanDocument, width int, styles PageStyles) []permissionCardLine {
	if document == nil {
		return nil
	}
	lines := make([]permissionCardLine, 0, 32)
	appendWrapped := func(label, value string, style tcell.Style) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, line := range wrapText(label+value, maxInt(1, width)) {
			lines = append(lines, permissionCardLine{Text: line, Style: style})
		}
	}
	appendList := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		lines = append(lines, permissionCardLine{Text: label + ":", Style: styles.Secondary.Bold(true)})
		for _, value := range values {
			for index, line := range wrapText(strings.TrimSpace(value), maxInt(1, width-2)) {
				prefix := "  "
				if index == 0 {
					prefix = "• "
				}
				lines = append(lines, permissionCardLine{Text: prefix + line, Style: styles.Text})
			}
		}
	}
	appendWrapped("Goal: ", document.Info.Goal, styles.Text.Bold(true))
	appendWrapped("Scope: ", document.Info.Scope, styles.Text)
	appendWrapped("Context: ", document.Info.Context, styles.Text)
	appendList("Decisions", document.Info.Decisions)
	appendList("Success criteria", document.Info.SuccessCriteria)
	appendList("Constraints", document.Info.Constraints)
	appendList("Assumptions", document.Info.Assumptions)
	appendList("Open questions", document.Info.OpenQuestions)
	appendList("Files", document.Info.RelevantFiles)
	appendWrapped("Validation: ", document.Info.ValidationStrategy, styles.Text)
	for index, checkpoint := range document.Checkpoints {
		if len(lines) > 0 {
			lines = append(lines, permissionCardLine{Text: "", Style: styles.Muted})
		}
		order := checkpoint.Order
		if order <= 0 {
			order = index + 1
		}
		status := strings.ReplaceAll(strings.TrimSpace(checkpoint.Status), "_", " ")
		heading := fmt.Sprintf("%d. %s", order, firstNonEmpty(checkpoint.Title, checkpoint.ID, "Untitled checkpoint"))
		if status != "" {
			heading += "  [" + status + "]"
		}
		appendWrapped("", heading, styles.Primary.Bold(true))
		appendWrapped("Objective: ", checkpoint.Objective, styles.Text)
		appendList("Tasks", checkpoint.Tasks)
		appendList("Acceptance", checkpoint.AcceptanceCriteria)
		appendWrapped("Notes: ", checkpoint.Notes, styles.Muted)
		appendWrapped("Report: ", checkpoint.Report, styles.Text)
		appendWrapped("Result: ", checkpoint.Result, styles.Text)
		appendList("Changed files", checkpoint.ChangedFiles)
		appendList("Validation", checkpoint.Validation)
	}
	if len(lines) == 0 {
		lines = append(lines, permissionCardLine{Text: "No structured plan content was provided.", Style: styles.Muted})
	}
	return lines
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
	kind       string
	seq        uint64
	createdAt  int64
	order      int
	message    Message
	tool       ToolTimelineItem
	live       LiveSegment
	reasoning  ReasoningSegment
	permission PermissionTimelineItem
}

func (p *Page) renderRows(state State, width int, styles PageStyles) []renderRow {
	return p.renderRowsForHeight(state, width, 0, styles)
}

func (p *Page) renderRowsForHeight(state State, width, availableHeight int, styles PageStyles) []renderRow {
	permissions := SelectPermissions(state)
	items := make([]timelineRenderItem, 0, len(state.Messages)+len(state.Tools)+len(state.Live)+len(state.Reasoning)+len(permissions))
	for _, message := range SelectMessages(state) {
		item := timelineRenderItem{kind: "message", seq: message.GlobalSeq, createdAt: message.CreatedAt, order: len(items), message: message}
		if tool, ok := parseToolMessage(message); ok {
			if toolCoalescedWithPermission(tool, permissions) {
				continue
			}
			item.kind, item.tool = "tool", tool
		}
		items = append(items, item)
	}
	for _, tool := range SelectLiveTools(state) {
		if toolCoalescedWithPermission(tool, permissions) {
			continue
		}
		items = append(items, timelineRenderItem{kind: "tool", seq: tool.GlobalSeq, createdAt: tool.CreatedAt, order: len(items), tool: tool})
	}
	for _, segment := range SelectLiveSegments(state) {
		items = append(items, timelineRenderItem{kind: "live", seq: segment.GlobalSeq, createdAt: segment.CreatedAt, order: len(items), live: segment})
	}
	for _, segment := range SelectReasoningSegments(state) {
		items = append(items, timelineRenderItem{kind: "reasoning", seq: segment.GlobalSeq, createdAt: segment.StartedAt, order: len(items), reasoning: segment})
	}
	for _, permission := range permissions {
		createdAt := firstPositiveInt64(permission.Record.PermissionRequestedAt, permission.Record.CreatedAt)
		items = append(items, timelineRenderItem{kind: "permission", seq: permission.GlobalSeq, createdAt: createdAt, order: len(items), permission: permission})
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

	pendingPermissions := SelectPendingPermissions(state)
	p.mu.Lock()
	permissionIndex := p.permissionIndex
	permissionNote := append([]rune(nil), p.permissionInput...)
	permissionBusy, permissionError, permissionPrefix := p.permissionBusy, p.permissionError, p.permissionPrefix
	permissionContentScroll, permissionContentID := p.permissionContentScroll, p.permissionContentID
	permissionPlanReview, permissionPlanReviewID := p.permissionPlanReview, p.permissionPlanReviewID
	var permissionInteraction *permissionInteractionView
	if len(pendingPermissions) > 0 {
		interactionIndex := maxInt(0, minInt(permissionIndex, len(pendingPermissions)-1))
		permissionInteraction = p.permissionInteractionViewLocked(pendingPermissions[interactionIndex])
	}
	showThinkingTags := p.showThinkingTags
	p.mu.Unlock()
	selectedPermissionID := ""
	if len(pendingPermissions) > 0 {
		permissionIndex = maxInt(0, minInt(permissionIndex, len(pendingPermissions)-1))
		selectedPermissionID = pendingPermissions[permissionIndex].ID
	}

	rows := make([]renderRow, 0, len(items)*3+len(state.Pending)*2+4)
	if draft, ok := SelectRoutedDraft(state); ok && draft.Status != RoutedDraftResolved {
		if strings.TrimSpace(draft.Prompt) != "" {
			rows = append(rows, p.renderUserRows("routed-draft:"+draft.ClientRequestID, draft.Prompt, width, styles)...)
		}
		flags := []string{"Plan: " + map[bool]string{true: "on", false: "off"}[draft.PlanModeRequested], "Worktree: " + map[bool]string{true: "on", false: "off"}[draft.ManagedWorktreeRequested]}
		statusStyle := styles.Muted
		if draft.Status == RoutedDraftFailed {
			statusStyle = styles.Error
		}
		rows = append(rows, renderRow{text: routedDraftStatusLine(draft), style: statusStyle})
		rows = append(rows, renderRow{text: strings.Join(flags, " • "), style: styles.Muted}, renderRow{text: "", style: styles.Text})
	}
	boundedPermissionID, boundedPermissionMaxScroll := "", 0
	copyBlockBaseIndex := 0
	for _, item := range items {
		switch item.kind {
		case "permission":
			record := item.permission.Record
			prefix := record.SavedRulePreview
			selected := permissionPending(record) && record.ID == selectedPermissionID
			if selected && strings.TrimSpace(permissionPrefix) != "" {
				prefix = permissionPrefix
			}
			var manualReview *bool
			if record.ID == permissionPlanReviewID {
				manualReview = &permissionPlanReview
			}
			if isAskUserPermission(record) || isWorkspaceScopePermission(record) {
				interaction := permissionInteraction
				if interaction == nil || interaction.PermissionID != record.ID {
					interaction = &permissionInteractionView{PermissionID: record.ID}
				}
				rows = append(rows, specializedPermissionCardRows(record, len(pendingPermissions), width, styles, selected, permissionBusy, permissionError, interaction)...)
			} else if selected && isBashPermissionRequest(record) && availableHeight > 0 {
				contentScroll := 0
				if permissionContentID == record.ID {
					contentScroll = permissionContentScroll
				}
				cardRows, maxScroll := inlinePermissionCardRowsBounded(record, len(pendingPermissions), width, styles, prefix, selected, permissionNote, permissionBusy, permissionError, availableHeight, contentScroll)
				rows = append(rows, cardRows...)
				boundedPermissionID, boundedPermissionMaxScroll = record.ID, maxScroll
			} else {
				rows = append(rows, inlinePermissionCardRowsWithPlanReview(record, len(pendingPermissions), width, styles, prefix, selected, permissionNote, permissionBusy, permissionError, manualReview)...)
			}
		case "tool":
			rows = append(rows, p.renderToolRows(item.tool, width, styles)...)
		case "live":
			rows = append(rows, p.renderCopyAwareAssistantRows(item.live.Text, copyBlockBaseIndex, width, styles)...)
			copyBlockBaseIndex += copyBlockCount(item.live.Text)
		case "reasoning":
			rows = append(rows, p.renderReasoningRows(item.reasoning, showThinkingTags, width, styles)...)
		case "message":
			message := item.message
			if isStructuredFinalHandoffMessage(message) {
				rows = append(rows, p.renderFinalHandoffRows(message, width, styles)...)
				continue
			}
			if strings.EqualFold(message.Role, "user") {
				if len(rows) == 0 {
					rows = append(rows, renderRow{text: "", style: styles.Text})
				}
				rows = append(rows, p.renderUserRows("message:"+message.ID, message.Content, width, styles)...)
				continue
			}
			rows = append(rows, p.renderCopyAwareAssistantRows(message.Content, copyBlockBaseIndex, width, styles)...)
			copyBlockBaseIndex += copyBlockCount(message.Content)
			rows = append(rows, renderRow{text: "", style: styles.Text})
		}
	}

	pending := SelectPending(state)
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	for _, message := range pending {
		rows = append(rows, p.renderUserRows("pending:"+message.ID, message.Content, width, styles)...)
	}
	p.mu.Lock()
	if boundedPermissionID != "" && (p.permissionContentID == "" || p.permissionContentID == boundedPermissionID) {
		p.permissionContentID = boundedPermissionID
		p.permissionContentMaxScroll = boundedPermissionMaxScroll
		p.permissionContentScroll = minInt(maxInt(0, p.permissionContentScroll), boundedPermissionMaxScroll)
	} else if boundedPermissionID == "" {
		p.permissionContentMaxScroll = 0
		p.permissionContentScroll = 0
	}
	p.mu.Unlock()
	return rows
}

// Approval-gated control-plane tools are one timeline interaction. Their
// permission record owns the initial position and evolves through resolution and
// execution; a correlated tool-history item would produce a duplicate card.
func toolCoalescedWithPermission(tool ToolTimelineItem, permissions []PermissionTimelineItem) bool {
	toolName := normalizeToolDisplayName(tool.Name)
	if toolName != "plan-manage" && toolName != "exit-plan-mode" && toolName != "manage-sessions" {
		return false
	}
	callID := strings.TrimSpace(tool.CallID)
	if callID == "" {
		return false
	}
	for _, item := range permissions {
		record := item.Record
		if strings.TrimSpace(record.CallID) != callID {
			continue
		}
		if toolName == "plan-manage" || toolName == "exit-plan-mode" {
			if _, ok := parsePlanPermissionIntent(record); ok && normalizePermissionToolName(record.ToolName) == strings.ReplaceAll(toolName, "-", "_") {
				return true
			}
		}
		if toolName == "manage-sessions" && normalizePermissionToolName(record.ToolName) == "manage_sessions" && isManageSessionsApprovalRequirement(record.Requirement) {
			return true
		}
	}
	return false
}

func (p *Page) renderReasoningRows(segment ReasoningSegment, showThinkingTags bool, width int, styles PageStyles) []renderRow {
	status := strings.ToLower(strings.TrimSpace(segment.Status))
	symbol, headline := "•", "Thinking"
	style := styles.Accent
	switch status {
	case "done", "completed":
		symbol, style = "✓", styles.Success
	case "error", "failed":
		symbol, headline, style = "✕", "Thinking failed", styles.Error
	}
	rows := []renderRow{{text: symbol + " " + headline, style: style.Bold(true)}}
	if !showThinkingTags {
		return append(rows, renderRow{text: "", style: styles.Text})
	}
	body := strings.TrimSpace(firstNonEmpty(segment.Text, segment.Summary))
	if body == "" && status == "running" {
		body = "Thinking…"
	}
	for _, row := range p.renderAssistantRows(body, maxInt(1, width-2), styles) {
		row.text = "  " + row.text
		if len(row.spans) > 0 {
			row.spans = append([]renderSpan{{text: "  ", style: styles.Muted}}, row.spans...)
		}
		rows = append(rows, row)
	}
	return append(rows, renderRow{text: "", style: styles.Text})
}

func (p *Page) renderAssistantRows(content string, width int, styles PageStyles) []renderRow {
	content = sanitizeLegacyHandoffMarkers(content)
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
		spans := make([]renderSpan, 0, maxInt(1, len(line.Spans)))
		for _, span := range line.Spans {
			spans = append(spans, renderSpan{text: span.Text, style: span.Style})
		}
		if len(spans) == 0 {
			spans = append(spans, renderSpan{text: line.Text, style: line.Style})
		}
		for _, wrapped := range wrapStyledSpans(spans, width, 0) {
			rows = append(rows, renderRow{text: renderSpansText(wrapped), style: line.Style, spans: wrapped})
		}
	}
	return rows
}

func (p *Page) renderToolRows(tool ToolTimelineItem, width int, styles PageStyles) []renderRow {
	status := canonicalToolStatus(tool.Status)
	symbol, headerStyle := "•", styles.Accent
	switch status {
	case "completed":
		symbol, headerStyle = "✓", styles.Success
	case "failed", "cancelled":
		symbol, headerStyle = "✕", styles.Error
	}

	presentation := buildToolPresentation(tool)
	if presentation.Kind == "plan" {
		return p.renderPlanToolRows(tool, presentation, width, styles)
	}
	if presentation.Kind == "task" {
		return p.renderTaskToolRows(tool, presentation, width, styles)
	}
	if presentation.Kind == "manage-sessions" {
		return p.renderManageSessionsToolRows(tool, presentation, width, styles)
	}
	toolName := normalizeToolDisplayName(tool.Name)
	summary := strings.TrimSpace(presentation.Summary)
	if summary != toolName && !strings.HasPrefix(summary, toolName+" ") {
		summary = toolName + " · " + summary
	}
	headerStyle = headerStyle.Bold(true)
	textStyle := styles.Text.Bold(true)
	nameStyle := styleWithForeground(textStyle, styles.Primary)
	headerSpans := []renderSpan{
		{text: symbol, style: headerStyle},
		{text: " ", style: textStyle},
		{text: toolName, style: nameStyle},
		{text: strings.TrimPrefix(summary, toolName), style: textStyle},
	}
	if duration := toolDurationLabel(tool.DurationMS); duration != "" {
		headerSpans = append(headerSpans, renderSpan{text: " · " + duration, style: textStyle})
	}
	wrappedHeader := wrapStyledSpans(headerSpans, width, 0)
	rows := make([]renderRow, 0, len(wrappedHeader)+4)
	for index, spans := range wrappedHeader {
		row := renderRow{text: renderSpansText(spans), style: textStyle, spans: spans}
		if index == 0 {
			row.prefixWidth = utf8.RuneCountInString(symbol)
			row.prefixStyle = headerStyle
			row.highlightStart = utf8.RuneCountInString(symbol + " ")
			row.highlightWidth = utf8.RuneCountInString(toolName)
			row.highlightStyle = nameStyle
		}
		rows = append(rows, row)
	}
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
		clippedText := "  … output clipped"
		if normalizeToolDisplayName(tool.Name) == "bash" {
			clippedText = "  … clipped · use /output"
		}
		rows[len(rows)-1] = renderRow{text: clippedText, style: styles.Muted}
	}
	rows = append(rows, renderRow{text: "", style: styles.Text})
	return rows
}

func (p *Page) renderManageSessionsToolRows(tool ToolTimelineItem, presentation toolPresentation, width int, styles PageStyles) []renderRow {
	if width < 12 {
		return nil
	}
	borderStyle := styles.Border
	status := strings.ToLower(strings.TrimSpace(tool.Status))
	if status == "running" || status == "pending" || status == "started" {
		borderStyle = styles.Accent
	} else if status == "failed" || status == "error" || status == "cancelled" || status == "canceled" {
		borderStyle = styles.Error
	}
	innerWidth := maxInt(1, width-2)
	contentWidth := maxInt(1, innerWidth-2)
	rows := []renderRow{{text: "┌" + strings.Repeat("─", innerWidth) + "┐", style: borderStyle}}
	appendBody := func(text string, style tcell.Style) {
		for _, wrapped := range p.cachedWrap("manage-sessions:"+tool.ID+":"+text, strings.TrimSpace(text), contentWidth) {
			wrapped = truncateCells(wrapped, contentWidth)
			padding := strings.Repeat(" ", maxInt(0, contentWidth-displayWidth(wrapped)))
			rows = append(rows, renderRow{
				text:  "│ " + wrapped + padding + " │",
				style: style,
				spans: []renderSpan{{text: "│ ", style: borderStyle}, {text: wrapped + padding, style: style}, {text: " │", style: borderStyle}},
			})
		}
	}
	appendBody("SESSIONS  ·  "+strings.TrimPrefix(strings.TrimSpace(presentation.Summary), "sessions "), styles.Secondary.Bold(true))
	for _, line := range presentation.Lines {
		style := styles.Text
		switch line.Tone {
		case "added":
			style = styles.Success
		case "error":
			style = styles.Error
		case "path":
			style = styles.Secondary
		case "muted":
			style = styles.Muted
		case "label":
			style = styles.Text.Bold(true)
		}
		appendBody(line.Text, style)
	}
	if len(presentation.Lines) == 0 {
		appendBody("No structured session details were returned.", styles.Muted)
	}
	rows = append(rows, renderRow{text: "└" + strings.Repeat("─", innerWidth) + "┘", style: borderStyle})
	return append(rows, renderRow{text: "", style: styles.Text})
}

func (p *Page) renderTaskToolRows(tool ToolTimelineItem, presentation toolPresentation, width int, styles PageStyles) []renderRow {
	if width <= 0 {
		return nil
	}
	headerStyle := styles.Secondary.Bold(true)
	rows := []renderRow{{text: truncateRunes(strings.ToUpper(strings.TrimSpace(presentation.Summary)), width), style: headerStyle}}
	if width < 8 {
		rows = append(rows, renderRow{text: "", style: styles.Text})
		return rows
	}

	innerWidth := width - 2
	contentWidth := maxInt(1, innerWidth-2)
	for _, taskRow := range presentation.TaskRows {
		statusLabel := taskPresentationStatusLabel(taskRow.Status)
		statusStyle := styles.Muted
		borderStyle := styles.Border
		switch taskRow.Status {
		case "done":
			statusStyle = styles.Success
			borderStyle = styles.Success
		case "error", "cancelled":
			statusStyle = styles.Error
			borderStyle = styles.Error
		case "running":
			statusStyle = styles.Accent
			borderStyle = styles.Accent
		}
		rows = append(rows, renderRow{text: "┌" + strings.Repeat("─", innerWidth) + "┐", style: borderStyle})
		appendCardLine := func(key, text string, style tcell.Style) {
			text = strings.TrimSpace(text)
			if text == "" {
				return
			}
			for _, wrapped := range p.cachedWrap("task-card:"+tool.ID+":"+fmt.Sprint(taskRow.Index)+":"+key, text, contentWidth) {
				wrapped = truncateRunes(wrapped, contentWidth)
				padding := strings.Repeat(" ", maxInt(0, contentWidth-utf8.RuneCountInString(wrapped)))
				rows = append(rows, renderRow{
					text:  "│ " + wrapped + padding + " │",
					style: style,
					spans: []renderSpan{
						{text: "│ ", style: borderStyle},
						{text: wrapped + padding, style: style},
						{text: " │", style: borderStyle},
					},
				})
			}
		}

		title := firstNonEmptyToolRaw(taskRow.Title, taskRow.Agent, "subagent")
		appendCardLine("title", "["+statusLabel+"] "+title, statusStyle.Bold(true))
		agent := strings.TrimSpace(taskRow.Agent)
		if agent != "" && !strings.HasPrefix(agent, "@") {
			agent = "@" + agent
		}
		appendCardLine("identity", appendToolFacts(agent, []string{taskRow.Model}), styles.Secondary)
		appendCardLine("activity", "current: "+appendToolFacts(taskRow.Tool, []string{taskRow.Time}), styles.Muted)
		appendCardLine("preview", taskRow.Preview, styles.Muted)
		if taskRow.Error != "" {
			appendCardLine("error", "error: "+taskRow.Error, styles.Error)
		}
		rows = append(rows, renderRow{text: "└" + strings.Repeat("─", innerWidth) + "┘", style: borderStyle})
	}
	rows = append(rows, renderRow{text: "", style: styles.Text})
	return rows
}

func taskPresentationStatusLabel(status string) string {
	switch status {
	case "done":
		return "OK"
	case "error":
		return "ER"
	case "cancelled":
		return "CX"
	case "running":
		return "RUN"
	default:
		return "…"
	}
}

func (p *Page) renderPlanToolRows(tool ToolTimelineItem, presentation toolPresentation, width int, styles PageStyles) []renderRow {
	if width < 12 {
		return nil
	}
	borderStyle := styles.Border
	status := strings.ToLower(strings.TrimSpace(tool.Status))
	if status == "running" || status == "pending" || status == "started" {
		borderStyle = styles.Accent
	} else if status == "failed" || status == "error" || status == "cancelled" || status == "canceled" {
		borderStyle = styles.Error
	}
	innerWidth := maxInt(1, width-2)
	rows := make([]renderRow, 0, len(presentation.Lines)+3)
	rows = append(rows, renderRow{text: "┌" + strings.Repeat("─", innerWidth) + "┐", style: borderStyle})
	appendBody := func(text string, style tcell.Style) {
		text = truncateRunes(strings.TrimSpace(text), maxInt(1, innerWidth-2))
		padding := strings.Repeat(" ", maxInt(0, innerWidth-utf8.RuneCountInString(text)-2))
		rows = append(rows, renderRow{
			text:  "│ " + text + padding + " │",
			style: style,
			spans: []renderSpan{
				{text: "│ ", style: borderStyle},
				{text: text + padding, style: style},
				{text: " │", style: borderStyle},
			},
		})
	}
	planTitle := "PLAN  ·  " + strings.TrimPrefix(strings.TrimSpace(presentation.Summary), "plan ")
	for _, wrapped := range p.cachedWrap("plan-tool:"+tool.ID+":title", planTitle, maxInt(1, innerWidth-2)) {
		appendBody(wrapped, styles.Secondary.Bold(true))
	}
	for _, line := range presentation.Lines {
		style := styles.Text
		if line.Tone == "label" {
			style = styles.Text.Bold(true)
		} else if line.Tone == "muted" {
			style = styles.Muted
		} else if strings.HasPrefix(line.Tone, "checkpoint:") {
			style = planCheckpointCardStyle(strings.TrimPrefix(line.Tone, "checkpoint:"), styles)
		}
		for _, wrapped := range p.cachedWrap("plan-tool:"+tool.ID+":"+line.Tone+":"+line.Text, line.Text, maxInt(1, innerWidth-2)) {
			appendBody(wrapped, style)
		}
	}
	if p.runtime != nil && p.runtime.Store() != nil {
		plan := p.runtime.Store().Snapshot().Plan.ActivePlan
		if plan != nil && plan.Document != nil {
			appendBody("Ctrl+P or /plan  Open full plan", styles.Muted)
		}
	}
	rows = append(rows, renderRow{text: "└" + strings.Repeat("─", innerWidth) + "┘", style: borderStyle})
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
	type visualLine struct {
		text                   string
		prefixWidth            int
		sourceStart, sourceEnd int
		nextSource             int
		logicalEnd             int
	}

	width = maxInt(1, width)
	runes := []rune(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	cursor = maxInt(0, minInt(cursor, len(runes)))
	visualLines := make([]visualLine, 0, 4)
	firstLine := true
	segmentStart := 0

	for segmentStart <= len(runes) {
		segmentEnd := segmentStart
		for segmentEnd < len(runes) && runes[segmentEnd] != '\n' {
			segmentEnd++
		}
		position := segmentStart
		if position == segmentEnd {
			prefix := continuationPrefix
			if firstLine {
				prefix = firstPrefix
			}
			visualLines = append(visualLines, visualLine{
				text:        prefix,
				prefixWidth: len([]rune(prefix)),
				sourceStart: position,
				sourceEnd:   position,
				nextSource:  position,
				logicalEnd:  segmentEnd,
			})
			firstLine = false
		} else {
			for position < segmentEnd {
				prefix := continuationPrefix
				if firstLine {
					prefix = firstPrefix
				}
				prefixWidth := len([]rune(prefix))
				available := maxInt(1, width-prefixWidth)
				headEnd, nextSource := composerWordWrapBreak(runes, position, segmentEnd, available)
				visualLines = append(visualLines, visualLine{
					text:        prefix + string(runes[position:headEnd]),
					prefixWidth: prefixWidth,
					sourceStart: position,
					sourceEnd:   headEnd,
					nextSource:  nextSource,
					logicalEnd:  segmentEnd,
				})
				position = nextSource
				firstLine = false
			}
		}
		if segmentEnd == len(runes) {
			break
		}
		segmentStart = segmentEnd + 1
	}

	// Keep the insertion point visible when the final row exactly fills the
	// composer width, matching terminal behavior at the rightmost cell.
	terminalCursorLine := -1
	if len(visualLines) > 0 && len([]rune(visualLines[len(visualLines)-1].text)) >= width {
		terminalCursorLine = len(visualLines)
		visualLines = append(visualLines, visualLine{
			text:        continuationPrefix,
			prefixWidth: len([]rune(continuationPrefix)),
			sourceStart: len(runes),
			sourceEnd:   len(runes),
			nextSource:  len(runes),
			logicalEnd:  len(runes),
		})
	}

	lines := make([]string, len(visualLines))
	for i := range visualLines {
		lines[i] = visualLines[i].text
	}
	if terminalCursorLine >= 0 && cursor == len(runes) {
		return lines, terminalCursorLine, visualLines[terminalCursorLine].prefixWidth
	}
	for i, line := range visualLines {
		if cursor >= line.sourceStart && cursor < line.sourceEnd {
			return lines, i, line.prefixWidth + cursor - line.sourceStart
		}
		if cursor == line.sourceEnd {
			if line.sourceEnd < line.logicalEnd && line.nextSource == line.sourceEnd && i+1 < len(visualLines) {
				return lines, i + 1, visualLines[i+1].prefixWidth
			}
			return lines, i, line.prefixWidth + line.sourceEnd - line.sourceStart
		}
		if cursor > line.sourceEnd && cursor <= line.nextSource && i+1 < len(visualLines) {
			return lines, i + 1, visualLines[i+1].prefixWidth
		}
	}
	last := len(visualLines) - 1
	return lines, last, len([]rune(lines[last]))
}

func composerWordWrapBreak(runes []rune, start, end, width int) (headEnd, nextStart int) {
	if end-start <= width {
		return end, end
	}
	limit := start + width
	space := -1
	for i := start; i < limit; i++ {
		if runes[i] == ' ' || runes[i] == '\t' {
			space = i
		}
	}
	if space < 0 {
		return limit, limit
	}
	headEnd = space
	for headEnd > start && (runes[headEnd-1] == ' ' || runes[headEnd-1] == '\t') {
		headEnd--
	}
	if headEnd == start {
		return limit, limit
	}
	nextStart = space + 1
	for nextStart < end && (runes[nextStart] == ' ' || runes[nextStart] == '\t') {
		nextStart++
	}
	return headEnd, nextStart
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
	rows := wrapStyledSpans([]renderSpan{{text: strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")}}, width, 0)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, renderSpansText(row))
	}
	return out
}

func renderSpansText(spans []renderSpan) string {
	var text strings.Builder
	for _, span := range spans {
		text.WriteString(span.text)
	}
	return text.String()
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
	drawStyledText(s, x, y, width, style, text)
}

func drawSpans(s tcell.Screen, x, y, width int, spans []renderSpan) {
	col := 0
	for _, span := range spans {
		written := drawStyledText(s, x+col, y, width-col, span.style, span.text)
		col += written
		if col >= width {
			return
		}
	}
}

// drawStyledText follows tcell's grapheme-aware Put contract: Put writes one
// grapheme and reports its occupied cell width, so wide and combining content
// cannot overwrite the following card border or styled span.
func drawStyledText(s tcell.Screen, x, y, width int, style tcell.Style, text string) int {
	if width <= 0 || text == "" {
		return 0
	}
	col := 0
	for text != "" && col < width {
		cluster, rest, clusterWidth, _ := uniseg.FirstGraphemeClusterInString(text, -1)
		if cluster == "" {
			break
		}
		if clusterWidth <= 0 {
			clusterWidth = 1
		}
		if col+clusterWidth > width {
			break
		}
		_, displayedWidth := s.Put(x+col, y, cluster, style)
		if displayedWidth <= 0 {
			displayedWidth = clusterWidth
		}
		col += displayedWidth
		text = rest
	}
	return col
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

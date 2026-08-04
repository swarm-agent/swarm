package ui

import (
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

const (
	bottomBarHeight   = 3
	sectionGap        = 1
	inputCursorRune   = '█'
	homeTopBarHeight  = 5
	homeMaxInputRunes = 8000
)

type layoutVariant struct {
	Name              string
	ShowWorkspaceList bool
	ShowDirectory     bool
	ShowHero          bool
	ShowPresets       bool
	ShowTips          bool
	InputFirst        bool
	TopPadding        int
	UseSwarmTopBar    bool
	CenterRows        bool
}

type topItem struct {
	Label  string
	Style  tcell.Style
	Action string
	Index  int
}

type workspaceButton struct {
	Label  string
	Value  string
	Style  tcell.Style
	Action string
}

type clickTarget struct {
	Rect   Rect
	Action string
	Index  int
	Meta   string
}

type workspaceButtonState int

const (
	workspaceButtonIdle workspaceButtonState = iota
	workspaceButtonHover
	workspaceButtonPressed
	workspaceButtonSelected
)

var homeLayout = layoutVariant{
	Name:              "Tabs",
	ShowWorkspaceList: false,
	ShowDirectory:     false,
	ShowHero:          true,
	ShowPresets:       false,
	ShowTips:          true,
	InputFirst:        true,
	TopPadding:        0,
	UseSwarmTopBar:    true,
	CenterRows:        true,
}

type HomePage struct {
	theme                     Theme
	keybinds                  *KeyBindings
	model                     model.HomeModel
	prompt                    string
	sessionIntent             HomeSessionIntent
	promptCursor              int
	topBarTargets             []clickTarget
	bottomBarTargets          []clickTarget
	commandPaletteTargets     []clickTarget
	commandPaletteOptionIndex int
	commandPaletteOptionOwner string
	statusLine                string
	sessionMode               string
	selectedTopAction         string
	hoverTopAction            string
	pressedTopAction          string
	pressedTopFrames          int
	commandOverlay            []string
	onboarding                onboardingState
	swarmName                 string
	swarmNotificationCount    int
	alertsModal               alertsModalState
	sessionsModal             sessionsModalState
	commandSuggestions        []CommandSuggestion
	commandPaletteIndex       int
	authModal                 authModalState
	vaultModal                vaultModalState
	authDefaultsInfoModal     authDefaultsInfoModalState
	pendingAuthAction         *AuthModalAction
	pendingVaultAction        *VaultModalAction
	workspaceModal            workspaceModalState
	pendingWorkspaceAction    *WorkspaceModalAction
	worktreesModal            worktreesModalState
	pendingWorktreesAction    *WorktreesModalAction
	codexModal                codexUsageModalState
	codexModalTargets         []clickTarget
	modelsModal               modelsModalState
	modelsModalTargets        []clickTarget
	pendingModelsAction       *ModelsModalAction
	profilesModal             profilesModalState
	profilesModalTargets      []clickTarget
	agentsModal               agentsModalState
	agentsModalTargets        []clickTarget
	pendingAgentsAction       *AgentsModalAction
	voiceModal                voiceModalState
	pendingVoiceAction        *VoiceModalAction
	voiceInput                VoiceInputState
	pasteActive               bool
	pasteBuffer               []rune
	lastPasteBatchSize        int
	themeModal                themeModalState
	pendingThemeAction        *ThemeModalAction
	keybindsModal             keybindsModalState
	pendingKeybindsAction     *KeybindsModalAction
	pendingHomeAction         *HomeAction
	codexUsage                client.CodexAccountUsage
	codexResetCredits         client.CodexResetCredits
	toast                     toastState
}

func NewHomePage(m model.HomeModel) *HomePage {
	page := &HomePage{
		theme:        NordTheme(),
		keybinds:     NewDefaultKeyBindings(),
		model:        m,
		statusLine:   "Waiting...",
		sessionMode:  "auto",
		swarmName:    "Local",
		promptCursor: 0,
	}
	if m.OnboardingRequired {
		page.ShowOnboardingLocked("Complete required identity setup before using Swarm.")
	}
	return page
}

func (p *HomePage) HandleMouse(ev *tcell.EventMouse) {
	if p == nil || ev == nil {
		return
	}
	if p.codexModal.Visible {
		p.handleCodexUsageModalMouse(ev)
		return
	}
	if p.profilesModal.Visible {
		p.handleProfilesModalMouse(ev)
		return
	}
	if p.modelsModal.Visible {
		p.handleModelsModalMouse(ev)
		return
	}
	if p.agentsModal.Visible {
		p.handleAgentsModalMouse(ev)
		return
	}
	if p.onboarding.Visible || p.alertsModal.Visible || p.sessionsModal.Visible || p.authModal.Visible || p.vaultModal.Visible || p.authDefaultsInfoModal.Visible || p.workspaceModal.Visible || p.worktreesModal.Visible || p.voiceModal.Visible || p.themeModal.Visible || p.keybindsModal.Visible {
		return
	}

	x, y := ev.Position()
	buttons := ev.Buttons()

	target, hasTarget := p.topTargetAt(x, y)
	if hasTarget {
		p.hoverTopAction = target.Action
	} else {
		p.hoverTopAction = ""
	}
	if buttons&tcell.Button1 != 0 {
		for _, target := range p.bottomBarTargets {
			if target.Rect.Contains(x, y) {
				p.pressedTopAction = target.Action
				p.pressedTopFrames = 2
				p.handleTopTarget(target)
				return
			}
		}
	}
	if paletteTarget, ok := p.commandPaletteTargetAt(x, y); ok {
		switch paletteTarget.Action {
		case "palette-option":
			if selected, ok := p.selectedCommandSuggestion(); ok {
				p.commandPaletteOptionOwner = selected.Command
				p.commandPaletteOptionIndex = paletteTarget.Index
			}
			if buttons&tcell.Button1 != 0 {
				p.activateCommandPaletteTarget(paletteTarget)
			}
		case "palette-row":
			p.commandPaletteIndex = paletteTarget.Index
			p.resetCommandPaletteOptionSelection()
			if buttons&tcell.Button1 != 0 {
				p.activateCommandPaletteTarget(paletteTarget)
			}
		default:
			p.commandPaletteIndex = paletteTarget.Index
			if buttons&tcell.Button1 != 0 {
				p.activateCommandPaletteTarget(paletteTarget)
			}
		}
		return
	}

	if buttons&tcell.Button1 != 0 && hasTarget {
		p.pressedTopAction = target.Action
		p.pressedTopFrames = 2
		p.handleTopTarget(target)
		return
	}

	if buttons&tcell.Button1 == 0 && p.pressedTopFrames == 0 {
		p.pressedTopAction = ""
	}

}

func (p *HomePage) HandleTick() bool {
	changed := false
	if p.pressedTopFrames > 0 {
		p.pressedTopFrames--
		changed = true
		if p.pressedTopFrames == 0 {
			p.pressedTopAction = ""
		}
	}
	if p.toast.tick(time.Now()) {
		changed = true
	}
	return changed
}

func (p *HomePage) HandleKey(ev *tcell.EventKey) {
	if p.onboarding.Visible {
		p.handleOnboardingKey(ev)
		return
	}
	if p.alertsModal.Visible {
		p.handleAlertsModalKey(ev)
		return
	}
	if p.sessionsModal.Visible {
		p.handleSessionsModalKey(ev)
		return
	}
	if p.vaultModal.Visible {
		p.handleVaultModalKey(ev)
		return
	}
	if p.keybindsModal.Visible {
		p.handleKeybindsModalKey(ev)
		return
	}
	if p.authDefaultsInfoModal.Visible {
		p.handleAuthDefaultsInfoKey(ev)
		return
	}
	if p.authModal.Visible {
		p.handleAuthModalKey(ev)
		return
	}
	if p.workspaceModal.Visible {
		p.handleWorkspaceModalKey(ev)
		return
	}
	if p.worktreesModal.Visible {
		p.handleWorktreesModalKey(ev)
		return
	}
	if p.codexModal.Visible {
		p.handleCodexUsageModalKey(ev)
		return
	}
	if p.profilesModal.Visible {
		p.handleProfilesModalKey(ev)
		return
	}
	if p.modelsModal.Visible {
		p.handleModelsModalKey(ev)
		return
	}
	if p.agentsModal.Visible {
		p.handleAgentsModalKey(ev)
		return
	}
	if p.voiceModal.Visible {
		p.handleVoiceModalKey(ev)
		return
	}
	if p.themeModal.Visible {
		p.handleThemeModalKey(ev)
		return
	}
	if p.pasteActive {
		if p.HandlePasteKey(ev) {
			p.syncCommandPaletteSelection()
		}
		return
	}

	if p.keybinds.Match(ev, KeybindChatCycleMode) {
		if p.CanCycleSessionMode() {
			next := nextHomeSessionMode(p.sessionMode)
			p.SetSessionMode(next)
			p.statusLine = "Plan: " + currentDisplayedHomeSessionMode(p)
		} else {
			p.statusLine = "Plan toggle unavailable: " + currentHomeAgentModeCapability(p)
		}
		return
	}
	if p.keybinds.Match(ev, KeybindGlobalCycleRoute) {
		p.pendingHomeAction = &HomeAction{Kind: HomeActionCycleRoute}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindHomePaletteMoveUp):
		if p.commandPaletteActive() {
			p.moveCommandPaletteSelection(-1)
		}
		return
	case p.keybinds.Match(ev, KeybindHomePaletteMoveDown):
		if p.commandPaletteActive() {
			p.moveCommandPaletteSelection(1)
		}
		return
	case ev.Key() == tcell.KeyLeft:
		if p.commandPaletteActive() {
			p.moveCommandPaletteOptionSelection(-1)
			return
		}
		p.promptCursor = moveRuneCursorLeft(p.prompt, p.promptCursor)
		return
	case ev.Key() == tcell.KeyRight:
		if p.commandPaletteActive() {
			p.moveCommandPaletteOptionSelection(1)
			return
		}
		p.promptCursor = moveRuneCursorRight(p.prompt, p.promptCursor)
		return
	case p.keybinds.Match(ev, KeybindHomePromptBackspace):
		if len(p.pasteBuffer) > 0 {
			p.pasteBuffer = p.pasteBuffer[:len(p.pasteBuffer)-1]
			p.lastPasteBatchSize = len(p.pasteBuffer)
			p.syncCommandPaletteSelection()
			p.resetCommandPaletteOptionSelection()
			return
		}
		var changed bool
		p.prompt, p.promptCursor, changed = backspaceMultilineAtCursor(p.prompt, p.promptCursor)
		if changed {
			p.lastPasteBatchSize = 0
		}
		p.syncCommandPaletteSelection()
		p.resetCommandPaletteOptionSelection()
		return
	case p.keybinds.Match(ev, KeybindHomePromptClear):
		p.prompt = ""
		p.promptCursor = 0
		p.pasteBuffer = p.pasteBuffer[:0]
		p.lastPasteBatchSize = 0
		p.syncCommandPaletteSelection()
		p.resetCommandPaletteOptionSelection()
		return
	case p.keybinds.Match(ev, KeybindHomePromptComplete):
		p.completeCommandFromPalette()
		return
	case p.keybinds.Match(ev, KeybindHomePromptInsertNewline):
		inserted := 0
		p.prompt, p.promptCursor, inserted = insertMultilineAtCursor(p.prompt, p.promptCursor, "\n", homeMaxInputRunes)
		if inserted > 0 {
			p.pasteBuffer = p.pasteBuffer[:0]
			p.lastPasteBatchSize = 0
		}
		p.syncCommandPaletteSelection()
		p.resetCommandPaletteOptionSelection()
		return
	}
	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if unicode.IsPrint(r) {
			inserted := 0
			p.prompt, p.promptCursor, inserted = insertMultilineAtCursor(p.prompt, p.promptCursor, string(r), homeMaxInputRunes)
			if inserted > 0 {
				p.pasteBuffer = p.pasteBuffer[:0]
				p.lastPasteBatchSize = 0
			}
			p.syncCommandPaletteSelection()
			p.resetCommandPaletteOptionSelection()
		}
	}
}

func (p *HomePage) ChatOverlayVisible() bool {
	if p == nil {
		return false
	}
	return p.onboarding.Visible ||
		p.alertsModal.Visible ||
		p.sessionsModal.Visible ||
		p.keybindsModal.Visible ||
		p.authDefaultsInfoModal.Visible ||
		p.authModal.Visible ||
		p.workspaceModal.Visible ||
		p.worktreesModal.Visible ||
		p.codexModal.Visible ||
		p.profilesModal.Visible ||
		p.modelsModal.Visible ||
		p.agentsModal.Visible ||
		p.voiceModal.Visible ||
		p.themeModal.Visible
}

func (p *HomePage) HandleChatOverlayMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil {
		return false
	}
	switch {
	case p.codexModal.Visible:
		p.handleCodexUsageModalMouse(ev)
		return true
	case p.profilesModal.Visible:
		p.handleProfilesModalMouse(ev)
		return true
	case p.modelsModal.Visible:
		p.handleModelsModalMouse(ev)
		return true
	case p.agentsModal.Visible:
		p.handleAgentsModalMouse(ev)
		return true
	case p.ChatOverlayVisible():
		return true
	default:
		return false
	}
}

func (p *HomePage) HandleChatOverlayKey(ev *tcell.EventKey) bool {
	if p == nil {
		return false
	}
	if p.onboarding.Visible {
		p.handleOnboardingKey(ev)
		return true
	}
	switch {
	case p.alertsModal.Visible:
		p.handleAlertsModalKey(ev)
		return true
	case p.sessionsModal.Visible:
		p.handleSessionsModalKey(ev)
		return true
	case p.keybindsModal.Visible:
		p.handleKeybindsModalKey(ev)
		return true
	case p.authDefaultsInfoModal.Visible:
		p.handleAuthDefaultsInfoKey(ev)
		return true
	case p.authModal.Visible:
		p.handleAuthModalKey(ev)
		return true
	case p.workspaceModal.Visible:
		p.handleWorkspaceModalKey(ev)
		return true
	case p.worktreesModal.Visible:
		p.handleWorktreesModalKey(ev)
		return true
	case p.codexModal.Visible:
		p.handleCodexUsageModalKey(ev)
		return true
	case p.profilesModal.Visible:
		p.handleProfilesModalKey(ev)
		return true
	case p.modelsModal.Visible:
		p.handleModelsModalKey(ev)
		return true
	case p.agentsModal.Visible:
		p.handleAgentsModalKey(ev)
		return true
	case p.voiceModal.Visible:
		p.handleVoiceModalKey(ev)
		return true
	case p.themeModal.Visible:
		p.handleThemeModalKey(ev)
		return true
	default:
		return false
	}
}

func (p *HomePage) DrawChatOverlay(s tcell.Screen) {
	if p == nil {
		return
	}
	p.drawOnboarding(s)
	p.drawAuthModal(s)
	p.drawAuthDefaultsInfoModal(s)
	p.drawWorkspaceModal(s)
	p.drawWorktreesModal(s)
	p.drawCodexUsageModal(s)
	p.drawProfilesModal(s)
	p.drawModelsModal(s)
	p.drawAgentsModal(s)
	p.drawVoiceModal(s)
	p.drawThemeModal(s)
	p.drawKeybindsModal(s)
	p.drawSessionsModal(s)
	p.drawAlertsModal(s)
	w, h := s.Size()
	drawToastOverlay(s, p.theme, &p.toast, Rect{X: 0, Y: 0, W: w, H: h}, 1)
}

func (p *HomePage) Draw(s tcell.Screen) {
	w, h := s.Size()
	FillRect(s, Rect{X: 0, Y: 0, W: w, H: h}, p.theme.Background)
	s.HideCursor()
	p.commandPaletteTargets = p.commandPaletteTargets[:0]
	p.bottomBarTargets = p.bottomBarTargets[:0]

	if w <= 0 || h <= 0 {
		return
	}
	if p.onboarding.Visible {
		p.drawOnboarding(s)
		return
	}
	if w < 16 || h < 4 {
		DrawText(s, 0, 0, w, p.theme.Warning, clampEllipsis("swarm home", w))
		return
	}

	profile := resolveHomeResponsiveLayout(w, h)
	variant := profile.Variant
	bottomBarH := profile.BottomBarHeight
	if bottomBarH >= h {
		bottomBarH = maxInt(0, h-1)
	}

	contentW := w - 8
	if variant.UseSwarmTopBar {
		if contentW > 92 {
			contentW = 92
		}
		if contentW < 54 {
			contentW = w - 2
		}
	} else {
		if contentW > 84 {
			contentW = 84
		}
		if contentW < 34 {
			contentW = w - 2
		}
	}
	if contentW < 16 {
		contentW = w
	}
	contentX := (w - contentW) / 2
	if contentX < 0 {
		contentX = 0
	}

	topAnchor := 0
	if variant.UseSwarmTopBar && h-bottomBarH >= homeTopBarHeight+4 {
		topBarRect := Rect{X: 0, Y: 0, W: w, H: homeTopBarHeight}
		p.drawSwarmTopBar(s, topBarRect)
		topAnchor = topBarRect.Y + topBarRect.H + 1
	} else {
		variant.UseSwarmTopBar = false
		p.topBarTargets = p.topBarTargets[:0]
	}

	sections := buildHomeSections(variant)
	if len(sections) == 0 {
		sections = []homeSection{{kind: "input", h: 3}}
	}
	inputHeight := p.desiredInputBarHeight(contentW)
	for i := range sections {
		switch sections[i].kind {
		case "input":
			sections[i].h = inputHeight
		case "tips":
			if warning := p.workspaceSetupWarning(); warning != "" {
				warningWidth := contentW
				if warningWidth > 118 {
					warningWidth = 118
				}
				sections[i].h = 1 + len(wrapVoiceModalText(warning, warningWidth))
			}
		}
	}
	pinMetaTop := profile.PinMetaTop && !variant.UseSwarmTopBar
	pinnedMetaH := 0
	if pinMetaTop {
		metaIdx := -1
		for i, sec := range sections {
			if sec.kind == "meta" {
				metaIdx = i
				pinnedMetaH = sec.h
				break
			}
		}
		if metaIdx >= 0 {
			sections = append(sections[:metaIdx], sections[metaIdx+1:]...)
		}
	}
	sectionsH := sectionStackHeight(sections)

	mainTop := topAnchor
	mainBottom := h - bottomBarH
	if mainBottom < mainTop {
		mainBottom = mainTop
	}
	if pinMetaTop && pinnedMetaH > 0 && mainTop < mainBottom {
		metaRect := Rect{X: contentX, Y: mainTop, W: contentW, H: pinnedMetaH}
		if metaRect.Y < 0 {
			delta := -metaRect.Y
			metaRect.Y = 0
			metaRect.H -= delta
		}
		if metaRect.Y+metaRect.H > mainBottom {
			metaRect.H = mainBottom - metaRect.Y
		}
		if metaRect.H > 0 {
			p.drawMeta(s, metaRect, variant)
			mainTop = metaRect.Y + metaRect.H
			if len(sections) > 0 && mainTop < mainBottom {
				mainTop += sectionGap
			}
		}
	}
	availableMainH := mainBottom - mainTop
	if availableMainH < 1 {
		availableMainH = 1
	}

	stackH := sectionsH
	if stackH > availableMainH {
		stackH = availableMainH
	}

	minStart := mainTop
	maxStart := mainBottom - stackH
	if maxStart < minStart {
		maxStart = minStart
	}
	startY := minStart
	if profile.CenterStack && stackH < availableMainH {
		startY = mainTop + (availableMainH-stackH)/2 + variant.TopPadding
		inputOffset, inputFound := inputSectionOffset(sections)
		if variant.UseSwarmTopBar && inputFound {
			desiredInputY := h/2 - inputHeight/2
			startY = desiredInputY - inputOffset + variant.TopPadding
		}
	}
	if startY < minStart {
		startY = minStart
	}
	if startY > maxStart {
		startY = maxStart
	}

	clipMainRect := func(rect Rect) (Rect, bool) {
		if rect.W <= 0 || rect.H <= 0 {
			return Rect{}, false
		}
		if rect.Y < mainTop {
			delta := mainTop - rect.Y
			rect.Y = mainTop
			rect.H -= delta
		}
		if rect.Y+rect.H > mainBottom {
			rect.H = mainBottom - rect.Y
		}
		if rect.Y < 0 {
			delta := -rect.Y
			rect.Y = 0
			rect.H -= delta
		}
		if rect.Y+rect.H > h {
			rect.H = h - rect.Y
		}
		return rect, rect.W > 0 && rect.H > 0
	}

	y := startY
	var inputRect Rect
	hasInputRect := false
	for i, sec := range sections {
		rawRect := Rect{X: contentX, Y: y, W: contentW, H: sec.h}
		if sec.kind == "input" {
			rawRect = Rect{X: 0, Y: y, W: w, H: sec.h}
		}
		rect, ok := clipMainRect(rawRect)
		if ok {
			switch sec.kind {
			case "hero":
				p.drawHeroPanel(s, rect, variant.CenterRows)
			case "meta":
				p.drawMeta(s, rect, variant)
			case "input":
				p.drawInputBar(s, rect, variant.CenterRows)
				inputRect = rect
				hasInputRect = true
			case "presets":
				p.drawPresetsRow(s, rect, variant.CenterRows)
			case "tips":
				p.drawTipsRow(s, rect, variant.CenterRows)
			}
		}
		y += sec.h
		if i < len(sections)-1 {
			y += sectionGap
		}
	}

	if bottomBarH > 0 {
		bottomRect := Rect{X: 0, Y: h - bottomBarH, W: w, H: bottomBarH}
		p.drawBottomBar(s, bottomRect, variant)
	}
	if hasInputRect {
		p.drawCommandPalette(s, inputRect, variant, bottomBarH)
	}
	p.drawAuthModal(s)
	p.drawVaultModal(s)
	p.drawAuthDefaultsInfoModal(s)
	p.drawWorkspaceModal(s)
	p.drawWorktreesModal(s)
	p.drawCodexUsageModal(s)
	p.drawProfilesModal(s)
	p.drawModelsModal(s)
	p.drawAgentsModal(s)
	p.drawVoiceModal(s)
	p.drawThemeModal(s)
	p.drawKeybindsModal(s)
	p.drawSessionsModal(s)
	p.drawAlertsModal(s)
	toastInset := 1
	drawToastOverlay(s, p.theme, &p.toast, Rect{X: 0, Y: 0, W: w, H: h}, toastInset)
}

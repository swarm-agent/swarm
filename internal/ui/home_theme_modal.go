package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type ThemeModalEntry struct {
	ID   string
	Name string
}

type ThemeModalActionKind string

const (
	ThemeModalActionPreview ThemeModalActionKind = "preview"
	ThemeModalActionApply   ThemeModalActionKind = "apply"
	ThemeModalActionCancel  ThemeModalActionKind = "cancel"
)

type ThemeModalAction struct {
	Kind    ThemeModalActionKind
	ThemeID string
}

type themeModalState struct {
	Visible    bool
	Entries    []ThemeModalEntry
	Selected   int
	OriginalID string
	Status     string
}

const (
	themeModalMinWidth = 40
)

func (p *HomePage) ShowThemeModal(currentThemeID string) {
	p.themeModal.Visible = true
	p.themeModal.OriginalID = NormalizeThemeID(currentThemeID)
	if p.themeModal.OriginalID == "" {
		p.themeModal.OriginalID = DefaultThemeID()
	}
	if len(p.themeModal.Entries) == 0 {
		p.themeModal.Status = "no themes available"
		return
	}
	idx := p.findThemeModalIndex(p.themeModal.OriginalID)
	if idx < 0 {
		idx = 0
	}
	p.themeModal.Selected = idx
	p.themeModal.Status = "Up/Down previews live, Enter applies, Esc cancels"
}

func (p *HomePage) HideThemeModal() {
	p.themeModal = themeModalState{}
}

func (p *HomePage) ThemeModalVisible() bool {
	return p.themeModal.Visible
}

func (p *HomePage) HandleThemeModalKey(ev *tcell.EventKey) {
	if p == nil || !p.themeModal.Visible {
		return
	}
	p.handleThemeModalKey(ev)
}

func (p *HomePage) DrawThemeModalOverlay(s tcell.Screen) {
	if p == nil || !p.themeModal.Visible {
		return
	}
	p.drawThemeModal(s)
}

func (p *HomePage) SetThemeModalStatus(status string) {
	p.themeModal.Status = strings.TrimSpace(status)
}

func (p *HomePage) SetThemeModalData(entries []ThemeModalEntry, currentThemeID string) {
	p.themeModal.Entries = append([]ThemeModalEntry(nil), entries...)
	p.themeModal.Selected = p.findThemeModalIndex(currentThemeID)
	if p.themeModal.Selected < 0 {
		p.themeModal.Selected = 0
	}
}

func (p *HomePage) PopThemeModalAction() (ThemeModalAction, bool) {
	if p.pendingThemeAction == nil {
		return ThemeModalAction{}, false
	}
	action := *p.pendingThemeAction
	p.pendingThemeAction = nil
	return action, true
}

func (p *HomePage) handleThemeModalKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.enqueueThemeModalAction(ThemeModalAction{Kind: ThemeModalActionCancel, ThemeID: p.themeModal.OriginalID})
		p.HideThemeModal()
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveThemeModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveThemeModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalJumpHome):
		p.jumpThemeModalSelection(0)
		return
	case p.keybinds.Match(ev, KeybindModalJumpEnd):
		p.jumpThemeModalSelection(len(p.themeModal.Entries) - 1)
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		selected, ok := p.selectedThemeModalEntry()
		if !ok {
			return
		}
		p.enqueueThemeModalAction(ThemeModalAction{Kind: ThemeModalActionApply, ThemeID: selected.ID})
		p.HideThemeModal()
		return
	}

	if ev.Key() == tcell.KeyRune {
		switch {
		case p.keybinds.Match(ev, KeybindModalMoveDownAlt):
			p.moveThemeModalSelection(1)
		case p.keybinds.Match(ev, KeybindModalMoveUpAlt):
			p.moveThemeModalSelection(-1)
		case p.keybinds.Match(ev, KeybindThemeJumpHomeAlt):
			p.jumpThemeModalSelection(0)
		case p.keybinds.Match(ev, KeybindThemeJumpEndAlt):
			p.jumpThemeModalSelection(len(p.themeModal.Entries) - 1)
		}
	}
}

func (p *HomePage) moveThemeModalSelection(delta int) {
	if len(p.themeModal.Entries) == 0 || delta == 0 {
		return
	}
	next := p.themeModal.Selected + delta
	if next < 0 {
		next = len(p.themeModal.Entries) - 1
	}
	if next >= len(p.themeModal.Entries) {
		next = 0
	}
	p.jumpThemeModalSelection(next)
}

func (p *HomePage) jumpThemeModalSelection(index int) {
	if len(p.themeModal.Entries) == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(p.themeModal.Entries) {
		index = len(p.themeModal.Entries) - 1
	}
	if index == p.themeModal.Selected {
		return
	}
	p.themeModal.Selected = index
	selected, ok := p.selectedThemeModalEntry()
	if !ok {
		return
	}
	p.enqueueThemeModalAction(ThemeModalAction{Kind: ThemeModalActionPreview, ThemeID: selected.ID})
	p.themeModal.Status = fmt.Sprintf("previewing: %s", selected.ID)
}

func (p *HomePage) selectedThemeModalEntry() (ThemeModalEntry, bool) {
	if len(p.themeModal.Entries) == 0 {
		return ThemeModalEntry{}, false
	}
	if p.themeModal.Selected < 0 {
		p.themeModal.Selected = 0
	}
	if p.themeModal.Selected >= len(p.themeModal.Entries) {
		p.themeModal.Selected = len(p.themeModal.Entries) - 1
	}
	return p.themeModal.Entries[p.themeModal.Selected], true
}

func (p *HomePage) findThemeModalIndex(themeID string) int {
	themeID = NormalizeThemeID(themeID)
	if themeID == "" {
		return -1
	}
	for i, entry := range p.themeModal.Entries {
		if NormalizeThemeID(entry.ID) == themeID {
			return i
		}
	}
	return -1
}

func (p *HomePage) enqueueThemeModalAction(action ThemeModalAction) {
	p.pendingThemeAction = &action
}

func (p *HomePage) drawThemeModal(s tcell.Screen) {
	if !p.themeModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 40 || h < 10 {
		return
	}

	modalW := minInt(72, w-6)
	if modalW < 52 {
		modalW = w - 2
	}
	if modalW < themeModalMinWidth {
		return
	}
	modalH := minInt(22, h-4)
	if modalH < 14 {
		modalH = h - 2
	}
	if modalH < 10 {
		return
	}

	rect := Rect{
		X: maxInt(1, (w-modalW)/2),
		Y: maxInt(1, (h-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Themes")

	selected, _ := p.selectedThemeModalEntry()
	status := strings.TrimSpace(p.themeModal.Status)
	if status == "" {
		status = "Up/Down previews live, Enter applies, Esc cancels"
	}
	contentW := rect.W - 4
	DrawText(s, rect.X+2, rect.Y+1, contentW, p.theme.TextMuted, clampEllipsis(status, contentW))
	if strings.TrimSpace(selected.ID) != "" {
		DrawTextRight(s, rect.X+rect.W-3, rect.Y+1, rect.W/2, p.theme.TextMuted, clampEllipsis(selected.ID, rect.W/2))
	}

	listTop := rect.Y + 3
	listH := rect.H - 5
	if listH < 1 {
		listH = 1
	}
	if len(p.themeModal.Entries) == 0 {
		DrawText(s, rect.X+2, listTop, rect.W-4, p.theme.Warning, "no themes available")
		DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, "Esc close")
		return
	}

	start := 0
	if p.themeModal.Selected >= listH {
		start = p.themeModal.Selected - listH + 1
	}
	maxStart := len(p.themeModal.Entries) - listH
	if maxStart < 0 {
		maxStart = 0
	}
	if start > maxStart {
		start = maxStart
	}

	y := listTop
	for i := 0; i < listH; i++ {
		idx := start + i
		if idx >= len(p.themeModal.Entries) {
			break
		}
		entry := p.themeModal.Entries[idx]
		prefix := "  "
		if idx == p.themeModal.Selected {
			prefix = "> "
		}
		label := fmt.Sprintf("%s%-18s %s", prefix, entry.ID, entry.Name)
		DrawText(s, rect.X+2, y, rect.W-4, p.theme.Text, clampEllipsis(label, rect.W-4))
		y++
	}

	footer := "Up/Down move  Enter apply  Esc cancel"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(footer, rect.W-4))
}

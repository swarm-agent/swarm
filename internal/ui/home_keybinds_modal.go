package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type KeybindsModalActionKind string

const (
	KeybindsModalActionPersist KeybindsModalActionKind = "persist"
)

type KeybindsModalAction struct {
	Kind       KeybindsModalActionKind
	StatusHint string
}

type keybindsModalState struct {
	Visible   bool
	Selected  int
	Scroll    int
	Status    string
	Editing   bool
	EditingID KeybindID
}

func (p *HomePage) ShowKeybindsModal() {
	p.keybindsModal.Visible = true
	p.keybindsModal.Editing = false
	p.keybindsModal.EditingID = ""
	p.keybindsModal.Status = "Enter edits selected keybind"
	p.clampKeybindsModalSelection()
}

func (p *HomePage) HideKeybindsModal() {
	p.keybindsModal = keybindsModalState{}
	p.pendingKeybindsAction = nil
}

func (p *HomePage) KeybindsModalVisible() bool {
	return p.keybindsModal.Visible
}

func (p *HomePage) SetKeybindsModalStatus(status string) {
	p.keybindsModal.Status = strings.TrimSpace(status)
}

func (p *HomePage) PopKeybindsModalAction() (KeybindsModalAction, bool) {
	if p.pendingKeybindsAction == nil {
		return KeybindsModalAction{}, false
	}
	action := *p.pendingKeybindsAction
	p.pendingKeybindsAction = nil
	return action, true
}

func (p *HomePage) enqueueKeybindsModalAction(action KeybindsModalAction) {
	p.pendingKeybindsAction = &action
}

func (p *HomePage) keybindsModalDefinitions() []KeybindDefinition {
	defs := KeybindDefinitions()
	out := make([]KeybindDefinition, 0, len(defs))
	for _, def := range defs {
		if def.Editable {
			out = append(out, def)
		}
	}
	return out
}

func (p *HomePage) clampKeybindsModalSelection() {
	defs := p.keybindsModalDefinitions()
	if len(defs) == 0 {
		p.keybindsModal.Selected = 0
		p.keybindsModal.Scroll = 0
		return
	}
	if p.keybindsModal.Selected < 0 {
		p.keybindsModal.Selected = 0
	}
	if p.keybindsModal.Selected >= len(defs) {
		p.keybindsModal.Selected = len(defs) - 1
	}
	if p.keybindsModal.Scroll < 0 {
		p.keybindsModal.Scroll = 0
	}
}

func (p *HomePage) shiftKeybindsModalSelection(delta int) {
	defs := p.keybindsModalDefinitions()
	if len(defs) == 0 || delta == 0 {
		return
	}
	next := p.keybindsModal.Selected + delta
	if next < 0 {
		next = 0
	}
	if next >= len(defs) {
		next = len(defs) - 1
	}
	p.keybindsModal.Selected = next
}

func (p *HomePage) selectedKeybindDefinition() (KeybindDefinition, bool) {
	defs := p.keybindsModalDefinitions()
	if len(defs) == 0 {
		return KeybindDefinition{}, false
	}
	if p.keybindsModal.Selected < 0 {
		p.keybindsModal.Selected = 0
	}
	if p.keybindsModal.Selected >= len(defs) {
		p.keybindsModal.Selected = len(defs) - 1
	}
	return defs[p.keybindsModal.Selected], true
}

func (p *HomePage) handleKeybindsModalKey(ev *tcell.EventKey) {
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	if p.keybindsModal.Editing {
		if p.keybinds.Match(ev, KeybindKeybindsModalCancelEdit) {
			p.keybindsModal.Editing = false
			p.keybindsModal.EditingID = ""
			p.keybindsModal.Status = "Edit cancelled"
			return
		}
		if p.keybindsModal.EditingID == "" {
			p.keybindsModal.Editing = false
			p.keybindsModal.Status = "No keybind selected"
			return
		}
		if err := p.keybinds.SetFromEvent(p.keybindsModal.EditingID, ev); err != nil {
			p.keybindsModal.Status = fmt.Sprintf("Invalid key: %v", err)
			return
		}
		def, ok := LookupKeybindDefinition(p.keybindsModal.EditingID)
		if !ok {
			def.Action = string(p.keybindsModal.EditingID)
		}
		p.keybindsModal.Editing = false
		p.keybindsModal.Status = fmt.Sprintf("%s set to %s", def.Action, p.keybinds.Label(p.keybindsModal.EditingID))
		p.keybindsModal.EditingID = ""
		p.enqueueKeybindsModalAction(KeybindsModalAction{Kind: KeybindsModalActionPersist, StatusHint: "keybind updated"})
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindKeybindsModalClose):
		p.HideKeybindsModal()
		return
	case p.keybinds.Match(ev, KeybindKeybindsModalMoveUp), p.keybinds.Match(ev, KeybindKeybindsModalMoveUpAlt):
		p.shiftKeybindsModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindKeybindsModalMoveDown), p.keybinds.Match(ev, KeybindKeybindsModalMoveDownAlt):
		p.shiftKeybindsModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindKeybindsModalReset):
		def, ok := p.selectedKeybindDefinition()
		if !ok {
			p.keybindsModal.Status = "No keybind selected"
			return
		}
		p.keybinds.Reset(def.ID)
		p.keybindsModal.Status = fmt.Sprintf("Reset %s", def.Action)
		p.enqueueKeybindsModalAction(KeybindsModalAction{Kind: KeybindsModalActionPersist, StatusHint: "keybind reset"})
		return
	case p.keybinds.Match(ev, KeybindKeybindsModalResetAll):
		p.keybinds.ResetAll()
		p.keybindsModal.Status = "Reset all keybinds"
		p.enqueueKeybindsModalAction(KeybindsModalAction{Kind: KeybindsModalActionPersist, StatusHint: "keybinds reset"})
		return
	case p.keybinds.Match(ev, KeybindKeybindsModalEdit):
		def, ok := p.selectedKeybindDefinition()
		if !ok {
			p.keybindsModal.Status = "No keybind selected"
			return
		}
		p.keybindsModal.Editing = true
		p.keybindsModal.EditingID = def.ID
		p.keybindsModal.Status = fmt.Sprintf("Editing %s: press new key (Esc to cancel)", def.Action)
		return
	}
}

func (p *HomePage) drawKeybindsModal(s tcell.Screen) {
	if !p.keybindsModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 44 || h < 12 {
		return
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	defs := p.keybindsModalDefinitions()
	p.clampKeybindsModalSelection()

	modalW := minInt(100, w-6)
	if modalW < 62 {
		modalW = w - 2
	}
	if modalW < 44 {
		return
	}
	modalH := minInt(30, h-4)
	if modalH < 16 {
		modalH = h - 2
	}
	if modalH < 12 {
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
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Keybinds")

	status := strings.TrimSpace(p.keybindsModal.Status)
	if status == "" {
		status = "Enter edit • r reset selected • Shift+R reset all • Esc close"
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, clampEllipsis(status, rect.W-4))

	listTop := rect.Y + 3
	listH := rect.H - 6
	if listH < 1 {
		listH = 1
	}
	if len(defs) == 0 {
		DrawText(s, rect.X+2, listTop, rect.W-4, p.theme.Warning, "No keybind definitions found")
		return
	}

	if p.keybindsModal.Selected < p.keybindsModal.Scroll {
		p.keybindsModal.Scroll = p.keybindsModal.Selected
	}
	if p.keybindsModal.Selected >= p.keybindsModal.Scroll+listH {
		p.keybindsModal.Scroll = p.keybindsModal.Selected - listH + 1
	}
	if p.keybindsModal.Scroll < 0 {
		p.keybindsModal.Scroll = 0
	}
	maxScroll := len(defs) - listH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if p.keybindsModal.Scroll > maxScroll {
		p.keybindsModal.Scroll = maxScroll
	}

	compact := rect.W < 62
	groupW := 15
	if compact {
		groupW = 10
	}
	if groupW > rect.W/3 {
		groupW = rect.W / 3
	}
	keyColW := 18
	if compact {
		keyColW = 10
	}
	if keyColW > rect.W/3 {
		keyColW = rect.W / 3
	}

	y := listTop
	for row := 0; row < listH; row++ {
		idx := p.keybindsModal.Scroll + row
		if idx >= len(defs) {
			break
		}
		def := defs[idx]
		selected := idx == p.keybindsModal.Selected
		style := p.theme.TextMuted
		if selected {
			style = p.theme.Primary
		}

		group := clampEllipsis(def.Group, groupW)
		actionW := rect.W - 6 - groupW - keyColW
		if actionW < 8 {
			actionW = 8
		}
		action := clampEllipsis(def.Action, actionW)
		keyLabel := p.keybinds.Label(def.ID)
		if p.keybindsModal.Editing && p.keybindsModal.EditingID == def.ID {
			keyLabel = "..."
		}

		if compact {
			line := clampEllipsis(fmt.Sprintf("%s %s", group, action), rect.W-keyColW-5)
			DrawText(s, rect.X+2, y, rect.W-keyColW-4, style, line)
			DrawTextRight(s, rect.X+rect.W-3, y, keyColW, style, clampEllipsis(keyLabel, keyColW))
		} else {
			DrawText(s, rect.X+2, y, groupW, style, group)
			DrawText(s, rect.X+2+groupW+1, y, actionW, style, action)
			DrawTextRight(s, rect.X+rect.W-3, y, keyColW, style, clampEllipsis(keyLabel, keyColW))
		}
		y++
	}

	helpline := "Enter edit • r reset • Shift+R reset all • Esc close"
	if p.keybindsModal.Editing {
		helpline = "Press new key now • Esc cancels"
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(helpline, rect.W-4))
}

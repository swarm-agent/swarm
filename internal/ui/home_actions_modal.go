package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

const maxWorkspaceActionQuickList = 20

type workspaceActionsModalState struct {
	Visible       bool
	Loading       bool
	WorkspacePath string
	Actions       []client.WorkspaceAction
	Selected      int
	Error         string
	Status        string
}

type WorkspaceActionSelection struct {
	WorkspacePath string
	Action        client.WorkspaceAction
}

func (p *HomePage) ShowWorkspaceActionsLoading(workspacePath string) {
	p.workspaceActionsModal = workspaceActionsModalState{
		Visible:       true,
		Loading:       true,
		WorkspacePath: strings.TrimSpace(workspacePath),
		Selected:      -1,
		Status:        "Loading canonical workspace Actions…",
	}
}

func (p *HomePage) SetWorkspaceActions(actions []client.WorkspaceAction) {
	ordered := append([]client.WorkspaceAction(nil), actions...)
	if len(ordered) > maxWorkspaceActionQuickList {
		ordered = ordered[:maxWorkspaceActionQuickList]
	}
	p.workspaceActionsModal.Actions = ordered
	p.workspaceActionsModal.Loading = false
	p.workspaceActionsModal.Error = ""
	p.workspaceActionsModal.Selected = -1
	if len(ordered) > 0 {
		p.workspaceActionsModal.Selected = 0
	}
	p.workspaceActionsModal.Status = "Choose an Action and press Enter to run it explicitly."
	if len(actions) > len(ordered) {
		p.workspaceActionsModal.Status = fmt.Sprintf("Showing the first %d of %d Actions. Press Enter to run the selected Action.", len(ordered), len(actions))
	}
}

func (p *HomePage) SetWorkspaceActionsError(err string) {
	p.workspaceActionsModal.Loading = false
	p.workspaceActionsModal.Error = strings.TrimSpace(err)
	p.workspaceActionsModal.Status = ""
}

func (p *HomePage) WorkspaceActionsModalVisible() bool {
	return p != nil && p.workspaceActionsModal.Visible
}

func (p *HomePage) PopWorkspaceActionSelection() (WorkspaceActionSelection, bool) {
	if p == nil || p.pendingWorkspaceActionSelection == nil {
		return WorkspaceActionSelection{}, false
	}
	selection := *p.pendingWorkspaceActionSelection
	p.pendingWorkspaceActionSelection = nil
	return selection, true
}

func (p *HomePage) handleWorkspaceActionsModalKey(ev *tcell.EventKey) {
	if ev == nil {
		return
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.workspaceActionsModal = workspaceActionsModalState{}
	case ev.Key() == tcell.KeyUp:
		p.moveWorkspaceActionsSelection(-1)
	case ev.Key() == tcell.KeyDown:
		p.moveWorkspaceActionsSelection(1)
	case ev.Key() == tcell.KeyEnter:
		state := &p.workspaceActionsModal
		if state.Loading || state.Selected < 0 || state.Selected >= len(state.Actions) {
			return
		}
		action := state.Actions[state.Selected]
		if len(action.Inputs) > 0 {
			state.Status = "This Action needs prompted inputs; run it from Desktop after reviewing them."
			return
		}
		p.pendingWorkspaceActionSelection = &WorkspaceActionSelection{WorkspacePath: state.WorkspacePath, Action: action}
		state.Status = "Starting " + strings.TrimSpace(action.Name) + "…"
	}
}

func (p *HomePage) moveWorkspaceActionsSelection(delta int) {
	state := &p.workspaceActionsModal
	if delta == 0 || len(state.Actions) == 0 {
		return
	}
	if state.Selected < 0 || state.Selected >= len(state.Actions) {
		state.Selected = 0
		return
	}
	state.Selected = (state.Selected + delta + len(state.Actions)) % len(state.Actions)
}

func (p *HomePage) drawWorkspaceActionsModal(s tcell.Screen) {
	state := &p.workspaceActionsModal
	if !state.Visible {
		return
	}
	w, h := s.Size()
	width := minInt(72, maxInt(34, w-4))
	height := minInt(maxInt(10, len(state.Actions)+8), maxInt(8, h-2))
	rect := Rect{X: (w - width) / 2, Y: (h - height) / 2, W: width, H: height}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text.Bold(true), "Workspace Actions")
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, clampEllipsis(workspaceModalDisplayPath(state.WorkspacePath), rect.W-4))

	rowY := rect.Y + 3
	switch {
	case state.Loading:
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, "Loading canonical workspace Actions…")
	case state.Error != "":
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.Error, clampEllipsis(state.Error, rect.W-4))
	case len(state.Actions) == 0:
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, "No Actions are saved for this workspace.")
	default:
		for i, action := range state.Actions {
			if rowY >= rect.Y+rect.H-3 {
				break
			}
			prefix := "  "
			style := p.theme.Text
			if i == state.Selected {
				prefix = "> "
				style = p.theme.Primary.Bold(true)
			}
			pin := ""
			if action.Pinned {
				pin = "[pinned] "
			}
			line := fmt.Sprintf("%s%s%s · %s", prefix, pin, strings.TrimSpace(action.Name), strings.TrimSpace(action.Entrypoint))
			DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(line, rect.W-4))
			rowY++
		}
	}
	footerY := rect.Y + rect.H - 2
	if state.Status != "" {
		DrawText(s, rect.X+2, footerY-1, rect.W-4, p.theme.TextMuted, clampEllipsis(state.Status, rect.W-4))
	}
	DrawText(s, rect.X+2, footerY, rect.W-4, p.theme.TextMuted, "↑/↓ choose • Enter run • Esc close")
}

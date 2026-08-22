package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

type profilesModalState struct {
	Visible  bool
	Selected int
	Status   string
}

func (p *HomePage) ShowProfilesModal() {
	if p == nil {
		return
	}
	p.profilesModal.Visible = true
	p.profilesModal.Selected = 0
	for i, profile := range p.model.ModelProfiles {
		if strings.TrimSpace(profile.ProfileID) == strings.TrimSpace(p.model.ActiveModelProfile.ProfileID) {
			p.profilesModal.Selected = i
			break
		}
	}
	p.profilesModal.Status = "Enter applies • e opens agent setup • Esc closes"
}

func (p *HomePage) HideProfilesModal() {
	if p == nil {
		return
	}
	p.profilesModal = profilesModalState{}
	p.profilesModalTargets = p.profilesModalTargets[:0]
}

func (p *HomePage) ProfilesModalVisible() bool {
	return p != nil && p.profilesModal.Visible
}

func (p *HomePage) moveProfilesModalSelection(delta int) {
	if p == nil || len(p.model.ModelProfiles) == 0 || delta == 0 {
		return
	}
	p.profilesModal.Selected = (p.profilesModal.Selected + delta + len(p.model.ModelProfiles)) % len(p.model.ModelProfiles)
}

func (p *HomePage) handleProfilesModalKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.profilesModal.Visible {
		return
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideProfilesModal()
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveProfilesModalSelection(-1)
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveProfilesModalSelection(1)
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.applySelectedModelProfile()
	case ev.Key() == tcell.KeyRune && (ev.Rune() == 'e' || ev.Rune() == 'a'):
		p.openSelectedModelProfileEditor()
	}
}

func (p *HomePage) openSelectedModelProfileEditor() {
	if p == nil {
		return
	}
	profileID := ""
	if p.profilesModal.Selected >= 0 && p.profilesModal.Selected < len(p.model.ModelProfiles) {
		profileID = strings.TrimSpace(p.model.ModelProfiles[p.profilesModal.Selected].ProfileID)
	}
	p.HideProfilesModal()
	p.pendingHomeAction = &HomeAction{Kind: HomeActionOpenAgentsModal, ModelProfileID: profileID}
	p.statusLine = "opening favorite in agent setup..."
}

func (p *HomePage) applySelectedModelProfile() {
	if p == nil || p.profilesModal.Selected < 0 || p.profilesModal.Selected >= len(p.model.ModelProfiles) {
		return
	}
	profile := p.model.ModelProfiles[p.profilesModal.Selected]
	if !p.QueueSelectModelProfile(profile.ProfileID) {
		p.profilesModal.Status = "profile is unavailable"
		return
	}
	p.HideProfilesModal()
}

func (p *HomePage) handleProfilesModalMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil || !p.profilesModal.Visible {
		return false
	}
	x, y := ev.Position()
	if ev.Buttons()&tcell.WheelUp != 0 {
		p.moveProfilesModalSelection(-1)
		return true
	}
	if ev.Buttons()&tcell.WheelDown != 0 {
		p.moveProfilesModalSelection(1)
		return true
	}
	if ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	for _, target := range p.profilesModalTargets {
		if !target.Rect.Contains(x, y) {
			continue
		}
		switch target.Action {
		case "profile-row":
			p.profilesModal.Selected = target.Index
			p.applySelectedModelProfile()
		case "profile-edit":
			p.openSelectedModelProfileEditor()
		}
		return true
	}
	return true
}

func (p *HomePage) drawProfilesModal(s tcell.Screen) {
	p.profilesModalTargets = p.profilesModalTargets[:0]
	if p == nil || !p.profilesModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := minInt(72, w-4)
	modalH := minInt(maxInt(10, len(p.model.ModelProfiles)+7), h-4)
	if modalW < 36 || modalH < 8 {
		return
	}
	rect := Rect{X: (w - modalW) / 2, Y: (h - modalH) / 2, W: modalW, H: modalH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Model Profiles")
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, "Select the default model profile used by new sessions")

	rowY := rect.Y + 3
	availableRows := rect.H - 6
	if len(p.model.ModelProfiles) == 0 {
		DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.Warning, "No saved profiles. Open /agents to configure agent models.")
	} else {
		start := 0
		if p.profilesModal.Selected >= availableRows {
			start = p.profilesModal.Selected - availableRows + 1
		}
		for i := start; i < len(p.model.ModelProfiles) && rowY < rect.Y+rect.H-3; i++ {
			profile := p.model.ModelProfiles[i]
			prefix := "  "
			style := p.theme.Text
			if i == p.profilesModal.Selected {
				prefix = "> "
				style = p.theme.Primary.Bold(true)
			}
			markers := make([]string, 0, 2)
			if strings.TrimSpace(profile.ProfileID) == strings.TrimSpace(p.model.ActiveModelProfile.ProfileID) {
				markers = append(markers, "active")
			}
			if strings.TrimSpace(profile.ProfileID) == strings.TrimSpace(p.model.DefaultModelProfileID) {
				markers = append(markers, "default")
			}
			state := ""
			if len(markers) > 0 {
				state = "  [" + strings.Join(markers, ", ") + "]"
			}
			label := fmt.Sprintf("%s%s%s  %s", prefix, emptyValue(strings.TrimSpace(profile.Name), profile.ProfileID), state, modelProfileSummary(profile, p.sessionMode))
			DrawText(s, rect.X+2, rowY, rect.W-4, style, clampEllipsis(label, rect.W-4))
			p.profilesModalTargets = append(p.profilesModalTargets, clickTarget{Rect: Rect{X: rect.X + 2, Y: rowY, W: rect.W - 4, H: 1}, Action: "profile-row", Index: i})
			rowY++
		}
	}

	edit := "[ e: agent setup ]"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.Secondary, edit)
	p.profilesModalTargets = append(p.profilesModalTargets, clickTarget{Rect: Rect{X: rect.X + 2, Y: rect.Y + rect.H - 2, W: len([]rune(edit)), H: 1}, Action: "profile-edit"})
}

func modelProfileSummary(profile client.ModelProfile, sessionMode string) string {
	selection := profile.Single
	if strings.EqualFold(strings.TrimSpace(profile.ModelMode), "split") {
		if normalizeHomeSessionMode(sessionMode) == "plan" {
			selection = profile.Plan
		} else {
			selection = profile.Auto
		}
	}
	if selection == nil {
		return strings.TrimSpace(profile.ModelMode)
	}
	parts := []string{model.DisplayModelLabel(selection.Provider, selection.Model, selection.ServiceTier, selection.ContextMode)}
	if thinking := strings.TrimSpace(selection.Thinking); thinking != "" {
		parts = append(parts, "thinking "+thinking)
	}
	priority := strings.TrimSpace(selection.ServiceTier)
	if priority == "" || strings.EqualFold(priority, "standard") {
		priority = "off / standard"
	}
	parts = append(parts, "priority "+priority)
	return strings.Join(parts, " · ")
}

package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type VaultModalActionKind string

const (
	VaultModalActionEnable  VaultModalActionKind = "enable"
	VaultModalActionUnlock  VaultModalActionKind = "unlock"
	VaultModalActionLock    VaultModalActionKind = "lock"
	VaultModalActionDisable VaultModalActionKind = "disable"
)

type VaultModalAction struct {
	Kind     VaultModalActionKind
	Password string
}

type vaultModalMode string

const (
	vaultModalModeEnableWarning vaultModalMode = "enable_warning"
	vaultModalModeEnableForm    vaultModalMode = "enable_form"
	vaultModalModeUnlock        vaultModalMode = "unlock"
	vaultModalModeDisable       vaultModalMode = "disable"
	vaultModalModeStatus        vaultModalMode = "status"
)

type vaultModalState struct {
	Visible       bool
	Blocking      bool
	Loading       bool
	Mode          vaultModalMode
	Status        string
	Error         string
	Enabled       bool
	Unlocked      bool
	Password      string
	Confirm       string
	SelectedField int
	JustEnabled   bool
}

func (p *HomePage) ShowVaultSetupWarning() {
	p.vaultModal = vaultModalState{
		Visible: true,
		Mode:    vaultModalModeEnableWarning,
		Status:  "Vault mode encrypts saved provider credentials on disk. Swarm will keep the vault unlocked only until the app exits. Press Enter to continue.",
	}
}

func (p *HomePage) ShowVaultUnlockModal(blocking bool, status string) {
	p.vaultModal = vaultModalState{
		Visible:  true,
		Blocking: blocking,
		Mode:     vaultModalModeUnlock,
		Enabled:  true,
		Unlocked: false,
		Status:   strings.TrimSpace(status),
	}
	if strings.TrimSpace(p.vaultModal.Status) == "" {
		p.vaultModal.Status = "Enter your vault password to unlock saved provider credentials."
	}
}

func (p *HomePage) ShowVaultDisableModal() {
	p.vaultModal = vaultModalState{
		Visible:  true,
		Mode:     vaultModalModeDisable,
		Enabled:  true,
		Unlocked: true,
		Status:   "Enter your vault password to disable vault protection and return saved provider credentials to local plaintext storage.",
	}
}

func (p *HomePage) ShowVaultStatusModal() {
	p.vaultModal = vaultModalState{
		Visible:  true,
		Enabled:  true,
		Unlocked: true,
		Mode:     vaultModalModeStatus,
		Status:   "Vault is enabled and will stay unlocked until the app exits. Press e to export, i to import guidance, l to lock, or d to disable it.",
	}
}

func (p *HomePage) VaultModalVisible() bool {
	return p.vaultModal.Visible
}

func (p *HomePage) VaultUnlockModalActive() bool {
	return p.vaultModal.Visible && (p.vaultModal.Mode == vaultModalModeUnlock || p.vaultModal.Mode == vaultModalModeDisable)
}

func (p *HomePage) HideVaultModal() {
	if p.vaultModal.Blocking {
		return
	}
	p.DismissVaultModal()
}

func (p *HomePage) DismissVaultModal() {
	p.vaultModal = vaultModalState{}
	p.pendingVaultAction = nil
}

func (p *HomePage) SetVaultModalLoading(loading bool) {
	p.vaultModal.Loading = loading
}

func (p *HomePage) SetVaultModalError(err string) {
	p.vaultModal.Error = strings.TrimSpace(err)
	if p.vaultModal.Error != "" {
		p.vaultModal.Loading = false
	}
}

func (p *HomePage) SetVaultModalStatus(status string) {
	p.vaultModal.Status = strings.TrimSpace(status)
	if p.vaultModal.Status != "" {
		p.vaultModal.Error = ""
	}
}

func (p *HomePage) SetVaultEnabledState(enabled, unlocked bool) {
	p.vaultModal.Enabled = enabled
	p.vaultModal.Unlocked = unlocked
}

func (p *HomePage) PopVaultModalAction() (VaultModalAction, bool) {
	if p.pendingVaultAction == nil {
		return VaultModalAction{}, false
	}
	action := *p.pendingVaultAction
	p.pendingVaultAction = nil
	return action, true
}

func (p *HomePage) handleVaultModalKey(ev *tcell.EventKey) {
	switch p.vaultModal.Mode {
	case vaultModalModeEnableWarning:
		switch {
		case p.keybinds.Match(ev, KeybindModalClose):
			p.HideVaultModal()
		case p.keybinds.Match(ev, KeybindModalEnter):
			p.vaultModal.Mode = vaultModalModeEnableForm
			p.vaultModal.Status = "Enter a new vault password, press Tab, then confirm it and press Enter."
			p.vaultModal.Error = ""
			p.vaultModal.Password = ""
			p.vaultModal.Confirm = ""
			p.vaultModal.SelectedField = 0
		}
		return
	case vaultModalModeStatus:
		switch {
		case p.keybinds.Match(ev, KeybindModalClose):
			p.HideVaultModal()
		case ev.Key() == tcell.KeyRune && (ev.Rune() == 'e' || ev.Rune() == 'E'):
			p.HideVaultModal()
		case ev.Key() == tcell.KeyRune && (ev.Rune() == 'i' || ev.Rune() == 'I'):
			p.HideVaultModal()
		case ev.Key() == tcell.KeyRune && (ev.Rune() == 'l' || ev.Rune() == 'L'):
			p.pendingVaultAction = &VaultModalAction{Kind: VaultModalActionLock}
			p.vaultModal.Loading = true
		case ev.Key() == tcell.KeyRune && (ev.Rune() == 'd' || ev.Rune() == 'D'):
			p.ShowVaultDisableModal()
		}
		return
	}

	if ev.Key() == tcell.KeyRune && unicode.IsPrint(ev.Rune()) {
		if p.vaultModal.Mode == vaultModalModeEnableForm {
			if p.vaultModal.SelectedField == 0 {
				p.vaultModal.Password += string(ev.Rune())
			} else {
				p.vaultModal.Confirm += string(ev.Rune())
			}
		} else {
			p.vaultModal.Password += string(ev.Rune())
		}
		p.vaultModal.Error = ""
		return
	}

	if ev.Key() == tcell.KeyEscape || p.keybinds.Match(ev, KeybindModalClose) {
		p.HideVaultModal()
		return
	}
	if p.keybinds.Match(ev, KeybindModalFocusNext) || p.keybinds.Match(ev, KeybindModalFocusPrev) {
		if p.vaultModal.Mode == vaultModalModeEnableForm {
			p.vaultModal.SelectedField = (p.vaultModal.SelectedField + 1) % 2
		}
		return
	}
	if p.keybinds.Match(ev, KeybindModalSearchBackspace) {
		if p.vaultModal.Mode == vaultModalModeEnableForm {
			if p.vaultModal.SelectedField == 0 {
				p.vaultModal.Password = trimLastRune(p.vaultModal.Password)
			} else {
				p.vaultModal.Confirm = trimLastRune(p.vaultModal.Confirm)
			}
		} else {
			p.vaultModal.Password = trimLastRune(p.vaultModal.Password)
		}
		return
	}
	if p.keybinds.Match(ev, KeybindModalEnter) {
		switch p.vaultModal.Mode {
		case vaultModalModeUnlock, vaultModalModeDisable:
			if strings.TrimSpace(p.vaultModal.Password) == "" {
				p.vaultModal.Error = "Vault password is required."
				return
			}
			actionKind := VaultModalActionUnlock
			if p.vaultModal.Mode == vaultModalModeDisable {
				actionKind = VaultModalActionDisable
			}
			p.pendingVaultAction = &VaultModalAction{Kind: actionKind, Password: p.vaultModal.Password}
			p.vaultModal.Loading = true
		case vaultModalModeEnableForm:
			if p.vaultModal.Password == "" {
				p.vaultModal.Error = "Vault password is required."
				return
			}
			if p.vaultModal.Password != p.vaultModal.Confirm {
				p.vaultModal.Error = "Vault passwords do not match."
				return
			}
			p.pendingVaultAction = &VaultModalAction{Kind: VaultModalActionEnable, Password: p.vaultModal.Password}
			p.vaultModal.Loading = true
		}
		return
	}
}

func (p *HomePage) drawVaultModal(s tcell.Screen) {
	if !p.vaultModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 40 || h < 10 {
		return
	}
	modalW := minInt(76, w-6)
	if modalW < 54 {
		modalW = w - 2
	}
	if modalW < 40 {
		return
	}
	modalH := 15
	if p.vaultModal.Mode == vaultModalModeEnableForm {
		modalH = 18
	}
	if modalH > h-2 {
		modalH = h - 2
	}
	rect := Rect{
		X: maxInt(1, (w-modalW)/2),
		Y: maxInt(1, (h-modalH)/2),
		W: modalW,
		H: modalH,
	}
	FillRect(s, rect, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, rect, onPanel(p.theme.BorderActive))
	header := "Vault"
	if p.vaultModal.Blocking {
		header = "Vault Locked"
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, rect.W-4))

	statusY := rect.Y + 3
	bodyW := rect.W - 4
	for _, line := range Wrap(strings.TrimSpace(p.vaultModal.Status), bodyW) {
		DrawText(s, rect.X+2, statusY, bodyW, onPanel(p.theme.TextMuted), clampEllipsis(line, bodyW))
		statusY++
	}
	if errText := strings.TrimSpace(p.vaultModal.Error); errText != "" {
		DrawText(s, rect.X+2, statusY, bodyW, onPanel(p.theme.Error), clampEllipsis(errText, bodyW))
		statusY++
	}

	switch p.vaultModal.Mode {
	case vaultModalModeEnableWarning:
		DrawText(s, rect.X+2, rect.Y+rect.H-2, bodyW, onPanel(p.theme.TextMuted), clampEllipsis("Enter continue • Esc cancel", bodyW))
	case vaultModalModeEnableForm:
		drawVaultField(s, p, rect.X+2, statusY+1, bodyW, "Password", p.vaultModal.Password, p.vaultModal.SelectedField == 0)
		drawVaultField(s, p, rect.X+2, statusY+3, bodyW, "Confirm", p.vaultModal.Confirm, p.vaultModal.SelectedField == 1)
		DrawText(s, rect.X+2, rect.Y+rect.H-2, bodyW, onPanel(p.theme.TextMuted), clampEllipsis("Tab switch • Enter enable • Esc cancel", bodyW))
	case vaultModalModeUnlock, vaultModalModeDisable:
		drawVaultField(s, p, rect.X+2, statusY+1, bodyW, "Password", p.vaultModal.Password, true)
		help := "Enter unlock"
		if p.vaultModal.Mode == vaultModalModeDisable {
			help = "Enter disable • Esc cancel"
		} else if !p.vaultModal.Blocking {
			help = "Enter unlock • Esc cancel"
		}
		DrawText(s, rect.X+2, rect.Y+rect.H-2, bodyW, onPanel(p.theme.TextMuted), clampEllipsis(help, bodyW))
	case vaultModalModeStatus:
		help := "e export • i import • l lock • d disable • Esc close"
		if rect.W < 54 {
			help = "e export • i import • Esc close"
		}
		DrawText(s, rect.X+2, rect.Y+rect.H-2, bodyW, onPanel(p.theme.TextMuted), clampEllipsis(help, bodyW))
	}
}

func drawVaultField(s tcell.Screen, p *HomePage, x, y, width int, label, value string, selected bool) {
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawText(s, x, y, width, onPanel(p.theme.TextMuted), clampEllipsis(label, width))
	masked := strings.Repeat("•", utf8.RuneCountInString(value))
	fieldStyle := onPanel(p.theme.Text)
	if selected {
		fieldStyle = onPanel(p.theme.Primary)
	}
	DrawText(s, x, y+1, width, fieldStyle, clampEllipsis("> "+masked, width))
	if selected {
		cursorX := x + minInt(width-1, 2+utf8.RuneCountInString(masked))
		s.SetContent(cursorX, y+1, inputCursorRune, nil, onPanel(p.theme.Primary))
	}
}

func trimLastRune(value string) string {
	if value == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(value)
	if size <= 0 {
		return ""
	}
	return value[:len(value)-size]
}

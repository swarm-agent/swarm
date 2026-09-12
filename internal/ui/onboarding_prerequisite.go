package ui

import (
	"bytes"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// PrerequisiteActions separates explicit account creation from optional password
// setup. Password bytes are transient; neither callback may log them.
type PrerequisiteActions struct {
	Begin           func(string) (bool, error)
	Complete        func([]byte) error
	Status          func() string
	PasswordDone    func() bool
	SSHRequired     func() bool
	ChooseSSH       func(string, bool) error
	SSHGuidance     func(string) string
	Handoff         func() error
	Destination     func() string
	InitialUsername string
	InitialCreated  bool
	InitialResume   bool
}

type PrerequisiteScreen struct {
	Username     string
	Error        string
	Focus        int
	Busy         bool
	Progress     string
	Phase        string
	publicKey    string
	sshAddress   string
	sshNotice    string
	noticeOffset int
	pasting      bool
	password     []byte
	repeated     []byte
}

func (p *PrerequisiteScreen) clearPasswords() {
	clear(p.password)
	clear(p.repeated)
	p.password, p.repeated = nil, nil
}

func (p *PrerequisiteScreen) Key(ev *tcell.EventKey) (submit, cancel bool) {
	if p.Busy {
		return false, false
	}
	if ev.Key() == tcell.KeyCtrlC || ev.Key() == tcell.KeyEscape {
		p.clearPasswords()
		return false, true
	}
	if p.pasting && ev.Key() != tcell.KeyRune {
		// Bracketed paste is data, never an action. Preserve line breaks so the
		// authority rejects multi-record keys instead of silently joining them.
		if p.Phase == "ssh" && p.Focus == 0 && ev.Key() == tcell.KeyEnter && len(p.publicKey) < 4096 {
			p.publicKey += "\n"
		}
		return false, false
	}
	if p.Phase == "retry" {
		p.scrollGuidance(ev)
		return ev.Key() == tcell.KeyEnter, false
	}
	if p.Phase == "ssh-choice" {
		switch ev.Key() {
		case tcell.KeyTab, tcell.KeyDown, tcell.KeyBacktab, tcell.KeyUp:
			p.Focus = (p.Focus + 1) % 2
		case tcell.KeyEnter:
			if p.Focus == 0 {
				p.Phase, p.Focus, p.Error = "ssh", 0, ""
				return false, false
			}
			return true, false
		}
		return false, false
	}
	if p.Phase == "ssh" {
		switch ev.Key() {
		case tcell.KeyTab, tcell.KeyDown:
			p.Focus = (p.Focus + 1) % 5
		case tcell.KeyBacktab, tcell.KeyUp:
			p.Focus = (p.Focus + 4) % 5
		case tcell.KeyEnter:
			if p.Focus == 4 {
				return false, true
			}
			if p.Focus != 3 && strings.TrimSpace(p.publicKey) == "" {
				p.Error = "Paste one ssh-ed25519 public key or choose Skip."
				p.Focus = 0
				return
			}
			return true, false
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			target := &p.publicKey
			if p.Focus == 1 {
				target = &p.sshAddress
			}
			if p.Focus < 2 && len(*target) > 0 {
				*target = (*target)[:len(*target)-1]
			}
		case tcell.KeyRune:
			target, limit := &p.publicKey, 4096
			if p.Focus == 1 {
				target, limit = &p.sshAddress, 64
			}
			if p.Focus < 2 && ev.Rune() >= 32 && ev.Rune() <= 126 && len(*target) < limit {
				*target += string(ev.Rune())
			}
		}
		return
	}
	count := 3
	if p.Phase == "password" {
		count = 4
	}
	switch ev.Key() {
	case tcell.KeyTab, tcell.KeyDown:
		p.Focus = (p.Focus + 1) % count
	case tcell.KeyBacktab, tcell.KeyUp:
		p.Focus = (p.Focus + count - 1) % count
	case tcell.KeyEnter:
		if p.Focus == count-1 {
			p.clearPasswords()
			return false, true
		}
		switch p.Phase {
		case "choice":
			if p.Focus == 0 {
				p.Phase = "password"
				p.password, p.repeated = make([]byte, 0, 1024), make([]byte, 0, 1024)
				p.Focus = 0
				p.Error = ""
				return
			}
			p.clearPasswords()
			return true, false
		case "password":
			if p.Focus == 0 {
				p.Focus = 1
				return
			}
			if len(p.password) == 0 {
				p.Error = "Enter a password, or go back and skip."
				p.Focus = 0
				return
			}
			if !bytes.Equal(p.password, p.repeated) {
				p.Error = "Passwords do not match."
				p.Focus = 1
				return
			}
			return true, false
		default:
			if strings.TrimSpace(p.Username) == "" {
				p.Error = "Enter a username to continue."
				p.Focus = 0
				return
			}
			return true, false
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.Phase == "password" && p.Focus < 2 {
			target := &p.password
			if p.Focus == 1 {
				target = &p.repeated
			}
			if len(*target) > 0 {
				_, n := utf8.DecodeLastRune(*target)
				clear((*target)[len(*target)-n:])
				*target = (*target)[:len(*target)-n]
			}
		} else if p.Phase == "" && p.Focus == 0 && len(p.Username) > 0 {
			r := []rune(p.Username)
			p.Username = string(r[:len(r)-1])
		}
	case tcell.KeyCtrlB:
		if p.Phase == "password" {
			p.clearPasswords()
			p.Phase = "choice"
			p.Focus = 0
			p.Error = ""
		}
	case tcell.KeyRune:
		if !unicode.IsPrint(ev.Rune()) {
			return
		}
		if p.Phase == "password" && p.Focus < 2 {
			target := &p.password
			if p.Focus == 1 {
				target = &p.repeated
			}
			if len(*target)+utf8.RuneLen(ev.Rune()) <= 1024 {
				*target = utf8.AppendRune(*target, ev.Rune())
			}
		} else if p.Phase == "" && p.Focus == 0 && len(p.Username) < 32 {
			p.Username += string(ev.Rune())
		}
	}
	return
}

func (p *PrerequisiteScreen) Draw(s tcell.Screen) {
	theme := BuiltinThemeCatalog()[0].Theme
	w, h := s.Size()
	FillRect(s, Rect{W: w, H: h}, theme.Background)
	s.HideCursor()
	if w < 50 || h < 22 {
		DrawText(s, 1, 1, w-2, theme.Text, "Swarm setup: enlarge the terminal to 50 x 22.")
		return
	}
	bw, bh := minInt(88, w-4), 22
	x, y := (w-bw)/2, (h-bh)/2
	box := Rect{X: x, Y: y, W: bw, H: bh}
	FillRect(s, box, theme.Panel)
	DrawBox(s, box, theme.BorderActive)
	text := func(row int, value string) { DrawText(s, x+3, y+row, bw-6, theme.Text, value) }
	button := func(row, focus int, label string) {
		prefix := "  "
		if p.Focus == focus {
			prefix = "› "
		}
		text(row, prefix+"[ "+label+" ]")
	}
	field := func(row, focus int, label, value string) {
		text(row, label)
		border := theme.Border
		if p.Focus == focus {
			border = theme.BorderActive
		}
		DrawBox(s, Rect{X: x + 3, Y: y + row + 1, W: bw - 6, H: 3}, border)
		text(row+2, "  "+value)
	}
	text(1, "SWARM  ·  FIRST LAUNCH")
	text(2, "BEFORE STEP 1  ·  Device setup")
	switch p.Phase {
	case "ssh-choice":
		text(4, "Would you like to add an SSH key?")
		text(6, "Your account is ready: "+p.Username)
		text(8, "Add a public key for SSH access, or skip this step.")
		text(10, "Skipping leaves SSH keys unchanged.")
		button(13, 0, "Continue to add an SSH key")
		button(15, 1, "Skip and continue")
	case "ssh":
		text(4, "Optional SSH public key for "+p.Username)
		field(5, 0, "Public key: ssh-ed25519 only (never a private key)", p.publicKey)
		field(9, 1, "This machine's IP (optional, for connection guidance)", p.sshAddress)
		text(14, "Key installation does not verify SSH login or reachability.")
		button(15, 2, "Install key and start Swarm")
		button(16, 3, "Skip key and start Swarm")
		button(18, 4, "Exit")
	case "retry", "working":
		text(4, "Continue setup for "+p.Username)
		for i, line := range Wrap(p.Progress, bw-6) {
			if i < 3 {
				text(6+i, line)
			}
		}
		lines := Wrap(p.sshNotice, bw-6)
		p.noticeOffset = minInt(p.noticeOffset, maxInt(0, len(lines)-6))
		for i := 0; i < 6 && p.noticeOffset+i < len(lines); i++ {
			text(9+i, lines[p.noticeOffset+i])
		}
		if len(lines) > 6 {
			text(15, "↑/↓ scroll SSH guidance")
		}
		if !p.Busy {
			button(16, 0, "Retry current stage")
			text(18, "Account and password choice are preserved.")
		}
	case "choice":
		text(4, "Your account is ready: "+p.Username)
		text(6, "Would you like to set a login password?")
		text(8, "You can enter it here without leaving Swarm.")
		text(10, "Skip keeps password login disabled.")
		text(11, "It does not allow blank-password login.")
		button(13, 0, "Set a password")
		button(15, 1, "Skip and continue")
		button(18, 2, "Exit")
	case "password":
		text(4, "Set your login password")
		field(6, 0, "Password", strings.Repeat("•", minInt(utf8.RuneCount(p.password), bw-10)))
		field(10, 1, "Confirm password", strings.Repeat("•", minInt(utf8.RuneCount(p.repeated), bw-10)))
		text(15, "Ctrl+B goes back to the password choice.")
		button(18, 2, "Set password and continue")
		prefix := "  "
		if p.Focus == 3 {
			prefix = "› "
		}
		DrawText(s, x+bw-15, y+19, 12, theme.Text, prefix+"[ Exit ]")
	default:
		text(4, "Set up your user account.")
		for i, line := range Wrap("Swarm needs a non-root account to keep your work separate from system administration.", bw-6) {
			if i < 2 {
				text(6+i, line)
			}
		}
		value := p.Username
		if value == "" {
			value = "Choose your username"
		}
		field(9, 0, "Username", value)
		text(14, "Next: choose whether to set a password.")
		text(15, "No administrator access is granted.")
		text(16, "New name creates an account; existing name keeps credentials.")
		button(18, 1, "Create account and continue")
		prefix := "  "
		if p.Focus == 2 {
			prefix = "› "
		}
		DrawText(s, x+bw-15, y+19, 12, theme.Text, prefix+"[ Exit ]")
	}
	text(17, p.Error)
	if p.Busy {
		text(20, "Working… input is paused until this operation finishes.")
	} else {
		text(20, "Tab moves focus · Enter submits · Esc exits")
	}
}

// RunPrerequisite never relinquishes the terminal for password entry.
func RunPrerequisite(s tcell.Screen, actions PrerequisiteActions) error {
	p := &PrerequisiteScreen{}
	if actions.InitialResume {
		p.Username = actions.InitialUsername
		p.Phase = "choice"
		if !actions.InitialCreated || (actions.PasswordDone != nil && actions.PasswordDone()) {
			p.Phase = "retry"
			p.Progress = "Interrupted setup retained. Press Enter to resume."
			if actions.Status != nil {
				p.Progress = actions.Status()
			}
		}
	}
	if actions.SSHRequired != nil && actions.SSHRequired() {
		p.Phase = "ssh-choice"
		p.Focus = 0
	}
	defer p.clearPasswords()
	s.EnablePaste()
	defer s.DisablePaste()
	for {
		p.Draw(s)
		s.Show()
		event := s.PollEvent()
		if event == nil {
			return errors.New("setup terminal closed")
		}
		if paste, ok := event.(*tcell.EventPaste); ok {
			p.pasting = paste.Start()
			continue
		}
		key, ok := event.(*tcell.EventKey)
		if !ok {
			continue
		}
		submit, cancel := p.Key(key)
		if cancel {
			return nil
		}
		if !submit {
			continue
		}
		if p.Phase == "" {
			var created bool
			err := p.runAction(s, actions.Status, func() (err error) {
				created, err = actions.Begin(strings.TrimSpace(p.Username))
				return err
			})
			if err != nil {
				p.Error = err.Error()
				p.Focus = 0
				continue
			}
			p.Phase = "choice"
			p.Focus = 0
			p.Error = ""
			if created {
				continue
			}
			// Existing accounts retain their password and go straight to Identity.
		}
		if p.Phase == "ssh" || p.Phase == "ssh-choice" {
			skip := p.Phase == "ssh-choice" || p.Focus == 3
			key := p.publicKey
			if skip {
				key = ""
			}
			if err := p.runAction(s, actions.Status, func() error { return actions.ChooseSSH(key, skip) }); err != nil {
				p.Focus = 0
				p.Error = err.Error()
				continue
			}
			p.publicKey = ""
			p.Phase, p.Focus, p.Error = "working", 0, ""
			p.Progress = "SSH key skipped."
			if !skip {
				p.Progress = "SSH public key installed.\n"
				if actions.SSHGuidance != nil {
					p.Progress += actions.SSHGuidance(p.sshAddress)
				}
			}
			p.sshNotice = p.Progress
		}
		password := append([]byte(nil), p.password...)
		p.clearPasswords()
		p.Phase = "working"
		p.Error = ""
		p.Progress = "Preparing Swarm…"
		err := p.runAction(s, actions.Status, func() error {
			defer clear(password)
			return actions.Complete(password)
		})
		if actions.SSHRequired != nil && actions.SSHRequired() {
			p.Phase, p.Focus = "ssh-choice", 0
			if err != nil {
				p.Error = err.Error()
			}
			continue
		}
		if err == nil && actions.Handoff != nil {
			err = actions.Handoff()
		}
		if err == nil {
			return nil
		}
		p.Error = err.Error()
		p.Progress = err.Error()
		p.Phase = "choice"
		if actions.PasswordDone != nil && actions.PasswordDone() {
			p.Phase = "retry"
		}
		p.Focus = 0
	}
}

// runAction keeps progress visible and discards queued input at every mutation
// boundary, not just installation. Exit during a mutation cannot undo its effects.
func (p *PrerequisiteScreen) runAction(s tcell.Screen, status func() string, action func() error) error {
	p.Busy = true
	phase := p.Phase
	p.Phase = "working"
	defer func() { p.Busy = false; p.pasting = false; p.Phase = phase }()
	result := make(chan error, 1)
	go func() { result <- action() }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status != nil {
			p.Progress = status()
		}
		p.Draw(s)
		s.Show()
		select {
		case err := <-result:
			for s.HasPendingEvent() {
				event := s.PollEvent()
				if event == nil {
					break
				}
				if key, ok := event.(*tcell.EventKey); ok {
					p.scrollGuidance(key)
				}
			}
			return err
		case <-ticker.C:
			for s.HasPendingEvent() {
				event := s.PollEvent()
				if event == nil {
					break
				}
				if key, ok := event.(*tcell.EventKey); ok {
					p.scrollGuidance(key)
				}
			}
		}
	}
}

func (p *PrerequisiteScreen) scrollGuidance(ev *tcell.EventKey) {
	if ev.Key() == tcell.KeyDown && p.noticeOffset < len(p.sshNotice) {
		p.noticeOffset++
	}
	if ev.Key() == tcell.KeyUp && p.noticeOffset > 0 {
		p.noticeOffset--
	}
}

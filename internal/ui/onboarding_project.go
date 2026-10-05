package ui

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// Phase 3: Project Creation (Project-first onboarding)

func (p *HomePage) handleOnboardingProjectKey(ev *tcell.EventKey) {
	s := &p.onboarding
	switch ev.Key() {
	case tcell.KeyEscape:
		p.ShowOnboardingProvider("Provider configuration.")
		return
	case tcell.KeyTab, tcell.KeyDown:
		s.ProjectFocus = (s.ProjectFocus + 1) % 4
		s.Error = ""
		return
	case tcell.KeyBacktab, tcell.KeyUp:
		s.ProjectFocus = (s.ProjectFocus + 3) % 4
		s.Error = ""
		return
	case tcell.KeyEnter:
		if s.ProjectFocus == 0 {
			if strings.TrimSpace(s.ProjectName) == "" {
				s.Error = "Project name is required."
				return
			}
			s.ProjectFocus = 1
			s.Error = ""
			return
		}
		if s.ProjectFocus == 1 {
			s.ProjectFocus = 2
			s.Error = ""
			return
		}
		if s.ProjectFocus == 2 { // Open Project in Terminal
			name := strings.TrimSpace(s.ProjectName)
			if name == "" {
				name = "default"
			}
			s.Pending = true
			s.Error = ""
			s.Status = "Creating project and opening terminal..."
			p.pendingHomeAction = &HomeAction{
				Kind:               HomeActionCreateOnboardingProject,
				ProjectName:        name,
				ProjectDescription: strings.TrimSpace(s.ProjectDescription),
				AttachWorkspace:    false,
			}
			return
		}
		if s.ProjectFocus == 3 { // Attach Workspace Folder
			name := strings.TrimSpace(s.ProjectName)
			if name == "" {
				name = "default"
			}
			s.Pending = true
			s.Error = ""
			s.Status = "Creating project before workspace setup..."
			p.pendingHomeAction = &HomeAction{
				Kind:               HomeActionCreateOnboardingProject,
				ProjectName:        name,
				ProjectDescription: strings.TrimSpace(s.ProjectDescription),
				AttachWorkspace:    true,
			}
			return
		}
	}

	if s.ProjectFocus > 1 {
		return
	}

	field := &s.ProjectName
	if s.ProjectFocus == 1 {
		field = &s.ProjectDescription
	}

	switch ev.Key() {
	case tcell.KeyCtrlU:
		*field = ""
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		_, size := utf8.DecodeLastRuneInString(*field)
		if size > 0 {
			*field = (*field)[:len(*field)-size]
		}
	case tcell.KeyRune:
		if unicode.IsPrint(ev.Rune()) {
			*field += string(ev.Rune())
		}
	}
	s.Error = ""
}

func (p *HomePage) drawOnboardingProject(screen tcell.Screen, content Rect) {
	s := &p.onboarding
	fields := []struct {
		label string
		value string
		focus int
		hint  string
	}{
		{label: "Project name", value: s.ProjectName, focus: 0, hint: "Name your project (e.g. default, my-agent)"},
		{label: "Project instructions / goal (optional)", value: s.ProjectDescription, focus: 1, hint: "High-level goal or guidelines for the Orchestrator"},
	}

	y := content.Y + 1
	for _, field := range fields {
		DrawText(screen, content.X+1, y, content.W-2, p.theme.TextMuted, field.label)
		fieldRect := Rect{X: content.X, Y: y + 1, W: content.W, H: 3}
		border := p.theme.Border
		valueStyle := p.theme.Text
		if s.ProjectFocus == field.focus {
			border = p.theme.BorderActive
			valueStyle = p.theme.Primary
		}
		DrawBox(screen, fieldRect, border)
		value := field.value
		if value == "" {
			value = field.hint
			valueStyle = p.theme.TextMuted
		}
		DrawText(screen, fieldRect.X+2, fieldRect.Y+1, fieldRect.W-4, valueStyle, clampTail(value, fieldRect.W-4))
		y += 5
	}

	actions := []struct {
		label string
		focus int
	}{
		{label: "[ Open Project in Terminal (Enter) ]", focus: 2},
		{label: "[ Attach Workspace Folder (Optional) ]", focus: 3},
	}

	for i, act := range actions {
		style := p.theme.TextMuted
		prefix := "  "
		if s.ProjectFocus == act.focus {
			style = p.theme.Primary.Bold(true)
			prefix = "› "
		}
		DrawText(screen, content.X+1, y+i*2, content.W-2, style, prefix+act.label)
	}

	DrawText(screen, content.X+1, content.Y+content.H-1, content.W-2, p.theme.TextMuted, "Tab/↑/↓ move · Enter select/next · Esc back to provider")
}

// Workspace folder creation subdialog (Phase 4 helper)

func (p *HomePage) beginOnboardingProject() {
	s := &p.onboarding
	s.NamingProject = true
	s.ProjectField = 0
	s.SetupConsent = false
	s.ChoosingRepository = false
	s.Review = nil
	s.Repository = nil
	s.ConfirmOmissions = false
	s.BaselineAttempt = nil
	s.Error = ""
	s.Status = "A separate project folder is recommended. Name it, check the destination, then confirm setup."
}

func (p *HomePage) onboardingProjectDestination() string {
	s := &p.onboarding
	if strings.TrimSpace(s.ProjectName) == "" {
		return strings.TrimSpace(s.ProjectParent)
	}
	return filepath.Join(strings.TrimSpace(s.ProjectParent), strings.TrimSpace(s.ProjectName))
}

func (p *HomePage) handleOnboardingProjectFolderKey(ev *tcell.EventKey) {
	s := &p.onboarding
	switch ev.Key() {
	case tcell.KeyEscape:
		s.NamingProject = false
		s.WorkspacePath = ""
		s.ActionIndex = 0
		return
	case tcell.KeyTab, tcell.KeyDown:
		s.ProjectField = (s.ProjectField + 1) % 3
		return
	case tcell.KeyBacktab, tcell.KeyUp:
		s.ProjectField = (s.ProjectField + 2) % 3
		return
	case tcell.KeyEnter:
		if s.ProjectField < 2 {
			s.ProjectField++
			return
		}
		name := strings.TrimSpace(s.ProjectName)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			s.Error = "Enter a project name, not a path."
			s.ProjectField = 0
			return
		}
		if !filepath.IsAbs(strings.TrimSpace(s.ProjectParent)) {
			s.Error = "Enter an absolute parent location."
			s.ProjectField = 1
			return
		}
		s.WorkspacePath = p.onboardingProjectDestination()
		s.NamingProject = false
		s.ActionIndex = 0
		s.Pending = true
		s.Error = ""
		s.Status = "Inspecting destination before setup confirmation…"
		p.pendingHomeAction = &HomeAction{Kind: HomeActionInspectOnboardingRepository, WorkspacePath: s.WorkspacePath}
		return
	}
	if s.ProjectField > 1 {
		return
	}
	field := &s.ProjectName
	if s.ProjectField == 1 {
		field = &s.ProjectParent
	}
	switch ev.Key() {
	case tcell.KeyCtrlU:
		*field = ""
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		_, size := utf8.DecodeLastRuneInString(*field)
		if size > 0 {
			*field = (*field)[:len(*field)-size]
		}
	case tcell.KeyRune:
		if unicode.IsPrint(ev.Rune()) {
			*field += string(ev.Rune())
		}
	}
	s.Error = ""
}

func (p *HomePage) drawOnboardingProjectFolder(screen tcell.Screen, content Rect) {
	s := &p.onboarding
	rows := []string{"Project name: " + s.ProjectName, "Parent location: " + s.ProjectParent, "Continue to setup confirmation"}
	for i, text := range rows {
		prefix := "  "
		style := p.theme.Text
		if s.ProjectField == i {
			prefix = "› "
			style = p.theme.Primary
		}
		DrawText(screen, content.X, content.Y+1+i, content.W, style, clampEllipsis(prefix+text, content.W))
	}
	destination := p.onboardingProjectDestination()
	if destination == "" {
		destination = "Enter a project name"
	}
	DrawText(screen, content.X, content.Y+5, content.W, p.theme.TextMuted, clampTail("Destination: "+destination, content.W))
	DrawText(screen, content.X, content.Y+7, content.W, p.theme.TextMuted, "Tab fields · Enter next · Ctrl+U clear · Esc back (keeps draft)")
}

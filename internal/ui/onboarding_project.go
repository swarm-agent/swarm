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
	case tcell.KeyEnter:
		name := strings.TrimSpace(s.ProjectName)
		if name == "" {
			s.Error = "Project name is required. Please enter a name for your project."
			return
		}
		s.ProjectName = name
		p.ShowOnboardingWorkspace("")
		return
	case tcell.KeyCtrlU:
		s.ProjectName = ""
		s.Error = ""
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		_, size := utf8.DecodeLastRuneInString(s.ProjectName)
		if size > 0 {
			s.ProjectName = s.ProjectName[:len(s.ProjectName)-size]
		}
		s.Error = ""
		return
	case tcell.KeyRune:
		if unicode.IsPrint(ev.Rune()) {
			s.ProjectName += string(ev.Rune())
		}
		s.Error = ""
		return
	}
}

func (p *HomePage) drawOnboardingProject(screen tcell.Screen, content Rect) {
	s := &p.onboarding
	y := content.Y + 2
	DrawText(screen, content.X+1, y, content.W-2, p.theme.TextMuted, "Project name")
	fieldRect := Rect{X: content.X, Y: y + 1, W: content.W, H: 3}
	DrawBox(screen, fieldRect, p.theme.BorderActive)
	value := s.ProjectName
	valueStyle := p.theme.Primary
	if value == "" {
		value = "Type project name..."
		valueStyle = p.theme.TextMuted
	}
	DrawText(screen, fieldRect.X+2, fieldRect.Y+1, fieldRect.W-4, valueStyle, clampTail(value, fieldRect.W-4))

	DrawText(screen, content.X+1, y+5, content.W-2, p.theme.Primary.Bold(true), "› [ Continue to Workspaces (Enter) ]")
	DrawText(screen, content.X+1, content.Y+content.H-1, content.W-2, p.theme.TextMuted, "Enter continue to workspaces · Esc back to provider")
}

// Legacy helper preserved for compatibility
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
		destination = "Enter a folder name"
	}
	DrawText(screen, content.X, content.Y+5, content.W, p.theme.TextMuted, clampTail("Destination: "+destination, content.W))
	DrawText(screen, content.X, content.Y+7, content.W, p.theme.TextMuted, "Tab fields · Enter next · Ctrl+U clear · Esc back (keeps draft)")
}

package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

type onboardingPhase int

const (
	onboardingPhaseIdentity onboardingPhase = iota
	onboardingPhaseProvider
	onboardingPhaseProject
	onboardingPhaseWorkspace
)

type onboardingFocus int

const (
	onboardingFocusUsername onboardingFocus = iota
	onboardingFocusSwarmName
	onboardingFocusContinue
	onboardingFocusCancel
)

type onboardingState struct {
	Visible            bool
	Locked             bool
	Phase              onboardingPhase
	Focus              onboardingFocus
	Status             string
	Error              string
	Pending            bool
	Personalizing      bool
	PreFinish          bool
	PreFinishTicks     int
	PreFinishProjectID string
	PreFinishProjectName string
	PreFinishWorkspaces []string
	Tick               int
	AddingWorkspaces   bool
	CreatingFolder     bool
	NewFolderPath      string
	WorkspacePath      string
	WorkspaceReady     bool
	SetupConsent       bool
	EditingPath        bool
	RuntimeAccount     string
	HomePath           string
	ProjectNamed       bool
	ProjectName        string
	ProjectParent      string
	ProjectDescription string
	ProjectFocus       int
	NamingProject      bool
	ProjectField       int
	PreviousPath       string
	ActionIndex        int
	ChoosingRepository bool
	Repositories       []client.WorkspaceDiscoverEntry
	Repository         *client.OnboardingRepository
	Review             *client.OnboardingReview
	Selected           map[string]bool
	ConfirmOmissions   bool
	BaselineAttempt    *client.OnboardingBaseline
}

func (p *HomePage) SetOnboardingRequired(required bool, username, swarmName string) {
	if p == nil {
		return
	}
	p.model.OnboardingRequired = required
	p.model.OnboardingUsername = strings.TrimSpace(username)
	p.model.OnboardingSwarmName = strings.TrimSpace(swarmName)
	if required {
		p.ShowOnboardingLocked("Complete required setup before using Swarm.")
	}
}

func (p *HomePage) ShowOnboardingLocked(status string) {
	if p == nil {
		return
	}
	wasVisible := p.onboarding.Visible
	p.onboarding.Visible = true
	p.onboarding.Locked = true
	if !wasVisible {
		p.onboarding.Phase = onboardingPhaseIdentity
		if p.model.OnboardingIdentityBootstrapped && p.model.OnboardingUsername != "" {
			p.onboarding.Phase = onboardingPhaseProvider
		}
		p.onboarding.Focus = onboardingFocusUsername
		if p.model.OnboardingUsername != "" && p.model.OnboardingSwarmName == "" {
			p.onboarding.Focus = onboardingFocusSwarmName
		}
	}
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) OnboardingVisible() bool {
	return p != nil && p.onboarding.Visible
}

func (p *HomePage) OnboardingProviderActive() bool {
	return p != nil && p.onboarding.Visible && p.onboarding.Phase == onboardingPhaseProvider
}

func (p *HomePage) OnboardingProjectActive() bool {
	return p != nil && p.onboarding.Visible && (p.onboarding.Phase == onboardingPhaseProject || p.onboarding.NamingProject)
}

func (p *HomePage) OnboardingProjectName() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.onboarding.ProjectName)
}

func (p *HomePage) OnboardingWorkspaceActive() bool {
	return p != nil && p.onboarding.Visible && p.onboarding.Phase == onboardingPhaseWorkspace
}

// Guidance is an offer, never authority to replace a selected workspace or create files.
func (p *HomePage) SetOnboardingWorkspaceGuidance(account, home string) {
	p.onboarding.RuntimeAccount = strings.TrimSpace(account)
	p.onboarding.HomePath = strings.TrimSpace(home)
	if p.onboarding.ProjectParent == "" {
		p.onboarding.ProjectParent = p.onboarding.HomePath
	}
}

func (p *HomePage) OnboardingWorkspacePath() string {
	if p.onboarding.NamingProject {
		return p.onboardingProjectDestination()
	}
	if p.onboarding.WorkspacePath != "" {
		return p.onboarding.WorkspacePath
	}
	if !p.model.WorkspaceSetupHasGit && p.model.CWD != "" {
		return strings.TrimSpace(p.model.CWD)
	}
	return ""
}

func (p *HomePage) SetOnboardingWorkspacePath(path string) {
	if p == nil || strings.TrimSpace(path) == "" {
		return
	}
	p.onboarding.WorkspacePath = strings.TrimSpace(path)
}

func (p *HomePage) SetOnboardingWorkspaceGitReadiness(path string, hasGit bool, readiness model.GitReadiness) {
	if p == nil || strings.TrimSpace(path) == "" {
		return
	}
	p.onboarding.WorkspacePath = strings.TrimSpace(path)
	p.model.WorkspaceSetupPath = strings.TrimSpace(path)
	p.model.WorkspaceSetupHasGit = hasGit
	p.model.WorkspaceSetupGitReadiness = readiness
}

func (p *HomePage) ShowOnboardingProvider(status string) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	p.onboarding.Phase = onboardingPhaseProvider
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	p.authModal.Focus = authModalFocusProviders
	p.authModal.reconcileSelections()
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) ShowOnboardingProject(status string) {
	if p == nil {
		return
	}
	p.onboarding.Visible = true
	p.authModal.Editor = nil
	p.authModal.Login = nil
	p.authModal.Loading = false
	p.onboarding.Phase = onboardingPhaseProject
	p.onboarding.ProjectFocus = 0
	p.onboarding.ActionIndex = 0
	p.onboarding.ProjectNamed = false
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) ShowOnboardingWorkspace(status string) {
	if p == nil {
		return
	}
	p.onboarding.Visible = true
	p.authModal.Editor = nil
	p.authModal.Login = nil
	p.authModal.Loading = false
	p.onboarding.Phase = onboardingPhaseWorkspace
	p.onboarding.ActionIndex = 0
	p.onboarding.Pending = false
	p.onboarding.Personalizing = false
	p.onboarding.PreFinish = false
	p.onboarding.AddingWorkspaces = false
	p.onboarding.CreatingFolder = false
	p.onboarding.Error = ""
	if strings.TrimSpace(status) != "" {
		p.onboarding.Status = strings.TrimSpace(status)
	}
}

func (p *HomePage) ShowOnboardingPreFinish(projectID, projectName string, workspaces []string) {
	if p == nil {
		return
	}
	s := &p.onboarding
	s.Visible = true
	s.Phase = onboardingPhaseWorkspace
	s.Personalizing = false
	s.PreFinish = true
	s.PreFinishProjectID = strings.TrimSpace(projectID)
	s.PreFinishProjectName = strings.TrimSpace(projectName)
	if s.PreFinishProjectName == "" {
		s.PreFinishProjectName = strings.TrimSpace(s.ProjectName)
	}
	s.PreFinishWorkspaces = workspaces
	s.PreFinishTicks = 0
	s.Pending = false
	s.Error = ""
	s.Status = ""
	s.ActionIndex = 0
}

func (p *HomePage) OnboardingPreFinishActive() bool {
	return p != nil && p.onboarding.Visible && p.onboarding.PreFinish
}

func (p *HomePage) FinishOnboardingPreFinish() {
	if p == nil {
		return
	}
	s := &p.onboarding
	projID := strings.TrimSpace(s.PreFinishProjectID)
	projName := strings.TrimSpace(s.PreFinishProjectName)
	if projName == "" {
		projName = strings.TrimSpace(s.ProjectName)
	}
	p.pendingHomeAction = &HomeAction{
		Kind:        HomeActionFinishOnboardingProject,
		ProjectID:   projID,
		ProjectName: projName,
	}
}

func (p *HomePage) CompleteOnboardingWorkspace() {
	if p == nil {
		return
	}
	p.onboarding.WorkspaceReady = true
	p.onboarding.Pending = false
	p.onboarding = onboardingState{}
	p.model.OnboardingRequired = false
}

func (p *HomePage) HideOnboarding() {
	if p == nil {
		return
	}
	if p.onboarding.Locked && !p.onboarding.WorkspaceReady {
		return
	}
	p.onboarding = onboardingState{}
}

func (p *HomePage) SetOnboardingStatus(status string) {
	if p == nil {
		return
	}
	p.onboarding.Status = strings.TrimSpace(status)
	p.onboarding.Error = ""
}

func (p *HomePage) SetOnboardingError(message string) {
	if p == nil {
		return
	}
	p.onboarding.Pending = false
	p.onboarding.SetupConsent = false
	p.onboarding.Error = strings.TrimSpace(message)
}

func (p *HomePage) handleOnboardingKey(ev *tcell.EventKey) {
	if p == nil || ev == nil || !p.onboarding.Visible || p.onboarding.Pending {
		return
	}
	switch p.onboarding.Phase {
	case onboardingPhaseProvider:
		p.handleOnboardingProviderKey(ev)
	case onboardingPhaseProject:
		p.handleOnboardingProjectKey(ev)
	case onboardingPhaseWorkspace:
		p.handleOnboardingWorkspaceKey(ev)
	default:
		p.handleOnboardingIdentityKey(ev)
	}
}

func (p *HomePage) handleOnboardingIdentityKey(ev *tcell.EventKey) {
	if ev.Key() == tcell.KeyEnter && p.onboarding.Focus == onboardingFocusCancel {
		p.pendingHomeAction = &HomeAction{Kind: HomeActionKind("exit-onboarding")}
		return
	}
	switch {
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown):
		p.advanceOnboardingFocus(1)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp):
		p.advanceOnboardingFocus(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		p.deleteOnboardingRune()
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		p.clearOnboardingField()
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if p.onboarding.Focus == onboardingFocusCancel {
			p.pendingHomeAction = &HomeAction{Kind: HomeActionKind("exit-onboarding")}
			return
		}
		if p.onboarding.Focus == onboardingFocusUsername && strings.TrimSpace(p.model.OnboardingUsername) != "" {
			p.onboarding.Focus = onboardingFocusSwarmName
			p.onboarding.Error = ""
			return
		}
		p.submitOnboardingIdentity()
		return
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.onboarding.Error = "Enter your username to continue."
		return
	}
	if p.onboarding.Focus > onboardingFocusSwarmName || ev.Key() != tcell.KeyRune || !unicode.IsPrint(ev.Rune()) {
		return
	}
	if p.onboarding.Focus == onboardingFocusUsername {
		p.model.OnboardingUsername += string(ev.Rune())
	} else {
		p.model.OnboardingSwarmName += string(ev.Rune())
	}
	p.onboarding.Error = ""
}

func (p *HomePage) handleOnboardingProviderKey(ev *tcell.EventKey) {
	if p.authModal.Loading {
		return
	}
	if p.authModal.Editor != nil {
		if ev.Key() == tcell.KeyEscape {
			p.authModal.Editor = nil
			p.authModal.Status = "Editor closed"
			return
		}
		if ev.Key() == tcell.KeyCtrlS {
			p.authModal.Editor = nil
			p.ShowOnboardingProject("Provider skipped. Enter a project name to continue.")
			return
		}
		p.handleAuthModalEditorKey(ev)
		return
	}
	if ev.Key() == tcell.KeyTab {
		p.onboarding.ActionIndex = (p.onboarding.ActionIndex + 1) % 3
		return
	}
	if ev.Key() == tcell.KeyBacktab {
		p.onboarding.ActionIndex = (p.onboarding.ActionIndex + 2) % 3
		return
	}
	if (ev.Key() == tcell.KeyRune && (ev.Rune() == 's' || ev.Rune() == 'S')) || ev.Key() == tcell.KeyEscape {
		p.ShowOnboardingProject("Provider skipped. Enter a project name to continue.")
		return
	}
	if p.onboarding.ActionIndex == 1 { // Skip for now
		switch {
		case ev.Key() == tcell.KeyDown:
			p.onboarding.ActionIndex = 2
			return
		case ev.Key() == tcell.KeyUp:
			p.onboarding.ActionIndex = 0
			return
		case ev.Key() == tcell.KeyEnter:
			p.ShowOnboardingProject("Provider skipped. Enter a project name to continue.")
			p.onboarding.ActionIndex = 0
			return
		}
	}
	if p.onboarding.ActionIndex == 2 { // Cancel / Exit
		switch {
		case ev.Key() == tcell.KeyUp:
			p.onboarding.ActionIndex = 1
			return
		case ev.Key() == tcell.KeyEnter:
			p.pendingHomeAction = &HomeAction{Kind: HomeActionKind("exit-onboarding")}
			return
		}
	}
	// ActionIndex == 0 (In provider list)
	switch {
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown), ev.Key() == tcell.KeyRight:
		p.authModal.Focus = authModalFocusProviders
		if len(p.authModal.Providers) > 0 && p.authModal.SelectedProvider >= len(p.authModal.Providers)-1 {
			p.onboarding.ActionIndex = 1
			return
		}
		p.moveAuthModalSelection(1)
		p.onboarding.Error = ""
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp), ev.Key() == tcell.KeyLeft:
		p.authModal.Focus = authModalFocusProviders
		p.moveAuthModalSelection(-1)
		p.onboarding.Error = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		providerID := p.selectedAuthProviderID()
		if providerID == "" {
			p.onboarding.Error = "No provider is available yet. Press s to continue without one."
			return
		}
		if provider, ok := p.selectedAuthProvider(); ok && provider.Ready && provider.Runnable {
			p.ShowOnboardingProject("Connected provider selected. Enter a project name to continue.")
			return
		}
		p.triggerProviderLogin(providerID)
		return
	}
}

func (p *HomePage) handleOnboardingWorkspaceShortcut(ev *tcell.EventKey) {
	if p.onboarding.EditingPath {
		switch ev.Key() {
		case tcell.KeyEscape:
			p.onboarding.WorkspacePath = p.onboarding.PreviousPath
			p.onboarding.EditingPath = false
		case tcell.KeyEnter:
			p.onboarding.EditingPath = false
			p.onboarding.Repository = nil
			p.onboarding.Review = nil
			p.onboarding.ActionIndex = 0
			if strings.TrimSpace(p.onboarding.WorkspacePath) == "" {
				p.onboarding.EditingPath = true
				p.onboarding.Error = "Enter a folder path."
				return
			}
			p.onboarding.Pending = true
			p.pendingHomeAction = &HomeAction{Kind: HomeActionInspectOnboardingRepository, WorkspacePath: p.onboarding.WorkspacePath}
			p.onboarding.Status = "Verifying selected folder…"
		case tcell.KeyCtrlU:
			p.onboarding.WorkspacePath = ""
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			_, size := utf8.DecodeLastRuneInString(p.onboarding.WorkspacePath)
			if size > 0 {
				p.onboarding.WorkspacePath = p.onboarding.WorkspacePath[:len(p.onboarding.WorkspacePath)-size]
			}
		case tcell.KeyRune:
			if unicode.IsPrint(ev.Rune()) {
				p.onboarding.WorkspacePath += string(ev.Rune())
			}
		}
		p.onboarding.SetupConsent = false
		p.onboarding.Error = ""
		p.model.WorkspaceSetupGitReadiness = model.GitReadinessUnknown
		return
	}
	if ev.Key() == tcell.KeyCtrlS || ev.Key() == tcell.KeyCtrlN {
		p.beginOnboardingProject()
		return
	}
	if ev.Key() == tcell.KeyCtrlL {
		p.onboarding.ChoosingRepository = false
		p.onboarding.ConfirmOmissions = false
		p.onboarding.Review = nil
		p.onboarding.BaselineAttempt = nil
		p.onboarding.PreviousPath = p.onboarding.WorkspacePath
		p.onboarding.EditingPath = true
		p.onboarding.SetupConsent = false
		p.onboarding.Error = ""
		p.onboarding.Status = "Edit location: Ctrl+U clear, Enter select and verify, Esc cancel."
		return
	}
}

func (p *HomePage) advanceOnboardingFocus(delta int) {
	if delta == 0 {
		return
	}
	order := []onboardingFocus{onboardingFocusUsername, onboardingFocusSwarmName, onboardingFocusContinue, onboardingFocusCancel}
	current := 0
	for i, f := range order {
		if p.onboarding.Focus == f {
			current = i
			break
		}
	}
	next := (current + delta) % len(order)
	if next < 0 {
		next += len(order)
	}
	p.onboarding.Focus = order[next]
	p.onboarding.Error = ""
}

func (p *HomePage) deleteOnboardingRune() {
	field := p.onboardingField()
	if field == nil || len(*field) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(*field)
	if size > 0 {
		*field = (*field)[:len(*field)-size]
	}
}

func (p *HomePage) clearOnboardingField() {
	if field := p.onboardingField(); field != nil {
		*field = ""
	}
}

func (p *HomePage) onboardingField() *string {
	if p.onboarding.Focus > onboardingFocusSwarmName {
		return nil
	}
	if p.onboarding.Focus == onboardingFocusUsername {
		return &p.model.OnboardingUsername
	}
	return &p.model.OnboardingSwarmName
}

func (p *HomePage) identityOnboardingComplete() bool {
	return strings.TrimSpace(p.model.OnboardingUsername) != ""
}

func (p *HomePage) submitOnboardingIdentity() {
	user := strings.TrimSpace(p.model.OnboardingUsername)
	if user == "" {
		p.onboarding.Focus = onboardingFocusUsername
		p.onboarding.Error = "Your username is required."
		return
	}
	swarm := strings.TrimSpace(p.model.OnboardingSwarmName)
	if swarm == "" {
		swarm = "default"
	}
	p.pendingHomeAction = &HomeAction{
		Kind:      HomeActionSaveOnboarding,
		Username:  user,
		SwarmName: swarm,
	}
	p.onboarding.Pending = true
	p.onboarding.Status = "Creating account and saving settings…"
	p.onboarding.Error = ""
}

func (p *HomePage) drawOnboarding(s tcell.Screen) {
	if p == nil || !p.onboarding.Visible {
		return
	}
	w, h := s.Size()
	boxW := minInt(92, w-4)
	if boxW < 48 {
		boxW = w - 2
	}
	boxH := minInt(24, h-2)
	if boxW <= 8 || boxH <= 10 {
		return
	}
	rect := Rect{X: (w - boxW) / 2, Y: (h - boxH) / 2, W: boxW, H: boxH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	p.drawOnboardingHeader(s, rect)

	content := Rect{X: rect.X + 3, Y: rect.Y + 6, W: rect.W - 6, H: rect.H - 10}
	switch p.onboarding.Phase {
	case onboardingPhaseProvider:
		p.drawOnboardingProvider(s, content)
	case onboardingPhaseProject:
		p.drawOnboardingProject(s, content)
	case onboardingPhaseWorkspace:
		p.drawOnboardingWorkspace(s, content)
	default:
		p.drawOnboardingIdentity(s, content)
	}

	status := strings.TrimSpace(p.onboarding.Status)
	statusStyle := p.theme.TextMuted
	if p.onboarding.Phase == onboardingPhaseProvider {
		if authStatus := strings.TrimSpace(p.authModal.Status); authStatus != "" {
			status = authStatus
		}
		if authError := strings.TrimSpace(p.authModal.Error); authError != "" {
			status = authError
			statusStyle = p.theme.Error
		}
	}
	if errText := strings.TrimSpace(p.onboarding.Error); errText != "" {
		status = errText
		statusStyle = p.theme.Error
	}
	if status != "" {
		lines := Wrap(status, rect.W-6)
		for i, line := range lines {
			if i >= 2 {
				break
			}
			DrawText(s, rect.X+3, rect.Y+rect.H-4+i, rect.W-6, statusStyle, line)
		}
	}
	help := "Ctrl+C exit • Tab/↑/↓ move • Enter continue"
	if p.onboarding.Phase == onboardingPhaseProvider {
		help = "Ctrl+C exit • ←/→ select • Enter connect • s/Esc skip"
	} else if p.onboarding.Phase == onboardingPhaseProject {
		help = "Enter continue to workspaces · Esc back to provider"
	} else if p.onboarding.Phase == onboardingPhaseWorkspace {
		if p.onboarding.PreFinish {
			help = "Enter launch project · Space launch · Esc back"
		} else if p.onboarding.Personalizing {
			help = "Personalizing your Project.. Please wait while AI Router synthesizes context"
		} else if p.onboarding.CreatingFolder {
			help = "Type folder path · Enter create · Esc cancel"
		} else if p.onboarding.AddingWorkspaces {
			help = "Space/Enter toggle · Enter on Finish to submit · Esc back · s skip"
		} else if strings.TrimSpace(p.onboarding.ProjectName) != "" {
			help = "Enter choose · ↑/↓ navigate · Esc back to project name"
		} else {
			help = "Tab/↑/↓ choose · Enter activate · Esc back · s skip"
			if p.onboarding.EditingPath {
				help = "Type path · Ctrl+U clear · Enter select · Esc cancel"
			}
		}
	}
	DrawText(s, rect.X+3, rect.Y+rect.H-2, rect.W-6, p.theme.TextMuted, clampEllipsis(help, rect.W-6))
}

func (p *HomePage) drawOnboardingHeader(s tcell.Screen, rect Rect) {
	step := int(p.onboarding.Phase) + 1
	if step > 4 {
		step = 4
	}
	labels := []string{"Identity", "Provider", "Project", "Workspace (optional)"}
	if p.onboarding.PreFinish {
		labels[3] = "Ready"
	}
	DrawText(s, rect.X+3, rect.Y+1, rect.W-6, p.theme.Text, "SWARM  ·  FIRST LAUNCH")
	DrawText(s, rect.X+3, rect.Y+2, rect.W-6, p.theme.TextMuted, fmt.Sprintf("STEP %d OF 4  ·  %s", step, labels[step-1]))
	barW := maxInt(3, (rect.W-12)/4)
	for i := 0; i < 4; i++ {
		style := p.theme.Border
		marker := strings.Repeat("─", barW)
		if i == step-1 || (p.onboarding.PreFinish && i <= 3) {
			style = p.theme.Primary
			marker = strings.Repeat("━", barW)
		}
		DrawText(s, rect.X+3+i*(barW+1), rect.Y+3, barW, style, marker)
	}
	step4Title := "Attach a workspace folder."
	step4Subtitle := "Optional: attach a folder now, or let Swarm Orchestrator manage workspaces."
	if p.onboarding.PreFinish {
		step4Title = "All systems online. Project ready to launch!"
		step4Subtitle = "Swarm is primed with your project context and ready for instructions."
	} else if strings.TrimSpace(p.onboarding.ProjectName) != "" {
		step4Title = fmt.Sprintf("Add workspaces into %s?", strings.TrimSpace(p.onboarding.ProjectName))
		step4Subtitle = "Add workspaces to your project or skip straight to Swarm."
	}
	titles := []string{"Name your Swarm.", "Connect your AI provider.", "Create your first project.", step4Title}
	subtitles := []string{
		"Start with your name and the name of this Swarm.",
		"Connect now, or skip ahead. Provider can be added later.",
		"Projects organize your chats, plans, and tasks. Enter a name to get started.",
		step4Subtitle,
	}
	DrawText(s, rect.X+3, rect.Y+4, rect.W-6, p.theme.Text, titles[step-1])
	DrawText(s, rect.X+3, rect.Y+5, rect.W-6, p.theme.TextMuted, clampEllipsis(subtitles[step-1], rect.W-6))
}

func (p *HomePage) drawOnboardingIdentity(s tcell.Screen, content Rect) {
	fields := []struct {
		label string
		value string
		focus onboardingFocus
	}{
		{label: "Your name", value: p.model.OnboardingUsername, focus: onboardingFocusUsername},
		{label: "Swarm name (optional)", value: p.model.OnboardingSwarmName, focus: onboardingFocusSwarmName},
	}
	y := content.Y + 1
	for _, field := range fields {
		DrawText(s, content.X+1, y, content.W-2, p.theme.TextMuted, field.label)
		fieldRect := Rect{X: content.X, Y: y + 1, W: content.W, H: 3}
		border := p.theme.Border
		valueStyle := p.theme.Text
		if p.onboarding.Focus == field.focus {
			border = p.theme.BorderActive
			valueStyle = p.theme.Primary
		}
		DrawBox(s, fieldRect, border)
		value := field.value
		if value == "" {
			value = "Type here"
			if field.focus == onboardingFocusSwarmName {
				value = "default (optional)"
			}
			valueStyle = p.theme.TextMuted
		}
		DrawText(s, fieldRect.X+2, fieldRect.Y+1, fieldRect.W-4, valueStyle, clampTail(value, fieldRect.W-4))
		y += 5
	}

	label := "[ Continue (Enter) ]   [ Cancel / Exit ]"
	if p.onboarding.Focus == onboardingFocusContinue {
		label = "› [ Continue (Enter) ]   [ Cancel / Exit ]"
	}
	if p.onboarding.Focus == onboardingFocusCancel {
		label = "[ Continue (Enter) ]   › [ Cancel / Exit ]"
	}
	DrawText(s, content.X, content.Y+content.H-1, content.W, p.theme.Primary, label)
}

func (p *HomePage) drawOnboardingProvider(s tcell.Screen, content Rect) {
	if p.authModal.Editor != nil {
		p.drawAuthModalEditor(s, Rect{X: content.X - 2, Y: content.Y - 1, W: content.W + 4, H: content.H + 3})
		return
	}
	providers := p.authModal.Providers
	if len(providers) == 0 {
		DrawText(s, content.X, content.Y+2, content.W, p.theme.TextMuted, "No providers loaded yet.")
		label := "Skip provider"
		if p.onboarding.ActionIndex == 1 {
			label = "› " + label
		}
		DrawText(s, content.X, content.Y+4, content.W, p.theme.Warning, label+" (Tab to focus, Enter to activate)")
		DrawText(s, content.X, content.Y+5, content.W, p.theme.Text, "Cancel / Exit (Tab to focus, Enter)")
		return
	}
	labels := []string{"Connect selected provider", "Skip provider", "Cancel / Exit"}
	if provider, ok := p.selectedAuthProvider(); ok && provider.Ready && provider.Runnable {
		labels[0] = "Use connected provider and continue"
	}
	for i, label := range labels {
		prefix := "  "
		if p.onboarding.ActionIndex == i {
			prefix = "› "
		}
		DrawText(s, content.X, content.Y+content.H-3+i, content.W, p.theme.Primary, prefix+label)
	}
	selected := p.authModal.SelectedProvider
	if selected < 0 || selected >= len(providers) {
		selected = 0
	}

	const columns = 2
	const gutter = 2
	const cardHeight = 3
	const rowGap = 1
	cardW := (content.W - gutter) / columns
	maxRows := maxInt(1, (content.H-4)/(cardHeight+rowGap))
	totalRows := (len(providers) + columns - 1) / columns
	selectedRow := selected / columns
	startRow := maxInt(0, selectedRow-maxRows/2)
	startRow = minInt(startRow, maxInt(0, totalRows-maxRows))
	start := startRow * columns
	end := minInt(len(providers), start+maxRows*columns)

	for i := start; i < end; i++ {
		visibleIndex := i - start
		row := visibleIndex / columns
		column := visibleIndex % columns
		provider := providers[i]
		card := Rect{
			X: content.X + column*(cardW+gutter),
			Y: content.Y + 1 + row*(cardHeight+rowGap),
			W: cardW,
			H: cardHeight,
		}
		style := p.theme.Border
		textStyle := p.theme.Text
		prefix := "  "
		if i == selected {
			style = p.theme.BorderActive
			textStyle = p.theme.Primary
			prefix = "› "
		}
		DrawBox(s, card, style)
		state := "needs auth"
		if provider.Ready {
			state = "connected"
		}
		label := fmt.Sprintf("%s%s  ·  %s", prefix, provider.ID, state)
		DrawText(s, card.X+1, card.Y+1, card.W-2, textStyle, clampEllipsis(label, card.W-2))
	}
}

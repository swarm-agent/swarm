package ui

import (
	"fmt"
	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

type onboardingControl struct{ label, action, path string }

func (p *HomePage) repositoryControls() []onboardingControl {
	s := &p.onboarding
	if s.SetupConsent {
		return []onboardingControl{{"Create folder + Git first commit + open workspace", "setup", ""}, {"Cancel", "cancel", ""}}
	}
	if s.ChoosingRepository {
		controls := []onboardingControl{}
		for _, repo := range s.Repositories {
			controls = append(controls, onboardingControl{"Git repo: " + repo.Path, "repository", repo.Path})
		}
		return append(controls, onboardingControl{"Enter repository path", "edit", ""}, onboardingControl{"Refresh repository list", "discover", ""}, onboardingControl{"Back", "cancel", ""})
	}
	if s.Review != nil {
		controls := []onboardingControl{}
		if s.Review.Repository.HeadCommit == "" {
			for _, f := range s.Review.Files {
				mark := "[ ]"
				if s.Selected[f.Path] {
					mark = "[x]"
				}
				if !f.Selectable {
					mark = "[-]"
				}
				controls = append(controls, onboardingControl{fmt.Sprintf("%s %s (%d bytes)", mark, f.Path, f.Size), "file", f.Path})
			}
		}
		mark := "[ ]"
		if s.ConfirmOmissions {
			mark = "[x]"
		}
		controls = append(controls, onboardingControl{mark + " Omitted/uncommitted files will NOT enter worktrees", "omissions", ""})
		if s.Review.Repository.HeadCommit == "" {
			controls = append(controls, onboardingControl{"Create selected baseline / Retry same request", "baseline", ""})
		} else {
			controls = append(controls, onboardingControl{"Use committed content and save workspace", "save", ""})
		}
		return append(controls, onboardingControl{"Refresh review (discard selection)", "review", ""}, onboardingControl{"Cancel review", "cancel", ""})
	}
	// Put the next useful step first, not another verification loop. Repository
	// state is daemon-owned; discovery and folder selection never grant consent.
	primary := onboardingControl{"Inspect selected folder", "inspect", ""}
	if s.Error != "" {
		primary.label = "Retry selected folder"
	}
	r := s.Repository
	if r != nil {
		switch {
		case r.State == "ready" && r.ContentReady:
			primary = onboardingControl{"Open workspace and finish setup", "save", ""}
		case r.CanSetup && !r.NeedsReview, r.State == "directory_missing":
			primary = onboardingControl{"Set up Git and create workspace", "consent", ""}
		case r.NeedsReview:
			primary = onboardingControl{"Review files and finish Git setup", "review", ""}
			if r.State == "ready" {
				primary.label = "Review uncommitted files and open workspace"
			}
		}
	}
	controls := []onboardingControl{}
	if s.WorkspacePath != "" {
		controls = append(controls, primary)
	}
	controls = append(controls, onboardingControl{"Create a new project folder (recommended)", "new", ""})
	if s.HomePath != "" {
		controls = append(controls, onboardingControl{"Use home folder", "home", s.HomePath})
	}
	controls = append(controls,
		onboardingControl{"Use an existing Git repository…", "discover", ""},
		onboardingControl{"Select another location", "edit", ""})
	return append(controls, onboardingControl{"Cancel setup / Exit", "exit", ""})
}
func (p *HomePage) handleOnboardingWorkspaceKey(ev *tcell.EventKey) {
	s := &p.onboarding
	if s.NamingProject {
		p.handleOnboardingProjectKey(ev)
		return
	}
	if s.EditingPath {
		p.handleOnboardingWorkspaceShortcut(ev)
		return
	}
	controls := p.repositoryControls()
	if ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyDown {
		s.ActionIndex = (s.ActionIndex + 1) % len(controls)
		return
	}
	if ev.Key() == tcell.KeyBacktab || ev.Key() == tcell.KeyUp {
		s.ActionIndex = (s.ActionIndex + len(controls) - 1) % len(controls)
		return
	}
	if ev.Key() == tcell.KeyEscape {
		if s.Review != nil || s.SetupConsent || s.ChoosingRepository {
			s.ChoosingRepository = false
			s.ConfirmOmissions = false
			s.BaselineAttempt = nil
			s.Review = nil
			wasConsent := s.SetupConsent
			s.SetupConsent = false
			s.ActionIndex = 0
			if wasConsent && s.ProjectName != "" && s.WorkspacePath == p.onboardingProjectDestination() {
				s.NamingProject = true
			}
		} else if s.WorkspacePath != "" {
			s.WorkspacePath = ""
			s.Repository = nil
			s.ActionIndex = 0
		} else {
			p.ShowOnboardingProvider("Provider is optional. Skip to return.")
		}
		return
	}
	if ev.Key() == tcell.KeyCtrlL || ev.Key() == tcell.KeyCtrlN || ev.Key() == tcell.KeyCtrlS {
		p.handleOnboardingWorkspaceShortcut(ev)
		return
	}
	if s.SetupConsent && ev.Key() == tcell.KeyRune && ev.Rune() == 'y' {
		s.ActionIndex = 0
	} else if ev.Key() != tcell.KeyEnter {
		return
	}
	if s.ActionIndex >= len(controls) {
		s.ActionIndex = 0
	}
	c := controls[s.ActionIndex]
	kind := HomeActionKind("")
	switch c.action {
	case "discover":
		kind = HomeActionDiscoverOnboardingRepositories
	case "repository", "home":
		s.ChoosingRepository = false
		s.WorkspacePath = c.path
		s.Repository = nil
		s.Review = nil
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		s.ActionIndex = 0
		kind = HomeActionInspectOnboardingRepository
	case "new":
		p.beginOnboardingProject()
		return
	case "consent":
		s.SetupConsent = true
		s.ActionIndex = 0
		s.Error = ""
		s.Status = "Confirm: create this folder if needed, initialize Git and an empty first commit, then open it. No existing files are staged."
		return
	case "inspect":
		kind = HomeActionInspectOnboardingRepository
	case "review":
		kind = HomeActionReviewOnboardingRepository
	case "setup":
		kind = HomeActionSetupOnboardingRepository
		s.SetupConsent = false
	case "save":
		if s.Review != nil && !s.ConfirmOmissions {
			p.SetOnboardingError("Acknowledge omitted content before continuing.")
			return
		}
		kind = HomeActionCreateOnboardingWorkspace
	case "baseline":
		if !s.ConfirmOmissions {
			p.SetOnboardingError("Acknowledge omitted content before creating a baseline.")
			return
		}
		kind = HomeActionBaselineOnboardingRepository
	case "file":
		if s.BaselineAttempt != nil {
			p.SetOnboardingError("Retry the same request or refresh review before changing selection.")
			return
		}
		for _, f := range s.Review.Files {
			if f.Path == c.path && f.Selectable {
				s.Selected[c.path] = !s.Selected[c.path]
			}
		}
		return
	case "omissions":
		if s.BaselineAttempt == nil {
			s.ConfirmOmissions = !s.ConfirmOmissions
		}
		return
	case "cancel":
		s.ChoosingRepository = false
		s.BaselineAttempt = nil
		s.Review = nil
		s.SetupConsent = false
		s.ConfirmOmissions = false
		s.ActionIndex = 0
		return
	case "edit":
		s.ChoosingRepository = false
		s.Review = nil
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		p.handleOnboardingWorkspaceShortcut(tcell.NewEventKey(tcell.KeyCtrlL, 0, tcell.ModNone))
		return
	case "exit":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionKind("exit-onboarding")}
		return
	}
	s.Pending = true
	s.Error = ""
	s.Status = "Waiting for daemon acknowledgement..."
	p.pendingHomeAction = &HomeAction{Kind: kind, WorkspacePath: s.WorkspacePath}
}
func (p *HomePage) SetOnboardingRepositories(entries []client.WorkspaceDiscoverEntry) {
	s := &p.onboarding
	s.Repositories = nil
	// Saved Git directories may live outside the daemon's default search roots.
	// They remain candidates only: choosing one still revalidates via inspection.
	candidates := make([]client.WorkspaceDiscoverEntry, 0, len(entries)+len(p.model.Directories))
	for _, directory := range p.model.Directories {
		if directory.HasGit {
			candidates = append(candidates, client.WorkspaceDiscoverEntry{Path: directory.ResolvedPath, Name: directory.Name, IsGitRepo: true})
		}
	}
	candidates = append(candidates, entries...)
	seen := map[string]bool{}
	for _, entry := range candidates {
		if entry.IsGitRepo && entry.Path != "" && !seen[entry.Path] {
			s.Repositories = append(s.Repositories, entry)
			seen[entry.Path] = true
		}
	}
	s.Pending = false
	s.ChoosingRepository = true
	s.ActionIndex = 0
	s.Error = ""
	s.Status = "Select a repository to verify it, then open the workspace."
	if len(s.Repositories) == 0 {
		s.Status = "No Git repositories found in the runtime account's search locations. Enter a repository path, or go Back to create a new workspace."
	}
}

func (p *HomePage) SetOnboardingRepository(r client.OnboardingRepository) {
	p.onboarding.Repository = &r
	p.onboarding.Pending = false
	p.onboarding.Error = ""
	p.onboarding.WorkspacePath = r.Path
	p.onboarding.Status = r.Message
	p.onboarding.ActionIndex = 0
}
func (p *HomePage) SetOnboardingReview(r client.OnboardingReview) {
	p.SetOnboardingRepository(r.Repository)
	p.onboarding.Review = &r
	p.onboarding.Selected = map[string]bool{}
	p.onboarding.ConfirmOmissions = false
	p.onboarding.BaselineAttempt = nil
	p.onboarding.Status = r.Warning
}
func (p *HomePage) OnboardingCommittedOnly() bool { return p.onboarding.ConfirmOmissions }
func (p *HomePage) OnboardingBaselineRequest() client.OnboardingBaseline {
	s := &p.onboarding
	if s.BaselineAttempt != nil {
		return *s.BaselineAttempt
	}
	if s.Review == nil {
		return client.OnboardingBaseline{}
	}
	req := client.OnboardingBaseline{Path: s.Review.Repository.Path, ExpectedResolvedPath: s.Review.Repository.Path, ReviewDigest: s.Review.Digest, SelectedPaths: []string{}, ConfirmBaseline: true, ConfirmOmissions: s.ConfirmOmissions}
	for _, f := range s.Review.Files {
		if s.Selected[f.Path] {
			req.SelectedPaths = append(req.SelectedPaths, f.Path)
		}
	}
	s.BaselineAttempt = &req
	return req
}
func (p *HomePage) ClearOnboardingReview() { p.onboarding.Review = nil }
func (p *HomePage) drawOnboardingWorkspace(s tcell.Screen, content Rect) {
	st := &p.onboarding
	DrawText(s, content.X, content.Y, content.W, p.theme.TextMuted, clampEllipsis("Runtime account: "+st.RuntimeAccount+" (not terminal identity)", content.W))
	DrawText(s, content.X, content.Y+1, content.W, p.theme.Primary, clampTail(st.WorkspacePath, content.W))
	if st.NamingProject {
		p.drawOnboardingProject(s, content)
		return
	}
	if st.WorkspacePath == "" {
		DrawText(s, content.X, content.Y+1, content.W, p.theme.Text, "A separate project folder is recommended; home is also supported.")
	}
	if st.EditingPath {
		DrawText(s, content.X, content.Y+3, content.W, p.theme.Text, "Edit path: Enter select and verify · Esc cancel · Ctrl+U clear")
		return
	}
	controls := p.repositoryControls()
	if st.ChoosingRepository {
		DrawText(s, content.X, content.Y+2, content.W, p.theme.TextMuted, fmt.Sprintf("Git repositories · %d found · ↑/↓ scroll", len(st.Repositories)))
	}
	height := maxInt(1, content.H-3)
	start := maxInt(0, st.ActionIndex-height+1)
	for i := start; i < len(controls) && i < start+height; i++ {
		prefix := "  "
		style := p.theme.Text
		if i == st.ActionIndex {
			prefix = "› "
			style = p.theme.Primary
		}
		DrawText(s, content.X, content.Y+3+i-start, content.W, style, clampEllipsis(prefix+controls[i].label, content.W))
	}
}

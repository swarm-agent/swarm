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
		return []onboardingControl{{"Confirm empty Git initialization", "setup", ""}, {"Cancel", "cancel", ""}}
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
	controls := []onboardingControl{{"Verify / Retry selected folder", "inspect", ""}, {"Select another location", "edit", ""}, {"Create selected folder", "folder", ""}}
	if s.SuggestedPath != "" {
		controls = append(controls, onboardingControl{"Select suggested location", "suggest", ""})
	}
	if r := s.Repository; r != nil {
		if r.State == "access_denied" || r.State == "trust_required" || r.State == "git_unavailable" || r.State == "repository_error" {
			controls = append(controls, onboardingControl{"Administrative repair guidance", "repair", ""})
		}
		if r.State == "ready" && r.ContentReady {
			controls = append(controls, onboardingControl{"Save / Open workspace", "save", ""})
		}
		if r.CanSetup {
			controls = append(controls, onboardingControl{"Initialize empty Git repository", "consent", ""})
		}
		if r.NeedsReview || r.State == "needs_assisted_setup" || r.State == "needs_initial_commit" || r.State == "not_repository" || r.State == "ready" {
			controls = append(controls, onboardingControl{"Review content (no provider needed)", "review", ""})
		}
	}
	return append(controls, onboardingControl{"Back to optional provider", "back", ""}, onboardingControl{"Cancel setup / Exit", "exit", ""})
}
func (p *HomePage) handleOnboardingWorkspaceKey(ev *tcell.EventKey) {
	s := &p.onboarding
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
		if s.Review != nil || s.SetupConsent {
			s.ConfirmOmissions = false
			s.BaselineAttempt = nil
			s.Review = nil
			s.SetupConsent = false
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
	case "inspect":
		kind = HomeActionInspectOnboardingRepository
	case "review":
		kind = HomeActionReviewOnboardingRepository
	case "folder":
		kind = HomeActionCreateOnboardingFolder
	case "setup":
		kind = HomeActionSetupOnboardingRepository
		s.SetupConsent = false
	case "consent":
		s.SetupConsent = true
		s.ActionIndex = 0
		return
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
		s.Review = nil
		s.SetupConsent = false
		s.ConfirmOmissions = false
		s.ActionIndex = 0
		return
	case "edit":
		s.Review = nil
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		p.handleOnboardingWorkspaceShortcut(tcell.NewEventKey(tcell.KeyCtrlL, 0, tcell.ModNone))
		return
	case "suggest":
		s.ConfirmOmissions = false
		s.BaselineAttempt = nil
		s.WorkspacePath = s.SuggestedPath
		s.Repository = nil
		s.ActionIndex = 0
		s.Status = "Suggested location selected. Verify or create it explicitly."
		return
	case "repair":
		s.Status = "Use the installation administrator's OS terminal for runtime/access repair; no passwords here. Or select an accessible folder. Then Verify / Retry."
		return
	case "back":
		p.ShowOnboardingProvider("Provider is optional. Skip to return.")
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
func (p *HomePage) SetOnboardingRepository(r client.OnboardingRepository) {
	p.onboarding.Repository = &r
	p.onboarding.Pending = false
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
	if st.EditingPath {
		DrawText(s, content.X, content.Y+3, content.W, p.theme.Text, "Edit path: Enter select · Esc cancel · Ctrl+U clear")
		return
	}
	controls := p.repositoryControls()
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

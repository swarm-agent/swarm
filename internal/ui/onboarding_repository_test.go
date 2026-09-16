package ui

import (
	"github.com/gdamore/tcell/v2"
	"reflect"
	"swarm-refactor/swarmtui/internal/client"
	"testing"
)

func focusRepositoryControl(p *HomePage, action string) {
	for range p.repositoryControls() {
		if p.repositoryControls()[p.onboarding.ActionIndex].action == action {
			return
		}
		p.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	}
}

// Requirement: real HomePage key dispatch must expose all mandatory setup choices
// without Ctrl+S or a provider, freeze attempted consent and never infer success.
// Threat: hidden shortcuts, response loss and changed selection cause accidental
// staging or duplicate preparation. This UI boundary proves emitted user intent;
// client/API tests independently prove authenticated acknowledgement.
func TestOnboardingVisibleControlsAndFrozenConsent(t *testing.T) {
	p := readyOnboardingPage()
	p.ShowOnboardingProvider("")
	p.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !p.OnboardingWorkspaceActive() {
		t.Fatal("focusable skip failed")
	}
	p.SetOnboardingWorkspaceGuidance("worker", "/projects/new")
	focusRepositoryControl(p, "new")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !p.onboarding.NamingProject || p.onboarding.ProjectName != "" {
		t.Fatal("new project must ask for a name")
	}
	if _, ok := p.PopHomeAction(); ok || p.onboarding.SetupConsent {
		t.Fatal("blank project must not authorize Git setup")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	p.SetOnboardingReview(client.OnboardingReview{Repository: client.OnboardingRepository{Path: "/projects/new", State: "needs_assisted_setup"}, Digest: "exact-review", Files: []client.OnboardingReviewFile{{Path: "README.md", Selectable: true}, {Path: ".env", Selectable: true}, {Path: "link", Selectable: false}}})
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0)) // select README only
	focusRepositoryControl(p, "baseline")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if _, ok := p.PopHomeAction(); ok {
		t.Fatal("omission consent bypass")
	}
	focusRepositoryControl(p, "omissions")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	focusRepositoryControl(p, "baseline")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := p.PopHomeAction()
	if !ok || action.Kind != HomeActionBaselineOnboardingRepository {
		t.Fatalf("action %+v", action)
	}
	first := p.OnboardingBaselineRequest()
	if !reflect.DeepEqual(first.SelectedPaths, []string{"README.md"}) || !first.ConfirmOmissions || first.ReviewDigest != "exact-review" {
		t.Fatalf("consent %+v", first)
	}
	p.SetOnboardingError("response lost")
	focusRepositoryControl(p, "file")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !reflect.DeepEqual(first, p.OnboardingBaselineRequest()) {
		t.Fatal("retry consent changed")
	}
	if !p.OnboardingVisible() {
		t.Fatal("unacknowledged setup released gate")
	}
	focusRepositoryControl(p, "cancel")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if p.OnboardingCommittedOnly() {
		t.Fatal("cancel retained omission permission")
	}
}

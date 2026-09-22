package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestWorkspaceModalReviewFlow(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	review := client.OnboardingReview{
		Repository: client.OnboardingRepository{
			State: "not_repository",
			Path:  "/tmp/new-repo",
		},
		Digest: "digest-123",
		Files: []client.OnboardingReviewFile{
			{Path: "main.go", Size: 200, Selectable: true},
			{Path: "README.md", Size: 100, Selectable: true},
		},
	}
	p.OpenWorkspaceModalReview("/tmp/new-repo", "new-repo", "default", true, "", review)
	if !p.WorkspaceModalReviewActive() {
		t.Fatal("expected workspace modal review to be active")
	}

	controls := p.workspaceModalReviewControls()
	if len(controls) != 5 { // toggle_all, main.go, README.md, baseline, cancel
		t.Fatalf("expected 5 controls, got %d: %#v", len(controls), controls)
	}

	// Default: all selectable files are selected
	if !p.workspaceModal.Review.Selected["main.go"] || !p.workspaceModal.Review.Selected["README.md"] {
		t.Fatal("expected all selectable files to be selected by default")
	}

	// Toggle first file (main.go) to unselect it
	p.workspaceModal.Review.ActionIndex = 1
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if p.workspaceModal.Review.Selected["main.go"] {
		t.Fatal("expected main.go to be deselected after toggle")
	}

	// Controls should now include omissions confirmation
	controls = p.workspaceModalReviewControls()
	if len(controls) != 6 { // toggle_all, main.go, README.md, omissions, baseline, cancel
		t.Fatalf("expected 6 controls with omissions confirmation, got %d: %#v", len(controls), controls)
	}

	// Toggle all to reselect all
	p.workspaceModal.Review.ActionIndex = 0
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !p.workspaceModal.Review.Selected["main.go"] || !p.workspaceModal.Review.Selected["README.md"] {
		t.Fatal("expected all files to be reselected after toggle all")
	}

	// Select baseline action and submit
	baselineIdx := -1
	controls = p.workspaceModalReviewControls()
	for i, c := range controls {
		if c.Action == "baseline" {
			baselineIdx = i
			break
		}
	}
	if baselineIdx < 0 {
		t.Fatal("expected baseline action control")
	}
	p.workspaceModal.Review.ActionIndex = baselineIdx
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopWorkspaceModalAction()
	if !ok {
		t.Fatal("expected queued workspace modal action")
	}
	if action.Kind != WorkspaceModalActionBaseline {
		t.Fatalf("action kind = %q, want baseline", action.Kind)
	}
	if action.Path != "/tmp/new-repo" || action.Name != "new-repo" || !action.MakeCurrent {
		t.Fatalf("unexpected action: %#v", action)
	}
	if len(action.SelectedPaths) != 2 || !action.ConfirmBaseline {
		t.Fatalf("unexpected selected paths / confirmation: %#v", action)
	}
	if p.WorkspaceModalReviewActive() {
		t.Fatal("expected review to close after baseline submission")
	}
}

func TestWorkspaceModalReviewCancel(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	review := client.OnboardingReview{
		Repository: client.OnboardingRepository{State: "not_repository", Path: "/tmp/new-repo"},
		Digest:     "digest-123",
	}
	p.OpenWorkspaceModalReview("/tmp/new-repo", "new-repo", "", false, "", review)
	if !p.WorkspaceModalReviewActive() {
		t.Fatal("expected review active")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	if p.WorkspaceModalReviewActive() {
		t.Fatal("expected review cancelled after Esc")
	}
}

func TestWorkspaceModalReviewEmptyDirectory(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	review := client.OnboardingReview{
		Repository: client.OnboardingRepository{State: "not_repository", Path: "/tmp/empty-repo"},
		Digest:     "digest-empty",
		Files:      []client.OnboardingReviewFile{},
	}
	p.OpenWorkspaceModalReview("/tmp/empty-repo", "empty-repo", "", false, "", review)
	if !p.WorkspaceModalReviewActive() {
		t.Fatal("expected review active")
	}
	controls := p.workspaceModalReviewControls()
	if len(controls) != 3 { // empty_info, baseline, cancel
		t.Fatalf("expected 3 controls, got %d: %#v", len(controls), controls)
	}
	if p.workspaceModal.Review.ActionIndex != 1 {
		t.Fatalf("expected action index = 1 for empty directory, got %d", p.workspaceModal.Review.ActionIndex)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopWorkspaceModalAction()
	if !ok || action.Kind != WorkspaceModalActionBaseline {
		t.Fatalf("expected baseline action, got ok=%v %#v", ok, action)
	}
	if len(action.SelectedPaths) != 0 || !action.ConfirmBaseline {
		t.Fatalf("expected 0 selected paths and confirm baseline, got %#v", action)
	}
}

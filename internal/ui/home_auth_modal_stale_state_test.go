package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestAuthModalDeleteConfirmListsAffectedAgentsInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]AuthModalCredential{{ID: "cred-1", Provider: "codex", Active: true}},
	)
	p.SetAuthModalAgentProfiles([]AgentModalProfile{{Name: "alpha", Provider: "codex"}, {Name: "beta", Provider: "codex"}, {Name: "other", Provider: "openai"}})
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusCredentials
	p.authModal.ConfirmDelete = true

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 110, 28
	screen.SetSize(w, h)
	p.drawAuthModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "These agents will reset to Inherit:") {
		t.Fatalf("expected affected-agent heading in confirm overlay, got:\n%s", text)
	}
	if !strings.Contains(text, "- alpha") || !strings.Contains(text, "- beta") {
		t.Fatalf("expected affected agent names in confirm overlay, got:\n%s", text)
	}
	if strings.Contains(text, "- other") {
		t.Fatalf("did not expect unrelated agent in confirm overlay, got:\n%s", text)
	}
}

func TestAuthModalClearSnapshotRemovesStaleStateInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]AuthModalCredential{{ID: "cred-1", Provider: "codex", Active: true}},
	)
	p.SetAuthModalAgentProfiles([]AgentModalProfile{{Name: "alpha", Provider: "codex"}})
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusCredentials
	p.authModal.ConfirmDelete = true
	p.SetAuthModalStatus("stale warning")

	p.ClearAuthModalSnapshot()
	p.SetAuthModalData(nil, nil)

	if got := len(p.authModal.Providers); got != 0 {
		t.Fatalf("provider count = %d, want 0", got)
	}
	if got := len(p.authModal.Credentials); got != 0 {
		t.Fatalf("credential count = %d, want 0", got)
	}
	if got := len(p.authModal.AgentProfiles); got != 0 {
		t.Fatalf("agent profile count = %d, want 0", got)
	}
	if p.authModal.SelectedProvider != -1 {
		t.Fatalf("selected provider = %d, want -1", p.authModal.SelectedProvider)
	}
	if p.authModal.SelectedCredential != -1 {
		t.Fatalf("selected credential = %d, want -1", p.authModal.SelectedCredential)
	}
	if p.authModal.ConfirmDelete {
		t.Fatalf("expected delete confirm to be cleared")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 110, 28
	screen.SetSize(w, h)
	p.drawAuthModal(screen)

	text := dumpScreenText(screen, w, h)
	if strings.Contains(text, "Delete Credential?") {
		t.Fatalf("did not expect stale delete confirm overlay, got:\n%s", text)
	}
	if !strings.Contains(text, "no providers") {
		t.Fatalf("expected empty provider state, got:\n%s", text)
	}
}

package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestCodexAuthLoginDefaultsToOneEnterLocalFlowInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex", Ready: false}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.handleAuthModalEnter()

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_login" {
		t.Fatalf("expected codex login editor, got %#v", p.authModal.Editor)
	}
	if got := p.authModal.Editor.Selected; got != 1 {
		t.Fatalf("selected field = %d, want method field", got)
	}
	if got := p.authModal.Editor.Fields[1].Value; got != "local" {
		t.Fatalf("default method = %q, want local", got)
	}

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected pending auth action")
	}
	if action.Kind != AuthModalActionLogin || action.Login == nil {
		t.Fatalf("action = %#v, want login", action)
	}
	if action.Login.Provider != "codex" || action.Login.Method != "auto" || !action.Login.OpenBrowser {
		t.Fatalf("login = %#v, want codex local login", action.Login)
	}
	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_browser_pending" {
		t.Fatalf("expected local browser pending editor, got %#v", p.authModal.Editor)
	}
}

func TestCodexAuthLoginCyclesThroughDeviceAndRemoteInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex", Ready: false}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.handleAuthModalEnter()

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := p.authModal.Editor.Fields[1].Value; got != "device" {
		t.Fatalf("method after first right = %q, want device", got)
	}
	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopAuthModalAction()
	if !ok || action.Login == nil || action.Login.Method != "device" || action.Login.OpenBrowser {
		t.Fatalf("device action = %#v, ok=%v", action, ok)
	}

	p.authModal.Loading = false
	p.openAuthModalEditor("codex_login")
	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := p.authModal.Editor.Fields[1].Value; got != "remote" {
		t.Fatalf("method after left = %q, want remote", got)
	}
}

func TestAuthModalLeftRightStayInProviderPaneInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex"}, {ID: "openai"}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.handleAuthModalKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if p.authModal.Focus != authModalFocusProviders {
		t.Fatalf("right changed focus to %v", p.authModal.Focus)
	}
	if got := p.selectedAuthProviderID(); got != "openai" {
		t.Fatalf("selected provider = %q, want openai", got)
	}
	p.handleAuthModalKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := p.selectedAuthProviderID(); got != "codex" {
		t.Fatalf("selected provider = %q, want codex", got)
	}
}

func TestCodexAuthLoginDoesNotExposeAPIKeyInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex", Ready: false}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.handleAuthModalEnter()

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyRune, '4', tcell.ModNone))
	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_browser_pending" {
		t.Fatalf("expected local browser pending editor, got %#v", p.authModal.Editor)
	}
	action, ok := p.PopAuthModalAction()
	if !ok || action.Login == nil || action.Login.Provider != "codex" {
		t.Fatalf("expected codex OAuth login action, got ok=%v action=%#v", ok, action)
	}
}

func TestCodexAuthLoginLabelThenSubmitStartsSelectedMethodInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex", Ready: false}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.handleAuthModalEnter()

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	for _, r := range "work" {
		p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok || action.Login == nil {
		t.Fatalf("expected pending login action, got ok=%v action=%#v", ok, action)
	}
	if action.Login.Label != "work" || action.Login.Method != "auto" || !action.Login.OpenBrowser {
		t.Fatalf("login = %#v, want labeled local login", action.Login)
	}
}

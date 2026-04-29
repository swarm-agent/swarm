package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestCodexAuthLoginDefaultsToOneEnterBrowserFlowInternal(t *testing.T) {
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

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected pending auth action")
	}
	if action.Kind != AuthModalActionLogin || action.Login == nil {
		t.Fatalf("action = %#v, want login", action)
	}
	if action.Login.Provider != "codex" || action.Login.Method != "auto" || !action.Login.OpenBrowser {
		t.Fatalf("login = %#v, want codex browser login", action.Login)
	}
	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_browser_pending" {
		t.Fatalf("expected browser pending editor, got %#v", p.authModal.Editor)
	}
}

func TestCodexAuthLoginCanChooseAPIKeyInternal(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData([]AuthModalProvider{{ID: "codex", Ready: false}}, nil)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.handleAuthModalEnter()

	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone))
	p.handleAuthModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "api" {
		t.Fatalf("expected api editor, got %#v", p.authModal.Editor)
	}
	provider := ""
	selectedKey := ""
	for i, field := range p.authModal.Editor.Fields {
		if field.Key == "provider" {
			provider = field.Value
		}
		if i == p.authModal.Editor.Selected {
			selectedKey = field.Key
		}
	}
	if provider != "codex" {
		t.Fatalf("provider field = %q, want codex", provider)
	}
	if selectedKey != "api_key" {
		t.Fatalf("selected field = %q, want api_key", selectedKey)
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
		t.Fatalf("login = %#v, want labeled browser login", action.Login)
	}
}

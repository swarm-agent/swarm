package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestOpenAuthModalAPIKeyEditorPrefillsProvider(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
	)

	p.OpenAuthModalAPIKeyEditor("google")

	if !p.AuthModalVisible() {
		t.Fatalf("expected auth modal to be visible")
	}
	if p.authModal.Editor == nil {
		t.Fatalf("expected auth modal editor to be open")
	}
	if p.authModal.Editor.Mode != "api" {
		t.Fatalf("editor mode = %q, want api", p.authModal.Editor.Mode)
	}
	provider := ""
	for _, field := range p.authModal.Editor.Fields {
		if field.Key == "provider" {
			provider = field.Value
			break
		}
	}
	if provider != "google" {
		t.Fatalf("provider field = %q, want google", provider)
	}
}

func TestAuthModalEnterOnGoogleProviderOpensAPIEditor(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil {
		t.Fatalf("expected editor to open")
	}
	if p.authModal.Editor.Mode != "api" {
		t.Fatalf("editor mode = %q, want api", p.authModal.Editor.Mode)
	}
	if !strings.Contains(strings.ToLower(p.authModal.Status), "paste api key for google") {
		t.Fatalf("status = %q, want google API key paste guidance", p.authModal.Status)
	}
}

func TestAuthModalEnterOnCopilotProviderEnqueuesRefresh(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor != nil {
		t.Fatalf("did not expect editor for copilot login")
	}
	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected refresh action")
	}
	if action.Kind != AuthModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionRefresh)
	}
	if !strings.Contains(strings.ToLower(action.StatusHint), "copilot") {
		t.Fatalf("status hint = %q, want copilot status check hint", action.StatusHint)
	}
}

func TestAuthModalRefreshKeyOnCopilotUsesCopilotStatusHint(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected refresh action")
	}
	if action.Kind != AuthModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionRefresh)
	}
	if !strings.Contains(strings.ToLower(action.StatusHint), "copilot") {
		t.Fatalf("status hint = %q, want copilot status check hint", action.StatusHint)
	}
}

func TestAuthModalVerifyKeyOnCopilotUsesCopilotStatusHint(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusCredentials

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'v', tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected verify refresh action")
	}
	if action.Kind != AuthModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionRefresh)
	}
	if !strings.Contains(strings.ToLower(action.StatusHint), "copilot") {
		t.Fatalf("status hint = %q, want copilot status check hint", action.StatusHint)
	}
}

func TestAuthModalEnterOnCopilotCredentialsWithoutCredentialsEnqueuesRefresh(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusCredentials

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected refresh action")
	}
	if action.Kind != AuthModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionRefresh)
	}
	if !strings.Contains(strings.ToLower(action.StatusHint), "copilot") {
		t.Fatalf("status hint = %q, want copilot status check hint", action.StatusHint)
	}
}

func TestAuthModalLoginKeyOnCopilotEnqueuesRefresh(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'l', tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected refresh action from copilot login key")
	}
	if action.Kind != AuthModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionRefresh)
	}
	if !strings.Contains(strings.ToLower(action.StatusHint), "copilot") {
		t.Fatalf("status hint = %q, want copilot status check hint", action.StatusHint)
	}
}

func TestAuthModalEnterOnCodexProviderEnqueuesLogin(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil {
		t.Fatalf("expected login editor to open for codex")
	}
	if p.authModal.Editor.Mode != "codex_login" {
		t.Fatalf("editor mode = %q, want codex_login", p.authModal.Editor.Mode)
	}
	if got := len(p.authModal.Editor.Fields); got != 3 {
		t.Fatalf("codex login field count = %d, want 3", got)
	}
	if p.authModal.Editor.Fields[0].Key != "method" || p.authModal.Editor.Fields[1].Key != "label" || p.authModal.Editor.Fields[2].Key != "active" {
		t.Fatalf("unexpected codex login field order: %#v", p.authModal.Editor.Fields)
	}
	if got := p.authModal.Editor.Fields[1].Placeholder; got != "optional (type while selected)" {
		t.Fatalf("credential name placeholder = %q, want optional (type while selected)", got)
	}
	if !strings.Contains(strings.ToLower(p.authModal.Status), "choose login method") {
		t.Fatalf("status = %q, want codex login guidance", p.authModal.Status)
	}
	if _, ok := p.PopAuthModalAction(); ok {
		t.Fatalf("did not expect pending auth action before submitting login editor")
	}

	p.authModal.Editor.Selected = len(p.authModal.Editor.Fields) - 1
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor != nil {
		t.Fatalf("expected editor to close after login submit")
	}
	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected login action")
	}
	if action.Kind != AuthModalActionLogin {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionLogin)
	}
	if action.Login == nil {
		t.Fatalf("expected login payload")
	}
	if action.Login.Provider != "codex" {
		t.Fatalf("login provider = %q, want codex", action.Login.Provider)
	}
	if action.Login.Method != "auto" {
		t.Fatalf("login method = %q, want auto", action.Login.Method)
	}
	if !action.Login.OpenBrowser {
		t.Fatalf("expected browser login to set open browser true")
	}
}

func TestAuthModalCodexRemoteLoginMapsToCodeMethod(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_login" {
		t.Fatalf("expected codex login editor")
	}
	p.authModal.Editor.Fields[0].Value = "remote"
	p.authModal.Editor.Fields[1].Value = "team-token"
	p.authModal.Editor.Fields[2].Value = "n"
	p.authModal.Editor.Selected = len(p.authModal.Editor.Fields) - 1
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected login action")
	}
	if action.Kind != AuthModalActionLogin || action.Login == nil {
		t.Fatalf("expected login action with payload, got kind=%q", action.Kind)
	}
	if action.Login.Method != "code" {
		t.Fatalf("login method = %q, want code", action.Login.Method)
	}
	if action.Login.OpenBrowser {
		t.Fatalf("remote login should not auto-open browser")
	}
	if action.Login.Active {
		t.Fatalf("login active = true, want false")
	}
	if action.Login.Label != "team-token" {
		t.Fatalf("login label = %q, want team-token", action.Login.Label)
	}
}

func TestAuthModalCodexCallbackPromptUsesAuthURLAndCallbackFields(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowAuthModal()
	p.authModal.Login = &authModalLoginState{
		Provider:    "codex",
		Label:       "stored-label",
		Active:      false,
		Method:      "code",
		OpenBrowser: false,
	}

	authURL := "https://auth.example.com/authorize?state=abc"
	p.StartAuthModalCodexCallbackPrompt("", authURL)

	if p.authModal.Editor == nil {
		t.Fatalf("expected callback editor")
	}
	if p.authModal.Editor.Mode != "codex_callback" {
		t.Fatalf("editor mode = %q, want codex_callback", p.authModal.Editor.Mode)
	}
	if got := len(p.authModal.Editor.Fields); got != 3 {
		t.Fatalf("callback editor field count = %d, want 3", got)
	}
	if p.authModal.Editor.Fields[0].Key != "auth_url" || p.authModal.Editor.Fields[1].Key != "copy_url" || p.authModal.Editor.Fields[2].Key != "callback_input" {
		t.Fatalf("unexpected callback editor field order: %#v", p.authModal.Editor.Fields)
	}
	if got := p.authModal.Editor.Fields[0].Value; got != authURL {
		t.Fatalf("auth url field = %q, want %q", got, authURL)
	}
	if got := p.authModal.Editor.Selected; got != 2 {
		t.Fatalf("callback editor selected index = %d, want 2", got)
	}
	if status := strings.ToLower(p.authModal.Status); !strings.Contains(status, "before sign-in") || !strings.Contains(status, "after sign-in") {
		t.Fatalf("expected before/after guidance in status, got %q", p.authModal.Status)
	}
	if p.authModal.Editor.Fields[2].Value != "" {
		t.Fatalf("callback input should start empty, got %q", p.authModal.Editor.Fields[2].Value)
	}

	p.authModal.Editor.Selected = 1
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected copy action")
	}
	if action.Kind != AuthModalActionCopy {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionCopy)
	}
	if action.CopyText != authURL {
		t.Fatalf("copy text = %q, want %q", action.CopyText, authURL)
	}
	if p.authModal.Editor == nil {
		t.Fatalf("expected callback editor to remain open after copy action")
	}

	p.authModal.Editor.Fields[2].Value = "http://localhost:1455/auth/callback?code=abc123"
	p.authModal.Editor.Selected = 2
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok = p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected login callback action")
	}
	if action.Kind != AuthModalActionLoginCallback {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionLoginCallback)
	}
	if action.Login == nil {
		t.Fatalf("expected login payload")
	}
	if action.Login.Provider != "codex" {
		t.Fatalf("callback provider = %q, want codex", action.Login.Provider)
	}
	if action.Login.Label != "stored-label" {
		t.Fatalf("callback label = %q, want stored-label", action.Login.Label)
	}
	if action.Login.Active {
		t.Fatalf("callback active = true, want false")
	}
	if action.Login.Method != "code" {
		t.Fatalf("callback method = %q, want code", action.Login.Method)
	}
	if action.Login.OpenBrowser {
		t.Fatalf("callback open browser should be false")
	}
}

func TestAuthModalCodexLoginMethodFieldCyclesWithLeftRight(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_login" {
		t.Fatalf("expected codex login editor")
	}
	if got := p.authModal.Editor.Fields[0].Value; got != "browser" {
		t.Fatalf("initial method field = %q, want browser", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := p.authModal.Editor.Fields[0].Value; got != "remote" {
		t.Fatalf("method field after right = %q, want remote", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := p.authModal.Editor.Fields[0].Value; got != "browser" {
		t.Fatalf("method field after left = %q, want browser", got)
	}
}

func TestAuthModalCodexLoginOptionFieldsAreSelectionOnly(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusProviders
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_login" {
		t.Fatalf("expected codex login editor")
	}

	p.authModal.Editor.Selected = 0
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	if got := p.authModal.Editor.Fields[0].Value; got != "browser" {
		t.Fatalf("method field changed by rune input: got %q want browser", got)
	}

	p.authModal.Editor.Selected = 2
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone))
	if got := p.authModal.Editor.Fields[2].Value; got != "y" {
		t.Fatalf("active field changed by rune input: got %q want y", got)
	}

	p.authModal.Editor.Selected = 1
	for _, r := range "name-ok" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if got := p.authModal.Editor.Fields[1].Value; got != "name-ok" {
		t.Fatalf("label field should accept typing, got %q", got)
	}
}

func TestNormalizeCodexLoginMethod(t *testing.T) {
	method, open := normalizeCodexLoginMethod("browser")
	if method != "auto" || !open {
		t.Fatalf("browser mapping = (%q,%t), want (auto,true)", method, open)
	}
	method, open = normalizeCodexLoginMethod("remote")
	if method != "code" || open {
		t.Fatalf("remote mapping = (%q,%t), want (code,false)", method, open)
	}
	method, open = normalizeCodexLoginMethod("code")
	if method != "code" || open {
		t.Fatalf("code mapping = (%q,%t), want (code,false)", method, open)
	}
	method, open = normalizeCodexLoginMethod("")
	if method != "auto" || !open {
		t.Fatalf("empty mapping = (%q,%t), want (auto,true)", method, open)
	}
}

func TestAuthModalAPIEditorEnterOnAPIKeySubmits(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
	)
	p.OpenAuthModalAPIKeyEditor("google")

	if p.authModal.Editor == nil {
		t.Fatalf("expected editor to be open")
	}
	if p.authModal.Editor.Mode != "api" {
		t.Fatalf("editor mode = %q, want api", p.authModal.Editor.Mode)
	}
	if p.authModal.Editor.Selected != 2 {
		t.Fatalf("selected field = %d, want 2 (api_key)", p.authModal.Editor.Selected)
	}

	for _, r := range "sk-test-enter-submit" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if p.authModal.Editor != nil {
		t.Fatalf("expected editor to close after submit")
	}
	action, ok := p.PopAuthModalAction()
	if !ok {
		t.Fatalf("expected auth upsert action")
	}
	if action.Kind != AuthModalActionUpsert {
		t.Fatalf("action kind = %q, want %q", action.Kind, AuthModalActionUpsert)
	}
	if action.Upsert == nil {
		t.Fatalf("expected upsert payload")
	}
	if action.Upsert.Provider != "google" {
		t.Fatalf("upsert provider = %q, want google", action.Upsert.Provider)
	}
	if action.Upsert.APIKey != "sk-test-enter-submit" {
		t.Fatalf("upsert APIKey = %q, want sk-test-enter-submit", action.Upsert.APIKey)
	}
	if !action.Upsert.Active {
		t.Fatalf("expected upsert active true")
	}
}

func TestAuthModalDeleteConfirmListsAffectedAgents(t *testing.T) {
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

func TestAuthModalRefreshClearsStaleSnapshotWhenProvidersDisappear(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "codex", Ready: true, Runnable: true}},
		[]AuthModalCredential{{ID: "cred-1", Provider: "codex", Active: true}},
	)
	p.SetAuthModalAgentProfiles([]AgentModalProfile{{Name: "alpha", Provider: "codex"}})
	p.ShowAuthModal()
	p.authModal.Focus = authModalFocusCredentials
	p.authModal.ConfirmDelete = true

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

func TestAuthModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetAuthModalData(
		[]AuthModalProvider{{ID: "google", Ready: false, Runnable: false}},
		nil,
	)
	p.ShowAuthModal()

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 54, 16
	screen.SetSize(w, h)
	p.drawAuthModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Auth Manager") {
		t.Fatalf("expected auth modal on narrow screen, got:\n%s", text)
	}
}

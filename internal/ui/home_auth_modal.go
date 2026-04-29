package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type AuthModalProvider struct {
	ID              string
	Ready           bool
	Runnable        bool
	Reason          string
	RunReason       string
	DefaultModel    string
	DefaultThinking string
	AuthMethods     []AuthModalAuthMethod
}

type AuthModalAuthMethod struct {
	ID             string
	Label          string
	CredentialType string
	Description    string
}

type AuthModalCredential struct {
	ID           string
	Provider     string
	Active       bool
	AuthType     string
	Label        string
	Tags         []string
	UpdatedAt    int64
	CreatedAt    int64
	ExpiresAt    int64
	Last4        string
	HasRefresh   bool
	HasAccountID bool
	StorageMode  string
}

type authModalLoginState struct {
	Provider    string
	Label       string
	Active      bool
	Method      string
	OpenBrowser bool
	AuthURL     string
}

type AuthModalActionKind string

const (
	AuthModalActionRefresh       AuthModalActionKind = "refresh"
	AuthModalActionVerify        AuthModalActionKind = "verify"
	AuthModalActionUpsert        AuthModalActionKind = "upsert"
	AuthModalActionSetActive     AuthModalActionKind = "set-active"
	AuthModalActionDelete        AuthModalActionKind = "delete"
	AuthModalActionLogin         AuthModalActionKind = "login"
	AuthModalActionLoginCallback AuthModalActionKind = "login-callback"
	AuthModalActionCopy          AuthModalActionKind = "copy"
)

type AuthModalUpsert struct {
	ID           string
	Provider     string
	Type         string
	Label        string
	Tags         []string
	APIKey       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	AccountID    string
	Active       bool
}

type AuthModalLogin struct {
	Provider      string
	Label         string
	Active        bool
	Method        string
	OpenBrowser   bool
	CallbackInput string
}

type AuthModalAction struct {
	Kind       AuthModalActionKind
	Provider   string
	ID         string
	Upsert     *AuthModalUpsert
	Login      *AuthModalLogin
	CopyText   string
	StatusHint string
}

type authModalFocus int

const (
	authModalFocusProviders authModalFocus = iota
	authModalFocusCredentials
	authModalFocusProviderSearch
	authModalFocusCredentialSearch
)

type authModalEditor struct {
	Mode         string
	Fields       []authModalEditorField
	Selected     int
	CredentialID string
}

type authModalEditorField struct {
	Key         string
	Label       string
	Value       string
	Placeholder string
	Secret      bool
}

type authModalState struct {
	Visible            bool
	Loading            bool
	Status             string
	Error              string
	Focus              authModalFocus
	ProviderSearch     string
	CredentialSearch   string
	Providers          []AuthModalProvider
	Credentials        []AuthModalCredential
	AgentProfiles      []AgentModalProfile
	SelectedProvider   int
	SelectedCredential int
	ConfirmDelete      bool
	Editor             *authModalEditor
	Login              *authModalLoginState
}

func (p *HomePage) ShowAuthModal() {
	p.authModal.Visible = true
	if p.authModal.Focus < authModalFocusProviders || p.authModal.Focus > authModalFocusCredentialSearch {
		p.authModal.Focus = authModalFocusCredentials
	}
	p.authModal.ConfirmDelete = false
}

func (p *HomePage) OpenAuthModalAPIKeyEditor(providerID string) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	p.ShowAuthModal()
	p.authModal.ProviderSearch = ""
	p.authModal.CredentialSearch = ""

	if providerID != "" {
		idx := p.findAuthProviderIndex(providerID)
		if idx < 0 {
			p.authModal.Providers = append(p.authModal.Providers, AuthModalProvider{
				ID:       providerID,
				Ready:    false,
				Runnable: false,
				Reason:   "auth required",
			})
			idx = len(p.authModal.Providers) - 1
		}
		p.authModal.SelectedProvider = idx
	}
	p.authModal.reconcileSelections()
	p.openAuthModalEditor("api")
	if providerID == "" || p.authModal.Editor == nil {
		return
	}
	for i := range p.authModal.Editor.Fields {
		if p.authModal.Editor.Fields[i].Key == "provider" {
			p.authModal.Editor.Fields[i].Value = providerID
			break
		}
	}
}

func (p *HomePage) HideAuthModal() {
	p.authModal = authModalState{}
	p.pendingAuthAction = nil
	p.HideAuthDefaultsInfo()
}

func (p *HomePage) SetAuthModalLoginInput(input string) {
	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_callback" {
		return
	}
	for i := range p.authModal.Editor.Fields {
		if p.authModal.Editor.Fields[i].Key == "callback_input" {
			p.authModal.Editor.Fields[i].Value = strings.TrimSpace(input)
			p.authModal.Editor.Selected = i
			break
		}
	}
}

func (p *HomePage) FocusAuthModalCallbackInput() {
	if p.authModal.Editor == nil || p.authModal.Editor.Mode != "codex_callback" {
		return
	}
	for i := range p.authModal.Editor.Fields {
		if p.authModal.Editor.Fields[i].Key == "callback_input" {
			p.authModal.Editor.Selected = i
			return
		}
	}
}

func (p *HomePage) StartAuthModalCodexCallbackPrompt(status, authURL string) {
	if p.authModal.Login == nil {
		p.authModal.Login = &authModalLoginState{
			Provider:    "codex",
			Active:      true,
			Method:      "code",
			OpenBrowser: false,
		}
	}
	p.authModal.Login.AuthURL = strings.TrimSpace(authURL)
	p.authModal.Editor = &authModalEditor{
		Mode: "codex_callback",
		Fields: []authModalEditorField{
			{Key: "copy_url", Label: "Copy URL", Value: "Press Enter to copy full auth URL"},
			{Key: "callback_input", Label: "Callback URL or code", Value: "", Placeholder: "paste full callback URL or code"},
		},
		Selected: 0,
	}
	if strings.TrimSpace(status) == "" {
		status = "Before sign-in: press Enter on Copy URL to copy the full auth URL. After sign-in: paste the callback URL or code here."
	}
	p.authModal.Status = strings.TrimSpace(status)
	p.authModal.Error = ""
	p.authModal.ConfirmDelete = false
}

func (p *HomePage) StartAuthModalCodexBrowserPending(status, authURL string) {
	if p.authModal.Login == nil {
		p.authModal.Login = &authModalLoginState{
			Provider:    "codex",
			Active:      true,
			Method:      "auto",
			OpenBrowser: true,
		}
	}
	p.authModal.Login.AuthURL = strings.TrimSpace(authURL)
	p.authModal.Editor = &authModalEditor{
		Mode: "codex_browser_pending",
		Fields: []authModalEditorField{
			{Key: "copy_url", Label: "Copy URL", Value: "Press Enter to copy full auth URL"},
		},
		Selected: 0,
	}
	if strings.TrimSpace(status) == "" {
		status = "Finish sign-in in your browser. This modal will close automatically after confirmation."
	}
	p.authModal.Status = strings.TrimSpace(status)
	p.authModal.Error = ""
	p.authModal.ConfirmDelete = false
}

func (p *HomePage) AuthModalVisible() bool {
	return p.authModal.Visible
}

func (p *HomePage) SetAuthModalLoading(loading bool) {
	p.authModal.Loading = loading
}

func (p *HomePage) ClearAuthModalSnapshot() {
	p.authModal.Providers = nil
	p.authModal.Credentials = nil
	p.authModal.AgentProfiles = nil
	p.authModal.SelectedProvider = -1
	p.authModal.SelectedCredential = -1
	p.authModal.ConfirmDelete = false
}

func (p *HomePage) SetAuthModalStatus(status string) {
	p.authModal.Status = strings.TrimSpace(status)
	if p.authModal.Status != "" {
		p.authModal.Error = ""
	}
}

func (p *HomePage) SetAuthModalError(err string) {
	p.authModal.Error = strings.TrimSpace(err)
	if p.authModal.Error != "" {
		p.authModal.Loading = false
	}
}

func (p *HomePage) AuthModalEditorMode() string {
	if p == nil || p.authModal.Editor == nil {
		return ""
	}
	return strings.TrimSpace(p.authModal.Editor.Mode)
}

func (p *HomePage) SetAuthModalData(providers []AuthModalProvider, credentials []AuthModalCredential) {
	selectedProvider := p.selectedAuthProviderID()
	selectedCredential := p.selectedAuthCredentialID()

	p.authModal.Providers = append([]AuthModalProvider(nil), providers...)
	p.authModal.Credentials = append([]AuthModalCredential(nil), credentials...)

	if len(p.authModal.Providers) == 0 {
		seen := make(map[string]struct{}, len(credentials))
		for _, credential := range credentials {
			providerID := strings.ToLower(strings.TrimSpace(credential.Provider))
			if providerID == "" {
				continue
			}
			if _, ok := seen[providerID]; ok {
				continue
			}
			seen[providerID] = struct{}{}
			p.authModal.Providers = append(p.authModal.Providers, AuthModalProvider{ID: providerID})
		}
	}

	p.authModal.SelectedProvider = p.findAuthProviderIndex(selectedProvider)
	if p.authModal.SelectedProvider < 0 && len(p.authModal.Providers) > 0 {
		p.authModal.SelectedProvider = 0
	}
	p.authModal.SelectedCredential = p.findAuthCredentialIndex(selectedCredential)
	p.authModal.reconcileSelections()
}

func (p *HomePage) SetAuthModalAgentProfiles(profiles []AgentModalProfile) {
	p.authModal.AgentProfiles = append([]AgentModalProfile(nil), profiles...)
}

func (p *HomePage) PopAuthModalAction() (AuthModalAction, bool) {
	if p.pendingAuthAction == nil {
		return AuthModalAction{}, false
	}
	action := *p.pendingAuthAction
	p.pendingAuthAction = nil
	return action, true
}

func (p *HomePage) handleAuthModalKey(ev *tcell.EventKey) {
	if p.authModal.Editor != nil {
		p.handleAuthModalEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideAuthModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext):
		p.advanceAuthModalFocus(1)
		return
	case p.keybinds.Match(ev, KeybindModalFocusPrev):
		p.advanceAuthModalFocus(-1)
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		p.authModal.Focus = authModalFocusProviders
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		p.authModal.Focus = authModalFocusCredentials
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveAuthModalSelection(-1)
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveAuthModalSelection(1)
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.deleteAuthModalSearchRune()
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.clearAuthModalSearch()
		p.authModal.ConfirmDelete = false
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleAuthModalEnter()
		p.authModal.ConfirmDelete = false
		return
	}

	if ev.Key() == tcell.KeyRune {
		p.handleAuthModalRune(ev)
	}
}

func (p *HomePage) handleAuthModalRune(ev *tcell.EventKey) {
	r := ev.Rune()
	if p.authModal.Focus == authModalFocusProviderSearch || p.authModal.Focus == authModalFocusCredentialSearch {
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			if p.authModal.Focus == authModalFocusProviderSearch {
				p.authModal.ProviderSearch += string(r)
			} else {
				p.authModal.CredentialSearch += string(r)
			}
			p.authModal.reconcileSelections()
		}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalSearchFocus):
		p.authModal.Focus = authModalFocusProviderSearch
	case p.keybinds.Match(ev, KeybindAuthFocusCredentialSearch):
		p.authModal.Focus = authModalFocusCredentialSearch
	case p.keybinds.Match(ev, KeybindAuthFocusProviders):
		p.authModal.Focus = authModalFocusProviders
	case p.keybinds.Match(ev, KeybindAuthFocusCredentials):
		p.authModal.Focus = authModalFocusCredentials
	case p.keybinds.Match(ev, KeybindAuthClearSearchAlt):
		p.clearAuthModalSearch()
	case p.keybinds.Match(ev, KeybindAuthVerify):
		credential, ok := p.authVerificationTarget()
		if !ok {
			p.authModal.Status = "No credential selected to verify"
			return
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionVerify,
			Provider:   credential.Provider,
			ID:         credential.ID,
			StatusHint: p.authVerifyStatusHint(credential),
		})
	case p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveAuthModalSelection(1)
	case p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveAuthModalSelection(-1)
	case p.keybinds.Match(ev, KeybindAuthRefresh):
		if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") {
			if credential, ok := p.authVerificationTarget(); ok {
				p.enqueueAuthModalAction(AuthModalAction{
					Kind:       AuthModalActionVerify,
					Provider:   credential.Provider,
					ID:         credential.ID,
					StatusHint: "Refreshing Copilot auth status via the auth manager...",
				})
				return
			}
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionRefresh,
			StatusHint: p.authRefreshStatusHint(),
		})
	case p.keybinds.Match(ev, KeybindAuthLogin):
		p.triggerProviderLogin(p.authContextProviderID())
	case p.keybinds.Match(ev, KeybindAuthSetActive):
		credential, ok := p.selectedAuthCredential()
		if !ok {
			p.authModal.Status = "No credential selected"
			return
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionSetActive,
			Provider:   credential.Provider,
			ID:         credential.ID,
			StatusHint: fmt.Sprintf("Setting this credential active for %s...", credential.Provider),
		})
	case p.keybinds.Match(ev, KeybindAuthDelete):
		credential, ok := p.selectedAuthCredential()
		if !ok {
			p.authModal.Status = "No credential selected"
			return
		}
		if !p.authModal.ConfirmDelete {
			p.authModal.ConfirmDelete = true
			p.authModal.Status = fmt.Sprintf("Press d again to delete %s/%s. If this removes the provider auth, affected agents reset to inherit; reassign them in /agents. The default model may also clear.", credential.Provider, credential.ID)
			return
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionDelete,
			Provider:   credential.Provider,
			ID:         credential.ID,
			StatusHint: fmt.Sprintf("Deleting %s/%s ...", credential.Provider, credential.ID),
		})
		p.authModal.ConfirmDelete = false
	case p.keybinds.Match(ev, KeybindAuthNewAPI):
		if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") {
			p.openCopilotAuthEditor("token", nil)
			p.authModal.Status = "Advanced: add a GitHub token for Copilot. Default path is Copilot CLI sidecar via `copilot login`."
			return
		}
		p.openAuthModalEditor("api")
	case p.keybinds.Match(ev, KeybindAuthNewOAuth):
		if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") {
			p.openCopilotAuthEditor("cli", nil)
			p.authModal.Status = "Use Copilot CLI sidecar auth: run `copilot login`, then verify/refresh here."
			return
		}
		p.openAuthModalEditor("oauth")
	case p.keybinds.Match(ev, KeybindAuthEdit):
		credential, ok := p.selectedAuthCredential()
		if !ok {
			p.authModal.Status = "No credential selected"
			return
		}
		p.openAuthModalEditorForUpdate(credential)
	}
}

func (p *HomePage) handleAuthModalEnter() {
	switch p.authModal.Focus {
	case authModalFocusProviderSearch:
		p.authModal.Focus = authModalFocusProviders
	case authModalFocusCredentialSearch:
		p.authModal.Focus = authModalFocusCredentials
	case authModalFocusProviders:
		p.triggerProviderLogin(p.selectedAuthProviderID())
	case authModalFocusCredentials:
		providerID := p.authContextProviderID()
		credential, ok := p.selectedAuthCredential()
		if !ok {
			if strings.EqualFold(strings.TrimSpace(providerID), "copilot") {
				p.triggerProviderLogin(providerID)
				return
			}
			p.triggerProviderLogin(providerID)
			return
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionSetActive,
			Provider:   credential.Provider,
			ID:         credential.ID,
			StatusHint: fmt.Sprintf("Setting this credential active for %s...", credential.Provider),
		})
	}
}

func (p *HomePage) authRefreshStatusHint() string {
	return p.authRefreshStatusHintForProvider(p.authContextProviderID())
}

func (p *HomePage) authRefreshStatusHintForProvider(providerID string) string {
	if strings.EqualFold(strings.TrimSpace(providerID), "copilot") {
		return "Refreshing Copilot auth status for the selected Swarm auth source. Use Enter or l to choose method; use r or v to verify current status."
	}
	return "Refreshing auth records..."
}

func (p *HomePage) authVerificationTarget() (AuthModalCredential, bool) {
	if credential, ok := p.selectedAuthCredential(); ok {
		return credential, true
	}
	providerID := strings.ToLower(strings.TrimSpace(p.authContextProviderID()))
	if providerID == "" {
		return AuthModalCredential{}, false
	}
	for _, credential := range p.authModal.Credentials {
		if strings.EqualFold(strings.TrimSpace(credential.Provider), providerID) && credential.Active {
			return credential, true
		}
	}
	return AuthModalCredential{}, false
}

func (p *HomePage) authVerifyStatusHint(credential AuthModalCredential) string {
	providerID := strings.ToLower(strings.TrimSpace(credential.Provider))
	if providerID == "copilot" {
		return fmt.Sprintf("Verifying Copilot auth source %s via the auth manager...", authCredentialDisplayLabel(credential))
	}
	return fmt.Sprintf("Verifying %s/%s ...", credential.Provider, credential.ID)
}

func (p *HomePage) triggerProviderLogin(providerID string) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		p.authModal.Status = "Select a provider first"
		p.authModal.Error = ""
		return
	}
	selectedProvider, _ := p.selectedAuthProvider()
	if !strings.EqualFold(selectedProvider.ID, providerID) {
		for _, candidate := range p.authModal.Providers {
			if strings.EqualFold(candidate.ID, providerID) {
				selectedProvider = candidate
				break
			}
		}
	}
	methodsSummary := providerAuthMethodsSummary(selectedProvider)
	if providerID == "copilot" {
		p.openCopilotAuthEditor("cli", nil)
		p.authModal.Status = "Use Copilot CLI sidecar auth: run `copilot login`, then press Enter to verify. Token/gh are advanced fallback methods."
		if methodsSummary != "" {
			p.authModal.Status = fmt.Sprintf("Copilot auth: %s. Default path is the Copilot CLI sidecar; press Enter to verify or save it active.", methodsSummary)
		}
		p.authModal.Error = ""
		return
	}
	if providerID != "codex" {
		p.openAuthModalEditor("api")
		p.authModal.Status = fmt.Sprintf("Paste API key for %s and press Enter to save.", providerID)
		if methodsSummary != "" {
			p.authModal.Status = fmt.Sprintf("Auth methods for %s: %s. Paste API key and press Enter to save.", providerID, methodsSummary)
		}
		p.authModal.Error = ""
		return
	}
	p.openAuthModalEditor("codex_login")
	p.authModal.Status = "Codex auth: press Enter for browser sign-in, ←/→ to choose API key or remote, or ↑ to add an optional label first."
	p.authModal.Error = ""
}

func (p *HomePage) openAuthModalEditor(mode string) {
	providerID := p.selectedAuthProviderID()
	switch mode {
	case "oauth":
		p.authModal.Editor = &authModalEditor{
			Mode: "oauth",
			Fields: []authModalEditorField{
				{Key: "provider", Label: "Provider", Value: providerID, Placeholder: "provider id"},
				{Key: "label", Label: "Credential Name", Value: "", Placeholder: "optional (email if known)"},
				{Key: "access_token", Label: "Access Token", Value: "", Placeholder: "token", Secret: true},
				{Key: "refresh_token", Label: "Refresh Token", Value: "", Placeholder: "token", Secret: true},
				{Key: "expires_at", Label: "Expires At (unix ms)", Value: "", Placeholder: "optional"},
				{Key: "account_id", Label: "Account ID", Value: "", Placeholder: "optional"},
				{Key: "active", Label: "Set this credential active? (y/n)", Value: "y", Placeholder: "y"},
			},
		}
	case "codex_login":
		if providerID == "" {
			providerID = "codex"
		}
		p.authModal.Editor = &authModalEditor{
			Mode: "codex_login",
			Fields: []authModalEditorField{
				{Key: "label", Label: "Optional Label", Value: "", Placeholder: "press ↑ and type to name this credential"},
				{Key: "method", Label: "Auth method", Value: "browser", Placeholder: "browser"},
			},
			Selected: 1,
		}
	default:
		p.authModal.Editor = &authModalEditor{
			Mode: "api",
			Fields: []authModalEditorField{
				{Key: "provider", Label: "Provider", Value: providerID, Placeholder: "provider id"},
				{Key: "label", Label: "Credential Name", Value: "", Placeholder: "optional"},
				{Key: "api_key", Label: "API Key", Value: "", Placeholder: "sk-...", Secret: true},
				{Key: "active", Label: "Set this API key active? (y/n)", Value: "y", Placeholder: "y"},
			},
		}
		if providerID != "" {
			p.authModal.Editor.Selected = 2
		}
	}
	p.authModal.Status = "Fill fields and press Enter to continue"
	p.authModal.Error = ""
	p.authModal.ConfirmDelete = false
}

func (p *HomePage) openCopilotAuthEditor(initialMethod string, credential *AuthModalCredential) {
	method := normalizeCopilotLoginMethod(initialMethod)
	if method == "" {
		method = "cli"
	}
	label := ""
	active := "y"
	credentialID := ""
	if credential != nil {
		label = strings.TrimSpace(credential.Label)
		credentialID = strings.TrimSpace(credential.ID)
		if !credential.Active {
			active = "n"
		}
		credentialMethod := normalizeCopilotLoginMethod(credential.AuthType)
		if credentialMethod != "" {
			method = credentialMethod
		}
	}
	p.authModal.Editor = &authModalEditor{
		Mode:         "copilot_login",
		CredentialID: credentialID,
		Fields: []authModalEditorField{
			{Key: "method", Label: "Method (cli sidecar/gh/token)", Value: method, Placeholder: "cli"},
			{Key: "label", Label: "Credential Name", Value: label, Placeholder: "optional"},
			{Key: "token", Label: "GitHub Token", Value: "", Placeholder: "github_pat_... or OAuth token", Secret: true},
			{Key: "active", Label: "Set this auth source active? (y/n)", Value: active, Placeholder: "y"},
		},
	}
	if method == "token" {
		p.authModal.Editor.Selected = 2
	}
	p.authModal.ConfirmDelete = false
	p.authModal.Error = ""
}

func (p *HomePage) openAuthModalEditorForUpdate(credential AuthModalCredential) {
	mode := strings.ToLower(strings.TrimSpace(credential.AuthType))
	if strings.EqualFold(strings.TrimSpace(credential.Provider), "copilot") && mode != "oauth" {
		p.openCopilotAuthEditor(mode, &credential)
		p.authModal.Status = fmt.Sprintf("Editing Copilot auth source %s. Switch Method if needed, rename it, or replace the token for token-backed auth.", authCredentialDisplayLabel(credential))
		return
	}
	if mode != "oauth" {
		mode = "api"
	}
	p.openAuthModalEditor(mode)
	editor := p.authModal.Editor
	if editor == nil {
		return
	}
	editor.CredentialID = credential.ID
	for i := range editor.Fields {
		field := &editor.Fields[i]
		switch field.Key {
		case "provider":
			field.Value = credential.Provider
		case "label":
			field.Value = credential.Label
		case "active":
			if credential.Active {
				field.Value = "y"
			} else {
				field.Value = "n"
			}
		}
	}
	if mode == "api" {
		p.authModal.Status = fmt.Sprintf("Editing API key for %s. You can rename this credential. Leave API key blank to keep current key.", credential.Provider)
		return
	}
	p.authModal.Status = fmt.Sprintf("Editing OAuth credential for %s. You can rename this credential.", credential.Provider)
}

func (p *HomePage) handleAuthModalEditorKey(ev *tcell.EventKey) {
	editor := p.authModal.Editor
	if editor == nil {
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.authModal.Editor = nil
		p.authModal.Status = "Editor closed"
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorMoveDown):
		editor.Selected = (editor.Selected + 1) % len(editor.Fields)
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusPrev, KeybindEditorMoveUp):
		editor.Selected = (editor.Selected - 1 + len(editor.Fields)) % len(editor.Fields)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		field := &editor.Fields[editor.Selected]
		if editor.Mode == "codex_login" {
			switch field.Key {
			case "method":
				field.Value = cycleCodexLoginMethodValue(field.Value, -1)
			case "active":
				field.Value = cycleYNValue(field.Value, -1)
			}
		} else if editor.Mode == "copilot_login" {
			switch field.Key {
			case "method":
				field.Value = cycleCopilotLoginMethodValue(field.Value, -1)
			case "active":
				field.Value = cycleYNValue(field.Value, -1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		field := &editor.Fields[editor.Selected]
		if editor.Mode == "codex_login" {
			switch field.Key {
			case "method":
				field.Value = cycleCodexLoginMethodValue(field.Value, 1)
			case "active":
				field.Value = cycleYNValue(field.Value, 1)
			}
		} else if editor.Mode == "copilot_login" {
			switch field.Key {
			case "method":
				field.Value = cycleCopilotLoginMethodValue(field.Value, 1)
			case "active":
				field.Value = cycleYNValue(field.Value, 1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		field := &editor.Fields[editor.Selected]
		if editor.Mode == "codex_callback" && field.Key != "callback_input" {
			return
		}
		if editor.Mode == "codex_browser_pending" {
			return
		}
		if editor.Mode == "codex_login" && field.Key != "label" {
			return
		}
		if editor.Mode == "copilot_login" && field.Key != "label" && field.Key != "token" {
			return
		}
		if len(field.Value) > 0 {
			_, sz := utf8.DecodeLastRuneInString(field.Value)
			if sz > 0 {
				field.Value = field.Value[:len(field.Value)-sz]
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		field := &editor.Fields[editor.Selected]
		if editor.Mode == "codex_callback" {
			switch field.Key {
			case "callback_input":
				field.Value = ""
			}
			return
		}
		if editor.Mode == "codex_browser_pending" {
			return
		}
		if editor.Mode == "codex_login" {
			switch field.Key {
			case "label":
				field.Value = ""
			case "method":
				field.Value = "browser"
			}
			return
		}
		if editor.Mode == "copilot_login" {
			switch field.Key {
			case "label", "token":
				field.Value = ""
			case "method":
				field.Value = "cli"
			case "active":
				field.Value = "y"
			}
			return
		}
		editor.Fields[editor.Selected].Value = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		selectedField := editor.Fields[editor.Selected]
		if selectedField.Key == "api_key" || selectedField.Key == "callback_input" || selectedField.Key == "copy_url" || selectedField.Key == "token" {
			p.submitAuthModalEditor()
			return
		}
		if editor.Mode == "codex_login" && selectedField.Key == "label" && strings.TrimSpace(selectedField.Value) != "" {
			p.submitAuthModalEditor()
			return
		}
		if editor.Selected < len(editor.Fields)-1 {
			editor.Selected++
			return
		}
		p.submitAuthModalEditor()
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		field := &editor.Fields[editor.Selected]
		if editor.Mode == "codex_callback" {
			switch field.Key {
			case "copy_url":
				if unicode.IsPrint(r) && (r == ' ' || r == 'c' || r == 'C') {
					p.submitAuthModalEditor()
				}
				return
			}
		}
		if editor.Mode == "codex_browser_pending" {
			if field.Key == "copy_url" && unicode.IsPrint(r) && (r == ' ' || r == 'c' || r == 'C') {
				p.submitAuthModalEditor()
			}
			return
		}
		if editor.Mode == "codex_login" && field.Key != "label" {
			if field.Key == "method" {
				switch {
				case r == ' ' || r == 'h' || r == 'l':
					field.Value = cycleCodexLoginMethodValue(field.Value, 1)
				case r == '1' || r == 'b' || r == 'B':
					field.Value = "browser"
				case r == '2' || r == 'r' || r == 'R':
					field.Value = "remote"
				case r == '3' || r == 'a' || r == 'A':
					field.Value = "api key"
				}
			}
			return
		}
		if editor.Mode == "copilot_login" && field.Key != "label" && field.Key != "token" {
			switch {
			case r == ' ':
				if field.Key == "active" {
					field.Value = cycleYNValue(field.Value, 1)
				}
			case r == 'h' || r == 'l':
				if field.Key == "method" {
					field.Value = cycleCopilotLoginMethodValue(field.Value, 1)
				}
			case r == 'j' || r == 'k':
				if field.Key == "active" {
					field.Value = cycleYNValue(field.Value, 1)
				}
			}
			return
		}
		if unicode.IsPrint(r) {
			editor.Fields[editor.Selected].Value += string(r)
		}
	}
}

func (p *HomePage) submitAuthModalEditor() {
	editor := p.authModal.Editor
	if editor == nil {
		return
	}

	get := func(key string) string {
		for _, field := range editor.Fields {
			if field.Key == key {
				return strings.TrimSpace(field.Value)
			}
		}
		return ""
	}

	active := parseYN(get("active"))

	if editor.Mode == "codex_login" {
		method, openBrowser := normalizeCodexLoginMethod(get("method"))
		label := get("label")
		provider := "codex"
		if method == "api" {
			p.openAuthModalEditor("api")
			if p.authModal.Editor != nil {
				for i := range p.authModal.Editor.Fields {
					switch p.authModal.Editor.Fields[i].Key {
					case "provider":
						p.authModal.Editor.Fields[i].Value = provider
					case "label":
						p.authModal.Editor.Fields[i].Value = label
					case "api_key":
						p.authModal.Editor.Selected = i
					}
				}
			}
			p.authModal.Status = "Paste Codex API key and press Enter to save."
			p.authModal.Error = ""
			return
		}
		authURL := ""
		if p.authModal.Login != nil {
			authURL = strings.TrimSpace(p.authModal.Login.AuthURL)
		}
		p.authModal.Login = &authModalLoginState{
			Provider:    provider,
			Label:       label,
			Active:      active,
			Method:      method,
			OpenBrowser: openBrowser,
			AuthURL:     authURL,
		}
		statusHint := "Starting Codex OAuth login..."
		if method == "code" {
			p.authModal.Editor = nil
			statusHint = "Preparing remote Codex OAuth login..."
		} else {
			p.StartAuthModalCodexBrowserPending("Starting Codex OAuth browser login...", authURL)
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind: AuthModalActionLogin,
			Login: &AuthModalLogin{
				Provider:    provider,
				Label:       label,
				Active:      active,
				Method:      method,
				OpenBrowser: openBrowser,
			},
			StatusHint: statusHint,
		})
		return
	}

	if editor.Mode == "codex_browser_pending" {
		copyText := ""
		if p.authModal.Login != nil {
			copyText = strings.TrimSpace(p.authModal.Login.AuthURL)
		}
		if copyText == "" {
			p.authModal.Error = "auth URL is not ready yet"
			return
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind:       AuthModalActionCopy,
			CopyText:   copyText,
			StatusHint: "Copying auth URL...",
		})
		return
	}

	if editor.Mode == "copilot_login" {
		method := normalizeCopilotLoginMethod(get("method"))
		if method == "" {
			p.authModal.Error = "choose a Copilot auth method"
			return
		}
		label := get("label")
		token := get("token")
		if method == "token" {
			upsert := AuthModalUpsert{
				ID:       strings.TrimSpace(editor.CredentialID),
				Provider: "copilot",
				Type:     "api",
				Label:    label,
				APIKey:   token,
				Active:   active,
			}
			if upsert.APIKey == "" && upsert.ID == "" {
				p.authModal.Error = "GitHub token is required for token auth"
				return
			}
			p.authModal.Editor = nil
			p.enqueueAuthModalAction(AuthModalAction{
				Kind:       AuthModalActionUpsert,
				Upsert:     &upsert,
				StatusHint: "Saving Copilot GitHub token...",
			})
			return
		}
		p.authModal.Login = &authModalLoginState{
			Provider: "copilot",
			Label:    label,
			Active:   active,
			Method:   method,
		}
		p.authModal.Editor = nil
		statusHint := "Verifying Copilot auth source..."
		if method == "cli" {
			statusHint = "Verifying Copilot CLI sidecar auth in the active swarmd runtime..."
		} else if method == "gh" {
			statusHint = "Verifying GitHub CLI auth for Copilot in the active swarmd runtime..."
		}
		p.enqueueAuthModalAction(AuthModalAction{
			Kind: AuthModalActionLogin,
			Login: &AuthModalLogin{
				Provider: "copilot",
				Label:    label,
				Active:   active,
				Method:   method,
			},
			StatusHint: statusHint,
		})
		return
	}

	if editor.Mode == "codex_callback" {
		if editor.Fields[editor.Selected].Key == "copy_url" {
			copyText := ""
			if p.authModal.Login != nil {
				copyText = strings.TrimSpace(p.authModal.Login.AuthURL)
			}
			if copyText == "" {
				p.authModal.Error = "auth URL is unavailable; restart remote login"
				return
			}
			p.enqueueAuthModalAction(AuthModalAction{
				Kind:       AuthModalActionCopy,
				CopyText:   copyText,
				StatusHint: "Copying auth URL...",
			})
			return
		}
		callbackInput := get("callback_input")
		if callbackInput == "" {
			p.authModal.Error = "callback URL or code is required"
			return
		}
		provider := "codex"
		label := ""
		if p.authModal.Login != nil {
			if trimmed := strings.ToLower(strings.TrimSpace(p.authModal.Login.Provider)); trimmed != "" {
				provider = trimmed
			}
			if trimmed := strings.TrimSpace(p.authModal.Login.Label); trimmed != "" {
				label = trimmed
			}
			active = p.authModal.Login.Active
		}
		p.authModal.Editor = nil
		p.enqueueAuthModalAction(AuthModalAction{
			Kind: AuthModalActionLoginCallback,
			Login: &AuthModalLogin{
				Provider:      provider,
				Label:         label,
				Active:        active,
				Method:        "code",
				OpenBrowser:   false,
				CallbackInput: callbackInput,
			},
			StatusHint: "Completing Codex OAuth login from pasted callback...",
		})
		return
	}

	provider := strings.ToLower(strings.TrimSpace(get("provider")))
	if provider == "" {
		p.authModal.Error = "provider is required"
		return
	}

	upsert := AuthModalUpsert{
		ID:       strings.TrimSpace(editor.CredentialID),
		Provider: provider,
		Label:    get("label"),
		Active:   active,
	}

	switch editor.Mode {
	case "oauth":
		upsert.Type = "oauth"
		upsert.AccessToken = get("access_token")
		upsert.RefreshToken = get("refresh_token")
		upsert.AccountID = get("account_id")
		if (upsert.AccessToken == "" || upsert.RefreshToken == "") && upsert.ID == "" {
			p.authModal.Error = "OAuth needs access + refresh tokens"
			return
		}
		if rawExpires := get("expires_at"); rawExpires != "" {
			n, err := strconv.ParseInt(rawExpires, 10, 64)
			if err != nil || n < 0 {
				p.authModal.Error = "expires_at must be unix milliseconds"
				return
			}
			upsert.ExpiresAt = n
		}
	default:
		upsert.Type = "api"
		upsert.APIKey = get("api_key")
		if upsert.APIKey == "" && upsert.ID == "" {
			p.authModal.Error = "API key is required"
			return
		}
	}

	p.authModal.Editor = nil
	p.enqueueAuthModalAction(AuthModalAction{
		Kind:       AuthModalActionUpsert,
		Upsert:     &upsert,
		StatusHint: fmt.Sprintf("Saving %s credential for %s...", upsert.Type, upsert.Provider),
	})
}

func (p *HomePage) enqueueAuthModalAction(action AuthModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingAuthAction = &action
	p.authModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.authModal.Status = action.StatusHint
	}
	p.authModal.Error = ""
}

func (p *HomePage) advanceAuthModalFocus(delta int) {
	order := []authModalFocus{
		authModalFocusProviders,
		authModalFocusCredentials,
		authModalFocusProviderSearch,
		authModalFocusCredentialSearch,
	}
	idx := 0
	for i, focus := range order {
		if focus == p.authModal.Focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	p.authModal.Focus = order[idx]
}

func (p *HomePage) moveAuthModalSelection(delta int) {
	if delta == 0 {
		return
	}
	switch p.authModal.Focus {
	case authModalFocusProviders:
		matches := p.authFilteredProviderIndexes()
		if len(matches) == 0 {
			return
		}
		current := p.authModal.SelectedProvider
		pos := indexInList(matches, current)
		if pos < 0 {
			pos = 0
		}
		pos = (pos + delta + len(matches)) % len(matches)
		p.authModal.SelectedProvider = matches[pos]
		p.authModal.reconcileSelections()
	case authModalFocusCredentials:
		matches := p.authFilteredCredentialIndexes()
		if len(matches) == 0 {
			return
		}
		current := p.authModal.SelectedCredential
		pos := indexInList(matches, current)
		if pos < 0 {
			pos = 0
		}
		pos = (pos + delta + len(matches)) % len(matches)
		p.authModal.SelectedCredential = matches[pos]
	}
}

func (p *HomePage) deleteAuthModalSearchRune() {
	switch p.authModal.Focus {
	case authModalFocusProviderSearch:
		if len(p.authModal.ProviderSearch) == 0 {
			return
		}
		_, sz := utf8.DecodeLastRuneInString(p.authModal.ProviderSearch)
		if sz > 0 {
			p.authModal.ProviderSearch = p.authModal.ProviderSearch[:len(p.authModal.ProviderSearch)-sz]
		}
		p.authModal.reconcileSelections()
	case authModalFocusCredentialSearch:
		if len(p.authModal.CredentialSearch) == 0 {
			return
		}
		_, sz := utf8.DecodeLastRuneInString(p.authModal.CredentialSearch)
		if sz > 0 {
			p.authModal.CredentialSearch = p.authModal.CredentialSearch[:len(p.authModal.CredentialSearch)-sz]
		}
		p.authModal.reconcileSelections()
	}
}

func (p *HomePage) clearAuthModalSearch() {
	if p.authModal.Focus == authModalFocusProviderSearch {
		p.authModal.ProviderSearch = ""
	} else if p.authModal.Focus == authModalFocusCredentialSearch {
		p.authModal.CredentialSearch = ""
	} else {
		p.authModal.ProviderSearch = ""
		p.authModal.CredentialSearch = ""
	}
	p.authModal.reconcileSelections()
}

func (s *authModalState) reconcileSelections() {
	providers := s.filteredProviderIndexes()
	if len(providers) == 0 {
		s.SelectedProvider = -1
		s.SelectedCredential = -1
		return
	}
	if indexInList(providers, s.SelectedProvider) < 0 {
		s.SelectedProvider = providers[0]
	}

	credentials := s.filteredCredentialIndexes()
	if len(credentials) == 0 {
		s.SelectedCredential = -1
		return
	}
	if indexInList(credentials, s.SelectedCredential) < 0 {
		s.SelectedCredential = credentials[0]
	}
}

func (p *HomePage) selectedAuthProvider() (AuthModalProvider, bool) {
	idx := p.authModal.SelectedProvider
	if idx < 0 || idx >= len(p.authModal.Providers) {
		return AuthModalProvider{}, false
	}
	return p.authModal.Providers[idx], true
}

func (p *HomePage) selectedAuthProviderID() string {
	provider, ok := p.selectedAuthProvider()
	if !ok {
		return ""
	}
	return provider.ID
}

func (p *HomePage) authContextProviderID() string {
	selected := strings.ToLower(strings.TrimSpace(p.selectedAuthProviderID()))
	if selected != "" {
		return selected
	}
	matches := p.authFilteredProviderIndexes()
	if len(matches) == 1 {
		idx := matches[0]
		if idx >= 0 && idx < len(p.authModal.Providers) {
			return strings.ToLower(strings.TrimSpace(p.authModal.Providers[idx].ID))
		}
	}
	return ""
}

func (p *HomePage) selectedAuthCredential() (AuthModalCredential, bool) {
	idx := p.authModal.SelectedCredential
	if idx < 0 || idx >= len(p.authModal.Credentials) {
		return AuthModalCredential{}, false
	}
	return p.authModal.Credentials[idx], true
}

func (p *HomePage) selectedAuthCredentialID() string {
	credential, ok := p.selectedAuthCredential()
	if !ok {
		return ""
	}
	return credential.ID
}

func (p *HomePage) findAuthProviderIndex(providerID string) int {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return -1
	}
	for i, provider := range p.authModal.Providers {
		if strings.EqualFold(provider.ID, providerID) {
			return i
		}
	}
	return -1
}

func (p *HomePage) findAuthCredentialIndex(credentialID string) int {
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	if credentialID == "" {
		return -1
	}
	for i, credential := range p.authModal.Credentials {
		if strings.EqualFold(credential.ID, credentialID) {
			return i
		}
	}
	return -1
}

func (p *HomePage) authFilteredProviderIndexes() []int {
	return p.authModal.filteredProviderIndexes()
}

func (s *authModalState) filteredProviderIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(s.ProviderSearch))
	out := make([]int, 0, len(s.Providers))
	for i, provider := range s.Providers {
		if query == "" ||
			strings.Contains(strings.ToLower(provider.ID), query) ||
			strings.Contains(strings.ToLower(provider.Reason), query) ||
			strings.Contains(strings.ToLower(provider.RunReason), query) ||
			strings.Contains(strings.ToLower(providerAuthMethodsSummary(provider)), query) {
			out = append(out, i)
		}
	}
	return out
}

func (p *HomePage) authFilteredCredentialIndexes() []int {
	return p.authModal.filteredCredentialIndexes()
}

func (s *authModalState) filteredCredentialIndexes() []int {
	providerID := ""
	if s.SelectedProvider >= 0 && s.SelectedProvider < len(s.Providers) {
		providerID = strings.ToLower(strings.TrimSpace(s.Providers[s.SelectedProvider].ID))
	}
	query := strings.ToLower(strings.TrimSpace(s.CredentialSearch))
	out := make([]int, 0, len(s.Credentials))
	for i, credential := range s.Credentials {
		if providerID != "" && !strings.EqualFold(credential.Provider, providerID) {
			continue
		}
		if query != "" && !credentialMatchesQuery(credential, query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func credentialMatchesQuery(credential AuthModalCredential, query string) bool {
	if query == "" {
		return true
	}
	terms := strings.Fields(query)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "#") {
			if !credentialHasTag(credential, strings.TrimPrefix(term, "#")) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "tag:") {
			if !credentialHasTag(credential, strings.TrimPrefix(term, "tag:")) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "type:") {
			want := strings.TrimSpace(strings.TrimPrefix(term, "type:"))
			if want == "" || !strings.Contains(strings.ToLower(credential.AuthType), want) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "active:") {
			want := strings.TrimSpace(strings.TrimPrefix(term, "active:"))
			if (want == "true" || want == "1" || want == "yes") && !credential.Active {
				return false
			}
			if (want == "false" || want == "0" || want == "no") && credential.Active {
				return false
			}
			continue
		}
		line := strings.ToLower(strings.Join([]string{
			credential.Provider,
			credential.ID,
			credential.Label,
			credential.AuthType,
			credential.Last4,
			strings.Join(credential.Tags, ","),
		}, " "))
		if !strings.Contains(line, strings.ToLower(term)) {
			return false
		}
	}
	return true
}

func credentialHasTag(credential AuthModalCredential, tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return false
	}
	for _, item := range credential.Tags {
		if strings.Contains(strings.ToLower(item), tag) {
			return true
		}
	}
	return false
}

func (p *HomePage) drawAuthModal(s tcell.Screen) {
	if !p.authModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := w - 2
	if modalW < 70 {
		modalW = w - 2
	}
	modalH := h - 2
	if modalH < 16 {
		modalH = h - 2
	}
	rect := Rect{
		X: maxInt(1, (w-modalW)/2),
		Y: maxInt(1, (h-modalH)/2),
		W: modalW,
		H: modalH,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)

	title := "Auth Manager"
	if p.authModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	statusStyle := p.theme.TextMuted
	status := ""
	editorOwnsStatus := p.authModal.Editor != nil &&
		(p.authModal.Editor.Mode == "codex_callback" || p.authModal.Editor.Mode == "codex_browser_pending")
	if !editorOwnsStatus {
		status = strings.TrimSpace(p.authModal.Status)
		if strings.TrimSpace(p.authModal.Error) != "" {
			status = p.authModal.Error
			statusStyle = p.theme.Error
		}
	}
	if status == "" {
		if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") {
			status = "Enter/l choose Copilot auth method • n add token • e edit • a set active • d delete • r refresh • v verify"
		} else {
			status = "n add API key • o add OAuth • e edit • a set credential active • d delete • r refresh • v verify copilot • l login"
		}
	}
	statusLines := Wrap(status, rect.W-4)
	if len(statusLines) == 0 {
		statusLines = []string{""}
	}
	statusY := rect.Y + 1
	for i, line := range statusLines {
		y := statusY + i
		if y >= rect.Y+rect.H-1 {
			break
		}
		DrawText(s, rect.X+2, y, rect.W-4, statusStyle, line)
	}

	providerFocus := ""
	if p.authModal.Focus == authModalFocusProviderSearch {
		providerFocus = " [edit]"
	}
	credentialFocus := ""
	if p.authModal.Focus == authModalFocusCredentialSearch {
		credentialFocus = " [edit]"
	}
	providerSearchY := statusY + len(statusLines)
	credentialSearchY := providerSearchY + 1
	DrawText(s, rect.X+2, providerSearchY, rect.W-4, p.theme.TextMuted, "provider search"+providerFocus+": "+p.authModal.ProviderSearch)
	DrawText(s, rect.X+2, credentialSearchY, rect.W-4, p.theme.TextMuted, "credential search"+credentialFocus+": "+p.authModal.CredentialSearch)

	providerW := 28
	if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") && len(p.authFilteredCredentialIndexes()) == 0 {
		providerW = 22
	}
	enterHelp := "Enter login/add key/set active"
	if strings.EqualFold(strings.TrimSpace(p.authContextProviderID()), "copilot") {
		enterHelp = "Enter/l choose Copilot auth • r/v verify"
	}
	help := fmt.Sprintf("Tab focus • / provider search • f credential search • %s • n/o add • e edit • Esc close", enterHelp)
	helpLines := Wrap(help, rect.W-4)
	if len(helpLines) == 0 {
		helpLines = []string{""}
	}
	listRect := Rect{
		X: rect.X + 1,
		Y: credentialSearchY + 1,
		W: rect.W - 2,
		H: rect.H - ((credentialSearchY + 1) - rect.Y) - (len(helpLines) + 1),
	}
	if listRect.W >= 20 && listRect.H >= 4 {
		compactWidth := listRect.W < 72
		compactHeight := listRect.H < 8
		stackedLayout := compactWidth && !compactHeight && listRect.H >= 10
		if stackedLayout {
			providerRows := len(p.authFilteredProviderIndexes()) + 2
			providerH := maxInt(4, minInt(providerRows, maxInt(4, listRect.H/3)))
			if providerH > listRect.H-5 {
				providerH = maxInt(4, listRect.H-5)
			}
			credentialH := listRect.H - providerH - 1
			if credentialH < 4 {
				credentialH = 4
				providerH = maxInt(4, listRect.H-credentialH-1)
			}
			providerRect := Rect{X: listRect.X, Y: listRect.Y, W: listRect.W, H: providerH}
			credentialRect := Rect{X: listRect.X, Y: providerRect.Y + providerRect.H + 1, W: listRect.W, H: listRect.H - providerRect.H - 1}
			p.drawAuthProviderPane(s, providerRect)
			p.drawAuthCredentialPane(s, credentialRect)
		} else if compactWidth || compactHeight {
			if p.authModal.Focus == authModalFocusCredentials || p.authModal.Editor != nil {
				p.drawAuthCredentialPane(s, listRect)
			} else {
				p.drawAuthProviderPane(s, listRect)
			}
		} else {
			if providerW > listRect.W/2 {
				providerW = listRect.W / 2
			}
			providerRect := Rect{X: listRect.X, Y: listRect.Y, W: providerW, H: listRect.H}
			credentialRect := Rect{X: providerRect.X + providerRect.W + 1, Y: listRect.Y, W: listRect.W - providerRect.W - 1, H: listRect.H}
			if credentialRect.W < 20 {
				credentialRect.W = 20
			}

			p.drawAuthProviderPane(s, providerRect)
			p.drawAuthCredentialPane(s, credentialRect)
		}
	}
	helpStartY := rect.Y + rect.H - 1 - len(helpLines)
	for i, line := range helpLines {
		y := helpStartY + i
		if y <= rect.Y || y >= rect.Y+rect.H-1 {
			continue
		}
		DrawText(s, rect.X+2, y, rect.W-4, p.theme.TextMuted, line)
	}
	if p.authModal.ConfirmDelete {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.Warning, "Delete is armed: press d again to confirm")
	} else {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "↑/↓ move • p providers • c credentials")
	}

	if p.authModal.Editor != nil {
		p.drawAuthModalEditor(s, rect)
	} else if p.authModal.ConfirmDelete {
		p.drawAuthModalDeleteConfirm(s, rect)
	}
}

func (p *HomePage) drawAuthModalDeleteConfirm(s tcell.Screen, modal Rect) {
	credential, ok := p.selectedAuthCredential()
	if !ok {
		return
	}
	boxW := minInt(76, modal.W-6)
	if boxW < 48 {
		boxW = modal.W - 2
	}
	message := fmt.Sprintf("Press d again to delete %s/%s", credential.Provider, credential.ID)
	affectedAgents := p.authModalAffectedAgentsForProvider(credential.Provider)
	warningLines := []string{message, ""}
	if len(affectedAgents) > 0 {
		warningLines = append(warningLines, "These agents will reset to Inherit:")
		for _, name := range affectedAgents {
			warningLines = append(warningLines, "- "+name)
		}
		warningLines = append(warningLines, "", "Reassign them in /agents.")
	} else {
		warningLines = append(warningLines, "If this removes the provider auth, affected agents reset to Inherit.", "Reassign them in /agents.")
	}
	warningLines = append(warningLines, "The default model may also clear.")
	wrapped := make([]string, 0, len(warningLines)+2)
	for _, line := range warningLines {
		wrapped = append(wrapped, Wrap(line, maxInt(1, boxW-4))...)
	}
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	boxH := len(wrapped) + 4
	if boxH < 8 {
		boxH = 8
	}
	if boxH > modal.H-2 {
		boxH = modal.H - 2
	}
	if boxW <= 6 || boxH <= 4 {
		return
	}
	rect := Rect{
		X: modal.X + (modal.W-boxW)/2,
		Y: modal.Y + (modal.H-boxH)/2,
		W: boxW,
		H: boxH,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Warning, "Delete Credential?")
	bodyY := rect.Y + 1
	for i := 0; i < len(wrapped) && bodyY+i < rect.Y+rect.H-2; i++ {
		style := p.theme.Text
		if i == 0 {
			style = p.theme.Warning.Bold(true)
		}
		DrawCenteredText(s, rect.X+2, bodyY+i, rect.W-4, style, wrapped[i])
	}
	hint := "Press d again to confirm • Esc or move to cancel"
	DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, clampEllipsis(hint, rect.W-4))
}

func (p *HomePage) authModalAffectedAgentsForProvider(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	names := make([]string, 0, len(p.authModal.AgentProfiles))
	for _, profile := range p.authModal.AgentProfiles {
		if !strings.EqualFold(strings.TrimSpace(profile.Provider), provider) {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p *HomePage) drawAuthProviderPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Providers"
	if p.authModal.Focus == authModalFocusProviders {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	matches := p.authFilteredProviderIndexes()
	rowY := rect.Y + 1
	availableRows := rect.H - 2
	for i := 0; i < availableRows && i < len(matches); i++ {
		idx := matches[i]
		provider := p.authModal.Providers[idx]
		prefix := "  "
		if idx == p.authModal.SelectedProvider {
			prefix = "> "
		}
		health := "ready"
		switch {
		case !provider.Ready:
			health = "needs auth"
		case !provider.Runnable:
			if strings.Contains(strings.ToLower(strings.TrimSpace(provider.RunReason)), "search-only provider") {
				health = "search-only"
			} else {
				health = "not runnable"
			}
		}
		line := fmt.Sprintf("%s%s [%s]", prefix, provider.ID, health)
		DrawText(s, rect.X+1, rowY, rect.W-2, p.theme.Text, clampEllipsis(line, rect.W-2))
		rowY++
	}
	if len(matches) == 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no providers")
	}
}

func (p *HomePage) drawAuthCredentialPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Credentials"
	if p.authModal.Focus == authModalFocusCredentials {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	providerID := p.authContextProviderID()
	if providerID == "" {
		providerID = "all"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header+" · "+providerID)

	matches := p.authFilteredCredentialIndexes()
	rowY := rect.Y + 1
	availableRows := rect.H - 2
	for i := 0; i < availableRows && i < len(matches); i++ {
		idx := matches[i]
		credential := p.authModal.Credentials[idx]
		prefix := "  "
		if idx == p.authModal.SelectedCredential {
			prefix = "> "
		}
		activeMarker := " "
		activeTag := ""
		if credential.Active {
			activeMarker = "*"
			activeTag = " [active]"
		}
		label := authCredentialDisplayLabel(credential)
		capSummary := authCredentialCapabilitySummary(credential.Tags)
		line := fmt.Sprintf("%s%s %s%s%s", prefix, activeMarker, label, activeTag, capSummary)
		DrawText(s, rect.X+1, rowY, rect.W-2, p.theme.Text, clampEllipsis(line, rect.W-2))
		rowY++
	}
	if len(matches) == 0 {
		if strings.EqualFold(strings.TrimSpace(providerID), "copilot") {
			statusLine := "No Copilot auth source saved yet. Press Enter or l to choose one."
			statusStyle := p.theme.Warning
			if idx := p.findAuthProviderIndex("copilot"); idx >= 0 && idx < len(p.authModal.Providers) {
				provider := p.authModal.Providers[idx]
				reason := strings.TrimSpace(provider.Reason)
				if provider.Ready {
					if reason == "" {
						reason = "authenticated. New Copilot runs use the selected Swarm Copilot auth source until changed in /auth."
					}
					statusLine = "Copilot auth: " + reason
					statusStyle = p.theme.Success
				} else {
					if reason == "" {
						reason = "not authenticated."
					}
					statusLine = "Copilot auth: " + reason
				}
			}
			lines := []struct {
				style tcell.Style
				text  string
			}{
				{style: statusStyle, text: statusLine},
				{style: p.theme.Warning, text: "Default auth is the Copilot CLI sidecar: install `copilot`, run `copilot login`, then verify here."},
				{style: p.theme.Warning, text: "Use r or v to verify the currently selected Copilot auth source."},
			}
			maxLines := availableRows
			if maxLines < 0 {
				maxLines = 0
			}
			rowsUsed := 0
			for _, line := range lines {
				for _, wrapped := range Wrap(line.text, rect.W-4) {
					if rowsUsed >= maxLines {
						break
					}
					DrawText(s, rect.X+2, rect.Y+1+rowsUsed, rect.W-4, line.style, wrapped)
					rowsUsed++
				}
				if rowsUsed >= maxLines {
					break
				}
			}
			return
		}
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no credentials for current filter")
	}
}

func (p *HomePage) drawAuthModalEditor(s tcell.Screen, parent Rect) {
	editor := p.authModal.Editor
	if editor == nil {
		return
	}
	width := parent.W - 12
	if editor.Mode == "codex_callback" || editor.Mode == "codex_browser_pending" {
		width = parent.W - 4
	} else if width > 96 {
		width = 96
	}
	if width < 48 {
		width = parent.W - 4
	}
	renderRows := len(editor.Fields)
	editorStatusLines := []string(nil)
	editorStatusStyle := p.theme.TextMuted
	editorCommandLines := []string(nil)
	if editor.Mode == "codex_callback" || editor.Mode == "codex_browser_pending" {
		callbackStatus := strings.TrimSpace(p.authModal.Status)
		if errText := strings.TrimSpace(p.authModal.Error); errText != "" {
			callbackStatus = errText
			editorStatusStyle = p.theme.Error
		}
		if callbackStatus == "" && editor.Mode == "codex_callback" {
			callbackStatus = "Before sign-in: press Enter on Copy URL to copy the full auth URL. After sign-in: paste the callback URL or code here."
		}
		if callbackStatus == "" && editor.Mode == "codex_browser_pending" {
			callbackStatus = "Finish sign-in in your browser. This modal will close automatically after confirmation."
		}
		editorStatusLines = Wrap(callbackStatus, maxInt(1, width-4))
		if len(editorStatusLines) == 0 {
			editorStatusLines = []string{""}
		}
		commandText := "Need terminal flow? Run: swarm auth codex remote"
		if editor.Mode == "codex_browser_pending" {
			commandText = "If the browser did not open, press Enter to copy the auth URL."
		}
		editorCommandLines = Wrap(commandText, maxInt(1, width-4))
		if len(editorCommandLines) == 0 {
			editorCommandLines = []string{""}
		}
		renderRows += len(editorStatusLines) + len(editorCommandLines)
	}
	height := renderRows + 4
	if height < 10 {
		height = 10
	}
	maxHeight := parent.H - 2
	if maxHeight >= 10 && height > maxHeight {
		height = maxHeight
	}
	rect := Rect{
		X: parent.X + (parent.W-width)/2,
		Y: parent.Y + (parent.H-height)/2,
		W: width,
		H: height,
	}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	title := "Add " + strings.ToUpper(editor.Mode) + " Credential"
	if editor.Mode == "codex_login" {
		title = "Codex OAuth Login Setup"
	} else if editor.Mode == "codex_browser_pending" {
		title = "Codex Browser Sign-In"
	} else if editor.Mode == "codex_callback" {
		title = "Codex OAuth Callback"
	} else if strings.TrimSpace(editor.CredentialID) != "" {
		title = "Edit " + strings.ToUpper(editor.Mode) + " Credential"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	rowY := rect.Y + 1
	if editor.Mode == "codex_callback" || editor.Mode == "codex_browser_pending" {
		for _, line := range editorStatusLines {
			if rowY >= rect.Y+rect.H-2 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, editorStatusStyle, line)
			rowY++
		}
	}
	for i, field := range editor.Fields {
		if rowY >= rect.Y+rect.H-2 {
			break
		}
		style := p.theme.TextMuted
		value := field.Value
		if field.Key == "copy_url" {
			if i == editor.Selected {
				style = p.theme.Primary
			} else {
				style = p.theme.Text
			}
		} else if strings.TrimSpace(value) == "" {
			value = field.Placeholder
			if value == "" {
				value = "-"
			}
			style = p.theme.TextMuted
		} else if field.Secret {
			value = strings.Repeat("*", minInt(utf8.RuneCountInString(value), 24))
			style = p.theme.Text
		} else {
			style = p.theme.Text
		}
		prefix := "  "
		if i == editor.Selected {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s: %s", prefix, field.Label, value)
		if field.Key == "copy_url" {
			line = fmt.Sprintf("%s[%s]", prefix, value)
		}
		for _, part := range Wrap(line, maxInt(1, rect.W-4)) {
			if rowY >= rect.Y+rect.H-2 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, style, part)
			rowY++
		}
	}
	if (editor.Mode == "codex_callback" || editor.Mode == "codex_browser_pending") && rowY < rect.Y+rect.H-2 {
		for _, part := range editorCommandLines {
			if rowY >= rect.Y+rect.H-2 {
				break
			}
			DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.TextMuted, part)
			rowY++
		}
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, authEditorHelpText(editor.Mode))
}

func parseYN(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "y", "yes", "true", "1":
		return true
	default:
		return false
	}
}

func cycleCodexLoginMethodValue(value string, delta int) string {
	options := []string{"browser", "remote", "api key"}
	if len(options) == 0 {
		return "browser"
	}

	value = strings.ToLower(strings.TrimSpace(value))
	index := 0
	switch value {
	case "remote", "code", "manual":
		index = 1
	case "api", "api key", "apikey", "key":
		index = 2
	default:
		index = 0
	}

	if delta == 0 {
		return options[index]
	}

	step := delta
	if step > 0 {
		step = 1
	} else {
		step = -1
	}
	index = (index + step + len(options)) % len(options)
	return options[index]
}

func cycleCopilotLoginMethodValue(value string, delta int) string {
	options := []string{"cli", "gh", "token"}
	if len(options) == 0 {
		return "cli"
	}

	value = normalizeCopilotLoginMethod(value)
	index := 0
	switch value {
	case "gh":
		index = 1
	case "token", "api":
		index = 2
	default:
		index = 0
	}
	if delta == 0 {
		return options[index]
	}
	step := -1
	if delta > 0 {
		step = 1
	}
	index = (index + step + len(options)) % len(options)
	return options[index]
}

func cycleYNValue(value string, delta int) string {
	options := []string{"y", "n"}
	index := 0
	if !parseYN(value) {
		index = 1
	}
	if delta == 0 {
		return options[index]
	}
	step := -1
	if delta > 0 {
		step = 1
	}
	index = (index + step + len(options)) % len(options)
	return options[index]
}

func authEditorHelpText(mode string) string {
	switch mode {
	case "codex_callback":
		return "Tab/↑/↓ move • Enter copies URL or submits callback • Esc cancel"
	case "codex_browser_pending":
		return "Enter copies URL • Esc close"
	case "codex_login":
		return "Enter starts selected method • ↑ label first • ←/→ or 1/2/3 choose browser/remote/API key • Esc cancel"
	case "copilot_login":
		return "Tab/↑/↓ move • ←/→ toggle Method/active • type in Name or Token • Enter next/submit"
	default:
		return "Tab/↑/↓ move • Enter next/save • Esc cancel"
	}
}

func normalizeCodexLoginMethod(value string) (method string, openBrowser bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "remote", "code", "manual":
		return "code", false
	case "api", "api key", "apikey", "key":
		return "api", false
	default:
		return "auto", true
	}
}

func normalizeCopilotLoginMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cli", "copilot", "copilot-cli", "copilot_login":
		return "cli"
	case "gh", "github", "github-cli", "gh_auth":
		return "gh"
	case "token", "api", "github-token", "github_token":
		return "token"
	default:
		return ""
	}
}

func authCredentialDisplayLabel(credential AuthModalCredential) string {
	label := strings.TrimSpace(credential.Label)
	credentialID := strings.TrimSpace(credential.ID)
	last4 := strings.TrimSpace(credential.Last4)
	if label != "" && !strings.EqualFold(label, credentialID) {
		if last4 == "" {
			return label
		}
		return fmt.Sprintf("%s (ending in %s)", label, last4)
	}
	authType := strings.ToLower(strings.TrimSpace(credential.AuthType))
	switch authType {
	case "oauth":
		if last4 == "" {
			return "OAuth token"
		}
		return fmt.Sprintf("OAuth token ending in %s", last4)
	case "cli":
		if label != "" {
			return label
		}
		return "Copilot CLI login"
	case "gh":
		if label != "" {
			return label
		}
		return "GitHub CLI auth"
	case "api":
		if strings.EqualFold(strings.TrimSpace(credential.Provider), "copilot") {
			if last4 == "" {
				return "GitHub token"
			}
			return fmt.Sprintf("GitHub token ending in %s", last4)
		}
		if last4 == "" {
			return "API key"
		}
		return fmt.Sprintf("API key ending in %s", last4)
	default:
		if last4 == "" {
			return "API key"
		}
		return fmt.Sprintf("API key ending in %s", last4)
	}
}

func authCredentialCapabilitySummary(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(tags))
	caps := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if !strings.HasPrefix(tag, "cap:") {
			continue
		}
		capability := strings.TrimSpace(strings.TrimPrefix(tag, "cap:"))
		if capability == "" {
			continue
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		caps = append(caps, capability)
	}
	if len(caps) == 0 {
		return ""
	}
	sort.Strings(caps)
	return " [capabilities: " + strings.Join(caps, ", ") + "]"
}

func providerAuthMethodsSummary(provider AuthModalProvider) string {
	if len(provider.AuthMethods) == 0 {
		return ""
	}
	parts := make([]string, 0, len(provider.AuthMethods))
	for _, method := range provider.AuthMethods {
		label := strings.TrimSpace(method.Label)
		if label == "" {
			label = strings.TrimSpace(method.ID)
		}
		if label == "" {
			continue
		}
		credentialType := strings.TrimSpace(method.CredentialType)
		if credentialType != "" && !strings.EqualFold(credentialType, "none") {
			label = fmt.Sprintf("%s (%s)", label, credentialType)
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func indexInList(list []int, value int) int {
	for i, item := range list {
		if item == value {
			return i
		}
	}
	return -1
}

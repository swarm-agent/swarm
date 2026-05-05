package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

type ModelsModalProvider struct {
	ID       string
	Ready    bool
	Runnable bool
	Reason   string
}

type ModelsModalEntry struct {
	Provider         string
	Model            string
	ContextMode      string
	ContextWindow    int
	MaxOutputTokens  int
	Reasoning        bool
	Source           string
	Favorite         bool
	FavoriteLabel    string
	FavoriteThinking string
	AddedAt          int64
	UpdatedAt        int64
}

func (e ModelsModalEntry) DisplayName() string {
	name := model.DisplayModelName(e.Provider, e.Model)
	if model.Codex1MEnabled(e.Provider, e.Model, e.ContextMode) {
		return name + " (1m)"
	}
	return name
}

func supportsCodexFastRuntime(provider, modelName string) bool {
	return model.SupportsCodexFastMode(provider, modelName)
}

type ModelsModalActionKind string

const (
	ModelsModalActionRefresh        ModelsModalActionKind = "refresh"
	ModelsModalActionSetActiveModel ModelsModalActionKind = "set-active-model"
	ModelsModalActionToggleFavorite ModelsModalActionKind = "toggle-favorite"
	ModelsModalActionUpsertAPIKey   ModelsModalActionKind = "upsert-api-key"
)

type ModelsModalAction struct {
	Kind        ModelsModalActionKind
	Provider    string
	Model       string
	ContextMode string
	Thinking    string
	Favorite    bool
	Label       string
	APIKey      string
	KeyLabel    string
	SetActive   bool
	CloseAfter  bool
	StatusHint  string
	FastOnly    bool
}

type modelsModalFocus int

const (
	modelsModalFocusProviders modelsModalFocus = iota
	modelsModalFocusModels
	modelsModalFocusSearch
)

type modelsModalAuthEditorStep int

const (
	modelsModalAuthEditorStepAPIKey modelsModalAuthEditorStep = iota
	modelsModalAuthEditorStepLabel
)

type modelsModalAuthEditor struct {
	Provider string
	Step     modelsModalAuthEditorStep
	APIKey   string
	Label    string
}

type modelsModalState struct {
	Visible           bool
	Loading           bool
	Status            string
	Error             string
	Focus             modelsModalFocus
	Search            string
	FavoritesOnly     bool
	Providers         []ModelsModalProvider
	Models            []ModelsModalEntry
	SelectedProvider  int
	SelectedModel     int
	ProviderScroll    int
	ModelScroll       int
	ActiveProvider    string
	ActiveModel       string
	ActiveContextMode string
	ActiveThinking    string
	AuthEditor        *modelsModalAuthEditor
}

func (p *HomePage) ShowModelsModal() {
	p.modelsModal.Visible = true
	if p.modelsModal.Focus < modelsModalFocusProviders || p.modelsModal.Focus > modelsModalFocusSearch {
		p.modelsModal.Focus = modelsModalFocusModels
	}
}

func (p *HomePage) HideModelsModal() {
	p.modelsModal = modelsModalState{}
	p.modelsModalTargets = p.modelsModalTargets[:0]
	p.pendingModelsAction = nil
}

func (p *HomePage) ModelsModalVisible() bool {
	return p.modelsModal.Visible
}

func (p *HomePage) HandleModelsModalKey(ev *tcell.EventKey) {
	if p == nil || !p.modelsModal.Visible {
		return
	}
	p.handleModelsModalKey(ev)
}

func (p *HomePage) DrawModelsModalOverlay(s tcell.Screen) {
	if p == nil || !p.modelsModal.Visible {
		return
	}
	p.drawModelsModal(s)
}

func (p *HomePage) SetModelsModalLoading(loading bool) {
	p.modelsModal.Loading = loading
}

func (p *HomePage) SetModelsModalStatus(status string) {
	p.modelsModal.Status = strings.TrimSpace(status)
	if p.modelsModal.Status != "" {
		p.modelsModal.Error = ""
	}
}

func (p *HomePage) SetModelsModalError(err string) {
	p.modelsModal.Error = strings.TrimSpace(err)
	if p.modelsModal.Error != "" {
		p.modelsModal.Loading = false
	}
}

func (p *HomePage) SetModelsModalData(providers []ModelsModalProvider, models []ModelsModalEntry, activeProvider, activeModel, activeContextMode, activeThinking string) {
	selectedProvider := p.selectedModelsModalProviderID()
	selectedModel := p.selectedModelsModalModelKey()

	p.modelsModal.Providers = append([]ModelsModalProvider(nil), providers...)
	p.modelsModal.Models = append([]ModelsModalEntry(nil), models...)
	p.modelsModal.ActiveProvider = strings.ToLower(strings.TrimSpace(activeProvider))
	p.modelsModal.ActiveModel = strings.TrimSpace(activeModel)
	p.modelsModal.ActiveContextMode = strings.TrimSpace(activeContextMode)
	p.modelsModal.ActiveThinking = normalizeModelThinking(activeThinking)

	if len(p.modelsModal.Providers) == 0 {
		seen := make(map[string]struct{}, len(models))
		for _, model := range models {
			providerID := strings.ToLower(strings.TrimSpace(model.Provider))
			if providerID == "" {
				continue
			}
			if _, ok := seen[providerID]; ok {
				continue
			}
			seen[providerID] = struct{}{}
			p.modelsModal.Providers = append(p.modelsModal.Providers, ModelsModalProvider{ID: providerID})
		}
	}
	if selectedProvider == "" {
		selectedProvider = p.modelsModal.ActiveProvider
	}
	if selectedModel == "" && p.modelsModal.ActiveProvider != "" && p.modelsModal.ActiveModel != "" {
		selectedModel = modelKey(p.modelsModal.ActiveProvider, p.modelsModal.ActiveModel, p.modelsModal.ActiveContextMode)
	}
	p.modelsModal.SelectedProvider = p.findModelsModalProviderIndex(selectedProvider)
	if p.modelsModal.SelectedProvider < 0 && len(p.modelsModal.Providers) > 0 {
		p.modelsModal.SelectedProvider = 0
	}
	p.modelsModal.SelectedModel = p.findModelsModalModelIndex(selectedModel)
	p.modelsModal.reconcileSelections()
}

func (p *HomePage) PopModelsModalAction() (ModelsModalAction, bool) {
	if p.pendingModelsAction == nil {
		return ModelsModalAction{}, false
	}
	action := *p.pendingModelsAction
	p.pendingModelsAction = nil
	return action, true
}

func (p *HomePage) registerModelsModalTarget(rect Rect, action string, index int, meta string) {
	if action == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.modelsModalTargets = append(p.modelsModalTargets, clickTarget{Rect: rect, Action: action, Index: index, Meta: meta})
}

func (p *HomePage) modelsModalTargetAt(x, y int) (clickTarget, bool) {
	for i := len(p.modelsModalTargets) - 1; i >= 0; i-- {
		target := p.modelsModalTargets[i]
		if target.Rect.Contains(x, y) {
			return target, true
		}
	}
	return clickTarget{}, false
}

func (p *HomePage) selectedModelsModalProviderID() string {
	provider, ok := p.selectedModelsModalProvider()
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(provider.ID))
}

func (p *HomePage) selectedModelsModalProvider() (ModelsModalProvider, bool) {
	if len(p.modelsModal.Providers) == 0 {
		return ModelsModalProvider{}, false
	}
	if p.modelsModal.SelectedProvider < 0 || p.modelsModal.SelectedProvider >= len(p.modelsModal.Providers) {
		return ModelsModalProvider{}, false
	}
	return p.modelsModal.Providers[p.modelsModal.SelectedProvider], true
}

func (p *HomePage) selectedModelsModalModel() (ModelsModalEntry, bool) {
	indexes := p.modelsFilteredModelIndexes()
	if len(indexes) == 0 {
		return ModelsModalEntry{}, false
	}
	if p.modelsModal.SelectedModel < 0 || p.modelsModal.SelectedModel >= len(p.modelsModal.Models) {
		return ModelsModalEntry{}, false
	}
	for _, idx := range indexes {
		if idx == p.modelsModal.SelectedModel {
			return p.modelsModal.Models[idx], true
		}
	}
	return ModelsModalEntry{}, false
}

func (p *HomePage) selectedModelsModalModelKey() string {
	model, ok := p.selectedModelsModalModel()
	if !ok {
		return ""
	}
	return modelKey(model.Provider, model.Model, model.ContextMode)
}

func (p *HomePage) modelsModalProviderByID(providerID string) (ModelsModalProvider, bool) {
	index := p.findModelsModalProviderIndex(providerID)
	if index < 0 || index >= len(p.modelsModal.Providers) {
		return ModelsModalProvider{}, false
	}
	return p.modelsModal.Providers[index], true
}

func (p *HomePage) openModelsModalAuthEditor(providerID string) bool {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return false
	}
	p.modelsModal.AuthEditor = &modelsModalAuthEditor{
		Provider: providerID,
		Step:     modelsModalAuthEditorStepAPIKey,
	}
	p.modelsModal.Status = fmt.Sprintf("Paste API key for %s, then press Enter to save.", providerID)
	p.modelsModal.Error = ""
	p.modelsModal.Loading = false
	return true
}

func (p *HomePage) handleModelsModalKey(ev *tcell.EventKey) {
	if p.modelsModal.AuthEditor != nil {
		p.handleModelsModalAuthEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideModelsModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext):
		p.advanceModelsModalFocus(1)
		return
	case p.keybinds.Match(ev, KeybindModalFocusPrev):
		p.advanceModelsModalFocus(-1)
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		switch p.modelsModal.Focus {
		case modelsModalFocusSearch:
			p.modelsModal.Focus = modelsModalFocusModels
		default:
			p.modelsModal.Focus = modelsModalFocusProviders
		}
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		switch p.modelsModal.Focus {
		case modelsModalFocusProviders:
			p.modelsModal.Focus = modelsModalFocusModels
		default:
			p.modelsModal.Focus = modelsModalFocusSearch
		}
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveModelsModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveModelsModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalPageUp):
		p.moveModelsModalSelection(-10)
		return
	case p.keybinds.Match(ev, KeybindModalPageDown):
		p.moveModelsModalSelection(10)
		return
	case p.keybinds.Match(ev, KeybindModalJumpHome):
		p.moveModelsModalSelectionToEdge(true)
		return
	case p.keybinds.Match(ev, KeybindModalJumpEnd):
		p.moveModelsModalSelectionToEdge(false)
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.deleteModelsModalSearchRune()
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.modelsModal.Search = ""
		p.modelsModal.reconcileSelections()
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleModelsModalEnter()
		return
	}

	if ev.Key() == tcell.KeyRune {
		p.handleModelsModalRune(ev)
	}
}

func (p *HomePage) handleModelsModalMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil || !p.modelsModal.Visible {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.WheelUp != 0 || buttons&tcell.WheelDown != 0 {
		delta := -1
		if buttons&tcell.WheelDown != 0 {
			delta = 1
		}
		p.handleModelsModalMouseWheel(x, y, delta)
		return true
	}
	if buttons&tcell.Button1 == 0 {
		return true
	}
	target, ok := p.modelsModalTargetAt(x, y)
	if !ok {
		return true
	}
	p.activateModelsModalTarget(target)
	return true
}

func (p *HomePage) handleModelsModalMouseWheel(x, y, delta int) {
	if delta == 0 || p.modelsModal.AuthEditor != nil {
		return
	}
	target, ok := p.modelsModalTargetAt(x, y)
	if ok && target.Action == "models-provider" {
		p.modelsModal.Focus = modelsModalFocusProviders
	} else if ok && target.Action == "models-model" {
		p.modelsModal.Focus = modelsModalFocusModels
	}
	p.moveModelsModalSelection(delta * 3)
}

func (p *HomePage) activateModelsModalTarget(target clickTarget) {
	switch target.Action {
	case "models-provider":
		if target.Index < 0 || target.Index >= len(p.modelsModal.Providers) {
			return
		}
		wasSelected := target.Index == p.modelsModal.SelectedProvider
		p.modelsModal.Focus = modelsModalFocusProviders
		p.modelsModal.SelectedProvider = target.Index
		p.modelsModal.reconcileSelections()
		if wasSelected {
			p.handleModelsModalEnter()
		} else if provider, ok := p.selectedModelsModalProvider(); ok {
			p.modelsModal.Status = fmt.Sprintf("selected provider: %s", strings.ToLower(strings.TrimSpace(provider.ID)))
			p.modelsModal.Error = ""
		}
	case "models-model":
		if target.Index < 0 || target.Index >= len(p.modelsModal.Models) {
			return
		}
		p.modelsModal.Focus = modelsModalFocusModels
		p.modelsModal.SelectedModel = target.Index
		p.handleModelsModalEnter()
	case "models-search":
		p.modelsModal.Focus = modelsModalFocusSearch
		p.modelsModal.Status = "type to search models"
		p.modelsModal.Error = ""
	case "models-favorites":
		p.modelsModal.FavoritesOnly = !p.modelsModal.FavoritesOnly
		p.modelsModal.Focus = modelsModalFocusModels
		p.modelsModal.reconcileSelections()
		if p.modelsModal.FavoritesOnly {
			p.modelsModal.Status = "Favorites filter: on"
		} else {
			p.modelsModal.Status = "Favorites filter: off"
		}
		p.modelsModal.Error = ""
	case "models-auth-api":
		if p.modelsModal.AuthEditor != nil {
			p.modelsModal.AuthEditor.Step = modelsModalAuthEditorStepAPIKey
		}
	case "models-auth-label":
		if p.modelsModal.AuthEditor != nil {
			p.modelsModal.AuthEditor.Step = modelsModalAuthEditorStepLabel
		}
	case "models-auth-submit":
		p.submitModelsModalAuthEditor()
	}
}

func (p *HomePage) handleModelsModalRune(ev *tcell.EventKey) {
	r := ev.Rune()
	if p.modelsModal.Focus == modelsModalFocusSearch {
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			p.modelsModal.Search += string(r)
			p.modelsModal.reconcileSelections()
		}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalSearchFocus):
		p.modelsModal.Focus = modelsModalFocusSearch
	case p.keybinds.Match(ev, KeybindModelsFocusProviders):
		p.modelsModal.Focus = modelsModalFocusProviders
	case p.keybinds.Match(ev, KeybindModelsFocusModels):
		p.modelsModal.Focus = modelsModalFocusModels
	case p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveModelsModalSelection(1)
	case p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveModelsModalSelection(-1)
	case p.keybinds.Match(ev, KeybindModelsRefresh):
		p.enqueueModelsModalAction(ModelsModalAction{
			Kind:       ModelsModalActionRefresh,
			Provider:   p.selectedModelsModalProviderID(),
			StatusHint: "Refreshing model list...",
		})
	case p.keybinds.Match(ev, KeybindModelsAddAuth):
		if providerID := p.selectedModelsModalProviderID(); providerID != "" {
			p.openModelsModalAuthEditor(providerID)
		} else {
			p.modelsModal.Status = "Select a provider first"
			p.modelsModal.Error = ""
		}
	case p.keybinds.Match(ev, KeybindModelsToggleFavoritesFilter):
		p.modelsModal.FavoritesOnly = !p.modelsModal.FavoritesOnly
		p.modelsModal.reconcileSelections()
		if p.modelsModal.FavoritesOnly {
			p.modelsModal.Status = "Favorites filter: on"
		} else {
			p.modelsModal.Status = "Favorites filter: off"
		}
	case p.keybinds.Match(ev, KeybindModelsToggleFavorite):
		model, ok := p.selectedModelsModalModel()
		if !ok {
			return
		}
		desiredFavorite := !model.Favorite
		hint := "Removing favorite..."
		if desiredFavorite {
			hint = "Adding favorite..."
		}
		p.enqueueModelsModalAction(ModelsModalAction{
			Kind:        ModelsModalActionToggleFavorite,
			Provider:    model.Provider,
			Model:       model.Model,
			ContextMode: model.ContextMode,
			Thinking:    modelThinkingForAction(model, p.modelsModal.ActiveThinking),
			Label:       strings.TrimSpace(model.FavoriteLabel),
			Favorite:    desiredFavorite,
			StatusHint:  hint,
		})
	case p.keybinds.Match(ev, KeybindModelsThinkingOff):
		thinking := "off"
		p.enqueueModelsModalThinkingAction(thinking, true)
	case p.keybinds.Match(ev, KeybindModelsThinkingLow):
		thinking := "low"
		p.enqueueModelsModalThinkingAction(thinking, true)
	case p.keybinds.Match(ev, KeybindModelsThinkingMedium):
		thinking := "medium"
		p.enqueueModelsModalThinkingAction(thinking, true)
	case p.keybinds.Match(ev, KeybindModelsThinkingHigh):
		thinking := "high"
		p.enqueueModelsModalThinkingAction(thinking, true)
	case p.keybinds.Match(ev, KeybindModelsThinkingXHigh):
		thinking := "xhigh"
		p.enqueueModelsModalThinkingAction(thinking, true)
	default:
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			p.modelsModal.Focus = modelsModalFocusSearch
			p.modelsModal.Search += string(r)
			p.modelsModal.reconcileSelections()
		}
	}
}

func (p *HomePage) enqueueModelsModalThinkingAction(thinking string, closeAfter bool) {
	thinking = normalizeModelThinking(thinking)
	if thinking == "" {
		return
	}

	model, ok := p.selectedModelsModalModel()
	if !ok {
		p.modelsModal.Status = "Select a model first"
		p.modelsModal.Error = ""
		return
	}

	provider, found := p.modelsModalProviderByID(model.Provider)
	if found && !provider.Ready {
		p.openModelsModalAuthEditor(provider.ID)
		return
	}
	if found && !provider.Runnable {
		reason := strings.TrimSpace(provider.Reason)
		if reason == "" {
			reason = "runner unavailable"
		}
		p.modelsModal.Status = fmt.Sprintf("%s is not runnable (%s)", strings.ToLower(strings.TrimSpace(provider.ID)), reason)
		p.modelsModal.Error = ""
		return
	}

	if !model.Reasoning {
		thinking = "off"
	}

	providerID := strings.ToLower(strings.TrimSpace(model.Provider))
	if thinking == "xhigh" && providerID == "copilot" {
		p.modelsModal.Status = "copilot does not support xhigh thinking"
		p.modelsModal.Error = ""
		return
	}

	p.enqueueModelsModalAction(ModelsModalAction{
		Kind:        ModelsModalActionSetActiveModel,
		Provider:    model.Provider,
		Model:       model.Model,
		ContextMode: model.ContextMode,
		Thinking:    thinking,
		CloseAfter:  closeAfter,
		StatusHint:  fmt.Sprintf("Setting thinking: %s (%s/%s) ...", thinking, model.Provider, model.Model),
		FastOnly:    supportsCodexFastRuntime(model.Provider, model.Model),
	})
}

func (p *HomePage) handleModelsModalEnter() {
	switch p.modelsModal.Focus {
	case modelsModalFocusProviders:
		provider, ok := p.selectedModelsModalProvider()
		if !ok {
			return
		}
		providerID := strings.ToLower(strings.TrimSpace(provider.ID))
		if providerID == "" {
			return
		}
		if !provider.Ready {
			p.openModelsModalAuthEditor(providerID)
			return
		}
		if !provider.Runnable {
			reason := strings.TrimSpace(provider.Reason)
			if reason == "" {
				reason = "runner unavailable"
			}
			if strings.Contains(strings.ToLower(reason), "no model runner") {
				p.modelsModal.Status = fmt.Sprintf("%s is auth-only; configure it in /auth", providerID)
			} else {
				p.modelsModal.Status = fmt.Sprintf("%s is not runnable (%s)", providerID, reason)
			}
			p.modelsModal.Error = ""
			return
		}
		p.modelsModal.Focus = modelsModalFocusModels
		matches := p.modelsFilteredModelIndexes()
		if len(matches) == 0 {
			p.modelsModal.Status = fmt.Sprintf("%s selected. No models loaded yet; press r to refresh.", providerID)
		} else {
			p.modelsModal.Status = fmt.Sprintf("%s selected. Press Enter to set the default model and close.", providerID)
		}
		p.modelsModal.Error = ""
		return
	default:
		model, ok := p.selectedModelsModalModel()
		if !ok {
			provider, hasProvider := p.selectedModelsModalProvider()
			if hasProvider && !provider.Ready {
				p.openModelsModalAuthEditor(provider.ID)
			}
			return
		}
		provider, found := p.modelsModalProviderByID(model.Provider)
		if found && !provider.Ready {
			p.openModelsModalAuthEditor(provider.ID)
			return
		}
		if found && !provider.Runnable {
			reason := strings.TrimSpace(provider.Reason)
			if reason == "" {
				reason = "runner unavailable"
			}
			if strings.Contains(strings.ToLower(reason), "no model runner") {
				p.modelsModal.Status = fmt.Sprintf("%s is auth-only; configure it in /auth", strings.ToLower(strings.TrimSpace(provider.ID)))
			} else {
				p.modelsModal.Status = fmt.Sprintf("%s is not runnable (%s)", strings.ToLower(strings.TrimSpace(provider.ID)), reason)
			}
			p.modelsModal.Error = ""
			return
		}
		thinking := modelThinkingForAction(model, p.modelsModal.ActiveThinking)
		p.enqueueModelsModalAction(ModelsModalAction{
			Kind:        ModelsModalActionSetActiveModel,
			Provider:    model.Provider,
			Model:       model.Model,
			ContextMode: model.ContextMode,
			Thinking:    thinking,
			CloseAfter:  true,
			StatusHint:  fmt.Sprintf("Setting default model: %s/%s ...", model.Provider, model.Model),
			FastOnly:    supportsCodexFastRuntime(model.Provider, model.Model),
		})
	}
}

func (p *HomePage) handleModelsModalAuthEditorKey(ev *tcell.EventKey) {
	editor := p.modelsModal.AuthEditor
	if editor == nil {
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.modelsModal.AuthEditor = nil
		p.modelsModal.Status = "API key setup canceled"
		p.modelsModal.Error = ""
		return
	case p.keybinds.MatchAny(ev, KeybindEditorFocusNext, KeybindEditorFocusPrev, KeybindEditorMoveUp, KeybindEditorMoveDown):
		if editor.Step == modelsModalAuthEditorStepAPIKey {
			editor.Step = modelsModalAuthEditorStepLabel
		} else {
			editor.Step = modelsModalAuthEditorStepAPIKey
		}
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		value := p.modelsModalAuthEditorValuePointer(editor)
		if value == nil || len(*value) == 0 {
			return
		}
		_, size := utf8.DecodeLastRuneInString(*value)
		if size > 0 {
			*value = (*value)[:len(*value)-size]
		}
		return
	case p.keybinds.Match(ev, KeybindEditorClear):
		value := p.modelsModalAuthEditorValuePointer(editor)
		if value != nil {
			*value = ""
		}
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if editor.Step == modelsModalAuthEditorStepAPIKey {
			if strings.TrimSpace(editor.APIKey) == "" {
				p.modelsModal.Status = "API key is required. Paste it here, then press Enter."
				p.modelsModal.Error = ""
				return
			}
			p.submitModelsModalAuthEditor()
			return
		}
		p.submitModelsModalAuthEditor()
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if !unicode.IsPrint(r) || utf8.RuneLen(r) <= 0 {
			return
		}
		value := p.modelsModalAuthEditorValuePointer(editor)
		if value != nil {
			*value += string(r)
		}
	}
}

func (p *HomePage) modelsModalAuthEditorValuePointer(editor *modelsModalAuthEditor) *string {
	if editor == nil {
		return nil
	}
	if editor.Step == modelsModalAuthEditorStepAPIKey {
		return &editor.APIKey
	}
	return &editor.Label
}

func (p *HomePage) submitModelsModalAuthEditor() {
	editor := p.modelsModal.AuthEditor
	if editor == nil {
		return
	}

	providerID := strings.ToLower(strings.TrimSpace(editor.Provider))
	apiKey := strings.TrimSpace(editor.APIKey)
	if providerID == "" {
		p.modelsModal.Status = "Provider is required"
		p.modelsModal.Error = ""
		return
	}
	if apiKey == "" {
		p.modelsModal.Status = "API key is required. Paste it here, then press Enter."
		p.modelsModal.Error = ""
		editor.Step = modelsModalAuthEditorStepAPIKey
		return
	}

	keyLabel := strings.TrimSpace(editor.Label)
	p.modelsModal.AuthEditor = nil
	p.enqueueModelsModalAction(ModelsModalAction{
		Kind:       ModelsModalActionUpsertAPIKey,
		Provider:   providerID,
		APIKey:     apiKey,
		KeyLabel:   keyLabel,
		SetActive:  true,
		StatusHint: fmt.Sprintf("Saving API key for %s ...", providerID),
	})
}

func (p *HomePage) enqueueModelsModalAction(action ModelsModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingModelsAction = &action
	p.modelsModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.modelsModal.Status = action.StatusHint
	}
	p.modelsModal.Error = ""
}

func (p *HomePage) advanceModelsModalFocus(delta int) {
	order := []modelsModalFocus{
		modelsModalFocusProviders,
		modelsModalFocusModels,
		modelsModalFocusSearch,
	}
	idx := 0
	for i, focus := range order {
		if focus == p.modelsModal.Focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	p.modelsModal.Focus = order[idx]
}

func (p *HomePage) moveModelsModalSelection(delta int) {
	if delta == 0 {
		return
	}
	switch p.modelsModal.Focus {
	case modelsModalFocusProviders:
		if len(p.modelsModal.Providers) == 0 {
			return
		}
		next := p.modelsModal.SelectedProvider + delta
		if next < 0 {
			next = 0
		}
		if next >= len(p.modelsModal.Providers) {
			next = len(p.modelsModal.Providers) - 1
		}
		p.modelsModal.SelectedProvider = next
		p.modelsModal.reconcileSelections()
	default:
		indexes := p.modelsFilteredModelIndexes()
		if len(indexes) == 0 {
			return
		}
		current := 0
		for i, idx := range indexes {
			if idx == p.modelsModal.SelectedModel {
				current = i
				break
			}
		}
		next := current + delta
		if next < 0 {
			next = 0
		}
		if next >= len(indexes) {
			next = len(indexes) - 1
		}
		p.modelsModal.SelectedModel = indexes[next]
	}
}

func (p *HomePage) moveModelsModalSelectionToEdge(first bool) {
	switch p.modelsModal.Focus {
	case modelsModalFocusProviders:
		if len(p.modelsModal.Providers) == 0 {
			return
		}
		if first {
			p.modelsModal.SelectedProvider = 0
		} else {
			p.modelsModal.SelectedProvider = len(p.modelsModal.Providers) - 1
		}
		p.modelsModal.reconcileSelections()
	default:
		indexes := p.modelsFilteredModelIndexes()
		if len(indexes) == 0 {
			return
		}
		if first {
			p.modelsModal.SelectedModel = indexes[0]
		} else {
			p.modelsModal.SelectedModel = indexes[len(indexes)-1]
		}
	}
}

func (p *HomePage) deleteModelsModalSearchRune() {
	if p.modelsModal.Focus != modelsModalFocusSearch {
		return
	}
	if len(p.modelsModal.Search) == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(p.modelsModal.Search)
	if size <= 0 {
		return
	}
	p.modelsModal.Search = p.modelsModal.Search[:len(p.modelsModal.Search)-size]
	p.modelsModal.reconcileSelections()
}

func (m *modelsModalState) reconcileSelections() {
	if len(m.Providers) == 0 {
		m.SelectedProvider = -1
		m.SelectedModel = -1
		return
	}
	if m.SelectedProvider < 0 {
		m.SelectedProvider = 0
	}
	if m.SelectedProvider >= len(m.Providers) {
		m.SelectedProvider = len(m.Providers) - 1
	}
}

func (p *HomePage) findModelsModalProviderIndex(providerID string) int {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return -1
	}
	for i, provider := range p.modelsModal.Providers {
		if strings.EqualFold(strings.TrimSpace(provider.ID), providerID) {
			return i
		}
	}
	return -1
}

func (p *HomePage) findModelsModalModelIndex(key string) int {
	key = strings.TrimSpace(key)
	if key == "" {
		return -1
	}
	for i, model := range p.modelsModal.Models {
		if modelKey(model.Provider, model.Model, model.ContextMode) == key {
			return i
		}
	}
	return -1
}

func (p *HomePage) modelsFilteredModelIndexes() []int {
	selectedProvider := p.selectedModelsModalProviderID()
	query := strings.ToLower(strings.TrimSpace(p.modelsModal.Search))
	out := make([]int, 0, len(p.modelsModal.Models))
	for i, model := range p.modelsModal.Models {
		if selectedProvider != "" && !strings.EqualFold(strings.TrimSpace(model.Provider), selectedProvider) {
			continue
		}
		if p.modelsModal.FavoritesOnly && !model.Favorite {
			continue
		}
		if query != "" && !modelMatchesQuery(model, query) {
			continue
		}
		out = append(out, i)
	}
	if len(out) == 0 {
		p.modelsModal.SelectedModel = -1
		return out
	}
	for _, idx := range out {
		if idx == p.modelsModal.SelectedModel {
			return out
		}
	}
	p.modelsModal.SelectedModel = out[0]
	return out
}

func modelMatchesQuery(model ModelsModalEntry, query string) bool {
	if query == "" {
		return true
	}
	terms := strings.Fields(query)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "provider:") {
			needle := strings.TrimSpace(strings.TrimPrefix(term, "provider:"))
			if needle == "" || !strings.Contains(strings.ToLower(model.Provider), needle) {
				return false
			}
			continue
		}
		if strings.HasPrefix(term, "fav:") {
			want := strings.TrimSpace(strings.TrimPrefix(term, "fav:"))
			switch want {
			case "1", "true", "yes":
				if !model.Favorite {
					return false
				}
			case "0", "false", "no":
				if model.Favorite {
					return false
				}
			default:
				return false
			}
			continue
		}
		if !strings.Contains(strings.ToLower(model.Provider), term) &&
			!strings.Contains(strings.ToLower(model.Model), term) &&
			!strings.Contains(strings.ToLower(model.DisplayName()), term) &&
			!strings.Contains(strings.ToLower(model.Source), term) &&
			!strings.Contains(strings.ToLower(model.FavoriteLabel), term) {
			return false
		}
	}
	return true
}

func modelThinkingForAction(model ModelsModalEntry, activeThinking string) string {
	if !model.Reasoning {
		return "off"
	}

	resolve := func() string {
		if thinking := normalizeModelThinking(model.FavoriteThinking); thinking != "" {
			return thinking
		}
		if thinking := normalizeModelThinking(activeThinking); thinking != "" {
			return thinking
		}
		return "high"
	}

	thinking := resolve()
	providerID := strings.ToLower(strings.TrimSpace(model.Provider))
	if thinking == "xhigh" && providerID == "copilot" {
		return "high"
	}
	return thinking
}

func normalizeModelThinking(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "off", "low", "medium", "high", "xhigh":
		return value
	default:
		return ""
	}
}

func modelKey(providerID, modelID, contextMode string) string {
	return strings.ToLower(strings.TrimSpace(providerID)) + "/" + strings.TrimSpace(modelID) + "#" + strings.TrimSpace(contextMode)
}

func (p *HomePage) drawModelsModal(s tcell.Screen) {
	p.modelsModalTargets = p.modelsModalTargets[:0]
	if !p.modelsModal.Visible {
		return
	}

	w, h := s.Size()
	if w < 28 || h < 14 {
		return
	}

	fg, _, _ := p.theme.Text.Decompose()
	bg := tcell.StyleDefault.Background(tcell.NewRGBColor(0, 0, 0)).Foreground(fg)
	FillRect(s, Rect{X: 0, Y: 0, W: w, H: h}, bg)

	rectW := w - 4
	if w < 64 {
		rectW = w - 2
	}
	if rectW > 118 {
		rectW = 118
	}
	if rectW < 28 {
		rectW = w
	}
	rectH := h - 4
	if h < 18 {
		rectH = h - 2
	}
	if rectH > 30 {
		rectH = 30
	}
	if rectH < 12 {
		rectH = h
	}
	rect := Rect{X: maxInt(0, (w-rectW)/2), Y: maxInt(0, (h-rectH)/2), W: rectW, H: rectH}
	DrawBox(s, rect, p.theme.BorderActive)

	title := "Models"
	if providerID := p.selectedModelsModalProviderID(); providerID != "" {
		title = "Models · " + providerID
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Primary, title)

	statusStyle := p.theme.TextMuted
	status := strings.TrimSpace(p.modelsModal.Status)
	if p.modelsModal.Loading {
		statusStyle = p.theme.Accent
		if status == "" {
			status = "Loading models..."
		}
	} else if strings.TrimSpace(p.modelsModal.Error) != "" {
		statusStyle = p.theme.Warning
		status = p.modelsModal.Error
	}
	if status == "" {
		status = "Enter selects model. 1=off 2=low 3=medium 4=high 5=xhigh. Fast is on/off and only available on Codex gpt-5.4/gpt-5.5."
	}
	statusLines := Wrap(status, rect.W-4)
	if len(statusLines) == 0 {
		statusLines = []string{""}
	}
	for i, line := range statusLines {
		y := rect.Y + 1 + i
		if y >= rect.Y+3 {
			break
		}
		DrawText(s, rect.X+2, y, rect.W-4, statusStyle, line)
	}
	filterState := "off"
	if p.modelsModal.FavoritesOnly {
		filterState = "on"
	}
	matchCount := len(p.modelsFilteredModelIndexes())
	metaLine := fmt.Sprintf("search: %s   favorites: %s   sort: favorites/newest   matches: %d", p.modelsModal.Search, filterState, matchCount)
	DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.TextMuted, metaLine)
	p.registerModelsModalTarget(Rect{X: rect.X + 2, Y: rect.Y + 2, W: minInt(rect.W-4, maxInt(8, utf8.RuneCountInString("search: ")+utf8.RuneCountInString(p.modelsModal.Search))), H: 1}, "models-search", -1, "")
	favNeedle := "favorites: " + filterState
	if favX := strings.Index(metaLine, favNeedle); favX >= 0 {
		p.registerModelsModalTarget(Rect{X: rect.X + 2 + favX, Y: rect.Y + 2, W: utf8.RuneCountInString(favNeedle), H: 1}, "models-favorites", -1, "")
	}

	listRect := Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6}
	if listRect.W < 28 || listRect.H < 6 {
		return
	}

	compactLayout := listRect.W < 66 || listRect.H < 8
	if compactLayout {
		if p.modelsModal.Focus == modelsModalFocusModels || len(p.modelsModal.Providers) <= 1 {
			p.drawModelsModalModelPane(s, listRect)
		} else {
			p.drawModelsModalProviderPane(s, listRect)
		}
	} else {
		providerW := maxInt(12, listRect.W/3)
		if providerW > 24 {
			providerW = 24
		}
		if providerW > listRect.W/2 {
			providerW = listRect.W / 2
		}
		providerRect := Rect{X: listRect.X, Y: listRect.Y, W: providerW, H: listRect.H}
		modelRect := Rect{X: providerRect.X + providerRect.W + 1, Y: listRect.Y, W: listRect.W - providerRect.W - 1, H: listRect.H}

		p.drawModelsModalProviderPane(s, providerRect)
		p.drawModelsModalModelPane(s, modelRect)
	}

	help := "Enter select/set default | 1..5 set thinking | Fast on/off shown only on Codex gpt-5.4/gpt-5.5 | a favorite | n add API key | f favorites-only | r refresh | / search | Tab focus | Esc close"
	for _, line := range Wrap(help, rect.W-4) {
		DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(line, rect.W-4))
		break
	}

	if p.modelsModal.AuthEditor != nil {
		p.drawModelsModalAuthEditor(s, rect)
	}
}

func (p *HomePage) drawModelsModalProviderPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Providers"
	if p.modelsModal.Focus == modelsModalFocusProviders {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	rowY := rect.Y + 1
	maxRows := rect.H - 2
	if maxRows <= 0 {
		return
	}
	maxScroll := maxInt(0, len(p.modelsModal.Providers)-maxRows)
	if p.modelsModal.ProviderScroll < 0 {
		p.modelsModal.ProviderScroll = 0
	}
	if p.modelsModal.ProviderScroll > maxScroll {
		p.modelsModal.ProviderScroll = maxScroll
	}
	if p.modelsModal.SelectedProvider >= 0 {
		if p.modelsModal.ProviderScroll > p.modelsModal.SelectedProvider {
			p.modelsModal.ProviderScroll = p.modelsModal.SelectedProvider
		}
		if p.modelsModal.ProviderScroll+maxRows-1 < p.modelsModal.SelectedProvider {
			p.modelsModal.ProviderScroll = p.modelsModal.SelectedProvider - maxRows + 1
		}
		if p.modelsModal.ProviderScroll < 0 {
			p.modelsModal.ProviderScroll = 0
		}
		if p.modelsModal.ProviderScroll > maxScroll {
			p.modelsModal.ProviderScroll = maxScroll
		}
	}
	for i := 0; i < maxRows; i++ {
		providerIdx := p.modelsModal.ProviderScroll + i
		if providerIdx < 0 || providerIdx >= len(p.modelsModal.Providers) {
			break
		}
		provider := p.modelsModal.Providers[providerIdx]
		prefix := "  "
		style := p.theme.TextMuted
		if providerIdx == p.modelsModal.SelectedProvider {
			prefix = "> "
			style = p.theme.Text
		}
		health := "ready"
		switch {
		case !provider.Ready:
			health = "auth needed"
		case !provider.Runnable:
			health = "not runnable"
		}
		line := fmt.Sprintf("%s%s [%s]", prefix, provider.ID, health)
		rowRect := Rect{X: rect.X + 1, Y: rowY, W: rect.W - 2, H: 1}
		DrawText(s, rowRect.X, rowRect.Y, rowRect.W, style, clampEllipsis(line, rowRect.W))
		p.registerModelsModalTarget(rowRect, "models-provider", providerIdx, provider.ID)
		rowY++
	}
	if len(p.modelsModal.Providers) == 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no providers")
	}
}

func (p *HomePage) drawModelsModalModelPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Models (favorites/newest)"
	if p.modelsModal.Focus == modelsModalFocusModels {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	matches := p.modelsFilteredModelIndexes()
	contentTop := rect.Y + 1
	if strings.EqualFold(strings.TrimSpace(p.selectedModelsModalProviderID()), "copilot") {
		warning := "Some models are not available if not on paid plans, please be advised."
		DrawText(s, rect.X+2, contentTop, rect.W-4, p.theme.Warning, clampEllipsis(warning, rect.W-4))
		contentTop++
	}

	availableRows := rect.H - (contentTop - rect.Y) - 1
	if availableRows <= 0 {
		return
	}
	cardHeight := 2
	maxCards := availableRows / cardHeight
	if maxCards < 1 {
		maxCards = 1
		cardHeight = 1
	}
	selectedMatch := 0
	for i, idx := range matches {
		if idx == p.modelsModal.SelectedModel {
			selectedMatch = i
			break
		}
	}
	maxScroll := maxInt(0, len(matches)-maxCards)
	if p.modelsModal.ModelScroll < 0 {
		p.modelsModal.ModelScroll = 0
	}
	if p.modelsModal.ModelScroll > maxScroll {
		p.modelsModal.ModelScroll = maxScroll
	}
	if p.modelsModal.ModelScroll > selectedMatch {
		p.modelsModal.ModelScroll = selectedMatch
	}
	if p.modelsModal.ModelScroll+maxCards-1 < selectedMatch {
		p.modelsModal.ModelScroll = selectedMatch - maxCards + 1
	}
	if p.modelsModal.ModelScroll < 0 {
		p.modelsModal.ModelScroll = 0
	}
	if p.modelsModal.ModelScroll > maxScroll {
		p.modelsModal.ModelScroll = maxScroll
	}
	rowY := contentTop
	for i := 0; i < maxCards; i++ {
		matchIdx := p.modelsModal.ModelScroll + i
		if matchIdx < 0 || matchIdx >= len(matches) {
			break
		}
		idx := matches[matchIdx]
		model := p.modelsModal.Models[idx]
		prefix := "  "
		style := p.theme.TextMuted
		if idx == p.modelsModal.SelectedModel {
			prefix = "> "
			style = p.theme.Text
		}
		favorite := " "
		if model.Favorite {
			favorite = "*"
		}
		active := ""
		if strings.EqualFold(strings.TrimSpace(model.Provider), strings.TrimSpace(p.modelsModal.ActiveProvider)) &&
			strings.EqualFold(strings.TrimSpace(model.Model), strings.TrimSpace(p.modelsModal.ActiveModel)) &&
			strings.EqualFold(strings.TrimSpace(model.ContextMode), strings.TrimSpace(p.modelsModal.ActiveContextMode)) {
			active = " [active]"
		}
		title := fmt.Sprintf("%s[%s] %s%s", prefix, favorite, model.DisplayName(), active)
		cardRect := Rect{X: rect.X + 1, Y: rowY, W: rect.W - 2, H: cardHeight}
		DrawText(s, rect.X+1, rowY, rect.W-2, style, title)
		p.registerModelsModalTarget(cardRect, "models-model", idx, modelKey(model.Provider, model.Model, model.ContextMode))
		rowY++
		if cardHeight > 1 {
			info := modelsModalFeatureSummary(model)
			if info == "" {
				info = "no metadata"
			}
			infoStyle := p.theme.TextMuted
			if idx == p.modelsModal.SelectedModel {
				infoStyle = p.theme.Text
			}
			DrawText(s, rect.X+3, rowY, rect.W-4, infoStyle, info)
			rowY++
		}
	}
	if len(matches) == 0 {
		DrawText(s, rect.X+2, contentTop, rect.W-4, p.theme.Warning, "no models match current filter")
	}
}

func (p *HomePage) drawModelsModalAuthEditor(s tcell.Screen, parent Rect) {
	editor := p.modelsModal.AuthEditor
	if editor == nil {
		return
	}

	width := parent.W - 14
	if width > 96 {
		width = 96
	}
	if width < 54 {
		width = parent.W - 4
	}
	height := 11
	rect := Rect{
		X: parent.X + (parent.W-width)/2,
		Y: parent.Y + (parent.H-height)/2,
		W: width,
		H: height,
	}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, "Provider API Key · "+editor.Provider)
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, clampEllipsis("Paste API key and press Enter. Tab switches to optional name.", rect.W-4))

	apiValue := "<paste api key>"
	apiStyle := p.theme.TextMuted
	if strings.TrimSpace(editor.APIKey) != "" {
		apiValue = strings.Repeat("*", minInt(utf8.RuneCountInString(editor.APIKey), 24))
		apiStyle = p.theme.Text
	}
	apiPrefix := "  "
	if editor.Step == modelsModalAuthEditorStepAPIKey {
		apiPrefix = "> "
	}
	apiRect := Rect{X: rect.X + 2, Y: rect.Y + 3, W: rect.W - 4, H: 1}
	DrawText(s, apiRect.X, apiRect.Y, apiRect.W, apiStyle, fmt.Sprintf("%sAPI Key: %s", apiPrefix, apiValue))
	p.registerModelsModalTarget(apiRect, "models-auth-api", -1, editor.Provider)

	labelValue := "(optional)"
	labelStyle := p.theme.TextMuted
	if strings.TrimSpace(editor.Label) != "" {
		labelValue = editor.Label
		labelStyle = p.theme.Text
	}
	labelPrefix := "  "
	if editor.Step == modelsModalAuthEditorStepLabel {
		labelPrefix = "> "
	}
	labelRect := Rect{X: rect.X + 2, Y: rect.Y + 5, W: rect.W - 4, H: 1}
	DrawText(s, labelRect.X, labelRect.Y, labelRect.W, labelStyle, fmt.Sprintf("%sName: %s", labelPrefix, labelValue))
	p.registerModelsModalTarget(labelRect, "models-auth-label", -1, editor.Provider)
	DrawText(s, rect.X+2, rect.Y+7, rect.W-4, p.theme.TextMuted, clampEllipsis("Saved key is set active immediately. Esc cancels.", rect.W-4))
	submitRect := Rect{X: rect.X + 2, Y: rect.Y + 8, W: rect.W - 4, H: 1}
	DrawText(s, submitRect.X, submitRect.Y, submitRect.W, p.theme.TextMuted, clampEllipsis("Tab/↑/↓ switch field • Enter save", submitRect.W))
	p.registerModelsModalTarget(submitRect, "models-auth-submit", -1, editor.Provider)
}

func emptyStringFallback(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func modelsModalFeatureSummary(model ModelsModalEntry) string {
	parts := make([]string, 0, 3)
	if model.ContextWindow > 0 {
		parts = append(parts, "context: "+compactModelTokenCount(model.ContextWindow))
	}
	thinking := "off"
	if model.Reasoning {
		thinking = "on"
	}
	parts = append(parts, "thinking: "+thinking)
	return strings.Join(parts, " ")
}

func compactModelTokenCount(value int) string {
	switch {
	case value >= 1_000_000:
		if value%1_000_000 == 0 {
			return fmt.Sprintf("%dm", value/1_000_000)
		}
		return fmt.Sprintf("%.1fm", float64(value)/1_000_000)
	case value >= 1_000:
		if value%1_000 == 0 {
			return fmt.Sprintf("%dk", value/1_000)
		}
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type AgentModalProfile struct {
	Name             string
	Mode             string
	Description      string
	Provider         string
	Model            string
	Thinking         string
	Prompt           string
	ExecutionSetting string
	Enabled          bool
	UpdatedAt        int64
}

type AgentsModalData struct {
	Profiles             []AgentModalProfile
	ActivePrimary        string
	ActiveSubagent       map[string]string
	Version              int64
	Providers            []string
	ModelsByProvider     map[string][]string
	ReasoningModels      map[string]bool
	DefaultProvider      string
	DefaultModel         string
	DefaultThinking      string
	UtilityProvider      string
	UtilityModel         string
	UtilityThinking      string
	UtilityAgents        []string
	StaleInheritedAgents []string
}

type AgentsModalActionKind string

const (
	AgentsModalActionRefresh         AgentsModalActionKind = "refresh"
	AgentsModalActionRestoreDefaults AgentsModalActionKind = "restore-defaults"
	AgentsModalActionResetDefaults   AgentsModalActionKind = "reset-defaults"
	AgentsModalActionActivatePrimary AgentsModalActionKind = "activate-primary"
	AgentsModalActionUpsert          AgentsModalActionKind = "upsert"
	AgentsModalActionDelete          AgentsModalActionKind = "delete"
)

type AgentsModalUpsert struct {
	Name        string
	Mode        string
	Description string
	Provider    string
	Model       string
	Thinking    string
	Prompt      string
	Enabled     *bool
}

type AgentsModalAction struct {
	Kind       AgentsModalActionKind
	Name       string
	Upsert     *AgentsModalUpsert
	StatusHint string
}

type agentsModalFocus int

const (
	agentsModalFocusProfiles agentsModalFocus = iota
	agentsModalFocusDetails
	agentsModalFocusSearch
)

type agentsModalEditor struct {
	Mode          string
	TargetName    string
	Fields        []agentsModalEditorField
	InitialFields []agentsModalEditorField
	Selected      int
	Editing       bool
	RequirePrompt bool
}

type agentsModalEditorField struct {
	Key         string
	Label       string
	Value       string
	Placeholder string
	Options     []string
}

type agentsModalState struct {
	Visible              bool
	Loading              bool
	Status               string
	Error                string
	Focus                agentsModalFocus
	Search               string
	FilterMode           string
	Profiles             []AgentModalProfile
	ActivePrimary        string
	ActiveSubagent       map[string]string
	Version              int64
	Providers            []string
	ModelsByProvider     map[string][]string
	ReasoningModels      map[string]bool
	DefaultProvider      string
	DefaultModel         string
	DefaultThinking      string
	UtilityProvider      string
	UtilityModel         string
	UtilityThinking      string
	UtilityAgents        []string
	StaleInheritedAgents []string
	SelectedProfile      int
	ListScroll           int
	DetailScroll         int
	ConfirmDelete        bool
	ConfirmName          string
	ConfirmResetDefaults bool
	ConfirmUnsaved       bool
	UnsavedSaveFirst     bool
	Editor               *agentsModalEditor
}

func (p *HomePage) ShowAgentsModal() {
	p.agentsModal.Visible = true
	if p.agentsModal.Focus < agentsModalFocusProfiles || p.agentsModal.Focus > agentsModalFocusSearch {
		p.agentsModal.Focus = agentsModalFocusProfiles
	}
	if strings.TrimSpace(p.agentsModal.FilterMode) == "" {
		p.agentsModal.FilterMode = "all"
	}
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) HideAgentsModal() {
	p.agentsModal = agentsModalState{}
	p.pendingAgentsAction = nil
}

func (p *HomePage) AgentsModalVisible() bool {
	return p.agentsModal.Visible
}

func (p *HomePage) HandleAgentsModalKey(ev *tcell.EventKey) {
	if p == nil || !p.agentsModal.Visible {
		return
	}
	p.handleAgentsModalKey(ev)
}

func (p *HomePage) DrawAgentsModalOverlay(s tcell.Screen) {
	if p == nil || !p.agentsModal.Visible {
		return
	}
	p.drawAgentsModal(s)
}

func (p *HomePage) SetAgentsModalLoading(loading bool) {
	p.agentsModal.Loading = loading
}

func (p *HomePage) SetAgentsModalStatus(status string) {
	p.agentsModal.Status = strings.TrimSpace(status)
	if p.agentsModal.Status != "" {
		p.agentsModal.Error = ""
	}
}

func (p *HomePage) SetAgentsModalError(err string) {
	p.agentsModal.Error = strings.TrimSpace(err)
	if p.agentsModal.Error != "" {
		p.agentsModal.Loading = false
	}
}

func (p *HomePage) SetAgentsModalData(data AgentsModalData) {
	selectedName := p.selectedAgentsModalName()
	filter := strings.TrimSpace(p.agentsModal.FilterMode)

	p.agentsModal.Profiles = append([]AgentModalProfile(nil), data.Profiles...)
	p.agentsModal.ActivePrimary = strings.TrimSpace(data.ActivePrimary)
	if p.agentsModal.ActivePrimary == "" {
		p.agentsModal.ActivePrimary = "swarm"
	}
	p.agentsModal.ActiveSubagent = make(map[string]string, len(data.ActiveSubagent))
	for role, name := range data.ActiveSubagent {
		role = strings.ToLower(strings.TrimSpace(role))
		name = strings.ToLower(strings.TrimSpace(name))
		if role == "" || name == "" {
			continue
		}
		p.agentsModal.ActiveSubagent[role] = name
	}
	p.agentsModal.Version = data.Version
	p.agentsModal.Providers = dedupeAgentsModelOptions(data.Providers)
	p.agentsModal.ModelsByProvider = make(map[string][]string, len(data.ModelsByProvider))
	for providerID, models := range data.ModelsByProvider {
		providerID = strings.ToLower(strings.TrimSpace(providerID))
		if providerID == "" {
			continue
		}
		p.agentsModal.ModelsByProvider[providerID] = dedupeAgentsModelOptions(models)
	}
	p.agentsModal.ReasoningModels = make(map[string]bool, len(data.ReasoningModels))
	for name, enabled := range data.ReasoningModels {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		p.agentsModal.ReasoningModels[key] = enabled
	}
	p.agentsModal.DefaultProvider = strings.ToLower(strings.TrimSpace(data.DefaultProvider))
	p.agentsModal.DefaultModel = strings.TrimSpace(data.DefaultModel)
	p.agentsModal.DefaultThinking = normalizeThinkingValue(data.DefaultThinking)
	if p.agentsModal.DefaultThinking == "" {
		p.agentsModal.DefaultThinking = "xhigh"
	}
	p.agentsModal.UtilityProvider = strings.ToLower(strings.TrimSpace(data.UtilityProvider))
	p.agentsModal.UtilityModel = strings.TrimSpace(data.UtilityModel)
	p.agentsModal.UtilityThinking = normalizeThinkingValue(data.UtilityThinking)
	p.agentsModal.UtilityAgents = dedupeAgentsModalOptions(data.UtilityAgents)
	p.agentsModal.StaleInheritedAgents = dedupeAgentsModalOptions(data.StaleInheritedAgents)
	p.agentsModal.FilterMode = filter
	if p.agentsModal.FilterMode == "" {
		p.agentsModal.FilterMode = "all"
	}

	p.agentsModal.SelectedProfile = p.findAgentsModalIndexByName(selectedName)
	if p.agentsModal.SelectedProfile < 0 {
		p.agentsModal.SelectedProfile = p.findAgentsModalIndexByName(p.agentsModal.ActivePrimary)
	}
	p.agentsModal.reconcileSelections()
	p.agentsModal.ListScroll = 0
	p.agentsModal.DetailScroll = 0
	if p.agentsModal.Editor != nil {
		p.normalizeAgentsModalEditorFields(p.agentsModal.Editor)
	}
	if p.agentsModal.ConfirmDelete && !strings.EqualFold(strings.TrimSpace(p.agentsModal.ConfirmName), p.selectedAgentsModalName()) {
		p.clearAgentsModalDeleteConfirm()
	}
}

func cloneAgentsModalEditorFields(fields []agentsModalEditorField) []agentsModalEditorField {
	out := make([]agentsModalEditorField, 0, len(fields))
	for _, field := range fields {
		next := field
		next.Options = append([]string(nil), field.Options...)
		out = append(out, next)
	}
	return out
}

func agentsModalEditorHasPendingChanges(editor *agentsModalEditor) bool {
	if editor == nil {
		return false
	}
	if len(editor.Fields) != len(editor.InitialFields) {
		return true
	}
	for i := range editor.Fields {
		current := editor.Fields[i]
		initial := editor.InitialFields[i]
		if current.Key != initial.Key {
			return true
		}
		if current.Value != initial.Value {
			return true
		}
	}
	return false
}

func (p *HomePage) agentsModalEditorSaveLabel() string {
	if p != nil && p.keybinds != nil {
		if label := strings.TrimSpace(p.keybinds.Label(KeybindAgentsEditorSave)); label != "" {
			return label
		}
	}
	return "Ctrl+Y"
}

func (p *HomePage) openAgentsModalUnsavedConfirm() {
	p.agentsModal.ConfirmUnsaved = true
	p.agentsModal.UnsavedSaveFirst = true
	p.agentsModal.Status = fmt.Sprintf("Save changes before closing? %s yes, No discards.", p.agentsModalEditorSaveLabel())
	p.agentsModal.Error = ""
}

func (p *HomePage) dismissAgentsModalUnsavedConfirm() {
	p.agentsModal.ConfirmUnsaved = false
	p.agentsModal.UnsavedSaveFirst = false
}

func (p *HomePage) closeAgentsModalEditorDiscard() {
	p.agentsModal.Editor = nil
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = "editor closed (changes discarded)"
	p.dismissAgentsModalUnsavedConfirm()
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) PopAgentsModalAction() (AgentsModalAction, bool) {
	if p.pendingAgentsAction == nil {
		return AgentsModalAction{}, false
	}
	action := *p.pendingAgentsAction
	p.pendingAgentsAction = nil
	return action, true
}

func (p *HomePage) handleAgentsModalKey(ev *tcell.EventKey) {
	if p.agentsModal.Editor != nil {
		p.handleAgentsModalEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.agentsModal.Focus = agentsModalFocusProfiles
			p.agentsModal.DetailScroll = 0
			p.agentsModal.Status = "back to profiles"
			p.clearAgentsModalDeleteConfirm()
			return
		}
		p.HideAgentsModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext):
		p.advanceAgentsModalFocus(1)
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalFocusPrev):
		p.advanceAgentsModalFocus(-1)
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		switch p.agentsModal.Focus {
		case agentsModalFocusSearch:
			p.agentsModal.Focus = agentsModalFocusDetails
		default:
			p.agentsModal.Focus = agentsModalFocusProfiles
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		switch p.agentsModal.Focus {
		case agentsModalFocusProfiles:
			p.agentsModal.Focus = agentsModalFocusDetails
		default:
			p.agentsModal.Focus = agentsModalFocusSearch
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(-1)
		} else {
			p.moveAgentsModalSelection(-1)
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(1)
		} else {
			p.moveAgentsModalSelection(1)
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalPageUp):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(-8)
		} else {
			p.moveAgentsModalSelection(-4)
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalPageDown):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(8)
		} else {
			p.moveAgentsModalSelection(4)
		}
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		p.deleteAgentsModalSearchRune()
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.clearAgentsModalSearch()
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleAgentsModalEnter()
		p.clearAgentsModalDeleteConfirm()
		return
	}

	if ev.Key() == tcell.KeyRune {
		p.handleAgentsModalRune(ev)
	}
}

func (p *HomePage) handleAgentsModalRune(ev *tcell.EventKey) {
	r := ev.Rune()
	if p.agentsModal.Focus == agentsModalFocusSearch {
		if unicode.IsPrint(r) && utf8.RuneLen(r) > 0 {
			p.agentsModal.Search += string(r)
			p.agentsModal.reconcileSelections()
		}
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalSearchFocus):
		p.agentsModal.Focus = agentsModalFocusSearch
	case p.keybinds.Match(ev, KeybindAgentsFocusProfiles):
		p.agentsModal.Focus = agentsModalFocusProfiles
	case p.keybinds.Match(ev, KeybindAgentsFocusDetails):
		p.agentsModal.Focus = agentsModalFocusDetails
	case p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(1)
		} else {
			p.moveAgentsModalSelection(1)
		}
	case p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			p.scrollAgentsModalDetail(-1)
		} else {
			p.moveAgentsModalSelection(-1)
		}
	case p.keybinds.Match(ev, KeybindAgentsClearSearchAlt):
		p.clearAgentsModalSearch()
	case p.keybinds.Match(ev, KeybindAgentsRefresh):
		p.enqueueAgentsModalAction(AgentsModalAction{
			Kind:       AgentsModalActionRefresh,
			StatusHint: "Refreshing agent profiles...",
		})
	case p.keybinds.Match(ev, KeybindAgentsRestoreDefaults):
		p.enqueueAgentsModalAction(AgentsModalAction{
			Kind:       AgentsModalActionRestoreDefaults,
			StatusHint: "Restoring default agents...",
		})
	case p.keybinds.Match(ev, KeybindAgentsResetDefaults):
		if p.agentsModal.ConfirmResetDefaults {
			p.enqueueAgentsModalAction(AgentsModalAction{
				Kind:       AgentsModalActionResetDefaults,
				StatusHint: "Resetting all agents to defaults...",
			})
			return
		}
		p.agentsModal.ConfirmResetDefaults = true
		p.agentsModal.Status = "Warning: Shift+Z again resets all agents/tools to built-in defaults and deletes custom agents"
	case p.keybinds.Match(ev, KeybindAgentsActivate), p.keybinds.Match(ev, KeybindAgentsActivateAlt):
		p.handleAgentsActivateSelected()
	case p.keybinds.Match(ev, KeybindAgentsDelete):
		p.handleAgentsDeleteSelected()
	case p.keybinds.Match(ev, KeybindAgentsToggleEnabled):
		p.toggleAgentsSelectedEnabled()
	case p.keybinds.Match(ev, KeybindAgentsEdit), p.keybinds.Match(ev, KeybindAgentsEditAlt):
		profile, ok := p.selectedAgentsModalProfile()
		if !ok {
			p.agentsModal.Status = "No agent selected"
			return
		}
		p.openAgentsModalEditEditor(profile)
	case p.keybinds.Match(ev, KeybindAgentsNew):
		p.openAgentsModalCreateEditor()
	case p.keybinds.Match(ev, KeybindAgentsFilterAll):
		p.agentsModal.FilterMode = "all"
		p.agentsModal.reconcileSelections()
		p.agentsModal.Status = "filter: all profiles"
	case p.keybinds.Match(ev, KeybindAgentsFilterPrimary):
		p.agentsModal.FilterMode = "primary"
		p.agentsModal.reconcileSelections()
		p.agentsModal.Status = "filter: primary agents"
	case p.keybinds.Match(ev, KeybindAgentsFilterSubagent):
		p.agentsModal.FilterMode = "subagent"
		p.agentsModal.reconcileSelections()
		p.agentsModal.Status = "filter: subagents"
	}
}

func (p *HomePage) handleAgentsModalEnter() {
	switch p.agentsModal.Focus {
	case agentsModalFocusSearch:
		p.agentsModal.Focus = agentsModalFocusProfiles
	case agentsModalFocusProfiles:
		profile, ok := p.selectedAgentsModalProfile()
		if !ok {
			p.agentsModal.Status = "No agent selected"
			return
		}
		p.agentsModal.Focus = agentsModalFocusDetails
		p.agentsModal.DetailScroll = 0
		p.agentsModal.Status = fmt.Sprintf("Viewing profile: %s. Enter edits. Esc returns to cards.", profile.Name)
	case agentsModalFocusDetails:
		profile, ok := p.selectedAgentsModalProfile()
		if !ok {
			p.agentsModal.Status = "No agent selected"
			return
		}
		p.openAgentsModalEditEditor(profile)
	}
}

func (p *HomePage) handleAgentsActivateSelected() {
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		p.agentsModal.Status = "No agent selected"
		return
	}
	if !strings.EqualFold(profile.Mode, "primary") {
		p.agentsModal.Status = fmt.Sprintf("%s is not a primary agent", profile.Name)
		return
	}
	if !profile.Enabled {
		p.agentsModal.Status = fmt.Sprintf("%s is disabled; enable it first", profile.Name)
		return
	}
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind:       AgentsModalActionActivatePrimary,
		Name:       profile.Name,
		StatusHint: fmt.Sprintf("Activating primary agent %s...", profile.Name),
	})
}

func (p *HomePage) toggleAgentsSelectedEnabled() {
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		p.agentsModal.Status = "No agent selected"
		return
	}
	nextEnabled := !profile.Enabled
	if strings.EqualFold(profile.Name, "swarm") && !nextEnabled {
		p.agentsModal.Status = "swarm cannot be disabled"
		return
	}
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind: AgentsModalActionUpsert,
		Upsert: &AgentsModalUpsert{
			Name:    profile.Name,
			Enabled: &nextEnabled,
		},
		StatusHint: fmt.Sprintf("Setting %s enabled=%s...", profile.Name, boolLabel(nextEnabled)),
	})
}

func (p *HomePage) handleAgentsDeleteSelected() {
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		p.agentsModal.Status = "No agent selected"
		return
	}
	name := strings.ToLower(strings.TrimSpace(profile.Name))
	if name == "" {
		p.agentsModal.Status = "No agent selected"
		return
	}
	if name == "swarm" {
		p.agentsModal.Status = "swarm is protected and cannot be deleted"
		p.clearAgentsModalDeleteConfirm()
		return
	}
	if name == "memory" {
		p.agentsModal.Status = "memory is protected and cannot be deleted; it is used for session titles"
		p.clearAgentsModalDeleteConfirm()
		return
	}
	if !p.agentsModal.ConfirmDelete || !strings.EqualFold(strings.TrimSpace(p.agentsModal.ConfirmName), name) {
		p.agentsModal.ConfirmDelete = true
		p.agentsModal.ConfirmName = name
		p.agentsModal.Status = fmt.Sprintf("Press d again to delete %s", name)
		return
	}
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind:       AgentsModalActionDelete,
		Name:       name,
		StatusHint: fmt.Sprintf("Deleting agent %s...", name),
	})
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) openAgentsModalCreateEditor() {
	providerOptions := p.agentsModalProviderOptions()
	modelOptions := p.agentsModalModelOptionsForProvider("")
	thinkingOptions := p.agentsModalThinkingOptions("", "")
	p.agentsModal.Editor = &agentsModalEditor{
		Mode:          "create",
		TargetName:    "",
		RequirePrompt: true,
		Fields: []agentsModalEditorField{
			{Key: "name", Label: "Name", Value: "", Placeholder: "new-agent"},
			{Key: "mode", Label: "Mode", Value: "subagent", Placeholder: "primary|subagent|background", Options: []string{"primary", "subagent", "background"}},
			{Key: "description", Label: "Role", Value: "", Placeholder: "What this agent does"},
			{Key: "provider", Label: "Provider", Value: "", Placeholder: "inherit default provider", Options: providerOptions},
			{Key: "model", Label: "Model", Value: "", Placeholder: "inherit default model", Options: modelOptions},
			{Key: "thinking", Label: "Thinking", Value: "", Placeholder: "inherit default thinking", Options: thinkingOptions},
			{Key: "enabled", Label: "Enabled", Value: "y", Placeholder: "y", Options: []string{"y", "n"}},
			{Key: "prompt", Label: "Prompt", Value: "", Placeholder: "System prompt"},
		},
	}
	p.agentsModal.Editor.Editing = false
	p.normalizeAgentsModalEditorFields(p.agentsModal.Editor)
	p.agentsModal.Editor.InitialFields = cloneAgentsModalEditorFields(p.agentsModal.Editor.Fields)
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = fmt.Sprintf("Create profile editor. Enter edits/commits fields. %s saves changes.", p.agentsModalEditorSaveLabel())
	p.agentsModal.Error = ""
	p.dismissAgentsModalUnsavedConfirm()
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) openAgentsModalEditEditor(profile AgentModalProfile) {
	mode := strings.ToLower(strings.TrimSpace(profile.Mode))
	if mode == "" {
		mode = "subagent"
	}
	enabledValue := "n"
	if profile.Enabled {
		enabledValue = "y"
	}
	providerOptions := p.agentsModalProviderOptions()
	profileProvider := strings.ToLower(strings.TrimSpace(profile.Provider))
	modelOptions := p.agentsModalModelOptionsForProvider(profileProvider)
	thinkingOptions := p.agentsModalThinkingOptions(profileProvider, profile.Model)
	p.agentsModal.Editor = &agentsModalEditor{
		Mode:          "edit",
		TargetName:    profile.Name,
		RequirePrompt: false,
		Fields: []agentsModalEditorField{
			{Key: "mode", Label: "Mode", Value: mode, Placeholder: "primary|subagent|background", Options: []string{"primary", "subagent", "background"}},
			{Key: "description", Label: "Role", Value: profile.Description, Placeholder: "What this agent does"},
			{Key: "provider", Label: "Provider", Value: profileProvider, Placeholder: "inherit default provider", Options: providerOptions},
			{Key: "model", Label: "Model", Value: profile.Model, Placeholder: "inherit default model", Options: modelOptions},
			{Key: "thinking", Label: "Thinking", Value: profile.Thinking, Placeholder: "inherit default thinking", Options: thinkingOptions},
			{Key: "enabled", Label: "Enabled", Value: enabledValue, Placeholder: "y", Options: []string{"y", "n"}},
			{Key: "prompt", Label: "Prompt", Value: profile.Prompt, Placeholder: "System prompt"},
		},
	}
	p.agentsModal.Editor.Editing = false
	p.normalizeAgentsModalEditorFields(p.agentsModal.Editor)
	p.agentsModal.Editor.InitialFields = cloneAgentsModalEditorFields(p.agentsModal.Editor.Fields)
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = fmt.Sprintf("Edit profile: %s. Enter edits/commits fields. %s saves changes.", profile.Name, p.agentsModalEditorSaveLabel())
	p.agentsModal.Error = ""
	p.dismissAgentsModalUnsavedConfirm()
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) handleAgentsModalEditorKey(ev *tcell.EventKey) {
	editor := p.agentsModal.Editor
	if editor == nil {
		return
	}
	if p.agentsModal.ConfirmUnsaved {
		p.handleAgentsModalUnsavedConfirmKey(ev)
		return
	}

	moveField := func(delta int) {
		if len(editor.Fields) == 0 {
			return
		}
		editor.Selected = (editor.Selected + delta + len(editor.Fields)) % len(editor.Fields)
		p.agentsModal.DetailScroll = 0
	}

	selectedField := func() *agentsModalEditorField {
		if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
			return nil
		}
		return &editor.Fields[editor.Selected]
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		if editor.Editing {
			editor.Editing = false
			p.agentsModal.Status = "field edit canceled"
			return
		}
		if agentsModalEditorHasPendingChanges(editor) {
			p.openAgentsModalUnsavedConfirm()
			return
		}
		p.closeAgentsModalEditorDiscard()
		p.agentsModal.Status = "editor closed"
		return
	case p.keybinds.Match(ev, KeybindAgentsEditorSave):
		if editor.Editing {
			editor.Editing = false
		}
		p.dismissAgentsModalUnsavedConfirm()
		if !agentsModalEditorHasPendingChanges(editor) {
			p.agentsModal.Status = "No pending changes to save"
			return
		}
		p.submitAgentsModalEditor()
		return
	case p.keybinds.Match(ev, KeybindEditorFocusNext):
		if editor.Editing {
			editor.Editing = false
		}
		moveField(1)
		return
	case p.keybinds.Match(ev, KeybindEditorFocusPrev):
		if editor.Editing {
			editor.Editing = false
		}
		moveField(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveUp):
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(field, -1)
				return
			}
		}
		moveField(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveDown):
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(field, 1)
				return
			}
		}
		moveField(1)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(field, -1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(field, 1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorBackspace):
		if !editor.Editing {
			return
		}
		field := selectedField()
		if field == nil || len(field.Options) > 0 {
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
		if !editor.Editing {
			return
		}
		field := selectedField()
		if field == nil {
			return
		}
		if len(field.Options) > 0 {
			field.Value = ""
			p.syncAgentsModalEditorDependentOptions(editor)
			return
		}
		field.Value = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if !editor.Editing {
			editor.Editing = true
			field := selectedField()
			fieldLabel := "field"
			if field != nil {
				fieldLabel = strings.ToLower(strings.TrimSpace(field.Label))
			}
			if field != nil && len(field.Options) > 0 {
				p.agentsModal.Status = fmt.Sprintf("editing %s (use <-/-> or up/down, Enter commits, Esc cancels)", fieldLabel)
			} else {
				p.agentsModal.Status = fmt.Sprintf("editing %s (type text, Enter commits, Esc cancels)", fieldLabel)
			}
			return
		}
		editor.Editing = false
		p.syncAgentsModalEditorDependentOptions(editor)
		if field := selectedField(); field != nil {
			p.agentsModal.Status = fmt.Sprintf("field updated: %s", strings.ToLower(strings.TrimSpace(field.Label)))
		}
		return
	case p.keybinds.Match(ev, KeybindAgentsEditorInsertNewline):
		if !editor.Editing {
			return
		}
		field := selectedField()
		if field == nil || len(field.Options) > 0 {
			return
		}
		field.Value += "\n"
		return
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if !editor.Editing {
			return
		}
		field := selectedField()
		if field == nil {
			return
		}
		if len(field.Options) > 0 {
			p.selectAgentsModalEditorOptionByRune(field, r)
			return
		}
		if unicode.IsPrint(r) {
			field.Value += string(r)
		}
	}
}

func (p *HomePage) resolveAgentsModalUnsavedConfirm(save bool) {
	editor := p.agentsModal.Editor
	if !save {
		p.closeAgentsModalEditorDiscard()
		return
	}
	if editor == nil {
		p.dismissAgentsModalUnsavedConfirm()
		return
	}
	if editor.Editing {
		editor.Editing = false
	}
	p.dismissAgentsModalUnsavedConfirm()
	if !agentsModalEditorHasPendingChanges(editor) {
		p.closeAgentsModalEditorDiscard()
		return
	}
	p.submitAgentsModalEditor()
}

func (p *HomePage) handleAgentsModalUnsavedConfirmKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		p.dismissAgentsModalUnsavedConfirm()
		p.agentsModal.Status = "close canceled"
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext),
		p.keybinds.Match(ev, KeybindModalFocusPrev),
		p.keybinds.Match(ev, KeybindModalFocusLeft),
		p.keybinds.Match(ev, KeybindModalFocusRight),
		p.keybinds.Match(ev, KeybindEditorMoveLeft),
		p.keybinds.Match(ev, KeybindEditorMoveRight):
		p.agentsModal.UnsavedSaveFirst = !p.agentsModal.UnsavedSaveFirst
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.resolveAgentsModalUnsavedConfirm(p.agentsModal.UnsavedSaveFirst)
		return
	case p.keybinds.Match(ev, KeybindAgentsEditorSave):
		p.resolveAgentsModalUnsavedConfirm(true)
		return
	}
	if ev.Key() != tcell.KeyRune {
		return
	}
	switch unicode.ToLower(ev.Rune()) {
	case 'y':
		p.resolveAgentsModalUnsavedConfirm(true)
	case 'n':
		p.resolveAgentsModalUnsavedConfirm(false)
	}
}

func (p *HomePage) submitAgentsModalEditor() {
	editor := p.agentsModal.Editor
	if editor == nil {
		return
	}
	p.dismissAgentsModalUnsavedConfirm()

	get := func(key string) string {
		for _, field := range editor.Fields {
			if field.Key == key {
				return strings.TrimSpace(field.Value)
			}
		}
		return ""
	}

	mode, ok := normalizeAgentModeValue(get("mode"))
	if !ok {
		p.agentsModal.Error = "mode must be primary, subagent, or background"
		return
	}

	enabled := parseYN(get("enabled"))
	provider := p.normalizeAgentsModalProviderValue(get("provider"))
	model := p.normalizeAgentsModalModelValue(provider, get("model"))
	if provider == "" {
		model = ""
	}
	thinkingOptions := p.agentsModalThinkingOptions(provider, model)
	thinking := normalizeAgentsModalThinkingValue(get("thinking"), thinkingOptions, p.agentsModal.DefaultThinking)

	upsert := AgentsModalUpsert{
		Mode:        mode,
		Description: strings.TrimSpace(get("description")),
		Provider:    provider,
		Model:       model,
		Thinking:    thinking,
		Prompt:      strings.TrimSpace(get("prompt")),
		Enabled:     &enabled,
	}

	if editor.Mode == "create" {
		upsert.Name = strings.ToLower(strings.TrimSpace(get("name")))
		if upsert.Name == "" {
			p.agentsModal.Error = "agent name is required"
			return
		}
		if editor.RequirePrompt && strings.TrimSpace(upsert.Prompt) == "" {
			p.agentsModal.Error = "prompt is required for new profiles"
			return
		}
	} else {
		upsert.Name = strings.ToLower(strings.TrimSpace(editor.TargetName))
		if upsert.Name == "" {
			p.agentsModal.Error = "target agent is missing"
			return
		}
	}

	if strings.EqualFold(upsert.Name, "swarm") {
		upsert.Mode = "primary"
		forced := true
		upsert.Enabled = &forced
	}

	p.agentsModal.Editor = nil
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind:       AgentsModalActionUpsert,
		Upsert:     &upsert,
		StatusHint: fmt.Sprintf("Saving profile %s...", upsert.Name),
	})
}

func normalizeAgentModeValue(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case value == "":
		return "subagent", true
	case strings.HasPrefix(value, "pri"):
		return "primary", true
	case value == "p":
		return "primary", true
	case strings.HasPrefix(value, "back"):
		return "background", true
	case value == "b":
		return "background", true
	case strings.HasPrefix(value, "sub"):
		return "subagent", true
	case value == "s":
		return "subagent", true
	default:
		return "", false
	}
}

func normalizeThinkingValue(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "inherit", "default":
		return ""
	case "off", "low", "medium", "high", "xhigh":
		return value
	case "x-high":
		return "xhigh"
	default:
		return ""
	}
}

func (p *HomePage) enqueueAgentsModalAction(action AgentsModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingAgentsAction = &action
	p.agentsModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.agentsModal.Status = action.StatusHint
	}
	p.agentsModal.Error = ""
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) advanceAgentsModalFocus(delta int) {
	order := []agentsModalFocus{
		agentsModalFocusProfiles,
		agentsModalFocusDetails,
		agentsModalFocusSearch,
	}
	idx := 0
	for i, focus := range order {
		if focus == p.agentsModal.Focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	p.agentsModal.Focus = order[idx]
}

func (p *HomePage) moveAgentsModalSelection(delta int) {
	if delta == 0 {
		return
	}
	if p.agentsModal.Focus != agentsModalFocusProfiles {
		return
	}
	matches := groupedAgentsModalIndexes(p.agentsFilteredIndexes(), p.agentsModal.Profiles)
	if len(matches) == 0 {
		return
	}
	current := p.agentsModal.SelectedProfile
	pos := indexInList(matches, current)
	if pos < 0 {
		pos = 0
	}
	pos = (pos + delta + len(matches)) % len(matches)
	p.agentsModal.SelectedProfile = matches[pos]
	p.agentsModal.DetailScroll = 0
	if p.agentsModal.ConfirmDelete && !strings.EqualFold(strings.TrimSpace(p.agentsModal.ConfirmName), p.selectedAgentsModalName()) {
		p.clearAgentsModalDeleteConfirm()
	}
}

func (p *HomePage) deleteAgentsModalSearchRune() {
	if p.agentsModal.Focus != agentsModalFocusSearch {
		return
	}
	if len(p.agentsModal.Search) == 0 {
		return
	}
	_, sz := utf8.DecodeLastRuneInString(p.agentsModal.Search)
	if sz > 0 {
		p.agentsModal.Search = p.agentsModal.Search[:len(p.agentsModal.Search)-sz]
	}
	p.agentsModal.reconcileSelections()
}

func (p *HomePage) clearAgentsModalSearch() {
	p.agentsModal.Search = ""
	p.agentsModal.reconcileSelections()
}

func (p *HomePage) scrollAgentsModalDetail(delta int) {
	if delta == 0 {
		return
	}
	p.agentsModal.DetailScroll += delta
	if p.agentsModal.DetailScroll < 0 {
		p.agentsModal.DetailScroll = 0
	}
}

func (p *HomePage) clearAgentsModalDeleteConfirm() {
	p.agentsModal.ConfirmDelete = false
	p.agentsModal.ConfirmName = ""
	p.agentsModal.ConfirmResetDefaults = false
}

func (s *agentsModalState) reconcileSelections() {
	matches := groupedAgentsModalIndexes(s.filteredIndexes(), s.Profiles)
	if len(matches) == 0 {
		s.SelectedProfile = -1
		return
	}
	if indexInList(matches, s.SelectedProfile) < 0 {
		s.SelectedProfile = matches[0]
	}
}

func (p *HomePage) selectedAgentsModalProfile() (AgentModalProfile, bool) {
	idx := p.agentsModal.SelectedProfile
	if idx < 0 || idx >= len(p.agentsModal.Profiles) {
		return AgentModalProfile{}, false
	}
	return p.agentsModal.Profiles[idx], true
}

func (p *HomePage) selectedAgentsModalName() string {
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		return ""
	}
	return strings.TrimSpace(profile.Name)
}

func (p *HomePage) findAgentsModalIndexByName(name string) int {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return -1
	}
	for i, profile := range p.agentsModal.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), name) {
			return i
		}
	}
	return -1
}

func groupedAgentsModalIndexes(indexes []int, profiles []AgentModalProfile) []int {
	if len(indexes) == 0 {
		return nil
	}
	primary := make([]int, 0, len(indexes))
	subagents := make([]int, 0, len(indexes))
	background := make([]int, 0, len(indexes))
	other := make([]int, 0, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(profiles) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(profiles[idx].Mode)) {
		case "primary":
			primary = append(primary, idx)
		case "subagent":
			subagents = append(subagents, idx)
		case "background":
			background = append(background, idx)
		default:
			other = append(other, idx)
		}
	}
	out := make([]int, 0, len(indexes))
	out = append(out, primary...)
	out = append(out, subagents...)
	out = append(out, background...)
	out = append(out, other...)
	return out
}

func (p *HomePage) agentsFilteredIndexes() []int {
	return p.agentsModal.filteredIndexes()
}

func (s *agentsModalState) filteredIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(s.Search))
	filter := strings.ToLower(strings.TrimSpace(s.FilterMode))
	out := make([]int, 0, len(s.Profiles))
	for i, profile := range s.Profiles {
		mode := strings.ToLower(strings.TrimSpace(profile.Mode))
		switch filter {
		case "primary":
			if mode != "primary" {
				continue
			}
		case "subagent":
			if mode != "subagent" {
				continue
			}
		case "background":
			if mode != "background" {
				continue
			}
		}
		if query != "" && !agentMatchesQuery(profile, query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func agentMatchesQuery(profile AgentModalProfile, query string) bool {
	if query == "" {
		return true
	}
	target := strings.ToLower(strings.Join([]string{
		profile.Name,
		profile.Mode,
		profile.Description,
		profile.Provider,
		profile.Model,
		profile.Thinking,
		profile.Prompt,
		profile.ExecutionSetting,
	}, " "))
	for _, token := range strings.Fields(query) {
		if !strings.Contains(target, strings.ToLower(token)) {
			return false
		}
	}
	return true
}

func (p *HomePage) drawAgentsModal(s tcell.Screen) {
	if !p.agentsModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := w - 8
	if modalW > 126 {
		modalW = 126
	}
	if modalW < 84 {
		modalW = w - 2
	}
	modalH := h - 6
	if modalH > 34 {
		modalH = 34
	}
	if modalH < 20 {
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

	title := "Agents Manager"
	if p.agentsModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)

	statusStyle := p.theme.TextMuted
	status := strings.TrimSpace(p.agentsModal.Status)
	saveLabel := p.agentsModalEditorSaveLabel()
	if strings.TrimSpace(p.agentsModal.Error) != "" {
		status = p.agentsModal.Error
		statusStyle = p.theme.Error
	}
	if status == "" {
		if editor := p.agentsModal.Editor; editor != nil {
			if agentsModalEditorHasPendingChanges(editor) {
				status = fmt.Sprintf("Save changes? %s save • Esc asks before closing", saveLabel)
			} else {
				status = fmt.Sprintf("Enter edit/commit field • %s save • Esc close", saveLabel)
			}
		} else if p.agentsModal.Focus == agentsModalFocusDetails {
			status = "Enter edits • Esc back to cards • ↑/↓ scroll • Tab focus search"
		} else if len(p.agentsModal.StaleInheritedAgents) > 0 {
			status = fmt.Sprintf("Stale Utility AI inheritance: %s • Shift+R applies Utility AI", strings.Join(p.agentsModal.StaleInheritedAgents, ", "))
			statusStyle = p.theme.Warning
		} else {
			status = "Enter open profile • a activate primary • n new • t enable/disable • d delete • r refresh • Shift+R apply Utility AI • Shift+Z reset all"
		}
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(status, rect.W-4))

	filter := nonEmpty(strings.TrimSpace(p.agentsModal.FilterMode), "all")
	searchEdit := ""
	if p.agentsModal.Focus == agentsModalFocusSearch {
		searchEdit = " [edit]"
	}
	meta := fmt.Sprintf("active primary: %s  |  policy version: %d  |  filter: %s", nonEmpty(p.agentsModal.ActivePrimary, "swarm"), p.agentsModal.Version, filter)
	DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.TextMuted, clampEllipsis(meta, rect.W-4))
	DrawText(s, rect.X+2, rect.Y+3, rect.W-4, p.theme.TextMuted, clampEllipsis("search"+searchEdit+": "+p.agentsModal.Search, rect.W-4))

	bodyRect := Rect{X: rect.X + 1, Y: rect.Y + 4, W: rect.W - 2, H: rect.H - 7}
	if bodyRect.W < 28 || bodyRect.H < 8 {
		return
	}

	if p.agentsModal.Editor != nil || p.agentsModal.Focus == agentsModalFocusDetails {
		p.drawAgentsModalDetailPane(s, bodyRect)
	} else {
		p.drawAgentsModalListPane(s, bodyRect)
	}

	help := "Enter open profile • Tab focus • ↑/↓ move • PgUp/PgDn move • Esc close • Shift+R apply Utility AI • Shift+Z reset all"
	if p.agentsModal.Focus == agentsModalFocusDetails {
		help = "Enter edit • Tab focus • ↑/↓ or PgUp/PgDn scroll • Esc back to cards"
	}
	if editor := p.agentsModal.Editor; editor != nil {
		if agentsModalEditorHasPendingChanges(editor) {
			help = fmt.Sprintf("Save changes? %s save • Esc asks before closing • Tab focus • ↑/↓ move", saveLabel)
		} else {
			help = fmt.Sprintf("Enter edit/commit • %s save • Tab focus • ↑/↓ move • Esc close", saveLabel)
		}
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(help, rect.W-4))
	if p.agentsModal.ConfirmDelete && strings.TrimSpace(p.agentsModal.ConfirmName) != "" {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.Warning, "Delete armed: press d again to confirm")
	} else if p.agentsModal.ConfirmResetDefaults {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.Warning, "Warning: Shift+Z again resets all agents/tools to built-in defaults and deletes custom agents")
	} else {
		DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "memory cannot be deleted; used for session titles • Shift+R applies Utility AI to built-ins • Shift+Z destructively resets everything")
	}
	if p.agentsModal.ConfirmUnsaved {
		p.drawAgentsModalUnsavedConfirm(s, rect)
	}

}

func (p *HomePage) drawAgentsModalUnsavedConfirm(s tcell.Screen, modal Rect) {
	boxW := minInt(68, modal.W-6)
	if boxW < 44 {
		boxW = modal.W - 2
	}
	boxH := 8
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
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Warning, "Unsaved Changes")
	DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.Text, "Save changes before closing this editor?")

	saveLabel := fmt.Sprintf("[ Yes, save (%s) ]", p.agentsModalEditorSaveLabel())
	discardLabel := "[ No, discard ]"
	saveStyle := p.theme.TextMuted
	discardStyle := p.theme.TextMuted
	if p.agentsModal.UnsavedSaveFirst {
		saveStyle = p.theme.Primary.Bold(true)
	} else {
		discardStyle = p.theme.Warning.Bold(true)
	}

	buttonY := rect.Y + 4
	saveX := rect.X + 2
	discardX := saveX + utf8.RuneCountInString(saveLabel) + 2
	DrawText(s, saveX, buttonY, rect.W-4, saveStyle, saveLabel)
	if discardX < rect.X+rect.W-2 {
		DrawText(s, discardX, buttonY, rect.W-(discardX-rect.X)-2, discardStyle, discardLabel)
	}

	hint := "Left/Right switch • Enter confirm • Esc cancel • y/n quick choice"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(hint, rect.W-4))
}

func (p *HomePage) drawAgentsModalListPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Profiles"
	if p.agentsModal.Focus == agentsModalFocusProfiles {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	matches := groupedAgentsModalIndexes(p.agentsFilteredIndexes(), p.agentsModal.Profiles)
	if len(matches) == 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.Warning, "no profiles match current filter")
		return
	}

	contentW := rect.W - 4
	contentH := rect.H - 2
	if contentW <= 0 || contentH <= 0 {
		return
	}

	orderedPrimary := make([]int, 0)
	orderedSubagents := make([]int, 0)
	orderedBackground := make([]int, 0)
	other := make([]int, 0)
	for _, idx := range matches {
		switch strings.ToLower(strings.TrimSpace(p.agentsModal.Profiles[idx].Mode)) {
		case "primary":
			orderedPrimary = append(orderedPrimary, idx)
		case "subagent":
			orderedSubagents = append(orderedSubagents, idx)
		case "background":
			orderedBackground = append(orderedBackground, idx)
		default:
			other = append(other, idx)
		}
	}

	lines := make([]agentsModalRenderLine, 0, len(matches)*4+12)
	selectedLine := 0
	appendSection := func(title string, indexes []int) {
		if len(indexes) == 0 {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		lines = append(lines, agentsModalRenderLine{Text: title, Style: p.theme.Primary.Bold(true)})
		for _, idx := range indexes {
			profile := p.agentsModal.Profiles[idx]
			selected := idx == p.agentsModal.SelectedProfile
			if selected {
				selectedLine = len(lines)
			}
			lineStyle := p.theme.Text
			metaStyle := p.theme.TextMuted
			prefix := "  "
			if selected {
				prefix = "> "
				lineStyle = p.theme.Text.Bold(true)
				metaStyle = p.theme.Text
			}

			nameLine := prefix + nonEmpty(profile.Name, "-")
			if strings.EqualFold(profile.Name, p.agentsModal.ActivePrimary) {
				nameLine += "  [active]"
			}
			if strings.EqualFold(profile.Name, "swarm") {
				nameLine += "  [default]"
			}
			if p.agentsModalProfileIsUtility(profile.Name) {
				nameLine += "  [Utility AI]"
			}
			lines = append(lines, agentsModalRenderLine{Text: clampEllipsis(nameLine, contentW), Style: lineStyle})

			roleLine := fmt.Sprintf("    role: %s", agentsModalRoleSummary(profile, p.agentsModal.ActiveSubagent))
			for _, line := range wrapAgentsModalWithPrefix("", roleLine, contentW) {
				lines = append(lines, agentsModalRenderLine{Text: line, Style: metaStyle})
			}

			modeLine := fmt.Sprintf("    mode: %s • execution: %s", agentsModalModeLabel(profile.Mode), agentsModalExecutionSettingLabel(profile.ExecutionSetting))
			for _, line := range wrapAgentsModalWithPrefix("", modeLine, contentW) {
				lines = append(lines, agentsModalRenderLine{Text: line, Style: metaStyle})
			}
		}
	}

	appendSection("Primary", orderedPrimary)
	appendSection("Subagents", orderedSubagents)
	appendSection("Background", orderedBackground)
	appendSection("Other", other)

	maxScroll := maxInt(0, len(lines)-contentH)
	if p.agentsModal.ListScroll > maxScroll {
		p.agentsModal.ListScroll = maxScroll
	}
	if p.agentsModal.ListScroll < 0 {
		p.agentsModal.ListScroll = 0
	}
	if p.agentsModal.ListScroll > selectedLine {
		p.agentsModal.ListScroll = selectedLine
	}
	if p.agentsModal.ListScroll+contentH-1 < selectedLine {
		p.agentsModal.ListScroll = selectedLine - contentH + 1
	}
	if p.agentsModal.ListScroll < 0 {
		p.agentsModal.ListScroll = 0
	}

	rowY := rect.Y + 1
	for i := 0; i < contentH; i++ {
		lineIdx := p.agentsModal.ListScroll + i
		if lineIdx < 0 || lineIdx >= len(lines) {
			break
		}
		DrawText(s, rect.X+2, rowY, contentW, lines[lineIdx].Style, clampEllipsis(lines[lineIdx].Text, contentW))
		rowY++
	}
}

func (p *HomePage) drawAgentsModalDetailPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Profile Details"
	if p.agentsModal.Focus == agentsModalFocusDetails || p.agentsModal.Editor != nil {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	editor := p.agentsModal.Editor
	if editor != nil {
		if editor.Mode == "create" {
			header = "Create Profile [focus]"
		} else {
			header = "Edit Profile [focus]"
		}
		if editor.Editing {
			header += " [editing]"
		}
	}

	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	contentWidth := rect.W - 4
	contentRows := rect.H - 2
	if contentWidth <= 0 || contentRows <= 0 {
		return
	}

	rowY := rect.Y + 1
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		DrawText(s, rect.X+2, rowY, contentWidth, p.theme.Warning, "select an agent profile")
		return
	}

	lines := make([]agentsModalRenderLine, 0, 96)
	selectedStart := -1
	selectedEnd := -1

	if editor != nil {
		if editor.TargetName != "" {
			for _, line := range wrapAgentsModalWithPrefix("target: ", editor.TargetName, contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: line, Style: p.theme.Text})
			}
		}
		lines = append(lines, agentsModalRenderLine{Text: "blank model/thinking inherits current defaults", Style: p.theme.TextMuted})
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		for i, field := range editor.Fields {
			value := strings.TrimSpace(field.Value)
			style := p.theme.TextMuted
			if value == "" {
				value = nonEmpty(strings.TrimSpace(field.Placeholder), "-")
			}
			prefix := "  "
			if i == editor.Selected {
				prefix = "->"
				style = p.theme.Accent.Bold(true)
				if editor.Editing {
					prefix = ">>"
					style = p.theme.Primary.Bold(true)
				}
			}
			value = agentsModalEditorFieldDisplayValue(field, value)
			if i == editor.Selected && editor.Editing && len(field.Options) == 0 {
				value = agentsModalEditorFieldEditCursorValue(field)
			}
			entryStart := len(lines)
			fieldPrefix := fmt.Sprintf("%s %s: ", prefix, field.Label)
			for _, line := range wrapAgentsModalWithPrefix(fieldPrefix, value, contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: line, Style: style})
			}
			if len(lines) == entryStart {
				lines = append(lines, agentsModalRenderLine{Text: fieldPrefix, Style: style})
			}
			optionListStart := -1
			if i == editor.Selected && len(field.Options) > 0 {
				if editor.Editing {
					choices := dedupeAgentsModalOptions(field.Options)
					current := normalizeAgentsModalOptionValue(field.Value, choices, "")
					optionListStart = len(lines)
					lines = append(lines, agentsModalRenderLine{Text: "    choices:", Style: p.theme.TextMuted})
					for _, option := range choices {
						label := agentsModalEditorOptionDisplay(option)
						choicePrefix := "      "
						choiceStyle := p.theme.TextMuted
						if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(current)) {
							choicePrefix = "    > "
							choiceStyle = p.theme.Text
						}
						for _, line := range wrapAgentsModalWithPrefix(choicePrefix, label, contentWidth) {
							lines = append(lines, agentsModalRenderLine{Text: line, Style: choiceStyle})
						}
					}
				} else {
					lines = append(lines, agentsModalRenderLine{Text: "    (press Enter to edit choices)", Style: p.theme.TextMuted})
				}
			}
			lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
			entryEnd := len(lines) - 1
			if i == editor.Selected {
				selectedStart = entryStart
				selectedEnd = entryEnd
				if optionListStart >= 0 {
					selectedStart = optionListStart
					selectedEnd = optionListStart
				}
			}
		}
		saveLabel := p.agentsModalEditorSaveLabel()
		saveHint := fmt.Sprintf("Enter edit/commit field • Tab move field • %s save • Esc close", saveLabel)
		if agentsModalEditorHasPendingChanges(editor) {
			saveHint = fmt.Sprintf("Save changes? %s save • Tab move field • Esc asks before closing", saveLabel)
		}
		lines = append(lines, agentsModalRenderLine{Text: saveHint, Style: p.theme.TextMuted})
	} else {
		base := []string{
			"name: " + nonEmpty(profile.Name, "-"),
			"type: " + strings.ToUpper(nonEmpty(profile.Mode, "subagent")),
			"enabled: " + boolLabel(profile.Enabled),
			"protected: " + boolLabel(strings.EqualFold(profile.Name, "swarm") || strings.EqualFold(profile.Name, "memory")),
		}
		if p.agentsModalProfileIsUtility(profile.Name) {
			base = append(base, "tag: Utility AI")
			if strings.TrimSpace(p.agentsModal.UtilityModel) != "" {
				base = append(base, "utility AI: "+nonEmpty(p.agentsModal.UtilityProvider, p.agentsModal.DefaultProvider)+"/"+p.agentsModal.UtilityModel)
			}
		}
		base = append(base,
			"model: "+agentsModalModelLabel(profile.Model),
			"thinking: "+agentsModalThinkingLabel(profile.Thinking),
			"prompt tokens: "+chatFormatTokenCount(agentsModalPromptTokenEstimate(profile.Prompt)),
			"updated: "+agentsModalTimeLabel(profile.UpdatedAt),
		)
		for _, line := range base {
			for _, wrapped := range Wrap(line, contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: wrapped, Style: p.theme.Text})
			}
		}
		lines = append(lines, agentsModalRenderLine{Text: "model/thinking blank = inherit current defaults", Style: p.theme.TextMuted})
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})

		for _, line := range wrapAgentsModalWithPrefix("role: ", nonEmpty(profile.Description, "not set"), contentWidth) {
			lines = append(lines, agentsModalRenderLine{Text: line, Style: p.theme.Text})
		}
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		lines = append(lines, agentsModalRenderLine{Text: "active subagent assignments:", Style: p.theme.TextMuted})
		for _, line := range agentAssignmentLines(p.agentsModal.ActiveSubagent) {
			style := p.theme.TextMuted
			if strings.Contains(strings.ToLower(line), strings.ToLower(profile.Name)) {
				style = p.theme.Text
			}
			for _, wrapped := range Wrap(line, contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: wrapped, Style: style})
			}
		}
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		lines = append(lines, agentsModalRenderLine{Text: "prompt:", Style: p.theme.TextMuted})
		for _, line := range wrapAgentsModalWithPrefix("  ", nonEmpty(strings.TrimSpace(profile.Prompt), "(prompt not set)"), contentWidth) {
			lines = append(lines, agentsModalRenderLine{Text: line, Style: p.theme.TextMuted})
		}
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		lines = append(lines, agentsModalRenderLine{Text: "focus details and use ↑/↓ or PgUp/PgDn to scroll", Style: p.theme.TextMuted})
	}

	if len(lines) == 0 {
		return
	}

	maxScroll := maxInt(0, len(lines)-contentRows)
	if p.agentsModal.DetailScroll > maxScroll {
		p.agentsModal.DetailScroll = maxScroll
	}
	if p.agentsModal.DetailScroll < 0 {
		p.agentsModal.DetailScroll = 0
	}
	if selectedStart >= 0 {
		if p.agentsModal.DetailScroll > selectedStart {
			p.agentsModal.DetailScroll = selectedStart
		}
		if p.agentsModal.DetailScroll+contentRows-1 < selectedEnd {
			p.agentsModal.DetailScroll = selectedEnd - contentRows + 1
			if p.agentsModal.DetailScroll < 0 {
				p.agentsModal.DetailScroll = 0
			}
		}
	}

	for i := 0; i < contentRows; i++ {
		lineIdx := p.agentsModal.DetailScroll + i
		if lineIdx < 0 || lineIdx >= len(lines) {
			break
		}
		DrawText(s, rect.X+2, rowY+i, contentWidth, lines[lineIdx].Style, lines[lineIdx].Text)
	}
}

type agentsModalRenderLine struct {
	Text  string
	Style tcell.Style
}

func wrapAgentsModalWithPrefix(prefix, body string, width int) []string {
	return wrapWithCustomPrefixes(prefix, "", body, width)
}

func (p *HomePage) agentsModalProfileIsUtility(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, agentName := range p.agentsModal.UtilityAgents {
		if strings.EqualFold(strings.TrimSpace(agentName), name) {
			return true
		}
	}
	return false
}

func dedupeAgentsModalOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	hasBlank := false
	for _, raw := range options {
		value := strings.TrimSpace(raw)
		if value == "" {
			if !hasBlank {
				out = append(out, "")
				hasBlank = true
			}
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dedupeAgentsModelOptions(models []string) []string {
	return dedupeAgentsModalOptions(models)
}

func findAgentsModalOption(options []string, value string) (string, bool) {
	target := strings.ToLower(strings.TrimSpace(value))
	if target == "" {
		for _, option := range options {
			if strings.TrimSpace(option) == "" {
				return "", true
			}
		}
		return "", false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), target) {
			return strings.TrimSpace(option), true
		}
	}
	return "", false
}

func findAgentsModalOptionIndex(options []string, value string) int {
	matched, ok := findAgentsModalOption(options, value)
	if !ok {
		return -1
	}
	for i, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(matched)) {
			return i
		}
	}
	return -1
}

func normalizeAgentsModalOptionValue(value string, options []string, fallback string) string {
	options = dedupeAgentsModalOptions(options)
	if len(options) == 0 {
		return strings.TrimSpace(value)
	}
	if matched, ok := findAgentsModalOption(options, value); ok {
		return matched
	}
	if matched, ok := findAgentsModalOption(options, fallback); ok {
		return matched
	}
	return options[0]
}

func normalizeAgentsModalProviderID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func agentsModalReasoningKey(providerID, modelID string) string {
	providerID = normalizeAgentsModalProviderID(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return ""
	}
	return providerID + "/" + strings.ToLower(modelID)
}

func (s *agentsModalState) defaultModelForProvider(providerID string) string {
	providerID = normalizeAgentsModalProviderID(providerID)
	if providerID == "" {
		return ""
	}
	if normalizeAgentsModalProviderID(s.DefaultProvider) == providerID {
		if model := strings.TrimSpace(s.DefaultModel); model != "" {
			return model
		}
	}
	models := s.ModelsByProvider[providerID]
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" {
			return model
		}
	}
	return ""
}

func (p *HomePage) agentsModalProviderOptions() []string {
	out := make([]string, 0, len(p.agentsModal.Providers)+2)
	out = append(out, "")
	out = append(out, p.agentsModal.Providers...)
	if strings.TrimSpace(p.agentsModal.DefaultProvider) != "" {
		out = append(out, strings.TrimSpace(p.agentsModal.DefaultProvider))
	}
	return dedupeAgentsModalOptions(out)
}

func (p *HomePage) normalizeAgentsModalProviderValue(raw string) string {
	value := normalizeAgentsModalProviderID(raw)
	options := p.agentsModalProviderOptions()
	if matched, ok := findAgentsModalOption(options, value); ok {
		return normalizeAgentsModalProviderID(matched)
	}
	if matched, ok := findAgentsModalOption(options, p.agentsModal.DefaultProvider); ok {
		return normalizeAgentsModalProviderID(matched)
	}
	return ""
}

func (p *HomePage) agentsModalModelOptionsForProvider(providerID string) []string {
	providerID = normalizeAgentsModalProviderID(providerID)
	out := make([]string, 0, len(p.agentsModal.ModelsByProvider[providerID])+2)
	out = append(out, "")
	if providerID != "" {
		out = append(out, p.agentsModal.ModelsByProvider[providerID]...)
		if fallback := p.agentsModal.defaultModelForProvider(providerID); fallback != "" {
			out = append(out, fallback)
		}
	}
	return dedupeAgentsModalOptions(out)
}

func (p *HomePage) agentsModalModelReasoningEnabled(providerID, model string) bool {
	key := agentsModalReasoningKey(providerID, model)
	if key == "" || len(p.agentsModal.ReasoningModels) == 0 {
		return true
	}
	if enabled, ok := p.agentsModal.ReasoningModels[key]; ok {
		return enabled
	}
	legacyKey := strings.ToLower(strings.TrimSpace(model))
	if enabled, ok := p.agentsModal.ReasoningModels[legacyKey]; ok {
		return enabled
	}
	return true
}

func (p *HomePage) agentsModalThinkingOptions(providerID, model string) []string {
	effectiveProvider := normalizeAgentsModalProviderID(providerID)
	if effectiveProvider == "" {
		effectiveProvider = normalizeAgentsModalProviderID(p.agentsModal.DefaultProvider)
	}
	effectiveModel := strings.TrimSpace(model)
	if effectiveModel == "" {
		effectiveModel = p.agentsModal.defaultModelForProvider(effectiveProvider)
	}
	options := []string{"", "off"}
	if p.agentsModalModelReasoningEnabled(effectiveProvider, effectiveModel) {
		options = append(options, "low", "medium", "high", "xhigh")
	}
	return dedupeAgentsModalOptions(options)
}

func (p *HomePage) normalizeAgentsModalModelValue(providerID, raw string) string {
	providerID = normalizeAgentsModalProviderID(providerID)
	if providerID == "" {
		return ""
	}
	value := strings.TrimSpace(raw)
	options := p.agentsModalModelOptionsForProvider(providerID)
	if matched, ok := findAgentsModalOption(options, value); ok {
		return matched
	}
	fallback := p.agentsModal.defaultModelForProvider(providerID)
	if matched, ok := findAgentsModalOption(options, fallback); ok {
		return matched
	}
	for _, option := range options {
		if strings.TrimSpace(option) != "" {
			return option
		}
	}
	return ""
}

func normalizeAgentsModalThinkingValue(raw string, options []string, fallback string) string {
	value := normalizeThinkingValue(raw)
	if matched, ok := findAgentsModalOption(options, value); ok {
		return matched
	}
	defaultValue := normalizeThinkingValue(fallback)
	if matched, ok := findAgentsModalOption(options, defaultValue); ok {
		return matched
	}
	if matched, ok := findAgentsModalOption(options, "off"); ok {
		return matched
	}
	if matched, ok := findAgentsModalOption(options, ""); ok {
		return matched
	}
	return ""
}

func (p *HomePage) findAgentsModalEditorField(editor *agentsModalEditor, key string) *agentsModalEditorField {
	if editor == nil {
		return nil
	}
	for i := range editor.Fields {
		if editor.Fields[i].Key == key {
			return &editor.Fields[i]
		}
	}
	return nil
}

func (p *HomePage) normalizeAgentsModalEditorFields(editor *agentsModalEditor) {
	if editor == nil {
		return
	}
	p.syncAgentsModalEditorDependentOptions(editor)
}

func (p *HomePage) syncAgentsModalEditorDependentOptions(editor *agentsModalEditor) {
	if editor == nil {
		return
	}
	if mode := p.findAgentsModalEditorField(editor, "mode"); mode != nil {
		mode.Options = dedupeAgentsModalOptions(nonEmptySlice(mode.Options, []string{"primary", "subagent", "background"}))
		mode.Value = normalizeAgentsModalOptionValue(normalizeAgentModeLiteral(mode.Value), mode.Options, "subagent")
	}
	if enabled := p.findAgentsModalEditorField(editor, "enabled"); enabled != nil {
		enabled.Options = dedupeAgentsModalOptions(nonEmptySlice(enabled.Options, []string{"y", "n"}))
		fallback := "y"
		if editor.Mode == "edit" {
			fallback = "n"
		}
		if parseYN(enabled.Value) {
			enabled.Value = "y"
		} else {
			enabled.Value = "n"
		}
		enabled.Value = normalizeAgentsModalOptionValue(enabled.Value, enabled.Options, fallback)
	}

	providerField := p.findAgentsModalEditorField(editor, "provider")
	if providerField != nil {
		providerField.Options = p.agentsModalProviderOptions()
		providerField.Value = p.normalizeAgentsModalProviderValue(providerField.Value)
	}
	selectedProvider := ""
	if providerField != nil {
		selectedProvider = normalizeAgentsModalProviderID(providerField.Value)
	}

	modelField := p.findAgentsModalEditorField(editor, "model")
	if modelField != nil {
		modelField.Options = p.agentsModalModelOptionsForProvider(selectedProvider)
		modelField.Value = p.normalizeAgentsModalModelValue(selectedProvider, modelField.Value)
	}

	modelValue := ""
	if modelField != nil {
		modelValue = strings.TrimSpace(modelField.Value)
	}
	if thinking := p.findAgentsModalEditorField(editor, "thinking"); thinking != nil {
		thinking.Options = p.agentsModalThinkingOptions(selectedProvider, modelValue)
		thinking.Value = normalizeAgentsModalThinkingValue(thinking.Value, thinking.Options, p.agentsModal.DefaultThinking)
	}
}

func normalizeAgentModeLiteral(raw string) string {
	mode, ok := normalizeAgentModeValue(raw)
	if !ok {
		return "subagent"
	}
	return mode
}

func nonEmptySlice(values []string, fallback []string) []string {
	if len(values) == 0 {
		return append([]string(nil), fallback...)
	}
	return values
}

func (p *HomePage) cycleAgentsModalEditorOption(field *agentsModalEditorField, delta int) {
	if field == nil || delta == 0 {
		return
	}
	options := dedupeAgentsModalOptions(field.Options)
	if len(options) == 0 {
		return
	}
	field.Options = options
	idx := findAgentsModalOptionIndex(options, field.Value)
	if idx < 0 {
		idx = 0
	}
	idx = (idx + delta + len(options)) % len(options)
	field.Value = options[idx]
	p.syncAgentsModalEditorDependentOptions(p.agentsModal.Editor)
}

func (p *HomePage) selectAgentsModalEditorOptionByRune(field *agentsModalEditorField, r rune) {
	if field == nil || !unicode.IsPrint(r) {
		return
	}
	options := dedupeAgentsModalOptions(field.Options)
	if len(options) == 0 {
		return
	}
	search := strings.ToLower(string(r))
	for _, option := range options {
		label := strings.ToLower(agentsModalEditorOptionDisplay(option))
		if strings.HasPrefix(label, search) {
			field.Value = option
			p.syncAgentsModalEditorDependentOptions(p.agentsModal.Editor)
			return
		}
	}
}

func agentsModalEditorOptionDisplay(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return "inherit"
	}
	switch strings.ToLower(v) {
	case "y":
		return "enabled"
	case "n":
		return "disabled"
	default:
		return v
	}
}

func agentsModalEditorFieldDisplayValue(field agentsModalEditorField, fallback string) string {
	value := strings.TrimSpace(field.Value)
	switch field.Key {
	case "provider":
		if value == "" {
			return "inherit default provider"
		}
	case "model", "thinking":
		if value == "" {
			return "inherit default model/thinking"
		}
	case "enabled":
		if parseYN(value) {
			return "enabled"
		}
		return "disabled"
	}
	if value == "" {
		return fallback
	}
	return value
}

func agentsModalEditorFieldEditCursorValue(field agentsModalEditorField) string {
	value := strings.ReplaceAll(field.Value, "\r\n", "\n")
	value = strings.TrimRight(value, "\r")
	return value + "|"
}

func agentsModalEditorFieldOptionsDisplay(options []string) []string {
	unique := dedupeAgentsModalOptions(options)
	out := make([]string, 0, len(unique))
	for _, option := range unique {
		out = append(out, agentsModalEditorOptionDisplay(option))
	}
	return out
}

func agentsModalPromptTokenEstimate(prompt string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return 0
	}
	totalRunes := utf8.RuneCountInString(prompt)
	if totalRunes <= 0 {
		return 0
	}
	return (totalRunes + 3) / 4
}

func agentAssignmentLines(assignments map[string]string) []string {
	if len(assignments) == 0 {
		return []string{"- none"}
	}
	keys := make([]string, 0, len(assignments))
	for role := range assignments {
		keys = append(keys, role)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, role := range keys {
		name := strings.TrimSpace(assignments[role])
		if name == "" {
			name = "-"
		}
		lines = append(lines, fmt.Sprintf("- %s -> %s", role, name))
	}
	return lines
}

func agentsModalModeLabel(mode string) string {
	if strings.EqualFold(mode, "primary") {
		return "primary"
	}
	if strings.EqualFold(mode, "background") {
		return "background"
	}
	return "subagent"
}

func agentsModalEnabledLabel(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func agentsModalModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "inherit default model"
	}
	return model
}

func agentsModalThinkingLabel(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return "inherit default thinking"
	}
	return thinking
}

func agentAssignedRoles(assignments map[string]string, profileName string) []string {
	name := strings.ToLower(strings.TrimSpace(profileName))
	if name == "" || len(assignments) == 0 {
		return nil
	}
	roles := make([]string, 0, len(assignments))
	for role, assigned := range assignments {
		if strings.EqualFold(strings.TrimSpace(assigned), name) {
			roles = append(roles, strings.ToLower(strings.TrimSpace(role)))
		}
	}
	sort.Strings(roles)
	return roles
}

func agentsModalRoleSummary(profile AgentModalProfile, assignments map[string]string) string {
	role := nonEmpty(strings.TrimSpace(profile.Description), "not set")
	assigned := agentAssignedRoles(assignments, profile.Name)
	if len(assigned) == 0 {
		return role
	}
	return role + " | assigned: " + strings.Join(assigned, ", ")
}

func agentsModalExecutionSettingLabel(setting string) string {
	setting = strings.ToLower(strings.TrimSpace(setting))
	if setting == "" {
		return "plan"
	}
	switch setting {
	case "readwrite":
		return "read/write"
	case "read":
		return "read"
	default:
		return setting
	}
}

func agentsModalRuntimeLabel(profile AgentModalProfile) string {
	if strings.TrimSpace(profile.Mode) == "" {
		return "unset"
	}
	if strings.EqualFold(strings.TrimSpace(profile.Name), "swarm") {
		return "plan → auto"
	}
	switch strings.ToLower(strings.TrimSpace(profile.Mode)) {
	case "primary":
		return "primary"
	case "background":
		return "background"
	default:
		return "subagent"
	}
}

func agentsModalTimeLabel(unixMillis int64) string {
	if unixMillis <= 0 {
		return "-"
	}
	return time.UnixMilli(unixMillis).Local().Format("2006-01-02 15:04")
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

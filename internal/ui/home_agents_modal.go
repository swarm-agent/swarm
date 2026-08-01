package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	modelpkg "swarm-refactor/swarmtui/internal/model"
)

type AgentModalProfile struct {
	Name               string
	Mode               string
	Description        string
	Provider           string
	Model              string
	Thinking           string
	ServiceTier        string
	DefaultSessionMode string
	ModelMode          string
	PlanProvider       string
	PlanModel          string
	PlanThinking       string
	PlanServiceTier    string
	AutoProvider       string
	AutoModel          string
	AutoThinking       string
	AutoServiceTier    string
	Prompt             string
	ExecutionSetting   string
	Enabled            bool
	Protected          bool
	UpdatedAt          int64
}

type AgentsModalData struct {
	Profiles              []AgentModalProfile
	ActivePrimary         string
	ActiveSubagent        map[string]string
	Version               int64
	Providers             []string
	ModelsByProvider      map[string][]string
	ModelCatalog          map[string]client.ModelCatalogRecord
	DefaultProvider       string
	DefaultModel          string
	DefaultThinking       string
	UtilityProvider       string
	UtilityModel          string
	UtilityThinking       string
	UtilityAgents         []string
	CustomUtilityAgents   []string
	UtilityBaselineAgents []string
	StaleInheritedAgents  []string
	ModelProfiles         []client.ModelProfile
	DefaultModelProfileID string
	ActiveModelProfileID  string
}

type AgentsModalActionKind string

const (
	AgentsModalActionRefresh            AgentsModalActionKind = "refresh"
	AgentsModalActionSetUtilityAI       AgentsModalActionKind = "set-utility-ai"
	AgentsModalActionResetDefaults      AgentsModalActionKind = "reset-defaults"
	AgentsModalActionActivatePrimary    AgentsModalActionKind = "activate-primary"
	AgentsModalActionUpsert             AgentsModalActionKind = "upsert"
	AgentsModalActionCreateModelProfile AgentsModalActionKind = "create-model-profile"
	AgentsModalActionUpdateModelProfile AgentsModalActionKind = "update-model-profile"
	AgentsModalActionApplyTemporary     AgentsModalActionKind = "apply-temporary-model-profile"
	AgentsModalActionDelete             AgentsModalActionKind = "delete"
	AgentsModalActionSetProfileDefault  AgentsModalActionKind = "set-profile-default"
	AgentsModalActionSwitchProfile      AgentsModalActionKind = "switch-profile"
)

type AgentsModalUpsert struct {
	Name               string
	Mode               string
	Description        string
	Provider           string
	Model              string
	Thinking           string
	ServiceTier        string
	DefaultSessionMode string
	ModelMode          string
	PlanProvider       string
	PlanModel          string
	PlanThinking       string
	PlanServiceTier    string
	AutoProvider       string
	AutoModel          string
	AutoThinking       string
	AutoServiceTier    string
	Prompt             string
	Enabled            *bool
}

type AgentsModalUtilityAI struct {
	Provider          string
	Model             string
	Thinking          string
	OverwriteExplicit bool
}

type AgentsModalAction struct {
	Kind              AgentsModalActionKind
	Name              string
	Upsert            *AgentsModalUpsert
	UtilityAI         *AgentsModalUtilityAI
	ModelProfile      *client.ModelProfileInput
	ApplyModelProfile bool
	StatusHint        string
	ModelProfileID    string
}

type agentsModalFocus int

const (
	agentsModalFocusProfiles agentsModalFocus = iota
	agentsModalFocusDetails
	agentsModalFocusSearch
)

type agentsV2Screen int

const (
	agentsV2ScreenList agentsV2Screen = iota
	agentsV2ScreenEditor
)

const agentsModalCreateProfileOption = "__create_new_profile__"

type agentsModalEditor struct {
	Mode                string
	TargetName          string
	Fields              []agentsModalEditorField
	InitialFields       []agentsModalEditorField
	Selected            int
	Editing             bool
	EditingOption       string
	EditingOptionSet    bool
	ActionSelected      int
	ActionFocused       bool
	CreateModelProfile  bool
	AgentSettingsLocked bool
	ModelReadOnly       bool
}

type agentsModalEditorField struct {
	Key         string
	Label       string
	Value       string
	Placeholder string
	Options     []string
}

type agentsModalState struct {
	Visible                bool
	Screen                 agentsV2Screen
	Loading                bool
	Status                 string
	Error                  string
	Focus                  agentsModalFocus
	Search                 string
	FilterMode             string
	Profiles               []AgentModalProfile
	ActivePrimary          string
	ActiveSubagent         map[string]string
	Version                int64
	Providers              []string
	ModelsByProvider       map[string][]string
	ModelCatalog           map[string]client.ModelCatalogRecord
	DefaultProvider        string
	DefaultModel           string
	DefaultThinking        string
	UtilityProvider        string
	UtilityModel           string
	UtilityThinking        string
	UtilityAgents          []string
	CustomUtilityAgents    []string
	UtilityBaselineAgents  []string
	StaleInheritedAgents   []string
	ModelProfiles          []client.ModelProfile
	DefaultModelProfileID  string
	ActiveModelProfileID   string
	SelectedModelProfileID string
	SelectedProfile        int
	ListScroll             int
	DetailScroll           int
	ConfirmDelete          bool
	ConfirmName            string
	ConfirmResetDefaults   bool
	ConfirmUnsaved         bool
	UnsavedSaveFirst       bool
	Editor                 *agentsModalEditor
}

func (p *HomePage) ShowAgentsModal() {
	p.agentsModal.Visible = true
	p.agentsModal.Screen = agentsV2ScreenList
	p.agentsModal.Focus = agentsModalFocusProfiles
	p.agentsModal.FilterMode = "all"
	p.agentsModal.Editor = nil
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) HideAgentsModal() {
	p.agentsModal = agentsModalState{}
	p.agentsModalTargets = p.agentsModalTargets[:0]
	p.pendingAgentsAction = nil
}

func (p *HomePage) AgentsModalVisible() bool {
	return p.agentsModal.Visible
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
	p.agentsModal.ModelCatalog = make(map[string]client.ModelCatalogRecord, len(data.ModelCatalog))
	for name, record := range data.ModelCatalog {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		record.Provider = normalizeAgentsModalProviderID(record.Provider)
		record.Model = strings.TrimSpace(record.Model)
		record.ThinkingOptions = normalizeAgentsModalCatalogOptions(record.ThinkingOptions)
		record.DefaultThinking = normalizeThinkingValue(record.DefaultThinking)
		record.ServiceTiers = normalizeAgentsModalServiceTierOptions(record.ServiceTiers, record.ServiceTierMappings)
		record.DefaultServiceTier = normalizeAgentsModalServiceTierValue(record.DefaultServiceTier)
		p.agentsModal.ModelCatalog[key] = record
	}
	p.agentsModal.DefaultProvider = strings.ToLower(strings.TrimSpace(data.DefaultProvider))
	p.agentsModal.DefaultModel = strings.TrimSpace(data.DefaultModel)
	p.agentsModal.DefaultThinking = normalizeThinkingValue(data.DefaultThinking)
	if record, ok := p.agentsModalCatalogRecord(p.agentsModal.DefaultProvider, p.agentsModal.DefaultModel); ok && p.agentsModal.DefaultThinking == "" {
		p.agentsModal.DefaultThinking = record.DefaultThinking
	}
	p.agentsModal.UtilityProvider = strings.ToLower(strings.TrimSpace(data.UtilityProvider))
	p.agentsModal.UtilityModel = strings.TrimSpace(data.UtilityModel)
	p.agentsModal.UtilityThinking = normalizeThinkingValue(data.UtilityThinking)
	p.agentsModal.UtilityAgents = dedupeAgentsModalOptions(data.UtilityAgents)
	if len(p.agentsModal.UtilityAgents) == 0 {
		p.agentsModal.UtilityAgents = []string{"finder", "memory"}
	}
	p.agentsModal.CustomUtilityAgents = dedupeAgentsModalOptions(data.CustomUtilityAgents)
	p.agentsModal.UtilityBaselineAgents = dedupeAgentsModalOptions(data.UtilityBaselineAgents)
	if len(p.agentsModal.UtilityBaselineAgents) == 0 {
		p.agentsModal.UtilityBaselineAgents = agentsModalUtilityBaselineAgents(p.agentsModal.UtilityAgents, p.agentsModal.CustomUtilityAgents)
	}
	if len(p.agentsModal.UtilityBaselineAgents) == 0 && len(p.agentsModal.CustomUtilityAgents) == 0 {
		p.agentsModal.UtilityBaselineAgents = append([]string(nil), p.agentsModal.UtilityAgents...)
	}
	if p.agentsModal.UtilityProvider == "" || p.agentsModal.UtilityModel == "" {
		for _, name := range p.agentsModal.UtilityBaselineAgents {
			for _, profile := range p.agentsModal.Profiles {
				if !strings.EqualFold(strings.TrimSpace(profile.Name), name) {
					continue
				}
				profileProvider := strings.ToLower(strings.TrimSpace(profile.Provider))
				profileModel := strings.TrimSpace(profile.Model)
				if profileProvider != "" && profileModel != "" {
					p.agentsModal.UtilityProvider = profileProvider
					p.agentsModal.UtilityModel = profileModel
					p.agentsModal.UtilityThinking = normalizeThinkingValue(profile.Thinking)
					break
				}
			}
			if p.agentsModal.UtilityProvider != "" && p.agentsModal.UtilityModel != "" {
				break
			}
		}
	}
	p.agentsModal.StaleInheritedAgents = dedupeAgentsModalOptions(data.StaleInheritedAgents)
	p.agentsModal.ModelProfiles = append([]client.ModelProfile(nil), data.ModelProfiles...)
	p.agentsModal.DefaultModelProfileID = strings.TrimSpace(data.DefaultModelProfileID)
	p.agentsModal.ActiveModelProfileID = strings.TrimSpace(data.ActiveModelProfileID)
	if p.agentsModal.ActiveModelProfileID == "" {
		p.agentsModal.ActiveModelProfileID = p.agentsModal.DefaultModelProfileID
	}
	if p.agentsModal.SelectedModelProfileID == "" || !p.agentsModalHasModelProfile(p.agentsModal.SelectedModelProfileID) {
		p.agentsModal.SelectedModelProfileID = p.agentsModal.ActiveModelProfileID
	}
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
	if p.agentsModal.Screen == agentsV2ScreenEditor {
		if profile, ok := p.selectedAgentsModalProfile(); ok {
			p.openAgentsModalEditEditor(profile)
			p.agentsModal.Focus = agentsModalFocusDetails
		}
	} else {
		p.agentsModal.Editor = nil
		p.agentsModal.Focus = agentsModalFocusProfiles
	}
	if p.agentsModal.ConfirmDelete && !strings.EqualFold(strings.TrimSpace(p.agentsModal.ConfirmName), p.selectedAgentsModalName()) {
		p.clearAgentsModalDeleteConfirm()
	}
}

func (p *HomePage) agentsModalHasModelProfile(profileID string) bool {
	profileID = strings.TrimSpace(profileID)
	if p == nil || profileID == "" {
		return false
	}
	for _, profile := range p.agentsModal.ModelProfiles {
		if strings.TrimSpace(profile.ProfileID) == profileID {
			return true
		}
	}
	return false
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
	initialByKey := make(map[string]string, len(editor.InitialFields))
	for _, field := range editor.InitialFields {
		initialByKey[field.Key] = field.Value
	}
	for i, current := range editor.Fields {
		value := current.Value
		if editor.Editing && editor.Selected == i && editor.EditingOptionSet && len(current.Options) > 0 {
			value = editor.EditingOption
		}
		initial, ok := initialByKey[current.Key]
		if !ok || value != initial {
			return true
		}
		delete(initialByKey, current.Key)
	}
	return len(initialByKey) != 0
}

func (p *HomePage) agentsModalEditorProfileSwitch(editor *agentsModalEditor) (string, bool) {
	if p == nil || editor == nil {
		return "", false
	}
	selected := ""
	for _, field := range editor.Fields {
		if field.Key == "model_profile" {
			selected = strings.TrimSpace(field.Value)
			break
		}
	}
	active := strings.TrimSpace(p.agentsModal.ActiveModelProfileID)
	return selected, selected != "" && selected != active
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
	p.agentsModal.Screen = agentsV2ScreenList
	p.agentsModal.Focus = agentsModalFocusProfiles
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = "changes discarded; back to agent list"
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

func (p *HomePage) registerAgentsModalTarget(rect Rect, action string, index int, meta string) {
	if action == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.agentsModalTargets = append(p.agentsModalTargets, clickTarget{Rect: rect, Action: action, Index: index, Meta: meta})
}

func (p *HomePage) agentsModalTargetAt(x, y int) (clickTarget, bool) {
	for i := len(p.agentsModalTargets) - 1; i >= 0; i-- {
		target := p.agentsModalTargets[i]
		if target.Rect.Contains(x, y) {
			return target, true
		}
	}
	return clickTarget{}, false
}

func (p *HomePage) handleAgentsModalKey(ev *tcell.EventKey) {
	if p.agentsModal.Screen == agentsV2ScreenEditor && p.agentsModal.Editor != nil {
		p.handleAgentsModalEditorKey(ev)
		return
	}

	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		if p.agentsModal.Focus == agentsModalFocusDetails {
			if agentsModalEditorHasPendingChanges(p.agentsModal.Editor) {
				p.openAgentsModalUnsavedConfirm()
				return
			}
			p.agentsModal.Focus = agentsModalFocusProfiles
			p.agentsModal.DetailScroll = 0
			p.agentsModal.Status = "back to agent list"
			p.clearAgentsModalDeleteConfirm()
			return
		}
		p.HideAgentsModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext), p.keybinds.Match(ev, KeybindModalFocusPrev):
		p.agentsModal.Focus = agentsModalFocusProfiles
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalFocusLeft):
		p.agentsModal.Focus = agentsModalFocusProfiles
		p.clearAgentsModalDeleteConfirm()
		return
	case p.keybinds.Match(ev, KeybindModalFocusRight):
		p.openAgentsV2Editor()
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
		p.openAgentsModalUtilityAIEditor()
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
		p.focusAgentsModalDetails()
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

func (p *HomePage) handleAgentsModalMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil || !p.agentsModal.Visible {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.WheelUp != 0 || buttons&tcell.WheelDown != 0 {
		delta := -1
		if buttons&tcell.WheelDown != 0 {
			delta = 1
		}
		p.handleAgentsModalMouseWheel(x, y, delta)
		return true
	}
	target, ok := p.agentsModalTargetAt(x, y)
	if ok && p.agentsModal.Screen == agentsV2ScreenList && target.Action == "agents-profile" && target.Index >= 0 && target.Index < len(p.agentsModal.Profiles) {
		p.agentsModal.SelectedProfile = target.Index
		p.agentsModal.Focus = agentsModalFocusProfiles
	}
	if buttons&tcell.Button1 == 0 || !ok {
		return true
	}
	p.activateAgentsModalTarget(target)
	return true
}

func (p *HomePage) handleAgentsModalMouseWheel(x, y, delta int) {
	if delta == 0 {
		return
	}
	if target, ok := p.agentsModalTargetAt(x, y); ok {
		switch target.Action {
		case "agents-profile":
			p.agentsModal.Focus = agentsModalFocusProfiles
		case "agents-detail", "agents-editor-field":
			p.agentsModal.Focus = agentsModalFocusDetails
		}
	}
	if p.agentsModal.Focus == agentsModalFocusDetails {
		p.scrollAgentsModalDetail(delta * 3)
		return
	}
	p.agentsModal.Focus = agentsModalFocusProfiles
	p.moveAgentsModalSelection(delta * 3)
}

func (p *HomePage) activateAgentsModalTarget(target clickTarget) {
	switch target.Action {
	case "agents-profile":
		if target.Index < 0 || target.Index >= len(p.agentsModal.Profiles) {
			return
		}
		p.agentsModal.SelectedProfile = target.Index
		p.openAgentsV2Editor()
	case "agents-detail":
		p.agentsModal.Focus = agentsModalFocusDetails
		if target.Index >= 0 && target.Index < len(p.agentsModal.Profiles) {
			p.agentsModal.SelectedProfile = target.Index
		}
	case "agents-editor-field":
		editor := p.agentsModal.Editor
		if editor == nil || target.Index < 0 || target.Index >= len(editor.Fields) {
			return
		}
		p.beginAgentsModalEditorFieldEdit(editor, target.Index)
		p.agentsModal.Focus = agentsModalFocusDetails
		p.agentsModal.Status = fmt.Sprintf("editing %s • Enter commits the choice", strings.ToLower(strings.TrimSpace(editor.Fields[target.Index].Label)))
		p.agentsModal.Error = ""
	case "agents-editor-option", "agents-model-profile-option":
		editor := p.agentsModal.Editor
		if editor == nil || target.Index < 0 || target.Index >= len(editor.Fields) {
			return
		}
		editor.Selected = target.Index
		editor.Fields[target.Index].Value = target.Meta
		editor.Editing = false
		p.agentsModal.Focus = agentsModalFocusDetails
		if target.Action == "agents-model-profile-option" {
			if target.Meta == agentsModalCreateProfileOption {
				p.openAgentsModalCreateModelProfileEditor()
				return
			}
			p.applyAgentsModalModelProfile(target.Meta)
			return
		}
		p.syncAgentsModalEditorDependentOptions(editor)
		p.agentsModal.Status = fmt.Sprintf("field updated: %s", strings.ToLower(strings.TrimSpace(editor.Fields[target.Index].Label)))
		p.agentsModal.Error = ""
	case "agents-profile-default":
		p.queueAgentsModalProfileDefault(target.Meta)
	case "agents-editor-action":
		p.handleAgentsModalEditorAction(target.Meta)
	case "agents-unsaved-save":
		p.resolveAgentsModalUnsavedConfirm(true)
	case "agents-unsaved-discard":
		p.resolveAgentsModalUnsavedConfirm(false)
	case "agents-search":
		p.agentsModal.Focus = agentsModalFocusSearch
		p.agentsModal.Status = "type to search agents"
		p.agentsModal.Error = ""
	}
}

func (p *HomePage) handleAgentsModalEnter() {
	switch p.agentsModal.Focus {
	case agentsModalFocusSearch:
		p.agentsModal.Focus = agentsModalFocusProfiles
	case agentsModalFocusProfiles:
		p.focusAgentsModalDetails()
	case agentsModalFocusDetails:
		if p.agentsModal.Editor != nil {
			p.handleAgentsModalEditorKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
		}
	}
}

func (p *HomePage) focusAgentsModalDetails() {
	p.openAgentsV2Editor()
}

func (p *HomePage) openAgentsV2Editor() {
	if p == nil {
		return
	}
	profile, ok := p.selectedAgentsModalProfile()
	if !ok {
		p.agentsModal.Status = "No agent selected"
		return
	}
	p.openAgentsModalEditEditor(profile)
	p.agentsModal.Screen = agentsV2ScreenEditor
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = fmt.Sprintf("Editing model settings: %s", agentsModalDisplayName(profile.Name))
}

func (p *HomePage) closeAgentsV2Editor() {
	p.agentsModal.Editor = nil
	p.agentsModal.Screen = agentsV2ScreenList
	p.agentsModal.Focus = agentsModalFocusProfiles
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = "back to agent list"
	p.dismissAgentsModalUnsavedConfirm()
	p.clearAgentsModalDeleteConfirm()
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

func (p *HomePage) openAgentsModalCreateModelProfileEditor() {
	selectedAgent, ok := p.selectedAgentsModalProfile()
	if !ok {
		p.agentsModal.Error = "select an agent before creating a model profile"
		return
	}
	p.openAgentsModalEditEditor(selectedAgent)
	editor := p.agentsModal.Editor
	if editor == nil {
		return
	}
	editor.CreateModelProfile = true
	editor.AgentSettingsLocked = false
	editor.ModelReadOnly = false
	filtered := make([]agentsModalEditorField, 0, len(editor.Fields))
	filtered = append(filtered, agentsModalEditorField{Key: "profile_name", Label: "Profile name", Placeholder: "My model profile"})
	for _, field := range editor.Fields {
		if field.Key == "model_profile" || field.Key == "default_session_mode" {
			continue
		}
		filtered = append(filtered, field)
	}
	editor.Fields = filtered
	for i := range editor.Fields {
		switch editor.Fields[i].Key {
		case "provider", "model", "thinking", "service_tier",
			"plan_provider", "plan_model", "plan_thinking", "plan_service_tier",
			"auto_provider", "auto_model", "auto_thinking", "auto_service_tier":
			editor.Fields[i].Value = ""
		}
	}
	p.syncAgentsModalEditorDependentOptions(editor)
	editor.InitialFields = cloneAgentsModalEditorFields(editor.Fields)
	editor.Selected = 0
	editor.Editing = true
	p.agentsModal.Screen = agentsV2ScreenEditor
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Status = fmt.Sprintf("Name the saved model profile, adjust its model settings, then %s creates it", p.agentsModalEditorSaveLabel())
	p.agentsModal.Error = ""
}

func (p *HomePage) openAgentsModalUtilityAIEditor() {
	provider := p.agentsModal.UtilityProvider
	model := p.agentsModal.UtilityModel
	thinking := p.agentsModal.UtilityThinking
	if provider == "" || model == "" {
		for _, name := range p.agentsModal.UtilityBaselineAgents {
			profile, ok := p.findAgentsModalProfileByName(name)
			if !ok {
				continue
			}
			profileProvider := strings.ToLower(strings.TrimSpace(profile.Provider))
			profileModel := strings.TrimSpace(profile.Model)
			if profileProvider != "" && profileModel != "" {
				provider = profileProvider
				model = profileModel
				thinking = normalizeThinkingValue(profile.Thinking)
				break
			}
		}
	}
	if thinking == "" {
		thinking = "off"
	}
	providerOptions := p.agentsModalProviderOptions()
	modelOptions := p.agentsModalModelOptionsForProvider(provider)
	thinkingOptions := p.agentsModalThinkingOptions(provider, model)
	p.agentsModal.Editor = &agentsModalEditor{
		Mode:       "utility-ai",
		TargetName: "utility-ai",
		Fields: []agentsModalEditorField{
			{Key: "provider", Label: "Provider", Value: provider, Placeholder: "choose provider", Options: providerOptions},
			{Key: "model", Label: "Model", Value: model, Placeholder: "choose model", Options: modelOptions},
			{Key: "thinking", Label: "Thinking", Value: thinking, Placeholder: "thinking", Options: thinkingOptions},
			{Key: "scope", Label: "Apply", Value: "blank only", Placeholder: "blank only", Options: []string{"blank only", "clear overrides"}},
		},
	}
	p.agentsModal.Editor.Editing = false
	p.normalizeAgentsModalEditorFields(p.agentsModal.Editor)
	p.agentsModal.Editor.InitialFields = cloneAgentsModalEditorFields(p.agentsModal.Editor.Fields)
	p.agentsModal.Focus = agentsModalFocusDetails
	p.agentsModal.DetailScroll = 0
	status := fmt.Sprintf("Set Utility AI fills blank utility agents (%s). Enter edits/commits fields. %s saves changes.", p.agentsModalUtilityAgentsLabel(), p.agentsModalEditorSaveLabel())
	if customLabel := p.agentsModalCustomUtilityAgentsLabel(); customLabel != "" {
		status += " Existing overrides for " + customLabel + " stay custom unless Apply=clear overrides."
	}
	p.agentsModal.Status = status
	p.agentsModal.Error = ""
	p.dismissAgentsModalUnsavedConfirm()
	p.clearAgentsModalDeleteConfirm()
}

func (p *HomePage) openAgentsModalEditEditor(profile AgentModalProfile) {
	profileName := strings.ToLower(strings.TrimSpace(profile.Name))
	agentSettingsLocked := profileName == "system-clone" || profileName == "clone" || profileName == "coder" || profileName == "system-finder" || profileName == "finder" || profileName == "system-designer" || profileName == "designer"
	modelReadOnly := false
	modelMode := strings.ToLower(strings.TrimSpace(profile.ModelMode))
	if modelMode != "split" {
		modelMode = "single"
	}
	sessionMode := strings.ToLower(strings.TrimSpace(profile.DefaultSessionMode))
	if sessionMode != "plan" {
		sessionMode = "auto"
	}
	providerOptions := p.agentsModalProviderOptions()
	singleProvider := strings.ToLower(strings.TrimSpace(profile.Provider))
	planProvider := strings.ToLower(strings.TrimSpace(profile.PlanProvider))
	autoProvider := strings.ToLower(strings.TrimSpace(profile.AutoProvider))
	if planProvider == "" {
		planProvider = singleProvider
	}
	if autoProvider == "" {
		autoProvider = singleProvider
	}
	planModel := strings.TrimSpace(profile.PlanModel)
	if planModel == "" {
		planModel = strings.TrimSpace(profile.Model)
	}
	autoModel := strings.TrimSpace(profile.AutoModel)
	if autoModel == "" {
		autoModel = strings.TrimSpace(profile.Model)
	}
	planThinking := strings.TrimSpace(profile.PlanThinking)
	if planThinking == "" {
		planThinking = strings.TrimSpace(profile.Thinking)
	}
	autoThinking := strings.TrimSpace(profile.AutoThinking)
	if autoThinking == "" {
		autoThinking = strings.TrimSpace(profile.Thinking)
	}
	modelProfileOptions := p.agentsModalModelProfileOptions(profile)
	if !isCompiledSingleModelSubagent(profile.Name) {
		modelProfileOptions = append(modelProfileOptions, agentsModalCreateProfileOption)
	}
	selectedModelProfileID := p.agentsModal.SelectedModelProfileID
	if isCompiledSingleModelSubagent(profile.Name) && !agentsModalStringOptionExists(modelProfileOptions, selectedModelProfileID) {
		selectedModelProfileID = ""
	}
	fields := []agentsModalEditorField{
		{Key: "model_profile", Label: "Profile", Value: selectedModelProfileID, Options: modelProfileOptions},
		{Key: "default_session_mode", Label: "Default session", Value: sessionMode, Options: []string{"plan", "auto"}},
		{Key: "model_mode", Label: "Model policy", Value: modelMode, Options: []string{"single", "split"}},
		{Key: "provider", Label: "Provider", Value: singleProvider, Placeholder: "choose provider", Options: providerOptions},
		{Key: "model", Label: "Model", Value: profile.Model, Placeholder: "choose model", Options: p.agentsModalModelOptionsForProvider(singleProvider)},
		{Key: "thinking", Label: "Thinking", Value: profile.Thinking, Placeholder: "off", Options: p.agentsModalThinkingOptions(singleProvider, profile.Model)},
		{Key: "service_tier", Label: "Priority", Value: profile.ServiceTier, Placeholder: "off", Options: p.agentsModalServiceTierOptions(singleProvider, profile.Model)},
		{Key: "plan_provider", Label: "Provider", Value: planProvider, Placeholder: "choose provider", Options: providerOptions},
		{Key: "plan_model", Label: "Model", Value: planModel, Placeholder: "choose model", Options: p.agentsModalModelOptionsForProvider(planProvider)},
		{Key: "plan_thinking", Label: "Thinking", Value: planThinking, Placeholder: "off", Options: p.agentsModalThinkingOptions(planProvider, planModel)},
		{Key: "plan_service_tier", Label: "Priority", Value: profile.PlanServiceTier, Placeholder: "off", Options: p.agentsModalServiceTierOptions(planProvider, planModel)},
		{Key: "auto_provider", Label: "Provider", Value: autoProvider, Placeholder: "choose provider", Options: providerOptions},
		{Key: "auto_model", Label: "Model", Value: autoModel, Placeholder: "choose model", Options: p.agentsModalModelOptionsForProvider(autoProvider)},
		{Key: "auto_thinking", Label: "Thinking", Value: autoThinking, Placeholder: "off", Options: p.agentsModalThinkingOptions(autoProvider, autoModel)},
		{Key: "auto_service_tier", Label: "Priority", Value: profile.AutoServiceTier, Placeholder: "off", Options: p.agentsModalServiceTierOptions(autoProvider, autoModel)},
	}
	p.agentsModal.Editor = &agentsModalEditor{Mode: "model", TargetName: profile.Name, Fields: fields, AgentSettingsLocked: agentSettingsLocked, ModelReadOnly: modelReadOnly}
	if selectedModelProfileID == "" || !p.applyAgentsModalModelProfile(selectedModelProfileID) {
		p.normalizeAgentsModalEditorFields(p.agentsModal.Editor)
	}
	p.agentsModal.Editor.InitialFields = cloneAgentsModalEditorFields(p.agentsModal.Editor.Fields)
	p.agentsModal.Editor.Selected = 0
	if visible := agentsModalVisibleEditorFieldIndexes(p.agentsModal.Editor); len(visible) > 0 {
		p.agentsModal.Editor.Selected = visible[0]
	}
	p.agentsModal.DetailScroll = 0
	if agentSettingsLocked {
		p.agentsModal.Status = fmt.Sprintf("%s is a compiled system agent • agent policy locked • single-model choices only", agentsModalDisplayName(profile.Name))
	} else {
		p.agentsModal.Status = fmt.Sprintf("%s setup • Enter opens a selector • arrows navigate • %s saves", profile.Name, p.agentsModalEditorSaveLabel())
	}
	p.agentsModal.Error = ""
	p.dismissAgentsModalUnsavedConfirm()
}

func agentsModalStringOptionExists(options []string, target string) bool {
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func isCompiledSingleModelSubagent(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "system-clone" || name == "clone" || name == "coder" || name == "system-finder" || name == "finder" || name == "system-designer" || name == "designer"
}

func (p *HomePage) agentsModalModelProfileOptions(agent AgentModalProfile) []string {
	if p == nil || len(p.agentsModal.ModelProfiles) == 0 {
		return nil
	}
	singleOnly := isCompiledSingleModelSubagent(agent.Name)
	out := make([]string, 0, len(p.agentsModal.ModelProfiles))
	for _, profile := range p.agentsModal.ModelProfiles {
		if singleOnly && (!strings.EqualFold(strings.TrimSpace(profile.ModelMode), "single") || profile.Single == nil) {
			continue
		}
		if id := strings.TrimSpace(profile.ProfileID); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (p *HomePage) agentsModalServiceTierOptions(providerID, model string) []string {
	record, ok := p.agentsModalCatalogRecord(providerID, model)
	if !ok {
		return []string{""}
	}
	return dedupeAgentsModalOptions(append([]string{""}, record.ServiceTiers...))
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
		visible := agentsModalVisibleEditorFieldIndexes(editor)
		if len(visible) == 0 {
			return
		}
		pos := indexInList(visible, editor.Selected)
		if pos < 0 {
			pos = 0
		} else {
			pos = (pos + delta + len(visible)) % len(visible)
		}
		editor.Selected = visible[pos]
		editor.ActionFocused = false
		p.agentsModal.DetailScroll = 0
	}

	selectedField := func() *agentsModalEditorField {
		if editor == nil || editor.Selected < 0 || editor.Selected >= len(editor.Fields) {
			return nil
		}
		return &editor.Fields[editor.Selected]
	}
	editorActions := agentsModalEditorActions(editor)
	commitFieldEdit := func() {
		if !editor.Editing {
			return
		}
		if editor.Selected >= 0 && editor.Selected < len(editor.Fields) {
			field := &editor.Fields[editor.Selected]
			if len(field.Options) > 0 && editor.EditingOptionSet {
				field.Value = editor.EditingOption
			}
		}
		editor.Editing = false
		editor.EditingOption = ""
		editor.EditingOptionSet = false
		p.syncAgentsModalEditorDependentOptions(editor)
	}
	moveAction := func(delta int) {
		if len(editorActions) == 0 {
			return
		}
		editor.ActionFocused = true
		editor.ActionSelected = (editor.ActionSelected + delta + len(editorActions)) % len(editorActions)
		p.agentsModal.DetailScroll = 1 << 20
	}

	switch {
	case p.keybinds.Match(ev, KeybindEditorClose):
		if editor.Editing {
			editor.Editing = false
			editor.EditingOption = ""
			editor.EditingOptionSet = false
			p.agentsModal.Status = "field edit canceled"
			return
		}
		if agentsModalEditorHasPendingChanges(editor) {
			p.openAgentsModalUnsavedConfirm()
			return
		}
		p.closeAgentsV2Editor()
		return
	case !editor.Editing && p.keybinds.Match(ev, KeybindAgentsProfileDefault):
		field := p.findAgentsModalEditorField(editor, "model_profile")
		if field == nil {
			p.agentsModal.Status = "select a saved Profile before setting the account default"
			return
		}
		p.queueAgentsModalProfileDefault(field.Value)
		return
	case p.keybinds.Match(ev, KeybindAgentsEditorSave):
		commitFieldEdit()
		p.dismissAgentsModalUnsavedConfirm()
		if !agentsModalEditorHasPendingChanges(editor) {
			if editor.Mode == "utility-ai" || editor.Mode == "utility-ai-overwrite" {
				p.submitAgentsModalEditor()
				return
			}
			if profileID, switchProfile := p.agentsModalEditorProfileSwitch(editor); switchProfile {
				p.queueAgentsModalProfileSwitch(profileID)
				return
			}
			p.agentsModal.Status = "No pending changes to save"
			return
		}
		p.submitAgentsModalEditor()
		return
	case p.keybinds.Match(ev, KeybindEditorFocusNext):
		commitFieldEdit()
		if editor.ActionFocused {
			moveField(1)
		} else {
			visible := agentsModalVisibleEditorFieldIndexes(editor)
			if len(visible) > 0 && editor.Selected == visible[len(visible)-1] && len(editorActions) > 0 {
				moveAction(0)
			} else {
				moveField(1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorFocusPrev):
		commitFieldEdit()
		if editor.ActionFocused {
			moveField(-1)
		} else {
			visible := agentsModalVisibleEditorFieldIndexes(editor)
			if len(visible) > 0 && editor.Selected == visible[0] && len(editorActions) > 0 {
				editor.ActionSelected = len(editorActions) - 1
				moveAction(0)
			} else {
				moveField(-1)
			}
		}
		return
	case p.keybinds.Match(ev, KeybindEditorMoveUp):
		if editor.ActionFocused {
			if editor.ActionSelected == 0 {
				visible := agentsModalVisibleEditorFieldIndexes(editor)
				if len(visible) > 0 {
					editor.Selected = visible[len(visible)-1]
					editor.ActionFocused = false
					p.agentsModal.DetailScroll = 0
				}
			} else {
				moveAction(-1)
			}
			return
		}
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(editor, field, -1)
				return
			}
		}
		moveField(-1)
		return
	case p.keybinds.Match(ev, KeybindEditorMoveDown):
		if editor.ActionFocused {
			moveAction(1)
			return
		}
		if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(editor, field, 1)
				return
			}
		}
		visible := agentsModalVisibleEditorFieldIndexes(editor)
		if len(visible) > 0 && editor.Selected == visible[len(visible)-1] && len(editorActions) > 0 {
			moveAction(0)
		} else {
			moveField(1)
		}
		return
	case p.keybinds.Match(ev, KeybindEditorMoveLeft):
		if editor.ActionFocused {
			moveAction(-1)
		} else if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(editor, field, -1)
			}
		} else {
			moveField(-1)
		}
		return
	case p.keybinds.Match(ev, KeybindEditorMoveRight):
		if editor.ActionFocused {
			moveAction(1)
		} else if editor.Editing {
			field := selectedField()
			if field != nil && len(field.Options) > 0 {
				p.cycleAgentsModalEditorOption(editor, field, 1)
			}
		} else {
			moveField(1)
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
			p.beginAgentsModalEditorFieldEdit(editor, editor.Selected)
			return
		}
		field.Value = ""
		return
	case p.keybinds.Match(ev, KeybindEditorSubmit):
		if editor.ActionFocused {
			if len(editorActions) > 0 && editor.ActionSelected >= 0 && editor.ActionSelected < len(editorActions) {
				p.handleAgentsModalEditorAction(editorActions[editor.ActionSelected].Action)
			}
			return
		}
		if !editor.Editing {
			p.beginAgentsModalEditorFieldEdit(editor, editor.Selected)
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
		field := selectedField()
		if field != nil && len(field.Options) > 0 && editor.EditingOptionSet {
			field.Value = editor.EditingOption
		}
		editor.Editing = false
		editor.EditingOption = ""
		editor.EditingOptionSet = false
		if field != nil && field.Key == "model_profile" {
			if field.Value == agentsModalCreateProfileOption {
				p.openAgentsModalCreateModelProfileEditor()
				return
			}
			p.applyAgentsModalModelProfile(field.Value)
			return
		}
		p.syncAgentsModalEditorDependentOptions(editor)
		if field != nil {
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
			p.selectAgentsModalEditorOptionByRune(editor, field, r)
			return
		}
		if unicode.IsPrint(r) {
			field.Value += string(r)
		}
	}
}

func (p *HomePage) agentsModalEditorModelProfileInput(editor *agentsModalEditor) (client.ModelProfileInput, bool) {
	if p == nil || editor == nil || editor.Mode != "model" {
		return client.ModelProfileInput{}, false
	}
	get := func(key string) string {
		return agentsModalEditorFieldValue(editor, key)
	}
	name := "Temporary/customized"
	if field := p.findAgentsModalEditorField(editor, "profile_name"); field != nil {
		name = strings.TrimSpace(field.Value)
	} else {
		profileID := strings.TrimSpace(agentsModalEditorFieldValue(editor, "model_profile"))
		for _, profile := range p.agentsModal.ModelProfiles {
			if strings.TrimSpace(profile.ProfileID) == profileID && strings.TrimSpace(profile.Name) != "" {
				name = strings.TrimSpace(profile.Name)
				break
			}
		}
	}
	selection := func(prefix string) (*client.ModelProfileSelection, bool) {
		provider := p.normalizeAgentsModalProviderValue(get(prefix + "provider"))
		model := p.normalizeAgentsModalModelValue(provider, get(prefix+"model"))
		thinking := normalizeAgentsModalThinkingValue(get(prefix+"thinking"), p.agentsModalThinkingOptions(provider, model), "")
		if provider == "" || model == "" || thinking == "" {
			return nil, false
		}
		return &client.ModelProfileSelection{
			Provider: provider, Model: model, Thinking: thinking,
			ServiceTier: strings.ToLower(strings.TrimSpace(get(prefix + "service_tier"))),
		}, true
	}
	input := client.ModelProfileInput{Name: name, ModelMode: agentsModalEditorModelMode(editor)}
	if input.ModelMode == "split" {
		plan, planOK := selection("plan_")
		auto, autoOK := selection("auto_")
		if !planOK || !autoOK {
			p.agentsModal.Error = "provider, model, and thinking are required for both Plan and Action"
			return client.ModelProfileInput{}, false
		}
		input.Plan, input.Auto = plan, auto
	} else {
		input.ModelMode = "single"
		single, ok := selection("")
		if !ok {
			p.agentsModal.Error = "provider, model, and thinking are required"
			return client.ModelProfileInput{}, false
		}
		input.Single = single
	}
	return input, true
}

func (p *HomePage) handleAgentsModalEditorAction(action string) {
	editor := p.agentsModal.Editor
	if editor == nil {
		return
	}
	if editor.Editing {
		if editor.Selected >= 0 && editor.Selected < len(editor.Fields) {
			field := &editor.Fields[editor.Selected]
			if len(field.Options) > 0 && editor.EditingOptionSet {
				field.Value = editor.EditingOption
			}
		}
		editor.Editing = false
		editor.EditingOption = ""
		editor.EditingOptionSet = false
		p.syncAgentsModalEditorDependentOptions(editor)
	}
	p.agentsModal.Error = ""
	switch action {
	case "cancel":
		if agentsModalEditorHasPendingChanges(editor) {
			p.openAgentsModalUnsavedConfirm()
			return
		}
		p.agentsModal.Focus = agentsModalFocusProfiles
		p.agentsModal.Status = "back to agent list"
	case "temporary":
		input, ok := p.agentsModalEditorModelProfileInput(editor)
		if !ok {
			return
		}
		p.enqueueAgentsModalAction(AgentsModalAction{
			Kind: AgentsModalActionApplyTemporary, ModelProfile: &input,
			StatusHint: "Applying model choices to this chat...",
		})
	case "save-copy":
		profileID := strings.TrimSpace(agentsModalEditorFieldValue(editor, "model_profile"))
		if !p.agentsModalHasModelProfile(profileID) {
			p.agentsModal.Error = "select a saved profile before saving a copy"
			return
		}
		name := "Profile copy"
		for _, profile := range p.agentsModal.ModelProfiles {
			if strings.TrimSpace(profile.ProfileID) == profileID {
				name = nonEmpty(strings.TrimSpace(profile.Name), "Profile") + " copy"
				break
			}
		}
		editor.CreateModelProfile = true
		fields := make([]agentsModalEditorField, 0, len(editor.Fields))
		fields = append(fields, agentsModalEditorField{Key: "profile_name", Label: "Profile name", Value: name, Placeholder: "My model profile"})
		for _, field := range editor.Fields {
			if field.Key == "model_profile" || field.Key == "default_session_mode" {
				continue
			}
			fields = append(fields, field)
		}
		editor.Fields = fields
		editor.InitialFields = cloneAgentsModalEditorFields(editor.Fields)
		editor.Selected = 0
		editor.Editing = true
		editor.ActionFocused = false
		p.agentsModal.DetailScroll = 0
		p.agentsModal.Status = "Name the new profile, then choose Create profile and apply"
	case "save":
		if editor.AgentSettingsLocked {
			p.submitAgentsModalEditor()
			return
		}
		if editor.CreateModelProfile {
			input, ok := p.agentsModalEditorModelProfileInput(editor)
			if !ok {
				return
			}
			if strings.TrimSpace(input.Name) == "" {
				p.agentsModal.Error = "model profile name is required"
				return
			}
			p.enqueueAgentsModalAction(AgentsModalAction{
				Kind: AgentsModalActionCreateModelProfile, ModelProfile: &input, ApplyModelProfile: true,
				StatusHint: "Creating and applying model profile...",
			})
			return
		}
		input, ok := p.agentsModalEditorModelProfileInput(editor)
		if !ok {
			return
		}
		profileID := strings.TrimSpace(agentsModalEditorFieldValue(editor, "model_profile"))
		if !p.agentsModalHasModelProfile(profileID) {
			p.agentsModal.Error = "select a saved profile before saving changes"
			return
		}
		p.enqueueAgentsModalAction(AgentsModalAction{
			Kind: AgentsModalActionUpdateModelProfile, ModelProfileID: profileID, ModelProfile: &input, ApplyModelProfile: true,
			StatusHint: "Saving and applying model profile...",
		})
	}
}

func (p *HomePage) queueAgentsModalProfileSwitch(profileID string) {
	profileID = strings.TrimSpace(profileID)
	if p == nil || profileID == "" {
		return
	}
	if !p.agentsModalHasModelProfile(profileID) {
		p.agentsModal.Error = "selected profile is no longer available"
		return
	}
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind:           AgentsModalActionSwitchProfile,
		ModelProfileID: profileID,
		StatusHint:     "switching profile...",
	})
}

func (p *HomePage) queueAgentsModalProfileDefault(profileID string) {
	profileID = strings.TrimSpace(profileID)
	if p == nil || profileID == "" {
		return
	}
	if profileID == strings.TrimSpace(p.agentsModal.DefaultModelProfileID) {
		p.agentsModal.Status = "profile is already the account default"
		return
	}
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind:           AgentsModalActionSetProfileDefault,
		ModelProfileID: profileID,
		StatusHint:     "setting account default profile...",
	})
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

	if editor.Mode == "utility-ai" || editor.Mode == "utility-ai-overwrite" {
		provider := p.normalizeAgentsModalProviderValue(get("provider"))
		model := p.normalizeAgentsModalModelValue(provider, get("model"))
		if provider == "" || model == "" {
			p.agentsModal.Error = "provider and model are required for Utility AI"
			return
		}
		thinkingOptions := p.agentsModalThinkingOptions(provider, model)
		thinkingFallback := ""
		if record, ok := p.agentsModalCatalogRecord(provider, model); ok {
			thinkingFallback = record.DefaultThinking
		}
		thinking := normalizeAgentsModalThinkingValue(get("thinking"), thinkingOptions, thinkingFallback)
		p.agentsModal.Editor = nil
		overwriteExplicit := strings.EqualFold(editor.Mode, "utility-ai-overwrite") || agentsModalUtilityAIOverwriteChoice(get("scope"))
		p.enqueueAgentsModalAction(AgentsModalAction{
			Kind: AgentsModalActionSetUtilityAI,
			UtilityAI: &AgentsModalUtilityAI{
				Provider:          provider,
				Model:             model,
				Thinking:          thinking,
				OverwriteExplicit: overwriteExplicit,
			},
			StatusHint: agentsModalUtilityAIStatusHint(provider, model, overwriteExplicit),
		})
		return
	}

	if editor.CreateModelProfile {
		p.submitAgentsModalModelProfileCreate(editor, get)
		return
	}

	if editor.Mode == "model" {
		profile, ok := p.findAgentsModalProfileByName(editor.TargetName)
		if !ok {
			p.agentsModal.Error = "selected agent is no longer available"
			return
		}
		modelMode := strings.ToLower(strings.TrimSpace(get("model_mode")))
		if editor.AgentSettingsLocked {
			modelMode = "single"
		}
		if modelMode != "split" {
			modelMode = "single"
		}
		sessionMode := strings.ToLower(strings.TrimSpace(get("default_session_mode")))
		if editor.AgentSettingsLocked {
			sessionMode = strings.ToLower(strings.TrimSpace(profile.DefaultSessionMode))
		}
		if sessionMode != "plan" {
			sessionMode = "auto"
		}
		upsert := AgentsModalUpsert{Name: profile.Name, DefaultSessionMode: sessionMode, ModelMode: modelMode}
		if modelMode == "split" {
			upsert.Provider = ""
			upsert.Model = ""
			upsert.Thinking = ""
			upsert.PlanProvider = p.normalizeAgentsModalProviderValue(get("plan_provider"))
			upsert.PlanModel = p.normalizeAgentsModalModelValue(upsert.PlanProvider, get("plan_model"))
			upsert.PlanThinking = normalizeAgentsModalThinkingValue(get("plan_thinking"), p.agentsModalThinkingOptions(upsert.PlanProvider, upsert.PlanModel), "")
			upsert.PlanServiceTier = strings.ToLower(get("plan_service_tier"))
			upsert.AutoProvider = p.normalizeAgentsModalProviderValue(get("auto_provider"))
			upsert.AutoModel = p.normalizeAgentsModalModelValue(upsert.AutoProvider, get("auto_model"))
			upsert.AutoThinking = normalizeAgentsModalThinkingValue(get("auto_thinking"), p.agentsModalThinkingOptions(upsert.AutoProvider, upsert.AutoModel), "")
			upsert.AutoServiceTier = strings.ToLower(get("auto_service_tier"))
			if upsert.PlanProvider == "" || upsert.PlanModel == "" || upsert.AutoProvider == "" || upsert.AutoModel == "" {
				p.agentsModal.Error = "provider and model are required for both Plan and Action"
				return
			}
		} else {
			upsert.PlanProvider = ""
			upsert.PlanModel = ""
			upsert.PlanThinking = ""
			upsert.PlanServiceTier = ""
			upsert.AutoProvider = ""
			upsert.AutoModel = ""
			upsert.AutoThinking = ""
			upsert.Provider = p.normalizeAgentsModalProviderValue(get("provider"))
			upsert.Model = p.normalizeAgentsModalModelValue(upsert.Provider, get("model"))
			upsert.Thinking = normalizeAgentsModalThinkingValue(get("thinking"), p.agentsModalThinkingOptions(upsert.Provider, upsert.Model), "")
			upsert.ServiceTier = strings.ToLower(get("service_tier"))
			upsert.AutoServiceTier = upsert.ServiceTier
			if upsert.Provider == "" || upsert.Model == "" {
				p.agentsModal.Error = "provider and model are required"
				return
			}
		}
		modelProfileID, switchProfile := p.agentsModalEditorProfileSwitch(editor)
		editor.InitialFields = cloneAgentsModalEditorFields(editor.Fields)
		action := AgentsModalAction{Kind: AgentsModalActionUpsert, Upsert: &upsert, StatusHint: fmt.Sprintf("Saving setup for %s...", upsert.Name)}
		if switchProfile {
			action.ModelProfileID = modelProfileID
			action.StatusHint = fmt.Sprintf("Saving setup for %s and switching profile...", upsert.Name)
		}
		p.enqueueAgentsModalAction(action)
		return
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
	thinking := normalizeAgentsModalThinkingValue(get("thinking"), thinkingOptions, "")

	upsert := AgentsModalUpsert{
		Mode:            mode,
		Description:     strings.TrimSpace(get("description")),
		Provider:        provider,
		Model:           model,
		Thinking:        thinking,
		ServiceTier:     get("service_tier"),
		ModelMode:       get("model_mode"),
		PlanProvider:    get("plan_provider"),
		PlanModel:       get("plan_model"),
		PlanThinking:    get("plan_thinking"),
		PlanServiceTier: get("plan_service_tier"),
		AutoProvider:    get("auto_provider"),
		AutoModel:       get("auto_model"),
		AutoThinking:    get("auto_thinking"),
		AutoServiceTier: get("auto_service_tier"),
		Prompt:          strings.TrimSpace(get("prompt")),
		Enabled:         &enabled,
	}

	upsert.Name = strings.ToLower(strings.TrimSpace(editor.TargetName))
	if upsert.Name == "" {
		p.agentsModal.Error = "target agent is missing"
		return
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

func (p *HomePage) submitAgentsModalModelProfileCreate(editor *agentsModalEditor, get func(string) string) {
	name := strings.TrimSpace(get("profile_name"))
	if name == "" {
		p.agentsModal.Error = "model profile name is required"
		return
	}
	modelMode := strings.ToLower(strings.TrimSpace(get("model_mode")))
	input := client.ModelProfileInput{Name: name, ModelMode: modelMode}
	selection := func(prefix string) (*client.ModelProfileSelection, bool) {
		provider := p.normalizeAgentsModalProviderValue(get(prefix + "provider"))
		model := p.normalizeAgentsModalModelValue(provider, get(prefix+"model"))
		thinking := normalizeAgentsModalThinkingValue(get(prefix+"thinking"), p.agentsModalThinkingOptions(provider, model), "")
		if provider == "" || model == "" || thinking == "" {
			return nil, false
		}
		return &client.ModelProfileSelection{
			Provider: provider, Model: model, Thinking: thinking,
			ServiceTier: strings.ToLower(strings.TrimSpace(get(prefix + "service_tier"))),
		}, true
	}
	if modelMode == "split" {
		plan, planOK := selection("plan_")
		auto, autoOK := selection("auto_")
		if !planOK || !autoOK {
			p.agentsModal.Error = "provider, model, and thinking are required for both Plan and Action"
			return
		}
		input.Plan, input.Auto = plan, auto
	} else {
		input.ModelMode = "single"
		single, ok := selection("")
		if !ok {
			p.agentsModal.Error = "provider, model, and thinking are required"
			return
		}
		input.Single = single
	}
	editor.InitialFields = cloneAgentsModalEditorFields(editor.Fields)
	p.enqueueAgentsModalAction(AgentsModalAction{
		Kind: AgentsModalActionCreateModelProfile, ModelProfile: &input,
		StatusHint: "Creating saved model profile " + name + "...",
	})
}

func agentsModalUtilityAIOverwriteChoice(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(value, "clear") || strings.Contains(value, "override") || value == "all"
}

func agentsModalUtilityAIStatusHint(provider, model string, overwriteExplicit bool) string {
	if overwriteExplicit {
		return fmt.Sprintf("Clearing Utility AI overrides and setting %s/%s...", provider, modelpkg.DisplayModelName(provider, model))
	}
	return fmt.Sprintf("Setting Utility AI %s/%s for blank utility agents...", provider, modelpkg.DisplayModelName(provider, model))
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
	case "x-high":
		return "xhigh"
	default:
		return value
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
	nextProfile := matches[pos]
	if agentsModalEditorHasPendingChanges(p.agentsModal.Editor) && !strings.EqualFold(p.agentsModal.Editor.TargetName, p.agentsModal.Profiles[nextProfile].Name) {
		p.agentsModal.Error = "save or discard current model changes before switching agents"
		return
	}
	p.agentsModal.SelectedProfile = nextProfile
	p.agentsModal.DetailScroll = 0
	p.agentsModal.Editor = nil
	p.agentsModal.Focus = agentsModalFocusProfiles
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

func (p *HomePage) findAgentsModalProfileByName(name string) (AgentModalProfile, bool) {
	idx := p.findAgentsModalIndexByName(name)
	if idx < 0 || idx >= len(p.agentsModal.Profiles) {
		return AgentModalProfile{}, false
	}
	return p.agentsModal.Profiles[idx], true
}

func (p *HomePage) agentsModalUtilityAgentsLabel() string {
	agents := dedupeAgentsModalOptions(p.agentsModal.UtilityBaselineAgents)
	if len(agents) == 0 && len(p.agentsModal.CustomUtilityAgents) > 0 {
		return "none"
	}
	if len(agents) == 0 {
		agents = dedupeAgentsModalOptions(p.agentsModal.UtilityAgents)
	}
	if len(agents) == 0 {
		agents = []string{"finder", "memory"}
	}
	return strings.Join(agents, ", ")
}

func (p *HomePage) agentsModalCustomUtilityAgentsLabel() string {
	agents := dedupeAgentsModalOptions(p.agentsModal.CustomUtilityAgents)
	return strings.Join(agents, ", ")
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
	p.agentsModalTargets = p.agentsModalTargets[:0]
	if !p.agentsModal.Visible {
		return
	}
	if p.agentsModal.Screen == agentsV2ScreenEditor {
		p.drawAgentsV2EditorScreen(s)
		return
	}
	p.drawAgentsV2ListScreen(s)
}

func (p *HomePage) agentsV2Rect(s tcell.Screen) Rect {
	w, h := s.Size()
	modalW := w - 8
	if modalW > 112 {
		modalW = 112
	}
	if modalW < 64 {
		modalW = w - 2
	}
	modalH := h - 6
	if modalH > 34 {
		modalH = 34
	}
	if modalH < 18 {
		modalH = h - 2
	}
	return Rect{X: maxInt(1, (w-modalW)/2), Y: maxInt(1, (h-modalH)/2), W: modalW, H: modalH}
}

func (p *HomePage) drawAgentsV2ListScreen(s tcell.Screen) {
	rect := p.agentsV2Rect(s)
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	title := "Agents"
	if p.agentsModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)
	status := strings.TrimSpace(p.agentsModal.Status)
	style := p.theme.TextMuted
	if errText := strings.TrimSpace(p.agentsModal.Error); errText != "" {
		status, style = errText, p.theme.Error
	}
	if status == "" {
		status = "Choose an agent to configure"
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, style, clampEllipsis(status, rect.W-4))
	p.drawAgentsV2ListRows(s, Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6})
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, "↑/↓ focus • Enter opens editor • click a row • / search • r refresh • Esc close")
	DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "Each row shows the agent's configured model profile and resolved model")
}

func (p *HomePage) drawAgentsV2ListRows(s tcell.Screen, rect Rect) {
	DrawBox(s, rect, p.theme.Border)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, "Agent                                      Model profile / model")
	indexes := groupedAgentsModalIndexes(p.agentsFilteredIndexes(), p.agentsModal.Profiles)
	contentW, contentH := rect.W-4, rect.H-2
	if contentW <= 0 || contentH <= 0 {
		return
	}
	selectedPos := indexInList(indexes, p.agentsModal.SelectedProfile)
	if selectedPos < 0 {
		selectedPos = 0
	}
	visibleRows := maxInt(1, contentH/2)
	if p.agentsModal.ListScroll > selectedPos {
		p.agentsModal.ListScroll = selectedPos
	}
	if p.agentsModal.ListScroll+visibleRows-1 < selectedPos {
		p.agentsModal.ListScroll = selectedPos - visibleRows + 1
	}
	maxScroll := maxInt(0, len(indexes)-visibleRows)
	p.agentsModal.ListScroll = minInt(maxInt(0, p.agentsModal.ListScroll), maxScroll)
	for row := 0; row < visibleRows; row++ {
		pos := p.agentsModal.ListScroll + row
		if pos >= len(indexes) {
			break
		}
		idx := indexes[pos]
		profile := p.agentsModal.Profiles[idx]
		selected := idx == p.agentsModal.SelectedProfile
		prefix, nameStyle, detailStyle := "  ", p.theme.Text, p.theme.TextMuted
		if selected {
			prefix, nameStyle, detailStyle = "> ", p.theme.Accent.Bold(true), p.theme.Text
		}
		name := nonEmpty(agentsModalDisplayName(profile.Name), "-")
		if strings.EqualFold(profile.Name, p.agentsModal.ActivePrimary) {
			name += " [active]"
		}
		profileLabel := p.agentsV2ProfileLabel(profile)
		nameW := minInt(maxInt(18, contentW/3), maxInt(18, contentW-20))
		firstLine := prefix + padAgentsV2Column(name, nameW) + "  Profile: " + profileLabel
		modelSummary := strings.Join(p.agentsModalModelBehaviorLines(profile), " | ")
		secondLine := "    Model: " + modelSummary
		y := rect.Y + 1 + row*2
		DrawText(s, rect.X+2, y, contentW, nameStyle, clampEllipsis(firstLine, contentW))
		DrawText(s, rect.X+2, y+1, contentW, detailStyle, clampEllipsis(secondLine, contentW))
		p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: y, W: contentW, H: minInt(2, rect.Y+rect.H-1-y)}, "agents-profile", idx, "")
	}
}

func padAgentsV2Column(value string, width int) string {
	value = clampEllipsis(value, width)
	if missing := width - utf8.RuneCountInString(value); missing > 0 {
		value += strings.Repeat(" ", missing)
	}
	return value
}

func (p *HomePage) agentsV2ProfileLabel(agent AgentModalProfile) string {
	profileID := strings.TrimSpace(p.agentsModal.SelectedModelProfileID)
	if !strings.EqualFold(strings.TrimSpace(agent.Name), "swarm") {
		profileID = ""
	}
	if profileID == "" {
		for _, candidate := range p.agentsModal.ModelProfiles {
			if candidate.Single == nil || !strings.EqualFold(strings.TrimSpace(agent.ModelMode), "single") {
				continue
			}
			if strings.EqualFold(candidate.Single.Provider, agent.Provider) && candidate.Single.Model == agent.Model {
				profileID = candidate.ProfileID
				break
			}
		}
	}
	if profileID == "" {
		return "Custom"
	}
	return p.agentsModalModelProfileLabel(profileID)
}

func (p *HomePage) drawAgentsV2EditorScreen(s tcell.Screen) {
	rect := p.agentsV2Rect(s)
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	profile, ok := p.selectedAgentsModalProfile()
	if !ok || p.agentsModal.Editor == nil {
		DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.Error, "Selected agent is unavailable")
		return
	}
	title := "Edit agent — " + agentsModalDisplayName(profile.Name)
	if p.agentsModal.Editor.Editing {
		title += " [editing]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)
	status, style := strings.TrimSpace(p.agentsModal.Status), p.theme.TextMuted
	if errText := strings.TrimSpace(p.agentsModal.Error); errText != "" {
		status, style = errText, p.theme.Error
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, style, clampEllipsis(status, rect.W-4))
	p.drawAgentsV2EditorBody(s, Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6})
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, "Enter edit/confirm • Tab fields/actions • arrows navigate • Esc returns to Agents")
	DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "This is the V2 agent editor; the list is a separate screen")
	if p.agentsModal.ConfirmUnsaved {
		p.drawAgentsModalUnsavedConfirm(s, rect)
	}
}

func (p *HomePage) drawAgentsV2EditorBody(s tcell.Screen, rect Rect) {
	p.drawAgentsModalDetailPane(s, rect)
}

func (p *HomePage) drawAgentsModalLegacy(s tcell.Screen) {
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

	title := "Agent Setup"
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
				status = "Changes ready • Tab to the completion buttons and choose how to continue"
			} else {
				status = "Enter edits a field • Tab moves through fields and completion buttons"
			}
		} else if p.agentsModal.Focus == agentsModalFocusDetails {
			status = fmt.Sprintf("Enter edits field • %s saves • Left or Esc returns to agents", saveLabel)
		} else {
			status = "Up/Down selects an agent • Enter or Right opens model settings"
		}
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(status, rect.W-4))

	filter := nonEmpty(strings.TrimSpace(p.agentsModal.FilterMode), "all")
	searchEdit := ""
	if p.agentsModal.Focus == agentsModalFocusSearch {
		searchEdit = " [edit]"
	}
	meta := fmt.Sprintf("Agents and model settings • filter: %s", filter)
	DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.TextMuted, clampEllipsis(meta, rect.W-4))
	searchLine := "search" + searchEdit + ": " + p.agentsModal.Search
	DrawText(s, rect.X+2, rect.Y+3, rect.W-4, p.theme.TextMuted, clampEllipsis(searchLine, rect.W-4))
	p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: rect.Y + 3, W: minInt(rect.W-4, maxInt(8, utf8.RuneCountInString(searchLine))), H: 1}, "agents-search", -1, "")

	bodyRect := Rect{X: rect.X + 1, Y: rect.Y + 4, W: rect.W - 2, H: rect.H - 7}
	if bodyRect.W < 28 || bodyRect.H < 8 {
		return
	}
	if bodyRect.W >= 70 {
		leftW := maxInt(28, bodyRect.W*38/100)
		left := Rect{X: bodyRect.X, Y: bodyRect.Y, W: leftW, H: bodyRect.H}
		right := Rect{X: bodyRect.X + leftW + 1, Y: bodyRect.Y, W: bodyRect.W - leftW - 1, H: bodyRect.H}
		p.drawAgentsModalListPane(s, left)
		p.drawAgentsModalDetailPane(s, right)
	} else if p.agentsModal.Focus == agentsModalFocusDetails {
		p.drawAgentsModalDetailPane(s, bodyRect)
	} else {
		p.drawAgentsModalListPane(s, bodyRect)
	}

	help := "↑/↓ select agent • Enter/Right model settings • r refresh • Esc close"
	if p.agentsModal.Focus == agentsModalFocusDetails {
		help = "Enter edit • Left/Esc agents • ↑/↓ fields • PgUp/PgDn scroll"
	}
	if editor := p.agentsModal.Editor; editor != nil {
		help = "Enter edit/commit • Tab fields/actions • arrows navigate • Esc close"
	}
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(help, rect.W-4))
	DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "Agents stay on the left; the selected agent's model settings stay on the right")
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
	p.registerAgentsModalTarget(Rect{X: saveX, Y: buttonY, W: utf8.RuneCountInString(saveLabel), H: 1}, "agents-unsaved-save", -1, "")
	if discardX < rect.X+rect.W-2 {
		DrawText(s, discardX, buttonY, rect.W-(discardX-rect.X)-2, discardStyle, discardLabel)
		p.registerAgentsModalTarget(Rect{X: discardX, Y: buttonY, W: utf8.RuneCountInString(discardLabel), H: 1}, "agents-unsaved-discard", -1, "")
	}

	hint := "Left/Right switch • Enter confirm • Esc cancel • y/n quick choice"
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, clampEllipsis(hint, rect.W-4))
}

func (p *HomePage) drawAgentsModalListPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Agents"
	if p.agentsModal.Focus == agentsModalFocusProfiles {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	DrawBox(s, rect, borderStyle)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, header)

	matches := groupedAgentsModalIndexes(p.agentsFilteredIndexes(), p.agentsModal.Profiles)

	contentW := rect.W - 4
	contentH := rect.H - 2
	if contentW <= 0 || contentH <= 0 {
		return
	}

	primaryAgents := make([]int, 0)
	subagents := make([]int, 0)
	otherAgents := make([]int, 0)
	for _, idx := range matches {
		switch strings.ToLower(strings.TrimSpace(p.agentsModal.Profiles[idx].Mode)) {
		case "primary":
			primaryAgents = append(primaryAgents, idx)
		case "subagent":
			subagents = append(subagents, idx)
		default:
			otherAgents = append(otherAgents, idx)
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

			nameLine := prefix + nonEmpty(agentsModalDisplayName(profile.Name), "-")
			if strings.EqualFold(profile.Name, p.agentsModal.ActivePrimary) {
				nameLine += "  [active]"
			}
			lines = append(lines, agentsModalRenderLine{Text: clampEllipsis(nameLine, contentW), Style: lineStyle, ProfileIdx: idx, ProfileTarget: true})

			behaviorLines := p.agentsModalModelBehaviorLines(profile)
			for _, behavior := range behaviorLines {
				for _, line := range wrapAgentsModalWithPrefix("    ", behavior, contentW) {
					lines = append(lines, agentsModalRenderLine{Text: line, Style: metaStyle, ProfileIdx: idx, ProfileTarget: true})
				}
			}
		}
	}

	appendSection("Agents", primaryAgents)
	appendSection("Subagents", subagents)
	appendSection("Other agents", otherAgents)

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
		line := lines[lineIdx]
		DrawText(s, rect.X+2, rowY, contentW, line.Style, clampEllipsis(line.Text, contentW))
		if line.ProfileTarget {
			p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: rowY, W: contentW, H: 1}, "agents-profile", line.ProfileIdx, "")
		}
		rowY++
	}
}

func agentsModalSessionModeLabel(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "plan") {
		return "Plan"
	}
	return "Action"
}

func agentsModalDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if isCompiledSingleModelSubagent(name) && (strings.EqualFold(name, "clone") || strings.EqualFold(name, "system-clone") || strings.EqualFold(name, "coder")) {
		return "Coder"
	}
	if strings.EqualFold(name, "system-finder") {
		return "Finder"
	}
	if strings.EqualFold(name, "system-designer") || strings.EqualFold(name, "designer") {
		return "Designer"
	}
	return name
}

func agentsModalFormatModelSelection(label, provider, model, thinking, tier string) string {
	selection := strings.Trim(strings.TrimSpace(provider)+"/"+modelpkg.DisplayModelName(provider, model), "/")
	if selection == "" {
		selection = "Default model"
	}
	parts := []string{label + ": " + selection}
	if thinking = strings.TrimSpace(thinking); thinking != "" {
		parts = append(parts, "thinking "+thinking)
	}
	if tier = strings.TrimSpace(tier); tier != "" {
		parts = append(parts, "priority "+tier)
	}
	return strings.Join(parts, " • ")
}

func (p *HomePage) agentsModalSelectedModelProfile() *client.ModelProfile {
	if p == nil {
		return nil
	}
	selectedID := strings.TrimSpace(p.agentsModal.SelectedModelProfileID)
	if selectedID == "" {
		selectedID = strings.TrimSpace(p.agentsModal.ActiveModelProfileID)
	}
	if selectedID == "" {
		selectedID = strings.TrimSpace(p.agentsModal.DefaultModelProfileID)
	}
	for i := range p.agentsModal.ModelProfiles {
		if strings.TrimSpace(p.agentsModal.ModelProfiles[i].ProfileID) == selectedID {
			return &p.agentsModal.ModelProfiles[i]
		}
	}
	return nil
}

func (p *HomePage) agentsModalModelBehaviorLines(profile AgentModalProfile) []string {
	format := agentsModalFormatModelSelection
	if strings.EqualFold(strings.TrimSpace(profile.ModelMode), "split") {
		return []string{
			format("Plan", profile.PlanProvider, profile.PlanModel, profile.PlanThinking, profile.PlanServiceTier),
			format("Action", profile.AutoProvider, profile.AutoModel, profile.AutoThinking, profile.AutoServiceTier),
		}
	}
	if strings.EqualFold(strings.TrimSpace(profile.Name), "swarm") {
		if saved := p.agentsModalSelectedModelProfile(); saved != nil {
			if strings.EqualFold(strings.TrimSpace(saved.ModelMode), "split") && saved.Plan != nil && saved.Auto != nil {
				return []string{
					format("Plan", saved.Plan.Provider, saved.Plan.Model, saved.Plan.Thinking, saved.Plan.ServiceTier),
					format("Action", saved.Auto.Provider, saved.Auto.Model, saved.Auto.Thinking, saved.Auto.ServiceTier),
				}
			}
			if saved.Single != nil {
				return []string{format("Single", saved.Single.Provider, saved.Single.Model, saved.Single.Thinking, saved.Single.ServiceTier)}
			}
		}
	}
	return []string{"Single model"}
}

func agentsModalEditorModelMode(editor *agentsModalEditor) string {
	if editor == nil {
		return "single"
	}
	for _, field := range editor.Fields {
		if field.Key == "model_mode" && strings.EqualFold(strings.TrimSpace(field.Value), "split") {
			return "split"
		}
	}
	return "single"
}

func agentsModalEditorFieldValue(editor *agentsModalEditor, key string) string {
	if editor == nil {
		return ""
	}
	for _, field := range editor.Fields {
		if field.Key == key {
			return strings.TrimSpace(field.Value)
		}
	}
	return ""
}

func agentsModalEditorFieldVisible(editor *agentsModalEditor, field agentsModalEditorField) bool {
	if editor == nil || editor.Mode != "model" {
		return true
	}
	if editor.CreateModelProfile && (field.Key == "model_profile" || field.Key == "default_session_mode") {
		return false
	}
	dependency := map[string]string{
		"model": "provider", "thinking": "model", "service_tier": "model",
		"plan_model": "plan_provider", "plan_thinking": "plan_model", "plan_service_tier": "plan_model",
		"auto_model": "auto_provider", "auto_thinking": "auto_model", "auto_service_tier": "auto_model",
	}[field.Key]
	if dependency != "" && agentsModalEditorFieldValue(editor, dependency) == "" {
		return false
	}
	if editor.AgentSettingsLocked && (field.Key == "default_session_mode" || field.Key == "model_mode") {
		return false
	}
	if editor.AgentSettingsLocked && field.Key == "model_profile" && len(field.Options) == 0 {
		return false
	}
	if editor.ModelReadOnly && field.Key != "model_profile" {
		return false
	}
	switch field.Key {
	case "provider", "model", "thinking", "service_tier":
		return agentsModalEditorModelMode(editor) == "single"
	case "plan_provider", "plan_model", "plan_thinking", "plan_service_tier", "auto_provider", "auto_model", "auto_thinking", "auto_service_tier":
		return agentsModalEditorModelMode(editor) == "split"
	default:
		return true
	}
}

func agentsModalVisibleEditorFieldIndexes(editor *agentsModalEditor) []int {
	if editor == nil {
		return nil
	}
	out := make([]int, 0, len(editor.Fields))
	for i, field := range editor.Fields {
		if agentsModalEditorFieldVisible(editor, field) {
			out = append(out, i)
		}
	}
	return out
}

func agentsModalEditorFieldGroup(key string) string {
	switch {
	case key == "model_profile" || key == "profile_name":
		return "profiles"
	case strings.HasPrefix(key, "plan_"):
		return "plan"
	case strings.HasPrefix(key, "auto_"):
		return "action"
	case key == "provider" || key == "model" || key == "thinking" || key == "service_tier":
		return "single"
	default:
		return "settings"
	}
}

type agentsModalEditorAction struct {
	Action string
	Label  string
}

func agentsModalEditorActions(editor *agentsModalEditor) []agentsModalEditorAction {
	if editor == nil {
		return nil
	}
	actions := []agentsModalEditorAction{{Action: "cancel", Label: "Cancel"}}
	if editor.Mode != "model" {
		return append(actions, agentsModalEditorAction{Action: "save", Label: "Save changes"})
	}
	if !editor.AgentSettingsLocked {
		actions = append(actions, agentsModalEditorAction{Action: "temporary", Label: "Continue for this chat only"})
		if !editor.CreateModelProfile && strings.TrimSpace(agentsModalEditorFieldValue(editor, "model_profile")) != "" {
			actions = append(actions, agentsModalEditorAction{Action: "save-copy", Label: "Save as new"})
		}
	}
	saveLabel := "Save and apply"
	if editor.CreateModelProfile {
		saveLabel = "Create profile and apply"
	} else if editor.AgentSettingsLocked {
		saveLabel = "Save model"
	}
	return append(actions, agentsModalEditorAction{Action: "save", Label: saveLabel})
}

func agentsModalEditorActionLines(p *HomePage, editor *agentsModalEditor, compact bool) []agentsModalRenderLine {
	if p == nil || editor == nil {
		return nil
	}
	lines := make([]agentsModalRenderLine, 0, len(agentsModalEditorActions(editor))+3)
	if !compact {
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
	}
	lines = append(lines, agentsModalRenderLine{Text: "Choose how to continue", Style: p.theme.Primary.Bold(true)})
	for i, action := range agentsModalEditorActions(editor) {
		style := p.theme.TextMuted
		prefix := "  "
		if editor.ActionFocused && editor.ActionSelected == i {
			style = p.theme.Accent.Bold(true)
			prefix = "> "
		}
		lines = append(lines, agentsModalRenderLine{Text: prefix + "[ " + action.Label + " ]", Style: style, EditorActionTarget: true, EditorAction: action.Action})
	}
	if !compact {
		lines = append(lines, agentsModalRenderLine{Text: "Tab reaches actions • arrows choose • Enter confirms", Style: p.theme.TextMuted})
	}
	return lines
}

func (p *HomePage) drawAgentsModalDetailPane(s tcell.Screen, rect Rect) {
	borderStyle := p.theme.Border
	header := "Agent + Model Settings"
	if p.agentsModal.Focus == agentsModalFocusDetails {
		borderStyle = p.theme.BorderActive
		header += " [focus]"
	}
	editor := p.agentsModal.Editor
	if editor != nil {
		header = "Model Settings"
		if p.agentsModal.Focus == agentsModalFocusDetails {
			header += " [focus]"
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
		modelMode := agentsModalEditorModelMode(editor)
		editorTitle := nonEmpty(agentsModalDisplayName(profile.Name), "Agent")
		if editor.CreateModelProfile {
			editorTitle = "New model profile"
		}
		lines = append(lines, agentsModalRenderLine{Text: editorTitle, Style: p.theme.Text.Bold(true)})
		if editor.AgentSettingsLocked {
			lockText := "Compiled system agent: agent identity, default session, and model policy are locked."
			lockText += " Only independent single-model choices are editable."
			lines = append(lines, agentsModalRenderLine{Text: lockText, Style: p.theme.TextMuted})
		} else {
			lines = append(lines, agentsModalRenderLine{Text: "Default session and model policy are agent settings. Model controls below edit the selected policy.", Style: p.theme.TextMuted})
		}
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		lastGroup := ""
		for i, field := range editor.Fields {
			if !agentsModalEditorFieldVisible(editor, field) {
				continue
			}
			group := agentsModalEditorFieldGroup(field.Key)
			if group != lastGroup {
				if lastGroup != "" {
					lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
				}
				switch group {
				case "profiles":
					heading := "YOUR PROFILE"
					if editor.CreateModelProfile {
						heading = "NEW SAVED PROFILE"
					}
					lines = append(lines, agentsModalRenderLine{Text: heading, Style: p.theme.Primary.Bold(true)})
				case "single":
					lines = append(lines, agentsModalRenderLine{Text: "Single model", Style: p.theme.Primary.Bold(true)})
				case "plan":
					lines = append(lines, agentsModalRenderLine{Text: "Plan", Style: p.theme.Primary.Bold(true)})
				case "action":
					lines = append(lines, agentsModalRenderLine{Text: "Action", Style: p.theme.Primary.Bold(true)})
				case "settings":
					lines = append(lines, agentsModalRenderLine{Text: "Agent settings", Style: p.theme.Primary.Bold(true)})
				}
				lastGroup = group
			}
			value := strings.TrimSpace(field.Value)
			if value == "" {
				value = nonEmpty(strings.TrimSpace(field.Placeholder), "standard")
			}
			value = agentsModalEditorFieldDisplayValue(field, value)
			if field.Key == "model_profile" {
				value = p.agentsModalModelProfileLabel(field.Value)
				if strings.TrimSpace(field.Value) == strings.TrimSpace(p.agentsModal.DefaultModelProfileID) {
					value += "  ★ default"
				} else {
					value += "  ☆"
				}
			}
			if field.Key == "default_session_mode" {
				value = agentsModalSessionModeLabel(field.Value)
			}
			if field.Key == "model_mode" {
				if modelMode == "split" {
					value = "Plan / Action"
				} else {
					value = "Single"
				}
			}
			style := p.theme.TextMuted
			prefix := "  "
			if i == editor.Selected {
				prefix = "> "
				style = p.theme.Accent.Bold(true)
				if editor.Editing {
					prefix = "* "
					style = p.theme.Primary.Bold(true)
				}
			}
			entryStart := len(lines)
			lineText := fmt.Sprintf("%s[%s: %s]", prefix, field.Label, value)
			for _, line := range Wrap(lineText, contentWidth) {
				renderLine := agentsModalRenderLine{Text: line, Style: style, FieldIdx: i, FieldTarget: true}
				if field.Key == "model_profile" && strings.TrimSpace(field.Value) != "" {
					renderLine.ProfileDefaultTarget = true
					renderLine.ModelProfileID = strings.TrimSpace(field.Value)
				}
				lines = append(lines, renderLine)
			}
			optionListStart := -1
			if i == editor.Selected && editor.Editing && len(field.Options) > 0 {
				choices := dedupeAgentsModalOptions(field.Options)
				current := normalizeAgentsModalOptionValue(field.Value, choices, "")
				if editor.EditingOptionSet {
					current = normalizeAgentsModalOptionValue(editor.EditingOption, choices, "")
				}
				optionListStart = len(lines)
				lines = append(lines, agentsModalRenderLine{Text: "    Select with arrows, then Enter:", Style: p.theme.TextMuted})
				for _, option := range choices {
					label := agentsModalEditorOptionDisplay(option)
					if field.Key == "model_profile" {
						label = p.agentsModalModelProfileLabel(option)
						if strings.TrimSpace(option) == strings.TrimSpace(p.agentsModal.DefaultModelProfileID) {
							label += "  ★ default"
						}
					} else if field.Key == "default_session_mode" {
						label = agentsModalSessionModeLabel(option)
					} else if field.Key == "model_mode" && strings.EqualFold(option, "split") {
						label = "Plan / Action"
					}
					choicePrefix := "      "
					choiceStyle := p.theme.TextMuted
					if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(current)) {
						choicePrefix = "    > "
						choiceStyle = p.theme.Text
					}
					for _, line := range wrapAgentsModalWithPrefix(choicePrefix, label, contentWidth) {
						lines = append(lines, agentsModalRenderLine{
							Text:               line,
							Style:              choiceStyle,
							FieldIdx:           i,
							OptionTarget:       true,
							OptionValue:        option,
							ModelProfileOption: field.Key == "model_profile",
						})
					}
				}
			}
			entryEnd := len(lines) - 1
			selectedEntry := i == editor.Selected
			if selectedEntry && !editor.ActionFocused {
				selectedStart = entryStart
				selectedEnd = entryEnd
				if optionListStart >= 0 {
					selectedStart = optionListStart
				}
			}
		}
		lines = append(lines, agentsModalRenderLine{Text: "", Style: p.theme.TextMuted})
		if len(p.agentsModal.ModelProfiles) > 0 {
			for _, direction := range agentsModalProfileDirectionLines(contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: direction, Style: p.theme.TextMuted})
			}
		}
	} else {
		lines = append(lines, agentsModalRenderLine{Text: nonEmpty(agentsModalDisplayName(profile.Name), "Agent"), Style: p.theme.Text.Bold(true)})
		for _, behavior := range p.agentsModalModelBehaviorLines(profile) {
			for _, wrapped := range Wrap(behavior, contentWidth) {
				lines = append(lines, agentsModalRenderLine{Text: wrapped, Style: p.theme.Text})
			}
		}
		lines = append(lines, agentsModalRenderLine{Text: "Enter to edit provider, model, thinking, and priority.", Style: p.theme.TextMuted})
	}

	stickyActions := []agentsModalRenderLine(nil)
	if editor != nil && agentsModalEditorHasPendingChanges(editor) {
		stickyActions = agentsModalEditorActionLines(p, editor, true)
		if len(stickyActions) > contentRows && len(stickyActions) > 0 {
			stickyActions = stickyActions[1:]
		}
	}
	bodyRows := maxInt(0, contentRows-len(stickyActions))
	if len(lines) == 0 && len(stickyActions) == 0 {
		return
	}

	maxScroll := maxInt(0, len(lines)-bodyRows)
	if p.agentsModal.DetailScroll > maxScroll {
		p.agentsModal.DetailScroll = maxScroll
	}
	if p.agentsModal.DetailScroll < 0 {
		p.agentsModal.DetailScroll = 0
	}
	if selectedStart >= 0 && bodyRows > 0 {
		if p.agentsModal.DetailScroll > selectedStart {
			p.agentsModal.DetailScroll = selectedStart
		}
		if p.agentsModal.DetailScroll+bodyRows-1 < selectedEnd {
			p.agentsModal.DetailScroll = selectedEnd - bodyRows + 1
			if p.agentsModal.DetailScroll < 0 {
				p.agentsModal.DetailScroll = 0
			}
		}
	}

	drawLine := func(line agentsModalRenderLine, lineY int) {
		DrawText(s, rect.X+2, lineY, contentWidth, line.Style, line.Text)
		if line.EditorActionTarget {
			p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: lineY, W: contentWidth, H: 1}, "agents-editor-action", -1, line.EditorAction)
		} else if line.OptionTarget {
			action := "agents-editor-option"
			if line.ModelProfileOption {
				action = "agents-model-profile-option"
			}
			p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: lineY, W: contentWidth, H: 1}, action, line.FieldIdx, line.OptionValue)
		} else if line.FieldTarget {
			p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: lineY, W: contentWidth, H: 1}, "agents-editor-field", line.FieldIdx, "")
			if line.ProfileDefaultTarget {
				starX := rect.X + 2 + minInt(maxInt(0, utf8.RuneCountInString(line.Text)-1), maxInt(0, contentWidth-1))
				p.registerAgentsModalTarget(Rect{X: starX, Y: lineY, W: 1, H: 1}, "agents-profile-default", -1, line.ModelProfileID)
			}
		} else if line.ProfileTarget {
			p.registerAgentsModalTarget(Rect{X: rect.X + 2, Y: lineY, W: contentWidth, H: 1}, "agents-detail", line.ProfileIdx, "")
		}
	}
	for i := 0; i < bodyRows; i++ {
		lineIdx := p.agentsModal.DetailScroll + i
		if lineIdx < 0 || lineIdx >= len(lines) {
			break
		}
		drawLine(lines[lineIdx], rowY+i)
	}
	for i, line := range stickyActions {
		drawLine(line, rowY+bodyRows+i)
	}
}

type agentsModalRenderLine struct {
	Text                 string
	Style                tcell.Style
	ProfileIdx           int
	ProfileTarget        bool
	FieldIdx             int
	FieldTarget          bool
	OptionTarget         bool
	OptionValue          string
	ModelProfileOption   bool
	EditorActionTarget   bool
	EditorAction         string
	ProfileDefaultTarget bool
	ModelProfileID       string
}

const agentsModalProfileDirections = "Profile directions: Enter selects a profile • D sets account default • after editing, use the completion buttons below"

func agentsModalProfileDirectionLines(width int) []string {
	return Wrap(agentsModalProfileDirections, width)
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

func agentsModalUtilityBaselineAgents(utilityAgents, customAgents []string) []string {
	if len(utilityAgents) == 0 {
		return nil
	}
	custom := make(map[string]struct{}, len(customAgents))
	for _, name := range customAgents {
		key := strings.ToLower(strings.TrimSpace(name))
		if key != "" {
			custom[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(utilityAgents))
	for _, name := range utilityAgents {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := custom[strings.ToLower(trimmed)]; ok {
			continue
		}
		out = append(out, trimmed)
	}
	return dedupeAgentsModalOptions(out)
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

func normalizeAgentsModalCatalogOptions(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeThinkingValue(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return dedupeAgentsModalOptions(out)
}

func normalizeAgentsModalServiceTierValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "standard" || value == "off" || value == "batch" {
		return ""
	}
	return value
}

func normalizeAgentsModalServiceTierOptions(values []string, mappings []client.ModelCatalogServiceTierMapping) []string {
	candidates := append([]string(nil), values...)
	for _, mapping := range mappings {
		candidates = append(candidates, mapping.Tier, mapping.SwarmSetting)
	}
	out := make([]string, 0, len(candidates))
	for _, value := range candidates {
		if value = normalizeAgentsModalServiceTierValue(value); value != "" {
			out = append(out, value)
		}
	}
	return dedupeAgentsModalOptions(out)
}

func (p *HomePage) agentsModalCatalogRecord(providerID, model string) (client.ModelCatalogRecord, bool) {
	if p == nil {
		return client.ModelCatalogRecord{}, false
	}
	key := agentsModalReasoningKey(providerID, model)
	if key == "" {
		return client.ModelCatalogRecord{}, false
	}
	record, ok := p.agentsModal.ModelCatalog[key]
	return record, ok
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
	out := make([]string, 0, len(p.agentsModal.Providers)+1)
	out = append(out, p.agentsModal.Providers...)
	if strings.TrimSpace(p.agentsModal.DefaultProvider) != "" {
		out = append(out, strings.TrimSpace(p.agentsModal.DefaultProvider))
	}
	return dedupeAgentsModalOptions(out)
}

func (p *HomePage) normalizeAgentsModalProviderValue(raw string) string {
	value := normalizeAgentsModalProviderID(raw)
	if matched, ok := findAgentsModalOption(p.agentsModalProviderOptions(), value); ok {
		return normalizeAgentsModalProviderID(matched)
	}
	return ""
}

func (p *HomePage) agentsModalModelOptionsForProvider(providerID string) []string {
	providerID = normalizeAgentsModalProviderID(providerID)
	out := make([]string, 0, len(p.agentsModal.ModelsByProvider[providerID])+1)
	if providerID != "" {
		out = append(out, p.agentsModal.ModelsByProvider[providerID]...)
		if fallback := p.agentsModal.defaultModelForProvider(providerID); fallback != "" {
			out = append(out, fallback)
		}
	}
	return dedupeAgentsModalOptions(out)
}

func (p *HomePage) agentsModalThinkingOptions(providerID, model string) []string {
	providerID = normalizeAgentsModalProviderID(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return nil
	}
	record, ok := p.agentsModalCatalogRecord(providerID, model)
	if !ok {
		return nil
	}
	return normalizeAgentsModalCatalogOptions(record.ThinkingOptions)
}

func (p *HomePage) normalizeAgentsModalModelValue(providerID, raw string) string {
	providerID = normalizeAgentsModalProviderID(providerID)
	if providerID == "" {
		return ""
	}
	if matched, ok := findAgentsModalOption(p.agentsModalModelOptionsForProvider(providerID), raw); ok {
		return matched
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
	if sessionMode := p.findAgentsModalEditorField(editor, "default_session_mode"); sessionMode != nil {
		sessionMode.Options = []string{"plan", "auto"}
		sessionMode.Value = normalizeAgentsModalOptionValue(strings.ToLower(strings.TrimSpace(sessionMode.Value)), sessionMode.Options, "auto")
	}
	if modelMode := p.findAgentsModalEditorField(editor, "model_mode"); modelMode != nil {
		modelMode.Options = []string{"single", "split"}
		modelMode.Value = normalizeAgentsModalOptionValue(strings.ToLower(strings.TrimSpace(modelMode.Value)), modelMode.Options, "single")
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
		thinkingFallback := ""
		if record, ok := p.agentsModalCatalogRecord(selectedProvider, modelValue); ok {
			thinkingFallback = record.DefaultThinking
		}
		thinking.Value = normalizeAgentsModalThinkingValue(thinking.Value, thinking.Options, thinkingFallback)
	}
	if serviceTier := p.findAgentsModalEditorField(editor, "service_tier"); serviceTier != nil {
		serviceTier.Options = p.agentsModalServiceTierOptions(selectedProvider, modelValue)
		serviceTier.Value = normalizeAgentsModalOptionValue(strings.ToLower(strings.TrimSpace(serviceTier.Value)), serviceTier.Options, "")
	}
	for _, prefix := range []string{"plan", "auto"} {
		provider := p.findAgentsModalEditorField(editor, prefix+"_provider")
		if provider == nil {
			continue
		}
		provider.Options = p.agentsModalProviderOptions()
		provider.Value = p.normalizeAgentsModalProviderValue(provider.Value)
		model := p.findAgentsModalEditorField(editor, prefix+"_model")
		if model != nil {
			model.Options = p.agentsModalModelOptionsForProvider(provider.Value)
			model.Value = p.normalizeAgentsModalModelValue(provider.Value, model.Value)
		}
		thinking := p.findAgentsModalEditorField(editor, prefix+"_thinking")
		modelValue := ""
		if model != nil {
			modelValue = model.Value
		}
		if thinking != nil {
			thinking.Options = p.agentsModalThinkingOptions(provider.Value, modelValue)
			thinkingFallback := ""
			if record, ok := p.agentsModalCatalogRecord(provider.Value, modelValue); ok {
				thinkingFallback = record.DefaultThinking
			}
			thinking.Value = normalizeAgentsModalThinkingValue(thinking.Value, thinking.Options, thinkingFallback)
		}
		if serviceTier := p.findAgentsModalEditorField(editor, prefix+"_service_tier"); serviceTier != nil {
			serviceTier.Options = p.agentsModalServiceTierOptions(provider.Value, modelValue)
			serviceTier.Value = normalizeAgentsModalOptionValue(strings.ToLower(strings.TrimSpace(serviceTier.Value)), serviceTier.Options, "")
		}
	}
	if scope := p.findAgentsModalEditorField(editor, "scope"); scope != nil {
		scope.Options = dedupeAgentsModalOptions(nonEmptySlice(scope.Options, []string{"blank only", "clear overrides"}))
		fallback := "blank only"
		if strings.EqualFold(editor.Mode, "utility-ai-overwrite") {
			fallback = "clear overrides"
		}
		scope.Value = normalizeAgentsModalOptionValue(scope.Value, scope.Options, fallback)
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

func (p *HomePage) beginAgentsModalEditorFieldEdit(editor *agentsModalEditor, fieldIndex int) {
	if editor == nil || fieldIndex < 0 || fieldIndex >= len(editor.Fields) {
		return
	}
	editor.Selected = fieldIndex
	editor.Editing = true
	editor.EditingOption = ""
	editor.EditingOptionSet = false
	field := &editor.Fields[fieldIndex]
	options := dedupeAgentsModalOptions(field.Options)
	if len(options) == 0 {
		return
	}
	field.Options = options
	editor.EditingOption = normalizeAgentsModalOptionValue(field.Value, options, "")
	editor.EditingOptionSet = true
}

func (p *HomePage) cycleAgentsModalEditorOption(editor *agentsModalEditor, field *agentsModalEditorField, delta int) {
	if editor == nil || field == nil || delta == 0 {
		return
	}
	options := dedupeAgentsModalOptions(field.Options)
	if len(options) == 0 {
		return
	}
	field.Options = options
	current := field.Value
	if editor.EditingOptionSet {
		current = editor.EditingOption
	}
	idx := findAgentsModalOptionIndex(options, current)
	if idx < 0 {
		idx = 0
	}
	idx = (idx + delta + len(options)) % len(options)
	editor.EditingOption = options[idx]
	editor.EditingOptionSet = true
}

func (p *HomePage) selectAgentsModalEditorOptionByRune(editor *agentsModalEditor, field *agentsModalEditorField, r rune) {
	if editor == nil || field == nil || !unicode.IsPrint(r) {
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
			editor.EditingOption = option
			editor.EditingOptionSet = true
			return
		}
	}
}

func (p *HomePage) agentsModalModelProfileLabel(profileID string) string {
	profileID = strings.TrimSpace(profileID)
	if profileID == agentsModalCreateProfileOption {
		return "Create new profile"
	}
	if p == nil || profileID == "" {
		return "No saved profile"
	}
	for _, profile := range p.agentsModal.ModelProfiles {
		if strings.TrimSpace(profile.ProfileID) != profileID {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			name = profileID
		}
		return name + " · " + modelProfileSummary(profile, p.sessionMode)
	}
	return profileID
}

func (p *HomePage) applyAgentsModalModelProfile(profileID string) bool {
	profileID = strings.TrimSpace(profileID)
	if p == nil || profileID == "" {
		return false
	}
	var selected *client.ModelProfile
	for i := range p.agentsModal.ModelProfiles {
		if strings.TrimSpace(p.agentsModal.ModelProfiles[i].ProfileID) == profileID {
			selected = &p.agentsModal.ModelProfiles[i]
			break
		}
	}
	if selected == nil {
		p.agentsModal.Error = "selected profile is no longer available"
		return false
	}
	editor := p.agentsModal.Editor
	if editor == nil {
		return false
	}
	p.agentsModal.SelectedModelProfileID = profileID
	if field := p.findAgentsModalEditorField(editor, "model_profile"); field != nil {
		field.Value = profileID
	}
	apply := func(prefix string, selection *client.ModelProfileSelection) {
		if selection == nil {
			return
		}
		set := func(key, value string) {
			if field := p.findAgentsModalEditorField(editor, prefix+key); field != nil {
				field.Value = strings.TrimSpace(value)
			}
		}
		set("provider", selection.Provider)
		set("model", selection.Model)
		set("thinking", selection.Thinking)
		set("service_tier", selection.ServiceTier)
	}
	mode := strings.ToLower(strings.TrimSpace(selected.ModelMode))
	if editor.AgentSettingsLocked {
		mode = "single"
	}
	if mode == "split" {
		if field := p.findAgentsModalEditorField(editor, "model_mode"); field != nil {
			field.Value = "split"
		}
		apply("plan_", selected.Plan)
		apply("auto_", selected.Auto)
	} else {
		if field := p.findAgentsModalEditorField(editor, "model_mode"); field != nil {
			field.Value = "single"
		}
		apply("", selected.Single)
		if selected.Single == nil {
			if normalizeHomeSessionMode(p.sessionMode) == "plan" {
				apply("", selected.Plan)
			} else {
				apply("", selected.Auto)
			}
		}
	}
	p.syncAgentsModalEditorDependentOptions(editor)
	p.agentsModal.Status = "loaded profile: " + p.agentsModalModelProfileLabel(profileID) + " • model changes are unsaved"
	p.agentsModal.Error = ""
	return true
}

func agentsModalEditorOptionDisplay(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return "off"
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
	case "provider", "plan_provider", "auto_provider":
		if value == "" {
			return "choose provider"
		}
	case "model", "plan_model", "auto_model":
		if value == "" {
			return "choose model"
		}
	case "thinking", "plan_thinking", "auto_thinking":
		if value == "" {
			return "choose thinking"
		}
	case "service_tier", "plan_service_tier", "auto_service_tier":
		if value == "" {
			return "off"
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

func agentsModalModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "choose model"
	}
	return model
}

func agentsModalThinkingLabel(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return "choose thinking"
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

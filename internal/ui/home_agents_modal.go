package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	modelpkg "swarm-refactor/swarmtui/internal/model"
)

var canonicalAgentModelNames = []string{"swarm", "finder", "coder", "designer", "compact", "router"}

const utilityAgentModelStart = 4

// AgentModalProfile is retained for auth-provider dependency warnings; /agents
// itself no longer treats these records as editable profiles.
type AgentModalProfile struct {
	Name     string
	Provider string
}

type AgentsModalData struct {
	Settings         client.AgentModelSettings
	Providers        []string
	ModelsByProvider map[string][]string
	ModelCatalog     map[string]client.ModelCatalogRecord
}

type AgentsModalActionKind string

const (
	AgentsModalActionRefresh AgentsModalActionKind = "refresh"
	AgentsModalActionSave    AgentsModalActionKind = "save"
)

type AgentsModalAction struct {
	Kind           AgentsModalActionKind
	Agent          string
	Swarm          *client.AgentModelSettingsSwarmPatch
	Assignment     *client.AgentModelAssignment
	ModelProfileID string
	StayOpen       bool
	StatusHint     string
}

type agentsModalFocus int

const (
	agentsModalFocusAgents agentsModalFocus = iota
	agentsModalFocusAssignments
	agentsModalFocusFields
	agentsModalFocusSave
)

type agentsModalState struct {
	Visible            bool
	Loading            bool
	Status             string
	Error              string
	Providers          []string
	ModelsByProvider   map[string][]string
	ModelCatalog       map[string]client.ModelCatalogRecord
	SelectedAgent      int
	Focus              agentsModalFocus
	SelectedAssignment int
	SelectedField      int
	EditingField       bool
	EditingOption      string
	EditingOptionSet   bool
	Drafts             map[string][]client.AgentModelAssignment
	InitialDrafts      map[string][]client.AgentModelAssignment
	EditingProfileID   string
}

func (p *HomePage) ShowAgentsModal() {
	p.ShowAgentsModalForProfile("")
}

func (p *HomePage) ShowAgentsModalForProfile(profileID string) {
	p.agentsModal = agentsModalState{Visible: true, Loading: true, SelectedAgent: 0, Focus: agentsModalFocusAgents, EditingProfileID: strings.TrimSpace(profileID)}
}

func (p *HomePage) HideAgentsModal() {
	p.agentsModal = agentsModalState{}
	p.agentsModalTargets = p.agentsModalTargets[:0]
	p.pendingAgentsAction = nil
}

func (p *HomePage) AgentsModalVisible() bool { return p != nil && p.agentsModal.Visible }

func (p *HomePage) SetAgentsModalLoading(loading bool) {
	p.agentsModal.Loading = loading
	p.agentsModalTargets = p.agentsModalTargets[:0]
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
	p.agentsModal.Providers = dedupeAgentsModalOptions(data.Providers)
	p.agentsModal.ModelsByProvider = make(map[string][]string, len(data.ModelsByProvider))
	for provider, models := range data.ModelsByProvider {
		provider = normalizeAgentsModalProviderID(provider)
		if provider != "" {
			p.agentsModal.ModelsByProvider[provider] = dedupeAgentsModalOptions(models)
		}
	}
	p.agentsModal.ModelCatalog = make(map[string]client.ModelCatalogRecord, len(data.ModelCatalog))
	for key, record := range data.ModelCatalog {
		key = strings.ToLower(strings.TrimSpace(key))
		record.Provider = normalizeAgentsModalProviderID(record.Provider)
		record.Model = strings.TrimSpace(record.Model)
		record.ThinkingOptions = normalizeAgentsModalOptionValues(record.ThinkingOptions, normalizeThinkingValue)
		record.DefaultThinking = normalizeThinkingValue(record.DefaultThinking)
		record.ServiceTiers = agentsModalPriorityOptions(record)
		record.DefaultServiceTier = normalizeAgentsModalPriorityValue(record.DefaultServiceTier)
		p.agentsModal.ModelCatalog[key] = record
	}
	p.agentsModal.Drafts = agentModelSettingsDrafts(data.Settings)
	if profileID := strings.TrimSpace(p.agentsModal.EditingProfileID); profileID != "" {
		for _, profile := range p.model.ModelProfiles {
			if strings.TrimSpace(profile.ProfileID) != profileID {
				continue
			}
			selection := profile.Single
			if selection == nil && strings.TrimSpace(profile.Provider) != "" && strings.TrimSpace(profile.Model) != "" {
				selection = &client.ModelProfileSelection{
					Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking,
					ServiceTier: profile.ServiceTier, ContextMode: profile.ContextMode,
				}
			}
			if selection != nil {
				p.agentsModal.Drafts["swarm"][0] = client.AgentModelAssignment{
					Provider: selection.Provider, Model: selection.Model, Thinking: selection.Thinking,
					ServiceTier: selection.ServiceTier, ContextMode: selection.ContextMode,
				}
			}
			break
		}
	}
	p.agentsModal.InitialDrafts = cloneAgentModelDrafts(p.agentsModal.Drafts)
	p.agentsModal.SelectedAgent = minInt(maxInt(0, p.agentsModal.SelectedAgent), len(canonicalAgentModelNames)-1)
	p.agentsModal.Focus = agentsModalFocusAgents
	p.agentsModal.SelectedAssignment = 0
	p.agentsModal.SelectedField = 0
	p.agentsModal.EditingField = false
	p.agentsModal.Loading = false
}

func agentModelSettingsDrafts(settings client.AgentModelSettings) map[string][]client.AgentModelAssignment {
	return map[string][]client.AgentModelAssignment{
		"swarm":    {settings.Swarm.Action, settings.Swarm.Plan},
		"compact":  {settings.SystemAgents.Compact},
		"finder":   {settings.SystemAgents.Finder},
		"coder":    {settings.SystemAgents.Coder},
		"designer": {settings.SystemAgents.Designer},
		"router":   {settings.SystemAgents.Router},
	}
}

func cloneAgentModelDrafts(source map[string][]client.AgentModelAssignment) map[string][]client.AgentModelAssignment {
	out := make(map[string][]client.AgentModelAssignment, len(source))
	for name, assignments := range source {
		out[name] = append([]client.AgentModelAssignment(nil), assignments...)
	}
	return out
}

func (p *HomePage) PopAgentsModalAction() (AgentsModalAction, bool) {
	if p == nil || p.pendingAgentsAction == nil {
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
		if p.agentsModalTargets[i].Rect.Contains(x, y) {
			return p.agentsModalTargets[i], true
		}
	}
	return clickTarget{}, false
}

func (p *HomePage) selectedAgentsModalName() string {
	if p == nil || p.agentsModal.SelectedAgent < 0 || p.agentsModal.SelectedAgent >= len(canonicalAgentModelNames) {
		return ""
	}
	return canonicalAgentModelNames[p.agentsModal.SelectedAgent]
}

func agentsModalDisplayName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "swarm" {
		return "Swarm"
	}
	if name == "" {
		return "Agent"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (p *HomePage) selectedAgentsModalAssignments() []client.AgentModelAssignment {
	return p.agentsModal.Drafts[p.selectedAgentsModalName()]
}

func (p *HomePage) selectedAgentsModalAssignment() *client.AgentModelAssignment {
	name := p.selectedAgentsModalName()
	assignments := p.agentsModal.Drafts[name]
	if p.agentsModal.SelectedAssignment < 0 || p.agentsModal.SelectedAssignment >= len(assignments) {
		return nil
	}
	return &p.agentsModal.Drafts[name][p.agentsModal.SelectedAssignment]
}

func agentsModalAssignmentLabel(agent string, index int) string {
	if agent != "swarm" {
		return "Model"
	}
	if index == 0 {
		return "Default model"
	}
	return "Plan model"
}

func agentsModalAssignmentModelLine(assignment client.AgentModelAssignment) string {
	selection := strings.Trim(strings.TrimSpace(assignment.Provider)+"/"+modelpkg.DisplayModelName(assignment.Provider, assignment.Model), "/")
	if selection == "" {
		return "not configured"
	}
	return selection
}

func agentsModalAssignmentSettingsLine(assignment client.AgentModelAssignment) string {
	thinking := emptyValue(strings.TrimSpace(assignment.Thinking), "off")
	priority := emptyValue(strings.TrimSpace(assignment.ServiceTier), "off")
	return fmt.Sprintf("%s • %s", thinking, priority)
}

func agentsModalAssignmentSummary(assignment client.AgentModelAssignment) string {
	parts := []string{agentsModalAssignmentModelLine(assignment)}
	if thinking := strings.TrimSpace(assignment.Thinking); thinking != "" {
		parts = append(parts, thinking)
	}
	if priority := strings.TrimSpace(assignment.ServiceTier); priority != "" {
		parts = append(parts, priority)
	}
	return strings.Join(parts, " • ")
}

func (p *HomePage) agentsModalHasChanges() bool {
	name := p.selectedAgentsModalName()
	current, initial := p.agentsModal.Drafts[name], p.agentsModal.InitialDrafts[name]
	if len(current) != len(initial) {
		return true
	}
	for i := range current {
		if current[i] != initial[i] {
			return true
		}
	}
	return false
}

func (p *HomePage) handleAgentsModalKey(ev *tcell.EventKey) {
	if p == nil || ev == nil {
		return
	}
	if p.agentsModal.Loading {
		return
	}
	if p.agentsModal.EditingField {
		p.handleAgentsModalFieldKey(ev)
		return
	}
	if p.keybinds.Match(ev, KeybindAgentsEditorSave) {
		p.submitAgentsModalSave(true)
		return
	}
	if ev.Key() == tcell.KeyRune && (ev.Rune() == 's' || ev.Rune() == 'S') {
		p.submitAgentsModalSave(false)
		return
	}
	if p.keybinds.Match(ev, KeybindModalClose) {
		p.HideAgentsModal()
		return
	}
	if ev.Key() == tcell.KeyRune && (ev.Rune() == 'r' || ev.Rune() == 'R') {
		p.enqueueAgentsModalAction(AgentsModalAction{Kind: AgentsModalActionRefresh, StatusHint: "Refreshing agent model settings..."})
		return
	}

	switch p.agentsModal.Focus {
	case agentsModalFocusAgents:
		switch ev.Key() {
		case tcell.KeyUp:
			p.agentsModal.SelectedAgent = (p.agentsModal.SelectedAgent - 1 + len(canonicalAgentModelNames)) % len(canonicalAgentModelNames)
		case tcell.KeyDown:
			p.agentsModal.SelectedAgent = (p.agentsModal.SelectedAgent + 1) % len(canonicalAgentModelNames)
		case tcell.KeyRight, tcell.KeyEnter:
			p.agentsModal.SelectedAssignment = 0
			p.agentsModal.Focus = agentsModalFocusAssignments
		}
	case agentsModalFocusAssignments:
		assignments := p.selectedAgentsModalAssignments()
		switch ev.Key() {
		case tcell.KeyUp:
			if len(assignments) > 0 {
				p.agentsModal.SelectedAssignment = (p.agentsModal.SelectedAssignment - 1 + len(assignments)) % len(assignments)
			}
		case tcell.KeyDown:
			if len(assignments) == 0 {
				p.agentsModal.Focus = agentsModalFocusSave
			} else if p.agentsModal.SelectedAssignment == len(assignments)-1 {
				p.agentsModal.Focus = agentsModalFocusSave
			} else {
				p.agentsModal.SelectedAssignment++
			}
		case tcell.KeyLeft:
			p.agentsModal.Focus = agentsModalFocusAgents
		case tcell.KeyRight, tcell.KeyEnter:
			p.agentsModal.SelectedField = 0
			p.agentsModal.Focus = agentsModalFocusFields
		}
	case agentsModalFocusFields:
		switch ev.Key() {
		case tcell.KeyUp:
			if p.agentsModal.SelectedField == 0 {
				p.agentsModal.Focus = agentsModalFocusAssignments
			} else {
				p.agentsModal.SelectedField--
			}
		case tcell.KeyDown:
			if p.agentsModal.SelectedField == 3 {
				p.agentsModal.Focus = agentsModalFocusSave
			} else {
				p.agentsModal.SelectedField++
			}
		case tcell.KeyLeft:
			p.agentsModal.Focus = agentsModalFocusAssignments
		case tcell.KeyEnter, tcell.KeyRight:
			p.beginAgentsModalFieldEdit()
		}
	case agentsModalFocusSave:
		switch ev.Key() {
		case tcell.KeyUp:
			p.agentsModal.Focus = agentsModalFocusFields
			p.agentsModal.SelectedField = 3
		case tcell.KeyLeft:
			p.agentsModal.Focus = agentsModalFocusAgents
		case tcell.KeyEnter:
			p.submitAgentsModalSave(true)
		}
	}
}

func (p *HomePage) beginAgentsModalFieldEdit() {
	options := p.agentsModalSelectedFieldOptions()
	if len(options) == 0 {
		return
	}
	current := p.agentsModalSelectedFieldValue()
	index := findAgentsModalOptionIndex(options, current)
	if index < 0 {
		index = 0
	}
	p.agentsModal.EditingField = true
	p.agentsModal.EditingOption = options[index]
	p.agentsModal.EditingOptionSet = true
}

func (p *HomePage) handleAgentsModalFieldKey(ev *tcell.EventKey) {
	options := p.agentsModalSelectedFieldOptions()
	if len(options) == 0 {
		p.agentsModal.EditingField = false
		return
	}
	if p.keybinds.Match(ev, KeybindModalClose) {
		p.agentsModal.EditingField = false
		p.agentsModal.EditingOption = ""
		p.agentsModal.EditingOptionSet = false
		return
	}
	if p.keybinds.Match(ev, KeybindAgentsEditorSave) {
		p.commitAgentsModalFieldEdit()
		p.submitAgentsModalSave(true)
		return
	}
	index := findAgentsModalOptionIndex(options, p.agentsModal.EditingOption)
	if index < 0 {
		index = 0
	}
	switch ev.Key() {
	case tcell.KeyUp, tcell.KeyLeft:
		index = (index - 1 + len(options)) % len(options)
		p.agentsModal.EditingOption = options[index]
	case tcell.KeyDown, tcell.KeyRight:
		index = (index + 1) % len(options)
		p.agentsModal.EditingOption = options[index]
	case tcell.KeyEnter:
		p.commitAgentsModalFieldEdit()
	}
}

func (p *HomePage) commitAgentsModalFieldEdit() {
	if p.agentsModal.EditingOptionSet {
		p.setAgentsModalSelectedFieldValue(p.agentsModal.EditingOption)
	}
	p.agentsModal.EditingField = false
	p.agentsModal.EditingOption = ""
	p.agentsModal.EditingOptionSet = false
}

func (p *HomePage) agentsModalSelectedFieldValue() string {
	assignment := p.selectedAgentsModalAssignment()
	if assignment == nil {
		return ""
	}
	switch p.agentsModal.SelectedField {
	case 0:
		return assignment.Provider
	case 1:
		return assignment.Model
	case 2:
		return assignment.Thinking
	default:
		return assignment.ServiceTier
	}
}

func (p *HomePage) setAgentsModalSelectedFieldValue(value string) {
	assignment := p.selectedAgentsModalAssignment()
	if assignment == nil {
		return
	}
	value = strings.TrimSpace(value)
	switch p.agentsModal.SelectedField {
	case 0:
		provider := normalizeAgentsModalProviderID(value)
		if provider != assignment.Provider {
			assignment.Provider, assignment.Model, assignment.Thinking, assignment.ServiceTier, assignment.ContextMode = provider, "", "", "", ""
		}
	case 1:
		if value != assignment.Model {
			assignment.Model, assignment.Thinking, assignment.ServiceTier = value, "", ""
			if record, ok := p.agentsModalCatalogRecord(assignment.Provider, assignment.Model); ok {
				assignment.ContextMode = strings.TrimSpace(record.ContextMode)
				assignment.Thinking = normalizeThinkingValue(record.DefaultThinking)
				assignment.ServiceTier = normalizeAgentsModalPriorityValue(record.DefaultServiceTier)
			}
		}
	case 2:
		assignment.Thinking = normalizeThinkingValue(value)
	case 3:
		assignment.ServiceTier = normalizeAgentsModalPriorityValue(value)
	}
}

func (p *HomePage) agentsModalSelectedFieldOptions() []string {
	assignment := p.selectedAgentsModalAssignment()
	if assignment == nil {
		return nil
	}
	switch p.agentsModal.SelectedField {
	case 0:
		return p.agentsModal.Providers
	case 1:
		return p.agentsModal.ModelsByProvider[normalizeAgentsModalProviderID(assignment.Provider)]
	case 2:
		if record, ok := p.agentsModalCatalogRecord(assignment.Provider, assignment.Model); ok {
			options := dedupeAgentsModalOptions(record.ThinkingOptions)
			if len(options) == 0 && strings.TrimSpace(record.DefaultThinking) != "" {
				options = []string{normalizeThinkingValue(record.DefaultThinking)}
			}
			return options
		}
		return nil
	default:
		if record, ok := p.agentsModalCatalogRecord(assignment.Provider, assignment.Model); ok {
			return dedupeAgentsModalOptions(append([]string{""}, record.ServiceTiers...))
		}
		return []string{""}
	}
}

func agentsModalPriorityOptions(record client.ModelCatalogRecord) []string {
	options := make([]string, 0, len(record.ServiceTiers)+len(record.ServiceTierMappings))
	for _, value := range record.ServiceTiers {
		if value = normalizeAgentsModalPriorityValue(value); value != "" {
			options = append(options, value)
		}
	}
	for _, mapping := range record.ServiceTierMappings {
		if value := normalizeAgentsModalPriorityValue(mapping.SwarmSetting); value != "" {
			options = append(options, value)
		}
	}
	return dedupeAgentsModalOptions(options)
}

func normalizeAgentsModalPriorityValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "standard" || value == "off" {
		return ""
	}
	return value
}

func normalizeAgentsModalOptionValues(options []string, normalize func(string) string) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, normalize(option))
	}
	return dedupeAgentsModalOptions(values)
}

func (p *HomePage) submitAgentsModalSave(closeAfterSave bool) {
	name := p.selectedAgentsModalName()
	assignments := p.agentsModal.Drafts[name]
	if (name == "swarm" && len(assignments) != 2) || (name != "swarm" && len(assignments) != 1) {
		p.agentsModal.Error = "canonical agent assignments are unavailable; refresh and try again"
		return
	}
	if !p.agentsModalHasChanges() && strings.TrimSpace(p.agentsModal.EditingProfileID) == "" {
		p.agentsModal.Status = "No pending changes to save"
		return
	}
	for _, assignment := range assignments {
		if strings.TrimSpace(assignment.Provider) == "" || strings.TrimSpace(assignment.Model) == "" || strings.TrimSpace(assignment.Thinking) == "" {
			p.agentsModal.Error = "provider, model, and thinking are required"
			return
		}
	}
	action := AgentsModalAction{Kind: AgentsModalActionSave, Agent: name, ModelProfileID: strings.TrimSpace(p.agentsModal.EditingProfileID), StayOpen: !closeAfterSave, StatusHint: "Saving agent model settings..."}
	if name == "swarm" {
		action.Swarm = &client.AgentModelSettingsSwarmPatch{Action: assignments[0], Plan: assignments[1]}
	} else {
		assignment := assignments[0]
		action.Assignment = &assignment
	}
	p.enqueueAgentsModalAction(action)
}

func (p *HomePage) enqueueAgentsModalAction(action AgentsModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingAgentsAction = &action
	p.agentsModal.Loading = true
	p.agentsModalTargets = p.agentsModalTargets[:0]
	p.agentsModal.Status = strings.TrimSpace(action.StatusHint)
	p.agentsModal.Error = ""
}

func (p *HomePage) handleAgentsModalMouse(ev *tcell.EventMouse) bool {
	if p == nil || ev == nil || !p.agentsModal.Visible {
		return false
	}
	if p.agentsModal.Loading {
		return true
	}
	x, y := ev.Position()
	target, ok := p.agentsModalTargetAt(x, y)
	if !ok || ev.Buttons()&tcell.Button1 == 0 {
		return true
	}
	switch target.Action {
	case "agents-agent":
		p.agentsModal.SelectedAgent = target.Index
		p.agentsModal.SelectedAssignment = 0
		p.agentsModal.Focus = agentsModalFocusAssignments
	case "agents-assignment":
		p.agentsModal.SelectedAssignment = target.Index
		p.agentsModal.SelectedField = 0
		p.agentsModal.Focus = agentsModalFocusFields
	case "agents-field":
		p.agentsModal.SelectedField = target.Index
		p.agentsModal.Focus = agentsModalFocusFields
		p.beginAgentsModalFieldEdit()
	case "agents-option":
		p.agentsModal.SelectedField = target.Index
		p.agentsModal.EditingOption = target.Meta
		p.agentsModal.EditingOptionSet = true
		p.commitAgentsModalFieldEdit()
	case "agents-save-continue":
		p.submitAgentsModalSave(false)
	case "agents-save-exit":
		p.submitAgentsModalSave(true)
	}
	return true
}

func (p *HomePage) drawAgentsModal(s tcell.Screen) {
	p.agentsModalTargets = p.agentsModalTargets[:0]
	if p == nil || !p.agentsModal.Visible {
		return
	}
	w, h := s.Size()
	modalW := minInt(112, w-4)
	modalH := minInt(34, h-4)
	rect := Rect{X: maxInt(1, (w-modalW)/2), Y: maxInt(1, (h-modalH)/2), W: modalW, H: modalH}
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	title := "Agents"
	if p.agentsModal.Loading {
		title += " [loading]"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, title)
	status, statusStyle := strings.TrimSpace(p.agentsModal.Status), p.theme.TextMuted
	if p.agentsModal.Error != "" {
		status, statusStyle = p.agentsModal.Error, p.theme.Error
	}
	if status == "" {
		status = "Choose an agent, then edit its daemon-owned model settings"
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(status, rect.W-4))

	body := Rect{X: rect.X + 1, Y: rect.Y + 3, W: rect.W - 2, H: rect.H - 6}
	leftW := maxInt(36, body.W*2/5)
	leftW = minInt(leftW, maxInt(22, body.W-36))
	left := Rect{X: body.X, Y: body.Y, W: leftW, H: body.H}
	right := Rect{X: body.X + leftW + 1, Y: body.Y, W: body.W - leftW - 1, H: body.H}
	p.drawAgentsModalAgentList(s, left)
	p.drawAgentsModalEditor(s, right)
	DrawText(s, rect.X+2, rect.Y+rect.H-2, rect.W-4, p.theme.TextMuted, "↑/↓ navigate • Enter opens • S saves and continues • Ctrl+Y saves and exits • Esc cancels")
	DrawText(s, rect.X+2, rect.Y+rect.H-1, rect.W-4, p.theme.TextMuted, "All values read and write /v1/agent-model-settings")
}

func (p *HomePage) drawAgentsModalAgentList(s tcell.Screen, rect Rect) {
	style := p.theme.Border
	if p.agentsModal.Focus == agentsModalFocusAgents {
		style = p.theme.BorderActive
	}
	DrawBox(s, rect, style)
	const cardH = 4
	visibleCards := maxInt(1, (rect.H-2)/cardH)
	start := 0
	if p.agentsModal.SelectedAgent >= visibleCards {
		start = p.agentsModal.SelectedAgent - visibleCards + 1
	}
	listTitle := "Core system agents"
	if start >= utilityAgentModelStart {
		listTitle = "Utilities"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, listTitle)
	for i := start; i < len(canonicalAgentModelNames); i++ {
		name := canonicalAgentModelNames[i]
		card := Rect{X: rect.X + 1, Y: rect.Y + 1 + (i-start)*cardH, W: rect.W - 2, H: cardH}
		if start < utilityAgentModelStart && i == utilityAgentModelStart {
			DrawText(s, card.X+1, card.Y-1, card.W-2, p.theme.TextMuted.Bold(true), " Utilities ")
		}
		if card.Y+card.H > rect.Y+rect.H-1 {
			break
		}
		selected := i == p.agentsModal.SelectedAgent
		cardBorder := p.theme.Border
		prefix, nameStyle := "  ", p.theme.Text.Bold(true)
		if selected {
			cardBorder = p.theme.BorderActive
			prefix, nameStyle = "> ", p.theme.Accent.Bold(true)
		}
		DrawBox(s, card, cardBorder)
		contentX, contentW := card.X+1, maxInt(0, card.W-2)
		DrawText(s, contentX, card.Y, contentW, styleForCurrentCellBackground(nameStyle), clampEllipsis(prefix+agentsModalDisplayName(name), contentW))

		assignment := client.AgentModelAssignment{}
		if assignments := p.agentsModal.Drafts[name]; len(assignments) > 0 {
			assignment = assignments[0]
		}
		DrawText(s, contentX+2, card.Y+1, maxInt(0, contentW-2), styleForCurrentCellBackground(p.theme.Text), clampEllipsis(agentsModalAssignmentModelLine(assignment), maxInt(0, contentW-2)))
		DrawText(s, contentX+2, card.Y+2, maxInt(0, contentW-2), styleForCurrentCellBackground(p.theme.TextMuted), clampEllipsis(agentsModalAssignmentSettingsLine(assignment), maxInt(0, contentW-2)))
		p.registerAgentsModalTarget(card, "agents-agent", i, "")
	}
}

func (p *HomePage) drawAgentsModalEditor(s tcell.Screen, rect Rect) {
	style := p.theme.Border
	if p.agentsModal.Focus != agentsModalFocusAgents {
		style = p.theme.BorderActive
	}
	DrawBox(s, rect, style)
	name := p.selectedAgentsModalName()
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, agentsModalDisplayName(name)+" model settings")
	assignments := p.selectedAgentsModalAssignments()
	row := rect.Y + 2
	for i, assignment := range assignments {
		selected := p.agentsModal.Focus == agentsModalFocusAssignments && i == p.agentsModal.SelectedAssignment
		prefix, lineStyle := "  ", p.theme.Text
		if selected {
			prefix, lineStyle = "> ", p.theme.Accent.Bold(true)
		}
		label := agentsModalAssignmentLabel(name, i)
		DrawText(s, rect.X+2, row, rect.W-4, lineStyle, prefix+label)
		DrawText(s, rect.X+4, row+1, rect.W-6, p.theme.TextMuted, clampEllipsis(agentsModalAssignmentSummary(assignment), rect.W-6))
		p.registerAgentsModalTarget(Rect{X: rect.X + 1, Y: row, W: rect.W - 2, H: 2}, "agents-assignment", i, "")
		row += 3
	}

	assignment := p.selectedAgentsModalAssignment()
	if assignment != nil && (p.agentsModal.Focus == agentsModalFocusFields || p.agentsModal.Focus == agentsModalFocusSave) {
		row++
		labels := []string{"Provider", "Model", "Thinking", "Priority"}
		values := []string{assignment.Provider, assignment.Model, assignment.Thinking, assignment.ServiceTier}
		for i, label := range labels {
			if row >= rect.Y+rect.H-3 {
				break
			}
			selected := p.agentsModal.Focus == agentsModalFocusFields && i == p.agentsModal.SelectedField
			prefix, fieldStyle := "  ", p.theme.TextMuted
			if selected {
				prefix, fieldStyle = "> ", p.theme.Accent.Bold(true)
				if p.agentsModal.EditingField {
					prefix, fieldStyle = "* ", p.theme.Primary.Bold(true)
				}
			}
			value := strings.TrimSpace(values[i])
			if value == "" {
				value = "off"
				if i < 3 {
					value = "choose " + strings.ToLower(label)
				}
			}
			DrawText(s, rect.X+2, row, rect.W-4, fieldStyle, fmt.Sprintf("%s[%s: %s]", prefix, label, value))
			p.registerAgentsModalTarget(Rect{X: rect.X + 1, Y: row, W: rect.W - 2, H: 1}, "agents-field", i, "")
			row++
			if selected && p.agentsModal.EditingField {
				for _, option := range p.agentsModalSelectedFieldOptions() {
					if row >= rect.Y+rect.H-3 {
						break
					}
					optionLabel := strings.TrimSpace(option)
					if optionLabel == "" {
						optionLabel = "off"
					}
					optionPrefix, optionStyle := "    ", p.theme.TextMuted
					if strings.EqualFold(option, p.agentsModal.EditingOption) {
						optionPrefix, optionStyle = "  > ", p.theme.Text
					}
					DrawText(s, rect.X+2, row, rect.W-4, optionStyle, optionPrefix+optionLabel)
					p.registerAgentsModalTarget(Rect{X: rect.X + 1, Y: row, W: rect.W - 2, H: 1}, "agents-option", i, option)
					row++
				}
			}
		}
	}

	saveStyle, savePrefix := p.theme.TextMuted, "  "
	if p.agentsModal.Focus == agentsModalFocusSave {
		saveStyle, savePrefix = p.theme.Accent.Bold(true), "> "
	}
	saveY := rect.Y + rect.H - 2
	continueLabel := "[ Save & Continue ]"
	exitLabel := "[ Save & Exit ]"
	DrawText(s, rect.X+2, saveY, rect.W-4, saveStyle, savePrefix+continueLabel+"  "+exitLabel)
	continueX := rect.X + 2 + len([]rune(savePrefix))
	p.registerAgentsModalTarget(Rect{X: continueX, Y: saveY, W: len([]rune(continueLabel)), H: 1}, "agents-save-continue", -1, "")
	exitX := continueX + len([]rune(continueLabel)) + 2
	p.registerAgentsModalTarget(Rect{X: exitX, Y: saveY, W: len([]rune(exitLabel)), H: 1}, "agents-save-exit", -1, "")
}

func (p *HomePage) agentsModalCatalogRecord(providerID, modelID string) (client.ModelCatalogRecord, bool) {
	if p == nil {
		return client.ModelCatalogRecord{}, false
	}
	key := strings.ToLower(strings.TrimSpace(providerID) + "/" + strings.TrimSpace(modelID))
	record, ok := p.agentsModal.ModelCatalog[key]
	return record, ok
}

func normalizeThinkingValue(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "x-high" {
		return "xhigh"
	}
	return value
}

func normalizeAgentsModalProviderID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func dedupeAgentsModalOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, raw := range options {
		value := strings.TrimSpace(raw)
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func findAgentsModalOptionIndex(options []string, value string) int {
	for i, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(value)) {
			return i
		}
	}
	return -1
}

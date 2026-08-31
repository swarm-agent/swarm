package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

const workspaceScopeDecisionPathID = "permission.workspace_scope.decision.v1"

type askUserOption struct {
	Value       string
	Label       string
	Description string
	AllowCustom bool
}

type askUserQuestion struct {
	ID       string
	Header   string
	Question string
	Options  []askUserOption
	Required bool
}

type askUserIntent struct {
	Title     string
	Context   string
	Questions []askUserQuestion
}

type workspaceScopeAction struct {
	Decision    string
	Label       string
	Description string
	Available   bool
}

type workspaceScopeIntent struct {
	Title              string
	Summary            string
	ToolName           string
	AccessLabel        string
	RequestedPath      string
	ResolvedPath       string
	DirectoryPath      string
	TemporaryBehavior  string
	PersistentBehavior string
	SessionAllow       workspaceScopeAction
}

type permissionInteractionView struct {
	PermissionID       string
	Ask                *askUserIntent
	AskQuestion        int
	AskSelection       int
	AskAnswers         map[string]string
	AskCustomInput     string
	AskCustomMode      bool
	WorkspaceSelection int
}

func isAskUserPermission(record client.PermissionRecord) bool {
	return normalizePermissionToolName(record.ToolName) == "ask_user"
}

func isWorkspaceScopePermission(record client.PermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "workspace_scope")
}

func defaultAskUserOptions() []askUserOption {
	return nil
}

func ensureAskUserFreeformOption(options []askUserOption) []askUserOption {
	concrete := make([]askUserOption, 0, len(options)+1)
	for _, option := range options {
		if option.AllowCustom || strings.EqualFold(strings.TrimSpace(option.Value), "__custom__") {
			continue
		}
		concrete = append(concrete, option)
	}
	return append(concrete, askUserOption{Value: "__custom__", Label: "Custom response", Description: "Type your own response.", AllowCustom: true})
}

func parseAskUserOptions(raw any) []askUserOption {
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	options := make([]askUserOption, 0, len(entries))
	for _, entry := range entries {
		switch value := entry.(type) {
		case string:
			label := strings.TrimSpace(value)
			if label != "" {
				options = append(options, askUserOption{Value: label, Label: label, AllowCustom: strings.EqualFold(label, "__custom__")})
			}
		case map[string]any:
			label := toolString(value, "label")
			optionValue := toolString(value, "value")
			allowCustom := toolBool(value, "allow_custom") || toolBool(value, "allowCustom") || strings.EqualFold(optionValue, "__custom__")
			if allowCustom && optionValue == "" {
				optionValue = "__custom__"
			}
			if allowCustom && label == "" {
				label = "Custom response"
			}
			if label == "" {
				label = optionValue
			}
			if optionValue == "" {
				optionValue = label
			}
			if label != "" || optionValue != "" {
				options = append(options, askUserOption{Value: optionValue, Label: label, Description: toolString(value, "description"), AllowCustom: allowCustom})
			}
		}
	}
	return options
}

func parseAskUserIntent(record client.PermissionRecord) (askUserIntent, bool) {
	if !isAskUserPermission(record) {
		return askUserIntent{}, false
	}
	payload := parseToolObject(record.ToolArguments)
	if payload == nil {
		return askUserIntent{Title: "Ask User", Questions: []askUserQuestion{{ID: "q_1", Question: "User input requested", Options: ensureAskUserFreeformOption(defaultAskUserOptions()), Required: true}}}, true
	}
	intent := askUserIntent{Title: firstNonEmptyToolRaw(toolString(payload, "title"), "Ask User"), Context: toolString(payload, "context")}
	if rawQuestions, ok := payload["questions"].([]any); ok {
		for index, rawQuestion := range rawQuestions {
			questionPayload, ok := rawQuestion.(map[string]any)
			if !ok {
				continue
			}
			question := askUserQuestion{
				ID:       firstNonEmptyToolRaw(toolString(questionPayload, "id"), fmt.Sprintf("q_%d", index+1)),
				Header:   toolString(questionPayload, "header"),
				Question: firstNonEmptyToolRaw(toolString(questionPayload, "question"), toolString(questionPayload, "prompt"), toolString(questionPayload, "text"), toolString(questionPayload, "title"), "User input requested"),
				Options:  parseAskUserOptions(questionPayload["options"]),
				Required: true,
			}
			if value, exists := questionPayload["required"]; exists {
				switch required := value.(type) {
				case bool:
					question.Required = required
				case string:
					question.Required = !containsFold([]string{"false", "0", "no"}, required)
				}
			}
			question.Options = ensureAskUserFreeformOption(question.Options)
			intent.Questions = append(intent.Questions, question)
		}
	}
	if len(intent.Questions) == 0 {
		options := parseAskUserOptions(payload["options"])
		options = ensureAskUserFreeformOption(options)
		intent.Questions = []askUserQuestion{{ID: "q_1", Question: firstNonEmptyToolRaw(toolString(payload, "question"), "User input requested"), Options: options, Required: true}}
	}
	return intent, true
}

func workspaceScopeAccessLabel(toolName string) string {
	switch normalizePermissionToolName(toolName) {
	case "read", "list", "grep", "agentic_search", "search":
		return "read access"
	default:
		return "access"
	}
}

func parseWorkspaceScopeAction(payload map[string]any, fallback workspaceScopeAction) workspaceScopeAction {
	if payload == nil {
		return fallback
	}
	action := workspaceScopeAction{
		Decision:    firstNonEmptyToolRaw(toolString(payload, "decision"), fallback.Decision),
		Label:       firstNonEmptyToolRaw(toolString(payload, "label"), fallback.Label),
		Description: firstNonEmptyToolRaw(toolString(payload, "description"), fallback.Description),
		Available:   fallback.Available,
	}
	if _, exists := payload["available"]; exists {
		action.Available = toolBool(payload, "available")
	}
	return action
}

func parseWorkspaceScopeIntent(record client.PermissionRecord) (workspaceScopeIntent, bool) {
	if !isWorkspaceScopePermission(record) {
		return workspaceScopeIntent{}, false
	}
	payload := parseToolObject(record.ToolArguments)
	if payload == nil {
		payload = map[string]any{}
	}
	tool := toolObject(payload, "tool")
	request := toolObject(payload, "request")
	actions := toolObject(payload, "actions")
	toolName := firstNonEmptyToolRaw(toolString(tool, "name"), record.ToolName)
	accessLabel := firstNonEmptyToolRaw(toolString(request, "access_label"), workspaceScopeAccessLabel(toolName))
	requestedPath := toolString(request, "requested_path")
	resolvedPath := toolString(request, "resolved_target_path")
	directoryPath := firstNonEmptyToolRaw(toolString(request, "directory_path"), resolvedPath, requestedPath)
	temporaryBehavior := fmt.Sprintf("Approving this temporarily allows %s to %s for this chat session only. It does not save or change any workspace. A different chat session will ask again.", accessLabel, firstNonEmptyToolRaw(directoryPath, "the requested directory"))
	intent := workspaceScopeIntent{
		Title:              firstNonEmptyToolRaw(toolString(payload, "title"), fmt.Sprintf("Allow %s outside the current workspace?", accessLabel)),
		Summary:            toolString(payload, "summary"),
		ToolName:           toolName,
		AccessLabel:        accessLabel,
		RequestedPath:      requestedPath,
		ResolvedPath:       resolvedPath,
		DirectoryPath:      directoryPath,
		TemporaryBehavior:  temporaryBehavior,
		PersistentBehavior: "For durable use, add this folder as its own new workspace from the workspace picker.",
	}
	if intent.Summary == "" {
		intent.Summary = fmt.Sprintf("Allow %s for this chat session only.", accessLabel)
	}
	intent.SessionAllow = parseWorkspaceScopeAction(toolObject(actions, "session_allow"), workspaceScopeAction{Decision: "session_allow", Label: "Allow for This Chat Session", Description: temporaryBehavior, Available: true})
	intent.SessionAllow.Decision = "session_allow"
	intent.SessionAllow.Label = "Allow for This Chat Session"
	intent.SessionAllow.Description = temporaryBehavior
	intent.SessionAllow.Available = true
	return intent, true
}

func workspaceScopeResolutionReason(decision string) string {
	decision = "session_allow"
	encoded, _ := json.Marshal(map[string]string{"path_id": workspaceScopeDecisionPathID, "decision": decision})
	return string(encoded)
}

func askUserResolutionReason(intent askUserIntent, answers map[string]string) (string, bool) {
	normalized := make(map[string]string, len(intent.Questions))
	ordered := make([]map[string]string, 0, len(intent.Questions))
	for _, question := range intent.Questions {
		id := strings.TrimSpace(question.ID)
		answer := strings.TrimSpace(answers[id])
		if answer == "" && question.Required {
			return "", false
		}
		if id != "" {
			normalized[id] = answer
		}
		ordered = append(ordered, map[string]string{"id": id, "question": strings.TrimSpace(question.Question), "answer": answer})
	}
	if len(intent.Questions) == 1 {
		return strings.TrimSpace(normalized[strings.TrimSpace(intent.Questions[0].ID)]), true
	}
	encoded, err := json.Marshal(map[string]any{"path_id": "tool.ask-user.ui.v1", "answers": normalized, "items": ordered})
	return string(encoded), err == nil
}

func (p *Page) resetPermissionInteractionLocked() {
	p.permissionInteractionID = ""
	p.permissionAskQuestion = 0
	p.permissionAskSelections = nil
	p.permissionAskAnswers = nil
	p.permissionAskCustomInput = nil
	p.permissionAskCustomMode = false
	p.permissionWorkspaceSelection = 0
}

func (p *Page) syncPermissionInteractionLocked(record client.PermissionRecord) {
	id := strings.TrimSpace(record.ID)
	if id == p.permissionInteractionID {
		return
	}
	p.resetPermissionInteractionLocked()
	p.permissionInteractionID = id
	if intent, ok := parseAskUserIntent(record); ok {
		p.permissionAskSelections = make(map[string]int, len(intent.Questions))
		p.permissionAskAnswers = make(map[string]string, len(intent.Questions))
		for _, question := range intent.Questions {
			questionID := strings.TrimSpace(question.ID)
			p.permissionAskSelections[questionID] = 0
			if len(question.Options) > 0 && !question.Options[0].AllowCustom {
				p.permissionAskAnswers[questionID] = firstNonEmptyToolRaw(question.Options[0].Value, question.Options[0].Label)
			}
		}
	}
}

func (p *Page) permissionInteractionViewLocked(record client.PermissionRecord) *permissionInteractionView {
	if strings.TrimSpace(record.ID) != p.permissionInteractionID {
		return nil
	}
	view := &permissionInteractionView{PermissionID: p.permissionInteractionID, WorkspaceSelection: p.permissionWorkspaceSelection}
	if intent, ok := parseAskUserIntent(record); ok {
		view.Ask = &intent
		view.AskQuestion = maxInt(0, minInt(p.permissionAskQuestion, len(intent.Questions)-1))
		if len(intent.Questions) > 0 {
			view.AskSelection = p.permissionAskSelections[strings.TrimSpace(intent.Questions[view.AskQuestion].ID)]
		}
		view.AskAnswers = make(map[string]string, len(p.permissionAskAnswers))
		for key, value := range p.permissionAskAnswers {
			view.AskAnswers[key] = value
		}
		view.AskCustomInput = string(p.permissionAskCustomInput)
		view.AskCustomMode = p.permissionAskCustomMode
	}
	return view
}

func (p *Page) currentAskUserStateLocked(record client.PermissionRecord) (askUserIntent, askUserQuestion, int, bool) {
	intent, ok := parseAskUserIntent(record)
	if !ok || len(intent.Questions) == 0 {
		return askUserIntent{}, askUserQuestion{}, 0, false
	}
	p.permissionAskQuestion = maxInt(0, minInt(p.permissionAskQuestion, len(intent.Questions)-1))
	question := intent.Questions[p.permissionAskQuestion]
	selection := p.permissionAskSelections[strings.TrimSpace(question.ID)]
	selection = maxInt(0, minInt(selection, len(question.Options)-1))
	p.permissionAskSelections[strings.TrimSpace(question.ID)] = selection
	return intent, question, selection, true
}

func (p *Page) selectAskUserOptionLocked(question askUserQuestion, selection int) {
	if len(question.Options) == 0 {
		return
	}
	selection = maxInt(0, minInt(selection, len(question.Options)-1))
	id := strings.TrimSpace(question.ID)
	p.permissionAskSelections[id] = selection
	option := question.Options[selection]
	if !option.AllowCustom {
		p.permissionAskAnswers[id] = firstNonEmptyToolRaw(option.Value, option.Label)
		p.permissionAskCustomInput = nil
		p.permissionAskCustomMode = false
	}
}

func (p *Page) submitAskUserPermissionLocked(record client.PermissionRecord) bool {
	intent, _, _, ok := p.currentAskUserStateLocked(record)
	if !ok {
		return false
	}
	reason, valid := askUserResolutionReason(intent, p.permissionAskAnswers)
	if !valid {
		p.permissionError = "Answer each required question before submitting."
		return false
	}
	p.resolvePermissionWithReasonLocked(record, "allow_once", reason)
	return true
}

func (p *Page) handleAskUserPermissionKeyLocked(record client.PermissionRecord, ev *tcell.EventKey) PageAction {
	intent, question, selection, ok := p.currentAskUserStateLocked(record)
	if !ok {
		return PageActionNone
	}
	if p.permissionAskCustomMode {
		switch ev.Key() {
		case tcell.KeyEscape:
			p.permissionAskCustomMode = false
			p.permissionAskCustomInput = nil
		case tcell.KeyEnter:
			answer := strings.TrimSpace(string(p.permissionAskCustomInput))
			if answer == "" {
				p.permissionError = "Type a response before continuing."
				return PageActionNone
			}
			p.permissionAskAnswers[strings.TrimSpace(question.ID)] = answer
			p.permissionAskCustomMode = false
			p.permissionAskCustomInput = nil
			if p.permissionAskQuestion+1 < len(intent.Questions) {
				p.permissionAskQuestion++
			} else {
				p.submitAskUserPermissionLocked(record)
			}
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(p.permissionAskCustomInput) > 0 {
				p.permissionAskCustomInput = p.permissionAskCustomInput[:len(p.permissionAskCustomInput)-1]
			}
		case tcell.KeyCtrlU:
			p.permissionAskCustomInput = nil
		case tcell.KeyRune:
			if utf8.ValidRune(ev.Rune()) && ev.Rune() >= ' ' && len(p.permissionAskCustomInput) < maxComposerRunes {
				p.permissionAskCustomInput = append(p.permissionAskCustomInput, ev.Rune())
			}
		}
		return PageActionNone
	}

	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolvePermissionWithReasonLocked(record, "deny_once", "")
	case tcell.KeyUp:
		p.selectAskUserOptionLocked(question, selection-1)
	case tcell.KeyDown:
		p.selectAskUserOptionLocked(question, selection+1)
	case tcell.KeyLeft, tcell.KeyBacktab:
		p.permissionAskQuestion = maxInt(0, p.permissionAskQuestion-1)
	case tcell.KeyRight, tcell.KeyTab:
		p.permissionAskQuestion = minInt(len(intent.Questions)-1, p.permissionAskQuestion+1)
	case tcell.KeyEnter:
		if len(question.Options) == 0 {
			return PageActionNone
		}
		option := question.Options[selection]
		if option.AllowCustom {
			p.permissionAskCustomMode = true
			p.permissionAskCustomInput = []rune(p.permissionAskAnswers[strings.TrimSpace(question.ID)])
			return PageActionNone
		}
		p.selectAskUserOptionLocked(question, selection)
		if p.permissionAskQuestion+1 < len(intent.Questions) {
			p.permissionAskQuestion++
		} else {
			p.submitAskUserPermissionLocked(record)
		}
	case tcell.KeyRune:
		r := ev.Rune()
		switch {
		case r >= '1' && r <= '9':
			index := int(r - '1')
			if index < len(question.Options) {
				p.selectAskUserOptionLocked(question, index)
			}
		case r == 's' || r == 'S':
			p.submitAskUserPermissionLocked(record)
		}
	}
	return PageActionNone
}

func specializedPermissionCardRows(record client.PermissionRecord, pendingCount, width int, styles PageStyles, selected, busy bool, errorText string, interaction *permissionInteractionView) []renderRow {
	if interaction == nil {
		interaction = &permissionInteractionView{PermissionID: strings.TrimSpace(record.ID)}
	}
	if intent, ok := parseWorkspaceScopeIntent(record); ok {
		model := permissionCardModel{
			Title: intent.Title,
			Badge: "WORKSPACE",
			Meta:  permissionCardMeta(record, pendingCount, firstNonEmpty(strings.TrimSpace(record.Mode), "auto")),
		}
		appendWrappedPermissionLine := func(text string, style tcell.Style) {
			for _, line := range wrapText(strings.TrimSpace(text), maxInt(1, width-4)) {
				model.Content = append(model.Content, permissionCardLine{Text: line, Style: style})
			}
		}
		appendWrappedPermissionLine(intent.Summary, styles.Text)
		model.Content = append(model.Content, permissionCardLine{Text: "", Style: styles.Muted})
		appendWrappedPermissionLine("REQUESTED PATH  ·  "+firstNonEmptyToolRaw(intent.RequestedPath, "Unavailable"), styles.Secondary)
		if intent.ResolvedPath != "" && intent.ResolvedPath != intent.RequestedPath {
			appendWrappedPermissionLine("RESOLVED TARGET  ·  "+intent.ResolvedPath, styles.Secondary)
		}
		appendWrappedPermissionLine("SESSION SCOPE ROOT  ·  "+firstNonEmptyToolRaw(intent.DirectoryPath, intent.ResolvedPath, intent.RequestedPath, "Unavailable"), styles.Secondary)
		model.Content = append(model.Content, permissionCardLine{Text: "", Style: styles.Muted})
		appendWrappedPermissionLine(intent.TemporaryBehavior, styles.Text)
		appendWrappedPermissionLine(intent.PersistentBehavior, styles.Muted)
		actions := []permissionCardAction{
			{Label: "1 " + intent.SessionAllow.Label, Action: "workspace_session", Tone: styles.Success},
			{Label: "2 Deny", Action: "deny_once", Tone: styles.Error},
		}
		if !permissionPending(record) {
			model.Content = append(model.Content,
				permissionCardLine{Text: "", Style: styles.Muted},
				permissionCardLine{Text: "RESOLVED", Style: styles.Muted.Bold(true)},
				permissionCardLine{Text: permissionResolvedLabel(record), Style: permissionResolvedStyle(record, styles).Bold(true)},
			)
		}
		for index := range actions {
			selectedAction := interaction.WorkspaceSelection
			if interaction.WorkspaceSelection == 2 {
				selectedAction = len(actions) - 1
			}
			if index == selectedAction {
				actions[index].Label = "› " + actions[index].Label
			}
		}
		return permissionCardRows(permissionCardView{Model: model, Selected: selected, Pending: permissionPending(record), Busy: busy, ErrorText: errorText, HideNote: true, Actions: actions}, width, styles)
	}
	intent, ok := parseAskUserIntent(record)
	if !ok || len(intent.Questions) == 0 {
		return inlinePermissionCardRows(record, pendingCount, width, styles, record.SavedRulePreview, selected, nil, busy, errorText)
	}
	questionIndex := maxInt(0, minInt(interaction.AskQuestion, len(intent.Questions)-1))
	question := intent.Questions[questionIndex]
	model := permissionCardModel{
		Title: firstNonEmptyToolRaw(intent.Title, "Ask User"),
		Badge: "QUESTION",
		Meta:  fmt.Sprintf("Response required  ·  question %d/%d", questionIndex+1, len(intent.Questions)),
	}
	if intent.Context != "" {
		for _, line := range wrapText(intent.Context, maxInt(1, width-4)) {
			model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Muted})
		}
		model.Content = append(model.Content, permissionCardLine{Text: "", Style: styles.Muted})
	}
	header := firstNonEmptyToolRaw(question.Header, fmt.Sprintf("Question %d", questionIndex+1))
	if question.Required {
		header += "  ·  required"
	}
	model.Content = append(model.Content, permissionCardLine{Text: strings.ToUpper(header), Style: styles.Muted.Bold(true)})
	for _, line := range wrapText(question.Question, maxInt(1, width-4)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text.Bold(true)})
	}
	model.Content = append(model.Content, permissionCardLine{Text: "", Style: styles.Muted})
	selection := maxInt(0, minInt(interaction.AskSelection, len(question.Options)-1))
	for index, option := range question.Options {
		prefix := "  "
		style := styles.Text
		if index == selection {
			prefix = "› "
			style = styles.Primary.Bold(true)
		}
		label := firstNonEmptyToolRaw(option.Label, option.Value, "Option")
		model.Content = append(model.Content, permissionCardLine{Text: fmt.Sprintf("%s%d %s", prefix, index+1, label), Style: style})
		if index == selection && option.Description != "" {
			for _, line := range wrapText("    "+option.Description, maxInt(1, width-4)) {
				model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Muted})
			}
		}
	}
	if !permissionPending(record) {
		model.Content = append(model.Content,
			permissionCardLine{Text: "", Style: styles.Muted},
			permissionCardLine{Text: "RESOLVED", Style: styles.Muted.Bold(true)},
			permissionCardLine{Text: permissionResolvedLabel(record), Style: permissionResolvedStyle(record, styles).Bold(true)},
		)
	} else if interaction.AskCustomMode {
		model.Content = append(model.Content,
			permissionCardLine{Text: "", Style: styles.Muted},
			permissionCardLine{Text: "CUSTOM RESPONSE", Style: styles.Muted.Bold(true)},
			permissionCardLine{Text: "> " + interaction.AskCustomInput + "▌", Style: styles.Text},
			permissionCardLine{Text: "Enter saves · Esc cancels typing", Style: styles.Muted},
		)
	} else {
		model.Content = append(model.Content, permissionCardLine{Text: "↑/↓ choose · Enter select · ←/→ question · S submit · Esc deny", Style: styles.Muted})
	}
	return permissionCardRows(permissionCardView{Model: model, Selected: selected, Pending: permissionPending(record), Busy: busy, ErrorText: errorText, HideNote: true, Actions: []permissionCardAction{{Label: "Enter Select", Action: "ask_select", Tone: styles.Success}, {Label: "S Submit", Action: "ask_submit", Tone: styles.Accent}, {Label: "Esc Deny", Action: "deny_once", Tone: styles.Error}}}, width, styles)
}

func (p *Page) handleWorkspaceScopePermissionKeyLocked(record client.PermissionRecord, ev *tcell.EventKey) PageAction {
	intent, ok := parseWorkspaceScopeIntent(record)
	if !ok {
		return PageActionNone
	}
	available := []int{0, 2}
	shift := func(delta int) {
		position := 0
		for index, value := range available {
			if value == p.permissionWorkspaceSelection {
				position = index
				break
			}
		}
		position = (position + delta + len(available)) % len(available)
		p.permissionWorkspaceSelection = available[position]
	}
	resolveSelection := func() {
		switch p.permissionWorkspaceSelection {
		case 2:
			p.resolvePermissionWithReasonLocked(record, "deny_once", "")
		default:
			p.resolvePermissionWithReasonLocked(record, "allow_once", workspaceScopeResolutionReason(intent.SessionAllow.Decision))
		}
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolvePermissionWithReasonLocked(record, "deny_once", "")
	case tcell.KeyEnter:
		resolveSelection()
	case tcell.KeyLeft, tcell.KeyBacktab:
		shift(-1)
	case tcell.KeyRight, tcell.KeyTab:
		shift(1)
	case tcell.KeyRune:
		switch ev.Rune() {
		case '1':
			p.permissionWorkspaceSelection = 0
			resolveSelection()
		case '2':
			p.permissionWorkspaceSelection = 2
			resolveSelection()
		}
	}
	return PageActionNone
}

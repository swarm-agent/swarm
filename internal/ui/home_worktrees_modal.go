package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type worktreeEditorKind int

const (
	worktreeEditorNone worktreeEditorKind = iota
	worktreeEditorCreatedBranch
	worktreeEditorBranchOffSource
)

type WorktreesModalData struct {
	WorkspacePath    string
	Enabled          bool
	UseCurrentBranch bool
	BaseBranch       string
	BranchName       string
	ResolvedBranch   string
	UpdatedAt        int64
}

type WorktreesModalActionKind string

const (
	WorktreesModalActionRefresh          WorktreesModalActionKind = "refresh"
	WorktreesModalActionSetCreatedBranch WorktreesModalActionKind = "set_created_branch"
	WorktreesModalActionSetBranchSource  WorktreesModalActionKind = "set_branch_source"
	WorktreesModalActionCreateSession    WorktreesModalActionKind = "create_session"
)

type WorktreesModalAction struct {
	Kind       WorktreesModalActionKind
	BaseBranch string
	BranchName string
	Title      string
	StatusHint string
}

type worktreesModalState struct {
	Visible          bool
	Loading          bool
	Selected         int
	Status           string
	Error            string
	Data             WorktreesModalData
	EditingBranch    bool
	EditorKind       worktreeEditorKind
	BranchInput      string
	BranchInputHint  string
	CreatingSession  bool
	CreateField      int
	CreateTitle      string
	CreateBranch     string
	BranchOverridden bool
}

func (p *HomePage) ShowWorktreesModal() {
	p.worktreesModal.Visible = true
	p.worktreesModal.CreatingSession = false
	if p.worktreesModal.Selected < 0 || p.worktreesModal.Selected > 2 {
		p.worktreesModal.Selected = 0
	}
	if strings.TrimSpace(p.worktreesModal.Status) == "" {
		p.worktreesModal.Status = "Enter: action  •  c edit created branch  •  b edit branch-off source  •  Esc close"
	}
}

func (p *HomePage) ShowWorktreeCreateModal() {
	p.worktreesModal = worktreesModalState{
		Visible:         true,
		CreatingSession: true,
		CreateField:     0,
		Status:          "Create an isolated session and branch from this workspace",
	}
}

func (p *HomePage) HideWorktreesModal() {
	p.worktreesModal = worktreesModalState{}
	p.pendingWorktreesAction = nil
}

func (p *HomePage) WorktreesModalVisible() bool {
	return p.worktreesModal.Visible
}

func (p *HomePage) WorktreeCreateModalVisible() bool {
	return p.worktreesModal.Visible && p.worktreesModal.CreatingSession
}

func (p *HomePage) SetWorktreesModalLoading(loading bool) {
	p.worktreesModal.Loading = loading
}

func (p *HomePage) SetWorktreesModalStatus(status string) {
	p.worktreesModal.Status = strings.TrimSpace(status)
	if p.worktreesModal.Status != "" {
		p.worktreesModal.Error = ""
	}
}

func (p *HomePage) SetWorktreesModalError(err string) {
	p.worktreesModal.Error = strings.TrimSpace(err)
	if p.worktreesModal.Error != "" {
		p.worktreesModal.Loading = false
	}
}

func (p *HomePage) SetWorktreesModalData(data WorktreesModalData) {
	data.WorkspacePath = strings.TrimSpace(data.WorkspacePath)
	data.BaseBranch = strings.TrimSpace(data.BaseBranch)
	data.BranchName = strings.TrimSpace(data.BranchName)
	data.ResolvedBranch = strings.TrimSpace(data.ResolvedBranch)
	p.worktreesModal.Data = data
}

func (p *HomePage) WorktreesModalData() WorktreesModalData {
	return p.worktreesModal.Data
}

func (p *HomePage) PopWorktreesModalAction() (WorktreesModalAction, bool) {
	if p.pendingWorktreesAction == nil {
		return WorktreesModalAction{}, false
	}
	action := *p.pendingWorktreesAction
	p.pendingWorktreesAction = nil
	return action, true
}

func (p *HomePage) enqueueWorktreesModalAction(action WorktreesModalAction) {
	p.pendingWorktreesAction = &action
	if strings.TrimSpace(action.StatusHint) != "" {
		p.worktreesModal.Status = strings.TrimSpace(action.StatusHint)
	}
	p.worktreesModal.Loading = true
	p.worktreesModal.Error = ""
}

func (p *HomePage) handleWorktreesModalKey(ev *tcell.EventKey) {
	if p.worktreesModal.CreatingSession {
		p.handleWorktreeCreateKey(ev)
		return
	}
	if p.worktreesModal.EditingBranch {
		p.handleWorktreesBranchEditorKey(ev)
		return
	}
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideWorktreesModal()
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt), p.keybinds.Match(ev, KeybindModalFocusLeft):
		p.moveWorktreesModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt), p.keybinds.Match(ev, KeybindModalFocusRight), p.keybinds.Match(ev, KeybindModalFocusNext):
		p.moveWorktreesModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.triggerWorktreesModalSelection()
		return
	}

	if ev.Key() != tcell.KeyRune {
		return
	}
	switch strings.ToLower(string(ev.Rune())) {
	case "c":
		p.openWorktreesCreatedBranchEditor()
	case "b":
		p.openWorktreesBranchSourceEditor()
	case "r":
		p.enqueueWorktreesModalAction(WorktreesModalAction{
			Kind:       WorktreesModalActionRefresh,
			StatusHint: "Refreshing worktrees settings...",
		})
	}
}

func worktreeBranchSlug(value string) string {
	var out strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(r)
			pendingDash = false
		} else if out.Len() > 0 {
			pendingDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func (p *HomePage) handleWorktreeCreateKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideWorktreesModal()
		return
	case p.keybinds.Match(ev, KeybindModalFocusNext):
		p.worktreesModal.CreateField = (p.worktreesModal.CreateField + 1) % 2
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		title := strings.TrimSpace(p.worktreesModal.CreateTitle)
		branch := worktreeBranchSlug(p.worktreesModal.CreateBranch)
		if title == "" {
			p.worktreesModal.Error = "Title is required"
			return
		}
		if branch == "" {
			p.worktreesModal.Error = "Branch is required"
			return
		}
		p.enqueueWorktreesModalAction(WorktreesModalAction{
			Kind: WorktreesModalActionCreateSession, Title: title, BranchName: branch,
			StatusHint: "Creating worktree session...",
		})
		return
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		value := &p.worktreesModal.CreateTitle
		if p.worktreesModal.CreateField == 1 {
			value = &p.worktreesModal.CreateBranch
			p.worktreesModal.BranchOverridden = true
		}
		if len(*value) > 0 {
			_, size := utf8.DecodeLastRuneInString(*value)
			*value = (*value)[:len(*value)-size]
		}
		if p.worktreesModal.CreateField == 0 && !p.worktreesModal.BranchOverridden {
			p.worktreesModal.CreateBranch = worktreeBranchSlug(p.worktreesModal.CreateTitle)
		}
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		if p.worktreesModal.CreateField == 0 {
			p.worktreesModal.CreateTitle = ""
			if !p.worktreesModal.BranchOverridden {
				p.worktreesModal.CreateBranch = ""
			}
		} else {
			p.worktreesModal.CreateBranch = ""
			p.worktreesModal.BranchOverridden = true
		}
		return
	}
	if ev.Key() != tcell.KeyRune || !unicode.IsPrint(ev.Rune()) {
		return
	}
	if p.worktreesModal.CreateField == 0 {
		p.worktreesModal.CreateTitle += string(ev.Rune())
		if !p.worktreesModal.BranchOverridden {
			p.worktreesModal.CreateBranch = worktreeBranchSlug(p.worktreesModal.CreateTitle)
		}
	} else {
		p.worktreesModal.CreateBranch += string(ev.Rune())
		p.worktreesModal.BranchOverridden = true
	}
	p.worktreesModal.Error = ""
}

func (p *HomePage) handleWorktreesBranchEditorKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.worktreesModal.EditingBranch = false
		p.worktreesModal.EditorKind = worktreeEditorNone
		p.worktreesModal.BranchInput = ""
		p.worktreesModal.Status = "Worktree edit canceled"
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		value := strings.TrimSpace(p.worktreesModal.BranchInput)
		switch p.worktreesModal.EditorKind {
		case worktreeEditorCreatedBranch:
			nextBranch := displayWorktreeCreatedBranch(value)
			p.worktreesModal.EditingBranch = false
			p.worktreesModal.EditorKind = worktreeEditorNone
			p.enqueueWorktreesModalAction(WorktreesModalAction{
				Kind:       WorktreesModalActionSetCreatedBranch,
				BranchName: nextBranch,
				StatusHint: fmt.Sprintf("Updating created branch to %s...", nextBranch),
			})
			return
		case worktreeEditorBranchOffSource:
			base, useCurrent := normalizeBranchSourceInput(value)
			p.worktreesModal.EditingBranch = false
			p.worktreesModal.EditorKind = worktreeEditorNone
			p.enqueueWorktreesModalAction(WorktreesModalAction{
				Kind:       WorktreesModalActionSetBranchSource,
				BaseBranch: base,
				StatusHint: fmt.Sprintf("Updating branch-off source to %s...", worktreesModalBranchLabel(useCurrent, base)),
			})
			return
		default:
			p.worktreesModal.Status = "No worktree field selected"
			return
		}
	case p.keybinds.Match(ev, KeybindModalSearchBackspace):
		if len(p.worktreesModal.BranchInput) == 0 {
			return
		}
		_, size := utf8.DecodeLastRuneInString(p.worktreesModal.BranchInput)
		if size > 0 {
			p.worktreesModal.BranchInput = p.worktreesModal.BranchInput[:len(p.worktreesModal.BranchInput)-size]
		}
		return
	case p.keybinds.Match(ev, KeybindModalSearchClear):
		p.worktreesModal.BranchInput = ""
		return
	}
	if ev.Key() == tcell.KeyRune && unicode.IsPrint(ev.Rune()) {
		p.worktreesModal.BranchInput += string(ev.Rune())
	}
}

func (p *HomePage) moveWorktreesModalSelection(delta int) {
	if delta == 0 {
		return
	}
	next := p.worktreesModal.Selected + delta
	if next < 0 {
		next = 2
	}
	if next > 2 {
		next = 0
	}
	p.worktreesModal.Selected = next
}

func (p *HomePage) triggerWorktreesModalSelection() {
	switch p.worktreesModal.Selected {
	case 0:
		p.openWorktreesCreatedBranchEditor()
	case 1:
		p.openWorktreesBranchSourceEditor()
	default:
		p.enqueueWorktreesModalAction(WorktreesModalAction{
			Kind:       WorktreesModalActionRefresh,
			StatusHint: "Refreshing worktrees settings...",
		})
	}
}

func (p *HomePage) openWorktreesCreatedBranchEditor() {
	p.worktreesModal.EditingBranch = true
	p.worktreesModal.EditorKind = worktreeEditorCreatedBranch
	p.worktreesModal.BranchInput = normalizeWorktreeBranchPrefixInput(strings.TrimSpace(p.worktreesModal.Data.BranchName))
	if p.worktreesModal.BranchInput == "" {
		p.worktreesModal.BranchInput = defaultWorktreeBranchDisplay
	}
	p.worktreesModal.BranchInputHint = "Edit only the branch prefix. Swarm will create worktree branches as <prefix>/<id>. Default: agent/<id>"
	p.worktreesModal.Status = "Edit created branch and press Enter"
	p.worktreesModal.Error = ""
	p.worktreesModal.Loading = false
}

func (p *HomePage) openWorktreesBranchSourceEditor() {
	p.worktreesModal.EditingBranch = true
	p.worktreesModal.EditorKind = worktreeEditorBranchOffSource
	if p.worktreesModal.Data.UseCurrentBranch {
		p.worktreesModal.BranchInput = "current"
	} else {
		p.worktreesModal.BranchInput = strings.TrimSpace(p.worktreesModal.Data.BaseBranch)
	}
	p.worktreesModal.BranchInputHint = "enter 'current' to branch off the current branch, or a specific base branch name"
	p.worktreesModal.Status = "Edit branch-off source and press Enter"
	p.worktreesModal.Error = ""
	p.worktreesModal.Loading = false
}

const defaultWorktreeBranchDisplay = "agent"

func normalizeWorktreeBranchPrefixInput(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return defaultWorktreeBranchDisplay
	}
	if strings.EqualFold(trimmed, "agent/<id>") {
		return defaultWorktreeBranchDisplay
	}
	if strings.HasSuffix(trimmed, "/<id>") {
		trimmed = strings.TrimSuffix(trimmed, "/<id>")
		trimmed = strings.Trim(trimmed, "/")
	}
	if trimmed == "" {
		return defaultWorktreeBranchDisplay
	}
	return trimmed
}

func displayWorktreeCreatedBranch(value string) string {
	return normalizeWorktreeBranchPrefixInput(value)
}

func worktreesModalBranchLabel(useCurrentBranch bool, baseBranch string) string {
	if useCurrentBranch {
		return "current branch"
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "unset"
	}
	return strings.TrimSpace(baseBranch)
}

func worktreesModalResolvedBranchLabel(useCurrentBranch bool, baseBranch, resolvedBranch string) string {
	if useCurrentBranch {
		if branch := strings.TrimSpace(resolvedBranch); branch != "" {
			return branch
		}
		return "unknown"
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "unset"
	}
	return strings.TrimSpace(baseBranch)
}

func worktreesEditorTitle(kind worktreeEditorKind) string {
	switch kind {
	case worktreeEditorCreatedBranch:
		return "Created Branch Prefix"
	case worktreeEditorBranchOffSource:
		return "Branch-off Source"
	default:
		return "Worktree Setting"
	}
}

func normalizeBranchSourceInput(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "auto", "current", "current-branch", "current_branch":
		return "", true
	default:
		return trimmed, false
	}
}

func (p *HomePage) drawWorktreesModal(s tcell.Screen) {
	if !p.worktreesModal.Visible {
		return
	}
	w, h := s.Size()
	if w < 44 || h < 12 {
		return
	}
	modalW := minInt(76, w-6)
	modalH := minInt(19, h-4)
	if modalW < 62 {
		modalW = w - 2
	}
	if modalW < 44 {
		return
	}
	if modalH < 16 {
		modalH = h - 2
	}
	if modalH < 12 {
		return
	}
	rect := Rect{X: maxInt(1, (w-modalW)/2), Y: maxInt(1, (h-modalH)/2), W: modalW, H: modalH}

	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, p.theme.BorderActive)
	modalTitle := "Worktrees"
	if p.worktreesModal.CreatingSession {
		modalTitle = "New Worktree Session"
	}
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.Text, modalTitle)

	statusLine := strings.TrimSpace(p.worktreesModal.Status)
	statusStyle := p.theme.TextMuted
	if strings.TrimSpace(p.worktreesModal.Error) != "" {
		statusLine = strings.TrimSpace(p.worktreesModal.Error)
		statusStyle = p.theme.Error
	}
	if statusLine == "" {
		statusLine = "Enter: action  •  c edit created branch  •  b edit branch-off source  •  Esc close"
	}
	if p.worktreesModal.Loading && !p.worktreesModal.CreatingSession {
		statusLine = "loading worktrees settings..."
	}
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, statusStyle, clampEllipsis(statusLine, rect.W-4))

	if p.worktreesModal.CreatingSession {
		prefix := normalizeWorktreeBranchPrefixInput(p.worktreesModal.Data.BranchName)
		innerX := rect.X + 3
		innerW := rect.W - 6
		fieldW := innerW
		titleBox := Rect{X: innerX, Y: rect.Y + 5, W: fieldW, H: 3}
		branchBox := Rect{X: innerX, Y: rect.Y + 10, W: fieldW, H: 3}

		DrawText(s, innerX, rect.Y+2, innerW, p.theme.Text, "Start a focused session without touching your current checkout.")
		DrawText(s, innerX, rect.Y+3, innerW, p.theme.TextMuted, "Swarm creates the branch and opens the new session immediately.")

		titleBorder, branchBorder := p.theme.Border, p.theme.Border
		titleStyle, branchStyle := p.theme.Text, p.theme.Text
		if p.worktreesModal.CreateField == 0 {
			titleBorder = p.theme.BorderActive
			titleStyle = p.theme.Primary.Bold(true)
		} else {
			branchBorder = p.theme.BorderActive
			branchStyle = p.theme.Primary.Bold(true)
		}
		FillRect(s, titleBox, p.theme.Element)
		DrawBox(s, titleBox, titleBorder)
		DrawText(s, titleBox.X+2, titleBox.Y, titleBox.W-4, p.theme.TextMuted, " Session title ")
		titleValue := p.worktreesModal.CreateTitle
		if p.worktreesModal.CreateField == 0 {
			titleValue += "█"
		}
		if titleValue == "" {
			titleValue = "Describe the work"
			titleStyle = p.theme.TextMuted
		}
		DrawText(s, titleBox.X+2, titleBox.Y+1, titleBox.W-4, titleStyle, clampEllipsis(titleValue, titleBox.W-4))

		FillRect(s, branchBox, p.theme.Element)
		DrawBox(s, branchBox, branchBorder)
		DrawText(s, branchBox.X+2, branchBox.Y, branchBox.W-4, p.theme.TextMuted, " Branch ")
		branchDisplay := prefix + "/" + p.worktreesModal.CreateBranch
		if p.worktreesModal.CreateField == 1 {
			branchDisplay += "█"
		}
		DrawText(s, branchBox.X+2, branchBox.Y+1, branchBox.W-4, branchStyle, clampEllipsis(branchDisplay, branchBox.W-4))

		DrawText(s, innerX, rect.Y+14, innerW, p.theme.TextMuted, "Branch is suggested from the title. Press Tab to customize it.")
		DrawText(s, innerX, rect.Y+16, innerW, p.theme.Secondary.Bold(true), "Enter  Create & open")
		DrawTextRight(s, innerX+innerW-1, rect.Y+16, minInt(30, innerW), p.theme.TextMuted, "Tab  Switch field   Esc  Cancel")
		return
	}

	data := p.worktreesModal.Data
	resolvedBranch := worktreesModalResolvedBranchLabel(data.UseCurrentBranch, strings.TrimSpace(data.BaseBranch), strings.TrimSpace(data.ResolvedBranch))
	createdBranch := strings.TrimSpace(data.BranchName)
	if createdBranch == "" {
		createdBranch = defaultWorktreeBranchDisplay
	}
	branchSource := worktreesModalBranchLabel(data.UseCurrentBranch, strings.TrimSpace(data.BaseBranch))
	compact := rect.W < 64
	DrawText(s, rect.X+2, rect.Y+3, rect.W-4, p.theme.TextMuted, "Workspace:")
	DrawText(s, rect.X+13, rect.Y+3, rect.W-15, p.theme.Primary, clampEllipsis(strings.TrimSpace(data.WorkspacePath), rect.W-15))
	DrawText(s, rect.X+2, rect.Y+5, rect.W-4, p.theme.TextMuted, "Created branch prefix:")
	DrawText(s, rect.X+25, rect.Y+5, rect.W-27, p.theme.Primary, clampEllipsis(createdBranch+"/<id>", rect.W-27))
	DrawText(s, rect.X+2, rect.Y+6, rect.W-4, p.theme.TextMuted, "Branches off from:")
	DrawText(s, rect.X+20, rect.Y+6, rect.W-22, p.theme.Primary, clampEllipsis(branchSource, rect.W-22))
	DrawText(s, rect.X+2, rect.Y+7, rect.W-4, p.theme.TextMuted, "Resolved source:")
	DrawText(s, rect.X+19, rect.Y+7, rect.W-21, p.theme.Success, clampEllipsis(resolvedBranch, rect.W-21))

	updatedText := "never"
	if data.UpdatedAt > 0 {
		updatedText = time.UnixMilli(data.UpdatedAt).Format(time.RFC3339)
	}
	DrawText(s, rect.X+2, rect.Y+8, rect.W-4, p.theme.TextMuted, "Updated: "+updatedText)

	actions := []struct {
		Label string
		Key   string
	}{
		{Label: "Edit Created Branch Prefix", Key: "c"},
		{Label: "Edit Branch-off Source", Key: "b"},
		{Label: "Refresh", Key: "r"},
	}
	actionsY := rect.Y + 10
	for i, action := range actions {
		style := p.theme.TextMuted
		if i == p.worktreesModal.Selected {
			style = p.theme.Primary
		}
		label := action.Label
		if compact {
			label = action.Key + ": " + action.Label
		}
		DrawText(s, rect.X+4, actionsY+i, rect.W-8, style, clampEllipsis(label, rect.W-8))
	}

	if p.worktreesModal.EditingBranch {
		editorW := maxInt(44, minInt(rect.W-8, 76))
		editorH := 7
		editor := Rect{
			X: rect.X + (rect.W-editorW)/2,
			Y: rect.Y + rect.H - editorH - 2,
			W: editorW,
			H: editorH,
		}
		FillRect(s, editor, p.theme.Panel)
		DrawBox(s, editor, p.theme.Border)
		DrawText(s, editor.X+2, editor.Y, editor.W-4, p.theme.Text, worktreesEditorTitle(p.worktreesModal.EditorKind))
		help := strings.TrimSpace(p.worktreesModal.BranchInputHint)
		if help == "" {
			help = "Enter branch name and press Enter"
		}
		DrawText(s, editor.X+2, editor.Y+1, editor.W-4, p.theme.TextMuted, clampEllipsis(help, editor.W-4))
		value := strings.TrimSpace(p.worktreesModal.BranchInput)
		DrawText(s, editor.X+2, editor.Y+3, editor.W-4, p.theme.Primary, clampEllipsis(value+"█", editor.W-4))
		DrawText(s, editor.X+2, editor.Y+5, editor.W-4, p.theme.TextMuted, clampEllipsis("Enter save  •  Esc cancel  •  Backspace delete", editor.W-4))
	}
}

package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

type chatManageSessionsProposal struct {
	ID                 string
	Title              string
	Prompt             string
	Mode               string
	AgentName          string
	AgentMode          string
	WorkspacePath      string
	WorkspaceName      string
	ManagedWorktree    bool
	WorktreeBaseBranch string
	WorktreeBranch     string
}

func (p *ChatPage) OpenManageSessionsPermissionModal(record ChatPermissionRecord) bool {
	if classifyChatPermission(record) != chatPermissionDestinationManageSessionsModal {
		return false
	}
	payload := decodePermissionArguments(record.ToolArguments)
	approved := canonicalPermissionApprovedArguments(record)

	action := manageSessionsActionForRequirement(record.Requirement)
	proposals := parseManageSessionsProposals(payload)
	selected := make([]bool, len(proposals))
	for i := range proposals {
		if manageSessionsProposalValid(proposals[i]) {
			selected[i] = true
			break
		}
	}

	p.manageSessionsPermission = strings.TrimSpace(record.ID)
	p.manageSessionsAction = action
	p.manageSessionsApproved = approved
	p.manageSessionsProposals = proposals
	p.manageSessionsSelected = selected
	p.manageSessionsFocus = firstManageSessionsValidProposal(proposals)
	p.manageSessionsScroll = 0
	p.manageSessionsMaxScroll = 0
	p.manageSessionsApproveRect = Rect{}
	p.manageSessionsDenyRect = Rect{}
	p.manageSessionsProposalRects = nil
	p.manageSessionsProposalIndexes = nil
	p.statusLine = "manage-sessions permission active"
	return p.manageSessionsPermission != ""
}

func (p *ChatPage) manageSessionsPermissionModalActive() bool {
	return strings.TrimSpace(p.manageSessionsPermission) != ""
}

func (p *ChatPage) closeManageSessionsPermissionModal() {
	p.manageSessionsPermission = ""
	p.manageSessionsAction = ""
	p.manageSessionsApproved = ""
	p.manageSessionsProposals = nil
	p.manageSessionsSelected = nil
	p.manageSessionsFocus = 0
	p.manageSessionsScroll = 0
	p.manageSessionsMaxScroll = 0
	p.manageSessionsApproveRect = Rect{}
	p.manageSessionsDenyRect = Rect{}
	p.manageSessionsProposalRects = nil
	p.manageSessionsProposalIndexes = nil
}

func (p *ChatPage) handleManageSessionsPermissionModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.manageSessionsPermissionModalActive() {
		return false
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolveManageSessionsPermissionModal(false)
	case tcell.KeyEnter:
		p.resolveManageSessionsPermissionModal(true)
	case tcell.KeyUp:
		if p.manageSessionsAction == "deploy" {
			p.shiftManageSessionsProposalFocus(-1)
		} else {
			p.shiftManageSessionsPermissionScroll(-1)
		}
	case tcell.KeyDown:
		if p.manageSessionsAction == "deploy" {
			p.shiftManageSessionsProposalFocus(1)
		} else {
			p.shiftManageSessionsPermissionScroll(1)
		}
	case tcell.KeyPgUp:
		p.shiftManageSessionsPermissionScroll(-6)
	case tcell.KeyPgDn:
		p.shiftManageSessionsPermissionScroll(6)
	case tcell.KeyHome:
		p.manageSessionsScroll = 0
	case tcell.KeyEnd:
		p.manageSessionsScroll = p.manageSessionsMaxScroll
	case tcell.KeyRune:
		if p.manageSessionsAction == "deploy" && ev.Rune() == ' ' {
			p.toggleManageSessionsProposal(p.manageSessionsFocus)
		} else if ev.Rune() == 'a' || ev.Rune() == 'A' {
			p.resolveManageSessionsPermissionModal(true)
		} else if ev.Rune() == 'd' || ev.Rune() == 'D' {
			p.resolveManageSessionsPermissionModal(false)
		}
	}
	return true
}

func (p *ChatPage) handleManageSessionsPermissionModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.manageSessionsPermissionModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.manageSessionsApproveRect.Contains(x, y):
			p.resolveManageSessionsPermissionModal(true)
		case p.manageSessionsDenyRect.Contains(x, y):
			p.resolveManageSessionsPermissionModal(false)
		default:
			for i, rect := range p.manageSessionsProposalRects {
				if !rect.Contains(x, y) || i >= len(p.manageSessionsProposalIndexes) {
					continue
				}
				idx := p.manageSessionsProposalIndexes[i]
				p.manageSessionsFocus = idx
				p.toggleManageSessionsProposal(idx)
				break
			}
		}
		return true
	}
	if buttons&tcell.WheelUp != 0 {
		p.shiftManageSessionsPermissionScroll(-1)
	} else if buttons&tcell.WheelDown != 0 {
		p.shiftManageSessionsPermissionScroll(1)
	}
	return true
}

func (p *ChatPage) shiftManageSessionsPermissionScroll(delta int) {
	p.manageSessionsScroll += delta
	if p.manageSessionsScroll < 0 {
		p.manageSessionsScroll = 0
	}
	if p.manageSessionsScroll > p.manageSessionsMaxScroll {
		p.manageSessionsScroll = p.manageSessionsMaxScroll
	}
}

func (p *ChatPage) shiftManageSessionsProposalFocus(delta int) {
	if len(p.manageSessionsProposals) == 0 || delta == 0 {
		return
	}
	next := p.manageSessionsFocus + delta
	if next < 0 {
		next = len(p.manageSessionsProposals) - 1
	} else if next >= len(p.manageSessionsProposals) {
		next = 0
	}
	p.manageSessionsFocus = next
}

func (p *ChatPage) toggleManageSessionsProposal(index int) {
	if index < 0 || index >= len(p.manageSessionsSelected) || index >= len(p.manageSessionsProposals) {
		return
	}
	if !manageSessionsProposalValid(p.manageSessionsProposals[index]) {
		p.statusLine = "deployment proposal is incomplete"
		return
	}
	if p.manageSessionsSelected[index] && p.manageSessionsSelectedCount() == 1 {
		p.statusLine = "at least one deployment proposal must remain selected"
		return
	}
	p.manageSessionsSelected[index] = !p.manageSessionsSelected[index]
}

func (p *ChatPage) manageSessionsSelectedCount() int {
	count := 0
	for _, selected := range p.manageSessionsSelected {
		if selected {
			count++
		}
	}
	return count
}

func (p *ChatPage) drawManageSessionsPermissionModal(s tcell.Screen, screen Rect) {
	if !p.manageSessionsPermissionModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.manageSessionsPermission)
	if !ok {
		p.closeManageSessionsPermissionModal()
		return
	}
	modalW := screen.W - 8
	if modalW > 112 {
		modalW = 112
	}
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	lines, proposalLines := p.manageSessionsPermissionModalLines(record, modalW-4)
	modalH := len(lines) + 7
	if modalH < 14 {
		modalH = 14
	}
	if maxH := screen.H - 4; modalH > maxH {
		modalH = maxH
	}
	if modalH < 12 {
		return
	}
	modal := Rect{X: maxInt(1, (screen.W-modalW)/2), Y: maxInt(1, (screen.H-modalH)/2), W: modalW, H: modalH}
	p.manageSessionsApproveRect = Rect{}
	p.manageSessionsDenyRect = Rect{}
	p.manageSessionsProposalRects = nil
	p.manageSessionsProposalIndexes = nil

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(manageSessionsModalTitle(p.manageSessionsAction), modal.W-4))
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Dedicated manage-sessions approval · "+record.ID, modal.W-4))

	contentTop := modal.Y + 3
	contentH := modal.H - 6
	if contentH < 1 {
		contentH = 1
	}
	p.manageSessionsMaxScroll = maxInt(0, len(lines)-contentH)
	p.shiftManageSessionsPermissionScroll(0)
	for row := 0; row < contentH; row++ {
		idx := p.manageSessionsScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, modal.X+2, contentTop+row, modal.W-4, lines[idx])
		if proposalIndex := proposalLines[idx]; proposalIndex >= 0 {
			p.manageSessionsProposalRects = append(p.manageSessionsProposalRects, Rect{X: modal.X + 2, Y: contentTop + row, W: modal.W - 4, H: 1})
			p.manageSessionsProposalIndexes = append(p.manageSessionsProposalIndexes, proposalIndex)
		}
	}

	help := "a/Enter approve  •  d/Esc deny  •  ↑/↓ scroll"
	if p.manageSessionsAction == "deploy" {
		help = "↑/↓ choose  •  Space select  •  a/Enter approve  •  d/Esc deny"
	}
	helpY := modal.Y + modal.H - 3
	DrawText(s, modal.X+2, helpY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(help, modal.W-4))

	denyLabel := "d Deny"
	approveLabel := "a Approve"
	if p.manageSessionsAction == "deploy" {
		approveLabel = fmt.Sprintf("a Deploy %d", p.manageSessionsSelectedCount())
	}
	denyW := utf8.RuneCountInString(denyLabel) + 2
	approveW := utf8.RuneCountInString(approveLabel) + 2
	startX := modal.X + (modal.W-denyW-2-approveW)/2
	buttonY := modal.Y + modal.H - 2
	FillRect(s, Rect{X: startX, Y: buttonY, W: denyW, H: 1}, filledButtonStyle(p.theme.Warning))
	DrawCenteredText(s, startX, buttonY, denyW, filledButtonStyle(p.theme.Warning), denyLabel)
	approveX := startX + denyW + 2
	FillRect(s, Rect{X: approveX, Y: buttonY, W: approveW, H: 1}, filledButtonStyle(p.theme.Success))
	DrawCenteredText(s, approveX, buttonY, approveW, filledButtonStyle(p.theme.Success), approveLabel)
	p.manageSessionsDenyRect = Rect{X: startX, Y: buttonY, W: denyW, H: 1}
	p.manageSessionsApproveRect = Rect{X: approveX, Y: buttonY, W: approveW, H: 1}
}

func (p *ChatPage) manageSessionsPermissionModalLines(record ChatPermissionRecord, width int) ([]chatRenderLine, []int) {
	if width < 8 {
		width = 8
	}
	lines := make([]chatRenderLine, 0, 48)
	proposalLines := make([]int, 0, 48)
	appendLine := func(text string, style tcell.Style, proposalIndex int) {
		rows := wrapWithCustomPrefixes("", "", strings.TrimSpace(text), width)
		if len(rows) == 0 {
			rows = []string{""}
		}
		for _, row := range rows {
			lines = append(lines, chatRenderLine{Text: row, Style: styleForCurrentCellBackground(style)})
			proposalLines = append(proposalLines, proposalIndex)
		}
	}
	appendField := func(label, value string, proposalIndex int) {
		if strings.TrimSpace(value) != "" {
			appendLine("  "+label+": "+strings.TrimSpace(value), p.theme.Text, proposalIndex)
		}
	}
	payload := decodePermissionArguments(record.ToolArguments)
	switch p.manageSessionsAction {
	case "deploy":
		appendLine("Select the exact sessions to deploy. One safe valid proposal is selected by default.", p.theme.TextMuted, -1)
		for i, proposal := range p.manageSessionsProposals {
			appendLine("", p.theme.Text, -1)
			marker := "[ ]"
			if i < len(p.manageSessionsSelected) && p.manageSessionsSelected[i] {
				marker = "[x]"
			}
			focus := " "
			if i == p.manageSessionsFocus {
				focus = ">"
			}
			title := firstNonEmptyToolValue(proposal.Title, "Untitled proposal")
			appendLine(fmt.Sprintf("%s %s Proposal %d: %s", focus, marker, i+1, title), p.theme.Primary.Bold(true), i)
			appendField("Prompt", proposal.Prompt, i)
			appendField("Mode", proposal.Mode, i)
			appendField("Agent", strings.TrimSpace(proposal.AgentName+" "+proposal.AgentMode), i)
			appendField("Workspace", firstNonEmptyToolValue(proposal.WorkspaceName, proposal.WorkspacePath), i)
			if proposal.WorkspaceName != "" && proposal.WorkspacePath != "" {
				appendField("Workspace path", proposal.WorkspacePath, i)
			}
			worktree := "No managed worktree"
			if proposal.ManagedWorktree {
				worktree = "Managed worktree"
				if proposal.WorktreeBaseBranch != "" || proposal.WorktreeBranch != "" {
					worktree += " · " + strings.TrimSpace(proposal.WorktreeBaseBranch+" → "+proposal.WorktreeBranch)
				}
			}
			appendField("Worktree", worktree, i)
			if !manageSessionsProposalValid(proposal) {
				appendField("Availability", "Incomplete canonical proposal; cannot select", i)
			}
		}
	case "commit":
		commits := jsonObjectSlice(planPermissionObject(payload["manifest"]), "commits")
		appendLine(fmt.Sprintf("Review %d commit entries.", len(commits)), p.theme.TextMuted, -1)
		for i, commit := range commits {
			appendLine("", p.theme.Text, -1)
			appendLine(fmt.Sprintf("Commit %d", i+1), p.theme.Primary.Bold(true), -1)
			appendField("Message", mapStringArg(commit, "message"), -1)
			appendField("Repository", mapStringArg(commit, "repository"), -1)
			files := manageSessionsCommitFiles(commit["files"])
			if len(files) > 0 {
				appendField("Files", strings.Join(files, ", "), -1)
			}
		}
	case "archive", "unarchive":
		sessions := jsonObjectSlice(payload, "sessions")
		appendLine(fmt.Sprintf("Review %d sessions to %s.", len(sessions), p.manageSessionsAction), p.theme.TextMuted, -1)
		for i, session := range sessions {
			appendLine("", p.theme.Text, -1)
			appendLine(fmt.Sprintf("Session %d: %s", i+1, firstNonEmptyToolValue(mapStringArg(session, "title"), "Untitled session")), p.theme.Primary.Bold(true), -1)
			appendField("Workspace", firstNonEmptyToolValue(mapStringArg(session, "workspace_name"), "Unknown workspace"), -1)
			appendField("State", strings.ReplaceAll(firstNonEmptyToolValue(mapStringArg(session, "state"), "unknown"), "_", " "), -1)
			if updated := manageSessionsInt64(session["updated_at"]); updated > 0 {
				appendField("Updated", time.UnixMilli(updated).Local().Format("2006-01-02 15:04"), -1)
			}
		}
	}
	if len(lines) == 0 {
		appendLine("No structured manage-sessions content was provided.", p.theme.TextMuted, -1)
	}
	return lines, proposalLines
}

func (p *ChatPage) resolveManageSessionsPermissionModal(approve bool) {
	permissionID := strings.TrimSpace(p.manageSessionsPermission)
	approvedArguments := ""
	if approve {
		approvedArguments = p.manageSessionsPermissionApprovedArguments()
		if approvedArguments == "" {
			if p.manageSessionsAction == "deploy" && p.manageSessionsSelectedCount() == 0 {
				p.statusLine = "select at least one valid deployment proposal"
			} else {
				p.statusLine = "manage-sessions approval arguments unavailable"
			}
			return
		}
	}
	action := p.manageSessionsAction
	p.closeManageSessionsPermissionModal()
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", "", approvedArguments)
		p.statusLine = "session " + action + " approved"
	} else {
		p.queueResolvePermissionByID(permissionID, "deny", "")
		p.statusLine = "session " + action + " denied"
	}
}

func (p *ChatPage) manageSessionsPermissionApprovedArguments() string {
	var args map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(p.manageSessionsApproved)), &args) != nil || args == nil {
		return ""
	}
	if p.manageSessionsAction == "deploy" {
		selected := make([]string, 0, p.manageSessionsSelectedCount())
		for i, chosen := range p.manageSessionsSelected {
			if !chosen || i >= len(p.manageSessionsProposals) || !manageSessionsProposalValid(p.manageSessionsProposals[i]) {
				continue
			}
			selected = append(selected, p.manageSessionsProposals[i].ID)
		}
		if len(selected) == 0 {
			return ""
		}
		args["selected_proposal_ids"] = selected
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(raw)
}

func manageSessionsActionForRequirement(requirement string) string {
	switch strings.ToLower(strings.TrimSpace(requirement)) {
	case "session_deploy":
		return "deploy"
	case "session_commit":
		return "commit"
	case "session_unarchive":
		return "unarchive"
	default:
		return "archive"
	}
}

func manageSessionsModalTitle(action string) string {
	switch action {
	case "deploy":
		return "Deploy sessions?"
	case "commit":
		return "Commit session changes?"
	case "unarchive":
		return "Unarchive sessions?"
	default:
		return "Archive sessions?"
	}
}

func parseManageSessionsProposals(payload map[string]any) []chatManageSessionsProposal {
	items := jsonObjectSlice(payload, "proposals")
	if len(items) > 8 {
		items = items[:8]
	}
	out := make([]chatManageSessionsProposal, 0, len(items))
	for _, item := range items {
		managed, _ := manageTodosBoolArg(item, "managed_worktree")
		out = append(out, chatManageSessionsProposal{
			ID:                 mapStringArg(item, "id"),
			Title:              mapStringArg(item, "title"),
			Prompt:             mapStringArg(item, "prompt"),
			Mode:               strings.ToLower(mapStringArg(item, "mode")),
			AgentName:          mapStringArg(item, "agent_name"),
			AgentMode:          mapStringArg(item, "agent_mode"),
			WorkspacePath:      mapStringArg(item, "workspace_path"),
			WorkspaceName:      mapStringArg(item, "workspace_name"),
			ManagedWorktree:    managed,
			WorktreeBaseBranch: mapStringArg(item, "worktree_base_branch"),
			WorktreeBranch:     mapStringArg(item, "worktree_branch"),
		})
	}
	return out
}

func manageSessionsProposalValid(proposal chatManageSessionsProposal) bool {
	return proposal.ID != "" && proposal.Prompt != "" && proposal.AgentName != "" && proposal.WorkspacePath != "" && (proposal.Mode == "plan" || proposal.Mode == "auto")
}

func firstManageSessionsValidProposal(proposals []chatManageSessionsProposal) int {
	for i := range proposals {
		if manageSessionsProposalValid(proposals[i]) {
			return i
		}
	}
	return 0
}

func manageSessionsCommitFiles(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	files := make([]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if path := strings.TrimSpace(typed); path != "" {
				files = append(files, path)
			}
		case map[string]any:
			if path := mapStringArg(typed, "path"); path != "" {
				files = append(files, path)
			}
		}
	}
	return files
}

func manageSessionsInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

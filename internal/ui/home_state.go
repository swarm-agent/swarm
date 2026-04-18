package ui

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

func (p *HomePage) registerTopTarget(rect Rect, action string, index int) {
	if action == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.topBarTargets = append(p.topBarTargets, clickTarget{
		Rect:   rect,
		Action: action,
		Index:  index,
	})
}

func (p *HomePage) registerBottomTarget(rect Rect, action string, index int) {
	if action == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.bottomBarTargets = append(p.bottomBarTargets, clickTarget{
		Rect:   rect,
		Action: action,
		Index:  index,
	})
}

func (p *HomePage) topTargetAt(x, y int) (clickTarget, bool) {
	for _, target := range p.topBarTargets {
		if target.Rect.Contains(x, y) {
			return target, true
		}
	}
	return clickTarget{}, false
}

func (p *HomePage) commandPaletteTargetAt(x, y int) (clickTarget, bool) {
	for _, target := range p.commandPaletteTargets {
		if target.Rect.Contains(x, y) {
			return target, true
		}
	}
	return clickTarget{}, false
}

func (p *HomePage) activateCommandPaletteTarget(target clickTarget) bool {
	switch target.Action {
	case "palette-option":
		selected, ok := p.selectedCommandSuggestion()
		if !ok {
			return false
		}
		p.commandPaletteOptionOwner = selected.Command
		p.commandPaletteOptionIndex = target.Index
		return p.completeCommandFromPalette()
	case "palette-row":
		p.commandPaletteIndex = target.Index
		p.resetCommandPaletteOptionSelection()
		return p.completeCommandFromPalette()
	default:
		p.commandPaletteIndex = target.Index
		return p.completeCommandFromPalette()
	}
}

func (p *HomePage) moveSelection(delta int) {
	if !p.sessionsFocused {
		return
	}
	total := len(p.model.RecentSessions)
	if total == 0 {
		return
	}
	next := p.selectedIndex + delta
	if next < 0 {
		next = 0
	}
	if next >= total {
		next = total - 1
	}
	p.selectedIndex = next
	p.syncPageFromSelection(p.recentPageSize)
}

func (p *HomePage) enterSessionsMode() {
	total := len(p.model.RecentSessions)
	if total == 0 {
		return
	}
	if p.selectedIndex >= total || p.selectedIndex < 0 {
		p.selectedIndex = 0
	}
	p.sessionsFocused = true
	p.syncPageFromSelection(p.recentPageSize)
}

func (p *HomePage) exitSessionsMode() {
	p.sessionsFocused = false
}

func (p *HomePage) shiftPage(delta int) {
	if !p.sessionsFocused {
		return
	}
	total := len(p.model.RecentSessions)
	if total == 0 {
		return
	}
	pageSize := p.recentPageSize
	if pageSize < 1 {
		pageSize = recentVisibleRows
	}
	maxStart := total - pageSize
	if maxStart < 0 {
		maxStart = 0
	}
	nextStart := p.recentPage + (delta * pageSize)
	if nextStart < 0 {
		nextStart = 0
	}
	if nextStart > maxStart {
		nextStart = maxStart
	}
	if nextStart == p.recentPage {
		return
	}

	p.recentPage = nextStart
	pageEnd := p.recentPage + pageSize
	if p.selectedIndex < p.recentPage || p.selectedIndex >= pageEnd {
		p.selectedIndex = p.recentPage
		if p.selectedIndex >= total {
			p.selectedIndex = total - 1
		}
	}
	p.statusLine = ""
}

func (p *HomePage) handleTopTarget(target clickTarget) {
	switch target.Action {
	case "workspace-add-hint":
		p.prompt = "/workspace"
		p.pasteBuffer = p.pasteBuffer[:0]
		p.lastPasteBatchSize = 0
		p.statusLine = "press Enter to open workspace manager"
		return
	case "workspace":
		if target.Index >= 0 && target.Index < len(p.model.Workspaces) {
			p.queueSelectWorkspaceAction(p.model.Workspaces[target.Index])
		} else {
			p.statusLine = "workspace unavailable"
		}
		return
	}

	p.selectedTopAction = target.Action
	switch target.Action {
	case "open-agents-modal":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionOpenAgentsModal}
		p.statusLine = "opening agents manager..."
	case "open-models-modal":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionOpenModelsModal}
		p.statusLine = "opening model manager..."
	case "cycle-thinking":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionCycleThinking}
		p.statusLine = "cycling thinking level..."
	case "open-git", "open-dirty":
		d := p.primaryDirectory()
		p.statusLine = homeGitStatusText(d)
	case "open-agents":
		d := p.primaryDirectory()
		if d.AgentsToken == "" || d.AgentsToken == "-" || d.AgentsToken == "none" {
			p.statusLine = "context sources: no AGENTS.md/CLAUDE.md in scope"
		} else {
			p.statusLine = fmt.Sprintf("context sources: %s", d.AgentsToken)
		}
	case "open-plan":
		p.statusLine = fmt.Sprintf("active plan: %s", p.activePlanName())
	case "open-mode":
		p.pendingHomeAction = &HomeAction{Kind: HomeActionSetDefaultSessionMode, SessionMode: nextHomeSessionMode(p.sessionMode)}
		p.statusLine = fmt.Sprintf("default new chat mode: %s", nextHomeSessionMode(p.sessionMode))
	case "open-workspace":
		d := p.primaryDirectory()
		if d.IsWorkspace {
			p.statusLine = fmt.Sprintf("workspace path: %s", d.Path)
		} else {
			p.statusLine = fmt.Sprintf("directory path: %s (run /workspace to create one)", d.Path)
		}
	}
}

func (p *HomePage) syncPageFromSelection(pageSize int) {
	total := len(p.model.RecentSessions)
	if total == 0 {
		p.recentPage = 0
		return
	}
	if pageSize < 1 {
		pageSize = recentVisibleRows
	}

	maxStart := total - pageSize
	if maxStart < 0 {
		maxStart = 0
	}
	if p.recentPage < 0 {
		p.recentPage = 0
	}
	if p.recentPage > maxStart {
		p.recentPage = maxStart
	}
	if p.selectedIndex < p.recentPage {
		p.recentPage = p.selectedIndex
	}
	if p.selectedIndex >= p.recentPage+pageSize {
		p.recentPage = p.selectedIndex - pageSize + 1
	}
	if p.recentPage > maxStart {
		p.recentPage = maxStart
	}
}

func (p *HomePage) totalPages(pageSize int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	total := len(p.model.RecentSessions)
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	if pages == 0 {
		return 1
	}
	return pages
}

func (p *HomePage) activeWorkspaceName() string {
	idx := p.activeWorkspaceIndex()
	if idx >= 0 && idx < len(p.model.Workspaces) {
		return p.model.Workspaces[idx].Name
	}
	return ""
}

func (p *HomePage) activeWorkspaceIndex() int {
	for i, ws := range p.model.Workspaces {
		if ws.Active {
			return i
		}
	}
	return -1
}

func (p *HomePage) primaryDirectory() model.DirectoryItem {
	contextPath := strings.TrimSpace(p.model.CWD)
	if contextPath != "" {
		for _, dir := range p.model.Directories {
			if strings.TrimSpace(dir.ResolvedPath) == contextPath {
				return dir
			}
		}
	}
	active := p.activeWorkspaceIndex()
	if active >= 0 && active < len(p.model.Workspaces) {
		activePath := strings.TrimSpace(p.model.Workspaces[active].Path)
		for _, dir := range p.model.Directories {
			if strings.TrimSpace(dir.ResolvedPath) == activePath {
				return dir
			}
		}
	}
	if len(p.model.Directories) > 0 {
		return p.model.Directories[0]
	}
	return model.DirectoryItem{Name: "directory", Path: ".", ResolvedPath: ".", Branch: "-", IsWorkspace: false}
}

func (p *HomePage) contextDisplayName() string {
	d := p.primaryDirectory()
	if d.IsWorkspace {
		if name := strings.TrimSpace(p.activeWorkspaceName()); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(d.Name); name != "" {
		return name
	}
	if d.IsWorkspace {
		return "workspace"
	}
	return "directory"
}

func (p *HomePage) activeWorkspace() (model.Workspace, bool) {
	idx := p.activeWorkspaceIndex()
	if idx >= 0 && idx < len(p.model.Workspaces) {
		return p.model.Workspaces[idx], true
	}
	return model.Workspace{}, false
}

func (p *HomePage) activeWorkspaceLinkedDirectories() []string {
	workspace, ok := p.activeWorkspace()
	if !ok {
		return nil
	}
	out := make([]string, 0, len(workspace.Directories))
	workspacePath := strings.TrimSpace(workspace.Path)
	for _, directory := range workspace.Directories {
		directory = strings.TrimSpace(directory)
		if directory == "" || directory == workspacePath {
			continue
		}
		out = append(out, directory)
	}
	return out
}

func (p *HomePage) activePlanName() string {
	if p.model.ActivePlan != "" {
		return p.model.ActivePlan
	}
	return "none"
}

func clampTail(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[len(runes)-width:])
}

func clampEllipsis(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func displayRuntimeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "single":
		return "local"
	case "box":
		return "box"
	case "":
		return "local"
	default:
		return strings.TrimSpace(mode)
	}
}

func runtimeModeDescription(mode string, bypassPermissions bool) string {
	base := ""
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "single", "":
		base = "local (single-user daemon)"
	case "box":
		base = "box (network mode)"
	default:
		base = displayRuntimeMode(mode)
	}
	if bypassPermissions {
		return base + " · bypass permissions"
	}
	return base
}

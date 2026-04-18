package ui

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

type HomeActionKind string

const (
	HomeActionOpenSession           HomeActionKind = "open-session"
	HomeActionOpenAgentsModal       HomeActionKind = "open-agents-modal"
	HomeActionOpenModelsModal       HomeActionKind = "open-models-modal"
	HomeActionCycleThinking         HomeActionKind = "cycle-thinking"
	HomeActionSelectWorkspace       HomeActionKind = "select-workspace"
	HomeActionSetDefaultSessionMode HomeActionKind = "set-default-session-mode"
)

type HomeAction struct {
	Kind             HomeActionKind
	SessionID        string
	SessionTitle     string
	SessionMode      string
	WorkspacePath    string
	WorkspaceName    string
	WorktreeBranch   string
	WorktreeEnabled  bool
	WorktreeRootPath string
}

func (p *HomePage) PopHomeAction() (HomeAction, bool) {
	if p.pendingHomeAction == nil {
		return HomeAction{}, false
	}
	action := *p.pendingHomeAction
	p.pendingHomeAction = nil
	return action, true
}

func (p *HomePage) queueOpenSessionAction(session model.SessionSummary) {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = "session"
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		p.statusLine = fmt.Sprintf("cannot open session: missing id for %s", title)
		return
	}
	p.pendingHomeAction = &HomeAction{
		Kind:             HomeActionOpenSession,
		SessionID:        sessionID,
		SessionTitle:     title,
		SessionMode:      strings.TrimSpace(session.Mode),
		WorkspacePath:    strings.TrimSpace(session.WorkspacePath),
		WorkspaceName:    strings.TrimSpace(session.WorkspaceName),
		WorktreeBranch:   strings.TrimSpace(session.WorktreeBranch),
		WorktreeEnabled:  session.WorktreeEnabled,
		WorktreeRootPath: strings.TrimSpace(session.WorktreeRootPath),
	}
	p.statusLine = fmt.Sprintf("open session: %s", title)
}

func (p *HomePage) queueSelectWorkspaceAction(workspace model.Workspace) {
	path := strings.TrimSpace(workspace.Path)
	name := strings.TrimSpace(workspace.Name)
	if path == "" {
		p.statusLine = "cannot switch workspace: missing path"
		return
	}
	if name == "" {
		name = path
	}
	p.pendingHomeAction = &HomeAction{
		Kind:          HomeActionSelectWorkspace,
		WorkspacePath: path,
		WorkspaceName: name,
	}
	p.statusLine = fmt.Sprintf("switch workspace: %s", name)
}

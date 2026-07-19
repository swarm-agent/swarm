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
	HomeActionOpenProfilesModal     HomeActionKind = "open-profiles-modal"
	HomeActionSelectModelProfile    HomeActionKind = "select-model-profile"
	HomeActionRefreshCodexUsage     HomeActionKind = "refresh-codex-usage"
	HomeActionConsumeCodexReset     HomeActionKind = "consume-codex-reset"
	HomeActionCycleThinking         HomeActionKind = "cycle-thinking"
	HomeActionCycleRoute            HomeActionKind = "cycle-route"
	HomeActionOpenWorkspaceSelector HomeActionKind = "open-workspace-selector"
	HomeActionSetDefaultSessionMode HomeActionKind = "set-default-session-mode"
	HomeActionOpenAlertSession      HomeActionKind = "open-alert-session"
	HomeActionClearAlerts           HomeActionKind = "clear-alerts"
	HomeActionOpenAuthModal         HomeActionKind = "open-auth-modal"
	HomeActionSaveOnboarding        HomeActionKind = "save-onboarding"
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
	NotificationID   string
	Username         string
	SwarmName        string
	ModelProfileID   string
	ResetCreditID    string
	IdempotencyKey   string
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

func (p *HomePage) QueueSelectModelProfile(profileID string) bool {
	profileID = strings.TrimSpace(profileID)
	if p == nil || profileID == "" {
		return false
	}
	p.pendingHomeAction = &HomeAction{Kind: HomeActionSelectModelProfile, ModelProfileID: profileID}
	p.statusLine = "selecting profile..."
	return true
}

func (p *HomePage) QueueCodexUsageRefresh() {
	if p == nil {
		return
	}
	p.pendingHomeAction = &HomeAction{Kind: HomeActionRefreshCodexUsage}
	p.statusLine = "refreshing Codex account usage..."
}

func (p *HomePage) QueueCodexResetCredit(creditID, idempotencyKey string) bool {
	creditID = strings.TrimSpace(creditID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if p == nil || creditID == "" || idempotencyKey == "" {
		return false
	}
	p.pendingHomeAction = &HomeAction{Kind: HomeActionConsumeCodexReset, ResetCreditID: creditID, IdempotencyKey: idempotencyKey}
	p.statusLine = "using Codex reset credit..."
	return true
}

package ui

import "strings"

type ChatActionKind string

const (
	ChatActionOpenSession             ChatActionKind = "open-session"
	ChatActionCopyText                ChatActionKind = "copy-text"
	ChatActionSavePlan                ChatActionKind = "save-plan"
	ChatActionOpenAgentsModal         ChatActionKind = "open-agents-modal"
	ChatActionOpenModelsModal         ChatActionKind = "open-models-modal"
	ChatActionCycleThinking           ChatActionKind = "cycle-thinking"
	ChatActionToggleBypassPermissions ChatActionKind = "toggle-bypass-permissions"
)

type ChatSessionPlan struct {
	ID            string
	Title         string
	Plan          string
	Status        string
	ApprovalState string
}

type ChatAction struct {
	Kind          ChatActionKind
	Session       ChatSessionPaletteItem
	Text          string
	SuccessStatus string
	Plan          ChatSessionPlan
}

func (p *ChatPage) queueFooterAction(action string) {
	if p == nil {
		return
	}
	switch action {
	case "open-agents-modal":
		p.pendingChatAction = &ChatAction{Kind: ChatActionOpenAgentsModal}
	case "open-models-modal":
		p.pendingChatAction = &ChatAction{Kind: ChatActionOpenModelsModal}
	case "cycle-thinking":
		p.pendingChatAction = &ChatAction{Kind: ChatActionCycleThinking}
	}
}

func (p *ChatPage) PopChatAction() (ChatAction, bool) {
	if p == nil || p.pendingChatAction == nil {
		return ChatAction{}, false
	}
	action := *p.pendingChatAction
	p.pendingChatAction = nil
	return action, true
}

func normalizeChatSessionPaletteItems(tabs []ChatSessionTab) []ChatSessionPaletteItem {
	if len(tabs) == 0 {
		return nil
	}
	items := make([]ChatSessionPaletteItem, 0, len(tabs))
	for _, tab := range tabs {
		id := strings.TrimSpace(tab.ID)
		title := strings.TrimSpace(tab.Title)
		if id == "" && title == "" {
			continue
		}
		if title == "" {
			title = id
		}
		if id == "" {
			id = title
		}
		items = append(items, ChatSessionPaletteItem{
			ID:              id,
			Title:           title,
			WorkspaceName:   strings.TrimSpace(tab.WorkspaceName),
			WorkspacePath:   strings.TrimSpace(tab.WorkspacePath),
			Mode:            strings.TrimSpace(tab.Mode),
			UpdatedAgo:      strings.TrimSpace(tab.UpdatedAgo),
			Provider:        strings.TrimSpace(tab.Provider),
			ModelName:       strings.TrimSpace(tab.ModelName),
			ServiceTier:     strings.TrimSpace(tab.ServiceTier),
			ContextMode:     strings.TrimSpace(tab.ContextMode),
			Background:      tab.Background,
			ParentSessionID: strings.TrimSpace(tab.ParentSessionID),
			LineageKind:     strings.TrimSpace(tab.LineageKind),
			LineageLabel:    normalizeSessionLineageLabel(tab.LineageLabel),
			TargetKind:      strings.TrimSpace(tab.TargetKind),
			TargetName:      strings.TrimSpace(tab.TargetName),
			Depth:           tab.Depth,
		})
	}
	return items
}

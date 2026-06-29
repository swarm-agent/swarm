package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

var tuiRealtimeWorksetResources = []string{"membership", "projections", "current_run_state", "permission_summaries", "sessions", "tombstones"}

func (a *App) applyTUISessionWorksetSnapshot(workset client.SessionV3Workset, state tuiRealtimeWorksetState) {
	if a == nil {
		return
	}
	if a.tuiSessionStore == nil {
		a.tuiSessionStore = newTUISessionStore()
	}
	if a.tuiRealtime != nil && strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey) != "" && strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey) != strings.TrimSpace(state.ScopeKey) {
		a.tuiRealtime.Stop()
		a.drainTUIRealtimeQueues()
	}
	a.tuiSessionStore.ResetFromWorkset(workset)
	a.tuiRealtimeWorkset = cloneTUIRealtimeWorksetState(state)
}

func (a *App) markTUIRealtimeScopeStale(reason string) {
	if a == nil {
		return
	}
	if a.tuiSessionStore != nil {
		a.tuiSessionStore.MarkScopeStale(reason)
	}
	if a.tuiRealtime != nil {
		a.tuiRealtime.Stop()
	}
	a.drainTUIRealtimeQueues()
}

func (a *App) drainTUIRealtimeQueues() {
	if a == nil {
		return
	}
	for {
		select {
		case <-a.tuiRealtimeFrames:
			continue
		case <-a.tuiRealtimeStatuses:
			continue
		default:
			return
		}
	}
}

func (a *App) canUseCachedTUIWorkset(state tuiRealtimeWorksetState) bool {
	if a == nil || a.tuiSessionStore == nil {
		return false
	}
	if strings.TrimSpace(state.ScopeKey) == "" || strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey) != strings.TrimSpace(state.ScopeKey) {
		return false
	}
	if a.tuiSessionStore.EndpointCursor() == "" {
		return false
	}
	return !a.tuiSessionStore.StaleState().Stale
}

func (a *App) reconcileTUIRealtime() error {
	if a == nil || a.api == nil || a.tuiSessionStore == nil {
		return nil
	}
	state := a.tuiRealtimeWorkset
	if strings.TrimSpace(state.ScopeKey) == "" {
		return nil
	}
	cursor := a.tuiSessionStore.EndpointCursor()
	if cursor == "" {
		return errors.New("tui realtime requires workset snapshot endpoint cursor")
	}
	if a.tuiRealtimeFrames == nil {
		a.tuiRealtimeFrames = make(chan client.V3RealtimeFrame, 256)
	}
	if a.tuiRealtimeStatuses == nil {
		a.tuiRealtimeStatuses = make(chan tuiRealtimeStatus, 32)
	}
	if a.tuiRealtime == nil {
		a.tuiRealtime = newTUIRealtimeController(a.api, a.tuiRealtimeFrames, a.tuiRealtimeStatuses)
	}
	a.tuiRealtime.SetWake(a.requestStreamReadyInterrupt)
	workset := a.tuiRealtimeWorksetSubscription(state)
	return a.tuiRealtime.Reconcile(a.tuiSessionStore.DesiredSubscriptions(a.tuiRealtimeClientID), []client.V3RealtimeWorksetSubscription{workset}, cursor)
}

func (a *App) sendTUIV3ChatMessage(ctx context.Context, sessionID string, req ui.ChatSendRequest) (client.SessionV3MessageResult, error) {
	if a == nil || a.api == nil {
		return client.SessionV3MessageResult{}, errors.New("api client is not configured")
	}
	result, err := a.api.SendSessionV3Message(ctx, sessionID, client.SessionV3MessageOptions{Role: "user", Content: req.Prompt, Metadata: tuiV3ChatSendMetadata(req)})
	if err != nil {
		return client.SessionV3MessageResult{}, err
	}
	if a.tuiSessionStore == nil {
		a.tuiSessionStore = newTUISessionStore()
	}
	applyResult := a.tuiSessionStore.MergeMessageResult(result)
	if applyResult.Changed {
		a.applyTUISessionStoreToHome()
		if applyResult.SessionID != "" && a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == applyResult.SessionID {
			a.applyTUISessionStoreToChat(applyResult.SessionID)
		}
	}
	if err := a.reconcileTUIRealtime(); err != nil {
		return client.SessionV3MessageResult{}, err
	}
	return result, nil
}

func tuiV3ChatSendMetadata(req ui.ChatSendRequest) map[string]any {
	metadata := make(map[string]any, 2)
	if req.Compact {
		metadata["compact"] = true
	}
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		metadata["instructions"] = instructions
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func (a *App) tuiRealtimeWorksetSubscription(state tuiRealtimeWorksetState) client.V3RealtimeWorksetSubscription {
	clientID := strings.TrimSpace(a.tuiRealtimeClientID)
	if clientID == "" {
		clientID = fmt.Sprintf("tui:%d", time.Now().UnixNano())
		a.tuiRealtimeClientID = clientID
	}
	scope := strings.TrimSpace(state.ScopeKey)
	selector := client.V3RealtimeWorksetSelector{Kind: "workspace"}
	if cwd := strings.TrimSpace(state.CWDPath); cwd != "" {
		selector.WorkspacePath = cwd
	} else if len(state.WorkspacePaths) == 1 {
		selector.WorkspacePath = state.WorkspacePaths[0]
	} else if len(state.WorkspacePaths) > 1 {
		selector.WorkspacePaths = append([]string(nil), state.WorkspacePaths...)
	}
	return client.V3RealtimeWorksetSubscription{
		WorksetID:             "tui:" + scope,
		SubscriptionID:        clientID + ":workset:" + scope,
		Surface:               "tui",
		Selector:              selector,
		Resources:             append([]string(nil), tuiRealtimeWorksetResources...),
		AutoSubscribeSessions: true,
	}
}

func (a *App) consumeTUIRealtimeEvents() bool {
	if a == nil {
		return false
	}
	changed := false
	for {
		select {
		case frame := <-a.tuiRealtimeFrames:
			if a.applyTUIRealtimeFrame(frame) {
				changed = true
			}
		case status := <-a.tuiRealtimeStatuses:
			if a.applyTUIRealtimeStatus(status) {
				changed = true
			}
		default:
			return changed
		}
	}
}

func (a *App) applyTUIRealtimeFrame(frame client.V3RealtimeFrame) bool {
	if a == nil || a.tuiSessionStore == nil {
		return false
	}
	if a.tuiSessionStore.StaleState().ScopeChanged {
		return false
	}
	result := a.tuiSessionStore.ApplyRealtimeFrame(frame)
	if result.NeedsRehydrate {
		a.queueTUIWorksetRehydrate(result)
	}
	if result.Changed {
		a.applyTUISessionStoreToHome()
		if a.chat != nil && strings.TrimSpace(result.SessionID) == a.chat.SessionID() {
			a.applyTUISessionStoreToChat(result.SessionID)
		}
		return true
	}
	return false
}

func (a *App) applyTUIRealtimeStatus(status tuiRealtimeStatus) bool {
	if a == nil {
		return false
	}
	switch status.Kind {
	case tuiRealtimeStatusTerminal:
		if a.tuiSessionStore == nil {
			return false
		}
		if a.tuiSessionStore.StaleState().Stale {
			return false
		}
		result := a.tuiSessionStore.ApplyRealtimeFrame(status.Frame)
		if result.NeedsRehydrate {
			a.queueTUIWorksetRehydrate(result)
		}
		return false
	case tuiRealtimeStatusFailed:
		if a.tuiSessionStore != nil {
			a.tuiSessionStore.MarkScopeStale(statusReason(status))
		}
		a.queueTUIWorksetRehydrate(tuiSessionStoreApplyResult{NeedsRehydrate: true, SessionID: strings.TrimSpace(status.SessionID), Reason: statusReason(status)})
		return false
	default:
		return false
	}
}

func (a *App) queueTUIWorksetRehydrate(result tuiSessionStoreApplyResult) {
	if a == nil {
		return
	}
	if a.tuiSessionStore != nil && a.tuiSessionStore.StaleState().ScopeChanged {
		return
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = "realtime cursor requires rehydrate"
	}
	if sessionID := strings.TrimSpace(result.SessionID); sessionID != "" {
		a.queueTUISessionRehydrate(sessionID, reason)
		return
	}
	if a.route == "chat" && a.chat != nil {
		a.chat.SetStatus("realtime reconnecting: " + reason)
	} else if a.home != nil {
		a.home.SetStatus("realtime reconnecting: " + reason)
	}
	a.queueReload(true)
}

func (a *App) queueTUISessionRehydrate(sessionID, reason string) {
	if a == nil || a.api == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "realtime cursor requires session rehydrate"
	}
	if a.route == "chat" && a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
		a.chat.SetStatus("realtime reconnecting: " + reason)
	} else if a.home != nil {
		a.home.SetStatus("realtime reconnecting: " + reason)
	}
	if !a.reloading.CompareAndSwap(false, true) {
		return
	}
	workspaceScope := ""
	cwdScope := ""
	if cwd := strings.TrimSpace(a.tuiRealtimeWorkset.CWDPath); cwd != "" {
		cwdScope = cwd
	} else if len(a.tuiRealtimeWorkset.WorkspacePaths) > 0 {
		workspaceScope = strings.TrimSpace(a.tuiRealtimeWorkset.WorkspacePaths[0])
	} else if path := strings.TrimSpace(a.activeWorkspacePath()); path != "" {
		workspaceScope = path
	} else if path := strings.TrimSpace(a.activeContextPath()); path != "" {
		cwdScope = path
	} else {
		cwdScope = strings.TrimSpace(a.startupCWD)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		hydrated, err := a.api.GetSessionV3TUI(ctx, sessionID, workspaceScope, cwdScope)
		result := homeReloadResult{sessionID: sessionID, err: err, silent: true}
		if err == nil {
			result.hydrated = &hydrated
		}
		select {
		case a.reloadCh <- result:
		default:
		}
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptReloadReady))
		}
	}()
}

func (a *App) applyTUISessionHydratedReload(hydrated client.SessionV3Hydrated, fallbackSessionID string) {
	if a == nil {
		return
	}
	if a.tuiSessionStore == nil {
		a.tuiSessionStore = newTUISessionStore()
	}
	a.tuiSessionStore.MergeHydrated(hydrated)
	sessionID := firstNonEmpty(strings.TrimSpace(hydrated.Session.ID), strings.TrimSpace(hydrated.Projection.SessionID), strings.TrimSpace(fallbackSessionID))
	a.applyTUISessionStoreToHome()
	if sessionID != "" && a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
		a.applyTUISessionStoreToChat(sessionID)
	}
	if err := a.reconcileTUIRealtime(); err != nil && a.home != nil {
		a.home.SetStatus(fmt.Sprintf("realtime unavailable: %v", err))
	}
}

func (a *App) applyTUISessionStoreToHome() {
	if a == nil || a.tuiSessionStore == nil || a.home == nil {
		return
	}
	next := a.tuiSessionStore.HomeModel(a.homeModel)
	next.BackgroundSessions = backgroundSessionSummariesForSessions(next.RecentSessions, next.BackgroundSessions)
	a.homeModel.RecentSessions = next.RecentSessions
	a.homeModel.BackgroundSessions = next.BackgroundSessions
	a.home.SetModel(a.homeModel)
	if a.chat != nil {
		a.chat.SetSessionTabs(chatSessionTabsFromSummaries(a.homeModel.RecentSessions))
	}
}

func (a *App) applyTUISessionStoreToChat(sessionID string) {
	if a == nil || a.chat == nil || a.tuiSessionStore == nil {
		return
	}
	snapshot, ok := a.tuiSessionStore.ChatSnapshot(sessionID)
	if !ok {
		return
	}
	a.chat.SetSessionTabs(chatSessionTabsFromSummaries(a.homeModel.RecentSessions))
	a.chat.SetMessages(chatMessagesFromClient(snapshot.Messages))
	a.chat.SetUsageSummary(convertClientUsageSummary(snapshot.UsageSummary))
	if snapshot.Session.Lifecycle != nil {
		a.chat.ApplySessionLifecycle(chatLifecycleFromClient(snapshot.Session.Lifecycle))
	}
	resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown := resolveSessionEffectiveAgent(snapshot.Summary,
		emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm"),
		strings.TrimSpace(a.homeModel.ActiveAgentExecutionSetting),
		a.homeModel.ActiveAgentExitPlanMode,
		a.homeModel.ActiveAgentRuntimeKnown,
	)
	a.chat.SetAgentRuntime(resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown)
	taskCount, openCount, inProgressCount := agentTodoCountsFromMetadata(snapshot.Summary.Metadata)
	a.chat.SetAgentTodoSummary(taskCount, openCount, inProgressCount)
}

func (a *App) bootstrapTUIRealtimeWorkset(ctx context.Context, opts tuiSessionWorksetLoadOptions) (client.SessionV3Workset, tuiRealtimeWorksetState, error) {
	state, err := tuiRealtimeWorksetStateFromOptions(opts)
	if err != nil {
		return client.SessionV3Workset{}, tuiRealtimeWorksetState{}, err
	}
	if opts.Limit <= 0 {
		opts.Limit = homeRecentSessionLimit
	}
	workset, err := a.api.GetSessionV3TUIWorkset(ctx, client.SessionV3TUIWorksetRequest{
		Scope: client.SessionV3TUIWorksetScope{
			WorkspacePaths: append([]string(nil), state.WorkspacePaths...),
			CWDPath:        state.CWDPath,
		},
		Recent: client.SessionV3WorksetRecent{
			Limit:           opts.Limit,
			BeforeUpdatedAt: opts.BeforeUpdatedAt,
			BeforeSessionID: strings.TrimSpace(opts.BeforeSessionID),
		},
		History: client.SessionV3WorksetHistory{
			Mode:                  "tail",
			MaxMessagesPerSession: 20,
			MaxEventsPerSession:   50,
			ManifestPolicy:        "manifest",
			IncludeEvents:         true,
		},
	})
	if err != nil {
		return client.SessionV3Workset{}, tuiRealtimeWorksetState{}, err
	}
	if strings.TrimSpace(workset.SnapshotEndpointCursor) == "" {
		return client.SessionV3Workset{}, tuiRealtimeWorksetState{}, errors.New("tui workset snapshot endpoint cursor is required")
	}
	return workset, state, nil
}

func tuiRealtimeWorksetStateFromOptions(opts tuiSessionWorksetLoadOptions) (tuiRealtimeWorksetState, error) {
	workspacePaths := canonicalUniquePaths(opts.WorkspacePaths)
	cwdPath := normalizePath(strings.TrimSpace(opts.CWDPath))
	if len(workspacePaths) == 0 && cwdPath == "" {
		return tuiRealtimeWorksetState{}, errors.New("tui realtime workset scope is required")
	}
	state := tuiRealtimeWorksetState{WorkspacePaths: append([]string(nil), workspacePaths...), CWDPath: cwdPath}
	state.ScopeKey = tuiRealtimeScopeKey(state)
	if state.ScopeKey == "" {
		return tuiRealtimeWorksetState{}, errors.New("tui realtime workset scope key is required")
	}
	return state, nil
}

func tuiRealtimeScopeKey(state tuiRealtimeWorksetState) string {
	if cwd := normalizePath(strings.TrimSpace(state.CWDPath)); cwd != "" {
		return "cwd:" + cwd
	}
	paths := canonicalUniquePaths(state.WorkspacePaths)
	if len(paths) == 0 {
		return ""
	}
	return "workspace:" + strings.Join(paths, "|")
}

func cloneTUIRealtimeWorksetState(state tuiRealtimeWorksetState) tuiRealtimeWorksetState {
	return tuiRealtimeWorksetState{
		ScopeKey:       strings.TrimSpace(state.ScopeKey),
		WorkspacePaths: append([]string(nil), state.WorkspacePaths...),
		CWDPath:        strings.TrimSpace(state.CWDPath),
	}
}

func chatMessagesFromClient(messages []client.SessionMessage) []ui.ChatMessageRecord {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ui.ChatMessageRecord, 0, len(messages))
	for _, message := range messages {
		out = append(out, convertClientMessage(message))
	}
	return out
}

func chatLifecycleFromClient(lifecycle *client.SessionLifecycleSnapshot) ui.ChatSessionLifecycle {
	if lifecycle == nil {
		return ui.ChatSessionLifecycle{}
	}
	return ui.ChatSessionLifecycle{
		SessionID:      lifecycle.SessionID,
		RunID:          lifecycle.RunID,
		Active:         lifecycle.Active,
		Phase:          lifecycle.Phase,
		StartedAt:      lifecycle.StartedAt,
		EndedAt:        lifecycle.EndedAt,
		UpdatedAt:      lifecycle.UpdatedAt,
		Generation:     lifecycle.Generation,
		StopReason:     lifecycle.StopReason,
		Error:          lifecycle.Error,
		OwnerTransport: lifecycle.OwnerTransport,
	}
}

func statusReason(status tuiRealtimeStatus) string {
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		return reason
	}
	if status.Err != nil {
		return status.Err.Error()
	}
	return string(status.Kind)
}

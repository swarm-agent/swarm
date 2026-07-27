package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"

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

func (a *App) canUseCachedTUIWorkset(state tuiRealtimeWorksetState, sessionIDs []string) bool {
	if a == nil || a.tuiSessionStore == nil {
		return false
	}
	if strings.TrimSpace(state.ScopeKey) == "" || strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey) != strings.TrimSpace(state.ScopeKey) {
		return false
	}
	if a.tuiSessionStore.EndpointCursor() == "" {
		return false
	}
	if a.tuiSessionStore.StaleState().Stale {
		return false
	}
	if len(trimTUIRealtimeStrings(sessionIDs)) == 0 {
		return true
	}
	workset := a.tuiSessionStore.WorksetSnapshot()
	for _, id := range trimTUIRealtimeStrings(sessionIDs) {
		if _, ok := workset.SessionsByID[id]; !ok {
			return false
		}
	}
	return true
}

func (a *App) ensureTUIRealtimeWorksetReady(ctx context.Context, opts tuiSessionWorksetLoadOptions) error {
	if a == nil || a.api == nil {
		return errors.New("api client is not configured")
	}
	state, err := tuiRealtimeWorksetStateFromOptions(opts)
	if err != nil {
		return err
	}
	if a.tuiSessionStore == nil {
		a.tuiSessionStore = newTUISessionStore()
	}
	if a.canUseCachedTUIWorkset(state, opts.SessionIDs) {
		return nil
	}
	previousScope := strings.TrimSpace(a.tuiRealtimeWorkset.ScopeKey)
	workset, state, err := a.bootstrapTUIRealtimeWorkset(ctx, opts)
	if err != nil {
		return err
	}
	if previousScope != "" && previousScope == strings.TrimSpace(state.ScopeKey) && !a.tuiSessionStore.StaleState().ScopeChanged {
		a.tuiSessionStore.MergeWorkset(workset)
	} else {
		a.tuiSessionStore.ResetFromWorkset(workset)
	}
	a.tuiRealtimeWorkset = cloneTUIRealtimeWorksetState(state)
	a.applyTUISessionStoreToHome()
	return nil
}

func (a *App) reconcileTUIRealtime() error {
	return a.reconcileTUIRealtimeWithContext(nil, false)
}

func (a *App) waitForTUIRealtimeReady(ctx context.Context) error {
	return a.reconcileTUIRealtimeWithContext(ctx, true)
}

func (a *App) reconcileTUIRealtimeWithContext(ctx context.Context, wait bool) error {
	if a == nil || a.api == nil || a.tuiSessionStore == nil {
		if wait {
			return errors.New("tui realtime requires session store before sending")
		}
		return nil
	}
	state := a.tuiRealtimeWorkset
	if strings.TrimSpace(state.ScopeKey) == "" {
		if wait {
			return errors.New("tui realtime requires a workset subscription before sending")
		}
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
	subscriptions := a.tuiSessionStore.DesiredSubscriptions(a.tuiRealtimeClientID)
	worksets := []client.V3RealtimeWorksetSubscription{workset}
	if wait {
		return a.tuiRealtime.ReconcileAndWait(ctx, subscriptions, worksets, cursor)
	}
	return a.tuiRealtime.Reconcile(subscriptions, worksets, cursor)
}

func (a *App) sendTUIV3ChatMessage(ctx context.Context, sessionID string, req ui.ChatSendRequest) (client.SessionV3MessageResult, error) {
	if a == nil || a.api == nil {
		return client.SessionV3MessageResult{}, errors.New("api client is not configured")
	}
	if err := a.ensureTUIRealtimeWorksetReady(ctx, a.tuiSessionWorksetOptionsForRealtime(sessionID)); err != nil {
		return client.SessionV3MessageResult{}, err
	}
	if err := a.waitForTUIRealtimeReady(ctx); err != nil {
		return client.SessionV3MessageResult{}, err
	}
	op := newTUIV3MessageOperation(sessionID)
	result, err := a.api.SendSessionV3Message(ctx, sessionID, client.SessionV3MessageOptions{ClientRequestID: op.ClientRequestID, MessageID: op.MessageID, RunID: op.RunID, Role: "user", Content: req.Prompt, Metadata: tuiV3ChatSendMetadata(req)})
	if err != nil {
		return client.SessionV3MessageResult{}, err
	}
	if err := validateTUIV3MessageAccepted(result); err != nil {
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

func (a *App) tuiSessionWorksetOptionsForRealtime(sessionID string) tuiSessionWorksetLoadOptions {
	if a == nil {
		return tuiSessionWorksetLoadOptions{}
	}
	if state := cloneTUIRealtimeWorksetState(a.tuiRealtimeWorkset); strings.TrimSpace(state.ScopeKey) != "" {
		return tuiSessionWorksetLoadOptions{Limit: homeRecentSessionLimit, SessionIDs: []string{strings.TrimSpace(sessionID)}, WorkspacePaths: state.WorkspacePaths, CWDPath: state.CWDPath}
	}
	if workspacePath := normalizePath(strings.TrimSpace(a.activeWorkspacePath())); workspacePath != "" {
		return tuiSessionWorksetLoadOptions{Limit: homeRecentSessionLimit, SessionIDs: []string{strings.TrimSpace(sessionID)}, WorkspacePaths: []string{workspacePath}}
	}
	if contextPath := normalizePath(strings.TrimSpace(a.activeContextPath())); contextPath != "" {
		return tuiSessionWorksetLoadOptions{Limit: homeRecentSessionLimit, SessionIDs: []string{strings.TrimSpace(sessionID)}, CWDPath: contextPath}
	}
	if startupCWD := normalizePath(strings.TrimSpace(a.startupCWD)); startupCWD != "" {
		return tuiSessionWorksetLoadOptions{Limit: homeRecentSessionLimit, SessionIDs: []string{strings.TrimSpace(sessionID)}, CWDPath: startupCWD}
	}
	return tuiSessionWorksetLoadOptions{Limit: homeRecentSessionLimit, SessionIDs: []string{strings.TrimSpace(sessionID)}}
}

type tuiV3MessageOperation struct {
	OperationID     string
	ClientRequestID string
	MessageID       string
	RunID           string
}

func newTUIV3MessageOperation(sessionID string) tuiV3MessageOperation {
	sessionID = strings.TrimSpace(sessionID)
	operationID := strings.ReplaceAll(uuid.NewString(), "-", "")
	return tuiV3MessageOperation{
		OperationID:     operationID,
		ClientRequestID: fmt.Sprintf("tui-v3-existing-message:%s:%s", sessionID, operationID),
		MessageID:       "tui-v3-message:" + operationID,
		RunID:           "tui-v3-run:" + operationID,
	}
}

func validateTUIV3MessageAccepted(result client.SessionV3MessageResult) error {
	status := strings.ToLower(strings.TrimSpace(result.RunIntent.Status))
	switch status {
	case "accepted", "pending_executor":
		return nil
	case "":
		return errors.New("tui v3 chat run was not accepted: missing phase")
	default:
		return fmt.Errorf("tui v3 chat run was not accepted: %s", status)
	}
}

func tuiV3ChatSendMetadata(req ui.ChatSendRequest) map[string]any {
	metadata := make(map[string]any, 1)
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
	if strings.EqualFold(strings.TrimSpace(frame.Kind), v3RealtimeLivePatchKind) {
		return a.applyTUIRealtimeLivePatch(frame)
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

const v3RealtimeLivePatchKind = "live.patch"

func (a *App) applyTUIRealtimeLivePatch(frame client.V3RealtimeFrame) bool {
	if a == nil || a.chat == nil || frame.Live == nil {
		return false
	}
	live := frame.Live
	sessionID := strings.TrimSpace(frame.SessionID)
	if sessionID == "" || sessionID != strings.TrimSpace(live.SessionID) || sessionID != strings.TrimSpace(a.chat.SessionID()) {
		return false
	}
	return a.chat.ApplySharedStreamEvent(ui.ChatRunStreamEvent{
		Type:         "assistant.live.delta",
		SessionID:    sessionID,
		RunID:        strings.TrimSpace(live.RunID),
		Step:         live.Step,
		StepID:       strings.TrimSpace(live.StepID),
		StreamID:     strings.TrimSpace(live.StreamID),
		StreamKind:   strings.TrimSpace(live.StreamKind),
		Operation:    strings.TrimSpace(live.Operation),
		LiveSeqStart: live.LiveSeqStart,
		LiveSeqEnd:   live.LiveSeqEnd,
		OffsetStart:  live.OffsetStart,
		OffsetEnd:    live.OffsetEnd,
		Delta:        live.Text,
	}, live.RecordedAt)
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
	a.chat.SetSessionMode(snapshot.Session.Mode)
	if strings.TrimSpace(snapshot.Preference.Provider) != "" || strings.TrimSpace(snapshot.Preference.Model) != "" {
		a.chat.SetModelState(
			strings.TrimSpace(snapshot.Preference.Provider),
			strings.TrimSpace(snapshot.Preference.Model),
			strings.TrimSpace(snapshot.Preference.Thinking),
			strings.TrimSpace(snapshot.Preference.ServiceTier),
			strings.TrimSpace(snapshot.Preference.ContextMode),
		)
	}
	if snapshot.AgentModelPolicy.ContextWindow > 0 {
		a.chat.SetContextWindow(snapshot.AgentModelPolicy.ContextWindow)
	}
	a.chat.SetMessages(chatMessagesFromClient(snapshot.Messages, snapshot.Events))
	a.chat.ApplyPermissionRecords(convertClientPermissions(snapshot.PendingPerms))
	a.chat.SetUsageSummary(convertClientUsageSummary(snapshot.UsageSummary))
	var activePlan client.SessionPlan
	for _, plan := range snapshot.Plans {
		if plan.Active {
			activePlan = plan
			break
		}
	}
	if activePlan.ID == "" && len(snapshot.Plans) > 0 {
		activePlan = snapshot.Plans[0]
	}
	uiPlan := chatSessionPlanFromClient(activePlan)
	uiRevisions := make([]ui.ChatSessionPlan, 0, len(snapshot.PlanRevisions))
	for _, revision := range snapshot.PlanRevisions {
		uiRevisions = append(uiRevisions, chatSessionPlanFromClient(revision))
	}
	runID, runStatus := "", ""
	if snapshot.ActiveRunIntent != nil {
		runID, runStatus = snapshot.ActiveRunIntent.RunID, snapshot.ActiveRunIntent.Status
	}
	a.chat.SetPlanExecutionState(uiPlan, uiRevisions, runID, runStatus)
	if activePlan.ID != "" {
		a.chat.SetActivePlan(chatPlanLabel(activePlan))
	}
	if snapshot.Session.Lifecycle != nil {
		a.chat.ApplySessionLifecycle(chatLifecycleFromClient(snapshot.Session.Lifecycle))
	}
	resolvedAgent, resolvedExecution, resolvedExitPlanMode, resolvedRuntimeKnown := resolveSessionEffectiveAgent(snapshot.Summary, snapshot.AgentModelPolicy, a.agentState,
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
		SessionIDs: trimTUIRealtimeStrings(opts.SessionIDs),
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

func chatSessionPlanFromClient(plan client.SessionPlan) ui.ChatSessionPlan {
	return ui.ChatSessionPlan{ID: strings.TrimSpace(plan.ID), Title: strings.TrimSpace(plan.Title), Plan: plan.Plan, Document: plan.Document, Status: strings.TrimSpace(plan.Status), ApprovalState: strings.TrimSpace(plan.ApprovalState), Active: plan.Active, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt, PriorTitle: strings.TrimSpace(plan.PriorTitle), PriorPlan: plan.PriorPlan, DiffLines: append([]string(nil), plan.DiffLines...), UpdateSummary: strings.TrimSpace(plan.UpdateSummary), UpdateScope: strings.TrimSpace(plan.UpdateScope), UpdateKind: strings.TrimSpace(plan.UpdateKind), Version: plan.Version, ParentRevision: plan.ParentRevision, Checkpoint: plan.Checkpoint}
}

func chatMessagesFromClient(messages []client.SessionMessage, events []client.SessionV3Event) []ui.ChatMessageRecord {
	out := make([]ui.ChatMessageRecord, 0, len(messages)+len(events))
	eventToolInstances := make(map[string]struct{})
	projectedEventMessages := make([]ui.ChatMessageRecord, 0, len(events))
	for _, event := range events {
		if message, instanceID, ok := chatToolMessageFromV3Event(event); ok {
			if instanceID != "" {
				eventToolInstances[instanceID] = struct{}{}
			}
			projectedEventMessages = append(projectedEventMessages, message)
			continue
		}
		if message, ok := chatReasoningMessageFromV3Event(event); ok {
			projectedEventMessages = append(projectedEventMessages, message)
		}
	}
	for _, message := range messages {
		if instanceID := clientToolMessageInstanceID(message); instanceID != "" {
			if _, projected := eventToolInstances[instanceID]; projected {
				continue
			}
		}
		out = append(out, convertClientMessage(message))
	}
	out = append(out, projectedEventMessages...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.GlobalSeq != right.GlobalSeq {
			if left.GlobalSeq == 0 {
				return false
			}
			if right.GlobalSeq == 0 {
				return true
			}
			return left.GlobalSeq < right.GlobalSeq
		}
		if left.CreatedAt != right.CreatedAt {
			if left.CreatedAt == 0 {
				return false
			}
			if right.CreatedAt == 0 {
				return true
			}
			return left.CreatedAt < right.CreatedAt
		}
		return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
	})
	return out
}

func chatReasoningMessageFromV3Event(event client.SessionV3Event) (ui.ChatMessageRecord, bool) {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	switch eventType {
	case "session.reasoning.started", "session.reasoning.delta", "session.reasoning.completed":
	default:
		return ui.ChatMessageRecord{}, false
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload == nil {
		return ui.ChatMessageRecord{}, false
	}
	text := strings.TrimSpace(firstNonEmpty(anyString(payload["summary"]), anyString(payload["delta"]), "Thinking"))
	reasoningID := strings.TrimSpace(anyString(payload["reasoning_id"]))
	if reasoningID == "" {
		reasoningID = strings.TrimSpace(event.ID)
	}
	metadata := map[string]any{"v3_reasoning_event": true, "reasoning_event_type": eventType}
	for _, key := range []string{"run_id", "reasoning_id", "reasoning_key", "delta_mode", "step_id"} {
		if value := strings.TrimSpace(anyString(payload[key])); value != "" {
			metadata[key] = value
		}
	}
	return ui.ChatMessageRecord{
		ID:        "v3-reasoning:" + reasoningID + ":" + strings.TrimSpace(event.ID),
		SessionID: strings.TrimSpace(event.SessionID),
		GlobalSeq: event.Seq,
		Role:      "reasoning",
		Content:   text,
		Metadata:  metadata,
		CreatedAt: event.TsUnixMS,
	}, true
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func chatToolMessageFromV3Event(event client.SessionV3Event) (ui.ChatMessageRecord, string, bool) {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	switch eventType {
	case "session.tool.started", "session.tool.delta", "session.tool.completed", "session.tool.failed", "session.tool.cancelled", "session.tool.canceled":
	default:
		return ui.ChatMessageRecord{}, "", false
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload == nil {
		return ui.ChatMessageRecord{}, "", false
	}
	payload["type"] = eventType
	content, err := json.Marshal(payload)
	if err != nil {
		return ui.ChatMessageRecord{}, "", false
	}
	createdAt := event.TsUnixMS
	if createdAt <= 0 {
		switch value := payload["recorded_at"].(type) {
		case float64:
			createdAt = int64(value)
		case int64:
			createdAt = value
		case int:
			createdAt = int64(value)
		}
	}
	instanceID, _ := payload["tool_instance_id"].(string)
	return ui.ChatMessageRecord{
		ID:        "v3-tool-event:" + strings.TrimSpace(event.ID),
		SessionID: strings.TrimSpace(event.SessionID),
		GlobalSeq: event.Seq,
		Role:      "tool",
		Content:   string(content),
		Metadata:  map[string]any{"v3_tool_event": true},
		CreatedAt: createdAt,
	}, strings.TrimSpace(instanceID), true
}

func clientToolMessageInstanceID(message client.SessionMessage) string {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
		return ""
	}
	if instanceID, _ := message.Metadata["tool_instance_id"].(string); strings.TrimSpace(instanceID) != "" {
		return strings.TrimSpace(instanceID)
	}
	var payload map[string]any
	if json.Unmarshal([]byte(message.Content), &payload) != nil {
		return ""
	}
	instanceID, _ := payload["tool_instance_id"].(string)
	return strings.TrimSpace(instanceID)
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

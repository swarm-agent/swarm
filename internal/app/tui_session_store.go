package app

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

const (
	tuiRealtimeKindEvent        = "event"
	tuiRealtimeKindCursorError  = "cursor.error"
	tuiRealtimeKindAuthDenied   = "auth.denied"
	tuiRealtimeKindSlowConsumer = "slow_consumer.reconnect_required"
	tuiRealtimeKindSessionGap   = "session_cursor_gap"
)

type tuiSessionStore struct {
	mu sync.RWMutex

	workset          client.SessionV3Workset
	hydratedSessions map[string]bool
	stale            tuiSessionStoreStaleState
}

type tuiSessionStoreStaleState struct {
	Stale        bool
	SessionID    string
	Reason       string
	ScopeChanged bool
}

type tuiSessionStoreApplyResult struct {
	Changed        bool
	NeedsRehydrate bool
	SessionID      string
	Reason         string
}

type tuiSessionChatSnapshot struct {
	Session          client.SessionSummary
	Summary          model.SessionSummary
	Projection       client.SessionV3Projection
	Messages         []client.SessionMessage
	Events           []client.SessionV3Event
	PendingPerms     []client.PermissionRecord
	UsageSummary     *client.SessionUsageSummary
	Preference       client.ModelPreference
	AgentModelPolicy client.SessionV3AgentModelPolicy
	ActiveRunIntent  *client.SessionV3RunIntent
	Plans            []client.SessionPlan
	PlanRevisions    []client.SessionPlan
	EndpointCursor   string
	Hydrated         bool
}

func newTUISessionStore() *tuiSessionStore {
	return &tuiSessionStore{hydratedSessions: make(map[string]bool)}
}

func (s *tuiSessionStore) ResetFromWorkset(workset client.SessionV3Workset) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workset = cloneSessionV3Workset(workset)
	s.hydratedSessions = make(map[string]bool)
	s.stale = tuiSessionStoreStaleState{}
}

func (s *tuiSessionStore) MergeWorkset(workset client.SessionV3Workset) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mergeSessionV3Workset(&s.workset, workset)
	s.stale = tuiSessionStoreStaleState{}
}

func (s *tuiSessionStore) MergeHydrated(hydrated client.SessionV3Hydrated) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeHydratedLocked(hydrated)
	s.stale = tuiSessionStoreStaleState{}
}

// ApplyModePreference keeps the local projection aligned with the authoritative
// mode mutation response until its realtime event is observed.
func (s *tuiSessionStore) ApplyModePreference(sessionID, mode string, preference client.ModelPreference, contextWindow int) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureWorksetMapsLocked()
	summary := s.workset.SessionsByID[sessionID]
	summary.ID = sessionID
	if strings.TrimSpace(summary.SessionAPI) == "" {
		summary.SessionAPI = "v3"
	}
	if nextMode := strings.TrimSpace(mode); nextMode != "" {
		summary.Mode = nextMode
	}
	summary.Preference = mergeClientModelPreference(summary.Preference, preference)
	s.workset.SessionsByID[sessionID] = summary
	s.workset.PreferencesBySession[sessionID] = preference
	if policy, ok := s.workset.AgentModelPolicyBySession[sessionID]; ok {
		policy.Preference = preference
		if contextWindow > 0 {
			policy.ContextWindow = contextWindow
		}
		s.workset.AgentModelPolicyBySession[sessionID] = policy
	}
}

func (s *tuiSessionStore) MergeMessageResult(result client.SessionV3MessageResult) tuiSessionStoreApplyResult {
	if s == nil {
		return tuiSessionStoreApplyResult{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := firstNonEmpty(strings.TrimSpace(result.Session.ID), strings.TrimSpace(result.Projection.SessionID), strings.TrimSpace(result.Message.SessionID), strings.TrimSpace(result.RunIntent.SessionID))
	if sessionID == "" {
		return tuiSessionStoreApplyResult{}
	}
	s.ensureWorksetMapsLocked()
	if strings.TrimSpace(result.Session.ID) != "" {
		session := cloneClientSessionSummary(result.Session)
		if strings.TrimSpace(session.SessionAPI) == "" {
			session.SessionAPI = "v3"
		}
		s.workset.SessionsByID[sessionID] = session
	} else if _, ok := s.workset.SessionsByID[sessionID]; !ok {
		s.workset.SessionsByID[sessionID] = client.SessionSummary{ID: sessionID, SessionAPI: "v3"}
	}
	if strings.TrimSpace(result.Projection.SessionID) != "" || result.Projection.LastEventSeq != 0 || result.Projection.ProjectionHighWatermarkSeq != 0 {
		projection := result.Projection
		if strings.TrimSpace(projection.SessionID) == "" {
			projection.SessionID = sessionID
		}
		s.workset.ProjectionsBySession[sessionID] = projection
	}
	if strings.TrimSpace(result.Message.ID) != "" {
		message := result.Message
		if strings.TrimSpace(message.SessionID) == "" {
			message.SessionID = sessionID
		}
		s.workset.MessagesBySession[sessionID] = appendOrReplaceMessage(s.workset.MessagesBySession[sessionID], message)
	}
	for _, message := range result.Messages {
		if strings.TrimSpace(message.ID) == "" {
			continue
		}
		if strings.TrimSpace(message.SessionID) == "" {
			message.SessionID = sessionID
		}
		s.workset.MessagesBySession[sessionID] = appendOrReplaceMessage(s.workset.MessagesBySession[sessionID], message)
	}
	for _, event := range result.Events {
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = sessionID
		}
		if strings.TrimSpace(event.ID) != "" {
			s.workset.EventsBySession[sessionID] = appendOrReplaceV3Event(s.workset.EventsBySession[sessionID], event)
		}
		s.applyEventPayloadLocked(sessionID, event)
	}
	if strings.TrimSpace(result.RunIntent.RunID) != "" {
		intent := result.RunIntent
		if strings.TrimSpace(intent.SessionID) == "" {
			intent.SessionID = sessionID
		}
		s.workset.RunIntentsBySession[sessionID] = appendOrReplaceRunIntent(s.workset.RunIntentsBySession[sessionID], intent)
		if lifecycle := v3RunIntentSessionLifecycle(sessionID, &intent); lifecycle != nil {
			session := s.workset.SessionsByID[sessionID]
			session.Lifecycle = lifecycle
			s.workset.SessionsByID[sessionID] = session
		}
	}
	if summary, ok := s.workset.SessionsByID[sessionID]; ok {
		if strings.TrimSpace(summary.SessionAPI) == "" {
			summary.SessionAPI = "v3"
		}
		summary.MessageCount = len(s.workset.MessagesBySession[sessionID])
		if projection, ok := s.workset.ProjectionsBySession[sessionID]; ok {
			summary.LastEventSeq = projection.LastEventSeq
			summary.ProjectionHighWatermarkSeq = projection.ProjectionHighWatermarkSeq
		}
		s.workset.SessionsByID[sessionID] = summary
	}
	s.prependSessionOrderLocked(sessionID)
	s.stale = tuiSessionStoreStaleState{}
	return tuiSessionStoreApplyResult{Changed: true, SessionID: sessionID}
}

func (s *tuiSessionStore) ApplyRealtimeFrame(frame client.V3RealtimeFrame) tuiSessionStoreApplyResult {
	if s == nil {
		return tuiSessionStoreApplyResult{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kind := strings.ToLower(strings.TrimSpace(frame.Kind))
	sessionID := firstNonEmpty(strings.TrimSpace(frame.SessionID), frameSessionID(frame))
	if tuiRealtimeFrameIsTerminal(frame) {
		return s.markStaleLocked(sessionID, tuiRealtimeFrameTerminalReason(frame))
	}
	if cursor := strings.TrimSpace(frame.EndpointCursor); cursor != "" && tuiRealtimeFrameCanAdvanceEndpointCursor(kind) {
		s.workset.SnapshotEndpointCursor = cursor
	}
	switch kind {
	case "workset.session.discovered", "workset.session.updated":
		return s.applyWorksetSessionFrameLocked(frame)
	case "workset.session.removed":
		return s.applyWorksetSessionRemovedLocked(frame)
	case "", "hello", "keepalive", "replay.started", "replay.complete":
		return tuiSessionStoreApplyResult{}
	case "projection.high_watermark":
		if sessionID == "" {
			return tuiSessionStoreApplyResult{}
		}
		projection := s.workset.ProjectionsBySession[sessionID]
		projection.SessionID = sessionID
		if frame.HighWatermarkSeq > projection.ProjectionHighWatermarkSeq {
			projection.ProjectionHighWatermarkSeq = frame.HighWatermarkSeq
			s.ensureWorksetMapsLocked()
			s.workset.ProjectionsBySession[sessionID] = projection
			return tuiSessionStoreApplyResult{Changed: true, SessionID: sessionID}
		}
		return tuiSessionStoreApplyResult{}
	case tuiRealtimeKindEvent:
		return s.applyEventFrameLocked(frame, sessionID)
	default:
		return tuiSessionStoreApplyResult{}
	}
}

func tuiRealtimeFrameCanAdvanceEndpointCursor(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case tuiRealtimeKindEvent,
		"endpoint.watermark",
		"workset.session.discovered",
		"workset.session.updated",
		"workset.session.removed",
		"projection.high_watermark",
		"replay.started",
		"replay.complete",
		"hello",
		"keepalive",
		"":
		return true
	default:
		return false
	}
}

func (s *tuiSessionStore) ApplySessionEvent(event client.StreamEventEnvelope) tuiSessionStoreApplyResult {
	if s == nil {
		return tuiSessionStoreApplyResult{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if eventType == "session.created" {
		var session client.SessionSummary
		if err := json.Unmarshal(event.Payload, &session); err != nil || strings.TrimSpace(session.ID) == "" {
			return tuiSessionStoreApplyResult{}
		}
		if strings.TrimSpace(session.SessionAPI) == "" {
			session.SessionAPI = "v3"
		}
		s.ensureWorksetMapsLocked()
		s.workset.SessionsByID[session.ID] = cloneClientSessionSummary(session)
		s.prependSessionOrderLocked(session.ID)
		return tuiSessionStoreApplyResult{Changed: true, SessionID: session.ID}
	}
	return tuiSessionStoreApplyResult{}
}

func (s *tuiSessionStore) HomeModel(shell model.HomeModel) model.HomeModel {
	if s == nil {
		return shell
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	next := shell
	next.RecentSessions = modelSessionSummariesFromTUIWorkset(cloneSessionV3Workset(s.workset))
	return next
}

func (s *tuiSessionStore) HomeSessions() []model.SessionSummary {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneModelSessionSummaries(modelSessionSummariesFromTUIWorkset(cloneSessionV3Workset(s.workset)))
}

func (s *tuiSessionStore) ChatSnapshot(sessionID string) (tuiSessionChatSnapshot, bool) {
	if s == nil {
		return tuiSessionChatSnapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return tuiSessionChatSnapshot{}, false
	}
	summary, ok := s.workset.SessionsByID[sessionID]
	if !ok {
		return tuiSessionChatSnapshot{}, false
	}
	snapshot := tuiSessionChatSnapshot{
		Session:          cloneClientSessionSummary(summary),
		Projection:       s.workset.ProjectionsBySession[sessionID],
		Messages:         cloneSessionMessages(s.workset.MessagesBySession[sessionID]),
		Events:           cloneSessionV3Events(s.workset.EventsBySession[sessionID]),
		PendingPerms:     clonePermissionRecords(s.workset.PermissionsBySession[sessionID]),
		Preference:       s.workset.PreferencesBySession[sessionID],
		AgentModelPolicy: s.workset.AgentModelPolicyBySession[sessionID],
		Plans:            cloneSessionPlans(s.workset.PlansBySession[sessionID]),
		PlanRevisions:    cloneSessionPlans(s.workset.PlanRevisionsBySession[sessionID]),
		EndpointCursor:   strings.TrimSpace(s.workset.SnapshotEndpointCursor),
		Hydrated:         s.hydratedSessions[sessionID],
	}
	if usage, ok := s.workset.UsageBySession[sessionID]; ok {
		usageCopy := usage
		snapshot.UsageSummary = &usageCopy
	}
	if intents := s.workset.RunIntentsBySession[sessionID]; len(intents) > 0 {
		intent := intents[0]
		snapshot.ActiveRunIntent = &intent
	}
	snapshot.Summary = modelSessionSummariesFromTUIWorkset(client.SessionV3Workset{
		SessionsByID:              map[string]client.SessionSummary{sessionID: snapshot.Session},
		ProjectionsBySession:      map[string]client.SessionV3Projection{sessionID: snapshot.Projection},
		PermissionsBySession:      map[string][]client.PermissionRecord{sessionID: snapshot.PendingPerms},
		UsageBySession:            usageMapForSnapshot(sessionID, snapshot.UsageSummary),
		PreferencesBySession:      map[string]client.ModelPreference{sessionID: snapshot.Preference},
		AgentModelPolicyBySession: map[string]client.SessionV3AgentModelPolicy{sessionID: snapshot.AgentModelPolicy},
		RunIntentsBySession:       runIntentMapForSnapshot(sessionID, snapshot.ActiveRunIntent),
		PlansBySession:            map[string][]client.SessionPlan{sessionID: snapshot.Plans},
		PlanRevisionsBySession:    map[string][]client.SessionPlan{sessionID: snapshot.PlanRevisions},
		SessionOrder:              []string{sessionID},
	})[0]
	return snapshot, true
}

func (s *tuiSessionStore) DesiredSubscriptions(clientID string) []client.V3RealtimeSubscription {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := orderedSessionIDs(s.workset)
	out := make([]client.V3RealtimeSubscription, 0, len(ids))
	cursor := strings.TrimSpace(s.workset.SnapshotEndpointCursor)
	clientID = strings.TrimSpace(clientID)
	for _, id := range ids {
		lastSeq := uint64(0)
		if projection, ok := s.workset.ProjectionsBySession[id]; ok {
			lastSeq = projection.LastEventSeq
		}
		subscriptionID := "tui:" + id
		if clientID != "" {
			subscriptionID = clientID + ":session:" + id
		}
		out = append(out, client.V3RealtimeSubscription{
			SessionID:      id,
			EndpointCursor: cursor,
			LastSeq:        lastSeq,
			SubscriptionID: subscriptionID,
		})
	}
	return out
}

func (s *tuiSessionStore) EndpointCursor() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.workset.SnapshotEndpointCursor)
}

func (s *tuiSessionStore) WorksetSnapshot() client.SessionV3Workset {
	if s == nil {
		return client.SessionV3Workset{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSessionV3Workset(s.workset)
}

func (s *tuiSessionStore) NeedsHydration(sessionID string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	if _, ok := s.workset.SessionsByID[sessionID]; !ok {
		return true
	}
	return !s.hydratedSessions[sessionID]
}

func (s *tuiSessionStore) StaleState() tuiSessionStoreStaleState {
	if s == nil {
		return tuiSessionStoreStaleState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stale
}

func (s *tuiSessionStore) MarkScopeStale(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stale = tuiSessionStoreStaleState{Stale: true, Reason: strings.TrimSpace(reason), ScopeChanged: true}
}

func (s *tuiSessionStore) applyWorksetSessionFrameLocked(frame client.V3RealtimeFrame) tuiSessionStoreApplyResult {
	sessionID := strings.TrimSpace(frame.SessionID)
	if sessionID == "" {
		sessionID = frameSessionID(frame)
	}
	if sessionID == "" {
		return tuiSessionStoreApplyResult{}
	}
	s.ensureWorksetMapsLocked()
	if frame.Session != nil {
		session := cloneClientSessionSummary(*frame.Session)
		if session.ID == "" {
			session.ID = sessionID
		}
		if strings.TrimSpace(session.SessionAPI) == "" {
			session.SessionAPI = "v3"
		}
		s.workset.SessionsByID[sessionID] = session
	} else if _, ok := s.workset.SessionsByID[sessionID]; !ok {
		s.workset.SessionsByID[sessionID] = client.SessionSummary{ID: sessionID, SessionAPI: "v3"}
	}
	if frame.Projection != nil {
		projection := *frame.Projection
		if projection.SessionID == "" {
			projection.SessionID = sessionID
		}
		s.workset.ProjectionsBySession[sessionID] = projection
	}
	if summary, ok := s.workset.SessionsByID[sessionID]; ok {
		if projection, ok := s.workset.ProjectionsBySession[sessionID]; ok {
			summary.LastEventSeq = projection.LastEventSeq
			summary.ProjectionHighWatermarkSeq = projection.ProjectionHighWatermarkSeq
		}
		s.workset.SessionsByID[sessionID] = summary
	}
	s.prependSessionOrderLocked(sessionID)
	return tuiSessionStoreApplyResult{Changed: true, SessionID: sessionID}
}

func (s *tuiSessionStore) applyWorksetSessionRemovedLocked(frame client.V3RealtimeFrame) tuiSessionStoreApplyResult {
	sessionID := strings.TrimSpace(frame.SessionID)
	if sessionID == "" {
		sessionID = frameSessionID(frame)
	}
	if sessionID == "" {
		return tuiSessionStoreApplyResult{}
	}
	delete(s.workset.SessionsByID, sessionID)
	delete(s.workset.ProjectionsBySession, sessionID)
	delete(s.workset.MessagesBySession, sessionID)
	delete(s.workset.EventsBySession, sessionID)
	delete(s.workset.PlansBySession, sessionID)
	delete(s.workset.PlanRevisionsBySession, sessionID)
	delete(s.workset.PermissionsBySession, sessionID)
	delete(s.workset.UsageBySession, sessionID)
	delete(s.workset.PreferencesBySession, sessionID)
	delete(s.workset.AgentModelPolicyBySession, sessionID)
	delete(s.workset.RunIntentsBySession, sessionID)
	delete(s.workset.HistoryManifestsBySession, sessionID)
	delete(s.workset.HistoryChunksByID, sessionID)
	delete(s.hydratedSessions, sessionID)
	s.workset.SessionOrder = removeStringFromOrder(s.workset.SessionOrder, sessionID)
	return tuiSessionStoreApplyResult{Changed: true, SessionID: sessionID}
}

func (s *tuiSessionStore) applyEventFrameLocked(frame client.V3RealtimeFrame, sessionID string) tuiSessionStoreApplyResult {
	if frame.Event == nil {
		return tuiSessionStoreApplyResult{}
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(frame.Event.SessionID)
	}
	if sessionID == "" {
		return tuiSessionStoreApplyResult{}
	}
	seq := frame.Event.Seq
	if seq == 0 {
		seq = frame.LastSeq
	}
	projection := s.workset.ProjectionsBySession[sessionID]
	lastSeq := projection.LastEventSeq
	if seq != 0 {
		if seq <= lastSeq {
			return tuiSessionStoreApplyResult{SessionID: sessionID}
		}
	}
	s.ensureWorksetMapsLocked()
	event := cloneSessionV3Event(*frame.Event)
	if event.SessionID == "" {
		event.SessionID = sessionID
	}
	if event.Seq == 0 {
		event.Seq = seq
	}
	s.workset.EventsBySession[sessionID] = appendOrReplaceV3Event(s.workset.EventsBySession[sessionID], event)
	projection.SessionID = sessionID
	if seq > projection.LastEventSeq {
		projection.LastEventSeq = seq
	}
	if frame.HighWatermarkSeq > projection.ProjectionHighWatermarkSeq {
		projection.ProjectionHighWatermarkSeq = frame.HighWatermarkSeq
	}
	if frame.Projection != nil {
		projection = *frame.Projection
		if projection.SessionID == "" {
			projection.SessionID = sessionID
		}
	}
	s.workset.ProjectionsBySession[sessionID] = projection
	changed := s.applyEventPayloadLocked(sessionID, event)
	return tuiSessionStoreApplyResult{Changed: changed || seq != 0, SessionID: sessionID}
}

func (s *tuiSessionStore) applyEventPayloadLocked(sessionID string, event client.SessionV3Event) bool {
	s.ensureWorksetMapsLocked()
	changed := false
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if eventType == "" {
		eventType = strings.ToLower(strings.TrimSpace(eventTypeFromPayload(event.Payload)))
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(event.Payload, &payload)

	if raw := firstRaw(payload, "session"); len(raw) > 0 {
		var session client.SessionSummary
		if json.Unmarshal(raw, &session) == nil && strings.TrimSpace(session.ID) != "" {
			if strings.TrimSpace(session.SessionAPI) == "" {
				session.SessionAPI = "v3"
			}
			s.workset.SessionsByID[session.ID] = cloneClientSessionSummary(session)
			s.prependSessionOrderLocked(session.ID)
			changed = true
		}
	}
	if _, ok := s.workset.SessionsByID[sessionID]; !ok {
		s.workset.SessionsByID[sessionID] = client.SessionSummary{ID: sessionID, SessionAPI: "v3"}
		s.prependSessionOrderLocked(sessionID)
		changed = true
	}

	summary := s.workset.SessionsByID[sessionID]
	summary.ID = sessionID
	if strings.TrimSpace(summary.SessionAPI) == "" {
		summary.SessionAPI = "v3"
	}
	if raw := firstRaw(payload, "title"); len(raw) > 0 {
		var title string
		if json.Unmarshal(raw, &title) == nil && strings.TrimSpace(title) != "" {
			summary.Title = strings.TrimSpace(title)
			changed = true
		}
	}
	if raw := firstRaw(payload, "mode"); len(raw) > 0 {
		var mode string
		if json.Unmarshal(raw, &mode) == nil && strings.TrimSpace(mode) != "" {
			summary.Mode = strings.TrimSpace(mode)
			changed = true
		}
	}
	if raw := firstRaw(payload, "workspace_path"); len(raw) > 0 {
		var workspacePath string
		if json.Unmarshal(raw, &workspacePath) == nil && strings.TrimSpace(workspacePath) != "" {
			summary.WorkspacePath = strings.TrimSpace(workspacePath)
			changed = true
		}
	}
	if raw := firstRaw(payload, "workspace_name"); len(raw) > 0 {
		var workspaceName string
		if json.Unmarshal(raw, &workspaceName) == nil && strings.TrimSpace(workspaceName) != "" {
			summary.WorkspaceName = strings.TrimSpace(workspaceName)
			changed = true
		}
	}
	if raw := firstRaw(payload, "metadata"); len(raw) > 0 {
		var metadata map[string]any
		if json.Unmarshal(raw, &metadata) == nil {
			summary.Metadata = cloneMetadataMap(metadata)
			changed = true
		}
	}
	if raw := firstRaw(payload, "lifecycle"); len(raw) > 0 {
		var lifecycle client.SessionLifecycleSnapshot
		if json.Unmarshal(raw, &lifecycle) == nil {
			summary.Lifecycle = cloneClientSessionLifecycle(&lifecycle)
			changed = true
		}
	}
	if raw := firstRaw(payload, "message"); len(raw) > 0 {
		var message client.SessionMessage
		if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message.ID) != "" {
			if message.SessionID == "" {
				message.SessionID = sessionID
			}
			s.workset.MessagesBySession[sessionID] = appendOrReplaceMessage(s.workset.MessagesBySession[sessionID], message)
			summary.MessageCount = len(s.workset.MessagesBySession[sessionID])
			if message.CreatedAt > summary.LastMessageAt {
				summary.LastMessageAt = message.CreatedAt
			}
			changed = true
		}
	}
	if raw := firstRaw(payload, "permission"); len(raw) > 0 {
		var permission client.PermissionRecord
		if json.Unmarshal(raw, &permission) == nil && strings.TrimSpace(permission.ID) != "" {
			if permission.SessionID == "" {
				permission.SessionID = sessionID
			}
			s.workset.PermissionsBySession[sessionID] = appendOrReplacePermission(s.workset.PermissionsBySession[sessionID], permission)
			summary.PendingPermissionCount = countPendingPermissions(s.workset.PermissionsBySession[sessionID])
			changed = true
		}
	}
	if raw := firstRaw(payload, "pending_count", "pending_permission_count"); len(raw) > 0 {
		var count int
		if json.Unmarshal(raw, &count) == nil {
			summary.PendingPermissionCount = count
			changed = true
		}
	}
	if raw := firstRaw(payload, "usage_summary", "usage"); len(raw) > 0 {
		var usage client.SessionUsageSummary
		if json.Unmarshal(raw, &usage) == nil {
			if usage.SessionID == "" {
				usage.SessionID = sessionID
			}
			s.workset.UsageBySession[sessionID] = usage
			changed = true
		}
	}
	if raw := firstRaw(payload, "preference"); len(raw) > 0 {
		var preference client.ModelPreference
		if json.Unmarshal(raw, &preference) == nil {
			s.workset.PreferencesBySession[sessionID] = preference
			summary.Preference = mergeClientModelPreference(summary.Preference, preference)
			changed = true
		}
	}
	if raw := firstRaw(payload, "agent_model_policy"); len(raw) > 0 {
		var policy client.SessionV3AgentModelPolicy
		if json.Unmarshal(raw, &policy) == nil {
			s.workset.AgentModelPolicyBySession[sessionID] = policy
			changed = true
		}
	}
	if raw := firstRaw(payload, "run_intent"); len(raw) > 0 {
		var intent client.SessionV3RunIntent
		if json.Unmarshal(raw, &intent) == nil && strings.TrimSpace(intent.RunID) != "" {
			if intent.SessionID == "" {
				intent.SessionID = sessionID
			}
			priorIntents := s.workset.RunIntentsBySession[sessionID]
			currentIntents := []client.SessionV3RunIntent{intent}
			for _, prior := range priorIntents {
				if strings.TrimSpace(prior.RunID) != strings.TrimSpace(intent.RunID) {
					currentIntents = append(currentIntents, prior)
				}
			}
			s.workset.RunIntentsBySession[sessionID] = currentIntents
			if lifecycle := v3RunIntentSessionLifecycle(sessionID, &intent); lifecycle != nil {
				summary.Lifecycle = lifecycle
			}
			changed = true
		}
	}
	if raw := firstRaw(payload, "plan"); len(raw) > 0 {
		var plan client.SessionPlan
		if json.Unmarshal(raw, &plan) == nil && strings.TrimSpace(plan.ID) != "" {
			// Durable lifecycle events carry the complete current plan. Keep a single
			// active authority while retaining non-active versions as revisions.
			if plan.Active {
				for _, prior := range s.workset.PlansBySession[sessionID] {
					if prior.ID != plan.ID || prior.Version != plan.Version {
						prior.Active = false
						s.workset.PlanRevisionsBySession[sessionID] = appendOrReplacePlan(s.workset.PlanRevisionsBySession[sessionID], prior)
					}
				}
				s.workset.PlansBySession[sessionID] = []client.SessionPlan{plan}
			} else {
				s.workset.PlanRevisionsBySession[sessionID] = appendOrReplacePlan(s.workset.PlanRevisionsBySession[sessionID], plan)
			}
			changed = true
		}
	}
	if raw := firstRaw(payload, "plan_revisions", "revisions"); len(raw) > 0 {
		var revisions []client.SessionPlan
		if json.Unmarshal(raw, &revisions) == nil {
			s.workset.PlanRevisionsBySession[sessionID] = cloneSessionPlans(revisions)
			changed = true
		}
	}
	if event.Seq > summary.LastEventSeq {
		summary.LastEventSeq = event.Seq
	}
	if projection, ok := s.workset.ProjectionsBySession[sessionID]; ok {
		summary.LastEventSeq = projection.LastEventSeq
		summary.ProjectionHighWatermarkSeq = projection.ProjectionHighWatermarkSeq
	}
	s.workset.SessionsByID[sessionID] = summary
	return changed
}

func (s *tuiSessionStore) mergeHydratedLocked(hydrated client.SessionV3Hydrated) {
	s.ensureWorksetMapsLocked()
	sessionID := strings.TrimSpace(hydrated.Session.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(hydrated.Projection.SessionID)
	}
	if sessionID == "" {
		return
	}
	session := cloneClientSessionSummary(hydrated.Session)
	if session.ID == "" {
		session.ID = sessionID
	}
	if strings.TrimSpace(session.SessionAPI) == "" {
		session.SessionAPI = "v3"
	}
	if hydrated.ActiveRunIntent != nil {
		if lifecycle := v3RunIntentSessionLifecycle(sessionID, hydrated.ActiveRunIntent); lifecycle != nil {
			session.Lifecycle = lifecycle
		}
	}
	if len(hydrated.PendingPermissions) > 0 {
		session.PendingPermissionCount = countPendingPermissions(hydrated.PendingPermissions)
	}
	if hydrated.Projection.SessionID == "" {
		hydrated.Projection.SessionID = sessionID
	}
	session.LastEventSeq = hydrated.Projection.LastEventSeq
	session.ProjectionHighWatermarkSeq = hydrated.Projection.ProjectionHighWatermarkSeq
	s.workset.SessionsByID[sessionID] = session
	s.prependSessionOrderLocked(sessionID)
	s.workset.ProjectionsBySession[sessionID] = hydrated.Projection
	s.workset.MessagesBySession[sessionID] = cloneSessionMessages(hydrated.Messages)
	s.workset.EventsBySession[sessionID] = cloneSessionV3Events(hydrated.Events)
	s.workset.PermissionsBySession[sessionID] = clonePermissionRecords(hydrated.PendingPermissions)
	if hydrated.UsageSummary != nil {
		s.workset.UsageBySession[sessionID] = *hydrated.UsageSummary
	}
	s.workset.PreferencesBySession[sessionID] = hydrated.Preference
	s.workset.AgentModelPolicyBySession[sessionID] = hydrated.AgentModelPolicy
	if hydrated.ActiveRunIntent != nil {
		s.workset.RunIntentsBySession[sessionID] = []client.SessionV3RunIntent{*hydrated.ActiveRunIntent}
	}
	if hydrated.ActivePlan != nil {
		s.workset.PlansBySession[sessionID] = []client.SessionPlan{*hydrated.ActivePlan}
	}
	s.workset.PlanRevisionsBySession[sessionID] = cloneSessionPlans(hydrated.PlanRevisions)
	s.hydratedSessions[sessionID] = true
}

func (s *tuiSessionStore) markStaleLocked(sessionID, reason string) tuiSessionStoreApplyResult {
	s.stale = tuiSessionStoreStaleState{Stale: true, SessionID: strings.TrimSpace(sessionID), Reason: strings.TrimSpace(reason)}
	return tuiSessionStoreApplyResult{NeedsRehydrate: true, SessionID: strings.TrimSpace(sessionID), Reason: strings.TrimSpace(reason)}
}

func (s *tuiSessionStore) ensureWorksetMapsLocked() {
	if s.workset.SessionsByID == nil {
		s.workset.SessionsByID = make(map[string]client.SessionSummary)
	}
	if s.workset.ProjectionsBySession == nil {
		s.workset.ProjectionsBySession = make(map[string]client.SessionV3Projection)
	}
	if s.workset.MessagesBySession == nil {
		s.workset.MessagesBySession = make(map[string][]client.SessionMessage)
	}
	if s.workset.EventsBySession == nil {
		s.workset.EventsBySession = make(map[string][]client.SessionV3Event)
	}
	if s.workset.PlansBySession == nil {
		s.workset.PlansBySession = make(map[string][]client.SessionPlan)
	}
	if s.workset.PlanRevisionsBySession == nil {
		s.workset.PlanRevisionsBySession = make(map[string][]client.SessionPlan)
	}
	if s.workset.PermissionsBySession == nil {
		s.workset.PermissionsBySession = make(map[string][]client.PermissionRecord)
	}
	if s.workset.UsageBySession == nil {
		s.workset.UsageBySession = make(map[string]client.SessionUsageSummary)
	}
	if s.workset.PreferencesBySession == nil {
		s.workset.PreferencesBySession = make(map[string]client.ModelPreference)
	}
	if s.workset.AgentModelPolicyBySession == nil {
		s.workset.AgentModelPolicyBySession = make(map[string]client.SessionV3AgentModelPolicy)
	}
	if s.workset.RunIntentsBySession == nil {
		s.workset.RunIntentsBySession = make(map[string][]client.SessionV3RunIntent)
	}
	if s.workset.HistoryManifestsBySession == nil {
		s.workset.HistoryManifestsBySession = make(map[string][]client.SessionV3HistoryManifestItem)
	}
	if s.workset.HistoryChunksByID == nil {
		s.workset.HistoryChunksByID = make(map[string]client.SessionV3HistoryChunk)
	}
	if s.hydratedSessions == nil {
		s.hydratedSessions = make(map[string]bool)
	}
}

func (s *tuiSessionStore) prependSessionOrderLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	next := []string{sessionID}
	for _, existing := range s.workset.SessionOrder {
		if strings.TrimSpace(existing) != sessionID && strings.TrimSpace(existing) != "" {
			next = append(next, existing)
		}
	}
	s.workset.SessionOrder = next
}

func removeStringFromOrder(order []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" || len(order) == 0 {
		return order
	}
	next := make([]string, 0, len(order))
	for _, value := range order {
		value = strings.TrimSpace(value)
		if value == "" || value == target {
			continue
		}
		next = append(next, value)
	}
	return next
}

func mergeSessionV3Workset(dst *client.SessionV3Workset, incoming client.SessionV3Workset) {
	if dst == nil {
		return
	}
	base := cloneSessionV3Workset(*dst)
	ensureSessionV3WorksetMaps(&base)
	page := cloneSessionV3Workset(incoming)
	if cursor := strings.TrimSpace(page.SnapshotEndpointCursor); cursor != "" {
		base.SnapshotEndpointCursor = cursor
	}
	base.OK = page.OK || base.OK
	if page.Rev > base.Rev {
		base.Rev = page.Rev
	}
	mergeMap(base.SessionsByID, page.SessionsByID)
	mergeMap(base.ProjectionsBySession, page.ProjectionsBySession)
	mergeMap(base.MessagesBySession, page.MessagesBySession)
	mergeMap(base.EventsBySession, page.EventsBySession)
	mergeMap(base.PlansBySession, page.PlansBySession)
	mergeMap(base.PlanRevisionsBySession, page.PlanRevisionsBySession)
	mergeMap(base.PermissionsBySession, page.PermissionsBySession)
	mergeMap(base.UsageBySession, page.UsageBySession)
	mergeMap(base.PreferencesBySession, page.PreferencesBySession)
	mergeMap(base.AgentModelPolicyBySession, page.AgentModelPolicyBySession)
	mergeMap(base.RunIntentsBySession, page.RunIntentsBySession)
	mergeMap(base.HistoryManifestsBySession, page.HistoryManifestsBySession)
	mergeMap(base.HistoryChunksByID, page.HistoryChunksByID)
	base.Omissions = append(base.Omissions, page.Omissions...)
	base.Pagination = page.Pagination
	base.Watermarks = page.Watermarks
	base.SessionOrder = mergeSessionOrder(base.SessionOrder, page.SessionOrder)
	*dst = base
}

func ensureSessionV3WorksetMaps(workset *client.SessionV3Workset) {
	if workset.SessionsByID == nil {
		workset.SessionsByID = make(map[string]client.SessionSummary)
	}
	if workset.ProjectionsBySession == nil {
		workset.ProjectionsBySession = make(map[string]client.SessionV3Projection)
	}
	if workset.MessagesBySession == nil {
		workset.MessagesBySession = make(map[string][]client.SessionMessage)
	}
	if workset.EventsBySession == nil {
		workset.EventsBySession = make(map[string][]client.SessionV3Event)
	}
	if workset.PlansBySession == nil {
		workset.PlansBySession = make(map[string][]client.SessionPlan)
	}
	if workset.PlanRevisionsBySession == nil {
		workset.PlanRevisionsBySession = make(map[string][]client.SessionPlan)
	}
	if workset.PermissionsBySession == nil {
		workset.PermissionsBySession = make(map[string][]client.PermissionRecord)
	}
	if workset.UsageBySession == nil {
		workset.UsageBySession = make(map[string]client.SessionUsageSummary)
	}
	if workset.PreferencesBySession == nil {
		workset.PreferencesBySession = make(map[string]client.ModelPreference)
	}
	if workset.AgentModelPolicyBySession == nil {
		workset.AgentModelPolicyBySession = make(map[string]client.SessionV3AgentModelPolicy)
	}
	if workset.RunIntentsBySession == nil {
		workset.RunIntentsBySession = make(map[string][]client.SessionV3RunIntent)
	}
	if workset.HistoryManifestsBySession == nil {
		workset.HistoryManifestsBySession = make(map[string][]client.SessionV3HistoryManifestItem)
	}
	if workset.HistoryChunksByID == nil {
		workset.HistoryChunksByID = make(map[string]client.SessionV3HistoryChunk)
	}
}

func cloneSessionV3Workset(in client.SessionV3Workset) client.SessionV3Workset {
	out := in
	out.SessionsByID = cloneMap(in.SessionsByID, cloneClientSessionSummary)
	out.ProjectionsBySession = cloneMapIdentity(in.ProjectionsBySession)
	out.MessagesBySession = cloneMap(in.MessagesBySession, cloneSessionMessages)
	out.EventsBySession = cloneMap(in.EventsBySession, cloneSessionV3Events)
	out.PlansBySession = cloneMap(in.PlansBySession, cloneSessionPlans)
	out.PlanRevisionsBySession = cloneMap(in.PlanRevisionsBySession, cloneSessionPlans)
	out.PermissionsBySession = cloneMap(in.PermissionsBySession, clonePermissionRecords)
	out.UsageBySession = cloneMapIdentity(in.UsageBySession)
	out.PreferencesBySession = cloneMapIdentity(in.PreferencesBySession)
	out.AgentModelPolicyBySession = cloneMapIdentity(in.AgentModelPolicyBySession)
	out.RunIntentsBySession = cloneMap(in.RunIntentsBySession, cloneSessionV3RunIntents)
	out.HistoryManifestsBySession = cloneMap(in.HistoryManifestsBySession, cloneHistoryManifestItems)
	out.HistoryChunksByID = cloneMap(in.HistoryChunksByID, cloneHistoryChunk)
	out.Omissions = append([]client.SessionV3WorksetOmission(nil), in.Omissions...)
	out.SessionOrder = append([]string(nil), in.SessionOrder...)
	return out
}

func cloneClientSessionSummary(in client.SessionSummary) client.SessionSummary {
	out := in
	out.Metadata = cloneMetadataMap(in.Metadata)
	out.Lifecycle = cloneClientSessionLifecycle(in.Lifecycle)
	return out
}

func cloneSessionV3Event(in client.SessionV3Event) client.SessionV3Event {
	out := in
	out.Payload = append(json.RawMessage(nil), in.Payload...)
	return out
}

func cloneSessionMessages(in []client.SessionMessage) []client.SessionMessage {
	out := append([]client.SessionMessage(nil), in...)
	for i := range out {
		out[i].Metadata = cloneMetadataMap(out[i].Metadata)
	}
	return sortSessionMessages(out)
}

func cloneSessionV3Events(in []client.SessionV3Event) []client.SessionV3Event {
	out := make([]client.SessionV3Event, len(in))
	for i := range in {
		out[i] = cloneSessionV3Event(in[i])
	}
	return sortSessionV3Events(out)
}

func clonePermissionRecords(in []client.PermissionRecord) []client.PermissionRecord {
	return append([]client.PermissionRecord(nil), in...)
}

func cloneSessionPlans(in []client.SessionPlan) []client.SessionPlan {
	return append([]client.SessionPlan(nil), in...)
}

func cloneSessionV3RunIntents(in []client.SessionV3RunIntent) []client.SessionV3RunIntent {
	return append([]client.SessionV3RunIntent(nil), in...)
}

func cloneHistoryManifestItems(in []client.SessionV3HistoryManifestItem) []client.SessionV3HistoryManifestItem {
	return append([]client.SessionV3HistoryManifestItem(nil), in...)
}

func cloneHistoryChunk(in client.SessionV3HistoryChunk) client.SessionV3HistoryChunk {
	out := in
	out.Messages = cloneSessionMessages(in.Messages)
	out.Events = cloneSessionV3Events(in.Events)
	return out
}

func cloneModelSessionSummaries(in []model.SessionSummary) []model.SessionSummary {
	out := append([]model.SessionSummary(nil), in...)
	for i := range out {
		out[i].Metadata = cloneMetadataMap(out[i].Metadata)
		out[i].Lifecycle = cloneClientSessionLifecycle(out[i].Lifecycle)
	}
	return out
}

func cloneMap[K comparable, V any](in map[K]V, clone func(V) V) map[K]V {
	if in == nil {
		return nil
	}
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = clone(v)
	}
	return out
}

func cloneMapIdentity[K comparable, V any](in map[K]V) map[K]V {
	if in == nil {
		return nil
	}
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeMap[K comparable, V any](dst map[K]V, src map[K]V) {
	for k, v := range src {
		dst[k] = v
	}
}

func mergeSessionOrder(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range incoming {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func orderedSessionIDs(workset client.SessionV3Workset) []string {
	seen := make(map[string]struct{}, len(workset.SessionsByID))
	out := make([]string, 0, len(workset.SessionsByID))
	for _, id := range workset.SessionOrder {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := workset.SessionsByID[id]; !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(seen) < len(workset.SessionsByID) {
		ids := make([]string, 0, len(workset.SessionsByID)-len(seen))
		for id := range workset.SessionsByID {
			if _, ok := seen[id]; !ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		out = append(out, ids...)
	}
	return out
}

func appendOrReplaceMessage(items []client.SessionMessage, item client.SessionMessage) []client.SessionMessage {
	id := strings.TrimSpace(item.ID)
	for i := range items {
		if strings.TrimSpace(items[i].ID) == id {
			items[i] = item
			return sortSessionMessages(items)
		}
	}
	return sortSessionMessages(append(items, item))
}

func sortSessionMessages(items []client.SessionMessage) []client.SessionMessage {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
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
	return items
}

func appendOrReplaceV3Event(items []client.SessionV3Event, item client.SessionV3Event) []client.SessionV3Event {
	id := strings.TrimSpace(item.ID)
	seq := item.Seq
	for i := range items {
		if id != "" && strings.TrimSpace(items[i].ID) == id {
			items[i] = item
			return sortSessionV3Events(items)
		}
		if id == "" && seq != 0 && items[i].Seq == seq {
			items[i] = item
			return sortSessionV3Events(items)
		}
	}
	return sortSessionV3Events(append(items, item))
}

func sortSessionV3Events(items []client.SessionV3Event) []client.SessionV3Event {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if left.Seq != right.Seq {
			if left.Seq == 0 {
				return false
			}
			if right.Seq == 0 {
				return true
			}
			return left.Seq < right.Seq
		}
		if left.TsUnixMS != right.TsUnixMS {
			if left.TsUnixMS == 0 {
				return false
			}
			if right.TsUnixMS == 0 {
				return true
			}
			return left.TsUnixMS < right.TsUnixMS
		}
		return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
	})
	return items
}

func appendOrReplacePermission(items []client.PermissionRecord, item client.PermissionRecord) []client.PermissionRecord {
	id := strings.TrimSpace(item.ID)
	pending := strings.EqualFold(strings.TrimSpace(item.Status), "pending") || strings.TrimSpace(item.Status) == ""
	out := items[:0]
	replaced := false
	for _, existing := range items {
		if strings.TrimSpace(existing.ID) == id {
			replaced = true
			if pending {
				out = append(out, item)
			}
			continue
		}
		out = append(out, existing)
	}
	if !replaced && pending {
		out = append(out, item)
	}
	return out
}

func appendOrReplaceRunIntent(items []client.SessionV3RunIntent, item client.SessionV3RunIntent) []client.SessionV3RunIntent {
	for i := range items {
		if strings.TrimSpace(items[i].RunID) == strings.TrimSpace(item.RunID) {
			items[i] = item
			return sortRunIntents(items)
		}
	}
	return sortRunIntents(append(items, item))
}

func sortRunIntents(items []client.SessionV3RunIntent) []client.SessionV3RunIntent {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].RunID < items[j].RunID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	return items
}

func appendOrReplacePlan(items []client.SessionPlan, item client.SessionPlan) []client.SessionPlan {
	for i := range items {
		if strings.TrimSpace(items[i].ID) == strings.TrimSpace(item.ID) {
			items[i] = item
			return items
		}
	}
	return append(items, item)
}

func countPendingPermissions(items []client.PermissionRecord) int {
	count := 0
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Status), "pending") || strings.TrimSpace(item.Status) == "" {
			count++
		}
	}
	return count
}

func frameSessionID(frame client.V3RealtimeFrame) string {
	if frame.Event != nil {
		return strings.TrimSpace(frame.Event.SessionID)
	}
	return ""
}

func eventTypeFromPayload(raw json.RawMessage) string {
	var payload struct {
		Type      string `json:"type"`
		EventType string `json:"event_type"`
	}
	_ = json.Unmarshal(raw, &payload)
	return firstNonEmpty(payload.EventType, payload.Type)
}

func firstRaw(payload map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := payload[key]; ok {
			return raw
		}
	}
	return nil
}

func usageMapForSnapshot(sessionID string, usage *client.SessionUsageSummary) map[string]client.SessionUsageSummary {
	if usage == nil {
		return nil
	}
	return map[string]client.SessionUsageSummary{sessionID: *usage}
}

func runIntentMapForSnapshot(sessionID string, intent *client.SessionV3RunIntent) map[string][]client.SessionV3RunIntent {
	if intent == nil {
		return nil
	}
	return map[string][]client.SessionV3RunIntent{sessionID: {*intent}}
}

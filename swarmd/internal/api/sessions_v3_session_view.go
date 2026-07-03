package api

import (
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) sessionsV3SyncActivePlanViews(snapshot pebblestore.V3SyncSnapshotResult) (map[string]sessionsV3SessionView, error) {
	views := make(map[string]sessionsV3SessionView, len(snapshot.SessionOrder))
	for _, sessionID := range snapshot.SessionOrder {
		hasActive := false
		view := sessionsV3SessionView{HasActivePlan: &hasActive}
		if plan, ok, err := s.sessionsV3SyncActivePlanForSession(sessionID); err != nil {
			return nil, err
		} else if ok {
			hasActive = true
			view.ActivePlan = &plan
		}
		views[sessionID] = view
	}
	return views, nil
}

func (s *Server) sessionsV3SyncSessionViews(options sessionsV3ResolvedSyncOptions, snapshot pebblestore.V3SyncSnapshotResult) (map[string]sessionsV3SessionView, error) {
	if len(snapshot.SessionOrder) > sessionsV3SyncHydrateMaxSessionViews {
		return nil, errors.New("sync hydrate resources.session_view cannot target more than 8 sessions")
	}
	views := make(map[string]sessionsV3SessionView, len(snapshot.SessionOrder))
	for _, sessionID := range snapshot.SessionOrder {
		session, ok := snapshot.SessionsByID[sessionID]
		if !ok {
			continue
		}
		projection := snapshot.ProjectionsBySession[sessionID]
		var currentRunState *pebblestore.V3SessionRunState
		if state, ok := snapshot.CurrentRunStateBySession[sessionID]; ok {
			stateCopy := state
			currentRunState = &stateCopy
		}
		view, err := s.buildSessionsV3SessionView(options.Principal, session, projection, currentRunState, options.Snapshot.IncludeActivePlan)
		if err != nil {
			return nil, err
		}
		views[sessionID] = view
	}
	return views, nil
}

func (s *Server) buildSessionsV3SessionView(principal identity.Principal, session pebblestore.SessionSnapshot, projection pebblestore.V3SessionProjection, currentRunState *pebblestore.V3SessionRunState, includeActivePlan bool) (sessionsV3SessionView, error) {
	if strings.TrimSpace(session.ID) == "" {
		return sessionsV3SessionView{}, errors.New("session id is required")
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3SessionView{}, errors.New("session is outside principal account scope")
	}
	pendingPermissions := []pebblestore.PermissionRecord{}
	if s.perm != nil {
		permissions, err := s.perm.ListPending(session.ID, 200)
		if err != nil {
			return sessionsV3SessionView{}, err
		}
		pendingPermissions = permissions
	}
	var usageSummary *pebblestore.SessionUsageSummary
	if summary, hasSummary, err := s.sessions.Store().GetUsageSummary(session.ID); err != nil {
		return sessionsV3SessionView{}, err
	} else if hasSummary {
		usageSummary = &summary
	}
	if currentRunState == nil {
		if state, ok, err := s.sessions.GetSessionRunState(session.ID); err != nil {
			return sessionsV3SessionView{}, err
		} else if ok {
			currentRunState = &state
		}
	}

	storedPreference := normalizeSessionsV3ModelPreference(session.Preference)
	effectivePreference := storedPreference
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		if resolved, err := s.model.ResolvePreference(storedPreference); err == nil {
			effectivePreference = normalizeSessionsV3ModelPreference(resolved.Preference)
			contextWindow = resolved.ContextWindow
			maxOutputTokens = resolved.MaxOutputTokens
		}
	}
	agentModelPolicy := s.sessionsV3AgentModelPolicy(session, effectivePreference, contextWindow, maxOutputTokens)
	if agentModelPolicy.Locked {
		effectivePreference = agentModelPolicy.Preference
		contextWindow = agentModelPolicy.ContextWindow
		maxOutputTokens = agentModelPolicy.MaxOutputTokens
	}

	var hasActivePlan *bool
	var activePlan *pebblestore.SessionPlanSnapshot
	if includeActivePlan {
		hasActive := false
		hasActivePlan = &hasActive
		if plan, ok, err := s.sessionsV3SyncActivePlanForSession(session.ID); err != nil {
			return sessionsV3SessionView{}, err
		} else if ok {
			hasActive = true
			activePlan = &plan
		}
	}
	return sessionsV3SessionView{
		AgenticSettings: sessionsV3AgenticSettings{
			Mode:                strings.TrimSpace(session.Mode),
			AgentName:           sessionsV3MetadataString(session.Metadata, "agent_name"),
			ResolvedAgentName:   sessionsV3MetadataString(session.Metadata, "resolved_agent_name"),
			RuntimeMode:         sessionsV3MetadataString(session.Metadata, "runtime_mode"),
			StoredPreference:    storedPreference,
			EffectivePreference: effectivePreference,
			AgentModelPolicy:    agentModelPolicy,
			ContextWindow:       contextWindow,
			MaxOutputTokens:     maxOutputTokens,
			ProjectionSeq:       projection.LastEventSeq,
		},
		PendingPermissions: pendingPermissions,
		UsageSummary:       usageSummary,
		CurrentRunState:    currentRunState,
		HasActivePlan:      hasActivePlan,
		ActivePlan:         activePlan,
	}, nil
}

func (s *Server) sessionsV3SyncActivePlanForSession(sessionID string) (pebblestore.SessionPlanSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("session id is required")
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return pebblestore.SessionPlanSnapshot{}, false, errors.New("sessions v3 service is not configured")
	}
	active, ok, err := s.sessions.Store().GetActivePlan(sessionID)
	if err != nil || !ok {
		return pebblestore.SessionPlanSnapshot{}, ok, err
	}
	plan, found, err := s.sessions.Store().GetPlan(sessionID, active.PlanID)
	if err != nil || !found {
		return pebblestore.SessionPlanSnapshot{}, found, err
	}
	plan.Active = true
	return plan, true, nil
}

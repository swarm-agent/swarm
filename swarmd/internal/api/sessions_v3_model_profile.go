package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) resolveSessionsV3ModelProfileChoice(ctx context.Context, choice *sessionsV3ModelProfileChoice, appliedAt int64) (*pebblestore.SessionModelProfileSnapshot, error) {
	if choice == nil {
		// Omission means the caller's explicit session preference remains
		// authoritative. A saved profile is applied only when the caller chooses
		// one (including an explicit account-default choice).
		return nil, nil
	}

	useDefault := choice.UseAccountDefault != nil && *choice.UseAccountDefault
	useAgentDefault := choice.UseAgentDefault != nil && *choice.UseAgentDefault
	if choice.UseAccountDefault != nil && !*choice.UseAccountDefault {
		return nil, errors.New("use_account_default must be true when provided")
	}
	if choice.UseAgentDefault != nil && !*choice.UseAgentDefault {
		return nil, errors.New("use_agent_default must be true when provided")
	}
	selected := 0
	if useDefault {
		selected++
	}
	if strings.TrimSpace(choice.SavedProfileID) != "" {
		selected++
	}
	if choice.Temporary != nil {
		selected++
	}
	if useAgentDefault {
		selected++
	}
	if selected != 1 {
		return nil, errors.New("choose exactly one of use_account_default, saved_profile_id, temporary, or use_agent_default")
	}
	if useAgentDefault {
		return nil, nil
	}
	if s.modelProfiles == nil {
		return nil, modelprofile.ErrNotConfigured
	}
	if useDefault {
		return s.sessionModelProfileSnapshotFromAccountDefault(ctx, appliedAt)
	}
	if profileID := strings.TrimSpace(choice.SavedProfileID); profileID != "" {
		profile, err := s.modelProfiles.Get(ctx, profileID)
		if err != nil {
			return nil, err
		}
		return sessionModelProfileSnapshotFromSaved(profile, appliedAt), nil
	}
	inline := choice.Temporary
	validated, err := modelprofile.ValidateInput(modelprofile.Input{
		Name:        firstNonEmpty(inline.Name, "Temporary"),
		Provider:    inline.Provider,
		Model:       inline.Model,
		Thinking:    inline.Thinking,
		ServiceTier: inline.ServiceTier,
		ContextMode: inline.ContextMode,
	})
	if err != nil {
		return nil, err
	}
	return &pebblestore.SessionModelProfileSnapshot{
		Source:    pebblestore.SessionModelProfileSourceTemporary,
		Action:    sessionModelSelectionFromFlatProfile(validated.Provider, validated.Model, validated.Thinking, validated.ServiceTier, validated.ContextMode),
		AppliedAt: appliedAt,
	}, nil
}

func (s *Server) sessionModelProfileSnapshotFromAccountDefault(ctx context.Context, appliedAt int64) (*pebblestore.SessionModelProfileSnapshot, error) {
	if s.agentModelSettings == nil {
		return nil, agentmodelsettings.ErrNotConfigured
	}
	settings, err := s.agentModelSettings.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &pebblestore.SessionModelProfileSnapshot{
		Source:            pebblestore.SessionModelProfileSourceSwarmSettings,
		UseAccountDefault: true,
		Action:            agentModelAssignmentSelection(settings.Swarm.Action),
		Plan:              cloneAgentModelAssignmentSelection(settings.Swarm.Plan),
		AppliedAt:         appliedAt,
	}, nil
}

func agentModelAssignmentSelection(assignment pebblestore.AgentModelAssignment) pebblestore.ModelProfileSelection {
	return sessionModelSelectionFromFlatProfile(assignment.Provider, assignment.Model, assignment.Thinking, assignment.ServiceTier, assignment.ContextMode)
}

func cloneAgentModelAssignmentSelection(assignment pebblestore.AgentModelAssignment) *pebblestore.ModelProfileSelection {
	selection := agentModelAssignmentSelection(assignment)
	return &selection
}

func sessionModelProfileSnapshotFromSaved(profile modelprofile.Profile, appliedAt int64) *pebblestore.SessionModelProfileSnapshot {
	return &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		ActionFavoriteID:   profile.ProfileID,
		ActionFavoriteName: profile.Name,
		Action:             sessionModelSelectionFromFlatProfile(profile.Provider, profile.Model, profile.Thinking, profile.ServiceTier, profile.ContextMode),
		AppliedAt:          appliedAt,
	}
}

func sessionModelSelectionFromFlatProfile(provider, model, thinking, serviceTier, contextMode string) pebblestore.ModelProfileSelection {
	return pebblestore.ModelProfileSelection{
		Provider:    provider,
		Model:       model,
		Thinking:    thinking,
		ServiceTier: serviceTier,
		ContextMode: contextMode,
	}
}

func sessionsV3ModelProfileMetadata(metadata map[string]any, profile *pebblestore.SessionModelProfileSnapshot) map[string]any {
	metadata = cloneSessionsV3Metadata(metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	delete(metadata, "model_profile")
	if profile != nil {
		metadata["model_profile"] = cloneSessionsV3ModelProfileSnapshot(*profile)
	}
	return metadata
}

func cloneSessionsV3ModelProfileSnapshot(profile pebblestore.SessionModelProfileSnapshot) pebblestore.SessionModelProfileSnapshot {
	return *pebblestore.CloneSessionModelProfileSnapshot(&profile)
}

func mergeSessionsV3ModelProfileChoice(current pebblestore.SessionSnapshot, resolved *pebblestore.SessionModelProfileSnapshot) (*pebblestore.SessionModelProfileSnapshot, error) {
	if resolved == nil {
		return nil, nil
	}
	mode := sessionruntime.NormalizeMode(current.Mode)
	if current.ModelProfile == nil {
		if mode == sessionruntime.ModePlan && resolved.Plan == nil {
			return nil, errors.New("cannot set the Plan model slot before the session has an Action model slot")
		}
		return pebblestore.CloneSessionModelProfileSnapshot(resolved), nil
	}

	next := pebblestore.CloneSessionModelProfileSnapshot(current.ModelProfile)
	next.Source = resolved.Source
	next.UseAccountDefault = resolved.UseAccountDefault
	next.AppliedAt = resolved.AppliedAt
	if mode == sessionruntime.ModePlan {
		selection := &resolved.Action
		favoriteID, favoriteName := resolved.ActionFavoriteID, resolved.ActionFavoriteName
		if resolved.Plan != nil {
			selection = resolved.Plan
			favoriteID, favoriteName = resolved.PlanFavoriteID, resolved.PlanFavoriteName
		}
		next.Plan = pebblestore.CloneModelProfileSelection(selection)
		next.PlanFavoriteID = favoriteID
		next.PlanFavoriteName = favoriteName
	} else {
		next.Action = resolved.Action
		next.ActionFavoriteID = resolved.ActionFavoriteID
		next.ActionFavoriteName = resolved.ActionFavoriteName
	}
	return next, nil
}

func sessionsV3ProfilePreference(session pebblestore.SessionSnapshot) (pebblestore.ModelPreference, bool) {
	profile := session.ModelProfile
	if profile == nil {
		return pebblestore.ModelPreference{}, false
	}
	selection := &profile.Action
	if strings.EqualFold(strings.TrimSpace(session.Mode), sessionruntime.ModePlan) {
		selection = profile.Plan
	}
	if selection == nil || strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "" {
		return pebblestore.ModelPreference{}, false
	}
	return pebblestore.ModelPreference{Provider: strings.ToLower(strings.TrimSpace(selection.Provider)), Model: strings.TrimSpace(selection.Model), Thinking: strings.TrimSpace(selection.Thinking), ServiceTier: strings.TrimSpace(selection.ServiceTier), ContextMode: strings.TrimSpace(selection.ContextMode), UpdatedAt: profile.AppliedAt}, true
}

func (s *Server) handleSessionV3PrimaryModelProfile(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	current, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if s.rejectSystemSidechatMutation(w, current) {
		return
	}
	if active, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if ok && sessionV3RunIntentStatusActive(active.Status) {
		writeError(w, http.StatusConflict, errors.New("model profile cannot be changed during an active run"))
		return
	}

	var req sessionsV3ModelProfileApplyRequest
	if r.Method == http.MethodPut {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	} else {
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		req.Choice.UseAgentDefault = boolPtr(true)
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	now := time.Now().UnixMilli()
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	resolved, err := s.resolveSessionsV3ModelProfileChoice(ctx, &req.Choice, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	if resolved == nil {
		writeError(w, http.StatusConflict, errors.New("cannot clear the session model profile because the current mode requires immutable model authority; replace the current mode slot instead"))
		return
	}
	snapshot, err := mergeSessionsV3ModelProfileChoice(current, resolved)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	next := current
	next.ModelProfile = snapshot
	next.Metadata = sessionsV3ModelProfileMetadata(current.Metadata, snapshot)
	if profilePreference, ok := sessionsV3ProfilePreference(next); ok {
		next.Preference = normalizeSessionsV3ModelPreference(profilePreference)
	} else {
		writeError(w, http.StatusConflict, errors.New("session model profile current mode selection has no provider/model"))
		return
	}
	next.UpdatedAt = now
	policy := s.sessionsV3AgentModelPolicy(next, next.Preference, 0, 0)
	payload := map[string]any{"session_id": sessionID, "model_profile": snapshot, "preference": next.Preference, "agent_model_policy": policy, "updated_at": now}
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateModelProfile, payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationUpdateModelProfile, EventPayload: eventPayload, Session: &next, ExpectedLastEventSeq: req.IfProjectionSeq, NowUnixMs: now})
	if err != nil {
		var conflict *pebblestore.V3ProjectionConflictError
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": conflict})
			return
		}
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	responseSession := next
	if result.Session != nil {
		responseSession = *result.Session
	}
	responsePolicy := s.sessionsV3AgentModelPolicy(responseSession, responseSession.Preference, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": responseSession.Metadata, "model_profile": responseSession.ModelProfile, "preference": responseSession.Preference, "agent_model_policy": responsePolicy, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox})
}

func boolPtr(value bool) *bool { return &value }

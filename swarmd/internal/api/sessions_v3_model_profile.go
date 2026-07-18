package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) resolveSessionsV3ModelProfileChoice(ctx context.Context, choice *sessionsV3ModelProfileChoice, useDefaultWhenOmitted bool, appliedAt int64) (*pebblestore.SessionModelProfileSnapshot, error) {
	if choice == nil {
		if !useDefaultWhenOmitted || s.modelProfiles == nil {
			return nil, nil
		}
		profile, ok, err := s.modelProfiles.GetDefault(ctx)
		if err != nil || !ok {
			return nil, err
		}
		return sessionModelProfileSnapshotFromSaved(profile, appliedAt), nil
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
		profile, ok, err := s.modelProfiles.GetDefault(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("account has no default model profile")
		}
		return sessionModelProfileSnapshotFromSaved(profile, appliedAt), nil
	}
	if profileID := strings.TrimSpace(choice.SavedProfileID); profileID != "" {
		profile, err := s.modelProfiles.Get(ctx, profileID)
		if err != nil {
			return nil, err
		}
		return sessionModelProfileSnapshotFromSaved(profile, appliedAt), nil
	}
	inline := choice.Temporary
	validated, err := modelprofile.ValidateInput(modelprofile.Input{Name: firstNonEmpty(inline.Name, "Temporary"), ModelMode: inline.ModelMode, Single: inline.Single, Plan: inline.Plan, Auto: inline.Auto})
	if err != nil {
		return nil, err
	}
	return &pebblestore.SessionModelProfileSnapshot{
		Source: pebblestore.SessionModelProfileSourceTemporary, Name: validated.Name, ModelMode: validated.ModelMode,
		Single: cloneSessionModelSelection(validated.Single), Plan: cloneSessionModelSelection(validated.Plan), Auto: cloneSessionModelSelection(validated.Auto), AppliedAt: appliedAt,
	}, nil
}

func sessionModelProfileSnapshotFromSaved(profile modelprofile.Profile, appliedAt int64) *pebblestore.SessionModelProfileSnapshot {
	return &pebblestore.SessionModelProfileSnapshot{
		Source: pebblestore.SessionModelProfileSourceSaved, SavedProfileID: profile.ProfileID, Name: profile.Name, ModelMode: profile.ModelMode,
		Single: cloneSessionModelSelection(profile.Single), Plan: cloneSessionModelSelection(profile.Plan), Auto: cloneSessionModelSelection(profile.Auto), AppliedAt: appliedAt,
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
	profile.Single = cloneSessionModelSelection(profile.Single)
	profile.Plan = cloneSessionModelSelection(profile.Plan)
	profile.Auto = cloneSessionModelSelection(profile.Auto)
	return profile
}

func cloneSessionModelSelection(selection *pebblestore.ModelProfileSelection) *pebblestore.ModelProfileSelection {
	if selection == nil {
		return nil
	}
	copy := *selection
	return &copy
}

func sessionsV3ProfilePreference(session pebblestore.SessionSnapshot) (pebblestore.ModelPreference, bool) {
	profile := session.ModelProfile
	if profile == nil {
		return pebblestore.ModelPreference{}, false
	}
	selection := profile.Single
	if profile.ModelMode == pebblestore.ModelProfileModeSplit {
		if strings.EqualFold(strings.TrimSpace(session.Mode), sessionruntime.ModePlan) {
			selection = profile.Plan
		} else {
			selection = profile.Auto
		}
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
	snapshot, err := s.resolveSessionsV3ModelProfileChoice(ctx, &req.Choice, false, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	next := current
	next.ModelProfile = snapshot
	next.Metadata = sessionsV3ModelProfileMetadata(current.Metadata, snapshot)
	next.UpdatedAt = now
	payload := map[string]any{"session_id": sessionID, "model_profile": snapshot, "updated_at": now}
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateModelProfile, payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationUpdateModelProfile, Session: &next, ExpectedLastEventSeq: req.IfProjectionSeq, NowUnixMs: now})
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
	policy := s.sessionsV3AgentModelPolicy(next, next.Preference, 0, 0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": next.Metadata, "model_profile": snapshot, "agent_model_policy": policy, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox})
}

func boolPtr(value bool) *bool { return &value }

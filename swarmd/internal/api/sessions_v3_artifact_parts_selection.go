package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3ArtifactPartChoiceRequest struct {
	PartID           string                                           `json:"part_id"`
	Revision         pebblestore.SessionArtifactPartRevisionReference `json:"revision"`
	RevisionEventSeq uint64                                           `json:"revision_event_seq"`
	Locked           bool                                             `json:"locked"`
}

func (s *Server) handleSessionV3ArtifactPartSelection(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if s.artifacts == nil {
		writeError(w, http.StatusInternalServerError, errors.New("artifact authority is not configured"))
		return
	}
	var req struct {
		ClientRequestID string                                `json:"client_request_id"`
		EventSeq        uint64                                `json:"event_seq"`
		Choices         []sessionsV3ArtifactPartChoiceRequest `json:"choices"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ClientRequestID == "" || len(req.ClientRequestID) > 256 || req.EventSeq == 0 || len(req.Choices) == 0 || len(req.Choices) > pebblestore.SessionArtifactMaxParts {
		writeError(w, http.StatusBadRequest, errors.New("part selection requires client_request_id, exact source event, and bounded choices"))
		return
	}
	source, ok, err := s.sessions.GetSessionArtifactVariantByID(principal.AccountScopeID, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok || source.Status != pebblestore.SessionArtifactStatusReady || source.EventSeq != req.EventSeq || source.Composition == nil || source.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative {
		writeError(w, http.StatusConflict, errors.New("artifact part selection source is stale or not authoritative"))
		return
	}
	choices := make([]artifact.PartRevisionChoiceInput, 0, len(req.Choices))
	for _, choice := range req.Choices {
		choices = append(choices, artifact.PartRevisionChoiceInput{PartID: choice.PartID, Revision: choice.Revision, RevisionEventSeq: choice.RevisionEventSeq, Locked: choice.Locked})
	}
	principalContext := artifact.Principal{SessionID: session.ID, AccountScopeID: session.AccountScopeID, UserID: session.UserID}
	authority := artifact.NewAuthority(s.artifacts, s.sessions)
	digest := sha256.Sum256([]byte(session.ID + "\x00" + req.ClientRequestID))
	seed := fmt.Sprintf("%x", digest[:12])
	created, err := authority.SelectPartRevisions(r.Context(), principalContext, artifact.SelectPartRevisionsInput{RequestID: "api-part-selection-" + seed, CollectionID: "part-selection-" + seed, VariantID: "part-selection-" + seed, ArtifactStepID: "part-selection-" + seed, SourceArtifact: pebblestore.SessionArtifactSelectionReference{SessionID: source.SessionID, CollectionID: source.CollectionID, VariantID: source.ID, EventSeq: source.EventSeq}, SourceComposition: *source.Composition, Choices: choices})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "mixed") || strings.Contains(err.Error(), "duplicate") {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"ok": false, "error_code": "artifact_part_selection_conflict", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reference": pebblestore.SessionArtifactSelectionReference{SessionID: created.SessionID, CollectionID: created.CollectionID, VariantID: created.ID, EventSeq: created.EventSeq}, "composition": created.Composition})
}

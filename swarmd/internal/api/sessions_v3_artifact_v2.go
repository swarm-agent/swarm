package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/artifactv2"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3ArtifactV2ListLimit = 256

type sessionsV3ArtifactV2CatalogItem struct {
	SchemaVersion int                                   `json:"schema_version"`
	ReadOnly      bool                                  `json:"read_only"`
	Working       pebblestore.ArtifactV2WorkingArtifact `json:"working"`
	Projection    pebblestore.ArtifactV2Projection      `json:"projection"`
}

type sessionsV3ArtifactV2Studio struct {
	SchemaVersion int                                      `json:"schema_version"`
	ReadOnly      bool                                     `json:"read_only"`
	Working       pebblestore.ArtifactV2WorkingArtifact    `json:"working"`
	Projection    pebblestore.ArtifactV2Projection         `json:"projection"`
	Parts         []pebblestore.ArtifactV2Part             `json:"parts"`
	PartRevisions []pebblestore.ArtifactV2PartRevision     `json:"part_revisions"`
	Compositions  []pebblestore.ArtifactV2Composition      `json:"compositions"`
	Builds        []pebblestore.ArtifactV2BuildResult      `json:"builds"`
	Validations   []pebblestore.ArtifactV2ValidationResult `json:"validations"`
	Derivatives   []pebblestore.ArtifactV2Derivative       `json:"derivatives"`
	Iterations    []pebblestore.ArtifactV2IterationRound   `json:"iterations"`
	Published     []pebblestore.ArtifactV2PublishedHead    `json:"published_heads"`
}

func (s *Server) SetArtifactV2Service(service *artifactv2.Service) {
	if s != nil {
		s.artifactV2 = service
	}
}

func (s *Server) handleSessionV3ArtifactV2(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	if s == nil || s.sessions == nil || s.artifactV2 == nil {
		writeError(w, http.StatusInternalServerError, errors.New("artifact v2 service is not configured"))
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

	tail = strings.Trim(strings.TrimSpace(tail), "/")
	if tail == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		working, err := s.sessions.ListArtifactV2WorkingForSession(principal.AccountScopeID, sessionID, sessionsV3ArtifactV2ListLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items := make([]sessionsV3ArtifactV2CatalogItem, 0, len(working))
		for _, artifact := range working {
			if artifact.UserID != principal.UserID || artifact.SessionID != session.ID {
				writeError(w, http.StatusInternalServerError, errors.New("artifact v2 session index contains foreign state"))
				return
			}
			parts, err := s.sessions.ListArtifactV2Parts(principal.AccountScopeID, artifact.ID, sessionsV3ArtifactV2ListLimit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			items = append(items, sessionsV3ArtifactV2CatalogItem{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ReadOnly: true, Working: artifact, Projection: artifactV2APIProjection(artifact, len(parts))})
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Working.UpdatedAt > items[j].Working.UpdatedAt || items[i].Working.UpdatedAt == items[j].Working.UpdatedAt && items[i].Working.ID < items[j].Working.ID
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": items})
		return
	}

	artifactID, action, hasAction := strings.Cut(tail, "/")
	artifactID, action = strings.TrimSpace(artifactID), strings.TrimSpace(action)
	if artifactID == "" || strings.Contains(artifactID, "/") || hasAction && strings.Contains(action, "/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid artifact v2 path"))
		return
	}
	working, ok, err := s.sessions.GetArtifactV2Working(principal.AccountScopeID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok || working.SessionID != sessionID || working.UserID != principal.UserID {
		writeSessionNotFound(w)
		return
	}

	if !hasAction {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		studio, err := s.artifactV2Studio(principal, working)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": studio})
		return
	}
	if action == "preview" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if working.LatestBuildID == "" || working.LatestValidationID == "" || (working.State != pebblestore.ArtifactV2StateReady && working.State != pebblestore.ArtifactV2StatePublishedView) {
			writeError(w, http.StatusConflict, errors.New("artifact v2 preview requires an exact valid build"))
			return
		}
		body, mediaType, err := s.artifactV2.ReadBuildOutput(r.Context(), artifactV2APIPrincipal(principal, sessionID), working.ID, working.LatestBuildID)
		if err != nil || mediaType != "text/html" {
			writeError(w, http.StatusConflict, errors.New("artifact v2 animation preview is unavailable"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src blob: data:; connect-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	// Artifact V2 remains registered only as authenticated historical read
	// compatibility. New managed writes, candidate selection, and composition
	// mutation are owned exclusively by Artifact V3.
	writeError(w, http.StatusGone, errors.New("artifact v2 is read-only history; create and iterate managed artifacts through Artifact V3"))
}

func (s *Server) artifactV2Studio(principal identity.Principal, working pebblestore.ArtifactV2WorkingArtifact) (sessionsV3ArtifactV2Studio, error) {
	parts, err := s.sessions.ListArtifactV2Parts(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	revisions := make([]pebblestore.ArtifactV2PartRevision, 0)
	for _, part := range parts {
		rows, err := s.sessions.ListArtifactV2PartRevisions(principal.AccountScopeID, working.ID, part.ID, sessionsV3ArtifactV2ListLimit)
		if err != nil {
			return sessionsV3ArtifactV2Studio{}, err
		}
		revisions = append(revisions, rows...)
	}
	compositions, err := s.sessions.ListArtifactV2Compositions(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	builds, err := s.sessions.ListArtifactV2Builds(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	validations, err := s.sessions.ListArtifactV2Validations(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	derivatives, err := s.sessions.ListArtifactV2Derivatives(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	iterations, err := s.sessions.ListArtifactV2Iterations(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	published, err := s.sessions.ListArtifactV2PublishedHeads(principal.AccountScopeID, working.ID, sessionsV3ArtifactV2ListLimit)
	if err != nil {
		return sessionsV3ArtifactV2Studio{}, err
	}
	return sessionsV3ArtifactV2Studio{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ReadOnly: true, Working: working, Projection: artifactV2APIProjection(working, len(parts)), Parts: parts, PartRevisions: revisions, Compositions: compositions, Builds: builds, Validations: validations, Derivatives: derivatives, Iterations: iterations, Published: published}, nil
}

func artifactV2APIProjection(working pebblestore.ArtifactV2WorkingArtifact, partCount int) pebblestore.ArtifactV2Projection {
	return pebblestore.ArtifactV2Projection{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ArtifactID: working.ID, SessionID: working.SessionID, Kind: working.Kind, State: working.State, Revision: working.Revision, EventSeq: working.EventSeq, PartCount: partCount, CompositionHead: working.CompositionHead, LatestBuildID: working.LatestBuildID, LatestValidationID: working.LatestValidationID, ActiveIterationID: working.ActiveIterationID, PublishedHead: working.PublishedHead, LatestDiagnostic: working.LatestDiagnostic, UpdatedAt: working.UpdatedAt}
}

func artifactV2APIPrincipal(principal identity.Principal, sessionID string) artifactv2.Principal {
	return artifactv2.Principal{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, ActorClass: "user"}
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	artifactV3EventPrefix         = "artifact.v3."
	artifactV3ProjectionEventType = "artifact.v3.projection.updated"
	artifactV3DefaultPageLimit    = 100
	artifactV3MaximumPageLimit    = 500
)

// ArtifactV3Service is the API-facing boundary over the authoritative Artifact
// V3 repository and turn engine. It exposes complete Git revisions and never
// translates collection/variant or Artifact V2 state.
type ArtifactV3Service interface {
	ListArtifacts(context.Context, ArtifactV3Principal, string, int) ([]ArtifactV3Artifact, error)
	GetArtifact(context.Context, ArtifactV3Principal, string, string) (ArtifactV3Artifact, error)
	ListRevisions(context.Context, ArtifactV3Principal, string, string, string, int) (ArtifactV3RevisionPage, error)
	GetRevision(context.Context, ArtifactV3Principal, string, string, string) (ArtifactV3Revision, error)
	OpenPreview(context.Context, ArtifactV3Principal, string, string, string, string, string) (ArtifactV3Preview, error)
	OpenTurn(context.Context, ArtifactV3Principal, ArtifactV3OpenTurnRequest) (ArtifactV3Turn, error)
	SelectCandidate(context.Context, ArtifactV3Principal, ArtifactV3SelectCandidateRequest) (ArtifactV3SelectionResult, error)
}

type ArtifactV3Principal struct {
	AccountScopeID string
	UserID         string
}

type ArtifactV3Artifact struct {
	ID              string                       `json:"id"`
	OwnerSessionID  string                       `json:"owner_session_id"`
	IntentReference string                       `json:"intent_reference,omitempty"`
	ArtifactRef     string                       `json:"artifact_ref"`
	Status          string                       `json:"status"`
	Revision        uint64                       `json:"revision"`
	PartCount       int                          `json:"part_count"`
	Parts           []pebblestore.ArtifactV3Part `json:"parts"`
	Head            *ArtifactV3Revision          `json:"head,omitempty"`
	CurrentRevision *ArtifactV3Revision          `json:"current_revision,omitempty"`
	Revisions       []ArtifactV3Revision         `json:"revisions,omitempty"`
	Turns           []ArtifactV3Turn             `json:"turns,omitempty"`
	UpdatedAt       int64                        `json:"updated_at"`
}

type ArtifactV3Revision struct {
	RevisionRef     string                         `json:"revision_ref"`
	CommitOID       string                         `json:"commit_oid"`
	TreeOID         string                         `json:"tree_oid"`
	ManifestBlobOID string                         `json:"manifest_blob_oid"`
	Parents         []string                       `json:"parents,omitempty"`
	Manifest        pebblestore.ArtifactV3Manifest `json:"manifest"`
	FileCount       int                            `json:"file_count"`
	TreeBytes       int64                          `json:"tree_bytes"`
	ChangedFiles    []string                       `json:"changed_files,omitempty"`
	ChangedParts    []string                       `json:"changed_parts,omitempty"`
	Build           *ArtifactV3BuildEvidence       `json:"build,omitempty"`
	Validation      *ArtifactV3ValidationEvidence  `json:"validation,omitempty"`
	CreatedAt       int64                          `json:"created_at,omitempty"`
}

type ArtifactV3RevisionPage struct {
	Revisions  []ArtifactV3Revision `json:"revisions"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type ArtifactV3BuildEvidence struct {
	ID          string                 `json:"id"`
	Status      string                 `json:"status"`
	CommitOID   string                 `json:"commit_oid"`
	TreeOID     string                 `json:"tree_oid"`
	Diagnostics []ArtifactV3Diagnostic `json:"diagnostics,omitempty"`
}

type ArtifactV3ValidationEvidence struct {
	ID              string                 `json:"id"`
	Status          string                 `json:"status"`
	CommitOID       string                 `json:"commit_oid"`
	TreeOID         string                 `json:"tree_oid"`
	EvidenceDigests []string               `json:"evidence_digests,omitempty"`
	Diagnostics     []ArtifactV3Diagnostic `json:"diagnostics,omitempty"`
}

type ArtifactV3Diagnostic struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type ArtifactV3Candidate struct {
	CandidateID string                        `json:"candidate_id"`
	Status      string                        `json:"status"`
	Revision    *ArtifactV3Revision           `json:"revision,omitempty"`
	Build       *ArtifactV3BuildEvidence      `json:"build,omitempty"`
	Validation  *ArtifactV3ValidationEvidence `json:"validation,omitempty"`
	Diagnostics []ArtifactV3Diagnostic        `json:"diagnostics,omitempty"`
}

type ArtifactV3Turn struct {
	TurnID              string                `json:"turn_id"`
	Revision            uint64                `json:"revision"`
	Status              string                `json:"status"`
	Intent              string                `json:"intent"`
	TargetPartIDs       []string              `json:"target_part_ids,omitempty"`
	BaseCommitOID       string                `json:"base_commit_oid"`
	BaseRevision        *ArtifactV3Revision   `json:"base_revision,omitempty"`
	Candidates          []ArtifactV3Candidate `json:"candidates,omitempty"`
	SelectedCandidateID string                `json:"selected_candidate_id,omitempty"`
	CreatedAt           int64                 `json:"created_at"`
	UpdatedAt           int64                 `json:"updated_at"`
}

type ArtifactV3Preview struct {
	RevisionRef string `json:"revision_ref"`
	CommitOID   string `json:"commit_oid"`
	MediaType   string `json:"media_type"`
	Body        []byte `json:"-"`
	ETag        string `json:"-"`
}

type ArtifactV3OpenTurnRequest struct {
	SessionID       string
	ArtifactID      string
	ClientRequestID string
	Intent          string
	BaseRevisionRef string
	TargetPartIDs   []string
	CandidateCount  int
}

type ArtifactV3SelectCandidateRequest struct {
	SessionID            string
	ArtifactID           string
	TurnID               string
	ClientRequestID      string
	CandidateID          string
	ExpectedHeadRef      string
	ExpectedTurnRevision uint64
}

type ArtifactV3SelectionResult struct {
	Head ArtifactV3Revision `json:"head"`
	Turn ArtifactV3Turn     `json:"turn"`
}

// ArtifactV3ProjectionPayload is embedded directly in durable artifact.v3.*
// session events. Normal V3 hydrate and realtime replay therefore project this
// native shape without a parallel transport or collection/variant translation.
type ArtifactV3ProjectionPayload struct {
	SchemaVersion int                `json:"schema_version"`
	Artifact      ArtifactV3Artifact `json:"artifact"`
	EventType     string             `json:"event_type"`
	RecordedAt    int64              `json:"recorded_at"`
}

func (s *Server) SetArtifactV3Service(service ArtifactV3Service) {
	if s != nil {
		s.artifactV3 = service
	}
}

func (s *Server) ArtifactV3Service() ArtifactV3Service {
	if s == nil {
		return nil
	}
	return s.artifactV3
}

func artifactV3APIPrincipal(principal identity.Principal) ArtifactV3Principal {
	return ArtifactV3Principal{AccountScopeID: strings.TrimSpace(principal.AccountScopeID), UserID: strings.TrimSpace(principal.UserID)}
}

func (s *Server) handleSessionV3ArtifactsV3(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	if s == nil || s.sessions == nil || s.artifactV3 == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("artifact v3 service is not configured"))
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
	if session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
		writeSessionNotFound(w)
		return
	}

	tail = strings.Trim(strings.TrimSpace(tail), "/")
	if tail == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		limit, ok := artifactV3PositiveLimit(w, r, artifactV3DefaultPageLimit)
		if !ok {
			return
		}
		artifacts, err := s.artifactV3.ListArtifacts(r.Context(), artifactV3APIPrincipal(principal), sessionID, limit)
		if err != nil {
			s.writeArtifactV3Error(w, err)
			return
		}
		sort.SliceStable(artifacts, func(i, j int) bool {
			if artifacts[i].UpdatedAt == artifacts[j].UpdatedAt {
				return artifacts[i].ID < artifacts[j].ID
			}
			return artifacts[i].UpdatedAt > artifacts[j].UpdatedAt
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": artifacts})
		return
	}

	segments := strings.Split(tail, "/")
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 path"))
			return
		}
	}
	artifactID := segments[0]
	if len(segments) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		artifact, err := s.artifactV3.GetArtifact(r.Context(), artifactV3APIPrincipal(principal), sessionID, artifactID)
		if err != nil {
			s.writeArtifactV3Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact})
		return
	}

	switch segments[1] {
	case "revisions":
		s.handleArtifactV3Revisions(w, r, principal, sessionID, artifactID, segments[2:])
	case "preview":
		if len(segments) == 3 && segments[2] == "access" {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			var request struct {
				RevisionRef string `json:"revision_ref"`
			}
			if err := decodeJSON(r, &request); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			request.RevisionRef = strings.TrimSpace(request.RevisionRef)
			if request.RevisionRef == "" {
				writeError(w, http.StatusBadRequest, errors.New("revision_ref is required"))
				return
			}
			if _, err := s.artifactV3.GetRevision(r.Context(), artifactV3APIPrincipal(principal), sessionID, artifactID, request.RevisionRef); err != nil {
				s.writeArtifactV3Error(w, err)
				return
			}
			token, err := s.issueSessionV3ArtifactPreviewTokenForRevision(principal, sessionID, artifactID, request.RevisionRef, time.Now().Add(sessionsV3ArtifactPreviewTokenTTL))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			previewURL := fmt.Sprintf("/v3/sessions/%s/artifacts-v3/%s/preview/access/%s?revision=%s", url.PathEscape(sessionID), url.PathEscape(artifactID), token, url.QueryEscape(request.RevisionRef))
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview_url": previewURL, "expires_at": time.Now().Add(sessionsV3ArtifactPreviewTokenTTL).Unix()})
			return
		}
		accessToken := ""
		if len(segments) >= 4 && segments[2] == "access" {
			accessToken = segments[3]
			segments = append(segments[:2], segments[4:]...)
		}
		validPreviewPath := len(segments) == 2 || (len(segments) >= 4 && segments[2] == "files")
		if !validPreviewPath || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			if !validPreviewPath {
				writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 preview path"))
			} else {
				methodNotAllowed(w)
			}
			return
		}
		assetPath := ""
		if len(segments) >= 4 {
			assetPath = strings.Join(segments[3:], "/")
			if decoded, decodeErr := url.PathUnescape(assetPath); decodeErr == nil {
				assetPath = decoded
			} else {
				writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 preview asset path"))
				return
			}
		}
		_ = accessToken
		revisionRef := strings.TrimSpace(r.URL.Query().Get("revision"))
		if revisionRef == "" {
			writeError(w, http.StatusBadRequest, errors.New("revision is required"))
			return
		}
		preview, err := s.artifactV3.OpenPreview(r.Context(), artifactV3APIPrincipal(principal), sessionID, artifactID, revisionRef, assetPath, accessToken)
		if err != nil {
			s.writeArtifactV3Error(w, err)
			return
		}
		w.Header().Set("Content-Type", firstNonEmpty(strings.TrimSpace(preview.MediaType), "application/octet-stream"))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Sandboxed Artifact Studio iframes have an opaque origin. Without an
		// explicit CORP policy Chromium blocks same-endpoint CSS/JS as
		// ERR_BLOCKED_BY_ORB even though the authenticated request succeeds.
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("Cache-Control", "private, no-store")
		if strings.TrimSpace(preview.ETag) != "" {
			w.Header().Set("ETag", preview.ETag)
		}
		if strings.HasPrefix(strings.ToLower(preview.MediaType), "text/html") {
			w.Header().Set("Content-Security-Policy", sessionsV3ArtifactPreviewHTMLCSP)
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(preview.Body)
		}
	case "turns":
		s.handleArtifactV3Turns(w, r, principal, sessionID, artifactID, segments[2:])
	default:
		writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 path"))
	}
}

func (s *Server) handleArtifactV3Revisions(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string, tail []string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if len(tail) == 0 {
		limit, ok := artifactV3PositiveLimit(w, r, artifactV3DefaultPageLimit)
		if !ok {
			return
		}
		page, err := s.artifactV3.ListRevisions(r.Context(), artifactV3APIPrincipal(principal), sessionID, artifactID, strings.TrimSpace(r.URL.Query().Get("cursor")), limit)
		if err != nil {
			s.writeArtifactV3Error(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revisions": page.Revisions, "next_cursor": page.NextCursor})
		return
	}
	if len(tail) != 1 {
		writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 revision path"))
		return
	}
	revision, err := s.artifactV3.GetRevision(r.Context(), artifactV3APIPrincipal(principal), sessionID, artifactID, tail[0])
	if err != nil {
		s.writeArtifactV3Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": revision})
}

func (s *Server) handleArtifactV3Turns(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string, tail []string) {
	if len(tail) == 0 {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req struct {
			ClientRequestID string   `json:"client_request_id"`
			Intent          string   `json:"intent"`
			BaseRevisionRef string   `json:"base_revision_ref"`
			TargetPartIDs   []string `json:"target_part_ids,omitempty"`
			CandidateCount  int      `json:"candidate_count,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := validateArtifactV3RequestID(req.ClientRequestID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Intent) == "" || strings.TrimSpace(req.BaseRevisionRef) == "" || req.CandidateCount < 0 || req.CandidateCount > 50 {
			writeError(w, http.StatusBadRequest, errors.New("intent, exact base_revision_ref, and a bounded candidate_count are required"))
			return
		}
		turn, err := s.artifactV3.OpenTurn(r.Context(), artifactV3APIPrincipal(principal), ArtifactV3OpenTurnRequest{SessionID: sessionID, ArtifactID: artifactID, ClientRequestID: strings.TrimSpace(req.ClientRequestID), Intent: strings.TrimSpace(req.Intent), BaseRevisionRef: strings.TrimSpace(req.BaseRevisionRef), TargetPartIDs: canonicalArtifactV3IDs(req.TargetPartIDs), CandidateCount: req.CandidateCount})
		if err != nil {
			s.writeArtifactV3Error(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "turn": turn})
		return
	}
	if len(tail) != 2 || tail[1] != "select" {
		writeError(w, http.StatusBadRequest, errors.New("invalid artifact v3 turn path"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ClientRequestID      string `json:"client_request_id"`
		CandidateID          string `json:"candidate_id"`
		ExpectedHeadRef      string `json:"expected_head_ref"`
		ExpectedTurnRevision uint64 `json:"expected_turn_revision"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateArtifactV3RequestID(req.ClientRequestID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.CandidateID) == "" || strings.TrimSpace(req.ExpectedHeadRef) == "" || req.ExpectedTurnRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("candidate_id, expected_head_ref, and expected_turn_revision are required"))
		return
	}
	result, err := s.artifactV3.SelectCandidate(r.Context(), artifactV3APIPrincipal(principal), ArtifactV3SelectCandidateRequest{SessionID: sessionID, ArtifactID: artifactID, TurnID: tail[0], ClientRequestID: strings.TrimSpace(req.ClientRequestID), CandidateID: strings.TrimSpace(req.CandidateID), ExpectedHeadRef: strings.TrimSpace(req.ExpectedHeadRef), ExpectedTurnRevision: req.ExpectedTurnRevision})
	if err != nil {
		s.writeArtifactV3Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "head": result.Head, "turn": result.Turn})
}

func artifactV3PositiveLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	limit, ok := parseRequestPositiveLimit(w, r, defaultLimit)
	if !ok {
		return 0, false
	}
	if limit > artifactV3MaximumPageLimit {
		writeError(w, http.StatusBadRequest, fmt.Errorf("artifact v3 limit cannot exceed %d", artifactV3MaximumPageLimit))
		return 0, false
	}
	return limit, true
}

func validateArtifactV3RequestID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return errors.New("client_request_id is required and must be 256 characters or fewer")
	}
	return nil
}

func canonicalArtifactV3IDs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Server) writeArtifactV3Error(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pebblestore.ErrArtifactV3Unauthorized), errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusNotFound, errors.New("artifact v3 not found"))
	case errors.Is(err, pebblestore.ErrArtifactV3NotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrArtifactV3Conflict), errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "artifact_v3_conflict", "error": err.Error()})
	case errors.Is(err, pebblestore.ErrArtifactV3Invalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, pebblestore.ErrArtifactV3Quota):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, pebblestore.ErrArtifactV3Integrity):
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "artifact_v3_integrity", "error": "artifact v3 revision is unavailable"})
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// PublishArtifactV3Projection commits a bounded native projection into the
// session event/outbox path. Git bytes and host paths are deliberately absent.
func (s *Server) PublishArtifactV3Projection(principal identity.Principal, artifact ArtifactV3Artifact, eventType, requestID string) error {
	if s == nil || s.sessions == nil || !principal.Valid() {
		return identity.ErrPrincipalRequired
	}
	if artifact.ID == "" || artifact.OwnerSessionID == "" || artifact.OwnerSessionID != strings.TrimSpace(artifact.OwnerSessionID) {
		return pebblestore.ErrArtifactV3Invalid
	}
	if _, found, err := s.requireSessionV3Access(principal, artifact.OwnerSessionID); err != nil {
		return err
	} else if !found {
		return pebblestore.ErrArtifactV3Unauthorized
	}
	eventType = strings.TrimSpace(eventType)
	if !strings.HasPrefix(eventType, artifactV3EventPrefix) {
		eventType = artifactV3ProjectionEventType
	}
	payload := ArtifactV3ProjectionPayload{SchemaVersion: 3, Artifact: artifact, EventType: eventType, RecordedAt: time.Now().UnixMilli()}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = fmt.Sprintf("artifact-v3:%s:%d:%x", artifact.ID, artifact.Revision, sum[:8])
	}
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: artifact.OwnerSessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: fmt.Sprintf("sha256:%x", sum), RequestHash: fmt.Sprintf("sha256:%x", sum),
		Kind: "artifact.v3.projection", EventType: eventType, EventPayload: raw, CorrelationID: artifact.ID, NowUnixMs: payload.RecordedAt,
	})
	return err
}

func ArtifactV3ProjectionPayloadFromRecord(record sessionruntime.RealtimeOutboxRecord) (ArtifactV3ProjectionPayload, bool) {
	if !strings.HasPrefix(strings.TrimSpace(record.Event.EventType), artifactV3EventPrefix) || len(record.Event.Payload) == 0 {
		return ArtifactV3ProjectionPayload{}, false
	}
	var payload ArtifactV3ProjectionPayload
	if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
		return ArtifactV3ProjectionPayload{}, false
	}
	if payload.SchemaVersion != 3 || payload.Artifact.ID == "" || payload.Artifact.OwnerSessionID != record.SessionID {
		return ArtifactV3ProjectionPayload{}, false
	}
	return payload, true
}

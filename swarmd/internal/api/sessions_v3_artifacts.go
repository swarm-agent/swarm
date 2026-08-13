package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionsV3ArtifactMaxBytes            int64 = 32 << 20
	sessionsV3ArtifactBundleMaxBytes      int64 = 128 << 20
	sessionsV3ArtifactBundleMaxFiles            = 2_000
	sessionsV3ArtifactPreviewTokenTTL           = 5 * time.Minute
	sessionsV3ArtifactPreviewAccessPath         = "access/"
	sessionsV3ArtifactPackageEntryPath          = "__swarm_artifact_entry__.html"
	sessionsV3ArtifactCatalogDefaultLimit       = 500
	sessionsV3ArtifactCatalogMaxLimit           = 2_000
	sessionsV3ArtifactCatalogSessionLimit       = 10_000
	// Package HTML is nested beneath a srcdoc iframe sandboxed without
	// allow-same-origin, so its immediate ancestor has an opaque origin. CSP
	// cannot express that as a frame-ancestors source. Keep framing controlled
	// by the scoped bearer capability and inherited sandbox instead; a
	// restrictive frame-ancestors policy requires a dedicated preview origin.
	sessionsV3ArtifactPackageHTMLCSP = "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' data: blob:; frame-src 'self'; connect-src 'none'; worker-src blob:; object-src 'none'; base-uri 'self'; form-action 'none'"
)

type sessionsV3ArtifactPreviewTokenClaims struct {
	UserID         string `json:"user_id"`
	AccountScopeID string `json:"account_scope_id"`
	SessionID      string `json:"session_id"`
	ArtifactID     string `json:"artifact_id"`
	ExpiresAt      int64  `json:"expires_at"`
}

var sessionsV3ArtifactPackageMediaTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".gif":   "image/gif",
	".htm":   "text/html; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".mjs":   "text/javascript; charset=utf-8",
	".otf":   "font/otf",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".ttf":   "font/ttf",
	".txt":   "text/plain; charset=utf-8",
	".wasm":  "application/wasm",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

type sessionsV3ResolvedArtifact struct {
	Reference  pebblestore.SessionPlanArtifactReference
	Descriptor pebblestore.PlanFinalHandoffArtifact
	Managed    *pebblestore.SessionArtifactVariant
}

type sessionsV3ArtifactCatalogItem struct {
	ArtifactID      string                                `json:"artifact_id"`
	CollectionID    string                                `json:"collection_id,omitempty"`
	SessionID       string                                `json:"session_id"`
	SessionTitle    string                                `json:"session_title"`
	WorkspacePath   string                                `json:"workspace_path,omitempty"`
	WorkspaceName   string                                `json:"workspace_name,omitempty"`
	PlanID          string                                `json:"plan_id,omitempty"`
	PlanTitle       string                                `json:"plan_title,omitempty"`
	CheckpointID    string                                `json:"checkpoint_id,omitempty"`
	CheckpointTitle string                                `json:"checkpoint_title,omitempty"`
	Label           string                                `json:"label"`
	Description     string                                `json:"description"`
	Filename        string                                `json:"filename"`
	MediaType       string                                `json:"media_type"`
	Kind            string                                `json:"kind"`
	Status          string                                `json:"status,omitempty"`
	FailureCode     string                                `json:"failure_code,omitempty"`
	Previewable     bool                                  `json:"previewable"`
	Selected        bool                                  `json:"selected,omitempty"`
	Category        string                                `json:"category"`
	UpdatedAt       int64                                 `json:"updated_at"`
	EventSeq        uint64                                `json:"event_seq,omitempty"`
	Progress        *sessionsV3ArtifactCollectionProgress `json:"progress,omitempty"`
	Lineage         *pebblestore.SessionArtifactLineage   `json:"lineage,omitempty"`
	Content         string                                `json:"content,omitempty"`
}

type sessionsV3ArtifactCollectionProgress struct {
	Total       int `json:"total"`
	Staging     int `json:"staging"`
	Ready       int `json:"ready"`
	Failed      int `json:"failed"`
	Unavailable int `json:"unavailable"`
}

func (s *Server) handleSessionsV3Artifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	limit, ok := parseRequestPositiveLimit(w, r, sessionsV3ArtifactCatalogDefaultLimit)
	if !ok {
		return
	}
	if limit > sessionsV3ArtifactCatalogMaxLimit {
		writeError(w, http.StatusBadRequest, errors.New("artifact limit cannot exceed 2000"))
		return
	}

	sessions, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, sessionsV3ArtifactCatalogSessionLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	artifacts := make([]sessionsV3ArtifactCatalogItem, 0, limit)
	seen := make(map[string]struct{})
	for _, session := range sessions {
		if sessionsV3SystemSidechat(session) {
			continue
		}
		workspacePath, workspaceName := sessionsV3ArtifactCatalogWorkspace(session)
		nativeHandoffs := make(map[string]struct{})
		if s.artifacts != nil {
			// Repair redundant progress indexes before projection so interrupted historical metadata cannot hide valid artifacts.
			if _, err := s.sessions.RepairSessionArtifactCollections(session.ID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			collections, err := s.sessions.ListSessionArtifactCollections(session.AccountScopeID, session.ID, "", pebblestore.SessionArtifactMaxCollections)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			for _, collection := range collections {
				variants, err := s.sessions.ListSessionArtifactVariants(session.AccountScopeID, session.ID, collection.ID, pebblestore.SessionArtifactMaxVariantsPerCollection)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				progress := sessionsV3ArtifactCollectionProgress{Total: collection.VariantCount, Staging: collection.StagingCount, Ready: collection.ReadyCount, Failed: collection.FailedCount, Unavailable: collection.UnavailableCount}
				if progress.Total != progress.Staging+progress.Ready+progress.Failed+progress.Unavailable || progress.Total != len(variants) {
					writeError(w, http.StatusInternalServerError, errors.New("artifact collection progress is inconsistent"))
					return
				}
				for _, variant := range variants {
					if variant.Status != pebblestore.SessionArtifactStatusStaging && variant.Status != pebblestore.SessionArtifactStatusReady && variant.Status != pebblestore.SessionArtifactStatusFailed && variant.Status != pebblestore.SessionArtifactStatusUnavailable {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant status is inconsistent"))
						return
					}
					if collection.Lineage.TaskCallID != "" && variant.Lineage.TaskCallID != "" && collection.Lineage.TaskCallID != variant.Lineage.TaskCallID {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant task lineage is inconsistent"))
						return
					}
					if collection.Lineage.ProgramID != "" && variant.Lineage.ProgramID != "" && collection.Lineage.ProgramID != variant.Lineage.ProgramID {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant program lineage is inconsistent"))
						return
					}
					lineage := variant.Lineage
					kind, previewable := sessionsV3ArtifactPresentation(variant)
					if variant.Status == pebblestore.SessionArtifactStatusReady {
						handoffKind, handoffMediaType := kind, variant.MediaType
						if handoffKind == "package" && handoffMediaType == "application/zip" {
							handoffKind, handoffMediaType = "html", "text/html"
						}
						nativeHandoffs[sessionsV3NativeHandoffKey(lineage.PlanID, lineage.CheckpointID, lineage.RunID, lineage.AttemptID, variant.Filename, handoffMediaType, handoffKind)] = struct{}{}
					}
					if kind == "package" && variant.Status == pebblestore.SessionArtifactStatusReady && variant.MediaType == "application/zip" {
						kind, previewable = "html", true
					}
					appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+variant.ID, sessionsV3ArtifactCatalogItem{
						ArtifactID: variant.ID, CollectionID: collection.ID, SessionID: session.ID, SessionTitle: session.Title,
						PlanID: lineage.PlanID, CheckpointID: lineage.CheckpointID,
						WorkspacePath: workspacePath, WorkspaceName: workspaceName,
						Label: firstNonEmpty(variant.Presentation.Label, collection.Name, variant.Filename), Description: firstNonEmpty(variant.Presentation.Description, collection.Description),
						Filename: variant.Filename, MediaType: variant.MediaType, Kind: kind, Status: variant.Status, FailureCode: variant.FailureCode,
						Previewable: previewable, Selected: collection.SelectedVariantID == variant.ID,
						Category: sessionsV3ManagedArtifactCategory(variant), UpdatedAt: variant.UpdatedAt, EventSeq: variant.EventSeq, Progress: &progress, Lineage: &lineage,
					})
				}
			}
		}
		plans, _, err := s.sessions.ListPlans(session.ID, sessionsV3PlansPageMaxLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, plan := range plans {
			if plan.AccountScopeID != "" && strings.TrimSpace(plan.AccountScopeID) != principal.AccountScopeID || plan.UserID != "" && strings.TrimSpace(plan.UserID) != principal.UserID {
				continue
			}
			planText := strings.TrimSpace(plan.Plan)
			if plan.Document != nil {
				if display := strings.TrimSpace(plan.Document.DisplayText); display != "" {
					planText = display
				} else if rendered := strings.TrimSpace(plan.Document.RenderedText); rendered != "" {
					planText = rendered
				}
			}
			planTitle := strings.TrimSpace(plan.Title)
			if planTitle == "" && plan.Document != nil {
				planTitle = strings.TrimSpace(plan.Document.Title)
			}
			if planTitle == "" {
				planTitle = "Plan"
			}
			planArtifactID := sessionsV3PlanArtifactID(session.ID, plan.ID)
			appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+planArtifactID, sessionsV3ArtifactCatalogItem{
				ArtifactID: planArtifactID, SessionID: session.ID, SessionTitle: session.Title,
				WorkspacePath: workspacePath, WorkspaceName: workspaceName,
				PlanID: plan.ID, PlanTitle: planTitle, Label: planTitle,
				Description: "Durable session plan", Filename: "plan.md", MediaType: "text/markdown",
				Kind: "markdown", Previewable: true, Category: "plan", UpdatedAt: plan.UpdatedAt, Content: planText,
			})

			if plan.Document == nil {
				continue
			}
			for _, checkpoint := range plan.Document.Checkpoints {
				if checkpoint.Handoff == nil || strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
					continue
				}
				references := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
				references = append(references, checkpoint.Artifacts...)
				for _, descriptor := range sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, references) {
					if _, ok := nativeHandoffs[sessionsV3NativeHandoffKey(plan.ID, checkpoint.ID, checkpoint.RunID, checkpoint.AttemptID, descriptor.Filename, descriptor.MediaType, descriptor.Kind)]; ok {
						// Native managed bytes are already cataloged; do not project a second unavailable workspace-backed compatibility item.
						continue
					}
					category := "document"
					if descriptor.Kind == "html" || descriptor.Kind == "image" || descriptor.Kind == "pdf" {
						category = "visual"
					}
					updatedAt := checkpoint.CompletedAt
					if updatedAt == 0 {
						updatedAt = plan.UpdatedAt
					}
					_, managedArtifactID := sessionsV3LegacyManagedArtifactIDs(session.ID, plan.ID, checkpoint.ID, descriptor.ID)
					appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+managedArtifactID, sessionsV3ArtifactCatalogItem{
						ArtifactID: managedArtifactID, SessionID: session.ID, SessionTitle: session.Title,
						WorkspacePath: workspacePath, WorkspaceName: workspaceName,
						PlanID: plan.ID, PlanTitle: planTitle, CheckpointID: checkpoint.ID, CheckpointTitle: checkpoint.Title,
						Label: descriptor.Label, Description: descriptor.Description, Filename: descriptor.Filename,
						MediaType: descriptor.MediaType, Kind: descriptor.Kind, Previewable: descriptor.Previewable,
						Category: category, UpdatedAt: updatedAt,
					})
				}
			}
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].UpdatedAt == artifacts[j].UpdatedAt {
			if artifacts[i].SessionID == artifacts[j].SessionID {
				return artifacts[i].ArtifactID < artifacts[j].ArtifactID
			}
			return artifacts[i].SessionID < artifacts[j].SessionID
		}
		return artifacts[i].UpdatedAt > artifacts[j].UpdatedAt
	})
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": artifacts})
}

func sessionsV3NativeHandoffKey(planID, checkpointID, runID, attemptID, filename, mediaType, kind string) string {
	return strings.Join([]string{strings.TrimSpace(planID), strings.TrimSpace(checkpointID), strings.TrimSpace(runID), strings.TrimSpace(attemptID), strings.TrimSpace(filename), strings.ToLower(strings.TrimSpace(mediaType)), strings.ToLower(strings.TrimSpace(kind))}, "\x00")
}

func sessionsV3ArtifactPresentation(variant pebblestore.SessionArtifactVariant) (string, bool) {
	kind, previewable := variant.Presentation.Kind, variant.Presentation.Previewable
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return kind, previewable
	}
	if strings.EqualFold(strings.TrimSpace(variant.MediaType), "image/svg+xml") {
		return "image", true
	}
	if kind == "package" && variant.MediaType == "application/zip" {
		return "html", true
	}
	return kind, previewable
}

func sessionsV3ManagedArtifactCategory(variant pebblestore.SessionArtifactVariant) string {
	kind, _ := sessionsV3ArtifactPresentation(variant)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "html", "package", "image", "pdf":
		return "visual"
	default:
		return "document"
	}
}

func appendCatalogArtifact(artifacts *[]sessionsV3ArtifactCatalogItem, seen map[string]struct{}, key string, artifact sessionsV3ArtifactCatalogItem) {
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*artifacts = append(*artifacts, artifact)
}

func sessionsV3ArtifactCatalogWorkspace(session pebblestore.SessionSnapshot) (string, string) {
	workspacePath := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(session.WorkspacePath)
	}
	workspaceName := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_name"))
	if workspaceName == "" {
		workspaceName = strings.TrimSpace(session.WorkspaceName)
	}
	if workspaceName == "" {
		workspaceName = filepath.Base(workspacePath)
	}
	return workspacePath, workspaceName
}

func sessionsV3PlanArtifactID(sessionID, planID string) string {
	canonical := strings.Join([]string{"swarm-plan-artifact-v1", strings.TrimSpace(sessionID), strings.TrimSpace(planID)}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return "plan_art_" + base64.RawURLEncoding.EncodeToString(digest[:18])
}

func (s *Server) handleSessionV3ArtifactPreviewAccess(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	artifactID := strings.TrimSpace(req.ArtifactID)
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" || artifact.Managed == nil || artifact.Managed.Status != pebblestore.SessionArtifactStatusReady {
		writeError(w, http.StatusNotFound, errors.New("artifact preview not found"))
		return
	}
	if artifact.Managed.MediaType != "application/zip" && artifact.Managed.MediaType != "text/html" {
		writeError(w, http.StatusNotFound, errors.New("artifact preview not found"))
		return
	}
	expiresAt := time.Now().Add(sessionsV3ArtifactPreviewTokenTTL)
	token, err := s.issueSessionV3ArtifactPreviewToken(principal, sessionID, artifactID, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"token":      token,
		"expires_at": expiresAt.Unix(),
	})
}

func (s *Server) issueSessionV3ArtifactPreviewToken(principal identity.Principal, sessionID, artifactID string, expiresAt time.Time) (string, error) {
	if s == nil || len(s.artifactPreviewKey) < 32 || !principal.Valid() {
		return "", errors.New("artifact preview access is unavailable")
	}
	claims := sessionsV3ArtifactPreviewTokenClaims{
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		SessionID:      strings.TrimSpace(sessionID),
		ArtifactID:     strings.TrimSpace(artifactID),
		ExpiresAt:      expiresAt.Unix(),
	}
	if claims.SessionID == "" || claims.ArtifactID == "" || claims.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("invalid artifact preview access scope")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.artifactPreviewKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, payload, []byte("swarm-v3-artifact-preview-v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Server) validateSessionV3ArtifactPreviewRequest(r *http.Request) (identity.Principal, bool) {
	if s == nil || r == nil || r.Method != http.MethodGet || len(s.artifactPreviewKey) < 32 {
		return identity.Principal{}, false
	}
	sessionID, subpath, ok := parseSessionsV3PrimaryPath(r.URL.Path)
	if !ok || !strings.HasPrefix(subpath, "artifacts/") || !strings.Contains(subpath, "/content/") {
		return identity.Principal{}, false
	}
	artifactPath := strings.TrimPrefix(subpath, "artifacts/")
	artifactID, contentPath, ok := strings.Cut(artifactPath, "/content/")
	artifactID = strings.TrimSpace(artifactID)
	if !ok || artifactID == "" || strings.Contains(artifactID, "/") {
		return identity.Principal{}, false
	}
	token, _, ok := strings.Cut(strings.TrimPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath), "/")
	if !ok || !strings.HasPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath) {
		return identity.Principal{}, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return identity.Principal{}, false
	}
	block, err := aes.NewCipher(s.artifactPreviewKey)
	if err != nil {
		return identity.Principal{}, false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < aead.NonceSize() {
		return identity.Principal{}, false
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	payload, err := aead.Open(nil, nonce, ciphertext, []byte("swarm-v3-artifact-preview-v1"))
	if err != nil {
		return identity.Principal{}, false
	}
	var claims sessionsV3ArtifactPreviewTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return identity.Principal{}, false
	}
	now := time.Now().Unix()
	if strings.TrimSpace(claims.SessionID) != sessionID || strings.TrimSpace(claims.ArtifactID) != artifactID || claims.ExpiresAt <= now || claims.ExpiresAt > now+int64(sessionsV3ArtifactPreviewTokenTTL/time.Second)+30 {
		return identity.Principal{}, false
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             strings.TrimSpace(claims.UserID),
		AccountScopeID:     strings.TrimSpace(claims.AccountScopeID),
		AccountScopeSource: identity.AccountScopeSourceSession,
		TokenExpires:       time.Unix(claims.ExpiresAt, 0),
	}
	return principal, principal.Valid()
}

func (s *Server) handleSessionV3ArtifactSelection(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		ClientRequestID string `json:"client_request_id"`
		EventSeq        uint64 `json:"event_seq"`
		Action          string `json:"action,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ClientRequestID == "" || len(req.ClientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required and must be 256 characters or fewer"))
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "select"
	}
	if action != "select" && action != "use" {
		writeError(w, http.StatusBadRequest, errors.New("artifact selection action must be select or use"))
		return
	}
	variant, found, err := s.sessions.GetSessionArtifactVariantByID(principal.AccountScopeID, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found || variant.Status != pebblestore.SessionArtifactStatusReady {
		writeError(w, http.StatusBadRequest, errors.New("only a ready artifact variant can be selected"))
		return
	}
	if req.EventSeq == 0 || req.EventSeq != variant.EventSeq {
		writeError(w, http.StatusBadRequest, errors.New("artifact selection event sequence is required and must match the ready variant"))
		return
	}
	collection, found, err := s.sessions.GetSessionArtifactCollection(principal.AccountScopeID, sessionID, variant.CollectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusBadRequest, errors.New("artifact collection was not found"))
		return
	}
	selection := &pebblestore.SessionArtifactSelectionReference{
		SessionID: sessionID, CollectionID: collection.ID, VariantID: variant.ID, EventSeq: req.EventSeq, Action: action,
		Label:       firstNonEmpty(variant.Presentation.Label, collection.Name, variant.Filename),
		Description: firstNonEmpty(variant.Presentation.Description, collection.Description),
	}
	payloadHash, err := sessionsV3ArtifactSelectionPayloadHash(sessionID, action, *selection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: req.ClientRequestID, IdempotencyKey: req.ClientRequestID,
		PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationSelectArtifact,
		Artifact: &pebblestore.V3ArtifactMutation{Collection: collection, Selection: selection}, NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "idempotency_conflict", "error": err.Error()})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if result.Artifact == nil || result.Artifact.Selection == nil || result.Artifact.Collection.SelectedVariantID != variant.ID {
		writeError(w, http.StatusInternalServerError, errors.New("artifact selection was not persisted"))
		return
	}
	response := map[string]any{"ok": true, "session_id": sessionID, "action": action, "selection": result.Artifact.Selection, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox}
	writeJSON(w, http.StatusOK, response)
}

func sessionsV3ArtifactSelectionPayloadHash(sessionID, action string, selection pebblestore.SessionArtifactSelectionReference) (string, error) {
	canonical := struct {
		Operation string                                        `json:"operation"`
		SessionID string                                        `json:"session_id"`
		Action    string                                        `json:"action"`
		Selection pebblestore.SessionArtifactSelectionReference `json:"selection"`
	}{sessionruntime.SessionMutationSelectArtifact, strings.TrimSpace(sessionID), strings.TrimSpace(action), selection}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}

func (s *Server) handleSessionV3Artifact(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	var file sessionsV3ReadSeekCloser
	var info os.FileInfo
	mediaType := artifact.Descriptor.MediaType
	if artifact.Descriptor.Kind == "html" && artifact.Managed != nil && artifact.Managed.MediaType == "application/zip" {
		file, info, mediaType, err = s.openManagedSessionV3ArtifactPackageFile(r.Context(), session, artifact, sessionsV3ArtifactPackageEntryPath)
	} else {
		file, info, err = s.openManagedSessionV3Artifact(r.Context(), session, artifact)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact file is unavailable"))
		return
	}
	defer file.Close()
	if info.Size() > sessionsV3ArtifactMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("artifact exceeds the preview size limit"))
		return
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": artifact.Descriptor.Filename})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, artifact.Descriptor.Filename, info.ModTime(), file)
}

func (s *Server) handleSessionV3ArtifactBundle(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	file, info, err := s.openManagedSessionV3Artifact(r.Context(), session, artifact)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact bundle is unavailable"))
		return
	}
	defer file.Close()
	bundleName := sessionV3ArtifactBundleFilename(artifact.Descriptor.Filename)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": bundleName})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodHead {
		return
	}
	if artifact.Managed != nil && artifact.Managed.MediaType == "application/zip" {
		_, _ = io.Copy(w, file)
		return
	}
	archive := zip.NewWriter(w)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		_ = archive.Close()
		return
	}
	header.Name = path.Join(sessionV3ArtifactBundleRootName(artifact.Descriptor.Filename), artifact.Descriptor.Filename)
	header.Method = zip.Deflate
	header.Modified = time.Time{}
	header.SetMode(0o600)
	entry, err := archive.CreateHeader(header)
	if err != nil {
		_ = archive.Close()
		return
	}
	if _, err := io.Copy(entry, file); err != nil {
		_ = archive.Close()
		return
	}
	_ = archive.Close()
}

type sessionsV3ArtifactBundleFile struct {
	WorkspaceRoot string
	WorkspacePath string
	RelativePath  string
	Info          os.FileInfo
}

func collectSessionV3ArtifactBundle(workspaceRoot, artifactPath string, includePackage bool) (string, []sessionsV3ArtifactBundleFile, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" || filepath.IsAbs(artifactPath) || strings.Contains(artifactPath, "\\") {
		return "", nil, errors.New("invalid artifact path")
	}
	cleanArtifact := filepath.Clean(filepath.FromSlash(artifactPath))
	if cleanArtifact == "." || cleanArtifact == ".." || strings.HasPrefix(cleanArtifact, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("artifact path escapes workspace")
	}
	packageRelativeRoot := filepath.Dir(cleanArtifact)
	if packageRelativeRoot == "." || !includePackage {
		file, info, err := openSessionV3ArtifactFile(workspaceRoot, filepath.ToSlash(cleanArtifact))
		if err != nil {
			return "", nil, err
		}
		file.Close()
		return sessionV3ArtifactBundleRootName(filepath.Base(cleanArtifact)), []sessionsV3ArtifactBundleFile{{
			WorkspaceRoot: workspaceRoot,
			WorkspacePath: filepath.ToSlash(cleanArtifact),
			RelativePath:  filepath.Base(cleanArtifact),
			Info:          info,
		}}, nil
	}

	resolvedWorkspace, err := filepath.EvalSymlinks(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return "", nil, err
	}
	resolvedWorkspace, err = filepath.Abs(resolvedWorkspace)
	if err != nil {
		return "", nil, err
	}
	packageRoot := filepath.Join(resolvedWorkspace, packageRelativeRoot)
	resolvedPackageRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil || filepath.Clean(packageRoot) != filepath.Clean(resolvedPackageRoot) {
		return "", nil, errors.New("artifact package path contains a symlink")
	}
	packageInfo, err := os.Stat(resolvedPackageRoot)
	if err != nil || !packageInfo.IsDir() {
		return "", nil, errors.New("artifact package directory is unavailable")
	}

	files := make([]sessionsV3ArtifactBundleFile, 0, 16)
	var totalBytes int64
	err = filepath.WalkDir(resolvedPackageRoot, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if candidate == resolvedPackageRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact package contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact package contains a non-regular file")
		}
		totalBytes += info.Size()
		if len(files) >= sessionsV3ArtifactBundleMaxFiles || totalBytes > sessionsV3ArtifactBundleMaxBytes {
			return errors.New("artifact package exceeds the bundle limit")
		}
		relative, err := filepath.Rel(resolvedPackageRoot, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("artifact package file escapes its directory")
		}
		workspacePath := filepath.Join(packageRelativeRoot, relative)
		files = append(files, sessionsV3ArtifactBundleFile{
			WorkspaceRoot: workspaceRoot,
			WorkspacePath: filepath.ToSlash(workspacePath),
			RelativePath:  filepath.ToSlash(relative),
			Info:          info,
		})
		return nil
	})
	if err != nil || len(files) == 0 {
		return "", nil, errors.New("artifact package is empty or unavailable")
	}
	return sessionV3ArtifactBundleRootName(filepath.Base(packageRelativeRoot)), files, nil
}

func buildSessionV3LegacyArtifactPackage(workspaceRoot, artifactPath string) ([]byte, error) {
	_, files, err := collectSessionV3ArtifactBundle(workspaceRoot, artifactPath, true)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, bundled := range files {
		header, err := zip.FileInfoHeader(bundled.Info)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		header.Name = bundled.RelativePath
		header.Method = zip.Deflate
		header.Modified = time.Time{}
		header.SetMode(0o600)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		file, _, err := openSessionV3ArtifactFile(bundled.WorkspaceRoot, bundled.WorkspacePath)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil {
			_ = archive.Close()
			return nil, copyErr
		}
		if closeErr != nil {
			_ = archive.Close()
			return nil, closeErr
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if int64(buffer.Len()) > sessionsV3ArtifactBundleMaxBytes {
		return nil, artifact.ErrQuotaExceeded
	}
	return buffer.Bytes(), nil
}

func sessionV3ArtifactBundleRootName(value string) string {
	name := strings.TrimSpace(value)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, ".-")
	if name == "" {
		return "artifact"
	}
	return name
}

func sessionV3ArtifactBundleFilename(artifactFilename string) string {
	return sessionV3ArtifactBundleRootName(artifactFilename) + ".zip"
}

func (s *Server) handleSessionV3ArtifactContent(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID, contentPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
	if strings.HasPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath) {
		_, scopedPath, ok := strings.Cut(strings.TrimPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath), "/")
		if !ok || strings.TrimSpace(scopedPath) == "" {
			writeError(w, http.StatusNotFound, errors.New("artifact package file is unavailable"))
			return
		}
		contentPath = scopedPath
	}
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" || artifact.Managed == nil || artifact.Managed.Status != pebblestore.SessionArtifactStatusReady || artifact.Managed.MediaType != "application/zip" {
		writeError(w, http.StatusNotFound, errors.New("artifact package not found"))
		return
	}

	file, info, mediaType, err := s.openManagedSessionV3ArtifactPackageFile(r.Context(), session, artifact, contentPath)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact package file is unavailable"))
		return
	}
	defer file.Close()
	if info.Size() > sessionsV3ArtifactMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("artifact package file exceeds the preview size limit"))
		return
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(contentPath)})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(mediaType, "text/html") {
		w.Header().Set("Content-Security-Policy", sessionsV3ArtifactPackageHTMLCSP)
	}
	http.ServeContent(w, r, filepath.Base(contentPath), info.ModTime(), file)
}

func (s *Server) resolveSessionV3Artifact(ctx context.Context, principal identity.Principal, sessionID, artifactID string) (sessionsV3ResolvedArtifact, bool, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" || strings.Contains(artifactID, "/") || !principal.Valid() {
		return sessionsV3ResolvedArtifact{}, false, nil
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	}
	if !found || session.UserID != principal.UserID || session.AccountScopeID != principal.AccountScopeID {
		return sessionsV3ResolvedArtifact{}, false, nil
	}
	plans := make([]pebblestore.SessionPlanSnapshot, 0, sessionsV3PlansPageMaxLimit+1)
	if active, ok, err := s.sessions.GetActivePlan(sessionID); err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	} else if ok {
		plans = append(plans, active)
	}
	listed, _, err := s.sessions.ListPlans(sessionID, sessionsV3PlansPageMaxLimit)
	if err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	}
	for _, plan := range listed {
		if len(plans) == 0 || plan.ID != plans[0].ID {
			plans = append(plans, plan)
		}
	}
	if s.artifacts != nil {
		if managed, ok, err := s.sessions.GetSessionArtifactVariantByID(principal.AccountScopeID, sessionID, artifactID); err != nil {
			return sessionsV3ResolvedArtifact{}, false, err
		} else if ok {
			if managed.SessionID != sessionID || managed.AccountScopeID != principal.AccountScopeID || (managed.Lineage.ParentSessionID != "" && managed.Lineage.ParentSessionID != sessionID) {
				return sessionsV3ResolvedArtifact{}, false, nil
			}
			if managed.Status != pebblestore.SessionArtifactStatusReady {
				return sessionsV3ResolvedArtifact{}, false, nil
			}
			resolved := sessionsV3ResolvedArtifact{Managed: &managed}
			// Path is a compatibility-only in-memory filename hint used by the legacy
			// package adapter. Native managed bytes remain addressed by opaque IDs.
			resolved.Reference = pebblestore.SessionPlanArtifactReference{Path: managed.Filename, Description: managed.Presentation.Description}
			kind, previewable := sessionsV3ArtifactPresentation(managed)
			resolved.Descriptor = pebblestore.PlanFinalHandoffArtifact{
				ID: managed.ID, Label: firstNonEmpty(managed.Presentation.Label, managed.Filename), Description: managed.Presentation.Description,
				Filename: managed.Filename, MediaType: managed.MediaType, Kind: kind, Previewable: previewable,
			}
			return resolved, true, nil
		}
	}
	for _, plan := range plans {
		if plan.Document == nil || (plan.SessionID != "" && plan.SessionID != sessionID) {
			continue
		}
		if plan.SessionID == "" {
			plan.SessionID = sessionID
		}
		for _, checkpoint := range plan.Document.Checkpoints {
			if checkpoint.Handoff == nil || strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
				continue
			}
			artifacts := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
			artifacts = append(artifacts, checkpoint.Artifacts...)
			for _, reference := range artifacts {
				descriptors := sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, []pebblestore.SessionPlanArtifactReference{reference})
				if len(descriptors) != 1 {
					continue
				}
				descriptor := descriptors[0]
				collectionID, variantID := sessionsV3LegacyManagedArtifactIDs(sessionID, plan.ID, checkpoint.ID, descriptor.ID)
				if artifactID != descriptor.ID && artifactID != variantID {
					continue
				}
				if (plan.UserID != "" && plan.UserID != principal.UserID) || (plan.AccountScopeID != "" && plan.AccountScopeID != principal.AccountScopeID) {
					continue
				}
				resolved := sessionsV3ResolvedArtifact{Reference: reference, Descriptor: descriptor}
				managed, err := s.importLegacySessionV3Artifact(ctx, principal, plan, checkpoint, reference, descriptor, collectionID, variantID)
				if err != nil {
					return sessionsV3ResolvedArtifact{}, false, err
				}
				if managed.ID == "" {
					return sessionsV3ResolvedArtifact{}, false, errors.New("managed artifact metadata is unavailable")
				}
				resolved.Managed = &managed
				resolved.Descriptor.ID = managed.ID
				if managed.Filename != "" {
					resolved.Descriptor.Filename = managed.Filename
				}
				if descriptor.Kind != "html" && managed.Status == pebblestore.SessionArtifactStatusReady {
					resolved.Descriptor.MediaType = managed.MediaType
					resolved.Descriptor.Kind = managed.Presentation.Kind
					resolved.Descriptor.Previewable = managed.Presentation.Previewable
				}
				return resolved, true, nil
			}
		}
	}
	return sessionsV3ResolvedArtifact{}, false, nil
}

func sessionsV3LegacyManagedArtifactIDs(sessionID, planID, checkpointID, legacyArtifactID string) (string, string) {
	canonical := strings.Join([]string{"swarm-legacy-artifact-import-v1", strings.TrimSpace(sessionID), strings.TrimSpace(planID), strings.TrimSpace(checkpointID), strings.TrimSpace(legacyArtifactID)}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	opaque := fmt.Sprintf("%x", digest[:18])
	return "legacy_col_" + opaque, "legacy_var_" + opaque
}

func (s *Server) importLegacySessionV3Artifact(ctx context.Context, principal identity.Principal, plan pebblestore.SessionPlanSnapshot, checkpoint pebblestore.SessionPlanCheckpoint, reference pebblestore.SessionPlanArtifactReference, descriptor pebblestore.PlanFinalHandoffArtifact, collectionID, variantID string) (pebblestore.SessionArtifactVariant, error) {
	if s == nil || s.sessions == nil || s.artifacts == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("managed artifact storage is unavailable")
	}
	s.artifactImportMu.Lock()
	defer s.artifactImportMu.Unlock()

	if existing, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	} else if ok && (existing.Status == pebblestore.SessionArtifactStatusReady || existing.Status == pebblestore.SessionArtifactStatusUnavailable || existing.Status == pebblestore.SessionArtifactStatusFailed) {
		return existing, nil
	}

	session, found, err := s.sessions.GetSession(plan.SessionID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("legacy artifact session was not found")
		}
		return pebblestore.SessionArtifactVariant{}, err
	}
	if session.UserID != principal.UserID || session.AccountScopeID != principal.AccountScopeID {
		return pebblestore.SessionArtifactVariant{}, errors.New("legacy artifact session ownership does not match")
	}

	presentation := pebblestore.SessionArtifactPresentation{Kind: descriptor.Kind, Label: descriptor.Label, Description: descriptor.Description, Previewable: descriptor.Previewable}
	if descriptor.Kind == "html" {
		presentation.Kind = "package"
		presentation.Previewable = false
	}
	variant := pebblestore.SessionArtifactVariant{ID: variantID, CollectionID: collectionID, AccountScopeID: principal.AccountScopeID, SessionID: plan.SessionID, Filename: descriptor.Filename, MediaType: descriptor.MediaType, Presentation: presentation, Lineage: pebblestore.SessionArtifactLineage{SourceSessionID: plan.SessionID, SourceVariantID: descriptor.ID, PlanID: plan.ID, CheckpointID: checkpoint.ID, RunID: checkpoint.RunID, AttemptID: checkpoint.AttemptID}}
	collection := pebblestore.SessionArtifactCollection{ID: collectionID, Name: descriptor.Label, Description: descriptor.Description, Presentation: presentation}
	if strings.TrimSpace(collection.Name) == "" {
		collection.Name = descriptor.Filename
	}
	if _, ok, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	} else if !ok {
		if _, err := s.applyLegacyArtifactMutation(principal, plan.SessionID, "create", sessionruntime.SessionMutationCreateArtifact, collection, variant); err != nil {
			if _, found, getErr := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID); getErr != nil || !found {
				return pebblestore.SessionArtifactVariant{}, err
			}
		}
	}

	workspaceRoot := sessionV3ArtifactWorkspaceRoot(session)
	service, err := s.artifacts.ServiceForSession(plan.SessionID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	var staged artifact.Staged
	sourceUnavailable := false
	if descriptor.Kind == "html" {
		packageBytes, sourceErr := buildSessionV3LegacyArtifactPackage(workspaceRoot, reference.Path)
		if sourceErr == nil {
			variant.MediaType = "application/zip"
			variant.Presentation.Kind = "package"
			variant.Presentation.Previewable = false
			staged, err = service.Stage(ctx, variant, bytes.NewReader(packageBytes))
		} else {
			err = sourceErr
			sourceUnavailable = !errors.Is(sourceErr, artifact.ErrQuotaExceeded)
		}
	} else {
		file, _, sourceErr := openSessionV3ArtifactFile(workspaceRoot, reference.Path)
		if sourceErr == nil {
			staged, err = service.Stage(ctx, variant, file)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		} else {
			err = sourceErr
			sourceUnavailable = true
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return pebblestore.SessionArtifactVariant{}, err
		}
		failureCode := "legacy_import_failed"
		kind := sessionruntime.SessionMutationFailArtifact
		if sourceUnavailable {
			failureCode = "legacy_source_unavailable"
			kind = sessionruntime.SessionMutationUnavailableArtifact
		} else if errors.Is(err, artifact.ErrQuotaExceeded) {
			failureCode = "legacy_import_quota_exceeded"
		}
		variant.FailureCode = failureCode
		if _, mutationErr := s.applyLegacyArtifactMutation(principal, plan.SessionID, "terminal-"+failureCode, kind, collection, variant); mutationErr != nil {
			return pebblestore.SessionArtifactVariant{}, mutationErr
		}
		stored, found, getErr := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID)
		if getErr != nil || !found {
			if getErr == nil {
				getErr = errors.New("managed artifact terminal metadata was not recorded")
			}
			return pebblestore.SessionArtifactVariant{}, getErr
		}
		return stored, nil
	}
	blob, err := service.Finalize(ctx, staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return pebblestore.SessionArtifactVariant{}, err
		}
		variant.FailureCode = "legacy_finalize_failed"
		if _, mutationErr := s.applyLegacyArtifactMutation(principal, plan.SessionID, "terminal-legacy_finalize_failed", sessionruntime.SessionMutationFailArtifact, collection, variant); mutationErr != nil {
			return pebblestore.SessionArtifactVariant{}, mutationErr
		}
		stored, found, getErr := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID)
		if getErr != nil || !found {
			if getErr == nil {
				getErr = errors.New("managed artifact finalize failure was not recorded")
			}
			return pebblestore.SessionArtifactVariant{}, getErr
		}
		return stored, nil
	}
	variant.Filename = blob.Filename
	variant.MediaType = blob.MediaType
	variant.DigestSHA256 = blob.DigestSHA256
	variant.Size = blob.Size
	variant.Presentation = blob.Presentation
	if _, err := s.applyLegacyArtifactMutation(principal, plan.SessionID, "finalize-"+blob.DigestSHA256, sessionruntime.SessionMutationFinalizeArtifact, collection, variant); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	stored, found, err := s.sessions.GetSessionArtifactVariant(principal.AccountScopeID, plan.SessionID, collectionID, variantID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("managed artifact metadata was not finalized")
		}
		return pebblestore.SessionArtifactVariant{}, err
	}
	return stored, nil
}

func (s *Server) applyLegacyArtifactMutation(principal identity.Principal, sessionID, phase, kind string, collection pebblestore.SessionArtifactCollection, variant pebblestore.SessionArtifactVariant) (sessionruntime.SessionMutationResult, error) {
	keySource := strings.Join([]string{"legacy-artifact-import", sessionID, collection.ID, variant.ID, phase}, "\x00")
	sum := sha256.Sum256([]byte(keySource))
	key := "legacy-artifact-" + base64.RawURLEncoding.EncodeToString(sum[:18])
	payload, err := json.Marshal(struct {
		Kind       string                                `json:"kind"`
		Collection pebblestore.SessionArtifactCollection `json:"collection"`
		Variant    pebblestore.SessionArtifactVariant    `json:"variant"`
	}{Kind: kind, Collection: collection, Variant: variant})
	if err != nil {
		return sessionruntime.SessionMutationResult{}, err
	}
	payloadSum := sha256.Sum256(payload)
	requestHash := fmt.Sprintf("%x", payloadSum[:])
	return s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: requestHash, RequestHash: requestHash, Kind: kind, Artifact: &pebblestore.V3ArtifactMutation{Collection: collection, Variant: &variant}, NowUnixMs: time.Now().UnixMilli()})
}

func (s *Server) openManagedSessionV3Artifact(ctx context.Context, session pebblestore.SessionSnapshot, resolved sessionsV3ResolvedArtifact) (*os.File, os.FileInfo, error) {
	if resolved.Managed == nil || resolved.Managed.Status != pebblestore.SessionArtifactStatusReady || s.artifacts == nil {
		return nil, nil, artifact.ErrNotReady
	}
	if resolved.Managed.SessionID != session.ID || resolved.Managed.AccountScopeID != session.AccountScopeID {
		return nil, nil, errors.New("managed artifact ownership does not match session")
	}
	service, err := s.artifacts.ServiceForSession(session.ID)
	if err != nil {
		return nil, nil, err
	}
	file, _, err := service.Open(ctx, *resolved.Managed)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func (s *Server) openManagedSessionV3ArtifactPackageFile(ctx context.Context, session pebblestore.SessionSnapshot, resolved sessionsV3ResolvedArtifact, contentPath string) (sessionsV3ReadSeekCloser, os.FileInfo, string, error) {
	file, _, err := s.openManagedSessionV3Artifact(ctx, session, resolved)
	if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()
	archive, err := zip.NewReader(file, resolved.Managed.Size)
	if err != nil {
		return nil, nil, "", err
	}
	contentPath = strings.TrimSpace(contentPath)
	entryName := filepath.ToSlash(filepath.Clean(filepath.FromSlash(contentPath)))
	if filepath.IsAbs(filepath.FromSlash(contentPath)) || strings.HasPrefix(entryName, "/") {
		return nil, nil, "", errors.New("artifact package path must be relative")
	}
	if contentPath == sessionsV3ArtifactPackageEntryPath {
		if resolved.Managed != nil && resolved.Managed.Presentation.Kind == "package" {
			entryName = managedSessionV3ArtifactPackageEntry(archive)
		} else {
			entryName = filepath.ToSlash(filepath.Base(filepath.FromSlash(resolved.Reference.Path)))
		}
	} else if resolved.Managed == nil || resolved.Managed.Presentation.Kind != "package" {
		legacyRoot := filepath.ToSlash(filepath.Clean(filepath.Dir(filepath.FromSlash(resolved.Reference.Path))))
		if legacyRoot != "." && legacyRoot != "" && strings.HasPrefix(entryName, legacyRoot+"/") {
			entryName = strings.TrimPrefix(entryName, legacyRoot+"/")
		}
	}
	if entryName == "." || entryName == ".." || strings.HasPrefix(entryName, "../") || strings.Contains(contentPath, "\\") {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	mediaType, ok := sessionsV3ArtifactPackageMediaTypes[strings.ToLower(filepath.Ext(entryName))]
	if !ok {
		return nil, nil, "", errors.New("artifact package file type is not previewable")
	}
	for _, entry := range archive.File {
		cleanEntry := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name)))
		if cleanEntry != entryName || cleanEntry == ".." || strings.HasPrefix(cleanEntry, "../") || strings.Contains(entry.Name, "\\") || !entry.Mode().IsRegular() {
			continue
		}
		if entry.UncompressedSize64 > uint64(sessionsV3ArtifactMaxBytes) {
			return nil, nil, "", errors.New("artifact package file exceeds the preview size limit")
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, nil, "", err
		}
		data, err := io.ReadAll(io.LimitReader(reader, sessionsV3ArtifactMaxBytes+1))
		_ = reader.Close()
		if err != nil || int64(len(data)) > sessionsV3ArtifactMaxBytes {
			return nil, nil, "", errors.New("artifact package file is unavailable")
		}
		info := sessionsV3MemoryFileInfo{name: filepath.Base(entryName), size: int64(len(data)), modTime: entry.ModTime()}
		return sessionsV3NewMemoryFile(data), info, mediaType, nil
	}
	return nil, nil, "", errors.New("artifact package file is unavailable")
}

func managedSessionV3ArtifactPackageEntry(archive *zip.Reader) string {
	if archive == nil {
		return "index.html"
	}
	for _, candidate := range []string{"index.html", "index.htm"} {
		for _, entry := range archive.File {
			if filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name))) == candidate && entry.Mode().IsRegular() {
				return candidate
			}
		}
	}
	for _, entry := range archive.File {
		name := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name)))
		if entry.Mode().IsRegular() && name != "." && name != ".." && !strings.HasPrefix(name, "/") && !strings.Contains(entry.Name, "\\") && !strings.HasPrefix(name, "../") && (strings.HasSuffix(strings.ToLower(name), ".html") || strings.HasSuffix(strings.ToLower(name), ".htm")) {
			return name
		}
	}
	return "index.html"
}

type sessionsV3MemoryFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (i sessionsV3MemoryFileInfo) Name() string       { return i.name }
func (i sessionsV3MemoryFileInfo) Size() int64        { return i.size }
func (i sessionsV3MemoryFileInfo) Mode() os.FileMode  { return 0o600 }
func (i sessionsV3MemoryFileInfo) ModTime() time.Time { return i.modTime }
func (i sessionsV3MemoryFileInfo) IsDir() bool        { return false }
func (i sessionsV3MemoryFileInfo) Sys() any           { return nil }

type sessionsV3ReadSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

type sessionsV3MemoryFile struct{ *bytes.Reader }

func (sessionsV3MemoryFile) Close() error { return nil }

func sessionsV3NewMemoryFile(data []byte) sessionsV3ReadSeekCloser {
	return sessionsV3MemoryFile{Reader: bytes.NewReader(data)}
}

func sessionV3ArtifactWorkspaceRoot(session pebblestore.SessionSnapshot) string {
	for _, candidate := range []string{
		sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_workspace_path"),
		session.WorktreeRootPath,
		session.WorkspacePath,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func openSessionV3ArtifactPackageFile(workspaceRoot, artifactPath, contentPath string) (*os.File, os.FileInfo, string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	contentPath = strings.TrimSpace(contentPath)
	if contentPath == sessionsV3ArtifactPackageEntryPath {
		contentPath = filepath.Base(filepath.FromSlash(artifactPath))
	}
	if artifactPath == "" || contentPath == "" || filepath.IsAbs(contentPath) || strings.Contains(contentPath, "\\") {
		return nil, nil, "", errors.New("invalid artifact package path")
	}
	packageRoot := filepath.Clean(filepath.Dir(filepath.FromSlash(artifactPath)))
	if packageRoot == ".." || strings.HasPrefix(packageRoot, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("html artifact package escapes workspace")
	}
	cleanContent := filepath.Clean(filepath.FromSlash(contentPath))
	if cleanContent == "." || cleanContent == ".." || strings.HasPrefix(cleanContent, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	mediaType, ok := sessionsV3ArtifactPackageMediaTypes[strings.ToLower(filepath.Ext(cleanContent))]
	if !ok {
		return nil, nil, "", errors.New("artifact package file type is not previewable")
	}
	candidate := filepath.Join(packageRoot, cleanContent)
	relative, err := filepath.Rel(packageRoot, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	file, info, err := openSessionV3ArtifactFile(workspaceRoot, filepath.ToSlash(candidate))
	if err != nil {
		return nil, nil, "", err
	}
	return file, info, mediaType, nil
}

func openSessionV3ArtifactFile(workspaceRoot, artifactPath string) (*os.File, os.FileInfo, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	artifactPath = strings.TrimSpace(artifactPath)
	if workspaceRoot == "" || artifactPath == "" || filepath.IsAbs(artifactPath) || strings.Contains(artifactPath, "\\") {
		return nil, nil, errors.New("invalid artifact path")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(artifactPath))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("artifact path escapes workspace")
	}
	resolvedRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, nil, err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	candidate := filepath.Join(resolvedRoot, cleanRelative)
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, nil, err
	}
	resolvedCandidate, err = filepath.Abs(resolvedCandidate)
	if err != nil {
		return nil, nil, err
	}
	if filepath.Clean(candidate) != filepath.Clean(resolvedCandidate) {
		return nil, nil, errors.New("artifact path contains a symlink")
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("artifact path escapes workspace")
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("artifact is not a regular file")
	}
	return file, info, nil
}

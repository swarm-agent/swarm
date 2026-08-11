package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionsV3ArtifactMaxBytes          int64 = 32 << 20
	sessionsV3ArtifactPreviewTokenTTL         = 5 * time.Minute
	sessionsV3ArtifactPreviewAccessPath       = "access/"
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
	artifact, found, err := s.resolveSessionV3Artifact(sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" {
		writeError(w, http.StatusNotFound, errors.New("artifact package not found"))
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
	artifact, found, err := s.resolveSessionV3Artifact(sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	workspaceRoot := sessionV3ArtifactWorkspaceRoot(session)
	file, info, err := openSessionV3ArtifactFile(workspaceRoot, artifact.Reference.Path)
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
	w.Header().Set("Content-Type", artifact.Descriptor.MediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, artifact.Descriptor.Filename, info.ModTime(), file)
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
	artifact, found, err := s.resolveSessionV3Artifact(sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" {
		writeError(w, http.StatusNotFound, errors.New("artifact package not found"))
		return
	}

	file, info, mediaType, err := openSessionV3ArtifactPackageFile(sessionV3ArtifactWorkspaceRoot(session), artifact.Reference.Path, contentPath)
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

func (s *Server) resolveSessionV3Artifact(sessionID, artifactID string) (sessionsV3ResolvedArtifact, bool, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" || strings.Contains(artifactID, "/") {
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
	for _, plan := range plans {
		if plan.Document == nil {
			continue
		}
		for _, checkpoint := range plan.Document.Checkpoints {
			if checkpoint.Handoff == nil || strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted {
				continue
			}
			artifacts := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
			artifacts = append(artifacts, checkpoint.Artifacts...)
			for _, reference := range artifacts {
				descriptors := sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, []pebblestore.SessionPlanArtifactReference{reference})
				if len(descriptors) == 1 && descriptors[0].ID == artifactID {
					return sessionsV3ResolvedArtifact{Reference: reference, Descriptor: descriptors[0]}, true, nil
				}
			}
		}
	}
	return sessionsV3ResolvedArtifact{}, false, nil
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
	if artifactPath == "" || contentPath == "" || filepath.IsAbs(contentPath) || strings.Contains(contentPath, "\\") {
		return nil, nil, "", errors.New("invalid artifact package path")
	}
	packageRoot := filepath.Clean(filepath.Dir(filepath.FromSlash(artifactPath)))
	if packageRoot == "." || packageRoot == ".." || strings.HasPrefix(packageRoot, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("html artifact is not in a dedicated package directory")
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

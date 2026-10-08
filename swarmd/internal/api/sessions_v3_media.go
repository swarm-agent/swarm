package api

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3MediaCapabilityEntry struct {
	Modality   string   `json:"modality"`
	MIMETypes  []string `json:"mime_types,omitempty"`
	FileTypes  []string `json:"file_types,omitempty"`
	MaxBytes   int64    `json:"max_bytes"`
	MaxCount   int      `json:"max_count"`
	Provenance []string `json:"provenance,omitempty"`
}

type sessionsV3MediaCapability struct {
	Status            string                           `json:"status"`
	ContractVersion   int                              `json:"contract_version"`
	ContractToken     string                           `json:"contract_token,omitempty"`
	Provider          string                           `json:"provider,omitempty"`
	Model             string                           `json:"model,omitempty"`
	ProviderSurface   string                           `json:"provider_surface,omitempty"`
	CredentialSurface string                           `json:"credential_surface,omitempty"`
	AdapterID         string                           `json:"adapter_id,omitempty"`
	SnapshotID        string                           `json:"snapshot_id,omitempty"`
	SnapshotVersion   string                           `json:"snapshot_version,omitempty"`
	SnapshotSource    string                           `json:"snapshot_source,omitempty"`
	DenialReasons     []string                         `json:"denial_reasons,omitempty"`
	ResolutionError   string                           `json:"resolution_error,omitempty"`
	Capabilities      []sessionsV3MediaCapabilityEntry `json:"capabilities"`
}

func projectSessionsV3MediaCapability(contract provideriface.SessionMediaContract) sessionsV3MediaCapability {
	projection := sessionsV3MediaCapability{
		Status: "unavailable", ContractVersion: contract.Version, Provider: contract.ProviderID, Model: contract.Model,
		ProviderSurface: contract.ProviderSurface, CredentialSurface: contract.CredentialSurface, AdapterID: contract.AdapterID,
		SnapshotID: contract.SnapshotID, SnapshotVersion: contract.SnapshotVersion, SnapshotSource: contract.SnapshotSource,
		DenialReasons: append([]string(nil), contract.DenialReasons...), Capabilities: []sessionsV3MediaCapabilityEntry{},
	}
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || strings.TrimSpace(capability.Modality) == "" {
			continue
		}
		projection.Capabilities = append(projection.Capabilities, sessionsV3MediaCapabilityEntry{
			Modality: capability.Modality, MIMETypes: append([]string(nil), capability.MIMETypes...), FileTypes: append([]string(nil), capability.FileTypes...),
			MaxBytes: capability.MaxBytes, MaxCount: capability.MaxCount, Provenance: append([]string(nil), capability.Provenance...),
		})
	}
	sort.Slice(projection.Capabilities, func(i, j int) bool { return projection.Capabilities[i].Modality < projection.Capabilities[j].Modality })
	if len(projection.Capabilities) > 0 && strings.TrimSpace(contract.Hash) != "" {
		projection.Status = "available"
		projection.ContractToken = contract.Hash
	}
	return projection
}

func (s *Server) handleSessionV3MediaCapability(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
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
	contract, err := s.sessionsV3MediaContract(principal, session)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("resolve session media capability: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "media_capability": projectSessionsV3MediaCapability(contract)})
}

func (s *Server) handleSessionV3MediaUpload(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
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
	modality := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Swarm-Media-Modality")))
	fileType := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-Swarm-Media-File-Type")), "."))
	filename := strings.TrimSpace(r.Header.Get("X-Swarm-Media-Filename"))
	if filename == "" {
		if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
			filename = params["filename"]
		}
	}
	declaredMIME := strings.TrimSpace(r.Header.Get("Content-Type"))
	requestedContract := strings.TrimSpace(r.Header.Get("X-Swarm-Media-Contract"))

	contractHash := ""
	providerID := ""
	model := ""
	maxBytes := pebblestore.SessionMediaDefaultMaxBytes
	maxCount := pebblestore.SessionMediaDefaultMaxCount

	contract, err := s.sessionsV3MediaContract(principal, session)
	if err == nil && strings.TrimSpace(contract.Hash) != "" {
		if requestedContract != "" && requestedContract != contract.Hash {
			writeError(w, http.StatusConflict, errors.New("media capability changed; refresh attachment support before uploading"))
			return
		}
		if capability, ok := sessionMediaAllowedCapability(contract, modality, declaredMIME, fileType); ok {
			contractHash = contract.Hash
			providerID = contract.ProviderID
			model = contract.Model
			if capability.MaxBytes > 0 && capability.MaxBytes <= pebblestore.SessionMediaDefaultMaxBytes {
				maxBytes = capability.MaxBytes
			}
			if capability.MaxCount > 0 {
				maxCount = capability.MaxCount
			}
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1)
	asset, replayed, err := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID:   principal.AccountScopeID,
		SessionID:        sessionID,
		Modality:         modality,
		DeclaredMIMEType: declaredMIME,
		FileType:         fileType,
		FileName:         filename,
		ContractHash:     contractHash,
		ProviderID:       providerID,
		Model:            model,
		MaxBytes:         maxBytes,
		MaxCount:         maxCount,
		Reader:           r.Body,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, map[string]any{"ok": true, "asset": asset, "replayed": replayed})
}

func (s *Server) validateSessionsV3MessageMedia(principal identity.Principal, session pebblestore.SessionSnapshot, references []pebblestore.SessionMediaReference) error {
	if len(references) == 0 {
		return nil
	}
	if len(references) > pebblestore.SessionMediaDefaultMaxCount {
		return errors.New("message media reference count limit exceeded")
	}
	for index, reference := range references {
		assetID := strings.TrimSpace(reference.AssetID)
		if assetID == "" {
			return fmt.Errorf("media reference %d is missing asset_id", index)
		}
		asset, ok, err := s.sessions.GetSessionMediaAsset(principal.AccountScopeID, session.ID, assetID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("media reference %d is outside the authenticated session scope", index)
		}
		if asset.DigestSHA256 != reference.DigestSHA256 || asset.Size != reference.Size {
			return fmt.Errorf("media reference %d does not match stored asset", index)
		}
	}
	return nil
}

func (s *Server) handleSessionV3MediaAsset(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, assetID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete {
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
	_ = session
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		writeError(w, http.StatusBadRequest, errors.New("asset id is required"))
		return
	}

	if r.Method == http.MethodDelete {
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		deleted, err := s.sessions.Store().DeleteUnreferencedSessionMediaAsset(principal.AccountScopeID, sessionID, assetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, errors.New("media asset not found or cannot be deleted"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true, "asset_id": assetID})
		return
	}

	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	asset, payload, err := s.sessions.ReadSessionMediaAsset(principal.AccountScopeID, sessionID, assetID)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("media asset not found"))
		return
	}
	if len(payload) == 0 {
		writeError(w, http.StatusNotFound, errors.New("media asset is empty"))
		return
	}

	mediaType := asset.DetectedMIMEType
	if mediaType == "" {
		mediaType = asset.DeclaredMIMEType
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	filename := asset.FileName
	if filename == "" {
		if asset.FileType != "" {
			filename = fmt.Sprintf("%s.%s", asset.ID, asset.FileType)
		} else {
			filename = asset.ID
		}
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": filename})
	if disposition == "" {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; media-src 'self' data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("ETag", fmt.Sprintf("%q", asset.DigestSHA256))
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")

	modTime := time.UnixMilli(asset.CreatedAt)
	if asset.CreatedAt == 0 {
		modTime = time.Now()
	}
	http.ServeContent(w, r, filename, modTime, bytes.NewReader(payload))
}

func (s *Server) sessionsV3MediaContract(principal identity.Principal, session pebblestore.SessionSnapshot) (provideriface.SessionMediaContract, error) {
	if s == nil {
		return provideriface.SessionMediaContract{}, errors.New("v3 session service is not configured")
	}
	// A resolver value has no worker lifecycle and requires no execution setup.
	resolver := &sessionV3Executor{server: s}
	resolved, err := resolver.resolveSessionV3Capabilities(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID})
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	// Unsupported surfaces are valid denied contracts with useful provenance,
	// unlike resolution failures, which the read handler exposes as errors.
	return resolved.MediaContract, nil
}

func sessionMediaAllowedCapability(contract provideriface.SessionMediaContract, modality, mimeType, fileType string) (provideriface.MediaContractCapability, bool) {
	if !runruntime.SessionMediaContractAllows(contract, modality, mimeType, fileType) {
		return provideriface.MediaContractCapability{}, false
	}
	modality = strings.ToLower(strings.TrimSpace(modality))
	for _, capability := range contract.Capabilities {
		if capability.State == provideriface.MediaCapabilityStateAllowed && capability.Modality == modality {
			return capability, true
		}
	}
	return provideriface.MediaContractCapability{}, false
}

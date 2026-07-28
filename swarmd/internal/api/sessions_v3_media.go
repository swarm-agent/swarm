package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

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
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "media_capability": sessionsV3MediaCapability{Status: "unavailable", Capabilities: []sessionsV3MediaCapabilityEntry{}}})
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
	declaredMIME := strings.TrimSpace(r.Header.Get("Content-Type"))
	requestedContract := strings.TrimSpace(r.Header.Get("X-Swarm-Media-Contract"))
	contract, err := s.sessionsV3MediaContract(principal, session)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if requestedContract == "" || requestedContract != contract.Hash {
		writeError(w, http.StatusConflict, errors.New("media capability changed; refresh attachment support before uploading"))
		return
	}
	capability, ok := sessionMediaAllowedCapability(contract, modality, declaredMIME, fileType)
	if !ok || strings.TrimSpace(contract.Hash) == "" {
		writeError(w, http.StatusBadRequest, errors.New("current session media contract does not admit this upload"))
		return
	}
	maxBytes := capability.MaxBytes
	if maxBytes <= 0 || maxBytes > pebblestore.SessionMediaDefaultMaxBytes {
		maxBytes = pebblestore.SessionMediaDefaultMaxBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1)
	asset, replayed, err := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{
		AccountScopeID: principal.AccountScopeID, SessionID: sessionID, Modality: modality,
		DeclaredMIMEType: declaredMIME, FileType: fileType, ContractHash: contract.Hash,
		ProviderID: contract.ProviderID, Model: contract.Model, MaxBytes: maxBytes, MaxCount: capability.MaxCount,
		Reader: r.Body,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"asset": asset, "replayed": replayed})
}

func (s *Server) validateSessionsV3MessageMedia(principal identity.Principal, session pebblestore.SessionSnapshot, references []pebblestore.SessionMediaReference) error {
	if len(references) == 0 {
		return nil
	}
	if len(references) > pebblestore.SessionMediaDefaultMaxCount {
		return errors.New("message media reference count limit exceeded")
	}
	contract, err := s.sessionsV3MediaContract(principal, session)
	if err != nil {
		return err
	}
	if strings.TrimSpace(contract.Hash) == "" {
		return errors.New("current session media contract is empty")
	}
	for index, reference := range references {
		asset, ok, err := s.sessions.GetSessionMediaAsset(principal.AccountScopeID, session.ID, strings.TrimSpace(reference.AssetID))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("media reference %d is outside the authenticated session scope", index)
		}
		if asset.ContractHash != contract.Hash || reference.ContractHash != contract.Hash {
			return fmt.Errorf("media reference %d was admitted under a stale or mismatched contract", index)
		}
		if !runruntime.SessionMediaContractAllows(contract, asset.Modality, asset.DetectedMIMEType, asset.FileType) {
			return fmt.Errorf("media reference %d is not admitted by the current contract", index)
		}
	}
	return nil
}

func (s *Server) sessionsV3MediaContract(principal identity.Principal, session pebblestore.SessionSnapshot) (provideriface.SessionMediaContract, error) {
	if s == nil || s.v3SessionExecutor == nil {
		return provideriface.SessionMediaContract{}, errors.New("v3 session executor is not configured")
	}
	resolved, err := s.v3SessionExecutor.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: "media-admission"})
	if err != nil {
		return provideriface.SessionMediaContract{}, err
	}
	contract := resolved.MediaContract
	if !runruntime.SessionMediaProviderEnabled(contract.ProviderID) {
		return provideriface.SessionMediaContract{}, errors.New("media admission is restricted to reviewed conversational provider surfaces")
	}
	return contract, nil
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

package pebblestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	SessionMediaAssetVersion       = 1
	SessionMediaDefaultMaxBytes    = int64(20 << 20)
	SessionMediaDefaultMaxCount    = 8
	SessionMediaDefaultQuotaBytes  = int64(64 << 20)
	SessionMediaDefaultQuotaAssets = 32
)

// SessionMediaAsset is immutable metadata for one content-addressed session
// asset. Blob bytes are stored separately and are never included in events,
// projections, diagnostics, or realtime payloads.
type SessionMediaAsset struct {
	Version          int    `json:"version"`
	ID               string `json:"id"`
	AccountScopeID   string `json:"account_scope_id"`
	SessionID        string `json:"session_id"`
	DigestSHA256     string `json:"digest_sha256"`
	Modality         string `json:"modality"`
	DeclaredMIMEType string `json:"declared_mime_type"`
	DetectedMIMEType string `json:"detected_mime_type"`
	FileType         string `json:"file_type,omitempty"`
	Size             int64  `json:"size"`
	CreatedAt        int64  `json:"created_at"`
	ContractHash     string `json:"contract_hash"`
	ProviderID       string `json:"provider_id"`
	Model            string `json:"model"`
	ReferenceCount   int64  `json:"reference_count,omitempty"`
}

// SessionMediaReference is an ordered, typed durable message reference. Order
// is represented by its position in MessageSnapshot.Media.
type SessionMediaReference struct {
	AssetID      string `json:"asset_id"`
	Modality     string `json:"modality"`
	MIMEType     string `json:"mime_type"`
	FileType     string `json:"file_type,omitempty"`
	Size         int64  `json:"size"`
	DigestSHA256 string `json:"digest_sha256"`
	ContractHash string `json:"contract_hash"`
}

type PutSessionMediaAssetInput struct {
	AccountScopeID   string
	SessionID        string
	Modality         string
	DeclaredMIMEType string
	FileType         string
	ContractHash     string
	ProviderID       string
	Model            string
	MaxBytes         int64
	MaxCount         int
	QuotaBytes       int64
	QuotaAssets      int
	NowUnixMs        int64
	Reader           io.Reader
}

func KeySessionMediaAsset(accountScopeID, sessionID, assetID string) string {
	return fmt.Sprintf("v3/session_media/assets/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(assetID))
}

func SessionMediaAssetPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_media/assets/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionMediaBlob(accountScopeID, sessionID, assetID string) string {
	return fmt.Sprintf("v3/session_media/blobs/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(assetID))
}

func SessionMediaBlobPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_media/blobs/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

// Keep this storage-layer guard explicit: the store cannot import orchestration
// policy without creating a package cycle, and unknown providers must fail closed.
func sessionMediaAssetProviderEnabled(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "openai", "codex", "google", "anthropic", "fireworks", "openrouter":
		return true
	default:
		return false
	}
}

func (s *SessionStore) PutSessionMediaAsset(input PutSessionMediaAssetInput) (SessionMediaAsset, bool, error) {
	if s == nil || s.store == nil {
		return SessionMediaAsset{}, false, errors.New("session store is not configured")
	}
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Modality = strings.ToLower(strings.TrimSpace(input.Modality))
	input.DeclaredMIMEType = normalizeSessionMediaMIME(input.DeclaredMIMEType)
	input.FileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(input.FileType), "."))
	input.ContractHash = strings.TrimSpace(input.ContractHash)
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	input.Model = strings.TrimSpace(input.Model)
	if input.AccountScopeID == "" || input.SessionID == "" {
		return SessionMediaAsset{}, false, errors.New("media asset account and session scope are required")
	}
	if input.Modality == "" || input.DeclaredMIMEType == "" || input.ContractHash == "" {
		return SessionMediaAsset{}, false, errors.New("media asset modality, declared MIME type, and contract hash are required")
	}
	if !sessionMediaAssetProviderEnabled(input.ProviderID) {
		return SessionMediaAsset{}, false, errors.New("media assets are restricted to reviewed conversational provider surfaces")
	}
	if input.Reader == nil {
		return SessionMediaAsset{}, false, errors.New("media asset body is required")
	}
	if input.MaxBytes <= 0 || input.MaxBytes > SessionMediaDefaultMaxBytes {
		input.MaxBytes = SessionMediaDefaultMaxBytes
	}
	if input.MaxCount <= 0 || input.MaxCount > SessionMediaDefaultMaxCount {
		input.MaxCount = SessionMediaDefaultMaxCount
	}
	if input.QuotaBytes <= 0 {
		input.QuotaBytes = SessionMediaDefaultQuotaBytes
	}
	if input.QuotaAssets <= 0 {
		input.QuotaAssets = SessionMediaDefaultQuotaAssets
	}

	payload, err := io.ReadAll(io.LimitReader(input.Reader, input.MaxBytes+1))
	if err != nil {
		return SessionMediaAsset{}, false, fmt.Errorf("read media asset: %w", err)
	}
	if len(payload) == 0 {
		return SessionMediaAsset{}, false, errors.New("media asset body is empty")
	}
	if int64(len(payload)) > input.MaxBytes {
		return SessionMediaAsset{}, false, fmt.Errorf("media asset exceeds %d byte limit", input.MaxBytes)
	}
	detected := detectSessionMediaMIME(payload)
	if detected != input.DeclaredMIMEType {
		return SessionMediaAsset{}, false, fmt.Errorf("declared MIME type %q does not match detected MIME type %q", input.DeclaredMIMEType, detected)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	// A refreshed contract is a new immutable admission domain. Include it in
	// identity so identical bytes can be re-admitted without mutating an older,
	// now-stale asset while the full content digest remains explicit metadata.
	identitySum := sha256.Sum256([]byte(digest + "\x00" + input.ContractHash))
	assetID := "media_" + hex.EncodeToString(identitySum[:])
	asset := SessionMediaAsset{
		Version: SessionMediaAssetVersion, ID: assetID, AccountScopeID: input.AccountScopeID, SessionID: input.SessionID,
		DigestSHA256: digest, Modality: input.Modality, DeclaredMIMEType: input.DeclaredMIMEType, DetectedMIMEType: detected,
		FileType: input.FileType, Size: int64(len(payload)), ContractHash: input.ContractHash,
		ProviderID: input.ProviderID, Model: input.Model, CreatedAt: input.NowUnixMs,
	}
	if asset.CreatedAt == 0 {
		asset.CreatedAt = time.Now().UnixMilli()
	}

	unlock := s.store.sessionMutations.lockSessions(input.SessionID)
	defer unlock()
	if existing, ok, err := s.GetSessionMediaAsset(input.AccountScopeID, input.SessionID, assetID); err != nil {
		return SessionMediaAsset{}, false, err
	} else if ok {
		if existing.DigestSHA256 != digest || existing.Size != asset.Size || existing.DetectedMIMEType != detected {
			return SessionMediaAsset{}, false, errors.New("immutable media asset identity collision")
		}
		return existing, true, nil
	}
	count, totalBytes, err := s.sessionMediaUsage(input.AccountScopeID, input.SessionID)
	if err != nil {
		return SessionMediaAsset{}, false, err
	}
	if count >= input.MaxCount || count >= input.QuotaAssets {
		return SessionMediaAsset{}, false, errors.New("session media asset count quota exceeded")
	}
	if totalBytes+asset.Size > input.QuotaBytes {
		return SessionMediaAsset{}, false, errors.New("session media byte quota exceeded")
	}
	metadata, err := json.Marshal(asset)
	if err != nil {
		return SessionMediaAsset{}, false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySessionMediaAsset(input.AccountScopeID, input.SessionID, assetID)), metadata, nil); err != nil {
		return SessionMediaAsset{}, false, err
	}
	if err := batch.Set([]byte(KeySessionMediaBlob(input.AccountScopeID, input.SessionID, assetID)), payload, nil); err != nil {
		return SessionMediaAsset{}, false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return SessionMediaAsset{}, false, err
	}
	return asset, false, nil
}

func detectSessionMediaMIME(payload []byte) string {
	detected := normalizeSessionMediaMIME(http.DetectContentType(payload))
	if detected != "application/octet-stream" || len(payload) < 12 || !bytes.Equal(payload[4:8], []byte("ftyp")) {
		return detected
	}
	switch string(payload[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim":
		return "image/heic"
	case "mif1", "msf1":
		return "image/heif"
	default:
		return detected
	}
}

func (s *SessionStore) GetSessionMediaAsset(accountScopeID, sessionID, assetID string) (SessionMediaAsset, bool, error) {
	var asset SessionMediaAsset
	ok, err := s.store.GetJSON(KeySessionMediaAsset(strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(assetID)), &asset)
	if err != nil || !ok {
		return SessionMediaAsset{}, ok, err
	}
	if asset.AccountScopeID != strings.TrimSpace(accountScopeID) || asset.SessionID != strings.TrimSpace(sessionID) || asset.ID != strings.TrimSpace(assetID) {
		return SessionMediaAsset{}, false, errors.New("media asset ownership metadata is inconsistent")
	}
	return asset, true, nil
}

// ReadSessionMediaAsset is intentionally an internal store operation: callers
// must provide the authenticated account/session scope, and no filesystem path
// or public URL is ever returned.
func (s *SessionStore) ReadSessionMediaAsset(accountScopeID, sessionID, assetID string) (SessionMediaAsset, []byte, error) {
	asset, ok, err := s.GetSessionMediaAsset(accountScopeID, sessionID, assetID)
	if err != nil {
		return SessionMediaAsset{}, nil, err
	}
	if !ok {
		return SessionMediaAsset{}, nil, errors.New("media asset not found")
	}
	payload, ok, err := s.store.GetBytes(KeySessionMediaBlob(accountScopeID, sessionID, assetID))
	if err != nil {
		return SessionMediaAsset{}, nil, err
	}
	if !ok {
		return SessionMediaAsset{}, nil, errors.New("media asset bytes are missing")
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != asset.DigestSHA256 || int64(len(payload)) != asset.Size {
		return SessionMediaAsset{}, nil, errors.New("media asset integrity check failed")
	}
	return asset, payload, nil
}

func (s *SessionStore) DeleteUnreferencedSessionMediaAsset(accountScopeID, sessionID, assetID string) (bool, error) {
	unlock := s.store.sessionMutations.lockSessions(strings.TrimSpace(sessionID))
	defer unlock()
	asset, ok, err := s.GetSessionMediaAsset(accountScopeID, sessionID, assetID)
	if err != nil || !ok {
		return false, err
	}
	if asset.ReferenceCount != 0 {
		return false, errors.New("referenced media asset is immutable and cannot be deleted")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeySessionMediaAsset(accountScopeID, sessionID, assetID)), nil); err != nil {
		return false, err
	}
	if err := batch.Delete([]byte(KeySessionMediaBlob(accountScopeID, sessionID, assetID)), nil); err != nil {
		return false, err
	}
	return true, batch.Commit(pebble.Sync)
}

func (s *SessionStore) sessionMediaUsage(accountScopeID, sessionID string) (int, int64, error) {
	count := 0
	var bytes int64
	err := s.store.IteratePrefix(SessionMediaAssetPrefix(accountScopeID, sessionID), SessionMediaDefaultQuotaAssets+1, func(_ string, value []byte) error {
		var asset SessionMediaAsset
		if err := json.Unmarshal(value, &asset); err != nil {
			return err
		}
		count++
		bytes += asset.Size
		return nil
	})
	return count, bytes, err
}

func normalizeSessionMediaReferences(references []SessionMediaReference) []SessionMediaReference {
	if len(references) == 0 {
		return nil
	}
	out := make([]SessionMediaReference, 0, len(references))
	for _, reference := range references {
		reference.AssetID = strings.TrimSpace(reference.AssetID)
		reference.Modality = strings.ToLower(strings.TrimSpace(reference.Modality))
		reference.MIMEType = normalizeSessionMediaMIME(reference.MIMEType)
		reference.FileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(reference.FileType), "."))
		reference.DigestSHA256 = strings.ToLower(strings.TrimSpace(reference.DigestSHA256))
		reference.ContractHash = strings.TrimSpace(reference.ContractHash)
		out = append(out, reference)
	}
	return out
}

func normalizeSessionMediaMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		return strings.ToLower(strings.TrimSpace(parsed))
	}
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}

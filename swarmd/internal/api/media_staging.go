package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	MediaStagingCollectionPath = "/v3/media-staging"

	mediaStagingIdempotencyHeader = "Idempotency-Key"
	mediaStagingFilenameHeader    = "X-Swarm-Media-Filename"
	mediaStagingTTLHeader         = "X-Swarm-Media-TTL-Seconds"
	mediaStagingMaxIDLength       = 36 // "stg_" plus 128 bits encoded as hex.
)

type mediaStagingHTTPRecord struct {
	ID               string                        `json:"id"`
	Status           pebblestore.MediaStagingState `json:"status"`
	Consumable       bool                          `json:"consumable"`
	DeclaredMIMEType string                        `json:"declared_mime_type"`
	DetectedMIMEType string                        `json:"detected_mime_type"`
	FileName         string                        `json:"file_name,omitempty"`
	Size             int64                         `json:"size"`
	CreatedAt        int64                         `json:"created_at"`
	ExpiresAt        int64                         `json:"expires_at"`
}

func (s *Server) handleMediaStagingCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if s == nil || s.mediaStaging == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("media staging is not configured"))
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, errors.New("media staging upload does not accept query parameters"))
		return
	}

	idempotencyKey, err := singleBoundedHeader(r, mediaStagingIdempotencyHeader, 256, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	declaredMIME, err := declaredMediaStagingMIME(r)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err)
		return
	}
	filename, err := singleBoundedHeader(r, mediaStagingFilenameHeader, 512, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ttl, err := mediaStagingTTL(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.ContentLength > pebblestore.MediaStagingDefaultMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("staged media exceeds the upload byte limit"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, pebblestore.MediaStagingDefaultMaxBytes+1)
	record, replayed, err := s.mediaStaging.Put(pebblestore.PutMediaStagingInput{
		AccountScopeID: principal.AccountScopeID, IdempotencyKey: idempotencyKey,
		DeclaredMIMEType: declaredMIME, FileName: filename, TTL: ttl,
		MaxBytes: pebblestore.MediaStagingDefaultMaxBytes, MaxCount: pebblestore.MediaStagingDefaultMaxCount,
		QuotaBytes: pebblestore.MediaStagingDefaultQuotaBytes, QuotaAssets: pebblestore.MediaStagingDefaultQuotaAssets,
		Reader: r.Body,
	})
	if err != nil {
		writeMediaStagingError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, map[string]any{"ok": true, "replayed": replayed, "staging": projectMediaStagingRecord(record, time.Now().UnixMilli())})
}

func (s *Server) handleMediaStagingItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if s == nil || s.mediaStaging == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("media staging is not configured"))
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		writeError(w, http.StatusBadRequest, errors.New("media staging item requests do not accept a query or body"))
		return
	}
	stagingID := strings.TrimPrefix(r.URL.Path, MediaStagingCollectionPath+"/")
	if !validMediaStagingID(stagingID) {
		writeError(w, http.StatusNotFound, errors.New("media staging record not found"))
		return
	}

	now := time.Now().UnixMilli()
	if r.Method == http.MethodGet {
		record, found, err := s.mediaStaging.Get(principal.AccountScopeID, stagingID)
		if err != nil {
			writeMediaStagingError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("media staging record not found"))
			return
		}
		// GET intentionally uses only metadata; it never calls Service.Read.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "staging": projectMediaStagingRecord(record, now)})
		return
	}

	record, replayed, err := s.mediaStaging.Delete(principal.AccountScopeID, stagingID, now)
	if err != nil {
		writeMediaStagingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replayed": replayed, "staging": projectMediaStagingRecord(record, now)})
}

func singleBoundedHeader(r *http.Request, name string, maxLength int, required bool) (string, error) {
	values := r.Header.Values(name)
	if len(values) > 1 {
		return "", fmt.Errorf("%s must be supplied exactly once", name)
	}
	value := ""
	if len(values) == 1 {
		value = strings.TrimSpace(values[0])
	}
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maxLength {
		return "", fmt.Errorf("%s exceeds %d characters", name, maxLength)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s contains invalid characters", name)
	}
	return value, nil
}

func declaredMediaStagingMIME(r *http.Request) (string, error) {
	value, err := singleBoundedHeader(r, "Content-Type", 256, true)
	if err != nil {
		return "", err
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(parsed, "/") {
		return "", errors.New("a valid declared Content-Type is required")
	}
	return strings.ToLower(strings.TrimSpace(parsed)), nil
}

func mediaStagingTTL(r *http.Request) (time.Duration, error) {
	value, err := singleBoundedHeader(r, mediaStagingTTLHeader, 16, false)
	if err != nil || value == "" {
		return 0, err
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", mediaStagingTTLHeader)
	}
	if seconds > int64(pebblestore.MediaStagingMaximumTTL/time.Second) {
		return 0, fmt.Errorf("%s exceeds %s", mediaStagingTTLHeader, pebblestore.MediaStagingMaximumTTL)
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl > pebblestore.MediaStagingMaximumTTL {
		return 0, fmt.Errorf("%s exceeds %s", mediaStagingTTLHeader, pebblestore.MediaStagingMaximumTTL)
	}
	return ttl, nil
}

func validMediaStagingID(value string) bool {
	if len(value) != mediaStagingMaxIDLength || !strings.HasPrefix(value, "stg_") {
		return false
	}
	for _, character := range value[len("stg_"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func projectMediaStagingRecord(record pebblestore.MediaStagingRecord, now int64) mediaStagingHTTPRecord {
	status := record.State
	consumable := status == pebblestore.MediaStagingStateStaged && now < record.ExpiresAt
	if status == pebblestore.MediaStagingStateStaged && !consumable {
		status = pebblestore.MediaStagingStateExpired
	}
	return mediaStagingHTTPRecord{
		ID: record.ID, Status: status, Consumable: consumable,
		DeclaredMIMEType: record.DeclaredMIMEType, DetectedMIMEType: record.DetectedMIMEType,
		FileName: record.FileName, Size: record.Size, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt,
	}
}

func writeMediaStagingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pebblestore.ErrMediaStagingNotFound), errors.Is(err, pebblestore.ErrMediaStagingAccountDenied):
		writeError(w, http.StatusNotFound, errors.New("media staging record not found"))
	case errors.Is(err, pebblestore.ErrMediaStagingConflict):
		writeError(w, http.StatusConflict, errors.New("media staging request conflicts with an existing idempotency key"))
	case errors.Is(err, pebblestore.ErrMediaStagingNotConsumable):
		writeError(w, http.StatusConflict, errors.New("media staging record is not consumable"))
	case errors.Is(err, pebblestore.ErrMediaStagingAlreadyBound):
		writeError(w, http.StatusConflict, errors.New("bound media staging records cannot be abandoned"))
	case errors.Is(err, pebblestore.ErrMediaStagingIntegrity):
		writeError(w, http.StatusUnprocessableEntity, errors.New("media staging content failed integrity validation"))
	case strings.Contains(err.Error(), "body exceeds") || strings.Contains(err.Error(), "request body too large"):
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("staged media exceeds the upload byte limit"))
	case strings.Contains(err.Error(), "does not match detected MIME type"):
		writeError(w, http.StatusUnsupportedMediaType, errors.New("declared Content-Type does not match staged media"))
	case strings.Contains(err.Error(), "quota exceeded"):
		writeError(w, http.StatusTooManyRequests, errors.New("media staging account quota exceeded"))
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "empty"), strings.Contains(err.Error(), "bounds"), strings.Contains(err.Error(), "TTL exceeds"):
		writeError(w, http.StatusBadRequest, errors.New("invalid media staging request"))
	default:
		writeError(w, http.StatusInternalServerError, errors.New("media staging operation failed"))
	}
}

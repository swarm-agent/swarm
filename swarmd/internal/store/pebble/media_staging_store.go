package pebblestore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	MediaStagingRecordVersion      = 1
	MediaStagingDefaultMaxBytes    = int64(20 << 20)
	MediaStagingDefaultMaxCount    = 8
	MediaStagingDefaultQuotaBytes  = int64(64 << 20)
	MediaStagingDefaultQuotaAssets = 32
	MediaStagingDefaultTTL         = time.Hour
	MediaStagingMaximumTTL         = 24 * time.Hour
)

var (
	ErrMediaStagingNotFound      = errors.New("media staging record not found")
	ErrMediaStagingAccountDenied = errors.New("media staging account scope denied")
	ErrMediaStagingConflict      = errors.New("media staging idempotency conflict")
	ErrMediaStagingNotConsumable = errors.New("media staging record is not consumable")
	ErrMediaStagingAlreadyBound  = errors.New("bound media staging record cannot be deleted")
	ErrMediaStagingIntegrity     = errors.New("media staging content integrity check failed")
)

type MediaStagingState string

const (
	MediaStagingStateStaged  MediaStagingState = "staged"
	MediaStagingStateBound   MediaStagingState = "bound"
	MediaStagingStateDeleted MediaStagingState = "deleted"
	MediaStagingStateExpired MediaStagingState = "expired"
)

// MediaStagingRecord describes temporary, account-owned bytes. It is not a
// session media asset and carries no provider, model, workspace, or session
// authority until a caller records a completed bind to SessionMediaAsset.
type MediaStagingRecord struct {
	Version          int               `json:"version"`
	ID               string            `json:"id"`
	AccountScopeID   string            `json:"account_scope_id"`
	State            MediaStagingState `json:"state"`
	DigestSHA256     string            `json:"digest_sha256"`
	DeclaredMIMEType string            `json:"declared_mime_type"`
	DetectedMIMEType string            `json:"detected_mime_type"`
	FileName         string            `json:"file_name,omitempty"`
	Size             int64             `json:"size"`
	CreatedAt        int64             `json:"created_at"`
	ExpiresAt        int64             `json:"expires_at"`
	BoundAt          int64             `json:"bound_at,omitempty"`
	BoundSessionID   string            `json:"bound_session_id,omitempty"`
	AuthorityAssetID string            `json:"authority_asset_id,omitempty"`
	DeletedAt        int64             `json:"deleted_at,omitempty"`
}

type PutMediaStagingInput struct {
	AccountScopeID   string
	IdempotencyKey   string
	DeclaredMIMEType string
	FileName         string
	TTL              time.Duration
	MaxBytes         int64
	MaxCount         int
	QuotaBytes       int64
	QuotaAssets      int
	NowUnixMs        int64
	Reader           io.Reader
}

type MediaStagingBinding struct {
	StagingID        string
	AuthorityAssetID string
	DigestSHA256     string
}

type BindMediaStagingInput struct {
	AccountScopeID string
	SessionID      string
	Bindings       []MediaStagingBinding
	NowUnixMs      int64
}

type MediaStagingExpiry struct {
	AccountScopeID string
	StagingID      string
	ExpiresAt      int64
}

type mediaStagingIdempotency struct {
	AccountScopeID string `json:"account_scope_id"`
	Key            string `json:"key"`
	RequestHash    string `json:"request_hash"`
	StagingID      string `json:"staging_id"`
}

// MediaStagingStore serializes quota and lifecycle transitions over one Pebble
// store. Different instances for the same Store share a lock.
type MediaStagingStore struct {
	store *Store
	mu    *sync.Mutex
}

var mediaStagingLocks sync.Map // map[*Store]*sync.Mutex

func NewMediaStagingStore(store *Store) *MediaStagingStore {
	if store == nil {
		return &MediaStagingStore{}
	}
	lock, _ := mediaStagingLocks.LoadOrStore(store, &sync.Mutex{})
	return &MediaStagingStore{store: store, mu: lock.(*sync.Mutex)}
}

func KeyMediaStagingRecord(accountScopeID, stagingID string) string {
	return fmt.Sprintf("v3/media_staging/records/%s/%s", keyPart(accountScopeID), keyPart(stagingID))
}

func MediaStagingRecordPrefix(accountScopeID string) string {
	return fmt.Sprintf("v3/media_staging/records/%s/", keyPart(accountScopeID))
}

func KeyMediaStagingBlob(accountScopeID, stagingID string) string {
	return fmt.Sprintf("v3/media_staging/blobs/%s/%s", keyPart(accountScopeID), keyPart(stagingID))
}

func KeyMediaStagingOwner(stagingID string) string {
	return "v3/media_staging/owners/" + keyPart(stagingID)
}

func keyMediaStagingIdempotency(accountScopeID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	return fmt.Sprintf("v3/media_staging/idempotency/%s/%s", keyPart(accountScopeID), hex.EncodeToString(sum[:]))
}

func keyMediaStagingExpiry(expiresAt int64, stagingID string) string {
	return fmt.Sprintf("v3/media_staging/expiry/%020d/%s", expiresAt, keyPart(stagingID))
}

func mediaStagingExpiryPrefix() string { return "v3/media_staging/expiry/" }

func (s *MediaStagingStore) Put(input PutMediaStagingInput) (MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return MediaStagingRecord{}, false, errors.New("media staging store is not configured")
	}
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.DeclaredMIMEType = normalizeMediaStagingMIME(input.DeclaredMIMEType)
	input.FileName = strings.TrimSpace(input.FileName)
	if input.AccountScopeID == "" || input.IdempotencyKey == "" {
		return MediaStagingRecord{}, false, errors.New("media staging account scope and idempotency key are required")
	}
	if input.DeclaredMIMEType == "" || input.Reader == nil {
		return MediaStagingRecord{}, false, errors.New("media staging MIME type and body are required")
	}
	if len(input.AccountScopeID) > 512 || len(input.IdempotencyKey) > 256 || len(input.FileName) > 512 || len(input.DeclaredMIMEType) > 256 {
		return MediaStagingRecord{}, false, errors.New("media staging metadata exceeds bounds")
	}
	if input.MaxBytes <= 0 || input.MaxBytes > MediaStagingDefaultMaxBytes {
		input.MaxBytes = MediaStagingDefaultMaxBytes
	}
	if input.MaxCount <= 0 || input.MaxCount > MediaStagingDefaultMaxCount {
		input.MaxCount = MediaStagingDefaultMaxCount
	}
	if input.QuotaBytes <= 0 || input.QuotaBytes > MediaStagingDefaultQuotaBytes {
		input.QuotaBytes = MediaStagingDefaultQuotaBytes
	}
	if input.QuotaAssets <= 0 || input.QuotaAssets > MediaStagingDefaultQuotaAssets {
		input.QuotaAssets = MediaStagingDefaultQuotaAssets
	}
	if input.TTL <= 0 {
		input.TTL = MediaStagingDefaultTTL
	}
	if input.TTL > MediaStagingMaximumTTL {
		return MediaStagingRecord{}, false, fmt.Errorf("media staging TTL exceeds %s", MediaStagingMaximumTTL)
	}
	payload, err := io.ReadAll(io.LimitReader(input.Reader, input.MaxBytes+1))
	if err != nil {
		return MediaStagingRecord{}, false, fmt.Errorf("read staged media: %w", err)
	}
	if len(payload) == 0 {
		return MediaStagingRecord{}, false, errors.New("media staging body is empty")
	}
	if int64(len(payload)) > input.MaxBytes {
		return MediaStagingRecord{}, false, fmt.Errorf("media staging body exceeds %d byte limit", input.MaxBytes)
	}
	detected := normalizeMediaStagingMIME(http.DetectContentType(payload))
	if detected != input.DeclaredMIMEType {
		return MediaStagingRecord{}, false, fmt.Errorf("declared MIME type %q does not match detected MIME type %q", input.DeclaredMIMEType, detected)
	}
	digestSum := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestSum[:])
	requestSum := sha256.Sum256([]byte(input.DeclaredMIMEType + "\x00" + input.FileName + "\x00" + digest + "\x00" + input.TTL.String()))
	requestHash := hex.EncodeToString(requestSum[:])
	// Admission failures must not reserve an idempotency key. Check an existing
	// key only after the body has passed the same validation as a first write.
	var prior mediaStagingIdempotency
	if ok, err := s.store.GetJSON(keyMediaStagingIdempotency(input.AccountScopeID, input.IdempotencyKey), &prior); err != nil {
		return MediaStagingRecord{}, false, err
	} else if ok {
		if prior.AccountScopeID != input.AccountScopeID || prior.Key != input.IdempotencyKey || prior.RequestHash != requestHash {
			return MediaStagingRecord{}, false, ErrMediaStagingConflict
		}
		record, ok, err := s.Get(input.AccountScopeID, prior.StagingID)
		if err != nil {
			return MediaStagingRecord{}, false, err
		}
		if !ok {
			return MediaStagingRecord{}, false, errors.New("media staging idempotency record is inconsistent")
		}
		return record, true, nil
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Recheck under the write lock so concurrent retries cannot create two
	// records or bypass account quota.
	prior = mediaStagingIdempotency{}
	if ok, err := s.store.GetJSON(keyMediaStagingIdempotency(input.AccountScopeID, input.IdempotencyKey), &prior); err != nil {
		return MediaStagingRecord{}, false, err
	} else if ok {
		if prior.AccountScopeID != input.AccountScopeID || prior.Key != input.IdempotencyKey || prior.RequestHash != requestHash {
			return MediaStagingRecord{}, false, ErrMediaStagingConflict
		}
		record, ok, err := s.getLocked(input.AccountScopeID, prior.StagingID)
		if err != nil {
			return MediaStagingRecord{}, false, err
		}
		if !ok {
			return MediaStagingRecord{}, false, errors.New("media staging idempotency record is inconsistent")
		}
		return record, true, nil
	}
	count, totalBytes, err := s.usageLocked(input.AccountScopeID)
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	if count >= input.MaxCount || count >= input.QuotaAssets {
		return MediaStagingRecord{}, false, errors.New("media staging asset count quota exceeded")
	}
	if totalBytes+int64(len(payload)) > input.QuotaBytes {
		return MediaStagingRecord{}, false, errors.New("media staging byte quota exceeded")
	}
	stagingID, err := newOpaqueMediaStagingID()
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	record := MediaStagingRecord{
		Version: MediaStagingRecordVersion, ID: stagingID, AccountScopeID: input.AccountScopeID,
		State: MediaStagingStateStaged, DigestSHA256: digest, DeclaredMIMEType: input.DeclaredMIMEType,
		DetectedMIMEType: detected, FileName: input.FileName, Size: int64(len(payload)), CreatedAt: now,
		ExpiresAt: now + input.TTL.Milliseconds(),
	}
	metadata, err := json.Marshal(record)
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	idempotency, err := json.Marshal(mediaStagingIdempotency{AccountScopeID: input.AccountScopeID, Key: input.IdempotencyKey, RequestHash: requestHash, StagingID: stagingID})
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for key, value := range map[string][]byte{
		KeyMediaStagingRecord(input.AccountScopeID, stagingID):                 metadata,
		KeyMediaStagingBlob(input.AccountScopeID, stagingID):                   payload,
		KeyMediaStagingOwner(stagingID):                                        []byte(input.AccountScopeID),
		keyMediaStagingIdempotency(input.AccountScopeID, input.IdempotencyKey): idempotency,
		keyMediaStagingExpiry(record.ExpiresAt, stagingID):                     []byte(input.AccountScopeID),
	} {
		if err := batch.Set([]byte(key), value, nil); err != nil {
			return MediaStagingRecord{}, false, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return MediaStagingRecord{}, false, err
	}
	return record, false, nil
}

func (s *MediaStagingStore) Get(accountScopeID, stagingID string) (MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return MediaStagingRecord{}, false, errors.New("media staging store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(strings.TrimSpace(accountScopeID), strings.TrimSpace(stagingID))
}

func (s *MediaStagingStore) getLocked(accountScopeID, stagingID string) (MediaStagingRecord, bool, error) {
	if accountScopeID == "" || stagingID == "" {
		return MediaStagingRecord{}, false, errors.New("media staging account scope and ID are required")
	}
	owner, ok, err := s.store.GetBytes(KeyMediaStagingOwner(stagingID))
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	if !ok {
		return MediaStagingRecord{}, false, nil
	}
	if string(owner) != accountScopeID {
		return MediaStagingRecord{}, false, ErrMediaStagingAccountDenied
	}
	var record MediaStagingRecord
	ok, err = s.store.GetJSON(KeyMediaStagingRecord(accountScopeID, stagingID), &record)
	if err != nil || !ok {
		return MediaStagingRecord{}, ok, err
	}
	if record.AccountScopeID != accountScopeID || record.ID != stagingID || record.Version != MediaStagingRecordVersion {
		return MediaStagingRecord{}, false, errors.New("media staging ownership metadata is inconsistent")
	}
	return record, true, nil
}

func (s *MediaStagingStore) Read(accountScopeID, stagingID string, nowUnixMs int64) (MediaStagingRecord, []byte, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return MediaStagingRecord{}, nil, errors.New("media staging store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok, err := s.getLocked(strings.TrimSpace(accountScopeID), strings.TrimSpace(stagingID))
	if err != nil {
		return MediaStagingRecord{}, nil, err
	}
	if !ok {
		return MediaStagingRecord{}, nil, ErrMediaStagingNotFound
	}
	if nowUnixMs == 0 {
		nowUnixMs = time.Now().UnixMilli()
	}
	if record.State != MediaStagingStateStaged || nowUnixMs >= record.ExpiresAt {
		return MediaStagingRecord{}, nil, ErrMediaStagingNotConsumable
	}
	payload, ok, err := s.store.GetBytes(KeyMediaStagingBlob(record.AccountScopeID, record.ID))
	if err != nil {
		return MediaStagingRecord{}, nil, err
	}
	if !ok {
		return MediaStagingRecord{}, nil, errors.New("media staging bytes are missing")
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != record.DigestSHA256 || int64(len(payload)) != record.Size {
		return MediaStagingRecord{}, nil, ErrMediaStagingIntegrity
	}
	return record, payload, nil
}

// Bind atomically consumes all listed staging blobs after the caller has
// materialized the corresponding authoritative SessionMediaAsset records.
// Routed creation uses the same preflight/batch helpers to include this
// transition in the canonical V3 mutation commit. Exact repeats are idempotent;
// partial or conflicting repeats fail closed.
func (s *MediaStagingStore) Bind(input BindMediaStagingInput) ([]MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return nil, false, errors.New("media staging store is not configured")
	}
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.AccountScopeID == "" || input.SessionID == "" || len(input.Bindings) == 0 {
		return nil, false, errors.New("media staging account, session, and bindings are required")
	}
	if len(input.AccountScopeID) > 512 || len(input.SessionID) > 512 {
		return nil, false, errors.New("media staging binding scope exceeds bounds")
	}
	if len(input.Bindings) > MediaStagingDefaultMaxCount {
		return nil, false, errors.New("media staging binding count exceeds bounds")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindLocked(BindMediaStagingInput{
		AccountScopeID: input.AccountScopeID,
		SessionID:      input.SessionID,
		Bindings:       normalizeMediaStagingBindings(input.Bindings),
		NowUnixMs:      now,
	}, now)
}

func (s *MediaStagingStore) prepareBindLocked(input BindMediaStagingInput) ([]MediaStagingRecord, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Bindings = normalizeMediaStagingBindings(input.Bindings)
	if input.AccountScopeID == "" || input.SessionID == "" || len(input.Bindings) == 0 {
		return nil, false, errors.New("media staging account, session, and bindings are required")
	}
	if len(input.AccountScopeID) > 512 || len(input.SessionID) > 512 {
		return nil, false, errors.New("media staging binding scope exceeds bounds")
	}
	if len(input.Bindings) > MediaStagingDefaultMaxCount {
		return nil, false, errors.New("media staging binding count exceeds bounds")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return s.preflightBindLocked(input, now)
}

func (s *MediaStagingStore) bindLocked(input BindMediaStagingInput, now int64) ([]MediaStagingRecord, bool, error) {
	records, replayed, err := s.preflightBindLocked(input, now)
	if err != nil || replayed {
		return records, replayed, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setMediaStagingBindingsInBatch(batch, records, input.Bindings, input.SessionID, now); err != nil {
		return nil, false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, false, err
	}
	return boundMediaStagingRecords(records, input.Bindings, input.SessionID, now), false, nil
}

func (s *MediaStagingStore) preflightBindLocked(input BindMediaStagingInput, now int64) ([]MediaStagingRecord, bool, error) {
	seen := make(map[string]struct{}, len(input.Bindings))
	records := make([]MediaStagingRecord, len(input.Bindings))
	allReplayed := true
	for index, binding := range input.Bindings {
		binding.StagingID = strings.TrimSpace(binding.StagingID)
		binding.AuthorityAssetID = strings.TrimSpace(binding.AuthorityAssetID)
		binding.DigestSHA256 = strings.ToLower(strings.TrimSpace(binding.DigestSHA256))
		if binding.StagingID == "" || binding.AuthorityAssetID == "" || binding.DigestSHA256 == "" {
			return nil, false, errors.New("complete media staging binding identity is required")
		}
		if len(binding.StagingID) > 128 || len(binding.AuthorityAssetID) > 256 || len(binding.DigestSHA256) != sha256.Size*2 {
			return nil, false, errors.New("media staging binding identity exceeds bounds")
		}
		if _, duplicate := seen[binding.StagingID]; duplicate {
			return nil, false, errors.New("duplicate media staging binding")
		}
		seen[binding.StagingID] = struct{}{}
		record, ok, err := s.getLocked(input.AccountScopeID, binding.StagingID)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, ErrMediaStagingNotFound
		}
		if record.DigestSHA256 != binding.DigestSHA256 {
			return nil, false, ErrMediaStagingIntegrity
		}
		switch record.State {
		case MediaStagingStateStaged:
			if now >= record.ExpiresAt {
				return nil, false, ErrMediaStagingNotConsumable
			}
			allReplayed = false
		case MediaStagingStateBound:
			if record.BoundSessionID != input.SessionID || record.AuthorityAssetID != binding.AuthorityAssetID {
				return nil, false, ErrMediaStagingConflict
			}
		default:
			return nil, false, ErrMediaStagingNotConsumable
		}
		records[index] = record
	}
	if allReplayed {
		return records, true, nil
	}
	for _, record := range records {
		if record.State == MediaStagingStateBound {
			return nil, false, ErrMediaStagingConflict
		}
	}
	return records, false, nil
}

func setMediaStagingBindingsInBatch(batch *pebble.Batch, records []MediaStagingRecord, bindings []MediaStagingBinding, sessionID string, now int64) error {
	if batch == nil || len(records) == 0 || len(records) != len(bindings) {
		return errors.New("complete media staging batch bindings are required")
	}
	for _, record := range boundMediaStagingRecords(records, bindings, sessionID, now) {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyMediaStagingRecord(record.AccountScopeID, record.ID)), payload, nil); err != nil {
			return err
		}
		if err := batch.Delete([]byte(KeyMediaStagingBlob(record.AccountScopeID, record.ID)), nil); err != nil {
			return err
		}
		if err := batch.Delete([]byte(keyMediaStagingExpiry(record.ExpiresAt, record.ID)), nil); err != nil {
			return err
		}
	}
	return nil
}

func normalizeMediaStagingBindings(bindings []MediaStagingBinding) []MediaStagingBinding {
	normalized := append([]MediaStagingBinding(nil), bindings...)
	for index := range normalized {
		normalized[index].StagingID = strings.TrimSpace(normalized[index].StagingID)
		normalized[index].AuthorityAssetID = strings.TrimSpace(normalized[index].AuthorityAssetID)
		normalized[index].DigestSHA256 = strings.ToLower(strings.TrimSpace(normalized[index].DigestSHA256))
	}
	return normalized
}

func boundMediaStagingRecords(records []MediaStagingRecord, bindings []MediaStagingBinding, sessionID string, now int64) []MediaStagingRecord {
	bound := append([]MediaStagingRecord(nil), records...)
	for index := range bound {
		bound[index].State = MediaStagingStateBound
		bound[index].BoundAt = now
		bound[index].BoundSessionID = strings.TrimSpace(sessionID)
		bound[index].AuthorityAssetID = strings.TrimSpace(bindings[index].AuthorityAssetID)
	}
	return bound
}

func (s *MediaStagingStore) Delete(accountScopeID, stagingID string, nowUnixMs int64) (MediaStagingRecord, bool, error) {
	return s.transitionTerminal(accountScopeID, stagingID, nowUnixMs, MediaStagingStateDeleted, false)
}

func (s *MediaStagingStore) Expire(accountScopeID, stagingID string, nowUnixMs int64) (MediaStagingRecord, bool, error) {
	return s.transitionTerminal(accountScopeID, stagingID, nowUnixMs, MediaStagingStateExpired, true)
}

func (s *MediaStagingStore) transitionTerminal(accountScopeID, stagingID string, nowUnixMs int64, state MediaStagingState, requireExpired bool) (MediaStagingRecord, bool, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return MediaStagingRecord{}, false, errors.New("media staging store is not configured")
	}
	if nowUnixMs == 0 {
		nowUnixMs = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok, err := s.getLocked(strings.TrimSpace(accountScopeID), strings.TrimSpace(stagingID))
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	if !ok {
		return MediaStagingRecord{}, false, ErrMediaStagingNotFound
	}
	if record.State == state {
		return record, true, nil
	}
	if record.State == MediaStagingStateBound {
		return MediaStagingRecord{}, false, ErrMediaStagingAlreadyBound
	}
	if record.State != MediaStagingStateStaged || (requireExpired && nowUnixMs < record.ExpiresAt) {
		return MediaStagingRecord{}, false, ErrMediaStagingNotConsumable
	}
	record.State = state
	record.DeletedAt = nowUnixMs
	payload, err := json.Marshal(record)
	if err != nil {
		return MediaStagingRecord{}, false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyMediaStagingRecord(record.AccountScopeID, record.ID)), payload, nil); err != nil {
		return MediaStagingRecord{}, false, err
	}
	if err := batch.Delete([]byte(KeyMediaStagingBlob(record.AccountScopeID, record.ID)), nil); err != nil {
		return MediaStagingRecord{}, false, err
	}
	if err := batch.Delete([]byte(keyMediaStagingExpiry(record.ExpiresAt, record.ID)), nil); err != nil {
		return MediaStagingRecord{}, false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return MediaStagingRecord{}, false, err
	}
	return record, false, nil
}

func (s *MediaStagingStore) ListExpired(nowUnixMs int64, limit int) ([]MediaStagingExpiry, error) {
	if s == nil || s.store == nil || s.mu == nil {
		return nil, errors.New("media staging store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if nowUnixMs == 0 {
		nowUnixMs = time.Now().UnixMilli()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	items := make([]MediaStagingExpiry, 0, limit)
	stop := errors.New("media staging expiry scan complete")
	err := s.store.IteratePrefix(mediaStagingExpiryPrefix(), limit+1, func(key string, value []byte) error {
		parts := strings.SplitN(strings.TrimPrefix(key, mediaStagingExpiryPrefix()), "/", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return errors.New("decode media staging expiry index")
		}
		expiresAt, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return fmt.Errorf("decode media staging expiry index: %w", err)
		}
		if expiresAt > nowUnixMs || len(items) >= limit {
			return stop
		}
		items = append(items, MediaStagingExpiry{AccountScopeID: string(value), StagingID: parts[1], ExpiresAt: expiresAt})
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, err
	}
	return items, nil
}

func (s *MediaStagingStore) usageLocked(accountScopeID string) (int, int64, error) {
	count := 0
	var total int64
	err := s.store.IteratePrefix(MediaStagingRecordPrefix(accountScopeID), MediaStagingDefaultQuotaAssets+1, func(_ string, value []byte) error {
		var record MediaStagingRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if record.AccountScopeID != accountScopeID {
			return errors.New("media staging usage ownership metadata is inconsistent")
		}
		if record.State == MediaStagingStateStaged {
			count++
			total += record.Size
		}
		return nil
	})
	return count, total, err
}

func newOpaqueMediaStagingID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate media staging ID: %w", err)
	}
	return "stg_" + hex.EncodeToString(value[:]), nil
}

func normalizeMediaStagingMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		return strings.ToLower(strings.TrimSpace(parsed))
	}
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}

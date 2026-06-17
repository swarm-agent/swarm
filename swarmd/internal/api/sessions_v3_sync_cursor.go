package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

const (
	v3SyncCursorPrefix       = "v3c1."
	v3SyncCursorVersion      = 1
	v3SyncCursorKindEndpoint = "endpoint"
	v3SyncCursorDefaultKID   = "dev-v3-sync-cursor"
)

var v3SyncCursorDefaultKey = []byte("swarm-v3-sync-cursor-default-non-secret-test-key-v1")

type v3SyncCursorScope struct {
	AccountScopeID     string
	PrincipalUserID    string
	Surface            string
	StreamKind         string
	SelectorFilterHash string
	ResourceSet        string
}

type v3SyncCursorPayload struct {
	Version            int    `json:"version"`
	Kind               string `json:"kind"`
	Account            string `json:"account"`
	Principal          string `json:"principal"`
	Surface            string `json:"surface"`
	StreamKind         string `json:"stream_kind"`
	SelectorFilterHash string `json:"selector_filter_hash"`
	ResourceSet        string `json:"resource_set"`
	AfterEndpointSeq   uint64 `json:"after_endpoint_seq"`
	IssuedAt           int64  `json:"issued_at"`
	KID                string `json:"kid"`
}

type v3SyncCursorKeyring struct {
	CurrentKID string
	CurrentKey []byte
	Previous   map[string][]byte
	Err        error
}

type v3SyncCursorError struct {
	Code              string
	BootstrapRequired bool
	OldestAvailable   uint64
	Latest            uint64
	Err               error
}

func (e *v3SyncCursorError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *v3SyncCursorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newV3SyncCursorError(code string, err error) *v3SyncCursorError {
	return &v3SyncCursorError{Code: code, Err: err}
}

func newV3SyncCursorKeyring(dataDir string) *v3SyncCursorKeyring {
	key := v3SyncCursorDefaultKey
	kid := v3SyncCursorDefaultKID
	dataDir = strings.TrimSpace(dataDir)
	if dataDir != "" {
		loaded, err := loadOrCreateV3SyncCursorKey(dataDir)
		if err != nil {
			return &v3SyncCursorKeyring{Err: fmt.Errorf("load v3 sync cursor signing key: %w", err), Previous: map[string][]byte{}}
		}
		if len(loaded) < 32 {
			return &v3SyncCursorKeyring{Err: fmt.Errorf("v3 sync cursor signing key is too short: %d bytes", len(loaded)), Previous: map[string][]byte{}}
		}
		key = loaded
		sum := sha256.Sum256(loaded)
		kid = "v3sync-" + hex.EncodeToString(sum[:6])
	}
	return &v3SyncCursorKeyring{CurrentKID: kid, CurrentKey: append([]byte(nil), key...), Previous: map[string][]byte{}}
}

func loadOrCreateV3SyncCursorKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "v3-sync-cursor.key")
	if raw, err := os.ReadFile(path); err == nil {
		decoded, decErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decErr != nil {
			return nil, decErr
		}
		return decoded, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Server) v3SyncCursorKeyring() *v3SyncCursorKeyring {
	if s == nil {
		return newV3SyncCursorKeyring("")
	}
	if s.v3SyncCursors == nil {
		s.v3SyncCursors = newV3SyncCursorKeyring(s.dataDir)
	}
	return s.v3SyncCursors
}

func v3SyncCursorScopeForRealtime(principal identity.Principal, surface string) v3SyncCursorScope {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		surface = "desktop"
	}
	return v3SyncCursorScope{
		AccountScopeID:     strings.TrimSpace(principal.AccountScopeID),
		PrincipalUserID:    strings.TrimSpace(principal.UserID),
		Surface:            surface,
		StreamKind:         "v3.realtime",
		SelectorFilterHash: v3SyncDeterministicSelectorHash("realtime:session-subscriptions"),
		ResourceSet:        "events,projections",
	}
}

func v3SyncCursorScopeForSnapshot(principal identity.Principal, surface, streamKind string, selector any, resources []string) v3SyncCursorScope {
	if strings.TrimSpace(surface) == "" {
		surface = "desktop"
	}
	if strings.TrimSpace(streamKind) == "" {
		streamKind = "snapshot"
	}
	return v3SyncCursorScope{
		AccountScopeID:     strings.TrimSpace(principal.AccountScopeID),
		PrincipalUserID:    strings.TrimSpace(principal.UserID),
		Surface:            strings.TrimSpace(surface),
		StreamKind:         strings.TrimSpace(streamKind),
		SelectorFilterHash: v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(selector)),
		ResourceSet:        canonicalV3SyncResourceSet(resources),
	}
}

func v3SyncCanonicalSelector(selector any) string {
	raw, err := json.Marshal(selector)
	if err != nil {
		return fmt.Sprintf("%#v", selector)
	}
	return string(raw)
}

func v3SyncDeterministicSelectorHash(selector string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(selector)))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) signV3SyncEndpointCursorFromLegacy(scope v3SyncCursorScope, legacyCursor string) (string, error) {
	seq, err := parseV3RealtimeEndpointCursorStrict(legacyCursor)
	if err != nil {
		return "", err
	}
	return s.signV3SyncEndpointCursor(scope, seq)
}

func (s *Server) signV3SyncEndpointCursor(scope v3SyncCursorScope, endpointSeq uint64) (string, error) {
	keyring := s.v3SyncCursorKeyring()
	if keyring.Err != nil {
		return "", keyring.Err
	}
	if len(keyring.CurrentKey) < 32 || strings.TrimSpace(keyring.CurrentKID) == "" {
		return "", errors.New("v3 sync cursor signing key is not configured")
	}
	payload := v3SyncCursorPayload{
		Version:            v3SyncCursorVersion,
		Kind:               v3SyncCursorKindEndpoint,
		Account:            strings.TrimSpace(scope.AccountScopeID),
		Principal:          strings.TrimSpace(scope.PrincipalUserID),
		Surface:            strings.TrimSpace(scope.Surface),
		StreamKind:         strings.TrimSpace(scope.StreamKind),
		SelectorFilterHash: strings.TrimSpace(scope.SelectorFilterHash),
		ResourceSet:        strings.TrimSpace(scope.ResourceSet),
		AfterEndpointSeq:   endpointSeq,
		IssuedAt:           time.Now().Unix(),
		KID:                keyring.CurrentKID,
	}
	return encodeV3SyncCursorPayload(payload, keyring.CurrentKey)
}

func encodeV3SyncCursorPayload(payload v3SyncCursorPayload, key []byte) (string, error) {
	if payload.Version == 0 {
		payload.Version = v3SyncCursorVersion
	}
	if strings.TrimSpace(payload.KID) == "" {
		return "", errors.New("v3 sync cursor kid is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	sig := signV3SyncCursorBody(body, key)
	return v3SyncCursorPrefix + body + "." + sig, nil
}

func signV3SyncCursorBody(body string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) parseV3SyncEndpointCursor(raw string, expected v3SyncCursorScope) (uint64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	if strings.HasPrefix(raw, "cursor-") {
		seq, err := strconv.ParseUint(strings.TrimPrefix(raw, "cursor-"), 10, 64)
		if err != nil {
			return 0, false, newV3SyncCursorError("endpoint_cursor_malformed", fmt.Errorf("malformed endpoint_cursor %q", raw))
		}
		return seq, true, nil
	}
	payload, err := s.verifyV3SyncCursor(raw)
	if err != nil {
		return 0, false, err
	}
	if payload.Kind != v3SyncCursorKindEndpoint {
		return 0, false, newV3SyncCursorError("endpoint_cursor_unsupported_kind", fmt.Errorf("unsupported sync cursor kind %q", payload.Kind))
	}
	if err := validateV3SyncCursorScope(payload, expected); err != nil {
		return 0, false, err
	}
	return payload.AfterEndpointSeq, false, nil
}

func (s *Server) parseV3RealtimeEndpointCursor(raw string, principal identity.Principal, surface string) (uint64, bool, error) {
	realtimeScope := v3SyncCursorScopeForRealtime(principal, surface)
	seq, legacy, err := s.parseV3SyncEndpointCursor(raw, realtimeScope)
	if err == nil {
		if legacy {
			return 0, false, newV3SyncCursorError("endpoint_cursor_legacy_unsupported", errors.New("v3 realtime requires a signed scoped endpoint_cursor"))
		}
		return seq, false, nil
	}
	var cursorErr *v3SyncCursorError
	if !errors.As(err, &cursorErr) || cursorErr.Code != "endpoint_cursor_scope_mismatch" {
		return 0, false, err
	}
	payload, verifyErr := s.verifyV3SyncCursor(strings.TrimSpace(raw))
	if verifyErr != nil {
		return 0, false, err
	}
	if payload.Kind != v3SyncCursorKindEndpoint {
		return 0, false, newV3SyncCursorError("endpoint_cursor_unsupported_kind", fmt.Errorf("unsupported sync cursor kind %q", payload.Kind))
	}
	if err := validateV3RealtimeSnapshotHandoffCursorScope(payload, realtimeScope); err != nil {
		return 0, false, err
	}
	return payload.AfterEndpointSeq, false, nil
}

func (s *Server) verifyV3SyncCursor(raw string) (v3SyncCursorPayload, error) {
	if !strings.HasPrefix(raw, v3SyncCursorPrefix) {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_malformed", fmt.Errorf("malformed endpoint_cursor %q", raw))
	}
	parts := strings.Split(strings.TrimPrefix(raw, v3SyncCursorPrefix), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_malformed", fmt.Errorf("malformed endpoint_cursor %q", raw))
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_malformed", fmt.Errorf("malformed endpoint_cursor payload: %w", err))
	}
	var payload v3SyncCursorPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_malformed", fmt.Errorf("malformed endpoint_cursor payload: %w", err))
	}
	if payload.Version != v3SyncCursorVersion {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_unsupported_version", fmt.Errorf("unsupported sync cursor version %d", payload.Version))
	}
	key, ok := s.v3SyncCursorVerificationKey(payload.KID)
	if !ok {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_retired_key", fmt.Errorf("sync cursor signing key %q is unavailable", payload.KID))
	}
	want := signV3SyncCursorBody(parts[0], key)
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return v3SyncCursorPayload{}, newV3SyncCursorError("endpoint_cursor_tampered", errors.New("sync cursor signature is invalid"))
	}
	return payload, nil
}

func (s *Server) v3SyncCursorVerificationKey(kid string) ([]byte, bool) {
	kid = strings.TrimSpace(kid)
	keyring := s.v3SyncCursorKeyring()
	if keyring.Err != nil {
		return nil, false
	}
	if kid == keyring.CurrentKID {
		return keyring.CurrentKey, true
	}
	if keyring.Previous != nil {
		key, ok := keyring.Previous[kid]
		return key, ok
	}
	return nil, false
}

func validateV3SyncCursorScope(payload v3SyncCursorPayload, expected v3SyncCursorScope) error {
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"account", payload.Account, expected.AccountScopeID},
		{"principal", payload.Principal, expected.PrincipalUserID},
		{"surface", payload.Surface, expected.Surface},
		{"stream_kind", payload.StreamKind, expected.StreamKind},
		{"selector_filter_hash", payload.SelectorFilterHash, expected.SelectorFilterHash},
		{"resource_set", payload.ResourceSet, expected.ResourceSet},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.got) != strings.TrimSpace(check.want) {
			return newV3SyncCursorError("endpoint_cursor_scope_mismatch", fmt.Errorf("sync cursor scope mismatch for %s", check.name))
		}
	}
	return nil
}

func validateV3RealtimeSnapshotHandoffCursorScope(payload v3SyncCursorPayload, realtimeScope v3SyncCursorScope) error {
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"account", payload.Account, realtimeScope.AccountScopeID},
		{"principal", payload.Principal, realtimeScope.PrincipalUserID},
		{"surface", payload.Surface, realtimeScope.Surface},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.got) != strings.TrimSpace(check.want) {
			return newV3SyncCursorError("endpoint_cursor_scope_mismatch", fmt.Errorf("sync cursor scope mismatch for %s", check.name))
		}
	}
	if strings.TrimSpace(payload.StreamKind) != "v3.sync.snapshot" {
		return newV3SyncCursorError("endpoint_cursor_scope_mismatch", fmt.Errorf("sync cursor scope mismatch for stream_kind"))
	}
	if strings.TrimSpace(payload.SelectorFilterHash) == "" {
		return newV3SyncCursorError("endpoint_cursor_scope_mismatch", fmt.Errorf("sync cursor scope mismatch for selector_filter_hash"))
	}
	if strings.TrimSpace(payload.ResourceSet) == "" {
		return newV3SyncCursorError("endpoint_cursor_scope_mismatch", fmt.Errorf("sync cursor scope mismatch for resource_set"))
	}
	return nil
}

func canonicalV3SyncResourceSet(resources []string) string {
	cleaned := make([]string, 0, len(resources))
	seen := map[string]struct{}{}
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			continue
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		cleaned = append(cleaned, resource)
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

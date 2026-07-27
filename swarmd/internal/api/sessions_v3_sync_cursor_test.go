package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

func TestV3SyncCursorSignsVerifiesAndRejectsTamper(t *testing.T) {
	server := &Server{}
	scope := testV3SyncCursorScope()
	cursor, err := server.signV3SyncEndpointCursor(scope, 42)
	if err != nil {
		t.Fatalf("sign cursor: %v", err)
	}
	if !strings.HasPrefix(cursor, v3SyncCursorPrefix) || strings.Contains(cursor, "cursor-42") {
		t.Fatalf("cursor = %q, want opaque signed v3 cursor", cursor)
	}
	seq, legacy, err := server.parseV3SyncEndpointCursor(cursor, scope)
	if err != nil {
		t.Fatalf("parse signed cursor: %v", err)
	}
	if legacy || seq != 42 {
		t.Fatalf("parse signed cursor = seq:%d legacy:%v, want seq 42 non-legacy", seq, legacy)
	}
	tampered := cursor[:len(cursor)-1] + "x"
	assertV3SyncCursorCode(t, server, tampered, scope, "endpoint_cursor_tampered")
}

func TestV3SyncCursorRejectsEveryWrongScopeField(t *testing.T) {
	server := &Server{}
	scope := testV3SyncCursorScope()
	cursor, err := server.signV3SyncEndpointCursor(scope, 7)
	if err != nil {
		t.Fatalf("sign cursor: %v", err)
	}
	cases := map[string]func(v3SyncCursorScope) v3SyncCursorScope{
		"account":     func(s v3SyncCursorScope) v3SyncCursorScope { s.AccountScopeID = "other-account"; return s },
		"principal":   func(s v3SyncCursorScope) v3SyncCursorScope { s.PrincipalUserID = "other-user"; return s },
		"surface":     func(s v3SyncCursorScope) v3SyncCursorScope { s.Surface = "tui"; return s },
		"stream_kind": func(s v3SyncCursorScope) v3SyncCursorScope { s.StreamKind = "snapshot"; return s },
		"selector_filter_hash": func(s v3SyncCursorScope) v3SyncCursorScope {
			s.SelectorFilterHash = v3SyncDeterministicSelectorHash("different-selector")
			return s
		},
		"resource_set": func(s v3SyncCursorScope) v3SyncCursorScope { s.ResourceSet = "events"; return s },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			assertV3SyncCursorCode(t, server, cursor, mutate(scope), "endpoint_cursor_scope_mismatch")
		})
	}
}

func TestV3SyncCursorMalformedAndUnsupportedCodes(t *testing.T) {
	server := &Server{}
	scope := testV3SyncCursorScope()
	if seq, legacy, err := server.parseV3SyncEndpointCursor("", scope); err != nil || seq != 0 || legacy {
		t.Fatalf("empty parse = seq:%d legacy:%v err:%v, want zero/no error", seq, legacy, err)
	}
	for name, raw := range map[string]string{
		"random garbage": "not-a-cursor",
		"bad split":      v3SyncCursorPrefix + "no-dot",
		"bad base64":     v3SyncCursorPrefix + "%%%" + ".sig",
		"bad json":       v3SyncCursorPrefix + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig",
	} {
		t.Run(name, func(t *testing.T) {
			assertV3SyncCursorCode(t, server, raw, scope, "endpoint_cursor_malformed")
		})
	}

	payload := testV3SyncCursorPayload(server, scope, 8)
	payload.Version = 99
	unsupportedVersion := encodeV3SyncCursorPayloadForTest(t, server, payload)
	assertV3SyncCursorCode(t, server, unsupportedVersion, scope, "endpoint_cursor_unsupported_version")

	payload = testV3SyncCursorPayload(server, scope, 8)
	payload.Kind = "snapshot"
	unsupportedKind := encodeV3SyncCursorPayloadForTest(t, server, payload)
	assertV3SyncCursorCode(t, server, unsupportedKind, scope, "endpoint_cursor_unsupported_kind")

	payload = testV3SyncCursorPayload(server, scope, 8)
	payload.KID = "missing-kid"
	unknownKID := encodeV3SyncCursorPayloadForTestWithKey(t, payload, server.v3SyncCursorKeyring().CurrentKey)
	assertV3SyncCursorCode(t, server, unknownKID, scope, "endpoint_cursor_retired_key")
}

func TestV3SyncCursorLegacyUpgradeKeyPersistenceAndRetiredKey(t *testing.T) {
	server := &Server{dataDir: t.TempDir()}
	scope := testV3SyncCursorScope()
	seq, legacy, err := server.parseV3SyncEndpointCursor("cursor-13", scope)
	if err != nil {
		t.Fatalf("parse legacy cursor: %v", err)
	}
	if !legacy || seq != 13 {
		t.Fatalf("legacy parse = seq:%d legacy:%v, want seq 13 legacy", seq, legacy)
	}
	upgraded, err := server.signV3SyncEndpointCursor(scope, seq)
	if err != nil {
		t.Fatalf("sign upgraded cursor: %v", err)
	}
	serverAfterRestart := &Server{dataDir: server.dataDir}
	parsed, legacy, err := serverAfterRestart.parseV3SyncEndpointCursor(upgraded, scope)
	if err != nil {
		t.Fatalf("parse after restart: %v", err)
	}
	if legacy || parsed != 13 {
		t.Fatalf("restart parse = seq:%d legacy:%v, want seq 13 non-legacy", parsed, legacy)
	}
	retiredKeyServer := &Server{}
	assertV3SyncCursorCode(t, retiredKeyServer, upgraded, scope, "endpoint_cursor_retired_key")
}

func TestV3SyncCursorKeyringFailsClosedForBadPersistentKey(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt": "not base64!!!",
		"short":   base64.RawURLEncoding.EncodeToString([]byte("short")),
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dataDir, "v3-sync-cursor.key"), []byte(content), 0o600); err != nil {
				t.Fatalf("write bad key: %v", err)
			}
			server := &Server{dataDir: dataDir}
			if _, err := server.signV3SyncEndpointCursor(testV3SyncCursorScope(), 1); err == nil {
				t.Fatalf("signing succeeded with bad persistent key")
			}
			if server.v3SyncCursorKeyring().CurrentKID == v3SyncCursorDefaultKID {
				t.Fatalf("bad persistent key fell back to default dev kid")
			}
		})
	}
}

func TestV3SyncCursorPreviousKeyVerification(t *testing.T) {
	scope := testV3SyncCursorScope()
	oldKey := []byte("old-v3-sync-cursor-key-32-bytes!!!")
	newKey := []byte("new-v3-sync-cursor-key-32-bytes!!!")
	oldServer := &Server{v3SyncCursors: &v3SyncCursorKeyring{CurrentKID: "old", CurrentKey: oldKey, Previous: map[string][]byte{}}}
	oldCursor, err := oldServer.signV3SyncEndpointCursor(scope, 55)
	if err != nil {
		t.Fatalf("sign old cursor: %v", err)
	}
	rotated := &Server{v3SyncCursors: &v3SyncCursorKeyring{CurrentKID: "new", CurrentKey: newKey, Previous: map[string][]byte{"old": oldKey}}}
	seq, legacy, err := rotated.parseV3SyncEndpointCursor(oldCursor, scope)
	if err != nil || legacy || seq != 55 {
		t.Fatalf("rotated previous-key parse = seq:%d legacy:%v err:%v", seq, legacy, err)
	}
	newCursor, err := rotated.signV3SyncEndpointCursor(scope, 56)
	if err != nil {
		t.Fatalf("sign new cursor: %v", err)
	}
	payload, err := rotated.verifyV3SyncCursor(newCursor)
	if err != nil || payload.KID != "new" {
		t.Fatalf("new cursor payload = %+v err=%v, want kid new", payload, err)
	}
	removed := &Server{v3SyncCursors: &v3SyncCursorKeyring{CurrentKID: "new", CurrentKey: newKey, Previous: map[string][]byte{}}}
	assertV3SyncCursorCode(t, removed, oldCursor, scope, "endpoint_cursor_retired_key")
}

func TestV3SyncCursorCanonicalSelectorAndResourceSet(t *testing.T) {
	a := v3SyncCanonicalSelector(map[string]any{"b": 2, "a": map[string]any{"d": 4, "c": 3}})
	b := v3SyncCanonicalSelector(map[string]any{"a": map[string]any{"c": 3, "d": 4}, "b": 2})
	if v3SyncDeterministicSelectorHash(a) != v3SyncDeterministicSelectorHash(b) {
		t.Fatalf("same selector with different key order hashed differently: %s vs %s", a, b)
	}
	changed := v3SyncCanonicalSelector(map[string]any{"a": map[string]any{"c": 99, "d": 4}, "b": 2})
	if v3SyncDeterministicSelectorHash(a) == v3SyncDeterministicSelectorHash(changed) {
		t.Fatalf("different selector values produced same deterministic hash")
	}
	if got := canonicalV3SyncResourceSet([]string{" projections ", "events", "events", "plans"}); got != "events,plans,projections" {
		t.Fatalf("canonical resource set = %q", got)
	}
}

func TestV3SyncLegacySnapshotCursorIsRealtimeResumeScope(t *testing.T) {
	server := &Server{}
	principal := identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}
	legacyScope := v3SyncCursorScopeForRealtime(principal, "desktop")
	cursor, err := server.signV3SyncEndpointCursor(legacyScope, 9)
	if err != nil {
		t.Fatalf("sign legacy bridge cursor: %v", err)
	}
	if _, _, err := server.parseV3SyncEndpointCursor(cursor, legacyScope); err != nil {
		t.Fatalf("legacy bridge realtime scope rejected: %v", err)
	}
	snapshotScope := v3SyncCursorScopeForSnapshot(principal, "desktop", "snapshot", map[string]any{"surface": "desktop"}, []string{"sessions"})
	assertV3SyncCursorCode(t, server, cursor, snapshotScope, "endpoint_cursor_scope_mismatch")
}

func testV3SyncCursorPayload(server *Server, scope v3SyncCursorScope, seq uint64) v3SyncCursorPayload {
	return v3SyncCursorPayload{
		Version:            v3SyncCursorVersion,
		Kind:               v3SyncCursorKindEndpoint,
		Account:            scope.AccountScopeID,
		Principal:          scope.PrincipalUserID,
		Surface:            scope.Surface,
		StreamKind:         scope.StreamKind,
		SelectorFilterHash: scope.SelectorFilterHash,
		ResourceSet:        scope.ResourceSet,
		AfterEndpointSeq:   seq,
		IssuedAt:           1,
		KID:                server.v3SyncCursorKeyring().CurrentKID,
	}
}

func encodeV3SyncCursorPayloadForTest(t *testing.T, server *Server, payload v3SyncCursorPayload) string {
	t.Helper()
	return encodeV3SyncCursorPayloadForTestWithKey(t, payload, server.v3SyncCursorKeyring().CurrentKey)
}

func encodeV3SyncCursorPayloadForTestWithKey(t *testing.T, payload v3SyncCursorPayload, key []byte) string {
	t.Helper()
	cursor, err := encodeV3SyncCursorPayload(payload, key)
	if err != nil {
		t.Fatalf("encode cursor payload: %v", err)
	}
	return cursor
}

func assertV3SyncCursorCode(t *testing.T, server *Server, raw string, scope v3SyncCursorScope, wantCode string) {
	t.Helper()
	_, _, err := server.parseV3SyncEndpointCursor(raw, scope)
	if err == nil {
		t.Fatalf("parse cursor succeeded, want code %s", wantCode)
	}
	var cursorErr *v3SyncCursorError
	if !errors.As(err, &cursorErr) {
		t.Fatalf("parse error = %T %v, want v3SyncCursorError code %s", err, err, wantCode)
	}
	if cursorErr.Code != wantCode {
		rawPayload, _ := json.Marshal(cursorErr)
		t.Fatalf("cursor error code = %q, want %q (err=%v payload=%s)", cursorErr.Code, wantCode, err, rawPayload)
	}
}

func testV3SyncCursorScope() v3SyncCursorScope {
	return v3SyncCursorScopeForRealtime(identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, "desktop")
}

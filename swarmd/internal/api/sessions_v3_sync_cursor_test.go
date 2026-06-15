package api

import (
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
	if _, _, err := server.parseV3SyncEndpointCursor(tampered, scope); err == nil {
		t.Fatal("tampered cursor verified successfully")
	}
}

func TestV3SyncCursorRejectsWrongScopeAndUnsupportedVersion(t *testing.T) {
	server := &Server{}
	scope := testV3SyncCursorScope()
	cursor, err := server.signV3SyncEndpointCursor(scope, 7)
	if err != nil {
		t.Fatalf("sign cursor: %v", err)
	}
	wrongAccount := scope
	wrongAccount.AccountScopeID = "other-account"
	if _, _, err := server.parseV3SyncEndpointCursor(cursor, wrongAccount); err == nil || !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("wrong-account parse err = %v, want scope mismatch", err)
	}
	wrongPrincipal := scope
	wrongPrincipal.PrincipalUserID = "other-user"
	if _, _, err := server.parseV3SyncEndpointCursor(cursor, wrongPrincipal); err == nil || !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("wrong-principal parse err = %v, want scope mismatch", err)
	}
	wrongFilter := scope
	wrongFilter.SelectorFilterHash = v3SyncDeterministicSelectorHash("different-selector")
	if _, _, err := server.parseV3SyncEndpointCursor(cursor, wrongFilter); err == nil || !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("wrong-filter parse err = %v, want scope mismatch", err)
	}

	payload := v3SyncCursorPayload{
		Version:            99,
		Kind:               v3SyncCursorKindEndpoint,
		Account:            scope.AccountScopeID,
		Principal:          scope.PrincipalUserID,
		Surface:            scope.Surface,
		StreamKind:         scope.StreamKind,
		SelectorFilterHash: scope.SelectorFilterHash,
		ResourceSet:        scope.ResourceSet,
		AfterEndpointSeq:   8,
		IssuedAt:           1,
		KID:                server.v3SyncCursorKeyring().CurrentKID,
	}
	unsupported, err := encodeV3SyncCursorPayload(payload, server.v3SyncCursorKeyring().CurrentKey)
	if err != nil {
		t.Fatalf("encode unsupported cursor: %v", err)
	}
	if _, _, err := server.parseV3SyncEndpointCursor(unsupported, scope); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported-version parse err = %v, want unsupported", err)
	}
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
	if _, _, err := retiredKeyServer.parseV3SyncEndpointCursor(upgraded, scope); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("retired-key parse err = %v, want unavailable", err)
	}
}

func testV3SyncCursorScope() v3SyncCursorScope {
	return v3SyncCursorScopeForRealtime(identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}, "desktop")
}

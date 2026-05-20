package identity

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestLocalProductSessionIssuesValidatesAndPersistsSigningKey(t *testing.T) {
	rawStore, identityStore := newSessionTestIdentityStore(t)
	bootstrapSessionIdentity(t, identityStore)

	sessions := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore))
	issued, err := sessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if !strings.HasPrefix(issued.Token, "ey") || issued.Actor.UserID != "user_session_test" || issued.Actor.AccountScopeID != "acct_session_test" {
		t.Fatalf("issued session = %+v token=%q", issued, issued.Token)
	}
	if issued.SessionID == "" || issued.JWTID == "" {
		t.Fatalf("issued session missing sid/jti: %+v", issued)
	}

	claims := mustVerifySessionClaims(t, rawStore, issued.Token)
	if claims.Issuer != LocalProductSessionIssuer || claims.Audience != LocalProductSessionAudience {
		t.Fatalf("issuer/audience = %q/%q", claims.Issuer, claims.Audience)
	}
	if claims.Subject != "user_session_test" || claims.AccountScopeID != "acct_session_test" || claims.TeamID != "" {
		t.Fatalf("subject/account_scope_id/team_id = %q/%q/%q", claims.Subject, claims.AccountScopeID, claims.TeamID)
	}
	if claims.SessionID != issued.SessionID || claims.JWTID != issued.JWTID {
		t.Fatalf("claims sid/jti = %q/%q issued = %q/%q", claims.SessionID, claims.JWTID, issued.SessionID, issued.JWTID)
	}
	if claims.IssuedAt == 0 || claims.NotBefore == 0 || claims.ExpiresAt == 0 {
		t.Fatalf("claims missing time safety fields: %+v", claims)
	}

	actor, err := sessions.Validate(issued.Token)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	if actor.UserID != "user_session_test" || actor.AccountScopeID != "acct_session_test" || actor.TeamID != "" {
		t.Fatalf("actor = %+v", actor)
	}

	restarted := NewSessionService(pebblestore.NewIdentityStore(rawStore), pebblestore.NewIdentitySessionStore(rawStore))
	if _, err := restarted.Validate(issued.Token); err != nil {
		t.Fatalf("validate session after service restart with persisted signing key: %v", err)
	}
}

func TestLocalProductSessionRejectsInvalidJWTClaims(t *testing.T) {
	fixedNow := time.Unix(1779199000, 0).UTC()
	rawStore, identityStore := newSessionTestIdentityStore(t)
	bootstrapSessionIdentity(t, identityStore)
	sessions := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore), WithSessionClock(func() time.Time { return fixedNow }))
	issued, err := sessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	validClaims := mustVerifySessionClaims(t, rawStore, issued.Token)

	parts := strings.Split(issued.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("issued token is not compact JWT: %q", issued.Token)
	}
	forged := parts[0] + "." + parts[1] + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := sessions.Validate(forged); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("forged token err=%v, want ErrInvalidProductSession", err)
	}

	expiredSvc := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore), WithSessionClock(func() time.Time {
		return fixedNow.Add(LocalProductSessionTTL + time.Second)
	}))
	if _, err := expiredSvc.Validate(issued.Token); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("expired token err=%v, want ErrInvalidProductSession", err)
	}

	tests := []struct {
		name   string
		mutate func(localProductClaims) localProductClaims
	}{
		{name: "missing subject", mutate: func(c localProductClaims) localProductClaims { c.Subject = ""; return c }},
		{name: "missing sid", mutate: func(c localProductClaims) localProductClaims { c.SessionID = ""; return c }},
		{name: "missing jti", mutate: func(c localProductClaims) localProductClaims { c.JWTID = ""; return c }},
		{name: "wrong issuer", mutate: func(c localProductClaims) localProductClaims { c.Issuer = "other"; return c }},
		{name: "wrong audience", mutate: func(c localProductClaims) localProductClaims { c.Audience = "other"; return c }},
		{name: "missing iat", mutate: func(c localProductClaims) localProductClaims { c.IssuedAt = 0; return c }},
		{name: "future iat", mutate: func(c localProductClaims) localProductClaims { c.IssuedAt = fixedNow.Add(time.Hour).Unix(); return c }},
		{name: "not yet valid", mutate: func(c localProductClaims) localProductClaims { c.NotBefore = fixedNow.Add(time.Hour).Unix(); return c }},
		{name: "missing exp", mutate: func(c localProductClaims) localProductClaims { c.ExpiresAt = 0; return c }},
		{name: "account as subject", mutate: func(c localProductClaims) localProductClaims { c.Subject = "acct_session_test"; return c }},
		{name: "missing account scope", mutate: func(c localProductClaims) localProductClaims { c.AccountScopeID = ""; return c }},
		{name: "stale account scope mismatch", mutate: func(c localProductClaims) localProductClaims { c.AccountScopeID = "acct_stale"; return c }},
		{name: "stale team mismatch", mutate: func(c localProductClaims) localProductClaims { c.TeamID = "team_stale"; return c }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token := mustSignSessionClaims(t, rawStore, tc.mutate(validClaims))
			if _, err := sessions.Validate(token); !errors.Is(err, ErrInvalidProductSession) {
				t.Fatalf("err=%v, want ErrInvalidProductSession", err)
			}
		})
	}

	wrongAlgToken := mustSignSessionClaimsWithHeader(t, rawStore, map[string]string{"alg": "none", "typ": "JWT"}, validClaims)
	if _, err := sessions.Validate(wrongAlgToken); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("wrong alg err=%v, want ErrInvalidProductSession", err)
	}
}

func TestLocalProductSessionRejectsMissingCanonicalIdentity(t *testing.T) {
	rawStore, identityStore := newSessionTestIdentityStore(t)
	bootstrapSessionIdentity(t, identityStore)
	sessions := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore))
	issued, err := sessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	if err := rawStore.Delete(pebblestore.KeyIdentityUser("user_session_test")); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := sessions.Validate(issued.Token); !errors.Is(err, ErrProductIdentityRequired) {
		t.Fatalf("missing user err=%v, want ErrProductIdentityRequired", err)
	}
}

func TestLocalProductSessionRejectsUserWithoutSelectedMembership(t *testing.T) {
	rawStore, identityStore := newSessionTestIdentityStore(t)
	bootstrapSessionIdentity(t, identityStore)
	sessions := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore))
	issued, err := sessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if err := rawStore.Delete(pebblestore.KeyAccountUser("acct_session_test", "user_session_test")); err != nil {
		t.Fatalf("delete account user: %v", err)
	}
	if _, err := sessions.Validate(issued.Token); !errors.Is(err, ErrProductIdentityRequired) {
		t.Fatalf("missing membership err=%v, want ErrProductIdentityRequired", err)
	}
}

func mustVerifySessionClaims(t *testing.T, rawStore *pebblestore.Store, token string) localProductClaims {
	t.Helper()
	key, _, err := pebblestore.NewIdentitySessionStore(rawStore).EnsureLocalProductJWTSigningKey()
	if err != nil {
		t.Fatalf("load signing key: %v", err)
	}
	claims, err := verifyLocalProductJWT(token, key)
	if err != nil {
		t.Fatalf("verify jwt: %v", err)
	}
	return claims
}

func mustSignSessionClaims(t *testing.T, rawStore *pebblestore.Store, claims localProductClaims) string {
	t.Helper()
	return mustSignSessionClaimsWithHeader(t, rawStore, map[string]string{"alg": "HS256", "typ": "JWT"}, claims)
}

func mustSignSessionClaimsWithHeader(t *testing.T, rawStore *pebblestore.Store, header map[string]string, claims localProductClaims) string {
	t.Helper()
	key, _, err := pebblestore.NewIdentitySessionStore(rawStore).EnsureLocalProductJWTSigningKey()
	if err != nil {
		t.Fatalf("load signing key: %v", err)
	}
	token, err := signLocalProductJWTWithHeader(header, claims, key)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return token
}

func newSessionTestIdentityStore(t *testing.T) (*pebblestore.Store, *pebblestore.IdentityStore) {
	t.Helper()
	rawStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "identity-session.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	return rawStore, pebblestore.NewIdentityStore(rawStore)
}

func bootstrapSessionIdentity(t *testing.T, identityStore *pebblestore.IdentityStore) {
	t.Helper()
	_, err := NewService(identityStore, WithIDGenerator(func(prefix string) (string, error) {
		switch prefix {
		case "user":
			return "user_session_test", nil
		case "acct":
			return "acct_session_test", nil
		default:
			return prefix + "_session_test", nil
		}
	})).BootstrapFirstIdentity("session-user")
	if err != nil {
		t.Fatalf("bootstrap identity: %v", err)
	}
}

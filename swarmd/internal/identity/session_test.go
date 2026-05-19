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
	if !strings.HasPrefix(issued.Token, "ey") || issued.Actor.UserID != "user_session_test" || issued.Actor.TeamID != "team_session_test" {
		t.Fatalf("issued session = %+v token=%q", issued, issued.Token)
	}

	actor, err := sessions.Validate(issued.Token)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	if actor.UserID != "user_session_test" || actor.TeamID != "team_session_test" {
		t.Fatalf("actor = %+v", actor)
	}

	restarted := NewSessionService(pebblestore.NewIdentityStore(rawStore), pebblestore.NewIdentitySessionStore(rawStore))
	if _, err := restarted.Validate(issued.Token); err != nil {
		t.Fatalf("validate session after service restart with persisted signing key: %v", err)
	}
}

func TestLocalProductSessionRejectsNegativeCases(t *testing.T) {
	fixedNow := time.Unix(1779199000, 0).UTC()
	rawStore, identityStore := newSessionTestIdentityStore(t)
	bootstrapSessionIdentity(t, identityStore)
	sessions := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore), WithSessionClock(func() time.Time { return fixedNow }))
	issued, err := sessions.IssueForCurrentSelection()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	forged := issued.Token[:len(issued.Token)-1] + "x"
	if _, err := sessions.Validate(forged); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("forged token err=%v, want ErrInvalidProductSession", err)
	}

	expiredSvc := NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(rawStore), WithSessionClock(func() time.Time {
		return fixedNow.Add(LocalProductSessionTTL + time.Second)
	}))
	if _, err := expiredSvc.Validate(issued.Token); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("expired token err=%v, want ErrInvalidProductSession", err)
	}

	teamOnlyClaims := localProductClaims{Issuer: LocalProductSessionIssuer, Subject: "team_session_test", Audience: LocalProductSessionAudience, IssuedAt: fixedNow.Unix(), NotBefore: fixedNow.Unix(), ExpiresAt: fixedNow.Add(time.Hour).Unix()}
	key, _, err := pebblestore.NewIdentitySessionStore(rawStore).EnsureLocalProductJWTSigningKey()
	if err != nil {
		t.Fatalf("load signing key: %v", err)
	}
	teamOnlyToken, err := signLocalProductJWT(teamOnlyClaims, key)
	if err != nil {
		t.Fatalf("sign team-only token: %v", err)
	}
	if _, err := sessions.Validate(teamOnlyToken); !errors.Is(err, ErrInvalidProductSession) {
		t.Fatalf("team-only token err=%v, want ErrInvalidProductSession", err)
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
	if err := rawStore.Delete(pebblestore.KeyIdentityTeamMembership("team_session_test", "user_session_test")); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	if _, err := sessions.Validate(issued.Token); !errors.Is(err, ErrProductIdentityRequired) {
		t.Fatalf("missing membership err=%v, want ErrProductIdentityRequired", err)
	}
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
		case "team":
			return "team_session_test", nil
		default:
			return prefix + "_session_test", nil
		}
	})).BootstrapFirstIdentity("session-user")
	if err != nil {
		t.Fatalf("bootstrap identity: %v", err)
	}
}

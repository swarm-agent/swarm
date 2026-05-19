package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	LocalProductSessionIssuer   = "swarm-desktop-local"
	LocalProductSessionAudience = "swarm-desktop"
	LocalProductSessionTTL      = 180 * 24 * time.Hour
)

var (
	ErrSessionServiceNotConfigured = errors.New("identity session service is not configured")
	ErrProductIdentityRequired     = errors.New("product identity has not been bootstrapped")
	ErrInvalidProductSession       = errors.New("invalid product session")
)

type SessionService struct {
	identityStore *pebblestore.IdentityStore
	sessionStore  *pebblestore.IdentitySessionStore
	now           func() time.Time
}

type SessionOption func(*SessionService)

func WithSessionClock(now func() time.Time) SessionOption {
	return func(s *SessionService) {
		s.now = now
	}
}

type ActorContext struct {
	UserID       string                             `json:"user_id"`
	TeamID       string                             `json:"team_id"`
	User         pebblestore.UserRecord             `json:"user"`
	Team         pebblestore.TeamRecord             `json:"team"`
	Membership   pebblestore.TeamMembershipRecord   `json:"membership"`
	Selection    pebblestore.CurrentSelectionRecord `json:"selection"`
	TokenExpires time.Time                          `json:"token_expires_at,omitempty"`
}

type IssuedSession struct {
	Token     string       `json:"token"`
	IssuedAt  time.Time    `json:"issued_at"`
	ExpiresAt time.Time    `json:"expires_at"`
	Actor     ActorContext `json:"actor"`
}

type localProductClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	ExpiresAt int64  `json:"exp"`
}

func NewSessionService(identityStore *pebblestore.IdentityStore, sessionStore *pebblestore.IdentitySessionStore, opts ...SessionOption) *SessionService {
	svc := &SessionService{identityStore: identityStore, sessionStore: sessionStore, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

func (s *SessionService) IssueForCurrentSelection() (IssuedSession, error) {
	if err := s.configured(); err != nil {
		return IssuedSession{}, err
	}
	now := s.currentTime()
	actor, err := s.actorForCurrentSelection()
	if err != nil {
		return IssuedSession{}, err
	}
	claims := localProductClaims{
		Issuer:    LocalProductSessionIssuer,
		Subject:   actor.UserID,
		Audience:  LocalProductSessionAudience,
		IssuedAt:  now.Unix(),
		NotBefore: now.Add(-1 * time.Minute).Unix(),
		ExpiresAt: now.Add(LocalProductSessionTTL).Unix(),
	}
	key, _, err := s.sessionStore.EnsureLocalProductJWTSigningKey()
	if err != nil {
		return IssuedSession{}, err
	}
	token, err := signLocalProductJWT(claims, key)
	if err != nil {
		return IssuedSession{}, err
	}
	actor.TokenExpires = time.Unix(claims.ExpiresAt, 0).UTC()
	return IssuedSession{Token: token, IssuedAt: now, ExpiresAt: actor.TokenExpires, Actor: actor}, nil
}

func (s *SessionService) Validate(token string) (ActorContext, error) {
	if err := s.configured(); err != nil {
		return ActorContext{}, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ActorContext{}, ErrInvalidProductSession
	}
	key, _, err := s.sessionStore.EnsureLocalProductJWTSigningKey()
	if err != nil {
		return ActorContext{}, err
	}
	claims, err := verifyLocalProductJWT(token, key)
	if err != nil {
		return ActorContext{}, err
	}
	now := s.currentTime().Unix()
	if claims.Issuer != LocalProductSessionIssuer || claims.Audience != LocalProductSessionAudience {
		return ActorContext{}, ErrInvalidProductSession
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return ActorContext{}, ErrInvalidProductSession
	}
	if claims.NotBefore != 0 && now < claims.NotBefore {
		return ActorContext{}, ErrInvalidProductSession
	}
	if claims.ExpiresAt == 0 || now >= claims.ExpiresAt {
		return ActorContext{}, ErrInvalidProductSession
	}
	actor, err := s.actorForUserID(claims.Subject)
	if err != nil {
		return ActorContext{}, err
	}
	actor.TokenExpires = time.Unix(claims.ExpiresAt, 0).UTC()
	return actor, nil
}

func (s *SessionService) actorForCurrentSelection() (ActorContext, error) {
	selection, ok, err := s.identityStore.GetCurrentSelection()
	if err != nil {
		return ActorContext{}, err
	}
	if !ok || strings.TrimSpace(selection.UserID) == "" || strings.TrimSpace(selection.TeamID) == "" {
		return ActorContext{}, ErrProductIdentityRequired
	}
	return s.actorForSelection(selection)
}

func (s *SessionService) actorForUserID(userID string) (ActorContext, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ActorContext{}, ErrInvalidProductSession
	}
	selection, ok, err := s.identityStore.GetCurrentSelection()
	if err != nil {
		return ActorContext{}, err
	}
	if !ok || strings.TrimSpace(selection.UserID) == "" || strings.TrimSpace(selection.TeamID) == "" {
		return ActorContext{}, ErrProductIdentityRequired
	}
	if selection.UserID != userID {
		return ActorContext{}, ErrInvalidProductSession
	}
	return s.actorForSelection(selection)
}

func (s *SessionService) actorForSelection(selection pebblestore.CurrentSelectionRecord) (ActorContext, error) {
	user, ok, err := s.identityStore.GetUser(selection.UserID)
	if err != nil {
		return ActorContext{}, err
	}
	if !ok {
		return ActorContext{}, ErrProductIdentityRequired
	}
	team, ok, err := s.identityStore.GetTeam(selection.TeamID)
	if err != nil {
		return ActorContext{}, err
	}
	if !ok {
		return ActorContext{}, ErrProductIdentityRequired
	}
	membership, ok, err := s.identityStore.GetTeamMembership(selection.TeamID, selection.UserID)
	if err != nil {
		return ActorContext{}, err
	}
	if !ok {
		return ActorContext{}, ErrProductIdentityRequired
	}
	return ActorContext{UserID: user.ID, TeamID: team.ID, User: user, Team: team, Membership: membership, Selection: selection}, nil
}

func (s *SessionService) configured() error {
	if s == nil || s.identityStore == nil || s.sessionStore == nil {
		return ErrSessionServiceNotConfigured
	}
	return nil
}

func (s *SessionService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func signLocalProductJWT(claims localProductClaims, key []byte) (string, error) {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerPayload, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsPayload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerPayload) + "." + base64.RawURLEncoding.EncodeToString(claimsPayload)
	signature := signLocalProductJWTBytes(unsigned, key)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func verifyLocalProductJWT(token string, key []byte) (localProductClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return localProductClaims{}, ErrInvalidProductSession
	}
	unsigned := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return localProductClaims{}, ErrInvalidProductSession
	}
	expected := signLocalProductJWTBytes(unsigned, key)
	if !hmac.Equal(sig, expected) {
		return localProductClaims{}, ErrInvalidProductSession
	}
	headerPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return localProductClaims{}, ErrInvalidProductSession
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerPayload, &header); err != nil {
		return localProductClaims{}, ErrInvalidProductSession
	}
	if header.Alg != "HS256" || header.Typ != "JWT" {
		return localProductClaims{}, ErrInvalidProductSession
	}
	claimsPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return localProductClaims{}, ErrInvalidProductSession
	}
	var claims localProductClaims
	if err := json.Unmarshal(claimsPayload, &claims); err != nil {
		return localProductClaims{}, ErrInvalidProductSession
	}
	return claims, nil
}

func signLocalProductJWTBytes(unsigned string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(unsigned))
	return mac.Sum(nil)
}

func (c localProductClaims) String() string {
	return fmt.Sprintf("local product session sub=%q aud=%q exp=%d", c.Subject, c.Audience, c.ExpiresAt)
}

package api

import (
	"context"
	"fmt"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type scopedTokenContextKey string

const (
	productScopedTokenRequestContextKey scopedTokenContextKey = "product-scoped-token"
)

func requestWithScopedToken(r *http.Request, record *pebblestore.ScopedTokenRecord) *http.Request {
	if r == nil || record == nil {
		return r
	}
	ctx := context.WithValue(r.Context(), productScopedTokenRequestContextKey, record)
	return r.WithContext(ctx)
}

func ScopedTokenFromRequest(r *http.Request) (*pebblestore.ScopedTokenRecord, bool) {
	if r == nil {
		return nil, false
	}
	rec, ok := r.Context().Value(productScopedTokenRequestContextKey).(*pebblestore.ScopedTokenRecord)
	return rec, ok && rec != nil
}

func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, requiredScope string) bool {
	if scopedRec, ok := ScopedTokenFromRequest(r); ok {
		if !scopedRec.HasScope(requiredScope) {
			writeError(w, http.StatusForbidden, fmt.Errorf("token lacks required scope %q", requiredScope))
			return false
		}
	}
	return true
}

func (s *Server) resolveActorForScopedToken(rec *pebblestore.ScopedTokenRecord) (identity.ActorContext, error) {
	if s == nil || s.identitySessions == nil {
		return identity.ActorContext{}, identity.ErrSessionServiceNotConfigured
	}
	if rec != nil && rec.UserID != "" {
		if actor, err := s.identitySessions.ActorForUserID(rec.UserID); err == nil && isCompleteProductActor(actor) {
			return actor, nil
		}
	}
	if actor, err := s.identitySessions.ActorForCurrentSelection(); err == nil && isCompleteProductActor(actor) {
		return actor, nil
	}
	return identity.ActorContext{}, identity.ErrProductIdentityRequired
}

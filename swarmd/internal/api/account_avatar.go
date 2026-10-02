package api

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

func (s *Server) handleAccountAvatar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	actor, ok := s.requireProductActor(w, r)
	if !ok {
		return
	}
	// Bind requests to the identity rendered by the caller, including in-flight
	// uploads during account switches. These parameters never select an owner.
	if r.URL.Query().Get("user_id") != actor.UserID || r.URL.Query().Get("account_scope_id") != actor.AccountScopeID {
		writeError(w, http.StatusForbidden, identity.ErrAvatarUnauthorized)
		return
	}
	var data []byte
	var err error
	if r.Method == http.MethodPut {
		contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || contentType != "image/png" {
			writeError(w, http.StatusUnsupportedMediaType, identity.ErrInvalidAvatar)
			return
		}
		data, err = io.ReadAll(http.MaxBytesReader(w, r.Body, identity.MaxAvatarBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, identity.ErrInvalidAvatar)
			return
		}
		data, err = s.identityService.SaveCurrentUserAvatar(actor, data)
	} else {
		data, err = s.identityService.CurrentUserAvatar(actor)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, identity.ErrAvatarUnauthorized) {
			status = http.StatusForbidden
		} else if errors.Is(err, identity.ErrInvalidAvatar) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	image := ""
	if len(data) > 0 {
		image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}
	writeJSON(w, http.StatusOK, map[string]string{"image": image, "user_id": actor.UserID, "account_scope_id": actor.AccountScopeID})
}

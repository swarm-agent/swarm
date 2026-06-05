package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

func (s *Server) handleV3RealtimeStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	conn, err := transportws.Accept(w, r)
	if err != nil {
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()

	if raw, err := json.Marshal(NewV3RealtimeMessage(V3RealtimeKindKeepalive)); err == nil {
		_ = conn.WriteText(raw)
	}
}

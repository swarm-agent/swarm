package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	deployruntime "swarm/packages/swarmd/internal/deploy"
)

func (s *Server) handleDeployContainerPairingAccountBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.deployContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container service not configured"))
		return
	}
	bindSvc, ok := s.deployContainers.(interface {
		BindLocalPairingAccount(context.Context, deployruntime.ContainerPairingAccountBindInput) error
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("deploy container pairing account bind not configured"))
		return
	}
	peerSwarmID, peerAuthorized := authorizedPeerSwarmID(r)
	if !peerAuthorized {
		writeError(w, http.StatusUnauthorized, errors.New("peer auth is required"))
		return
	}
	var req deployruntime.ContainerPairingAccountBindInput
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.HostSwarmID) == "" {
		req.HostSwarmID = strings.TrimSpace(peerSwarmID)
	}
	if !strings.EqualFold(strings.TrimSpace(req.HostSwarmID), strings.TrimSpace(peerSwarmID)) {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "path_id": deployruntime.PathContainerPairingAccountBind, "error": "authenticated peer swarm id does not match host swarm id"})
		return
	}
	if err := bindSvc.BindLocalPairingAccount(context.Background(), req); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "path_id": deployruntime.PathContainerPairingAccountBind, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path_id": deployruntime.PathContainerPairingAccountBind})
}

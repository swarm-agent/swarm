package api

import (
	"errors"
	"net/http"
	"strings"
)

const managedHostUpdateRunPath = "/v1/swarm/managed-hosts/update/run"

type managedHostUpdateRunRequest struct {
	TargetSwarmID string `json:"target_swarm_id,omitempty"`
}

type managedHostUpdateRunResponse struct {
	OK     bool         `json:"ok"`
	Target *swarmTarget `json:"target,omitempty"`
	Error  string       `json:"error,omitempty"`
}

func (s *Server) handleManagedHostUpdateRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req managedHostUpdateRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.TargetSwarmID) == "" {
		req.TargetSwarmID = strings.TrimSpace(r.URL.Query().Get("swarm_id"))
	}
	resp, status, err := s.runManagedHostUpdate(r, req)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) runManagedHostUpdate(r *http.Request, req managedHostUpdateRunRequest) (managedHostUpdateRunResponse, int, error) {
	if s == nil {
		return managedHostUpdateRunResponse{}, http.StatusInternalServerError, errors.New("server is not configured")
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		return managedHostUpdateRunResponse{}, http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetSwarmID), targetSwarmID)
	if err != nil {
		return managedHostUpdateRunResponse{}, status, err
	}
	var peerResp managedHostPeerUpdateRunResponse
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerUpdateRunPath, map[string]any{}, &peerResp); err != nil {
		return managedHostUpdateRunResponse{Target: target}, http.StatusBadGateway, err
	}
	if !peerResp.OK {
		return managedHostUpdateRunResponse{Target: target}, http.StatusBadGateway, errors.New(firstNonEmpty(peerResp.Error, "managed host update run failed"))
	}
	return managedHostUpdateRunResponse{OK: true, Target: target}, http.StatusAccepted, nil
}

type managedHostPeerUpdateRunResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handlePeerUpdateRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.update == nil {
		writeJSON(w, http.StatusInternalServerError, managedHostPeerUpdateRunResponse{OK: false, Error: "update service is not configured"})
		return
	}
	job, err := defaultUpdateJobRunner.Start(r.Context(), s)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, managedHostPeerUpdateRunResponse{OK: false, Error: err.Error()})
		return
	}
	_ = job
	writeJSON(w, http.StatusAccepted, managedHostPeerUpdateRunResponse{OK: true})
}

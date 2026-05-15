package api

import (
	"errors"
	"net/http"
	"strings"
)

const (
	managedHostUpdateRunPath    = "/v1/swarm/managed-hosts/update/run"
	managedHostUpdateStatusPath = "/v1/swarm/managed-hosts/update/status"
)

type managedHostUpdateRunRequest struct {
	TargetSwarmID string `json:"target_swarm_id,omitempty"`
}

type managedHostUpdateRunResponse struct {
	OK     bool                        `json:"ok"`
	Target *swarmTarget                `json:"target,omitempty"`
	Job    *managedHostUpdateJobStatus `json:"job,omitempty"`
	Error  string                      `json:"error,omitempty"`
}

type managedHostUpdateJobStatus struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	HelperPID int    `json:"helper_pid,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
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

func (s *Server) handleManagedHostUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	var req managedHostUpdateRunRequest
	if r.Method == http.MethodPost && r.Body != nil {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if strings.TrimSpace(req.TargetSwarmID) == "" {
		req.TargetSwarmID = strings.TrimSpace(r.URL.Query().Get("swarm_id"))
	}
	resp, status, err := s.managedHostUpdateStatus(r, req)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) runManagedHostUpdate(r *http.Request, req managedHostUpdateRunRequest) (managedHostUpdateRunResponse, int, error) {
	target, status, err := s.resolveManagedHostUpdateTarget(r, req)
	if err != nil {
		return managedHostUpdateRunResponse{}, status, err
	}
	var peerResp managedHostPeerUpdateRunResponse
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerUpdateRunPath, map[string]any{}, &peerResp); err != nil {
		return managedHostUpdateRunResponse{Target: target}, http.StatusBadGateway, err
	}
	if !peerResp.OK {
		return managedHostUpdateRunResponse{Target: target, Job: peerResp.Job}, http.StatusBadGateway, errors.New(firstNonEmpty(peerResp.Error, "managed host update run failed"))
	}
	return managedHostUpdateRunResponse{OK: true, Target: target, Job: peerResp.Job}, http.StatusAccepted, nil
}

func (s *Server) managedHostUpdateStatus(r *http.Request, req managedHostUpdateRunRequest) (managedHostUpdateRunResponse, int, error) {
	target, status, err := s.resolveManagedHostUpdateTarget(r, req)
	if err != nil {
		return managedHostUpdateRunResponse{}, status, err
	}
	var peerResp managedHostPeerUpdateRunResponse
	if err := s.postPeerJSONToSwarmTarget(r.Context(), *target, peerUpdateStatusPath, map[string]any{}, &peerResp); err != nil {
		return managedHostUpdateRunResponse{Target: target}, http.StatusBadGateway, err
	}
	if !peerResp.OK {
		return managedHostUpdateRunResponse{Target: target, Job: peerResp.Job}, http.StatusBadGateway, errors.New(firstNonEmpty(peerResp.Error, "managed host update status failed"))
	}
	return managedHostUpdateRunResponse{OK: true, Target: target, Job: peerResp.Job}, http.StatusOK, nil
}

func (s *Server) resolveManagedHostUpdateTarget(r *http.Request, req managedHostUpdateRunRequest) (*swarmTarget, int, error) {
	if s == nil {
		return nil, http.StatusInternalServerError, errors.New("server is not configured")
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		return nil, http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	target, _, _, status, err := s.resolveManagedHostSessionTarget(requestWithSwarmTargetQuery(r, targetSwarmID), targetSwarmID)
	if err != nil {
		return nil, status, err
	}
	return target, http.StatusOK, nil
}

type managedHostPeerUpdateRunResponse struct {
	OK    bool                        `json:"ok"`
	Job   *managedHostUpdateJobStatus `json:"job,omitempty"`
	Error string                      `json:"error,omitempty"`
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
		writeJSON(w, http.StatusBadRequest, managedHostPeerUpdateRunResponse{OK: false, Job: summarizeManagedHostUpdateJob(job), Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, managedHostPeerUpdateRunResponse{OK: true, Job: summarizeManagedHostUpdateJob(job)})
}

func (s *Server) handlePeerUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.update == nil {
		writeJSON(w, http.StatusInternalServerError, managedHostPeerUpdateRunResponse{OK: false, Error: "update service is not configured"})
		return
	}
	writeJSON(w, http.StatusOK, managedHostPeerUpdateRunResponse{OK: true, Job: summarizeManagedHostUpdateJob(defaultUpdateJobRunner.Status(s))})
}

func summarizeManagedHostUpdateJob(job desktopUpdateJob) *managedHostUpdateJobStatus {
	if strings.TrimSpace(job.ID) == "" && strings.TrimSpace(job.Status) == "" {
		return nil
	}
	return &managedHostUpdateJobStatus{
		ID:        strings.TrimSpace(job.ID),
		Kind:      strings.TrimSpace(job.Kind),
		Status:    strings.TrimSpace(job.Status),
		Message:   strings.TrimSpace(job.Message),
		Error:     strings.TrimSpace(job.Error),
		HelperPID: job.HelperPID,
		LogPath:   strings.TrimSpace(job.LogPath),
	}
}

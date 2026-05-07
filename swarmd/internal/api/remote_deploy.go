package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
)

const remoteDeploySessionRefreshTimeout = 30 * time.Second

const remoteDeployRetiredError = "SSH remote deploy is retired; use Add Remote Swarm pairing instead."

func writeRemoteDeployRetired(w http.ResponseWriter, pathID string) {
	writeJSON(w, http.StatusGone, map[string]any{
		"ok":      false,
		"path_id": pathID,
		"error":   remoteDeployRetiredError,
		"code":    "410",
	})
}

func (s *Server) handleRemoteDeploySessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	refresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true")
	sessionID := strings.TrimSpace(r.URL.Query().Get("id"))
	if sessionID != "" {
		ctx := r.Context()
		var cancel context.CancelFunc
		if refresh {
			ctx, cancel = context.WithTimeout(r.Context(), remoteDeploySessionRefreshTimeout)
			defer cancel()
		}
		session, err := s.remoteDeploys.Get(ctx, sessionID, refresh)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"path_id":  remotedeploy.PathSessionList,
			"session":  session,
			"sessions": []remotedeploy.Session{session},
		})
		return
	}
	var (
		items []remotedeploy.Session
		err   error
	)
	if refresh {
		ctx, cancel := context.WithTimeout(r.Context(), remoteDeploySessionRefreshTimeout)
		defer cancel()
		items, err = s.remoteDeploys.List(ctx)
	} else {
		items, err = s.remoteDeploys.ListCached(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"path_id":  remotedeploy.PathSessionList,
		"sessions": items,
	})
}

func (s *Server) handleRemoteDeploySessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	writeRemoteDeployRetired(w, remotedeploy.PathSessionCreate)
}

func (s *Server) handleRemoteDeploySessionSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		ID        string `json:"id,omitempty"`
		AlwaysOn  *bool  `json:"always_on,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.ID)
	}
	session, err := s.remoteDeploys.UpdateSettings(r.Context(), remotedeploy.UpdateSettingsInput{
		SessionID: sessionID,
		AlwaysOn:  req.AlwaysOn,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": "deploy.remote.settings.v1",
			"session": session,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": "deploy.remote.settings.v1",
		"session": session,
	})
}

func (s *Server) handleRemoteDeploySessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		IDs            []string `json:"ids"`
		ChildSwarmIDs  []string `json:"child_swarm_ids,omitempty"`
		TeardownRemote bool     `json:"teardown_remote,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.remoteDeploys.Delete(context.Background(), remotedeploy.DeleteSessionInput{
		SessionIDs:     req.IDs,
		ChildSwarmIDs:  req.ChildSwarmIDs,
		TeardownRemote: req.TeardownRemote,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": remotedeploy.PathSessionDelete,
			"result":  result,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": remotedeploy.PathSessionDelete,
		"result":  result,
	})
}

func (s *Server) handleRemoteDeploySessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	writeRemoteDeployRetired(w, remotedeploy.PathSessionStart)
}

func (s *Server) handleRemoteDeploySessionUpdateJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		DevMode          *bool `json:"dev_mode,omitempty"`
		PostRebuildCheck bool  `json:"post_rebuild_check,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.remoteDeploys.RunUpdateJob(r.Context(), remotedeploy.UpdateJobInput{
		DevMode:          req.DevMode,
		PostRebuildCheck: req.PostRebuildCheck,
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":      false,
			"path_id": remotedeploy.PathSessionUpdateJob,
			"result":  result,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": remotedeploy.PathSessionUpdateJob,
		"result":  result,
	})
}

func (s *Server) handleRemoteDeploySessionSyncCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		SessionID     string `json:"session_id"`
		SessionToken  string `json:"session_token"`
		VaultPassword string `json:"vault_password,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bundle, err := s.remoteDeploys.SyncCredentialBundle(context.Background(), remotedeploy.SyncCredentialRequestInput{
		SessionID:     req.SessionID,
		SessionToken:  req.SessionToken,
		VaultPassword: req.VaultPassword,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "unlock") {
			status = http.StatusLocked
		}
		writeJSON(w, status, map[string]any{
			"ok":      false,
			"path_id": "deploy.remote.sync.credentials.v1",
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": "deploy.remote.sync.credentials.v1",
		"bundle":  bundle,
	})
}

func (s *Server) handleRemoteDeploySessionApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), "/v1/deploy/remote/session/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) != "approve" {
		writeError(w, http.StatusBadRequest, errors.New("expected /v1/deploy/remote/session/{id}/approve"))
		return
	}
	startedAt := time.Now()
	session, err := s.remoteDeploys.Approve(context.Background(), remotedeploy.ApproveSessionInput{SessionID: parts[0]})
	if err != nil {
		log.Printf("remote deploy approve failed session_id=%q elapsed_ms=%d err=%v", strings.TrimSpace(parts[0]), time.Since(startedAt).Milliseconds(), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": remotedeploy.PathSessionApprove,
			"session": session,
			"error":   err.Error(),
		})
		return
	}
	log.Printf("remote deploy approve success session_id=%q status=%q elapsed_ms=%d", session.ID, session.Status, time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": remotedeploy.PathSessionApprove,
		"session": session,
	})
}

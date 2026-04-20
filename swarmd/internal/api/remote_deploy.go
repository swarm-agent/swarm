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
	var (
		items []remotedeploy.Session
		err   error
	)
	if refresh {
		items, err = s.remoteDeploys.List(context.Background())
	} else {
		items, err = s.remoteDeploys.ListCached(context.Background())
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
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		Name                string `json:"name"`
		SSHSessionTarget    string `json:"ssh_session_target"`
		TransportMode       string `json:"transport_mode,omitempty"`
		RemoteAdvertiseHost string `json:"remote_advertise_host,omitempty"`
		GroupID             string `json:"group_id"`
		GroupName           string `json:"group_name"`
		RemoteRuntime       string `json:"remote_runtime"`
		ImageDeliveryMode   string `json:"image_delivery_mode,omitempty"`
		SyncEnabled         bool   `json:"sync_enabled"`
		BypassPermissions   bool   `json:"bypass_permissions,omitempty"`
		ContainerPackages   struct {
			BaseImage      string `json:"base_image,omitempty"`
			PackageManager string `json:"package_manager,omitempty"`
			Packages       []struct {
				Name   string `json:"name"`
				Source string `json:"source,omitempty"`
				Reason string `json:"reason,omitempty"`
			} `json:"packages,omitempty"`
		} `json:"container_packages,omitempty"`
		Payloads []struct {
			SourcePath    string `json:"source_path"`
			WorkspacePath string `json:"workspace_path"`
			WorkspaceName string `json:"workspace_name"`
			TargetPath    string `json:"target_path"`
			Mode          string `json:"mode"`
			Directories   []struct {
				SourcePath string `json:"source_path"`
				TargetPath string `json:"target_path"`
			} `json:"directories,omitempty"`
		} `json:"payloads"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	startedAt := time.Now()
	payloads := make([]remotedeploy.PayloadSelection, 0, len(req.Payloads))
	for _, payload := range req.Payloads {
		directories := make([]remotedeploy.PayloadDirectorySelection, 0, len(payload.Directories))
		for _, directory := range payload.Directories {
			directories = append(directories, remotedeploy.PayloadDirectorySelection{
				SourcePath: directory.SourcePath,
				TargetPath: directory.TargetPath,
			})
		}
		payloads = append(payloads, remotedeploy.PayloadSelection{
			SourcePath:    payload.SourcePath,
			WorkspacePath: payload.WorkspacePath,
			WorkspaceName: payload.WorkspaceName,
			TargetPath:    payload.TargetPath,
			Mode:          payload.Mode,
			Directories:   directories,
		})
	}
	packages := make([]remotedeploy.ContainerPackageSelection, 0, len(req.ContainerPackages.Packages))
	for _, pkg := range req.ContainerPackages.Packages {
		packages = append(packages, remotedeploy.ContainerPackageSelection{
			Name:   pkg.Name,
			Source: pkg.Source,
			Reason: pkg.Reason,
		})
	}
	session, err := s.remoteDeploys.Create(context.Background(), remotedeploy.CreateSessionInput{
		Name:                req.Name,
		SSHSessionTarget:    req.SSHSessionTarget,
		TransportMode:       req.TransportMode,
		RemoteAdvertiseHost: req.RemoteAdvertiseHost,
		GroupID:             req.GroupID,
		GroupName:           req.GroupName,
		RemoteRuntime:       req.RemoteRuntime,
		ImageDeliveryMode:   req.ImageDeliveryMode,
		SyncEnabled:         req.SyncEnabled,
		BypassPermissions:   req.BypassPermissions,
		ContainerPackages: remotedeploy.ContainerPackageManifest{
			BaseImage:      req.ContainerPackages.BaseImage,
			PackageManager: req.ContainerPackages.PackageManager,
			Packages:       packages,
		},
		Payloads: payloads,
	})
	if err != nil {
		log.Printf("remote deploy create failed name=%q ssh_target=%q payloads=%d sync_enabled=%t elapsed_ms=%d err=%v", strings.TrimSpace(req.Name), strings.TrimSpace(req.SSHSessionTarget), len(req.Payloads), req.SyncEnabled, time.Since(startedAt).Milliseconds(), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": remotedeploy.PathSessionCreate,
			"session": session,
			"error":   err.Error(),
		})
		return
	}
	log.Printf("remote deploy create success session_id=%q ssh_target=%q payloads=%d sync_enabled=%t elapsed_ms=%d", session.ID, strings.TrimSpace(req.SSHSessionTarget), len(req.Payloads), req.SyncEnabled, time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": remotedeploy.PathSessionCreate,
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
	if s.remoteDeploys == nil {
		writeError(w, http.StatusInternalServerError, errors.New("remote deploy service not configured"))
		return
	}
	var req struct {
		SessionID         string `json:"session_id"`
		TailscaleAuthKey  string `json:"tailscale_auth_key"`
		SyncVaultPassword string `json:"sync_vault_password,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	startedAt := time.Now()
	session, err := s.remoteDeploys.Start(context.Background(), remotedeploy.StartSessionInput{
		SessionID:         req.SessionID,
		TailscaleAuthKey:  req.TailscaleAuthKey,
		SyncVaultPassword: req.SyncVaultPassword,
	})
	if err != nil {
		log.Printf("remote deploy start failed session_id=%q elapsed_ms=%d err=%v", strings.TrimSpace(req.SessionID), time.Since(startedAt).Milliseconds(), err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"path_id": remotedeploy.PathSessionStart,
			"session": session,
			"error":   err.Error(),
		})
		return
	}
	log.Printf("remote deploy start success session_id=%q status=%q elapsed_ms=%d", session.ID, session.Status, time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": remotedeploy.PathSessionStart,
		"session": session,
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

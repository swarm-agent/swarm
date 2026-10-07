package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/remote"
)

// RemoteTransportService manages the optional outbound relay connection.
type RemoteTransportService interface {
	Status() (remote.Status, error)
	Init(remote.InitInput) (remote.Status, error)
	SetEnabled(bool) (remote.Status, error)
	Reset() (remote.Status, error)
	DecideConsent(code string, approve bool, scopes []string) (remote.Consent, error)
}

func (s *Server) SetRemoteTransportService(service RemoteTransportService) {
	s.remoteTransport = service
}

// handleRemoteTransport is owner administration. Scoped tokens are refused so
// a remote client can never reconfigure, widen or approve its own access.
func (s *Server) handleRemoteTransport(w http.ResponseWriter, r *http.Request) {
	if s.remoteTransport == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("remote transport is not available"))
		return
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped {
		writeError(w, http.StatusForbidden, errors.New("remote transport administration requires the machine owner"))
		return
	}
	if principal, ok := PrincipalFromRequest(r); !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("owner identity required"))
		return
	}
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/remote"), "/")
	if action == "" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		status, err := s.remoteTransport.Status()
		writeRemoteTransportResult(w, status, err)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		RelayURL     string   `json:"relay_url"`
		DeviceName   string   `json:"device_name"`
		AllowWrite   bool     `json:"allow_write"`
		AllowApprove bool     `json:"allow_approve"`
		AllowManage  bool     `json:"allow_manage"`
		Code         string   `json:"code"`
		Scopes       []string `json:"scopes"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
			return
		}
	}
	switch action {
	case "init":
		status, err := s.remoteTransport.Init(remote.InitInput{RelayURL: body.RelayURL, DeviceName: body.DeviceName, AllowWrite: body.AllowWrite, AllowApprove: body.AllowApprove, AllowManage: body.AllowManage})
		writeRemoteTransportResult(w, status, err)
	case "enable", "disable":
		status, err := s.remoteTransport.SetEnabled(action == "enable")
		writeRemoteTransportResult(w, status, err)
	case "reset":
		status, err := s.remoteTransport.Reset()
		writeRemoteTransportResult(w, status, err)
	case "consents/approve", "consents/deny":
		consent, err := s.remoteTransport.DecideConsent(body.Code, action == "consents/approve", body.Scopes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "consent": consent, "approved": action == "consents/approve"})
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown remote transport action"))
	}
}

func writeRemoteTransportResult(w http.ResponseWriter, status remote.Status, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "remote": status})
}

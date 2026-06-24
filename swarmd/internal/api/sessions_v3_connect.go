package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sessionconnection"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionConnectionStreamPrefix = "/v3/session-connections/"

func (s *Server) sessionConnectionService() (*sessionconnection.Service, error) {
	if s == nil {
		return nil, errors.New("server is not configured")
	}
	if s.sessionConnections == nil {
		svc, err := sessionconnection.NewService(sessionconnection.Options{DataDir: s.dataDir})
		if err != nil {
			return nil, err
		}
		s.sessionConnections = svc
	}
	return s.sessionConnections, nil
}

func (s *Server) handleSessionV3Connect(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req SessionConnectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeSessionConnectionError(w, http.StatusBadRequest, "invalid_request", err.Error(), false, sessionID)
		return
	}
	svc, err := s.sessionConnectionService()
	if err != nil {
		writeSessionConnectionError(w, http.StatusInternalServerError, "service_unavailable", err.Error(), true, sessionID)
		return
	}
	resumeToken := ""
	if req.ResumeToken != nil {
		resumeToken = strings.TrimSpace(*req.ResumeToken)
	}
	pending := func(sessionID string, limit int) ([]pebblestore.PermissionRecord, error) {
		if s.perm == nil {
			return nil, nil
		}
		return s.perm.ListPending(sessionID, limit)
	}
	result, err := svc.Connect(sessionconnection.ConnectInput{
		Principal:   principal,
		SessionID:   sessionID,
		ClientID:    req.ClientId,
		RequestID:   req.RequestId,
		ResumeToken: resumeToken,
		Store:       s.sessions,
		Pending:     pending,
		StreamPath: func(connectionID, token string) string {
			return sessionConnectionStreamPath(connectionID, token)
		},
	})
	if err != nil {
		writeSessionConnectionConnectError(w, sessionID, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionConnectResponseFromResult(result))
}

func sessionConnectionStreamPath(connectionID, token string) string {
	return sessionConnectionStreamPrefix + url.PathEscape(strings.TrimSpace(connectionID)) + "/stream?token=" + url.QueryEscape(strings.TrimSpace(token))
}

func writeSessionConnectionConnectError(w http.ResponseWriter, sessionID string, err error) {
	status := http.StatusBadRequest
	code := "connect_failed"
	retryable := false
	if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
		code = "session_not_found"
	} else if errors.Is(err, sessionconnection.ErrUnauthorized) {
		status = http.StatusForbidden
		code = "authorization_failed"
	} else if strings.Contains(err.Error(), "expired") {
		status = http.StatusUnauthorized
		code = "resume_token_expired"
		retryable = true
	} else if strings.Contains(err.Error(), "session store") || strings.Contains(err.Error(), "service") {
		status = http.StatusInternalServerError
		code = "service_unavailable"
		retryable = true
	}
	writeSessionConnectionError(w, status, code, err.Error(), retryable, sessionID)
}

func writeSessionConnectionError(w http.ResponseWriter, status int, code, message string, retryable bool, sessionID string) {
	writeJSON(w, status, SessionConnectionError{Code: code, Message: message, Retryable: retryable, Action: SessionErrorAction{Method: http.MethodPost, Path: fmt.Sprintf("/v3/sessions/%s:connect", url.PathEscape(strings.TrimSpace(sessionID)))}})
}

func sessionConnectResponseFromResult(result sessionconnection.ConnectResult) SessionConnectResponse {
	return SessionConnectResponse{
		Ok:              true,
		ContractVersion: SessionConnectionContractVersion,
		SessionId:       result.SessionID,
		Snapshot: SessionSnapshot{
			EventSeq:           result.Snapshot.EventSeq,
			Session:            result.Snapshot.Session,
			Messages:           result.Snapshot.Messages,
			CurrentRun:         sessionCurrentRunFromConnection(result.Snapshot.CurrentRun),
			PendingPermissions: result.Snapshot.PendingPermissions,
			ActivePlan:         result.Snapshot.ActivePlan,
			Usage:              result.Snapshot.Usage,
		},
		Connection: SessionConnectionInfo{
			ConnectionId:   result.Connection.ConnectionID,
			Transport:      "websocket",
			Protocol:       SessionConnectionProtocol,
			StreamUrl:      result.StreamURL,
			ResumeToken:    result.ResumeToken,
			ReadyTimeoutMs: SessionConnectionDefaultReadyTimeoutMS,
		},
	}
}

func sessionCurrentRunFromConnection(run *sessionconnection.CurrentRun) *SessionCurrentRun {
	if run == nil {
		return nil
	}
	return &SessionCurrentRun{RunId: run.RunID, Phase: RunPhase(run.Phase), Reason: sessionRunReasonFromConnection(run.Reason)}
}

func sessionRunReasonFromConnection(reason *sessionconnection.RunPhaseReason) *RunPhaseReason {
	if reason == nil {
		return nil
	}
	return &RunPhaseReason{Code: reason.Code, Message: reason.Message, Retryable: reason.Retryable}
}

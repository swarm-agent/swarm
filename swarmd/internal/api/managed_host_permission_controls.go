package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	managedHostPermissionControlCreate        = "permission.create"
	managedHostPermissionControlWait          = "permission.wait"
	managedHostPermissionControlResolve       = "permission.resolve"
	managedHostPermissionControlCancelRun     = "permission.cancel_run"
	managedHostPermissionControlMarkStarted   = "permission.mark_started"
	managedHostPermissionControlMarkCompleted = "permission.mark_completed"

	managedHostPermissionControlResult = "permission.control.result"
)

type managedHostPermissionControlRequest struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	CallID      string `json:"call_id,omitempty"`
	Step        int    `json:"step,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`

	Input        permission.CreateInput  `json:"input,omitempty,omitzero"`
	ResolveInput permission.ResolveInput `json:"resolve,omitempty,omitzero"`
	PermissionID string                  `json:"permission_id,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
	Result       tool.Result             `json:"result,omitempty,omitzero"`
}

type managedHostPermissionControlResponse struct {
	Type        string                         `json:"type"`
	OK          bool                           `json:"ok"`
	RequestID   string                         `json:"request_id,omitempty"`
	SessionID   string                         `json:"session_id,omitempty"`
	Permission  pebblestore.PermissionRecord   `json:"permission,omitempty"`
	Permissions []pebblestore.PermissionRecord `json:"permissions,omitempty"`
	Found       bool                           `json:"found,omitempty"`
	SavedRule   *permission.PolicyRule         `json:"saved_rule,omitempty"`
	Error       string                         `json:"error,omitempty"`
}

type managedHostPermissionControlClient struct {
	server *Server
	nextID atomic.Uint64
}

func NewManagedHostPermissionControlClient(server *Server) *managedHostPermissionControlClient {
	return &managedHostPermissionControlClient{server: server}
}

func (c *managedHostPermissionControlClient) CreatePending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input permission.CreateInput) (pebblestore.PermissionRecord, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlCreate, SessionID: input.SessionID, RunID: input.RunID, CallID: input.CallID, Input: input})
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	return response.Permission, nil
}

func (c *managedHostPermissionControlClient) WaitForResolution(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, permissionID string) (pebblestore.PermissionRecord, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlWait, SessionID: sessionID, PermissionID: permissionID})
	if err != nil {
		return pebblestore.PermissionRecord{}, err
	}
	return response.Permission, nil
}

func (c *managedHostPermissionControlClient) Resolve(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, input permission.ResolveInput) (permission.ResolveResult, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlResolve, SessionID: input.SessionID, PermissionID: input.PermissionID, ResolveInput: input})
	if err != nil {
		return permission.ResolveResult{}, err
	}
	return permission.ResolveResult{Record: response.Permission, SavedRule: response.SavedRule}, nil
}

func (c *managedHostPermissionControlClient) CancelRunPending(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlCancelRun, SessionID: sessionID, RunID: runID, Reason: reason})
	if err != nil {
		return nil, err
	}
	return response.Permissions, nil
}

func (c *managedHostPermissionControlClient) MarkToolStarted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlMarkStarted, SessionID: sessionID, RunID: runID, CallID: callID, Step: step, StartedAt: startedAt})
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	return response.Permission, response.Found, nil
}

func (c *managedHostPermissionControlClient) MarkToolCompleted(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error) {
	response, err := c.roundTrip(ctx, descriptor, managedHostPermissionControlRequest{Type: managedHostPermissionControlMarkCompleted, SessionID: sessionID, RunID: runID, CallID: callID, Step: step, Result: result, CompletedAt: completedAt})
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	return response.Permission, response.Found, nil
}

func (c *managedHostPermissionControlClient) roundTrip(ctx context.Context, descriptor sessionruntime.HostedSessionDescriptor, request managedHostPermissionControlRequest) (managedHostPermissionControlResponse, error) {
	if c == nil || c.server == nil {
		return managedHostPermissionControlResponse{}, errors.New("managed host permission control client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.Type = strings.ToLower(strings.TrimSpace(request.Type))
	if request.Type == "" {
		return managedHostPermissionControlResponse{}, errors.New("permission control type is required")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.SessionID == "" {
		request.SessionID = strings.TrimSpace(request.Input.SessionID)
	}
	if request.SessionID == "" {
		request.SessionID = strings.TrimSpace(request.ResolveInput.SessionID)
	}
	if request.SessionID == "" {
		return managedHostPermissionControlResponse{}, errors.New("session_id is required")
	}
	request.RequestID = c.newRequestID()

	target, err := c.server.managedHostPermissionPrimaryTarget(descriptor)
	if err != nil {
		return managedHostPermissionControlResponse{}, err
	}

	response, err := c.server.postManagedHostPermissionControlToPrimary(ctx, target, request)
	if err != nil {
		return managedHostPermissionControlResponse{}, err
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) != "" {
			return managedHostPermissionControlResponse{}, errors.New(strings.TrimSpace(response.Error))
		}
		return managedHostPermissionControlResponse{}, errors.New("managed host permission control request failed")
	}
	return response, nil
}

func (c *managedHostPermissionControlClient) newRequestID() string {
	if c == nil {
		return fmt.Sprintf("permctl_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("permctl_%d_%06d", time.Now().UnixNano(), c.nextID.Add(1))
}

func (s *Server) managedHostPermissionPrimaryTarget(descriptor sessionruntime.HostedSessionDescriptor) (swarmTarget, error) {
	if s == nil {
		return swarmTarget{}, errors.New("server is not configured")
	}
	primarySwarmID := strings.TrimSpace(descriptor.HostSwarmID)
	if primarySwarmID == "" {
		return swarmTarget{}, errors.New("primary swarm id is not configured for hosted permission routing")
	}
	baseURL := strings.TrimSpace(descriptor.HostBackendURL)
	if baseURL == "" {
		cfg, err := s.loadStartupConfig()
		if err != nil {
			return swarmTarget{}, err
		}
		baseURL = strings.TrimSpace(cfg.DeployContainer.HostAPIBaseURL)
	}
	if baseURL == "" {
		return swarmTarget{}, errors.New("primary backend url is not configured for hosted permission routing")
	}
	return swarmTarget{SwarmID: primarySwarmID, BackendURL: baseURL}, nil
}

func (s *Server) postManagedHostPermissionControlToPrimary(ctx context.Context, target swarmTarget, request managedHostPermissionControlRequest) (managedHostPermissionControlResponse, error) {
	if s == nil {
		return managedHostPermissionControlResponse{}, errors.New("server is not configured")
	}
	var response managedHostPermissionControlResponse
	if err := s.postPeerJSONToSwarmTarget(ctx, target, peerManagedHostSessionRunStreamPath, request, &response); err != nil {
		return managedHostPermissionControlResponse{}, err
	}
	return response, nil
}

func (s *Server) handleManagedHostPermissionControl(w http.ResponseWriter, r *http.Request, req managedHostPermissionControlRequest) bool {
	if !isManagedHostPermissionControlType(req.Type) {
		return false
	}
	if s == nil || s.perm == nil {
		writeJSON(w, http.StatusInternalServerError, managedHostPermissionControlResponse{Type: managedHostPermissionControlResult, OK: false, RequestID: strings.TrimSpace(req.RequestID), SessionID: strings.TrimSpace(req.SessionID), Error: "permission service is not configured"})
		return true
	}
	response := s.applyManagedHostPermissionControl(r.Context(), req)
	status := http.StatusOK
	if !response.OK {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, response)
	return true
}

func (s *Server) applyManagedHostPermissionControl(ctx context.Context, req managedHostPermissionControlRequest) managedHostPermissionControlResponse {
	response := managedHostPermissionControlResponse{Type: managedHostPermissionControlResult, RequestID: strings.TrimSpace(req.RequestID), SessionID: strings.TrimSpace(req.SessionID)}
	if ctx == nil {
		ctx = context.Background()
	}
	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case managedHostPermissionControlCreate:
		record, err := s.perm.CreatePending(req.Input)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permission = record
		response.SessionID = strings.TrimSpace(record.SessionID)
	case managedHostPermissionControlWait:
		record, err := s.perm.WaitForResolution(ctx, req.SessionID, req.PermissionID)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permission = record
		response.SessionID = strings.TrimSpace(record.SessionID)
	case managedHostPermissionControlResolve:
		input := req.ResolveInput
		if strings.TrimSpace(input.SessionID) == "" {
			input.SessionID = req.SessionID
		}
		if strings.TrimSpace(input.PermissionID) == "" {
			input.PermissionID = req.PermissionID
		}
		record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(input.SessionID, input.PermissionID, input.Action, input.Reason, input.ApprovedArguments)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permission = record
		response.SavedRule = savedRule
		response.SessionID = strings.TrimSpace(record.SessionID)
	case managedHostPermissionControlCancelRun:
		records, err := s.perm.CancelRunPending(req.SessionID, req.RunID, req.Reason)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permissions = records
	case managedHostPermissionControlMarkStarted:
		record, found, err := s.perm.MarkToolStarted(req.SessionID, req.RunID, req.CallID, req.Step, req.StartedAt)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permission = record
		response.Found = found
	case managedHostPermissionControlMarkCompleted:
		record, found, err := s.perm.MarkToolCompleted(req.SessionID, req.RunID, req.CallID, req.Step, req.Result, req.CompletedAt)
		if err != nil {
			response.Error = err.Error()
			return response
		}
		response.OK = true
		response.Permission = record
		response.Found = found
	default:
		response.Error = fmt.Sprintf("unsupported permission control type %q", strings.TrimSpace(req.Type))
	}
	return response
}

func isManagedHostPermissionControlType(messageType string) bool {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case managedHostPermissionControlCreate,
		managedHostPermissionControlWait,
		managedHostPermissionControlResolve,
		managedHostPermissionControlCancelRun,
		managedHostPermissionControlMarkStarted,
		managedHostPermissionControlMarkCompleted:
		return true
	default:
		return false
	}
}

func (s *Server) routeMirroredPermissionToManagedHost(ctx context.Context, sessionID string, record pebblestore.PermissionRecord, savedRule *permission.PolicyRule) error {
	if s == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(record.ID) == "" {
		return nil
	}
	target, ok, err := s.managedHostTargetForMirroredSession(sessionID)
	if err != nil || !ok {
		return err
	}
	request := managedHostSessionEventRequest{
		SessionID:     sessionID,
		EventType:     permissionNotificationEventTypeForRecord(record),
		Payload:       managedHostPermissionEventPayload(record, savedRule),
		CausationID:   strings.TrimSpace(record.RunID),
		CorrelationID: strings.TrimSpace(record.CallID),
	}
	if err := s.postPeerJSONToSwarmTarget(ctx, target, peerManagedHostSessionEventPath, request, nil); err != nil {
		return err
	}
	return s.publishPermissionToPrimaryRunStream(sessionID, request.Payload)
}

func (s *Server) managedHostTargetForMirroredSession(sessionID string) (swarmTarget, bool, error) {
	if s == nil || s.sessions == nil {
		return swarmTarget{}, false, nil
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return swarmTarget{}, false, err
	}
	if value, ok := session.Metadata["swarm_managed_host_session"].(bool); !ok || !value {
		return swarmTarget{}, false, nil
	}
	swarmID := managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_swarm_id")
	backendURL := managedHostSessionStringMetadata(session.Metadata, "swarm_managed_host_backend_url")
	if swarmID == "" || backendURL == "" {
		return swarmTarget{}, false, nil
	}
	return swarmTarget{SwarmID: swarmID, BackendURL: backendURL}, true, nil
}

func managedHostPermissionEventPayload(record pebblestore.PermissionRecord, savedRule *permission.PolicyRule) map[string]any {
	payload := map[string]any{
		"type":       permissionNotificationEventTypeForRecord(record),
		"session_id": strings.TrimSpace(record.SessionID),
		"run_id":     strings.TrimSpace(record.RunID),
		"call_id":    strings.TrimSpace(record.CallID),
		"tool_name":  strings.TrimSpace(record.ToolName),
		"arguments":  strings.TrimSpace(record.ToolArguments),
		"permission": record,
	}
	if savedRule != nil {
		payload["saved_rule"] = savedRule
	}
	return payload
}

func permissionNotificationEventTypeForRecord(record pebblestore.PermissionRecord) string {
	if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
		return "permission.requested"
	}
	return "permission.updated"
}

func (s *Server) storeMirroredEventPayloadPermission(sessionID string, payload map[string]any) error {
	if s == nil || s.perm == nil || len(payload) == 0 {
		return nil
	}
	var envelope struct {
		Permission *pebblestore.PermissionRecord `json:"permission"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Permission == nil || strings.TrimSpace(envelope.Permission.ID) == "" {
		return nil
	}
	record := *envelope.Permission
	if strings.TrimSpace(record.SessionID) == "" {
		record.SessionID = strings.TrimSpace(sessionID)
	}
	if strings.TrimSpace(record.SessionID) == "" {
		return nil
	}
	return s.storeMirroredPermissionRecord(record)
}

func (s *Server) storeMirroredPermissionRecord(record pebblestore.PermissionRecord) error {
	if s == nil || s.perm == nil || strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.ID) == "" {
		return nil
	}
	if mirrored, ok := s.perm.(interface {
		StoreMirroredPermission(pebblestore.PermissionRecord) error
	}); ok {
		return mirrored.StoreMirroredPermission(record)
	}
	return nil
}

func (s *Server) publishPermissionToPrimaryRunStream(sessionID string, payload map[string]any) error {
	if s == nil || s.runStreams == nil || len(payload) == 0 {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var msg runStreamWireEvent
	if err := json.Unmarshal(raw, &msg); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Type) == "" {
		msg.Type = "permission.updated"
	}
	if strings.TrimSpace(msg.SessionID) == "" {
		msg.SessionID = strings.TrimSpace(sessionID)
	}
	if strings.TrimSpace(msg.SessionID) == "" || strings.TrimSpace(msg.RunID) == "" {
		return nil
	}
	state, err := s.runStreams.ensureRunWithID(msg.SessionID, msg.RunID)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	s.runStreams.publish(msg.RunID, msg)
	return nil
}

var _ permission.HostedPermissionSync = (*managedHostPermissionControlClient)(nil)

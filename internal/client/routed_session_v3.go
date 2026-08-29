package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const routedSessionV3Path = "/v3/sessions:routed"

type RoutedSessionV3MediaRequest struct {
	StagingID string `json:"staging_id"`
	Modality  string `json:"modality,omitempty"`
	FileType  string `json:"file_type,omitempty"`
}

type RoutedSessionV3StartRequest struct {
	Input                    string                        `json:"input"`
	ClientRequestID          string                        `json:"client_request_id"`
	IdempotencyKey           string                        `json:"idempotency_key"`
	AgentName                string                        `json:"agent_name,omitempty"`
	Metadata                 map[string]any                `json:"metadata,omitempty"`
	PlanModeRequested        bool                          `json:"plan_mode_requested"`
	WorkspacePath            string                        `json:"workspace_path"`
	HostWorkspacePath        string                        `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath     string                        `json:"runtime_workspace_path,omitempty"`
	WorkspaceBindingID       string                        `json:"workspace_binding_id"`
	SwarmID                  string                        `json:"swarm_id"`
	TargetKind               string                        `json:"target_kind"`
	TargetRelationship       string                        `json:"target_relationship"`
	Media                    []RoutedSessionV3MediaRequest `json:"media,omitempty"`
	StagingIDs               []string                      `json:"staging_ids,omitempty"`
}

type RoutedSessionV3Identity struct {
	SessionID             string `json:"session_id"`
	Title                 string `json:"title"`
	WorkspaceID           string `json:"workspace_id,omitempty"`
	WorkspaceBindingID    string `json:"workspace_binding_id,omitempty"`
	SourceWorkspaceID     string `json:"source_workspace_id,omitempty"`
	SourceWorkspaceName   string `json:"source_workspace_name"`
	SourceWorkspacePath   string `json:"source_workspace_path"`
	RuntimeWorkspacePath  string `json:"runtime_workspace_path"`
	RuntimeSwarmID        string `json:"runtime_swarm_id,omitempty"`
	AuthorityHostSwarmID  string `json:"authority_host_swarm_id,omitempty"`
	WorktreeEnabled       bool   `json:"worktree_enabled"`
	RequestedWorktreeName string `json:"requested_worktree_name,omitempty"`
	WorktreeRootPath      string `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch    string `json:"worktree_base_branch,omitempty"`
	WorktreeBranch        string `json:"worktree_branch,omitempty"`
}

type RoutedSessionV3AgenticSettings struct {
	Mode                string                    `json:"mode"`
	AgentName           string                    `json:"agent_name"`
	ResolvedAgentName   string                    `json:"resolved_agent_name"`
	RuntimeMode         string                    `json:"runtime_mode,omitempty"`
	StoredPreference    ModelPreference           `json:"stored_preference"`
	EffectivePreference ModelPreference           `json:"effective_preference"`
	AgentModelPolicy    SessionV3AgentModelPolicy `json:"agent_model_policy"`
	ContextWindow       int                       `json:"context_window"`
	MaxOutputTokens     int                       `json:"max_output_tokens"`
	ProjectionSeq       uint64                    `json:"projection_seq"`
}

type RoutedSessionV3RunState struct {
	SessionID            string `json:"session_id"`
	RunID                string `json:"run_id"`
	Active               bool   `json:"active"`
	Status               string `json:"status"`
	BlockedReason        string `json:"blocked_reason,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	StartedAt            int64  `json:"started_at,omitempty"`
	CompletedAt          int64  `json:"completed_at,omitempty"`
	DurationMS           int64  `json:"duration_ms,omitempty"`
	CumulativeDurationMS int64  `json:"cumulative_duration_ms,omitempty"`
	UpdatedAt            int64  `json:"updated_at"`
	EventSeq             uint64 `json:"event_seq"`
}

func (s RoutedSessionV3RunState) RunIntent() SessionV3RunIntent {
	return SessionV3RunIntent{
		SessionID: s.SessionID, RunID: s.RunID, Status: s.Status, BlockedReason: s.BlockedReason,
		CreatedAt: s.CreatedAt, StartedAt: s.StartedAt, CompletedAt: s.CompletedAt,
		DurationMS: s.DurationMS, CumulativeDurationMS: s.CumulativeDurationMS,
		UpdatedAt: s.UpdatedAt, EventSeq: s.EventSeq,
	}
}

type RoutedSessionV3SessionView struct {
	Identity           *RoutedSessionV3Identity        `json:"identity"`
	AgenticSettings    *RoutedSessionV3AgenticSettings `json:"agentic_settings"`
	MediaCapability    *json.RawMessage                `json:"media_capability"`
	PendingPermissions []PermissionRecord              `json:"pending_permissions"`
	UsageSummary       *SessionUsageSummary            `json:"usage_summary,omitempty"`
	CurrentRunState    *RoutedSessionV3RunState        `json:"current_run_state,omitempty"`
	HasActivePlan      *bool                           `json:"has_active_plan,omitempty"`
	ActivePlan         *SessionPlan                    `json:"active_plan,omitempty"`
}

type RoutedSessionV3StartResponse struct {
	OK           bool                       `json:"ok"`
	SessionID    string                     `json:"session_id"`
	Title        string                     `json:"title"`
	StartingMode string                     `json:"starting_mode"`
	Replayed     bool                       `json:"replayed"`
	Session      SessionSummary             `json:"session"`
	SessionView  RoutedSessionV3SessionView `json:"session_view"`
	FirstMessage SessionMessage             `json:"first_message"`
	Projection   SessionV3Projection        `json:"projection"`
	Mutation     SessionV3MutationResult    `json:"mutation"`
}

func (r RoutedSessionV3StartResponse) Hydrated() SessionV3Hydrated {
	events := append([]SessionV3Event(nil), r.Mutation.Events...)
	if len(events) == 0 && strings.TrimSpace(r.Mutation.Event.ID) != "" {
		events = append(events, r.Mutation.Event)
	}
	var activeRun *SessionV3RunIntent
	if r.Mutation.RunIntent != nil {
		run := *r.Mutation.RunIntent
		activeRun = &run
	} else if r.SessionView.CurrentRunState != nil {
		run := r.SessionView.CurrentRunState.RunIntent()
		activeRun = &run
	}
	hydrated := SessionV3Hydrated{
		Session: r.Session, Projection: r.Projection, Messages: []SessionMessage{r.FirstMessage}, Events: events,
		PendingPermissions: append([]PermissionRecord(nil), r.SessionView.PendingPermissions...),
		UsageSummary:       r.SessionView.UsageSummary, ActiveRunIntent: activeRun,
		HasActivePlan: r.SessionView.HasActivePlan != nil && *r.SessionView.HasActivePlan,
		ActivePlan:    r.SessionView.ActivePlan,
	}
	if settings := r.SessionView.AgenticSettings; settings != nil {
		hydrated.Preference = settings.EffectivePreference
		hydrated.ContextWindow = settings.ContextWindow
		hydrated.MaxOutputTokens = settings.MaxOutputTokens
		hydrated.AgentModelPolicy = settings.AgentModelPolicy
	}
	// RealtimeOutbox.EndpointCursor is the durable outbox storage cursor from the
	// mutation response, not a signed transport cursor scoped to a realtime
	// surface. A newly routed session must use the cursorless start-at-current
	// handshake so /v3/realtime/stream can issue the canonical signed cursor.
	hydrated.Session = markSessionV3(hydrated.Session, hydrated.Projection)
	return hydrated
}

func (c *API) StartRoutedSessionV3(ctx context.Context, request RoutedSessionV3StartRequest) (RoutedSessionV3StartResponse, error) {
	request.Input = strings.TrimSpace(request.Input)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.AgentName = strings.TrimSpace(request.AgentName)
	request.WorkspacePath = strings.TrimSpace(request.WorkspacePath)
	request.HostWorkspacePath = strings.TrimSpace(request.HostWorkspacePath)
	request.RuntimeWorkspacePath = strings.TrimSpace(request.RuntimeWorkspacePath)
	request.WorkspaceBindingID = strings.TrimSpace(request.WorkspaceBindingID)
	request.SwarmID = strings.TrimSpace(request.SwarmID)
	request.TargetKind = strings.TrimSpace(request.TargetKind)
	request.TargetRelationship = strings.TrimSpace(request.TargetRelationship)
	if request.Input == "" {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start requires input")
	}
	if request.ClientRequestID == "" {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start requires client_request_id")
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = request.ClientRequestID
	}
	if request.IdempotencyKey != request.ClientRequestID {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start requires one stable client_request_id/idempotency identity")
	}
	if request.WorkspacePath == "" || request.WorkspaceBindingID == "" || request.SwarmID == "" {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start requires workspace_path, workspace_binding_id, and swarm_id")
	}
	if !strings.EqualFold(request.TargetKind, "host") || !strings.EqualFold(request.TargetRelationship, "self") {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start requires canonical host/self workspace authority")
	}
	if len(request.Media) != 0 && len(request.StagingIDs) != 0 {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start accepts media or staging_ids, not both")
	}
	status, body, err := c.requestWithHeaders(ctx, http.MethodPost, routedSessionV3Path, request, true, map[string]string{"Idempotency-Key": request.ClientRequestID})
	if err != nil {
		return RoutedSessionV3StartResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return RoutedSessionV3StartResponse{}, decodeAPIError(status, body)
	}
	var raw routedSessionV3StartResponseWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return RoutedSessionV3StartResponse{}, fmt.Errorf("decode %s response: %w", routedSessionV3Path, err)
	}
	response, err := validateRoutedSessionV3StartResponse(raw)
	if err != nil {
		return RoutedSessionV3StartResponse{}, err
	}
	if response.FirstMessage.Content != request.Input {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start returned an invalid response: first_message does not match input")
	}
	identity := response.SessionView.Identity
	if !strings.EqualFold(strings.TrimSpace(identity.SourceWorkspacePath), request.WorkspacePath) || !strings.EqualFold(strings.TrimSpace(identity.WorkspaceBindingID), request.WorkspaceBindingID) {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start returned source workspace authority different from the request")
	}
	responseSwarmID := strings.TrimSpace(identity.AuthorityHostSwarmID)
	if responseSwarmID == "" {
		responseSwarmID = strings.TrimSpace(identity.RuntimeSwarmID)
	}
	if !strings.EqualFold(responseSwarmID, request.SwarmID) {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start returned swarm authority different from the request")
	}
	if !identity.WorktreeEnabled || !response.Session.WorktreeEnabled {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start returned a non-worktree session")
	}
	return response, nil
}

type routedSessionV3StartResponseWire struct {
	OK           *bool                       `json:"ok"`
	SessionID    string                      `json:"session_id"`
	Title        string                      `json:"title"`
	StartingMode string                      `json:"starting_mode"`
	Replayed     *bool                       `json:"replayed"`
	Session      *SessionSummary             `json:"session"`
	SessionView  *RoutedSessionV3SessionView `json:"session_view"`
	FirstMessage *SessionMessage             `json:"first_message"`
	Projection   *SessionV3Projection        `json:"projection"`
	Mutation     *SessionV3MutationResult    `json:"mutation"`
}

func validateRoutedSessionV3StartResponse(raw routedSessionV3StartResponseWire) (RoutedSessionV3StartResponse, error) {
	invalid := func(reason string) (RoutedSessionV3StartResponse, error) {
		return RoutedSessionV3StartResponse{}, errors.New("v3 routed start returned an invalid response: " + reason)
	}
	if raw.OK == nil || !*raw.OK {
		return invalid("ok must be true")
	}
	if raw.Replayed == nil {
		return invalid("replayed is required")
	}
	sessionID := strings.TrimSpace(raw.SessionID)
	title := strings.TrimSpace(raw.Title)
	mode := strings.ToLower(strings.TrimSpace(raw.StartingMode))
	if sessionID == "" || title == "" || (mode != "auto" && mode != "plan") {
		return invalid("session_id, title, and starting_mode are required")
	}
	if raw.Session == nil || strings.TrimSpace(raw.Session.ID) != sessionID || strings.TrimSpace(raw.Session.Title) != title || strings.ToLower(strings.TrimSpace(raw.Session.Mode)) != mode {
		return invalid("session does not match routed identity")
	}
	if raw.SessionView == nil || raw.SessionView.Identity == nil || strings.TrimSpace(raw.SessionView.Identity.SessionID) != sessionID || strings.TrimSpace(raw.SessionView.Identity.Title) != title || strings.TrimSpace(raw.SessionView.Identity.SourceWorkspacePath) == "" || strings.TrimSpace(raw.SessionView.Identity.RuntimeWorkspacePath) == "" {
		return invalid("session_view.identity does not match routed identity")
	}
	if raw.SessionView.AgenticSettings == nil || strings.ToLower(strings.TrimSpace(raw.SessionView.AgenticSettings.Mode)) != mode || raw.SessionView.MediaCapability == nil || raw.SessionView.PendingPermissions == nil {
		return invalid("session_view canonical resources are incomplete or do not match starting_mode")
	}
	if strings.TrimSpace(raw.SessionView.AgenticSettings.EffectivePreference.Provider) == "" || strings.TrimSpace(raw.SessionView.AgenticSettings.EffectivePreference.Model) == "" {
		return invalid("session_view effective model authority is incomplete")
	}
	if raw.FirstMessage == nil || strings.TrimSpace(raw.FirstMessage.ID) == "" || strings.TrimSpace(raw.FirstMessage.SessionID) != sessionID || strings.ToLower(strings.TrimSpace(raw.FirstMessage.Role)) != "user" {
		return invalid("first_message is not the durable user message")
	}
	if raw.Projection == nil || strings.TrimSpace(raw.Projection.SessionID) != sessionID || raw.Projection.LastEventSeq == 0 {
		return invalid("projection does not match session_id")
	}
	if raw.Mutation == nil || strings.TrimSpace(raw.Mutation.SessionID) != sessionID || strings.TrimSpace(raw.Mutation.Projection.SessionID) != sessionID || raw.Mutation.Projection.LastEventSeq != raw.Projection.LastEventSeq {
		return invalid("mutation projection does not match durable response")
	}
	if raw.Mutation.Message == nil || strings.TrimSpace(raw.Mutation.Message.ID) != strings.TrimSpace(raw.FirstMessage.ID) || strings.TrimSpace(raw.Mutation.Message.SessionID) != sessionID || strings.ToLower(strings.TrimSpace(raw.Mutation.Message.Role)) != "user" || raw.Mutation.Message.Content != raw.FirstMessage.Content {
		return invalid("mutation message does not match first_message")
	}
	if raw.Mutation.RunIntent == nil || strings.TrimSpace(raw.Mutation.RunIntent.SessionID) != sessionID || strings.TrimSpace(raw.Mutation.RunIntent.RunID) == "" || strings.TrimSpace(raw.Mutation.RunIntent.Status) == "" {
		return invalid("mutation run_intent does not match session_id")
	}
	if raw.Mutation.RealtimeOutbox != nil && (strings.TrimSpace(raw.Mutation.RealtimeOutbox.SessionID) != sessionID || strings.TrimSpace(raw.Mutation.RealtimeOutbox.EndpointCursor) == "" || strings.TrimSpace(raw.Mutation.RealtimeOutbox.Projection.SessionID) != sessionID || raw.Mutation.RealtimeOutbox.Projection.LastEventSeq != raw.Projection.LastEventSeq || strings.TrimSpace(raw.Mutation.RealtimeOutbox.Event.SessionID) != sessionID) {
		return invalid("mutation realtime_outbox does not match session_id")
	}
	if eventID := strings.TrimSpace(raw.Mutation.Event.ID); eventID == "" || strings.TrimSpace(raw.Mutation.Event.SessionID) != sessionID || raw.Mutation.Event.Seq != raw.Mutation.PrimarySeq {
		return invalid("mutation primary event does not match session_id/primary_seq")
	}
	for _, event := range raw.Mutation.Events {
		if strings.TrimSpace(event.SessionID) != sessionID {
			return invalid("mutation event does not match session_id")
		}
	}
	response := RoutedSessionV3StartResponse{
		OK: true, SessionID: sessionID, Title: title, StartingMode: mode, Replayed: *raw.Replayed,
		Session: *raw.Session, SessionView: *raw.SessionView, FirstMessage: *raw.FirstMessage,
		Projection: *raw.Projection, Mutation: *raw.Mutation,
	}
	response.Session = markSessionV3(response.Session, response.Projection)
	return response, nil
}

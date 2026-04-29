package flow

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Checkpoint-1 contracts for distributed Flows.
//
// Ownership model:
//   - the controller persists the desired Flow and an outbox command;
//   - the controller resolves the target swarm and delivers an idempotent command;
//   - the target persists accepted assignments and owns all scheduling/execution;
//   - later controller downtime must not prevent accepted target-local schedules from firing.
//
// These interfaces intentionally do not expose request-time tool overrides. A Flow
// selects a saved agent profile by TargetKind/TargetName, and the target daemon must
// resolve that saved profile locally when it launches the run.

type CommandAction string

const (
	CommandInstall CommandAction = "install"
	CommandUpdate  CommandAction = "update"
	CommandDelete  CommandAction = "delete"
	CommandRunNow  CommandAction = "run_now"
)

type AssignmentStatus string

const (
	AssignmentAccepted       AssignmentStatus = "accepted"
	AssignmentDuplicate      AssignmentStatus = "duplicate"
	AssignmentRejected       AssignmentStatus = "rejected"
	AssignmentOutOfOrder     AssignmentStatus = "out_of_order"
	AssignmentPendingSync    AssignmentStatus = "pending_sync"
	AssignmentTargetOffline  AssignmentStatus = "target_offline"
	AssignmentTargetUnusable AssignmentStatus = "target_unusable"
)

type TargetSelection struct {
	SwarmID      string `json:"swarm_id,omitempty"`
	Kind         string `json:"kind,omitempty"` // self, local, remote; mirrors api.swarmTarget.Kind.
	DeploymentID string `json:"deployment_id,omitempty"`
	Name         string `json:"name,omitempty"`
}

type ResolvedTarget struct {
	Selection    TargetSelection `json:"selection"`
	SwarmID      string          `json:"swarm_id"`
	Name         string          `json:"name,omitempty"`
	Relationship string          `json:"relationship,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	DeploymentID string          `json:"deployment_id,omitempty"`
	BackendURL   string          `json:"backend_url,omitempty"`
	Online       bool            `json:"online"`
	Selectable   bool            `json:"selectable"`
	LastError    string          `json:"last_error,omitempty"`
}

type TargetStatus struct {
	Target    ResolvedTarget `json:"target"`
	Online    bool           `json:"online"`
	Status    string         `json:"status,omitempty"`
	LastError string         `json:"last_error,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
}

// TargetResolver maps a user/controller target selection onto an addressable swarm.
// Controller implementations should reuse the existing swarm target list paths in
// api/swarm_targets.go instead of inventing new discovery records.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, selection TargetSelection) (ResolvedTarget, error)
	ListTargets(ctx context.Context) ([]ResolvedTarget, error)
}

// TargetStatusProvider reports whether a target is currently deliverable. Unreachable
// targets are not command success; controller callers should keep the outbox command
// pending and surface pending_sync/target_offline state to the UI.
type TargetStatusProvider interface {
	TargetStatus(ctx context.Context, target ResolvedTarget) (TargetStatus, error)
}

type AgentSelection struct {
	// TargetKind and TargetName are forwarded to the target daemon's run service.
	// For saved background profiles this is target_kind="background" and the
	// saved profile name. Do not copy or store the profile tool contract here.
	TargetKind string `json:"target_kind"`
	TargetName string `json:"target_name"`
}

type WorkspaceContext struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	WorktreeMode  string `json:"worktree_mode,omitempty"`
}

type ScheduleSpec struct {
	Cadence  string `json:"cadence"`
	Time     string `json:"time,omitempty"`
	Weekday  string `json:"weekday,omitempty"`
	MonthDay int    `json:"month_day,omitempty"`
	Timezone string `json:"timezone"`
}

type CatchUpPolicy struct {
	Mode       string `json:"mode"`
	MaxCatchUp int    `json:"max_catch_up,omitempty"`
}

type PromptIntent struct {
	Prompt string     `json:"prompt"`
	Mode   string     `json:"mode,omitempty"`
	Tasks  []TaskStep `json:"tasks,omitempty"`
}

type TaskStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Action string `json:"action"`
}

type Assignment struct {
	FlowID        string           `json:"flow_id"`
	Revision      int64            `json:"revision"`
	Name          string           `json:"name"`
	Enabled       bool             `json:"enabled"`
	Target        TargetSelection  `json:"target"`
	Agent         AgentSelection   `json:"agent"`
	Workspace     WorkspaceContext `json:"workspace"`
	Schedule      ScheduleSpec     `json:"schedule"`
	CatchUpPolicy CatchUpPolicy    `json:"catch_up_policy"`
	Intent        PromptIntent     `json:"intent"`
}

type AssignmentCommand struct {
	CommandID  string        `json:"command_id"`
	FlowID     string        `json:"flow_id"`
	Revision   int64         `json:"revision"`
	Action     CommandAction `json:"action"`
	CreatedAt  time.Time     `json:"created_at"`
	Assignment Assignment    `json:"assignment,omitempty"`
}

type CommandKey struct {
	FlowID    string `json:"flow_id"`
	Revision  int64  `json:"revision"`
	CommandID string `json:"command_id"`
}

func (c AssignmentCommand) IdempotencyKey() CommandKey {
	return CommandKey{
		FlowID:    strings.TrimSpace(firstNonEmpty(c.FlowID, c.Assignment.FlowID)),
		Revision:  firstNonZero(c.Revision, c.Assignment.Revision),
		CommandID: strings.TrimSpace(c.CommandID),
	}
}

func (c AssignmentCommand) ValidateIdempotencyKey() error {
	key := c.IdempotencyKey()
	if key.FlowID == "" {
		return errors.New("flow_id is required")
	}
	if key.Revision <= 0 {
		return errors.New("revision is required")
	}
	if key.CommandID == "" {
		return errors.New("command_id is required")
	}
	return nil
}

type AssignmentAck struct {
	CommandID        string           `json:"command_id"`
	FlowID           string           `json:"flow_id"`
	AcceptedRevision int64            `json:"accepted_revision,omitempty"`
	Status           AssignmentStatus `json:"status"`
	Reason           string           `json:"reason,omitempty"`
	TargetSwarmID    string           `json:"target_swarm_id,omitempty"`
	TargetClock      time.Time        `json:"target_clock,omitempty"`
}

// FlowAssignmentTransport sends idempotent commands to a resolved target. The
// current controller implementation should use api.postPeerJSONToSwarmTarget and
// register a target endpoint under /v1/swarm/peer/flows/apply for local transport.
type FlowAssignmentTransport interface {
	DeliverCommand(ctx context.Context, target ResolvedTarget, command AssignmentCommand) (AssignmentAck, error)
}

type AcceptedAssignment struct {
	Assignment
	AcceptedAt time.Time `json:"accepted_at"`
}

type RunRequest struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	ScheduledAt time.Time `json:"scheduled_at"`
	RunNow      bool      `json:"run_now,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
}

type RunClaimKey struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

func (r RunRequest) ClaimKey() RunClaimKey {
	return RunClaimKey{FlowID: strings.TrimSpace(r.FlowID), Revision: r.Revision, ScheduledAt: r.ScheduledAt.UTC()}
}

type RunStart struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	ScheduledAt time.Time `json:"scheduled_at"`
	SessionID   string    `json:"session_id"`
	RunID       string    `json:"run_id"`
	Status      string    `json:"status"`
}

// FlowRunner is implemented on the target daemon. It launches accepted Flows from
// target-local state only, using the saved agent profile named by Assignment.Agent.
// Implementations must leave request-time tool_scope empty so capabilities come
// from the target's saved agent profile contract.
type FlowRunner interface {
	RunAcceptedFlow(ctx context.Context, assignment AcceptedAssignment, request RunRequest) (RunStart, error)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

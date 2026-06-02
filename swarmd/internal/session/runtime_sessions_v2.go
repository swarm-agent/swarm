package session

import (
	"encoding/json"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	RuntimeSessionMirrorTypeSessionSnapshot  = "session.snapshot"
	RuntimeSessionMirrorTypeSessionLifecycle = "session.lifecycle"
	RuntimeSessionMirrorTypeRunEvent         = "run.event"
	RuntimeSessionMirrorTypeMessageStored    = "message.stored"
	RuntimeSessionMirrorTypeUsageDelta       = "usage.delta"
	RuntimeSessionMirrorTypeRuntimeOpened    = "runtime.opened"
	RuntimeSessionMirrorTypeRuntimeClosed    = "runtime.closed"
	RuntimeSessionMirrorTypeRuntimeError     = "runtime.error"
)

type RuntimeSessionAuthority struct {
	SessionID                 string `json:"session_id"`
	UserID                    string `json:"user_id,omitempty"`
	AccountScopeID            string `json:"account_scope_id,omitempty"`
	ExecutionClass            string `json:"execution_class"`
	RuntimeSwarmID            string `json:"runtime_swarm_id"`
	RuntimeKind               string `json:"runtime_kind,omitempty"`
	AuthorityHostSwarmID      string `json:"authority_host_swarm_id"`
	AuthorityContainerID      string `json:"authority_container_id,omitempty"`
	WorkspaceBindingID        string `json:"workspace_binding_id"`
	PlacementGeneration       int    `json:"placement_generation"`
	BindingGeneration         int    `json:"binding_generation"`
	SourceWorkspaceID         string `json:"source_workspace_id"`
	SourceWorkspaceGeneration int64  `json:"source_workspace_generation"`
	SourceWorkspaceName       string `json:"source_workspace_name,omitempty"`
	SourceWorkspacePath       string `json:"source_workspace_path"`
	DestinationRuntimeSwarmID string `json:"destination_runtime_swarm_id"`
	DestinationRuntimeKind    string `json:"destination_runtime_kind"`
	DestinationAuthorityHost  string `json:"destination_authority_host_swarm_id"`
	DestinationContainerID    string `json:"destination_container_id,omitempty"`
	RuntimeWorkspacePath      string `json:"runtime_workspace_path"`
}

type RuntimeSessionWorkspaceFacts struct {
	WorkspaceID          string   `json:"workspace_id,omitempty"`
	WorkspaceGeneration  int64    `json:"workspace_generation,omitempty"`
	WorkspaceName        string   `json:"workspace_name,omitempty"`
	WorkspacePath        string   `json:"workspace_path"`
	RuntimeWorkspacePath string   `json:"runtime_workspace_path"`
	TemporaryRoots       []string `json:"temporary_workspace_roots,omitempty"`
}

type RuntimeSessionConfig struct {
	Title                    string                      `json:"title,omitempty"`
	Mode                     string                      `json:"mode,omitempty"`
	AgentName                string                      `json:"agent_name,omitempty"`
	WorktreeMode             string                      `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch *bool                       `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch       string                      `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName       string                      `json:"worktree_branch_name,omitempty"`
	Preference               pebblestore.ModelPreference `json:"preference,omitempty"`
	Metadata                 map[string]any              `json:"metadata,omitempty"`
}

type RuntimeSessionWorktreeFacts struct {
	Enabled    bool   `json:"enabled,omitempty"`
	RootPath   string `json:"root_path,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

type RuntimeSessionOpenRequest struct {
	SessionID                   string                                      `json:"session_id"`
	Authority                   RuntimeSessionAuthority                     `json:"authority"`
	SessionExecution            pebblestore.SessionExecutionV2Record        `json:"session_execution"`
	BindingAuthoritySnapshot    *pebblestore.TopologyWorkspaceBindingRecord `json:"binding_authority_snapshot,omitempty"`
	SourceWorkspace             RuntimeSessionWorkspaceFacts                `json:"source_workspace"`
	DestinationRuntimeWorkspace RuntimeSessionWorkspaceFacts                `json:"destination_runtime_workspace"`
	Config                      RuntimeSessionConfig                        `json:"config"`
	Worktree                    RuntimeSessionWorktreeFacts                 `json:"worktree,omitempty"`
}

type RuntimeSessionOpenResponse struct {
	OK                   bool                                  `json:"ok"`
	SessionID            string                                `json:"session_id"`
	RuntimeSwarmID       string                                `json:"runtime_swarm_id"`
	AuthorityHostSwarmID string                                `json:"authority_host_swarm_id"`
	AuthorityContainerID string                                `json:"authority_container_id,omitempty"`
	WorkspaceBindingID   string                                `json:"workspace_binding_id"`
	Status               string                                `json:"status,omitempty"`
	LifecycleState       string                                `json:"lifecycle_state,omitempty"`
	RuntimeWorkspacePath string                                `json:"runtime_workspace_path,omitempty"`
	Worktree             RuntimeSessionWorktreeFacts           `json:"worktree,omitempty"`
	Title                string                                `json:"title,omitempty"`
	Mode                 string                                `json:"mode,omitempty"`
	Preference           pebblestore.ModelPreference           `json:"preference,omitempty"`
	Metadata             map[string]any                        `json:"metadata,omitempty"`
	InitialMessages      []pebblestore.MessageSnapshot         `json:"initial_messages,omitempty"`
	InitialEvents        []pebblestore.EventEnvelope           `json:"initial_events,omitempty"`
	UsageBaseline        *pebblestore.SessionTurnUsageSnapshot `json:"usage_baseline,omitempty"`
	MirrorBatch          *RuntimeSessionMirrorBatchRequest     `json:"mirror_batch,omitempty"`
	Error                string                                `json:"error,omitempty"`
}

type RuntimeSessionSyncStateRequest struct {
	SessionID        string                                `json:"session_id"`
	Authority        RuntimeSessionAuthority               `json:"authority"`
	SessionExecution pebblestore.SessionExecutionV2Record  `json:"session_execution"`
	Session          *pebblestore.SessionSnapshot          `json:"session,omitempty"`
	Lifecycle        *pebblestore.SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	Metadata         map[string]any                        `json:"metadata,omitempty"`
}

type RuntimeSessionSyncStateResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	Status    string `json:"status,omitempty"`
}

type RuntimeSessionRunRequest struct {
	SessionID        string                               `json:"session_id"`
	Authority        RuntimeSessionAuthority              `json:"authority"`
	SessionExecution pebblestore.SessionExecutionV2Record `json:"session_execution"`
	RunID            string                               `json:"run_id,omitempty"`
	Prompt           string                               `json:"prompt,omitempty"`
	Background       bool                                 `json:"background,omitempty"`
	Mode             string                               `json:"mode,omitempty"`
	AgentName        string                               `json:"agent_name,omitempty"`
	Preference       pebblestore.ModelPreference          `json:"preference,omitempty"`
	Metadata         map[string]any                       `json:"metadata,omitempty"`
}

type RuntimeSessionRunResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id,omitempty"`
	Status    string `json:"status,omitempty"`
}

type RuntimeSessionStreamRequest struct {
	SessionID        string                               `json:"session_id"`
	Authority        RuntimeSessionAuthority              `json:"authority,omitempty"`
	SessionExecution pebblestore.SessionExecutionV2Record `json:"session_execution,omitempty"`
	RunID            string                               `json:"run_id,omitempty"`
	AfterSeq         uint64                               `json:"after_seq,omitempty"`
}

type RuntimeSessionStreamResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Stream    string `json:"stream,omitempty"`
}

type RuntimeSessionMirrorBatchRequest struct {
	SessionID        string                               `json:"session_id"`
	Authority        RuntimeSessionAuthority              `json:"authority"`
	SessionExecution pebblestore.SessionExecutionV2Record `json:"session_execution"`
	Items            []RuntimeSessionMirrorItem           `json:"items"`
}

type RuntimeSessionMirrorBatchResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	Accepted  int    `json:"accepted"`
	Status    string `json:"status,omitempty"`
}

type RuntimeSessionMirrorItem struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	RunID     string          `json:"run_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	CreatedAt int64           `json:"created_at,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type RuntimeSessionSnapshotMirrorItem struct {
	Session pebblestore.SessionSnapshot `json:"session"`
}

type RuntimeSessionLifecycleMirrorItem struct {
	Lifecycle pebblestore.SessionLifecycleSnapshot `json:"lifecycle"`
}

type RuntimeSessionRunEventMirrorItem struct {
	RunID string                    `json:"run_id"`
	Seq   uint64                    `json:"seq"`
	Event pebblestore.EventEnvelope `json:"event"`
}

type RuntimeSessionMessageStoredMirrorItem struct {
	Message pebblestore.MessageSnapshot `json:"message"`
}

type RuntimeSessionUsageDeltaMirrorItem struct {
	UsageDelta pebblestore.SessionTurnUsageSnapshot `json:"usage_delta"`
}

type RuntimeSessionOpenedMirrorItem struct {
	SessionID        string                               `json:"session_id"`
	SessionExecution pebblestore.SessionExecutionV2Record `json:"session_execution"`
	OpenedAt         int64                                `json:"opened_at,omitempty"`
}

type RuntimeSessionClosedMirrorItem struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason,omitempty"`
	ClosedAt  int64  `json:"closed_at,omitempty"`
}

type RuntimeSessionErrorMirrorItem struct {
	SessionID string         `json:"session_id"`
	RunID     string         `json:"run_id,omitempty"`
	Code      string         `json:"code,omitempty"`
	Error     string         `json:"error"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

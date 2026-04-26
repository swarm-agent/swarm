package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultTimeout = 5 * time.Second

const (
	streamErrorLogTimeout   = 2 * time.Second
	streamClientErrorPathID = "run.stream.client.error.v3"
	maxRunStreamReconnects  = 3
	localTransportBaseURL   = "http://swarm-local-transport"
	localTransportSocketEnv = "SWARMD_LOCAL_TRANSPORT_SOCKET"
)

type API struct {
	baseURL string
	http    *http.Client
	mu      sync.RWMutex
	token   string
}

type HealthStatus struct {
	OK                bool   `json:"ok"`
	Mode              string `json:"mode"`
	BypassPermissions bool   `json:"bypass_permissions,omitempty"`
}

type UpdateStatus struct {
	CurrentVersion   string `json:"current_version"`
	CurrentLane      string `json:"current_lane,omitempty"`
	DevMode          bool   `json:"dev_mode"`
	Suppressed       bool   `json:"suppressed"`
	Reason           string `json:"reason,omitempty"`
	CheckedAtUnixMS  int64  `json:"checked_at_unix_ms,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	LatestURL        string `json:"latest_url,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	ComparisonSource string `json:"comparison_source,omitempty"`
	Error            string `json:"error,omitempty"`
	Stale            bool   `json:"stale,omitempty"`
}

type UpdateApplyPlan struct {
	CurrentVersion   string `json:"current_version"`
	CurrentLane      string `json:"current_lane,omitempty"`
	TargetVersion    string `json:"target_version"`
	ReleaseURL       string `json:"release_url,omitempty"`
	AssetName        string `json:"asset_name"`
	AssetURL         string `json:"asset_url"`
	SHA256           string `json:"sha256"`
	ComparisonSource string `json:"comparison_source,omitempty"`
}

type LocalContainerUpdatePlan struct {
	PathID        string                       `json:"path_id"`
	Mode          string                       `json:"mode"`
	DevMode       bool                         `json:"dev_mode"`
	Target        LocalContainerUpdateTarget   `json:"target"`
	Summary       LocalContainerUpdateSummary  `json:"summary"`
	Containers    []LocalContainerUpdateItem   `json:"containers"`
	Contract      LocalContainerUpdateContract `json:"contract"`
	Error         string                       `json:"error,omitempty"`
	CheckedAtUnix int64                        `json:"checked_at_unix_ms,omitempty"`
}

type LocalContainerUpdateJobResult struct {
	PathID          string                         `json:"path_id"`
	Mode            string                         `json:"mode"`
	DevMode         bool                           `json:"dev_mode"`
	Target          LocalContainerUpdateTarget     `json:"target"`
	Summary         LocalContainerUpdateJobSummary `json:"summary"`
	Items           []LocalContainerUpdateJobItem  `json:"items"`
	CheckedAtUnix   int64                          `json:"checked_at_unix_ms,omitempty"`
	StartedAtUnix   int64                          `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64                          `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64                          `json:"completed_at_unix_ms,omitempty"`
}

type LocalContainerUpdateJobSummary struct {
	Total          int `json:"total"`
	Replaced       int `json:"replaced"`
	Skipped        int `json:"skipped"`
	Failed         int `json:"failed"`
	AlreadyCurrent int `json:"already_current"`
	Unknown        int `json:"unknown"`
}

type LocalContainerUpdateJobItem struct {
	ID                  string                   `json:"id"`
	Name                string                   `json:"name,omitempty"`
	ContainerName       string                   `json:"container_name,omitempty"`
	Runtime             string                   `json:"runtime,omitempty"`
	PreviousContainerID string                   `json:"previous_container_id,omitempty"`
	ContainerID         string                   `json:"container_id,omitempty"`
	PreviousImageRef    string                   `json:"previous_image_ref,omitempty"`
	TargetImageRef      string                   `json:"target_image_ref,omitempty"`
	TargetFingerprint   string                   `json:"target_fingerprint,omitempty"`
	Status              string                   `json:"status,omitempty"`
	State               string                   `json:"state"`
	Reason              string                   `json:"reason,omitempty"`
	Warning             string                   `json:"warning,omitempty"`
	Error               string                   `json:"error,omitempty"`
	Plan                LocalContainerUpdateItem `json:"plan,omitempty"`
}

type LocalContainerUpdateTarget struct {
	ImageRef               string `json:"image_ref,omitempty"`
	DigestRef              string `json:"digest_ref,omitempty"`
	Version                string `json:"version,omitempty"`
	Fingerprint            string `json:"fingerprint,omitempty"`
	PostRebuildImageRef    string `json:"post_rebuild_image_ref,omitempty"`
	PostRebuildFingerprint string `json:"post_rebuild_fingerprint,omitempty"`
	Commit                 string `json:"commit,omitempty"`
}

type LocalContainerUpdateSummary struct {
	Total          int `json:"total"`
	Affected       int `json:"affected"`
	AlreadyCurrent int `json:"already_current"`
	NeedsUpdate    int `json:"needs_update"`
	Unknown        int `json:"unknown"`
	Errors         int `json:"errors"`
}

type LocalContainerUpdateContract struct {
	WarningCopy      string `json:"warning_copy"`
	DismissalScope   string `json:"dismissal_scope"`
	FailureSemantics string `json:"failure_semantics"`
	Replacement      string `json:"replacement"`
}

type LocalContainerUpdateItem struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	ContainerName      string            `json:"container_name"`
	Runtime            string            `json:"runtime"`
	Status             string            `json:"status,omitempty"`
	ContainerID        string            `json:"container_id,omitempty"`
	StoredImageRef     string            `json:"stored_image_ref,omitempty"`
	CurrentImageRef    string            `json:"current_image_ref,omitempty"`
	CurrentDigestRef   string            `json:"current_digest_ref,omitempty"`
	CurrentFingerprint string            `json:"current_fingerprint,omitempty"`
	TargetImageRef     string            `json:"target_image_ref,omitempty"`
	TargetDigestRef    string            `json:"target_digest_ref,omitempty"`
	TargetVersion      string            `json:"target_version,omitempty"`
	TargetFingerprint  string            `json:"target_fingerprint,omitempty"`
	State              string            `json:"state"`
	Reason             string            `json:"reason,omitempty"`
	Error              string            `json:"error,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type CodexStatus struct {
	Provider     string              `json:"provider"`
	Configured   bool                `json:"configured"`
	AuthType     string              `json:"auth_type"`
	UpdatedAt    int64               `json:"updated_at"`
	ExpiresAt    int64               `json:"expires_at"`
	Last4        string              `json:"last4"`
	HasRefresh   bool                `json:"has_refresh_token"`
	HasAccountID bool                `json:"has_account_id"`
	StorageMode  string              `json:"storage_mode"`
	AutoDefaults *AutoDefaultsStatus `json:"auto_defaults,omitempty"`
}

type VaultStatus struct {
	Enabled        bool   `json:"enabled"`
	Unlocked       bool   `json:"unlocked"`
	UnlockRequired bool   `json:"unlock_required"`
	StorageMode    string `json:"storage_mode"`
	Warning        string `json:"warning,omitempty"`
}

type VaultImportResult struct {
	Imported int         `json:"imported"`
	Vault    VaultStatus `json:"vault"`
}

type ProviderStatus struct {
	ID              string       `json:"id"`
	Ready           bool         `json:"ready"`
	Runnable        bool         `json:"runnable"`
	Reason          string       `json:"reason"`
	RunReason       string       `json:"run_reason"`
	DefaultModel    string       `json:"default_model"`
	DefaultThinking string       `json:"default_thinking"`
	AuthMethods     []AuthMethod `json:"auth_methods"`
}

type AuthMethod struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	CredentialType string `json:"credential_type"`
	Description    string `json:"description"`
}

type AuthConnectionStatus struct {
	Connected  bool   `json:"connected"`
	Method     string `json:"method"`
	Message    string `json:"message"`
	VerifiedAt int64  `json:"verified_at"`
}

type AuthCredential struct {
	ID           string                `json:"id"`
	Provider     string                `json:"provider"`
	Active       bool                  `json:"active"`
	AuthType     string                `json:"auth_type"`
	Label        string                `json:"label"`
	Tags         []string              `json:"tags"`
	UpdatedAt    int64                 `json:"updated_at"`
	CreatedAt    int64                 `json:"created_at"`
	ExpiresAt    int64                 `json:"expires_at"`
	Last4        string                `json:"last4"`
	HasRefresh   bool                  `json:"has_refresh_token"`
	HasAccountID bool                  `json:"has_account_id"`
	StorageMode  string                `json:"storage_mode"`
	AutoDefaults *AutoDefaultsStatus   `json:"auto_defaults,omitempty"`
	Connection   *AuthConnectionStatus `json:"connection,omitempty"`
}

type AutoDefaultsStatus struct {
	Applied         bool     `json:"applied"`
	Error           string   `json:"error,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Model           string   `json:"model,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	GlobalModel     bool     `json:"global_model,omitempty"`
	Agents          []string `json:"agents,omitempty"`
	Subagents       []string `json:"subagents,omitempty"`
	UtilityProvider string   `json:"utility_provider,omitempty"`
	UtilityModel    string   `json:"utility_model,omitempty"`
	UtilityThinking string   `json:"utility_thinking,omitempty"`
}

type AuthCredentialList struct {
	Provider  string           `json:"provider"`
	Query     string           `json:"query"`
	Total     int              `json:"total"`
	Records   []AuthCredential `json:"records"`
	Providers []string         `json:"providers"`
}

type AuthCredentialDeleteCleanup struct {
	ProviderUnavailable     bool     `json:"provider_unavailable"`
	ClearedGlobalPreference bool     `json:"cleared_global_preference"`
	ClearedSessionCount     int      `json:"cleared_session_count,omitempty"`
	ClearedSessionIDs       []string `json:"cleared_session_ids,omitempty"`
	ResetAgents             []string `json:"reset_agents,omitempty"`
}

type AuthCredentialDeleteResult struct {
	OK       bool                        `json:"ok"`
	Deleted  bool                        `json:"deleted"`
	Provider string                      `json:"provider"`
	ID       string                      `json:"id"`
	Cleanup  AuthCredentialDeleteCleanup `json:"cleanup"`
}

type AuthCredentialUpsertRequest struct {
	ID           string   `json:"id,omitempty"`
	Provider     string   `json:"provider"`
	Type         string   `json:"type"`
	Label        string   `json:"label,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	APIKey       string   `json:"api_key,omitempty"`
	AccessToken  string   `json:"access_token,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	ExpiresAt    int64    `json:"expires_at,omitempty"`
	AccountID    string   `json:"account_id,omitempty"`
	Active       bool     `json:"active"`
}

type ModelPreference struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

type ModelResolved struct {
	Preference      ModelPreference `json:"preference"`
	ContextWindow   int             `json:"context_window"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	CatalogSource   string          `json:"catalog_source"`
	CatalogFetched  int64           `json:"catalog_fetched_at"`
	CatalogExpires  int64           `json:"catalog_expires_at"`
	CatalogStale    bool            `json:"catalog_stale"`
	CatalogPresent  bool            `json:"catalog_present"`
}

type ModelCatalogRecord struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ContextMode     string `json:"context_mode,omitempty"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Reasoning       bool   `json:"reasoning"`
	Source          string `json:"source"`
	FetchedAt       int64  `json:"fetched_at"`
	ExpiresAt       int64  `json:"expires_at"`
}

type ModelFavoriteRecord struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Label     string `json:"label"`
	Thinking  string `json:"thinking"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ModelFavoriteUpsertRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Label    string `json:"label,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type AgentProfile struct {
	Name                string             `json:"name"`
	Mode                string             `json:"mode"`
	Description         string             `json:"description"`
	Provider            string             `json:"provider"`
	Model               string             `json:"model"`
	Thinking            string             `json:"thinking"`
	Prompt              string             `json:"prompt"`
	ExecutionSetting    string             `json:"execution_setting,omitempty"`
	ExitPlanModeEnabled *bool              `json:"exit_plan_mode_enabled,omitempty"`
	ToolScope           *AgentToolScope    `json:"tool_scope,omitempty"`
	ToolContract        *AgentToolContract `json:"tool_contract,omitempty"`
	Enabled             bool               `json:"enabled"`
	Protected           bool               `json:"protected,omitempty"`
	UpdatedAt           int64              `json:"updated_at"`
}

type AgentToolScope struct {
	Preset        string   `json:"preset,omitempty"`
	AllowTools    []string `json:"allow_tools,omitempty"`
	DenyTools     []string `json:"deny_tools,omitempty"`
	BashPrefixes  []string `json:"bash_prefixes,omitempty"`
	InheritPolicy bool     `json:"inherit_policy,omitempty"`
}

type AgentToolConfig struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	BashPrefixes []string `json:"bash_prefixes,omitempty"`
}

type AgentToolContract struct {
	Preset        string                     `json:"preset,omitempty"`
	Tools         map[string]AgentToolConfig `json:"tools,omitempty"`
	InheritPolicy bool                       `json:"inherit_policy,omitempty"`
}

type ProviderDefaultsPreview struct {
	Provider             string   `json:"provider,omitempty"`
	PrimaryAgent         string   `json:"primary_agent,omitempty"`
	PrimaryModel         string   `json:"primary_model,omitempty"`
	PrimaryThinking      string   `json:"primary_thinking,omitempty"`
	UtilityProvider      string   `json:"utility_provider,omitempty"`
	UtilityModel         string   `json:"utility_model,omitempty"`
	UtilityThinking      string   `json:"utility_thinking,omitempty"`
	UtilityAgents        []string `json:"utility_agents,omitempty"`
	AffectedAgents       []string `json:"affected_agents,omitempty"`
	OutOfSyncAgents      []string `json:"out_of_sync_agents,omitempty"`
	InheritingAgents     []string `json:"inheriting_agents,omitempty"`
	StaleInheritedAgents []string `json:"stale_inherited_agents,omitempty"`
}

type AgentState struct {
	Profiles                []AgentProfile              `json:"profiles"`
	CustomTools             []AgentCustomToolDefinition `json:"custom_tools,omitempty"`
	ActivePrimary           string                      `json:"active_primary"`
	ActiveSubagent          map[string]string           `json:"active_subagent"`
	Version                 int64                       `json:"version"`
	ProviderDefaultsPreview *ProviderDefaultsPreview    `json:"-"`
}

type AgentCustomToolDefinition struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	UpdatedAt   int64  `json:"updated_at"`
}

type RestoreAgentsDefaultsResult struct {
	Profiles                []AgentProfile           `json:"profiles"`
	ActivePrimary           string                   `json:"active_primary"`
	ActiveSubagent          map[string]string        `json:"active_subagent"`
	Version                 int64                    `json:"version"`
	ProviderDefaultsPreview *ProviderDefaultsPreview `json:"provider_defaults_preview,omitempty"`
}

type AgentUpsertRequest struct {
	Name                string                      `json:"name"`
	Mode                string                      `json:"mode,omitempty"`
	Description         string                      `json:"description,omitempty"`
	Provider            *string                     `json:"provider,omitempty"`
	Model               *string                     `json:"model,omitempty"`
	Thinking            *string                     `json:"thinking,omitempty"`
	Prompt              string                      `json:"prompt,omitempty"`
	ExecutionSetting    string                      `json:"execution_setting,omitempty"`
	ExitPlanModeEnabled *bool                       `json:"exit_plan_mode_enabled,omitempty"`
	ToolScope           *AgentToolScope             `json:"tool_scope,omitempty"`
	ToolContract        *AgentToolContract          `json:"tool_contract,omitempty"`
	Enabled             *bool                       `json:"enabled,omitempty"`
	CustomTools         []AgentCustomToolDefinition `json:"custom_tools,omitempty"`
	AssignCustomTools   []string                    `json:"assign_custom_tools,omitempty"`
}

type WorkspaceResolution struct {
	RequestedPath string `json:"requested_path"`
	ResolvedPath  string `json:"resolved_path"`
	WorkspacePath string `json:"workspace_path"`
	WorkspaceName string `json:"workspace_name"`
	ThemeID       string `json:"theme_id,omitempty"`
}

type WorkspaceEntry struct {
	Path           string   `json:"path"`
	WorkspaceName  string   `json:"workspace_name"`
	ThemeID        string   `json:"theme_id,omitempty"`
	Directories    []string `json:"directories"`
	IsGitRepo      bool     `json:"is_git_repo"`
	SortIndex      int      `json:"sort_index"`
	AddedAt        int64    `json:"added_at"`
	UpdatedAt      int64    `json:"updated_at"`
	LastSelectedAt int64    `json:"last_selected_at"`
	Active         bool     `json:"active"`
}

type WorkspaceOverviewWorkspace struct {
	WorkspaceEntry
	Sessions []SessionSummary `json:"sessions"`
}

type WorkspaceOverviewResponse struct {
	OK               bool                         `json:"ok"`
	CurrentWorkspace *WorkspaceResolution         `json:"current_workspace,omitempty"`
	Workspaces       []WorkspaceOverviewWorkspace `json:"workspaces"`
	Directories      []WorkspaceDiscoverEntry     `json:"directories"`
	Cursor           int                          `json:"cursor,omitempty"`
	Limit            int                          `json:"limit,omitempty"`
	NextCursor       int                          `json:"next_cursor,omitempty"`
	HasMore          bool                         `json:"has_more,omitempty"`
	TotalWorkspaces  int                          `json:"total_workspaces,omitempty"`
}

type RuleSource struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type SkillSource struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type InvalidSkillSource struct {
	DirectoryName string `json:"directory_name"`
	DeclaredName  string `json:"declared_name,omitempty"`
	Path          string `json:"path"`
	Scope         string `json:"scope"`
	Origin        string `json:"origin"`
	Error         string `json:"error"`
}

type ContextReport struct {
	RequestedPath string               `json:"requested_path"`
	ResolvedPath  string               `json:"resolved_path"`
	Rules         []RuleSource         `json:"rules"`
	Skills        []SkillSource        `json:"skills"`
	InvalidSkills []InvalidSkillSource `json:"invalid_skills,omitempty"`
}

type SessionLifecycleSnapshot struct {
	SessionID      string `json:"session_id"`
	RunID          string `json:"run_id,omitempty"`
	Active         bool   `json:"active"`
	Phase          string `json:"phase,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	EndedAt        int64  `json:"ended_at,omitempty"`
	UpdatedAt      int64  `json:"updated_at,omitempty"`
	Generation     uint64 `json:"generation,omitempty"`
	StopReason     string `json:"stop_reason,omitempty"`
	Error          string `json:"error,omitempty"`
	OwnerTransport string `json:"owner_transport,omitempty"`
}

type SessionCreateOptions struct {
	Title                string
	WorkspacePath        string
	HostWorkspacePath    string
	RuntimeWorkspacePath string
	WorkspaceName        string
	Mode                 string
	AgentName            string
	Metadata             map[string]any
	Preference           ModelPreference
	WorktreeMode         string
}

type SessionSummary struct {
	ID                     string                    `json:"id"`
	WorkspacePath          string                    `json:"workspace_path"`
	WorkspaceName          string                    `json:"workspace_name"`
	Title                  string                    `json:"title"`
	Mode                   string                    `json:"mode"`
	Warning                string                    `json:"warning,omitempty"`
	Preference             ModelPreference           `json:"preference,omitempty"`
	WorktreeEnabled        bool                      `json:"worktree_enabled,omitempty"`
	WorktreeRootPath       string                    `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch     string                    `json:"worktree_base_branch,omitempty"`
	WorktreeBranch         string                    `json:"worktree_branch,omitempty"`
	GitBranch              string                    `json:"git_branch,omitempty"`
	GitHasGit              bool                      `json:"git_has_git,omitempty"`
	GitClean               bool                      `json:"git_clean,omitempty"`
	GitDirtyCount          int                       `json:"git_dirty_count,omitempty"`
	GitStagedCount         int                       `json:"git_staged_count,omitempty"`
	GitModifiedCount       int                       `json:"git_modified_count,omitempty"`
	GitUntrackedCount      int                       `json:"git_untracked_count,omitempty"`
	GitConflictCount       int                       `json:"git_conflict_count,omitempty"`
	GitAheadCount          int                       `json:"git_ahead_count,omitempty"`
	GitBehindCount         int                       `json:"git_behind_count,omitempty"`
	GitCommitDetected      bool                      `json:"git_commit_detected,omitempty"`
	GitCommitCount         int                       `json:"git_commit_count,omitempty"`
	Metadata               map[string]any            `json:"metadata,omitempty"`
	CreatedAt              int64                     `json:"created_at"`
	UpdatedAt              int64                     `json:"updated_at"`
	MessageCount           int                       `json:"message_count"`
	LastMessageAt          int64                     `json:"last_message_at"`
	PendingPermissionCount int                       `json:"pending_permission_count"`
	Lifecycle              *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
}

type SessionPlan struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	Title         string `json:"title"`
	Plan          string `json:"plan"`
	Status        string `json:"status"`
	ApprovalState string `json:"approval_state"`
	Active        bool   `json:"active"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type SessionPlanUpsertRequest struct {
	ID            string `json:"id,omitempty"`
	PlanID        string `json:"plan_id,omitempty"`
	Title         string `json:"title,omitempty"`
	Plan          string `json:"plan,omitempty"`
	Status        string `json:"status,omitempty"`
	ApprovalState string `json:"approval_state,omitempty"`
	Activate      *bool  `json:"activate,omitempty"`
}

type PermissionRecord struct {
	ID                    string `json:"id"`
	SessionID             string `json:"session_id"`
	RunID                 string `json:"run_id"`
	Step                  int    `json:"step,omitempty"`
	CallID                string `json:"call_id"`
	ToolName              string `json:"tool_name"`
	ToolArguments         string `json:"tool_arguments"`
	ApprovedArguments     string `json:"approved_arguments,omitempty"`
	Requirement           string `json:"requirement"`
	Mode                  string `json:"mode"`
	Status                string `json:"status"`
	Decision              string `json:"decision"`
	Reason                string `json:"reason"`
	PermissionRequestedAt int64  `json:"permission_requested_at,omitempty"`
	ResolvedAt            int64  `json:"resolved_at"`
	ExecutionStatus       string `json:"execution_status,omitempty"`
	Output                string `json:"output,omitempty"`
	Error                 string `json:"error,omitempty"`
	DurationMS            int64  `json:"duration_ms,omitempty"`
	StartedAt             int64  `json:"started_at,omitempty"`
	CompletedAt           int64  `json:"completed_at,omitempty"`
	CreatedAt             int64  `json:"created_at"`
	UpdatedAt             int64  `json:"updated_at"`
	SavedRulePreview      string `json:"saved_rule_preview,omitempty"`
}

type SessionMessage struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	GlobalSeq uint64         `json:"global_seq"`
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt int64          `json:"created_at"`
}

type SessionCodexConfig struct {
	ServiceTier            string `json:"service_tier"`
	ContextMode            string `json:"context_mode"`
	EffectiveContextWindow int    `json:"effective_context_window"`
	UpdatedAt              int64  `json:"updated_at"`
}

type SessionCodexConfigUpdateRequest struct {
	ServiceTier *string `json:"service_tier,omitempty"`
	ContextMode *string `json:"context_mode,omitempty"`
}

type SessionTurnUsage struct {
	SessionID        string           `json:"session_id"`
	RunID            string           `json:"run_id"`
	Provider         string           `json:"provider"`
	Model            string           `json:"model"`
	Source           string           `json:"source"`
	Transport        string           `json:"transport,omitempty"`
	ConnectedViaWS   *bool            `json:"connected_via_websocket,omitempty"`
	ContextWindow    int              `json:"context_window"`
	Steps            int              `json:"steps"`
	InputTokens      int64            `json:"input_tokens"`
	OutputTokens     int64            `json:"output_tokens"`
	ThinkingTokens   int64            `json:"thinking_tokens"`
	CacheReadTokens  int64            `json:"cache_read_tokens"`
	CacheWriteTokens int64            `json:"cache_write_tokens"`
	TotalTokens      int64            `json:"total_tokens"`
	APIUsageRaw      map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath  string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory  []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths    []string         `json:"api_usage_paths,omitempty"`
	CreatedAt        int64            `json:"created_at"`
	UpdatedAt        int64            `json:"updated_at"`
}

type SessionUsageSummary struct {
	SessionID          string `json:"session_id"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Source             string `json:"source"`
	LastTransport      string `json:"last_transport,omitempty"`
	LastConnectedViaWS *bool  `json:"last_connected_via_websocket,omitempty"`
	ContextWindow      int    `json:"context_window"`
	TurnCount          int    `json:"turn_count"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	ThinkingTokens     int64  `json:"thinking_tokens"`
	CacheReadTokens    int64  `json:"cache_read_tokens"`
	CacheWriteTokens   int64  `json:"cache_write_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	RemainingTokens    int64  `json:"remaining_tokens"`
	LastRunID          string `json:"last_run_id"`
	UpdatedAt          int64  `json:"updated_at"`
}

type SandboxCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type SandboxStatus struct {
	Enabled      bool           `json:"enabled"`
	UpdatedAt    int64          `json:"updated_at"`
	Ready        bool           `json:"ready"`
	Summary      string         `json:"summary"`
	Checks       []SandboxCheck `json:"checks"`
	Remediation  []string       `json:"remediation"`
	SetupCommand string         `json:"setup_command"`
}

type WorktreeSettings struct {
	WorkspacePath    string `json:"workspace_path,omitempty"`
	Enabled          bool   `json:"enabled"`
	UseCurrentBranch bool   `json:"use_current_branch"`
	BaseBranch       string `json:"base_branch,omitempty"`
	BranchName       string `json:"branch_name,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type WorktreeSettingsUpdateRequest struct {
	WorkspacePath    string  `json:"workspace_path,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	UseCurrentBranch *bool   `json:"use_current_branch,omitempty"`
	BaseBranch       string  `json:"base_branch,omitempty"`
	BranchName       *string `json:"branch_name,omitempty"`
}

type ManageWorktreeItem struct {
	Commit                  string `json:"commit,omitempty"`
	CommitShort             string `json:"commit_short,omitempty"`
	CommittedAt             string `json:"committed_at,omitempty"`
	Subject                 string `json:"subject,omitempty"`
	BranchName              string `json:"branch_name,omitempty"`
	MergedIntoCurrentBranch bool   `json:"merged_into_current_branch,omitempty"`
}

type ManageWorktreeResponse struct {
	Status           string               `json:"status"`
	Action           string               `json:"action"`
	Workspace        map[string]any       `json:"workspace,omitempty"`
	WorktreeConfig   map[string]any       `json:"worktree_config,omitempty"`
	CurrentBranch    string               `json:"current_branch,omitempty"`
	Items            []ManageWorktreeItem `json:"items,omitempty"`
	Total            int                  `json:"total,omitempty"`
	Returned         int                  `json:"returned,omitempty"`
	Cursor           int                  `json:"cursor,omitempty"`
	Limit            int                  `json:"limit,omitempty"`
	NextCursor       int                  `json:"next_cursor,omitempty"`
	HasMore          bool                 `json:"has_more,omitempty"`
	SupportedActions []string             `json:"supported_actions,omitempty"`
	Instructions     string               `json:"instructions,omitempty"`
	Examples         []map[string]any     `json:"examples,omitempty"`
}

type UIThemePalette struct {
	Background     string `json:"background,omitempty"`
	Panel          string `json:"panel,omitempty"`
	Element        string `json:"element,omitempty"`
	Border         string `json:"border,omitempty"`
	BorderActive   string `json:"border_active,omitempty"`
	Text           string `json:"text,omitempty"`
	TextMuted      string `json:"text_muted,omitempty"`
	Primary        string `json:"primary,omitempty"`
	Secondary      string `json:"secondary,omitempty"`
	Accent         string `json:"accent,omitempty"`
	Success        string `json:"success,omitempty"`
	Warning        string `json:"warning,omitempty"`
	Error          string `json:"error,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	PromptCursorBG string `json:"prompt_cursor_bg,omitempty"`
	PromptCursorFG string `json:"prompt_cursor_fg,omitempty"`
	CodeBackground string `json:"code_background,omitempty"`
	CodeText       string `json:"code_text,omitempty"`
	CodeKeyword    string `json:"code_keyword,omitempty"`
	CodeType       string `json:"code_type,omitempty"`
	CodeString     string `json:"code_string,omitempty"`
	CodeNumber     string `json:"code_number,omitempty"`
	CodeComment    string `json:"code_comment,omitempty"`
	CodeFunction   string `json:"code_function,omitempty"`
	CodeOperator   string `json:"code_operator,omitempty"`
}

type UIThemeCustomTheme struct {
	ID      string         `json:"id"`
	Name    string         `json:"name,omitempty"`
	Palette UIThemePalette `json:"palette,omitempty"`
}

type UIThemeSettings struct {
	ActiveID     string               `json:"active_id"`
	CustomThemes []UIThemeCustomTheme `json:"custom_themes,omitempty"`
}

type UIInputSettings struct {
	MouseEnabled bool              `json:"mouse_enabled"`
	Keybinds     map[string]string `json:"keybinds,omitempty"`
}

type UIChatToolStreamSettings struct {
	ShowAnchor    bool     `json:"show_anchor"`
	PulseFrames   []string `json:"pulse_frames,omitempty"`
	RunningSymbol string   `json:"running_symbol,omitempty"`
	SuccessSymbol string   `json:"success_symbol,omitempty"`
	ErrorSymbol   string   `json:"error_symbol,omitempty"`
}

type UIChatSettings struct {
	ShowHeader            bool                     `json:"show_header"`
	ThinkingTags          bool                     `json:"thinking_tags"`
	DefaultNewSessionMode string                   `json:"default_new_session_mode,omitempty"`
	ToolStream            UIChatToolStreamSettings `json:"tool_stream,omitempty"`
}

type UISwarmingSettings struct {
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// UISwarmSettings is the persisted machine/device identity branch.
// Keep this separate from UISwarmingSettings:
// - UISwarmingSettings is for the run/activity indicator copy.
// - UISwarmSettings is for the user-editable machine name shared by TUI /swarm and desktop settings.
// Do not merge these concepts in future edits.
type UISwarmSettings struct {
	Name             string   `json:"name,omitempty"`
	RemoteSSHTargets []string `json:"remote_ssh_targets,omitempty"`
}

type UIUpdateSettings struct {
	LocalContainerWarningDismissed bool `json:"local_container_warning_dismissed,omitempty"`
}

type UISettings struct {
	Theme     UIThemeSettings    `json:"theme,omitempty"`
	Input     UIInputSettings    `json:"input,omitempty"`
	Chat      UIChatSettings     `json:"chat,omitempty"`
	Swarming  UISwarmingSettings `json:"swarming,omitempty"`
	Swarm     UISwarmSettings    `json:"swarm,omitempty"`
	Updates   UIUpdateSettings   `json:"updates,omitempty"`
	UpdatedAt int64              `json:"updated_at"`
}

type MCPServer struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	Source    string            `json:"source"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

type MCPServerUpsertRequest struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	Transport string            `json:"transport,omitempty"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
	Source    string            `json:"source,omitempty"`
}

type SessionRunResult struct {
	SessionID        string               `json:"session_id"`
	Agent            string               `json:"agent"`
	Model            string               `json:"model"`
	Thinking         string               `json:"thinking"`
	ReasoningSummary string               `json:"reasoning_summary"`
	Steps            int                  `json:"steps"`
	ToolCallCount    int                  `json:"tool_call_count"`
	TurnUsage        *SessionTurnUsage    `json:"turn_usage,omitempty"`
	UsageSummary     *SessionUsageSummary `json:"usage_summary,omitempty"`
	UserMessage      SessionMessage       `json:"user_message"`
	ToolMessages     []SessionMessage     `json:"tool_messages"`
	Commentary       []SessionMessage     `json:"commentary"`
	AssistantMessage SessionMessage       `json:"assistant_message"`
	TargetKind       string               `json:"target_kind,omitempty"`
	TargetName       string               `json:"target_name,omitempty"`
}

type RunToolScope struct {
	Preset        string   `json:"preset,omitempty"`
	AllowTools    []string `json:"allow_tools,omitempty"`
	DenyTools     []string `json:"deny_tools,omitempty"`
	BashPrefixes  []string `json:"bash_prefixes,omitempty"`
	InheritPolicy bool     `json:"inherit_policy,omitempty"`
}

type RunExecutionContext struct {
	WorkspacePath      string `json:"workspace_path,omitempty"`
	CWD                string `json:"cwd,omitempty"`
	WorktreeMode       string `json:"worktree_mode,omitempty"`
	WorktreeRootPath   string `json:"worktree_root_path,omitempty"`
	WorktreeBranch     string `json:"worktree_branch,omitempty"`
	WorktreeBaseBranch string `json:"worktree_base_branch,omitempty"`
}

type BackgroundRunAccepted struct {
	OK             bool   `json:"ok"`
	SessionID      string `json:"session_id"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	Background     bool   `json:"background,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	TargetName     string `json:"target_name,omitempty"`
	OwnerTransport string `json:"owner_transport,omitempty"`
}

type RunSessionOptions struct {
	Compact          bool                 `json:"compact,omitempty"`
	Background       bool                 `json:"background,omitempty"`
	TargetKind       string               `json:"target_kind,omitempty"`
	TargetName       string               `json:"target_name,omitempty"`
	ToolScope        *RunToolScope        `json:"tool_scope,omitempty"`
	ExecutionContext *RunExecutionContext `json:"execution_context,omitempty"`
}

type SessionRunStreamEvent struct {
	Type         string                    `json:"type"`
	SessionID    string                    `json:"session_id,omitempty"`
	RunID        string                    `json:"run_id,omitempty"`
	Seq          uint64                    `json:"seq,omitempty"`
	Agent        string                    `json:"agent,omitempty"`
	Step         int                       `json:"step,omitempty"`
	Delta        string                    `json:"delta,omitempty"`
	Summary      string                    `json:"summary,omitempty"`
	ToolName     string                    `json:"tool_name,omitempty"`
	CallID       string                    `json:"call_id,omitempty"`
	Arguments    string                    `json:"arguments,omitempty"`
	Output       string                    `json:"output,omitempty"`
	RawOutput    string                    `json:"raw_output,omitempty"`
	Error        string                    `json:"error,omitempty"`
	DurationMS   int64                     `json:"duration_ms,omitempty"`
	Message      *SessionMessage           `json:"message,omitempty"`
	Permission   *PermissionRecord         `json:"permission,omitempty"`
	TurnUsage    *SessionTurnUsage         `json:"turn_usage,omitempty"`
	UsageSummary *SessionUsageSummary      `json:"usage_summary,omitempty"`
	Title        string                    `json:"title,omitempty"`
	TitleStage   string                    `json:"title_stage,omitempty"`
	Warning      string                    `json:"warning,omitempty"`
	Branch       string                    `json:"branch,omitempty"`
	Lifecycle    *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	Result       SessionRunResult          `json:"result"`
}

type VoiceStatus struct {
	PathID   string         `json:"path_id"`
	STT      VoiceSTTStatus `json:"stt"`
	TTS      VoiceTTSStatus `json:"tts"`
	Profiles []VoiceProfile `json:"profiles"`
	Config   VoiceConfig    `json:"config"`
}

type VoiceProfile struct {
	ID                string            `json:"id"`
	Label             string            `json:"label"`
	Adapter           string            `json:"adapter"`
	STTModel          string            `json:"stt_model"`
	STTLanguage       string            `json:"stt_language"`
	TTSVoice          string            `json:"tts_voice"`
	Options           map[string]string `json:"options"`
	UpdatedAt         int64             `json:"updated_at"`
	ActiveSTT         bool              `json:"active_stt"`
	ActiveTTS         bool              `json:"active_tts"`
	AdapterConfigured bool              `json:"adapter_configured"`
	AdapterReason     string            `json:"adapter_reason"`
}

type VoiceConfig struct {
	STTProfile  string `json:"stt_profile"`
	STTProvider string `json:"stt_provider"`
	STTModel    string `json:"stt_model"`
	STTLanguage string `json:"stt_language"`
	DeviceID    string `json:"device_id"`
	TTSProfile  string `json:"tts_profile"`
	TTSProvider string `json:"tts_provider"`
	TTSVoice    string `json:"tts_voice"`
	UpdatedAt   int64  `json:"updated_at"`
}

type VoiceSTTStatus struct {
	Profile    string                `json:"profile"`
	Provider   string                `json:"provider"`
	Model      string                `json:"model"`
	Configured bool                  `json:"configured"`
	Reason     string                `json:"reason"`
	Providers  []VoiceSTTProviderRef `json:"providers"`
}

type VoiceSTTProviderRef struct {
	ID           string   `json:"id"`
	Configured   bool     `json:"configured"`
	Reason       string   `json:"reason"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
}

type VoiceTTSStatus struct {
	Provider   string                `json:"provider"`
	Voice      string                `json:"voice"`
	Configured bool                  `json:"configured"`
	Reason     string                `json:"reason"`
	Providers  []VoiceTTSProviderRef `json:"providers"`
}

type VoiceTTSProviderRef struct {
	ID         string `json:"id"`
	Configured bool   `json:"configured"`
	Reason     string `json:"reason"`
}

type VoiceDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Default  bool   `json:"default"`
	Selected bool   `json:"selected"`
	Backend  string `json:"backend"`
}

type VoiceConfigUpdateRequest struct {
	STTProfile  *string `json:"stt_profile,omitempty"`
	STTProvider *string `json:"stt_provider,omitempty"`
	STTModel    *string `json:"stt_model,omitempty"`
	STTLanguage *string `json:"stt_language,omitempty"`
	DeviceID    *string `json:"device_id,omitempty"`
	TTSProfile  *string `json:"tts_profile,omitempty"`
	TTSProvider *string `json:"tts_provider,omitempty"`
	TTSVoice    *string `json:"tts_voice,omitempty"`
}

type VoiceProfileUpsertRequest struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Adapter     string            `json:"adapter"`
	STTModel    string            `json:"stt_model"`
	STTLanguage string            `json:"stt_language"`
	TTSVoice    string            `json:"tts_voice"`
	Options     map[string]string `json:"options"`
}

type VoiceTestSTTRequest struct {
	Profile  string `json:"profile,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Language string `json:"language,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
}

type VoiceTestSTTResult struct {
	PathID        string `json:"path_id"`
	Profile       string `json:"profile"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Language      string `json:"language"`
	Text          string `json:"text"`
	Seconds       int    `json:"seconds"`
	DeviceID      string `json:"device_id"`
	RecordBackend string `json:"record_backend"`
	AudioBytes    int    `json:"audio_bytes"`
	DurationMS    int64  `json:"duration_ms"`
}

type STTTranscribeRequest struct {
	Profile  string `json:"profile,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Language string `json:"language,omitempty"`
	Audio    []byte `json:"-"`
}

type STTTranscribeResult struct {
	PathID     string `json:"path_id"`
	Profile    string `json:"profile"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Language   string `json:"language"`
	Text       string `json:"text"`
	AudioBytes int    `json:"audio_bytes"`
	DurationMS int64  `json:"duration_ms"`
}

func New(baseURL string) *API {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:7781"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &API{
		baseURL: baseURL,
		// Do not set a global client timeout here. Long-running requests such as
		// /v1/sessions/{id}/run must be controlled by per-request context deadlines.
		http: &http.Client{},
	}
}

func (c *API) BaseURL() string {
	return c.baseURL
}

func (c *API) SetToken(token string) {
	c.mu.Lock()
	c.token = strings.TrimSpace(token)
	c.mu.Unlock()
}

func (c *API) SetBypassPermissions(_ bool) {
}

func (c *API) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *API) EnsureLocalAuth(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *API) requestTarget() (string, *http.Client, string) {
	if socketPath := resolveLocalTransportSocketPath(c.baseURL, c.Token()); socketPath != "" {
		return localTransportBaseURL, newLocalTransportHTTPClient(socketPath), socketPath
	}
	return c.baseURL, c.http, ""
}

func resolveLocalTransportSocketPath(baseURL, token string) string {
	if strings.TrimSpace(token) != "" || !isLoopbackBaseURL(baseURL) {
		return ""
	}
	seen := map[string]struct{}{}
	for _, candidate := range localTransportSocketCandidates() {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func localTransportSocketCandidates() []string {
	candidates := make([]string, 0, 2)
	if configured := strings.TrimSpace(os.Getenv(localTransportSocketEnv)); configured != "" {
		candidates = append(candidates, configured)
	}
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "local-transport", "api.sock"))
	}
	return candidates
}

func isLoopbackBaseURL(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newLocalTransportHTTPClient(socketPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	return &http.Client{Transport: transport}
}

func (c *API) GetHealth(ctx context.Context) (HealthStatus, error) {
	var status HealthStatus
	if err := c.getJSON(ctx, "/healthz", &status, false); err != nil {
		return HealthStatus{}, err
	}
	return status, nil
}

func (c *API) Shutdown(ctx context.Context, reason string) error {
	payload := map[string]string{}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = strings.TrimSpace(reason)
	}
	return c.postJSON(ctx, "/v1/system/shutdown", payload, nil, true)
}

func (c *API) GetUpdateStatus(ctx context.Context) (UpdateStatus, error) {
	var status UpdateStatus
	if err := c.getJSON(ctx, "/v1/update/status", &status, true); err != nil {
		return UpdateStatus{}, err
	}
	return status, nil
}

func (c *API) ApplyUpdate(ctx context.Context) (UpdateApplyPlan, error) {
	var plan UpdateApplyPlan
	if err := c.postJSON(ctx, "/v1/update/apply", map[string]any{}, &plan, true); err != nil {
		return UpdateApplyPlan{}, err
	}
	return plan, nil
}

func (c *API) GetLocalContainerUpdatePlan(ctx context.Context, devMode *bool, targetVersion string) (LocalContainerUpdatePlan, error) {
	return c.GetLocalContainerUpdatePlanWithPostRebuild(ctx, devMode, targetVersion, false)
}

func (c *API) GetLocalContainerUpdatePlanWithPostRebuild(ctx context.Context, devMode *bool, targetVersion string, postRebuildCheck bool) (LocalContainerUpdatePlan, error) {
	path := "/v1/update/local-containers"
	query := url.Values{}
	if devMode != nil {
		query.Set("dev_mode", strconv.FormatBool(*devMode))
	}
	if strings.TrimSpace(targetVersion) != "" {
		query.Set("target_version", strings.TrimSpace(targetVersion))
	}
	if postRebuildCheck {
		query.Set("post_rebuild_check", "true")
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var plan LocalContainerUpdatePlan
	if err := c.getJSON(ctx, path, &plan, true); err != nil {
		return LocalContainerUpdatePlan{}, err
	}
	return plan, nil
}

func (c *API) GetUISettings(ctx context.Context) (UISettings, error) {
	var settings UISettings
	if err := c.getJSON(ctx, "/v1/ui/settings", &settings, true); err != nil {
		return UISettings{}, err
	}
	return settings, nil
}

func (c *API) UpdateUISettings(ctx context.Context, settings UISettings) (UISettings, error) {
	var saved UISettings
	if err := c.postJSON(ctx, "/v1/ui/settings", settings, &saved, true); err != nil {
		return UISettings{}, err
	}
	return saved, nil
}

func (c *API) GetVaultStatus(ctx context.Context) (VaultStatus, error) {
	var status VaultStatus
	if err := c.getJSON(ctx, "/v1/vault", &status, true); err != nil {
		return VaultStatus{}, err
	}
	return status, nil
}

func (c *API) EnableVault(ctx context.Context, password string) (VaultStatus, error) {
	var status VaultStatus
	if err := c.postJSON(ctx, "/v1/vault/enable", map[string]string{"password": strings.TrimSpace(password)}, &status, true); err != nil {
		return VaultStatus{}, err
	}
	return status, nil
}

func (c *API) UnlockVault(ctx context.Context, password string) (VaultStatus, error) {
	var status VaultStatus
	if err := c.postJSON(ctx, "/v1/vault/unlock", map[string]string{"password": strings.TrimSpace(password)}, &status, true); err != nil {
		return VaultStatus{}, err
	}
	return status, nil
}

func (c *API) LockVault(ctx context.Context) (VaultStatus, error) {
	var status VaultStatus
	if err := c.postJSON(ctx, "/v1/vault/lock", map[string]string{}, &status, true); err != nil {
		return VaultStatus{}, err
	}
	return status, nil
}

func (c *API) DisableVault(ctx context.Context, password string) (VaultStatus, error) {
	var status VaultStatus
	if err := c.postJSON(ctx, "/v1/vault/disable", map[string]string{"password": strings.TrimSpace(password)}, &status, true); err != nil {
		return VaultStatus{}, err
	}
	return status, nil
}

func (c *API) ExportVaultCredentials(ctx context.Context, password, vaultPassword string) ([]byte, int, error) {
	var resp struct {
		Exported int    `json:"exported"`
		Bundle   []byte `json:"bundle"`
	}
	payload := map[string]string{"password": strings.TrimSpace(password)}
	if strings.TrimSpace(vaultPassword) != "" {
		payload["vault_password"] = strings.TrimSpace(vaultPassword)
	}
	if err := c.postJSON(ctx, "/v1/vault/export", payload, &resp, true); err != nil {
		return nil, 0, err
	}
	return resp.Bundle, resp.Exported, nil
}

func (c *API) ImportVaultCredentials(ctx context.Context, password, vaultPassword string, bundle []byte) (VaultImportResult, error) {
	var resp VaultImportResult
	payload := map[string]any{
		"password": strings.TrimSpace(password),
		"bundle":   bundle,
	}
	if strings.TrimSpace(vaultPassword) != "" {
		payload["vault_password"] = strings.TrimSpace(vaultPassword)
	}
	if err := c.postJSON(ctx, "/v1/vault/import", payload, &resp, true); err != nil {
		return VaultImportResult{}, err
	}
	return resp, nil
}

func (c *API) GetCodexStatus(ctx context.Context) (CodexStatus, error) {
	var status CodexStatus
	if err := c.getJSON(ctx, "/v1/auth/codex", &status, true); err != nil {
		return CodexStatus{}, err
	}
	return status, nil
}

func (c *API) SetCodexAPIKey(ctx context.Context, key string) (CodexStatus, error) {
	var status CodexStatus
	req := map[string]string{
		"type":    "api",
		"api_key": strings.TrimSpace(key),
	}
	if err := c.postJSON(ctx, "/v1/auth/codex", req, &status, true); err != nil {
		return CodexStatus{}, err
	}
	return status, nil
}

type CodexOAuthStartRequest struct {
	Provider string `json:"provider,omitempty"`
	Label    string `json:"label,omitempty"`
	Active   bool   `json:"active"`
	Method   string `json:"method,omitempty"`
}

type CodexOAuthSessionStatus struct {
	SessionID  string          `json:"session_id"`
	Provider   string          `json:"provider"`
	Method     string          `json:"method"`
	Label      string          `json:"label,omitempty"`
	Active     bool            `json:"active"`
	AuthURL    string          `json:"auth_url,omitempty"`
	Status     string          `json:"status"`
	Error      string          `json:"error,omitempty"`
	Credential *AuthCredential `json:"credential,omitempty"`
}

type CodexOAuthCompleteRequest struct {
	SessionID     string `json:"session_id"`
	CallbackInput string `json:"callback_input"`
}

func (c *API) StartCodexOAuth(ctx context.Context, req CodexOAuthStartRequest) (CodexOAuthSessionStatus, error) {
	var out CodexOAuthSessionStatus
	if err := c.postJSON(ctx, "/v1/auth/codex/oauth/start", req, &out, true); err != nil {
		return CodexOAuthSessionStatus{}, err
	}
	return out, nil
}

func (c *API) GetCodexOAuthStatus(ctx context.Context, sessionID string) (CodexOAuthSessionStatus, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return CodexOAuthSessionStatus{}, errors.New("session id is required")
	}
	path := "/v1/auth/codex/oauth/status?session_id=" + url.QueryEscape(sessionID)
	var out CodexOAuthSessionStatus
	if err := c.getJSON(ctx, path, &out, true); err != nil {
		return CodexOAuthSessionStatus{}, err
	}
	return out, nil
}

func (c *API) CompleteCodexOAuth(ctx context.Context, req CodexOAuthCompleteRequest) (CodexOAuthSessionStatus, error) {
	var out CodexOAuthSessionStatus
	if err := c.postJSON(ctx, "/v1/auth/codex/oauth/complete", req, &out, true); err != nil {
		return CodexOAuthSessionStatus{}, err
	}
	return out, nil
}

func (c *API) ListProviders(ctx context.Context) ([]ProviderStatus, error) {
	var resp struct {
		Providers []ProviderStatus `json:"providers"`
	}
	if err := c.getJSON(ctx, "/v1/providers", &resp, true); err != nil {
		return nil, err
	}
	return resp.Providers, nil
}

func (c *API) GetVoiceStatus(ctx context.Context) (VoiceStatus, error) {
	var resp struct {
		OK     bool        `json:"ok"`
		PathID string      `json:"path_id"`
		Status VoiceStatus `json:"status"`
	}
	if err := c.getJSON(ctx, "/v1/voice/status", &resp, true); err != nil {
		return VoiceStatus{}, err
	}
	if strings.TrimSpace(resp.Status.PathID) == "" {
		resp.Status.PathID = strings.TrimSpace(resp.PathID)
	}
	return resp.Status, nil
}

func (c *API) ListVoiceProfiles(ctx context.Context) ([]VoiceProfile, error) {
	var resp struct {
		OK       bool           `json:"ok"`
		PathID   string         `json:"path_id"`
		Profiles []VoiceProfile `json:"profiles"`
	}
	if err := c.getJSON(ctx, "/v1/voice/profiles", &resp, true); err != nil {
		return nil, err
	}
	return resp.Profiles, nil
}

func (c *API) UpsertVoiceProfile(ctx context.Context, req VoiceProfileUpsertRequest) (VoiceProfile, error) {
	var resp struct {
		OK      bool         `json:"ok"`
		PathID  string       `json:"path_id"`
		Profile VoiceProfile `json:"profile"`
	}
	if err := c.postJSON(ctx, "/v1/voice/profiles/upsert", req, &resp, true); err != nil {
		return VoiceProfile{}, err
	}
	return resp.Profile, nil
}

func (c *API) DeleteVoiceProfile(ctx context.Context, id string) error {
	payload := map[string]string{"id": strings.TrimSpace(id)}
	return c.postJSON(ctx, "/v1/voice/profiles/delete", payload, nil, true)
}

func (c *API) UpdateVoiceConfig(ctx context.Context, req VoiceConfigUpdateRequest) (VoiceStatus, error) {
	var resp struct {
		OK     bool        `json:"ok"`
		PathID string      `json:"path_id"`
		Status VoiceStatus `json:"status"`
	}
	if err := c.postJSON(ctx, "/v1/voice/config", req, &resp, true); err != nil {
		return VoiceStatus{}, err
	}
	if strings.TrimSpace(resp.Status.PathID) == "" {
		resp.Status.PathID = strings.TrimSpace(resp.PathID)
	}
	return resp.Status, nil
}

func (c *API) ListVoiceDevices(ctx context.Context) ([]VoiceDevice, error) {
	var resp struct {
		OK      bool          `json:"ok"`
		PathID  string        `json:"path_id"`
		Devices []VoiceDevice `json:"devices"`
	}
	if err := c.getJSON(ctx, "/v1/voice/devices", &resp, true); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

func (c *API) TestVoiceSTT(ctx context.Context, req VoiceTestSTTRequest) (VoiceTestSTTResult, error) {
	var resp struct {
		OK     bool               `json:"ok"`
		PathID string             `json:"path_id"`
		Result VoiceTestSTTResult `json:"result"`
	}
	if err := c.postJSON(ctx, "/v1/voice/test-stt", req, &resp, true); err != nil {
		return VoiceTestSTTResult{}, err
	}
	if strings.TrimSpace(resp.Result.PathID) == "" {
		resp.Result.PathID = strings.TrimSpace(resp.PathID)
	}
	return resp.Result, nil
}

func (c *API) TranscribeSTT(ctx context.Context, req STTTranscribeRequest) (STTTranscribeResult, error) {
	if len(req.Audio) == 0 {
		return STTTranscribeResult{}, errors.New("audio payload is required")
	}
	payload := struct {
		Profile   string `json:"profile,omitempty"`
		Provider  string `json:"provider,omitempty"`
		Model     string `json:"model,omitempty"`
		Language  string `json:"language,omitempty"`
		AudioBase string `json:"audio_base64"`
	}{
		Profile:   strings.TrimSpace(req.Profile),
		Provider:  strings.TrimSpace(req.Provider),
		Model:     strings.TrimSpace(req.Model),
		Language:  strings.TrimSpace(req.Language),
		AudioBase: base64.StdEncoding.EncodeToString(req.Audio),
	}
	var resp struct {
		OK     bool                `json:"ok"`
		PathID string              `json:"path_id"`
		Result STTTranscribeResult `json:"result"`
	}
	if err := c.postJSON(ctx, "/v1/stt/transcribe", payload, &resp, true); err != nil {
		return STTTranscribeResult{}, err
	}
	if strings.TrimSpace(resp.Result.PathID) == "" {
		resp.Result.PathID = strings.TrimSpace(resp.PathID)
	}
	return resp.Result, nil
}

func (c *API) ListAuthCredentials(ctx context.Context, provider, query string, limit int) (AuthCredentialList, error) {
	if limit <= 0 {
		limit = 200
	}
	params := url.Values{}
	if strings.TrimSpace(provider) != "" {
		params.Set("provider", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(query) != "" {
		params.Set("query", strings.TrimSpace(query))
	}
	params.Set("limit", strconv.Itoa(limit))
	path := "/v1/auth/credentials?" + params.Encode()
	var out AuthCredentialList
	if err := c.getJSON(ctx, path, &out, true); err != nil {
		return AuthCredentialList{}, err
	}
	return out, nil
}

func (c *API) UpsertAuthCredential(ctx context.Context, req AuthCredentialUpsertRequest) (AuthCredential, error) {
	var out AuthCredential
	if err := c.postJSON(ctx, "/v1/auth/credentials", req, &out, true); err != nil {
		return AuthCredential{}, err
	}
	return out, nil
}

func (c *API) SetActiveAuthCredential(ctx context.Context, provider, credentialID string) (AuthCredential, error) {
	payload := map[string]string{
		"provider": strings.TrimSpace(provider),
		"id":       strings.TrimSpace(credentialID),
	}
	var out AuthCredential
	if err := c.postJSON(ctx, "/v1/auth/credentials/active", payload, &out, true); err != nil {
		return AuthCredential{}, err
	}
	return out, nil
}

func (c *API) DeleteAuthCredential(ctx context.Context, provider, credentialID string) (AuthCredentialDeleteResult, error) {
	payload := map[string]string{
		"provider": strings.TrimSpace(provider),
		"id":       strings.TrimSpace(credentialID),
	}
	var out AuthCredentialDeleteResult
	if err := c.postJSON(ctx, "/v1/auth/credentials/delete", payload, &out, true); err != nil {
		return AuthCredentialDeleteResult{}, err
	}
	return out, nil
}

func (c *API) VerifyAuthCredential(ctx context.Context, provider, credentialID string) (AuthConnectionStatus, error) {
	payload := map[string]string{
		"provider": strings.TrimSpace(provider),
		"id":       strings.TrimSpace(credentialID),
	}
	var out struct {
		Connection AuthConnectionStatus `json:"connection"`
	}
	if err := c.postJSON(ctx, "/v1/auth/credentials/verify", payload, &out, true); err != nil {
		return AuthConnectionStatus{}, err
	}
	return out.Connection, nil
}

func (c *API) GetModel(ctx context.Context) (ModelResolved, error) {
	var resolved ModelResolved
	if err := c.getJSON(ctx, "/v1/model", &resolved, true); err != nil {
		return ModelResolved{}, err
	}
	return resolved, nil
}

func (c *API) SetModel(ctx context.Context, provider, model, thinking, serviceTier, contextMode string) (ModelResolved, error) {
	req := map[string]string{
		"provider":     strings.TrimSpace(provider),
		"model":        strings.TrimSpace(model),
		"thinking":     strings.TrimSpace(thinking),
		"service_tier": strings.TrimSpace(serviceTier),
		"context_mode": strings.TrimSpace(contextMode),
	}
	var out ModelResolved
	if err := c.postJSON(ctx, "/v1/model", req, &out, true); err != nil {
		return ModelResolved{}, err
	}
	return out, nil
}

func (c *API) ListModelCatalog(ctx context.Context, provider string, limit int) ([]ModelCatalogRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	params := url.Values{}
	params.Set("provider", strings.TrimSpace(provider))
	params.Set("limit", strconv.Itoa(limit))
	path := "/v1/model/catalog?" + params.Encode()
	var resp struct {
		OK       bool                 `json:"ok"`
		Provider string               `json:"provider"`
		Count    int                  `json:"count"`
		Records  []ModelCatalogRecord `json:"records"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Records, nil
}

func (c *API) ListModelFavorites(ctx context.Context, provider, query string, limit int) ([]ModelFavoriteRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	params := url.Values{}
	if strings.TrimSpace(provider) != "" {
		params.Set("provider", strings.TrimSpace(provider))
	}
	if strings.TrimSpace(query) != "" {
		params.Set("query", strings.TrimSpace(query))
	}
	params.Set("limit", strconv.Itoa(limit))
	path := "/v1/models/favorites?" + params.Encode()
	var resp struct {
		OK      bool                  `json:"ok"`
		Records []ModelFavoriteRecord `json:"records"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Records, nil
}

func (c *API) UpsertModelFavorite(ctx context.Context, req ModelFavoriteUpsertRequest) (ModelFavoriteRecord, error) {
	var resp struct {
		OK       bool                `json:"ok"`
		Favorite ModelFavoriteRecord `json:"favorite"`
	}
	if err := c.postJSON(ctx, "/v1/models/favorites", req, &resp, true); err != nil {
		return ModelFavoriteRecord{}, err
	}
	return resp.Favorite, nil
}

func (c *API) DeleteModelFavorite(ctx context.Context, provider, model string) error {
	payload := map[string]string{
		"provider": strings.TrimSpace(provider),
		"model":    strings.TrimSpace(model),
	}
	return c.postJSON(ctx, "/v1/models/favorites/delete", payload, nil, true)
}

func (c *API) ListAgents(ctx context.Context, limit int) (AgentState, error) {
	if limit <= 0 {
		limit = 200
	}
	path := "/v2/agents?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK                      bool                     `json:"ok"`
		State                   AgentState               `json:"state"`
		ProviderDefaultsPreview *ProviderDefaultsPreview `json:"provider_defaults_preview"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return AgentState{}, err
	}
	resp.State.ProviderDefaultsPreview = normalizeProviderDefaultsPreview(resp.ProviderDefaultsPreview)
	return resp.State, nil
}

func normalizeProviderDefaultsPreview(preview *ProviderDefaultsPreview) *ProviderDefaultsPreview {
	if preview == nil {
		return nil
	}
	out := *preview
	out.Provider = strings.TrimSpace(out.Provider)
	out.PrimaryAgent = strings.TrimSpace(out.PrimaryAgent)
	out.PrimaryModel = strings.TrimSpace(out.PrimaryModel)
	out.PrimaryThinking = strings.TrimSpace(out.PrimaryThinking)
	out.UtilityProvider = strings.TrimSpace(out.UtilityProvider)
	if out.UtilityProvider == "" {
		out.UtilityProvider = out.Provider
	}
	out.UtilityModel = strings.TrimSpace(out.UtilityModel)
	out.UtilityThinking = strings.TrimSpace(out.UtilityThinking)
	out.UtilityAgents = trimStringSlice(out.UtilityAgents)
	out.AffectedAgents = trimStringSlice(out.AffectedAgents)
	out.OutOfSyncAgents = trimStringSlice(out.OutOfSyncAgents)
	out.InheritingAgents = trimStringSlice(out.InheritingAgents)
	out.StaleInheritedAgents = trimStringSlice(out.StaleInheritedAgents)
	return &out
}

func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (c *API) UpsertAgent(ctx context.Context, req AgentUpsertRequest) (AgentProfile, int64, error) {
	var resp struct {
		OK      bool         `json:"ok"`
		Profile AgentProfile `json:"profile"`
		Version int64        `json:"version"`
	}
	path := "/v2/agents/" + url.PathEscape(strings.TrimSpace(req.Name))
	toolContract := req.ToolContract
	if toolContract == nil && req.ToolScope != nil {
		tools := map[string]AgentToolConfig{}
		for _, name := range req.ToolScope.AllowTools {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			enabled := true
			tools[name] = AgentToolConfig{Enabled: &enabled}
		}
		for _, name := range req.ToolScope.DenyTools {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			enabled := false
			tools[name] = AgentToolConfig{Enabled: &enabled}
		}
		if len(req.ToolScope.BashPrefixes) > 0 {
			enabled := true
			tools["bash"] = AgentToolConfig{
				Enabled:      &enabled,
				BashPrefixes: append([]string(nil), req.ToolScope.BashPrefixes...),
			}
		}
		toolContract = &AgentToolContract{
			Preset:        strings.TrimSpace(req.ToolScope.Preset),
			Tools:         tools,
			InheritPolicy: req.ToolScope.InheritPolicy,
		}
	}
	payload := map[string]any{
		"mode":                   strings.TrimSpace(req.Mode),
		"description":            strings.TrimSpace(req.Description),
		"prompt":                 req.Prompt,
		"execution_setting":      strings.TrimSpace(req.ExecutionSetting),
		"exit_plan_mode_enabled": req.ExitPlanModeEnabled,
		"tool_contract":          toolContract,
		"enabled":                req.Enabled,
		"custom_tools":           req.CustomTools,
		"assign_custom_tools":    req.AssignCustomTools,
	}
	if req.Provider != nil {
		payload["provider"] = *req.Provider
	}
	if req.Model != nil {
		payload["model"] = *req.Model
	}
	if req.Thinking != nil {
		payload["thinking"] = *req.Thinking
	}
	if err := c.putJSON(ctx, path, payload, &resp, true); err != nil {
		return AgentProfile{}, 0, err
	}
	return resp.Profile, resp.Version, nil
}

func (c *API) ListCustomTools(ctx context.Context, limit int) ([]AgentCustomToolDefinition, error) {
	if limit <= 0 {
		limit = 200
	}
	path := "/v2/custom-tools?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK          bool                        `json:"ok"`
		CustomTools []AgentCustomToolDefinition `json:"custom_tools"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.CustomTools, nil
}

func (c *API) PutCustomTool(ctx context.Context, definition AgentCustomToolDefinition) (AgentCustomToolDefinition, error) {
	var resp struct {
		OK         bool                      `json:"ok"`
		CustomTool AgentCustomToolDefinition `json:"custom_tool"`
	}
	path := "/v2/custom-tools/" + url.PathEscape(strings.TrimSpace(definition.Name))
	payload := map[string]any{
		"name":        strings.TrimSpace(definition.Name),
		"kind":        strings.TrimSpace(definition.Kind),
		"description": strings.TrimSpace(definition.Description),
		"command":     strings.TrimSpace(definition.Command),
	}
	if err := c.putJSON(ctx, path, payload, &resp, true); err != nil {
		return AgentCustomToolDefinition{}, err
	}
	return resp.CustomTool, nil
}

func (c *API) DeleteCustomTool(ctx context.Context, name string) (string, error) {
	var resp struct {
		OK      bool   `json:"ok"`
		Deleted string `json:"deleted"`
	}
	status, body, err := c.request(ctx, http.MethodDelete, "/v2/custom-tools/"+url.PathEscape(strings.TrimSpace(name)), nil, true)
	if err != nil {
		return "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", decodeAPIError(status, body)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return "", fmt.Errorf("decode /v2/custom-tools delete response: %w", err)
		}
	}
	return strings.TrimSpace(resp.Deleted), nil
}

func (c *API) AssignCustomTool(ctx context.Context, agentName, toolName string) (AgentProfile, int64, error) {
	var resp struct {
		OK      bool         `json:"ok"`
		Profile AgentProfile `json:"profile"`
		Version int64        `json:"version"`
	}
	path := "/v2/agents/" + url.PathEscape(strings.TrimSpace(agentName)) + "/custom-tools/" + url.PathEscape(strings.TrimSpace(toolName))
	if err := c.putJSON(ctx, path, map[string]any{}, &resp, true); err != nil {
		return AgentProfile{}, 0, err
	}
	return resp.Profile, resp.Version, nil
}

func (c *API) UnassignCustomTool(ctx context.Context, agentName, toolName string) (AgentProfile, int64, error) {
	var resp struct {
		OK      bool         `json:"ok"`
		Profile AgentProfile `json:"profile"`
		Version int64        `json:"version"`
	}
	path := "/v2/agents/" + url.PathEscape(strings.TrimSpace(agentName)) + "/custom-tools/" + url.PathEscape(strings.TrimSpace(toolName))
	status, body, err := c.request(ctx, http.MethodDelete, path, nil, true)
	if err != nil {
		return AgentProfile{}, 0, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return AgentProfile{}, 0, decodeAPIError(status, body)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return AgentProfile{}, 0, fmt.Errorf("decode custom tool unassign response: %w", err)
		}
	}
	return resp.Profile, resp.Version, nil
}

func (c *API) ActivatePrimaryAgent(ctx context.Context, name string) (string, int64, error) {
	req := map[string]string{"name": strings.TrimSpace(name)}
	var resp struct {
		OK            bool   `json:"ok"`
		ActivePrimary string `json:"active_primary"`
		Version       int64  `json:"version"`
	}
	if err := c.putJSON(ctx, "/v2/agents/active/primary", req, &resp, true); err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(resp.ActivePrimary), resp.Version, nil
}

func (c *API) DeleteAgent(ctx context.Context, name string) (string, string, int64, error) {
	var resp struct {
		OK            bool   `json:"ok"`
		Deleted       string `json:"deleted"`
		ActivePrimary string `json:"active_primary"`
		Version       int64  `json:"version"`
	}
	status, body, err := c.request(ctx, http.MethodDelete, "/v2/agents/"+url.PathEscape(strings.TrimSpace(name)), nil, true)
	if err != nil {
		return "", "", 0, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", "", 0, decodeAPIError(status, body)
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return "", "", 0, fmt.Errorf("decode /v2/agents delete response: %w", err)
		}
	}
	return strings.TrimSpace(resp.Deleted), strings.TrimSpace(resp.ActivePrimary), resp.Version, nil
}

func (c *API) RestoreAgentDefaults(ctx context.Context, utilityInput ...ProviderDefaultsPreview) (RestoreAgentsDefaultsResult, error) {
	var resp struct {
		OK                      bool                     `json:"ok"`
		Profiles                []AgentProfile           `json:"profiles"`
		ActivePrimary           string                   `json:"active_primary"`
		ActiveSubagent          map[string]string        `json:"active_subagent"`
		Version                 int64                    `json:"version"`
		ProviderDefaultsPreview *ProviderDefaultsPreview `json:"provider_defaults_preview"`
	}
	payload := map[string]any{}
	if len(utilityInput) > 0 {
		input := utilityInput[0]
		if strings.TrimSpace(input.UtilityProvider) != "" {
			payload["utility_provider"] = strings.TrimSpace(input.UtilityProvider)
		}
		if strings.TrimSpace(input.UtilityModel) != "" {
			payload["utility_model"] = strings.TrimSpace(input.UtilityModel)
		}
		if strings.TrimSpace(input.UtilityThinking) != "" {
			payload["utility_thinking"] = strings.TrimSpace(input.UtilityThinking)
		}
	}
	if err := c.postJSON(ctx, "/v2/agents/defaults/restore", payload, &resp, true); err != nil {
		return RestoreAgentsDefaultsResult{}, err
	}
	return RestoreAgentsDefaultsResult{
		Profiles:                resp.Profiles,
		ActivePrimary:           strings.TrimSpace(resp.ActivePrimary),
		ActiveSubagent:          resp.ActiveSubagent,
		Version:                 resp.Version,
		ProviderDefaultsPreview: normalizeProviderDefaultsPreview(resp.ProviderDefaultsPreview),
	}, nil
}

func (c *API) ResetAgentDefaults(ctx context.Context) (RestoreAgentsDefaultsResult, error) {
	var resp struct {
		OK                      bool                     `json:"ok"`
		Profiles                []AgentProfile           `json:"profiles"`
		ActivePrimary           string                   `json:"active_primary"`
		ActiveSubagent          map[string]string        `json:"active_subagent"`
		Version                 int64                    `json:"version"`
		ProviderDefaultsPreview *ProviderDefaultsPreview `json:"provider_defaults_preview"`
	}
	if err := c.postJSON(ctx, "/v2/agents/defaults/reset", map[string]any{}, &resp, true); err != nil {
		return RestoreAgentsDefaultsResult{}, err
	}
	return RestoreAgentsDefaultsResult{
		Profiles:                resp.Profiles,
		ActivePrimary:           strings.TrimSpace(resp.ActivePrimary),
		ActiveSubagent:          resp.ActiveSubagent,
		Version:                 resp.Version,
		ProviderDefaultsPreview: normalizeProviderDefaultsPreview(resp.ProviderDefaultsPreview),
	}, nil
}

func (c *API) WorkspaceCurrent(ctx context.Context) (WorkspaceResolution, bool, error) {
	status, body, err := c.request(ctx, http.MethodGet, "/v1/workspace/current", nil, true)
	if err != nil {
		return WorkspaceResolution{}, false, err
	}
	if status == http.StatusNotFound {
		return WorkspaceResolution{}, false, nil
	}
	if status != http.StatusOK {
		return WorkspaceResolution{}, false, decodeAPIError(status, body)
	}
	var resolution WorkspaceResolution
	if err := json.Unmarshal(body, &resolution); err != nil {
		return WorkspaceResolution{}, false, fmt.Errorf("decode workspace current response: %w", err)
	}
	return resolution, true, nil
}

func (c *API) ResolveWorkspace(ctx context.Context, cwd string) (WorkspaceResolution, error) {
	query := ""
	if strings.TrimSpace(cwd) != "" {
		query = "?cwd=" + url.QueryEscape(strings.TrimSpace(cwd))
	}
	var resolution WorkspaceResolution
	if err := c.getJSON(ctx, "/v1/workspace/resolve"+query, &resolution, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resolution, nil
}

func (c *API) SelectWorkspace(ctx context.Context, path string) (WorkspaceResolution, error) {
	req := map[string]string{
		"path": strings.TrimSpace(path),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/select", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) AddWorkspace(ctx context.Context, path, name, themeID string, makeCurrent bool) (WorkspaceResolution, error) {
	req := map[string]any{
		"path":         strings.TrimSpace(path),
		"name":         strings.TrimSpace(name),
		"theme_id":     strings.TrimSpace(themeID),
		"make_current": makeCurrent,
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/add", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) MoveWorkspace(ctx context.Context, path string, delta int) (WorkspaceResolution, error) {
	req := map[string]any{
		"path":  strings.TrimSpace(path),
		"delta": delta,
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/move", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) SetWorkspaceTheme(ctx context.Context, path, themeID string) (WorkspaceResolution, error) {
	req := map[string]string{
		"path":     strings.TrimSpace(path),
		"theme_id": strings.TrimSpace(themeID),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/theme", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) AddWorkspaceDirectory(ctx context.Context, workspacePath, directoryPath string) (WorkspaceResolution, error) {
	req := map[string]string{
		"workspace_path": strings.TrimSpace(workspacePath),
		"directory_path": strings.TrimSpace(directoryPath),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/directories/add", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) RemoveWorkspaceDirectory(ctx context.Context, workspacePath, directoryPath string) (WorkspaceResolution, error) {
	req := map[string]string{
		"workspace_path": strings.TrimSpace(workspacePath),
		"directory_path": strings.TrimSpace(directoryPath),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/directories/remove", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) RenameWorkspace(ctx context.Context, path, name string) (WorkspaceResolution, error) {
	req := map[string]string{
		"path": strings.TrimSpace(path),
		"name": strings.TrimSpace(name),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/rename", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) DeleteWorkspace(ctx context.Context, path string) (WorkspaceResolution, error) {
	req := map[string]string{
		"path": strings.TrimSpace(path),
	}
	var resp struct {
		OK        bool                `json:"ok"`
		Workspace WorkspaceResolution `json:"workspace"`
	}
	if err := c.postJSON(ctx, "/v1/workspace/delete", req, &resp, true); err != nil {
		return WorkspaceResolution{}, err
	}
	return resp.Workspace, nil
}

func (c *API) ListWorkspaces(ctx context.Context, limit int) ([]WorkspaceEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	query := "?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK         bool             `json:"ok"`
		Workspaces []WorkspaceEntry `json:"workspaces"`
	}
	if err := c.getJSON(ctx, "/v1/workspace/list"+query, &resp, true); err != nil {
		return nil, err
	}
	return resp.Workspaces, nil
}

func (c *API) WorkspaceOverview(ctx context.Context, cwd string, roots []string, sessionLimit int) (WorkspaceOverviewResponse, error) {
	query := url.Values{}
	query.Set("workspace_limit", "200")
	query.Set("discover_limit", "200")
	if sessionLimit > 0 {
		query.Set("session_limit", strconv.Itoa(sessionLimit))
	}
	if strings.TrimSpace(cwd) != "" {
		query.Set("cwd", strings.TrimSpace(cwd))
	}
	trimmedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root != "" {
			trimmedRoots = append(trimmedRoots, root)
		}
	}
	if len(trimmedRoots) > 0 {
		query.Set("roots", strings.Join(trimmedRoots, ","))
	}
	path := "/v1/workspace/overview?" + query.Encode()
	var resp WorkspaceOverviewResponse
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return WorkspaceOverviewResponse{}, err
	}
	return resp, nil
}

func (c *API) ManageWorktree(ctx context.Context, workspacePath string, limit, cursor int) (ManageWorktreeResponse, error) {
	query := url.Values{}
	query.Set("action", "inspect")
	if strings.TrimSpace(workspacePath) != "" {
		query.Set("workspace_path", strings.TrimSpace(workspacePath))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor > 0 {
		query.Set("cursor", strconv.Itoa(cursor))
	}
	var resp ManageWorktreeResponse
	if err := c.getJSON(ctx, "/v1/manage-worktree?"+query.Encode(), &resp, true); err != nil {
		return ManageWorktreeResponse{}, err
	}
	return resp, nil
}

func (c *API) ContextSources(ctx context.Context, cwd string) (ContextReport, error) {
	path := "/v1/context/sources"
	if strings.TrimSpace(cwd) != "" {
		path += "?cwd=" + url.QueryEscape(strings.TrimSpace(cwd))
	}
	var resp struct {
		OK     bool          `json:"ok"`
		Report ContextReport `json:"report"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return ContextReport{}, err
	}
	return resp.Report, nil
}

func (c *API) GetSandboxStatus(ctx context.Context) (SandboxStatus, error) {
	var resp struct {
		OK      bool          `json:"ok"`
		Sandbox SandboxStatus `json:"sandbox"`
	}
	if err := c.getJSON(ctx, "/v1/sandbox", &resp, true); err != nil {
		return SandboxStatus{}, err
	}
	return resp.Sandbox, nil
}

func (c *API) PreflightSandbox(ctx context.Context) (SandboxStatus, error) {
	var resp struct {
		OK      bool          `json:"ok"`
		Sandbox SandboxStatus `json:"sandbox"`
	}
	if err := c.postJSON(ctx, "/v1/sandbox/preflight", map[string]any{}, &resp, true); err != nil {
		return SandboxStatus{}, err
	}
	return resp.Sandbox, nil
}

func (c *API) SetSandboxEnabled(ctx context.Context, enabled bool) (SandboxStatus, error) {
	req := map[string]bool{"enabled": enabled}
	var resp struct {
		OK      bool          `json:"ok"`
		Reason  string        `json:"reason"`
		Sandbox SandboxStatus `json:"sandbox"`
	}
	if err := c.postJSON(ctx, "/v1/sandbox", req, &resp, true); err != nil {
		return SandboxStatus{}, err
	}
	if !resp.OK {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "sandbox prerequisites are not ready"
		}
		return resp.Sandbox, errors.New(reason)
	}
	return resp.Sandbox, nil
}

func (c *API) GetWorktreeSettings(ctx context.Context, workspacePath string) (WorktreeSettings, error) {
	var resp struct {
		OK        bool             `json:"ok"`
		Worktrees WorktreeSettings `json:"worktrees"`
	}
	path := "/v1/worktrees"
	if value := strings.TrimSpace(workspacePath); value != "" {
		path += "?workspace_path=" + url.QueryEscape(value)
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return WorktreeSettings{}, err
	}
	return resp.Worktrees, nil
}

func (c *API) UpdateWorktreeSettings(ctx context.Context, req WorktreeSettingsUpdateRequest) (WorktreeSettings, error) {
	payload := map[string]any{}
	if value := strings.TrimSpace(req.WorkspacePath); value != "" {
		payload["workspace_path"] = value
	}
	if req.Enabled != nil {
		payload["enabled"] = *req.Enabled
	}
	if req.UseCurrentBranch != nil {
		payload["use_current_branch"] = *req.UseCurrentBranch
	}
	if value := strings.TrimSpace(req.BaseBranch); value != "" {
		payload["base_branch"] = value
	}
	if req.BranchName != nil {
		payload["branch_name"] = strings.TrimSpace(*req.BranchName)
	}
	var resp struct {
		OK        bool             `json:"ok"`
		Worktrees WorktreeSettings `json:"worktrees"`
	}
	if err := c.postJSON(ctx, "/v1/worktrees", payload, &resp, true); err != nil {
		return WorktreeSettings{}, err
	}
	return resp.Worktrees, nil
}

func (c *API) ListMCPServers(ctx context.Context, limit int) ([]MCPServer, error) {
	if limit <= 0 {
		limit = 500
	}
	path := "/v1/mcp/servers?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK      bool        `json:"ok"`
		Servers []MCPServer `json:"servers"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Servers, nil
}

func (c *API) UpsertMCPServer(ctx context.Context, req MCPServerUpsertRequest) (MCPServer, error) {
	payload := map[string]any{
		"id": strings.TrimSpace(req.ID),
	}
	if value := strings.TrimSpace(req.Name); value != "" {
		payload["name"] = value
	}
	if value := strings.TrimSpace(req.Transport); value != "" {
		payload["transport"] = strings.ToLower(value)
	}
	if value := strings.TrimSpace(req.URL); value != "" {
		payload["url"] = value
	}
	if value := strings.TrimSpace(req.Command); value != "" {
		payload["command"] = value
	}
	if len(req.Args) > 0 {
		payload["args"] = append([]string(nil), req.Args...)
	}
	if len(req.Env) > 0 {
		payload["env"] = req.Env
	}
	if len(req.Headers) > 0 {
		payload["headers"] = req.Headers
	}
	if req.Enabled != nil {
		payload["enabled"] = *req.Enabled
	}
	if value := strings.TrimSpace(req.Source); value != "" {
		payload["source"] = strings.ToLower(value)
	}
	var resp struct {
		OK     bool      `json:"ok"`
		Server MCPServer `json:"server"`
	}
	if err := c.postJSON(ctx, "/v1/mcp/servers/upsert", payload, &resp, true); err != nil {
		return MCPServer{}, err
	}
	return resp.Server, nil
}

func (c *API) DeleteMCPServer(ctx context.Context, id string) error {
	var resp struct {
		OK      bool   `json:"ok"`
		Deleted bool   `json:"deleted"`
		ID      string `json:"id"`
	}
	if err := c.postJSON(ctx, "/v1/mcp/servers/delete", map[string]any{
		"id": strings.TrimSpace(id),
	}, &resp, true); err != nil {
		return err
	}
	if !resp.OK || !resp.Deleted {
		return errors.New("mcp server deletion failed")
	}
	return nil
}

func (c *API) SetMCPServerEnabled(ctx context.Context, id string, enabled bool) (MCPServer, error) {
	var resp struct {
		OK     bool      `json:"ok"`
		Server MCPServer `json:"server"`
	}
	if err := c.postJSON(ctx, "/v1/mcp/servers/enabled", map[string]any{
		"id":      strings.TrimSpace(id),
		"enabled": enabled,
	}, &resp, true); err != nil {
		return MCPServer{}, err
	}
	return resp.Server, nil
}

func (c *API) ListSessions(ctx context.Context, limit int) ([]SessionSummary, error) {
	return c.ListSessionsForCWD(ctx, limit, "")
}

func (c *API) ListSessionsForCWD(ctx context.Context, limit int, cwd string) ([]SessionSummary, error) {
	return c.listSessionsForPath(ctx, limit, cwd, false)
}

func (c *API) ListSessionsForExactCWD(ctx context.Context, limit int, cwd string) ([]SessionSummary, error) {
	return c.listSessionsForPath(ctx, limit, cwd, true)
}

func (c *API) listSessionsForPath(ctx context.Context, limit int, cwd string, exact bool) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	path := "/v1/sessions?limit=" + strconv.Itoa(limit)
	if value := strings.TrimSpace(cwd); value != "" {
		path += "&cwd=" + url.QueryEscape(value)
	}
	if exact {
		path += "&exact_path=true"
	}
	var resp struct {
		OK       bool             `json:"ok"`
		Sessions []SessionSummary `json:"sessions"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

func (c *API) GetSession(ctx context.Context, sessionID string) (SessionSummary, error) {
	path := "/v1/sessions/" + url.PathEscape(sessionID)
	var resp struct {
		OK      bool           `json:"ok"`
		Session SessionSummary `json:"session"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return SessionSummary{}, err
	}
	return resp.Session, nil
}

func (c *API) CreateSession(ctx context.Context, title, workspacePath, workspaceName string, preference ModelPreference) (SessionSummary, error) {
	return c.CreateSessionWithOptions(ctx, SessionCreateOptions{
		Title:         title,
		WorkspacePath: workspacePath,
		WorkspaceName: workspaceName,
		Preference:    preference,
	})
}

func (c *API) CreateSessionWithOptions(ctx context.Context, options SessionCreateOptions) (SessionSummary, error) {
	req := map[string]any{
		"title":                  strings.TrimSpace(options.Title),
		"workspace_path":         strings.TrimSpace(options.WorkspacePath),
		"host_workspace_path":    strings.TrimSpace(options.HostWorkspacePath),
		"runtime_workspace_path": strings.TrimSpace(options.RuntimeWorkspacePath),
		"workspace_name":         strings.TrimSpace(options.WorkspaceName),
		"mode":                   strings.TrimSpace(options.Mode),
		"agent_name":             strings.TrimSpace(options.AgentName),
		"metadata":               options.Metadata,
		"worktree_mode":          strings.TrimSpace(options.WorktreeMode),
		"preference": map[string]string{
			"provider":     strings.TrimSpace(options.Preference.Provider),
			"model":        strings.TrimSpace(options.Preference.Model),
			"thinking":     strings.TrimSpace(options.Preference.Thinking),
			"service_tier": strings.TrimSpace(options.Preference.ServiceTier),
			"context_mode": strings.TrimSpace(options.Preference.ContextMode),
		},
	}
	var resp struct {
		OK      bool           `json:"ok"`
		Session SessionSummary `json:"session"`
		Warning string         `json:"warning,omitempty"`
	}
	if err := c.postJSON(ctx, "/v1/sessions", req, &resp, true); err != nil {
		return SessionSummary{}, err
	}
	resp.Session.Warning = strings.TrimSpace(resp.Warning)
	return resp.Session, nil
}

func (c *API) ListSessionMessages(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]SessionMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}

	query := url.Values{}
	if afterSeq > 0 {
		query.Set("after_seq", strconv.FormatUint(afterSeq, 10))
	}
	query.Set("limit", strconv.Itoa(limit))
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/messages?" + query.Encode()

	var resp struct {
		OK        bool             `json:"ok"`
		SessionID string           `json:"session_id"`
		Messages  []SessionMessage `json:"messages"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

func (c *API) GetSessionUsage(ctx context.Context, sessionID string, limit int) (SessionUsageSummary, bool, []SessionTurnUsage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionUsageSummary{}, false, nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/usage?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK              bool                `json:"ok"`
		SessionID       string              `json:"session_id"`
		HasUsageSummary bool                `json:"has_usage_summary"`
		UsageSummary    SessionUsageSummary `json:"usage_summary"`
		TurnUsage       []SessionTurnUsage  `json:"turn_usage_records"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return SessionUsageSummary{}, false, nil, err
	}
	return resp.UsageSummary, resp.HasUsageSummary, resp.TurnUsage, nil
}

func (c *API) GetSessionMode(ctx context.Context, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/mode"
	var resp struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"session_id"`
		Mode      string `json:"mode"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Mode), nil
}

func (c *API) SetSessionMode(ctx context.Context, sessionID, mode string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	req := map[string]string{
		"mode": strings.TrimSpace(mode),
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/mode"
	var resp struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"session_id"`
		Mode      string `json:"mode"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Mode), nil
}

func (c *API) UpdateSessionMetadata(ctx context.Context, sessionID string, metadata map[string]any) (SessionSummary, error) {
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/metadata"
	payload := map[string]any{
		"metadata": metadata,
	}
	var resp struct {
		OK      bool           `json:"ok"`
		Session SessionSummary `json:"session"`
	}
	if err := c.postJSON(ctx, path, payload, &resp, true); err != nil {
		return SessionSummary{}, err
	}
	return resp.Session, nil
}

func (c *API) GetSessionCodexConfig(ctx context.Context, sessionID string) (SessionCodexConfig, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionCodexConfig{}, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/codex"
	var resp struct {
		OK                     bool   `json:"ok"`
		SessionID              string `json:"session_id"`
		Provider               string `json:"provider"`
		Model                  string `json:"model"`
		Thinking               string `json:"thinking"`
		ServiceTier            string `json:"service_tier"`
		ContextMode            string `json:"context_mode"`
		EffectiveContextWindow int    `json:"effective_context_window"`
		UpdatedAt              int64  `json:"updated_at"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return SessionCodexConfig{}, err
	}
	return SessionCodexConfig{
		ServiceTier:            strings.TrimSpace(resp.ServiceTier),
		ContextMode:            strings.TrimSpace(resp.ContextMode),
		EffectiveContextWindow: resp.EffectiveContextWindow,
		UpdatedAt:              resp.UpdatedAt,
	}, nil
}

func (c *API) UpdateSessionCodexConfig(ctx context.Context, sessionID string, req SessionCodexConfigUpdateRequest) (SessionCodexConfig, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionCodexConfig{}, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/codex"
	var resp struct {
		OK                     bool   `json:"ok"`
		SessionID              string `json:"session_id"`
		Provider               string `json:"provider"`
		Model                  string `json:"model"`
		Thinking               string `json:"thinking"`
		ServiceTier            string `json:"service_tier"`
		ContextMode            string `json:"context_mode"`
		EffectiveContextWindow int    `json:"effective_context_window"`
		UpdatedAt              int64  `json:"updated_at"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return SessionCodexConfig{}, err
	}
	return SessionCodexConfig{
		ServiceTier:            strings.TrimSpace(resp.ServiceTier),
		ContextMode:            strings.TrimSpace(resp.ContextMode),
		EffectiveContextWindow: resp.EffectiveContextWindow,
		UpdatedAt:              resp.UpdatedAt,
	}, nil
}

func (c *API) GetSessionPreference(ctx context.Context, sessionID string) (ModelResolved, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ModelResolved{}, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/preference"
	var resolved ModelResolved
	if err := c.getJSON(ctx, path, &resolved, true); err != nil {
		return ModelResolved{}, err
	}
	return resolved, nil
}

func (c *API) SetSessionPreference(ctx context.Context, sessionID string, req map[string]any) (ModelResolved, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ModelResolved{}, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/preference"
	var resolved ModelResolved
	if err := c.postJSON(ctx, path, req, &resolved, true); err != nil {
		return ModelResolved{}, err
	}
	return resolved, nil
}

func (c *API) ListSessionPlans(ctx context.Context, sessionID string, limit int) ([]SessionPlan, string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, "", errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/plans?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK           bool          `json:"ok"`
		SessionID    string        `json:"session_id"`
		Count        int           `json:"count"`
		ActivePlanID string        `json:"active_plan_id"`
		Plans        []SessionPlan `json:"plans"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, "", err
	}
	return resp.Plans, strings.TrimSpace(resp.ActivePlanID), nil
}

func (c *API) GetSessionPlan(ctx context.Context, sessionID, planID string) (SessionPlan, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return SessionPlan{}, errors.New("session id is required")
	}
	if planID == "" {
		return SessionPlan{}, errors.New("plan id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/plans/" + url.PathEscape(planID)
	var resp struct {
		OK        bool        `json:"ok"`
		SessionID string      `json:"session_id"`
		Plan      SessionPlan `json:"plan"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return SessionPlan{}, err
	}
	return resp.Plan, nil
}

func (c *API) GetActiveSessionPlan(ctx context.Context, sessionID string) (SessionPlan, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionPlan{}, false, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/plans/active"
	var resp struct {
		OK         bool        `json:"ok"`
		SessionID  string      `json:"session_id"`
		HasActive  bool        `json:"has_active"`
		ActivePlan SessionPlan `json:"active_plan"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return SessionPlan{}, false, err
	}
	return resp.ActivePlan, resp.HasActive, nil
}

func (c *API) SaveSessionPlan(ctx context.Context, sessionID string, req SessionPlanUpsertRequest) (SessionPlan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionPlan{}, errors.New("session id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/plans"
	var resp struct {
		OK        bool        `json:"ok"`
		SessionID string      `json:"session_id"`
		Plan      SessionPlan `json:"plan"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return SessionPlan{}, err
	}
	return resp.Plan, nil
}

func (c *API) SetActiveSessionPlan(ctx context.Context, sessionID, planID string) (SessionPlan, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return SessionPlan{}, errors.New("session id is required")
	}
	if planID == "" {
		return SessionPlan{}, errors.New("plan id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/plans/active"
	req := map[string]string{"plan_id": planID}
	var resp struct {
		OK         bool        `json:"ok"`
		SessionID  string      `json:"session_id"`
		ActivePlan SessionPlan `json:"active_plan"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return SessionPlan{}, err
	}
	return resp.ActivePlan, nil
}

func (c *API) ListPendingPermissions(ctx context.Context, sessionID string, limit int) ([]PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 200
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/permissions?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK          bool               `json:"ok"`
		SessionID   string             `json:"session_id"`
		Count       int                `json:"count"`
		Permissions []PermissionRecord `json:"permissions"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	out := make([]PermissionRecord, 0, len(resp.Permissions))
	for _, record := range resp.Permissions {
		if strings.EqualFold(strings.TrimSpace(record.Status), "pending") {
			out = append(out, record)
		}
	}
	return out, nil
}

func (c *API) ListPermissions(ctx context.Context, sessionID string, limit int) ([]PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 200
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/permissions?limit=" + strconv.Itoa(limit)
	var resp struct {
		OK          bool               `json:"ok"`
		SessionID   string             `json:"session_id"`
		Count       int                `json:"count"`
		Permissions []PermissionRecord `json:"permissions"`
	}
	if err := c.getJSON(ctx, path, &resp, true); err != nil {
		return nil, err
	}
	return resp.Permissions, nil
}

func (c *API) ResolvePermission(ctx context.Context, sessionID, permissionID, action, reason string) (PermissionRecord, error) {
	return c.ResolvePermissionWithArguments(ctx, sessionID, permissionID, action, reason, "")
}

func (c *API) ResolvePermissionWithArguments(ctx context.Context, sessionID, permissionID, action, reason, approvedArguments string) (PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	permissionID = strings.TrimSpace(permissionID)
	if sessionID == "" {
		return PermissionRecord{}, errors.New("session id is required")
	}
	if permissionID == "" {
		return PermissionRecord{}, errors.New("permission id is required")
	}
	req := map[string]any{
		"action": strings.TrimSpace(action),
		"reason": strings.TrimSpace(reason),
	}
	if approvedArguments = strings.TrimSpace(approvedArguments); approvedArguments != "" {
		req["approved_arguments"] = json.RawMessage(approvedArguments)
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/permissions/" + url.PathEscape(permissionID) + "/resolve"
	var resp struct {
		OK         bool             `json:"ok"`
		SessionID  string           `json:"session_id"`
		Permission PermissionRecord `json:"permission"`
		SavedRule  *PermissionRule  `json:"saved_rule,omitempty"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return PermissionRecord{}, err
	}
	if resp.SavedRule != nil {
		resp.Permission.SavedRulePreview = strings.TrimSpace(previewPermissionRule(*resp.SavedRule))
	}
	return resp.Permission, nil
}

func (c *API) ResolveAllPermissions(ctx context.Context, sessionID, action, reason string) ([]PermissionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	req := map[string]any{
		"action": strings.TrimSpace(action),
		"reason": strings.TrimSpace(reason),
		"limit":  1000,
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/permissions/resolve_all"
	var resp struct {
		OK        bool               `json:"ok"`
		SessionID string             `json:"session_id"`
		Count     int                `json:"count"`
		Resolved  []PermissionRecord `json:"resolved"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return nil, err
	}
	return resp.Resolved, nil
}

func runSessionRequest(prompt, agentName, instructions string, options RunSessionOptions) map[string]any {
	req := map[string]any{
		"prompt":       strings.TrimSpace(prompt),
		"agent_name":   strings.TrimSpace(agentName),
		"instructions": strings.TrimSpace(instructions),
		"compact":      options.Compact,
	}
	if options.Background {
		req["background"] = true
	}
	if value := strings.TrimSpace(options.TargetKind); value != "" {
		req["target_kind"] = value
	}
	if value := strings.TrimSpace(options.TargetName); value != "" {
		req["target_name"] = value
	}
	if options.ToolScope != nil {
		req["tool_scope"] = options.ToolScope
	}
	if options.ExecutionContext != nil {
		req["execution_context"] = options.ExecutionContext
	}
	return req
}

func (c *API) RunSession(ctx context.Context, sessionID, prompt, agentName, instructions string) (SessionRunResult, error) {
	return c.RunSessionWithOptions(ctx, sessionID, prompt, agentName, instructions, RunSessionOptions{})
}

func (c *API) RunSessionWithOptions(ctx context.Context, sessionID, prompt, agentName, instructions string, options RunSessionOptions) (SessionRunResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionRunResult{}, errors.New("session id is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && !options.Compact {
		return SessionRunResult{}, errors.New("prompt is required")
	}

	req := runSessionRequest(prompt, agentName, instructions, options)
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/run"
	var resp struct {
		OK     bool             `json:"ok"`
		Result SessionRunResult `json:"result"`
	}
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return SessionRunResult{}, err
	}
	return resp.Result, nil
}

func (c *API) RunSessionStream(ctx context.Context, sessionID, prompt, agentName, instructions string, onEvent func(SessionRunStreamEvent)) (SessionRunResult, error) {
	return c.RunSessionStreamWithOptions(ctx, sessionID, prompt, agentName, instructions, RunSessionOptions{}, onEvent)
}

func (c *API) StopSessionRun(ctx context.Context, sessionID, runID string) error {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if runID == "" {
		return errors.New("run id is required")
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/run/stream"
	return c.postJSON(ctx, path, map[string]any{
		"type":   "run.stop",
		"run_id": runID,
	}, nil, true)
}

func (c *API) RunSessionStreamWithOptions(ctx context.Context, sessionID, prompt, agentName, instructions string, options RunSessionOptions, onEvent func(SessionRunStreamEvent)) (SessionRunResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionRunResult{}, errors.New("session id is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && !options.Compact {
		return SessionRunResult{}, errors.New("prompt is required")
	}

	if ctx == nil {
		var cancel context.CancelFunc
		// Streaming run requests should not time out by default.
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}

	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/run/stream"
	connect := func() (*wsClientConn, error) {
		baseURL, _, socketPath := c.requestTarget()
		return dialDaemonWS(ctx, baseURL, c.Token(), socketPath, path, "")
	}
	conn, err := connect()
	if err != nil {
		c.persistRunStreamClientError(sessionID, "connect", err)
		return SessionRunResult{}, fmt.Errorf("connect websocket %s: %w", path, err)
	}
	defer conn.Close()

	startPayload := runSessionRequest(prompt, agentName, instructions, options)
	startPayload["type"] = "run.start"

	startMsg, err := json.Marshal(startPayload)
	if err != nil {
		return SessionRunResult{}, fmt.Errorf("marshal run stream start payload: %w", err)
	}
	if err := conn.WriteText(startMsg); err != nil {
		c.persistRunStreamClientError(sessionID, "start", err)
		return SessionRunResult{}, fmt.Errorf("send run stream start payload: %w", err)
	}

	var final SessionRunResult
	runID := ""
	lastSeq := uint64(0)
	reconnects := 0

	for {
		var event SessionRunStreamEvent
		raw, readErr := conn.ReadText(ctx)
		if readErr != nil {
			if ctx != nil && ctx.Err() != nil {
				return SessionRunResult{}, ctx.Err()
			}
			if strings.TrimSpace(runID) != "" && reconnects < maxRunStreamReconnects {
				reconnects++
				_ = conn.Close()
				conn, err = connect()
				if err != nil {
					continue
				}
				resumeMsg, marshalErr := json.Marshal(map[string]any{
					"type":     "run.resume",
					"run_id":   runID,
					"last_seq": lastSeq,
				})
				if marshalErr != nil {
					return SessionRunResult{}, fmt.Errorf("marshal run stream resume payload: %w", marshalErr)
				}
				if writeErr := conn.WriteText(resumeMsg); writeErr != nil {
					continue
				}
				continue
			}
			c.persistRunStreamClientError(sessionID, "read", readErr)
			return SessionRunResult{}, fmt.Errorf("read run stream websocket event: %w", readErr)
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			c.persistRunStreamClientError(sessionID, "decode", err)
			return SessionRunResult{}, fmt.Errorf("decode run stream websocket event: %w", err)
		}
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = sessionID
		}
		if seq := event.Seq; seq > lastSeq {
			lastSeq = seq
		}
		if eventRunID := strings.TrimSpace(event.RunID); eventRunID != "" {
			runID = eventRunID
		} else if runID != "" {
			event.RunID = runID
		}

		eventType := strings.ToLower(strings.TrimSpace(event.Type))
		switch eventType {
		case "run.accepted", "resume.accepted":
			continue
		case "resume.error":
			msg := strings.TrimSpace(event.Error)
			if msg == "" {
				msg = "run stream resume cursor is no longer available; reconnect on the same run stream path and resync"
			}
			return SessionRunResult{}, errors.New(msg)
		case "error":
			msg := strings.TrimSpace(event.Error)
			if msg == "" {
				msg = "run stream failed"
			}
			return SessionRunResult{}, errors.New(msg)
		}

		reconnects = 0
		if onEvent != nil {
			onEvent(event)
		}
		switch eventType {
		case "turn.completed":
			if strings.TrimSpace(event.Result.SessionID) == "" && strings.TrimSpace(event.Result.AssistantMessage.ID) == "" {
				continue
			}
			final = event.Result
			if strings.TrimSpace(final.SessionID) == "" {
				final.SessionID = sessionID
			}
			return final, nil
		case "turn.error":
			msg := strings.TrimSpace(event.Error)
			if msg == "" {
				msg = "stream run failed"
			}
			return SessionRunResult{}, errors.New(msg)
		}
	}
}

func (c *API) StartBackgroundSessionRun(ctx context.Context, sessionID, prompt, agentName, instructions string, options RunSessionOptions) (BackgroundRunAccepted, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return BackgroundRunAccepted{}, errors.New("session id is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && !options.Compact {
		return BackgroundRunAccepted{}, errors.New("prompt is required")
	}
	options.Background = true

	req := runSessionRequest(prompt, agentName, instructions, options)
	req["type"] = "run.start"

	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/run/stream"
	var resp BackgroundRunAccepted
	if err := c.postJSON(ctx, path, req, &resp, true); err != nil {
		return BackgroundRunAccepted{}, err
	}
	resp.SessionID = strings.TrimSpace(resp.SessionID)
	resp.RunID = strings.TrimSpace(resp.RunID)
	resp.Status = strings.TrimSpace(resp.Status)
	resp.TargetKind = strings.TrimSpace(resp.TargetKind)
	resp.TargetName = strings.TrimSpace(resp.TargetName)
	resp.OwnerTransport = strings.TrimSpace(resp.OwnerTransport)
	return resp, nil
}

func (c *API) persistRunStreamClientError(sessionID, stage string, runErr error) {
	if c == nil || runErr == nil {
		return
	}
	if errors.Is(runErr, context.Canceled) {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	stage = strings.TrimSpace(stage)
	detail := strings.TrimSpace(runErr.Error())
	if sessionID == "" || detail == "" {
		return
	}
	content := "Run stream failed [" + streamClientErrorPathID + "]: " + detail
	if stage != "" {
		content = "Run stream failed [" + streamClientErrorPathID + "/" + stage + "]: " + detail
	}
	req := map[string]string{
		"role":    "system",
		"content": content,
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/messages"
	ctx, cancel := context.WithTimeout(context.Background(), streamErrorLogTimeout)
	defer cancel()

	var resp struct {
		OK bool `json:"ok"`
	}
	_ = c.postJSON(ctx, path, req, &resp, true)
}

func (c *API) getJSON(ctx context.Context, path string, out any, attachAuth bool) error {
	status, body, err := c.request(ctx, http.MethodGet, path, nil, attachAuth)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return decodeAPIError(status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *API) postJSON(ctx context.Context, path string, payload any, out any, attachAuth bool) error {
	status, body, err := c.request(ctx, http.MethodPost, path, payload, attachAuth)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return decodeAPIError(status, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *API) putJSON(ctx context.Context, path string, payload any, out any, attachAuth bool) error {
	status, body, err := c.request(ctx, http.MethodPut, path, payload, attachAuth)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return decodeAPIError(status, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *API) request(ctx context.Context, method, path string, payload any, attachAuth bool) (int, []byte, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
	}
	baseURL, httpClient, _ := c.requestTarget()
	fullURL := baseURL + path

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request payload for %s: %w", path, err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if attachAuth {
		token := strings.TrimSpace(c.Token())
		if token != "" {
			req.Header.Set("X-Swarm-Token", token)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response %s %s: %w", method, path, err)
	}
	return resp.StatusCode, raw, nil
}

func decodeAPIError(status int, raw []byte) error {
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil {
		if strings.TrimSpace(body.Error) != "" {
			return fmt.Errorf("api %d: %s", status, body.Error)
		}
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return fmt.Errorf("api %d", status)
	}
	return fmt.Errorf("api %d: %s", status, text)
}

func previewPermissionRule(rule PermissionRule) string {
	decision := strings.TrimSpace(rule.Decision)
	if decision == "" {
		decision = "allow"
	}
	switch strings.TrimSpace(rule.Kind) {
	case "bash_prefix":
		return fmt.Sprintf("%s bash prefix: %s", decision, strings.TrimSpace(rule.Pattern))
	case "phrase":
		if strings.TrimSpace(rule.Tool) != "" {
			return fmt.Sprintf("%s %s phrase: %s", decision, strings.TrimSpace(rule.Tool), strings.TrimSpace(rule.Pattern))
		}
		return fmt.Sprintf("%s phrase: %s", decision, strings.TrimSpace(rule.Pattern))
	default:
		return fmt.Sprintf("%s tool: %s", decision, strings.TrimSpace(rule.Tool))
	}
}

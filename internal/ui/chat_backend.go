package ui

import "context"

type ChatMessageRecord struct {
	ID        string
	SessionID string
	GlobalSeq uint64
	Role      string
	Content   string
	Metadata  map[string]any
	CreatedAt int64
}

type ChatRunToolScope struct {
	Preset        string
	AllowTools    []string
	DenyTools     []string
	BashPrefixes  []string
	InheritPolicy bool
}

type ChatRunExecutionContext struct {
	WorkspacePath      string
	CWD                string
	WorktreeMode       string
	WorktreeRootPath   string
	WorktreeBranch     string
	WorktreeBaseBranch string
}

type ChatRunRequest struct {
	Prompt           string
	AgentName        string
	Instructions     string
	Compact          bool
	TargetKind       string
	TargetName       string
	Background       bool
	ToolScope        *ChatRunToolScope
	ExecutionContext *ChatRunExecutionContext
}

type ChatSessionLifecycle struct {
	SessionID      string
	RunID          string
	Active         bool
	Phase          string
	StartedAt      int64
	EndedAt        int64
	UpdatedAt      int64
	Generation     uint64
	StopReason     string
	Error          string
	OwnerTransport string
}

type ChatPermissionRecord struct {
	ID                    string
	SessionID             string
	RunID                 string
	CallID                string
	ToolName              string
	ToolArguments         string
	ApprovedArguments     string
	Requirement           string
	Mode                  string
	Status                string
	Decision              string
	Reason                string
	Step                  int
	PermissionRequestedAt int64
	ResolvedAt            int64
	ExecutionStatus       string
	Output                string
	Error                 string
	DurationMS            int64
	StartedAt             int64
	CompletedAt           int64
	CreatedAt             int64
	UpdatedAt             int64
	SavedRulePreview      string
}

type ChatPermissionRule struct {
	ID        string
	Kind      string
	Decision  string
	Tool      string
	Pattern   string
	CreatedAt int64
	UpdatedAt int64
}

type ChatPermissionPolicy struct {
	Version   int
	Rules     []ChatPermissionRule
	UpdatedAt int64
}

type ChatPermissionExplain struct {
	Decision    string
	Source      string
	Reason      string
	ToolName    string
	Command     string
	Rule        *ChatPermissionRule
	RulePreview string
}

type ChatTurnUsage struct {
	ContextWindow   int
	TotalTokens     int64
	CacheReadTokens int64
	Transport       string
	ConnectedViaWS  *bool
}

type ChatUsageSummary struct {
	ContextWindow      int
	TotalTokens        int64
	CacheReadTokens    int64
	RemainingTokens    int64
	Source             string
	LastRunID          string
	LastTransport      string
	LastConnectedViaWS *bool
}

type ChatRunResponse struct {
	Model            string
	Thinking         string
	ReasoningSummary string
	TurnUsage        *ChatTurnUsage
	UsageSummary     *ChatUsageSummary
	UserMessage      ChatMessageRecord
	ToolMessages     []ChatMessageRecord
	AssistantMessage ChatMessageRecord
	Commentary       []ChatMessageRecord
	TargetKind       string
	TargetName       string
}

type ChatRunStreamEvent struct {
	Type         string
	SessionID    string
	RunID        string
	Agent        string
	Step         int
	Delta        string
	Summary      string
	ToolName     string
	CallID       string
	Arguments    string
	Output       string
	RawOutput    string
	Error        string
	DurationMS   int64
	Message      *ChatMessageRecord
	Permission   *ChatPermissionRecord
	TurnUsage    *ChatTurnUsage
	UsageSummary *ChatUsageSummary
	Title        string
	TitleStage   string
	Warning      string
	Lifecycle    *ChatSessionLifecycle
	Result       ChatRunResponse
}

type ChatBackend interface {
	LoadMessages(ctx context.Context, sessionID string, afterSeq uint64, limit int) ([]ChatMessageRecord, error)
	GetSessionUsageSummary(ctx context.Context, sessionID string) (*ChatUsageSummary, error)
	GetSessionMode(ctx context.Context, sessionID string) (string, error)
	SetSessionMode(ctx context.Context, sessionID, mode string) (string, error)
	GetSessionPreference(ctx context.Context, sessionID string) (string, string, string, string, string, int, error)
	SetSessionPreference(ctx context.Context, sessionID, provider, model, thinking, serviceTier, contextMode string) (string, string, string, string, string, int, error)
	GetActiveSessionPlan(ctx context.Context, sessionID string) (ChatSessionPlan, bool, error)
	SaveSessionPlan(ctx context.Context, sessionID string, plan ChatSessionPlan) (ChatSessionPlan, error)
	ListPermissions(ctx context.Context, sessionID string, limit int) ([]ChatPermissionRecord, error)
	ListPendingPermissions(ctx context.Context, sessionID string, limit int) ([]ChatPermissionRecord, error)
	ResolvePermission(ctx context.Context, sessionID, permissionID, action, reason string) (ChatPermissionRecord, error)
	ResolvePermissionWithArguments(ctx context.Context, sessionID, permissionID, action, reason, approvedArguments string) (ChatPermissionRecord, error)
	ResolveAllPermissions(ctx context.Context, sessionID, action, reason string) ([]ChatPermissionRecord, error)
	GetPermissionPolicy(ctx context.Context) (ChatPermissionPolicy, error)
	AddPermissionRule(ctx context.Context, rule ChatPermissionRule) (ChatPermissionRule, error)
	RemovePermissionRule(ctx context.Context, ruleID string) (bool, error)
	ResetPermissionPolicy(ctx context.Context) (ChatPermissionPolicy, error)
	ExplainPermission(ctx context.Context, mode, toolName, arguments string) (ChatPermissionExplain, error)
	StopRun(ctx context.Context, sessionID, runID string) error
	RunTurn(ctx context.Context, sessionID string, req ChatRunRequest) (ChatRunResponse, error)
	RunTurnStream(ctx context.Context, sessionID string, req ChatRunRequest, onEvent func(ChatRunStreamEvent)) (ChatRunResponse, error)
}

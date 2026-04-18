package provideriface

import "context"

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	SessionID         string
	Model             string
	Thinking          string
	Instructions      string
	Input             []map[string]any
	Tools             []ToolDefinition
	ToolChoice        string
	ServiceTier       string
	ContextMode       string
	ContextWindow     int
	ParallelToolCalls bool
	WorkspacePath     string
	ToolInvoker       ToolInvoker
}

type ToolInvocation struct {
	CallID    string
	Name      string
	Arguments string
	Metadata  map[string]any
}

type ToolExecutionResult struct {
	CallID       string
	Name         string
	Output       string
	Error        string
	DurationMS   int64
	TextForModel string
	RestartTurn  bool
}

type ToolInvoker interface {
	ExecuteTool(context.Context, ToolInvocation) (ToolExecutionResult, error)
}

type FunctionCall struct {
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type AssistantPhase string

const (
	AssistantPhaseUnknown     AssistantPhase = ""
	AssistantPhaseCommentary  AssistantPhase = "commentary"
	AssistantPhaseFinalAnswer AssistantPhase = "final_answer"
)

type TokenUsage struct {
	InputTokens      int64            `json:"input_tokens,omitempty"`
	OutputTokens     int64            `json:"output_tokens,omitempty"`
	ThinkingTokens   int64            `json:"thinking_tokens,omitempty"`
	TotalTokens      int64            `json:"total_tokens,omitempty"`
	CacheReadTokens  int64            `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64            `json:"cache_write_tokens,omitempty"`
	Source           string           `json:"source,omitempty"`
	Transport        string           `json:"transport,omitempty"`
	ConnectedViaWS   *bool            `json:"connected_via_websocket,omitempty"`
	APIUsageRaw      map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath  string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory  []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths    []string         `json:"api_usage_paths,omitempty"`
}

type Response struct {
	ID                string             `json:"id,omitempty"`
	Model             string             `json:"model,omitempty"`
	StopReason        string             `json:"stop_reason,omitempty"`
	Text              string             `json:"text,omitempty"`
	ReasoningSummary  string             `json:"reasoning_summary,omitempty"`
	AssistantMessages []AssistantMessage `json:"assistant_messages,omitempty"`
	FunctionCalls     []FunctionCall     `json:"function_calls,omitempty"`
	Usage             TokenUsage         `json:"usage,omitempty"`
	RestartTurn       bool               `json:"restart_turn,omitempty"`
}

type AssistantMessage struct {
	Text  string         `json:"text,omitempty"`
	Phase AssistantPhase `json:"phase,omitempty"`
}

type StreamEventType string

const (
	StreamEventOutputTextDelta       StreamEventType = "response.output_text.delta"
	StreamEventReasoningSummaryDelta StreamEventType = "response.reasoning_summary_text.delta"
	StreamEventAssistantCommentary   StreamEventType = "response.assistant_commentary.delta"
)

type StreamEvent struct {
	Type         StreamEventType
	Delta        string
	Phase        AssistantPhase
	ReasoningKey string
}

type Runner interface {
	ID() string
	CreateResponse(ctx context.Context, req Request) (Response, error)
	CreateResponseStreaming(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error)
}

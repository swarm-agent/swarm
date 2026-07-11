package provideriface

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	// SessionID is the stable durable Swarm session identity used for
	// diagnostics/storage. Providers must not treat it as a cache/session
	// affinity key; use ProviderCacheKey/SessionAffinityKey instead.
	SessionID         string
	ProviderLineageID string
	ContextBranchID   string
	ProviderCacheKey  string
	// TurnAffinityKey identifies one provider transport window. It must rotate
	// across independent turns and restart boundaries while ProviderCacheKey
	// remains stable for the durable thread.
	TurnAffinityKey               string
	SessionAffinityKey            string
	BoundaryReason                string
	PreviousProviderLineageID     string
	PreviousProviderID            string
	PreviousModel                 string
	NewProviderID                 string
	NewModel                      string
	HandoffSummaryMessageID       string
	HandoffSummaryGlobalSeq       uint64
	ProviderLineageStartMessageID string
	ProviderLineageStartRunID     string
	ProviderLineageStartGlobalSeq uint64
	NativeContinuationAllowed     bool
	ForceFreshProviderContext     bool
	Model                         string
	Thinking                      string
	Instructions                  string
	Input                         []map[string]any
	Tools                         []ToolDefinition
	ToolChoice                    string
	ServiceTier                   string
	ContextMode                   string
	ContextWindow                 int
	ContextWindowGeneration       int
	ModelCatalog                  any
	ParallelToolCalls             bool
	WorkspacePath                 string
	ToolInvoker                   ToolInvoker
}

func (r Request) EffectiveProviderCacheKey() string {
	return strings.TrimSpace(firstNonEmpty(r.ProviderCacheKey, r.ProviderLineageID))
}

func (r Request) EffectiveSessionAffinityKey() string {
	return strings.TrimSpace(firstNonEmpty(r.TurnAffinityKey, r.SessionAffinityKey, r.ProviderCacheKey, r.ProviderLineageID))
}

func ShortProviderLineageKey(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(cleaned, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	ServiceTier      string           `json:"service_tier,omitempty"`
	EstimatedCostUSD float64          `json:"estimated_cost_usd,omitempty"`
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
	Raw               map[string]any     `json:"raw,omitempty"`
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

	// Tool-call construction events describe provider/model output while a tool call
	// is being assembled. They are intentionally separate from Swarm runtime tool
	// execution events such as tool.started/tool.delta/tool.completed.
	StreamEventToolCallStarted           StreamEventType = "response.tool_call.started"
	StreamEventToolCallArgumentsDelta    StreamEventType = "response.tool_call.arguments.delta"
	StreamEventToolCallArgumentsSnapshot StreamEventType = "response.tool_call.arguments.snapshot"
	StreamEventToolCallCompleted         StreamEventType = "response.tool_call.completed"
)

type StreamEvent struct {
	Type         StreamEventType
	Delta        string
	Phase        AssistantPhase
	ReasoningKey string

	// Tool-call construction identity/content. Providers should populate the
	// stable fields as soon as they are known; ToolCallIndex may be present before
	// a provider call ID exists. ArgumentsDelta is append-only when available,
	// while ArgumentsSnapshot is a replacement snapshot for providers that stream
	// full argument states instead of byte deltas. Arguments is the final argument
	// string for StreamEventToolCallCompleted.
	ToolCallID        string
	ToolCallIndex     *int
	ToolName          string
	Arguments         string
	ArgumentsDelta    string
	ArgumentsSnapshot string
	Metadata          map[string]any
}

type Runner interface {
	ID() string
	CreateResponse(ctx context.Context, req Request) (Response, error)
	CreateResponseStreaming(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error)
}

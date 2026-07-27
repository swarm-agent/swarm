package provideriface

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

const (
	MediaCapabilityStateAllowed = "allowed"
	MediaCapabilityStateDenied  = "denied"

	MediaAdapterIDOpenAIResponsesV1     = "openai-responses-v1"
	MediaProviderSurfaceOpenAIResponses = "responses_api"
	MediaCredentialSurfaceOpenAIAPIKey  = "openai_api_key"
	MediaAdapterIDCodexChatGPTV1        = "codex-chatgpt-v1"
	MediaProviderSurfaceCodexChatGPT    = "chatgpt_codex"
	MediaCredentialSurfaceCodexOAuth    = "codex_oauth"
)

// MediaAdapterCapability is an adapter's exact, implemented admission ceiling.
// It is intentionally independent from model-catalog claims.
type MediaAdapterCapability struct {
	Modality     string   `json:"modality"`
	Semantics    string   `json:"semantics"`
	MIMETypes    []string `json:"mime_types,omitempty"`
	FileTypes    []string `json:"file_types,omitempty"`
	ContentTypes []string `json:"content_types,omitempty"`
	MaxBytes     int64    `json:"max_bytes"`
	MaxCount     int      `json:"max_count"`
}

type MediaAdapterDeclaration struct {
	AdapterID             string                   `json:"adapter_id"`
	ProviderID            string                   `json:"provider_id"`
	ProviderSurface       string                   `json:"provider_surface"`
	CredentialSurface     string                   `json:"credential_surface"`
	CredentialFingerprint string                   `json:"credential_fingerprint,omitempty"`
	Inputs                []MediaAdapterCapability `json:"inputs,omitempty"`
}

type MediaContractCapability struct {
	Modality     string   `json:"modality"`
	State        string   `json:"state"`
	Reason       string   `json:"reason"`
	Semantics    string   `json:"semantics,omitempty"`
	MIMETypes    []string `json:"mime_types,omitempty"`
	FileTypes    []string `json:"file_types,omitempty"`
	ContentTypes []string `json:"content_types,omitempty"`
	MaxBytes     int64    `json:"max_bytes,omitempty"`
	MaxCount     int      `json:"max_count,omitempty"`
	Provenance   []string `json:"provenance,omitempty"`
}

// SessionMediaContract is the sole normalized result consumed by media schema,
// prompts, execution admission, capability projection, and provider lineage.
type SessionMediaContract struct {
	Version               int                       `json:"version"`
	ProviderID            string                    `json:"provider_id"`
	Model                 string                    `json:"model"`
	ProviderSurface       string                    `json:"provider_surface,omitempty"`
	CredentialSurface     string                    `json:"credential_surface,omitempty"`
	CredentialFingerprint string                    `json:"credential_fingerprint,omitempty"`
	AdapterID             string                    `json:"adapter_id,omitempty"`
	SnapshotID            string                    `json:"snapshot_id,omitempty"`
	SnapshotVersion       string                    `json:"snapshot_version,omitempty"`
	SnapshotSource        string                    `json:"snapshot_source,omitempty"`
	ExecutionMode         string                    `json:"execution_mode,omitempty"`
	WorkspaceScope        string                    `json:"workspace_scope,omitempty"`
	SessionScope          string                    `json:"session_scope,omitempty"`
	Capabilities          []MediaContractCapability `json:"capabilities,omitempty"`
	DenialReasons         []string                  `json:"denial_reasons,omitempty"`
	Hash                  string                    `json:"hash"`
}

// SessionMediaPayload carries authorized immutable bytes inside the daemon.
// Bytes are deliberately excluded from JSON so request diagnostics, exports,
// and realtime serialization cannot disclose asset contents.
type SessionMediaPayload struct {
	AssetID      string `json:"asset_id"`
	Modality     string `json:"modality"`
	MIMEType     string `json:"mime_type"`
	FileType     string `json:"file_type,omitempty"`
	DigestSHA256 string `json:"digest_sha256"`
	Size         int64  `json:"size"`
	Bytes        []byte `json:"-"`
}

type Request struct {
	// SessionID is the stable durable Swarm session identity used for
	// diagnostics/storage. Providers must not treat it as a cache/session
	// affinity key; use ProviderCacheKey/SessionAffinityKey instead.
	SessionID                 string
	ProviderLineageID         string
	ExecutionEpochID          string
	ProviderConfigurationHash string
	ContextBranchID           string
	ProviderCacheKey          string
	SessionAffinityKey        string
	// TransportAffinityKey identifies a compatible reusable transport and must
	// not be coupled to an execution epoch or provider continuation chain.
	TransportAffinityKey          string
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
	// Provider chain lifecycle is independent from transport lifecycle. A new
	// chain clears provider continuation state, but may reuse a compatible healthy
	// transport. ResetTransport always wins over ReuseTransport.
	StartNewChain     bool
	AllowContinuation bool
	ReuseTransport    bool
	ResetTransport    bool
	// NativeContinuationAllowed and ForceFreshProviderContext are retained as
	// inputs for callers not yet migrated to the explicit lifecycle policy.
	NativeContinuationAllowed bool
	ForceFreshProviderContext bool
	Model                     string
	Thinking                  string
	Instructions              string
	Input                     []map[string]any
	Tools                     []ToolDefinition
	ToolChoice                string
	ServiceTier               string
	ContextMode               string
	ContextWindow             int
	ModelCatalog              any
	MediaContract             SessionMediaContract
	ParallelToolCalls         bool
	WorkspacePath             string
	ToolInvoker               ToolInvoker
}

func (r Request) EffectiveProviderCacheKey() string {
	return strings.TrimSpace(firstNonEmpty(r.ProviderCacheKey, r.ProviderLineageID))
}

func (r Request) EffectiveSessionAffinityKey() string {
	return strings.TrimSpace(firstNonEmpty(r.SessionAffinityKey, r.ProviderCacheKey, r.ProviderLineageID))
}

// WithRuntimeContext adds request-scoped, backend-authoritative model and time
// context after stable lineage/cache keys have been resolved.
func (r Request) WithRuntimeContext(providerID string, now time.Time) Request {
	currentProvider := strings.TrimSpace(firstNonEmpty(r.NewProviderID, providerID))
	currentModel := strings.TrimSpace(firstNonEmpty(r.NewModel, r.Model))
	previousProvider := strings.TrimSpace(r.PreviousProviderID)
	previousModel := strings.TrimSpace(r.PreviousModel)
	identityChanged := strings.TrimSpace(r.PreviousProviderLineageID) != "" &&
		previousProvider != "" && previousModel != "" &&
		(!strings.EqualFold(previousProvider, currentProvider) || previousModel != currentModel)

	contextBlock := RuntimeContextInstructions(currentProvider, currentModel, previousProvider, previousModel, identityChanged, now)
	r.Instructions = strings.TrimSpace(strings.TrimSpace(r.Instructions) + "\n\n" + contextBlock)
	return r
}

// RuntimeContextInstructions formats the canonical dynamic context supplied to
// conversational provider requests. Callers must not include its timestamp in
// lineage or provider-cache identity.
func RuntimeContextInstructions(currentProvider, currentModel, previousProvider, previousModel string, identityChanged bool, now time.Time) string {
	currentProvider = strings.TrimSpace(currentProvider)
	currentModel = strings.TrimSpace(currentModel)
	previousProvider = strings.TrimSpace(previousProvider)
	previousModel = strings.TrimSpace(previousModel)
	var b strings.Builder
	b.WriteString("[request-runtime-context]\n")
	b.WriteString("Backend-authoritative context for this provider request; do not infer or claim a different identity or time.\n")
	fmt.Fprintf(&b, "- current_utc_time: %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- current_provider: %s\n", currentProvider)
	fmt.Fprintf(&b, "- current_model: %s", currentModel)
	if identityChanged {
		b.WriteString("\n- provider_model_change: The resolved provider/model changed for this request.\n")
		fmt.Fprintf(&b, "- previous_provider: %s\n", previousProvider)
		fmt.Fprintf(&b, "- previous_model: %s", previousModel)
	}
	return b.String()
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
	CallID           string
	Name             string
	Output           string
	Error            string
	DurationMS       int64
	PermissionWaitMS int64
	TextForModel     string
	Media            *SessionMediaPayload
	RestartTurn      bool
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

// StreamEventDeltaMode makes the provider adapter's text operation explicit.
// Reasoning summary events must set append for raw chunks or replace for full snapshots.
type StreamEventDeltaMode string

const (
	StreamEventDeltaModeAppend  StreamEventDeltaMode = "append"
	StreamEventDeltaModeReplace StreamEventDeltaMode = "replace"

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
	DeltaMode    StreamEventDeltaMode
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

type ExecutionEpochContextMode string

const (
	// ExecutionEpochContextStatelessFullInput sends the complete, explicitly
	// selected epoch input on every request and carries no provider continuation
	// object between calls.
	ExecutionEpochContextStatelessFullInput ExecutionEpochContextMode = "stateless_full_input"
	// ExecutionEpochContextResponsesChain permits provider-native continuation
	// only inside one execution epoch. A new epoch always starts a new chain.
	ExecutionEpochContextResponsesChain ExecutionEpochContextMode = "responses_chain"
)

type ExecutionEpochLifecycleCapabilities struct {
	ContextMode                ExecutionEpochContextMode
	EpochScopedCacheKey        bool
	EpochScopedSessionAffinity bool
	TransportReusable          bool
}

func (c ExecutionEpochLifecycleCapabilities) Valid() bool {
	switch c.ContextMode {
	case ExecutionEpochContextStatelessFullInput, ExecutionEpochContextResponsesChain:
		return true
	default:
		return false
	}
}

// ExecutionEpochLifecycleRunner requires every conversational runner to declare
// how provider context, caches, affinity, and transport behave at epoch
// boundaries. Callers must fail closed when the declaration is absent or invalid.
type ExecutionEpochLifecycleRunner interface {
	ExecutionEpochLifecycle() ExecutionEpochLifecycleCapabilities
}

// MediaCapabilityRunner declares only media admission implemented by the exact
// provider/credential surface selected for this request.
type MediaCapabilityRunner interface {
	MediaCapabilityDeclaration(context.Context) (MediaAdapterDeclaration, error)
}

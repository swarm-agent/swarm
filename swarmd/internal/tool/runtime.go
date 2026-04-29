package tool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/fff"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	todoruntime "swarm/packages/swarmd/internal/todo"
	uisettings "swarm/packages/swarmd/internal/uisettings"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	defaultReadMaxLines = 2000
	maxReadMaxLines     = 2000
	maxReadLineBytes    = 1024 * 1024
	maxReadLineChars    = 16 * 1024
	maxEditBytes        = 2 * 1024 * 1024
	maxEditPreviewRunes = 1200
	maxCommandOutput    = 32 * 1024
	// Keep /output useful for real command logs while still bounding in-memory capture.
	maxBashOutputViewerBytes            = 4 * 1024 * 1024
	bashStreamEmitChunkBytes            = 1024
	maxSearchCommandOut                 = 256 * 1024
	defaultBashTimeout                  = 2 * time.Minute
	maxBashTimeout                      = 30 * time.Minute
	defaultGitTimeout                   = 20 * time.Second
	defaultSearchTimeout                = 8 * time.Second
	maxSearchTimeout                    = 45 * time.Second
	defaultSearchResults                = 100
	maxSearchResults                    = 4000
	maxSearchQueries                    = 32
	defaultAgenticSearchFiles           = 200
	maxAgenticSearchFiles               = 5000
	defaultAgenticSearchResults         = 200
	maxAgenticSearchResults             = 6000
	defaultAgenticSearchContextBefore   = 1
	defaultAgenticSearchContextAfter    = 1
	maxAgenticSearchContextLines        = 12
	defaultAgenticSearchQueryPage       = 24
	defaultAgenticSearchMatchBudget     = 2400
	defaultAgenticSearchCandidateBudget = 2400
	defaultAgenticSearchParallelQueries = 6
	maxAgenticSearchParallelQueries     = 32
	maxAgenticSearchFileContexts        = 20
	maxAgenticSearchReadSuggestions     = 12
	defaultListEntries                  = 120
	maxListEntries                      = 2000
	defaultListDepth                    = 4
	maxListDepth                        = 24
	maxListScanEntries                  = 20000
	searchResultPageSlack               = 8
	searchDefinitionAfterContext        = 5
	compactSearchHitRunes               = 140
	compactSearchContextRunes           = 120
	maxGrepLineChars                    = 500
	maxSafetyScanChars                  = 16 * 1024
	maxSkillContentBytes                = 16 * 1024
	maxSkillListPreview                 = 24
	defaultWebSearchResults             = 8
	maxWebSearchResults                 = 25
	maxWebSearchQueries                 = 16
	defaultWebSearchParallelQueries     = 4
	maxWebSearchParallelQueries         = 16
	defaultWebSearchTimeout             = 12 * time.Second
	maxWebSearchTimeout                 = 45 * time.Second
	defaultWebFetchURLs                 = 6
	maxWebFetchURLs                     = 20
	defaultWebFetchTimeout              = 18 * time.Second
	maxWebFetchTimeout                  = 50 * time.Second
	webFetchModeLight                   = "light"
	webFetchModeDeep                    = "deep"
	webFetchModeFull                    = "full"
	defaultWebFetchRetrievalMode        = webFetchModeLight
	defaultWebFetchLightTextChars       = 1200
	defaultWebFetchDeepTextChars        = 8000
	defaultWebFetchFullTextChars        = 32000
	maxWebFetchTextCharsPerURL          = 200000
	defaultWebFetchLightTotalTextChars  = 8000
	defaultWebFetchDeepTotalTextChars   = 32000
	defaultWebFetchFullTotalTextChars   = 160000
	maxWebFetchTotalTextChars           = 1000000
	maxWebResultTextChars               = 8000
	maxWebResponseBytes                 = 8 * 1024 * 1024
	defaultWebDownloadDir               = ".swarm/downloads"
	defaultExaSearchURL                 = "https://api.exa.ai/search"
	defaultExaContentsURL               = "https://api.exa.ai/contents"
	manageThemeActionInspect            = "inspect"
	manageThemeActionGet                = "get"
	manageThemeActionCreate             = "create"
	manageThemeActionUpdate             = "update"
	manageThemeActionDelete             = "delete"
	manageThemeActionSet                = "set"
)

var (
	ansiCSIRegex  = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	ansiOSCRegex  = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	promptMarkers = []string{
		"ignore previous instructions",
		"ignore all previous instructions",
		"disregard previous instructions",
		"forget previous instructions",
		"do not follow previous instructions",
		"reveal the system prompt",
		"reveal system prompt",
		"developer message",
		"you are now",
		"jailbreak",
	}
	errListScanLimit = errors.New("list scan limit reached")
)

type Runtime struct {
	maxParallel       int
	httpClient        *http.Client
	exaConfigResolver func(context.Context) (ExaRuntimeConfig, error)
	sessions          manageWorktreeSessionService
	workspace         manageWorktreeWorkspaceService
	worktrees         manageWorktreeConfigService
	agents            manageAgentService
	todos             manageTodoService
	uiSettings        manageThemeUISettingsService
	themeWorkspace    manageThemeWorkspaceService
}

type ExaRuntimeConfig struct {
	Enabled     bool
	Source      string
	APIKey      string
	SearchURL   string
	ContentsURL string
	MCPURL      string
}

type BashSandboxConfig struct {
	Enabled bool
	RunID   string
}

type bashSandboxContextKey struct{}

type WorkspaceScope struct {
	PrimaryPath string
	Roots       []string
	SessionID   string
}

type manageWorktreeSessionService interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	ListTopSessionsByWorkspace(workspacePaths []string, perWorkspaceLimit int) ([]pebblestore.WorkspaceSessionList, error)
}

type manageWorktreeWorkspaceService interface {
	CurrentBinding() (workspaceruntime.Resolution, bool, error)
	ScopeForPath(path string) (workspaceruntime.Scope, error)
	ListKnown(limit int) ([]workspaceruntime.Entry, error)
}

type manageWorktreeConfigService interface {
	GetConfig(workspacePath string) (worktreeruntime.Config, error)
}

type manageAgentService interface {
	ListState(limit int) (agentruntime.State, error)
	ReplaceManagedState(state agentruntime.State, syncProfiles, syncCustomTools bool) (agentruntime.State, int64, *pebblestore.EventEnvelope, error)
	ListCustomTools(limit int) ([]pebblestore.AgentCustomToolDefinition, error)
	GetCustomTool(name string) (pebblestore.AgentCustomToolDefinition, bool, error)
	PutCustomTool(definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error)
	DeleteCustomTool(name string) (bool, error)
	AssignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	UnassignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	GetProfile(name string) (pebblestore.AgentProfile, bool, error)
	PreviewUpsert(input agentruntime.UpsertInput) (agentruntime.PreviewUpsertResult, error)
	Upsert(input agentruntime.UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	ActivatePrimary(name string) (string, int64, *pebblestore.EventEnvelope, error)
	Delete(name string) (agentruntime.DeleteResult, int64, *pebblestore.EventEnvelope, error)
	SetActiveSubagent(purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
	DeleteActiveSubagent(purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
}

type manageTodoService interface {
	List(workspacePath string, options ...todoruntime.ListOptions) ([]pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, error)
	Create(input todoruntime.CreateInput) (pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	Update(input todoruntime.UpdateInput, options ...todoruntime.ListOptions) (pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	Delete(workspacePath, itemID string, options ...todoruntime.ListOptions) (pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	DeleteDone(workspacePath string, options ...todoruntime.ListOptions) ([]pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	DeleteAll(workspacePath string, options ...todoruntime.ListOptions) ([]pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	Reorder(input todoruntime.ReorderInput, options ...todoruntime.ListOptions) ([]pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	SetInProgress(workspacePath, itemID string, options ...todoruntime.ListOptions) (pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
	ApplyBatch(workspacePath string, operations []todoruntime.BatchOperation, options ...todoruntime.ListOptions) ([]todoruntime.BatchResult, []pebblestore.WorkspaceTodoItem, pebblestore.WorkspaceTodoSummary, *pebblestore.EventEnvelope, error)
}

type manageThemeUISettingsService interface {
	Get() (uisettings.UISettings, error)
	Set(settings uisettings.UISettings) (uisettings.UISettings, error)
}

type manageThemeWorkspaceService interface {
	SetThemeID(path, themeID string) (workspaceruntime.Resolution, error)
	ScopeForPath(path string) (workspaceruntime.Scope, error)
	ListKnown(limit int) ([]workspaceruntime.Entry, error)
}

type manageWorktreeSessionRecord = pebblestore.SessionSnapshot

type manageWorktreeWorkspaceSessionList = pebblestore.WorkspaceSessionList

type manageWorktreeWorkspaceBinding = workspaceruntime.Resolution

type manageWorktreeWorkspaceScopeInfo = workspaceruntime.Scope

type manageWorktreeWorkspaceEntry = workspaceruntime.Entry

type manageWorktreeConfig = worktreeruntime.Config

type workspaceScopeContextKey struct{}

func WithBashSandbox(parent context.Context, config BashSandboxConfig) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, bashSandboxContextKey{}, config)
}

func WithWorkspaceScope(parent context.Context, scope WorkspaceScope) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	normalized := normalizeWorkspaceScope(scope.PrimaryPath, scope.Roots)
	normalized.SessionID = strings.TrimSpace(scope.SessionID)
	return context.WithValue(parent, workspaceScopeContextKey{}, normalized)
}

func ExecuteForWorkspaceScope(ctx context.Context, scope WorkspaceScope, call Call) (string, error) {
	runtime := NewRuntime(0)
	ctx = WithWorkspaceScope(ctx, scope)
	results := runtime.ExecuteBatch(ctx, scope.PrimaryPath, []Call{call})
	if len(results) == 0 {
		return "", errors.New("tool execution failed")
	}
	if strings.TrimSpace(results[0].Error) != "" {
		return strings.TrimSpace(results[0].Output), errors.New(strings.TrimSpace(results[0].Error))
	}
	return strings.TrimSpace(results[0].Output), nil
}

func (r *Runtime) ExecuteForWorkspaceScopeWithRuntime(ctx context.Context, scope WorkspaceScope, call Call) (string, error) {
	if r == nil {
		return ExecuteForWorkspaceScope(ctx, scope, call)
	}
	ctx = WithWorkspaceScope(ctx, scope)
	results := r.ExecuteBatch(ctx, scope.PrimaryPath, []Call{call})
	if len(results) == 0 {
		return "", errors.New("tool execution failed")
	}
	if strings.TrimSpace(results[0].Error) != "" {
		return strings.TrimSpace(results[0].Output), errors.New(strings.TrimSpace(results[0].Error))
	}
	return strings.TrimSpace(results[0].Output), nil
}

func bashSandboxConfigFromContext(ctx context.Context) BashSandboxConfig {
	if ctx == nil {
		return BashSandboxConfig{}
	}
	config, ok := ctx.Value(bashSandboxContextKey{}).(BashSandboxConfig)
	if !ok {
		return BashSandboxConfig{}
	}
	return config
}

func workspaceScopeFromContext(ctx context.Context, workspacePath string) WorkspaceScope {
	scope := normalizeWorkspaceScope(workspacePath, nil)
	if ctx == nil {
		return scope
	}
	override, ok := ctx.Value(workspaceScopeContextKey{}).(WorkspaceScope)
	if !ok {
		return scope
	}
	if strings.TrimSpace(override.PrimaryPath) == "" && len(override.Roots) == 0 {
		return scope
	}
	normalized := normalizeWorkspaceScope(override.PrimaryPath, override.Roots)
	normalized.SessionID = strings.TrimSpace(override.SessionID)
	return normalized
}

type Definition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type Call struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Result struct {
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type Progress struct {
	Stage  string `json:"stage"`
	Output string `json:"output,omitempty"`
}

type BatchResultHandler func(index int, call Call, result Result)
type BatchProgressHandler func(index int, call Call, progress Progress)

func NewRuntime(maxParallel int) *Runtime {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return &Runtime{
		maxParallel: maxParallel,
		httpClient: &http.Client{
			Timeout: maxWebFetchTimeout + 5*time.Second,
		},
	}
}

func (r *Runtime) SetExaConfigResolver(resolver func(context.Context) (ExaRuntimeConfig, error)) {
	if r == nil {
		return
	}
	r.exaConfigResolver = resolver
}

func (r *Runtime) SetManageWorktreeServices(sessions manageWorktreeSessionService, workspace manageWorktreeWorkspaceService, worktrees manageWorktreeConfigService) {
	if r == nil {
		return
	}
	r.sessions = sessions
	r.workspace = workspace
	r.worktrees = worktrees
}

func (r *Runtime) SetManageAgentService(agents manageAgentService) {
	if r == nil {
		return
	}
	r.agents = agents
}

func (r *Runtime) SetManageTodoService(todos manageTodoService) {
	if r == nil {
		return
	}
	r.todos = todos
}

func (r *Runtime) SetManageThemeServices(uiSettings manageThemeUISettingsService, workspace manageThemeWorkspaceService) {
	if r == nil {
		return
	}
	r.uiSettings = uiSettings
	r.themeWorkspace = workspace
}

func (r *Runtime) Definitions() []Definition {
	return []Definition{
		{
			Type:        "function",
			Name:        "read",
			Description: "Read file content from the current workspace with line-number pagination for high-context investigation",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "Path to file (absolute or workspace-relative)"},
					"line_start": map[string]any{"type": "integer", "description": "First 1-based line to include (default 1)"},
					"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return (default 2000, max 2000). Safe to request up to 2000 lines when context requires it; page and continue reading when deeper context is needed."},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "write",
			Description: "Write content to a file in the current workspace",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path to file (absolute or workspace-relative)"},
					"content": map[string]any{"type": "string", "description": "File content to write"},
					"append":  map[string]any{"type": "boolean", "description": "Append to file instead of overwrite"},
				},
				"required":             []string{"path", "content"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "bash",
			Description: "Execute a shell command in the current workspace directory",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":    map[string]any{"type": "string", "description": "Shell command to execute"},
					"timeout_ms": map[string]any{"type": "integer", "description": "Timeout in milliseconds (default 120000, max 1800000)"},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "git_status",
			Description: "Inspect repository status using Git without shell indirection",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"short":  map[string]any{"type": "boolean", "description": "Use short status output"},
					"branch": map[string]any{"type": "boolean", "description": "Show branch information"},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "git_diff",
			Description: "Inspect repository diffs using Git without shell indirection",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"staged":   map[string]any{"type": "boolean", "description": "Show staged changes instead of working tree changes"},
					"pathspec": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional pathspec arguments passed to git diff after --"},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "git_add",
			Description: "Stage files using Git without shell indirection",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pathspec": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Pathspec arguments to stage"},
					"all":      map[string]any{"type": "boolean", "description": "Stage all tracked modifications"},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "git_commit",
			Description: "Create a Git commit using the user's existing Git identity and signing configuration",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Commit message"},
					"all":     map[string]any{"type": "boolean", "description": "Stage tracked modifications before committing"},
				},
				"required":             []string{"message"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "search",
			Description: "Canonical workspace search powered directly by FFF. Supports single-query and multi-query search in one call. Prefer narrow path/include scopes first, and use `queries` for multi-symbol search instead of packing OR syntax into one string. Returns line-level content matches when available, with stable summaries, truncation signals, and file metadata to guide follow-up reads.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":     map[string]any{"type": "string", "description": "Legacy single-query alias. Use an exact symbol, error string, config key, or short natural fragment."},
					"query":       map[string]any{"type": "string", "description": "Single search query. Preferred over `pattern` for new callers."},
					"queries":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional multi-query batch for one search call. Use this for parallel/multi-symbol search within the same path/include scope."},
					"path":        map[string]any{"type": "string", "description": "Search root directory (absolute or workspace-relative). Keep this as narrow as possible for model-readable results."},
					"include":     map[string]any{"type": "string", "description": "Optional file include glob such as `*.go`. This is the canonical way to scope search to file types."},
					"max_results": map[string]any{"type": "integer", "description": "Maximum merged results to return (default 100, max 4000). If results truncate, narrow path/include/query scope and rerun instead of broadening search scope."},
					"timeout_ms":  map[string]any{"type": "integer", "description": "Search timeout in milliseconds (default 8000, max 45000). Used for FFF scan wait and grep time budget."},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "websearch",
			Description: "Run Exa /search with optional parallel multi-query fan-out and optional nested contents retrieval",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Single search query"},
					"queries": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional multi-query batch for parallel retrieval",
					},
					"num_results": map[string]any{"type": "integer", "description": "Maximum results per query (default 8, max 25)"},
					"search_type": map[string]any{"type": "string", "description": "Exa search type (default auto): instant|auto|fast|neural|deep|deep-reasoning"},
					"additional_queries": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional Exa additionalQueries for deep search variants",
					},
					"category":      map[string]any{"type": "string", "description": "Optional Exa category filter"},
					"user_location": map[string]any{"type": "string", "description": "Optional two-letter ISO country code"},
					"include_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional allowlist of domains",
					},
					"exclude_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional blocklist of domains",
					},
					"start_crawl_date":     map[string]any{"type": "string", "description": "Optional ISO 8601 crawl-date lower bound"},
					"end_crawl_date":       map[string]any{"type": "string", "description": "Optional ISO 8601 crawl-date upper bound"},
					"start_published_date": map[string]any{"type": "string", "description": "Optional ISO 8601 published-date lower bound"},
					"end_published_date":   map[string]any{"type": "string", "description": "Optional ISO 8601 published-date upper bound"},
					"include_text":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional Exa includeText filter"},
					"exclude_text":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional Exa excludeText filter"},
					"moderation":           map[string]any{"type": "boolean", "description": "Enable Exa moderation filtering"},
					"system_prompt":        map[string]any{"type": "string", "description": "Optional Exa deep-search systemPrompt"},
					"output_schema":        map[string]any{"type": "object", "description": "Optional Exa deep-search outputSchema"},
					"contents": map[string]any{
						"type":        "object",
						"description": "Optional Exa /search contents request: text, highlights, summary, subpages, subpage_target, extras, max_age_hours, livecrawl_timeout_ms",
					},
					"timeout_ms": map[string]any{
						"type":        "integer",
						"description": "Request timeout in milliseconds (default 12000, max 45000)",
					},
					"max_parallel_queries": map[string]any{
						"type":        "integer",
						"description": "Parallel query fan-out (default 4, max 16)",
					},
				},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "webfetch",
			Description: "Fetch content for selected URLs through Exa /contents using the current Exa contents request contract",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "Single URL to fetch"},
					"urls": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "One or more URLs to fetch",
					},
					"max_urls": map[string]any{"type": "integer", "description": "Maximum URLs to process (default 6, max 20)"},
					"text": map[string]any{
						"description": "Exa text option: boolean or object with max_characters, include_html_tags, verbosity (compact|standard|full), include_sections, exclude_sections; main/article normalize to body",
						"anyOf": []any{
							map[string]any{"type": "boolean"},
							map[string]any{
								"type":                 "object",
								"properties":           map[string]any{},
								"required":             []string{},
								"additionalProperties": true,
							},
						},
					},
					"highlights": map[string]any{
						"description": "Exa highlights option: boolean or object with max_characters, num_sentences, highlights_per_url, query",
						"anyOf": []any{
							map[string]any{"type": "boolean"},
							map[string]any{
								"type":                 "object",
								"properties":           map[string]any{},
								"required":             []string{},
								"additionalProperties": true,
							},
						},
					},
					"summary":  map[string]any{"type": "object", "description": "Optional Exa summary object with query and schema"},
					"subpages": map[string]any{"type": "integer", "description": "Optional Exa subpages count"},
					"subpage_target": map[string]any{
						"description": "Optional Exa subpageTarget string or string[]",
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
					},
					"extras":               map[string]any{"type": "object", "description": "Optional Exa extras object with links and image_links"},
					"max_age_hours":        map[string]any{"type": "integer", "description": "Optional Exa maxAgeHours value"},
					"livecrawl_timeout_ms": map[string]any{"type": "integer", "description": "Optional Exa livecrawlTimeout in milliseconds"},
					"timeout_ms": map[string]any{
						"type":        "integer",
						"description": "Request timeout in milliseconds (default 18000, max 50000)",
					},
				},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "webdownload",
			Description: "Download full URL contents via Exa /contents to workspace files when context would be too large",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "Single URL to download"},
					"urls": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "One or more URLs to download",
					},
					"max_urls": map[string]any{"type": "integer", "description": "Maximum URLs to download (default 6, max 20)"},
					"livecrawl": map[string]any{
						"type":        "string",
						"description": "Optional livecrawl mode: never|fallback|always|auto",
					},
					"timeout_ms": map[string]any{
						"type":        "integer",
						"description": "Request timeout in milliseconds (default 18000, max 50000)",
					},
					"output_dir": map[string]any{
						"type":        "string",
						"description": "Workspace-relative output directory (default .swarm/downloads)",
					},
					"filename_mode": map[string]any{
						"type":        "string",
						"description": "Filename mode: host_slug or sha1 (default host_slug)",
					},
				},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "list",
			Description: "List workspace files/directories (flat or tree mode) with pagination",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        map[string]any{"type": "string", "description": "List root directory (absolute or workspace-relative)"},
					"mode":        map[string]any{"type": "string", "description": "List mode: flat or tree (default flat)"},
					"max_entries": map[string]any{"type": "integer", "description": "Maximum entries to return (default 120, max 2000)"},
					"max_depth":   map[string]any{"type": "integer", "description": "Maximum depth for tree mode (default 4, max 24)"},
					"cursor":      map[string]any{"type": "integer", "description": "Offset cursor for pagination (default 0)"},
				},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "edit",
			Description: "Edit a text file by replacing one or more exact string matches",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        map[string]any{"type": "string", "description": "Path to file (absolute or workspace-relative)"},
					"old_string":  map[string]any{"type": "string", "description": "Exact text to replace for single-edit mode. Ignored when edits is provided."},
					"new_string":  map[string]any{"type": "string", "description": "Replacement text for single-edit mode. Ignored when edits is provided."},
					"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence in single-edit mode (default false). When edits is provided, this is the default for items that omit replace_all."},
					"edits": map[string]any{
						"type":        "array",
						"description": "Single-file multi-edit mode. Apply edits in order and write once after all edits validate. When edits is provided, it is the authoritative edit list.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_string":  map[string]any{"type": "string", "description": "Exact text to replace"},
								"new_string":  map[string]any{"type": "string", "description": "Replacement text"},
								"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence for this edit (default false)"},
							},
							"required":             []string{"old_string", "new_string"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "ask-user",
			Description: "Request a user decision/input through the permission interaction flow",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":    map[string]any{"type": "string", "description": "Optional modal title"},
					"context":  map[string]any{"type": "string", "description": "Optional context shown above questions"},
					"question": map[string]any{"type": "string", "description": "Single-question prompt shown to the user"},
					"options": map[string]any{
						"type": "array",
						"items": map[string]any{
							"oneOf": []any{
								map[string]any{"type": "string"},
								map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":        map[string]any{"type": "string"},
										"value":        map[string]any{"type": "string"},
										"description":  map[string]any{"type": "string"},
										"allow_custom": map[string]any{"type": "boolean"},
										"allowCustom":  map[string]any{"type": "boolean"},
									},
									"required":             []string{},
									"additionalProperties": false,
								},
							},
						},
						"description": "Optional choices for the single-question path",
					},
					"questions": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":       map[string]any{"type": "string"},
								"header":   map[string]any{"type": "string"},
								"question": map[string]any{"type": "string"},
								"required": map[string]any{"type": "boolean"},
								"options": map[string]any{
									"type": "array",
									"items": map[string]any{
										"oneOf": []any{
											map[string]any{"type": "string"},
											map[string]any{
												"type": "object",
												"properties": map[string]any{
													"label":        map[string]any{"type": "string"},
													"value":        map[string]any{"type": "string"},
													"description":  map[string]any{"type": "string"},
													"allow_custom": map[string]any{"type": "boolean"},
													"allowCustom":  map[string]any{"type": "boolean"},
												},
												"required":             []string{},
												"additionalProperties": false,
											},
										},
									},
								},
							},
							"required":             []string{"question"},
							"additionalProperties": false,
						},
						"description": "Optional multi-question payload for structured user input",
					},
				},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "skill-use",
			Description: "Load a discovered skill by name so it can guide the current run",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skill": map[string]any{"type": "string", "description": "Skill name or canonical id"},
				},
				"required":             []string{"skill"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage-skill",
			Description: "Inspect and manage workspace skills under .agents/skills; call with {\"action\":\"inspect\"} for usage details; supports inspect/list/get/create/update/delete, and create/update/delete return approval-ready previews unless confirm=true",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "description": "Action: inspect|list|get|create|update|delete"},
					"skill":   map[string]any{"type": "string", "description": "Skill name or canonical id"},
					"name":    map[string]any{"type": "string", "description": "Skill display name; used for create/update when skill is omitted"},
					"content": map[string]any{"type": "string", "description": "Proposed SKILL.md content for create/update"},
					"confirm": map[string]any{"type": "boolean", "description": "Set true after approval to apply the proposed change to disk"},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage-agent",
			Description: "Inspect and manage saved agents and custom tools; call with {\"action\":\"inspect\"} first for usage details; supports inspect/list/get/create/update/delete/activate_primary/set_active_subagent/remove_active_subagent/create_custom_tool/update_custom_tool/delete_custom_tool/assign_custom_tool/unassign_custom_tool, and mutating actions return approval-ready before/after previews unless confirm=true",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":    map[string]any{"type": "string", "description": "Action: inspect|list|get|create|update|delete|activate_primary|set_active_subagent|remove_active_subagent|create_custom_tool|update_custom_tool|delete_custom_tool|assign_custom_tool|unassign_custom_tool"},
					"agent":     map[string]any{"type": "string", "description": "Agent name or canonical id"},
					"name":      map[string]any{"type": "string", "description": "Agent display name; used for create/update when agent is omitted"},
					"tool_name": map[string]any{"type": "string", "description": "Custom tool name for delete/assign/unassign actions"},
					"content": map[string]any{
						"description": "Structured agent profile, custom tool, or assignment payload. Prefer an object; legacy JSON-object strings are still accepted.",
						"oneOf": []any{
							map[string]any{
								"type": "object",
								"properties": map[string]any{
									"name":                   map[string]any{"type": "string"},
									"agent":                  map[string]any{"type": "string"},
									"purpose":                map[string]any{"type": "string"},
									"tool_name":              map[string]any{"type": "string"},
									"kind":                   map[string]any{"type": "string", "description": "fixed_bash"},
									"command":                map[string]any{"type": "string"},
									"mode":                   map[string]any{"type": "string", "description": "primary|subagent"},
									"description":            map[string]any{"type": "string"},
									"provider":               map[string]any{"type": "string"},
									"model":                  map[string]any{"type": "string"},
									"thinking":               map[string]any{"type": "string"},
									"prompt":                 map[string]any{"type": "string"},
									"execution_setting":      map[string]any{"type": "string", "description": "read|readwrite"},
									"exit_plan_mode_enabled": map[string]any{"type": "boolean"},
									"enabled":                map[string]any{"type": "boolean"},
									"tool_scope": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"preset":         map[string]any{"type": "string"},
											"allow_tools":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
											"deny_tools":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
											"bash_prefixes":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
											"inherit_policy": map[string]any{"type": "boolean"},
										},
										"additionalProperties": false,
									},
								},
								"additionalProperties": false,
							},
							map[string]any{"type": "string", "description": "Legacy JSON-encoded object payload"},
						},
					},
					"confirm": map[string]any{"type": "boolean", "description": "Set true after approval to apply the proposed change"},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage-theme",
			Description: "Inspect and manage builtin/custom themes through existing UI settings and workspace theme mutation paths; supports inspect/list/get/create/update/delete/set, and mutating actions return approval-ready previews unless confirm=true",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: inspect|list|get|create|update|delete|set"},
					"theme_id":       map[string]any{"type": "string", "description": "Theme id for get/update/delete/set"},
					"name":           map[string]any{"type": "string", "description": "Theme display name for create/update"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path for workspace-scoped set/list"},
					"base_theme_id":  map[string]any{"type": "string", "description": "Optional builtin/custom base theme id for create"},
					"confirm":        map[string]any{"type": "boolean", "description": "Set true after approval to apply the proposed change"},
					"content": map[string]any{
						"type":        "object",
						"description": "Theme payload for create/update with optional palette overrides",
						"properties": map[string]any{
							"id":   map[string]any{"type": "string"},
							"name": map[string]any{"type": "string"},
							"palette": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"background":       map[string]any{"type": "string"},
									"panel":            map[string]any{"type": "string"},
									"element":          map[string]any{"type": "string"},
									"border":           map[string]any{"type": "string"},
									"border_active":    map[string]any{"type": "string"},
									"text":             map[string]any{"type": "string"},
									"text_muted":       map[string]any{"type": "string"},
									"primary":          map[string]any{"type": "string"},
									"secondary":        map[string]any{"type": "string"},
									"accent":           map[string]any{"type": "string"},
									"success":          map[string]any{"type": "string"},
									"warning":          map[string]any{"type": "string"},
									"error":            map[string]any{"type": "string"},
									"prompt":           map[string]any{"type": "string"},
									"prompt_cursor_bg": map[string]any{"type": "string"},
									"prompt_cursor_fg": map[string]any{"type": "string"},
									"code_background":  map[string]any{"type": "string"},
									"code_text":        map[string]any{"type": "string"},
									"code_keyword":     map[string]any{"type": "string"},
									"code_type":        map[string]any{"type": "string"},
									"code_string":      map[string]any{"type": "string"},
									"code_number":      map[string]any{"type": "string"},
									"code_comment":     map[string]any{"type": "string"},
									"code_function":    map[string]any{"type": "string"},
									"code_operator":    map[string]any{"type": "string"},
								},
								"additionalProperties": false,
							},
						},
						"additionalProperties": false,
					},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage-worktree",
			Description: "Inspect combined commits for the workspace worktree branch family; defaults to the configured branch prefix for the workspace (for example agent/ or foo/) and supports an optional branch_name override",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: inspect|list"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path; defaults to current/active workspace scope"},
					"branch_name":    map[string]any{"type": "string", "description": "Optional worktree branch family/prefix override such as agent or foo"},
					"limit":          map[string]any{"type": "integer", "description": "Page size for returned commits (default 25)"},
					"cursor":         map[string]any{"type": "integer", "description": "0-based result offset for pagination"},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage_todos",
			Description: "Manage workspace todo items and summaries. Supports list/create/update/delete/reorder/in_progress actions and atomic batch mutations for a regular todo list with priorities, tags, groups, in-progress state, and ordering.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: list|summary|create|update|delete|delete_done|delete_all|reorder|in_progress|batch"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path; defaults to current/active workspace scope"},
					"owner_kind":     map[string]any{"type": "string", "description": "Optional owner kind filter/scope: user|agent"},
					"id":             map[string]any{"type": "string", "description": "Todo id for update/delete/in_progress"},
					"text":           map[string]any{"type": "string", "description": "Todo text"},
					"done":           map[string]any{"type": "boolean", "description": "Completed state"},
					"priority":       map[string]any{"type": "string", "description": "Priority: low|medium|high|urgent"},
					"group":          map[string]any{"type": "string", "description": "Optional grouping label"},
					"tags":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tags"},
					"in_progress":    map[string]any{"type": "boolean", "description": "In-progress state"},
					"session_id":     map[string]any{"type": "string", "description": "Optional conversation/session id for agent checklist grouping"},
					"parent_id":      map[string]any{"type": "string", "description": "Optional parent task id for nested agent checklist steps"},
					"ordered_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ordered todo ids for reorder"},
					"operations": map[string]any{
						"type":        "array",
						"description": "Atomic batch operations for batch action",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"action":      map[string]any{"type": "string"},
								"id":          map[string]any{"type": "string"},
								"owner_kind":  map[string]any{"type": "string"},
								"text":        map[string]any{"type": "string"},
								"done":        map[string]any{"type": "boolean"},
								"priority":    map[string]any{"type": "string"},
								"group":       map[string]any{"type": "string"},
								"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"in_progress": map[string]any{"type": "boolean"},
								"session_id":  map[string]any{"type": "string"},
								"parent_id":   map[string]any{"type": "string"},
								"ordered_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
							"required":             []string{"action"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "exit_plan_mode",
			Description: "Submit a plan for approval so the session can leave plan mode and continue execution",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Plan title"},
					"plan":  map[string]any{"type": "string", "description": "Full markdown plan body"},
				},
				"required":             []string{"title", "plan"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "plan_manage",
			Description: "Manage saved plans (list/get/get-active/save/set-active/new). save updates the active plan when plan_id is omitted and does not exit plan mode",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: list|get|get-active|save|set-active|new. Aliases such as active/current/use/create/update/edit/write_active are also accepted."},
					"plan_id":        map[string]any{"type": "string", "description": "Plan id for get/set-active/save. Omit on save to update the active plan when one exists."},
					"id":             map[string]any{"type": "string", "description": "Alias for plan_id."},
					"title":          map[string]any{"type": "string", "description": "Plan title for save/new. Existing title is kept when omitted on save for an existing plan."},
					"plan":           map[string]any{"type": "string", "description": "Full markdown plan body for save. Required when saving a plan."},
					"status":         map[string]any{"type": "string", "description": "Optional plan status to persist on save."},
					"approval_state": map[string]any{"type": "string", "description": "Optional approval state to persist on save."},
					"activate":       map[string]any{"type": "boolean", "description": "Whether the saved/new plan becomes the active plan (default true)."},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "task",
			Description: "Delegate a focused task to one or more subagents in a single approval batch (prefer explorer for repo scouting) and return a concise, evidence-backed report",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"description": "Optional action. Supported: spawn (default).",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Short task label shown in UI.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Shared main task prompt for the delegated run(s). Each child also receives its assigned subagent type and per-launch meta instructions.",
					},
					"subagent_type": map[string]any{
						"type":        "string",
						"description": "Subagent profile or purpose for the single-launch shorthand (for example explorer, memory, parallel, clone). Prefer explorer when mapping unfamiliar code and identifying candidate filepaths.",
					},
					"launches": map[string]any{
						"type":        "array",
						"description": "Optional batched child launches for one task approval. Use one entry per subagent when the same parent task should spawn multiple specialized children.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"subagent_type": map[string]any{"type": "string", "description": "Assigned child subagent type/profile."},
								"agent":         map[string]any{"type": "string", "description": "Alias for subagent_type."},
								"purpose":       map[string]any{"type": "string", "description": "Alias for subagent_type."},
								"meta_prompt":   map[string]any{"type": "string", "description": "Per-child meta instruction or assignment shown in the approval modal."},
								"role":          map[string]any{"type": "string", "description": "Alias for meta_prompt."},
							},
							"additionalProperties": false,
						},
					},
					"allow_bash": map[string]any{
						"type":        "boolean",
						"description": "Allow bash for this delegated run (default false).",
					},
					"report_max_chars": map[string]any{
						"type":        "integer",
						"description": "Max chars returned in delegated report excerpt before truncation/persistence.",
					},
				},
				"required":             []string{"prompt"},
				"additionalProperties": false,
			},
		},
	}
}

func (r *Runtime) ExecuteBatch(ctx context.Context, workspacePath string, calls []Call) []Result {
	return r.executeBatch(ctx, workspacePath, calls, nil, nil)
}

func (r *Runtime) ExecuteBatchStreaming(ctx context.Context, workspacePath string, calls []Call, onResult BatchResultHandler) []Result {
	return r.executeBatch(ctx, workspacePath, calls, nil, onResult)
}

func (r *Runtime) ExecuteBatchStreamingWithProgress(ctx context.Context, workspacePath string, calls []Call, onProgress BatchProgressHandler, onResult BatchResultHandler) []Result {
	return r.executeBatch(ctx, workspacePath, calls, onProgress, onResult)
}

func (r *Runtime) executeBatch(ctx context.Context, workspacePath string, calls []Call, onProgress BatchProgressHandler, onResult BatchResultHandler) []Result {
	if len(calls) == 0 {
		return nil
	}
	scope := workspaceScopeFromContext(ctx, workspacePath)
	workers := r.maxParallel
	if workers > len(calls) {
		workers = len(calls)
	}
	if workers <= 0 {
		workers = 1
	}

	type job struct {
		index int
		call  Call
	}

	jobs := make(chan job)
	results := make([]Result, len(calls))
	var wg sync.WaitGroup

	workerFn := func() {
		defer wg.Done()
		for current := range jobs {
			start := time.Now()
			result := Result{
				CallID: current.call.CallID,
				Name:   current.call.Name,
			}
			progressFn := func(_ Progress) {}
			if onProgress != nil {
				progressFn = func(progress Progress) {
					onProgress(current.index, current.call, progress)
				}
			}
			output, err := r.executeOne(ctx, scope, current.call, progressFn)
			if err != nil {
				result.Error = err.Error()
				if strings.TrimSpace(output) != "" {
					result.Output = output
				} else {
					result.Output = err.Error()
				}
			} else {
				result.Output = output
			}
			result.DurationMS = time.Since(start).Milliseconds()
			results[current.index] = result
			if onResult != nil {
				onResult(current.index, current.call, result)
			}
		}
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go workerFn()
	}
	for i := range calls {
		jobs <- job{index: i, call: calls[i]}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (r *Runtime) executeOne(ctx context.Context, scope WorkspaceScope, call Call, onProgress func(Progress)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	args := map[string]any{}
	trimmed := strings.TrimSpace(call.Arguments)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Errorf("invalid JSON arguments for tool %q: %w", call.Name, err)
		}
	}

	name := strings.ToLower(strings.TrimSpace(call.Name))
	switch name {
	case "read":
		return executeRead(scope, args)
	case "write":
		return executeWrite(scope, args)
	case "bash":
		return executeBash(ctx, scope, args, func(chunk string) {
			if onProgress == nil {
				return
			}
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				return
			}
			onProgress(Progress{
				Stage:  "output",
				Output: chunk,
			})
		})
	case "git_status":
		return executeGitStatus(ctx, scope, args)
	case "git_diff":
		return executeGitDiff(ctx, scope, args)
	case "git_add":
		return executeGitAdd(ctx, scope, args)
	case "git_commit":
		return executeGitCommit(ctx, scope, args)
	case "glob":
		return "", errors.New("glob is disabled; use list for path discovery and search for canonical FFF-backed retrieval")
	case "search":
		return r.executeSearch(ctx, scope, args)
	case "websearch":
		return r.executeWebSearch(ctx, args)
	case "webfetch":
		return r.executeWebFetch(ctx, args)
	case "webdownload":
		return r.executeWebDownload(ctx, scope, args)
	case "agentic_search":
		return "", errors.New("agentic_search is removed; use the canonical search tool with tighter path/include scopes and read/list follow-up")
	case "list":
		return executeList(scope, args)
	case "edit":
		return executeEdit(scope, args)
	case "skill-use", "skill_use":
		return executeSkillUse(scope, args)
	case "manage-skill", "manage_skill":
		return executeManageSkill(scope, args)
	case "manage-agent", "manage_agent":
		return r.executeManageAgent(scope, args)
	case "manage-theme", "manage_theme":
		return r.executeManageTheme(scope, args)
	case "manage-worktree", "manage_worktree":
		return r.executeManageWorktree(scope, args)
	case "manage-todos", "manage_todos":
		return r.executeManageTodos(scope, args)
	case "ask-user", "ask_user", "exit_plan_mode", "exit-plan-mode", "plan_manage", "plan-manage":
		return executeStubTool(name, args)
	case "task":
		return "", errors.New("task must be handled by run-service control-plane")
	default:
		return r.executeCustomTool(ctx, scope, name, args, onProgress)
	}
}

func (r *Runtime) executeCustomTool(ctx context.Context, scope WorkspaceScope, name string, args map[string]any, onProgress func(Progress)) (string, error) {
	if r == nil || r.agents == nil {
		return "", fmt.Errorf("unsupported tool %q", name)
	}
	definition, ok, err := r.agents.GetCustomTool(name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("unsupported tool %q", name)
	}
	if len(args) > 0 {
		return "", fmt.Errorf("custom tool %q does not accept arguments", name)
	}
	switch definition.Kind {
	case pebblestore.AgentCustomToolKindFixedBash:
		return executeBash(ctx, scope, map[string]any{"command": definition.Command}, func(chunk string) {
			if onProgress == nil {
				return
			}
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				return
			}
			onProgress(Progress{Stage: "output", Output: chunk})
		})
	default:
		return "", fmt.Errorf("custom tool %q has unsupported kind %q", name, definition.Kind)
	}
}

func executeRead(scope WorkspaceScope, args map[string]any) (string, error) {
	targetPath, err := resolveWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	if _, ok := args["max_bytes"]; ok {
		return "", errors.New("read no longer supports max_bytes; use line_start and max_lines")
	}
	if _, ok := args["offset_bytes"]; ok {
		return "", errors.New("read no longer supports offset_bytes; use line_start and max_lines")
	}
	if _, ok := args["offset"]; ok {
		return "", errors.New("read no longer supports offset; use line_start and max_lines")
	}
	lineStart := asInt(args["line_start"], 1)
	if lineStart <= 0 {
		lineStart = 1
	}
	maxLines := asInt(args["max_lines"], defaultReadMaxLines)
	if maxLines <= 0 {
		maxLines = defaultReadMaxLines
	}
	if maxLines > maxReadMaxLines {
		maxLines = maxReadMaxLines
	}

	file, err := os.Open(targetPath)
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	defer file.Close()

	head := make([]byte, 4096)
	headRead, headErr := file.Read(head)
	if headErr != nil && !errors.Is(headErr, io.EOF) {
		return "", fmt.Errorf("read failed: %w", headErr)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read failed: rewind: %w", err)
	}
	binarySuppressed := isLikelyBinary(head[:headRead])
	if binarySuppressed {
		response := map[string]any{
			"path":                 targetPath,
			"bytes":                0,
			"line_start":           lineStart,
			"max_lines":            maxLines,
			"count":                0,
			"next_line_start":      lineStart,
			"truncated":            false,
			"eof":                  true,
			"line_text_truncated":  false,
			"binary_suppressed":    true,
			"lines":                []map[string]any{},
			"path_id":              toolPathID("read"),
			"summary":              readSummary(targetPath, 0, false, true),
			"details_truncated":    true,
			"safety":               buildUntrustedSafety(""),
			"prompt_injection_tag": "tool_output_untrusted",
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadLineBytes)

	lines := make([]map[string]any, 0, maxLines)
	currentLine := 0
	truncated := false
	lineTextTruncated := false
	var safetyBuilder strings.Builder

	for scanner.Scan() {
		currentLine++
		if currentLine < lineStart {
			continue
		}
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		text := sanitizeForToolOutput(scanner.Text())
		text, didTruncate := clampRunesWithEllipsis(text, maxReadLineChars)
		if didTruncate {
			lineTextTruncated = true
		}
		lines = append(lines, map[string]any{
			"line": currentLine,
			"text": text,
		})
		if safetyBuilder.Len() > 0 {
			safetyBuilder.WriteByte('\n')
		}
		safetyBuilder.WriteString(text)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	content := safetyBuilder.String()
	nextLineStart := lineStart + len(lines)
	detailsTruncated := truncated || lineTextTruncated
	bytesRead := len(content)

	response := map[string]any{
		"path":                 targetPath,
		"bytes":                bytesRead,
		"line_start":           lineStart,
		"max_lines":            maxLines,
		"count":                len(lines),
		"next_line_start":      nextLineStart,
		"eof":                  !truncated,
		"truncated":            truncated,
		"line_text_truncated":  lineTextTruncated,
		"binary_suppressed":    false,
		"lines":                lines,
		"path_id":              toolPathID("read"),
		"summary":              readSummary(targetPath, bytesRead, truncated, false),
		"details_truncated":    detailsTruncated,
		"safety":               buildUntrustedSafety(content),
		"prompt_injection_tag": "tool_output_untrusted",
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func executeWrite(scope WorkspaceScope, args map[string]any) (string, error) {
	targetPath, err := resolveWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	if _, ok := args["content"]; !ok {
		return "", errors.New("write requires content")
	}
	content := asString(args["content"])
	appendMode := asBool(args["append"])

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}

	if appendMode {
		f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("open file for append: %w", err)
		}
		defer f.Close()
		if _, err := io.WriteString(f, content); err != nil {
			return "", fmt.Errorf("append failed: %w", err)
		}
	} else {
		if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write failed: %w", err)
		}
	}

	response := map[string]any{
		"path":              targetPath,
		"bytes_written":     len(content),
		"append":            appendMode,
		"path_id":           toolPathID("write"),
		"summary":           writeSummary(targetPath, len(content), appendMode),
		"details_truncated": false,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func executeBash(parent context.Context, scope WorkspaceScope, args map[string]any, onDelta func(string)) (string, error) {
	command := strings.TrimSpace(asString(args["command"]))
	if command == "" {
		return "", errors.New("bash requires command")
	}
	timeout := time.Duration(asInt(args["timeout_ms"], int(defaultBashTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	sandboxConfig := bashSandboxConfigFromContext(parent)
	var cmd *exec.Cmd
	if sandboxConfig.Enabled {
		sandboxCmd, err := buildSandboxBashCommand(ctx, scope, command)
		if err != nil {
			errorOutput, marshalErr := marshalBashStartError(command, err.Error(), true, sandboxConfig.RunID)
			if marshalErr != nil {
				return "", marshalErr
			}
			return errorOutput, err
		}
		cmd = sandboxCmd
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-lc", command)
		cmd.Dir = scope.PrimaryPath
	}
	prepareCommandForCancellation(cmd)

	capture := newCappedBuffer(maxBashOutputViewerBytes)
	streamWriter := newBashStreamWriter(capture, maxBashOutputViewerBytes, onDelta)
	cmd.Stdout = streamWriter
	cmd.Stderr = streamWriter

	stopWatchingCancel := watchCommandCancellation(ctx, cmd)
	defer stopWatchingCancel()

	err := cmd.Run()
	streamWriter.Flush()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	wasTruncated := capture.Truncated()
	rawOutput := capture.Bytes()
	binarySuppressed := streamWriter.BinarySuppressed() || isLikelyBinary(rawOutput)
	combined := ""
	if !binarySuppressed {
		combined = sanitizeForToolOutput(capture.String())
	}
	detailsTruncated := wasTruncated || timedOut || binarySuppressed

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			exitCode = -1
		}
	}

	response := map[string]any{
		"command":              command,
		"exit_code":            exitCode,
		"timed_out":            timedOut,
		"truncated":            wasTruncated,
		"binary_suppressed":    binarySuppressed,
		"sandboxed":            sandboxConfig.Enabled,
		"sandbox_run_id":       strings.TrimSpace(sandboxConfig.RunID),
		"output":               combined,
		"path_id":              toolPathID("bash"),
		"summary":              bashSummary(command, exitCode, timedOut, wasTruncated, binarySuppressed),
		"details_truncated":    detailsTruncated,
		"safety":               buildUntrustedSafety(combined),
		"prompt_injection_tag": "tool_output_untrusted",
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}

	if err != nil && exitCode == -1 {
		return string(encoded), fmt.Errorf("bash execution failed: %w", err)
	}
	return string(encoded), nil
}

func executeGitStatus(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	argv := []string{"status"}
	if asBool(args["short"]) {
		argv = append(argv, "--short")
	}
	if asBool(args["branch"]) {
		argv = append(argv, "--branch")
	}
	return executeGitCommand(parent, scope, "git_status", argv)
}

func executeGitDiff(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	argv := []string{"diff"}
	if asBool(args["staged"]) {
		argv = append(argv, "--staged")
	}
	pathspec := asStringSlice(args["pathspec"])
	if len(pathspec) > 0 {
		argv = append(argv, "--")
		argv = append(argv, pathspec...)
	}
	return executeGitCommand(parent, scope, "git_diff", argv)
}

func executeGitAdd(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	argv := []string{"add"}
	pathspec := asStringSlice(args["pathspec"])
	all := asBool(args["all"])
	if all {
		argv = append(argv, "--all")
	}
	if len(pathspec) == 0 && !all {
		return "", errors.New("git_add requires pathspec or all=true")
	}
	argv = append(argv, pathspec...)
	return executeGitCommand(parent, scope, "git_add", argv)
}

func executeGitCommit(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	message := strings.TrimSpace(asString(args["message"]))
	if message == "" {
		return "", errors.New("git_commit requires message")
	}
	argv := []string{"commit", "-m", message}
	if asBool(args["all"]) {
		argv = append(argv, "--all")
	}
	return executeGitCommand(parent, scope, "git_commit", argv)
}

func executeGitCommand(parent context.Context, scope WorkspaceScope, toolName string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("git command is required")
	}
	timeout := defaultGitTimeout
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = scope.PrimaryPath
	cmd.Env = filteredGitEnv(os.Environ())

	capture := newCappedBuffer(maxCommandOutput)
	cmd.Stdout = capture
	cmd.Stderr = capture

	err := cmd.Run()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	wasTruncated := capture.Truncated()
	rawOutput := capture.Bytes()
	binarySuppressed := isLikelyBinary(rawOutput)
	combined := ""
	if !binarySuppressed {
		combined = sanitizeForToolOutput(capture.String())
	}
	detailsTruncated := wasTruncated || timedOut || binarySuppressed

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	response := map[string]any{
		"argv":                 append([]string{"git"}, argv...),
		"exit_code":            exitCode,
		"timed_out":            timedOut,
		"truncated":            wasTruncated,
		"binary_suppressed":    binarySuppressed,
		"output":               combined,
		"path_id":              toolPathID(toolName),
		"summary":              gitCommandSummary(toolName, argv, exitCode, timedOut, wasTruncated, binarySuppressed),
		"details_truncated":    detailsTruncated,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(combined),
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil && exitCode == -1 {
		return string(encoded), fmt.Errorf("%s execution failed: %w", toolName, err)
	}
	return string(encoded), nil
}

func filteredGitEnv(base []string) []string {
	if len(base) == 0 {
		return nil
	}
	blocked := map[string]struct{}{
		"GIT_AUTHOR_NAME":     {},
		"GIT_AUTHOR_EMAIL":    {},
		"GIT_AUTHOR_DATE":     {},
		"GIT_COMMITTER_NAME":  {},
		"GIT_COMMITTER_EMAIL": {},
		"GIT_COMMITTER_DATE":  {},
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.Index(entry, "="); idx >= 0 {
			key = entry[:idx]
		}
		if _, deny := blocked[key]; deny {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func gitCommandSummary(toolName string, argv []string, exitCode int, timedOut, truncated, binarySuppressed bool) string {
	summary := fmt.Sprintf("%s exited %d", toolName, exitCode)
	if len(argv) > 0 {
		summary = fmt.Sprintf("git %s exited %d", strings.Join(argv, " "), exitCode)
	}
	if timedOut {
		summary += " (timed out)"
	}
	if truncated {
		summary += " (truncated)"
	}
	if binarySuppressed {
		summary += " (binary suppressed)"
	}
	return summary
}

func buildSandboxBashCommand(ctx context.Context, scope WorkspaceScope, command string) (*exec.Cmd, error) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("sandbox is ON but bubblewrap (bwrap) is not installed; run /sandbox for setup")
	}

	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	if workspacePath == "" {
		return nil, errors.New("sandbox is ON but workspace path is empty")
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("sandbox path resolve failed: %w", err)
	}

	args := []string{
		"--new-session",
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-net",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/var/tmp",
		"--chdir", absWorkspace,
		"--setenv", "HOME", absWorkspace,
		"--setenv", "PATH", os.Getenv("PATH"),
	}
	for _, root := range scope.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		args = append(args, "--bind", root, root)
	}
	args = append(args, "--", "/bin/bash", "-lc", command)
	cmd := exec.CommandContext(ctx, bwrapPath, args...)
	cmd.Dir = absWorkspace
	cmd.Env = os.Environ()
	return cmd, nil
}

func marshalBashStartError(command, detail string, sandboxed bool, runID string) (string, error) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "bash execution failed before start"
	}
	response := map[string]any{
		"command":              strings.TrimSpace(command),
		"exit_code":            -1,
		"timed_out":            false,
		"truncated":            false,
		"binary_suppressed":    false,
		"sandboxed":            sandboxed,
		"sandbox_run_id":       strings.TrimSpace(runID),
		"output":               detail,
		"path_id":              toolPathID("bash"),
		"summary":              fmt.Sprintf("bash %s failed to start", truncateSummary(command, 80)),
		"details_truncated":    false,
		"safety":               buildUntrustedSafety(detail),
		"prompt_injection_tag": "tool_output_untrusted",
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func executeGlob(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	return "", errors.New("glob is disabled; use list for path discovery and search for canonical FFF-backed retrieval")
}

func (r *Runtime) executeSearch(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	queries, err := parseSearchQueries(args)
	if err != nil {
		return "", err
	}

	searchRoot, err := resolveSearchRoot(scope, args["path"])
	if err != nil {
		return "", err
	}

	include := strings.TrimSpace(asString(args["include"]))
	payloadStyle := strings.ToLower(strings.TrimSpace(asString(args["_search_payload_style"])))
	maxResults := clampInt(asInt(args["max_results"], defaultSearchResults), 1, maxSearchResults)
	timeout := resolveSearchTimeout(args["timeout_ms"])
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	inst, _, err := fff.Create(searchRoot, false)
	if err != nil {
		return "", fmt.Errorf("search FFF create failed: %w", err)
	}
	defer inst.Destroy()

	completed, _, err := inst.WaitForScan(timeout)
	if err != nil {
		return "", fmt.Errorf("search FFF scan wait failed: %w", err)
	}
	if !completed {
		results := make([]searchQueryExecution, len(queries))
		for i, query := range queries {
			results[i] = searchQueryExecution{Query: query, Mode: "content", Truncated: true, TimedOut: true}
		}
		return encodeSearchPayload(selectSearchContentPayload(payloadStyle, searchRoot, queries, include, results, maxResults))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var (
		contentResults []searchQueryExecution
		contentErrors  []error
	)
	if len(queries) == 1 {
		result, err := executeSearchContentQuery(inst, searchRoot, queries[0], include, maxResults, timeout)
		contentResults = []searchQueryExecution{result}
		if err != nil {
			contentErrors = []error{fmt.Errorf("query %q: %w", queries[0], err)}
		}
	} else {
		result, err := executeSearchMultiContentQuery(inst, searchRoot, queries, include, maxResults, timeout)
		contentResults = []searchQueryExecution{result}
		if err != nil {
			contentErrors = []error{fmt.Errorf("multi-grep %q: %w", strings.Join(queries, " | "), err)}
		}
	}
	if len(contentErrors) == 0 || ctx.Err() != nil {
		return encodeSearchPayload(selectSearchContentPayload(payloadStyle, searchRoot, queries, include, contentResults, maxResults))
	}

	workers := searchWorkerCount(r, len(queries))
	fileResults, fileErrors := executeSearchQueryBatch(ctx, queries, workers, func(query string) (searchQueryExecution, error) {
		return executeSearchFileQuery(inst, searchRoot, query, include, maxResults)
	})
	if len(fileErrors) > 0 {
		return "", fmt.Errorf("search query execution failed: content mode: %s; file mode: %s", formatSearchQueryErrors(contentErrors), formatSearchQueryErrors(fileErrors))
	}
	payload := selectSearchFilePayload(payloadStyle, searchRoot, queries, include, fileResults, maxResults)
	payload["content_search_error"] = formatSearchQueryErrors(contentErrors)
	return encodeSearchPayload(payload)
}

func selectSearchContentPayload(style, searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	if strings.EqualFold(strings.TrimSpace(style), "legacy") {
		return buildSearchContentLegacyPayload(searchRoot, queries, include, results, maxResults)
	}
	return buildSearchContentPayload(searchRoot, queries, include, results, maxResults)
}

func selectSearchFilePayload(style, searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	if strings.EqualFold(strings.TrimSpace(style), "legacy") {
		return buildSearchFileLegacyPayload(searchRoot, queries, include, results, maxResults)
	}
	return buildSearchFilePayload(searchRoot, queries, include, results, maxResults)
}

func encodeSearchPayload(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func buildFFFGrepQuery(include, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	include = strings.TrimSpace(include)
	if include == "" {
		return pattern
	}
	return include + " " + pattern
}

func buildFFFSearchQuery(include, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	include = strings.TrimSpace(include)
	if include == "" {
		return pattern
	}
	return include + " " + pattern
}

type searchQuerySummary struct {
	Query              string `json:"query"`
	Mode               string `json:"mode,omitempty"`
	Count              int    `json:"count"`
	TotalMatched       int    `json:"total_matched,omitempty"`
	TotalFilesSearched int    `json:"total_files_searched,omitempty"`
	TotalFiles         int    `json:"total_files,omitempty"`
	FilteredFileCount  int    `json:"filtered_file_count,omitempty"`
	NextFileOffset     int    `json:"next_file_offset,omitempty"`
	RegexFallbackError string `json:"regex_fallback_error,omitempty"`
	TimedOut           bool   `json:"timed_out,omitempty"`
	Truncated          bool   `json:"truncated,omitempty"`
	Error              string `json:"error,omitempty"`
	Summary            string `json:"summary"`
}

type searchQueryExecution struct {
	Query         string
	Mode          string
	ContentRows   []searchContentRow
	FileRows      []searchFileRow
	Totals        searchAggregateTotals
	ReturnedCount int
	Truncated     bool
	TimedOut      bool
	Error         string
}

type searchContentRow struct {
	Query        string
	Path         string
	RelativePath string
	FileName     string
	GitStatus    string
	Line         int
	Column       int
	Text         string
	IsDefinition bool
	MatchRanges  []fff.MatchRange
	ContextAfter []string
}

type searchFileRow struct {
	Query        string
	Path         string
	RelativePath string
	FileName     string
	GitStatus    string
	Score        int
}

type searchAggregateTotals struct {
	TotalMatched       int
	TotalFilesSearched int
	TotalFiles         int
	FilteredFileCount  int
	NextFileOffset     int
	RegexFallbackError string
}

type searchQueryBatchJob struct {
	Index int
	Query string
}

func parseSearchQueries(args map[string]any) ([]string, error) {
	queries := make([]string, 0, 8)
	if single := strings.TrimSpace(asString(args["query"])); single != "" {
		queries = append(queries, single)
	}
	if legacy := strings.TrimSpace(asString(args["pattern"])); legacy != "" {
		queries = append(queries, legacy)
	}
	queries = append(queries, asStringSlice(args["queries"])...)
	if len(queries) == 0 {
		return nil, errors.New("search requires query, pattern, or queries")
	}
	seen := make(map[string]struct{}, len(queries))
	deduped := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, query)
	}
	if len(deduped) == 0 {
		return nil, errors.New("search requires at least one non-empty query")
	}
	if len(deduped) > maxSearchQueries {
		return nil, fmt.Errorf("search supports at most %d queries per call; split the batch and retry", maxSearchQueries)
	}
	return deduped, nil
}

func firstSearchQuery(queries []string) string {
	if len(queries) == 0 {
		return ""
	}
	return strings.TrimSpace(queries[0])
}

func searchWorkerCount(r *Runtime, queryCount int) int {
	if queryCount <= 0 {
		return 1
	}
	workers := 4
	if r != nil && r.maxParallel > 0 {
		workers = r.maxParallel
	}
	if workers > queryCount {
		workers = queryCount
	}
	if workers <= 0 {
		workers = 1
	}
	return workers
}

func executeSearchQueryBatch(ctx context.Context, queries []string, workers int, runner func(string) (searchQueryExecution, error)) ([]searchQueryExecution, []error) {
	results := make([]searchQueryExecution, len(queries))
	if len(queries) == 0 {
		return results, nil
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > len(queries) {
		workers = len(queries)
	}

	jobs := make(chan searchQueryBatchJob, len(queries))
	for idx, query := range queries {
		jobs <- searchQueryBatchJob{Index: idx, Query: query}
	}
	close(jobs)

	errSlots := make([]error, len(queries))
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if err := ctx.Err(); err != nil {
					results[job.Index] = searchQueryExecution{
						Query:     job.Query,
						Mode:      "content",
						Truncated: true,
						TimedOut:  errors.Is(err, context.DeadlineExceeded),
						Error:     strings.TrimSpace(err.Error()),
					}
					errSlots[job.Index] = fmt.Errorf("query %q: %w", job.Query, err)
					continue
				}
				result, err := runner(job.Query)
				if strings.TrimSpace(result.Query) == "" {
					result.Query = job.Query
				}
				if result.ReturnedCount == 0 {
					result.ReturnedCount = len(result.ContentRows) + len(result.FileRows)
				}
				results[job.Index] = result
				if err != nil {
					errSlots[job.Index] = fmt.Errorf("query %q: %w", job.Query, err)
				}
			}
		}()
	}
	wg.Wait()

	errs := make([]error, 0, len(errSlots))
	for _, err := range errSlots {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return results, errs
}

func executeSearchContentQuery(inst *fff.Instance, searchRoot, query, include string, maxResults int, timeout time.Duration) (searchQueryExecution, error) {
	result := searchQueryExecution{Query: query, Mode: "content"}
	pageLimit := uint32(maxResults + searchResultPageSlack)
	matches, metrics, err := inst.GrepWithConfig(buildFFFGrepQuery(include, query), fff.GrepOptions{
		PageLimit:           pageLimit,
		TimeBudget:          timeout,
		AfterContext:        searchDefinitionAfterContext,
		ClassifyDefinitions: true,
	})
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		return result, err
	}
	rows, totals, truncated, timedOut, _ := collectSearchContentRows(query, searchRoot, include, matches, metrics, maxResults)
	result.ContentRows = rows
	result.Totals = totals
	result.ReturnedCount = len(rows)
	result.Truncated = truncated
	result.TimedOut = timedOut
	return result, nil
}

func executeSearchMultiContentQuery(inst *fff.Instance, searchRoot string, queries []string, include string, maxResults int, timeout time.Duration) (searchQueryExecution, error) {
	result := searchQueryExecution{Query: firstSearchQuery(queries), Mode: "content"}
	pageLimit := uint32(maxResults + searchResultPageSlack)
	matches, metrics, err := inst.MultiGrepWithOptions(compactSearchQueries(queries), include, pageLimit, timeout, 0, 0, searchDefinitionAfterContext, true)
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		return result, err
	}
	rows, totals, truncated, timedOut, _ := collectSearchContentRows("", searchRoot, include, matches, metrics, maxResults)
	result.ContentRows = rows
	result.Totals = totals
	result.ReturnedCount = len(rows)
	result.Truncated = truncated
	result.TimedOut = timedOut
	return result, nil
}

func executeSearchFileQuery(inst *fff.Instance, searchRoot, query, include string, maxResults int) (searchQueryExecution, error) {
	result := searchQueryExecution{Query: query, Mode: "files"}
	items, metrics, err := inst.SearchWithOptions(buildFFFSearchQuery(include, query), uint32(maxResults+1), 0)
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		return result, err
	}
	rows, totals, truncated := collectSearchFileRows(query, searchRoot, include, items, metrics, maxResults)
	result.FileRows = rows
	result.Totals = totals
	result.ReturnedCount = len(rows)
	result.Truncated = truncated
	return result, nil
}

func collectSearchContentRows(query, searchRoot, include string, matches []fff.GrepMatch, metrics fff.GrepMetrics, maxResults int) ([]searchContentRow, searchAggregateTotals, bool, bool, string) {
	rows := make([]searchContentRow, 0, minInt(len(matches), maxResults))
	truncated := false
	candidateLimit := maxResults + searchResultPageSlack
	if candidateLimit < maxResults {
		candidateLimit = maxResults
	}
	var safetySource strings.Builder
	for _, match := range matches {
		pathValue := filepath.Clean(match.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, match.RelativePath)
		if !matchesIncludeGlob(include, relPath) {
			continue
		}
		text := strings.TrimSpace(sanitizeForToolOutput(match.LineContent))
		if len([]rune(text)) > maxGrepLineChars {
			text = string([]rune(text)[:maxGrepLineChars]) + "..."
			truncated = true
		}
		rows = append(rows, searchContentRow{
			Query:        query,
			Path:         pathValue,
			RelativePath: relPath,
			FileName:     strings.TrimSpace(match.FileName),
			GitStatus:    strings.TrimSpace(match.GitStatus),
			Line:         int(match.LineNumber),
			Column:       int(match.Column),
			Text:         text,
			IsDefinition: match.IsDefinition,
			MatchRanges:  append([]fff.MatchRange(nil), match.MatchRanges...),
			ContextAfter: append([]string(nil), match.ContextAfter...),
		})
		if len(rows) >= candidateLimit {
			truncated = true
			break
		}
		appendSearchSafetyText(&safetySource, text)
	}
	if metrics.NextFileOffset != 0 || metrics.TotalMatched > uint32(len(rows)) {
		truncated = true
	}
	return rows, searchAggregateTotals{
		TotalMatched:       int(metrics.TotalMatched),
		TotalFilesSearched: int(metrics.TotalFilesSearched),
		TotalFiles:         int(metrics.TotalFiles),
		FilteredFileCount:  int(metrics.FilteredFileCount),
		NextFileOffset:     int(metrics.NextFileOffset),
		RegexFallbackError: strings.TrimSpace(metrics.RegexFallbackError),
	}, truncated, false, safetySource.String()
}

func collectSearchFileRows(query, searchRoot, include string, items []fff.SearchItem, metrics fff.SearchMetrics, maxResults int) ([]searchFileRow, searchAggregateTotals, bool) {
	files := make([]searchFileRow, 0, minInt(len(items), maxResults))
	truncated := false
	for _, item := range items {
		pathValue := filepath.Clean(item.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, item.RelativePath)
		if !matchesIncludeGlob(include, relPath) {
			continue
		}
		files = append(files, searchFileRow{
			Query:        query,
			Path:         pathValue,
			RelativePath: relPath,
			FileName:     strings.TrimSpace(item.FileName),
			GitStatus:    strings.TrimSpace(item.GitStatus),
			Score:        item.Score,
		})
		if len(files) >= maxResults {
			truncated = true
			break
		}
	}
	if metrics.TotalMatched > uint32(len(files)) {
		truncated = true
	}
	return files, searchAggregateTotals{
		TotalMatched: int(metrics.TotalMatched),
		TotalFiles:   int(metrics.TotalFiles),
	}, truncated
}

func buildSearchQuerySummaries(results []searchQueryExecution) []searchQuerySummary {
	out := make([]searchQuerySummary, 0, len(results))
	for _, result := range results {
		count := result.ReturnedCount
		if count <= 0 {
			count = len(result.ContentRows) + len(result.FileRows)
		}
		out = append(out, searchQuerySummary{
			Query:              result.Query,
			Mode:               result.Mode,
			Count:              count,
			TotalMatched:       result.Totals.TotalMatched,
			TotalFilesSearched: result.Totals.TotalFilesSearched,
			TotalFiles:         result.Totals.TotalFiles,
			FilteredFileCount:  result.Totals.FilteredFileCount,
			NextFileOffset:     result.Totals.NextFileOffset,
			RegexFallbackError: result.Totals.RegexFallbackError,
			TimedOut:           result.TimedOut,
			Truncated:          result.Truncated,
			Error:              result.Error,
			Summary:            searchSummaryForQueries([]string{result.Query}, "", count, result.Truncated, result.TimedOut, result.Mode == "content"),
		})
	}
	return out
}

func buildSearchContentLegacyPayload(searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	merged, mergeTruncated, safetySource := mergeSearchContentRows(results, maxResults)
	totals := aggregateSearchTotals(results)
	truncated, timedOut := searchBatchFlags(results)
	truncated = truncated || mergeTruncated

	rows := make([]map[string]any, 0, len(merged))
	for _, match := range merged {
		row := map[string]any{
			"query":         match.Query,
			"path":          match.Path,
			"relative_path": match.RelativePath,
			"file_name":     match.FileName,
			"git_status":    match.GitStatus,
			"line":          match.Line,
			"column":        match.Column,
			"text":          match.Text,
		}
		if match.IsDefinition {
			row["is_definition"] = true
		}
		rows = append(rows, row)
	}

	response := map[string]any{
		"pattern":              firstSearchQuery(queries),
		"query":                firstSearchQuery(queries),
		"queries":              queries,
		"query_count":          len(queries),
		"path":                 searchRoot,
		"include":              include,
		"count":                len(rows),
		"matches":              rows,
		"truncated":            truncated,
		"output_truncated":     false,
		"timed_out":            timedOut,
		"path_id":              toolPathID("search"),
		"summary":              searchSummaryForQueries(queries, searchRoot, len(rows), truncated, timedOut, true),
		"details_truncated":    truncated,
		"search_mode":          "content",
		"provider":             "fff",
		"total_matched":        totals.TotalMatched,
		"total_files_searched": totals.TotalFilesSearched,
		"total_files":          totals.TotalFiles,
		"filtered_file_count":  totals.FilteredFileCount,
		"next_file_offset":     totals.NextFileOffset,
		"query_results":        buildSearchQuerySummaries(results),
		"truncated_queries":    false,
		"merge_strategy":       "round_robin_by_query",
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(safetySource),
	}
	if fallback := strings.TrimSpace(totals.RegexFallbackError); fallback != "" {
		response["regex_fallback_error"] = fallback
	}
	return response
}

func buildSearchContentPayload(searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	merged, mergeTruncated, safetySource := mergeSearchContentRows(results, maxResults)
	totals := aggregateSearchTotals(results)
	truncated, timedOut := searchBatchFlags(results)
	truncated = truncated || mergeTruncated
	queryResults := buildSearchQuerySummaries(results)

	response := map[string]any{
		"path_id":              toolPathID("search"),
		"search_mode":          "content",
		"path":                 searchRoot,
		"count":                len(merged),
		"results":              buildCompactSearchContentResults(merged, len(queryResults) > 1),
		"truncated":            truncated,
		"timed_out":            timedOut,
		"summary":              searchSummaryForQueries(queries, searchRoot, len(merged), truncated, timedOut, true),
		"details_truncated":    truncated,
		"provider":             "fff",
		"total_matched":        totals.TotalMatched,
		"total_files_searched": totals.TotalFilesSearched,
		"total_files":          totals.TotalFiles,
		"filtered_file_count":  totals.FilteredFileCount,
		"query_results":        queryResults,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(safetySource),
	}
	if trimmed := strings.TrimSpace(include); trimmed != "" {
		response["include"] = trimmed
	}
	if totals.NextFileOffset > 0 {
		response["next_file_offset"] = totals.NextFileOffset
	}
	if fallback := strings.TrimSpace(totals.RegexFallbackError); fallback != "" {
		response["regex_fallback_error"] = fallback
	}
	return response
}

func buildSearchFileLegacyPayload(searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	merged, mergeTruncated := mergeSearchFileRows(results, maxResults)
	totals := aggregateSearchTotals(results)
	truncated, timedOut := searchBatchFlags(results)
	truncated = truncated || mergeTruncated

	files := make([]map[string]any, 0, len(merged))
	for _, item := range merged {
		files = append(files, map[string]any{
			"query":         item.Query,
			"path":          item.Path,
			"relative_path": item.RelativePath,
			"file_name":     item.FileName,
			"git_status":    item.GitStatus,
			"score":         item.Score,
		})
	}

	response := map[string]any{
		"pattern":              firstSearchQuery(queries),
		"query":                firstSearchQuery(queries),
		"queries":              queries,
		"query_count":          len(queries),
		"path":                 searchRoot,
		"include":              include,
		"count":                len(files),
		"files":                files,
		"truncated":            truncated,
		"output_truncated":     false,
		"timed_out":            timedOut,
		"path_id":              toolPathID("search"),
		"summary":              searchSummaryForQueries(queries, searchRoot, len(files), truncated, timedOut, false),
		"details_truncated":    truncated,
		"search_mode":          "files",
		"provider":             "fff",
		"total_matched":        totals.TotalMatched,
		"total_files":          totals.TotalFiles,
		"query_results":        buildSearchQuerySummaries(results),
		"truncated_queries":    false,
		"merge_strategy":       "round_robin_by_query",
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	if fallback := strings.TrimSpace(totals.RegexFallbackError); fallback != "" {
		response["regex_fallback_error"] = fallback
	}
	return response
}

func buildSearchFilePayload(searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	merged, mergeTruncated := mergeSearchFileRows(results, maxResults)
	totals := aggregateSearchTotals(results)
	truncated, timedOut := searchBatchFlags(results)
	truncated = truncated || mergeTruncated
	queryResults := buildSearchQuerySummaries(results)

	response := map[string]any{
		"path_id":              toolPathID("search"),
		"search_mode":          "files",
		"path":                 searchRoot,
		"count":                len(merged),
		"results":              buildCompactSearchFileResults(merged, len(queryResults) > 1),
		"truncated":            truncated,
		"timed_out":            timedOut,
		"summary":              searchSummaryForQueries(queries, searchRoot, len(merged), truncated, timedOut, false),
		"details_truncated":    truncated,
		"provider":             "fff",
		"total_matched":        totals.TotalMatched,
		"total_files":          totals.TotalFiles,
		"query_results":        queryResults,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	if trimmed := strings.TrimSpace(include); trimmed != "" {
		response["include"] = trimmed
	}
	if totals.NextFileOffset > 0 {
		response["next_file_offset"] = totals.NextFileOffset
	}
	if fallback := strings.TrimSpace(totals.RegexFallbackError); fallback != "" {
		response["regex_fallback_error"] = fallback
	}
	return response
}

func buildCompactSearchContentResults(rows []searchContentRow, multiQuery bool) []map[string]any {
	if len(rows) == 0 {
		return []map[string]any{}
	}
	order := make([]string, 0, len(rows))
	groups := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		pathValue := strings.TrimSpace(row.RelativePath)
		if pathValue == "" {
			pathValue = strings.TrimSpace(row.Path)
		}
		key := strings.ToLower(pathValue)
		if key == "" {
			key = fmt.Sprintf("__pathless_%d", len(order))
		}
		group, ok := groups[key]
		if !ok {
			group = map[string]any{
				"path":  pathValue,
				"items": make([]map[string]any, 0, 4),
			}
			groups[key] = group
			order = append(order, key)
		}
		item := map[string]any{
			"line": row.Line,
			"text": row.Text,
		}
		if multiQuery && strings.TrimSpace(row.Query) != "" {
			item["query"] = row.Query
		}
		if row.Column > 0 {
			item["column"] = row.Column
		}
		if row.IsDefinition {
			item["is_definition"] = true
		}
		group["items"] = append(group["items"].([]map[string]any), item)
	}
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func buildCompactSearchFileResults(rows []searchFileRow, multiQuery bool) []map[string]any {
	if len(rows) == 0 {
		return []map[string]any{}
	}
	order := make([]string, 0, len(rows))
	groups := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		pathValue := strings.TrimSpace(row.RelativePath)
		if pathValue == "" {
			pathValue = strings.TrimSpace(row.Path)
		}
		key := strings.ToLower(pathValue)
		if key == "" {
			key = fmt.Sprintf("__pathless_%d", len(order))
		}
		group, ok := groups[key]
		if !ok {
			group = map[string]any{
				"path":  pathValue,
				"items": make([]map[string]any, 0, 4),
			}
			groups[key] = group
			order = append(order, key)
		}
		item := map[string]any{}
		if multiQuery && strings.TrimSpace(row.Query) != "" {
			item["query"] = row.Query
		}
		if row.Score > 0 {
			item["score"] = row.Score
		}
		group["items"] = append(group["items"].([]map[string]any), item)
	}
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func mergeSearchContentRows(results []searchQueryExecution, maxResults int) ([]searchContentRow, bool, string) {
	merged := make([]searchContentRow, 0, maxResults)
	positions := make([]int, len(results))
	var safetySource strings.Builder
	for len(merged) < maxResults {
		progressed := false
		for idx, result := range results {
			if positions[idx] >= len(result.ContentRows) {
				continue
			}
			row := result.ContentRows[positions[idx]]
			positions[idx]++
			merged = append(merged, row)
			appendSearchSafetyText(&safetySource, row.Text)
			progressed = true
			if len(merged) >= maxResults {
				break
			}
		}
		if !progressed {
			break
		}
	}
	truncated := false
	for idx, result := range results {
		if positions[idx] < len(result.ContentRows) {
			truncated = true
			break
		}
	}
	return merged, truncated, safetySource.String()
}

func mergeSearchFileRows(results []searchQueryExecution, maxResults int) ([]searchFileRow, bool) {
	merged := make([]searchFileRow, 0, maxResults)
	positions := make([]int, len(results))
	for len(merged) < maxResults {
		progressed := false
		for idx, result := range results {
			if positions[idx] >= len(result.FileRows) {
				continue
			}
			merged = append(merged, result.FileRows[positions[idx]])
			positions[idx]++
			progressed = true
			if len(merged) >= maxResults {
				break
			}
		}
		if !progressed {
			break
		}
	}
	truncated := false
	for idx, result := range results {
		if positions[idx] < len(result.FileRows) {
			truncated = true
			break
		}
	}
	return merged, truncated
}

func aggregateSearchTotals(results []searchQueryExecution) searchAggregateTotals {
	var totals searchAggregateTotals
	for _, result := range results {
		totals.TotalMatched += result.Totals.TotalMatched
		totals.TotalFilesSearched += result.Totals.TotalFilesSearched
		if result.Totals.TotalFiles > totals.TotalFiles {
			totals.TotalFiles = result.Totals.TotalFiles
		}
		if result.Totals.FilteredFileCount > totals.FilteredFileCount {
			totals.FilteredFileCount = result.Totals.FilteredFileCount
		}
		if result.Totals.NextFileOffset > totals.NextFileOffset {
			totals.NextFileOffset = result.Totals.NextFileOffset
		}
		totals.RegexFallbackError = joinSearchText(totals.RegexFallbackError, result.Totals.RegexFallbackError)
	}
	return totals
}

func searchBatchFlags(results []searchQueryExecution) (bool, bool) {
	truncated := false
	timedOut := false
	for _, result := range results {
		if result.Truncated {
			truncated = true
		}
		if result.TimedOut {
			timedOut = true
			truncated = true
		}
	}
	return truncated, timedOut
}

func appendSearchSafetyText(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if builder == nil || text == "" || builder.Len() >= maxSafetyScanChars {
		return
	}
	if builder.Len() > 0 {
		builder.WriteByte('\n')
	}
	remaining := maxSafetyScanChars - builder.Len()
	if remaining <= 0 {
		return
	}
	if len(text) > remaining {
		builder.WriteString(text[:remaining])
		return
	}
	builder.WriteString(text)
}

func joinSearchText(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	parts := strings.Split(existing, " | ")
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part), next) {
			return existing
		}
	}
	return existing + " | " + next
}

func formatSearchQueryErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		parts = append(parts, strings.TrimSpace(err.Error()))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

func normalizeSearchRelativePath(searchRoot, fullPath, relativePath string) string {
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if relativePath != "" && relativePath != "." {
		return relativePath
	}
	if rel, err := filepath.Rel(searchRoot, fullPath); err == nil {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel != "" && rel != "." && !strings.HasPrefix(rel, "../") {
			return rel
		}
	}
	return filepath.Base(fullPath)
}

func matchesIncludeGlob(include, relativePath string) bool {
	include = strings.TrimSpace(include)
	if include == "" {
		return true
	}
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if relativePath == "" {
		return false
	}
	ok, err := path.Match(include, relativePath)
	if err == nil && ok {
		return true
	}
	ok, err = path.Match(include, path.Base(relativePath))
	return err == nil && ok
}

type webSearchHit struct {
	ID              string         `json:"id,omitempty"`
	URL             string         `json:"url"`
	Title           string         `json:"title,omitempty"`
	PublishedDate   string         `json:"published_date,omitempty"`
	Author          string         `json:"author,omitempty"`
	Score           float64        `json:"score,omitempty"`
	Summary         string         `json:"summary,omitempty"`
	Text            string         `json:"text,omitempty"`
	Highlights      []string       `json:"highlights,omitempty"`
	HighlightScores []float64      `json:"highlight_scores,omitempty"`
	Image           string         `json:"image,omitempty"`
	Favicon         string         `json:"favicon,omitempty"`
	Subpages        []webSearchHit `json:"subpages,omitempty"`
	Extras          map[string]any `json:"extras,omitempty"`
}

type webSearchQueryOutput struct {
	Query               string         `json:"query"`
	Count               int            `json:"count"`
	Results             []webSearchHit `json:"results"`
	RequestID           string         `json:"request_id,omitempty"`
	RequestedSearchType string         `json:"requested_search_type,omitempty"`
	ResolvedSearchType  string         `json:"resolved_search_type,omitempty"`
	SearchTimeMS        float64        `json:"search_time_ms,omitempty"`
	CostDollars         map[string]any `json:"cost_dollars,omitempty"`
	Output              map[string]any `json:"output,omitempty"`
	TimedOut            bool           `json:"timed_out,omitempty"`
	Error               string         `json:"error,omitempty"`
	Summary             string         `json:"summary"`
}

type exaSearchResponse struct {
	RequestID          string            `json:"requestId"`
	ResolvedSearchType string            `json:"resolvedSearchType"`
	Results            []exaSearchResult `json:"results"`
	Output             map[string]any    `json:"output"`
	SearchTime         float64           `json:"searchTime"`
	CostDollars        map[string]any    `json:"costDollars"`
}

type exaSearchResult struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Title           string            `json:"title"`
	PublishedDate   string            `json:"publishedDate"`
	Author          string            `json:"author"`
	Score           float64           `json:"score"`
	Summary         string            `json:"summary"`
	Text            string            `json:"text"`
	Highlights      []string          `json:"highlights"`
	HighlightScores []float64         `json:"highlightScores"`
	Image           string            `json:"image"`
	Favicon         string            `json:"favicon"`
	Subpages        []exaSearchResult `json:"subpages"`
	Extras          map[string]any    `json:"extras"`
}

type exaContentResult struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Title           string            `json:"title"`
	PublishedDate   string            `json:"publishedDate"`
	Author          string            `json:"author"`
	Text            string            `json:"text"`
	Summary         string            `json:"summary"`
	Highlights      []string          `json:"highlights"`
	HighlightScores []float64         `json:"highlightScores"`
	Image           string            `json:"image"`
	Favicon         string            `json:"favicon"`
	Subpages        []exaSearchResult `json:"subpages"`
	Extras          map[string]any    `json:"extras"`
	Error           string            `json:"error"`
}

type exaContentsResponse struct {
	RequestID   string             `json:"requestId"`
	Results     []exaContentResult `json:"results"`
	Statuses    []exaContentStatus `json:"statuses"`
	Error       *exaContentsError  `json:"error"`
	CostDollars map[string]any     `json:"costDollars"`
	SearchTime  float64            `json:"searchTime"`
}

type exaContentStatus struct {
	ID     string               `json:"id"`
	Status string               `json:"status"`
	Source string               `json:"source"`
	Error  *exaContentStatusErr `json:"error"`
}

type exaContentStatusErr struct {
	Tag            string `json:"tag"`
	HTTPStatusCode int    `json:"httpStatusCode"`
}

type exaContentsError struct {
	Message string `json:"message"`
}

type exaContentsTextOptions struct {
	MaxCharacters   int      `json:"maxCharacters,omitempty"`
	IncludeHTMLTags bool     `json:"includeHtmlTags,omitempty"`
	Verbosity       string   `json:"verbosity,omitempty"`
	IncludeSections []string `json:"includeSections,omitempty"`
	ExcludeSections []string `json:"excludeSections,omitempty"`
}

type exaContentsRequestOptions struct {
	Text               any
	Highlights         any
	Summary            map[string]any
	Subpages           int
	SubpageTarget      any
	Extras             map[string]any
	MaxAgeHours        *int
	LivecrawlTimeoutMS int
	Livecrawl          string
}

type exaSearchRequestOptions struct {
	Query              string
	NumResults         int
	SearchType         string
	AdditionalQueries  []string
	Category           string
	UserLocation       string
	IncludeDomains     []string
	ExcludeDomains     []string
	StartCrawlDate     string
	EndCrawlDate       string
	StartPublishedDate string
	EndPublishedDate   string
	IncludeText        []string
	ExcludeText        []string
	Moderation         bool
	SystemPrompt       string
	OutputSchema       map[string]any
	Contents           *exaContentsRequestOptions
}

type mcpErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResultEnvelope struct {
	Content []mcpToolContentItem `json:"content"`
	IsError bool                 `json:"isError"`
}

type mcpToolCallEnvelope struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      any                    `json:"id"`
	Result  *mcpToolResultEnvelope `json:"result,omitempty"`
	Error   *mcpErrorEnvelope      `json:"error,omitempty"`
}

func (r *Runtime) executeWebSearch(parent context.Context, args map[string]any) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	config, err := r.resolveExaConfig(parent)
	if err != nil {
		return "", err
	}

	queries, queryTruncated, err := parseWebSearchQueries(args)
	if err != nil {
		return "", err
	}
	if len(queries) == 0 {
		return "", errors.New("websearch requires query or queries")
	}

	searchType, err := normalizeWebSearchType(asString(args["search_type"]))
	if err != nil {
		return "", err
	}
	numResults := clampInt(asInt(args["num_results"], defaultWebSearchResults), 1, maxWebSearchResults)
	maxParallel := clampInt(asInt(args["max_parallel_queries"], defaultWebSearchParallelQueries), 1, maxWebSearchParallelQueries)
	if maxParallel > len(queries) {
		maxParallel = len(queries)
	}
	if maxParallel <= 0 {
		maxParallel = 1
	}
	timeout := resolveWebTimeout(args["timeout_ms"], defaultWebSearchTimeout, maxWebSearchTimeout)
	contentsOptions, err := parseExaContentsRequestOptions(args["contents"], "websearch contents", false)
	if err != nil {
		return "", err
	}
	additionalQueries := asStringSlice(args["additional_queries"])
	category := strings.TrimSpace(asString(args["category"]))
	userLocation := strings.ToUpper(strings.TrimSpace(asString(args["user_location"])))
	includeDomains := asStringSlice(args["include_domains"])
	excludeDomains := asStringSlice(args["exclude_domains"])
	includeText := asStringSlice(args["include_text"])
	excludeText := asStringSlice(args["exclude_text"])
	startCrawlDate := strings.TrimSpace(asString(args["start_crawl_date"]))
	endCrawlDate := strings.TrimSpace(asString(args["end_crawl_date"]))
	startPublishedDate := strings.TrimSpace(asString(args["start_published_date"]))
	endPublishedDate := strings.TrimSpace(asString(args["end_published_date"]))
	systemPrompt := strings.TrimSpace(asString(args["system_prompt"]))
	outputSchema, err := parseOptionalObjectArg(args["output_schema"], "websearch output_schema")
	if err != nil {
		return "", err
	}
	moderation := boolArgDefault(args["moderation"], false)

	results := make([]webSearchQueryOutput, len(queries))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for idx, query := range queries {
		wg.Add(1)
		sem <- struct{}{}
		go func(index int, currentQuery string) {
			defer wg.Done()
			defer func() { <-sem }()

			out := webSearchQueryOutput{
				Query:               currentQuery,
				Count:               0,
				Results:             []webSearchHit{},
				RequestedSearchType: searchType,
			}
			queryCtx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()

			decoded, err := r.exaSearch(queryCtx, config, exaSearchRequestOptions{
				Query:              currentQuery,
				NumResults:         numResults,
				SearchType:         searchType,
				AdditionalQueries:  additionalQueries,
				Category:           category,
				UserLocation:       userLocation,
				IncludeDomains:     includeDomains,
				ExcludeDomains:     excludeDomains,
				StartCrawlDate:     startCrawlDate,
				EndCrawlDate:       endCrawlDate,
				StartPublishedDate: startPublishedDate,
				EndPublishedDate:   endPublishedDate,
				IncludeText:        includeText,
				ExcludeText:        excludeText,
				Moderation:         moderation,
				SystemPrompt:       systemPrompt,
				OutputSchema:       outputSchema,
				Contents:           contentsOptions,
			})
			if err != nil {
				out.Error = strings.TrimSpace(err.Error())
				out.TimedOut = errors.Is(err, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded)
				out.Summary = fmt.Sprintf("websearch query %q failed", truncateSummary(currentQuery, 64))
				results[index] = out
				return
			}
			out.RequestID = strings.TrimSpace(decoded.RequestID)
			out.ResolvedSearchType = strings.TrimSpace(decoded.ResolvedSearchType)
			if out.ResolvedSearchType == "" {
				out.ResolvedSearchType = searchType
			}
			out.SearchTimeMS = decoded.SearchTime
			if len(decoded.CostDollars) > 0 {
				out.CostDollars = decoded.CostDollars
			}
			if len(decoded.Output) > 0 {
				out.Output = decoded.Output
			}
			out.Results = convertExaSearchResults(decoded.Results)
			out.Count = len(out.Results)
			out.Summary = fmt.Sprintf("query %q returned %d result(s)", truncateSummary(currentQuery, 64), len(out.Results))
			results[index] = out
		}(idx, query)
	}
	wg.Wait()

	failed := 0
	totalResults := 0
	detailsTruncated := queryTruncated
	resolvedSearchTypes := make([]string, 0, len(results))
	requestIDs := make([]string, 0, len(results))
	seenURLs := make(map[string]struct{}, len(results)*4)
	suggestions := make([]map[string]any, 0, len(results)*2)
	var safetyBuilder strings.Builder
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" {
			failed++
		}
		if resolved := strings.TrimSpace(result.ResolvedSearchType); resolved != "" {
			resolvedSearchTypes = appendUniqueCaseInsensitive(resolvedSearchTypes, resolved)
		}
		if requestID := strings.TrimSpace(result.RequestID); requestID != "" {
			requestIDs = append(requestIDs, requestID)
		}
		totalResults += result.Count
		for i, hit := range result.Results {
			if safetyBuilder.Len() > 0 {
				safetyBuilder.WriteByte('\n')
			}
			safetyBuilder.WriteString(strings.TrimSpace(hit.Title))
			if safetyBuilder.Len() > 0 {
				safetyBuilder.WriteByte('\n')
			}
			safetyBuilder.WriteString(strings.TrimSpace(hit.URL))
			if i >= 2 {
				continue
			}
			urlValue := strings.TrimSpace(hit.URL)
			if urlValue == "" {
				continue
			}
			urlKey := strings.ToLower(urlValue)
			if _, ok := seenURLs[urlKey]; ok {
				continue
			}
			seenURLs[urlKey] = struct{}{}
			suggestions = append(suggestions, map[string]any{
				"url":    urlValue,
				"query":  result.Query,
				"reason": fmt.Sprintf("top websearch hit for %q", truncateSummary(result.Query, 72)),
			})
		}
	}

	response := map[string]any{
		"provider":              "exa",
		"path_id":               toolPathID("websearch"),
		"exa_source":            strings.TrimSpace(config.Source),
		"queries":               queries,
		"query":                 firstQueryOrEmpty(queries),
		"query_count":           len(results),
		"num_results":           numResults,
		"requested_search_type": searchType,
		"resolved_search_types": resolvedSearchTypes,
		"total_results":         totalResults,
		"failed_queries":        failed,
		"results":               results,
		"request_ids":           requestIDs,
		"webfetch_suggestions":  suggestions,
		"truncated_queries":     queryTruncated,
		"details_truncated":     detailsTruncated,
		"summary":               fmt.Sprintf("websearch processed %d query(s), returned %d result(s)", len(results), totalResults),
		"safety":                buildUntrustedSafety(safetyBuilder.String()),
		"prompt_injection_tag":  "tool_output_untrusted",
		"exa_search_endpoint":   strings.TrimSpace(config.SearchURL),
		"exa_contents_endpoint": strings.TrimSpace(config.ContentsURL),
		"exa_mcp_endpoint":      strings.TrimSpace(config.MCPURL),
		"contents_requested":    contentsOptions != nil,
		"parallel_query_fanout": maxParallel,
		"additional_queries":    additionalQueries,
		"category":              category,
		"user_location":         userLocation,
		"include_domains":       includeDomains,
		"exclude_domains":       excludeDomains,
		"start_crawl_date":      startCrawlDate,
		"end_crawl_date":        endCrawlDate,
		"start_published_date":  startPublishedDate,
		"end_published_date":    endPublishedDate,
		"include_text":          includeText,
		"exclude_text":          excludeText,
		"moderation":            moderation,
		"system_prompt":         systemPrompt,
	}
	if contentsOptions != nil {
		response["contents"] = contentsOptions.OutputMap()
	}
	if len(outputSchema) > 0 {
		response["output_schema"] = outputSchema
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if failed == len(results) {
		return string(encoded), errors.New("websearch failed for all queries")
	}
	return string(encoded), nil
}

func (r *Runtime) executeWebFetch(parent context.Context, args map[string]any) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	config, err := r.resolveExaConfig(parent)
	if err != nil {
		return "", err
	}

	maxURLs := clampInt(asInt(args["max_urls"], defaultWebFetchURLs), 1, maxWebFetchURLs)
	urls, truncatedURLs, err := parseWebFetchURLs(args, maxURLs)
	if err != nil {
		return "", err
	}
	options, err := parseExaContentsRequestOptions(args, "webfetch", true)
	if err != nil {
		return "", err
	}
	timeout := resolveWebTimeout(args["timeout_ms"], defaultWebFetchTimeout, maxWebFetchTimeout)
	fetchCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	decoded, err := r.exaContents(fetchCtx, config, urls, *options)
	timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(fetchCtx.Err(), context.DeadlineExceeded)
	if err != nil && timedOut {
		decoded.Results = nil
		decoded.Statuses = nil
	}

	records := make([]map[string]any, 0, len(decoded.Results))
	successCount := 0
	var safetyBuilder strings.Builder
	for _, item := range decoded.Results {
		record := mapExaContentResult(item)
		itemErr := strings.TrimSpace(item.Error)
		if itemErr != "" {
			record["error"] = itemErr
		} else {
			successCount++
		}
		appendExaContentSafety(&safetyBuilder, record)
		if title := strings.TrimSpace(item.Title); title != "" {
			if safetyBuilder.Len() > 0 {
				safetyBuilder.WriteByte('\n')
			}
			safetyBuilder.WriteString(title)
		}
		if itemURL := strings.TrimSpace(item.URL); itemURL != "" {
			if safetyBuilder.Len() > 0 {
				safetyBuilder.WriteByte('\n')
			}
			safetyBuilder.WriteString(itemURL)
		}
		records = append(records, record)
	}

	statusRecords := make([]map[string]any, 0, len(decoded.Statuses))
	for _, status := range decoded.Statuses {
		entry := map[string]any{
			"id":     strings.TrimSpace(status.ID),
			"status": strings.TrimSpace(status.Status),
		}
		if source := strings.TrimSpace(status.Source); source != "" {
			entry["source"] = source
		}
		if status.Error != nil {
			entry["error"] = map[string]any{
				"tag":              strings.TrimSpace(status.Error.Tag),
				"http_status_code": status.Error.HTTPStatusCode,
			}
		}
		statusRecords = append(statusRecords, entry)
	}

	detailsTruncated := truncatedURLs || timedOut
	response := map[string]any{
		"provider":                  "exa",
		"path_id":                   toolPathID("webfetch"),
		"exa_source":                strings.TrimSpace(config.Source),
		"urls":                      urls,
		"url":                       firstQueryOrEmpty(urls),
		"count":                     len(records),
		"success_count":             successCount,
		"timed_out":                 timedOut,
		"truncated_urls":            truncatedURLs,
		"details_truncated":         detailsTruncated,
		"results":                   records,
		"statuses":                  statusRecords,
		"status_count":              len(statusRecords),
		"summary":                   fmt.Sprintf("webfetch processed %d URL(s), returned %d record(s)", len(urls), len(records)),
		"safety":                    buildUntrustedSafety(safetyBuilder.String()),
		"prompt_injection_tag":      "tool_output_untrusted",
		"exa_search_endpoint":       strings.TrimSpace(config.SearchURL),
		"exa_contents_endpoint":     strings.TrimSpace(config.ContentsURL),
		"exa_mcp_endpoint":          strings.TrimSpace(config.MCPURL),
		"allowed_exa_endpoints":     []string{"/search", "/contents"},
		"answer_endpoint_supported": false,
		"request_id":                strings.TrimSpace(decoded.RequestID),
		"search_time_ms":            decoded.SearchTime,
		"contents":                  options.OutputMap(),
	}
	if len(decoded.CostDollars) > 0 {
		response["cost_dollars"] = decoded.CostDollars
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		response["exa_error"] = strings.TrimSpace(decoded.Error.Message)
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil && !timedOut {
		return string(encoded), err
	}
	if successCount == 0 {
		if err != nil {
			return string(encoded), err
		}
		return string(encoded), errors.New("webfetch returned no successful records")
	}
	return string(encoded), nil
}

func (r *Runtime) executeWebDownload(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	config, err := r.resolveExaConfig(parent)
	if err != nil {
		return "", err
	}

	maxURLs := clampInt(asInt(args["max_urls"], defaultWebFetchURLs), 1, maxWebFetchURLs)
	urls, truncatedURLs, err := parseWebFetchURLs(args, maxURLs)
	if err != nil {
		return "", err
	}
	livecrawl := strings.ToLower(strings.TrimSpace(asString(args["livecrawl"])))
	if livecrawl != "" && livecrawl != "never" && livecrawl != "fallback" && livecrawl != "always" && livecrawl != "auto" {
		return "", errors.New("webdownload livecrawl must be one of: never, fallback, always, auto")
	}
	filenameMode := strings.ToLower(strings.TrimSpace(asString(args["filename_mode"])))
	if filenameMode == "" {
		filenameMode = "host_slug"
	}
	if filenameMode != "host_slug" && filenameMode != "sha1" {
		return "", errors.New("webdownload filename_mode must be one of: host_slug, sha1")
	}

	outputDirArg := strings.TrimSpace(asString(args["output_dir"]))
	if outputDirArg == "" {
		outputDirArg = defaultWebDownloadDir
	}
	outputDirPath, err := resolveWorkspacePath(scope, outputDirArg)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(outputDirPath, 0o755); err != nil {
		return "", fmt.Errorf("create download directory: %w", err)
	}

	timeout := resolveWebTimeout(args["timeout_ms"], defaultWebFetchTimeout, maxWebFetchTimeout)
	fetchCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	options := exaContentsRequestOptions{
		Text: map[string]any{
			"max_characters": maxWebFetchTextCharsPerURL,
		},
		Livecrawl: livecrawl,
	}
	decoded, err := r.exaContents(fetchCtx, config, urls, options)
	timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(fetchCtx.Err(), context.DeadlineExceeded)
	if err != nil && timedOut {
		decoded.Results = nil
		decoded.Statuses = nil
	}

	manifest := make([]map[string]any, 0, len(decoded.Results))
	successCount := 0
	writeErrors := make([]string, 0, len(decoded.Results))
	var safetyBuilder strings.Builder
	for i, item := range decoded.Results {
		entry := map[string]any{
			"id":             strings.TrimSpace(item.ID),
			"url":            strings.TrimSpace(item.URL),
			"title":          strings.TrimSpace(item.Title),
			"published_date": strings.TrimSpace(item.PublishedDate),
			"author":         strings.TrimSpace(item.Author),
		}
		if itemErr := strings.TrimSpace(item.Error); itemErr != "" {
			entry["error"] = itemErr
			manifest = append(manifest, entry)
			continue
		}
		fileName := webDownloadFilename(item.URL, i, filenameMode)
		targetPath := filepath.Join(outputDirPath, fileName)
		text := strings.TrimSpace(sanitizeForToolOutput(item.Text))
		if err := os.WriteFile(targetPath, []byte(text), 0o644); err != nil {
			entry["error"] = fmt.Sprintf("write failed: %v", err)
			writeErrors = append(writeErrors, fmt.Sprintf("%s: %v", strings.TrimSpace(item.URL), err))
			manifest = append(manifest, entry)
			continue
		}
		relPath, _ := filepath.Rel(scope.PrimaryPath, targetPath)
		relPath = filepath.ToSlash(strings.TrimSpace(relPath))
		if relPath == "" {
			relPath = filepath.ToSlash(targetPath)
		}
		entry["file_path"] = relPath
		entry["bytes_written"] = len(text)
		successCount++
		if safetyBuilder.Len() > 0 {
			safetyBuilder.WriteByte('\n')
		}
		safetyBuilder.WriteString(strings.TrimSpace(item.Title))
		if safetyBuilder.Len() > 0 {
			safetyBuilder.WriteByte('\n')
		}
		safetyBuilder.WriteString(strings.TrimSpace(item.URL))
		manifest = append(manifest, entry)
	}

	statusRecords := make([]map[string]any, 0, len(decoded.Statuses))
	for _, status := range decoded.Statuses {
		entry := map[string]any{
			"id":     strings.TrimSpace(status.ID),
			"status": strings.TrimSpace(status.Status),
		}
		if source := strings.TrimSpace(status.Source); source != "" {
			entry["source"] = source
		}
		if status.Error != nil {
			entry["error"] = map[string]any{
				"tag":              strings.TrimSpace(status.Error.Tag),
				"http_status_code": status.Error.HTTPStatusCode,
			}
		}
		statusRecords = append(statusRecords, entry)
	}

	detailsTruncated := truncatedURLs || timedOut
	response := map[string]any{
		"provider":                  "exa",
		"path_id":                   toolPathID("webdownload"),
		"exa_source":                strings.TrimSpace(config.Source),
		"urls":                      urls,
		"count":                     len(manifest),
		"success_count":             successCount,
		"timed_out":                 timedOut,
		"truncated_urls":            truncatedURLs,
		"details_truncated":         detailsTruncated,
		"output_dir":                filepath.ToSlash(strings.TrimSpace(outputDirArg)),
		"filename_mode":             filenameMode,
		"manifest":                  manifest,
		"statuses":                  statusRecords,
		"status_count":              len(statusRecords),
		"summary":                   fmt.Sprintf("webdownload processed %d URL(s), wrote %d file(s)", len(urls), successCount),
		"write_errors":              writeErrors,
		"safety":                    buildUntrustedSafety(safetyBuilder.String()),
		"prompt_injection_tag":      "tool_output_untrusted",
		"exa_search_endpoint":       strings.TrimSpace(config.SearchURL),
		"exa_contents_endpoint":     strings.TrimSpace(config.ContentsURL),
		"exa_mcp_endpoint":          strings.TrimSpace(config.MCPURL),
		"allowed_exa_endpoints":     []string{"/search", "/contents"},
		"answer_endpoint_supported": false,
	}
	if decoded.Error != nil {
		response["exa_error"] = strings.TrimSpace(decoded.Error.Message)
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	if err != nil && !timedOut {
		return string(encoded), err
	}
	if successCount == 0 {
		if err != nil {
			return string(encoded), err
		}
		if len(writeErrors) > 0 {
			return string(encoded), errors.New("webdownload failed to write any files")
		}
		return string(encoded), errors.New("webdownload returned no successful records")
	}
	return string(encoded), nil
}

func webDownloadFilename(rawURL string, index int, mode string) string {
	u := strings.TrimSpace(rawURL)
	if mode == "sha1" {
		hash := sha1.Sum([]byte(u))
		return fmt.Sprintf("%03d-%s.txt", index+1, hex.EncodeToString(hash[:]))
	}
	parsed, err := url.Parse(u)
	host := "url"
	pathPart := "index"
	if err == nil {
		if h := strings.TrimSpace(parsed.Hostname()); h != "" {
			host = h
		}
		if p := strings.TrimSpace(parsed.Path); p != "" && p != "/" {
			pathPart = strings.Trim(p, "/")
		}
	}
	host = slugifyFilenameComponent(host)
	pathPart = slugifyFilenameComponent(pathPart)
	if host == "" {
		host = "url"
	}
	if pathPart == "" {
		pathPart = "index"
	}
	name := fmt.Sprintf("%03d-%s-%s.txt", index+1, host, pathPart)
	if len(name) > 180 {
		name = name[:180]
		name = strings.TrimRight(name, "-_.") + ".txt"
	}
	return name
}

func slugifyFilenameComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (r *Runtime) resolveExaConfig(ctx context.Context) (ExaRuntimeConfig, error) {
	if r == nil || r.exaConfigResolver == nil {
		return ExaRuntimeConfig{}, errors.New("websearch is not configured; exa resolver unavailable")
	}
	config, err := r.exaConfigResolver(ctx)
	if err != nil {
		return ExaRuntimeConfig{}, err
	}
	if !config.Enabled {
		return ExaRuntimeConfig{}, errors.New("exa websearch is unavailable: configure /auth key exa <api_key>; built-in free Exa MCP search is unavailable")
	}
	config.Source = strings.ToLower(strings.TrimSpace(config.Source))
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.MCPURL = strings.TrimSpace(config.MCPURL)
	if config.Source == "" {
		switch {
		case config.APIKey != "":
			config.Source = "api_key"
		case config.MCPURL != "":
			config.Source = "mcp"
		default:
			return ExaRuntimeConfig{}, errors.New("exa source is unavailable: configure /auth key exa <api_key>; built-in free Exa MCP search is unavailable")
		}
	}
	if config.Source != "mcp" && config.Source != "api_key" {
		return ExaRuntimeConfig{}, fmt.Errorf("invalid exa source %q", config.Source)
	}
	if config.Source == "api_key" && config.APIKey == "" {
		return ExaRuntimeConfig{}, errors.New("exa api key is missing for API-key mode (run /auth key exa <api_key>)")
	}
	if config.Source == "mcp" && config.MCPURL == "" {
		return ExaRuntimeConfig{}, errors.New("built-in free Exa MCP endpoint is missing")
	}
	if config.APIKey == "" {
		config.Source = "mcp"
	}
	config.SearchURL = strings.TrimSpace(config.SearchURL)
	if config.SearchURL == "" {
		config.SearchURL = defaultExaSearchURL
	}
	config.ContentsURL = strings.TrimSpace(config.ContentsURL)
	if config.ContentsURL == "" {
		config.ContentsURL = defaultExaContentsURL
	}
	return config, nil
}

func (r *Runtime) exaSearch(ctx context.Context, config ExaRuntimeConfig, options exaSearchRequestOptions) (exaSearchResponse, error) {
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" {
		return exaSearchResponse{}, errors.New("query is required")
	}
	if strings.EqualFold(strings.TrimSpace(config.Source), "mcp") {
		hits, err := r.exaSearchViaMCP(ctx, config, options.Query, options.NumResults, options.SearchType)
		if err != nil {
			return exaSearchResponse{}, err
		}
		return exaSearchResponse{
			ResolvedSearchType: normalizeMCPExaSearchType(options.SearchType),
			Results:            convertSearchHitsToExaResults(hits),
		}, nil
	}

	payload := map[string]any{
		"query":      options.Query,
		"numResults": options.NumResults,
		"type":       options.SearchType,
	}
	if len(options.AdditionalQueries) > 0 {
		payload["additionalQueries"] = options.AdditionalQueries
	}
	if options.Category != "" {
		payload["category"] = options.Category
	}
	if options.UserLocation != "" {
		payload["userLocation"] = options.UserLocation
	}
	if len(options.IncludeDomains) > 0 {
		payload["includeDomains"] = options.IncludeDomains
	}
	if len(options.ExcludeDomains) > 0 {
		payload["excludeDomains"] = options.ExcludeDomains
	}
	if options.StartCrawlDate != "" {
		payload["startCrawlDate"] = options.StartCrawlDate
	}
	if options.EndCrawlDate != "" {
		payload["endCrawlDate"] = options.EndCrawlDate
	}
	if options.StartPublishedDate != "" {
		payload["startPublishedDate"] = options.StartPublishedDate
	}
	if options.EndPublishedDate != "" {
		payload["endPublishedDate"] = options.EndPublishedDate
	}
	if len(options.IncludeText) > 0 {
		payload["includeText"] = options.IncludeText
	}
	if len(options.ExcludeText) > 0 {
		payload["excludeText"] = options.ExcludeText
	}
	if options.Moderation {
		payload["moderation"] = true
	}
	if options.SystemPrompt != "" {
		payload["systemPrompt"] = options.SystemPrompt
	}
	if len(options.OutputSchema) > 0 {
		payload["outputSchema"] = options.OutputSchema
	}
	if options.Contents != nil {
		contentsPayload := map[string]any{}
		options.Contents.ApplyExaPayload(contentsPayload)
		if len(contentsPayload) > 0 {
			payload["contents"] = contentsPayload
		}
	}

	var decoded exaSearchResponse
	if err := r.doExaRequest(ctx, config.SearchURL, config.APIKey, payload, &decoded); err != nil {
		return exaSearchResponse{}, err
	}
	return decoded, nil
}

func (r *Runtime) exaContents(ctx context.Context, config ExaRuntimeConfig, urls []string, options exaContentsRequestOptions) (exaContentsResponse, error) {
	if len(urls) == 0 {
		return exaContentsResponse{}, errors.New("urls are required")
	}
	if strings.EqualFold(strings.TrimSpace(config.Source), "mcp") {
		return r.exaContentsViaMCP(ctx, config, urls, options)
	}
	payload := map[string]any{
		"urls": urls,
	}
	options.ApplyExaPayload(payload)

	var decoded exaContentsResponse
	if err := r.doExaRequest(ctx, config.ContentsURL, config.APIKey, payload, &decoded); err != nil {
		return exaContentsResponse{}, err
	}
	return decoded, nil
}

func (r *Runtime) exaSearchViaMCP(ctx context.Context, config ExaRuntimeConfig, query string, maxResults int, searchType string) ([]webSearchHit, error) {
	mcpURL := strings.TrimSpace(config.MCPURL)
	if mcpURL == "" {
		return nil, errors.New("exa mcp endpoint is not configured")
	}
	args := map[string]any{
		"query":      query,
		"numResults": maxResults,
		"type":       normalizeMCPExaSearchType(searchType),
	}
	textOutput, err := r.doMCPToolCall(ctx, mcpURL, "web_search_exa", args)
	if err != nil {
		return nil, err
	}
	hits := parseMCPExaSearchHits(textOutput, maxResults)
	if len(hits) == 0 {
		return nil, errors.New("exa mcp websearch returned no URL results")
	}
	return hits, nil
}

func (r *Runtime) exaContentsViaMCP(ctx context.Context, config ExaRuntimeConfig, urls []string, options exaContentsRequestOptions) (exaContentsResponse, error) {
	mcpURL := strings.TrimSpace(config.MCPURL)
	if mcpURL == "" {
		return exaContentsResponse{}, errors.New("exa mcp endpoint is not configured")
	}
	args := map[string]any{
		"urls": urls,
	}
	maxCharacters := mcpExaFetchMaxCharacters(options)
	if maxCharacters > 0 {
		args["maxCharacters"] = maxCharacters
	}
	textOutput, err := r.doMCPToolCall(ctx, mcpURL, "web_fetch_exa", args)
	if err != nil {
		return exaContentsResponse{}, err
	}
	results, statuses := parseMCPExaContentResults(textOutput, urls)
	if len(results) == 0 && len(statuses) == 0 {
		return exaContentsResponse{}, errors.New("exa mcp webfetch returned no content")
	}
	return exaContentsResponse{
		Results:  results,
		Statuses: statuses,
	}, nil
}

func normalizeMCPExaSearchType(searchType string) string {
	switch strings.ToLower(strings.TrimSpace(searchType)) {
	case "fast", "instant":
		return "fast"
	default:
		return "auto"
	}
}

func parseMCPExaContentResults(raw string, requestedURLs []string) ([]exaContentResult, []exaContentStatus) {
	text := strings.TrimSpace(sanitizeForToolOutput(raw))
	if text == "" {
		return nil, nil
	}
	sections := splitMCPMarkdownSections(text)
	if len(sections) == 0 {
		sections = []string{text}
	}
	results := make([]exaContentResult, 0, len(sections))
	statuses := make([]exaContentStatus, 0)
	seenURLs := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		result := parseMCPExaContentSection(section)
		if strings.TrimSpace(result.Error) != "" {
			statuses = append(statuses, exaContentStatus{
				ID:     firstNonEmptyString(result.URL, result.ID),
				Status: "error",
				Source: "mcp",
				Error:  &exaContentStatusErr{Tag: strings.TrimSpace(result.Error)},
			})
			continue
		}
		result.URL = strings.TrimSpace(result.URL)
		if result.URL == "" && len(requestedURLs) == 1 {
			result.URL = strings.TrimSpace(requestedURLs[0])
		}
		if result.URL == "" && strings.TrimSpace(result.Text) == "" {
			continue
		}
		key := strings.ToLower(result.URL)
		if key != "" {
			if _, ok := seenURLs[key]; ok {
				continue
			}
			seenURLs[key] = struct{}{}
		}
		if result.ID == "" {
			result.ID = result.URL
		}
		results = append(results, result)
	}
	return results, statuses
}

func splitMCPMarkdownSections(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	sections := make([]string, 0, 4)
	var current []string
	flush := func() {
		section := strings.TrimSpace(strings.Join(current, "\n"))
		if section != "" {
			sections = append(sections, section)
		}
		current = nil
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return sections
}

func parseMCPExaContentSection(section string) exaContentResult {
	lines := strings.Split(strings.ReplaceAll(section, "\r\n", "\n"), "\n")
	result := exaContentResult{}
	var body []string
	inBody := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Error fetching ") {
			message := strings.TrimSpace(strings.TrimPrefix(trimmed, "Error fetching "))
			if splitAt := strings.LastIndex(message, ": "); splitAt >= 0 {
				result.URL = strings.TrimSpace(message[:splitAt])
				result.ID = result.URL
				result.Error = strings.TrimSpace(message[splitAt+2:])
			} else {
				result.Error = message
			}
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && result.Title == "" && !inBody {
			result.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			continue
		}
		if strings.HasPrefix(trimmed, "URL:") && !inBody {
			result.URL = strings.TrimSpace(strings.TrimPrefix(trimmed, "URL:"))
			result.ID = result.URL
			continue
		}
		if strings.HasPrefix(trimmed, "Published:") && !inBody {
			result.PublishedDate = strings.TrimSpace(strings.TrimPrefix(trimmed, "Published:"))
			continue
		}
		if strings.HasPrefix(trimmed, "Author:") && !inBody {
			result.Author = strings.TrimSpace(strings.TrimPrefix(trimmed, "Author:"))
			continue
		}
		if trimmed == "" && !inBody {
			if result.Title != "" || result.URL != "" || result.PublishedDate != "" || result.Author != "" {
				inBody = true
			}
			continue
		}
		inBody = true
		body = append(body, line)
	}
	result.Title = strings.TrimSpace(sanitizeForToolOutput(result.Title))
	result.Author = strings.TrimSpace(result.Author)
	result.PublishedDate = strings.TrimSpace(result.PublishedDate)
	result.Text = strings.TrimSpace(sanitizeForToolOutput(strings.Join(body, "\n")))
	if result.Title == "(no title)" {
		result.Title = ""
	}
	return result
}

func mcpExaFetchMaxCharacters(options exaContentsRequestOptions) int {
	switch text := options.Text.(type) {
	case map[string]any:
		return asInt(text["max_characters"], 0)
	case map[string]string:
		return asInt(text["max_characters"], 0)
	case exaContentsTextOptions:
		return text.MaxCharacters
	case *exaContentsTextOptions:
		if text != nil {
			return text.MaxCharacters
		}
	}
	return 0
}

func parseMCPExaSearchHits(raw string, maxResults int) []webSearchHit {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := make([]webSearchHit, 0, maxResults)
	seen := make(map[string]struct{}, maxResults)
	current := webSearchHit{}
	hasCurrent := false
	flush := func() {
		if !hasCurrent {
			return
		}
		current.URL = strings.TrimSpace(current.URL)
		if current.URL != "" {
			key := strings.ToLower(current.URL)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				current.Title = strings.TrimSpace(sanitizeForToolOutput(current.Title))
				current.Author = strings.TrimSpace(current.Author)
				current.PublishedDate = strings.TrimSpace(current.PublishedDate)
				out = append(out, current)
			}
		}
		current = webSearchHit{}
		hasCurrent = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Title:"):
			if hasCurrent {
				flush()
				if maxResults > 0 && len(out) >= maxResults {
					return out[:maxResults]
				}
			}
			current.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "Title:"))
			hasCurrent = true
		case strings.HasPrefix(trimmed, "URL:"):
			current.URL = strings.TrimSpace(strings.TrimPrefix(trimmed, "URL:"))
			hasCurrent = true
		case strings.HasPrefix(trimmed, "Author:"):
			current.Author = strings.TrimSpace(strings.TrimPrefix(trimmed, "Author:"))
			hasCurrent = true
		case strings.HasPrefix(trimmed, "Published Date:"):
			current.PublishedDate = strings.TrimSpace(strings.TrimPrefix(trimmed, "Published Date:"))
			hasCurrent = true
		case trimmed == "":
			if hasCurrent && strings.TrimSpace(current.URL) != "" {
				flush()
				if maxResults > 0 && len(out) >= maxResults {
					return out[:maxResults]
				}
			}
		}
	}
	flush()
	if maxResults > 0 && len(out) > maxResults {
		return out[:maxResults]
	}
	return out
}

func (r *Runtime) doMCPToolCall(ctx context.Context, endpoint, toolName string, args map[string]any) (string, error) {
	if r == nil {
		return "", errors.New("runtime is not configured")
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errors.New("mcp endpoint is required")
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      "swarm-exa-tool-call",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      strings.TrimSpace(toolName),
			"arguments": args,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal mcp request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	client := r.httpClient
	if client == nil {
		client = &http.Client{Timeout: maxWebFetchTimeout + 5*time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWebResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		trimmed := strings.TrimSpace(sanitizeForToolOutput(string(raw)))
		trimmed, _ = clampRunesWithEllipsis(trimmed, 500)
		if trimmed == "" {
			return "", fmt.Errorf("mcp request failed status=%d", resp.StatusCode)
		}
		return "", fmt.Errorf("mcp request failed status=%d body=%s", resp.StatusCode, trimmed)
	}
	return parseMCPToolCallOutput(raw, resp.Header.Get("Content-Type"))
}

func parseMCPToolCallOutput(raw []byte, contentType string) (string, error) {
	envelope, err := decodeMCPToolEnvelope(raw, contentType)
	if err != nil {
		return "", err
	}
	if envelope.Error != nil {
		msg := strings.TrimSpace(envelope.Error.Message)
		if msg == "" {
			msg = "mcp tool call failed"
		}
		return "", errors.New(msg)
	}
	if envelope.Result == nil {
		return "", errors.New("mcp tool call returned empty result")
	}
	var textParts []string
	for _, item := range envelope.Result.Content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "text") {
			continue
		}
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		textParts = append(textParts, text)
	}
	if len(textParts) == 0 {
		return "", errors.New("mcp tool call returned no text output")
	}
	out := strings.TrimSpace(strings.Join(textParts, "\n"))
	if envelope.Result.IsError {
		if out == "" {
			out = "mcp tool call failed"
		}
		return "", errors.New(out)
	}
	return out, nil
}

func decodeMCPToolEnvelope(raw []byte, contentType string) (mcpToolCallEnvelope, error) {
	var envelope mcpToolCallEnvelope
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(contentType, "application/json") {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return mcpToolCallEnvelope{}, fmt.Errorf("decode mcp json response: %w", err)
		}
		return envelope, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 4096), maxWebResponseBytes)
	candidates := make([]string, 0, 8)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		candidates = append(candidates, payload)
	}
	if err := scanner.Err(); err != nil {
		return mcpToolCallEnvelope{}, fmt.Errorf("decode mcp event stream: %w", err)
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		payload := candidates[i]
		if strings.EqualFold(payload, "[done]") {
			continue
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err == nil {
			return envelope, nil
		}
	}
	trimmed := strings.TrimSpace(sanitizeForToolOutput(string(raw)))
	trimmed, _ = clampRunesWithEllipsis(trimmed, 500)
	if trimmed == "" {
		return mcpToolCallEnvelope{}, errors.New("mcp response did not include a parseable JSON payload")
	}
	return mcpToolCallEnvelope{}, fmt.Errorf("mcp response did not include a parseable JSON payload: %s", trimmed)
}

func (r *Runtime) doExaRequest(ctx context.Context, endpoint, apiKey string, payload map[string]any, out any) error {
	if r == nil {
		return errors.New("runtime is not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal exa request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(endpoint), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", strings.TrimSpace(apiKey))

	client := r.httpClient
	if client == nil {
		client = &http.Client{Timeout: maxWebFetchTimeout + 5*time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWebResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		trimmed := strings.TrimSpace(sanitizeForToolOutput(string(raw)))
		trimmed, _ = clampRunesWithEllipsis(trimmed, 500)
		if trimmed == "" {
			return fmt.Errorf("exa request failed status=%d", resp.StatusCode)
		}
		return fmt.Errorf("exa request failed status=%d body=%s", resp.StatusCode, trimmed)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode exa response: %w", err)
	}
	return nil
}

func normalizeWebSearchType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "auto", nil
	case "instant":
		return "instant", nil
	case "fast":
		return "fast", nil
	case "neural":
		return "neural", nil
	case "deep":
		return "deep", nil
	case "deep-reasoning":
		return "deep-reasoning", nil
	default:
		return "", errors.New("websearch search_type must be one of: instant, auto, fast, neural, deep, deep-reasoning")
	}
}

func parseWebSearchQueries(args map[string]any) ([]string, bool, error) {
	queries := make([]string, 0, 8)
	if single := strings.TrimSpace(asString(args["query"])); single != "" {
		queries = append(queries, single)
	}
	queries = append(queries, asStringSlice(args["queries"])...)
	if len(queries) == 0 {
		return nil, false, nil
	}
	seen := make(map[string]struct{}, len(queries))
	deduped := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, query)
	}
	if len(deduped) == 0 {
		return nil, false, errors.New("websearch requires at least one non-empty query")
	}
	truncated := false
	if len(deduped) > maxWebSearchQueries {
		deduped = deduped[:maxWebSearchQueries]
		truncated = true
	}
	return deduped, truncated, nil
}

func parseWebFetchURLs(args map[string]any, maxURLs int) ([]string, bool, error) {
	urls := make([]string, 0, maxURLs)
	if single := strings.TrimSpace(asString(args["url"])); single != "" {
		urls = append(urls, single)
	}
	urls = append(urls, asStringSlice(args["urls"])...)
	seen := make(map[string]struct{}, len(urls))
	deduped := make([]string, 0, len(urls))
	for _, value := range urls {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, value)
	}
	if len(deduped) == 0 {
		return nil, false, errors.New("webfetch requires url or urls")
	}
	truncated := false
	if len(deduped) > maxURLs {
		deduped = deduped[:maxURLs]
		truncated = true
	}
	return deduped, truncated, nil
}

func parseExaContentsRequestOptions(raw any, fieldLabel string, defaultText bool) (*exaContentsRequestOptions, error) {
	if raw == nil {
		if defaultText {
			return &exaContentsRequestOptions{Text: true}, nil
		}
		return nil, nil
	}
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, err
	}
	options := &exaContentsRequestOptions{}
	hasAny := false

	if value, exists := typed["text"]; exists {
		parsed, set, err := parseExaTextOption(value, fieldLabel+".text")
		if err != nil {
			return nil, err
		}
		if set {
			options.Text = parsed
			hasAny = true
		}
	}
	if value, exists := typed["highlights"]; exists {
		parsed, set, err := parseExaHighlightsOption(value, fieldLabel+".highlights")
		if err != nil {
			return nil, err
		}
		if set {
			options.Highlights = parsed
			hasAny = true
		}
	}
	if value, exists := typed["summary"]; exists && value != nil {
		parsed, err := parseExaSummaryOption(value, fieldLabel+".summary")
		if err != nil {
			return nil, err
		}
		if len(parsed) > 0 {
			options.Summary = parsed
			hasAny = true
		}
	}
	if value, exists := typed["subpages"]; exists && value != nil {
		subpages := asInt(value, -1)
		if subpages < 0 {
			return nil, fmt.Errorf("%s.subpages must be a non-negative integer", fieldLabel)
		}
		options.Subpages = subpages
		hasAny = true
	}
	if value, exists := typed["subpage_target"]; exists && value != nil {
		switch typedValue := value.(type) {
		case string:
			target := strings.TrimSpace(typedValue)
			if target != "" {
				options.SubpageTarget = target
				hasAny = true
			}
		case []any, []string:
			targets := asStringSlice(value)
			if targets == nil {
				return nil, fmt.Errorf("%s.subpage_target must be a string or string array", fieldLabel)
			}
			if len(targets) > 0 {
				options.SubpageTarget = targets
				hasAny = true
			}
		default:
			return nil, fmt.Errorf("%s.subpage_target must be a string or string array", fieldLabel)
		}
	}
	if value, exists := typed["extras"]; exists && value != nil {
		parsed, err := parseExaExtrasOption(value, fieldLabel+".extras")
		if err != nil {
			return nil, err
		}
		if len(parsed) > 0 {
			options.Extras = parsed
			hasAny = true
		}
	}
	if value, exists := typed["max_age_hours"]; exists && value != nil {
		maxAge := asInt(value, 0)
		options.MaxAgeHours = &maxAge
		hasAny = true
	}
	if value, exists := typed["livecrawl_timeout_ms"]; exists && value != nil {
		timeout := asInt(value, -1)
		if timeout <= 0 {
			return nil, fmt.Errorf("%s.livecrawl_timeout_ms must be a positive integer", fieldLabel)
		}
		options.LivecrawlTimeoutMS = timeout
		hasAny = true
	}

	if !hasAny && defaultText {
		options.Text = true
		hasAny = true
	}
	if !hasAny {
		return nil, nil
	}
	return options, nil
}

func parseExaTextOption(raw any, fieldLabel string) (any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if flag, ok := raw.(bool); ok {
		return flag, true, nil
	}
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, false, fmt.Errorf("%s must be a boolean, object, or JSON object string", fieldLabel)
	}
	parsed := map[string]any{}
	if value, exists := typed["max_characters"]; exists && value != nil {
		maxChars := asInt(value, -1)
		if maxChars <= 0 {
			return nil, false, fmt.Errorf("%s.max_characters must be a positive integer", fieldLabel)
		}
		parsed["max_characters"] = clampInt(maxChars, 1, maxWebFetchTextCharsPerURL)
	}
	if value, exists := typed["include_html_tags"]; exists && value != nil {
		parsedBool, ok := value.(bool)
		if !ok {
			return nil, false, fmt.Errorf("%s.include_html_tags must be a boolean", fieldLabel)
		}
		parsed["include_html_tags"] = parsedBool
	}
	if value, exists := typed["verbosity"]; exists && value != nil {
		verbosity, ok := normalizeExaTextVerbosity(value)
		if !ok {
			return nil, false, fmt.Errorf("%s.verbosity must be one of: compact, standard, full", fieldLabel)
		}
		if verbosity != "" {
			parsed["verbosity"] = verbosity
		}
	}
	if value, exists := typed["include_sections"]; exists && value != nil {
		sections, err := parseExaTextSections(value, fieldLabel+".include_sections")
		if err != nil {
			return nil, false, err
		}
		if len(sections) > 0 {
			parsed["include_sections"] = sections
		}
	}
	if value, exists := typed["exclude_sections"]; exists && value != nil {
		sections, err := parseExaTextSections(value, fieldLabel+".exclude_sections")
		if err != nil {
			return nil, false, err
		}
		if len(sections) > 0 {
			parsed["exclude_sections"] = sections
		}
	}
	if len(parsed) == 0 {
		return true, true, nil
	}
	return parsed, true, nil
}

func normalizeExaTextVerbosity(raw any) (string, bool) {
	verbosity := strings.ToLower(strings.TrimSpace(asString(raw)))
	switch verbosity {
	case "":
		return "", true
	case "compact", "standard", "full":
		return verbosity, true
	case "medium":
		return "standard", true
	default:
		return "", false
	}
}

func parseExaTextSections(raw any, fieldLabel string) ([]string, error) {
	sections := asStringSlice(raw)
	if sections == nil {
		return nil, fmt.Errorf("%s must be an array of strings", fieldLabel)
	}
	if len(sections) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(sections))
	seen := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		normalized, ok := normalizeExaTextSection(section)
		if !ok {
			return nil, fmt.Errorf("%s must only include: header, navigation, banner, body, sidebar, footer, metadata", fieldLabel)
		}
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeExaTextSection(raw string) (string, bool) {
	section := strings.ToLower(strings.TrimSpace(raw))
	switch section {
	case "":
		return "", true
	case "header", "navigation", "banner", "body", "sidebar", "footer", "metadata":
		return section, true
	case "main", "article", "content":
		return "body", true
	default:
		return "", false
	}
}

func parseExaHighlightsOption(raw any, fieldLabel string) (any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if flag, ok := raw.(bool); ok {
		return flag, true, nil
	}
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, false, fmt.Errorf("%s must be a boolean, object, or JSON object string", fieldLabel)
	}
	parsed := map[string]any{}
	if value, exists := typed["max_characters"]; exists && value != nil {
		maxChars := asInt(value, -1)
		if maxChars <= 0 {
			return nil, false, fmt.Errorf("%s.max_characters must be a positive integer", fieldLabel)
		}
		parsed["max_characters"] = maxChars
	}
	if value, exists := typed["num_sentences"]; exists && value != nil {
		numSentences := asInt(value, -1)
		if numSentences <= 0 {
			return nil, false, fmt.Errorf("%s.num_sentences must be a positive integer", fieldLabel)
		}
		parsed["num_sentences"] = numSentences
	}
	if value, exists := typed["highlights_per_url"]; exists && value != nil {
		perURL := asInt(value, -1)
		if perURL <= 0 {
			return nil, false, fmt.Errorf("%s.highlights_per_url must be a positive integer", fieldLabel)
		}
		parsed["highlights_per_url"] = perURL
	}
	if value, exists := typed["query"]; exists && value != nil {
		query := strings.TrimSpace(asString(value))
		if query != "" {
			parsed["query"] = query
		}
	}
	if len(parsed) == 0 {
		return true, true, nil
	}
	return parsed, true, nil
}

func parseExaSummaryOption(raw any, fieldLabel string) (map[string]any, error) {
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, err
	}
	parsed := map[string]any{}
	if value, exists := typed["query"]; exists && value != nil {
		query := strings.TrimSpace(asString(value))
		if query != "" {
			parsed["query"] = query
		}
	}
	if value, exists := typed["schema"]; exists && value != nil {
		schema, err := parseJSONObjectInput(value, fieldLabel+".schema")
		if err != nil {
			return nil, err
		}
		if len(schema) > 0 {
			parsed["schema"] = schema
		}
	}
	return parsed, nil
}

func parseExaExtrasOption(raw any, fieldLabel string) (map[string]any, error) {
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, err
	}
	parsed := map[string]any{}
	if value, exists := typed["links"]; exists && value != nil {
		links := asInt(value, -1)
		if links < 0 {
			return nil, fmt.Errorf("%s.links must be a non-negative integer", fieldLabel)
		}
		parsed["links"] = links
	}
	if value, exists := typed["image_links"]; exists && value != nil {
		imageLinks := asInt(value, -1)
		if imageLinks < 0 {
			return nil, fmt.Errorf("%s.image_links must be a non-negative integer", fieldLabel)
		}
		parsed["image_links"] = imageLinks
	}
	return parsed, nil
}

func parseOptionalObjectArg(raw any, fieldLabel string) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	typed, err := parseJSONObjectInput(raw, fieldLabel)
	if err != nil {
		return nil, err
	}
	if len(typed) == 0 {
		return nil, nil
	}
	return typed, nil
}

func parseJSONObjectInput(raw any, fieldLabel string) (map[string]any, error) {
	switch typed := raw.(type) {
	case map[string]any:
		return typed, nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, fmt.Errorf("%s must be an object or JSON object string", fieldLabel)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("%s must be an object or JSON object string", fieldLabel)
		}
		if parsed == nil {
			return nil, fmt.Errorf("%s must be an object or JSON object string", fieldLabel)
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("%s must be an object or JSON object string", fieldLabel)
	}
}

func (o *exaContentsRequestOptions) OutputMap() map[string]any {
	if o == nil {
		return nil
	}
	payload := map[string]any{}
	if o.Text != nil {
		payload["text"] = o.Text
	}
	if o.Highlights != nil {
		payload["highlights"] = o.Highlights
	}
	if len(o.Summary) > 0 {
		payload["summary"] = o.Summary
	}
	if o.Subpages > 0 {
		payload["subpages"] = o.Subpages
	}
	if o.SubpageTarget != nil {
		payload["subpage_target"] = o.SubpageTarget
	}
	if len(o.Extras) > 0 {
		payload["extras"] = o.Extras
	}
	if o.MaxAgeHours != nil {
		payload["max_age_hours"] = *o.MaxAgeHours
	}
	if o.LivecrawlTimeoutMS > 0 {
		payload["livecrawl_timeout_ms"] = o.LivecrawlTimeoutMS
	}
	if strings.TrimSpace(o.Livecrawl) != "" {
		payload["livecrawl"] = strings.TrimSpace(o.Livecrawl)
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func (o *exaContentsRequestOptions) ApplyExaPayload(payload map[string]any) {
	if o == nil || payload == nil {
		return
	}
	if o.Text != nil {
		payload["text"] = exaTextOptionToRequest(o.Text)
	}
	if o.Highlights != nil {
		payload["highlights"] = exaHighlightsOptionToRequest(o.Highlights)
	}
	if len(o.Summary) > 0 {
		payload["summary"] = o.Summary
	}
	if o.Subpages > 0 {
		payload["subpages"] = o.Subpages
	}
	if o.SubpageTarget != nil {
		payload["subpageTarget"] = o.SubpageTarget
	}
	if len(o.Extras) > 0 {
		payload["extras"] = exaExtrasOptionToRequest(o.Extras)
	}
	if o.MaxAgeHours != nil {
		payload["maxAgeHours"] = *o.MaxAgeHours
	}
	if o.LivecrawlTimeoutMS > 0 {
		payload["livecrawlTimeout"] = o.LivecrawlTimeoutMS
	}
	if strings.TrimSpace(o.Livecrawl) != "" {
		payload["livecrawl"] = strings.TrimSpace(o.Livecrawl)
	}
}

func exaTextOptionToRequest(raw any) any {
	typed, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	out := map[string]any{}
	if value, exists := typed["max_characters"]; exists {
		out["maxCharacters"] = value
	}
	if value, exists := typed["include_html_tags"]; exists {
		out["includeHtmlTags"] = value
	}
	if value, exists := typed["verbosity"]; exists {
		out["verbosity"] = value
	}
	if value, exists := typed["include_sections"]; exists {
		out["includeSections"] = value
	}
	if value, exists := typed["exclude_sections"]; exists {
		out["excludeSections"] = value
	}
	if len(out) == 0 {
		return true
	}
	return out
}

func exaHighlightsOptionToRequest(raw any) any {
	typed, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	out := map[string]any{}
	if value, exists := typed["max_characters"]; exists {
		out["maxCharacters"] = value
	}
	if value, exists := typed["num_sentences"]; exists {
		out["numSentences"] = value
	}
	if value, exists := typed["highlights_per_url"]; exists {
		out["highlightsPerUrl"] = value
	}
	if value, exists := typed["query"]; exists {
		out["query"] = value
	}
	if len(out) == 0 {
		return true
	}
	return out
}

func exaExtrasOptionToRequest(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if value, exists := raw["links"]; exists {
		out["links"] = value
	}
	if value, exists := raw["image_links"]; exists {
		out["imageLinks"] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertSearchHitsToExaResults(hits []webSearchHit) []exaSearchResult {
	if len(hits) == 0 {
		return nil
	}
	out := make([]exaSearchResult, 0, len(hits))
	for _, hit := range hits {
		out = append(out, exaSearchResult{
			ID:            strings.TrimSpace(hit.ID),
			URL:           strings.TrimSpace(hit.URL),
			Title:         strings.TrimSpace(hit.Title),
			PublishedDate: strings.TrimSpace(hit.PublishedDate),
			Author:        strings.TrimSpace(hit.Author),
			Score:         hit.Score,
		})
	}
	return out
}

func convertExaSearchResults(results []exaSearchResult) []webSearchHit {
	if len(results) == 0 {
		return nil
	}
	out := make([]webSearchHit, 0, len(results))
	for _, item := range results {
		hit := webSearchHit{
			ID:              strings.TrimSpace(item.ID),
			URL:             strings.TrimSpace(item.URL),
			Title:           strings.TrimSpace(sanitizeForToolOutput(item.Title)),
			PublishedDate:   strings.TrimSpace(item.PublishedDate),
			Author:          strings.TrimSpace(item.Author),
			Score:           item.Score,
			Summary:         strings.TrimSpace(sanitizeForToolOutput(item.Summary)),
			Text:            strings.TrimSpace(sanitizeForToolOutput(item.Text)),
			Highlights:      sanitizeStringSlice(item.Highlights),
			HighlightScores: cloneFloat64Slice(item.HighlightScores),
			Image:           strings.TrimSpace(item.Image),
			Favicon:         strings.TrimSpace(item.Favicon),
		}
		if len(item.Subpages) > 0 {
			hit.Subpages = convertExaSearchResults(item.Subpages)
		}
		if len(item.Extras) > 0 {
			hit.Extras = item.Extras
		}
		if hit.URL == "" {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func mapExaContentResult(item exaContentResult) map[string]any {
	record := map[string]any{
		"id":             strings.TrimSpace(item.ID),
		"url":            strings.TrimSpace(item.URL),
		"title":          strings.TrimSpace(sanitizeForToolOutput(item.Title)),
		"published_date": strings.TrimSpace(item.PublishedDate),
		"author":         strings.TrimSpace(item.Author),
	}
	if text := strings.TrimSpace(sanitizeForToolOutput(item.Text)); text != "" {
		record["text"] = text
	}
	if summary := strings.TrimSpace(sanitizeForToolOutput(item.Summary)); summary != "" {
		record["summary"] = summary
	}
	if highlights := sanitizeStringSlice(item.Highlights); len(highlights) > 0 {
		record["highlights"] = highlights
	}
	if len(item.HighlightScores) > 0 {
		record["highlight_scores"] = cloneFloat64Slice(item.HighlightScores)
	}
	if image := strings.TrimSpace(item.Image); image != "" {
		record["image"] = image
	}
	if favicon := strings.TrimSpace(item.Favicon); favicon != "" {
		record["favicon"] = favicon
	}
	if len(item.Subpages) > 0 {
		record["subpages"] = convertExaSearchResults(item.Subpages)
	}
	if len(item.Extras) > 0 {
		record["extras"] = item.Extras
	}
	return record
}

func appendExaContentSafety(builder *strings.Builder, record map[string]any) {
	if builder == nil || record == nil {
		return
	}
	for _, key := range []string{"summary", "text"} {
		value := strings.TrimSpace(asString(record[key]))
		if value == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(value)
	}
	if highlights, ok := record["highlights"].([]string); ok {
		for _, highlight := range highlights {
			highlight = strings.TrimSpace(highlight)
			if highlight == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(highlight)
		}
	}
}

func sanitizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(sanitizeForToolOutput(value))
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneFloat64Slice(values []float64) []float64 {
	if len(values) == 0 {
		return nil
	}
	out := make([]float64, len(values))
	copy(out, values)
	return out
}

func firstQueryOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func appendUniqueCaseInsensitive(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), next) {
			return values
		}
	}
	return append(values, next)
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			out = append(out, entry)
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			str := strings.TrimSpace(asString(entry))
			if str == "" {
				continue
			}
			out = append(out, str)
		}
		return out
	default:
		return nil
	}
}

func normalizeManageTodoOwnerKind(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	normalized, ok := pebblestore.ParseWorkspaceTodoOwnerKind(trimmed)
	if !ok {
		return "", fmt.Errorf("owner_kind must be user or agent")
	}
	return normalized, nil
}

func manageTodoListScope(ownerKind, sessionID string) todoruntime.ListOptions {
	ownerKind = strings.TrimSpace(ownerKind)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return todoruntime.ListOptions{OwnerKind: ownerKind}
	}
	if ownerKind == "" || ownerKind == pebblestore.WorkspaceTodoOwnerKindAgent {
		return todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: sessionID}
	}
	return todoruntime.ListOptions{OwnerKind: ownerKind}
}

func parseManageTodoBatchOperations(value any, defaultOwnerKind, defaultSessionID string) ([]todoruntime.BatchOperation, error) {
	rawOps, ok := value.([]any)
	if !ok || len(rawOps) == 0 {
		return nil, errors.New("operations is required")
	}
	operations := make([]todoruntime.BatchOperation, 0, len(rawOps))
	for idx, raw := range rawOps {
		payload, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("operation %d must be an object", idx)
		}
		action := strings.ToLower(strings.TrimSpace(asString(payload["action"])))
		if action == "" {
			return nil, fmt.Errorf("operation %d action is required", idx)
		}
		ownerKindRaw := asString(payload["owner_kind"])
		if strings.TrimSpace(ownerKindRaw) == "" {
			ownerKindRaw = defaultOwnerKind
		}
		ownerKind, err := normalizeManageTodoOwnerKind(ownerKindRaw)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", idx, err)
		}
		op := todoruntime.BatchOperation{
			Action:     action,
			ID:         strings.TrimSpace(asString(payload["id"])),
			OwnerKind:  ownerKind,
			Tags:       asStringSlice(payload["tags"]),
			OrderedIDs: asStringSlice(payload["ordered_ids"]),
		}
		if rawText, ok := payload["text"]; ok {
			value := strings.TrimSpace(asString(rawText))
			op.Text = &value
		}
		if rawDone, ok := payload["done"]; ok {
			value := asBool(rawDone)
			op.Done = &value
		}
		if rawPriority, ok := payload["priority"]; ok {
			value := asString(rawPriority)
			op.Priority = &value
		}
		if rawGroup, ok := payload["group"]; ok {
			value := asString(rawGroup)
			op.Group = &value
		}
		if rawInProgress, ok := payload["in_progress"]; ok {
			value := asBool(rawInProgress)
			op.InProgress = &value
		}
		if rawSessionID, ok := payload["session_id"]; ok {
			value := strings.TrimSpace(asString(rawSessionID))
			op.SessionID = &value
		}
		if rawParentID, ok := payload["parent_id"]; ok {
			value := strings.TrimSpace(asString(rawParentID))
			op.ParentID = &value
		}
		if ownerKind == pebblestore.WorkspaceTodoOwnerKindAgent && action == "create" && (op.SessionID == nil || strings.TrimSpace(*op.SessionID) == "") {
			value := strings.TrimSpace(defaultSessionID)
			op.SessionID = &value
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func resolveWebTimeout(raw any, defaultTimeout, maxTimeout time.Duration) time.Duration {
	timeout := time.Duration(asInt(raw, int(defaultTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

func boolArgDefault(raw any, defaultValue bool) bool {
	if raw == nil {
		return defaultValue
	}
	return asBool(raw)
}

type agenticSearchCandidate struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Score        int    `json:"score"`
}

type agenticSearchLineContext struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type agenticSearchMatch struct {
	Query  string                     `json:"query,omitempty"`
	Path   string                     `json:"path"`
	Line   int                        `json:"line"`
	Column int                        `json:"column"`
	Text   string                     `json:"text"`
	Before []agenticSearchLineContext `json:"before,omitempty"`
	After  []agenticSearchLineContext `json:"after,omitempty"`
}

type agenticSearchFileContext struct {
	Query          string   `json:"query,omitempty"`
	Path           string   `json:"path"`
	RelativePath   string   `json:"relative_path"`
	Score          int      `json:"score"`
	MatchCount     int      `json:"match_count"`
	FirstMatchLine int      `json:"first_match_line,omitempty"`
	LastMatchLine  int      `json:"last_match_line,omitempty"`
	SampleMatches  []string `json:"sample_matches,omitempty"`
}

type agenticSearchReadSuggestion struct {
	Query     string `json:"query,omitempty"`
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	MaxLines  int    `json:"max_lines"`
	Reason    string `json:"reason"`
}

type agenticSearchQueryResult struct {
	Query               string                        `json:"query"`
	MatchModeRequested  string                        `json:"match_mode_requested"`
	MatchModeUsed       string                        `json:"match_mode_used"`
	MaxFiles            int                           `json:"max_files"`
	MaxResults          int                           `json:"max_results"`
	ContextBefore       int                           `json:"context_before"`
	ContextAfter        int                           `json:"context_after"`
	RankedCandidates    int                           `json:"ranked_candidates"`
	Files               []agenticSearchCandidate      `json:"files"`
	Matches             []agenticSearchMatch          `json:"matches"`
	Count               int                           `json:"count"`
	Truncated           bool                          `json:"truncated"`
	DetailsTruncated    bool                          `json:"details_truncated"`
	BudgetLimited       bool                          `json:"budget_limited,omitempty"`
	FileContexts        []agenticSearchFileContext    `json:"file_contexts,omitempty"`
	ReadSuggestions     []agenticSearchReadSuggestion `json:"read_suggestions,omitempty"`
	FileErrors          []string                      `json:"file_errors,omitempty"`
	FileErrorsTruncated bool                          `json:"file_errors_truncated,omitempty"`
	Summary             string                        `json:"summary"`
}

func executeAgenticSearch(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	return "", errors.New("agentic_search is removed; use the canonical search tool")
}

func executeAgenticSearchQuery(ctx context.Context, rgPath, searchRoot, query, matchMode string, maxFiles, maxResults int, caseSensitive bool, contextBefore, contextAfter int) agenticSearchQueryResult {
	return agenticSearchQueryResult{}
}

func runAgenticSearchMatches(ctx context.Context, rgPath, searchRoot, query string, tokens []string, requestedMode string, maxResults int, caseSensitive bool, contextBefore, contextAfter int) ([]agenticSearchMatch, string, bool, []string) {
	return nil, "none", false, []string{"agentic_search is removed; use search"}
}

func runRipgrepAgenticSearchMatchesForMode(ctx context.Context, rgPath, searchRoot, query string, tokens []string, mode string, maxResults int, caseSensitive bool) ([]agenticSearchMatch, bool, []string, error) {
	return nil, false, nil, errors.New("agentic_search is removed; use search")
}

func appendAgenticSearchLineContexts(matches []agenticSearchMatch, contextBefore, contextAfter int) ([]agenticSearchMatch, []string) {
	return matches, nil
}

func parseRipgrepAgenticSearchErrors(stderrText string) []string {
	return nil
}

func buildAgenticSearchCandidatesFromMatches(searchRoot string, matches []agenticSearchMatch, query string, tokens []string, maxFiles int) []agenticSearchCandidate {
	return nil
}

func scoreAgenticSearchPath(relPath, queryLower string, tokens []string) int {
	return 0
}

func tokenizeAgenticSearchQuery(query string, caseSensitive bool) []string {
	return nil
}

func readAgenticSearchLines(path string) ([]string, bool, error) {
	return nil, false, errors.New("agentic_search is removed; use search")
}

func buildAgenticSearchLineContext(lines []string, start, end int) []agenticSearchLineContext {
	return nil
}

func buildAgenticSearchFileContexts(searchRoot, query string, candidates []agenticSearchCandidate, matches []agenticSearchMatch, limit int) []agenticSearchFileContext {
	return nil
}

func buildAgenticSearchReadSuggestions(query string, fileContexts []agenticSearchFileContext, limit int, contextBefore, contextAfter int) []agenticSearchReadSuggestion {
	return nil
}

func parseAgenticSearchQueries(args map[string]any) ([]string, error) {
	return nil, errors.New("agentic_search is removed; use search")
}

func truncateAgenticSearchLine(value string, maxChars int) string {
	return value
}

type listEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Depth int    `json:"depth,omitempty"`
}

func executeList(scope WorkspaceScope, args map[string]any) (string, error) {
	searchRoot, err := resolveSearchRoot(scope, args["path"])
	if err != nil {
		return "", err
	}

	mode := strings.ToLower(strings.TrimSpace(asString(args["mode"])))
	if mode == "" {
		mode = "flat"
	}
	if mode != "flat" && mode != "tree" {
		return "", errors.New("list mode must be \"flat\" or \"tree\"")
	}

	maxEntries := clampInt(asInt(args["max_entries"], defaultListEntries), 1, maxListEntries)
	maxDepth := clampInt(asInt(args["max_depth"], defaultListDepth), 0, maxListDepth)
	cursor := asInt(args["cursor"], 0)
	if cursor < 0 {
		cursor = 0
	}

	entries, scanLimited, err := collectListEntries(searchRoot, mode, maxDepth)
	if err != nil {
		return "", err
	}

	totalFound := len(entries)
	if cursor > totalFound {
		cursor = totalFound
	}
	end := cursor + maxEntries
	if end > totalFound {
		end = totalFound
	}
	window := entries[cursor:end]
	truncated := end < totalFound || scanLimited

	response := map[string]any{
		"path":              searchRoot,
		"mode":              mode,
		"cursor":            cursor,
		"max_entries":       maxEntries,
		"max_depth":         maxDepth,
		"count":             len(window),
		"total_found":       totalFound,
		"entries":           window,
		"truncated":         truncated,
		"scan_limited":      scanLimited,
		"path_id":           toolPathID("list"),
		"summary":           listSummary(searchRoot, mode, len(window), totalFound, truncated, scanLimited),
		"details_truncated": truncated,
	}
	if end < totalFound {
		response["next_cursor"] = end
	}

	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
}

type editOperation struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

func executeEdit(scope WorkspaceScope, args map[string]any) (string, error) {
	targetPath, err := resolveWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	operations, err := parseEditOperations(args)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return "", fmt.Errorf("edit read failed: %w", err)
	}
	if len(data) > maxEditBytes {
		return "", fmt.Errorf("edit rejected: file exceeds %d bytes", maxEditBytes)
	}
	if isLikelyBinary(data) {
		return "", errors.New("edit rejected: binary file content")
	}

	before := string(data)
	after := before
	editResults := make([]map[string]any, 0, len(operations))
	totalMatches := 0
	totalReplacements := 0
	anyReplaceAll := false
	detailsTruncated := false
	for i, operation := range operations {
		matches := strings.Count(after, operation.OldString)
		if matches == 0 {
			if len(operations) == 1 {
				return "", errors.New("edit failed: old_string not found")
			}
			return "", fmt.Errorf("edit failed: edits[%d].old_string not found", i)
		}
		if !operation.ReplaceAll && matches > 1 {
			if len(operations) == 1 {
				return "", fmt.Errorf("edit failed: old_string matched %d times; set replace_all=true", matches)
			}
			return "", fmt.Errorf("edit failed: edits[%d].old_string matched %d times; set replace_all=true", i, matches)
		}

		replacements := 1
		if operation.ReplaceAll {
			replacements = matches
			after = strings.ReplaceAll(after, operation.OldString, operation.NewString)
		} else {
			after = strings.Replace(after, operation.OldString, operation.NewString, 1)
		}
		oldPreview, oldTruncated := sanitizeEditPreview(operation.OldString, 0)
		newPreview, newTruncated := sanitizeEditPreview(operation.NewString, 0)
		editResults = append(editResults, map[string]any{
			"index":                i + 1,
			"matches":              matches,
			"replacements":         replacements,
			"replace_all":          operation.ReplaceAll,
			"old_string_preview":   oldPreview,
			"new_string_preview":   newPreview,
			"old_string_truncated": oldTruncated,
			"new_string_truncated": newTruncated,
		})
		totalMatches += matches
		totalReplacements += replacements
		anyReplaceAll = anyReplaceAll || operation.ReplaceAll
		detailsTruncated = detailsTruncated || oldTruncated || newTruncated
	}

	if err := os.WriteFile(targetPath, []byte(after), 0o644); err != nil {
		return "", fmt.Errorf("edit write failed: %w", err)
	}

	response := map[string]any{
		"path":              targetPath,
		"matches":           totalMatches,
		"replacements":      totalReplacements,
		"replace_all":       anyReplaceAll,
		"changed":           before != after,
		"bytes_before":      len(before),
		"bytes_after":       len(after),
		"path_id":           toolPathID("edit"),
		"summary":           editSummary(targetPath, totalReplacements, len(operations), anyReplaceAll),
		"details_truncated": detailsTruncated,
	}
	if len(editResults) == 1 {
		response["old_string_preview"] = editResults[0]["old_string_preview"]
		response["new_string_preview"] = editResults[0]["new_string_preview"]
		response["old_string_truncated"] = editResults[0]["old_string_truncated"]
		response["new_string_truncated"] = editResults[0]["new_string_truncated"]
	} else {
		response["edit_count"] = len(editResults)
		response["edits"] = editResults
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
}

func parseEditOperations(args map[string]any) ([]editOperation, error) {
	if rawEdits, ok := args["edits"]; ok {
		rawItems, ok := rawEdits.([]any)
		if !ok {
			return nil, errors.New("edit edits must be an array")
		}
		if len(rawItems) == 0 {
			return nil, errors.New("edit edits must not be empty")
		}
		defaultReplaceAll := asBool(args["replace_all"])
		operations := make([]editOperation, 0, len(rawItems))
		for i, rawItem := range rawItems {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("edit edits[%d] must be an object", i)
			}
			if _, ok := item["old_string"]; !ok {
				return nil, fmt.Errorf("edit edits[%d] requires old_string", i)
			}
			if _, ok := item["new_string"]; !ok {
				return nil, fmt.Errorf("edit edits[%d] requires new_string", i)
			}
			oldString := asString(item["old_string"])
			if oldString == "" {
				return nil, fmt.Errorf("edit edits[%d].old_string must not be empty", i)
			}
			replaceAll := defaultReplaceAll
			if _, ok := item["replace_all"]; ok {
				replaceAll = asBool(item["replace_all"])
			}
			operations = append(operations, editOperation{
				OldString:  oldString,
				NewString:  asString(item["new_string"]),
				ReplaceAll: replaceAll,
			})
		}
		return operations, nil
	}
	if _, ok := args["old_string"]; !ok {
		return nil, errors.New("edit requires old_string or edits")
	}
	if _, ok := args["new_string"]; !ok {
		return nil, errors.New("edit requires new_string or edits")
	}
	oldString := asString(args["old_string"])
	if oldString == "" {
		return nil, errors.New("edit old_string must not be empty")
	}
	return []editOperation{{
		OldString:  oldString,
		NewString:  asString(args["new_string"]),
		ReplaceAll: asBool(args["replace_all"]),
	}}, nil
}

func executeStubTool(rawName string, args map[string]any) (string, error) {
	name := canonicalStubToolName(rawName)
	reason := "tool requires control-plane wiring that is not active in this runtime"
	nextAction := "Use core runtime tools (read/write/bash/search/list/edit) until this tool is enabled."
	switch name {
	case "manage_todos":
		reason = "manage_todos is handled by the API/control-plane and direct todo service endpoints, not standalone runtime"
		nextAction = "Use manage_todos through the shared run pipeline or the workspace todo HTTP APIs."
	case "exit_plan_mode":
		nextAction = "Use /plan exit in the TUI to submit the plan for approval and leave plan mode."
	case "plan_manage":
		reason = "plan_manage is handled by run-service control-plane, not standalone runtime"
		nextAction = "Use plan_manage through the shared run pipeline or session plan APIs."
	}
	summary := fmt.Sprintf("%s is not active in this session", strings.ReplaceAll(name, "_", "-"))
	response := map[string]any{
		"tool":              name,
		"enabled":           false,
		"status":            "not_available",
		"reason":            reason,
		"next_action":       nextAction,
		"arguments_present": len(args) > 0,
		"path_id":           stubToolPathID(name),
		"summary":           summary,
		"details_truncated": false,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func executeSkillUse(scope WorkspaceScope, args map[string]any) (string, error) {
	skill := strings.TrimSpace(asString(args["skill"]))
	if skill == "" {
		skill = strings.TrimSpace(asString(args["name"]))
	}
	if skill == "" {
		return "", errors.New("skill-use requires skill")
	}

	scanner := discovery.NewService()
	report, err := scanner.ScanScope(scope.PrimaryPath, scope.Roots)
	if err != nil {
		return "", fmt.Errorf("skill-use scan failed: %w", err)
	}

	target := normalizeSkillLookup(skill)
	matched, ok := matchSkillSource(report.Skills, target)
	if !ok {
		available := summarizeAvailableSkills(report.Skills, maxSkillListPreview)
		truncated := len(report.Skills) > len(available)
		response := map[string]any{
			"skill":               skill,
			"status":              "not_found",
			"available_skills":    available,
			"invalid_skills":      report.InvalidSkills,
			"path_id":             toolPathID("skill-use"),
			"summary":             fmt.Sprintf("skill %q not found", skill),
			"details_truncated":   truncated,
			"suggested_next_step": "Use /skills to inspect discovered skills, then retry skill-use with a canonical name.",
		}
		encoded, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(encoded), nil
	}

	raw, err := os.ReadFile(matched.Path)
	if err != nil {
		return "", fmt.Errorf("skill-use read failed: %w", err)
	}
	truncated := false
	if len(raw) > maxSkillContentBytes {
		raw = raw[:maxSkillContentBytes]
		truncated = true
	}
	content := strings.TrimSpace(sanitizeForToolOutput(string(raw)))

	response := map[string]any{
		"skill": map[string]any{
			"name":           matched.Name,
			"canonical_name": matched.CanonicalName,
			"description":    matched.Description,
			"path":           matched.Path,
			"scope":          matched.Scope,
			"origin":         matched.Origin,
			"metadata":       matched.Metadata,
		},
		"status":               "activated",
		"content":              content,
		"truncated":            truncated,
		"path_id":              toolPathID("skill-use"),
		"summary":              fmt.Sprintf("skill %s loaded", matched.CanonicalName),
		"details_truncated":    truncated,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(content),
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
}

func executeManageSkill(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "inspect"
	}
	confirm := asBool(args["confirm"])
	switch action {
	case "inspect", "list":
		return manageSkillInspect(scope)
	case "get", "read":
		return manageSkillGet(scope, args)
	case "create":
		return manageSkillChange(scope, args, false, confirm)
	case "update":
		return manageSkillChange(scope, args, true, confirm)
	case "delete", "remove":
		if confirm {
			return manageSkillDelete(scope, args)
		}
		return manageSkillProposeDelete(scope, args)
	default:
		return "", fmt.Errorf("manage-skill action %q is unsupported", action)
	}
}

func (r *Runtime) executeManageAgent(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "inspect"
	}
	confirm := asBool(args["confirm"])
	switch action {
	case "inspect", "list":
		return r.manageAgentInspect(scope)
	case "get", "read":
		return r.manageAgentGet(args)
	case "create":
		return r.manageAgentUpsert(args, false, confirm)
	case "update":
		return r.manageAgentUpsert(args, true, confirm)
	case "delete", "remove":
		return r.manageAgentDelete(args, confirm)
	case "create_custom_tool", "create-custom-tool":
		return r.manageAgentCustomToolUpsert(args, false, confirm)
	case "update_custom_tool", "update-custom-tool":
		return r.manageAgentCustomToolUpsert(args, true, confirm)
	case "delete_custom_tool", "delete-custom-tool", "remove_custom_tool", "remove-custom-tool":
		return r.manageAgentDeleteCustomTool(args, confirm)
	case "assign_custom_tool", "assign-custom-tool":
		return r.manageAgentAssignCustomTool(args, confirm)
	case "unassign_custom_tool", "unassign-custom-tool":
		return r.manageAgentUnassignCustomTool(args, confirm)
	case "activate_primary", "activate-primary":
		return r.manageAgentActivatePrimary(args, confirm)
	case "set_active_subagent", "set-active-subagent":
		return r.manageAgentSetActiveSubagent(args, confirm)
	case "remove_active_subagent", "remove-active-subagent", "delete_active_subagent", "delete-active-subagent":
		return r.manageAgentRemoveActiveSubagent(args, confirm)
	default:
		return "", fmt.Errorf("manage-agent action %q is unsupported", action)
	}
}

func (r *Runtime) executeManageTheme(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "inspect"
	}
	confirm := asBool(args["confirm"])
	switch action {
	case "inspect", "list":
		return r.manageThemeInspect(scope, args)
	case "get", "read":
		return r.manageThemeGet(scope, args)
	case "create":
		return r.manageThemeUpsert(scope, args, false, confirm)
	case "update":
		return r.manageThemeUpsert(scope, args, true, confirm)
	case "delete", "remove":
		return r.manageThemeDelete(scope, args, confirm)
	case "set", "use":
		return r.manageThemeSet(scope, args, confirm)
	default:
		return "", fmt.Errorf("manage-theme action %q is unsupported", action)
	}
}

func (r *Runtime) executeManageWorktree(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "inspect"
	}
	switch action {
	case "inspect", "list":
		return r.manageWorktreeInspect(scope, args)
	default:
		return "", fmt.Errorf("manage-worktree action %q is unsupported", action)
	}
}

func (r *Runtime) executeManageTodos(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.todos == nil {
		return executeStubTool("manage_todos", args)
	}
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "list"
	}
	requestedWorkspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if requestedWorkspacePath == "" {
		requestedWorkspacePath = "."
	}
	workspacePath, err := resolveWorkspacePath(scope, requestedWorkspacePath)
	if err != nil {
		return "", err
	}

	ownerKind, err := normalizeManageTodoOwnerKind(asString(args["owner_kind"]))
	if err != nil {
		return "", err
	}

	response := map[string]any{
		"tool":              "manage_todos",
		"status":            "ok",
		"action":            action,
		"workspace_path":    workspacePath,
		"owner_kind":        ownerKind,
		"path_id":           toolPathID("manage_todos"),
		"details_truncated": false,
	}
	if strings.TrimSpace(scope.SessionID) != "" {
		response["session_id"] = strings.TrimSpace(scope.SessionID)
	}

	switch action {
	case "list":
		listOptions := manageTodoListScope(ownerKind, scope.SessionID)
		items, summary, err := r.todos.List(workspacePath, listOptions)
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "summary":
		listOptions := manageTodoListScope(ownerKind, scope.SessionID)
		_, summary, err := r.todos.List(workspacePath, listOptions)
		if err != nil {
			return "", err
		}
		response["summary"] = summary
	case "create":
		text := strings.TrimSpace(asString(args["text"]))
		if text == "" {
			return "", errors.New("text is required")
		}
		sessionID := strings.TrimSpace(asString(args["session_id"]))
		if ownerKind == pebblestore.WorkspaceTodoOwnerKindAgent && sessionID == "" {
			sessionID = strings.TrimSpace(scope.SessionID)
		}
		item, summary, _, err := r.todos.Create(todoruntime.CreateInput{
			WorkspacePath: workspacePath,
			OwnerKind:     ownerKind,
			Text:          text,
			Priority:      asString(args["priority"]),
			Group:         asString(args["group"]),
			Tags:          asStringSlice(args["tags"]),
			InProgress:    asBool(args["in_progress"]),
			SessionID:     sessionID,
			ParentID:      asString(args["parent_id"]),
		})
		if err != nil {
			return "", err
		}
		response["item"] = item
		response["summary"] = summary
	case "update":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required")
		}
		var text *string
		if raw, ok := args["text"]; ok {
			value := strings.TrimSpace(asString(raw))
			text = &value
		}
		var done *bool
		if raw, ok := args["done"]; ok {
			value := asBool(raw)
			done = &value
		}
		var priority *string
		if raw, ok := args["priority"]; ok {
			value := asString(raw)
			priority = &value
		}
		var group *string
		if raw, ok := args["group"]; ok {
			value := asString(raw)
			group = &value
		}
		var tags []string
		if raw, ok := args["tags"]; ok {
			tags = asStringSlice(raw)
		}
		var inProgress *bool
		if raw, ok := args["in_progress"]; ok {
			value := asBool(raw)
			inProgress = &value
		}
		var sessionID *string
		if raw, ok := args["session_id"]; ok {
			value := strings.TrimSpace(asString(raw))
			sessionID = &value
		}
		var parentID *string
		if raw, ok := args["parent_id"]; ok {
			value := strings.TrimSpace(asString(raw))
			parentID = &value
		}
		updateSessionID := ""
		if sessionID != nil {
			updateSessionID = strings.TrimSpace(*sessionID)
		} else if ownerKind == pebblestore.WorkspaceTodoOwnerKindAgent {
			updateSessionID = strings.TrimSpace(scope.SessionID)
		}
		item, summary, _, err := r.todos.Update(todoruntime.UpdateInput{
			WorkspacePath: workspacePath,
			ID:            id,
			Text:          text,
			Done:          done,
			Priority:      priority,
			Group:         group,
			Tags:          tags,
			InProgress:    inProgress,
			SessionID:     sessionID,
			ParentID:      parentID,
		}, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: updateSessionID})
		if err != nil {
			return "", err
		}
		response["item"] = item
		response["summary"] = summary
	case "delete":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required")
		}
		summary, _, err := r.todos.Delete(workspacePath, id, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["id"] = id
		response["summary"] = summary
	case "delete_done":
		items, summary, _, err := r.todos.DeleteDone(workspacePath, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "delete_all":
		items, summary, _, err := r.todos.DeleteAll(workspacePath, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "reorder":
		orderedIDs := asStringSlice(args["ordered_ids"])
		if len(orderedIDs) == 0 {
			return "", errors.New("ordered_ids is required")
		}
		items, summary, _, err := r.todos.Reorder(todoruntime.ReorderInput{WorkspacePath: workspacePath, OwnerKind: ownerKind, OrderedIDs: orderedIDs}, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "in_progress":
		id := strings.TrimSpace(asString(args["id"]))
		if id == "" {
			return "", errors.New("id is required")
		}
		item, summary, _, err := r.todos.SetInProgress(workspacePath, id, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["item"] = item
		response["summary"] = summary
	case "batch":
		operations, err := parseManageTodoBatchOperations(args["operations"], ownerKind, scope.SessionID)
		if err != nil {
			return "", err
		}
		results, items, summary, _, err := r.todos.ApplyBatch(workspacePath, operations, todoruntime.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["results"] = results
		response["items"] = items
		response["summary"] = summary
		response["operation_count"] = len(operations)
	default:
		return "", fmt.Errorf("unsupported todo action %q", action)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageWorktreeInspect(scope WorkspaceScope, args map[string]any) (string, error) {
	workspacePath, err := r.manageWorktreeResolveWorkspacePath(scope, strings.TrimSpace(asString(args["workspace_path"])))
	if err != nil {
		return "", err
	}
	limit := asInt(args["limit"], 25)
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	cursor := asInt(args["cursor"], 0)
	if cursor < 0 {
		cursor = 0
	}

	config := manageWorktreeConfig{}
	if r != nil && r.worktrees != nil {
		cfg, cfgErr := r.worktrees.GetConfig(workspacePath)
		if cfgErr != nil {
			return "", fmt.Errorf("manage-worktree get config failed: %w", cfgErr)
		}
		config = cfg
	}

	workspaceName := filepath.Base(strings.TrimSpace(workspacePath))
	if r != nil && r.workspace != nil {
		if info, scopeErr := r.workspace.ScopeForPath(workspacePath); scopeErr == nil {
			if strings.TrimSpace(info.WorkspaceName) != "" {
				workspaceName = strings.TrimSpace(info.WorkspaceName)
			}
		}
	}

	branchPrefix := strings.TrimSpace(asString(args["branch_name"]))
	if branchPrefix == "" {
		branchPrefix = strings.TrimSpace(config.BranchName)
	}
	branchPrefix = normalizeManageWorktreeBranchPrefix(branchPrefix)

	items, total, currentBranch, err := r.manageWorktreeCommitsForWorkspace(scope, workspacePath, branchPrefix)
	if err != nil {
		return "", err
	}
	end := cursor + limit
	if end > total {
		end = total
	}
	pageItems := []map[string]any{}
	if cursor < total {
		pageItems = items[cursor:end]
	}
	nextCursor := 0
	if end < total {
		nextCursor = end
	}

	response := map[string]any{
		"status": "ok",
		"action": "inspect",
		"workspace": map[string]any{
			"path": workspacePath,
			"name": workspaceName,
		},
		"worktree_config":   r.manageWorktreeConfigMap(config),
		"branch_name":       branchPrefix,
		"current_branch":    currentBranch,
		"items":             pageItems,
		"total":             total,
		"returned":          len(pageItems),
		"cursor":            cursor,
		"limit":             limit,
		"next_cursor":       nextCursor,
		"has_more":          nextCursor > 0,
		"supported_actions": []string{"inspect", "list"},
		"instructions":      "Use this tool to inspect combined commits for the workspace worktree branch family. It defaults to the configured branch prefix, supports branch_name overrides such as agent or foo, and includes merged_into_current_branch for each returned commit when that commit or an equivalent patch is already present on the current branch.",
		"examples": []map[string]any{
			{"action": "inspect"},
			{"action": "inspect", "branch_name": branchPrefix, "limit": 25, "cursor": 0},
			{"action": "inspect", "workspace_path": workspacePath, "branch_name": branchPrefix, "limit": 25},
		},
		"path_id":              toolPathID("manage-worktree"),
		"summary":              fmt.Sprintf("returned %d of %d commits for %s/%s*", len(pageItems), total, workspaceName, branchPrefix),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func normalizeManageWorktreeBranchPrefix(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "/")
	if value == "" {
		return "agent"
	}
	if strings.HasSuffix(value, "/<id>") {
		value = strings.TrimSuffix(value, "/<id>")
		value = strings.Trim(value, "/")
	}
	if value == "" {
		return "agent"
	}
	return value
}

func manageWorktreeShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (r *Runtime) manageWorktreeCommitsForWorkspace(scope WorkspaceScope, workspacePath, branchPrefix string) ([]map[string]any, int, string, error) {
	branchPrefix = normalizeManageWorktreeBranchPrefix(branchPrefix)
	branchGlob := manageWorktreeShellQuote(branchPrefix + "/*")
	command := fmt.Sprintf(`set -euo pipefail
current_branch=$(git branch --show-current)
printf '__CURRENT_BRANCH__\t%%s\n' "$current_branch"
declare -A head_patch_ids=()
while read -r patch_id commit_id; do
	if [ -n "$patch_id" ]; then
		head_patch_ids["$patch_id"]=1
	fi
done < <(git log -p --format=%%H HEAD | git patch-id --stable || true)
declare -A branch_patch_ids=()
while read -r patch_id commit_id; do
	if [ -n "$patch_id" ] && [ -n "$commit_id" ]; then
		branch_patch_ids["$commit_id"]="$patch_id"
	fi
done < <(git log -p --format=%%H --branches=%s | git patch-id --stable || true)
git log --format=%%H%%x09%%h%%x09%%cI%%x09%%s --branches=%s | while IFS=$'\t' read -r commit short committed_at subject; do
	if git merge-base --is-ancestor "$commit" HEAD; then
		merged=true
	else
		merged=false
		patch_id="${branch_patch_ids[$commit]:-}"
		if [ -n "$patch_id" ] && [ -n "${head_patch_ids[$patch_id]:-}" ]; then
			merged=true
		fi
	fi
	printf '%%s\t%%s\t%%s\t%%s\t%%s\n' "$commit" "$short" "$committed_at" "$merged" "$subject"
done`, branchGlob, branchGlob)
	bashArgs := map[string]any{
		"command":    command,
		"timeout_ms": 20000,
	}
	output, err := executeBash(context.Background(), normalizeWorkspaceScope(workspacePath, scope.Roots), bashArgs, nil)
	if err != nil {
		return nil, 0, "", err
	}
	var payload struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil, 0, "", fmt.Errorf("manage-worktree decode git log output failed: %w", err)
	}
	if payload.ExitCode != 0 {
		message := strings.TrimSpace(payload.Output)
		if message == "" {
			message = fmt.Sprintf("git inspection failed with exit code %d", payload.ExitCode)
		}
		return nil, 0, "", fmt.Errorf("manage-worktree git inspection failed: %s", message)
	}
	text := strings.TrimRight(payload.Output, "\n")
	if text == "" {
		return []map[string]any{}, 0, "", nil
	}
	lines := strings.Split(text, "\n")
	items := make([]map[string]any, 0, len(lines))
	currentBranch := ""
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "__CURRENT_BRANCH__\t") {
			currentBranch = strings.TrimPrefix(line, "__CURRENT_BRANCH__\t")
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 {
			continue
		}
		items = append(items, map[string]any{
			"commit":                     strings.TrimSpace(parts[0]),
			"commit_short":               strings.TrimSpace(parts[1]),
			"committed_at":               strings.TrimSpace(parts[2]),
			"merged_into_current_branch": strings.EqualFold(strings.TrimSpace(parts[3]), "true"),
			"subject":                    strings.TrimSpace(parts[4]),
			"branch_name":                branchPrefix,
		})
	}
	return items, len(items), currentBranch, nil
}

func (r *Runtime) manageWorktreeResolveWorkspacePath(scope WorkspaceScope, requested string) (string, error) {
	if requested != "" {
		return resolveWorkspacePath(scope, requested)
	}
	if r != nil && r.workspace != nil {
		if current, ok, err := r.workspace.CurrentBinding(); err != nil {
			return "", fmt.Errorf("manage-worktree resolve current workspace failed: %w", err)
		} else if ok {
			if path := strings.TrimSpace(current.ResolvedPath); path != "" {
				return resolveWorkspacePath(scope, path)
			}
			if path := strings.TrimSpace(current.WorkspacePath); path != "" {
				return resolveWorkspacePath(scope, path)
			}
		}
	}
	if strings.TrimSpace(scope.PrimaryPath) != "" {
		return resolveWorkspacePath(scope, scope.PrimaryPath)
	}
	return normalizeScopePath(scope.PrimaryPath), nil
}

func (r *Runtime) manageWorktreeConfigMap(cfg manageWorktreeConfig) map[string]any {
	return map[string]any{
		"workspace_path":     strings.TrimSpace(cfg.WorkspacePath),
		"enabled":            cfg.Enabled,
		"use_current_branch": cfg.UseCurrentBranch,
		"base_branch":        strings.TrimSpace(cfg.BaseBranch),
		"branch_name":        normalizeManageWorktreeBranchPrefix(strings.TrimSpace(cfg.BranchName)),
		"updated_at":         cfg.UpdatedAt,
	}
}

func manageSkillInspect(scope WorkspaceScope) (string, error) {
	report, err := discovery.NewService().ScanScope(scope.PrimaryPath, scope.Roots)
	if err != nil {
		return "", fmt.Errorf("manage-skill inspect scan failed: %w", err)
	}
	skills := make([]map[string]any, 0, len(report.Skills))
	for _, skill := range report.Skills {
		if !manageSkillSkillPathAllowed(skill.Path, scope) {
			continue
		}
		skills = append(skills, map[string]any{
			"name":           skill.Name,
			"canonical_name": skill.CanonicalName,
			"description":    skill.Description,
			"path":           skill.Path,
			"scope":          skill.Scope,
			"origin":         skill.Origin,
			"metadata":       skill.Metadata,
			"active":         skill.Active,
		})
	}
	response := map[string]any{
		"status":               "ok",
		"action":               "inspect",
		"skill_root":           manageSkillSkillRoot(scope),
		"skills":               skills,
		"invalid_skills":       report.InvalidSkills,
		"count":                len(skills),
		"supported_actions":    []string{"inspect", "list", "get", "create", "update", "delete"},
		"instructions":         "Use manage-skill for workspace skill discovery and CRUD. Call inspect/list to discover available skills, get to read a skill, and create/update/delete for preview-first skill edits under .agents/skills.",
		"path_id":              toolPathID("manage-skill"),
		"summary":              fmt.Sprintf("found %d workspace skills", len(skills)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
		"hot_reload": map[string]any{
			"enabled": true,
			"summary": "Skills are discovered from disk and appear after refresh without a restart.",
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageAgentInspect(scope WorkspaceScope) (string, error) {
	_ = scope
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent inspect failed: %w", err)
	}
	agents := make([]map[string]any, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		agents = append(agents, manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)))
	}
	customTools := make([]map[string]any, 0, len(state.CustomTools))
	for _, definition := range state.CustomTools {
		customTools = append(customTools, manageAgentCustomToolMap(definition))
	}
	response := map[string]any{
		"status":            "ok",
		"action":            "inspect",
		"agents":            agents,
		"count":             len(agents),
		"custom_tools":      customTools,
		"custom_tool_count": len(customTools),
		"active_primary":    strings.TrimSpace(state.ActivePrimary),
		"active_subagent":   cloneStringMap(state.ActiveSubagent),
		"version":           state.Version,
		"supported_actions": []string{"inspect", "list", "get", "create", "update", "delete", "activate_primary", "set_active_subagent", "remove_active_subagent", "create_custom_tool", "update_custom_tool", "delete_custom_tool", "assign_custom_tool", "unassign_custom_tool"},
		"instructions":      "Use manage-agent to inspect and manage saved agents and custom tools. Call inspect/list first, then get before mutating an agent profile. For agent create/update, prefer object-form `content` with the desired profile fields; do not rely on flattened top-level fields. Custom tool actions use `content={name,kind,description?,command}` and assignment actions use top-level `agent` plus `tool_name`. Nested `tool_scope` supports `allow_tools`, `deny_tools`, `bash_prefixes`, `preset`, and `inherit_policy`. Mutating actions return approval-ready previews unless confirm=true.",
		"examples": []map[string]any{
			{"action": "inspect"},
			{"action": "get", "agent": strings.TrimSpace(state.ActivePrimary)},
			{"action": "create", "agent": "review-bot", "content": map[string]any{"name": "review-bot", "mode": "subagent", "description": "Code review specialist.", "prompt": "Review diffs and call out concrete risks.", "execution_setting": "read", "tool_scope": map[string]any{"allow_tools": []string{"search", "list", "read"}, "deny_tools": []string{"write", "edit", "bash"}}}},
			{"action": "create_custom_tool", "content": map[string]any{"name": "show_go_version", "kind": "fixed_bash", "description": "Show the installed Go version.", "command": "go version"}},
			{"action": "assign_custom_tool", "agent": strings.TrimSpace(state.ActivePrimary), "tool_name": "show_go_version"},
		},
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("found %d saved agents and %d custom tools", len(agents), len(customTools)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageAgentGet(args map[string]any) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent get list failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(args)
	if err != nil {
		return "", err
	}
	response := map[string]any{
		"status":               "ok",
		"action":               "get",
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("loaded agent %s", strings.TrimSpace(profile.Name)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(strings.TrimSpace(profile.Prompt)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentUpsert(args map[string]any, mustExist, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	input, err := manageAgentUpsertInputFromArgs(args)
	if err != nil {
		return "", err
	}
	if err := validateManageAgentMutationInput(input, mustExist); err != nil {
		return "", err
	}
	preview, err := r.agents.PreviewUpsert(input)
	if err != nil {
		return "", err
	}
	if mustExist && !preview.Exists {
		return "", fmt.Errorf("agent %q does not exist", preview.After.Name)
	}
	if !mustExist && preview.Exists {
		return "", fmt.Errorf("agent %q already exists; use update", preview.After.Name)
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	action := "create"
	status := "proposed_create"
	summary := fmt.Sprintf("proposed new agent %s", preview.After.Name)
	if preview.Exists {
		action = "update"
		status = "proposed_update"
		summary = fmt.Sprintf("proposed update for agent %s", preview.After.Name)
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "agent_profile",
		"operation": action,
		"before":    manageAgentOptionalProfileMap(preview.Before, state),
		"after":     manageAgentProfileMap(preview.After, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(preview.After.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, preview.After.Name)),
	}
	if confirm {
		profile, _, _, err := r.agents.Upsert(input)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(updatedState.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(updatedState.ActiveSubagent, profile.Name)),
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              strings.Replace(summary, "proposed ", "applied ", 1),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               status,
		"action":               action,
		"agent":                manageAgentProfileMap(preview.After, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(preview.After.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, preview.After.Name)),
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              summary,
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentDelete(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(args)
	if err != nil {
		return "", err
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "agent_profile",
		"operation": "delete",
		"before":    manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"after":     nil,
	}
	if confirm {
		result, _, _, err := r.agents.Delete(profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "delete",
			"applied":              true,
			"agent":                manageAgentProfileMap(profile, false, nil),
			"deleted":              strings.TrimSpace(result.Deleted),
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              fmt.Sprintf("applied delete for agent %s", profile.Name),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_delete",
		"action":               "delete",
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("proposed delete for agent %s", profile.Name),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentActivatePrimary(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(args)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModePrimary) {
		return "", fmt.Errorf("agent %q is not a primary agent", profile.Name)
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "active_primary",
		"operation": "activate_primary",
		"before":    strings.TrimSpace(state.ActivePrimary),
		"after":     strings.TrimSpace(profile.Name),
	}
	if confirm {
		active, _, _, err := r.agents.ActivatePrimary(profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "activate_primary",
			"applied":              true,
			"agent":                manageAgentProfileMap(profile, true, manageAgentPurposesForProfile(updatedState.ActiveSubagent, profile.Name)),
			"change":               change,
			"active_primary":       strings.TrimSpace(active),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              fmt.Sprintf("applied active primary: %s", active),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_activate_primary",
		"action":               "activate_primary",
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("proposed active primary: %s", profile.Name),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentSetActiveSubagent(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	purpose, name, err := manageAgentAssignmentArgs(args)
	if err != nil {
		return "", err
	}
	profile, ok, err := r.agents.GetProfile(name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("agent %q not found", name)
	}
	if !strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModeSubagent) {
		return "", fmt.Errorf("agent %q is not a subagent", name)
	}
	beforeAssignments := cloneStringMap(state.ActiveSubagent)
	afterAssignments := cloneStringMap(state.ActiveSubagent)
	afterAssignments[purpose] = strings.TrimSpace(profile.Name)
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "active_subagent",
		"operation": "set_active_subagent",
		"purpose":   purpose,
		"before":    beforeAssignments,
		"after":     afterAssignments,
	}
	if confirm {
		assignments, _, _, err := r.agents.SetActiveSubagent(purpose, profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "set_active_subagent",
			"applied":              true,
			"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(updatedState.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(updatedState.ActiveSubagent, profile.Name)),
			"purpose":              purpose,
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(assignments),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              fmt.Sprintf("applied subagent assignment: %s → %s", purpose, profile.Name),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_set_active_subagent",
		"action":               "set_active_subagent",
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"purpose":              purpose,
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      beforeAssignments,
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("proposed subagent assignment: %s → %s", purpose, profile.Name),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentRemoveActiveSubagent(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	purpose := strings.TrimSpace(manageAgentStringArg(args, "purpose"))
	if purpose == "" {
		return "", errors.New("manage-agent requires purpose for remove_active_subagent")
	}
	beforeAssignments := cloneStringMap(state.ActiveSubagent)
	afterAssignments := cloneStringMap(state.ActiveSubagent)
	delete(afterAssignments, purpose)
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "active_subagent",
		"operation": "remove_active_subagent",
		"purpose":   purpose,
		"before":    beforeAssignments,
		"after":     afterAssignments,
	}
	if confirm {
		assignments, _, _, err := r.agents.DeleteActiveSubagent(purpose)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "remove_active_subagent",
			"applied":              true,
			"purpose":              purpose,
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(assignments),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              fmt.Sprintf("applied subagent removal for %s", purpose),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_remove_active_subagent",
		"action":               "remove_active_subagent",
		"purpose":              purpose,
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      beforeAssignments,
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("proposed subagent removal for %s", purpose),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentCustomToolUpsert(args map[string]any, mustExist, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	definition, err := manageAgentCustomToolDefinitionFromArgs(args)
	if err != nil {
		return "", err
	}
	current, exists, err := r.agents.GetCustomTool(definition.Name)
	if err != nil {
		return "", err
	}
	if mustExist && !exists {
		return "", fmt.Errorf("custom tool %q does not exist", definition.Name)
	}
	if !mustExist && exists {
		return "", fmt.Errorf("custom tool %q already exists; use update", definition.Name)
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	action := "create_custom_tool"
	status := "proposed_create_custom_tool"
	summary := fmt.Sprintf("proposed new custom tool %s", definition.Name)
	var before *pebblestore.AgentCustomToolDefinition
	if exists {
		action = "update_custom_tool"
		status = "proposed_update_custom_tool"
		summary = fmt.Sprintf("proposed update for custom tool %s", definition.Name)
		before = &current
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "custom_tool",
		"operation": action,
		"before":    manageAgentOptionalCustomToolMap(before),
		"after":     manageAgentCustomToolMap(definition),
	}
	if confirm {
		stored, err := r.agents.PutCustomTool(definition)
		if err != nil {
			return "", err
		}
		change["after"] = manageAgentCustomToolMap(stored)
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"custom_tool":          manageAgentCustomToolMap(stored),
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              strings.Replace(summary, "proposed ", "applied ", 1),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               status,
		"action":               action,
		"custom_tool":          manageAgentCustomToolMap(definition),
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              summary,
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentDeleteCustomTool(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	toolName, err := manageAgentCustomToolNameFromArgs(args)
	if err != nil {
		return "", err
	}
	definition, ok, err := r.agents.GetCustomTool(toolName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("custom tool %q not found", toolName)
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "custom_tool",
		"operation": "delete_custom_tool",
		"before":    manageAgentCustomToolMap(definition),
		"after":     nil,
	}
	if confirm {
		deleted, err := r.agents.DeleteCustomTool(toolName)
		if err != nil {
			return "", err
		}
		if !deleted {
			return "", fmt.Errorf("custom tool %q not found", toolName)
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "delete_custom_tool",
			"applied":              true,
			"custom_tool":          manageAgentCustomToolMap(definition),
			"deleted":              toolName,
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              fmt.Sprintf("applied delete for custom tool %s", toolName),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_delete_custom_tool",
		"action":               "delete_custom_tool",
		"custom_tool":          manageAgentCustomToolMap(definition),
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("proposed delete for custom tool %s", toolName),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentAssignCustomTool(args map[string]any, confirm bool) (string, error) {
	return r.manageAgentCustomToolAssignment(args, true, confirm)
}

func (r *Runtime) manageAgentUnassignCustomTool(args map[string]any, confirm bool) (string, error) {
	return r.manageAgentCustomToolAssignment(args, false, confirm)
}

func (r *Runtime) manageAgentCustomToolAssignment(args map[string]any, assign, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.agents.ListState(500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	agentName, toolName, err := manageAgentCustomToolAssignmentArgs(args)
	if err != nil {
		return "", err
	}
	profile, ok, err := r.agents.GetProfile(agentName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentName)
	}
	assignedBefore := manageAgentProfileHasToolAssignment(profile, toolName)
	var definition *pebblestore.AgentCustomToolDefinition
	if assign {
		current, ok, err := r.agents.GetCustomTool(toolName)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("custom tool %q not found", toolName)
		}
		definition = &current
		if assignedBefore {
			return "", fmt.Errorf("custom tool %q is already assigned to agent %s", toolName, profile.Name)
		}
	} else {
		if current, ok, err := r.agents.GetCustomTool(toolName); err != nil {
			return "", err
		} else if ok {
			definition = &current
		}
		if !assignedBefore {
			return "", fmt.Errorf("custom tool %q is not assigned to agent %s", toolName, profile.Name)
		}
	}
	action := "assign_custom_tool"
	status := "proposed_assign_custom_tool"
	summary := fmt.Sprintf("proposed custom tool assignment: %s → %s", profile.Name, toolName)
	assignedAfter := true
	if !assign {
		action = "unassign_custom_tool"
		status = "proposed_unassign_custom_tool"
		summary = fmt.Sprintf("proposed custom tool removal: %s × %s", profile.Name, toolName)
		assignedAfter = false
	}
	change := map[string]any{
		"kind":      "agent_change",
		"target":    "custom_tool_assignment",
		"operation": action,
		"agent":     strings.TrimSpace(profile.Name),
		"tool_name": toolName,
		"before":    assignedBefore,
		"after":     assignedAfter,
	}
	customTool := any(nil)
	if definition != nil {
		customTool = manageAgentCustomToolMap(*definition)
	}
	if confirm {
		var updatedProfile pebblestore.AgentProfile
		var applyErr error
		if assign {
			updatedProfile, _, _, applyErr = r.agents.AssignCustomTool(profile.Name, toolName)
		} else {
			updatedProfile, _, _, applyErr = r.agents.UnassignCustomTool(profile.Name, toolName)
		}
		if applyErr != nil {
			return "", applyErr
		}
		updatedState, stateErr := r.agents.ListState(500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"agent":                manageAgentProfileMap(updatedProfile, strings.EqualFold(strings.TrimSpace(updatedState.ActivePrimary), strings.TrimSpace(updatedProfile.Name)), manageAgentPurposesForProfile(updatedState.ActiveSubagent, updatedProfile.Name)),
			"tool_name":            toolName,
			"custom_tool":          customTool,
			"assigned":             assignedAfter,
			"change":               change,
			"active_primary":       strings.TrimSpace(updatedState.ActivePrimary),
			"active_subagent":      cloneStringMap(updatedState.ActiveSubagent),
			"version":              updatedState.Version,
			"path_id":              toolPathID("manage-agent"),
			"summary":              strings.Replace(summary, "proposed ", "applied ", 1),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
		}
		return manageAgentEncodeResponse(response)
	}
	response := map[string]any{
		"status":               status,
		"action":               action,
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"tool_name":            toolName,
		"custom_tool":          customTool,
		"assigned":             assignedAfter,
		"change":               change,
		"active_primary":       strings.TrimSpace(state.ActivePrimary),
		"active_subagent":      cloneStringMap(state.ActiveSubagent),
		"version":              state.Version,
		"path_id":              toolPathID("manage-agent"),
		"summary":              summary,
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentSafetyText(change)),
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) lookupManageAgentProfile(args map[string]any) (pebblestore.AgentProfile, error) {
	if r == nil || r.agents == nil {
		return pebblestore.AgentProfile{}, errors.New("manage-agent service is not configured")
	}
	name := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(args, "name")))
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("manage-agent requires agent or name")
	}
	profile, ok, err := r.agents.GetProfile(name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q not found", name)
	}
	return profile, nil
}

func manageAgentUpsertInputFromArgs(args map[string]any) (agentruntime.UpsertInput, error) {
	content, err := manageAgentContentObject(args)
	if err != nil {
		return agentruntime.UpsertInput{}, err
	}
	name := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(args, "name"), manageAgentStringArg(content, "name")))
	if name == "" {
		return agentruntime.UpsertInput{}, errors.New("manage-agent requires agent or name")
	}
	input := agentruntime.UpsertInput{
		Name:             name,
		Mode:             strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "mode"), manageAgentStringArg(content, "mode"))),
		Description:      strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "description"), manageAgentStringArg(content, "description"))),
		Prompt:           strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "prompt"), manageAgentStringArg(content, "prompt"))),
		ExecutionSetting: strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "execution_setting"), manageAgentStringArg(content, "execution_setting"))),
	}
	if value, ok := manageAgentValue(args, content, "provider"); ok {
		input.Provider = strings.TrimSpace(asString(value))
		input.ProviderSet = true
	}
	if value, ok := manageAgentValue(args, content, "model"); ok {
		input.Model = strings.TrimSpace(asString(value))
		input.ModelSet = true
	}
	if value, ok := manageAgentValue(args, content, "thinking"); ok {
		input.Thinking = strings.TrimSpace(asString(value))
		input.ThinkingSet = true
	}
	if value, ok := manageAgentValue(args, content, "exit_plan_mode_enabled"); ok {
		if typed, ok := value.(bool); ok {
			input.ExitPlanModeEnabled = pebblestore.BoolPtr(typed)
		} else {
			return agentruntime.UpsertInput{}, errors.New("manage-agent exit_plan_mode_enabled must be boolean")
		}
	}
	if value, ok := manageAgentValue(args, content, "enabled"); ok {
		if typed, ok := value.(bool); ok {
			input.Enabled = pebblestore.BoolPtr(typed)
		} else {
			return agentruntime.UpsertInput{}, errors.New("manage-agent enabled must be boolean")
		}
	}
	if value, ok := manageAgentValue(args, content, "tool_scope"); ok {
		scope, err := manageAgentToolScopeFromValue(value)
		if err != nil {
			return agentruntime.UpsertInput{}, err
		}
		input.ToolScope = scope
	}
	return input, nil
}

func manageAgentAssignmentArgs(args map[string]any) (string, string, error) {
	content, err := manageAgentContentObject(args)
	if err != nil {
		return "", "", err
	}
	purpose := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "purpose"), manageAgentStringArg(content, "purpose")))
	name := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(args, "name"), manageAgentStringArg(content, "agent"), manageAgentStringArg(content, "name")))
	if purpose == "" {
		return "", "", errors.New("manage-agent requires purpose")
	}
	if name == "" {
		return "", "", errors.New("manage-agent requires agent or name")
	}
	return purpose, name, nil
}

func manageAgentCustomToolDefinitionFromArgs(args map[string]any) (pebblestore.AgentCustomToolDefinition, error) {
	content, err := manageAgentContentObject(args)
	if err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	definition := pebblestore.NormalizeAgentCustomToolDefinition(pebblestore.AgentCustomToolDefinition{
		Name:        strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "tool_name"), manageAgentStringArg(args, "name"), manageAgentStringArg(content, "tool_name"), manageAgentStringArg(content, "name"))),
		Kind:        strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "kind"), manageAgentStringArg(content, "kind"))),
		Description: strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "description"), manageAgentStringArg(content, "description"))),
		Command:     strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "command"), manageAgentStringArg(content, "command"))),
	})
	if definition.Name == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("manage-agent custom tool requires content.name")
	}
	if definition.Kind == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("manage-agent custom tool requires content.kind")
	}
	if definition.Command == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("manage-agent custom tool requires content.command")
	}
	return definition, nil
}

func manageAgentCustomToolNameFromArgs(args map[string]any) (string, error) {
	content, err := manageAgentContentObject(args)
	if err != nil {
		return "", err
	}
	name := pebblestore.NormalizeAgentCustomToolName(strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "tool_name"), manageAgentStringArg(args, "name"), manageAgentStringArg(content, "tool_name"), manageAgentStringArg(content, "name"))))
	if name == "" {
		return "", errors.New("manage-agent requires tool_name")
	}
	return name, nil
}

func manageAgentCustomToolAssignmentArgs(args map[string]any) (string, string, error) {
	content, err := manageAgentContentObject(args)
	if err != nil {
		return "", "", err
	}
	agentName := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(content, "agent")))
	if agentName == "" {
		return "", "", errors.New("manage-agent requires agent")
	}
	toolName := pebblestore.NormalizeAgentCustomToolName(strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "tool_name"), manageAgentStringArg(content, "tool_name"), manageAgentStringArg(content, "name"))))
	if toolName == "" {
		return "", "", errors.New("manage-agent requires tool_name")
	}
	return agentName, toolName, nil
}

func validateManageAgentMutationInput(input agentruntime.UpsertInput, mustExist bool) error {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return errors.New("manage-agent requires agent or name")
	}
	hasMutation := strings.TrimSpace(input.Mode) != "" ||
		strings.TrimSpace(input.Description) != "" ||
		strings.TrimSpace(input.Prompt) != "" ||
		strings.TrimSpace(input.ExecutionSetting) != "" ||
		input.ProviderSet || input.ModelSet || input.ThinkingSet ||
		input.ExitPlanModeEnabled != nil || input.ToolScope != nil || input.Enabled != nil
	if mustExist {
		if !hasMutation {
			return errors.New("manage-agent update requires at least one field to change")
		}
		return nil
	}
	if strings.TrimSpace(input.Mode) == "" {
		return errors.New("manage-agent create requires content.mode")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return errors.New("manage-agent create requires content.prompt")
	}
	exitPlanEnabled := input.ExitPlanModeEnabled != nil && *input.ExitPlanModeEnabled
	if !exitPlanEnabled && strings.TrimSpace(input.ExecutionSetting) == "" {
		return errors.New("manage-agent create requires content.execution_setting or content.exit_plan_mode_enabled=true")
	}
	return nil
}

func manageAgentContentObject(args map[string]any) (map[string]any, error) {
	raw, ok := args["content"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed), nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return nil, fmt.Errorf("manage-agent content must be a JSON object string or object payload: %w", err)
		}
		return payload, nil
	case []byte:
		text := strings.TrimSpace(string(typed))
		if text == "" {
			return nil, nil
		}
		var payload map[string]any
		if err := json.Unmarshal(typed, &payload); err != nil {
			return nil, fmt.Errorf("manage-agent content must be a JSON object string or object payload: %w", err)
		}
		return payload, nil
	default:
		return nil, errors.New("manage-agent content must be an object or JSON object string")
	}
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		out[trimmed] = cloneStringAnyValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneStringAnyValue(item))
		}
		return out
	default:
		return value
	}
}

func manageAgentStringArg(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	return strings.TrimSpace(asString(source[key]))
}

func manageAgentValue(primary, secondary map[string]any, key string) (any, bool) {
	if primary != nil {
		if value, ok := primary[key]; ok {
			return value, true
		}
	}
	if secondary != nil {
		if value, ok := secondary[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func manageAgentToolScopeFromValue(value any) (*pebblestore.AgentToolScope, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("manage-agent tool_scope must be an object")
	}
	scope := &pebblestore.AgentToolScope{}
	scope.Preset = strings.TrimSpace(asString(object["preset"]))
	scope.AllowTools = manageAgentStringSlice(object["allow_tools"])
	scope.DenyTools = manageAgentStringSlice(object["deny_tools"])
	scope.BashPrefixes = manageAgentStringSlice(object["bash_prefixes"])
	if inherit, ok := object["inherit_policy"].(bool); ok {
		scope.InheritPolicy = inherit
	}
	return pebblestore.NormalizeAgentToolScope(scope), nil
}

func manageAgentStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(asString(item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func manageAgentProfileMap(profile pebblestore.AgentProfile, activePrimary bool, purposes []string) map[string]any {
	payload := map[string]any{
		"name":                   strings.TrimSpace(profile.Name),
		"mode":                   strings.TrimSpace(profile.Mode),
		"description":            strings.TrimSpace(profile.Description),
		"provider":               strings.TrimSpace(profile.Provider),
		"model":                  strings.TrimSpace(profile.Model),
		"thinking":               strings.TrimSpace(profile.Thinking),
		"prompt":                 strings.TrimSpace(profile.Prompt),
		"execution_setting":      strings.TrimSpace(profile.ExecutionSetting),
		"exit_plan_mode_enabled": pebblestore.AgentExitPlanModeEnabled(profile),
		"tool_scope":             manageAgentToolScopeMap(profile.ToolScope),
		"tool_contract":          manageAgentToolContractMap(profile.ToolContract),
		"enabled":                profile.Enabled,
		"updated_at":             profile.UpdatedAt,
		"active_primary":         activePrimary,
		"active_purposes":        append([]string(nil), purposes...),
	}
	return payload
}

func manageAgentOptionalProfileMap(profile *pebblestore.AgentProfile, state agentruntime.State) any {
	if profile == nil {
		return nil
	}
	return manageAgentProfileMap(*profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name))
}

func manageAgentToolScopeMap(scope *pebblestore.AgentToolScope) any {
	if scope == nil {
		return nil
	}
	return map[string]any{
		"preset":         strings.TrimSpace(scope.Preset),
		"allow_tools":    append([]string(nil), scope.AllowTools...),
		"deny_tools":     append([]string(nil), scope.DenyTools...),
		"bash_prefixes":  append([]string(nil), scope.BashPrefixes...),
		"inherit_policy": scope.InheritPolicy,
	}
}

func manageAgentToolContractMap(contract *pebblestore.AgentToolContract) any {
	if contract == nil {
		return nil
	}
	payload := map[string]any{
		"preset":         strings.TrimSpace(contract.Preset),
		"inherit_policy": contract.InheritPolicy,
	}
	if len(contract.Tools) > 0 {
		tools := make(map[string]any, len(contract.Tools))
		for name, cfg := range contract.Tools {
			entry := map[string]any{}
			if cfg.Enabled != nil {
				entry["enabled"] = *cfg.Enabled
			}
			if len(cfg.BashPrefixes) > 0 {
				entry["bash_prefixes"] = append([]string(nil), cfg.BashPrefixes...)
			}
			if len(entry) == 0 {
				continue
			}
			tools[name] = entry
		}
		if len(tools) > 0 {
			payload["tools"] = tools
		}
	}
	return payload
}

func manageAgentCustomToolMap(definition pebblestore.AgentCustomToolDefinition) map[string]any {
	return map[string]any{
		"name":        strings.TrimSpace(definition.Name),
		"kind":        strings.TrimSpace(definition.Kind),
		"description": strings.TrimSpace(definition.Description),
		"command":     strings.TrimSpace(definition.Command),
		"updated_at":  definition.UpdatedAt,
	}
}

func manageAgentOptionalCustomToolMap(definition *pebblestore.AgentCustomToolDefinition) any {
	if definition == nil {
		return nil
	}
	return manageAgentCustomToolMap(*definition)
}

func manageAgentProfileHasToolAssignment(profile pebblestore.AgentProfile, toolName string) bool {
	toolName = pebblestore.NormalizeAgentCustomToolName(toolName)
	if toolName == "" || profile.ToolContract == nil || len(profile.ToolContract.Tools) == 0 {
		return false
	}
	cfg, ok := profile.ToolContract.Tools[toolName]
	if !ok {
		return false
	}
	return cfg.Enabled == nil || *cfg.Enabled
}

func manageAgentPurposesForProfile(assignments map[string]string, profileName string) []string {
	profileName = strings.TrimSpace(strings.ToLower(profileName))
	if profileName == "" || len(assignments) == 0 {
		return nil
	}
	out := make([]string, 0, len(assignments))
	for purpose, name := range assignments {
		if strings.TrimSpace(strings.ToLower(name)) == profileName {
			out = append(out, strings.TrimSpace(purpose))
		}
	}
	sort.Strings(out)
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func manageAgentSafetyText(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func manageAgentEncodeResponse(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func manageSkillGet(scope WorkspaceScope, args map[string]any) (string, error) {
	matched, err := manageSkillLookupSkill(scope, args)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(matched.Path)
	if err != nil {
		return "", fmt.Errorf("manage-skill get read failed: %w", err)
	}
	truncated := false
	if len(raw) > maxSkillContentBytes {
		raw = raw[:maxSkillContentBytes]
		truncated = true
	}
	content := strings.TrimSpace(sanitizeForToolOutput(string(raw)))
	response := map[string]any{
		"status": "ok",
		"action": "get",
		"skill": map[string]any{
			"name":           matched.Name,
			"canonical_name": matched.CanonicalName,
			"description":    matched.Description,
			"path":           matched.Path,
			"scope":          matched.Scope,
			"origin":         matched.Origin,
			"metadata":       matched.Metadata,
		},
		"content":              content,
		"truncated":            truncated,
		"path_id":              toolPathID("manage-skill"),
		"summary":              fmt.Sprintf("loaded skill %s", matched.CanonicalName),
		"details_truncated":    truncated,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(content),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageSkillChange(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	if confirm {
		return manageSkillApplyChange(scope, args, mustExist)
	}
	return manageSkillProposeChange(scope, args, mustExist)
}

func manageSkillProposeChange(scope WorkspaceScope, args map[string]any, mustExist bool) (string, error) {
	canonical := manageSkillRequestedCanonical(args)
	if canonical == "" {
		return "", errors.New("manage-skill requires skill or name")
	}
	content := strings.TrimSpace(asString(args["content"]))
	if content == "" {
		return "", errors.New("manage-skill requires content for create/update")
	}
	frontmatter, err := discovery.ParseSkillFrontmatter([]byte(content))
	if err != nil {
		return "", fmt.Errorf("manage-skill invalid skill content: %w", err)
	}
	if err := discovery.ValidateSkillFrontmatter(frontmatter, canonical); err != nil {
		return "", fmt.Errorf("manage-skill invalid skill content: %w", err)
	}
	path := manageSkillSkillPath(scope, canonical)
	beforeBytes, readErr := os.ReadFile(path)
	before := ""
	if readErr == nil {
		before = string(beforeBytes)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("manage-skill read existing skill failed: %w", readErr)
	}
	if mustExist && strings.TrimSpace(before) == "" {
		return "", fmt.Errorf("skill %q does not exist", canonical)
	}
	if !mustExist && strings.TrimSpace(before) != "" {
		return "", fmt.Errorf("skill %q already exists; use update", canonical)
	}
	formatted := ensureTrailingNewline(content)
	action := "create"
	status := "proposed_create"
	summary := fmt.Sprintf("proposed new skill %s", canonical)
	if strings.TrimSpace(before) != "" {
		action = "update"
		status = "proposed_update"
		summary = fmt.Sprintf("proposed update for skill %s", canonical)
	}
	response := map[string]any{
		"status": status,
		"action": action,
		"skill": map[string]any{
			"name":           strings.TrimSpace(frontmatter.Name),
			"canonical_name": canonical,
			"description":    strings.TrimSpace(frontmatter.Description),
			"path":           path,
			"metadata":       frontmatter.Metadata,
		},
		"change": map[string]any{
			"kind":      "skill_change",
			"operation": action,
			"path":      path,
			"before":    before,
			"after":     formatted,
		},
		"path_id":              toolPathID("manage-skill"),
		"summary":              summary,
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(before + "\n" + formatted),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageSkillProposeDelete(scope WorkspaceScope, args map[string]any) (string, error) {
	matched, err := manageSkillLookupSkill(scope, args)
	if err != nil {
		return "", err
	}
	beforeBytes, err := os.ReadFile(matched.Path)
	if err != nil {
		return "", fmt.Errorf("manage-skill delete read failed: %w", err)
	}
	before := string(beforeBytes)
	response := map[string]any{
		"status": "proposed_delete",
		"action": "delete",
		"skill": map[string]any{
			"name":           matched.Name,
			"canonical_name": matched.CanonicalName,
			"description":    matched.Description,
			"path":           matched.Path,
			"metadata":       matched.Metadata,
		},
		"change": map[string]any{
			"kind":      "skill_change",
			"operation": "delete",
			"path":      matched.Path,
			"before":    before,
			"after":     "",
		},
		"path_id":              toolPathID("manage-skill"),
		"summary":              fmt.Sprintf("proposed delete for skill %s", matched.CanonicalName),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(before),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageSkillApplyChange(scope WorkspaceScope, args map[string]any, mustExist bool) (string, error) {
	proposalRaw, err := manageSkillProposeChange(scope, args, mustExist)
	if err != nil {
		return "", err
	}
	var proposal map[string]any
	if err := json.Unmarshal([]byte(proposalRaw), &proposal); err != nil {
		return "", err
	}
	change, _ := proposal["change"].(map[string]any)
	path := strings.TrimSpace(asString(change["path"]))
	after := asString(change["after"])
	if path == "" {
		return "", errors.New("manage-skill apply proposal missing path")
	}
	if !manageSkillSkillPathAllowed(path, scope) {
		return "", fmt.Errorf("manage-skill path %q is outside workspace skill root", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("manage-skill create skill directory failed: %w", err)
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		return "", fmt.Errorf("manage-skill write failed: %w", err)
	}
	response := map[string]any{
		"status":               "ok",
		"action":               strings.TrimSpace(asString(proposal["action"])),
		"applied":              true,
		"skill":                proposal["skill"],
		"change":               change,
		"path_id":              toolPathID("manage-skill"),
		"summary":              strings.Replace(strings.TrimSpace(asString(proposal["summary"])), "proposed ", "applied ", 1),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(after),
		"hot_reload": map[string]any{
			"enabled": true,
			"summary": "Skill change written to disk. Refresh to rediscover it.",
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageSkillDelete(scope WorkspaceScope, args map[string]any) (string, error) {
	proposalRaw, err := manageSkillProposeDelete(scope, args)
	if err != nil {
		return "", err
	}
	var proposal map[string]any
	if err := json.Unmarshal([]byte(proposalRaw), &proposal); err != nil {
		return "", err
	}
	change, _ := proposal["change"].(map[string]any)
	path := strings.TrimSpace(asString(change["path"]))
	if path == "" {
		return "", errors.New("manage-skill delete proposal missing path")
	}
	if !manageSkillSkillPathAllowed(path, scope) {
		return "", fmt.Errorf("manage-skill path %q is outside workspace skill root", path)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("manage-skill delete failed: %w", err)
	}
	_ = os.Remove(filepath.Dir(path))
	response := map[string]any{
		"status":               "ok",
		"action":               "delete",
		"applied":              true,
		"skill":                proposal["skill"],
		"change":               change,
		"path_id":              toolPathID("manage-skill"),
		"summary":              strings.Replace(strings.TrimSpace(asString(proposal["summary"])), "proposed ", "applied ", 1),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(asString(change["before"])),
		"hot_reload": map[string]any{
			"enabled": true,
			"summary": "Skill deleted from disk. Refresh to rediscover the updated set.",
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageSkillLookupSkill(scope WorkspaceScope, args map[string]any) (discovery.SkillSource, error) {
	requested := strings.TrimSpace(asString(args["skill"]))
	if requested == "" {
		requested = strings.TrimSpace(asString(args["name"]))
	}
	if requested == "" {
		return discovery.SkillSource{}, errors.New("manage-skill requires skill or name")
	}
	report, err := discovery.NewService().ScanScope(scope.PrimaryPath, scope.Roots)
	if err != nil {
		return discovery.SkillSource{}, fmt.Errorf("manage-skill scan failed: %w", err)
	}
	target := normalizeSkillLookup(requested)
	for _, candidate := range report.Skills {
		if !manageSkillSkillPathAllowed(candidate.Path, scope) {
			continue
		}
		if normalizeSkillLookup(candidate.CanonicalName) == target || normalizeSkillLookup(candidate.Name) == target {
			return candidate, nil
		}
	}
	return discovery.SkillSource{}, fmt.Errorf("skill %q not found", requested)
}

func manageSkillRequestedCanonical(args map[string]any) string {
	requested := strings.TrimSpace(asString(args["skill"]))
	if requested == "" {
		requested = strings.TrimSpace(asString(args["name"]))
	}
	if requested == "" {
		return ""
	}
	return discovery.NormalizeSkillName(requested)
}

func manageSkillSkillRoot(scope WorkspaceScope) string {
	return filepath.Join(scope.PrimaryPath, ".agents", "skills")
}

func manageSkillSkillPath(scope WorkspaceScope, canonical string) string {
	canonical = discovery.NormalizeSkillName(canonical)
	if canonical == "" {
		canonical = "skill"
	}
	return filepath.Join(manageSkillSkillRoot(scope), canonical, "SKILL.md")
}

func manageSkillSkillPathAllowed(path string, scope WorkspaceScope) bool {
	root := manageSkillSkillRoot(scope)
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ensureTrailingNewline(value string) string {
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "\n") {
		return value
	}
	return value + "\n"
}

func summarizeAvailableSkills(skills []discovery.SkillSource, maxItems int) []string {
	if maxItems <= 0 {
		maxItems = maxSkillListPreview
	}
	out := make([]string, 0, minInt(maxItems, len(skills)))
	for i := range skills {
		name := strings.TrimSpace(skills[i].CanonicalName)
		if name == "" {
			name = strings.TrimSpace(skills[i].Name)
		}
		if name == "" {
			continue
		}
		out = append(out, name)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func matchSkillSource(skills []discovery.SkillSource, target string) (discovery.SkillSource, bool) {
	for i := range skills {
		candidate := skills[i]
		if normalizeSkillLookup(candidate.CanonicalName) == target || normalizeSkillLookup(candidate.Name) == target {
			return candidate, true
		}
	}
	return discovery.SkillSource{}, false
}

func normalizeSkillLookup(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func collectListEntries(rootPath, mode string, maxDepth int) ([]listEntry, bool, error) {
	entries := make([]listEntry, 0, 256)
	scanLimited := false

	walkErr := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == rootPath {
			return nil
		}

		relative, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(strings.TrimSpace(relative))
		if relative == "" || relative == "." {
			return nil
		}

		depth := listPathDepth(relative)
		if mode == "tree" && depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entry := listEntry{
			Path: relative,
			Type: dirEntryType(d),
		}
		if mode == "tree" {
			entry.Depth = depth
		}
		entries = append(entries, entry)
		if len(entries) >= maxListScanEntries {
			scanLimited = true
			return errListScanLimit
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errListScanLimit) {
		return nil, scanLimited, fmt.Errorf("list failed: %w", walkErr)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, scanLimited, nil
}

func listPathDepth(relative string) int {
	if strings.TrimSpace(relative) == "" || relative == "." {
		return 0
	}
	return strings.Count(relative, "/") + 1
}

func dirEntryType(entry os.DirEntry) string {
	if entry == nil {
		return "other"
	}
	mode := entry.Type()
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case entry.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	default:
		return "other"
	}
}

func canonicalStubToolName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	switch name {
	case "ask-user", "ask_user":
		return "ask_user"
	case "manage-skill", "manage_skill":
		return "manage_skill"
	case "manage-agent", "manage_agent":
		return "manage_agent"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	case "skill-use", "skill_use":
		return "skill_use"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	default:
		return strings.ReplaceAll(name, "-", "_")
	}
}

func stubToolPathID(name string) string {
	switch canonicalStubToolName(name) {
	case "ask_user":
		return "tool.stub.ask-user.v3"
	case "manage_skill":
		return "tool.manage-skill.v1"
	case "manage_agent":
		return "tool.manage-agent.v1"
	case "manage_worktree":
		return "tool.manage-worktree.v1"
	case "manage_todos":
		return "tool.manage-todos.v1"
	case "skill_use":
		return "tool.stub.skill-use.v3"
	case "exit_plan_mode":
		return "tool.stub.exit-plan-mode.v3"
	case "plan_manage":
		return "tool.stub.plan-manage.v3"
	default:
		return "tool.stub.unknown.v3"
	}
}

func runRipgrepFiles(ctx context.Context, rgPath, searchRoot, pattern string) ([]string, bool, bool, error) {
	return nil, false, false, errors.New("ripgrep file search removed; use FFF-backed search")
}

func runRipgrepGrep(ctx context.Context, rgPath, searchRoot, pattern, include string) ([]map[string]any, bool, bool, error) {
	return nil, false, false, errors.New("ripgrep grep removed; use FFF-backed search")
}

func resolveWorkspacePath(scope WorkspaceScope, requested string) (string, error) {
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	if workspacePath == "" {
		return "", errors.New("workspace path is empty")
	}
	candidateAbs, resolvedCandidate, err := normalizeWorkspaceCandidatePath(workspacePath, requested)
	if err != nil {
		return "", err
	}

	if !pathWithinAllowedRoots(resolveAllowedRoots(scope), resolvedCandidate) {
		return "", fmt.Errorf("path %q escapes workspace scope", requested)
	}
	return candidateAbs, nil
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	default:
		return false
	}
}

func mapString(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	return strings.TrimSpace(asString(source[key]))
}

func asInt(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
		return fallback
	default:
		return fallback
	}
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func resolveSearchRoot(scope WorkspaceScope, rawPath any) (string, error) {
	if path := strings.TrimSpace(asString(rawPath)); path != "" {
		return resolveWorkspacePath(scope, path)
	}
	return resolveWorkspacePath(scope, ".")
}

func normalizeWorkspaceScope(primary string, roots []string) WorkspaceScope {
	primary = normalizeScopePath(primary)
	out := make([]string, 0, len(roots)+1)
	seen := make(map[string]struct{}, len(roots)+1)
	add := func(path string) {
		path = normalizeScopePath(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	add(primary)
	for _, root := range roots {
		add(root)
	}
	if primary == "" && len(out) > 0 {
		primary = out[0]
	}
	if primary != "" && len(out) == 0 {
		out = []string{primary}
	}
	return WorkspaceScope{
		PrimaryPath: primary,
		Roots:       out,
	}
}

func normalizeScopePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return abs
}

func resolveAllowedRoots(scope WorkspaceScope) []string {
	normalized := normalizeWorkspaceScope(scope.PrimaryPath, scope.Roots)
	if len(normalized.Roots) > 0 {
		return normalized.Roots
	}
	if strings.TrimSpace(normalized.PrimaryPath) != "" {
		return []string{normalized.PrimaryPath}
	}
	return nil
}

func pathWithinAllowedRoots(roots []string, candidate string) bool {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func resolveSearchTimeout(raw any) time.Duration {
	timeout := time.Duration(asInt(raw, int(defaultSearchTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultSearchTimeout
	}
	if timeout > maxSearchTimeout {
		timeout = maxSearchTimeout
	}
	return timeout
}

type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit <= 0 {
		limit = maxCommandOutput
	}
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.limit <= 0 {
		c.dropped += len(p)
		return len(p), nil
	}
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.dropped += len(p)
		return len(p), nil
	}
	if len(p) <= remaining {
		_, _ = c.buf.Write(p)
		return len(p), nil
	}
	_, _ = c.buf.Write(p[:remaining])
	c.dropped += len(p) - remaining
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	if c.dropped <= 0 {
		return c.buf.String()
	}
	return c.buf.String() + fmt.Sprintf("\n...[truncated %d bytes]", c.dropped)
}

func (c *cappedBuffer) Bytes() []byte {
	return c.buf.Bytes()
}

func (c *cappedBuffer) Truncated() bool {
	return c.dropped > 0
}

type bashStreamWriter struct {
	mu                   sync.Mutex
	capture              *cappedBuffer
	emit                 func(string)
	streamBudget         int
	streamTruncated      bool
	streamTruncAnnounced bool
	binarySuppressed     bool
	pending              strings.Builder
}

func newBashStreamWriter(capture *cappedBuffer, streamBudget int, emit func(string)) *bashStreamWriter {
	if streamBudget <= 0 {
		streamBudget = maxCommandOutput
	}
	return &bashStreamWriter{
		capture:      capture,
		emit:         emit,
		streamBudget: streamBudget,
	}
}

func (w *bashStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.capture != nil {
		_, _ = w.capture.Write(p)
	}

	if w.binarySuppressed {
		return len(p), nil
	}
	if isLikelyBinary(p) {
		w.binarySuppressed = true
		w.pending.Reset()
		if w.emit != nil {
			w.emit("[binary output suppressed]")
		}
		return len(p), nil
	}
	if w.emit == nil {
		return len(p), nil
	}

	sanitized := sanitizeForToolOutput(string(p))
	if sanitized == "" {
		return len(p), nil
	}
	w.pending.WriteString(sanitized)
	w.flushLocked(false)
	return len(p), nil
}

func (w *bashStreamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(true)
}

func (w *bashStreamWriter) flushLocked(force bool) {
	if w.emit == nil {
		w.pending.Reset()
		return
	}
	for w.pending.Len() > 0 {
		if w.streamBudget <= 0 {
			if !w.streamTruncAnnounced {
				w.emit("...[stream output truncated]")
				w.streamTruncAnnounced = true
			}
			w.pending.Reset()
			return
		}

		pendingText := w.pending.String()
		flushAt := -1
		if idx := strings.LastIndexByte(pendingText, '\n'); idx >= 0 {
			flushAt = idx + 1
		}
		if flushAt < 0 && !force && len(pendingText) < bashStreamEmitChunkBytes {
			return
		}
		if flushAt < 0 || flushAt > len(pendingText) {
			flushAt = len(pendingText)
		}

		candidate := pendingText[:flushAt]
		if candidate == "" {
			w.pending.Reset()
			if flushAt < len(pendingText) {
				w.pending.WriteString(pendingText[flushAt:])
			}
			continue
		}
		chunk := clampToUTF8Bytes(candidate, w.streamBudget)
		if chunk == "" {
			if !w.streamTruncAnnounced {
				w.emit("...[stream output truncated]")
				w.streamTruncAnnounced = true
			}
			w.pending.Reset()
			return
		}

		w.streamBudget -= len(chunk)
		if w.streamBudget <= 0 {
			w.streamTruncated = true
		}
		w.emit(chunk)

		consumed := flushAt
		if chunk != candidate {
			consumed = len(chunk)
		}
		if consumed < 0 {
			consumed = 0
		}
		if consumed > len(pendingText) {
			consumed = len(pendingText)
		}
		remaining := pendingText[consumed:]
		w.pending.Reset()
		w.pending.WriteString(remaining)

		if w.streamTruncated {
			if !w.streamTruncAnnounced {
				w.emit("...[stream output truncated]")
				w.streamTruncAnnounced = true
			}
			w.pending.Reset()
			return
		}
		if !force {
			return
		}
	}
}

func (w *bashStreamWriter) BinarySuppressed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.binarySuppressed
}

func clampToUTF8Bytes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	trimmed := value[:limit]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func toolPathID(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "tool.read.v3"
	case "write":
		return "tool.write.v3"
	case "bash":
		return "tool.bash.v3"
	case "glob":
		return "tool.glob.v3"
	case "search":
		return "tool.search.v1"
	case "websearch":
		return "tool.websearch.exa.v1"
	case "webfetch":
		return "tool.webfetch.exa.v1"
	case "agentic_search":
		return "tool.agentic-search.v1"
	case "list":
		return "tool.list.v3"
	case "edit":
		return "tool.edit.v3"
	case "manage-skill", "manage_skill":
		return "tool.manage-skill.v1"
	case "manage-agent", "manage_agent":
		return "tool.manage-agent.v1"
	case "manage-worktree", "manage_worktree":
		return "tool.manage-worktree.v1"
	case "manage-todos", "manage_todos":
		return "tool.manage-todos.v1"
	case "skill-use", "skill_use":
		return "tool.skill-use.v3"
	default:
		return "tool.unknown.v3"
	}
}

func readSummary(path string, bytesRead int, truncated, binarySuppressed bool) string {
	label := fmt.Sprintf("read %s (%d bytes", truncateSummary(path, 160), bytesRead)
	if truncated {
		label += ", partial"
	}
	if binarySuppressed {
		label += ", binary output hidden"
	}
	return label + ")"
}

func writeSummary(path string, bytesWritten int, appendMode bool) string {
	action := "write"
	if appendMode {
		action = "append"
	}
	return fmt.Sprintf("%s %s (%d bytes)", action, truncateSummary(path, 160), bytesWritten)
}

func countSummary(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func parentheticalSummary(label string, notes ...string) string {
	filtered := make([]string, 0, len(notes))
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note == "" {
			continue
		}
		filtered = append(filtered, note)
	}
	if len(filtered) == 0 {
		return label
	}
	return label + " (" + strings.Join(filtered, ", ") + ")"
}

func listModeSummary(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tree":
		return "tree view"
	case "flat":
		return "flat view"
	case "":
		return ""
	default:
		return mode + " view"
	}
}

func bashSummary(command string, exitCode int, timedOut, truncated, binarySuppressed bool) string {
	label := "bash"
	if command = strings.TrimSpace(truncateSummary(command, 80)); command != "" {
		label += " " + command
	}
	notes := make([]string, 0, 3)
	switch {
	case timedOut:
		notes = append(notes, "timed out")
	case exitCode != 0:
		notes = append(notes, "failed")
	}
	if truncated {
		notes = append(notes, "partial output")
	}
	if binarySuppressed {
		notes = append(notes, "binary output hidden")
	}
	return parentheticalSummary(label, notes...)
}

func globSummary(pattern, root string, count int, truncated, timedOut bool) string {
	label := "glob"
	if pattern = strings.TrimSpace(truncateSummary(pattern, 80)); pattern != "" {
		label += " " + fmt.Sprintf("%q", pattern)
	}
	if root = strings.TrimSpace(truncateSummary(root, 120)); root != "" {
		label += " in " + root
	}
	notes := []string{countSummary(count, "file", "files")}
	if timedOut {
		notes = append(notes, "timed out")
	} else if truncated {
		notes = append(notes, "partial results")
	}
	return parentheticalSummary(label, notes...)
}

func searchSummary(pattern, root string, count int, truncated, timedOut, contentMode bool) string {
	return searchSummaryForQueries([]string{pattern}, root, count, truncated, timedOut, contentMode)
}

func searchSummaryForQueries(queries []string, root string, count int, truncated, timedOut, contentMode bool) string {
	label := "search"
	queries = compactSearchQueries(queries)
	if len(queries) == 1 {
		label += " " + fmt.Sprintf("%q", truncateSummary(queries[0], 80))
	} else if len(queries) > 1 {
		label += " [" + countSummary(len(queries), "query", "queries") + "]"
	}
	if root = strings.TrimSpace(truncateSummary(root, 120)); root != "" {
		label += " in " + root
	}
	notes := []string{countSummary(count, "file", "files")}
	if contentMode {
		notes[0] = countSummary(count, "match", "matches")
	}
	if timedOut {
		notes = append(notes, "timed out")
	} else if truncated {
		notes = append(notes, "partial results")
	}
	return parentheticalSummary(label, notes...)
}

func compactSearchQueries(queries []string) []string {
	if len(queries) == 0 {
		return nil
	}
	out := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, query)
	}
	return out
}

func grepSummary(pattern, root string, count int, truncated, timedOut bool) string {
	return searchSummary(pattern, root, count, truncated, timedOut, true)
}

func agenticSearchSummary(query string, candidates, matches int, truncated bool, mode string) string {
	label := "agentic_search"
	if query = strings.TrimSpace(truncateSummary(query, 80)); query != "" {
		label += " " + fmt.Sprintf("%q", query)
	}
	notes := []string{
		countSummary(candidates, "candidate", "candidates"),
		countSummary(matches, "match", "matches"),
	}
	if mode = strings.TrimSpace(mode); mode != "" {
		notes = append(notes, mode+" mode")
	}
	if truncated {
		notes = append(notes, "partial results")
	}
	return parentheticalSummary(label, notes...)
}

func agenticSearchBatchSummary(results []agenticSearchQueryResult) string {
	if len(results) == 0 {
		return "agentic_search (no queries)"
	}
	if len(results) == 1 {
		return results[0].Summary
	}

	totalCandidates := 0
	totalMatches := 0
	for _, result := range results {
		totalCandidates += result.RankedCandidates
		totalMatches += result.Count
	}

	return parentheticalSummary(
		"agentic_search batch",
		countSummary(len(results), "query", "queries"),
		countSummary(totalCandidates, "candidate", "candidates"),
		countSummary(totalMatches, "match", "matches"),
	)
}

func listSummary(path, mode string, count, totalFound int, truncated, scanLimited bool) string {
	label := "list"
	if path = strings.TrimSpace(truncateSummary(path, 120)); path != "" {
		label += " " + path
	}
	notes := make([]string, 0, 4)
	switch {
	case totalFound > count:
		notes = append(notes, fmt.Sprintf("showing %d of %d entries", count, totalFound))
	default:
		notes = append(notes, countSummary(count, "entry", "entries"))
	}
	if view := listModeSummary(mode); view != "" {
		notes = append(notes, view)
	}
	if truncated {
		notes = append(notes, "partial results")
	}
	if scanLimited {
		notes = append(notes, "scan limited")
	}
	return parentheticalSummary(label, notes...)
}

func editSummary(path string, replacements, editCount int, replaceAll bool) string {
	label := "edit"
	if path = strings.TrimSpace(truncateSummary(path, 120)); path != "" {
		label += " " + path
	}
	notes := make([]string, 0, 3)
	if editCount > 1 {
		notes = append(notes, countSummary(editCount, "edit", "edits"))
	}
	notes = append(notes, countSummary(replacements, "replacement", "replacements"))
	if replaceAll {
		if editCount > 1 {
			notes = append(notes, "contains replace-all")
		} else {
			notes = append(notes, "replace all")
		}
	}
	return parentheticalSummary(label, notes...)
}

func sanitizeEditPreview(value string, maxRunes int) (string, bool) {
	value = sanitizeForToolOutput(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return value, false
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes]), true
	}
	return string(runes[:maxRunes-3]) + "...", true
}

func truncateSummary(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func clampRunesWithEllipsis(value string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", value != ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes]), true
	}
	return string(runes[:maxRunes-3]) + "...", true
}

func sanitizeForToolOutput(value string) string {
	if value == "" {
		return ""
	}
	if strings.IndexByte(value, 0x1b) >= 0 {
		value = ansiCSIRegex.ReplaceAllString(value, "")
		value = ansiOSCRegex.ReplaceAllString(value, "")
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isLikelyBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0x00) >= 0 {
		return true
	}
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if !utf8.Valid(sample) {
		return true
	}
	controlChars := 0
	runeCount := 0
	for len(sample) > 0 {
		r, size := utf8.DecodeRune(sample)
		if r == utf8.RuneError && size == 1 {
			return true
		}
		runeCount++
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			controlChars++
		}
		sample = sample[size:]
	}
	if runeCount == 0 {
		return false
	}
	return controlChars > (runeCount/20)+4
}

func buildUntrustedSafety(text string) map[string]any {
	signals, scanTruncated := detectPromptInjectionSignals(text)
	return map[string]any{
		"untrusted_content":          true,
		"prompt_injection_detected":  len(signals) > 0,
		"prompt_injection_signals":   signals,
		"scan_truncated_for_signals": scanTruncated,
	}
}

func detectPromptInjectionSignals(text string) ([]string, bool) {
	scan := strings.ToLower(strings.TrimSpace(text))
	if scan == "" {
		return nil, false
	}
	scanTruncated := false
	if len(scan) > maxSafetyScanChars {
		scan = scan[:maxSafetyScanChars]
		scanTruncated = true
	}
	signals := make([]string, 0, 4)
	for _, marker := range promptMarkers {
		if strings.Contains(scan, marker) {
			signals = append(signals, marker)
			if len(signals) >= 4 {
				break
			}
		}
	}
	return signals, scanTruncated
}

func collectGrepSafetyText(matches []map[string]any) string {
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	for _, match := range matches {
		text := strings.TrimSpace(asString(match["text"]))
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			if b.Len()+1 >= maxSafetyScanChars {
				break
			}
			b.WriteByte('\n')
		}
		remaining := maxSafetyScanChars - b.Len()
		if remaining <= 0 {
			break
		}
		if len(text) > remaining {
			b.WriteString(text[:remaining])
			break
		}
		b.WriteString(text)
	}
	return b.String()
}

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
	"io/fs"
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
	"unicode"
	"unicode/utf8"

	actionruntime "swarm/packages/swarmd/internal/action"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/fff"
	"swarm/packages/swarmd/internal/gitenv"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	todoruntime "swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/tool/searchipc"
	uisettings "swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/videosource"
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
	maxSearchHelperStdout               = 8 * 1024 * 1024
	maxSearchHelperStderr               = 64 * 1024
	defaultBashTimeout                  = 2 * time.Minute
	maxBashTimeout                      = 30 * time.Minute
	defaultGitTimeout                   = 20 * time.Second
	defaultGitCommitTimeout             = 5 * time.Minute
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
	defaultWebDownloadSubdir            = "downloads"
	defaultExaSearchURL                 = "https://api.exa.ai/search"
	defaultExaContentsURL               = "https://api.exa.ai/contents"
	fffSearchHelperBinaryName           = "swarm-fff-search"
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
	maxParallel          int
	httpClient           *http.Client
	exaConfigResolver    func(context.Context) (ExaRuntimeConfig, error)
	sessions             manageSessionService
	publishSessionOutbox func(pebblestore.V3RealtimeOutboxRecord) error
	workspace            manageWorktreeWorkspaceService
	worktrees            manageWorktreeConfigService
	agents               manageAgentService
	orchestration        manageOrchestrationPolicyService
	todos                manageTodoService
	actions              manageActionService
	uiSettings           manageThemeUISettingsService
	themeWorkspace       manageThemeWorkspaceService
	artifacts            *artifact.Registry
	artifactAuthority    ArtifactAuthority
	imageGeneration      ManagedImageGenerationService
	video                manageVideoService
	videoSources         *videosource.Service
	videoProjects        manageVideoProjectService
	videoRender          manageVideoRenderService
	searchCoordinator    *SearchCoordinator
}

type ExaRuntimeConfig struct {
	Enabled     bool
	Source      string
	APIKey      string
	SearchURL   string
	ContentsURL string
}

type WorkspaceScope struct {
	PrimaryPath         string
	Roots               []string
	SessionID           string
	Principal           identity.Principal
	WorktreeEnabled     bool
	WorktreeRootPath    string
	WorktreeBranch      string
	SourceWorkspacePath string
}

type manageSessionService interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	GetV3MessageByID(sessionID, messageID string) (pebblestore.MessageSnapshot, bool, error)
	GetActivePlan(sessionID string) (pebblestore.SessionPlanSnapshot, bool, error)
	ListMessages(sessionID string, afterGlobalSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error)
	ListTopSessionsByWorkspace(workspacePaths []string, perWorkspaceLimit int) ([]pebblestore.WorkspaceSessionList, error)
	SearchSessions(pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error)
	ListSessionEventsBefore(sessionID string, beforeSeq uint64, limit int) ([]pebblestore.V3SessionEvent, error)
	GetSessionTombstone(sessionID string) (pebblestore.V3SessionTombstone, bool, error)
	ListSessionMessageTail(sessionID string, limit int) ([]pebblestore.MessageSnapshot, error)
	ListSessionMessagesBefore(sessionID string, beforeSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error)
	ArchiveSessionsWithEventsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) ([]*pebblestore.EventEnvelope, error)
	ReactivateArchivedSessionsIfUnchanged(sessionIDs []string, expectedUpdatedAt map[string]int64) error
	CurrentRealtimeOutboxRevision() (uint64, error)
	LastRealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID string, endpointSeq uint64) (pebblestore.V3RealtimeOutboxRecord, bool, error)
}

type manageWorktreeWorkspaceService interface {
	CurrentBindingForPrincipal(principal identity.Principal) (workspaceruntime.Resolution, bool, error)
	ScopeForPathForPrincipal(principal identity.Principal, path string) (workspaceruntime.Scope, error)
	ListKnownForPrincipal(principal identity.Principal, limit int) ([]workspaceruntime.Entry, error)
}

type manageWorktreeConfigService interface {
	GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error)
	InspectTaskWorkspace(workspacePath string) (worktreeruntime.TaskWorkspaceState, error)
	TaskCommitDescendsFrom(workspacePath, baseCommit, headCommit string) (bool, error)
	VerifyTaskIntegrationWorkspace(parentPath, childPath, sessionID, branchName, baseCommit, headCommit string) (worktreeruntime.TaskWorkspaceState, error)
	PrepareTaskIntegration(parentPath, expectedParentHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error)
	ApplyTaskIntegration(parentPath string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error)
}

type manageWorktreeIntegrationClassifier interface {
	TaskCommitRangeIntegratedInto(workspacePath, baseCommit, headCommit, parentHead string) (bool, error)
}

type manageOrchestrationPolicyService interface {
	CurrentSubagentPolicyForAccount(accountScopeID string) (map[string]any, error)
	UpdateSubagentPolicyMapForAccount(accountScopeID string, input map[string]any) (map[string]any, error)
}

type manageAgentService interface {
	ListState(limit int) (agentruntime.State, error)
	ListStateForAccount(accountScopeID string, limit int) (agentruntime.State, error)
	ReplaceManagedState(state agentruntime.State, syncProfiles, syncCustomTools bool) (agentruntime.State, int64, *pebblestore.EventEnvelope, error)
	ListCustomTools(limit int) ([]pebblestore.AgentCustomToolDefinition, error)
	ListCustomToolsForAccount(accountScopeID string, limit int) ([]pebblestore.AgentCustomToolDefinition, error)
	GetCustomTool(name string) (pebblestore.AgentCustomToolDefinition, bool, error)
	GetCustomToolForAccount(accountScopeID, name string) (pebblestore.AgentCustomToolDefinition, bool, error)
	PutCustomTool(definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error)
	PutCustomToolForAccount(accountScopeID string, definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error)
	DeleteCustomTool(name string) (bool, error)
	DeleteCustomToolForAccount(accountScopeID, name string) (bool, error)
	AssignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	AssignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	UnassignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	UnassignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	GetProfile(name string) (pebblestore.AgentProfile, bool, error)
	GetProfileForAccount(accountScopeID, name string) (pebblestore.AgentProfile, bool, error)
	PreviewUpsert(input agentruntime.UpsertInput) (agentruntime.PreviewUpsertResult, error)
	PreviewUpsertForAccount(accountScopeID string, input agentruntime.UpsertInput) (agentruntime.PreviewUpsertResult, error)
	Upsert(input agentruntime.UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	UpsertForAccount(accountScopeID string, input agentruntime.UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error)
	ActivatePrimary(name string) (string, int64, *pebblestore.EventEnvelope, error)
	ActivatePrimaryForAccount(accountScopeID, name string) (string, int64, *pebblestore.EventEnvelope, error)
	Delete(name string) (agentruntime.DeleteResult, int64, *pebblestore.EventEnvelope, error)
	DeleteForAccount(accountScopeID, name string) (agentruntime.DeleteResult, int64, *pebblestore.EventEnvelope, error)
	SetActiveSubagent(purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
	SetActiveSubagentForAccount(accountScopeID, purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
	DeleteActiveSubagent(purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
	DeleteActiveSubagentForAccount(accountScopeID, purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error)
}

type manageActionService interface {
	List(actionruntime.Scope) ([]pebblestore.WorkspaceAction, error)
	Get(actionruntime.Scope, string) (pebblestore.WorkspaceAction, bool, error)
	Create(actionruntime.CreateInput) (pebblestore.WorkspaceAction, error)
	Update(actionruntime.UpdateInput) (pebblestore.WorkspaceAction, error)
	Delete(actionruntime.Scope, string) (bool, error)
	Reorder(actionruntime.Scope, []string) ([]pebblestore.WorkspaceAction, error)
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

// ManagedImageGenerationService is the provider-neutral in-memory generation
// boundary used by manage_artifact. The runtime resolves the account setting;
// AI-authored calls never select providers or models.
type ManagedImageGenerationService interface {
	ManagedImageCapabilities(selectionID string) (imagegen.ManagedImageCapabilities, error)
	GenerateManagedImage(context.Context, imagegen.ManagedGenerateRequest) (imagegen.ManagedImage, error)
}

type manageThemeUISettingsService interface {
	Get() (uisettings.UISettings, error)
	GetForAccount(accountScopeID string) (uisettings.UISettings, error)
	Set(settings uisettings.UISettings) (uisettings.UISettings, error)
	SetForAccount(accountScopeID string, settings uisettings.UISettings) (uisettings.UISettings, error)
}

type manageThemeWorkspaceService interface {
	SetThemeIDForPrincipal(principal identity.Principal, path, themeID string) (workspaceruntime.Resolution, error)
	ScopeForPathForPrincipal(principal identity.Principal, path string) (workspaceruntime.Scope, error)
	ListKnownForPrincipal(principal identity.Principal, limit int) ([]workspaceruntime.Entry, error)
}

type manageWorktreeSessionRecord = pebblestore.SessionSnapshot

type manageWorktreeWorkspaceSessionList = pebblestore.WorkspaceSessionList

type manageWorktreeWorkspaceBinding = workspaceruntime.Resolution

type manageWorktreeWorkspaceScopeInfo = workspaceruntime.Scope

type manageWorktreeWorkspaceEntry = workspaceruntime.Entry

type manageWorktreeConfig = worktreeruntime.Config

type workspaceScopeContextKey struct{}

func WithWorkspaceScope(parent context.Context, scope WorkspaceScope) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	normalized := normalizeWorkspaceScope(scope.PrimaryPath, scope.Roots)
	normalized.SessionID = strings.TrimSpace(scope.SessionID)
	normalized.Principal = scope.Principal
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
		// Some control-plane tools deliberately omit path roots and authorize from
		// the durable principal/session identity instead. Preserve that identity
		// while retaining the caller's normalized path scope.
		scope.SessionID = strings.TrimSpace(override.SessionID)
		scope.Principal = override.Principal
		return scope
	}
	normalized := normalizeWorkspaceScope(override.PrimaryPath, override.Roots)
	normalized.SessionID = strings.TrimSpace(override.SessionID)
	normalized.Principal = override.Principal
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

type MediaPayload struct {
	AssetID      string
	Modality     string
	MIMEType     string
	FileType     string
	DigestSHA256 string
	Size         int64
	Bytes        []byte
}

type Result struct {
	CallID     string        `json:"call_id"`
	Name       string        `json:"name"`
	Output     string        `json:"output"`
	Error      string        `json:"error,omitempty"`
	DurationMS int64         `json:"duration_ms"`
	Media      *MediaPayload `json:"-"`
}

type Progress struct {
	Stage    string         `json:"stage"`
	Output   string         `json:"output,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type BatchResultHandler func(index int, call Call, result Result)
type BatchProgressHandler func(index int, call Call, progress Progress)

func NewRuntime(maxParallel int) *Runtime {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return &Runtime{
		maxParallel:       maxParallel,
		searchCoordinator: NewSearchCoordinator(defaultSearchResidentRoots),
		httpClient: &http.Client{
			Timeout: maxWebFetchTimeout + 5*time.Second,
		},
	}
}

func (r *Runtime) LongSessionSnapshot() map[string]any {
	if r == nil {
		return nil
	}
	snapshot := r.searchCoordinator.Snapshot()
	return map[string]any{
		"max_parallel":             r.maxParallel,
		"search_resident_roots":    snapshot.ResidentRoots,
		"search_inflight":          snapshot.Inflight,
		"search_pending_calls":     snapshot.PendingCalls,
		"search_native_executions": snapshot.NativeExecutions,
		"search_worker_restarts":   snapshot.WorkerRestarts,
		"search_queue_wait_ms":     snapshot.QueueWait.Milliseconds(),
	}
}

func (r *Runtime) SetArtifactRegistry(registry *artifact.Registry) {
	if r != nil {
		r.artifacts = registry
	}
}

func (r *Runtime) ArtifactRegistry() *artifact.Registry {
	if r == nil {
		return nil
	}
	return r.artifacts
}

func (r *Runtime) SetArtifactAuthority(authority ArtifactAuthority) {
	if r != nil {
		r.artifactAuthority = authority
	}
}

func (r *Runtime) ArtifactAuthority() ArtifactAuthority {
	if r == nil {
		return nil
	}
	return r.artifactAuthority
}

// GenerateManagedImageArtifact is the trusted orchestration entrypoint for
// direct image swarms. It reuses the canonical account image setting and
// artifact finalization path without creating an AI worker session.
func (r *Runtime) GenerateManagedImageArtifact(ctx context.Context, scope WorkspaceScope, callID, prompt string, run ArtifactRunContext, source *pebblestore.SessionArtifactSelectionReference) (string, error) {
	if r == nil {
		return "", errors.New("manage_artifact runtime is not configured")
	}
	ctx = WithWorkspaceScope(ctx, scope)
	ctx = WithArtifactRunContext(ctx, run)
	args := map[string]any{"action": "generate_image", "prompt": strings.TrimSpace(prompt)}
	if source != nil {
		args["source_session_id"] = strings.TrimSpace(source.SessionID)
		args["source_collection_id"] = strings.TrimSpace(source.CollectionID)
		args["source_variant_id"] = strings.TrimSpace(source.VariantID)
		args["source_event_seq"] = int(source.EventSeq)
	}
	if capabilities, err := r.managedImageCapabilities(scope.Principal.AccountScopeID); err != nil {
		return "", err
	} else if capabilities.CapabilityToken != "" {
		args["capability_token"] = capabilities.CapabilityToken
	}
	return r.executeManageArtifact(ctx, scope, callID, args)
}

func (r *Runtime) SetManagedImageGenerationService(service ManagedImageGenerationService) {
	if r != nil {
		r.imageGeneration = service
	}
}

func (r *Runtime) SetManageVideoServices(service manageVideoService, sources *videosource.Service) {
	if r != nil {
		r.video = service
		r.videoSources = sources
	}
}

func (r *Runtime) SetManageVideoPipelineServices(service manageVideoService, sources *videosource.Service, projects manageVideoProjectService, render manageVideoRenderService) {
	if r != nil {
		r.video = service
		r.videoSources = sources
		r.videoProjects = projects
		r.videoRender = render
	}
}

func (r *Runtime) SetManageVideoProjectServices(projects manageVideoProjectService, render manageVideoRenderService) {
	if r != nil {
		r.videoProjects = projects
		r.videoRender = render
	}
}

// SetManageVideoService remains for focused tests and compatibility wiring.
func (r *Runtime) SetManageVideoService(service manageVideoService) {
	r.SetManageVideoServices(service, nil)
}

func (r *Runtime) SetExaConfigResolver(resolver func(context.Context) (ExaRuntimeConfig, error)) {
	if r == nil {
		return
	}
	r.exaConfigResolver = resolver
}

func (r *Runtime) SetManageSessionService(sessions manageSessionService) {
	if r != nil {
		r.sessions = sessions
	}
}

// SetManageSessionRealtimePublisher wires durable manage-sessions mutations into
// the same V3 realtime wake path used by HTTP session mutations.
func (r *Runtime) SetManageSessionRealtimePublisher(publish func(pebblestore.V3RealtimeOutboxRecord) error) {
	if r != nil {
		r.publishSessionOutbox = publish
	}
}

func (r *Runtime) SetManageWorktreeServices(sessions manageSessionService, workspace manageWorktreeWorkspaceService, worktrees manageWorktreeConfigService) {
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
	if service, ok := agents.(manageOrchestrationPolicyService); ok {
		r.orchestration = service
	}
}

func (r *Runtime) SetManageOrchestrationPolicyService(service manageOrchestrationPolicyService) {
	if r != nil {
		r.orchestration = service
	}
}

func (r *Runtime) SetManageActionService(actions manageActionService) {
	if r != nil {
		r.actions = actions
	}
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
			Name:        "edit_pending_plan",
			Description: "Edit the pending plan proposal bound to the reserved Plan sidechat using optimistic concurrency. Pass document as a native structured JSON object, never as serialized/quoted JSON text. Start from the authoritative attached document and preserve its current title unless the user explicitly requests a rename.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"expected_revision": map[string]any{"type": "integer", "description": "Current pending proposal revision as an integer, not a quoted string"},
					"document":          map[string]any{"type": "object", "description": "Complete replacement structured plan supplied directly as a native JSON object; do not pass JSON text, quoted/stringified JSON, markdown, or a wrapper string. Copy the current title from the authoritative attached document unless the user explicitly requests a rename"},
				},
				"required":             []string{"expected_revision", "document"},
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
			Description: "Execute a shell command in the current workspace directory. Summarize routine intent in one direct line; use more explanation items only for distinct material effects. Always name consequential environmental changes and whether the user should pay special attention.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "Shell command to execute"},
					"explanation": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Ordered plain-English effects. Prefer one concise, human-scannable sentence for routine commands; do not narrate obvious shell mechanics, output capture, exit status, working directory, lack of source edits, or generic artifacts. Use multiple concise items only for distinct material effects. Name concrete filesystem mutations, processes, listeners, ports, network exposure, privileges, destructive actions, and other consequential changes when present.",
					},
					"category":   map[string]any{"type": "string", "enum": []string{"read", "write", "update", "delete"}, "description": "Overall effect category: read only observes state; write creates new state, resources, or processes; update is a non-removal in-place mutation; delete removes state and always requires critical=true. Use the highest-impact applicable category."},
					"critical":   map[string]any{"type": "boolean", "description": "Set true when the user should pay special attention before execution, including public listeners or network exposure, destructive or privileged operations, security-sensitive changes, or other unusually consequential effects."},
					"timeout_ms": map[string]any{"type": "integer", "description": "Timeout in milliseconds (default 120000, max 1800000)"},
				},
				"required":             []string{"command", "explanation", "category", "critical"},
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
			Description: "Canonical FFF content/symbol search. Use for text inside files: exact symbols, error strings, config keys, or short natural fragments. Supports literal, regex, and fuzzy content modes, context lines, file-offset pagination, per-file match caps, include globs, tight path scoping, and multi-query batching. For path/directory discovery, use find instead.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":              map[string]any{"type": "string", "description": "Legacy single-query alias. Use an exact symbol, error string, config key, or short natural fragment."},
					"query":                map[string]any{"type": "string", "description": "Single content search query. Preferred over `pattern` for new callers."},
					"queries":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional multi-query batch for one search call. Use this for parallel/multi-symbol content search within the same path/include scope."},
					"path":                 map[string]any{"type": "string", "description": "Search root directory or file (absolute or workspace-relative). Keep this as narrow as possible for model-readable results. For multiple roots, prefer `paths`."},
					"paths":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional explicit batch of search root directories/files. Use instead of stuffing multiple roots into `path`."},
					"include":              map[string]any{"type": "string", "description": "Optional file include glob such as `*.go`. This is the canonical way to scope content search to file types."},
					"content_mode":         map[string]any{"type": "string", "description": "Content matching mode: literal (default), regex, or fuzzy. Prefer literal for exact code/symbol strings; use regex only when pattern syntax is needed."},
					"before_context":       map[string]any{"type": "integer", "description": "Context lines before each content match (default 0)."},
					"after_context":        map[string]any{"type": "integer", "description": "Context lines after each content match (default 5 for definition-aware search)."},
					"file_offset":          map[string]any{"type": "integer", "description": "File-based pagination offset returned as next_file_offset; use to continue a truncated content search."},
					"max_matches_per_file": map[string]any{"type": "integer", "description": "Maximum content matches per file (0 = unlimited). Use to keep broad searches readable."},
					"max_results":          map[string]any{"type": "integer", "description": "Maximum merged results to return (default 100, max 4000). If results truncate, narrow path/include/query scope or continue with file_offset."},
					"timeout_ms":           map[string]any{"type": "integer", "description": "Search timeout in milliseconds (default 8000, max 45000). Used for FFF scan wait and grep time budget."},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "find",
			Description: "FFF-backed path discovery for files, directories, mixed file+directory results, or glob-only file-pattern lookup. Use find when you need candidate paths before read/edit, not content matches. Supports multi-query batching, path/include scoping, pagination, and compact typed output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "Single fuzzy path/directory query or glob pattern when mode=glob."},
					"queries":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional multi-query batch for one find call within the same path/include scope."},
					"mode":        map[string]any{"type": "string", "description": "Discovery mode: files (default), directories, mixed, or glob. Use mixed when either files or directories may be relevant."},
					"path":        map[string]any{"type": "string", "description": "Discovery root directory (absolute or workspace-relative). Keep narrow when possible. For multiple roots, prefer `paths`."},
					"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional explicit batch of discovery roots."},
					"include":     map[string]any{"type": "string", "description": "Optional file include glob for files/mixed results, such as `*.go`. Directory matches are not filtered by include."},
					"page_index":  map[string]any{"type": "integer", "description": "Result page index for FFF file/directory/mixed discovery (default 0)."},
					"max_results": map[string]any{"type": "integer", "description": "Maximum merged path results to return (default 100, max 4000). If truncated, narrow scope/query or increment page_index."},
					"timeout_ms":  map[string]any{"type": "integer", "description": "Discovery timeout in milliseconds (default 8000, max 45000)."},
				},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "websearch",
			Description: "Run Exa /search using an active account-scoped Exa API key; sends queries and optional selected URLs to Exa and returns results to the agent/model context without using a browser profile",
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
			Description: "Fetch selected URLs through Exa /contents using an active account-scoped Exa API key; sends those URLs to Exa and returns results to the agent/model context without using a browser profile",
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
			Description: "Download full URL contents via Exa /contents using an active account-scoped Exa API key; sends selected URLs to Exa without using a browser profile, and omitted output_dir uses the managed private workspace cache",
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
						"description": "Workspace-relative or scoped absolute output directory. When omitted, uses the managed private workspace cache downloads bucket.",
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
			Description: "Request user input through the permission interaction flow. Supply at least two concrete choices per question. The backend always appends a protected option labeled exactly \"Custom response\" so the user can freely type a different answer. Never add a custom/other/input-box option; returned answers may not match any supplied choice.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":    map[string]any{"type": "string", "description": "Optional modal title"},
					"context":  map[string]any{"type": "string", "description": "Optional context shown above questions"},
					"question": map[string]any{"type": "string", "description": "Single-question prompt shown to the user"},
					"options": map[string]any{
						"type":        "array",
						"minItems":    2,
						"description": "At least two concrete suggested answers for the single-question path. Do not add a custom/other/input-box option; the backend appends \"Custom response\" automatically.",
						"items": map[string]any{
							"oneOf": []any{
								map[string]any{"type": "string"},
								map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string"},
										"value":       map[string]any{"type": "string"},
										"description": map[string]any{"type": "string"},
									},
									"required":             []string{},
									"additionalProperties": false,
								},
							},
						},
					},
					"questions": map[string]any{
						"type":        "array",
						"minItems":    1,
						"description": "Structured questions. Every question needs at least two concrete choices; the backend separately appends a protected \"Custom response\" option.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":       map[string]any{"type": "string"},
								"header":   map[string]any{"type": "string"},
								"question": map[string]any{"type": "string"},
								"required": map[string]any{"type": "boolean"},
								"options": map[string]any{
									"type":        "array",
									"minItems":    2,
									"description": "At least two concrete suggested answers. Do not add a custom/other/input-box option; the backend appends \"Custom response\" automatically.",
									"items": map[string]any{
										"oneOf": []any{
											map[string]any{"type": "string"},
											map[string]any{
												"type": "object",
												"properties": map[string]any{
													"label":       map[string]any{"type": "string"},
													"value":       map[string]any{"type": "string"},
													"description": map[string]any{"type": "string"},
												},
												"required":             []string{},
												"additionalProperties": false,
											},
										},
									},
								},
							},
							"required":             []string{"question", "options"},
							"additionalProperties": false,
						},
					},
				},
				"anyOf": []any{
					map[string]any{"required": []string{"question", "options"}},
					map[string]any{"required": []string{"questions"}},
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
			Description: "Inspect and manage workspace skills under .agents/skills; call with {\"action\":\"inspect\"} for usage details; supports inspect/list/get/create/update/delete; create applies immediately, while update/delete return approval-ready previews unless confirm=true",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":            map[string]any{"type": "string", "description": "Action: inspect|list|get|create|update|delete"},
					"skill":             map[string]any{"type": "string", "description": "Skill name or canonical id"},
					"name":              map[string]any{"type": "string", "description": "Skill display name; used for create/update when skill is omitted"},
					"content":           map[string]any{"type": "string", "description": "Proposed SKILL.md content for create/update"},
					"confirm":           map[string]any{"type": "boolean", "description": "Set true after approval to apply the proposed change to disk"},
					"expected_revision": map[string]any{"type": "string", "description": "Revision token returned by the approved proposal; required when confirm=true"},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "manage-agent",
			Description: "Inspect and manage saved agents and custom tools; call with {\"action\":\"inspect\"} first for usage details, including agent mode guidance, execution modes, available tool bundles/presets, and concrete tool grants; if the user has not specified which agent type/mode or execution mode to create, clarify before creating; create/update should set explicit runtime_mode (plan_auto, read, or readwrite), choose the least-privilege preset first, and only add explicit per-tool overrides from the advertised inventory when necessary; mutating actions return approval-ready before/after previews unless confirm=true",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":    map[string]any{"type": "string", "description": "Action: inspect|list|get_orchestration_policy|update_orchestration_policy|get|create|update|delete|activate_primary|set_active_subagent|remove_active_subagent|create_custom_tool|update_custom_tool|delete_custom_tool|assign_custom_tool|unassign_custom_tool"},
					"agent":     map[string]any{"type": "string", "description": "Agent name or canonical id"},
					"name":      map[string]any{"type": "string", "description": "Agent display name; used for create/update when agent is omitted"},
					"tool_name": map[string]any{"type": "string", "description": "Custom tool name for delete/assign/unassign actions"},
					"content": map[string]any{
						"description": "Structured agent profile, orchestration policy, custom tool, or assignment payload. Prefer an object; legacy JSON-object strings are still accepted.",
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
									"mode":                   map[string]any{"type": "string", "description": "primary|subagent|background. Clarify if unspecified: primary is user-selectable in Desktop/TUI; subagent is usable by primary agents for delegation and also user-selectable in Desktop/TUI; background is reserved for non-interactive system work and does not appear in the Desktop/TUI selector."},
									"description":            map[string]any{"type": "string"},
									"provider":               map[string]any{"type": "string"},
									"model":                  map[string]any{"type": "string"},
									"thinking":               map[string]any{"type": "string"},
									"prompt":                 map[string]any{"type": "string"},
									"runtime_mode":           map[string]any{"type": "string", "description": "Authoritative execution mode: plan_auto|read|readwrite"},
									"default_session_mode":   map[string]any{"type": "string", "description": "Default mode for new sessions: plan|auto"},
									"execution_setting":      map[string]any{"type": "string", "description": "legacy alias for read|readwrite direct runtime only; prefer runtime_mode"},
									"exit_plan_mode_enabled": map[string]any{"type": "boolean", "description": "Derived from runtime_mode: true for plan_auto, false for read/readwrite"},
									"enabled":                map[string]any{"type": "boolean"},
									"tool_scope":             manageAgentToolScopeSchema(),
									"tool_contract":          manageAgentToolContractSchema(),
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
			Description: "Inspect and manage builtin/custom themes. create_batch previews or creates up to 8 themes in one confirmation-safe call for comparison, with optional apply_theme_id selection. Create requires theme_id (or content.id), name (or content.name), and content.palette (or base_theme_id for inherited palette). Mutating actions preview unless confirm=true. create/update can atomically apply with apply_to=workspace|account|global|none; workspace apply defaults to the active workspace when available.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: inspect|list|get|create|create_batch|update|delete|set"},
					"theme_id":       map[string]any{"type": "string", "description": "Theme id for get/update/delete/set/create; create also accepts content.id"},
					"name":           map[string]any{"type": "string", "description": "Theme display name for create/update; create also accepts content.name"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path for workspace-scoped operations; defaults to current/active workspace scope when applying to workspace"},
					"apply_to":       map[string]any{"type": "string", "description": "Optional apply target for create/update/set: workspace|account|global|none. Create defaults to workspace when an active workspace exists; set defaults to active workspace when available; account/global changes account settings."},
					"base_theme_id":  map[string]any{"type": "string", "description": "Optional builtin/custom base theme id for create/update; allows inherited palette when content.palette is omitted"},
					"apply_theme_id": map[string]any{"type": "string", "description": "For create_batch only, optional id from themes to apply after all themes are created; requires apply_to=workspace|account|global"},
					"themes": map[string]any{
						"type":        "array",
						"description": "For create_batch, 1 to 8 theme payloads. Each item requires id or theme_id, name, and palette (or base_theme_id).",
						"minItems":    1,
						"maxItems":    8,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":            map[string]any{"type": "string"},
								"theme_id":      map[string]any{"type": "string"},
								"name":          map[string]any{"type": "string"},
								"base_theme_id": map[string]any{"type": "string"},
								"palette":       manageThemePaletteSchema(),
							},
							"additionalProperties": false,
						},
					},
					"confirm": map[string]any{"type": "boolean", "description": "Set true after approval to apply the proposed change; create_batch applies all selected preview items atomically to saved settings"},
					"content": map[string]any{
						"type":        "object",
						"description": "Theme payload for create/update. For create provide id or top-level theme_id, name or top-level name, and palette object unless base_theme_id supplies an inherited palette.",
						"properties": map[string]any{
							"id":            map[string]any{"type": "string"},
							"theme_id":      map[string]any{"type": "string"},
							"name":          map[string]any{"type": "string"},
							"base_theme_id": map[string]any{"type": "string"},
							"palette":       manageThemePaletteSchema(),
						},
						"additionalProperties": false,
					},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		manageSessionsDefinition(),
		{
			Type:        "function",
			Name:        "manage-worktree",
			Description: "Recall durable Coder child lineage or atomically integrate a committed child batch. For large waves, pass action=integrate with task_call_id so the tool selects every Coder child from that durable task call without copying session IDs. Explicit session_ids remain supported. The tool validates the full selection, preflights the complete ordered stack, applies automatically without confirmation, and propagates errors without partially mutating the parent.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: inspect|list|recall|integrate"},
					"session_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Explicit selected Coder child session ids from durable current-parent lineage; mutually exclusive with task_call_id"},
					"task_call_id":   map[string]any{"type": "string", "description": "Durable parent task call to recall or integrate as one complete Coder wave; mutually exclusive with session_ids for integrate"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path; defaults to current/active workspace scope"},
					"branch_name":    map[string]any{"type": "string", "description": "Optional worktree branch family/prefix override such as agent or foo"},
					"limit":          map[string]any{"type": "integer", "description": "Page size for returned children (default 25, max 100)"},
					"cursor":         map[string]any{"type": "integer", "description": "0-based result offset for pagination"},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		manageActionsDefinition(),
		manageArtifactDefinition(),
		manageVideoDefinition(),
		{
			Type:        "function",
			Name:        "manage_todos",
			Description: "Manage user-owned workspace todo items and summaries. Supports list/create/update/delete/reorder/in_progress actions and atomic batch mutations for regular user todo lists with priorities, tags, groups, in-progress state, and ordering. Do not use this for agent self-tracking, execution checklists, or checkpoint lifecycle state; use plan_manage for agent progress.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: list|summary|create|update|delete|delete_done|delete_all|reorder|in_progress|batch"},
					"workspace_path": map[string]any{"type": "string", "description": "Optional workspace path; defaults to current/active workspace scope"},
					"owner_kind":     map[string]any{"type": "string", "description": "Optional owner kind filter/scope: user|agent. Agents should use user for user todo requests; agent self-tracking belongs in plan_manage, not manage_todos."},
					"id":             map[string]any{"type": "string", "description": "Todo id for update/delete/in_progress"},
					"text":           map[string]any{"type": "string", "description": "Todo text"},
					"done":           map[string]any{"type": "boolean", "description": "Completed state"},
					"priority":       map[string]any{"type": "string", "description": "Priority: low|medium|high|urgent"},
					"group":          map[string]any{"type": "string", "description": "Optional grouping label"},
					"tags":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tags"},
					"in_progress":    map[string]any{"type": "boolean", "description": "In-progress state"},
					"session_id":     map[string]any{"type": "string", "description": "Optional conversation/session id for existing todo records; do not use for new agent self-tracking, which belongs in plan_manage."},
					"parent_id":      map[string]any{"type": "string", "description": "Optional parent todo id for existing todo records; do not use for new agent self-tracking, which belongs in plan_manage."},
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
			Name:        "compact",
			Description: "Continue plan-mode research in fresh provider context after a context-pressure warning. Supply a concise handoff that preserves the active goal, decisions, evidence, relevant files, and immediate next action. This control is exposed only while the backend is awaiting the required choice between compact and exit_plan_mode.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"handoff": map[string]any{"type": "string", "minLength": 1, "description": "Concise research handoff for the fresh-context continuation"},
				},
				"required":             []string{"handoff"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "exit_plan_mode",
			Description: "Submit the final structured executable plan for approval so the session can leave plan mode and continue execution. document is required and must contain at least one complete checkpoint; markdown plan text is display/export only and can never substitute for document. The backend revalidates the exact approved document before persistence, mode changes, or execution.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":                  map[string]any{"type": "string", "description": "Final plan title. Optional when document.title is provided."},
					"plan":                   map[string]any{"type": "string", "description": "Optional markdown/display text for export only; document is canonical. Include any last display-text updates here instead of first calling plan_manage save."},
					"document":               sessionExecutablePlanDocumentToolSchema(),
					"plan_id":                map[string]any{"type": "string", "description": "Existing active plan id to update and submit. Optional; when omitted, the current active plan is reused if one exists."},
					"id":                     map[string]any{"type": "string", "description": "Alias for plan_id."},
					"continuation_policy":    map[string]any{"type": "string", "description": "Recommended checkpoint continuation after approval: review_each_checkpoint/pause or automatic/continue_automatically. The user approval remains authoritative."},
					"continue_automatically": map[string]any{"type": "boolean", "description": "Recommended checkpoint continuation after approval: true auto-continues completed checkpoints; false pauses for review. The user approval remains authoritative."},
				},
				"required":             []string{"document"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "plan_manage",
			Description: "Manage the canonical structured session plan, agent execution progress, and typed plan lifecycle changes. document is authoritative; markdown is display-only. In auto mode with no active plan, start_session_checkpoint atomically creates and starts one bounded checkpoint; use request_new_plan for broad, uncertain, high-risk, multi-phase, or approval-gated work. From a trusted parent provider turn with an active plan, transition_checkpoint_boundary is the only action that appends one self-contained checkpoint and assigns it to the already-current run; its successful result preserves context and continues that provider turn. The retired request_followup_checkpoint action and all aliases are rejected. Do not call transition_checkpoint_boundary from a checkpoint-owned run. Classify feedback by contract impact: guidance needs no mutation; bounded additive work uses add_subtask; a superseded checklist uses replace_subtasks; invalidated objectives use restart_checkpoint; independently shippable work from a parent turn uses transition_checkpoint_boundary; future-plan rewrites use amend_plan; whole-plan replacement uses request_new_plan. Never use manage_todos for agent progress. Put terminal report, changed_files, validation, and result on the terminal checkpoint action.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":                     map[string]any{"type": "string", "description": "Action: list|get|get-active|save|patch/update_section/update_info/upsert_checkpoint/update_checkpoint/approve_and_start/restart_checkpoint/rewind_to_checkpoint/resolve_blocked_checkpoint/start_session_checkpoint/transition_checkpoint_boundary/amend_plan/request_new_plan/start_checkpoint/continue_checkpoint/complete_checkpoint/checkpoint_outcome/mark_needs_review/mark_blocked/mark_failed/remove_checkpoint/reorder_checkpoints/set_active_checkpoint/add_subtask/replace_subtasks/update_subtask/remove_subtask/reorder_subtasks/focus_subtask/complete_subtask/set-active/new/history. transition_checkpoint_boundary is the sole active-plan checkpoint-boundary action and is valid only in a trusted parent provider turn. It copies the self-contained change request into a new checkpoint, assigns checkpoint ownership to the already-current run, and continues the parent turn without allocating another run. request_followup_checkpoint, request_changes, and all follow-up aliases are retired and rejected. Checkpoint-owned runs must finish their selected objective and cannot transition the boundary."},
					"continuation_policy":        map[string]any{"type": "string", "description": "For approve_and_start: review_each_checkpoint/pause or automatic/continue_automatically."},
					"continue_automatically":     map[string]any{"type": "boolean", "description": "For approve_and_start checkpointed execution: true auto-continues completed checkpoints; false pauses for review."},
					"plan_id":                    map[string]any{"type": "string", "description": "Plan id for get/set-active/save/patch or typed lifecycle actions. Omit on lifecycle actions to use the active plan."},
					"id":                         map[string]any{"type": "string", "description": "Alias for plan_id."},
					"title":                      map[string]any{"type": "string", "description": "Plan title for save/new. Existing title is kept when omitted on save for an existing plan."},
					"plan":                       map[string]any{"type": "string", "description": "Full markdown plan body for save. Required for full-body save; omit for patch/update_section."},
					"patch":                      map[string]any{"type": "object", "description": "Optional object form for action=patch. Fields mirror operation, section, old_text, new_text, text, checklist_item, checked, and replace_all."},
					"operation":                  map[string]any{"type": "string", "description": "Patch operation: replace_text, replace_section, append_to_section, append_text, append_checklist_item, or set_checkbox."},
					"section":                    map[string]any{"type": "string", "description": "Markdown heading text targeted by replace_section, update_section, append_to_section, or append_checklist_item."},
					"old_text":                   map[string]any{"type": "string", "description": "Exact text to find for replace_text."},
					"new_text":                   map[string]any{"type": "string", "description": "Replacement text for replace_text, replace_section, or update_section."},
					"text":                       map[string]any{"type": "string", "description": "Text to append for append_text/append_to_section, or alternate section replacement body for update_section."},
					"checklist_item":             map[string]any{"type": "string", "description": "Checklist item text for append_checklist_item or set_checkbox."},
					"checked":                    map[string]any{"type": "boolean", "description": "Desired checkbox state for set_checkbox or appended checklist items."},
					"replace_all":                map[string]any{"type": "boolean", "description": "For replace_text, replace every occurrence instead of requiring a single exact match."},
					"status":                     map[string]any{"type": "string", "description": "Optional plan status to persist on save."},
					"approval_state":             map[string]any{"type": "string", "description": "Optional approval state to persist on save."},
					"update_summary":             map[string]any{"type": "string", "description": "Short human-readable summary of what this plan update changes and why."},
					"update_scope":               map[string]any{"type": "string", "description": "Specific plan section, phase, or checkpoint affected by this update."},
					"scope":                      map[string]any{"type": "string", "description": "Alias for update_scope."},
					"update_kind":                map[string]any{"type": "string", "description": "Optional update category such as checkpoint, scope_update, or full_rewrite."},
					"base_revision":              map[string]any{"type": "integer", "description": "Required for amend_plan unless override_stale=true: current plan revision/version the amendment is based on."},
					"replace_from_checkpoint_id": map[string]any{"type": "string", "description": "For amend_plan: first future checkpoint id to replace from the proposed document; completed/current runtime state before this checkpoint is preserved."},
					"amend_future_checkpoints":   map[string]any{"type": "boolean", "description": "For amend_plan: allow replacing pending future checkpoints; when replace_from_checkpoint_id is omitted, the first pending future checkpoint is used."},
					"override_stale":             map[string]any{"type": "boolean", "description": "For amend_plan only: explicitly allow amendment when base_revision is missing or stale."},
					"checkpoint":                 map[string]any{"anyOf": []any{map[string]any{"type": "boolean"}, map[string]any{"type": "object"}}, "description": "Structured checkpoint object for checkpoint document operations, or boolean marker for checkpoint-style plan update metadata. With action=update_checkpoint/patch_checkpoint, only provided checkpoint object fields are merged and omitted fields are preserved; use fields such as status, tasks, notes, report, changed_files, and validation for agent progress/checklist tracking. With upsert_checkpoint/replace_checkpoint/set_checkpoint, the checkpoint object intentionally replaces the target checkpoint."},
					"document":                   map[string]any{"anyOf": []any{sessionPlanDocumentToolSchema(), map[string]any{"type": "string"}}, "description": "Canonical structured SessionPlanDocument. For approval-bearing actions (request_new_plan and amend_plan), an explicit object with title, info.goal, and at least one complete ordered pending checkpoint is required; markdown-only and partial documents are rejected. Draft mutation actions retain the looser document shape."},
					"document_patch":             map[string]any{"anyOf": []any{map[string]any{"type": "object"}, map[string]any{"type": "string"}}, "description": "Atomic structured document patch for modular info/checkpoint edits. update_info and update_checkpoint merge only provided fields and preserve omitted fields; replace/set operations intentionally replace. A JSON-encoded object string is also accepted for compatibility."},
					"document_operation":         map[string]any{"type": "string", "description": "Structured document operation alias, such as update_info, update_checkpoint, upsert_checkpoint, start_checkpoint, continue_checkpoint, complete_checkpoint, checkpoint_outcome, accept_checkpoint_review, restart_checkpoint, rewind_to_checkpoint, reorder_checkpoints, or set_active_checkpoint."},
					"operations":                 map[string]any{"anyOf": []any{map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, map[string]any{"type": "string"}}, "description": "Batch of structured document patch operations applied atomically. A JSON-encoded array string is also accepted for compatibility."},
					"info":                       map[string]any{"anyOf": []any{sessionPlanInfoToolSchema(), map[string]any{"type": "string"}}, "description": "Structured plan info for update_info document patches. goal, scope, context, and validation_strategy/validation are strings; decisions, relevant_files/files, constraints, assumptions, open_questions, and success_criteria are arrays of strings. update_info merges only provided fields; use replace_info/set_info only for intentional full info replacement. A JSON-encoded object string is also accepted."},
					"change_request":             map[string]any{"type": "string", "description": "Required for start_session_checkpoint and transition_checkpoint_boundary, and for restart_checkpoint when feedback invalidates the current checkpoint objective or acceptance criteria: verbatim full original user request text. A requirement-changing restart atomically replaces the checkpoint definition before fresh-context execution; localized additive refinements whose existing checklist remains valid use add_subtask, checklist-superseding same-contract feedback uses replace_subtasks, and an unchanged stale definition must not be restarted."},
					"checkpoint_title":           map[string]any{"type": "string", "description": "Proposed title for start_session_checkpoint or transition_checkpoint_boundary. Required for a requirement-changing restart_checkpoint so the replacement definition is complete."},
					"source_message_id":          map[string]any{"type": "string", "description": "Optional source message id for start_session_checkpoint or requirement-changing restart_checkpoint. transition_checkpoint_boundary uses trusted provider-turn source identity instead of model input."},
					"checkpoint_id":              map[string]any{"type": "string", "description": "Target checkpoint id for checkpoint document operations; omitted start/continue/outcome actions use the active or next checkpoint when possible. For start_session_checkpoint, optional id for the created checkpoint; defaults to cp-1."},
					"checkpoint_order":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Full checkpoint id order for reorder_checkpoints."},
					"subtask":                    map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string", "minLength": 1}, "status": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"}, "result": map[string]any{"type": "string"}, "order": map[string]any{"type": "integer"}}, "additionalProperties": false, "description": "For add_subtask, pass a JSON object with a non-empty title in the same call, for example: {\"action\":\"add_subtask\",\"checkpoint_id\":\"cp-1\",\"subtask\":{\"title\":\"Measure Swarm hosting capacity\"}}. Do not put title at the top level, pass subtask as bare text, or issue an incomplete format-probing call. add_subtask is only for a bounded refinement when the existing checklist remains valid; it keeps the checkpoint boundary and attempt history, and must not clear blocked or failed state."},
					"subtasks":                   map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Complete authoritative subtask list for replace_subtasks. Omitted stale subtasks are removed atomically, one actionable subtask is focused, and the checkpoint contract and attempt history are preserved."},
					"subtask_id":                 map[string]any{"type": "string", "description": "Stable subtask id for update/remove/focus/complete operations. For complete_subtask, omit when using subtask_ids."},
					"subtask_ids":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For complete_subtask, stable ids of multiple genuinely completed subtasks to transition atomically in one call."},
					"subtask_order":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Full subtask id order for reorder_subtasks."},
					"complete_checkpoint":        map[string]any{"type": "boolean", "description": "For complete_subtask only: also complete the current checkpoint atomically when all work and acceptance criteria are done. Include the usual terminal report, changed_files, validation, result, and run ownership fields. If this is the final checkpoint, include handoff_overview and use the structured handoff as the only user-visible completion."},
					"active_checkpoint_id":       map[string]any{"type": "string", "description": "Checkpoint id to mark active for set_active_checkpoint."},
					"active_checkpoint":          map[string]any{"type": "string", "description": "Alias for active_checkpoint_id."},
					"notes":                      map[string]any{"type": "string", "description": "Checkpoint notes for update/complete operations. For start_session_checkpoint, transition_checkpoint_boundary, or a requirement-changing restart_checkpoint, use this for self-contained replacement/handoff context, constraints, relevant files, and validation expectations."},
					"tasks":                      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Proposed checkpoint tasks. Required and complete for a requirement-changing restart_checkpoint; for checkpoint-boundary transitions include enough concrete steps to preserve material request parts."},
					"acceptance_criteria":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Proposed checkpoint acceptance criteria. Required and complete for a requirement-changing restart_checkpoint; include clear completion checks and validation expectations."},
					"artifacts":                  map[string]any{"type": "array", "items": sessionPlanArtifactToolSchema(), "description": "Workspace-relative artifact references for checkpoint creation/restart or terminal outcomes. On completion, include only concrete deliverables created by this checkpoint; viewable role=deliverable artifacts may appear in the final handoff gallery."},
					"report":                     map[string]any{"type": "string", "description": "Checkpoint report for update/complete checkpoint operations."},
					"result":                     map[string]any{"type": "string", "description": "Checkpoint result for update/complete checkpoint operations."},
					"reviewed_at":                map[string]any{"type": "integer", "description": "Optional review/resolution timestamp for accept_checkpoint or resolve_blocked_checkpoint."},
					"start_next":                 map[string]any{"type": "boolean", "description": "For resolve_blocked_checkpoint: after confirming resolution, immediately resume the same checkpoint in a fresh provider run. This never completes it or selects a later checkpoint."},
					"continue_next":              map[string]any{"type": "boolean", "description": "Compatibility alias for start_next; resumes the same resolved checkpoint."},
					"changed_files":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Files changed while completing/updating a checkpoint."},
					"validation":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Validation evidence for checkpoint updates."},
					"recommendation":             map[string]any{"type": "object", "description": "Single final-review recommendation for terminal checkpoint outcomes: decision ship/change/revert/defer, action, short reason, and action_state taken/ready/needs_approval."},
					"handoff_title":              map[string]any{"type": "string", "description": "Optional concise title for a terminal final or blocked handoff card (maximum 120 characters). For mark_blocked, name the blocker or blocked outcome plainly."},
					"handoff_overview":           map[string]any{"type": "string", "description": "Required concise overview for final checkpoint completion, mark_blocked compact handoffs, and whenever any handoff field is supplied (maximum 600 characters). For mark_blocked, identify the external blocker and why it prevents progress; keep full evidence in report/result/validation."},
					"impact_bullets":             map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "string"}, "description": "Up to three concise impact bullets for a final or blocked handoff. For mark_blocked, lead with the exact resolution required and optionally note unchanged/safe state."},
					"copyable_code_blocks":       map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}, "language": map[string]any{"type": "string"}, "code": map[string]any{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false}, "description": "Up to three optional display-only code or command blocks for final and blocked handoffs. Use when the user needs exact text to copy, such as a run command. label and language are optional; code is required. Clients expose a copy affordance and never execute the text automatically."},
					"suggested_prompts":          map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}, "required": []string{"label", "prompt"}, "additionalProperties": false}, "description": "Up to three inert next-step label/prompt objects that clients may send only as ordinary V3 user chat messages. Useful for final and blocked handoffs, including a resume prompt after the blocker is resolved."},
					"pull_request_url":           map[string]any{"type": "string", "description": "Optional public GitHub pull-request URL for a final handoff. Must exactly use https://github.com/<owner>/<repository>/pull/<number>; clients may expose it as a safe external link and must omit the action when absent or invalid."},
					"activate":                   map[string]any{"type": "boolean", "description": "Whether the saved/new plan becomes the active plan (default true)."},
					"override":                   map[string]any{"type": "boolean", "description": "Legacy action=new field. Replacement is rejected; use request_new_plan with the current plan_id and a complete structured document."},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
		},
		{
			Type:        "function",
			Name:        "task",
			Description: "Delegate normal heavy work through explicit Finder, Coder, or Designer launches, optionally submit one staged Task Program, or set mode=swarm for an Iteration Swarm. Coder and Designer swarms launch Router-hydrated workers. Image swarms use a distinct direct format: Router independently hydrates the parent brief plus each base theme, then orchestration sends each prompt straight to the account image model without agent sessions. Idea Swarms repeat the same question directly.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"description": "Optional action. Supported: spawn (default); Task Programs use start or status. Start uses program when supplied, or the canonical task_program on the active approved checkpoint when program is omitted.",
					},
					"mode":       map[string]any{"type": "string", "enum": []string{"regular", "swarm"}, "description": "regular uses explicit dependency-ready launches or an optional staged program. swarm generates a rapid wave from agent_type and count."},
					"program_id": map[string]any{"type": "string", "description": "Stable program ID for status. A new start carries a new ID inside program.id; existing IDs cannot be continued."},
					"program":    taskProgramToolSchema(),
					"swarm_mode": map[string]any{"type": "boolean", "description": "Compatibility alias for mode=swarm. Do not combine with mode=regular."},

					"agent_type": map[string]any{"type": "string", "enum": []string{"coder", "designer", "image", "idea"}, "description": "Required for mode=swarm. image independently Router-hydrates the parent brief plus each base theme and dispatches directly to the account image model without agent sessions. Idea is tool-free and available only in swarm mode."},
					"count":      map[string]any{"type": "integer", "minimum": 1, "maximum": 256, "description": "Final worker count for mode=swarm. The account's separate swarm-mode limit controls approval-free capacity; over-limit waves follow its configured action within this absolute bound."},
					"themes":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional Coder/Designer/image seed themes; cardinality must equal count."},
					"groups":     map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer", "minimum": 1}, "instructions": map[string]any{"type": "string"}}, "required": []string{"name", "count"}, "additionalProperties": false}, "description": "Optional Coder/Designer groups. Group counts must total count and Router uses them to specialize prompts."},
					"iteration_controls": map[string]any{"type": "object", "properties": map[string]any{
						"preserve": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Parent-authored details every Router and worker must preserve."},
						"change":   map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "The only dimensions the Router may vary during this focused iteration."},
						"exclude":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Parent-authored additions or directions every Router and worker must avoid."},
					}, "required": []string{"change"}, "additionalProperties": false, "description": "Optional parent-controlled focused iteration boundary for Designer and image swarms. Router may elaborate execution detail only inside this boundary and cannot add, remove, weaken, or reinterpret the parent brief."},
					"output_contract":     map[string]any{"type": "string", "description": "Shared Coder/Designer/image swarm deliverable contract. Omit for Idea swarms."},
					"output_requirements": artifact.OutputRequirementsToolSchema(),
					"animation_profile":   artifact.AnimationProfileToolSchema(),
					"source_artifact": map[string]any{"type": "object", "properties": map[string]any{
						"session_id": map[string]any{"type": "string"}, "collection_id": map[string]any{"type": "string"},
						"variant_id": map[string]any{"type": "string"}, "event_seq": map[string]any{"type": "integer", "minimum": 1},
					}, "required": []string{"session_id", "collection_id", "variant_id", "event_seq"}, "additionalProperties": false, "description": "Optional exact ready managed image reference for direct image swarm remixing. The trusted generation boundary resolves its authenticated bounded bytes; Router receives text only."},
					"output_mode":          map[string]any{"type": "string", "enum": []string{"managed", "workspace"}, "description": "Designer and image output contract. Image is always managed. Designer defaults to managed; workspace Designer swarms require one non-overlapping owned_scope_template. Managed launches never supply destination IDs."},
					"owned_scope_template": map[string]any{"type": "string", "description": "Workspace-mode Iteration Swarm target containing exactly one {index}. Required only for output_mode=workspace Designer swarms; forbidden for managed Designer/image swarms and omitted for Idea swarms."},
					"description": map[string]any{
						"type":        "string",
						"description": "Short overall task label shown in UI.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Shared authoritative parent task. In Coder/Designer/image swarm mode, Router may elaborate execution detail but cannot add, remove, weaken, or reinterpret its requirements. In regular mode, pair it with explicit launches. In Idea swarm mode, this exact question is sent unchanged to every one-shot Idea.",
					},
					"subagent_type": map[string]any{
						"type":        "string",
						"enum":        []string{"coder", "finder", "designer"},
						"description": "Subagent type for the single-launch shorthand. Supported values: coder, finder, or designer. Designer defaults to output_mode=managed; workspace mode requires concrete owned_scope. Requires a top-level meta_prompt or role.",
					},
					"agent": map[string]any{
						"type":        "string",
						"enum":        []string{"coder", "finder", "designer"},
						"description": "Alias for subagent_type in the single-launch shorthand.",
					},
					"purpose": map[string]any{
						"type":        "string",
						"enum":        []string{"coder", "finder", "designer"},
						"description": "Alias for subagent_type in the single-launch shorthand.",
					},
					"meta_prompt": map[string]any{
						"type":        "string",
						"description": "Full instructive assignment for the single-launch shorthand; required when launches is omitted. Do not shorten this for display or put it inside prompt.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Concise cosmetic title for this child, ideally three words (for example, Backend Security Audit). The UI displays this instead of meta_prompt.",
					},
					"role": map[string]any{
						"type":        "string",
						"description": "Alias for meta_prompt in the single-launch shorthand.",
					},
					"deliverable":         map[string]any{"type": "string", "description": "Specific child output the parent will verify."},
					"concurrency_reason":  map[string]any{"type": "string", "description": "Why this scope is useful and safe to delegate now."},
					"owned_scope":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declared files, directories, or output target owned by the child. Required as a concrete clean workspace-relative path for workspace-mode Designer and forbidden for managed Designer; an omitted Coder scope safely defaults to its entire isolated worktree."},
					"dependency_evidence": map[string]any{"type": "string", "description": "Evidence that the launch does not depend on unfinished child work."},
					"launches": map[string]any{
						"type":        "array",
						"description": "The exact dependency-ready wave for one task approval. Do not paste JSON into prompt. Use Finder for distinct research deliverables, Coder for implementation scopes created from the same parent HEAD on unique sibling worktrees, and Designer only for explicit requests for multiple UI/design iterations or variants. Designer defaults to managed parent-owned artifact output; workspace mode permits read/search/find/list/write/edit with no Bash or Git and requires distinct non-overlapping output scopes. The current backend orchestration policy defines launch limits; available budget is never a target.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"subagent_type":       map[string]any{"type": "string", "enum": []string{"coder", "finder", "designer"}, "description": "Subagent type for this child. Supported values: coder, finder, or designer."},
								"agent":               map[string]any{"type": "string", "enum": []string{"coder", "finder", "designer"}, "description": "Alias for subagent_type."},
								"purpose":             map[string]any{"type": "string", "enum": []string{"coder", "finder", "designer"}, "description": "Alias for subagent_type."},
								"meta_prompt":         map[string]any{"type": "string", "description": "Required full instructive per-child assignment. Keep all scope and constraints here; this must be a field on the launch object, not text embedded in prompt."},
								"title":               map[string]any{"type": "string", "description": "Concise cosmetic title for this child, ideally three words (for example, Frontend Security Audit). The UI displays this instead of meta_prompt."},
								"role":                map[string]any{"type": "string", "description": "Alias for meta_prompt."},
								"deliverable":         map[string]any{"type": "string", "description": "Specific child output the parent will verify."},
								"concurrency_reason":  map[string]any{"type": "string", "description": "Why this scope is useful and safe to run in the current wave."},
								"output_requirements": artifact.OutputRequirementsToolSchema(),
								"animation_profile":   artifact.AnimationProfileToolSchema(),
								"output_mode":         map[string]any{"type": "string", "enum": []string{"managed", "workspace"}, "description": "Designer output contract only; defaults to managed. managed forbids owned_scope and workspace requires it. Trusted destination identity is server-owned and cannot be supplied here."},
								"owned_scope":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declared files, directories, or output target. Required for workspace-mode Designer as a concrete clean workspace-relative path and must not overlap another concurrent workspace Designer launch; forbidden for managed Designer. An omitted Coder scope defaults to its isolated worktree."},
								"dependency_evidence": map[string]any{"type": "string", "description": "Evidence that this launch does not depend on another child's unfinished work."},
							},
							"additionalProperties": false,
						},
					},
				},
				"additionalProperties": false,
			},
		},
	}
}

func taskProgramToolSchema() map[string]any {
	return taskProgramDefinitionToolSchema("Optional complete staged Task Program. The backend validates every job and stage before any reservation or child launch. Designer jobs default to managed output and omit owned_scope; explicit workspace Designer jobs set output_mode=workspace and require concrete non-overlapping workspace-relative owned_scope targets. One accepted program counts as one parent task invocation; internal capacity cohorts do not require another model-authored task call.")
}

func taskProgramDefinitionToolSchema(description string) map[string]any {
	id := map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_-]{0,63}$"}
	return map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"id":              id,
			"max_concurrency": map[string]any{"type": "integer", "minimum": 1, "description": "Optional explicit lower concurrency cap. Omit to use the number of ready jobs bounded by current account capacity and backend safety limits."},
			"stages": map[string]any{
				"type": "array", "minItems": 1,
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"id":                  id,
					"depends_on":          map[string]any{"type": "array", "items": id, "description": "Earlier stage IDs that must reach their integration barrier first."},
					"dependency_evidence": map[string]any{"type": "string", "minLength": 1, "description": "Why this stage is ready initially or what prior integrated state unlocks it."},
				}, "required": []string{"id", "dependency_evidence"}, "additionalProperties": false},
			},
			"jobs": map[string]any{
				"type": "array", "minItems": 1,
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"id":                  id,
					"stage_id":            id,
					"depends_on":          map[string]any{"type": "array", "items": id, "description": "Earlier-stage job IDs whose accepted/integrated handoffs are required."},
					"agent_type":          map[string]any{"type": "string", "enum": []string{"coder", "finder", "designer"}},
					"subagent_type":       map[string]any{"type": "string", "enum": []string{"coder", "finder", "designer"}, "description": "Alias for agent_type."},
					"meta_prompt":         map[string]any{"type": "string", "minLength": 1, "description": "Complete distinguished assignment; broad copies of the parent objective are invalid program design."},
					"title":               map[string]any{"type": "string", "minLength": 1},
					"deliverable":         map[string]any{"type": "string", "minLength": 1},
					"output_requirements": artifact.OutputRequirementsToolSchema(),
					"animation_profile":   artifact.AnimationProfileToolSchema(),
					"output_mode":         map[string]any{"type": "string", "enum": []string{"managed", "workspace"}, "description": "Designer jobs only; defaults to managed. Managed forbids owned_scope. Workspace requires concrete non-overlapping workspace-relative owned_scope targets."},
					"owned_scope":         map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "minLength": 1}, "description": "Required for Coder/Finder and workspace Designer jobs; omitted for managed Designer jobs."},
					"acceptance_criteria": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "minLength": 1}},
					"dependency_evidence": map[string]any{"type": "string", "minLength": 1},
				}, "required": []string{"id", "stage_id", "meta_prompt", "title", "deliverable", "acceptance_criteria", "dependency_evidence"}, "additionalProperties": false},
			},
		},
		"required":             []string{"id", "stages", "jobs"},
		"additionalProperties": false,
	}
}

func sessionPlanDocumentToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"status":      map[string]any{"type": "string"},
			"info":        sessionPlanInfoToolSchema(),
			"artifacts":   map[string]any{"type": "array", "items": sessionPlanArtifactToolSchema(), "description": "Workspace-relative artifact references only; file contents are not embedded."},
			"checkpoints": map[string]any{"type": "array", "items": sessionPlanCheckpointToolSchema()},
		},
		"additionalProperties": true,
	}
}

func sessionExecutablePlanDocumentToolSchema() map[string]any {
	schema := sessionPlanDocumentToolSchema()
	schema["required"] = []string{"title", "info", "checkpoints"}
	properties := schema["properties"].(map[string]any)
	properties["info"] = sessionExecutablePlanInfoToolSchema()
	properties["checkpoints"] = map[string]any{
		"type":        "array",
		"minItems":    1,
		"items":       sessionPlanCheckpointToolSchema(),
		"description": "At least one complete ordered checkpoint is required. Each checkpoint requires id, title, status, order, acceptance_criteria, and either objective or concrete tasks.",
	}
	return schema
}

func sessionExecutablePlanInfoToolSchema() map[string]any {
	schema := sessionPlanInfoToolSchema()
	schema["required"] = []string{"goal"}
	return schema
}

func sessionPlanCheckpointToolSchema() map[string]any {
	stringArray := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":                  map[string]any{"type": "string"},
			"title":               map[string]any{"type": "string"},
			"status":              map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "needs_review", "blocked", "failed"}},
			"order":               map[string]any{"type": "integer", "minimum": 1},
			"objective":           map[string]any{"type": "string"},
			"tasks":               stringArray(),
			"acceptance_criteria": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
			"task_program":        taskProgramDefinitionToolSchema("Canonical staged implementation program for this lifecycle checkpoint. Include it when dependent delegated implementation qualifies for a Task Program; the approved definition is shown to the user and delivered to the executing checkpoint."),
			"artifacts":           map[string]any{"type": "array", "items": sessionPlanArtifactToolSchema(), "description": "Workspace-relative artifacts relevant to or delivered by this checkpoint."},
			"notes":               map[string]any{"type": "string"},
		},
		"required": []string{"id", "title", "status", "order", "acceptance_criteria"},
		"anyOf": []any{
			map[string]any{"required": []string{"objective"}},
			map[string]any{"required": []string{"tasks"}},
		},
		"additionalProperties": true,
	}
}

func sessionPlanArtifactToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":          map[string]any{"type": "string", "description": "Clean workspace-relative path; absolute and workspace-escaping paths are rejected."},
			"source_ref":    map[string]any{"type": "string", "description": "Exact opaque videosrc_ reference returned by manage_video browse_source for a final video deliverable."},
			"session_id":    map[string]any{"type": "string"},
			"collection_id": map[string]any{"type": "string"},
			"variant_id":    map[string]any{"type": "string"},
			"event_seq":     map[string]any{"type": "integer", "minimum": 1},
			"label":         map[string]any{"type": "string"},
			"role":          map[string]any{"type": "string", "enum": []string{"input", "deliverable"}, "description": "input may be selectively read; deliverable must be included or linked in the user-visible assistant response."},
			"description":   map[string]any{"type": "string"},
			"media_type":    map[string]any{"type": "string"},
		},
		"anyOf": []any{
			map[string]any{"required": []string{"path"}},
			map[string]any{"required": []string{"source_ref"}},
			map[string]any{"required": []string{"session_id", "collection_id", "variant_id", "event_seq"}},
		},
		"additionalProperties": false,
	}
}

func sessionPlanInfoToolSchema() map[string]any {
	stringArray := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal":                map[string]any{"type": "string"},
			"scope":               map[string]any{"type": "string"},
			"context":             map[string]any{"type": "string"},
			"decisions":           stringArray(),
			"constraints":         stringArray(),
			"assumptions":         stringArray(),
			"open_questions":      stringArray(),
			"relevant_files":      stringArray(),
			"success_criteria":    stringArray(),
			"validation_strategy": map[string]any{"type": "string"},
		},
		"additionalProperties": true,
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
	case "find":
		return r.executeFind(ctx, scope, args)
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
	case "manage-sessions", "manage_sessions":
		return r.executeManageSessions(ctx, scope, args)
	case "manage-worktree", "manage_worktree":
		return r.executeManageWorktree(scope, args)
	case "manage-actions", "manage_actions":
		return r.executeManageActions(scope, args)
	case "manage-artifact", "manage_artifact":
		return r.executeManageArtifact(ctx, scope, call.CallID, args)
	case "manage-video", "manage_video":
		return r.executeManageVideo(ctx, scope, args)
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
	definition, ok, err := r.getCustomToolForScope(scope, name)
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
		return executeBashCommand(ctx, scope, map[string]any{}, strings.TrimSpace(definition.Command), func(chunk string) {
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
	target, err := openRootedWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	defer target.Close()
	targetPath := target.absolutePath
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

	file, err := target.open()
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
	target, err := openRootedWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	defer target.Close()
	targetPath := target.absolutePath
	if _, ok := args["content"]; !ok {
		return "", errors.New("write requires content")
	}
	content := asString(args["content"])
	appendMode := asBool(args["append"])

	if err := target.mkdirParent(); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}

	if appendMode {
		f, err := target.openMutable(os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("open file for append: %w", err)
		}
		defer f.Close()
		if _, err := io.WriteString(f, content); err != nil {
			return "", fmt.Errorf("append failed: %w", err)
		}
	} else {
		f, err := target.openMutable(os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("open file for write: %w", err)
		}
		if err := f.Truncate(0); err != nil {
			f.Close()
			return "", fmt.Errorf("write failed: truncate: %w", err)
		}
		if _, err := io.WriteString(f, content); err != nil {
			f.Close()
			return "", fmt.Errorf("write failed: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("write failed: close: %w", err)
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

// ValidateBashCallArguments enforces the canonical AI-authored Bash request contract
// before a command can reach permission or execution handling.
func ValidateBashCallArguments(arguments string) error {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &args); err != nil {
		return fmt.Errorf("bash arguments must be a JSON object: %w", err)
	}
	_, err := validateBashArguments(args)
	return err
}

func validateBashArguments(args map[string]any) (string, error) {
	command := strings.TrimSpace(asString(args["command"]))
	if command == "" {
		return "", errors.New("bash requires command")
	}

	rawExplanation, ok := args["explanation"].([]any)
	if !ok || len(rawExplanation) == 0 {
		return "", errors.New("bash requires explanation as a non-empty list of precise command effects")
	}
	for index, entry := range rawExplanation {
		text, ok := entry.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("bash explanation item %d must be a non-empty string", index+1)
		}
	}

	category, ok := args["category"].(string)
	if !ok {
		return "", errors.New("bash requires category to be one of read, write, update, or delete")
	}
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "read", "write", "update", "delete":
	default:
		return "", errors.New("bash category must be one of read, write, update, or delete")
	}
	critical, ok := args["critical"].(bool)
	if !ok {
		return "", errors.New("bash requires critical as an explicit boolean")
	}
	if category == "delete" && !critical {
		return "", errors.New("bash delete category requires critical=true")
	}
	return command, nil
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
	if len(pathspec) > 0 {
		argv = append(argv, "--")
		argv = append(argv, pathspec...)
	}
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
	return executeGitCommandWithTimeout(parent, scope, "git_commit", argv, defaultGitCommitTimeout)
}

func executeGitCommand(parent context.Context, scope WorkspaceScope, toolName string, argv []string) (string, error) {
	return executeGitCommandWithTimeout(parent, scope, toolName, argv, defaultGitTimeout)
}

func executeGitCommandWithTimeout(parent context.Context, scope WorkspaceScope, toolName string, argv []string, timeout time.Duration) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("git command is required")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = scope.PrimaryPath
	cmd.Env = gitenv.FilterIdentityOverrides(os.Environ())

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

func (r *Runtime) executeSearch(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	queries, err := parseSearchQueries(args)
	if err != nil {
		return "", err
	}

	searchTargets, err := resolveSearchTargets(scope, args)
	if err != nil {
		return "", err
	}
	defer closeSearchTargets(searchTargets)
	searchRoots := searchTargetRoots(searchTargets)

	include := strings.TrimSpace(asString(args["include"]))
	payloadStyle := strings.ToLower(strings.TrimSpace(asString(args["_search_payload_style"])))
	contentMode := normalizeSearchContentMode(args["content_mode"])
	beforeContext := uint32FromArgs(args, "before_context", 0)
	afterContext := uint32FromArgs(args, "after_context", searchDefinitionAfterContext)
	fileOffset := uint32FromArgs(args, "file_offset", 0)
	maxMatchesPerFile := uint32FromArgs(args, "max_matches_per_file", 0)
	maxResults := clampInt(asInt(args["max_results"], defaultSearchResults), 1, maxSearchResults)
	rootResultLimit := maxResults
	if len(searchRoots) > 1 && rootResultLimit > 0 {
		rootResultLimit = max(1, maxResults/len(searchRoots))
	}
	if rootResultLimit < 1 {
		rootResultLimit = 1
	}
	if parent == nil {
		parent = context.Background()
	}

	combinedResults := make([]searchQueryExecution, 0, len(searchRoots)*len(queries))
	rootErrors := make([]error, 0)
	completed := true
	searchRoot := searchRootLabel(searchTargets)
	for _, target := range searchTargets {
		root := target.Root
		targetInclude := searchTargetInclude(target, include)
		timeout := resolveSearchTimeout(args["timeout_ms"])
		ctx, cancel := context.WithTimeout(parent, timeout)
		indexRoot, targetPath := selectResidentSearchScope(scope, target)
		helperResp, err := r.searchCoordinator.Execute(ctx, searchipc.Request{
			IndexRoot:         indexRoot,
			TargetPath:        targetPath,
			Operation:         "content",
			Queries:           queries,
			Include:           targetInclude,
			MaxResults:        rootResultLimit,
			PageLimit:         uint32(rootResultLimit + searchResultPageSlack),
			TimeoutMillis:     timeout.Milliseconds(),
			ContentMode:       contentMode,
			FileOffset:        fileOffset,
			MaxMatchesPerFile: maxMatchesPerFile,
			BeforeContext:     beforeContext,
			AfterContext:      afterContext,
		})
		ctxErr := ctx.Err()
		cancel()
		if err != nil {
			rootErrors = append(rootErrors, fmt.Errorf("%s: %w", searchTargetDisplay(target), err))
			completed = false
			combinedResults = append(combinedResults, timedOutSearchResults(queries)...)
			continue
		}
		if strings.TrimSpace(helperResp.HelperError) != "" {
			rootErrors = append(rootErrors, fmt.Errorf("%s: %s", searchTargetDisplay(target), strings.TrimSpace(helperResp.HelperError)))
			completed = false
			combinedResults = append(combinedResults, erroredSearchResults(queries, strings.TrimSpace(helperResp.HelperError))...)
			continue
		}
		if !helperResp.Completed || ctxErr != nil {
			completed = false
			combinedResults = append(combinedResults, timedOutSearchResults(queries)...)
			continue
		}

		contentResults, contentErrors := searchHelperContentResults(helperResp, root, queries, targetInclude, rootResultLimit)
		fileResults, fileErrors := searchHelperFileResults(helperResp.FileResults, root, queries, targetInclude, rootResultLimit)
		combinedResults = append(combinedResults, mergeSearchHelperResults(contentResults, fileResults)...)
		if len(fileResults) == 0 || !searchErrorsAreFallbackMisses(contentErrors, fileResults) {
			rootErrors = append(rootErrors, contentErrors...)
		}
		rootErrors = append(rootErrors, fileErrors...)
	}
	combinedResults = rewriteSearchResultsForDisplay(scope.PrimaryPath, searchRoots, searchTargetsContainFile(searchTargets), combinedResults)
	if len(combinedResults) == 0 {
		return "", fmt.Errorf("search query execution failed: %s", formatSearchQueryErrors(rootErrors))
	}
	payload := selectSearchContentPayload(payloadStyle, searchRoot, queries, include, combinedResults, maxResults)
	if len(rootErrors) > 0 && !hasSearchResultRows(combinedResults) {
		payload["search_errors"] = formatSearchQueryErrors(rootErrors)
	} else if len(rootErrors) > 0 && !completed {
		payload["search_warnings"] = formatSearchQueryErrors(rootErrors)
	}
	return encodeSearchPayload(payload)
}

func (r *Runtime) executeFind(parent context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	queries, err := parseFindQueries(args)
	if err != nil {
		return "", err
	}
	searchTargets, err := resolveSearchTargets(scope, args)
	if err != nil {
		return "", err
	}
	defer closeSearchTargets(searchTargets)
	searchRoots := searchTargetRoots(searchTargets)
	include := strings.TrimSpace(asString(args["include"]))
	mode := normalizeFindMode(args["mode"])
	pageIndex := uint32FromArgs(args, "page_index", 0)
	maxResults := clampInt(asInt(args["max_results"], defaultSearchResults), 1, maxSearchResults)
	rootResultLimit := maxResults
	if len(searchRoots) > 1 && rootResultLimit > 0 {
		rootResultLimit = max(1, maxResults/len(searchRoots))
	}
	if rootResultLimit < 1 {
		rootResultLimit = 1
	}
	if parent == nil {
		parent = context.Background()
	}

	combinedResults := make([]findQueryExecution, 0, len(searchTargets)*len(queries))
	rootErrors := make([]error, 0)
	completed := true
	searchRoot := searchRootLabel(searchTargets)
	for _, target := range searchTargets {
		root := target.Root
		targetInclude := searchTargetInclude(target, include)
		timeout := resolveSearchTimeout(args["timeout_ms"])
		ctx, cancel := context.WithTimeout(parent, timeout)
		indexRoot, targetPath := selectResidentSearchScope(scope, target)
		helperResp, err := r.searchCoordinator.Execute(ctx, searchipc.Request{
			IndexRoot:     indexRoot,
			TargetPath:    targetPath,
			Operation:     mode,
			Queries:       queries,
			Include:       targetInclude,
			MaxResults:    rootResultLimit,
			PageLimit:     uint32(rootResultLimit + searchResultPageSlack),
			PageIndex:     pageIndex,
			TimeoutMillis: timeout.Milliseconds(),
		})
		ctxErr := ctx.Err()
		cancel()
		if err != nil {
			rootErrors = append(rootErrors, fmt.Errorf("%s: %w", searchTargetDisplay(target), err))
			completed = false
			combinedResults = append(combinedResults, timedOutFindResults(queries, mode)...)
			continue
		}
		if strings.TrimSpace(helperResp.HelperError) != "" {
			rootErrors = append(rootErrors, fmt.Errorf("%s: %s", searchTargetDisplay(target), strings.TrimSpace(helperResp.HelperError)))
			completed = false
			combinedResults = append(combinedResults, erroredFindResults(queries, mode, strings.TrimSpace(helperResp.HelperError))...)
			continue
		}
		if !helperResp.Completed || ctxErr != nil {
			completed = false
			combinedResults = append(combinedResults, timedOutFindResults(queries, mode)...)
			continue
		}
		results, errs := findHelperResults(helperResp, root, queries, targetInclude, rootResultLimit, mode)
		combinedResults = append(combinedResults, results...)
		rootErrors = append(rootErrors, errs...)
	}
	combinedResults = rewriteFindResultsForDisplay(scope.PrimaryPath, searchRoots, searchTargetsContainFile(searchTargets), combinedResults)
	if len(combinedResults) == 0 {
		return "", fmt.Errorf("find query execution failed: %s", formatSearchQueryErrors(rootErrors))
	}
	payload := buildFindPayload(searchRoot, queries, include, combinedResults, maxResults, mode)
	if len(rootErrors) > 0 && !hasFindResultRows(combinedResults) {
		payload["find_errors"] = formatSearchQueryErrors(rootErrors)
	} else if len(rootErrors) > 0 && !completed {
		payload["find_warnings"] = formatSearchQueryErrors(rootErrors)
	}
	return encodeSearchPayload(payload)
}

func (r *Runtime) Close() error {
	if r == nil || r.searchCoordinator == nil {
		return nil
	}
	return r.searchCoordinator.Close()
}

func selectResidentSearchScope(scope WorkspaceScope, target searchTarget) (string, string) {
	targetPath := target.Root
	if strings.TrimSpace(target.FileName) != "" {
		targetPath = filepath.Join(target.Root, target.FileName)
	}
	best := ""
	for _, authorized := range append([]string{scope.PrimaryPath}, scope.Roots...) {
		authorized = filepath.Clean(strings.TrimSpace(authorized))
		rel, err := filepath.Rel(authorized, targetPath)
		if authorized == "" || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if best == "" || len(authorized) < len(best) {
			best = authorized
		}
	}
	if best == "" {
		best = target.Root
	}
	return best, targetPath
}

func executeSearchHelper(ctx context.Context, req searchipc.Request) (searchipc.Response, error) {
	helperPath, err := resolveSearchHelperPath()
	if err != nil {
		return searchipc.Response{}, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return searchipc.Response{}, fmt.Errorf("marshal FFF search helper request: %w", err)
	}
	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Stdin = bytes.NewReader(payload)
	stdout := newBoundedSearchHelperBuffer(maxSearchHelperStdout)
	stderr := newBoundedSearchHelperBuffer(maxSearchHelperStderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return searchipc.Response{}, fmt.Errorf("search FFF helper timed out or was cancelled: %w", ctxErr)
	}
	if stdout.Overflowed() {
		return searchipc.Response{}, fmt.Errorf("search FFF helper stdout exceeded %d bytes", maxSearchHelperStdout)
	}
	if stderr.Overflowed() {
		return searchipc.Response{}, fmt.Errorf("search FFF helper stderr exceeded %d bytes", maxSearchHelperStderr)
	}
	if runErr != nil {
		return searchipc.Response{}, fmt.Errorf("search FFF helper exited: %w%s", runErr, formatSearchHelperDiagnostic(stderr.String(), stdout.String()))
	}
	var resp searchipc.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return searchipc.Response{}, fmt.Errorf("decode FFF search helper response: malformed output: %w%s", err, formatSearchHelperDiagnostic(stderr.String(), stdout.String()))
	}
	return resp, nil
}

type boundedSearchHelperBuffer struct {
	limit      int
	buf        bytes.Buffer
	overflowed bool
}

func newBoundedSearchHelperBuffer(limit int) *boundedSearchHelperBuffer {
	return &boundedSearchHelperBuffer{limit: limit}
}

func (b *boundedSearchHelperBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	if originalLen > remaining {
		b.overflowed = true
	}
	return originalLen, nil
}

func (b *boundedSearchHelperBuffer) Bytes() []byte    { return b.buf.Bytes() }
func (b *boundedSearchHelperBuffer) String() string   { return b.buf.String() }
func (b *boundedSearchHelperBuffer) Overflowed() bool { return b.overflowed }

func resolveSearchHelperPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("SWARM_FFF_SEARCH_HELPER")); configured != "" {
		if isExecutableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("configured FFF search helper is not executable: %s", configured)
	}
	candidates := make([]string, 0, 3)
	if binDir := strings.TrimSpace(os.Getenv("SWARM_BIN_DIR")); binDir != "" {
		candidates = append(candidates, filepath.Join(binDir, fffSearchHelperBinaryName))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), fffSearchHelperBinaryName))
	}
	for _, candidate := range candidates {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(fffSearchHelperBinaryName); err == nil && isExecutableFile(path) {
		return path, nil
	}
	return "", fmt.Errorf("FFF search helper %q is not installed; rebuild or reinstall swarmd binaries", fffSearchHelperBinaryName)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func formatSearchHelperDiagnostic(stderrText, stdoutText string) string {
	detail := strings.TrimSpace(stderrText)
	if detail == "" {
		detail = strings.TrimSpace(stdoutText)
	}
	if detail == "" {
		return ""
	}
	detail = strings.ReplaceAll(strings.ReplaceAll(detail, "\r", " "), "\n", " ")
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	return ": " + detail
}

func searchHelperContentResults(resp searchipc.Response, searchRoot string, queries []string, include string, maxResults int) ([]searchQueryExecution, []error) {
	helperResults := resp.ContentResults
	if len(helperResults) == 0 && strings.TrimSpace(resp.Content.Query) != "" {
		helperResults = []searchipc.GrepQueryResult{resp.Content}
	}
	byQuery := make(map[string]searchipc.GrepQueryResult, len(helperResults))
	for _, result := range helperResults {
		byQuery[strings.ToLower(strings.TrimSpace(result.Query))] = result
	}
	results := make([]searchQueryExecution, 0, len(queries))
	errs := make([]error, 0)
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		helperResult, ok := byQuery[strings.ToLower(query)]
		result := searchQueryExecution{Query: query, Mode: "content"}
		if !ok {
			err := fmt.Errorf("query %q: FFF search helper did not return content-mode results", query)
			result.Error = err.Error()
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		if strings.TrimSpace(helperResult.Error) != "" {
			result.Error = strings.TrimSpace(helperResult.Error)
			results = append(results, result)
			errs = append(errs, formatSearchContentHelperError([]string{query}, result.Error))
			continue
		}
		rows, totals, truncated, timedOut, _ := collectSearchContentRows(query, searchRoot, include, helperResult.Matches, helperResult.Metrics, maxResults)
		result.ContentRows = rows
		result.Totals = totals
		result.ReturnedCount = len(rows)
		result.Truncated = truncated
		result.TimedOut = timedOut
		results = append(results, result)
	}
	return results, errs
}

func formatSearchContentHelperError(queries []string, message string) error {
	message = strings.TrimSpace(message)
	if len(queries) == 1 {
		return fmt.Errorf("query %q: %s", firstSearchQuery(queries), message)
	}
	return fmt.Errorf("multi-grep %q: %s", strings.Join(queries, " | "), message)
}

func searchHelperFileResults(helperResults []searchipc.SearchQueryResult, searchRoot string, queries []string, include string, maxResults int) ([]searchQueryExecution, []error) {
	if len(helperResults) == 0 {
		return nil, nil
	}
	allowedQueries := make(map[string]string, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" {
			allowedQueries[strings.ToLower(query)] = query
		}
	}
	results := make([]searchQueryExecution, 0, len(helperResults))
	errs := make([]error, 0)
	for _, helperResult := range helperResults {
		query := strings.TrimSpace(helperResult.Query)
		if canonical, ok := allowedQueries[strings.ToLower(query)]; ok {
			query = canonical
		}
		if query == "" {
			continue
		}
		result := searchQueryExecution{Query: query, Mode: "files"}
		if strings.TrimSpace(helperResult.Error) != "" {
			err := fmt.Errorf("query %q: %s", query, strings.TrimSpace(helperResult.Error))
			result.Error = strings.TrimSpace(helperResult.Error)
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		rows, totals, truncated := collectSearchFileRows(query, searchRoot, include, helperResult.Items, helperResult.Metrics, maxResults)
		result.FileRows = rows
		result.Totals = totals
		result.ReturnedCount = len(rows)
		result.Truncated = truncated
		results = append(results, result)
	}
	return results, errs
}

func findHelperResults(resp searchipc.Response, searchRoot string, queries []string, include string, maxResults int, mode string) ([]findQueryExecution, []error) {
	switch mode {
	case "directories":
		return findHelperDirectoryResults(resp.DirectoryResults, searchRoot, queries, maxResults)
	case "mixed":
		return findHelperMixedResults(resp.MixedResults, searchRoot, queries, include, maxResults)
	default:
		return findHelperFileResults(resp.FileResults, searchRoot, queries, include, maxResults, mode)
	}
}

func findHelperFileResults(helperResults []searchipc.SearchQueryResult, searchRoot string, queries []string, include string, maxResults int, mode string) ([]findQueryExecution, []error) {
	byQuery := make(map[string]searchipc.SearchQueryResult, len(helperResults))
	for _, result := range helperResults {
		byQuery[strings.ToLower(strings.TrimSpace(result.Query))] = result
	}
	results := make([]findQueryExecution, 0, len(queries))
	errs := make([]error, 0)
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		helperResult, ok := byQuery[strings.ToLower(query)]
		result := findQueryExecution{Query: query, Mode: mode}
		if !ok {
			err := fmt.Errorf("query %q: FFF find helper did not return %s results", query, mode)
			result.Error = err.Error()
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		if strings.TrimSpace(helperResult.Error) != "" {
			err := fmt.Errorf("query %q: %s", query, strings.TrimSpace(helperResult.Error))
			result.Error = strings.TrimSpace(helperResult.Error)
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		rows, totals, truncated := collectFindFileRows(query, searchRoot, include, helperResult.Items, helperResult.Metrics, maxResults, "file")
		result.Rows = rows
		result.Totals = totals
		result.ReturnedCount = len(rows)
		result.Truncated = truncated
		results = append(results, result)
	}
	return results, errs
}

func findHelperDirectoryResults(helperResults []searchipc.DirectoryQueryResult, searchRoot string, queries []string, maxResults int) ([]findQueryExecution, []error) {
	byQuery := make(map[string]searchipc.DirectoryQueryResult, len(helperResults))
	for _, result := range helperResults {
		byQuery[strings.ToLower(strings.TrimSpace(result.Query))] = result
	}
	results := make([]findQueryExecution, 0, len(queries))
	errs := make([]error, 0)
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		helperResult, ok := byQuery[strings.ToLower(query)]
		result := findQueryExecution{Query: query, Mode: "directories"}
		if !ok {
			err := fmt.Errorf("query %q: FFF find helper did not return directory results", query)
			result.Error = err.Error()
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		if strings.TrimSpace(helperResult.Error) != "" {
			err := fmt.Errorf("query %q: %s", query, strings.TrimSpace(helperResult.Error))
			result.Error = strings.TrimSpace(helperResult.Error)
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		rows, totals, truncated := collectFindDirectoryRows(query, searchRoot, helperResult.Items, helperResult.Metrics, maxResults)
		result.Rows = rows
		result.Totals = totals
		result.ReturnedCount = len(rows)
		result.Truncated = truncated
		results = append(results, result)
	}
	return results, errs
}

func findHelperMixedResults(helperResults []searchipc.MixedQueryResult, searchRoot string, queries []string, include string, maxResults int) ([]findQueryExecution, []error) {
	byQuery := make(map[string]searchipc.MixedQueryResult, len(helperResults))
	for _, result := range helperResults {
		byQuery[strings.ToLower(strings.TrimSpace(result.Query))] = result
	}
	results := make([]findQueryExecution, 0, len(queries))
	errs := make([]error, 0)
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		helperResult, ok := byQuery[strings.ToLower(query)]
		result := findQueryExecution{Query: query, Mode: "mixed"}
		if !ok {
			err := fmt.Errorf("query %q: FFF find helper did not return mixed results", query)
			result.Error = err.Error()
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		if strings.TrimSpace(helperResult.Error) != "" {
			err := fmt.Errorf("query %q: %s", query, strings.TrimSpace(helperResult.Error))
			result.Error = strings.TrimSpace(helperResult.Error)
			results = append(results, result)
			errs = append(errs, err)
			continue
		}
		rows, totals, truncated := collectFindMixedRows(query, searchRoot, include, helperResult.Items, helperResult.Metrics, maxResults)
		result.Rows = rows
		result.Totals = totals
		result.ReturnedCount = len(rows)
		result.Truncated = truncated
		results = append(results, result)
	}
	return results, errs
}

func selectSearchContentPayload(style, searchRoot string, queries []string, include string, results []searchQueryExecution, maxResults int) map[string]any {
	if strings.EqualFold(strings.TrimSpace(style), "legacy") {
		return buildSearchContentLegacyPayload(searchRoot, queries, include, results, maxResults)
	}
	return buildSearchContentPayload(searchRoot, queries, include, results, maxResults)
}

func encodeSearchPayload(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func normalizeSearchContentMode(raw any) string {
	switch strings.ToLower(strings.TrimSpace(asString(raw))) {
	case "regex", "regexp":
		return "regex"
	case "fuzzy":
		return "fuzzy"
	default:
		return "literal"
	}
}

func normalizeFindMode(raw any) string {
	switch strings.ToLower(strings.TrimSpace(asString(raw))) {
	case "dir", "dirs", "directory", "directories":
		return "directories"
	case "mixed", "all":
		return "mixed"
	case "glob", "pattern":
		return "glob"
	default:
		return "files"
	}
}

func uint32FromArgs(args map[string]any, key string, fallback uint32) uint32 {
	value := asInt(args[key], int(fallback))
	if value < 0 {
		return fallback
	}
	if value > int(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

func parseFindQueries(args map[string]any) ([]string, error) {
	queries := make([]string, 0, 8)
	if single := strings.TrimSpace(asString(args["query"])); single != "" {
		queries = append(queries, single)
	}
	queries = append(queries, asStringSlice(args["queries"])...)
	if len(queries) == 0 {
		return nil, errors.New("find requires query or queries")
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
		return nil, errors.New("find requires at least one non-empty query")
	}
	if len(deduped) > maxSearchQueries {
		return nil, fmt.Errorf("find supports at most %d queries per call; split the batch and retry", maxSearchQueries)
	}
	return deduped, nil
}

func buildFFFGrepQuery(include, pattern string) string {
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

type findRow struct {
	Query        string
	Path         string
	RelativePath string
	Name         string
	Kind         string
	GitStatus    string
	Score        int
}

type findQueryExecution struct {
	Query         string
	Mode          string
	Rows          []findRow
	Totals        searchAggregateTotals
	ReturnedCount int
	Truncated     bool
	TimedOut      bool
	Error         string
}

type searchAggregateTotals struct {
	TotalMatched       int
	TotalFilesSearched int
	TotalFiles         int
	FilteredFileCount  int
	NextFileOffset     int
	RegexFallbackError string
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

type searchTarget struct {
	Root        string
	FileName    string
	DisplayPath string
	Authority   *rootedWorkspacePath
}

func resolveSearchTargets(scope WorkspaceScope, args map[string]any) ([]searchTarget, error) {
	requested := asStringSlice(args["paths"])
	if len(requested) == 0 {
		pathArg := strings.TrimSpace(asString(args["path"]))
		if pathArg == "" {
			pathArg = "."
		}
		if target, err := resolveSearchTarget(scope, pathArg); err == nil {
			return []searchTarget{target}, nil
		} else {
			split, splitErr := splitSearchPathArgument(scope, pathArg)
			if splitErr != nil {
				return nil, err
			}
			return dedupeSearchTargets(split), nil
		}
	}
	targets := make([]searchTarget, 0, len(requested))
	for _, path := range requested {
		target, err := resolveSearchTarget(scope, path)
		if err != nil {
			closeSearchTargets(targets)
			return nil, err
		}
		targets = append(targets, target)
	}
	targets = dedupeSearchTargets(targets)
	if len(targets) == 0 {
		return nil, errors.New("search path is required")
	}
	return targets, nil
}

func resolveSearchTarget(scope WorkspaceScope, requested string) (searchTarget, error) {
	authority, err := openRootedWorkspacePath(scope, requested)
	if err != nil {
		return searchTarget{}, err
	}
	info, err := authority.stat()
	if err != nil {
		authority.Close()
		return searchTarget{}, fmt.Errorf("stat search path %q: %w", requested, err)
	}
	root := filepath.Clean(authority.absolutePath)
	if info.IsDir() {
		return searchTarget{Root: root, DisplayPath: root, Authority: authority}, nil
	}
	return searchTarget{Root: filepath.Dir(root), FileName: filepath.Base(root), DisplayPath: root, Authority: authority}, nil
}

func closeSearchTargets(targets []searchTarget) {
	for i := range targets {
		if targets[i].Authority != nil {
			_ = targets[i].Authority.Close()
		}
	}
}

func dedupeSearchTargets(targets []searchTarget) []searchTarget {
	out := make([]searchTarget, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target.Root = filepath.Clean(strings.TrimSpace(target.Root))
		target.FileName = strings.TrimSpace(target.FileName)
		target.DisplayPath = filepath.Clean(strings.TrimSpace(target.DisplayPath))
		if target.Root == "" {
			if target.Authority != nil {
				_ = target.Authority.Close()
			}
			continue
		}
		if target.DisplayPath == "" || target.DisplayPath == "." {
			target.DisplayPath = target.Root
		}
		key := strings.ToLower(target.Root + "\x00" + target.FileName)
		if _, ok := seen[key]; ok {
			if target.Authority != nil {
				_ = target.Authority.Close()
			}
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

func searchTargetRoots(targets []searchTarget) []string {
	roots := make([]string, 0, len(targets))
	for _, target := range targets {
		roots = append(roots, target.Root)
	}
	return dedupeSearchRoots(roots)
}

func searchTargetsContainFile(targets []searchTarget) bool {
	for _, target := range targets {
		if strings.TrimSpace(target.FileName) != "" {
			return true
		}
	}
	return false
}

func dedupeSearchRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" {
			continue
		}
		key := strings.ToLower(root)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, root)
	}
	return out
}

func searchTargetInclude(target searchTarget, include string) string {
	if strings.TrimSpace(target.FileName) != "" {
		return target.FileName
	}
	return strings.TrimSpace(include)
}

func splitSearchPathArgument(scope WorkspaceScope, raw string) ([]searchTarget, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	if len(fields) <= 1 {
		return nil, fmt.Errorf("search path %q is not a resolvable workspace path", raw)
	}
	targets := make([]searchTarget, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		target, err := resolveSearchTarget(scope, field)
		if err != nil {
			closeSearchTargets(targets)
			return nil, err
		}
		targets = append(targets, target)
	}
	if len(targets) <= 1 {
		closeSearchTargets(targets)
		return nil, fmt.Errorf("search path %q is not a multi-path value", raw)
	}
	return targets, nil
}

func firstSearchQuery(queries []string) string {
	if len(queries) == 0 {
		return ""
	}
	return strings.TrimSpace(queries[0])
}

func timedOutSearchResults(queries []string) []searchQueryExecution {
	results := make([]searchQueryExecution, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		results = append(results, searchQueryExecution{Query: query, Mode: "content", Truncated: true, TimedOut: true})
	}
	return results
}

func erroredSearchResults(queries []string, message string) []searchQueryExecution {
	results := make([]searchQueryExecution, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		results = append(results, searchQueryExecution{Query: query, Mode: "content", Error: strings.TrimSpace(message)})
	}
	return results
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

func searchErrorsAreFallbackMisses(contentErrors []error, fileResults []searchQueryExecution) bool {
	if len(contentErrors) == 0 || len(fileResults) == 0 {
		return false
	}
	for _, result := range fileResults {
		if len(result.FileRows) > 0 {
			return true
		}
	}
	return false
}

func mergeSearchHelperResults(contentResults, fileResults []searchQueryExecution) []searchQueryExecution {
	if len(fileResults) == 0 {
		return contentResults
	}
	if len(contentResults) == 0 {
		return fileResults
	}
	byQuery := make(map[string]int, len(contentResults))
	merged := make([]searchQueryExecution, 0, len(contentResults)+len(fileResults))
	for _, result := range contentResults {
		key := strings.ToLower(strings.TrimSpace(result.Query))
		if key != "" {
			byQuery[key] = len(merged)
		}
		merged = append(merged, result)
	}
	for _, fileResult := range fileResults {
		key := strings.ToLower(strings.TrimSpace(fileResult.Query))
		idx, ok := byQuery[key]
		if !ok {
			merged = append(merged, fileResult)
			continue
		}
		contentResult := merged[idx]
		if len(contentResult.ContentRows) == 0 {
			fileResult.Error = joinSearchText(contentResult.Error, fileResult.Error)
			fileResult.Truncated = fileResult.Truncated || contentResult.Truncated
			fileResult.TimedOut = fileResult.TimedOut || contentResult.TimedOut
			merged[idx] = fileResult
		}
	}
	return merged
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

func collectFindFileRows(query, searchRoot, include string, items []fff.SearchItem, metrics fff.SearchMetrics, maxResults int, kind string) ([]findRow, searchAggregateTotals, bool) {
	rows := make([]findRow, 0, minInt(len(items), maxResults))
	truncated := false
	for _, item := range items {
		pathValue := filepath.Clean(item.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, item.RelativePath)
		if !matchesIncludeGlob(include, relPath) {
			continue
		}
		rows = append(rows, findRow{
			Query:        query,
			Path:         pathValue,
			RelativePath: relPath,
			Name:         strings.TrimSpace(item.FileName),
			Kind:         kind,
			GitStatus:    strings.TrimSpace(item.GitStatus),
			Score:        item.Score,
		})
		if len(rows) >= maxResults {
			truncated = true
			break
		}
	}
	if metrics.TotalMatched > uint32(len(rows)) {
		truncated = true
	}
	return rows, searchAggregateTotals{TotalMatched: int(metrics.TotalMatched), TotalFiles: int(metrics.TotalFiles)}, truncated
}

func collectFindDirectoryRows(query, searchRoot string, items []fff.DirectoryItem, metrics fff.SearchMetrics, maxResults int) ([]findRow, searchAggregateTotals, bool) {
	rows := make([]findRow, 0, minInt(len(items), maxResults))
	truncated := false
	for _, item := range items {
		pathValue := filepath.Clean(item.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, item.RelativePath)
		rows = append(rows, findRow{
			Query:        query,
			Path:         pathValue,
			RelativePath: relPath,
			Name:         strings.TrimSpace(item.DirectoryName),
			Kind:         "directory",
			Score:        item.Score,
		})
		if len(rows) >= maxResults {
			truncated = true
			break
		}
	}
	if metrics.TotalMatched > uint32(len(rows)) {
		truncated = true
	}
	return rows, searchAggregateTotals{TotalMatched: int(metrics.TotalMatched), TotalFiles: int(metrics.TotalFiles)}, truncated
}

func collectFindMixedRows(query, searchRoot, include string, items []fff.MixedItem, metrics fff.SearchMetrics, maxResults int) ([]findRow, searchAggregateTotals, bool) {
	rows := make([]findRow, 0, minInt(len(items), maxResults))
	truncated := false
	for _, item := range items {
		pathValue := filepath.Clean(item.Path)
		relPath := normalizeSearchRelativePath(searchRoot, pathValue, item.RelativePath)
		kind := strings.TrimSpace(item.ItemType)
		if kind == "" {
			kind = "file"
		}
		if kind == "file" && !matchesIncludeGlob(include, relPath) {
			continue
		}
		rows = append(rows, findRow{
			Query:        query,
			Path:         pathValue,
			RelativePath: relPath,
			Name:         strings.TrimSpace(item.DisplayName),
			Kind:         kind,
			GitStatus:    strings.TrimSpace(item.GitStatus),
			Score:        item.Score,
		})
		if len(rows) >= maxResults {
			truncated = true
			break
		}
	}
	if metrics.TotalMatched > uint32(len(rows)) {
		truncated = true
	}
	return rows, searchAggregateTotals{TotalMatched: int(metrics.TotalMatched), TotalFiles: int(metrics.TotalFiles)}, truncated
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
	merged, mergeTruncated, safetySource := mergeSearchRows(results, maxResults)
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
	merged, mergeTruncated, safetySource := mergeSearchRows(results, maxResults)
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

func buildFindPayload(searchRoot string, queries []string, include string, results []findQueryExecution, maxResults int, mode string) map[string]any {
	merged, mergeTruncated := mergeFindRows(results, maxResults)
	totals := aggregateFindTotals(results)
	truncated, timedOut := findBatchFlags(results)
	truncated = truncated || mergeTruncated
	response := map[string]any{
		"path_id":           toolPathID("find"),
		"search_mode":       mode,
		"path":              searchRoot,
		"count":             len(merged),
		"results":           buildCompactFindResults(merged, len(queries) > 1),
		"truncated":         truncated,
		"timed_out":         timedOut,
		"summary":           findSummaryForQueries(queries, searchRoot, len(merged), truncated, timedOut, mode),
		"details_truncated": truncated,
		"provider":          "fff",
		"total_matched":     totals.TotalMatched,
		"total_files":       totals.TotalFiles,
		"query_results":     buildFindQuerySummaries(results),
	}
	if trimmed := strings.TrimSpace(include); trimmed != "" {
		response["include"] = trimmed
	}
	return response
}

func mergeFindRows(results []findQueryExecution, maxResults int) ([]findRow, bool) {
	merged := make([]findRow, 0, maxResults)
	positions := make([]int, len(results))
	for len(merged) < maxResults {
		progressed := false
		for idx, result := range results {
			if positions[idx] >= len(result.Rows) {
				continue
			}
			merged = append(merged, result.Rows[positions[idx]])
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
		if positions[idx] < len(result.Rows) {
			truncated = true
			break
		}
	}
	return merged, truncated
}

func buildCompactFindResults(rows []findRow, multiQuery bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		pathValue := strings.TrimSpace(row.RelativePath)
		if pathValue == "" {
			pathValue = strings.TrimSpace(row.Path)
		}
		entry := map[string]any{
			"path": pathValue,
			"kind": row.Kind,
		}
		if multiQuery && strings.TrimSpace(row.Query) != "" {
			entry["query"] = row.Query
		}
		if strings.TrimSpace(row.Name) != "" {
			entry["name"] = row.Name
		}
		if strings.TrimSpace(row.GitStatus) != "" {
			entry["git_status"] = row.GitStatus
		}
		if row.Score != 0 {
			entry["score"] = row.Score
		}
		out = append(out, entry)
	}
	return out
}

func buildFindQuerySummaries(results []findQueryExecution) []searchQuerySummary {
	out := make([]searchQuerySummary, 0, len(results))
	for _, result := range results {
		count := result.ReturnedCount
		if count <= 0 {
			count = len(result.Rows)
		}
		out = append(out, searchQuerySummary{
			Query:        result.Query,
			Mode:         result.Mode,
			Count:        count,
			TotalMatched: result.Totals.TotalMatched,
			TotalFiles:   result.Totals.TotalFiles,
			TimedOut:     result.TimedOut,
			Truncated:    result.Truncated,
			Error:        result.Error,
			Summary:      findSummaryForQueries([]string{result.Query}, "", count, result.Truncated, result.TimedOut, result.Mode),
		})
	}
	return out
}

func aggregateFindTotals(results []findQueryExecution) searchAggregateTotals {
	var totals searchAggregateTotals
	for _, result := range results {
		totals.TotalMatched += result.Totals.TotalMatched
		if result.Totals.TotalFiles > totals.TotalFiles {
			totals.TotalFiles = result.Totals.TotalFiles
		}
	}
	return totals
}

func findBatchFlags(results []findQueryExecution) (bool, bool) {
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

func hasFindResultRows(results []findQueryExecution) bool {
	for _, result := range results {
		if len(result.Rows) > 0 {
			return true
		}
	}
	return false
}

func timedOutFindResults(queries []string, mode string) []findQueryExecution {
	results := make([]findQueryExecution, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		results = append(results, findQueryExecution{Query: query, Mode: mode, Truncated: true, TimedOut: true})
	}
	return results
}

func erroredFindResults(queries []string, mode string, message string) []findQueryExecution {
	results := make([]findQueryExecution, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		results = append(results, findQueryExecution{Query: query, Mode: mode, Error: strings.TrimSpace(message)})
	}
	return results
}

func rewriteFindResultsForDisplay(primaryRoot string, searchRoots []string, force bool, results []findQueryExecution) []findQueryExecution {
	if len(results) == 0 || (!force && len(searchRoots) <= 1) {
		return results
	}
	primaryRoot = filepath.Clean(strings.TrimSpace(primaryRoot))
	for resultIdx := range results {
		for rowIdx := range results[resultIdx].Rows {
			row := &results[resultIdx].Rows[rowIdx]
			row.RelativePath = workspaceRelativeSearchPath(primaryRoot, row.Path, row.RelativePath)
		}
	}
	return results
}

func hasSearchResultRows(results []searchQueryExecution) bool {
	for _, result := range results {
		if len(result.ContentRows) > 0 || len(result.FileRows) > 0 {
			return true
		}
	}
	return false
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

func mergeSearchRows(results []searchQueryExecution, maxResults int) ([]searchContentRow, bool, string) {
	merged := make([]searchContentRow, 0, maxResults)
	positions := make([]int, len(results))
	var safetySource strings.Builder
	for len(merged) < maxResults {
		progressed := false
		for idx, result := range results {
			var row searchContentRow
			if positions[idx] < len(result.ContentRows) {
				row = result.ContentRows[positions[idx]]
			} else if positions[idx]-len(result.ContentRows) < len(result.FileRows) {
				fileRow := result.FileRows[positions[idx]-len(result.ContentRows)]
				row = searchContentRow{
					Query:        fileRow.Query,
					Path:         fileRow.Path,
					RelativePath: fileRow.RelativePath,
					FileName:     fileRow.FileName,
					GitStatus:    fileRow.GitStatus,
					Text:         "file match",
				}
			} else {
				continue
			}
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
		if positions[idx] < len(result.ContentRows)+len(result.FileRows) {
			truncated = true
			break
		}
	}
	return merged, truncated, safetySource.String()
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

func rewriteSearchResultsForDisplay(primaryRoot string, searchRoots []string, force bool, results []searchQueryExecution) []searchQueryExecution {
	if len(results) == 0 || (!force && len(searchRoots) <= 1) {
		return results
	}
	primaryRoot = filepath.Clean(strings.TrimSpace(primaryRoot))
	for resultIdx := range results {
		for rowIdx := range results[resultIdx].ContentRows {
			row := &results[resultIdx].ContentRows[rowIdx]
			row.RelativePath = workspaceRelativeSearchPath(primaryRoot, row.Path, row.RelativePath)
		}
		for rowIdx := range results[resultIdx].FileRows {
			row := &results[resultIdx].FileRows[rowIdx]
			row.RelativePath = workspaceRelativeSearchPath(primaryRoot, row.Path, row.RelativePath)
		}
	}
	return results
}

func workspaceRelativeSearchPath(primaryRoot, fullPath, fallback string) string {
	fullPath = filepath.Clean(strings.TrimSpace(fullPath))
	if primaryRoot != "" && fullPath != "." {
		if rel, err := filepath.Rel(primaryRoot, fullPath); err == nil {
			rel = filepath.ToSlash(strings.TrimSpace(rel))
			if rel != "" && rel != "." && !strings.HasPrefix(rel, "../") {
				return rel
			}
		}
	}
	return filepath.ToSlash(strings.TrimSpace(fallback))
}

func searchRootLabel(targets []searchTarget) string {
	if len(targets) == 1 {
		return searchTargetDisplay(targets[0])
	}
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, searchTargetDisplay(target))
	}
	return strings.Join(parts, ", ")
}

func searchTargetDisplay(target searchTarget) string {
	if display := searchRootDisplay(target.DisplayPath); display != "." {
		return display
	}
	return searchRootDisplay(target.Root)
}

func searchRootDisplay(root string) string {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return "."
	}
	return root
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
	outputDirPath, outputDirLabel, err := resolveWebDownloadOutputDir(scope, outputDirArg)
	if err != nil {
		return "", err
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
		data := []byte(text)
		var writeErr error
		if outputDirArg == "" {
			writeErr = appstorage.WritePrivateFile(targetPath, data)
		} else {
			writeErr = writeWorkspaceFile(scope, targetPath, data, 0o644)
		}
		if writeErr != nil {
			entry["error"] = fmt.Sprintf("write failed: %v", writeErr)
			writeErrors = append(writeErrors, fmt.Sprintf("%s: %v", strings.TrimSpace(item.URL), writeErr))
			manifest = append(manifest, entry)
			continue
		}
		entry["file_path"] = displayWebDownloadFilePath(scope, targetPath)
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
		"output_dir":                outputDirLabel,
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

func resolveWebDownloadOutputDir(scope WorkspaceScope, outputDirArg string) (string, string, error) {
	outputDirArg = strings.TrimSpace(outputDirArg)
	if outputDirArg == "" {
		path, err := appstorage.WorkspaceCacheDir(scope.PrimaryPath, defaultWebDownloadSubdir)
		if err != nil {
			return "", "", err
		}
		return path, filepath.ToSlash(path), nil
	}
	path, err := resolveWorkspacePath(scope, outputDirArg)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", fmt.Errorf("create download directory: %w", err)
	}
	return path, filepath.ToSlash(outputDirArg), nil
}

func writeWorkspaceFile(scope WorkspaceScope, targetPath string, data []byte, perm fs.FileMode) error {
	target, err := openRootedWorkspacePath(scope, targetPath)
	if err != nil {
		return err
	}
	defer target.Close()
	return target.writeFile(data, perm)
}

func displayWebDownloadFilePath(scope WorkspaceScope, targetPath string) string {
	if primary := strings.TrimSpace(scope.PrimaryPath); primary != "" {
		if relPath, err := filepath.Rel(primary, targetPath); err == nil {
			relPath = filepath.ToSlash(strings.TrimSpace(relPath))
			if relPath != "" && relPath != "." && !strings.HasPrefix(relPath, "../") && relPath != ".." {
				return relPath
			}
		}
	}
	return filepath.ToSlash(targetPath)
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
		return ExaRuntimeConfig{}, errors.New("Exa web access requires an active API key, but the account credential resolver is unavailable")
	}
	config, err := r.exaConfigResolver(ctx)
	if err != nil {
		return ExaRuntimeConfig{}, err
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	if !config.Enabled || config.APIKey == "" {
		return ExaRuntimeConfig{}, errors.New("Exa web access requires an active API key; add one in Settings > Providers or run /auth key exa <api_key>")
	}
	config.Source = "api_key"
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

func (r *Runtime) doExaRequest(ctx context.Context, endpoint, apiKey string, payload map[string]any, out any) error {
	if r == nil {
		return errors.New("runtime is not configured")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return errors.New("Exa web access requires an active API key; add one in Settings > Providers or run /auth key exa <api_key>")
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
	req.Header.Set("x-api-key", apiKey)

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
		return pebblestore.WorkspaceTodoOwnerKindUser, nil
	}
	normalized, ok := pebblestore.ParseWorkspaceTodoOwnerKind(trimmed)
	if !ok {
		return "", fmt.Errorf("owner_kind must be user")
	}
	if normalized == pebblestore.WorkspaceTodoOwnerKindAgent {
		return "", errors.New("manage_todos is user-owned only; use plan_manage for agent self-tracking, execution checklists, and checkpoint progress")
	}
	return normalized, nil
}

func mergeManageTodoAccountScope(options todoruntime.ListOptions, accountScopeID string) todoruntime.ListOptions {
	options.AccountScopeID = strings.TrimSpace(accountScopeID)
	return options
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

type listEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Depth int    `json:"depth,omitempty"`
}

func executeList(scope WorkspaceScope, args map[string]any) (string, error) {
	requestedPath := strings.TrimSpace(asString(args["path"]))
	if requestedPath == "" {
		requestedPath = "."
	}
	rootedPath, err := openRootedWorkspacePath(scope, requestedPath)
	if err != nil {
		return "", err
	}
	defer rootedPath.Close()
	searchRoot := rootedPath.absolutePath

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

	entries, scanLimited, err := collectListEntries(rootedPath, mode, maxDepth)
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
	target, err := openRootedWorkspacePath(scope, asString(args["path"]))
	if err != nil {
		return "", err
	}
	defer target.Close()
	targetPath := target.absolutePath
	operations, err := parseEditOperations(args)
	if err != nil {
		return "", err
	}

	file, err := target.openMutable(os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("edit open failed: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEditBytes+1))
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

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("edit write failed: rewind: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return "", fmt.Errorf("edit write failed: truncate: %w", err)
	}
	if _, err := io.WriteString(file, after); err != nil {
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

	raw := append([]byte(nil), matched.Content...)
	if len(raw) == 0 {
		return "", errors.New("skill-use discovered skill has no rooted content snapshot")
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
		createArgs := cloneMapStringAny(args)
		createArgs["expected_revision"] = manageSkillMissingRevision
		return manageSkillApplyChange(scope, createArgs, false)
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
	case "get_orchestration_policy", "get-orchestration-policy":
		return r.manageAgentOrchestrationPolicy(scope, args, false, false)
	case "update_orchestration_policy", "update-orchestration-policy":
		return r.manageAgentOrchestrationPolicy(scope, args, true, confirm)
	case "transcript", "session_transcript", "session-transcript":
		return r.manageAgentSessionTranscript(scope, args)
	case "get", "read":
		return r.manageAgentGet(scope, args)
	case "create":
		return r.manageAgentUpsert(scope, args, false, confirm)
	case "update":
		return r.manageAgentUpsert(scope, args, true, confirm)
	case "delete", "remove":
		return r.manageAgentDelete(scope, args, confirm)
	case "create_custom_tool", "create-custom-tool":
		return r.manageAgentCustomToolUpsert(scope, args, false, confirm)
	case "update_custom_tool", "update-custom-tool":
		return r.manageAgentCustomToolUpsert(scope, args, true, confirm)
	case "delete_custom_tool", "delete-custom-tool", "remove_custom_tool", "remove-custom-tool":
		return r.manageAgentDeleteCustomTool(scope, args, confirm)
	case "assign_custom_tool", "assign-custom-tool":
		return r.manageAgentAssignCustomTool(scope, args, confirm)
	case "unassign_custom_tool", "unassign-custom-tool":
		return r.manageAgentUnassignCustomTool(scope, args, confirm)
	case "activate_primary", "activate-primary":
		return r.manageAgentActivatePrimary(scope, args, confirm)
	case "set_active_subagent", "set-active-subagent":
		return r.manageAgentSetActiveSubagent(scope, args, confirm)
	case "remove_active_subagent", "remove-active-subagent", "delete_active_subagent", "delete-active-subagent":
		return r.manageAgentRemoveActiveSubagent(scope, args, confirm)
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
	case "create_batch", "create-batch":
		return r.manageThemeCreateBatch(scope, args, confirm)
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
	case "recall":
		return r.manageWorktreeRecall(scope, args)
	case "integrate":
		return r.manageWorktreeIntegrate(scope, args)
	default:
		return "", fmt.Errorf("manage-worktree action %q is unsupported", action)
	}
}

func (r *Runtime) executeManageTodos(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "list"
	}
	ownerKind, err := normalizeManageTodoOwnerKind(asString(args["owner_kind"]))
	if err != nil {
		return "", err
	}
	if r == nil || r.todos == nil {
		return executeStubTool("manage_todos", args)
	}
	requestedWorkspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if requestedWorkspacePath == "" {
		requestedWorkspacePath = "."
	}
	workspacePath, err := resolveWorkspacePath(scope, requestedWorkspacePath)
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
		listOptions := mergeManageTodoAccountScope(manageTodoListScope(ownerKind, scope.SessionID), scope.Principal.AccountScopeID)
		items, summary, err := r.todos.List(workspacePath, listOptions)
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "summary":
		listOptions := mergeManageTodoAccountScope(manageTodoListScope(ownerKind, scope.SessionID), scope.Principal.AccountScopeID)
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
			AccountScopeID: scope.Principal.AccountScopeID, WorkspacePath: workspacePath,
			OwnerKind:  ownerKind,
			Text:       text,
			Priority:   asString(args["priority"]),
			Group:      asString(args["group"]),
			Tags:       asStringSlice(args["tags"]),
			InProgress: asBool(args["in_progress"]),
			SessionID:  sessionID,
			ParentID:   asString(args["parent_id"]),
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
			AccountScopeID: scope.Principal.AccountScopeID, WorkspacePath: workspacePath,
			ID:         id,
			Text:       text,
			Done:       done,
			Priority:   priority,
			Group:      group,
			Tags:       tags,
			InProgress: inProgress,
			SessionID:  sessionID,
			ParentID:   parentID,
		}, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: updateSessionID})
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
		summary, _, err := r.todos.Delete(workspacePath, id, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["id"] = id
		response["summary"] = summary
	case "delete_done":
		items, summary, _, err := r.todos.DeleteDone(workspacePath, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
		if err != nil {
			return "", err
		}
		response["items"] = items
		response["summary"] = summary
	case "delete_all":
		items, summary, _, err := r.todos.DeleteAll(workspacePath, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
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
		items, summary, _, err := r.todos.Reorder(todoruntime.ReorderInput{AccountScopeID: scope.Principal.AccountScopeID, WorkspacePath: workspacePath, OwnerKind: ownerKind, OrderedIDs: orderedIDs}, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
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
		item, summary, _, err := r.todos.SetInProgress(workspacePath, id, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
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
		results, items, summary, _, err := r.todos.ApplyBatch(workspacePath, operations, todoruntime.ListOptions{AccountScopeID: scope.Principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(scope.SessionID)})
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

func manageWorktreeLaunchRows(entry map[string]any) []any {
	rows := make([]any, 0)
	switch typed := entry["launches"].(type) {
	case []any:
		rows = append(rows, typed...)
	case []map[string]any:
		for _, row := range typed {
			rows = append(rows, row)
		}
	}
	return rows
}

func (r *Runtime) manageWorktreeIntegrate(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || r.worktrees == nil {
		return "", errors.New("manage-worktree integrate requires session and worktree services")
	}
	parentSessionID := strings.TrimSpace(firstNonEmptyString(scope.SessionID, scope.Principal.SessionID))
	parent, ok, err := r.sessions.GetSession(parentSessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("parent session %q not found", parentSessionID)
	}
	parentPath, err := r.manageWorktreeResolveWorkspacePath(scope, "")
	if err != nil {
		return "", err
	}
	selected := asStringSlice(args["session_ids"])
	selectedTaskCallID := strings.TrimSpace(asString(args["task_call_id"]))
	if len(selected) > 0 && selectedTaskCallID != "" {
		return "", errors.New("integrate accepts exactly one selector: session_ids or task_call_id")
	}
	launchMap, _ := parent.Metadata["task_launches"].(map[string]any)
	if len(selected) == 0 {
		if selectedTaskCallID == "" {
			return "", errors.New("integrate requires session_ids or task_call_id; use task_call_id for a complete large Coder wave")
		}
		rawEntry, exists := launchMap[selectedTaskCallID]
		if !exists {
			return "", fmt.Errorf("task call %q is not present in this parent's durable Coder lineage", selectedTaskCallID)
		}
		entry, _ := rawEntry.(map[string]any)
		rows := manageWorktreeLaunchRows(entry)
		for _, rawRow := range rows {
			row, _ := rawRow.(map[string]any)
			if !agentruntime.IsCoderAgentName(asString(row["subagent"])) {
				continue
			}
			id := strings.TrimSpace(asString(row["child_session_id"]))
			if id == "" {
				return "", fmt.Errorf("task call %q contains a Coder launch without a durable child session id", selectedTaskCallID)
			}
			if childErr := strings.TrimSpace(asString(row["error"])); childErr != "" {
				return "", fmt.Errorf("task call %q child %q failed: %s", selectedTaskCallID, id, childErr)
			}
			selected = append(selected, id)
		}
		if len(selected) == 0 {
			return "", fmt.Errorf("task call %q contains no Coder children", selectedTaskCallID)
		}
	}
	selectedSet := map[string]bool{}
	for _, id := range selected {
		id = strings.TrimSpace(id)
		if id == "" || selectedSet[id] {
			return "", fmt.Errorf("invalid or duplicate selected session_id %q", id)
		}
		selectedSet[id] = true
	}
	type candidate struct {
		callID string
		index  int
		child  worktreeruntime.TaskIntegrationChild
	}
	candidates := make([]candidate, 0, len(selected))
	for callID, raw := range launchMap {
		if selectedTaskCallID != "" && callID != selectedTaskCallID {
			continue
		}
		entry, _ := raw.(map[string]any)
		for _, rawRow := range manageWorktreeLaunchRows(entry) {
			row, _ := rawRow.(map[string]any)
			id := strings.TrimSpace(asString(row["child_session_id"]))
			if !selectedSet[id] || !agentruntime.IsCoderAgentName(asString(row["subagent"])) {
				continue
			}
			childSession, found, sessionErr := r.sessions.GetSession(id)
			if sessionErr != nil {
				return "", fmt.Errorf("verify selected child session %q: %w", id, sessionErr)
			}
			if !found {
				return "", fmt.Errorf("verify selected child session %q: not found", id)
			}
			if childSession.AccountScopeID != parent.AccountScopeID || childSession.UserID != parent.UserID ||
				strings.TrimSpace(asString(childSession.Metadata["parent_session_id"])) != parentSessionID ||
				strings.TrimSpace(asString(childSession.Metadata["lineage_kind"])) != "delegated_subagent" ||
				!agentruntime.IsCoderAgentName(asString(childSession.Metadata["subagent"])) {
				return "", fmt.Errorf("selected child %q is not an owned Coder child of this parent", id)
			}
			path := strings.TrimSpace(firstNonEmptyString(childSession.WorktreeRootPath, childSession.WorkspacePath))
			baseCommit := strings.TrimSpace(asString(row["base_commit"]))
			headCommit := strings.TrimSpace(asString(row["head_commit"]))
			state, inspectErr := r.worktrees.VerifyTaskIntegrationWorkspace(parentPath, path, id, childSession.WorktreeBranch, baseCommit, headCommit)
			if inspectErr != nil {
				return "", fmt.Errorf("verify selected child %q lineage: %w", id, inspectErr)
			}
			if !state.Clean {
				return "", fmt.Errorf("selected child %q is dirty:\n%s", id, state.Status)
			}
			candidates = append(candidates, candidate{callID: callID, index: asInt(row["launch_index"], 0), child: worktreeruntime.TaskIntegrationChild{SessionID: id, BaseCommit: baseCommit, HeadCommit: state.HeadCommit}})
		}
	}
	if len(candidates) != len(selectedSet) {
		return "", errors.New("one or more selected children are missing or unauthorized by durable parent lineage")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].callID != candidates[j].callID {
			return candidates[i].callID < candidates[j].callID
		}
		return candidates[i].index < candidates[j].index
	})
	children := make([]worktreeruntime.TaskIntegrationChild, 0, len(candidates))
	for _, item := range candidates {
		children = append(children, item.child)
	}
	parentState, inspectErr := r.worktrees.InspectTaskWorkspace(parentPath)
	if inspectErr != nil {
		return "", fmt.Errorf("inspect current parent before integration: %w", inspectErr)
	}
	plan, err := r.worktrees.PrepareTaskIntegration(parentPath, parentState.HeadCommit, children)
	if err != nil {
		var conflict *worktreeruntime.TaskIntegrationConflictError
		if errors.As(err, &conflict) {
			encoded, _ := json.Marshal(map[string]any{
				"status": "conflict", "action": "integrate", "parent_unchanged": true,
				"parent_head": parentState.HeadCommit, "conflicting_child_session_id": conflict.SessionID,
				"conflicting_commit": conflict.Commit, "detail": conflict.Detail,
				"next_action": "Resolve the reported child-stack conflict in a dedicated child or choose a non-conflicting subset, then call integrate once with the final selected session_ids. Do not retry the same batch unchanged.",
				"path_id":     toolPathID("manage-worktree"),
			})
			return string(encoded), err
		}
		return "", err
	}
	result, err := r.worktrees.ApplyTaskIntegration(parentPath, plan)
	if err != nil {
		return "", err
	}
	childStates := make(map[string]string, len(result.Entries))
	for _, entry := range result.Entries {
		childStates[entry.SessionID] = "integrated"
	}
	response := map[string]any{"status": "ok", "action": "integrate", "parent_session_id": parentSessionID, "selected_count": len(selectedSet), "child_states": childStates, "resulting_parent_head": result.ResultingParentHead, "integration": result, "path_id": toolPathID("manage-worktree")}
	if selectedTaskCallID != "" {
		response["task_call_id"] = selectedTaskCallID
		response["selection"] = "complete_task_call"
	} else {
		response["selection"] = "explicit_session_ids"
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageWorktreeRecall(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil || r.worktrees == nil {
		return "", errors.New("manage-worktree recall requires session and worktree services")
	}
	parentSessionID := strings.TrimSpace(firstNonEmptyString(scope.SessionID, scope.Principal.SessionID))
	if parentSessionID == "" {
		return "", errors.New("manage-worktree recall requires current parent session_id")
	}
	parent, ok, err := r.sessions.GetSession(parentSessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("parent session %q not found", parentSessionID)
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
	parentPath, parentPathErr := r.manageWorktreeResolveWorkspacePath(scope, "")
	parentState := worktreeruntime.TaskWorkspaceState{}
	if parentPathErr == nil {
		parentState, parentPathErr = r.worktrees.InspectTaskWorkspace(parentPath)
	}
	launchMap, _ := parent.Metadata["task_launches"].(map[string]any)
	selectedTaskCallID := strings.TrimSpace(asString(args["task_call_id"]))
	if selectedTaskCallID != "" {
		if _, exists := launchMap[selectedTaskCallID]; !exists {
			return "", fmt.Errorf("task call %q is not present in this parent's durable Coder lineage", selectedTaskCallID)
		}
	}
	children := make([]map[string]any, 0)
	for callID, raw := range launchMap {
		if selectedTaskCallID != "" && callID != selectedTaskCallID {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, rawRow := range manageWorktreeLaunchRows(entry) {
			row, ok := rawRow.(map[string]any)
			if !ok || !agentruntime.IsCoderAgentName(asString(row["subagent"])) {
				continue
			}
			child := make(map[string]any, len(row)+2)
			for key, value := range row {
				child[key] = value
			}
			child["task_call_id"] = callID
			child["parent_session_id"] = parentSessionID
			path := strings.TrimSpace(firstNonEmptyString(asString(row["worktree_root_path"]), asString(row["workspace_path"])))
			childState := "blocked"
			if path != "" {
				if state, inspectErr := r.worktrees.InspectTaskWorkspace(path); inspectErr != nil {
					child["git_inspection_error"] = inspectErr.Error()
				} else {
					child["worktree_path"] = state.WorkspacePath
					child["child_branch"] = state.BranchName
					child["head_commit"] = state.HeadCommit
					child["git_status"] = state.Status
					child["worktree_clean"] = state.Clean
					recordedBranch := strings.TrimSpace(asString(row["worktree_branch"]))
					recordedBase := strings.TrimSpace(asString(row["base_commit"]))
					recordedHead := strings.TrimSpace(asString(row["head_commit"]))
					switch {
					case !state.Clean:
						childState = "dirty-recoverable"
					case recordedBranch == "" || recordedBase == "" || recordedHead == "" || recordedBase == recordedHead:
						childState = "blocked"
					case state.BranchName != recordedBranch || state.HeadCommit != recordedHead:
						childState = "stale"
					case parentPathErr != nil:
						child["parent_git_inspection_error"] = parentPathErr.Error()
						// Parent inspection affects integrated-vs-committed detection, not
						// whether this clean child has a durable committed handoff.
						childState = "committed"
					default:
						integrated, integrationErr := r.manageWorktreeCommitIntegrated(parentPath, recordedBase, recordedHead, parentState.HeadCommit)
						if integrationErr != nil {
							child["integration_inspection_error"] = integrationErr.Error()
							childState = "blocked"
						} else if integrated {
							childState = "integrated"
						} else {
							childState = "committed"
						}
					}
				}
			}
			child["child_state"] = childState
			children = append(children, child)
		}
	}
	sort.SliceStable(children, func(i, j int) bool {
		leftCall, rightCall := asString(children[i]["task_call_id"]), asString(children[j]["task_call_id"])
		if leftCall != rightCall {
			return leftCall < rightCall
		}
		return asInt(children[i]["launch_index"], 0) < asInt(children[j]["launch_index"], 0)
	})
	total := len(children)
	end := cursor + limit
	if end > total {
		end = total
	}
	page := []map[string]any{}
	if cursor < total {
		page = children[cursor:end]
	}
	nextCursor := 0
	if end < total {
		nextCursor = end
	}
	integrationInfo := map[string]any{
		"recommended_order": "review children in task call order then launch_index; call integrate once with only the complete selected committed session_ids, then recall once to verify",
		"state_labels":      []string{"committed", "dirty-recoverable", "integrated", "blocked", "stale", "conflicting"},
		"conflict_policy":   "the complete ordered stack is preflighted before mutation; conflicts return the exact failing commit and leave the parent unchanged",
		"automatic":         true,
	}
	committedIDs := make([]string, 0)
	stateCounts := map[string]int{}
	for _, child := range children {
		state := strings.TrimSpace(asString(child["child_state"]))
		stateCounts[state]++
		if strings.EqualFold(state, "committed") {
			committedIDs = append(committedIDs, strings.TrimSpace(asString(child["child_session_id"])))
		}
	}
	if len(committedIDs) > 0 {
		integrationInfo["ready_count"] = len(committedIDs)
		if selectedTaskCallID != "" && len(committedIDs) == total {
			integrationInfo["integrate_request"] = map[string]any{"action": "integrate", "task_call_id": selectedTaskCallID}
		} else {
			integrationInfo["ready_session_ids"] = committedIDs
			integrationInfo["integrate_request"] = map[string]any{"action": "integrate", "session_ids": committedIDs}
		}
	}
	response := map[string]any{
		"status": "ok", "action": "recall", "parent_session_id": parentSessionID,
		"children": page, "total": total, "returned": len(page), "cursor": cursor, "limit": limit,
		"next_cursor": nextCursor, "has_more": nextCursor > 0, "state_counts": stateCounts,
		"integration": integrationInfo,
		"path_id":     toolPathID("manage-worktree"), "details_truncated": false,
	}
	if selectedTaskCallID != "" {
		response["task_call_id"] = selectedTaskCallID
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) manageWorktreeCommitIntegrated(parentPath, baseCommit, headCommit, parentHead string) (bool, error) {
	if classifier, ok := r.worktrees.(manageWorktreeIntegrationClassifier); ok {
		return classifier.TaskCommitRangeIntegratedInto(parentPath, baseCommit, headCommit, parentHead)
	}
	// Compatibility for narrow test doubles and alternate implementations: the
	// canonical worktree service provides patch-equivalent range classification.
	return r.worktrees.TaskCommitDescendsFrom(parentPath, headCommit, parentHead)
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
		cfg, cfgErr := r.worktrees.GetConfigForPrincipal(scope.Principal, workspacePath)
		if cfgErr != nil {
			return "", fmt.Errorf("manage-worktree get config failed: %w", cfgErr)
		}
		config = cfg
	}

	workspaceName := filepath.Base(strings.TrimSpace(workspacePath))
	if r != nil && r.workspace != nil {
		if info, scopeErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, workspacePath); scopeErr == nil {
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
		"command":     command,
		"timeout_ms":  20000,
		"explanation": []any{"Inspect worktree-family Git commits and compare them with the current branch without changing repository state."},
		"category":    "read",
		"critical":    false,
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
	if strings.TrimSpace(scope.SessionID) == "" {
		return "", errors.New("manage-worktree calling context missing backend session_id")
	}
	if !scope.Principal.Valid() {
		return "", errors.New("manage-worktree calling context missing authenticated principal")
	}
	if requested != "" {
		return resolveWorkspacePath(scope, requested)
	}
	// The calling session is the authority for the parent workspace. The account's
	// currently selected binding may change independently while a run is active.
	if strings.TrimSpace(scope.PrimaryPath) != "" && strings.TrimSpace(scope.PrimaryPath) != "." {
		return resolveWorkspacePath(scope, scope.PrimaryPath)
	}
	if r != nil && r.workspace != nil {
		current, ok, err := r.workspace.CurrentBindingForPrincipal(scope.Principal)
		if err != nil {
			return "", fmt.Errorf("manage-worktree resolve current workspace from authenticated principal failed: %w", err)
		}
		if ok {
			if path := strings.TrimSpace(current.ResolvedPath); path != "" {
				return resolveWorkspacePath(scope, path)
			}
			if path := strings.TrimSpace(current.WorkspacePath); path != "" {
				return resolveWorkspacePath(scope, path)
			}
		}
	}
	return "", errors.New("manage-worktree calling context missing parent workspace_path")
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
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return "", fmt.Errorf("manage-skill inspect scan failed: %w", err)
	}
	defer store.Close()
	discovered, invalid, err := store.discover()
	if err != nil {
		return "", fmt.Errorf("manage-skill inspect scan failed: %w", err)
	}
	skills := make([]map[string]any, 0, len(discovered))
	for _, skill := range discovered {
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
		"skill_root":           store.rootPath,
		"skills":               skills,
		"invalid_skills":       invalid,
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

func (r *Runtime) manageAgentOrchestrationPolicy(scope WorkspaceScope, args map[string]any, update, confirm bool) (string, error) {
	if r == nil || r.orchestration == nil {
		return "", errors.New("manage-agent orchestration policy service is not configured")
	}
	accountScopeID := strings.TrimSpace(scope.Principal.AccountScopeID)
	before, err := r.orchestration.CurrentSubagentPolicyForAccount(accountScopeID)
	if err != nil {
		return "", err
	}
	if !update {
		return manageAgentEncodeResponse(map[string]any{"status": "ok", "action": "get_orchestration_policy", "orchestration_policy": before})
	}
	content, err := manageAgentContentObject(args)
	if err != nil {
		return "", err
	}
	if !confirm {
		return manageAgentEncodeResponse(map[string]any{"status": "approval_required", "action": "update_orchestration_policy", "before": before, "after": content, "confirm_required": true})
	}
	after, err := r.orchestration.UpdateSubagentPolicyMapForAccount(accountScopeID, content)
	if err != nil {
		return "", err
	}
	return manageAgentEncodeResponse(map[string]any{"status": "ok", "action": "update_orchestration_policy", "applied": true, "before": before, "after": after})
}

func manageAgentInstructionsText() string {
	return "Use manage-agent to inspect and manage saved agents, custom tools, and subagent transcripts. Call inspect/list first, then get before mutating an agent profile. If the user asks to create an agent but does not specify the agent type/mode or execution mode, clarify before creating. Agent modes: primary agents are user-selectable in Desktop/TUI; subagent agents are usable by primary agents for task delegation and are also user-selectable in Desktop/TUI; background agents are reserved for non-interactive system work and do not appear in the Desktop/TUI selector. Execution modes are explicit: runtime_mode=plan_auto means plan approval mode and forces exit_plan_mode_enabled=true; runtime_mode=read means direct read-only mode and forces exit_plan_mode_enabled=false; runtime_mode=readwrite means direct read/write mode and forces exit_plan_mode_enabled=false. Treat runtime_mode as authoritative for create/update. execution_setting is a legacy alias for direct read/readwrite only; do not use it with plan_auto and prefer runtime_mode for new changes. Tool presets are least-privilege grant suggestions and may imply a default direct mode, but they must not override an explicit user-requested runtime_mode. Use action=transcript/session_transcript with session_id from a task report_ref to read a child subagent transcript when inline task output was truncated or omitted. Inspect output includes tool_inventory.tools and tool_inventory.presets; each preset lists the concrete tools it grants. For create/update, prefer object-form `content`, set explicit `runtime_mode`, choose the smallest preset/bundle that fits the requested job, and avoid overscoping. Use explicit `tool_contract.tools.{tool}.enabled` overrides only when the user request needs narrower or slightly broader access than a preset; override names must come from tool_inventory.tools[].contract_name/name. Do not enable bash unless it is limited by explicit bash_prefixes. Do not use inherit_policy for model-created profiles. Custom tool actions use `content={name,kind,description?,command}` and assignment actions use top-level `agent` plus `tool_name`. Mutating actions return approval-ready previews unless confirm=true."
}

func isReservedCloneName(args map[string]any) bool {
	name := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(args, "name")))
	if content, err := manageAgentContentObject(args); err == nil {
		name = firstNonEmptyString(name, manageAgentStringArg(content, "name"))
	}
	return strings.EqualFold(strings.TrimSpace(name), "clone")
}

func (r *Runtime) manageAgentInspect(scope WorkspaceScope) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
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
		"tool_inventory":    manageAgentToolInventoryMap(r.Definitions(), customTools),
		"count":             len(agents),
		"custom_tools":      customTools,
		"custom_tool_count": len(customTools),
		"active_primary":    strings.TrimSpace(state.ActivePrimary),
		"active_subagent":   cloneStringMap(state.ActiveSubagent),
		"version":           state.Version,
		"supported_actions": []string{"inspect", "list", "get_orchestration_policy", "update_orchestration_policy", "get", "transcript", "session_transcript", "create", "update", "delete", "activate_primary", "set_active_subagent", "remove_active_subagent", "create_custom_tool", "update_custom_tool", "delete_custom_tool", "assign_custom_tool", "unassign_custom_tool"},
		"instructions":      manageAgentInstructionsText(),
		"examples": []map[string]any{
			{"action": "inspect"},
			{"action": "get", "agent": strings.TrimSpace(state.ActivePrimary)},
			{"action": "create", "agent": "review-bot", "content": map[string]any{"name": "review-bot", "mode": "subagent", "description": "Code review specialist usable by primary agents for delegation.", "prompt": "Review diffs and call out concrete risks.", "runtime_mode": "read", "tool_contract": map[string]any{"preset": "read_only", "tools": map[string]any{"bash": map[string]any{"enabled": false}}}}},
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

func (r *Runtime) manageAgentGet(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent get list failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(scope, args)
	if err != nil {
		return "", err
	}
	response := map[string]any{
		"status":               "ok",
		"action":               "get",
		"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name)),
		"tool_inventory":       manageAgentToolInventoryMap(r.Definitions(), manageAgentCustomToolMapsFromState(state.CustomTools)),
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

func (r *Runtime) manageAgentSessionTranscript(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil {
		return "", errors.New("manage-agent session transcript service is not configured")
	}
	sessionID := strings.TrimSpace(firstNonEmptyString(asString(args["session_id"]), asString(args["child_session_id"]), asString(args["id"])))
	if sessionID == "" {
		return "", errors.New("manage-agent transcript requires session_id")
	}
	limit := clampInt(asInt(args["limit"], 200), 1, 1000)
	afterGlobalSeq := uint64(0)
	if rawAfter := asInt(args["after_global_seq"], 0); rawAfter > 0 {
		afterGlobalSeq = uint64(rawAfter)
	}
	session, ok, err := r.sessions.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("manage-agent transcript session lookup failed: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if scopeAccount := strings.TrimSpace(scope.Principal.AccountScopeID); scopeAccount != "" && strings.TrimSpace(session.AccountScopeID) != "" && !strings.EqualFold(scopeAccount, strings.TrimSpace(session.AccountScopeID)) {
		return "", fmt.Errorf("session %q is not in the active account scope", sessionID)
	}
	messages, err := r.sessions.ListMessages(sessionID, afterGlobalSeq, limit)
	if err != nil {
		return "", fmt.Errorf("manage-agent transcript list messages failed: %w", err)
	}
	rows := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		rows = append(rows, map[string]any{
			"id":         strings.TrimSpace(message.ID),
			"global_seq": message.GlobalSeq,
			"role":       strings.TrimSpace(message.Role),
			"content":    strings.TrimSpace(message.Content),
			"metadata":   cloneMapStringAny(message.Metadata),
			"created_at": message.CreatedAt,
		})
	}
	response := map[string]any{
		"status":               "ok",
		"action":               "transcript",
		"session_id":           sessionID,
		"session":              manageAgentSessionSummaryMap(session),
		"messages":             rows,
		"count":                len(rows),
		"limit":                limit,
		"after_global_seq":     afterGlobalSeq,
		"path_id":              toolPathID("manage-agent"),
		"summary":              fmt.Sprintf("loaded %d transcript message(s) for session %s", len(rows), sessionID),
		"details_truncated":    len(rows) >= limit,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageAgentTranscriptSafetyText(messages)),
	}
	if len(rows) >= limit && len(messages) > 0 {
		response["next_after_global_seq"] = messages[len(messages)-1].GlobalSeq
	}
	return manageAgentEncodeResponse(response)
}

func (r *Runtime) manageAgentUpsert(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	if isReservedCloneName(args) {
		return "", errors.New("agent name clone is reserved for the built-in parent-copying subagent")
	}
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	input, err := manageAgentUpsertInputFromArgs(args)
	if err != nil {
		return "", err
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	if err := validateManageAgentMutationInput(input, mustExist, manageAgentKnownToolSet(r.Definitions(), state.CustomTools)); err != nil {
		return "", err
	}
	preview, err := r.previewUpsertAgentForScope(scope, input)
	if err != nil {
		return "", err
	}
	if mustExist && !preview.Exists {
		return "", fmt.Errorf("agent %q does not exist", preview.After.Name)
	}
	if !mustExist && preview.Exists {
		return "", fmt.Errorf("agent %q already exists; use update", preview.After.Name)
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
		profile, _, _, err := r.upsertAgentForScope(scope, input)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
		if stateErr != nil {
			return "", fmt.Errorf("manage-agent list state failed: %w", stateErr)
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"agent":                manageAgentProfileMap(profile, strings.EqualFold(strings.TrimSpace(updatedState.ActivePrimary), strings.TrimSpace(profile.Name)), manageAgentPurposesForProfile(updatedState.ActiveSubagent, profile.Name)),
			"change":               change,
			"tool_inventory":       manageAgentToolInventoryMap(r.Definitions(), manageAgentCustomToolMapsFromState(updatedState.CustomTools)),
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
		"tool_inventory":       manageAgentToolInventoryMap(r.Definitions(), manageAgentCustomToolMapsFromState(state.CustomTools)),
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

func (r *Runtime) manageAgentDelete(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	if isReservedCloneName(args) {
		return "", errors.New("agent name clone is reserved for the built-in parent-copying subagent")
	}
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(scope, args)
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
		result, _, _, err := r.deleteAgentForScope(scope, profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentActivatePrimary(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	profile, err := r.lookupManageAgentProfile(scope, args)
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
		active, _, _, err := r.activatePrimaryForScope(scope, profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentSetActiveSubagent(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	purpose, name, err := manageAgentAssignmentArgs(args)
	if err != nil {
		return "", err
	}
	profile, ok, err := r.getAgentProfileForScope(scope, name)
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
		assignments, _, _, err := r.setActiveSubagentForScope(scope, purpose, profile.Name)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentRemoveActiveSubagent(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
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
		assignments, _, _, err := r.deleteActiveSubagentForScope(scope, purpose)
		if err != nil {
			return "", err
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentCustomToolUpsert(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	definition, err := manageAgentCustomToolDefinitionFromArgs(args)
	if err != nil {
		return "", err
	}
	current, exists, err := r.getCustomToolForScope(scope, definition.Name)
	if err != nil {
		return "", err
	}
	if mustExist && !exists {
		return "", fmt.Errorf("custom tool %q does not exist", definition.Name)
	}
	if !mustExist && exists {
		return "", fmt.Errorf("custom tool %q already exists; use update", definition.Name)
	}
	state, err := r.listStateForScope(scope, 500)
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
		stored, err := r.putCustomToolForScope(scope, definition)
		if err != nil {
			return "", err
		}
		change["after"] = manageAgentCustomToolMap(stored)
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentDeleteCustomTool(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	toolName, err := manageAgentCustomToolNameFromArgs(args)
	if err != nil {
		return "", err
	}
	definition, ok, err := r.getCustomToolForScope(scope, toolName)
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
		deleted, err := r.deleteCustomToolForScope(scope, toolName)
		if err != nil {
			return "", err
		}
		if !deleted {
			return "", fmt.Errorf("custom tool %q not found", toolName)
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) manageAgentAssignCustomTool(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	return r.manageAgentCustomToolAssignment(scope, args, true, confirm)
}

func (r *Runtime) manageAgentUnassignCustomTool(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	return r.manageAgentCustomToolAssignment(scope, args, false, confirm)
}

func (r *Runtime) manageAgentCustomToolAssignment(scope WorkspaceScope, args map[string]any, assign, confirm bool) (string, error) {
	if r == nil || r.agents == nil {
		return "", errors.New("manage-agent service is not configured")
	}
	state, err := r.listStateForScope(scope, 500)
	if err != nil {
		return "", fmt.Errorf("manage-agent list state failed: %w", err)
	}
	agentName, toolName, err := manageAgentCustomToolAssignmentArgs(args)
	if err != nil {
		return "", err
	}
	profile, ok, err := r.getAgentProfileForScope(scope, agentName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("agent %q not found", agentName)
	}
	assignedBefore := manageAgentProfileHasToolAssignment(profile, toolName)
	var definition *pebblestore.AgentCustomToolDefinition
	if assign {
		current, ok, err := r.getCustomToolForScope(scope, toolName)
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
		if current, ok, err := r.getCustomToolForScope(scope, toolName); err != nil {
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
			updatedProfile, _, _, applyErr = r.assignCustomToolForScope(scope, profile.Name, toolName)
		} else {
			updatedProfile, _, _, applyErr = r.unassignCustomToolForScope(scope, profile.Name, toolName)
		}
		if applyErr != nil {
			return "", applyErr
		}
		updatedState, stateErr := r.listStateForScope(scope, 500)
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

func (r *Runtime) lookupManageAgentProfile(scope WorkspaceScope, args map[string]any) (pebblestore.AgentProfile, error) {
	if r == nil || r.agents == nil {
		return pebblestore.AgentProfile{}, errors.New("manage-agent service is not configured")
	}
	name := strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "agent"), manageAgentStringArg(args, "name")))
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("manage-agent requires agent or name")
	}
	profile, ok, err := r.getAgentProfileForScope(scope, name)
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
		Name:               name,
		Mode:               strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "mode"), manageAgentStringArg(content, "mode"))),
		Description:        strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "description"), manageAgentStringArg(content, "description"))),
		Prompt:             strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "prompt"), manageAgentStringArg(content, "prompt"))),
		RuntimeMode:        strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "runtime_mode"), manageAgentStringArg(content, "runtime_mode"))),
		DefaultSessionMode: strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "default_session_mode"), manageAgentStringArg(content, "default_session_mode"))),
		ExecutionSetting:   strings.TrimSpace(firstNonEmptyString(manageAgentStringArg(args, "execution_setting"), manageAgentStringArg(content, "execution_setting"))),
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
	if value, ok := manageAgentValue(args, content, "tool_contract"); ok {
		contract, err := manageAgentToolContractFromValue(value)
		if err != nil {
			return agentruntime.UpsertInput{}, err
		}
		input.ToolContract = contract
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

func validateManageAgentMutationInput(input agentruntime.UpsertInput, mustExist bool, knownTools map[string]struct{}) error {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return errors.New("manage-agent requires agent or name")
	}
	if err := validateManageAgentMode(input.Mode, mustExist); err != nil {
		return err
	}
	if err := validateManageAgentRuntimeMode(input.RuntimeMode); err != nil {
		return err
	}
	if err := validateManageAgentExecutionSetting(input.ExecutionSetting); err != nil {
		return err
	}
	if input.ToolScope != nil {
		return errors.New("manage-agent create/update requires tool_contract; tool_scope is legacy and cannot be set by model-created profiles")
	}
	if err := validateManageAgentToolContract(input.ToolContract, knownTools); err != nil {
		return err
	}
	hasMutation := strings.TrimSpace(input.Mode) != "" ||
		strings.TrimSpace(input.Description) != "" ||
		strings.TrimSpace(input.Prompt) != "" ||
		strings.TrimSpace(input.RuntimeMode) != "" ||
		strings.TrimSpace(input.ExecutionSetting) != "" ||
		input.ProviderSet || input.ModelSet || input.ThinkingSet ||
		input.ExitPlanModeEnabled != nil || input.ToolContract != nil || input.Enabled != nil
	if mustExist {
		if !hasMutation {
			return errors.New("manage-agent update requires at least one field to change")
		}
		return validateManageAgentRuntimeAliases(input)
	}
	if strings.TrimSpace(input.Mode) == "" {
		return errors.New("manage-agent create requires content.mode")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return errors.New("manage-agent create requires content.prompt")
	}
	runtimeMode := pebblestore.NormalizeAgentRuntimeMode(input.RuntimeMode)
	if runtimeMode == "" && input.ExitPlanModeEnabled != nil && *input.ExitPlanModeEnabled {
		runtimeMode = pebblestore.AgentRuntimeModePlanAuto
	}
	if runtimeMode == "" {
		runtimeMode = pebblestore.NormalizeAgentExecutionSetting(input.ExecutionSetting)
	}
	if runtimeMode == "" {
		runtimeMode = pebblestore.AgentRuntimeModeForToolContractWithDefault(input.ToolContract, true)
	}
	if runtimeMode == "" {
		return errors.New("manage-agent create requires content.runtime_mode (plan_auto, read, or readwrite)")
	}
	if err := validateManageAgentRuntimeAliases(input); err != nil {
		return err
	}
	if runtimeMode != pebblestore.AgentRuntimeModePlanAuto && input.ToolContract == nil {
		return errors.New("manage-agent create requires content.tool_contract.preset; inspect tool_inventory.presets and choose the least-privilege bundle")
	}
	return nil
}

func validateManageAgentRuntimeAliases(input agentruntime.UpsertInput) error {
	runtimeMode := pebblestore.NormalizeAgentRuntimeMode(input.RuntimeMode)
	executionSetting := pebblestore.NormalizeAgentExecutionSetting(input.ExecutionSetting)
	if runtimeMode == pebblestore.AgentRuntimeModePlanAuto && strings.TrimSpace(input.ExecutionSetting) != "" {
		return errors.New("manage-agent runtime_mode=plan_auto cannot include execution_setting")
	}
	if runtimeMode != "" && runtimeMode != pebblestore.AgentRuntimeModePlanAuto && executionSetting != "" && executionSetting != runtimeMode {
		return fmt.Errorf("manage-agent runtime_mode=%s contradicts execution_setting=%s", runtimeMode, executionSetting)
	}
	if input.ExitPlanModeEnabled != nil {
		if *input.ExitPlanModeEnabled && runtimeMode != "" && runtimeMode != pebblestore.AgentRuntimeModePlanAuto {
			return errors.New("manage-agent direct runtime cannot have exit_plan_mode_enabled=true; use runtime_mode=plan_auto")
		}
		if !*input.ExitPlanModeEnabled && runtimeMode == pebblestore.AgentRuntimeModePlanAuto {
			return errors.New("manage-agent runtime_mode=plan_auto contradicts exit_plan_mode_enabled=false")
		}
	}
	return nil
}

func validateManageAgentRuntimeMode(mode string) error {
	if strings.TrimSpace(mode) == "" {
		return nil
	}
	if pebblestore.NormalizeAgentRuntimeMode(mode) == "" {
		return fmt.Errorf("manage-agent unsupported runtime_mode %q; use plan_auto, read, or readwrite", strings.TrimSpace(mode))
	}
	return nil
}

func validateManageAgentMode(mode string, _ bool) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return nil
	}
	if mode == agentruntime.ModePrimary || mode == agentruntime.ModeSubagent || mode == agentruntime.ModeBackground {
		return nil
	}
	return fmt.Errorf("manage-agent unsupported mode %q; use primary, subagent, or background", mode)
}

func validateManageAgentExecutionSetting(setting string) error {
	if strings.TrimSpace(setting) == "" {
		return nil
	}
	if pebblestore.NormalizeAgentExecutionSetting(setting) == "" {
		return fmt.Errorf("manage-agent unsupported execution_setting %q; use read or readwrite", strings.TrimSpace(setting))
	}
	return nil
}

func validateManageAgentToolContract(contract *pebblestore.AgentToolContract, knownTools map[string]struct{}) error {
	if contract == nil {
		return nil
	}
	if contract.InheritPolicy {
		return errors.New("manage-agent tool_contract.inherit_policy is not allowed for model-created profiles; choose an explicit preset and scoped overrides")
	}
	preset := strings.ToLower(strings.TrimSpace(contract.Preset))
	if preset == "" {
		return errors.New("manage-agent tool_contract requires preset; inspect tool_inventory.presets and choose the least-privilege bundle")
	}
	if _, ok := manageAgentToolPresetByID(preset); !ok {
		return fmt.Errorf("manage-agent unsupported tool_contract.preset %q; inspect tool_inventory.presets for supported bundles", preset)
	}
	for rawName, cfg := range contract.Tools {
		name := manageAgentCanonicalToolName(rawName)
		if _, ok := knownTools[name]; name == "" || !ok {
			return fmt.Errorf("manage-agent tool_contract.tools.%s is not in the advertised tool_inventory", strings.TrimSpace(rawName))
		}
		if len(cfg.BashPrefixes) > 0 && name != "bash" {
			return fmt.Errorf("manage-agent tool_contract.tools.%s.bash_prefixes is only valid for bash", rawName)
		}
		if name == "bash" && cfg.Enabled != nil && *cfg.Enabled && len(cfg.BashPrefixes) == 0 {
			return errors.New("manage-agent tool_contract.tools.bash enabled=true requires explicit bash_prefixes")
		}
		for _, prefix := range cfg.BashPrefixes {
			if strings.TrimSpace(prefix) == "" {
				return errors.New("manage-agent tool_contract.tools.bash.bash_prefixes cannot contain empty entries")
			}
		}
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

func manageAgentToolContractFromValue(value any) (*pebblestore.AgentToolContract, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("manage-agent tool_contract must be an object")
	}
	contract := &pebblestore.AgentToolContract{}
	contract.Preset = strings.TrimSpace(asString(object["preset"]))
	if inherit, ok := object["inherit_policy"].(bool); ok {
		contract.InheritPolicy = inherit
	}
	if rawTools, ok := object["tools"]; ok && rawTools != nil {
		toolsObject, ok := rawTools.(map[string]any)
		if !ok {
			return nil, errors.New("manage-agent tool_contract.tools must be an object")
		}
		contract.Tools = make(map[string]pebblestore.AgentToolConfig, len(toolsObject))
		for rawName, rawConfig := range toolsObject {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			configObject, ok := rawConfig.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("manage-agent tool_contract.tools.%s must be an object", name)
			}
			config := pebblestore.AgentToolConfig{}
			if rawEnabled, ok := configObject["enabled"]; ok {
				enabled, ok := rawEnabled.(bool)
				if !ok {
					return nil, fmt.Errorf("manage-agent tool_contract.tools.%s.enabled must be boolean", name)
				}
				config.Enabled = pebblestore.BoolPtr(enabled)
			}
			config.BashPrefixes = manageAgentStringSlice(configObject["bash_prefixes"])
			contract.Tools[name] = config
		}
	}
	return pebblestore.NormalizeAgentToolContract(contract), nil
}

func manageAgentEffectiveExecutionSetting(profile pebblestore.AgentProfile) string {
	if runtimeMode := pebblestore.AgentProfileRuntimeMode(profile); runtimeMode != "" {
		return runtimeMode
	}
	baseline := pebblestore.NormalizeAgentExecutionSetting(profile.ExecutionSetting)
	if profile.ToolContract == nil && profile.ToolScope == nil {
		return baseline
	}
	writeTools := map[string]bool{
		"write":      baseline == pebblestore.AgentExecutionSettingReadWrite,
		"edit":       baseline == pebblestore.AgentExecutionSettingReadWrite,
		"bash":       false,
		"task":       false,
		"git_add":    false,
		"git_commit": false,
	}
	applyPreset := func(preset string) {
		preset = strings.ToLower(strings.TrimSpace(preset))
		if preset == "" {
			return
		}
		for name := range writeTools {
			writeTools[name] = false
		}
		switch preset {
		case "read_write":
			writeTools["write"] = true
			writeTools["edit"] = true
		case "bash_git_only":
			writeTools["bash"] = true
		case "background_commit":
			writeTools["git_add"] = true
			writeTools["git_commit"] = true
		}
	}
	setWriteTool := func(name string, enabled bool) {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, ok := writeTools[name]; ok {
			writeTools[name] = enabled
		}
	}
	if contract := profile.ToolContract; contract != nil {
		applyPreset(contract.Preset)
		for name, cfg := range contract.Tools {
			if cfg.Enabled != nil {
				setWriteTool(name, *cfg.Enabled)
			}
			if len(cfg.BashPrefixes) > 0 {
				setWriteTool(name, true)
			}
		}
	} else if scope := profile.ToolScope; scope != nil {
		applyPreset(scope.Preset)
		for _, name := range scope.AllowTools {
			setWriteTool(name, true)
		}
		for _, name := range scope.DenyTools {
			setWriteTool(name, false)
		}
		if len(scope.BashPrefixes) > 0 {
			setWriteTool("bash", true)
		}
	}
	for _, enabled := range writeTools {
		if enabled {
			return pebblestore.AgentExecutionSettingReadWrite
		}
	}
	return pebblestore.AgentExecutionSettingRead
}

func manageAgentProfileMap(profile pebblestore.AgentProfile, activePrimary bool, purposes []string) map[string]any {
	payload := map[string]any{
		"name":                        strings.TrimSpace(profile.Name),
		"mode":                        strings.TrimSpace(profile.Mode),
		"description":                 strings.TrimSpace(profile.Description),
		"provider":                    strings.TrimSpace(profile.Provider),
		"model":                       strings.TrimSpace(profile.Model),
		"thinking":                    strings.TrimSpace(profile.Thinking),
		"prompt":                      strings.TrimSpace(profile.Prompt),
		"runtime_mode":                pebblestore.AgentProfileRuntimeMode(profile),
		"default_session_mode":        pebblestore.AgentProfileDefaultSessionMode(profile),
		"execution_setting":           strings.TrimSpace(profile.ExecutionSetting),
		"effective_execution_setting": manageAgentEffectiveExecutionSetting(profile),
		"exit_plan_mode_enabled":      pebblestore.AgentExitPlanModeEnabled(profile),
		"tool_scope":                  manageAgentToolScopeMap(profile.ToolScope),
		"tool_contract":               manageAgentToolContractMap(profile.ToolContract),
		"enabled":                     profile.Enabled,
		"updated_at":                  profile.UpdatedAt,
		"active_primary":              activePrimary,
		"active_purposes":             append([]string(nil), purposes...),
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

func manageAgentToolScopeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"preset":         map[string]any{"type": "string"},
			"allow_tools":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"deny_tools":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"bash_prefixes":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"inherit_policy": map[string]any{"type": "boolean"},
		},
		"additionalProperties": false,
	}
}

func manageAgentToolContractSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"preset":         map[string]any{"type": "string"},
			"inherit_policy": map[string]any{"type": "boolean"},
			"tools": map[string]any{
				"type": "object",
				"additionalProperties": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"enabled":       map[string]any{"type": "boolean"},
						"bash_prefixes": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"additionalProperties": false,
				},
			},
		},
		"additionalProperties": false,
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

func manageAgentCustomToolMapsFromState(definitions []pebblestore.AgentCustomToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, manageAgentCustomToolMap(definition))
	}
	return out
}

func manageAgentToolInventoryMap(definitions []Definition, customTools []map[string]any) map[string]any {
	tools := make([]map[string]any, 0, len(definitions)+len(customTools))
	seen := make(map[string]struct{}, len(definitions)+len(customTools))
	for _, definition := range definitions {
		displayName := strings.TrimSpace(definition.Name)
		name := manageAgentCanonicalToolName(displayName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, map[string]any{
			"name":          displayName,
			"contract_name": name,
			"description":   strings.TrimSpace(definition.Description),
			"group":         manageAgentToolGroup(name),
			"kind":          "built_in",
		})
	}
	for _, customTool := range customTools {
		name := manageAgentCanonicalToolName(asString(customTool["name"]))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, map[string]any{
			"name":          strings.TrimSpace(asString(customTool["name"])),
			"contract_name": name,
			"description":   strings.TrimSpace(asString(customTool["description"])),
			"group":         "custom",
			"kind":          "custom",
		})
	}
	sort.Slice(tools, func(i, j int) bool {
		left, _ := tools[i]["contract_name"].(string)
		right, _ := tools[j]["contract_name"].(string)
		return strings.TrimSpace(left) < strings.TrimSpace(right)
	})
	return map[string]any{
		"tools":        tools,
		"tool_count":   len(tools),
		"custom_tools": customTools,
		"presets":      manageAgentToolPresetInventory(),
		"schemas": map[string]any{
			"tool_scope":    manageAgentToolScopeSchema(),
			"tool_contract": manageAgentToolContractSchema(),
		},
	}
}

func manageAgentToolPresetInventory() []map[string]any {
	presets := manageAgentToolPresets()
	out := make([]map[string]any, 0, len(presets))
	for _, preset := range presets {
		entry := map[string]any{
			"id":                  preset.ID,
			"label":               preset.Label,
			"description":         preset.Description,
			"enabled_tools":       append([]string(nil), preset.EnabledTools...),
			"disabled_by_default": append([]string(nil), preset.DisabledByDefault...),
		}
		if len(preset.BashPrefixes) > 0 {
			entry["bash_prefixes"] = append([]string(nil), preset.BashPrefixes...)
		}
		out = append(out, entry)
	}
	return out
}

type manageAgentToolPresetDefinition struct {
	ID                string
	Label             string
	Description       string
	EnabledTools      []string
	DisabledByDefault []string
	BashPrefixes      []string
}

func manageAgentToolPresets() []manageAgentToolPresetDefinition {
	return []manageAgentToolPresetDefinition{
		{
			ID:                "custom",
			Label:             "Custom",
			Description:       "Fully custom tool contract controlled by explicit per-tool allow/block choices.",
			EnabledTools:      []string{},
			DisabledByDefault: []string{},
		},
		{
			ID:                "read_only",
			Label:             "Read only",
			Description:       "Inspect workspace files and web content without file mutation or shell execution.",
			EnabledTools:      []string{"read", "search", "list", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"write", "edit", "bash", "task"},
		},
		{
			ID:                "read_write",
			Label:             "Read/write",
			Description:       "Inspect and edit workspace files without shell execution or delegation.",
			EnabledTools:      []string{"read", "search", "list", "write", "edit", "websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"bash", "task"},
		},
		{
			ID:                "bash_git_only",
			Label:             "Git shell only",
			Description:       "Allow read tools plus bash restricted to git status/diff/log/show prefixes.",
			EnabledTools:      []string{"read", "search", "list", "bash", "skill_use", "plan_manage", "ask_user", "exit_plan_mode"},
			DisabledByDefault: []string{"write", "edit", "task"},
			BashPrefixes:      []string{"git status", "git diff", "git log", "git show"},
		},
		{
			ID:                "background_commit",
			Label:             "Background commit",
			Description:       "Allow only read/list/search plus git status/diff/add/commit tools for durable commits.",
			EnabledTools:      []string{"read", "search", "list", "git_status", "git_diff", "git_add", "git_commit"},
			DisabledByDefault: []string{"write", "edit", "bash", "task"},
		},
	}
}

func manageAgentToolPresetByID(id string) (manageAgentToolPresetDefinition, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, preset := range manageAgentToolPresets() {
		if preset.ID == id {
			return preset, true
		}
	}
	return manageAgentToolPresetDefinition{}, false
}

func manageAgentToolGroup(name string) string {
	switch manageAgentCanonicalToolName(name) {
	case "read", "search", "list":
		return "workspace_inspection"
	case "write", "edit":
		return "file_mutation"
	case "bash":
		return "shell"
	case "websearch", "webfetch", "webdownload":
		return "web"
	case "task":
		return "delegation"
	case "ask-user", "ask_user", "exit_plan_mode", "plan_manage":
		return "conversation_control"
	case "git_status", "git_diff", "git_add", "git_commit":
		return "git_commit"
	case "skill-use", "skill_use", "manage-skill", "manage_skill", "manage-agent", "manage_agent", "manage-theme", "manage_theme", "manage-worktree", "manage_worktree", "manage_todos":
		return "management"
	default:
		return "other"
	}
}

func manageAgentKnownToolSet(definitions []Definition, customTools []pebblestore.AgentCustomToolDefinition) map[string]struct{} {
	known := map[string]struct{}{
		"ask_user":       {},
		"exit_plan_mode": {},
		"plan_manage":    {},
		"task":           {},
	}
	for _, definition := range definitions {
		name := manageAgentCanonicalToolName(definition.Name)
		if name != "" {
			known[name] = struct{}{}
		}
	}
	for _, customTool := range customTools {
		name := manageAgentCanonicalToolName(customTool.Name)
		if name != "" {
			known[name] = struct{}{}
		}
	}
	return known
}

func manageAgentCanonicalToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ask-user", "ask_user":
		return "ask_user"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	case "skill-use", "skill_use":
		return "skill_use"
	case "manage-skill", "manage_skill":
		return "manage_skill"
	case "manage-agent", "manage_agent":
		return "manage_agent"
	case "manage-theme", "manage_theme":
		return "manage_theme"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-actions", "manage_actions":
		return "manage_actions"
	case "manage-artifact", "manage_artifact":
		return "manage_artifact"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	default:
		return strings.ToLower(strings.TrimSpace(name))
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

func manageAgentTranscriptSafetyText(messages []pebblestore.MessageSnapshot) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(strings.TrimSpace(message.Role))
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(message.Content))
		b.WriteString("\n")
	}
	return b.String()
}

func manageAgentSessionSummaryMap(session pebblestore.SessionSnapshot) map[string]any {
	return map[string]any{
		"id":                 strings.TrimSpace(session.ID),
		"title":              strings.TrimSpace(session.Title),
		"mode":               strings.TrimSpace(session.Mode),
		"workspace_path":     strings.TrimSpace(session.WorkspacePath),
		"workspace_name":     strings.TrimSpace(session.WorkspaceName),
		"message_count":      session.MessageCount,
		"last_message_at":    session.LastMessageAt,
		"parent_session_id":  mapString(session.Metadata, "parent_session_id"),
		"lineage_kind":       mapString(session.Metadata, "lineage_kind"),
		"lineage_label":      mapString(session.Metadata, "lineage_label"),
		"requested_subagent": mapString(session.Metadata, "requested_subagent"),
		"subagent":           mapString(session.Metadata, "subagent"),
	}
}

func cloneMapStringAny(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	return out
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
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return "", fmt.Errorf("manage-skill get open skill root failed: %w", err)
	}
	defer store.Close()
	raw, err := store.read(matched.CanonicalName)
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
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return "", fmt.Errorf("manage-skill open skill root failed: %w", err)
	}
	defer store.Close()
	path := store.skillPath(canonical)
	revision, beforeBytes, readErr := store.revision(canonical)
	if readErr != nil {
		return "", fmt.Errorf("manage-skill read existing skill failed: %w", readErr)
	}
	before := string(beforeBytes)
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
			"kind":              "skill_change",
			"operation":         action,
			"path":              path,
			"before":            before,
			"after":             formatted,
			"expected_revision": revision,
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
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return "", fmt.Errorf("manage-skill delete open skill root failed: %w", err)
	}
	defer store.Close()
	revision, beforeBytes, err := store.revision(matched.CanonicalName)
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
			"kind":              "skill_change",
			"operation":         "delete",
			"path":              matched.Path,
			"before":            before,
			"after":             "",
			"expected_revision": revision,
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
	canonical := manageSkillRequestedCanonical(args)
	store, err := openManageSkillStore(scope, true)
	if err != nil {
		return "", fmt.Errorf("manage-skill open skill root failed: %w", err)
	}
	defer store.Close()
	if path != store.skillPath(canonical) {
		return "", fmt.Errorf("manage-skill path %q is outside workspace skill root", path)
	}
	if err := store.write(canonical, []byte(after), mustExist, strings.TrimSpace(asString(args["expected_revision"]))); err != nil {
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
	canonical := strings.TrimSpace(asString(proposal["skill"].(map[string]any)["canonical_name"]))
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return "", fmt.Errorf("manage-skill delete open skill root failed: %w", err)
	}
	defer store.Close()
	if path != store.skillPath(canonical) {
		return "", fmt.Errorf("manage-skill path %q is outside workspace skill root", path)
	}
	if err := store.delete(canonical, strings.TrimSpace(asString(args["expected_revision"]))); err != nil {
		return "", fmt.Errorf("manage-skill delete failed: %w", err)
	}
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
	store, err := openManageSkillStore(scope, false)
	if err != nil {
		return discovery.SkillSource{}, fmt.Errorf("manage-skill scan failed: %w", err)
	}
	defer store.Close()
	skills, _, err := store.discover()
	if err != nil {
		return discovery.SkillSource{}, fmt.Errorf("manage-skill scan failed: %w", err)
	}
	target := normalizeSkillLookup(requested)
	for _, candidate := range skills {
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

func collectListEntries(rootPath *rootedWorkspacePath, mode string, maxDepth int) ([]listEntry, bool, error) {
	entries := make([]listEntry, 0, 256)
	scanLimited := false

	fsys := rootPath.root.FS()
	walkErr := fs.WalkDir(fsys, rootPath.relative, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == rootPath.relative {
			return nil
		}

		relative, relErr := filepath.Rel(rootPath.relative, path)
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
				return fs.SkipDir
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
	case "manage-actions", "manage_actions":
		return "manage_actions"
	case "manage-artifact", "manage_artifact":
		return "manage_artifact"
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
	case "manage-actions", "manage_actions":
		return "tool.manage-actions.v1"
	case "manage-artifact", "manage_artifact":
		return "tool.manage-artifact.v1"
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

func findSummaryForQueries(queries []string, root string, count int, truncated, timedOut bool, mode string) string {
	label := "find"
	queries = compactSearchQueries(queries)
	if len(queries) == 1 {
		label += " " + fmt.Sprintf("%q", truncateSummary(queries[0], 80))
	} else if len(queries) > 1 {
		label += " [" + countSummary(len(queries), "query", "queries") + "]"
	}
	if root = strings.TrimSpace(truncateSummary(root, 120)); root != "" {
		label += " in " + root
	}
	mode = strings.TrimSpace(mode)
	itemSingular, itemPlural := "path", "paths"
	if mode == "directories" {
		itemSingular, itemPlural = "directory", "directories"
	} else if mode == "files" || mode == "glob" {
		itemSingular, itemPlural = "file", "files"
	}
	notes := []string{countSummary(count, itemSingular, itemPlural)}
	if mode != "" {
		notes = append(notes, mode)
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

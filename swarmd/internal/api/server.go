package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"

	actionruntime "swarm/packages/swarmd/internal/action"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	integrationruntime "swarm/packages/swarmd/internal/integration"
	"swarm/packages/swarmd/internal/longsessiondiag"
	mcpruntime "swarm/packages/swarmd/internal/mcp"
	"swarm/packages/swarmd/internal/mediastaging"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/update"
	"swarm/packages/swarmd/internal/voice"
	"swarm/packages/swarmd/internal/webpush"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type codexOAuthSession struct {
	CodeVerifier        string
	State               string
	UserID              string
	AccountScopeID      string
	Provider            string
	Label               string
	Active              bool
	Method              string
	AuthURL             string
	VerificationURL     string
	UserCode            string
	ExpiresAt           time.Time
	DeviceAuthorization *codex.DeviceAuthorization
	Status              string
	Error               string
	Credential          *auth.CredentialStatus
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const (
	v3LivePatchDefaultEnabled = true

	maxSTTDecodedAudioBytes = 25 << 20
	maxSTTRequestBodyBytes  = (maxSTTDecodedAudioBytes*4)/3 + (64 << 10)
)

type Server struct {
	auth                        *auth.Service
	agents                      *agentruntime.Service
	model                       *model.Service
	modelProfiles               *modelprofile.Service
	agentModelSettings          *agentmodelsettings.Service
	agentModelSettingsStore     *pebblestore.AgentModelSettingsStore // complete-record bootstrap only
	runner                      runService
	runStreams                  *runControlAllocator
	v3RealtimeOutbox            *v3RealtimeOutboxHub
	v3LiveHub                   *v3LiveHub
	v3LivePatchEnabled          bool
	v3SyncCursors               *v3SyncCursorKeyring
	v3SessionExecutor           *sessionV3Executor
	planLifecycle               *sessionruntime.PlanLifecycleService
	sessions                    *sessionruntime.Service
	workspace                   *workspace.Service
	discovery                   *discovery.Service
	worktrees                   worktreeService
	mcp                         mcpService
	security                    *security.Service
	providers                   *registry.Registry
	codexAccount                codexAccountClient
	perm                        permissionService
	notifications               notificationService
	webPush                     *webpush.Service
	hub                         *stream.Hub
	events                      *pebblestore.EventLog
	voice                       *voice.Service
	uiSettings                  *uisettings.Service
	todos                       *todo.Service
	actions                     *actionruntime.Service
	actionRuns                  *actionruntime.Runner
	aiTasks                     aiTaskEnqueuer
	swarm                       swarmService
	update                      *update.Service
	topology                    *topologyruntime.Service
	swarmDesktopTargetSelection *pebblestore.SwarmDesktopTargetSelectionStore
	videoThreads                *pebblestore.VideoThreadStore
	imageThreads                *pebblestore.ImageThreadStore
	imageGen                    *imagegen.Service
	integrations                *integrationruntime.Service
	dataDir                     string
	startupConfigPath           string
	startedAt                   time.Time
	bypassPermissions           bool
	longSessionDiagnostics      *longsessiondiag.Recorder
	mediaStaging                *mediastaging.Service

	longSessionDesktopSampleLogOnce sync.Once

	codexOAuthMu       sync.Mutex
	codexOAuthSessions map[string]*codexOAuthSession

	shuttingDown           atomic.Bool
	activeRunMu            sync.Mutex
	runCtx                 context.Context
	runCancel              context.CancelFunc
	runWG                  sync.WaitGroup
	activeRuns             atomic.Int32
	requestStop            func(reason string)
	desktopLocalSessions   *desktopLocalSessionManager
	identityService        *identity.Service
	identitySessions       *identity.SessionService
	gitRealtime            *gitRealtimeManager
	swarmStore             *pebblestore.SwarmStore
	tailscaleServePolicy   *pebblestore.TailscaleServeAllowlistStore
	tailscaleServeDetector tailscaleServeDetector
	reviewCommitMu         sync.Mutex
	reviewCommitActive     map[string]string
	reviewAutoArchiveOnce  sync.Once
}

type aiTaskEnqueuer interface {
	Enqueue(pebblestore.WorkspaceTodoItem) bool
}

type codexAccountClient interface {
	GetAccountUsage(context.Context) (codex.AccountUsage, error)
	GetResetCredits(context.Context) (codex.ResetCredits, error)
	ConsumeResetCredit(context.Context, codex.ConsumeResetCreditRequest) (codex.ConsumeResetCreditResponse, error)
}

type runService interface {
	RunTurn(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error)
	RunTurnStreaming(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error)
	StopSessionRun(sessionID, runID, reason string) error
	ExecuteToolForSessionScope(ctx context.Context, workspacePath string, call tool.Call) (string, error)
	ListAgentToolDefinitions() []tool.Definition
	ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition
	ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error)
	ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error)
}

type swarmService interface {
	EnsureLocalState(input swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error)
	RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error)
	ListGroupsForSwarm(swarmID string, limit int) ([]swarmruntime.GroupState, string, error)
	UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error)
	DeleteGroup(groupID string) error
	SetCurrentGroup(groupID string, localSwarmID string) (swarmruntime.GroupState, error)
	UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error)
	RemoveGroupMember(input swarmruntime.RemoveGroupMemberInput) error
}

type permissionService interface {
	ListPermissions(sessionID string, limit int) ([]pebblestore.PermissionRecord, error)
	ListPending(sessionID string, limit int) ([]pebblestore.PermissionRecord, error)
	ListPendingSummaries(accountScopeID, principalID string, limit int) ([]pebblestore.PermissionSummary, error)
	PendingCount(sessionID string) (int, error)
	CreatePending(input permission.CreateInput) (pebblestore.PermissionRecord, error)
	Resolve(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, error)
	ResolveWithArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, error)
	ResolveAll(sessionID, action, reason string, limit int) ([]pebblestore.PermissionRecord, error)
	WaitForResolution(ctx context.Context, sessionID, permissionID string) (pebblestore.PermissionRecord, error)
	CancelRunPending(sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error)
	CurrentPolicy() (permission.Policy, error)
	CurrentPolicyForAccount(accountScopeID string) (permission.Policy, error)
	UpdateCapabilityPoliciesForAccount(accountScopeID string, sessionDeploy permission.SessionDeployPolicy, planAcceptance permission.PlanAcceptancePolicy) (permission.Policy, error)
	UpdateBashApprovalProfileForAccount(accountScopeID string, profile permission.BashApprovalProfile) (permission.Policy, error)
	UpsertRule(rule permission.PolicyRule) (permission.PolicyRule, error)
	UpsertRuleForAccount(accountScopeID string, rule permission.PolicyRule) (permission.PolicyRule, error)
	RemoveRule(ruleID string) (bool, error)
	RemoveRuleForAccount(accountScopeID, ruleID string) (bool, error)
	ResetPolicy() (permission.Policy, error)
	ResetPolicyForAccount(accountScopeID string) (permission.Policy, error)
	ExplainTool(mode, toolName, toolArguments string, overlay *permission.Policy) (permission.PolicyExplain, error)
	ExplainToolForAccount(accountScopeID, mode, toolName, toolArguments string, overlay *permission.Policy) (permission.PolicyExplain, error)
	ResolveWithPolicy(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, *permission.PolicyRule, error)
	ResolveWithPolicyAndArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, *permission.PolicyRule, error)
	MarkToolStarted(sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error)
	MarkToolCompleted(sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error)
	SetBypassPermissions(enabled bool)
	BypassPermissions() bool
	CurrentPermissionStateForAccount(accountScopeID string) (permission.PermissionState, error)
}

type notificationService interface {
	LocalSwarmID() string
	ListNotifications(swarmID string, limit int) ([]pebblestore.NotificationRecord, error)
	Summary(swarmID string) (pebblestore.NotificationSummary, error)
	ClearNotifications(swarmID string) (notification.ClearResult, error)
	UpdateNotification(input notification.UpdateInput) (pebblestore.NotificationRecord, bool, error)
	UpsertSystemNotification(record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error)
}

type worktreeService interface {
	GetConfig(workspacePath string) (worktreeruntime.Config, error)
	GetConfigForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.Config, error)
	SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error)
	SetConfigForPrincipal(principal identity.Principal, workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error)
	AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error)
	AllocateDetachedWorkspaceForPrincipal(principal identity.Principal, workspacePath, nameSeed string) (worktreeruntime.Allocation, error)
	AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error)
	AllocateDetachedWorkspaceRequestedForPrincipal(principal identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error)
	RollbackAllocation(allocation worktreeruntime.Allocation) error
	AttachBranch(workspacePath, sessionID, title string) (string, error)
	ListManaged(workspacePath string) ([]worktreeruntime.ManagedWorktree, error)
	ListManagedForPrincipal(principal identity.Principal, workspacePath string) ([]worktreeruntime.ManagedWorktree, error)
	PruneManaged(workspacePath string) (worktreeruntime.PruneResult, error)
	PruneManagedForPrincipal(principal identity.Principal, workspacePath string) (worktreeruntime.PruneResult, error)
}

type mcpService interface {
	List(limit int) ([]mcpruntime.Server, error)
	Get(id string) (mcpruntime.Server, bool, error)
	Upsert(input mcpruntime.UpsertInput) (mcpruntime.Server, *pebblestore.EventEnvelope, error)
	Delete(id string) (bool, *pebblestore.EventEnvelope, error)
	SetEnabled(id string, enabled bool) (mcpruntime.Server, *pebblestore.EventEnvelope, error)
}

func NewServer(authSvc *auth.Service, agentSvc *agentruntime.Service, modelSvc *model.Service, runSvc runService, sessionSvc *sessionruntime.Service, workspaceSvc *workspace.Service, discoverySvc *discovery.Service, securitySvc *security.Service, providers *registry.Registry, permSvc permissionService, notificationSvc notificationService, events *pebblestore.EventLog, hub *stream.Hub) *Server {
	runCtx, runCancel := context.WithCancel(context.Background())
	server := &Server{
		auth:                 authSvc,
		agents:               agentSvc,
		model:                modelSvc,
		runner:               runSvc,
		runStreams:           newRunControlAllocator(),
		v3RealtimeOutbox:     newV3RealtimeOutboxHub(),
		v3LiveHub:            newV3LiveHub(),
		v3LivePatchEnabled:   v3LivePatchDefaultEnabled,
		sessions:             sessionSvc,
		workspace:            workspaceSvc,
		discovery:            discoverySvc,
		security:             securitySvc,
		providers:            providers,
		perm:                 permSvc,
		notifications:        notificationSvc,
		hub:                  hub,
		events:               events,
		startedAt:            time.Now(),
		codexOAuthSessions:   make(map[string]*codexOAuthSession),
		desktopLocalSessions: newDesktopLocalSessionManager(),
		gitRealtime:          nil,
		reviewCommitActive:   make(map[string]string),
		runCtx:               runCtx,
		runCancel:            runCancel,
	}
	if server.desktopLocalSessions != nil {
		server.desktopLocalSessions.server = server
	}
	if permissionSvc, ok := permSvc.(*permission.Service); ok {
		permissionSvc.SetSummaryRealtimePublisher(server.publishPermissionSummaryV3Realtime)
		permissionSvc.SetPermissionRealtimePublisher(func(sessionID string, record pebblestore.PermissionRecord) error {
			_, _, err := server.publishSessionV3PermissionUpdatedFromRecord(identity.Principal{}, sessionID, record)
			return err
		})
	}
	if notificationSvc, ok := notificationSvc.(*notification.Service); ok {
		notificationSvc.SetRealtimePublisher(server.publishNotificationV3Realtime)
	}
	if authSvc != nil {
		authSvc.SetCredentialChangePublisher(server.publishAuthCredentialV3Realtime)
	}
	if sessionSvc != nil {
		server.v3SessionExecutor = newSessionV3Executor(server)
		server.planLifecycle = sessionruntime.NewPlanLifecycleService(sessionSvc)
		server.planLifecycle.SetApplySessionMutation(server.applySessionV3PrimaryMutation)
	}
	server.gitRealtime = newGitRealtimeManager(server)
	return server
}

func (s *Server) SetLongSessionDiagnostics(recorder *longsessiondiag.Recorder) {
	if s != nil {
		s.longSessionDiagnostics = recorder
	}
}

// SetMediaStagingService configures bounded, account-scoped pre-session media
// staging. The service deliberately has no session or model authority.
func (s *Server) SetMediaStagingService(service *mediastaging.Service) {
	if s != nil {
		s.mediaStaging = service
	}
}

func (s *Server) LongSessionSnapshot() map[string]any {
	if s == nil || s.longSessionDiagnostics == nil {
		return nil
	}
	legacy := map[string]any(nil)
	if s.hub != nil {
		stats := s.hub.Stats()
		legacy = map[string]any{"clients": stats.ConnectedClients, "subscriptions": stats.Subscriptions, "pending_messages": stats.PendingMessages}
	}
	executor := map[string]any(nil)
	if s.v3SessionExecutor != nil {
		executor = s.v3SessionExecutor.diagnosticsSnapshot()
	}
	return map[string]any{"active_api_runs": s.activeRuns.Load(), "run_stream": s.runStreams.diagnosticsSnapshot(), "v3_executor": executor, "realtime_outbox": s.v3RealtimeOutbox.diagnosticsSnapshot(), "live_patch": s.v3LiveHub.diagnosticsSnapshot(), "legacy_hub": legacy}
}

func (s *Server) SetCodexAccountClient(client codexAccountClient) {
	if s == nil {
		return
	}
	s.codexAccount = client
}

func (s *Server) SetModelProfileService(service *modelprofile.Service) {
	if s == nil {
		return
	}
	s.modelProfiles = service
}

// SetAgentModelSettingsService injects the canonical authenticated service.
// Startup also supplies its store for the one complete-record onboarding write.
func (s *Server) SetAgentModelSettingsService(service *agentmodelsettings.Service, stores ...*pebblestore.AgentModelSettingsStore) {
	if s == nil {
		return
	}
	s.agentModelSettings = service
	if len(stores) != 0 {
		s.agentModelSettingsStore = stores[0]
	}
}

func (s *Server) SetWebPushService(service *webpush.Service) {
	if s == nil {
		return
	}
	s.webPush = service
}

func (s *Server) SetBypassPermissions(enabled bool) {
	if s == nil {
		return
	}
	s.bypassPermissions = enabled
	if s.perm != nil {
		s.perm.SetBypassPermissions(enabled)
	}
}

func (s *Server) SetStartupConfigPath(path string) {
	if s == nil {
		return
	}
	s.startupConfigPath = strings.TrimSpace(path)
}

func (s *Server) SetDataDir(path string) {
	if s == nil {
		return
	}
	s.dataDir = strings.TrimSpace(path)
}

func (s *Server) SetIdentityService(identitySvc *identity.Service) {
	if s == nil {
		return
	}
	s.identityService = identitySvc
}

func (s *Server) SetIdentitySessionService(sessionSvc *identity.SessionService) {
	if s == nil {
		return
	}
	s.identitySessions = sessionSvc
}

func (s *Server) BypassPermissions() bool {
	if s == nil {
		return false
	}
	if s.perm != nil {
		return s.perm.BypassPermissions()
	}
	return s.bypassPermissions
}

func (s *Server) permissionBypassForAccount(accountScopeID string) bool {
	if s == nil {
		return false
	}
	if s.perm != nil {
		state, err := s.perm.CurrentPermissionStateForAccount(accountScopeID)
		if err == nil {
			return state.BypassPermissions
		}
	}
	return s.bypassPermissions
}

func (s *Server) SetWorktreeService(worktreeSvc worktreeService) {
	if s == nil {
		return
	}
	s.worktrees = worktreeSvc
}

func (s *Server) SetMCPService(mcpSvc mcpService) {
	if s == nil {
		return
	}
	s.mcp = mcpSvc
}

func (s *Server) SetVoiceService(voiceSvc *voice.Service) {
	if s == nil {
		return
	}
	s.voice = voiceSvc
}

func (s *Server) SetPlanLifecycleService(planLifecycle *sessionruntime.PlanLifecycleService) {
	if s == nil {
		return
	}
	s.planLifecycle = planLifecycle
}

func (s *Server) SetUISettingsService(uiSettingsSvc *uisettings.Service) {
	if s == nil {
		return
	}
	s.uiSettings = uiSettingsSvc
	if uiSettingsSvc != nil && s.sessions != nil && s.runCtx != nil {
		s.reviewAutoArchiveOnce.Do(func() {
			go s.runSessionsV3ReviewAutoArchive(s.runCtx)
		})
	}
}

func (s *Server) SetActionService(actionSvc *actionruntime.Service) {
	if s == nil {
		return
	}
	s.actions = actionSvc
	s.actionRuns = actionruntime.NewRunner(s.runCtx, actionSvc)
}

func (s *Server) SetTodoService(todoSvc *todo.Service) {
	if s == nil {
		return
	}
	s.todos = todoSvc
	if todoSvc != nil {
		todoSvc.SetAITaskLifecyclePublisher(s.publishAITaskLifecycle)
	}
}

func (s *Server) SetAITaskEnqueuer(enqueuer aiTaskEnqueuer) {
	if s == nil {
		return
	}
	s.aiTasks = enqueuer
}

func (s *Server) SetIntegrationService(integrationSvc *integrationruntime.Service) {
	if s == nil {
		return
	}
	s.integrations = integrationSvc
}

func (s *Server) SetImageThreadStore(store *pebblestore.ImageThreadStore) {
	if s == nil {
		return
	}
	s.imageThreads = store
	if s.imageGen != nil {
		s.imageGen.SetImageThreadStore(store)
	}
}

func (s *Server) SetSwarmService(swarmSvc swarmService) {
	if s == nil {
		return
	}
	s.swarm = swarmSvc
}

func (s *Server) SetSwarmStore(store *pebblestore.SwarmStore) {
	if s == nil {
		return
	}
	s.swarmStore = store
}

func (s *Server) swarmLocalNode() (pebblestore.SwarmLocalNodeRecord, bool, error) {
	if s == nil || s.swarmStore == nil {
		return pebblestore.SwarmLocalNodeRecord{}, false, nil
	}
	return s.swarmStore.GetLocalNode()
}

func (s *Server) SetSwarmDesktopTargetSelectionStore(store *pebblestore.SwarmDesktopTargetSelectionStore) {
	if s == nil {
		return
	}
	s.swarmDesktopTargetSelection = store
}

func (s *Server) SetShutdownHandler(handler func(reason string)) {
	if s == nil {
		return
	}
	s.requestStop = handler
}

func (s *Server) BeginShutdown() {
	if s == nil {
		return
	}
	s.activeRunMu.Lock()
	s.shuttingDown.Store(true)
	s.activeRunMu.Unlock()
	if s.actionRuns != nil {
		s.actionRuns.CancelAll()
	}
}

func (s *Server) CancelInFlightRuns() {
	if s == nil {
		return
	}
	s.shuttingDown.Store(true)
	if s.runCancel != nil {
		s.runCancel()
	}
	if s.actionRuns != nil {
		s.actionRuns.CancelAll()
	}
	if s.gitRealtime != nil {
		s.gitRealtime.stopAll()
	}
}

func (s *Server) WaitForInFlightRuns(timeout time.Duration) bool {
	if s == nil {
		return true
	}
	s.activeRunMu.Lock()
	s.shuttingDown.Store(true)
	s.activeRunMu.Unlock()
	if timeout <= 0 {
		s.runWG.Wait()
		if s.actionRuns != nil {
			s.actionRuns.Wait(0)
		}
		return true
	}
	deadline := time.Now().Add(timeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runWG.Wait()
	}()
	select {
	case <-done:
		if s.actionRuns == nil {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		return s.actionRuns.Wait(remaining)
	case <-time.After(time.Until(deadline)):
		return false
	}
}

func (s *Server) ActiveRunCount() int {
	if s == nil {
		return 0
	}
	count := s.activeRuns.Load()
	if count < 0 {
		return 0
	}
	return int(count)
}

func (s *Server) beginActiveRun() bool {
	if s == nil {
		return false
	}
	s.activeRunMu.Lock()
	defer s.activeRunMu.Unlock()
	if s.shuttingDown.Load() {
		return false
	}
	s.runWG.Add(1)
	s.activeRuns.Add(1)
	return true
}

func (s *Server) endActiveRun() {
	if s == nil {
		return
	}
	s.activeRuns.Add(-1)
	s.runWG.Done()
}

func (s *Server) isShuttingDown() bool {
	return s != nil && s.shuttingDown.Load()
}

func (s *Server) apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	s.registerCoreRoutes(mux)
	s.registerAuthVaultRoutes(mux)
	s.registerOnboardingRoutes(mux)
	s.registerSwarmRoutes(mux)
	s.registerAgentRoutes(mux)
	s.registerProviderRoutes(mux)
	s.registerWorkspaceRoutes(mux)
	s.registerRuntimeRoutes(mux)
	return mux
}

func (s *Server) Handler() http.Handler {
	return s.withAuth(s.withVaultGate(s.withJSON(s.apiMux())))
}

func (s *Server) localTransportMux() http.Handler {
	return s.apiMux()
}

func (s *Server) LocalTransportHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = markLocalTransportRequest(r)
		s.withAuth(s.withVaultGate(s.withJSON(s.localTransportMux()))).ServeHTTP(w, r)
	})
}

func (s *Server) DesktopHandler() http.Handler {
	apiHandler := s.withDesktopLocalSession(s.withAuth(s.withVaultGate(s.withJSON(s.apiMux()))))
	desktopSurface := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldServeDesktopAsset(r) {
			s.withDesktopLocalSession(s.withDesktopAssets(http.NotFoundHandler())).ServeHTTP(w, r)
			return
		}
		apiHandler.ServeHTTP(w, r)
	})
	return s.withDesktopBoundary(desktopSurface)
}

func (s *Server) handleDesktopStream(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusInternalServerError, errors.New("stream hub not configured"))
		return
	}
	s.hub.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	status := map[string]any{
		"ok":                 true,
		"bypass_permissions": s.BypassPermissions(),
		"uptime_ms":          time.Since(s.startedAt).Milliseconds(),
		"global_sequence":    s.events.CurrentSequence(),
		"clients":            s.hub.Stats().ConnectedClients,
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.isShuttingDown() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":     false,
			"reason": "shutting_down",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSystemShutdown(w http.ResponseWriter, r *http.Request) {
	if !isLocalAdministrativeRequest(r) {
		writeError(w, http.StatusForbidden, errors.New("host shutdown requires the local administrative transport"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		if err := decodeJSON(r, &req); err != nil {
			if !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "api"
	}
	s.BeginShutdown()
	if s.requestStop != nil {
		s.requestStop(reason)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":            true,
		"shutting_down": true,
		"reason":        reason,
	})
}

func (s *Server) handleWorktrees(w http.ResponseWriter, r *http.Request) {
	if s.worktrees == nil {
		writeError(w, http.StatusInternalServerError, errors.New("worktree service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	workspacePath, err := s.resolveWorktreeConfigPath(r, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		config, err := s.worktrees.GetConfigForPrincipal(principal, workspacePath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		managed, err := s.worktrees.ListManagedForPrincipal(principal, workspacePath)
		warning := ""
		if err != nil {
			warning = worktreeruntime.DetachedWorkspaceFallbackWarning(err)
			if warning == "" {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			// A workspace does not need to be a Git repository to host a session.
			// Report worktrees as effectively disabled so ordinary TUI session
			// creation stays in the selected directory without requesting an
			// allocation that cannot exist.
			config.Enabled = false
			managed = []worktreeruntime.ManagedWorktree{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"worktrees": config,
			"managed":   managed,
			"warning":   warning,
		})
	case http.MethodPost:
		var req struct {
			WorkspacePath    string  `json:"workspace_path"`
			Enabled          *bool   `json:"enabled"`
			UseCurrentBranch *bool   `json:"use_current_branch"`
			BaseBranch       string  `json:"base_branch"`
			BranchName       *string `json:"branch_name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.WorkspacePath) != "" {
			workspacePath = strings.TrimSpace(req.WorkspacePath)
			workspacePath, err = s.resolveWorktreeConfigPathForValue(workspacePath, principal)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		current, err := s.worktrees.GetConfigForPrincipal(principal, workspacePath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		enabled := current.Enabled
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		baseBranch := strings.TrimSpace(req.BaseBranch)
		useCurrentBranch := current.UseCurrentBranch
		if req.UseCurrentBranch != nil {
			useCurrentBranch = *req.UseCurrentBranch
		}
		if useCurrentBranch {
			baseBranch = ""
		} else if baseBranch == "" {
			baseBranch = strings.TrimSpace(current.BaseBranch)
		}
		branchName := strings.TrimSpace(current.BranchName)
		if req.BranchName != nil {
			branchName = strings.TrimSpace(*req.BranchName)
		}
		config, event, err := s.worktrees.SetConfigForPrincipal(principal, workspacePath, enabled, useCurrentBranch, baseBranch, branchName)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"worktrees": config,
		})
	case http.MethodDelete:
		result, err := s.worktrees.PruneManagedForPrincipal(principal, workspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"result": result,
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleManageWorktree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("run service"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	workspacePath, err := s.resolveWorktreeConfigPath(r, principal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	query := map[string]any{"action": "inspect"}
	if strings.TrimSpace(workspacePath) != "" {
		query["workspace_path"] = workspacePath
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			query["limit"] = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			query["cursor"] = parsed
		}
	}
	resolvedWorkspacePath := workspacePath
	callArgs, err := json.Marshal(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	result, execErr := s.runner.ExecuteToolForSessionScope(ctx, resolvedWorkspacePath, tool.Call{Name: "manage-worktree", Arguments: string(callArgs)})
	if execErr != nil {
		writeError(w, http.StatusBadRequest, execErr)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) resolveWorktreeConfigPath(r *http.Request, principal identity.Principal) (string, error) {
	if r == nil {
		return "", errors.New("request is required")
	}
	if value := strings.TrimSpace(r.URL.Query().Get("workspace_path")); value != "" {
		return s.resolveWorktreeConfigPathForValue(value, principal)
	}
	if s.workspace == nil {
		return "", errors.New("workspace service not configured")
	}
	current, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(current.ResolvedPath) == "" {
		return "", errors.New("workspace path is required")
	}
	return s.resolveWorktreeConfigPathForValue(current.ResolvedPath, principal)
}

func (s *Server) resolveWorktreeConfigPathForValue(path string, principal identity.Principal) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace path is required")
	}
	if s.workspace == nil {
		return "", errors.New("workspace service not configured")
	}
	scope, err := s.workspace.ScopeForPathForPrincipal(principal, path)
	if err != nil {
		return "", err
	}
	if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
		return strings.TrimSpace(scope.WorkspacePath), nil
	}
	return "", errors.New("account-owned workspace path is required")
}

func (s *Server) handleCodexAuth(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	switch r.Method {
	case http.MethodGet:
		status, err := s.auth.CodexStatusForAccount(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPost:
		var req struct {
			Type         string `json:"type"`
			APIKey       string `json:"api_key"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresAt    int64  `json:"expires_at"`
			AccountID    string `json:"account_id"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		authType := strings.ToLower(strings.TrimSpace(req.Type))
		if authType == "" {
			if strings.TrimSpace(req.APIKey) != "" {
				authType = "api"
			} else {
				authType = "oauth"
			}
		}

		var (
			status auth.CodexStatus
			event  *pebblestore.EventEnvelope
			err    error
		)
		switch authType {
		case "api":
			writeError(w, http.StatusBadRequest, errors.New("codex api-key auth moved to the openai provider; configure provider=openai through /v1/auth/credentials"))
			return
		case "oauth":
			status, event, err = s.auth.SetCodexOAuthForAccount(accountScopeID, req.AccessToken, req.RefreshToken, req.ExpiresAt, req.AccountID)
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported codex auth type %q", authType))
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		autoDefaults, defaultsErr := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(accountScopeID, principal.UserID, "codex")
		if defaultsErr != nil {
			status.AutoDefaults = &auth.AutoDefaultsStatus{Error: defaultsErr.Error()}
		} else if autoDefaults != nil {
			status.AutoDefaults = autoDefaults
		}
		writeJSON(w, http.StatusOK, status)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAuthCredentials(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	switch r.Method {
	case http.MethodGet:
		provider := strings.TrimSpace(r.URL.Query().Get("provider"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		limit := 200
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = n
		}
		list, err := s.auth.ListCredentialsForAccount(accountScopeID, provider, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var req struct {
			ID           string   `json:"id"`
			Provider     string   `json:"provider"`
			Type         string   `json:"type"`
			Label        string   `json:"label"`
			Tags         []string `json:"tags"`
			APIKey       string   `json:"api_key"`
			AccessToken  string   `json:"access_token"`
			RefreshToken string   `json:"refresh_token"`
			ExpiresAt    int64    `json:"expires_at"`
			AccountID    string   `json:"account_id"`
			Active       bool     `json:"active"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		provider := strings.ToLower(strings.TrimSpace(req.Provider))
		if provider == "" {
			writeError(w, http.StatusBadRequest, errors.New("provider is required"))
			return
		}
		if req.Active {
			if firstRun, _, _, firstRunErr := s.firstOnboardingProviderHydrationState(accountScopeID); firstRunErr != nil {
				writeError(w, http.StatusBadRequest, firstRunErr)
				return
			} else if firstRun {
				writeError(w, http.StatusBadRequest, errors.New("first onboarding provider credential must use /v1/onboarding/provider/credential"))
				return
			}
		}
		wantsActive := req.Active
		input := auth.CredentialUpsertInput{
			ID:             req.ID,
			Provider:       provider,
			AccountScopeID: accountScopeID,
			Type:           req.Type,
			Label:          req.Label,
			Tags:           req.Tags,
			APIKey:         req.APIKey,
			AccessToken:    req.AccessToken,
			RefreshToken:   req.RefreshToken,
			ExpiresAt:      req.ExpiresAt,
			AccountID:      req.AccountID,
			Active:         false,
		}
		connection, verifyErr := s.verifyCredentialMaterialForAccount(r.Context(), accountScopeID, provideriface.AuthCredential{
			ID:           strings.ToLower(strings.TrimSpace(input.ID)),
			Provider:     provider,
			Type:         input.Type,
			Label:        input.Label,
			Tags:         append([]string(nil), input.Tags...),
			APIKey:       input.APIKey,
			AccessToken:  input.AccessToken,
			RefreshToken: input.RefreshToken,
			ExpiresAt:    input.ExpiresAt,
			AccountID:    input.AccountID,
		})
		if verifyErr != nil {
			writeError(w, http.StatusInternalServerError, verifyErr)
			return
		}
		if !authCredentialVerificationAccepted(connection) {
			writeError(w, http.StatusBadRequest, authCredentialVerificationError(connection))
			return
		}
		status, event, err := s.auth.UpsertCredential(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		if connection != nil {
			updated, updateEvent, updateErr := s.auth.UpdateCredentialConnectionForAccount(accountScopeID, provider, status.ID, connection)
			if updateErr != nil {
				writeError(w, http.StatusInternalServerError, updateErr)
				return
			}
			status = updated
			if updateEvent != nil {
				s.hub.Publish(*updateEvent)
			}
		}
		if wantsActive {
			status, event, err = s.auth.SetActiveCredentialForAccount(accountScopeID, provider, status.ID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
		}
		status.Connection = connection
		if wantsActive {
			autoDefaults, defaultsErr := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(accountScopeID, principal.UserID, provider)
			if defaultsErr != nil {
				status.AutoDefaults = &auth.AutoDefaultsStatus{Error: defaultsErr.Error()}
			} else if autoDefaults != nil {
				status.AutoDefaults = autoDefaults
			}
		}
		writeJSON(w, http.StatusOK, status)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAuthCredentialVerify(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	credentialID := strings.ToLower(strings.TrimSpace(req.ID))
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	if credentialID == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	connection, verifyErr := s.verifyAuthCredentialConnectionForAccount(r.Context(), accountScopeID, provider, credentialID)
	if verifyErr != nil {
		writeError(w, http.StatusInternalServerError, verifyErr)
		return
	}
	if connection == nil {
		connection = &auth.ConnectionStatus{
			Connected: false,
			Method:    "unavailable",
			Message:   "provider does not expose credential verification",
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":   provider,
		"id":         credentialID,
		"connection": connection,
	})
}

func (s *Server) handleAuthCredentialActive(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	credentialID := strings.ToLower(strings.TrimSpace(req.ID))
	if credentialID == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	connection, verifyErr := s.verifyAuthCredentialConnectionForAccount(r.Context(), accountScopeID, provider, credentialID)
	if verifyErr != nil {
		writeError(w, http.StatusInternalServerError, verifyErr)
		return
	}
	if !authCredentialVerificationAccepted(connection) {
		writeError(w, http.StatusBadRequest, authCredentialVerificationError(connection))
		return
	}
	status, event, err := s.auth.SetActiveCredentialForAccount(accountScopeID, provider, credentialID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	status.Connection = connection
	autoDefaults, defaultsErr := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(accountScopeID, principal.UserID, provider)
	if defaultsErr != nil {
		status.AutoDefaults = &auth.AutoDefaultsStatus{Error: defaultsErr.Error()}
	} else if autoDefaults != nil {
		status.AutoDefaults = autoDefaults
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAuthCredentialDelete(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	deleted, event, err := s.auth.DeleteCredentialForAccount(accountScopeID, provider, req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, errors.New("credential not found"))
		return
	}
	cleanup, err := s.cleanupProviderAfterCredentialDeletionForAccount(r.Context(), accountScopeID, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"deleted":  true,
		"provider": provider,
		"id":       strings.ToLower(strings.TrimSpace(req.ID)),
		"cleanup":  cleanup,
	})
}

func (s *Server) handleAttachRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.security == nil {
		writeError(w, http.StatusInternalServerError, errors.New("security service not configured"))
		return
	}
	status, event, err := s.security.RotateAttachToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleModelPreference(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pref, err := s.model.GetResolvedPreferenceForAccount(principal.AccountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, pref)
	case http.MethodPost:
		var req struct {
			Provider    string `json:"provider"`
			Model       string `json:"model"`
			Thinking    string `json:"thinking"`
			ServiceTier string `json:"service_tier"`
			ContextMode string `json:"context_mode"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		pref, event, err := s.model.SetPreferenceForAccount(principal.AccountScopeID, principal.UserID, req.Provider, req.Model, req.Thinking, req.ServiceTier, req.ContextMode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, pref)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleModelCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method == http.MethodPost {
		refresh, err := s.model.RefreshCatalogManual(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"ok":      false,
				"error":   err.Error(),
				"refresh": refresh,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"refresh": refresh,
		})
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model"))

	meta, metaOK, err := s.model.CatalogMeta()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if modelID != "" {
		lookup, err := s.model.GetCatalog(provider, modelID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !lookup.Found {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"ok":       false,
				"provider": provider,
				"model":    modelID,
				"error":    "model catalog record not found",
			})
			return
		}
		body := map[string]any{
			"ok":       true,
			"provider": provider,
			"model":    modelID,
			"lookup":   lookup,
		}
		if metaOK {
			body["meta"] = meta
		}
		body["catalog_status"] = catalogStatusPayload(meta, metaOK)
		writeJSON(w, http.StatusOK, body)
		return
	}

	limit := 500
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = parsed
	}

	records, err := s.model.ListCatalog(provider, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	body := map[string]any{
		"ok":       true,
		"provider": provider,
		"count":    len(records),
		"records":  records,
	}
	if metaOK {
		body["meta"] = meta
	}
	body["catalog_status"] = catalogStatusPayload(meta, metaOK)
	writeJSON(w, http.StatusOK, body)
}

func catalogStatusPayload(meta pebblestore.ModelCatalogMeta, ok bool) map[string]any {
	status := map[string]any{
		"configured": ok,
	}
	if !ok {
		return status
	}
	status["source"] = meta.Source
	status["source_url"] = meta.SourceURL
	status["version_url"] = meta.VersionURL
	status["snapshot_id"] = meta.SnapshotID
	status["snapshot_version"] = meta.SnapshotVersion
	status["generated_at"] = meta.GeneratedAt
	status["fetched_at"] = meta.FetchedAt
	status["last_checked_at"] = meta.LastCheckedAt
	status["expires_at"] = meta.ExpiresAt
	status["record_count"] = meta.RecordCount
	status["model_count"] = meta.ModelCount
	status["pinned_snapshot_id"] = meta.PinnedSnapshotID
	status["pinned_snapshot_version"] = meta.PinnedSnapshotVersion
	status["live_snapshot_id"] = meta.LiveSnapshotID
	status["live_snapshot_version"] = meta.LiveSnapshotVersion
	status["live_checked_at"] = meta.LiveCheckedAt
	status["using_cache_fallback"] = meta.UsingCacheFallback
	status["last_error"] = meta.LastError
	status["last_error_at"] = meta.LastErrorAt
	status["last_refresh_reason"] = meta.LastRefreshReason
	if meta.PinnedSnapshotVersion != "" && meta.SnapshotVersion != "" {
		status["matches_pinned_snapshot"] = meta.PinnedSnapshotID == meta.SnapshotID && meta.PinnedSnapshotVersion == meta.SnapshotVersion
	}
	if meta.LiveSnapshotVersion != "" && meta.SnapshotVersion != "" {
		status["matches_live_snapshot"] = meta.LiveSnapshotID == meta.SnapshotID && meta.LiveSnapshotVersion == meta.SnapshotVersion
	}
	return status
}

func (s *Server) handleModelFavorites(w http.ResponseWriter, r *http.Request) {
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	switch r.Method {
	case http.MethodGet:
		provider := strings.TrimSpace(r.URL.Query().Get("provider"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		limit := 500
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		records, err := s.model.ListFavoritesForAccount(principal.AccountScopeID, provider, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"provider": strings.ToLower(strings.TrimSpace(provider)),
			"query":    query,
			"count":    len(records),
			"records":  records,
		})
	case http.MethodPost:
		var req struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Label    string `json:"label"`
			Thinking string `json:"thinking"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, event, err := s.model.UpsertFavoriteForAccount(principal.AccountScopeID, principal.UserID, req.Provider, req.Model, req.Label, req.Thinking)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"favorite": record,
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleModelFavoriteDelete(w http.ResponseWriter, r *http.Request) {
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deleted, event, err := s.model.DeleteFavoriteForAccount(principal.AccountScopeID, req.Provider, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, errors.New("favorite not found"))
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"deleted":  true,
		"provider": strings.ToLower(strings.TrimSpace(req.Provider)),
		"model":    strings.TrimSpace(req.Model),
	})
}

func (s *Server) handleWorkspaceResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	resolution, err := s.workspace.ResolveForPrincipal(principal, cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resolution)
}

func (s *Server) handleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.SelectForPrincipal(principal, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	resolution, ok, err := s.workspace.CurrentBindingForPrincipal(principal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace binding not found"})
		return
	}
	writeJSON(w, http.StatusOK, resolution)
}

func (s *Server) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entries, err = s.applyWorkspaceWorktreeStatus(principal, entries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"workspaces": entries,
	})
}

func (s *Server) handleWorkspaceDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	var roots []string
	if raw := strings.TrimSpace(r.URL.Query().Get("roots")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				roots = append(roots, part)
			}
		}
	}
	entries, err := s.workspace.Discover(roots, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"directories": entries,
	})
}

func (s *Server) handleWorkspaceBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	browser, err := s.workspace.BrowseForPrincipal(principal, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"browser": browser,
	})
}

func (s *Server) handleWorkspaceFolderCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		ParentPath string `json:"parent_path"`
		Name       string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	folder, err := s.workspace.CreateFolderForPrincipal(principal, req.ParentPath, req.Name)
	if err != nil {
		status := http.StatusBadRequest
		if folder.RequiresSudo {
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]any{
			"ok":     false,
			"error":  err.Error(),
			"folder": folder,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"folder": folder,
	})
}

func (s *Server) handleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path        string `json:"path"`
		Name        string `json:"name"`
		ThemeID     string `json:"theme_id"`
		MakeCurrent *bool  `json:"make_current"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	makeCurrent := true
	if req.MakeCurrent != nil {
		makeCurrent = *req.MakeCurrent
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	if _, err := s.topology.EnsureLocalSelfPlacementForPrincipal(principal.AccountScopeID, principal.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resolution, entry, created, err := s.workspace.AddForPrincipalWithEntryWithoutSelection(principal, req.Path, req.Name, req.ThemeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	binding, err := s.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(principal.AccountScopeID, principal.UserID, entry)
	if err != nil {
		if created {
			if rollbackErr := s.workspace.RollbackCreatedWorkspaceForPrincipal(principal, entry); rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("create workspace self binding: %w; rollback failed: %w", err, rollbackErr))
				return
			}
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if makeCurrent {
		if err := s.workspace.SelectEntryForPrincipal(principal, entry); err != nil {
			if created {
				if rollbackErr := s.workspace.RollbackCreatedWorkspaceForPrincipal(principal, entry); rollbackErr != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("select workspace after self binding: %w; rollback failed: %w", err, rollbackErr))
					return
				}
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	resolution.LocalWorkspaceBindingID = binding.BindingID
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                         true,
		"workspace":                  resolution,
		"workspace_id":               resolution.WorkspaceID,
		"local_workspace_binding_id": binding.BindingID,
	})
}

func (s *Server) handleWorkspaceDirectoryAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		DirectoryPath string `json:"directory_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.AddDirectoryForPrincipal(principal, req.WorkspacePath, req.DirectoryPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceDirectoryRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		DirectoryPath string `json:"directory_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.RemoveDirectoryForPrincipal(principal, req.WorkspacePath, req.DirectoryPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path  string `json:"path"`
		Delta int    `json:"delta"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.MoveForPrincipal(principal, req.Path, req.Delta)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.RenameForPrincipal(principal, req.Path, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceTheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path    string `json:"path"`
		ThemeID string `json:"theme_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.SetThemeIDForPrincipal(principal, req.Path, req.ThemeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path           string `json:"path"`
		IconPNGDataURL string `json:"icon_png_data_url"`
	}
	if err := decodeJSONLimited(w, r, &req, 1_500_000); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.SetIconPNGDataURLForPrincipal(principal, req.Path, req.IconPNGDataURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.DeleteForPrincipal(principal, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleContextSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.discovery == nil {
		writeError(w, http.StatusInternalServerError, errors.New("discovery service not configured"))
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		current, currentOK, err := s.workspace.CurrentBindingForPrincipal(principal)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if currentOK {
			cwd = current.ResolvedPath
		}
	}
	if cwd == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace path is required"))
		return
	}

	scope, err := s.workspace.ScopeForPathForPrincipal(principal, cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !scope.Matched || strings.TrimSpace(scope.WorkspacePath) == "" {
		writeError(w, http.StatusBadRequest, errAccountOwnedWorkspacePathRequired)
		return
	}
	report, err := s.discovery.ScanScope(scope.WorkspacePath, scope.Directories)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"report": report,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
		exactPath := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("exact_path")), "true")
		var (
			sessions []pebblestore.SessionSnapshot
			listErr  error
		)
		if cwd == "" {
			sessions, listErr = s.sessions.ListSessionsForAccount(principal.AccountScopeID, limit)
		} else {
			sessions, listErr = listSessionsForCWDWithTopology(s.sessions, s.workspace, s.topology, principal, cwd, limit, exactPath)
		}
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		visibleSessions := sessions[:0]
		for _, session := range sessions {
			if !sessionsV3SystemSidechat(session) {
				visibleSessions = append(visibleSessions, session)
			}
		}
		responseSessions, enrichErr := s.enrichSessionSummariesForList(visibleSessions)
		if enrichErr != nil {
			writeError(w, http.StatusInternalServerError, enrichErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"sessions": responseSessions,
		})
	case http.MethodPost:
		req, principal, principalOK, err := s.decodeSessionCreateRequest(r)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, identity.ErrPrincipalRequired) {
				status = http.StatusUnauthorized
			}
			writeError(w, status, err)
			return
		}
		session, event, warning, modeWarning, err := s.createSessionFromRequest(req, principal, principalOK, nil, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"session": session,
			"warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning}, " ")),
		})
	default:
		methodNotAllowed(w)
	}
}

type sessionSummaryResponse struct {
	pebblestore.SessionSnapshot
	gitStatusResponseFields
	GitCommitDetected      bool `json:"git_commit_detected,omitempty"`
	GitCommitCount         int  `json:"git_commit_count,omitempty"`
	PendingPermissionCount int  `json:"pending_permission_count"`
}

const sessionListPermissionParallelism = 16

func (s *Server) enrichSessionSummariesForList(sessions []pebblestore.SessionSnapshot) ([]sessionSummaryResponse, error) {
	responseSessions := make([]sessionSummaryResponse, len(sessions))
	if len(sessions) == 0 {
		return responseSessions, nil
	}

	workerCount := sessionListPermissionParallelism
	if workerCount > len(sessions) {
		workerCount = len(sessions)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	type jobResult struct {
		index int
		item  sessionSummaryResponse
		err   error
	}
	jobs := make(chan int, len(sessions))
	results := make(chan jobResult, len(sessions))

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				session := sessions[index]
				pendingPermissionCount := 0
				if s.perm != nil {
					count, err := s.perm.PendingCount(session.ID)
					if err != nil {
						results <- jobResult{err: err}
						continue
					}
					pendingPermissionCount = count
				}
				fields := gitStatusResponseForSession(session)
				results <- jobResult{
					index: index,
					item: sessionSummaryResponse{
						SessionSnapshot:         session,
						gitStatusResponseFields: fields,
						GitCommitDetected:       gitCommitDetectedForSession(session, fields),
						GitCommitCount:          gitCommitCountForSession(session, fields),
						PendingPermissionCount:  pendingPermissionCount,
					},
				}
			}
		}()
	}

	for i := range sessions {
		jobs <- i
	}
	close(jobs)

	var firstErr error
	for i := 0; i < len(sessions); i++ {
		result := <-results
		if result.err != nil {
			firstErr = result.err
			continue
		}
		responseSessions[result.index] = result.item
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return responseSessions, nil
}

func (s *Server) deleteSessionAndRoutes(sessionID string) error {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	var failures []string
	if s.sessions != nil {
		if event, err := s.sessions.DeleteSessionWithEvent(sessionID); err != nil {
			failures = append(failures, "delete session: "+err.Error())
		} else if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return errors.New(strings.Join(failures, "; "))
}

func mergeSessionCreateMetadata(base, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range extra {
		merged[key] = value
	}
	for key, value := range base {
		if _, exists := merged[key]; exists && overridableSessionCreateMetadataKey(key) {
			continue
		}
		merged[key] = value
	}
	return merged
}

func overridableSessionCreateMetadataKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "title_pending":
		return true
	default:
		return false
	}
}

func (s *Server) verifySessionOwnershipForRequest(w http.ResponseWriter, r *http.Request, sessionID string) (identity.Principal, pebblestore.SessionSnapshot, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return identity.Principal{}, pebblestore.SessionSnapshot{}, false
	}
	if found {
		if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
			writeSessionNotFound(w)
			return identity.Principal{}, pebblestore.SessionSnapshot{}, false
		}
		return principal, session, true
	}
	writeSessionNotFound(w)
	return identity.Principal{}, pebblestore.SessionSnapshot{}, false
}

func writeSessionNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"ok":    false,
		"error": "session not found",
	})
}

func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}
	const prefix = "/v1/sessions/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	rest = strings.TrimSpace(rest)
	if rest == "" {
		writeError(w, http.StatusNotFound, errors.New("session path is required"))
		return
	}

	if strings.HasSuffix(rest, "/messages") {
		sessionID := strings.TrimSuffix(rest, "/messages")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		principal, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			afterSeq := uint64(0)
			if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
				parsed, err := strconv.ParseUint(raw, 10, 64)
				if err != nil {
					writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
					return
				}
				afterSeq = parsed
			}
			limit := 500
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil || parsed <= 0 {
					writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
					return
				}
				limit = parsed
			}
			messages, err := s.sessions.ListMessages(sessionID, afterSeq, limit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"messages":   messages,
			})
		case http.MethodPost:
			if err := s.enforceSessionBindingWriteAccess(principal, sessionID, "append message"); err != nil {
				writeError(w, http.StatusForbidden, err)
				return
			}
			var req struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			message, updatedSession, event, err := s.sessions.AppendMessage(sessionID, req.Role, req.Content, nil)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":      true,
				"message": message,
				"session": updatedSession,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/metadata") {
		sessionID := strings.TrimSuffix(rest, "/metadata")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, session, ok := s.verifySessionOwnershipForRequest(w, r, sessionID)
			if !ok {
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"metadata":   session.Metadata,
				"updated_at": session.UpdatedAt,
			})
		case http.MethodPost:
			var req struct {
				Metadata map[string]any `json:"metadata"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			session, event, err := s.sessions.UpdateMetadata(sessionID, req.Metadata)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":      true,
				"session": session,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/mode") {
		sessionID := strings.TrimSuffix(rest, "/mode")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		principal, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			mode, err := s.sessions.GetMode(sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"mode":       mode,
			})
		case http.MethodPost:
			var req struct {
				Mode string `json:"mode"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			profile, profileErr := s.agents.ResolvePrimaryForAccount(principal.AccountScopeID, "")
			if profileErr != nil {
				writeError(w, http.StatusBadRequest, profileErr)
				return
			}
			requestedMode := sessionruntime.NormalizeMode(req.Mode)
			modeWarning := ""
			if !pebblestore.AgentExitPlanModeEnabled(profile) {
				setting := pebblestore.AgentProfileRuntimeMode(profile)
				if setting == "" || setting == pebblestore.AgentRuntimeModePlanAuto {
					agentName := strings.TrimSpace(profile.Name)
					if agentName == "" {
						agentName = "active primary agent"
					}
					writeError(w, http.StatusBadRequest, fmt.Errorf("%s has plan mode disabled but no runtime_mode is configured", agentName))
					return
				}
				if requestedMode != setting {
					modeWarning = fmt.Sprintf("active primary agent %q has plan mode disabled; ignoring requested session mode %q and using runtime mode %q", strings.TrimSpace(profile.Name), requestedMode, setting)
				}
				req.Mode = setting
			}
			session, event, err := s.sessions.SetMode(sessionID, req.Mode)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"mode":       session.Mode,
				"updated_at": session.UpdatedAt,
				"warning":    modeWarning,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/preference") {
		sessionID := strings.TrimSuffix(rest, "/preference")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			pref, err := s.sessions.GetSessionPreference(sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			resolved, err := s.model.ResolvePreference(pref)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, resolved)
		case http.MethodPost:
			var req struct {
				Provider    *string `json:"provider,omitempty"`
				Model       *string `json:"model,omitempty"`
				Thinking    *string `json:"thinking,omitempty"`
				ServiceTier *string `json:"service_tier,omitempty"`
				ContextMode *string `json:"context_mode,omitempty"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			pref, event, err := s.sessions.SetSessionPreference(sessionID, sessionruntime.SessionPreferenceUpdate{
				Provider:    req.Provider,
				Model:       req.Model,
				Thinking:    req.Thinking,
				ServiceTier: req.ServiceTier,
				ContextMode: req.ContextMode,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			resolved, err := s.model.ResolvePreference(pref)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, resolved)
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/codex") {
		sessionID := strings.TrimSuffix(rest, "/codex")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			config, err := s.sessions.GetCodexConfig(sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
		case http.MethodPost:
			var req struct {
				ServiceTier *string `json:"service_tier,omitempty"`
				ContextMode *string `json:"context_mode,omitempty"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			config, event, err := s.sessions.SetCodexConfig(sessionID, sessionruntime.SessionCodexConfigUpdate{
				ServiceTier: req.ServiceTier,
				ContextMode: req.ContextMode,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, s.codexSessionConfigResponse(sessionID, config))
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/plans/active") {
		sessionID := strings.TrimSuffix(rest, "/plans/active")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			plan, ok, err := s.sessions.GetActivePlan(sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":          true,
					"session_id":  sessionID,
					"has_active":  false,
					"active_plan": nil,
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":          true,
				"session_id":  sessionID,
				"has_active":  true,
				"active_plan": plan,
			})
		case http.MethodPost:
			var req struct {
				PlanID string `json:"plan_id"`
				ID     string `json:"id"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			planID := strings.TrimSpace(req.PlanID)
			if planID == "" {
				planID = strings.TrimSpace(req.ID)
			}
			plan, event, err := s.sessions.SetActivePlan(sessionID, planID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":          true,
				"session_id":  sessionID,
				"active_plan": plan,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.Contains(rest, "/plans/") {
		parts := strings.Split(strings.Trim(rest, "/"), "/plans/")
		if len(parts) == 2 && strings.HasSuffix(parts[1], "/history") {
			sessionID := strings.TrimSpace(parts[0])
			planID := strings.TrimSpace(strings.TrimSuffix(parts[1], "/history"))
			if sessionID == "" {
				writeError(w, http.StatusBadRequest, errors.New("session id is required"))
				return
			}
			if planID == "" {
				writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
				return
			}
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
				return
			}
			limit := 100
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil || parsed <= 0 {
					writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
					return
				}
				limit = parsed
			}
			revisionKind := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("revision_kind"), r.URL.Query().Get("kind"))))
			if revisionKind == "" {
				revisionKind = sessionruntime.PlanRevisionKindDefinition
			}
			revisions, err := s.sessions.ListPlanRevisionsByKind(sessionID, planID, limit, revisionKind)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":            true,
				"session_id":    sessionID,
				"plan_id":       planID,
				"revision_kind": revisionKind,
				"count":         len(revisions),
				"revisions":     revisions,
			})
			return
		}
		if len(parts) == 2 && !strings.Contains(parts[1], "/") {
			sessionID := strings.TrimSpace(parts[0])
			planID := strings.TrimSpace(parts[1])
			if sessionID == "" {
				writeError(w, http.StatusBadRequest, errors.New("session id is required"))
				return
			}
			if planID == "" {
				writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
				return
			}
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
				return
			}
			plan, ok, err := s.sessions.GetPlan(sessionID, planID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"ok":    false,
					"error": "plan not found",
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"plan":       plan,
			})
			return
		}
	}

	if strings.HasSuffix(rest, "/plans") {
		sessionID := strings.TrimSuffix(rest, "/plans")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit := 100
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil || parsed <= 0 {
					writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
					return
				}
				limit = parsed
			}
			plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":             true,
				"session_id":     sessionID,
				"active_plan_id": activeID,
				"count":          len(plans),
				"plans":          plans,
			})
		case http.MethodPost:
			var req struct {
				ID            string                            `json:"id"`
				PlanID        string                            `json:"plan_id"`
				Title         string                            `json:"title"`
				Plan          string                            `json:"plan"`
				Document      *pebblestore.SessionPlanDocument  `json:"document"`
				DocumentPatch *sessionruntime.PlanDocumentPatch `json:"document_patch"`
				Status        string                            `json:"status"`
				ApprovalState string                            `json:"approval_state"`
				UpdateSummary string                            `json:"update_summary"`
				UpdateScope   string                            `json:"update_scope"`
				Scope         string                            `json:"scope"`
				UpdateKind    string                            `json:"update_kind"`
				RevisionKind  string                            `json:"revision_kind"`
				Checkpoint    bool                              `json:"checkpoint"`
				Activate      *bool                             `json:"activate"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			planID := strings.TrimSpace(req.PlanID)
			if planID == "" {
				planID = strings.TrimSpace(req.ID)
			}
			activate := true
			if req.Activate != nil {
				activate = *req.Activate
			}
			updateScope := strings.TrimSpace(req.UpdateScope)
			if updateScope == "" {
				updateScope = strings.TrimSpace(req.Scope)
			}
			metadata := sessionruntime.PlanSaveMetadata{UpdateSummary: req.UpdateSummary, UpdateScope: updateScope, UpdateKind: req.UpdateKind, RevisionKind: req.RevisionKind, Checkpoint: req.Checkpoint, Document: req.Document}
			var plan pebblestore.SessionPlanSnapshot
			var event *pebblestore.EventEnvelope
			var err error
			if req.DocumentPatch != nil {
				activatePtr := &activate
				plan, event, err = s.sessions.PatchPlan(sessionID, sessionruntime.PlanPatchOptions{PlanID: planID, Title: req.Title, Status: req.Status, ApprovalState: req.ApprovalState, Activate: activatePtr, Document: req.Document, DocumentPatch: req.DocumentPatch, Metadata: metadata})
			} else {
				plan, event, err = s.sessions.SavePlanWithMetadata(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate, metadata)
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         true,
				"session_id": sessionID,
				"plan":       plan,
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if strings.HasSuffix(rest, "/permissions/resolve_all") {
		if s.perm == nil {
			writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
			return
		}
		sessionID := strings.TrimSuffix(rest, "/permissions/resolve_all")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}

		var req struct {
			Action string `json:"action"`
			Reason string `json:"reason"`
			Limit  int    `json:"limit"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolved, err := s.perm.ResolveAll(sessionID, req.Action, req.Reason, req.Limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"session_id": sessionID,
			"count":      len(resolved),
			"resolved":   resolved,
		})
		return
	}

	if strings.Contains(rest, "/permissions/") && strings.HasSuffix(rest, "/resolve") {
		if s.perm == nil {
			writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}

		base := strings.TrimSuffix(rest, "/resolve")
		parts := strings.Split(strings.Trim(base, "/"), "/permissions/")
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, errors.New("invalid permission resolve path"))
			return
		}
		sessionID := strings.Trim(parts[0], "/")
		permissionID := strings.Trim(parts[1], "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if permissionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}

		var req struct {
			Action            string          `json:"action"`
			Reason            string          `json:"reason"`
			ApprovedArguments json.RawMessage `json:"approved_arguments,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(sessionID, permissionID, req.Action, req.Reason, string(req.ApprovedArguments))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"session_id": sessionID,
			"permission": record,
			"saved_rule": savedRule,
		})
		return
	}

	if strings.HasSuffix(rest, "/permissions") {
		if s.perm == nil {
			writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
			return
		}
		sessionID := strings.TrimSuffix(rest, "/permissions")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		limit := 200
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		var permissions []pebblestore.PermissionRecord
		var err error
		switch status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))); status {
		case "", "all":
			permissions, err = s.perm.ListPermissions(sessionID, limit)
		case pebblestore.PermissionStatusPending:
			permissions, err = s.perm.ListPending(sessionID, limit)
		default:
			writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"session_id":  sessionID,
			"count":       len(permissions),
			"permissions": permissions,
		})
		return
	}

	if strings.HasSuffix(rest, "/usage") {
		sessionID := strings.TrimSuffix(rest, "/usage")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if _, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}

		summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		turns, err := s.sessions.ListTurnUsage(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var summaryPayload any
		if hasSummary {
			summaryPayload = summary
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                 true,
			"session_id":         sessionID,
			"has_usage_summary":  hasSummary,
			"usage_summary":      summaryPayload,
			"turn_usage_records": turns,
		})
		return
	}

	if strings.HasSuffix(rest, "/run") {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		sessionID := strings.TrimSuffix(rest, "/run")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		principal, _, ok := s.verifySessionOwnershipForRequest(w, r, sessionID)
		if !ok {
			return
		}
		if s.runner == nil {
			writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
			return
		}
		if s.isShuttingDown() {
			writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
			return
		}

		var req runruntime.RunRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.enforceSessionBindingWriteAccess(principal, sessionID, "run start"); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		if !s.beginActiveRun() {
			writeError(w, http.StatusServiceUnavailable, errors.New("daemon is shutting down"))
			return
		}
		defer s.endActiveRun()
		result, err := s.runner.RunTurn(identity.ContextWithPrincipal(r.Context(), principal), sessionID, req, runruntime.RunStartMeta{Principal: principal})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, runruntime.ErrSessionAlreadyActive) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		for _, event := range result.Events {
			s.hub.Publish(event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"result": result,
		})
		return
	}

	sessionID := strings.Trim(rest, "/")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, session, ok := s.verifySessionOwnershipForRequest(w, r, sessionID); !ok {
		return
	} else {
		s.writeSessionSnapshot(w, session)
	}
}

func (s *Server) writeSessionSnapshot(w http.ResponseWriter, session pebblestore.SessionSnapshot) {
	fields := gitStatusResponseForSession(session)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"session": struct {
			pebblestore.SessionSnapshot
			gitStatusResponseFields
			GitCommitDetected bool `json:"git_commit_detected,omitempty"`
			GitCommitCount    int  `json:"git_commit_count,omitempty"`
		}{
			SessionSnapshot:         session,
			gitStatusResponseFields: fields,
			GitCommitDetected:       gitCommitDetectedForSession(session, fields),
			GitCommitCount:          gitCommitCountForSession(session, fields),
		},
	})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	statuses, err := s.providers.ListStatuses(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": statuses,
	})
}

func (s *Server) codexSessionConfigResponse(sessionID string, config pebblestore.ModelPreference) map[string]any {
	resolved, err := s.model.ResolvePreference(config)
	effectiveContextWindow := 0
	if err == nil {
		effectiveContextWindow = resolved.ContextWindow
	}
	return map[string]any{
		"ok":                       true,
		"session_id":               strings.TrimSpace(sessionID),
		"provider":                 strings.TrimSpace(config.Provider),
		"model":                    strings.TrimSpace(config.Model),
		"thinking":                 strings.TrimSpace(config.Thinking),
		"service_tier":             strings.TrimSpace(config.ServiceTier),
		"context_mode":             strings.TrimSpace(config.ContextMode),
		"effective_context_window": effectiveContextWindow,
		"updated_at":               config.UpdatedAt,
	}
}

func (s *Server) handleSTTTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	var req struct {
		Profile   string `json:"profile"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Language  string `json:"language"`
		AudioBase string `json:"audio_base64"`
	}
	if err := decodeJSONLimited(w, r, &req, maxSTTRequestBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid STT request: %w", err))
		return
	}
	audio, err := decodeBase64Audio(req.AudioBase, maxSTTDecodedAudioBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.voice.Transcribe(ctx, voice.TranscribeInput{
		Profile:  req.Profile,
		Provider: req.Provider,
		Model:    req.Model,
		Language: req.Language,
		Audio:    audio,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": result.PathID,
		"result":  result,
	})
}

type uiSettingsPatchPresence struct {
	Theme     *uiThemeSettingsPatchPresence    `json:"theme"`
	Input     *uiInputSettingsPatchPresence    `json:"input"`
	Chat      *uiChatSettingsPatchPresence     `json:"chat"`
	Swarming  *uiSwarmingSettingsPatchPresence `json:"swarming"`
	Swarm     *uiSwarmSettingsPatchPresence    `json:"swarm"`
	Tools     *uiToolSettingsPatchPresence     `json:"tools"`
	UpdatedAt *int64                           `json:"updated_at"`
}

type uiThemeSettingsPatchPresence struct {
	ActiveID     *string                        `json:"active_id"`
	CustomThemes *[]uisettings.ThemeCustomTheme `json:"custom_themes"`
}

type uiInputSettingsPatchPresence struct {
	MouseEnabled *bool              `json:"mouse_enabled"`
	Keybinds     *map[string]string `json:"keybinds"`
}

type uiChatToolStreamSettingsPatchPresence struct {
	ShowAnchor    *bool     `json:"show_anchor"`
	PulseFrames   *[]string `json:"pulse_frames"`
	RunningSymbol *string   `json:"running_symbol"`
	SuccessSymbol *string   `json:"success_symbol"`
	ErrorSymbol   *string   `json:"error_symbol"`
}

type uiChatSettingsPatchPresence struct {
	ShowHeader                      *bool                                  `json:"show_header"`
	ShowTips                        *bool                                  `json:"show_tips"`
	ThinkingTags                    *bool                                  `json:"thinking_tags"`
	ShowCompactButton               *bool                                  `json:"show_compact_button"`
	DefaultNewSessionMode           *string                                `json:"default_new_session_mode"`
	FollowupCheckpointPolicyDefault *string                                `json:"followup_checkpoint_policy_default"`
	ReviewAutoArchiveMinutes        *int                                   `json:"review_auto_archive_minutes"`
	SidebarHideInactiveHours        *int                                   `json:"sidebar_hide_inactive_hours"`
	DefaultWorkspaceRoutes          *map[string]string                     `json:"default_workspace_routes"`
	ToolStream                      *uiChatToolStreamSettingsPatchPresence `json:"tool_stream"`
}

type uiSwarmingSettingsPatchPresence struct {
	Title  *string `json:"title"`
	Status *string `json:"status"`
}

type uiSwarmSettingsPatchPresence struct {
	Name             *string   `json:"name"`
	RemoteSSHTargets *[]string `json:"remote_ssh_targets"`
}

type uiToolImageSettingsPatchPresence struct {
	DefaultModel *string `json:"default_model"`
}

type uiToolSettingsPatchPresence struct {
	Image *uiToolImageSettingsPatchPresence `json:"image"`
}

func mergeUISettingsPatch(current, patch uisettings.UISettings, raw uiSettingsPatchPresence) uisettings.UISettings {
	settings := current
	if raw.Theme != nil {
		if raw.Theme.ActiveID != nil {
			settings.Theme.ActiveID = patch.Theme.ActiveID
		}
		if raw.Theme.CustomThemes != nil {
			settings.Theme.CustomThemes = patch.Theme.CustomThemes
		}
	}
	if raw.Input != nil {
		if raw.Input.MouseEnabled != nil {
			settings.Input.MouseEnabled = patch.Input.MouseEnabled
		}
		if raw.Input.Keybinds != nil {
			settings.Input.Keybinds = patch.Input.Keybinds
		}
	}
	if raw.Chat != nil {
		if raw.Chat.ShowHeader != nil {
			settings.Chat.ShowHeader = patch.Chat.ShowHeader
		}
		if raw.Chat.ShowTips != nil {
			settings.Chat.ShowTips = patch.Chat.ShowTips
		}
		if raw.Chat.ThinkingTags != nil {
			settings.Chat.ThinkingTags = patch.Chat.ThinkingTags
		}
		if raw.Chat.ShowCompactButton != nil {
			settings.Chat.ShowCompactButton = patch.Chat.ShowCompactButton
		}
		if raw.Chat.DefaultNewSessionMode != nil {
			settings.Chat.DefaultNewSessionMode = patch.Chat.DefaultNewSessionMode
		}
		if raw.Chat.FollowupCheckpointPolicyDefault != nil {
			settings.Chat.FollowupCheckpointPolicyDefault = patch.Chat.FollowupCheckpointPolicyDefault
		}
		if raw.Chat.ReviewAutoArchiveMinutes != nil {
			settings.Chat.ReviewAutoArchiveMinutes = patch.Chat.ReviewAutoArchiveMinutes
		}
		if raw.Chat.SidebarHideInactiveHours != nil {
			settings.Chat.SidebarHideInactiveHours = patch.Chat.SidebarHideInactiveHours
		}
		if raw.Chat.DefaultWorkspaceRoutes != nil {
			settings.Chat.DefaultWorkspaceRoutes = patch.Chat.DefaultWorkspaceRoutes
		}
		if raw.Chat.ToolStream != nil {
			if raw.Chat.ToolStream.ShowAnchor != nil {
				settings.Chat.ToolStream.ShowAnchor = patch.Chat.ToolStream.ShowAnchor
			}
			if raw.Chat.ToolStream.PulseFrames != nil {
				settings.Chat.ToolStream.PulseFrames = patch.Chat.ToolStream.PulseFrames
			}
			if raw.Chat.ToolStream.RunningSymbol != nil {
				settings.Chat.ToolStream.RunningSymbol = patch.Chat.ToolStream.RunningSymbol
			}
			if raw.Chat.ToolStream.SuccessSymbol != nil {
				settings.Chat.ToolStream.SuccessSymbol = patch.Chat.ToolStream.SuccessSymbol
			}
			if raw.Chat.ToolStream.ErrorSymbol != nil {
				settings.Chat.ToolStream.ErrorSymbol = patch.Chat.ToolStream.ErrorSymbol
			}
		}
	}
	if raw.Swarming != nil {
		if raw.Swarming.Title != nil {
			settings.Swarming.Title = patch.Swarming.Title
		}
		if raw.Swarming.Status != nil {
			settings.Swarming.Status = patch.Swarming.Status
		}
	}
	if raw.Swarm != nil {
		if raw.Swarm.Name != nil {
			settings.Swarm.Name = patch.Swarm.Name
		}
		if raw.Swarm.RemoteSSHTargets != nil {
			settings.Swarm.RemoteSSHTargets = patch.Swarm.RemoteSSHTargets
		}
	}
	if raw.Tools != nil && raw.Tools.Image != nil {
		if raw.Tools.Image.DefaultModel != nil {
			settings.Tools.Image.DefaultModel = patch.Tools.Image.DefaultModel
		}
	}
	return settings
}

func (s *Server) handleUISettings(w http.ResponseWriter, r *http.Request) {
	if s.uiSettings == nil {
		writeError(w, http.StatusInternalServerError, errors.New("ui settings service not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)

	switch r.Method {
	case http.MethodGet:
		settings, err := s.uiSettings.GetForAccount(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		_ = r.Body.Close()
		var raw uiSettingsPatchPresence
		if err := decodeJSONBytes(body, &raw); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var patch uisettings.UISettings
		if err := json.Unmarshal(body, &patch); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		current, err := s.uiSettings.GetForAccount(accountScopeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		settings := mergeUISettingsPatch(current, patch, raw)
		if raw.Swarm != nil && raw.Swarm.Name != nil {
			if s.swarm == nil {
				writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
				return
			}
			state, err := s.swarm.RenameLocalSwarm(swarmruntime.RenameLocalSwarmInput{Name: patch.Swarm.Name})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			settings.Swarm.Name = strings.TrimSpace(state.Node.Name)
		}
		saved, err := s.uiSettings.SetForAccount(accountScopeID, settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if raw.Chat != nil && raw.Chat.ReviewAutoArchiveMinutes != nil {
			if reconcileErr := s.reconcileSessionsV3ReviewAutoArchiveForAccount(r.Context(), time.Now(), accountScopeID); reconcileErr != nil {
				log.Printf("warning: v3 review auto-archive reconciliation after settings update failed account=%q: %v", accountScopeID, reconcileErr)
			}
		}
		writeJSON(w, http.StatusOK, saved)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	status, err := s.voice.Status(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": voice.PathStatus,
		"status":  status,
	})
}

func (s *Server) handleVoiceProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	profiles, err := s.voice.ListProfiles(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"path_id":  voice.PathProfilesList,
		"profiles": profiles,
	})
}

func (s *Server) handleVoiceProfileUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	var req struct {
		ID          string            `json:"id"`
		Label       string            `json:"label"`
		Adapter     string            `json:"adapter"`
		STTModel    string            `json:"stt_model"`
		STTLanguage string            `json:"stt_language"`
		TTSVoice    string            `json:"tts_voice"`
		Options     map[string]string `json:"options"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.voice.UpsertProfile(ctx, voice.ProfileUpsertInput{
		ID:          req.ID,
		Label:       req.Label,
		Adapter:     req.Adapter,
		STTModel:    req.STTModel,
		STTLanguage: req.STTLanguage,
		TTSVoice:    req.TTSVoice,
		Options:     req.Options,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": voice.PathProfilesUpsert,
		"profile": profile,
	})
}

func (s *Server) handleVoiceProfileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.voice.DeleteProfile(ctx, req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": result.PathID,
		"deleted": result.Deleted,
	})
}

func (s *Server) handleVoiceConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	var req struct {
		STTProfile  *string `json:"stt_profile"`
		STTProvider *string `json:"stt_provider"`
		STTModel    *string `json:"stt_model"`
		STTLanguage *string `json:"stt_language"`
		DeviceID    *string `json:"device_id"`
		TTSProfile  *string `json:"tts_profile"`
		TTSProvider *string `json:"tts_provider"`
		TTSVoice    *string `json:"tts_voice"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.voice.UpdateConfig(ctx, voice.ConfigPatch{
		STTProfile:  req.STTProfile,
		STTProvider: req.STTProvider,
		STTModel:    req.STTModel,
		STTLanguage: req.STTLanguage,
		DeviceID:    req.DeviceID,
		TTSProfile:  req.TTSProfile,
		TTSProvider: req.TTSProvider,
		TTSVoice:    req.TTSVoice,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": voice.PathConfig,
		"status":  status,
	})
}

func (s *Server) handleVoiceDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	devices, err := s.voice.ListDevices(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": voice.PathDevices,
		"devices": devices,
	})
}

func (s *Server) handleVoiceTestSTT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	var req struct {
		Profile  string `json:"profile"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Language string `json:"language"`
		DeviceID string `json:"device_id"`
		Seconds  int    `json:"seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.voice.TestSTT(ctx, voice.TestSTTInput{
		Profile:  req.Profile,
		Provider: req.Provider,
		Model:    req.Model,
		Language: req.Language,
		DeviceID: req.DeviceID,
		Seconds:  req.Seconds,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": result.PathID,
		"result":  result,
	})
}

func (s *Server) withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ws") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.security == nil {
			next.ServeHTTP(w, r)
			return
		}

		if s.isAuthExemptRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		if s.identitySessions != nil {
			if actor, err := s.identitySessions.Validate(productSessionTokenFromRequest(r)); err == nil {
				next.ServeHTTP(w, requestWithActorContext(r, actor))
				return
			}
		}

		if isLocalTransportRequest(r) {
			if s.identitySessions != nil {
				if actor, err := s.identitySessions.Validate(productSessionTokenFromRequest(r)); err == nil {
					next.ServeHTTP(w, requestWithActorContext(r, actor))
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		token := extractAttachToken(r)
		ok, err := s.security.ValidateAttachToken(token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if ok {
			next.ServeHTTP(w, r)
			return
		}

		log.Printf("attach auth denied method=%s path=%s remote_addr=%s", r.Method, r.URL.Path, strings.TrimSpace(r.RemoteAddr))
		s.security.AuditDenied(r.Method, r.URL.Path, r.RemoteAddr, "invalid attach token", token)
		writeError(w, http.StatusUnauthorized, errors.New("invalid or missing attach token"))
	})
}
func extractAttachToken(r *http.Request) string {
	headerToken := strings.TrimSpace(r.Header.Get("X-Swarm-Token"))
	if headerToken != "" {
		return headerToken
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

func (s *Server) isAuthExemptRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz":
		return true
	case "/v1/auth/desktop/session":
		return r.Method == http.MethodGet && shouldAllowDesktopLocalSessionBootstrapRequest(r)
	case "/v1/onboarding":
		return r.Method == http.MethodGet || (r.Method == http.MethodPost && s.allowsUnauthenticatedOnboardingPost(r))
	case TailscaleOnboardingApprovalPath:
		_, pending := pendingDesktopOrigin(r)
		_, admitted := admittedDesktopOrigin(r)
		return (pending || admitted) && (r.Method == http.MethodGet || (pending && r.Method == http.MethodPost))
	default:
		return false
	}
}

func isSameOriginBrowserRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if admission, ok := admittedDesktopOrigin(r); ok {
		if !browserHeadersMatchAdmittedOrigin(r, admission.origin) {
			return false
		}
		site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
		return site == "same-origin" || site == "same-site" || site == "none"
	}
	if !requestURLMatchesHeaderOrigin(r, r.Header.Get("Origin")) {
		return false
	}
	if !requestURLMatchesHeaderOrigin(r, r.Header.Get("Referer")) {
		return false
	}
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site == "" {
		return false
	}
	return site == "same-origin" || site == "same-site" || site == "none"
}

func requestURLMatchesHeaderOrigin(r *http.Request, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(u.Host), strings.TrimSpace(requestHost(r))) {
		return false
	}
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, requestScheme(r)) {
		return false
	}
	return true
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host != "" {
		return host
	}
	return strings.TrimSpace(r.URL.Host)
}

func requestScheme(r *http.Request) string {
	if r == nil {
		return "http"
	}
	if admission, ok := admittedDesktopOrigin(r); ok && admission.tailscaleServe && exactSingleHeaderValue(r.Header, "X-Forwarded-Proto", "https") {
		return "https"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func remoteRequestIP(r *http.Request) net.IP {
	hostPort := strings.TrimSpace(r.RemoteAddr)
	if hostPort == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return net.ParseIP("127.0.0.1")
	}
	return net.ParseIP(host)
}

func isTailscaleIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		_, cidr, err := net.ParseCIDR("100.64.0.0/10")
		return err == nil && cidr.Contains(v4)
	}
	_, cidr, err := net.ParseCIDR("fd7a:115c:a1e0::/48")
	return err == nil && cidr.Contains(ip)
}

func readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("missing request body")
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func decodeJSON(r *http.Request, out any) error {
	body, err := readRequestBody(r)
	if err != nil {
		return err
	}
	return decodeJSONBytes(body, out)
}

func decodeJSONLimited(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) error {
	if maxBytes <= 0 {
		return errors.New("request body limit must be positive")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := decodeJSON(r, out); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return fmt.Errorf("request body exceeds %d bytes", maxBytes)
		}
		return err
	}
	return nil
}

func decodeJSONBytes(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	return decodeJSONObject(decoder, out)
}

func decodeJSONObject(decoder *json.Decoder, out any) error {
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func decodeBase64Audio(raw string, maxDecodedBytes int) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("audio_base64 is required")
	}
	if maxDecodedBytes <= 0 {
		return nil, errors.New("decoded audio limit must be positive")
	}

	encoding := base64.RawStdEncoding
	decodedLen := encoding.DecodedLen(len(raw))
	if strings.HasSuffix(raw, "=") {
		encoding = base64.StdEncoding
		decodedLen = encoding.DecodedLen(len(raw))
		if strings.HasSuffix(raw, "==") {
			decodedLen -= 2
		} else {
			decodedLen--
		}
	}
	if decodedLen < 0 {
		return nil, errors.New("decode audio_base64: invalid padding")
	}
	if decodedLen > maxDecodedBytes {
		return nil, fmt.Errorf("audio_base64 exceeds %d decoded bytes", maxDecodedBytes)
	}

	audio := make([]byte, decodedLen)
	n, err := encoding.Decode(audio, []byte(raw))
	if err != nil {
		return nil, fmt.Errorf("decode audio_base64: %w", err)
	}
	return audio[:n], nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	header := w.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": err.Error(),
		"code":  strconv.Itoa(status),
	})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case "/v1/permissions/bypass":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg, err := s.loadStartupConfig()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !cfg.Exists {
			cfg = startupconfig.Default(cfg.Path)
		}
		cfg.BypassPermissions = req.Enabled
		if err := startupconfig.Write(cfg); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.SetBypassPermissions(req.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bypass_permissions": s.BypassPermissions()})
		return
	}
	var accountScopeID string
	switch path {
	case "/v1/permissions", "/v1/permissions/reset", "/v1/permissions/explain":
		principal, ok := PrincipalFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
			return
		}
		accountScopeID = strings.TrimSpace(principal.AccountScopeID)
	default:
		if strings.HasPrefix(path, "/v1/permissions/") {
			principal, ok := PrincipalFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
				return
			}
			accountScopeID = strings.TrimSpace(principal.AccountScopeID)
		}
	}
	switch {
	case path == "/v1/permissions/bash-profile":
		switch r.Method {
		case http.MethodGet:
			policy, err := s.perm.CurrentPolicyForAccount(accountScopeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bash_profile": policy.BashProfile})
		case http.MethodPost, http.MethodPut:
			var req struct {
				BashProfile permission.BashApprovalProfile `json:"bash_profile"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			policy, err := s.perm.UpdateBashApprovalProfileForAccount(accountScopeID, req.BashProfile)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bash_profile": policy.BashProfile})
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/permissions/capabilities":
		switch r.Method {
		case http.MethodGet:
			policy, err := s.perm.CurrentPolicyForAccount(accountScopeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_deploy": policy.SessionDeploy, "plan_acceptance": policy.PlanAcceptance})
		case http.MethodPost, http.MethodPut:
			current, err := s.perm.CurrentPolicyForAccount(accountScopeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			var req struct {
				SessionDeploy  *permission.SessionDeployPolicy  `json:"session_deploy"`
				PlanAcceptance *permission.PlanAcceptancePolicy `json:"plan_acceptance"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if req.SessionDeploy != nil {
				current.SessionDeploy = *req.SessionDeploy
			}
			if req.PlanAcceptance != nil {
				current.PlanAcceptance = *req.PlanAcceptance
			}
			policy, err := s.perm.UpdateCapabilityPoliciesForAccount(accountScopeID, current.SessionDeploy, current.PlanAcceptance)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_deploy": policy.SessionDeploy, "plan_acceptance": policy.PlanAcceptance})
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/permissions/subagents":
		switch r.Method {
		case http.MethodGet:
			policy, err := s.perm.CurrentPolicyForAccount(accountScopeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subagents": policy.Subagents})
		case http.MethodPost, http.MethodPut:
			var req permission.SubagentPolicy
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			updater, ok := s.perm.(interface {
				UpdateSubagentPolicyForAccount(string, permission.SubagentPolicy) (permission.Policy, error)
			})
			if !ok {
				writeError(w, http.StatusInternalServerError, errors.New("subagent permission policy updates are not configured"))
				return
			}
			policy, err := updater.UpdateSubagentPolicyForAccount(accountScopeID, req)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subagents": policy.Subagents})
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/permissions":
		switch r.Method {
		case http.MethodGet:
			policy, err := s.perm.CurrentPolicyForAccount(accountScopeID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": policy, "bypass_permissions": s.permissionBypassForAccount(accountScopeID)})
		case http.MethodPost:
			var req struct {
				Kind     string `json:"kind"`
				Decision string `json:"decision"`
				Tool     string `json:"tool"`
				Pattern  string `json:"pattern"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			rule, err := s.perm.UpsertRuleForAccount(accountScopeID, permission.PolicyRule{
				Kind:     permission.PolicyRuleKind(req.Kind),
				Decision: permission.PolicyDecision(req.Decision),
				Tool:     req.Tool,
				Pattern:  req.Pattern,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/permissions/reset":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		policy, err := s.perm.ResetPolicyForAccount(accountScopeID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": policy})
		return
	case path == "/v1/permissions/explain":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		explain, err := s.perm.ExplainToolForAccount(accountScopeID, r.URL.Query().Get("mode"), r.URL.Query().Get("tool"), r.URL.Query().Get("arguments"), nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "explain": explain})
		return
	case strings.HasPrefix(path, "/v1/permissions/"):
		ruleID := strings.TrimPrefix(path, "/v1/permissions/")
		ruleID = strings.Trim(ruleID, "/")
		if ruleID == "" {
			writeError(w, http.StatusBadRequest, errors.New("rule id is required"))
			return
		}
		if r.Method != http.MethodDelete {
			methodNotAllowed(w)
			return
		}
		removed, err := s.perm.RemoveRuleForAccount(accountScopeID, ruleID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed, "rule_id": ruleID})
		return
	default:
		writeError(w, http.StatusNotFound, errors.New("permission path not found"))
	}
}

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

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	containerprofiles "swarm/packages/swarmd/internal/containerprofiles"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/discovery"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	mcpruntime "swarm/packages/swarmd/internal/mcp"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/registry"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	runruntime "swarm/packages/swarmd/internal/run"
	sandboxruntime "swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/update"
	"swarm/packages/swarmd/internal/voice"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type codexOAuthSession struct {
	CodeVerifier string
	State        string
	Provider     string
	Label        string
	Active       bool
	Method       string
	AuthURL      string
	Status       string
	Error        string
	Credential   *auth.CredentialStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Server struct {
	auth                        *auth.Service
	agents                      *agentruntime.Service
	model                       *model.Service
	runner                      runService
	runStreams                  *runStreamManager
	sessions                    *sessionruntime.Service
	workspace                   *workspace.Service
	discovery                   *discovery.Service
	sandbox                     sandboxService
	worktrees                   worktreeService
	mcp                         mcpService
	security                    *security.Service
	providers                   *registry.Registry
	perm                        permissionService
	notifications               notificationService
	hub                         *stream.Hub
	events                      *pebblestore.EventLog
	voice                       *voice.Service
	uiSettings                  *uisettings.Service
	todos                       *todo.Service
	swarm                       swarmService
	containerProfiles           containerProfileService
	localContainers             localContainerService
	deployContainers            deployContainerService
	remoteDeploys               remoteDeployService
	update                      *update.Service
	swarmDesktopTargetSelection *pebblestore.SwarmDesktopTargetSelectionStore
	sessionRoutes               *pebblestore.SessionRouteStore
	mode                        string
	startupConfigPath           string
	startedAt                   time.Time
	bypassPermissions           bool

	codexOAuthMu       sync.Mutex
	codexOAuthSessions map[string]*codexOAuthSession

	shuttingDown         atomic.Bool
	runCtx               context.Context
	runCancel            context.CancelFunc
	runWG                sync.WaitGroup
	activeRuns           atomic.Int32
	requestStop          func(reason string)
	desktopLocalSessions *desktopLocalSessionManager
}

type runService interface {
	RunTurn(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error)
	RunTurnStreaming(ctx context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error)
	StopSessionRun(sessionID, runID, reason string) error
	ExecuteToolForSessionScope(ctx context.Context, workspacePath string, call tool.Call) (string, error)
	ListAgentToolDefinitions() []tool.Definition
	ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error)
}

type swarmService interface {
	EnsureLocalState(input swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error)
	ListGroupsForSwarm(swarmID string, limit int) ([]swarmruntime.GroupState, string, error)
	UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error)
	DeleteGroup(groupID string) error
	SetCurrentGroup(groupID string, localSwarmID string) (swarmruntime.GroupState, error)
	OutgoingPeerAuthToken(swarmID string) (string, bool, error)
	ValidateIncomingPeerAuth(swarmID, rawToken string) (bool, error)
	UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error)
	RemoveGroupMember(input swarmruntime.RemoveGroupMemberInput) error
	CreateInvite(input swarmruntime.CreateInviteInput) (swarmruntime.Invite, error)
	SubmitEnrollment(input swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error)
	ListPendingEnrollments(limit int) ([]swarmruntime.Enrollment, error)
	DecideEnrollment(input swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error)
	PrepareRemoteBootstrapParentPeer(input swarmruntime.PrepareRemoteBootstrapParentPeerInput) error
	FinalizeRemoteBootstrapChildPairing(input swarmruntime.FinalizeRemoteBootstrapChildPairingInput) (swarmruntime.PairingState, error)
	UpdateLocalPairingFromConfig(cfg startupconfig.FileConfig, transports []swarmruntime.TransportSummary) (swarmruntime.PairingState, error)
	DetachToStandalone(localSwarmID string) error
}

type containerProfileService interface {
	ListProfiles(ctx context.Context) ([]containerprofiles.Profile, error)
	UpsertProfile(ctx context.Context, input containerprofiles.UpsertInput) (containerprofiles.Profile, error)
	DeleteProfile(ctx context.Context, profileID string) (containerprofiles.DeleteResult, error)
}

type localContainerService interface {
	RuntimeStatus(ctx context.Context) (localcontainers.RuntimeStatus, error)
	List(ctx context.Context) ([]localcontainers.Container, error)
	Create(ctx context.Context, input localcontainers.CreateInput) (localcontainers.Container, error)
	Act(ctx context.Context, input localcontainers.ActionInput) (localcontainers.Container, error)
	BulkDelete(ctx context.Context, containerIDs []string) (localcontainers.DeleteResult, error)
	PruneMissing(ctx context.Context) (localcontainers.DeleteResult, error)
	SetHostCallbackURL(runtimeName, baseURL string)
	HostCallbackURL(runtimeName string) (string, bool)
}

type deployContainerService interface {
	RuntimeStatus(ctx context.Context) (deployruntime.ContainerRuntimeStatus, error)
	List(ctx context.Context) ([]deployruntime.ContainerDeployment, error)
	Create(ctx context.Context, input deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error)
	Act(ctx context.Context, input deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error)
	Delete(ctx context.Context, deploymentIDs []string) (localcontainers.DeleteResult, error)
	ChildAttachState(ctx context.Context, input deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error)
	AttachRequest(ctx context.Context, input deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error)
	AttachStatus(ctx context.Context, input deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error)
	AttachApprove(ctx context.Context, input deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error)
	FinalizeAttachFromHost(ctx context.Context, input deployruntime.ContainerAttachFinalizeInput) error
	SyncCredentialBundle(ctx context.Context, input deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error)
	SyncAgentBundle(ctx context.Context, input deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error)
	WorkspaceBootstrap(ctx context.Context, input deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error)
	AutoAttachChild(ctx context.Context) error
	UnlockManagedLocalChildVaults(ctx context.Context) error
}

type remoteDeployService interface {
	List(ctx context.Context) ([]remotedeploy.Session, error)
	ListCached(ctx context.Context) ([]remotedeploy.Session, error)
	Create(ctx context.Context, input remotedeploy.CreateSessionInput) (remotedeploy.Session, error)
	Delete(ctx context.Context, input remotedeploy.DeleteSessionInput) (localcontainers.DeleteResult, error)
	Start(ctx context.Context, input remotedeploy.StartSessionInput) (remotedeploy.Session, error)
	Approve(ctx context.Context, input remotedeploy.ApproveSessionInput) (remotedeploy.Session, error)
	ChildStatus(ctx context.Context, input remotedeploy.ChildStatusInput) (remotedeploy.Session, error)
	SyncCredentialBundle(ctx context.Context, input remotedeploy.SyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error)
}

type permissionService interface {
	ListPermissions(sessionID string, limit int) ([]pebblestore.PermissionRecord, error)
	ListPending(sessionID string, limit int) ([]pebblestore.PermissionRecord, error)
	Summary(sessionID string) (pebblestore.PermissionSummary, error)
	CreatePending(input permission.CreateInput) (pebblestore.PermissionRecord, error)
	Resolve(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, error)
	ResolveWithArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, error)
	ResolveAll(sessionID, action, reason string, limit int) ([]pebblestore.PermissionRecord, error)
	WaitForResolution(ctx context.Context, sessionID, permissionID string) (pebblestore.PermissionRecord, error)
	CancelRunPending(sessionID, runID, reason string) ([]pebblestore.PermissionRecord, error)
	CurrentPolicy() (permission.Policy, error)
	UpsertRule(rule permission.PolicyRule) (permission.PolicyRule, error)
	RemoveRule(ruleID string) (bool, error)
	ResetPolicy() (permission.Policy, error)
	ExplainTool(mode, toolName, toolArguments string, overlay *permission.Policy) (permission.PolicyExplain, error)
	ResolveWithPolicy(sessionID, permissionID, action, reason string) (pebblestore.PermissionRecord, *permission.PolicyRule, error)
	ResolveWithPolicyAndArguments(sessionID, permissionID, action, reason, approvedArguments string) (pebblestore.PermissionRecord, *permission.PolicyRule, error)
	MarkToolStarted(sessionID, runID, callID string, step int, startedAt int64) (pebblestore.PermissionRecord, bool, error)
	MarkToolCompleted(sessionID, runID, callID string, step int, result tool.Result, completedAt int64) (pebblestore.PermissionRecord, bool, error)
	SetBypassPermissions(enabled bool)
	BypassPermissions() bool
}

type notificationService interface {
	LocalSwarmID() string
	ListNotifications(swarmID string, limit int) ([]pebblestore.NotificationRecord, error)
	Summary(swarmID string) (pebblestore.NotificationSummary, error)
	UpdateNotification(input notification.UpdateInput) (pebblestore.NotificationRecord, bool, error)
	UpsertSystemNotification(record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error)
}

type sandboxService interface {
	GetStatus() (sandboxruntime.Status, error)
	Preflight() (sandboxruntime.Status, error)
	SetEnabled(enabled bool) (sandboxruntime.Status, *pebblestore.EventEnvelope, error)
}

type worktreeService interface {
	GetConfig(workspacePath string) (worktreeruntime.Config, error)
	SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (worktreeruntime.Config, *pebblestore.EventEnvelope, error)
	AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error)
	AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error)
	AttachBranch(workspacePath, sessionID, title string) (string, error)
}

type mcpService interface {
	List(limit int) ([]mcpruntime.Server, error)
	Get(id string) (mcpruntime.Server, bool, error)
	Upsert(input mcpruntime.UpsertInput) (mcpruntime.Server, *pebblestore.EventEnvelope, error)
	Delete(id string) (bool, *pebblestore.EventEnvelope, error)
	SetEnabled(id string, enabled bool) (mcpruntime.Server, *pebblestore.EventEnvelope, error)
}

func NewServer(mode string, authSvc *auth.Service, agentSvc *agentruntime.Service, modelSvc *model.Service, runSvc runService, sessionSvc *sessionruntime.Service, workspaceSvc *workspace.Service, discoverySvc *discovery.Service, securitySvc *security.Service, providers *registry.Registry, permSvc permissionService, notificationSvc notificationService, events *pebblestore.EventLog, hub *stream.Hub) *Server {
	runCtx, runCancel := context.WithCancel(context.Background())
	return &Server{
		auth:                 authSvc,
		agents:               agentSvc,
		model:                modelSvc,
		runner:               runSvc,
		runStreams:           newRunStreamManager(),
		sessions:             sessionSvc,
		workspace:            workspaceSvc,
		discovery:            discoverySvc,
		security:             securitySvc,
		providers:            providers,
		perm:                 permSvc,
		notifications:        notificationSvc,
		hub:                  hub,
		events:               events,
		mode:                 mode,
		startedAt:            time.Now(),
		codexOAuthSessions:   make(map[string]*codexOAuthSession),
		desktopLocalSessions: newDesktopLocalSessionManager(),
		runCtx:               runCtx,
		runCancel:            runCancel,
	}
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

func (s *Server) BypassPermissions() bool {
	if s == nil {
		return false
	}
	return s.bypassPermissions
}

func (s *Server) SetSandboxService(sandboxSvc sandboxService) {
	if s == nil {
		return
	}
	s.sandbox = sandboxSvc
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

func (s *Server) SetUISettingsService(uiSettingsSvc *uisettings.Service) {
	if s == nil {
		return
	}
	s.uiSettings = uiSettingsSvc
}

func (s *Server) SetTodoService(todoSvc *todo.Service) {
	if s == nil {
		return
	}
	s.todos = todoSvc
}

func (s *Server) SetSwarmService(swarmSvc swarmService) {
	if s == nil {
		return
	}
	s.swarm = swarmSvc
}

func (s *Server) SetContainerProfileService(containerProfileSvc containerProfileService) {
	if s == nil {
		return
	}
	s.containerProfiles = containerProfileSvc
}

func (s *Server) SetLocalContainerService(localContainerSvc localContainerService) {
	if s == nil {
		return
	}
	s.localContainers = localContainerSvc
}

func (s *Server) SetDeployContainerService(deployContainerSvc deployContainerService) {
	if s == nil {
		return
	}
	s.deployContainers = deployContainerSvc
}

func (s *Server) SetRemoteDeployService(remoteDeploySvc remoteDeployService) {
	if s == nil {
		return
	}
	s.remoteDeploys = remoteDeploySvc
}

func (s *Server) SetSwarmDesktopTargetSelectionStore(store *pebblestore.SwarmDesktopTargetSelectionStore) {
	if s == nil {
		return
	}
	s.swarmDesktopTargetSelection = store
}

func (s *Server) SetSessionRouteStore(store *pebblestore.SessionRouteStore) {
	if s == nil {
		return
	}
	s.sessionRoutes = store
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
	s.shuttingDown.Store(true)
}

func (s *Server) CancelInFlightRuns() {
	if s == nil {
		return
	}
	s.shuttingDown.Store(true)
	if s.runCancel != nil {
		s.runCancel()
	}
}

func (s *Server) WaitForInFlightRuns(timeout time.Duration) bool {
	if s == nil {
		return true
	}
	if timeout <= 0 {
		s.runWG.Wait()
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runWG.Wait()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
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

func (s *Server) beginActiveRun() {
	if s == nil {
		return
	}
	s.runWG.Add(1)
	s.activeRuns.Add(1)
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
	s.registerDeployRoutes(mux)
	s.registerAgentRoutes(mux)
	s.registerProviderRoutes(mux)
	s.registerWorkspaceRoutes(mux)
	s.registerRuntimeRoutes(mux)
	s.registerPeerRoutes(mux)
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

func hostedSessionHostBackendURL(cfg startupconfig.FileConfig) string {
	host := strings.TrimSpace(cfg.AdvertiseHost)
	if host == "" {
		host = strings.TrimSpace(cfg.Host)
	}
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "https://") || strings.HasPrefix(host, "http://") {
		return strings.TrimSuffix(host, "/")
	}
	port := cfg.AdvertisePort
	if port < 1 || port > 65535 {
		port = cfg.Port
	}
	if port < 1 || port > 65535 {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func (s *Server) DesktopHandler() http.Handler {
	apiHandler := s.withDesktopLocalSession(s.withAuth(s.withVaultGate(s.withJSON(s.apiMux()))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldServeDesktopAsset(r) {
			s.withDesktopLocalSession(s.withDesktopAssets(http.NotFoundHandler())).ServeHTTP(w, r)
			return
		}
		apiHandler.ServeHTTP(w, r)
	})
}

func (s *Server) handleDesktopStream(w http.ResponseWriter, r *http.Request) {
	remoteTarget, err := s.currentRemoteSwarmTargetForRequest(r)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if remoteTarget != nil {
		if err := s.proxyRequestToSwarmTarget(w, r, *remoteTarget); err != nil {
			writeError(w, http.StatusBadGateway, err)
		}
		return
	}
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
		"mode":               s.mode,
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

func (s *Server) handleSandbox(w http.ResponseWriter, r *http.Request) {
	if s.sandbox == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sandbox service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		status, err := s.sandbox.GetStatus()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"sandbox": status,
		})
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, errors.New("enabled is required"))
			return
		}
		status, event, err := s.sandbox.SetEnabled(*req.Enabled)
		if err != nil {
			var notReady *sandboxruntime.ErrNotReady
			if errors.As(err, &notReady) {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      false,
					"reason":  strings.TrimSpace(notReady.Error()),
					"sandbox": status,
				})
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"sandbox": status,
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSandboxPreflight(w http.ResponseWriter, r *http.Request) {
	if s.sandbox == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sandbox service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	status, err := s.sandbox.Preflight()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"sandbox": status,
	})
}

func (s *Server) handleWorktrees(w http.ResponseWriter, r *http.Request) {
	if s.worktrees == nil {
		writeError(w, http.StatusInternalServerError, errors.New("worktree service not configured"))
		return
	}

	workspacePath, err := s.resolveWorktreeConfigPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		config, err := s.worktrees.GetConfig(workspacePath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"worktrees": config,
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
			workspacePath, err = s.resolveWorktreeConfigPathForValue(workspacePath)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		current, err := s.worktrees.GetConfig(workspacePath)
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
		config, event, err := s.worktrees.SetConfig(workspacePath, enabled, useCurrentBranch, baseBranch, branchName)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"worktrees": config,
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
	workspacePath, err := s.resolveWorktreeConfigPath(r)
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
	current, ok, err := s.workspace.CurrentBinding()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("current workspace is not selected"))
		return
	}
	resolvedWorkspacePath := workspacePath
	if strings.TrimSpace(resolvedWorkspacePath) == "" {
		resolvedWorkspacePath = strings.TrimSpace(current.ResolvedPath)
	}
	callArgs, err := json.Marshal(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result, execErr := s.runner.ExecuteToolForSessionScope(r.Context(), resolvedWorkspacePath, tool.Call{Name: "manage-worktree", Arguments: string(callArgs)})
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

func (s *Server) resolveWorktreeConfigPath(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("request is required")
	}
	if value := strings.TrimSpace(r.URL.Query().Get("workspace_path")); value != "" {
		return s.resolveWorktreeConfigPathForValue(value)
	}
	if s.workspace == nil {
		return "", errors.New("workspace service not configured")
	}
	current, ok, err := s.workspace.CurrentBinding()
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(current.ResolvedPath) == "" {
		return "", errors.New("workspace path is required")
	}
	return s.resolveWorktreeConfigPathForValue(current.ResolvedPath)
}

func (s *Server) resolveWorktreeConfigPathForValue(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace path is required")
	}
	if s.workspace == nil {
		return "", errors.New("workspace service not configured")
	}
	scope, err := s.workspace.ScopeForPath(path)
	if err != nil {
		return "", err
	}
	if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
		return strings.TrimSpace(scope.WorkspacePath), nil
	}
	return strings.TrimSpace(scope.ResolvedPath), nil
}

func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, http.StatusInternalServerError, errors.New("mcp service not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
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
	servers, err := s.mcp.List(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"servers": servers,
		"count":   len(servers),
	})
}

func (s *Server) handleMCPServerUpsert(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, http.StatusInternalServerError, errors.New("mcp service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		Transport string            `json:"transport"`
		URL       string            `json:"url"`
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       map[string]string `json:"env"`
		Headers   map[string]string `json:"headers"`
		Enabled   *bool             `json:"enabled"`
		Source    string            `json:"source"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	server, event, err := s.mcp.Upsert(mcpruntime.UpsertInput{
		ID:        req.ID,
		Name:      req.Name,
		Transport: req.Transport,
		URL:       req.URL,
		Command:   req.Command,
		Args:      req.Args,
		Env:       req.Env,
		Headers:   req.Headers,
		Enabled:   req.Enabled,
		Source:    req.Source,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"server": server,
	})
}

func (s *Server) handleMCPServerDelete(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, http.StatusInternalServerError, errors.New("mcp service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deleted, event, err := s.mcp.Delete(req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, errors.New("mcp server not found"))
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"deleted": true,
		"id":      strings.ToLower(strings.TrimSpace(req.ID)),
	})
}

func (s *Server) handleMCPServerEnabled(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, http.StatusInternalServerError, errors.New("mcp service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	server, event, err := s.mcp.SetEnabled(req.ID, *req.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"server": server,
	})
}

func (s *Server) handleCodexAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status, err := s.auth.CodexStatus()
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
			status, event, err = s.auth.SetCodexKey(req.APIKey)
		case "oauth":
			status, event, err = s.auth.SetCodexOAuth(req.AccessToken, req.RefreshToken, req.ExpiresAt, req.AccountID)
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
		autoDefaults, defaultsErr := s.applyUtilityModelDefaults("codex")
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
		list, err := s.auth.ListCredentials(provider, query, limit)
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
		status, event, err := s.auth.UpsertCredential(auth.CredentialUpsertInput{
			ID:           req.ID,
			Provider:     provider,
			Type:         req.Type,
			Label:        req.Label,
			Tags:         req.Tags,
			APIKey:       req.APIKey,
			AccessToken:  req.AccessToken,
			RefreshToken: req.RefreshToken,
			ExpiresAt:    req.ExpiresAt,
			AccountID:    req.AccountID,
			Active:       req.Active,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil {
			s.hub.Publish(*event)
		}
		connection, verifyErr := s.verifyAuthCredentialConnection(r.Context(), provider, status.ID)
		if verifyErr != nil {
			writeError(w, http.StatusInternalServerError, verifyErr)
			return
		}
		status.Connection = connection
		autoDefaults, defaultsErr := s.applyUtilityModelDefaults(provider)
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

func (s *Server) handleAuthCredentialVerify(w http.ResponseWriter, r *http.Request) {
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
	connection, verifyErr := s.verifyAuthCredentialConnection(r.Context(), provider, credentialID)
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
	status, event, err := s.auth.SetActiveCredential(provider, req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event != nil {
		s.hub.Publish(*event)
	}
	connection, verifyErr := s.verifyAuthCredentialConnection(r.Context(), provider, status.ID)
	if verifyErr != nil {
		writeError(w, http.StatusInternalServerError, verifyErr)
		return
	}
	status.Connection = connection
	autoDefaults, defaultsErr := s.applyUtilityModelDefaults(provider)
	if defaultsErr != nil {
		status.AutoDefaults = &auth.AutoDefaultsStatus{Error: defaultsErr.Error()}
	} else if autoDefaults != nil {
		status.AutoDefaults = autoDefaults
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAuthCredentialDelete(w http.ResponseWriter, r *http.Request) {
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
	deleted, event, err := s.auth.DeleteCredential(provider, req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, errors.New("credential not found"))
		return
	}
	cleanup, err := s.cleanupProviderAfterCredentialDeletion(r.Context(), provider)
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
	switch r.Method {
	case http.MethodGet:
		pref, err := s.model.GetResolvedGlobalPreference()
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
		pref, event, err := s.model.SetGlobalPreference(req.Provider, req.Model, req.Thinking, req.ServiceTier, req.ContextMode)
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
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleModelFavorites(w http.ResponseWriter, r *http.Request) {
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service not configured"))
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
		records, err := s.model.ListFavorites(provider, query, limit)
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
		record, event, err := s.model.UpsertFavorite(req.Provider, req.Model, req.Label, req.Thinking)
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
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deleted, event, err := s.model.DeleteFavorite(req.Provider, req.Model)
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
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	resolution, err := s.workspace.Resolve(cwd)
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
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.Select(req.Path)
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
	resolution, ok, err := s.workspace.CurrentBinding()
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
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	entries, err := s.workspace.ListKnown(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entries, err = s.applyWorkspaceWorktreeStatus(entries)
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
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	browser, err := s.workspace.Browse(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"browser": browser,
	})
}

func (s *Server) handleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
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
	resolution, err := s.workspace.Add(req.Path, req.Name, req.ThemeID, makeCurrent)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceDirectoryAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
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
	resolution, err := s.workspace.AddDirectory(req.WorkspacePath, req.DirectoryPath)
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
	var req struct {
		WorkspacePath string `json:"workspace_path"`
		DirectoryPath string `json:"directory_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.RemoveDirectory(req.WorkspacePath, req.DirectoryPath)
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
	var req struct {
		Path  string `json:"path"`
		Delta int    `json:"delta"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.Move(req.Path, req.Delta)
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
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.Rename(req.Path, req.Name)
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
	var req struct {
		Path    string `json:"path"`
		ThemeID string `json:"theme_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.SetThemeID(req.Path, req.ThemeID)
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
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolution, err := s.workspace.Delete(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"workspace": resolution,
	})
}

func (s *Server) handleWorkspaceTodos(w http.ResponseWriter, r *http.Request) {
	if s.todos == nil {
		writeError(w, http.StatusInternalServerError, errors.New("todo service not configured"))
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	resolveWorkspacePath := func(raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", errors.New("workspace path is required")
		}
		scope, err := s.workspace.ScopeForPath(raw)
		if err != nil {
			return "", err
		}
		if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
			return strings.TrimSpace(scope.WorkspacePath), nil
		}
		return strings.TrimSpace(scope.ResolvedPath), nil
	}

	switch r.Method {
	case http.MethodGet:
		workspacePath, err := resolveWorkspacePath(r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ownerKind, err := normalizeWorkspaceTodoOwnerKindRequest(r.URL.Query().Get("owner_kind"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		items, summary, err := s.todos.List(workspacePath, todo.ListOptions{OwnerKind: ownerKind, SessionID: sessionID})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"workspace_path": workspacePath,
			"owner_kind":     ownerKind,
			"session_id":     sessionID,
			"items":          items,
			"summary":        summary,
		})
	case http.MethodPost:
		var req struct {
			Action        string   `json:"action"`
			WorkspacePath string   `json:"workspace_path"`
			OwnerKind     string   `json:"owner_kind"`
			ID            string   `json:"id"`
			Text          string   `json:"text"`
			Done          *bool    `json:"done"`
			Priority      string   `json:"priority"`
			Group         string   `json:"group"`
			Tags          []string `json:"tags"`
			InProgress    *bool    `json:"in_progress"`
			SessionID     string   `json:"session_id"`
			ParentID      string   `json:"parent_id"`
			OrderedIDs    []string `json:"ordered_ids"`
			Operations    []struct {
				Action     string   `json:"action"`
				ID         string   `json:"id"`
				OwnerKind  string   `json:"owner_kind"`
				Text       *string  `json:"text"`
				Done       *bool    `json:"done"`
				Priority   *string  `json:"priority"`
				Group      *string  `json:"group"`
				Tags       []string `json:"tags"`
				InProgress *bool    `json:"in_progress"`
				SessionID  *string  `json:"session_id"`
				ParentID   *string  `json:"parent_id"`
				OrderedIDs []string `json:"ordered_ids"`
			} `json:"operations"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspacePath, err := resolveWorkspacePath(req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ownerKind, err := normalizeWorkspaceTodoOwnerKindRequest(req.OwnerKind)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "upsert"
		}
		switch action {
		case "create":
			item, summary, _, err := s.todos.Create(todo.CreateInput{
				WorkspacePath: workspacePath,
				OwnerKind:     ownerKind,
				Text:          req.Text,
				Priority:      req.Priority,
				Group:         req.Group,
				Tags:          req.Tags,
				InProgress:    req.InProgress != nil && *req.InProgress,
				SessionID:     req.SessionID,
				ParentID:      req.ParentID,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
		case "update", "upsert":
			priority := req.Priority
			group := req.Group
			sessionID := req.SessionID
			if ownerKind == pebblestore.WorkspaceTodoOwnerKindAgent && strings.TrimSpace(sessionID) == "" {
				sessionID = req.SessionID
			}
			item, summary, _, err := s.todos.Update(todo.UpdateInput{
				WorkspacePath: workspacePath,
				ID:            req.ID,
				Text:          stringPointerIfPresent(req.Text),
				Done:          req.Done,
				Priority:      stringPointerIfPresent(priority),
				Group:         stringPointerIfPresent(group),
				Tags:          req.Tags,
				InProgress:    req.InProgress,
				SessionID:     stringPointerIfPresent(sessionID),
				ParentID:      stringPointerIfPresent(req.ParentID),
			}, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(sessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
		case "delete":
			summary, _, err := s.todos.Delete(workspacePath, req.ID, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": strings.TrimSpace(req.ID), "summary": summary})
		case "delete_done":
			items, summary, _, err := s.todos.DeleteDone(workspacePath, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
		case "delete_all":
			items, summary, _, err := s.todos.DeleteAll(workspacePath, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
		case "reorder":
			items, summary, _, err := s.todos.Reorder(todo.ReorderInput{WorkspacePath: workspacePath, OwnerKind: ownerKind, OrderedIDs: req.OrderedIDs}, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
		case "in_progress":
			item, summary, _, err := s.todos.SetInProgress(workspacePath, req.ID, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
		case "batch":
			operations := make([]todo.BatchOperation, 0, len(req.Operations))
			for _, rawOp := range req.Operations {
				opOwnerKind := rawOp.OwnerKind
				if strings.TrimSpace(opOwnerKind) == "" {
					opOwnerKind = ownerKind
				}
				operations = append(operations, todo.BatchOperation{
					Action:     rawOp.Action,
					ID:         rawOp.ID,
					OwnerKind:  opOwnerKind,
					Text:       rawOp.Text,
					Done:       rawOp.Done,
					Priority:   rawOp.Priority,
					Group:      rawOp.Group,
					Tags:       rawOp.Tags,
					InProgress: rawOp.InProgress,
					SessionID:  rawOp.SessionID,
					ParentID:   rawOp.ParentID,
					OrderedIDs: rawOp.OrderedIDs,
				})
			}
			results, items, summary, _, err := s.todos.ApplyBatch(workspacePath, operations, todo.ListOptions{OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results, "items": items, "summary": summary, "operation_count": len(operations)})
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported todo action %q", action))
		}
	default:
		methodNotAllowed(w)
	}
}

func stringPointerIfPresent(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func normalizeWorkspaceTodoOwnerKindRequest(raw string) (string, error) {
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

func (s *Server) handleContextSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.discovery == nil {
		writeError(w, http.StatusInternalServerError, errors.New("discovery service not configured"))
		return
	}

	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		current, ok, err := s.workspace.CurrentBinding()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if ok {
			cwd = current.ResolvedPath
		}
	}

	scope, err := s.workspace.ScopeForPath(cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
			sessions, listErr = s.sessions.ListSessions(limit)
		} else if s.workspace != nil {
			scope, scopeErr := s.workspace.ScopeForPath(cwd)
			if scopeErr != nil {
				writeError(w, http.StatusBadRequest, scopeErr)
				return
			}
			if exactPath {
				sessions, listErr = s.sessions.ListSessionsForPath(scope.ResolvedPath, limit)
			} else if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
				sessions, listErr = s.sessions.ListSessionsForScope(scope.WorkspacePath, limit)
			} else {
				sessions, listErr = s.sessions.ListSessionsForPath(scope.ResolvedPath, limit)
			}
		} else {
			sessions, listErr = s.sessions.ListSessionsForPath(cwd, limit)
		}
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		type sessionSummaryResponse struct {
			pebblestore.SessionSnapshot
			gitStatusResponseFields
			GitCommitDetected      bool `json:"git_commit_detected,omitempty"`
			GitCommitCount         int  `json:"git_commit_count,omitempty"`
			PendingPermissionCount int  `json:"pending_permission_count"`
		}
		responseSessions := make([]sessionSummaryResponse, 0, len(sessions))
		for _, session := range sessions {
			pendingPermissionCount := 0
			if s.perm != nil {
				summary, err := s.perm.Summary(session.ID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				pendingPermissionCount = summary.PendingCount
			}
			fields := gitStatusResponseForSession(session)
			responseSessions = append(responseSessions, sessionSummaryResponse{
				SessionSnapshot:         session,
				gitStatusResponseFields: fields,
				GitCommitDetected:       gitCommitDetectedForSession(session, fields),
				GitCommitCount:          gitCommitCountForSession(session, fields),
				PendingPermissionCount:  pendingPermissionCount,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"sessions": responseSessions,
		})
	case http.MethodPost:
		req, err := s.decodeSessionCreateRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		remoteTarget, err := s.currentRemoteSwarmTargetForRequest(r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		if remoteTarget != nil {
			if s.sessionRoutes == nil {
				writeError(w, http.StatusInternalServerError, errors.New("session route store not configured"))
				return
			}
			if strings.EqualFold(strings.TrimSpace(req.RuntimeWorkspacePath), strings.TrimSpace(req.HostWorkspacePath)) ||
				strings.EqualFold(strings.TrimSpace(req.RuntimeWorkspacePath), strings.TrimSpace(req.WorkspacePath)) {
				if resolved := s.resolveRemoteRuntimeWorkspacePath(r.Context(), *remoteTarget, req.HostWorkspacePath, req.WorkspaceName); strings.TrimSpace(resolved) != "" {
					req.RuntimeWorkspacePath = strings.TrimSpace(resolved)
				}
			}
			cfg, cfgErr := s.loadStartupConfig()
			if cfgErr != nil {
				writeError(w, http.StatusInternalServerError, cfgErr)
				return
			}
			state, stateErr := s.currentSwarmState(cfg)
			if stateErr != nil {
				writeError(w, http.StatusInternalServerError, stateErr)
				return
			}
			hostBackendURL := hostedSessionHostBackendURL(cfg)
			if resolved := s.resolveRemoteHostBackendURL(r.Context(), *remoteTarget); strings.TrimSpace(resolved) != "" {
				hostBackendURL = strings.TrimSpace(resolved)
			}
			routeMetadata := map[string]any{
				sessionruntime.HostedSessionMetadataHostBackendURL:       hostBackendURL,
				sessionruntime.HostedSessionMetadataChildSwarmID:         strings.TrimSpace(remoteTarget.SwarmID),
				sessionruntime.HostedSessionMetadataHostWorkspacePath:    strings.TrimSpace(req.HostWorkspacePath),
				sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: strings.TrimSpace(req.RuntimeWorkspacePath),
			}
			sessionID := sessionruntime.NewSessionID()
			session, event, warning, modeWarning, err := s.createSessionFromRequestWithSessionID(req, routeMetadata, false, sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			rollbackHostedCreate := func(cause error) {
				if cleanupErr := s.rollbackHostedSessionCreate(session.ID); cleanupErr != nil {
					log.Printf("hosted session create rollback failed session_id=%q err=%v", session.ID, cleanupErr)
				}
				writeError(w, http.StatusBadGateway, cause)
			}
			if _, err := s.sessionRoutes.Put(pebblestore.SessionRouteRecord{
				SessionID:            session.ID,
				ChildSwarmID:         strings.TrimSpace(remoteTarget.SwarmID),
				ChildBackendURL:      strings.TrimSpace(remoteTarget.BackendURL),
				HostWorkspacePath:    strings.TrimSpace(req.HostWorkspacePath),
				RuntimeWorkspacePath: strings.TrimSpace(req.RuntimeWorkspacePath),
				CreatedAt:            session.CreatedAt,
				UpdatedAt:            session.UpdatedAt,
			}); err != nil {
				if cleanupErr := s.sessions.DeleteSession(session.ID); cleanupErr != nil {
					log.Printf("hosted session create rollback failed session_id=%q err=%v", session.ID, cleanupErr)
				}
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			childDescriptor := sessionruntime.HostedSessionDescriptor{
				HostSwarmID:          strings.TrimSpace(state.Node.SwarmID),
				HostBackendURL:       hostBackendURL,
				HostWorkspacePath:    strings.TrimSpace(req.HostWorkspacePath),
				RuntimeWorkspacePath: strings.TrimSpace(req.RuntimeWorkspacePath),
				ChildSwarmID:         strings.TrimSpace(remoteTarget.SwarmID),
			}
			var childResp struct {
				OK      bool                        `json:"ok"`
				Session pebblestore.SessionSnapshot `json:"session"`
				Warning string                      `json:"warning,omitempty"`
			}
			if err := s.postPeerJSONToSwarmTarget(r.Context(), *remoteTarget, "/v1/swarm/peer/sessions/open", peerSessionOpenRequest{
				SessionID: session.ID,
				Request:   req,
				Hosted:    childDescriptor,
			}, &childResp); err != nil {
				rollbackHostedCreate(hostedSessionOpenError(*remoteTarget, err))
				return
			}
			syncedSession, syncErr := s.sessions.SyncHostedMirrorOpenState(session.ID, childResp.Session)
			if syncErr != nil {
				log.Printf("hosted session create mirror sync failed session_id=%q err=%v", session.ID, syncErr)
			} else {
				session = syncedSession
			}
			if event != nil {
				s.hub.Publish(*event)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":      true,
				"session": session,
				"warning": strings.TrimSpace(strings.Join([]string{warning, modeWarning, childResp.Warning}, " ")),
			})
			return
		}
		session, event, warning, modeWarning, err := s.createSessionFromRequest(req, nil, true)
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

func hostedSessionOpenError(target swarmTarget, err error) error {
	if err == nil {
		return nil
	}
	trimmed := strings.TrimSpace(err.Error())
	if strings.HasPrefix(trimmed, "404 ") || strings.EqualFold(trimmed, http.StatusText(http.StatusNotFound)) {
		return fmt.Errorf("child swarm %q at %q returned 404 for routed peer session open; the child runtime is missing the routed session API. Rebuild the child image/runtime and recreate the deployment", strings.TrimSpace(target.SwarmID), strings.TrimSpace(target.BackendURL))
	}
	return err
}

func (s *Server) rollbackHostedSessionCreate(sessionID string) error {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	var failures []string
	if s.sessionRoutes != nil {
		if err := s.sessionRoutes.Delete(sessionID); err != nil {
			failures = append(failures, "delete session route: "+err.Error())
		}
	}
	if s.sessions != nil {
		if err := s.sessions.DeleteSession(sessionID); err != nil {
			failures = append(failures, "delete session: "+err.Error())
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
		merged[key] = value
	}
	return merged
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			session, ok, err := s.sessions.GetSession(sessionID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"ok":    false,
					"error": "session not found",
				})
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
			profile, profileErr := s.agents.ResolvePrimary("")
			if profileErr != nil {
				writeError(w, http.StatusBadRequest, profileErr)
				return
			}
			requestedMode := sessionruntime.NormalizeMode(req.Mode)
			modeWarning := ""
			if !pebblestore.AgentExitPlanModeEnabled(profile) {
				setting, ok := pebblestore.AgentExecutionSetting(profile)
				if !ok {
					agentName := strings.TrimSpace(profile.Name)
					if agentName == "" {
						agentName = "active primary agent"
					}
					writeError(w, http.StatusBadRequest, fmt.Errorf("%s has plan mode disabled but no execution_setting is configured", agentName))
					return
				}
				if requestedMode != setting {
					modeWarning = fmt.Sprintf("active primary agent %q has plan mode disabled; ignoring requested session mode %q and using execution setting %q", strings.TrimSpace(profile.Name), requestedMode, setting)
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
		if r.Method == http.MethodPost && s.proxyRoutedSessionRequest(w, r, sessionID) {
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
				ID            string `json:"id"`
				PlanID        string `json:"plan_id"`
				Title         string `json:"title"`
				Plan          string `json:"plan"`
				Status        string `json:"status"`
				ApprovalState string `json:"approval_state"`
				Activate      *bool  `json:"activate"`
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
			plan, event, err := s.sessions.SavePlan(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate)
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
		limit := 200
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		pending, err := s.perm.ListPermissions(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"session_id":  sessionID,
			"count":       len(pending),
			"permissions": pending,
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
		if s.runner == nil {
			writeError(w, http.StatusInternalServerError, errors.New("run service not configured"))
			return
		}
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
		if s.proxyRoutedSessionRequest(w, r, sessionID) {
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

		s.beginActiveRun()
		defer s.endActiveRun()
		result, err := s.runner.RunTurn(r.Context(), sessionID, req, runruntime.RunStartMeta{})
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

	if strings.HasSuffix(rest, "/run/stream") {
		sessionID := strings.TrimSuffix(rest, "/run/stream")
		sessionID = strings.Trim(sessionID, "/")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session id is required"))
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.handleRunStreamWebsocket(w, r, sessionID)
		case http.MethodPost:
			s.handleRunStreamControl(w, r, sessionID)
		default:
			writeError(w, http.StatusUpgradeRequired, errors.New("run stream requires websocket upgrade (GET) or control POST"))
		}
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
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": "session not found",
		})
		return
	}
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
	statuses, err := s.providers.ListStatuses(context.Background())
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

func (s *Server) codexSessionEffectiveContextWindow(config pebblestore.ModelPreference) int {
	resolved, err := s.model.ResolvePreference(config)
	if err != nil {
		return 0
	}
	return resolved.ContextWindow
}

func (s *Server) handleSTTTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
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
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	audio, err := decodeBase64Audio(req.AudioBase)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.voice.Transcribe(context.Background(), voice.TranscribeInput{
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

func (s *Server) handleUISettings(w http.ResponseWriter, r *http.Request) {
	if s.uiSettings == nil {
		writeError(w, http.StatusInternalServerError, errors.New("ui settings service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		settings, err := s.uiSettings.Get()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPost:
		var settings uisettings.UISettings
		if err := decodeJSON(r, &settings); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := s.uiSettings.Set(settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
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
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	status, err := s.voice.Status(context.Background())
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
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	profiles, err := s.voice.ListProfiles(context.Background())
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
	profile, err := s.voice.UpsertProfile(context.Background(), voice.ProfileUpsertInput{
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
	result, err := s.voice.DeleteProfile(context.Background(), req.ID)
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
	status, err := s.voice.UpdateConfig(context.Background(), voice.ConfigPatch{
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
	if s.voice == nil {
		writeError(w, http.StatusInternalServerError, errors.New("voice service not configured"))
		return
	}
	devices, err := s.voice.ListDevices(context.Background())
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
	result, err := s.voice.TestSTT(context.Background(), voice.TestSTTInput{
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

		loopback := isLoopbackRequest(r)
		if isAuthExemptRequest(r, loopback, isTrustedNetworkRequest(r)) {
			next.ServeHTTP(w, r)
			return
		}

		if shouldUseDesktopLocalSessionAuth(r) {
			if token := desktopLocalSessionTokenFromRequest(r); s.desktopLocalSessions != nil && s.desktopLocalSessions.Validate(token, time.Now()) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if isLocalTransportRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if bootstrapAuthed, updatedReq, err := authorizeBootstrapRequest(r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if bootstrapAuthed {
			next.ServeHTTP(w, updatedReq)
			return
		} else {
			r = updatedReq
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

		peerSwarmID, peerToken := extractPeerAuth(r)
		if peerSwarmID != "" && peerToken != "" && s.swarm != nil {
			peerOK, err := s.swarm.ValidateIncomingPeerAuth(peerSwarmID, peerToken)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if peerOK {
				next.ServeHTTP(w, r)
				return
			}
			log.Printf("peer auth denied method=%s path=%s remote_addr=%s peer_swarm_id=%q", r.Method, r.URL.Path, strings.TrimSpace(r.RemoteAddr), peerSwarmID)
			s.security.AuditDenied(r.Method, r.URL.Path, r.RemoteAddr, "invalid peer auth", peerToken)
			writeError(w, http.StatusUnauthorized, errors.New("invalid peer auth"))
			return
		}

		log.Printf("attach auth denied method=%s path=%s remote_addr=%s", r.Method, r.URL.Path, strings.TrimSpace(r.RemoteAddr))
		s.security.AuditDenied(r.Method, r.URL.Path, r.RemoteAddr, "invalid attach token", token)
		writeError(w, http.StatusUnauthorized, errors.New("invalid or missing attach token"))
	})
}

func authorizeBootstrapRequest(r *http.Request) (bool, *http.Request, error) {
	if r == nil {
		return false, r, nil
	}
	switch r.URL.Path {
	case "/v1/swarm/enroll", "/v1/swarm/remote-pairing/request":
		inviteToken, updatedReq, err := inviteTokenFromBootstrapRequest(r)
		if err != nil {
			return false, updatedReq, err
		}
		return strings.TrimSpace(inviteToken) != "", updatedReq, nil
	default:
		return false, r, nil
	}
}

func inviteTokenFromBootstrapRequest(r *http.Request) (string, *http.Request, error) {
	if r == nil {
		return "", r, nil
	}
	if r.Body == nil {
		return "", r, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", r, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if len(body) == 0 {
		return "", r, nil
	}
	var payload struct {
		InviteToken string `json:"invite_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", r, err
	}
	return strings.TrimSpace(payload.InviteToken), r, nil
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
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func extractPeerAuth(r *http.Request) (string, string) {
	if r == nil {
		return "", ""
	}
	return strings.TrimSpace(r.Header.Get(peerAuthSwarmIDHeader)), strings.TrimSpace(r.Header.Get(peerAuthTokenHeader))
}

func isAuthExemptRequest(r *http.Request, loopback, trustedNetwork bool) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz":
		return true
	case "/v1/auth/desktop/session":
		return r.Method == http.MethodGet && shouldUseDesktopLocalSessionAuth(r)
	case "/v1/update/status":
		return loopback && r.Method == http.MethodGet
	case "/v1/swarm/discovery":
		return trustedNetwork && r.Method == http.MethodGet
	case "/v1/deploy/container/attach/child-state", "/v1/deploy/container/attach/request", "/v1/deploy/container/attach/approve", "/v1/deploy/container/attach/finalize", "/v1/deploy/container/sync/credentials", "/v1/deploy/container/sync/agents", "/v1/deploy/container/workspaces/bootstrap", "/v1/deploy/remote/session/sync/credentials":
		return trustedNetwork && r.Method == http.MethodPost
	default:
		return false
	}
}

func isLoopbackRequest(r *http.Request) bool {
	ip := remoteRequestIP(r)
	return ip != nil && ip.IsLoopback()
}

func isTrustedNetworkRequest(r *http.Request) bool {
	ip := remoteRequestIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	return isTailscaleIP(ip)
}

func isSameOriginBrowserRequest(r *http.Request) bool {
	if r == nil {
		return false
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
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
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

func decodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
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

func decodeBase64Audio(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("audio_base64 is required")
	}
	audio, err := base64.StdEncoding.DecodeString(raw)
	if err == nil {
		return audio, nil
	}
	audio, rawErr := base64.RawStdEncoding.DecodeString(raw)
	if rawErr == nil {
		return audio, nil
	}
	return nil, fmt.Errorf("decode audio_base64: %w", err)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
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
	switch {
	case path == "/v1/permissions":
		switch r.Method {
		case http.MethodGet:
			policy, err := s.perm.CurrentPolicy()
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": policy})
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
			rule, err := s.perm.UpsertRule(permission.PolicyRule{
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
		policy, err := s.perm.ResetPolicy()
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
		explain, err := s.perm.ExplainTool(r.URL.Query().Get("mode"), r.URL.Query().Get("tool"), r.URL.Query().Get("arguments"), nil)
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
		removed, err := s.perm.RemoveRule(ruleID)
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

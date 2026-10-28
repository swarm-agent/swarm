package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm-refactor/swarmtui/pkg/storagecontract"
	actionruntime "swarm/packages/swarmd/internal/action"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/artifactv2"
	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/gitenv"
	"swarm/packages/swarmd/internal/htmlcapture"
	identityruntime "swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	integrationruntime "swarm/packages/swarmd/internal/integration"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/lock"
	"swarm/packages/swarmd/internal/longsessiondiag"
	mcpruntime "swarm/packages/swarmd/internal/mcp"
	"swarm/packages/swarmd/internal/mediastaging"
	"swarm/packages/swarmd/internal/memory"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/outbox"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/anthropic"
	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/codex"
	"swarm/packages/swarmd/internal/provider/copilot"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	exaprovider "swarm/packages/swarmd/internal/provider/exa"
	"swarm/packages/swarmd/internal/provider/fireworks"
	"swarm/packages/swarmd/internal/provider/google"
	"swarm/packages/swarmd/internal/provider/openai"
	"swarm/packages/swarmd/internal/provider/openrouter"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/remote"
	"swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/uisettings"
	update "swarm/packages/swarmd/internal/update"
	"swarm/packages/swarmd/internal/videogen"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videotranscription"
	"swarm/packages/swarmd/internal/voice"
	"swarm/packages/swarmd/internal/webhook"
	"swarm/packages/swarmd/internal/webpush"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type artifactMetadataBoundary struct {
	*sessionruntime.Service
	mu        sync.RWMutex
	publisher func(sessionruntime.RealtimeOutboxRecord) error
}

func (b *artifactMetadataBoundary) SetPublisher(publisher func(sessionruntime.RealtimeOutboxRecord) error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.publisher = publisher
	b.mu.Unlock()
}

func (b *artifactMetadataBoundary) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	if b == nil || b.Service == nil {
		return pebblestore.V3SessionMutationResult{}, errors.New("artifact metadata boundary is not configured")
	}
	result, err := b.Service.ApplySessionMutation(input)
	if err != nil || result.Replayed {
		return result, err
	}
	b.mu.RLock()
	publisher := b.publisher
	b.mu.RUnlock()
	if publisher == nil {
		return result, errors.New("artifact realtime publisher is not configured")
	}
	outboxes := result.RealtimeOutboxes
	if len(outboxes) == 0 && result.RealtimeOutbox != nil {
		outboxes = []pebblestore.V3RealtimeOutboxRecord{*result.RealtimeOutbox}
	}
	if len(outboxes) == 0 {
		return result, errors.New("committed artifact mutation is missing durable realtime outbox record")
	}
	for _, outbox := range outboxes {
		if outbox.EndpointSeq == 0 {
			continue
		}
		if publishErr := publisher(outbox); publishErr != nil {
			log.Printf("warning: artifact realtime outbox wake failed after durable commit session=%q endpoint_seq=%d: %v", result.SessionID, outbox.EndpointSeq, publishErr)
		}
	}
	return result, nil
}

func newWorkspaceMapService(store *pebblestore.Store) *pebblestore.WorkspaceMapService {
	return pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(store))
}

type Daemon struct {
	automationMu              sync.Mutex
	automationClosed          bool
	automationV2Scheduler     *sessionruntime.AutomationV2Scheduler
	workerExecution           *run.WorkerExecutionService
	worktreeService           *worktreeruntime.Service
	webhookDispatcher         *webhook.Dispatcher
	automationLoop            *automationLoop
	automationExecution       *automation.ExecutionService
	automationApproval        *automation.PolicyApproval
	cfg                       config.Config
	lock                      *lock.FileLock
	store                     *pebblestore.Store
	secretStore               *pebblestore.Store
	events                    *pebblestore.EventLog
	hub                       *stream.Hub
	apiServer                 *api.Server
	notificationService       *notification.Service
	httpServer                *http.Server
	clientServers             []*clientHTTPServer
	localTransportServer      *http.Server
	peerTransportServer       *http.Server
	containerSDKServer        *http.Server
	listener                  net.Listener
	localTransportListener    net.Listener
	peerTransportListener     net.Listener
	serveDone                 chan struct{}
	localTransportServeDone   chan struct{}
	peerTransportServeDone    chan struct{}
	stopCh                    chan string
	stopOnce                  sync.Once
	cleanupOnce               sync.Once
	cleanupErr                error
	longSessionDiagnostics    *longsessiondiag.Recorder
	remoteTransport           *remote.Service
	signalForwarder           *signals.Forwarder
	resultOutbox              *outbox.Exporter
	jobAutomations            *jobautomation.Runner
	bgCtx                     context.Context
	bgCancel                  context.CancelFunc
	memoryDone                <-chan struct{}
	copilot                   *copilot.Manager
	secretGatewayStop         func()
	toolRuntime               *tool.Runtime
	videoRenderService        *videorender.Service
	aiTaskDispatcher          *run.AITaskV2Dispatcher
	deploymentMgr             *lifecycle.DeploymentManager
	localTransportRuntimeName string
	localTransportBaseURL     string
	localTransportSocketPath  string
}

const (
	lingerPollInterval           = 250 * time.Millisecond
	localTransportSocketDirMode  = 0o700
	localTransportSocketFileMode = 0o600
	shutdownTimeout              = 5 * time.Second
)

func localTransportSocketDirPerm() os.FileMode {
	return localTransportSocketDirMode
}

func localTransportSocketPerm() os.FileMode {
	return localTransportSocketFileMode
}

func removeStaleUnixSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace unexpected local transport path %q with mode %s", path, info.Mode())
	}
	return os.Remove(path)
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is not a directory", path)
	}
	return os.Chmod(path, 0o700)
}

func htmlCaptureConcurrency() int {
	if goruntime.NumCPU() >= 8 {
		return 2
	}
	return 1
}

func newDaemon(cfg config.Config, headless bool, configureClient func(*api.Server, *pebblestore.Store)) (*Daemon, error) {
	if err := ensurePrivateDirectory(cfg.DataDir); err != nil {
		return nil, fmt.Errorf("secure data directory: %w", err)
	}
	if err := ensurePrivateDirectory(filepath.Dir(cfg.LockPath)); err != nil {
		return nil, fmt.Errorf("secure lock parent directory: %w", err)
	}

	lk, err := lock.Acquire(cfg.LockPath, lock.Metadata{
		PID:        os.Getpid(),
		ListenAddr: cfg.ListenAddr,
		StartedAt:  time.Now().UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, lock.ErrAlreadyRunning) {
			return nil, fmt.Errorf("daemon lock unavailable: %w", err)
		}
		return nil, err
	}

	store, err := pebblestore.Open(cfg.DBPath)
	if err != nil {
		_ = lk.Release()
		return nil, err
	}
	// The wait progress hook is installed with job automations
	// (Server.SetJobAutomations), after the startup wait reconciliation.
	store.AwaitProjectTaskProgress()
	historyContext, cancelHistory := context.WithTimeout(context.Background(), 2*time.Minute)
	historyErr := pebblestore.NewSessionStore(store).CompleteRepositoryHistoryMaintenance(historyContext)
	cancelHistory()
	if historyErr != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("initialize repository history (resumable maintenance): %w", historyErr)
	}
	if _, err := pebblestore.RunModelProfileFlatMigration(store); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("migrate model profiles to flat favorites: %w", err)
	}
	if _, err := pebblestore.RunAgentModelSettingsMigration(store); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("migrate unified agent model settings: %w", err)
	}
	if _, err := pebblestore.RunAccountUsageRollupBackfillMigration(store); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("backfill account usage rollups: %w", err)
	}
	if _, err := pebblestore.RunWorkerRunIndexMigration(store); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("index worker runs: %w", err)
	}
	secretStore, err := pebblestore.Open(filepath.Join(cfg.DataDir, "swarmd-secrets.pebble"))
	if err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, err
	}

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, err
	}

	hub := stream.NewHub(events)
	authStore := pebblestore.NewAuthStoreWithSecretStore(store, secretStore)
	authSvc := auth.NewService(authStore, events)
	codexClient := codex.NewClient(authStore)
	toolRuntime := tool.NewRuntime(8)
	var htmlRenderer *htmlcapture.ChromedpRenderer
	cacheRoot, cacheRootErr := storagecontract.ResolveRoot(storagecontract.RootCache, storagecontract.Options{})
	if cacheRootErr == nil {
		htmlRenderer = htmlcapture.NewChromedpRendererWithConcurrency(htmlcapture.SystemChromePath, filepath.Join(cacheRoot, "html-capture"), htmlCaptureConcurrency())
		toolRuntime.SetHTMLCaptureRenderer(htmlRenderer)
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureSystemAgentRegistry(); err != nil {
		log.Printf("warning: system agent registry validation failed; Plan/AI sidechats will be unavailable until the daemon binary is corrected: %v", err)
	}
	modelCatalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(store))
	switch catalogMode := strings.TrimSpace(os.Getenv(model.CatalogEnv)); catalogMode {
	case "":
	case model.CatalogEmbedded:
		modelCatalog.UseEmbeddedOnly()
		log.Printf("model catalog: %s=%s, using the snapshot built into this binary; no remote fetch", model.CatalogEnv, catalogMode)
	default:
		return nil, fmt.Errorf("%s=%q: the only accepted value is %q", model.CatalogEnv, catalogMode, model.CatalogEmbedded)
	}
	modelSvc := model.NewServiceWithFavorites(
		pebblestore.NewModelStore(store),
		events,
		modelCatalog,
		pebblestore.NewModelFavoriteStore(store),
	)
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	artifactRegistry := artifact.NewRegistry(sessionSvc, artifact.Limits{})
	// Managed artifacts are part of the normal agent runtime and use native Git
	// as their byte authority. The installer provisions Git on supported hosts;
	// keep startup fail-closed if an installed runtime is incomplete or damaged.
	if err := artifactRegistry.VerifyGitPrerequisite(context.Background()); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("initialize private artifact Git storage: %w", err)
	}
	artifactMetadata := &artifactMetadataBoundary{Service: sessionSvc}
	artifactAuthority := artifact.NewAuthority(artifactRegistry, artifactMetadata)
	// Artifact V2 is an independent write authority. It reuses only the audited
	// private Git repository opener and canonical V3 mutation boundary; it never
	// receives the legacy artifact authority.
	artifactV2Service := artifactv2.NewService(sessionSvc, sessionSvc, sessionSvc, artifactv2.NewGitBlobStore(artifactRegistry))
	if cacheRootErr != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("resolve cache root for Artifact V3: %w", cacheRootErr)
	}
	artifactV3RepositoryRoot, artifactV3WorkspaceRoot, artifactV3EvidenceRoot, err := artifactV3StorageRoots(cfg.DataDir, cacheRoot)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("configure Artifact V3 storage roots: %w", err)
	}
	artifactV3Service, err := pebblestore.NewArtifactV3Service(sessionSvc.Store(), artifactV3RepositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("configure Artifact V3 Git service: %w", err)
	}
	var artifactV3Renderer htmlcapture.Renderer
	if cacheRootErr == nil {
		artifactV3Renderer = htmlcapture.NewChromedpRendererWithConcurrency(htmlcapture.SystemChromePath, filepath.Join(cacheRoot, "html-capture"), htmlCaptureConcurrency())
	}
	artifactV3Runtime := newArtifactV3RuntimeAdapter(artifactV3Service, sessionSvc.Store(), artifactV3RepositoryRoot, artifactV3EvidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3Renderer)
	toolRuntime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(artifactV3WorkspaceRoot, artifactV3Runtime, artifactV3Runtime, artifactV3Runtime))
	toolRuntime.SetArtifactV3NativeImporter(artifactV3Runtime)
	toolRuntime.SetArtifactRegistry(artifactRegistry)
	toolRuntime.SetArtifactAuthority(artifactAuthority)
	mediaStagingSvc := mediastaging.NewService(pebblestore.NewMediaStagingStore(store))
	if err := sessionSvc.EnsureSessionRunStateIndex(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("migrate v3 run-state index: %w", err)
	}
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), events, hub.Publish)
	// Devices are client sessions, kept on the secrets database with the rest
	// of the identity session store (see identitySessionSvc below).
	permissionSvc.SetClientSessionLister(pebblestore.NewIdentitySessionStore(secretStore))
	if cfg.LockPermissionPolicy {
		permissionSvc.LockPolicy()
		log.Printf("swarmd permission policy locked by --lock-permission-policy; bypass is off and policy changes are refused until restart")
	}
	notificationSvc := notification.NewService(pebblestore.NewNotificationStore(store), events, hub.Publish)
	webPushRepository, err := webpush.NewPebbleRepository(secretStore)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("configure web push repository: %w", err)
	}
	webPushSvc, err := webpush.NewService(webPushRepository, "https://github.com/swarm-agent/swarm", nil)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("configure web push service: %w", err)
	}
	if _, err := webPushSvc.PublicKey(context.Background()); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("initialize web push VAPID key: %w", err)
	}
	permissionSvc.SetSessionResolver(sessionSvc)
	permissionSvc.SetLocalSwarmIDResolver(func() string {
		localNode, ok, err := swarmStore.GetLocalNode()
		if err != nil || !ok {
			return ""
		}
		return strings.TrimSpace(localNode.SwarmID)
	})
	permissionSvc.SetRetainToolOutputHistory(cfg.RetainToolOutputHistory)
	notificationSvc.SetLocalSwarmIDResolver(func() string {
		localNode, ok, err := swarmStore.GetLocalNode()
		if err != nil || !ok {
			return ""
		}
		return strings.TrimSpace(localNode.SwarmID)
	})
	permissionSvc.SetNotificationService(notificationSvc)
	// Task status transitions (orchestrated tasks and worker-run tasks) notify the person.
	store.SetProjectTaskStatusObserver(notificationSvc.ObserveProjectTaskStatus)
	discoverySvc := discovery.NewService()
	swarmSvc := swarmruntime.NewService(swarmStore, events, hub.Publish)
	swarmSvc.SetSecretStore(secretStore)
	if err := swarmSvc.EnsureActivationCredentialPending(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("prepare activation credential recovery: %w", err)
	}
	// The local swarm ID is daemon identity, not onboarding state. Mint and
	// durably store it before topology or any V3 session path can resolve self.
	if _, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{}); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("ensure canonical local swarm identity: %w", err)
	}
	// The machine signal feed: high-level events monitors and forwarders follow.
	signalStore := pebblestore.NewSignalStore(store)
	store.SetLimitSignalStore(signalStore)
	signalEmitter := signals.NewEmitter(signalStore)
	permissionSvc.SetSignalEmitter(signalEmitter)
	store.SetWorkerRunObserver(workerRunSignal(signalEmitter, pebblestore.NewSessionStore(store)))
	store.SetJobReviewObserver(jobReviewSignal(signalEmitter))
	// A design job the restart marks failed ends like any other: its
	// design.job.failed signal feeds the activity feed, outbox and automations.
	store.SetDesignJobEndObserver(func(task pebblestore.ProjectTaskRecord) {
		if sig, ok := api.DesignJobEndSignal(task); ok {
			if _, err := signalStore.AppendOnce(sig); err != nil {
				log.Printf("swarmd signal %s not recorded: %v", sig.Kind, err)
			}
		}
	})
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	sandboxMgr, err := newSandboxManager(context.Background(), cfg, workspaceStore, store)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("agent sandbox: %w", err)
	}
	sandbox.SetDefault(sandboxMgr)
	leaseMgr, leaseOff := newLeaseManager(context.Background(), cfg, workspaceStore, store)
	if leaseMgr == nil {
		log.Printf("swarmd environment leases unavailable (%s)", leaseOff)
	}
	sandbox.SetLeases(leaseMgr)
	emitWorkerRun := workerRunSignal(signalEmitter, pebblestore.NewSessionStore(store))
	store.SetWorkerRunObserver(func(w pebblestore.WorkerRecord, r pebblestore.WorkerRunRecord) {
		if snap, ok, err := pebblestore.NewSessionStore(store).GetSession(r.SessionID); err == nil && ok {
			if err := sandboxMgr.EndJob(context.Background(), snap.WorktreeRootPath, func() (bool, error) {
				current, found, err := pebblestore.NewWorkerStore(store).GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
				if err != nil || !found {
					return false, err
				}
				if !pebblestore.AutomationV2Terminal(current.Status) || current.FollowupActive() {
					return false, nil
				}
				_, active, err := pebblestore.NewSessionStore(store).GetV3SessionActiveRunIntent(r.SessionID)
				return !active, err
			}); err != nil && !errors.Is(err, sandbox.ErrCleanupDeferred) {
				log.Printf("cleanup terminal worker sandbox: %v", err)
				if saveErr := pebblestore.NewWorkerStore(store).SetSandboxCleanupError(r, err.Error()); saveErr != nil {
					log.Printf("record sandbox cleanup: %v", saveErr)
				}
			}
		}
		if r.Role == pebblestore.WorkerRoleReviewer && pebblestore.AutomationV2Terminal(r.Status) {
			// Container cleanup is retried by the manager's background sweep.
		}
		emitWorkerRun(w, r)
	})
	provider.BuildSourceGitRouter = sandbox.RoutedArgv
	permissionSvc.SetBypassGate(func() (bool, string) {
		// Headless runs unattended: the owner's bypass setting applies with or
		// without a sandbox, and worker jobs never wait on a prompt.
		if headless {
			return true, ""
		}
		status := sandboxMgr.Status()
		return status.Active, status.Reason
	})
	if status := sandboxMgr.Status(); status.Active {
		runtimeName := status.Runtime
		if runtimeName == "" {
			runtimeName = "engine default runtime"
		}
		log.Printf("swarmd agent sandbox active (%s); agent commands and Git on projects use the configured isolation backend", runtimeName)
	} else if status.Mode == sandbox.ModeRequired {
		log.Printf("swarmd agent execution blocked: required sandbox unavailable (%s); inspect the readiness reason and repair the configured isolation backend, then restart; setup and diagnostics remain available", status.Reason)
	} else if headless {
		log.Printf("swarmd agent sandbox off (%s); agents run on this machine", status.Reason)
	} else {
		log.Printf("swarmd agent sandbox inactive (%s); agent commands run on this machine and permission bypass is disabled", status.Reason)
	}
	emitStartupSignals(signalEmitter, sandboxMgr.Status(), headless)
	if err := reconcileWorkerSandboxes(context.Background(), sandboxMgr, store); err != nil {
		sandboxMgr.Close()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("recover worker sandboxes: %w", err)
	}
	sandboxMgr.SetTerminalCleanup(func(ctx context.Context) { cleanupTerminalSandboxes(ctx, sandboxMgr, store) })
	sandboxMgr.Start(context.Background())
	workspaceSvc := workspace.NewService(workspaceStore)
	workspaceSvc.SetEventPublisher(events, hub.Publish)
	workspaceSvc.SetProtectedRoots(cfg.DataDir, cacheRoot)
	identityStore := pebblestore.NewIdentityStore(store)
	identitySvc := identityruntime.NewService(identityStore)
	identitySessionSvc := identityruntime.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(secretStore))
	agentSvc.SetEventPublisher(hub.Publish)
	authSvc.SetEventPublisher(hub.Publish)
	modelSvc.SetEventPublisher(hub.Publish)
	topologySvc := topologyruntime.NewService(topologyStore, swarmStore)
	worktreeSvc := worktreeruntime.NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, events)
	mcpSvc := mcpruntime.NewService(pebblestore.NewMCPStore(store), events)
	securitySvc := security.NewService(pebblestore.NewClientAuthStoreWithSecretStore(store, secretStore), events)
	securitySvc.SetSignalEmitter(signalEmitter)
	voiceSvc := voice.NewService(
		pebblestore.NewVoiceStore(store),
		voice.NewWhisperLocalAdapter(),
	)
	uiSettingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	agentModelSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentModelSettingsSvc := agentmodelsettings.NewService(agentModelSettingsStore)
	uiSettingsSvc.SetEventPublisher(events, hub.Publish)
	planLifecycleSvc := sessionruntime.NewPlanLifecycleService(sessionSvc)
	planLifecycleSvc.SetApplySessionMutation(sessionSvc.ApplySessionMutation)
	planLifecycleSvc.SetGlobalFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) {
		settings, err := uiSettingsSvc.GetForAccount(accountScopeID)
		if err != nil {
			return "", err
		}
		return settings.Chat.FollowupCheckpointPolicyDefault, nil
	})
	swarmDesktopTargetSelectionStore := pebblestore.NewSwarmDesktopTargetSelectionStore(store)
	todoSvc := todo.NewService(pebblestore.NewWorkspaceTodoStore(store), events, hub.Publish, sessionSvc)
	actionSvc := actionruntime.NewService(pebblestore.NewWorkspaceActionStore(store))
	integrationSvc := integrationruntime.NewService(pebblestore.NewIntegrationStore(store))
	startupCfg, startupCfgErr := startupconfig.Load(cfg.ConfigPath)
	if startupCfgErr != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("load startup config: %w", startupCfgErr)
	}
	if startupCfg.V3Diagnostics {
		if err := os.Setenv("SWARM_V3_DIAGNOSTICS", "1"); err != nil {
			return nil, fmt.Errorf("enable v3 diagnostics: %w", err)
		}
	} else {
		if err := os.Setenv("SWARM_V3_DIAGNOSTICS", "0"); err != nil {
			return nil, fmt.Errorf("disable v3 diagnostics: %w", err)
		}
	}
	if err := os.Setenv(providerdiagnostics.EnvName, providerdiagnostics.BoolEnvValue(startupCfg.ProviderAPIDiagnostics)); err != nil {
		return nil, fmt.Errorf("configure provider api diagnostics: %w", err)
	}
	// A person's folder is shared with core.sharedRepository=group, whose
	// setgid directories the daemon's RestrictSUIDSGID refuses.
	if err := gitenv.UseUmaskSharing(); err != nil {
		return nil, fmt.Errorf("configure git sharing: %w", err)
	}
	updateSvc := update.NewService(strings.TrimSpace(os.Getenv("SWARM_LANE")), startupCfg.DevMode)
	if err := seedUISwarmName(cfg.ConfigPath, uiSettingsSvc); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed ui swarm name: %w", err)
	}
	toolRuntime.SetManageWorktreeServices(sessionSvc, workspaceSvc, worktreeSvc)
	// Agent persistence remains wired for session selection and orchestration, but
	// the AI-facing manage-agent tool is disabled for the MVP compiled crew.
	toolRuntime.SetManageAgentService(agentSvc)
	toolRuntime.SetManageOrchestrationPolicyService(permissionSvc)
	toolRuntime.SetManageTodoService(todoSvc)
	toolRuntime.SetManageActionService(actionSvc)
	toolRuntime.SetManageProjectStore(sessionSvc.Store())
	automationApproval, err := automation.NewPolicyApproval(store, sessionSvc.Store(), automationAccess{workspaces: workspaceSvc, sessions: sessionSvc, members: pebblestore.NewIdentityStore(store)}, automation.RuntimeApprovalIdentity(), time.Now)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("compose automation approval: %w", err)
	}
	automationSvc, err := composeConversationAutomation(store, sessionSvc, automationApproval, time.Now)
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("compose automations: %w", err)
	}
	// V1 tool creation/execution is retired; existing records remain readable.
	toolRuntime.SetManageThemeServices(uiSettingsSvc, workspaceSvc)
	connStore := pebblestore.NewConnectionStore(store)
	envStore := pebblestore.NewEnvironmentStore(store)
	depStore := pebblestore.NewDeploymentStore(store)
	providerReg := provider.NewRegistry()
	providerReg.Register(provider.NewLocalDockerProvider(nil))
	podmanProvider := provider.NewLocalPodmanProvider(nil)
	podmanProvider.ConfigureBuildRoot(filepath.Join(cacheRoot, "environment-builds"))
	providerReg.Register(podmanProvider)
	providerReg.Register(provider.NewSSHDockerProvider(nil))
	deploymentMgr := lifecycle.NewDeploymentManager(connStore, envStore, depStore, workspaceStore, providerReg)
	toolRuntime.SetEnvironmentServices(connStore, envStore, deploymentMgr, workspaceStore, providerReg)
	videoTranscriptionSvc := videotranscription.NewService(sessionSvc.Store(), modelSvc, uiSettingsSvc, google.NewVideoTranscriptionAdapter(authStore))
	videoProjectSvc := videoproject.NewService(sessionSvc.Store())
	videoProjectSvc.SetArtifactV2Authority(artifactV2Service)
	var videoAnimationRenderer htmlcapture.AnimationRenderer
	if cacheRootErr == nil {
		videoAnimationRenderer = htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, filepath.Join(cacheRoot, "html-capture"))
	}
	toolRuntime.SetArtifactV2VideoConversionService(artifactv2.NewVideoConversionService(artifactV2Service, videoProjectSvc, artifactv2.TrustedMotionRenderer{Renderer: videoAnimationRenderer}))
	artifactV3Derivatives, err := newArtifactV3DerivativeStore(filepath.Join(cfg.DataDir, filepath.FromSlash(artifactV3VideoDerivativeDir)))
	if err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("configure Artifact V3 video derivative storage: %w", err)
	}
	artifactV3VideoSvc := artifactv3video.New(artifactV3Runtime, artifactV3AnimationRenderer{renderer: videoAnimationRenderer}, artifactV3Derivatives)
	artifactV3VideoBridge := &artifactV3VideoBridge{artifacts: artifactV3Runtime, service: artifactV3VideoSvc, projects: videoProjectSvc}
	videoProjectSvc.SetArtifactV3Authority(artifactV3VideoSvc)
	toolRuntime.SetArtifactV3VideoConversionService(artifactV3VideoBridge)
	videoRenderSvc := videorender.NewService(
		videorender.Config{},
		sessionSvc.Store(),
		artifactAuthority,
		videoAnimationRenderer,
		workspaceSvc,
		nil,
	)
	videoRenderSvc.SetArtifactV2Authority(artifactV2Service)
	videoRenderSvc.SetArtifactV3Authority(artifactV3VideoSvc)
	toolRuntime.SetManageVideoPipelineServices(
		videoTranscriptionSvc,
		videosource.NewService(workspaceSvc, sessionSvc.Store()),
		videoProjectSvc,
		videoRenderSvc,
	)
	toolRuntime.SetExaConfigResolver(func(ctx context.Context) (tool.ExaRuntimeConfig, error) {
		cfg := tool.ExaRuntimeConfig{
			SearchURL:   "https://api.exa.ai/search",
			ContentsURL: "https://api.exa.ai/contents",
		}
		principal, principalOK := identityruntime.PrincipalFromContext(ctx)
		if !principalOK {
			return tool.ExaRuntimeConfig{}, identityruntime.ErrPrincipalRequired
		}
		record, ok, err := authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "exa")
		if err != nil {
			return tool.ExaRuntimeConfig{}, err
		}
		if ok {
			cfg.APIKey = strings.TrimSpace(record.APIKey)
		}
		if cfg.APIKey != "" {
			cfg.Enabled = true
			cfg.Source = "api_key"
		}
		return cfg, nil
	})
	// Copilot provider code is retained, but it is intentionally not registered
	// as a selectable/runnable provider right now. We cannot fairly validate the
	// sidecar flow while the required paid Copilot plan is unavailable, so keep
	// the implementation dormant until it can be tested end-to-end.
	var copilotManager *copilot.Manager
	// Claude Code drives the person's own locally signed-in, unmodified CLI;
	// Swarm never handles Claude credentials (see provider/claudecode).
	claudeCodeCLI := claudecode.NewCLI()
	// Vertex AI runs Gemini with the machine's own Google identity (attached
	// service account or GOOGLE_APPLICATION_CREDENTIALS); no key is stored.
	vertexCredentials := google.NewApplicationDefaultCredentials()
	providers := registry.New(
		anthropic.NewAdapter(authStore),
		claudecode.NewAdapter(authStore, claudeCodeCLI),
		codex.NewAdapter(authStore),
		fireworks.NewAdapter(authStore),
		google.NewAdapter(authStore),
		openai.NewAdapter(authStore),
		openrouter.NewAdapter(authStore),
		google.NewVertexAdapter(authStore, vertexCredentials),
		exaprovider.NewAdapter(authStore),
	)
	providers.RegisterRunner(anthropic.NewRunner(authStore))
	providers.RegisterRunner(claudecode.NewRunner(authStore, claudeCodeCLI))
	// Copilot runner registration is disabled with the adapter above; leave the
	// code in place for re-enablement after paid-plan validation is possible.
	providers.RegisterRunner(codex.NewRunner(codexClient))
	providers.RegisterRunner(fireworks.NewRunner(authStore))
	providers.RegisterRunner(google.NewRunner(authStore))
	providers.RegisterRunner(openai.NewRunner(authStore, codexClient))
	providers.RegisterRunner(openrouter.NewRunner(authStore))
	providers.RegisterRunner(google.NewVertexRunner(authStore, vertexCredentials))
	modelProfileStore := pebblestore.NewModelProfileStore(store)
	modelProfileSvc := modelprofile.NewService(modelProfileStore)
	runSvc := run.NewService(sessionSvc, modelSvc, providers, toolRuntime, permissionSvc, agentSvc, discoverySvc, events)
	runSvc.SetModelProfileService(modelProfileSvc)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetWorkspaceMapService(newWorkspaceMapService(store))
	runSvc.SetUISettingsService(uiSettingsSvc)
	runSvc.SetAgentModelSettingsService(agentModelSettingsSvc)
	runSvc.SetWorktreeService(worktreeSvc)
	runSvc.SetEnvironmentServices(connStore, envStore, deploymentMgr, workspaceStore)
	runSvc.SetEventPublisher(hub.Publish)

	if err := agentSvc.EnsureDefaults(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed default agents: %w", err)
	}
	if err := modelSvc.EnsureBootDefaults(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("load default model stack: %w", err)
	}
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("ensure attach auth token: %w", err)
	}
	if err := mcpSvc.EnsureDefaults(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed mcp defaults: %w", err)
	}
	if _, err := topologySvc.EnsureSnapshot(); err != nil {
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed topology snapshot: %w", err)
	}
	if err := runSvc.ReconcileActiveLifecycles("daemon restarted"); err != nil {
		log.Printf("warning: reconcile active session lifecycles: %v", err)
	}
	if marked, err := sessionSvc.Store().ReconcileInterruptedDesignJobs(time.Now().UnixMilli()); err != nil {
		log.Printf("warning: reconcile interrupted design jobs: %v", err)
	} else if marked > 0 {
		log.Printf("marked %d design job(s) interrupted by the restart as failed", marked)
	}
	if err := pebblestore.NewWorkerStore(store).RecoverClearedWorkerFollowups(); err != nil {
		return nil, fmt.Errorf("recover follow-up outcomes: %w", err)
	}
	if err := permissionSvc.ReconcilePendingRuns("daemon restarted"); err != nil {
		log.Printf("warning: reconcile pending permissions: %v", err)
	}
	if err := permissionSvc.RepairSummaryPendingIndex("", ""); err != nil {
		log.Printf("warning: repair permission summary pending index: %v", err)
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	notificationSvc.SetWebPushDispatcher(func(_ context.Context, accountScopeID string, record pebblestore.NotificationRecord) error {
		_, err := webPushSvc.Send(bgCtx, accountScopeID, webpush.Payload{Title: record.Title, Body: record.Body, URL: record.ActionURL, Tag: record.ID}, webpush.SendOptions{})
		return err
	})
	modelSvc.StartCatalogAutoRefresh(bgCtx)
	memorySvc := memory.NewService(pebblestore.NewMemoryStore(store), &memory.RuntimeProvider{Runners: providers, Catalog: pebblestore.NewModelCatalogStore(store), Sessions: sessionSvc.Store()})
	memoryDone := make(chan struct{})
	runSvc.SetMemoryStore(memorySvc.Store)
	apiServer := api.NewCoreServer(authSvc, agentSvc, modelSvc, runSvc, sessionSvc, workspaceSvc, discoverySvc, securitySvc, providers, permissionSvc, notificationSvc, events, hub)
	if configureClient != nil {
		configureClient(apiServer, store)
	}
	toolRuntime.SetProjectTaskLifecycleService(apiServer)
	toolRuntime.SetCustomAgentLifecycleService(apiServer)
	runSvc.SetWorkflowLifecycleService(apiServer)
	// Automation mutations always commit through canonical V3 authority before
	// waking realtime. Wake failures cannot turn a committed execution into retry.
	apiServer.ConfigureAutomationRealtime(store)
	apiServer.ConfigureEnvironmentRealtime(store)
	apiServer.ConfigureProjectRealtime(store)
	// Keep the legacy catalog read-only. Do not install V1 approval, dispatch,
	// tool execution, or run-context authorities alongside plan-native V2.
	apiServer.ConfigureAutomations(automationSvc, nil, nil, nil)

	apiServer.SetMemoryService(memorySvc)
	apiServer.SetEnvironmentServices(connStore, envStore, deploymentMgr, workspaceStore, providerReg)
	// Folders' sandbox environments: the sandbox runs each folder on its last
	// ready environment image; a build a restart cut short is marked failed.
	sandboxEnvironments := pebblestore.NewSandboxEnvironmentStore(store)
	if err := sandboxEnvironments.InterruptBuilds(); err != nil {
		log.Printf("warning: sandbox environments: %v", err)
	}
	sandboxMgr.SetEnvironmentResolver(sandboxEnvironmentResolver{store: sandboxEnvironments})
	if leaseMgr != nil {
		leaseMgr.SetEnvironmentResolver(sandboxEnvironmentResolver{store: sandboxEnvironments})
		leaseMgr.SetLeasePolicy(leaseLimit(sandboxEnvironments), leaseOwnerRunning(pebblestore.NewSessionStore(store), toolRuntime.SessionBackgroundCommandsRunning))
	}
	apiServer.SetSandboxEnvironmentStore(sandboxEnvironments)
	secretSlots := pebblestore.NewSecretSlotStore(store)
	apiServer.SetSecretServices(secretSlots, authStore)
	apiServer.SetFleet(pebblestore.NewFleetStore(store), authStore)
	apiServer.SetSignalStore(signalStore)
	apiServer.SetSecretsGatewayEnabled(cfg.SecretsGateway == "on")
	signalSinks := pebblestore.NewSignalSinkStore(store)
	apiServer.SetSignalSinkStore(signalSinks)
	// Forwards the signal feed to owner-configured sinks; idle until one exists.
	signalForwarder := signals.NewForwarder(signalStore, signalSinks, func() string { return startupSwarmName(cfg.ConfigPath) })
	// Exports the feed and finished jobs' results to the owner's storage when
	// SWARM_OUTBOX_URL is set (docs/checkpoints/outbox.md); off otherwise.
	var resultOutbox *outbox.Exporter
	if outboxCfg, on, err := outbox.ConfigFromEnv(os.Getenv, startupSwarmName(cfg.ConfigPath)); err != nil {
		log.Printf("warning: outbox: %v", err)
		apiServer.SetOutboxStatus(func() outbox.Status { return outbox.Status{LastError: err.Error(), Failing: true} })
	} else if on {
		if dest, err := outboxCfg.Open(); err != nil {
			log.Printf("warning: outbox: %v", err)
			apiServer.SetOutboxStatus(func() outbox.Status { return outbox.Status{LastError: err.Error(), Failing: true} })
		} else {
			resultOutbox = outbox.NewExporter(outboxCfg, dest, signalStore, store, apiServer.OutboxAccount, apiServer.OutboxResultBuilder())
			resultOutbox.SetStatusBuilder(apiServer.OutboxStatusBuilder())
			apiServer.SetOutboxStatus(resultOutbox.Status)
			apiServer.SetOutboxDeliveryWriter(resultOutbox.WriteDelivery)
			log.Printf("swarmd outbox exports to %s as machine %s", outboxCfg.Describe(), outboxCfg.Machine)
		}
	}
	// Job automations (docs/automations.md): when a job ends, run the steps
	// the owner set for it, and nothing when there are none.
	jobAutomationRunner := jobautomation.NewRunner(jobautomation.Options{
		Store: jobautomation.NewStore(store), Feed: signalStore, Account: apiServer.OutboxAccount,
		Results: apiServer.JobAutomationResults(), Agent: apiServer.JobAutomationAgents(), Reopen: apiServer.JobAutomationReopener(),
		Head: api.ImportJobRef, Verify: api.VerifyJobHead, Lines: api.JobHeadChangedLines, Emit: signalEmitter.Emit,
		// Deliver steps hand deliverables over; produce steps' files are found.
		Deliver: apiServer.JobAutomationDeliverer(),
		// Run steps are the owner's root-owned scripts, run on the host as the
		// daemon's user, like the publish command.
		Run: jobautomation.HostExecutor{},
	})
	apiServer.SetJobAutomations(jobAutomationRunner)
	// Jobs in folders whose owner set a publish command (swarmctl workspace
	// publish) are delivered as pull requests when swarm_deliver_job asks.
	apiServer.SetPublishConfig(os.Getenv(publish.ConfigEnv))
	apiServer.EmitPublishConfigWarnings(signalEmitter.Emit)
	// A sync rule starts each worker job from the forge's latest base branch.
	runSvc.SetPublishBaseResolver(func(ctx context.Context, folder, current string) (string, string, bool, error) {
		rules, err := publish.Load(os.Getenv(publish.ConfigEnv))
		if err != nil {
			return "", "", false, err
		}
		rule, ok := publish.For(rules, folder)
		if !ok || !rule.Sync {
			return "", "", false, nil
		}
		base, err := publish.Update(ctx, rule, current)
		if err != nil {
			return "", "", false, err
		}
		return base.Commit, base.Branch, true, nil
	})
	var secretGatewayStop func()
	if cfg.SecretsGateway == "on" {
		broker, stopGateway, err := startSecretGateway(cfg, secretSlots, authStore, signalEmitter)
		if err != nil {
			bgCancel()
			_ = secretStore.Close()
			_ = store.Close()
			_ = lk.Release()
			return nil, fmt.Errorf("secret gateway: %w", err)
		}
		sandboxMgr.SetSecretBroker(broker)
		if leaseMgr != nil {
			leaseMgr.SetSecretBroker(broker)
		}
		secretGatewayStop = stopGateway
		log.Printf("swarmd secret gateway enabled; granted secrets inject through the egress gateway without the agent seeing them")
	}
	if err := deploymentMgr.Recover(bgCtx); err != nil {
		log.Printf("warning: environment supervisor recovery: %v", err)
	}
	go deploymentMgr.RunReviewCleanup(bgCtx, func(err error) {
		log.Printf("warning: environment review cleanup: %v", err)
	})
	if leaseMgr != nil {
		leaseMgr.Start(bgCtx)
		leaseMgr.StartLeases(bgCtx)
		log.Printf("swarmd environment leases ready: agents lease their folder's environment for bash, up to its max_leases; leases end with the agent's run")
	}
	apiServer.SetMediaStagingService(mediaStagingSvc)
	apiServer.SetVideoTranscriptionService(videoTranscriptionSvc)
	apiServer.SetVideoProjectService(videoProjectSvc)
	apiServer.SetVideoRenderService(videoRenderSvc)
	apiServer.SetArtifactRegistry(artifactRegistry)
	apiServer.SetArtifactV2Service(artifactV2Service)
	apiServer.SetArtifactV3Service(artifactV3Runtime)
	artifactV3Runtime.publish = apiServer.PublishArtifactV3Projection
	if err := recoverArtifactV3Repositories(context.Background(), artifactV3Runtime); err != nil {
		bgCancel()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("recover Artifact V3 repositories: %w", err)
	}
	if err := migrateArtifactV3VideoReceipts(context.Background(), artifactV3Runtime, artifactV3Derivatives); err != nil {
		bgCancel()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("migrate Artifact V3 video receipts: %w", err)
	}
	runSvc.SetSessionDeployCanonicalizer(apiServer.CanonicalizeSessionDeploy)
	runSvc.SetSessionDeployEnqueuer(apiServer.EnqueueSessionDeployRun)
	automationV2Host, err := run.NewAutomationV2ExecutionHost(runSvc, sessionSvc.Store(), worktreeSvc, sessionSvc.ApplySessionMutation, apiServer.EnqueueAutomationRun)
	if err != nil {
		bgCancel()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("compose automation v2 execution: %w", err)
	}
	workerExecution, err := run.NewWorkerExecutionService(runSvc, sessionSvc.Store(), worktreeSvc, sessionSvc.ApplySessionMutation, apiServer.EnqueueAutomationRun)
	if err != nil {
		bgCancel()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("compose worker execution: %w", err)
	}
	if err := workerExecution.SweepReviewAdmissions(bgCtx); err != nil {
		log.Printf("warning: recover review admissions: %v", err)
	}
	runSvc.SetWorkerExecutionService(workerExecution)
	runSvc.SetAITaskBinder(todoSvc)
	aiTaskDispatcher, err := runSvc.StartAITaskV2Dispatcher(bgCtx, aiTaskQueueAdapter{service: todoSvc}, sessionSvc.ApplySessionMutation)
	if err != nil {
		bgCancel()
		_ = secretStore.Close()
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("start AI task V2 dispatcher: %w", err)
	}
	apiServer.SetAITaskEnqueuer(aiTaskDispatcher)
	toolRuntime.SetManageSessionRealtimePublisher(apiServer.PublishCommittedV3RealtimeOutbox)
	toolRuntime.SetManageSessionController(apiServer)
	// Background bash commands: stopping a run or archiving/deleting its
	// conversation kills them; a run's end re-delivers an undelivered report.
	apiServer.SetBackgroundCommandLifecycle(toolRuntime)
	sessionSvc.SetTombstoneObserver(func(sessionID, kind string) {
		toolRuntime.StopSessionBackgroundCommands(sessionID, "conversation "+kind)
	})
	artifactMetadata.SetPublisher(apiServer.PublishCommittedV3RealtimeOutbox)
	apiServer.SetCodexAccountClient(codexClient)
	apiServer.SetClaudeCodeUsageClient(claudeCodeCLI)
	apiServer.SetWebPushService(webPushSvc)
	apiServer.SetModelProfileService(modelProfileSvc)
	apiServer.SetAgentModelSettingsService(agentModelSettingsSvc, agentModelSettingsStore)
	apiServer.SetIdentityService(identitySvc)
	apiServer.SetIdentitySessionService(identitySessionSvc)
	apiServer.SetBypassPermissions(cfg.BypassPermissions)
	apiServer.SetDataDir(cfg.DataDir)
	apiServer.SetStartupConfigPath(cfg.ConfigPath)
	apiServer.SetWorkspaceRoot(cfg.StartupCWD)
	apiServer.SetWorktreeService(worktreeSvc)
	apiServer.SetMCPService(mcpSvc)
	// Remote transport is off until the owner initializes and enables a relay.
	remoteSvc := remote.NewService(secretStore, remoteTokenIssuer{security: securitySvc, identities: identitySessionSvc})
	remoteSvc.SetControlHandler(apiServer.ContainerSDKHandler())
	apiServer.SetRemoteTransportService(remoteSvc)
	apiServer.SetVoiceService(voiceSvc)
	apiServer.SetUISettingsService(uiSettingsSvc)
	apiServer.SetPlanLifecycleService(planLifecycleSvc)
	apiServer.SetSwarmDesktopTargetSelectionStore(swarmDesktopTargetSelectionStore)
	apiServer.SetVideoThreadStore(pebblestore.NewVideoThreadStore(store))
	imageThreadStore := pebblestore.NewImageThreadStore(store)
	imageGenSvc := imagegen.NewService(codexClient, authStore, imageThreadStore, modelSvc)
	toolRuntime.SetManagedImageGenerationService(imageGenSvc)
	apiServer.SetImageGenerationService(imageGenSvc)
	apiServer.SetImageThreadStore(imageThreadStore)
	videoGenSvc := videogen.NewService(authStore, uiSettingsSvc, modelSvc)
	if htmlRenderer != nil {
		videoGenSvc.SetSVGRasterizer(htmlRenderer)
	}
	toolRuntime.SetManagedVideoGenerationService(videoGenSvc)
	apiServer.SetVideoGenerationService(videoGenSvc)
	audioGenSvc := audiogen.NewService(authStore, uiSettingsSvc, modelSvc)
	toolRuntime.SetManagedAudioGenerationService(audioGenSvc)
	apiServer.SetAudioGenerationService(audioGenSvc)
	apiServer.SetTodoService(todoSvc)
	apiServer.SetActionService(actionSvc)
	apiServer.SetIntegrationService(integrationSvc)
	apiServer.SetSwarmService(swarmSvc)
	apiServer.SetPublicAPIClient(swarmruntime.NewPublicAPIClient(swarmSvc))
	apiServer.SetSwarmStore(swarmStore)
	apiServer.SetUpdateService(updateSvc)
	apiServer.SetTopologyService(topologySvc)

	// Design jobs and in-session swarm_design calls check each design in the
	// sandboxed headless Chrome; without a renderer every check reports
	// unavailable (never a pass).
	designChecker := design.ChromeChecker{}
	if htmlRenderer != nil {
		designChecker.Browser = htmlRenderer
	}
	apiServer.SetDesignJobs(designChecker, runSvc)
	runSvc.SetDesignChecker(designChecker)
	// manage_design was replaced by swarm_design: its queue is no longer
	// dispatched, so requests still pending from before are ended once here.
	if ended, err := sessionSvc.DesignStore().RetirePendingDesignRequests(); err != nil {
		log.Printf("warning: retire pending manage_design requests: %v", err)
	} else if ended > 0 {
		log.Printf("ended %d pending manage_design candidate(s): manage_design was replaced by swarm_design", ended)
	}
	localTransportRuntimeName := ""

	d := &Daemon{
		cfg:                 cfg,
		lock:                lk,
		store:               store,
		secretStore:         secretStore,
		events:              events,
		hub:                 hub,
		apiServer:           apiServer,
		notificationService: notificationSvc,
		bgCtx:               bgCtx,
		bgCancel:            bgCancel,
		// V1 scheduling/approval authorities are intentionally not installed.
		stopCh:                    make(chan string, 1),
		copilot:                   copilotManager,
		toolRuntime:               toolRuntime,
		videoRenderService:        videoRenderSvc,
		aiTaskDispatcher:          aiTaskDispatcher,
		workerExecution:           workerExecution,
		worktreeService:           worktreeSvc,
		deploymentMgr:             deploymentMgr,
		localTransportRuntimeName: localTransportRuntimeName,
		remoteTransport:           remoteSvc,
		signalForwarder:           signalForwarder,
		resultOutbox:              resultOutbox,
		jobAutomations:            jobAutomationRunner,
	}
	d.secretGatewayStop = secretGatewayStop
	apiServer.SetShutdownHandler(func(reason string) {
		d.requestStop("api:" + strings.TrimSpace(reason))
	})
	// The loopback API listener alone also serves the provider key page.
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiServer.LoopbackAPIHandler(),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Keep write timeout disabled so long-lived websocket/API streams are
		// not cut mid-event during multi-step turns.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	d.httpServer = httpServer
	if cfg.ContainerSDKPort > 0 {
		d.containerSDKServer = &http.Server{
			Addr:              net.JoinHostPort(cfg.ContainerSDKHost, strconv.Itoa(cfg.ContainerSDKPort)),
			Handler:           sdkListenerHandler(cfg, apiServer),
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}
	if shouldEnableLocalTransport(cfg.ListenAddr) {
		localTransportSocketPath := filepath.Join(cfg.DataDir, "local-transport", "api.sock")
		if err := os.MkdirAll(filepath.Dir(localTransportSocketPath), localTransportSocketDirPerm()); err != nil {
			return nil, fmt.Errorf("create local transport directory: %w", err)
		}
		d.localTransportSocketPath = localTransportSocketPath
		d.localTransportServer = &http.Server{
			Handler:           apiServer.LocalTransportHandler(),
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       60 * time.Second,
		}
	}
	transportMux := http.NewServeMux()
	transportMux.Handle("/", apiServer.Handler())
	d.peerTransportServer = &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.PeerTransportPort)),
		Handler:           transportMux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}

	diagnostics, err := longsessiondiag.Start(longsessiondiag.Options{
		Enabled:      cfg.LongSessionDiagnostics,
		DatabasePath: cfg.DBPath,
	})
	if err != nil {
		_ = d.cleanup()
		return nil, fmt.Errorf("start long-session diagnostics: %w", err)
	}
	webhookDispatcher := webhook.NewDispatcher(nil)
	d.webhookDispatcher = webhookDispatcher
	d.automationV2Scheduler = sessionruntime.NewAutomationV2Scheduler(sessionSvc, automationV2Host)
	d.automationV2Scheduler.SetWebhookDispatcher(webhookDispatcher)
	apiServer.SetAutomationV2Scheduler(d.automationV2Scheduler)
	apiServer.SetWebhookDispatcher(webhookDispatcher)
	d.longSessionDiagnostics = diagnostics
	if diagnostics != nil {
		codexClient.SetLongSessionDiagnostics(diagnostics)
		apiServer.SetLongSessionDiagnostics(diagnostics)
		diagnostics.RegisterSnapshotProvider("codex", codexClient.LongSessionSnapshot)
		diagnostics.RegisterSnapshotProvider("run_service", runSvc.LongSessionSnapshot)
		diagnostics.RegisterSnapshotProvider("api", apiServer.LongSessionSnapshot)
		diagnostics.RegisterSnapshotProvider("tools", toolRuntime.LongSessionSnapshot)
		log.Printf("long-session diagnostics enabled directory=%q", diagnostics.Directory())
	}
	// Start store-backed background work only after every constructor step that
	// can close the stores has succeeded. Otherwise a startup error can race an
	// eager maintenance pass and panic on an already-closed Pebble database.
	startV3SessionRetention(bgCtx, sessionSvc)
	startAgentSessionRetention(bgCtx, apiServer)
	startMediaStagingCleanup(bgCtx, mediaStagingSvc)
	startKeyExpiryWarnings(bgCtx, securitySvc)
	startArtifactMaintenance(bgCtx, artifactRegistry)
	startVideoRenderRecovery(bgCtx, videoRenderSvc)
	startMintReport(bgCtx, swarmSvc)
	d.memoryDone = memoryDone
	go func() {
		defer close(memoryDone)
		memorySvc.RunScheduler(bgCtx, pebblestore.NewIdentityStore(store), func(error) { log.Print("memory scheduler operation failed; inspect memory job status") })
	}()
	return d, nil
}

func seedUISwarmName(configPath string, uiSettingsSvc *uisettings.Service) error {
	if uiSettingsSvc == nil {
		return fmt.Errorf("ui settings service not configured")
	}
	path := strings.TrimSpace(configPath)
	if path == "" {
		resolved, err := startupconfig.ResolvePath()
		if err != nil {
			return err
		}
		path = resolved
	}
	startupCfg, err := startupconfig.Load(path)
	if err != nil {
		return err
	}
	startupName := strings.TrimSpace(startupCfg.SwarmName)
	if startupName == "" {
		return nil
	}
	settings, err := uiSettingsSvc.Get()
	if err != nil {
		return err
	}
	currentName := strings.TrimSpace(settings.Swarm.Name)
	if currentName != "" && !strings.EqualFold(currentName, "Local") {
		return nil
	}
	settings.Swarm.Name = startupName
	_, err = uiSettingsSvc.Set(settings)
	return err
}

func (d *Daemon) Close() error {
	return d.cleanup()
}

func (d *Daemon) cleanup() error {
	if d == nil {
		return nil
	}

	d.cleanupOnce.Do(func() {
		var errs []error
		d.automationMu.Lock()
		d.automationClosed = true
		if d.automationLoop != nil {
			d.automationLoop.Close()
			d.automationLoop = nil
		}
		d.automationMu.Unlock()
		if d.bgCancel != nil {
			d.bgCancel()
			d.bgCancel = nil
		}
		if d.secretGatewayStop != nil {
			d.secretGatewayStop()
			d.secretGatewayStop = nil
		}
		if d.memoryDone != nil {
			<-d.memoryDone
			d.memoryDone = nil
		}
		if d.longSessionDiagnostics != nil {
			if err := d.longSessionDiagnostics.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close long-session diagnostics: %w", err))
			}
			d.longSessionDiagnostics = nil
		}
		if d.videoRenderService != nil {
			waitCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			_ = d.videoRenderService.WaitForIdle(waitCtx)
			cancel()
			d.videoRenderService = nil
		}
		if d.deploymentMgr != nil {
			if err := d.deploymentMgr.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close deployment manager: %w", err))
			}
			d.deploymentMgr = nil
		}
		if d.aiTaskDispatcher != nil {
			d.aiTaskDispatcher.Close()
			d.aiTaskDispatcher = nil
		}
		if d.webhookDispatcher != nil {
			d.webhookDispatcher.Close()
			d.webhookDispatcher = nil
		}
		if d.toolRuntime != nil {
			if err := d.toolRuntime.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close tool runtime: %w", err))
			}
			d.toolRuntime = nil
		}
		if d.notificationService != nil {
			d.notificationService.CloseWebPushDispatcher()
			d.notificationService = nil
		}
		if d.copilot != nil {
			if err := d.copilot.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close copilot manager: %w", err))
			}
			d.copilot = nil
		}
		if d.store != nil {
			if d.workerExecution != nil {
				d.workerExecution.CloseCleanup()
			}
			if d.worktreeService != nil {
				d.worktreeService.Close()
			}
			if err := d.store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close store: %w", err))
			}
			d.store = nil
		}
		if d.secretStore != nil {
			if err := d.secretStore.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close secret store: %w", err))
			}
			d.secretStore = nil
		}
		if d.lock != nil {
			if err := d.lock.Release(); err != nil {
				errs = append(errs, fmt.Errorf("release lock: %w", err))
			}
			d.lock = nil
		}
		d.cleanupErr = errors.Join(errs...)
	})

	return d.cleanupErr
}

func (d *Daemon) requestStop(reason string) {
	if d == nil || d.stopCh == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "requested"
	}
	d.stopOnce.Do(func() {
		d.stopCh <- reason
	})
}

func (d *Daemon) hasLifecycleActivity() bool {
	if d == nil {
		return false
	}
	if d.copilot != nil && d.copilot.HasActiveSession() {
		return true
	}
	if d.hub != nil && d.hub.HasClients() {
		return true
	}
	return false
}

func (d *Daemon) Run() error {
	ln, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return err
	}
	d.listener = ln
	d.httpServer.Addr = ln.Addr().String()
	if d.apiServer != nil {
		d.apiServer.SetProviderKeyPageAddr(d.httpServer.Addr)
		// The fleet: listen for the homes this Swarm invited, if it did.
		if err := d.apiServer.StartFleetListener(); err != nil {
			log.Printf("swarmd fleet: %v", err)
		}
		// Linked task waits from before a restart resume only now, after
		// every setter has run (watchers read the node key and fleet links).
		d.apiServer.ResumeLinkedTaskWaits()
	}
	log.Printf("swarmd listener topology api_listen=%q", d.httpServer.Addr)

	d.serveDone = make(chan struct{})
	go func() {
		defer close(d.serveDone)
		if err := d.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("swarmd http serve error: %v", err)
			d.requestStop("http-serve-error")
		}
	}()
	for _, client := range d.clientServers {
		if err := d.serveClient(client); err != nil {
			_ = ln.Close()
			for _, started := range d.clientServers {
				_ = started.server.Close()
			}
			return err
		}
	}
	if d.localTransportServer != nil {
		socketPath := strings.TrimSpace(d.localTransportSocketPath)
		if socketPath == "" {
			return fmt.Errorf("local transport socket path is not configured")
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), localTransportSocketDirPerm()); err != nil {
			return fmt.Errorf("create local transport directory: %w", err)
		}
		if err := removeStaleUnixSocket(socketPath); err != nil {
			return fmt.Errorf("prepare local transport socket %q: %w", socketPath, err)
		}
		localTransportLn, err := net.Listen("unix", socketPath)
		if err != nil {
			return fmt.Errorf("listen on local transport socket %q: %w", socketPath, err)
		}
		// Local transport is trusted daemon-owner IPC. Keep both the directory
		// and socket inaccessible to other users on the host.
		if err := os.Chmod(filepath.Dir(socketPath), localTransportSocketDirPerm()); err != nil {
			_ = localTransportLn.Close()
			_ = os.Remove(socketPath)
			return fmt.Errorf("chmod local transport directory %q: %w", filepath.Dir(socketPath), err)
		}
		if err := os.Chmod(socketPath, localTransportSocketPerm()); err != nil {
			_ = localTransportLn.Close()
			_ = os.Remove(socketPath)
			return fmt.Errorf("chmod local transport socket %q: %w", socketPath, err)
		}
		d.localTransportListener = localTransportLn
		log.Printf("swarmd listener topology local_transport_socket=%q", socketPath)
		d.localTransportServeDone = make(chan struct{})
		go func() {
			defer close(d.localTransportServeDone)
			if err := d.localTransportServer.Serve(localTransportLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("swarmd local transport serve error: %v", err)
				d.requestStop("local-transport-serve-error")
			}
		}()
	}
	if d.peerTransportServer != nil {
		transportLn, err := net.Listen("tcp", d.peerTransportServer.Addr)
		if err != nil {
			_ = ln.Close()
			return err
		}
		d.peerTransportListener = transportLn
		d.peerTransportServer.Addr = transportLn.Addr().String()
		log.Printf("swarmd listener topology peer_transport_listen=%q", d.peerTransportServer.Addr)
		d.peerTransportServeDone = make(chan struct{})
		go func() {
			defer close(d.peerTransportServeDone)
			if err := d.peerTransportServer.Serve(transportLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("swarmd peer transport serve error: %v", err)
				d.requestStop("peer-transport-serve-error")
			}
		}()
	}
	if d.containerSDKServer != nil {
		sdkLn, err := net.Listen("tcp4", d.containerSDKServer.Addr)
		if err != nil {
			return fmt.Errorf("listen on container SDK port: %w", err)
		}
		defer d.containerSDKServer.Close()
		go func() {
			if err := d.containerSDKServer.Serve(sdkLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				d.requestStop("container-sdk-serve-error")
			}
		}()
	}
	if d.remoteTransport != nil && d.bgCtx != nil {
		go d.remoteTransport.Run(d.bgCtx)
	}
	if d.signalForwarder != nil && d.bgCtx != nil {
		go d.signalForwarder.Run(d.bgCtx)
	}
	if d.resultOutbox != nil && d.bgCtx != nil {
		go d.resultOutbox.Run(d.bgCtx)
	}
	if d.jobAutomations != nil && d.bgCtx != nil {
		go d.jobAutomations.Run(d.bgCtx)
		go d.apiServer.RunWorkflowTriggers(d.bgCtx)
	}
	// Start only V2, after listeners succeed; never migrate or execute V1 records.
	if d.automationV2Scheduler != nil {
		if err := d.StartAutomationV2Scheduling(context.Background()); err != nil {
			return err
		}
	}
	return d.waitForShutdown()
}

var ErrReleaseUpdateRestart = errors.New("release update activated; restart required")

func (d *Daemon) waitForShutdown() error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	reason := ""
	select {
	case sig := <-sigCh:
		reason = sig.String()
	case reason = <-d.stopCh:
	}
	if strings.TrimSpace(reason) == "" {
		reason = "requested"
	}
	var errs []error
	d.automationMu.Lock()
	if d.automationLoop != nil {
		d.automationLoop.Close()
		d.automationLoop = nil
	}
	d.automationClosed = true
	d.automationMu.Unlock()
	if d.apiServer != nil {
		d.apiServer.BeginShutdown()
		d.apiServer.CloseFleetListener()
		d.apiServer.CancelInFlightRuns()
		if !d.apiServer.WaitForInFlightRuns(shutdownTimeout) {
			errs = append(errs, fmt.Errorf("timed out draining %d active run(s)", d.apiServer.ActiveRunCount()))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	servers := []struct {
		name   string
		server *http.Server
	}{
		{name: "api", server: d.httpServer},
		{name: "container SDK", server: d.containerSDKServer},
		{name: "peer transport", server: d.peerTransportServer},
		{name: "local transport", server: d.localTransportServer},
	}
	for _, client := range d.clientServers {
		servers = append(servers, struct {
			name   string
			server *http.Server
		}{client.name, client.server})
	}
	for _, item := range servers {
		if item.server == nil {
			continue
		}
		if err := item.server.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s server: %w", item.name, err))
			if closeErr := item.server.Close(); closeErr != nil {
				errs = append(errs, fmt.Errorf("close %s server: %w", item.name, closeErr))
			}
		}
	}
	if strings.TrimSpace(d.localTransportSocketPath) != "" {
		if err := removeStaleUnixSocket(d.localTransportSocketPath); err != nil {
			errs = append(errs, fmt.Errorf("remove local transport socket: %w", err))
		}
	}
	if err := d.cleanup(); err != nil {
		errs = append(errs, err)
	}
	if strings.EqualFold(strings.TrimSpace(reason), "api:update-release") {
		errs = append(errs, ErrReleaseUpdateRestart)
	}
	return errors.Join(errs...)
}

func shouldEnableLocalTransport(listenAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	return host != ""
}

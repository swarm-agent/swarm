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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/config"
	containerprofiles "swarm/packages/swarmd/internal/containerprofiles"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/discovery"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	"swarm/packages/swarmd/internal/lock"
	mcpruntime "swarm/packages/swarmd/internal/mcp"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/provider/anthropic"
	"swarm/packages/swarmd/internal/provider/codex"
	"swarm/packages/swarmd/internal/provider/copilot"
	exaprovider "swarm/packages/swarmd/internal/provider/exa"
	"swarm/packages/swarmd/internal/provider/fireworks"
	"swarm/packages/swarmd/internal/provider/google"
	"swarm/packages/swarmd/internal/provider/openrouter"
	"swarm/packages/swarmd/internal/provider/registry"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	"swarm/packages/swarmd/internal/run"
	sandboxruntime "swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/todo"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/voice"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type Daemon struct {
	cfg                       config.Config
	lock                      *lock.FileLock
	store                     *pebblestore.Store
	events                    *pebblestore.EventLog
	hub                       *stream.Hub
	apiServer                 *api.Server
	httpServer                *http.Server
	desktopServer             *http.Server
	localTransportServer      *http.Server
	peerTransportServer       *http.Server
	listener                  net.Listener
	desktopListener           net.Listener
	localTransportListener    net.Listener
	peerTransportListener     net.Listener
	serveDone                 chan struct{}
	desktopServeDone          chan struct{}
	localTransportServeDone   chan struct{}
	peerTransportServeDone    chan struct{}
	stopCh                    chan string
	stopOnce                  sync.Once
	cleanupOnce               sync.Once
	cleanupErr                error
	bgCtx                     context.Context
	bgCancel                  context.CancelFunc
	copilot                   *copilot.Manager
	deployContainers          *deployruntime.Service
	remoteDeploys             *remotedeploy.Service
	localContainers           *localcontainers.Service
	localTransportRuntimeName string
	localTransportBaseURL     string
	localTransportSocketPath  string
}

const (
	lingerPollInterval = 250 * time.Millisecond
)

func New(cfg config.Config) (*Daemon, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create lock parent directory: %w", err)
	}

	lk, err := lock.Acquire(cfg.LockPath, lock.Metadata{
		PID:        os.Getpid(),
		Mode:       cfg.Mode,
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

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, err
	}

	hub := stream.NewHub(events)
	authStore := pebblestore.NewAuthStore(store)
	authSvc := auth.NewService(authStore, events)
	codexClient := codex.NewClient(authStore)
	toolRuntime := tool.NewRuntime(8)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	modelCatalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(store))
	modelSvc := model.NewServiceWithFavorites(
		pebblestore.NewModelStore(store),
		events,
		modelCatalog,
		pebblestore.NewModelFavoriteStore(store),
	)
	swarmStore := pebblestore.NewSwarmStore(store)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	sessionSvc.SetHostedSync(sessionruntime.NewHostedSyncClient(cfg.ConfigPath, swarmStore))
	sessionSvc.SetLocalSwarmIDResolver(func() string {
		localNode, ok, err := swarmStore.GetLocalNode()
		if err != nil || !ok {
			return ""
		}
		return strings.TrimSpace(localNode.SwarmID)
	})
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), events, hub.Publish)
	notificationSvc := notification.NewService(pebblestore.NewNotificationStore(store), events, hub.Publish)
	permissionSvc.SetSessionResolver(sessionSvc)
	permissionSvc.SetHostedSync(permission.NewHostedSyncClient(cfg.ConfigPath, swarmStore))
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
	discoverySvc := discovery.NewService()
	swarmSvc := swarmruntime.NewService(swarmStore, events, hub.Publish)
	containerProfileSvc := containerprofiles.NewService(pebblestore.NewSwarmContainerProfileStore(store))
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	workspaceSvc.SetEventPublisher(events, hub.Publish)
	localContainerSvc := localcontainers.NewService(
		pebblestore.NewSwarmLocalContainerStore(store),
		pebblestore.NewDeployContainerStore(store),
		swarmStore,
		authStore,
		workspaceSvc,
		cfg.ConfigPath,
	)
	deployContainerSvc := deployruntime.NewService(pebblestore.NewDeployContainerStore(store), localContainerSvc, swarmSvc, swarmStore, authSvc, agentSvc, workspaceSvc, cfg.ConfigPath)
	remoteDeploySvc := remotedeploy.NewService(pebblestore.NewRemoteDeploySessionStore(store), swarmSvc, swarmStore, localContainerSvc, authSvc, workspaceSvc, cfg.ConfigPath, cfg.StartupCWD)
	sandboxSvc := sandboxruntime.NewService(pebblestore.NewSandboxStore(store), events)
	worktreeSvc := worktreeruntime.NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, events)
	mcpSvc := mcpruntime.NewService(pebblestore.NewMCPStore(store), events)
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), events)
	voiceSvc := voice.NewService(
		pebblestore.NewVoiceStore(store),
		voice.NewWhisperLocalAdapter(),
	)
	uiSettingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	uiSettingsSvc.SetEventPublisher(events, hub.Publish)
	swarmDesktopTargetSelectionStore := pebblestore.NewSwarmDesktopTargetSelectionStore(store)
	todoSvc := todo.NewService(pebblestore.NewWorkspaceTodoStore(store), events, hub.Publish)
	if err := seedUISwarmName(cfg.ConfigPath, uiSettingsSvc); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed ui swarm name: %w", err)
	}
	toolRuntime.SetManageWorktreeServices(sessionSvc, workspaceSvc, worktreeSvc)
	toolRuntime.SetManageAgentService(agentSvc)
	toolRuntime.SetManageTodoService(todoSvc)
	toolRuntime.SetManageThemeServices(uiSettingsSvc, workspaceSvc)
	toolRuntime.SetExaConfigResolver(func(context.Context) (tool.ExaRuntimeConfig, error) {
		mcpConfig, err := mcpSvc.ResolveExaRuntimeConfig()
		if err != nil {
			return tool.ExaRuntimeConfig{}, err
		}
		cfg := tool.ExaRuntimeConfig{
			SearchURL:   "https://api.exa.ai/search",
			ContentsURL: "https://api.exa.ai/contents",
			MCPURL:      strings.TrimSpace(mcpConfig.URL),
		}
		record, ok, err := authStore.GetActiveCredential("exa")
		if err != nil {
			return tool.ExaRuntimeConfig{}, err
		}
		if ok {
			cfg.APIKey = strings.TrimSpace(record.APIKey)
		}
		if cfg.APIKey == "" {
			cfg.APIKey = strings.TrimSpace(os.Getenv("EXA_API_KEY"))
		}
		if cfg.APIKey != "" {
			cfg.Enabled = true
			cfg.Source = "api_key"
			if mcpConfig.Enabled {
				if _, _, err := mcpSvc.SetEnabled(mcpruntime.DefaultExaServerID, false); err != nil {
					return tool.ExaRuntimeConfig{}, err
				}
				cfg.MCPURL = strings.TrimSpace(mcpConfig.URL)
			}
			return cfg, nil
		}
		if mcpConfig.Enabled {
			cfg.Enabled = true
			cfg.Source = "mcp"
			return cfg, nil
		}
		return cfg, nil
	})
	copilotManager := copilot.NewManager(authStore)
	providers := registry.New(
		anthropic.NewAdapter(authStore),
		copilot.NewAdapterWithManager(authStore, copilotManager),
		codex.NewAdapter(authStore),
		fireworks.NewAdapter(authStore),
		google.NewAdapter(authStore),
		openrouter.NewAdapter(authStore),
		exaprovider.NewAdapter(authStore, func(context.Context) (bool, error) {
			mcpConfig, err := mcpSvc.ResolveExaRuntimeConfig()
			if err != nil {
				return false, err
			}
			return mcpConfig.Enabled, nil
		}),
	)
	providers.RegisterRunner(anthropic.NewRunner(authStore))
	providers.RegisterRunner(copilot.NewRunnerWithManager(copilotManager))
	providers.RegisterRunner(codex.NewRunner(codexClient))
	providers.RegisterRunner(fireworks.NewRunner(authStore))
	providers.RegisterRunner(google.NewRunner(authStore))
	providers.RegisterRunner(openrouter.NewRunner(authStore))
	runSvc := run.NewService(sessionSvc, modelSvc, providers, toolRuntime, permissionSvc, agentSvc, discoverySvc, events)
	runSvc.SetWorkspaceService(workspaceSvc)
	runSvc.SetSandboxService(sandboxSvc)
	runSvc.SetWorktreeService(worktreeSvc)
	runSvc.SetEventPublisher(hub.Publish)

	if err := agentSvc.EnsureDefaults(); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed default agents: %w", err)
	}
	if err := modelSvc.EnsureBootDefaults(); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("load default model stack: %w", err)
	}
	if _, err := modelCatalog.Refresh(context.Background()); err != nil {
		log.Printf("warning: refresh model catalog: %v", err)
	}
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("ensure attach auth token: %w", err)
	}
	if err := mcpSvc.EnsureDefaults(); err != nil {
		_ = store.Close()
		_ = lk.Release()
		return nil, fmt.Errorf("seed mcp defaults: %w", err)
	}
	if err := runSvc.ReconcileActiveLifecycles("daemon restarted"); err != nil {
		log.Printf("warning: reconcile active session lifecycles: %v", err)
	}
	if err := permissionSvc.ReconcilePendingRuns("daemon restarted"); err != nil {
		log.Printf("warning: reconcile pending permissions: %v", err)
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	modelSvc.StartCatalogAutoRefresh(bgCtx)

	apiServer := api.NewServer(cfg.Mode, authSvc, agentSvc, modelSvc, runSvc, sessionSvc, workspaceSvc, discoverySvc, securitySvc, providers, permissionSvc, notificationSvc, events, hub)
	apiServer.SetBypassPermissions(cfg.BypassPermissions)
	apiServer.SetStartupConfigPath(cfg.ConfigPath)
	apiServer.SetSandboxService(sandboxSvc)
	apiServer.SetWorktreeService(worktreeSvc)
	apiServer.SetMCPService(mcpSvc)
	apiServer.SetVoiceService(voiceSvc)
	apiServer.SetUISettingsService(uiSettingsSvc)
	apiServer.SetSwarmDesktopTargetSelectionStore(swarmDesktopTargetSelectionStore)
	apiServer.SetSessionRouteStore(pebblestore.NewSessionRouteStore(store))
	apiServer.SetTodoService(todoSvc)
	apiServer.SetSwarmService(swarmSvc)
	apiServer.SetContainerProfileService(containerProfileSvc)
	apiServer.SetLocalContainerService(localContainerSvc)
	deployContainerSvc.SetHostCallbackURLResolver(localContainerSvc.HostCallbackURL)
	apiServer.SetDeployContainerService(deployContainerSvc)
	apiServer.SetRemoteDeployService(remoteDeploySvc)

	runtimeStatus, runtimeStatusErr := localContainerSvc.RuntimeStatus(context.Background())
	localTransportRuntimeName := ""
	if runtimeStatusErr != nil {
		log.Printf("warning: resolve local child transport runtime: %v", runtimeStatusErr)
	} else {
		localTransportRuntimeName = strings.TrimSpace(runtimeStatus.Recommended)
	}

	d := &Daemon{
		cfg:                       cfg,
		lock:                      lk,
		store:                     store,
		events:                    events,
		hub:                       hub,
		apiServer:                 apiServer,
		bgCtx:                     bgCtx,
		bgCancel:                  bgCancel,
		stopCh:                    make(chan string, 1),
		copilot:                   copilotManager,
		deployContainers:          deployContainerSvc,
		remoteDeploys:             remoteDeploySvc,
		localContainers:           localContainerSvc,
		localTransportRuntimeName: localTransportRuntimeName,
	}
	apiServer.SetShutdownHandler(func(reason string) {
		d.requestStop("api:" + strings.TrimSpace(reason))
	})

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiServer.Handler(),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Keep write timeout disabled so long-lived websocket/API streams are
		// not cut mid-event during multi-step turns.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	d.httpServer = httpServer
	if shouldEnableLocalTransport(cfg.ListenAddr) {
		localTransportSocketPath := filepath.Join(cfg.DataDir, "local-transport", "api.sock")
		if err := os.MkdirAll(filepath.Dir(localTransportSocketPath), 0o700); err != nil {
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
		deployContainerSvc.SetLocalTransportSocketPath(localTransportSocketPath)
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
	if cfg.DesktopPort > 0 {
		listenHost, _, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			return nil, fmt.Errorf("resolve desktop listen host from %q: %w", cfg.ListenAddr, err)
		}
		desktopMux := http.NewServeMux()
		desktopMux.Handle("/", apiServer.DesktopHandler())
		d.desktopServer = &http.Server{
			Addr:              net.JoinHostPort(listenHost, strconv.Itoa(cfg.DesktopPort)),
			Handler:           desktopMux,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       60 * time.Second,
		}
	}
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
		if d.bgCancel != nil {
			d.bgCancel()
			d.bgCancel = nil
		}
		if d.copilot != nil {
			if err := d.copilot.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close copilot manager: %w", err))
			}
			d.copilot = nil
		}
		if d.store != nil {
			if err := d.store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close store: %w", err))
			}
			d.store = nil
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
	log.Printf("swarmd listener topology api_listen=%q desktop_port=%d desktop_assets_enabled=%t desktop_assets_on_api=%t", d.httpServer.Addr, d.cfg.DesktopPort, strings.TrimSpace(os.Getenv("SWARM_WEB_DIST_DIR")) != "", false)

	d.serveDone = make(chan struct{})
	go func() {
		defer close(d.serveDone)
		if err := d.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("swarmd http serve error: %v", err)
			d.requestStop("http-serve-error")
		}
	}()
	if d.desktopServer != nil {
		desktopLn, err := net.Listen("tcp", d.desktopServer.Addr)
		if err != nil {
			_ = ln.Close()
			return err
		}
		d.desktopListener = desktopLn
		d.desktopServer.Addr = desktopLn.Addr().String()
		log.Printf("swarmd listener topology desktop_listen=%q desktop_assets_on_desktop=%t", d.desktopServer.Addr, true)
		d.desktopServeDone = make(chan struct{})
		go func() {
			defer close(d.desktopServeDone)
			if err := d.desktopServer.Serve(desktopLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("swarmd desktop serve error: %v", err)
				d.requestStop("desktop-serve-error")
			}
		}()
	}
	if d.localTransportServer != nil {
		socketPath := strings.TrimSpace(d.localTransportSocketPath)
		if socketPath == "" {
			return fmt.Errorf("local transport socket path is not configured")
		}
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
			return fmt.Errorf("create local transport directory: %w", err)
		}
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale local transport socket %q: %w", socketPath, err)
		}
		localTransportLn, err := net.Listen("unix", socketPath)
		if err != nil {
			return fmt.Errorf("listen on local transport socket %q: %w", socketPath, err)
		}
		if err := os.Chmod(socketPath, 0o600); err != nil {
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
	if d.deployContainers != nil {
		go func() {
			if err := d.deployContainers.AutoAttachChild(context.Background()); err != nil {
				log.Printf("warning: deploy child auto-attach failed: %v", err)
			}
		}()
		go func() {
			if err := d.deployContainers.RecoverLocalDeployments(context.Background()); err != nil {
				log.Printf("warning: deploy local recovery failed: %v", err)
			}
		}()
		go d.deployContainers.RunManagedCredentialSyncLoop(d.bgCtx)
	}
	return d.waitForShutdown()
}

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.httpServer.Shutdown(ctx); err != nil {
		return err
	}
	if d.desktopServer != nil {
		if err := d.desktopServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	if d.peerTransportServer != nil {
		if err := d.peerTransportServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	if d.localTransportServer != nil {
		if err := d.localTransportServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	if d.serveDone != nil {
		<-d.serveDone
	}
	if d.desktopServeDone != nil {
		<-d.desktopServeDone
	}
	if d.localTransportServeDone != nil {
		<-d.localTransportServeDone
	}
	if strings.TrimSpace(d.localTransportSocketPath) != "" {
		_ = os.Remove(d.localTransportSocketPath)
	}
	if d.peerTransportServeDone != nil {
		<-d.peerTransportServeDone
	}
	_ = reason
	return d.cleanup()
}

func shouldEnableLocalTransport(listenAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	return host != ""
}

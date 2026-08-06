package api

import (
	"path/filepath"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/uisettings"
)

func newRoutedSessionTestServerWithSwarmStore(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, any, any) {
	t.Helper()
	t.Setenv("SWARM_API_NO_AUTH", "1")
	t.Setenv("SWARM_V3_DIAGNOSTICS", "0")

	var server *Server
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-v3-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if server != nil {
			server.CancelInFlightRuns()
			server.WaitForInFlightRuns(2 * time.Second)
		}
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	runSvc := runruntime.NewService(sessionSvc, modelSvc, nil, tool.NewRuntime(1), permissionSvc, agentSvc, nil, nil)
	server = NewServer(nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, nil, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	uiSettingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	server.SetUISettingsService(uiSettingsSvc)
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentSettings := testAgentModelSettingsRecord(testPrincipal().AccountScopeID)
	if _, err := agentSettingsStore.PutForAccount(agentSettings); err != nil {
		t.Fatalf("seed canonical agent model settings: %v", err)
	}
	agentSettingsSvc := agentmodelsettings.NewService(agentSettingsStore)
	server.SetAgentModelSettingsService(agentSettingsSvc)
	runSvc.SetAgentModelSettingsService(agentSettingsSvc)
	server.v3SessionExecutor = nil
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
	server.SetSwarmStore(swarmStore)
	server.SetSwarmDesktopTargetSelectionStore(pebblestore.NewSwarmDesktopTargetSelectionStore(store))
	swarmSvc := swarmruntime.NewService(swarmStore, eventLog, nil)
	if _, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"}); err != nil {
		t.Fatalf("ensure local swarm: %v", err)
	}
	server.SetSwarmService(swarmSvc)
	server.SetStartupConfigPath(filepath.Join(t.TempDir(), "swarm.conf"))

	return server, sessionSvc, permissionSvc, nil, swarmStore
}
